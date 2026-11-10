package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Feed and timing defaults.
const (
	// DefaultFeedBase holds <channel>.json for each channel. The repository is public, so no credentials are sent.
	DefaultFeedBase = "https://github.com/adamveld12/hal9001/releases/download/channels"
	// EnvFeedURL overrides DefaultFeedBase (tests and the e2e harness).
	EnvFeedURL = "HAL9001_UPDATE_FEED_URL"
	// ReleasesURL is where a person downloads a build by hand.
	ReleasesURL = "https://github.com/adamveld12/hal9001/releases/latest"

	defaultStartDelay     = 15 * time.Second
	defaultCheckInterval  = 6 * time.Hour
	defaultIdlePoll       = 10 * time.Second
	defaultInstallTimeout = 10 * time.Minute
	defaultMinCheckGap    = 10 * time.Second
	maxManifestBytes      = 256 << 10
)

// Settings are the user's updater settings (config.yaml: update_channel, update_auto_install).
type Settings struct {
	Channel     string
	AutoInstall bool
}

// Options configure New.
type Options struct {
	// Current is the running build and Disabled the reason updates are off (empty: enabled); see Detect.
	Current  Current
	Disabled string
	// Settings returns the current settings; required.
	Settings func() Settings
	// Busy returns the number of flow runs that are running; nil means never busy.
	Busy func(ctx context.Context) (int, error)
	// FeedBase is the URL prefix of <channel>.json; default DefaultFeedBase.
	FeedBase string
	HTTP     *http.Client
	// MacOS is the running macOS version ("14.5"); empty skips the min_macos check.
	MacOS string
	Log   *slog.Logger
	Now   func() time.Time

	// Timings; zero means the default. A negative MinCheckGap disables the gap.
	StartDelay, CheckInterval, IdlePoll, InstallTimeout, MinCheckGap time.Duration
}

// Event is something the service wants announced on the stream.
type Event struct {
	Type string // EventStatus | EventInstall
	Data any
	// InstallerOnly events go to installer connections only.
	InstallerOnly bool
}

// Service is the daemon side of self-update: it checks the feed, tracks availability and the install request, decides
// when an install may start (no flow running) and tells the installer app. It never changes the bundle itself.
type Service struct {
	opts Options
	log  *slog.Logger
	cur  Current

	poke    chan struct{}
	checkMu sync.Mutex // serializes feed checks

	mu            sync.Mutex
	latest        *Manifest
	latestChannel string // channel latest was fetched from
	checking      bool
	lastChecked   time.Time
	checkErr      string
	pending       bool
	installing    bool
	phase         string
	lastReport    time.Time
	installFailed string
	attempted     int // build the auto-install already tried
	installers    int
	lastPublished []byte
	subsMu        sync.Mutex
	subs          map[int]func(Event)
	nextSub       int
}

// New returns a Service. Run drives its schedule.
func New(opts Options) *Service {
	if opts.Settings == nil {
		panic("update: Settings is required")
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.FeedBase == "" {
		opts.FeedBase = DefaultFeedBase
	}
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	for _, d := range []struct {
		v   *time.Duration
		def time.Duration
	}{{&opts.StartDelay, defaultStartDelay}, {&opts.CheckInterval, defaultCheckInterval}, {&opts.IdlePoll, defaultIdlePoll},
		{&opts.InstallTimeout, defaultInstallTimeout}, {&opts.MinCheckGap, defaultMinCheckGap}} {
		if *d.v == 0 {
			*d.v = d.def
		}
	}
	return &Service{opts: opts, log: opts.Log, cur: opts.Current, poke: make(chan struct{}, 1), subs: map[int]func(Event){}}
}

// Enabled reports whether updates are on for this build.
func (s *Service) Enabled() bool { return s.opts.Disabled == "" }

// Subscribe registers fn for the events the service emits and returns the function that removes it. fn runs on the
// goroutine that changed the state and must not block or call back into the service.
func (s *Service) Subscribe(fn func(Event)) (unsubscribe func()) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	id := s.nextSub
	s.nextSub++
	s.subs[id] = fn
	return func() {
		s.subsMu.Lock()
		delete(s.subs, id)
		s.subsMu.Unlock()
	}
}

func (s *Service) emit(ev Event) {
	s.subsMu.Lock()
	fns := make([]func(Event), 0, len(s.subs))
	for _, fn := range s.subs {
		fns = append(fns, fn)
	}
	s.subsMu.Unlock()
	for _, fn := range fns {
		fn(ev)
	}
}

// Poke asks the run loop to re-evaluate now (settings changed, a flow run finished, an installer connected).
func (s *Service) Poke() {
	select {
	case s.poke <- struct{}{}:
	default:
	}
}

// ConnectInstaller registers an installer (the app's stream connection). Call the returned function on disconnect.
// An install request that waits for flows to finish is dropped when the last installer goes away.
func (s *Service) ConnectInstaller(ctx context.Context) (release func()) {
	s.mu.Lock()
	s.installers++
	s.mu.Unlock()
	s.publishStatus(ctx)
	s.Poke()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.installers--
			if s.installers == 0 {
				s.pending = false
			}
			s.mu.Unlock()
			s.publishStatus(context.WithoutCancel(ctx))
		})
	}
}

func (s *Service) settings() Settings {
	set := s.opts.Settings()
	if !slices.Contains(Channels, set.Channel) {
		set.Channel = ChannelStable
	}
	return set
}

func (s *Service) measureBusy(ctx context.Context) int {
	if s.opts.Busy == nil {
		return 0
	}
	n, err := s.opts.Busy(ctx)
	if err != nil {
		s.log.Warn("update: cannot count running flow runs", "err", err)
		return 0
	}
	return n
}

// Status returns the current status.
func (s *Service) Status(ctx context.Context) Status {
	busy := s.measureBusy(ctx)
	set := s.settings()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked(set, busy)
}

func (s *Service) availableLocked(set Settings) bool {
	m := s.latest
	return s.Enabled() && m != nil && s.latestChannel == set.Channel && m.Build > s.cur.Build && s.macOSOK(*m)
}

func (s *Service) macOSOK(m Manifest) bool {
	return s.opts.MacOS == "" || m.MinMacOS == "" || CompareVersions(s.opts.MacOS, m.MinMacOS) >= 0
}

func (s *Service) statusLocked(set Settings, busy int) Status {
	st := Status{
		Enabled: s.Enabled(), Current: s.cur, Channel: set.Channel, AutoInstallWhenIdle: set.AutoInstall,
		Busy: Busy{RunningRuns: busy}, InstallerConnected: s.installers > 0,
	}
	if !st.Enabled {
		st.DisabledReason = new(s.opts.Disabled)
		st.State = StateDisabled
		return st
	}
	if s.latest != nil && s.latestChannel == set.Channel {
		st.Latest = releaseOf(*s.latest)
	}
	st.Available = s.availableLocked(set)
	if !s.lastChecked.IsZero() {
		t := s.lastChecked.UTC()
		st.LastCheckedAt = &t
	}
	st.Pending = s.pending
	switch {
	case s.installing:
		st.State = StateInstalling
		st.Phase = new(s.phase)
	case s.pending:
		st.State = StatePending
	case s.installFailed != "":
		st.State = StateError
	case s.checking && !st.Available:
		st.State = StateChecking
	case st.Available:
		st.State = StateAvailable
	case s.checkErr != "":
		st.State = StateError
	case s.lastChecked.IsZero():
		st.State = StateIdle
	default:
		st.State = StateUpToDate
	}
	switch {
	case s.installFailed != "":
		st.Error = new(s.installFailed)
	case s.checkErr != "":
		st.Error = new(s.checkErr)
	}
	return st
}

// publishStatus announces the status when it differs from the last one announced.
func (s *Service) publishStatus(ctx context.Context) {
	st := s.Status(ctx)
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	s.mu.Lock()
	same := bytes.Equal(b, s.lastPublished)
	s.lastPublished = b
	s.mu.Unlock()
	if !same {
		s.emit(Event{Type: EventStatus, Data: st})
	}
}

// Check fetches the channel's feed now and returns the new status. A failed fetch is reported in Status.Error, not as
// an error. Checks within MinCheckGap of the last successful one return the cached result.
func (s *Service) Check(ctx context.Context) Status {
	s.check(ctx, true)
	s.Poke()
	return s.Status(ctx)
}

func (s *Service) check(ctx context.Context, onDemand bool) {
	if !s.Enabled() {
		return
	}
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	set := s.settings()
	s.mu.Lock()
	if onDemand && s.opts.MinCheckGap > 0 && s.latestChannel == set.Channel && s.checkErr == "" &&
		!s.lastChecked.IsZero() && s.opts.Now().Sub(s.lastChecked) < s.opts.MinCheckGap {
		s.mu.Unlock()
		return
	}
	s.checking = true
	s.mu.Unlock()
	s.publishStatus(ctx)

	m, err := s.fetch(ctx, set.Channel)

	s.mu.Lock()
	s.checking = false
	s.lastChecked = s.opts.Now()
	if err != nil {
		s.checkErr = err.Error()
	} else {
		s.checkErr, s.latest, s.latestChannel = "", &m, set.Channel
	}
	s.mu.Unlock()
	if err != nil {
		s.log.Warn("update: check failed", "channel", set.Channel, "err", err)
	} else {
		s.log.Info("update: checked", "channel", set.Channel, "latest", m.Version, "build", m.Build, "current", s.cur.Build)
	}
	s.publishStatus(ctx)
}

func (s *Service) fetch(ctx context.Context, channel string) (Manifest, error) {
	u := strings.TrimRight(s.opts.FeedBase, "/") + "/" + channel + ".json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Manifest{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "hal9001/"+s.cur.Version)
	resp, err := s.opts.HTTP.Do(req)
	if err != nil {
		return Manifest{}, fmt.Errorf("cannot reach the update feed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Manifest{}, fmt.Errorf("no release is published on the %s channel yet", channel)
	case resp.StatusCode != http.StatusOK:
		return Manifest{}, fmt.Errorf("the update feed answered %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("cannot read the update feed: %w", err)
	}
	if len(data) > maxManifestBytes {
		return Manifest{}, errors.New("the update manifest is too large")
	}
	m, err := ParseManifest(data)
	if err != nil {
		return Manifest{}, err
	}
	if m.Channel != channel {
		return Manifest{}, fmt.Errorf("the %s feed describes a %s release", channel, m.Channel)
	}
	return m, nil
}

// Install reasons (InstallError.Reason).
const (
	ReasonDisabled     = "disabled"
	ReasonNotAvailable = "not_available"
	ReasonNoInstaller  = "no_installer"
)

// InstallError is a refused install request.
type InstallError struct {
	Reason string
	Detail string
	// ReleaseURL is where to download the build by hand.
	ReleaseURL string
}

func (e *InstallError) Error() string { return e.Detail }

// Install requests the install of the available update. Without force, while flow runs are running, the request is
// kept (state pending) and starts when none runs; with force, or when nothing runs, the installer app is told to
// install now. Requesting again while an install is running or pending is a no-op.
func (s *Service) Install(ctx context.Context, force bool) (Status, error) {
	if !s.Enabled() {
		return Status{}, &InstallError{Reason: ReasonDisabled, Detail: "updates are off: " + s.opts.Disabled, ReleaseURL: ReleasesURL}
	}
	busy := s.measureBusy(ctx)
	set := s.settings()

	s.mu.Lock()
	if s.installing {
		st := s.statusLocked(set, busy)
		s.mu.Unlock()
		return st, nil
	}
	if !s.availableLocked(set) {
		s.mu.Unlock()
		return Status{}, &InstallError{Reason: ReasonNotAvailable, Detail: "no update is available", ReleaseURL: ReleasesURL}
	}
	m := *s.latest
	if s.installers == 0 {
		s.mu.Unlock()
		url := m.NotesURL
		if url == "" {
			url = ReleasesURL
		}
		return Status{}, &InstallError{
			Reason:     ReasonNoInstaller,
			Detail:     fmt.Sprintf("Open the HAL9001 app to install this update, or download HAL9001 %s from %s", m.Version, url),
			ReleaseURL: url,
		}
	}
	s.installFailed = ""
	s.attempted = m.Build
	var fire *Manifest
	if busy > 0 && !force {
		s.pending = true
	} else {
		fire = s.startLocked()
	}
	st := s.statusLocked(set, busy)
	s.mu.Unlock()
	if fire != nil {
		s.dispatch(*fire)
	}
	s.publishStatus(ctx)
	return st, nil
}

// startLocked marks an install as started and returns the manifest to send; the caller dispatches it after unlocking.
func (s *Service) startLocked() *Manifest {
	m := *s.latest
	s.pending, s.installing, s.phase, s.installFailed = false, true, PhaseRequested, ""
	s.lastReport = s.opts.Now()
	return &m
}

func (s *Service) dispatch(m Manifest) {
	s.log.Info("update: asking the app to install", "version", m.Version, "build", m.Build)
	s.emit(Event{Type: EventInstall, Data: InstallEvent{Manifest: m}, InstallerOnly: true})
}

// ErrNotInstalling is returned by Report when no install is running.
var ErrNotInstalling = errors.New("no update install is in progress")

// Report records progress or failure sent by the installer app.
func (s *Service) Report(ctx context.Context, state, msg string) error {
	switch state {
	case PhaseDownloading, PhaseVerifying, PhaseInstalling, ReportFailed:
	default:
		return fmt.Errorf("unknown report state %q", state)
	}
	s.mu.Lock()
	if !s.installing {
		s.mu.Unlock()
		return ErrNotInstalling
	}
	if state == ReportFailed {
		s.installing, s.phase = false, ""
		s.installFailed = strings.TrimSpace(msg)
		if s.installFailed == "" {
			s.installFailed = "the update could not be installed"
		}
		s.log.Warn("update: the app reported a failed install", "error", s.installFailed)
	} else {
		s.phase = state
		s.lastReport = s.opts.Now()
	}
	s.mu.Unlock()
	s.publishStatus(ctx)
	return nil
}

// Run drives the schedule until ctx ends: a check shortly after start, one every CheckInterval, and an evaluation
// of the install policy whenever poked and every IdlePoll. It returns at once when updates are off.
func (s *Service) Run(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	start := time.NewTimer(s.opts.StartDelay)
	defer start.Stop()
	every := time.NewTicker(s.opts.CheckInterval)
	defer every.Stop()
	idle := time.NewTicker(s.opts.IdlePoll)
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-start.C:
			s.check(ctx, false)
		case <-every.C:
			s.check(ctx, false)
		case <-idle.C:
		case <-s.poke:
		}
		s.evaluate(ctx)
	}
}

// evaluate applies the install policy: a channel switch re-checks, a requested install starts once no flow runs, and
// with auto-install on an available update starts once no flow runs (once per build; a failure is not retried
// automatically).
func (s *Service) evaluate(ctx context.Context) {
	set := s.settings()
	s.mu.Lock()
	switched := s.latestChannel != "" && s.latestChannel != set.Channel
	s.mu.Unlock()
	if switched {
		s.check(ctx, false)
	}

	busy := s.measureBusy(ctx)
	s.mu.Lock()
	var fire *Manifest
	switch {
	case s.installing:
		if s.opts.Now().Sub(s.lastReport) > s.opts.InstallTimeout {
			s.installing, s.phase = false, ""
			s.installFailed = "the update did not finish; see ~/Library/Logs/hal9001/app.log"
			s.log.Warn("update: the install did not finish in time", "after", s.opts.InstallTimeout)
		}
	case !s.availableLocked(set):
		s.pending = false
	case s.installers > 0 && busy == 0 && s.pending:
		fire = s.startLocked()
	case s.installers > 0 && busy == 0 && set.AutoInstall && s.installFailed == "" && s.attempted != s.latest.Build:
		s.attempted = s.latest.Build
		fire = s.startLocked()
	}
	s.mu.Unlock()
	if fire != nil {
		s.dispatch(*fire)
	}
	s.publishStatus(ctx)
}

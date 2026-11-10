package update

import "time"

// State is the updater state reported by GET /api/update.
type State string

// Updater states.
const (
	StateDisabled   State = "disabled"   // not updatable (see DisabledReason)
	StateIdle       State = "idle"       // enabled, no check has finished yet
	StateChecking   State = "checking"   // a feed check is running and nothing is known yet
	StateUpToDate   State = "up_to_date" // the feed offers nothing newer
	StateAvailable  State = "available"  // a newer build is offered
	StatePending    State = "pending"    // an install was requested; it starts when no flow is running
	StateInstalling State = "installing" // the app is downloading, verifying or swapping the bundle
	StateError      State = "error"      // the last check or install failed (Error says why)
)

// Phases of an install, as reported by the app (POST /api/update/report) and shown in Status.Phase.
const (
	PhaseRequested   = "requested" // the daemon asked the app to install; nothing reported yet
	PhaseDownloading = "downloading"
	PhaseVerifying   = "verifying"
	PhaseInstalling  = "installing" // swapping the bundle and relaunching
	ReportFailed     = "failed"     // report state only: the install failed
)

// Stream headers and events of the updater (the macOS app connects with the header to execute installs).
const (
	// InstallerHeader marks a stream connection as the installer when its value is InstallerApp.
	InstallerHeader = "X-Hal9001-Updater"
	InstallerApp    = "app"
	// EventStatus carries a Status; every client may subscribe to it.
	EventStatus = "update.status"
	// EventInstall carries an InstallEvent and is delivered only to installer connections.
	EventInstall = "update.install"
)

// Channels of the update feed.
const (
	ChannelStable = "stable"
	ChannelBeta   = "beta"
)

// Channels lists the legal values of the update_channel setting.
var Channels = []string{ChannelStable, ChannelBeta}

// Busy is what keeps a normal install from starting.
type Busy struct {
	RunningRuns int `json:"running_runs"`
}

// Release is a Manifest as shown to clients: everything but the signature.
type Release struct {
	Schema      int       `json:"schema"`
	Channel     string    `json:"channel"`
	Version     string    `json:"version"`
	Build       int       `json:"build"`
	Commit      string    `json:"commit"`
	PublishedAt time.Time `json:"published_at"`
	MinMacOS    string    `json:"min_macos"`
	URL         string    `json:"url"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	NotesURL    string    `json:"notes_url"`
	Notes       string    `json:"notes"`
}

func releaseOf(m Manifest) *Release {
	return &Release{
		Schema: m.Schema, Channel: m.Channel, Version: m.Version, Build: m.Build, Commit: m.Commit,
		PublishedAt: m.PublishedAt.UTC(), MinMacOS: m.MinMacOS, URL: m.URL, Size: m.Size, SHA256: m.SHA256,
		NotesURL: m.NotesURL, Notes: m.Notes,
	}
}

// Status is the body of GET /api/update and the data of the update.status stream event.
type Status struct {
	Enabled        bool    `json:"enabled"`
	DisabledReason *string `json:"disabled_reason"`
	Current        Current `json:"current"`
	// Channel and AutoInstallWhenIdle are the settings update_channel and update_auto_install.
	Channel             string `json:"channel"`
	AutoInstallWhenIdle bool   `json:"auto_install_when_idle"`
	State               State  `json:"state"`
	// Latest is the newest release on the channel's feed; it is set even when nothing newer than Current is offered
	// (Available false). Nil before the first successful check.
	Latest *Release `json:"latest"`
	// Available: Latest is newer than Current and this macOS can run it.
	Available bool `json:"available"`
	// Pending: an install was requested while flows run; it starts when none runs.
	Pending bool `json:"pending"`
	// Phase is the install phase while State is installing (Phase* constants); null otherwise.
	Phase              *string    `json:"phase"`
	Busy               Busy       `json:"busy"`
	InstallerConnected bool       `json:"installer_connected"`
	LastCheckedAt      *time.Time `json:"last_checked_at"`
	// Error is the last check or install failure; null when there is none.
	Error *string `json:"error"`
}

// InstallEvent is the data of the update.install stream event: the full manifest, signature included, which the app
// verifies the downloaded archive against.
type InstallEvent struct {
	Manifest Manifest `json:"manifest"`
}

// InstallRequest is the body of POST /api/update/install.
type InstallRequest struct {
	// Force starts the install now even while flows run; the daemon restart interrupts them.
	Force bool `json:"force"`
}

// ReportRequest is the body of POST /api/update/report, sent by the installer app.
type ReportRequest struct {
	State string `json:"state"` // downloading | verifying | installing | failed
	Error string `json:"error,omitempty"`
}

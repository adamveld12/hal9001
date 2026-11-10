package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"hal9001/internal/update"
)

// fakeFeed serves <channel>.json like the release feed; a channel nobody published answers 404.
type fakeFeed struct {
	srv  *httptest.Server
	mu   sync.Mutex
	docs map[string][]byte
}

func newFakeFeed(t *testing.T) *fakeFeed {
	t.Helper()
	f := &fakeFeed{docs: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		doc, ok := f.docs[strings.TrimPrefix(r.URL.Path, "/")]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(doc)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// publish makes build the newest release of channel; mods adjust the manifest fields.
func (f *fakeFeed) publish(channel string, build int, mods ...func(map[string]any)) {
	m := map[string]any{
		"schema": 1, "channel": channel, "version": fmt.Sprintf("0.1.%d", build), "build": build, "commit": "abc123",
		"published_at": "2026-11-10T12:00:00Z", "min_macos": "13.0",
		"url":  fmt.Sprintf("https://example.com/HAL9001-0.1.%d.app.zip", build),
		"size": 1234, "sha256": strings.Repeat("a", 64), "signature": "c2lnbmF0dXJl",
		"notes_url": "https://example.com/notes", "notes": "fixes",
	}
	for _, mod := range mods {
		mod(m)
	}
	b, _ := json.Marshal(m)
	f.mu.Lock()
	f.docs[channel+".json"] = b
	f.mu.Unlock()
}

// updateEnv is an API env whose daemon runs build 5 of the release app, with a fake feed and a settable number of
// running flow runs. The update service's run loop is on, so settings changes and idle transitions take effect.
type updateEnv struct {
	*env
	feed *fakeFeed
	busy atomic.Int32
}

func newUpdateEnv(t *testing.T, mods ...func(*update.Options)) *updateEnv {
	t.Helper()
	u := &updateEnv{feed: newFakeFeed(t)}
	u.env = newEnv(t, func(eo *envOptions) {
		eo.update = func(e *env) update.Options {
			o := update.Options{
				Current: update.Current{BundleID: "dev.hal9001", Version: "0.1.5", Build: 5},
				Settings: func() update.Settings {
					c := e.ws.Load().Config.Value
					return update.Settings{Channel: c.UpdateChannel, AutoInstall: c.UpdateAutoInstall}
				},
				Busy:     func(context.Context) (int, error) { return int(u.busy.Load()), nil },
				FeedBase: u.feed.srv.URL, MacOS: "14.5",
				StartDelay: time.Hour, CheckInterval: time.Hour, IdlePoll: 20 * time.Millisecond, MinCheckGap: -1,
			}
			for _, m := range mods {
				m(&o)
			}
			return o
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); u.upd.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return u
}

func (u *updateEnv) status() update.Status {
	u.t.Helper()
	var st update.Status
	u.get("/api/update").want(200).decode(&st)
	return st
}

func (u *updateEnv) check() update.Status {
	u.t.Helper()
	var st update.Status
	u.do(http.MethodPost, "/api/update/check", nil).want(200).decode(&st)
	return st
}

// installer connects a stream the way the app does.
func (u *updateEnv) installer(ts *httptest.Server) *sseClient {
	u.t.Helper()
	c := u.openStream(ts, "?types=update.status,update.install", map[string]string{update.InstallerHeader: update.InstallerApp})
	c.expect("stream.ready")
	eventually(u.t, "the installer to be connected", func() bool { return u.status().InstallerConnected })
	return c
}

func (u *updateEnv) setSettings(patch map[string]any) {
	u.t.Helper()
	u.do(http.MethodPatch, "/api/settings", patch).want(200)
}

// noInstall fails if an update.install order arrives within a short window.
func noInstall(c *sseClient) {
	c.t.Helper()
	deadline := time.After(150 * time.Millisecond)
	for {
		select {
		case m := <-c.msgs:
			if m.Event == update.EventInstall {
				c.t.Fatalf("unexpected install order: %v", m.Data)
			}
		case <-deadline:
			return
		}
	}
}

func TestUpdateDisabledExplainsWhy(t *testing.T) {
	u := newUpdateEnv(t, func(o *update.Options) { o.Disabled = "local build (build number 0)" })
	st := u.status()
	if st.Enabled || st.State != update.StateDisabled || st.DisabledReason == nil || !strings.Contains(*st.DisabledReason, "local build") {
		t.Fatalf("status = %+v", st)
	}
	if st := u.check(); st.State != update.StateDisabled || st.LastCheckedAt != nil {
		t.Fatalf("a disabled updater must not check the feed: %+v", st)
	}
	r := u.do(http.MethodPost, "/api/update/install", "{}").want(409)
	if r.problem() != "invalid-state" || r.json()["current_state"] != "disabled" {
		t.Fatalf("install while disabled: %s", r.Body)
	}
}

func TestUpdateWithoutServiceIsDisabled(t *testing.T) {
	e := newEnv(t)
	var st update.Status
	e.get("/api/update").want(200).decode(&st)
	if st.Enabled || st.State != update.StateDisabled {
		t.Fatalf("status = %+v", st)
	}
	e.do(http.MethodPost, "/api/update/check", nil).want(503)
}

func TestUpdateOffersOnlyNewerBuildsThisMacCanRun(t *testing.T) {
	u := newUpdateEnv(t)
	if st := u.status(); st.State != update.StateIdle || st.Latest != nil || st.Channel != "stable" || st.Current.Build != 5 {
		t.Fatalf("before the first check: %+v", st)
	}
	for _, c := range []struct {
		name  string
		build int
		mods  []func(map[string]any)
		state update.State
	}{
		{"newer", 7, nil, update.StateAvailable},
		{"same build", 5, nil, update.StateUpToDate},
		{"older build (never a downgrade)", 3, nil, update.StateUpToDate},
		{"needs a newer macOS", 8, []func(map[string]any){func(m map[string]any) { m["min_macos"] = "15.0" }}, update.StateUpToDate},
	} {
		u.feed.publish("stable", c.build, c.mods...)
		st := u.check()
		if st.State != c.state || st.Available != (c.state == update.StateAvailable) || st.Latest == nil || st.Latest.Build != c.build {
			t.Errorf("%s: state %s available %v latest %+v, want state %s", c.name, st.State, st.Available, st.Latest, c.state)
		}
		if st.LastCheckedAt == nil || st.Error != nil {
			t.Errorf("%s: last_checked_at %v error %v", c.name, st.LastCheckedAt, st.Error)
		}
	}
}

func TestUpdateCheckFailureIsReportedNotRaised(t *testing.T) {
	u := newUpdateEnv(t)
	st := u.check() // nothing published: the feed answers 404
	if st.State != update.StateError || st.Error == nil || !strings.Contains(*st.Error, "stable") {
		t.Fatalf("status = %+v", st)
	}
	u.feed.publish("stable", 6)
	if st := u.check(); st.State != update.StateAvailable || st.Error != nil {
		t.Fatalf("a good check clears the error: %+v", st)
	}
}

func TestUpdateChannelSwitchRechecksTheNewFeed(t *testing.T) {
	u := newUpdateEnv(t)
	u.feed.publish("stable", 7)
	u.feed.publish("beta", 9)
	if st := u.check(); st.Latest == nil || st.Latest.Build != 7 {
		t.Fatalf("stable: %+v", st)
	}
	u.do(http.MethodPatch, "/api/settings", map[string]any{"update_channel": "nightly"}).want(422)
	u.setSettings(map[string]any{"update_channel": "beta"})
	eventually(t, "the beta feed to be checked", func() bool {
		st := u.status()
		return st.Channel == "beta" && st.Latest != nil && st.Latest.Build == 9 && st.Available
	})
	var s struct {
		Config map[string]any `json:"config"`
	}
	u.get("/api/settings").want(200).decode(&s)
	if s.Config["update_channel"] != "beta" || s.Config["update_auto_install"] != false {
		t.Fatalf("settings = %v", s.Config)
	}
}

func TestUpdateInstallRefusalsSayWhat(t *testing.T) {
	u := newUpdateEnv(t)
	u.feed.publish("stable", 5)
	u.check()
	r := u.do(http.MethodPost, "/api/update/install", "{}").want(409)
	if r.problem() != "invalid-state" || r.json()["current_state"] != "not_available" {
		t.Fatalf("nothing newer: %s", r.Body)
	}

	u.feed.publish("stable", 7)
	u.check()
	r = u.do(http.MethodPost, "/api/update/install", nil).want(409) // no installer connected (headless daemon)
	body := r.json()
	if r.problem() != "invalid-state" || body["current_state"] != "no_installer" || body["release_url"] != "https://example.com/notes" ||
		!strings.Contains(fmt.Sprint(body["detail"]), "Open the HAL9001 app") {
		t.Fatalf("no installer: %s", r.Body)
	}
	if st := u.status(); st.State != update.StateAvailable || st.InstallerConnected {
		t.Fatalf("a refused install changes nothing: %+v", st)
	}
}

func TestUpdateInstallOrderReachesOnlyTheInstaller(t *testing.T) {
	u := newUpdateEnv(t)
	u.feed.publish("stable", 7)
	u.check()
	ts := u.listen()
	installer := u.installer(ts)
	other := u.openStream(ts, "?types=update.status,update.install", nil)
	other.expect("stream.ready")

	var st update.Status
	u.do(http.MethodPost, "/api/update/install", "{}").want(202).decode(&st)
	if st.State != update.StateInstalling || st.Phase == nil || *st.Phase != update.PhaseRequested || st.Pending {
		t.Fatalf("install: %+v", st)
	}
	order := installer.expectUntil("update.install")
	if !conforms("StreamEvent", order.Data) {
		t.Errorf("update.install does not match the contract: %v", order.Data)
	}
	m, _ := order.data()["manifest"].(map[string]any)
	if m["build"] != float64(7) || m["signature"] != "c2lnbmF0dXJl" || m["url"] != "https://example.com/HAL9001-0.1.7.app.zip" {
		t.Fatalf("manifest = %v", m)
	}
	for { // every subscriber sees the state change; only the installer gets the order
		msg := other.next()
		if msg.Event == update.EventInstall {
			t.Fatalf("a non-installer received the install order: %v", msg.Data)
		}
		if msg.Event == update.EventStatus && msg.data()["state"] == "installing" {
			if !conforms("StreamEvent", msg.Data) {
				t.Errorf("update.status does not match the contract: %v", msg.Data)
			}
			break
		}
	}

	// A second request while installing is a no-op, not a second order.
	u.do(http.MethodPost, "/api/update/install", "{}").want(202)
	noInstall(installer)
}

func TestUpdateReportsDriveTheInstallState(t *testing.T) {
	u := newUpdateEnv(t)
	u.feed.publish("stable", 7)
	u.check()
	u.do(http.MethodPost, "/api/update/report", map[string]any{"state": "downloading"}).want(409)

	ts := u.listen()
	installer := u.installer(ts)
	u.do(http.MethodPost, "/api/update/install", "{}").want(202)
	installer.expectUntil("update.install")

	u.do(http.MethodPost, "/api/update/report", map[string]any{"state": "reticulating"}).want(422)
	for _, phase := range []string{"downloading", "verifying", "installing"} {
		var st update.Status
		u.do(http.MethodPost, "/api/update/report", map[string]any{"state": phase}).want(200).decode(&st)
		if st.State != update.StateInstalling || st.Phase == nil || *st.Phase != phase {
			t.Fatalf("after %s: %+v", phase, st)
		}
	}
	var st update.Status
	u.do(http.MethodPost, "/api/update/report", map[string]any{"state": "failed", "error": "HAL9001 is not in a writable folder"}).want(200).decode(&st)
	if st.State != update.StateError || st.Phase != nil || !st.Available || st.Error == nil || !strings.Contains(*st.Error, "writable") {
		t.Fatalf("after failed: %+v", st)
	}
	noInstall(installer) // a failed install is not retried by itself

	u.do(http.MethodPost, "/api/update/install", "{}").want(202) // the user can try again
	installer.expectUntil("update.install")
	if st := u.status(); st.State != update.StateInstalling || st.Error != nil {
		t.Fatalf("retry: %+v", st)
	}
}

func TestUpdateInstallWaitsForRunningFlows(t *testing.T) {
	u := newUpdateEnv(t)
	u.feed.publish("stable", 7)
	u.check()
	installer := u.installer(u.listen())

	u.busy.Store(2)
	var st update.Status
	u.do(http.MethodPost, "/api/update/install", "{}").want(202).decode(&st)
	if st.State != update.StatePending || !st.Pending || st.Busy.RunningRuns != 2 {
		t.Fatalf("while flows run: %+v", st)
	}
	noInstall(installer)
	if st := u.status(); st.State != update.StatePending {
		t.Fatalf("still waiting: %+v", st)
	}

	u.busy.Store(0) // the last flow finished
	installer.expectUntil("update.install")
	if st := u.status(); st.State != update.StateInstalling || st.Pending {
		t.Fatalf("after the flows finished: %+v", st)
	}
}

func TestUpdateForceInstallsWhileFlowsRun(t *testing.T) {
	u := newUpdateEnv(t)
	u.feed.publish("stable", 7)
	u.check()
	installer := u.installer(u.listen())

	u.busy.Store(3)
	var st update.Status
	u.do(http.MethodPost, "/api/update/install", map[string]any{"force": true}).want(202).decode(&st)
	if st.State != update.StateInstalling || st.Pending {
		t.Fatalf("force: %+v", st)
	}
	installer.expectUntil("update.install")
}

func TestUpdatePendingInstallIsDroppedWhenTheInstallerLeaves(t *testing.T) {
	u := newUpdateEnv(t)
	u.feed.publish("stable", 7)
	u.check()
	installer := u.installer(u.listen())
	u.busy.Store(1)
	u.do(http.MethodPost, "/api/update/install", "{}").want(202)

	_ = installer.body.Close() // the app quit
	eventually(t, "the pending install to be dropped", func() bool {
		st := u.status()
		return !st.Pending && !st.InstallerConnected && st.State == update.StateAvailable
	})
}

func TestUpdateAutoInstallWhenIdle(t *testing.T) {
	u := newUpdateEnv(t)
	u.feed.publish("stable", 7)
	u.check()
	installer := u.installer(u.listen())

	noInstall(installer) // off by default: an available update is only offered
	if st := u.status(); st.State != update.StateAvailable || st.AutoInstallWhenIdle {
		t.Fatalf("auto-install off: %+v", st)
	}

	u.busy.Store(1)
	u.setSettings(map[string]any{"update_auto_install": true})
	noInstall(installer) // on, but a flow is running
	u.busy.Store(0)
	installer.expectUntil("update.install")
	if st := u.status(); st.State != update.StateInstalling || !st.AutoInstallWhenIdle {
		t.Fatalf("auto-install on: %+v", st)
	}

	u.do(http.MethodPost, "/api/update/report", map[string]any{"state": "failed", "error": "download failed"}).want(200)
	noInstall(installer) // one automatic try per build
}

func TestUpdateAutoInstallNeedsTheInstallerApp(t *testing.T) {
	u := newUpdateEnv(t)
	u.feed.publish("stable", 7)
	u.setSettings(map[string]any{"update_auto_install": true})
	u.check()
	time.Sleep(100 * time.Millisecond) // several idle polls
	if st := u.status(); st.State != update.StateAvailable {
		t.Fatalf("headless daemon must not start an install: %+v", st)
	}
	installer := u.installer(u.listen()) // the app starts later: the waiting update goes out
	installer.expectUntil("update.install")
}

//go:build darwin

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"hal9001/internal/menubar"
	"hal9001/internal/service"
	"hal9001/internal/update"
)

// None of these tests may reach cgo code (alerts, SMAppService, launchctl) or start anything on this machine: they
// run the parts of the app that decide, against a fake daemon and temp directories.

func TestInstallRootIsTheReleaseAppsOwnBundle(t *testing.T) {
	dir := t.TempDir() // the paths are only parsed, never created
	exe := func(app string) string { return filepath.Join(dir, app, "Contents", "MacOS", "HAL9001") }
	for _, tc := range []struct {
		name string
		p    service.Profile
		exe  string
		want string
	}{
		{"the release app", service.Profile{BundleID: service.ReleaseBundleID}, exe("HAL9001.app"), filepath.Join(dir, "HAL9001.app")},
		{"a renamed copy of it", service.Profile{BundleID: service.ReleaseBundleID}, exe("HAL9001 2.app"), filepath.Join(dir, "HAL9001 2.app")},
		{"the dev app", service.Profile{BundleID: "dev.hal9001.dev"}, exe("HAL9001 Dev.app"), ""},
		{"a binary run from source", service.Profile{}, filepath.Join(dir, "hal9001-app"), ""},
		{"the release id on a binary outside a bundle", service.Profile{BundleID: service.ReleaseBundleID}, filepath.Join(dir, "hal9001-app"), ""},
	} {
		if got := installRoot(tc.p, tc.exe); got != tc.want {
			t.Errorf("%s: installRoot = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRelaunchWaitsForTheOldProcessThenOpensTheBundle(t *testing.T) {
	cmd := relaunchCmd(4242, "/Users/me/Applications/HAL9001 2.app")
	want := []string{"/bin/sh", "-c", relaunchScript, "sh", "4242", "/Users/me/Applications/HAL9001 2.app"}
	if !slices.Equal(cmd.Args, want) {
		t.Fatalf("args %q, want %q", cmd.Args, want)
	}
	for _, part := range []string{`kill -0 "$1"`, `exec /usr/bin/open "$2"`} {
		if !strings.Contains(relaunchScript, part) {
			t.Errorf("the script must contain %s: %s", part, relaunchScript)
		}
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Error("the helper must run in its own session so that it outlives the app")
	}
	if cmd.Stdin != nil || cmd.Stdout != nil || cmd.Stderr != nil {
		t.Error("stdio must stay nil, which is the null device")
	}
	if cmd.Process != nil {
		t.Error("building the command must not start it")
	}
}

func TestRetryEveryRetriesUntilAnAttemptSucceeds(t *testing.T) {
	refused := errors.New("launchd refused")
	calls := 0
	err := retryEvery(context.Background(), time.Millisecond, time.Minute, refused, func() (bool, error) {
		calls++
		if calls < 3 {
			return false, refused
		}
		return true, nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err %v after %d attempts, want success on the third", err, calls)
	}
}

func TestRetryEveryGivesUpWhenTheWindowIsUsedUp(t *testing.T) {
	first, last := errors.New("first failure"), errors.New("last failure")
	calls := 0
	start := time.Now()
	err := retryEvery(context.Background(), 10*time.Millisecond, 55*time.Millisecond, first, func() (bool, error) {
		calls++
		return false, last
	})
	if !errors.Is(err, last) || calls == 0 || calls > 5 || time.Since(start) > 5*time.Second {
		t.Fatalf("err %v after %d attempts in %v: want the last attempt's error, at most 5 attempts in the window", err, calls, time.Since(start))
	}

	// With no room for one interval nothing is tried, and the failure that started the retries is what remains.
	calls = 0
	if err := retryEvery(context.Background(), time.Hour, time.Minute, first, func() (bool, error) { calls++; return true, nil }); !errors.Is(err, first) || calls != 0 {
		t.Fatalf("err %v after %d attempts", err, calls)
	}
}

func TestRetryEveryStopsWhenTheContextEnds(t *testing.T) {
	first, last := errors.New("first failure"), errors.New("last failure")
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	done := make(chan error, 1)
	go func() {
		done <- retryEvery(ctx, 5*time.Millisecond, time.Hour, first, func() (bool, error) {
			calls++
			if calls == 2 {
				cancel()
			}
			return false, last
		})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, last) || calls != 2 {
			t.Fatalf("err %v after %d attempts", err, calls)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retryEvery ignored the cancelled context")
	}
}

func TestRetryEveryReturnsWhyAnAttemptStopped(t *testing.T) {
	first, taken := errors.New("first failure"), errors.New("another agent holds the port")
	if err := retryEvery(context.Background(), time.Millisecond, time.Minute, first, func() (bool, error) { return true, taken }); !errors.Is(err, taken) {
		t.Errorf("err %v, want the reason the attempt gave", err)
	}
	if err := retryEvery(context.Background(), time.Millisecond, time.Minute, first, func() (bool, error) { return true, nil }); err != nil {
		t.Errorf("err %v, want nil: the earlier failure no longer matters once an attempt is done", err)
	}
}

// fakeUpdateDaemon records the reports the app sends.
type fakeUpdateDaemon struct {
	mu      sync.Mutex
	reports []update.ReportRequest
	got     chan struct{}
}

func (f *fakeUpdateDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && r.URL.Path == "/api/update/report" {
		var req update.ReportRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.reports = append(f.reports, req)
		f.mu.Unlock()
		select {
		case f.got <- struct{}{}:
		default:
		}
		_, _ = w.Write([]byte(`{}`))
		return
	}
	http.NotFound(w, r)
}

func (f *fakeUpdateDaemon) snapshot() []update.ReportRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reports)
}

// testApp is an app whose daemon is f and whose files are in temp directories.
func testApp(t *testing.T, f *fakeUpdateDaemon) *app {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &app{
		client: menubar.NewClient(srv.URL), userHome: t.TempDir(), ctx: ctx, cancel: cancel, poke: func() {},
		installRoot: filepath.Join(t.TempDir(), "HAL9001.app"), // does not exist: an install of it cannot get far
	}
}

func (f *fakeUpdateDaemon) waitForReport(t *testing.T) update.ReportRequest {
	t.Helper()
	select {
	case <-f.got:
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon heard nothing")
	}
	reports := f.snapshot()
	return reports[len(reports)-1]
}

func installOrder(build int) menubar.Event {
	data, _ := json.Marshal(map[string]any{"type": "update.install", "id": "1", "data": update.InstallEvent{Manifest: update.Manifest{Build: build, Version: "0.2.0", URL: "https://example.test/HAL9001.zip", Size: 10}}})
	return menubar.Event{Type: menubar.EventUpdateInstall, Data: data}
}

func TestAnInstallThatCannotRunIsReportedFailedAndTheMenuReleased(t *testing.T) {
	f := &fakeUpdateDaemon{got: make(chan struct{}, 8)}
	a := testApp(t, f)
	a.onUpdateInstall(installOrder(7))

	var report update.ReportRequest
	for report.State != update.ReportFailed { // progress reports may come first
		report = f.waitForReport(t)
	}
	if report.Error == "" {
		t.Fatal("a failure must say why")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var st menubar.ServiceState
		a.updateState(&st)
		if !st.UpdateInstalling {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the menu still says Installing update after the failure")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestARepeatedOrderWhileOneRunsIsIgnored(t *testing.T) {
	f := &fakeUpdateDaemon{got: make(chan struct{}, 8)}
	a := testApp(t, f)
	a.setUpdate(true, "downloading 40%")
	a.onUpdateInstall(installOrder(7))
	time.Sleep(50 * time.Millisecond)
	if r := f.snapshot(); len(r) != 0 {
		t.Fatalf("a second order started something: %+v", r)
	}
	var st menubar.ServiceState
	a.updateState(&st)
	if !st.UpdateInstalling || st.UpdatePhase != "downloading 40%" {
		t.Fatalf("the running install was disturbed: %+v", st)
	}
}

func TestAnOrderThisAppCannotExecuteIsRefusedAtOnce(t *testing.T) {
	f := &fakeUpdateDaemon{got: make(chan struct{}, 8)}
	a := testApp(t, f)
	a.installRoot = ""
	a.onUpdateInstall(installOrder(7))
	if r := f.waitForReport(t); r.State != update.ReportFailed || !strings.Contains(r.Error, "cannot install") {
		t.Fatalf("report %+v", r)
	}
	var st menubar.ServiceState
	a.updateState(&st)
	if st.UpdateInstalling {
		t.Fatal("nothing ran")
	}
}

func TestAnUnreadableOrderIsReportedFailed(t *testing.T) {
	f := &fakeUpdateDaemon{got: make(chan struct{}, 8)}
	a := testApp(t, f)
	a.onUpdateInstall(menubar.Event{Type: menubar.EventUpdateInstall, Data: []byte(`{"data":"nope"}`)})
	if r := f.waitForReport(t); r.State != update.ReportFailed || !strings.Contains(r.Error, "could not be read") {
		t.Fatalf("report %+v", r)
	}
}

func TestUpdateStateReachesTheMenuState(t *testing.T) {
	a := &app{}
	a.setUpdate(true, "verifying")
	a.setServiceUpdating(true)
	var st menubar.ServiceState
	a.updateState(&st)
	if !st.UpdateInstalling || st.UpdatePhase != "verifying" || !st.ServiceUpdating {
		t.Fatalf("state %+v", st)
	}
	a.setUpdate(false, "")
	a.setServiceUpdating(false)
	st = menubar.ServiceState{}
	a.updateState(&st)
	if st.UpdateInstalling || st.UpdatePhase != "" || st.ServiceUpdating {
		t.Fatalf("state %+v", st)
	}
}

func TestProgressShowsInTheMenuAndGoesToTheDaemon(t *testing.T) {
	f := &fakeUpdateDaemon{got: make(chan struct{}, 8)}
	a := testApp(t, f)
	a.reportProgress(update.PhaseDownloading, "40%")
	var st menubar.ServiceState
	a.updateState(&st)
	if !st.UpdateInstalling || st.UpdatePhase != "downloading 40%" {
		t.Fatalf("state %+v", st)
	}
	if r := f.waitForReport(t); r.State != "downloading" || r.Error != "" {
		t.Fatalf("report %+v: the percentage is for the menu, the daemon gets the phase", r)
	}
	a.reportProgress(update.PhaseVerifying, "")
	if r := f.waitForReport(t); r.State != "verifying" {
		t.Fatalf("report %+v", r)
	}
	a.updateState(&st)
	if st.UpdatePhase != "verifying" {
		t.Fatalf("phase %q", st.UpdatePhase)
	}
}

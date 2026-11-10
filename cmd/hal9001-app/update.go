//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"fyne.io/systray"

	"hal9001/internal/menubar"
	"hal9001/internal/service"
	"hal9001/internal/update"
)

// reportTimeout bounds one progress report to the daemon; a daemon that does not answer must not hold the install up.
const reportTimeout = 5 * time.Second

// installRoot is the app bundle this process replaces when an update is installed. Only the release app is the
// installer (a dev build has its own bundle id and daemon, and an update would swap the wrong app); it is empty for
// every other build, and for a binary that does not run from an app bundle.
func installRoot(p service.Profile, exe string) string {
	if p.BundleID != service.ReleaseBundleID {
		return ""
	}
	root, err := update.BundleRoot(exe)
	if err != nil {
		return ""
	}
	return root
}

// onUpdateInstall starts the install the daemon asked for with an update.install event, which only the installer
// connection receives.
func (a *app) onUpdateInstall(ev menubar.Event) {
	var inst update.InstallEvent
	if err := ev.Payload(&inst); err != nil {
		slog.Error("unreadable update.install event", "err", err)
		a.reportUpdate(update.ReportFailed, "the update request could not be read: "+err.Error())
		return
	}
	a.startInstall(inst.Manifest)
}

// startInstall runs the install in the background, one at a time: the daemon sends the order once, but a second
// one (a reconnect, a retried request) while this one runs must not start another download.
func (a *app) startInstall(m update.Manifest) {
	if a.installRoot == "" { // only the installer connection gets the order, so this is a bug, but say so
		a.reportUpdate(update.ReportFailed, "this app cannot install updates")
		return
	}
	a.updMu.Lock()
	if a.updRunning {
		a.updMu.Unlock()
		slog.Info("an update install is already running; ignoring the repeated request", "version", m.Version, "build", m.Build)
		return
	}
	a.updRunning, a.updPhase = true, ""
	a.updMu.Unlock()
	a.poke()
	go a.runInstall(m)
}

// runInstall downloads and swaps in the update m and relaunches the app. On failure it tells the daemon, which shows
// the error in the menu through update.status; only a folder the app cannot write to also gets an alert, because
// then the user has to act.
func (a *app) runInstall(m update.Manifest) {
	slog.Info("installing update", "from", version, "version", m.Version, "build", m.Build)
	if err := a.applyUpdate(m); err != nil {
		slog.Error("update failed", "version", m.Version, "build", m.Build, "err", err)
		a.reportUpdate(update.ReportFailed, err.Error())
		a.setUpdate(false, "")
		a.poke()
		if errors.Is(err, update.ErrInstallDirNotWritable) {
			alert("HAL9001 could not install the update", err.Error(), "OK", "")
		}
		return
	}
	// Nothing more is reported: the daemon never hears "done", the old daemon is stopped by the new app.
	slog.Info("update installed; restarting into it", "from", version, "version", m.Version, "build", m.Build)
	a.relaunch()
}

// applyUpdate runs update.Apply on the running bundle.
func (a *app) applyUpdate(m update.Manifest) error {
	pub, err := update.PublicKey()
	if err != nil {
		return fmt.Errorf("this build cannot verify updates, download HAL9001 %s from %s: %w", m.Version, update.ReleasesURL, err)
	}
	cfg := update.ApplyConfig{
		InstalledBundle: a.installRoot,
		CacheDir:        filepath.Join(a.userHome, "Library", "Caches", a.profile.BundleID, "updates"),
		PublicKey:       pub,
	}
	return update.Apply(a.ctx, cfg, m, a.reportProgress)
}

// reportProgress shows how far the install is in the menu and tells the daemon.
func (a *app) reportProgress(state, msg string) {
	phase := state
	if msg != "" {
		phase += " " + msg
	}
	a.setUpdate(true, phase)
	a.poke()
	a.reportUpdate(state, "")
}

// reportUpdate tells the daemon about the install. The daemon is only informed: a failed report is logged and the
// install goes on.
func (a *app) reportUpdate(state, msg string) {
	ctx, cancel := context.WithTimeout(a.ctx, reportTimeout)
	defer cancel()
	if err := a.client.ReportUpdate(ctx, state, msg); err != nil {
		slog.Warn("could not report the update to the daemon", "state", state, "err", err)
	}
}

// setUpdate records whether this app is executing an install and how far it is, for the menu.
func (a *app) setUpdate(running bool, phase string) {
	a.updMu.Lock()
	defer a.updMu.Unlock()
	a.updRunning, a.updPhase = running, phase
}

// setServiceUpdating records that the service is being registered again after an app update, for the menu.
func (a *app) setServiceUpdating(on bool) {
	a.updMu.Lock()
	defer a.updMu.Unlock()
	a.serviceUpdating = on
}

// updateState adds the update parts of the menu state.
func (a *app) updateState(st *menubar.ServiceState) {
	a.updMu.Lock()
	defer a.updMu.Unlock()
	st.UpdateInstalling, st.UpdatePhase, st.ServiceUpdating = a.updRunning, a.updPhase, a.serviceUpdating
}

// requestInstall is the menu's Install & Restart: it asks the daemon, which tells this app to install now or once
// no flow runs. The menu shows which of the two happened, so success needs no message.
func (a *app) requestInstall() {
	err := a.client.InstallUpdate(a.ctx)
	var refused *menubar.APIError
	switch {
	case err == nil:
	case errors.As(err, &refused) && refused.Status == http.StatusConflict:
		slog.Warn("the daemon refused the update install", "detail", refused.Detail)
		alert("HAL9001 cannot install the update", refused.Detail, "OK", "")
	default:
		a.fail("HAL9001 could not start the update", err)
	}
}

// relaunchScript waits until the old process is gone, then opens the new app. It cannot open the app while this
// one is still running: LaunchServices would only bring the running instance forward.
const relaunchScript = `while kill -0 "$1" 2>/dev/null; do sleep 0.2; done; exec /usr/bin/open "$2"`

// relaunchCmd is the helper that opens bundle once the process pid has exited. It runs in a session of its own so
// that it outlives this process, and its stdio is the null device (nil). It is not started here.
func relaunchCmd(pid int, bundle string) *exec.Cmd {
	// Not tied to a context: it must keep running after the app, and the app's context ends with it.
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", relaunchScript, "sh", strconv.Itoa(pid), bundle) //nolint:gosec // fixed script; the arguments are this process's pid and its own bundle path
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}

// relaunch starts the helper and quits; the helper opens the updated app when this process is gone. If the helper
// cannot start, the app keeps running (the old code, which is still in memory) and tells the user to restart it.
func (a *app) relaunch() {
	if err := relaunchCmd(os.Getpid(), a.installRoot).Start(); err != nil {
		slog.Error("cannot restart the updated app", "err", err)
		a.setUpdate(false, "")
		a.poke()
		alert("HAL9001 was updated but could not restart", "Quit HAL9001 and open it again to finish the update.\n\n"+err.Error(), "OK", "")
		return
	}
	systray.Quit()
}

//go:build darwin

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"hal9001/internal/menubar"
	"hal9001/internal/runner"
	"hal9001/internal/service"
)

func runLaunchctl(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "launchctl", args...).CombinedOutput() //nolint:gosec // fixed binary; args are built by this package
}

// found reports the headless and older agents that compete with this app's service.
func (a *app) found() service.Found {
	f, err := service.Detect(a.userHome)
	if err != nil {
		slog.Warn("cannot look for other launch agents", "err", err)
	}
	return a.profile.Relevant(f)
}

// serviceState is what the menu shows about the service and the app's login item.
func (a *app) serviceState() menubar.ServiceState {
	st := menubar.ServiceState{AppVersion: version, Home: a.profile.HomeDir(a.userHome), UserHome: a.userHome}
	if !a.profile.Bundled() {
		return st
	}
	found := a.found()
	st.Bundled = true
	st.Headless, st.Legacy = found.Headless, len(found.Legacy) > 0
	st.Managed = !found.Conflicts()
	switch smStatusOf(smAgent, a.profile.PlistName()) {
	case smEnabled:
		st.Registered = true
	case smRequiresApproval:
		st.Registered, st.NeedsApproval = true, true
	}
	st.LaunchAtLogin = smStatusOf(smMainApp, "") == smEnabled
	return st
}

// registrationFile records the digest of the daemon binary the bundled agent was last registered with. Its absence
// means the app never registered its service (first launch). launchd pins a registered agent to the cdhash of its
// binary, so after the app is replaced by a new build the old registration fails to start (EX_CONFIG) until it is
// registered again; the digest tells the app when that is due.
func (a *app) registrationFile() string {
	return filepath.Join(a.userHome, "Library", "Application Support", a.profile.BundleID, "service-registration")
}

// daemonBinary is the hal9001 binary inside this app bundle, the program of the bundled agent.
func (a *app) daemonBinary() string {
	return filepath.Join(filepath.Dir(a.exe), "..", "Resources", "hal9001")
}

func (a *app) daemonDigest() (string, error) {
	f, err := os.Open(a.daemonBinary())
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (a *app) recordRegistration() {
	digest, err := a.daemonDigest()
	if err == nil {
		err = os.MkdirAll(filepath.Dir(a.registrationFile()), 0o750)
	}
	if err == nil {
		err = os.WriteFile(a.registrationFile(), []byte(digest+"\n"), 0o600)
	}
	if err != nil {
		slog.Warn("cannot record the service registration", "err", err)
	}
}

// ensureService runs at launch. The first time, it registers the daemon service (which launchd starts at once); when
// the app was replaced by a new build, it registers the service again so that launchd runs the new daemon binary.
// A service the user stopped stays stopped. Nothing happens while a headless service or an older agent holds the
// daemon's port; the menu then explains what to do, and the next launch tries again.
func (a *app) ensureService(ctx context.Context) {
	if !a.profile.Bundled() {
		return
	}
	if found := a.found(); found.Conflicts() {
		slog.Info("not registering the service: another launch agent is installed", "headless", found.Headless, "legacy", len(found.Legacy))
		return
	}
	a.actMu.Lock()
	defer a.actMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	recorded, err := os.ReadFile(a.registrationFile())
	first := errors.Is(err, fs.ErrNotExist)
	st := smStatusOf(smAgent, a.profile.PlistName())
	registered := st == smEnabled || st == smRequiresApproval
	current, _ := a.daemonDigest()
	switch {
	case first && !registered:
		slog.Info("first launch: registering the service")
	case registered && strings.TrimSpace(string(recorded)) != current:
		slog.Info("the app was updated: registering the service again", "status", st.String())
		if st, err := smUnregister(smAgent, a.profile.PlistName()); err != nil {
			slog.Warn("unregistering the outdated service failed", "status", st.String(), "err", err)
		}
	default:
		return
	}
	if err := a.registerService(); err != nil {
		// No alert: this runs at every launch. The menu shows the daemon as stopped or not responding, and Start
		// service reports the same error when the user asks for it.
		slog.Error("could not start the service at launch", "err", err)
	}
	a.poke()
}

// runAction executes the menu action id, one at a time, and refreshes the menu afterwards.
func (a *app) runAction(id string) {
	a.actMu.Lock()
	defer a.actMu.Unlock()
	defer a.poke()
	switch id {
	case menubar.ActionServiceStart:
		if err := a.registerService(); err != nil {
			a.fail("HAL9001 could not start its service", err)
		}
	case menubar.ActionServiceStop:
		st, err := smUnregister(smAgent, a.profile.PlistName())
		slog.Info("unregistered service", "label", a.profile.Label(), "status", st.String(), "err", err)
		if err != nil && st != smNotRegistered {
			a.fail("HAL9001 could not stop its service", err)
		}
	case menubar.ActionServiceRestart:
		out, err := a.launchctl(a.ctx, "kickstart", "-k", service.Target(os.Getuid(), a.profile.Label()))
		slog.Info("restarted service", "label", a.profile.Label(), "err", err, "output", strings.TrimSpace(string(out)))
		if err != nil {
			a.fail("HAL9001 could not restart its service", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out))))
		}
	case menubar.ActionOpenLogs:
		a.openLogs()
	case menubar.ActionLaunchAtLogin:
		a.toggleLaunchAtLogin()
	case menubar.ActionApproveLogin:
		openLoginItems()
	case menubar.ActionInstallCLI:
		a.installCLI()
	case menubar.ActionRemoveLegacy:
		a.removeLegacy()
	default:
		slog.Error("unknown menu action", "id", id)
	}
}

// pause sleeps for d or until the app quits.
func (a *app) pause(d time.Duration) {
	select {
	case <-a.ctx.Done():
	case <-time.After(d):
	}
}

// waitJobGone waits until launchd no longer knows the service, so that registering does not race the teardown of the
// previous instance: unregistering stops the daemon, which takes a few seconds to shut down.
func (a *app) waitJobGone(timeout time.Duration) {
	target := service.Target(os.Getuid(), a.profile.Label())
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) && a.ctx.Err() == nil {
		if _, err := a.launchctl(a.ctx, "print", target); err != nil {
			return
		}
		a.pause(100 * time.Millisecond)
	}
	slog.Warn("the previous service instance is still shutting down; registering anyway", "label", a.profile.Label())
}

// jobState is what launchd reports about the service.
type jobState int

const (
	jobDown     jobState = iota // not loaded, or loaded without a process
	jobUp                       // a process exists
	jobRejected                 // launchd refused to start it (exit 78, EX_CONFIG)
)

func (a *app) jobState(target string) jobState {
	out, err := a.launchctl(a.ctx, "print", target)
	if err != nil {
		return jobDown
	}
	switch info := service.ParsePrint(string(out)); {
	case info.LaunchRejected():
		return jobRejected
	case info.PID > 0:
		return jobUp
	}
	return jobDown
}

// awaitRunning reports whether launchd started the daemon process and it stayed up. A process id alone does not
// count: a rejected job can be spawned and exit a moment later.
func (a *app) awaitRunning(timeout time.Duration) bool {
	const stayUp = 2 * time.Second
	target := service.Target(os.Getuid(), a.profile.Label())
	deadline := time.Now().Add(timeout)
	var upSince time.Time
	for time.Now().Before(deadline) && a.ctx.Err() == nil {
		switch a.jobState(target) {
		case jobRejected:
			return false
		case jobUp:
			if upSince.IsZero() {
				upSince = time.Now()
			} else if time.Since(upSince) >= stayUp {
				return true
			}
		default:
			upSince = time.Time{}
		}
		a.pause(250 * time.Millisecond)
	}
	return false
}

// registerService registers the bundled agent with launchd, which starts it right away (RunAtLoad), and checks that
// the daemon process stays up. launchd pins a registered agent to the code hash of its binary. Observed in testing:
// when the app bundle was replaced by a new build up to 74 seconds ago, a job registered now exits with 78 (EX_CONFIG)
// and never starts, and registering again does not help; 144 seconds after the replacement it worked. The
// registration is therefore not recorded on failure, so the next launch (or Start service) registers it again.
func (a *app) registerService() error {
	a.waitJobGone(20 * time.Second)
	st, err := smRegister(smAgent, a.profile.PlistName())
	slog.Info("registered service", "label", a.profile.Label(), "status", st.String(), "err", err)
	switch {
	case st == smRequiresApproval: // launchd waits for the user's approval; there is no process to check
		a.recordRegistration()
		return nil
	case st != smEnabled && err != nil:
		return err
	case st != smEnabled:
		return fmt.Errorf("the service is %s after registering", st)
	}
	if !a.awaitRunning(8 * time.Second) {
		return fmt.Errorf("launchd did not keep the daemon running after the service was registered. If the app was just replaced by a new build, wait a minute or two and choose Service > Stop service, then Start service. Log: %s", a.tilde(a.profile.LogFile(a.userHome)))
	}
	a.recordRegistration()
	return nil
}

func (a *app) toggleLaunchAtLogin() {
	var st smStatus
	var err error
	if smStatusOf(smMainApp, "") == smEnabled {
		st, err = smUnregister(smMainApp, "")
	} else {
		st, err = smRegister(smMainApp, "")
	}
	slog.Info("toggled launch at login", "status", st.String(), "err", err)
	if err != nil {
		a.fail("HAL9001 could not change Launch at login", err)
	}
}

func (a *app) openLogs() {
	log := a.profile.LogFile(a.userHome)
	if _, err := os.Stat(log); err != nil {
		a.fail("There is no log yet", fmt.Errorf("%s does not exist; the daemon writes it once its service has started", a.tilde(log)))
		return
	}
	if err := exec.CommandContext(a.ctx, "open", log).Run(); err != nil { //nolint:gosec // the log path is derived from the profile
		a.fail("HAL9001 could not open the log", err)
	}
}

// installCLI links ~/.local/bin/hal9001 to the daemon binary inside this app.
func (a *app) installCLI() {
	target, err := filepath.EvalSymlinks(a.daemonBinary())
	if err != nil {
		a.fail("The command-line tool is not part of this app", err)
		return
	}
	// The app runs with launchd's minimal PATH; what matters is the PATH a terminal gets.
	pathEnv, err := runner.LoginShellPATH(a.ctx, "", 0)
	if err != nil {
		slog.Warn("cannot read the login-shell PATH", "err", err)
		pathEnv = os.Getenv("PATH")
	}
	res, err := service.InstallCLI(a.userHome, a.profile.CLIName(), target, pathEnv)
	if err != nil {
		a.fail("HAL9001 could not install the command-line tool", err)
		return
	}
	msg := fmt.Sprintf("%s points to the hal9001 inside this app, so it is updated together with the app.", a.tilde(res.Link))
	if !res.OnPath {
		msg += "\n\n" + a.tilde(service.CLIDir(a.userHome)) + " is not on your PATH. Add this line to your shell profile (for example ~/.zshrc), then open a new terminal:\n\nexport PATH=\"$HOME/.local/bin:$PATH\""
	}
	alert("Command-line tool installed", msg, "OK", "")
}

// removeLegacy removes the launch agents of older versions after confirmation and hands the daemon over to the app.
func (a *app) removeLegacy() {
	found := a.found()
	if len(found.Legacy) == 0 {
		return
	}
	var list []string
	for _, l := range found.Legacy {
		list = append(list, "• "+a.tilde(l.Path))
	}
	msg := "Older versions of hal9001 installed these launch agents:\n\n" + strings.Join(list, "\n") +
		"\n\nRemoving them stops the old daemon and menubar app so this app can run the daemon instead. Your hal9001 home directory is not touched."
	if !alert("Remove older launch agents?", msg, "Remove", "Not now") {
		return
	}
	if err := service.RemoveLegacy(a.ctx, a.launchctl, os.Getuid(), found.Legacy); err != nil {
		a.fail("HAL9001 could not remove the older launch agents", err)
		return
	}
	if found.Headless {
		return // the menu keeps explaining how to remove the headless service
	}
	if err := a.registerService(); err != nil {
		a.fail("HAL9001 could not start its service", err)
	}
}

// fail logs err and tells the user.
func (a *app) fail(title string, err error) {
	slog.Error(title, "err", err)
	alert(title, err.Error(), "OK", "")
}

// tilde abbreviates the user's home directory in a path shown to people.
func (a *app) tilde(path string) string {
	if rest, ok := strings.CutPrefix(path, a.userHome+string(filepath.Separator)); ok {
		return "~/" + rest
	}
	return path
}

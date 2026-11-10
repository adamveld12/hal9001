package cli_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"hal9001/internal/cli"
)

// launchctlLog is a fake launchctl: it records calls and fails the subcommands named in fail.
type launchctlLog struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]string // subcommand -> error output
	out   map[string]string // subcommand -> output on success
}

func (l *launchctlLog) run(_ context.Context, args ...string) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, strings.Join(args, " "))
	if out, bad := l.fail[args[0]]; bad {
		return []byte(out), errors.New("exit status 5")
	}
	return []byte(l.out[args[0]]), nil
}

type serviceEnv struct {
	user    string
	exe     string
	launchd *launchctlLog
	opts    runOpts
}

func newServiceEnv(t *testing.T) *serviceEnv {
	t.Helper()
	user := t.TempDir()
	exe := filepath.Join(user, "bin", "hal9001")
	if err := os.MkdirAll(filepath.Dir(exe), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	// macOS resolves temp dirs through /var -> /private/var; the CLI resolves symlinks too.
	exe, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatal(err)
	}
	// The app's own agent is not loaded unless a test says so.
	se := &serviceEnv{user: user, exe: exe, launchd: &launchctlLog{fail: map[string]string{"print": "Could not find service"}, out: map[string]string{}}}
	se.opts = runOpts{hook: func(e *cli.Env) {
		e.UserHomeDir = func() (string, error) { return user, nil }
		e.Executable = func() (string, error) { return exe, nil }
		e.Getuid = func() int { return 501 }
		e.Launchctl = se.launchd.run
	}}
	return se
}

func (se *serviceEnv) plist(label string) string {
	return filepath.Join(se.user, "Library", "LaunchAgents", label+".plist")
}

func (se *serviceEnv) writePlist(t *testing.T, label string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(se.plist(label)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(se.plist(label), []byte("<plist/>"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInstallCommandIsGone(t *testing.T) {
	for _, name := range []string{"install", "uninstall"} {
		if res := run(t, runOpts{}, name); res.code != cli.ExitUsage || !strings.Contains(res.stderr, "unknown command") {
			t.Errorf("%s: exit %d stderr %q", name, res.code, res.stderr)
		}
	}
}

func TestServiceInstallDryRunPrintsThePlistAndTouchesNothing(t *testing.T) {
	se := newServiceEnv(t)
	home := filepath.Join(se.user, ".hal9001")
	res := run(t, se.opts, "service", "install", "--dry-run", "--home", home)
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	for _, want := range []string{
		"<string>dev.hal9001.daemon.headless</string>",
		"<string>" + se.exe + "</string>\n\t\t<string>serve</string>\n\t\t<string>--home</string>\n\t\t<string>" + home + "</string>",
		"<key>HAL9001_LAUNCHD</key>\n\t\t<string>1</string>",
		"<key>KeepAlive</key>\n\t<true/>",
	} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("plist lacks %q:\n%s", want, res.stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(se.user, "Library")); !os.IsNotExist(err) {
		t.Errorf("--dry-run created files under the user home: %v", err)
	}
	if len(se.launchd.calls) != 0 {
		t.Errorf("--dry-run ran launchctl: %v", se.launchd.calls)
	}
}

func TestServiceInstallWritesPlistBootstrapsAndWaitsForTheDaemon(t *testing.T) {
	se := newServiceEnv(t)
	f := newFakeDaemon(t)
	f.on("GET /api/health", ok(map[string]any{"status": "ok", "version": "test"}))
	se.opts.env = map[string]string{cli.EnvURL: f.srv.URL}

	home := filepath.Join(se.user, "hal-home")
	res := run(t, se.opts, "service", "install", "--home", home)
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	plist := se.plist("dev.hal9001.daemon.headless")
	data, err := os.ReadFile(plist)
	if err != nil || !strings.Contains(string(data), "<string>"+home+"</string>") {
		t.Fatalf("plist not written with the home: %v\n%s", err, data)
	}
	for _, d := range []string{filepath.Join(se.user, "Library", "Logs", "hal9001"), home} {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			t.Errorf("%s was not created: %v", d, err)
		}
	}
	want := []string{
		"print gui/501/dev.hal9001.daemon",
		"bootout gui/501/dev.hal9001.daemon.headless",
		"bootstrap gui/501 " + plist,
	}
	if strings.Join(se.launchd.calls, "; ") != strings.Join(want, "; ") {
		t.Errorf("launchctl calls = %v, want %v (check the app's agent, replace any running instance, load the new plist)", se.launchd.calls, want)
	}
	if !strings.Contains(res.stdout, "is running") {
		t.Errorf("stdout = %q", res.stdout)
	}
}

func TestServiceInstallReplacesTheLegacyDaemonAgentButNotTheLegacyMenubar(t *testing.T) {
	se := newServiceEnv(t)
	se.writePlist(t, "com.hal9001.daemon")
	se.writePlist(t, "dev.hal9001.menubar")
	f := newFakeDaemon(t)
	f.on("GET /api/health", ok(map[string]any{"status": "ok", "version": "test"}))
	se.opts.env = map[string]string{cli.EnvURL: f.srv.URL}

	res := run(t, se.opts, "service", "install", "--home", filepath.Join(se.user, "h"))
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	if _, err := os.Stat(se.plist("com.hal9001.daemon")); !os.IsNotExist(err) {
		t.Errorf("legacy daemon plist still present: %v", err)
	}
	if _, err := os.Stat(se.plist("dev.hal9001.menubar")); err != nil {
		t.Errorf("the old menubar agent is not the daemon's and must stay: %v", err)
	}
	if !strings.Contains(strings.Join(se.launchd.calls, "; "), "bootout gui/501/com.hal9001.daemon") {
		t.Errorf("legacy agent was not booted out: %v", se.launchd.calls)
	}
}

func TestServiceInstallRefusesWhileTheAppRunsItsOwnDaemon(t *testing.T) {
	se := newServiceEnv(t)
	delete(se.launchd.fail, "print") // `launchctl print gui/501/dev.hal9001.daemon` succeeds: the app's agent is loaded
	res := run(t, se.opts, "service", "install", "--home", filepath.Join(se.user, "h"))
	if res.code != cli.ExitError || !strings.Contains(res.stderr, "HAL9001 app") {
		t.Errorf("exit %d stderr %q", res.code, res.stderr)
	}
	if _, err := os.Stat(se.plist("dev.hal9001.daemon.headless")); !os.IsNotExist(err) {
		t.Errorf("plist written despite the conflict: %v", err)
	}
}

func TestServiceInstallFailsWhenLaunchctlBootstrapFails(t *testing.T) {
	se := newServiceEnv(t)
	se.launchd.fail["bootstrap"] = "Bootstrap failed: 5: Input/output error"
	res := run(t, se.opts, "service", "install", "--home", filepath.Join(se.user, "h"))
	if res.code != cli.ExitError || !strings.Contains(res.stderr, "Input/output error") {
		t.Errorf("exit %d stderr %q", res.code, res.stderr)
	}
}

func TestServiceUninstallRemovesHeadlessAndLegacyDaemonButKeepsTheHome(t *testing.T) {
	se := newServiceEnv(t)
	home := filepath.Join(se.user, "h")
	if err := os.MkdirAll(home, 0o750); err != nil {
		t.Fatal(err)
	}
	se.writePlist(t, "dev.hal9001.daemon.headless")
	se.writePlist(t, "com.hal9001.daemon")

	res := run(t, se.opts, "service", "uninstall")
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	want := "bootout gui/501/dev.hal9001.daemon.headless; bootout gui/501/com.hal9001.daemon"
	if strings.Join(se.launchd.calls, "; ") != want {
		t.Errorf("launchctl calls = %v", se.launchd.calls)
	}
	for _, l := range []string{"dev.hal9001.daemon.headless", "com.hal9001.daemon"} {
		if _, err := os.Stat(se.plist(l)); !os.IsNotExist(err) {
			t.Errorf("%s plist still present: %v", l, err)
		}
	}
	if _, err := os.Stat(home); err != nil {
		t.Errorf("uninstall removed the home directory: %v", err)
	}

	// Uninstalling again is not an error.
	se.launchd.fail["bootout"] = "Boot-out failed: 3: No such process"
	if res := run(t, se.opts, "service", "uninstall"); res.code != 0 {
		t.Errorf("second uninstall: exit %d: %s", res.code, res.stderr)
	}
}

func TestServiceStatusReportsInstallLaunchdAndDaemon(t *testing.T) {
	se := newServiceEnv(t)
	f := newFakeDaemon(t)
	f.on("GET /api/health", ok(map[string]any{"status": "ok", "version": "1.2.3"}))
	se.opts.env = map[string]string{cli.EnvURL: f.srv.URL}

	res := run(t, se.opts, "service", "status")
	if res.code != 0 || !strings.Contains(res.stdout, "not installed") {
		t.Errorf("not installed: exit %d stdout %q stderr %q", res.code, res.stdout, res.stderr)
	}

	se.writePlist(t, "dev.hal9001.daemon.headless")
	se.writePlist(t, "com.hal9001.daemon")
	delete(se.launchd.fail, "print")
	se.launchd.out["print"] = "dev.hal9001.daemon.headless = {\n\tstate = running\n\truns = 1\n\tpid = 4242\n\tlast exit code = (never exited)\n}\n"
	res = run(t, se.opts, "service", "status")
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	for _, want := range []string{"installed (" + se.plist("dev.hal9001.daemon.headless"), "state running, pid 4242", "answering", "1.2.3", "com.hal9001.daemon", "hal9001.log"} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("status lacks %q:\n%s", want, res.stdout)
		}
	}
}

func TestServiceLogsPrintsTheTail(t *testing.T) {
	se := newServiceEnv(t)
	if res := run(t, se.opts, "service", "logs"); res.code != cli.ExitError || !strings.Contains(res.stderr, "no log yet") {
		t.Errorf("without a log: exit %d stderr %q", res.code, res.stderr)
	}
	logFile := filepath.Join(se.user, "Library", "Logs", "hal9001", "hal9001.log")
	if err := os.MkdirAll(filepath.Dir(logFile), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logFile, []byte("one\ntwo\nthree\nfour\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := run(t, se.opts, "service", "logs", "-n", "2"); res.code != 0 || res.stdout != "three\nfour\n" {
		t.Errorf("-n 2: exit %d stdout %q stderr %q", res.code, res.stdout, res.stderr)
	}
	if res := run(t, se.opts, "service", "logs"); res.code != 0 || res.stdout != "one\ntwo\nthree\nfour\n" {
		t.Errorf("default: exit %d stdout %q", res.code, res.stdout)
	}
}

package cli_test

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"hal9001/content"
	"hal9001/internal/auth"
	"hal9001/internal/cli"
	"hal9001/internal/daemon"
	"hal9001/internal/spec"
)

// ---- install / uninstall ----

type launchctlLog struct {
	mu    sync.Mutex
	calls [][]string
	fail  map[string]string // subcommand -> error output
}

func (l *launchctlLog) run(_ context.Context, args ...string) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, args)
	if out, bad := l.fail[args[0]]; bad {
		return []byte(out), errors.New("exit status 5")
	}
	return nil, nil
}

type installEnv struct {
	user    string
	exe     string
	launchd *launchctlLog
	opts    runOpts
}

const loginPATH = "/Users/adam/.local/share/mise/shims:/opt/homebrew/bin:/usr/bin:/weird&dir"

func newInstallEnv(t *testing.T) *installEnv {
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
	ie := &installEnv{user: user, exe: exe, launchd: &launchctlLog{fail: map[string]string{}}}
	ie.opts = runOpts{hook: func(e *cli.Env) {
		e.UserHomeDir = func() (string, error) { return user, nil }
		e.Executable = func() (string, error) { return exe, nil }
		e.Getuid = func() int { return 501 }
		e.LoginShellPATH = func(context.Context) (string, error) { return loginPATH, nil }
		e.Launchctl = ie.launchd.run
	}}
	return ie
}

// plistDict decodes the top-level <dict> of a property list into Go values:
// strings, bools, []string for string arrays and nested map[string]any.
func plistDict(t *testing.T, data string) map[string]any {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(data))
	var parseValue func(start xml.StartElement) any
	parseDict := func() map[string]any {
		m := map[string]any{}
		var key string
		for {
			tok, err := dec.Token()
			if err != nil {
				t.Fatalf("plist is not well-formed XML: %v", err)
			}
			switch el := tok.(type) {
			case xml.StartElement:
				if el.Name.Local == "key" {
					var k string
					if err := dec.DecodeElement(&k, &el); err != nil {
						t.Fatal(err)
					}
					key = k
				} else {
					m[key] = parseValue(el)
				}
			case xml.EndElement:
				return m
			}
		}
	}
	parseValue = func(start xml.StartElement) any {
		switch start.Name.Local {
		case "string":
			var s string
			if err := dec.DecodeElement(&s, &start); err != nil {
				t.Fatal(err)
			}
			return s
		case "true":
			_ = dec.Skip()
			return true
		case "false":
			_ = dec.Skip()
			return false
		case "array":
			var out []string
			for {
				tok, err := dec.Token()
				if err != nil {
					t.Fatal(err)
				}
				switch el := tok.(type) {
				case xml.StartElement:
					var s string
					if err := dec.DecodeElement(&s, &el); err != nil {
						t.Fatal(err)
					}
					out = append(out, s)
				case xml.EndElement:
					return out
				}
			}
		case "dict":
			return parseDict()
		}
		t.Fatalf("unexpected plist element %q", start.Name.Local)
		return nil
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("no <dict> in plist: %v", err)
		}
		if el, ok := tok.(xml.StartElement); ok && el.Name.Local == "dict" {
			return parseDict()
		}
	}
}

func TestInstallDryRunPrintsPlistWithLoginShellPATHAndTouchesNothing(t *testing.T) {
	ie := newInstallEnv(t)
	home := filepath.Join(ie.user, ".hal9001")
	res := run(t, ie.opts, "install", "--dry-run", "--home", home)
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	d := plistDict(t, res.stdout)

	if d["Label"] != "com.hal9001.daemon" {
		t.Errorf("Label = %v", d["Label"])
	}
	if d["KeepAlive"] != true || d["RunAtLoad"] != true {
		t.Errorf("KeepAlive = %v, RunAtLoad = %v; the daemon must start at login and restart when it exits", d["KeepAlive"], d["RunAtLoad"])
	}
	args, _ := d["ProgramArguments"].([]string)
	if strings.Join(args, " ") != ie.exe+" serve --home "+home {
		t.Errorf("ProgramArguments = %v", args)
	}
	envVars, _ := d["EnvironmentVariables"].(map[string]any)
	if envVars["PATH"] != loginPATH {
		t.Errorf("PATH = %q, want the captured login-shell PATH %q (so mise shims, gh and jq resolve)", envVars["PATH"], loginPATH)
	}
	logDir := filepath.Join(ie.user, "Library", "Logs", "hal9001")
	for _, k := range []string{"StandardOutPath", "StandardErrorPath"} {
		if p, _ := d[k].(string); filepath.Dir(p) != logDir {
			t.Errorf("%s = %q, want a file under %s", k, p, logDir)
		}
	}

	if _, err := os.Stat(filepath.Join(ie.user, "Library")); !os.IsNotExist(err) {
		t.Errorf("--dry-run created files under the user home: %v", err)
	}
	if n := len(ie.launchd.calls); n != 0 {
		t.Errorf("--dry-run ran launchctl %d times", n)
	}
}

func TestInstallWritesPlistBootstrapsAndWaitsForTheDaemon(t *testing.T) {
	ie := newInstallEnv(t)
	f := newFakeDaemon(t)
	f.on("GET /api/health", ok(map[string]any{"status": "ok", "version": "test"}))
	ie.opts.env = map[string]string{cli.EnvURL: f.srv.URL}

	home := filepath.Join(ie.user, "hal-home")
	res := run(t, ie.opts, "install", "--home", home)
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}

	plist := filepath.Join(ie.user, "Library", "LaunchAgents", "com.hal9001.daemon.plist")
	data, err := os.ReadFile(plist)
	if err != nil {
		t.Fatalf("plist not written: %v", err)
	}
	if d := plistDict(t, string(data)); d["Label"] != "com.hal9001.daemon" {
		t.Errorf("written plist = %v", d)
	}
	if st, _ := os.Stat(filepath.Join(ie.user, "Library", "Logs", "hal9001")); st == nil || !st.IsDir() {
		t.Error("log directory was not created; launchd would fail to open the log files")
	}
	if _, err := os.Stat(home); err != nil {
		t.Errorf("home directory (the agent's WorkingDirectory) was not created: %v", err)
	}

	want := [][]string{
		{"bootout", "gui/501/com.hal9001.daemon"},
		{"bootstrap", "gui/501", plist},
	}
	if len(ie.launchd.calls) != 2 || strings.Join(ie.launchd.calls[0], " ") != strings.Join(want[0], " ") ||
		strings.Join(ie.launchd.calls[1], " ") != strings.Join(want[1], " ") {
		t.Errorf("launchctl calls = %v, want %v (replace any running instance, then load the new plist)", ie.launchd.calls, want)
	}
	if !strings.Contains(res.stdout, "is running") {
		t.Errorf("stdout = %q", res.stdout)
	}
}

func TestInstallFailsWhenLaunchctlBootstrapFails(t *testing.T) {
	ie := newInstallEnv(t)
	ie.launchd.fail["bootstrap"] = "Bootstrap failed: 5: Input/output error"
	res := run(t, ie.opts, "install", "--home", filepath.Join(ie.user, "h"))
	if res.code != cli.ExitError || !strings.Contains(res.stderr, "Input/output error") {
		t.Errorf("exit %d stderr %q", res.code, res.stderr)
	}
}

func TestInstallFailsBeforeWritingWhenLoginShellPATHCannotBeCaptured(t *testing.T) {
	ie := newInstallEnv(t)
	inner := ie.opts.hook
	ie.opts.hook = func(e *cli.Env) {
		inner(e)
		e.LoginShellPATH = func(context.Context) (string, error) { return "", errors.New("login shell PATH: zsh timed out") }
	}
	res := run(t, ie.opts, "install", "--home", filepath.Join(ie.user, "h"))
	if res.code != cli.ExitError || !strings.Contains(res.stderr, "timed out") {
		t.Errorf("exit %d stderr %q", res.code, res.stderr)
	}
	if _, err := os.Stat(filepath.Join(ie.user, "Library")); !os.IsNotExist(err) {
		t.Error("files were written despite the failure")
	}
	if len(ie.launchd.calls) != 0 {
		t.Errorf("launchctl ran: %v", ie.launchd.calls)
	}
}

func TestUninstallBootsOutAndRemovesThePlistButKeepsTheHome(t *testing.T) {
	ie := newInstallEnv(t)
	f := newFakeDaemon(t)
	f.on("GET /api/health", ok(map[string]any{"status": "ok", "version": "test"}))
	ie.opts.env = map[string]string{cli.EnvURL: f.srv.URL}
	home := filepath.Join(ie.user, "h")
	if res := run(t, ie.opts, "install", "--home", home); res.code != 0 {
		t.Fatalf("install: %s", res.stderr)
	}
	ie.launchd.calls = nil

	res := run(t, ie.opts, "uninstall")
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	if len(ie.launchd.calls) != 1 || strings.Join(ie.launchd.calls[0], " ") != "bootout gui/501/com.hal9001.daemon" {
		t.Errorf("launchctl calls = %v", ie.launchd.calls)
	}
	if _, err := os.Stat(filepath.Join(ie.user, "Library", "LaunchAgents", "com.hal9001.daemon.plist")); !os.IsNotExist(err) {
		t.Errorf("plist still present: %v", err)
	}
	if _, err := os.Stat(home); err != nil {
		t.Errorf("uninstall removed the home directory: %v", err)
	}

	// Uninstalling again is not an error.
	ie.launchd.fail["bootout"] = "Boot-out failed: 3: No such process"
	if res := run(t, ie.opts, "uninstall"); res.code != 0 {
		t.Errorf("second uninstall: exit %d: %s", res.code, res.stderr)
	}
}

// ---- open / serve / version ----

func TestOpenOpensTheUIURLWithOptionalPath(t *testing.T) {
	var opened []string
	opts := runOpts{hook: func(e *cli.Env) {
		e.OpenURL = func(_ context.Context, u string) error { opened = append(opened, u); return nil }
	}}
	if res := run(t, opts, "open"); res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	if res := run(t, opts, "open", "/inbox/"+id1); res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	if len(opened) != 2 || opened[0] != "http://127.0.0.1:9001/" || opened[1] != "http://127.0.0.1:9001/inbox/"+id1 {
		t.Errorf("opened %v", opened)
	}
}

func TestServeStartsTheDaemonWithResolvedHomeAndWebDir(t *testing.T) {
	var got daemon.Options
	opts := runOpts{
		env: map[string]string{cli.EnvHome: "/tmp/hal-home-from-env", cli.EnvWebDir: "/tmp/ui"},
		hook: func(e *cli.Env) {
			e.Serve = func(_ context.Context, o daemon.Options) error { got = o; return nil }
		},
	}
	if res := run(t, opts, "serve"); res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	if got.Home != "/tmp/hal-home-from-env" || got.WebDir != "/tmp/ui" || got.Version != "9.9.9-test" {
		t.Errorf("daemon options = %+v", got)
	}

	// --home beats the environment; the default is ~/.hal9001.
	opts.hook = func(e *cli.Env) {
		e.UserHomeDir = func() (string, error) { return "/Users/someone", nil }
		e.Serve = func(_ context.Context, o daemon.Options) error { got = o; return nil }
	}
	if res := run(t, opts, "serve", "--home", "/tmp/explicit"); res.code != 0 || got.Home != "/tmp/explicit" {
		t.Errorf("--home: exit %d, home %q", res.code, got.Home)
	}
	opts.env = nil
	if res := run(t, opts, "serve"); res.code != 0 || got.Home != "/Users/someone/.hal9001" {
		t.Errorf("default home = %q (exit %d)", got.Home, res.code)
	}
}

func TestServeFailureExitsNonZero(t *testing.T) {
	opts := runOpts{hook: func(e *cli.Env) {
		e.Serve = func(context.Context, daemon.Options) error {
			return errors.New("listen on 127.0.0.1:9001: address already in use")
		}
	}}
	if res := run(t, opts, "serve", "--home", t.TempDir()); res.code != cli.ExitError || !strings.Contains(res.stderr, "address already in use") {
		t.Errorf("exit %d stderr %q", res.code, res.stderr)
	}
}

// ---- validate ----

func seededHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if _, err := spec.Init(home, content.FS); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestValidateAcceptsTheSeededHome(t *testing.T) {
	home := seededHome(t)
	res := run(t, runOpts{}, "validate", "--home", home)
	if res.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "0 errors") {
		t.Errorf("summary missing: %s", res.stdout)
	}
}

func TestValidateReportsErrorsWithFileAndExitsNonZero(t *testing.T) {
	home := seededHome(t)
	flow := filepath.Join(home, "flows", "demo.yaml")
	data, err := os.ReadFile(flow)
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(data), "subscribe: [", "subscribe: [no-such-sensor, ", 1)
	if broken == string(data) {
		t.Fatalf("fixture changed: demo.yaml has no `subscribe: [` to break:\n%s", data)
	}
	if err := os.WriteFile(flow, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	res := run(t, runOpts{}, "validate", "--home", home)
	if res.code != cli.ExitError {
		t.Errorf("exit = %d, want %d", res.code, cli.ExitError)
	}
	if !strings.Contains(res.stdout, "flows/demo.yaml") || !strings.Contains(res.stdout, "no-such-sensor") {
		t.Errorf("diagnostic should name the file and the unknown sensor:\n%s", res.stdout)
	}

	js := run(t, runOpts{}, "validate", "--home", home, "--json")
	var doc struct {
		Errors      int
		Diagnostics []spec.Diagnostic
	}
	if err := json.Unmarshal([]byte(js.stdout), &doc); err != nil || js.code != cli.ExitError || doc.Errors == 0 || len(doc.Diagnostics) == 0 {
		t.Errorf("--json: exit %d, %v, %s", js.code, err, js.stdout)
	}
}

func TestValidateFailsForAMissingHome(t *testing.T) {
	res := run(t, runOpts{}, "validate", "--home", filepath.Join(t.TempDir(), "nope"))
	if res.code != cli.ExitError || !strings.Contains(res.stderr, "does not exist") {
		t.Errorf("exit %d stderr %q", res.code, res.stderr)
	}
}

// ---- auth set-password ----

func passwordHashOnDisk(t *testing.T, home string) string {
	t.Helper()
	ws := spec.Load(home)
	if errs := ws.Config.Diagnostics; spec.HasErrors(errs) {
		t.Fatalf("config.yaml invalid after set-password: %v", errs)
	}
	return daemon.AuthConfig(ws.Config.Value).PasswordHash
}

func TestSetPasswordStoresAnArgon2idHashThatLogsIn(t *testing.T) {
	home := seededHome(t)
	before, err := os.ReadFile(filepath.Join(home, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	res := run(t, runOpts{stdin: "correct horse battery staple\n"}, "auth", "set-password", "--stdin", "--home", home)
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	hash := passwordHashOnDisk(t, home)
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("hash = %q", hash)
	}
	if ok, err := auth.VerifyPassword(hash, "correct horse battery staple"); err != nil || !ok {
		t.Errorf("stored hash does not verify the password: %v %v", ok, err)
	}
	if ok, _ := auth.VerifyPassword(hash, "wrong"); ok {
		t.Error("stored hash accepts a wrong password")
	}

	after, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	if strings.Contains(res.stdout+res.stderr, "argon2id") {
		t.Error("the hash was echoed to the terminal output")
	}
	// Hand-written comments and the other settings survive the edit.
	for _, keep := range []string{"# hal9001 config. Hot-reloaded on change.", "max_agent_runs: 2", "retention_days: 30", "runtimes:", "claude:", "# Auth: PAM-style stacks per service"} {
		if !strings.Contains(string(after), keep) {
			t.Errorf("config.yaml lost %q after set-password:\n%s", keep, after)
		}
	}
	if string(after) == string(before) {
		t.Error("config.yaml unchanged")
	}

	// Setting it again replaces the hash.
	if res := run(t, runOpts{stdin: "second password\n"}, "auth", "set-password", "--stdin", "--home", home); res.code != 0 {
		t.Fatalf("second set-password: %s", res.stderr)
	}
	if h2 := passwordHashOnDisk(t, home); h2 == hash {
		t.Error("hash not replaced")
	} else if ok, _ := auth.VerifyPassword(h2, "second password"); !ok {
		t.Error("new hash does not verify the new password")
	}
	if strings.Count(string(mustRead(t, filepath.Join(home, "config.yaml"))), "$argon2id$") != 1 {
		t.Error("expected exactly one stored hash")
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSetPasswordPromptsTwiceAndRefusesAMismatch(t *testing.T) {
	home := seededHome(t)
	answers := []string{"first", "different"}
	var prompts []string
	opts := runOpts{hook: func(e *cli.Env) {
		e.ReadPassword = func(prompt string) (string, error) {
			prompts = append(prompts, prompt)
			a := answers[0]
			answers = answers[1:]
			return a, nil
		}
	}}
	res := run(t, opts, "auth", "set-password", "--home", home)
	if res.code != cli.ExitError || !strings.Contains(res.stderr, "do not match") || len(prompts) != 2 {
		t.Errorf("exit %d stderr %q prompts %v", res.code, res.stderr, prompts)
	}
	if h := passwordHashOnDisk(t, home); h != "" {
		t.Errorf("a hash was stored after a mismatch: %q", h)
	}

	answers = []string{"same-both-times", "same-both-times"}
	if res := run(t, opts, "auth", "set-password", "--home", home); res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	if ok, _ := auth.VerifyPassword(passwordHashOnDisk(t, home), "same-both-times"); !ok {
		t.Error("prompted password was not stored")
	}
}

func TestSetPasswordRejectsAnEmptyPassword(t *testing.T) {
	home := seededHome(t)
	res := run(t, runOpts{stdin: "\n"}, "auth", "set-password", "--stdin", "--home", home)
	if res.code != cli.ExitError || !strings.Contains(res.stderr, "empty") {
		t.Errorf("exit %d stderr %q", res.code, res.stderr)
	}
}

func TestSetPasswordWithoutAPasswordModuleExplainsWhatToAdd(t *testing.T) {
	home := t.TempDir()
	cfg := "auth:\n  stacks:\n    default:\n      - {module: localhost-trust, control: sufficient}\n"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	res := run(t, runOpts{stdin: "pw\n"}, "auth", "set-password", "--stdin", "--home", home)
	if res.code != cli.ExitError || !strings.Contains(res.stderr, "no `password` module") {
		t.Errorf("exit %d stderr %q", res.code, res.stderr)
	}
	if got := string(mustRead(t, filepath.Join(home, "config.yaml"))); got != cfg {
		t.Errorf("config.yaml was modified:\n%s", got)
	}
}

func TestSetPasswordCreatesConfigWithDefaultStackWhenMissing(t *testing.T) {
	home := t.TempDir()
	if res := run(t, runOpts{stdin: "pw-from-nothing\n"}, "auth", "set-password", "--stdin", "--home", home); res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	ws := spec.Load(home)
	cfg := daemon.AuthConfig(ws.Config.Value)
	if ok, _ := auth.VerifyPassword(cfg.PasswordHash, "pw-from-nothing"); !ok {
		t.Error("password not stored")
	}
	if len(cfg.Stacks["default"]) != 4 || cfg.Stacks["default"][0].Module != "localhost-trust" {
		t.Errorf("default stack not preserved: %+v", cfg.Stacks)
	}
}

func TestSetPasswordSetsEveryPasswordEntryAcrossStacks(t *testing.T) {
	home := t.TempDir()
	cfg := `auth:
  stacks:
    strict:
      - {module: password, control: required}
    lax:
      - {module: localhost-trust, control: sufficient}
      - {module: password, control: sufficient, options: {note: keep}}
  services: {ui: lax, api: lax, approve: strict, admin: strict}
`
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := run(t, runOpts{stdin: "pw\n"}, "auth", "set-password", "--stdin", "--home", home); res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	got := string(mustRead(t, filepath.Join(home, "config.yaml")))
	if strings.Count(got, "$argon2id$") != 2 || !strings.Contains(got, "note: keep") {
		t.Errorf("config.yaml:\n%s", got)
	}
}

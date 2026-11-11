package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hal9001/content"
	"hal9001/internal/auth"
	"hal9001/internal/authcfg"
	"hal9001/internal/cli"
	"hal9001/internal/daemon"
	"hal9001/internal/spec"
)

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

func TestServeUnderLaunchdLogsToAFileAndUsesTheLoginShellPATH(t *testing.T) {
	user := t.TempDir()
	var got daemon.Options
	var logged string
	setenv := map[string]string{}
	opts := runOpts{
		env: map[string]string{"HAL9001_LAUNCHD": "1"},
		hook: func(e *cli.Env) {
			e.UserHomeDir = func() (string, error) { return user, nil }
			e.LoginShellPATH = func(context.Context) (string, error) { return "/shims:/opt/homebrew/bin:/usr/bin", nil }
			e.Setenv = func(k, v string) error { setenv[k] = v; return nil }
			e.RedirectLog = func(path string) error { logged = path; return nil }
			e.Serve = func(_ context.Context, o daemon.Options) error { got = o; return nil }
		},
	}
	if res := run(t, opts, "serve", "--home", t.TempDir()); res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	if want := filepath.Join(user, "Library", "Logs", "hal9001", "hal9001.log"); logged != want {
		t.Errorf("log file = %q, want %q", logged, want)
	}
	if st, err := os.Stat(filepath.Join(user, "Library", "Logs", "hal9001")); err != nil || !st.IsDir() {
		t.Errorf("log directory was not created: %v", err)
	}
	if !strings.HasPrefix(got.BasePATH, "/shims:/opt/homebrew/bin:/usr/bin") {
		t.Errorf("BasePATH = %q, want the login-shell PATH so mise shims, gh and jq resolve inside nodes", got.BasePATH)
	}
	if setenv["PATH"] != got.BasePATH {
		t.Errorf("process PATH = %q, want the effective PATH %q so the daemon's own lookups (kubectl, gh, tailscale) agree with its children", setenv["PATH"], got.BasePATH)
	}

	// A bundled agent names its log with a ~ that launchd does not expand.
	opts.env["HAL9001_LOG_FILE"] = "~/Library/Logs/hal9001-dev/hal9001.log"
	if res := run(t, opts, "serve", "--home", t.TempDir()); res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	if want := filepath.Join(user, "Library", "Logs", "hal9001-dev", "hal9001.log"); logged != want {
		t.Errorf("log file = %q, want %q", logged, want)
	}
}

func TestServeUnderLaunchdStillStartsWhenTheLoginShellPATHCannotBeCaptured(t *testing.T) {
	var got daemon.Options
	started := false
	user := t.TempDir()
	opts := runOpts{
		env: map[string]string{"HAL9001_LAUNCHD": "1", "PATH": "/usr/bin:/bin"},
		hook: func(e *cli.Env) {
			e.UserHomeDir = func() (string, error) { return user, nil }
			e.Setenv = func(string, string) error { return nil }
			e.LoginShellPATH = func(context.Context) (string, error) { return "", errors.New("login shell PATH: zsh timed out") }
			e.RedirectLog = func(string) error { return nil }
			e.Serve = func(_ context.Context, o daemon.Options) error { got, started = o, true; return nil }
		},
	}
	if res := run(t, opts, "serve", "--home", t.TempDir()); res.code != 0 || !started || !strings.HasPrefix(got.BasePATH, "/usr/bin:/bin") {
		t.Errorf("exit %d, started %v, BasePATH %q: a slow shell rc must not keep the daemon down, and the PATH launchd gave must survive", res.code, started, got.BasePATH)
	}
}

func TestServeWithoutLaunchdLeavesLogsAndPATHAlone(t *testing.T) {
	opts := runOpts{hook: func(e *cli.Env) {
		e.RedirectLog = func(string) error { t.Error("stdout/stderr were redirected outside launchd"); return nil }
		e.LoginShellPATH = func(context.Context) (string, error) {
			t.Error("login shell was consulted outside launchd")
			return "", nil
		}
		e.Serve = func(context.Context, daemon.Options) error { return nil }
	}}
	if res := run(t, opts, "serve", "--home", t.TempDir()); res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
}

func TestServeDevPortOverridesTheListenPort(t *testing.T) {
	var got daemon.Options
	opts := runOpts{
		env:  map[string]string{"HAL9001_DEV_PORT": "19001"},
		hook: func(e *cli.Env) { e.Serve = func(_ context.Context, o daemon.Options) error { got = o; return nil } },
	}
	if res := run(t, opts, "serve", "--home", t.TempDir()); res.code != 0 || got.ListenAddr != "127.0.0.1:19001" {
		t.Errorf("exit %d, ListenAddr %q", res.code, got.ListenAddr)
	}
	opts.env["HAL9001_DEV_PORT"] = "nine"
	if res := run(t, opts, "serve", "--home", t.TempDir()); res.code != cli.ExitError || !strings.Contains(res.stderr, "not a port number") {
		t.Errorf("bad port: exit %d stderr %q", res.code, res.stderr)
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
	return authcfg.AuthConfig(ws.Config.Value).PasswordHash
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

func TestSetPasswordRefusesAShortPasswordAndChangesNothing(t *testing.T) {
	home := seededHome(t)
	before := mustRead(t, filepath.Join(home, "config.yaml"))
	res := run(t, runOpts{stdin: "too short\n"}, "auth", "set-password", "--stdin", "--home", home)
	if res.code != cli.ExitError || !strings.Contains(res.stderr, "at least 12 characters") {
		t.Errorf("exit %d stderr %q", res.code, res.stderr)
	}
	if got := mustRead(t, filepath.Join(home, "config.yaml")); string(got) != string(before) {
		t.Errorf("config.yaml was modified:\n%s", got)
	}
}

func TestSetPasswordAddsThePasswordModuleWhenNoStackHasIt(t *testing.T) {
	home := t.TempDir()
	cfg := "auth:\n  stacks:\n    default:\n      - {module: localhost-trust, control: sufficient}\n"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := run(t, runOpts{stdin: "a long enough password\n"}, "auth", "set-password", "--stdin", "--home", home); res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	stack := authcfg.AuthConfig(spec.Load(home).Config.Value).Stacks["default"]
	if len(stack) != 2 || stack[0].Module != "localhost-trust" || stack[1].Module != "password" || stack[1].Control != "sufficient" {
		t.Fatalf("stack = %+v", stack)
	}
	if ok, _ := auth.VerifyPassword(passwordHashOnDisk(t, home), "a long enough password"); !ok {
		t.Error("password not stored")
	}
}

func TestSetPasswordCreatesConfigWithDefaultStackWhenMissing(t *testing.T) {
	home := t.TempDir()
	if res := run(t, runOpts{stdin: "pw-from-nothing\n"}, "auth", "set-password", "--stdin", "--home", home); res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	ws := spec.Load(home)
	cfg := authcfg.AuthConfig(ws.Config.Value)
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
	if res := run(t, runOpts{stdin: "pw-for-every-entry\n"}, "auth", "set-password", "--stdin", "--home", home); res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	got := string(mustRead(t, filepath.Join(home, "config.yaml")))
	if strings.Count(got, "$argon2id$") != 2 || !strings.Contains(got, "note: keep") {
		t.Errorf("config.yaml:\n%s", got)
	}
}

func TestStartersAddRestoresNamedWithoutOverwriting(t *testing.T) {
	home := seededHome(t)
	flow := filepath.Join(home, "flows", "demo.yaml")
	if err := os.Remove(flow); err != nil {
		t.Fatal(err)
	}
	if res := run(t, runOpts{}, "starters", "list", "--home", home); res.code != 0 || !strings.Contains(res.stdout, "missing  flows/demo.yaml") {
		t.Fatalf("list: %d %s %s", res.code, res.stdout, res.stderr)
	}
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := run(t, runOpts{}, "starters", "add", "demo", "config.yaml", "--home", home); res.code != 0 {
		t.Fatalf("add: %d %s %s", res.code, res.stdout, res.stderr)
	}
	if _, err := os.Stat(flow); err != nil {
		t.Errorf("demo not restored: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "config.yaml")); string(b) != "mine\n" {
		t.Errorf("config overwritten: %q", b)
	}
}

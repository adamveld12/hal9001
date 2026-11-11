package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"hal9001/internal/auth"
	"hal9001/internal/authcfg"
	"hal9001/internal/authstore"
	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// recordingEngine captures the workspaces the daemon applies.
type recordingEngine struct {
	core.Engine
	applied []*spec.Workspace
}

func (e *recordingEngine) Apply(ws *spec.Workspace)    { e.applied = append(e.applied, ws) }
func (e *recordingEngine) Start(context.Context) error { return nil }
func (e *recordingEngine) Stop(context.Context) error  { return nil }

const strictConfig = `max_agent_runs: 5
allowed_hosts: [mac.tail1234.ts.net]
auth:
  stacks:
    strict:
      - {module: password, control: required, options: {hash: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$a2V5a2V5a2V5"}}
  services: {ui: strict, api: strict, approve: strict, admin: strict}
`

func newReloadHarness(t *testing.T) (*daemon, *recordingEngine, string) {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(strictConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(home, "data", "hal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ws := spec.Load(home)
	mgr, err := auth.New(auth.Options{Config: authcfg.AuthConfig(ws.Config.Value), Store: authstore.New(st), Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	eng := &recordingEngine{}
	d := &daemon{home: home, log: log, engine: eng, auth: mgr, lastGoodConfig: ws.Config}
	d.ws.Store(ws)
	return d, eng, home
}

func TestReloadAppliesValidConfigToEngineAndAuth(t *testing.T) {
	d, eng, home := newReloadHarness(t)
	next := `max_agent_runs: 7
allowed_logins: [alice@example.com]
`
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(next), 0o600); err != nil {
		t.Fatal(err)
	}
	d.reload()

	if len(eng.applied) != 1 || eng.applied[0].Config.Value.MaxAgentRuns != 7 {
		t.Fatalf("engine got %+v, want one workspace with max_agent_runs 7", eng.applied)
	}
	cfg := d.auth.Config()
	if len(cfg.AllowedLogins) != 1 || cfg.AllowedLogins[0] != "alice@example.com" || cfg.Services[auth.ServiceAdmin] != "default" {
		t.Errorf("auth config after reload = %+v", cfg)
	}
	if got := d.workspace().Config.Value.MaxAgentRuns; got != 7 {
		t.Errorf("API workspace max_agent_runs = %d", got)
	}
}

func TestBrokenConfigKeepsPreviousAuthAndEngineConfigButShowsDiagnostics(t *testing.T) {
	d, eng, home := newReloadHarness(t)
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("auth: [this is: not valid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.reload()

	// Defaults include localhost-trust; falling back to them would open a
	// locked-down daemon to every local process.
	cfg := d.auth.Config()
	if cfg.Services[auth.ServiceAdmin] != "strict" || len(cfg.Stacks["strict"]) != 1 {
		t.Errorf("auth config changed after a broken reload: %+v", cfg)
	}
	if len(eng.applied) != 1 || eng.applied[0].Config.Value.MaxAgentRuns != 5 {
		t.Errorf("engine was handed %+v, want the previous config (max_agent_runs 5)", eng.applied)
	}
	if !spec.HasErrors(d.workspace().Config.Diagnostics) {
		t.Error("the API workspace lost the config.yaml diagnostics")
	}

	// Fixing the file recovers.
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("max_agent_runs: 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.reload()
	if got := eng.applied[len(eng.applied)-1].Config.Value.MaxAgentRuns; got != 3 {
		t.Errorf("after fix engine max_agent_runs = %d, want 3", got)
	}
	if d.auth.Config().Services[auth.ServiceAdmin] != "default" {
		t.Errorf("auth did not recover: %+v", d.auth.Config().Services)
	}
}

func TestAuthConfigMapsConfigYAML(t *testing.T) {
	ws := spec.Load(func() string {
		home := t.TempDir()
		_ = os.WriteFile(filepath.Join(home, "config.yaml"), []byte(strictConfig+"session_ttl: 2h\nallowed_logins: [a@b.c]\n"), 0o600)
		return home
	}())
	if errs := errorMessages(ws.Config.Diagnostics); len(errs) > 0 {
		t.Fatalf("fixture config invalid: %v", errs)
	}
	cfg := authcfg.AuthConfig(ws.Config.Value)
	if cfg.SessionTTL.String() != "2h0m0s" || len(cfg.AllowedHosts) != 1 || cfg.AllowedHosts[0] != "mac.tail1234.ts.net" ||
		len(cfg.AllowedLogins) != 1 {
		t.Errorf("scalar settings = %+v", cfg)
	}
	if cfg.Services[auth.ServiceApprove] != "strict" || cfg.Stacks["strict"][0].Control != auth.Required {
		t.Errorf("stacks/services = %+v / %+v", cfg.Stacks, cfg.Services)
	}
	if cfg.PasswordHash == "" || cfg.PasswordHash[:9] != "$argon2id" {
		t.Errorf("PasswordHash = %q, want the hash from the password entry's options", cfg.PasswordHash)
	}
}

func TestAuthConfigDefaultsHaveNoPasswordHashAndValidate(t *testing.T) {
	var c spec.Config
	c.ApplyDefaults()
	cfg := authcfg.AuthConfig(&c)
	if cfg.PasswordHash != "" {
		t.Errorf("PasswordHash = %q", cfg.PasswordHash)
	}
	if _, err := auth.New(auth.Options{Config: cfg, Store: auth.NewMemoryStore()}); err != nil {
		t.Errorf("default spec config maps to an invalid auth config: %v", err)
	}
}

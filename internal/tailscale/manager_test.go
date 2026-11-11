package tailscale_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"hal9001/content"
	"hal9001/internal/spec"
	"hal9001/internal/tailscale"
	"hal9001/internal/tailscale/tailscaletest"
)

const halPort = 9001

type env struct {
	t    *testing.T
	home string
	stub *tailscaletest.Stub
	m    *tailscale.Manager
}

// newEnv is a home seeded with the starter config.yaml and a manager that can only reach the stub.
func newEnv(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	b, err := content.FS.ReadFile("config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, home: home, stub: tailscaletest.New(t)}
	e.writeConfig(string(b))
	e.m = e.manager(e.stub.BinDir)
	return e
}

func (e *env) manager(binDir string) *tailscale.Manager {
	return tailscale.New(tailscale.Options{
		Home: e.home, Port: halPort, CLI: tailscale.CLI{Path: binDir, Exact: true},
		CacheTTL: time.Nanosecond, StartupRetry: []time.Duration{time.Millisecond, time.Millisecond},
	})
}

func (e *env) writeConfig(s string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.home, "config.yaml"), []byte(s), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) config() string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.home, "config.yaml"))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

// removeHost deletes an allowed_hosts entry from config.yaml, as the user would.
func (e *env) removeHost(host string) {
	e.t.Helper()
	out, changed, err := spec.EditConfigYAML([]byte(e.config()), func(root *yaml.Node) (bool, error) { return spec.RemoveAllowedHost(root, host), nil })
	if err != nil || !changed {
		e.t.Fatalf("removeHost %s: changed %v, %v", host, changed, err)
	}
	e.writeConfig(string(out))
}

func (e *env) cfg() spec.Config {
	e.t.Helper()
	cfg, ds := spec.CheckConfig([]byte(e.config()))
	if cfg == nil || spec.HasErrors(ds) {
		e.t.Fatalf("config.yaml is invalid: %v\n%s", ds, e.config())
	}
	return *cfg
}

func (e *env) enable(req tailscale.EnableRequest) tailscale.Report {
	e.t.Helper()
	r, err := e.m.Enable(context.Background(), req)
	if err != nil {
		e.t.Fatalf("Enable: %v", err)
	}
	return r
}

func (e *env) warnings(r tailscale.Report) []string {
	var codes []string
	for _, w := range r.Warnings {
		codes = append(codes, w.Code)
	}
	return codes
}

func (e *env) statusState() string {
	b, err := os.ReadFile(filepath.Join(e.home, tailscale.StateFile))
	if err != nil {
		return ""
	}
	return string(b)
}

var serveCall = "serve --bg --yes --https=443 http://127.0.0.1:9001"

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func TestDetectionOfAReadyMachine(t *testing.T) {
	e := newEnv(t)
	r := e.m.Status(context.Background(), true)
	if !r.CLI.Found || str(r.CLI.Version) != "1.80.2" || str(r.CLI.Path) != filepath.Join(e.stub.BinDir, "tailscale") {
		t.Errorf("cli = %+v", r.CLI)
	}
	if !r.LoggedIn || str(r.BackendState) != "Running" || str(r.DNSName) != tailscaletest.DefaultDNS || str(r.Tailnet) != "example-tailnet.org" ||
		str(r.OwnerLogin) != tailscaletest.DefaultOwner || !r.HTTPSEnabled || !r.MagicDNSEnabled {
		t.Errorf("report = %+v", r)
	}
	if str(r.URL) != "https://"+tailscaletest.DefaultDNS+"/" || r.Serve.Active || r.Serve.Enabled || r.Serve.HTTPSPort != 443 ||
		r.Serve.Target != "http://127.0.0.1:9001" {
		t.Errorf("url/serve = %s %+v", str(r.URL), r.Serve)
	}
	if !slices.Equal(r.AllowedLogins, []string{tailscaletest.DefaultOwner}) {
		t.Errorf("allowed logins = %v, want the owner when allowed_logins is empty", r.AllowedLogins)
	}
	if len(r.Warnings) != 0 || r.Error != nil {
		t.Errorf("warnings %v, error %v", e.warnings(r), r.Error)
	}
}

func TestDetectionProblems(t *testing.T) {
	cases := []struct {
		name  string
		setup func(e *env)
		want  string
		check func(t *testing.T, r tailscale.Report)
	}{
		{"cli missing", func(e *env) { e.m = e.manager(t.TempDir()) }, tailscale.WarnCLINotFound, func(t *testing.T, r tailscale.Report) {
			if r.CLI.Found || r.Error == nil || r.LoggedIn {
				t.Errorf("report = %+v", r)
			}
		}},
		{"tailscaled down", func(e *env) { e.stub.Write("down", "") }, tailscale.WarnDaemonUnreachable, func(t *testing.T, r tailscale.Report) {
			if !r.CLI.Found || !strings.Contains(str(r.Error), "unavailable") {
				t.Errorf("report = %+v error %q", r.CLI, str(r.Error))
			}
		}},
		{"signed out", func(e *env) { e.stub.SetStatus(tailscaletest.StatusOpts{BackendState: "NeedsLogin"}) }, tailscale.WarnLoggedOut, func(t *testing.T, r tailscale.Report) {
			if r.LoggedIn || str(r.Warnings[0].Command) != "tailscale up" {
				t.Errorf("report = %+v", r.Warnings)
			}
		}},
		{"stopped", func(e *env) { e.stub.SetStatus(tailscaletest.StatusOpts{BackendState: "Stopped"}) }, tailscale.WarnStopped, nil},
		{"needs machine approval", func(e *env) { e.stub.SetStatus(tailscaletest.StatusOpts{BackendState: "NeedsMachineAuth"}) }, tailscale.WarnNeedsMachineAuth, nil},
		{"https certificates off", func(e *env) { e.stub.SetStatus(tailscaletest.StatusOpts{NoHTTPS: true}) }, tailscale.WarnHTTPSDisabled, func(t *testing.T, r tailscale.Report) {
			if r.HTTPSEnabled || !strings.Contains(str(r.Warnings[0].URL), "login.tailscale.com/admin/dns") {
				t.Errorf("report = %+v", r.Warnings)
			}
		}},
		{"magicdns off", func(e *env) { e.stub.SetStatus(tailscaletest.StatusOpts{NoMagicDNS: true}) }, tailscale.WarnMagicDNSDisabled, nil},
		{"funnel on", func(e *env) { e.stub.Serve(443, "http://127.0.0.1:3000"); e.stub.Write("funnel", "") }, tailscale.WarnFunnelEnabled, func(t *testing.T, r tailscale.Report) {
			if !r.Serve.Funnel {
				t.Error("funnel not reported")
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			c.setup(e)
			r := e.m.Status(context.Background(), true)
			if len(r.Warnings) == 0 || r.Warnings[0].Code != c.want {
				t.Fatalf("warnings = %v, want %s first", e.warnings(r), c.want)
			}
			if c.check != nil {
				c.check(t, r)
			}
		})
	}
}

func TestEnableRunsServeAndWritesTheConfigOnce(t *testing.T) {
	e := newEnv(t)
	before := e.config()
	r := e.enable(tailscale.EnableRequest{})

	if got := e.stub.Mutations(); !slices.Equal(got, []string{serveCall}) {
		t.Fatalf("serve invocations = %q, want exactly %q", got, serveCall)
	}
	if e.stub.Served(443) != "http://127.0.0.1:9001" {
		t.Errorf("mapping = %q", e.stub.Served(443))
	}
	cfg := e.cfg()
	if !slices.Equal(cfg.AllowedHosts, []string{tailscaletest.DefaultDNS}) || !cfg.Tailscale.Serve || cfg.Tailscale.HTTPSPort != 443 {
		t.Errorf("config: allowed_hosts %v, tailscale %+v", cfg.AllowedHosts, cfg.Tailscale)
	}
	// Comments survive the edit.
	for _, c := range []string{"# hal9001 config. Hot-reloaded on change.", "# Hostnames (besides localhost:9001", "# Logins accepted by the tailscale-identity auth module.", "# ACP agent runtimes"} {
		if !strings.Contains(e.config(), c) {
			t.Errorf("config.yaml lost the comment %q:\n%s", c, e.config())
		}
	}
	if !r.Serve.Enabled || !r.Serve.Active || !r.Serve.PointsAtHal || r.Serve.Conflict != nil ||
		str(r.URL) != "https://"+tailscaletest.DefaultDNS+"/" || !r.AllowedHost.Present || !r.AllowedHost.AddedByHal || len(r.Warnings) != 0 {
		t.Errorf("report after enable = %+v warnings %v", r.Serve, e.warnings(r))
	}

	// Enabling again changes nothing: no command, no config write.
	after := e.config()
	e.enable(tailscale.EnableRequest{})
	if got := e.stub.Mutations(); len(got) != 1 {
		t.Errorf("re-enable ran serve again: %q", got)
	}
	if e.config() != after {
		t.Errorf("re-enable rewrote config.yaml")
	}
	if before == after {
		t.Error("enable did not change config.yaml")
	}
}

func TestEnableOnAnotherPortUsesTheHostWithPort(t *testing.T) {
	e := newEnv(t)
	r := e.enable(tailscale.EnableRequest{HTTPSPort: 8443})
	want := "serve --bg --yes --https=8443 http://127.0.0.1:9001"
	if got := e.stub.Mutations(); !slices.Equal(got, []string{want}) {
		t.Fatalf("serve invocations = %q, want %q", got, want)
	}
	cfg := e.cfg()
	if !slices.Equal(cfg.AllowedHosts, []string{tailscaletest.DefaultDNS + ":8443"}) || cfg.Tailscale.HTTPSPort != 8443 {
		t.Errorf("config: allowed_hosts %v, tailscale %+v", cfg.AllowedHosts, cfg.Tailscale)
	}
	if str(r.URL) != "https://"+tailscaletest.DefaultDNS+":8443/" {
		t.Errorf("url = %s", str(r.URL))
	}

	// Moving back to 443 takes the 8443 mapping and the entry hal9001 added down.
	e.enable(tailscale.EnableRequest{HTTPSPort: 443})
	if e.stub.Served(8443) != "" || e.stub.Served(443) != "http://127.0.0.1:9001" {
		t.Errorf("mappings after the move: 8443=%q 443=%q", e.stub.Served(8443), e.stub.Served(443))
	}
	if got := e.cfg().AllowedHosts; !slices.Equal(got, []string{tailscaletest.DefaultDNS}) {
		t.Errorf("allowed_hosts after the move = %v", got)
	}
}

func TestEnableKeepsAHostTheUserAlreadyHad(t *testing.T) {
	e := newEnv(t)
	e.writeConfig("# mine\nallowed_hosts:\n  - other.example.com\n  - " + strings.ToUpper(tailscaletest.DefaultDNS) + " # added by me\n")
	r := e.enable(tailscale.EnableRequest{})
	cfg := e.cfg()
	if !slices.Equal(cfg.AllowedHosts, []string{"other.example.com", strings.ToUpper(tailscaletest.DefaultDNS)}) {
		t.Errorf("allowed_hosts = %v: the existing entry (any case) must be kept and not duplicated", cfg.AllowedHosts)
	}
	if !strings.Contains(e.config(), "# mine") || !strings.Contains(e.config(), "# added by me") {
		t.Errorf("comments lost:\n%s", e.config())
	}
	if r.AllowedHost.AddedByHal || e.statusState() != "" {
		t.Errorf("an entry the user wrote was recorded as added by hal9001: %+v state %q", r.AllowedHost, e.statusState())
	}

	// Disabling leaves it, and the other one.
	if _, err := e.m.Disable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.cfg().AllowedHosts; len(got) != 2 {
		t.Errorf("allowed_hosts after disable = %v, want both user entries kept", got)
	}
}

func TestEnableRefusesAPortServingSomethingElseUnlessReplaced(t *testing.T) {
	e := newEnv(t)
	e.stub.Serve(443, "http://127.0.0.1:3000")
	before := e.config()

	_, err := e.m.Enable(context.Background(), tailscale.EnableRequest{})
	var conflict *tailscale.ConflictError
	if !errors.As(err, &conflict) || conflict.Target != "http://127.0.0.1:3000" || conflict.HTTPSPort != 443 {
		t.Fatalf("err = %v, want a ConflictError naming the other target", err)
	}
	if len(e.stub.Mutations()) != 0 || e.config() != before || e.stub.Served(443) != "http://127.0.0.1:3000" {
		t.Errorf("a refused enable changed something: %q", e.stub.Mutations())
	}
	r := e.m.Status(context.Background(), true)
	if r.Serve.Conflict == nil || *r.Serve.Conflict != "http://127.0.0.1:3000" || r.Serve.PointsAtHal {
		t.Errorf("status = %+v", r.Serve)
	}

	e.enable(tailscale.EnableRequest{Replace: true})
	if e.stub.Served(443) != "http://127.0.0.1:9001" {
		t.Errorf("mapping after replace = %q", e.stub.Served(443))
	}
}

func TestEnableRefusesWhenTailscaleIsNotReady(t *testing.T) {
	cases := map[string]struct {
		setup func(e *env)
		code  string
	}{
		"signed out":  {func(e *env) { e.stub.SetStatus(tailscaletest.StatusOpts{BackendState: "NeedsLogin"}) }, tailscale.WarnLoggedOut},
		"https off":   {func(e *env) { e.stub.SetStatus(tailscaletest.StatusOpts{NoHTTPS: true}) }, tailscale.WarnHTTPSDisabled},
		"cli missing": {func(e *env) { e.m = e.manager(t.TempDir()) }, tailscale.WarnCLINotFound},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			c.setup(e)
			before := e.config()
			_, err := e.m.Enable(context.Background(), tailscale.EnableRequest{})
			var nr *tailscale.NotReadyError
			if !errors.As(err, &nr) || nr.Warnings[0].Code != c.code {
				t.Fatalf("err = %v, want NotReadyError with %s", err, c.code)
			}
			if len(e.stub.Mutations()) != 0 || e.config() != before {
				t.Error("a refused enable changed something")
			}
		})
	}
}

func TestEnableReportsAFailedCommand(t *testing.T) {
	e := newEnv(t)
	e.stub.Write("serve-fail", "Serve is not enabled on your tailnet.\nTo enable, visit: https://login.tailscale.com/f/serve?node=n123\n")
	before := e.config()
	_, err := e.m.Enable(context.Background(), tailscale.EnableRequest{})
	var ce *tailscale.CommandError
	if !errors.As(err, &ce) || !strings.Contains(ce.Output, "https://login.tailscale.com/f/serve") {
		t.Fatalf("err = %v, want a CommandError with the CLI's output", err)
	}
	if e.config() != before {
		t.Error("config.yaml changed although serve failed")
	}
}

func TestDisableRemovesOnlyHalsMappingAndHalsHost(t *testing.T) {
	e := newEnv(t)
	e.writeConfig("allowed_hosts: [mine.example.com]\n")
	e.enable(tailscale.EnableRequest{})
	if got := e.cfg().AllowedHosts; !slices.Equal(got, []string{"mine.example.com", tailscaletest.DefaultDNS}) {
		t.Fatalf("allowed_hosts = %v", got)
	}
	r, err := e.m.Disable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := e.stub.Mutations(); !slices.Equal(got, []string{serveCall, "serve --https=443 off"}) {
		t.Errorf("serve invocations = %q", got)
	}
	cfg := e.cfg()
	if !slices.Equal(cfg.AllowedHosts, []string{"mine.example.com"}) || cfg.Tailscale.Serve {
		t.Errorf("config after disable: allowed_hosts %v serve %v", cfg.AllowedHosts, cfg.Tailscale.Serve)
	}
	if r.Serve.Enabled || r.Serve.Active || e.statusState() != "" {
		t.Errorf("report = %+v, state %q", r.Serve, e.statusState())
	}

	// Disabling again is a no-op, and a mapping to something else is never taken down.
	e.stub.Serve(443, "http://127.0.0.1:3000")
	if _, err := e.m.Disable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.stub.Served(443) != "http://127.0.0.1:3000" || len(e.stub.Mutations()) != 2 {
		t.Errorf("disable touched a mapping that is not hal9001's: %q %q", e.stub.Served(443), e.stub.Mutations())
	}
}

func TestDisableWithoutTheCLIOnlyTurnsTheSettingOff(t *testing.T) {
	e := newEnv(t)
	e.enable(tailscale.EnableRequest{})
	e.m = e.manager(t.TempDir())
	if _, err := e.m.Disable(context.Background()); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if e.cfg().Tailscale.Serve {
		t.Error("tailscale.serve is still true")
	}
}

func TestDisableFailsWhenTailscaledIsDown(t *testing.T) {
	e := newEnv(t)
	e.enable(tailscale.EnableRequest{})
	e.stub.Write("down", "")
	if _, err := e.m.Disable(context.Background()); !errors.Is(err, tailscale.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if !e.cfg().Tailscale.Serve {
		t.Error("tailscale.serve was turned off although the mapping could not be removed")
	}
}

func TestReconcilePutsTheMappingBack(t *testing.T) {
	e := newEnv(t)
	e.enable(tailscale.EnableRequest{})
	e.stub.Remove("serve-443") // Tailscale was reset
	e.m.Reconcile(context.Background())
	if got := e.stub.Mutations(); !slices.Equal(got, []string{serveCall, serveCall}) || e.stub.Served(443) != "http://127.0.0.1:9001" {
		t.Errorf("serve invocations = %q, mapping %q", got, e.stub.Served(443))
	}
	if r := e.m.Status(context.Background(), true); len(r.Warnings) != 0 {
		t.Errorf("warnings = %v", e.warnings(r))
	}

	// With the mapping in place it does nothing.
	e.m.Reconcile(context.Background())
	if len(e.stub.Mutations()) != 2 {
		t.Errorf("reconcile ran serve although the mapping exists: %q", e.stub.Mutations())
	}
}

func TestReconcileDoesNothingWhenSharingIsOff(t *testing.T) {
	e := newEnv(t)
	before := e.config()
	e.m.Reconcile(context.Background())
	if len(e.stub.Calls()) != 0 || e.config() != before {
		t.Errorf("calls %q", e.stub.Calls())
	}
}

func TestReconcileWaitsForTailscaledToComeUp(t *testing.T) {
	e := newEnv(t)
	e.enable(tailscale.EnableRequest{})
	e.stub.Remove("serve-443")
	e.stub.Write("down", "")
	done := make(chan struct{})
	go func() { e.m.Reconcile(context.Background()); close(done) }()
	time.Sleep(10 * time.Millisecond)
	e.stub.Remove("down")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Reconcile did not finish")
	}
	if e.stub.Served(443) != "http://127.0.0.1:9001" {
		t.Errorf("mapping = %q: the retry did not re-apply it", e.stub.Served(443))
	}
}

func TestReconcileSurfacesFailuresAndNeverReplaces(t *testing.T) {
	e := newEnv(t)
	e.enable(tailscale.EnableRequest{})
	e.stub.Serve(443, "http://127.0.0.1:3000") // something else took the port
	e.m.Reconcile(context.Background())
	if e.stub.Served(443) != "http://127.0.0.1:3000" {
		t.Errorf("reconcile replaced a mapping that is not hal9001's: %q", e.stub.Served(443))
	}
	r := e.m.Status(context.Background(), true)
	codes := e.warnings(r)
	if !slices.Contains(codes, tailscale.WarnReapplyFailed) || !slices.Contains(codes, tailscale.WarnPortConflict) {
		t.Errorf("warnings = %v, want reapply_failed and port_conflict", codes)
	}

	// Once the port is free again, the next start repairs it and the warning goes away.
	e.stub.Remove("serve-443")
	e.m.Reconcile(context.Background())
	if r := e.m.Status(context.Background(), true); len(r.Warnings) != 0 || !r.Serve.PointsAtHal {
		t.Errorf("after the repair: %v %+v", e.warnings(r), r.Serve)
	}
}

func TestReconcileFollowsAMagicDNSRename(t *testing.T) {
	e := newEnv(t)
	e.enable(tailscale.EnableRequest{})
	e.stub.SetStatus(tailscaletest.StatusOpts{DNSName: "hal-mac-2.example-tailnet.ts.net"})
	e.m.Reconcile(context.Background())

	hosts := e.cfg().AllowedHosts
	if !slices.Equal(hosts, []string{tailscaletest.DefaultDNS, "hal-mac-2.example-tailnet.ts.net"}) {
		t.Fatalf("allowed_hosts = %v: the new name is added and the old one is kept", hosts)
	}
	r := e.m.Status(context.Background(), true)
	if !slices.Contains(e.warnings(r), tailscale.WarnHostRenamed) {
		t.Fatalf("warnings = %v, want host_renamed", e.warnings(r))
	}
	for _, w := range r.Warnings {
		if w.Code == tailscale.WarnHostRenamed && !strings.Contains(w.Message, tailscaletest.DefaultDNS) {
			t.Errorf("message %q does not name the old host", w.Message)
		}
	}
	if !r.AllowedHost.Present || str(r.AllowedHost.Entry) != "hal-mac-2.example-tailnet.ts.net" {
		t.Errorf("allowed_host = %+v", r.AllowedHost)
	}

	// The user removes the old name: the notice goes.
	e.removeHost(tailscaletest.DefaultDNS)
	if r := e.m.Status(context.Background(), true); slices.Contains(e.warnings(r), tailscale.WarnHostRenamed) {
		t.Errorf("host_renamed stays after the old entry is gone: %v", e.warnings(r))
	}
}

func TestStatusWarnsWhenSharingIsOnButTheMappingOrHostIsMissing(t *testing.T) {
	e := newEnv(t)
	e.enable(tailscale.EnableRequest{})
	e.stub.Remove("serve-443")
	e.removeHost(tailscaletest.DefaultDNS)
	codes := e.warnings(e.m.Status(context.Background(), true))
	if !slices.Contains(codes, tailscale.WarnServeMissing) || !slices.Contains(codes, tailscale.WarnHostMissing) {
		t.Errorf("warnings = %v, want serve_missing and host_missing", codes)
	}
}

func TestCLILocatesOnlyWhereItIsTold(t *testing.T) {
	stub := tailscaletest.New(t)
	if _, err := (tailscale.CLI{Path: t.TempDir(), Exact: true}).Locate(); !errors.Is(err, tailscale.ErrUnavailable) {
		t.Errorf("empty PATH: err = %v", err)
	}
	if _, err := (tailscale.CLI{Exact: true}).Locate(); !errors.Is(err, tailscale.ErrUnavailable) {
		t.Errorf("Exact without a PATH must find nothing, got err = %v", err)
	}
	got, err := (tailscale.CLI{Path: stub.BinDir, Exact: true}).Locate()
	if err != nil || got != filepath.Join(stub.BinDir, "tailscale") {
		t.Errorf("Locate = %q, %v", got, err)
	}
}

func TestCLIErrors(t *testing.T) {
	stub := tailscaletest.New(t)
	cli := tailscale.CLI{Path: stub.BinDir, Exact: true}
	stub.Write("down", "")
	if _, err := cli.Status(context.Background()); !errors.Is(err, tailscale.ErrUnavailable) {
		t.Errorf("tailscaled down: err = %v", err)
	}
	stub.Remove("down")
	// stderr noise (the client/server version warning) must not end up in the JSON.
	if st, err := cli.Status(context.Background()); err != nil || st.Hostname() != tailscaletest.DefaultDNS {
		t.Errorf("Status = %+v, %v", st, err)
	}
}

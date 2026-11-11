package api

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"hal9001/internal/spec"
	"hal9001/internal/tailscale"
	"hal9001/internal/tailscale/tailscaletest"
)

type tsEnv struct {
	*env
	stub *tailscaletest.Stub
}

// newTailscaleEnv is an API over the real router with a Tailscale manager that can only reach the stub.
func newTailscaleEnv(t *testing.T) *tsEnv {
	t.Helper()
	stub := tailscaletest.New(t)
	e := newEnv(t, func(eo *envOptions) {
		eo.tailscale = func(e *env) *tailscale.Manager {
			return tailscale.New(tailscale.Options{
				Home: e.home, Port: 9001, CLI: tailscale.CLI{Path: stub.BinDir, Exact: true}, CacheTTL: time.Nanosecond,
			})
		}
	})
	return &tsEnv{env: e, stub: stub}
}

type tsStatusBody struct {
	CLI struct {
		Found   bool    `json:"found"`
		Version *string `json:"version"`
	} `json:"cli"`
	LoggedIn bool    `json:"logged_in"`
	DNSName  *string `json:"dns_name"`
	URL      *string `json:"url"`
	Serve    struct {
		Enabled     bool    `json:"enabled"`
		HTTPSPort   int     `json:"https_port"`
		Target      string  `json:"target"`
		PointsAtHal bool    `json:"points_at_hal"`
		Conflict    *string `json:"conflict"`
	} `json:"serve"`
	AllowedHost struct {
		Present    bool `json:"present"`
		AddedByHal bool `json:"added_by_hal"`
	} `json:"allowed_host"`
	Warnings []struct {
		Code string `json:"code"`
	} `json:"warnings"`
}

func (b tsStatusBody) codes() []string {
	var out []string
	for _, w := range b.Warnings {
		out = append(out, w.Code)
	}
	return out
}

func (e *tsEnv) config() spec.Config {
	e.t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.home, "config.yaml"))
	if err != nil {
		e.t.Fatal(err)
	}
	cfg, ds := spec.CheckConfig(raw)
	if cfg == nil || spec.HasErrors(ds) {
		e.t.Fatalf("config.yaml invalid: %v", ds)
	}
	return *cfg
}

func TestTailscaleOperationsAnswer503WithoutAManager(t *testing.T) {
	e := newEnv(t)
	e.do(http.MethodGet, "/api/tailscale", nil).want(503)
	e.do(http.MethodPost, "/api/tailscale/serve", nil).want(503)
}

func TestTailscaleStatusThroughTheRouter(t *testing.T) {
	e := newTailscaleEnv(t)
	var st tsStatusBody
	e.do(http.MethodGet, "/api/tailscale", nil).want(200).decode(&st)
	if !st.CLI.Found || st.CLI.Version == nil || *st.CLI.Version != "1.80.2" || !st.LoggedIn || st.DNSName == nil || *st.DNSName != tailscaletest.DefaultDNS ||
		st.URL == nil || *st.URL != "https://"+tailscaletest.DefaultDNS+"/" || st.Serve.Enabled || st.Serve.Target != "http://127.0.0.1:9001" || len(st.Warnings) != 0 {
		t.Errorf("status = %+v", st)
	}

	e.stub.SetStatus(tailscaletest.StatusOpts{BackendState: "NeedsLogin"})
	e.do(http.MethodPost, "/api/tailscale/refresh", nil).want(200).decode(&st)
	if st.LoggedIn || !slices.Equal(st.codes(), []string{"logged_out"}) {
		t.Errorf("after sign-out: %+v", st)
	}
	if len(e.stub.Mutations()) != 0 {
		t.Errorf("status commands changed Tailscale: %q", e.stub.Mutations())
	}
}

func TestEnableTailscaleServeThroughTheRouter(t *testing.T) {
	e := newTailscaleEnv(t)
	var st tsStatusBody
	e.do(http.MethodPost, "/api/tailscale/serve", nil).want(200).decode(&st)
	if got := e.stub.Mutations(); !slices.Equal(got, []string{"serve --bg --yes --https=443 http://127.0.0.1:9001"}) {
		t.Fatalf("serve invocations = %q", got)
	}
	if !st.Serve.Enabled || !st.Serve.PointsAtHal || !st.AllowedHost.Present || !st.AllowedHost.AddedByHal {
		t.Errorf("status = %+v", st)
	}
	cfg := e.config()
	if !slices.Equal(cfg.AllowedHosts, []string{tailscaletest.DefaultDNS}) || !cfg.Tailscale.Serve {
		t.Errorf("config = %v %+v", cfg.AllowedHosts, cfg.Tailscale)
	}
	if e.reloads.Load() == 0 {
		t.Error("the daemon was not asked to reload the config")
	}

	// GET /api/settings shows the section; the request body is validated.
	var settings struct {
		Config struct {
			AllowedHosts []string `json:"allowed_hosts"`
			Tailscale    struct {
				Serve     bool `json:"serve"`
				HTTPSPort int  `json:"https_port"`
			} `json:"tailscale"`
		} `json:"config"`
	}
	e.do(http.MethodGet, "/api/settings", nil).want(200).decode(&settings)
	if !settings.Config.Tailscale.Serve || settings.Config.Tailscale.HTTPSPort != 443 || !slices.Equal(settings.Config.AllowedHosts, []string{tailscaletest.DefaultDNS}) {
		t.Errorf("settings = %+v", settings.Config)
	}
	e.do(http.MethodPost, "/api/tailscale/serve", map[string]any{"https_port": 0}).want(422)
	e.do(http.MethodPost, "/api/tailscale/serve", map[string]any{"https_port": 70000}).want(422)
	e.do(http.MethodPost, "/api/tailscale/serve", map[string]any{"port": 443}).want(400)
}

func TestEnableTailscaleServeConflictAndReplace(t *testing.T) {
	e := newTailscaleEnv(t)
	e.stub.Serve(443, "http://127.0.0.1:3000")
	r := e.do(http.MethodPost, "/api/tailscale/serve", nil).want(409)
	if r.problem() != "already-exists" || r.json()["target"] != "http://127.0.0.1:3000" || r.json()["https_port"] != float64(443) {
		t.Errorf("problem = %s", r.Body)
	}
	if len(e.stub.Mutations()) != 0 || len(e.config().AllowedHosts) != 0 {
		t.Error("a refused enable changed something")
	}
	e.do(http.MethodPost, "/api/tailscale/serve", map[string]any{"replace": true}).want(200)
	if e.stub.Served(443) != "http://127.0.0.1:9001" {
		t.Errorf("mapping = %q", e.stub.Served(443))
	}
}

func TestEnableTailscaleServeWhenNotReadyOrFailing(t *testing.T) {
	e := newTailscaleEnv(t)
	e.stub.SetStatus(tailscaletest.StatusOpts{NoHTTPS: true})
	r := e.do(http.MethodPost, "/api/tailscale/serve", nil).want(409)
	warnings, _ := r.json()["warnings"].([]any)
	if r.problem() != "invalid-state" || len(warnings) != 1 || warnings[0].(map[string]any)["code"] != "https_disabled" {
		t.Errorf("problem = %s", r.Body)
	}

	e.stub.SetStatus(tailscaletest.StatusOpts{})
	e.stub.Write("serve-fail", "Serve is not enabled on your tailnet.\nTo enable, visit: https://login.tailscale.com/f/serve?node=n123\n")
	r = e.do(http.MethodPost, "/api/tailscale/serve", nil).want(503)
	if r.problem() != "unavailable" || !strings.Contains(r.json()["detail"].(string), "https://login.tailscale.com/f/serve") {
		t.Errorf("problem = %s", r.Body)
	}
}

func TestDisableTailscaleServeThroughTheRouter(t *testing.T) {
	e := newTailscaleEnv(t)
	e.do(http.MethodPost, "/api/tailscale/serve", nil).want(200)
	var st tsStatusBody
	e.do(http.MethodDelete, "/api/tailscale/serve", nil).want(200).decode(&st)
	if got := e.stub.Mutations(); !slices.Equal(got, []string{"serve --bg --yes --https=443 http://127.0.0.1:9001", "serve --https=443 off"}) {
		t.Errorf("serve invocations = %q", got)
	}
	if st.Serve.Enabled || st.Serve.PointsAtHal || st.AllowedHost.Present {
		t.Errorf("status = %+v", st)
	}
	if cfg := e.config(); cfg.Tailscale.Serve || len(cfg.AllowedHosts) != 0 {
		t.Errorf("config = %v %+v", cfg.AllowedHosts, cfg.Tailscale)
	}
}

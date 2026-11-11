package api

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"hal9001/internal/httpsec"
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

// Sharing is tailnet-only: a funnel port is refused whatever it serves and with or without replace, and nothing is
// run or written (allowed_hosts and tailscale.serve stay as they were).
func TestEnableTailscaleServeRefusesAFunnelPort(t *testing.T) {
	cases := []struct {
		name    string
		mapping string
		body    any
	}{
		{"funnel to hal", "http://127.0.0.1:9001", nil},
		{"funnel to hal, replace", "http://127.0.0.1:9001", map[string]any{"replace": true}},
		{"funnel to something else", "http://127.0.0.1:3000", nil},
		{"funnel to something else, replace", "http://127.0.0.1:3000", map[string]any{"replace": true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTailscaleEnv(t)
			e.stub.Serve(443, c.mapping)
			e.stub.Write("funnel", "")
			before := e.config()

			r := e.do(http.MethodPost, "/api/tailscale/serve", c.body).want(409)
			detail, _ := r.json()["detail"].(string)
			if r.problem() != "invalid-state" || !strings.Contains(detail, "HTTPS port 443 is exposed to the public internet with tailscale funnel") ||
				!strings.Contains(detail, "`tailscale funnel --https=443 off`") || r.json()["https_port"] != float64(443) {
				t.Errorf("problem = %s", r.Body)
			}
			if got := e.stub.Mutations(); len(got) != 0 || e.stub.Served(443) != c.mapping {
				t.Errorf("serve was run: %q, mapping %q", got, e.stub.Served(443))
			}
			after := e.config()
			if !slices.Equal(after.AllowedHosts, before.AllowedHosts) || len(after.AllowedHosts) != 0 || after.Tailscale != before.Tailscale || after.Tailscale.Serve {
				t.Errorf("config changed: %v %+v", after.AllowedHosts, after.Tailscale)
			}
			if e.reloads.Load() != 0 {
				t.Error("a refused enable reloaded the config")
			}

			var st struct {
				Serve struct {
					FunnelOnPort bool `json:"funnel_on_port"`
				} `json:"serve"`
				Warnings []struct {
					Code    string  `json:"code"`
					Command *string `json:"command"`
				} `json:"warnings"`
			}
			e.do(http.MethodGet, "/api/tailscale", nil).want(200).decode(&st)
			var found bool
			for _, w := range st.Warnings {
				if w.Code == "funnel_on_port" && w.Command != nil && *w.Command == "tailscale funnel --https=443 off" {
					found = true
				}
			}
			if !st.Serve.FunnelOnPort || !found {
				t.Errorf("status = %+v", st)
			}
		})
	}
}

// A request that came through funnel is refused by the baseline in front of the router, even for an allowed host,
// and never reaches the API.
func TestFunnelRequestsAreRefusedBeforeTheAPI(t *testing.T) {
	e := newTailscaleEnv(t)
	e.do(http.MethodPost, "/api/tailscale/serve", nil).want(200)
	h := httpsec.Middleware(httpsec.Options{AllowedHosts: func() []string { return e.config().AllowedHosts }})(e.srv)

	for _, host := range []string{tailscaletest.DefaultDNS, "localhost:9001"} {
		req := request(http.MethodGet, "/api/tailscale", nil)
		req.Host = host
		req.Header.Set("Tailscale-Funnel-Request", "?1")
		if r := serve(t, h, req); r.Status != 403 || r.problem() != "forbidden" {
			t.Errorf("funnel request to %s: %d %s", host, r.Status, r.Body)
		}
	}
	// Without the header the request gets past the baseline and reaches auth, which does not admit it either: the
	// loopback peer and the allowed host are not enough for localhost-trust.
	req := request(http.MethodGet, "/api/tailscale", nil)
	req.Host = tailscaletest.DefaultDNS
	if r := serve(t, h, req); r.Status != 401 || r.problem() != "unauthenticated" {
		t.Errorf("proxied request without the funnel header: %d %s", r.Status, r.Body)
	}
}

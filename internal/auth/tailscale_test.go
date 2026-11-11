package auth_test

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"hal9001/internal/auth"
	"hal9001/internal/tailscale"
)

// Scenario 24: tailscale-identity with a fake whois.
func TestTailscaleIdentityScenarios(t *testing.T) {
	cfg := auth.DefaultConfig()
	cfg.AllowedHosts = []string{tsHost}

	setup := func(t *testing.T, mod func(*env)) *env {
		e := newEnv(t, withConfig(cfg))
		e.ts.setWhois(t, clientIP, "whois_user.json")
		e.ts.setWhois(t, "100.64.9.9", "whois_tagged.json")
		if mod != nil {
			mod(e)
		}
		return e
	}

	t.Run("header matches whois and login is the Mac owner: success", func(t *testing.T) {
		e := setup(t, nil)
		rec := e.do(get("/api/ping/api").viaServe("alice@example.com", clientIP))
		wantStatus(t, rec, 200)
		if got := decode[map[string]string](t, rec); got["principal"] != "alice@example.com" {
			t.Errorf("principal = %v", got)
		}
		ev := e.lastEvent()
		if ev.Outcome != auth.OutcomeSuccess || ev.Principal != "alice@example.com" || ev.Host != tsHost ||
			joinResults(ev) != "localhost-trust:ignore,tailscale-identity:success" {
			t.Errorf("event = %+v (%s)", ev, joinResults(ev))
		}
	})

	t.Run("header login differs from whois login: fail", func(t *testing.T) {
		e := setup(t, nil)
		rec := e.do(get("/api/ping/api").viaServe("bob@example.com", clientIP))
		wantStatus(t, rec, 401)
		ev := e.lastEvent()
		if !strings.Contains(joinResults(ev), "tailscale-identity:fail") || !strings.Contains(ev.Modules[1].Reason, "does not match whois") {
			t.Errorf("event modules = %+v", ev.Modules)
		}
	})

	t.Run("login not in allowed_logins: fail", func(t *testing.T) {
		e := setup(t, nil)
		c := e.m.Config()
		c.AllowedLogins = []string{"carol@example.com"}
		if err := e.m.Reload(c); err != nil {
			t.Fatal(err)
		}
		rec := e.do(get("/api/ping/api").viaServe("alice@example.com", clientIP))
		wantStatus(t, rec, 401)
		if r := e.lastEvent().Modules[1].Reason; !strings.Contains(r, "not in allowed_logins") {
			t.Errorf("reason = %q", r)
		}
	})

	t.Run("allowed_logins overrides the Mac owner default", func(t *testing.T) {
		e := setup(t, nil)
		c := e.m.Config()
		c.AllowedLogins = []string{"Alice@Example.com"}
		if err := e.m.Reload(c); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, e.do(get("/api/ping/api").viaServe("alice@example.com", clientIP)), 200)
	})

	t.Run("default allowed login is the Mac owner, not any tailnet user", func(t *testing.T) {
		e := setup(t, func(e *env) {
			bob := strings.Replace(string(fixture(t, "whois_user.json")), "alice@example.com", "bob@example.com", 1)
			w, err := tailscale.ParseWhois([]byte(bob))
			if err != nil {
				t.Fatal(err)
			}
			e.ts.whois[netip.MustParseAddr("100.101.102.200")] = w
		})
		rec := e.do(get("/api/ping/api").viaServe("bob@example.com", "100.101.102.200"))
		wantStatus(t, rec, 401)
		if r := e.lastEvent().Modules[1].Reason; !strings.Contains(r, "not in allowed_logins") {
			t.Errorf("reason = %q", r)
		}
	})

	t.Run("tagged device: fail", func(t *testing.T) {
		e := setup(t, nil)
		// Serve does not send a login for tagged nodes; a forged header must not help.
		rec := e.do(get("/api/ping/api").viaServe("alice@example.com", "100.64.9.9"))
		wantStatus(t, rec, 401)
		if r := e.lastEvent().Modules[1].Reason; !strings.Contains(r, "tagged device") {
			t.Errorf("reason = %q", r)
		}
	})

	t.Run("client ip unknown to whois: fail", func(t *testing.T) {
		e := setup(t, nil)
		wantStatus(t, e.do(get("/api/ping/api").viaServe("alice@example.com", "100.99.99.99")), 401)
		if got := e.lastEvent().Modules[1].Result; got != auth.KindFail {
			t.Errorf("result = %s, want fail", got)
		}
	})

	t.Run("missing or invalid X-Forwarded-For: fail", func(t *testing.T) {
		e := setup(t, nil)
		wantStatus(t, e.do(get("/api/ping/api").viaServe("alice@example.com", "not-an-ip")), 401)
		r := get("/api/ping/api").viaServe("alice@example.com", "")
		delete(r.headers, "X-Forwarded-For")
		wantStatus(t, e.do(r), 401)
	})

	t.Run("a client-supplied leading X-Forwarded-For entry is not trusted", func(t *testing.T) {
		e := setup(t, nil)
		// 100.101.102.103 (alice) is spoofed first; serve's own entry (an unknown ip) is last.
		wantStatus(t, e.do(get("/api/ping/api").viaServe("alice@example.com", clientIP+", 100.99.99.99")), 401)
	})

	t.Run("localhost Host: ignore", func(t *testing.T) {
		e := setup(t, nil)
		r := get("/api/ping/api").viaServe("alice@example.com", clientIP)
		r.host = localHost
		wantStatus(t, e.do(r), 401) // proxy headers also exclude localhost-trust
		ev := e.lastEvent()
		if ev.Modules[1].Module != "tailscale-identity" || ev.Modules[1].Result != auth.KindIgnore {
			t.Errorf("modules = %+v, want tailscale-identity ignore", ev.Modules)
		}
	})

	t.Run("ts.net host not in allowed_hosts: rejected by the baseline", func(t *testing.T) {
		e := setup(t, nil)
		r := get("/api/ping/api").viaServe("alice@example.com", clientIP)
		r.host = "other.tail1234.ts.net"
		wantStatus(t, e.do(r), 403)
	})

	t.Run("missing identity header: ignore", func(t *testing.T) {
		e := setup(t, nil)
		r := get("/api/ping/api").viaServe("", clientIP)
		delete(r.headers, "Tailscale-User-Login")
		wantStatus(t, e.do(r), 401)
		if ev := e.lastEvent(); ev.Modules[1].Result != auth.KindIgnore {
			t.Errorf("modules = %+v, want tailscale-identity ignore", ev.Modules)
		}
	})

	t.Run("tailscale CLI missing: ignore with the reason in auth events", func(t *testing.T) {
		e := newEnv(t, withConfig(cfg), withTailscale(tailscale.CLI{Binary: "/nonexistent/dir/tailscale"}))
		wantStatus(t, e.do(get("/api/ping/api").viaServe("alice@example.com", clientIP)), 401)
		var ts auth.ModuleTrace
		for _, m := range e.lastEvent().Modules {
			if m.Module == "tailscale-identity" {
				ts = m
			}
		}
		if ts.Result != auth.KindIgnore || !strings.Contains(ts.Reason, "tailscale unavailable") {
			t.Errorf("trace = %+v, want ignore with an unavailable reason", ts)
		}
	})

	t.Run("daemon unreachable: ignore", func(t *testing.T) {
		e := setup(t, func(e *env) { e.ts.err = tailscale.ErrUnavailable })
		wantStatus(t, e.do(get("/api/ping/api").viaServe("alice@example.com", clientIP)), 401)
		if got := e.lastEvent().Modules[1].Result; got != auth.KindIgnore {
			t.Errorf("result = %s, want ignore", got)
		}
	})
}

func TestTailscaleIdentityFallsThroughToLaterModules(t *testing.T) {
	cfg := auth.DefaultConfig()
	cfg.AllowedHosts = []string{tsHost}
	e := newEnv(t, withConfig(cfg))
	// tailscale-identity ignores (CLI unavailable) and the password module further down the stack still decides.
	e.ts.err = tailscale.ErrUnavailable
	rec := e.do(post("/api/auth/api/begin", map[string]string{"password": testPW}).viaServe("alice@example.com", clientIP))
	wantStatus(t, rec, 200)
	if got := decode[map[string]any](t, rec); got["principal"].(map[string]any)["name"] != "owner" {
		t.Errorf("response = %v", got)
	}
}

func TestWhoisIsCachedFor60Seconds(t *testing.T) {
	cfg := auth.DefaultConfig()
	cfg.AllowedHosts = []string{tsHost}
	e := newEnv(t, withConfig(cfg))
	e.ts.setWhois(t, clientIP, "whois_user.json")
	call := func() { wantStatus(t, e.do(get("/api/ping/api").viaServe("alice@example.com", clientIP)), 200) }

	call()
	e.clock.Advance(59 * time.Second)
	call()
	if e.ts.whoisCalls != 1 || e.ts.statusCalls != 1 {
		t.Errorf("after 59s: whois=%d status=%d calls, want 1 each (cached)", e.ts.whoisCalls, e.ts.statusCalls)
	}
	e.clock.Advance(2 * time.Second)
	call()
	if e.ts.whoisCalls != 2 {
		t.Errorf("after 61s: whois=%d calls, want 2 (cache expired)", e.ts.whoisCalls)
	}
}

package auth_test

import (
	"net/http"
	"strings"
	"testing"

	"hal9001/internal/auth"
)

// Scenario 18 end to end: the baseline sits in front of the auth endpoints
// and rejects before any auth decision (or auth event) happens.
func TestBaselineRejectsBeforeAuth(t *testing.T) {
	e := newEnv(t) // default stack: localhost-trust would otherwise admit these
	tests := []struct {
		name string
		req  *req
		want int
	}{
		{"cross-origin POST", post("/api/auth/ui/begin", map[string]string{}).hdr("Origin", "https://evil.example"), 403},
		{"text/plain POST", post("/api/auth/ui/begin", nil).hdr("Content-Type", "text/plain"), 415},
		{"DNS rebinding Host", get("/api/ping/admin").on("evil.example"), 403},
		{"DNS rebinding Host with port", get("/api/ping/admin").on("evil.example:9001"), 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantStatus(t, e.do(tt.req), tt.want)
		})
	}
	if n := len(e.events()); n != 0 {
		t.Errorf("%d auth events recorded for requests the baseline rejected", n)
	}
}

func TestSessionCookieFlags(t *testing.T) {
	login := func(t *testing.T, r *req) *http.Cookie {
		e := newEnv(t, withConfig(func() auth.Config {
			c := stackConfig(entry("password", auth.Sufficient))
			c.AllowedHosts = []string{tsHost}
			return c
		}()))
		return sessionCookie(t, e.do(r))
	}
	body := map[string]string{"password": testPW}

	t.Run("plain http on localhost", func(t *testing.T) {
		c := login(t, post("/api/auth/ui/begin", body))
		if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Secure || c.Path != "/" {
			t.Errorf("cookie = %+v, want HttpOnly SameSite=Strict, not Secure", c)
		}
	})
	t.Run("https via tailscale serve (loopback proxy, ts.net host)", func(t *testing.T) {
		c := login(t, post("/api/auth/ui/begin", body).viaServe("alice@example.com", clientIP))
		if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || !c.Secure {
			t.Errorf("cookie = %+v, want HttpOnly SameSite=Strict Secure", c)
		}
	})
	t.Run("ts.net host through a proxy that sends no X-Forwarded-Proto", func(t *testing.T) {
		r := post("/api/auth/ui/begin", body).on(tsHost)
		if c := login(t, r); !c.Secure {
			t.Errorf("cookie = %+v, want Secure", c)
		}
	})
	t.Run("direct TLS", func(t *testing.T) {
		c := login(t, post("/api/auth/ui/begin", body).viaHTTPS())
		if !c.Secure {
			t.Errorf("cookie = %+v, want Secure", c)
		}
	})
	t.Run("X-Forwarded-Proto from a non-loopback peer is not trusted", func(t *testing.T) {
		c := login(t, post("/api/auth/ui/begin", body).from("192.0.2.9:1").hdr("X-Forwarded-Proto", "https"))
		if c.Secure {
			t.Errorf("cookie = %+v: spoofed X-Forwarded-Proto marked the cookie Secure", c)
		}
	})
	t.Run("the cookie secret is never a hash of itself", func(t *testing.T) {
		c := login(t, post("/api/auth/ui/begin", body))
		if len(c.Value) < 40 || strings.ContainsAny(c.Value, " ;,") {
			t.Errorf("cookie value %q looks too short or unsafe", c.Value)
		}
	})
}

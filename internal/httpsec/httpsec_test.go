package httpsec_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hal9001/internal/httpsec"
)

func newHandler() http.Handler {
	mw := httpsec.Middleware(httpsec.Options{
		AllowedHosts: func() []string { return []string{"Mac.tail1234.ts.net"} },
	})
	return mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
}

func do(t *testing.T, method, host, contentType string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://"+host+"/api/x", strings.NewReader(`{}`))
	req.Host = host
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	newHandler().ServeHTTP(rec, req)
	return rec
}

func TestBaselineDecisions(t *testing.T) {
	const js = "application/json"
	tests := []struct {
		name        string
		method      string
		host        string
		contentType string
		hdr         map[string]string
		wantStatus  int
		wantType    string
	}{
		{"localhost GET", "GET", "localhost:9001", "", nil, 204, ""},
		{"127.0.0.1 GET", "GET", "127.0.0.1:9001", "", nil, 204, ""},
		{"json POST", "POST", "localhost:9001", js, nil, 204, ""},
		{"json with charset", "POST", "localhost:9001", "application/json; charset=utf-8", nil, 204, ""},
		{"allowed host is case-insensitive, no port", "GET", "mac.tail1234.ts.net", "", nil, 204, ""},
		{"DNS rebinding host", "GET", "evil.example", "", nil, 403, httpsec.TypeHostNotAllowed},
		{"rebinding host even with port 9001", "GET", "evil.example:9001", "", nil, 403, httpsec.TypeHostNotAllowed},
		{"localhost wrong port", "GET", "localhost:8080", "", nil, 403, httpsec.TypeHostNotAllowed},
		{"allowed host with unexpected port", "GET", "mac.tail1234.ts.net:9001", "", nil, 403, httpsec.TypeHostNotAllowed},
		{"same-origin Origin", "POST", "localhost:9001", js, map[string]string{"Origin": "http://localhost:9001"}, 204, ""},
		{"https Origin for allowed host", "POST", "mac.tail1234.ts.net", js, map[string]string{"Origin": "https://mac.tail1234.ts.net"}, 204, ""},
		{"cross-origin POST", "POST", "localhost:9001", js, map[string]string{"Origin": "https://evil.example"}, 403, httpsec.TypeOriginNotAllowed},
		{"cross-origin GET", "GET", "localhost:9001", "", map[string]string{"Origin": "https://evil.example"}, 403, httpsec.TypeOriginNotAllowed},
		{"null origin", "POST", "localhost:9001", js, map[string]string{"Origin": "null"}, 403, httpsec.TypeOriginNotAllowed},
		{"wrong scheme for loopback origin", "GET", "localhost:9001", "", map[string]string{"Origin": "https://localhost:9001"}, 403, httpsec.TypeOriginNotAllowed},
		{"plain http origin for ts.net host", "GET", "mac.tail1234.ts.net", "", map[string]string{"Origin": "http://mac.tail1234.ts.net"}, 403, httpsec.TypeOriginNotAllowed},
		{"origin with path", "GET", "localhost:9001", "", map[string]string{"Origin": "http://localhost:9001/x"}, 403, httpsec.TypeOriginNotAllowed},
		{"text/plain POST", "POST", "localhost:9001", "text/plain", nil, 415, httpsec.TypeUnsupportedMedia},
		{"form POST", "POST", "localhost:9001", "application/x-www-form-urlencoded", nil, 415, httpsec.TypeUnsupportedMedia},
		{"multipart POST", "POST", "localhost:9001", "multipart/form-data; boundary=x", nil, 415, httpsec.TypeUnsupportedMedia},
		{"POST without content type", "POST", "localhost:9001", "", nil, 415, httpsec.TypeUnsupportedMedia},
		{"PUT text/plain", "PUT", "localhost:9001", "text/plain", nil, 415, httpsec.TypeUnsupportedMedia},
		{"PATCH text/plain", "PATCH", "localhost:9001", "text/plain", nil, 415, httpsec.TypeUnsupportedMedia},
		{"DELETE text/plain", "DELETE", "localhost:9001", "text/plain", nil, 415, httpsec.TypeUnsupportedMedia},
		{"host check precedes content-type check", "POST", "evil.example", "text/plain", nil, 403, httpsec.TypeHostNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, tt.method, tt.host, tt.contentType, tt.hdr)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.wantType == "" {
				return
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q", ct)
			}
			var p struct {
				Type   string `json:"type"`
				Status int    `json:"status"`
				Title  string `json:"title"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			if p.Type != tt.wantType || p.Status != tt.wantStatus || p.Title == "" {
				t.Errorf("problem = %+v, want type %s status %d", p, tt.wantType, tt.wantStatus)
			}
		})
	}
}

func TestPreflightIsNeverGranted(t *testing.T) {
	rec := do(t, "OPTIONS", "localhost:9001", "", map[string]string{
		"Origin":                         "https://evil.example",
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "content-type",
	})
	if rec.Code != 403 {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	for k := range rec.Header() {
		if strings.HasPrefix(strings.ToLower(k), "access-control-") {
			t.Errorf("response carries CORS header %s", k)
		}
	}
}

func TestAllowedHostsReloadTakesEffect(t *testing.T) {
	hosts := []string{}
	h := httpsec.Middleware(httpsec.Options{AllowedHosts: func() []string { return hosts }})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	get := func() int {
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = "box.tail.ts.net"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := get(); got != 403 {
		t.Fatalf("before reload: %d", got)
	}
	hosts = []string{"box.tail.ts.net"}
	if got := get(); got != 204 {
		t.Fatalf("after reload: %d", got)
	}
}

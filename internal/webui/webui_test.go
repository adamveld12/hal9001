package webui_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hal9001/internal/webui"
)

func buildDist(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"index.html":            "<!doctype html><div id=root></div>",
		"favicon.svg":           "<svg/>",
		"assets/app-1a2b3c.js":  "console.log('app')",
		"assets/app-1a2b3c.css": "body{}",
	}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func get(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func newHandler(t *testing.T) http.Handler {
	t.Helper()
	h, err := webui.New(webui.Options{Dir: buildDist(t)})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestRootAndClientRoutesServeIndexWithoutCaching(t *testing.T) {
	h := newHandler(t)
	for _, target := range []string{"/", "/flows/triage-github", "/runs/01J0000000000000000000000/nodes/x", "/inbox"} {
		rec := get(t, h, http.MethodGet, target)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id=root`) {
			t.Errorf("GET %s = %d %q, want the SPA index", target, rec.Code, rec.Body)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q, want no-cache so a new build is picked up", target, cc)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET %s Content-Type = %q", target, ct)
		}
	}
}

func TestHashedAssetsAreLongCachedWithRealContentType(t *testing.T) {
	h := newHandler(t)
	rec := get(t, h, http.MethodGet, "/assets/app-1a2b3c.js")
	if rec.Code != http.StatusOK || rec.Body.String() != "console.log('app')" {
		t.Fatalf("asset = %d %q", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q, want javascript", ct)
	}
	if ct := get(t, h, http.MethodGet, "/assets/app-1a2b3c.css").Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("css Content-Type = %q", ct)
	}
}

func TestMissingAssetIs404NotIndex(t *testing.T) {
	rec := get(t, newHandler(t), http.MethodGet, "/assets/app-stale.js")
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "root") {
		t.Errorf("missing asset = %d %q, want 404 without the index body", rec.Code, rec.Body)
	}
}

func TestOtherStaticFilesAreServedAndRevalidated(t *testing.T) {
	rec := get(t, newHandler(t), http.MethodGet, "/favicon.svg")
	if rec.Code != http.StatusOK || rec.Body.String() != "<svg/>" || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("favicon = %d %q cc=%q", rec.Code, rec.Body, rec.Header().Get("Cache-Control"))
	}
}

func TestAPIPathsNeverFallBackToIndex(t *testing.T) {
	h := newHandler(t)
	for _, target := range []string{"/api/nope", "/api/", "/api"} {
		if rec := get(t, h, http.MethodGet, target); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, rec.Code)
		}
	}
}

func TestPathTraversalCannotEscapeTheUIDirectory(t *testing.T) {
	dist := buildDist(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(dist), "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := webui.New(webui.Options{Dir: dist})
	if err != nil {
		t.Fatal(err)
	}
	rec := get(t, h, http.MethodGet, "/assets/../../secret.txt")
	if strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("traversal leaked a file outside the UI directory: %q", rec.Body)
	}
}

func TestOnlyGetAndHeadAreAccepted(t *testing.T) {
	h := newHandler(t)
	if rec := get(t, h, http.MethodPost, "/"); rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST / = %d allow=%q", rec.Code, rec.Header().Get("Allow"))
	}
	if rec := get(t, h, http.MethodHead, "/"); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD / = %d body=%q", rec.Code, rec.Body)
	}
}

func TestDirWithoutIndexIsAnError(t *testing.T) {
	if _, err := webui.New(webui.Options{Dir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "pnpm --dir web build") {
		t.Errorf("New(empty dir) = %v, want an error telling how to build the UI", err)
	}
}

func TestWithoutAnyUIThePlaceholderExplainsHowToBuildIt(t *testing.T) {
	// Run from a directory with no web/dist so the dev fallback finds nothing.
	t.Chdir(t.TempDir())
	h, err := webui.New(webui.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if h.Available() {
		t.Fatal("Available() with no UI")
	}
	rec := get(t, h, http.MethodGet, "/")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "pnpm --dir web build") {
		t.Errorf("GET / = %d %q", rec.Code, rec.Body)
	}
	if rec := get(t, h, http.MethodGet, "/api/health"); rec.Code != http.StatusNotFound {
		t.Errorf("api path through UI handler = %d, want 404", rec.Code)
	}
}

func TestCheckoutRelativeDistIsUsedWhenPresent(t *testing.T) {
	root := t.TempDir()
	dist := filepath.Join(root, "web", "dist")
	if err := os.MkdirAll(dist, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("dev build"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	h, err := webui.New(webui.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(t, h, http.MethodGet, "/x"); !h.Available() || rec.Body.String() != "dev build" {
		t.Errorf("fallback = available %v body %q", h.Available(), rec.Body)
	}
}

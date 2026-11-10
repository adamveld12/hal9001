package update

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func dlServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// dlLeftovers lists what is in dir (nothing if it does not exist), to show that a failed download leaves neither the
// file nor its .part.
func dlLeftovers(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestDownloadWritesTheFileAndReportsProgress(t *testing.T) {
	payload := bytes.Repeat([]byte("hal9001"), 30000)
	srv := dlServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) })
	dest := filepath.Join(t.TempDir(), "update.zip")

	var seen []int64
	if err := Download(context.Background(), nil, srv.URL+"/update.zip", dest, int64(len(payload)), func(n int64) { seen = append(seen, n) }); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("file differs from the served bytes (read error %v)", err)
	}
	if names := dlLeftovers(t, filepath.Dir(dest)); len(names) != 1 {
		t.Fatalf("directory holds %v, want only the finished file", names)
	}
	if len(seen) == 0 || seen[len(seen)-1] != int64(len(payload)) {
		t.Fatalf("progress %v must end at the full size %d", seen, len(payload))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			t.Fatalf("progress went backwards: %v", seen)
		}
	}
}

func TestDownloadOverHTTPSUsesTheGivenClient(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("secure")) }))
	t.Cleanup(srv.Close)
	dest := filepath.Join(t.TempDir(), "u.zip")
	if err := Download(context.Background(), srv.Client(), srv.URL, dest, 6, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "secure" {
		t.Fatalf("content %q", b)
	}
}

func TestDownloadRefusesPlainHTTPToARemoteHost(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "u.zip")
	for _, u := range []string{"http://example.com/u.zip", "http://10.0.0.5:9001/u.zip", "ftp://example.com/u.zip", "file:///etc/passwd", "u.zip"} {
		if err := Download(context.Background(), nil, u, dest, 10, nil); err == nil {
			t.Errorf("%s: want a refusal", u)
		}
	}
	if names := dlLeftovers(t, filepath.Dir(dest)); len(names) != 0 {
		t.Fatalf("refused downloads left %v", names)
	}
}

func TestDownloadFollowsRedirectsButNotToPlainHTTPElsewhere(t *testing.T) {
	var final atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/cdn/blob", http.StatusFound) })
	mux.HandleFunc("/cdn/blob", func(w http.ResponseWriter, _ *http.Request) { final.Add(1); _, _ = w.Write([]byte("zipdata")) })
	mux.HandleFunc("/downgrade", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/blob", http.StatusFound)
	})
	srv := dlServer(t, mux.ServeHTTP)
	dir := t.TempDir()

	if err := Download(context.Background(), nil, srv.URL+"/asset", filepath.Join(dir, "ok.zip"), 7, nil); err != nil {
		t.Fatalf("redirect within loopback: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "ok.zip")); string(b) != "zipdata" || final.Load() != 1 {
		t.Fatalf("content %q after %d hits of the target", b, final.Load())
	}
	err := Download(context.Background(), nil, srv.URL+"/downgrade", filepath.Join(dir, "bad.zip"), 7, nil)
	if err == nil || !strings.Contains(err.Error(), "https only") {
		t.Fatalf("a redirect to plain http on another host must be refused, got %v", err)
	}
	if names := dlLeftovers(t, dir); len(names) != 1 || names[0] != "ok.zip" {
		t.Fatalf("directory holds %v", names)
	}
}

func TestDownloadRefusesAnythingButTheExpectedSize(t *testing.T) {
	const want = 1000
	chunked := func(n int) http.HandlerFunc { // no Content-Length: the size is only known once the body ends
		return func(w http.ResponseWriter, _ *http.Request) {
			w.(http.Flusher).Flush()
			_, _ = w.Write(bytes.Repeat([]byte("x"), n))
		}
	}
	for _, tc := range []struct {
		name    string
		h       http.HandlerFunc
		message string
	}{
		{"announced larger", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(make([]byte, want+1)) }, "announces 1001 bytes"},
		{"announced smaller", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(make([]byte, want-1)) }, "announces 999 bytes"},
		{"streamed larger", chunked(want + 50), "more than the 1000 bytes"},
		{"streamed smaller", chunked(want - 50), "950 of the 1000 bytes"},
		{"not found", http.NotFound, "404"},
	} {
		srv := dlServer(t, tc.h)
		dest := filepath.Join(t.TempDir(), "u.zip")
		err := Download(context.Background(), nil, srv.URL, dest, want, nil)
		if err == nil || !strings.Contains(err.Error(), tc.message) {
			t.Errorf("%s: err = %v, want one containing %q", tc.name, err, tc.message)
		}
		if names := dlLeftovers(t, filepath.Dir(dest)); len(names) != 0 {
			t.Errorf("%s: left %v", tc.name, names)
		}
	}
}

func TestDownloadStopsReadingAtOneByteOverTheSize(t *testing.T) {
	written := make(chan int, 1)
	srv := dlServer(t, func(w http.ResponseWriter, _ *http.Request) {
		chunk := make([]byte, 4096)
		n := 0
		for n < 64<<20 {
			if _, err := w.Write(chunk); err != nil {
				break
			}
			w.(http.Flusher).Flush()
			n += len(chunk)
		}
		written <- n
	})
	dest := filepath.Join(t.TempDir(), "u.zip")
	if err := Download(context.Background(), nil, srv.URL, dest, 1000, nil); err == nil {
		t.Fatal("an endless response must be refused")
	}
	select {
	case n := <-written:
		if n >= 64<<20 {
			t.Fatalf("the server could write all %d bytes: the client kept reading", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the server is still writing: the client did not hang up")
	}
}

func TestDownloadEndsWhenTheContextIsCancelled(t *testing.T) {
	started := make(chan struct{})
	srv := dlServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	})
	dest := filepath.Join(t.TempDir(), "u.zip")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Download(ctx, nil, srv.URL, dest, 1000, nil) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled download must fail")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Download ignored the cancelled context")
	}
	if names := dlLeftovers(t, filepath.Dir(dest)); len(names) != 0 {
		t.Fatalf("left %v", names)
	}
}

func TestDownloadGivesUpOnAStalledConnection(t *testing.T) {
	old := downloadIdleTimeout
	downloadIdleTimeout = 100 * time.Millisecond
	t.Cleanup(func() { downloadIdleTimeout = old })
	srv := dlServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	dest := filepath.Join(t.TempDir(), "u.zip")
	err := Download(context.Background(), nil, srv.URL, dest, 1000, nil)
	if err == nil || !strings.Contains(err.Error(), "no data arrived") {
		t.Fatalf("err = %v, want a stall", err)
	}
	if names := dlLeftovers(t, filepath.Dir(dest)); len(names) != 0 {
		t.Fatalf("left %v", names)
	}
}

func TestDownloadProgressReportsEveryTenPercent(t *testing.T) {
	var got []string
	report := func(state, msg string) {
		if state != PhaseDownloading {
			t.Fatalf("state %q", state)
		}
		got = append(got, msg)
	}
	p := downloadProgress(1000, report)
	for _, done := range []int64{5, 99, 100, 150, 199, 200, 999, 1000} {
		p(done)
	}
	if want := "10% 20% 90% 100%"; strings.Join(got, " ") != want {
		t.Fatalf("reports %v, want %s (one per step, none for 0%% again)", got, want)
	}

	// Inside a step nothing is sent (the heartbeat only comes after downloadHeartbeat).
	got = nil
	p = downloadProgress(1000, report)
	p(10)
	if len(got) != 0 {
		t.Fatalf("reported inside a step: %v", got)
	}
}

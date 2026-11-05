//go:build integrated

package daemon_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hal9001/internal/daemon"
)

type running struct {
	addr   net.Addr
	cancel context.CancelFunc
	done   chan error
}

func start(t *testing.T, home string) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan net.Addr, 1)
	r := &running{cancel: cancel, done: make(chan error, 1)}
	go func() {
		r.done <- daemon.Run(ctx, daemon.Options{
			Home: home, Version: "test", ListenAddr: "127.0.0.1:0",
			Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
			Ready: func(a net.Addr) { ready <- a },
		})
	}()
	select {
	case r.addr = <-ready:
	case err := <-r.done:
		cancel()
		t.Fatalf("Run returned before serving: %v", err)
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("daemon did not start")
	}
	t.Cleanup(func() { r.stop(t) })
	return r
}

func (r *running) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	select {
	case err := <-r.done:
		if err != nil {
			t.Errorf("Run = %v, want nil on graceful shutdown", err)
		}
	case <-time.After(30 * time.Second):
		t.Error("daemon did not shut down")
	}
}

// health requests /api/health with the given Host header (empty: the address
// used to connect) and returns the status code.
func (r *running) health(t *testing.T, host string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+r.addr.String()+"/api/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestRunSeedsHomeServesAPIAndRejectsRebindingHost(t *testing.T) {
	home := t.TempDir()
	r := start(t, home)

	for _, p := range []string{"config.yaml", "flows/demo.yaml", "data/hal.db"} {
		if _, err := os.Stat(filepath.Join(home, p)); err != nil {
			t.Errorf("home not seeded: %v", err)
		}
	}
	if !strings.HasPrefix(r.addr.String(), "127.0.0.1:") {
		t.Errorf("listening on %s, want loopback only", r.addr)
	}
	if code := r.health(t, ""); code != http.StatusOK {
		t.Errorf("GET /api/health = %d", code)
	}
	if code := r.health(t, "evil.example"); code != http.StatusForbidden {
		t.Errorf("GET /api/health with Host evil.example = %d, want 403", code)
	}
}

func TestRunRefusesToStartWhenAnotherInstanceHoldsThePort(t *testing.T) {
	r := start(t, t.TempDir())
	err := daemon.Run(context.Background(), daemon.Options{
		Home: t.TempDir(), ListenAddr: r.addr.String(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err == nil || !strings.Contains(err.Error(), "another hal9001") {
		t.Errorf("second Run = %v, want a listen error", err)
	}
}

func TestRunRefusesToStartWithBrokenConfig(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("bogus_key: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := daemon.Run(context.Background(), daemon.Options{
		Home: home, ListenAddr: "127.0.0.1:0", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err == nil || !strings.Contains(err.Error(), "config.yaml") {
		t.Errorf("Run = %v, want a config.yaml error", err)
	}
}

func TestEditingConfigOnDiskIsHotReloaded(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the file watcher")
	}
	home := t.TempDir()
	r := start(t, home)
	// Add a Host through allowed_hosts and see the baseline accept it.
	if code := r.health(t, "mac.tail1234.ts.net"); code != http.StatusForbidden {
		t.Fatalf("host accepted before it was allowed: %d", code)
	}
	cfg := "allowed_hosts: [mac.tail1234.ts.net]\n"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if r.health(t, "mac.tail1234.ts.net") == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("allowed_hosts edit was not picked up")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

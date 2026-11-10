package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// An install the app never reports on (it crashed, or the relaunch failed) must not stay "installing" forever: the
// daemon shows an error and accepts a new request.
func TestInstallWithoutReportsTimesOut(t *testing.T) {
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"schema": 1, "channel": "stable", "version": "0.1.7", "build": 7, "commit": "c", "published_at": "2026-11-10T12:00:00Z",
			"url": "https://example.com/a.zip", "size": 1, "sha256": strings.Repeat("a", 64), "signature": "c2ln",
		})
	}))
	defer feed.Close()

	var offset atomic.Int64
	start := time.Now()
	s := New(Options{
		Current:  Current{BundleID: "dev.hal9001", Version: "0.1.5", Build: 5},
		Settings: func() Settings { return Settings{Channel: ChannelStable} },
		FeedBase: feed.URL, InstallTimeout: 10 * time.Minute, MinCheckGap: -1,
		Now: func() time.Time { return start.Add(time.Duration(offset.Load())) },
	})
	ctx := context.Background()
	defer s.ConnectInstaller(ctx)()
	s.Check(ctx)
	if st, err := s.Install(ctx, false); err != nil || st.State != StateInstalling {
		t.Fatalf("install: %+v, %v", st, err)
	}

	offset.Store(int64(9 * time.Minute))
	s.evaluate(ctx)
	if st := s.Status(ctx); st.State != StateInstalling {
		t.Fatalf("9 minutes without a report is still installing: %+v", st)
	}
	if err := s.Report(ctx, PhaseDownloading, ""); err != nil { // a report restarts the clock
		t.Fatal(err)
	}
	offset.Store(int64(18 * time.Minute))
	s.evaluate(ctx)
	if st := s.Status(ctx); st.State != StateInstalling {
		t.Fatalf("9 minutes after the last report is still installing: %+v", st)
	}
	offset.Store(int64(20 * time.Minute))
	s.evaluate(ctx)
	st := s.Status(ctx)
	if st.State != StateError || st.Error == nil || !st.Available {
		t.Fatalf("after the timeout: %+v", st)
	}
	if st, err := s.Install(ctx, false); err != nil || st.State != StateInstalling {
		t.Fatalf("a new request after the timeout: %+v, %v", st, err)
	}
}

func TestMissingChannelManifestIsNotAnError(t *testing.T) {
	feed := httptest.NewServer(http.NotFoundHandler())
	defer feed.Close()
	s := New(Options{
		Current:  Current{BundleID: "dev.hal9001", Version: "0.1.5", Build: 5},
		Settings: func() Settings { return Settings{Channel: ChannelStable} },
		FeedBase: feed.URL, MinCheckGap: -1,
	})
	ctx := context.Background()
	s.Check(ctx)
	st := s.Status(ctx)
	if st.State != StateUpToDate || st.Error != nil || st.Available {
		t.Fatalf("404 feed: %+v", st)
	}
}

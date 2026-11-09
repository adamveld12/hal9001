package menubar

import (
	"bytes"
	"context"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	statusJSON  = `{"unread_inbox":3,"pending_approvals":2}`
	flowsJSON   = `{"items":[{"id":"triage","enabled":true},{"id":"off","enabled":false},{"id":"broken","enabled":null}]}`
	sensorsJSON = `{"items":[{"id":"gh","status":{"paused":false}},{"id":"mail","status":{"paused":true}}]}`
)

func daemon(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(statusJSON)) })
	mux.HandleFunc("/api/flows", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(flowsJSON)) })
	mux.HandleFunc("/api/sensors", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(sensorsJSON)) })
	mux.HandleFunc("/api/stream", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("id: 1\nevent: stream.ready\ndata: {}\n\n: keepalive\n\nid: 2\nevent: inbox.created\ndata: {}\n\n"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchAndBuildMenu(t *testing.T) {
	srv := daemon(t)
	snap, err := NewClient(srv.URL).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := BuildMenu(snap, "http://ui")
	if !m.Reachable || m.Title != "5" {
		t.Fatalf("reachable=%v title=%q, want true and 5 (unread + approvals)", m.Reachable, m.Title)
	}
	titles := func(es []Entry) (out []string) {
		for _, e := range es {
			out = append(out, e.Title+"|"+e.URL)
		}
		return
	}
	got := strings.Join(titles(m.Entries), ",")
	want := "Inbox (3)|http://ui/inbox,Approvals (2)|http://ui/inbox?kind=action,Flows|,Sensors|,Settings|http://ui/settings/general,|,Quit|"
	if got != want {
		t.Fatalf("entries\n got %s\nwant %s", got, want)
	}
	if g := strings.Join(titles(m.Entries[2].Children), ","); g != "All flows|http://ui/flows,|,triage|http://ui/flows/triage" {
		t.Fatalf("flows submenu (only enabled flows): %s", g)
	}
	if g := strings.Join(titles(m.Entries[3].Children), ","); g != "All sensors|http://ui/sensors,|,gh|http://ui/sensors/gh,mail (paused)|http://ui/sensors/mail" {
		t.Fatalf("sensors submenu: %s", g)
	}
}

func TestZeroCountHidesTitle(t *testing.T) {
	m := BuildMenu(&Snapshot{}, "http://ui")
	if m.Title != "" {
		t.Fatalf("title %q, want hidden at 0", m.Title)
	}
	if m.Entries[0].Title != "Inbox" || m.Entries[1].Title != "Approvals" {
		t.Fatalf("zero counts must have no suffix: %q, %q", m.Entries[0].Title, m.Entries[1].Title)
	}
}

func TestUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if _, err := NewClient(url).Fetch(context.Background()); err == nil {
		t.Fatal("want error for a dead daemon")
	}
	m := BuildMenu(nil, "http://ui")
	if m.Reachable || m.Title != "" || m.Entries[0].Kind != Info || m.Entries[0].Title != "hal9001 is not running" {
		t.Fatalf("unreachable menu: %+v", m)
	}
}

func TestFetchHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", 500) }))
	defer srv.Close()
	if _, err := NewClient(srv.URL).Fetch(context.Background()); err == nil {
		t.Fatal("want error on 500")
	}
}

func TestStreamReportsEventsThenEnds(t *testing.T) {
	srv := daemon(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got []string
	err := NewClient(srv.URL).Stream(ctx, func(typ string) { got = append(got, typ) })
	if err == nil {
		t.Fatal("stream end must be reported as an error so the caller reconnects")
	}
	if strings.Join(got, ",") != "stream.ready,inbox.created" {
		t.Fatalf("events %v", got)
	}
}

func TestIconSizes(t *testing.T) {
	for _, n := range []int{16, 32} {
		if b := Icon(n, false); len(b) < 8 || string(b[1:4]) != "PNG" {
			t.Fatalf("size %d: not a PNG", n)
		}
		if len(Icon(n, true)) == 0 {
			t.Fatal("dim icon empty")
		}
	}
}

func TestThrobFrames(t *testing.T) {
	frames := ThrobIcons(32)
	if len(frames) != ThrobFrames {
		t.Fatalf("frames = %d", len(frames))
	}
	seen := map[string]bool{}
	for _, f := range frames {
		cfg, err := png.DecodeConfig(bytes.NewReader(f))
		if err != nil || cfg.Width != 32 || cfg.Height != 32 {
			t.Fatalf("bad frame: %v %+v", err, cfg)
		}
		seen[string(f)] = true
	}
	if len(seen) < ThrobFrames/2 {
		t.Fatalf("only %d distinct frames", len(seen))
	}
	centre := func(b []byte) uint32 {
		img, _ := png.Decode(bytes.NewReader(b))
		r, _, _, _ := img.At(16, 10).RGBA() // lens body
		return r
	}
	if centre(frames[0]) <= centre(frames[ThrobFrames/2]) {
		t.Fatal("peak frame should be brighter than trough frame")
	}
}

func TestShouldThrob(t *testing.T) {
	if ShouldThrob(nil) {
		t.Fatal("unreachable daemon must not throb")
	}
	if ShouldThrob(&Snapshot{}) {
		t.Fatal("idle must not throb")
	}
	if !ShouldThrob(&Snapshot{Status: Status{RunningFlowRuns: 1}}) {
		t.Fatal("running flow must throb")
	}
}

func TestFlowRunCountLabel(t *testing.T) {
	var f Flow
	on := true
	f.ID, f.Enabled, f.Health.Running = "triage-github", &on, 2
	idle := Flow{ID: "idle", Enabled: &on}
	m := BuildMenu(&Snapshot{Flows: []Flow{f, idle}}, "http://ui")
	var got []string
	for _, e := range m.Entries {
		if e.Title == "Flows" {
			for _, c := range e.Children {
				got = append(got, c.Title)
			}
		}
	}
	if len(got) < 4 || got[2] != "triage-github (2)" || got[3] != "idle" {
		t.Fatalf("labels = %v", got)
	}
}

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
	versionJSON = `{"version":"1.2.3","commit":"c","build_time":"t","go_version":"g","started_at":"2026-01-01T00:00:00Z","home":"/Users/me/.hal9001"}`
	flowsJSON   = `{"items":[{"id":"triage","enabled":true},{"id":"off","enabled":false},{"id":"broken","enabled":null}]}`
	sensorsJSON = `{"items":[{"id":"gh","status":{"paused":false}},{"id":"mail","status":{"paused":true}}]}`
)

// plain is the state of an app that does not run from a bundle: no service entries.
var plain = ServiceState{AppVersion: "9.9", UserHome: "/Users/me"}

func daemon(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(statusJSON)) })
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(versionJSON)) })
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
	m := BuildMenu(snap, "http://ui", plain)
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
	want := "Running · 1.2.3|,Home: ~/.hal9001|,|,Inbox (3)|http://ui/inbox,Approvals (2)|http://ui/inbox?kind=action,Flows|,Sensors|,Settings|http://ui/settings/general,|,hal9001 on GitHub|https://github.com/adamveld12/hal9001,Quit|"
	if got != want {
		t.Fatalf("entries\n got %s\nwant %s", got, want)
	}
	if g := strings.Join(titles(m.Entries[5].Children), ","); g != "All flows|http://ui/flows,|,triage|http://ui/flows/triage" {
		t.Fatalf("flows submenu (only enabled flows): %s", g)
	}
	if g := strings.Join(titles(m.Entries[6].Children), ","); g != "All sensors|http://ui/sensors,|,gh|http://ui/sensors/gh,mail (paused)|http://ui/sensors/mail" {
		t.Fatalf("sensors submenu: %s", g)
	}
}

func TestZeroCountHidesTitle(t *testing.T) {
	m := BuildMenu(&Snapshot{}, "http://ui", plain)
	if m.Title != "" {
		t.Fatalf("title %q, want hidden at 0", m.Title)
	}
	inbox, approvals := entryTitled(t, m, "Inbox"), entryTitled(t, m, "Approvals")
	if inbox.Title != "Inbox" || approvals.Title != "Approvals" {
		t.Fatalf("zero counts must have no suffix: %q, %q", inbox.Title, approvals.Title)
	}
}

func TestNoDanglingSeparator(t *testing.T) {
	for _, snap := range []*Snapshot{nil, {}} {
		m := BuildMenu(snap, "http://ui", plain)
		for _, e := range m.Entries {
			if e.Kind != Submenu {
				continue
			}
			if len(e.Children) != 1 {
				t.Fatalf("%s submenu children = %d, want only the All link", e.Title, len(e.Children))
			}
		}
	}
}

func TestUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if _, err := NewClient(url).Fetch(context.Background()); err == nil {
		t.Fatal("want error for a dead daemon")
	}
	m := BuildMenu(nil, "http://ui", plain)
	if m.Reachable || m.Title != "" || m.Entries[0].Kind != Info || m.Entries[0].Title != "Stopped · 9.9" {
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
	err := NewClient(srv.URL).Stream(ctx, StreamOptions{}, func(e Event) { got = append(got, e.Type) })
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
	m := BuildMenu(&Snapshot{Flows: []Flow{f, idle}}, "http://ui", plain)
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

// entryTitled finds the first top-level entry with the given title.
func entryTitled(t *testing.T, m Menu, title string) Entry {
	t.Helper()
	for _, e := range m.Entries {
		if e.Title == title {
			return e
		}
	}
	t.Fatalf("no %q entry in %+v", title, m.Entries)
	return Entry{}
}

// actions lists the Action entries of a menu (descending into submenus) as "id" or "id!" when disabled, "id*" when checked.
func actions(es []Entry) (out []string) {
	for _, e := range es {
		if e.Kind == Submenu {
			out = append(out, actions(e.Children)...)
		}
		if e.Kind != Action {
			continue
		}
		s := e.ID
		if e.Disabled {
			s += "!"
		}
		if e.Checked {
			s += "*"
		}
		out = append(out, s)
	}
	return out
}

func TestUnbundledAppHasNoServiceEntries(t *testing.T) {
	if got := actions(BuildMenu(nil, "http://ui", plain).Entries); len(got) != 0 {
		t.Fatalf("an app outside a bundle cannot manage a service, got %v", got)
	}
}

func TestStatusLine(t *testing.T) {
	running := &Snapshot{Version: Version{Version: "1.2.3", Home: "/Users/me/.hal9001"}}
	for _, tc := range []struct {
		name string
		snap *Snapshot
		svc  ServiceState
		want string
	}{
		{"running reports the daemon's version", running, ServiceState{AppVersion: "9.9"}, "Running · 1.2.3"},
		{"running without a reported version falls back to the app's", &Snapshot{}, ServiceState{AppVersion: "9.9"}, "Running · 9.9"},
		{"registered but silent", nil, ServiceState{Registered: true, AppVersion: "9.9"}, "Not responding · 9.9"},
		{"stopped", nil, ServiceState{AppVersion: "9.9"}, "Stopped · 9.9"},
		{"stopped, version unknown", nil, ServiceState{}, "Stopped"},
	} {
		if got := StatusLine(tc.snap, tc.svc); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	svc := ServiceState{Home: "/Users/me/.hal9001", UserHome: "/Users/me"}
	if got := homeLine(nil, svc); got != "Home: ~/.hal9001" {
		t.Errorf("home line = %q", got)
	}
	if got := homeLine(&Snapshot{Version: Version{Home: "/data/hal"}}, svc); got != "Home: /data/hal" {
		t.Errorf("a daemon's own home wins and is only abbreviated under the user's home: %q", got)
	}
	if got := homeLine(nil, ServiceState{Home: "/Users/meow/x", UserHome: "/Users/me"}); got != "Home: /Users/meow/x" {
		t.Errorf("a sibling directory must not be abbreviated: %q", got)
	}
}

func TestServiceEntriesFollowTheServiceState(t *testing.T) {
	base := ServiceState{Bundled: true, Managed: true, AppVersion: "9.9"}
	for _, tc := range []struct {
		name string
		svc  ServiceState
		want string
	}{
		{"stopped: only Start is available", base,
			"service.start,service.stop!,service.restart!,logs.open,login.toggle,cli.install"},
		{"registered: Start is disabled", ServiceState{Bundled: true, Managed: true, Registered: true, LaunchAtLogin: true},
			"service.start!,service.stop,service.restart,logs.open,login.toggle*,cli.install"},
		{"needs approval", ServiceState{Bundled: true, Managed: true, Registered: true, NeedsApproval: true},
			"login.approve,service.start!,service.stop,service.restart,logs.open,login.toggle,cli.install"},
		{"headless service: no start/stop, logs stay", ServiceState{Bundled: true, Headless: true},
			"logs.open,login.toggle,cli.install"},
		{"older agents offered for removal", ServiceState{Bundled: true, Legacy: true},
			"legacy.remove,logs.open,login.toggle,cli.install"},
	} {
		m := BuildMenu(nil, "http://ui", tc.svc)
		if got := strings.Join(actions(m.Entries), ","); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
		if last := m.Entries[len(m.Entries)-1]; last.Kind != Quit {
			t.Errorf("%s: menu must end with Quit", tc.name)
		}
	}
	m := BuildMenu(nil, "http://ui", ServiceState{Bundled: true, Headless: true})
	var info []string
	for _, e := range m.Entries {
		if e.Kind == Info {
			info = append(info, e.Title)
		}
	}
	if got := strings.Join(info, "|"); !strings.Contains(got, "hal9001 service uninstall") {
		t.Errorf("the headless notice must name the command that removes it: %v", info)
	}
}

func TestFetchToleratesADaemonWithoutVersion(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(statusJSON)) })
	mux.HandleFunc("/api/flows", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(flowsJSON)) })
	mux.HandleFunc("/api/sensors", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(sensorsJSON)) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	snap, err := NewClient(srv.URL).Fetch(context.Background())
	if err != nil || snap.Version.Version != "" {
		t.Fatalf("snap %+v, err %v", snap, err)
	}
}

func TestAppIconIsARoundedTileWithTheEye(t *testing.T) {
	for _, n := range []int{16, 64, 256} {
		img, err := png.Decode(bytes.NewReader(AppIcon(n)))
		if err != nil || img.Bounds().Dx() != n || img.Bounds().Dy() != n {
			t.Fatalf("size %d: %v %v", n, err, img.Bounds())
		}
		if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
			t.Errorf("size %d: the corner must be transparent (the tile leaves a margin)", n)
		}
		r, g, _, a := img.At(n/2, n/2-n/16).RGBA()
		if a == 0 || r < 0x8000 || g > 0x4000 {
			t.Errorf("size %d: the middle of the tile should be the red lens, got r=%x g=%x a=%x", n, r, g, a)
		}
		if _, _, _, a := img.At(n/2, n/6).RGBA(); a == 0 {
			t.Errorf("size %d: the tile body above the eye must be opaque", n)
		}
	}
}

func TestMenuMCPAndGitHubLinks(t *testing.T) {
	m := BuildMenu(nil, "http://ui", ServiceState{Bundled: true, Managed: true, AppVersion: "9.9"})
	idx := func(title string) int {
		for i, e := range m.Entries {
			if e.Title == title {
				return i
			}
		}
		t.Fatalf("no entry %q", title)
		return -1
	}
	cli, mcp := idx("Install command-line tool…"), idx("Install the MCP…")
	if mcp != cli+1 {
		t.Errorf("MCP entry at %d, want directly below the CLI entry at %d", mcp, cli)
	}
	if e := m.Entries[mcp]; e.Kind != Link || e.URL != "http://ui/settings/mcp" {
		t.Errorf("MCP entry = %+v", e)
	}
	n := len(m.Entries)
	if e := m.Entries[n-2]; e.Kind != Link || e.Title != "hal9001 on GitHub" || e.URL != "https://github.com/adamveld12/hal9001" {
		t.Errorf("entry above Quit = %+v", e)
	}
	if m.Entries[n-1].Kind != Quit {
		t.Errorf("menu must end with Quit")
	}
}

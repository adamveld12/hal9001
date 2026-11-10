package menubar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"hal9001/internal/update"
)

func TestUpdateWireNamesMatchTheDaemons(t *testing.T) {
	if UpdaterHeader != update.InstallerHeader || UpdaterApp != update.InstallerApp ||
		EventUpdateStatus != update.EventStatus || EventUpdateInstall != update.EventInstall {
		t.Error("the stream header and event names must be the daemon's")
	}
	if stateInstalling != string(update.StateInstalling) || stateError != string(update.StateError) || phaseRequested != update.PhaseRequested {
		t.Error("the states the menu tells apart must be the daemon's")
	}

	// What the daemon sends for GET /api/update decodes into the subset the menu reads.
	body, err := json.Marshal(update.Status{
		Enabled: true, State: update.StateError, Available: true, Phase: new("downloading"), Error: new("it broke"),
		Latest: &update.Release{Version: "0.2.0", Build: 7},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got UpdateStatus
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.State != "error" || !got.Available || got.Phase != "downloading" || got.Error != "it broke" ||
		got.Latest == nil || got.Latest.Version != "0.2.0" || got.Latest.Build != 7 {
		t.Errorf("decoded %+v", got)
	}
	got = UpdateStatus{}
	if err := json.Unmarshal([]byte(`{"enabled":true,"state":"up_to_date","phase":null,"latest":null,"error":null}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.Phase != "" || got.Error != "" || got.Latest != nil {
		t.Errorf("null fields must decode to empty: %+v", got)
	}
}

// updateDaemon is daemon() with an /api/update answer of its own.
func updateDaemon(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(statusJSON)) })
	mux.HandleFunc("/api/flows", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(flowsJSON)) })
	mux.HandleFunc("/api/sensors", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(sensorsJSON)) })
	mux.HandleFunc("/api/update", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchCarriesTheUpdateStatusWithoutDependingOnIt(t *testing.T) {
	snap, err := NewClient(updateDaemon(t, 200, `{"enabled":true,"state":"available","available":true,"latest":{"version":"0.2.0","build":7}}`).URL).Fetch(context.Background())
	if err != nil || snap.Update == nil || snap.Update.State != "available" || snap.Update.Latest.Version != "0.2.0" {
		t.Fatalf("snap.Update = %+v, err %v", snap.Update, err)
	}
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"an older daemon":       {404, `{}`},
		"a failing updater":     {500, `{}`},
		"an unreadable answer":  {200, `not json`},
		"an empty answer":       {200, ``},
		"an updater that fails": {503, `{"detail":"updates are not configured"}`},
	} {
		snap, err := NewClient(updateDaemon(t, tc.status, tc.body).URL).Fetch(context.Background())
		if err != nil || snap.Update != nil {
			t.Errorf("%s: err %v, update %+v; the daemon is running and the menu has no update information", name, err, snap.Update)
		}
	}
}

func TestUpdateStatusIsAbsentOnADaemonWithoutTheEndpoint(t *testing.T) {
	st, ok, err := NewClient(updateDaemon(t, 404, `{}`).URL).UpdateStatus(context.Background())
	if ok || err != nil || st != (UpdateStatus{}) {
		t.Fatalf("404: %+v %v %v", st, ok, err)
	}
	if _, ok, err := NewClient(updateDaemon(t, 500, `{}`).URL).UpdateStatus(context.Background()); ok || err == nil {
		t.Fatalf("500 is an error, not an absence: %v %v", ok, err)
	}
}

func TestInstallUpdateSendsAnEmptyObjectNeverForce(t *testing.T) {
	f := &fakeAPI{status: 202, body: `{"state":"installing"}`}
	srv := httptest.NewServer(f)
	defer srv.Close()
	if err := NewClient(srv.URL).InstallUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0] != "POST /api/update/install application/json {}" {
		t.Fatalf("calls %q", f.calls)
	}
}

func TestCheckUpdateSendsNoBody(t *testing.T) {
	f := &fakeAPI{status: 200, body: `{"state":"up_to_date"}`}
	srv := httptest.NewServer(f)
	defer srv.Close()
	if err := NewClient(srv.URL).CheckUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || strings.Join(strings.Fields(f.calls[0]), " ") != "POST /api/update/check" {
		t.Fatalf("calls %q: want a POST without content type or body", f.calls)
	}
}

func TestInstallRefusalSurfacesTheDaemonsDetail(t *testing.T) {
	const detail = "Open the HAL9001 app to install this update, or download HAL9001 0.2.0 from https://example.test/notes"
	f := &fakeAPI{status: 409, body: `{"type":"urn:hal9001:problem:invalid-state","title":"Invalid state","status":409,"detail":"` + detail + `"}`}
	srv := httptest.NewServer(f)
	defer srv.Close()
	err := NewClient(srv.URL).InstallUpdate(context.Background())
	var refused *APIError
	if !errors.As(err, &refused) || refused.Status != 409 || refused.Detail != detail {
		t.Fatalf("err = %v, want an *APIError with the problem detail", err)
	}
	if !strings.Contains(err.Error(), detail) {
		t.Fatalf("the message must carry the detail: %v", err)
	}
}

func TestReportUpdateSendsStateAndOnlyAFailuresError(t *testing.T) {
	f := &fakeAPI{status: 200, body: `{}`}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := NewClient(srv.URL)
	for _, r := range [][2]string{{"downloading", ""}, {"failed", "disk full"}} {
		if err := c.ReportUpdate(context.Background(), r[0], r[1]); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		`POST /api/update/report application/json {"state":"downloading"}`,
		`POST /api/update/report application/json {"state":"failed","error":"disk full"}`,
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls\n%s\nwant\n%s", strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
	}
	f.status, f.body = 409, `{"title":"Conflict","status":409,"detail":"no update install is in progress"}`
	if err := c.ReportUpdate(context.Background(), "installing", ""); err == nil || !strings.Contains(err.Error(), "no update install") {
		t.Fatalf("err = %v", err)
	}
}

func TestEventPayloadDecodesTheEnvelopeData(t *testing.T) {
	ev := Event{Type: EventUpdateInstall, Data: []byte(`{"type":"update.install","id":"3","ts":"2026-11-10T12:00:00Z","data":{"manifest":{"build":7,"version":"0.2.0"}}}`)}
	var got update.InstallEvent
	if err := ev.Payload(&got); err != nil || got.Manifest.Build != 7 || got.Manifest.Version != "0.2.0" {
		t.Fatalf("payload %+v, err %v", got, err)
	}
	if err := (Event{Type: "x", Data: []byte("{")}).Payload(&got); err == nil {
		t.Fatal("broken JSON must be an error")
	}
}

// streamRecorder serves /api/stream, records each request and lets respond answer it.
type streamRecorder struct {
	mu       sync.Mutex
	headers  []http.Header
	types    []string
	requests int
	respond  func(r *http.Request, w http.ResponseWriter)
}

func (s *streamRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests++
	s.headers = append(s.headers, r.Header.Clone())
	s.types = append(s.types, r.URL.Query().Get("types"))
	s.mu.Unlock()
	s.respond(r, w)
}

func streamEventsThen(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		_, _ = w.Write([]byte("event: " + e + "\ndata: {}\n\n"))
	}
}

func hasType(types, t string) bool {
	for _, have := range strings.Split(types, ",") {
		if have == t {
			return true
		}
	}
	return false
}

func TestStreamRegistersTheInstallerOnlyWhenAsked(t *testing.T) {
	rec := &streamRecorder{respond: func(_ *http.Request, w http.ResponseWriter) { streamEventsThen(w, "update.status") }}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	c := NewClient(srv.URL)

	var got []string
	for _, updater := range []bool{false, true} {
		_ = c.Stream(context.Background(), StreamOptions{Updater: updater}, func(e Event) { got = append(got, e.Type) })
	}
	if rec.headers[0].Get("X-Hal9001-Updater") != "" || hasType(rec.types[0], "update.install") {
		t.Errorf("a plain connection is not the installer: header %q types %q", rec.headers[0].Get("X-Hal9001-Updater"), rec.types[0])
	}
	if rec.headers[1].Get("X-Hal9001-Updater") != "app" || !hasType(rec.types[1], "update.install") {
		t.Errorf("installer: header %q types %q", rec.headers[1].Get("X-Hal9001-Updater"), rec.types[1])
	}
	for i, types := range rec.types {
		if !hasType(types, "update.status") || !hasType(types, "approval.created") {
			t.Errorf("request %d: types %q must carry update.status next to the refresh events", i, types)
		}
	}
	if strings.Join(got, ",") != "update.status,update.status" {
		t.Errorf("events %v", got)
	}
}

func TestStreamFallsBackForADaemonThatDoesNotKnowTheUpdateEvents(t *testing.T) {
	// An older daemon: 400 for any update event type, a normal stream otherwise.
	rec := &streamRecorder{respond: func(r *http.Request, w http.ResponseWriter) {
		if strings.Contains(r.URL.Query().Get("types"), "update.") {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"title":"Bad request","status":400,"detail":"unknown event type \"update.status\""}`))
			return
		}
		streamEventsThen(w, "stream.ready", "notification.created")
	}}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	var got []string
	err := NewClient(srv.URL).Stream(context.Background(), StreamOptions{Notifications: true, Updater: true}, func(e Event) { got = append(got, e.Type) })
	if err == nil {
		t.Fatal("a stream that ends is an error so that the caller reconnects")
	}
	if strings.Join(got, ",") != "stream.ready,notification.created" {
		t.Fatalf("events %v: the fallback connection must deliver", got)
	}
	if rec.requests != 2 {
		t.Fatalf("%d requests, want the refused one and the fallback", rec.requests)
	}
	first, second := rec.types[0], rec.types[1]
	if !hasType(first, "update.status") || !hasType(first, "update.install") || rec.headers[0].Get("X-Hal9001-Updater") != "app" {
		t.Errorf("first attempt: types %q header %q", first, rec.headers[0].Get("X-Hal9001-Updater"))
	}
	if strings.Contains(second, "update.") || rec.headers[1].Get("X-Hal9001-Updater") != "" {
		t.Errorf("fallback: types %q header %q must have no update parts", second, rec.headers[1].Get("X-Hal9001-Updater"))
	}
	if rec.headers[1].Get("X-Hal9001-Notifier") != "app" || !hasType(second, "notification.created") || !hasType(second, "approval.created") {
		t.Errorf("the notification sink must survive the fallback: types %q notifier %q", second, rec.headers[1].Get("X-Hal9001-Notifier"))
	}
}

func TestStreamRetriesOnceAndOnlyForABadRequest(t *testing.T) {
	for status, wantRequests := range map[int]int{http.StatusBadRequest: 2, http.StatusInternalServerError: 1, http.StatusForbidden: 1} {
		rec := &streamRecorder{respond: func(_ *http.Request, w http.ResponseWriter) { http.Error(w, "no", status) }}
		srv := httptest.NewServer(rec)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := NewClient(srv.URL).Stream(ctx, StreamOptions{Updater: true}, func(Event) {})
		cancel()
		srv.Close()
		if err == nil || rec.requests != wantRequests {
			t.Errorf("status %d: %d requests (want %d), err %v", status, rec.requests, wantRequests, err)
		}
	}
}

// updateMenu builds the menu of a bundled app whose daemon reports upd.
func updateMenu(upd *UpdateStatus, svc ServiceState) Menu {
	svc.Bundled, svc.Managed = true, true
	return BuildMenu(&Snapshot{Update: upd}, "http://ui", svc)
}

// updateLines lists the entries about updates as "info: title" or "action <id>: title".
func updateLines(m Menu) []string {
	var out []string
	for _, e := range m.Entries {
		switch {
		case e.Kind == Action && strings.HasPrefix(e.ID, "update."):
			out = append(out, "action "+e.ID+": "+e.Title)
		case e.Kind == Info && (strings.HasPrefix(e.Title, "Update ") || strings.HasPrefix(e.Title, "Installing update")):
			out = append(out, "info: "+e.Title)
		}
	}
	return out
}

func latest(version string) *UpdateRelease { return &UpdateRelease{Version: version, Build: 7} }

func TestMenuOffersTheUpdateStates(t *testing.T) {
	const (
		install = "action update.install: Update available: 0.2.0 — Install & Restart"
		check   = "action update.check: Check for updates"
	)
	for _, tc := range []struct {
		name string
		upd  *UpdateStatus
		svc  ServiceState
		want []string
	}{
		{"available", &UpdateStatus{Enabled: true, State: "available", Available: true, Latest: latest("0.2.0")}, ServiceState{},
			[]string{install, check}},
		{"available without a version", &UpdateStatus{Enabled: true, State: "available", Available: true}, ServiceState{},
			[]string{"action update.install: Update available — Install & Restart", check}},
		{"pending until the flows finish", &UpdateStatus{Enabled: true, State: "pending", Available: true, Pending: true, Latest: latest("0.2.0")}, ServiceState{},
			[]string{"info: Update 0.2.0 installs when flows finish", check}},
		{"pending without a version", &UpdateStatus{Enabled: true, State: "pending", Available: true, Pending: true}, ServiceState{},
			[]string{"info: Update installs when flows finish", check}},
		{"installing, as the daemon says", &UpdateStatus{Enabled: true, State: "installing", Available: true, Latest: latest("0.2.0")}, ServiceState{},
			[]string{"info: Installing update…", check}},
		{"installing, requested but nothing reported yet", &UpdateStatus{Enabled: true, State: "installing", Available: true, Phase: "requested"}, ServiceState{},
			[]string{"info: Installing update…", check}},
		{"installing, with the daemon's phase", &UpdateStatus{Enabled: true, State: "installing", Available: true, Phase: "verifying"}, ServiceState{},
			[]string{"info: Installing update… (verifying)", check}},
		{"installing in this app", &UpdateStatus{Enabled: true, State: "available", Available: true, Latest: latest("0.2.0")},
			ServiceState{UpdateInstalling: true, UpdatePhase: "downloading 40%"},
			[]string{"info: Installing update… (downloading 40%)", check}},
		{"installing in this app, no phase yet", &UpdateStatus{Enabled: true, State: "available", Available: true}, ServiceState{UpdateInstalling: true},
			[]string{"info: Installing update…", check}},
		{"failed, can be tried again", &UpdateStatus{Enabled: true, State: "error", Available: true, Latest: latest("0.2.0"), Error: "download failed: the server answered 404 Not Found"}, ServiceState{},
			[]string{"info: Update failed: download failed: the server answered 404 Not Found", install, check}},
		{"a failed check with nothing available", &UpdateStatus{Enabled: true, State: "error", Error: "feed: 503"}, ServiceState{},
			[]string{check}},
		{"up to date", &UpdateStatus{Enabled: true, State: "up_to_date", Latest: latest("0.1.5")}, ServiceState{},
			[]string{check}},
		{"updates are off", &UpdateStatus{Enabled: false, State: "disabled", Available: true, Latest: latest("0.2.0")}, ServiceState{}, nil},
		{"an older daemon has no update information", nil, ServiceState{}, nil},
	} {
		if got := updateLines(updateMenu(tc.upd, tc.svc)); strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

func TestFailedUpdateMessageIsShortenedInTheMenu(t *testing.T) {
	msg := "the downloaded update has no valid code signature: exit status 1:\n/Applications/HAL9001.app: a sealed resource is missing or invalid\nfile modified: /Applications/HAL9001.app/Contents/MacOS/HAL9001"
	m := updateMenu(&UpdateStatus{Enabled: true, State: "error", Available: true, Latest: latest("0.2.0"), Error: msg}, ServiceState{})
	var line string
	for _, e := range m.Entries {
		if strings.HasPrefix(e.Title, "Update failed: ") {
			line = e.Title
		}
	}
	if !strings.HasPrefix(line, "Update failed: the downloaded update has no valid code signature: exit status 1: /Applications") ||
		!strings.HasSuffix(line, "…") || strings.Contains(line, "\n") || len([]rune(line)) > len("Update failed: ")+maxUpdateErrorLen {
		t.Fatalf("line %q", line)
	}
}

func TestMenuShowsAnInstallThisAppRunsEvenWhenTheDaemonDoesNotAnswer(t *testing.T) {
	m := BuildMenu(nil, "http://ui", ServiceState{Bundled: true, UpdateInstalling: true, UpdatePhase: "installing"})
	if got := updateLines(m); len(got) != 1 || got[0] != "info: Installing update… (installing)" {
		t.Fatalf("lines %q", got)
	}
}

func TestUpdateEntriesBelongToABundledApp(t *testing.T) {
	m := BuildMenu(&Snapshot{Update: &UpdateStatus{Enabled: true, State: "available", Available: true, Latest: latest("0.2.0")}}, "http://ui", plain)
	if got := updateLines(m); len(got) != 0 {
		t.Fatalf("an app outside a bundle cannot install anything, got %q", got)
	}
	if got := actions(m.Entries); len(got) != 0 {
		t.Fatalf("actions %v", got)
	}
}

func TestCheckForUpdatesSitsWithTheAppSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		upd  *UpdateStatus
		want string
	}{
		{"updates on", &UpdateStatus{Enabled: true, State: "up_to_date"}, "service.start,service.stop!,service.restart!,logs.open,update.check,login.toggle,cli.install"},
		{"updates off", &UpdateStatus{Enabled: false}, "service.start,service.stop!,service.restart!,logs.open,login.toggle,cli.install"},
		{"no update information", nil, "service.start,service.stop!,service.restart!,logs.open,login.toggle,cli.install"},
	} {
		m := BuildMenu(&Snapshot{Update: tc.upd}, "http://ui", ServiceState{Bundled: true, Managed: true})
		if got := strings.Join(actions(m.Entries), ","); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

func TestStatusLineWhileTheServiceIsBeingUpdated(t *testing.T) {
	const updating = "Updating service… (up to 2 min)"
	if got := StatusLine(nil, ServiceState{ServiceUpdating: true, Registered: true, AppVersion: "9.9"}); got != updating {
		t.Errorf("unreachable: %q", got)
	}
	if got := StatusLine(&Snapshot{Version: Version{Version: "1.2.3"}}, ServiceState{ServiceUpdating: true}); got != "Running · 1.2.3" {
		t.Errorf("once the daemon answers the line is the usual one: %q", got)
	}
	m := BuildMenu(nil, "http://ui", ServiceState{Bundled: true, Managed: true, ServiceUpdating: true})
	if m.Entries[0].Kind != Info || m.Entries[0].Title != updating {
		t.Errorf("first entry %+v", m.Entries[0])
	}
}

func TestShortenKeepsMenuLinesShort(t *testing.T) {
	if got := shorten("  a \n b\t c "); got != "a b c" {
		t.Errorf("whitespace: %q", got)
	}
	long := strings.Repeat("é", 2*maxUpdateErrorLen)
	got := shorten(long)
	if r := []rune(got); len(r) != maxUpdateErrorLen || r[maxUpdateErrorLen-1] != '…' || string(r[:maxUpdateErrorLen-1]) != strings.Repeat("é", maxUpdateErrorLen-1) {
		t.Errorf("cut at %d characters, not bytes: %q", maxUpdateErrorLen, got)
	}
	if exact := strings.Repeat("x", maxUpdateErrorLen); shorten(exact) != exact {
		t.Error("a message of exactly the maximum length is not cut")
	}
}

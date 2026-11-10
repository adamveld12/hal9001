package menubar

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func note() Notification {
	return Notification{
		ID: "n1", Kind: "note", FlowID: "triage", Title: "triage", Subtitle: "Deploy is failing", Body: "Roll back now.",
		Priority: "normal", ThreadID: "triage", URL: "http://localhost:9001/inbox/i1", ItemID: "i1", Channel: "app",
	}
}

func approval(kind string) Notification {
	return Notification{
		ID: "n2", Kind: kind, FlowID: "triage", Title: "triage", Subtitle: "Approval: ship it", Body: "Plan",
		Priority: "high", ThreadID: "triage", URL: "http://localhost:9001/inbox/approval/a1", ApprovalID: "a1", ApprovalType: kind, Channel: "app",
	}
}

func TestOnlyChannelAppIsShownByTheApp(t *testing.T) {
	for _, ch := range []string{"terminal-notifier", "none", ""} {
		n := note()
		n.Channel = ch
		if _, ok := PlanNotification(n); ok {
			t.Errorf("channel %q: terminal-notifier (or nothing) already handled it; the app must not show it again", ch)
		}
	}
	if _, ok := PlanNotification(note()); !ok {
		t.Fatal("channel app must be shown")
	}
}

func TestPlanNotificationFields(t *testing.T) {
	n := note()
	n.Priority = "low"
	r, _ := PlanNotification(n)
	if r.ID != "n1" || r.Title != "triage" || r.Subtitle != "Deploy is failing" || r.Body != "Roll back now." || r.Thread != "triage" {
		t.Errorf("request = %+v", r)
	}
	want := map[string]string{
		"url": "http://localhost:9001/inbox/i1", "kind": "note", "item_id": "i1", "approval_id": "", "approval_type": "", "editable": "false",
	}
	for k, v := range want {
		if r.UserInfo[k] != v {
			t.Errorf("userInfo[%s] = %q, want %q", k, r.UserInfo[k], v)
		}
	}
	for prio, sound := range map[string]bool{"low": false, "normal": false, "high": true, "urgent": true} {
		n.Priority = prio
		if r, _ := PlanNotification(n); r.Sound != sound {
			t.Errorf("priority %s: sound = %v, want %v", prio, r.Sound, sound)
		}
	}
}

func actionIDs(cat string) string {
	for _, c := range Categories() {
		if c.ID == cat {
			var ids []string
			for _, a := range c.Actions {
				id := a.ID
				if a.Text {
					id += "*"
				}
				ids = append(ids, id+":"+a.Title)
			}
			return strings.Join(ids, ",")
		}
	}
	return "missing"
}

func TestCategoriesAndTheirButtons(t *testing.T) {
	req := approval("request")
	editable := req
	editable.Editable = true
	for _, tc := range []struct {
		n    Notification
		cat  string
		want string
	}{
		{note(), CategoryNote, "open:Open,mark_read:Mark read,archive:Archive"},
		{approval("proposal"), CategoryApproval, "approve:Approve,deny:Deny,approve_comment*:Approve with comment…,deny_comment*:Deny with comment…"},
		{approval("tool_call"), CategoryApproval, "approve:Approve,deny:Deny,approve_comment*:Approve with comment…,deny_comment*:Deny with comment…"},
		{req, CategoryApproval, "approve:Approve,deny:Deny,approve_comment*:Approve with comment…,deny_comment*:Deny with comment…"},
		{editable, CategoryEditable, "approve:Approve as-is,deny:Deny,open:Open to edit"},
	} {
		if got := CategoryFor(tc.n); got != tc.cat {
			t.Errorf("%s editable=%v: category %q, want %q", tc.n.Kind, tc.n.Editable, got, tc.cat)
		}
		if got := actionIDs(tc.cat); got != tc.want {
			t.Errorf("%s: buttons\n got %s\nwant %s", tc.cat, got, tc.want)
		}
	}
	if got := CategoryFor(Notification{Kind: "mystery"}); got != "" {
		t.Errorf("unknown kind: category %q, want none", got)
	}
	if _, err := json.Marshal(Categories()); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsDecisions(t *testing.T) {
	on := NotifySettings{Auth: AuthAuthorized, Style: StyleBanner, AlertEnabled: true, CenterEnabled: true}
	for _, tc := range []struct {
		name                       string
		s                          NotifySettings
		sink, off, pending, banner bool
	}{
		{"authorized banners", on, true, false, false, true},
		{"provisional", NotifySettings{Auth: AuthProvisional, Style: StyleBanner, CenterEnabled: true}, true, false, false, true},
		{"not asked yet", NotifySettings{Auth: AuthNotDetermined}, false, false, true, false},
		{"denied", NotifySettings{Auth: AuthDenied}, false, true, false, false},
		{"authorized but everything switched off", NotifySettings{Auth: AuthAuthorized, Style: StyleNone}, false, true, false, false},
		{"alerts style", NotifySettings{Auth: AuthAuthorized, Style: StyleAlert, AlertEnabled: true}, true, false, false, false},
	} {
		urgent := Notification{Priority: "urgent"}
		if tc.s.Sink() != tc.sink || tc.s.Off() != tc.off || tc.s.Pending() != tc.pending || tc.s.HintAlertStyle(urgent) != tc.banner {
			t.Errorf("%s: sink %v off %v pending %v hint %v", tc.name, tc.s.Sink(), tc.s.Off(), tc.s.Pending(), tc.s.HintAlertStyle(urgent))
		}
	}
	if on.HintAlertStyle(Notification{Priority: "high"}) {
		t.Error("only urgent notifications call for the Alerts style")
	}
}

func TestNotificationEntriesInTheMenu(t *testing.T) {
	has := func(m Menu, id string) (Entry, bool) {
		for _, e := range m.Entries {
			if e.ID == id {
				return e, true
			}
		}
		return Entry{}, false
	}
	m := BuildMenu(nil, "http://ui", ServiceState{Bundled: true})
	if _, ok := has(m, ActionNotificationSettings); ok {
		t.Error("no notification entry while notifications work")
	}
	e, ok := has(BuildMenu(nil, "http://ui", ServiceState{Bundled: true, NotificationsOff: true}), ActionNotificationSettings)
	if !ok || e.Title != "Notifications are off — Open Settings" {
		t.Errorf("off entry = %+v, %v", e, ok)
	}
	e, ok = has(BuildMenu(nil, "http://ui", ServiceState{Bundled: true, AlertStyleHint: true}), ActionNotificationSettings)
	if !ok || !strings.Contains(e.Title, "Alerts") {
		t.Errorf("hint entry = %+v, %v", e, ok)
	}
	if _, ok := has(BuildMenu(nil, "http://ui", ServiceState{NotificationsOff: true}), ActionNotificationSettings); ok {
		t.Error("an app outside a bundle has no notifications to turn on")
	}
}

func TestStreamRegistersAsASinkOnlyWhenAsked(t *testing.T) {
	var mu sync.Mutex
	var headers []string
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, r.Header.Get("X-Hal9001-Notifier"))
		queries = append(queries, r.URL.Query().Get("types"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		env := `{"type":"notification.created","id":"7","ts":"2026-11-09T12:00:00Z","data":{"id":"n1","kind":"note","channel":"app","title":"triage","priority":"urgent","item_id":"i1"}}`
		_, _ = io.WriteString(w, "id: 7\nevent: notification.created\ndata: "+env+"\n\n")
	}))
	defer srv.Close()
	c := NewClient(srv.URL)

	var got []Notification
	for _, sink := range []bool{false, true} {
		_ = c.Stream(context.Background(), StreamOptions{Notifications: sink}, func(e Event) {
			if e.Type == EventNotification {
				n, err := e.Notification()
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, n)
			}
		})
	}
	if headers[0] != "" || strings.Contains(queries[0], "notification.created") {
		t.Errorf("without notifications the app must not be a sink: header %q types %q", headers[0], queries[0])
	}
	if headers[1] != "app" || !strings.Contains(queries[1], "notification.created") || !strings.Contains(queries[1], "approval.created") {
		t.Errorf("sink: header %q types %q (notification.created must be added to the refresh types)", headers[1], queries[1])
	}
	if len(got) != 2 || got[0].ID != "n1" || got[0].Priority != "urgent" || got[0].ItemID != "i1" {
		t.Errorf("decoded notifications = %+v", got)
	}
}

// fakeAPI records the requests of the action handler and answers with status.
type fakeAPI struct {
	mu     sync.Mutex
	calls  []string
	status int
	body   string
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+" "+r.Header.Get("Content-Type")+" "+strings.TrimSpace(string(b)))
	f.mu.Unlock()
	if f.status >= 400 {
		w.Header().Set("Content-Type", "application/problem+json")
	}
	w.WriteHeader(f.status)
	_, _ = io.WriteString(w, f.body)
}

func handler(t *testing.T, f *fakeAPI) (ActionHandler, *[]string, *[]Request) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	var opened []string
	var failures []Request
	return ActionHandler{
		Client: NewClient(srv.URL),
		Open:   func(u string) error { opened = append(opened, u); return nil },
		Fail:   func(r Request) { failures = append(failures, r) },
	}, &opened, &failures
}

func act(id, text string, n Notification) NotificationAction {
	r, _ := PlanNotification(n)
	return NotificationAction{Action: id, Text: text, RequestID: n.ID, ThreadID: n.ThreadID, UserInfo: r.UserInfo}
}

func TestActionsCallTheAPI(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    NotificationAction
		want string
	}{
		{"approve", act(ActApprove, "", approval("proposal")), `POST /api/approvals/a1/approve application/json {}`},
		{"approve as-is on an editable request", act(ActApprove, "", func() Notification { n := approval("request"); n.Editable = true; return n }()), `POST /api/approvals/a1/approve application/json {}`},
		{"deny", act(ActDeny, "", approval("tool_call")), `POST /api/approvals/a1/deny application/json {}`},
		{"approve with comment", act(ActApproveComment, "ship it", approval("proposal")), `POST /api/approvals/a1/approve application/json {"comment":"ship it"}`},
		{"deny with comment", act(ActDenyComment, "not now", approval("proposal")), `POST /api/approvals/a1/deny application/json {"comment":"not now"}`},
		{"mark read", act(ActMarkRead, "", note()), `PATCH /api/inbox/i1 application/json {"read":true}`},
		{"archive", act(ActArchive, "", note()), `PATCH /api/inbox/i1 application/json {"archived":true}`},
	} {
		f := &fakeAPI{status: 200, body: `{}`}
		h, opened, failures := handler(t, f)
		if err := h.Handle(context.Background(), tc.a); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if len(f.calls) != 1 || f.calls[0] != tc.want {
			t.Errorf("%s: calls %v, want %s", tc.name, f.calls, tc.want)
		}
		if len(*opened) != 0 || len(*failures) != 0 {
			t.Errorf("%s: opened %v failures %v", tc.name, *opened, *failures)
		}
	}
}

func TestClickAndOpenButtonOpenTheLinkWithoutCallingTheAPI(t *testing.T) {
	for _, id := range []string{DefaultAction, ActOpen} {
		f := &fakeAPI{status: 200}
		h, opened, _ := handler(t, f)
		if err := h.Handle(context.Background(), act(id, "", note())); err != nil {
			t.Fatal(err)
		}
		if len(*opened) != 1 || (*opened)[0] != "http://localhost:9001/inbox/i1" || len(f.calls) != 0 {
			t.Errorf("%s: opened %v, API calls %v", id, *opened, f.calls)
		}
	}
}

func TestFailedActionPostsAFailureNotification(t *testing.T) {
	f := &fakeAPI{status: 409, body: `{"type":"about:blank","title":"Conflict","status":409,"detail":"approval is no longer pending"}`}
	h, _, failures := handler(t, f)
	a := act(ActApprove, "", approval("proposal"))
	err := h.Handle(context.Background(), a)
	if err == nil || !strings.Contains(err.Error(), "no longer pending") {
		t.Fatalf("err = %v", err)
	}
	if len(*failures) != 1 {
		t.Fatalf("failures = %v", *failures)
	}
	r := (*failures)[0]
	if r.Subtitle != "Could not approve" || !strings.Contains(r.Body, "no longer pending") || r.Category != "" || r.UserInfo[InfoURL] != "http://localhost:9001/inbox/approval/a1" || r.Thread != "triage" {
		t.Errorf("failure request = %+v", r)
	}
}

func TestActionsWithMissingIdsFailVisibly(t *testing.T) {
	f := &fakeAPI{status: 200}
	h, _, failures := handler(t, f)
	bad := NotificationAction{Action: ActApprove, RequestID: "x", UserInfo: map[string]string{}}
	if err := h.Handle(context.Background(), bad); err == nil || len(*failures) != 1 || len(f.calls) != 0 {
		t.Errorf("err %v failures %v calls %v", err, *failures, f.calls)
	}
	if err := h.Handle(context.Background(), NotificationAction{Action: "bogus", UserInfo: map[string]string{}}); err == nil {
		t.Error("an unknown action must fail")
	}
}

func TestOpenErrorsAreReported(t *testing.T) {
	h, _, failures := handler(t, &fakeAPI{status: 200})
	h.Open = func(string) error { return errors.New("open: no browser") }
	if err := h.Handle(context.Background(), act(DefaultAction, "", note())); err == nil || len(*failures) != 1 {
		t.Errorf("err %v failures %v", err, *failures)
	}
}

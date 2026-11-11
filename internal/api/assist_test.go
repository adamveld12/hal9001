package api

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"hal9001/internal/core"
	"hal9001/internal/store"
)

// assistDouble stands in for core.Assist. Tests script the call they exercise.
type assistDouble struct {
	create  func(ctx context.Context, req core.AssistCreateRequest) (*store.AssistSession, error)
	send    func(ctx context.Context, id, text string, yaml *string, page *core.AssistPageContext) (*store.AssistSession, error)
	draft   func(ctx context.Context, id, yaml, expectedHash string) (*store.AssistSession, error)
	test    func(ctx context.Context, id string, byAgent bool, req core.AssistTestRequest) (core.AssistTestOutcome, error)
	cancel  func(ctx context.Context, id string) (*store.AssistSession, error)
	closeS  func(ctx context.Context, id string) error
	created func(ctx context.Context, sensorID string) error
	fork    func(ctx context.Context, agentRunID string) (*store.AssistSession, error)
}

func (d *assistDouble) CreateSession(ctx context.Context, req core.AssistCreateRequest) (*store.AssistSession, error) {
	if d.create == nil {
		return nil, errUnscripted
	}
	return d.create(ctx, req)
}

func (d *assistDouble) SendMessage(ctx context.Context, id, text string, yaml *string, page *core.AssistPageContext) (*store.AssistSession, error) {
	if d.send == nil {
		return nil, errUnscripted
	}
	return d.send(ctx, id, text, yaml, page)
}

func (d *assistDouble) UpdateDraft(ctx context.Context, id, yaml, expectedHash string) (*store.AssistSession, error) {
	if d.draft == nil {
		return nil, errUnscripted
	}
	return d.draft(ctx, id, yaml, expectedHash)
}

func (d *assistDouble) TestDraft(ctx context.Context, id string, byAgent bool, req core.AssistTestRequest) (core.AssistTestOutcome, error) {
	if d.test == nil {
		return core.AssistTestOutcome{}, errUnscripted
	}
	return d.test(ctx, id, byAgent, req)
}

func (d *assistDouble) Samples(context.Context, string) ([]core.AssistSample, error) {
	return []core.AssistSample{}, nil
}

func (d *assistDouble) Cancel(ctx context.Context, id string) (*store.AssistSession, error) {
	if d.cancel == nil {
		return nil, errUnscripted
	}
	return d.cancel(ctx, id)
}

func (d *assistDouble) Close(ctx context.Context, id string) error {
	if d.closeS == nil {
		return errUnscripted
	}
	return d.closeS(ctx, id)
}

func (d *assistDouble) SensorCreated(ctx context.Context, sensorID string) error {
	if d.created == nil {
		return nil
	}
	return d.created(ctx, sensorID)
}

const hash64 = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func (e *env) assistSession(target, status string) *store.AssistSession {
	e.t.Helper()
	a := &store.AssistSession{
		TargetKind: store.AssistTargetSensor, TargetID: target, Runtime: "claude", Status: store.AssistStatus(status),
		DraftYAML: "id: x\n", DraftHash: hash64, DraftUpdatedBy: store.DraftByUser,
	}
	must(e.t, e.st.CreateAssistSession(ctx(), a))
	return a
}

func TestAssistWritesNeedTheServiceAndReadsDoNot(t *testing.T) {
	e := newEnv(t) // no assist service
	a := e.assistSession("", "idle")

	e.get("/api/assist/sessions/" + a.ID).want(200)
	e.get("/api/assist/sessions").want(200)
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/assist/sessions", `{"target":{"kind":"sensor"},"runtime":"claude"}`},
		{"POST", "/api/assist/sessions/" + a.ID + "/messages", `{"text":"hi"}`},
		{"PUT", "/api/assist/sessions/" + a.ID + "/draft", `{"yaml":"id: x\n"}`},
		{"POST", "/api/assist/sessions/" + a.ID + "/test", `{}`},
		{"POST", "/api/assist/sessions/" + a.ID + "/cancel", `{}`},
		{"DELETE", "/api/assist/sessions/" + a.ID, ``},
	} {
		if got := e.do(c.method, c.path, c.body).want(503).problem(); got != "unavailable" {
			t.Errorf("%s %s: problem %q, want unavailable", c.method, c.path, got)
		}
	}
}

func TestAssistSessionReadsShowDraftAndConversation(t *testing.T) {
	e := newEnv(t)
	a := e.assistSession("github-notifications", "running")
	other := e.assistSession("", "idle")
	text := "make it fire on mentions"
	must(t, e.st.CreateAssistMessage(ctx(), &store.AssistMessage{SessionID: a.ID, Role: "user", Text: &text}))
	must(t, e.st.CreateAssistMessage(ctx(), &store.AssistMessage{
		SessionID: a.ID, Role: "tool", AgentRunID: "01JC7Z3K9Q8V5T2M4N6P8R0S1T", ApprovalID: "01JC7Z3K9Q8V5T2M4N6P8R0S1V",
		ToolCall: []byte(`{"tool_call_id":"t1","title":"Edit","kind":"edit","status":"pending","raw_input":{"path":"sensor.yaml"},"content":[]}`),
	}))

	got := e.get("/api/assist/sessions/" + a.ID).want(200).json()
	target := got["target"].(map[string]any)
	if target["kind"] != "sensor" || target["id"] != "github-notifications" || got["status"] != "running" || got["error"] != nil || got["last_agent_run_id"] != nil {
		t.Errorf("session = %v", got)
	}
	draft := got["draft"].(map[string]any)
	if draft["yaml"] != "id: x\n" || draft["hash"] != hash64 || draft["updated_by"] != "user" || draft["sensor_id"] != nil {
		t.Errorf("draft = %v", draft)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["text"] != text || msgs[1].(map[string]any)["text"] != nil {
		t.Fatalf("messages = %v", msgs)
	}
	if tc := msgs[1].(map[string]any)["tool_call"].(map[string]any); tc["title"] != "Edit" || msgs[1].(map[string]any)["approval_id"] != "01JC7Z3K9Q8V5T2M4N6P8R0S1V" {
		t.Errorf("tool message = %v", msgs[1])
	}

	if ids := e.get("/api/assist/sessions?target_kind=sensor&target_id=github-notifications").want(200).ids(); len(ids) != 1 || ids[0] != a.ID {
		t.Errorf("sessions of one sensor = %v, want only %s (not %s)", ids, a.ID, other.ID)
	}
	e.get("/api/assist/sessions?target_kind=bogus").want(400)
	e.get("/api/assist/sessions/01JC7Z3K9Q8V5T2M4N6P8R0S1T").want(404)
}

func TestCreateAssistSessionValidatesTargetAndPassesTheRequestOn(t *testing.T) {
	var got core.AssistCreateRequest
	var e *env
	e = newEnv(t, func(o *envOptions) {
		o.assist = &assistDouble{create: func(ctx context.Context, req core.AssistCreateRequest) (*store.AssistSession, error) {
			got = req
			a := &store.AssistSession{
				TargetKind: req.TargetKind, TargetID: req.TargetID, Runtime: req.Runtime, Status: store.AssistIdle,
				DraftYAML: "id: x\n", DraftHash: hash64, DraftUpdatedBy: store.DraftByUser,
			}
			return a, e.st.CreateAssistSession(ctx, a)
		}}
	})

	e.do("POST", "/api/assist/sessions", `{"target":{"kind":"bogus"},"runtime":"claude"}`).want(422)
	e.do("POST", "/api/assist/sessions", `{"target":{"kind":"sensor","id":"bad id"},"runtime":"claude"}`).want(422)
	e.do("POST", "/api/assist/sessions", `{"target":{"kind":"sensor"},"runtime":"not a name"}`).want(422)
	e.do("POST", "/api/assist/sessions", `{"target":{"kind":"sensor"},"runtime":"claude","extra":1}`).want(400)
	e.do("POST", "/api/assist/sessions", `{"target":{"kind":"sensor","id":"no-such-sensor"},"runtime":"claude"}`).want(404)

	r := e.do("POST", "/api/assist/sessions", `{"target":{"kind":"sensor","id":"demo-ticker"},"runtime":"claude","model":"sonnet","yaml":"id: demo-ticker\n"}`).want(201)
	id, _ := r.json()["id"].(string)
	if loc := r.Header.Get("Location"); loc != "/api/assist/sessions/"+id {
		t.Errorf("Location = %q", loc)
	}
	if got.TargetID != "demo-ticker" || got.Runtime != "claude" || got.Model != "sonnet" || got.YAML == nil || *got.YAML != "id: demo-ticker\n" || got.Principal == "" {
		t.Errorf("service received %+v", got)
	}
}

func TestAssistMessageAndDraftErrorsMapToProblems(t *testing.T) {
	svc := &assistDouble{}
	e := newEnv(t, func(o *envOptions) { o.assist = svc })
	a := e.assistSession("", "idle")
	base := "/api/assist/sessions/" + a.ID

	svc.send = func(context.Context, string, string, *string, *core.AssistPageContext) (*store.AssistSession, error) {
		return nil, errors.Join(core.ErrInvalidState)
	}
	e.do("POST", base+"/messages", `{"text":"hi"}`).want(409)
	e.do("POST", base+"/messages", `{"text":"   "}`).want(422)
	svc.send = func(_ context.Context, id, text string, yaml *string, _ *core.AssistPageContext) (*store.AssistSession, error) {
		if text != "go" || yaml == nil || *yaml != "id: y\n" {
			t.Errorf("service got text %q yaml %v", text, yaml)
		}
		a.Status = store.AssistRunning
		return a, nil
	}
	if st := e.do("POST", base+"/messages", `{"text":"go","yaml":"id: y\n"}`).want(202).json()["status"]; st != "running" {
		t.Errorf("status = %v", st)
	}

	svc.draft = func(_ context.Context, _, _, expected string) (*store.AssistSession, error) {
		return nil, &core.DraftConflictError{Expected: expected, Current: hash64}
	}
	r := e.do("PUT", base+"/draft", `{"yaml":"id: z\n","expected_hash":"`+hash64+`"}`).want(409)
	if r.problem() != "conflict" || r.json()["current_hash"] != hash64 {
		t.Errorf("conflict = %s", r.Body)
	}
	e.do("PUT", base+"/draft", `{"yaml":"id: z\n","expected_hash":"nope"}`).want(422)
	svc.draft = func(_ context.Context, _, yaml, _ string) (*store.AssistSession, error) {
		a.DraftYAML, a.DraftUpdatedBy = yaml, store.DraftByUser
		return a, nil
	}
	if y := e.do("PUT", base+"/draft", `{"yaml":"id: z\n"}`).want(200).json()["yaml"]; y != "id: z\n" {
		t.Errorf("draft yaml = %v", y)
	}

	var byAgent []bool
	svc.test = func(_ context.Context, _ string, agent bool, _ core.AssistTestRequest) (core.AssistTestOutcome, error) {
		byAgent = append(byAgent, agent)
		if agent {
			return core.AssistTestOutcome{}, core.ErrInvalidState
		}
		return core.AssistTestOutcome{Sensor: &core.AssistTestResult{Kind: "sensor", ExitCode: 0, Stdout: core.AssistText{Text: "[]", Size: 2}}}, nil
	}
	res := e.do("POST", base+"/test", `{}`).want(200).json()
	if items, ok := res["items"].([]any); !ok || len(items) != 0 || res["item_count"] != float64(0) || res["parse_error"] != nil {
		t.Errorf("test result = %v (items must be an empty array, not null)", res)
	}
	// The agent's tests carry the header; a script the user has not approved is a 409.
	req := request("POST", base+"/test", []byte(`{}`))
	req.Header.Set(assistAgentHeader, "1")
	if r := serve(t, e.srv, req).want(409); r.problem() != "invalid-state" {
		t.Errorf("agent test problem = %q", r.problem())
	}
	if len(byAgent) != 2 || byAgent[0] || !byAgent[1] {
		t.Errorf("byAgent per call = %v, want [false true]", byAgent)
	}
	svc.closeS = func(context.Context, string) error { return store.ErrNotFound }
	e.do("DELETE", base, "").want(404)
	svc.closeS = func(context.Context, string) error { return nil }
	e.do("DELETE", base, "").want(204)
}

func TestAssistTurnsAreAgentRunsWithoutAFlow(t *testing.T) {
	e := newEnv(t)
	a := e.assistSession("", "awaiting_approval")
	turn := &store.AgentRun{
		AssistSessionID: a.ID, Runtime: "claude", Mode: store.ModeApprove, ApproveStyle: store.StylePerTool, Policy: store.PolicyGated,
		Phase: store.PhaseExecute, Status: store.AgentAwaitingApproval, Prompt: store.Inline("hi"),
	}
	must(t, e.st.CreateAgentRun(ctx(), turn))
	pr := &store.PermissionRequest{AgentRunID: turn.ID, ToolCallID: "t1", ToolKind: "execute", Title: "curl example.com"}
	must(t, e.st.CreatePermissionRequest(ctx(), pr))
	ap := &store.Approval{AgentRunID: turn.ID, Type: store.ApprovalToolCall, PermissionRequestID: pr.ID}
	must(t, e.st.CreateApproval(ctx(), ap))

	run := e.get("/api/agent-runs/" + turn.ID).want(200).json()
	if run["assist_session_id"] != a.ID || run["node_run_id"] != nil || run["flow_run_id"] != nil || run["flow_id"] != nil || run["node_id"] != nil {
		t.Errorf("assist turn = %v", run)
	}
	if ids := run["phase_run_ids"].([]any); len(ids) != 1 || ids[0] != turn.ID {
		t.Errorf("phase_run_ids = %v, want only this session's turns", ids)
	}
	other := e.assistSession("", "idle")
	must(t, e.st.CreateAgentRun(ctx(), &store.AgentRun{
		AssistSessionID: other.ID, Runtime: "claude", Mode: store.ModeApprove, ApproveStyle: store.StylePerTool, Policy: store.PolicyGated,
		Phase: store.PhaseExecute, Status: store.AgentSucceeded,
	}))
	if ids := e.get("/api/agent-runs?assist_session_id=" + a.ID).want(200).ids(); len(ids) != 1 || ids[0] != turn.ID {
		t.Errorf("runs of the session = %v", ids)
	}

	appr := e.get("/api/approvals/" + ap.ID).want(200).json()
	if appr["assist_session_id"] != a.ID || appr["flow_id"] != nil || appr["node_run_id"] != nil || appr["event_id"] != nil || appr["type"] != "tool_call" {
		t.Errorf("assist approval = %v", appr)
	}
	if ids := e.get("/api/approvals?status=pending").want(200).ids(); len(ids) != 1 || ids[0] != ap.ID {
		t.Errorf("pending approvals = %v, want the assist tool call in the shared queue", ids)
	}
}

func TestStreamAnnouncesAssistSessionsAndMessages(t *testing.T) {
	e := newEnv(t)
	ts := e.listen()
	mine := e.assistSession("", "idle")
	theirs := e.assistSession("", "idle")
	c := e.openStream(ts, "?types=assist.updated,assist.message,approval.created&assist_session_id="+mine.ID, nil)
	c.expect("stream.ready")

	text := "hello"
	must(t, e.st.CreateAssistMessage(ctx(), &store.AssistMessage{SessionID: theirs.ID, Role: "user", Text: &text}))
	mine.Status, mine.DraftYAML, mine.DraftHash, mine.DraftUpdatedBy = store.AssistRunning, "id: y\n", hash64, store.DraftByAgent
	must(t, e.st.UpdateAssistSession(ctx(), mine))
	msg := &store.AssistMessage{SessionID: mine.ID, Role: "assistant", Text: &text}
	must(t, e.st.CreateAssistMessage(ctx(), msg))

	// The session's creation may still be in flight when the stream opens: skip to the update.
	var up map[string]any
	for range 5 {
		if up = c.expectUntil("assist.updated").data(); up["status"] == "running" {
			break
		}
	}
	if up["id"] != mine.ID || up["status"] != "running" || up["draft"].(map[string]any)["updated_by"] != "agent" {
		t.Errorf("assist.updated = %v", up)
	}
	m := c.expectUntil("assist.message").data()
	if m["id"] != msg.ID || m["session_id"] != mine.ID || m["text"] != "hello" {
		t.Errorf("assist.message = %v", m)
	}
	c.quiet() // nothing from the other session
}

func TestStreamAnnouncesAssistUIEventsToTheirSessionOnly(t *testing.T) {
	e := newEnv(t)
	ts := e.listen()
	mine := e.assistSession("", "idle")
	theirs := e.assistSession("", "idle")
	c := e.openStream(ts, "?types=assist.navigate,assist.start_draft&assist_session_id="+mine.ID, nil)
	c.expect("stream.ready")

	e.srv.PublishAssistNavigate(theirs.ID, "/inbox", "")
	e.srv.PublishAssistNavigate(mine.ID, "/flows/triage?view=runs", "the failing runs")
	e.srv.PublishAssistNavigate(mine.ID, "/timeline", "")
	e.srv.PublishAssistStartDraft(mine.ID, "flow", "triage", "add a retry node")
	e.srv.PublishAssistStartDraft(mine.ID, "sensor", "", "")

	if nav := c.expect("assist.navigate").data(); nav["session_id"] != mine.ID || nav["path"] != "/flows/triage?view=runs" || nav["reason"] != "the failing runs" {
		t.Errorf("assist.navigate = %v", nav)
	}
	if nav := c.expect("assist.navigate").data(); nav["path"] != "/timeline" || nav["reason"] != nil {
		t.Errorf("assist.navigate without a reason = %v (reason must be null)", nav)
	}
	if sd := c.expect("assist.start_draft").data(); sd["session_id"] != mine.ID || sd["kind"] != "flow" || sd["id"] != "triage" || sd["prompt"] != "add a retry node" {
		t.Errorf("assist.start_draft = %v", sd)
	}
	if sd := c.expect("assist.start_draft").data(); sd["kind"] != "sensor" || sd["id"] != nil || sd["prompt"] != nil {
		t.Errorf("assist.start_draft for a new sensor = %v (id and prompt must be null)", sd)
	}
	c.quiet() // nothing from the other session
}

func TestGeneralAssistSessionsHaveNoTargetAndShowATitle(t *testing.T) {
	var e *env
	e = newEnv(t, func(o *envOptions) {
		o.assist = &assistDouble{create: func(ctx context.Context, req core.AssistCreateRequest) (*store.AssistSession, error) {
			a := &store.AssistSession{
				TargetKind: req.TargetKind, TargetID: req.TargetID, Runtime: req.Runtime, Status: store.AssistIdle,
				DraftHash: hash64, DraftUpdatedBy: store.DraftByUser,
			}
			return a, e.st.CreateAssistSession(ctx, a)
		}}
	})

	e.do("POST", "/api/assist/sessions", `{"target":{"kind":"general","id":"triage"},"runtime":"claude"}`).want(422)
	r := e.do("POST", "/api/assist/sessions", `{"target":{"kind":"general"},"runtime":"claude"}`).want(201).json()
	id, _ := r["id"].(string)
	target, draft := r["target"].(map[string]any), r["draft"].(map[string]any)
	if target["kind"] != "general" || target["id"] != nil || r["title"] != nil ||
		draft["kind"] != "general" || draft["yaml"] != "" || draft["sensor_id"] != nil || draft["flow_id"] != nil || draft["tool_id"] != nil {
		t.Errorf("general session = %v", r)
	}
	if ids := e.get("/api/assist/sessions?target_kind=general").want(200).ids(); len(ids) != 1 || ids[0] != id {
		t.Errorf("general sessions = %v, want only %s", ids, id)
	}

	// The title is the first user message, one line, 80 characters at most; the list and the detail show it.
	a, err := e.st.GetAssistSession(ctx(), id)
	must(t, err)
	a.Title = store.AssistTitle("why did   the\ntriage flow fail? " + strings.Repeat("é", 100))
	must(t, e.st.UpdateAssistSession(ctx(), a))
	want := "why did the triage flow fail? " + strings.Repeat("é", 50)
	if got := e.get("/api/assist/sessions/" + id).want(200).json()["title"]; got != want {
		t.Errorf("title = %q, want %q", got, want)
	}
	items := e.get("/api/assist/sessions").want(200).json()["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["title"] != want {
		t.Errorf("listed title = %v", items)
	}
}

func TestSendingAMessageHandsThePageContextToTheService(t *testing.T) {
	svc := &assistDouble{}
	e := newEnv(t, func(o *envOptions) { o.assist = svc })
	a := e.assistSession("", "idle")
	path := "/api/assist/sessions/" + a.ID + "/messages"

	var got *core.AssistPageContext
	svc.send = func(_ context.Context, _, _ string, _ *string, page *core.AssistPageContext) (*store.AssistSession, error) {
		got = page
		return a, nil
	}
	e.do("POST", path, `{"text":"hi"}`).want(202)
	if got != nil {
		t.Errorf("page context %+v without one in the request", got)
	}
	e.do("POST", path, `{"text":"why?","page_context":{"route":"/flows/triage?view=runs","title":"Flows","summary":"Runs of triage",`+
		`"facts":[{"label":"status","value":"failed"}],"entities":[{"kind":"flow","id":"triage","label":"Triage","detail":"enabled"},{"kind":"run"}]}}`).want(202)
	want := &core.AssistPageContext{
		Route: "/flows/triage?view=runs", Title: "Flows", Summary: "Runs of triage",
		Facts:    []core.AssistPageFact{{Label: "status", Value: "failed"}},
		Entities: []core.AssistPageEntity{{Kind: "flow", ID: "triage", Label: "Triage", Detail: "enabled"}, {Kind: "run"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("service received %+v, want %+v", got, want)
	}
	got = nil
	for _, route := range []string{"https://evil.example/x", "//evil.example", "flows", `/a\\b`, ""} {
		e.do("POST", path, `{"text":"hi","page_context":{"route":"`+route+`","title":"T"}}`).want(422)
	}
	if got != nil {
		t.Errorf("a malformed page context reached the service: %+v", got)
	}
}

func (d *assistDouble) FlowCreated(context.Context, string) error                   { return nil }
func (d *assistDouble) TargetRenamed(context.Context, string, string, string) error { return nil }

func (d *assistDouble) Fork(ctx context.Context, agentRunID string) (*store.AssistSession, error) {
	if d.fork == nil {
		return nil, errUnscripted
	}
	return d.fork(ctx, agentRunID)
}
func (d *assistDouble) ToolCreated(context.Context, string) error { return nil }

func TestAssistTestRejectsMalformedSimulateEmit(t *testing.T) {
	e := newEnv(t)
	a := e.assistSession("", "idle")
	for _, body := range map[string]string{
		"bad node id":    `{"simulate_emit":{"bad id":{"port":"ok"}}}`,
		"bad port":       `{"simulate_emit":{"n":{"port":"Not-A-Port"}}}`,
		"missing port":   `{"simulate_emit":{"n":{}}}`,
		"output a list":  `{"simulate_emit":{"n":{"port":"ok","output":[1]}}}`,
		"output a value": `{"simulate_emit":{"n":{"port":"ok","output":"x"}}}`,
	} {
		e.do("POST", "/api/assist/sessions/"+a.ID+"/test", body).want(422)
	}
	e.do("POST", "/api/assist/sessions/"+a.ID+"/test", `{"simulate_emit":{"n":{"port":"ok","other":1}}}`).want(400)
	// A well-formed request gets past validation and reaches the missing service.
	e.do("POST", "/api/assist/sessions/"+a.ID+"/test", `{"simulate_emit":{"n":{"port":"ok","output":{"a":1}}}}`).want(503)
}

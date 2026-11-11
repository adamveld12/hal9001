package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type fakeOperatorHost struct {
	decision   Decision
	err        error
	asked      []OperatorApproval
	questions  []OperatorQuestion
	questionID string
	notes      []OperatorNote
}

func (h *fakeOperatorHost) ApproveOperator(_ context.Context, req OperatorApproval) (Decision, error) {
	h.asked = append(h.asked, req)
	return h.decision, h.err
}

func (h *fakeOperatorHost) AskOperator(_ context.Context, q OperatorQuestion) (string, error) {
	h.questions = append(h.questions, q)
	return h.questionID, h.err
}

func (h *fakeOperatorHost) PostOperatorNote(_ context.Context, n OperatorNote) (OperatorNoted, error) {
	h.notes = append(h.notes, n)
	return OperatorNoted{ItemID: "item1", URL: "http://x/inbox/item1"}, h.err
}

type sentRequest struct {
	method, path string
	query        url.Values
	body         string
}

func newOperator(t *testing.T, host *fakeOperatorHost) *Operator {
	t.Helper()
	o, err := NewOperator(host, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func recordingAPI(status int, body string, sent *[]sentRequest) APIFunc {
	return func(_ context.Context, method, path string, q url.Values, b []byte) (int, []byte, error) {
		*sent = append(*sent, sentRequest{method, path, q, string(b)})
		return status, []byte(body), nil
	}
}

func envOf(api APIFunc, principal string) OperatorEnv {
	return OperatorEnv{API: api, Caller: OperatorCaller{Principal: principal, Client: "test-agent 1.0"}}
}

func TestOperatorReadsMapToTheRESTAPIWithoutApproval(t *testing.T) {
	host := &fakeOperatorHost{}
	o := newOperator(t, host)
	var sent []sentRequest
	env := envOf(recordingAPI(200, `{"items":[]}`, &sent), "localhost")

	res, err := o.Call(context.Background(), env, "inbox_list", json.RawMessage(`{"q":"deploy","read":false,"limit":5,"archived":"any"}`))
	if err != nil || res.Text != `{"items":[]}` {
		t.Fatalf("inbox_list: %q %v", res.Text, err)
	}
	got := sent[0]
	if got.method != http.MethodGet || got.path != "/api/inbox" || got.query.Get("q") != "deploy" || got.query.Get("read") != "false" || got.query.Get("limit") != "5" || got.query.Get("archived") != "any" {
		t.Errorf("request = %+v", got)
	}
	if _, err := o.Call(context.Background(), env, "approvals_list", nil); err != nil {
		t.Fatal(err)
	}
	if q := sent[1].query; q.Get("status") != "pending" || q.Get("limit") != "20" {
		t.Errorf("approvals_list defaults: %v", q)
	}
	if len(host.asked) != 0 {
		t.Errorf("a read asked for approval: %+v", host.asked)
	}
}

func TestOperatorSideEffectsWaitForApprovalAndRunOnlyWhenAllowed(t *testing.T) {
	var sent []sentRequest
	env := envOf(recordingAPI(202, `{"flow_run_ids":["r1"]}`, &sent), "alice")

	denied := &fakeOperatorHost{decision: Decision{Comment: "no"}}
	o := newOperator(t, denied)
	_, err := o.Call(context.Background(), env, "flow_run", json.RawMessage(`{"id":"pr-review","rerun_seen":true}`))
	var de *DeniedError
	if !errors.As(err, &de) || de.Comment != "no" {
		t.Fatalf("denied flow_run error = %v", err)
	}
	if len(sent) != 0 {
		t.Fatalf("a denied call reached the API: %+v", sent)
	}
	if len(denied.asked) != 1 || denied.asked[0].Caller.Principal != "alice" || denied.asked[0].Caller.Client != "test-agent 1.0" || denied.asked[0].Timeout != time.Minute ||
		!strings.Contains(denied.asked[0].Reason, `"pr-review"`) {
		t.Errorf("approval request = %+v", denied.asked)
	}

	expired := &fakeOperatorHost{decision: Decision{Expired: true}}
	if _, err := newOperator(t, expired).Call(context.Background(), env, "sensor_poll", json.RawMessage(`{"id":"s"}`)); !errors.As(err, &de) || !de.Expired {
		t.Errorf("expired sensor_poll error = %v", err)
	}

	allowed := &fakeOperatorHost{decision: Decision{Allowed: true}}
	res, err := newOperator(t, allowed).Call(context.Background(), env, "flow_run", json.RawMessage(`{"id":"pr-review","rerun_seen":true}`))
	if err != nil || !strings.Contains(res.Text, "r1") {
		t.Fatalf("approved flow_run: %q %v", res.Text, err)
	}
	if len(sent) != 1 || sent[0].method != http.MethodPost || sent[0].path != "/api/flows/pr-review/run" || sent[0].body != `{"rerun_seen":true}` {
		t.Errorf("request = %+v", sent)
	}

	broken := &fakeOperatorHost{err: errors.New("store down")}
	if _, err := newOperator(t, broken).Call(context.Background(), env, "flow_run", json.RawMessage(`{"id":"f"}`)); err == nil || !strings.Contains(err.Error(), "approval failed") {
		t.Errorf("approval failure error = %v", err)
	}
	if len(sent) != 1 {
		t.Errorf("a call whose approval failed reached the API: %+v", sent)
	}
}

func TestOperatorRefusesBadArgumentsBeforeAskingAnyone(t *testing.T) {
	host := &fakeOperatorHost{decision: Decision{Allowed: true}}
	o := newOperator(t, host)
	var sent []sentRequest
	env := envOf(recordingAPI(200, `{}`, &sent), "a")
	for _, tc := range []struct{ tool, args string }{
		{"flow_run", `{"id":"../killswitch/pause"}`},
		{"flow_run", `{"id":"a/b"}`},
		{"flow_run", `{}`},
		{"flow_run", `{"id":"f","unknown":1}`},
		{"sensor_poll", `{"id":"s","flows":["ok","bad id"]}`},
		{"flow_get", `{"id":".."}`},
		{"inbox_list", `{"limit":0}`},
		{"inbox_list", `{"limit":500}`},
		{"inbox_list", `{"read":"yes"}`},
		{"flow_run", `[1]`},
		{"flow_create", `{}`},
		{"flow_create", `{"spec":"  "}`},
		{"sensor_create", `{"spec":"lang: bash","extra":1}`},
		{"request_approval", `{}`},
		{"request_approval", `{"title":"t"}`},
		{"request_approval", `{"title":"t","description":"  "}`},
		{"request_approval", `{"title":"t","description":"` + strings.Repeat("x", 601) + `"}`},
		{"request_approval", `{"title":"t","description":"d","wait_seconds":241}`},
		{"request_approval", `{"title":"t","description":"d","timeout_seconds":1}`},
		{"request_approval", `{"title":"t","description":"d","priority":"critical"}`},
		{"request_approval", `{"title":"t","description":"d","editable":[{"key":"a b"}]}`},
		{"request_approval", `{"title":"t","description":"d","editable":[{"key":"a"},{"key":"a"}]}`},
		{"send_note", `{"title":"t","url":"javascript:alert(1)"}`},
		{"send_note", `{"title":"line\nbreak"}`},
		{"send_note", `{"title":"t","body":"` + strings.Repeat("x", MaxNoteBody+1) + `"}`},
	} {
		if _, err := o.Call(context.Background(), env, tc.tool, json.RawMessage(tc.args)); err == nil {
			t.Errorf("%s %.60s was accepted", tc.tool, tc.args)
		}
	}
	if len(host.asked) != 0 || len(host.questions) != 0 || len(host.notes) != 0 || len(sent) != 0 {
		t.Errorf("bad arguments reached the host or the API: asked=%d questions=%d notes=%d sent=%d", len(host.asked), len(host.questions), len(host.notes), len(sent))
	}
	if _, err := o.Call(context.Background(), env, "approve_approval", nil); !errors.Is(err, ErrUnknownTool) {
		t.Errorf("unknown tool error = %v", err)
	}
}

func TestOperatorTurnsAPIProblemsIntoToolErrorsAndTruncatesLongResults(t *testing.T) {
	o := newOperator(t, &fakeOperatorHost{})
	var sent []sentRequest
	_, err := o.Call(context.Background(), envOf(recordingAPI(404, `{"title":"Not found","detail":"no flow x"}`, &sent), "a"), "flow_get", json.RawMessage(`{"id":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "no flow x") {
		t.Errorf("404 error = %v", err)
	}
	res, err := o.Call(context.Background(), envOf(recordingAPI(200, strings.Repeat("é", maxOperatorText), &sent), "a"), "flows_list", nil)
	if err != nil || len(res.Text) > maxOperatorText+200 || !strings.Contains(res.Text, "[truncated") {
		t.Errorf("long result: len=%d err=%v", len(res.Text), err)
	}
}

func TestOperatorToolsCannotDecideApprovals(t *testing.T) {
	o := newOperator(t, &fakeOperatorHost{})
	for _, info := range o.Tools() {
		if strings.Contains(info.Name, "approve") {
			t.Errorf("operator tool %q could decide approvals", info.Name)
		}
	}
	for _, name := range []string{"flow_run", "sensor_poll", "flow_create", "sensor_create"} {
		if !o.NeedsApproval(name) || o.Service(name) != ServiceAdmin {
			t.Errorf("%s: needs approval %v, service %q", name, o.NeedsApproval(name), o.Service(name))
		}
	}
	if o.NeedsApproval("flows_list") || o.NeedsApproval("flow_validate") || o.NeedsApproval("send_note") {
		t.Error("a read, a validator or send_note asks for approval")
	}
	if o.Service("flows_list") != "" || o.Service("flow_validate") != "" || o.Service("request_approval") != ServiceAdmin || o.Service("send_note") != ServiceAdmin {
		t.Error("service flags are wrong")
	}
}

// A call the request is not admitted for is refused before an approval is raised.
func TestOperatorChecksTheAuthServiceBeforeAskingAnyone(t *testing.T) {
	host := &fakeOperatorHost{decision: Decision{Allowed: true}}
	o := newOperator(t, host)
	var sent []sentRequest
	env := envOf(recordingAPI(200, `{}`, &sent), "a")
	env.Authorize = func(_ context.Context, service string) error {
		if service == ServiceAdmin {
			return errors.New("hal9001: 403 Forbidden: admin required")
		}
		return nil
	}
	for tool, args := range map[string]string{
		"flow_run": `{"id":"f"}`, "flow_create": `{"spec":"nodes: []"}`, "request_approval": `{"title":"t","description":"d"}`, "send_note": `{"title":"t"}`,
	} {
		if _, err := o.Call(context.Background(), env, tool, json.RawMessage(args)); err == nil || !strings.Contains(err.Error(), "403") {
			t.Errorf("%s: error = %v", tool, err)
		}
	}
	if _, err := o.Call(context.Background(), env, "flows_list", nil); err != nil {
		t.Errorf("a read was refused: %v", err)
	}
	if len(host.asked)+len(host.questions)+len(host.notes) != 0 {
		t.Errorf("a refused call reached the host: %+v", host)
	}
}

// fakeSpecAPI answers the REST calls flow_create makes: validation, the existence check and creation.
type fakeSpecAPI struct {
	validate string // body of the validate response
	existing map[string]bool
	created  []sentRequest
	sent     []sentRequest
}

func (f *fakeSpecAPI) api(_ context.Context, method, path string, q url.Values, b []byte) (int, []byte, error) {
	f.sent = append(f.sent, sentRequest{method, path, q, string(b)})
	switch {
	case method == http.MethodPost && strings.HasSuffix(path, "/validate"):
		return 200, []byte(f.validate), nil
	case method == http.MethodGet:
		id := path[strings.LastIndex(path, "/")+1:]
		if f.existing[id] {
			return 200, []byte(`{}`), nil
		}
		return 404, []byte(`{"title":"Not found"}`), nil
	case method == http.MethodPost:
		f.created = append(f.created, sentRequest{method, path, q, string(b)})
		return 201, []byte(`{"id":"triage","path":"flows/triage.yaml","hash":"h","diagnostics":[]}`), nil
	}
	return 500, nil, nil
}

const validFlowResponse = `{"valid":true,"diagnostics":[{"severity":"warning","code":"unused_node","message":"node b is unused","path":"nodes[1]"}],"flow":{"description":"Triage","enabled":false,"dry_run":true,"subscribe":["gh"],"nodes":[{"id":"a","type":"command","script":"echo"},{"id":"b","type":"agent","prompt":"triage","tools":["lookup",{"id":"other"}]}]}}`

func TestFlowCreateReturnsValidationErrorsWithoutAskingAnyone(t *testing.T) {
	host := &fakeOperatorHost{decision: Decision{Allowed: true}}
	f := &fakeSpecAPI{validate: `{"valid":false,"diagnostics":[{"severity":"error","code":"unknown_node","message":"edge to nowhere","path":"edges[0].to","line":12},{"severity":"warning","code":"w","message":"warn"}]}`}
	_, err := newOperator(t, host).Call(context.Background(), envOf(f.api, "a"), "flow_create", json.RawMessage(`{"id":"triage","spec":"nodes: []"}`))
	if err == nil || !strings.Contains(err.Error(), "[unknown_node] edges[0].to, line 12: edge to nowhere") || !strings.Contains(err.Error(), "nobody was asked") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "warn") && !strings.Contains(err.Error(), "1 error") {
		t.Errorf("warnings are mixed into the errors: %v", err)
	}
	if len(host.asked) != 0 || len(f.created) != 0 {
		t.Errorf("an invalid spec reached approval or creation: %+v %+v", host.asked, f.created)
	}
}

func TestFlowCreateShowsTheSpecAndCreatesOnlyWhenApproved(t *testing.T) {
	spec := "description: Triage\nsubscribe: [gh]\nnodes:\n  - {id: a, type: command, script: echo}\n"
	args, _ := json.Marshal(map[string]any{"id": "triage", "spec": spec})

	denied := &fakeOperatorHost{decision: Decision{Comment: "too risky"}}
	f := &fakeSpecAPI{validate: validFlowResponse}
	_, err := newOperator(t, denied).Call(context.Background(), envOf(f.api, "alice"), "flow_create", args)
	var de *DeniedError
	if !errors.As(err, &de) || de.Comment != "too risky" || len(f.created) != 0 {
		t.Fatalf("denied: err=%v created=%v", err, f.created)
	}
	ask := denied.asked[0]
	for _, want := range []string{"triage", "Triage", "`gh`", "`triage`", "`lookup`", "`other`", "command nodes `a`", "disabled", "```yaml", "script: echo", "test-agent 1.0"} {
		if !strings.Contains(ask.BodyMD+ask.Title, want) {
			t.Errorf("approval lacks %q:\n%s", want, ask.BodyMD)
		}
	}
	if ask.Title != "Create flow: triage" || ask.Timeout != time.Minute {
		t.Errorf("approval = %+v", ask)
	}

	allowed := &fakeOperatorHost{decision: Decision{Allowed: true}}
	f = &fakeSpecAPI{validate: validFlowResponse}
	res, err := newOperator(t, allowed).Call(context.Background(), envOf(f.api, "alice"), "flow_create", args)
	if err != nil || !strings.Contains(res.Text, `"created":true`) || !strings.Contains(res.Text, `"enabled":false`) {
		t.Fatalf("approved: %q %v", res.Text, err)
	}
	var body struct{ ID, YAML string }
	if len(f.created) != 1 || f.created[0].path != "/api/flows" {
		t.Fatalf("created = %+v", f.created)
	}
	_ = json.Unmarshal([]byte(f.created[0].body), &body)
	if body.ID != "triage" || body.YAML != spec {
		t.Errorf("create body = %+v", body)
	}
}

func TestFlowCreateRefusesAnExistingIdAndNeverOverwrites(t *testing.T) {
	host := &fakeOperatorHost{decision: Decision{Allowed: true}}
	f := &fakeSpecAPI{validate: validFlowResponse, existing: map[string]bool{"triage": true}}
	_, err := newOperator(t, host).Call(context.Background(), envOf(f.api, "a"), "flow_create", json.RawMessage(`{"id":"triage","spec":"nodes: []"}`))
	if err == nil || !strings.Contains(err.Error(), "already exists") || len(host.asked) != 0 || len(f.created) != 0 {
		t.Errorf("err=%v asked=%d created=%d", err, len(host.asked), len(f.created))
	}
}

func TestSpecTextConvertsJSONToBlockYAMLKeepingKeyOrder(t *testing.T) {
	got, err := specText(`{"id":"x","description":"a\nb","nodes":[{"id":"n","type":"command","script":"echo hi"}],"enabled":true,"max_concurrent":2}`)
	if err != nil {
		t.Fatal(err)
	}
	want := "id: x\ndescription: |-\n  a\n  b\nnodes:\n  - id: n\n    type: command\n    script: echo hi\nenabled: true\nmax_concurrent: 2\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if specID(got) != "x" {
		t.Errorf("specID = %q", specID(got))
	}
	if _, err := specText(strings.Repeat("x", maxSpecBytes+1)); err == nil {
		t.Error("an oversized spec was accepted")
	}
}

func TestSensorCreateShowsScheduleAndCodeAndRefusesDuplicates(t *testing.T) {
	validate := `{"valid":true,"diagnostics":[],"sensor":{"description":"Poll gh","interval":"5m","lang":"bash","script":"gh api /notifications"}}`
	host := &fakeOperatorHost{decision: Decision{Allowed: true}}
	f := &fakeSpecAPI{validate: validate}
	res, err := newOperator(t, host).Call(context.Background(), envOf(f.api, "a"), "sensor_create", json.RawMessage(`{"id":"gh","spec":"lang: bash\nscript: gh api /notifications\nkey: '{{.id}}'\n"}`))
	if err != nil || !strings.Contains(res.Text, `"created":true`) {
		t.Fatalf("sensor_create: %q %v", res.Text, err)
	}
	ask := host.asked[0]
	for _, want := range []string{"sensors/gh.yaml", "every 5m", "bash", "Runs code", "gh api /notifications"} {
		if !strings.Contains(ask.BodyMD, want) {
			t.Errorf("approval lacks %q:\n%s", want, ask.BodyMD)
		}
	}
	if f.created[0].path != "/api/sensors" {
		t.Errorf("created at %s", f.created[0].path)
	}

	host2 := &fakeOperatorHost{decision: Decision{Allowed: true}}
	f2 := &fakeSpecAPI{validate: validate, existing: map[string]bool{"gh": true}}
	if _, err := newOperator(t, host2).Call(context.Background(), envOf(f2.api, "a"), "sensor_create", json.RawMessage(`{"id":"gh","spec":"lang: bash"}`)); err == nil || !strings.Contains(err.Error(), "already exists") || len(host2.asked) != 0 {
		t.Errorf("duplicate sensor: err=%v asked=%d", err, len(host2.asked))
	}
}

func TestValidateToolsReportWithoutAskingOrCreating(t *testing.T) {
	host := &fakeOperatorHost{}
	f := &fakeSpecAPI{validate: validFlowResponse, existing: map[string]bool{"taken": true}}
	o := newOperator(t, host)
	res, err := o.Call(context.Background(), envOf(f.api, "a"), "flow_validate", json.RawMessage(`{"id":"fresh","spec":"nodes: []"}`))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Valid       bool
		IDAvailable bool `json:"id_available"`
		Errors      []diagnostic
		Warnings    []diagnostic
	}
	_ = json.Unmarshal([]byte(res.Text), &v)
	if !v.Valid || !v.IDAvailable || len(v.Errors) != 0 || len(v.Warnings) != 1 {
		t.Errorf("result = %s", res.Text)
	}
	res, _ = o.Call(context.Background(), envOf(f.api, "a"), "flow_validate", json.RawMessage(`{"id":"taken","spec":"nodes: []"}`))
	_ = json.Unmarshal([]byte(res.Text), &v)
	if v.IDAvailable {
		t.Errorf("a taken id is available: %s", res.Text)
	}
	res, _ = o.Call(context.Background(), envOf(f.api, "a"), "flow_validate", json.RawMessage(`{"id":"bad id","spec":"nodes: []"}`))
	if !strings.Contains(res.Text, "invalid_id") {
		t.Errorf("bad id: %s", res.Text)
	}
	if len(host.asked) != 0 || len(f.created) != 0 {
		t.Errorf("validation asked or created: %+v %+v", host.asked, f.created)
	}
}

func approvalView(status, comment string, editable string, edits string) string {
	return `{"id":"A1","type":"request","status":"` + status + `","comment":"` + comment + `","decided_by":"alice","decided_at":"2026-01-01T00:00:00Z","expires_at":"2026-01-02T00:00:00Z","title":"t","editable":` + editable + `,"edits":` + edits + `}`
}

func TestRequestApprovalReturnsTheDecisionCommentAndEditedValues(t *testing.T) {
	host := &fakeOperatorHost{questionID: "A1"}
	o := newOperator(t, host)
	args := json.RawMessage(`{"title":"Send this reply?","description":"Reply to the customer about the refund.","body":"details","priority":"high","timeout_seconds":600,"wait_seconds":3,
		"editable":[{"key":"reply","value":"Hi"},{"key":"count","value":2}]}`)

	var sent []sentRequest
	api := recordingAPI(200, approvalView("approved", "ok, but shorter", `[{"key":"reply","value":"Hi"},{"key":"count","value":2}]`, `{"reply":{"before":"Hi","after":"Hello"}}`), &sent)
	res, err := o.Call(context.Background(), envOf(api, "alice"), "request_approval", args)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ApprovalID string `json:"approval_id"`
		Status     string
		Decision   string
		Comment    string
		Values     map[string]any
		Edited     bool
	}
	_ = json.Unmarshal([]byte(res.Text), &got)
	if got.ApprovalID != "A1" || got.Decision != "approved" || got.Status != "approved" || got.Comment != "ok, but shorter" || got.Values["reply"] != "Hello" || got.Values["count"] != float64(2) || !got.Edited {
		t.Errorf("result = %s", res.Text)
	}
	q := host.questions[0]
	if q.Title != "Send this reply?" || q.Description != "Reply to the customer about the refund." || q.Priority != "high" || q.Timeout != 600*time.Second || q.Wait != 3*time.Second || len(q.Editable) != 2 || string(q.Editable[0].Value) != `"Hi"` || q.Caller.Client != "test-agent 1.0" {
		t.Errorf("question = %+v", q)
	}

	api = recordingAPI(200, approvalView("denied", "no", `[{"key":"reply","value":"Hi"}]`, `null`), &sent)
	res, _ = o.Call(context.Background(), envOf(api, "alice"), "request_approval", args)
	if strings.Contains(res.Text, `"values"`) || !strings.Contains(res.Text, `"decision":"denied"`) || !strings.Contains(res.Text, `"comment":"no"`) || !strings.Contains(res.Text, `"message":"Denied by the user: \"no\". Do not retry the same action unchanged; do what the user asked instead."`) {
		t.Errorf("denied result = %s", res.Text)
	}

	api = recordingAPI(200, `{"id":"A1","type":"request","status":"pending","comment":null,"expires_at":"2026-01-02T00:00:00Z","editable":[]}`, &sent)
	res, _ = o.Call(context.Background(), envOf(api, "alice"), "request_approval", json.RawMessage(`{"title":"t","description":"d"}`))
	if !strings.Contains(res.Text, `"status":"pending"`) || !strings.Contains(res.Text, `"approval_id":"A1"`) || !strings.Contains(res.Text, "approval_get") {
		t.Errorf("pending result = %s", res.Text)
	}
	if q := host.questions[2]; q.Timeout != DefaultRequestTimeout || q.Wait != MaxRequestWait {
		t.Errorf("defaults: timeout %v wait %v", q.Timeout, q.Wait)
	}

	// approval_get carries the same outcome in result.
	api = recordingAPI(200, approvalView("approved", "fine", `[{"key":"reply","value":"Hi"}]`, `null`), &sent)
	res, err = o.Call(context.Background(), envOf(api, "alice"), "approval_get", json.RawMessage(`{"id":"A1"}`))
	if err != nil || !strings.Contains(res.Text, `"result":{`) || !strings.Contains(res.Text, `"decision":"approved"`) || !strings.Contains(res.Text, `"comment":"fine"`) || !strings.Contains(res.Text, `"values":{"reply":"Hi"}`) {
		t.Errorf("approval_get = %q %v", res.Text, err)
	}
}

func TestSendNotePassesTheNoteAndRateLimitsPerClient(t *testing.T) {
	host := &fakeOperatorHost{}
	o := newOperator(t, host)
	var sent []sentRequest
	env := envOf(recordingAPI(200, `{}`, &sent), "alice")
	res, err := o.Call(context.Background(), env, "send_note", json.RawMessage(`{"title":"Build done","body":"**ok**","priority":"urgent","url":"https://example.com/x","link_label":"CI","thread":"ci"}`))
	if err != nil || !strings.Contains(res.Text, "item1") {
		t.Fatalf("send_note: %q %v", res.Text, err)
	}
	n := host.notes[0]
	if n.Title != "Build done" || n.Priority != "urgent" || n.URL != "https://example.com/x" || n.URLLabel != "CI" || n.Thread != "ci" || n.Caller.Client != "test-agent 1.0" {
		t.Errorf("note = %+v", n)
	}
	for i := 1; i < NotesPerMinute; i++ {
		if _, err := o.Call(context.Background(), env, "send_note", json.RawMessage(`{"title":"n"}`)); err != nil {
			t.Fatalf("note %d: %v", i, err)
		}
	}
	if _, err := o.Call(context.Background(), env, "send_note", json.RawMessage(`{"title":"one too many"}`)); err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("over the limit: %v", err)
	}
	other := envOf(recordingAPI(200, `{}`, &sent), "alice")
	other.Caller.Client = "another-agent"
	if _, err := o.Call(context.Background(), other, "send_note", json.RawMessage(`{"title":"n"}`)); err != nil {
		t.Errorf("another client was limited: %v", err)
	}
	if len(host.notes) != NotesPerMinute+1 {
		t.Errorf("notes posted = %d", len(host.notes))
	}
}

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
	decision Decision
	err      error
	asked    []OperatorApproval
}

func (h *fakeOperatorHost) ApproveOperator(_ context.Context, req OperatorApproval) (Decision, error) {
	h.asked = append(h.asked, req)
	return h.decision, h.err
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

func TestOperatorReadsMapToTheRESTAPIWithoutApproval(t *testing.T) {
	host := &fakeOperatorHost{}
	o := newOperator(t, host)
	var sent []sentRequest
	api := recordingAPI(200, `{"items":[]}`, &sent)

	res, err := o.Call(context.Background(), api, "localhost", "inbox_list", json.RawMessage(`{"q":"deploy","read":false,"limit":5,"archived":"any"}`))
	if err != nil || res.Text != `{"items":[]}` {
		t.Fatalf("inbox_list: %q %v", res.Text, err)
	}
	got := sent[0]
	if got.method != http.MethodGet || got.path != "/api/inbox" || got.query.Get("q") != "deploy" || got.query.Get("read") != "false" || got.query.Get("limit") != "5" || got.query.Get("archived") != "any" {
		t.Errorf("request = %+v", got)
	}
	if _, err := o.Call(context.Background(), api, "localhost", "approvals_list", nil); err != nil {
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
	api := recordingAPI(202, `{"flow_run_ids":["r1"]}`, &sent)

	denied := &fakeOperatorHost{decision: Decision{Comment: "no"}}
	o := newOperator(t, denied)
	_, err := o.Call(context.Background(), api, "alice", "flow_run", json.RawMessage(`{"id":"pr-review","rerun_seen":true}`))
	var de *DeniedError
	if !errors.As(err, &de) || de.Comment != "no" {
		t.Fatalf("denied flow_run error = %v", err)
	}
	if len(sent) != 0 {
		t.Fatalf("a denied call reached the API: %+v", sent)
	}
	if len(denied.asked) != 1 || denied.asked[0].Caller != "alice" || denied.asked[0].Timeout != time.Minute ||
		!strings.Contains(denied.asked[0].Reason, `"pr-review"`) {
		t.Errorf("approval request = %+v", denied.asked)
	}

	expired := &fakeOperatorHost{decision: Decision{Expired: true}}
	if _, err := newOperator(t, expired).Call(context.Background(), api, "a", "sensor_poll", json.RawMessage(`{"id":"s"}`)); !errors.As(err, &de) || !de.Expired {
		t.Errorf("expired sensor_poll error = %v", err)
	}

	allowed := &fakeOperatorHost{decision: Decision{Allowed: true}}
	res, err := newOperator(t, allowed).Call(context.Background(), api, "alice", "flow_run", json.RawMessage(`{"id":"pr-review","rerun_seen":true}`))
	if err != nil || !strings.Contains(res.Text, "r1") {
		t.Fatalf("approved flow_run: %q %v", res.Text, err)
	}
	if len(sent) != 1 || sent[0].method != http.MethodPost || sent[0].path != "/api/flows/pr-review/run" || sent[0].body != `{"rerun_seen":true}` {
		t.Errorf("request = %+v", sent)
	}

	broken := &fakeOperatorHost{err: errors.New("store down")}
	if _, err := newOperator(t, broken).Call(context.Background(), api, "a", "flow_run", json.RawMessage(`{"id":"f"}`)); err == nil || !strings.Contains(err.Error(), "approval failed") {
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
	api := recordingAPI(200, `{}`, &sent)
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
	} {
		if _, err := o.Call(context.Background(), api, "a", tc.tool, json.RawMessage(tc.args)); err == nil {
			t.Errorf("%s %s was accepted", tc.tool, tc.args)
		}
	}
	if len(host.asked) != 0 || len(sent) != 0 {
		t.Errorf("bad arguments reached approval or the API: asked=%d sent=%d", len(host.asked), len(sent))
	}
	if _, err := o.Call(context.Background(), api, "a", "approve_approval", nil); !errors.Is(err, ErrUnknownTool) {
		t.Errorf("unknown tool error = %v", err)
	}
}

func TestOperatorTurnsAPIProblemsIntoToolErrorsAndTruncatesLongResults(t *testing.T) {
	o := newOperator(t, &fakeOperatorHost{})
	var sent []sentRequest
	_, err := o.Call(context.Background(), recordingAPI(404, `{"title":"Not found","detail":"no flow x"}`, &sent), "a", "flow_get", json.RawMessage(`{"id":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "no flow x") {
		t.Errorf("404 error = %v", err)
	}
	res, err := o.Call(context.Background(), recordingAPI(200, strings.Repeat("é", maxOperatorText), &sent), "a", "flows_list", nil)
	if err != nil || len(res.Text) > maxOperatorText+200 || !strings.Contains(res.Text, "[truncated") {
		t.Errorf("long result: len=%d err=%v", len(res.Text), err)
	}
}

func TestOperatorToolsCannotDecideApprovals(t *testing.T) {
	o := newOperator(t, &fakeOperatorHost{})
	for _, info := range o.Tools() {
		if strings.Contains(info.Name, "approve") && info.Name != "approvals_list" && info.Name != "approval_get" {
			t.Errorf("operator tool %q could decide approvals", info.Name)
		}
	}
	if !o.NeedsApproval("flow_run") || !o.NeedsApproval("sensor_poll") || o.NeedsApproval("flows_list") {
		t.Error("approval flags are wrong")
	}
}

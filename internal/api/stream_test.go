package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"hal9001/internal/auth"
	"hal9001/internal/store"
)

type sseMsg struct {
	ID, Event string
	Data      map[string]any
	Comment   string
}

type sseClient struct {
	t    *testing.T
	body io.ReadCloser
	msgs chan sseMsg
}

// openStream connects to /api/stream of a real listener, so the handler
// streams and flushes as it does in the daemon.
func (e *env) openStream(ts *httptest.Server, query string, header map[string]string) *sseClient {
	e.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/api/stream"+query, nil)
	must(e.t, err)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	must(e.t, err)
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		b, _ := io.ReadAll(res.Body)
		e.t.Fatalf("stream: %d %s %s", res.StatusCode, res.Header.Get("Content-Type"), b)
	}
	c := &sseClient{t: e.t, body: res.Body, msgs: make(chan sseMsg, 1024)}
	e.t.Cleanup(func() { _ = res.Body.Close() })
	go func() {
		defer close(c.msgs)
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var cur sseMsg
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if cur.Event != "" {
					c.msgs <- cur
				}
				cur = sseMsg{}
			case strings.HasPrefix(line, ":"):
				c.msgs <- sseMsg{Comment: strings.TrimSpace(line[1:])}
			case strings.HasPrefix(line, "id: "):
				cur.ID = line[4:]
			case strings.HasPrefix(line, "event: "):
				cur.Event = line[7:]
			case strings.HasPrefix(line, "data: "):
				_ = json.Unmarshal([]byte(line[6:]), &cur.Data)
			}
		}
	}()
	return c
}

func (c *sseClient) next() sseMsg {
	c.t.Helper()
	select {
	case m, ok := <-c.msgs:
		if !ok {
			c.t.Fatal("stream ended")
		}
		return m
	case <-time.After(5 * time.Second):
		c.t.Fatal("timed out waiting for a stream message")
	}
	return sseMsg{}
}

// expect returns the next message, which must have the given event type.
func (c *sseClient) expect(typ string) sseMsg {
	c.t.Helper()
	m := c.next()
	if m.Event != typ {
		c.t.Fatalf("got %q (%v), want %q", m.Event, m.Data, typ)
	}
	// stream.ready on an idle hub has no SSE id (so a reconnecting browser
	// keeps its Last-Event-ID) and the envelope id "0".
	wantID := m.ID
	if wantID == "" {
		wantID = "0"
	}
	if m.Data["type"] != typ || m.Data["id"] != wantID || m.Data["ts"] == nil {
		c.t.Fatalf("envelope does not mirror the SSE fields: id %q %v", m.ID, m.Data)
	}
	if !conforms("StreamEvent", m.Data) {
		c.t.Errorf("%s does not match the StreamEvent schema: %v", typ, m.Data)
	}
	return m
}

// expectUntil skips messages until one has the given type.
func (c *sseClient) expectUntil(typ string) sseMsg {
	c.t.Helper()
	for range 50 {
		if m := c.next(); m.Event == typ {
			return m
		}
	}
	c.t.Fatalf("never saw %q", typ)
	return sseMsg{}
}

// quiet fails if anything arrives within a short window. The messages that could arrive are
// ordered behind the ones the test already received, so the window only has to cover delivery.
func (c *sseClient) quiet() {
	const d = 50 * time.Millisecond
	c.t.Helper()
	select {
	case m := <-c.msgs:
		c.t.Fatalf("unexpected message: %s %v", m.Event, m.Data)
	case <-time.After(d):
	}
}

func (m sseMsg) data() map[string]any { d, _ := m.Data["data"].(map[string]any); return d }

func TestStreamDeliversChangesAndFiltersByType(t *testing.T) {
	e := newEnv(t)
	ts := e.listen()
	c := e.openStream(ts, "?types=flow_run.created,flow_run.updated,inbox.created", nil)

	ready := c.expect("stream.ready")
	if ready.data()["last_event_id"] != "" || ready.data()["version"] != "test" {
		t.Errorf("ready = %v", ready.Data)
	}

	fr := e.flowRun("demo", "", store.KindLive, store.FlowRunQueued)
	created := c.expect("flow_run.created")
	if created.data()["id"] != fr.ID || created.data()["status"] != "queued" {
		t.Errorf("created = %v", created.Data)
	}
	e.event("gh", "k", `{}`)                                // not subscribed to
	nr := e.nodeRun(fr.ID, "filter", store.NodeQueued, nil) // not subscribed to
	fr.Status = store.FlowRunRunning
	must(t, e.st.UpdateFlowRun(ctx(), fr))
	updated := c.expect("flow_run.updated")
	if updated.data()["status"] != "running" {
		t.Errorf("updated = %v", updated.Data)
	}
	it := &store.InboxItem{SourceType: store.SourceNodeRun, NodeRunID: nr.ID, Title: "hello", Priority: store.PriorityHigh}
	must(t, e.st.CreateInboxItem(ctx(), it))
	inbox := c.expect("inbox.created")
	if inbox.data()["title"] != "hello" || inbox.data()["flow_id"] != "demo" {
		t.Errorf("inbox = %v", inbox.Data)
	}
	c.quiet()

	a, _ := strconv.ParseUint(created.ID, 10, 64)
	b, _ := strconv.ParseUint(updated.ID, 10, 64)
	d, _ := strconv.ParseUint(inbox.ID, 10, 64)
	if a >= b || b >= d {
		t.Errorf("ids must increase: %d %d %d", a, b, d)
	}
}

func TestStreamCarriesRecordsStatusKillSwitchAndConfig(t *testing.T) {
	e := newEnv(t)
	ts := e.listen()
	c := e.openStream(ts, "", nil)
	c.expect("stream.ready")

	ev := e.event("gh", "k", `{"x":1}`)
	if m := c.expect("event.created"); m.data()["id"] != ev.ID || m.data()["payload_preview"] != `{"x":1}` {
		t.Errorf("event.created = %v", m.Data)
	}
	fr := e.flowRun("demo", ev.ID, store.KindLive, store.FlowRunQueued)
	c.expect("flow_run.created")
	if m := c.expect("status.changed"); m.data()["queued_flow_runs"] != float64(1) {
		t.Errorf("status.changed = %v", m.Data)
	}
	nr := e.nodeRun(fr.ID, "review", store.NodeRunning, func(n *store.NodeRun) { n.NodeType = store.NodeAgent })
	if m := c.expect("node_run.created"); m.data()["node_id"] != "review" {
		t.Errorf("node_run.created = %v", m.Data)
	}
	f := e.agentFixtureFor(fr, nr)
	c.expectUntil("agent_run.created")
	if m := c.expectUntil("agent_event.appended"); m.data()["agent_run_id"] != f.agentRun.ID || m.data()["seq"] != float64(1) {
		t.Errorf("agent_event = %v", m.Data)
	}
	c.expectUntil("permission_request.created")
	if m := c.expectUntil("approval.created"); m.data()["flow_id"] != "demo" {
		t.Errorf("approval = %v", m.Data)
	}
	c.expectUntil("status.changed")

	// Kill switch changes, with the reason the API stored.
	must(t, e.st.SetSetting(ctx(), killSwitchReasonKey, json.RawMessage(`"deploy"`)))
	must(t, e.st.SetKillSwitch(ctx(), store.KillSwitch{Paused: true, By: "localhost", Since: time.Now()}))
	var ks sseMsg
	for range 4 {
		if ks = c.expectUntil("killswitch.changed"); ks.data()["paused"] == true {
			break
		}
	}
	if ks.data()["paused"] != true || ks.data()["by"] != "localhost" {
		t.Errorf("killswitch.changed = %v", ks.Data)
	}

	// A write through the API announces the file.
	r := e.do("POST", "/api/prompts", map[string]any{"markdown": "---\nname: fresh\n---\nHi\n"}).want(201)
	cfg := c.expectUntil("config.changed")
	if cfg.data()["kind"] != "prompt" || cfg.data()["id"] != "fresh" || cfg.data()["deleted"] != false || cfg.data()["hash"] == nil {
		t.Errorf("config.changed = %v (%s)", cfg.Data, r.Body)
	}
	e.srv.PublishConfigChange([]string{"config.yaml", "flows/gone.yaml", "notes.txt"})
	if m := c.expectUntil("config.changed"); m.data()["kind"] != "settings" || m.data()["id"] != nil {
		t.Errorf("settings change = %v", m.Data)
	}
	if m := c.expect("config.changed"); m.data()["kind"] != "flow" || m.data()["deleted"] != true || m.data()["hash"] != nil {
		t.Errorf("deleted flow = %v", m.Data)
	}
}

// agentFixtureFor builds agent rows under an existing flow/node run.
func (e *env) agentFixtureFor(fr *store.FlowRun, nr *store.NodeRun) agentFixture {
	e.t.Helper()
	f := agentFixture{flowRun: fr, nodeRun: nr}
	f.agentRun = e.agentRun(nr.ID, store.AgentRunning)
	must(e.t, e.st.AppendAgentEvent(ctx(), &store.AgentEvent{AgentRunID: f.agentRun.ID, Kind: "message", Payload: store.Inline(`{"text":"hi"}`)}))
	f.perm = &store.PermissionRequest{AgentRunID: f.agentRun.ID, ToolCallID: "c", ToolKind: "edit", Title: "Edit", RawInput: store.Inline(`{}`)}
	must(e.t, e.st.CreatePermissionRequest(ctx(), f.perm))
	f.toolAppr = seeded(&store.Approval{AgentRunID: f.agentRun.ID, Type: store.ApprovalToolCall, PermissionRequestID: f.perm.ID})
	must(e.t, e.st.CreateApproval(ctx(), f.toolAppr))
	return f
}

func TestStreamResourceFiltersLeaveOtherTypesAlone(t *testing.T) {
	e := newEnv(t)
	ts := e.listen()
	mine := e.flowRun("demo", "", store.KindLive, store.FlowRunQueued)
	other := e.flowRun("demo", "", store.KindLive, store.FlowRunQueued)
	e.settleHub() // the setup changes must not reach the stream opened next
	c := e.openStream(ts, "?flow_run_id="+mine.ID+"&sensor_id=gh", nil)
	c.expect("stream.ready")

	e.nodeRun(other.ID, "filter", store.NodeQueued, nil)
	e.nodeRun(mine.ID, "filter", store.NodeQueued, nil)
	if m := c.expect("node_run.created"); m.data()["flow_run_id"] != mine.ID {
		t.Errorf("a node run of another flow run got through: %v", m.Data)
	}
	e.event("slack", "k", `{}`) // sensor_id filter drops events of other sensors
	e.event("gh", "k", `{}`)
	if m := c.expect("event.created"); m.data()["sensor_id"] != "gh" {
		t.Errorf("event.created = %v", m.Data)
	}
	// Types without the filtered resource (status) still arrive, while the
	// flow_run.created of the unrelated run is dropped.
	e.flowRun("demo", "", store.KindLive, store.FlowRunQueued)
	if m := c.expect("status.changed"); m.data()["queued_flow_runs"] != float64(3) {
		t.Errorf("status.changed = %v", m.Data)
	}
	e.get("/api/stream?types=bogus").want(400)
	e.get("/api/stream?flow_run_id=lower").want(400)
}

func TestStreamResumeReplaysOrResyncs(t *testing.T) {
	e := newEnv(t)
	ts := e.listen()
	c := e.openStream(ts, "?types=event.created", nil)
	c.expect("stream.ready")
	e1 := e.event("gh", "1", `{}`)
	first := c.expect("event.created")
	_ = c.body.Close()

	e2 := e.event("gh", "2", `{}`)
	e3 := e.event("gh", "3", `{}`)
	e.settleHub() // the hub reads the changes asynchronously

	c = e.openStream(ts, "?types=event.created", map[string]string{"Last-Event-ID": first.ID})
	ready := c.expect("stream.ready")
	if ready.data()["last_event_id"] == "" {
		t.Error("ready should name the newest buffered message once there is one")
	}
	if m := c.expect("event.created"); m.data()["id"] != e2.ID {
		t.Errorf("replay started at %v, want %s (not %s)", m.data()["id"], e2.ID, e1.ID)
	}
	if m := c.expect("event.created"); m.data()["id"] != e3.ID {
		t.Errorf("replay continued with %v", m.data()["id"])
	}
	c.quiet()

	// An id from a previous daemon run cannot be replayed.
	c = e.openStream(ts, "", map[string]string{"Last-Event-ID": "5"})
	c.expect("stream.ready")
	if m := c.expect("stream.resync"); m.data()["reason"] != "daemon_restarted" {
		t.Errorf("resync = %v", m.Data)
	}

	// A gap larger than the buffer cannot be replayed either.
	for range ringSize + 10 {
		e.srv.hub.publish("status.changed", map[string]any{}, nil, false)
	}
	c = e.openStream(ts, "?types=stream.resync", map[string]string{"Last-Event-ID": first.ID})
	c.expect("stream.ready")
	if m := c.expect("stream.resync"); m.data()["reason"] != "buffer_overrun" {
		t.Errorf("resync = %v", m.Data)
	}
}

func TestSlowClientsAreResyncedNotBlocking(t *testing.T) {
	e := newEnv(t)
	slow := &client{wake: make(chan struct{}, 1)}
	e.srv.hub.subscribe(slow, "")
	defer e.srv.hub.unsubscribe(slow)

	total := clientBuffer + 50
	for i := range total {
		e.srv.hub.publish("status.changed", map[string]any{"i": i}, nil, false)
	}
	msgs, dropped := slow.take()
	if !dropped {
		t.Fatal("a client that never reads must be flagged for resync")
	}
	if len(msgs) == 0 || len(msgs) > clientBuffer {
		t.Fatalf("queue holds %d messages", len(msgs))
	}
	last := msgs[len(msgs)-1]
	var env struct{ Data struct{ I int } }
	must(t, json.Unmarshal(last.data, &env))
	if env.Data.I != total-1 {
		t.Errorf("the newest message must survive the drop, got #%d", env.Data.I)
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i].seq != msgs[i-1].seq+1 {
			t.Fatal("messages after a drop must stay contiguous and in order")
		}
	}
	if _, dropped := slow.take(); dropped {
		t.Error("the flag clears once the client has been told")
	}
}

func TestStreamHeartbeatPrunedAndShutdown(t *testing.T) {
	e := newEnv(t)
	e.srv.heartbeat = 40 * time.Millisecond
	ts := e.listen()
	c := e.openStream(ts, "?types=stream.resync", nil)
	c.expect("stream.ready")
	if m := c.next(); m.Comment != "keepalive" {
		t.Errorf("expected a keepalive comment, got %+v", m)
	}

	// Bulk deletes by retention pruning tell clients to refetch.
	e.srv.hub.enqueue(store.Change{Resource: store.ResFlowRun, ID: "*", Op: store.OpDelete})
	if m := c.expectUntil("stream.resync"); m.data()["reason"] != "pruned" {
		t.Errorf("resync = %v", m.Data)
	}

	e.srv.Close()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-c.msgs:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("the stream stayed open after Close")
		}
	}
}

func TestAuthEventsReachOnlyCallersGrantedAdmin(t *testing.T) {
	// api is trusted locally; admin needs a password nobody presents.
	cfg := lockedConfig()
	cfg.Stacks["local"] = []auth.StackEntry{{Module: "localhost-trust", Control: auth.Sufficient}}
	cfg.Services[auth.ServiceAPI] = "local"
	e := newEnv(t, func(o *envOptions) { o.auth = &cfg })
	ts := e.listen()
	c := e.openStream(ts, "?types=auth_event.created,event.created", nil)
	c.expect("stream.ready")

	must(t, e.st.CreateAuthEvent(ctx(), &store.AuthEvent{Action: "request", Service: store.ServiceAdmin, Endpoint: "GET /api/flows", Outcome: "fail", RemoteAddr: "127.0.0.1:1", Host: "localhost"}))
	e.event("gh", "k", `{}`)
	if m := c.expect("event.created"); m.data()["sensor_id"] != "gh" {
		t.Errorf("got %v", m.Data)
	}
	c.quiet()

	// With localhost-trust on the admin stack the same caller sees them.
	e2 := newEnv(t)
	ts2 := e2.listen()
	c2 := e2.openStream(ts2, "?types=auth_event.created", nil)
	c2.expect("stream.ready")
	must(t, e2.st.CreateAuthEvent(ctx(), &store.AuthEvent{Action: "login_begin", Service: store.ServiceAPI, Endpoint: "POST /api/auth/api/begin", Outcome: "success",
		Principal: "owner", RemoteAddr: "127.0.0.1:1", Host: "localhost", Results: []store.ModuleResult{{Module: "password", Control: "sufficient", Result: "success"}}}))
	m := c2.expectUntil("auth_event.created")
	if m.data()["principal"] != "owner" || m.data()["action"] != "login_begin" {
		t.Errorf("auth event = %v", m.Data)
	}
}

// listen serves the API on a real listener. Open streams are closed before
// the listener, which would otherwise wait for them.
func (e *env) listen() *httptest.Server {
	ts := httptest.NewServer(e.srv)
	e.t.Cleanup(func() {
		ts.CloseClientConnections()
		ts.Close()
	})
	return ts
}

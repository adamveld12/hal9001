package api

import (
	"encoding/json"
	"strings"
	"testing"

	"hal9001/internal/store"
)

func (e *env) sandboxEntry(sessionID, kind, level, title string, d store.SandboxLogData) *store.SandboxLogEntry {
	e.t.Helper()
	raw, err := json.Marshal(d)
	must(e.t, err)
	en := &store.SandboxLogEntry{SessionID: sessionID, Kind: kind, Level: level, Title: title, Data: raw}
	must(e.t, e.st.AppendSandboxLogEntry(ctx(), en))
	return en
}

func TestSandboxLogEndpoints(t *testing.T) {
	e := newEnv(t)
	fr := e.flowRun("demo", "", store.KindLive, store.FlowRunRunning)
	nr := e.nodeRun(fr.ID, "review", store.NodeRunning, func(n *store.NodeRun) { n.NodeType = store.NodeAgent })
	ar := e.agentRun(nr.ID, store.AgentRunning)
	ss := &store.SandboxSession{NodeRunID: nr.ID, AgentRunID: ar.ID, Backend: "docker", Image: "alpine", EnvNames: []string{"TOKEN"}}
	must(t, e.st.CreateSandboxSession(ctx(), ss))
	zero, ms := 0, int64(12)
	e.sandboxEntry(ss.ID, store.SandboxKindExec, store.SandboxInfo, "echo hi", store.SandboxLogData{
		Tool: "shell", Command: "echo hi", ExitCode: &zero, DurationMS: &ms, Stdout: "hi\n", Stderr: "warn\n"})
	e.sandboxEntry(ss.ID, store.SandboxKindDenied, store.SandboxWarn, "rm -rf /", store.SandboxLogData{Tool: "shell", Approval: "denied by policy: readonly"})
	e.sandboxEntry(ss.ID, store.SandboxKindLifecycle, store.SandboxError, "teardown failed", store.SandboxLogData{Error: "boom"})

	type entry = map[string]any
	var got struct {
		Sessions []map[string]any `json:"sessions"`
		Entries  []entry          `json:"entries"`
		HasMore  bool             `json:"has_more"`
	}
	e.get("/api/agent-runs/" + ar.ID + "/sandbox-log").want(200).decode(&got)
	if len(got.Sessions) != 1 || len(got.Entries) != 3 || got.HasMore {
		t.Fatalf("log = %+v", got)
	}
	first := got.Entries[0]
	if v, ok := first["exit_code"]; !ok || v != float64(0) || first["stdout"] != "hi\n" || first["stderr"] != "warn\n" || first["command"] != "echo hi" {
		t.Errorf("exec entry not flattened: %v", first)
	}
	if _, ok := got.Entries[1]["exit_code"]; ok || got.Entries[1]["approval"] != "denied by policy: readonly" {
		t.Errorf("denied entry = %v", got.Entries[1])
	}
	if got.Entries[2]["error"] != "boom" || got.Entries[2]["level"] != "error" {
		t.Errorf("lifecycle entry = %v", got.Entries[2])
	}

	// paging
	firstID := first["id"].(float64)
	e.get("/api/agent-runs/" + ar.ID + "/sandbox-log?limit=1").want(200).decode(&got)
	if len(got.Entries) != 1 || !got.HasMore || got.Entries[0]["id"] != firstID {
		t.Errorf("limit page = %+v", got)
	}
	e.get("/api/agent-runs/" + ar.ID + "/sandbox-log?after_id=" + strings.TrimSuffix(jsonNum(firstID), ".0")).want(200).decode(&got)
	if len(got.Entries) != 2 || got.HasMore {
		t.Errorf("after_id page = %+v", got)
	}
	e.get("/api/agent-runs/" + ar.ID + "/sandbox-log?tail=true&limit=2").want(200).decode(&got)
	if len(got.Entries) != 2 || !got.HasMore || got.Entries[0]["title"] != "rm -rf /" || got.Entries[1]["title"] != "teardown failed" {
		t.Errorf("tail = %+v", got)
	}
	for _, q := range []string{"limit=0", "limit=1001", "after_id=-1", "tail=maybe"} {
		e.get("/api/agent-runs/" + ar.ID + "/sandbox-log?" + q).want(400)
	}

	// node run aggregates
	e.get("/api/node-runs/" + nr.ID + "/sandbox-log").want(200).decode(&got)
	if len(got.Entries) != 3 {
		t.Errorf("node run entries = %d", len(got.Entries))
	}

	// a session without an agent run row (provisioning failed)
	orphan := &store.SandboxSession{NodeRunID: nr.ID, AgentRunID: e.st.NewID(), Backend: "ssh", Status: store.SandboxFailed, Error: "no route"}
	must(t, e.st.CreateSandboxSession(ctx(), orphan))
	e.get("/api/agent-runs/" + orphan.AgentRunID + "/sandbox-log").want(200).decode(&got)
	if len(got.Sessions) != 1 || got.Entries == nil || len(got.Entries) != 0 {
		t.Errorf("orphan = %+v", got)
	}

	// no sandbox: empty arrays, never null; unknown ids: 404
	ar2 := e.agentRun(e.nodeRun(fr.ID, "other", store.NodeRunning, nil).ID, store.AgentRunning)
	body := string(e.get("/api/agent-runs/" + ar2.ID + "/sandbox-log").want(200).Body)
	if !strings.Contains(body, `"sessions":[]`) || !strings.Contains(body, `"entries":[]`) {
		t.Errorf("empty = %s", body)
	}
	unknown := e.st.NewID()
	e.get("/api/agent-runs/" + unknown + "/sandbox-log").want(404)
	e.get("/api/node-runs/" + unknown + "/sandbox-log").want(404)
	e.get("/api/agent-runs/" + unknown + "/sandbox-log/raw").want(404)

	// raw
	r := e.get("/api/node-runs/" + nr.ID + "/sandbox-log/raw").want(200)
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "text/plain") ||
		r.Header.Get("Content-Disposition") != `attachment; filename="sandbox-log-`+nr.ID+`.txt"` {
		t.Errorf("headers = %v", r.Header)
	}
	text := string(r.Body)
	for _, want := range []string{"(docker)", "image: alpine", "env: TOKEN", "INFO echo hi", "exit code: 0", "stdout:\n    hi", "stderr:\n    warn", "WARN rm -rf /", "approval: denied by policy: readonly", "ERROR teardown failed", "error: boom", "(ssh)"} {
		if !strings.Contains(text, want) {
			t.Errorf("raw log lacks %q:\n%s", want, text)
		}
	}
}

// A chat's sandbox sessions belong to the conversation: they are not part of the original run's logs (the agent run
// and its node run), they are served by the conversation endpoint with the same shape and paging, and their events
// carry the assist session id.
func TestConversationSandboxLogIsSeparateFromTheRunsLog(t *testing.T) {
	e := newEnv(t)
	fr := e.flowRun("demo", "", store.KindLive, store.FlowRunSucceeded)
	nr := e.nodeRun(fr.ID, "review", store.NodeDone, func(n *store.NodeRun) { n.NodeType = store.NodeAgent })
	ar := e.agentRun(nr.ID, store.AgentSucceeded)
	chat := e.assistSession("demo", "idle")

	run := &store.SandboxSession{NodeRunID: nr.ID, AgentRunID: ar.ID, Backend: "local"}
	must(t, e.st.CreateSandboxSession(ctx(), run))
	e.sandboxEntry(run.ID, store.SandboxKindLifecycle, store.SandboxInfo, "Run ended; sandbox kept for 1m", store.SandboxLogData{})
	// The chat's sandbox: reattached first, then recreated. node_run_id stays the original node run.
	first := &store.SandboxSession{NodeRunID: nr.ID, AgentRunID: chat.ID, AssistSessionID: chat.ID, Backend: "local", Status: store.SandboxReady}
	must(t, e.st.CreateSandboxSession(ctx(), first))
	e.sandboxEntry(first.ID, store.SandboxKindLifecycle, store.SandboxInfo, "Reattached sandbox of run "+ar.ID, store.SandboxLogData{})
	second := &store.SandboxSession{NodeRunID: nr.ID, AgentRunID: chat.ID, AssistSessionID: chat.ID, Backend: "local"}
	must(t, e.st.CreateSandboxSession(ctx(), second))
	e.sandboxEntry(second.ID, store.SandboxKindExec, store.SandboxInfo, "echo again", store.SandboxLogData{Command: "echo again"})

	type logResp struct {
		Sessions []map[string]any `json:"sessions"`
		Entries  []map[string]any `json:"entries"`
		HasMore  bool             `json:"has_more"`
	}
	var got logResp
	for _, path := range []string{"/api/agent-runs/" + ar.ID + "/sandbox-log", "/api/node-runs/" + nr.ID + "/sandbox-log"} {
		e.get(path).want(200).decode(&got)
		if len(got.Sessions) != 1 || got.Sessions[0]["id"] != run.ID || got.Sessions[0]["assist_session_id"] != "" || len(got.Entries) != 1 {
			t.Errorf("%s = %+v", path, got)
		}
	}
	if raw := string(e.get("/api/node-runs/" + nr.ID + "/sandbox-log/raw").want(200).Body); strings.Contains(raw, "Reattached") {
		t.Errorf("the node run's raw log has the chat's entries:\n%s", raw)
	}

	e.get("/api/conversations/" + chat.ID + "/sandbox-log").want(200).decode(&got)
	if len(got.Sessions) != 2 || got.Sessions[0]["id"] != first.ID || got.Sessions[1]["id"] != second.ID ||
		got.Sessions[0]["assist_session_id"] != chat.ID || got.Sessions[0]["node_run_id"] != nr.ID || len(got.Entries) != 2 {
		t.Fatalf("conversation log = %+v", got)
	}
	if got.Entries[0]["title"] != "Reattached sandbox of run "+ar.ID || got.Entries[1]["command"] != "echo again" {
		t.Errorf("entries = %+v", got.Entries)
	}
	e.get("/api/conversations/" + chat.ID + "/sandbox-log?limit=1").want(200).decode(&got)
	if len(got.Entries) != 1 || !got.HasMore {
		t.Errorf("limit page = %+v", got)
	}
	e.get("/api/conversations/" + chat.ID + "/sandbox-log?tail=true&limit=1").want(200).decode(&got)
	if len(got.Entries) != 1 || !got.HasMore || got.Entries[0]["command"] != "echo again" {
		t.Errorf("tail = %+v", got)
	}
	e.get("/api/conversations/" + chat.ID + "/sandbox-log?limit=0").want(400)
	raw := e.get("/api/conversations/" + chat.ID + "/sandbox-log/raw").want(200)
	if !strings.Contains(string(raw.Body), "Reattached sandbox of run "+ar.ID) || strings.Contains(string(raw.Body), "Run ended") ||
		raw.Header.Get("Content-Disposition") != `attachment; filename="sandbox-log-`+chat.ID+`.txt"` {
		t.Errorf("raw conversation log: %v\n%s", raw.Header, raw.Body)
	}

	// A chat without a sandbox has an empty log; an agent run id is a conversation of its own and serves the run's log.
	empty := e.assistSession("other", "idle")
	body := string(e.get("/api/conversations/" + empty.ID + "/sandbox-log").want(200).Body)
	if !strings.Contains(body, `"sessions":[]`) || !strings.Contains(body, `"entries":[]`) {
		t.Errorf("empty = %s", body)
	}
	e.get("/api/conversations/" + ar.ID + "/sandbox-log").want(200).decode(&got)
	if len(got.Sessions) != 1 || got.Sessions[0]["id"] != run.ID {
		t.Errorf("flow conversation = %+v", got)
	}
	e.get("/api/conversations/" + e.st.NewID() + "/sandbox-log").want(404)
}
func jsonNum(f float64) string { b, _ := json.Marshal(f); return string(b) }

func TestStreamSandboxLogAppendedOmitsOutput(t *testing.T) {
	e := newEnv(t)
	ts := e.listen()
	fr := e.flowRun("demo", "", store.KindLive, store.FlowRunRunning)
	nr := e.nodeRun(fr.ID, "review", store.NodeRunning, nil)
	ss := &store.SandboxSession{NodeRunID: nr.ID, AgentRunID: e.st.NewID(), Backend: "local"}
	c := e.openStream(ts, "?types=sandbox_session.created,sandbox_log.appended&agent_run_id="+ss.AgentRunID, nil)
	c.expect("stream.ready")
	must(t, e.st.CreateSandboxSession(ctx(), ss))
	if d := c.expect("sandbox_session.created").data(); d["id"] != ss.ID {
		t.Errorf("session event = %v", d)
	}
	zero := 0
	en := e.sandboxEntry(ss.ID, store.SandboxKindExec, store.SandboxInfo, "ls", store.SandboxLogData{Command: "ls", ExitCode: &zero, Stdout: "secret output"})
	d := c.expect("sandbox_log.appended").data()
	if _, ok := d["stdout"]; ok {
		t.Errorf("stdout must not be streamed: %v", d)
	}
	if d["id"] != float64(en.ID) || d["agent_run_id"] != ss.AgentRunID || d["node_run_id"] != nr.ID || d["command"] != "ls" || d["exit_code"] != float64(0) {
		t.Errorf("appended = %v", d)
	}
}

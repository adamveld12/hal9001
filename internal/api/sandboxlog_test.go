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

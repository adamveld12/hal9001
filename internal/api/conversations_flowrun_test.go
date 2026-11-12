package api

import (
	"testing"
	"time"

	"hal9001/internal/store"
)

func (e *env) forkChat(from string, at time.Time) *store.AssistSession {
	e.t.Helper()
	a := &store.AssistSession{
		TargetKind: store.AssistTargetGeneral, Runtime: "claude", Status: store.AssistIdle, CreatedAt: at, DraftHash: hash64, DraftUpdatedBy: store.DraftByUser,
		ForkedFromAgentRunID: from, ForkContext: store.ForkResumed,
	}
	must(e.t, e.st.CreateAssistSession(ctx(), a))
	return a
}

// flow_run lists the agent conversations of the run (a propose and execute phase are one), the chats forked from them
// and the agent conversations of continuation runs started from it, and nothing of other runs.
func TestConversationsFlowRunFilterAndInboxAgentChats(t *testing.T) {
	e := newEnv(t)
	t0 := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	agentNode := func(n *store.NodeRun) { n.NodeType = store.NodeAgent }

	run := e.flowRun("demo", "", store.KindLive, store.FlowRunSucceeded)
	nrA := e.nodeRun(run.ID, "review", store.NodeDone, agentNode)
	propose := e.convRun(nrA.ID, "", store.AgentSucceeded, "plan", "plan: rebase", t0)
	execute := e.convRun(nrA.ID, propose.ID, store.AgentSucceeded, "go", "done", t0.Add(time.Minute))
	nrB := e.nodeRun(run.ID, "triage", store.NodeDone, agentNode)
	second := e.convRun(nrB.ID, "", store.AgentSucceeded, "triage", "ok", t0.Add(2*time.Minute))
	fork := e.forkChat(execute.ID, t0.Add(3*time.Minute))

	cont := e.continuationRun(run, fork.ID)
	nrC := e.nodeRun(cont.ID, "after", store.NodeDone, agentNode)
	contAgent := e.convRun(nrC.ID, "", store.AgentSucceeded, "follow up", "ok", t0.Add(4*time.Minute))

	// Unrelated: another run with an agent and a fork of it, and a run without agents.
	other := e.flowRun("demo", "", store.KindLive, store.FlowRunSucceeded)
	nrO := e.nodeRun(other.ID, "review", store.NodeDone, agentNode)
	otherAgent := e.convRun(nrO.ID, "", store.AgentSucceeded, "other", "ok", t0.Add(5*time.Minute))
	e.forkChat(otherAgent.ID, t0.Add(6*time.Minute))
	plain := e.flowRun("demo", "", store.KindLive, store.FlowRunSucceeded)
	nrP := e.nodeRun(plain.ID, "notify", store.NodeDone, nil)

	got := convIDs(e.convList("?flow_run=" + run.ID))
	sameSet(t, "flow_run of the original run", got, execute.ID, second.ID, fork.ID, contAgent.ID)
	sameSet(t, "flow_run of the continuation run", convIDs(e.convList("?flow_run="+cont.ID)), contAgent.ID)
	sameSet(t, "flow_run without agents", convIDs(e.convList("?flow_run="+plain.ID)))

	byID := map[string]convItem{}
	for _, c := range e.convList("?flow_run=" + run.ID) {
		byID[c.ID] = c
	}
	if c := byID[contAgent.ID]; c.FlowRunID == nil || *c.FlowRunID != cont.ID || c.ForkedFrom != nil {
		t.Errorf("continuation conversation = %+v, want its own run id and no fork link", c)
	}
	if c := byID[fork.ID]; c.FlowRunID == nil || *c.FlowRunID != run.ID || c.ForkedFrom == nil {
		t.Errorf("fork conversation = %+v, want the original run id and a fork link", c)
	}

	note := func(source store.SourceType, nodeRunID string) inboxItemView {
		it := &store.InboxItem{SourceType: source, NodeRunID: nodeRunID, Title: "note"}
		if source == store.SourceMCP {
			it.NodeRunID, it.Client = "", "some-client"
		}
		must(t, e.st.CreateInboxItem(ctx(), it))
		var v inboxItemView
		e.get("/api/inbox/" + it.ID).want(200).decode(&v)
		return v
	}
	if v := note(store.SourceNodeRun, nrA.ID); v.AgentChats != 4 {
		t.Errorf("agent_chats of a note of the run = %d, want 4 (two agents, a fork, a continuation agent)", v.AgentChats)
	}
	if v := note(store.SourceNodeRun, nrC.ID); v.AgentChats != 1 {
		t.Errorf("agent_chats of a note of the continuation run = %d, want 1", v.AgentChats)
	}
	if v := note(store.SourceNodeRun, nrP.ID); v.AgentChats != 0 {
		t.Errorf("agent_chats of a note of a run without agents = %d, want 0", v.AgentChats)
	}
	if v := note(store.SourceMCP, ""); v.AgentChats != 0 {
		t.Errorf("agent_chats of an MCP note = %d, want 0", v.AgentChats)
	}
}

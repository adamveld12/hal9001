package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"hal9001/internal/agent"
	"hal9001/internal/store"
	"hal9001/internal/tools"
)

// ToolHub is the hub of hal's per-run MCP tool sessions (internal/tools). The API serves it under /api/mcp/, and
// other packages register tool providers on it (sandbox fs/shell, custom tools).
func (e *Engine) ToolHub() *tools.Hub { return e.tools }

// newToolHub builds the hub over the engine's store and approval queue.
func (e *Engine) newToolHub() (*tools.Hub, error) {
	h, err := tools.NewHub(tools.Options{
		Store: tokenStore{e.st},
		Host:  halToolHost{e},
		Clock: e.clock.Now,
		GH:    tools.GHOptions{Path: e.opt.BasePATH},
		Log:   e.log,
	})
	if err != nil {
		return nil, err
	}
	h.AddProvider(tools.NewCustomProvider(tools.CustomOptions{
		Lookup:    e.lookupCustomTool,
		BasePATH:  e.opt.BasePATH,
		StateRoot: filepath.Join(e.opt.Home, "data", "state", "_tools"),
	}))
	return h, nil
}

// lookupCustomTool resolves a custom tool of the current snapshot, so tool edits reach the next run.
func (e *Engine) lookupCustomTool(id string) (tools.CustomTool, error) {
	snap := e.snapshot()
	f := snap.ws.Tool(id)
	switch {
	case f == nil:
		return tools.CustomTool{}, errors.New("no such tool")
	case !f.Valid():
		return tools.CustomTool{}, errors.New("the tool has validation errors")
	}
	env := maps.Clone(snap.env)
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, snap.toolEnv[id])
	return tools.CustomTool{Tool: f.Value, Env: env, Redactor: snap.red}, nil
}

// tokenStore adapts the store to tools.TokenStore.
type tokenStore struct{ st *store.Store }

func (t tokenStore) CreateToken(ctx context.Context, hash, runID, kind string) error {
	return t.st.CreateMCPToken(ctx, &store.MCPToken{TokenHash: hash, AgentRunID: runID, Kind: kind})
}

func (t tokenStore) RevokeToken(ctx context.Context, hash string) error {
	return t.st.RevokeMCPToken(ctx, hash)
}

// halToolHost records hal tool calls as permission requests of the agent run and routes calls that need approval
// through the tool_call approval queue (tools.Host).
type halToolHost struct{ e *Engine }

var _ tools.Host = halToolHost{}

func (h halToolHost) permission(runID, tool string, args json.RawMessage, extra string) *store.PermissionRequest {
	red := h.e.snapshot().red
	raw, _ := json.Marshal(map[string]any{"tool": tool, "arguments": args, "note": extra})
	id := h.e.st.NewID()
	return &store.PermissionRequest{
		ID: id, AgentRunID: runID, ToolCallID: "hal:" + tool + ":" + id, ToolKind: "other", Title: "hal tool: " + tool,
		RawInput: store.Inline(red.String(string(raw))),
	}
}

// Record writes an auto-allowed call as a permission request decided by policy.
func (h halToolHost) Record(ctx context.Context, c tools.CallRecord) (string, error) {
	p := h.permission(c.RunID, c.Tool, c.Args, "")
	now := h.e.st.Now()
	p.Decision, p.DecidedBy, p.DecidedAt = store.PermAllow, store.DecidedByPolicy, &now
	p.MatchedRule, p.Reason = "hal:builtin", c.Reason
	if err := h.e.st.CreatePermissionRequest(context.WithoutCancel(ctx), p); err != nil {
		return "", err
	}
	return p.ID, nil
}

// Finish appends the outcome to the record's reason.
func (h halToolHost) Finish(ctx context.Context, id, outcome string) {
	p, err := h.e.st.GetPermissionRequest(ctx, id)
	if err != nil {
		h.e.log.Warn("engine: finish hal tool record", "permission_request", id, "err", err)
		return
	}
	p.Reason = strings.TrimSpace(p.Reason + "; " + outcome)
	p.Reason = strings.TrimPrefix(p.Reason, "; ")
	if err := h.e.st.UpdatePermissionRequest(ctx, p); err != nil {
		h.e.log.Warn("engine: finish hal tool record", "permission_request", id, "err", err)
	}
}

var errHalApprovalTimeout = errors.New("tool approval timeout")

// Approve queues the call as a tool_call approval and blocks for the decision, the timeout or ctx.
func (h halToolHost) Approve(ctx context.Context, req tools.ApprovalRequest) (tools.Decision, error) {
	e := h.e
	p := h.permission(req.RunID, req.Tool, req.Args, req.Reason)
	p.Reason, p.MatchedRule = req.Reason, "hal:approval"
	if err := e.st.CreatePermissionRequest(e.dbCtx, p); err != nil {
		return tools.Decision{}, err
	}
	decide := func(dec store.PermDecision, by store.PermDecidedBy, reason string) {
		now := e.st.Now()
		u := *p
		u.Decision, u.DecidedBy, u.DecidedAt, u.Reason = dec, by, &now, reason
		if err := e.st.UpdatePermissionRequest(e.dbCtx, &u); err != nil && !errors.Is(err, store.ErrConflict) {
			e.log.Warn("engine: decide hal tool request", "permission_request", p.ID, "err", err)
		}
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = tools.DefaultApprovalTimeout
	}
	actx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	timer := e.clock.NewTimer(timeout)
	defer timer.Stop()
	go func() {
		select {
		case <-timer.C():
			cancel(errHalApprovalTimeout)
		case <-actx.Done():
		}
	}()

	dec, err := e.broker.AwaitToolApproval(actx, agent.ToolApproval{
		RunID: req.RunID, NodeRunID: req.NodeRunID, PermissionRequestID: p.ID, ToolCallID: p.ToolCallID,
		ToolKind: p.ToolKind, Title: p.Title, RawInput: json.RawMessage(e.readContent(p.RawInput)),
		ExpiresAt: e.clock.Now().Add(timeout),
	})
	if err != nil {
		cause := context.Cause(actx)
		switch {
		case errors.Is(cause, errHalApprovalTimeout):
			decide(store.PermExpired, store.DecidedByTimeout, "no decision within the approval timeout of "+timeout.String())
			return tools.Decision{Expired: true, RecordID: p.ID}, nil
		case cause != nil:
			decide(store.PermCancelled, store.DecidedByPolicy, "call cancelled while waiting for approval: "+cancelText(actx))
			return tools.Decision{}, fmt.Errorf("cancelled while waiting for approval")
		}
		decide(store.PermReject, store.DecidedByPolicy, "approval queue error: "+err.Error())
		return tools.Decision{}, err
	}
	// DecideApproval already wrote the decision to the permission request.
	d := tools.Decision{Allowed: dec.Verdict == agent.VerdictAllow, Comment: dec.Comment, RecordID: p.ID}
	if dec.Verdict == agent.VerdictDenyCancel {
		d.Cancelled = true
		key := req.NodeRunID
		if key == "" {
			key = "assist:" + req.RunID
		}
		e.cancelOne(key, agent.ErrDeniedCancel)
	}
	return d, nil
}

// halTools is a run's hal MCP session and the server entry that hands it to the agent.
type halTools struct {
	sess   *tools.Session
	server agent.MCPServer
}

// openHalTools opens the tool session of one agent run and returns the MCP server entry for session/new. The
// caller closes sess when the run ends. info is evaluated per call.
func (e *Engine) openHalTools(ctx context.Context, spec tools.RunSpec) (*halTools, error) {
	exe := e.opt.Executable
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return nil, fmt.Errorf("locate hal9001 binary: %w", err)
		}
	}
	sess, err := e.tools.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &halTools{sess: sess, server: agent.MCPServer{
		Name: tools.ServerName, Type: "stdio", Command: exe, Args: tools.ServerArgs(e.opt.MCPURL, sess.Token()),
	}}, nil
}

// agentRunInfo is the tools.RunInfo of an agent node run.
func (e *Engine) agentRunInfo(ne *nodeEnv, agentRunID string) tools.RunInfo {
	info := tools.RunInfo{
		AgentRunID: agentRunID, NodeRunID: ne.nr.ID, FlowRunID: ne.fr.ID, FlowID: ne.fr.FlowID, NodeID: ne.node.ID,
		RunKind: string(ne.fr.Kind), DryRun: ne.fr.DryRun, EventID: ne.fr.EventID,
		Input: json.RawMessage(ne.input), Event: json.RawMessage(ne.event),
	}
	if ne.fr.EventID != "" {
		if ev, err := e.st.GetEvent(e.dbCtx, ne.fr.EventID); err == nil {
			info.SensorID = ev.SensorID
			if ev.SensorRunID != "" {
				if sr, err := e.st.GetSensorRun(e.dbCtx, ev.SensorRunID); err == nil {
					info.PolledAt = sr.StartedAt
				}
			}
		}
	}
	return info
}

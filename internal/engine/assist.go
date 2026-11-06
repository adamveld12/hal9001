package engine

import (
	"context"
	"maps"
	"sync"

	"hal9001/internal/agent"
	"hal9001/internal/assist"
	"hal9001/internal/core"
	"hal9001/internal/runner"
	"hal9001/internal/tools"
)

// ToolApprovalObserver is told when a gated tool call of an agent run starts
// waiting for a decision and when it stops waiting (decided, expired or the run
// ended; allowed tells whether the user allowed the call). The assist manager uses it to link approvals to its conversation.
type ToolApprovalObserver interface {
	ToolApprovalPending(runID, toolCallID, approvalID string)
	ToolApprovalResolved(runID, approvalID string, allowed bool)
}

type observerSlot struct {
	mu sync.RWMutex
	o  ToolApprovalObserver
}

// SetToolApprovalObserver installs the observer (nil removes it).
func (e *Engine) SetToolApprovalObserver(o ToolApprovalObserver) {
	e.observer.mu.Lock()
	defer e.observer.mu.Unlock()
	e.observer.o = o
}

func (e *Engine) toolApprovalObserver() ToolApprovalObserver {
	e.observer.mu.RLock()
	defer e.observer.mu.RUnlock()
	return e.observer.o
}

func (e *Engine) toolApprovalPending(runID, toolCallID, approvalID string) {
	if o := e.toolApprovalObserver(); o != nil {
		o.ToolApprovalPending(runID, toolCallID, approvalID)
	}
}

func (e *Engine) toolApprovalResolved(runID, approvalID string, allowed bool) {
	if o := e.toolApprovalObserver(); o != nil {
		o.ToolApprovalResolved(runID, approvalID, allowed)
	}
}

// AssistHost is the engine as the assist manager uses it: runtime resolution,
// the env values and redactor, the approval queue, the kill switch and the
// engine's lifetime.
func (e *Engine) AssistHost() assist.Host { return assistHost{e} }

type assistHost struct{ e *Engine }

var _ assist.Host = assistHost{}

func (h assistHost) Context() context.Context { return h.e.ctx }

func (h assistHost) Spawn(fn func(ctx context.Context)) bool { return h.e.spawn(fn) }

func (h assistHost) Track(id string, cancel context.CancelCauseFunc) func() {
	return h.e.track(id, cancel)
}

func (h assistHost) Paused() bool { return h.e.isPaused() }

func (h assistHost) Runtime(name string) (agent.RuntimeConfig, bool) {
	return h.e.runtimeConfig(h.e.snapshot(), name)
}

func (h assistHost) Env(sensorID string) (global, sensor map[string]string, red *runner.Redactor) {
	snap := h.e.snapshot()
	return maps.Clone(snap.env), maps.Clone(snap.sensorEnv[sensorID]), snap.red
}

func (h assistHost) ToolEnv(toolID string) (global, tool map[string]string, red *runner.Redactor) {
	snap := h.e.snapshot()
	return maps.Clone(snap.env), maps.Clone(snap.toolEnv[toolID]), snap.red
}

func (h assistHost) BasePATH() string { return h.e.opt.BasePATH }

func (h assistHost) Recorder(assistSessionID, model string) agent.Recorder {
	return &runRecorder{e: h.e, redFn: func() *runner.Redactor { return h.e.snapshot().red }, model: model, assist: assistSessionID}
}

func (h assistHost) Broker() agent.ApprovalBroker { return h.e.broker }

func (h assistHost) Tools() *tools.Hub { return h.e.tools }

func (h assistHost) Clock() agent.Clock { return h.e.clock }

func (h assistHost) TestFlow(ctx context.Context, req core.FlowTestRequest) (core.AssistFlowTestResult, error) {
	return h.e.TestFlow(ctx, req)
}

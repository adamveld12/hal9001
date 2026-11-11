package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// Replay runs a stored event through the current version of a flow (the
// flow need not be enabled or subscribed to the event's sensor). The new
// kind=replay run is linked to the event's most recent earlier run in that
// flow. A flow configured dry_run stays dry. It is accepted while paused; the
// run stays queued until the kill switch is released.
func (e *Engine) Replay(ctx context.Context, eventID, flowID string, dryRun bool) (*store.FlowRun, error) {
	ev, err := e.st.GetEvent(ctx, eventID)
	if err != nil {
		return nil, err
	}
	return e.replayRun(ctx, e.snapshot(), ev, flowID, dryRun)
}

// replayRun queues a kind=replay run of ev through the version of flowID in
// snap, linked to the event's most recent earlier run in that flow.
func (e *Engine) replayRun(ctx context.Context, snap *snapshot, ev *store.Event, flowID string, dryRun bool) (*store.FlowRun, error) {
	fd := snap.flows[flowID]
	if fd == nil {
		return nil, fmt.Errorf("%w: flow %q", store.ErrNotFound, flowID)
	}
	prior, err := e.st.ListFlowRuns(ctx, store.FlowRunFilter{
		EventID: ev.ID, FlowID: flowID, Kinds: []store.FlowRunKind{store.KindLive, store.KindReplay}, ListOpts: store.ListOpts{Limit: 1},
	})
	if err != nil {
		return nil, err
	}
	fr := &store.FlowRun{
		FlowID: flowID, FlowHash: fd.hash, FlowYAML: string(fd.raw), EventID: ev.ID,
		Kind: store.KindReplay, DryRun: dryRun || fd.flow.DryRun,
	}
	if len(prior.Items) > 0 {
		fr.ReplayOf = prior.Items[0].ID
	}
	if err := e.st.CreateFlowRun(ctx, fr); err != nil {
		return nil, err
	}
	e.wake()
	return fr, nil
}

// TestNode runs one node on the given input as a kind=test flow run ("Run
// node on sample"): the node does not feed downstream nodes, and the run is
// hidden from default timelines. It waits until the node run has finished (or
// parked on an approval); when ctx expires first it returns the unfinished
// records without an error. req.Flow, when set, is an unsaved editor copy: it
// is validated and its YAML is what the run is recorded against.
func (e *Engine) TestNode(ctx context.Context, req core.TestNodeRequest) (*store.FlowRun, *store.NodeRun, error) {
	if e.isPaused() {
		return nil, nil, fmt.Errorf("%w: node tests are rejected while the kill switch is engaged", core.ErrPaused)
	}
	snap := e.snapshot()
	flowID := req.FlowID
	var flow *spec.Flow
	var raw []byte
	if req.Flow != nil {
		f := req.Flow.Clone()
		f.ApplyDefaults()
		if flowID == "" {
			flowID = f.ID
		}
		if ds := spec.ValidateFlow(&f, snap.ws.Refs()); spec.HasErrors(ds) {
			return nil, nil, &core.ValidationError{Diagnostics: ds}
		}
		var err error
		if raw, err = spec.MarshalFlow(f); err != nil {
			return nil, nil, fmt.Errorf("engine: marshal flow: %w", err)
		}
		flow = &f
	} else {
		fd := snap.flows[flowID]
		if fd == nil {
			return nil, nil, fmt.Errorf("%w: flow %q", store.ErrNotFound, flowID)
		}
		flow, raw = fd.flow, fd.raw
	}
	node := flow.NodeByID(req.NodeID)
	if node == nil {
		return nil, nil, fmt.Errorf("%w: node %q in flow %q", core.ErrUnknownRef, req.NodeID, flowID)
	}
	if req.SimulateEmit != nil {
		if err := validateSimulatedEmits(flow, map[string]core.SimulatedEmit{node.ID: *req.SimulateEmit}); err != nil {
			return nil, nil, err
		}
	}
	input := []byte(strings.TrimSpace(string(req.Input)))
	if req.EventID != "" {
		ev, err := e.st.GetEvent(ctx, req.EventID)
		if err != nil {
			return nil, nil, err
		}
		if len(input) == 0 {
			if input, err = e.eventPayload(ev); err != nil {
				return nil, nil, err
			}
		}
	}
	if len(input) == 0 {
		return nil, nil, fmt.Errorf("%w: input is required (pass input or an event id)", store.ErrInvalid)
	}

	now := time.Now().UTC()
	fr := &store.FlowRun{
		FlowID: flowID, FlowHash: spec.Hash(raw), FlowYAML: string(raw), EventID: req.EventID,
		Kind: store.KindTest, DryRun: req.DryRun || flow.DryRun, Status: store.FlowRunRunning, StartedAt: &now,
	}
	if err := e.st.CreateFlowRun(ctx, fr); err != nil {
		return nil, nil, err
	}
	if node.Type == spec.NodeApproval || node.RequiresApproval {
		e.simulations.Store(fr.ID, simulatedDecision(req.SimulateApproval))
	}
	if req.SimulateEmit != nil {
		e.simEmits.Store(fr.ID, map[string]core.SimulatedEmit{node.ID: *req.SimulateEmit})
	}
	nr := &store.NodeRun{FlowRunID: fr.ID, NodeID: node.ID, NodeType: nodeRunType(node), Stdin: store.Inline(string(input))}
	if err := e.st.CreateNodeRun(ctx, nr); err != nil {
		return nil, nil, err
	}
	e.wake()
	return e.waitTest(ctx, fr.ID)
}

// waitTest waits for a test run to finish and returns it with its node run.
func (e *Engine) waitTest(ctx context.Context, flowRunID string) (*store.FlowRun, *store.NodeRun, error) {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		fr, err := e.st.GetFlowRun(e.dbCtx, flowRunID)
		if err != nil {
			return nil, nil, err
		}
		if !fr.Status.Terminal() && fr.Status != store.FlowRunAwaitingApproval {
			select {
			case <-tick.C:
				continue
			case <-ctx.Done():
				if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return nil, nil, ctx.Err()
				}
			}
		}
		nodes, err := e.nodeRuns(flowRunID)
		if err != nil {
			return nil, nil, err
		}
		if len(nodes) == 0 {
			return nil, nil, fmt.Errorf("engine: test run %s has no node run", flowRunID)
		}
		full, err := e.st.GetNodeRun(e.dbCtx, nodes[0].ID)
		return fr, full, err
	}
}

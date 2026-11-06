package assist

import (
	"context"
	"encoding/json"
	"fmt"

	"hal9001/internal/core"
	"hal9001/internal/spec"
)

// testFlow runs the flow draft once on one sample input through the engine (see core.FlowTestRequest). Command
// nodes run with the global env file, so who asks matters as it does for sensors: the user's test always runs and
// marks the command nodes approved; the agent's runs only while they are what the user approved (a flow without
// command nodes needs no approval).
func (m *Manager) testFlow(ctx context.Context, s *session, byAgent bool, req core.AssistTestRequest) (core.AssistFlowTestResult, error) {
	var zero core.AssistFlowTestResult
	n := 0
	if req.SampleIndex != nil {
		n++
	}
	if req.EventID != "" {
		n++
	}
	if len(req.Input) > 0 {
		n++
	}
	if n > 1 {
		return zero, invalid("", "give at most one of sample_index, event_id and input")
	}

	s.mu.Lock()
	if err := s.usableLocked(); err != nil {
		s.mu.Unlock()
		return zero, err
	}
	flow, _, ds, _ := m.checkFlow(s.row.DraftYAML, s.row.TargetID, s.dir)
	if flow == nil || spec.HasErrors(ds) {
		s.mu.Unlock()
		return zero, &core.ValidationError{Diagnostics: ds}
	}
	input, eventID, err := m.testInput(s.dir, req)
	if err != nil {
		s.mu.Unlock()
		return zero, err
	}
	available := m.availableEnv("")
	id := scriptOfFlow(flow, available)
	if byAgent {
		if approved, _ := s.scriptApprovedLocked(id, available); !approved {
			s.mu.Unlock()
			return zero, fmt.Errorf("%w: the draft's command nodes changed since the user last tested or approved them; "+
				"running them needs the user's approval (the permission request for `hal9001 assist test`)", core.ErrInvalidState)
		}
	} else if !id.empty() {
		s.markApprovedLocked(id)
	}
	s.mu.Unlock()

	return m.host.TestFlow(ctx, core.FlowTestRequest{Flow: flow, Input: input, EventID: eventID})
}

// testInput picks the input of a flow test: the given payload, a stored event, a sample, or the first sample.
func (m *Manager) testInput(dir string, req core.AssistTestRequest) (input json.RawMessage, eventID string, err error) {
	switch {
	case len(req.Input) > 0:
		if !json.Valid(req.Input) {
			return nil, "", invalid("input", "input must be one JSON value")
		}
		return req.Input, "", nil
	case req.EventID != "":
		return nil, req.EventID, nil
	}
	samples := sampleList(dir)
	i := 0
	if req.SampleIndex != nil {
		i = *req.SampleIndex
	}
	if i < 0 || i >= len(samples) {
		if req.SampleIndex == nil {
			return nil, "", invalid("input", "there is no sample input: pass input or event_id (the known sensors have no recent events)")
		}
		return nil, "", invalid("sample_index", fmt.Sprintf("sample_index %d is out of range (%d samples)", i, len(samples)))
	}
	return samples[i].Payload, "", nil
}

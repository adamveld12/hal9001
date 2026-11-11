package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"hal9001/internal/outschema"
)

func openWithOutput(t *testing.T, h *Hub, sink *OutputSink) *Session {
	t.Helper()
	s, err := h.Open(context.Background(), RunSpec{
		RunID: "run1", ApprovalTimeout: time.Minute, Output: sink,
		Info: func() RunInfo { return RunInfo{AgentRunID: "run1", NodeRunID: "nr1"} },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func hasTool(s *Session, name string) bool {
	for _, ti := range s.Tools() {
		if ti.Name == name {
			return true
		}
	}
	return false
}

func reviewSchema(t *testing.T) *outschema.Schema {
	t.Helper()
	s, err := outschema.Compile(map[string]any{
		"type": "object", "required": []any{"priority", "body"},
		"properties": map[string]any{
			"priority": map[string]any{"enum": []any{"urgent", "normal"}},
			"body":     map[string]any{"type": "string"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// submit_output exists only in the session of a run that has an output sink, with the node's schema as its input
// schema; every other session (other runs, nodes with output none) does not have it.
func TestSubmitOutputOnlyInSessionsWithASink(t *testing.T) {
	h, _ := newTestHub(t, &fakeHost{}, t.TempDir())
	schema := reviewSchema(t)
	with := openWithOutput(t, h, NewOutputSink(schema, true))
	without := openSession(t, h)
	other := openWithOutput(t, h, nil)

	if !hasTool(with, OutputToolName) {
		t.Fatal("the run with an output sink has no submit_output")
	}
	for name, s := range map[string]*Session{"plain run": without, "nil sink": other} {
		if hasTool(s, OutputToolName) {
			t.Errorf("%s exposes submit_output", name)
		}
		if _, err := s.Call(context.Background(), OutputToolName, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "unknown tool") {
			t.Errorf("%s: calling submit_output = %v, want unknown tool", name, err)
		}
	}
	for _, ti := range with.Tools() {
		if ti.Name == OutputToolName {
			props, _ := ti.InputSchema["properties"].(map[string]any)
			if _, ok := props["priority"]; !ok || ti.InputSchema["type"] != "object" {
				t.Errorf("input schema is not the node's output schema: %v", ti.InputSchema)
			}
		}
	}
}

// An invalid call is refused with the path and reason and leaves nothing behind; a valid call is kept; a later valid
// call replaces it; a later invalid call keeps the earlier valid result.
func TestSubmitOutputValidatesAndLastValidWins(t *testing.T) {
	host := &fakeHost{}
	h, _ := newTestHub(t, host, t.TempDir())
	sink := NewOutputSink(reviewSchema(t), true)
	s := openWithOutput(t, h, sink)
	ctx := context.Background()

	_, err := s.Call(ctx, OutputToolName, json.RawMessage(`{"priority":"meh"}`))
	if err == nil || !strings.Contains(err.Error(), "/priority:") || !strings.Contains(err.Error(), "missing propert") {
		t.Fatalf("invalid call error = %v", err)
	}
	if sink.Value() != nil || !strings.Contains(sink.LastError(), "/priority") {
		t.Fatalf("value %s last error %q", sink.Value(), sink.LastError())
	}
	if _, err := s.Call(ctx, OutputToolName, json.RawMessage(`{ "priority": "urgent", "body": "first" }`)); err != nil {
		t.Fatal(err)
	}
	if string(sink.Value()) != `{"priority":"urgent","body":"first"}` || sink.LastError() != "" {
		t.Fatalf("value %s last error %q", sink.Value(), sink.LastError())
	}
	if _, err := s.Call(ctx, OutputToolName, json.RawMessage(`{"priority":"urgent"}`)); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	if string(sink.Value()) != `{"priority":"urgent","body":"first"}` {
		t.Errorf("an invalid call must not discard the earlier result: %s", sink.Value())
	}
	if _, err := s.Call(ctx, OutputToolName, json.RawMessage(`{"priority":"normal","body":"second"}`)); err != nil {
		t.Fatal(err)
	}
	if string(sink.Value()) != `{"priority":"normal","body":"second"}` {
		t.Errorf("last valid call must win: %s", sink.Value())
	}
	if len(host.approvals) != 0 {
		t.Errorf("submit_output must not ask for approval: %v", host.approvals)
	}
}

// A read-only (or dry-run) run can still submit its result: the tool changes nothing.
func TestSubmitOutputWorksInReadOnlyRuns(t *testing.T) {
	h, _ := newTestHub(t, &fakeHost{}, t.TempDir())
	sink := NewOutputSink(outschema.Note(), false)
	s, err := h.Open(context.Background(), RunSpec{
		RunID: "run1", ReadOnly: true, Output: sink,
		Info: func() RunInfo { return RunInfo{AgentRunID: "run1", NodeRunID: "nr1", DryRun: true} },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err := s.Call(context.Background(), OutputToolName, json.RawMessage(`{"title":"t","summary_md":"s"}`)); err != nil {
		t.Fatal(err)
	}
	if sink.Value() == nil {
		t.Fatal("no value")
	}
}

func TestPreambleSaysHowTheResultIsReturned(t *testing.T) {
	req := Preamble(PreambleData{Output: NewOutputSink(outschema.Note(), true)})
	pref := Preamble(PreambleData{Output: NewOutputSink(outschema.Note(), false)})
	none := Preamble(PreambleData{})
	if !strings.Contains(req, "submit_output") || !strings.Contains(req, "not used as the result") {
		t.Errorf("required preamble:\n%s", req)
	}
	if !strings.Contains(pref, "(preferred)") || !strings.Contains(pref, "still accepted") {
		t.Errorf("preferred preamble:\n%s", pref)
	}
	if strings.Contains(none, "submit_output") {
		t.Errorf("a run without a sink is told about submit_output:\n%s", none)
	}
}

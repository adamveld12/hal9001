package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"hal9001/internal/spec"
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

var reviewedSchema = spec.JSONSchema{
	"type": "object", "required": []any{"priority", "body"},
	"properties": map[string]any{
		"priority": map[string]any{"enum": []any{"urgent", "normal"}},
		"body":     map[string]any{"type": "string"},
	},
}

func portSink(t *testing.T, many bool) *OutputSink {
	t.Helper()
	s, err := NewPortSink([]spec.EmitTool{
		{Port: "reviewed", Name: "emit_reviewed", Description: "Reviewed.", LeadsTo: `inbox note "Review: ..."`, Schema: reviewedSchema},
		{Port: "needs_human", Name: "emit_needs_human", Description: "Needs a person.", LeadsTo: "command node", Schema: reviewedSchema},
	}, many)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// emit_<port> exists only in the session of a run that has a port sink, with the port schema as its input schema
// and the Leads-to summary in its description; every other session (other runs, nodes without ports) has none.
func TestEmitToolsOnlyInSessionsWithASink(t *testing.T) {
	h, _ := newTestHub(t, &fakeHost{}, t.TempDir())
	with := openWithOutput(t, h, portSink(t, false))
	without := openSession(t, h)
	other := openWithOutput(t, h, NewNoteSink())

	for _, name := range []string{"emit_reviewed", "emit_needs_human"} {
		if !hasTool(with, name) {
			t.Errorf("the run with a port sink has no %s", name)
		}
	}
	for label, s := range map[string]*Session{"plain run": without, "note sink": other} {
		for _, name := range []string{"emit_reviewed", "emit_needs_human"} {
			if hasTool(s, name) {
				t.Errorf("%s exposes %s", label, name)
			}
			if _, err := s.Call(context.Background(), name, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "unknown tool") {
				t.Errorf("%s: calling %s = %v, want unknown tool", label, name, err)
			}
		}
	}
	if hasTool(with, OutputToolName) {
		t.Error("a port sink also exposes submit_output")
	}
	for _, ti := range with.Tools() {
		if ti.Name == "emit_reviewed" {
			props, _ := ti.InputSchema["properties"].(map[string]any)
			if _, ok := props["priority"]; !ok || ti.InputSchema["type"] != "object" {
				t.Errorf("input schema is not the port schema: %v", ti.InputSchema)
			}
			if want := `Reviewed. Leads to: inbox note "Review: ...".`; !strings.Contains(ti.Description, want) {
				t.Errorf("description %q lacks %q", ti.Description, want)
			}
		}
	}
}

// An invalid call is refused with the path and reason and leaves nothing behind; with emit: one a valid call is kept
// and a later valid call (even on another port) replaces it; a later invalid call keeps the earlier valid result.
func TestEmitValidatesAndLastValidWinsForOne(t *testing.T) {
	host := &fakeHost{}
	h, _ := newTestHub(t, host, t.TempDir())
	sink := portSink(t, false)
	s := openWithOutput(t, h, sink)
	ctx := context.Background()

	_, err := s.Call(ctx, "emit_reviewed", json.RawMessage(`{"priority":"meh"}`))
	if err == nil || !strings.Contains(err.Error(), "/priority:") || !strings.Contains(err.Error(), "missing propert") {
		t.Fatalf("invalid call error = %v", err)
	}
	if sink.Done() || !strings.Contains(sink.LastError(), "/priority") {
		t.Fatalf("done %v last error %q", sink.Done(), sink.LastError())
	}
	res, err := s.Call(ctx, "emit_reviewed", json.RawMessage(`{ "priority": "urgent", "body": "first" }`))
	if err != nil || res.Text != "ok" {
		t.Fatalf("valid call = %v, %v", res, err)
	}
	if sink.LastError() != "" {
		t.Errorf("last error %q after a valid call", sink.LastError())
	}
	if _, err := s.Call(ctx, "emit_reviewed", json.RawMessage(`{"priority":"urgent"}`)); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	if got := sink.Emissions(); len(got) != 1 || got[0].Port != "reviewed" || string(got[0].Output) != `{"priority":"urgent","body":"first"}` {
		t.Errorf("an invalid call must not discard the earlier result: %+v", got)
	}
	if _, err := s.Call(ctx, "emit_needs_human", json.RawMessage(`{"priority":"normal","body":"second"}`)); err != nil {
		t.Fatal(err)
	}
	if got := sink.Emissions(); len(got) != 1 || got[0].Port != "needs_human" || string(got[0].Output) != `{"priority":"normal","body":"second"}` {
		t.Errorf("last valid call must win, on its port: %+v", got)
	}
	if len(host.approvals) != 0 {
		t.Errorf("emit tools must not ask for approval: %v", host.approvals)
	}
}

// emit: many keeps every valid call as its own emission, in call order.
func TestEmitManyKeepsEveryValidCall(t *testing.T) {
	h, _ := newTestHub(t, &fakeHost{}, t.TempDir())
	sink := portSink(t, true)
	s := openWithOutput(t, h, sink)
	ctx := context.Background()
	for _, c := range []struct{ tool, args string }{
		{"emit_reviewed", `{"priority":"urgent","body":"a"}`},
		{"emit_reviewed", `{"priority":"bad"}`},
		{"emit_needs_human", `{"priority":"normal","body":"b"}`},
		{"emit_reviewed", `{"priority":"normal","body":"c"}`},
	} {
		_, _ = s.Call(ctx, c.tool, json.RawMessage(c.args))
	}
	var got []string
	for _, e := range sink.Emissions() {
		got = append(got, e.Port+":"+string(e.Output))
	}
	want := `reviewed:{"priority":"urgent","body":"a"} needs_human:{"priority":"normal","body":"b"} reviewed:{"priority":"normal","body":"c"}`
	if strings.Join(got, " ") != want {
		t.Errorf("emissions %v, want %s", got, want)
	}
}

// A read-only (or dry-run) run can still emit: the tools change nothing.
func TestEmitWorksInReadOnlyRuns(t *testing.T) {
	h, _ := newTestHub(t, &fakeHost{}, t.TempDir())
	sink := portSink(t, false)
	s, err := h.Open(context.Background(), RunSpec{
		RunID: "run1", ReadOnly: true, Output: sink,
		Info: func() RunInfo { return RunInfo{AgentRunID: "run1", NodeRunID: "nr1", DryRun: true} },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err := s.Call(context.Background(), "emit_reviewed", json.RawMessage(`{"priority":"urgent","body":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if !sink.Done() {
		t.Fatal("no emission")
	}
}

// submit_output (output: note): the note schema, last valid call wins, the call is optional.
func TestSubmitOutputNoteSink(t *testing.T) {
	h, _ := newTestHub(t, &fakeHost{}, t.TempDir())
	sink := NewNoteSink()
	s := openWithOutput(t, h, sink)
	ctx := context.Background()
	if sink.Required() {
		t.Error("submit_output of output: note must not be required")
	}
	if _, err := s.Call(ctx, OutputToolName, json.RawMessage(`{"summary_md":"s"}`)); err == nil || !strings.Contains(err.Error(), "title") {
		t.Fatalf("a note without title: %v", err)
	}
	if _, err := s.Call(ctx, OutputToolName, json.RawMessage(`{"title":"t","summary_md":"s"}`)); err != nil {
		t.Fatal(err)
	}
	if string(sink.Value()) != `{"title":"t","summary_md":"s"}` {
		t.Errorf("value %s", sink.Value())
	}
}

func TestPreambleSaysHowTheResultIsReturned(t *testing.T) {
	ports := Preamble(PreambleData{Output: portSink(t, false)})
	many := Preamble(PreambleData{Output: portSink(t, true)})
	note := Preamble(PreambleData{Output: NewNoteSink()})
	none := Preamble(PreambleData{})
	if !strings.Contains(ports, "emit_reviewed, emit_needs_human") || !strings.Contains(ports, "not used as the result") {
		t.Errorf("ports preamble:\n%s", ports)
	}
	if !strings.Contains(many, "one or more of emit_reviewed") {
		t.Errorf("many preamble:\n%s", many)
	}
	if !strings.Contains(note, "(preferred)") || !strings.Contains(note, "still accepted") {
		t.Errorf("note preamble:\n%s", note)
	}
	if strings.Contains(none, "submit_output") || strings.Contains(none, "emit_") {
		t.Errorf("a run without a sink is told about output tools:\n%s", none)
	}
}

func TestPortSinkFollowUpAndResumeNote(t *testing.T) {
	s := portSink(t, false)
	if got := s.FollowUpPrompt(); got != "You must finish by calling one of: emit_reviewed, emit_needs_human." {
		t.Errorf("follow-up = %q", got)
	}
	if !strings.HasPrefix(s.ResumeNote(), "## Outputs\nFinish by calling exactly one of these tools:") || !s.Required() {
		t.Errorf("resume note %q required %v", s.ResumeNote(), s.Required())
	}
}

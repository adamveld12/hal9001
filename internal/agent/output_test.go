package agent_test

import (
	"encoding/json"
	"strings"
	"testing"

	"hal9001/internal/agent"
	"hal9001/internal/agent/fakeagent"
)

// outputHook is a hook whose submission appears once the agent has been prompted `after` times: the fake agent
// cannot call hal's tools here, so the number of session/prompt calls stands in for "the agent called the tool".
func outputHook(e *env, after int, required bool, value string) *agent.OutputHook {
	return &agent.OutputHook{
		Required: required, FollowUp: "You must call submit_output now.",
		Done: func() bool { return value != "" && len(e.callsOf("session/prompt")) >= after },
		Value: func() json.RawMessage {
			if value != "" && len(e.callsOf("session/prompt")) >= after {
				return json.RawMessage(value)
			}
			return nil
		},
		Emissions: func() []agent.Emission {
			if value != "" && len(e.callsOf("session/prompt")) >= after {
				return []agent.Emission{{Port: "reviewed", Output: json.RawMessage(value)}, {Port: "needs_human", Output: json.RawMessage(value)}}
			}
			return nil
		},
		LastError: func() string { return "/priority: value must be one of 'low'" },
	}
}

func finalOnly() fakeagent.Script {
	return fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{msg("done")}}}}
}

func promptTexts(e *env) []string {
	var out []string
	for _, c := range e.callsOf("session/prompt") {
		out = append(out, c["text"].(string))
	}
	return out
}

// A turn that ends without a valid submission gets exactly one reminder in the same session; a valid submission after
// it makes the run succeed with the output and the emissions recorded.
func TestRequiredOutputIsRequestedOnce(t *testing.T) {
	e := newEnv(t, finalOnly())
	req := e.req(agent.ModeReadonly, "")
	req.Output = outputHook(e, 2, true, `{"verdict":"ok"}`)
	rec := e.run(req)
	if rec.Status != agent.StatusSucceeded || string(rec.Output) != `{"verdict":"ok"}` || len(rec.Emissions) != 2 || rec.Emissions[1].Port != "needs_human" {
		t.Fatalf("status %s output %s error %q", rec.Status, rec.Output, rec.Error)
	}
	texts := promptTexts(e)
	if len(texts) != 2 || !strings.Contains(texts[1], "You must call submit_output now.") {
		t.Errorf("prompts: %q", texts)
	}
	if sessions := e.callsOf("session/new"); len(sessions) != 1 {
		t.Errorf("the reminder must reuse the session, got %d session/new", len(sessions))
	}
}

// Without a valid submission after the reminder the run fails with no_output, naming the last refused call.
func TestRequiredOutputMissingFailsNoOutput(t *testing.T) {
	e := newEnv(t, finalOnly())
	req := e.req(agent.ModeReadonly, "")
	req.Output = outputHook(e, 99, true, `{"never":true}`)
	rec := e.run(req)
	if rec.Status != agent.StatusFailed || rec.FailureReason != agent.ReasonNoOutput || !strings.Contains(rec.Error, "/priority: value must be one of") {
		t.Fatalf("status %s reason %q error %q", rec.Status, rec.FailureReason, rec.Error)
	}
	if n := len(promptTexts(e)); n != 2 {
		t.Errorf("prompts = %d, want the original and one reminder", n)
	}
}

// A submission on the first turn needs no reminder.
func TestRequiredOutputSubmittedFirstTime(t *testing.T) {
	e := newEnv(t, finalOnly())
	req := e.req(agent.ModeReadonly, "")
	req.Output = outputHook(e, 1, true, `{"a":1}`)
	rec := e.run(req)
	if rec.Status != agent.StatusSucceeded || len(promptTexts(e)) != 1 {
		t.Fatalf("status %s prompts %q", rec.Status, promptTexts(e))
	}
}

// A preferred (not required) submission never triggers a reminder or a failure; a valid one becomes the note and
// the final message is not parsed.
func TestOptionalOutputNeverFailsAndBecomesNote(t *testing.T) {
	e := newEnv(t, finalOnly())
	req := e.req(agent.ModeReadonly, "")
	req.ExpectNote = true
	h := outputHook(e, 1, false, "")
	req.Output = h
	rec := e.run(req)
	if rec.Status != agent.StatusSucceeded || len(promptTexts(e)) != 1 || rec.Output != nil || rec.NoteParseError == "" {
		t.Fatalf("no submission: status %s prompts %q output %s parse error %q", rec.Status, promptTexts(e), rec.Output, rec.NoteParseError)
	}

	e = newEnv(t, finalOnly()) // final message "done" is not a note: only the submission can be the note
	req = e.req(agent.ModeReadonly, "")
	req.ExpectNote = true
	h = outputHook(e, 1, false, `{"title":"T","summary_md":"S"}`)
	h.AsNote = true
	req.Output = h
	rec = e.run(req)
	if rec.Note == nil || rec.Note.Title != "T" || rec.Note.Priority != "normal" || rec.NoteParseError != "" {
		t.Fatalf("note %+v parse error %q", rec.Note, rec.NoteParseError)
	}
}

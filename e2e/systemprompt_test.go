package e2e

import (
	"strings"
	"testing"
)

const sysPromptHeader = "## User context (from Settings → Context)"

// The system prompt round-trips through /api/settings, is capped with a 422, and leads the prompt of a flow agent
// node (before the hal context); with it cleared the prompt has no user-context section.
func TestSystemPromptReachesFlowAgentNode(t *testing.T) {
	d, _ := halToolDaemon(t, "")

	var got struct {
		Config struct {
			SystemPrompt string `json:"system_prompt"`
		} `json:"config"`
	}
	d.Patch("/api/settings", map[string]any{"system_prompt": "My name is Ada.\n\n- prefer metric units"}).Want(200).JSON(&got)
	if got.Config.SystemPrompt != "My name is Ada.\n\n- prefer metric units" {
		t.Fatalf("patch response system_prompt = %q", got.Config.SystemPrompt)
	}
	d.Get("/api/settings").Want(200).JSON(&got)
	if !strings.Contains(got.Config.SystemPrompt, "My name is Ada.") {
		t.Fatalf("GET system_prompt = %q", got.Config.SystemPrompt)
	}

	runID := d.InjectRun("src", map[string]any{"id": "7"})
	ap := d.WaitApproval("tool_call")
	d.Decide(ap.ID, true, nil)
	done := d.WaitRun(runID, "succeeded")
	prompt := d.AgentRuns(d.Node(done, "work").ID)[0].RenderedPrompt
	i, j := strings.Index(prompt, sysPromptHeader), strings.Index(prompt, "<hal_context>")
	if i != 0 || j < 0 || !strings.Contains(prompt[:j], "My name is Ada.") || !strings.Contains(prompt[:j], "prefer metric units") {
		t.Errorf("system prompt does not lead the prompt:\n%s", prompt)
	}

	// Over the limit: 422 and nothing changes.
	d.Patch("/api/settings", map[string]any{"system_prompt": strings.Repeat("x", 16<<10+1)}).Want(422)
	d.Get("/api/settings").Want(200).JSON(&got)
	if !strings.Contains(got.Config.SystemPrompt, "My name is Ada.") {
		t.Errorf("rejected patch changed the prompt")
	}

	// Empty adds nothing.
	d.Patch("/api/settings", map[string]any{"system_prompt": ""}).Want(200)
	runID = d.InjectRun("src", map[string]any{"id": "8"})
	ap = d.WaitApproval("tool_call")
	d.Decide(ap.ID, true, nil)
	done = d.WaitRun(runID, "succeeded")
	for _, r := range d.AgentRuns(d.Node(done, "work").ID) {
		if strings.Contains(r.RenderedPrompt, "User context") || !strings.HasPrefix(r.RenderedPrompt, "<hal_context>") {
			t.Errorf("empty system prompt changed the prompt:\n%s", r.RenderedPrompt)
		}
	}
}

// An assist session turn carries the system prompt too, ahead of hal's own instructions.
func TestSystemPromptReachesAssistSession(t *testing.T) {
	d := newAssistDaemon(t, func(d *Daemon) {
		d.assistRuntime(false, turn{When: when("hello$"), Steps: []step{say("hi")}})
	})
	d.Patch("/api/settings", map[string]any{"system_prompt": "I live in Oslo."}).Want(200)
	s := d.assistCreate("")
	d.assistTurn(s.ID, "hello")
	runs := d.assistRuns(s.ID)
	if len(runs) != 1 {
		t.Fatalf("runs = %+v", runs)
	}
	p := runs[0].RenderedPrompt
	i, j, k := strings.Index(p, sysPromptHeader), strings.Index(p, "I live in Oslo."), strings.Index(p, "<hal_context>")
	if i != 0 || j < 0 || k < j || !strings.Contains(p, "[hal9001 sensor assistant]") {
		t.Errorf("assist prompt:\n%s", p)
	}
}

package conversation

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func textMsg(role, text string) Message { return Message{Role: role, Text: &text} }

func toolMsg(title, args, output string) Message {
	c := ToolCall{ToolCallID: title, Title: title, Kind: "execute", Status: "completed", RawInput: json.RawMessage(args)}
	if output != "" {
		part, _ := json.Marshal(map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": output}})
		c.Content = []json.RawMessage{part}
	}
	b, _ := json.Marshal(c)
	return Message{Role: "tool", ToolCall: b}
}

// run is a transcript with user and assistant text and n tool calls (each with a distinct 600-byte output).
func run(n int) []Message {
	msgs := []Message{textMsg("user", "Why does the build fail?"), textMsg("assistant", "I will look at the logs.")}
	for i := range n {
		out := fmt.Sprintf("output-%d:", i) + strings.Repeat("x", 600)
		msgs = append(msgs, toolMsg(fmt.Sprintf("call-%d", i), fmt.Sprintf(`{"command":"step %d"}`, i), out))
		msgs = append(msgs, textMsg("assistant", fmt.Sprintf("Looked at step %d.", i)))
	}
	return append(msgs, textMsg("user", "And the tests?"), textMsg("assistant", "They pass."))
}

func TestCompactKeepsEverythingThatFits(t *testing.T) {
	msgs := run(2)
	got := Compact(msgs, DefaultBudget)
	for _, want := range []string{"[user]\nWhy does the build fail?", "[assistant]\nI will look at the logs.", "[tool] call-0 (execute, completed)", `arguments: {"command":"step 1"}`, "output-1:", "[assistant]\nThey pass."} {
		if !strings.Contains(got, want) {
			t.Errorf("compact lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "omitted") || strings.Contains(got, "truncated") {
		t.Errorf("nothing needed cutting, but the result says so:\n%s", got)
	}
}

func TestCompactIsDeterministic(t *testing.T) {
	msgs := run(40)
	for _, budget := range []int{1 << 10, 8 << 10, 20 << 10, DefaultBudget} {
		a, b := Compact(msgs, budget), Compact(run(40), budget)
		if a != b {
			t.Errorf("budget %d: two runs over the same input differ", budget)
		}
	}
}

func TestCompactFitsTheBudgetByDroppingMiddleOutputsFirst(t *testing.T) {
	msgs := run(40) // about 25 KB of tool output
	got := Compact(msgs, 14<<10)
	if len(got) > 14<<10 {
		t.Fatalf("compact is %d bytes, over the budget of %d", len(got), 14<<10)
	}
	if !strings.Contains(got, "[... ") || !strings.Contains(got, " tool outputs omitted ...]") {
		t.Errorf("the elision is not marked:\n%.300s", got)
	}
	// The outputs at both ends survive; the middle ones are dropped; every call is still listed.
	for _, i := range []int{0, 1, 2, 37, 38, 39} {
		if !strings.Contains(got, fmt.Sprintf("output-%d:", i)) {
			t.Errorf("the output of call %d was dropped, the ends should keep theirs", i)
		}
	}
	if strings.Contains(got, "output-20:") {
		t.Error("the output of a middle call survived although the budget needed it dropped")
	}
	for i := range 40 {
		if !strings.Contains(got, fmt.Sprintf("[tool] call-%d ", i)) || !strings.Contains(got, fmt.Sprintf(`"command":"step %d"`, i)) {
			t.Errorf("call %d lost its title or arguments before any whole call had to go", i)
		}
	}
}

func TestCompactNeverDropsUserOrAssistantText(t *testing.T) {
	tests := []struct {
		name   string
		calls  int
		budget int
	}{
		{"fits", 3, DefaultBudget},
		{"middle outputs go", 40, 14 << 10},
		{"outputs are cut", 40, 9 << 10},
		{"all outputs go", 40, 6 << 10},
		{"whole calls go", 40, 3 << 10},
		{"budget below the text itself", 40, 10},
		{"no budget", 40, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := run(tt.calls)
			got := Compact(msgs, tt.budget)
			for _, m := range msgs {
				if m.Role != "tool" && !strings.Contains(got, *m.Text) {
					t.Fatalf("text %q is missing:\n%s", *m.Text, got)
				}
			}
			// Dropped parts are always marked.
			if tt.calls > 6 && len(got) < len(Compact(msgs, 1<<30)) && !strings.Contains(got, "omitted") && !strings.Contains(got, "truncated") {
				t.Errorf("shortened without a marker:\n%s", got)
			}
		})
	}
}

func TestCompactDropsWholeToolCallsOnlyAsALastResort(t *testing.T) {
	msgs := run(40)
	// Enough for the text and the titles but not for arguments and outputs.
	textOnly := len(Compact([]Message{msgs[0], msgs[1], msgs[len(msgs)-2], msgs[len(msgs)-1]}, 1<<30))
	got := Compact(msgs, textOnly+1500)
	if !strings.Contains(got, " tool calls omitted ...]") {
		t.Fatalf("no whole call was dropped at this budget:\n%s", got)
	}
	// Oldest first: the oldest call is gone, and a newer one is only dropped after all older ones.
	if strings.Contains(got, "[tool] call-0 ") {
		t.Errorf("the oldest call survived:\n%s", got)
	}
	if strings.Contains(got, "[tool] call-38 ") && !strings.Contains(got, "[tool] call-39 ") {
		t.Errorf("a newer call was dropped before an older one:\n%s", got)
	}
}

func TestCompactSkipsDividersAndNotices(t *testing.T) {
	div, note := textMsg("system", "Continued from run X"), textMsg("system", "The sandbox was recreated.")
	div.Kind, note.Kind = "divider", "notice"
	got := Compact([]Message{div, textMsg("user", "hi"), note, textMsg("system", "Run finished.")}, DefaultBudget)
	if strings.Contains(got, "Continued") || strings.Contains(got, "recreated") || !strings.Contains(got, "[system]\nRun finished.") {
		t.Errorf("compact = %q", got)
	}
}

func TestCompactTruncatesLongOutputsPerItemAndRendersDiffs(t *testing.T) {
	long := strings.Repeat("y", 5000)
	diff, _ := json.Marshal(map[string]any{"type": "diff", "path": "a.go", "oldText": "old", "newText": "new body"})
	c := ToolCall{Title: "Edit a.go", Kind: "edit", Status: "completed", RawInput: json.RawMessage(`{"path":"a.go"}`), Content: []json.RawMessage{diff}}
	b, _ := json.Marshal(c)
	got := Compact([]Message{toolMsg("big", `{}`, long), {Role: "tool", ToolCall: b}}, DefaultBudget)
	if strings.Contains(got, strings.Repeat("y", 2100)) || !strings.Contains(got, "[truncated] (5000 bytes)") {
		t.Errorf("a long output is not cut per item:\n%.400s", got)
	}
	if !strings.Contains(got, "diff of a.go:\nnew body") {
		t.Errorf("diff content: %s", got)
	}
}

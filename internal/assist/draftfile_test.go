package assist

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"hal9001/internal/hashline"
	"hal9001/internal/store"
)

func viewOf(t *testing.T, content string, exists bool) hashline.View {
	t.Helper()
	v, err := hashline.Snapshot(content, exists, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func numbersOf(lines []hashline.Line) []int {
	out := []int{}
	for _, l := range lines {
		out = append(out, l.Number)
	}
	return out
}

// The result of an edit names the lines that changed, with their new anchors, and nothing else: the lines between two
// separate edits stay out, and where lines were only removed the neighbours of the gap show where.
func TestAssistHashlineAffectedLinesAreTheLinesThatChanged(t *testing.T) {
	long := strings.Repeat("a", 3000)
	for _, tc := range []struct {
		name, prev, next string
		prevExists       bool
		want             []int
		truncated        bool
	}{
		{name: "one line replaced", prev: "a\nb\nc\n", next: "a\nB\nc\n", prevExists: true, want: []int{2}},
		{name: "two separate lines", prev: "1\n2\n3\n4\n5\n6\n7\n", next: "1\nX\n3\n4\n5\nY\n7\n", prevExists: true, want: []int{2, 6}},
		{name: "lines inserted", prev: "a\nd\n", next: "a\nb\nc\nd\n", prevExists: true, want: []int{2, 3}},
		{name: "lines added at the end", prev: "a\nb\na\n", next: "a\nb\na\nb\na\n", prevExists: true, want: []int{4, 5}},
		{name: "lines removed: the lines around the gap", prev: "a\nb\nc\nd\n", next: "a\nd\n", prevExists: true, want: []int{1, 2}},
		{name: "last lines removed", prev: "a\nb\nc\n", next: "a\n", prevExists: true, want: []int{1}},
		{name: "everything removed", prev: "a\nb\n", next: "", prevExists: true, want: []int{}},
		{name: "nothing changed", prev: "a\nb\n", next: "a\nb\n", prevExists: true, want: []int{}},
		{name: "a new file", prev: "", next: "x\ny\n", want: []int{1, 2}},
		{name: "the final newline added", prev: "a\nb", next: "a\nb\n", prevExists: true, want: []int{2}},
		{name: "text over the size bound", prev: "", next: long + "1\n" + long + "2\n" + long + "3\n" + long + "4\n", want: []int{1, 2}, truncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, truncated := affectedLines(viewOf(t, tc.prev, tc.prevExists), viewOf(t, tc.next, true))
			if !slices.Equal(numbersOf(got), tc.want) || truncated != tc.truncated {
				t.Errorf("affected lines %v (truncated %v), want %v (truncated %v)", numbersOf(got), truncated, tc.want, tc.truncated)
			}
			next := viewOf(t, tc.next, true)
			for _, l := range got {
				if l != next.Lines[l.Number-1] {
					t.Errorf("line %d is %+v, want the anchored line of the new file %+v", l.Number, l, next.Lines[l.Number-1])
				}
			}
		})
	}

	t.Run("more lines than the bound", func(t *testing.T) {
		var prev, next []string
		for i := range 100 {
			prev = append(prev, "old"+strconv.Itoa(i))
			next = append(next, "new"+strconv.Itoa(i))
		}
		got, truncated := affectedLines(viewOf(t, strings.Join(prev, "\n")+"\n", true), viewOf(t, strings.Join(next, "\n")+"\n", true))
		if len(got) != maxAffectedLines || !truncated || got[0].Number != 1 {
			t.Errorf("listed %d lines (truncated %v), want the first %d and truncated", len(got), truncated, maxAffectedLines)
		}
	})
}

// Only the draft file of the session's kind, and for a flow its prompts and sensors by a valid name, can be addressed.
func TestAssistHashlineDraftPathsResolveToTheFilesTheSessionHas(t *testing.T) {
	for _, tc := range []struct {
		kind, path, want string
	}{
		{store.AssistTargetSensor, "", "sensor.yaml"},
		{store.AssistTargetSensor, " sensor.yaml ", "sensor.yaml"},
		{store.AssistTargetTool, "tool.yaml", "tool.yaml"},
		{store.AssistTargetFlow, "", "flow.yaml"},
		{store.AssistTargetFlow, "prompts/triage.md", "prompts/triage.md"},
		{store.AssistTargetFlow, "sensors/pr-watch.yaml", "sensors/pr-watch.yaml"},
		// Refused: files the session does not have, and names that could leave the directory or clash with the bookkeeping.
		{store.AssistTargetSensor, "prompts/triage.md", ""},
		{store.AssistTargetSensor, "sensors/gh.yaml", ""},
		{store.AssistTargetSensor, "flow.yaml", ""},
		{store.AssistTargetFlow, "sensor.yaml", ""},
		{store.AssistTargetFlow, "prompts/../flow.yaml", ""},
		{store.AssistTargetFlow, "prompts/a/b.md", ""},
		{store.AssistTargetFlow, "prompts/.md", ""},
		{store.AssistTargetFlow, "sensors/.bases.json", ""},
		{store.AssistTargetFlow, "sensors/.hidden.yaml", ""},
		{store.AssistTargetFlow, "reference/runtimes.txt", ""},
		{store.AssistTargetFlow, "prompts/" + strings.Repeat("a", 65) + ".md", ""},
	} {
		s, _ := uiTestSession(t, tc.kind)
		got, err := s.resolveDraft(tc.path)
		switch {
		case tc.want == "" && err == nil:
			t.Errorf("%s session resolved %q to %q, want it refused", tc.kind, tc.path, got.path)
		case tc.want != "" && (err != nil || got.path != tc.want):
			t.Errorf("%s session resolved %q to %q (%v), want %q", tc.kind, tc.path, got.path, err, tc.want)
		}
	}
}

// draft_write takes the edit request every Hashline tool shares, with the file to change added: a call without the hash
// it was made against, or one that cannot name its file, would be unusable.
func TestAssistHashlineDraftWriteSchemaIsTheSharedEditRequestPlusAPath(t *testing.T) {
	var schema map[string]any
	for _, tool := range (&session{}).draftTools() {
		if tool.Name == "draft_write" {
			schema = tool.InputSchema
		}
	}
	props, _ := schema["properties"].(map[string]any)
	for _, name := range []string{"path", "expected_hash", "content", "edits"} {
		if props[name] == nil {
			t.Errorf("draft_write has no property %q: %v", name, schema)
		}
	}
	if required, _ := schema["required"].([]string); !slices.Contains(required, "expected_hash") || slices.Contains(required, "path") {
		t.Errorf("required = %v, want the hash and not the path", schema["required"])
	}
	if shared, _ := hashline.RequestSchema()["properties"].(map[string]any); shared["path"] != nil {
		t.Error("the path was added to the schema every caller shares")
	}
}

package assist

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"hal9001/internal/store"
	"hal9001/internal/tools"
)

type uiEvent struct{ kind, session, path, reason, draftKind, id, prompt string }

// recordingUI collects what the UI tools publish.
type recordingUI struct {
	mu     sync.Mutex
	events []uiEvent
}

func (r *recordingUI) PublishAssistNavigate(session, path, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, uiEvent{kind: "navigate", session: session, path: path, reason: reason})
}

func (r *recordingUI) PublishAssistStartDraft(session, kind, id, prompt string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, uiEvent{kind: "start_draft", session: session, draftKind: kind, id: id, prompt: prompt})
}

// uiTestSession is a session of a manager that has only a home directory: enough for the tools that never touch the
// engine.
func uiTestSession(t *testing.T, kind string) (*session, *recordingUI) {
	t.Helper()
	home := t.TempDir()
	for dir, file := range map[string]string{"flows": "triage.yaml", "sensors": "gh.yaml", "tools": "echo.yaml"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, dir, file), []byte("id: x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := &Manager{home: home}
	ui := &recordingUI{}
	m.SetUIPublisher(ui)
	return newSession(m, store.AssistSession{ID: "01JC7Z3K9Q8V5T2M4N6P8R0S1T", TargetKind: kind, Status: store.AssistIdle}), ui
}

func callTool(t *testing.T, s *session, name string, args any) (tools.Result, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range s.uiTools() {
		if tool.Name == name {
			if tool.Approval != nil || tool.Policy != nil {
				t.Fatalf("%s needs no approval", name)
			}
			return tool.Run(context.Background(), tools.Call{Name: name, Args: raw})
		}
	}
	t.Fatalf("no tool %s", name)
	return tools.Result{}, nil
}

func TestNavigateUIPublishesOneEventForAnAllowedPath(t *testing.T) {
	s, ui := uiTestSession(t, store.AssistTargetGeneral)
	res, err := callTool(t, s, "navigate_ui", map[string]any{"path": "/runs/r1?run=n2&node=n1", "reason": "the failing\nnode,  highlighted"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "/runs/r1?node=n1&run=n2") {
		t.Errorf("result %q does not name the normalised path", res.Text)
	}
	want := []uiEvent{{kind: "navigate", session: s.id, path: "/runs/r1?node=n1&run=n2", reason: "the failing node, highlighted"}}
	if !slices.Equal(ui.events, want) {
		t.Errorf("published %+v, want %+v", ui.events, want)
	}
	if _, err := callTool(t, s, "navigate_ui", map[string]any{"path": "/inbox", "reason": strings.Repeat("r", 500)}); err != nil {
		t.Fatal(err)
	}
	if got := ui.events[1]; len(got.reason) > maxNavigateReason+3 || got.reason == "" {
		t.Errorf("long reason = %q", got.reason)
	}
}

func TestNavigateUIRefusesBadPathsWithoutPublishing(t *testing.T) {
	s, ui := uiTestSession(t, store.AssistTargetFlow)
	for _, path := range []string{"https://evil.example", "//evil.example", "javascript:alert(1)", "/nope", "/flows/../x", "/inbox?next=/x", "", "/inbox#x"} {
		if _, err := callTool(t, s, "navigate_ui", map[string]any{"path": path}); err == nil {
			t.Errorf("navigate_ui accepted %q", path)
		}
	}
	if _, err := callTool(t, s, "navigate_ui", map[string]any{"path": 5}); err == nil {
		t.Error("navigate_ui accepted a non-string path")
	}
	if len(ui.events) != 0 {
		t.Errorf("published %+v for refused paths", ui.events)
	}
}

func TestNavigateUIFailsWhenNoUIIsConnected(t *testing.T) {
	s, _ := uiTestSession(t, store.AssistTargetGeneral)
	s.m.SetUIPublisher(nil)
	if _, err := callTool(t, s, "navigate_ui", map[string]any{"path": "/inbox"}); err == nil {
		t.Error("navigate_ui succeeded without a UI to move")
	}
}

func TestStartDraftPublishesForAKindAndAnExistingTarget(t *testing.T) {
	s, ui := uiTestSession(t, store.AssistTargetGeneral)
	for _, args := range []map[string]any{
		{"kind": "flow", "id": "triage", "prompt": "  add a retry node to the failing branch  "},
		{"kind": "sensor", "prompt": "watch the build"},
		{"kind": "tool"},
		{"kind": "sensor", "id": "gh"},
	} {
		if _, err := callTool(t, s, "start_draft", args); err != nil {
			t.Fatalf("start_draft %v: %v", args, err)
		}
	}
	want := []uiEvent{
		{kind: "start_draft", session: s.id, draftKind: "flow", id: "triage", prompt: "add a retry node to the failing branch"},
		{kind: "start_draft", session: s.id, draftKind: "sensor", prompt: "watch the build"},
		{kind: "start_draft", session: s.id, draftKind: "tool"},
		{kind: "start_draft", session: s.id, draftKind: "sensor", id: "gh"},
	}
	if !slices.Equal(ui.events, want) {
		t.Errorf("published %+v, want %+v", ui.events, want)
	}
}

func TestStartDraftRefusesBadInputAndNamesTheIdsThatExist(t *testing.T) {
	s, ui := uiTestSession(t, store.AssistTargetGeneral)
	for _, tc := range []struct {
		args map[string]any
		want string // text the error must carry
	}{
		{map[string]any{"kind": "prompt"}, "sensor, flow or tool"},
		{map[string]any{}, "sensor, flow or tool"},
		{map[string]any{"kind": "flow", "id": "nope"}, "`triage`"},
		{map[string]any{"kind": "sensor", "id": "triage"}, "`gh`"}, // a flow's id is not a sensor
		{map[string]any{"kind": "tool", "id": "bad id"}, "not a valid tool id"},
		{map[string]any{"kind": "flow", "prompt": strings.Repeat("p", maxDraftPrompt+1)}, "shorten"},
	} {
		_, err := callTool(t, s, "start_draft", tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("start_draft %v: error %v, want one containing %q", tc.args, err, tc.want)
		}
	}
	if len(ui.events) != 0 {
		t.Errorf("published %+v for refused calls", ui.events)
	}
}

func TestEverySessionKindGetsTheUIToolsButOnlyDraftKindsGetDraftTools(t *testing.T) {
	names := func(kind string) []string {
		s, _ := uiTestSession(t, kind)
		var out []string
		for _, tool := range s.sessionTools() {
			out = append(out, tool.Name)
		}
		return out
	}
	for _, kind := range []string{store.AssistTargetGeneral, store.AssistTargetSensor, store.AssistTargetFlow, store.AssistTargetTool} {
		got := names(kind)
		for _, want := range []string{"navigate_ui", "start_draft", "github_issue_link", "flow_runs_list", "agent_run_get"} {
			if !slices.Contains(got, want) {
				t.Errorf("%s session lacks %s", kind, want)
			}
		}
		for _, draft := range []string{"draft_read", "draft_write", "draft_test", "validate"} {
			if has, want := slices.Contains(got, draft), kind != store.AssistTargetGeneral; has != want {
				t.Errorf("%s session has %s = %v, want %v", kind, draft, has, want)
			}
		}
	}
}

func TestPreamblesPointAtTheUIToolsAndTheGeneralOneCarriesThePageList(t *testing.T) {
	for name, p := range map[string]string{
		"sensor":  preamble("", nil, nil, "summary"),
		"flow":    flowPreamble("", nil, "summary"),
		"tool":    toolPreamble("", nil, nil, "summary"),
		"general": generalPreamble(),
	} {
		if !strings.Contains(p, "navigate_ui") || !strings.Contains(p, "start_draft") {
			t.Errorf("%s preamble does not mention navigate_ui and start_draft:\n%s", name, p)
		}
	}
	g := generalPreamble()
	for _, want := range []string{"[hal9001 assistant]", "no file or shell access", "/flows/:id/versions/:hash", "Looking at what hal9001 did", "github_issue_link", "start_draft"} {
		if !strings.Contains(g, want) {
			t.Errorf("general preamble lacks %q", want)
		}
	}
	if strings.Contains(g, "draft_write") || strings.Contains(g, "draft_read") {
		t.Error("the general preamble mentions draft tools the session does not have")
	}
	if !strings.Contains(configGuide(), "## The app's pages") {
		t.Error("the instructions of draft sessions do not carry the page list")
	}
}

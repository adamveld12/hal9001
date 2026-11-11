package assist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
	"hal9001/internal/tools"
)

// The assist agent has no file or shell tools (docs/acp-lockdown.md): it edits the draft and tests it through hal's
// MCP tools draft_read, draft_write, draft_test and validate (plus the always-available ones).

// registerTools adds the assist tools to every assist tool session the hub opens.
func (m *Manager) registerTools() {
	m.host.Tools().AddProvider(func(_ context.Context, rs tools.RunSpec) (tools.Extension, error) {
		if rs.Kind != tools.KindAssist {
			return tools.Extension{}, nil
		}
		m.mu.Lock()
		s := m.sessions[rs.RunID]
		m.mu.Unlock()
		if s == nil {
			return tools.Extension{}, fmt.Errorf("assist: no session %s", rs.RunID)
		}
		return tools.Extension{Tools: s.sessionTools()}, nil
	})
}

// sessionTools are the tools of one session: the draft tools (not for a general session, which has no draft), the read
// tools with github_issue_link, and navigate_ui and start_draft.
func (s *session) sessionTools() []tools.Tool {
	var out []tools.Tool
	if !s.isGeneral() {
		out = s.draftTools()
	}
	out = append(out, s.helperTools()...)
	return append(out, s.uiTools()...)
}

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func (s *session) draftTools() []tools.Tool {
	pathDoc := "The draft file: omit it for the draft itself, or use `prompts/<name>.md` (flow sessions: a new prompt) or `sensors/<id>.yaml` (flow sessions: a new sensor, or an edit of a saved one)."
	return []tools.Tool{
		{
			Name: "draft_read",
			Description: "Read the session's draft. Without `path` it returns the current draft with its validation diagnostics as JSON. " +
				"`path` may also be `instructions` (how to author this draft: read it first), `prompts/<name>.md` (a draft prompt), `sensors/<id>.yaml` (flow sessions: a draft sensor, else the saved one) or `sensors` (list the draft sensors) or `reference/<file>` " +
				"(read-only reference material of a flow session: runtimes.txt, sandboxes.md, tools.md, settings.md, docs/agent-nodes.md; flow sessions also docs/flows.md, sensors/, prompts/, samples/; a directory lists its entries).",
			InputSchema: obj(map[string]any{"path": map[string]any{"type": "string"}}),
			Run:         s.runDraftRead,
		},
		{
			Name: "draft_write",
			Description: "Replace the draft (or a draft prompt or sensor) with `content`. It never needs approval; it returns the validation diagnostics of the result. " +
				pathDoc,
			InputSchema: obj(map[string]any{
				"content": map[string]any{"type": "string", "description": "The complete new file content."},
				"path":    map[string]any{"type": "string", "description": pathDoc},
			}, "content"),
			Run: s.runDraftWrite,
		},
		{
			Name: "draft_test",
			Description: "Run the draft the way a poll (sensor) or one flow run (flow) would and return the result as JSON; flow sessions can pick the input with `sample` " +
				"(an index of reference/samples/index.txt) or `event`, or `sensor` (the id of a draft sensor of the session: run it like a poll) or `from_sensor` (+ `item`: run the draft sensor and use its item as the flow's input); tool sessions pass `args` (the tool's arguments, result is stdout/stderr/exit code). It runs the code with the user's credentials: the first test after the script changed asks the user for approval.",
			InputSchema: obj(map[string]any{
				"sample":            map[string]any{"type": "integer", "minimum": 0},
				"event":             map[string]any{"type": "string"},
				"sensor":            map[string]any{"type": "string", "description": "Flow sessions: the id of a draft sensor to run like a poll."},
				"from_sensor":       map[string]any{"type": "string", "description": "Flow sessions: the id of a draft sensor whose output item is the flow test's input."},
				"item":              map[string]any{"type": "integer", "minimum": 0, "description": "With from_sensor: which output item to use (default 0)."},
				"args":              map[string]any{"type": "object", "description": "Tool sessions: the arguments of the test call (a JSON object, defaults are filled in)."},
				"simulate_approval": map[string]any{"type": "string", "enum": []string{"approved", "denied"}, "description": "Flow sessions: the decision request-approval nodes and requires_approval gates take in the test (default approved); the test never waits for a person."},
			}),
			Approval: s.testApproval,
			Run:      s.runDraftTest,
		},
		{
			Name:        "validate",
			Description: "Validate the current draft against the sensor, flow or tool format and return the diagnostics as JSON.",
			InputSchema: obj(map[string]any{}),
			Run:         s.runValidate,
		},
	}
}

// instructionsText is the authoring guide of this session (what AGENTS.md used to hold).
func (s *session) instructionsText() (string, error) {
	if !s.isFlow() {
		if err := s.m.writeConfigReference(s.dir); err != nil {
			return "", err
		}
	}
	if s.isFlow() {
		if err := s.m.writeReference(s.dir); err != nil {
			return "", err
		}
		global, _ := s.m.envNames("")
		return flowInstructions(s.id, s.row.TargetID, global), nil
	}
	if s.isTool() {
		global, own := s.m.envNamesOf(s.row.TargetKind, s.row.TargetID)
		return toolInstructions(s.id, s.row.TargetID, global, own), nil
	}
	global, sensor := s.m.envNames(s.row.TargetID)
	return instructions(s.id, s.row.TargetID, global, sensor), nil
}

type draftView struct {
	File        string          `json:"file"`
	YAML        string          `json:"yaml"`
	Hash        string          `json:"hash"`
	Diagnostics json.RawMessage `json:"diagnostics"`
	Prompts     []string        `json:"prompts,omitempty"`
	Sensors     []string        `json:"sensors,omitempty"`
}

func (s *session) viewLocked() draftView {
	v := draftView{File: draftFileOf(s.row.TargetKind), YAML: s.row.DraftYAML, Hash: s.row.DraftHash, Diagnostics: s.row.DraftDiagnostics}
	for _, p := range readDraftPrompts(s.dir) {
		v.Prompts = append(v.Prompts, p.Name)
	}
	for _, f := range readDraftSensors(s.dir) {
		v.Sensors = append(v.Sensors, f.ID)
	}
	return v
}

type pathArg struct {
	Path string `json:"path"`
}

func (s *session) runDraftRead(_ context.Context, c tools.Call) (tools.Result, error) {
	var a pathArg
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return tools.Result{}, fmt.Errorf("arguments: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return tools.Result{}, err
	}
	switch p := strings.TrimSpace(a.Path); {
	case p == "" || p == draftFileOf(s.row.TargetKind):
		b, _ := json.MarshalIndent(s.viewLocked(), "", "  ")
		return tools.Result{Text: string(b)}, nil
	case p == "instructions":
		text, err := s.instructionsText()
		return tools.Result{Text: text}, err
	case strings.HasPrefix(p, PromptsDir+"/") && s.isFlow():
		return s.readUnder(PromptsDir, strings.TrimPrefix(p, PromptsDir+"/"))
	case s.isFlow() && (p == SensorsDir || p == SensorsDir+"/"):
		return s.readUnder(SensorsDir, ".")
	case s.isFlow() && strings.HasPrefix(p, SensorsDir+"/") && strings.HasSuffix(p, draftSensorSuffix):
		return s.readDraftSensorLocked(strings.TrimSuffix(strings.TrimPrefix(p, SensorsDir+"/"), draftSensorSuffix))
	case strings.HasPrefix(p, ReferenceDir):
		return s.readUnder(ReferenceDir, strings.TrimPrefix(strings.TrimPrefix(p, ReferenceDir), "/"))
	}
	return tools.Result{}, fmt.Errorf("unknown path %q: use no path, instructions, prompts/<name>.md, sensors/<id>.yaml or reference/<file>", a.Path)
}

// readUnder reads a file (or lists a directory) inside <draft dir>/<root>, refusing anything outside it.
func (s *session) readUnder(root, rel string) (tools.Result, error) {
	rel = path.Clean("/" + rel)[1:]
	if rel == "" {
		rel = "."
	}
	base := filepath.Join(s.dir, root)
	full := filepath.Join(base, filepath.FromSlash(rel))
	if r, err := filepath.Rel(base, full); err != nil || strings.HasPrefix(r, "..") {
		return tools.Result{}, errors.New("path escapes the draft directory")
	}
	st, err := os.Stat(full)
	if err != nil {
		return tools.Result{}, fmt.Errorf("%s/%s: not found", root, rel)
	}
	if st.IsDir() {
		entries, err := os.ReadDir(full)
		if err != nil {
			return tools.Result{}, err
		}
		var names []string
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() {
				n += "/"
			}
			names = append(names, n)
		}
		sort.Strings(names)
		return tools.Result{Text: strings.Join(names, "\n")}, nil
	}
	if st.Size() > 1<<20 {
		return tools.Result{}, errors.New("file is larger than 1 MiB")
	}
	b, err := os.ReadFile(full) //nolint:gosec // confined to the session's draft directory above
	return tools.Result{Text: string(b)}, err
}

func (s *session) runDraftWrite(_ context.Context, c tools.Call) (tools.Result, error) {
	var a struct {
		Content string `json:"content"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return tools.Result{}, fmt.Errorf("arguments: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return tools.Result{}, err
	}
	switch p := strings.TrimSpace(a.Path); {
	case p == "" || p == draftFileOf(s.row.TargetKind):
		if len(a.Content) > maxDraftBytes {
			return tools.Result{}, errors.New("the draft is too large")
		}
		if err := s.setDraftLocked(a.Content, store.DraftByAgent); err != nil {
			return tools.Result{}, err
		}
	case s.isFlow() && strings.HasPrefix(p, PromptsDir+"/") && strings.HasSuffix(p, ".md"):
		name := strings.TrimSuffix(strings.TrimPrefix(p, PromptsDir+"/"), ".md")
		if !spec.ValidID(name) {
			return tools.Result{}, fmt.Errorf("prompt name %q is invalid (letters, digits, - and _, at most 64 characters)", name)
		}
		if len(a.Content) > maxPromptBytes {
			return tools.Result{}, errors.New("the prompt is too large")
		}
		if err := writeAtomic(filepath.Join(s.dir, PromptsDir, name+".md"), []byte(a.Content)); err != nil {
			return tools.Result{}, err
		}
		s.applyDraft(s.row.DraftYAML, store.DraftByAgent)
		if err := s.saveLocked(); err != nil {
			return tools.Result{}, err
		}
	case s.isFlow() && strings.HasPrefix(p, SensorsDir+"/") && strings.HasSuffix(p, draftSensorSuffix):
		id := strings.TrimSuffix(strings.TrimPrefix(p, SensorsDir+"/"), draftSensorSuffix)
		if err := s.writeDraftSensorLocked(id, a.Content); err != nil {
			return tools.Result{}, err
		}
		s.applyDraft(s.row.DraftYAML, store.DraftByAgent)
		if err := s.saveLocked(); err != nil {
			return tools.Result{}, err
		}
	default:
		return tools.Result{}, fmt.Errorf("cannot write %q: use no path for the draft, or prompts/<name>.md or sensors/<id>.yaml in a flow session", a.Path)
	}
	b, _ := json.MarshalIndent(s.viewLocked(), "", "  ")
	return tools.Result{Text: "written. Current state:\n" + string(b)}, nil
}

func (s *session) runValidate(_ context.Context, _ tools.Call) (tools.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return tools.Result{}, err
	}
	var diags json.RawMessage
	switch {
	case s.isFlow():
		_, diags, _, _ = s.m.evaluateFlow(s.row.DraftYAML, s.row.TargetID, s.dir)
	case s.isTool():
		_, diags = evaluateTool(s.row.DraftYAML, s.row.TargetID)
	default:
		_, diags = evaluate(s.row.DraftYAML, s.row.TargetID)
	}
	return tools.Result{Text: string(diags)}, nil
}

// testApproval is draft_test's approval rule, the same the assist manager applies to a user-less test: running the
// draft uses real credentials, so it is allowed without asking only while its script is one the user has tested or
// approved. A changed script is queued with its text and diff (see approvalPending).
func (s *session) testApproval(args json.RawMessage) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var a struct {
		Sensor     string `json:"sensor"`
		FromSensor string `json:"from_sensor"`
	}
	_ = json.Unmarshal(args, &a)
	var id scriptID
	var approved bool
	var base string
	if s.isFlow() && (a.Sensor != "" || a.FromSensor != "") {
		p := s.testPlanLocked(core.AssistTestRequest{Sensor: a.Sensor, FromSensor: a.FromSensor})
		if !p.ok {
			return false, "" // a draft that does not validate is not run: the test returns the diagnostics
		}
		id = p.id
		approved, base = s.approvedAgainstLocked(p.id, p.saved, p.hasSaved)
	} else {
		var available []string
		var ok bool
		id, available, ok = s.draftScriptLocked()
		if !ok {
			return false, "" // a draft that does not validate is not run: the test returns the diagnostics
		}
		approved, base = s.scriptApprovedLocked(id, available)
	}
	if approved {
		return false, ""
	}
	s.toolTest = &pendingTest{id: id, base: base}
	return true, "the draft's code changed since the user last tested or approved it, and a test runs it with real credentials"
}

func (s *session) runDraftTest(ctx context.Context, c tools.Call) (tools.Result, error) {
	var a struct {
		Sample           *int            `json:"sample"`
		Event            string          `json:"event"`
		Args             json.RawMessage `json:"args"`
		Sensor           string          `json:"sensor"`
		FromSensor       string          `json:"from_sensor"`
		Item             *int            `json:"item"`
		SimulateApproval string          `json:"simulate_approval"`
	}
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return tools.Result{}, fmt.Errorf("arguments: %w", err)
	}
	out, err := s.m.TestDraft(ctx, s.id, true, core.AssistTestRequest{SampleIndex: a.Sample, EventID: a.Event, Args: a.Args,
		Sensor: a.Sensor, FromSensor: a.FromSensor, Item: a.Item, SimulateApproval: a.SimulateApproval})
	if err != nil {
		var verr *core.ValidationError
		if errors.As(err, &verr) {
			b, _ := json.Marshal(verr.Diagnostics)
			return tools.Result{}, fmt.Errorf("the draft does not validate: %s", b)
		}
		return tools.Result{}, err
	}
	b, merr := json.MarshalIndent(out, "", "  ")
	return tools.Result{Text: string(b)}, merr
}

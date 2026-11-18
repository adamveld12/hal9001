package assist

import (
	"bytes"
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
	"hal9001/internal/hashline"
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
	out = append(out, s.mcpTools()...)
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
	pathDoc := "The file: omit it for the draft itself, or use `prompts/<name>.md` (flow sessions: a prompt) or `sensors/<id>.yaml` (flow sessions: a sensor, new or a saved one)."
	return []tools.Tool{
		{
			Name: "draft_read",
			Description: "Read the session's draft as numbered lines with anchors, and its hash. " +
				"Every line is `{number, anchor, text}`: an anchor is `<line>:<4 hex digits>`, the line number and a short hash of its text. The result also has the file's `hash`, which draft_write needs as `expected_hash`, and the validation `diagnostics`. " +
				"Without `path` it reads the draft; `start` and `end` (1-based, inclusive) read a range of lines only. " +
				"`path` may also be `instructions` (how to author this draft: read it first), `prompts/<name>.md` (a draft prompt), `sensors/<id>.yaml` (flow sessions: a draft sensor, else the saved one; a new one reads as `exists: false`), `sensors` or `prompts` (list the draft files) or `reference/<file>` " +
				"(read-only reference material of a flow session: runtimes.txt, sandboxes.md, tools.md, settings.md, docs/agent-nodes.md; flow sessions also docs/flows.md, sensors/, prompts/, samples/; plain text, a directory lists its entries).",
			InputSchema: obj(map[string]any{
				"path":  map[string]any{"type": "string", "description": pathDoc},
				"start": map[string]any{"type": "integer", "minimum": 1, "description": "The first line to return (1-based). Draft files only."},
				"end":   map[string]any{"type": "integer", "minimum": 1, "description": "The last line to return, inclusive. Draft files only."},
			}),
			Run: s.runDraftRead,
		},
		{
			Name: "draft_write",
			Description: "Change the session's draft with line-anchored edits, or create or replace a whole file. " +
				"Always send `expected_hash`: the hash from your latest draft_read of that file, or from the result of your last draft_write to it. " +
				"Send `edits` to change some lines (each `{op, start, end?, content?}`: `op` is replace, delete, insert_before or insert_after; `start` and `end` (replace and delete only) are anchors from the read; all the edits of a call refer to the file as you read it and must not overlap), " +
				"or `content` for a whole file, written exactly as sent: with an empty `expected_hash` it creates a file that must not exist yet, with the file's hash it replaces all of it. " +
				"If the file changed since you read it, the call fails with a conflict and changes nothing: read the file again and repeat your edit with the new hash and anchors. " +
				"It never needs approval and returns the file's new `hash`, the `diagnostics` and the anchors of the changed lines (`affected`), not the whole file. " +
				pathDoc,
			InputSchema: draftWriteSchema(pathDoc),
			Run:         s.runDraftWrite,
		},
		{
			Name: "draft_test",
			Description: "Run the draft the way a poll (sensor) or one flow run (flow) would and return the result as JSON; flow sessions can pick the input with `sample` " +
				"(an index of reference/samples/index.txt) or `event`, or `sensor` (the id of a draft sensor of the session: run it like a poll) or `from_sensor` (+ `item`: run the draft sensor and use its item as the flow's input); `simulate_emit` (`{\"<node_id>\": {port, output?}}`) makes agent nodes emit on a port without running; tool sessions pass `args` (the tool's arguments, result is stdout/stderr/exit code). It runs the code with the user's credentials: the first test after the script changed asks the user for approval.",
			InputSchema: obj(map[string]any{
				"sample":            map[string]any{"type": "integer", "minimum": 0},
				"event":             map[string]any{"type": "string"},
				"sensor":            map[string]any{"type": "string", "description": "Flow sessions: the id of a draft sensor to run like a poll."},
				"from_sensor":       map[string]any{"type": "string", "description": "Flow sessions: the id of a draft sensor whose output item is the flow test's input."},
				"item":              map[string]any{"type": "integer", "minimum": 0, "description": "With from_sensor: which output item to use (default 0)."},
				"args":              map[string]any{"type": "object", "description": "Tool sessions: the arguments of the test call (a JSON object, defaults are filled in)."},
				"simulate_approval": map[string]any{"type": "string", "enum": []string{"approved", "denied"}, "description": "Flow sessions: the decision request-approval nodes and requires_approval gates take in the test (default approved); the test never waits for a person."},
				"simulate_emit": map[string]any{"type": "object", "description": "Flow sessions: agent nodes that emit instead of running, keyed by node id.",
					"additionalProperties": map[string]any{"type": "object", "required": []string{"port"}, "properties": map[string]any{
						"port":   map[string]any{"type": "string"},
						"output": map[string]any{"type": "object", "description": "The emitted arguments; omitted: a minimal payload built from the port's schema."},
					}}},
			}),
			Approval: s.testApproval,
			Describe: s.testDescribe,
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

// draftWriteSchema is the input schema of draft_write: the edit request every Hashline tool shares, and the path of the file.
func draftWriteSchema(pathDoc string) map[string]any {
	schema := map[string]any{}
	for k, v := range hashline.RequestSchema() {
		schema[k] = v
	}
	props := map[string]any{"path": map[string]any{"type": "string", "description": pathDoc}}
	if request, ok := schema["properties"].(map[string]any); ok {
		for k, v := range request {
			props[k] = v
		}
	}
	schema["properties"] = props
	return schema
}

// decodeArgs decodes the arguments of a tool call. A field the tool does not have is an error: a misspelled one would
// otherwise be ignored without a word.
func decodeArgs(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// marshalResult encodes the result of a draft tool as one line of JSON. Unlike json.Marshal it leaves <, > and & as
// they are: the agent copies text out of the lines it reads, and \u0026 is not what a script says.
func marshalResult(v any) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// draftReadResult is what draft_read returns for a draft file: its anchored lines and hash, and for the draft itself
// the names of the prompts and sensors drafted next to it.
type draftReadResult struct {
	core.AssistDraftFile
	Prompts []string `json:"prompts,omitempty"`
	Sensors []string `json:"sensors,omitempty"`
}

func (s *session) runDraftRead(_ context.Context, c tools.Call) (tools.Result, error) {
	var a struct {
		Path  string `json:"path"`
		Start int    `json:"start"`
		End   int    `json:"end"`
	}
	if err := decodeArgs(c.Args, &a); err != nil {
		return tools.Result{}, fmt.Errorf("arguments: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return tools.Result{}, err
	}
	p := strings.TrimSpace(a.Path)
	// What is not a draft file is plain text: the instructions, the reference material and the listings.
	plain := func() error {
		if a.Start != 0 || a.End != 0 {
			return errors.New("start and end only select lines of a draft file")
		}
		return nil
	}
	switch {
	case p == "instructions":
		if err := plain(); err != nil {
			return tools.Result{}, err
		}
		text, err := s.instructionsText()
		return tools.Result{Text: text}, err
	case strings.HasPrefix(p, ReferenceDir):
		if err := plain(); err != nil {
			return tools.Result{}, err
		}
		return s.readUnder(ReferenceDir, strings.TrimPrefix(strings.TrimPrefix(p, ReferenceDir), "/"))
	case s.isFlow() && (p == PromptsDir || p == PromptsDir+"/"):
		if err := plain(); err != nil {
			return tools.Result{}, err
		}
		return s.readUnder(PromptsDir, ".")
	case s.isFlow() && (p == SensorsDir || p == SensorsDir+"/"):
		if err := plain(); err != nil {
			return tools.Result{}, err
		}
		return s.readUnder(SensorsDir, ".")
	}
	t, ok, err := s.draftTargetOf(p)
	switch {
	case err != nil:
		return tools.Result{}, draftToolError(err)
	case !ok:
		return tools.Result{}, fmt.Errorf("unknown path %q: use %s, instructions, a listing (prompts, sensors) or reference/<file>", a.Path, s.draftPathsHelp())
	}
	file, err := s.readDraftLocked(t, a.Start, a.End)
	if err != nil {
		return tools.Result{}, draftToolError(err)
	}
	res := draftReadResult{AssistDraftFile: file}
	if t.kind == draftIsPrimary {
		for _, pr := range readDraftPrompts(s.dir) {
			res.Prompts = append(res.Prompts, pr.Name)
		}
		for _, f := range readDraftSensors(s.dir) {
			res.Sensors = append(res.Sensors, f.ID)
		}
	}
	text, err := marshalResult(res)
	return tools.Result{Text: text}, err
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
		Path         string          `json:"path"`
		ExpectedHash string          `json:"expected_hash"`
		Content      *string         `json:"content"`
		Edits        []hashline.Edit `json:"edits"`
	}
	if err := decodeArgs(c.Args, &a); err != nil {
		return tools.Result{}, fmt.Errorf("arguments: %w", err)
	}
	res, err := s.editDraft(a.Path, hashline.Request{ExpectedHash: a.ExpectedHash, Content: a.Content, Edits: a.Edits}, store.DraftByAgent)
	if err != nil {
		return tools.Result{}, draftToolError(err)
	}
	text, err := marshalResult(res)
	return tools.Result{Text: text}, err
}

// draftToolError words an error of the draft operations for the agent: what to do next, not a problem type.
func draftToolError(err error) error {
	var conflict *core.DraftConflictError
	var verr *core.ValidationError
	switch {
	case errors.As(err, &conflict):
		msg := conflict.Error()
		if conflict.Expected != "" && conflict.Current != "" {
			msg += ": read it again with draft_read, then repeat your edit with the new hash and anchors"
		}
		return errors.New(msg)
	case errors.As(err, &verr):
		msgs := make([]string, 0, len(verr.Diagnostics))
		for _, d := range verr.Diagnostics {
			msgs = append(msgs, d.Message)
		}
		return errors.New(strings.Join(msgs, "; "))
	}
	return err
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
		_, diags = evaluate(s.row.DraftYAML, s.row.TargetID, s.m.sensorRefs())
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

// testDescribe is draft_test's approval text from the facts of the script testApproval just queued: what runs and
// which of the user's credentials it mentions.
func (s *session) testDescribe(json.RawMessage, string) tools.ApprovalText {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.toolTest
	if p == nil {
		return tools.ApprovalText{}
	}
	id := p.id
	kind, what := "sensor", "script"
	switch {
	case id.Flow:
		kind = "flow"
		var names []string
		for _, n := range id.Nodes {
			names = append(names, n.ID)
		}
		what = "command nodes"
		if len(names) > 0 {
			what += " (" + strings.Join(names, ", ") + ")"
		}
		if len(id.Sensors) > 0 {
			what += " and the draft sensors that feed it"
		}
	case id.Tool:
		kind = "tool"
	}
	if !id.Flow && len(id.Command) > 0 {
		what = "command"
	}
	if !id.Flow && id.Lang != "" {
		what = id.Lang + " " + what
	}
	env := map[string]bool{}
	for _, e := range id.Env {
		env[e] = true
	}
	for _, n := range append(append([]nodeScript(nil), id.Nodes...), id.Sensors...) {
		for _, e := range n.Env {
			env[e] = true
		}
	}
	creds := make([]string, 0, len(env))
	for e := range env {
		creds = append(creds, e)
	}
	sort.Strings(creds)
	with := "none of the user's credentials"
	if len(creds) > 0 {
		with = "the credentials " + strings.Join(creds, ", ")
	}
	target := s.row.TargetID
	if target == "" {
		target = "(new)"
	}
	fields := []tools.ApprovalField{{Key: "draft", Value: kind + " " + target}, {Key: "credentials", Value: strings.Join(creds, ", ")}}
	if mcp := mcpFact(id); mcp != "" {
		fields = append(fields, tools.ApprovalField{Key: "mcp", Value: mcp})
		with += " and calls remote MCP servers (" + mcp + ")"
	}
	return tools.ApprovalText{
		Title:       "Test the draft " + kind + " " + target,
		Description: fmt.Sprintf("Run the draft %s %s: its %s changed since it was last tested or approved. It uses %s.", kind, target, what, with),
		Command:     id.text(),
		Fields:      fields,
	}
}

func (s *session) runDraftTest(ctx context.Context, c tools.Call) (tools.Result, error) {
	var a struct {
		Sample           *int                          `json:"sample"`
		Event            string                        `json:"event"`
		Args             json.RawMessage               `json:"args"`
		Sensor           string                        `json:"sensor"`
		FromSensor       string                        `json:"from_sensor"`
		Item             *int                          `json:"item"`
		SimulateApproval string                        `json:"simulate_approval"`
		SimulateEmit     map[string]core.SimulatedEmit `json:"simulate_emit"`
	}
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return tools.Result{}, fmt.Errorf("arguments: %w", err)
	}
	out, err := s.m.TestDraft(ctx, s.id, true, core.AssistTestRequest{SampleIndex: a.Sample, EventID: a.Event, Args: a.Args,
		Sensor: a.Sensor, FromSensor: a.FromSensor, Item: a.Item, SimulateApproval: a.SimulateApproval, SimulateEmit: a.SimulateEmit})
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

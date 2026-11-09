package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"hal9001/internal/agent/fakeagent"
	"hal9001/internal/spec"
)

// AI-assisted sensor authoring end to end: a real daemon, the scripted fake ACP agent as the runtime, and the
// real hal9001 binary on the agent's PATH (agents run `hal9001 assist test` against the daemon over HTTP).

const (
	secretSensorValue = "sensor-secret-value-4444"
	secretGlobalValue = "global-secret-value-5555"

	emitTwoYAML = "id: emit-two\ninterval: 5m\nlang: bash\nscript: |\n  echo '[{\"id\":1,\"name\":\"a\"},{\"id\":2,\"name\":\"b\"}]'\nkey: \"{{.id}}\"\nfirst_run: fire\ntimeout: 10s\n"
)

type assistMsg struct {
	ID         string  `json:"id"`
	Role       string  `json:"role"`
	Text       *string `json:"text"`
	AgentRunID *string `json:"agent_run_id"`
	ApprovalID *string `json:"approval_id"`
	ToolCall   *struct {
		ToolCallID string `json:"tool_call_id"`
		Title      string `json:"title"`
		Kind       string `json:"kind"`
		Status     string `json:"status"`
	} `json:"tool_call"`
}

type assistDraftJSON struct {
	YAML        string           `json:"yaml"`
	Hash        string           `json:"hash"`
	SensorID    *string          `json:"sensor_id"`
	Diagnostics []map[string]any `json:"diagnostics"`
	UpdatedBy   string           `json:"updated_by"`
}

type assistDetail struct {
	ID             string  `json:"id"`
	Status         string  `json:"status"`
	Error          *string `json:"error"`
	LastAgentRunID *string `json:"last_agent_run_id"`
	Target         struct {
		Kind string  `json:"kind"`
		ID   *string `json:"id"`
	} `json:"target"`
	Draft    assistDraftJSON `json:"draft"`
	Messages []assistMsg     `json:"messages"`
}

func (m assistMsg) text() string {
	if m.Text == nil {
		return ""
	}
	return *m.Text
}

// assistRuntime registers the fake runtime "fake" with the given turns.
func (d *Daemon) assistRuntime(load bool, turns ...turn) {
	d.t.Helper()
	d.Runtime("fake", script{LoadSession: load, Turns: turns})
}

// newAssistDaemon is a daemon with the real CLI on PATH, started after cfg runs.
func newAssistDaemon(t *testing.T, cfg func(d *Daemon)) *Daemon {
	t.Helper()
	d := newDaemon(t)
	d.InstallCLI()
	if cfg != nil {
		cfg(d)
	}
	d.Start()
	return d
}

func (d *Daemon) assistCreate(sensor string) assistDetail {
	d.t.Helper()
	target := map[string]any{"kind": "sensor"}
	if sensor != "" {
		target["id"] = sensor
	}
	var s assistDetail
	r := d.Post("/api/assist/sessions", map[string]any{"target": target, "runtime": "fake"}).Want(201)
	r.JSON(&s)
	if loc := r.Header.Get("Location"); loc != "/api/assist/sessions/"+s.ID {
		d.t.Errorf("Location = %q", loc)
	}
	return s
}

func (d *Daemon) assistGet(id string) assistDetail {
	d.t.Helper()
	var s assistDetail
	d.Get("/api/assist/sessions/" + id).Want(200).JSON(&s)
	return s
}

func (d *Daemon) assistSend(id, text string, yaml *string) {
	d.t.Helper()
	body := map[string]any{"text": text}
	if yaml != nil {
		body["yaml"] = *yaml
	}
	d.Post("/api/assist/sessions/"+id+"/messages", body).Want(202)
}

// assistWait waits until the session has one of the statuses.
func (d *Daemon) assistWait(id string, statuses ...string) assistDetail {
	d.t.Helper()
	var s assistDetail
	d.Eventually(fmt.Sprintf("assist session %s to be %v", id, statuses), func() bool {
		s = d.assistGet(id)
		for _, st := range statuses {
			if s.Status == st {
				return true
			}
		}
		return false
	})
	return s
}

func (d *Daemon) assistDir(id string) string { return d.Path("data/assist/" + id) }

// assistTurn sends text, waits for the turn to end and returns the session.
func (d *Daemon) assistTurn(id, text string) assistDetail {
	d.t.Helper()
	d.assistSend(id, text, nil)
	return d.assistWait(id, "idle", "failed")
}

type runJSON struct {
	ID              string  `json:"id"`
	AssistSessionID *string `json:"assist_session_id"`
	NodeRunID       *string `json:"node_run_id"`
	Mode            string  `json:"mode"`
	ApproveStyle    *string `json:"approve_style"`
	Policy          string  `json:"policy"`
	Phase           string  `json:"phase"`
	Status          string  `json:"status"`
	SessionID       *string `json:"session_id"`
	ResumedVia      *string `json:"resumed_via"`
	ResumeFallback  *string `json:"resume_fallback"`
	Error           *string `json:"error"`
	RenderedPrompt  string  `json:"rendered_prompt"`
}

func (d *Daemon) assistRuns(id string) []runJSON {
	d.t.Helper()
	var l struct {
		Items []runJSON `json:"items"`
	}
	d.Get("/api/agent-runs?assist_session_id=" + id + "&limit=200").Want(200).JSON(&l)
	for i, j := 0, len(l.Items)-1; i < j; i, j = i+1, j-1 { // oldest first
		l.Items[i], l.Items[j] = l.Items[j], l.Items[i]
	}
	return l.Items
}

type permJSON struct {
	ToolKind    string  `json:"tool_kind"`
	Title       string  `json:"title"`
	Decision    *string `json:"decision"`
	DecidedBy   *string `json:"decided_by"`
	MatchedRule *string `json:"matched_rule"`
	ApprovalID  *string `json:"approval_id"`
}

func (d *Daemon) permissions(runID string) []permJSON {
	d.t.Helper()
	var l struct {
		Items []permJSON `json:"items"`
	}
	d.Get("/api/agent-runs/" + runID + "/permission-requests").Want(200).JSON(&l)
	return l.Items
}

func decisionOf(p permJSON) string {
	if p.Decision == nil {
		return ""
	}
	return *p.Decision
}

func ruleOf(p permJSON) string {
	if p.MatchedRule == nil {
		return ""
	}
	return *p.MatchedRule
}

// mcpStep is the agent calling a tool of hal's MCP server like a real runtime: it asks its own permission (hal's tools
// are allowed at that layer), then the call runs inside hal, which approves, runs and records it. save is a file in the
// agent's working directory that receives the result text when the call succeeded.
func mcpStep(id, tool string, args any, save string) step {
	return step{Op: fakeagent.OpMCPCall, ID: id, Tool: tool, Args: args, Path: save}
}

// assistEdit is the agent replacing the sensor draft (draft_write).
func assistEdit(yaml string) step {
	return mcpStep("edit-draft", "draft_write", map[string]any{"content": yaml}, "")
}

// halPerms are the hal-side records of the run's tool calls (the agent layer's own requests are the others).
func (d *Daemon) halPerms(runID, tool string) []permJSON {
	d.t.Helper()
	var out []permJSON
	for _, p := range d.permissions(runID) {
		if p.Title == "hal tool: "+tool {
			out = append(out, p)
		}
	}
	return out
}

// shellStep runs a shell command in the agent's working directory with the agent's own environment.
func shellStep(cmd string) step { return step{Op: fakeagent.OpExec, Cmd: []string{"sh", "-c", cmd}} }

func when(prompt string) *fakeagent.When { return &fakeagent.When{PromptMatches: prompt} }

func (d *Daemon) pendingApprovals() []map[string]any {
	d.t.Helper()
	var l struct {
		Items []map[string]any `json:"items"`
	}
	d.Get("/api/approvals?status=pending").Want(200).JSON(&l)
	return l.Items
}

func (d *Daemon) waitPendingApproval() map[string]any {
	d.t.Helper()
	var a map[string]any
	d.Eventually("a pending approval", func() bool {
		if items := d.pendingApprovals(); len(items) > 0 {
			a = items[0]
			return true
		}
		return false
	})
	return a
}

// assertNoSecret fails when any of the secrets appears in anything the assist and agent APIs return for the session.
func (d *Daemon) assertNoSecret(sessionID string, secrets ...string) {
	d.t.Helper()
	paths := []string{
		"/api/assist/sessions", "/api/assist/sessions/" + sessionID, "/api/agent-runs?assist_session_id=" + sessionID, "/api/approvals",
	}
	for _, run := range d.assistRuns(sessionID) {
		paths = append(paths,
			"/api/agent-runs/"+run.ID, "/api/agent-runs/"+run.ID+"/events?limit=500",
			"/api/agent-runs/"+run.ID+"/permission-requests", "/api/agent-runs/"+run.ID+"/raw")
	}
	for _, p := range paths {
		body := d.Get(p, withoutContract()).Body
		for _, s := range secrets {
			if strings.Contains(string(body), s) {
				d.t.Errorf("GET %s leaks a secret value", p)
			}
		}
	}
}

// ---- tests ----

// The agent replaces the draft with draft_write: the call is allowed without asking, the draft shows up in
// the session with the validator's diagnostics, the conversation records it, and the turn is an agent run linked to
// the session.
func TestAssistAgentEditsTheDraftOfANewSensor(t *testing.T) {
	d := newAssistDaemon(t, func(d *Daemon) {
		d.assistRuntime(false, turn{When: when("make it emit two items$"), Steps: []step{
			say("I will write the sensor."),
			assistEdit(emitTwoYAML),
			say("Done: it emits two items."),
		}})
	})

	s := d.assistCreate("")
	if s.Status != "idle" || s.Target.Kind != "sensor" || s.Target.ID != nil || s.Draft.UpdatedBy != "user" || s.Draft.SensorID == nil || *s.Draft.SensorID != "new-sensor" {
		t.Fatalf("new session = %+v", s)
	}
	if s.Draft.Diagnostics == nil {
		t.Error("diagnostics must be an array, not null")
	}
	skeleton := s.Draft.YAML
	if _, err := os.Stat(filepath.Join(d.assistDir(s.ID), "sensor.yaml")); err != nil {
		t.Fatalf("draft file: %v", err)
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if fileExists(filepath.Join(d.assistDir(s.ID), name)) {
			t.Errorf("%s was written: the agent has no file access, it reads the instructions with draft_read", name)
		}
	}

	got := d.assistTurn(s.ID, "make it emit two items")
	if got.Status != "idle" || got.Error != nil {
		t.Fatalf("after the turn: status %q error %v", got.Status, got.Error)
	}
	if got.Draft.YAML != emitTwoYAML || got.Draft.UpdatedBy != "agent" || got.Draft.Hash == s.Draft.Hash || got.Draft.YAML == skeleton {
		t.Errorf("draft = %+v", got.Draft)
	}
	if got.Draft.SensorID == nil || *got.Draft.SensorID != "emit-two" || len(got.Draft.Diagnostics) != 0 {
		t.Errorf("draft id %v diagnostics %v", got.Draft.SensorID, got.Draft.Diagnostics)
	}

	// Conversation: user, assistant text, the tool call (updated in place, completed), the tool result, assistant text.
	var roles []string
	for _, m := range got.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "user,assistant,tool,assistant,assistant" { // the fake agent reports the tool result as a message
		t.Fatalf("roles = %v, messages %+v", roles, got.Messages)
	}
	if tool := got.Messages[2]; tool.ToolCall == nil || tool.ToolCall.Kind != "other" || tool.ToolCall.Status != "completed" || tool.ApprovalID != nil || tool.AgentRunID == nil {
		t.Errorf("tool message = %+v %+v", tool, tool.ToolCall)
	}
	if got.Messages[0].text() != "make it emit two items" || got.Messages[1].text() != "I will write the sensor." || got.Messages[4].text() != "Done: it emits two items." {
		t.Errorf("texts = %q %q %q", got.Messages[0].text(), got.Messages[1].text(), got.Messages[4].text())
	}

	// Traceability: one agent run per turn, gated, linked to the session, edit allowed by the draft-directory rule.
	runs := d.assistRuns(s.ID)
	if len(runs) != 1 || got.LastAgentRunID == nil || *got.LastAgentRunID != runs[0].ID {
		t.Fatalf("runs = %+v, last_agent_run_id = %v", runs, got.LastAgentRunID)
	}
	r := runs[0]
	if r.AssistSessionID == nil || *r.AssistSessionID != s.ID || r.NodeRunID != nil || r.Mode != "approve" || r.Policy != "gated" || r.Status != "succeeded" || r.Phase != "execute" {
		t.Errorf("run = %+v", r)
	}
	perms := d.permissions(r.ID)
	if len(perms) != 2 || decisionOf(perms[0]) != "allow" || ruleOf(perms[0]) != "hal:mcp" || perms[1].Title != "hal tool: draft_write" || decisionOf(perms[1]) != "allow" || *perms[1].DecidedBy != "policy" {
		t.Errorf("permissions = %+v", perms)
	}
	for _, want := range []string{"<hal_context>", "assist_session: " + s.ID, "- draft_write:", "- draft_test:", "[hal9001 sensor assistant]", "make it emit two items"} {
		if !strings.Contains(r.RenderedPrompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, r.RenderedPrompt)
		}
	}
	var hasHal bool
	for _, c := range d.AgentCalls("fake") {
		if c["method"] == "session/new" {
			b, _ := json.Marshal(c["mcp_servers"])
			hasHal = strings.Contains(string(b), `"name":"hal"`) && strings.Contains(string(b), "hal_mcp_")
		}
	}
	if !hasHal {
		t.Error("session/new carried no hal MCP server")
	}
	if n := len(d.pendingApprovals()); n != 0 {
		t.Errorf("%d approvals pending", n)
	}
	var detail struct {
		AssistSessionID *string `json:"assist_session_id"`
	}
	d.Get("/api/agent-runs/" + r.ID).Want(200).JSON(&detail)

	// The CLI is a thin client of the same API.
	out, _, code := d.CLI("", "assist", "draft", s.ID)
	var cliDraft assistDraftJSON
	if code != 0 || json.Unmarshal([]byte(out), &cliDraft) != nil || cliDraft.Hash != got.Draft.Hash {
		t.Errorf("assist draft: code %d out %s", code, out)
	}
}

// The agent calls draft_test (asks first because it changed the script): the daemon runs the draft with the global env
// overlaid by the target sensor's own values and returns the redacted result. The agent's environment holds no
// secret, no API response shows one, the sensor file stays as it was until the user saves, and nothing is recorded.
func TestAssistTestUsesSecretsButNeverShowsThem(t *testing.T) {
	const gh = "id: gh\ninterval: 1h\nlang: bash\nscript: |\n  echo '[]'\nkey: \"{{.id}}\"\nfirst_run: fire\ntimeout: 10s\n"
	draft := "id: gh\ninterval: 1h\nlang: bash\nscript: |\n" +
		"  printf '[{\"id\":\"%s\",\"sensor\":\"%s\",\"global\":\"%s\",\"state\":\"%s\",\"last\":\"%s\"}]' \"$HAL_NODE\" \"$SENSOR_TOKEN\" \"$GLOBAL_TOKEN\" \"$HAL_STATE_DIR\" \"${HAL_LAST_SUCCESS_AT:-unset}\"\n" +
		"  echo \"using $SENSOR_TOKEN\" >&2\n" +
		"key: \"{{.id}}:{{.sensor}}\"\nfirst_run: fire\ntimeout: 10s\n"
	d := newAssistDaemon(t, func(d *Daemon) {
		d.Sensor("gh", gh)
		d.Sensor("other", "id: other\ninterval: 1h\nlang: bash\nscript: \"true\"\nkey: \"{{.id}}\"\n")
		d.Env("GLOBAL_TOKEN=" + secretGlobalValue + "\n")
		d.assistRuntime(false, turn{When: when("test it$"), Steps: []step{
			assistEdit(draft),
			shellStep("env > agent-env.txt"),
			mcpStep("t1", "draft_test", map[string]any{}, "test-result.json"),
			mcpStep("t2", "draft_read", map[string]any{}, "draft.json"),
			mcpStep("t3", "draft_read", map[string]any{"path": "instructions"}, "instructions.txt"),
			say("Tested."),
		}})
	})
	d.Put("/api/sensors/gh/env/SENSOR_TOKEN", map[string]any{"value": secretSensorValue}).Want(204)
	d.Put("/api/sensors/other/env/OTHER_TOKEN", map[string]any{"value": "other-secret-value-6666"}).Want(204)

	// An existing sensor starts from its file; the sensor file is never written by the session.
	var before struct {
		Hash string `json:"hash"`
	}
	d.Get("/api/sensors/gh").Want(200).JSON(&before)
	s := d.assistCreate("gh")
	if s.Target.ID == nil || *s.Target.ID != "gh" || s.Draft.YAML != gh || s.Draft.Hash != before.Hash {
		t.Fatalf("session for an existing sensor = %+v", s)
	}

	// The agent changed the script, so its test asks first; the user allows it.
	d.assistSend(s.ID, "test it", nil)
	ap := d.waitPendingApproval()
	d.Post("/api/approvals/"+ap["id"].(string)+"/approve", map[string]any{}).Want(200)
	got := d.assistWait(s.ID, "idle", "failed")
	if got.Status != "idle" {
		t.Fatalf("status %q error %v", got.Status, got.Error)
	}
	if got.Draft.YAML != draft {
		t.Fatalf("draft = %q", got.Draft.YAML)
	}
	var after struct {
		Hash string `json:"hash"`
		YAML string `json:"yaml"`
	}
	d.Get("/api/sensors/gh").Want(200).JSON(&after)
	if after.Hash != before.Hash || after.YAML != gh {
		t.Errorf("the sensor file changed before Save: %+v", after)
	}

	// The test of the changed script was decided by the user, the draft print by policy.
	run := d.assistRuns(s.ID)[0]
	test, read1 := d.halPerms(run.ID, "draft_test"), d.halPerms(run.ID, "draft_read")
	if len(test) != 1 || decisionOf(test[0]) != "allow" || *test[0].DecidedBy != "user" || ruleOf(test[0]) != "hal:approval" ||
		len(read1) != 2 || decisionOf(read1[0]) != "allow" || *read1[0].DecidedBy != "policy" {
		t.Errorf("hal permissions: test %+v draft_read %+v", test, read1)
	}

	// What the agent's tool calls returned: the real result, redacted.
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(d.assistDir(s.ID), name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return b
	}
	var res struct {
		ExitCode  int  `json:"exit_code"`
		TimedOut  bool `json:"timed_out"`
		ItemCount int  `json:"item_count"`
		Items     []struct {
			DedupKey *string        `json:"dedup_key"`
			KeyError *string        `json:"key_error"`
			Item     map[string]any `json:"item"`
		} `json:"items"`
		Stdout struct {
			Text string `json:"text"`
		} `json:"stdout"`
		Stderr struct {
			Text string `json:"text"`
		} `json:"stderr"`
	}
	if err := json.Unmarshal(read("test-result.json"), &res); err != nil {
		t.Fatalf("test-result.json: %v: %s", err, read("test-result.json"))
	}
	if res.ExitCode != 0 || res.TimedOut || res.ItemCount != 1 || len(res.Items) != 1 || res.Items[0].KeyError != nil {
		t.Fatalf("result = %+v", res)
	}
	it := res.Items[0].Item
	if it["sensor"] != "«SENSOR_TOKEN»" || it["global"] != "«GLOBAL_TOKEN»" || it["id"] != "gh" || it["last"] != "unset" {
		t.Errorf("item = %v (secrets must be redacted, the sensor id set, HAL_LAST_SUCCESS_AT unset)", it)
	}
	if res.Items[0].DedupKey == nil || *res.Items[0].DedupKey != "gh:«SENSOR_TOKEN»" || !strings.Contains(res.Stderr.Text, "using «SENSOR_TOKEN»") {
		t.Errorf("dedup key %v, stderr %q", res.Items[0].DedupKey, res.Stderr.Text)
	}
	if state, _ := it["state"].(string); state == "" || strings.HasPrefix(state, d.Home) {
		t.Errorf("HAL_STATE_DIR = %q, want a temporary directory outside the home", state)
	}
	var printed assistDraftJSON
	if err := json.Unmarshal(read("draft.json"), &printed); err != nil || printed.Hash != got.Draft.Hash {
		t.Errorf("assist draft printed %s (%v)", read("draft.json"), err)
	}

	// The agent's own environment: no secret value, and no way back to the daemon's CLI.
	agentEnv := string(read("agent-env.txt"))
	for _, secret := range []string{secretSensorValue, secretGlobalValue, "other-secret-value-6666"} {
		if strings.Contains(agentEnv, secret) {
			t.Errorf("the agent environment holds a secret value")
		}
	}
	for _, name := range []string{"SENSOR_TOKEN=", "GLOBAL_TOKEN=", "OTHER_TOKEN=", "HAL9001_ASSIST_SESSION="} {
		if strings.Contains(agentEnv, name) {
			t.Errorf("agent environment has %s", name)
		}
	}
	// The instructions name the variables, never the values.
	instr := string(read("instructions.txt"))
	if !strings.Contains(instr, "`GLOBAL_TOKEN`") || !strings.Contains(instr, "`SENSOR_TOKEN`") || strings.Contains(instr, "OTHER_TOKEN") || !strings.Contains(instr, "draft_test") {
		t.Errorf("the instructions list the wrong variable names:\n%s", instr)
	}

	// The endpoint behind the CLI: same result, nothing recorded.
	var api struct {
		ItemCount int `json:"item_count"`
		Items     []struct {
			DedupKey *string `json:"dedup_key"`
		} `json:"items"`
		Stdout struct {
			Text string `json:"text"`
		} `json:"stdout"`
	}
	d.Post("/api/assist/sessions/"+s.ID+"/test", nil).Want(200).JSON(&api)
	if api.ItemCount != 1 || strings.Contains(api.Stdout.Text, secretSensorValue) || !strings.Contains(api.Stdout.Text, "«SENSOR_TOKEN»") {
		t.Errorf("POST test = %+v", api)
	}
	var runs, events struct {
		Items []json.RawMessage `json:"items"`
	}
	d.Get("/api/sensors/gh/runs").Want(200).JSON(&runs)
	d.Get("/api/events?sensor_id=gh").Want(200).JSON(&events)
	if len(runs.Items) != 0 || len(events.Items) != 0 {
		t.Errorf("a draft test recorded %d sensor runs and %d events", len(runs.Items), len(events.Items))
	}

	d.assertNoSecret(s.ID, secretSensorValue, secretGlobalValue, "other-secret-value-6666")

	// Save is the normal sensor endpoint with the sensor's own hash.
	d.Put("/api/sensors/gh", map[string]any{"yaml": got.Draft.YAML, "expected_hash": before.Hash}).Want(200)
	if d.Read("sensors/gh.yaml") != string(spec.DropLegacyID([]byte(draft))) {
		t.Errorf("sensor file after Save = %q", d.Read("sensors/gh.yaml"))
	}
}

// A draft that does not validate cannot be tested (422 with the diagnostics); a key template that fails on an item
// is reported per item; a script that fails reports its exit code.
func TestAssistTestReportsKeyErrorsExitCodesAndInvalidDrafts(t *testing.T) {
	d := newAssistDaemon(t, func(d *Daemon) { d.assistRuntime(false, turn{Steps: []step{say("ok")}}) })
	s := d.assistCreate("")

	put := func(yaml string) assistDraftJSON {
		var dr assistDraftJSON
		d.Put("/api/assist/sessions/"+s.ID+"/draft", map[string]any{"yaml": yaml}).Want(200).JSON(&dr)
		return dr
	}
	if dr := put("id: [broken"); len(dr.Diagnostics) == 0 || dr.SensorID != nil {
		t.Errorf("broken draft: %+v", dr)
	}
	r := d.Post("/api/assist/sessions/"+s.ID+"/test", nil).Want(422)
	if r.Problem() != "validation-failed" {
		t.Errorf("problem = %q", r.Problem())
	}

	put("id: keys\ninterval: 5m\nlang: bash\nscript: |\n  echo '[{\"id\":1},{\"name\":\"x\"}]'\nkey: \"{{.id}}\"\nfirst_run: fire\n")
	var res struct {
		ItemCount int `json:"item_count"`
		Items     []struct {
			DedupKey *string `json:"dedup_key"`
			KeyError *string `json:"key_error"`
		} `json:"items"`
	}
	d.Post("/api/assist/sessions/"+s.ID+"/test", nil).Want(200).JSON(&res)
	if res.ItemCount != 2 || res.Items[0].DedupKey == nil || *res.Items[0].DedupKey != "1" || res.Items[1].DedupKey != nil || res.Items[1].KeyError == nil {
		t.Errorf("items = %+v", res)
	}

	put("id: fails\ninterval: 5m\nlang: bash\nscript: |\n  echo oops >&2\n  exit 3\nkey: \"{{.id}}\"\n")
	var failed struct {
		ExitCode  int `json:"exit_code"`
		ItemCount int `json:"item_count"`
		Stderr    struct {
			Text string `json:"text"`
		} `json:"stderr"`
	}
	d.Post("/api/assist/sessions/"+s.ID+"/test", nil).Want(200).JSON(&failed)
	if failed.ExitCode != 3 || failed.ItemCount != 0 || !strings.Contains(failed.Stderr.Text, "oops") {
		t.Errorf("failed run = %+v", failed)
	}

	put("id: bad-output\ninterval: 5m\nlang: bash\nscript: |\n  echo 'not json'\nkey: \"{{.id}}\"\n")
	var bad struct {
		ParseError *string `json:"parse_error"`
	}
	d.Post("/api/assist/sessions/"+s.ID+"/test", nil).Want(200).JSON(&bad)
	if bad.ParseError == nil {
		t.Error("output that is not JSON has no parse_error")
	}
}

// Everything the agent asks permission for besides hal's tools is rejected by the policy; a hal tool that needs the
// user (a test of a changed script) goes to the approval queue inline in the session: the tool call carries its approval,
// the session waits, a denied call is a tool error and the agent continues, an allowed one runs.
func TestAssistGatedCallsNeedApprovalAndDenyRejects(t *testing.T) {
	d := newAssistDaemon(t, func(d *Daemon) {
		d.assistRuntime(false, turn{When: when("risky$"), Steps: []step{
			say("Trying things."),
			editPermission("o1", "../outside.txt", "x\n", say("The edit outside was refused.")),
			execPermission("c1", "curl http://127.0.0.1:1/ping", []step{{Op: fakeagent.OpWriteFile, Path: "curl-ran.txt", Data: "yes"}}, say("curl refused")),
			assistEdit(scriptYAML(1, "5m")),
			mcpStep("t1", "draft_test", map[string]any{}, "r1.json"),
			mcpStep("t2", "draft_test", map[string]any{}, "r2.json"),
			say("All done."),
		}})
	})
	s := d.assistCreate("")
	d.assistSend(s.ID, "risky", nil)

	// 1. the first test of the new script
	ap := d.waitPendingApproval()
	if ap["type"] != "tool_call" || ap["assist_session_id"] != s.ID || ap["flow_id"] != nil {
		t.Fatalf("approval = %v", ap)
	}
	pr := ap["permission_request"].(map[string]any)
	if pr["tool_kind"] != "other" || pr["title"] != "hal tool: draft_test" || pr["matched_rule"] != "hal:approval" {
		t.Errorf("permission request = %v", pr)
	}
	waiting := d.assistWait(s.ID, "awaiting_approval")
	var linked *assistMsg
	for i := range waiting.Messages {
		if m := waiting.Messages[i]; m.ApprovalID != nil {
			linked = &waiting.Messages[i]
		}
	}
	if linked == nil || *linked.ApprovalID != ap["id"] || linked.Role != "tool" || !strings.HasSuffix(linked.ToolCall.Title, "draft_test") {
		t.Fatalf("no tool message carries approval %v: %+v", ap["id"], waiting.Messages)
	}
	d.Post("/api/approvals/"+ap["id"].(string)+"/deny", map[string]any{"comment": "not yet"}).Want(200)

	// 2. the same test again: asked again, allowed this time
	ap2 := d.waitPendingApproval()
	if ap2["id"] == ap["id"] {
		t.Fatalf("second approval = %v", ap2)
	}
	d.assistWait(s.ID, "awaiting_approval")
	d.Post("/api/approvals/"+ap2["id"].(string)+"/approve", map[string]any{}).Want(200)

	got := d.assistWait(s.ID, "idle", "failed")
	if got.Status != "idle" {
		t.Fatalf("status %q error %v", got.Status, got.Error)
	}
	for _, f := range []string{"../outside.txt", "curl-ran.txt", "r1.json"} {
		if fileExists(filepath.Join(d.assistDir(s.ID), f)) {
			t.Errorf("%s exists: a rejected or denied call ran", f)
		}
	}
	if !fileExists(filepath.Join(d.assistDir(s.ID), "r2.json")) {
		t.Error("the approved test did not run")
	}
	run := d.assistRuns(s.ID)[0]
	agentLayer := map[string]permJSON{}
	for _, p := range d.permissions(run.ID) {
		if !strings.HasPrefix(p.Title, "hal tool: ") {
			agentLayer[p.Title] = p
		}
	}
	if p := agentLayer["Write ../outside.txt"]; decisionOf(p) != "reject" || ruleOf(p) != "lockdown" {
		t.Errorf("native edit = %+v", p)
	}
	if p := agentLayer["curl http://127.0.0.1:1/ping"]; decisionOf(p) != "reject" || ruleOf(p) != "lockdown" {
		t.Errorf("native command = %+v", p)
	}
	tests := d.halPerms(run.ID, "draft_test")
	if len(tests) != 2 || decisionOf(tests[0]) != "reject" || *tests[0].DecidedBy != "user" || decisionOf(tests[1]) != "allow" || *tests[1].DecidedBy != "user" {
		t.Errorf("draft_test records = %+v", tests)
	}
	if tr := d.Transcript(run.ID); !strings.Contains(tr, "mcp[t1] is_error=true") || !strings.Contains(tr, "denied by the user: not yet") || !strings.Contains(tr, "mcp[t2] is_error=false") {
		t.Errorf("transcript lacks the tool error of the denial and the result of the allowed test:\n%s", tr)
	}
	last := got.Messages[len(got.Messages)-1]
	if last.text() != "All done." {
		t.Errorf("last message = %+v", last)
	}
}

// Deny and stop ends the turn; the session stays usable.
func TestAssistDenyAndStopEndsTheTurn(t *testing.T) {
	d := newAssistDaemon(t, func(d *Daemon) {
		d.assistRuntime(false,
			turn{When: when("risky$"), Steps: []step{assistEdit(scriptYAML(1, "5m")), mcpStep("c1", "draft_test", map[string]any{}, ""), say("unreachable")}},
			turn{When: when("calm$"), Steps: []step{say("fine")}},
		)
	})
	s := d.assistCreate("")
	d.assistSend(s.ID, "risky", nil)
	ap := d.waitPendingApproval()
	d.Post("/api/approvals/"+ap["id"].(string)+"/deny", map[string]any{"cancel_run": true}).Want(200)
	got := d.assistWait(s.ID, "idle", "failed")
	if got.Status != "idle" {
		t.Fatalf("status %q error %v", got.Status, got.Error)
	}
	if runs := d.assistRuns(s.ID); len(runs) != 1 || runs[0].Status != "denied" {
		t.Errorf("runs = %+v", runs)
	}
	var sawNote bool
	for _, m := range got.Messages {
		sawNote = sawNote || (m.Role == "system" && strings.Contains(m.text(), "denied"))
	}
	if !sawNote {
		t.Errorf("no system message about the denial: %+v", got.Messages)
	}
	if again := d.assistTurn(s.ID, "calm"); again.Status != "idle" || again.Messages[len(again.Messages)-1].text() != "fine" {
		t.Errorf("session after deny and stop: %+v", again)
	}
}

func countCalls(d *Daemon, method string) int {
	n := 0
	for _, c := range d.AgentCalls("fake") {
		if c["method"] == method {
			n++
		}
	}
	return n
}

// waitAgentSuspended waits until the agent process of the latest initialize call has exited: the idle timeout
// suspended it.
func waitAgentSuspended(d *Daemon) {
	d.t.Helper()
	pid := 0
	for _, c := range d.AgentCalls("fake") {
		if c["method"] == "initialize" {
			pid = int(c["pid"].(float64))
		}
	}
	if pid == 0 {
		d.t.Fatal("no initialize call with a pid in the agent call log")
	}
	d.Eventually("the idle agent process to be suspended", func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH })
}

// The agent process is kept between turns; after the idle timeout it is suspended and the next message resumes the
// same ACP session with session/load.
func TestAssistKeepsTheProcessThenResumesWithSessionLoad(t *testing.T) {
	d := newAssistDaemon(t, func(d *Daemon) {
		d.AssistIdle = 250 * time.Millisecond
		d.assistRuntime(true,
			turn{When: when("first$"), Steps: []step{say("one")}},
			turn{When: when("second$"), Steps: []step{say("two")}},
			turn{When: when("third$"), Steps: []step{say("three")}},
		)
	})
	s := d.assistCreate("")
	d.assistTurn(s.ID, "first")
	d.assistTurn(s.ID, "second")
	if n := countCalls(d, "initialize"); n != 1 {
		t.Fatalf("%d agent processes served two quick turns, want 1", n)
	}

	waitAgentSuspended(d)
	got := d.assistTurn(s.ID, "third")
	if got.Status != "idle" || got.Messages[len(got.Messages)-1].text() != "three" {
		t.Fatalf("after idle: %+v", got)
	}
	if n := countCalls(d, "initialize"); n != 2 {
		t.Errorf("%d initialize calls, want 2 (one restart)", n)
	}
	if n := countCalls(d, "session/load"); n != 1 {
		t.Errorf("%d session/load calls, want 1", n)
	}
	runs := d.assistRuns(s.ID)
	if len(runs) != 3 || runs[0].SessionID == nil || runs[2].SessionID == nil || *runs[0].SessionID != *runs[2].SessionID {
		t.Fatalf("runs = %+v", runs)
	}
	if runs[1].ResumedVia != nil || runs[2].ResumedVia == nil || *runs[2].ResumedVia != "load" {
		t.Errorf("resumed_via: second turn %v, third turn %v", runs[1].ResumedVia, runs[2].ResumedVia)
	}
	for _, c := range d.AgentCalls("fake") {
		if c["method"] == "session/load" && c["session_id"] != *runs[0].SessionID {
			t.Errorf("session/load for %v, want %s", c["session_id"], *runs[0].SessionID)
		}
	}
}

// An agent that cannot load sessions gets a new one with a recap of the conversation, recorded on the run.
func TestAssistResumeFallsBackToANewSessionWithARecap(t *testing.T) {
	d := newAssistDaemon(t, func(d *Daemon) {
		d.AssistIdle = 250 * time.Millisecond
		d.assistRuntime(false,
			turn{When: when("first message$"), Steps: []step{say("one")}},
			turn{When: when("third message$"), Steps: []step{say("three")}},
		)
	})
	s := d.assistCreate("")
	d.assistTurn(s.ID, "first message")
	waitAgentSuspended(d)
	got := d.assistTurn(s.ID, "third message")
	if got.Status != "idle" {
		t.Fatalf("status %q error %v", got.Status, got.Error)
	}
	runs := d.assistRuns(s.ID)
	if len(runs) != 2 || runs[1].ResumedVia == nil || *runs[1].ResumedVia != "new_session" || runs[1].ResumeFallback == nil {
		t.Fatalf("runs = %+v", runs)
	}
	if !strings.Contains(runs[1].RenderedPrompt, "first message") || !strings.Contains(runs[1].RenderedPrompt, "one") || !strings.HasSuffix(runs[1].RenderedPrompt, "third message") {
		t.Errorf("fallback prompt = %q", runs[1].RenderedPrompt)
	}
	if n := countCalls(d, "session/new"); n != 2 {
		t.Errorf("%d session/new calls, want 2", n)
	}
}

// The editor's content reaches the agent: PUT draft (guarded by the hash) and `yaml` on a message both replace the
// draft before the agent looks at the file.
func TestAssistUserEditsReachTheAgent(t *testing.T) {
	d := newAssistDaemon(t, func(d *Daemon) {
		d.assistRuntime(false, turn{Steps: []step{mcpStep("r", "draft_read", map[string]any{}, "seen.json"), say("read it")}})
	})
	s := d.assistCreate("")

	mine := "id: mine\ninterval: 2m\nlang: bash\nscript: |\n  echo '[]'\nkey: \"{{.id}}\"\n"
	r := d.Put("/api/assist/sessions/"+s.ID+"/draft", map[string]any{"yaml": mine, "expected_hash": strings.Repeat("0", 64)}).Want(409)
	var conflict struct {
		CurrentHash string `json:"current_hash"`
	}
	r.JSON(&conflict)
	if r.Problem() != "conflict" || conflict.CurrentHash != s.Draft.Hash {
		t.Errorf("conflict = %s", r.Body)
	}
	var dr assistDraftJSON
	d.Put("/api/assist/sessions/"+s.ID+"/draft", map[string]any{"yaml": mine, "expected_hash": s.Draft.Hash}).Want(200).JSON(&dr)
	if dr.YAML != mine || dr.UpdatedBy != "user" || dr.Hash == s.Draft.Hash || dr.SensorID == nil || *dr.SensorID != "mine" {
		t.Errorf("draft = %+v", dr)
	}
	if b, _ := os.ReadFile(filepath.Join(d.assistDir(s.ID), "sensor.yaml")); string(b) != mine {
		t.Errorf("draft file = %q", b)
	}

	// yaml on the message wins over the stored draft.
	newer := strings.Replace(mine, "2m", "3m", 1)
	d.assistSend(s.ID, "look at it", &newer)
	got := d.assistWait(s.ID, "idle", "failed")
	if got.Status != "idle" {
		t.Fatalf("status %q error %v", got.Status, got.Error)
	}
	var seen struct {
		YAML string `json:"yaml"`
	}
	if b, err := os.ReadFile(filepath.Join(d.assistDir(s.ID), "seen.json")); err != nil || json.Unmarshal(b, &seen) != nil || seen.YAML != newer {
		t.Errorf("the agent saw %q (%v), want the editor's content", b, err)
	}
	if got.Draft.YAML != newer || got.Draft.UpdatedBy != "user" {
		t.Errorf("draft = %+v", got.Draft)
	}
	if got.Messages[0].text() != "look at it" {
		t.Errorf("user message = %q, want it stored as sent", got.Messages[0].text())
	}
}

// Cancel ends the running turn; the agent process and the session stay usable.
func TestAssistCancelEndsTheTurnAndKeepsTheSession(t *testing.T) {
	d := newAssistDaemon(t, func(d *Daemon) {
		d.assistRuntime(false,
			turn{When: when("hang$"), Steps: []step{say("working"), {Op: fakeagent.OpHang}}},
			turn{When: when("again$"), Steps: []step{say("back")}},
		)
	})
	s := d.assistCreate("")
	d.assistSend(s.ID, "hang", nil)
	d.Eventually("the agent to start the turn", func() bool { return countCalls(d, "session/prompt") == 1 })
	if got := d.assistGet(s.ID); got.Status != "running" {
		t.Fatalf("status = %q while the agent works", got.Status)
	}
	var c assistDetail
	d.Post("/api/assist/sessions/"+s.ID+"/cancel", nil).Want(200).JSON(&c)
	if c.Status != "idle" || c.Error != nil {
		t.Errorf("after cancel: %+v", c)
	}
	runs := d.assistRuns(s.ID)
	if len(runs) != 1 || runs[0].Status != "killed" || runs[0].Error == nil || !strings.Contains(*runs[0].Error, "cancel") {
		t.Errorf("runs = %+v", runs)
	}
	var noted bool
	for _, m := range d.assistGet(s.ID).Messages {
		noted = noted || (m.Role == "system" && strings.Contains(m.text(), "cancelled"))
	}
	if !noted {
		t.Error("no system message about the cancel")
	}
	// Without a running turn, cancel changes nothing.
	d.Post("/api/assist/sessions/"+s.ID+"/cancel", nil).Want(200)

	if got := d.assistTurn(s.ID, "again"); got.Status != "idle" || got.Messages[len(got.Messages)-1].text() != "back" {
		t.Errorf("next turn: %+v", got)
	}
	if n := countCalls(d, "initialize"); n != 1 {
		t.Errorf("%d agent processes, want the same one after a cancel", n)
	}
	// A second message while the first turn runs is refused.
	d2 := d.assistCreate("")
	d.assistSend(d2.ID, "hang", nil)
	d.Post("/api/assist/sessions/"+d2.ID+"/messages", map[string]any{"text": "again"}).Want(409)
	d.Post("/api/assist/sessions/"+d2.ID+"/cancel", nil).Want(200)
}

// Deleting a session stops the agent (its whole process group), removes the draft directory and ends the session.
func TestAssistDeleteRemovesTheDraftDirectoryAndStopsTheAgent(t *testing.T) {
	d := newAssistDaemon(t, func(d *Daemon) {
		d.assistRuntime(false, turn{Steps: []step{
			{Op: fakeagent.OpSpawnChild, Cmd: []string{"sleep", "300"}, PidFile: "child.pid"},
			say("started"),
		}})
	})
	s := d.assistCreate("")
	d.assistTurn(s.ID, "go")
	pidText, err := os.ReadFile(filepath.Join(d.assistDir(s.ID), "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(string(pidText))
	if syscall.Kill(pid, 0) != nil {
		t.Fatalf("child %d is not running", pid)
	}

	d.Delete("/api/assist/sessions/" + s.ID).Want(204)
	if _, err := os.Stat(d.assistDir(s.ID)); !os.IsNotExist(err) {
		t.Errorf("draft directory survives the delete: %v", err)
	}
	d.Eventually("the agent's process group to be gone", func() bool { return syscall.Kill(pid, 0) != nil })
	if got := d.assistGet(s.ID); got.Status != "closed" {
		t.Errorf("status = %q, want closed", got.Status)
	}
	var l struct {
		Items []assistDetail `json:"items"`
	}
	d.Get("/api/assist/sessions").Want(200).JSON(&l)
	for _, it := range l.Items {
		if it.ID == s.ID {
			t.Error("a closed session is listed")
		}
	}
	d.Post("/api/assist/sessions/"+s.ID+"/messages", map[string]any{"text": "hi"}).Want(409)
	d.Put("/api/assist/sessions/"+s.ID+"/draft", map[string]any{"yaml": "id: x\n"}).Want(409)
	d.Delete("/api/assist/sessions/" + s.ID).Want(204) // idempotent
	d.Delete("/api/assist/sessions/01JC7Z3K9Q8V5T2M4N6P8R0S1T").Want(404)
}

// The kill switch stops sensors, flow runs and flow agent nodes, not the chat assistant: turns and draft tests run
// while it is engaged, and "pause and kill running" leaves a running turn alone (Stop and the daemon's shutdown end it).
func TestAssistIsNotAffectedByThePause(t *testing.T) {
	d := newAssistDaemon(t, func(d *Daemon) {
		d.assistRuntime(false,
			turn{When: &fakeagent.When{PromptMatches: "go on"}, Steps: []step{say("working"), {Op: fakeagent.OpHang}}},
			turn{Steps: []step{say("ok")}})
	})
	s := d.assistCreate("")
	d.Post("/api/killswitch/pause", map[string]any{"kill_running": true}).Want(200)
	d.Post("/api/assist/sessions/"+s.ID+"/test", nil).Want(200) // the draft skeleton runs like a poll
	if got := d.assistTurn(s.ID, "hi"); got.Status != "idle" {
		t.Errorf("turn while paused: %+v", got)
	}
	d.assistSend(s.ID, "go on", nil)
	d.Eventually("the agent to start the second turn", func() bool { return countCalls(d, "session/prompt") == 2 })
	d.Post("/api/killswitch/pause", map[string]any{"kill_running": true}).Want(200) // again, with the turn running
	time.Sleep(500 * time.Millisecond)
	if got := d.assistGet(s.ID); got.Status != "running" {
		t.Errorf("a running turn must survive pause and kill: %+v", got)
	}
	d.Post("/api/assist/sessions/"+s.ID+"/cancel", nil).Want(200)
	if got := d.assistWait(s.ID, "idle", "failed"); got.Status != "idle" {
		t.Errorf("after Stop: %+v", got)
	}
}

// A request for an unknown runtime or sensor is refused up front, and an agent edit that changes the id of an
// existing sensor is flagged in the draft's diagnostics.
func TestAssistCreateValidationAndIdMismatch(t *testing.T) {
	const gh = "id: gh\ninterval: 1h\nlang: bash\nscript: \"true\"\nkey: \"{{.id}}\"\n"
	d := newAssistDaemon(t, func(d *Daemon) {
		d.Sensor("gh", gh)
		d.assistRuntime(false, turn{Steps: []step{assistEdit(strings.Replace(gh, "id: gh", "id: renamed", 1)), say("renamed")}})
	})
	d.Post("/api/assist/sessions", map[string]any{"target": map[string]any{"kind": "sensor"}, "runtime": "nope"}).Want(422)
	d.Post("/api/assist/sessions", map[string]any{"target": map[string]any{"kind": "sensor", "id": "missing"}, "runtime": "fake"}).Want(404)

	s := d.assistCreate("gh")
	got := d.assistTurn(s.ID, "rename it")
	var warned bool
	for _, dg := range got.Draft.Diagnostics {
		warned = warned || (dg["code"] == "id_mismatch" && dg["severity"] == "warning")
	}
	if !warned {
		t.Errorf("no id_mismatch warning: %v", got.Draft.Diagnostics)
	}
	var list struct {
		Items []assistDetail `json:"items"`
	}
	d.Get("/api/assist/sessions?target_kind=sensor&target_id=gh").Want(200).JSON(&list)
	if len(list.Items) != 1 || list.Items[0].ID != s.ID {
		t.Errorf("sessions of gh = %+v", list.Items)
	}
}

// A daemon restart leaves the conversation and the draft; a turn that was running is reported failed, and the next
// message resumes the agent session.
func TestAssistSessionSurvivesARestart(t *testing.T) {
	d := newAssistDaemon(t, func(d *Daemon) {
		d.assistRuntime(true,
			turn{When: when("start$"), Steps: []step{assistEdit(emitTwoYAML), say("written")}},
			turn{When: when("hang$"), Steps: []step{say("working"), {Op: fakeagent.OpHang, IgnoreCancel: false}}},
			turn{When: when("after$"), Steps: []step{say("resumed")}},
		)
	})
	s := d.assistCreate("")
	d.assistTurn(s.ID, "start")
	d.assistSend(s.ID, "hang", nil)
	d.Eventually("the agent to start the second turn", func() bool { return countCalls(d, "session/prompt") == 2 })

	orphan := "data/assist/01JC7Z3K9Q8V5T2M4N6P8R0S1T" // a draft directory no session owns (left by a crash)
	d.Write(orphan+"/sensor.yaml", "id: orphan\n")
	d.Restart()
	if _, err := os.Stat(d.Path(orphan)); !os.IsNotExist(err) {
		t.Errorf("the orphan draft directory survives a restart: %v", err)
	}
	if _, err := os.Stat(filepath.Join(d.assistDir(s.ID), "sensor.yaml")); err != nil {
		t.Errorf("the session's own draft directory was removed: %v", err)
	}
	got := d.assistGet(s.ID)
	if got.Draft.YAML != emitTwoYAML || len(got.Messages) < 4 {
		t.Fatalf("after restart: draft %q, %d messages", got.Draft.YAML, len(got.Messages))
	}
	if got.Status == "running" {
		t.Errorf("status %q after a restart interrupted the turn", got.Status)
	}
	again := d.assistTurn(s.ID, "after")
	if again.Status != "idle" || again.Messages[len(again.Messages)-1].text() != "resumed" {
		t.Errorf("after the restart: %+v", again)
	}
	var loaded bool
	for _, c := range d.AgentCalls("fake") {
		loaded = loaded || c["method"] == "session/load"
	}
	if !loaded {
		t.Error("the new daemon did not resume the agent session with session/load")
	}
}

// ---- a test runs the script with real credentials: a changed script needs the user ----

// scriptYAML is a sensor whose script prints one item carrying n; interval varies without changing the script.
func scriptYAML(n int, interval string) string {
	return fmt.Sprintf("id: probe\ninterval: %s\nlang: bash\nscript: |\n  echo '[{\"id\":%d}]'\nkey: \"{{.id}}\"\nfirst_run: fire\ntimeout: 10s\n", interval, n)
}

// agentTest is the agent calling draft_test; the result lands in file (written only when it ran).
func agentTest(id, file string) step { return mcpStep(id, "draft_test", map[string]any{}, file) }

// toolContents returns the content items of the tool message that carries the approval.
func (d *Daemon) toolContents(sessionID, approvalID string) []map[string]any {
	d.t.Helper()
	var det struct {
		Messages []struct {
			ApprovalID *string `json:"approval_id"`
			ToolCall   *struct {
				Content []map[string]any `json:"content"`
			} `json:"tool_call"`
		} `json:"messages"`
	}
	d.Get("/api/assist/sessions/" + sessionID).Want(200).JSON(&det)
	for _, m := range det.Messages {
		if m.ApprovalID != nil && *m.ApprovalID == approvalID && m.ToolCall != nil {
			return m.ToolCall.Content
		}
	}
	d.t.Fatalf("no tool message carries approval %s", approvalID)
	return nil
}

// The agent's test of a script the user has not seen is a tool_call approval whose description shows the script (and
// a diff against the last approved version); allowing it records that script as approved, so the same script runs
// again without asking, a changed one asks again, and a denial approves nothing.
func TestAssistTestOfAChangedScriptNeedsApprovalOnce(t *testing.T) {
	s1, s2 := scriptYAML(1, "5m"), scriptYAML(2, "5m")
	d := newAssistDaemon(t, func(d *Daemon) {
		d.assistRuntime(false,
			turn{When: when("first$"), Steps: []step{assistEdit(s1), agentTest("t1", "r1.json"), say("tested 1")}},
			turn{When: when("second$"), Steps: []step{agentTest("t2", "r2.json"), say("tested 1 again")}},
			turn{When: when("third$"), Steps: []step{assistEdit(s2), agentTest("t3", "r3.json"), say("tested 2")}},
			turn{When: when("fourth$"), Steps: []step{agentTest("t4", "r4.json"), say("tested 2 again")}},
			turn{When: when("fifth$"), Steps: []step{assistEdit(scriptYAML(2, "9m")), agentTest("t5", "r5.json"), say("only the interval changed")}},
		)
	})
	s := d.assistCreate("")
	dir := d.assistDir(s.ID)

	// 1. A new script: the test asks, and does not run until allowed.
	d.assistSend(s.ID, "first", nil)
	ap := d.waitPendingApproval()
	pr := ap["permission_request"].(map[string]any)
	if ap["assist_session_id"] != s.ID || pr["title"] != "hal tool: draft_test" || pr["matched_rule"] != "hal:approval" || !strings.Contains(fmt.Sprint(pr["reason"]), "changed since the user last tested or approved") {
		t.Fatalf("approval = %v", ap)
	}
	d.assistWait(s.ID, "awaiting_approval")
	if fileExists(filepath.Join(dir, "r1.json")) {
		t.Fatal("the test ran before it was allowed")
	}
	content := d.toolContents(s.ID, ap["id"].(string))
	var sawText, sawDiff bool
	for _, c := range content {
		switch c["type"] {
		case "content":
			sawText = strings.Contains(fmt.Sprint(c["content"]), "real credentials")
		case "diff":
			sawDiff = c["oldText"] == "" && strings.Contains(fmt.Sprint(c["newText"]), `echo '[{"id":1}]'`) && strings.Contains(fmt.Sprint(c["newText"]), "# lang: bash")
		}
	}
	if !sawText || !sawDiff {
		t.Errorf("the approval does not show the script: %v", content)
	}
	d.Post("/api/approvals/"+ap["id"].(string)+"/approve", map[string]any{}).Want(200)
	if got := d.assistWait(s.ID, "idle", "failed"); got.Status != "idle" {
		t.Fatalf("status %q error %v", got.Status, got.Error)
	}
	if !fileExists(filepath.Join(dir, "r1.json")) {
		t.Fatal("the allowed test did not run")
	}

	// 2. The same script again: no approval.
	if got := d.assistTurn(s.ID, "second"); got.Status != "idle" {
		t.Fatalf("status %q error %v (a test of the approved script must not wait for anyone)", got.Status, got.Error)
	}
	if !fileExists(filepath.Join(dir, "r2.json")) {
		t.Error("the test of the approved script did not run")
	}
	runs := d.assistRuns(s.ID)
	if p := d.halPerms(runs[1].ID, "draft_test"); len(p) != 1 || decisionOf(p[0]) != "allow" || *p[0].DecidedBy != "policy" || ruleOf(p[0]) != "hal:builtin" {
		t.Errorf("second test permission = %+v", p)
	}

	// 3. A changed script asks again, with a diff against the approved one; a denial approves nothing.
	d.assistSend(s.ID, "third", nil)
	ap = d.waitPendingApproval()
	var diffOld, diffNew string
	for _, c := range d.toolContents(s.ID, ap["id"].(string)) {
		if c["type"] == "diff" {
			diffOld, diffNew = fmt.Sprint(c["oldText"]), fmt.Sprint(c["newText"])
		}
	}
	if !strings.Contains(diffOld, `"id":1`) || !strings.Contains(diffNew, `"id":2`) {
		t.Errorf("diff = %q -> %q, want the approved script against the new one", diffOld, diffNew)
	}
	d.Post("/api/approvals/"+ap["id"].(string)+"/deny", map[string]any{}).Want(200)
	d.assistWait(s.ID, "idle", "failed")
	if fileExists(filepath.Join(dir, "r3.json")) {
		t.Error("a denied test ran")
	}
	// Denied means not approved: the same script asks again, and allowing it makes it run unasked afterwards.
	d.assistSend(s.ID, "fourth", nil)
	ap = d.waitPendingApproval()
	d.Post("/api/approvals/"+ap["id"].(string)+"/approve", map[string]any{}).Want(200)
	d.assistWait(s.ID, "idle", "failed")
	if !fileExists(filepath.Join(dir, "r4.json")) {
		t.Error("the test allowed the second time did not run")
	}

	// 4. Changing only what a test does not execute (the interval) does not ask.
	if got := d.assistTurn(s.ID, "fifth"); got.Status != "idle" || !fileExists(filepath.Join(dir, "r5.json")) {
		t.Errorf("a change of the interval alone asked for approval or failed: status %q", got.Status)
	}
}

// The user's own test always runs and approves the script; the daemon refuses the agent's test of any other script
// even if the agent got past the permission request, and a credential the script mentions that appears later changes
// what the approval covered.
func TestAssistUserTestApprovesTheScriptAndTheDaemonEnforcesIt(t *testing.T) {
	const withKey = "id: probe\ninterval: 5m\nlang: bash\nscript: |\n  echo \"[{\\\"id\\\":1,\\\"k\\\":\\\"$LATER_KEY\\\"}]\"\nkey: \"{{.id}}\"\nfirst_run: fire\n"
	d := newAssistDaemon(t, func(d *Daemon) {
		d.Sensor("probe", "id: probe\ninterval: 1h\nlang: bash\nscript: \"true\"\nkey: \"{{.id}}\"\n")
		d.assistRuntime(false,
			turn{When: when("write$"), Steps: []step{assistEdit(withKey), say("written")}},
			turn{When: when("run$"), Steps: []step{agentTest("t1", "r1.json"), say("ran")}},
		)
	})
	agent := withHeader("X-Hal9001-Assist-Agent", "1")
	s := d.assistCreate("probe")
	test := "/api/assist/sessions/" + s.ID + "/test"

	// The saved script of the sensor being edited is trusted; the agent's rewrite is not.
	d.Post(test, nil, agent).Want(200)
	d.assistTurn(s.ID, "write")
	r := d.Post(test, nil, agent).Want(409)
	if r.Problem() != "invalid-state" || !strings.Contains(string(r.Body), "approval") {
		t.Errorf("agent test of an unapproved script = %s", r.Body)
	}

	// The user's test runs, approves the script, and from then on the agent's test of it runs.
	d.Post(test, nil).Want(200)
	d.Post(test, nil, agent).Want(200)
	if got := d.assistTurn(s.ID, "run"); got.Status != "idle" || !fileExists(filepath.Join(d.assistDir(s.ID), "r1.json")) {
		t.Errorf("the agent's test of the user-tested script: status %q", got.Status)
	}
	if p := d.halPerms(d.assistRuns(s.ID)[1].ID, "draft_test"); len(p) != 1 || *p[0].DecidedBy != "policy" {
		t.Errorf("permissions = %+v, want the test allowed without asking", p)
	}

	// The script mentions $LATER_KEY. Once that credential exists, a test would use it: what the user approved no
	// longer covers it, for the policy and for the daemon.
	d.Put("/api/sensors/probe/env/LATER_KEY", map[string]any{"value": "later-key-value-7777"}).Want(204)
	d.Post(test, nil, agent).Want(409)
	d.assistSend(s.ID, "run", nil)
	ap := d.waitPendingApproval()
	if !strings.Contains(fmt.Sprint(d.toolContents(s.ID, ap["id"].(string))), "LATER_KEY") {
		t.Errorf("the approval does not name the credential the script now uses")
	}
	d.Post("/api/approvals/"+ap["id"].(string)+"/deny", map[string]any{"cancel_run": true}).Want(200)
	d.assistWait(s.ID, "idle", "failed")
	d.assertNoSecret(s.ID, "later-key-value-7777")
}

// A session on an existing sensor may test the sensor's own saved script without asking; changing it asks, with the
// saved script as the base of the diff.
func TestAssistExistingSensorsSavedScriptNeedsNoApproval(t *testing.T) {
	const saved = "id: gh\ninterval: 1h\nlang: bash\nscript: |\n  echo '[{\"id\":7}]'\nkey: \"{{.id}}\"\nfirst_run: fire\n"
	changed := strings.Replace(saved, `"id":7`, `"id":8`, 1)
	d := newAssistDaemon(t, func(d *Daemon) {
		d.Sensor("gh", saved)
		d.assistRuntime(false,
			turn{When: when("same$"), Steps: []step{agentTest("t1", "r1.json"), say("tested the saved script")}},
			turn{When: when("change$"), Steps: []step{assistEdit(changed), agentTest("t2", "r2.json"), say("tested the change")}},
		)
	})
	s := d.assistCreate("gh")
	if got := d.assistTurn(s.ID, "same"); got.Status != "idle" || !fileExists(filepath.Join(d.assistDir(s.ID), "r1.json")) {
		t.Fatalf("the saved script needed approval: status %q", got.Status)
	}
	d.assistSend(s.ID, "change", nil)
	ap := d.waitPendingApproval()
	var base string
	for _, c := range d.toolContents(s.ID, ap["id"].(string)) {
		if c["type"] == "diff" {
			base = fmt.Sprint(c["oldText"])
		}
	}
	if !strings.Contains(base, `"id":7`) {
		t.Errorf("the diff is not against the saved script: %q", base)
	}
	d.Post("/api/approvals/"+ap["id"].(string)+"/approve", map[string]any{}).Want(200)
	d.assistWait(s.ID, "idle", "failed")
}

// Saving a new sensor that a session authored makes the session a session on that sensor: it is listed under it, its
// instructions name the sensor's own env keys and its tests use their values. A session whose draft has another id,
// and one that was already on a sensor, are left alone.
func TestAssistSessionOfANewSensorBecomesTheSensorsSessionOnceItIsCreated(t *testing.T) {
	const other = "id: elsewhere\ninterval: 1h\nlang: bash\nscript: \"true\"\nkey: \"{{.id}}\"\n"
	d := newAssistDaemon(t, func(d *Daemon) {
		d.assistRuntime(false, turn{When: when("write it$"), Steps: []step{assistEdit(emitTwoYAML), mcpStep("i", "draft_read", map[string]any{"path": "instructions"}, "instructions.txt"), say("written")}})
	})
	authored := d.assistCreate("")
	unrelated := d.assistCreate("")
	d.Put("/api/assist/sessions/"+unrelated.ID+"/draft", map[string]any{"yaml": other}).Want(200)
	d.assistTurn(authored.ID, "write it")

	d.Post("/api/sensors", map[string]any{"yaml": emitTwoYAML}).Want(201)

	if got := d.assistGet(authored.ID); got.Target.ID == nil || *got.Target.ID != "emit-two" {
		t.Fatalf("target after the sensor was created = %+v", got.Target)
	}
	if got := d.assistGet(unrelated.ID); got.Target.ID != nil {
		t.Errorf("a session with another draft id was attached to %q", *got.Target.ID)
	}
	var list struct {
		Items []assistDetail `json:"items"`
	}
	d.Get("/api/assist/sessions?target_kind=sensor&target_id=emit-two").Want(200).JSON(&list)
	if len(list.Items) != 1 || list.Items[0].ID != authored.ID {
		t.Errorf("sessions of emit-two = %+v", list.Items)
	}

	// The sensor's own env keys now apply to the session.
	d.Put("/api/sensors/emit-two/env/EMIT_TOKEN", map[string]any{"value": secretSensorValue}).Want(204)
	d.assistTurn(authored.ID, "write it") // the agent reads fresh instructions
	instr := d.Read("data/assist/" + authored.ID + "/instructions.txt")
	if !strings.Contains(instr, "`EMIT_TOKEN`") {
		t.Errorf("the instructions do not list the sensor's env key:\n%s", instr)
	}
}

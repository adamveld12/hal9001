package assist

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"hal9001/docs"
	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// Directories of a flow session's draft directory.
const (
	// PromptsDir holds the new prompt files the agent drafts (<name>.md). Save creates those that do not exist yet.
	PromptsDir = "prompts"
	// ReferenceDir is read-only material for the agent: sensors, existing prompts, runtime names, samples, docs.
	ReferenceDir = "reference"

	samplesPerSensor = 5
	maxSampleBytes   = 64 << 10
	maxPromptBytes   = 64 << 10
)

// flowSkeleton is the draft of a new flow when the request carries no YAML.
const flowSkeleton = `# New flow. Describe what it should do and the assistant fills this in.
id: new-flow
description: What this flow does, in one line.
enabled: false
dry_run: true
subscribe: []
nodes: []
edges: []
`

func draftFileOf(kind string) string {
	switch kind {
	case store.AssistTargetFlow:
		return FlowDraftFile
	case store.AssistTargetTool:
		return ToolDraftFile
	}
	return SensorDraftFile
}

// draftPrompt is one prompt file of the draft directory.
type draftPrompt struct {
	Name   string `json:"name"`
	YAML   string `json:"yaml"`
	Exists bool   `json:"exists"`
}

// readDraftPrompts lists the prompts/*.md files of a draft directory by name.
func readDraftPrompts(dir string) []draftPrompt {
	entries, err := os.ReadDir(filepath.Join(dir, PromptsDir))
	if err != nil {
		return nil
	}
	var out []draftPrompt
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".md")
		if !ok || !e.Type().IsRegular() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, PromptsDir, e.Name())) //nolint:gosec // inside the session's draft directory
		if err != nil || len(b) > maxPromptBytes {
			continue
		}
		out = append(out, draftPrompt{Name: name, YAML: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// evaluateFlow validates a flow draft like a flow file, with the draft's own prompts counting as existing. A
// changed id of an existing flow is flagged. Prompt files are validated too; a draft prompt named like a saved
// one is flagged because Save never overwrites it.
func (m *Manager) evaluateFlow(yaml, target, dir string) (flowID string, diagnostics, prompts, sensors json.RawMessage) {
	_, flowID, ds, files, sens := m.checkFlow(yaml, target, dir)
	if files == nil {
		files = []draftPrompt{}
	}
	if sens == nil {
		sens = []draftSensor{}
	}
	pb, err := json.Marshal(files)
	if err != nil {
		pb = []byte("[]")
	}
	sb, err := json.Marshal(sens)
	if err != nil {
		sb = []byte("[]")
	}
	return flowID, marshalDiagnostics(ds), pb, sb
}

// checkFlow is evaluateFlow's work: the parsed flow (nil when it does not parse), its id, every diagnostic, the
// draft prompts and the draft sensors (which count as existing sensors for the flow).
func (m *Manager) checkFlow(yaml, target, dir string) (flow *spec.Flow, flowID string, ds []spec.Diagnostic, files []draftPrompt, sensors []draftSensor) {
	ws := spec.Load(m.home)
	refs := ws.Refs()
	files = readDraftPrompts(dir)
	for i := range files {
		saved := ws.Prompt(files[i].Name)
		files[i].Exists = saved != nil
		clash := saved != nil && string(saved.Raw) != string(spec.DropLegacyName([]byte(files[i].YAML))) // a prompt Save just created is not a clash
		refs.Prompts[files[i].Name] = true
		path := PromptsDir + "/" + files[i].Name + ".md"
		if !spec.ValidID(files[i].Name) {
			ds = append(ds, spec.Diagnostic{File: path, Severity: spec.SeverityError, Code: spec.CodeInvalidID,
				Message: fmt.Sprintf("prompt name %q is invalid (letters, digits, - and _, at most 64 characters)", files[i].Name)})
			continue
		}
		_, pds := spec.CheckPrompt(files[i].Name, []byte(files[i].YAML))
		ds = append(ds, pds...)
		if clash {
			ds = append(ds, spec.Diagnostic{File: path, Severity: spec.SeverityWarning, Code: spec.CodeIDMismatch,
				Message: fmt.Sprintf("a prompt named %q is already saved and Save will not overwrite it: use a new name", files[i].Name)})
		}
	}
	sensors, sds := checkDraftSensors(ws, dir, refs)
	flow, fds := spec.CheckFlow(target, []byte(yaml), refs)
	for i := range fds {
		fds[i].File = FlowDraftFile
	}
	ds = append(fds, ds...)
	ds = append(ds, sds...)
	if flow != nil {
		if spec.ValidID(flow.ID) {
			flowID = flow.ID
		}
	}
	return flow, flowID, ds, files, sensors
}

// writeReference rebuilds <dir>/reference: sensor definitions, existing prompts, runtime names, the samples and
// the format documentation. The files are read-only (owner read-only) and the agent's policy rejects edits there.
func (m *Manager) writeReference(dir string) error {
	ref := filepath.Join(dir, ReferenceDir)
	if err := os.RemoveAll(ref); err != nil {
		return fmt.Errorf("assist: reset reference: %w", err)
	}
	for _, sub := range []string{"sensors", "prompts", "samples", "docs"} {
		if err := os.MkdirAll(filepath.Join(ref, sub), 0o700); err != nil {
			return fmt.Errorf("assist: create reference: %w", err)
		}
	}
	put := func(rel string, data []byte) error {
		p := filepath.Join(ref, rel)
		if err := os.WriteFile(p, data, 0o400); err != nil {
			return fmt.Errorf("assist: write reference %s: %w", rel, err)
		}
		return nil
	}
	ws := spec.Load(m.home)
	var sensors []string
	for _, s := range ws.Sensors {
		sensors = append(sensors, s.Name)
		if err := put("sensors/"+s.Name+".yaml", s.Raw); err != nil {
			return err
		}
	}
	for _, p := range ws.Prompts {
		if err := put("prompts/"+p.Name+".md", p.Raw); err != nil {
			return err
		}
	}
	var runtimes []string
	if ws.Config.Value != nil {
		for name := range ws.Config.Value.Runtimes {
			runtimes = append(runtimes, name)
		}
	}
	sort.Strings(runtimes)
	if err := put("runtimes.txt", []byte(strings.Join(runtimes, "\n")+"\n")); err != nil {
		return err
	}
	if err := put("docs/flows.md", []byte(docs.Flows)); err != nil {
		return err
	}
	sort.Strings(sensors)
	var index strings.Builder
	index.WriteString("# index of the samples: draft_test with `sample` N runs the draft flow on sample N.\n")
	n := 0
	for _, id := range sensors {
		lines, err := m.recentPayloads(id)
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			continue
		}
		if err := put("samples/"+id+".jsonl", []byte(strings.Join(lines, "\n")+"\n")); err != nil {
			return err
		}
		for _, l := range lines {
			fmt.Fprintf(&index, "%d\t%s\t%s\n", n, id, cutUTF8(l, 100))
			n++
		}
	}
	return put("samples/index.txt", []byte(index.String()))
}

// recentPayloads returns up to samplesPerSensor recent event payloads of a sensor, redacted, one JSON value per line.
func (m *Manager) recentPayloads(sensorID string) ([]string, error) {
	page, err := m.st.ListEvents(m.ctx, store.EventFilter{SensorID: sensorID, ListOpts: store.ListOpts{Limit: samplesPerSensor}})
	if err != nil {
		return nil, fmt.Errorf("assist: list events of %s: %w", sensorID, err)
	}
	var out []string
	for _, ev := range page.Items {
		b, err := m.st.ReadContent(ev.Payload)
		if err != nil || len(b) > maxSampleBytes {
			continue
		}
		b = m.redactBytes(bytes.TrimSpace(b))
		var buf bytes.Buffer
		if json.Compact(&buf, b) != nil {
			q, _ := json.Marshal(string(b)) // not JSON: keep it as a string
			buf.Reset()
			buf.Write(q)
		}
		out = append(out, buf.String())
	}
	return out, nil
}

// sampleList reads the samples written by writeReference in index order.
func sampleList(dir string) []core.AssistSample {
	files, _ := filepath.Glob(filepath.Join(dir, ReferenceDir, "samples", "*.jsonl"))
	sort.Strings(files)
	out := []core.AssistSample{}
	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // inside the session's draft directory
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		for line := range strings.SplitSeq(string(b), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			out = append(out, core.AssistSample{Index: len(out), SensorID: id, Payload: json.RawMessage(line)})
		}
	}
	return out
}

// Samples lists the sample inputs of a flow session in the order `--sample N` and sample_index use.
func (m *Manager) Samples(ctx context.Context, sessionID string) ([]core.AssistSample, error) {
	s, err := m.session(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !s.isFlow() {
		return []core.AssistSample{}, nil
	}
	return sampleList(s.dir), nil
}

// flowPreamble prefixes every prompt of a flow session.
func flowPreamble(targetID string, globalEnv []string) string {
	var b strings.Builder
	b.WriteString("[hal9001 flow assistant] You have no file or shell access: you read and change the draft with the tools draft_read, draft_write, draft_test and validate. " +
		"New prompts are draft files `" + PromptsDir + "/<name>.md` (draft_write with that path). `" + ReferenceDir + "/` is read-only reference material (sensors, prompts, runtimes, samples, docs) read with draft_read. " +
		"Call draft_read with path `instructions` first: it holds the format, the node types and how to test.\n")
	if targetID != "" {
		b.WriteString("You are editing the existing flow `" + targetID + "`; keep its id.\n")
	} else {
		b.WriteString("You are creating a new flow. Never enable it: keep `enabled: false`; the user enables it.\n")
	}
	b.WriteString("Env variable names command nodes can use: " + nameList(globalEnv) + ".\n")
	b.WriteString("Test the draft with draft_test (optionally `sample`); the first test after you change a command node asks the user to approve it.\n\n")
	return b.String()
}

// flowInstructions is the authoring guide of a flow session; the agent reads it with draft_read path `instructions`.
func flowInstructions(sessionID, targetID string, globalEnv []string) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	w("# Flow authoring: instructions for the assistant\n\n")
	w("You help the user write or change ONE hal9001 flow. hal9001 is a local daemon that polls systems with sensors, ")
	w("turns what they return into events, and runs flows over those events. A flow is a small graph of nodes: ")
	w("it subscribes to sensors, reshapes and routes each event, optionally asks an agent, and puts what needs attention in the user's inbox. ")
	w("This is a private draft (session %s). You have no file or shell access: you work only through the hal tools draft_read, draft_write, draft_test and validate. Your job is the draft `%s`, plus new prompt files `%s/<name>.md` and, when the flow needs a source that does not exist, the sensors it subscribes to as `%s/<id>.yaml` (draft_write with those paths).\n\n", sessionID, FlowDraftFile, PromptsDir, SensorsDir)

	if targetID != "" {
		w("You are EDITING the existing flow `%s`. Do not add an `id:` line: the flow's name is its file name and the user saves the draft over it.\n\n", targetID)
	} else {
		w("You are creating a NEW flow. Pick a short kebab-case `id` (letters, digits, `-` and `_`, at most 64 characters) and keep it stable for the rest of the session: the user may already have the editor open on it. It becomes the file name and is not stored in the file.\n")
		w("Write a short `description:` (one line, what the flow does); the user sees it under the name.\n\n")
	}

	w("## Rules\n\n")
	w("- Change only the draft and files under `%s/` and `%s/`, with draft_write. `%s/` is READ-ONLY reference material (draft_read path `%s/...`). The user's own flows, prompts and settings are not editable from here, and sensors only through a draft (below).\n", PromptsDir, SensorsDir, ReferenceDir, ReferenceDir)
	w("- NEVER enable a flow: keep `enabled: false` (and `dry_run: true` for a new flow). The user enables it after reading it.\n")
	w("- Never print, echo, log or ask for a secret value. You only ever see variable names.\n")
	w("- Reading and writing the draft and validating it never ask the user. Testing it asks only when a command node changed (below).\n")
	w("- Test the draft with draft_test before you say it is done, and fix what it reports. Test both a sample that should pass and one that should be dropped when you can.\n")
	w("- A test runs `command` nodes with the user's real credentials, so the first test after you change a command node (`lang`, `script`, `command`, `cwd`, or the credentials it mentions) asks the user to approve it. ")
	w("Before you run it, say in a sentence what the command nodes do and which credentials they use. Once the user allowed a version, testing the same code again does not ask. ")
	w("`transform` nodes are generated code and never ask. If the user denies the test, do not look for another way to run it: explain what you wanted to check and wait.\n")
	w("- End every turn with a short plain-language summary of what the flow does now, what the test showed and what the user should check or decide. The user sees the draft and its graph update live.\n\n")

	w("## Tools\n\n")
	w("- `draft_read` returns the current draft with its validation diagnostics as JSON; `draft_write` replaces it (or a prompt file) and returns the same. `validate` returns the diagnostics only.\n")
	w("- `draft_test` runs the draft flow once on a sample and returns JSON: `flow_run_id`, `input`, `nodes` (per node: `status`, `decision` `emitted`/`dropped`/`error`/`skipped`, `reason`, `ports`, `outputs`, `would_create` for inbox nodes, `stdout`, `stderr`, `exit_code`) and `edges_taken`. ")
	w("Pick the input with `sample` (an index from `%s/samples/index.txt`) or `event` (an event id); without either the first sample is used. ", ReferenceDir)
	w("The test is a dry run: `side_effect` command nodes are skipped, **agent nodes are not run** (they are `skipped` with the reason `assist test: agent nodes are not run`, and their branch ends there) and inbox nodes only report the item they would create. ")
	w("`transform` and other `command` nodes run for real and follow the edges. `approval` nodes do not wait for a person: they take the decision `simulate_approval` (`approved`, the default, or `denied`) and emit on that port, so test both branches.\n")
	w("\n")

	w("## Sensors in this session\n\n")
	w("The user does not have to leave the flow to get a source. You MAY create or edit sensors here, but PREFER an existing sensor (`%s/sensors/`) when one fits: subscribe to it and do not copy it.\n", ReferenceDir)
	w("- A NEW sensor: `draft_write` path `%s/<id>.yaml` with the complete sensor file. Pick a short kebab-case id (letters, digits, `-` and `_`); it is the file name and is not stored in the file, so write no `id:` line. Give it a `description:` (one line, what it watches). The sensor format is in the sensor section of `%s/docs/flows.md`; `key` is required, and `first_run: baseline` is the safe default.\n", SensorsDir, ReferenceDir)
	w("- EDITING a saved sensor: read it (`draft_read` path `%s/<id>.yaml` returns the saved file when there is no draft yet), then `draft_write` the complete changed file to the same path; that copies it into the draft. Other flows may subscribe to that sensor, so change only what this flow needs and tell the user what else is affected.\n", SensorsDir)
	w("- The flow can `subscribe` to a draft sensor. `validate` and `draft_read` report the diagnostics of the flow and of every draft sensor (file `%s/<id>.yaml`); the user's Save writes the sensors first, then the prompts, then the flow.\n", SensorsDir)
	w("- Test a draft sensor with `draft_test` and `sensor: \"<id>\"` (it runs like a poll and returns `items` with their dedup keys). To test the flow on what a draft sensor really emits, use `from_sensor: \"<id>\"` (and `item`: which output item, default 0) instead of a sample. ")
	w("A sensor runs its script with the user's real credentials, so the first test after you change a sensor script asks the user to approve it, like a command node; say what the script does and which credentials it uses before you run it.\n")
	w("- Sensors from the draft have no events yet, so there are no samples for them: use `from_sensor`, or pass the shape you expect through a sample of another sensor.\n\n")

	w("## Reference material (`%s/`, read-only)\n\n", ReferenceDir)
	w("- `%s/docs/flows.md`: the full format of sensors, flows, nodes, templates and prompts. Read the flow and node sections before you write a flow.\n", ReferenceDir)
	w("- `%s/sensors/<id>.yaml`: the user's sensors, i.e. what can be subscribed to (the definitions contain no secret values).\n", ReferenceDir)
	w("- `%s/prompts/<name>.md`: the user's existing prompts, which agent nodes can use.\n", ReferenceDir)
	w("- For agent nodes PREFER inline `instructions: |` (a prompt template written on the node, `output: note|none`, default `note`) over creating a prompt file. Create a prompt file under `%s/` only when the user asks for a reusable prompt or several nodes share one; an agent node has either `prompt` or `instructions`, never both.\n", PromptsDir)
	w("- `%s/runtimes.txt`: the names of the configured agent runtimes (`runtime:` of an agent node).\n", ReferenceDir)
	w("- `%s/samples/<sensor>.jsonl` and `%s/samples/index.txt`: up to 5 recent real event payloads per sensor, redacted, one JSON value per line. ", ReferenceDir, ReferenceDir)
	w("Look at them before you write field names: use the fields that are really there.\n\n")

	w("## The flow file\n\n")
	w("```yaml\nid: triage-github        # new flows only: the file name, not stored\ndescription: Review requests become inbox notes.\nenabled: false\ndry_run: true\nsubscribe: [github-notifications]\nnodes:\n  - id: only-reviews\n    type: transform\n    steps:\n      - type: filter\n        conditions:\n          - {field: reason, op: equals, value: review_requested}\n  - id: note\n    type: inbox\n    title: \"Review requested: {{.input.subject.title}}\"\n    priority: normal\n    summary_md: \"{{.input.repository.full_name}}\"\n    ui: {x: 400, y: 120}\nedges:\n  - {from: only-reviews, to: note}\n```\n\n")
	w("- `subscribe` lists sensor ids (from `%s/sensors/`); each event of those sensors starts one run of the flow at its entry nodes (nodes with no incoming edge).\n", ReferenceDir)
	w("- Every `edges` entry is `{from: node, port: out, to: node}`; `port` defaults to `out`. A node can fail onto the `error` port. A port without edges ends the branch.\n")
	w("- Give every node a `ui: {x, y}` position so the graph reads left to right (about 300 apart horizontally, 140 vertically); nodes without a position are laid out automatically.\n")
	w("- Templates (`{{.input.field}}`, `{{.event.field}}`) are Go `text/template`; a missing field is an error. The full data model is in `%s/docs/flows.md`.\n\n", ReferenceDir)

	w("## Node types, and which to use\n\n")
	w("**PREFER `transform` nodes** for everything that filters, routes or reshapes items. They are plain-language steps (`filter`, `route`, `pick`, `set`, `remove`, `split`, `sort`, `limit`, `unique`, `aggregate`, `filter_list`) that the daemon compiles to jq: ")
	w("they need no code, no credentials and no approval, they explain in words why an item was dropped, and the user can edit them in the editor. ")
	w("Write a `command` node (bash, python, jq, ...) only when no sequence of steps can do the job (calling an API, running a tool). The exact step formats, conditions, operators and ports are in the transform section of `%s/docs/flows.md`.\n\n", ReferenceDir)
	w("```yaml\n- id: route\n  type: transform\n  steps:\n    - type: filter\n      conditions:\n        - {field: state, op: equals, value: open}\n    - type: route            # last step only\n      default_port: normal\n      rules:\n        - port: urgent\n          match: any\n          conditions:\n            - {field: 'labels[].name', op: in, value: [p0, p1]}\n```\n\n")
	w("A `route` step creates the output ports (`urgent` and `normal` here); edges leave the node on those ports.\n\n")
	w("| Type | Use it for |\n|---|---|\n")
	w("| `transform` | Filter, route, reshape. First choice. |\n")
	w("| `command` | Code (`lang`, `script`/`command`, `ports`, `side_effect`). Reads the item on stdin, writes JSON to stdout. Mark anything that changes the outside world `side_effect: true`. Needs the user's approval before its first test. |\n")
	w("| `agent` | Asks a coding agent to read the item and think (`runtime`, `mode`, `prompt`). Slower and costs money: use it only for judgement a rule cannot make (summarize, triage, draft a reply). |\n")
	w("| `inbox` | Puts an item in the user's inbox (`title`, `priority`, `summary_md`, `suggested_reply`, `links`; all are templates). What a person should look at ends here. |\n")
	w("| `approval` | \"Request approval\": asks the user to approve or deny before the flow goes on (`title`, `body` templates; `fields` input paths shown as a table; `priority`; optional `editable` input paths the approver may change before approving: downstream `input` then holds the edited values and `approval.edits` is `{path: {before, after}}`; optional `timeout` with `on_timeout: denied|approved|error`). Ports `approved` and `denied`; the payload is `{input, approval: {decision, comment, decided_by, decided_at, edited}}`. Put it between a step that prepares an action (for example an agent that drafts a reply) and the `side_effect` command that performs it: `draft -> approval -(approved)-> act`. |\n\n")

	w("### Agent nodes\n\n")
	w("- Default to `mode: readonly`: the agent can read but not act. For inbox triage use a prompt with `output: note` (see the existing prompts in `%s/prompts/`): the agent's note becomes an inbox item on its own, without an `inbox` node after it.\n", ReferenceDir)
	w("- `runtime` must be one of `%s/runtimes.txt`. Do not invent models.\n", ReferenceDir)
	w("- Use `mode: approve` or `auto` only if the user asked for the agent to act, and say so in your summary.\n")
	w("- `prompt` names a file in `prompts/` of the user's home. Reuse an existing prompt from `%s/prompts/` when one fits. If you need a new one, create the draft file `%s/<new-name>.md` (draft_write) with a front matter (`name`, `description`, `output: note`) and the prompt text (a template over `{{.input}}`); ", ReferenceDir, PromptsDir)
	w("the user's Save creates it. Never edit or reuse the name of an existing prompt: Save does not overwrite existing prompts, so choose a new name instead.\n")
	w("- You cannot run agent nodes in a test: they are skipped. Say in your summary that the agent step was not exercised.\n\n")

	w("### Inbox nodes\n\n")
	w("- `title` is required and must render non-empty. `priority` is one of `low`, `normal`, `high`, `urgent`. `summary_md` is Markdown for a reader who has not seen the item. Add `links` (`{label, url}`) to the source.\n")
	w("- A test shows each inbox node's rendered item in `would_create`; check the text reads well on the sample.\n\n")

	w("## Environment\n\n")
	w("`command` nodes run with the global env file. Credentials available (NAMES only; reference them as `$NAME`, never paste a value): %s.\n", nameList(globalEnv))
	w("If a command node needs a credential that is not listed, tell the user which variable to add instead of inventing one.\n")
	return b.String()
}

package assist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"hal9001/internal/agent"
	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
	"hal9001/internal/tools"
)

// ToolDraftFile is the draft tool, the only file the agent edits in a tool session.
const ToolDraftFile = "tool.yaml"

// toolSkeleton is the draft of a new tool when the request carries no YAML.
const toolSkeleton = `# New tool. Describe what it should do and the assistant fills this in.
id: new-tool
description: Describe what the tool does and when an agent should call it.
params:
  input:
    type: string
    description: What the tool works on.
    required: true
lang: bash
script: |
  echo "$ARG_INPUT"
approve: always
timeout: 60s
`

// evaluateTool validates a draft like a tool file. For a new tool the id need not exist yet. When editing an
// existing tool a changed id is flagged: saving would create a different tool.
func evaluateTool(yaml, target string) (toolID string, diagnostics json.RawMessage) {
	tool, ds := spec.CheckTool("", []byte(yaml))
	for i := range ds {
		ds[i].File = ToolDraftFile
	}
	if tool != nil {
		if spec.ValidID(tool.ID) {
			toolID = tool.ID
		}
		if target != "" && tool.ID != target {
			ds = append(ds, spec.Diagnostic{
				File: ToolDraftFile, Path: "id", Severity: spec.SeverityWarning, Code: spec.CodeIDMismatch,
				Message: fmt.Sprintf("id %q differs from the tool being edited (%q): saving would create a different tool", tool.ID, target),
			})
		}
	}
	return toolID, marshalDiagnostics(ds)
}

// scriptOfTool returns the identity of a tool's code given the credential names available to it.
func scriptOfTool(t *spec.Tool, available []string) scriptID {
	id := scriptID{Tool: true, Lang: t.Lang, Script: t.Script, Command: t.Command}
	text := t.Script + "\n" + strings.Join(t.Command, "\n")
	for _, name := range available {
		if mentions(text, name) {
			id.Env = append(id.Env, name)
		}
	}
	sort.Strings(id.Env)
	return id
}

// envNamesOf lists the variable names a script of the target sees: the global env file and the target's own
// file (a sensor's, or a tool's for a tool session).
func (m *Manager) envNamesOf(kind, targetID string) (global, own []string) {
	if kind != store.AssistTargetTool {
		return m.envNames(targetID)
	}
	g, tv, _ := m.host.ToolEnv(targetID)
	for k := range g {
		global = append(global, k)
	}
	for k := range tv {
		own = append(own, k)
	}
	sort.Strings(global)
	sort.Strings(own)
	return global, own
}

// availableEnvOf lists, sorted, every credential name a script of the target can read.
func (m *Manager) availableEnvOf(kind, targetID string) []string {
	global, own := m.envNamesOf(kind, targetID)
	all := append(append([]string(nil), global...), own...)
	sort.Strings(all)
	return all
}

func (s *session) isTool() bool { return s.row.TargetKind == store.AssistTargetTool }

// testTool calls the draft tool once and returns the redacted result. It records nothing. The environment is
// the global env file overlaid with the target tool's own values. Like a sensor test it runs real code with real
// credentials, so the same approval rule applies: the user's test always runs and marks the script approved; the
// agent's runs only for an approved script.
func (m *Manager) testTool(ctx context.Context, s *session, byAgent bool, req core.AssistTestRequest) (core.AssistToolTestResult, error) {
	var zero core.AssistToolTestResult
	s.mu.Lock()
	if err := s.usableLocked(); err != nil {
		s.mu.Unlock()
		return zero, err
	}
	target := s.row.TargetID
	tool, ds := spec.CheckTool("", []byte(s.row.DraftYAML))
	if tool == nil || spec.HasErrors(ds) {
		s.mu.Unlock()
		for i := range ds {
			ds[i].File = ToolDraftFile
		}
		return zero, &core.ValidationError{Diagnostics: ds}
	}
	available := m.availableEnvOf(store.AssistTargetTool, target)
	id := scriptOfTool(tool, available)
	if byAgent {
		if approved, _ := s.scriptApprovedLocked(id, available); !approved {
			s.mu.Unlock()
			return zero, fmt.Errorf("%w: the draft's script changed since the user last tested or approved it; "+
				"running it needs the user's approval (draft_test asks for it)", core.ErrInvalidState)
		}
	} else {
		s.markApprovedLocked(id)
	}
	s.mu.Unlock()

	global, own, red := m.host.ToolEnv(target)
	env := map[string]string{}
	for k, v := range global {
		env[k] = v
	}
	for k, v := range own {
		env[k] = v
	}
	stateDir, err := os.MkdirTemp("", "hal-assist-tool-")
	if err != nil {
		return zero, fmt.Errorf("assist: create state directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stateDir) }()

	// The run ends with the request, the engine, or the kill switch.
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop := context.AfterFunc(m.host.Context(), func() { cancel(agent.ErrInterrupted) })
	defer stop()
	untrack := m.host.Track("assist-test:"+m.st.NewID(), cancel)
	defer untrack()

	res, err := tools.RunUserTool(runCtx, tool, req.Args, tools.UserOptions{
		Env: env, Redactor: red, BasePATH: m.host.BasePATH(), StateDir: filepath.Join(stateDir, "state"), RunID: m.st.NewID(),
	})
	out := core.AssistToolTestResult{
		Kind: "tool", Args: res.Args, Stdout: clip(res.Stdout), Stderr: clip(res.Stderr), ExitCode: res.ExitCode,
		DurationMS: res.DurationMS, TimedOut: res.TimedOut, OK: err == nil,
	}
	if len(res.Structured) > 0 && len(res.Stdout) <= inlineLimit {
		out.Structured = res.Structured
	}
	if out.Args == nil {
		out.Args = json.RawMessage("{}")
	}
	if err == nil {
		return out, nil
	}
	var ae *tools.ArgError
	var te *tools.ToolError
	switch {
	case errors.As(err, &ae):
		return zero, invalid("args", ae.Msg)
	case errors.As(err, &te):
		msg := te.Error()
		out.Error = &msg
		return out, nil
	case errors.Is(context.Cause(runCtx), agent.ErrKilled):
		return zero, fmt.Errorf("%w: the test was stopped by the kill switch", core.ErrPaused)
	case runCtx.Err() != nil:
		return zero, context.Cause(runCtx)
	}
	// The process could not be started (missing interpreter): report it like a failed call.
	msg := red.String("could not run the draft: " + err.Error())
	out.ExitCode, out.Error = -1, &msg
	return out, nil
}

func clip(s string) string {
	if len(s) <= inlineLimit {
		return s
	}
	return s[:inlineLimit] + "\n… (truncated)"
}

// toolPreamble prefixes every prompt of a tool session.
func toolPreamble(targetID string, global, own []string) string {
	var b strings.Builder
	b.WriteString("[hal9001 tool assistant] You have no file or shell access: you read and change the draft with the tools draft_read, draft_write, draft_test and validate. " +
		"Call draft_read with path `instructions` first: it holds the tool format, how arguments reach the script and how to test.\n")
	if targetID != "" {
		b.WriteString("You are editing the existing tool `" + targetID + "`; keep its id.\n")
	} else {
		b.WriteString("You are creating a new tool.\n")
	}
	b.WriteString("Env variable names the script can use: " + nameList(append(append([]string(nil), global...), own...)) + ".\n")
	b.WriteString("Test with draft_test (pass `args`) before you say it is done.\n\n")
	return b.String()
}

// toolInstructions is the authoring guide of a tool session; the agent reads it with draft_read path `instructions`.
func toolInstructions(sessionID, targetID string, globalEnv, toolEnv []string) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	w("# Tool authoring: instructions for the assistant\n\n")
	w("You help the user write or change ONE hal9001 custom tool. A custom tool is a script that an AI agent running inside hal9001 ")
	w("can call: the agent supplies arguments, the script runs, its output goes back to the agent as the result. This directory is a ")
	w("private draft directory (session %s). Your whole job is the draft `%s`.\n\n", sessionID, ToolDraftFile)
	if targetID != "" {
		w("You are EDITING the existing tool `%s`. Keep `id: %s` unchanged: the user saves the draft over that tool.\n\n", targetID, targetID)
	} else {
		w("You are creating a NEW tool. Pick a short snake_case or kebab-case `id` (letters, digits, `-` and `_`, at most 64 characters).\n\n")
	}

	w("## Rules\n\n")
	w("- Change only the draft, with draft_write (the complete new file each time). The user's own tool files, flows and settings are not reachable and must not be touched.\n")
	w("- Never print, echo, log or ask for a secret value (tokens, API keys, passwords), and ")
	w("The script reads credentials from the environment variables listed below; you only ever see their names.\n")
	w("- Reading and writing the draft and validating it never ask the user. Testing it asks only when the code changed (below).\n")
	w("- Test the draft with draft_test before you say it is done, and fix what it reports.\n")
	w("- A test runs the script with the user's real credentials, so the first test after you change the code (`lang`, `script`, `command`, or the credentials it mentions) asks the user to approve it: they see the script and a diff against the last approved version. ")
	w("Before you run it, say in a sentence what the script does and which credentials it uses. Once the user allowed a version, testing the same code again does not ask. ")
	w("Changing only `description`, `params`, `approve`, `timeout` or `runs_in_sandbox` does not ask. If the user denies the test, or the command answers that the script needs approval, do not look for another way to run it: explain what you wanted to check and wait for the user.\n")
	w("- End every turn with a short plain-language summary of what you changed and what the test showed. The user sees the draft update live; say what they should check or decide.\n\n")

	w("## Tools\n\n")
	w("- `draft_read` returns the current draft with its validation diagnostics as JSON; `draft_write` replaces it and returns the same.\n")
	w("- `validate` checks the draft against the tool format and returns the diagnostics.\n")
	w("- `draft_test` calls the draft once with `args` (a JSON object; omit for none) and returns JSON: ")
	w("`ok`, `args` (with defaults filled in), `stdout`, `structured` (stdout parsed when it is a JSON object or array), `stderr`, `exit_code`, `timed_out` and `error`. ")
	w("Nothing is recorded. Values of the credentials appear as `«NAME»`.\n\n")

	w("## The tool file\n\n")
	w("```yaml\nid: http_get_json\ndescription: Fetch a URL with HTTP GET and return the JSON body.\nparams:\n  url: {type: string, description: The URL to fetch., required: true}\n  retries: {type: integer, default: 0}\nlang: python\nscript: |\n  import os\n  print(os.environ[\"ARG_URL\"])\napprove: always        # never | always | {when: {match: any, conditions: [...]}}\ntimeout: 30s\n```\n\n")
	w("| Field | Meaning |\n|---|---|\n")
	w("| `id` | Names the tool and its file. |\n")
	w("| `description` | Required. The calling agent reads it to decide when and how to use the tool: say what it does, what it returns and any limits. |\n")
	w("| `params` | Name to `{type, description, required, enum, default}`. `type` is `string`, `number`, `integer`, `boolean`, `array` or `object`. Parameter names are letters, digits and `_`. Describe every parameter. |\n")
	w("| `schema` | Instead of `params` (never both): a raw JSON Schema of the arguments, `type: object`. Use it only when `params` cannot express the shape. |\n")
	w("| `lang` | `bash`, `python` (python3), `node` or `exec` (`command: [argv...]`, no shell, no `script`). Inline `script` for every language except `exec`. |\n")
	w("| `approve` | When a call needs the user's approval: `never`, `always` (the default) or `{when: {match: all|any, conditions: [{field, op, value}]}}`, which asks only when the conditions match the arguments (fields are argument names; operators are those of the Transform node: `equals`, `not_equals`, `contains`, `starts_with`, `matches`, `gt`, `lt`, `in`, `exists`, ...). Be conservative: a tool that writes, sends, deletes or spends needs `always`; `never` is for harmless reads. |\n")
	w("| `timeout` | Default `60s`. The process group is killed when it is exceeded. |\n")
	w("| `runs_in_sandbox` | `true` runs the tool in the calling node's sandbox instead of on the hal host. Leave it out unless the user asks. |\n\n")

	w("## How a call reaches the script\n\n")
	w("- The arguments arrive as one JSON object on stdin, and as environment variables `ARG_<UPPER_NAME>`: strings as they are, numbers and booleans as text, arrays and objects as JSON. Optional arguments that were not passed (and have no default) are unset.\n")
	w("- Defaults from `params` are filled in before the call; unknown arguments and wrong types are rejected before the script runs.\n")
	w("- stdout is the result the calling agent sees. When it is a JSON object or array it is passed on as structured data, otherwise as text. Keep it short and relevant: the agent has a limited context.\n")
	w("- Exit 0 on success. Any other exit code is a tool error; the last part of stderr goes to the agent with it, so make stderr messages say what went wrong and what to change. Never put diagnostics on stdout.\n")
	w("- Interpreters are found on the daemon's `PATH`. Prefer tools that are already common (`curl`, `jq`, `gh`, `python3`) and say so when a script needs one that may not be installed.\n\n")

	w("## Environment\n\n")
	w("The script runs with a small base environment (`HOME`, `USER`, `LOGNAME`, `SHELL`, `LANG`, `LC_ALL`, `LC_CTYPE`, `TMPDIR`, `TZ`, `SSH_AUTH_SOCK`, `XDG_*`, `PATH`), then the variables below, then `ARG_*`, `HAL_RUN_ID` and `HAL_NODE` (the tool id).\n\n")
	w("Credentials available to this tool (NAMES only; reference them as `$NAME`, never paste a value):\n\n")
	w("- From the global env file: %s\n", nameList(globalEnv))
	w("- Only for this tool: %s\n\n", nameList(toolEnv))
	w("If the tool needs a credential that is not listed, tell the user which variable to add (name and what it is) instead of inventing one; they add it in the tool's Environment section. ")
	w("Write the script so that a missing variable produces a clear message on stderr and a non-zero exit.\n")
	return b.String()
}

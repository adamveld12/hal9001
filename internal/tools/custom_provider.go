package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"time"

	"hal9001/internal/runner"
	"hal9001/internal/sandbox"
	"hal9001/internal/spec"
	"hal9001/internal/transform"
)

// CustomTool is a user-defined tool as the provider needs it: the definition and what it runs with.
type CustomTool struct {
	Tool *spec.Tool
	// Env is the global env overlaid with the tool's own env file (real values).
	Env map[string]string
	// Redactor masks secret values in results and errors.
	Redactor *runner.Redactor
}

// CustomOptions configures NewCustomProvider.
type CustomOptions struct {
	// Lookup returns the tool with this id from the current workspace (called on every Open, so edits apply to
	// the next run). It returns an error for a missing tool or one with validation errors.
	Lookup func(id string) (CustomTool, error)
	// BasePATH is the PATH tools start with.
	BasePATH string
	// StateRoot is the directory holding one state directory per tool (<StateRoot>/<tool id>).
	StateRoot string
}

// ToolName is the MCP tool name of a custom tool: its id lowercased with '-' replaced by '_' (tool names are
// lowercase snake_case).
func ToolName(id string) string { return strings.ToLower(strings.ReplaceAll(id, "-", "_")) }

// NewCustomProvider adds the custom tools listed in RunSpec.Tools to a run's tool set. Each tool takes the
// arguments of its schema, asks for approval according to its approve setting (a node's override wins) and runs as
// a local process (or in the node's sandbox for runs_in_sandbox tools).
func NewCustomProvider(opt CustomOptions) Provider {
	return func(_ context.Context, rs RunSpec) (Extension, error) {
		var ext Extension
		for _, id := range rs.Tools {
			ct, err := opt.Lookup(id)
			if err != nil {
				return Extension{}, fmt.Errorf("custom tool %q: %w", id, err)
			}
			ext.Tools = append(ext.Tools, customTool(opt, rs, id, ct))
		}
		return ext, nil
	}
}

func customTool(opt CustomOptions, rs RunSpec, id string, ct CustomTool) Tool {
	t := ct.Tool.Clone()
	eff := &t
	if o, ok := rs.ToolApprove[id]; ok {
		eff.Approve = o
	}
	return Tool{
		Name:        ToolName(id),
		Description: t.Description,
		InputSchema: t.EffectiveSchema(),
		Approval: func(args json.RawMessage) (bool, string) {
			needs, err := ApprovalNeeded(context.Background(), eff, args, transform.NewRunner(opt.BasePATH, 10*time.Second))
			switch {
			case err != nil && needs:
				return true, fmt.Sprintf("custom tool %s: %v", id, err)
			case needs:
				return true, "custom tool " + id + " needs approval"
			}
			return false, ""
		},
		Describe: describeCustom(id, t.Lang, t.Script, t.Command, t.RunsInSandbox, rs.Sandbox),
		Run: func(ctx context.Context, c Call) (Result, error) {
			uo := UserOptions{
				Env: ct.Env, Redactor: ct.Redactor, BasePATH: opt.BasePATH,
				StateDir: filepath.Join(opt.StateRoot, id), RunID: rs.RunID,
			}
			var res UserResult
			var err error
			if t.RunsInSandbox {
				res, err = runToolInSandbox(sandbox.WithCall(ctx, sandbox.CallInfo{Tool: c.Name, Approval: c.Approval}), c.Sandbox, eff, c.Args, uo)
			} else {
				res, err = RunUserTool(ctx, eff, c.Args, uo)
			}
			if err != nil {
				return Result{}, err
			}
			if s, ok := res.Value().(string); ok {
				return Result{Text: s}, nil
			}
			b, _ := json.Marshal(res.Value())
			return Result{Text: string(b)}, nil
		},
	}
}

// runToolInSandbox runs a runs_in_sandbox tool in the run's sandbox session (the node's `sandbox:`): the arguments
// go on stdin as JSON and as ARG_ variables next to the tool's env, the script runs in the sandbox workdir, and the
// result is redacted. It returns the same errors as RunUserTool.
func runToolInSandbox(ctx context.Context, sess sandbox.Session, tool *spec.Tool, args json.RawMessage, opts UserOptions) (UserResult, error) {
	var out UserResult
	if sess == nil {
		return out, errors.New("this tool runs in a sandbox; set sandbox: on the agent node")
	}
	norm, err := NormalizeArgs(tool, args)
	if err != nil {
		return out, err
	}
	out.Args = norm
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(norm))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		return out, &ArgError{Msg: err.Error()}
	}
	env := maps.Clone(opts.Env)
	plain := make(map[string]string, len(obj)+2)
	for k, v := range obj {
		if s, ok := argEnvValue(v); ok {
			plain["ARG_"+strings.ToUpper(k)] = s
		}
	}
	plain["HAL_NODE"] = tool.ID
	if opts.RunID != "" {
		plain["HAL_RUN_ID"] = opts.RunID
	}
	timeout := tool.Timeout.Std()
	if opts.Timeout > 0 {
		timeout = opts.Timeout
	}
	if timeout == 0 {
		timeout = spec.DefaultToolTimeout.Std()
	}
	script := tool.Script
	if tool.Lang == spec.LangExec {
		quoted := make([]string, len(tool.Command))
		for i, a := range tool.Command {
			quoted[i] = shellQuote(a)
		}
		script = strings.Join(quoted, " ")
	}
	start := time.Now()
	res, err := RunInSandboxEnv(ctx, sess, tool.Lang, script, norm, env, plain, timeout)
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return out, cause
		}
		return out, fmt.Errorf("run tool %s in sandbox: %w", tool.ID, err)
	}
	out.Stdout = strings.TrimRight(opts.Redactor.String(res.Stdout), " \t\r\n")
	out.Stderr = opts.Redactor.String(res.Stderr)
	out.ExitCode = res.ExitCode
	out.TimedOut = res.TimedOut
	out.DurationMS = time.Since(start).Milliseconds()
	if res.TimedOut || res.ExitCode != 0 {
		return out, &ToolError{ExitCode: res.ExitCode, TimedOut: res.TimedOut, Stderr: tail(out.Stderr), Timeout: timeout}
	}
	if t := strings.TrimSpace(out.Stdout); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		var v any
		if json.Unmarshal([]byte(t), &v) == nil {
			out.Structured = json.RawMessage(t)
		}
	}
	return out, nil
}

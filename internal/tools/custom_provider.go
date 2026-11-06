package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"hal9001/internal/runner"
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
		Run: func(ctx context.Context, c Call) (Result, error) {
			uo := UserOptions{
				Env: ct.Env, Redactor: ct.Redactor, BasePATH: opt.BasePATH,
				StateDir: filepath.Join(opt.StateRoot, id), RunID: rs.RunID,
			}
			var res UserResult
			var err error
			if t.RunsInSandbox {
				res, err = runToolInSandbox(ctx, rs, eff, c.Args, uo)
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

// runToolInSandbox is where a runs_in_sandbox tool executes inside the node's sandbox session. It is the single
// plug-in point for sandbox execution: it must run the tool like RunUserTool does (arguments on stdin and as ARG_
// variables, redacted result) but in the sandbox.
//
//nolint:unparam // the sandbox implementation returns a result.
func runToolInSandbox(_ context.Context, rs RunSpec, _ *spec.Tool, _ json.RawMessage, _ UserOptions) (UserResult, error) {
	if rs.Sandbox == "" {
		return UserResult{}, errors.New("this tool runs in a sandbox; set sandbox: on the agent node")
	}
	return UserResult{}, errors.New("running tools in the node's sandbox is not available yet")
}

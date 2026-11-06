// Package tools runs the tools agents can call. user.go runs user-defined
// custom tools (tools/<id>.yaml) as local processes.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"hal9001/internal/runner"
	"hal9001/internal/spec"
	"hal9001/internal/transform"
)

// stderrTail is how much of a failing tool's stderr an error carries.
const stderrTail = 2048

// UserOptions configures RunUserTool.
type UserOptions struct {
	// Env holds the env-file values the tool sees: the global env overlaid
	// with the tool's own. They are real values.
	Env map[string]string
	// Redactor masks secret values in the result; nil redacts nothing.
	Redactor *runner.Redactor
	// BasePATH is the PATH the tool starts with (default: the daemon's).
	BasePATH string
	// StateDir is created and exposed as HAL_STATE_DIR; it is also the
	// working directory. Empty means the OS temp dir and no HAL_STATE_DIR.
	StateDir string
	// RunID is exposed as HAL_RUN_ID when set.
	RunID string
	// Timeout overrides the tool's own timeout when positive.
	Timeout time.Duration
}

// UserResult is the outcome of one tool run.
type UserResult struct {
	// Stdout is the redacted standard output, trimmed of trailing whitespace.
	Stdout string `json:"stdout"`
	// Structured is Stdout parsed as JSON when it is an object or array.
	Structured json.RawMessage `json:"structured,omitempty"`
	Stderr     string          `json:"stderr"`
	ExitCode   int             `json:"exit_code"`
	DurationMS int64           `json:"duration_ms"`
	TimedOut   bool            `json:"timed_out,omitempty"`
	// Args are the arguments as passed to the tool: defaults filled in.
	Args json.RawMessage `json:"args"`
}

// Value is what an agent sees as the tool's result: the structured JSON when
// stdout is an object or array, otherwise the text.
func (r UserResult) Value() any {
	if len(r.Structured) > 0 {
		return r.Structured
	}
	return r.Stdout
}

// ToolError is returned for a tool that ran and failed. The UserResult returned
// alongside it has the details.
type ToolError struct {
	ExitCode int
	TimedOut bool
	// Stderr is the last part of the redacted stderr.
	Stderr  string
	Timeout time.Duration
}

func (e *ToolError) Error() string {
	var msg string
	switch {
	case e.TimedOut:
		msg = fmt.Sprintf("tool timed out after %s", e.Timeout)
	case e.ExitCode < 0:
		msg = "tool was killed"
	default:
		msg = fmt.Sprintf("tool exited with status %d", e.ExitCode)
	}
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

// ArgError is returned when the arguments do not fit the tool's parameters.
type ArgError struct{ Msg string }

func (e *ArgError) Error() string { return "invalid arguments: " + e.Msg }

// RunUserTool runs tool with args (a JSON object). The arguments go to the
// process as JSON on stdin and as ARG_<UPPER_NAME> variables: strings as is,
// numbers and booleans as text, arrays and objects as JSON. Stdout is the
// result. A non-zero exit, a timeout or a kill returns the UserResult and a
// *ToolError; an ArgError means nothing ran; any other error means the process
// could not be started.
func RunUserTool(ctx context.Context, tool *spec.Tool, args json.RawMessage, opts UserOptions) (UserResult, error) {
	var out UserResult
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

	envFile := make(map[string]string, len(opts.Env)+len(obj))
	for k, v := range opts.Env {
		envFile[k] = v
	}
	for k, v := range obj {
		if s, ok := argEnvValue(v); ok {
			envFile["ARG_"+strings.ToUpper(k)] = s
		}
	}
	env, cleanup, err := runner.BuildEnv(runner.EnvOptions{Path: opts.BasePATH, EnvFile: envFile, RunID: opts.RunID, Node: tool.ID, StateDir: opts.StateDir})
	if err != nil {
		return out, err
	}
	defer cleanup()
	cwd := opts.StateDir
	if cwd == "" {
		cwd = os.TempDir()
	}
	timeout := tool.Timeout.Std()
	if opts.Timeout > 0 {
		timeout = opts.Timeout
	}
	if timeout == 0 {
		timeout = spec.DefaultToolTimeout.Std()
	}
	res, err := runner.Run(ctx, runner.Spec{
		Lang: runner.Lang(tool.Lang), Script: tool.Script, Command: tool.Command,
		Cwd: cwd, Env: env, BaseEnv: map[string]string{}, Stdin: norm, Timeout: timeout,
	})
	if err != nil {
		return out, fmt.Errorf("run tool %s: %w", tool.ID, err)
	}
	res = opts.Redactor.Result(res)
	out.Stdout = strings.TrimRight(string(res.Stdout), " \t\r\n")
	out.Stderr = string(res.Stderr)
	out.ExitCode = res.ExitCode
	out.DurationMS = res.Duration.Milliseconds()
	out.TimedOut = res.TimedOut
	switch {
	case res.Killed && !res.TimedOut:
		if cause := context.Cause(ctx); cause != nil {
			return out, cause
		}
		return out, &ToolError{ExitCode: -1, Stderr: tail(out.Stderr)}
	case res.TimedOut, res.ExitCode != 0:
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

// tail returns the last stderrTail bytes of s, trimmed, on a rune boundary.
func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= stderrTail {
		return s
	}
	s = s[len(s)-stderrTail:]
	for !utf8.ValidString(s[:1]) && len(s) > 1 {
		s = s[1:]
	}
	return "…" + s
}

// argEnvValue renders one argument for its ARG_ variable. null has none.
func argEnvValue(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", false
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// NormalizeArgs checks args (a JSON object; empty means {}) against the tool's
// parameters and returns them with defaults filled in. A tool with a raw
// schema takes any object. The error is an *ArgError.
func NormalizeArgs(tool *spec.Tool, args json.RawMessage) (json.RawMessage, error) {
	obj := map[string]any{}
	if t := bytes.TrimSpace(args); len(t) > 0 && string(t) != "null" {
		dec := json.NewDecoder(bytes.NewReader(t))
		dec.UseNumber()
		if err := dec.Decode(&obj); err != nil || obj == nil {
			return nil, &ArgError{Msg: "arguments must be a JSON object"}
		}
	}
	if tool.Schema == nil {
		var problems []string
		for _, name := range tool.ParamNames() {
			p := tool.Params[name]
			v, ok := obj[name]
			if !ok || v == nil {
				switch {
				case p.Default != nil:
					obj[name] = p.Default
				case p.Required:
					problems = append(problems, fmt.Sprintf("%s is required", name))
				}
				continue
			}
			if !hasType(v, p.Type) {
				problems = append(problems, fmt.Sprintf("%s must be %s", name, p.Type))
				continue
			}
			if len(p.Enum) > 0 && !slices.ContainsFunc(p.Enum, func(e any) bool { return fmt.Sprint(e) == fmt.Sprint(v) }) {
				problems = append(problems, fmt.Sprintf("%s must be one of %s", name, enumText(p.Enum)))
			}
		}
		var unknown []string
		for k := range obj {
			if _, ok := tool.Params[k]; !ok {
				unknown = append(unknown, k)
			}
		}
		slices.Sort(unknown)
		for _, k := range unknown {
			problems = append(problems, fmt.Sprintf("unknown argument %s", k))
		}
		if len(problems) > 0 {
			return nil, &ArgError{Msg: strings.Join(problems, "; ")}
		}
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, &ArgError{Msg: err.Error()}
	}
	return b, nil
}

func enumText(vals []any) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, ", ")
}

// hasType reports whether a decoded JSON value (numbers as json.Number) fits a
// parameter type.
func hasType(v any, typ string) bool {
	switch typ {
	case spec.ParamString:
		_, ok := v.(string)
		return ok
	case spec.ParamBoolean:
		_, ok := v.(bool)
		return ok
	case spec.ParamNumber:
		return isNumber(v, false)
	case spec.ParamInteger:
		return isNumber(v, true)
	case spec.ParamArray:
		_, ok := v.([]any)
		return ok
	case spec.ParamObject:
		_, ok := v.(map[string]any)
		return ok
	}
	return false
}

func isNumber(v any, integer bool) bool {
	switch x := v.(type) {
	case json.Number:
		if integer {
			if _, err := x.Int64(); err != nil {
				f, ferr := x.Float64()
				return ferr == nil && f == float64(int64(f))
			}
		}
		return true
	case int, int64, uint64:
		return true
	case float64:
		return !integer || x == float64(int64(x))
	}
	return false
}

// ApprovalNeeded reports whether a call of tool with args needs approval:
// always for approve: always, never for approve: never, and for {when: ...}
// when the conditions match the arguments (defaults filled in). run evaluates
// the conditions with jq; nil runs the jq on PATH. When the conditions cannot
// be evaluated it reports true with the error: unclear means ask.
func ApprovalNeeded(ctx context.Context, tool *spec.Tool, args json.RawMessage, run transform.RunFunc) (bool, error) {
	switch tool.Approve.Mode {
	case spec.ToolApproveNever:
		return false, nil
	case spec.ToolApproveWhen:
	default:
		return true, nil
	}
	if tool.Approve.When == nil || len(tool.Approve.When.Conditions) == 0 {
		return true, errors.New("approve.when has no conditions")
	}
	norm, err := NormalizeArgs(tool, args)
	if err != nil {
		return true, err
	}
	prog, err := transform.Compile([]transform.Step{{
		Type: transform.StepFilter, Match: tool.Approve.When.Match, Conditions: tool.Approve.When.Conditions,
	}})
	if err != nil {
		return true, err
	}
	if run == nil {
		run = transform.NewRunner("", 10*time.Second)
	}
	res, err := run(ctx, prog, norm)
	if err != nil {
		return true, fmt.Errorf("evaluate approve.when: %w", err)
	}
	if res.ExitCode != 0 {
		return true, fmt.Errorf("evaluate approve.when: jq exited with status %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	// The filter step emits the arguments when the conditions match.
	return len(bytes.TrimSpace(res.Stdout)) > 0, nil
}

// StateDirFor is the state directory of a tool under the daemon's data dir.
func StateDirFor(data, toolID string) string {
	return filepath.Join(data, "state", "_tools", toolID)
}

package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"hal9001/internal/runner"
)

// RunFunc runs one jq program with stdin as its input.
type RunFunc func(ctx context.Context, program string, stdin []byte) (runner.Result, error)

// NewRunner returns a RunFunc that runs programs with `jq -c -f` through
// internal/runner, resolving jq against path (the process PATH when empty).
func NewRunner(path string, timeout time.Duration) RunFunc {
	return func(ctx context.Context, program string, stdin []byte) (runner.Result, error) {
		return runner.Run(ctx, runner.Spec{
			Lang: runner.LangJQ, Script: program, Stdin: stdin, Timeout: timeout,
			Cwd: os.TempDir(), BaseEnv: runner.DefaultBaseEnv(path),
		})
	}
}

// Output is one item a run emitted, with its port.
type Output struct {
	Port    string          `json:"port"`
	Payload json.RawMessage `json:"payload"`
}

// StepResult is what one step did in a cumulative run: the number of items
// that left it when the steps up to and including it ran on the input.
type StepResult struct {
	Step         int    `json:"step"`
	Description  string `json:"description"`
	OutputsCount int    `json:"outputs_count"`
	Dropped      bool   `json:"dropped"`
	Error        string `json:"error,omitempty"`
}

// Decisions of a preview, equal to the node run decisions.
const (
	DecisionEmitted = "emitted"
	DecisionDropped = "dropped"
	DecisionError   = "error"
)

// Result is the outcome of Preview.
type Result struct {
	JQ          string
	Description []string
	Decision    string
	Outputs     []Output
	// Reason explains a drop in plain words, or (when emitted) why routed
	// items went where they did.
	Reason      string
	Error       string
	StepResults []StepResult
	Issues      []Issue
}

// Preview runs steps on input (one JSON value) and reports the outcome and,
// for each step, how many items were left after it. Nothing is stored. The
// last run is exactly what the engine would execute.
func Preview(ctx context.Context, steps []Step, input []byte, run RunFunc) Result {
	res := Result{Description: Describe(steps), Outputs: []Output{}, StepResults: []StepResult{}}
	res.Issues = Validate(steps)
	if HasErrors(res.Issues) {
		res.Decision = DecisionError
		res.Error = (&ValidationError{Issues: res.Issues}).Error()
		return res
	}
	jq, err := Compile(steps)
	if err != nil {
		res.Decision, res.Error = DecisionError, err.Error()
		return res
	}
	res.JQ = jq
	if len(steps) == 0 {
		out, errMsg := runPrefix(ctx, steps, input, run)
		res.finish(out, errMsg)
		return res
	}

	prev := input // first item entering the step being run
	var last *prefixRun
	for k := 1; k <= len(steps); k++ {
		out, errMsg := runPrefix(ctx, steps[:k], input, run)
		sr := StepResult{Step: k - 1, Description: res.Description[k-1], Error: errMsg}
		if errMsg == "" {
			sr.OutputsCount = len(out.outputs)
			sr.Dropped = len(out.outputs) == 0
		}
		res.StepResults = append(res.StepResults, sr)
		if errMsg != "" || sr.Dropped {
			if errMsg == "" {
				res.Reason = dropReason(ctx, steps, k-1, prev, run)
				if k >= 2 {
					if n := res.StepResults[k-2].OutputsCount; n > 1 {
						res.Reason += fmt.Sprintf(" (checked on the first of %d items)", n)
					}
				}
			}
			res.fillRest(steps)
			res.finish(out, errMsg)
			return res
		}
		last = out
		if len(out.outputs) > 0 {
			prev = out.outputs[0].Payload
		}
	}
	res.finish(last, "")
	return res
}

// fillRest adds zero-count results for the steps after the one that ended the run.
func (r *Result) fillRest(steps []Step) {
	for k := len(r.StepResults); k < len(steps); k++ {
		r.StepResults = append(r.StepResults, StepResult{Step: k, Description: r.Description[k]})
	}
}

func (r *Result) finish(out *prefixRun, errMsg string) {
	switch {
	case errMsg != "":
		r.Decision, r.Error = DecisionError, errMsg
	case len(out.outputs) == 0:
		r.Decision = DecisionDropped
		if r.Reason == "" {
			r.Reason = "no output: every item was filtered out"
		}
	default:
		r.Decision = DecisionEmitted
		var reasons []string
		for _, o := range out.outputs {
			r.Outputs = append(r.Outputs, Output{Port: o.Port, Payload: o.Payload})
			if o.Reason != "" && !containsStr(reasons, o.Reason) {
				reasons = append(reasons, o.Reason)
			}
		}
		r.Reason = strings.Join(reasons, "; ")
	}
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

type prefixRun struct{ outputs []runner.Output }

// runPrefix compiles and runs steps. errMsg is non-empty for a failed run.
func runPrefix(ctx context.Context, steps []Step, input []byte, run RunFunc) (*prefixRun, string) {
	prog, err := Compile(steps)
	if err != nil {
		return nil, err.Error()
	}
	res, err := run(ctx, prog, input)
	if err != nil {
		return nil, "cannot run jq: " + err.Error()
	}
	oc := res.Outcome(Ports(steps))
	if oc.Decision == runner.DecisionError {
		return nil, runError(res, oc)
	}
	return &prefixRun{outputs: oc.Outputs}, ""
}

var jqErrPrefix = regexp.MustCompile(`^(jq: error( \([^)]*\))*: |Error: )`)

// runError renders a failed jq run in one line.
func runError(res runner.Result, oc runner.Outcome) string {
	if res.TimedOut {
		return "timeout: the steps took too long to run"
	}
	if res.ExitCode != 0 {
		for _, line := range strings.Split(string(res.Stderr), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				return jqErrPrefix.ReplaceAllString(line, "")
			}
		}
	}
	return oc.Error
}

// DropReason explains in plain words why steps dropped input, by running the
// steps up to the one that dropped it. It returns "" when input is not dropped.
func DropReason(ctx context.Context, steps []Step, input []byte, run RunFunc) string {
	r := Preview(ctx, steps, input, run)
	if r.Decision != DecisionDropped {
		return ""
	}
	return r.Reason
}

// dropReason explains why step idx dropped the item prev.
func dropReason(ctx context.Context, steps []Step, idx int, prev []byte, run RunFunc) string {
	s := &steps[idx]
	pre := fmt.Sprintf("Dropped by step %d", idx+1)
	switch s.Type {
	case StepSplit:
		return fmt.Sprintf("%s: %s has no items to split", pre, listText(s.Field))
	case StepFilter:
		if ok := conditionResults(ctx, s.Conditions, prev, run); ok != nil {
			return pre + ": " + filterWhy(s, ok)
		}
	}
	return pre + " (" + DescribeStep(s) + ")"
}

// conditionResults evaluates each condition on item; nil when that failed.
func conditionResults(ctx context.Context, conds []Condition, item []byte, run RunFunc) []bool {
	c := &compiler{used: map[string]bool{}}
	parts := make([]string, len(conds))
	for i := range conds {
		parts[i] = c.condition(&conds[i])
	}
	var prog strings.Builder
	for _, h := range helperDefs {
		if c.used[h.name] {
			prog.WriteString(h.def + "\n")
		}
	}
	prog.WriteString("[" + strings.Join(parts, ", ") + "]\n")
	res, err := run(ctx, prog.String(), item)
	if err != nil || res.ExitCode != 0 {
		return nil
	}
	var out []bool
	if json.Unmarshal(res.Stdout, &out) != nil || len(out) != len(conds) {
		return nil
	}
	return out
}

func filterWhy(s *Step, met []bool) string {
	descs := make([]string, len(s.Conditions))
	for i, cn := range s.Conditions {
		descs[i] = DescribeCondition(cn)
	}
	first := func(want bool) int {
		for i, m := range met {
			if m == want {
				return i
			}
		}
		return 0
	}
	switch {
	case s.keeps() && s.match() == MatchAll:
		return "it did not meet the condition: " + descs[first(false)]
	case s.keeps() && len(descs) == 1:
		return "it did not meet the condition: " + descs[0]
	case s.keeps():
		return "it met none of the conditions: " + strings.Join(descs, "; ")
	case s.match() == MatchAny || len(descs) == 1:
		return "it met the condition: " + descs[first(true)]
	}
	return "it met all of the conditions: " + strings.Join(descs, "; ")
}

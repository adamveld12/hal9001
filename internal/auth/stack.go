package auth

import (
	"context"
	"fmt"
)

// Outcome is the final result of evaluating a stack.
type Outcome string

const (
	OutcomeSuccess   Outcome = "success"
	OutcomeFail      Outcome = "fail"
	OutcomeChallenge Outcome = "challenge"
)

// ModuleTrace records what one module returned.
type ModuleTrace struct {
	Module  string     `json:"module"`
	Control Control    `json:"control"`
	Result  ResultKind `json:"result"`
	Reason  string     `json:"reason,omitempty"`
}

// Evaluation is the result of running a stack: the outcome plus a full
// per-module trace.
type Evaluation struct {
	Outcome Outcome
	// Principal is set when Outcome is success.
	Principal Principal
	// Modules are the modules whose success counted toward the outcome.
	Modules []string
	Trace   []ModuleTrace
	// Reason explains a failure or names the granting module.
	Reason string
	// Pending is set when Outcome is challenge; pass it to Stack.Resume.
	Pending *Pending
}

// Pending is a stack evaluation paused on a module's Challenge.
type Pending struct {
	// Module is the module that issued the challenge.
	Module string
	// Step is the challenge to present to the client.
	Step  Step
	index int
	state evalState
}

// evalState is the PAM bookkeeping carried across entries (and across a
// challenge round trip).
type evalState struct {
	trace []ModuleTrace
	// requiredFailed names the first failed required module.
	requiredFailed string
	// counted is the number of non-optional modules that returned success or
	// fail (ignore does not count).
	counted      int
	principal    Principal
	hasPrincipal bool
	modules      []string
	// optional results only matter when no other module ran.
	optionalRan       int
	optionalPrincipal Principal
	optionalSuccess   bool
	optionalModules   []string
}

// Stack is an ordered list of module entries evaluated with PAM semantics.
type Stack struct {
	Name     string
	Entries  []StackEntry
	Registry *Registry
}

// Evaluate runs the stack from the first entry.
func (s *Stack) Evaluate(ctx context.Context, req *AuthRequest) Evaluation {
	return s.run(ctx, req, 0, evalState{}, nil)
}

// Resume continues a stack paused on a Challenge, handing resp to the module
// that issued it. The request must come from the same client; the caller
// checks that.
func (s *Stack) Resume(ctx context.Context, req *AuthRequest, p *Pending, resp StepResponse) Evaluation {
	resp.State = p.Step.State
	return s.run(ctx, req, p.index, p.state, &resp)
}

func (s *Stack) run(ctx context.Context, req *AuthRequest, start int, st evalState, resume *StepResponse) Evaluation {
	for i := start; i < len(s.Entries); i++ {
		entry := s.Entries[i]
		res := s.call(ctx, req, entry, resume)
		resume = nil

		if res.Kind == KindChallenge && !req.Interactive {
			res = Ignore("login challenge requires an interactive login")
		}
		st.trace = append(st.trace, ModuleTrace{Module: entry.Module, Control: entry.Control, Result: res.Kind, Reason: res.Reason})

		switch res.Kind {
		case KindChallenge:
			return Evaluation{
				Outcome: OutcomeChallenge,
				Trace:   st.trace,
				Reason:  fmt.Sprintf("module %s requires another step", entry.Module),
				Pending: &Pending{Module: entry.Module, Step: *res.Step, index: i, state: st},
			}
		case KindIgnore:
			continue
		case KindSuccess:
			if entry.Control == Optional {
				st.optionalRan++
				if !st.optionalSuccess {
					st.optionalSuccess, st.optionalPrincipal = true, res.Principal
				}
				st.optionalModules = append(st.optionalModules, entry.Module)
				continue
			}
			st.counted++
			if !st.hasPrincipal {
				st.principal, st.hasPrincipal = res.Principal, true
			}
			st.modules = append(st.modules, entry.Module)
			if entry.Control == Sufficient && st.requiredFailed == "" {
				return st.success(fmt.Sprintf("module %s succeeded", entry.Module))
			}
		case KindFail:
			switch entry.Control {
			case Optional:
				st.optionalRan++
			case Requisite:
				st.counted++
				return st.fail(fmt.Sprintf("requisite module %s failed: %s", entry.Module, res.Reason))
			case Required:
				st.counted++
				if st.requiredFailed == "" {
					st.requiredFailed = entry.Module
				}
			case Sufficient:
				st.counted++
			}
		}
	}

	switch {
	case st.requiredFailed != "":
		return st.fail(fmt.Sprintf("required module %s failed", st.requiredFailed))
	case st.counted > 0 && len(st.modules) > 0:
		return st.success("all required modules succeeded")
	case st.counted == 0 && st.optionalRan > 0 && st.optionalSuccess:
		st.principal, st.hasPrincipal, st.modules = st.optionalPrincipal, true, st.optionalModules
		return st.success("optional module succeeded as the only module that ran")
	default:
		return st.fail("no module succeeded")
	}
}

func (s *Stack) call(ctx context.Context, req *AuthRequest, entry StackEntry, resume *StepResponse) Result {
	mod, ok := s.Registry.Get(entry.Module)
	if !ok {
		return Fail(fmt.Sprintf("module %q is not registered", entry.Module))
	}
	r := *req
	r.Options = entry.Options
	r.Step = resume
	return mod.Authenticate(ctx, &r)
}

func (st *evalState) success(reason string) Evaluation {
	return Evaluation{Outcome: OutcomeSuccess, Principal: st.principal, Modules: st.modules, Trace: st.trace, Reason: reason}
}

func (st *evalState) fail(reason string) Evaluation {
	return Evaluation{Outcome: OutcomeFail, Trace: st.trace, Reason: reason}
}

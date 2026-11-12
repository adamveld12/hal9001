package assist

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"

	"hal9001/internal/agent"
	"hal9001/internal/core"
	"hal9001/internal/runner"
	"hal9001/internal/spec"
)

const (
	// maxTestItems bounds the items a test result lists; item_count is the total.
	maxTestItems = 200
	// inlineLimit is how much of stdout or stderr a test result carries; the rest is a blob.
	inlineLimit = 64 << 10
)

// TestDraft runs the draft like Poll now and returns the redacted result. It records nothing: no sensor run,
// no events, no dedup keys. The environment is the global env file overlaid with the values of the TARGET
// sensor (a new sensor has none), exactly what a poll of that sensor would get.
//
// The script runs with those credentials, so who asks matters. The user's test always runs and marks the script
// approved; the agent's runs only while the script is one the user has tested or approved (or the target sensor's
// saved script), else core.ErrInvalidState: the agent has to ask through its permission request first.
func (m *Manager) TestDraft(ctx context.Context, sessionID string, byAgent bool, req core.AssistTestRequest) (core.AssistTestOutcome, error) {
	s, err := m.session(ctx, sessionID)
	if err != nil {
		return core.AssistTestOutcome{}, err
	}
	if s.isGeneral() {
		return core.AssistTestOutcome{}, errNoDraft
	}
	if s.isFlow() {
		if err := checkFlowRequest(req); err != nil {
			return core.AssistTestOutcome{}, err
		}
		if req.Sensor != "" {
			res, err := m.testDraftSensor(ctx, s, byAgent, req)
			if err != nil {
				return core.AssistTestOutcome{}, err
			}
			return core.AssistTestOutcome{Sensor: &res}, nil
		}
		gated := false
		if req.FromSensor != "" {
			input, err := m.fromSensorInput(ctx, s, byAgent, req)
			if err != nil {
				return core.AssistTestOutcome{}, err
			}
			req.Input, req.FromSensor, gated = input, "", true // the sensor's approval covers the flow's command nodes too
		}
		res, err := m.testFlow(ctx, s, byAgent, gated, req)
		if err != nil {
			return core.AssistTestOutcome{}, err
		}
		return core.AssistTestOutcome{Flow: &res}, nil
	}
	if s.isTool() {
		res, err := m.testTool(ctx, s, byAgent, req)
		if err != nil {
			return core.AssistTestOutcome{}, err
		}
		return core.AssistTestOutcome{Tool: &res}, nil
	}
	res, err := m.testSensor(ctx, s, byAgent)
	if err != nil {
		return core.AssistTestOutcome{}, err
	}
	return core.AssistTestOutcome{Sensor: &res}, nil
}

func (m *Manager) testSensor(ctx context.Context, s *session, byAgent bool) (core.AssistTestResult, error) {
	var zero core.AssistTestResult
	s.mu.Lock()
	if err := s.usableLocked(); err != nil {
		s.mu.Unlock()
		return zero, err
	}
	target := s.row.TargetID
	sensor, ds := spec.CheckSensor("", []byte(s.row.DraftYAML), m.sensorRefs())
	if sensor == nil || spec.HasErrors(ds) {
		s.mu.Unlock()
		for i := range ds {
			ds[i].File = SensorDraftFile
		}
		return zero, &core.ValidationError{Diagnostics: ds}
	}
	available := m.availableEnv(target)
	id := scriptOf(sensor, available)
	if byAgent {
		if approved, _ := s.scriptApprovedLocked(id, available); !approved {
			s.mu.Unlock()
			return zero, fmt.Errorf("%w: the draft's script changed since the user last tested or approved it; "+
				"running it needs the user's approval (the permission request for `hal9001 assist test`)", core.ErrInvalidState)
		}
	} else {
		s.markApprovedLocked(id)
	}
	s.mu.Unlock()
	return m.runSensorDraft(ctx, sensor, target)
}

// runSensorDraft runs a validated sensor draft like a poll would, with the global env file overlaid by the env file
// of the saved sensor target ("" for a new sensor), and returns the redacted result. Nothing is recorded.
func (m *Manager) runSensorDraft(ctx context.Context, sensor *spec.Sensor, target string) (core.AssistTestResult, error) {
	var zero core.AssistTestResult
	global, own, red := m.host.Env(target)
	stateDir, err := os.MkdirTemp("", "hal-assist-state-")
	if err != nil {
		return zero, fmt.Errorf("assist: create state directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stateDir) }()
	envFile := global
	if envFile == nil {
		envFile = map[string]string{}
	}
	for k, v := range own {
		envFile[k] = v
	}
	env, cleanup, err := runner.BuildEnv(runner.EnvOptions{
		Path:     m.host.BasePATH(),
		EnvFile:  envFile,
		RunID:    m.st.NewID(),
		Node:     sensor.ID,
		StateDir: filepath.Join(stateDir, "state"),
		Sensor:   true, // no LastSuccessAt: HAL_LAST_SUCCESS_AT stays unset, as before a sensor's first success
	})
	if err != nil {
		return zero, err
	}
	defer cleanup()
	if m.opt.MCP != nil {
		access, err := m.opt.MCP.GrantScript(env["HAL_RUN_ID"], sensor.ID, sensor.MCPServers, sensor.Timeout.Std(), m.opt.URL, m.opt.Executable, env["PATH"])
		if err != nil {
			return zero, fmt.Errorf("assist: grant MCP access: %w", err)
		}
		if access != nil {
			defer access.Revoke()
			maps.Copy(env, access.Env)
		}
	}

	// The run ends with the request or the engine.
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop := context.AfterFunc(m.host.Context(), func() { cancel(agent.ErrInterrupted) })
	defer stop()
	untrack := m.host.Track("assist-test:"+m.st.NewID(), cancel)
	defer untrack()

	res, err := runner.Run(runCtx, runner.Spec{
		Lang:    runner.Lang(sensor.Lang),
		Script:  sensor.Script,
		Command: sensor.Command,
		Cwd:     sensor.Cwd,
		Env:     env,
		BaseEnv: map[string]string{},
		Timeout: sensor.Timeout.Std(),
	})
	if err != nil {
		// The process could not be started (missing interpreter, bad cwd): report it like a failed poll.
		msg := red.String("could not run the draft: " + err.Error() + "\n")
		stderr, berr := m.textBlob([]byte(msg), int64(len(msg)))
		if berr != nil {
			return zero, berr
		}
		return core.AssistTestResult{Kind: "sensor", ExitCode: -1, Items: []core.AssistTestItem{}, Stdout: core.AssistText{}, Stderr: stderr}, nil
	}
	if res.Killed && !res.TimedOut {
		return zero, context.Cause(runCtx)
	}
	res = red.Result(res)

	out := core.AssistTestResult{
		Kind: "sensor", ExitCode: res.ExitCode, DurationMS: res.Duration.Milliseconds(), TimedOut: res.TimedOut,
		Items: []core.AssistTestItem{},
	}
	if out.Stdout, err = m.textBlob(res.Stdout, res.StdoutBytes); err != nil {
		return zero, err
	}
	if out.Stderr, err = m.textBlob(res.Stderr, res.StderrBytes); err != nil {
		return zero, err
	}
	if res.ExitCode != 0 || res.TimedOut {
		return out, nil // a failing poll has no items; exit_code and the streams say why
	}
	raw, perr := runner.ParseSensorItems(res.Stdout)
	if perr != nil {
		out.ParseError = new(perr.Error())
		return out, nil
	}
	out.ItemCount = len(raw)
	for i, r := range raw {
		if i == maxTestItems {
			break
		}
		it := core.AssistTestItem{Item: r}
		key, kerr := renderKey(sensor, r)
		if kerr != nil {
			it.KeyError = new(kerr.Error())
		} else {
			it.DedupKey = &key
		}
		out.Items = append(out.Items, it)
	}
	return out, nil
}

// renderKey renders the dedup key of one item as a poll does.
func renderKey(sensor *spec.Sensor, item []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(item))
	dec.UseNumber() // keep large ids as written
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", fmt.Errorf("item is not valid JSON: %w", err)
	}
	key, err := spec.RenderNamed("key", sensor.Key, v)
	if err != nil {
		return "", fmt.Errorf("dedup key: %w", err)
	}
	return key, nil
}

// textBlob renders captured output: up to 64 KiB inline, the whole of larger output as a blob.
func (m *Manager) textBlob(b []byte, total int64) (core.AssistText, error) {
	size := max(total, int64(len(b)))
	if len(b) <= inlineLimit {
		return core.AssistText{Text: string(b), Size: size, Truncated: size > int64(len(b))}, nil
	}
	c, err := m.st.PutBlob(b)
	if err != nil {
		return core.AssistText{}, fmt.Errorf("assist: keep test output: %w", err)
	}
	text := cutUTF8(string(b), inlineLimit)
	return core.AssistText{Text: text, Size: size, Truncated: true, Blob: &c.Ref}, nil
}

package engine

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"hal9001/internal/agent"
	"hal9001/internal/runner"
	"hal9001/internal/spec"
)

// snapshot is the immutable view of the workspace the engine runs against.
// Only valid files are present; a file with errors is invisible to the engine.
type snapshot struct {
	ws      *spec.Workspace
	cfg     spec.Config
	env     map[string]string
	envKeys []string
	// sensorEnv maps a sensor id to its own env values (env.d/sensors); they
	// overlay env for that sensor's polls only.
	sensorEnv map[string]map[string]string
	red       *runner.Redactor
	sensors   map[string]*spec.Sensor
	flows     map[string]*flowDef
	prompts   map[string]*spec.Prompt
	// subs maps a sensor id to the enabled flows subscribed to it, sorted.
	subs map[string][]string
}

// flowDef is a loaded flow with the exact bytes and hash that will be
// snapshotted into flow_versions when a run is created.
type flowDef struct {
	flow *spec.Flow
	raw  []byte
	hash string
}

func newSnapshot(ws *spec.Workspace) *snapshot {
	s := &snapshot{
		ws:        ws,
		env:       map[string]string{},
		sensorEnv: map[string]map[string]string{},
		sensors:   map[string]*spec.Sensor{},
		flows:     map[string]*flowDef{},
		prompts:   map[string]*spec.Prompt{},
		subs:      map[string][]string{},
	}
	if ws.Config.Value != nil {
		s.cfg = ws.Config.Value.Clone()
	}
	s.cfg.ApplyDefaults()
	if ws.Env.Value != nil {
		s.env = maps.Clone(ws.Env.Value.Vars)
		s.envKeys = slices.Clone(ws.Env.Value.Keys)
	}
	secrets := []map[string]string{s.env}
	for _, f := range ws.SensorEnv {
		if f.Value == nil {
			continue
		}
		s.sensorEnv[f.Name] = maps.Clone(f.Value.Vars)
		// A per-sensor value is a secret everywhere the global env is.
		secrets = append(secrets, f.Value.Vars)
	}
	s.red = runner.NewRedactor(secrets...)
	for _, f := range ws.Sensors {
		if f.Valid() {
			s.sensors[f.Name] = f.Value
		}
	}
	for _, f := range ws.Prompts {
		if f.Valid() {
			s.prompts[f.Name] = f.Value
		}
	}
	for _, f := range ws.Flows {
		if !f.Valid() {
			continue
		}
		s.flows[f.Name] = &flowDef{flow: f.Value, raw: f.Raw, hash: f.Hash}
		if !f.Value.Enabled {
			continue
		}
		for _, sub := range f.Value.Subscribe {
			s.subs[sub] = append(s.subs[sub], f.Name)
		}
	}
	for _, ids := range s.subs {
		sort.Strings(ids)
	}
	return s
}

// snapshot returns the current workspace view.
func (e *Engine) snapshot() *snapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.snap
}

// Apply hands the engine a freshly loaded workspace (startup and hot reload).
// Running work keeps going; queued work and the next ticks see the new files.
func (e *Engine) Apply(ws *spec.Workspace) {
	if ws == nil {
		return
	}
	snap := newSnapshot(ws)
	e.mu.Lock()
	e.snap = snap
	e.mu.Unlock()
	e.syncSensors()
	e.wake()
}

// maxPerFlow is the concurrency cap of a flow for ClaimNextFlowRun.
func (e *Engine) maxPerFlow(flowID string) int {
	if fd := e.snapshot().flows[flowID]; fd != nil && fd.flow.MaxConcurrent > 0 {
		return fd.flow.MaxConcurrent
	}
	return spec.DefaultMaxConcurrent
}

// runtimeConfig returns the agent.RuntimeConfig of a configured runtime, with
// the {{data}} placeholder resolved. The isolated agent homes it names are
// created here (see ensureAgentHomes).
func (e *Engine) runtimeConfig(snap *snapshot, name string) (agent.RuntimeConfig, bool) {
	rt, ok := snap.cfg.Runtimes[name]
	if !ok {
		return agent.RuntimeConfig{}, false
	}
	sub := func(s string) string { return strings.ReplaceAll(s, spec.DataPlaceholder, e.data) }
	subAll := func(in []string) []string {
		out := make([]string, len(in))
		for i, v := range in {
			out[i] = sub(v)
		}
		return out
	}
	subMap := func(in map[string]string) map[string]string {
		if in == nil {
			return nil
		}
		out := make(map[string]string, len(in))
		for k, v := range in {
			out[k] = sub(v)
		}
		return out
	}
	cfg := agent.RuntimeConfig{
		Command:     subAll(rt.Command),
		Modes:       agent.ModeNames{Ask: rt.Modes.Ask, Full: rt.Modes.Full},
		Env:         subMap(rt.Env),
		Ask:         agent.PolicySpawn{Args: subAll(rt.Ask.Args), Env: subMap(rt.Ask.Env)},
		Full:        agent.PolicySpawn{Args: subAll(rt.Full.Args), Env: subMap(rt.Full.Env)},
		Elicitation: rt.Elicitation,
		SessionMeta: rt.SessionMeta,
	}
	if len(rt.Ask.Args) == 0 {
		cfg.Ask.Args = nil
	}
	if len(rt.Full.Args) == 0 {
		cfg.Full.Args = nil
	}
	e.ensureAgentHomes(cfg)
	return cfg, true
}

// isolatedHome describes an agent config directory the harness owns: user
// settings in the real home can auto-allow tools and bypass the approval
// policy (docs/acp-gate.md 1b), so the agent runs against a stable, separate
// directory that only holds a symlink to the existing login. The directory
// must stay stable so session/load works across processes.
var isolatedHomes = map[string][]struct{ name, target string }{
	"CODEX_HOME":          {{"auth.json", "~/.codex/auth.json"}},
	"PI_CODING_AGENT_DIR": {{"agent.db", "~/.omp/agent/agent.db"}},
}

// ensureAgentHomes creates the isolated home directories named by the
// runtime's environment and links the credential files into them. Missing
// credentials are skipped: the agent then reports its own login error.
func (e *Engine) ensureAgentHomes(cfg agent.RuntimeConfig) {
	for _, envs := range []map[string]string{cfg.Env, cfg.Ask.Env, cfg.Full.Env} {
		for key, dir := range envs {
			links, ok := isolatedHomes[key]
			if !ok || dir == "" {
				continue
			}
			if err := os.MkdirAll(dir, 0o700); err != nil {
				e.log.Warn("engine: create agent home", "dir", dir, "err", err)
				continue
			}
			for _, l := range links {
				target, err := agent.ExpandHome(l.target)
				if err != nil {
					continue
				}
				if _, err := os.Stat(target); err != nil {
					continue
				}
				link := filepath.Join(dir, l.name)
				if _, err := os.Lstat(link); err == nil {
					continue
				}
				if err := os.Symlink(target, link); err != nil {
					e.log.Warn("engine: link agent credentials", "link", link, "err", err)
				}
			}
		}
	}
}

// sensorSig fingerprints everything that affects how a sensor is scheduled and run.
func sensorSig(s *spec.Sensor) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// pollEnv is the env-file layer of a sensor's process: the global values with
// the sensor's own values on top.
func (s *snapshot) pollEnv(sensorID string) map[string]string {
	own := s.sensorEnv[sensorID]
	if len(own) == 0 {
		return s.env
	}
	out := maps.Clone(s.env)
	maps.Copy(out, own)
	return out
}

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hal9001/internal/agent"
	"hal9001/internal/core"
	"hal9001/internal/runner"
	"hal9001/internal/store"
)

const probeTimeout = 60 * time.Second

// ProbeRuntime starts a configured runtime, performs the ACP handshake plus a
// throwaway session/new, and reports what the agent advertised. A runtime
// that fails to start or answer is a result with OK false, not an error; an
// unknown runtime name is store.ErrNotFound.
func (e *Engine) ProbeRuntime(ctx context.Context, name string) (core.ProbeResult, error) {
	snap := e.snapshot()
	cfg, ok := e.runtimeConfig(snap, name)
	if !ok {
		return core.ProbeResult{}, fmt.Errorf("%w: runtime %q", store.ErrNotFound, name)
	}
	dir, err := ensureDir(filepath.Join(e.data, "state", "_probe"))
	if err != nil {
		return core.ProbeResult{}, err
	}
	env, cleanup, err := runner.BuildEnv(runner.EnvOptions{Path: e.opt.BasePATH, EnvFile: snap.env})
	if err != nil {
		return core.ProbeResult{}, err
	}
	defer cleanup()
	environ := make([]string, 0, len(env))
	for _, k := range sortedKeys(env) {
		environ = append(environ, k+"="+env[k])
	}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	res, perr := agent.Probe(pctx, cfg, dir, environ, 2*time.Second)

	out := core.ProbeResult{
		OK:              perr == nil,
		Capabilities:    res.Capabilities,
		Modes:           res.Modes,
		ConfigOptionIDs: res.ConfigOptionIDs,
		Duration:        res.Duration,
		ResolvedCommand: resolveCommand(res.Argv, env["PATH"]),
	}
	if res.AgentInfo != nil {
		out.AgentInfo, _ = json.Marshal(res.AgentInfo)
	}
	if perr != nil {
		msg := snap.red.String(perr.Error())
		if res.Stderr != "" {
			msg += "\nstderr: " + snap.red.String(lastLines(res.Stderr, 10))
		}
		out.Error = msg
	}
	return out, nil
}

// resolveCommand replaces argv[0] with the executable it resolves to on PATH,
// or leaves it as is when it cannot be found.
func resolveCommand(argv []string, pathEnv string) []string {
	if len(argv) == 0 {
		return nil
	}
	out := append([]string(nil), argv...)
	if strings.Contains(argv[0], "/") {
		return out
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		p := filepath.Join(dir, argv[0])
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0 {
			out[0] = p
			break
		}
	}
	return out
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

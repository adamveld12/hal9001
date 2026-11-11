package runtimes

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"hal9001/internal/runner"
)

const (
	// versionTimeout bounds `<tool> --version`; a shim that hangs counts as a broken tool.
	versionTimeout = 8 * time.Second
	// foundTTL and missingTTL are how long a tool lookup is remembered. A tool that was missing is looked for again
	// sooner so an install shows up quickly.
	foundTTL   = 5 * time.Minute
	missingTTL = 30 * time.Second
)

// tool is the outcome of looking for one executable on PATH.
type tool struct {
	found   bool
	path    string
	version string
}

type toolEntry struct {
	mu  sync.Mutex
	res tool
	at  time.Time
	ok  bool // res is set
}

var versionRE = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?(?:[-+][0-9A-Za-z.]+)?`)

// parseVersion extracts the first version number from the output of `--version`.
func parseVersion(out string) string {
	return versionRE.FindString(out)
}

// nodeMajor is the major version of a `node --version` result such as "24.22.1", or -1.
func nodeMajor(version string) int {
	major, _, _ := strings.Cut(strings.TrimPrefix(version, "v"), ".")
	n, err := strconv.Atoi(major)
	if err != nil {
		return -1
	}
	return n
}

// candidates lists every executable called name in the directories of pathEnv, in order, without duplicates.
func candidates(name, pathEnv string) []string {
	var out []string
	seen := map[string]bool{}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, name)
		if seen[p] {
			continue
		}
		seen[p] = true
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0 {
			out = append(out, p)
		}
	}
	return out
}

// lookup finds name on PATH. Every candidate is run with --version in a neutral directory and the first one that
// exits cleanly wins, so a shim that exists but cannot run (a mise or asdf shim without a configured version) counts
// as not found. Results are cached.
func (m *Manager) lookup(ctx context.Context, name string) tool {
	m.toolsMu.Lock()
	e := m.tools[name]
	if e == nil {
		e = &toolEntry{}
		m.tools[name] = e
	}
	m.toolsMu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ok {
		ttl := missingTTL
		if e.res.found {
			ttl = foundTTL
		}
		if time.Since(e.at) < ttl {
			return e.res
		}
	}
	res := tool{}
	for _, p := range candidates(name, m.path) {
		out, code, err := m.run(ctx, versionTimeout, p, "--version")
		if err != nil || code != 0 {
			continue
		}
		res = tool{found: true, path: p, version: parseVersion(out)}
		break
	}
	e.res, e.at, e.ok = res, time.Now(), true
	return res
}

// run executes argv[0] with args in a neutral directory with the detection environment and returns its stdout and
// stderr together and the exit code. err is set when the process could not start or timed out.
func (m *Manager) run(ctx context.Context, timeout time.Duration, argv0 string, args ...string) (out string, code int, err error) {
	res, err := runner.Run(ctx, runner.Spec{
		Lang:        runner.LangExec,
		Command:     append([]string{argv0}, args...),
		Cwd:         os.TempDir(),
		BaseEnv:     m.env(),
		Timeout:     timeout,
		GracePeriod: time.Second,
		MaxOutput:   1 << 20,
	})
	if err != nil {
		return "", -1, err
	}
	out = string(res.Stdout)
	if s := strings.TrimSpace(string(res.Stderr)); s != "" {
		out += "\n" + s
	}
	if res.TimedOut {
		return out, res.ExitCode, context.DeadlineExceeded
	}
	return out, res.ExitCode, nil
}

// env is the environment detection commands run in: the daemon's base environment with the effective PATH, plus the
// values of the env file, as an agent process gets them.
func (m *Manager) env() map[string]string {
	env := runner.DefaultBaseEnv(m.path)
	if m.opt.EnvFile != nil {
		for k, v := range m.opt.EnvFile() {
			if k != "PATH" {
				env[k] = v
			}
		}
	}
	return env
}

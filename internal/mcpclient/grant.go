package mcpclient

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Environment variables a script that declares mcp_servers gets: where the daemon is and the run token. The script never
// sees credentials of the servers.
const (
	EnvURL      = "HAL9001_URL"
	EnvRunToken = "HAL9001_RUN_TOKEN" //nolint:gosec // an environment variable name, not a credential
)

// ScriptAccess is the MCP access of one script run.
type ScriptAccess struct {
	// Env is added to the script's environment (and PATH is replaced when set).
	Env   map[string]string
	grant *RunGrant
	token string
	m     *Manager
}

// Calls returns the calls the script has made so far.
func (a *ScriptAccess) Calls() []CallLog { return a.grant.Calls() }

// Revoke ends the run token. It is safe to call more than once.
func (a *ScriptAccess) Revoke() { a.m.RevokeRunToken(a.token) }

// GrantScript issues a run token for servers and returns the environment that lets `hal9001 mcp call` reach the daemon at
// baseURL with it, or nil when servers is empty. When executable is named hal9001, its directory is put in front of path so
// the command resolves even if the command-line tool was never installed. ttl is how long the token may live at most.
func (m *Manager) GrantScript(runID, sensor string, servers []string, ttl time.Duration, baseURL, executable, path string) (*ScriptAccess, error) {
	if len(servers) == 0 {
		return nil, nil
	}
	token, grant, err := m.IssueRunToken(runID, sensor, servers, ttl+time.Minute)
	if err != nil {
		return nil, err
	}
	env := map[string]string{EnvURL: baseURL, EnvRunToken: token}
	if dir := executableDir(executable); dir != "" {
		env["PATH"] = dir + string(os.PathListSeparator) + path
	}
	return &ScriptAccess{Env: env, grant: grant, token: token, m: m}, nil
}

// executableDir is the directory of the hal9001 binary; empty when exe (default: this process) is not named hal9001, as
// in a test binary.
func executableDir(exe string) string {
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return ""
		}
	}
	if strings.TrimSuffix(filepath.Base(exe), ".exe") != "hal9001" {
		return ""
	}
	return filepath.Dir(exe)
}

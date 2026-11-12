package mcpclient

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// RunTokenPrefix starts every run token. The token is the only thing a sensor script holds: it names the run and the
// servers the sensor declares, and it opens nothing else.
const RunTokenPrefix = "hal_run_"

// RunGrant is the authority of one run token: the servers it may call and the calls made with it.
type RunGrant struct {
	// RunID is the sensor run (or assist test) the token belongs to; Sensor the sensor id.
	RunID, Sensor string
	servers       map[string]bool
	expires       time.Time

	mu    sync.Mutex
	calls []CallLog
}

// Allows reports whether the token grants server.
func (g *RunGrant) Allows(server string) bool { return g.servers[server] }

// Servers lists the granted servers, sorted.
func (g *RunGrant) Servers() []string {
	out := make([]string, 0, len(g.servers))
	for s := range g.servers {
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// Calls returns the calls made with the token so far, in order.
func (g *RunGrant) Calls() []CallLog {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.calls)
}

func (g *RunGrant) record(c CallLog) {
	g.mu.Lock()
	g.calls = append(g.calls, c)
	g.mu.Unlock()
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// IssueRunToken makes a token that lets the holder call servers, for runID of sensor, until Revoke or ttl. Tokens live in
// memory only: a daemon restart ends every run, so none survives one.
func (m *Manager) IssueRunToken(runID, sensor string, servers []string, ttl time.Duration) (token string, grant *RunGrant, err error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", nil, err
	}
	token = RunTokenPrefix + base64.RawURLEncoding.EncodeToString(raw[:])
	g := &RunGrant{RunID: runID, Sensor: sensor, servers: make(map[string]bool, len(servers)), expires: m.now().Add(ttl)}
	for _, s := range servers {
		g.servers[s] = true
	}
	m.mu.Lock()
	for h, old := range m.grants { // drop what ran out without being revoked
		if m.now().After(old.expires) {
			delete(m.grants, h)
		}
	}
	m.grants[hashToken(token)] = g
	m.mu.Unlock()
	return token, g, nil
}

// RevokeRunToken ends a token.
func (m *Manager) RevokeRunToken(token string) {
	m.mu.Lock()
	delete(m.grants, hashToken(token))
	m.mu.Unlock()
}

// LookupRunToken returns the grant of a live run token.
func (m *Manager) LookupRunToken(token string) (*RunGrant, bool) {
	if !strings.HasPrefix(token, RunTokenPrefix) {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.grants[hashToken(token)]
	if ok && m.now().After(g.expires) {
		delete(m.grants, hashToken(token))
		return nil, false
	}
	return g, ok
}

// CallWith calls a tool on behalf of a run token. A server the token does not grant is refused without being contacted.
// Every call, allowed or not, is logged on the grant and in the daemon log.
func (m *Manager) CallWith(ctx context.Context, g *RunGrant, server, tool string, args map[string]any) (CallResult, error) {
	start := m.now()
	var res CallResult
	var err error
	if !g.Allows(server) {
		err = fmt.Errorf("%w: %q is not among this run's mcp_servers (%s)", ErrNotAllowed, server, strings.Join(g.Servers(), ", "))
	} else {
		res, err = m.Call(ctx, server, tool, args)
	}
	c := CallLog{Server: server, Tool: tool, At: start.UTC(), DurationMS: m.now().Sub(start).Milliseconds(), OK: err == nil && !res.IsError}
	if err != nil {
		c.Error = err.Error()
	} else if res.IsError {
		c.Error = "the tool reported an error"
	}
	g.record(c)
	m.opt.Log.Info("mcp call", "run", g.RunID, "sensor", g.Sensor, "server", server, "tool", tool, "ok", c.OK, "ms", c.DurationMS)
	return res, err
}

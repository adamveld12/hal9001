package engine

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"hal9001/internal/mcpclient"
	"hal9001/internal/notify"
	"hal9001/internal/store"
)

// mcpNoteClient labels the Inbox notes of the MCP client (they are notes of source mcp, like those of external clients).
const mcpNoteClient = "hal9001 MCP servers"

// PostNote implements mcpclient.Notifier: an Inbox note, and a notification when its priority reaches the threshold.
func (e *Engine) PostNote(n mcpclient.Note) {
	red := e.snapshot().red
	link := e.url(n.LinkPath)
	thread := "mcp-server:" + n.Server + ":" + n.Kind
	it := &store.InboxItem{
		SourceType: store.SourceMCP, Title: red.String(n.Title), Priority: store.Priority(n.Priority), SummaryMD: red.String(n.BodyMD),
		Links: []store.InboxLink{{Label: n.LinkLabel, URL: link}}, Client: mcpNoteClient, ThreadKey: thread,
	}
	if err := e.st.CreateInboxItem(e.dbCtx, it); err != nil {
		e.log.Error("engine: post MCP server note", "server", n.Server, "err", err)
		return
	}
	e.notify(notify.FromMCP(notify.Note(notify.MCPFlowID, it, link), mcpNoteClient, thread))
	e.log.Info("engine: mcp server note posted", "item", it.ID, "server", n.Server, "kind", n.Kind)
}

// mcpRun is the MCP access of one sensor run: the env that lets its script call the declared servers, and what to
// do with the calls afterwards.
type mcpRun struct {
	// Env is added to the script's environment.
	Env map[string]string
	// Calls returns the calls made so far.
	Calls func() []store.SensorMCPCall
	// Revoke ends the run token.
	Revoke func()
}

// grantMCP issues a run token for the sensor's mcp_servers and returns what the script needs. It returns nil when the
// sensor declares no server (or the daemon has no MCP client).
func (e *Engine) grantMCP(runID, sensorID string, servers []string, ttl time.Duration, path string) (*mcpRun, error) {
	if len(servers) == 0 || e.opt.MCPClient == nil {
		return nil, nil
	}
	token, grant, err := e.opt.MCPClient.IssueRunToken(runID, sensorID, servers, ttl+time.Minute)
	if err != nil {
		return nil, err
	}
	env := map[string]string{
		"HAL9001_URL":       e.opt.MCPURL,
		"HAL9001_RUN_TOKEN": token,
	}
	if dir := e.executableDir(); dir != "" {
		env["PATH"] = dir + string(os.PathListSeparator) + path
	}
	return &mcpRun{
		Env: env,
		Calls: func() []store.SensorMCPCall {
			logs := grant.Calls()
			out := make([]store.SensorMCPCall, 0, len(logs))
			for _, c := range logs {
				out = append(out, store.SensorMCPCall{Server: c.Server, Tool: c.Tool, At: c.At, DurationMS: c.DurationMS, OK: c.OK, Error: c.Error})
			}
			return out
		},
		Revoke: func() { e.opt.MCPClient.RevokeRunToken(token) },
	}, nil
}

// executableDir is the directory of the hal9001 binary, so that `hal9001 mcp call` resolves in a script even when
// the command-line tool was never installed. Empty when the binary is not named hal9001 (a test binary).
func (e *Engine) executableDir() string {
	exe := e.opt.Executable
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

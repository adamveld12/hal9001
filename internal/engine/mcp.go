package engine

import (
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
	acc, err := e.opt.MCPClient.GrantScript(runID, sensorID, servers, ttl, e.opt.MCPURL, e.opt.Executable, path)
	if err != nil {
		return nil, err
	}
	return &mcpRun{
		Env: acc.Env,
		Calls: func() []store.SensorMCPCall {
			logs := acc.Calls()
			out := make([]store.SensorMCPCall, 0, len(logs))
			for _, c := range logs {
				out = append(out, store.SensorMCPCall{Server: c.Server, Tool: c.Tool, At: c.At, DurationMS: c.DurationMS, OK: c.OK, Error: c.Error})
			}
			return out
		},
		Revoke: acc.Revoke,
	}, nil
}

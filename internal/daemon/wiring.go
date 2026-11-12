package daemon

import (
	"context"
	"log/slog"
	"net/http"

	"hal9001/internal/agent"
	"hal9001/internal/api"
	"hal9001/internal/auth"
	"hal9001/internal/core"
	"hal9001/internal/inboxact"
	"hal9001/internal/mcpclient"
	"hal9001/internal/notify"
	"hal9001/internal/runtimes"
	"hal9001/internal/spec"
	"hal9001/internal/store"
	"hal9001/internal/tailscale"
	"hal9001/internal/tools"
	"hal9001/internal/update"
)

// runtimeEngine is the engine as the daemon drives it: the core.Engine
// contract plus its lifecycle.
type runtimeEngine interface {
	core.Engine
	// Start runs store.MarkInterrupted and starts schedulers, queues and
	// retention. It is called after the first Apply.
	Start(ctx context.Context) error
	// Interrupt cancels running work as interrupted without waiting. It is
	// called before in-flight HTTP calls are cancelled (see Stop).
	Interrupt()
	// Stop shuts the engine down gracefully.
	Stop(ctx context.Context) error
}

// engineParams are the inputs of engine.New (see wiring_engine.go).
type engineParams struct {
	Store     *store.Store
	Home      string
	BasePATH  string
	ExactPATH bool
	Notifier  core.Notifier
	Log       *slog.Logger
	UIBaseURL string
	Clock     agent.Clock
	// MCPURL and Executable are how agents reach hal's MCP server (see engine.Options).
	MCPURL     string
	Executable string
	// MCPClient is the remote MCP client; sensors that declare mcp_servers get run tokens for it.
	MCPClient *mcpclient.Manager
}

// apiParams are the inputs of api.New (see wiring_engine.go).
type apiParams struct {
	Store        *store.Store
	Home         string
	Engine       core.Engine
	Assist       core.Assist
	InboxActions *inboxact.Service
	Auth         *auth.Manager
	MCP          api.MCPHub
	Operator     *tools.Operator
	// Port and Executable are what GET /api/mcp/info reports (see api.Options).
	Port       int
	Executable string
	Workspace  func() *spec.Workspace
	Reload     func()
	BasePATH   string
	Runtimes   *runtimes.Manager
	Version    string
	UI         http.Handler
	Log        *slog.Logger
	Sinks      *notify.Sinks
	Update     *update.Service
	Tailscale  *tailscale.Manager
	MCPClient  *mcpclient.Manager
}

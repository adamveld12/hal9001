package daemon

import (
	"context"
	"log/slog"
	"net/http"

	"hal9001/internal/auth"
	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// runtimeEngine is the engine as the daemon drives it: the core.Engine
// contract plus its lifecycle.
type runtimeEngine interface {
	core.Engine
	// Start runs store.MarkInterrupted and starts schedulers, queues and
	// retention. It is called after the first Apply.
	Start(ctx context.Context) error
	// Stop shuts the engine down gracefully.
	Stop(ctx context.Context) error
}

// engineParams are the inputs of engine.New (see wiring_integrated.go).
type engineParams struct {
	Store     *store.Store
	Home      string
	BasePATH  string
	Notifier  core.Notifier
	Log       *slog.Logger
	UIBaseURL string
}

// apiParams are the inputs of api.New (see wiring_integrated.go).
type apiParams struct {
	Store     *store.Store
	Home      string
	Engine    core.Engine
	Auth      *auth.Manager
	Workspace func() *spec.Workspace
	Reload    func()
	Version   string
	UI        http.Handler
	Log       *slog.Logger
}

package daemon

import (
	"net/http"

	"hal9001/internal/api"
	"hal9001/internal/engine"
)

func newEngine(p engineParams) (*engine.Engine, error) {
	return engine.New(engine.Options{
		Store:     p.Store,
		Home:      p.Home,
		BasePATH:  p.BasePATH,
		ExactPATH: p.ExactPATH,
		Notifier:  p.Notifier,
		Log:       p.Log,
		UIBaseURL: p.UIBaseURL,
		Clock:     p.Clock,
		MCPURL:    p.MCPURL, Executable: p.Executable,
	})
}

func newAPI(p apiParams) http.Handler {
	return api.New(api.Options{
		Store:      p.Store,
		Home:       p.Home,
		Engine:     p.Engine,
		Assist:     p.Assist,
		Auth:       p.Auth,
		MCP:        p.MCP,
		Operator:   p.Operator,
		Port:       p.Port,
		Executable: p.Executable,
		Workspace:  p.Workspace,
		Reload:     p.Reload,
		BasePATH:   p.BasePATH,
		Runtimes:   p.Runtimes,
		Version:    p.Version,
		UI:         p.UI,
		Log:        p.Log,
		Sinks:      p.Sinks,
		Update:     p.Update,
		Tailscale:  p.Tailscale,
	})
}

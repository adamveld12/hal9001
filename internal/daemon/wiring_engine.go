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
		Notifier:  p.Notifier,
		Log:       p.Log,
		UIBaseURL: p.UIBaseURL,
		Clock:     p.Clock,
	})
}

func newAPI(p apiParams) http.Handler {
	return api.New(api.Options{
		Store:     p.Store,
		Home:      p.Home,
		Engine:    p.Engine,
		Assist:    p.Assist,
		Auth:      p.Auth,
		Workspace: p.Workspace,
		Reload:    p.Reload,
		Version:   p.Version,
		UI:        p.UI,
		Log:       p.Log,
	})
}

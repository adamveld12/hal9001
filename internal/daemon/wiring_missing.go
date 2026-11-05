//go:build !integrated

package daemon

import (
	"errors"
	"net/http"
)

// Until internal/engine and internal/api are merged, the daemon is built
// without them (build tag `integrated` selects wiring_integrated.go). The
// integration step deletes this file and the tag line of
// wiring_integrated.go.
var errNotIntegrated = errors.New("daemon: this binary was built without the engine and API packages (rebuild with -tags integrated)")

func newEngine(engineParams) (runtimeEngine, error) { return nil, errNotIntegrated }

func newAPI(apiParams) http.Handler { return nil }

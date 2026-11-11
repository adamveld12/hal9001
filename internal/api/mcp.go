package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"hal9001/internal/httpsec"
	"hal9001/internal/tools"
)

// MCPHub resolves a per-run capability token to its tool session (see internal/tools).
type MCPHub interface {
	Lookup(token string) (*tools.Session, bool)
}

// mcpSession authorizes a request by its bearer capability token. The /api/mcp/ operations are x-hal-public: the
// token, not the auth stack, is the credential. It answers 401 or 503 itself and returns false on failure.
func (s *Server) mcpSession(w http.ResponseWriter, r *http.Request) (*tools.Session, bool) {
	if s.opts.MCP == nil {
		problem(w, http.StatusServiceUnavailable, typeUnavailable, "Unavailable", "the daemon has no MCP tool hub", nil)
		return nil, false
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if ok {
		if sess, found := s.opts.MCP.Lookup(strings.TrimSpace(token)); found {
			return sess, true
		}
	}
	problem(w, http.StatusUnauthorized, httpsec.TypeUnauthenticated, "Unauthorized",
		"a valid, unrevoked run capability token is required", map[string]any{"service": "api", "methods": []string{}})
	return nil, false
}

func (s *Server) listMcpTools(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.mcpSession(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": sess.Tools()})
}

func (s *Server) markMcpReady(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.mcpSession(w, r)
	if !ok {
		return
	}
	sess.MarkReady()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) callMcpTool(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.mcpSession(w, r)
	if !ok {
		return
	}
	var req struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if req.Name == "" {
		badRequest(w, "name is required")
		return
	}
	ctx := r.Context()
	if sess.Kind() == tools.KindAssist {
		// The read tools of an assist session read hal's REST API (internal/tools/readtools.go); flow runs never get it.
		ctx = tools.WithAPI(ctx, s.readDispatcher(r))
	}
	res, err := sess.Call(ctx, req.Name, req.Arguments)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"text": err.Error(), "is_error": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": res.Text, "is_error": false})
}

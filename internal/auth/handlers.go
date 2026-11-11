package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"hal9001/internal/httpsec"
)

const maxBody = 64 << 10

// Routes registers the /api/auth endpoints on mux:
//
//	POST   /api/auth/{service}/begin        start a login (no auth required)
//	POST   /api/auth/{service}/continue     answer a challenge (no auth required)
//	POST   /api/auth/logout                 revoke the current session
//	GET    /api/auth/whoami                 who the caller is, and for which services
//	GET    /api/auth/sessions               list active sessions        (admin)
//	DELETE /api/auth/sessions/{id}          revoke one session          (admin)
//	DELETE /api/auth/sessions               revoke all sessions         (admin)
//	GET    /api/auth/events?limit=N         recent auth events          (admin)
//	GET    /api/auth/webauthn/credentials   list passkeys               (admin)
//	DELETE /api/auth/webauthn/credentials/{id}  remove a passkey        (admin)
//	POST   /api/auth/webauthn/register/begin    start passkey registration  (session covering admin)
//	POST   /api/auth/webauthn/register/finish   finish it                   (session covering admin)
func (m *Manager) Routes(mux *http.ServeMux) {
	admin := m.Require(ServiceAdmin)
	mux.HandleFunc("POST /api/auth/{service}/begin", m.handleBegin)
	mux.HandleFunc("POST /api/auth/{service}/continue", m.handleContinue)
	mux.HandleFunc("POST /api/auth/logout", m.handleLogout)
	mux.HandleFunc("GET /api/auth/whoami", m.handleWhoami)
	mux.Handle("GET /api/auth/sessions", admin(http.HandlerFunc(m.handleListSessions)))
	mux.Handle("DELETE /api/auth/sessions/{id}", admin(http.HandlerFunc(m.handleRevokeSession)))
	mux.Handle("DELETE /api/auth/sessions", admin(http.HandlerFunc(m.handleRevokeAll)))
	mux.Handle("GET /api/auth/events", admin(http.HandlerFunc(m.handleEvents)))
	mux.Handle("GET /api/auth/webauthn/credentials", admin(http.HandlerFunc(m.handleListCredentials)))
	mux.Handle("DELETE /api/auth/webauthn/credentials/{id}", admin(http.HandlerFunc(m.handleDeleteCredential)))
	mux.HandleFunc("POST /api/auth/webauthn/register/begin", m.handleRegisterBegin)
	mux.HandleFunc("POST /api/auth/webauthn/register/finish", m.handleRegisterFinish)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func badRequest(w http.ResponseWriter, detail string) {
	httpsec.WriteProblem(w, httpsec.Problem{Type: httpsec.TypeBadRequest, Title: "Bad request", Status: http.StatusBadRequest, Detail: detail})
}

func internalError(w http.ResponseWriter) {
	httpsec.WriteProblem(w, httpsec.Problem{Type: httpsec.TypeInternal, Title: "Internal server error", Status: http.StatusInternalServerError})
}

// readObject decodes a JSON object body; an empty body is an empty object.
func readObject(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		badRequest(w, "cannot read request body")
		return nil, false
	}
	obj := map[string]json.RawMessage{}
	if len(trimSpace(body)) == 0 {
		return obj, true
	}
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		badRequest(w, "body must be a JSON object")
		return nil, false
	}
	return obj, true
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\n' || b[0] == '\t' || b[0] == '\r') {
		b = b[1:]
	}
	return b
}

func parseService(w http.ResponseWriter, r *http.Request) (Service, bool) {
	svc := Service(r.PathValue("service"))
	if !svc.valid() {
		httpsec.WriteProblem(w, httpsec.Problem{Type: httpsec.TypeNotFound, Title: "Unknown service", Status: http.StatusNotFound,
			Detail: fmt.Sprintf("%q is not one of ui, api, approve, admin", svc)})
		return "", false
	}
	return svc, true
}

type sessionView struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
	Services  []Service `json:"services"`
}

func viewOf(s Session) sessionView {
	return sessionView{ID: s.ID, ExpiresAt: s.ExpiresAt.UTC(), Services: s.Services}
}

type traceView struct {
	Module string     `json:"module"`
	Result ResultKind `json:"result"`
}

// publicTrace omits reasons: they can reveal configuration to an
// unauthenticated caller and are available to admins in auth events.
func publicTrace(trace []ModuleTrace) []traceView {
	out := make([]traceView, len(trace))
	for i, t := range trace {
		out[i] = traceView{Module: t.Module, Result: t.Result}
	}
	return out
}

func (m *Manager) handleBegin(w http.ResponseWriter, r *http.Request) {
	svc, ok := parseService(w, r)
	if !ok {
		return
	}
	params, ok := readObject(w, r)
	if !ok {
		return
	}
	c := m.Config()
	req := m.newRequest(r, c, svc, true)
	req.Params = params
	if raw, ok := params["method"]; ok {
		_ = json.Unmarshal(raw, &req.Method)
	}

	if s, _, ok := m.activeSession(r); ok && s.Covers(svc) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "success", "service": svc, "principal": Principal{Name: s.Principal},
			"modules": s.Modules, "services": s.Services, "session": viewOf(s), "already_authenticated": true,
		})
		return
	}
	ev := m.stackFor(c, svc).Evaluate(r.Context(), req)
	m.finishLogin(w, r, c, req, "login_begin", ev)
}

func (m *Manager) handleContinue(w http.ResponseWriter, r *http.Request) {
	svc, ok := parseService(w, r)
	if !ok {
		return
	}
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	var loginID string
	if raw, ok := obj["login_id"]; !ok || json.Unmarshal(raw, &loginID) != nil || loginID == "" {
		badRequest(w, "login_id is required")
		return
	}
	c := m.Config()
	req := m.newRequest(r, c, svc, true)
	p, ok := m.logins.take(loginID, m.now())
	peer, _ := netip.ParseAddrPort(r.RemoteAddr)
	if !ok || p.service != svc || p.host != r.Host || p.peer != peer.Addr().Unmap() {
		httpsec.WriteProblem(w, httpsec.Problem{Type: httpsec.TypeLoginExpired, Title: "Login exchange expired or unknown", Status: http.StatusGone,
			Detail: "start again with POST /api/auth/" + string(svc) + "/begin"})
		return
	}
	req.Settings = c.settings()
	ev := p.stack.Resume(r.Context(), req, p.paused, StepResponse{Response: obj["response"]})
	m.finishLogin(w, r, c, req, "login_continue", ev)
}

// finishLogin turns an evaluation of begin/continue into a response and an
// auth event.
func (m *Manager) finishLogin(w http.ResponseWriter, r *http.Request, c Config, req *AuthRequest, action string, ev Evaluation) {
	svc := req.Service
	switch ev.Outcome {
	case OutcomeSuccess:
		s, err := m.establishSession(r.Context(), w, r, c, svc, ev)
		if err != nil {
			m.log.Error("auth: establish session failed", "err", err)
			e := eventFor(req, action, ev, "")
			e.Outcome, e.Reason, e.Principal = OutcomeFail, "session could not be created", ""
			m.record(r.Context(), e)
			internalError(w)
			return
		}
		m.record(r.Context(), eventFor(req, action, ev, s.ID))
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "success", "service": svc, "principal": ev.Principal, "modules": ev.Modules,
			"services": s.Services, "session": viewOf(s), "trace": publicTrace(ev.Trace),
		})
	case OutcomeChallenge:
		id, err := newToken()
		if err != nil {
			internalError(w)
			return
		}
		peer, _ := netip.ParseAddrPort(r.RemoteAddr)
		now := m.now()
		m.logins.put(id, &pendingLogin{
			service: svc, host: r.Host, peer: peer.Addr().Unmap(),
			stack:  Stack{Name: c.Services[svc], Entries: append([]StackEntry(nil), c.Stacks[c.Services[svc]]...), Registry: m.reg},
			paused: ev.Pending, created: now, expires: now.Add(loginTTL),
		}, now)
		m.record(r.Context(), eventFor(req, action, ev, ""))
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "challenge", "service": svc, "login_id": id, "module": ev.Pending.Module,
			"step": ev.Pending.Step, "methods": m.loginMethods(c, svc), "trace": publicTrace(ev.Trace),
		})
	default:
		m.record(r.Context(), eventFor(req, action, ev, ""))
		w.Header().Set("WWW-Authenticate", `Cookie realm="hal9001"`)
		httpsec.WriteProblem(w, httpsec.Problem{
			Type: httpsec.TypeAuthenticationFail, Title: "Authentication failed", Status: http.StatusUnauthorized,
			Detail:     "no module authenticated the request",
			Extensions: map[string]any{"service": svc, "methods": m.loginMethods(c, svc), "trace": publicTrace(ev.Trace)},
		})
	}
}

func (m *Manager) handleLogout(w http.ResponseWriter, r *http.Request) {
	s, _, ok := m.activeSession(r)
	revoked := false
	if ok {
		var err error
		if revoked, err = m.store.RevokeSession(r.Context(), s.ID, m.now()); err != nil {
			m.log.Error("auth: revoke session failed", "err", err)
			internalError(w)
			return
		}
	}
	m.clearCookie(w, r)
	e := AuthEvent{Action: "logout", Endpoint: r.Method + " " + r.URL.Path, Outcome: OutcomeSuccess, RemoteAddr: r.RemoteAddr, Host: r.Host}
	if ok {
		e.Principal, e.SessionID = s.Principal, s.ID
	}
	m.record(r.Context(), e)
	writeJSON(w, http.StatusOK, map[string]any{"revoked": revoked})
}

func (m *Manager) handleWhoami(w http.ResponseWriter, r *http.Request) {
	c := m.Config()
	if s, token, ok := m.activeSession(r); ok {
		m.slide(r.Context(), w, r, &s, token, c)
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": true, "source": "session", "principal": Principal{Name: s.Principal},
			"modules": s.Modules, "services": s.Services, "session": viewOf(s),
		})
		return
	}
	// Evaluate each distinct stack once; services sharing it share the result.
	var (
		principal Principal
		modules   []string
		services  = []Service{}
		seen      = map[string]bool{}
	)
	for _, svc := range Services {
		name := c.Services[svc]
		if seen[name] {
			continue
		}
		seen[name] = true
		req := m.newRequest(r, c, svc, false)
		ev := m.stackFor(c, svc).Evaluate(r.Context(), req)
		m.record(r.Context(), eventFor(req, "whoami", ev, ""))
		if ev.Outcome == OutcomeSuccess {
			if len(services) == 0 {
				principal, modules = ev.Principal, ev.Modules
			}
			services = append(services, grantedServices(c, svc)...)
		}
	}
	if len(services) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true, "source": "stack", "principal": principal, "modules": modules, "services": services,
	})
}

func (m *Manager) handleListSessions(w http.ResponseWriter, r *http.Request) {
	list, err := m.store.ListSessions(r.Context(), m.now())
	if err != nil {
		internalError(w)
		return
	}
	if list == nil {
		list = []Session{}
	}
	cur, _ := IdentityFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"sessions": list, "current": cur.SessionID})
}

func (m *Manager) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	revoked, err := m.store.RevokeSession(r.Context(), id, m.now())
	if err != nil {
		internalError(w)
		return
	}
	if !revoked {
		httpsec.WriteProblem(w, httpsec.Problem{Type: httpsec.TypeNotFound, Title: "Session not found", Status: http.StatusNotFound, Detail: "no active session with that id"})
		return
	}
	m.recordAdmin(r, "session_revoke", id)
	writeJSON(w, http.StatusOK, map[string]any{"revoked": 1})
}

func (m *Manager) handleRevokeAll(w http.ResponseWriter, r *http.Request) {
	n, err := m.store.RevokeAllSessions(r.Context(), m.now())
	if err != nil {
		internalError(w)
		return
	}
	m.recordAdmin(r, "session_revoke_all", "")
	m.clearCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"revoked": n})
}

// recordAdmin logs an admin action taken by an already-authenticated caller.
func (m *Manager) recordAdmin(r *http.Request, action, sessionID string) {
	id, _ := IdentityFrom(r.Context())
	m.record(r.Context(), AuthEvent{
		Action: action, Service: ServiceAdmin, Endpoint: r.Method + " " + r.URL.Path, Outcome: OutcomeSuccess,
		Principal: id.Principal.Name, RemoteAddr: r.RemoteAddr, Host: r.Host, SessionID: sessionID,
	})
}

func (m *Manager) handleEvents(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			badRequest(w, "limit must be between 1 and 1000")
			return
		}
		limit = n
	}
	events, err := m.store.ListAuthEvents(r.Context(), limit)
	if err != nil {
		internalError(w)
		return
	}
	if events == nil {
		events = []AuthEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

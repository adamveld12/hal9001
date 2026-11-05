package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"

	"hal9001/internal/httpsec"
)

// requireSession admits only a real session cookie that covers admin:
// registering a passkey creates a standing credential, so it must come from a
// logged-in browser rather than a per-request stack decision.
func (m *Manager) requireSession(w http.ResponseWriter, r *http.Request) (Session, bool) {
	s, _, ok := m.activeSession(r)
	switch {
	case !ok:
		m.deny(w, ServiceAdmin, http.StatusUnauthorized)
	case !s.Covers(ServiceAdmin):
		m.deny(w, ServiceAdmin, http.StatusForbidden)
	default:
		return s, true
	}
	return Session{}, false
}

func (m *Manager) handleRegisterBegin(w http.ResponseWriter, r *http.Request) {
	s, ok := m.requireSession(w, r)
	if !ok {
		return
	}
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	var label string
	if raw, ok := obj["label"]; ok && json.Unmarshal(raw, &label) != nil {
		badRequest(w, "label must be a string")
		return
	}
	peer, _ := netip.ParseAddrPort(r.RemoteAddr)
	id, creation, err := m.wa.BeginRegistration(r.Context(), s.ID, r.Host, isSecure(r, peer.Addr()), label)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"registration_id": id, "options": creation})
}

func (m *Manager) handleRegisterFinish(w http.ResponseWriter, r *http.Request) {
	s, ok := m.requireSession(w, r)
	if !ok {
		return
	}
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	var regID string
	if raw, ok := obj["registration_id"]; !ok || json.Unmarshal(raw, &regID) != nil || regID == "" {
		badRequest(w, "registration_id is required")
		return
	}
	if len(obj["credential"]) == 0 {
		badRequest(w, "credential is required")
		return
	}
	peer, _ := netip.ParseAddrPort(r.RemoteAddr)
	cred, err := m.wa.FinishRegistration(r.Context(), s.ID, r.Host, isSecure(r, peer.Addr()), regID, obj["credential"])
	e := AuthEvent{
		Action: "webauthn_register", Service: ServiceAdmin, Endpoint: r.Method + " " + r.URL.Path,
		Principal: s.Principal, RemoteAddr: r.RemoteAddr, Host: r.Host, SessionID: s.ID,
	}
	if err != nil {
		e.Outcome, e.Reason = OutcomeFail, err.Error()
		m.record(r.Context(), e)
		if errors.Is(err, errRegistrationUnknown) {
			httpsec.WriteProblem(w, httpsec.Problem{Type: httpsec.TypeLoginExpired, Title: "Registration expired or unknown", Status: http.StatusGone, Detail: err.Error()})
			return
		}
		badRequest(w, err.Error())
		return
	}
	e.Outcome = OutcomeSuccess
	m.record(r.Context(), e)
	writeJSON(w, http.StatusCreated, cred)
}

func (m *Manager) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	list, err := m.store.ListAllWebAuthnCredentials(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	if list == nil {
		list = []WebAuthnCredential{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"credentials": list})
}

func (m *Manager) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	err := m.store.DeleteWebAuthnCredential(r.Context(), r.PathValue("id"))
	if errors.Is(err, ErrNotFound) {
		httpsec.WriteProblem(w, httpsec.Problem{Type: httpsec.TypeNotFound, Title: "Passkey not found", Status: http.StatusNotFound})
		return
	}
	if err != nil {
		internalError(w)
		return
	}
	m.recordAdmin(r, "webauthn_delete", "")
	w.WriteHeader(http.StatusNoContent)
}

// Package authstore adapts the SQLite store to the auth.Store interface. It
// maps store.ErrNotFound to auth.ErrNotFound and converts between the two
// packages' record types.
package authstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"hal9001/internal/auth"
	"hal9001/internal/store"
)

// New returns an auth.Store backed by st.
func New(st *store.Store) auth.Store { return &adapter{st: st} }

type adapter struct{ st *store.Store }

func mapErr(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return auth.ErrNotFound
	}
	return err
}

// ---- sessions ----

func (a *adapter) CreateSession(ctx context.Context, s auth.Session) error {
	ss := toStoreSession(s)
	return a.st.CreateSession(ctx, &ss)
}

func (a *adapter) GetSession(ctx context.Context, id string) (auth.Session, error) {
	ss, err := a.st.GetSession(ctx, id)
	if err != nil {
		return auth.Session{}, mapErr(err)
	}
	return fromStoreSession(*ss), nil
}

func (a *adapter) UpdateSession(ctx context.Context, s auth.Session) error {
	ss := toStoreSession(s)
	return mapErr(a.st.UpdateSession(ctx, &ss))
}

// RevokeSession revokes id when it is active at the given time. The store
// stamps the revocation with its own clock.
func (a *adapter) RevokeSession(ctx context.Context, id string, at time.Time) (bool, error) {
	cur, err := a.st.GetSession(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if !fromStoreSession(*cur).Active(at) {
		return false, nil
	}
	if err := a.st.RevokeSession(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// RevokeAllSessions revokes the sessions active at the given time and returns
// how many; expired sessions are left alone.
func (a *adapter) RevokeAllSessions(ctx context.Context, at time.Time) (int, error) {
	active, err := a.st.ListSessions(ctx, at)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range active {
		if err := a.st.RevokeSession(ctx, s.ID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return n, err
		}
		n++
	}
	return n, nil
}

func (a *adapter) ListSessions(ctx context.Context, now time.Time) ([]auth.Session, error) {
	if now.IsZero() {
		return nil, nil // the store reads a zero time as "all sessions"
	}
	rows, err := a.st.ListSessions(ctx, now)
	if err != nil {
		return nil, err
	}
	out := make([]auth.Session, len(rows))
	for i, r := range rows {
		out[i] = fromStoreSession(r)
	}
	return out, nil
}

func toStoreSession(s auth.Session) store.Session {
	svcs := make([]store.AuthService, len(s.Services))
	for i, v := range s.Services {
		svcs[i] = store.AuthService(v)
	}
	return store.Session{
		ID: s.ID, Principal: s.Principal, Modules: s.Modules, Services: svcs,
		Host: s.Host, RemoteAddr: s.RemoteAddr,
		CreatedAt: s.CreatedAt, LastSeenAt: s.LastSeenAt, ExpiresAt: s.ExpiresAt, RevokedAt: s.RevokedAt,
	}
}

func fromStoreSession(s store.Session) auth.Session {
	svcs := make([]auth.Service, len(s.Services))
	for i, v := range s.Services {
		svcs[i] = auth.Service(v)
	}
	return auth.Session{
		ID: s.ID, Principal: s.Principal, Modules: s.Modules, Services: svcs,
		Host: s.Host, RemoteAddr: s.RemoteAddr,
		CreatedAt: s.CreatedAt, LastSeenAt: s.LastSeenAt, ExpiresAt: s.ExpiresAt, RevokedAt: s.RevokedAt,
	}
}

// ---- auth events ----

func (a *adapter) AppendAuthEvent(ctx context.Context, e auth.AuthEvent) error {
	results := make([]store.ModuleResult, len(e.Modules))
	for i, m := range e.Modules {
		results[i] = store.ModuleResult{Module: m.Module, Control: string(m.Control), Result: string(m.Result), Reason: m.Reason}
	}
	se := store.AuthEvent{
		ID: e.ID, TS: e.Time, Action: e.Action, Service: store.AuthService(e.Service), Endpoint: e.Endpoint,
		Results: results, Outcome: string(e.Outcome), Reason: e.Reason, Principal: e.Principal,
		RemoteAddr: e.RemoteAddr, Host: e.Host, SessionID: e.SessionID,
	}
	return a.st.CreateAuthEvent(ctx, &se)
}

func (a *adapter) ListAuthEvents(ctx context.Context, limit int) ([]auth.AuthEvent, error) {
	page, err := a.st.ListAuthEvents(ctx, store.AuthEventFilter{ListOpts: store.ListOpts{Limit: limit}})
	if err != nil {
		return nil, err
	}
	out := make([]auth.AuthEvent, len(page.Items))
	for i, e := range page.Items {
		mods := make([]auth.ModuleTrace, len(e.Results))
		for j, r := range e.Results {
			mods[j] = auth.ModuleTrace{Module: r.Module, Control: auth.Control(r.Control), Result: auth.ResultKind(r.Result), Reason: r.Reason}
		}
		out[i] = auth.AuthEvent{
			ID: e.ID, Time: e.TS, Action: e.Action, Service: auth.Service(e.Service), Endpoint: e.Endpoint,
			Modules: mods, Outcome: auth.Outcome(e.Outcome), Reason: e.Reason, Principal: e.Principal,
			RemoteAddr: e.RemoteAddr, Host: e.Host, SessionID: e.SessionID,
		}
	}
	return out, nil
}

// ---- webauthn credentials ----

func (a *adapter) ListWebAuthnCredentials(ctx context.Context, rpID string) ([]auth.WebAuthnCredential, error) {
	if rpID == "" {
		return nil, nil // the store reads "" as "every relying party"
	}
	return a.listCreds(ctx, rpID)
}

func (a *adapter) ListAllWebAuthnCredentials(ctx context.Context) ([]auth.WebAuthnCredential, error) {
	return a.listCreds(ctx, "")
}

func (a *adapter) listCreds(ctx context.Context, rpID string) ([]auth.WebAuthnCredential, error) {
	rows, err := a.st.ListWebAuthnCredentials(ctx, rpID)
	if err != nil {
		return nil, err
	}
	out := make([]auth.WebAuthnCredential, len(rows))
	for i, r := range rows {
		out[i] = fromStoreCred(r)
	}
	return out, nil
}

func (a *adapter) AddWebAuthnCredential(ctx context.Context, c auth.WebAuthnCredential) error {
	sc := store.WebAuthnCredential{
		ID: c.ID, RPID: c.RPID, Principal: "owner", PublicKey: c.PublicKey, SignCount: c.SignCount,
		Label: c.Label, Data: c.Data, CreatedAt: c.CreatedAt, LastUsedAt: c.LastUsedAt,
	}
	if err := a.st.CreateWebAuthnCredential(ctx, &sc); err != nil {
		return fmt.Errorf("authstore: add credential: %w", err)
	}
	return nil
}

// UpdateWebAuthnCredential replaces SignCount, LastUsedAt and Data and keeps
// the stored label.
func (a *adapter) UpdateWebAuthnCredential(ctx context.Context, c auth.WebAuthnCredential) error {
	cur, err := a.st.GetWebAuthnCredential(ctx, c.ID)
	if err != nil {
		return mapErr(err)
	}
	cur.SignCount, cur.LastUsedAt, cur.Data = c.SignCount, c.LastUsedAt, c.Data
	return mapErr(a.st.UpdateWebAuthnCredential(ctx, cur))
}

func (a *adapter) DeleteWebAuthnCredential(ctx context.Context, id string) error {
	return mapErr(a.st.DeleteWebAuthnCredential(ctx, id))
}

func fromStoreCred(c store.WebAuthnCredential) auth.WebAuthnCredential {
	return auth.WebAuthnCredential{
		ID: c.ID, RPID: c.RPID, PublicKey: c.PublicKey, SignCount: c.SignCount, Label: c.Label,
		CreatedAt: c.CreatedAt, LastUsedAt: c.LastUsedAt, Data: c.Data,
	}
}

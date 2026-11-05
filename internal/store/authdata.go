package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// ---- auth events ----

const authEventCols = `id, ts, action, service, endpoint, results, outcome, reason, principal, remote_addr, host, session_id`

func scanAuthEvent(r rowScanner) (AuthEvent, error) {
	var e AuthEvent
	var at, svc, results string
	if err := r.Scan(&e.ID, &at, &e.Action, &svc, &e.Endpoint, &results, &e.Outcome, &e.Reason, &e.Principal, &e.RemoteAddr, &e.Host, &e.SessionID); err != nil {
		return AuthEvent{}, scanErr(err)
	}
	e.Service = AuthService(svc)
	e.Results = []ModuleResult{}
	if err := json.Unmarshal([]byte(results), &e.Results); err != nil {
		return AuthEvent{}, fmt.Errorf("store: decode auth results: %w", err)
	}
	if e.Results == nil {
		e.Results = []ModuleResult{}
	}
	var err error
	e.TS, err = parseTS(at)
	return e, err
}

// CreateAuthEvent appends an auth decision, assigning ID and TS when unset.
func (s *Store) CreateAuthEvent(ctx context.Context, e *AuthEvent) error {
	if e.Outcome == "" {
		return fmt.Errorf("%w: auth event outcome required", ErrInvalid)
	}
	if e.ID == "" {
		e.ID = s.NewID()
	}
	if e.TS.IsZero() {
		e.TS = s.Now()
	}
	results := e.Results
	if results == nil {
		results = []ModuleResult{}
	}
	enc, err := jsonText(results)
	if err != nil {
		return err
	}
	return s.write(ctx, func(t *txn) error {
		_, err := t.ExecContext(ctx, `INSERT INTO auth_events (`+authEventCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			e.ID, ts(e.TS), e.Action, e.Service, e.Endpoint, enc, e.Outcome, e.Reason, e.Principal, e.RemoteAddr, e.Host, e.SessionID)
		if err != nil {
			return fmt.Errorf("store: insert auth_event: %w", err)
		}
		t.note(ResAuthEvent, e.ID, OpCreate)
		return nil
	})
}

// GetAuthEvent returns one auth event.
func (s *Store) GetAuthEvent(ctx context.Context, id string) (*AuthEvent, error) {
	e, err := scanAuthEvent(s.rdb.QueryRowContext(ctx, "SELECT "+authEventCols+" FROM auth_events WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// AuthEventFilter selects auth events for ListAuthEvents.
type AuthEventFilter struct {
	Service   AuthService
	Action    string
	Outcome   string
	Principal string
	SessionID string
	Time      TimeRange
	ListOpts
}

// ListAuthEvents lists auth events, newest first.
func (s *Store) ListAuthEvents(ctx context.Context, f AuthEventFilter) (Page[AuthEvent], error) {
	var w where
	w.eq("service", string(f.Service))
	w.eq("action", f.Action)
	w.eq("outcome", f.Outcome)
	w.eq("principal", f.Principal)
	w.eq("session_id", f.SessionID)
	w.timeRange("ts", f.Time)
	return listPage(ctx, s.rdb, "SELECT "+authEventCols+" FROM auth_events", &w, f.ListOpts, "id",
		scanAuthEvent, func(e AuthEvent) string { return e.ID })
}

// ---- sessions ----

const sessionCols = `id, principal, modules, services, host, remote_addr, created_at, last_seen_at, expires_at, revoked_at`

func scanSession(r rowScanner) (Session, error) {
	var ss Session
	var modules, services, created, seen, expires string
	var revoked sql.NullString
	if err := r.Scan(&ss.ID, &ss.Principal, &modules, &services, &ss.Host, &ss.RemoteAddr, &created, &seen, &expires, &revoked); err != nil {
		return Session{}, scanErr(err)
	}
	var err error
	if ss.Modules, err = parseStrings(modules); err != nil {
		return Session{}, err
	}
	svcs, err := parseStrings(services)
	if err != nil {
		return Session{}, err
	}
	ss.Services = make([]AuthService, len(svcs))
	for i, v := range svcs {
		ss.Services[i] = AuthService(v)
	}
	if ss.CreatedAt, err = parseTS(created); err != nil {
		return Session{}, err
	}
	if ss.LastSeenAt, err = parseTS(seen); err != nil {
		return Session{}, err
	}
	if ss.ExpiresAt, err = parseTS(expires); err != nil {
		return Session{}, err
	}
	ss.RevokedAt, err = parseTSPtr(revoked)
	return ss, err
}

// CreateSession inserts ss. ss.ID must already be the hash of the session
// secret; the store never sees the raw secret. CreatedAt/LastSeenAt default to now.
func (s *Store) CreateSession(ctx context.Context, ss *Session) error {
	if ss.ID == "" || ss.Principal == "" || ss.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: session id, principal and expires_at required", ErrInvalid)
	}
	if ss.CreatedAt.IsZero() {
		ss.CreatedAt = s.Now()
	}
	if ss.LastSeenAt.IsZero() {
		ss.LastSeenAt = ss.CreatedAt
	}
	svcs := strs(ss.Services)
	return s.write(ctx, func(t *txn) error {
		_, err := t.ExecContext(ctx, `INSERT INTO sessions (`+sessionCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			ss.ID, ss.Principal, stringsText(ss.Modules), stringsText(svcs), ss.Host, ss.RemoteAddr,
			ts(ss.CreatedAt), ts(ss.LastSeenAt), ts(ss.ExpiresAt), tsOrNil(ss.RevokedAt))
		if err != nil {
			return fmt.Errorf("store: insert session: %w", err)
		}
		t.note(ResSession, ss.ID, OpCreate)
		return nil
	})
}

// GetSession returns the session by hashed id, including revoked or expired
// ones; callers decide validity from RevokedAt and ExpiresAt.
func (s *Store) GetSession(ctx context.Context, id string) (*Session, error) {
	ss, err := scanSession(s.rdb.QueryRowContext(ctx, "SELECT "+sessionCols+" FROM sessions WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	return &ss, nil
}

// UpdateSession writes principal-independent mutable fields: modules, services,
// last_seen_at and expires_at (sliding expiry).
func (s *Store) UpdateSession(ctx context.Context, ss *Session) error {
	svcs := strs(ss.Services)
	return s.write(ctx, func(t *txn) error {
		res, err := t.ExecContext(ctx, `UPDATE sessions SET modules=?, services=?, last_seen_at=?, expires_at=? WHERE id=?`,
			stringsText(ss.Modules), stringsText(svcs), ts(ss.LastSeenAt), ts(ss.ExpiresAt), ss.ID)
		if err != nil {
			return fmt.Errorf("store: update session: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		t.note(ResSession, ss.ID, OpUpdate)
		return nil
	})
}

// RevokeSession marks one session revoked at the current time. Revoking an
// already revoked session keeps its original revocation time.
func (s *Store) RevokeSession(ctx context.Context, id string) error {
	return s.write(ctx, func(t *txn) error {
		res, err := t.ExecContext(ctx, `UPDATE sessions SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?`, ts(s.now()), id)
		if err != nil {
			return fmt.Errorf("store: revoke session: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		t.note(ResSession, id, OpUpdate)
		return nil
	})
}

// RevokeAllSessions revokes every not-yet-revoked session and returns how many.
func (s *Store) RevokeAllSessions(ctx context.Context) (int, error) {
	var n int
	err := s.write(ctx, func(t *txn) error {
		ids, err := t.updateReturning(ctx, `UPDATE sessions SET revoked_at = ? WHERE revoked_at IS NULL RETURNING id`, ts(s.now()))
		if err != nil {
			return fmt.Errorf("store: revoke all sessions: %w", err)
		}
		for _, id := range ids {
			t.note(ResSession, id, OpUpdate)
		}
		n = len(ids)
		return nil
	})
	return n, err
}

// ListSessions returns sessions newest first. With activeAt non-zero, only
// sessions that are unrevoked and unexpired at that instant are returned.
func (s *Store) ListSessions(ctx context.Context, activeAt time.Time) ([]Session, error) {
	var w where
	if !activeAt.IsZero() {
		w.add("revoked_at IS NULL")
		w.add("expires_at > ?", ts(activeAt))
	}
	rows, err := s.rdb.QueryContext(ctx, sqlJoin("SELECT ", sessionCols, " FROM sessions", w.clause(), " ORDER BY created_at DESC, id"), w.args...)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Session{}
	for rows.Next() {
		ss, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}

// ---- webauthn credentials ----

const webauthnCols = `id, rp_id, principal, public_key, sign_count, label, data, created_at, last_used_at`

func scanCred(r rowScanner) (WebAuthnCredential, error) {
	var c WebAuthnCredential
	var data, created string
	var used sql.NullString
	var signCount int64
	if err := r.Scan(&c.ID, &c.RPID, &c.Principal, &c.PublicKey, &signCount, &c.Label, &data, &created, &used); err != nil {
		return WebAuthnCredential{}, scanErr(err)
	}
	c.SignCount = uint32(signCount) //nolint:gosec // stored from a uint32
	c.Data = parseRaw(data)
	var err error
	if c.CreatedAt, err = parseTS(created); err != nil {
		return WebAuthnCredential{}, err
	}
	c.LastUsedAt, err = parseTSPtr(used)
	return c, err
}

// CreateWebAuthnCredential inserts c (ID is the base64url credential id).
func (s *Store) CreateWebAuthnCredential(ctx context.Context, c *WebAuthnCredential) error {
	if c.ID == "" || c.RPID == "" || len(c.PublicKey) == 0 {
		return fmt.Errorf("%w: credential id, rp_id and public_key required", ErrInvalid)
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = s.Now()
	}
	data, err := rawText(c.Data)
	if err != nil {
		return err
	}
	return s.write(ctx, func(t *txn) error {
		_, err := t.ExecContext(ctx, `INSERT INTO webauthn_credentials (`+webauthnCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
			c.ID, c.RPID, c.Principal, c.PublicKey, int64(c.SignCount), c.Label, data, ts(c.CreatedAt), tsOrNil(c.LastUsedAt))
		if err != nil {
			return fmt.Errorf("store: insert webauthn credential: %w", err)
		}
		t.note(ResWebAuthn, c.ID, OpCreate)
		return nil
	})
}

// GetWebAuthnCredential returns one credential by id.
func (s *Store) GetWebAuthnCredential(ctx context.Context, id string) (*WebAuthnCredential, error) {
	c, err := scanCred(s.rdb.QueryRowContext(ctx, "SELECT "+webauthnCols+" FROM webauthn_credentials WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ListWebAuthnCredentials returns credentials oldest first; rpID "" lists every relying party.
func (s *Store) ListWebAuthnCredentials(ctx context.Context, rpID string) ([]WebAuthnCredential, error) {
	var w where
	w.eq("rp_id", rpID)
	rows, err := s.rdb.QueryContext(ctx, sqlJoin("SELECT ", webauthnCols, " FROM webauthn_credentials", w.clause(), " ORDER BY created_at, id"), w.args...)
	if err != nil {
		return nil, fmt.Errorf("store: list webauthn credentials: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []WebAuthnCredential{}
	for rows.Next() {
		c, err := scanCred(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateWebAuthnCredential writes sign_count, label, data and last_used_at.
func (s *Store) UpdateWebAuthnCredential(ctx context.Context, c *WebAuthnCredential) error {
	data, err := rawText(c.Data)
	if err != nil {
		return err
	}
	return s.write(ctx, func(t *txn) error {
		res, err := t.ExecContext(ctx, `UPDATE webauthn_credentials SET sign_count=?, label=?, data=?, last_used_at=? WHERE id=?`,
			int64(c.SignCount), c.Label, data, tsOrNil(c.LastUsedAt), c.ID)
		if err != nil {
			return fmt.Errorf("store: update webauthn credential: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		t.note(ResWebAuthn, c.ID, OpUpdate)
		return nil
	})
}

// DeleteWebAuthnCredential removes a credential.
func (s *Store) DeleteWebAuthnCredential(ctx context.Context, id string) error {
	return s.write(ctx, func(t *txn) error {
		res, err := t.ExecContext(ctx, `DELETE FROM webauthn_credentials WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("store: delete webauthn credential: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		t.note(ResWebAuthn, id, OpDelete)
		return nil
	})
}

// ---- settings ----

// GetSetting returns the JSON value stored under key, or ErrNotFound.
func (s *Store) GetSetting(ctx context.Context, key string) (json.RawMessage, error) {
	var v string
	if err := s.rdb.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		return nil, scanErr(err)
	}
	return json.RawMessage(v), nil
}

// SetSetting stores a JSON value under key, replacing any previous value.
func (s *Store) SetSetting(ctx context.Context, key string, value json.RawMessage) error {
	if key == "" || !json.Valid(value) {
		return fmt.Errorf("%w: setting key required and value must be valid JSON", ErrInvalid)
	}
	return s.write(ctx, func(t *txn) error {
		_, err := t.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?,?,?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, string(value), ts(s.now()))
		if err != nil {
			return fmt.Errorf("store: set setting: %w", err)
		}
		t.note(ResSetting, key, OpUpdate)
		return nil
	})
}

// DeleteSetting removes key; deleting a missing key is not an error.
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	return s.write(ctx, func(t *txn) error {
		res, err := t.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
		if err != nil {
			return fmt.Errorf("store: delete setting: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			t.note(ResSetting, key, OpDelete)
		}
		return nil
	})
}

// ListSettings returns every setting as key -> JSON value.
func (s *Store) ListSettings(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("store: list settings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = json.RawMessage(v)
	}
	return out, rows.Err()
}

// KillSwitchKey is the settings key holding the persisted KillSwitch.
const KillSwitchKey = "kill_switch"

// GetKillSwitch returns the persisted kill-switch state; before it was ever
// set, the zero value (not paused) is returned.
func (s *Store) GetKillSwitch(ctx context.Context) (KillSwitch, error) {
	raw, err := s.GetSetting(ctx, KillSwitchKey)
	if err != nil {
		if isNotFound(err) {
			return KillSwitch{}, nil
		}
		return KillSwitch{}, err
	}
	var k KillSwitch
	if err := json.Unmarshal(raw, &k); err != nil {
		return KillSwitch{}, fmt.Errorf("store: decode kill switch: %w", err)
	}
	return k, nil
}

// SetKillSwitch persists the kill-switch state; Since defaults to now.
func (s *Store) SetKillSwitch(ctx context.Context, k KillSwitch) error {
	if k.Since.IsZero() {
		k.Since = s.Now()
	}
	raw, err := json.Marshal(k)
	if err != nil {
		return fmt.Errorf("store: encode kill switch: %w", err)
	}
	return s.SetSetting(ctx, KillSwitchKey, raw)
}

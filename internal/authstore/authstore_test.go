package authstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"hal9001/internal/auth"
	"hal9001/internal/authstore"
	"hal9001/internal/store"
)

var now = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func newStore(t *testing.T) auth.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "data", "hal.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return authstore.New(st)
}

func session(id string, created time.Time, ttl time.Duration) auth.Session {
	return auth.Session{
		ID: id, Principal: "owner", Modules: []string{"password"},
		Services:  []auth.Service{auth.ServiceUI, auth.ServiceAPI},
		CreatedAt: created, LastSeenAt: created, ExpiresAt: created.Add(ttl),
		Host: "localhost:9001", RemoteAddr: "127.0.0.1:50000",
	}
}

func TestSessionRoundTripAndNotFound(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	if _, err := s.GetSession(ctx, "nope"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("GetSession(unknown) = %v, want auth.ErrNotFound", err)
	}
	want := session("abc", now, time.Hour)
	if err := s.CreateSession(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ctx, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Principal != "owner" || got.Host != want.Host || got.RemoteAddr != want.RemoteAddr ||
		len(got.Modules) != 1 || got.Modules[0] != "password" ||
		!got.Covers(auth.ServiceUI) || !got.Covers(auth.ServiceAPI) || got.Covers(auth.ServiceAdmin) ||
		!got.ExpiresAt.Equal(want.ExpiresAt) || got.RevokedAt != nil {
		t.Errorf("round trip = %+v", got)
	}

	// UpdateSession replaces only the sliding fields, modules and services.
	upd := got
	upd.Principal = "attacker"
	upd.LastSeenAt = now.Add(10 * time.Minute)
	upd.ExpiresAt = now.Add(2 * time.Hour)
	upd.Services = []auth.Service{auth.ServiceAdmin}
	upd.Modules = []string{"password", "webauthn"}
	if err := s.UpdateSession(ctx, upd); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSession(ctx, "abc")
	if got.Principal != "owner" || !got.LastSeenAt.Equal(upd.LastSeenAt) || !got.ExpiresAt.Equal(upd.ExpiresAt) ||
		!got.Covers(auth.ServiceAdmin) || got.Covers(auth.ServiceUI) || len(got.Modules) != 2 {
		t.Errorf("after update = %+v", got)
	}
	if err := s.UpdateSession(ctx, session("ghost", now, time.Hour)); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("UpdateSession(unknown) = %v, want auth.ErrNotFound", err)
	}
}

func TestRevokeAndListSessionsOnlyCountActive(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for i, id := range []string{"old", "mid", "new", "expired"} {
		sess := session(id, now.Add(time.Duration(i)*time.Minute), time.Hour)
		if id == "expired" {
			sess.ExpiresAt = now // already expired at `now`
		}
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}

	list, err := s.ListSessions(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if ids := sessionIDs(list); len(ids) != 3 || ids[0] != "new" || ids[1] != "mid" || ids[2] != "old" {
		t.Errorf("ListSessions = %v, want [new mid old] (active only, newest first)", ids)
	}

	if ok, err := s.RevokeSession(ctx, "mid", now); err != nil || !ok {
		t.Fatalf("RevokeSession(active) = %v, %v; want true", ok, err)
	}
	if ok, err := s.RevokeSession(ctx, "mid", now); err != nil || ok {
		t.Errorf("RevokeSession(already revoked) = %v, %v; want false", ok, err)
	}
	if ok, err := s.RevokeSession(ctx, "expired", now); err != nil || ok {
		t.Errorf("RevokeSession(expired) = %v, %v; want false", ok, err)
	}
	if ok, err := s.RevokeSession(ctx, "unknown", now); err != nil || ok {
		t.Errorf("RevokeSession(unknown) = %v, %v; want false, nil", ok, err)
	}
	got, _ := s.GetSession(ctx, "mid")
	if got.RevokedAt == nil || got.Active(now) {
		t.Errorf("revoked session still active: %+v", got)
	}

	n, err := s.RevokeAllSessions(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("RevokeAllSessions = %d, want 2 (old and new; mid was revoked and expired is not active)", n)
	}
	if list, _ := s.ListSessions(ctx, now); len(list) != 0 {
		t.Errorf("sessions left after revoke all: %v", sessionIDs(list))
	}
	if exp, _ := s.GetSession(ctx, "expired"); exp.RevokedAt != nil {
		t.Error("RevokeAllSessions touched an expired session")
	}
}

func sessionIDs(ss []auth.Session) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.ID
	}
	return out
}

func TestAuthEventsListNewestFirstWithModuleTrace(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for i, outcome := range []auth.Outcome{auth.OutcomeFail, auth.OutcomeSuccess} {
		err := s.AppendAuthEvent(ctx, auth.AuthEvent{
			ID: string(rune('a' + i)), Time: now.Add(time.Duration(i) * time.Second), Action: "request",
			Service: auth.ServiceAdmin, Endpoint: "POST /api/env/X",
			Modules: []auth.ModuleTrace{
				{Module: "localhost-trust", Control: auth.Sufficient, Result: auth.KindIgnore, Reason: "proxied"},
				{Module: "password", Control: auth.Required, Result: auth.KindFail, Reason: "wrong password"},
			},
			Outcome: outcome, Principal: "owner", RemoteAddr: "127.0.0.1:1", Host: "localhost:9001", SessionID: "sid",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	evs, err := s.ListAuthEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Outcome != auth.OutcomeSuccess || evs[1].Outcome != auth.OutcomeFail {
		t.Fatalf("events = %+v, want newest (success) first", evs)
	}
	m := evs[0].Modules
	if len(m) != 2 || m[0].Module != "localhost-trust" || m[0].Result != auth.KindIgnore || m[0].Reason != "proxied" ||
		m[1].Control != auth.Required || m[1].Result != auth.KindFail {
		t.Errorf("module trace = %+v", m)
	}
	if evs[0].Service != auth.ServiceAdmin || evs[0].Endpoint != "POST /api/env/X" || evs[0].SessionID != "sid" || evs[0].Host != "localhost:9001" {
		t.Errorf("event fields = %+v", evs[0])
	}
	if one, _ := s.ListAuthEvents(ctx, 1); len(one) != 1 || one[0].Outcome != auth.OutcomeSuccess {
		t.Errorf("limit 1 = %+v", one)
	}
}

func TestWebAuthnCredentialsPerRPAndUpdateKeepsLabel(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	cred := func(id, rp string) auth.WebAuthnCredential {
		return auth.WebAuthnCredential{
			ID: id, RPID: rp, PublicKey: []byte("pk-" + id), SignCount: 1, Label: "MacBook " + id,
			CreatedAt: now, Data: json.RawMessage(`{"flags":1}`),
		}
	}
	for _, c := range []auth.WebAuthnCredential{cred("c1", "localhost"), cred("c2", "mac.tail1234.ts.net"), cred("c3", "localhost")} {
		if err := s.AddWebAuthnCredential(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	local, err := s.ListWebAuthnCredentials(ctx, "localhost")
	if err != nil || len(local) != 2 {
		t.Fatalf("localhost creds = %v, %v; want 2", local, err)
	}
	if none, _ := s.ListWebAuthnCredentials(ctx, "other.example"); len(none) != 0 {
		t.Errorf("creds for unknown RP = %v", none)
	}
	all, _ := s.ListAllWebAuthnCredentials(ctx)
	if len(all) != 3 {
		t.Errorf("all creds = %d, want 3", len(all))
	}

	used := now.Add(time.Minute)
	upd := cred("c1", "localhost")
	upd.SignCount, upd.LastUsedAt, upd.Label, upd.Data = 7, &used, "renamed", json.RawMessage(`{"flags":5}`)
	if err := s.UpdateWebAuthnCredential(ctx, upd); err != nil {
		t.Fatal(err)
	}
	local, _ = s.ListWebAuthnCredentials(ctx, "localhost")
	var c1 auth.WebAuthnCredential
	for _, c := range local {
		if c.ID == "c1" {
			c1 = c
		}
	}
	if c1.SignCount != 7 || c1.LastUsedAt == nil || !c1.LastUsedAt.Equal(used) || string(c1.Data) != `{"flags":5}` {
		t.Errorf("after update = %+v", c1)
	}
	if c1.Label != "MacBook c1" || string(c1.PublicKey) != "pk-c1" {
		t.Errorf("update changed label or key: %+v", c1)
	}

	if err := s.UpdateWebAuthnCredential(ctx, cred("zzz", "localhost")); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("update unknown = %v, want auth.ErrNotFound", err)
	}
	if err := s.DeleteWebAuthnCredential(ctx, "c2"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteWebAuthnCredential(ctx, "c2"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("second delete = %v, want auth.ErrNotFound", err)
	}
}

// The manager runs a full password login, session use, revocation and
// logout on the SQLite-backed adapter.
func TestManagerPasswordLoginSessionAndRevocationOnSQLite(t *testing.T) {
	hash, err := auth.HashPassword("hunter2 hunter2")
	if err != nil {
		t.Fatal(err)
	}
	cfg := auth.DefaultConfig()
	cfg.Stacks = map[string][]auth.StackEntry{"default": {{Module: "password", Control: auth.Sufficient}}}
	cfg.PasswordHash = hash
	m, err := auth.New(auth.Options{Config: cfg, Store: newStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	m.Routes(mux, 9001)
	mux.Handle("GET /api/ping", m.Require(auth.ServiceAPI)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))

	do := func(req *http.Request) *httptest.ResponseRecorder {
		req.Host, req.RemoteAddr = "localhost:9001", "127.0.0.1:50000"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	login := func(pw string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"password": pw})
		req := httptest.NewRequest(http.MethodPost, "/api/auth/api/begin", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		return do(req)
	}
	ping := func(c *http.Cookie) int {
		req := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
		if c != nil {
			req.AddCookie(c)
		}
		return do(req).Code
	}

	if code := login("wrong").Code; code != http.StatusUnauthorized {
		t.Errorf("wrong password = %d, want 401", code)
	}
	rec := login("hunter2 hunter2")
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", rec.Code, rec.Body)
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}
	if ping(nil) != http.StatusUnauthorized {
		t.Error("anonymous ping should be 401")
	}
	if ping(cookie) != http.StatusNoContent {
		t.Error("session ping should be 204")
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/auth/sessions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	if rec := do(req); rec.Code != http.StatusOK {
		t.Fatalf("revoke all = %d: %s", rec.Code, rec.Body)
	}
	if ping(cookie) != http.StatusUnauthorized {
		t.Error("revoked session still accepted")
	}
}

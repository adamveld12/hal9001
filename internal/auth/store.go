package auth

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by Store lookups that match nothing.
var ErrNotFound = errors.New("auth: not found")

// Session is a server-side login session. ID is the SHA-256 hex of the secret
// cookie value, so a database leak does not yield usable cookies.
type Session struct {
	ID         string     `json:"id"`
	Principal  string     `json:"principal"`
	Modules    []string   `json:"modules"`
	Services   []Service  `json:"services"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt time.Time  `json:"last_seen_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	Host       string     `json:"host,omitempty"`
	RemoteAddr string     `json:"remote_addr,omitempty"`
}

// Covers reports whether the session grants svc.
func (s Session) Covers(svc Service) bool {
	for _, v := range s.Services {
		if v == svc {
			return true
		}
	}
	return false
}

// Active reports whether the session is unrevoked and unexpired at now.
func (s Session) Active(now time.Time) bool {
	return s.RevokedAt == nil && now.Before(s.ExpiresAt)
}

// AuthEvent records one authentication decision (REQUIREMENTS §10.4).
type AuthEvent struct {
	ID   string    `json:"id"`
	Time time.Time `json:"ts"`
	// Action is what was attempted: request, login_begin, login_continue,
	// logout, session_revoke, webauthn_register.
	Action     string        `json:"action"`
	Service    Service       `json:"service,omitempty"`
	Endpoint   string        `json:"endpoint"`
	Modules    []ModuleTrace `json:"modules,omitempty"`
	Outcome    Outcome       `json:"outcome"`
	Reason     string        `json:"reason,omitempty"`
	Principal  string        `json:"principal,omitempty"`
	RemoteAddr string        `json:"remote_addr"`
	Host       string        `json:"host"`
	SessionID  string        `json:"session_id,omitempty"`
}

// WebAuthnCredential is a stored passkey, scoped to one relying-party ID.
type WebAuthnCredential struct {
	// ID is the base64url credential ID.
	ID         string     `json:"id"`
	RPID       string     `json:"rp_id"`
	PublicKey  []byte     `json:"-"`
	SignCount  uint32     `json:"sign_count"`
	Label      string     `json:"label"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	// Data is the JSON-encoded webauthn.Credential record, needed to verify
	// assertions. PublicKey and SignCount mirror fields inside it.
	Data []byte `json:"-"`
}

// Store is the persistence the auth package needs. The SQLite store satisfies
// it in wave 2; MemoryStore is the in-memory implementation used by tests.
type Store interface {
	CreateSession(ctx context.Context, s Session) error
	// GetSession returns ErrNotFound for unknown ids. Revoked and expired
	// sessions are still returned; callers check Active.
	GetSession(ctx context.Context, id string) (Session, error)
	// UpdateSession replaces LastSeenAt, ExpiresAt, Modules and Services.
	UpdateSession(ctx context.Context, s Session) error
	// RevokeSession marks the session revoked; it reports whether an active
	// session was revoked.
	RevokeSession(ctx context.Context, id string, at time.Time) (bool, error)
	// RevokeAllSessions revokes every active session and returns how many.
	RevokeAllSessions(ctx context.Context, at time.Time) (int, error)
	// ListSessions returns active (unrevoked, unexpired at now) sessions,
	// newest first.
	ListSessions(ctx context.Context, now time.Time) ([]Session, error)

	AppendAuthEvent(ctx context.Context, e AuthEvent) error
	// ListAuthEvents returns up to limit events, newest first.
	ListAuthEvents(ctx context.Context, limit int) ([]AuthEvent, error)

	// ListWebAuthnCredentials returns the credentials registered for rpID.
	ListWebAuthnCredentials(ctx context.Context, rpID string) ([]WebAuthnCredential, error)
	// ListAllWebAuthnCredentials returns credentials for every RP ID.
	ListAllWebAuthnCredentials(ctx context.Context) ([]WebAuthnCredential, error)
	AddWebAuthnCredential(ctx context.Context, c WebAuthnCredential) error
	// UpdateWebAuthnCredential replaces SignCount, LastUsedAt and Data.
	UpdateWebAuthnCredential(ctx context.Context, c WebAuthnCredential) error
	// DeleteWebAuthnCredential returns ErrNotFound for unknown ids.
	DeleteWebAuthnCredential(ctx context.Context, id string) error
}

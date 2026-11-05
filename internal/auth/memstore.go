package auth

import (
	"context"
	"slices"
	"sync"
	"time"
)

// MemoryStore is an in-memory Store for tests and ephemeral use.
type MemoryStore struct {
	mu       sync.Mutex
	sessions map[string]Session
	order    []string
	events   []AuthEvent
	creds    []WebAuthnCredential
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: map[string]Session{}}
}

var _ Store = (*MemoryStore)(nil)

func (m *MemoryStore) CreateSession(_ context.Context, s Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.ID] = cloneSession(s)
	m.order = append(m.order, s.ID)
	return nil
}

func (m *MemoryStore) GetSession(_ context.Context, id string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return Session{}, ErrNotFound
	}
	return cloneSession(s), nil
}

func (m *MemoryStore) UpdateSession(_ context.Context, s Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.sessions[s.ID]
	if !ok {
		return ErrNotFound
	}
	cur.LastSeenAt, cur.ExpiresAt = s.LastSeenAt, s.ExpiresAt
	cur.Modules, cur.Services = slices.Clone(s.Modules), slices.Clone(s.Services)
	m.sessions[s.ID] = cur
	return nil
}

func (m *MemoryStore) RevokeSession(_ context.Context, id string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || s.RevokedAt != nil || !at.Before(s.ExpiresAt) {
		return false, nil
	}
	s.RevokedAt = &at
	m.sessions[id] = s
	return true, nil
}

func (m *MemoryStore) RevokeAllSessions(_ context.Context, at time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, s := range m.sessions {
		if s.RevokedAt == nil && at.Before(s.ExpiresAt) {
			s.RevokedAt = &at
			m.sessions[id] = s
			n++
		}
	}
	return n, nil
}

func (m *MemoryStore) ListSessions(_ context.Context, now time.Time) ([]Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Session
	for i := len(m.order) - 1; i >= 0; i-- {
		if s := m.sessions[m.order[i]]; s.Active(now) {
			out = append(out, cloneSession(s))
		}
	}
	return out, nil
}

func (m *MemoryStore) AppendAuthEvent(_ context.Context, e AuthEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e.Modules = slices.Clone(e.Modules)
	m.events = append(m.events, e)
	return nil
}

func (m *MemoryStore) ListAuthEvents(_ context.Context, limit int) ([]AuthEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []AuthEvent
	for i := len(m.events) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
		e := m.events[i]
		e.Modules = slices.Clone(e.Modules)
		out = append(out, e)
	}
	return out, nil
}

func (m *MemoryStore) ListWebAuthnCredentials(_ context.Context, rpID string) ([]WebAuthnCredential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []WebAuthnCredential
	for _, c := range m.creds {
		if c.RPID == rpID {
			out = append(out, cloneCred(c))
		}
	}
	return out, nil
}

func (m *MemoryStore) ListAllWebAuthnCredentials(_ context.Context) ([]WebAuthnCredential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]WebAuthnCredential, 0, len(m.creds))
	for _, c := range m.creds {
		out = append(out, cloneCred(c))
	}
	return out, nil
}

func (m *MemoryStore) AddWebAuthnCredential(_ context.Context, c WebAuthnCredential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.creds = append(m.creds, cloneCred(c))
	return nil
}

func (m *MemoryStore) UpdateWebAuthnCredential(_ context.Context, c WebAuthnCredential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.creds {
		if m.creds[i].ID == c.ID {
			m.creds[i].SignCount, m.creds[i].LastUsedAt, m.creds[i].Data = c.SignCount, c.LastUsedAt, slices.Clone(c.Data)
			return nil
		}
	}
	return ErrNotFound
}

func (m *MemoryStore) DeleteWebAuthnCredential(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.creds {
		if m.creds[i].ID == id {
			m.creds = slices.Delete(m.creds, i, i+1)
			return nil
		}
	}
	return ErrNotFound
}

func cloneSession(s Session) Session {
	s.Modules, s.Services = slices.Clone(s.Modules), slices.Clone(s.Services)
	return s
}

func cloneCred(c WebAuthnCredential) WebAuthnCredential {
	c.PublicKey, c.Data = slices.Clone(c.PublicKey), slices.Clone(c.Data)
	return c
}

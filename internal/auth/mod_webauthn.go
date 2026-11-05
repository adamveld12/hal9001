package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const registrationTTL = 5 * time.Minute

// ownerHandle is the fixed WebAuthn user handle of the single owner.
var ownerHandle = func() []byte { h := sha256.Sum256([]byte("hal9001-owner")); return h[:] }()

// owner is the one WebAuthn user; its credentials are those registered for
// one relying-party ID.
type owner struct{ creds []webauthn.Credential }

func (owner) WebAuthnID() []byte                           { return ownerHandle }
func (owner) WebAuthnName() string                         { return "owner" }
func (owner) WebAuthnDisplayName() string                  { return "hal9001 owner" }
func (o owner) WebAuthnCredentials() []webauthn.Credential { return o.creds }

// WebAuthn authenticates with passkeys. The relying-party ID is the request
// hostname, so localhost and each ts.net hostname hold separate credentials.
// Login runs as a challenge exchange; registration is a separate API that
// needs an authenticated session.
type WebAuthn struct {
	store Store
	now   func() time.Time

	mu   sync.Mutex
	regs map[string]*registration
}

type registration struct {
	binding string // "session:<id>" or "stack:<principal>"
	host    string
	label   string
	data    webauthn.SessionData
	expires time.Time
}

// NewWebAuthn builds the module over store. now may be nil.
func NewWebAuthn(store Store, now func() time.Time) *WebAuthn {
	if now == nil {
		now = time.Now
	}
	return &WebAuthn{store: store, now: now, regs: map[string]*registration{}}
}

func (*WebAuthn) Name() string { return "webauthn" }
func (*WebAuthn) Interactive() {}

// relyingParty returns the library instance for a request's host.
func relyingParty(host string, secure bool) (wa *webauthn.WebAuthn, rpID string, err error) {
	rpID = hostName(host)
	if _, perr := netip.ParseAddr(rpID); perr == nil {
		return nil, "", errors.New("WebAuthn needs a domain name, not an IP address")
	}
	scheme := "http"
	if secure {
		scheme = "https"
	}
	wa, err = webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: "hal9001",
		RPOrigins:     []string{scheme + "://" + host},
	})
	return wa, rpID, err
}

func (m *WebAuthn) loadOwner(ctx context.Context, rpID string) (owner, error) {
	stored, err := m.store.ListWebAuthnCredentials(ctx, rpID)
	if err != nil {
		return owner{}, err
	}
	o := owner{}
	for _, s := range stored {
		var c webauthn.Credential
		if err := json.Unmarshal(s.Data, &c); err != nil {
			return owner{}, fmt.Errorf("stored passkey %s is unreadable: %w", s.ID, err)
		}
		o.creds = append(o.creds, c)
	}
	return o, nil
}

func (m *WebAuthn) Authenticate(ctx context.Context, req *AuthRequest) Result {
	if !req.Interactive {
		return Ignore("passkey login is interactive only")
	}
	if req.Method != "" && req.Method != "webauthn" {
		return Ignore("another login method was selected")
	}
	wa, rpID, err := relyingParty(req.Host, req.Secure)
	if err != nil {
		return Ignore(err.Error())
	}
	user, err := m.loadOwner(ctx, rpID)
	if err != nil {
		return Fail(err.Error())
	}
	if req.Step == nil {
		return m.beginLogin(wa, rpID, user)
	}
	return m.finishLogin(ctx, wa, rpID, user, req.Step)
}

func (m *WebAuthn) beginLogin(wa *webauthn.WebAuthn, rpID string, user owner) Result {
	if len(user.creds) == 0 {
		return Ignore("no passkey registered for " + rpID)
	}
	assertion, session, err := wa.BeginLogin(user, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return Fail("cannot start passkey login: " + err.Error())
	}
	data, err := json.Marshal(assertion)
	if err != nil {
		return Fail("cannot encode passkey challenge: " + err.Error())
	}
	state, err := json.Marshal(session)
	if err != nil {
		return Fail("cannot keep passkey challenge state: " + err.Error())
	}
	return Challenge(Step{Kind: "webauthn.assertion", Data: data, State: state})
}

func (m *WebAuthn) finishLogin(ctx context.Context, wa *webauthn.WebAuthn, rpID string, user owner, step *StepResponse) Result {
	var session webauthn.SessionData
	if err := json.Unmarshal(step.State, &session); err != nil {
		return Fail("passkey challenge state is unreadable")
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(step.Response)
	if err != nil {
		return Fail("malformed passkey assertion: " + err.Error())
	}
	cred, err := wa.ValidateLogin(user, session, parsed)
	if err != nil {
		return Fail("passkey assertion rejected: " + err.Error())
	}
	if cred.Authenticator.CloneWarning {
		return Fail("passkey sign count did not increase: possible cloned authenticator")
	}
	data, err := json.Marshal(cred)
	if err != nil {
		return Fail("cannot encode passkey record: " + err.Error())
	}
	used := m.now().UTC()
	err = m.store.UpdateWebAuthnCredential(ctx, WebAuthnCredential{
		ID: base64.RawURLEncoding.EncodeToString(cred.ID), RPID: rpID,
		SignCount: cred.Authenticator.SignCount, LastUsedAt: &used, Data: data,
	})
	if err != nil {
		return Fail("cannot record passkey use: " + err.Error())
	}
	return Success(Principal{Name: "owner"})
}

// BeginRegistration starts a passkey registration for host, bound to the
// caller (binding: a session or stack principal). It returns the registration
// id and the options for navigator.credentials.create().
func (m *WebAuthn) BeginRegistration(ctx context.Context, binding, host string, secure bool, label string) (string, *protocol.CredentialCreation, error) {
	wa, rpID, err := relyingParty(host, secure)
	if err != nil {
		return "", nil, err
	}
	user, err := m.loadOwner(ctx, rpID)
	if err != nil {
		return "", nil, err
	}
	exclude := make([]protocol.CredentialDescriptor, 0, len(user.creds))
	for _, c := range user.creds {
		exclude = append(exclude, c.Descriptor())
	}
	creation, data, err := wa.BeginRegistration(user,
		webauthn.WithExclusions(exclude),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
			UserVerification: protocol.VerificationRequired,
		}),
	)
	if err != nil {
		return "", nil, err
	}
	id, err := newToken()
	if err != nil {
		return "", nil, err
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, r := range m.regs {
		if !now.Before(r.expires) {
			delete(m.regs, k)
		}
	}
	if len(m.regs) >= maxPendingLogins {
		return "", nil, errors.New("too many pending registrations")
	}
	m.regs[id] = &registration{binding: binding, host: host, label: label, data: *data, expires: now.Add(registrationTTL)}
	return id, creation, nil
}

// errRegistrationUnknown means the registration id is unknown, expired,
// already used, or belongs to another caller or host.
var errRegistrationUnknown = errors.New("registration expired or unknown")

// FinishRegistration verifies the authenticator's attestation and stores the
// new passkey.
func (m *WebAuthn) FinishRegistration(ctx context.Context, binding, host string, secure bool, regID string, credential []byte) (WebAuthnCredential, error) {
	m.mu.Lock()
	reg, ok := m.regs[regID]
	delete(m.regs, regID)
	m.mu.Unlock()
	if !ok || !m.now().Before(reg.expires) || reg.binding != binding || reg.host != host {
		return WebAuthnCredential{}, errRegistrationUnknown
	}
	wa, rpID, err := relyingParty(host, secure)
	if err != nil {
		return WebAuthnCredential{}, err
	}
	user, err := m.loadOwner(ctx, rpID)
	if err != nil {
		return WebAuthnCredential{}, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(credential)
	if err != nil {
		return WebAuthnCredential{}, fmt.Errorf("malformed attestation: %w", err)
	}
	cred, err := wa.CreateCredential(user, reg.data, parsed)
	if err != nil {
		return WebAuthnCredential{}, fmt.Errorf("attestation rejected: %w", err)
	}
	data, err := json.Marshal(cred)
	if err != nil {
		return WebAuthnCredential{}, err
	}
	label := reg.label
	if label == "" {
		label = "passkey for " + rpID
	}
	stored := WebAuthnCredential{
		ID: base64.RawURLEncoding.EncodeToString(cred.ID), RPID: rpID, PublicKey: cred.PublicKey,
		SignCount: cred.Authenticator.SignCount, Label: label, CreatedAt: m.now().UTC(), Data: data,
	}
	if err := m.store.AddWebAuthnCredential(ctx, stored); err != nil {
		return WebAuthnCredential{}, err
	}
	return stored, nil
}

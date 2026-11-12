package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"time"

	"hal9001/internal/spec"
)

// ConnectResult is the link that starts an authorization.
type ConnectResult struct {
	// URL is the authorization URL to open in the browser.
	URL string `json:"authorize_url"`
	// ExpiresAt is when the link stops working: the callback of a later redirect is refused.
	ExpiresAt time.Time `json:"expires_at"`
}

// Connect starts the authorization of an OAuth server: it discovers the authorization server, registers the client when
// it has to, and returns the URL the user opens in a browser. The redirect lands on the daemon's callback (Callback).
func (m *Manager) Connect(ctx context.Context, name string) (ConnectResult, error) {
	cfg, err := m.config(name)
	if err != nil {
		return ConnectResult{}, err
	}
	if !cfg.UsesOAuth() {
		return ConnectResult{}, fmt.Errorf("%w: %q authenticates with %s", ErrNotOAuth, name, cfg.AuthMode())
	}
	redirect := m.opt.RedirectURL()
	meta, err := m.discover(ctx, cfg.URL)
	if err != nil {
		m.noteErr(name, err, false)
		return ConnectResult{}, err
	}
	client, err := m.clientFor(ctx, name, cfg, meta, redirect)
	if err != nil {
		m.noteErr(name, err, false)
		return ConnectResult{}, err
	}
	state, err := randomString()
	if err != nil {
		return ConnectResult{}, err
	}
	verifier, err := randomString()
	if err != nil {
		return ConnectResult{}, err
	}
	authURL, err := authorizeURL(meta, client, redirect, state, verifier, scopeFor(cfg, meta))
	if err != nil {
		m.noteErr(name, err, false)
		return ConnectResult{}, err
	}
	now := m.now()
	m.mu.Lock()
	m.pruneFlowsLocked()
	for st, p := range m.flows { // one authorization per server at a time
		if p.server == name {
			delete(m.flows, st)
		}
	}
	m.flows[state] = &pending{server: name, verifier: verifier, redirect: redirect, client: client, meta: meta, created: now}
	m.mu.Unlock()
	return ConnectResult{URL: authURL, ExpiresAt: now.Add(m.opt.FlowTTL)}, nil
}

// clientFor resolves the OAuth client hal9001 acts as: the pasted one, a registration made earlier for this
// authorization server and redirect, or a new dynamic registration.
func (m *Manager) clientFor(ctx context.Context, name string, cfg spec.RemoteMCP, meta *asMeta, redirect string) (clientCreds, error) {
	s := m.get(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	m.load(name, s)
	if cfg.AuthMode() == spec.MCPAuthOAuthClient {
		secret := s.sec.ClientSecret
		return clientCreds{ID: cfg.ClientID, Secret: secret, Method: tokenMethod(meta.AuthMethods, secret != "")}, nil
	}
	if r := s.sec.Registration; r != nil && r.ClientID != "" && r.RedirectURI == redirect && issuersEqual(r.Issuer, meta.Issuer) {
		return clientCreds{ID: r.ClientID, Secret: r.ClientSecret, Method: r.AuthMethod}, nil
	}
	if meta.RegURL == "" {
		return clientCreds{}, fmt.Errorf("%w (%s): use \"OAuth with client ID and secret\" and paste credentials from the provider", ErrNoRegistration, meta.Issuer)
	}
	reg, err := m.registerClient(ctx, meta, redirect)
	if err != nil {
		return clientCreds{}, err
	}
	s.sec.Registration = reg
	if err := m.saveSecrets(name, s); err != nil {
		return clientCreds{}, err
	}
	return clientCreds{ID: reg.ClientID, Secret: reg.ClientSecret, Method: reg.AuthMethod}, nil
}

// CallbackQuery is what the authorization server sent back to the callback.
type CallbackQuery struct {
	State            string
	Code             string
	Error            string
	ErrorDescription string
	// Iss is the issuer of RFC 9207, when the authorization server sent it.
	Iss string
}

// Callback completes an authorization: it validates the state (one use, ten minutes), exchanges the code and stores the
// tokens. It returns the server name once the state matched, with or without an error: the failure is recorded on the
// server so the UI shows it. With an unknown or reused state the name is empty and the error is ErrInvalidState.
func (m *Manager) Callback(ctx context.Context, q CallbackQuery) (string, error) {
	m.mu.Lock()
	m.pruneFlowsLocked()
	p, ok := m.flows[q.State]
	delete(m.flows, q.State) // single use, whatever happens next
	m.mu.Unlock()
	if !ok || q.State == "" {
		return "", ErrInvalidState
	}
	name := p.server
	fail := func(err error) (string, error) {
		m.noteErr(name, err, false)
		return name, err
	}
	if q.Error != "" {
		msg := "authorization was not granted: " + q.Error
		if q.ErrorDescription != "" {
			msg += ": " + q.ErrorDescription
		}
		return fail(errors.New(msg))
	}
	if q.Code == "" {
		return fail(errors.New("the authorization response has no code"))
	}
	switch {
	case q.Iss != "" && !issuersEqual(q.Iss, p.meta.Issuer):
		return fail(fmt.Errorf("the authorization response came from issuer %q, not %q", q.Iss, p.meta.Issuer))
	case q.Iss == "" && p.meta.ISSParamUsed:
		return fail(errors.New("the authorization server announces RFC 9207 issuer identification but sent no iss"))
	}
	tok, err := m.exchangeCode(ctx, p, q.Code)
	if err != nil {
		return fail(err)
	}
	s := m.get(name)
	s.mu.Lock()
	m.load(name, s)
	s.sec.Token = tok
	s.sec.Endpoints = &endpoints{Issuer: p.meta.Issuer, TokenURL: p.meta.TokenURL, Resource: p.meta.Resource, ClientID: p.client.ID, AuthMethod: p.client.Method}
	s.st.NeedsReconnect, s.st.LastError, s.st.LastErrorAt = false, "", nil
	err = errors.Join(m.saveSecrets(name, s), m.saveState(name, s))
	s.mu.Unlock()
	if err != nil {
		return fail(err)
	}
	m.changed(name)
	// The tool list is cached right away; the connection is good even if this first listing is not.
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if _, _, err := m.RefreshTools(lctx, name); err != nil {
		m.opt.Log.Warn("mcpclient: list tools after connect", "server", name, "err", err)
	}
	return name, nil
}

// credentials returns the header that authenticates a request to the server. For OAuth it is the access token, refreshed
// when it is about to expire or when rejected is the token the server just refused. used is the token sent.
func (m *Manager) credentials(ctx context.Context, name string, cfg spec.RemoteMCP, rejected string) (header, value, used string, err error) {
	switch cfg.AuthMode() {
	case spec.MCPAuthNone:
		return "", "", "", nil
	case spec.MCPAuthHeader:
		s := m.get(name)
		s.mu.Lock()
		m.load(name, s)
		key := s.sec.APIKey
		s.mu.Unlock()
		if key == "" {
			return "", "", "", fmt.Errorf("%w: no API key is stored for %q", ErrNotConnected, name)
		}
		return cfg.Header, key, key, nil
	}
	tok, err := m.accessToken(ctx, name, rejected)
	if err != nil {
		return "", "", "", err
	}
	return "Authorization", "Bearer " + tok, tok, nil
}

// accessToken returns a usable access token, refreshing it first when it expires within the skew or equals rejected.
// A refresh the authorization server refuses for good marks the server as needing a reconnect.
func (m *Manager) accessToken(ctx context.Context, name, rejected string) (string, error) {
	s := m.get(name)
	s.mu.Lock()
	m.load(name, s)
	var note *Note
	defer func() {
		s.mu.Unlock()
		if note != nil {
			m.postNote(*note)
			m.changed(name)
		}
	}()
	t := s.sec.Token
	if t == nil {
		return "", fmt.Errorf("%w: connect %q first", ErrNotConnected, name)
	}
	stale := !t.Expiry.IsZero() && m.now().Add(m.opt.RefreshSkew).After(t.Expiry)
	if !stale && (rejected == "" || t.AccessToken != rejected) {
		return t.AccessToken, nil
	}
	if t.RefreshToken == "" || s.sec.Endpoints == nil {
		note = m.noteErrLocked(name, s, errors.New("the access token expired and there is no refresh token"), true)
		return "", fmt.Errorf("%w: %q", ErrNeedsReconnect, name)
	}
	secret := s.sec.ClientSecret
	if r := s.sec.Registration; r != nil {
		secret = r.ClientSecret
	}
	nt, err := m.refreshToken(ctx, s.sec.Endpoints, secret, t.RefreshToken)
	if err != nil {
		var te *tokenError
		if errors.As(err, &te) && te.permanent() {
			note = m.noteErrLocked(name, s, fmt.Errorf("refreshing the access token failed: %w", err), true)
			return "", fmt.Errorf("%w: %q: %w", ErrNeedsReconnect, name, err)
		}
		return "", fmt.Errorf("refresh the access token of %q: %w", name, err)
	}
	s.sec.Token = nt
	if err := m.saveSecrets(name, s); err != nil {
		m.opt.Log.Warn("mcpclient: save refreshed token", "server", name, "err", err)
	}
	return nt.AccessToken, nil
}

package mcpclient

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"hal9001/internal/spec"
)

// The authorization flow follows the MCP authorization specification (2025-11-25): the protected resource metadata of
// RFC 9728 found through the WWW-Authenticate challenge (or the well-known locations), the authorization server
// metadata of RFC 8414 or OIDC discovery, dynamic client registration (RFC 7591) or a pasted client, the
// authorization code flow with PKCE S256, and the `resource` parameter of RFC 8707 on the authorization and token
// requests. Client ID Metadata Documents are not supported: they need an HTTPS URL the authorization server can
// fetch, and a loopback daemon has none.

// asMeta is what discovery found.
type asMeta struct {
	Resource      string
	Issuer        string
	AuthURL       string
	TokenURL      string
	RegURL        string
	AuthMethods   []string
	ISSParamUsed  bool
	ScopesFromPRM []string
	// Challenge is the scope the server's WWW-Authenticate challenge asked for.
	Challenge []string
	ASScopes  []string
}

// probe sends an unauthenticated initialize request and reads the challenge. It returns the resource_metadata URL and
// the scope of the challenge; ErrNoAuthRequired when the server did not refuse.
func (m *Manager) probe(ctx context.Context, serverURL string) (metadataURL string, scope []string, err error) {
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"hal9001","version":%q}}}`, m.opt.Version)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, strings.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := m.opt.HTTPClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("reach %s: %w", serverURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 == 2 {
		return "", nil, ErrNoAuthRequired
	}
	if resp.StatusCode != http.StatusUnauthorized {
		// Not a challenge: fall back to the well-known locations (earlier specification revisions).
		return "", nil, nil
	}
	challenges, perr := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
	if perr != nil {
		return "", nil, nil
	}
	for _, c := range challenges {
		if c.Scheme != "bearer" {
			continue
		}
		if metadataURL == "" {
			metadataURL = c.Params["resource_metadata"]
		}
		if scope == nil && c.Params["scope"] != "" {
			scope = strings.Fields(c.Params["scope"])
		}
	}
	return metadataURL, scope, nil
}

// discover finds the endpoints the authorization of serverURL uses.
func (m *Manager) discover(ctx context.Context, serverURL string) (*asMeta, error) {
	hint, scope, err := m.probe(ctx, serverURL)
	if err != nil {
		return nil, err
	}
	meta := &asMeta{Resource: serverURL, Challenge: scope}
	issuer := ""
	if prm := m.protectedResource(ctx, hint, serverURL); prm != nil {
		meta.Resource = prm.Resource
		meta.ScopesFromPRM = prm.ScopesSupported
		if len(prm.AuthorizationServers) > 0 {
			issuer = prm.AuthorizationServers[0]
		}
	}
	if issuer == "" {
		// Earlier revisions of the specification: the MCP server's origin is the authorization server.
		u, perr := url.Parse(serverURL)
		if perr != nil {
			return nil, perr
		}
		u.Path, u.RawQuery = "", ""
		issuer = u.String()
	}
	asm, err := auth.GetAuthServerMetadata(ctx, issuer, m.opt.HTTPClient)
	if err != nil {
		return nil, fmt.Errorf("authorization server metadata of %s: %w", issuer, err)
	}
	if asm == nil {
		asm = &oauthex.AuthServerMeta{
			Issuer:                issuer,
			AuthorizationEndpoint: strings.TrimSuffix(issuer, "/") + "/authorize",
			TokenEndpoint:         strings.TrimSuffix(issuer, "/") + "/token",
			RegistrationEndpoint:  strings.TrimSuffix(issuer, "/") + "/register",
		}
	} else if len(asm.CodeChallengeMethodsSupported) > 0 && !slices.Contains(asm.CodeChallengeMethodsSupported, "S256") {
		return nil, fmt.Errorf("authorization server %s does not support PKCE S256", issuer)
	}
	meta.Issuer = asm.Issuer
	meta.AuthURL, meta.TokenURL, meta.RegURL = asm.AuthorizationEndpoint, asm.TokenEndpoint, asm.RegistrationEndpoint
	meta.AuthMethods = asm.TokenEndpointAuthMethodsSupported
	meta.ISSParamUsed = asm.AuthorizationResponseIssParameterSupported
	meta.ASScopes = asm.ScopesSupported
	return meta, nil
}

// protectedResource fetches the protected resource metadata (RFC 9728) from the challenge's URL, then the well-known
// locations of the server's path and root. The first that validates wins; nil when there is none.
func (m *Manager) protectedResource(ctx context.Context, hint, serverURL string) *oauthex.ProtectedResourceMetadata {
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil
	}
	root := *u
	root.Path, root.RawQuery = "", ""
	type cand struct{ meta, resource string }
	var cands []cand
	resources := []string{serverURL, strings.TrimSuffix(serverURL, "/"), serverURL + "/", root.String(), root.String() + "/"}
	if hint != "" {
		for _, r := range resources {
			cands = append(cands, cand{hint, r})
		}
	}
	atPath := *u
	atPath.Path = "/.well-known/oauth-protected-resource/" + strings.TrimLeft(u.Path, "/")
	atPath.RawQuery = ""
	for _, r := range resources[:3] {
		cands = append(cands, cand{atPath.String(), r})
	}
	atRoot := root
	atRoot.Path = "/.well-known/oauth-protected-resource"
	for _, r := range resources[3:] {
		cands = append(cands, cand{atRoot.String(), r})
	}
	for _, c := range cands {
		prm, err := oauthex.GetProtectedResourceMetadata(ctx, c.meta, c.resource, m.opt.HTTPClient)
		if err == nil && prm != nil {
			return prm
		}
	}
	return nil
}

// clientCreds is the OAuth client hal9001 acts as at one authorization server.
type clientCreds struct {
	ID, Secret, Method string
}

// tokenMethod picks how the client authenticates at the token endpoint.
func tokenMethod(supported []string, hasSecret bool) string {
	if !hasSecret {
		return "none"
	}
	if len(supported) == 0 || slices.Contains(supported, "client_secret_post") {
		return "client_secret_post"
	}
	if slices.Contains(supported, "client_secret_basic") {
		return "client_secret_basic"
	}
	return "client_secret_post"
}

// registerClient registers hal9001 at the authorization server (RFC 7591) as a public native client.
func (m *Manager) registerClient(ctx context.Context, meta *asMeta, redirect string) (*registration, error) {
	method := "none"
	if len(meta.AuthMethods) > 0 && !slices.Contains(meta.AuthMethods, "none") {
		method = tokenMethod(meta.AuthMethods, true)
	}
	resp, err := oauthex.RegisterClient(ctx, meta.RegURL, &oauthex.ClientRegistrationMetadata{
		RedirectURIs:            []string{redirect},
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: method,
		ClientName:              "hal9001",
		ClientURI:               "https://github.com/adamveld12/hal9001",
		ApplicationType:         "native",
	}, m.opt.HTTPClient)
	if err != nil {
		return nil, fmt.Errorf("register client: %w", err)
	}
	if resp.TokenEndpointAuthMethod != "" {
		method = resp.TokenEndpointAuthMethod
	}
	return &registration{
		ClientID: resp.ClientID, ClientSecret: resp.ClientSecret, RedirectURI: redirect,
		Issuer: meta.Issuer, AuthMethod: method, At: m.now(),
	}, nil
}

// pending is an authorization in progress: the secrets of one authorize URL.
type pending struct {
	server   string
	verifier string
	redirect string
	client   clientCreds
	meta     *asMeta
	created  time.Time
}

func randomString() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func challengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// scopeFor chooses the scope to request: the configured override, else the challenge's, else the resource's
// advertised scopes; offline_access is added when the authorization server offers it so that a refresh token is issued.
func scopeFor(cfg spec.RemoteMCP, meta *asMeta) string {
	scopes := slices.Clone(cfg.Scopes)
	if len(scopes) == 0 {
		scopes = slices.Clone(meta.Challenge)
	}
	if len(scopes) == 0 {
		scopes = slices.Clone(meta.ScopesFromPRM)
	}
	if slices.Contains(meta.ASScopes, "offline_access") && !slices.Contains(scopes, "offline_access") {
		scopes = append(scopes, "offline_access")
	}
	return strings.Join(scopes, " ")
}

// authorizeURL builds the authorization request: code flow, PKCE S256, state and the resource indicator.
func authorizeURL(meta *asMeta, client clientCreds, redirect, state, verifier, scope string) (string, error) {
	u, err := url.Parse(meta.AuthURL)
	if err != nil {
		return "", fmt.Errorf("authorization endpoint %q: %w", meta.AuthURL, err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", client.ID)
	q.Set("redirect_uri", redirect)
	q.Set("state", state)
	q.Set("code_challenge", challengeS256(verifier))
	q.Set("code_challenge_method", "S256")
	if scope != "" {
		q.Set("scope", scope)
	}
	if meta.Resource != "" {
		q.Set("resource", meta.Resource)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// tokenError is an error response of the token endpoint.
type tokenError struct {
	Status int
	Code   string
	Desc   string
}

func (e *tokenError) Error() string {
	switch {
	case e.Code != "" && e.Desc != "":
		return fmt.Sprintf("token endpoint: %s: %s", e.Code, e.Desc)
	case e.Code != "":
		return "token endpoint: " + e.Code
	}
	return fmt.Sprintf("token endpoint: HTTP %d", e.Status)
}

// permanent reports whether the endpoint refused the credentials for good, as opposed to being unreachable or failing.
func (e *tokenError) permanent() bool {
	switch e.Code {
	case "invalid_grant", "invalid_client", "unauthorized_client", "invalid_token", "access_denied":
		return true
	}
	return e.Status == http.StatusBadRequest || e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// postToken sends one token request. The client authenticates the way client.Method says.
func (m *Manager) postToken(ctx context.Context, tokenURL string, form url.Values, client clientCreds) (*tokenSet, error) {
	form = cloneValues(form)
	switch client.Method {
	case "client_secret_basic":
		// RFC 6749 2.3.1: id and secret are form-urlencoded before they are joined.
	default:
		form.Set("client_id", client.ID)
		if client.Method == "client_secret_post" && client.Secret != "" {
			form.Set("client_secret", client.Secret)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if client.Method == "client_secret_basic" {
		req.SetBasicAuth(url.QueryEscape(client.ID), url.QueryEscape(client.Secret))
	}
	resp, err := m.opt.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("token endpoint: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		te := &tokenError{Status: resp.StatusCode}
		var e struct {
			Error string `json:"error"`
			Desc  string `json:"error_description"`
		}
		if json.Unmarshal(body, &e) == nil {
			te.Code, te.Desc = e.Error, e.Desc
		}
		return nil, te
	}
	var tr struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(body, &tr); err != nil || tr.AccessToken == "" {
		return nil, errors.New("token endpoint: the response has no access_token")
	}
	t := &tokenSet{AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken, TokenType: tr.TokenType, Scope: tr.Scope}
	if tr.ExpiresIn > 0 {
		t.Expiry = m.now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return t, nil
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v)+2)
	for k, vs := range v {
		out[k] = slices.Clone(vs)
	}
	return out
}

// exchangeCode redeems an authorization code.
func (m *Manager) exchangeCode(ctx context.Context, p *pending, code string) (*tokenSet, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {p.redirect},
		"code_verifier": {p.verifier},
	}
	if p.meta.Resource != "" {
		form.Set("resource", p.meta.Resource)
	}
	return m.postToken(ctx, p.meta.TokenURL, form, p.client)
}

// refreshToken redeems a refresh token. A server that does not rotate refresh tokens keeps the old one.
func (m *Manager) refreshToken(ctx context.Context, ep *endpoints, secret, refresh string) (*tokenSet, error) {
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}
	if ep.Resource != "" {
		form.Set("resource", ep.Resource)
	}
	t, err := m.postToken(ctx, ep.TokenURL, form, clientCreds{ID: ep.ClientID, Secret: secret, Method: ep.AuthMethod})
	if err != nil {
		return nil, err
	}
	if t.RefreshToken == "" {
		t.RefreshToken = refresh
	}
	return t, nil
}

// issuersEqual compares authorization server identifiers ignoring one trailing slash.
func issuersEqual(a, b string) bool {
	return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}

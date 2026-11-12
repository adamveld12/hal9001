// Package mcpclient connects hal9001 to remote MCP servers as a client (docs/mcp-servers.md): the server definitions
// of config.yaml `mcp_servers`, OAuth (RFC 9728, RFC 8414, RFC 7591, PKCE) or API-key authentication, a cached tool
// list, tool calls, and the run-scoped tokens that let a sensor script call the servers it declares.
//
// Credentials live in <home>/data/mcp/<name>.secrets.json (0600) and nowhere else: no value returned by this package
// is a secret, and a script that holds a run token never sees one.
package mcpclient

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"hal9001/internal/spec"
)

// Options configures New.
type Options struct {
	// Home is the hal9001 home directory.
	Home string
	// Servers returns the configured servers (config.yaml mcp_servers), read on every operation so a reload takes effect.
	Servers func() map[string]spec.RemoteMCP
	// RedirectURL is the OAuth redirect URI of the running daemon (http://127.0.0.1:<port>/oauth/callback).
	RedirectURL func() string
	// Notifier receives the Inbox notes: a tool list that changed, a server that needs to be reconnected. Optional.
	Notifier Notifier
	// Changed is called after a server's status, tools or credentials changed, for the UI's event stream. Optional.
	Changed func(name string)
	// Transport carries every request; default http.DefaultTransport.
	Transport http.RoundTripper
	// Version is reported to servers as the client version.
	Version string
	Log     *slog.Logger
	// Now is the clock; default time.Now.
	Now func() time.Time
	// FlowTTL is how long a Connect link stays valid (default 10 minutes).
	FlowTTL time.Duration
	// RefreshSkew is how long before its expiry an access token is refreshed (default 60 seconds).
	RefreshSkew time.Duration

	// HTTPClient is the client for discovery, registration and token requests; derived from Transport when nil.
	HTTPClient *http.Client
}

// Manager is the MCP client. It is safe for concurrent use.
type Manager struct {
	opt Options

	mu      sync.Mutex
	servers map[string]*srv
	flows   map[string]*pending
	grants  map[string]*RunGrant
}

// New returns a Manager.
func New(opt Options) *Manager {
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.Transport == nil {
		opt.Transport = http.DefaultTransport
	}
	if opt.HTTPClient == nil {
		opt.HTTPClient = &http.Client{Transport: opt.Transport, Timeout: 30 * time.Second}
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.FlowTTL <= 0 {
		opt.FlowTTL = 10 * time.Minute
	}
	if opt.RefreshSkew <= 0 {
		opt.RefreshSkew = time.Minute
	}
	if opt.Servers == nil {
		opt.Servers = func() map[string]spec.RemoteMCP { return nil }
	}
	if opt.RedirectURL == nil {
		opt.RedirectURL = func() string { return "http://127.0.0.1:9001/oauth/callback" }
	}
	return &Manager{opt: opt, servers: map[string]*srv{}, flows: map[string]*pending{}, grants: map[string]*RunGrant{}}
}

func (m *Manager) now() time.Time { return m.opt.Now() }

// SetNotifier sets where Inbox notes go. The daemon builds the Manager before the engine that posts the notes.
func (m *Manager) SetNotifier(n Notifier) {
	m.mu.Lock()
	m.opt.Notifier = n
	m.mu.Unlock()
}

// SetChanged sets the function called after a server changed (the stream event of the UI).
func (m *Manager) SetChanged(f func(name string)) {
	m.mu.Lock()
	m.opt.Changed = f
	m.mu.Unlock()
}

func (m *Manager) changed(name string) {
	m.mu.Lock()
	f := m.opt.Changed
	m.mu.Unlock()
	if f != nil {
		f(name)
	}
}

func (m *Manager) postNote(n Note) {
	m.mu.Lock()
	nt := m.opt.Notifier
	m.mu.Unlock()
	if nt != nil {
		nt.PostNote(n)
	}
}

// config returns the configuration of name.
func (m *Manager) config(name string) (spec.RemoteMCP, error) {
	cfg, ok := m.opt.Servers()[name]
	if !ok {
		return spec.RemoteMCP{}, fmt.Errorf("%w: %q", ErrUnknownServer, name)
	}
	return cfg, nil
}

// Names returns the configured server names, sorted.
func (m *Manager) Names() []string {
	names := make([]string, 0, len(m.opt.Servers()))
	for n := range m.opt.Servers() {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// List returns the view of every configured server, by name.
func (m *Manager) List() []View {
	cfgs := m.opt.Servers()
	out := make([]View, 0, len(cfgs))
	for _, n := range m.Names() {
		out = append(out, m.view(n, cfgs[n]))
	}
	return out
}

// Server returns the view of one server.
func (m *Manager) Server(name string) (View, error) {
	cfg, err := m.config(name)
	if err != nil {
		return View{}, err
	}
	return m.view(name, cfg), nil
}

// Detail returns the view of one server with its cached tools.
func (m *Manager) Detail(name string) (Detail, error) {
	cfg, err := m.config(name)
	if err != nil {
		return Detail{}, err
	}
	s := m.get(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	m.load(name, s)
	return Detail{View: viewLocked(name, cfg, s), Tools: append([]Tool{}, s.st.Tools...)}, nil
}

func (m *Manager) view(name string, cfg spec.RemoteMCP) View {
	s := m.get(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	m.load(name, s)
	return viewLocked(name, cfg, s)
}

func viewLocked(name string, cfg spec.RemoteMCP, s *srv) View {
	v := View{
		Name: name, URL: cfg.URL, Auth: cfg.AuthMode(), ClientID: cfg.ClientID, Header: cfg.Header,
		Scopes:          append([]string{}, cfg.Scopes...),
		HasClientSecret: s.sec.ClientSecret != "", HasAPIKey: s.sec.APIKey != "",
		Status: statusOf(cfg, s), Error: s.st.LastError, ErrorAt: s.st.LastErrorAt,
		ToolCount: len(s.st.Tools), ToolsRefreshedAt: s.st.ToolsRefreshedAt,
		ServerName: s.st.ServerName, ServerVersion: s.st.ServerVersion, Transport: s.st.Transport,
	}
	if cfg.UsesOAuth() && s.sec.Token != nil && !s.sec.Token.Expiry.IsZero() {
		exp := s.sec.Token.Expiry
		v.TokenExpiresAt = &exp
	}
	if v.Status == StatusConnected {
		v.Error, v.ErrorAt = "", nil
	}
	return v
}

func statusOf(cfg spec.RemoteMCP, s *srv) Status {
	switch cfg.AuthMode() {
	case spec.MCPAuthOAuth, spec.MCPAuthOAuthClient:
		switch {
		case s.sec.Token == nil && s.st.LastError != "":
			return StatusError
		case s.sec.Token == nil:
			return StatusDisconnected
		case s.st.NeedsReconnect:
			return StatusNeedsReconnect
		case s.st.LastError != "":
			return StatusError
		}
		return StatusConnected
	case spec.MCPAuthHeader:
		if s.sec.APIKey == "" {
			return StatusDisconnected
		}
	}
	switch {
	case s.st.LastError != "":
		return StatusError
	case s.st.ToolsRefreshedAt == nil && s.st.ServerName == "":
		return StatusUnknown
	}
	return StatusConnected
}

// Secrets are write-only values stored for a server. A nil field leaves the stored value alone; an empty string
// removes it.
type Secrets struct {
	ClientSecret *string
	APIKey       *string
}

// SetSecrets stores or removes the client secret and API key of a server.
func (m *Manager) SetSecrets(name string, in Secrets) error {
	s := m.get(name)
	s.mu.Lock()
	m.load(name, s)
	if in.ClientSecret != nil {
		s.sec.ClientSecret = *in.ClientSecret
	}
	if in.APIKey != nil {
		s.sec.APIKey = *in.APIKey
	}
	err := m.saveSecrets(name, s)
	s.mu.Unlock()
	m.changed(name)
	return err
}

// Disconnect drops the stored OAuth token and registration of a server: its status becomes disconnected and the next use
// needs a new Connect. The client secret and the tool cache stay.
func (m *Manager) Disconnect(name string) error {
	cfg, err := m.config(name)
	if err != nil {
		return err
	}
	if !cfg.UsesOAuth() {
		return fmt.Errorf("%w: %q authenticates with %s", ErrNotOAuth, name, cfg.AuthMode())
	}
	return m.DropAuth(name)
}

// DropAuth deletes the stored OAuth token, registration and pending authorizations of name whatever its configured mode
// is (the API calls it when a change of URL, mode or client makes the tokens meaningless).
func (m *Manager) DropAuth(name string) error {
	s := m.get(name)
	s.mu.Lock()
	m.load(name, s)
	s.sec.Token, s.sec.Endpoints, s.sec.Registration = nil, nil, nil
	s.st.NeedsReconnect, s.st.LastError, s.st.LastErrorAt = false, "", nil
	err := errors.Join(m.saveSecrets(name, s), m.saveState(name, s))
	s.mu.Unlock()
	m.mu.Lock()
	for st, p := range m.flows {
		if p.server == name {
			delete(m.flows, st)
		}
	}
	m.mu.Unlock()
	m.changed(name)
	return err
}

// noteOK records that an operation worked.
func (m *Manager) noteOK(name, serverName, serverVersion, transport string) {
	s := m.get(name)
	s.mu.Lock()
	m.load(name, s)
	dirty := s.st.LastError != "" || s.st.NeedsReconnect || s.st.ServerName != serverName || s.st.ServerVersion != serverVersion || s.st.Transport != transport
	s.st.LastError, s.st.LastErrorAt, s.st.NeedsReconnect = "", nil, false
	s.st.ServerName, s.st.ServerVersion, s.st.Transport = serverName, serverVersion, transport
	var err error
	if dirty {
		err = m.saveState(name, s)
	}
	s.mu.Unlock()
	if err != nil {
		m.opt.Log.Warn("mcpclient: save state", "server", name, "err", err)
	}
	if dirty {
		m.changed(name)
	}
}

// noteErr records a failed operation. needsReconnect marks credentials that were rejected for good; the first time that
// happens it also posts the Inbox note with the Reconnect link.
func (m *Manager) noteErr(name string, cause error, needsReconnect bool) {
	s := m.get(name)
	s.mu.Lock()
	m.load(name, s)
	note := m.noteErrLocked(name, s, cause, needsReconnect)
	s.mu.Unlock()
	if note != nil {
		m.postNote(*note)
	}
	m.changed(name)
}

// noteErrLocked is noteErr with s.mu held; it returns the note to post once the lock is released.
func (m *Manager) noteErrLocked(name string, s *srv, cause error, needsReconnect bool) *Note {
	msg := strings.TrimSpace(cause.Error())
	if len(msg) > 500 {
		msg = msg[:500] + "..."
	}
	at := m.now().UTC()
	s.st.LastError, s.st.LastErrorAt = msg, &at
	var note *Note
	if needsReconnect && !s.st.NeedsReconnect {
		s.st.NeedsReconnect = true
		note = &Note{
			Server: name, Kind: "reconnect", Priority: "high",
			Title:     fmt.Sprintf("MCP server %s needs to be reconnected", name),
			BodyMD:    fmt.Sprintf("The server rejected hal9001's stored credentials and refreshing them failed: %s\n\nSensors and chats cannot use `%s` until you connect it again.", msg, name),
			LinkLabel: "Reconnect", LinkPath: "/settings/context?mcp_server=" + name,
		}
	}
	if err := m.saveState(name, s); err != nil {
		m.opt.Log.Warn("mcpclient: save state", "server", name, "err", err)
	}
	return note
}

// pruneFlowsLocked drops expired authorizations. m.mu must be held.
func (m *Manager) pruneFlowsLocked() {
	for st, p := range m.flows {
		if m.now().Sub(p.created) > m.opt.FlowTTL {
			delete(m.flows, st)
		}
	}
}

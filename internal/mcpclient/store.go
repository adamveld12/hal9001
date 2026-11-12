package mcpclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Files per server under <home>/data/mcp (directory 0700, files 0600):
//
//	<name>.secrets.json  client secret, API key, OAuth registration and tokens. Never leaves the daemon: no API returns
//	                     it, and it is not in config.yaml.
//	<name>.state.json    what the daemon learned about the server: tool cache, last outcome. Not secret.
const (
	secretsSuffix = ".secrets.json"
	stateSuffix   = ".state.json"
)

// secrets is the secret half of a server's stored data.
type secrets struct {
	// ClientSecret is the pasted OAuth client secret (auth: oauth_client).
	ClientSecret string `json:"client_secret,omitempty"`
	// APIKey is the value of the key header (auth: header).
	APIKey string `json:"api_key,omitempty"`
	// Registration is the client dynamic client registration produced (auth: oauth).
	Registration *registration `json:"registration,omitempty"`
	// Token is the current OAuth token.
	Token *tokenSet `json:"token,omitempty"`
	// Endpoints is what the token was issued under; refreshing needs it.
	Endpoints *endpoints `json:"endpoints,omitempty"`
}

type registration struct {
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret,omitempty"`
	RedirectURI  string    `json:"redirect_uri"`
	Issuer       string    `json:"issuer"`
	AuthMethod   string    `json:"auth_method"`
	At           time.Time `json:"at"`
}

type tokenSet struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	Scope        string `json:"scope,omitempty"`
	// Expiry is zero when the server did not say how long the access token lives.
	Expiry time.Time `json:"expiry,omitempty"`
}

// endpoints is the part of the discovered authorization server a refresh needs.
type endpoints struct {
	Issuer     string `json:"issuer"`
	TokenURL   string `json:"token_url"`
	Resource   string `json:"resource,omitempty"`
	ClientID   string `json:"client_id"`
	AuthMethod string `json:"auth_method"`
}

// state is the non-secret half.
type state struct {
	Tools            []Tool     `json:"tools,omitempty"`
	ToolsRefreshedAt *time.Time `json:"tools_refreshed_at,omitempty"`
	// NeedsReconnect is set when the stored credentials were rejected and could not be refreshed.
	NeedsReconnect bool       `json:"needs_reconnect,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	LastErrorAt    *time.Time `json:"last_error_at,omitempty"`
	ServerName     string     `json:"server_name,omitempty"`
	ServerVersion  string     `json:"server_version,omitempty"`
	Transport      string     `json:"transport,omitempty"`
}

// srv is the in-memory copy of one server's files, guarded by mu. mu is also held while a token is refreshed so that
// concurrent callers do not redeem the same refresh token twice.
type srv struct {
	mu     sync.Mutex
	loaded bool
	sec    secrets
	st     state
}

func (m *Manager) dir() string { return filepath.Join(m.opt.Home, "data", "mcp") }

func (m *Manager) path(name, suffix string) string { return filepath.Join(m.dir(), name+suffix) }

// get returns the srv of name, loading its files on first use. The caller must hold s.mu around every access.
func (m *Manager) get(name string) *srv {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.servers[name]
	if !ok {
		s = &srv{}
		m.servers[name] = s
	}
	return s
}

// load reads the files once. Callers hold s.mu.
func (m *Manager) load(name string, s *srv) {
	if s.loaded {
		return
	}
	s.loaded = true
	readJSON(m.path(name, secretsSuffix), &s.sec, m.opt.Log)
	readJSON(m.path(name, stateSuffix), &s.st, m.opt.Log)
}

func readJSON(path string, v any, log *slog.Logger) {
	b, err := os.ReadFile(path) //nolint:gosec // path is built from a validated server name under the home dir
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Warn("mcpclient: read", "path", path, "err", err)
		}
		return
	}
	if err := json.Unmarshal(b, v); err != nil {
		log.Warn("mcpclient: parse", "path", path, "err", err)
	}
}

// saveSecrets and saveState write s's data. Callers hold s.mu.
func (m *Manager) saveSecrets(name string, s *srv) error {
	return writeJSON(m.dir(), m.path(name, secretsSuffix), s.sec)
}

func (m *Manager) saveState(name string, s *srv) error {
	return writeJSON(m.dir(), m.path(name, stateSuffix), s.st)
}

// writeJSON replaces path atomically with 0600 permissions (the directory is created 0700).
func writeJSON(dir, path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mcpclient: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("mcpclient: write %s: %w", path, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Forget deletes everything stored for the server (credentials, tool cache). It is called when the server is removed.
func (m *Manager) Forget(name string) error {
	s := m.get(name)
	s.mu.Lock()
	s.sec, s.st, s.loaded = secrets{}, state{}, true
	s.mu.Unlock()
	m.mu.Lock()
	delete(m.servers, name)
	for st, p := range m.flows {
		if p.server == name {
			delete(m.flows, st)
		}
	}
	m.mu.Unlock()
	var errs []error
	for _, suffix := range []string{secretsSuffix, stateSuffix} {
		if err := os.Remove(m.path(name, suffix)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	m.changed(name)
	return errors.Join(errs...)
}

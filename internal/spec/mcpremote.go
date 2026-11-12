package spec

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Authentication modes of a remote MCP server (docs/mcp-servers.md).
const (
	// MCPAuthOAuth is OAuth with automatic client registration (RFC 7591 dynamic registration).
	MCPAuthOAuth = "oauth"
	// MCPAuthOAuthClient is OAuth with a client id and secret the user pasted, for providers without dynamic registration.
	MCPAuthOAuthClient = "oauth_client"
	// MCPAuthHeader sends one secret header (an API key) with every request.
	MCPAuthHeader = "header"
	// MCPAuthNone sends no credentials.
	MCPAuthNone = "none"
)

// MCPAuthModes are the valid values of RemoteMCP.Auth.
var MCPAuthModes = []string{MCPAuthOAuth, MCPAuthOAuthClient, MCPAuthHeader, MCPAuthNone}

// RemoteMCP is one entry of config.yaml `mcp_servers`: a remote MCP server hal9001 connects to as a client. Only
// non-secret fields live here; client secrets, API keys and OAuth tokens are in the secrets store (data/mcp/).
//
// Not to be confused with MCPServer, which an agent node passes through to its agent session.
type RemoteMCP struct {
	// URL is the Streamable HTTP endpoint of the server (https; http only for a loopback host).
	URL string `yaml:"url" json:"url"`
	// Auth is one of MCPAuthModes; empty means oauth.
	Auth string `yaml:"auth,omitempty" json:"auth,omitempty"`
	// ClientID is the pasted OAuth client id (auth: oauth_client). It is not a secret.
	ClientID string `yaml:"client_id,omitempty" json:"client_id,omitempty"`
	// Header is the name of the header that carries the API key (auth: header).
	Header string `yaml:"header,omitempty" json:"header,omitempty"`
	// Scopes overrides the OAuth scopes hal9001 asks for; empty uses what the server advertises.
	Scopes []string `yaml:"scopes,omitempty" json:"scopes,omitempty"`
}

// AuthMode returns Auth with the default applied.
func (r RemoteMCP) AuthMode() string {
	if r.Auth == "" {
		return MCPAuthOAuth
	}
	return r.Auth
}

// UsesOAuth reports whether the server authenticates with OAuth (either client registration mode).
func (r RemoteMCP) UsesOAuth() bool {
	m := r.AuthMode()
	return m == MCPAuthOAuth || m == MCPAuthOAuthClient
}

var (
	mcpNameRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	headerNameRE = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

// ValidMCPServerName reports whether name is usable as an MCP server name: a lowercase slug of at most 32 characters.
func ValidMCPServerName(name string) bool { return mcpNameRE.MatchString(name) }

// forbiddenAuthHeaders are headers an API key must not be sent in: the transport sets them itself.
var forbiddenAuthHeaders = []string{"host", "content-length", "content-type", "accept", "connection", "transfer-encoding", "mcp-session-id", "mcp-protocol-version"}

// ValidateMCPServerURL checks that raw is an absolute https URL, or an http URL for a loopback host, without
// credentials or a fragment.
func ValidateMCPServerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("must be an absolute http(s) URL")
	}
	if u.User != nil {
		return fmt.Errorf("must not contain credentials; use an auth mode instead")
	}
	if u.Fragment != "" {
		return fmt.Errorf("must not contain a fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return fmt.Errorf("must use https (http is only allowed for localhost)")
		}
	default:
		return fmt.Errorf("must be an https URL")
	}
	return nil
}

func isLoopbackHost(h string) bool {
	return h == "localhost" || h == "::1" || strings.HasPrefix(h, "127.")
}

// ValidateRemoteMCP checks one `mcp_servers` entry named name; path prefixes the finding paths.
func validateRemoteMCP(c *collector, path, name string, r RemoteMCP) {
	if !ValidMCPServerName(name) {
		c.errorf(CodeInvalidID, path, "MCP server name %q is invalid: use lowercase letters, digits, - and _ (max 32, starting with a letter or digit)", name)
	}
	if r.URL == "" {
		c.errorf(CodeRequired, path+".url", "MCP server %q needs a url", name)
	} else if err := ValidateMCPServerURL(r.URL); err != nil {
		c.errorf(CodeInvalidValue, path+".url", "MCP server %q url %s", name, err)
	}
	c.enum(path+".auth", r.Auth, MCPAuthModes, false)
	mode := r.AuthMode()
	switch {
	case mode == MCPAuthOAuthClient && r.ClientID == "":
		c.errorf(CodeRequired, path+".client_id", "MCP server %q uses auth oauth_client and needs a client_id (the secret is stored separately)", name)
	case mode != MCPAuthOAuthClient && r.ClientID != "":
		c.errorf(CodeFieldNotAllowed, path+".client_id", "client_id applies to auth oauth_client only")
	}
	switch {
	case mode == MCPAuthHeader && r.Header == "":
		c.errorf(CodeRequired, path+".header", "MCP server %q uses auth header and needs the header name that carries the API key", name)
	case mode == MCPAuthHeader && !headerNameRE.MatchString(r.Header):
		c.errorf(CodeInvalidValue, path+".header", "%q is not a valid HTTP header name", r.Header)
	case mode == MCPAuthHeader && slices.Contains(forbiddenAuthHeaders, strings.ToLower(r.Header)):
		c.errorf(CodeInvalidValue, path+".header", "header %q is set by the MCP transport and cannot carry a key", r.Header)
	case mode != MCPAuthHeader && r.Header != "":
		c.errorf(CodeFieldNotAllowed, path+".header", "header applies to auth header only")
	}
	if len(r.Scopes) > 0 && !r.UsesOAuth() {
		c.errorf(CodeFieldNotAllowed, path+".scopes", "scopes apply to the OAuth auth modes only")
	}
	for i, s := range r.Scopes {
		if s == "" || strings.ContainsAny(s, " \t\r\n\"\\") {
			c.errorf(CodeInvalidValue, fmt.Sprintf("%s.scopes[%d]", path, i), "scope %q must be a single token without spaces", s)
		}
	}
}

// validateSensorMCPServers checks a sensor's mcp_servers list: valid, unique names that exist in refs (when known).
func validateSensorMCPServers(c *collector, names []string, refs *Refs) {
	seen := map[string]bool{}
	for i, n := range names {
		p := fmt.Sprintf("mcp_servers[%d]", i)
		switch {
		case !ValidMCPServerName(n):
			c.errorf(CodeInvalidID, p, "MCP server name %q is invalid: use the lowercase name from Settings → Context → MCP servers", n)
		case seen[n]:
			c.errorf(CodeDuplicateID, p, "MCP server %q is listed twice", n)
		case refs != nil && refs.MCPServers != nil && !refs.MCPServers[n]:
			c.errorf(CodeUnknownMCPServer, p, "unknown MCP server %q%s", n, availableList(sortedKeys(refs.MCPServers)))
		}
		seen[n] = true
	}
}

// MCPServerRefs returns the names of the remote MCP servers cfg defines. It is never nil, so a sensor's mcp_servers
// are always checked against it.
func MCPServerRefs(cfg *Config) map[string]bool {
	out := map[string]bool{}
	if cfg != nil {
		for name := range cfg.MCPServers {
			out[name] = true
		}
	}
	return out
}

// SensorRefs returns the references a sensor file may use (its mcp_servers), read from config.yaml under home. A
// config.yaml that is missing or does not parse has no servers.
func SensorRefs(home string) *Refs {
	var cfg *Config
	if raw, _, err := ReadFile(home, KindConfig, ""); err == nil {
		cfg, _ = ParseConfig(raw)
	}
	return &Refs{MCPServers: MCPServerRefs(cfg)}
}

// ValidateRemoteMCPEntry checks one server definition on its own (the API validates a request before it edits
// config.yaml). Finding paths are relative to the entry.
func ValidateRemoteMCPEntry(name string, r RemoteMCP) []Diagnostic {
	c := &collector{}
	validateRemoteMCP(c, "", name, r)
	for i := range c.ds {
		c.ds[i].Path = strings.TrimPrefix(c.ds[i].Path, ".")
	}
	return c.ds
}

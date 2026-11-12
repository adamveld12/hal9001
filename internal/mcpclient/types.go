package mcpclient

import (
	"encoding/json"
	"errors"
	"time"
)

// Status of a configured server, derived from its credentials and the outcome of its last operation.
type Status string

// The server states.
const (
	// StatusConnected: credentials are in place and the last operation worked (or, for OAuth, a token is stored and not
	// known to be bad).
	StatusConnected Status = "connected"
	// StatusNeedsReconnect: the server rejected the stored OAuth credentials and refreshing them failed; the user has to
	// run the authorization again.
	StatusNeedsReconnect Status = "needs_reconnect"
	// StatusDisconnected: no credentials yet (an OAuth server not connected, a header server without a key).
	StatusDisconnected Status = "disconnected"
	// StatusError: the last operation failed for a reason other than rejected credentials.
	StatusError Status = "error"
	// StatusUnknown: nothing has been tried yet (a server that needs no OAuth).
	StatusUnknown Status = "unknown"
)

// Errors the API maps to responses.
var (
	// ErrUnknownServer: no server of that name is configured.
	ErrUnknownServer = errors.New("mcpclient: unknown MCP server")
	// ErrNotConnected: an OAuth server has no token (never connected, or disconnected).
	ErrNotConnected = errors.New("mcpclient: server is not connected")
	// ErrNeedsReconnect: the stored credentials no longer work; the user must reconnect.
	ErrNeedsReconnect = errors.New("mcpclient: server needs to be reconnected")
	// ErrInvalidState: the OAuth callback carried an unknown, expired or already used state.
	ErrInvalidState = errors.New("mcpclient: invalid or expired authorization state")
	// ErrNoRegistration: the authorization server has no dynamic client registration and no client id was configured.
	ErrNoRegistration = errors.New("mcpclient: authorization server does not support dynamic client registration")
	// ErrNoAuthRequired: the server answered an unauthenticated request, so OAuth does not apply.
	ErrNoAuthRequired = errors.New("mcpclient: server did not ask for authorization")
	// ErrNotOAuth: the operation applies to OAuth servers only.
	ErrNotOAuth = errors.New("mcpclient: server does not use OAuth")
	// ErrNotAllowed: a run token does not grant the server.
	ErrNotAllowed = errors.New("mcpclient: server is not granted to this run")
)

// View is what the API shows of a server. It never carries a secret: only whether one is stored.
type View struct {
	Name     string   `json:"name"`
	URL      string   `json:"url"`
	Auth     string   `json:"auth"`
	ClientID string   `json:"client_id,omitempty"`
	Header   string   `json:"header,omitempty"`
	Scopes   []string `json:"scopes"`
	// HasClientSecret and HasAPIKey say that a secret is stored, never what it is.
	HasClientSecret bool   `json:"has_client_secret"`
	HasAPIKey       bool   `json:"has_api_key"`
	Status          Status `json:"status"`
	// Error is the message of the last failed operation (empty while status is connected).
	Error            string     `json:"error,omitempty"`
	ErrorAt          *time.Time `json:"error_at,omitempty"`
	ToolCount        int        `json:"tool_count"`
	ToolsRefreshedAt *time.Time `json:"tools_refreshed_at,omitempty"`
	// TokenExpiresAt is when the stored OAuth access token expires (it is refreshed before that).
	TokenExpiresAt *time.Time `json:"token_expires_at,omitempty"`
	ServerName     string     `json:"server_name,omitempty"`
	ServerVersion  string     `json:"server_version,omitempty"`
	// Transport is the one the last successful connection used: streamable_http or sse.
	Transport string `json:"transport,omitempty"`
}

// Tool is one tool of a server as the server describes it.
type Tool struct {
	Name         string          `json:"name"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
}

// Detail is a View with the cached tool list.
type Detail struct {
	View
	Tools []Tool `json:"tools"`
}

// ToolChanges is the difference between the cached tool list and a fresh one.
type ToolChanges struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	// Changed lists tools whose title, description or schema differ.
	Changed []string `json:"changed"`
}

// Any reports whether anything differs.
func (c ToolChanges) Any() bool { return len(c.Added)+len(c.Removed)+len(c.Changed) > 0 }

// TestResult is the outcome of Test.
type TestResult struct {
	OK              bool   `json:"ok"`
	Status          Status `json:"status"`
	LatencyMS       int64  `json:"latency_ms"`
	ServerName      string `json:"server_name,omitempty"`
	ServerVersion   string `json:"server_version,omitempty"`
	ProtocolVersion string `json:"protocol_version,omitempty"`
	ToolCount       int    `json:"tool_count"`
	Transport       string `json:"transport,omitempty"`
	Error           string `json:"error,omitempty"`
}

// CallResult is the outcome of a tool call.
type CallResult struct {
	// IsError is the tool's own error flag: the call reached the tool and the tool reported a failure.
	IsError bool `json:"is_error"`
	// Text is the text content blocks joined by newlines (other content is described in brackets).
	Text string `json:"text"`
	// Structured is the tool's structuredContent when it returned any.
	Structured json.RawMessage `json:"structured_content,omitempty"`
}

// Note is an informational or actionable message for the Inbox.
type Note struct {
	Server   string
	Title    string
	BodyMD   string
	Priority string // low, normal, high
	// Kind distinguishes notes of one server for threading: "tools" or "reconnect".
	Kind      string
	LinkLabel string
	// LinkPath is an in-app path (for example /settings/context?mcp_server=notion).
	LinkPath string
}

// Notifier posts Inbox notes. The daemon implements it with the engine; tests record them.
type Notifier interface {
	PostNote(n Note)
}

// CallLog is one tool call made with a run token.
type CallLog struct {
	Server     string    `json:"server"`
	Tool       string    `json:"tool"`
	At         time.Time `json:"at"`
	DurationMS int64     `json:"duration_ms"`
	OK         bool      `json:"ok"`
	Error      string    `json:"error,omitempty"`
}

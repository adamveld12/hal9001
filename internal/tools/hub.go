package tools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// DefaultApprovalTimeout bounds an approval wait when the run sets none.
const DefaultApprovalTimeout = time.Hour

// TokenPrefix starts every capability token.
const TokenPrefix = "hal_mcp_"

// Session kinds (RunSpec.Kind).
const (
	KindFlow   = "flow"
	KindAssist = "assist"
)

// ApprovalRequest asks the host for a user's decision on one call.
type ApprovalRequest struct {
	RunID     string
	NodeRunID string
	Tool      string
	Args      json.RawMessage
	// Reason says why the call needs approval.
	Reason string
	// Timeout is how long to wait; the host returns Decision.Expired when it elapses.
	Timeout time.Duration
}

// Decision is the host's answer to an ApprovalRequest.
type Decision struct {
	Allowed bool
	// Expired reports that nobody decided within the timeout (Allowed is false).
	Expired bool
	// Comment is the approver's comment, if any.
	Comment string
	// Cancelled reports that the approver also stopped the run (the host has cancelled it).
	Cancelled bool
	// RecordID identifies the trace record the host made for the call; it is passed back to Host.Finish.
	RecordID string
}

// CallRecord describes a call that needed no approval.
type CallRecord struct {
	RunID     string
	NodeRunID string
	Tool      string
	Args      json.RawMessage
	// Reason says why the call was allowed without approval.
	Reason string
}

// Host is what the daemon supplies to the hub: the approval queue and the run trace.
type Host interface {
	// Approve registers a tool_call approval and blocks until it is decided, the timeout passes, or ctx ends
	// (then it returns ctx's error). It records the call in the run trace; Decision.RecordID names that record.
	Approve(ctx context.Context, req ApprovalRequest) (Decision, error)
	// Record puts an auto-allowed call in the run trace before it runs and returns the record's id.
	Record(ctx context.Context, c CallRecord) (string, error)
	// Finish adds the outcome of the call ("ok" or an error text) to the record.
	Finish(ctx context.Context, recordID, outcome string)
}

// TokenStore persists token hashes. *store.Store implements it through an adapter in internal/engine.
type TokenStore interface {
	CreateToken(ctx context.Context, hash, runID, kind string) error
	RevokeToken(ctx context.Context, hash string) error
}

// RunSpec describes the run a session serves.
type RunSpec struct {
	// Kind is KindFlow or KindAssist.
	Kind string
	// RunID is the agent run the token is scoped to (for assist, the session id).
	RunID string
	// Info returns the run's current state; for an assist session it follows the current turn. Required.
	Info func() RunInfo
	// ApprovalTimeout bounds one approval wait (default DefaultApprovalTimeout).
	ApprovalTimeout time.Duration
	// ReadOnly makes every call that would need approval fail without asking: the run may not act (a read-only or
	// dry-run agent node, the planning phase of a proposal).
	ReadOnly bool

	// Node fields for providers (sandbox, custom tools): see internal/spec.Node.
	Sandbox string
	Image   string
	Repo    string
	Ref     string
	Tools   []string
}

// Extension is what a Provider adds to one session.
type Extension struct {
	Tools []Tool
	// Close is called once when the session closes. May be nil.
	Close func()
	// CloseWith is like Close but learns whether the run succeeded (Session.CloseWith; plain Close reports false).
	// A sandbox keeps a failed run's workspace for debugging. May be nil.
	CloseWith func(success bool)
}

// Provider contributes tools to a session (sandbox fs/shell, custom tools, assist draft tools).
type Provider func(ctx context.Context, spec RunSpec) (Extension, error)

// Options configures NewHub.
type Options struct {
	Store TokenStore
	Host  Host
	// Clock defaults to time.Now.
	Clock func() time.Time
	// GH configures the gh tools.
	GH  GHOptions
	Log *slog.Logger
}

// Hub owns the live sessions of a daemon.
type Hub struct {
	opt  Options
	base *Registry

	mu        sync.Mutex
	providers []Provider
	sessions  map[string]*Session // token hash -> session
}

// NewHub returns a hub whose sessions get the built-in tools.
func NewHub(opt Options) (*Hub, error) {
	if opt.Host == nil {
		return nil, errors.New("tools: Host is required")
	}
	if opt.Clock == nil {
		opt.Clock = time.Now
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	base := NewRegistry()
	if err := base.Register(builtinTools(opt.GH)...); err != nil {
		return nil, err
	}
	return &Hub{opt: opt, base: base, sessions: map[string]*Session{}}, nil
}

// AddProvider registers a provider consulted on every later Open.
func (h *Hub) AddProvider(p Provider) {
	h.mu.Lock()
	h.providers = append(h.providers, p)
	h.mu.Unlock()
}

// Session is one run's tool set and its capability token.
type Session struct {
	h     *Hub
	spec  RunSpec
	reg   *Registry
	token string
	hash  string

	mu      sync.Mutex
	closers []func(success bool)
	closed  bool
}

// HashToken is the stored form of a token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Open creates a session: it builds the tool set, issues a token and records its hash. The caller must Close it
// when the run ends.
func (h *Hub) Open(ctx context.Context, spec RunSpec) (*Session, error) {
	if spec.Info == nil || spec.RunID == "" {
		return nil, errors.New("tools: RunSpec needs RunID and Info")
	}
	if spec.Kind == "" {
		spec.Kind = KindFlow
	}
	if spec.ApprovalTimeout <= 0 {
		spec.ApprovalTimeout = DefaultApprovalTimeout
	}
	s := &Session{h: h, spec: spec, reg: h.base.Clone()}
	h.mu.Lock()
	providers := append([]Provider(nil), h.providers...)
	h.mu.Unlock()
	for _, p := range providers {
		ext, err := p(ctx, spec)
		if ext.Close != nil {
			c := ext.Close
			s.closers = append(s.closers, func(bool) { c() })
		}
		if ext.CloseWith != nil {
			s.closers = append(s.closers, ext.CloseWith)
		}
		if err == nil {
			err = s.reg.Register(ext.Tools...)
		}
		if err != nil {
			s.release(false)
			return nil, err
		}
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		s.release(false)
		return nil, err
	}
	s.token = TokenPrefix + base64.RawURLEncoding.EncodeToString(raw[:])
	s.hash = HashToken(s.token)
	if h.opt.Store != nil {
		if err := h.opt.Store.CreateToken(ctx, s.hash, spec.RunID, spec.Kind); err != nil {
			s.release(false)
			return nil, fmt.Errorf("tools: store token: %w", err)
		}
	}
	h.mu.Lock()
	h.sessions[s.hash] = s
	h.mu.Unlock()
	return s, nil
}

// Lookup returns the live session of a token. A revoked, unknown or malformed token yields false.
func (h *Hub) Lookup(token string) (*Session, bool) {
	if !strings.HasPrefix(token, TokenPrefix) {
		return nil, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[HashToken(token)]
	return s, ok
}

// Token is the capability token to hand to `hal9001 mcp --token`.
func (s *Session) Token() string { return s.token }

// Tools lists the session's tools.
func (s *Session) Tools() []ToolInfo { return s.reg.List() }

// Close revokes the token and releases the tool set as a failed run. It is safe to call more than once.
func (s *Session) Close() { s.CloseWith(false) }

// CloseWith is Close that tells the tool set whether the run succeeded.
func (s *Session) CloseWith(success bool) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	s.h.mu.Lock()
	delete(s.h.sessions, s.hash)
	s.h.mu.Unlock()
	if s.h.opt.Store != nil {
		// Revocation must complete even when the run's context is already cancelled.
		if err := s.h.opt.Store.RevokeToken(context.Background(), s.hash); err != nil {
			s.h.opt.Log.Warn("tools: revoke token", "run", s.spec.RunID, "err", err)
		}
	}
	s.release(success)
}

func (s *Session) release(success bool) {
	for i := len(s.closers) - 1; i >= 0; i-- {
		s.closers[i](success)
	}
	s.closers = nil
}

// DeniedError is the error of a call a user denied or let expire.
type DeniedError struct {
	Tool      string
	Expired   bool
	Cancelled bool
	Comment   string
}

func (e *DeniedError) Error() string {
	switch {
	case e.Cancelled:
		return fmt.Sprintf("tool %s was denied by the user and the run was stopped", e.Tool)
	case e.Expired:
		return fmt.Sprintf("tool %s was not run: nobody approved it before the approval timeout", e.Tool)
	case e.Comment != "":
		return fmt.Sprintf("tool %s was denied by the user: %s", e.Tool, e.Comment)
	}
	return fmt.Sprintf("tool %s was denied by the user", e.Tool)
}

// Call runs a tool: approval first when the tool asks for it, then Run. Every call is recorded through the host.
// The returned error is a tool error for the agent (ErrUnknownTool, *DeniedError or the tool's own error).
func (s *Session) Call(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	t, ok := s.reg.Get(name)
	if !ok {
		return Result{}, fmt.Errorf("%w: %s", ErrUnknownTool, name)
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	info := s.spec.Info()
	var recordID string
	if needs, why := approval(t, args); needs {
		if s.spec.ReadOnly || info.DryRun {
			return Result{}, fmt.Errorf("tool %s was not run: %s; this run is read-only", name, why)
		}
		if info.AgentRunID == "" {
			return Result{}, fmt.Errorf("tool %s needs approval but no agent run is active", name)
		}
		d, err := s.h.opt.Host.Approve(ctx, ApprovalRequest{
			RunID: info.AgentRunID, NodeRunID: info.NodeRunID, Tool: name, Args: args, Reason: why, Timeout: s.spec.ApprovalTimeout,
		})
		if err != nil {
			return Result{}, fmt.Errorf("tool %s: approval failed: %w", name, err)
		}
		recordID = d.RecordID
		if !d.Allowed {
			s.finish(recordID, "not run: denied")
			return Result{}, &DeniedError{Tool: name, Expired: d.Expired, Cancelled: d.Cancelled, Comment: d.Comment}
		}
	} else if info.AgentRunID != "" {
		id, err := s.h.opt.Host.Record(ctx, CallRecord{RunID: info.AgentRunID, NodeRunID: info.NodeRunID, Tool: name, Args: args, Reason: "hal tool allowed without approval"})
		if err != nil {
			s.h.opt.Log.Warn("tools: record call", "tool", name, "err", err)
		}
		recordID = id
	}
	res, err := t.Run(ctx, Call{Name: name, Args: args, Info: info, Now: s.h.opt.Clock()})
	switch {
	case err != nil:
		s.finish(recordID, "error: "+err.Error())
	default:
		s.finish(recordID, "ok")
	}
	return res, err
}

func (s *Session) finish(recordID, outcome string) {
	if recordID != "" {
		s.h.opt.Host.Finish(context.WithoutCancel(context.Background()), recordID, outcome)
	}
}

func approval(t Tool, args json.RawMessage) (bool, string) {
	if t.Approval == nil {
		return false, ""
	}
	return t.Approval(args)
}

// ServerName is the name of hal's MCP server in the agent's session.
const ServerName = "hal"

// ServerArgs are the arguments of the command that runs hal's stdio MCP server for token against the daemon at url.
func ServerArgs(url, token string) []string {
	return []string{"mcp", "--url", url, "--token", token}
}

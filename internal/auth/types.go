// Package auth implements the PAM-style authentication of REQUIREMENTS §10.4
// and the Tailscale identity module of §10.5: pluggable modules, ordered
// per-service stacks with PAM control semantics, multi-step logins, sessions,
// and an auth event for every decision.
package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
)

// Service is what is being accessed (REQUIREMENTS §10.4).
type Service string

const (
	ServiceUI      Service = "ui"
	ServiceAPI     Service = "api"
	ServiceApprove Service = "approve"
	ServiceAdmin   Service = "admin"
)

// Services lists every service in a stable order.
var Services = []Service{ServiceUI, ServiceAPI, ServiceApprove, ServiceAdmin}

func (s Service) valid() bool {
	for _, v := range Services {
		if s == v {
			return true
		}
	}
	return false
}

// Control is a PAM control flag.
type Control string

const (
	Required   Control = "required"
	Requisite  Control = "requisite"
	Sufficient Control = "sufficient"
	Optional   Control = "optional"
)

func (c Control) valid() bool {
	switch c {
	case Required, Requisite, Sufficient, Optional:
		return true
	}
	return false
}

// ResultKind is the outcome of one module.
type ResultKind string

const (
	KindSuccess   ResultKind = "success"
	KindFail      ResultKind = "fail"
	KindIgnore    ResultKind = "ignore"
	KindChallenge ResultKind = "challenge"
)

// Principal is the authenticated identity.
type Principal struct {
	// Name is the identity: the Tailscale login, "localhost", or "owner" for
	// password and passkey logins.
	Name string `json:"name"`
}

// Step is one round of a multi-step login. Data goes to the client; State is
// opaque module state kept server-side and handed back with the response.
type Step struct {
	Kind  string          `json:"kind"`
	Data  json.RawMessage `json:"data,omitempty"`
	State json.RawMessage `json:"-"`
}

// StepResponse is the client's answer to a Step, delivered to the module that
// issued it.
type StepResponse struct {
	State    json.RawMessage
	Response json.RawMessage
}

// Result is what a module returns. Build it with Success, Fail, Ignore or
// Challenge.
type Result struct {
	Kind      ResultKind
	Principal Principal
	Reason    string
	Step      *Step
}

// Success reports that the module authenticated the request as p.
func Success(p Principal) Result { return Result{Kind: KindSuccess, Principal: p} }

// Fail reports that the module is applicable and rejected the request.
func Fail(reason string) Result { return Result{Kind: KindFail, Reason: reason} }

// Ignore reports that the module does not apply to this request.
func Ignore(reason string) Result { return Result{Kind: KindIgnore, Reason: reason} }

// Challenge asks the client for more input (PAM conversation). It is only
// honored in interactive login; per-request evaluation treats it as ignore.
func Challenge(step Step) Result { return Result{Kind: KindChallenge, Step: &step} }

// Settings is the part of the configuration modules read. A snapshot is passed
// with every request so config reloads apply immediately.
type Settings struct {
	AllowedHosts  []string
	AllowedLogins []string
	PasswordHash  string
}

// AuthRequest is everything a module may look at.
type AuthRequest struct {
	Service Service
	// HTTPMethod and Path identify the endpoint being accessed.
	HTTPMethod string
	Path       string
	// Host is the Host header (with port, if any).
	Host       string
	RemoteAddr string
	// Peer is the parsed TCP peer address; invalid when unparseable.
	Peer   netip.Addr
	Header http.Header
	// Secure reports whether the request reached us over HTTPS (directly, or
	// through a loopback proxy such as tailscale serve).
	Secure bool
	// Interactive is true during begin/continue login. Modules that need user
	// input (password, webauthn) apply only then.
	Interactive bool
	// Method selects a login method by module name; empty means any.
	Method string
	// Params are the body fields of a login begin request.
	Params map[string]json.RawMessage
	// Step is non-nil when the module is being resumed with a client response.
	Step *StepResponse
	// Options are the options of the stack entry being evaluated.
	Options  map[string]any
	Settings Settings
}

// HostName returns the Host header without port, lower-cased.
func (r *AuthRequest) HostName() string { return hostName(r.Host) }

// StringParam returns a string body field, or "" when absent or not a string.
func (r *AuthRequest) StringParam(name string) string {
	raw, ok := r.Params[name]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func hostName(hostport string) string {
	h := strings.ToLower(hostport)
	if strings.HasPrefix(h, "[") {
		if i := strings.Index(h, "]"); i > 0 {
			return h[1:i]
		}
		return h
	}
	if i := strings.LastIndex(h, ":"); i >= 0 {
		return h[:i]
	}
	return h
}

// Module is an authentication module, compiled in and registered by name.
type Module interface {
	Name() string
	Authenticate(ctx context.Context, req *AuthRequest) Result
}

// Interactive is implemented by modules that need user input, so the login
// UI can offer them as methods.
type Interactive interface {
	Module
	Interactive()
}

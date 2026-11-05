package auth

import (
	"errors"
	"fmt"
	"time"
)

// DefaultSessionTTL is the sliding session lifetime when none is configured.
const DefaultSessionTTL = 7 * 24 * time.Hour

// StackEntry is one `{module, control, options}` line of a stack.
type StackEntry struct {
	Module  string         `json:"module"`
	Control Control        `json:"control"`
	Options map[string]any `json:"options,omitempty"`
}

// Config is the auth configuration. The caller (wave 2) maps the spec config
// onto it.
type Config struct {
	// Stacks maps a stack name to its ordered entries.
	Stacks map[string][]StackEntry
	// Services maps each service to the stack name that guards it.
	Services map[Service]string
	// AllowedHosts are extra Host values accepted by the baseline (the
	// Tailscale hostname). tailscale-identity also requires the Host to be in
	// this list.
	AllowedHosts []string
	// AllowedLogins are the Tailscale logins tailscale-identity accepts. Empty
	// means the login that owns this Mac.
	AllowedLogins []string
	// SessionTTL is the sliding session lifetime; zero means DefaultSessionTTL.
	SessionTTL time.Duration
	// PasswordHash is the argon2id PHC string used by the password module.
	PasswordHash string
}

// DefaultConfig is the default of REQUIREMENTS §10.4.
func DefaultConfig() Config {
	return Config{
		Stacks: map[string][]StackEntry{
			"default": {
				{Module: "localhost-trust", Control: Sufficient},
				{Module: "tailscale-identity", Control: Sufficient},
				{Module: "webauthn", Control: Sufficient},
				{Module: "password", Control: Sufficient},
			},
		},
		Services: map[Service]string{
			ServiceUI: "default", ServiceAPI: "default", ServiceApprove: "default", ServiceAdmin: "default",
		},
	}
}

func (c Config) sessionTTL() time.Duration {
	if c.SessionTTL <= 0 {
		return DefaultSessionTTL
	}
	return c.SessionTTL
}

func (c Config) settings() Settings {
	return Settings{AllowedHosts: c.AllowedHosts, AllowedLogins: c.AllowedLogins, PasswordHash: c.PasswordHash}
}

// Validate checks that every service is mapped to an existing stack and that
// every entry names a registered module with a valid control.
func (c Config) Validate(reg *Registry) error {
	var errs []error
	for _, svc := range Services {
		name, ok := c.Services[svc]
		if !ok {
			errs = append(errs, fmt.Errorf("service %q has no stack", svc))
			continue
		}
		if _, ok := c.Stacks[name]; !ok {
			errs = append(errs, fmt.Errorf("service %q uses undefined stack %q", svc, name))
		}
	}
	for svc := range c.Services {
		if !svc.valid() {
			errs = append(errs, fmt.Errorf("unknown service %q", svc))
		}
	}
	for name, entries := range c.Stacks {
		for i, e := range entries {
			if _, ok := reg.Get(e.Module); !ok {
				errs = append(errs, fmt.Errorf("stack %q entry %d: unknown module %q", name, i, e.Module))
			}
			if !e.Control.valid() {
				errs = append(errs, fmt.Errorf("stack %q entry %d: invalid control %q", name, i, e.Control))
			}
		}
	}
	return errors.Join(errs...)
}

package daemon

import (
	"time"

	"hal9001/internal/auth"
	"hal9001/internal/spec"
)

// PasswordHashOption is the option of a `password` auth-stack entry in
// config.yaml that holds the argon2id PHC hash written by
// `hal9001 auth set-password`.
const PasswordHashOption = "hash"

// AuthConfig maps the config.yaml settings onto the auth manager's
// configuration: stacks, service-to-stack mapping, allowed hosts and logins,
// session lifetime and the password hash. The hash is taken from the first
// `password` entry (service order ui, api, approve, admin, then the stack's
// entry order) that carries a string `hash` option.
func AuthConfig(c *spec.Config) auth.Config {
	cfg := auth.Config{
		Stacks:        make(map[string][]auth.StackEntry, len(c.Auth.Stacks)),
		Services:      make(map[auth.Service]string, len(c.Auth.Services)),
		AllowedHosts:  append([]string(nil), c.AllowedHosts...),
		AllowedLogins: append([]string(nil), c.AllowedLogins...),
		SessionTTL:    time.Duration(c.SessionTTL),
	}
	for name, entries := range c.Auth.Stacks {
		out := make([]auth.StackEntry, len(entries))
		for i, e := range entries {
			out[i] = auth.StackEntry{Module: e.Module, Control: auth.Control(e.Control), Options: e.Options}
		}
		cfg.Stacks[name] = out
	}
	for svc, stack := range c.Auth.Services {
		cfg.Services[auth.Service(svc)] = stack
	}
	cfg.PasswordHash = passwordHash(c)
	return cfg
}

func passwordHash(c *spec.Config) string {
	for _, svc := range auth.Services {
		for _, e := range c.Auth.Stacks[c.Auth.Services[string(svc)]] {
			if e.Module != "password" {
				continue
			}
			if h, ok := e.Options[PasswordHashOption].(string); ok && h != "" {
				return h
			}
		}
	}
	return ""
}

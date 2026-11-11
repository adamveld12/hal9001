// Package authcfg maps the auth part of config.yaml onto the auth manager's
// configuration and edits it as data: which sign-in methods the stacks give
// the services, the password hash that rides in the password entries, and the
// comment-preserving YAML writes behind `hal9001 auth set-password` and the
// /api/auth/* settings endpoints.
package authcfg

import (
	"encoding/json"
	"maps"
	"slices"
	"time"

	"hal9001/internal/auth"
	"hal9001/internal/spec"
)

// PasswordHashOption is the option of a `password` auth-stack entry in
// config.yaml that holds the argon2id PHC hash.
const PasswordHashOption = "hash"

// Modules the sign-in settings know by name.
const (
	ModuleLocalhost = "localhost-trust"
	ModuleTailscale = "tailscale-identity"
	ModulePasskeys  = "webauthn"
	ModulePassword  = "password"
)

// Method is one way in, as the settings present it.
type Method struct {
	// ID is the contract's method id.
	ID string
	// Module is the auth module that implements it.
	Module string
	// Name labels the row.
	Name string
	// Via completes "signed in with ...".
	Via        string
	Toggleable bool
	// Remote methods admit requests that do not come from this Mac.
	Remote bool
}

// Methods lists the ways in in the order the overview shows them.
var Methods = []Method{
	{ID: "localhost", Module: ModuleLocalhost, Name: "On this Mac", Via: "this Mac"},
	{ID: "tailscale", Module: ModuleTailscale, Name: "From your tailnet", Via: "your tailnet", Toggleable: true, Remote: true},
	{ID: "passkeys", Module: ModulePasskeys, Name: "Passkeys", Via: "a passkey", Toggleable: true, Remote: true},
	{ID: "password", Module: ModulePassword, Name: "Password", Via: "the password", Toggleable: true, Remote: true},
}

// MethodByID returns the method with the contract id id.
func MethodByID(id string) (Method, bool) {
	for _, m := range Methods {
		if m.ID == id {
			return m, true
		}
	}
	return Method{}, false
}

// MethodByModule returns the method an auth module implements.
func MethodByModule(module string) (Method, bool) {
	for _, m := range Methods {
		if m.Module == module {
			return m, true
		}
	}
	return Method{}, false
}

// IsRemoteModule reports whether module admits requests from other devices.
func IsRemoteModule(module string) bool {
	m, ok := MethodByModule(module)
	return ok && m.Remote
}

// AuthConfig maps the config.yaml settings onto the auth manager's
// configuration: stacks, service-to-stack mapping, allowed hosts and logins,
// session lifetime and the password hash. The hash is taken from the first
// `password` entry (service order ui, api, approve, admin, then the stack's
// entry order) that carries a string `hash` option.
func AuthConfig(c *spec.Config) auth.Config {
	cfg := auth.Config{
		Stacks:        make(map[string][]auth.StackEntry, len(c.Auth.Stacks)),
		Services:      make(map[auth.Service]string, len(c.Auth.Services)),
		AllowedHosts:  slices.Clone(c.AllowedHosts),
		AllowedLogins: slices.Clone(c.AllowedLogins),
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
	cfg.PasswordHash = PasswordHash(c.Auth)
	return cfg
}

// PasswordHash is the stored password hash: the `hash` option of the first
// password entry, over the stacks the services use, that has one.
func PasswordHash(a spec.Auth) string {
	for _, name := range UsedStacks(a) {
		for _, e := range a.Stacks[name] {
			if e.Module != ModulePassword {
				continue
			}
			if h, ok := e.Options[PasswordHashOption].(string); ok && h != "" {
				return h
			}
		}
	}
	return ""
}

// UsedStacks lists the stacks some service uses, in service order (ui, api,
// approve, admin) without repeats. A service with no mapping uses the default stack.
func UsedStacks(a spec.Auth) []string {
	var out []string
	for _, svc := range spec.Services {
		name := a.Services[svc]
		if name == "" {
			name = spec.DefaultStack
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// AdminModules lists the modules of the stack that guards the admin service.
func AdminModules(a spec.Auth) []string {
	name := a.Services[spec.ServiceAdmin]
	if name == "" {
		name = spec.DefaultStack
	}
	var out []string
	for _, e := range a.Stacks[name] {
		if !slices.Contains(out, e.Module) {
			out = append(out, e.Module)
		}
	}
	return out
}

// UsedModules lists the modules of every stack some service uses.
func UsedModules(a spec.Auth) []string {
	var out []string
	for _, name := range UsedStacks(a) {
		for _, e := range a.Stacks[name] {
			if !slices.Contains(out, e.Module) {
				out = append(out, e.Module)
			}
		}
	}
	return out
}

// Shape says whether module is in the stacks the services use (enabled) and
// whether that is not the plain shape (custom): present in some of those
// stacks but not all, or with a control other than sufficient.
func Shape(a spec.Auth, module string) (enabled, custom bool) {
	all := true
	for _, name := range UsedStacks(a) {
		found, plain := false, false
		for _, e := range a.Stacks[name] {
			if e.Module == module {
				found = true
				plain = plain || e.Control == spec.ControlSufficient
			}
		}
		enabled = enabled || found
		all = all && found
		custom = custom || (found && !plain)
	}
	return enabled, custom || (enabled && !all)
}

// Clone copies a so that edits of the copy do not reach a.
func Clone(a spec.Auth) spec.Auth {
	out := spec.Auth{Services: maps.Clone(a.Services)}
	if a.Stacks != nil {
		out.Stacks = make(map[string][]spec.AuthModule, len(a.Stacks))
		for name, entries := range a.Stacks {
			c := make([]spec.AuthModule, len(entries))
			for i, e := range entries {
				c[i] = spec.AuthModule{Module: e.Module, Control: e.Control, Options: maps.Clone(e.Options)}
			}
			out.Stacks[name] = c
		}
	}
	return out
}

// Enable returns a with module added as `{module, control: sufficient}` to
// every stack a service uses that lacks it. Entries that exist are left as they are.
func Enable(a spec.Auth, module string) spec.Auth {
	out := Clone(a)
	hash := ""
	if module == ModulePassword {
		hash = PasswordHash(a)
	}
	for _, name := range UsedStacks(out) {
		entries, defined := out.Stacks[name]
		if !defined || slices.ContainsFunc(entries, func(e spec.AuthModule) bool { return e.Module == module }) {
			continue
		}
		e := spec.AuthModule{Module: module, Control: spec.ControlSufficient}
		if hash != "" {
			e.Options = map[string]any{PasswordHashOption: hash}
		}
		out.Stacks[name] = append(entries, e)
	}
	return out
}

// Disable returns a without any entry of module, in every stack. Disabling
// password thereby also forgets its hash.
func Disable(a spec.Auth, module string) spec.Auth {
	out := Clone(a)
	for name, entries := range out.Stacks {
		out.Stacks[name] = slices.DeleteFunc(entries, func(e spec.AuthModule) bool { return e.Module == module })
	}
	return out
}

// WithPasswordHash returns a with hash set on every password entry. When no
// stack a service uses has a password entry, one is added to each of them
// first (which turns password sign-in on).
func WithPasswordHash(a spec.Auth, hash string) spec.Auth {
	if enabled, _ := Shape(a, ModulePassword); !enabled {
		a = Enable(a, ModulePassword)
	} else {
		a = Clone(a)
	}
	for _, entries := range a.Stacks {
		for i := range entries {
			if entries[i].Module != ModulePassword {
				continue
			}
			opts := maps.Clone(entries[i].Options)
			if opts == nil {
				opts = map[string]any{}
			}
			opts[PasswordHashOption] = hash
			entries[i].Options = opts
		}
	}
	return a
}

// PasswordEntries counts the password entries over all stacks.
func PasswordEntries(a spec.Auth) int {
	n := 0
	for _, entries := range a.Stacks {
		for _, e := range entries {
			if e.Module == ModulePassword {
				n++
			}
		}
	}
	return n
}

// Equal reports whether two auth sections mean the same (option values are
// compared by their JSON form, so 3 read from YAML equals 3 read from JSON).
func Equal(a, b spec.Auth) bool {
	if !maps.Equal(a.Services, b.Services) || len(a.Stacks) != len(b.Stacks) {
		return false
	}
	for name, entries := range a.Stacks {
		other, ok := b.Stacks[name]
		if !ok || len(other) != len(entries) {
			return false
		}
		for i := range entries {
			if entryKey(entries[i]) != entryKey(other[i]) {
				return false
			}
		}
	}
	return true
}

// entryKey is a comparable form of an entry.
func entryKey(e spec.AuthModule) string {
	b, err := json.Marshal(e)
	if err != nil {
		return e.Module + "/" + e.Control
	}
	return string(b)
}

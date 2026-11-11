package runtimes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const authTimeout = 30 * time.Second

// checkAuth asks the agent's CLI whether the user is signed in, using only read-only commands:
//
//	claude: `claude auth status` (JSON with loggedIn; exit 1 when signed out)
//	codex:  `codex login status` (exit 0 when signed in; exit 1 and "Not logged in" when not)
//	omp:    `omp models --json`, which lists only models of providers with credentials (plus Apple's on-device
//	        model, which needs none); a list with nothing else means no provider is configured
//
// Anything else, a timeout included, is AuthUnknown.
func (m *Manager) checkAuth(ctx context.Context, info kindInfo, cli string) string {
	switch info.kind {
	case "claude":
		out, code, err := m.run(ctx, authTimeout, cli, "auth", "status")
		if err != nil {
			return AuthUnknown
		}
		var st struct {
			LoggedIn *bool `json:"loggedIn"`
		}
		if json.Unmarshal([]byte(firstJSON(out)), &st) == nil && st.LoggedIn != nil {
			return authOf(*st.LoggedIn)
		}
		switch code {
		case 0:
			return AuthOK
		case 1:
			return AuthMissing
		}
	case "codex":
		out, code, err := m.run(ctx, authTimeout, cli, "login", "status")
		switch {
		case err != nil:
		case code == 0:
			return AuthOK
		case code == 1 && strings.Contains(strings.ToLower(out), "not logged in"):
			return AuthMissing
		}
	case "omp":
		out, code, err := m.run(ctx, authTimeout, cli, "models", "--json")
		if err != nil || code != 0 {
			return AuthUnknown
		}
		var list struct {
			Models []struct {
				Provider string `json:"provider"`
			} `json:"models"`
		}
		if json.Unmarshal([]byte(firstJSON(out)), &list) != nil {
			return AuthUnknown
		}
		for _, mod := range list.Models {
			if mod.Provider != "apple" {
				return AuthOK
			}
		}
		return AuthMissing
	}
	return AuthUnknown
}

func authOf(loggedIn bool) string {
	if loggedIn {
		return AuthOK
	}
	return AuthMissing
}

// firstJSON returns out from its first "{", so a banner line before the JSON does not break parsing, and cuts
// anything after the object.
func firstJSON(out string) string {
	i := strings.Index(out, "{")
	if i < 0 {
		return ""
	}
	dec := json.NewDecoder(strings.NewReader(out[i:]))
	var raw json.RawMessage
	if dec.Decode(&raw) != nil {
		return ""
	}
	return string(raw)
}

func authWarnings(info kindInfo, status string) []Warning {
	switch status {
	case AuthMissing:
		if info.kind == "omp" {
			return []Warning{{
				Code:    CodeProvidersMissing,
				Message: "omp has no model provider configured. Configure one with `omp login <provider>` (for example `omp login anthropic`), or set a provider API key.",
				Command: new(info.loginCommand),
			}}
		}
		return []Warning{{
			Code:    CodeAuthMissing,
			Message: fmt.Sprintf("You are not signed in to %s. Run `%s`.", info.title, info.loginCommand),
			Command: new(info.loginCommand),
		}}
	case AuthUnknown:
		return []Warning{{
			Code:    CodeAuthUnknown,
			Message: fmt.Sprintf("hal9001 could not tell whether you are signed in to %s. If runs fail with a login error, run `%s`.", info.title, info.loginCommand),
			Command: new(info.loginCommand),
		}}
	}
	return nil
}

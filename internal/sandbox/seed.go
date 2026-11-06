package sandbox

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// cloneTimeout bounds the seeding clone.
const cloneTimeout = 10 * time.Minute

// seed writes the run's initial workspace: an optional clone of Repo at Ref and input.json. The clone goes
// into the workdir when it is empty (or already a clone, which is fetched), else into repo/ so a shared
// persistent workdir is never overwritten. The git token is passed to the clone command only, as a
// transient config entry; it is not stored in .git/config or the session environment.
func seed(ctx context.Context, s Session, rs RunSpec) error {
	if rs.Repo != "" {
		if strings.HasPrefix(rs.Repo, "-") || strings.HasPrefix(rs.Ref, "-") {
			return fmt.Errorf("invalid repo or ref")
		}
		prefix := "GIT_TERMINAL_PROMPT=0 "
		if rs.GitToken != "" && strings.HasPrefix(rs.Repo, "https://") {
			cred := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + rs.GitToken))
			prefix += "GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=http.extraHeader GIT_CONFIG_VALUE_0=" + shQuote("Authorization: Basic "+cred) + " "
		}
		script := `set -e
if [ -d .git ]; then T=.; elif [ -z "$(ls -A . 2>/dev/null)" ]; then T=.; else T=repo; fi
if [ -d "$T/.git" ]; then ` + prefix + `git -C "$T" fetch --quiet origin; else ` + prefix + `git clone --quiet -- ` + shQuote(rs.Repo) + ` "$T"; fi
`
		if rs.Ref != "" {
			script += `git -C "$T" checkout --quiet ` + shQuote(rs.Ref) + " --\n"
		}
		res, err := s.Exec(ctx, script, nil, cloneTimeout)
		if err != nil {
			return fmt.Errorf("clone %s: %w", rs.Repo, err)
		}
		if res.ExitCode != 0 || res.TimedOut {
			msg := strings.TrimSpace(res.Stderr)
			if res.TimedOut {
				msg = "timed out"
			}
			return fmt.Errorf("clone %s: %s", rs.Repo, msg)
		}
	}
	if rs.Input != nil {
		if err := s.WriteFile(ctx, "input.json", rs.Input); err != nil {
			return fmt.Errorf("write input.json: %w", err)
		}
	}
	return nil
}

// GitHubToken returns the token of the user's `gh` login, or "" when gh is missing or not logged in. Pass it
// as RunSpec.GitToken to clone private GitHub repos.
func GitHubToken(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

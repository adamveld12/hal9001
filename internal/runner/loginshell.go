package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	pathMarker = "<<<HAL9001_PATH>>>"
	// DefaultLoginShellTimeout bounds LoginShellPATH; slow rc files should not hang install.
	DefaultLoginShellTimeout = 10 * time.Second
)

// LoginShellPATH captures PATH as the user's login shell sees it:
// `$SHELL -lic 'printf %s "$PATH"'`. An interactive shell may print banners or rc-file
// noise on stdout, so PATH is printed between markers and extracted from them.
// shell defaults to $SHELL, then /bin/zsh; timeout defaults to DefaultLoginShellTimeout.
func LoginShellPATH(ctx context.Context, shell string, timeout time.Duration) (string, error) {
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	if shell == "" {
		shell = "/bin/zsh"
	}
	if timeout <= 0 {
		timeout = DefaultLoginShellTimeout
	}
	script := fmt.Sprintf("printf '%%s%%s%%s' '%s' \"$PATH\" '%s'", pathMarker, pathMarker)
	res, err := Run(ctx, Spec{
		Lang:        LangExec,
		Command:     []string{shell, "-lic", script},
		Env:         map[string]string{},
		Timeout:     timeout,
		GracePeriod: time.Second,
		MaxOutput:   1 << 20,
		Cwd:         "/",
	})
	if err != nil {
		return "", fmt.Errorf("login shell PATH: %w", err)
	}
	if res.TimedOut {
		return "", fmt.Errorf("login shell PATH: %s timed out after %s", shell, timeout)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("login shell PATH: %s exited %d: %s", shell, res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	out := string(res.Stdout)
	_, rest, ok := strings.Cut(out, pathMarker)
	if !ok {
		return "", errors.New("login shell PATH: marker not found in shell output")
	}
	path, _, ok := strings.Cut(rest, pathMarker)
	if !ok || path == "" {
		return "", errors.New("login shell PATH: shell reported an empty PATH")
	}
	return path, nil
}

// WellKnownPATHDirs are the directories where user-level tools (mise, homebrew
// binaries, curl-installed CLIs) normally live. They are appended to a captured
// PATH when missing, because a login shell does not see directories that only an
// interactive rc file adds, and launchd gives the daemon almost nothing.
func WellKnownPATHDirs(home string) []string {
	dirs := []string{"/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}
	if home != "" {
		dirs = append([]string{
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, ".local", "share", "mise", "shims"),
		}, dirs...)
	}
	return dirs
}

// EffectivePATH returns base (the login-shell PATH, or the process PATH when the
// capture failed) followed by every existing well-known directory it lacks.
// Entries already in base keep their position, so the user's ordering wins.
func EffectivePATH(base, home string) string {
	seen := map[string]bool{}
	var out []string
	for _, d := range filepath.SplitList(base) {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	for _, d := range WellKnownPATHDirs(home) {
		if seen[d] {
			continue
		}
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return strings.Join(out, string(os.PathListSeparator))
}

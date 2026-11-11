package tailscale

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"hal9001/internal/runner"
)

// MacAppBinary is where the Mac App Store and standalone Tailscale apps ship the CLI when it is not on PATH.
const MacAppBinary = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"

const (
	// readTimeout bounds each read-only CLI call; a hung tailscaled counts as unavailable.
	readTimeout = 5 * time.Second
	// serveTimeout bounds `tailscale serve`, which can wait for the tailnet to enable Serve.
	serveTimeout = 30 * time.Second
)

// CLI is the real implementation: it runs the tailscale binary.
type CLI struct {
	// Binary is the executable to run; it overrides every lookup (tests).
	Binary string
	// Path is the PATH the executable is looked up on; empty means this process's PATH.
	Path string
	// Exact searches Path only: the well-known directories (Homebrew, ~/.local/bin, ...) and the macOS app bundle are
	// not tried. For tests and the sandbox, so they can never reach the Tailscale of the machine they run on.
	Exact bool
	// UserHome is the home directory for the well-known directories; default: the current user's.
	UserHome string
}

var _ Client = CLI{}

// Locate returns the tailscale executable: first on the PATH (with the well-known directories added unless Exact),
// then the macOS app bundle (unless Exact). It reports an error wrapping ErrUnavailable when there is none.
func (c CLI) Locate() (string, error) {
	if c.Binary != "" {
		return c.Binary, nil
	}
	path := c.Path
	if path == "" && !c.Exact {
		path = os.Getenv("PATH")
	}
	if !c.Exact {
		home := c.UserHome
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		path = runner.EffectivePATH(path, home)
	}
	if p, err := runner.LookPath("tailscale", path); err == nil {
		return p, nil
	}
	if !c.Exact {
		if st, err := os.Stat(MacAppBinary); err == nil && !st.IsDir() {
			return MacAppBinary, nil
		}
	}
	return "", fmt.Errorf("%w: tailscale CLI not found: %w", ErrUnavailable, exec.ErrNotFound)
}

// CommandError is a tailscale command that ran and failed.
type CommandError struct {
	Args   []string
	Output string
	Err    error
}

func (e *CommandError) Error() string {
	msg := fmt.Sprintf("tailscale %s: %v", strings.Join(e.Args, " "), e.Err)
	if e.Output != "" {
		msg += ": " + e.Output
	}
	return msg
}

func (e *CommandError) Unwrap() error { return e.Err }

// run executes the CLI with args and returns its stdout. Stderr (which carries the client/server version-mismatch
// warning) is kept apart so it never ends up in the JSON; it is part of the error.
func (c CLI) run(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	bin, err := c.Locate()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // bin is the tailscale CLI; args are fixed or validated
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(strings.TrimSpace(stderr.String()) + "\n" + strings.TrimSpace(stdout.String()))
		switch {
		case errors.Is(err, exec.ErrNotFound), errors.Is(err, os.ErrNotExist):
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return nil, &CommandError{Args: args, Output: msg, Err: context.DeadlineExceeded}
		case daemonDown(msg):
			return nil, fmt.Errorf("%w: %s", ErrUnavailable, firstLine(stderr.String()))
		}
		return nil, &CommandError{Args: args, Output: msg, Err: err}
	}
	return stdout.Bytes(), nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// daemonDown recognises the CLI's messages for "tailscaled is not reachable".
func daemonDown(out string) bool {
	l := strings.ToLower(out)
	for _, s := range []string{
		"failed to connect to local tailscaled",
		"failed to connect to local tailscale service",
		"tailscaled is not running",
		"tailscale is stopped",
		"is tailscale running",
		"is tailscaled running",
		"connect: no such file or directory",
		"connect: connection refused",
	} {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

// Whois implements Client.
func (c CLI) Whois(ctx context.Context, ip netip.Addr) (Whois, error) {
	out, err := c.run(ctx, readTimeout, "whois", "--json", ip.String())
	if err != nil {
		return Whois{}, err
	}
	return ParseWhois(out)
}

// Status implements Client.
func (c CLI) Status(ctx context.Context) (Status, error) {
	out, err := c.run(ctx, readTimeout, "status", "--json")
	if err != nil {
		return Status{}, err
	}
	return ParseStatus(out)
}

// ServeStatus implements Client.
func (c CLI) ServeStatus(ctx context.Context) (ServeConfig, error) {
	out, err := c.run(ctx, readTimeout, "serve", "status", "--json")
	if err != nil {
		return ServeConfig{}, err
	}
	return ParseServeConfig(out)
}

var versionRE = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?(?:[-+][0-9A-Za-z.]+)?`)

// Version is the version `tailscale version` reports, or "".
func (c CLI) Version(ctx context.Context) (string, error) {
	out, err := c.run(ctx, readTimeout, "version")
	if err != nil {
		return "", err
	}
	return versionRE.FindString(string(out)), nil
}

// ServeArgs is the argv (without the program) that shares the daemon on the tailnet: tailnet only, in the
// background so it survives restarts, without prompts.
func ServeArgs(httpsPort, halPort int) []string {
	return []string{"serve", "--bg", "--yes", "--https=" + strconv.Itoa(httpsPort), ServeTarget(halPort)}
}

// ServeOffArgs is the argv (without the program) that removes the mapping on an HTTPS port.
func ServeOffArgs(httpsPort int) []string {
	return []string{"serve", "--https=" + strconv.Itoa(httpsPort), "off"}
}

// ServeTarget is the loopback address of the daemon that `tailscale serve` proxies to.
func ServeTarget(halPort int) string { return "http://127.0.0.1:" + strconv.Itoa(halPort) }

// Serve runs `tailscale serve` for ServeArgs.
func (c CLI) Serve(ctx context.Context, httpsPort, halPort int) error {
	_, err := c.run(ctx, serveTimeout, ServeArgs(httpsPort, halPort)...)
	return err
}

// ServeOff runs `tailscale serve --https=<port> off`.
func (c CLI) ServeOff(ctx context.Context, httpsPort int) error {
	_, err := c.run(ctx, serveTimeout, ServeOffArgs(httpsPort)...)
	return err
}

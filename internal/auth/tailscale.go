package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ErrTailscaleUnavailable means the tailscale CLI is missing or tailscaled is
// not running or not reachable. tailscale-identity ignores such requests.
var ErrTailscaleUnavailable = errors.New("tailscale unavailable")

// Whois is the part of `tailscale whois --json <ip>` we use. Node.Tags is
// non-empty for tagged devices, which have no user.
type Whois struct {
	Node struct {
		Name string   `json:"Name"`
		Tags []string `json:"Tags"`
	} `json:"Node"`
	UserProfile *struct {
		LoginName   string `json:"LoginName"`
		DisplayName string `json:"DisplayName"`
	} `json:"UserProfile"`
}

// Login is the whois user login, or "" when the node has no user.
func (w Whois) Login() string {
	if w.UserProfile == nil {
		return ""
	}
	return w.UserProfile.LoginName
}

// TailscaleStatus is the part of `tailscale status --json` we use.
type TailscaleStatus struct {
	BackendState string `json:"BackendState"`
	Self         struct {
		HostName string `json:"HostName"`
		DNSName  string `json:"DNSName"`
		UserID   int64  `json:"UserID"`
	} `json:"Self"`
	User map[string]struct {
		LoginName   string `json:"LoginName"`
		DisplayName string `json:"DisplayName"`
	} `json:"User"`
}

// OwnerLogin is the login that owns this machine (Self.UserID looked up in
// the User map), or "".
func (s TailscaleStatus) OwnerLogin() string {
	return s.User[strconv.FormatInt(s.Self.UserID, 10)].LoginName
}

// Hostname is this machine's tailnet DNS name without the trailing dot.
func (s TailscaleStatus) Hostname() string { return strings.TrimSuffix(s.Self.DNSName, ".") }

// ServeConfig is the part of `tailscale serve status --json` we use.
type ServeConfig struct {
	TCP map[string]struct {
		HTTPS        bool   `json:"HTTPS"`
		TCPForward   string `json:"TCPForward"`
		TerminateTLS string `json:"TerminateTLS"`
	} `json:"TCP"`
	Web map[string]struct {
		Handlers map[string]struct {
			Proxy string `json:"Proxy"`
		} `json:"Handlers"`
	} `json:"Web"`
	AllowFunnel map[string]bool `json:"AllowFunnel"`
}

// Tailscale abstracts the tailscale CLI so tests can fake it.
type Tailscale interface {
	// Whois looks up the tailnet node and user for ip.
	Whois(ctx context.Context, ip netip.Addr) (Whois, error)
	Status(ctx context.Context) (TailscaleStatus, error)
	// ServeStatus returns the serve configuration (empty when none).
	ServeStatus(ctx context.Context) (ServeConfig, error)
}

// macAppBinary is where the Mac App Store / standalone app ships the CLI when
// it is not on PATH.
const macAppBinary = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"

// cliTimeout bounds each CLI call; a hung tailscaled counts as unavailable.
const cliTimeout = 5 * time.Second

// CLI is the real Tailscale implementation: it shells out to the tailscale
// binary.
type CLI struct {
	// Binary overrides binary lookup (tests). Empty means `tailscale` on PATH,
	// then the macOS app bundle.
	Binary string
}

var _ Tailscale = CLI{}

func (c CLI) binary() (string, error) {
	if c.Binary != "" {
		return c.Binary, nil
	}
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p, nil
	}
	if st, err := os.Stat(macAppBinary); err == nil && !st.IsDir() {
		return macAppBinary, nil
	}
	return "", fmt.Errorf("%w: tailscale CLI not found: %w", ErrTailscaleUnavailable, exec.ErrNotFound)
}

func (c CLI) run(ctx context.Context, args ...string) ([]byte, error) {
	bin, err := c.binary()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, cliTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // bin is the tailscale CLI; args are fixed except a validated IP
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		switch {
		case errors.Is(err, exec.ErrNotFound), errors.Is(err, os.ErrNotExist):
			return nil, fmt.Errorf("%w: %w", ErrTailscaleUnavailable, err)
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return nil, fmt.Errorf("%w: tailscale %s timed out", ErrTailscaleUnavailable, args[0])
		case daemonDown(msg):
			return nil, fmt.Errorf("%w: %s", ErrTailscaleUnavailable, msg)
		}
		return nil, fmt.Errorf("tailscale %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return stdout.Bytes(), nil
}

// daemonDown recognises the CLI's messages for "tailscaled is not reachable".
func daemonDown(stderr string) bool {
	l := strings.ToLower(stderr)
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

func (c CLI) Whois(ctx context.Context, ip netip.Addr) (Whois, error) {
	out, err := c.run(ctx, "whois", "--json", ip.String())
	if err != nil {
		return Whois{}, err
	}
	return ParseWhois(out)
}

func (c CLI) Status(ctx context.Context) (TailscaleStatus, error) {
	out, err := c.run(ctx, "status", "--json")
	if err != nil {
		return TailscaleStatus{}, err
	}
	return ParseStatus(out)
}

func (c CLI) ServeStatus(ctx context.Context) (ServeConfig, error) {
	out, err := c.run(ctx, "serve", "status", "--json")
	if err != nil {
		return ServeConfig{}, err
	}
	return ParseServeConfig(out)
}

// ParseWhois parses `tailscale whois --json` output.
func ParseWhois(data []byte) (Whois, error) {
	var w Whois
	if err := json.Unmarshal(data, &w); err != nil {
		return Whois{}, fmt.Errorf("parse tailscale whois: %w", err)
	}
	return w, nil
}

// ParseStatus parses `tailscale status --json` output.
func ParseStatus(data []byte) (TailscaleStatus, error) {
	var s TailscaleStatus
	if err := json.Unmarshal(data, &s); err != nil {
		return TailscaleStatus{}, fmt.Errorf("parse tailscale status: %w", err)
	}
	return s, nil
}

// ParseServeConfig parses `tailscale serve status --json` output. Empty
// output (nothing served) is an empty config.
func ParseServeConfig(data []byte) (ServeConfig, error) {
	var c ServeConfig
	if len(bytes.TrimSpace(data)) == 0 {
		return c, nil
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return ServeConfig{}, fmt.Errorf("parse tailscale serve status: %w", err)
	}
	return c, nil
}

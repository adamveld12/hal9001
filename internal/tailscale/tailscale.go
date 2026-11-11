// Package tailscale talks to the tailscale CLI: detection (installed, signed in, MagicDNS name, HTTPS certificates,
// the current `tailscale serve` mapping), the commands hal9001 runs to share itself on the tailnet, and keeping
// `allowed_hosts` in config.yaml in line with them (docs/auth.md). It never uses `tailscale funnel`.
package tailscale

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// ErrUnavailable means the tailscale CLI is missing or tailscaled is not running or not reachable.
// tailscale-identity ignores such requests.
var ErrUnavailable = errors.New("tailscale unavailable")

// Whois is the part of `tailscale whois --json <ip>` we use. Node.Tags is non-empty for tagged devices, which have no
// user.
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

// Status is the part of `tailscale status --json` we use.
type Status struct {
	Version      string `json:"Version"`
	BackendState string `json:"BackendState"`
	Self         struct {
		HostName string `json:"HostName"`
		DNSName  string `json:"DNSName"`
		UserID   int64  `json:"UserID"`
	} `json:"Self"`
	CurrentTailnet *struct {
		Name            string `json:"Name"`
		MagicDNSSuffix  string `json:"MagicDNSSuffix"`
		MagicDNSEnabled bool   `json:"MagicDNSEnabled"`
	} `json:"CurrentTailnet"`
	// CertDomains are the names the tailnet can issue HTTPS certificates for; empty when HTTPS is not enabled.
	CertDomains []string `json:"CertDomains"`
	User        map[string]struct {
		LoginName   string `json:"LoginName"`
		DisplayName string `json:"DisplayName"`
	} `json:"User"`
}

// OwnerLogin is the login that owns this machine (Self.UserID looked up in the User map), or "".
func (s Status) OwnerLogin() string {
	return s.User[strconv.FormatInt(s.Self.UserID, 10)].LoginName
}

// Hostname is this machine's tailnet DNS name without the trailing dot.
func (s Status) Hostname() string { return strings.TrimSuffix(s.Self.DNSName, ".") }

// TailnetName is the name of the tailnet, or "".
func (s Status) TailnetName() string {
	if s.CurrentTailnet == nil {
		return ""
	}
	return s.CurrentTailnet.Name
}

// MagicDNSEnabled reports whether the tailnet has MagicDNS on.
func (s Status) MagicDNSEnabled() bool {
	return s.CurrentTailnet != nil && s.CurrentTailnet.MagicDNSEnabled
}

// HTTPSEnabled reports whether the tailnet can issue a certificate for this machine's name, which `tailscale serve`
// over HTTPS needs.
func (s Status) HTTPSEnabled() bool {
	name := s.Hostname()
	if name == "" {
		return false
	}
	for _, d := range s.CertDomains {
		if strings.EqualFold(d, name) {
			return true
		}
	}
	return false
}

// ServeConfig is the part of `tailscale serve status --json` we use.
type ServeConfig struct {
	TCP map[string]struct {
		HTTPS        bool   `json:"HTTPS"`
		TCPForward   string `json:"TCPForward"`
		TerminateTLS string `json:"TerminateTLS"`
	} `json:"TCP"`
	Web map[string]struct {
		Handlers map[string]Handler `json:"Handlers"`
	} `json:"Web"`
	AllowFunnel map[string]bool `json:"AllowFunnel"`
}

// Handler is one serve web handler: a proxy, a path (file or directory), text or a redirect.
type Handler struct {
	Proxy    string `json:"Proxy"`
	Path     string `json:"Path"`
	Text     string `json:"Text"`
	Redirect string `json:"Redirect"`
}

// Funnel reports whether anything is exposed to the internet through funnel.
func (c ServeConfig) Funnel() bool {
	for _, on := range c.AllowFunnel {
		if on {
			return true
		}
	}
	return false
}

// FunnelOnPort reports whether funnel exposes the HTTPS port httpsPort to the internet (AllowFunnel is keyed by
// "<host>:<port>").
func (c ServeConfig) FunnelOnPort(httpsPort int) bool {
	suffix := ":" + strconv.Itoa(httpsPort)
	for key, on := range c.AllowFunnel {
		if on && strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return false
}

// Mapping is what `tailscale serve` has on one HTTPS port.
type Mapping struct {
	// Active: the port has a mapping, to anything.
	Active bool
	// PointsAtHal: the root handler proxies to the daemon's loopback port.
	PointsAtHal bool
	// Other describes what the port serves when it is not the daemon; "" otherwise.
	Other string
	// Funnel: funnel exposes the port to the public internet.
	Funnel bool
}

// MappingFor returns the mapping on the HTTPS port httpsPort and whether it proxies to the daemon on halPort.
func (c ServeConfig) MappingFor(httpsPort, halPort int) Mapping {
	suffix := ":" + strconv.Itoa(httpsPort)
	m := Mapping{Funnel: c.FunnelOnPort(httpsPort)}
	for key, web := range c.Web {
		if !strings.HasSuffix(key, suffix) {
			continue
		}
		m.Active = true
		root, ok := web.Handlers["/"]
		switch {
		case ok && proxyTargetsPort(root.Proxy, halPort):
			m.PointsAtHal = true
		case ok:
			m.Other = describeHandler(root)
		default:
			m.Other = "a handler for another path"
		}
		if m.PointsAtHal {
			break
		}
	}
	if !m.Active {
		if t, ok := c.TCP[strconv.Itoa(httpsPort)]; ok {
			m.Active = true
			if t.TCPForward != "" {
				m.Other = "a TCP forward to " + t.TCPForward
			} else {
				m.Other = "another service"
			}
		}
	}
	if m.PointsAtHal {
		m.Other = ""
	}
	return m
}

// ProxiesTo reports whether any web handler or TCP forward targets a loopback address on port.
func (c ServeConfig) ProxiesTo(port int) bool {
	for _, web := range c.Web {
		for _, h := range web.Handlers {
			if proxyTargetsPort(h.Proxy, port) {
				return true
			}
		}
	}
	for _, t := range c.TCP {
		if t.TCPForward != "" && proxyTargetsPort("tcp://"+t.TCPForward, port) {
			return true
		}
	}
	return false
}

func describeHandler(h Handler) string {
	switch {
	case h.Proxy != "":
		return h.Proxy
	case h.Path != "":
		return "files from " + h.Path
	case h.Redirect != "":
		return "a redirect to " + h.Redirect
	case h.Text != "":
		return "a text response"
	}
	return "another service"
}

func proxyTargetsPort(target string, port int) bool {
	if target == "" {
		return false
	}
	if !strings.Contains(target, "://") {
		target = "http://" + target
	}
	u, err := url.Parse(target)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
	default:
		return false
	}
	return u.Port() == strconv.Itoa(port)
}

// Client is the read side of the CLI, abstracted so tests can fake it (the auth module uses it).
type Client interface {
	// Whois looks up the tailnet node and user for ip.
	Whois(ctx context.Context, ip netip.Addr) (Whois, error)
	Status(ctx context.Context) (Status, error)
	// ServeStatus returns the serve configuration (empty when none).
	ServeStatus(ctx context.Context) (ServeConfig, error)
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
func ParseStatus(data []byte) (Status, error) {
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		return Status{}, fmt.Errorf("parse tailscale status: %w", err)
	}
	return s, nil
}

// ParseServeConfig parses `tailscale serve status --json` output. Empty output (nothing served) is an empty config.
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

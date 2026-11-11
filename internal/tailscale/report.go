package tailscale

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

// Warning codes (api/openapi.yaml TailscaleWarning).
const (
	WarnCLINotFound       = "cli_not_found"
	WarnDaemonUnreachable = "daemon_unreachable"
	WarnLoggedOut         = "logged_out"
	WarnStopped           = "stopped"
	WarnNeedsMachineAuth  = "needs_machine_auth"
	WarnHTTPSDisabled     = "https_disabled"
	WarnMagicDNSDisabled  = "magicdns_disabled"
	WarnFunnelEnabled     = "funnel_enabled"
	WarnPortConflict      = "port_conflict"
	WarnServeMissing      = "serve_missing"
	WarnHostMissing       = "host_missing"
	WarnHostRenamed       = "host_renamed"
	WarnReapplyFailed     = "reapply_failed"
)

const (
	urlDownload = "https://tailscale.com/download"
	urlDNSAdmin = "https://login.tailscale.com/admin/dns"
	urlMachines = "https://login.tailscale.com/admin/machines"
)

// Warning is a problem with its fix.
type Warning struct {
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Command *string `json:"command"`
	URL     *string `json:"url"`
}

// CLIInfo is the tailscale executable hal9001 found.
type CLIInfo struct {
	Found   bool    `json:"found"`
	Path    *string `json:"path"`
	Version *string `json:"version"`
}

// ServeInfo is the `tailscale serve` side of the report.
type ServeInfo struct {
	Enabled     bool    `json:"enabled"`
	HTTPSPort   int     `json:"https_port"`
	Target      string  `json:"target"`
	Active      bool    `json:"active"`
	PointsAtHal bool    `json:"points_at_hal"`
	Conflict    *string `json:"conflict"`
	Funnel      bool    `json:"funnel"`
}

// AllowedHostInfo says whether the URL's host is in `allowed_hosts`.
type AllowedHostInfo struct {
	Entry      *string `json:"entry"`
	Present    bool    `json:"present"`
	AddedByHal bool    `json:"added_by_hal"`
}

// Report is the answer of GET /api/tailscale (api/openapi.yaml TailscaleStatus).
type Report struct {
	CLI             CLIInfo         `json:"cli"`
	BackendState    *string         `json:"backend_state"`
	LoggedIn        bool            `json:"logged_in"`
	DNSName         *string         `json:"dns_name"`
	Tailnet         *string         `json:"tailnet"`
	OwnerLogin      *string         `json:"owner_login"`
	HTTPSEnabled    bool            `json:"https_enabled"`
	MagicDNSEnabled bool            `json:"magic_dns_enabled"`
	Serve           ServeInfo       `json:"serve"`
	URL             *string         `json:"url"`
	AllowedHost     AllowedHostInfo `json:"allowed_host"`
	AllowedLogins   []string        `json:"allowed_logins"`
	Warnings        []Warning       `json:"warnings"`
	Error           *string         `json:"error"`
	CheckedAt       time.Time       `json:"checked_at"`
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// HostEntry is the `allowed_hosts` entry that matches https://<name>[:port]/: the Host header carries the port only
// when it is not 443.
func HostEntry(name string, httpsPort int) string {
	if httpsPort == 443 {
		return name
	}
	return name + ":" + strconv.Itoa(httpsPort)
}

// URLFor is where the daemon answers on the tailnet when shared on httpsPort.
func URLFor(name string, httpsPort int) string { return "https://" + HostEntry(name, httpsPort) + "/" }

func hasHost(hosts []string, entry string) bool {
	return slices.ContainsFunc(hosts, func(h string) bool { return strings.EqualFold(strings.TrimSpace(h), entry) })
}

func warn(code, msg, command, url string) Warning {
	return Warning{Code: code, Message: msg, Command: ptr(command), URL: ptr(url)}
}

// serveCommand is the command that shares the daemon, for display.
func serveCommand(httpsPort, halPort int) string {
	return "tailscale " + strings.Join(ServeArgs(httpsPort, halPort), " ")
}

// readiness lists the warnings that stop `tailscale serve` from working: the CLI, the sign-in and the HTTPS
// certificates. They are part of the report; Enable refuses with them.
func (s *snapshot) readiness() []Warning {
	switch {
	case !s.cliFound:
		return []Warning{warn(WarnCLINotFound, "The tailscale CLI was not found. Install Tailscale (the app or `brew install tailscale`) and sign in.", "", urlDownload)}
	case s.stErr != nil:
		return []Warning{warn(WarnDaemonUnreachable, "Tailscale is installed but not running or not reachable. Start the Tailscale app (or tailscaled).", "", "")}
	}
	st := s.st
	var ws []Warning
	switch st.BackendState {
	case "Running":
	case "NeedsLogin":
		ws = append(ws, warn(WarnLoggedOut, "Tailscale is signed out on this Mac.", "tailscale up", ""))
	case "NeedsMachineAuth":
		ws = append(ws, warn(WarnNeedsMachineAuth, "This Mac must be approved by an admin of the tailnet before it can be reached.", "", urlMachines))
	default:
		ws = append(ws, warn(WarnStopped, "Tailscale is "+describeState(st.BackendState)+". Connect it in the Tailscale app or run `tailscale up`.", "tailscale up", ""))
	}
	if len(ws) > 0 {
		return ws
	}
	switch {
	case !st.MagicDNSEnabled() || st.Hostname() == "":
		ws = append(ws, warn(WarnMagicDNSDisabled, "MagicDNS is off for this tailnet. Turn on MagicDNS and HTTPS certificates under DNS in the Tailscale admin console.", "", urlDNSAdmin))
	case !st.HTTPSEnabled():
		ws = append(ws, warn(WarnHTTPSDisabled, "HTTPS certificates are not enabled for this tailnet, which `tailscale serve` over HTTPS needs. Enable them under DNS in the Tailscale admin console.", "", urlDNSAdmin))
	}
	return ws
}

func describeState(s string) string {
	if s == "" {
		return "not connected"
	}
	return "not connected (" + s + ")"
}

package httpsec

import (
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// DefaultPort is the only port the daemon listens on (REQUIREMENTS §10.3).
const DefaultPort = 9001

// Options configures the baseline middleware.
type Options struct {
	// Port is the loopback port used to build the built-in allowed hosts.
	// Zero means DefaultPort.
	Port int
	// AllowedHosts returns the configured `allowed_hosts` (e.g. the Tailscale
	// hostname). It is called per request so config reloads take effect.
	// Entries are compared with the Host header exactly, case-insensitively.
	AllowedHosts func() []string
}

// FunnelHeader is added by tailscaled to every request that arrives through `tailscale funnel` (the public
// internet). hal9001 only shares on the tailnet, so such requests are always refused.
const FunnelHeader = "Tailscale-Funnel-Request"

// Middleware returns the baseline middleware. Checks run in this order and the
// first failure short-circuits with a problem+json response:
//
//  0. A request that came through `tailscale funnel` (FunnelHeader present,
//     any value) is refused with 403 before anything else, whatever its Host.
//  1. Host must be localhost:<port>, 127.0.0.1:<port> or an allowed host.
//  2. An Origin header, when present, must be an allowed origin: http for the
//     loopback hosts, https for other allowed hosts.
//  3. POST, PUT, PATCH and DELETE requests must be application/json.
//
// No CORS headers are ever set, so cross-origin preflights cannot succeed.
func Middleware(opts Options) func(http.Handler) http.Handler {
	port := opts.Port
	if port == 0 {
		port = DefaultPort
	}
	builtin := []string{
		"localhost:" + strconv.Itoa(port),
		"127.0.0.1:" + strconv.Itoa(port),
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(r.Header.Values(FunnelHeader)) > 0 {
				WriteProblem(w, Problem{
					Type:   TypeForbidden,
					Title:  "Funnel requests refused",
					Status: http.StatusForbidden,
					Detail: "this request came through tailscale funnel (the public internet); hal9001 only serves devices on your tailnet",
				})
				return
			}
			hosts := allowedHosts(builtin, opts.AllowedHosts)
			host := strings.ToLower(r.Host)
			if !hosts[host] {
				WriteProblem(w, Problem{
					Type:   TypeHostNotAllowed,
					Title:  "Host not allowed",
					Status: http.StatusForbidden,
					Detail: fmt.Sprintf("Host %q is not in the allow-list", r.Host),
				})
				return
			}
			if origin := r.Header.Values("Origin"); len(origin) > 0 && !originAllowed(origin, hosts) {
				WriteProblem(w, Problem{
					Type:   TypeOriginNotAllowed,
					Title:  "Origin not allowed",
					Status: http.StatusForbidden,
					Detail: fmt.Sprintf("Origin %q is not allowed", strings.Join(origin, ", ")),
				})
				return
			}
			if isMutating(r.Method) && !isJSON(r.Header.Get("Content-Type")) {
				WriteProblem(w, Problem{
					Type:   TypeUnsupportedMedia,
					Title:  "Content-Type must be application/json",
					Status: http.StatusUnsupportedMediaType,
					Detail: fmt.Sprintf("%s requests must send Content-Type: application/json", r.Method),
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func allowedHosts(builtin []string, extra func() []string) map[string]bool {
	set := make(map[string]bool, len(builtin)+4)
	for _, h := range builtin {
		set[h] = true
	}
	if extra != nil {
		for _, h := range extra() {
			if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
				set[h] = true
			}
		}
	}
	return set
}

// originAllowed requires exactly one Origin value that is scheme://host with an
// allowed host and the scheme that host is served with.
func originAllowed(values []string, hosts map[string]bool) bool {
	if len(values) != 1 {
		return false
	}
	u, err := url.Parse(values[0])
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	host := strings.ToLower(u.Host)
	if !hosts[host] {
		return false
	}
	if isLoopbackHost(host) {
		return u.Scheme == "http"
	}
	return u.Scheme == "https"
}

func isLoopbackHost(hostport string) bool {
	h := hostport
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.HasSuffix(h, "]") {
		h = h[:i]
	}
	return h == "localhost" || h == "127.0.0.1" || h == "[::1]"
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func isJSON(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	return err == nil && mt == "application/json"
}

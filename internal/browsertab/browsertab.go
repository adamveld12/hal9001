// Package browsertab reuses an open hal9001 browser tab instead of opening a new one. The menubar app calls Opener.Open
// for every deep link it shows; when a supported browser already has a tab on the hal9001 UI, that tab is pointed at the
// link and brought to the front. Everything else (browser not running, no such tab, permission denied, a script error,
// a timeout, a link that is not on the UI) falls back to the plain open, so reuse can only make a click better.
package browsertab

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"time"
)

// Timeout bounds one attempt to reuse a tab, across every step and browser.
const Timeout = 2 * time.Second

// ErrNotRunning means the browser is not running. Nothing was sent to it, so it was not launched.
var ErrNotRunning = errors.New("browser is not running")

// ErrNoTab means the browser has no tab on the hal9001 UI.
var ErrNoTab = errors.New("no hal9001 tab open")

// Tab is one open browser tab.
type Tab struct {
	Window    string // browser-specific window id
	ID        string // browser-specific tab id
	Active    bool   // the tab its window shows
	Frontmost bool   // its window is the browser's frontmost window
	URL       string
}

// Browser is a browser whose tabs can be listed and navigated. Only Chrome implements it; another browser needs the
// same four operations.
type Browser interface {
	Name() string
	// Running reports whether the browser is running. It must not launch it.
	Running() bool
	// Tabs lists every open tab, frontmost window first. Only called while Running.
	Tabs(ctx context.Context) ([]Tab, error)
	// Show loads url in tab, makes it the active tab of its window, and brings that window to the front.
	Show(ctx context.Context, t Tab, url string) error
}

// Opener opens URLs, reusing a tab of one of Browsers for links on the UI.
type Opener struct {
	// UI is the hal9001 UI base URL. A link is only reused for when it is on this site (see SameSite).
	UI       string
	Browsers []Browser
	// Fallback opens the URL the normal way (the system's default handler).
	Fallback func(url string) error
}

// Open reuses a tab for u when it can and calls Fallback otherwise. It returns Fallback's error only.
func (o Opener) Open(u string) error {
	if !SameSite(u, o.UI) {
		return o.Fallback(u)
	}
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	for _, b := range o.Browsers {
		err := reuse(ctx, b, u)
		if err == nil {
			slog.Debug("reused browser tab", "browser", b.Name(), "url", u)
			return nil
		}
		if errors.Is(err, ErrNotRunning) || errors.Is(err, ErrNoTab) {
			slog.Debug("not reusing a tab", "browser", b.Name(), "reason", err)
		} else {
			slog.Info("could not reuse a tab; opening normally", "browser", b.Name(), "err", err)
		}
	}
	return o.Fallback(u)
}

func reuse(ctx context.Context, b Browser, u string) error {
	if !b.Running() {
		return ErrNotRunning
	}
	tabs, err := b.Tabs(ctx)
	if err != nil {
		return err
	}
	t, ok := pick(tabs, u)
	if !ok {
		return ErrNoTab
	}
	return b.Show(ctx, t, u)
}

// pick chooses the tab to reuse among those on the same site as u: the active tab of the frontmost window, otherwise
// the first match (tabs are ordered frontmost window first).
func pick(tabs []Tab, u string) (Tab, bool) {
	var first Tab
	found := false
	for _, t := range tabs {
		if !SameSite(t.URL, u) {
			continue
		}
		if t.Frontmost && t.Active {
			return t, true
		}
		if !found {
			first, found = t, true
		}
	}
	return first, found
}

// SameSite reports whether a and b are http(s) URLs with the same scheme, host and port. A substring is never enough:
// http://localhost:9001 is not http://localhost:90010, nor http://localhost:9001.evil.test. The loopback names
// localhost, 127.0.0.1 and ::1 count as one host, because the daemon builds links on localhost while the app reaches
// the daemon on 127.0.0.1.
func SameSite(a, b string) bool {
	sa, ha, pa, ok := site(a)
	if !ok {
		return false
	}
	sb, hb, pb, ok := site(b)
	if !ok {
		return false
	}
	if sa != sb || pa != pb {
		return false
	}
	return ha == hb || (loopback(ha) && loopback(hb))
}

func site(raw string) (scheme, host, port string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return "", "", "", false
	}
	scheme = strings.ToLower(u.Scheme)
	var def string
	switch scheme {
	case "http":
		def = "80"
	case "https":
		def = "443"
	default:
		return "", "", "", false
	}
	port = u.Port()
	if port == "" {
		port = def
	}
	return scheme, strings.ToLower(u.Hostname()), port, true
}

func loopback(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

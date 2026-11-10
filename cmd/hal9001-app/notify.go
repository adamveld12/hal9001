//go:build darwin

package main

import (
	"context"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"hal9001/internal/menubar"
)

// settingsMaxAge bounds how often the notification settings are read: they change only when the user visits System
// Settings, but the menu refreshes on every daemon event.
const settingsMaxAge = 3 * time.Second

// notifications is the native notification side of the app. It does nothing outside an app bundle (the notification
// center needs one), so a dev run from source never registers as the daemon's sink and the daemon keeps using
// terminal-notifier.
type notifications struct {
	a       *app
	enabled bool

	mu        sync.Mutex
	settings  menubar.NotifySettings
	readAt    time.Time
	sinkOn    bool // what the stream connection was last told
	requested bool // the authorization prompt was asked for in this session
	hint      bool // the Alerts-style hint is shown in the menu
	tracker   menubar.Tracker
}

// start installs the delegate and categories, and asks for permission the first time (the system shows its prompt
// only once; the app is an agent and stays running while it waits for the answer).
func (n *notifications) start() {
	if !n.a.profile.Bundled() {
		return
	}
	setNotifyHandlers(n.handle, n.authAnswered)
	if err := notifySetup(menubar.Categories()); err != nil {
		slog.Error("notifications unavailable", "err", err)
		return
	}
	n.enabled = true
	n.update(true)
}

// update re-reads the settings (at most every settingsMaxAge unless force) and, when that changes whether the app can
// be the daemon's sink, reconnects the stream with or without the sink registration.
func (n *notifications) update(force bool) {
	if !n.enabled {
		return
	}
	n.mu.Lock()
	if !force && time.Since(n.readAt) < settingsMaxAge {
		n.mu.Unlock()
		return
	}
	n.mu.Unlock()
	s := notifySettings()

	n.mu.Lock()
	prev := n.settings
	n.settings, n.readAt = s, time.Now()
	ask := s.Pending() && !n.requested
	if ask {
		n.requested = true
	}
	changed := s.Sink() != n.sinkOn
	n.mu.Unlock()
	if force || s != prev {
		slog.Info("notification settings", "auth", int(s.Auth), "style", int(s.Style), "alert", s.AlertEnabled, "center", s.CenterEnabled, "sink", s.Sink())
	}
	if ask {
		slog.Info("asking for notification permission")
		notifyRequestAuth()
	}
	if changed {
		slog.Info("notification sink changed", "sink", s.Sink())
		n.a.restartStream()
	}
}

// sink reports whether the stream should register as the daemon's notification sink, and records that it did.
func (n *notifications) sink() bool {
	if n == nil || !n.enabled {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sinkOn = n.settings.Sink()
	return n.sinkOn
}

// authAnswered runs when the user answered the authorization prompt.
func (n *notifications) authAnswered(granted bool, errMsg string) {
	slog.Info("notification permission answered", "granted", granted, "error", errMsg)
	n.update(true)
	n.a.poke()
}

// menuState adds the notification parts of the menu state.
func (n *notifications) menuState(st *menubar.ServiceState) {
	if n == nil || !n.enabled {
		return
	}
	n.update(false)
	n.mu.Lock()
	defer n.mu.Unlock()
	st.NotificationsOff = n.settings.Off()
	st.AlertStyleHint = n.hint
}

// onEvent shows a notification.created event that is routed to the app.
func (n *notifications) onEvent(ev menubar.Event) {
	note, err := ev.Notification()
	if err != nil {
		slog.Warn("unreadable notification event", "err", err)
		return
	}
	req, ok := menubar.PlanNotification(note)
	if !ok {
		return
	}
	n.post(req)
	n.tracker.Track(note)
	n.mu.Lock()
	s := n.settings
	n.mu.Unlock()
	if s.HintAlertStyle(note) {
		n.showHintOnce()
	}
}

func (n *notifications) post(r menubar.Request) {
	if err := notifyPost(r); err != nil {
		slog.Error("cannot post notification", "id", r.ID, "err", err)
	}
}

// hintMarker records that the Alerts-style hint was shown, so it appears once.
func (n *notifications) hintMarker() string {
	return filepath.Join(n.a.userHome, "Library", "Application Support", n.a.profile.BundleID, "alert-style-hint-shown")
}

func (n *notifications) showHintOnce() {
	if _, err := os.Stat(n.hintMarker()); err == nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(n.hintMarker()), 0o750); err == nil {
		_ = os.WriteFile(n.hintMarker(), nil, 0o600)
	}
	n.mu.Lock()
	n.hint = true
	n.mu.Unlock()
	n.a.poke()
}

// handle carries out the user's response to a notification.
func (n *notifications) handle(act menubar.NotificationAction) {
	h := menubar.ActionHandler{Client: n.a.client, Open: openURL, Fail: n.post}
	ctx, cancel := context.WithTimeout(n.a.ctx, 30*time.Second)
	defer cancel()
	if err := h.Handle(ctx, act); err != nil {
		slog.Error("notification action failed", "action", act.Action, "request", act.RequestID, "err", err)
		return
	}
	slog.Info("notification action", "action", act.Action, "request", act.RequestID)
	n.a.poke()
}

// openSettings opens the app's pane in System Settings > Notifications and dismisses the hint.
func (n *notifications) openSettings() {
	n.mu.Lock()
	n.hint = false
	n.mu.Unlock()
	u := "x-apple.systempreferences:com.apple.Notifications-Settings.extension?id=" + url.QueryEscape(n.a.profile.BundleID)
	if err := openURL(u); err != nil {
		n.a.fail("HAL9001 could not open System Settings", err)
	}
}

// openURL opens a URL with the system's default handler.
func openURL(u string) error {
	return exec.CommandContext(context.Background(), "open", u).Run() //nolint:gosec // the URL is a deep link from the operator's own daemon or a fixed settings URL
}

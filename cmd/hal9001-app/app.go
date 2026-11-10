//go:build darwin

package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"fyne.io/systray"

	"hal9001/internal/menubar"
	"hal9001/internal/service"
)

type app struct {
	profile   service.Profile
	client    *menubar.Client
	ui        string
	userHome  string
	exe       string // this executable: <bundle>/Contents/MacOS/HAL9001 when bundled
	launchctl service.Launchctl
	ctx       context.Context
	cancel    context.CancelFunc
	poke      func() // asks the refresh loop to refresh now

	// installRoot is the app bundle an update replaces: the release app's own bundle. Empty for a dev build and for a
	// binary outside a bundle; such an app is not the update installer and never registers as one.
	installRoot string

	actMu sync.Mutex // serializes menu actions

	mu       sync.Mutex
	last     menubar.Menu
	hasLast  bool
	throb    bool          // current throb state
	stopAnim chan struct{} // closes to stop the throb ticker; nil when idle
	frames   [][]byte      // precomputed throb frames, built on first use
	done     chan struct{} // closed to stop click goroutines of the current menu

	streamMu     sync.Mutex
	streamCancel context.CancelFunc // ends the current stream connection; nil before the first
	notif        *notifications

	// updMu guards the update state the menu shows.
	updMu           sync.Mutex
	updRunning      bool               // this app is executing an install
	updPhase        string             // how far it is, for the menu
	serviceUpdating bool               // the service is being registered again after an app update
	serviceRetry    context.CancelFunc // ends the automatic retries of that; nil when none runs
}

func (a *app) onReady() {
	a.ctx, a.cancel = context.WithCancel(context.Background())
	ctx := a.ctx
	systray.SetTooltip("hal9001")

	refresh := make(chan struct{}, 1)
	a.poke = func() {
		select {
		case refresh <- struct{}{}:
		default:
		}
	}
	a.refresh(ctx)

	go a.streamLoop(ctx, a.poke)
	go func() {
		t := time.NewTicker(10 * time.Second) // fallback when the stream is down; also detects daemon death
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.refresh(ctx)
			case <-refresh:
				a.refresh(ctx)
			}
		}
	}()
	go a.ensureService(ctx)
	go a.notif.start()
}

// streamLoop keeps the event stream connected. Whether it registers as the daemon's notification sink follows the
// notification settings; when they change, restartStream drops the connection and it reconnects at once.
func (a *app) streamLoop(ctx context.Context, poke func()) {
	for ctx.Err() == nil {
		cctx, cancel := context.WithCancel(ctx)
		a.streamMu.Lock()
		a.streamCancel = cancel
		a.streamMu.Unlock()
		err := a.client.Stream(cctx, menubar.StreamOptions{Notifications: a.notif.sink(), Updater: a.installRoot != ""}, a.onEvent)
		restarted := cctx.Err() != nil
		cancel()
		if ctx.Err() != nil {
			return
		}
		if restarted {
			continue
		}
		slog.Warn("stream dropped; polling until it reconnects", "err", err)
		poke()
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
}

// restartStream drops the current stream connection so that the loop reconnects with fresh options.
func (a *app) restartStream() {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	if a.streamCancel != nil {
		a.streamCancel()
	}
}

func (a *app) onEvent(ev menubar.Event) {
	switch ev.Type {
	case menubar.EventNotification:
		a.notif.onEvent(ev)
	case menubar.EventUpdateInstall:
		a.onUpdateInstall(ev)
	case "approval.updated", "inbox.updated":
		if a.notif.enabled {
			if ids := a.notif.tracker.Withdrawn(ev); len(ids) > 0 {
				notifyRemove(ids...)
			}
		}
	}
	a.poke()
}

func (a *app) refresh(ctx context.Context) {
	snap, err := a.client.Fetch(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		slog.Debug("daemon unreachable", "err", err)
		snap = nil
	}
	m := menubar.BuildMenu(snap, a.ui, a.serviceState())

	a.mu.Lock()
	defer a.mu.Unlock()
	a.setThrob(menubar.ShouldThrob(snap))
	if a.hasLast && sameMenu(a.last, m) {
		return
	}
	a.last, a.hasLast = m, true
	a.render(m)
}

func sameMenu(a, b menubar.Menu) bool { return fmt.Sprintf("%+v", a) == fmt.Sprintf("%+v", b) }

// setThrob starts or stops the single throb ticker. Called with a.mu held.
func (a *app) setThrob(on bool) {
	if on == a.throb {
		return
	}
	a.throb = on
	if !on {
		close(a.stopAnim)
		a.stopAnim = nil
		if a.hasLast { // restore the static icon
			systray.SetIcon(menubar.Icon(32, !a.last.Reachable))
		}
		return
	}
	if a.frames == nil {
		a.frames = menubar.ThrobIcons(32)
	}
	stop := make(chan struct{})
	a.stopAnim = stop
	frames := a.frames
	go func() {
		t := time.NewTicker(time.Second / menubar.ThrobFPS)
		defer t.Stop()
		for i := 0; ; i = (i + 1) % len(frames) {
			select {
			case <-stop:
				return
			case <-t.C:
				systray.SetIcon(frames[i])
			}
		}
	}()
}

// render replaces the whole menu. Called with a.mu held.
func (a *app) render(m menubar.Menu) {
	if a.done != nil {
		close(a.done)
	}
	a.done = make(chan struct{})
	systray.ResetMenu()
	if !a.throb {
		systray.SetIcon(menubar.Icon(32, !m.Reachable)) // 32px at 16pt = 2x; macOS downsamples on 1x displays
	}
	systray.SetTitle(m.Title)
	for _, e := range m.Entries {
		a.add(nil, e)
	}
}

func (a *app) add(parent *systray.MenuItem, e menubar.Entry) {
	if e.Kind == menubar.Separator {
		if parent == nil {
			systray.AddSeparator()
		} else {
			parent.AddSeparator()
		}
		return
	}
	var it *systray.MenuItem
	switch {
	case parent == nil && e.Checked:
		it = systray.AddMenuItemCheckbox(e.Title, "", true)
	case parent == nil:
		it = systray.AddMenuItem(e.Title, "")
	case e.Checked:
		it = parent.AddSubMenuItemCheckbox(e.Title, "", true)
	default:
		it = parent.AddSubMenuItem(e.Title, "")
	}
	switch e.Kind {
	case menubar.Info:
		it.Disable()
	case menubar.Submenu:
		for _, c := range e.Children {
			a.add(it, c)
		}
	case menubar.Link, menubar.Quit, menubar.Action:
		if e.Disabled {
			it.Disable()
			return
		}
		done := a.done
		go func() {
			for {
				select {
				case <-done:
					return
				case <-it.ClickedCh:
					switch e.Kind {
					case menubar.Quit:
						systray.Quit()
						return
					case menubar.Action:
						a.runAction(e.ID)
					default:
						if err := openURL(e.URL); err != nil {
							slog.Error("open failed", "url", e.URL, "err", err)
						}
					}
				}
			}
		}()
	}
}

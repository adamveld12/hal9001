// Command hal9001-menubar is a macOS menubar client of the hal9001 daemon: the red eye, the inbox count and quick
// links into the web UI. It only calls the public REST API.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"

	"hal9001/internal/menubar"
)

const defaultURL = "http://127.0.0.1:9001"

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "install" {
		os.Exit(runInstall(args[1:]))
	}
	fs := flag.NewFlagSet("hal9001-menubar", flag.ExitOnError)
	apiURL := fs.String("url", envOr("HAL9001_URL", defaultURL), "daemon base URL (env HAL9001_URL)")
	_ = fs.Parse(args)
	ui := envOr("HAL9001_UI_URL", *apiURL)

	app := &app{client: menubar.NewClient(*apiURL), ui: strings.TrimRight(ui, "/")}
	slog.Info("hal9001-menubar starting", "api", *apiURL, "ui", app.ui)
	systray.Run(app.onReady, func() { app.cancel() })
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type app struct {
	client *menubar.Client
	ui     string
	cancel context.CancelFunc

	mu      sync.Mutex
	last    menubar.Menu
	hasLast bool
	done    chan struct{} // closed to stop click goroutines of the current menu
}

func (a *app) onReady() {
	var ctx context.Context
	ctx, a.cancel = context.WithCancel(context.Background())
	systray.SetTooltip("hal9001")
	a.refresh(ctx)

	refresh := make(chan struct{}, 1)
	poke := func() {
		select {
		case refresh <- struct{}{}:
		default:
		}
	}
	go a.streamLoop(ctx, poke)
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
}

func (a *app) streamLoop(ctx context.Context, poke func()) {
	for ctx.Err() == nil {
		err := a.client.Stream(ctx, func(string) { poke() })
		if ctx.Err() != nil {
			return
		}
		slog.Warn("stream dropped; polling until it reconnects", "err", err)
		poke()
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
}

func (a *app) refresh(ctx context.Context) {
	snap, err := a.client.Fetch(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		slog.Warn("daemon unreachable", "err", err)
		snap = nil
	}
	m := menubar.BuildMenu(snap, a.ui)

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.hasLast && sameMenu(a.last, m) {
		return
	}
	a.last, a.hasLast = m, true
	a.render(m)
}

func sameMenu(a, b menubar.Menu) bool { return fmt.Sprintf("%+v", a) == fmt.Sprintf("%+v", b) }

// render replaces the whole menu. Called with a.mu held.
func (a *app) render(m menubar.Menu) {
	if a.done != nil {
		close(a.done)
	}
	a.done = make(chan struct{})
	systray.ResetMenu()
	systray.SetIcon(menubar.Icon(32, !m.Reachable)) // 32px at 16pt = 2x; macOS downsamples on 1x displays
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
	if parent == nil {
		it = systray.AddMenuItem(e.Title, "")
	} else {
		it = parent.AddSubMenuItem(e.Title, "")
	}
	switch e.Kind {
	case menubar.Info:
		it.Disable()
	case menubar.Submenu:
		for _, c := range e.Children {
			a.add(it, c)
		}
	case menubar.Link, menubar.Quit:
		done := a.done
		go func() {
			for {
				select {
				case <-done:
					return
				case <-it.ClickedCh:
					if e.Kind == menubar.Quit {
						systray.Quit()
						return
					}
					//nolint:gosec // URL is built from the operator-configured UI base URL
					if err := exec.CommandContext(context.Background(), "open", e.URL).Run(); err != nil {
						slog.Error("open failed", "url", e.URL, "err", err)
					}
				}
			}
		}()
	}
}

//go:build darwin

// Command hal9001-app is the HAL9001 macOS app: a menubar client of the hal9001 daemon (the red eye, the inbox count,
// links into the web UI) that also owns the daemon's launchd agent: it registers the agent bundled in the app
// (Contents/Library/LaunchAgents) through SMAppService and starts, stops and restarts it. It talks to the daemon only
// through the public REST API.
package main

import (
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"fyne.io/systray"

	"hal9001/internal/menubar"
	"hal9001/internal/service"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() { os.Exit(run()) }

func run() int {
	fs := flag.NewFlagSet("hal9001-app", flag.ExitOnError)
	apiURL := fs.String("url", "", "daemon base URL (env HAL9001_URL; default: the daemon this build's service runs, http://127.0.0.1:9001)")
	_ = fs.Parse(os.Args[1:])

	userHome, err := os.UserHomeDir()
	if err != nil {
		slog.Error("cannot resolve the home directory", "err", err)
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		slog.Error("cannot locate the executable", "err", err)
		return 1
	}
	profile := loadProfile()
	closeLog := logToFile(profile, userHome)
	defer closeLog()

	api := firstNonEmpty(*apiURL, os.Getenv("HAL9001_URL"), profile.APIURL())
	ui := strings.TrimRight(firstNonEmpty(os.Getenv("HAL9001_UI_URL"), api), "/")
	a := &app{profile: profile, client: menubar.NewClient(api), ui: ui, userHome: userHome, exe: exe, launchctl: runLaunchctl}
	slog.Info("hal9001-app starting", "version", version, "bundle", profile.BundleID, "api", api, "ui", ui)
	systray.Run(a.onReady, func() { a.cancel() })
	return 0
}

// loadProfile derives the build flavour from the bundle. Only a dev build honours the daemon home and port baked into
// its Info.plist, so a release app can never be pointed at a different daemon by its bundle.
func loadProfile() service.Profile {
	p := service.Profile{BundleID: bundleIdentifier()}
	if p.Dev() {
		p.Home = infoString("HAL9001DevHome")
		if n, err := strconv.Atoi(infoString("HAL9001DevPort")); err == nil {
			p.Port = n
		}
	}
	return p
}

// logToFile sends this process's slog output to app.log next to the daemon's log when the app runs from a bundle (a
// bundle launched from Finder has no terminal). It returns the function that closes the file.
func logToFile(p service.Profile, userHome string) func() {
	if !p.Bundled() {
		return func() {}
	}
	path := filepath.Join(filepath.Dir(p.LogFile(userHome)), "app.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return func() {}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // fixed path under the user's Library/Logs
	if err != nil {
		return func() {}
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, nil)))
	return func() { _ = f.Close() }
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// Package service holds the platform-independent pieces of running the hal9001 daemon under launchd: agent labels and
// plists, the app's per-build profile, detection of older or headless installs, and the command-line tool symlink.
// It never talks to launchd itself: callers pass a Launchctl function, so everything here is testable with temp dirs.
package service

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
)

// Launchd labels.
const (
	// ReleaseBundleID is the bundle id of the shipped app; its bundled agent is ReleaseBundleID + ".daemon".
	ReleaseBundleID = "dev.hal9001"
	// HeadlessLabel is the label of the daemon-only agent installed by `hal9001 service install`.
	HeadlessLabel = "dev.hal9001.daemon.headless"
	// LegacyDaemonLabel is the agent the removed `hal9001 install` wrote.
	LegacyDaemonLabel = "com.hal9001.daemon"
	// LegacyMenubarLabel is the agent the removed `hal9001-menubar install` wrote.
	LegacyMenubarLabel = "dev.hal9001.menubar"
)

// Environment variables the daemon reads when it runs as a service.
const (
	// EnvLaunchd is "1" in the environment of every agent plist this package renders. The daemon then logs to a file,
	// moves into its home directory and captures the login-shell PATH for the processes it spawns.
	EnvLaunchd = "HAL9001_LAUNCHD"
	// EnvLogFile overrides the log file of a service-run daemon; a leading "~/" is expanded by the daemon.
	EnvLogFile = "HAL9001_LOG_FILE"
	// EnvDevPort makes the daemon listen on this port of 127.0.0.1 instead of DefaultPort. Dev builds and tests only.
	EnvDevPort = "HAL9001_DEV_PORT"
)

// DefaultPort is the daemon's port.
const DefaultPort = 9001

// BundleProgram is the daemon binary inside the app bundle, relative to the bundle root.
const BundleProgram = "Contents/Resources/hal9001"

// Launchctl runs launchctl with args and returns its combined output.
type Launchctl func(ctx context.Context, args ...string) ([]byte, error)

// Domain is the launchd domain of a user's GUI session.
func Domain(uid int) string { return "gui/" + strconv.Itoa(uid) }

// Target is the launchctl service target of label in uid's GUI domain.
func Target(uid int, label string) string { return Domain(uid) + "/" + label }

// LaunchAgentsDir is where per-user LaunchAgent plists live.
func LaunchAgentsDir(userHome string) string {
	return filepath.Join(userHome, "Library", "LaunchAgents")
}

// DefaultHome is the daemon's default home directory.
func DefaultHome(userHome string) string { return filepath.Join(userHome, ".hal9001") }

// LogFile is the log file of the release daemon and of the headless service.
func LogFile(userHome string) string {
	return filepath.Join(userHome, "Library", "Logs", "hal9001", "hal9001.log")
}

// ExpandTilde expands a leading "~/" (or a lone "~") in path to userHome. launchd does not expand it in plists.
func ExpandTilde(path, userHome string) string {
	switch {
	case path == "~":
		return userHome
	case strings.HasPrefix(path, "~/"):
		return filepath.Join(userHome, path[2:])
	}
	return path
}

// Profile is what distinguishes one build of the app from another. The release app and a dev build differ only in the
// bundle id (hence the agent label, log file and CLI name), so a dev build never shares launchd, notification or login
// item state with the real app. A dev build may also pin a daemon home and port, so it can run next to a real daemon.
type Profile struct {
	// BundleID is the app's CFBundleIdentifier; empty when the app does not run from a bundle (go run), in which case
	// there is no service to manage.
	BundleID string
	// Home is the daemon home baked into a dev build; empty means ~/.hal9001.
	Home string
	// Port is the daemon port baked into a dev build; 0 means DefaultPort.
	Port int
}

// Bundled reports whether the app runs from an app bundle (required for SMAppService).
func (p Profile) Bundled() bool { return p.BundleID != "" }

// Dev reports whether this is a dev build.
func (p Profile) Dev() bool { return p.Bundled() && p.BundleID != ReleaseBundleID }

// Label is the launchd label of the app's bundled daemon agent.
func (p Profile) Label() string { return p.BundleID + ".daemon" }

// PlistName is the file name of the bundled agent plist (under Contents/Library/LaunchAgents).
func (p Profile) PlistName() string { return p.Label() + ".plist" }

// CLIName is the name of the command-line tool symlink the app installs.
func (p Profile) CLIName() string {
	if p.Dev() {
		return "hal9001-dev"
	}
	return "hal9001"
}

// LogFile is the daemon's log file for this profile.
func (p Profile) LogFile(userHome string) string {
	if p.Dev() {
		return filepath.Join(userHome, "Library", "Logs", "hal9001-dev", "hal9001.log")
	}
	return LogFile(userHome)
}

// HomeDir is the daemon's home directory for this profile.
func (p Profile) HomeDir(userHome string) string {
	if p.Home != "" {
		return p.Home
	}
	return DefaultHome(userHome)
}

// APIURL is the daemon's base URL for this profile.
func (p Profile) APIURL() string {
	port := p.Port
	if port == 0 {
		port = DefaultPort
	}
	return "http://127.0.0.1:" + strconv.Itoa(port)
}

// SharesDefaultDaemon reports whether this profile's daemon would listen on the default port. Only then can a legacy
// or headless agent (which always use it) conflict with the app's service.
func (p Profile) SharesDefaultDaemon() bool { return p.Port == 0 || p.Port == DefaultPort }

// Relevant returns what of f competes with this profile's daemon: all of it when the profile uses the default port
// (the headless and legacy agents always do), nothing otherwise (a dev build on its own port).
func (p Profile) Relevant(f Found) Found {
	if p.SharesDefaultDaemon() {
		return f
	}
	return Found{}
}

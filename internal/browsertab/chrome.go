package browsertab

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ChromeBundleID is Google Chrome's bundle identifier.
const ChromeBundleID = "com.google.Chrome"

// Runner runs an AppleScript with arguments, delivered as the script's argv, and returns its output. The script text
// is fixed; nothing from a URL is ever spliced into it.
type Runner func(ctx context.Context, script string, args ...string) (string, error)

// Osascript is the Runner that feeds the script to /usr/bin/osascript on stdin. Out of process, a hung script or a
// permission prompt nobody answers is killed when ctx ends; NSAppleScript would run on a thread of the app with no way
// to cancel it.
func Osascript(ctx context.Context, script string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", append([]string{"-"}, args...)...) //nolint:gosec // fixed program; args are data for the script's argv
	cmd.Stdin = strings.NewReader(script)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("osascript: %w", ctx.Err())
		}
		return "", fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// Chrome drives Google Chrome through its AppleScript interface. The first call makes macOS ask the user to let
// HAL9001 control Chrome (Automation permission, NSAppleEventsUsageDescription in Info.plist).
type Chrome struct {
	// IsRunning reports whether Chrome runs, without launching it. A script that names Chrome launches it, so the
	// scripts are only run after this says yes.
	IsRunning func() bool
	// Run runs the scripts; nil means Osascript.
	Run Runner
}

func (c Chrome) Name() string  { return "Google Chrome" }
func (c Chrome) Running() bool { return c.IsRunning() }

func (c Chrome) run(ctx context.Context, script string, args ...string) (string, error) {
	if c.Run != nil {
		return c.Run(ctx, script, args...)
	}
	return Osascript(ctx, script, args...)
}

// listScript prints one line per tab: window id, tab id, "1" for the active tab of its window, URL; separated by
// tabs, windows frontmost first. The guard repeats the caller's running check for the moment between the two: a tell
// block would launch a Chrome that quit in the meantime.
const listScript = `on run
	if not (application id "com.google.Chrome" is running) then return ""
	set sep to (ASCII character 9)
	set nl to (ASCII character 10)
	set out to ""
	tell application id "com.google.Chrome"
		repeat with w in windows
			set wid to id of w
			set activeIdx to active tab index of w
			set tabIds to id of tabs of w
			set tabUrls to URL of tabs of w
			repeat with i from 1 to count of tabUrls
				set isActive to "0"
				if i is activeIdx then set isActive to "1"
				set out to out & wid & sep & (item i of tabIds) & sep & isActive & sep & (item i of tabUrls) & nl
			end repeat
		end repeat
	end tell
	return out
end run`

// showScript takes window id, tab id and URL as argv. It prints "ok", or "gone" when the tab closed meanwhile.
const showScript = `on run argv
	set wid to (item 1 of argv) as integer
	set tid to (item 2 of argv) as integer
	set theUrl to item 3 of argv
	if not (application id "com.google.Chrome" is running) then return "gone"
	tell application id "com.google.Chrome"
		repeat with w in windows
			if (id of w) is wid then
				set i to 0
				repeat with t in tabs of w
					set i to i + 1
					if (id of t) is tid then
						set URL of t to theUrl
						set active tab index of w to i
						set index of w to 1
						activate
						return "ok"
					end if
				end repeat
			end if
		end repeat
	end tell
	return "gone"
end run`

func (c Chrome) Tabs(ctx context.Context) ([]Tab, error) {
	out, err := c.run(ctx, listScript)
	if err != nil {
		return nil, err
	}
	return parseTabs(out)
}

func parseTabs(out string) ([]Tab, error) {
	var tabs []Tab
	front := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 4)
		if len(f) != 4 || f[0] == "" || f[1] == "" {
			return nil, fmt.Errorf("unexpected tab line %q", line)
		}
		if front == "" {
			front = f[0]
		}
		tabs = append(tabs, Tab{Window: f[0], ID: f[1], Active: f[2] == "1", Frontmost: f[0] == front, URL: f[3]})
	}
	return tabs, nil
}

func (c Chrome) Show(ctx context.Context, t Tab, url string) error {
	out, err := c.run(ctx, showScript, t.Window, t.ID, url)
	if err != nil {
		return err
	}
	if out != "ok" {
		return errors.New("the tab closed before it could be reused")
	}
	return nil
}

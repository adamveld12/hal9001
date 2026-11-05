// Package notify delivers macOS notifications for inbox items and approvals
// (REQUIREMENTS §10.1). It runs `terminal-notifier` with an `-open` deep link
// and falls back to `osascript display notification` when terminal-notifier
// is missing or fails. Delivery is best-effort: failures are logged and
// returned, never retried.
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"hal9001/internal/core"
)

// terminalNotifier is the notifier binary looked up on PATH.
const terminalNotifier = "terminal-notifier"

// runTimeout bounds one notification command.
const runTimeout = 10 * time.Second

// Runner executes a command and returns its combined output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// LookPath resolves an executable name on PATH.
type LookPath func(name string) (string, error)

// Option configures New.
type Option func(*notifier)

// WithRunner replaces the command runner (tests).
func WithRunner(r Runner) Option { return func(n *notifier) { n.run = r } }

// WithLookPath replaces the PATH lookup (tests).
func WithLookPath(l LookPath) Option { return func(n *notifier) { n.look = l } }

// WithLogger sets the logger for delivery failures; default slog.Default().
func WithLogger(l *slog.Logger) Option { return func(n *notifier) { n.log = l } }

type notifier struct {
	run  Runner
	look LookPath
	log  *slog.Logger
}

// New returns a core.Notifier that shells out to terminal-notifier, with an
// osascript fallback.
func New(opts ...Option) core.Notifier {
	n := &notifier{run: execRunner, look: exec.LookPath, log: slog.Default()}
	for _, o := range opts {
		o(n)
	}
	return n
}

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // name is a fixed notifier binary
}

// Notify sends n. terminal-notifier is tried first; if it is not on PATH or
// exits non-zero, osascript is tried. The error joins both failures when
// neither worked.
func (n *notifier) Notify(ctx context.Context, msg core.Notification) error {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()

	primary := n.viaTerminalNotifier(ctx, msg)
	if primary == nil {
		return nil
	}
	n.log.Warn("notify: terminal-notifier failed, falling back to osascript", "err", primary, "kind", msg.Kind)
	fallback := n.viaOsascript(ctx, msg)
	if fallback == nil {
		return nil
	}
	err := errors.Join(primary, fallback)
	n.log.Error("notify: notification not delivered", "err", err, "kind", msg.Kind, "title", msg.Title)
	return err
}

func (n *notifier) viaTerminalNotifier(ctx context.Context, msg core.Notification) error {
	bin, err := n.look(terminalNotifier)
	if err != nil {
		return fmt.Errorf("%s not found on PATH: %w", terminalNotifier, err)
	}
	if out, err := n.run(ctx, bin, TerminalNotifierArgs(msg)...); err != nil {
		return fmt.Errorf("%s: %w: %s", terminalNotifier, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (n *notifier) viaOsascript(ctx context.Context, msg core.Notification) error {
	bin, err := n.look("osascript")
	if err != nil {
		return fmt.Errorf("osascript not found on PATH: %w", err)
	}
	if out, err := n.run(ctx, bin, OsascriptArgs(msg)...); err != nil {
		return fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// TerminalNotifierArgs builds the terminal-notifier argument list. The group
// is per deep link so a repeated notification for the same item replaces the
// old one while different items stack. terminal-notifier requires a message,
// so an empty body falls back to the title.
func TerminalNotifierArgs(msg core.Notification) []string {
	title, body := safeArg(msg.Title), safeArg(msg.Body)
	if strings.TrimSpace(body) == "" {
		body = title
	}
	if strings.TrimSpace(title) == "" {
		title = "hal9001"
	}
	group := "hal9001"
	if msg.URL != "" {
		group += ":" + msg.URL
	}
	args := []string{"-title", title, "-message", body, "-group", group}
	if msg.URL != "" {
		args = append(args, "-open", msg.URL)
	}
	return args
}

// OsascriptArgs builds the osascript argument list. Title and body travel as
// argv items, never spliced into the script, so quotes in them are inert.
func OsascriptArgs(msg core.Notification) []string {
	title := msg.Title
	if strings.TrimSpace(title) == "" {
		title = "hal9001"
	}
	body := msg.Body
	if strings.TrimSpace(body) == "" {
		body = title
	}
	return []string{
		"-e", "on run argv",
		"-e", "display notification (item 1 of argv) with title (item 2 of argv)",
		"-e", "end run",
		"--", body, title,
	}
}

// safeArg stops terminal-notifier from reading a value that starts with "-"
// as one of its own options.
func safeArg(s string) string {
	if strings.HasPrefix(s, "-") {
		return " " + s
	}
	return s
}

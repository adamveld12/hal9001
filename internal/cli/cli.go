// Package cli implements the hal9001 command line. Apart from `serve`,
// `install`/`uninstall`, `open`, `validate`, `auth set-password` and `version`,
// every command is a thin client of the daemon's REST API (REQUIREMENTS
// §10.2): it builds one request, prints the response (JSON by default, a table
// with --table), and exits non-zero on problem+json errors. It holds no
// business logic.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"hal9001/internal/daemon"
	"hal9001/internal/runner"
)

// Exit codes.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

// Environment variables.
const (
	EnvURL           = "HAL9001_URL"
	EnvSessionCookie = "HAL9001_SESSION_COOKIE"
	EnvHome          = "HAL9001_HOME"
	EnvWebDir        = "HAL9001_WEB_DIR"
	// EnvAssistSession is the id of the assist session the CLI runs inside; `assist test|draft|cancel` default to it.
	EnvAssistSession = "HAL9001_ASSIST_SESSION"
	// AssistAgentHeader marks a draft test requested from inside an assist session (the agent's); see
	// POST /api/assist/sessions/{id}/test.
	AssistAgentHeader = "X-Hal9001-Assist-Agent"
)

// Env is everything the CLI touches outside its own process memory, so tests
// can substitute it.
type Env struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	// HTTP is the client used for API calls; nil means http.DefaultClient.
	HTTP *http.Client
	// Version is printed by `version` and passed to the daemon.
	Version string

	// UserHomeDir resolves ~; nil means os.UserHomeDir.
	UserHomeDir func() (string, error)
	// Getuid returns the numeric user id for launchctl's gui/<uid> domain; nil means os.Getuid.
	Getuid func() int
	// Serve runs the daemon; nil means daemon.Run.
	Serve func(ctx context.Context, opts daemon.Options) error
	// LoginShellPATH captures the user's login-shell PATH; nil means runner.LoginShellPATH.
	LoginShellPATH func(ctx context.Context) (string, error)
	// Launchctl runs launchctl; nil means exec.
	Launchctl func(ctx context.Context, args ...string) ([]byte, error)
	// OpenURL opens a URL in the browser; nil means `open`.
	OpenURL func(ctx context.Context, url string) error
	// ReadPassword reads a secret from the terminal without echo; nil means
	// the controlling terminal via stty.
	ReadPassword func(prompt string) (string, error)
	// Executable returns the path of the running binary for the LaunchAgent; nil means os.Executable.
	Executable func() (string, error)
}

func (e *Env) defaults() {
	if e.Stdin == nil {
		e.Stdin = os.Stdin
	}
	if e.Stdout == nil {
		e.Stdout = os.Stdout
	}
	if e.Stderr == nil {
		e.Stderr = os.Stderr
	}
	if e.Getenv == nil {
		e.Getenv = os.Getenv
	}
	if e.UserHomeDir == nil {
		e.UserHomeDir = os.UserHomeDir
	}
	if e.Serve == nil {
		e.Serve = daemon.Run
	}
	if e.LoginShellPATH == nil {
		e.LoginShellPATH = func(ctx context.Context) (string, error) { return runner.LoginShellPATH(ctx, "", 0) }
	}
	if e.Launchctl == nil {
		e.Launchctl = func(ctx context.Context, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, "launchctl", args...).CombinedOutput() //nolint:gosec // fixed binary; args are built by this package
		}
	}
	if e.OpenURL == nil {
		e.OpenURL = func(ctx context.Context, url string) error {
			return exec.CommandContext(ctx, "open", url).Run() //nolint:gosec // fixed binary; the URL is the daemon URL
		}
	}
	if e.ReadPassword == nil {
		e.ReadPassword = func(prompt string) (string, error) { return readPasswordTTY(e.Stderr, prompt) }
	}
	if e.Getuid == nil {
		e.Getuid = os.Getuid
	}
	if e.Executable == nil {
		e.Executable = os.Executable
	}
}

// Run executes args (without the program name) and returns the exit code.
func Run(ctx context.Context, args []string, env Env) int {
	env.defaults()
	a := &app{env: env}
	err := a.run(ctx, args)
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, flag.ErrHelp), errors.Is(err, errHelp):
		return ExitOK
	}
	var ue *usageError
	if errors.As(err, &ue) {
		_, _ = fmt.Fprintf(env.Stderr, "hal9001: %s\n", ue.msg)
		if ue.hint != "" {
			_, _ = fmt.Fprintln(env.Stderr, ue.hint)
		}
		return ExitUsage
	}
	if p, ok := asProblem(err); ok {
		a.printProblem(p)
		return ExitError
	}
	var sc *silentError
	if errors.As(err, &sc) {
		return ExitError
	}
	_, _ = fmt.Fprintf(env.Stderr, "hal9001: %v\n", err)
	return ExitError
}

// usageError is a mistake in how the command was invoked (exit 2).
type usageError struct{ msg, hint string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error { return &usageError{msg: fmt.Sprintf(format, args...)} }

// silentError fails with ExitError after the command already printed why.
type silentError struct{}

func (*silentError) Error() string { return "failed" }

// errHelp is returned after help text was printed.
var errHelp = errors.New("help")

type app struct {
	env Env
	// table is set by the active command's --table flag; printProblem needs it.
	table bool
}

// printProblem reports an API error: JSON by default (stderr gets the problem
// document, for scripts), a summary with --table.
func (a *app) printProblem(p *problemError) {
	if a.table || !p.IsJSON {
		_, _ = fmt.Fprintf(a.env.Stderr, "hal9001: %s\n", p.Summary())
		for _, d := range p.Diagnostics {
			_, _ = fmt.Fprintf(a.env.Stderr, "  %s\n", d)
		}
		return
	}
	_ = writeJSON(a.env.Stderr, p.Raw)
}

// command is a node of the command tree: a group with subcommands, or a leaf.
type command struct {
	name    string
	summary string
	subs    []*command
	leaf    *leaf
}

// leaf is an executable command.
type leaf struct {
	// usage lists the positional arguments, e.g. "<id>".
	usage string
	// minArgs and maxArgs bound the positional arguments (maxArgs < 0: unlimited).
	minArgs, maxArgs int
	// local commands do not talk to the API; they get --home instead of the API flags.
	local bool
	// flags registers the command's own flags.
	flags func(fs *flag.FlagSet)
	run   func(ctx context.Context, c *call) error
}

// call is one invocation of a leaf command.
type call struct {
	app  *app
	env  *Env
	args []string
	fs   *flag.FlagSet

	url, cookie, home string
	table             bool
}

func (c *call) stdout() io.Writer { return c.env.Stdout }
func (c *call) stderr() io.Writer { return c.env.Stderr }

// flagSet reports whether the user passed --name.
func (c *call) flagSet(name string) bool {
	set := false
	c.fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func (a *app) run(ctx context.Context, args []string) error {
	root := a.tree()
	if len(args) == 0 {
		a.printGroup(a.env.Stderr, "hal9001", root)
		return usagef("no command given")
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		return a.help(root, args[1:])
	}
	cmd, path, rest, err := resolve(root, args)
	if err != nil {
		return err
	}
	if cmd.leaf == nil {
		if len(rest) > 0 && (rest[0] == "-h" || rest[0] == "--help") {
			a.printGroup(a.env.Stdout, path, cmd)
			return errHelp
		}
		a.printGroup(a.env.Stderr, path, cmd)
		return usagef("%s: missing subcommand", path)
	}
	return a.runLeaf(ctx, cmd, path, rest)
}

func (a *app) help(root *command, args []string) error {
	cmd, path, _, err := resolve(root, args)
	if err != nil || cmd == nil {
		a.printGroup(a.env.Stdout, "hal9001", root)
		return errHelp
	}
	if cmd.leaf != nil {
		_, _ = fmt.Fprintf(a.env.Stdout, "usage: %s %s\n%s\n", path, cmd.leaf.usage, cmd.summary)
		return errHelp
	}
	a.printGroup(a.env.Stdout, path, cmd)
	return errHelp
}

// resolve walks the tree along args. It returns the deepest command reached,
// its path, and the remaining arguments.
func resolve(root *command, args []string) (*command, string, []string, error) {
	cmd, path := root, "hal9001"
	for len(args) > 0 && cmd.leaf == nil {
		name := args[0]
		var next *command
		for _, s := range cmd.subs {
			if s.name == name {
				next = s
				break
			}
		}
		if next == nil {
			if strings.HasPrefix(name, "-") {
				break
			}
			return nil, "", nil, &usageError{msg: fmt.Sprintf("unknown command %q", strings.TrimPrefix(path+" "+name, "hal9001 ")), hint: "run `hal9001 help` for the list of commands"}
		}
		cmd, path, args = next, path+" "+name, args[1:]
	}
	return cmd, path, args, nil
}

func (a *app) printGroup(w io.Writer, path string, cmd *command) {
	_, _ = fmt.Fprintf(w, "usage: %s <command> [flags]\n\n", path)
	if cmd.summary != "" && path != "hal9001" {
		_, _ = fmt.Fprintf(w, "%s\n\n", cmd.summary)
	}
	width := 0
	for _, s := range cmd.subs {
		width = max(width, len(s.name))
	}
	for _, s := range cmd.subs {
		_, _ = fmt.Fprintf(w, "  %-*s  %s\n", width, s.name, s.summary)
	}
	if path == "hal9001" {
		_, _ = fmt.Fprintf(w, "\nAPI commands print JSON; add --table for a human-readable view.\nGlobal API flags: --url (env %s, default %s), --session-cookie (env %s), --table.\n", EnvURL, DefaultURL, EnvSessionCookie)
	}
}

func (a *app) runLeaf(ctx context.Context, cmd *command, path string, args []string) error {
	l := cmd.leaf
	fs := flag.NewFlagSet(path, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	c := &call{app: a, env: &a.env, fs: fs}
	if l.local {
		fs.StringVar(&c.home, "home", "", "hal9001 home directory (env "+EnvHome+", default ~/.hal9001)")
	} else {
		fs.StringVar(&c.url, "url", "", "daemon URL (env "+EnvURL+", default "+DefaultURL+")")
		fs.StringVar(&c.cookie, "session-cookie", "", "session cookie for non-local access (env "+EnvSessionCookie+")")
		fs.BoolVar(&c.table, "table", false, "print a table instead of JSON")
	}
	if l.flags != nil {
		l.flags(fs)
	}
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printLeafHelp(a.env.Stdout, path, cmd, fs)
			return errHelp
		}
		return &usageError{msg: fmt.Sprintf("%s: %v", short(path), err), hint: "usage: " + path + " " + l.usage}
	}
	if len(pos) < l.minArgs || (l.maxArgs >= 0 && len(pos) > l.maxArgs) {
		return &usageError{msg: short(path) + ": wrong number of arguments", hint: "usage: " + path + " " + l.usage}
	}
	c.args = pos
	a.table = c.table
	if !l.local {
		if c.url == "" {
			c.url = a.env.Getenv(EnvURL)
		}
		if c.url == "" {
			c.url = DefaultURL
		}
		if c.cookie == "" {
			c.cookie = a.env.Getenv(EnvSessionCookie)
		}
	}
	return l.run(ctx, c)
}

// short drops the program name from a command path for error messages.
func short(path string) string { return strings.TrimPrefix(path, "hal9001 ") }

func printLeafHelp(w io.Writer, path string, cmd *command, fs *flag.FlagSet) {
	_, _ = fmt.Fprintf(w, "usage: %s %s\n%s\n\nflags:\n", path, cmd.leaf.usage, cmd.summary)
	fs.SetOutput(w)
	fs.PrintDefaults()
	fs.SetOutput(io.Discard)
}

// parseInterspersed parses flags that may appear before, between or after
// positional arguments. Everything after a literal "--" is positional.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var tail []string
	for i, a := range args {
		if a == "--" {
			args, tail = args[:i], args[i+1:]
			break
		}
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	return append(pos, tail...), nil
}

// homeDir resolves the home directory: --home, $HAL9001_HOME, then ~/.hal9001.
func (c *call) homeDir() (string, error) {
	h := c.home
	if h == "" {
		h = c.env.Getenv(EnvHome)
	}
	if h == "" {
		u, err := c.env.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		h = filepath.Join(u, ".hal9001")
	}
	return filepath.Abs(h)
}

func (c *call) client() *client { return newClient(c.url, c.cookie, c.env.HTTP) }

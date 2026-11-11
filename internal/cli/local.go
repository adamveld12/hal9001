package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"hal9001/content"
	"hal9001/internal/auth"
	"hal9001/internal/daemon"
	"hal9001/internal/runner"
	"hal9001/internal/service"
	"hal9001/internal/spec"
)

func versionLeaf() *leaf {
	return &leaf{local: true, maxArgs: 0, run: func(_ context.Context, c *call) error {
		_, err := fmt.Fprintln(c.stdout(), c.env.Version)
		return err
	}}
}

func serveLeaf() *leaf {
	var webDir string
	return &leaf{
		local: true, maxArgs: 0,
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&webDir, "web-dir", "", "serve the UI from this directory instead of the embedded one (env "+EnvWebDir+")")
		},
		run: func(ctx context.Context, c *call) error {
			home, err := c.homeDir()
			if err != nil {
				return err
			}
			if webDir == "" {
				webDir = c.env.Getenv(EnvWebDir)
			}
			opts := daemon.Options{Home: home, Version: c.env.Version, WebDir: webDir, Log: nil}
			if p := c.env.Getenv(service.EnvDevPort); p != "" {
				if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
					return fmt.Errorf("%s=%q is not a port number", service.EnvDevPort, p)
				}
				opts.ListenAddr = net.JoinHostPort("127.0.0.1", p)
			}
			if c.env.Getenv(service.EnvLaunchd) == "1" {
				if err := c.setupLaunchd(ctx, &opts); err != nil {
					return err
				}
			}
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()
			return c.env.Serve(ctx, opts)
		},
	}
}

// setupLaunchd prepares a daemon that launchd started (HAL9001_LAUNCHD=1): launchd gives it no log file, a minimal
// PATH and no terminal, so it writes its own log and hands the login shell's PATH to the processes it spawns.
func (c *call) setupLaunchd(ctx context.Context, opts *daemon.Options) error {
	user, err := c.env.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve user home: %w", err)
	}
	logFile := service.ExpandTilde(c.env.Getenv(service.EnvLogFile), user)
	if logFile == "" {
		logFile = service.LogFile(user)
	}
	if err := os.MkdirAll(filepath.Dir(logFile), 0o750); err != nil {
		return err
	}
	if err := c.env.RedirectLog(logFile); err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	captured, err := c.env.LoginShellPATH(ctx)
	if err != nil {
		slog.Warn("login-shell PATH unavailable; using launchd's PATH plus well-known directories", "err", err)
		captured = c.env.Getenv("PATH")
	}
	path := runner.EffectivePATH(captured, user)
	// The daemon's own lookups (kubectl, gh, tailscale, terminal-notifier, the shell a sandbox runs) use the
	// process PATH, and exec.LookPath ignores a child's env, so the effective PATH has to be the process's too.
	if err := c.env.Setenv("PATH", path); err != nil {
		return fmt.Errorf("set PATH: %w", err)
	}
	opts.BasePATH = path
	slog.Info("running under launchd", "log", logFile, "path", path)
	return nil
}

func openLeaf() *leaf {
	return &leaf{
		local: true, usage: "[path]", maxArgs: 1,
		run: func(ctx context.Context, c *call) error {
			base := c.env.Getenv(EnvURL)
			if base == "" {
				base = DefaultURL
			}
			target := strings.TrimRight(base, "/") + "/"
			if len(c.args) == 1 {
				target += strings.TrimLeft(c.args[0], "/")
			}
			if _, err := url.Parse(target); err != nil {
				return fmt.Errorf("bad URL %q: %w", target, err)
			}
			if err := c.env.OpenURL(ctx, target); err != nil {
				return fmt.Errorf("open %s: %w", target, err)
			}
			_, err := fmt.Fprintln(c.stdout(), target)
			return err
		},
	}
}

func validateLeaf() *leaf {
	var asJSON bool
	return &leaf{
		local: true, maxArgs: 0,
		flags: func(fs *flag.FlagSet) { fs.BoolVar(&asJSON, "json", false, "print diagnostics as JSON") },
		run: func(_ context.Context, c *call) error {
			home, err := c.homeDir()
			if err != nil {
				return err
			}
			if st, err := os.Stat(home); err != nil || !st.IsDir() {
				return fmt.Errorf("home directory %s does not exist (start the daemon once, or pass --home)", home)
			}
			ws := spec.Load(home)
			ds := ws.AllDiagnostics()
			var errs, warns int
			for _, d := range ds {
				switch d.Severity {
				case spec.SeverityError:
					errs++
				case spec.SeverityWarning:
					warns++
				}
			}
			if asJSON {
				if ds == nil {
					ds = []spec.Diagnostic{}
				}
				out, _ := json.Marshal(map[string]any{"home": home, "errors": errs, "warnings": warns, "diagnostics": ds})
				if err := writeJSON(c.stdout(), out); err != nil {
					return err
				}
			} else {
				for _, d := range ds {
					_, _ = fmt.Fprintln(c.stdout(), d.String())
				}
				_, _ = fmt.Fprintf(c.stdout(), "%s: %d sensors, %d flows, %d prompts; %d errors, %d warnings\n",
					home, len(ws.Sensors), len(ws.Flows), len(ws.Prompts), errs, warns)
			}
			if errs > 0 {
				return &silentError{}
			}
			return nil
		},
	}
}

func setPasswordLeaf() *leaf {
	var fromStdin bool
	return &leaf{
		local: true, maxArgs: 0,
		flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&fromStdin, "stdin", false, "read the password from the first line of stdin instead of prompting")
		},
		run: func(_ context.Context, c *call) error {
			home, err := c.homeDir()
			if err != nil {
				return err
			}
			pw, err := c.readNewPassword(fromStdin)
			if err != nil {
				return err
			}
			hash, err := auth.HashPassword(pw)
			if err != nil {
				return err
			}
			n, err := writePasswordHash(home, hash)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(c.stdout(), "Password stored in %s (%d password module entries). A running daemon reloads it automatically.\n", spec.FileConfig, n)
			return err
		},
	}
}

func (c *call) readNewPassword(fromStdin bool) (string, error) {
	if fromStdin {
		line, err := bufio.NewReader(c.env.Stdin).ReadString('\n')
		if err != nil && (!errors.Is(err, io.EOF) || line == "") {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		pw := strings.TrimRight(line, "\r\n")
		if pw == "" {
			return "", errors.New("the password is empty")
		}
		if err := auth.ValidatePassword(pw); err != nil {
			return "", err
		}
		return pw, nil
	}
	first, err := c.env.ReadPassword("New password: ")
	if err != nil {
		return "", err
	}
	if first == "" {
		return "", errors.New("the password is empty")
	}
	second, err := c.env.ReadPassword("Repeat password: ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New("the passwords do not match; nothing was changed")
	}
	if err := auth.ValidatePassword(first); err != nil {
		return "", err
	}
	return first, nil
}

// readPasswordTTY prompts on the controlling terminal with echo off.
func readPasswordTTY(errOut io.Writer, prompt string) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("no terminal to prompt on (use --stdin): %w", err)
	}
	defer func() { _ = tty.Close() }()
	saved, err := stty(tty, "-g")
	if err != nil {
		return "", fmt.Errorf("read terminal settings: %w", err)
	}
	saved = strings.TrimSpace(saved)
	if _, err := stty(tty, "-echo"); err != nil {
		return "", fmt.Errorf("turn terminal echo off: %w", err)
	}
	restore := func() { _, _ = stty(tty, saved) }
	defer restore()

	// Ctrl-C ends the process without the shell's cleanup; restore echo first.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-sig:
			restore()
			_, _ = fmt.Fprintln(errOut)
			os.Exit(130)
		case <-done:
		}
	}()

	_, _ = fmt.Fprint(tty, prompt)
	line, err := bufio.NewReader(tty).ReadString('\n')
	_, _ = fmt.Fprintln(tty)
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", fmt.Errorf("read password: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func stty(tty *os.File, args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), "stty", args...) //nolint:gosec // fixed binary, flags are literals or a value stty printed itself
	cmd.Stdin = tty
	out, err := cmd.Output()
	return string(out), err
}

// startersLeaves builds the `starters list` and `starters add` leaves.
func startersGroup() *command {
	list := &leaf{
		local: true, maxArgs: 0,
		run: func(_ context.Context, c *call) error {
			home, err := c.homeDir()
			if err != nil {
				return err
			}
			all, err := spec.Starters(content.FS)
			if err != nil {
				return err
			}
			for _, p := range all {
				state := "missing"
				if _, err := os.Stat(filepath.Join(home, filepath.FromSlash(p))); err == nil {
					state = "present"
				}
				_, _ = fmt.Fprintf(c.stdout(), "%-8s %s\n", state, p)
			}
			return nil
		},
	}
	add := &leaf{
		local: true, usage: "[name...]", maxArgs: -1,
		run: func(_ context.Context, c *call) error {
			home, err := c.homeDir()
			if err != nil {
				return err
			}
			created, err := spec.SeedStarters(home, content.FS, c.args)
			if err != nil {
				return err
			}
			for _, p := range created {
				_, _ = fmt.Fprintln(c.stdout(), "added", p)
			}
			if len(created) == 0 {
				_, _ = fmt.Fprintln(c.stdout(), "nothing to add (existing files are never overwritten)")
			}
			return nil
		},
	}
	return group("starters", "list and add starter content",
		cmd("list", "show starter files and whether each exists in the home", list),
		cmd("add", "copy the named starters (or all missing ones) into the home; never overwrites", add))
}

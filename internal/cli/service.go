package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"hal9001/internal/service"
)

const (
	healthTimeout  = 20 * time.Second
	healthInterval = 500 * time.Millisecond
)

func serviceGroup() *command {
	return group("service", "run the daemon without the macOS app: a headless launchd LaunchAgent",
		cmd("install", "install and start the headless LaunchAgent (--dry-run prints its plist)", serviceInstallLeaf()),
		cmd("uninstall", "stop and remove the headless LaunchAgent (and the agent of older versions)", serviceUninstallLeaf()),
		cmd("status", "show whether the headless LaunchAgent is installed, loaded and answering", serviceStatusLeaf()),
		cmd("logs", "print the daemon log written under launchd (-f follows)", serviceLogsLeaf()))
}

// headlessPlan is everything `service install` derives from the environment.
type headlessPlan struct {
	userHome  string
	agent     service.Agent
	plistPath string
	logFile   string
}

func (c *call) headlessPlan() (headlessPlan, error) {
	home, err := c.homeDir()
	if err != nil {
		return headlessPlan{}, err
	}
	user, err := c.env.UserHomeDir()
	if err != nil {
		return headlessPlan{}, fmt.Errorf("resolve user home: %w", err)
	}
	exe, err := c.env.Executable()
	if err != nil {
		return headlessPlan{}, fmt.Errorf("locate this executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return headlessPlan{
		userHome:  user,
		agent:     service.HeadlessAgent(exe, home, user),
		plistPath: service.PlistPath(user, service.HeadlessLabel),
		logFile:   service.LogFile(user),
	}, nil
}

func (c *call) uid() int { return c.env.Getuid() }

func serviceInstallLeaf() *leaf {
	var dry bool
	return &leaf{
		local: true, maxArgs: 0,
		flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&dry, "dry-run", false, "print the LaunchAgent plist without installing it")
		},
		run: func(ctx context.Context, c *call) error {
			plan, err := c.headlessPlan()
			if err != nil {
				return err
			}
			data := plan.agent.Plist()
			if dry {
				_, err := c.stdout().Write(data)
				return err
			}
			// The app's own agent serves the same port; two daemons would fight over it.
			if _, err := c.env.Launchctl(ctx, "print", service.Target(c.uid(), service.ReleaseBundleID+".daemon")); err == nil {
				return fmt.Errorf("the HAL9001 app is running its own daemon (agent %s.daemon); stop it from the app's menu (Service > Stop service) before installing a headless one", service.ReleaseBundleID)
			}
			// `hal9001 install` of older versions wrote com.hal9001.daemon; this command replaces it.
			found, err := service.Detect(plan.userHome)
			if err != nil {
				return err
			}
			for _, l := range found.Legacy {
				if l.Label != service.LegacyDaemonLabel {
					continue
				}
				if err := service.Remove(ctx, c.env.Launchctl, c.uid(), l.Label, l.Path); err != nil {
					return err
				}
				_, _ = fmt.Fprintf(c.stdout(), "Removed the agent of an older version: %s\n", l.Path)
			}
			for _, d := range []string{filepath.Dir(plan.plistPath), filepath.Dir(plan.logFile), plan.agent.Args[3]} {
				if err := os.MkdirAll(d, 0o750); err != nil {
					return err
				}
			}
			if err := os.WriteFile(plan.plistPath, data, 0o644); err != nil { //nolint:gosec // launchd requires a world-readable plist
				return err
			}
			// Replace a running instance: bootout fails harmlessly when nothing is loaded.
			_, _ = c.env.Launchctl(ctx, "bootout", service.Target(c.uid(), service.HeadlessLabel))
			if out, err := c.env.Launchctl(ctx, "bootstrap", service.Domain(c.uid()), plan.plistPath); err != nil {
				return fmt.Errorf("launchctl bootstrap %s %s: %w: %s", service.Domain(c.uid()), plan.plistPath, err, strings.TrimSpace(string(out)))
			}
			_, _ = fmt.Fprintf(c.stdout(), "Installed %s\nLogs: %s\n", plan.plistPath, plan.logFile)
			base := c.env.Getenv(EnvURL)
			if base == "" {
				base = DefaultURL
			}
			if err := waitHealthy(ctx, newClient(base, "", c.env.HTTP), healthTimeout); err != nil {
				return fmt.Errorf("the LaunchAgent is loaded but the daemon is not answering at %s: %w (see %s)", base, err, plan.logFile)
			}
			_, err = fmt.Fprintf(c.stdout(), "hal9001 is running at %s\n", base)
			return err
		},
	}
}

// waitHealthy polls /api/health until it answers 200 or the timeout passes.
func waitHealthy(ctx context.Context, cl *client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		_, err := cl.do(ctx, http.MethodGet, "/api/health", nil, nil)
		if err == nil {
			return nil
		}
		if !time.Now().Add(healthInterval).Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(healthInterval):
		}
	}
}

func serviceUninstallLeaf() *leaf {
	return &leaf{
		local: true, maxArgs: 0,
		run: func(ctx context.Context, c *call) error {
			user, err := c.env.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolve user home: %w", err)
			}
			found, err := service.Detect(user)
			if err != nil {
				return err
			}
			type agent struct{ label, path string }
			agents := []agent{{service.HeadlessLabel, service.PlistPath(user, service.HeadlessLabel)}}
			for _, l := range found.Legacy {
				if l.Label == service.LegacyDaemonLabel {
					agents = append(agents, agent{l.Label, l.Path})
				}
			}
			for _, a := range agents {
				if err := service.Remove(ctx, c.env.Launchctl, c.uid(), a.label, a.path); err != nil {
					return err
				}
				_, _ = fmt.Fprintf(c.stdout(), "Removed %s\n", a.path)
			}
			_, err = fmt.Fprintln(c.stdout(), "Your home directory and logs were left in place.")
			return err
		},
	}
}

func serviceStatusLeaf() *leaf {
	return &leaf{
		local: true, maxArgs: 0,
		run: func(ctx context.Context, c *call) error {
			user, err := c.env.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolve user home: %w", err)
			}
			found, err := service.Detect(user)
			if err != nil {
				return err
			}
			w := c.stdout()
			if !found.Headless {
				_, _ = fmt.Fprintf(w, "Headless service: not installed (hal9001 service install)\n")
			} else {
				_, _ = fmt.Fprintf(w, "Headless service: installed (%s)\n", found.HeadlessPath)
				out, err := c.env.Launchctl(ctx, "print", service.Target(c.uid(), service.HeadlessLabel))
				if err != nil {
					_, _ = fmt.Fprintf(w, "launchd:          not loaded (hal9001 service install loads it)\n")
				} else {
					info := service.ParsePrint(string(out))
					line := "state " + info.State
					if info.PID > 0 {
						line += fmt.Sprintf(", pid %d", info.PID)
					}
					if info.LastExit != "" {
						line += ", last exit code " + info.LastExit
					}
					_, _ = fmt.Fprintf(w, "launchd:          %s\n", line)
				}
			}
			for _, l := range found.Legacy {
				_, _ = fmt.Fprintf(w, "Older agent:      %s (%s); `hal9001 service uninstall` removes the daemon one, the HAL9001 app offers to remove both\n", l.Label, l.Path)
			}
			base := c.env.Getenv(EnvURL)
			if base == "" {
				base = DefaultURL
			}
			if res, err := newClient(base, "", c.env.HTTP).do(ctx, http.MethodGet, "/api/health", nil, nil); err != nil {
				_, _ = fmt.Fprintf(w, "Daemon:           not answering at %s\n", base)
			} else {
				_, _ = fmt.Fprintf(w, "Daemon:           answering at %s %s\n", base, strings.TrimSpace(string(res.Body)))
			}
			_, err = fmt.Fprintf(w, "Logs:             %s\n", service.LogFile(user))
			return err
		},
	}
}

func serviceLogsLeaf() *leaf {
	var lines int
	var follow bool
	return &leaf{
		local: true, maxArgs: 0,
		flags: func(fs *flag.FlagSet) {
			fs.IntVar(&lines, "n", 50, "number of trailing lines to print")
			fs.BoolVar(&follow, "f", false, "keep printing lines as they are appended (Ctrl-C stops)")
		},
		run: func(ctx context.Context, c *call) error {
			user, err := c.env.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolve user home: %w", err)
			}
			path := service.LogFile(user)
			f, err := os.Open(path) //nolint:gosec // the fixed log file under the user's home
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return fmt.Errorf("no log yet: %s does not exist (the daemon has not run under launchd)", path)
				}
				return err
			}
			defer func() { _ = f.Close() }()
			if err := printTail(c.stdout(), f, lines); err != nil {
				return err
			}
			if !follow {
				return nil
			}
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()
			return followFile(ctx, c.stdout(), f)
		},
	}
}

// printTail writes the last n lines of f and leaves f positioned at its end.
func printTail(w io.Writer, f *os.File, n int) error {
	const window = 1 << 20 // the last MiB is plenty for any n a person asks for
	st, err := f.Stat()
	if err != nil {
		return err
	}
	start := max(st.Size()-window, 0)
	buf := make([]byte, st.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	text := string(buf)
	if start > 0 { // drop the partial first line of the window
		if _, rest, ok := strings.Cut(text, "\n"); ok {
			text = rest
		}
	}
	all := strings.SplitAfter(text, "\n")
	if all[len(all)-1] == "" {
		all = all[:len(all)-1]
	}
	if n >= 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	_, err = io.WriteString(w, strings.Join(all, ""))
	if err != nil {
		return err
	}
	_, err = f.Seek(0, io.SeekEnd)
	return err
}

// followFile copies appended data from f to w until ctx is done.
func followFile(ctx context.Context, w io.Writer, f *os.File) error {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		if _, err := io.Copy(w, f); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

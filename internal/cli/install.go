package cli

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LaunchAgentLabel is the launchd label of the daemon.
const LaunchAgentLabel = "com.hal9001.daemon"

const (
	healthTimeout  = 20 * time.Second
	healthInterval = 500 * time.Millisecond
)

// launchAgent describes the LaunchAgent to generate.
type launchAgent struct {
	Executable string
	Home       string
	PATH       string
	LogDir     string
}

func (l launchAgent) logPath() string { return filepath.Join(l.LogDir, "hal9001.log") }

// plist renders the LaunchAgent property list: started at load, restarted when
// it exits (KeepAlive), logging under ~/Library/Logs/hal9001/, with the
// login-shell PATH so mise shims, gh and jq resolve inside nodes.
func (l launchAgent) plist() []byte {
	var b bytes.Buffer
	str := func(indent, s string) { fmt.Fprintf(&b, "%s<string>%s</string>\n", indent, xmlEscape(s)) }
	key := func(indent, k string) { fmt.Fprintf(&b, "%s<key>%s</key>\n", indent, k) }
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	key("\t", "Label")
	str("\t", LaunchAgentLabel)
	key("\t", "ProgramArguments")
	b.WriteString("\t<array>\n")
	for _, a := range []string{l.Executable, "serve", "--home", l.Home} {
		str("\t\t", a)
	}
	b.WriteString("\t</array>\n")
	key("\t", "EnvironmentVariables")
	b.WriteString("\t<dict>\n")
	key("\t\t", "PATH")
	str("\t\t", l.PATH)
	b.WriteString("\t</dict>\n")
	key("\t", "WorkingDirectory")
	str("\t", l.Home)
	key("\t", "RunAtLoad")
	b.WriteString("\t<true/>\n")
	key("\t", "KeepAlive")
	b.WriteString("\t<true/>\n")
	key("\t", "StandardOutPath")
	str("\t", l.logPath())
	key("\t", "StandardErrorPath")
	str("\t", l.logPath())
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func (c *call) launchAgentPlan(ctx context.Context) (launchAgent, string, error) {
	home, err := c.homeDir()
	if err != nil {
		return launchAgent{}, "", err
	}
	user, err := c.env.UserHomeDir()
	if err != nil {
		return launchAgent{}, "", fmt.Errorf("resolve user home: %w", err)
	}
	exe, err := c.env.Executable()
	if err != nil {
		return launchAgent{}, "", fmt.Errorf("locate this executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	path, err := c.env.LoginShellPATH(ctx)
	if err != nil {
		return launchAgent{}, "", err
	}
	plan := launchAgent{Executable: exe, Home: home, PATH: path, LogDir: filepath.Join(user, "Library", "Logs", "hal9001")}
	return plan, filepath.Join(user, "Library", "LaunchAgents", LaunchAgentLabel+".plist"), nil
}

func (c *call) domain() string { return "gui/" + strconv.Itoa(c.env.Getuid()) }

func installLeaf() *leaf {
	var dry bool
	return &leaf{
		local: true, maxArgs: 0,
		flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&dry, "dry-run", false, "print the LaunchAgent plist without installing it")
		},
		run: func(ctx context.Context, c *call) error {
			plan, plistPath, err := c.launchAgentPlan(ctx)
			if err != nil {
				return err
			}
			data := plan.plist()
			if dry {
				_, err := c.stdout().Write(data)
				return err
			}
			for _, d := range []string{filepath.Dir(plistPath), plan.LogDir, plan.Home} {
				if err := os.MkdirAll(d, 0o750); err != nil {
					return err
				}
			}
			if err := os.WriteFile(plistPath, data, 0o644); err != nil { //nolint:gosec // launchd requires a world-readable plist
				return err
			}
			// Replace a running instance: bootout fails harmlessly when nothing is loaded.
			_, _ = c.env.Launchctl(ctx, "bootout", c.domain()+"/"+LaunchAgentLabel)
			if out, err := c.env.Launchctl(ctx, "bootstrap", c.domain(), plistPath); err != nil {
				return fmt.Errorf("launchctl bootstrap %s %s: %w: %s", c.domain(), plistPath, err, strings.TrimSpace(string(out)))
			}
			_, _ = fmt.Fprintf(c.stdout(), "Installed %s\nLogs: %s\n", plistPath, plan.logPath())
			base := c.env.Getenv(EnvURL)
			if base == "" {
				base = DefaultURL
			}
			if err := waitHealthy(ctx, newClient(base, "", c.env.HTTP), healthTimeout); err != nil {
				return fmt.Errorf("the LaunchAgent is loaded but the daemon is not answering at %s: %w (see %s)", base, err, plan.logPath())
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

func uninstallLeaf() *leaf {
	return &leaf{
		local: true, maxArgs: 0,
		run: func(ctx context.Context, c *call) error {
			user, err := c.env.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolve user home: %w", err)
			}
			plistPath := filepath.Join(user, "Library", "LaunchAgents", LaunchAgentLabel+".plist")
			out, bootErr := c.env.Launchctl(ctx, "bootout", c.domain()+"/"+LaunchAgentLabel)
			if bootErr != nil {
				// Not loaded is fine; anything else is worth showing.
				_, _ = fmt.Fprintf(c.stderr(), "launchctl bootout: %v: %s\n", bootErr, strings.TrimSpace(string(out)))
			}
			if err := os.Remove(plistPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			_, err = fmt.Fprintf(c.stdout(), "Removed %s. Your home directory and logs were left in place.\n", plistPath)
			return err
		},
	}
}

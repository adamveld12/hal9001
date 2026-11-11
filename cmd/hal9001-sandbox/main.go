// Command hal9001-sandbox runs a throwaway hal9001 daemon for development and browser verification.
//
// It builds the fake ACP agent, seeds a temporary home from the starter content, adds a `fake` runtime that plays a
// script, serves on a free loopback port (never 9001) and deletes the home on exit unless --keep is given.
// Test and dev use only.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"hal9001/content"
	"hal9001/internal/agent/agenttest"
	"hal9001/internal/agent/fakeagent"
	"hal9001/internal/daemon"
	"hal9001/internal/httpsec"
	"hal9001/internal/spec"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "hal9001-sandbox:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hal9001-sandbox", flag.ContinueOnError)
	fs.SetOutput(stderr)
	port := fs.Int("port", 0, "TCP port on 127.0.0.1 (default: a free port; 9001 is refused)")
	script := fs.String("script", "", "fake-acp-agent script (JSON) for the `fake` runtime (default: a one-line reply)")
	webDir := fs.String("web-dir", "", "serve the UI from this directory (default: embedded UI, else web/dist)")
	keep := fs.Bool("keep", false, "keep the temporary home on exit")
	path := fs.String("path", "", "the only PATH for finding the claude/codex/omp CLIs, node, npm and tailscale (default: this process's PATH plus the usual tool directories for the agent CLIs; no tailscale CLI at all, never the machine's own); point it at stub executables to try runtime and Tailscale detection")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *port == httpsec.DefaultPort {
		return fmt.Errorf("port %d belongs to the real daemon; pick another", httpsec.DefaultPort)
	}
	if *port < 0 || *port > 65535 {
		return fmt.Errorf("invalid port %d", *port)
	}

	home, err := os.MkdirTemp("", "hal9001-sandbox-")
	if err != nil {
		return err
	}
	if !*keep {
		defer func() { _ = os.RemoveAll(home) }()
	}

	if _, err := spec.Init(home, content.FS); err != nil {
		return err
	}
	binDir := filepath.Join(home, "fake")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		return err
	}
	bin, err := agenttest.BuildFakeAgent(binDir)
	if err != nil {
		return err
	}
	scriptPath := *script
	if scriptPath == "" {
		scriptPath = filepath.Join(binDir, "script.json")
		s := fakeagent.Script{
			Modes: []fakeagent.Mode{{ID: "default"}, {ID: "bypass"}},
			Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{{Op: "message", Text: "fake agent: {{prompt}}"}}}},
		}
		if err := s.Write(scriptPath); err != nil {
			return err
		}
	} else if scriptPath, err = filepath.Abs(scriptPath); err != nil {
		return err
	}
	halBin, err := agenttest.BuildHal(binDir)
	if err != nil {
		return err
	}
	if _, err := fakeagent.Load(scriptPath); err != nil {
		return err
	}
	if err := addFakeRuntime(filepath.Join(home, "config.yaml"), bin, scriptPath); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := daemon.Options{
		Home: home, Version: "sandbox", WebDir: *webDir, Executable: halBin, BasePATH: *path, ExactPATH: *path != "", TailscaleExact: true, TailscalePATH: *path,
		ListenAddr: net.JoinHostPort("127.0.0.1", fmt.Sprint(*port)),
		Log:        slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		Ready: func(addr net.Addr) {
			kept := "home is deleted on exit (--keep to retain)"
			if *keep {
				kept = "home is kept on exit (--keep)"
			}
			_, _ = fmt.Fprintf(stdout, "sandbox URL:  http://%s/\nsandbox home: %s\n%s\nstop:         Ctrl-C or SIGTERM to this process\n", addr, home, kept)
		},
	}
	if err := daemon.Run(ctx, opts); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// addFakeRuntime registers the `fake` runtime under the starter config's `runtimes:` map.
func addFakeRuntime(configPath, bin, script string) error {
	b, err := os.ReadFile(configPath) //nolint:gosec // path inside the sandbox home
	if err != nil {
		return err
	}
	const marker = "\nruntimes:\n"
	text := string(b)
	if !strings.Contains(text, marker) {
		return fmt.Errorf("%s has no top-level runtimes: block", configPath)
	}
	entry := fmt.Sprintf("  fake:\n    command: [%q, --script, %q]\n    modes: {ask: default, full: bypass}\n", bin, script)
	text = strings.Replace(text, marker, marker+entry, 1)
	return os.WriteFile(configPath, //nolint:gosec // path inside the sandbox home
		[]byte(text), 0o600)
}

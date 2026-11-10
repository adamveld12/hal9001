// Package daemon wires and runs the hal9001 daemon: seeds the home dir, loads
// the workspace, opens the store, starts the engine, serves the API and UI on
// 127.0.0.1:9001, and hot-reloads configuration files (REQUIREMENTS §10.1).
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"hal9001/content"
	"hal9001/internal/agent"
	"hal9001/internal/assist"
	"hal9001/internal/auth"
	"hal9001/internal/authstore"
	"hal9001/internal/httpsec"
	"hal9001/internal/menubar"
	"hal9001/internal/notify"
	"hal9001/internal/sandbox"
	"hal9001/internal/spec"
	"hal9001/internal/store"
	"hal9001/internal/tools"
	"hal9001/internal/webui"
)

// ListenAddr is the only address the daemon serves on (REQUIREMENTS §10.3).
// It is not configurable; Options.ListenAddr exists for tests.
const ListenAddr = "127.0.0.1:9001"

const (
	httpShutdownTimeout   = 10 * time.Second
	engineShutdownTimeout = 30 * time.Second
)

// Options configures Run.
type Options struct {
	// Home is the hal9001 home directory (required).
	Home string
	// Version is reported by the API.
	Version string
	// WebDir serves the UI from this directory instead of the embedded or
	// ./web/dist one.
	WebDir string
	// BasePATH is the PATH handed to node processes; default: this process's PATH
	// (under launchd, the login-shell PATH captured by `hal9001 install`).
	BasePATH string
	// UIURL is the origin notification deep links point at. Empty: $HAL9001_UI_URL,
	// else http://localhost:<port> (the daemon's own port, 9001 by default).
	UIURL string
	// Log receives operational logs; default slog.Default().
	Log *slog.Logger

	// AssistIdleTimeout suspends the agent process of an assist session after this long without a turn
	// (default 15m). Tests shorten it.
	AssistIdleTimeout time.Duration

	// ListenAddr overrides ListenAddr. Tests only (e.g. 127.0.0.1:0).
	ListenAddr string
	// Clock drives the engine's sensor schedules and agent timeouts; nil means the wall clock. Tests only.
	Clock agent.Clock
	// Executable is the hal9001 binary agents spawn as their `hal9001 mcp` server; default: os.Executable. Tests only
	// (an in-process daemon's executable is the test binary).
	Executable string
	// Ready, when set, is called once the daemon is serving, with the bound address. Tests only.
	Ready func(addr net.Addr)
}

// Run starts the daemon and blocks until ctx is done or serving fails, then
// shuts down gracefully: HTTP server first, then the engine, then the store.
func Run(ctx context.Context, opts Options) error {
	if opts.Home == "" {
		return errors.New("daemon: Home is required")
	}
	home, err := filepath.Abs(opts.Home)
	if err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	addr := opts.ListenAddr
	if addr == "" {
		addr = ListenAddr
	}

	// Bind first so a second instance fails before it touches the database.
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("daemon: listen on %s (is another hal9001 running?): %w", addr, err)
	}
	d := &daemon{home: home, log: log}
	serving := false
	defer func() {
		if !serving {
			_ = ln.Close()
		}
	}()

	created, err := spec.Init(home, content.FS)
	if err != nil {
		return err
	}
	if len(created) > 0 {
		log.Info("seeded home directory", "home", home, "files", len(created))
	}
	ws := spec.Load(home)
	if spec.HasErrors(ws.Config.Diagnostics) {
		return configError(ws)
	}
	logDiagnostics(log, ws)

	st, err := store.Open(filepath.Join(home, filepath.FromSlash(spec.FileDB)),
		store.WithBlobDir(filepath.Join(home, filepath.FromSlash(spec.DirBlobs))))
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("daemon: listener address %s is not TCP", ln.Addr())
	}
	port := tcp.Port
	basePATH := opts.BasePATH
	if basePATH == "" {
		basePATH = os.Getenv("PATH")
	}
	sinks := &notify.Sinks{}
	notifier := notify.NewRouter(notify.RouterOptions{
		Log: st, Sinks: sinks, Logger: log,
		Via:      func() string { return d.effective(d.workspace()).Config.Value.NotifyVia },
		Terminal: notify.New(notify.WithLogger(log), notify.WithIcon(writeNotifyIcon(home, log))),
	})
	eng, err := newEngine(engineParams{
		Store: st, Home: home, BasePATH: basePATH, Log: log,
		Notifier:  notifier,
		UIBaseURL: uiBaseURL(opts.UIURL, os.Getenv(EnvUIURL), port),
		Clock:     opts.Clock,
		MCPURL:    "http://127.0.0.1:" + strconv.Itoa(port), Executable: opts.Executable,
	})
	if err != nil {
		return fmt.Errorf("daemon: engine: %w", err)
	}
	d.engine = eng

	mgr, err := auth.New(auth.Options{Config: AuthConfig(ws.Config.Value), Store: authstore.New(st), Logger: log})
	if err != nil {
		return fmt.Errorf("daemon: auth: %w", err)
	}
	d.auth = mgr
	d.lastGoodConfig = ws.Config
	d.ws.Store(ws)

	// Sandbox manager: one backend per sandbox config, rebuilt on reload; sweeps failed per_run workspaces.
	d.sandboxes = sandbox.NewManager(sandbox.ManagerOptions{
		Home: home, Log: log,
		RetentionDays: func() int { return d.effective(d.workspace()).Config.Value.RetentionDays },
	})
	defer d.sandboxes.Close()
	eng.ToolHub().AddProvider(tools.SandboxProvider(d.sandboxes, tools.SandboxOptions{}))

	ui, err := webui.New(webui.Options{Dir: opts.WebDir})
	if err != nil {
		return err
	}
	if ui.Available() {
		log.Info("serving web UI", "source", ui.Source())
	} else {
		log.Warn("web UI not built; only the API is served (run `mise run build`)")
	}

	// Assist manager: it runs agent turns on the engine's lifetime, so it is shut down after the engine
	// (defers run last in, first out).
	assistMgr, err := assist.New(assist.Options{
		Store: st, Home: home, Host: eng.AssistHost(), Log: log,
		URL: "http://127.0.0.1:" + strconv.Itoa(port), IdleTimeout: opts.AssistIdleTimeout, Executable: opts.Executable,
	})
	if err != nil {
		return fmt.Errorf("daemon: assist: %w", err)
	}
	defer assistMgr.Shutdown()
	eng.SetToolApprovalObserver(assistMgr)
	if err := assistMgr.Start(); err != nil {
		return fmt.Errorf("daemon: start assist: %w", err)
	}

	// Engine: Apply before Start so schedulers know the sensors.
	eng.Apply(d.effective(ws))
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := eng.Start(runCtx); err != nil {
		return fmt.Errorf("daemon: start engine: %w", err)
	}
	d.sandboxes.Start(runCtx)
	engineRunning := true
	defer func() {
		if engineRunning {
			stopCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), engineShutdownTimeout)
			defer stop()
			if err := eng.Stop(stopCtx); err != nil {
				log.Error("engine stop", "err", err)
			}
		}
	}()

	apiHandler := newAPI(apiParams{
		Store: st, Home: home, Engine: eng, Assist: assistMgr, Auth: mgr, MCP: eng.ToolHub(),
		Workspace: d.workspace, Reload: d.reload, BasePATH: basePATH,
		Version: opts.Version, UI: ui, Log: log, Sinks: sinks,
	})
	// The API server announces on-disk edits to SSE clients and owns the
	// stream hub, which must be closed on shutdown.
	apiServer, _ := apiHandler.(interface {
		PublishConfigChange(paths []string)
		Close()
	})
	if apiServer != nil {
		defer apiServer.Close()
	}
	handler := httpsec.Middleware(httpsec.Options{
		Port:         port,
		AllowedHosts: func() []string { return d.workspace().Config.Value.AllowedHosts },
	})(apiHandler)

	// Handlers see baseCtx cancelled when shutdown starts, which ends SSE
	// streams and long-polls so Shutdown does not wait for them.
	baseCtx, cancelBase := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelBase()
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	changes, err := spec.Watch(runCtx, home)
	if err != nil {
		return err
	}
	var watchDone sync.WaitGroup
	watchDone.Add(1)
	go func() {
		defer watchDone.Done()
		for rl := range changes {
			log.Info("config changed; reloading", "paths", rl.Paths)
			d.reload()
			if apiServer != nil {
				apiServer.PublishConfigChange(rl.Paths)
			}
		}
	}()

	serveErr := make(chan error, 1)
	serving = true
	go func() { serveErr <- srv.Serve(ln) }()
	log.Info("hal9001 listening", "addr", ln.Addr().String(), "home", home, "version", opts.Version)
	if opts.Ready != nil {
		opts.Ready(ln.Addr())
	}

	var runErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-serveErr:
		runErr = fmt.Errorf("daemon: serve: %w", err)
	}

	cancelBase()
	shutCtx, shutCancel := context.WithTimeout(context.WithoutCancel(ctx), httpShutdownTimeout)
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Warn("http shutdown", "err", err)
		_ = srv.Close()
	}
	shutCancel()
	cancel()
	watchDone.Wait()

	stopCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), engineShutdownTimeout)
	defer stop()
	engineRunning = false
	if err := eng.Stop(stopCtx); err != nil {
		log.Error("engine stop", "err", err)
		runErr = errors.Join(runErr, fmt.Errorf("daemon: stop engine: %w", err))
	}
	return runErr
}

// daemon holds the state shared between the reload path and the HTTP handlers.
type daemon struct {
	home   string
	log    *slog.Logger
	engine runtimeEngine
	auth   *auth.Manager
	// sandboxes is set by Run; tests that build a daemon directly leave it nil.
	sandboxes *sandbox.Manager

	ws atomic.Pointer[spec.Workspace]

	mu             sync.Mutex // serializes reload
	lastGoodConfig spec.File[spec.Config]
}

func (d *daemon) workspace() *spec.Workspace { return d.ws.Load() }

// reload re-reads the home dir synchronously and applies it to the engine and
// auth manager. It is the file watcher's callback and the API's Reload hook.
//
// A config.yaml with errors does not replace the running configuration: the
// previous valid one stays in effect for the engine and auth (so a typo can
// never relax the auth stacks), while the API still serves the broken file
// with its diagnostics.
func (d *daemon) reload() {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws := spec.Load(d.home)
	if spec.HasErrors(ws.Config.Diagnostics) {
		d.log.Error("config.yaml has errors; keeping the previous configuration", "errors", errorMessages(ws.Config.Diagnostics))
	} else {
		d.lastGoodConfig = ws.Config
	}
	d.ws.Store(ws)
	if d.sandboxes != nil {
		d.sandboxes.Reload()
	}
	eff := d.effective(ws)
	d.engine.Apply(eff)
	if err := d.auth.Reload(AuthConfig(eff.Config.Value)); err != nil {
		d.log.Error("auth config rejected; keeping the previous one", "err", err)
	}
}

// effective returns ws with the last valid config substituted when ws's own
// config.yaml has errors.
func (d *daemon) effective(ws *spec.Workspace) *spec.Workspace {
	if !spec.HasErrors(ws.Config.Diagnostics) {
		return ws
	}
	eff := *ws
	eff.Config = d.lastGoodConfig
	return &eff
}

func configError(ws *spec.Workspace) error {
	return fmt.Errorf("daemon: %s has errors, refusing to start (fix it, or check with `hal9001 validate`): %v",
		spec.FileConfig, errorMessages(ws.Config.Diagnostics))
}

func errorMessages(ds []spec.Diagnostic) []string {
	var out []string
	for _, d := range ds {
		if d.Severity == spec.SeverityError {
			out = append(out, d.String())
		}
	}
	return out
}

func logDiagnostics(log *slog.Logger, ws *spec.Workspace) {
	var errs, warns int
	for _, d := range ws.AllDiagnostics() {
		switch d.Severity {
		case spec.SeverityError:
			errs++
			log.Error("workspace diagnostic", "diagnostic", d.String())
		case spec.SeverityWarning:
			warns++
		}
	}
	if errs > 0 || warns > 0 {
		log.Warn("workspace loaded with findings", "errors", errs, "warnings", warns)
	}
}

// EnvUIURL overrides the origin of notification deep links, e.g. the Vite dev
// server (http://localhost:5173) while developing.
const EnvUIURL = "HAL9001_UI_URL"

// uiBaseURL picks the notification link origin: the option, then the
// environment value, then the daemon's own address.
func uiBaseURL(opt, env string, port int) string {
	for _, v := range []string{opt, env} {
		if v = strings.TrimSpace(v); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return "http://localhost:" + strconv.Itoa(port)
}

// notifyIconFile is the HAL eye PNG terminal-notifier shows in notifications, under <home>/data.
const notifyIconFile = "hal-eye.png"

// writeNotifyIcon renders the menu-bar eye at notification size into the data directory and returns its path, or ""
// when it cannot be written (notifications then go without an image).
func writeNotifyIcon(home string, log *slog.Logger) string {
	dir := filepath.Join(home, spec.DirData)
	path := filepath.Join(dir, notifyIconFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Warn("notify icon", "err", err)
		return ""
	}
	if err := os.WriteFile(path, menubar.Icon(256, false), 0o600); err != nil {
		log.Warn("notify icon", "err", err)
		return ""
	}
	return path
}

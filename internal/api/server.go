// Package api serves the REST contract of api/openapi.yaml.
//
// Routes are registered from the contract itself: New walks the operations of
// the embedded OpenAPI document, looks each one up by operationId in a
// handler table and mounts it on a net/http ServeMux with the auth service the
// operation declares (x-hal-service) unless it is x-hal-public. A contract
// operation without a handler panics at construction, so the contract and the
// server cannot drift apart. The /api/auth/* operations are mounted by
// auth.Manager.Routes.
//
// Reads come straight from *store.Store. Sensors, flows, prompts, env and
// settings are files under the home directory, read and written through
// internal/spec. Anything that executes or changes run state goes through
// core.Engine. The daemon wraps the handler with httpsec.Middleware.
package api

import (
	"log/slog"
	"net/http"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"hal9001/internal/apispec"
	"hal9001/internal/auth"
	"hal9001/internal/core"
	"hal9001/internal/httpsec"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// Options configures New.
type Options struct {
	Store *store.Store
	// Home is the hal9001 home directory the daemon serves.
	Home   string
	Engine core.Engine
	// Assist serves the AI-assisted authoring endpoints that start agent turns or
	// run drafts. Optional: without it those endpoints answer 503 unavailable.
	Assist core.Assist
	Auth   *auth.Manager
	// Workspace returns the currently loaded configuration.
	Workspace func() *spec.Workspace
	// Reload re-reads the home directory and applies it, synchronously. The
	// API calls it after every file write so the engine sees the change
	// before the response is sent. May be nil when nothing needs to react.
	Reload  func()
	Version string
	// UI serves the web UI for "/", "/assets/*" and every other GET outside
	// /api/. Nil answers those routes with a problem saying the UI is not built.
	UI  http.Handler
	Log *slog.Logger
}

// Server is the http.Handler New returns. Besides serving requests it has two
// methods for the daemon: PublishConfigChange (feed it the hot-reload watcher's
// paths so SSE clients see edits made on disk) and Close.
type Server struct {
	opts    Options
	mux     *http.ServeMux
	started time.Time

	bus *changeBus
	hub *hub

	prevNotifier func(store.Change)
	closeOnce    sync.Once

	// routes are the "METHOD pattern" strings registered from the contract
	// (excluding /api/auth/*, which auth.Manager mounts).
	routes []string

	probeMu sync.Mutex
	probes  map[string]probeResultView

	// heartbeat is the SSE keepalive interval.
	heartbeat time.Duration
}

// New builds the API. It panics if a required option is missing or if the
// contract has an operation without a handler.
func New(opts Options) http.Handler {
	if opts.Store == nil || opts.Engine == nil || opts.Auth == nil || opts.Workspace == nil {
		panic("api: Store, Engine, Auth and Workspace are required")
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	s := &Server{
		opts: opts, mux: http.NewServeMux(), started: time.Now().UTC(), bus: newChangeBus(),
		probes: map[string]probeResultView{}, heartbeat: heartbeatInterval,
	}
	s.mountContract()
	opts.Auth.Routes(s.mux, httpsec.DefaultPort)
	s.mux.HandleFunc("/", s.fallback)

	s.hub = newHub(s)
	s.prevNotifier = opts.Store.Notifier()
	prev := s.prevNotifier
	opts.Store.SetNotifier(func(c store.Change) {
		if prev != nil {
			prev(c)
		}
		s.bus.publish(c)
		s.hub.enqueue(c)
	})
	go s.hub.run()
	return s
}

func (s *Server) log() *slog.Logger { return s.opts.Log }

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// Close stops the SSE hub, ends open streams and restores the store notifier
// that was installed before New.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		s.opts.Store.SetNotifier(s.prevNotifier)
		s.hub.stop()
	})
}

// mountContract registers every non-auth operation of the contract.
func (s *Server) mountContract() {
	table := s.handlers()
	doc := apispec.MustLoad()
	seen := map[string]bool{}
	for path, item := range doc.Paths.Map() {
		if strings.HasPrefix(path, "/api/auth/") {
			continue
		}
		for method, op := range item.Operations() {
			h, ok := table[op.OperationID]
			if !ok {
				panic("api: contract operation " + op.OperationID + " (" + method + " " + path + ") has no handler")
			}
			seen[op.OperationID] = true
			svc, _ := op.Extensions[apispec.ExtService].(string)
			public, _ := op.Extensions[apispec.ExtPublic].(bool)
			var handler http.Handler = h
			if !public {
				handler = s.opts.Auth.Require(auth.Service(svc))(h)
			}
			pattern := method + " " + muxPath(path)
			s.mux.Handle(pattern, handler)
			s.routes = append(s.routes, pattern)
		}
	}
	for id := range table {
		if !seen[id] {
			panic("api: handler " + id + " is not an operation of the contract")
		}
	}
	sort.Strings(s.routes)
}

// muxPath converts a contract path to a ServeMux pattern path.
func muxPath(path string) string {
	switch {
	case path == "/":
		return "/{$}"
	case strings.HasSuffix(path, "/{path}"):
		return strings.TrimSuffix(path, "}") + "...}"
	}
	return path
}

// fallback serves everything no contract operation matched: the web UI for
// GET and HEAD outside /api/, a problem otherwise. Like getUIShell and
// getUIAsset it is public: the login page lives in the shell, so a stack that
// does not admit the request must still be able to load it.
func (s *Server) fallback(w http.ResponseWriter, r *http.Request) {
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && !strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api" {
		s.serveUI(w, r)
		return
	}
	notFound(w, "no route for %s %s", r.Method, r.URL.Path)
}

func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	if s.opts.UI == nil {
		notFound(w, "the web UI is not built into this binary")
		return
	}
	s.opts.UI.ServeHTTP(w, r)
}

// handlers maps operationId to its handler.
func (s *Server) handlers() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"getUIShell": s.serveUI,
		"getUIAsset": s.serveUI,

		"getHealth":    s.getHealth,
		"getVersion":   s.getVersion,
		"getStatus":    s.getStatus,
		"streamEvents": s.streamEvents,
		"getBlob":      s.getBlob,

		"getKillSwitch":    s.getKillSwitch,
		"pauseKillSwitch":  s.pauseKillSwitch,
		"resumeKillSwitch": s.resumeKillSwitch,

		"getQueue":        s.getQueue,
		"clearQueue":      s.clearQueue,
		"removeFromQueue": s.removeFromQueue,

		"listSensors":     s.listSensors,
		"createSensor":    s.createSensor,
		"validateSensor":  s.validateSensor,
		"getSensor":       s.getSensor,
		"updateSensor":    s.updateSensor,
		"deleteSensor":    s.deleteSensor,
		"pollSensor":      s.pollSensor,
		"resetSensor":     s.resetSensor,
		"pauseSensor":     s.pauseSensor,
		"resumeSensor":    s.resumeSensor,
		"listSensorEnv":   s.listSensorEnv,
		"setSensorEnv":    s.setSensorEnvValue,
		"deleteSensorEnv": s.deleteSensorEnvValue,
		"listSensorRuns":  s.listSensorRuns,
		"getSensorRun":    s.getSensorRun,

		"listEvents":  s.listEvents,
		"injectEvent": s.injectEvent,
		"getEvent":    s.getEvent,
		"replayEvent": s.replayEvent,

		"listFlows":           s.listFlows,
		"createFlow":          s.createFlow,
		"validateFlow":        s.validateFlow,
		"getFlow":             s.getFlow,
		"updateFlow":          s.updateFlow,
		"deleteFlow":          s.deleteFlow,
		"testFlowNode":        s.testFlowNode,
		"runFlow":             s.runFlow,
		"listFlowNodeSamples": s.listFlowNodeSamples,
		"previewTransform":    s.previewTransform,
		"compileTransform":    s.compileTransform,
		"listFlowVersions":    s.listFlowVersions,
		"getFlowVersion":      s.getFlowVersion,

		"listFlowRuns": s.listFlowRuns,
		"getFlowRun":   s.getFlowRun,
		"getNodeRun":   s.getNodeRun,

		"listAgentRuns":               s.listAgentRuns,
		"getAgentRun":                 s.getAgentRun,
		"listAgentEvents":             s.listAgentEvents,
		"listAgentPermissionRequests": s.listAgentPermissionRequests,
		"downloadAgentRunRaw":         s.downloadAgentRunRaw,

		"listApprovals":   s.listApprovals,
		"getApproval":     s.getApproval,
		"approveApproval": s.approveApproval,
		"denyApproval":    s.denyApproval,

		"listInbox":       s.listInbox,
		"getInboxItem":    s.getInboxItem,
		"updateInboxItem": s.updateInboxItem,
		"bulkInbox":       s.bulkInbox,

		"listPrompts":  s.listPrompts,
		"createPrompt": s.createPrompt,
		"renderPrompt": s.renderPrompt,
		"getPrompt":    s.getPrompt,
		"updatePrompt": s.updatePrompt,
		"deletePrompt": s.deletePrompt,

		"listAssistSessions":  s.listAssistSessions,
		"createAssistSession": s.createAssistSession,
		"getAssistSession":    s.getAssistSession,
		"deleteAssistSession": s.deleteAssistSession,
		"sendAssistMessage":   s.sendAssistMessage,
		"updateAssistDraft":   s.updateAssistDraft,
		"testAssistDraft":     s.testAssistDraft,
		"listAssistSamples":   s.listAssistSamples,
		"cancelAssistSession": s.cancelAssistSession,

		"listRuntimes": s.listRuntimes,
		"probeRuntime": s.probeRuntime,

		"getSettings":    s.getSettings,
		"updateSettings": s.updateSettings,
		"listEnvKeys":    s.listEnvKeys,
		"setEnvValue":    s.setEnvValue,
		"deleteEnvValue": s.deleteEnvValue,
	}
}

// principal is the authenticated caller of a request that passed Require.
func principal(r *http.Request) string {
	if id, ok := auth.IdentityFrom(r.Context()); ok {
		return id.Principal.Name
	}
	return ""
}

// ---- meta handlers ----

func (s *Server) getHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.opts.Version})
}

func (s *Server) getVersion(w http.ResponseWriter, _ *http.Request) {
	commit, built := "unknown", "unknown"
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, kv := range bi.Settings {
			switch kv.Key {
			case "vcs.revision":
				commit = kv.Value
			case "vcs.time":
				built = kv.Value
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": s.opts.Version, "commit": commit, "build_time": built, "go_version": runtime.Version(),
		"started_at": s.started, "home": s.opts.Home,
	})
}

func (s *Server) getStatus(w http.ResponseWriter, r *http.Request) {
	v, err := s.statusView(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// refs returns the references flows may name, from the loaded workspace.
func (s *Server) refs() *spec.Refs { return s.opts.Workspace().Refs() }

// reload asks the daemon to re-read the home directory.
func (s *Server) reload() {
	if s.opts.Reload != nil {
		s.opts.Reload()
	}
}

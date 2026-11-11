package api

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sort"

	"hal9001/internal/agent"
	"hal9001/internal/runtimes"
	"hal9001/internal/spec"
)

type runtimeView struct {
	Name string `json:"name"`
	// Kind is the runtime family (the configured kind, else the one the command names); null for another agent.
	Kind      *string            `json:"kind"`
	Command   []string           `json:"command"`
	Modes     map[string]string  `json:"modes"`
	EnvKeys   []string           `json:"env_keys"`
	LastProbe *probeResultView   `json:"last_probe"`
	Detection runtimes.Detection `json:"detection"`
}

// adapterProgressView is the data of the `runtime.adapter` stream event.
type adapterProgressView struct {
	Name  string  `json:"name"`
	Phase string  `json:"phase"`
	Line  *string `json:"line"`
	Error *string `json:"error"`
}

// newRuntimes returns the runtime manager the server uses when Options has none.
func (s *Server) newRuntimes() *runtimes.Manager {
	return runtimes.New(runtimes.Options{
		Data: filepath.Join(s.opts.Home, "data"), Path: s.opts.BasePATH, Log: s.opts.Log,
		EnvFile: func() map[string]string {
			if ws := s.opts.Workspace(); ws != nil && ws.Env.Value != nil {
				return ws.Env.Value.Vars
			}
			return nil
		},
	})
}

// subscribeRuntimes forwards adapter install progress to the stream hub.
func (s *Server) subscribeRuntimes() {
	s.unsubRuntimes = s.rt.Subscribe(func(ev runtimes.Event) {
		s.hub.publish("runtime.adapter", adapterProgressView{
			Name: ev.Name, Phase: ev.Phase, Line: nonEmpty(ev.Line), Error: nonEmpty(ev.Error),
		}, nil, false)
	})
}

func (s *Server) configuredRuntimes() map[string]spec.Runtime {
	if ws := s.opts.Workspace(); ws != nil && ws.Config.Value != nil {
		return ws.Config.Value.Runtimes
	}
	return nil
}

// runtimeItem builds the API view of one runtime. force makes the detection start from scratch.
func (s *Server) runtimeItem(ctx context.Context, name string, rt spec.Runtime, force bool) runtimeView {
	v := runtimeView{Name: name, Command: nonNil(rt.Command), Modes: map[string]string{}, EnvKeys: []string{}}
	if k := kindName(rt); k != "" {
		v.Kind = &k
	}
	if rt.Modes.Ask != "" {
		v.Modes["ask"] = rt.Modes.Ask
	}
	if rt.Modes.Full != "" {
		v.Modes["full"] = rt.Modes.Full
	}
	for k := range rt.Env {
		v.EnvKeys = append(v.EnvKeys, k)
	}
	sort.Strings(v.EnvKeys)
	s.probeMu.Lock()
	if p, ok := s.probes[name]; ok {
		v.LastProbe = &p
	}
	s.probeMu.Unlock()
	v.Detection = s.rt.Detect(ctx, name, rt, force)
	return v
}

func kindName(rt spec.Runtime) string {
	if rt.Kind != "" {
		return rt.Kind
	}
	return agent.DetectKind(rt.Command)
}

// runtimeList detects every configured runtime in parallel. Detection outlives the request that asked for it, so a
// client that goes away does not leave a half-finished check in the cache.
func (s *Server) runtimeList(ctx context.Context, force bool) []runtimeView {
	rts := s.configuredRuntimes()
	names := make([]string, 0, len(rts))
	for name := range rts {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]runtimeView, len(names))
	done := make(chan struct{}, len(names))
	ctx = context.WithoutCancel(ctx)
	if force {
		s.rt.Refresh()
	}
	for i, name := range names {
		go func() {
			items[i] = s.runtimeItem(ctx, name, rts[name], false)
			done <- struct{}{}
		}()
	}
	for range names {
		<-done
	}
	return items
}

func (s *Server) listRuntimes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		Items []runtimeView `json:"items"`
	}{Items: s.runtimeList(r.Context(), false)})
}

func (s *Server) detectRuntimes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		Items []runtimeView `json:"items"`
	}{Items: s.runtimeList(r.Context(), true)})
}

func (s *Server) installRuntimeAdapter(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	rt, ok := s.configuredRuntimes()[name]
	if !ok {
		notFound(w, "no runtime %q", name)
		return
	}
	ctx := context.WithoutCancel(r.Context())
	if err := s.rt.Install(ctx, name, rt); err != nil {
		s.adapterError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, s.runtimeItem(ctx, name, rt, false))
}

func (s *Server) uninstallRuntimeAdapter(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	rt, ok := s.configuredRuntimes()[name]
	if !ok {
		notFound(w, "no runtime %q", name)
		return
	}
	if err := s.rt.Uninstall(name, rt); err != nil {
		s.adapterError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.runtimeItem(context.WithoutCancel(r.Context()), name, rt, true))
}

func (s *Server) adapterError(w http.ResponseWriter, err error) {
	var pre *runtimes.PrerequisiteError
	switch {
	case errors.As(err, &pre):
		problem(w, http.StatusUnprocessableEntity, typeValidation, "Prerequisite missing", pre.Message, map[string]any{
			"code":        pre.Code,
			"diagnostics": []spec.Diagnostic{{Severity: spec.SeverityError, Code: spec.CodeInvalidValue, Path: "runtime", Message: pre.Message}},
		})
	case errors.Is(err, runtimes.ErrNotInstallable):
		conflictProblem(w, typeInvalidState, err.Error(), nil)
	case errors.Is(err, runtimes.ErrBusy):
		conflictProblem(w, typeInvalidState, err.Error(), nil)
	default:
		s.log().Error("api: runtime adapter", "err", err)
		problem(w, http.StatusInternalServerError, typeInternalServer, "Internal server error", "the adapter operation failed", nil)
	}
}

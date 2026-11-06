package api

import (
	"errors"
	"net/http"
	"slices"
	"sort"

	"hal9001/internal/sandbox"
	"hal9001/internal/spec"
)

// ---- sandbox backends (sandboxes/<backend>.yaml, env.d/sandboxes/<backend>.env) ----

type sandboxDetail struct {
	Backend     string            `json:"backend"`
	Configured  bool              `json:"configured"`
	Hash        string            `json:"hash"`
	Config      spec.Sandbox      `json:"config"`
	Diagnostics []spec.Diagnostic `json:"diagnostics"`
}

// sandboxBackend returns the {backend} path value, answering 404 for an unknown name.
func sandboxBackend(w http.ResponseWriter, r *http.Request) (string, bool) {
	b := r.PathValue("backend")
	if !spec.ValidSandboxBackend(b) {
		notFound(w, "no sandbox backend %q (use %v)", b, spec.SandboxBackends)
		return "", false
	}
	return b, true
}

// sandboxFile reads a backend's config. A missing file gives the defaults, the hash of empty content and
// exists=false. A file that does not parse or does not fit the contract also gives the defaults; the
// diagnostics say what is wrong.
func (s *Server) sandboxFile(backend string) (detail sandboxDetail, err error) {
	raw, hash, err := spec.ReadFile(s.opts.Home, spec.KindSandbox, backend)
	exists := true
	if errors.Is(err, spec.ErrNotFound) {
		raw, hash, exists, err = nil, spec.Hash(nil), false, nil
	}
	if err != nil {
		return detail, err
	}
	detail = sandboxDetail{Backend: backend, Configured: exists, Hash: hash, Diagnostics: []spec.Diagnostic{}}
	cfg, ds := spec.CheckSandboxIn(s.opts.Home, backend, raw)
	if ds != nil {
		detail.Diagnostics = ds
	}
	if cfg == nil || !conforms("Sandbox", cfg) {
		def := spec.DefaultSandbox(backend)
		cfg = &def
	}
	detail.Config = *cfg
	return detail, nil
}

func (s *Server) listSandboxes(w http.ResponseWriter, r *http.Request) {
	out := struct {
		Items []map[string]any `json:"items"`
	}{Items: []map[string]any{}}
	for _, b := range spec.SandboxBackends {
		d, err := s.sandboxFile(b)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		out.Items = append(out.Items, map[string]any{
			"backend": b, "configured": d.Configured, "enabled": d.Config.Enabled, "lifecycle": d.Config.Lifecycle,
			"approval": d.Config.Approval, "valid": !spec.HasErrors(d.Diagnostics),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getSandbox(w http.ResponseWriter, r *http.Request) {
	backend, ok := sandboxBackend(w, r)
	if !ok {
		return
	}
	d, err := s.sandboxFile(backend)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) updateSandbox(w http.ResponseWriter, r *http.Request) {
	backend, ok := sandboxBackend(w, r)
	if !ok {
		return
	}
	var req struct {
		ExpectedHash string       `json:"expected_hash"`
		Config       spec.Sandbox `json:"config"`
	}
	req.Config = spec.DefaultSandbox(backend)
	if !decodeJSON(w, r, &req, false) || !expectedHashOK(w, req.ExpectedHash) {
		return
	}
	if backend == spec.SandboxLocal {
		req.Config.Enabled = true // the local sandbox has no off switch
	}
	raw, err := spec.MarshalSandbox(req.Config)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	_, ds := spec.CheckSandboxIn(s.opts.Home, backend, raw)
	if spec.HasErrors(ds) {
		validationProblem(w, "the sandbox config has errors", ds)
		return
	}
	cur, err := s.sandboxFile(backend)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if req.ExpectedHash != cur.Hash {
		fileConflict(w, spec.KindSandbox.Rel(backend)+" changed since you loaded it", req.ExpectedHash, cur.Hash)
		return
	}
	expected := cur.Hash
	if !cur.Configured {
		expected = ""
	}
	if _, err := spec.WriteFile(s.opts.Home, spec.KindSandbox, backend, raw, expected); err != nil {
		if errors.Is(err, spec.ErrConflict) {
			now, _ := s.sandboxFile(backend)
			fileConflict(w, err.Error(), req.ExpectedHash, now.Hash)
			return
		}
		s.writeError(w, r, err)
		return
	}
	s.reload()
	s.publishFileChange(spec.KindSandbox, backend)
	d, err := s.sandboxFile(backend)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) checkSandbox(w http.ResponseWriter, r *http.Request) {
	backend, ok := sandboxBackend(w, r)
	if !ok {
		return
	}
	var req struct {
		Config *spec.Sandbox `json:"config"`
	}
	if !decodeJSON(w, r, &req, true) {
		return
	}
	var cfg spec.Sandbox
	if req.Config != nil {
		cfg = *req.Config
	} else {
		d, err := s.sandboxFile(backend)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		cfg = d.Config
	}
	// A check needs the backend's fields whether or not it is enabled yet.
	probe := cfg
	probe.Enabled = true
	ds := append(spec.ValidateSandbox(backend, &probe), spec.ValidateSandboxCredentials(s.opts.Home, backend, &probe)...)
	if spec.HasErrors(ds) {
		validationProblem(w, "the sandbox config has errors", ds)
		return
	}
	env, err := sandbox.LoadEnv(s.opts.Home, backend)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	res := sandbox.Check(r.Context(), backend, probe, env, s.opts.Home)
	var fp *string
	if res.Fingerprint != "" {
		fp = &res.Fingerprint
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": res.OK, "message": res.Message, "fingerprint": fp})
}

func (s *Server) listSandboxEnv(w http.ResponseWriter, r *http.Request) {
	backend, ok := sandboxBackend(w, r)
	if !ok {
		return
	}
	raw, _, err := s.envFile(spec.KindSandboxEnv, backend)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	own, _ := spec.ParseEnv(raw)
	graw, _, err := s.envFile(spec.KindEnv, "")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	global, _ := spec.ParseEnv(graw)
	keys := slices.Clone(own.Keys)
	sort.Strings(keys)
	type item struct {
		envItem
		OverridesGlobal bool `json:"overrides_global"`
	}
	out := struct {
		Items []item `json:"items"`
	}{Items: []item{}}
	for _, k := range keys {
		_, ok := global.Vars[k]
		out.Items = append(out.Items, item{envItem: newEnvItem(own, k), OverridesGlobal: ok})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) setSandboxEnvValue(w http.ResponseWriter, r *http.Request) {
	backend, ok := sandboxBackend(w, r)
	if !ok {
		return
	}
	s.setEnv(w, r, spec.KindSandboxEnv, backend)
}

func (s *Server) deleteSandboxEnvValue(w http.ResponseWriter, r *http.Request) {
	backend, ok := sandboxBackend(w, r)
	if !ok {
		return
	}
	s.deleteEnv(w, r, spec.KindSandboxEnv, backend)
}

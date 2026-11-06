package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"hal9001/internal/core"
	"hal9001/internal/runner"
	"hal9001/internal/spec"
	"hal9001/internal/tools"
)

// Custom tool endpoints: files under tools/ with the same CRUD, validation and
// per-owner env handling as sensors, plus a test run.

type toolSummaryView struct {
	ID          string            `json:"id"`
	Path        string            `json:"path"`
	Hash        string            `json:"hash"`
	Tool        *spec.Tool        `json:"tool"`
	Schema      map[string]any    `json:"schema"`
	Diagnostics []spec.Diagnostic `json:"diagnostics"`
}

type toolDetailView struct {
	toolSummaryView
	YAML string `json:"yaml"`
}

// toolValue returns t for the API, or nil when it would not match the Tool schema.
func toolValue(t *spec.Tool) *spec.Tool {
	if t == nil || !conforms("Tool", t) {
		return nil
	}
	return t
}

func toolSummary(f *spec.File[spec.Tool]) toolSummaryView {
	v := toolSummaryView{ID: f.Name, Path: f.Path, Hash: f.Hash, Tool: toolValue(f.Value), Diagnostics: diagnostics(f.Diagnostics)}
	if v.Tool != nil {
		v.Schema = v.Tool.EffectiveSchema()
	}
	return v
}

func (s *Server) listTools(w http.ResponseWriter, _ *http.Request) {
	files := slices.Clone(s.opts.Workspace().Tools)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	out := struct {
		Items []toolSummaryView `json:"items"`
	}{Items: []toolSummaryView{}}
	for _, f := range files {
		out.Items = append(out.Items, toolSummary(f))
	}
	writeJSON(w, http.StatusOK, out)
}

// toolDetail reads tools/<id>.yaml from disk.
func (s *Server) toolDetail(id string) (toolDetailView, error) {
	raw, hash, err := s.readFile(spec.KindTool, id)
	if err != nil {
		return toolDetailView{}, err
	}
	tool, ds := spec.CheckTool(id, raw)
	f := &spec.File[spec.Tool]{Name: id, Path: spec.KindTool.Rel(id), Hash: hash, Value: tool, Diagnostics: ds}
	return toolDetailView{toolSummaryView: toolSummary(f), YAML: string(raw)}, nil
}

func (s *Server) getTool(w http.ResponseWriter, r *http.Request) {
	v, err := s.toolDetail(r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// toolInput turns a request's yaml or tool member into file bytes.
func toolInput(w http.ResponseWriter, yamlText *string, tool *spec.Tool) ([]byte, bool) {
	if !oneOf(w, yamlText != nil, tool != nil, "yaml", "tool") {
		return nil, false
	}
	if yamlText != nil {
		return []byte(*yamlText), true
	}
	raw, err := spec.MarshalTool(*tool)
	if err != nil {
		badRequest(w, "cannot render the tool as YAML: %s", err.Error())
		return nil, false
	}
	return raw, true
}

func (s *Server) createTool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		YAML *string    `json:"yaml"`
		Tool *spec.Tool `json:"tool"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	raw, ok := toolInput(w, req.YAML, req.Tool)
	if !ok {
		return
	}
	parsed, ds := spec.ParseTool(raw)
	if parsed == nil {
		validationProblem(w, "the tool does not parse", ds)
		return
	}
	id := parsed.ID
	if !checkID(w, id) {
		return
	}
	_, ds = spec.CheckTool(id, raw)
	if !s.saveFile(w, r, spec.KindTool, id, raw, "", ds) {
		return
	}
	if s.opts.Assist != nil {
		if err := s.opts.Assist.ToolCreated(r.Context(), id); err != nil {
			s.log().Error("api: attach assist sessions to the new tool", "tool", id, "err", err)
		}
	}
	v, err := s.toolDetail(id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/tools/"+id)
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) updateTool(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		ExpectedHash string     `json:"expected_hash"`
		YAML         *string    `json:"yaml"`
		Tool         *spec.Tool `json:"tool"`
	}
	if !decodeJSON(w, r, &req, false) || !expectedHashOK(w, req.ExpectedHash) {
		return
	}
	raw, ok := toolInput(w, req.YAML, req.Tool)
	if !ok {
		return
	}
	if _, _, err := s.readFile(spec.KindTool, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	_, ds := spec.CheckTool(id, raw)
	if !s.saveFile(w, r, spec.KindTool, id, raw, req.ExpectedHash, ds) {
		return
	}
	v, err := s.toolDetail(id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteTool(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.removeFile(w, r, spec.KindTool, id, r.URL.Query().Get("expected_hash"), func() error {
		// The tool's secrets go with it; a tool created later under the same id must not inherit them.
		err := os.Remove(spec.KindToolEnv.Path(s.opts.Home, id)) //nolint:gosec // id names an existing tool file under home
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		s.publishFileChange(spec.KindToolEnv, id)
		return err
	})
}

func (s *Server) validateTool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		YAML *string    `json:"yaml"`
		Tool *spec.Tool `json:"tool"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	raw, ok := toolInput(w, req.YAML, req.Tool)
	if !ok {
		return
	}
	tool, ds := spec.CheckTool("", raw)
	out := struct {
		Valid       bool              `json:"valid"`
		Diagnostics []spec.Diagnostic `json:"diagnostics"`
		YAML        *string           `json:"yaml"`
		Tool        *spec.Tool        `json:"tool"`
		Schema      map[string]any    `json:"schema"`
	}{Valid: !spec.HasErrors(ds), Diagnostics: diagnostics(ds), Tool: toolValue(tool)}
	if tool != nil {
		if canon, err := spec.MarshalTool(*tool); err == nil {
			text := string(canon)
			out.YAML = &text
		}
		out.Schema = tool.EffectiveSchema()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) toolExists(w http.ResponseWriter, id string) bool {
	if s.opts.Workspace().Tool(id) == nil {
		notFound(w, "no tool %q", id)
		return false
	}
	return true
}

// ---- test run ----

type toolTestResultView struct {
	OK         bool            `json:"ok"`
	Args       json.RawMessage `json:"args"`
	Stdout     string          `json:"stdout"`
	Structured json.RawMessage `json:"structured"`
	Stderr     string          `json:"stderr"`
	ExitCode   int             `json:"exit_code"`
	DurationMS int64           `json:"duration_ms"`
	TimedOut   bool            `json:"timed_out"`
	Error      *string         `json:"error"`
}

// workspaceSecrets is a redactor over every secret of the workspace: the global
// env and every sensor's and tool's own env.
func workspaceSecrets(ws *spec.Workspace) *runner.Redactor {
	var secrets []map[string]string
	if ws.Env.Value != nil {
		secrets = append(secrets, ws.Env.Value.SecretVars())
	}
	for _, f := range ws.SensorEnv {
		if f.Value != nil {
			secrets = append(secrets, f.Value.SecretVars())
		}
	}
	for _, f := range ws.ToolEnv {
		if f.Value != nil {
			secrets = append(secrets, f.Value.SecretVars())
		}
	}
	return runner.NewRedactor(secrets...)
}

func (s *Server) testTool(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Args json.RawMessage `json:"args"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	ws := s.opts.Workspace()
	f := ws.Tool(id)
	if f == nil {
		notFound(w, "no tool %q", id)
		return
	}
	if !f.Valid() {
		validationProblem(w, "the tool has errors", f.Diagnostics)
		return
	}
	ks, err := s.opts.Store.GetKillSwitch(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if ks.Paused {
		s.writeError(w, r, errors.Join(core.ErrPaused, errors.New("tool tests are off while the kill switch is engaged")))
		return
	}
	env := map[string]string{}
	if ws.Env.Value != nil {
		for k, v := range ws.Env.Value.Vars {
			env[k] = v
		}
	}
	if own := ws.ToolEnvFor(id); own != nil && own.Value != nil {
		for k, v := range own.Value.Vars {
			env[k] = v
		}
	}
	res, err := tools.RunUserTool(r.Context(), f.Value, req.Args, tools.UserOptions{
		Env: env, Redactor: workspaceSecrets(ws), BasePATH: s.opts.BasePATH,
		StateDir: filepath.Join(s.opts.Home, "data", "state", "_tools", id),
	})
	view := toolTestResultView{
		Args: res.Args, Stdout: res.Stdout, Stderr: res.Stderr, ExitCode: res.ExitCode,
		DurationMS: res.DurationMS, TimedOut: res.TimedOut, OK: err == nil,
	}
	if len(res.Structured) > 0 {
		view.Structured = res.Structured
	}
	if view.Args == nil {
		view.Args = json.RawMessage("{}")
	}
	if err != nil {
		var te *tools.ToolError
		var ae *tools.ArgError
		switch {
		case errors.As(err, &ae):
			fieldProblem(w, "args", ae.Msg)
			return
		case errors.As(err, &te):
			msg := te.Error()
			view.Error = &msg
		default:
			s.writeError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, view)
}

// ---- per-tool env ----

func (s *Server) listToolEnv(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.toolExists(w, id) {
		return
	}
	raw, _, err := s.envFile(spec.KindToolEnv, id)
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

func (s *Server) setToolEnvValue(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.toolExists(w, id) {
		return
	}
	s.setEnv(w, r, spec.KindToolEnv, id)
}

func (s *Server) deleteToolEnvValue(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.toolExists(w, id) {
		return
	}
	s.deleteEnv(w, r, spec.KindToolEnv, id)
}

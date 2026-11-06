package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"slices"
	"sort"
	"time"

	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// Files under the home directory (sensors, flows, prompts) are read and
// written through internal/spec. Single-resource reads and the responses to
// writes come from the file on disk, so they always agree with the hash a
// following write is checked against. Lists come from the loaded workspace.
// Every write calls Reload before it answers, so the engine already runs the
// new definition when the client sees the response.

func diagnostics(ds []spec.Diagnostic) []spec.Diagnostic {
	if ds == nil {
		return []spec.Diagnostic{}
	}
	return ds
}

// readFile reads a resource file. A missing file is spec.ErrNotFound.
func (s *Server) readFile(k spec.Kind, id string) ([]byte, string, error) {
	return spec.ReadFile(s.opts.Home, k, id)
}

// expectedHash validates an expected_hash from a body or query.
func expectedHashOK(w http.ResponseWriter, h string) bool {
	if !hashRE.MatchString(h) {
		badRequest(w, "expected_hash must be the 64 character hash from the last read")
		return false
	}
	return true
}

// saveFile validates and writes a file, then reloads and announces it. It
// answers any failure itself. expected is "" to create.
func (s *Server) saveFile(w http.ResponseWriter, r *http.Request, k spec.Kind, id string, raw []byte, expected string, ds []spec.Diagnostic) bool {
	if spec.HasErrors(ds) {
		validationProblem(w, "the file has errors", ds)
		return false
	}
	if _, err := spec.WriteFile(s.opts.Home, k, id, raw, expected); err != nil {
		switch {
		case errors.Is(err, spec.ErrConflict) && expected == "":
			conflictProblem(w, typeAlreadyExists, err.Error(), nil)
		case errors.Is(err, spec.ErrConflict):
			_, cur, _ := s.readFile(k, id)
			fileConflict(w, err.Error(), expected, cur)
		default:
			s.writeError(w, r, err)
		}
		return false
	}
	s.reload()
	s.publishFileChange(k, id)
	return true
}

// removeFile deletes a file if it still has the expected hash.
//
// cleanup, when non-nil, runs after the file is gone and before the reload, so
// state that belongs to the file goes away with it.
func (s *Server) removeFile(w http.ResponseWriter, r *http.Request, k spec.Kind, id, expected string, cleanup func() error) {
	if !expectedHashOK(w, expected) {
		return
	}
	if _, _, err := s.readFile(k, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := spec.DeleteFile(s.opts.Home, k, id, expected); err != nil {
		if errors.Is(err, spec.ErrConflict) {
			_, cur, _ := s.readFile(k, id)
			fileConflict(w, err.Error(), expected, cur)
			return
		}
		s.writeError(w, r, err)
		return
	}
	if cleanup != nil {
		if err := cleanup(); err != nil {
			s.log().Error("api: clean up after delete", "kind", k, "id", id, "err", err)
		}
	}
	s.reload()
	s.publishFileChange(k, id)
	w.WriteHeader(http.StatusNoContent)
}

// checkID rejects ids that cannot be file names with a 422.
func checkID(w http.ResponseWriter, id string) bool {
	if spec.ValidID(id) {
		return true
	}
	validationProblem(w, "invalid id", []spec.Diagnostic{{
		Severity: spec.SeverityError, Code: spec.CodeInvalidID, Path: "id",
		Message: "id must use letters, digits, - and _ (max 64) and start with a letter or digit",
	}})
	return false
}

// oneOf answers 400 unless exactly one of the request's alternatives is set.
func oneOf(w http.ResponseWriter, a, b bool, an, bn string) bool {
	if a == b {
		badRequest(w, "send exactly one of %s and %s", an, bn)
		return false
	}
	return true
}

// ---- sensors ----

type sensorStatusView struct {
	State string `json:"state"`
	// Paused is true when this sensor is paused individually (not by the kill
	// switch); PausedAt and PausedBy then say when and by whom.
	Paused        bool           `json:"paused"`
	PausedAt      *time.Time     `json:"paused_at"`
	PausedBy      *string        `json:"paused_by"`
	LastRun       *sensorRunView `json:"last_run"`
	LastSuccessAt *time.Time     `json:"last_success_at"`
	NextPollAt    *time.Time     `json:"next_poll_at"`
	NewItems24h   int            `json:"new_items_24h"`
}

type sensorSummaryView struct {
	ID          string            `json:"id"`
	Path        string            `json:"path"`
	Hash        string            `json:"hash"`
	Sensor      *spec.Sensor      `json:"sensor"`
	Diagnostics []spec.Diagnostic `json:"diagnostics"`
	Status      sensorStatusView  `json:"status"`
}

type sensorDetailView struct {
	sensorSummaryView
	YAML string `json:"yaml"`
}

func (s *Server) sensorStatus(ctx context.Context, id string, sensor *spec.Sensor, valid, killSwitch bool) (sensorStatusView, error) {
	st := s.opts.Store
	v := sensorStatusView{State: "idle"}
	runs, err := st.ListSensorRuns(ctx, store.SensorRunFilter{SensorID: id, ListOpts: store.ListOpts{Limit: 1}})
	if err != nil {
		return v, err
	}
	state, err := st.GetSensorState(ctx, id)
	if err != nil {
		return v, err
	}
	n, err := st.CountEvents(ctx, id, time.Now().Add(-24*time.Hour))
	if err != nil {
		return v, err
	}
	v.NewItems24h = n
	v.LastSuccessAt = utcPtr(state.LastSuccessAt)
	if state.PausedAt != nil {
		by := state.PausedBy
		v.Paused, v.PausedAt, v.PausedBy = true, utcPtr(state.PausedAt), &by
	}
	paused := killSwitch || v.Paused
	var last *store.SensorRun
	if len(runs.Items) > 0 {
		last = &runs.Items[0]
		rv := s.sensorRunView(*last, true)
		v.LastRun = &rv
	}
	switch {
	case !valid:
		v.State = "invalid"
	case paused:
		v.State = "paused"
	case last != nil && last.Status == store.SensorRunning:
		v.State = "running"
	case state.ConsecutiveFailures > 0:
		v.State = "failing"
	}
	// The engine only schedules sensors that an enabled flow subscribes to.
	if valid && !paused && sensor != nil && s.sensorSubscribed(id) {
		if sched, err := sensor.Schedule(); err == nil {
			now := time.Now()
			base := now
			if state.LastRunAt != nil {
				base = *state.LastRunAt
			}
			next := sched.Next(base)
			if next.Before(now) {
				next = now
			}
			next = next.UTC()
			v.NextPollAt = &next
		}
	}
	return v, nil
}

// sensorSubscribed reports whether an enabled, valid flow subscribes to the sensor.
func (s *Server) sensorSubscribed(id string) bool {
	for _, f := range s.opts.Workspace().Flows {
		if f.Valid() && f.Value.Enabled && slices.Contains(f.Value.Subscribe, id) {
			return true
		}
	}
	return false
}

func (s *Server) sensorSummary(ctx context.Context, f *spec.File[spec.Sensor], killSwitch bool) (sensorSummaryView, error) {
	status, err := s.sensorStatus(ctx, f.Name, f.Value, f.Valid(), killSwitch)
	if err != nil {
		return sensorSummaryView{}, err
	}
	return sensorSummaryView{
		ID: f.Name, Path: f.Path, Hash: f.Hash, Sensor: sensorValue(f.Value), Diagnostics: diagnostics(f.Diagnostics), Status: status,
	}, nil
}

func (s *Server) listSensors(w http.ResponseWriter, r *http.Request) {
	ks, err := s.opts.Store.GetKillSwitch(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	files := slices.Clone(s.opts.Workspace().Sensors)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	out := struct {
		Items []sensorSummaryView `json:"items"`
	}{Items: []sensorSummaryView{}}
	for _, f := range files {
		v, err := s.sensorSummary(r.Context(), f, ks.Paused)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		out.Items = append(out.Items, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// sensorDetail reads sensors/<id>.yaml from disk.
func (s *Server) sensorDetail(r *http.Request, id string) (sensorDetailView, error) {
	raw, hash, err := s.readFile(spec.KindSensor, id)
	if err != nil {
		return sensorDetailView{}, err
	}
	sensor, ds := spec.CheckSensor(id, raw)
	ks, err := s.opts.Store.GetKillSwitch(r.Context())
	if err != nil {
		return sensorDetailView{}, err
	}
	f := &spec.File[spec.Sensor]{Name: id, Path: spec.KindSensor.Rel(id), Hash: hash, Value: sensor, Diagnostics: ds}
	sum, err := s.sensorSummary(r.Context(), f, ks.Paused)
	return sensorDetailView{sensorSummaryView: sum, YAML: string(raw)}, err
}

func (s *Server) getSensor(w http.ResponseWriter, r *http.Request) {
	v, err := s.sensorDetail(r, r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// sensorInput turns a request's yaml or sensor member into file bytes.
func sensorInput(w http.ResponseWriter, yamlText *string, sensor *spec.Sensor) ([]byte, bool) {
	if !oneOf(w, yamlText != nil, sensor != nil, "yaml", "sensor") {
		return nil, false
	}
	if yamlText != nil {
		return []byte(*yamlText), true
	}
	raw, err := spec.MarshalSensor(*sensor)
	if err != nil {
		badRequest(w, "cannot render the sensor as YAML: %s", err.Error())
		return nil, false
	}
	return raw, true
}

func (s *Server) createSensor(w http.ResponseWriter, r *http.Request) {
	var req struct {
		YAML   *string      `json:"yaml"`
		Sensor *spec.Sensor `json:"sensor"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	raw, ok := sensorInput(w, req.YAML, req.Sensor)
	if !ok {
		return
	}
	parsed, ds := spec.ParseSensor(raw)
	if parsed == nil {
		validationProblem(w, "the sensor does not parse", ds)
		return
	}
	id := parsed.ID
	if !checkID(w, id) {
		return
	}
	_, ds = spec.CheckSensor(id, raw)
	if !s.saveFile(w, r, spec.KindSensor, id, raw, "", ds) {
		return
	}
	if s.opts.Assist != nil {
		// The file is saved either way; a failure here only leaves a session on the "new sensor" target.
		if err := s.opts.Assist.SensorCreated(r.Context(), id); err != nil {
			s.log().Error("api: attach assist sessions to the new sensor", "sensor", id, "err", err)
		}
	}
	v, err := s.sensorDetail(r, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/sensors/"+id)
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) updateSensor(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		ExpectedHash string       `json:"expected_hash"`
		YAML         *string      `json:"yaml"`
		Sensor       *spec.Sensor `json:"sensor"`
	}
	if !decodeJSON(w, r, &req, false) || !expectedHashOK(w, req.ExpectedHash) {
		return
	}
	raw, ok := sensorInput(w, req.YAML, req.Sensor)
	if !ok {
		return
	}
	if _, _, err := s.readFile(spec.KindSensor, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	_, ds := spec.CheckSensor(id, raw)
	if !s.saveFile(w, r, spec.KindSensor, id, raw, req.ExpectedHash, ds) {
		return
	}
	v, err := s.sensorDetail(r, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteSensor(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.removeFile(w, r, spec.KindSensor, id, r.URL.Query().Get("expected_hash"), func() error {
		// The sensor's secrets go with it; a sensor created later under the
		// same id must not inherit them.
		err := os.Remove(spec.KindSensorEnv.Path(s.opts.Home, id)) //nolint:gosec // id names an existing sensor file under home
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		s.publishFileChange(spec.KindSensorEnv, id)
		return err
	})
}

func (s *Server) validateSensor(w http.ResponseWriter, r *http.Request) {
	var req struct {
		YAML   *string      `json:"yaml"`
		Sensor *spec.Sensor `json:"sensor"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	raw, ok := sensorInput(w, req.YAML, req.Sensor)
	if !ok {
		return
	}
	sensor, ds := spec.CheckSensor("", raw)
	out := struct {
		Valid       bool              `json:"valid"`
		Diagnostics []spec.Diagnostic `json:"diagnostics"`
		YAML        *string           `json:"yaml"`
		Sensor      *spec.Sensor      `json:"sensor"`
	}{Valid: !spec.HasErrors(ds), Diagnostics: diagnostics(ds), Sensor: sensorValue(sensor)}
	if sensor != nil {
		if canon, err := spec.MarshalSensor(*sensor); err == nil {
			text := string(canon)
			out.YAML = &text
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- flows ----

// normalizeFlow returns f with empty lists instead of nulls, as the contract
// requires, or nil when f is nil.
func normalizeFlow(f *spec.Flow) *spec.Flow {
	if f == nil {
		return nil
	}
	c := f.Clone()
	if c.Subscribe == nil {
		c.Subscribe = []string{}
	}
	if c.Edges == nil {
		c.Edges = []spec.Edge{}
	}
	return &c
}

type flowHealthView struct {
	WindowHours      int        `json:"window_hours"`
	Runs             int        `json:"runs"`
	Failed           int        `json:"failed"`
	ErrorRate        float64    `json:"error_rate"`
	Running          int        `json:"running"`
	Queued           int        `json:"queued"`
	AwaitingApproval int        `json:"awaiting_approval"`
	LastRunID        *string    `json:"last_run_id"`
	LastRunAt        *time.Time `json:"last_run_at"`
	LastRunStatus    *string    `json:"last_run_status"`
}

const healthWindow = 7 * 24 * time.Hour

func healthView(st store.FlowRunStats) flowHealthView {
	v := flowHealthView{
		WindowHours: int(healthWindow / time.Hour), Runs: st.Runs, Failed: st.Failed, Running: st.Running, Queued: st.Queued,
		AwaitingApproval: st.AwaitingApproval, LastRunID: nonEmpty(st.LastRunID), LastRunAt: utcPtr(st.LastRunAt),
	}
	if st.Finished > 0 {
		v.ErrorRate = float64(st.Failed) / float64(st.Finished)
	}
	if st.LastRunStatus != "" {
		s := string(st.LastRunStatus)
		v.LastRunStatus = &s
	}
	return v
}

type flowSummaryView struct {
	ID          string            `json:"id"`
	Path        string            `json:"path"`
	Hash        string            `json:"hash"`
	Enabled     *bool             `json:"enabled"`
	DryRun      *bool             `json:"dry_run"`
	Subscribe   []string          `json:"subscribe"`
	NodeCount   int               `json:"node_count"`
	Diagnostics []spec.Diagnostic `json:"diagnostics"`
	Health      flowHealthView    `json:"health"`
}

func (s *Server) listFlows(w http.ResponseWriter, r *http.Request) {
	stats, err := s.opts.Store.FlowRunStats(r.Context(), time.Now().Add(-healthWindow))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	files := slices.Clone(s.opts.Workspace().Flows)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	out := struct {
		Items []flowSummaryView `json:"items"`
	}{Items: []flowSummaryView{}}
	for _, f := range files {
		v := flowSummaryView{
			ID: f.Name, Path: f.Path, Hash: f.Hash, Subscribe: []string{}, Diagnostics: diagnostics(f.Diagnostics),
			Health: healthView(stats[f.Name]),
		}
		if f.Value != nil {
			v.Enabled, v.DryRun = &f.Value.Enabled, &f.Value.DryRun
			v.NodeCount = len(f.Value.Nodes)
			v.Subscribe = nonNil(f.Value.Subscribe)
		}
		out.Items = append(out.Items, v)
	}
	writeJSON(w, http.StatusOK, out)
}

type flowDetailView struct {
	ID          string            `json:"id"`
	Path        string            `json:"path"`
	Hash        string            `json:"hash"`
	YAML        string            `json:"yaml"`
	Flow        *spec.Flow        `json:"flow"`
	Diagnostics []spec.Diagnostic `json:"diagnostics"`
}

// flowDetail reads flows/<id>.yaml from disk and checks it against the
// loaded sensors, prompts and runtimes.
func (s *Server) flowDetail(id string) (flowDetailView, error) {
	raw, hash, err := s.readFile(spec.KindFlow, id)
	if err != nil {
		return flowDetailView{}, err
	}
	flow, ds := spec.CheckFlow(id, raw, s.refs())
	return flowDetailView{
		ID: id, Path: spec.KindFlow.Rel(id), Hash: hash, YAML: string(raw), Flow: flowValue(flow), Diagnostics: diagnostics(ds),
	}, nil
}

func (s *Server) getFlow(w http.ResponseWriter, r *http.Request) {
	v, err := s.flowDetail(r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func flowInput(w http.ResponseWriter, yamlText *string, flow *spec.Flow) ([]byte, bool) {
	if !oneOf(w, yamlText != nil, flow != nil, "yaml", "flow") {
		return nil, false
	}
	if yamlText != nil {
		return []byte(*yamlText), true
	}
	raw, err := spec.MarshalFlow(*flow)
	if err != nil {
		badRequest(w, "cannot render the flow as YAML: %s", err.Error())
		return nil, false
	}
	return raw, true
}

func (s *Server) createFlow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		YAML *string    `json:"yaml"`
		Flow *spec.Flow `json:"flow"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	raw, ok := flowInput(w, req.YAML, req.Flow)
	if !ok {
		return
	}
	parsed, ds := spec.ParseFlow(raw)
	if parsed == nil {
		validationProblem(w, "the flow does not parse", ds)
		return
	}
	id := parsed.ID
	if !checkID(w, id) {
		return
	}
	_, ds = spec.CheckFlow(id, raw, s.refs())
	if !s.saveFile(w, r, spec.KindFlow, id, raw, "", ds) {
		return
	}
	if s.opts.Assist != nil {
		// The file is saved either way; a failure here only leaves a session on the "new flow" target.
		if err := s.opts.Assist.FlowCreated(r.Context(), id); err != nil {
			s.log().Error("api: attach assist sessions to the new flow", "flow", id, "err", err)
		}
	}
	v, err := s.flowDetail(id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/flows/"+id)
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) updateFlow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		ExpectedHash string     `json:"expected_hash"`
		YAML         *string    `json:"yaml"`
		Flow         *spec.Flow `json:"flow"`
	}
	if !decodeJSON(w, r, &req, false) || !expectedHashOK(w, req.ExpectedHash) {
		return
	}
	raw, ok := flowInput(w, req.YAML, req.Flow)
	if !ok {
		return
	}
	if _, _, err := s.readFile(spec.KindFlow, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	_, ds := spec.CheckFlow(id, raw, s.refs())
	if !s.saveFile(w, r, spec.KindFlow, id, raw, req.ExpectedHash, ds) {
		return
	}
	v, err := s.flowDetail(id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteFlow(w http.ResponseWriter, r *http.Request) {
	s.removeFile(w, r, spec.KindFlow, r.PathValue("id"), r.URL.Query().Get("expected_hash"), nil)
}

func (s *Server) validateFlow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		YAML *string    `json:"yaml"`
		Flow *spec.Flow `json:"flow"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	raw, ok := flowInput(w, req.YAML, req.Flow)
	if !ok {
		return
	}
	flow, ds := spec.CheckFlow("", raw, s.refs())
	hash := spec.Hash(raw)
	text := string(raw)
	writeJSON(w, http.StatusOK, struct {
		Valid       bool              `json:"valid"`
		Diagnostics []spec.Diagnostic `json:"diagnostics"`
		Hash        *string           `json:"hash"`
		YAML        *string           `json:"yaml"`
		Flow        *spec.Flow        `json:"flow"`
	}{Valid: !spec.HasErrors(ds), Diagnostics: diagnostics(ds), Hash: &hash, YAML: &text, Flow: flowValue(flow)})
}

// ---- prompts ----

type promptSummaryView struct {
	Name        string            `json:"name"`
	Path        string            `json:"path"`
	Hash        string            `json:"hash"`
	Prompt      *spec.Prompt      `json:"prompt"`
	Diagnostics []spec.Diagnostic `json:"diagnostics"`
}

type promptDetailView struct {
	promptSummaryView
	Markdown string `json:"markdown"`
}

func (s *Server) listPrompts(w http.ResponseWriter, _ *http.Request) {
	files := slices.Clone(s.opts.Workspace().Prompts)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	out := struct {
		Items []promptSummaryView `json:"items"`
	}{Items: []promptSummaryView{}}
	for _, f := range files {
		out.Items = append(out.Items, promptSummaryView{
			Name: f.Name, Path: f.Path, Hash: f.Hash, Prompt: promptValue(f.Value), Diagnostics: diagnostics(f.Diagnostics),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) promptDetail(name string) (promptDetailView, error) {
	raw, hash, err := s.readFile(spec.KindPrompt, name)
	if err != nil {
		return promptDetailView{}, err
	}
	p, ds := spec.CheckPrompt(name, raw)
	return promptDetailView{
		promptSummaryView: promptSummaryView{
			Name: name, Path: spec.KindPrompt.Rel(name), Hash: hash, Prompt: promptValue(p), Diagnostics: diagnostics(ds),
		},
		Markdown: string(raw),
	}, nil
}

func (s *Server) getPrompt(w http.ResponseWriter, r *http.Request) {
	v, err := s.promptDetail(r.PathValue("name"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func promptInput(w http.ResponseWriter, markdown *string, prompt *spec.Prompt) ([]byte, bool) {
	if !oneOf(w, markdown != nil, prompt != nil, "markdown", "prompt") {
		return nil, false
	}
	if markdown != nil {
		return []byte(*markdown), true
	}
	raw, err := spec.MarshalPrompt(*prompt)
	if err != nil {
		badRequest(w, "cannot render the prompt as markdown: %s", err.Error())
		return nil, false
	}
	return raw, true
}

func (s *Server) createPrompt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Markdown *string      `json:"markdown"`
		Prompt   *spec.Prompt `json:"prompt"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	raw, ok := promptInput(w, req.Markdown, req.Prompt)
	if !ok {
		return
	}
	// The file name comes from the prompt's own name (front matter).
	parsed, _ := spec.ParsePrompt("", raw)
	name := ""
	if parsed != nil {
		name = parsed.Name
	}
	if name == "" {
		fieldProblem(w, "name", "the prompt needs a name (the `name` front matter field), which becomes its file name")
		return
	}
	if !checkID(w, name) {
		return
	}
	_, ds := spec.CheckPrompt(name, raw)
	if !s.saveFile(w, r, spec.KindPrompt, name, raw, "", ds) {
		return
	}
	v, err := s.promptDetail(name)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/prompts/"+name)
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) updatePrompt(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		ExpectedHash string       `json:"expected_hash"`
		Markdown     *string      `json:"markdown"`
		Prompt       *spec.Prompt `json:"prompt"`
	}
	if !decodeJSON(w, r, &req, false) || !expectedHashOK(w, req.ExpectedHash) {
		return
	}
	raw, ok := promptInput(w, req.Markdown, req.Prompt)
	if !ok {
		return
	}
	if _, _, err := s.readFile(spec.KindPrompt, name); err != nil {
		s.writeError(w, r, err)
		return
	}
	_, ds := spec.CheckPrompt(name, raw)
	if !s.saveFile(w, r, spec.KindPrompt, name, raw, req.ExpectedHash, ds) {
		return
	}
	v, err := s.promptDetail(name)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deletePrompt(w http.ResponseWriter, r *http.Request) {
	s.removeFile(w, r, spec.KindPrompt, r.PathValue("name"), r.URL.Query().Get("expected_hash"), nil)
}

func (s *Server) renderPrompt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        *string         `json:"name"`
		Markdown    *string         `json:"markdown"`
		EventID     *string         `json:"event_id"`
		Input       json.RawMessage `json:"input"`
		ExtraPrompt string          `json:"extra_prompt"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if !oneOf(w, req.Name != nil, req.Markdown != nil, "name", "markdown") ||
		!oneOf(w, req.EventID != nil, req.Input != nil, "event_id", "input") {
		return
	}
	var raw []byte
	if req.Name != nil {
		b, _, err := s.readFile(spec.KindPrompt, *req.Name)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		raw = b
	} else {
		raw = []byte(*req.Markdown)
	}
	prompt, _ := spec.ParsePrompt("preview", raw)
	body := prompt.Body

	var data map[string]any
	if req.EventID != nil {
		ev, err := s.opts.Store.GetEvent(r.Context(), *req.EventID)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		var payload any
		_ = json.Unmarshal(s.contentJSON(ev.Payload), &payload)
		data = spec.NewData(payload, payload, spec.FlowInfo{}, spec.RunInfo{EventID: ev.ID, Kind: "preview"})
	} else {
		var input any
		_ = json.Unmarshal(req.Input, &input)
		event := map[string]any{"id": "", "sensor_id": "", "dedup_key": "", "payload": input}
		data = spec.NewData(event, input, spec.FlowInfo{}, spec.RunInfo{Kind: "preview"})
	}
	out := struct {
		Rendered    *string           `json:"rendered"`
		Diagnostics []spec.Diagnostic `json:"diagnostics"`
	}{Diagnostics: []spec.Diagnostic{}}
	text, err := spec.Render(body, data)
	if err != nil {
		out.Diagnostics = append(out.Diagnostics, spec.Diagnostic{
			Severity: spec.SeverityError, Code: spec.CodeInvalidTemplate, Message: err.Error(),
		})
	} else {
		if req.ExtraPrompt != "" {
			text += "\n\n" + req.ExtraPrompt
		}
		out.Rendered = &text
	}
	writeJSON(w, http.StatusOK, out)
}

package assist

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
	"hal9001/internal/tools"
)

// SensorsDir holds the sensors a flow session's agent drafted (<id>.yaml): new ones, and copies of saved ones it
// edits. Save writes them (before the prompts and the flow). A hidden sidecar file records, for every copy of a saved
// sensor, the hash of the saved file at the time of the copy, so Save can detect a change made meanwhile.
const (
	SensorsDir = "sensors"

	sensorBasesFile     = ".bases.json"
	maxDraftSensors     = 20
	maxDraftSensorBytes = 64 << 10
	draftSensorSuffix   = ".yaml"
)

// draftSensor is one sensor of a flow session's draft directory.
type draftSensor struct {
	ID     string `json:"id"`
	YAML   string `json:"yaml"`
	Exists bool   `json:"exists"`
	// BaseHash is the hash of the saved file when the draft copy was made (an edit); nil for a new sensor.
	BaseHash    *string         `json:"base_hash"`
	Diagnostics json.RawMessage `json:"diagnostics"`
}

func sensorBases(dir string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(filepath.Join(dir, SensorsDir, sensorBasesFile)) //nolint:gosec // inside the session's draft directory
	if err == nil {
		_ = json.Unmarshal(b, &out)
	}
	if out == nil {
		out = map[string]string{}
	}
	return out
}

func writeSensorBases(dir string, bases map[string]string) error {
	b, err := json.Marshal(bases)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, SensorsDir, sensorBasesFile), b)
}

// readDraftSensors lists the sensors/<id>.yaml files of a draft directory by id, with their base hashes.
func readDraftSensors(dir string) []draftSensor {
	entries, err := os.ReadDir(filepath.Join(dir, SensorsDir))
	if err != nil {
		return nil
	}
	bases := sensorBases(dir)
	var out []draftSensor
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), draftSensorSuffix)
		if !ok || !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, SensorsDir, e.Name())) //nolint:gosec // inside the session's draft directory
		if err != nil || len(b) > maxDraftSensorBytes {
			continue
		}
		ds := draftSensor{ID: id, YAML: string(b)}
		if h, ok := bases[id]; ok {
			ds.BaseHash = &h
		}
		out = append(out, ds)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// checkDraftSensors validates the draft sensors against the workspace, fills Exists and the per-sensor diagnostics,
// and returns every diagnostic (file sensors/<id>.yaml). A draft that equals the saved file (Save just wrote it)
// becomes the new base of its edit and is no clash. Sensors it declares count as existing for the flow.
func checkDraftSensors(ws *spec.Workspace, dir string, refs *spec.Refs) ([]draftSensor, []spec.Diagnostic) {
	files := readDraftSensors(dir)
	if len(files) == 0 {
		return nil, nil
	}
	var all []spec.Diagnostic
	bases := sensorBases(dir)
	rebased := false
	for i := range files {
		f := &files[i]
		path := SensorsDir + "/" + f.ID + draftSensorSuffix
		saved := ws.Sensor(f.ID)
		f.Exists = saved != nil
		var ds []spec.Diagnostic
		if !spec.ValidID(f.ID) {
			ds = append(ds, spec.Diagnostic{File: path, Severity: spec.SeverityError, Code: spec.CodeInvalidID,
				Message: fmt.Sprintf("sensor name %q is invalid (letters, digits, - and _, at most 64 characters)", f.ID)})
		} else {
			refs.Sensors[f.ID] = true
			_, sds := spec.CheckSensor(f.ID, []byte(f.YAML), refs)
			ds = append(ds, sds...)
			switch {
			case saved != nil && saved.Hash == spec.Hash(spec.DropLegacyID([]byte(f.YAML))):
				// What Save wrote (or an unchanged copy): the saved file is the base from now on.
				if f.BaseHash == nil || *f.BaseHash != saved.Hash {
					h := saved.Hash
					f.BaseHash, bases[f.ID], rebased = &h, h, true
				}
			case saved == nil && f.BaseHash != nil:
				// The sensor was deleted after the draft copied it: Save creates it again.
				delete(bases, f.ID)
				f.BaseHash, rebased = nil, true
				ds = append(ds, spec.Diagnostic{File: path, Severity: spec.SeverityWarning, Code: spec.CodeIDMismatch,
					Message: fmt.Sprintf("sensor %q was deleted since the draft copied it: Save creates it again", f.ID)})
			case saved != nil && f.BaseHash == nil:
				ds = append(ds, spec.Diagnostic{File: path, Severity: spec.SeverityError, Code: spec.CodeIDMismatch,
					Message: fmt.Sprintf("a sensor named %q already exists: name the new sensor differently (or edit the existing one)", f.ID)})
			case saved != nil && *f.BaseHash != saved.Hash:
				ds = append(ds, spec.Diagnostic{File: path, Severity: spec.SeverityWarning, Code: spec.CodeIDMismatch,
					Message: fmt.Sprintf("sensor %q changed on disk since the draft copied it: Save will report a conflict", f.ID)})
			}
		}
		for j := range ds {
			ds[j].File = path
		}
		f.Diagnostics = marshalDiagnostics(ds)
		all = append(all, ds...)
	}
	if rebased {
		_ = writeSensorBases(dir, bases) // best effort: the next evaluation derives it again
	}
	return files, all
}

// parseDraftSensor parses one draft sensor for a test or the approval identity; nil when it has errors.
func parseDraftSensor(f draftSensor, refs *spec.Refs) (*spec.Sensor, []spec.Diagnostic) {
	sensor, ds := spec.CheckSensor(f.ID, []byte(f.YAML), refs)
	for i := range ds {
		ds[i].File = SensorsDir + "/" + f.ID + draftSensorSuffix
	}
	if sensor == nil || spec.HasErrors(ds) {
		return nil, ds
	}
	return sensor, ds
}

// writeDraftSensorLocked stores the agent's draft of sensor id. The first write of a saved sensor copies it in: the
// hash of the saved file becomes the base of the edit. s.mu must be held.
func (s *session) writeDraftSensorLocked(id, content string) error {
	if !spec.ValidID(id) {
		return fmt.Errorf("sensor name %q is invalid (letters, digits, - and _, at most 64 characters)", id)
	}
	if len(content) > maxDraftSensorBytes {
		return fmt.Errorf("the sensor is too large")
	}
	dir := filepath.Join(s.dir, SensorsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("assist: create sensors directory: %w", err)
	}
	path := filepath.Join(dir, id+draftSensorSuffix)
	if _, err := os.Stat(path); err != nil {
		if len(readDraftSensors(s.dir)) >= maxDraftSensors {
			return fmt.Errorf("a flow session drafts at most %d sensors", maxDraftSensors)
		}
		if saved := spec.Load(s.m.home).Sensor(id); saved != nil {
			bases := sensorBases(s.dir)
			bases[id] = saved.Hash
			if err := writeSensorBases(s.dir, bases); err != nil {
				return err
			}
		}
	}
	return writeAtomic(path, []byte(content))
}

// findDraftSensorLocked returns the draft sensor id of a flow session. s.mu must be held.
func (s *session) findDraftSensorLocked(id string) (draftSensor, bool) {
	for _, f := range readDraftSensors(s.dir) {
		if f.ID == id {
			return f, true
		}
	}
	return draftSensor{}, false
}

// testPlan is what a draft test of a flow session executes with credentials, for the approval rule.
type testPlan struct {
	id        scriptID
	available []string
	// saved is the saved script the identity is compared with (hasSaved false: none).
	saved    scriptID
	hasSaved bool
	// sensor and flow are what the test runs (sensor nil: a plain flow test).
	sensor *spec.Sensor
	flow   *spec.Flow
	// envTarget is the saved sensor whose own env file the sensor draft runs with ("" for a new sensor).
	envTarget string
	// diagnostics explain ok == false.
	diagnostics []spec.Diagnostic
	ok          bool
}

// testPlanLocked works out what the test req of a flow session runs: the flow's command nodes (a plain test), a draft
// sensor (req.Sensor), or both (req.FromSensor: the sensor's item is the flow's input). s.mu must be held.
func (s *session) testPlanLocked(req core.AssistTestRequest) testPlan {
	sensorID := req.Sensor
	if sensorID == "" {
		sensorID = req.FromSensor
	}
	var p testPlan
	if sensorID != "" {
		f, found := s.findDraftSensorLocked(sensorID)
		if !found {
			p.diagnostics = invalidDiagnostics("sensor", fmt.Sprintf("the session has no draft sensor %q (write sensors/%s.yaml with draft_write first)", sensorID, sensorID))
			return p
		}
		sensor, ds := parseDraftSensor(f, s.m.sensorRefs())
		if sensor == nil {
			p.diagnostics = ds
			return p
		}
		p.sensor = sensor
		if saved := spec.Load(s.m.home).Sensor(sensorID); saved != nil {
			p.envTarget = sensorID
		}
		p.available = s.m.availableEnv(p.envTarget)
		p.id = scriptOf(sensor, p.available)
		p.saved, p.hasSaved = s.m.savedScript(store.AssistTargetSensor, p.envTarget, p.available)
		if req.FromSensor == "" {
			p.ok = true
			return p
		}
	}
	flow, _, ds, _, _ := s.m.checkFlow(s.row.DraftYAML, s.row.TargetID, s.dir)
	if flow == nil || spec.HasErrors(ds) {
		p.diagnostics = ds
		return p
	}
	p.flow = flow
	flowAvail := s.m.availableEnv("")
	if p.sensor == nil {
		p.id, p.available, p.ok = scriptOfFlow(flow, flowAvail), flowAvail, true
		p.saved, p.hasSaved = s.m.savedScript(s.row.TargetKind, s.row.TargetID, flowAvail)
		return p
	}
	p.id = scriptOfFlow(flow, flowAvail).withSensor(sensorID, p.sensor, p.available)
	p.hasSaved = false
	p.ok = true
	return p
}

func invalidDiagnostics(path, message string) []spec.Diagnostic {
	return []spec.Diagnostic{{Severity: spec.SeverityError, Code: spec.CodeInvalidValue, Path: path, Message: message}}
}

// testDraftSensor runs a draft sensor of a flow session like a sensor test (req.Sensor) and returns its result.
func (m *Manager) testDraftSensor(ctx context.Context, s *session, byAgent bool, req core.AssistTestRequest) (core.AssistTestResult, error) {
	var zero core.AssistTestResult
	s.mu.Lock()
	if err := s.usableLocked(); err != nil {
		s.mu.Unlock()
		return zero, err
	}
	p := s.testPlanLocked(req)
	if !p.ok {
		s.mu.Unlock()
		return zero, &core.ValidationError{Diagnostics: p.diagnostics}
	}
	if err := s.gateLocked(byAgent, p.id, p.saved, p.hasSaved); err != nil {
		s.mu.Unlock()
		return zero, err
	}
	s.mu.Unlock()
	res, err := m.runSensorDraft(ctx, p.sensor, p.envTarget)
	if err != nil {
		return zero, err
	}
	res.SensorID = &req.Sensor
	return res, nil
}

// gateLocked applies the approval rule to a test about to run id: the agent's test needs the script to be approved (or
// saved), the user's test approves it. s.mu must be held.
func (s *session) gateLocked(byAgent bool, id, saved scriptID, hasSaved bool) error {
	if byAgent {
		if approved, _ := s.approvedAgainstLocked(id, saved, hasSaved); !approved {
			return fmt.Errorf("%w: the draft's script changed since the user last tested or approved it; "+
				"running it needs the user's approval (draft_test asks for it)", core.ErrInvalidState)
		}
		return nil
	}
	if !id.empty() {
		s.markApprovedLocked(id)
	}
	return nil
}

// fromSensorInput runs the draft sensor of req.FromSensor and returns its item number req.Item (default 0), the
// input of the flow test.
func (m *Manager) fromSensorInput(ctx context.Context, s *session, byAgent bool, req core.AssistTestRequest) (json.RawMessage, error) {
	s.mu.Lock()
	if err := s.usableLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	p := s.testPlanLocked(core.AssistTestRequest{FromSensor: req.FromSensor})
	if !p.ok {
		s.mu.Unlock()
		return nil, &core.ValidationError{Diagnostics: p.diagnostics}
	}
	if err := s.gateLocked(byAgent, p.id, p.saved, p.hasSaved); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Unlock()
	res, err := m.runSensorDraft(ctx, p.sensor, p.envTarget)
	if err != nil {
		return nil, err
	}
	i := 0
	if req.Item != nil {
		i = *req.Item
	}
	switch {
	case res.ParseError != nil:
		return nil, invalid("from_sensor", "the sensor draft's output is not a list of items: "+*res.ParseError)
	case res.ExitCode != 0 || res.TimedOut:
		return nil, invalid("from_sensor", fmt.Sprintf("the sensor draft failed (exit code %d, timed out %v): test it with sensor to see why", res.ExitCode, res.TimedOut))
	case i < 0 || i >= len(res.Items):
		return nil, invalid("item", fmt.Sprintf("the sensor draft produced %d item(s); item %d does not exist", res.ItemCount, i))
	}
	return res.Items[i].Item, nil
}

// readDraftSensorLocked returns the draft of sensor id, or the saved file when there is no draft (an edit starts
// with the first draft_write of the id). s.mu must be held.
func (s *session) readDraftSensorLocked(id string) (tools.Result, error) {
	if f, ok := s.findDraftSensorLocked(id); ok {
		return tools.Result{Text: f.YAML}, nil
	}
	if !spec.ValidID(id) {
		return tools.Result{}, fmt.Errorf("sensor name %q is invalid", id)
	}
	if saved := spec.Load(s.m.home).Sensor(id); saved != nil {
		return tools.Result{Text: "# saved sensor, not drafted yet: draft_write sensors/" + id + ".yaml to edit it\n" + string(saved.Raw)}, nil
	}
	return tools.Result{}, fmt.Errorf("sensors/%s.yaml: there is no draft or saved sensor of that name", id)
}

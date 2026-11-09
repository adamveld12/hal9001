package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A flow session can also draft sensors: new ones, and edits of saved ones. They validate together with the flow, can
// be tested (alone, or as the input of a flow test) with the sensor approval rule, and Save writes them through the
// ordinary sensor endpoints before the flow.

const (
	prSensor = "description: PR watcher\ninterval: 10m\nlang: bash\nscript: |\n  echo '[{\"id\":\"1\",\"title\":\"hello\",\"severity\":\"urgent\"},{\"id\":\"2\",\"title\":\"again\",\"severity\":\"noise\"}]'\nkey: \"{{.id}}\"\nfirst_run: fire\ntimeout: 10s\n"

	prFlow = "id: pr-flow\ndescription: PR flow\nenabled: false\ndry_run: true\nsubscribe: [pr-watch]\nnodes:\n  - id: keep\n    type: transform\n    steps:\n      - {type: pick, fields: [id, title]}\n    ui: {x: 100, y: 100}\nedges: []\n"
)

func hasErrorDiag(ds []map[string]any) bool {
	for _, d := range ds {
		if d["severity"] == "error" {
			return true
		}
	}
	return false
}

func TestAssistFlowDraftsANewSensorTestsItAndSavesBoth(t *testing.T) {
	d := newAssistDaemon(t, func(d *Daemon) {
		srcSensor(d)
		d.assistRuntime(false,
			turn{When: when("draft it$"), Steps: []step{
				assistWrite("s1", "sensors/pr-watch.yaml", prSensor),
				assistWrite("f1", "flow.yaml", prFlow),
				mcpStep("v1", "validate", map[string]any{}, "validate.json"),
				mcpStep("t1", "draft_test", map[string]any{"sensor": "pr-watch"}, "sensor-test.json"),
				say("Drafted."),
			}},
			turn{When: when("again$"), Steps: []step{
				mcpStep("t2", "draft_test", map[string]any{"from_sensor": "pr-watch", "item": 1}, "flow-test.json"),
				mcpStep("r1", "draft_read", map[string]any{"path": "sensors"}, "sensor-list.txt"),
				say("Tested the flow on the second item."),
			}},
		)
	})
	s := d.assistCreateFlow("")
	d.assistSend(s.ID, "draft it", nil)
	// A new sensor script: the agent's first test asks the user.
	ap := d.waitPendingApproval()
	d.Post("/api/approvals/"+ap["id"].(string)+"/approve", map[string]any{}).Want(200)
	if got := d.assistWait(s.ID, "idle", "failed"); got.Status != "idle" {
		t.Fatalf("status %q error %v", got.Status, got.Error)
	}
	f := d.assistFlowGet(s.ID)
	if len(f.Draft.Sensors) != 1 || f.Draft.Sensors[0].ID != "pr-watch" || f.Draft.Sensors[0].Exists || f.Draft.Sensors[0].BaseHash != nil {
		t.Fatalf("draft sensors = %+v", f.Draft.Sensors)
	}
	if hasErrorDiag(f.Draft.Diagnostics) || f.Draft.FlowID == nil || *f.Draft.FlowID != "pr-flow" {
		t.Fatalf("the flow subscribes to the draft sensor, so both validate: %+v", f.Draft.Diagnostics)
	}
	dir := d.assistDir(s.ID)
	if v := string(readFile(t, dir, "validate.json")); strings.Contains(v, `"error"`) {
		t.Errorf("validate = %s", v)
	}
	if r := string(readFile(t, dir, "sensor-test.json")); !strings.Contains(r, `"item_count": 2`) || !strings.Contains(r, `"sensor_id": "pr-watch"`) {
		t.Errorf("agent sensor test = %s", r)
	}
	d.Get("/api/sensors/pr-watch").Want(404) // nothing real before Save

	// The user's own test of the draft sensor, and a flow test fed by its second item (the script was approved).
	var st struct {
		Kind      string  `json:"kind"`
		SensorID  *string `json:"sensor_id"`
		ItemCount int     `json:"item_count"`
	}
	d.Post("/api/assist/sessions/"+s.ID+"/test", map[string]any{"sensor": "pr-watch"}).Want(200).JSON(&st)
	if st.Kind != "sensor" || st.SensorID == nil || *st.SensorID != "pr-watch" || st.ItemCount != 2 {
		t.Errorf("sensor test = %+v", st)
	}
	res := d.assistFlowTest(s.ID, map[string]any{"from_sensor": "pr-watch", "item": 1})
	if n := res.node("keep"); n == nil || n.Status != "done" || res.Input["id"] != "2" {
		t.Errorf("flow test from the sensor = %+v", res)
	}
	// The agent's from_sensor test needs no new approval: the user's test approved the same scripts... and the plain
	// flow identity is checked against the sensor test's: transform-only flows ask nothing.
	d.assistTurn(s.ID, "again")
	if r := string(readFile(t, dir, "flow-test.json")); !strings.Contains(r, `"keep"`) {
		t.Errorf("agent flow test = %s", r)
	}
	if l := string(readFile(t, dir, "sensor-list.txt")); !strings.Contains(l, "pr-watch.yaml") {
		t.Errorf("sensor list = %q", l)
	}
	// Bad requests.
	d.Post("/api/assist/sessions/"+s.ID+"/test", map[string]any{"sensor": "nope"}).Want(422)
	d.Post("/api/assist/sessions/"+s.ID+"/test", map[string]any{"sensor": "pr-watch", "sample_index": 0}).Want(422)
	d.Post("/api/assist/sessions/"+s.ID+"/test", map[string]any{"item": 1}).Want(422)

	// Save, as the UI does: the sensor first, then the flow.
	d.Post("/api/sensors", map[string]any{"id": "pr-watch", "yaml": f.Draft.Sensors[0].YAML}).Want(201)
	d.Post("/api/flows", map[string]any{"yaml": f.Draft.YAML}).Want(201)
	if d.Read("sensors/pr-watch.yaml") == "" || d.Read("flows/pr-flow.yaml") == "" {
		t.Fatal("Save did not write both files")
	}
	// The draft sees the saved sensor: its base is the saved file, and there is no clash.
	d.Put("/api/assist/sessions/"+s.ID+"/draft", map[string]any{"yaml": f.Draft.YAML, "expected_hash": f.Draft.Hash}).Want(200)
	f = d.assistFlowGet(s.ID)
	var saved struct {
		Hash string `json:"hash"`
	}
	d.Get("/api/sensors/pr-watch").Want(200).JSON(&saved)
	if len(f.Draft.Sensors) != 1 || !f.Draft.Sensors[0].Exists || f.Draft.Sensors[0].BaseHash == nil || *f.Draft.Sensors[0].BaseHash != saved.Hash || hasErrorDiag(f.Draft.Diagnostics) {
		t.Errorf("after Save: sensors %+v, diagnostics %+v", f.Draft.Sensors, f.Draft.Diagnostics)
	}
}

func TestAssistFlowEditsASavedSensorAndSaveUpdatesItOrConflicts(t *testing.T) {
	const edited = "description: Edited by the flow session\ninterval: 30m\nlang: bash\nscript: \"true\"\nkey: \"{{.id}}\"\nfirst_run: fire\ntimeout: 10s\n"
	const flow = "id: uses-src\nenabled: false\ndry_run: true\nsubscribe: [src]\nnodes:\n  - id: keep\n    type: transform\n    steps:\n      - {type: pick, fields: [id]}\n    ui: {x: 100, y: 100}\nedges: []\n"
	d := newAssistDaemon(t, func(d *Daemon) {
		srcSensor(d)
		d.assistRuntime(false, turn{Steps: []step{
			mcpStep("r1", "draft_read", map[string]any{"path": "sensors/src.yaml"}, "saved-src.yaml"),
			assistWrite("s1", "sensors/src.yaml", edited),
			assistWrite("f1", "flow.yaml", flow),
			say("Edited src."),
		}})
	})
	var before struct {
		Hash string `json:"hash"`
	}
	d.Get("/api/sensors/src").Want(200).JSON(&before)
	savedBefore := d.Read("sensors/src.yaml")

	newSession := func() assistFlowDetail {
		s := d.assistCreateFlow("")
		if got := d.assistTurn(s.ID, "edit src"); got.Status != "idle" {
			t.Fatalf("status %q error %v", got.Status, got.Error)
		}
		return d.assistFlowGet(s.ID)
	}

	f := newSession()
	if r := string(readFile(t, d.assistDir(f.ID), "saved-src.yaml")); !strings.Contains(r, "saved sensor, not drafted yet") || !strings.Contains(r, "interval: 1h") {
		t.Errorf("reading an undrafted saved sensor = %q", r)
	}
	if len(f.Draft.Sensors) != 1 || f.Draft.Sensors[0].ID != "src" || !f.Draft.Sensors[0].Exists || f.Draft.Sensors[0].BaseHash == nil || *f.Draft.Sensors[0].BaseHash != before.Hash {
		t.Fatalf("draft sensors = %+v (saved hash %s)", f.Draft.Sensors, before.Hash)
	}
	if d.Read("sensors/src.yaml") != savedBefore {
		t.Fatal("the saved sensor changed before Save")
	}
	// Save with the base hash updates the file.
	d.Put("/api/sensors/src", map[string]any{"expected_hash": *f.Draft.Sensors[0].BaseHash, "yaml": f.Draft.Sensors[0].YAML}).Want(200)
	if !strings.Contains(d.Read("sensors/src.yaml"), "Edited by the flow session") {
		t.Error("Save did not update the sensor")
	}

	// A second session copies the (now changed) sensor; the file changes meanwhile: Save reports a conflict and the
	// draft warns about it.
	f2 := newSession()
	d.Write("sensors/src.yaml", "interval: 2h\nlang: bash\nscript: \"true\"\nkey: \"{{.id}}\"\nfirst_run: fire\ntimeout: 10s\n")
	d.Put("/api/sensors/src", map[string]any{"expected_hash": *f2.Draft.Sensors[0].BaseHash, "yaml": f2.Draft.Sensors[0].YAML}).Want(409)
	d.Put("/api/assist/sessions/"+f2.ID+"/draft", map[string]any{"yaml": f2.Draft.YAML, "expected_hash": f2.Draft.Hash}).Want(200)
	f2 = d.assistFlowGet(f2.ID)
	warned := false
	for _, dg := range f2.Draft.Diagnostics {
		warned = warned || (dg["severity"] == "warning" && dg["file"] == "sensors/src.yaml")
	}
	if !warned {
		t.Errorf("no warning that the sensor changed on disk: %+v", f2.Draft.Diagnostics)
	}
}

func readFile(t *testing.T, dir, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return b
}

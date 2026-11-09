package e2e

import (
	"os"
	"strings"
	"testing"
)

// Renaming a sensor renames its file (the file name is the id), moves its env
// file, rewrites the flows that subscribe to it, and carries its dedup state,
// so the next poll does not fire the items it already saw. Recorded events
// keep the old id.
func TestSensorRenameCarriesStateAndReferences(t *testing.T) {
	d := newDaemon(t)
	d.Sensor("ping", "description: Two fixed items.\nlang: bash\nscript: |\n  echo '[{\"id\":\"a\"},{\"id\":\"b\"}]'\nkey: \"{{.id}}\"\n")
	d.Write("env.d/sensors/ping.env", "TOKEN=secret\n")
	d.Flow("watch", "# keep this comment\nenabled: true\nsubscribe: [ping] # the sensor\nnodes:\n  - {id: note, type: inbox, title: \"item {{.input.id}}\"}\n")
	d.Start()

	if run := d.Poll("ping"); run.Status != "succeeded" || run.NewCount == nil || *run.NewCount != 2 {
		t.Fatalf("first poll: %+v", run)
	}
	var cur struct {
		Hash   string `json:"hash"`
		Sensor struct {
			ID          string `json:"id"`
			Description string `json:"description"`
		} `json:"sensor"`
	}
	d.Get("/api/sensors/ping").Want(200).JSON(&cur)
	if cur.Sensor.ID != "ping" || cur.Sensor.Description != "Two fixed items." {
		t.Fatalf("sensor = %+v", cur.Sensor)
	}

	d.Post("/api/sensors/ping/rename", map[string]any{"new_id": "bad id", "expected_hash": cur.Hash}).Want(422)
	d.Post("/api/sensors/ping/rename", map[string]any{"new_id": "watch-me", "expected_hash": strings.Repeat("0", 64)}).Want(409)
	d.Sensor("taken", "lang: bash\nscript: echo '[]'\nkey: k\n")
	d.Eventually("the taken sensor loads", func() bool { return d.Get("/api/sensors/taken").Status == 200 })
	d.Post("/api/sensors/ping/rename", map[string]any{"new_id": "taken", "expected_hash": cur.Hash}).Want(409)

	var renamed struct {
		ID   string `json:"id"`
		YAML string `json:"yaml"`
	}
	d.Post("/api/sensors/ping/rename", map[string]any{"new_id": "pong", "expected_hash": cur.Hash}).Want(200).JSON(&renamed)
	if renamed.ID != "pong" || strings.Contains(renamed.YAML, "id:") {
		t.Fatalf("renamed = %+v", renamed)
	}
	if _, err := os.Stat(d.Path("sensors/ping.yaml")); err == nil {
		t.Error("sensors/ping.yaml still exists")
	}
	if _, err := os.Stat(d.Path("sensors/pong.yaml")); err != nil {
		t.Errorf("sensors/pong.yaml: %v", err)
	}
	if _, err := os.Stat(d.Path("env.d/sensors/ping.env")); err == nil {
		t.Error("env.d/sensors/ping.env still exists")
	}
	if got := d.Read("env.d/sensors/pong.env"); got != "TOKEN=secret\n" {
		t.Errorf("moved env = %q", got)
	}
	if got := d.Read("flows/watch.yaml"); !strings.Contains(got, "subscribe: [pong] # the sensor") || !strings.Contains(got, "# keep this comment") {
		t.Errorf("flow after rename:\n%s", got)
	}
	d.Get("/api/sensors/ping").Want(404)

	// The dedup keys moved: the same two items are not new under the new id.
	if run := d.Poll("pong"); run.Status != "succeeded" || run.NewCount == nil || *run.NewCount != 0 {
		t.Fatalf("poll after rename: %+v", run)
	}
	if n := len(d.Events("ping")); n != 2 {
		t.Errorf("events recorded under the old id = %d, want 2", n)
	}
	if n := len(d.Events("pong")); n != 0 {
		t.Errorf("events under the new id = %d, want 0", n)
	}
}

// Flows and tools rename the same way: file, env file and references move, the
// old name is gone, and a legacy id: line is dropped from the moved file.
func TestFlowAndToolRename(t *testing.T) {
	d := newDaemon(t)
	d.Sensor("src", "lang: bash\nscript: echo '[]'\nkey: k\n")
	d.Flow("old-flow", "id: old-flow\ndescription: Routes things.\nenabled: false\nsubscribe: [src]\nnodes:\n  - {id: note, type: inbox, title: hi}\n")
	d.Write("tools/old_tool.yaml", "description: Echo.\nparams: {}\nlang: bash\nscript: echo hi\n")
	d.Write("env.d/tools/old_tool.env", "KEY=v\n")
	d.Start()

	var f struct {
		Hash string `json:"hash"`
	}
	d.Get("/api/flows/old-flow").Want(200).JSON(&f)
	var renamed struct {
		ID   string `json:"id"`
		YAML string `json:"yaml"`
		Flow struct {
			Description string `json:"description"`
		} `json:"flow"`
	}
	d.Post("/api/flows/old-flow/rename", map[string]any{"new_id": "new-flow", "expected_hash": f.Hash}).Want(200).JSON(&renamed)
	if renamed.ID != "new-flow" || strings.Contains(renamed.YAML, "id: old-flow") || renamed.Flow.Description != "Routes things." {
		t.Fatalf("renamed flow = %+v", renamed)
	}
	d.Get("/api/flows/old-flow").Want(404)
	d.Post("/api/flows/old-flow/rename", map[string]any{"new_id": "x", "expected_hash": f.Hash}).Want(404)

	out, stderr, code := d.CLI("", "tools", "rename", "old_tool", "new_tool")
	if code != 0 {
		t.Fatalf("tools rename: exit %d\n%s\n%s", code, out, stderr)
	}
	d.Get("/api/tools/old_tool").Want(404)
	d.Get("/api/tools/new_tool").Want(200)
	if got := d.Read("env.d/tools/new_tool.env"); got != "KEY=v\n" {
		t.Errorf("moved tool env = %q", got)
	}
}

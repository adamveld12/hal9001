package e2e

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Scenario 4: a jq filter flow. Non-matching events are `dropped` with a
// reason and stop; matching events continue to the next node.
func TestScenario04FilterDropsNonMatching(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("filter", `id: filter
enabled: true
subscribe: [src]
nodes:
  - id: only-urgent
    type: command
    lang: jq
    script: 'select(.severity == "urgent") | . + {_reason: "urgent only"}'
  - id: note
    type: inbox
    title: "Urgent: {{.input.title}}"
edges:
  - {from: only-urgent, to: note}
`)
	d.Start()

	keep := d.RunOf("src", map[string]any{"id": "1", "severity": "urgent", "title": "deploy failing"})
	only := d.Node(keep, "only-urgent")
	if only.Status != "done" || only.Decision != "emitted" || only.Reason != "urgent only" || only.EmittedCount != 1 {
		t.Errorf("matching event: %+v", only)
	}
	if note := d.Node(keep, "note"); note.ParentNodeRunID != only.ID || note.Decision != "emitted" {
		t.Errorf("matching event did not continue to the inbox node: %+v", note)
	}

	runID := d.InjectRun("src", map[string]any{"id": "2", "severity": "noise", "title": "newsletter"})
	drop := d.WaitRun(runID, "succeeded")
	dropped := d.Node(drop, "only-urgent")
	if dropped.Decision != "dropped" || dropped.Status != "done" || len(dropped.Ports) != 0 || dropped.EmittedCount != 0 || dropped.Reason == "" {
		t.Errorf("non-matching event: %+v", dropped)
	}
	if len(drop.NodeRuns) != 1 {
		t.Errorf("a dropped branch ran further nodes:\n%s", describeRun(drop))
	}
	if !strings.Contains(dropped.Stdin.Text, "newsletter") || dropped.ExitCode == nil || *dropped.ExitCode != 0 {
		t.Errorf("trace lacks stdin/exit: %+v", dropped)
	}

	// The same decision path is visible from the event.
	var ev struct {
		FanOut []struct {
			FlowRun  FlowRun `json:"flow_run"`
			NodeRuns []struct {
				NodeRun  NodeRun `json:"node_run"`
				Children []any   `json:"children"`
			} `json:"node_runs"`
		} `json:"fan_out"`
	}
	d.Get("/api/events/" + drop.FlowRun.EventID).Want(200).JSON(&ev)
	if len(ev.FanOut) != 1 || len(ev.FanOut[0].NodeRuns) != 1 || ev.FanOut[0].NodeRuns[0].NodeRun.Decision != "dropped" || len(ev.FanOut[0].NodeRuns[0].Children) != 0 {
		t.Errorf("event fan-out: %+v", ev.FanOut)
	}
	if items := d.Inbox(); len(items) != 1 || items[0].Title != "Urgent: deploy failing" {
		t.Errorf("inbox: %+v", items)
	}
}

// Scenario 5: a router node sends `_port: urgent` and `_port: fyi` to different
// nodes; a port the node does not declare is an error visible in the trace.
func TestScenario05RouterPortsAndUndeclaredPort(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("router", `id: router
enabled: true
subscribe: [src]
nodes:
  - id: route
    type: command
    lang: jq
    ports: [urgent, fyi]
    script: '. + {_port: .port}'
  - {id: note-urgent, type: inbox, title: "urgent {{.input.id}}", priority: urgent}
  - {id: note-fyi, type: inbox, title: "fyi {{.input.id}}", priority: low}
edges:
  - {from: route, port: urgent, to: note-urgent}
  - {from: route, port: fyi, to: note-fyi}
`)
	d.Start()

	u := d.RunOf("src", map[string]any{"id": "1", "port": "urgent"})
	if r := d.Node(u, "route"); len(r.Ports) != 1 || r.Ports[0] != "urgent" {
		t.Errorf("route ports: %+v", r)
	}
	if len(u.NodeRuns) != 2 || d.Node(u, "note-urgent").Status != "done" {
		t.Errorf("urgent branch:\n%s", describeRun(u))
	}
	f := d.RunOf("src", map[string]any{"id": "2", "port": "fyi"})
	if r := d.Node(f, "route"); len(r.Ports) != 1 || r.Ports[0] != "fyi" {
		t.Errorf("route ports: %+v", r)
	}
	if len(f.NodeRuns) != 2 || d.Node(f, "note-fyi").Status != "done" {
		t.Errorf("fyi branch:\n%s", describeRun(f))
	}
	items := d.InboxByTitle()
	if items["urgent 1"].Priority != "urgent" || items["fyi 2"].Priority != "low" || len(items) != 2 {
		t.Errorf("inbox: %+v", items)
	}

	bad := d.WaitRun(d.InjectRun("src", map[string]any{"id": "3", "port": "bogus"}), "failed")
	route := d.Node(bad, "route")
	if route.Status != "failed" || route.Decision != "error" || route.Error != "undeclared_port" || !strings.Contains(route.Reason, "bogus") {
		t.Errorf("undeclared port: %+v", route)
	}
	if len(bad.NodeRuns) != 1 {
		t.Errorf("a failed router fed downstream nodes:\n%s", describeRun(bad))
	}
	if len(d.Inbox()) != 2 {
		t.Errorf("the failed run created inbox items: %+v", d.Inbox())
	}
}

const replayFlowYAML = `id: replayme
enabled: true
subscribe: [src]
nodes:
  - {id: note, type: inbox, title: "%s {{.input.id}}"}
`

// Scenario 10: replaying an old event into an edited flow creates a new run
// linked to the original, with the new flow hash.
func TestScenario10ReplayIntoEditedFlow(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("replayme", strings.Replace(replayFlowYAML, "%s", "v1", 1))
	d.Start()

	orig := d.RunOf("src", map[string]any{"id": "7"})
	if orig.FlowRun.Kind != "live" || orig.FlowRun.ReplayOf != "" {
		t.Fatalf("original run: %+v", orig.FlowRun)
	}

	f := d.GetFlow("replayme")
	var saved FlowDetail
	d.Put("/api/flows/replayme", map[string]any{"expected_hash": f.Hash, "yaml": strings.Replace(replayFlowYAML, "%s", "v2", 1)}).Want(200).JSON(&saved)
	if saved.Hash == f.Hash {
		t.Fatal("saving changed YAML did not change the flow hash")
	}

	var replay FlowRun
	d.Post("/api/events/"+orig.FlowRun.EventID+"/replay", map[string]any{"flow": "replayme"}).Want(202).JSON(&replay)
	if replay.Kind != "replay" || replay.ReplayOf != orig.FlowRun.ID || replay.EventID != orig.FlowRun.EventID {
		t.Errorf("replay run: %+v", replay)
	}
	done := d.WaitRun(replay.ID, "succeeded")
	if done.FlowRun.FlowHash != saved.Hash || done.FlowRun.FlowHash == orig.FlowRun.FlowHash {
		t.Errorf("replay hash %s, original %s, edited flow %s", done.FlowRun.FlowHash, orig.FlowRun.FlowHash, saved.Hash)
	}
	items := d.InboxByTitle()
	if _, ok := items["v1 7"]; !ok {
		t.Errorf("original item missing: %v", items)
	}
	if _, ok := items["v2 7"]; !ok {
		t.Errorf("replay did not use the edited flow: %v", items)
	}

	// The run links to the exact YAML it used, for both versions.
	var v struct {
		YAML string `json:"yaml"`
	}
	d.Get("/api/flows/replayme/versions/" + orig.FlowRun.FlowHash).Want(200).JSON(&v)
	if !strings.Contains(v.YAML, "v1 {{") {
		t.Errorf("original version snapshot: %q", v.YAML)
	}
	d.Get("/api/flows/replayme/versions/" + done.FlowRun.FlowHash).Want(200).JSON(&v)
	if !strings.Contains(v.YAML, "v2 {{") {
		t.Errorf("replay version snapshot: %q", v.YAML)
	}

	// Filtering the run list by replay_of finds the replay.
	var l struct {
		Items []FlowRun `json:"items"`
	}
	d.Get("/api/flow-runs?replay_of=" + orig.FlowRun.ID).Want(200).JSON(&l)
	if len(l.Items) != 1 || l.Items[0].ID != replay.ID {
		t.Errorf("runs replaying %s: %+v", orig.FlowRun.ID, l.Items)
	}
}

// Scenario 11: "Run node on sample" returns stdout/stderr/exit and is recorded
// as a kind=test run that the default timeline views hide.
func TestScenario11TestNodeOnSample(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("sample", `id: sample
enabled: true
subscribe: [src]
nodes:
  - id: shout
    type: command
    lang: bash
    script: |
      in=$(cat)
      echo "{\"echo\": $in}"
      echo "to stderr" >&2
  - id: fails
    type: command
    lang: bash
    script: |
      echo "partial"
      echo "boom" >&2
      exit 3
`)
	d.Start()

	var res struct {
		FlowRun FlowRun `json:"flow_run"`
		NodeRun NodeRun `json:"node_run"`
	}
	d.Post("/api/flows/sample/test-node", map[string]any{
		"node_id": "shout", "input": map[string]any{"source": "json", "json": map[string]any{"n": 5}},
	}).Want(200).JSON(&res)
	n := res.NodeRun
	if res.FlowRun.Kind != "test" || n.Status != "done" || n.Decision != "emitted" || n.ExitCode == nil || *n.ExitCode != 0 {
		t.Fatalf("test run: %+v / %+v", res.FlowRun, n)
	}
	if !strings.Contains(n.Stdout.Text, `"echo"`) || !strings.Contains(n.Stdout.Text, `"n":5`) || strings.TrimSpace(n.Stderr.Text) != "to stderr" || !strings.Contains(n.Stdin.Text, `"n"`) {
		t.Errorf("captured streams: stdin %q stdout %q stderr %q", n.Stdin.Text, n.Stdout.Text, n.Stderr.Text)
	}

	res = struct {
		FlowRun FlowRun `json:"flow_run"`
		NodeRun NodeRun `json:"node_run"`
	}{}
	d.Post("/api/flows/sample/test-node", map[string]any{
		"node_id": "fails", "input": map[string]any{"source": "json", "json": map[string]any{}},
	}).Want(200).JSON(&res)
	if f := res.NodeRun; f.Status != "failed" || f.Decision != "error" || f.ExitCode == nil || *f.ExitCode != 3 || strings.TrimSpace(f.Stderr.Text) != "boom" || f.Error != "exit_nonzero" {
		t.Errorf("failing node: %+v", f)
	}

	// No timeline noise: no event, and the run is hidden unless asked for.
	if evs := d.Events("src"); len(evs) != 0 {
		t.Errorf("test runs created events: %+v", evs)
	}
	var runs struct {
		Items []FlowRun `json:"items"`
	}
	d.Get("/api/flow-runs").Want(200).JSON(&runs)
	if len(runs.Items) != 0 {
		t.Errorf("default run list shows test runs: %+v", runs.Items)
	}
	d.Get("/api/flow-runs?kind=test").Want(200).JSON(&runs)
	if len(runs.Items) != 2 {
		t.Errorf("kind=test run list: %+v", runs.Items)
	}
	if s := d.GetStatus(); s.RunningFlowRuns != 0 || s.QueuedFlowRuns != 0 {
		t.Errorf("status: %+v", s)
	}
}

// Scenario 13: `sleep 999` with timeout: 1s ends with error `timeout` and the
// process (and what it spawned) is gone.
func TestScenario13CommandTimeoutKillsProcessGroup(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	pids := d.Path("pids")
	d.Flow("slow", `id: slow
enabled: true
subscribe: [src]
nodes:
  - id: sleeper
    type: command
    lang: bash
    timeout: 1s
    script: |
      echo $$ > `+pids+`.main
      sleep 999 &
      echo $! > `+pids+`.child
      wait
  - {id: after, type: inbox, title: "after"}
edges:
  - {from: sleeper, to: after}
`)
	d.Start()

	start := time.Now()
	run := d.WaitRun(d.InjectRun("src", map[string]any{"id": "1"}), "failed")
	if took := time.Since(start); took > 15*time.Second {
		t.Errorf("a 1s timeout took %s", took)
	}
	n := d.Node(run, "sleeper")
	if n.Status != "failed" || n.Decision != "error" || n.Error != "timeout" {
		t.Errorf("sleeper: %+v", n)
	}
	if len(run.NodeRuns) != 1 {
		t.Errorf("downstream ran after a timeout:\n%s", describeRun(run))
	}
	for _, suffix := range []string{".main", ".child"} {
		b, err := os.ReadFile(pids + suffix)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			t.Fatal(err)
		}
		d.EventuallyWithin(10*time.Second, "process "+suffix+" to be gone", func() bool {
			return syscall.Kill(pid, 0) == syscall.ESRCH
		})
	}
}

// Scenario 16: a flow saved through the API is written to the YAML file; a
// hand edit of the file is picked up by the running daemon; a save with a stale
// expected_hash is rejected with a conflict.
func TestScenario16FlowEditingAPIAndFile(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	const tmpl = `id: editme
enabled: true
subscribe: [src]
nodes:
  - {id: note, type: inbox, title: "%s {{.input.id}}"}
`
	flowYAML := func(title string) string { return strings.Replace(tmpl, "%s", title, 1) }
	d.Flow("editme", flowYAML("original"))
	d.Start()

	loaded := d.GetFlow("editme")
	if loaded.YAML != flowYAML("original") || loaded.Hash == "" {
		t.Fatalf("flow as loaded: %+v", loaded)
	}

	// API save -> the file on disk is updated, and the new version runs.
	var saved FlowDetail
	d.Put("/api/flows/editme", map[string]any{"expected_hash": loaded.Hash, "yaml": flowYAML("from-api")}).Want(200).JSON(&saved)
	if got := d.Read("flows/editme.yaml"); got != flowYAML("from-api") {
		t.Errorf("file after API save:\n%s", got)
	}
	d.RunOf("src", map[string]any{"id": "1"})
	if _, ok := d.InboxByTitle()["from-api 1"]; !ok {
		t.Errorf("API-saved flow did not run: %v", d.InboxByTitle())
	}

	// Stale expected_hash -> 409 conflict naming the current hash; nothing is written.
	var prob struct {
		CurrentHash  string `json:"current_hash"`
		ExpectedHash string `json:"expected_hash"`
	}
	r := d.Put("/api/flows/editme", map[string]any{"expected_hash": loaded.Hash, "yaml": flowYAML("stale")}).Want(409)
	r.JSON(&prob)
	if r.Problem() != "conflict" || prob.CurrentHash != saved.Hash || prob.ExpectedHash != loaded.Hash {
		t.Errorf("conflict problem: %s", r)
	}
	if got := d.Read("flows/editme.yaml"); got != flowYAML("from-api") {
		t.Errorf("a rejected save changed the file:\n%s", got)
	}

	// Hand edit -> the API reflects it at once, and the engine runs the new
	// version after the watcher reload (a run per probe event until it does).
	d.Write("flows/editme.yaml", flowYAML("by-hand"))
	edited := d.GetFlow("editme")
	if edited.YAML != flowYAML("by-hand") || edited.Hash == saved.Hash {
		t.Errorf("API after hand edit: %+v", edited)
	}
	var probe int
	d.Eventually("the engine to run the hand-edited flow", func() bool {
		probe++
		d.RunOf("src", map[string]any{"id": "probe" + strconv.Itoa(probe)})
		_, ok := d.InboxByTitle()["by-hand probe"+strconv.Itoa(probe)]
		return ok
	})

	// A save based on the pre-edit hash is now stale.
	d.Put("/api/flows/editme", map[string]any{"expected_hash": saved.Hash, "yaml": flowYAML("late")}).Want(409)
	// Validation errors are 422 with diagnostics and do not touch the file.
	bad := d.Put("/api/flows/editme", map[string]any{"expected_hash": edited.Hash, "yaml": strings.Replace(flowYAML("x"), "subscribe: [src]", "subscribe: [nope]\nbogus_key: 1", 1)}).Want(422)
	var vp struct {
		Diagnostics []struct {
			Code string `json:"code"`
		} `json:"diagnostics"`
	}
	bad.JSON(&vp)
	if len(vp.Diagnostics) == 0 {
		t.Errorf("422 without diagnostics: %s", bad)
	}
	if got := d.Read("flows/editme.yaml"); got != flowYAML("by-hand") {
		t.Errorf("an invalid save changed the file:\n%s", got)
	}
}

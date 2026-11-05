package engine_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// withField appends a reserved field to the single-line JSON object on stdin.
const withField = `read -r line
field() { echo "${line%\}},$1}"; }
`

// Scenario 4: a filter node drops non-matching events (empty stdout) with a
// recorded reason; matching events continue and carry the _reason.
func TestScenario4FilterDropsAndPasses(t *testing.T) {
	h := newHarness(t)
	h.idleSensor()
	h.flow("f", `id: f
enabled: true
subscribe: [src]
nodes:
  - id: only-keep
    type: command
    lang: bash
    script: |
      `+strings.ReplaceAll(withField, "\n", "\n      ")+`case "$line" in
        *'"keep":true'*) field '"_reason":"keep flag set"' ;;
      esac
  - {id: note, type: inbox, title: "kept {{.input.id}}", summary_md: "{{json .input}}"}
edges:
  - {from: only-keep, to: note}
`)
	h.start()

	keep := h.waitRun(h.injectRun(`{"id":"1","keep":true}`), store.FlowRunSucceeded)
	drop := h.waitRun(h.injectRun(`{"id":"2","keep":false}`), store.FlowRunSucceeded)

	n := h.node(keep.ID, "only-keep")
	if n.Status != store.NodeDone || n.Decision != store.DecisionEmitted || n.Reason != "keep flag set" || len(n.Ports) != 1 || n.Ports[0] != "out" {
		t.Errorf("matching node run: %+v", n)
	}
	if got := h.node(keep.ID, "note"); got.ParentNodeRunID != n.ID || got.Status != store.NodeDone {
		t.Errorf("downstream node run: %+v", got)
	}
	d := h.node(drop.ID, "only-keep")
	if d.Status != store.NodeDone || d.Decision != store.DecisionDropped || d.Reason == "" || d.OutputCount != 0 {
		t.Errorf("dropped node run: %+v", d)
	}
	if got := len(h.nodeRuns(drop.ID)); got != 1 {
		t.Errorf("dropped event ran %d nodes, want only the filter", got)
	}
	items := h.inboxItems()
	if len(items) != 1 || items[0].Title != "kept 1" || strings.Contains(items[0].SummaryMD, "_reason") {
		t.Errorf("inbox: %+v (reserved keys must be stripped before downstream)", items)
	}
}

// Scenario 5: _port routes to different nodes; an undeclared port is an error
// visible in the trace, routed to the error port when wired, else the branch fails.
func TestScenario5RouterPorts(t *testing.T) {
	h := newHarness(t)
	h.idleSensor()
	router := `    type: command
    lang: bash
    ports: [urgent, fyi]
    script: |
      ` + strings.ReplaceAll(withField, "\n", "\n      ") + `case "$line" in
        *'"p":"u"'*) field '"_port":"urgent"' ;;
        *'"p":"f"'*) field '"_port":"fyi"' ;;
        *) field '"_port":"bogus"' ;;
      esac
`
	h.flow("plain", `id: plain
enabled: true
subscribe: [src]
nodes:
  - id: route
`+router+`  - {id: urgent, type: inbox, title: "urgent {{.input.id}}", priority: high}
  - {id: fyi, type: inbox, title: "fyi {{.input.id}}"}
edges:
  - {from: route, port: urgent, to: urgent}
  - {from: route, port: fyi, to: fyi}
`)
	h.flow("guarded", `id: guarded
enabled: true
subscribe: [src]
nodes:
  - id: route
`+router+`  - {id: urgent, type: inbox, title: "g-urgent {{.input.id}}"}
  - {id: fyi, type: inbox, title: "g-fyi {{.input.id}}"}
  - {id: oops, type: inbox, title: "oops {{.input.error}} {{.input.input.id}}", summary_md: "{{.input.reason}}"}
edges:
  - {from: route, port: urgent, to: urgent}
  - {from: route, port: fyi, to: fyi}
  - {from: route, port: error, to: oops}
`)
	h.start()

	run := func(item string) map[string]string {
		res := h.inject(item)
		out := map[string]string{}
		for _, id := range res.FlowRunIDs {
			fr := h.waitRun(id, store.FlowRunSucceeded, store.FlowRunFailed)
			out[fr.FlowID] = string(fr.Status)
		}
		return out
	}
	if got := run(`{"id":"1","p":"u"}`); got["plain"] != "succeeded" || got["guarded"] != "succeeded" {
		t.Fatalf("urgent: %v\n%s", got, h.dump())
	}
	if got := run(`{"id":"2","p":"f"}`); got["plain"] != "succeeded" {
		t.Fatalf("fyi: %v", got)
	}
	got := run(`{"id":"3","p":"x"}`)
	if got["plain"] != "failed" || got["guarded"] != "succeeded" {
		t.Fatalf("undeclared port: %v, want plain failed (no error edge), guarded succeeded\n%s", got, h.dump())
	}

	titles := map[string]bool{}
	for _, it := range h.inboxItems() {
		titles[it.Title] = true
	}
	for _, want := range []string{"urgent 1", "g-urgent 1", "fyi 2", "g-fyi 2", "oops undeclared_port 3"} {
		if !titles[want] {
			t.Errorf("missing inbox item %q in %v", want, titles)
		}
	}
	for _, bad := range []string{"urgent 3", "fyi 3", "g-urgent 3"} {
		if titles[bad] {
			t.Errorf("inbox item %q must not exist", bad)
		}
	}
	// The trace shows why.
	frs, _ := h.st.ListFlowRuns(t.Context(), store.FlowRunFilter{Statuses: []store.FlowRunStatus{store.FlowRunFailed}})
	if len(frs.Items) != 1 {
		t.Fatalf("failed flow runs: %d", len(frs.Items))
	}
	rn := h.node(frs.Items[0].ID, "route")
	if rn.Status != store.NodeFailed || rn.Decision != store.DecisionError || rn.Error != "undeclared_port" || !strings.Contains(rn.Reason, `"bogus"`) {
		t.Errorf("failed router node run: %+v", rn)
	}
	if !strings.Contains(frs.Items[0].Error, "route") {
		t.Errorf("flow run error %q should name the node", frs.Items[0].Error)
	}
	urgent := h.node(h.mustFlowRun("plain", `1`), "urgent")
	if urgent.Status != store.NodeDone {
		t.Errorf("urgent branch: %+v", urgent)
	}
}

// mustFlowRun finds the flow's run for the event with the dedup key.
func (h *harness) mustFlowRun(flow, key string) string {
	h.t.Helper()
	for _, ev := range h.events("src") {
		if ev.DedupKey != key {
			continue
		}
		page, _ := h.st.ListFlowRuns(h.t.Context(), store.FlowRunFilter{EventID: ev.ID, FlowID: flow})
		if len(page.Items) == 1 {
			return page.Items[0].ID
		}
	}
	h.t.Fatalf("no flow run of %s for key %s", flow, key)
	return ""
}

// Errors: a failing command routes to the error port when wired, and its
// payload carries the class, reason and original input; otherwise the node and
// flow run fail with the exit code and stderr recorded.
func TestCommandErrorRouting(t *testing.T) {
	h := newHarness(t)
	h.idleSensor()
	boom := `    type: command
    lang: bash
    script: |
      echo "kaboom" >&2
      exit 3
`
	h.flow("unwired", "id: unwired\nenabled: true\nsubscribe: [src]\nnodes:\n  - id: boom\n"+boom+"  - {id: after, type: inbox, title: never}\nedges:\n  - {from: boom, to: after}\n")
	h.flow("wired", "id: wired\nenabled: true\nsubscribe: [src]\nnodes:\n  - id: boom\n"+boom+"  - {id: rescue, type: inbox, title: \"rescued {{.input.error}} {{.input.exit_code}} {{.input.input.id}}\", summary_md: \"{{.input.stderr}}\"}\nedges:\n  - {from: boom, port: error, to: rescue}\n")
	h.start()
	res := h.inject(`{"id":"9"}`)
	for _, id := range res.FlowRunIDs {
		h.waitRun(id, store.FlowRunSucceeded, store.FlowRunFailed)
	}
	un := h.mustFlowRun("unwired", "9")
	w := h.mustFlowRun("wired", "9")
	if fr := h.run(un); fr.Status != store.FlowRunFailed {
		t.Errorf("unwired flow run: %s", h.dumpRun(un))
	}
	n := h.node(un, "boom")
	if n.Status != store.NodeFailed || n.Decision != store.DecisionError || n.Error != "exit_nonzero" || n.ExitCode == nil || *n.ExitCode != 3 ||
		!strings.Contains(n.Reason, "exit status 3") || !strings.Contains(n.Reason, "kaboom") || !strings.Contains(h.text(n.Stderr), "kaboom") {
		t.Errorf("failed node run: %+v", n)
	}
	if got := len(h.nodeRuns(un)); got != 1 {
		t.Errorf("a failed node fed downstream: %d node runs", got)
	}
	if fr := h.run(w); fr.Status != store.FlowRunSucceeded {
		t.Errorf("wired flow run: %s", h.dumpRun(w))
	}
	wn := h.node(w, "boom")
	if wn.Status != store.NodeDone || wn.Decision != store.DecisionError || len(wn.Ports) != 1 || wn.Ports[0] != "error" {
		t.Errorf("error-routed node run: %+v", wn)
	}
	var found bool
	for _, it := range h.inboxItems() {
		if it.Title == "rescued exit_nonzero 3 9" && strings.Contains(it.SummaryMD, "kaboom") {
			found = true
		}
	}
	if !found {
		t.Errorf("error payload did not reach the rescue node: %+v", h.inboxItems())
	}
}

// JSONL output fans out one payload per line, each routed by its own _port;
// one port can feed several nodes, which run concurrently.
func TestJSONLSplitAndPortFanOut(t *testing.T) {
	h := newHarness(t)
	h.idleSensor()
	h.flow("f", `id: f
enabled: true
subscribe: [src]
nodes:
  - id: split
    type: command
    lang: bash
    ports: [odd, even]
    script: |
      echo '{"n":1,"_port":"odd","_reason":"first"}'
      echo '{"n":2,"_port":"even"}'
      echo '{"n":3,"_port":"odd","_reason":"third"}'
  - {id: odd-a, type: inbox, title: "a{{.input.n}}"}
  - {id: odd-b, type: inbox, title: "b{{.input.n}}"}
  - {id: even, type: inbox, title: "e{{.input.n}}"}
edges:
  - {from: split, port: odd, to: odd-a}
  - {from: split, port: odd, to: odd-b}
  - {from: split, port: even, to: even}
`)
	h.start()
	id := h.waitRun(h.injectRun(`{"id":"x"}`), store.FlowRunSucceeded).ID
	sp := h.node(id, "split")
	if sp.OutputCount != 3 || len(sp.Ports) != 2 || sp.Reason != "first; third" {
		t.Errorf("split node run: %+v", sp)
	}
	got := map[string]bool{}
	for _, it := range h.inboxItems() {
		got[it.Title] = true
	}
	for _, want := range []string{"a1", "a3", "b1", "b3", "e2"} {
		if !got[want] {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	if len(got) != 5 {
		t.Errorf("inbox titles: %v", got)
	}
}

// Scenario 13: a command that outlives its timeout is killed (the whole
// process group), recorded as a timeout error.
func TestScenario13CommandTimeoutKillsProcessGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for a 2s timeout")
	}
	h := newHarness(t)
	h.idleSensor()
	pids := filepath.Join(t.TempDir(), "pids")
	h.flow("f", `id: f
enabled: true
subscribe: [src]
nodes:
  - id: slow
    type: command
    lang: bash
    timeout: 2s
    script: |
      sleep 999 &
      echo "$$ $!" > `+pids+`
      wait
`)
	h.start()
	start := time.Now()
	id := h.waitRun(h.injectRun(`{"id":"1"}`), store.FlowRunFailed).ID
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("timeout took %s", d)
	}
	n := h.node(id, "slow")
	if n.Status != store.NodeFailed || n.Error != "timeout" || n.Reason != "timeout" {
		t.Errorf("node run: %+v", n)
	}
	b, err := os.ReadFile(pids)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range strings.Fields(string(b)) {
		pid, _ := strconv.Atoi(f)
		h.eventually("process "+f+" to be gone", func() bool { return !pidAlive(pid) })
	}
}

// Scenario 9 (command part): in a dry-run flow side_effect nodes are skipped
// with skipped:dry_run, HAL_DRY_RUN=1 reaches the other nodes, and the inbox
// item is tagged dry-run.
func TestDryRunSkipsSideEffectsAndTagsInbox(t *testing.T) {
	h := newHarness(t)
	h.idleSensor()
	effect := filepath.Join(t.TempDir(), "effect")
	h.flow("f", `id: f
enabled: true
dry_run: true
subscribe: [src]
nodes:
  - id: look
    type: command
    lang: bash
    script: |
      cat > /dev/null
      echo "{\"dry\":\"$HAL_DRY_RUN\"}"
  - id: act
    type: command
    lang: bash
    side_effect: true
    script: |
      touch `+effect+`
      echo '{}'
  - {id: note, type: inbox, title: "dry={{.input.dry}}"}
edges:
  - {from: look, to: act}
  - {from: look, to: note}
`)
	h.start()
	id := h.waitRun(h.injectRun(`{"id":"1"}`), store.FlowRunSucceeded).ID
	if !h.run(id).DryRun {
		t.Error("flow run lost its dry_run flag")
	}
	act := h.node(id, "act")
	if act.Status != store.NodeSkipped || act.Decision != store.DecisionSkipped || act.Reason != "skipped:dry_run" {
		t.Errorf("side-effect node: %+v", act)
	}
	if _, err := os.Stat(effect); err == nil {
		t.Error("side-effect node ran in dry-run")
	}
	items := h.inboxItems()
	if len(items) != 1 || items[0].Title != "dry=1" || !items[0].DryRun {
		t.Errorf("inbox: %+v", items)
	}
}

// Scenario 10: replaying an event into an edited flow creates a new run linked
// to the original, on the new flow version.
func TestScenario10ReplayUsesCurrentFlowVersion(t *testing.T) {
	h := newHarness(t)
	h.idleSensor()
	flow := func(tag string) string {
		return "id: f\nenabled: true\nsubscribe: [src]\nnodes:\n  - {id: note, type: inbox, title: \"" + tag + " {{.input.id}}\"}\n"
	}
	h.flow("f", flow("v1"))
	h.start()
	res := h.inject(`{"id":"1"}`)
	orig := h.waitRun(res.FlowRunIDs[0], store.FlowRunSucceeded)

	h.flow("f", flow("v2"))
	h.reload()
	rp, err := h.eng.Replay(t.Context(), res.Event.ID, "f", false)
	if err != nil {
		t.Fatal(err)
	}
	if rp.Kind != store.KindReplay || rp.ReplayOf != orig.ID || rp.EventID != res.Event.ID || rp.FlowHash == orig.FlowHash || rp.DryRun {
		t.Fatalf("replay run: %+v (original %+v)", rp, orig)
	}
	h.waitRun(rp.ID, store.FlowRunSucceeded)
	v, err := h.st.GetFlowVersion(t.Context(), "f", rp.FlowHash)
	if err != nil || !strings.Contains(v.YAML, "v2") {
		t.Fatalf("flow version of the replay: %+v %v", v, err)
	}
	if v1, err := h.st.GetFlowVersion(t.Context(), "f", orig.FlowHash); err != nil || !strings.Contains(v1.YAML, "v1") {
		t.Fatalf("the original's flow version was lost: %+v %v", v1, err)
	}
	titles := map[string]bool{}
	for _, it := range h.inboxItems() {
		titles[it.Title] = true
	}
	if !titles["v1 1"] || !titles["v2 1"] {
		t.Errorf("inbox titles: %v", titles)
	}

	again, err := h.eng.Replay(t.Context(), res.Event.ID, "f", true)
	if err != nil || again.ReplayOf != rp.ID || !again.DryRun {
		t.Fatalf("second replay: %+v %v (want replay_of the first replay, dry-run)", again, err)
	}
	h.waitRun(again.ID, store.FlowRunSucceeded)

	if _, err := h.eng.Replay(t.Context(), res.Event.ID, "nope", false); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown flow: %v", err)
	}
	if _, err := h.eng.Replay(t.Context(), "01NOSUCHEVENT", "f", false); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown event: %v", err)
	}
}

// Scenario 11: running a node on a sample records a kind=test flow run that
// default timeline views hide, executes only that node, and can run an unsaved draft.
func TestScenario11TestNode(t *testing.T) {
	h := newHarness(t)
	h.idleSensor()
	h.flow("f", `id: f
enabled: true
subscribe: [src]
nodes:
  - id: show
    type: command
    lang: bash
    script: |
      cat
      echo "to stderr" >&2
  - {id: note, type: inbox, title: "should not run"}
edges:
  - {from: show, to: note}
`)
	h.start()

	fr, nr, err := h.eng.TestNode(t.Context(), core.TestNodeRequest{FlowID: "f", NodeID: "show", Input: []byte(`{"id":"sample"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if fr.Kind != store.KindTest || fr.Status != store.FlowRunSucceeded || nr.Status != store.NodeDone || nr.ExitCode == nil || *nr.ExitCode != 0 ||
		!strings.Contains(h.text(nr.Stdout), "sample") || !strings.Contains(h.text(nr.Stderr), "to stderr") {
		t.Fatalf("test run: %+v / %+v", fr, nr)
	}
	if got := len(h.nodeRuns(fr.ID)); got != 1 || len(h.inboxItems()) != 0 {
		t.Errorf("test run fed downstream: %d node runs, %d inbox items", got, len(h.inboxItems()))
	}
	if page, _ := h.st.ListFlowRuns(t.Context(), store.FlowRunFilter{}); len(page.Items) != 0 {
		t.Errorf("test run shows in the default timeline: %+v", page.Items)
	}

	// An unsaved draft runs instead of the file on disk, and is stored as the run's flow version.
	draft := spec.Flow{ID: "f", Enabled: true, Subscribe: []string{"src"}, Nodes: []spec.Node{
		{ID: "show", Type: spec.NodeCommand, Lang: spec.LangBash, Script: "cat >/dev/null; echo '{\"from\":\"draft\"}'"},
	}}
	fr2, nr2, err := h.eng.TestNode(t.Context(), core.TestNodeRequest{FlowID: "f", Flow: &draft, NodeID: "show", Input: []byte(`{}`)})
	if err != nil || !strings.Contains(h.text(nr2.Stdout), "draft") {
		t.Fatalf("draft test run: %v %+v", err, nr2)
	}
	v, err := h.st.GetFlowVersion(t.Context(), "f", fr2.FlowHash)
	if err != nil || !strings.Contains(v.YAML, "from") {
		t.Fatalf("draft flow version: %+v %v", v, err)
	}

	// The input can come from a stored event.
	res := h.inject(`{"id":"stored"}`)
	_, nr3, err := h.eng.TestNode(t.Context(), core.TestNodeRequest{FlowID: "f", NodeID: "show", EventID: res.Event.ID})
	if err != nil || !strings.Contains(h.text(nr3.Stdout), "stored") {
		t.Fatalf("event-input test run: %v %+v", err, nr3)
	}

	bad := draft.Clone()
	bad.Edges = []spec.Edge{{From: "show", To: "ghost"}}
	_, _, err = h.eng.TestNode(t.Context(), core.TestNodeRequest{FlowID: "f", Flow: &bad, NodeID: "show", Input: []byte(`{}`)})
	var ve *core.ValidationError
	if !errors.As(err, &ve) || !spec.HasErrors(ve.Diagnostics) {
		t.Errorf("invalid draft: %v, want *core.ValidationError", err)
	}
	if _, _, err := h.eng.TestNode(t.Context(), core.TestNodeRequest{FlowID: "f", NodeID: "ghost", Input: []byte(`{}`)}); !errors.Is(err, core.ErrUnknownRef) {
		t.Errorf("unknown node: %v", err)
	}
	if _, _, err := h.eng.TestNode(t.Context(), core.TestNodeRequest{FlowID: "nope", NodeID: "show", Input: []byte(`{}`)}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown flow: %v", err)
	}
	if _, _, err := h.eng.TestNode(t.Context(), core.TestNodeRequest{FlowID: "f", NodeID: "show"}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("no input: %v", err)
	}
}

// Inbox items notify when their priority reaches notify_min_priority (default
// normal), with a deep link to the item.
func TestInboxNotificationThreshold(t *testing.T) {
	flow := `id: f
enabled: true
subscribe: [src]
nodes:
  - {id: lo, type: inbox, title: "low", priority: low}
  - {id: no, type: inbox, title: "normal", priority: normal}
  - {id: hi, type: inbox, title: "high", priority: high}
  - {id: ur, type: inbox, title: "urgent", priority: urgent}
`
	for _, tc := range []struct {
		name, config string
		want         []string
	}{
		{"default is normal", "", []string{"normal", "high", "urgent"}},
		{"high", "notify_min_priority: high\n", []string{"high", "urgent"}},
		{"low", "notify_min_priority: low\n", []string{"low", "normal", "high", "urgent"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, withConfig(tc.config))
			h.idleSensor()
			h.flow("f", flow)
			h.start()
			h.waitRun(h.injectRun(`{"id":"1"}`), store.FlowRunSucceeded)
			byTitle := map[string]store.InboxItem{}
			for _, it := range h.inboxItems() {
				byTitle[it.Title] = it
			}
			if len(byTitle) != 4 {
				t.Fatalf("inbox items: %d, want 4 regardless of the threshold", len(byTitle))
			}
			got := map[string]core.Notification{}
			for _, n := range h.notes.kind("inbox") {
				got[n.Title] = n
			}
			if len(got) != len(tc.want) {
				t.Fatalf("notified %v, want %v", got, tc.want)
			}
			for _, title := range tc.want {
				n, ok := got[title]
				if !ok || n.URL != uiBase+"/inbox/"+byTitle[title].ID {
					t.Errorf("notification for %q: %+v (item %s)", title, n, byTitle[title].ID)
				}
			}
		})
	}
}

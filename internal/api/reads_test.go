package api

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"hal9001/internal/spec"
	"hal9001/internal/store"
)

func TestEventTimelineFiltersAndPagination(t *testing.T) {
	e := newEnv(t)
	e1 := e.event("gh", "1", `{"n":1}`)
	e2 := e.event("gh", "2", `{"n":2}`)
	e3 := e.event("gh", "3", `{"n":3}`)
	e4 := e.event("gh", "4", `{"n":4}`)
	e5 := e.event("gh", "5", `{"needle":true}`)
	e6 := e.event("slack", "6", `{"long":"`+strings.Repeat("y", 2000)+`"}`)

	a1 := e.flowRun("A", e1.ID, store.KindLive, store.FlowRunSucceeded)
	e.nodeRun(a1.ID, "filter", store.NodeDone, func(n *store.NodeRun) {
		n.Decision, n.Reason = store.DecisionDropped, "not urgent"
	})
	e.flowRun("A", e2.ID, store.KindLive, store.FlowRunFailed)
	e.flowRun("B", e3.ID, store.KindReplay, store.FlowRunRunning)
	e.flowRun("A", e4.ID, store.KindTest, store.FlowRunSucceeded)
	e.flowRun("A", e6.ID, store.KindLive, store.FlowRunSucceeded)

	all := []string{e6.ID, e5.ID, e4.ID, e3.ID, e2.ID, e1.ID}
	ids, pages := e.walk("/api/events", 2)
	if strings.Join(ids, ",") != strings.Join(all, ",") || pages != 3 {
		t.Errorf("walking 2 at a time: %d pages, ids %v, want newest first %v", pages, ids, all)
	}

	for _, tc := range []struct {
		name  string
		query string
		want  []string
	}{
		{"by sensor", "sensor_id=slack", []string{e6.ID}},
		{"by flow hides test runs", "flow_id=A", []string{e6.ID, e2.ID, e1.ID}},
		{"by flow with kind test", "flow_id=A&kind=test", []string{e4.ID}},
		{"by run status", "status=failed", []string{e2.ID}},
		{"by node decision", "decision=dropped", []string{e1.ID}},
		{"replay kind", "kind=replay", []string{e3.ID}},
		{"payload search", "q=NEEDLE", []string{e5.ID}},
		{"since is inclusive", "since=" + url.QueryEscape(e3.CreatedAt.Format(time.RFC3339Nano)), []string{e6.ID, e5.ID, e4.ID, e3.ID}},
		{"until is exclusive", "until=" + url.QueryEscape(e3.CreatedAt.Format(time.RFC3339Nano)), []string{e2.ID, e1.ID}},
	} {
		if got := e.get("/api/events?" + tc.query).want(200).ids(); strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}

	var list struct {
		Items []struct {
			ID             string `json:"id"`
			PayloadPreview string `json:"payload_preview"`
			FlowRuns       []struct {
				FlowID string `json:"flow_id"`
				Kind   string `json:"kind"`
			} `json:"flow_runs"`
		} `json:"items"`
	}
	e.get("/api/events").want(200).decode(&list)
	byID := map[string]int{}
	for i, it := range list.Items {
		byID[it.ID] = i
	}
	if runs := list.Items[byID[e4.ID]].FlowRuns; len(runs) != 0 {
		t.Errorf("test runs must not appear in the default timeline: %+v", runs)
	}
	if runs := list.Items[byID[e3.ID]].FlowRuns; len(runs) != 1 || runs[0].FlowID != "B" || runs[0].Kind != "replay" {
		t.Errorf("replay run missing from its event: %+v", runs)
	}
	if n := len([]rune(list.Items[byID[e6.ID]].PayloadPreview)); n != 512 {
		t.Errorf("payload_preview has %d characters, want it cut to 512", n)
	}

	for _, bad := range []string{"limit=0", "limit=201", "limit=x", "status=bogus", "kind=live,test", "cursor=nope", "since=yesterday", "q=", "sensor_id=has%20space"} {
		if r := e.get("/api/events?" + bad).want(400); r.problem() != "bad-request" {
			t.Errorf("%s: problem %q", bad, r.problem())
		}
	}
}

func TestEventDetailShowsTheFanOutTreeWithPreviews(t *testing.T) {
	e := newEnv(t)
	ev := e.event("gh", "1", `{"pr":7}`)
	fr := e.flowRun("demo", ev.ID, store.KindLive, store.FlowRunFailed)
	root := e.nodeRun(fr.ID, "filter", store.NodeDone, func(n *store.NodeRun) {
		n.Decision, n.Stdout = store.DecisionEmitted, store.Inline(strings.Repeat("a", 10_000))
	})
	e.nodeRun(fr.ID, "after", store.NodeFailed, func(n *store.NodeRun) {
		n.ParentNodeRunID, n.Decision, n.Error = root.ID, store.DecisionError, "exit_nonzero"
	})

	var detail struct {
		Event  struct{ Payload map[string]any } `json:"event"`
		FanOut []struct {
			FlowRun  struct{ ID string } `json:"flow_run"`
			NodeRuns []struct {
				NodeRun struct {
					NodeID string `json:"node_id"`
					Stdout struct {
						Text      string `json:"text"`
						Size      int    `json:"size"`
						Truncated bool   `json:"truncated"`
					} `json:"stdout"`
					Node struct{ Type, Lang string } `json:"node"`
				} `json:"node_run"`
				Children []struct {
					NodeRun struct {
						NodeID   string `json:"node_id"`
						Decision string `json:"decision"`
					} `json:"node_run"`
				} `json:"children"`
			} `json:"node_runs"`
		} `json:"fan_out"`
	}
	e.get("/api/events/" + ev.ID).want(200).decode(&detail)
	if detail.Event.Payload["pr"] != float64(7) || len(detail.FanOut) != 1 || detail.FanOut[0].FlowRun.ID != fr.ID {
		t.Fatalf("detail = %+v", detail)
	}
	root0 := detail.FanOut[0].NodeRuns[0]
	if root0.NodeRun.NodeID != "filter" || len(root0.Children) != 1 || root0.Children[0].NodeRun.Decision != "error" {
		t.Errorf("tree = %+v", root0)
	}
	if out := root0.NodeRun.Stdout; len(out.Text) != 4096 || out.Size != 10_000 || !out.Truncated {
		t.Errorf("stdout preview = %d bytes, size %d, truncated %v; want 4096 of 10000", len(out.Text), out.Size, out.Truncated)
	}
	if root0.NodeRun.Node.Type != "command" || root0.NodeRun.Node.Lang != "jq" {
		t.Errorf("node snapshot should come from the flow version the run used: %+v", root0.NodeRun.Node)
	}
	e.get("/api/events/01JC7Z3K9Q8V5T2M4N6P8R0S1T").want(404)
}

func TestFlowRunsListFiltersAndHidesTestRuns(t *testing.T) {
	e := newEnv(t)
	ev := e.event("gh", "1", `{}`)
	live := e.flowRun("A", ev.ID, store.KindLive, store.FlowRunSucceeded)
	failed := e.flowRun("A", "", store.KindLive, store.FlowRunFailed)
	test := e.flowRun("A", "", store.KindTest, store.FlowRunSucceeded)
	other := e.flowRun("B", ev.ID, store.KindReplay, store.FlowRunQueued)

	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"", []string{other.ID, failed.ID, live.ID}},
		{"flow_id=A", []string{failed.ID, live.ID}},
		{"kind=test", []string{test.ID}},
		{"status=failed", []string{failed.ID}},
		{"event_id=" + ev.ID, []string{other.ID, live.ID}},
	} {
		if got := e.get("/api/flow-runs?" + tc.query).want(200).ids(); strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("?%s: got %v, want %v", tc.query, got, tc.want)
		}
	}
	if ids, pages := e.walk("/api/flow-runs", 1); len(ids) != 3 || pages != 3 {
		t.Errorf("one at a time: %d ids over %d pages", len(ids), pages)
	}
	e.get("/api/flow-runs?replay_of=lower").want(400)
}

func TestFlowRunLongPollWakesOnChangeAndTimesOut(t *testing.T) {
	e := newEnv(t)
	fr := e.flowRun("A", "", store.KindLive, store.FlowRunQueued)
	e.nodeRun(fr.ID, "filter", store.NodeQueued, nil)
	path := "/api/flow-runs/" + fr.ID

	status := func(r *resp) string {
		var d struct {
			FlowRun struct{ Status string } `json:"flow_run"`
		}
		r.decode(&d)
		return d.FlowRun.Status
	}
	setStatus := func(s store.FlowRunStatus) {
		fr.Status = s
		must(t, e.st.UpdateFlowRun(ctx(), fr))
	}

	// Wakes when the run reaches a terminal status, long before the wait ends.
	wait := e.async(path + "?wait=1m")
	time.Sleep(100 * time.Millisecond)
	setStatus(store.FlowRunRunning) // not settled: keeps waiting
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	setStatus(store.FlowRunSucceeded)
	r := wait().want(200)
	if got := status(r); got != "succeeded" {
		t.Errorf("woke with status %q", got)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("long poll took %v to notice the change", d)
	}

	// awaiting_approval also ends the wait.
	fr2 := e.flowRun("A", "", store.KindLive, store.FlowRunRunning)
	wait = e.async("/api/flow-runs/" + fr2.ID + "?wait=1m")
	time.Sleep(100 * time.Millisecond)
	fr2.Status = store.FlowRunAwaitingApproval
	must(t, e.st.UpdateFlowRun(ctx(), fr2))
	if got := status(wait().want(200)); got != "awaiting_approval" {
		t.Errorf("status = %q", got)
	}

	// A run that does not settle returns its current state when the wait ends.
	fr3 := e.flowRun("A", "", store.KindLive, store.FlowRunRunning)
	start = time.Now()
	r = e.get("/api/flow-runs/" + fr3.ID + "?wait=300ms").want(200)
	if d := time.Since(start); d < 250*time.Millisecond || d > 3*time.Second {
		t.Errorf("wait=300ms returned after %v", d)
	}
	if got := status(r); got != "running" {
		t.Errorf("timed-out poll reports %q", got)
	}

	// Already settled: no waiting at all.
	start = time.Now()
	e.get(path + "?wait=1m").want(200)
	if time.Since(start) > time.Second {
		t.Error("a settled run must return immediately")
	}
	e.get(path + "?wait=soon").want(400)
	e.get("/api/flow-runs/01JC7Z3K9Q8V5T2M4N6P8R0S1T?wait=1s").want(404)
}

func TestNodeRunAndSensorRunWait(t *testing.T) {
	e := newEnv(t)
	fr := e.flowRun("A", "", store.KindLive, store.FlowRunRunning)
	nr := e.nodeRun(fr.ID, "filter", store.NodeRunning, nil)
	wait := e.async("/api/node-runs/" + nr.ID + "?wait=1m")
	time.Sleep(100 * time.Millisecond)
	nr.Status, nr.Decision = store.NodeDone, store.DecisionEmitted
	must(t, e.st.UpdateNodeRun(ctx(), nr))
	if got := wait().want(200).json()["status"]; got != "done" {
		t.Errorf("node run status = %v", got)
	}

	run := &store.SensorRun{SensorID: "demo-ticker", Origin: store.OriginManual}
	must(t, e.st.CreateSensorRun(ctx(), run))
	wait = e.async("/api/sensor-runs/" + run.ID + "?wait=1m")
	time.Sleep(100 * time.Millisecond)
	run.Status = store.SensorSucceeded
	end := time.Now()
	run.EndedAt = &end
	must(t, e.st.UpdateSensorRun(ctx(), run))
	got := wait().want(200).json()
	if got["status"] != "succeeded" || got["trigger"] != "manual" {
		t.Errorf("sensor run = %v", got)
	}
}

func TestSensorRunStatusFilterSeesTimeoutAndKilledAsFailedClasses(t *testing.T) {
	e := newEnv(t)
	mk := func(status store.SensorStatus, class string) *store.SensorRun {
		now := time.Now()
		r := &store.SensorRun{SensorID: "demo-ticker", Status: status, Error: class, EndedAt: &now}
		must(t, e.st.CreateSensorRun(ctx(), r))
		return r
	}
	ok := mk(store.SensorSucceeded, "")
	exit := mk(store.SensorFailed, "exit_nonzero")
	slow := mk(store.SensorFailed, "timeout")
	killed := mk(store.SensorFailed, "killed")

	base := "/api/sensors/demo-ticker/runs"
	for _, tc := range []struct {
		status string
		want   []string
	}{
		{"", []string{killed.ID, slow.ID, exit.ID, ok.ID}},
		{"succeeded", []string{ok.ID}},
		{"failed", []string{exit.ID}},
		{"timeout", []string{slow.ID}},
		{"killed", []string{killed.ID}},
	} {
		path := base
		if tc.status != "" {
			path += "?status=" + tc.status
		}
		if got := e.get(path).want(200).ids(); strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("status=%q: got %v, want %v", tc.status, got, tc.want)
		}
	}
	// Filtering by a derived status still paginates.
	mk(store.SensorFailed, "timeout")
	mk(store.SensorFailed, "timeout")
	if ids, pages := e.walk(base+"?status=timeout", 1); len(ids) != 3 || pages != 3 {
		t.Errorf("timeout runs one at a time: %d ids, %d pages", len(ids), pages)
	}
	e.get("/api/sensors/not-a-sensor/runs").want(404)
}

func TestTextBlobShapesAndBlobDownload(t *testing.T) {
	e := newEnv(t)
	fr := e.flowRun("demo", "", store.KindLive, store.FlowRunSucceeded)
	big := strings.Repeat("é", 40_000) // 80 000 bytes: spilled to a blob
	nr := e.nodeRun(fr.ID, "filter", store.NodeDone, func(n *store.NodeRun) {
		n.Stdin, n.Stdout, n.Stderr = store.Inline(`{"a":1}`), store.Inline(big), store.Inline("")
	})

	var got struct {
		Stdin, Stdout, Stderr struct {
			Text      string  `json:"text"`
			Size      int     `json:"size"`
			Truncated bool    `json:"truncated"`
			Blob      *string `json:"blob"`
		}
	}
	e.get("/api/node-runs/" + nr.ID).want(200).decode(&got)
	if got.Stdin.Text != `{"a":1}` || got.Stdin.Size != 7 || got.Stdin.Truncated || got.Stdin.Blob != nil {
		t.Errorf("inline stdin = %+v", got.Stdin)
	}
	if got.Stderr.Text != "" || got.Stderr.Size != 0 || got.Stderr.Truncated || got.Stderr.Blob != nil {
		t.Errorf("empty stderr = %+v", got.Stderr)
	}
	out := got.Stdout
	if out.Size != 80_000 || !out.Truncated || out.Blob == nil || len(out.Text) > 64<<10 || len(out.Text) < 60<<10 || !strings.HasPrefix(big, out.Text) {
		t.Fatalf("spilled stdout: size %d truncated %v blob %v text %d bytes", out.Size, out.Truncated, out.Blob, len(out.Text))
	}

	r := e.get("/api/blobs/" + *out.Blob).want(200)
	if string(r.Body) != big || r.Header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("blob download: %d bytes, type %q", len(r.Body), r.Header.Get("Content-Type"))
	}
	e.get("/api/blobs/" + strings.Repeat("0", 64)).want(404)
	e.get("/api/blobs/not-a-hash").want(400)

	// The same run in a flow run detail uses the detail limit; in a stream-style
	// preview it would be 4 KiB (covered by the event detail test).
	var detail struct {
		NodeRuns []struct {
			Stdout struct{ Size int } `json:"stdout"`
		} `json:"node_runs"`
	}
	e.get("/api/flow-runs/" + fr.ID).want(200).decode(&detail)
	if len(detail.NodeRuns) != 1 || detail.NodeRuns[0].Stdout.Size != 80_000 {
		t.Errorf("flow run detail = %+v", detail)
	}
}

func TestFlowVersionsListAndGet(t *testing.T) {
	e := newEnv(t)
	hash1 := e.flowRun("demo", "", store.KindLive, store.FlowRunSucceeded).FlowHash
	e.flowRun("demo", "", store.KindLive, store.FlowRunSucceeded)
	v2 := demoFlowYAML + "# second version\n"
	h2 := spec.Hash([]byte(v2))
	must(t, e.st.CreateFlowRun(ctx(), &store.FlowRun{FlowID: "demo", FlowHash: h2, FlowYAML: v2}))

	var list struct {
		Items []struct {
			Hash     string `json:"hash"`
			RunCount int    `json:"run_count"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	e.get("/api/flows/demo/versions").want(200).decode(&list)
	counts := map[string]int{}
	for _, v := range list.Items {
		counts[v.Hash] = v.RunCount
	}
	if len(list.Items) != 2 || counts[hash1] != 2 || counts[h2] != 1 {
		t.Errorf("versions = %+v", list.Items)
	}
	e.get("/api/flows/demo/versions?limit=1").want(200).decode(&list)
	if len(list.Items) != 1 || list.NextCursor == nil {
		t.Fatalf("first page = %+v", list)
	}
	var next struct {
		Items []struct{ Hash string } `json:"items"`
	}
	e.get("/api/flows/demo/versions?limit=1&cursor=" + *list.NextCursor).want(200).decode(&next)
	if len(next.Items) != 1 || next.Items[0].Hash == list.Items[0].Hash {
		t.Errorf("second page repeats the first: %+v", next)
	}

	var one struct {
		YAML string              `json:"yaml"`
		Flow struct{ ID string } `json:"flow"`
	}
	e.get("/api/flows/demo/versions/" + h2).want(200).decode(&one)
	if one.YAML != v2 || one.Flow.ID != "demo" {
		t.Errorf("version = %+v", one)
	}
	e.get("/api/flows/demo/versions/" + strings.Repeat("0", 64)).want(404)
	e.get("/api/flows/never-existed/versions").want(404)
}

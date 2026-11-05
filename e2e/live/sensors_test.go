package live

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"hal9001/content"
)

type sensorStatus struct {
	Status struct {
		State         string     `json:"state"`
		LastRun       *sensorRun `json:"last_run"`
		LastSuccessAt *string    `json:"last_success_at"`
	} `json:"status"`
}

// scheduledRun waits for the scheduler's immediate first poll of a sensor that
// an enabled flow just subscribed to, and returns it.
func (s *stack) scheduledRun(sensor string) sensorRun {
	s.t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		runs := s.sensorRuns(sensor)
		if n := len(runs); n > 0 && runs[n-1].Status != "running" {
			var run sensorRun
			s.must(http.StatusOK, http.MethodGet, "/api/sensor-runs/"+runs[n-1].ID, nil, &run)
			return run
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("sensor %s has no finished run after 2m", sensor)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (s *stack) eventsOf(sensor, q string) []eventSummary {
	s.t.Helper()
	var all []eventSummary
	cursor := ""
	for {
		var page struct {
			Items      []eventSummary `json:"items"`
			NextCursor *string        `json:"next_cursor"`
		}
		path := "/api/events?limit=200&sensor_id=" + url.QueryEscape(sensor)
		if q != "" {
			path += "&q=" + url.QueryEscape(q)
		}
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		s.must(http.StatusOK, http.MethodGet, path, nil, &page)
		all = append(all, page.Items...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			return all
		}
		cursor = *page.NextCursor
	}
}

type eventSummary struct {
	ID       string         `json:"id"`
	DedupKey string         `json:"dedup_key"`
	FlowRuns []flowRunBrief `json:"flow_runs"`
}

// Real `gh`, read-only: first poll baselines (no events); a copy with
// first_run: fire creates events; the triage-github flow in dry-run routes
// review requests by size with the agent forced readonly.
func TestLiveGitHubNotifications(t *testing.T) {
	requireLive(t)
	if out, err := exec.Command("gh", "auth", "status").CombinedOutput(); err != nil {
		t.Skipf("gh is not authenticated: %v\n%s", err, out)
	}
	s := startStack(t, stackOpts{keepSensors: []string{"github-notifications"}})
	s.seedPrompts()

	// 1. (the sensor is not subscribed by an enabled flow, so nothing polls it until we do)
	// first_run: baseline marks everything seen and fires nothing.
	base := s.pollSensor("github-notifications")
	events := s.eventsOf("github-notifications", "")
	var st sensorStatus
	s.must(http.StatusOK, http.MethodGet, "/api/sensors/github-notifications", nil, &st)
	evidence(t, "github-a-baseline", map[string]any{"sensor_run": base, "events": len(events), "status": st.Status})
	if base.Status != "succeeded" || !base.Baseline {
		t.Fatalf("first poll: status=%s baseline=%v stderr=%v", base.Status, base.Baseline, base.Stderr)
	}
	if base.ItemCount == nil || *base.ItemCount == 0 {
		t.Skip("the gh account has no notifications; nothing to baseline")
	}
	if base.NewCount == nil || *base.NewCount != 0 || len(events) != 0 {
		t.Fatalf("baseline poll created events: new_count=%v events=%d, want 0", base.NewCount, len(events))
	}
	if st.Status.LastSuccessAt == nil {
		t.Error("baseline poll did not set last_success_at")
	}
	// Same notifications again: still nothing new (their keys are claimed).
	again := s.pollSensor("github-notifications")
	if again.Status != "succeeded" || again.Baseline || again.NewCount == nil {
		t.Fatalf("second poll: %+v", again)
	}
	t.Logf("second poll: items=%d new=%d (new > 0 only if a notification changed in the meantime)", *again.ItemCount, *again.NewCount)

	// 2. Pick review-requested PRs for the dry-run flow: one small and one large when available.
	picks := pickReviewRequests(t)
	if len(picks) == 0 {
		t.Skip("no review-requested pull requests among the notifications")
	}

	// 3. Temp copy of the sensor with first_run: fire, and a copy of triage-github that
	// only lets the picked notifications through and runs the agent in a temp dir.
	sensorYAML := readSeed(t, "sensors/github-notifications.yaml")
	fire := strings.Replace(sensorYAML, "id: github-notifications\n", "id: github-notifications-fire\n", 1)
	fire = mustReplace(t, fire, "first_run: baseline", "first_run: fire")
	s.createSensor(fire)

	ids := make([]string, 0, len(picks))
	for _, p := range picks {
		ids = append(ids, fmt.Sprintf("%q", p.NotificationID))
	}
	flow := readSeed(t, "flows/triage-github.yaml")
	flow = mustReplace(t, flow, "id: triage-github\n", "id: live-triage-github\n")
	flow = mustReplace(t, flow, "enabled: false", "enabled: true")
	flow = mustReplace(t, flow, "subscribe: [github-notifications]", "subscribe: [github-notifications-fire]")
	flow = mustReplace(t, flow, `select(.reason == "review_requested" and .type == "PullRequest")`,
		fmt.Sprintf(`select(.reason == "review_requested" and .type == "PullRequest" and (.id | IN(%s)))`, strings.Join(ids, ", ")))
	flow = mustReplace(t, flow, `cwd: {mode: worktree, repo: "~/code/{{.input.repository.name}}", ref: "origin/main"}`,
		fmt.Sprintf("cwd: {mode: dir, path: %q}\n    model: haiku", t.TempDir()))
	flow = mustReplace(t, flow, "timeout: 20m", "timeout: 6m")
	s.createFlow(flow)

	// Subscribing the flow starts the sensor's scheduler, which polls at once.
	fireRun := s.scheduledRun("github-notifications-fire")
	evs := s.eventsOf("github-notifications-fire", "")
	evidence(t, "github-b-fire", map[string]any{"sensor_run": fireRun, "events": len(evs), "picks": picks})
	if fireRun.Status != "succeeded" || fireRun.Baseline || fireRun.ItemCount == nil || fireRun.NewCount == nil {
		t.Fatalf("fire poll: %+v", fireRun)
	}
	// The live feed can repeat a notification across pages while it changes,
	// and identical keys dedup within a poll, so allow a few fewer events than items.
	if len(evs) != *fireRun.NewCount || *fireRun.ItemCount-*fireRun.NewCount > 5 || *fireRun.NewCount == 0 {
		t.Errorf("fire poll: items=%d new=%d events=%d, want every (distinct) item to become an event", *fireRun.ItemCount, *fireRun.NewCount, len(evs))
	}

	// 4. Routing of the picked notifications, and dropping of the rest.
	for _, p := range picks {
		ev := s.eventsOf("github-notifications-fire", p.NotificationID)
		if len(ev) != 1 || len(ev[0].FlowRuns) != 1 {
			t.Fatalf("%s: events=%+v, want one event with one flow run", p.Title, ev)
		}
		det := s.waitFlowRun(ev[0].FlowRuns[0].ID, 10*time.Minute)
		runs := s.agentRuns(det.FlowRun.ID)
		var reqs []permissionRequest
		for _, r := range runs {
			reqs = append(reqs, s.permissionRequests(r.ID)...)
		}
		inbox := s.inboxFor("live-triage-github")
		evidence(t, "github-c-"+p.NotificationID, map[string]any{"pick": p, "flow_run": det, "agent_runs": runs, "permission_requests": reqs})

		if !det.FlowRun.DryRun || det.FlowRun.Status != "succeeded" {
			t.Fatalf("%s: dry_run=%v status=%s error=%v", p.Title, det.FlowRun.DryRun, det.FlowRun.Status, deref(det.FlowRun.Error))
		}
		got := map[string]string{}
		for _, nr := range det.NodeRuns {
			got[nr.NodeID] = nr.Status + "/" + deref(nr.Decision)
		}
		if got["only-review-requests"] != "done/emitted" {
			t.Errorf("%s: filter node = %q, want done/emitted", p.Title, got["only-review-requests"])
		}
		wantNode := "note-large"
		if p.Lines < 200 {
			wantNode = "review-small"
		}
		if _, ok := got[wantNode]; !ok {
			t.Errorf("%s: %d changed lines should reach %s, node runs = %v", p.Title, p.Lines, wantNode, got)
		}
		if p.Lines < 200 {
			if len(runs) != 1 || runs[0].Mode != "readonly" || runs[0].Policy != "readonly" {
				t.Errorf("%s: agent runs = %+v, want one readonly-policy run (forced by dry-run)", p.Title, runs)
			}
			for _, r := range reqs {
				if r.ToolKind != "read" && r.ToolKind != "search" && r.ToolKind != "think" && r.ToolKind != "fetch" &&
					(r.Decision == nil || *r.Decision == "allow") && !strings.HasPrefix(deref(r.MatchedRule), "allow.commands") {
					t.Errorf("%s: non-read request %q was allowed (%s by %s)", p.Title, r.Title, deref(r.Decision), deref(r.DecidedBy))
				}
			}
		} else if len(runs) != 0 {
			t.Errorf("%s: large PR started an agent run", p.Title)
		}
		found := false
		for _, it := range inbox {
			if strings.Contains(it.SummaryMD, p.URL) || strings.Contains(it.Title, p.Title) || (it.AgentRunID != nil && len(runs) == 1 && *it.AgentRunID == runs[0].ID) {
				found = true
				if !it.DryRun {
					t.Errorf("%s: inbox item %q is not tagged dry-run", p.Title, it.Title)
				}
			}
		}
		if !found {
			t.Errorf("%s: no inbox item for the PR among %d items", p.Title, len(inbox))
		}
	}
	// Everything else was dropped by the filter, not failed.
	other := 0
	for _, e := range evs {
		isPick := false
		for _, p := range picks {
			if strings.HasPrefix(e.DedupKey, p.NotificationID+":") {
				isPick = true
			}
		}
		if isPick || other >= 5 || len(e.FlowRuns) != 1 {
			continue
		}
		other++
		det := s.waitFlowRun(e.FlowRuns[0].ID, 2*time.Minute)
		if det.FlowRun.Status != "succeeded" || len(det.NodeRuns) != 1 || deref(det.NodeRuns[0].Decision) != "dropped" {
			t.Errorf("event %s: status=%s node runs=%d decision=%v, want a dropped branch", e.ID, det.FlowRun.Status, len(det.NodeRuns), det.NodeRuns)
		}
	}
}

// pickedPR is a review-requested pull request chosen for the dry-run flow.
type pickedPR struct {
	NotificationID string `json:"notification_id"`
	Title          string `json:"title"`
	URL            string `json:"url"`
	Lines          int    `json:"lines_changed"`
}

// pickReviewRequests returns up to one small (<200 changed lines) and one large
// review-requested PR from the real notifications, sizes taken from gh directly.
func pickReviewRequests(t *testing.T) []pickedPR {
	t.Helper()
	out, err := exec.Command("gh", "api", "notifications", "-q",
		`[.[] | select(.reason=="review_requested" and .subject.type=="PullRequest")][:25][] | [.id, .subject.title, .subject.url] | @tsv`).Output()
	if err != nil {
		t.Fatalf("gh api notifications: %v", err)
	}
	var small, large *pickedPR
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		sz, err := exec.Command("gh", "api", strings.TrimPrefix(f[2], "https://api.github.com/"), "-q", ".additions + .deletions").Output()
		if err != nil {
			continue
		}
		var n int
		if _, err := fmt.Sscan(strings.TrimSpace(string(sz)), &n); err != nil {
			continue
		}
		p := &pickedPR{NotificationID: f[0], Title: f[1], Lines: n,
			URL: strings.NewReplacer("https://api.github.com/repos/", "https://github.com/", "/pulls/", "/pull/").Replace(f[2])}
		switch {
		case n < 200 && small == nil:
			small = p
		case n >= 200 && large == nil:
			large = p
		}
		if small != nil && large != nil {
			break
		}
	}
	var picks []pickedPR
	if small != nil {
		picks = append(picks, *small)
	}
	if large != nil {
		picks = append(picks, *large)
	}
	return picks
}

func readSeed(t *testing.T, name string) string {
	t.Helper()
	b, err := content.FS.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustReplace(t *testing.T, s, old, repl string) string {
	t.Helper()
	if !strings.Contains(s, old) {
		t.Fatalf("seed no longer contains %q; update the live test", old)
	}
	return strings.Replace(s, old, repl, 1)
}

// Slack, Linear and Gmail have no credentials here: the sensors must fail
// visibly (non-zero exit, a clear stderr) and must not advance last_success_at.
func TestLiveCredentialSensorsFailVisibly(t *testing.T) {
	requireLive(t)
	s := startStack(t, stackOpts{keepSensors: []string{"slack-mentions-dms", "linear-assigned", "gmail-unread"}})
	cases := []struct {
		sensor string
		want   string
		skip   func() string
	}{
		{"slack-mentions-dms", "SLACK_USER_TOKEN", nil},
		{"linear-assigned", "LINEAR_API_KEY", nil},
		{"gmail-unread", "gog", func() string {
			if p, err := exec.LookPath("gog"); err == nil {
				return "gog is installed at " + p + "; the failure path cannot be observed"
			}
			return ""
		}},
	}
	for _, c := range cases {
		t.Run(c.sensor, func(t *testing.T) {
			s := &stack{t: t, home: s.home, base: s.base, http: s.http}
			if c.skip != nil {
				if why := c.skip(); why != "" {
					t.Skip(why)
				}
			}
			run := s.pollSensor(c.sensor)
			var st sensorStatus
			s.must(http.StatusOK, http.MethodGet, "/api/sensors/"+c.sensor, nil, &st)
			events := s.eventsOf(c.sensor, "")
			evidence(t, "sensor-"+c.sensor, map[string]any{"sensor_run": run, "status": st.Status, "events": len(events)})
			stderr := textOf(run.Stderr)
			if run.Status != "failed" || run.ExitCode == nil || *run.ExitCode == 0 {
				t.Fatalf("status=%s exit=%v, want a failed poll with a non-zero exit", run.Status, run.ExitCode)
			}
			if !strings.Contains(stderr, c.want) {
				t.Errorf("stderr = %q, want it to mention %s", stderr, c.want)
			}
			if st.Status.LastSuccessAt != nil || st.Status.State != "failing" {
				t.Errorf("last_success_at=%v state=%s, want unset and failing", deref(st.Status.LastSuccessAt), st.Status.State)
			}
			if len(events) != 0 {
				t.Errorf("%d events from a failed poll", len(events))
			}
		})
	}
}

// textOf extracts the inline text of a TextBlob.
func textOf(v any) string {
	b, _ := json.Marshal(v)
	var tb struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(b, &tb)
	return tb.Text
}

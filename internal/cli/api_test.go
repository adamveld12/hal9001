package cli_test

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"hal9001/internal/cli"
)

func TestReadCommandsHitTheRightEndpointsAndPrintTheResponseAsJSON(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		route string
		query url.Values
		body  any
	}{
		{"status", []string{"status"}, "GET /api/status", nil, statusBody()},
		{"sensors list", []string{"sensors", "list"}, "GET /api/sensors", nil, map[string]any{"items": []any{sensorSummary()}}},
		{"sensors get", []string{"sensors", "get", "github-notifications"}, "GET /api/sensors/github-notifications", nil,
			func() map[string]any { d := sensorSummary(); d["yaml"] = "id: github-notifications\n"; return d }()},
		{"events list filters", []string{"events", "list", "--sensor", "gh", "--status", "failed", "--q", "pr", "--limit", "5", "--cursor", id1},
			"GET /api/events", url.Values{"sensor_id": {"gh"}, "status": {"failed"}, "q": {"pr"}, "limit": {"5"}, "cursor": {id1}}, page(eventSummary())},
		{"runs list", []string{"runs", "list", "--flow", "triage-github", "--kind", "replay", "--replay-of", id1},
			"GET /api/flow-runs", url.Values{"flow_id": {"triage-github"}, "kind": {"replay"}, "replay_of": {id1}}, page(flowRun(id1, "succeeded"))},
		{"runs get", []string{"runs", "get", id1}, "GET /api/flow-runs/" + id1, nil, flowRunDetail("succeeded")},
		{"node-runs get", []string{"node-runs", "get", id3}, "GET /api/node-runs/" + id3, nil,
			func() map[string]any { n := nodeRun("done"); n["agent_runs"] = []any{}; return n }()},
		{"agent-runs list", []string{"agent-runs", "list", "--runtime", "claude", "--flow-run", id1},
			"GET /api/agent-runs", url.Values{"runtime": {"claude"}, "flow_run_id": {id1}}, page(agentRun("running"))},
		{"agent-runs get", []string{"agent-runs", "get", id2}, "GET /api/agent-runs/" + id2, nil,
			func() map[string]any {
				d := agentRun("succeeded")
				d["phase_run_ids"] = []string{id2}
				d["approvals"] = []any{}
				return d
			}()},
		{"agent-runs events", []string{"agent-runs", "events", id2, "--after-seq", "4", "--kind", "message", "--limit", "10"},
			"GET /api/agent-runs/" + id2 + "/events", url.Values{"after_seq": {"4"}, "kind": {"message"}, "limit": {"10"}},
			map[string]any{"items": []any{agentEvent(5)}, "last_seq": 5, "has_more": false}},
		{"approvals list", []string{"approvals", "list", "--status", "pending", "--type", "tool_call"},
			"GET /api/approvals", url.Values{"status": {"pending"}, "type": {"tool_call"}}, page(approval("pending"))},
		{"approvals get", []string{"approvals", "get", id1}, "GET /api/approvals/" + id1, nil, approval("pending")},
		{"inbox list unread", []string{"inbox", "list", "--unread", "--starred", "--priority", "high"},
			"GET /api/inbox", url.Values{"read": {"false"}, "starred": {"true"}, "priority": {"high"}}, page(inboxItem(false))},
		{"inbox get", []string{"inbox", "get", id1}, "GET /api/inbox/" + id1, nil, inboxItem(true)},
		{"killswitch get", []string{"killswitch", "get"}, "GET /api/killswitch", nil, killSwitch(false)},
		{"runtimes list", []string{"runtimes", "list"}, "GET /api/runtimes", nil, map[string]any{"items": []any{
			map[string]any{"name": "claude", "command": []string{"claude-agent-acp"}, "modes": map[string]any{"ask": "default"}, "last_probe": nil}}}},
		{"env list", []string{"env", "list"}, "GET /api/env", nil, map[string]any{"items": []any{map[string]any{"key": "GITHUB_TOKEN"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDaemon(t)
			f.on(tc.route, ok(tc.body))
			res := f.run(runOpts{}, tc.args...)
			if res.code != 0 {
				t.Fatalf("exit %d, stderr: %s", res.code, res.stderr)
			}
			req := f.only()
			if req.Method+" "+req.Path != tc.route {
				t.Errorf("request = %s %s, want %s", req.Method, req.Path, tc.route)
			}
			for k, v := range tc.query {
				if got := req.Query.Get(k); got != v[0] {
					t.Errorf("query %s = %q, want %q (all: %v)", k, got, v[0], req.Query)
				}
			}
			if len(req.Query) != len(tc.query) {
				t.Errorf("query = %v, want exactly %v", req.Query, tc.query)
			}
			if !jsonEqual(t, res.stdout, tc.body) {
				t.Errorf("stdout is not the response body:\n%s", res.stdout)
			}
			if res.stderr != "" {
				t.Errorf("stderr = %q", res.stderr)
			}
		})
	}
}

func TestMutatingCommandsSendJSONBodiesWithJSONContentType(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		stdin     string
		route     string
		wantQuery url.Values
		wantBody  any
		resp      canned
	}{
		{"sensors poll sends an empty JSON object", []string{"sensors", "poll", "gh"}, "", "POST /api/sensors/gh/poll", nil, map[string]any{},
			canned{status: 202, body: sensorRun(), location: "/api/sensor-runs/" + id1}},
		{"events inject posts the item with the sensor in the query", []string{"events", "inject", "github-notifications"}, `{"id":"42","reason":"review_requested"}`,
			"POST /api/events", url.Values{"sensor_id": {"github-notifications"}}, map[string]any{"id": "42", "reason": "review_requested"},
			canned{status: 201, location: "/api/events/" + id2, body: map[string]any{"event": event(), "deduplicated": false, "dedup_key": "42", "flow_runs": []any{flowRunBrief()}}}},
		{"events replay", []string{"events", "replay", id2, "--flow", "triage-github"}, "", "POST /api/events/" + id2 + "/replay", nil,
			map[string]any{"flow": "triage-github"}, canned{status: 202, location: "/api/flow-runs/" + id1, body: flowRun(id1, "queued")}},
		{"events replay with explicit dry-run=false", []string{"events", "replay", id2, "--flow", "triage-github", "--dry-run=false"}, "", "POST /api/events/" + id2 + "/replay", nil,
			map[string]any{"flow": "triage-github", "dry_run": false}, canned{status: 202, location: "/api/flow-runs/" + id1, body: flowRun(id1, "queued")}},
		{"approvals approve with comment", []string{"approvals", "approve", id1, "--comment", "ship it"}, "", "POST /api/approvals/" + id1 + "/approve", nil,
			map[string]any{"comment": "ship it"}, ok(approval("approved"))},
		{"approvals deny with cancel", []string{"approvals", "deny", id1, "--comment", "no", "--cancel-run"}, "", "POST /api/approvals/" + id1 + "/deny", nil,
			map[string]any{"comment": "no", "cancel_run": true}, ok(approval("denied"))},
		{"inbox read", []string{"inbox", "read", id1}, "", "PATCH /api/inbox/" + id1, nil, map[string]any{"read": true}, ok(inboxItem(true))},
		{"inbox unread", []string{"inbox", "read", id1, "--unread"}, "", "PATCH /api/inbox/" + id1, nil, map[string]any{"read": false}, ok(inboxItem(false))},
		{"inbox star", []string{"inbox", "star", id1}, "", "PATCH /api/inbox/" + id1, nil, map[string]any{"starred": true}, ok(inboxItem(true))},
		{"inbox archive", []string{"inbox", "archive", id1}, "", "PATCH /api/inbox/" + id1, nil, map[string]any{"archived": true}, ok(inboxItem(true))},
		{"killswitch pause", []string{"killswitch", "pause"}, "", "POST /api/killswitch/pause", nil, map[string]any{"kill_running": false}, ok(killSwitch(true))},
		{"killswitch pause --kill", []string{"killswitch", "pause", "--kill", "--reason", "demo"}, "", "POST /api/killswitch/pause", nil,
			map[string]any{"kill_running": true, "reason": "demo"}, ok(killSwitch(true))},
		{"killswitch resume", []string{"killswitch", "resume"}, "", "POST /api/killswitch/resume", nil, map[string]any{}, ok(killSwitch(false))},
		{"runtimes probe", []string{"runtimes", "probe", "claude"}, "", "POST /api/runtimes/claude/probe", nil, map[string]any{}, ok(probeResult(true))},
		{"env set value", []string{"env", "set", "LINEAR_API_KEY", "lin_abc"}, "", "PUT /api/env/LINEAR_API_KEY", nil, map[string]any{"value": "lin_abc"}, canned{status: 204}},
		{"env set from stdin", []string{"env", "set", "LINEAR_API_KEY"}, "lin_from_stdin\n", "PUT /api/env/LINEAR_API_KEY", nil, map[string]any{"value": "lin_from_stdin"}, canned{status: 204}},
		{"env unset", []string{"env", "unset", "LINEAR_API_KEY"}, "", "DELETE /api/env/LINEAR_API_KEY", nil, map[string]any{}, canned{status: 204}},
		{"test-node with an event", []string{"flows", "test-node", "triage-github", "classify", "--event", id2, "--dry-run"}, "", "POST /api/flows/triage-github/test-node", nil,
			map[string]any{"node_id": "classify", "dry_run": true, "input": map[string]any{"source": "event", "event_id": id2}},
			ok(map[string]any{"flow_run": flowRun(id1, "succeeded"), "node_run": nodeRun("done")})},
		{"test-node with a node run output index", []string{"flows", "test-node", "triage-github", "classify", "--node-run", id3 + ":2", "--wait", "60s"}, "", "POST /api/flows/triage-github/test-node",
			url.Values{"wait": {"60s"}},
			map[string]any{"node_id": "classify", "dry_run": false, "input": map[string]any{"source": "node_run", "node_run_id": id3, "output_index": 2}},
			ok(map[string]any{"flow_run": flowRun(id1, "succeeded"), "node_run": nodeRun("done")})},
		{"test-node reads pasted JSON from stdin", []string{"flows", "test-node", "triage-github", "classify"}, `{"a":[1,2]}`, "POST /api/flows/triage-github/test-node", nil,
			map[string]any{"node_id": "classify", "dry_run": false, "input": map[string]any{"source": "json", "json": map[string]any{"a": []any{1, 2}}}},
			canned{status: 202, body: map[string]any{"flow_run": flowRun(id1, "running"), "node_run": nodeRun("running")}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDaemon(t)
			f.on(tc.route, tc.resp)
			res := f.run(runOpts{stdin: tc.stdin}, tc.args...)
			if res.code != 0 {
				t.Fatalf("exit %d, stderr: %s", res.code, res.stderr)
			}
			req := f.only()
			if req.Method+" "+req.Path != tc.route {
				t.Fatalf("request = %s %s, want %s", req.Method, req.Path, tc.route)
			}
			if ct := req.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q; the baseline rejects anything but application/json on mutating requests", ct)
			}
			if !jsonEqual(t, req.Body, tc.wantBody) {
				t.Errorf("body = %s, want %v", req.Body, tc.wantBody)
			}
			for k, v := range tc.wantQuery {
				if got := req.Query.Get(k); got != v[0] {
					t.Errorf("query %s = %q, want %q", k, got, v[0])
				}
			}
			if tc.resp.status == 204 && res.stdout != "" {
				t.Errorf("204 printed %q", res.stdout)
			}
		})
	}
}

func TestEventsInjectSendsTheItemVerbatimAndRejectsNonJSON(t *testing.T) {
	f := newFakeDaemon(t)
	res := f.run(runOpts{stdin: "not json"}, "events", "inject", "gh")
	if res.code != cli.ExitUsage || len(f.requests()) != 0 {
		t.Errorf("non-JSON stdin: exit %d, %d requests; want usage error before any request", res.code, len(f.requests()))
	}
}

func TestEnvSetRejectsValueAndStdinTogether(t *testing.T) {
	f := newFakeDaemon(t)
	res := f.run(runOpts{stdin: "x"}, "env", "set", "K", "v", "--stdin")
	if res.code != cli.ExitUsage || len(f.requests()) != 0 {
		t.Errorf("exit %d, %d requests", res.code, len(f.requests()))
	}
}

func TestFlagsMayFollowPositionalArguments(t *testing.T) {
	f := newFakeDaemon(t)
	f.on("POST /api/approvals/"+id1+"/approve", ok(approval("approved")))
	res := f.run(runOpts{}, "approvals", "approve", id1, "--comment", "later flags work")
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	if !jsonEqual(t, f.only().Body, map[string]any{"comment": "later flags work"}) {
		t.Errorf("body = %s", f.only().Body)
	}
}

func TestProblemJSONIsPrintedToStderrAndExitsNonZero(t *testing.T) {
	f := newFakeDaemon(t)
	f.on("GET /api/approvals/"+id1, problem(404, "not-found", "Not found", "approval "+id1+" does not exist", nil))
	res := f.run(runOpts{}, "approvals", "get", id1)
	if res.code != cli.ExitError {
		t.Errorf("exit = %d, want %d", res.code, cli.ExitError)
	}
	if res.stdout != "" {
		t.Errorf("stdout = %q on error", res.stdout)
	}
	if !jsonEqual(t, res.stderr, map[string]any{"type": "urn:hal9001:problem:not-found", "title": "Not found", "status": 404, "detail": "approval " + id1 + " does not exist"}) {
		t.Errorf("stderr should be the problem document:\n%s", res.stderr)
	}
}

func TestProblemSummaryAndDiagnosticsWithTable(t *testing.T) {
	f := newFakeDaemon(t)
	f.on("POST /api/approvals/"+id1+"/approve", problem(409, "invalid-state", "Invalid state", "approval already decided", nil))
	res := f.run(runOpts{}, "approvals", "approve", id1, "--table")
	if res.code != cli.ExitError || !strings.Contains(res.stderr, "409 Invalid state (invalid-state): approval already decided") {
		t.Errorf("exit %d, stderr %q", res.code, res.stderr)
	}

	f = newFakeDaemon(t)
	f.on("PATCH /api/inbox/"+id1, problem(422, "validation-failed", "Validation failed", "bad patch", map[string]any{
		"diagnostics": []any{diagnostic("error", "invalid_value", "read must be a boolean")}}))
	res = f.run(runOpts{}, "inbox", "read", id1, "--table")
	if res.code != cli.ExitError || !strings.Contains(res.stderr, "flows/x.yaml:3 nodes[0]: error: read must be a boolean") {
		t.Errorf("exit %d, stderr %q; want the diagnostic listed", res.code, res.stderr)
	}
}

func TestPausedKillSwitchProblemExitsNonZero(t *testing.T) {
	f := newFakeDaemon(t)
	f.on("POST /api/sensors/gh/poll", problem(423, "paused", "Paused", "the kill switch is engaged", map[string]any{"kill_switch": killSwitch(true)}))
	res := f.run(runOpts{}, "sensors", "poll", "gh")
	if res.code != cli.ExitError || !strings.Contains(res.stderr, "urn:hal9001:problem:paused") {
		t.Errorf("exit %d stderr %q", res.code, res.stderr)
	}
}

func TestFlowsValidateExitsNonZeroWhenTheFlowIsInvalid(t *testing.T) {
	invalid := ok(map[string]any{
		"valid": false, "diagnostics": []any{diagnostic("error", "cycle", "a -> b -> a")}, "hash": nil, "yaml": "id: x", "flow": nil,
	})
	f := newFakeDaemon(t)
	f.on("POST /api/flows/validate", invalid)
	res := f.run(runOpts{stdin: "id: x\n"}, "flows", "validate")
	if res.code != cli.ExitError {
		t.Errorf("exit = %d, want %d for an invalid flow", res.code, cli.ExitError)
	}
	if !strings.Contains(res.stdout, `"cycle"`) {
		t.Errorf("diagnostics not printed: %s", res.stdout)
	}
	if !jsonEqual(t, f.only().Body, map[string]any{"yaml": "id: x\n"}) {
		t.Errorf("request body = %s, want the YAML text", f.only().Body)
	}

	f = newFakeDaemon(t)
	f.on("POST /api/flows/validate", ok(map[string]any{"valid": true, "diagnostics": []any{}, "hash": hash, "yaml": "id: x", "flow": nil}))
	if res := f.run(runOpts{stdin: "id: x\n"}, "flows", "validate", "-"); res.code != 0 {
		t.Errorf("valid flow: exit %d, stderr %s", res.code, res.stderr)
	}
}

func TestRunsGetWaitLongPollsUntilTheRunFinishes(t *testing.T) {
	f := newFakeDaemon(t)
	f.on("GET /api/flow-runs/"+id1,
		ok(flowRunDetail("queued")), ok(flowRunDetail("running")), ok(flowRunDetail("succeeded")))
	res := f.run(runOpts{}, "runs", "get", id1, "--wait")
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	reqs := f.requests()
	if len(reqs) != 3 {
		t.Fatalf("polled %d times, want 3 (queued, running, succeeded)", len(reqs))
	}
	for i, r := range reqs {
		if r.Query.Get("wait") == "" {
			t.Errorf("poll %d did not ask the server to hold the request (no wait param)", i)
		}
	}
	if !strings.Contains(res.stdout, `"status": "succeeded"`) {
		t.Errorf("final output is not the finished run:\n%s", res.stdout)
	}
}

func TestRunsGetWaitStopsAtAwaitingApproval(t *testing.T) {
	f := newFakeDaemon(t)
	f.on("GET /api/flow-runs/"+id1, ok(flowRunDetail("awaiting_approval")))
	res := f.run(runOpts{}, "runs", "get", id1, "--wait")
	if res.code != 0 || len(f.requests()) != 1 {
		t.Errorf("exit %d after %d requests; awaiting_approval is a stopping state", res.code, len(f.requests()))
	}
}

func TestRunsGetWaitTimeoutPrintsTheCurrentState(t *testing.T) {
	f := newFakeDaemon(t)
	f.on("GET /api/flow-runs/"+id1, ok(flowRunDetail("running")))
	res := f.run(runOpts{}, "runs", "get", id1, "--wait", "--timeout", "1ns")
	if res.code != 0 || !strings.Contains(res.stdout, `"running"`) || len(f.requests()) != 1 {
		t.Errorf("exit %d, %d requests, stdout %q", res.code, len(f.requests()), res.stdout)
	}
}

func TestNodeRunsGetWaitPollsUntilDone(t *testing.T) {
	f := newFakeDaemon(t)
	done := nodeRun("done")
	done["agent_runs"] = []any{}
	running := nodeRun("running")
	running["agent_runs"] = []any{}
	f.on("GET /api/node-runs/"+id3, ok(running), ok(done))
	res := f.run(runOpts{}, "node-runs", "get", id3, "--wait")
	if res.code != 0 || len(f.requests()) != 2 || !strings.Contains(res.stdout, `"status": "done"`) {
		t.Errorf("exit %d after %d polls: %s", res.code, len(f.requests()), res.stdout)
	}
}

func TestSessionCookieFromFlagAndEnvIsSent(t *testing.T) {
	f := newFakeDaemon(t)
	f.on("GET /api/status", ok(statusBody()))

	if res := f.run(runOpts{}, "status", "--session-cookie", "abc123"); res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	if got := f.only().Header.Get("Cookie"); got != "hal9001_session=abc123" {
		t.Errorf("Cookie = %q, want the bare value wrapped in the session cookie name", got)
	}

	f = newFakeDaemon(t)
	f.on("GET /api/status", ok(statusBody()))
	if res := f.run(runOpts{env: map[string]string{cli.EnvSessionCookie: "hal9001_session=fromenv"}}, "status"); res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	if got := f.only().Header.Get("Cookie"); got != "hal9001_session=fromenv" {
		t.Errorf("Cookie = %q", got)
	}

	f = newFakeDaemon(t)
	f.on("GET /api/status", ok(statusBody()))
	_ = f.run(runOpts{}, "status")
	if got := f.only().Header.Get("Cookie"); got != "" {
		t.Errorf("Cookie = %q without a configured session (localhost-trust needs none)", got)
	}
}

func TestURLFlagOverridesEnvironment(t *testing.T) {
	f := newFakeDaemon(t)
	f.on("GET /api/status", ok(statusBody()))
	res := run(t, runOpts{env: map[string]string{cli.EnvURL: "http://127.0.0.1:1"}}, "status", "--url", f.srv.URL)
	if res.code != 0 || len(f.requests()) != 1 {
		t.Errorf("exit %d, %d requests, stderr %s", res.code, len(f.requests()), res.stderr)
	}
}

func TestTableOutputForListsAndObjects(t *testing.T) {
	f := newFakeDaemon(t)
	list := page(flowRun(id1, "succeeded"), flowRun(id3, "running"))
	list["next_cursor"] = id3
	f.on("GET /api/flow-runs", ok(list))
	res := f.run(runOpts{}, "runs", "list", "--table")
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	lines := strings.Split(strings.TrimSpace(res.stdout), "\n")
	if len(lines) < 4 || !strings.HasPrefix(lines[0], "ID") || !strings.Contains(lines[0], "STATUS") {
		t.Fatalf("table header missing:\n%s", res.stdout)
	}
	if !strings.Contains(lines[1], id1) || !strings.Contains(lines[1], "succeeded") || !strings.Contains(lines[2], "running") {
		t.Errorf("rows wrong:\n%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "next cursor: "+id3) {
		t.Errorf("pagination hint missing:\n%s", res.stdout)
	}

	f = newFakeDaemon(t)
	f.on("GET /api/status", ok(statusBody()))
	res = f.run(runOpts{}, "status", "--table")
	if !regexp.MustCompile(`(?m)^pending_approvals\s+3$`).MatchString(res.stdout) || !regexp.MustCompile(`(?m)^unread_inbox\s+4$`).MatchString(res.stdout) {
		t.Errorf("object table should list one aligned key/value per line:\n%s", res.stdout)
	}

	f = newFakeDaemon(t)
	f.on("GET /api/flow-runs", ok(page()))
	if res := f.run(runOpts{}, "runs", "list", "--table"); strings.TrimSpace(res.stdout) != "(none)" {
		t.Errorf("empty list = %q", res.stdout)
	}
}

func TestUnreachableDaemonExplainsAndExitsNonZero(t *testing.T) {
	f := newFakeDaemon(t)
	url := f.srv.URL
	f.srv.Close()
	res := run(t, runOpts{env: map[string]string{cli.EnvURL: url}}, "status")
	if res.code != cli.ExitError || !strings.Contains(res.stderr, "cannot reach the daemon") {
		t.Errorf("exit %d stderr %q", res.code, res.stderr)
	}
}

func TestUsageErrorsExitTwoWithoutContactingTheDaemon(t *testing.T) {
	f := newFakeDaemon(t)
	for _, args := range [][]string{
		{"bogus"},
		{"runs"},
		{"runs", "get"},
		{"runs", "get", id1, id2},
		{"runs", "get", id1, "--no-such-flag"},
		{"events", "replay", id1},
		{"flows", "test-node", "f", "n", "--event", id1, "--json", "{}"},
		{"flows", "test-node", "f", "n", "--node-run", id1 + ":x"},
	} {
		res := f.run(runOpts{}, args...)
		if res.code != cli.ExitUsage {
			t.Errorf("%v: exit %d, want %d (stderr %q)", args, res.code, cli.ExitUsage, res.stderr)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("%d requests were sent for malformed invocations", n)
	}
}

func TestHelpListsCommandsAndExitsZero(t *testing.T) {
	res := run(t, runOpts{}, "help")
	if res.code != 0 || !strings.Contains(res.stdout, "killswitch") || !strings.Contains(res.stdout, "--table") {
		t.Errorf("help: exit %d\n%s", res.code, res.stdout)
	}
	if res := run(t, runOpts{}, "runs", "get", "--help"); res.code != 0 || !strings.Contains(res.stdout, "--wait") {
		t.Errorf("runs get --help: exit %d\n%s", res.code, res.stdout)
	}
}

func TestTailscaleStatusIsAnAPIClientCommand(t *testing.T) {
	f := newFakeDaemon(t)
	body := map[string]any{
		"available": true, "backend_state": "Running", "hostname": "mac.tail1234.ts.net", "owner_login": "a@b.c",
		"serve_configured": true, "funnel_enabled": false, "host_allowed": true, "allowed_logins": []string{"a@b.c"},
	}
	f.on("GET /api/auth/tailscale", ok(body))
	res := f.run(runOpts{}, "tailscale", "status")
	if res.code != 0 || !jsonEqual(t, res.stdout, body) {
		t.Errorf("exit %d: %s", res.code, res.stdout)
	}
}

func TestVersionPrintsTheBuildVersion(t *testing.T) {
	if res := run(t, runOpts{}, "version"); res.code != 0 || strings.TrimSpace(res.stdout) != "9.9.9-test" {
		t.Errorf("version = %q (exit %d)", res.stdout, res.code)
	}
}

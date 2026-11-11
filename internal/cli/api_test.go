package cli_test

import (
	"encoding/json"
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
		{"runs list continuations", []string{"runs", "list", "--kind", "continuation", "--continuation-of", id1, "--conversation", id2},
			"GET /api/flow-runs", url.Values{"kind": {"continuation"}, "continuation_of": {id1}, "conversation": {id2}}, page(flowRun(id3, "running"))},
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
		{"agent-runs sandbox-log", []string{"agent-runs", "sandbox-log", id2, "--after-id", "3", "--limit", "10", "--tail"},
			"GET /api/agent-runs/" + id2 + "/sandbox-log", url.Values{"after_id": {"3"}, "limit": {"10"}, "tail": {"true"}},
			map[string]any{"sessions": []any{}, "entries": []any{}, "has_more": false}},
		{"node-runs sandbox-log", []string{"node-runs", "sandbox-log", id3},
			"GET /api/node-runs/" + id3 + "/sandbox-log", nil,
			map[string]any{"sessions": []any{}, "entries": []any{}, "has_more": false}},
		{"approvals list", []string{"approvals", "list", "--status", "pending", "--type", "tool_call"},
			"GET /api/approvals", url.Values{"status": {"pending"}, "type": {"tool_call"}}, page(approval("pending"))},
		{"approvals get", []string{"approvals", "get", id1}, "GET /api/approvals/" + id1, nil, approval("pending")},
		{"inbox list unread", []string{"inbox", "list", "--unread", "--starred", "--priority", "high"},
			"GET /api/inbox", url.Values{"read": {"false"}, "starred": {"true"}, "priority": {"high"}}, page(inboxItem(false))},
		{"inbox get", []string{"inbox", "get", id1}, "GET /api/inbox/" + id1, nil, inboxItem(true)},
		{"killswitch get", []string{"killswitch", "get"}, "GET /api/killswitch", nil, killSwitch(false)},
		{"runtimes list", []string{"runtimes", "list"}, "GET /api/runtimes", nil, map[string]any{"items": []any{
			runtimeItem("claude", "ready", nil)}}},
		{"env list", []string{"env", "list"}, "GET /api/env", nil, map[string]any{"items": []any{
			map[string]any{"key": "GITHUB_TOKEN", "secret": true, "value": nil},
			map[string]any{"key": "LOG_LEVEL", "secret": false, "value": "debug"}}}},
		{"sensors env list", []string{"sensors", "env", "list", "linear"}, "GET /api/sensors/linear/env", nil, map[string]any{"items": []any{
			map[string]any{"key": "LINEAR_API_KEY", "overrides_global": true, "secret": true, "value": nil},
			map[string]any{"key": "REGION", "overrides_global": false, "secret": false, "value": "eu"}}}},
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
			canned{status: 202, body: pollResult(), location: "/api/sensor-runs/" + id1}},
		{"sensors poll sends rerun_seen and the flows filter", []string{"sensors", "poll", "gh", "--rerun-seen", "--flow", "a", "--flow", "b"}, "", "POST /api/sensors/gh/poll", nil,
			map[string]any{"rerun_seen": true, "flows": []any{"a", "b"}}, canned{status: 202, body: pollResult(), location: "/api/sensor-runs/" + id1}},
		{"flows run sends rerun_seen", []string{"flows", "run", "triage-github", "--rerun-seen"}, "", "POST /api/flows/triage-github/run", nil,
			map[string]any{"rerun_seen": true}, canned{status: 202, body: runFlowResult()}},
		{"sensors reset without run", []string{"sensors", "reset", "gh", "--yes"}, "", "POST /api/sensors/gh/reset", nil,
			map[string]any{"run": false}, canned{status: 200, body: resetResult(false)}},
		{"sensors reset with run and flows", []string{"sensors", "reset", "gh", "--run", "--flow", "triage-github"}, "", "POST /api/sensors/gh/reset", nil,
			map[string]any{"run": true, "rerun_flows": []any{"triage-github"}}, canned{status: 200, body: resetResult(true)}},
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
		{"queue remove", []string{"queue", "remove", id1}, "", "DELETE /api/queue/" + id1, nil, map[string]any{}, ok(flowRun(id1, "cancelled"))},
		{"queue clear", []string{"queue", "clear", "--yes"}, "", "POST /api/queue/clear", nil, map[string]any{}, ok(map[string]any{"cancelled": 0, "flow_run_ids": []any{}})},
		{"queue clear --flow", []string{"queue", "clear", "--flow", "triage-github", "--yes"}, "", "POST /api/queue/clear", nil,
			map[string]any{"flow_id": "triage-github"}, ok(map[string]any{"cancelled": 0, "flow_run_ids": []any{}})},
		{"runtimes probe", []string{"runtimes", "probe", "claude"}, "", "POST /api/runtimes/claude/probe", nil, map[string]any{}, ok(probeResult(true))},
		{"env set value", []string{"env", "set", "LINEAR_API_KEY", "lin_abc"}, "", "PUT /api/env/LINEAR_API_KEY", nil, map[string]any{"value": "lin_abc"}, canned{status: 204}},
		{"env set from stdin", []string{"env", "set", "LINEAR_API_KEY"}, "lin_from_stdin\n", "PUT /api/env/LINEAR_API_KEY", nil, map[string]any{"value": "lin_from_stdin"}, canned{status: 204}},
		{"env unset", []string{"env", "unset", "LINEAR_API_KEY"}, "", "DELETE /api/env/LINEAR_API_KEY", nil, map[string]any{}, canned{status: 204}},
		{"sensors pause", []string{"sensors", "pause", "linear"}, "", "POST /api/sensors/linear/pause", nil, map[string]any{}, ok(sensorSummary())},
		{"sensors resume", []string{"sensors", "resume", "linear"}, "", "POST /api/sensors/linear/resume", nil, map[string]any{}, ok(sensorSummary())},
		{"sensors env set value", []string{"sensors", "env", "set", "linear", "LINEAR_API_KEY", "lin_abc"}, "", "PUT /api/sensors/linear/env/LINEAR_API_KEY", nil,
			map[string]any{"value": "lin_abc"}, canned{status: 204}},
		{"sensors env set from stdin", []string{"sensors", "env", "set", "linear", "LINEAR_API_KEY", "--stdin"}, "lin_from_stdin\n", "PUT /api/sensors/linear/env/LINEAR_API_KEY", nil,
			map[string]any{"value": "lin_from_stdin"}, canned{status: 204}},
		{"sensors env unset", []string{"sensors", "env", "unset", "linear", "LINEAR_API_KEY"}, "", "DELETE /api/sensors/linear/env/LINEAR_API_KEY", nil, map[string]any{}, canned{status: 204}},
		{"env set --plain sends the value with secret false", []string{"env", "set", "LOG_LEVEL", "debug", "--plain"}, "", "PUT /api/env/LOG_LEVEL", nil,
			map[string]any{"value": "debug", "secret": false}, canned{status: 204}},
		{"env set --plain --stdin", []string{"env", "set", "LOG_LEVEL", "--plain", "--stdin"}, "debug\n", "PUT /api/env/LOG_LEVEL", nil,
			map[string]any{"value": "debug", "secret": false}, canned{status: 204}},
		{"env set --secret sends the value with secret true", []string{"env", "set", "TOKEN", "t", "--secret"}, "", "PUT /api/env/TOKEN", nil,
			map[string]any{"value": "t", "secret": true}, canned{status: 204}},
		{"env set --plain alone flips the flag without a value", []string{"env", "set", "LOG_LEVEL", "--plain"}, "", "PUT /api/env/LOG_LEVEL", nil,
			map[string]any{"secret": false}, canned{status: 204}},
		{"env set --secret alone flips the flag without a value", []string{"env", "set", "LOG_LEVEL", "--secret"}, "", "PUT /api/env/LOG_LEVEL", nil,
			map[string]any{"secret": true}, canned{status: 204}},
		{"sensors env set --plain sends the value with secret false", []string{"sensors", "env", "set", "linear", "REGION", "eu", "--plain"}, "", "PUT /api/sensors/linear/env/REGION", nil,
			map[string]any{"value": "eu", "secret": false}, canned{status: 204}},
		{"sensors env set --plain alone flips the flag without a value", []string{"sensors", "env", "set", "linear", "REGION", "--plain"}, "", "PUT /api/sensors/linear/env/REGION", nil,
			map[string]any{"secret": false}, canned{status: 204}},
		{"sensors env set --secret alone flips the flag without a value", []string{"sensors", "env", "set", "linear", "REGION", "--secret"}, "", "PUT /api/sensors/linear/env/REGION", nil,
			map[string]any{"secret": true}, canned{status: 204}},
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
		{"test-node --simulate-emit splits the port from the output at the first =", []string{"flows", "test-node", "triage-github", "triage", "--json", "1", "--simulate-emit", `reviewed={"title":"a=b"}`}, "", "POST /api/flows/triage-github/test-node", nil,
			map[string]any{"node_id": "triage", "dry_run": false, "input": map[string]any{"source": "json", "json": float64(1)},
				"simulate_emit": map[string]any{"port": "reviewed", "output": map[string]any{"title": "a=b"}}},
			ok(map[string]any{"flow_run": flowRun(id1, "succeeded"), "node_run": nodeRun("done")})},
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

func TestEnvSetRejectsPlainAndSecretTogether(t *testing.T) {
	f := newFakeDaemon(t)
	for _, args := range [][]string{
		{"env", "set", "K", "v", "--plain", "--secret"},
		{"sensors", "env", "set", "s", "K", "--plain", "--secret"},
	} {
		if res := f.run(runOpts{}, args...); res.code != cli.ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, res.code, cli.ExitUsage)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
}

func TestEnvSetWithoutFlagsStillOmitsTheSecretField(t *testing.T) {
	// Omitting secret lets the API apply its default (secret) when a value is set.
	f := newFakeDaemon(t)
	f.on("PUT /api/env/K", canned{status: 204})
	if res := f.run(runOpts{}, "env", "set", "K", "v"); res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	if strings.Contains(f.only().Body, "secret") {
		t.Errorf("body = %s, want no secret field", f.only().Body)
	}
}

func TestEnvListTableShowsTheSecretFlagAndPlainValuesOnly(t *testing.T) {
	f := newFakeDaemon(t)
	f.on("GET /api/env", ok(map[string]any{"items": []any{
		map[string]any{"key": "GITHUB_TOKEN", "secret": true, "value": nil},
		map[string]any{"key": "LOG_LEVEL", "secret": false, "value": "debug"}}}))
	res := f.run(runOpts{}, "env", "list", "--table")
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	lines := strings.Split(strings.TrimSpace(res.stdout), "\n")
	if len(lines) != 3 || !regexp.MustCompile(`^KEY\s+SECRET\s+VALUE$`).MatchString(lines[0]) {
		t.Fatalf("table = \n%s", res.stdout)
	}
	if !regexp.MustCompile(`^GITHUB_TOKEN\s+yes\s+-$`).MatchString(lines[1]) {
		t.Errorf("secret row = %q, want yes and no value", lines[1])
	}
	if !regexp.MustCompile(`^LOG_LEVEL\s+no\s+debug$`).MatchString(lines[2]) {
		t.Errorf("plain row = %q, want no and the value", lines[2])
	}

	f = newFakeDaemon(t)
	f.on("GET /api/sensors/linear/env", ok(map[string]any{"items": []any{
		map[string]any{"key": "LINEAR_API_KEY", "overrides_global": true, "secret": true, "value": nil},
		map[string]any{"key": "REGION", "overrides_global": false, "secret": false, "value": "eu"}}}))
	res = f.run(runOpts{}, "sensors", "env", "list", "linear", "--table")
	lines = strings.Split(strings.TrimSpace(res.stdout), "\n")
	if res.code != 0 || len(lines) != 3 || !regexp.MustCompile(`^KEY\s+OVERRIDES_GLOBAL\s+SECRET\s+VALUE$`).MatchString(lines[0]) {
		t.Fatalf("exit %d, table = \n%s", res.code, res.stdout)
	}
	if !regexp.MustCompile(`^LINEAR_API_KEY\s+yes\s+yes\s+-$`).MatchString(lines[1]) || !regexp.MustCompile(`^REGION\s+no\s+no\s+eu$`).MatchString(lines[2]) {
		t.Errorf("rows wrong:\n%s", res.stdout)
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

func TestPollAndFlowRunWaitPrintTheFinalStatusOfTheCreatedRuns(t *testing.T) {
	for _, args := range [][]string{
		{"sensors", "poll", "gh", "--wait"},
		{"flows", "run", "triage-github", "--wait"},
	} {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			f := newFakeDaemon(t)
			f.on("POST /api/sensors/gh/poll", canned{status: 202, body: pollResult()})
			f.on("POST /api/flows/triage-github/run", canned{status: 202, body: runFlowResult()})
			f.on("GET /api/flow-runs/"+id1, ok(flowRunDetail("running")), ok(flowRunDetail("succeeded")))
			res := f.run(runOpts{}, args...)
			if res.code != 0 {
				t.Fatalf("exit %d: %s", res.code, res.stderr)
			}
			var out struct {
				FlowRunIDs []string                      `json:"flow_run_ids"`
				FlowRuns   []struct{ ID, Status string } `json:"flow_runs"`
			}
			if err := json.Unmarshal([]byte(res.stdout), &out); err != nil {
				t.Fatalf("stdout is not JSON: %v\n%s", err, res.stdout)
			}
			if len(out.FlowRunIDs) != 1 || len(out.FlowRuns) != 1 || out.FlowRuns[0].ID != id1 || out.FlowRuns[0].Status != "succeeded" {
				t.Errorf("output = %s, want the run ids and the final status of the run", res.stdout)
			}
		})
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

// The table of a continuation run shows its kind and where it continues from; JSON shows the same fields as the API.
func TestRunsShowTheContinuationLink(t *testing.T) {
	cont := flowRun(id3, "running")
	cont["kind"] = "continuation"
	cont["continuation"] = map[string]any{"of_run_id": id1, "conversation_id": id2, "node_id": "work", "port": "ship", "approval_id": nil}

	f := newFakeDaemon(t)
	f.on("GET /api/flow-runs/"+id3, ok(map[string]any{"flow_run": cont, "node_runs": []any{nodeRun("done")}}))
	res := f.run(runOpts{}, "runs", "get", id3, "--table")
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`(?m)^kind\s+continuation$`),
		regexp.MustCompile(`(?m)^continuation.of_run_id\s+` + id1 + `$`),
		regexp.MustCompile(`(?m)^continuation.conversation_id\s+` + id2 + `$`),
		regexp.MustCompile(`(?m)^continuation.port\s+ship$`),
		regexp.MustCompile(`(?m)^continuation.approval_id\s+-$`),
		regexp.MustCompile(`(?m)^` + id3 + `\s+classify\s+command\s+done`),
	} {
		if !want.MatchString(res.stdout) {
			t.Errorf("table lacks %v:\n%s", want, res.stdout)
		}
	}
	if res := f.run(runOpts{}, "runs", "get", id3); !strings.Contains(res.stdout, `"of_run_id": "`+id1+`"`) {
		t.Errorf("JSON output lacks the continuation link:\n%s", res.stdout)
	}

	f = newFakeDaemon(t)
	f.on("GET /api/flow-runs", ok(page(cont, flowRun(id1, "succeeded"))))
	res = f.run(runOpts{}, "runs", "list", "--table")
	lines := strings.Split(strings.TrimSpace(res.stdout), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "OF_RUN_ID") || !strings.Contains(lines[1], id1) || !regexp.MustCompile(`continuation\s+running.*`+id1).MatchString(lines[1]) || !strings.Contains(lines[2], "-") {
		t.Errorf("list table:\n%s", res.stdout)
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
		{"flows", "test-node", "f", "n", "--json", "1", "--simulate-emit", "Bad-Port"},
		{"flows", "test-node", "f", "n", "--json", "1", "--simulate-emit", "ok=[1]"},
		{"assist", "test", id1, "--simulate-emit", "noport"},
		{"assist", "test", id1, "--simulate-emit", "n:ok", "--simulate-emit", "n:other"},
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

func tailscaleBody() map[string]any {
	b := map[string]any{
		"cli": map[string]any{"found": true, "path": "/opt/homebrew/bin/tailscale", "version": "1.80.2"}, "backend_state": "Running",
		"logged_in": true, "dns_name": "hal-mac.example-tailnet.ts.net", "tailnet": "example-tailnet.ts.net", "owner_login": "a@example.com",
		"https_enabled": true, "magic_dns_enabled": true,
		"serve": map[string]any{"enabled": true, "https_port": 443, "target": "http://127.0.0.1:9001", "active": true, "points_at_hal": true, "conflict": nil, "funnel": false, "funnel_on_port": false},
		"url":   "https://hal-mac.example-tailnet.ts.net/", "allowed_host": map[string]any{"entry": "hal-mac.example-tailnet.ts.net", "present": true, "added_by_hal": true},
		"allowed_logins": []string{"a@example.com"}, "warnings": []any{}, "error": nil, "checked_at": "2026-11-11T10:00:00Z",
	}
	return b
}

func TestTailscaleStatusIsAnAPIClientCommand(t *testing.T) {
	f := newFakeDaemon(t)
	body := tailscaleBody()
	f.on("GET /api/tailscale", ok(body))
	res := f.run(runOpts{}, "tailscale", "status")
	if res.code != 0 || !jsonEqual(t, res.stdout, body) {
		t.Errorf("exit %d: %s", res.code, res.stdout)
	}
	if res := f.run(runOpts{}, "tailscale", "status", "--table"); res.code != 0 ||
		!strings.Contains(res.stdout, "https://hal-mac.example-tailnet.ts.net/") || !strings.Contains(res.stdout, "added by hal9001") {
		t.Errorf("--table: exit %d\n%s", res.code, res.stdout)
	}
}

func TestTailscaleServeSendsThePortAndReplaceFlags(t *testing.T) {
	f := newFakeDaemon(t)
	f.on("POST /api/tailscale/serve", ok(tailscaleBody()))
	if res := f.run(runOpts{}, "tailscale", "serve", "--port", "8443", "--replace"); res.code != 0 {
		t.Fatalf("exit %d: %s%s", res.code, res.stdout, res.stderr)
	}
	r := f.only()
	if r.Method != "POST" || !jsonEqual(t, r.Body, map[string]any{"https_port": float64(8443), "replace": true}) {
		t.Errorf("request = %+v", r)
	}

	g := newFakeDaemon(t)
	g.on("POST /api/tailscale/serve", ok(tailscaleBody()))
	if res := g.run(runOpts{}, "tailscale", "serve"); res.code != 0 || strings.TrimSpace(g.only().Body) != "{}" {
		t.Errorf("no flags: exit %d, body %q", res.code, g.only().Body)
	}
}

func TestTailscaleServeConflictSuggestsReplace(t *testing.T) {
	f := newFakeDaemon(t)
	f.on("POST /api/tailscale/serve", problem(409, "already-exists", "Already exists", "HTTPS port 443 on the tailnet already serves http://127.0.0.1:3000", nil))
	res := f.run(runOpts{}, "tailscale", "serve")
	if res.code == 0 || !strings.Contains(res.stderr, "already serves http://127.0.0.1:3000") || !strings.Contains(res.stderr, "--replace") {
		t.Errorf("exit %d: %s%s", res.code, res.stdout, res.stderr)
	}
}

func TestTailscaleUnserveDeletesTheMapping(t *testing.T) {
	f := newFakeDaemon(t)
	f.on("DELETE /api/tailscale/serve", ok(tailscaleBody()))
	if res := f.run(runOpts{}, "tailscale", "unserve"); res.code != 0 {
		t.Fatalf("exit %d: %s%s", res.code, res.stdout, res.stderr)
	}
	if r := f.only(); r.Method != "DELETE" || r.Path != "/api/tailscale/serve" {
		t.Errorf("request = %+v", r)
	}
}

func TestVersionPrintsTheBuildVersion(t *testing.T) {
	if res := run(t, runOpts{}, "version"); res.code != 0 || strings.TrimSpace(res.stdout) != "9.9.9-test" {
		t.Errorf("version = %q (exit %d)", res.stdout, res.code)
	}
}

func updateStatusBody(state string, available bool) map[string]any {
	return map[string]any{
		"enabled": true, "disabled_reason": nil,
		"current": map[string]any{"bundle_id": "dev.hal9001", "version": "0.1.1", "build": 1},
		"channel": "stable", "auto_install_when_idle": false, "state": state,
		"latest": map[string]any{"schema": 1, "channel": "stable", "version": "0.1.2", "build": 2, "commit": "abc",
			"published_at": "2026-11-10T12:00:00Z", "min_macos": "13.0", "url": "https://example.com/a.zip", "size": 10,
			"sha256": strings.Repeat("a", 64), "notes_url": "https://example.com/n", "notes": ""},
		"available": available, "pending": state == "pending", "phase": nil,
		"busy": map[string]any{"running_runs": 1}, "installer_connected": true,
		"last_checked_at": nil, "error": nil,
	}
}

func TestUpdateCommands(t *testing.T) {
	t.Run("bare update prints the status table", func(t *testing.T) {
		f := newFakeDaemon(t)
		f.on("GET /api/update", ok(updateStatusBody("available", true)))
		res := f.run(runOpts{}, "update")
		if res.code != 0 || !strings.Contains(res.stdout, "0.1.2 (build 2)") || !strings.Contains(res.stdout, "available") {
			t.Fatalf("exit %d stdout %q stderr %q", res.code, res.stdout, res.stderr)
		}
	})
	t.Run("status --json prints the body", func(t *testing.T) {
		f := newFakeDaemon(t)
		f.on("GET /api/update", ok(updateStatusBody("up_to_date", false)))
		res := f.run(runOpts{}, "update", "status", "--json")
		if res.code != 0 || !strings.Contains(res.stdout, `"state": "up_to_date"`) {
			t.Fatalf("exit %d stdout %q", res.code, res.stdout)
		}
	})
	t.Run("install never forces and reports pending", func(t *testing.T) {
		f := newFakeDaemon(t)
		f.on("POST /api/update/install", canned{status: 202, body: updateStatusBody("pending", true)})
		res := f.run(runOpts{}, "update", "install")
		req := f.only()
		if res.code != 0 || !jsonEqual(t, req.Body, map[string]any{"force": false}) || !strings.Contains(res.stdout, "pending until flows finish") {
			t.Fatalf("exit %d body %s stdout %q", res.code, req.Body, res.stdout)
		}
	})
	t.Run("install without an installer shows the release URL", func(t *testing.T) {
		f := newFakeDaemon(t)
		f.on("POST /api/update/install", problem(409, "invalid-state", "Conflict", "Open the HAL9001 app to install",
			map[string]any{"current_state": "no_installer", "release_url": "https://example.com/rel"}))
		res := f.run(runOpts{}, "update", "install")
		if res.code == 0 || !strings.Contains(res.stderr, "https://example.com/rel") {
			t.Fatalf("exit %d stderr %q", res.code, res.stderr)
		}
	})
	t.Run("check posts to the check endpoint", func(t *testing.T) {
		f := newFakeDaemon(t)
		f.on("POST /api/update/check", ok(updateStatusBody("up_to_date", false)))
		if res := f.run(runOpts{}, "update", "check"); res.code != 0 {
			t.Fatalf("exit %d stderr %q", res.code, res.stderr)
		}
	})
}

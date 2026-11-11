package cli_test

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hal9001/internal/cli"
)

func TestAssistCommandsHitTheRightEndpointsAndPrintTheResponse(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		stdin     string
		route     string
		wantQuery url.Values
		wantBody  any // nil: no body expected
		resp      canned
		wantOut   any // nil: nothing printed
	}{
		{"list", []string{"assist", "list", "--target-kind", "sensor", "--target-id", "gh"}, "", "GET /api/assist/sessions",
			url.Values{"target_kind": {"sensor"}, "target_id": {"gh"}}, nil,
			ok(map[string]any{"items": []any{assistSession("idle")}}), map[string]any{"items": []any{assistSession("idle")}}},
		{"create for an existing sensor", []string{"assist", "create", "--runtime", "claude", "--sensor", "gh", "--model", "opus"}, "",
			"POST /api/assist/sessions", nil,
			map[string]any{"target": map[string]any{"kind": "sensor", "id": "gh"}, "runtime": "claude", "model": "opus"},
			canned{status: 201, location: "/api/assist/sessions/" + id1, body: assistSession("idle")}, assistSession("idle")},
		{"create a general chat", []string{"assist", "create", "--runtime", "claude", "--general"}, "",
			"POST /api/assist/sessions", nil,
			map[string]any{"target": map[string]any{"kind": "general"}, "runtime": "claude"},
			canned{status: 201, location: "/api/assist/sessions/" + id1, body: assistSession("idle")}, assistSession("idle")},
		{"create a new sensor from stdin yaml", []string{"assist", "create", "--runtime", "claude", "--yaml", "-"}, sensorYAML,
			"POST /api/assist/sessions", nil,
			map[string]any{"target": map[string]any{"kind": "sensor"}, "runtime": "claude", "yaml": sensorYAML},
			canned{status: 201, location: "/api/assist/sessions/" + id1, body: assistSession("idle")}, assistSession("idle")},
		{"get", []string{"assist", "get", id1}, "", "GET /api/assist/sessions/" + id1, nil, nil,
			ok(assistSessionDetail()), assistSessionDetail()},
		{"cancel", []string{"assist", "cancel", id1}, "", "POST /api/assist/sessions/" + id1 + "/cancel", nil, map[string]any{},
			ok(assistSession("idle")), assistSession("idle")},
		{"close prints nothing", []string{"assist", "close", id1}, "", "DELETE /api/assist/sessions/" + id1, nil, map[string]any{},
			canned{status: 204}, nil},
		{"test posts an empty object", []string{"assist", "test", id1}, "", "POST /api/assist/sessions/" + id1 + "/test", nil, map[string]any{},
			ok(assistTestResult(0)), assistTestResult(0)},
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
			if tc.wantBody != nil && !jsonEqual(t, req.Body, tc.wantBody) {
				t.Errorf("body = %s, want %v", req.Body, tc.wantBody)
			}
			for k, v := range tc.wantQuery {
				if got := req.Query.Get(k); got != v[0] {
					t.Errorf("query %s = %q, want %q", k, got, v[0])
				}
			}
			if tc.wantOut == nil {
				if res.stdout != "" {
					t.Errorf("printed %q, want nothing", res.stdout)
				}
			} else if !jsonEqual(t, res.stdout, tc.wantOut) {
				t.Errorf("stdout = %s, want %v", res.stdout, tc.wantOut)
			}
		})
	}
}

func TestAssistCreateRequiresARuntime(t *testing.T) {
	f := newFakeDaemon(t)
	res := f.run(runOpts{}, "assist", "create", "--sensor", "gh")
	if res.code != cli.ExitUsage || len(f.requests()) != 0 {
		t.Errorf("exit %d with %d requests; want usage error before any request", res.code, len(f.requests()))
	}
}

func TestAssistSendJoinsWordsAndPassesYAML(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "sensor.yaml")
	if err := os.WriteFile(file, []byte(sensorYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	route := "POST /api/assist/sessions/" + id1 + "/messages"
	accepted := canned{status: 202, body: assistSession("running")}

	t.Run("words joined with spaces and yaml from a file", func(t *testing.T) {
		f := newFakeDaemon(t)
		f.on(route, accepted)
		res := f.run(runOpts{}, "assist", "send", id1, "poll", "every", "hour", "--yaml", file)
		if res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		want := map[string]any{"text": "poll every hour", "yaml": sensorYAML}
		if req := f.only(); !jsonEqual(t, req.Body, want) {
			t.Errorf("body = %s, want %v", req.Body, want)
		}
		if !jsonEqual(t, res.stdout, assistSession("running")) {
			t.Errorf("stdout = %s", res.stdout)
		}
	})
	t.Run("text from stdin without words", func(t *testing.T) {
		f := newFakeDaemon(t)
		f.on(route, accepted)
		if res := f.run(runOpts{stdin: "fix the key\n"}, "assist", "send", id1); res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		if req := f.only(); !jsonEqual(t, req.Body, map[string]any{"text": "fix the key"}) {
			t.Errorf("body = %s", req.Body)
		}
	})
	t.Run("text from stdin with a dash", func(t *testing.T) {
		f := newFakeDaemon(t)
		f.on(route, accepted)
		if res := f.run(runOpts{stdin: "from dash"}, "assist", "send", id1, "-"); res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		if req := f.only(); !jsonEqual(t, req.Body, map[string]any{"text": "from dash"}) {
			t.Errorf("body = %s", req.Body)
		}
	})
	t.Run("text and yaml cannot both be stdin", func(t *testing.T) {
		f := newFakeDaemon(t)
		res := f.run(runOpts{stdin: "x"}, "assist", "send", id1, "--yaml", "-")
		if res.code != cli.ExitUsage || len(f.requests()) != 0 {
			t.Errorf("exit %d with %d requests", res.code, len(f.requests()))
		}
	})
	t.Run("empty text is rejected before any request", func(t *testing.T) {
		f := newFakeDaemon(t)
		res := f.run(runOpts{stdin: "\n"}, "assist", "send", id1)
		if res.code != cli.ExitUsage || len(f.requests()) != 0 {
			t.Errorf("exit %d with %d requests", res.code, len(f.requests()))
		}
	})
}

func TestAssistTestDraftAndCancelDefaultToTheSessionInTheEnvironment(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		route string
		resp  canned
	}{
		{"test", []string{"assist", "test"}, "POST /api/assist/sessions/" + id2 + "/test", ok(assistTestResult(0))},
		{"draft", []string{"assist", "draft"}, "GET /api/assist/sessions/" + id2, ok(assistSessionDetail())},
		{"cancel", []string{"assist", "cancel"}, "POST /api/assist/sessions/" + id2 + "/cancel", ok(assistSession("idle"))},
	}
	for _, tc := range cases {
		t.Run(tc.name+" uses the environment session", func(t *testing.T) {
			f := newFakeDaemon(t)
			f.on(tc.route, tc.resp)
			res := f.run(runOpts{env: map[string]string{cli.EnvAssistSession: id2}}, tc.args...)
			if res.code != 0 {
				t.Fatalf("exit %d: %s", res.code, res.stderr)
			}
			if req := f.only(); req.Method+" "+req.Path != tc.route {
				t.Errorf("request = %s %s, want %s", req.Method, req.Path, tc.route)
			}
		})
		t.Run(tc.name+" argument wins over the environment", func(t *testing.T) {
			f := newFakeDaemon(t)
			route := strings.ReplaceAll(tc.route, id2, id1)
			f.on(route, tc.resp)
			res := f.run(runOpts{env: map[string]string{cli.EnvAssistSession: id2}}, append(tc.args, id1)...)
			if res.code != 0 {
				t.Fatalf("exit %d: %s", res.code, res.stderr)
			}
			if req := f.only(); req.Method+" "+req.Path != route {
				t.Errorf("request = %s %s, want %s", req.Method, req.Path, route)
			}
		})
		t.Run(tc.name+" without a session fails clearly", func(t *testing.T) {
			f := newFakeDaemon(t)
			res := f.run(runOpts{}, tc.args...)
			if res.code != cli.ExitUsage || len(f.requests()) != 0 {
				t.Errorf("exit %d with %d requests, want usage error", res.code, len(f.requests()))
			}
			if !strings.Contains(res.stderr, cli.EnvAssistSession) {
				t.Errorf("stderr %q does not name %s", res.stderr, cli.EnvAssistSession)
			}
		})
	}
}

func TestAssistDraftPrintsOnlyTheDraft(t *testing.T) {
	f := newFakeDaemon(t)
	f.on("GET /api/assist/sessions/"+id1, ok(assistSessionDetail()))
	res := f.run(runOpts{}, "assist", "draft", id1)
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	if !jsonEqual(t, res.stdout, assistDraft()) {
		t.Errorf("stdout = %s, want only the draft", res.stdout)
	}
}

func TestAssistTestExitsZeroForAFailingScriptButNonZeroForAPIErrors(t *testing.T) {
	f := newFakeDaemon(t)
	f.on("POST /api/assist/sessions/"+id1+"/test", ok(assistTestResult(1)))
	res := f.run(runOpts{}, "assist", "test", id1)
	if res.code != 0 {
		t.Errorf("exit %d for a script that exited 1; the request succeeded: %s", res.code, res.stderr)
	}
	if !jsonEqual(t, res.stdout, assistTestResult(1)) {
		t.Errorf("stdout = %s", res.stdout)
	}

	f = newFakeDaemon(t)
	f.on("POST /api/assist/sessions/"+id1+"/test", problem(423, "paused", "Paused", "the kill switch is engaged", map[string]any{"kill_switch": killSwitch(true)}))
	res = f.run(runOpts{}, "assist", "test", id1)
	if res.code != cli.ExitError {
		t.Errorf("exit %d for 423 paused, want %d", res.code, cli.ExitError)
	}
	if res.stdout != "" || !strings.Contains(res.stderr, "paused") {
		t.Errorf("stdout %q stderr %q", res.stdout, res.stderr)
	}
}

func TestSensorsValidatePostsTheFileOrStdinContent(t *testing.T) {
	valid := ok(map[string]any{"valid": true, "diagnostics": []any{}, "yaml": sensorYAML, "sensor": nil})
	want := map[string]any{"yaml": sensorYAML}

	dir := t.TempDir()
	file := filepath.Join(dir, "sensor.yaml")
	if err := os.WriteFile(file, []byte(sensorYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		args  []string
		stdin string
	}{
		"file":          {[]string{"sensors", "validate", file}, ""},
		"stdin dash":    {[]string{"sensors", "validate", "-"}, sensorYAML},
		"stdin default": {[]string{"sensors", "validate"}, sensorYAML},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeDaemon(t)
			f.on("POST /api/sensors/validate", valid)
			res := f.run(runOpts{stdin: tc.stdin}, tc.args...)
			if res.code != 0 {
				t.Fatalf("exit %d: %s", res.code, res.stderr)
			}
			req := f.only()
			if !jsonEqual(t, req.Body, want) {
				t.Errorf("body = %s, want %v", req.Body, want)
			}
			if !jsonEqual(t, res.stdout, map[string]any{"valid": true, "diagnostics": []any{}, "yaml": sensorYAML, "sensor": nil}) {
				t.Errorf("stdout = %s", res.stdout)
			}
		})
	}

	t.Run("invalid sensor exits non-zero", func(t *testing.T) {
		f := newFakeDaemon(t)
		f.on("POST /api/sensors/validate", ok(map[string]any{
			"valid": false, "diagnostics": []any{diagnostic("error", "no-script", "script missing")}, "yaml": nil, "sensor": nil,
		}))
		res := f.run(runOpts{stdin: "id: gh\n"}, "sensors", "validate")
		if res.code != cli.ExitError {
			t.Errorf("exit %d, want %d", res.code, cli.ExitError)
		}
		if !strings.Contains(res.stdout, "no-script") {
			t.Errorf("diagnostics not printed: %s", res.stdout)
		}
	})
}

// Inside an assist session (the environment variable the daemon sets for the agent) the test request says so; a
// person at a terminal sends no marker, so their test always runs and approves the script.
func TestAssistTestMarksRequestsMadeInsideASession(t *testing.T) {
	route := "POST /api/assist/sessions/" + id2 + "/test"
	for _, tc := range []struct {
		name   string
		env    map[string]string
		args   []string
		marked bool
	}{
		{"agent, session from the environment", map[string]string{cli.EnvAssistSession: id2}, []string{"assist", "test"}, true},
		{"agent, session as argument", map[string]string{cli.EnvAssistSession: id1}, []string{"assist", "test", id2}, true},
		{"person at a terminal", nil, []string{"assist", "test", id2}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDaemon(t)
			f.on(route, ok(assistTestResult(0)))
			res := f.run(runOpts{env: tc.env}, tc.args...)
			if res.code != 0 {
				t.Fatalf("exit %d: %s", res.code, res.stderr)
			}
			if got := f.only().Header.Get(cli.AssistAgentHeader) != ""; got != tc.marked {
				t.Errorf("agent marker sent = %v, want %v", got, tc.marked)
			}
		})
	}
}

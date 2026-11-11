package cli_test

import (
	"strings"
	"testing"
	"time"

	"hal9001/internal/cli"
)

func TestRuntimesStatusPrintsReadableSummaryWithFixes(t *testing.T) {
	f := newFakeDaemon(t)
	warn := []any{map[string]any{"code": "auth_missing", "message": "You are not signed in to Claude Code. Run `claude login`.", "command": "claude login", "url": nil}}
	f.on("GET /api/runtimes", ok(map[string]any{"items": []any{runtimeItem("claude", "warning", warn), runtimeItem("codex", "ready", nil)}}))
	res := f.run(runOpts{}, "runtimes", "status")
	if res.code != 0 {
		t.Fatalf("exit %d, stderr: %s", res.code, res.stderr)
	}
	for _, want := range []string{"claude: WARNING", "codex: READY", "auth_missing", "run: claude login", "managed, installed 0.100.0"} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("status output lacks %q:\n%s", want, res.stdout)
		}
	}
}

func TestRuntimesStatusJSONIsTheResponseBody(t *testing.T) {
	f := newFakeDaemon(t)
	body := map[string]any{"items": []any{runtimeItem("claude", "ready", nil)}}
	f.on("GET /api/runtimes", ok(body))
	res := f.run(runOpts{}, "runtimes", "status", "--json")
	if res.code != 0 || !jsonEqual(t, res.stdout, body) {
		t.Fatalf("exit %d, stdout:\n%s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
}

func TestRuntimesDetectPostsAndPrints(t *testing.T) {
	f := newFakeDaemon(t)
	f.on("POST /api/runtimes/detect", ok(map[string]any{"items": []any{runtimeItem("omp", "ready", nil)}}))
	res := f.run(runOpts{}, "runtimes", "detect")
	if res.code != 0 || !strings.Contains(res.stdout, "omp: READY") {
		t.Fatalf("exit %d, stdout:\n%s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
}

func TestRuntimesInstallWaitsUntilTheInstallEnds(t *testing.T) {
	cli.SetInstallPollInterval(t, time.Millisecond)
	f := newFakeDaemon(t)
	f.on("POST /api/runtimes/claude/adapter", canned{status: 202, body: runtimeItem("claude", "unavailable", nil, withAdapter(map[string]any{"installing": true, "installed": false}))})
	f.on("GET /api/runtimes",
		ok(map[string]any{"items": []any{runtimeItem("claude", "unavailable", nil, withAdapter(map[string]any{"installing": true, "installed": false}))}}),
		ok(map[string]any{"items": []any{runtimeItem("claude", "ready", nil)}}))
	res := f.run(runOpts{}, "runtimes", "install", "claude")
	if res.code != 0 || !strings.Contains(res.stdout, "installed the ACP adapter for claude (version 0.100.0)") {
		t.Fatalf("exit %d, stdout:\n%s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
}

func TestRuntimesInstallFailureIsAnError(t *testing.T) {
	cli.SetInstallPollInterval(t, time.Millisecond)
	f := newFakeDaemon(t)
	f.on("POST /api/runtimes/claude/adapter", canned{status: 202, body: runtimeItem("claude", "unavailable", nil, withAdapter(map[string]any{"installing": true, "installed": false}))})
	failed := []any{map[string]any{"code": "adapter_install_failed", "message": "Installing the adapter failed: npm exploded", "command": nil, "url": nil}}
	f.on("GET /api/runtimes", ok(map[string]any{"items": []any{runtimeItem("claude", "unavailable", failed, withAdapter(map[string]any{"installed": false}))}}))
	res := f.run(runOpts{}, "runtimes", "install", "claude")
	if res.code == 0 || !strings.Contains(res.stderr, "npm exploded") {
		t.Fatalf("exit %d, stderr: %s", res.code, res.stderr)
	}
}

func TestRuntimesInstallRefusedShowsTheProblem(t *testing.T) {
	f := newFakeDaemon(t)
	f.on("POST /api/runtimes/omp/adapter", problem(409, "invalid-state", "Invalid state", "runtime has no adapter hal9001 can install", nil))
	res := f.run(runOpts{}, "runtimes", "install", "omp")
	if res.code == 0 || !strings.Contains(res.stderr, "no adapter hal9001 can install") {
		t.Fatalf("exit %d, stderr: %s", res.code, res.stderr)
	}
}

func TestRuntimesUninstallDeletes(t *testing.T) {
	f := newFakeDaemon(t)
	f.on("DELETE /api/runtimes/claude/adapter", ok(runtimeItem("claude", "unavailable", nil, withAdapter(map[string]any{"installed": false}))))
	res := f.run(runOpts{}, "runtimes", "uninstall", "claude")
	if res.code != 0 || !strings.Contains(res.stdout, "removed the ACP adapter for claude") {
		t.Fatalf("exit %d, stdout:\n%s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
}

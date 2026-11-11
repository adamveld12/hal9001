package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func skipIfMissing(t *testing.T, bin string) {
	t.Helper()
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("%s not installed", bin)
	}
}

func mustRun(t *testing.T, spec Spec) Result {
	t.Helper()
	if spec.Cwd == "" {
		spec.Cwd = t.TempDir()
	}
	res, err := Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestEachLangRunsAndFeedsParser(t *testing.T) {
	tests := []struct {
		name         string
		bin          string
		spec         Spec
		wantDecision Decision
		wantPayload  string
	}{
		{
			name: "bash", bin: "bash",
			spec:         Spec{Lang: LangBash, Script: `read -r line; echo "{\"got\":$line}"`, Stdin: []byte("41\n")},
			wantDecision: DecisionEmitted, wantPayload: `{"got":41}`,
		},
		{
			name: "python", bin: "python3",
			spec: Spec{Lang: LangPython, Script: "import sys, json\nd = json.load(sys.stdin)\nd['py'] = True\nprint(json.dumps(d))",
				Stdin: []byte(`{"a":1}`)},
			wantDecision: DecisionEmitted, wantPayload: `{"a":1,"py":true}`,
		},
		{
			name: "node", bin: "node",
			spec: Spec{Lang: LangNode, Script: "const d = JSON.parse(require('fs').readFileSync(0, 'utf8'));\nconsole.log(JSON.stringify({n: d.n + 1}));",
				Stdin: []byte(`{"n":1}`)},
			wantDecision: DecisionEmitted, wantPayload: `{"n":2}`,
		},
		{
			name: "jq transforms", bin: "jq",
			spec:         Spec{Lang: LangJQ, Script: `. + {seen: true}`, Stdin: []byte(`{"id":5}`)},
			wantDecision: DecisionEmitted, wantPayload: `{"id":5,"seen":true}`,
		},
		{
			name: "jq select filters out", bin: "jq",
			spec:         Spec{Lang: LangJQ, Script: `select(.reason == "review_requested")`, Stdin: []byte(`{"reason":"mention"}`)},
			wantDecision: DecisionDropped,
		},
		{
			name: "jq select keeps", bin: "jq",
			spec:         Spec{Lang: LangJQ, Script: `select(.reason == "review_requested")`, Stdin: []byte(`{"reason":"review_requested"}`)},
			wantDecision: DecisionEmitted, wantPayload: `{"reason":"review_requested"}`,
		},
		{
			name: "exec runs argv directly", bin: "printf",
			spec:         Spec{Lang: LangExec, Command: []string{"printf", "%s", `{"x":"a b"}`}},
			wantDecision: DecisionEmitted, wantPayload: `{"x":"a b"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			skipIfMissing(t, tc.bin)
			res := mustRun(t, tc.spec)
			if res.ExitCode != 0 {
				t.Fatalf("exit %d, stderr: %s", res.ExitCode, res.Stderr)
			}
			out := res.Outcome(nil)
			if out.Decision != tc.wantDecision {
				t.Fatalf("decision = %q (%s), want %q; stdout=%q stderr=%q", out.Decision, out.Error, tc.wantDecision, res.Stdout, res.Stderr)
			}
			if tc.wantDecision == DecisionEmitted && string(out.Outputs[0].Payload) != tc.wantPayload {
				t.Errorf("payload = %s, want %s", out.Outputs[0].Payload, tc.wantPayload)
			}
		})
	}
}

func TestArgvRecordsWhatRan(t *testing.T) {
	skipIfMissing(t, "bash")
	res := mustRun(t, Spec{Lang: LangBash, Script: "true"})
	if len(res.Argv) != 2 || res.Argv[0] != "bash" || !strings.HasSuffix(res.Argv[1], "script.sh") {
		t.Errorf("argv = %v", res.Argv)
	}
	skipIfMissing(t, "jq")
	res = mustRun(t, Spec{Lang: LangJQ, Script: ".", Stdin: []byte("1")})
	if len(res.Argv) != 4 || strings.Join(res.Argv[:3], " ") != "jq -c -f" {
		t.Errorf("jq argv = %v", res.Argv)
	}
}

func TestTempScriptIsRemoved(t *testing.T) {
	skipIfMissing(t, "bash")
	tmp := t.TempDir()
	mustRun(t, Spec{Lang: LangBash, Script: "true", TempDir: tmp})
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Errorf("temp dir not cleaned: %v", entries)
	}
}

func TestNonZeroExitAndStderr(t *testing.T) {
	skipIfMissing(t, "bash")
	res := mustRun(t, Spec{Lang: LangBash, Script: `echo '{"a":1}'; echo boom >&2; exit 3`})
	if res.ExitCode != 3 || res.TimedOut || res.Killed {
		t.Errorf("result = %+v", res)
	}
	if string(res.Stderr) != "boom\n" {
		t.Errorf("stderr = %q", res.Stderr)
	}
	if got := res.Outcome(nil); got.Decision != DecisionError || !strings.Contains(got.Error, "exit status 3") {
		t.Errorf("outcome = %+v", got)
	}
}

func TestInvalidSpecsAreErrors(t *testing.T) {
	ctx := context.Background()
	for name, spec := range map[string]Spec{
		"unknown lang":     {Lang: "ruby", Script: "x"},
		"empty script":     {Lang: LangBash, Script: "  \n"},
		"exec no command":  {Lang: LangExec},
		"missing cwd":      {Lang: LangExec, Command: []string{"/bin/echo"}, Cwd: "/definitely/not/here"},
		"missing binary":   {Lang: LangExec, Command: []string{"hal-no-such-binary"}},
		"missing absolute": {Lang: LangExec, Command: []string{"/definitely/not/here"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Run(ctx, spec); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestInterpreterResolvedOnChildPATH(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "hal-fake-tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\necho '{\"from\":\"fake\"}'\n"), 0o755); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	res := mustRun(t, Spec{
		Lang: LangExec, Command: []string{"hal-fake-tool"},
		BaseEnv: map[string]string{"PATH": dir},
	})
	if string(res.Stdout) != "{\"from\":\"fake\"}\n" {
		t.Errorf("stdout = %q", res.Stdout)
	}

	_, err := Run(context.Background(), Spec{
		Lang: LangExec, Command: []string{"hal-fake-tool"},
		BaseEnv: map[string]string{"PATH": "/nonexistent"}, Cwd: t.TempDir(),
	})
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestStdinDelivery(t *testing.T) {
	skipIfMissing(t, "bash")
	t.Run("small payload", func(t *testing.T) {
		res := mustRun(t, Spec{Lang: LangBash, Script: `cat`, Stdin: []byte(`{"k":"v"}`)})
		if string(res.Stdout) != `{"k":"v"}` {
			t.Errorf("stdout = %q", res.Stdout)
		}
	})
	t.Run("no stdin means EOF not a hang", func(t *testing.T) {
		res := mustRun(t, Spec{Lang: LangBash, Script: `cat; echo done`, Timeout: 5 * time.Second})
		if string(res.Stdout) != "done\n" || res.TimedOut {
			t.Errorf("result = %+v", res)
		}
	})
	t.Run("payload larger than the pipe buffer", func(t *testing.T) {
		res := mustRun(t, Spec{Lang: LangBash, Script: `wc -c`, Stdin: make([]byte, 1<<20), Timeout: 10 * time.Second})
		if strings.TrimSpace(string(res.Stdout)) != strconv.Itoa(1<<20) {
			t.Errorf("wc -c = %q", res.Stdout)
		}
	})
	t.Run("child that never reads stdin does not hang the runner", func(t *testing.T) {
		res := mustRun(t, Spec{Lang: LangBash, Script: `echo hi`, Stdin: make([]byte, 4<<20), Timeout: 10 * time.Second})
		if string(res.Stdout) != "hi\n" || res.TimedOut {
			t.Errorf("result = %+v", res)
		}
	})
}

func processGone(pid int) bool {
	for range 100 {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestTimeoutKillsGrandchildren(t *testing.T) {
	skipIfMissing(t, "bash")
	// A loaded machine can take longer to start bash than the timeout allows; the script then never starts its
	// grandchild and there is nothing to check. Try again with a longer timeout until it got that far.
	var res Result
	var timeout time.Duration
	for timeout = 300 * time.Millisecond; timeout <= 20*time.Second; timeout *= 4 {
		res = mustRun(t, Spec{
			Lang:        LangBash,
			Script:      "sleep 30 &\necho $!\nwait",
			Timeout:     timeout,
			GracePeriod: time.Second,
		})
		if strings.TrimSpace(string(res.Stdout)) != "" {
			break
		}
	}
	if strings.TrimSpace(string(res.Stdout)) == "" {
		t.Fatalf("the script did not start its grandchild within %s", timeout/4)
	}
	if !res.TimedOut || !res.Killed {
		t.Fatalf("TimedOut=%v Killed=%v", res.TimedOut, res.Killed)
	}
	if res.Duration > timeout+5*time.Second {
		t.Errorf("took %s for a %s timeout", res.Duration, timeout)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(res.Stdout)))
	if err != nil {
		t.Fatalf("grandchild pid from stdout %q: %v", res.Stdout, err)
	}
	if !processGone(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("grandchild %d survived the timeout", pid)
	}
	if got := res.Outcome(nil); got.Decision != DecisionError || got.Error != "timeout" {
		t.Errorf("outcome = %+v", got)
	}
}

// runCancelledWhenReady runs the bash script, which must write the pid of the process it started to the file "pid" in
// its working directory (atomically, with mv) once it is set up, and cancels the run when that file appears. Waiting
// for the script, not for a delay, keeps the test independent of how long bash takes to start. It returns the result,
// the pid, and when the cancellation was issued.
func runCancelledWhenReady(t *testing.T, spec Spec) (res Result, pid int, cancelledAt time.Time) {
	t.Helper()
	spec.Lang = LangBash
	spec.Cwd = t.TempDir()
	pidFile := filepath.Join(spec.Cwd, "pid")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cancelled := make(chan time.Time, 1)
	go func() {
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			if _, err := os.Stat(pidFile); err == nil {
				cancelled <- time.Now()
				cancel()
				return
			}
		}
	}()
	res, err := Run(ctx, spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	select {
	case cancelledAt = <-cancelled:
	default:
		t.Fatalf("the script ended before it reported ready: %+v", res)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("pid file %q: %v", b, err)
	}
	return res, pid, cancelledAt
}

func TestTermIgnoredEscalatesToKillAfterGrace(t *testing.T) {
	skipIfMissing(t, "bash")
	const grace = 300 * time.Millisecond
	// The script and its child ignore SIGTERM; only SIGKILL can end them. The run is cancelled once they are set up.
	res, pid, cancelledAt := runCancelledWhenReady(t, Spec{
		Script:      "trap '' TERM\n(trap '' TERM; exec sleep 30) &\necho $! > pid.tmp && mv pid.tmp pid\nwhile true; do sleep 0.05; done",
		GracePeriod: grace,
	})
	ended := time.Since(cancelledAt)
	if !res.Killed || res.TimedOut {
		t.Errorf("Killed=%v TimedOut=%v; a cancelled run is Killed without TimedOut", res.Killed, res.TimedOut)
	}
	if res.Signal != "killed" {
		t.Errorf("signal = %q, want killed (SIGTERM was ignored)", res.Signal)
	}
	if ended < grace {
		t.Errorf("ended %s after the cancellation, before the %s grace period elapsed", ended, grace)
	}
	if !processGone(pid) {
		t.Errorf("grandchild %d survived", pid)
	}
}

func TestTermHonoredEndsBeforeGrace(t *testing.T) {
	skipIfMissing(t, "bash")
	res := mustRun(t, Spec{
		Lang: LangBash, Script: "sleep 30", Timeout: 100 * time.Millisecond, GracePeriod: 10 * time.Second,
	})
	if !res.TimedOut || res.Duration > 5*time.Second {
		t.Errorf("TimedOut=%v Duration=%s", res.TimedOut, res.Duration)
	}
	if res.Signal != "terminated" {
		t.Errorf("signal = %q, want terminated", res.Signal)
	}
}

func TestContextCancelKillsGroup(t *testing.T) {
	skipIfMissing(t, "bash")
	res, pid, _ := runCancelledWhenReady(t, Spec{
		Script:      "sleep 30 &\necho $! > pid.tmp && mv pid.tmp pid\nwait",
		Timeout:     time.Minute,
		GracePeriod: time.Second,
	})
	if !res.Killed || res.TimedOut {
		t.Errorf("Killed=%v TimedOut=%v; cancel must be Killed without TimedOut", res.Killed, res.TimedOut)
	}
	if !processGone(pid) {
		t.Errorf("grandchild %d survived cancel", pid)
	}
	if got := res.Outcome(nil); got.Error != "killed" {
		t.Errorf("outcome = %+v", got)
	}
}

func TestAlreadyCancelledContextDoesNotStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, Spec{Lang: LangExec, Command: []string{"/bin/echo", "x"}}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func TestBackgroundChildHoldingPipeDoesNotBlockOrLeak(t *testing.T) {
	skipIfMissing(t, "bash")
	start := time.Now()
	res := mustRun(t, Spec{Lang: LangBash, Script: "sleep 30 &\necho $!\necho '{\"ok\":true}'", Timeout: 20 * time.Second})
	if time.Since(start) > 5*time.Second {
		t.Fatalf("Run blocked %s on a child holding the output pipe", time.Since(start))
	}
	lines := strings.Fields(string(res.Stdout))
	pid, _ := strconv.Atoi(lines[0])
	if pid == 0 || !processGone(pid) {
		t.Errorf("leftover background child %d survived normal exit", pid)
	}
}

func TestOutputCap(t *testing.T) {
	skipIfMissing(t, "bash")
	res := mustRun(t, Spec{
		Lang:      LangBash,
		Script:    "head -c 200000 /dev/zero | tr '\\0' x\nhead -c 50000 /dev/zero | tr '\\0' e >&2\necho done >&2",
		MaxOutput: 1000,
		Timeout:   10 * time.Second,
	})
	if res.ExitCode != 0 || res.TimedOut {
		t.Fatalf("a chatty process must not block or fail: %+v", res)
	}
	if len(res.Stdout) != 1000 || !res.StdoutTruncated || res.StdoutBytes != 200000 {
		t.Errorf("stdout len=%d truncated=%v total=%d", len(res.Stdout), res.StdoutTruncated, res.StdoutBytes)
	}
	if len(res.Stderr) != 1000 || !res.StderrTruncated || res.StderrBytes != 50005 {
		t.Errorf("stderr len=%d truncated=%v total=%d", len(res.Stderr), res.StderrTruncated, res.StderrBytes)
	}
	if got := res.Outcome(nil); got.Decision != DecisionError {
		t.Errorf("truncated stdout must not parse as output: %+v", got)
	}
}

func TestOutputUnderCapIsNotTruncated(t *testing.T) {
	skipIfMissing(t, "bash")
	res := mustRun(t, Spec{Lang: LangBash, Script: "printf 12345", MaxOutput: 5})
	if string(res.Stdout) != "12345" || res.StdoutTruncated {
		t.Errorf("exactly-at-cap output: %q truncated=%v", res.Stdout, res.StdoutTruncated)
	}
}

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/acp-go-sdk"
)

// TestMain doubles as a tiny scripted ACP agent: when GO_ACP_PROBE_HELPER is
// set the test binary speaks ACP on stdio instead of running tests, so the
// probe can be exercised end to end through a real subprocess.
func TestMain(m *testing.M) {
	if os.Getenv("GO_ACP_PROBE_HELPER") == "1" {
		os.Exit(fakeAgent(os.Stdin, os.Stdout))
	}
	os.Exit(m.Run())
}

// fakeAgent answers initialize/session/new/session/load/session/set_mode and
// session/prompt. A prompt first reports an execute tool call, then asks for
// permission and echoes the chosen option id back as agent text. A prompt that
// says "remember ... word: X" stores X in $GO_ACP_PROBE_STATE (unless the state
// path is empty, simulating an agent without persistence); a prompt asking for
// the word replies with the stored value.
func fakeAgent(in *os.File, out *os.File) int {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	send := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = fmt.Fprintf(out, "%s\n", b)
	}
	state := os.Getenv("GO_ACP_PROBE_STATE")
	read := func() map[string]json.RawMessage {
		if !sc.Scan() {
			return nil
		}
		var m map[string]json.RawMessage
		_ = json.Unmarshal(sc.Bytes(), &m)
		return m
	}
	update := func(sid string, u map[string]any) {
		send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sid, "update": u}})
	}
	for m := read(); m != nil; m = read() {
		var method string
		_ = json.Unmarshal(m["method"], &method)
		id := m["id"]
		var params struct {
			SessionID string `json:"sessionId"`
			Prompt    []struct {
				Text string `json:"text"`
			} `json:"prompt"`
		}
		_ = json.Unmarshal(m["params"], &params)
		reply := func(result any) { send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}) }
		switch method {
		case "initialize":
			reply(map[string]any{"protocolVersion": 1, "agentInfo": map[string]any{"name": "fake", "version": "1"}, "agentCapabilities": map[string]any{"loadSession": true}})
		case "session/new":
			reply(map[string]any{"sessionId": "sess-1", "modes": map[string]any{
				"currentModeId":  "default",
				"availableModes": []any{map[string]any{"id": "default", "name": "Default"}, map[string]any{"id": "yolo", "name": "Yolo"}},
			}})
		case "session/load":
			update(params.SessionID, map[string]any{"sessionUpdate": "user_message_chunk", "content": map[string]any{"type": "text", "text": "replayed"}})
			reply(map[string]any{})
		case "session/set_mode":
			reply(map[string]any{})
		case "session/prompt":
			text := ""
			if len(params.Prompt) > 0 {
				text = params.Prompt[0].Text
			}
			switch {
			case strings.Contains(text, "What was the secret"):
				b, _ := os.ReadFile(state)
				update(params.SessionID, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "I do not know" + string(b)}})
			case strings.Contains(text, "secret word"):
				if state != "" {
					_ = os.WriteFile(state, []byte(text[strings.Index(text, ": ")+2:strings.Index(text, ". ")]), 0o600)
				}
				update(params.SessionID, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "OK"}})
			default:
				update(params.SessionID, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "Terminal", "kind": "execute", "status": "pending", "rawInput": map[string]any{}})
				send(map[string]any{"jsonrpc": "2.0", "id": 100, "method": "session/request_permission", "params": map[string]any{
					"sessionId": params.SessionID,
					"toolCall":  map[string]any{"toolCallId": "t1", "title": "echo hi", "kind": "execute", "rawInput": map[string]any{"command": "echo hi"}},
					"options": []any{
						map[string]any{"optionId": "yes-once", "name": "Yes", "kind": "allow_once"},
						map[string]any{"optionId": "yes-always", "name": "Always", "kind": "allow_always"},
						map[string]any{"optionId": "no", "name": "No", "kind": "reject_once"},
					},
				}})
				resp := read()
				var res struct {
					Outcome struct {
						Outcome  string `json:"outcome"`
						OptionID string `json:"optionId"`
					} `json:"outcome"`
				}
				if resp != nil {
					_ = json.Unmarshal(resp["result"], &res)
				}
				update(params.SessionID, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "outcome=" + res.Outcome.Outcome + " option=" + res.Outcome.OptionID}})
			}
			reply(map[string]any{"stopReason": "end_turn"})
		}
	}
	return 0
}

// probe runs the probe against the fake agent and returns stdout.
func probe(t *testing.T, state string, args ...string) (string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	full := append([]string{
		"--env", "GO_ACP_PROBE_HELPER=1",
		"--env", "GO_ACP_PROBE_STATE=" + state,
		"--log", filepath.Join(t.TempDir(), "wire.jsonl"),
	}, args...)
	full = append(full, "--", os.Args[0])
	code := run(full, &out, &errOut)
	if code != 0 {
		t.Logf("stderr: %s", errOut.String())
	}
	return out.String(), code
}

func TestPolicyDecidesPermissionOutcome(t *testing.T) {
	tests := []struct {
		policy string
		want   string // substring of the agent's echo of what the client answered
		perm   string // substring of the permission decision line
	}{
		{policyAllowAll, "outcome=selected option=yes-once", "allow:yes-once"},
		{policyRejectAll, "outcome=selected option=no", "reject:no"},
		{policyLogAndReject, "outcome=selected option=no", "reject:no"},
		{policyCancel, "outcome=cancelled", "cancelled"},
	}
	for _, tc := range tests {
		t.Run(tc.policy, func(t *testing.T) {
			out, code := probe(t, "", "--policy", tc.policy, "--prompt", "go")
			if code != 0 {
				t.Fatalf("exit %d\n%s", code, out)
			}
			for _, want := range []string{tc.want, tc.perm, `kind="execute"`, `rawInput={"command":"echo hi"}`} {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestAllowAlwaysPicksAllowAlwaysOption(t *testing.T) {
	out, code := probe(t, "", "--policy", policyAllowAll, "--allow-always", "--prompt", "go")
	if code != 0 || !strings.Contains(out, "option=yes-always") {
		t.Fatalf("exit %d, want option=yes-always:\n%s", code, out)
	}
}

func TestModeIsSetAndSessionModesPrinted(t *testing.T) {
	out, code := probe(t, "", "--mode", "yolo", "--no-prompt")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, want := range []string{`id="yolo" name="Yolo"`, "session/set_mode yolo", "loadSession"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestLoadAndResumeAreMutuallyExclusive(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"--load", "a", "--resume", "b", "--", "x"}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "mutually exclusive") {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
}

func TestResumeCheckDetectsRestoredAndLostContext(t *testing.T) {
	t.Run("agent persists context", func(t *testing.T) {
		state := filepath.Join(t.TempDir(), "state")
		out, code := probe(t, state, "--resume-check")
		if code != 0 || !strings.Contains(out, "RESUME-CHECK RESULT: PASS") {
			t.Fatalf("exit %d, want PASS:\n%s", code, out)
		}
		if !strings.Contains(out, "1 session/update notifications replayed") {
			t.Errorf("replay count missing:\n%s", out)
		}
	})
	t.Run("agent loses context", func(t *testing.T) {
		out, code := probe(t, "", "--resume-check")
		if code != 0 || !strings.Contains(out, "RESUME-CHECK RESULT: FAIL") {
			t.Fatalf("exit %d, want FAIL:\n%s", code, out)
		}
	})
}

func TestWireLogRecordsBothDirections(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "wire.jsonl")
	var out, errOut bytes.Buffer
	code := run([]string{"--env", "GO_ACP_PROBE_HELPER=1", "--log", logPath, "--no-prompt", "--", os.Args[0]}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var sent, received int
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		var rec struct {
			Dir string          `json:"dir"`
			Msg json.RawMessage `json:"msg"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("wire log line is not JSON: %q", line)
		}
		switch rec.Dir {
		case "->":
			sent++
		case "<-":
			received++
		}
	}
	// initialize and session/new each produce one request and one response.
	if sent < 2 || received < 2 {
		t.Errorf("sent=%d received=%d, want at least 2 each", sent, received)
	}
}

func TestParseFlagsRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no agent command", []string{"--policy", policyAllowAll}, "missing agent command"},
		{"bad policy", []string{"--policy", "yes", "--", "agent"}, "invalid --policy"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseFlags(tc.args, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestPickOption(t *testing.T) {
	opts := func(kinds ...acp.PermissionOptionKind) []acp.PermissionOption {
		var o []acp.PermissionOption
		for i, k := range kinds {
			o = append(o, acp.PermissionOption{OptionId: acp.PermissionOptionId(fmt.Sprintf("o%d", i)), Kind: k})
		}
		return o
	}
	tests := []struct {
		name   string
		opts   []acp.PermissionOption
		always bool
		allow  bool
		want   string
		ok     bool
	}{
		{"allow prefers once", opts(acp.PermissionOptionKindAllowAlways, acp.PermissionOptionKindAllowOnce), false, true, "o1", true},
		{"allow always preferred when asked", opts(acp.PermissionOptionKindAllowOnce, acp.PermissionOptionKindAllowAlways), true, true, "o1", true},
		{"allow falls back to always", opts(acp.PermissionOptionKindAllowAlways, acp.PermissionOptionKindRejectOnce), false, true, "o0", true},
		{"reject prefers once", opts(acp.PermissionOptionKindRejectAlways, acp.PermissionOptionKindRejectOnce), false, false, "o1", true},
		{"reject falls back to always", opts(acp.PermissionOptionKindAllowOnce, acp.PermissionOptionKindRejectAlways), false, false, "o1", true},
		{"no allow option offered", opts(acp.PermissionOptionKindRejectOnce), false, true, "", false},
		{"no reject option offered", opts(acp.PermissionOptionKindAllowOnce), false, false, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := pickOption(tc.opts, tc.always, tc.allow)
			if ok != tc.ok || string(got) != tc.want {
				t.Fatalf("pickOption = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestApproveFormPicksApproveWording(t *testing.T) {
	raw := json.RawMessage(`{"requestedSchema":{"properties":{"value":{"enum":["Deny","Approve write for this session","Approve"]},"other":{"enum":["x","y"]}}}}`)
	got := approveForm(raw)
	if got["value"] != "Approve write for this session" {
		t.Errorf("value = %v, want first enum entry starting with Approve", got["value"])
	}
	if got["other"] != "x" {
		t.Errorf("other = %v, want first enum entry when none starts with Approve", got["other"])
	}
	if len(approveForm(json.RawMessage(`not json`))) != 0 {
		t.Error("unparseable params must yield an empty form")
	}
}

func TestMCPEchoServesToolAndLogsCalls(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "calls.log")
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"nope"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := runMCPEcho(logPath, strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d responses, want 4 (notification gets none):\n%s", len(lines), out.String())
	}
	for i, want := range []string{`"protocolVersion":"2025-03-26"`, `"name":"echo"`, `echo: hi`, `-32601`} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("response %d = %s, want containing %s", i, lines[i], want)
		}
	}
	b, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(b), `echo "hi"`) {
		t.Errorf("call log = %q, err %v; want the executed call recorded", b, err)
	}
}

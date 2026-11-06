package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeHost struct {
	mu        sync.Mutex
	decision  Decision
	approvals []ApprovalRequest
	records   []CallRecord
	outcomes  []string
}

func (h *fakeHost) Approve(_ context.Context, r ApprovalRequest) (Decision, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.approvals = append(h.approvals, r)
	d := h.decision
	d.RecordID = "rec-approved"
	return d, nil
}

func (h *fakeHost) Record(_ context.Context, c CallRecord) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, c)
	return "rec-auto", nil
}

func (h *fakeHost) Finish(_ context.Context, id, outcome string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.outcomes = append(h.outcomes, id+":"+outcome)
}

type memTokens struct {
	mu      sync.Mutex
	live    map[string]bool
	revoked int
}

func (m *memTokens) CreateToken(_ context.Context, hash, _, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.live == nil {
		m.live = map[string]bool{}
	}
	m.live[hash] = true
	return nil
}

func (m *memTokens) RevokeToken(_ context.Context, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.live, hash)
	m.revoked++
	return nil
}

// fakeGH writes a gh script that logs its arguments and stdin and prints a canned reply.
func fakeGH(t *testing.T) (dir, log string) {
	t.Helper()
	dir, log = t.TempDir(), filepath.Join(t.TempDir(), "gh.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\ncase \"$*\" in *--input*) cat >> " + log + " ;; esac\necho '{\"ok\":true}'\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil { //nolint:gosec // test script
		t.Fatal(err)
	}
	return dir, log
}

func newTestHub(t *testing.T, host Host, ghDir string) (*Hub, *memTokens) {
	t.Helper()
	tok := &memTokens{}
	h, err := NewHub(Options{Store: tok, Host: host, GH: GHOptions{Path: ghDir + ":/usr/bin:/bin"}})
	if err != nil {
		t.Fatal(err)
	}
	return h, tok
}

func openSession(t *testing.T, h *Hub) *Session {
	t.Helper()
	s, err := h.Open(context.Background(), RunSpec{
		RunID: "run1", ApprovalTimeout: time.Minute,
		Info: func() RunInfo {
			return RunInfo{AgentRunID: "run1", NodeRunID: "nr1", FlowID: "f", NodeID: "n", Input: json.RawMessage(`{"a":1}`)}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestGHAPIGetRunsWithoutApproval(t *testing.T) {
	dir, log := fakeGH(t)
	host := &fakeHost{}
	h, _ := newTestHub(t, host, dir)
	s := openSession(t, h)
	res, err := s.Call(context.Background(), "gh_api", json.RawMessage(`{"path":"/repos/o/r/pulls/1","params":{"state":"open"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, `"ok":true`) {
		t.Errorf("result = %q", res.Text)
	}
	if len(host.approvals) != 0 || len(host.records) != 1 || len(host.outcomes) != 1 || host.outcomes[0] != "rec-auto:ok" {
		t.Errorf("approvals=%d records=%d outcomes=%v", len(host.approvals), len(host.records), host.outcomes)
	}
	b, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(b)); got != "api -X GET -f state=open -- /repos/o/r/pulls/1" {
		t.Errorf("gh argv = %q", got)
	}
}

func TestGHAPIWriteNeedsApproval(t *testing.T) {
	for _, tc := range []struct {
		name    string
		d       Decision
		wantErr string
		ran     bool
	}{
		{"allowed", Decision{Allowed: true}, "", true},
		{"denied", Decision{Comment: "no thanks"}, "denied by the user: no thanks", false},
		{"expired", Decision{Expired: true}, "approval timeout", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, log := fakeGH(t)
			host := &fakeHost{decision: tc.d}
			h, _ := newTestHub(t, host, dir)
			s := openSession(t, h)
			_, err := s.Call(context.Background(), "gh_api", json.RawMessage(`{"method":"post","path":"/repos/o/r/issues/1/comments","params":{"body":"hi"}}`))
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if len(host.approvals) != 1 || host.approvals[0].Tool != "gh_api" || host.approvals[0].RunID != "run1" || host.approvals[0].Timeout != time.Minute {
				t.Fatalf("approvals = %+v", host.approvals)
			}
			b, _ := os.ReadFile(log)
			if ran := len(b) > 0; ran != tc.ran {
				t.Errorf("gh ran = %v, want %v (log %q)", ran, tc.ran, b)
			}
			if tc.ran && !strings.Contains(string(b), `{"body":"hi"}`) {
				t.Errorf("body not sent as JSON on stdin: %q", b)
			}
		})
	}
}

func TestGHAPIRejectsBadInput(t *testing.T) {
	dir, _ := fakeGH(t)
	h, _ := newTestHub(t, &fakeHost{}, dir)
	s := openSession(t, h)
	for _, args := range []string{
		`{"path":"https://evil.example/x"}`,
		`{"path":"/repos/o/r/../../x"}`,
		`{"path":"-h"}`,
		`{"method":"TRACE","path":"/x"}`,
	} {
		if _, err := s.Call(context.Background(), "gh_api", json.RawMessage(args)); err == nil {
			t.Errorf("%s: want an error", args)
		}
	}
}

func TestTokenLifecycle(t *testing.T) {
	h, tok := newTestHub(t, &fakeHost{}, t.TempDir())
	s := openSession(t, h)
	if got, ok := h.Lookup(s.Token()); !ok || got != s {
		t.Fatal("live token not found")
	}
	if _, ok := h.Lookup(s.Token() + "x"); ok {
		t.Error("altered token accepted")
	}
	if len(tok.live) != 1 || tok.live[s.Token()] {
		t.Errorf("store must hold only the hash: %v", tok.live)
	}
	s.Close()
	s.Close()
	if _, ok := h.Lookup(s.Token()); ok {
		t.Error("token accepted after Close")
	}
	if tok.revoked != 1 || len(tok.live) != 0 {
		t.Errorf("revoked=%d live=%d", tok.revoked, len(tok.live))
	}
}

func TestProvidersExtendTheToolSet(t *testing.T) {
	h, _ := newTestHub(t, &fakeHost{}, t.TempDir())
	closed := 0
	h.AddProvider(func(_ context.Context, spec RunSpec) (Extension, error) {
		if spec.Sandbox != "box" {
			return Extension{}, nil
		}
		return Extension{
			Tools: []Tool{{Name: "fs_read", InputSchema: obj(nil), Run: func(context.Context, Call) (Result, error) { return Result{Text: "contents"}, nil }}},
			Close: func() { closed++ },
		}, nil
	})
	info := func() RunInfo { return RunInfo{AgentRunID: "r"} }
	plain, err := h.Open(context.Background(), RunSpec{RunID: "r", Info: info})
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if _, err := plain.Call(context.Background(), "fs_read", nil); !errors.Is(err, ErrUnknownTool) {
		t.Errorf("plain session has fs_read: %v", err)
	}
	box, err := h.Open(context.Background(), RunSpec{RunID: "r", Info: info, Sandbox: "box"})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := box.Call(context.Background(), "fs_read", nil); err != nil || res.Text != "contents" {
		t.Errorf("fs_read = %v, %v", res, err)
	}
	box.Close()
	if closed != 1 {
		t.Errorf("extension closed %d times", closed)
	}
	h.AddProvider(func(context.Context, RunSpec) (Extension, error) {
		return Extension{Tools: []Tool{{Name: "now", InputSchema: obj(nil), Run: func(context.Context, Call) (Result, error) { return Result{}, nil }}}}, nil
	})
	if _, err := h.Open(context.Background(), RunSpec{RunID: "r", Info: info}); err == nil {
		t.Error("a provider tool shadowing a built-in must fail the open")
	}
}

func TestPreambleAndRunTools(t *testing.T) {
	h, _ := newTestHub(t, &fakeHost{}, t.TempDir())
	s, err := h.Open(context.Background(), RunSpec{RunID: "r", Info: func() RunInfo {
		return RunInfo{AgentRunID: "r", FlowID: "f", NodeID: "n", SensorID: "s", EventID: "e1", RunKind: "event",
			PolledAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Input: json.RawMessage(`{"big":"` + strings.Repeat("x", 20000) + `"}`)}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := Preamble(PreambleData{Info: s.spec.Info(), Now: time.Now(), Tools: s.Tools()})
	for _, want := range []string{"flow: f", "node: n", "sensor: s", "event: e1", "polled_at: 2026-01-02T03:04:05Z", "run_kind: event", "time: ", "truncated", "- gh_api:", "- now:"} {
		if !strings.Contains(p, want) {
			t.Errorf("preamble lacks %q", want)
		}
	}
	res, err := s.Call(context.Background(), "node_input", nil)
	if err != nil || len(res.Text) < 20000 {
		t.Errorf("node_input must be complete: %d bytes, %v", len(res.Text), err)
	}
	res, err = s.Call(context.Background(), "now", json.RawMessage(`{"timezone":"Asia/Tokyo"}`))
	if err != nil || !strings.Contains(res.Text, "Asia/Tokyo") {
		t.Errorf("now = %q, %v", res.Text, err)
	}
	if _, err := s.Call(context.Background(), "now", json.RawMessage(`{"timezone":"Mars/Base"}`)); err == nil {
		t.Error("unknown timezone accepted")
	}
}

func TestReadOnlyRunRefusesWritesWithoutAsking(t *testing.T) {
	dir, log := fakeGH(t)
	host := &fakeHost{decision: Decision{Allowed: true}}
	h, _ := newTestHub(t, host, dir)
	s, err := h.Open(context.Background(), RunSpec{RunID: "r", ReadOnly: true, Info: func() RunInfo { return RunInfo{AgentRunID: "r"} }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.Call(context.Background(), "gh_api", json.RawMessage(`{"method":"DELETE","path":"/repos/o/r"}`))
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("err = %v", err)
	}
	if len(host.approvals) != 0 || readFile(log) != "" {
		t.Errorf("approvals=%d gh log=%q", len(host.approvals), readFile(log))
	}
	if _, err := s.Call(context.Background(), "gh_api", json.RawMessage(`{"path":"/user"}`)); err != nil {
		t.Errorf("a GET must still run: %v", err)
	}
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

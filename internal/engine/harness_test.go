package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"hal9001/internal/agent"
	"hal9001/internal/agent/agenttest"
	"hal9001/internal/agent/fakeagent"
	"hal9001/internal/core"
	"hal9001/internal/engine"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

var (
	fakeOnce sync.Once
	fakeBin  string
	fakeErr  error
)

// fakeAgentBinary builds cmd/fake-acp-agent once per test binary.
func fakeAgentBinary(t *testing.T) string {
	t.Helper()
	fakeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "hal-fake-agent-")
		if err != nil {
			fakeErr = err
			return
		}
		fakeBin, fakeErr = agenttest.BuildFakeAgent(dir)
	})
	if fakeErr != nil {
		t.Fatalf("build fake agent: %v", fakeErr)
	}
	return fakeBin
}

func TestMain(m *testing.M) {
	code := m.Run()
	if fakeBin != "" {
		_ = os.RemoveAll(filepath.Dir(fakeBin))
	}
	os.Exit(code)
}

// recNotifier records every notification.
type recNotifier struct {
	mu  sync.Mutex
	got []core.Notification
}

func (r *recNotifier) Notify(_ context.Context, n core.Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, n)
	return nil
}

func (r *recNotifier) all() []core.Notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]core.Notification(nil), r.got...)
}

func (r *recNotifier) kind(k string) []core.Notification {
	var out []core.Notification
	for _, n := range r.all() {
		if n.Kind == k {
			out = append(out, n)
		}
	}
	return out
}

const uiBase = "http://localhost:9001"

// testWriter sends log output to the test log.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

type harness struct {
	t     *testing.T
	home  string
	st    *store.Store
	eng   *engine.Engine
	notes *recNotifier
	clock agent.Clock
	path  string

	config   string // config.yaml lines besides runtimes
	runtimes map[string]string
}

type option func(*harness)

func withClock(c agent.Clock) option { return func(h *harness) { h.clock = c } }
func withPath(p string) option       { return func(h *harness) { h.path = p } }
func withConfig(c string) option     { return func(h *harness) { h.config = c } }

func newHarness(t *testing.T, opts ...option) *harness {
	t.Helper()
	h := &harness{t: t, home: t.TempDir(), notes: &recNotifier{}, path: os.Getenv("PATH"), runtimes: map[string]string{}}
	for _, o := range opts {
		o(h)
	}
	for _, d := range []string{"sensors", "flows", "prompts"} {
		if err := os.MkdirAll(filepath.Join(h.home, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(filepath.Join(h.home, "data", "hal.db"))
	if err != nil {
		t.Fatal(err)
	}
	h.st = st
	t.Cleanup(func() { _ = st.Close() })
	h.eng = h.newEngine()
	return h
}

func (h *harness) newEngine() *engine.Engine {
	h.t.Helper()
	eng, err := engine.New(engine.Options{
		Store: h.st, Home: h.home, BasePATH: h.path, Notifier: h.notes, UIBaseURL: uiBase, Clock: h.clock,
		Log: slog.New(slog.NewTextHandler(testWriter{h.t}, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return eng
}

// start loads the workspace into the engine and starts it; the engine is
// stopped at cleanup.
func (h *harness) start() {
	h.t.Helper()
	h.reload()
	if err := h.eng.Start(h.t.Context()); err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(h.stopEngine)
}

func (h *harness) stopEngine() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := h.eng.Stop(ctx); err != nil {
		h.t.Errorf("stop engine: %v", err)
	}
}

// restart stops the engine (as a daemon shutdown would) and starts a new one
// on the same home and store.
func (h *harness) restart() {
	h.t.Helper()
	h.stopEngine()
	h.eng = h.newEngine()
	h.reload()
	if err := h.eng.Start(h.t.Context()); err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(h.stopEngine)
}

func (h *harness) write(rel, content string) {
	h.t.Helper()
	p := filepath.Join(h.home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) sensor(id, yaml string) { h.t.Helper(); h.write("sensors/"+id+".yaml", yaml) }
func (h *harness) flow(id, yaml string)   { h.t.Helper(); h.write("flows/"+id+".yaml", yaml) }
func (h *harness) prompt(body string) {
	h.t.Helper()
	h.write("prompts/p.md", "---\nname: p\ndescription: test\noutput: none\n---\n"+body)
}

func (h *harness) env(content string) { h.t.Helper(); h.write("env", content) }

// idleSensor is a sensor that never emits; flows subscribe to it and tests
// inject events.
func (h *harness) idleSensor() {
	h.t.Helper()
	h.sensor("src", "id: src\ninterval: 1h\nlang: bash\nscript: \"true\"\nkey: \"{{.id}}\"\nfirst_run: fire\ntimeout: 10s\n")
}

// runtime registers a fake ACP runtime driven by script.
func (h *harness) runtime(name string, s fakeagent.Script) {
	h.t.Helper()
	bin := fakeAgentBinary(h.t)
	if s.Modes == nil {
		s.Modes = []fakeagent.Mode{{ID: "default"}, {ID: "bypass"}}
	}
	dir := filepath.Join(h.home, "fake", name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		h.t.Fatal(err)
	}
	s.SessionsDir = filepath.Join(dir, "sessions")
	s.LogPath = filepath.Join(dir, "calls.jsonl")
	script := filepath.Join(dir, "script.json")
	if err := s.Write(script); err != nil {
		h.t.Fatal(err)
	}
	h.runtimes[name] = fmt.Sprintf("  %s:\n    command: [%q, --script, %q]\n    modes: {ask: default, full: bypass}\n", name, bin, script)
}

func (h *harness) writeConfig() {
	h.t.Helper()
	var b strings.Builder
	b.WriteString(h.config)
	b.WriteString("\n")
	if len(h.runtimes) > 0 {
		b.WriteString("runtimes:\n")
		names := make([]string, 0, len(h.runtimes))
		for n := range h.runtimes {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			b.WriteString(h.runtimes[n])
		}
	}
	h.write("config.yaml", b.String())
}

// reload writes config.yaml, reloads the workspace from disk and applies it.
func (h *harness) reload() {
	h.t.Helper()
	h.writeConfig()
	ws := spec.Load(h.home)
	for _, d := range ws.AllDiagnostics() {
		if d.Severity == spec.SeverityError {
			h.t.Fatalf("workspace diagnostic: %s", d)
		}
	}
	h.eng.Apply(ws)
}

// poll runs a plain manual poll (no options) and returns its sensor run.
func (h *harness) poll(ctx context.Context, sensorID string) (*store.SensorRun, error) {
	res, err := h.eng.PollSensor(ctx, sensorID, core.PollOptions{})
	return res.Run, err
}

func (h *harness) inject(item string) core.InjectResult {
	h.t.Helper()
	res, err := h.eng.InjectEvent(h.t.Context(), "src", json.RawMessage(item))
	if err != nil {
		h.t.Fatalf("inject %s: %v", item, err)
	}
	return res
}

// injectRun injects an item and returns the single flow run it created.
func (h *harness) injectRun(item string) string {
	h.t.Helper()
	res := h.inject(item)
	if len(res.FlowRunIDs) != 1 {
		h.t.Fatalf("inject created flow runs %v, want exactly one", res.FlowRunIDs)
	}
	return res.FlowRunIDs[0]
}

const waitFor = 30 * time.Second

// tick advances a manual clock the harness was built with (withClock) and returns once the
// sensor schedulers have handled the tick.
func (h *harness) tick(clock *agenttest.ManualClock, d time.Duration) {
	h.t.Helper()
	if !clock.Tick(d, waitFor) {
		h.t.Fatalf("the sensor schedulers did not re-arm after the tick\n%s", h.dump())
	}
}

// eventually polls cond until it holds or the test times out.
func (h *harness) eventually(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(waitFor)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s\n%s", what, h.dump())
		}
		time.Sleep(15 * time.Millisecond)
	}
}

// waitRun waits until the flow run has one of the statuses.
func (h *harness) waitRun(id string, want ...store.FlowRunStatus) *store.FlowRun {
	h.t.Helper()
	var fr *store.FlowRun
	h.eventually(fmt.Sprintf("flow run %s to be %v", id, want), func() bool {
		var err error
		fr, err = h.st.GetFlowRun(context.Background(), id)
		if err != nil {
			h.t.Fatal(err)
		}
		for _, w := range want {
			if fr.Status == w {
				return true
			}
		}
		if fr.Status.Terminal() {
			h.t.Fatalf("flow run %s ended %s, want %v\n%s", id, fr.Status, want, h.dumpRun(id))
		}
		return false
	})
	return fr
}

func (h *harness) run(id string) *store.FlowRun {
	h.t.Helper()
	fr, err := h.st.GetFlowRun(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return fr
}

// nodeRuns returns every node run of a flow run in creation order, with content.
func (h *harness) nodeRuns(runID string) []store.NodeRun {
	h.t.Helper()
	page, err := h.st.ListNodeRuns(context.Background(), store.NodeRunFilter{FlowRunID: runID, ListOpts: store.ListOpts{Oldest: true, Limit: 500}})
	if err != nil {
		h.t.Fatal(err)
	}
	return page.Items
}

// node returns the single node run of nodeID in the flow run.
func (h *harness) node(runID, nodeID string) store.NodeRun {
	h.t.Helper()
	var found []store.NodeRun
	for _, n := range h.nodeRuns(runID) {
		if n.NodeID == nodeID {
			found = append(found, n)
		}
	}
	if len(found) != 1 {
		h.t.Fatalf("flow run %s has %d node runs of %q, want 1\n%s", runID, len(found), nodeID, h.dumpRun(runID))
	}
	return found[0]
}

func (h *harness) inboxItems() []store.InboxItem {
	h.t.Helper()
	page, err := h.st.ListInbox(context.Background(), store.InboxFilter{ListOpts: store.ListOpts{Oldest: true, Limit: 500}})
	if err != nil {
		h.t.Fatal(err)
	}
	return page.Items
}

func (h *harness) events(sensor string) []store.Event {
	h.t.Helper()
	page, err := h.st.ListEvents(context.Background(), store.EventFilter{SensorID: sensor, ListOpts: store.ListOpts{Oldest: true, Limit: 500}})
	if err != nil {
		h.t.Fatal(err)
	}
	return page.Items
}

func (h *harness) approvals(statuses ...store.ApprovalState) []store.Approval {
	h.t.Helper()
	page, err := h.st.ListApprovals(context.Background(), store.ApprovalFilter{Statuses: statuses, ListOpts: store.ListOpts{Oldest: true, Limit: 100}})
	if err != nil {
		h.t.Fatal(err)
	}
	return page.Items
}

func (h *harness) agentRuns(nodeRunID string) []store.AgentRun {
	h.t.Helper()
	page, err := h.st.ListAgentRuns(context.Background(), store.AgentRunFilter{NodeRunID: nodeRunID, ListOpts: store.ListOpts{Oldest: true, Limit: 20}})
	if err != nil {
		h.t.Fatal(err)
	}
	return page.Items
}

// waitProposal waits for a pending proposal approval and returns it.
func (h *harness) waitProposal() store.Approval {
	h.t.Helper()
	var got store.Approval
	h.eventually("a pending proposal approval", func() bool {
		for _, a := range h.approvals(store.ApprovalPending) {
			if a.Type == store.ApprovalProposal {
				got = a
				return true
			}
		}
		return false
	})
	return got
}

func (h *harness) dumpRun(runID string) string {
	var b strings.Builder
	fr, err := h.st.GetFlowRun(context.Background(), runID)
	if err != nil {
		return err.Error()
	}
	fmt.Fprintf(&b, "flow run %s kind=%s status=%s dry=%v error=%q\n", fr.ID, fr.Kind, fr.Status, fr.DryRun, fr.Error)
	for _, n := range h.nodeRuns(runID) {
		fmt.Fprintf(&b, "  node %s [%s] status=%s decision=%s error=%q reason=%q ports=%v\n", n.NodeID, n.ID, n.Status, n.Decision, n.Error, n.Reason, n.Ports)
	}
	return b.String()
}

// dump describes every flow run, for failure messages.
func (h *harness) dump() string {
	page, err := h.st.ListFlowRuns(context.Background(), store.FlowRunFilter{
		Kinds: []store.FlowRunKind{store.KindLive, store.KindReplay, store.KindTest, store.KindContinuation}, ListOpts: store.ListOpts{Oldest: true, Limit: 50},
	})
	if err != nil {
		return err.Error()
	}
	var b strings.Builder
	for _, fr := range page.Items {
		b.WriteString(h.dumpRun(fr.ID))
	}
	return b.String()
}

// text returns a stored content value.
func (h *harness) text(c store.Content) string {
	h.t.Helper()
	b, err := h.st.ReadContent(c)
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

// pidAlive reports whether a process exists.
func pidAlive(pid int) bool {
	return exec.CommandContext(context.Background(), "kill", "-0", fmt.Sprint(pid)).Run() == nil
}

// nodeStatus is the status of the node's run, or "" while it does not exist yet.
func (h *harness) nodeStatus(runID, nodeID string) store.NodeRunStatus {
	for _, n := range h.nodeRuns(runID) {
		if n.NodeID == nodeID {
			return n.Status
		}
	}
	return ""
}

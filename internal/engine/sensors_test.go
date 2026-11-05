package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"hal9001/internal/core"
	"hal9001/internal/store"
)

// flowYAML is a flow that only writes an inbox note, so tests can count what a
// sensor fanned out.
func flowYAML(id, sensor string) string {
	return "id: " + id + "\nenabled: true\nsubscribe: [" + sensor + "]\nmax_concurrent: 10\nnodes:\n  - {id: note, type: inbox, title: \"got {{.input.id}}\"}\n"
}

// Scenario 2: first_run fire turns every item into an event; the same output
// creates nothing; a changed updated_at (part of the key) creates one.
func TestScenario2PollDedupAndRetrigger(t *testing.T) {
	h := newHarness(t)
	items := filepath.Join(t.TempDir(), "items.jsonl")
	write := func(s string) {
		if err := os.WriteFile(items, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"id":"a","updated_at":"1"}` + "\n" + `{"id":"b","updated_at":"1"}` + "\n" + `{"id":"c","updated_at":"1"}` + "\n")
	h.sensor("src", "id: src\ninterval: 1h\nlang: bash\nscript: cat "+items+"\nkey: \"{{.id}}:{{.updated_at}}\"\nfirst_run: fire\ntimeout: 10s\n")
	h.flow("f", flowYAML("f", "src"))
	h.start()

	// The scheduler polls an overdue interval sensor at start; wait for it.
	h.eventually("scheduled first poll", func() bool { return len(h.events("src")) == 3 })

	run, err := h.eng.PollSensor(t.Context(), "src")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.SensorSucceeded || run.ItemCount != 3 || run.NewCount != 0 || run.Origin != store.OriginManual {
		t.Fatalf("second poll: %+v", run)
	}
	if got := len(h.events("src")); got != 3 {
		t.Fatalf("same output created events: %d", got)
	}

	write(`{"id":"a","updated_at":"1"}` + "\n" + `{"id":"b","updated_at":"2"}` + "\n" + `{"id":"c","updated_at":"1"}` + "\n")
	run, err = h.eng.PollSensor(t.Context(), "src")
	if err != nil {
		t.Fatal(err)
	}
	if run.NewCount != 1 {
		t.Fatalf("changed updated_at: new_count = %d, want 1", run.NewCount)
	}
	evs := h.events("src")
	if len(evs) != 4 || !strings.Contains(h.text(evs[3].Payload), `"updated_at":"2"`) || evs[3].DedupKey != "b:2" {
		t.Fatalf("events: %+v", evs)
	}
	st, err := h.st.GetSensorState(t.Context(), "src")
	if err != nil || st.LastSuccessAt == nil {
		t.Fatalf("state %+v %v", st, err)
	}
}

// Scenario 3: a baseline first poll marks everything seen without firing; a new
// item afterwards fires exactly once. The sensor shells out to a fake gh on PATH.
func TestScenario3BaselineWithFakeGH(t *testing.T) {
	bin := t.TempDir()
	data := filepath.Join(t.TempDir(), "notifications.jsonl")
	if err := os.WriteFile(data, []byte(`{"id":"1","updated_at":"t1"}`+"\n"+`{"id":"2","updated_at":"t1"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gh := "#!/bin/sh\n[ \"$1\" = api ] && [ \"$2\" = notifications ] || { echo \"fake gh: unexpected args $*\" >&2; exit 2; }\ncat " + data + "\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o700); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	h := newHarness(t, withPath(bin+string(os.PathListSeparator)+os.Getenv("PATH")))
	h.sensor("gh-notes", "id: gh-notes\ninterval: 1h\nlang: bash\nscript: gh api notifications --paginate -q '.[]'\nkey: \"{{.id}}:{{.updated_at}}\"\nfirst_run: baseline\ntimeout: 10s\n")
	h.flow("f", flowYAML("f", "gh-notes"))
	h.start()

	h.eventually("baseline poll", func() bool {
		page, _ := h.st.ListSensorRuns(t.Context(), store.SensorRunFilter{SensorID: "gh-notes"})
		return len(page.Items) == 1 && page.Items[0].Status != store.SensorRunning
	})
	page, _ := h.st.ListSensorRuns(t.Context(), store.SensorRunFilter{SensorID: "gh-notes"})
	first := page.Items[0]
	if !first.Baseline || first.ItemCount != 2 || first.NewCount != 0 || first.Status != store.SensorSucceeded {
		t.Fatalf("baseline run: %+v", first)
	}
	if n := len(h.events("gh-notes")); n != 0 {
		t.Fatalf("baseline created %d events", n)
	}
	if fr, _ := h.st.ListFlowRuns(t.Context(), store.FlowRunFilter{}); len(fr.Items) != 0 {
		t.Fatalf("baseline fanned out %d flow runs", len(fr.Items))
	}

	f, err := os.OpenFile(data, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":"3","updated_at":"t2"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	run, err := h.eng.PollSensor(t.Context(), "gh-notes")
	if err != nil {
		t.Fatal(err)
	}
	if run.Baseline || run.NewCount != 1 || len(h.events("gh-notes")) != 1 {
		t.Fatalf("poll after baseline: %+v events=%d", run, len(h.events("gh-notes")))
	}
	if fr, _ := h.st.ListFlowRuns(t.Context(), store.FlowRunFilter{}); len(fr.Items) != 1 {
		t.Fatalf("flow runs: %d, want 1", len(fr.Items))
	}
}

// A failing poll is recorded with its error and does not advance
// HAL_LAST_SUCCESS_AT; a sensor sees the last successful time on the next poll.
func TestFailedPollDoesNotAdvanceLastSuccess(t *testing.T) {
	h := newHarness(t)
	marker := filepath.Join(t.TempDir(), "fail")
	out := filepath.Join(t.TempDir(), "seen-last-success")
	h.sensor("src", "id: src\ninterval: 1h\nlang: bash\nscript: |\n  echo \"${HAL_LAST_SUCCESS_AT:-none}\" >> "+out+"\n  if [ -f "+marker+" ]; then echo boom >&2; exit 3; fi\n  echo '{\"id\":\"x\"}'\nkey: \"{{.id}}\"\nfirst_run: fire\ntimeout: 10s\n")
	h.flow("f", flowYAML("f", "src"))
	h.start()
	state := func() store.SensorState {
		s, err := h.st.GetSensorState(t.Context(), "src")
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	h.eventually("first poll recorded", func() bool { return state().LastSuccessAt != nil })
	ok := state().LastSuccessAt
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	run, err := h.eng.PollSensor(t.Context(), "src")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.SensorFailed || run.ExitCode == nil || *run.ExitCode != 3 || !strings.Contains(run.Error, "exit status 3") || !strings.Contains(h.text(run.Stderr), "boom") {
		t.Fatalf("failed run: %+v", run)
	}
	st := state()
	if !st.LastSuccessAt.Equal(*ok) || st.ConsecutiveFailures != 1 || st.LastError == "" {
		t.Fatalf("state after failure: %+v (last success was %v)", st, ok)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.PollSensor(t.Context(), "src"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	lines := strings.Fields(string(b))
	if len(lines) != 3 || lines[0] != "none" || lines[1] != ok.UTC().Format("2006-01-02T15:04:05Z") || lines[2] != lines[1] {
		t.Fatalf("HAL_LAST_SUCCESS_AT seen by polls: %v (first success %v)", lines, ok)
	}
}

// Injected events go through the same key template, dedup and fan-out; a
// duplicate returns the stored event and creates nothing.
func TestInjectEventDedupAndFanOut(t *testing.T) {
	h := newHarness(t)
	h.idleSensor()
	h.flow("one", flowYAML("one", "src"))
	h.flow("two", flowYAML("two", "src"))
	h.flow("off", strings.Replace(flowYAML("off", "src"), "enabled: true", "enabled: false", 1))
	h.start()

	res := h.inject(`{"id":"i1"}`)
	if res.Duplicate || res.Event == nil || res.Event.DedupKey != "i1" || res.DedupKey != "i1" || len(res.FlowRunIDs) != 2 {
		t.Fatalf("first inject: %+v", res)
	}
	if res.Event.SensorRunID != "" {
		t.Errorf("injected event links sensor run %q", res.Event.SensorRunID)
	}
	var flows []string
	for _, id := range res.FlowRunIDs {
		fr := h.waitRun(id, store.FlowRunSucceeded)
		flows = append(flows, fr.FlowID)
		if fr.EventID != res.Event.ID || fr.Kind != store.KindLive {
			t.Errorf("flow run %+v", fr)
		}
	}
	if strings.Join(flows, ",") != "one,two" {
		t.Errorf("fan-out flows = %v (disabled flows must not run)", flows)
	}

	dup := h.inject(`{"id":"i1"}`)
	if !dup.Duplicate || dup.Event == nil || dup.Event.ID != res.Event.ID || len(dup.FlowRunIDs) != 0 {
		t.Fatalf("duplicate inject: %+v", dup)
	}
	runs, err := h.st.ListSensorRuns(t.Context(), store.SensorRunFilter{SensorID: "src"})
	injected := 0
	for _, r := range runs.Items {
		if r.Origin == store.OriginInject {
			injected++
		}
	}
	if err != nil || injected != 2 {
		t.Fatalf("inject sensor runs: %d %v", injected, err)
	}

	if _, err := h.eng.InjectEvent(t.Context(), "src", []byte(`{"nokey":1}`)); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("item the key template cannot render: %v, want ErrInvalid", err)
	}
	if _, err := h.eng.InjectEvent(t.Context(), "nope", []byte(`{}`)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown sensor: %v, want ErrNotFound", err)
	}
	if _, err := h.eng.PollSensor(t.Context(), "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("poll unknown sensor: %v", err)
	}
}

// A sensor with no enabled subscriber is not polled on schedule, but a manual
// poll always works.
func TestOnlySubscribedSensorsArePolled(t *testing.T) {
	h := newHarness(t)
	count := filepath.Join(t.TempDir(), "polls")
	h.sensor("lonely", "id: lonely\ninterval: 1h\nlang: bash\nscript: echo x >> "+count+"\nkey: \"{{.id}}\"\ntimeout: 10s\n")
	h.sensor("watched", "id: watched\ninterval: 1h\nlang: bash\nscript: echo y >> "+count+"\nkey: \"{{.id}}\"\ntimeout: 10s\n")
	h.flow("f", flowYAML("f", "watched"))
	h.start()
	h.eventually("scheduled poll of the subscribed sensor", func() bool {
		b, _ := os.ReadFile(count)
		return strings.Contains(string(b), "y")
	})
	if b, _ := os.ReadFile(count); strings.Contains(string(b), "x") {
		t.Fatal("unsubscribed sensor was polled on schedule")
	}
	run, err := h.eng.PollSensor(context.Background(), "lonely")
	if err != nil || run.Status != store.SensorSucceeded {
		t.Fatalf("manual poll: %+v %v", run, err)
	}
	if b, _ := os.ReadFile(count); !strings.Contains(string(b), "x") {
		t.Fatal("manual poll did not run the sensor")
	}
}

// Secrets from the env file are redacted in everything stored: sensor output,
// events, node stdin/stdout/argv; only env key names are recorded.
func TestEnvSecretsNeverStoredUnredacted(t *testing.T) {
	const secret = "s3cr3t-token-value"
	h := newHarness(t)
	h.env("API_TOKEN=" + secret + "\n")
	h.sensor("src", "id: src\ninterval: 1h\nlang: bash\nscript: |\n  echo \"{\\\"id\\\":\\\"1\\\",\\\"leak\\\":\\\"$API_TOKEN\\\"}\"\n  echo \"token $API_TOKEN\" >&2\nkey: \"{{.id}}\"\nfirst_run: fire\ntimeout: 10s\n")
	h.flow("f", `id: f
enabled: true
subscribe: [src]
nodes:
  - id: show
    type: command
    lang: bash
    script: |
      cat
      echo "env has $API_TOKEN" >&2
  - {id: note, type: inbox, title: "leak {{.input.leak}}", summary_md: "x"}
edges:
  - {from: show, to: note}
`)
	h.start()
	h.eventually("event from the first poll", func() bool { return len(h.events("src")) == 1 })
	runs, _ := h.st.ListSensorRuns(t.Context(), store.SensorRunFilter{SensorID: "src"})
	sr, err := h.st.GetSensorRun(t.Context(), runs.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	ev := h.events("src")[0]
	var fr store.FlowRun
	h.eventually("flow run", func() bool {
		page, _ := h.st.ListFlowRuns(t.Context(), store.FlowRunFilter{EventID: ev.ID})
		if len(page.Items) == 1 && page.Items[0].Status == store.FlowRunSucceeded {
			fr = page.Items[0]
			return true
		}
		return false
	})
	show := h.node(fr.ID, "show")
	for name, text := range map[string]string{
		"sensor stdout": h.text(sr.Stdout), "sensor stderr": h.text(sr.Stderr), "event payload": h.text(ev.Payload),
		"node stdin": h.text(show.Stdin), "node stdout": h.text(show.Stdout), "node stderr": h.text(show.Stderr),
		"inbox title": h.inboxItems()[0].Title, "argv": strings.Join(show.Argv, " "),
	} {
		if strings.Contains(text, secret) {
			t.Errorf("%s contains the secret: %q", name, text)
		}
	}
	if !strings.Contains(h.text(sr.Stdout), "«API_TOKEN»") || !strings.Contains(h.text(show.Stderr), "env has «API_TOKEN»") {
		t.Errorf("redaction markers missing: %q / %q", h.text(sr.Stdout), h.text(show.Stderr))
	}
	found := false
	for _, k := range show.EnvKeys {
		found = found || k == "API_TOKEN"
	}
	if !found {
		t.Errorf("env keys %v lack API_TOKEN", show.EnvKeys)
	}
}

// Only one poll of a sensor runs at a time: a manual poll during a running poll
// is rejected with ErrInvalidState.
func TestPollWhileRunningIsRejected(t *testing.T) {
	h := newHarness(t)
	gate := filepath.Join(t.TempDir(), "gate")
	started := filepath.Join(t.TempDir(), "started")
	h.sensor("slow", "id: slow\ninterval: 1h\nlang: bash\nscript: |\n  touch "+started+"\n  while [ ! -f "+gate+" ]; do sleep 0.02; done\nkey: \"{{.id}}\"\ntimeout: 20s\n")
	h.start()
	var wg sync.WaitGroup
	var first *store.SensorRun
	wg.Go(func() { first, _ = h.eng.PollSensor(context.Background(), "slow") })
	h.eventually("the poll to start", func() bool { _, err := os.Stat(started); return err == nil })
	if _, err := h.eng.PollSensor(t.Context(), "slow"); !errors.Is(err, core.ErrInvalidState) {
		t.Errorf("second poll: %v, want ErrInvalidState", err)
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if first == nil || first.Status != store.SensorSucceeded {
		t.Fatalf("first poll: %+v", first)
	}
}

// A poll whose output is not JSON fails, creates nothing, and is shown on the sensor.
func TestPollWithInvalidOutputFails(t *testing.T) {
	h := newHarness(t)
	h.sensor("bad", "id: bad\ninterval: 1h\nlang: bash\nscript: echo 'this is not json'\nkey: \"{{.id}}\"\ntimeout: 10s\n")
	h.start()
	run, err := h.eng.PollSensor(t.Context(), "bad")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.SensorFailed || !strings.Contains(run.Error, "JSON") || !strings.Contains(h.text(run.Stdout), "not json") {
		t.Fatalf("run: %+v", run)
	}
	st, _ := h.st.GetSensorState(t.Context(), "bad")
	if st.LastSuccessAt != nil || st.ConsecutiveFailures < 1 {
		t.Errorf("state: %+v", st)
	}
}

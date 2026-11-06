package e2e

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// PollResult is the poll endpoint's response: the sensor run plus what it started.
type PollResult struct {
	SensorRun
	NewEvents   int      `json:"new_events"`
	RerunEvents int      `json:"rerun_events"`
	FlowRunIDs  []string `json:"flow_run_ids"`
}

type flowSensorResult struct {
	SensorID    string     `json:"sensor_id"`
	Run         *SensorRun `json:"run"`
	NewEvents   int        `json:"new_events"`
	RerunEvents int        `json:"rerun_events"`
	FlowRunIDs  []string   `json:"flow_run_ids"`
	Error       *string    `json:"error"`
}

type runFlowResult struct {
	FlowID     string             `json:"flow_id"`
	Sensors    []flowSensorResult `json:"sensors"`
	FlowRunIDs []string           `json:"flow_run_ids"`
}

type resetResult struct {
	SensorID    string      `json:"sensor_id"`
	ResetAt     string      `json:"reset_at"`
	ResetBy     string      `json:"reset_by"`
	KeysCleared int         `json:"keys_cleared"`
	Run         *PollResult `json:"run"`
}

type sensorStatus struct {
	Status struct {
		State      string  `json:"state"`
		Manual     bool    `json:"manual"`
		Paused     bool    `json:"paused"`
		NextPollAt *string `json:"next_poll_at"`
		ResetAt    *string `json:"reset_at"`
		ResetBy    *string `json:"reset_by"`
	} `json:"status"`
}

// PollWith runs the sensor now with the given poll request body.
func (d *Daemon) PollWith(sensor string, body any) PollResult {
	d.t.Helper()
	var res PollResult
	d.Post("/api/sensors/"+sensor+"/poll", body).Want(202).JSON(&res)
	return res
}

// SensorStatus returns a sensor's status block.
func (d *Daemon) SensorStatus(sensor string) sensorStatus {
	d.t.Helper()
	var s sensorStatus
	d.Get("/api/sensors/" + sensor).Want(200).JSON(&s)
	return s
}

// FlowRuns lists a flow's runs of the kinds shown by default (live, replay), newest first.
func (d *Daemon) FlowRuns(flow string) []FlowRun {
	d.t.Helper()
	var l struct {
		Items []FlowRun `json:"items"`
	}
	d.Get("/api/flow-runs?limit=200&flow_id=" + flow).Want(200).JSON(&l)
	return l.Items
}

func (d *Daemon) waitAll(ids []string) []FlowRun {
	d.t.Helper()
	out := make([]FlowRun, 0, len(ids))
	for _, id := range ids {
		out = append(out, d.WaitRun(id, "succeeded").FlowRun)
	}
	return out
}

const abcItems = `{"id":"a"}` + "\n" + `{"id":"b"}` + "\n" + `{"id":"c"}` + "\n"

func inboxFlow(id string, enabled bool, subscribe ...string) string {
	on := "false"
	if enabled {
		on = "true"
	}
	return "id: " + id + "\nenabled: " + on + "\nsubscribe: [" + strings.Join(subscribe, ", ") + "]\nmax_concurrent: 10\nnodes:\n  - {id: note, type: inbox, title: \"" + id + " {{.input.id}}\"}\n"
}

func manualSensor(d *Daemon, id, firstRun, itemsFile string) {
	d.Sensor(id, "id: "+id+"\nlang: bash\nscript: cat "+shq(d.Path(itemsFile))+"\nkey: \"{{.id}}\"\nfirst_run: "+firstRun+"\ntimeout: 10s\n")
}

// A re-run replays already-seen items into each subscribed flow without making
// events; without the option nothing runs for them; `flows` limits the fan-out.
func TestRunNowRerunSeenReplaysSeenItems(t *testing.T) {
	d := newDaemon(t)
	d.Write("items.jsonl", abcItems)
	manualSensor(d, "ticker", "fire", "items.jsonl")
	d.Flow("one", inboxFlow("one", true, "ticker"))
	d.Flow("two", inboxFlow("two", true, "ticker"))
	d.Start()

	first := d.PollWith("ticker", nil)
	if first.NewEvents != 3 || first.RerunEvents != 0 || len(first.FlowRunIDs) != 6 || first.Trigger != "manual" {
		t.Fatalf("first run now: %+v", first)
	}
	d.waitAll(first.FlowRunIDs)
	if n := len(d.Events("ticker")); n != 3 {
		t.Fatalf("events: %d, want 3", n)
	}

	plain := d.PollWith("ticker", map[string]any{})
	if plain.NewEvents != 0 || plain.RerunEvents != 0 || len(plain.FlowRunIDs) != 0 {
		t.Fatalf("a poll of seen items without rerun_seen started something: %+v", plain)
	}
	if n := len(d.FlowRuns("one")) + len(d.FlowRuns("two")); n != 6 {
		t.Fatalf("flow runs after a plain poll: %d, want 6", n)
	}

	rerun := d.PollWith("ticker", map[string]any{"rerun_seen": true})
	if rerun.NewEvents != 0 || rerun.RerunEvents != 3 || len(rerun.FlowRunIDs) != 6 || *rerun.NewCount != 0 {
		t.Fatalf("rerun: %+v", rerun)
	}
	byFlow := map[string]int{}
	original := map[string]bool{}
	for _, fr := range d.FlowRuns("one") {
		if fr.Kind == "live" {
			original[fr.ID] = true
		}
	}
	for _, fr := range d.waitAll(rerun.FlowRunIDs) {
		byFlow[fr.FlowID]++
		if fr.Kind != "replay" || fr.EventID == "" {
			t.Errorf("rerun run %+v, want kind replay on an existing event", fr)
		}
		if fr.FlowID == "one" && !original[fr.ReplayOf] {
			t.Errorf("replay %s is not linked to the live run of the same event (replay_of %q)", fr.ID, fr.ReplayOf)
		}
	}
	if byFlow["one"] != 3 || byFlow["two"] != 3 {
		t.Errorf("replays per flow: %v, want 3 each", byFlow)
	}
	if n := len(d.Events("ticker")); n != 3 {
		t.Errorf("a re-run created events: %d, want still 3", n)
	}

	only := d.PollWith("ticker", map[string]any{"rerun_seen": true, "flows": []string{"two"}})
	if only.RerunEvents != 3 || len(only.FlowRunIDs) != 3 {
		t.Fatalf("flows filter: %+v", only)
	}
	for _, fr := range d.waitAll(only.FlowRunIDs) {
		if fr.FlowID != "two" {
			t.Errorf("run of flow %q although flows was [two]", fr.FlowID)
		}
	}

	// New items fan out only to the listed flow.
	d.Write("items.jsonl", abcItems+`{"id":"d"}`+"\n")
	fresh := d.PollWith("ticker", map[string]any{"flows": []string{"one"}})
	if fresh.NewEvents != 1 || len(fresh.FlowRunIDs) != 1 {
		t.Fatalf("new item with flows [one]: %+v", fresh)
	}
	if run := d.waitAll(fresh.FlowRunIDs)[0]; run.FlowID != "one" || run.Kind != "live" {
		t.Errorf("run = %+v, want the live run of flow one", run)
	}
	for _, fr := range d.FlowRuns("two") {
		if strings.Contains(string(d.Events("ticker")[0].Payload), `"d"`) && fr.EventID == d.Events("ticker")[0].ID {
			t.Errorf("flow two ran the event only meant for flow one")
		}
	}

	d.Post("/api/sensors/ticker/poll", map[string]any{"flows": []string{"ghost"}}).Want(404)
	d.Flow("elsewhere", inboxFlow("elsewhere", true, "other"))
	d.IdleSensor("other")
	time.Sleep(300 * time.Millisecond) // let the file watcher load the new files
	d.Post("/api/sensors/ticker/poll", map[string]any{"flows": []string{"elsewhere"}}).Want(422)
	d.Post("/api/sensors/ticker/poll", map[string]any{"rerun": true}).Want(400)
}

// A manual first poll fires even with first_run: baseline; the scheduler's
// first poll baselines.
func TestRunNowManualFirstPollIgnoresBaselineScheduledOneDoesNot(t *testing.T) {
	d := newDaemon(t)
	d.Write("items.jsonl", abcItems)
	manualSensor(d, "manual-first", "baseline", "items.jsonl")
	d.Sensor("scheduled-first", "id: scheduled-first\ninterval: 1h\nlang: bash\nscript: cat "+shq(d.Path("items.jsonl"))+"\nkey: \"{{.id}}\"\nfirst_run: baseline\ntimeout: 10s\n")
	d.Flow("watch", inboxFlow("watch", true, "manual-first", "scheduled-first"))
	d.Start()

	base := d.firstSensorRun("scheduled-first")
	if !base.Baseline || *base.NewCount != 0 || base.Trigger != "scheduled" {
		t.Fatalf("scheduled first poll: %+v", base)
	}
	if n := len(d.Events("scheduled-first")); n != 0 {
		t.Fatalf("baseline created %d events", n)
	}

	manual := d.PollWith("manual-first", nil)
	if manual.Baseline || manual.NewEvents != 3 || *manual.NewCount != 3 || len(manual.FlowRunIDs) != 3 {
		t.Fatalf("manual first poll with baseline: %+v", manual)
	}
	d.waitAll(manual.FlowRunIDs)
	if n := len(d.Events("manual-first")); n != 3 {
		t.Fatalf("events: %d, want 3", n)
	}
}

// Run now on a flow polls every subscribed sensor and runs only that flow; a
// disabled flow can be run by hand; a flow with nothing to poll is a 422.
func TestRunNowFlowPollsItsSensorsAndRunsOnlyThatFlow(t *testing.T) {
	d := newDaemon(t)
	d.Write("items1.jsonl", abcItems)
	d.Write("items2.jsonl", `{"id":"x"}`+"\n"+`{"id":"y"}`+"\n")
	manualSensor(d, "s1", "fire", "items1.jsonl")
	manualSensor(d, "s2", "fire", "items2.jsonl")
	d.Flow("main", inboxFlow("main", true, "s1", "s2"))
	d.Flow("bystander", inboxFlow("bystander", true, "s1"))
	d.Flow("sleeper", inboxFlow("sleeper", false, "s1"))
	d.Flow("nothing", "id: nothing\nenabled: true\nsubscribe: []\nnodes:\n  - {id: note, type: inbox, title: x}\n")
	d.Start()

	var res runFlowResult
	d.Post("/api/flows/main/run", nil).Want(202).JSON(&res)
	if res.FlowID != "main" || len(res.Sensors) != 2 || len(res.FlowRunIDs) != 5 {
		t.Fatalf("run flow: %+v", res)
	}
	got := map[string]int{}
	for _, s := range res.Sensors {
		if s.Error != nil || s.Run == nil || s.Run.Trigger != "manual" || s.Run.Status != "succeeded" {
			t.Errorf("sensor result %+v", s)
		}
		got[s.SensorID] = s.NewEvents
	}
	if got["s1"] != 3 || got["s2"] != 2 {
		t.Errorf("new events per sensor: %v", got)
	}
	for _, fr := range d.waitAll(res.FlowRunIDs) {
		if fr.FlowID != "main" || fr.Kind != "live" {
			t.Errorf("run %+v, want live runs of main only", fr)
		}
	}
	if n := len(d.FlowRuns("bystander")) + len(d.FlowRuns("sleeper")); n != 0 {
		t.Errorf("other flows ran %d times", n)
	}

	var again runFlowResult
	d.Post("/api/flows/main/run", map[string]any{"rerun_seen": true}).Want(202).JSON(&again)
	if len(again.FlowRunIDs) != 5 {
		t.Fatalf("rerun via flow: %+v", again)
	}
	for _, fr := range d.waitAll(again.FlowRunIDs) {
		if fr.FlowID != "main" || fr.Kind != "replay" {
			t.Errorf("run %+v, want replays of main only", fr)
		}
	}
	var none runFlowResult
	d.Post("/api/flows/main/run", nil).Want(202).JSON(&none)
	if len(none.FlowRunIDs) != 0 {
		t.Errorf("a flow run of seen items without rerun_seen started %v", none.FlowRunIDs)
	}

	// The disabled flow runs once on request, as the only target.
	d.Post("/api/sensors/s1/reset", map[string]any{}).Want(200)
	var slept runFlowResult
	d.Post("/api/flows/sleeper/run", nil).Want(202).JSON(&slept)
	if len(slept.FlowRunIDs) != 3 || slept.Sensors[0].NewEvents != 3 {
		t.Fatalf("disabled flow run: %+v", slept)
	}
	for _, fr := range d.waitAll(slept.FlowRunIDs) {
		if fr.FlowID != "sleeper" {
			t.Errorf("run of %q, want only sleeper", fr.FlowID)
		}
	}

	d.Post("/api/flows/nothing/run", nil).Want(422)
	d.Post("/api/flows/ghost/run", nil).Want(404)
	d.Post("/api/flows/main/run", map[string]any{"bogus": true}).Want(400)
}

// A sensor with neither interval nor cron is valid, never scheduled, and runs
// on request.
func TestManualOnlySensorIsNeverScheduled(t *testing.T) {
	d := newDaemon(t)
	d.Write("items.jsonl", abcItems)
	manualSensor(d, "hand", "fire", "items.jsonl")
	d.Flow("watch", inboxFlow("watch", true, "hand"))
	d.Start()

	time.Sleep(700 * time.Millisecond)
	var runs sensorRunList
	d.Get("/api/sensors/hand/runs").Want(200).JSON(&runs)
	if len(runs.Items) != 0 {
		t.Fatalf("a manual-only sensor was polled by the scheduler: %+v", runs.Items)
	}
	st := d.SensorStatus("hand")
	if !st.Status.Manual || st.Status.NextPollAt != nil || st.Status.State != "idle" {
		t.Fatalf("status: %+v, want manual with no next poll", st.Status)
	}
	var v struct {
		Valid bool `json:"valid"`
	}
	d.Post("/api/sensors/validate", map[string]any{"yaml": "id: hand\nlang: bash\nscript: echo\nkey: k\n"}).Want(200).JSON(&v)
	if !v.Valid {
		t.Errorf("a sensor without a schedule is invalid")
	}

	res := d.PollWith("hand", nil)
	if res.Trigger != "manual" || res.NewEvents != 3 {
		t.Fatalf("run now: %+v", res)
	}
	d.waitAll(res.FlowRunIDs)
}

// The kill switch rejects Run now, flow runs and a reset that runs; a plain
// reset still works.
func TestRunNowIsRejectedByTheKillSwitch(t *testing.T) {
	d := newDaemon(t)
	d.Write("items.jsonl", abcItems)
	manualSensor(d, "hand", "fire", "items.jsonl")
	d.Flow("watch", inboxFlow("watch", true, "hand"))
	d.Start()
	d.PollWith("hand", nil)

	d.Post("/api/killswitch/pause", map[string]any{"reason": "e2e"}).Want(200)
	if r := d.Post("/api/sensors/hand/poll", nil).Want(423); r.Problem() != "paused" {
		t.Errorf("poll problem = %q", r.Problem())
	}
	d.Post("/api/sensors/hand/poll", map[string]any{"rerun_seen": true}).Want(423)
	d.Post("/api/flows/watch/run", nil).Want(423)
	d.Post("/api/sensors/hand/reset", map[string]any{"run": true}).Want(423)
	if n := len(d.Events("hand")); n != 3 {
		t.Fatalf("events: %d", n)
	}
	// The rejected reset changed nothing: the keys are still known.
	d.Post("/api/killswitch/resume", nil).Want(200)
	if res := d.PollWith("hand", nil); res.NewEvents != 0 {
		t.Fatalf("a rejected reset forgot the dedup keys: %+v", res)
	}
	d.Post("/api/killswitch/pause", map[string]any{}).Want(200)
	d.Post("/api/sensors/hand/reset", map[string]any{}).Want(200)
	d.Post("/api/killswitch/resume", nil).Want(200)
}

// Reset forgets the dedup keys and last_success_at; history stays; a reset
// that runs makes every current item new again.
func TestResetSensorThenRunMakesEveryItemNew(t *testing.T) {
	d := newDaemon(t)
	d.Write("items.sh", "for id in a b c; do echo \"{\\\"id\\\":\\\"$id\\\",\\\"last\\\":\\\"${HAL_LAST_SUCCESS_AT:-unset}\\\"}\"; done\n")
	d.Sensor("hand", "id: hand\nlang: bash\nscript: sh "+shq(d.Path("items.sh"))+"\nkey: \"{{.id}}\"\nfirst_run: baseline\ntimeout: 10s\n")
	d.Flow("watch", inboxFlow("watch", true, "hand"))
	d.Start()

	first := d.PollWith("hand", nil)
	if first.NewEvents != 3 || !strings.Contains(first.Stdout.Text, `"last":"unset"`) {
		t.Fatalf("first run: %+v", first)
	}
	d.waitAll(first.FlowRunIDs)
	second := d.PollWith("hand", nil)
	if second.NewEvents != 0 || strings.Contains(second.Stdout.Text, `"last":"unset"`) {
		t.Fatalf("second run should know the last success and find nothing new: %+v", second)
	}
	oldEvents := map[string]bool{}
	for _, e := range d.Events("hand") {
		oldEvents[e.ID] = true
	}
	inboxBefore := len(d.Inbox())
	if st := d.SensorStatus("hand"); st.Status.ResetAt != nil || st.Status.ResetBy != nil {
		t.Fatalf("a new sensor reports a reset: %+v", st.Status)
	}

	var rs resetResult
	d.Post("/api/sensors/hand/reset", map[string]any{"run": true}).Want(200).JSON(&rs)
	if rs.KeysCleared != 3 || rs.ResetBy == "" || rs.ResetAt == "" || rs.Run == nil {
		t.Fatalf("reset: %+v", rs)
	}
	if rs.Run.Baseline || rs.Run.NewEvents != 3 || len(rs.Run.FlowRunIDs) != 3 || !strings.Contains(rs.Run.Stdout.Text, `"last":"unset"`) {
		t.Fatalf("run after reset: %+v (HAL_LAST_SUCCESS_AT must be unset, no baseline, all items new)", rs.Run)
	}
	d.waitAll(rs.Run.FlowRunIDs)
	st := d.SensorStatus("hand")
	if st.Status.ResetAt == nil || st.Status.ResetBy == nil || *st.Status.ResetBy != rs.ResetBy {
		t.Errorf("status after reset: %+v", st.Status)
	}

	// History stays: the old events are still there next to the new ones.
	evs := d.Events("hand")
	if len(evs) != 6 {
		t.Fatalf("events: %d, want 3 old + 3 new", len(evs))
	}
	var fresh []string
	for _, e := range evs {
		if oldEvents[e.ID] {
			delete(oldEvents, e.ID)
		} else {
			fresh = append(fresh, e.ID)
		}
	}
	if len(oldEvents) != 0 || len(fresh) != 3 {
		t.Fatalf("old events lost (%v) or new ones missing (%v)", oldEvents, fresh)
	}
	if n := len(d.Inbox()); n != inboxBefore+3 {
		t.Errorf("inbox items: %d, want %d (history kept, three new)", n, inboxBefore+3)
	}

	// A re-run after a reset replays the NEW events, not the old ones.
	rerun := d.PollWith("hand", map[string]any{"rerun_seen": true})
	if rerun.RerunEvents != 3 || rerun.NewEvents != 0 {
		t.Fatalf("rerun: %+v", rerun)
	}
	isFresh := map[string]bool{}
	for _, id := range fresh {
		isFresh[id] = true
	}
	for _, fr := range d.waitAll(rerun.FlowRunIDs) {
		if !isFresh[fr.EventID] {
			t.Errorf("replay %s runs old event %s, want the event stored after the reset", fr.ID, fr.EventID)
		}
	}
}

// Without run, the next scheduled poll behaves like a brand-new sensor:
// first_run baseline marks everything seen, first_run fire emits everything.
// A paused sensor can be reset and stays paused.
func TestResetSensorWithoutRunAppliesFirstRunToTheNextScheduledPoll(t *testing.T) {
	for _, firstRun := range []string{"baseline", "fire"} {
		t.Run(firstRun, func(t *testing.T) {
			d := newDaemon(t)
			d.Write("items.jsonl", abcItems)
			d.Sensor("tick", "id: tick\ninterval: 1s\nlang: bash\nscript: cat "+shq(d.Path("items.jsonl"))+"\nkey: \"{{.id}}\"\nfirst_run: "+firstRun+"\ntimeout: 10s\n")
			d.Flow("watch", inboxFlow("watch", true, "tick"))
			d.Start()

			first := d.firstSensorRun("tick")
			wantFirst := 3
			if firstRun == "baseline" {
				wantFirst = 0
			}
			if first.Baseline != (firstRun == "baseline") || len(d.Events("tick")) != wantFirst {
				t.Fatalf("first poll: %+v, %d events", first, len(d.Events("tick")))
			}

			d.Post("/api/sensors/tick/pause", nil).Want(200)
			// Let a poll that is in flight finish, then reset the paused sensor.
			d.Eventually("no poll running", func() bool {
				var l sensorRunList
				d.Get("/api/sensors/tick/runs?limit=1").Want(200).JSON(&l)
				return len(l.Items) == 0 || l.Items[0].Status != "running"
			})
			var rs resetResult
			d.Post("/api/sensors/tick/reset", nil).Want(200).JSON(&rs)
			if rs.Run != nil || rs.KeysCleared != 3 {
				t.Fatalf("reset: %+v, want the 3 keys (baselined or fired) cleared and no run", rs)
			}
			if st := d.SensorStatus("tick"); !st.Status.Paused || st.Status.ResetAt == nil {
				t.Fatalf("reset must keep the pause and record itself: %+v", st.Status)
			}
			before := len(d.Events("tick"))

			d.Write("items.jsonl", abcItems+`{"id":"d"}`+"\n")
			d.Post("/api/sensors/tick/resume", nil).Want(200)
			var next SensorRun
			d.Eventually("the scheduled poll after the reset", func() bool {
				var l sensorRunList
				d.Get("/api/sensors/tick/runs?limit=1").Want(200).JSON(&l)
				if len(l.Items) == 0 || l.Items[0].Status == "running" || l.Items[0].Trigger != "scheduled" || l.Items[0].ID == first.ID {
					return false
				}
				next = l.Items[0]
				return true
			})
			if firstRun == "baseline" {
				if !next.Baseline || len(d.Events("tick")) != before {
					t.Fatalf("poll after reset: %+v, %d events; want a baseline poll that fires nothing", next, len(d.Events("tick")))
				}
			} else {
				if next.Baseline || len(d.Events("tick")) != before+4 {
					t.Fatalf("poll after reset: %+v, %d events; want every item emitted again", next, len(d.Events("tick")))
				}
			}
		})
	}
}

// A reset is refused while a poll of the sensor is running, as is a second poll.
func TestResetSensorIsRefusedWhileAPollRuns(t *testing.T) {
	d := newDaemon(t)
	gate := d.Path("gate")
	d.Write("slow.sh", "while [ ! -f "+shq(gate)+" ]; do sleep 0.05; done\necho '{\"id\":\"a\"}'\n")
	d.Sensor("slow", "id: slow\nlang: bash\nscript: sh "+shq(d.Path("slow.sh"))+"\nkey: \"{{.id}}\"\nfirst_run: fire\ntimeout: 30s\n")
	d.Flow("watch", inboxFlow("watch", true, "slow"))
	d.Start()

	var wg sync.WaitGroup
	var res PollResult
	wg.Go(func() { d.Post("/api/sensors/slow/poll", nil).Want(202).JSON(&res) })
	d.Eventually("the poll to start", func() bool {
		var l sensorRunList
		d.Get("/api/sensors/slow/runs").Want(200).JSON(&l)
		return len(l.Items) == 1 && l.Items[0].Status == "running"
	})
	if r := d.Post("/api/sensors/slow/reset", nil).Want(409); r.Problem() != "invalid-state" {
		t.Errorf("reset problem = %q", r.Problem())
	}
	d.Post("/api/sensors/slow/reset", map[string]any{"run": true}).Want(409)
	d.Post("/api/sensors/slow/poll", nil).Want(409)
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if res.NewEvents != 1 {
		t.Fatalf("the running poll: %+v", res)
	}
	d.Post("/api/sensors/slow/reset", nil).Want(200)
	d.Post("/api/sensors/ghost/reset", nil).Want(404)
	d.Post("/api/sensors/slow/reset", map[string]any{"rerun_flows": []string{"watch"}}).Want(422)
}

// The CLI mirrors the endpoints and --wait prints the final status of the runs.
func TestRunNowCLI(t *testing.T) {
	d := newDaemon(t)
	d.Write("items.jsonl", abcItems)
	manualSensor(d, "ticker", "fire", "items.jsonl")
	d.Flow("one", inboxFlow("one", true, "ticker"))
	d.Flow("two", inboxFlow("two", true, "ticker"))
	d.Start()

	decode := func(out string) (m struct {
		NewEvents   int      `json:"new_events"`
		RerunEvents int      `json:"rerun_events"`
		FlowRunIDs  []string `json:"flow_run_ids"`
		FlowRuns    []struct {
			ID     string `json:"id"`
			FlowID string `json:"flow_id"`
			Kind   string `json:"kind"`
			Status string `json:"status"`
		} `json:"flow_runs"`
	}) {
		t.Helper()
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, out)
		}
		return m
	}
	out, errOut, code := d.CLI("", "sensors", "poll", "ticker", "--wait")
	if code != 0 {
		t.Fatalf("sensors poll: %d %s", code, errOut)
	}
	first := decode(out)
	if first.NewEvents != 3 || len(first.FlowRunIDs) != 6 || len(first.FlowRuns) != 6 {
		t.Fatalf("sensors poll --wait: %s", out)
	}
	for _, r := range first.FlowRuns {
		if r.Status != "succeeded" || r.Kind != "live" {
			t.Errorf("final status %+v", r)
		}
	}

	out, errOut, code = d.CLI("", "sensors", "poll", "ticker", "--rerun-seen", "--flow", "one", "--wait")
	if code != 0 {
		t.Fatalf("poll --rerun-seen: %d %s", code, errOut)
	}
	re := decode(out)
	if re.RerunEvents != 3 || len(re.FlowRuns) != 3 {
		t.Fatalf("poll --rerun-seen --flow one: %s", out)
	}
	for _, r := range re.FlowRuns {
		if r.FlowID != "one" || r.Kind != "replay" || r.Status != "succeeded" {
			t.Errorf("run %+v", r)
		}
	}

	out, errOut, code = d.CLI("", "flows", "run", "two", "--rerun-seen", "--wait")
	if code != 0 {
		t.Fatalf("flows run: %d %s", code, errOut)
	}
	fl := decode(out)
	if len(fl.FlowRunIDs) != 3 || len(fl.FlowRuns) != 3 {
		t.Fatalf("flows run --rerun-seen --wait: %s", out)
	}
	for _, r := range fl.FlowRuns {
		if r.FlowID != "two" || r.Kind != "replay" || r.Status != "succeeded" {
			t.Errorf("run %+v", r)
		}
	}

	out, errOut, code = d.CLI("", "sensors", "reset", "ticker", "--run", "--yes")
	if code != 0 {
		t.Fatalf("sensors reset: %d %s", code, errOut)
	}
	var rs resetResult
	if err := json.Unmarshal([]byte(out), &rs); err != nil || rs.KeysCleared != 3 || rs.Run == nil || rs.Run.NewEvents != 3 {
		t.Fatalf("sensors reset --run: %v\n%s", err, out)
	}
	if _, _, code := d.CLI("", "sensors", "reset", "ghost", "--yes"); code == 0 {
		t.Error("resetting an unknown sensor succeeded")
	}
}

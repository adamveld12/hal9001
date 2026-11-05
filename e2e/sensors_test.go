package e2e

import (
	"strings"
	"testing"
)

type sensorRunList struct {
	Items []SensorRun `json:"items"`
}

// firstSensorRun waits for the poll the scheduler makes when the daemon starts.
func (d *Daemon) firstSensorRun(sensor string) SensorRun {
	d.t.Helper()
	var l sensorRunList
	d.Eventually("the scheduled first poll of "+sensor, func() bool {
		d.Get("/api/sensors/" + sensor + "/runs").Want(200).JSON(&l)
		return len(l.Items) > 0 && l.Items[len(l.Items)-1].Status != "running"
	})
	return l.Items[len(l.Items)-1]
}

// Scenario 2: a bash sensor emitting 3 items with first_run: fire creates 3
// events; polling the same output again creates 0; changing one item's
// updated_at (part of the dedup key) creates exactly 1.
func TestScenario02SensorDedupByKey(t *testing.T) {
	d := newDaemon(t)
	items := func(u2 string) string {
		return `{"id":"a","updated_at":"1"}` + "\n" + `{"id":"b","updated_at":"` + u2 + `"}` + "\n" + `{"id":"c","updated_at":"1"}` + "\n"
	}
	d.Write("items.jsonl", items("1"))
	d.Sensor("ticker", "id: ticker\ninterval: 1h\nlang: bash\nscript: cat "+shq(d.Path("items.jsonl"))+"\nkey: \"{{.id}}:{{.updated_at}}\"\nfirst_run: fire\ntimeout: 10s\n")
	d.Flow("watch", "id: watch\nenabled: true\nsubscribe: [ticker]\nnodes:\n  - {id: note, type: inbox, title: \"item {{.input.id}}:{{.input.updated_at}}\"}\n")
	d.Start()

	first := d.firstSensorRun("ticker")
	if first.Status != "succeeded" || first.Baseline || first.ItemCount == nil || *first.ItemCount != 3 || first.NewCount == nil || *first.NewCount != 3 {
		t.Fatalf("first poll: %+v", first)
	}
	evs := d.Events("ticker")
	keys := map[string]bool{}
	for _, e := range evs {
		keys[e.DedupKey] = true
		if e.Injected {
			t.Errorf("polled event %s is marked injected", e.ID)
		}
	}
	if len(evs) != 3 || !keys["a:1"] || !keys["b:1"] || !keys["c:1"] {
		t.Fatalf("events after first poll: %v", keys)
	}

	same := d.Poll("ticker")
	if same.Trigger != "manual" || same.Status != "succeeded" || *same.ItemCount != 3 || *same.NewCount != 0 {
		t.Errorf("repeat poll: %+v", same)
	}
	if n := len(d.Events("ticker")); n != 3 {
		t.Errorf("repeat poll created events: %d, want 3 in total", n)
	}

	d.Write("items.jsonl", items("2"))
	changed := d.Poll("ticker")
	if *changed.ItemCount != 3 || *changed.NewCount != 1 {
		t.Errorf("changed poll: %+v", changed)
	}
	evs = d.Events("ticker")
	if len(evs) != 4 || evs[0].DedupKey != "b:2" {
		t.Errorf("events after change (newest first): %+v", evs)
	}
}

// Scenario 3: with first_run: baseline the first poll marks everything seen
// without firing; a notification that appears afterwards creates exactly one
// event. The sensor runs a fake `gh` from PATH, as the starter sensor does.
func TestScenario03BaselineThenOneNewNotification(t *testing.T) {
	d := newDaemon(t)
	fixture := d.Path("notifications.jsonl")
	n := func(id, upd string) string {
		return `{"id":"` + id + `","reason":"review_requested","updated_at":"` + upd + `","subject":{"title":"PR ` + id + `"}}` + "\n"
	}
	d.Write("notifications.jsonl", n("1", "t1")+n("2", "t1"))
	d.Script("gh", `[ "$1 $2" = "api notifications" ] || { echo "fake gh: unexpected args: $*" >&2; exit 2; }
cat `+shq(fixture))
	d.Sensor("github-notifications", `id: github-notifications
interval: 1h
lang: bash
script: |
  gh api notifications --paginate -q '.[]'
key: "{{.id}}:{{.updated_at}}"
first_run: baseline
timeout: 10s
`)
	d.Flow("watch", "id: watch\nenabled: true\nsubscribe: [github-notifications]\nnodes:\n  - {id: note, type: inbox, title: \"{{.input.subject.title}}\"}\n")
	d.Start()

	first := d.firstSensorRun("github-notifications")
	if first.Status != "succeeded" || !first.Baseline || *first.ItemCount != 2 || *first.NewCount != 0 {
		t.Fatalf("baseline poll: %+v", first)
	}
	if n := len(d.Events("github-notifications")); n != 0 {
		t.Fatalf("baseline poll created %d events, want none", n)
	}

	same := d.Poll("github-notifications")
	if same.Baseline || *same.NewCount != 0 {
		t.Errorf("poll with nothing new: %+v", same)
	}

	d.Write("notifications.jsonl", n("1", "t1")+n("2", "t1")+n("3", "t2"))
	next := d.Poll("github-notifications")
	if next.Baseline || *next.ItemCount != 3 || *next.NewCount != 1 {
		t.Fatalf("poll after a new notification: %+v", next)
	}
	evs := d.Events("github-notifications")
	if len(evs) != 1 || evs[0].DedupKey != "3:t2" || !strings.Contains(string(evs[0].Payload), "PR 3") {
		t.Errorf("events: %+v", evs)
	}
}

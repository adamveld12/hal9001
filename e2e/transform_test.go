package e2e

import (
	"os/exec"
	"strings"
	"testing"
)

// A transform node runs through the engine like a jq command node: a filter step
// drops with a plain-language reason, a route step picks the output port, and
// the trace records the compiled jq as what ran.
func TestTransformFilterAndRoute(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("tx", `id: tx
enabled: true
subscribe: [src]
nodes:
  - id: shape
    type: transform
    steps:
      - type: filter
        conditions:
          - {field: severity, op: not_equals, value: noise}
      - type: route
        default_port: normal
        rules:
          - port: urgent
            conditions:
              - {field: severity, op: equals, value: urgent}
  - {id: note-urgent, type: inbox, title: "urgent {{.input.id}}", priority: urgent}
  - {id: note-normal, type: inbox, title: "normal {{.input.id}}", priority: low}
edges:
  - {from: shape, port: urgent, to: note-urgent}
  - {from: shape, port: normal, to: note-normal}
`)
	d.Start()

	u := d.RunOf("src", map[string]any{"id": "1", "severity": "Urgent"})
	shape := d.Node(u, "shape")
	if shape.Decision != "emitted" || len(shape.Ports) != 1 || shape.Ports[0] != "urgent" || !strings.Contains(shape.Reason, "sent to urgent") {
		t.Errorf("urgent: %+v", shape)
	}
	if note := d.Node(u, "note-urgent"); note.ParentNodeRunID != shape.ID || note.Status != "done" {
		t.Errorf("urgent branch:\n%s", describeRun(u))
	}
	// The trace shows the program that ran, and the engine strips _port and _reason before the next node.
	var detail struct {
		Script string `json:"script"`
		Node   struct {
			Type  string           `json:"type"`
			Steps []map[string]any `json:"steps"`
		} `json:"node"`
	}
	d.Get("/api/node-runs/" + shape.ID).Want(200).JSON(&detail)
	if !strings.Contains(detail.Script, "# step 1: Keep only items where severity is not") || !strings.Contains(detail.Script, "_port") ||
		detail.Node.Type != "transform" || len(detail.Node.Steps) != 2 {
		t.Errorf("node run detail: %+v", detail)
	}
	if note := d.Node(u, "note-urgent"); strings.Contains(note.Stdin.Text, "_port") || strings.Contains(note.Stdin.Text, "_reason") {
		t.Errorf("reserved keys reached the next node: %s", note.Stdin.Text)
	}

	n := d.RunOf("src", map[string]any{"id": "2", "severity": "low"})
	if r := d.Node(n, "shape"); len(r.Ports) != 1 || r.Ports[0] != "normal" || d.Node(n, "note-normal").Status != "done" {
		t.Errorf("normal:\n%s", describeRun(n))
	}

	drop := d.WaitRun(d.InjectRun("src", map[string]any{"id": "3", "severity": "noise"}), "succeeded")
	dropped := d.Node(drop, "shape")
	if dropped.Decision != "dropped" || dropped.Reason != `Dropped by step 1: it did not meet the condition: severity is not "noise"` || len(drop.NodeRuns) != 1 {
		t.Errorf("noise:\n%s\nreason %q", describeRun(drop), dropped.Reason)
	}
	if items := d.InboxByTitle(); len(items) != 2 || items["urgent 1"].Priority != "urgent" || items["normal 2"].Priority != "low" {
		t.Errorf("inbox: %+v", items)
	}

	// Run node on sample works with an unsaved draft of a transform node.
	var res struct {
		NodeRun NodeRun `json:"node_run"`
	}
	d.Post("/api/flows/tx/test-node", map[string]any{
		"node_id": "shape",
		"node": map[string]any{"id": "shape", "type": "transform", "steps": []any{
			map[string]any{"type": "set", "field": "seen", "value": map[string]any{"kind": "constant", "value": true}},
			map[string]any{"type": "route", "default_port": "normal", "rules": []any{map[string]any{
				"port": "urgent", "conditions": []any{map[string]any{"field": "id", "op": "equals", "value": "9"}},
			}}},
		}},
		"input": map[string]any{"source": "json", "json": map[string]any{"id": "9"}},
	}).Want(200).JSON(&res)
	if res.NodeRun.Decision != "emitted" || !strings.Contains(res.NodeRun.Stdout.Text, `"seen":true`) || len(res.NodeRun.Ports) != 1 || res.NodeRun.Ports[0] != "urgent" {
		t.Errorf("test-node draft: %+v", res.NodeRun)
	}
}

// split fans one event out into one item per list element; aggregate summarises the list first.
func TestTransformSplitAndAggregate(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	d := newDaemon(t)
	d.IdleSensor("orders")
	d.Flow("fan", `id: fan
enabled: true
subscribe: [orders]
nodes:
  - id: lines
    type: transform
    steps:
      - {type: aggregate, field: items, into: total, op: sum, of: price}
      - {type: split, field: items, as: item, keep_parent: true}
  - {id: note, type: inbox, title: "{{.input.id}} {{.input.item.name}} of {{.input.total}}"}
edges:
  - {from: lines, to: note}
`)
	d.Start()

	run := d.RunOf("orders", map[string]any{"id": "o1", "items": []any{
		map[string]any{"name": "a", "price": 2}, map[string]any{"name": "b", "price": 3.5},
	}})
	lines := d.Node(run, "lines")
	if lines.Decision != "emitted" || lines.EmittedCount != 2 {
		t.Errorf("lines: %+v", lines)
	}
	if items := d.InboxByTitle(); len(items) != 2 || items["o1 a of 5.5"].Title == "" || items["o1 b of 5.5"].Title == "" {
		t.Errorf("inbox: %+v", items)
	}

	empty := d.WaitRun(d.InjectRun("orders", map[string]any{"id": "o2", "items": []any{}}), "succeeded")
	if n := d.Node(empty, "lines"); n.Decision != "dropped" || n.Reason != "Dropped by step 2: items has no items to split" {
		t.Errorf("empty order: %+v", n)
	}
}

package spec

import (
	"reflect"
	"strings"
	"testing"
)

const transformFlow = `id: tf
enabled: true
subscribe: [github-notifications]
nodes:
  - id: clean
    type: transform
    steps:
      - type: filter
        conditions:
          - {field: severity, op: not_equals, value: noise}
      - type: set
        field: label
        value: {kind: function, function: uppercase, field: severity}
      - type: route
        default_port: normal
        rules:
          - port: urgent
            match: any
            conditions:
              - {field: severity, op: equals, value: urgent}
              - {field: 'labels[].name', op: in, value: [p0, p1]}
  - id: note
    type: inbox
    title: "{{.input.title}}"
edges:
  - {from: clean, port: urgent, to: note}
  - {from: clean, port: normal, to: note}
`

func TestTransformNodeParsesAndRoundTrips(t *testing.T) {
	f, ds := CheckFlow("tf", []byte(transformFlow), testRefs())
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	n := f.NodeByID("clean")
	if n.Type != NodeTransform || len(n.Steps) != 3 || n.Timeout != DefaultCommandTimeout {
		t.Fatalf("node: %+v", n)
	}
	if got := n.OutPorts(); !reflect.DeepEqual(got, []string{"out", "urgent", "normal", "error"}) {
		t.Errorf("ports = %v", got)
	}
	if got := f.NodeByID("note").OutPorts(); !reflect.DeepEqual(got, []string{"out", "error"}) {
		t.Errorf("inbox ports = %v", got)
	}

	out, err := MarshalFlow(*f)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, want := range []string{
		"type: transform\n    steps:\n      - type: filter\n        conditions:\n          - {field: severity, op: not_equals, value: noise}",
		"value: {kind: function, function: uppercase, field: severity}",
		"default_port: normal",
		"- {field: 'labels[].name', op: in, value: [p0, p1]}",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("saved file lacks %q:\n%s", want, text)
		}
	}
	for _, bad := range []string{"timeout:", "keep:", "match: all", "order:"} {
		if strings.Contains(text, bad) {
			t.Errorf("default leaked into saved file (%q):\n%s", bad, text)
		}
	}
	g, ds := CheckFlow("tf", out, testRefs())
	if len(ds) != 0 {
		t.Fatalf("saved file does not parse: %v\n%s", ds, text)
	}
	if mustJSON(t, f) != mustJSON(t, g) {
		t.Errorf("round trip changed the flow:\n%s\n%s", mustJSON(t, f), mustJSON(t, g))
	}
	// And the saved YAML is stable.
	again, _ := MarshalFlow(*g)
	if string(again) != text {
		t.Errorf("second save differs:\n%s\n%s", text, again)
	}
}

func TestTransformNodeDiagnostics(t *testing.T) {
	steps := func(s string) string {
		return flowWith("  - id: a\n    type: transform\n    steps:\n"+s+cmd("b"), "  - {from: a, to: b}\n")
	}
	tests := []struct {
		name, yaml, code, path string
	}{
		{"unknown step type", steps("      - {type: shuffle}\n"), CodeInvalidEnum, "nodes[0].steps[0].type"},
		{"bad condition value", steps("      - type: filter\n        conditions:\n          - {field: a, op: gt, value: true}\n"), CodeInvalidValue, "nodes[0].steps[0].conditions[0].value"},
		{"missing value", steps("      - {type: set, field: a, value: {kind: constant, value: 1}}\n      - type: filter\n        conditions:\n          - {field: a, op: equals}\n"), CodeRequired, "nodes[0].steps[1].conditions[0].value"},
		{"unknown step field", steps("      - {type: filter, conditions: [{field: a, op: exists}], colour: red}\n"), CodeUnknownField, "nodes[0].steps[0].colour"},
		{"field of another step type", steps("      - {type: remove, fields: [a], order: asc}\n"), CodeFieldNotAllowed, "nodes[0].steps[0].order"},
		{"duplicate route ports", steps("      - type: route\n        rules:\n          - {port: p, conditions: [{field: a, op: exists}]}\n          - {port: p, conditions: [{field: b, op: exists}]}\n"), CodeDuplicateID, "nodes[0].steps[0].rules[1].port"},
		{"bad regex", steps("      - {type: filter, conditions: [{field: a, op: matches, value: '('}]}\n"), CodeInvalidRegex, "nodes[0].steps[0].conditions[0].value"},
		{"edge from a port the route does not declare", flowWith("  - id: a\n    type: transform\n    steps:\n      - type: route\n        rules: [{port: p, conditions: [{field: a, op: exists}]}]\n"+cmd("b"), "  - {from: a, port: q, to: b}\n"), CodeUndeclaredPort, "edges[0].port"},
		{"command fields on a transform node", flowWith("  - {id: a, type: transform, lang: jq, script: '.'}\n"+cmd("b"), "  - {from: a, to: b}\n"), CodeFieldNotAllowed, "nodes[0].lang"},
		{"steps on a command node", flowWith("  - {id: a, type: command, lang: jq, script: '.', steps: [{type: remove, fields: [a]}]}\n"+cmd("b"), "  - {from: a, to: b}\n"), CodeFieldNotAllowed, "nodes[0].steps"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ds := CheckFlow("t", []byte(tt.yaml), testRefs())
			for _, d := range ds {
				if d.Code == tt.code && d.Path == tt.path && d.Severity == SeverityError && (d.NodeID == "a" || tt.code == CodeUnknownField) {
					return
				}
			}
			t.Errorf("want %s at %s on node a; got %v", tt.code, tt.path, ds)
		})
	}
}

func TestEmptyTransformNodeIsAWarning(t *testing.T) {
	_, ds := CheckFlow("t", []byte(flowWith("  - {id: a, type: transform}\n"+cmd("b"), "  - {from: a, to: b}\n")), testRefs())
	if HasErrors(ds) || len(ds) != 1 || ds[0].Path != "nodes[0].steps" || ds[0].Severity != SeverityWarning {
		t.Errorf("diagnostics: %v", ds)
	}
}

package transform

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func needJQ(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
}

// parseSteps decodes YAML the way a flow file does (strict, snake_case).
func parseSteps(t *testing.T, doc string) []Step {
	t.Helper()
	var steps []Step
	dec := yaml.NewDecoder(strings.NewReader(doc))
	dec.KnownFields(true)
	if err := dec.Decode(&steps); err != nil {
		t.Fatalf("bad test steps: %v\n%s", err, doc)
	}
	return steps
}

func preview(t *testing.T, steps []Step, input string) Result {
	t.Helper()
	needJQ(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	return Preview(ctx, steps, []byte(input), NewRunner("", 10*time.Second))
}

// outs returns the compact payloads of a result, in order.
func outs(r Result) []string {
	var s []string
	for _, o := range r.Outputs {
		var buf bytes.Buffer
		_ = json.Compact(&buf, o.Payload)
		s = append(s, buf.String())
	}
	return s
}

func sameJSON(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("output %q is not JSON: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad expectation %q: %v", want, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// ---- conditions ----

func TestConditions(t *testing.T) {
	item := `{"sev":"Urgent","n":5,"ok":true,"no":false,"txt":"Hello World","empty":"","blank":"  \n","none":null,
	  "tags":["Bug","ui"],"nested":{"x":{"y":"deep"}},"labels":[{"name":"Bug"},{"name":"ui"}],"price":"2026-01-02","line":"a\nb"}`
	cases := []struct {
		name  string
		field string
		op    string
		value string // YAML flow value; empty for none
		cs    bool
		want  bool
	}{
		{"equals ignores case", "sev", "equals", `"urgent"`, false, true},
		{"equals case sensitive", "sev", "equals", `"urgent"`, true, false},
		{"equals exact case sensitive", "sev", "equals", `"Urgent"`, true, true},
		{"equals is whole value", "sev", "equals", `"urg"`, false, false},
		{"equals number", "n", "equals", `5`, false, true},
		{"equals number mismatch", "n", "equals", `6`, false, false},
		{"equals does not coerce text to number", "n", "equals", `"5"`, false, false},
		{"equals bool", "ok", "equals", `true`, false, true},
		{"equals folds non-ASCII", "sev", "equals", `"URGENT"`, false, true},
		{"not equals", "sev", "not_equals", `"noise"`, false, true},
		{"not equals missing field", "absent", "not_equals", `"noise"`, false, true},
		{"not equals same", "sev", "not_equals", `"urgent"`, false, false},
		{"contains text", "txt", "contains", `"LO WOR"`, false, true},
		{"contains case sensitive", "txt", "contains", `"LO WOR"`, true, false},
		{"contains in list", "tags", "contains", `"bug"`, false, true},
		{"contains in list case sensitive", "tags", "contains", `"bug"`, true, false},
		{"contains number text", "txt", "contains", `5`, false, false},
		{"contains on number field is false", "n", "contains", `"5"`, false, false},
		{"not contains", "txt", "not_contains", `"zzz"`, false, true},
		{"not contains hit", "txt", "not_contains", `"world"`, false, false},
		{"starts with", "txt", "starts_with", `"hello"`, false, true},
		{"starts with case sensitive", "txt", "starts_with", `"hello"`, true, false},
		{"starts with middle", "txt", "starts_with", `"World"`, false, false},
		{"ends with", "txt", "ends_with", `"WORLD"`, false, true},
		{"ends with case sensitive", "txt", "ends_with", `"World"`, true, true},
		{"ends with does not use $ line semantics", "line", "ends_with", `"a"`, false, false},
		{"matches", "txt", "matches", `"^hello\\s+w"`, false, true},
		{"matches case sensitive", "txt", "matches", `"^hello"`, true, false},
		{"matches non-text", "n", "matches", `"5"`, false, false},
		{"gt", "n", "gt", `4`, false, true},
		{"gt equal", "n", "gt", `5`, false, false},
		{"gte equal", "n", "gte", `5`, false, true},
		{"lt", "n", "lt", `6`, false, true},
		{"lte", "n", "lte", `4`, false, false},
		{"gt on text field is false", "sev", "gt", `1`, false, false},
		{"lexical date compare", "price", "lt", `"2026-02-01"`, false, true},
		{"exists", "sev", "exists", ``, false, true},
		{"exists null", "none", "exists", ``, false, false},
		{"exists missing", "absent", "exists", ``, false, false},
		{"exists nested", "nested.x.y", "exists", ``, false, true},
		{"not exists", "absent", "not_exists", ``, false, true},
		{"not exists present", "n", "not_exists", ``, false, false},
		{"path through text is missing, not an error", "sev.deeper", "exists", ``, false, false},
		{"is empty text", "empty", "is_empty", ``, false, true},
		{"is empty blank", "blank", "is_empty", ``, false, true},
		{"is empty null", "none", "is_empty", ``, false, true},
		{"is empty missing", "absent", "is_empty", ``, false, true},
		{"is empty non-empty", "sev", "is_empty", ``, false, false},
		{"is not empty", "sev", "is_not_empty", ``, false, true},
		{"is not empty list", "tags", "is_not_empty", ``, false, true},
		{"is true", "ok", "is_true", ``, false, true},
		{"is true false", "no", "is_true", ``, false, false},
		{"is false", "no", "is_false", ``, false, true},
		{"is false missing", "absent", "is_false", ``, false, false},
		{"in", "sev", "in", `["a", "URGENT"]`, false, true},
		{"in case sensitive", "sev", "in", `["a", "URGENT"]`, true, false},
		{"in numbers", "n", "in", `[1, 5]`, false, true},
		{"in miss", "sev", "in", `["a", "b"]`, false, false},
		{"any element equals", "labels[].name", "equals", `"bug"`, false, true},
		{"any element equals miss", "labels[].name", "equals", `"docs"`, false, false},
		{"no element equals", "labels[].name", "not_equals", `"docs"`, false, true},
		{"no element equals hit", "labels[].name", "not_equals", `"bug"`, false, false},
		{"missing list has no element", "absent[].name", "exists", ``, false, false},
		{"no element exists in missing list", "absent[].name", "not_exists", ``, false, true},
		{"elements of a list of text", "tags[]", "starts_with", `"u"`, false, true},
		{"whole item", ".", "exists", ``, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cond := "{field: " + yamlStr(tc.field) + ", op: " + tc.op
			if tc.value != "" {
				cond += ", value: " + tc.value
			}
			if tc.cs {
				cond += ", case_sensitive: true"
			}
			cond += "}"
			steps := parseSteps(t, "- {type: filter, conditions: ["+cond+"]}")
			r := preview(t, steps, item)
			if r.Decision == DecisionError {
				t.Fatalf("error: %s\n%s", r.Error, r.JQ)
			}
			if got := r.Decision == DecisionEmitted; got != tc.want {
				t.Errorf("kept = %v, want %v\n%s", got, tc.want, r.JQ)
			}
		})
	}
}

func yamlStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestFilterModes(t *testing.T) {
	both := `{"a":1,"b":2}`
	cases := []struct {
		name, step string
		input      string
		want       bool
	}{
		{"all met", `{type: filter, conditions: [{field: a, op: equals, value: 1}, {field: b, op: equals, value: 2}]}`, both, true},
		{"all one missed", `{type: filter, conditions: [{field: a, op: equals, value: 1}, {field: b, op: equals, value: 9}]}`, both, false},
		{"any one met", `{type: filter, match: any, conditions: [{field: a, op: equals, value: 9}, {field: b, op: equals, value: 2}]}`, both, true},
		{"any none met", `{type: filter, match: any, conditions: [{field: a, op: equals, value: 9}, {field: b, op: equals, value: 9}]}`, both, false},
		{"drop matching, matched", `{type: filter, keep: false, conditions: [{field: a, op: equals, value: 1}]}`, both, false},
		{"drop matching, other", `{type: filter, keep: false, conditions: [{field: a, op: equals, value: 2}]}`, both, true},
		{"item that is not an object", `{type: filter, conditions: [{field: a, op: exists}]}`, `"just text"`, false},
		{"item is text", `{type: filter, conditions: [{field: ., op: equals, value: "just text"}]}`, `"Just Text"`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := preview(t, parseSteps(t, "- "+tc.step), tc.input)
			if r.Decision == DecisionError || (r.Decision == DecisionEmitted) != tc.want {
				t.Errorf("decision %s (%s), want kept=%v", r.Decision, r.Error, tc.want)
			}
		})
	}
}

// ---- steps ----

func TestStepOutputs(t *testing.T) {
	issue := `{"id":7,"title":"  Crash on start ","author":{"login":"Sam","id":3},"labels":[{"name":"bug","n":2},{"name":"ui","n":5},{"name":"bug","n":1}],"tags":["b","a","b"],"amount":"12","price":4.5,"gone":"x"}`
	cases := []struct {
		name  string
		steps string
		input string
		want  []string // compact JSON per output, in order
	}{
		{"pick nested keeps structure", `[{type: pick, fields: [id, author.login]}]`, issue, []string{`{"id":7,"author":{"login":"Sam"}}`}},
		{"pick flatten", `[{type: pick, fields: [id, author.login], flatten: true}]`, issue, []string{`{"id":7,"author_login":"Sam"}`}},
		{"pick rename", `[{type: pick, fields: [id, author.login], rename: {author.login: who}}]`, issue, []string{`{"id":7,"who":"Sam"}`}},
		{"pick skips missing fields", `[{type: pick, fields: [id, nothing.here]}]`, issue, []string{`{"id":7}`}},
		{"set constant text", `[{type: set, field: status, value: {kind: constant, value: open}}]`, `{"a":1}`, []string{`{"a":1,"status":"open"}`}},
		{"set constant number", `[{type: set, field: n, value: {kind: constant, value: 2.5}}]`, `{}`, []string{`{"n":2.5}`}},
		{"set constant bool", `[{type: set, field: n, value: {kind: constant, value: false}}]`, `{}`, []string{`{"n":false}`}},
		{"set constant list", `[{type: set, field: n, value: {kind: constant, value: [1, a]}}]`, `{}`, []string{`{"n":[1,"a"]}`}},
		{"set nested target", `[{type: set, field: meta.source, value: {kind: constant, value: hal}}]`, `{"meta":{"x":1}}`, []string{`{"meta":{"x":1,"source":"hal"}}`}},
		{"set template", `[{type: set, field: subject, value: {kind: template, template: "#{id} {title}: by {author.login} ({labels[].name}) {{ok}}"}}]`, `{"id":7,"title":"T","author":{"login":"sam"},"labels":[{"name":"a"},{"name":"b"}]}`, []string{`{"id":7,"title":"T","author":{"login":"sam"},"labels":[{"name":"a"},{"name":"b"}],"subject":"#7 T: by sam (a, b) {ok}"}`}},
		{"template missing field is empty", `[{type: set, field: s, value: {kind: template, template: "[{nope}]"}}]`, `{}`, []string{`{"s":"[]"}`}},
		{"fn lowercase", `[{type: set, field: s, value: {kind: function, function: lowercase, field: t}}]`, `{"t":"AbC"}`, []string{`{"t":"AbC","s":"abc"}`}},
		{"fn uppercase", `[{type: set, field: s, value: {kind: function, function: uppercase, field: t}}]`, `{"t":"AbC"}`, []string{`{"t":"AbC","s":"ABC"}`}},
		{"fn trim", `[{type: set, field: s, value: {kind: function, function: trim, field: t}}]`, `{"t":" \n a b \t"}`, []string{`{"t":" \n a b \t","s":"a b"}`}},
		{"fn length of list", `[{type: set, field: s, value: {kind: function, function: length, field: t}}]`, `{"t":[1,2,3]}`, []string{`{"t":[1,2,3],"s":3}`}},
		{"fn length of text", `[{type: set, field: s, value: {kind: function, function: length, field: t}}]`, `{"t":"héllo"}`, []string{`{"t":"héllo","s":5}`}},
		{"fn length of missing", `[{type: set, field: s, value: {kind: function, function: length, field: t}}]`, `{}`, []string{`{"s":0}`}},
		{"fn first", `[{type: set, field: s, value: {kind: function, function: first, field: t}}]`, `{"t":[4,5]}`, []string{`{"t":[4,5],"s":4}`}},
		{"fn last", `[{type: set, field: s, value: {kind: function, function: last, field: t}}]`, `{"t":[4,5]}`, []string{`{"t":[4,5],"s":5}`}},
		{"fn last of empty", `[{type: set, field: s, value: {kind: function, function: last, field: t}}]`, `{"t":[]}`, []string{`{"t":[],"s":null}`}},
		{"fn split", `[{type: set, field: s, value: {kind: function, function: split, field: t, arg: ","}}]`, `{"t":"a,b,,c"}`, []string{`{"t":"a,b,,c","s":["a","b","","c"]}`}},
		{"fn to_number", `[{type: set, field: s, value: {kind: function, function: to_number, field: t}}]`, `{"t":"12.5"}`, []string{`{"t":"12.5","s":12.5}`}},
		{"fn to_number invalid", `[{type: set, field: s, value: {kind: function, function: to_number, field: t}}]`, `{"t":"abc"}`, []string{`{"t":"abc","s":null}`}},
		{"fn to_string", `[{type: set, field: s, value: {kind: function, function: to_string, field: t}}]`, `{"t":12}`, []string{`{"t":12,"s":"12"}`}},
		{"fn default used", `[{type: set, field: s, value: {kind: function, function: default, field: t, arg: none}}]`, `{}`, []string{`{"s":"none"}`}},
		{"fn default keeps falsy value", `[{type: set, field: s, value: {kind: function, function: default, field: t, arg: 1}}]`, `{"t":false}`, []string{`{"t":false,"s":false}`}},
		{"remove", `[{type: remove, fields: [gone, author.id, never.there]}]`, `{"a":1,"gone":2,"author":{"id":1,"login":"x"}}`, []string{`{"a":1,"author":{"login":"x"}}`}},
		{"remove below a non-object", `[{type: remove, fields: [a.b]}]`, `{"a":"text"}`, []string{`{"a":"text"}`}},
		{"split item list", `[{type: split, field: labels}]`, `{"labels":[{"name":"a"},{"name":"b"}]}`, []string{`{"name":"a"}`, `{"name":"b"}`}},
		{"split as", `[{type: split, field: tags, as: tag}]`, `{"tags":["x","y"]}`, []string{`{"tag":"x"}`, `{"tag":"y"}`}},
		{"split keep parent replaces in place", `[{type: split, field: tags, keep_parent: true}]`, `{"id":1,"tags":["x","y"]}`, []string{`{"id":1,"tags":"x"}`, `{"id":1,"tags":"y"}`}},
		{"split keep parent as", `[{type: split, field: tags, as: tag, keep_parent: true}]`, `{"id":1,"tags":["x","y"]}`, []string{`{"id":1,"tag":"x"}`, `{"id":1,"tag":"y"}`}},
		{"split root array", `[{type: split, field: .}]`, `[1,2,3]`, []string{`1`, `2`, `3`}},
		{"split empty list drops", `[{type: split, field: tags}]`, `{"tags":[]}`, nil},
		{"split non-list drops", `[{type: split, field: tags}]`, `{"tags":"x"}`, nil},
		{"split then filter", `[{type: split, field: labels}, {type: filter, conditions: [{field: n, op: gt, value: 1}]}]`, `{"labels":[{"n":1},{"n":2},{"n":3}]}`, []string{`{"n":2}`, `{"n":3}`}},
		{"sort ascending", `[{type: sort, field: tags}]`, `{"tags":["b","a","c"]}`, []string{`{"tags":["a","b","c"]}`}},
		{"sort by descending", `[{type: sort, field: labels, by: n, order: desc}]`, `{"labels":[{"n":2},{"n":9},{"n":5}]}`, []string{`{"labels":[{"n":9},{"n":5},{"n":2}]}`}},
		{"sort missing list untouched", `[{type: sort, field: nope}]`, `{"a":1}`, []string{`{"a":1}`}},
		{"limit", `[{type: limit, field: tags, count: 2}]`, `{"tags":[1,2,3]}`, []string{`{"tags":[1,2]}`}},
		{"limit zero", `[{type: limit, field: tags, count: 0}]`, `{"tags":[1,2,3]}`, []string{`{"tags":[]}`}},
		{"limit larger than list", `[{type: limit, field: tags, count: 9}]`, `{"tags":[1]}`, []string{`{"tags":[1]}`}},
		{"unique keeps first occurrence order", `[{type: unique, field: tags}]`, `{"tags":["b","a","b","c","a"]}`, []string{`{"tags":["b","a","c"]}`}},
		{"unique by", `[{type: unique, field: labels, by: name}]`, `{"labels":[{"name":"x","i":1},{"name":"y","i":2},{"name":"x","i":3}]}`, []string{`{"labels":[{"name":"x","i":1},{"name":"y","i":2}]}`}},
		{"unique tells 1 from text 1", `[{type: unique, field: l}]`, `{"l":[1,"1",1]}`, []string{`{"l":[1,"1"]}`}},
		{"aggregate count", `[{type: aggregate, field: labels, into: n, op: count}]`, `{"labels":[1,2,3]}`, []string{`{"labels":[1,2,3],"n":3}`}},
		{"aggregate count missing", `[{type: aggregate, field: labels, into: n, op: count}]`, `{}`, []string{`{"n":0}`}},
		{"aggregate sum", `[{type: aggregate, field: lines, into: total, op: sum, of: price}]`, `{"lines":[{"price":1.5},{"price":2},{"price":"x"},{}]}`, []string{`{"lines":[{"price":1.5},{"price":2},{"price":"x"},{}],"total":3.5}`}},
		{"aggregate sum of nothing", `[{type: aggregate, field: lines, into: total, op: sum, of: price}]`, `{"lines":[]}`, []string{`{"lines":[],"total":0}`}},
		{"aggregate avg", `[{type: aggregate, field: l, into: m, op: avg}]`, `{"l":[1,2,6]}`, []string{`{"l":[1,2,6],"m":3}`}},
		{"aggregate avg of nothing", `[{type: aggregate, field: l, into: m, op: avg}]`, `{"l":[]}`, []string{`{"l":[],"m":null}`}},
		{"aggregate max", `[{type: aggregate, field: l, into: m, op: max}]`, `{"l":[3,9,2]}`, []string{`{"l":[3,9,2],"m":9}`}},
		{"aggregate collect", `[{type: aggregate, field: l, into: names, op: collect, of: name}]`, `{"l":[{"name":"a"},{},{"name":"b"}]}`, []string{`{"l":[{"name":"a"},{},{"name":"b"}],"names":["a","b"]}`}},
		{"aggregate group count", `[{type: aggregate, field: l, into: by, op: group_count, group_by: kind}]`, `{"l":[{"kind":"a"},{"kind":"b"},{"kind":"a"},{}]}`, []string{`{"l":[{"kind":"a"},{"kind":"b"},{"kind":"a"},{}],"by":[{"value":"a","count":2},{"value":"b","count":1},{"value":null,"count":1}]}`}},
		{"aggregate into nested", `[{type: aggregate, field: l, into: stats.n, op: count}]`, `{"l":[1]}`, []string{`{"l":[1],"stats":{"n":1}}`}},
		{"filter list removes matches", `[{type: filter_list, field: l, keep: false, conditions: [{field: ., op: equals, value: a}]}]`, `{"l":["A","b","a"]}`, []string{`{"l":["b"]}`}},
		{"filter list on root", `[{type: filter_list, field: ., conditions: [{field: ., op: gt, value: 1}]}]`, `[1,2,3]`, []string{`[2,3]`}},
		{"steps chain in order", `[{type: filter, conditions: [{field: n, op: gt, value: 1}]}, {type: set, field: m, value: {kind: constant, value: 1}}, {type: remove, fields: [n]}]`, `{"n":2}`, []string{`{"m":1}`}},
		{"no steps pass through", `[]`, `{"a":1}`, []string{`{"a":1}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := preview(t, parseSteps(t, tc.steps), tc.input)
			if r.Decision == DecisionError {
				t.Fatalf("error: %s\n%s", r.Error, r.JQ)
			}
			got := outs(r)
			if len(got) != len(tc.want) {
				t.Fatalf("outputs = %v, want %v\n%s", got, tc.want, r.JQ)
			}
			for i := range got {
				sameJSON(t, got[i], tc.want[i])
			}
			if len(tc.want) == 0 && r.Decision != DecisionDropped {
				t.Errorf("decision = %s, want dropped", r.Decision)
			}
		})
	}
}

func TestStepOutputsExtra(t *testing.T) {
	issue := `{"id":7,"author":{"login":"Sam"},"labels":[{"name":"bug","n":2},{"name":"ui","n":5},{"name":"bug","n":1}],"lines":[{"v":3},{"v":1}]}`
	check := func(name, steps, want string) {
		t.Run(name, func(t *testing.T) {
			r := preview(t, parseSteps(t, steps), issue)
			if r.Decision == DecisionError || len(r.Outputs) != 1 {
				t.Fatalf("decision %s: %s", r.Decision, r.Error)
			}
			var got map[string]any
			if err := json.Unmarshal(r.Outputs[0].Payload, &got); err != nil {
				t.Fatal(err)
			}
			var w any
			if err := json.Unmarshal([]byte(want), &w); err != nil {
				t.Fatal(err)
			}
			if gv := got["s"]; !reflect.DeepEqual(gv, w) {
				t.Errorf("s = %v, want %v", gv, w)
			}
		})
	}
	check("copy field", `[{type: set, field: s, value: {kind: field, field: author.login}}]`, `"Sam"`)
	check("copy a list of matches", `[{type: set, field: s, value: {kind: field, field: "labels[].name"}}]`, `["bug","ui","bug"]`)
	check("join", `[{type: set, field: s, value: {kind: function, function: join, field: "labels[].name", arg: " | "}}]`, `"bug | ui | bug"`)
	check("min", `[{type: aggregate, field: lines, into: s, op: min, of: v}]`, `1`)
	check("filter list", `[{type: filter_list, field: labels, conditions: [{field: name, op: equals, value: bug}]}, {type: aggregate, field: labels, into: s, op: count}]`, `2`)
}

func TestRoute(t *testing.T) {
	steps := parseSteps(t, `
- type: route
  default_port: normal
  rules:
    - {port: urgent, conditions: [{field: severity, op: equals, value: urgent}]}
    - {port: big, match: any, conditions: [{field: size, op: gt, value: 100}, {field: "tags[]", op: equals, value: huge}]}
`)
	if got := Ports(steps); !reflect.DeepEqual(got, []string{"out", "urgent", "big", "normal"}) {
		t.Errorf("ports = %v", got)
	}
	cases := []struct{ input, port string }{
		{`{"severity":"URGENT","size":500}`, "urgent"}, // first matching rule wins
		{`{"severity":"low","size":500}`, "big"},
		{`{"severity":"low","tags":["huge"]}`, "big"},
		{`{"severity":"low","size":1}`, "normal"},
	}
	for _, tc := range cases {
		r := preview(t, steps, tc.input)
		if r.Decision != DecisionEmitted || len(r.Outputs) != 1 || r.Outputs[0].Port != tc.port {
			t.Errorf("%s -> %+v (%s), want port %s", tc.input, r.Outputs, r.Error, tc.port)
			continue
		}
		if strings.Contains(string(r.Outputs[0].Payload), "_port") || strings.Contains(string(r.Outputs[0].Payload), "_reason") {
			t.Errorf("reserved keys not stripped: %s", r.Outputs[0].Payload)
		}
		if r.Reason == "" {
			t.Errorf("no reason for %s", tc.input)
		}
	}
	if r := preview(t, steps, `{"severity":"urgent"}`); !strings.Contains(r.Reason, "urgent") || !strings.Contains(r.Reason, "severity is") {
		t.Errorf("reason = %q", r.Reason)
	}

	// Without default_port, unmatched items leave on out.
	plain := parseSteps(t, `- {type: route, rules: [{port: a, conditions: [{field: x, op: exists}]}]}`)
	if r := preview(t, plain, `{"y":1}`); len(r.Outputs) != 1 || r.Outputs[0].Port != "out" {
		t.Errorf("default port: %+v", r.Outputs)
	}
	// Route needs an object.
	if r := preview(t, steps, `"text"`); r.Decision != DecisionError || !strings.Contains(r.Error, "object") {
		t.Errorf("route on text: %s %s", r.Decision, r.Error)
	}
	// Routing after a split routes each item on its own.
	fan := parseSteps(t, `
- {type: split, field: items}
- type: route
  rules: [{port: a, conditions: [{field: k, op: equals, value: a}]}]
  default_port: b
`)
	r := preview(t, fan, `{"items":[{"k":"a"},{"k":"z"},{"k":"A"}]}`)
	var ports []string
	for _, o := range r.Outputs {
		ports = append(ports, o.Port)
	}
	if !reflect.DeepEqual(ports, []string{"a", "b", "a"}) {
		t.Errorf("fan-out ports = %v", ports)
	}
}

// ---- hostile values ----

var hostile = []string{
	`"`, `\`, `\"`, `")`, `"); halt_error("pwned"); ("`, `$__loc__`, `\(1+1)`, "line1\nline2", "tab\there",
	`}; error("x"); {`, `# comment`, "\u2028 sep \u2029", `'; rm -rf /; '`, `{{ }} {x}`, `.. | tostring`, "nul\x00byte",
}

func TestHostileValuesAreData(t *testing.T) {
	for _, h := range hostile {
		t.Run(h, func(t *testing.T) {
			t.Parallel()
			lit := yamlStr(h)
			input := mustJSON(map[string]any{"v": h, "list": []string{h, "x"}})
			// equals / contains / starts_with / ends_with / in find the value, in both case modes.
			for _, op := range []string{"equals", "contains", "starts_with", "ends_with"} {
				for _, cs := range []string{"false", "true"} {
					steps := parseSteps(t, "- {type: filter, conditions: [{field: v, op: "+op+", value: "+lit+", case_sensitive: "+cs+"}]}")
					if r := preview(t, steps, input); r.Decision != DecisionEmitted {
						t.Errorf("%s cs=%s: %s %s\n%s", op, cs, r.Decision, r.Error, r.JQ)
					}
				}
			}
			steps := parseSteps(t, "- {type: filter, conditions: [{field: v, op: in, value: ["+lit+", other]}, {field: 'list[]', op: equals, value: "+lit+"}]}")
			if r := preview(t, steps, input); r.Decision != DecisionEmitted {
				t.Errorf("in: %s %s\n%s", r.Decision, r.Error, r.JQ)
			}
			// A different value must not match: the text is data, not a pattern or code.
			steps = parseSteps(t, "- {type: filter, conditions: [{field: v, op: equals, value: "+yamlStr(h+"x")+"}]}")
			if r := preview(t, steps, input); r.Decision != DecisionDropped {
				t.Errorf("equals matched a different value: %s", r.Decision)
			}
			// Constants, templates, separators, defaults, field names, rename and as are data too.
			steps = parseSteps(t, `
- {type: set, field: c, value: {kind: constant, value: `+lit+`}}
- {type: set, field: t, value: {kind: template, template: `+yamlStr(strings.NewReplacer("{", "{{", "}", "}}").Replace(h)+" {v}")+`}}
- {type: set, field: j, value: {kind: function, function: join, field: list, arg: `+lit+`}}
- {type: set, field: d, value: {kind: function, function: default, field: nope, arg: `+lit+`}}
`)
			r := preview(t, steps, input)
			if r.Decision != DecisionEmitted {
				t.Fatalf("set: %s %s\n%s", r.Decision, r.Error, r.JQ)
			}
			var got map[string]any
			_ = json.Unmarshal(r.Outputs[0].Payload, &got)
			if got["c"] != h || got["d"] != h || got["t"] != h+" "+h || got["j"] != h+h+"x" {
				t.Errorf("values changed: %v", got)
			}
			// Field names that look like syntax.
			key := h
			if strings.ContainsAny(key, ".[]") || key == "" || strings.ContainsFunc(key, func(r rune) bool { return r < 0x20 }) {
				return
			}
			input2 := mustJSON(map[string]any{key: 1})
			steps = parseSteps(t, "- {type: pick, fields: ["+yamlStr(key)+"], rename: {"+yamlStr(key)+": "+yamlStr("r"+key)+"}}\n- {type: set, field: "+yamlStr("n"+key)+", value: {kind: field, field: "+yamlStr("r"+key)+"}}")
			r = preview(t, steps, input2)
			if r.Decision != DecisionEmitted {
				t.Fatalf("field names: %s %s\n%s", r.Decision, r.Error, r.JQ)
			}
			sameJSON(t, outs(r)[0], mustJSON(map[string]any{"r" + key: 1, "n" + key: 1}))
		})
	}
}

// Regex metacharacters in text operators are literal; only `matches` takes a pattern.
func TestTextOperatorsAreLiteral(t *testing.T) {
	for _, v := range []string{"a.c", "a+", "(x)", "[a-z]", "^a", "a$", "a|b", `a\d`, "{2}"} {
		steps := parseSteps(t, "- {type: filter, conditions: [{field: v, op: contains, value: "+yamlStr(v)+"}]}")
		if r := preview(t, steps, mustJSON(map[string]any{"v": "xx" + v + "yy"})); r.Decision != DecisionEmitted {
			t.Errorf("contains %q on its own text: %s", v, r.Decision)
		}
		if r := preview(t, steps, `{"v":"abc aa a1 x"}`); r.Decision != DecisionDropped {
			t.Errorf("contains %q matched as a pattern", v)
		}
	}
}

// The step description appears in a jq comment; nothing in it may leave the comment.
func TestDescriptionCannotEscapeComment(t *testing.T) {
	needJQ(t)
	for _, h := range hostile {
		for _, field := range []string{h, "a"} {
			steps := []Step{
				{Type: StepFilter, Conditions: []Condition{{Field: "x", Op: OpEquals, Value: h}}},
				{Type: StepSet, Field: "t", Value: &Value{Kind: ValueTemplate, Template: strings.NewReplacer("{", "{{", "}", "}}").Replace(h)}},
				{Type: StepSet, Field: "u", Value: &Value{Kind: ValueConstant, Constant: h}},
			}
			_ = field
			prog, err := Compile(steps)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(prog, "\n") {
				if strings.HasPrefix(line, "#") && (strings.ContainsRune(line, '\\') || strings.ContainsAny(line, "\r\x00\u2028\u2029")) {
					t.Errorf("unsafe comment line %q", line)
				}
			}
			n := 0
			for _, line := range strings.Split(prog, "\n") {
				if strings.HasPrefix(line, "# step ") {
					n++
				}
			}
			if n != 3 {
				t.Errorf("hostile text added or removed comment lines (%d):\n%s", n, prog)
			}
			r := preview(t, steps, mustJSON(map[string]any{"x": h}))
			if r.Decision != DecisionEmitted {
				t.Errorf("%q: %s %s", h, r.Decision, r.Error)
			}
		}
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// ---- determinism and readability ----

func TestCompileIsDeterministicAndReadable(t *testing.T) {
	steps := parseSteps(t, `
- {type: filter, conditions: [{field: severity, op: not_equals, value: noise}]}
- {type: set, field: label, value: {kind: function, function: uppercase, field: severity}}
`)
	a, err := Compile(steps)
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if b, _ := Compile(steps); b != a {
			t.Fatal("Compile output changed between calls")
		}
	}
	want := `def hal_get($p): try getpath($p) catch null;
# step 1: Keep only items where severity is not "noise"
select(((hal_get(["severity"]) | (type == "string" and test("\\Anoise\\z"; "i"))) | not))
# step 2: Set label to severity in uppercase
| setpath(["label"]; (hal_get(["severity"]) | if type == "string" then ascii_upcase else . end))
`
	if a != want {
		t.Errorf("program:\n%s\nwant:\n%s", a, want)
	}
	if got, _ := Compile(nil); got != "." {
		t.Errorf("no steps compile to %q", got)
	}
}

func TestDescribe(t *testing.T) {
	steps := parseSteps(t, `
- {type: filter, match: any, conditions: [{field: severity, op: not_equals, value: noise}, {field: "labels[].name", op: contains, value: bug, case_sensitive: true}]}
- {type: filter, keep: false, conditions: [{field: author, op: is_empty}]}
- {type: pick, fields: [id, author.login], rename: {author.login: who}, flatten: true}
- {type: set, field: s, value: {kind: function, function: default, field: t, arg: none}}
- {type: remove, fields: [a, b]}
- {type: split, field: items, as: item, keep_parent: true}
- {type: sort, field: items, by: n, order: desc}
- {type: limit, field: items, count: 3}
- {type: unique, field: items, by: id}
- {type: aggregate, field: items, into: total, op: sum, of: price}
- {type: filter_list, field: items, conditions: [{field: n, op: in, value: [1, 2]}]}
- {type: route, default_port: rest, rules: [{port: urgent, conditions: [{field: n, op: gt, value: 3}]}]}
`)
	want := []string{
		`Keep only items where severity is not "noise" or any labels[].name contains "bug" (same capitalization)`,
		`Drop items where author is empty`,
		`Keep only the fields id, author.login (author.login becomes "who"), with nested fields flattened`,
		`Set s to t, or "none" when it is missing`,
		`Remove the fields a, b`,
		`Turn each item of items into a separate item, keeping the other fields, with each item as "item"`,
		`Sort items by n (descending)`,
		`Keep the first 3 of items`,
		`Remove duplicates from items (same id)`,
		`Set total to the sum of price in items`,
		`Keep only the items of items where n is one of 1, 2`,
		`Send items to different outputs: "urgent" when n is greater than 3; everything else to "rest"`,
	}
	got := Describe(steps)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("step %d:\n got %s\nwant %s", i+1, got[i], want[i])
		}
	}
}

// ---- validation ----

func TestValidate(t *testing.T) {
	cases := []struct {
		name, steps string
		path, code  string // the first error expected; empty path = no error
	}{
		{"valid", `[{type: filter, conditions: [{field: a, op: exists}]}]`, "", ""},
		{"unknown kind", `[{type: shuffle}]`, "steps[0].type", CodeInvalidEnum},
		{"no kind", `[{}]`, "steps[0].type", CodeRequired},
		{"field of another kind", `[{type: remove, fields: [a], count: 2}]`, "steps[0].count", CodeFieldNotAllowed},
		{"filter without conditions", `[{type: filter}]`, "steps[0].conditions", CodeRequired},
		{"bad match", `[{type: filter, match: some, conditions: [{field: a, op: exists}]}]`, "steps[0].match", CodeInvalidEnum},
		{"unknown operator", `[{type: set, field: a, value: {kind: constant, value: 1}}, {type: filter, conditions: [{field: a, op: exists}, {field: b, op: like, value: x}]}]`, "steps[1].conditions[1].op", CodeInvalidEnum},
		{"bad path", `[{type: filter, conditions: [{field: "a..b", op: exists}]}]`, "steps[0].conditions[0].field", CodeInvalidValue},
		{"stray bracket", `[{type: filter, conditions: [{field: "a[0]", op: exists}]}]`, "steps[0].conditions[0].field", CodeInvalidValue},
		{"empty field", `[{type: filter, conditions: [{field: "", op: exists}]}]`, "steps[0].conditions[0].field", CodeRequired},
		{"value missing", `[{type: filter, conditions: [{field: a, op: equals}]}]`, "steps[0].conditions[0].value", CodeRequired},
		{"value not allowed", `[{type: filter, conditions: [{field: a, op: exists, value: 1}]}]`, "steps[0].conditions[0].value", CodeFieldNotAllowed},
		{"text op with number", `[{type: filter, conditions: [{field: a, op: starts_with, value: 1}]}]`, "steps[0].conditions[0].value", CodeInvalidValue},
		{"gt with bool", `[{type: filter, conditions: [{field: a, op: gt, value: true}]}]`, "steps[0].conditions[0].value", CodeInvalidValue},
		{"in without list", `[{type: filter, conditions: [{field: a, op: in, value: x}]}]`, "steps[0].conditions[0].value", CodeInvalidValue},
		{"in with empty list", `[{type: filter, conditions: [{field: a, op: in, value: []}]}]`, "steps[0].conditions[0].value", CodeInvalidValue},
		{"in with nested list", `[{type: filter, conditions: [{field: a, op: in, value: [[1]]}]}]`, "steps[0].conditions[0].value[0]", CodeInvalidValue},
		{"bad regex", `[{type: filter, conditions: [{field: a, op: matches, value: "("}]}]`, "steps[0].conditions[0].value", CodeInvalidRegex},
		{"case on non text", `[{type: filter, conditions: [{field: a, op: exists, case_sensitive: true}]}]`, "steps[0].conditions[0].case_sensitive", CodeFieldNotAllowed},
		{"route not last", `[{type: route, rules: [{port: a, conditions: [{field: x, op: exists}]}]}, {type: remove, fields: [x]}]`, "steps[0]", CodeInvalidValue},
		{"route duplicate ports", `[{type: route, rules: [{port: a, conditions: [{field: x, op: exists}]}, {port: a, conditions: [{field: y, op: exists}]}]}]`, "steps[0].rules[1].port", CodeDuplicateID},
		{"route reserved port", `[{type: route, rules: [{port: error, conditions: [{field: x, op: exists}]}]}]`, "steps[0].rules[0].port", CodeReservedPort},
		{"route bad port name", `[{type: route, rules: [{port: "a b", conditions: [{field: x, op: exists}]}]}]`, "steps[0].rules[0].port", CodeInvalidID},
		{"route rule without conditions", `[{type: route, rules: [{port: a}]}]`, "steps[0].rules[0].conditions", CodeRequired},
		{"route bad default", `[{type: route, default_port: error, rules: [{port: a, conditions: [{field: x, op: exists}]}]}]`, "steps[0].default_port", CodeReservedPort},
		{"route without rules", `[{type: route}]`, "steps[0].rules", CodeRequired},
		{"pick without fields", `[{type: pick}]`, "steps[0].fields", CodeRequired},
		{"pick with brackets", `[{type: pick, fields: ["a[].b"]}]`, "steps[0].fields[0]", CodeInvalidValue},
		{"pick rename of unpicked", `[{type: pick, fields: [a], rename: {b: c}}]`, "steps[0].rename.b", CodeInvalidValue},
		{"pick colliding names", `[{type: pick, fields: [a.id, b.id], flatten: false, rename: {a.id: x, b.id: x}}]`, "steps[0].fields[1]", CodeDuplicateID},
		{"pick flatten collision", `[{type: pick, fields: [a_b, a.b], flatten: true}]`, "steps[0].fields[1]", CodeDuplicateID},
		{"set without value", `[{type: set, field: a}]`, "steps[0].value", CodeRequired},
		{"set bad kind", `[{type: set, field: a, value: {kind: magic}}]`, "steps[0].value.kind", CodeInvalidEnum},
		{"set target with brackets", `[{type: set, field: "a[]", value: {kind: constant, value: 1}}]`, "steps[0].field", CodeInvalidValue},
		{"set stray field", `[{type: set, field: a, value: {kind: constant, value: 1, template: x}}]`, "steps[0].value.template", CodeFieldNotAllowed},
		{"set bad template", `[{type: set, field: a, value: {kind: template, template: "{oops"}}]`, "steps[0].value.template", CodeInvalidTemplate},
		{"set empty placeholder", `[{type: set, field: a, value: {kind: template, template: "x {} y"}}]`, "steps[0].value.template", CodeInvalidTemplate},
		{"set lone brace", `[{type: set, field: a, value: {kind: template, template: "x } y"}}]`, "steps[0].value.template", CodeInvalidTemplate},
		{"set placeholder bad path", `[{type: set, field: a, value: {kind: template, template: "{a..b}"}}]`, "steps[0].value.template", CodeInvalidTemplate},
		{"set unknown function", `[{type: set, field: a, value: {kind: function, function: reverse, field: b}}]`, "steps[0].value.function", CodeInvalidEnum},
		{"join without separator", `[{type: set, field: a, value: {kind: function, function: join, field: b}}]`, "steps[0].value.arg", CodeRequired},
		{"split with empty separator", `[{type: set, field: a, value: {kind: function, function: split, field: b, arg: ""}}]`, "steps[0].value.arg", CodeInvalidValue},
		{"default without value", `[{type: set, field: a, value: {kind: function, function: default, field: b}}]`, "steps[0].value.arg", CodeRequired},
		{"lowercase with arg", `[{type: set, field: a, value: {kind: function, function: lowercase, field: b, arg: x}}]`, "steps[0].value.arg", CodeFieldNotAllowed},
		{"remove without fields", `[{type: remove}]`, "steps[0].fields", CodeRequired},
		{"remove duplicates", `[{type: remove, fields: [a, a]}]`, "steps[0].fields[1]", CodeDuplicateID},
		{"split without field", `[{type: split}]`, "steps[0].field", CodeRequired},
		{"split bad as", `[{type: split, field: a, as: "x.y"}]`, "steps[0].as", CodeInvalidValue},
		{"split root keep parent", `[{type: split, field: ., keep_parent: true}]`, "steps[0].keep_parent", CodeInvalidValue},
		{"sort bad order", `[{type: sort, field: a, order: up}]`, "steps[0].order", CodeInvalidEnum},
		{"sort by with brackets", `[{type: sort, field: a, by: "b[]"}]`, "steps[0].by", CodeInvalidValue},
		{"limit without count", `[{type: limit, field: a}]`, "steps[0].count", CodeRequired},
		{"limit negative", `[{type: limit, field: a, count: -1}]`, "steps[0].count", CodeInvalidValue},
		{"aggregate bad op", `[{type: aggregate, field: a, into: b, op: median}]`, "steps[0].op", CodeInvalidEnum},
		{"aggregate group count needs group_by", `[{type: aggregate, field: a, into: b, op: group_count}]`, "steps[0].group_by", CodeRequired},
		{"aggregate count rejects of", `[{type: aggregate, field: a, into: b, op: count, of: x}]`, "steps[0].of", CodeFieldNotAllowed},
		{"aggregate without into", `[{type: aggregate, field: a, op: count}]`, "steps[0].into", CodeRequired},
		{"aggregate whole item", `[{type: aggregate, field: ., into: b, op: count}]`, "steps[0].field", CodeInvalidValue},
		{"filter list without conditions", `[{type: filter_list, field: a}]`, "steps[0].conditions", CodeRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			issues := Validate(parseSteps(t, tc.steps))
			var first *Issue
			for i := range issues {
				if issues[i].Severity == SeverityError {
					first = &issues[i]
					break
				}
			}
			switch {
			case tc.path == "" && first != nil:
				t.Fatalf("unexpected error %+v", *first)
			case tc.path != "" && first == nil:
				t.Fatalf("no error; want %s at %s", tc.code, tc.path)
			case tc.path != "":
				found := false
				for _, is := range issues {
					found = found || (is.Path == tc.path && is.Code == tc.code && is.Severity == SeverityError && is.Message != "")
				}
				if !found {
					t.Errorf("want %s at %s; got %+v", tc.code, tc.path, issues)
				}
				if _, err := Compile(parseSteps(t, tc.steps)); err == nil {
					t.Error("Compile accepted invalid steps")
				}
			}
		})
	}
	if is := Validate(nil); len(is) != 1 || is[0].Severity != SeverityWarning {
		t.Errorf("no steps: %+v", is)
	}
}

// ---- preview ----

func TestPreviewDropReasonsAndCounts(t *testing.T) {
	steps := parseSteps(t, `
- {type: split, field: items}
- {type: filter, conditions: [{field: kind, op: not_equals, value: noise}, {field: n, op: gt, value: 1}]}
- {type: set, field: seen, value: {kind: constant, value: true}}
`)
	r := preview(t, steps, `{"items":[{"kind":"a","n":5},{"kind":"noise","n":9},{"kind":"b","n":0}]}`)
	if r.Decision != DecisionEmitted || len(r.Outputs) != 1 {
		t.Fatalf("decision %s %s", r.Decision, r.Error)
	}
	counts := []int{}
	for _, sr := range r.StepResults {
		counts = append(counts, sr.OutputsCount)
	}
	if !reflect.DeepEqual(counts, []int{3, 1, 1}) {
		t.Errorf("counts = %v", counts)
	}

	drops := []struct {
		name, steps, input, reason string
	}{
		{"keep all: first unmet", `- {type: filter, conditions: [{field: a, op: exists}, {field: b, op: equals, value: x}, {field: c, op: exists}]}`, `{"a":1,"b":"y"}`, `Dropped by step 1: it did not meet the condition: b is "x"`},
		{"keep any", `- {type: filter, match: any, conditions: [{field: a, op: equals, value: 1}, {field: b, op: equals, value: 2}]}`, `{"a":9}`, `Dropped by step 1: it met none of the conditions: a is 1; b is 2`},
		{"keep one condition", `- {type: filter, conditions: [{field: a, op: equals, value: noise}]}`, `{"a":9}`, `Dropped by step 1: it did not meet the condition: a is "noise"`},
		{"drop matching one", `- {type: filter, keep: false, conditions: [{field: severity, op: equals, value: noise}]}`, `{"severity":"noise"}`, `Dropped by step 1: it met the condition: severity is "noise"`},
		{"drop matching all", `- {type: filter, keep: false, conditions: [{field: a, op: exists}, {field: b, op: exists}]}`, `{"a":1,"b":1}`, `Dropped by step 1: it met all of the conditions: a is present; b is present`},
		{"drop matching any", `- {type: filter, keep: false, match: any, conditions: [{field: a, op: exists}, {field: b, op: exists}]}`, `{"b":1}`, `Dropped by step 1: it met the condition: b is present`},
		{"second step", "- {type: set, field: x, value: {kind: constant, value: 1}}\n- {type: filter, conditions: [{field: x, op: equals, value: 2}]}", `{}`, `Dropped by step 2: it did not meet the condition: x is 2`},
		{"empty split", `- {type: split, field: items}`, `{"items":[]}`, `Dropped by step 1: items has no items to split`},
	}
	for _, tc := range drops {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := preview(t, parseSteps(t, tc.steps), tc.input)
			if r.Decision != DecisionDropped || r.Reason != tc.reason {
				t.Errorf("decision %s reason %q\nwant %q", r.Decision, r.Reason, tc.reason)
			}
			var dropped []int
			for _, sr := range r.StepResults {
				if sr.Dropped {
					dropped = append(dropped, sr.Step)
				}
			}
			if len(dropped) != 1 {
				t.Errorf("dropped steps = %v", dropped)
			}
			needJQ(t)
			run := NewRunner("", 10*time.Second)
			if got := DropReason(t.Context(), parseSteps(t, tc.steps), []byte(tc.input), run); got != tc.reason {
				t.Errorf("DropReason = %q", got)
			}
		})
	}
}

func TestPreviewRuntimeErrorAndInvalidSteps(t *testing.T) {
	steps := parseSteps(t, `
- {type: filter, conditions: [{field: ., op: exists}]}
- {type: set, field: "z", value: {kind: constant, value: 1}}
- {type: remove, fields: [z]}
`)
	// The item is a list, so adding a named field to it fails at run time.
	r := preview(t, steps, `[1]`)
	if r.Decision != DecisionError || r.Error == "" {
		t.Fatalf("want error, got %s", r.Decision)
	}
	if len(r.StepResults) != 3 {
		t.Fatalf("step results: %+v", r.StepResults)
	}
	if r.StepResults[0].Error != "" || r.StepResults[0].OutputsCount != 1 || r.StepResults[1].Error == "" || r.StepResults[2].OutputsCount != 0 || r.StepResults[2].Error != "" {
		t.Errorf("step results: %+v", r.StepResults)
	}

	bad := parseSteps(t, `- {type: filter, conditions: [{field: a, op: equals}]}`)
	r = preview(t, bad, `{}`)
	if r.Decision != DecisionError || r.JQ != "" || len(r.Issues) == 0 || r.Issues[0].Path != "steps[0].conditions[0].value" {
		t.Errorf("invalid steps: %+v", r)
	}
}

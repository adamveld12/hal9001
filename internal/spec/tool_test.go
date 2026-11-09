package spec

import (
	"encoding/json"
	"strings"
	"testing"
)

const toolYAML = `id: lookup
description: Look something up
params:
  query: {type: string, description: What to find, required: true}
  limit: {type: integer, default: 5}
  mode: {type: string, enum: [fast, deep], default: fast}
lang: bash
script: echo "$ARG_QUERY"
approve:
  when:
    match: any
    conditions:
      - {field: mode, op: equals, value: deep}
      - {field: limit, op: gt, value: 50}
timeout: 10s
runs_in_sandbox: true
`

func TestToolLoadsAndRoundTrips(t *testing.T) {
	tool, ds := CheckTool("lookup", []byte(toolYAML))
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	if tool.Approve.Mode != ToolApproveWhen || len(tool.Approve.When.Conditions) != 2 || !tool.RunsInSandbox {
		t.Fatalf("parsed %+v", tool)
	}
	out, err := MarshalTool(*tool)
	if err != nil {
		t.Fatal(err)
	}
	again, ds := CheckTool("lookup", out)
	if len(ds) != 0 {
		t.Fatalf("round trip diagnostics %v\n%s", ds, out)
	}
	a, _ := json.Marshal(tool)
	b, _ := json.Marshal(again)
	if string(a) != string(b) {
		t.Errorf("round trip changed the tool:\n%s\n%s", a, b)
	}
	j, _ := json.Marshal(tool.Approve)
	if !strings.HasPrefix(string(j), `{"when":`) {
		t.Errorf("approve JSON = %s", j)
	}
}

func TestToolDefaultsAndApproveForms(t *testing.T) {
	tool, ds := CheckTool("t", []byte("id: t\ndescription: d\nlang: bash\nscript: echo\n"))
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	if tool.Approve.Mode != ToolApproveAlways || tool.Timeout != DefaultToolTimeout {
		t.Errorf("defaults: %+v", tool)
	}
	tool, _ = CheckTool("t", []byte("id: t\ndescription: d\nlang: bash\nscript: echo\napprove: never\n"))
	if tool.Approve.Mode != ToolApproveNever {
		t.Errorf("approve = %+v", tool.Approve)
	}
	var a ToolApprove
	if err := json.Unmarshal([]byte(`{"when":{"conditions":[{"field":"a","op":"exists"}]}}`), &a); err != nil || a.Mode != ToolApproveWhen {
		t.Errorf("json approve = %+v, %v", a, err)
	}
}

func TestToolValidation(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, code, path string }{
		"jq lang":        {"id: t\ndescription: d\nlang: jq\nscript: .\n", CodeInvalidEnum, "lang"},
		"no description": {"id: t\nlang: bash\nscript: echo\n", CodeRequired, "description"},
		"no script":      {"id: t\ndescription: d\nlang: bash\n", CodeRequired, "script"},
		"bad param name": {"id: t\ndescription: d\nlang: bash\nscript: x\nparams: {'a-b': {type: string}}\n", CodeInvalidID, "params.a-b"},
		"bad type":       {"id: t\ndescription: d\nlang: bash\nscript: x\nparams: {a: {type: text}}\n", CodeInvalidEnum, "params.a.type"},
		"default type":   {"id: t\ndescription: d\nlang: bash\nscript: x\nparams: {a: {type: integer, default: x}}\n", CodeInvalidValue, "params.a.default"},
		"default enum":   {"id: t\ndescription: d\nlang: bash\nscript: x\nparams: {a: {type: string, enum: [a, b], default: c}}\n", CodeInvalidValue, "params.a.default"},
		"params+schema":  {"id: t\ndescription: d\nlang: bash\nscript: x\nparams: {a: {type: string}}\nschema: {type: object}\n", CodeFieldNotAllowed, "schema"},
		"schema type":    {"id: t\ndescription: d\nlang: bash\nscript: x\nschema: {type: string}\n", CodeInvalidValue, "schema.type"},
		"approve word":   {"id: t\ndescription: d\nlang: bash\nscript: x\napprove: sometimes\n", CodeInvalidEnum, "approve"},
		"when empty":     {"id: t\ndescription: d\nlang: bash\nscript: x\napprove: {when: {conditions: []}}\n", CodeRequired, "approve.when.conditions"},
		"when bad op":    {"id: t\ndescription: d\nlang: bash\nscript: x\napprove: {when: {conditions: [{field: a, op: nope}]}}\n", CodeInvalidEnum, "approve.when.conditions[0].op"},
	} {
		t.Run(name, func(t *testing.T) {
			_, ds := CheckTool("t", []byte(tc.yaml))
			for _, d := range ds {
				if d.Code == tc.code && d.Path == tc.path && d.Severity == SeverityError {
					return
				}
			}
			t.Errorf("want error %s at %s, got %v", tc.code, tc.path, ds)
		})
	}
}

func TestToolEffectiveSchema(t *testing.T) {
	tool, _ := ParseTool("", []byte(toolYAML))
	s := tool.EffectiveSchema()
	b, _ := json.Marshal(s)
	want := `{"additionalProperties":false,"properties":{"limit":{"default":5,"type":"integer"},"mode":{"default":"fast","enum":["fast","deep"],"type":"string"},"query":{"description":"What to find","type":"string"}},"required":["query"],"type":"object"}`
	if string(b) != want {
		t.Errorf("schema =\n%s\nwant\n%s", b, want)
	}
	raw, _ := ParseTool("", []byte("id: t\ndescription: d\nlang: bash\nscript: x\nschema: {type: object, properties: {x: {type: string}}}\n"))
	if raw.EffectiveSchema()["properties"] == nil {
		t.Errorf("raw schema not returned: %v", raw.EffectiveSchema())
	}
}

func TestToolsLoadFromHome(t *testing.T) {
	home := t.TempDir()
	if _, err := Init(home, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteTool(home, "lookup", []byte(toolYAML), ""); err != nil {
		t.Fatal(err)
	}
	if k, id, ok := ClassifyPath("tools/lookup.yaml"); !ok || k != KindTool || id != "lookup" {
		t.Errorf("classify tool = %v %v %v", k, id, ok)
	}
	if k, id, ok := ClassifyPath("env.d/tools/lookup.env"); !ok || k != KindToolEnv || id != "lookup" {
		t.Errorf("classify env = %v %v %v", k, id, ok)
	}
	ws := Load(home)
	if f := ws.Tool("lookup"); f == nil || !f.Valid() {
		t.Fatalf("tool not loaded valid: %+v", f)
	}
}

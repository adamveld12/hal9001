package hashline

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const schemaURL = "https://hashline.invalid/request.json"

func compileSchema(t *testing.T, doc map[string]any) *jsonschema.Schema {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaURL, parsed); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile(schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

func accepts(t *testing.T, sch *jsonschema.Schema, doc string) bool {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("test document %s: %v", doc, err)
	}
	return sch.Validate(inst) == nil
}

func TestRequestSchema(t *testing.T) {
	sch := compileSchema(t, RequestSchema())
	h := strings.Repeat("ab", 32)

	for _, doc := range []string{
		`{"expected_hash":"","content":"x"}`,
		`{"expected_hash":"","content":""}`,
		`{"expected_hash":"` + h + `","content":"x"}`,
		`{"expected_hash":"` + h + `","edits":[{"op":"delete","start":"3:abcd"}]}`,
		`{"expected_hash":"` + h + `","edits":[{"op":"replace","start":"3:abcd","end":"90:0f0f","content":"x"},{"op":"insert_after","start":"12:abcd","content":""},{"op":"insert_before","start":"1:0000","content":"y"}]}`,
	} {
		if !accepts(t, sch, doc) {
			t.Errorf("the schema rejects %s", doc)
		}
	}

	for _, tc := range []struct{ name, doc string }{
		{"no expected_hash", `{"content":"x"}`},
		{"neither content nor edits", `{"expected_hash":""}`},
		{"both content and edits", `{"expected_hash":"","content":"x","edits":[{"op":"delete","start":"1:abcd"}]}`},
		{"short hash", `{"expected_hash":"abc","content":"x"}`},
		{"uppercase hash", `{"expected_hash":"` + strings.ToUpper(h) + `","content":"x"}`},
		{"extra property", `{"expected_hash":"","content":"x","path":"a"}`},
		{"null content", `{"expected_hash":"","content":null}`},
		{"no edits", `{"expected_hash":"","edits":[]}`},
		{"unknown op", `{"expected_hash":"","edits":[{"op":"swap","start":"1:abcd"}]}`},
		{"edit without op", `{"expected_hash":"","edits":[{"start":"1:abcd"}]}`},
		{"edit without start", `{"expected_hash":"","edits":[{"op":"delete"}]}`},
		{"malformed anchor", `{"expected_hash":"","edits":[{"op":"delete","start":"1-abcd"}]}`},
		{"line zero", `{"expected_hash":"","edits":[{"op":"delete","start":"0:abcd"}]}`},
		{"uppercase anchor hash", `{"expected_hash":"","edits":[{"op":"delete","start":"1:ABCD"}]}`},
		{"malformed end", `{"expected_hash":"","edits":[{"op":"delete","start":"1:abcd","end":"x"}]}`},
		{"extra edit property", `{"expected_hash":"","edits":[{"op":"delete","start":"1:abcd","extra":1}]}`},
		{"edit content that is no string", `{"expected_hash":"","edits":[{"op":"replace","start":"1:abcd","content":1}]}`},
	} {
		if accepts(t, sch, tc.doc) {
			t.Errorf("%s: the schema accepts %s", tc.name, tc.doc)
		}
	}
}

// A tool adds its own properties (a path) to the schema; the rule that exactly one of content and edits is sent must
// not depend on how many properties there are.
func TestRequestSchemaWithMoreProperties(t *testing.T) {
	s := RequestSchema()
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties is a %T, want map[string]any", s["properties"])
	}
	props["path"] = map[string]any{"type": "string"}
	required, ok := s["required"].([]string)
	if !ok {
		t.Fatalf("required is a %T, want []string", s["required"])
	}
	s["required"] = append(required, "path")
	sch := compileSchema(t, s)

	for _, doc := range []string{
		`{"path":"a.yaml","expected_hash":"","content":"x"}`,
		`{"path":"a.yaml","expected_hash":"","edits":[{"op":"delete","start":"1:abcd"}]}`,
	} {
		if !accepts(t, sch, doc) {
			t.Errorf("the schema rejects %s", doc)
		}
	}
	for _, doc := range []string{
		`{"expected_hash":"","content":"x"}`,
		`{"path":"a.yaml","expected_hash":""}`,
		`{"path":"a.yaml","expected_hash":"","content":"x","edits":[{"op":"delete","start":"1:abcd"}]}`,
	} {
		if accepts(t, sch, doc) {
			t.Errorf("the schema accepts %s", doc)
		}
	}

	// Every call returns a schema of its own.
	if _, ok := RequestSchema()["properties"].(map[string]any)["path"]; ok {
		t.Error("a change to one schema shows in the next one")
	}
}

// The Anthropic API refuses a tool whose input schema has oneOf, anyOf or allOf at the top level, which breaks every
// request that carries the tool; $schema is refused as well.
func TestRequestSchemaTopLevel(t *testing.T) {
	s := RequestSchema()
	if s["type"] != "object" || s["additionalProperties"] != false {
		t.Errorf("type = %v, additionalProperties = %v", s["type"], s["additionalProperties"])
	}
	for _, key := range []string{"oneOf", "anyOf", "allOf", "$schema"} {
		if _, ok := s[key]; ok {
			t.Errorf("the schema has %s at the top level", key)
		}
	}
	if required, _ := s["required"].([]string); len(required) != 1 || required[0] != "expected_hash" {
		t.Errorf("required = %v, want [expected_hash]", s["required"])
	}
}

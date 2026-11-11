package outschema

import (
	"strings"
	"testing"
)

func TestCompileRejects(t *testing.T) {
	for name, tc := range map[string]struct {
		doc  map[string]any
		want string
	}{
		"not an object":      {map[string]any{"type": "string"}, "type: object"},
		"no type":            {map[string]any{"properties": map[string]any{}}, "type: object"},
		"bad keyword":        {map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "strng"}}}, "/properties/a/type"},
		"remote ref":         {map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"$ref": "https://example.com/x.json"}}}, "remote"},
		"file ref":           {map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"$ref": "file:///etc/passwd"}}}, "remote"},
		"missing local":      {map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"$ref": "#/$defs/nope"}}}, "nope"},
		"unknown metaschema": {map[string]any{"$schema": "https://example.com/meta", "type": "object"}, "meta"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Compile(tc.doc)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Compile error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestLocalRefAndValidationMessages(t *testing.T) {
	s, err := Compile(map[string]any{
		"type":     "object",
		"required": []any{"priority", "urls"},
		"$defs":    map[string]any{"url": map[string]any{"type": "string", "minLength": 1}},
		"properties": map[string]any{
			"priority": map[string]any{"enum": []any{"urgent", "normal"}},
			"urls":     map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/url"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate([]byte(`{"priority":"urgent","urls":["a"]}`)); err != nil {
		t.Fatalf("valid call rejected: %v", err)
	}
	err = s.Validate([]byte(`{"priority":"meh","urls":["a",2]}`))
	if err == nil {
		t.Fatal("invalid call accepted")
	}
	for _, want := range []string{"/priority:", "/urls/1:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	err = s.Validate([]byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "priority") || !strings.Contains(err.Error(), "(root)") {
		t.Errorf("missing required fields: %v", err)
	}
	if err := s.Validate([]byte(`not json`)); err == nil {
		t.Error("non-JSON accepted")
	}
}

func TestNoteSchema(t *testing.T) {
	n := Note()
	if err := n.Validate([]byte(`{"title":"t","summary_md":"s","links":[{"label":"l","url":"u"}]}`)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"title":"  ","summary_md":"s"}`, `{"title":"t"}`, `{"title":"t","summary_md":"s","priority":"meh"}`, `{"title":"t","summary_md":"s","extra":1}`} {
		if err := n.Validate([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

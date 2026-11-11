package outschema

import "testing"

func TestMinimalValidates(t *testing.T) {
	obj := func(props map[string]any, req ...any) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": req}
	}
	for name, tc := range map[string]struct {
		doc  map[string]any
		want string
	}{
		"required only": {obj(map[string]any{
			"title": map[string]any{"type": "string"}, "extra": map[string]any{"type": "string"}}, "title"), `{"title":"title"}`},
		"enum first":             {obj(map[string]any{"p": map[string]any{"type": "string", "enum": []any{"low", "high"}}}, "p"), `{"p":"low"}`},
		"const beats default":    {obj(map[string]any{"p": map[string]any{"const": "x", "default": "y"}}, "p"), `{"p":"x"}`},
		"default beats examples": {obj(map[string]any{"p": map[string]any{"type": "string", "default": "d", "examples": []any{"e"}}}, "p"), `{"p":"d"}`},
		"examples":               {obj(map[string]any{"p": map[string]any{"type": "string", "examples": []any{"e", "f"}}}, "p"), `{"p":"e"}`},
		"scalars": {obj(map[string]any{
			"n": map[string]any{"type": "integer", "minimum": 3}, "f": map[string]any{"type": "number"},
			"b": map[string]any{"type": "boolean"}}, "n", "f", "b"), `{"b":false,"f":0,"n":3}`},
		"arrays": {obj(map[string]any{
			"a": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"m": map[string]any{"type": "array", "minItems": 2, "items": map[string]any{"type": "integer"}}}, "a", "m"), `{"a":[],"m":[0,0]}`},
		"nested object": {obj(map[string]any{"o": obj(map[string]any{"k": map[string]any{"type": "string"}}, "k")}, "o"), `{"o":{"k":"k"}}`},
		"oneOf first": {obj(map[string]any{"v": map[string]any{"oneOf": []any{
			map[string]any{"type": "integer"}, map[string]any{"type": "string"}}}}, "v"), `{"v":0}`},
		"local ref": {map[string]any{"type": "object", "required": []any{"x"},
			"properties": map[string]any{"x": map[string]any{"$ref": "#/$defs/n"}},
			"$defs":      map[string]any{"n": map[string]any{"type": "string", "minLength": 3}}}, `{"x":"xxx"}`},
		"string lengths": {obj(map[string]any{"s": map[string]any{"type": "string", "minLength": 4}}, "s"), `{"s":"sxxx"}`},
		"note schema":    {NoteDoc(), ""},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Minimal(tc.doc)
			if err != nil {
				t.Fatal(err)
			}
			s, err := Compile(tc.doc)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Validate(got); err != nil {
				t.Fatalf("Minimal = %s does not validate: %v", got, err)
			}
			if tc.want != "" && string(got) != tc.want {
				t.Fatalf("Minimal = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestMinimalRecursiveRefFails(t *testing.T) {
	doc := map[string]any{"type": "object", "required": []any{"a"}, "properties": map[string]any{"a": map[string]any{"$ref": "#"}}}
	if _, err := Minimal(doc); err == nil {
		t.Fatal("want an error for a required self-reference")
	}
}

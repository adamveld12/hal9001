package outschema

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// maxMinimalDepth bounds $ref and nesting expansion so a recursive schema ends in an error instead of a loop.
const maxMinimalDepth = 32

// Minimal builds the smallest payload that satisfies the schema doc, for flow tests that simulate an agent's output:
// only required properties; a const, else a default, else the first example, else the first enum value; a string is
// its property name (or "text"); a number is its minimum (else 0); a boolean is false; an array is empty (or minItems
// placeholders); an object has its required properties; oneOf and anyOf take their first branch; local $ref resolves.
func Minimal(doc map[string]any) (json.RawMessage, error) {
	if doc == nil {
		return nil, errors.New("the schema is empty")
	}
	// Normalise to what encoding/json produces (float64, []any, map[string]any), whatever Go types doc was built from.
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("the schema is not JSON: %w", err)
	}
	var norm map[string]any
	if err := json.Unmarshal(raw, &norm); err != nil {
		return nil, err
	}
	v, err := minimalValue(norm, norm, "text", 0)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

func minimalValue(root, s map[string]any, name string, depth int) (any, error) {
	if depth > maxMinimalDepth {
		return nil, errors.New("the schema nests or recurses too deeply to build a minimal payload")
	}
	for {
		ref, ok := s["$ref"].(string)
		if !ok {
			break
		}
		target, err := resolveRef(root, ref)
		if err != nil {
			return nil, err
		}
		merged := map[string]any{}
		for k, v := range target {
			merged[k] = v
		}
		for k, v := range s {
			if k != "$ref" {
				merged[k] = v
			}
		}
		s = merged
		if depth++; depth > maxMinimalDepth {
			return nil, errors.New("the schema nests or recurses too deeply to build a minimal payload")
		}
	}
	if v, ok := s["const"]; ok {
		return v, nil
	}
	if v, ok := s["default"]; ok {
		return v, nil
	}
	if ex, ok := s["examples"].([]any); ok && len(ex) > 0 {
		return ex[0], nil
	}
	if en, ok := s["enum"].([]any); ok && len(en) > 0 {
		return en[0], nil
	}
	for _, k := range []string{"oneOf", "anyOf"} {
		if br, ok := s[k].([]any); ok && len(br) > 0 {
			b, ok := br[0].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s: the first branch is not a schema", k)
			}
			return minimalValue(root, b, name, depth+1)
		}
	}
	if all, ok := s["allOf"].([]any); ok && len(all) > 0 {
		merged := map[string]any{}
		for k, v := range s {
			if k != "allOf" {
				merged[k] = v
			}
		}
		for _, b := range all {
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if bm["$ref"] != nil {
				t, err := resolveRef(root, bm["$ref"].(string))
				if err != nil {
					return nil, err
				}
				bm = t
			}
			for k, v := range bm {
				if _, set := merged[k]; !set {
					merged[k] = v
				}
			}
		}
		return minimalValue(root, merged, name, depth+1)
	}
	switch typeOf(s) {
	case "object":
		out := map[string]any{}
		props, _ := s["properties"].(map[string]any)
		req, _ := s["required"].([]any)
		for _, r := range req {
			key, ok := r.(string)
			if !ok {
				continue
			}
			sub, ok := props[key].(map[string]any)
			if !ok {
				out[key] = "text"
				if key != "" {
					out[key] = key
				}
				continue
			}
			v, err := minimalValue(root, sub, key, depth+1)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			out[key] = v
		}
		return out, nil
	case "array":
		n := 0
		if f, ok := s["minItems"].(float64); ok && f > 0 {
			n = int(f)
		}
		items := make([]any, 0, n)
		if n > 0 {
			sub, _ := s["items"].(map[string]any)
			if sub == nil {
				sub = map[string]any{}
			}
			for i := 0; i < n; i++ {
				v, err := minimalValue(root, sub, name, depth+1)
				if err != nil {
					return nil, err
				}
				items = append(items, v)
			}
		}
		return items, nil
	case "string":
		return minimalString(s, name), nil
	case "integer", "number":
		return minimalNumber(s, typeOf(s) == "integer"), nil
	case "boolean":
		return false, nil
	case "null":
		return nil, nil
	}
	return minimalString(s, name), nil
}

// typeOf is the first non-null type of a schema, inferred from its keywords when there is no type.
func typeOf(s map[string]any) string {
	switch t := s["type"].(type) {
	case string:
		return t
	case []any:
		for _, x := range t {
			if str, ok := x.(string); ok && str != "null" {
				return str
			}
		}
		if len(t) > 0 {
			return "null"
		}
	}
	switch {
	case s["properties"] != nil || s["required"] != nil:
		return "object"
	case s["items"] != nil:
		return "array"
	}
	return ""
}

func minimalString(s map[string]any, name string) string {
	v := name
	if v == "" {
		v = "text"
	}
	switch s["format"] {
	case "email":
		v = "user@example.com"
	case "uri", "url", "iri":
		v = "https://example.com"
	case "date":
		v = "2000-01-01"
	case "date-time":
		v = "2000-01-01T00:00:00Z"
	case "uuid":
		v = "00000000-0000-4000-8000-000000000000"
	}
	if f, ok := s["minLength"].(float64); ok && int(f) > len(v) {
		v += strings.Repeat("x", int(f)-len(v))
	}
	if f, ok := s["maxLength"].(float64); ok && int(f) < len(v) {
		v = v[:int(f)]
	}
	return v
}

func minimalNumber(s map[string]any, integer bool) any {
	v := 0.0
	if m, ok := s["minimum"].(float64); ok && m > v {
		v = m
	}
	if m, ok := s["exclusiveMinimum"].(float64); ok && m >= v {
		v = m + 1
	}
	if m, ok := s["maximum"].(float64); ok && m < v {
		v = m
	}
	if integer {
		return int64(math.Ceil(v))
	}
	return v
}

// resolveRef follows a local "#/..." JSON pointer.
func resolveRef(root map[string]any, ref string) (map[string]any, error) {
	if ref == "#" {
		return root, nil
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("unsupported $ref %q", ref)
	}
	var cur any = root
	for _, part := range strings.Split(ref[2:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("$ref %q does not resolve", ref)
		}
		cur = m[part]
	}
	m, ok := cur.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("$ref %q does not resolve", ref)
	}
	return m, nil
}

package spec

import (
	"fmt"
	"sort"

	"go.yaml.in/yaml/v3"
)

// JSONSchema is a JSON Schema document (the schema of an agent node port) as decoded from YAML or JSON.
type JSONSchema map[string]any

// schemaKeyOrder is the order the common keywords are written in; any other keyword follows alphabetically.
var schemaKeyOrder = map[string]int{
	"$schema": 0, "$id": 1, "title": 2, "description": 3, "type": 4, "enum": 5, "const": 6, "format": 7,
	"required": 8, "properties": 9, "patternProperties": 10, "items": 11, "additionalProperties": 12,
}

// schemaNameMaps are keywords whose value maps names to schemas; those names are not keywords.
var schemaNameMaps = map[string]bool{"properties": true, "patternProperties": true, "$defs": true, "definitions": true, "dependentSchemas": true}

// MarshalYAML writes keywords in a readable order (type, required, properties, ...) instead of alphabetically.
func (s JSONSchema) MarshalYAML() (any, error) {
	n, err := schemaNode(map[string]any(s), false)
	if err != nil {
		return nil, err
	}
	return n, nil
}

func schemaNode(m map[string]any, names bool) (*yaml.Node, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	rank := func(k string) int {
		if names {
			return 0
		}
		if r, ok := schemaKeyOrder[k]; ok {
			return r
		}
		if k == "$defs" || k == "definitions" {
			return 1000
		}
		return 100
	}
	sort.Slice(keys, func(i, j int) bool {
		if ri, rj := rank(keys[i]), rank(keys[j]); ri != rj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})
	out := &yaml.Node{Kind: yaml.MappingNode}
	for _, k := range keys {
		vn, err := valueNode(k, m[k], names)
		if err != nil {
			return nil, err
		}
		out.Content = append(out.Content, strNode(k), vn)
	}
	return out, nil
}

// valueNode encodes v, the value of key k. In a names map (names true) every value is a schema; under a
// schema, the values of schemaNameMaps keywords are names maps and the rest is plain data or a sub-schema.
func valueNode(k string, v any, inNames bool) (*yaml.Node, error) {
	switch t := v.(type) {
	case map[string]any:
		return schemaNode(t, !inNames && schemaNameMaps[k])
	case []any:
		seq := &yaml.Node{Kind: yaml.SequenceNode}
		for _, e := range t {
			en, err := valueNode("", e, false)
			if err != nil {
				return nil, err
			}
			seq.Content = append(seq.Content, en)
		}
		if len(t) > 0 && allScalars(t) && len(t) <= 6 {
			seq.Style = yaml.FlowStyle
		}
		return seq, nil
	default:
		n := &yaml.Node{}
		if err := n.Encode(v); err != nil {
			return nil, fmt.Errorf("schema.%s: %w", k, err)
		}
		return n, nil
	}
}

func allScalars(s []any) bool {
	for _, e := range s {
		switch e.(type) {
		case map[string]any, []any:
			return false
		}
	}
	return true
}

// UnmarshalYAML decodes into a plain map so that nested objects are map[string]any too (yaml.v3 would otherwise use
// JSONSchema for them, and the schema code expects the plain type).
func (s *JSONSchema) UnmarshalYAML(n *yaml.Node) error {
	var m map[string]any
	if err := n.Decode(&m); err != nil {
		return err
	}
	*s = JSONSchema(m)
	return nil
}

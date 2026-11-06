package transform

import "go.yaml.in/yaml/v3"

// Conditions and set values are written as one-line flow mappings so a step
// list stays short and readable in a flow file:
//
//	- type: filter
//	  conditions:
//	    - {field: severity, op: not_equals, value: noise}

type (
	conditionAlias Condition
	valueAlias     Value
)

func flowStyle(v any) (any, error) {
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		return nil, err
	}
	n.Style |= yaml.FlowStyle
	return &n, nil
}

// MarshalYAML writes a condition as {field: a, op: equals, value: b}.
func (c Condition) MarshalYAML() (any, error) { return flowStyle(conditionAlias(c)) }

// MarshalYAML writes a value as {kind: constant, value: x}.
func (v Value) MarshalYAML() (any, error) { return flowStyle(valueAlias(v)) }

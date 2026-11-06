package spec

import (
	"encoding/json"
	"fmt"

	"go.yaml.in/yaml/v3"
)

// ToolRef is one entry of an agent node's tools: a custom tool id, optionally with an approval setting that
// overrides the tool's own. Plain `id` and `{id, approve}` are both accepted; without approve it is written as a
// plain string.
type ToolRef struct {
	ID      string
	Approve *ToolApprove
}

// Clone returns a copy sharing no pointers with r.
func (r ToolRef) Clone() ToolRef {
	if r.Approve != nil {
		a := *r.Approve
		if a.When != nil {
			w := *a.When
			w.Conditions = append(w.Conditions[:0:0], w.Conditions...)
			a.When = &w
		}
		r.Approve = &a
	}
	return r
}

type toolRefObject struct {
	ID      string       `yaml:"id" json:"id"`
	Approve *ToolApprove `yaml:"approve,omitempty" json:"approve,omitempty"`
}

// MarshalYAML implements yaml.Marshaler.
func (r ToolRef) MarshalYAML() (any, error) {
	if r.Approve == nil {
		return r.ID, nil
	}
	return toolRefObject(r), nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (r *ToolRef) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		var s string
		if err := n.Decode(&s); err != nil {
			return err
		}
		*r = ToolRef{ID: s}
		return nil
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if k := n.Content[i].Value; k != "id" && k != "approve" {
				return fmt.Errorf("line %d: tools entry: unknown field %q (id and approve only)", n.Content[i].Line, k)
			}
		}
		var o toolRefObject
		if err := n.Decode(&o); err != nil {
			return err
		}
		*r = ToolRef(o)
		return nil
	}
	return fmt.Errorf("line %d: a tools entry is a tool id or {id, approve}", n.Line)
}

// MarshalJSON implements json.Marshaler.
func (r ToolRef) MarshalJSON() ([]byte, error) {
	if r.Approve == nil {
		return json.Marshal(r.ID)
	}
	return json.Marshal(toolRefObject(r))
}

// UnmarshalJSON implements json.Unmarshaler.
func (r *ToolRef) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*r = ToolRef{ID: s}
		return nil
	}
	var o toolRefObject
	if err := json.Unmarshal(b, &o); err != nil {
		return fmt.Errorf("a tools entry is a tool id or {id, approve}")
	}
	*r = ToolRef(o)
	return nil
}

package spec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"go.yaml.in/yaml/v3"
)

// Emit modes of an agent node with declared ports (Node.Emit).
const (
	// EmitOne: the last valid emit_<port> call of the run is the node's single emission.
	EmitOne = "one"
	// EmitMany: every valid emit_<port> call is a separate emission, and downstream runs once per emission.
	EmitMany = "many"
)

// PortDecl is one declared port. Command nodes only name their ports; an agent node's port also has a Description
// (the text of its emit_<port> tool) and a Schema (the tool's input schema; empty means the inbox shortcut or an error).
type PortDecl struct {
	Name        string
	Description string
	Schema      JSONSchema
}

// Ports is the `ports` field of a node. A command node lists names (`ports: [ok, skip]`); an agent node maps
// names to definitions (`ports: {reviewed: {description: ..., schema: {...}}}`). Map records which of the two forms
// was read or is to be written; declaration order is kept in both.
type Ports struct {
	Decls []PortDecl
	// Map is the agent (definition) form.
	Map bool
}

// NamePorts is the list form with the given names.
func NamePorts(names ...string) Ports {
	p := Ports{}
	for _, n := range names {
		p.Decls = append(p.Decls, PortDecl{Name: n})
	}
	return p
}

// IsZero reports that no port is declared (the field is then omitted when written).
func (p Ports) IsZero() bool { return len(p.Decls) == 0 }

// Names lists the declared port names in order.
func (p Ports) Names() []string {
	if len(p.Decls) == 0 {
		return nil
	}
	out := make([]string, len(p.Decls))
	for i, d := range p.Decls {
		out[i] = d.Name
	}
	return out
}

// Get returns the declaration of the named port.
func (p Ports) Get(name string) (PortDecl, bool) {
	for _, d := range p.Decls {
		if d.Name == name {
			return d, true
		}
	}
	return PortDecl{}, false
}

// Clone returns a deep copy.
func (p Ports) Clone() Ports {
	if p.Decls == nil {
		return p
	}
	out := Ports{Map: p.Map, Decls: slices.Clone(p.Decls)}
	for i := range out.Decls {
		out.Decls[i].Schema = cloneSchema(out.Decls[i].Schema)
	}
	return out
}

func cloneSchema(s JSONSchema) JSONSchema {
	if s == nil {
		return nil
	}
	return JSONSchema(cloneAny(map[string]any(s)).(map[string]any))
}

func cloneAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, e := range t {
			m[k] = cloneAny(e)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, e := range t {
			s[i] = cloneAny(e)
		}
		return s
	}
	return v
}

// portBody is the value of one entry of the map form.
type portBody struct {
	Description string     `yaml:"description,omitempty" json:"description,omitempty"`
	Schema      JSONSchema `yaml:"schema,omitempty" json:"schema,omitempty"`
}

// UnmarshalYAML reads a sequence of names or a mapping of definitions.
func (p *Ports) UnmarshalYAML(n *yaml.Node) error {
	*p = Ports{}
	switch n.Kind {
	case yaml.SequenceNode:
		var names []string
		if err := n.Decode(&names); err != nil {
			return err
		}
		*p = NamePorts(names...)
		return nil
	case yaml.MappingNode:
		p.Map = true
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			d := PortDecl{Name: k.Value}
			if v.Kind != yaml.ScalarNode || v.Tag != "!!null" {
				if v.Kind != yaml.MappingNode {
					return fmt.Errorf("line %d: port %q must be a mapping with description and schema", v.Line, k.Value)
				}
				for j := 0; j+1 < len(v.Content); j += 2 {
					if f := v.Content[j].Value; f != "description" && f != "schema" {
						return fmt.Errorf("line %d: field %q not allowed in port %q (use description and schema)", v.Content[j].Line, f, k.Value)
					}
				}
				var b portBody
				if err := v.Decode(&b); err != nil {
					return err
				}
				d.Description, d.Schema = b.Description, b.Schema
			}
			p.Decls = append(p.Decls, d)
		}
		return nil
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			return nil
		}
	}
	return fmt.Errorf("line %d: ports must be a list of names or a mapping of port definitions", n.Line)
}

// MarshalYAML writes the form that was read.
func (p Ports) MarshalYAML() (any, error) {
	if !p.Map {
		return p.Names(), nil
	}
	out := &yaml.Node{Kind: yaml.MappingNode}
	for _, d := range p.Decls {
		body := &yaml.Node{Kind: yaml.MappingNode}
		if d.Description != "" {
			body.Content = append(body.Content, strNode("description"), strNode(d.Description))
		}
		if len(d.Schema) > 0 {
			sn, err := schemaNode(map[string]any(d.Schema), false)
			if err != nil {
				return nil, err
			}
			body.Content = append(body.Content, strNode("schema"), sn)
		}
		if len(body.Content) == 0 {
			body.Style = yaml.FlowStyle
		}
		out.Content = append(out.Content, strNode(d.Name), body)
	}
	return out, nil
}

// UnmarshalJSON reads an array of names or an object of definitions (in key order).
func (p *Ports) UnmarshalJSON(b []byte) error {
	*p = Ports{}
	b = bytes.TrimSpace(b)
	switch {
	case bytes.Equal(b, []byte("null")):
		return nil
	case len(b) > 0 && b[0] == '[':
		var names []string
		if err := json.Unmarshal(b, &names); err != nil {
			return err
		}
		*p = NamePorts(names...)
		return nil
	case len(b) > 0 && b[0] == '{':
		p.Map = true
		dec := json.NewDecoder(bytes.NewReader(b))
		if _, err := dec.Token(); err != nil {
			return err
		}
		for dec.More() {
			t, err := dec.Token()
			if err != nil {
				return err
			}
			name, _ := t.(string)
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return err
			}
			d := PortDecl{Name: name}
			if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				var body portBody
				bd := json.NewDecoder(bytes.NewReader(raw))
				bd.DisallowUnknownFields()
				if err := bd.Decode(&body); err != nil {
					return fmt.Errorf("port %q: %w", name, err)
				}
				d.Description, d.Schema = body.Description, body.Schema
			}
			p.Decls = append(p.Decls, d)
		}
		return nil
	}
	return errors.New("ports must be an array of names or an object of port definitions")
}

// MarshalJSON writes the form that was read, keeping the order of the declarations.
func (p Ports) MarshalJSON() ([]byte, error) {
	if !p.Map {
		names := p.Names()
		if names == nil {
			names = []string{}
		}
		return json.Marshal(names)
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, d := range p.Decls {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, _ := json.Marshal(d.Name)
		v, err := json.Marshal(portBody{Description: d.Description, Schema: d.Schema})
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

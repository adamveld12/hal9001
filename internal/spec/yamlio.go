package spec

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

var (
	typeErrLineRE  = regexp.MustCompile(`^line (\d+): (.*)$`)
	syntaxLineRE   = regexp.MustCompile(`line (\d+)`)
	unknownFieldRE = regexp.MustCompile(`^field (\S+) not found in type (\S+)$`)
	quotedRE       = regexp.MustCompile("[`\"]([^`\"]+)[`\"]")
)

// decodeYAML strict-decodes the first YAML document of b into out. Unknown
// fields and type mismatches are reported as diagnostics while the remaining
// fields are still decoded into out. empty reports a document with no
// content; fatal reports a syntax error after which out must not be used.
func decodeYAML(b []byte, out any) (ds []Diagnostic, empty, fatal bool) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	err := dec.Decode(out)
	if err == nil {
		return nil, false, false
	}
	if errors.Is(err, io.EOF) {
		return nil, true, false
	}
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		msg := strings.TrimPrefix(err.Error(), "yaml: ")
		line := 0
		if m := syntaxLineRE.FindStringSubmatch(msg); m != nil {
			line, _ = strconv.Atoi(m[1])
		}
		return []Diagnostic{{Line: line, Severity: SeverityError, Code: CodeYAMLSyntax, Message: msg}}, false, true
	}
	var root yaml.Node
	rootOK := yaml.Unmarshal(b, &root) == nil
	for _, e := range te.Errors {
		d := Diagnostic{Severity: SeverityError, Code: CodeYAMLType, Message: e}
		if m := typeErrLineRE.FindStringSubmatch(e); m != nil {
			d.Line, _ = strconv.Atoi(m[1])
			d.Message = m[2]
		}
		key := ""
		if m := unknownFieldRE.FindStringSubmatch(d.Message); m != nil {
			d.Code = CodeUnknownField
			key = m[1]
			d.Message = fmt.Sprintf("unknown field %q", key)
		}
		if rootOK && d.Line > 0 {
			loc := locator{line: d.Line, key: key}
			if m := quotedRE.FindStringSubmatch(d.Message); m != nil && key == "" {
				loc.hint = m[1]
			}
			d.Path = pathAtLine(&root, loc)
		}
		ds = append(ds, d)
	}
	return ds, false, false
}

// locator identifies a YAML element for error reporting. key matches a
// mapping key by name; hint matches a scalar value; both only on line.
type locator struct {
	line      int
	key, hint string
}

// pathAtLine returns the dotted path of the element on loc.line. Flow-style
// mappings put many elements on one line, so the key or the offending value
// (hint) disambiguates; without a match the first element on the line wins.
func pathAtLine(root *yaml.Node, loc locator) string {
	n := root
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	if loc.hint != "" {
		if p, ok := walkPath(n, "", loc); ok {
			return p
		}
		loc.hint = ""
	}
	p, _ := walkPath(n, "", loc)
	return p
}

func walkPath(n *yaml.Node, prefix string, loc locator) (string, bool) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			p := k.Value
			if prefix != "" {
				p = prefix + "." + k.Value
			}
			var match bool
			switch {
			case loc.key != "":
				match = k.Line == loc.line && k.Value == loc.key
			case loc.hint != "":
				match = v.Kind == yaml.ScalarNode && v.Line == loc.line && v.Value == loc.hint
			default:
				match = k.Line == loc.line
			}
			if match {
				return p, true
			}
			if sub, ok := walkPath(v, p, loc); ok {
				return sub, true
			}
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			p := fmt.Sprintf("%s[%d]", prefix, i)
			if sub, ok := walkPath(c, p, loc); ok {
				return sub, true
			}
			if loc.key == "" && c.Line == loc.line && (loc.hint == "" || c.Kind == yaml.ScalarNode && c.Value == loc.hint) {
				return p, true
			}
		}
	}
	return "", false
}

// encodeYAML renders v with two-space indentation.
func encodeYAML(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func strNode(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

func numNode(f float64) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: strconv.FormatFloat(f, 'f', -1, 64)}
}

// flowMap builds a flow-style mapping ({a: b, c: d}) from alternating key and
// value nodes.
func flowMap(kv ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle, Content: kv}
}

// MarshalYAML writes an edge as {from: a, port: p, to: b}.
func (e Edge) MarshalYAML() (any, error) {
	kv := []*yaml.Node{strNode("from"), strNode(e.From)}
	if e.Port != "" {
		kv = append(kv, strNode("port"), strNode(e.Port))
	}
	kv = append(kv, strNode("to"), strNode(e.To))
	return flowMap(kv...), nil
}

// MarshalYAML writes a position as {x: 1, y: 2}.
func (u UI) MarshalYAML() (any, error) {
	return flowMap(strNode("x"), numNode(u.X), strNode("y"), numNode(u.Y)), nil
}

// MarshalYAML writes a directory cwd as a plain string and everything else as
// a flow mapping.
func (c Cwd) MarshalYAML() (any, error) {
	if c.Mode == CwdDir && c.Repo == "" && c.Ref == "" {
		return c.Path, nil
	}
	kv := []*yaml.Node{strNode("mode"), strNode(c.Mode)}
	for _, f := range []struct{ k, v string }{{"path", c.Path}, {"repo", c.Repo}, {"ref", c.Ref}} {
		if f.v != "" {
			kv = append(kv, strNode(f.k), strNode(f.v))
		}
	}
	return flowMap(kv...), nil
}

// UnmarshalYAML accepts a string (directory path) or a mapping. Unknown keys
// are reported as a non-fatal *yaml.TypeError because yaml.Node.Decode does
// not inherit the decoder's strict mode.
func (c *Cwd) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*c = Cwd{Mode: CwdDir, Path: n.Value}
		return nil
	case yaml.MappingNode:
		*c = Cwd{}
		var errs []string
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			var dst *string
			switch k.Value {
			case "mode":
				dst = &c.Mode
			case "path":
				dst = &c.Path
			case "repo":
				dst = &c.Repo
			case "ref":
				dst = &c.Ref
			default:
				errs = append(errs, fmt.Sprintf("line %d: field %s not found in type spec.Cwd", k.Line, k.Value))
				continue
			}
			if v.Kind != yaml.ScalarNode {
				errs = append(errs, fmt.Sprintf("line %d: cwd.%s must be a string", v.Line, k.Value))
				continue
			}
			*dst = v.Value
		}
		if len(errs) > 0 {
			return &yaml.TypeError{Errors: errs}
		}
		return nil
	default:
		return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: cwd must be a path string or a mapping with mode, path, repo, ref", n.Line)}}
	}
}

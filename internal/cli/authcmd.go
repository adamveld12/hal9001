package cli

import (
	"bytes"
	"errors"
	"fmt"

	"go.yaml.in/yaml/v3"

	"hal9001/internal/daemon"
	"hal9001/internal/spec"
)

// writePasswordHash stores hash as the `hash` option of every `password`
// module in config.yaml's auth stacks and returns how many entries it set.
// The file is edited as a YAML tree so comments and layout survive; when
// config.yaml has no auth section the default stack is added. The write goes
// through spec.WriteConfig and fails on a concurrent edit.
func writePasswordHash(home, hash string) (int, error) {
	src, onDisk, err := spec.ReadFile(home, spec.KindConfig, "")
	if err != nil && !errors.Is(err, spec.ErrNotFound) {
		return 0, err
	}
	out, n, err := setPasswordHashYAML(src, hash)
	if err != nil {
		return 0, err
	}
	if _, ds := spec.CheckConfig(out); spec.HasErrors(ds) {
		return 0, fmt.Errorf("config.yaml would be invalid after the change: %s", ds[0])
	}
	if _, err := spec.WriteConfig(home, out, onDisk); err != nil {
		return 0, err
	}
	return n, nil
}

func setPasswordHashYAML(src []byte, hash string) ([]byte, int, error) {
	var doc yaml.Node
	if len(bytes.TrimSpace(src)) > 0 {
		if err := yaml.Unmarshal(src, &doc); err != nil {
			return nil, 0, fmt.Errorf("config.yaml is not valid YAML: %w", err)
		}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, 0, errors.New("config.yaml: the top level must be a mapping")
	}

	authNode := mapValue(root, "auth")
	if authNode == nil {
		authNode = setMapValue(root, "auth", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
	}
	if authNode.Kind != yaml.MappingNode {
		return nil, 0, errors.New("config.yaml: auth must be a mapping")
	}
	stacks := mapValue(authNode, "stacks")
	if stacks == nil {
		var def yaml.Node
		if err := def.Encode(spec.DefaultAuth().Stacks); err != nil {
			return nil, 0, err
		}
		stacks = setMapValue(authNode, "stacks", &def)
	}
	if stacks.Kind != yaml.MappingNode {
		return nil, 0, errors.New("config.yaml: auth.stacks must be a mapping")
	}

	n := 0
	for i := 1; i < len(stacks.Content); i += 2 {
		for _, entry := range stacks.Content[i].Content {
			if entry.Kind != yaml.MappingNode {
				continue
			}
			if m := mapValue(entry, "module"); m == nil || m.Value != "password" {
				continue
			}
			opts := mapValue(entry, "options")
			if opts == nil {
				opts = setMapValue(entry, "options", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
			}
			if opts.Kind != yaml.MappingNode {
				return nil, 0, errors.New("config.yaml: a password entry's options must be a mapping")
			}
			setMapValue(opts, daemon.PasswordHashOption, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: hash})
			n++
		}
	}
	if n == 0 {
		return nil, 0, errors.New("config.yaml has no `password` module in any auth stack; add {module: password, control: sufficient} first")
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, 0, err
	}
	if err := enc.Close(); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), n, nil
}

// mapValue returns the value node stored under key in a mapping node, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// setMapValue sets key to val in a mapping node (replacing the value of an
// existing key) and returns the stored node.
func setMapValue(m *yaml.Node, key string, val *yaml.Node) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = val
			return val
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val)
	return val
}

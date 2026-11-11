package spec

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// EditConfigYAML parses raw (config.yaml; empty means an empty mapping), applies edit to the top-level mapping and
// returns the encoded result. Comments and key order are kept. When edit reports no change the original bytes are
// returned unchanged, so a no-op never rewrites the file.
func EditConfigYAML(raw []byte, edit func(root *yaml.Node) (changed bool, err error)) ([]byte, bool, error) {
	var doc yaml.Node
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil, false, fmt.Errorf("config.yaml does not parse: %w", err)
		}
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, false, errors.New("config.yaml must be a mapping at the top level")
	}
	changed, err := edit(root)
	if err != nil || !changed {
		return raw, false, err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, false, err
	}
	if err := enc.Close(); err != nil {
		return nil, false, err
	}
	return buf.Bytes(), true, nil
}

// AddAllowedHost appends host to `allowed_hosts` unless an entry equal to it (ignoring case) is there. It reports
// whether it changed anything. Existing entries and their comments stay as they are.
func AddAllowedHost(root *yaml.Node, host string) (bool, error) {
	i := mapKeyIndex(root, "allowed_hosts")
	if i < 0 {
		root.Content = append(root.Content, strNode("allowed_hosts"),
			&yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{strNode(host)}})
		return true, nil
	}
	seq := root.Content[i+1]
	switch {
	case seq.Kind == yaml.ScalarNode && seq.Tag == "!!null":
		root.Content[i+1] = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{strNode(host)}, LineComment: seq.LineComment}
		return true, nil
	case seq.Kind != yaml.SequenceNode:
		return false, errors.New("allowed_hosts must be a list")
	}
	for _, e := range seq.Content {
		if strings.EqualFold(strings.TrimSpace(e.Value), host) {
			return false, nil
		}
	}
	seq.Content = append(seq.Content, strNode(host))
	return true, nil
}

// RemoveAllowedHost removes every entry equal to host (ignoring case) from `allowed_hosts`. It reports whether it
// removed any.
func RemoveAllowedHost(root *yaml.Node, host string) bool {
	seq := mapValue(root, "allowed_hosts")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return false
	}
	kept := seq.Content[:0:0]
	for _, e := range seq.Content {
		if !strings.EqualFold(strings.TrimSpace(e.Value), host) {
			kept = append(kept, e)
		}
	}
	if len(kept) == len(seq.Content) {
		return false
	}
	seq.Content = kept
	return true
}

// SetTailscaleServe sets `tailscale.serve` and `tailscale.https_port`, creating the section when it is missing, and
// reports whether it changed anything.
func SetTailscaleServe(root *yaml.Node, serve bool, httpsPort int) (bool, error) {
	i := mapKeyIndex(root, "tailscale")
	var sec *yaml.Node
	if i < 0 {
		sec = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, strNode("tailscale"), sec)
	} else if sec = root.Content[i+1]; sec.Kind == yaml.ScalarNode && sec.Tag == "!!null" {
		sec = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content[i+1] = sec
	}
	if sec.Kind != yaml.MappingNode {
		return false, errors.New("tailscale must be a mapping")
	}
	a := setScalar(sec, "serve", "!!bool", strconv.FormatBool(serve))
	b := setScalar(sec, "https_port", "!!int", strconv.Itoa(httpsPort))
	return i < 0 || a || b, nil
}

// setScalar sets key in the mapping m to a scalar, keeping the comments of the value it replaces.
func setScalar(m *yaml.Node, key, tag, value string) bool {
	if i := mapKeyIndex(m, key); i >= 0 {
		cur := m.Content[i+1]
		if cur.Kind == yaml.ScalarNode && cur.Value == value && cur.Tag == tag {
			return false
		}
		m.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value, LineComment: cur.LineComment, HeadComment: cur.HeadComment}
		return true
	}
	m.Content = append(m.Content, strNode(key), &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
	return true
}

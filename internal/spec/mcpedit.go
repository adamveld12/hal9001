package spec

import (
	"errors"
	"slices"

	"go.yaml.in/yaml/v3"
)

// SetRemoteMCP creates or updates the `mcp_servers.<name>` entry of config.yaml's top-level mapping to r. Comments and
// key order of the rest of the file, and unknown keys inside an existing entry, are kept. It reports whether anything
// changed.
func SetRemoteMCP(root *yaml.Node, name string, r RemoteMCP) (bool, error) {
	changed := false
	i := mapKeyIndex(root, "mcp_servers")
	var sec *yaml.Node
	switch {
	case i < 0:
		sec = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, strNode("mcp_servers"), sec)
		changed = true
	case root.Content[i+1].Kind == yaml.ScalarNode && root.Content[i+1].Tag == "!!null":
		sec = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content[i+1] = sec
		changed = true
	default:
		sec = root.Content[i+1]
	}
	if sec.Kind != yaml.MappingNode {
		return false, errors.New("mcp_servers must be a mapping")
	}
	j := mapKeyIndex(sec, name)
	var ent *yaml.Node
	if j < 0 {
		ent = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		sec.Content = append(sec.Content, strNode(name), ent)
		changed = true
	} else if ent = sec.Content[j+1]; ent.Kind != yaml.MappingNode {
		return false, errors.New("mcp_servers." + name + " must be a mapping")
	}
	if setScalar(ent, "url", "!!str", r.URL) {
		changed = true
	}
	for _, f := range []struct{ key, val string }{{"auth", r.Auth}, {"client_id", r.ClientID}, {"header", r.Header}} {
		if setOrDropScalar(ent, f.key, f.val) {
			changed = true
		}
	}
	if setOrDropScalars(ent, "scopes", r.Scopes) {
		changed = true
	}
	return changed, nil
}

// RemoveRemoteMCP deletes the `mcp_servers.<name>` entry (and the `mcp_servers` key when it becomes empty) and reports
// whether it was there.
func RemoveRemoteMCP(root *yaml.Node, name string) bool {
	i := mapKeyIndex(root, "mcp_servers")
	if i < 0 || root.Content[i+1].Kind != yaml.MappingNode {
		return false
	}
	sec := root.Content[i+1]
	j := mapKeyIndex(sec, name)
	if j < 0 {
		return false
	}
	sec.Content = slices.Delete(sec.Content, j, j+2)
	if len(sec.Content) == 0 {
		root.Content = slices.Delete(root.Content, i, i+2)
	}
	return true
}

// setOrDropScalar sets key to the string value, or removes it when value is empty.
func setOrDropScalar(m *yaml.Node, key, value string) bool {
	if value != "" {
		return setScalar(m, key, "!!str", value)
	}
	if i := mapKeyIndex(m, key); i >= 0 {
		m.Content = slices.Delete(m.Content, i, i+2)
		return true
	}
	return false
}

// setOrDropScalars sets key to a flow-style sequence of strings, or removes it when values is empty.
func setOrDropScalars(m *yaml.Node, key string, values []string) bool {
	i := mapKeyIndex(m, key)
	if len(values) == 0 {
		if i >= 0 {
			m.Content = slices.Delete(m.Content, i, i+2)
			return true
		}
		return false
	}
	if i >= 0 {
		cur := m.Content[i+1]
		if cur.Kind == yaml.SequenceNode && len(cur.Content) == len(values) {
			same := true
			for k, v := range values {
				if cur.Content[k].Value != v {
					same = false
				}
			}
			if same {
				return false
			}
		}
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, v := range values {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v})
	}
	if i >= 0 {
		seq.HeadComment, seq.LineComment = m.Content[i+1].HeadComment, m.Content[i+1].LineComment
		m.Content[i+1] = seq
	} else {
		m.Content = append(m.Content, strNode(key), seq)
	}
	return true
}

// HasRemoteMCP reports whether `mcp_servers.<name>` exists in config.yaml's top-level mapping.
func HasRemoteMCP(root *yaml.Node, name string) bool {
	i := mapKeyIndex(root, "mcp_servers")
	return i >= 0 && root.Content[i+1].Kind == yaml.MappingNode && mapKeyIndex(root.Content[i+1], name) >= 0
}

package authcfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

	"go.yaml.in/yaml/v3"

	"hal9001/internal/spec"
)

// Apply edits the document root of config.yaml so that its auth section means
// desired. current is the effective auth section of the same document (defaults
// filled in). Stacks and services that do not change are not touched; of a
// changed stack, entries that stay the same keep their YAML node (and comments),
// and an entry whose options change is patched in place. It reports whether it
// changed anything.
func Apply(root *yaml.Node, current, desired spec.Auth) (bool, error) {
	stacksChanged := !equalStacks(current.Stacks, desired.Stacks)
	servicesChanged := !equalServices(current.Services, desired.Services)
	if !stacksChanged && !servicesChanged {
		return false, nil
	}
	authNode, err := childMapping(root, "auth")
	if err != nil {
		return false, err
	}
	if stacksChanged {
		stacks, err := childMapping(authNode, "stacks")
		if err != nil {
			return false, err
		}
		if err := syncStacks(stacks, desired.Stacks); err != nil {
			return false, err
		}
	}
	if servicesChanged {
		services, err := childMapping(authNode, "services")
		if err != nil {
			return false, err
		}
		for _, svc := range spec.Services {
			want := desired.Services[svc]
			if i := keyIndex(services, svc); i >= 0 {
				setStr(services, i, want)
			} else if want != current.Services[svc] {
				services.Content = append(services.Content, strNode(svc), strNode(want))
			}
		}
	}
	return true, nil
}

func equalStacks(a, b map[string][]spec.AuthModule) bool {
	return Equal(spec.Auth{Stacks: a}, spec.Auth{Stacks: b})
}

func equalServices(a, b map[string]string) bool {
	for _, svc := range spec.Services {
		if a[svc] != b[svc] {
			return false
		}
	}
	return true
}

// SetAllowedLogins sets `allowed_logins` to logins and reports whether that
// changed anything. Entries that stay keep their node; an empty list is
// written as `[]`.
func SetAllowedLogins(root *yaml.Node, logins []string) (bool, error) {
	i := keyIndex(root, "allowed_logins")
	if i < 0 {
		if len(logins) == 0 {
			return false, nil
		}
		root.Content = append(root.Content, strNode("allowed_logins"), seqNode(logins))
		return true, nil
	}
	val := root.Content[i+1]
	switch {
	case val.Kind == yaml.ScalarNode && val.Tag == "!!null":
		if len(logins) == 0 {
			return false, nil
		}
		n := seqNode(logins)
		n.LineComment = val.LineComment
		root.Content[i+1] = n
		return true, nil
	case val.Kind != yaml.SequenceNode:
		return false, errors.New("allowed_logins must be a list")
	}
	same := len(val.Content) == len(logins)
	for j := 0; same && j < len(logins); j++ {
		same = val.Content[j].Kind == yaml.ScalarNode && val.Content[j].Value == logins[j]
	}
	if same {
		return false, nil
	}
	used := make([]bool, len(val.Content))
	content := make([]*yaml.Node, 0, len(logins))
	for _, l := range logins {
		j := nextIndex(val.Content, used, l)
		if j >= 0 {
			used[j] = true
			content = append(content, val.Content[j])
		} else {
			content = append(content, strNode(l))
		}
	}
	val.Content = content
	if len(content) == 0 {
		val.Style = yaml.FlowStyle
	} else {
		val.Style = 0
	}
	return true, nil
}

// nextIndex returns the index of the first unused scalar node equal to value, or -1.
func nextIndex(nodes []*yaml.Node, used []bool, value string) int {
	for j, n := range nodes {
		if !used[j] && n.Kind == yaml.ScalarNode && n.Value == value {
			return j
		}
	}
	return -1
}

// SetPassword returns raw (config.yaml; empty means no file yet) with hash
// stored as the `hash` option of every password entry in the auth stacks, and
// the number of such entries. When no stack a service uses has a password
// entry, `{module: password, control: sufficient}` is added to each of them
// (which turns password sign-in on); when config.yaml has no auth section the
// default stacks are written first. Comments and layout survive.
func SetPassword(raw []byte, hash string) ([]byte, int, error) {
	cfg, ds := spec.CheckConfig(raw)
	if cfg == nil {
		return nil, 0, fmt.Errorf("config.yaml is not valid: %s", ds[0])
	}
	desired := WithPasswordHash(cfg.Auth, hash)
	out, _, err := spec.EditConfigYAML(raw, func(root *yaml.Node) (bool, error) {
		return Apply(root, cfg.Auth, desired)
	})
	if err != nil {
		return nil, 0, err
	}
	return out, PasswordEntries(desired), nil
}

// ---- yaml helpers ----

func strNode(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

func seqNode(values []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, v := range values {
		n.Content = append(n.Content, strNode(v))
	}
	return n
}

// keyIndex returns the index of key's key node in the mapping m, or -1.
func keyIndex(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// setStr replaces the scalar under the key node at index i, keeping its comments.
func setStr(m *yaml.Node, i int, value string) {
	cur := m.Content[i+1]
	if cur.Kind == yaml.ScalarNode && cur.Value == value && cur.Tag == "!!str" {
		return
	}
	n := strNode(value)
	n.LineComment, n.HeadComment = cur.LineComment, cur.HeadComment
	m.Content[i+1] = n
}

// childMapping returns the mapping stored under key in the mapping parent,
// creating it (or replacing an empty value) when it is missing.
func childMapping(parent *yaml.Node, key string) (*yaml.Node, error) {
	i := keyIndex(parent, key)
	if i < 0 {
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		parent.Content = append(parent.Content, strNode(key), n)
		return n, nil
	}
	n := parent.Content[i+1]
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", LineComment: n.LineComment}
		parent.Content[i+1] = m
		return m, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config.yaml: %s must be a mapping", key)
	}
	return n, nil
}

// syncStacks makes the stacks mapping hold exactly want: stacks that are gone
// are removed, existing ones are synced entry by entry, new ones are appended
// in name order.
func syncStacks(stacks *yaml.Node, want map[string][]spec.AuthModule) error {
	kept := make([]*yaml.Node, 0, len(stacks.Content))
	seen := map[string]bool{}
	for i := 0; i+1 < len(stacks.Content); i += 2 {
		name := stacks.Content[i].Value
		entries, ok := want[name]
		if !ok {
			continue
		}
		seen[name] = true
		val, err := syncEntries(stacks.Content[i+1], entries)
		if err != nil {
			return fmt.Errorf("config.yaml: auth.stacks.%s: %w", name, err)
		}
		kept = append(kept, stacks.Content[i], val)
	}
	names := make([]string, 0, len(want))
	for name := range want {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		val, err := syncEntries(nil, want[name])
		if err != nil {
			return fmt.Errorf("config.yaml: auth.stacks.%s: %w", name, err)
		}
		kept = append(kept, strNode(name), val)
	}
	stacks.Content = kept
	return nil
}

// syncEntries returns the sequence node holding want, reusing the entry nodes
// of seq (which may be nil or not a sequence) that already say the same, then
// those of the same module and control (patching their options).
func syncEntries(seq *yaml.Node, want []spec.AuthModule) (*yaml.Node, error) {
	if seq == nil || seq.Kind != yaml.SequenceNode {
		seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	}
	old := seq.Content
	oldEntries := make([]*spec.AuthModule, len(old))
	for i, n := range old {
		var e spec.AuthModule
		if n.Kind == yaml.MappingNode && n.Decode(&e) == nil {
			oldEntries[i] = &e
		}
	}
	used := make([]bool, len(old))
	claim := func(match func(e spec.AuthModule) bool) int {
		for i, e := range oldEntries {
			if !used[i] && e != nil && match(*e) {
				used[i] = true
				return i
			}
		}
		return -1
	}
	content := make([]*yaml.Node, len(want))
	patch := make([]bool, len(want))
	for i, w := range want {
		if j := claim(func(e spec.AuthModule) bool { return entryKey(e) == entryKey(w) }); j >= 0 {
			content[i] = old[j]
		}
	}
	for i, w := range want {
		if content[i] != nil {
			continue
		}
		if j := claim(func(e spec.AuthModule) bool { return e.Module == w.Module && e.Control == w.Control }); j >= 0 {
			content[i], patch[i] = old[j], true
			continue
		}
		n, err := entryNode(w)
		if err != nil {
			return nil, err
		}
		content[i] = n
	}
	for i, w := range want {
		if patch[i] {
			if err := patchOptions(content[i], w.Options); err != nil {
				return nil, err
			}
		}
	}
	seq.Content = content
	if len(content) == 0 {
		seq.Style = yaml.FlowStyle
	} else if len(old) == 0 {
		seq.Style = 0
	}
	return seq, nil
}

// entryNode encodes one stack entry; an entry without options is a one-line flow mapping.
func entryNode(e spec.AuthModule) (*yaml.Node, error) {
	var n yaml.Node
	if err := n.Encode(e); err != nil {
		return nil, err
	}
	if len(e.Options) == 0 {
		n.Style = yaml.FlowStyle
	}
	return &n, nil
}

// patchOptions makes the options of the entry node hold exactly want.
func patchOptions(entry *yaml.Node, want map[string]any) error {
	i := keyIndex(entry, "options")
	if len(want) == 0 {
		if i >= 0 {
			entry.Content = slices.Delete(entry.Content, i, i+2)
		}
		return nil
	}
	if i < 0 {
		var opts yaml.Node
		if err := opts.Encode(want); err != nil {
			return err
		}
		entry.Content = append(entry.Content, strNode("options"), &opts)
		return nil
	}
	opts := entry.Content[i+1]
	if opts.Kind != yaml.MappingNode {
		var n yaml.Node
		if err := n.Encode(want); err != nil {
			return err
		}
		entry.Content[i+1] = &n
		return nil
	}
	kept := make([]*yaml.Node, 0, len(opts.Content))
	seen := map[string]bool{}
	for j := 0; j+1 < len(opts.Content); j += 2 {
		k := opts.Content[j].Value
		v, ok := want[k]
		if !ok {
			continue
		}
		seen[k] = true
		var cur any
		if opts.Content[j+1].Decode(&cur) == nil && sameJSON(cur, v) {
			kept = append(kept, opts.Content[j], opts.Content[j+1])
			continue
		}
		var val yaml.Node
		if err := val.Encode(v); err != nil {
			return err
		}
		val.LineComment = opts.Content[j+1].LineComment
		kept = append(kept, opts.Content[j], &val)
	}
	keys := make([]string, 0, len(want))
	for k := range want {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		var val yaml.Node
		if err := val.Encode(want[k]); err != nil {
			return err
		}
		kept = append(kept, strNode(k), &val)
	}
	opts.Content = kept
	return nil
}

func sameJSON(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}

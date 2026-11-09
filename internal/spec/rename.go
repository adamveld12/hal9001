package spec

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// legacyIDLine matches a top-level id: line of a sensor, flow or tool file
// (the id is the file name now; see Sensor.ID).
var legacyIDLine = regexp.MustCompile(`(?m)^id:[^\n]*\n?`)

// RenameFile moves the file of kind k from oldID to newID if it still hashes to
// expectedHash. It fails with ErrNotFound when oldID does not exist and with an
// error wrapping ErrConflict when expectedHash is stale or newID already exists;
// nothing is changed then. A legacy id: line is dropped from the moved content.
// It returns the hash of the file under its new name.
func RenameFile(home string, k Kind, oldID, newID, expectedHash string) (string, error) {
	if err := checkTarget(k, oldID); err != nil {
		return "", err
	}
	if err := checkTarget(k, newID); err != nil {
		return "", err
	}
	if oldID == newID {
		return "", fmt.Errorf("%w: the new id is the current one", ErrConflict)
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	oldPath, newPath := k.Path(home, oldID), k.Path(home, newID)
	mode, err := checkExpected(oldPath, k.Rel(oldID), expectedHash)
	if err != nil {
		if errors.Is(err, ErrConflict) && expectedHash != "" {
			if _, statErr := os.Stat(oldPath); errors.Is(statErr, fs.ErrNotExist) {
				return "", fmt.Errorf("%w: %s", ErrNotFound, k.Rel(oldID))
			}
		}
		return "", err
	}
	if _, err := os.Lstat(newPath); err == nil {
		return "", fmt.Errorf("%w: %s already exists", ErrConflict, k.Rel(newID))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	data, err := os.ReadFile(oldPath) //nolint:gosec // path is built from a validated id under home
	if err != nil {
		return "", err
	}
	if k == KindSensor || k == KindFlow || k == KindTool {
		data = legacyIDLine.ReplaceAll(data, nil)
	}
	if err := writeAtomic(newPath, data, mode); err != nil {
		return "", err
	}
	if err := os.Remove(oldPath); err != nil {
		_ = os.Remove(newPath)
		return "", err
	}
	return Hash(data), nil
}

// EnvExists reports whether the per-item env file of kind k (KindSensorEnv or
// KindToolEnv) exists for id.
func EnvExists(home string, k Kind, id string) bool {
	_, err := os.Lstat(k.Path(home, id))
	return err == nil
}

// RenameEnvFile moves a per-item env file (KindSensorEnv, KindToolEnv) to the
// new id. A missing source is not an error (moved is false); an existing target
// is.
func RenameEnvFile(home string, k Kind, oldID, newID string) (moved bool, err error) {
	if err := checkTarget(k, oldID); err != nil {
		return false, err
	}
	if err := checkTarget(k, newID); err != nil {
		return false, err
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	oldPath, newPath := k.Path(home, oldID), k.Path(home, newID)
	if _, err := os.Lstat(oldPath); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if _, err := os.Lstat(newPath); err == nil {
		return false, fmt.Errorf("%w: %s already exists", ErrConflict, k.Rel(newID))
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return false, err
	}
	return true, nil
}

// RenameRefs rewrites the references to a renamed item in one flow file and
// reports whether anything changed. what is the referenced kind: KindSensor
// (subscribe entries), KindTool (agent nodes' tools entries) or KindPrompt
// (agent nodes' prompt). Only the reference tokens change, so comments and
// layout survive. A file that does not parse is returned unchanged.
func RenameRefs(raw []byte, what Kind, oldID, newID string) ([]byte, bool) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return raw, false
	}
	var targets []*yaml.Node
	root := doc.Content[0]
	switch what {
	case KindSensor:
		targets = append(targets, seqScalars(mapValue(root, "subscribe"))...)
	case KindTool, KindPrompt:
		for _, n := range mapSeq(mapValue(root, "nodes")) {
			if n.Kind != yaml.MappingNode {
				continue
			}
			if what == KindPrompt {
				if v := mapValue(n, "prompt"); v != nil && v.Kind == yaml.ScalarNode {
					targets = append(targets, v)
				}
				continue
			}
			for _, t := range mapSeq(mapValue(n, "tools")) {
				switch t.Kind {
				case yaml.ScalarNode:
					targets = append(targets, t)
				case yaml.MappingNode:
					if v := mapValue(t, "id"); v != nil && v.Kind == yaml.ScalarNode {
						targets = append(targets, v)
					}
				}
			}
		}
	default:
		return raw, false
	}
	var hits []*yaml.Node
	for _, t := range targets {
		if t.Value == oldID {
			hits = append(hits, t)
		}
	}
	if len(hits) == 0 {
		return raw, false
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Line != hits[j].Line {
			return hits[i].Line > hits[j].Line
		}
		return hits[i].Column > hits[j].Column
	})
	out := raw
	changed := false
	for _, h := range hits {
		start := offsetOf(out, h.Line, h.Column)
		if start < 0 {
			continue
		}
		width := len(oldID)
		switch h.Style {
		case yaml.DoubleQuotedStyle, yaml.SingleQuotedStyle:
			start++ // keep the quotes; the value is a plain slug, so it has no escapes
		case 0:
		default:
			continue
		}
		if start+width > len(out) || string(out[start:start+width]) != oldID {
			continue
		}
		out = append(append(append([]byte{}, out[:start]...), newID...), out[start+width:]...)
		changed = true
	}
	return out, changed
}

// offsetOf converts a 1-based line and rune column to a byte offset, or -1.
func offsetOf(b []byte, line, col int) int {
	off := 0
	for l := 1; l < line; l++ {
		i := bytes.IndexByte(b[off:], '\n')
		if i < 0 {
			return -1
		}
		off += i + 1
	}
	for c := 1; c < col; c++ {
		if off >= len(b) {
			return -1
		}
		_, n := utf8.DecodeRune(b[off:])
		off += n
	}
	return off
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func mapSeq(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
}

func seqScalars(n *yaml.Node) []*yaml.Node {
	var out []*yaml.Node
	for _, c := range mapSeq(n) {
		if c.Kind == yaml.ScalarNode {
			out = append(out, c)
		}
	}
	return out
}

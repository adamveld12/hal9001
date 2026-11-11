package spec

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"go.yaml.in/yaml/v3"
)

// FileConfigBackup is the copy of config.yaml MigrateRuntimeCommands writes before it changes the file.
const FileConfigBackup = FileConfig + ".bak"

// legacyAdapters are the ACP adapters the old starter config ran through mise, by runtime kind.
var legacyAdapters = map[string]*regexp.Regexp{
	"claude": regexp.MustCompile(`^npm:@agentclientprotocol/claude-agent-acp@[^@\s]+$`),
	"codex":  regexp.MustCompile(`^npm:@agentclientprotocol/codex-acp@[^@\s]+$`),
}

var legacyBins = map[string]string{"claude": "claude-agent-acp", "codex": "codex-acp"}

// legacyKind returns the runtime kind whose old seeded mise command is argv
// (`mise exec npm:@agentclientprotocol/<adapter>@<version> -- <adapter>`), or "".
func legacyKind(argv []string) string {
	if len(argv) != 5 || argv[0] != "mise" || argv[1] != "exec" || argv[3] != "--" {
		return ""
	}
	for kind, re := range legacyAdapters {
		if re.MatchString(argv[2]) && argv[4] == legacyBins[kind] {
			return kind
		}
	}
	return ""
}

// MigrateRuntimeCommands removes the `command` of every claude or codex runtime in home's config.yaml that still
// holds the starter value of earlier versions (the pinned adapter run through `mise exec`), so hal9001 detects the
// agent itself (internal/runtimes). The runtime keeps its `kind`, which is set when absent. A customized command is
// left alone, and so is a runtime whose `kind` names another family. The rest of the file, comments included, is
// preserved; config.yaml.bak receives the original before the file is rewritten. It returns the names of the
// migrated runtimes; nothing is written when there are none. A missing or unparsable config.yaml is not an error
// (Load reports it).
func MigrateRuntimeCommands(home string) ([]string, error) {
	raw, hash, err := ReadFile(home, KindConfig, "")
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if yaml.Unmarshal(raw, &doc) != nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, nil
	}
	root := doc.Content[0]
	runtimes := mapValue(root, "runtimes")
	if runtimes == nil || runtimes.Kind != yaml.MappingNode {
		return nil, nil
	}
	var migrated []string
	for i := 0; i+1 < len(runtimes.Content); i += 2 {
		name, rt := runtimes.Content[i].Value, runtimes.Content[i+1]
		if rt.Kind != yaml.MappingNode {
			continue
		}
		idx := mapKeyIndex(rt, "command")
		if idx < 0 {
			continue
		}
		cmd := rt.Content[idx+1]
		if cmd.Kind != yaml.SequenceNode {
			continue
		}
		argv := make([]string, len(cmd.Content))
		for j, c := range cmd.Content {
			if c.Kind != yaml.ScalarNode {
				argv = nil
				break
			}
			argv[j] = c.Value
		}
		kind := legacyKind(argv)
		if kind == "" {
			continue
		}
		if k := mapValue(rt, "kind"); k != nil {
			if k.Value != kind {
				continue
			}
			rt.Content = append(rt.Content[:idx], rt.Content[idx+2:]...)
		} else {
			// kind takes the place of command, keeping its position and the comments around it.
			key := rt.Content[idx]
			key.Value = "kind"
			rt.Content[idx+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: kind, LineComment: cmd.LineComment}
		}
		migrated = append(migrated, name)
	}
	if len(migrated) == 0 {
		return nil, nil
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, fmt.Errorf("migrate runtimes: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("migrate runtimes: %w", err)
	}
	if err := os.WriteFile(filepath.Join(home, FileConfigBackup), raw, 0o600); err != nil {
		return nil, fmt.Errorf("migrate runtimes: write backup: %w", err)
	}
	if _, err := WriteConfig(home, buf.Bytes(), hash); err != nil {
		return nil, fmt.Errorf("migrate runtimes: %w", err)
	}
	return migrated, nil
}

func mapKeyIndex(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}

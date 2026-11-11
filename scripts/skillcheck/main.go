// Command skillcheck validates the YAML frontmatter of every .agents/skills/*/SKILL.md.
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

func check(path string) (name, desc string, err error) {
	b, err := os.ReadFile(path) //nolint:gosec // path comes from a fixed .agents/skills glob
	if err != nil {
		return "", "", err
	}
	rest, ok := bytes.CutPrefix(b, []byte("---\n"))
	if !ok {
		return "", "", fmt.Errorf("missing opening --- line")
	}
	fm, _, ok := strings.Cut(string(rest), "\n---")
	if !ok {
		return "", "", fmt.Errorf("missing closing --- line")
	}
	var meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(fm), &meta); err != nil {
		return "", "", fmt.Errorf("invalid YAML frontmatter: %w", err)
	}
	if meta.Name == "" || meta.Description == "" {
		return "", "", fmt.Errorf("name and description are required")
	}
	if dir := filepath.Base(filepath.Dir(path)); meta.Name != dir {
		return "", "", fmt.Errorf("name %q does not match directory %q", meta.Name, dir)
	}
	return meta.Name, meta.Description, nil
}

func main() {
	files, _ := filepath.Glob(".agents/skills/*/SKILL.md")
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "no skills found")
		os.Exit(1)
	}
	failed := false
	for _, f := range files {
		name, desc, err := check(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", f, err)
			failed = true
			continue
		}
		fmt.Printf("%s: name=%s\n  description=%s\n", f, name, desc)
	}
	if failed {
		os.Exit(1)
	}
}

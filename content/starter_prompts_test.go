package content

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestPromptsHaveFrontmatterAndNoteSchema(t *testing.T) {
	for _, n := range names(t, "prompts", ".md") {
		raw, err := FS.ReadFile("prompts/" + n + ".md")
		if err != nil {
			t.Fatal(err)
		}
		head, body, ok := strings.Cut(strings.TrimPrefix(string(raw), "---\n"), "\n---\n")
		if !ok || !strings.HasPrefix(string(raw), "---\n") {
			t.Fatalf("%s: no frontmatter", n)
		}
		var fm struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
			Output      string `yaml:"output"`
		}
		dec := yaml.NewDecoder(strings.NewReader(head))
		dec.KnownFields(true)
		if err := dec.Decode(&fm); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		if fm.Name != "" || fm.Description == "" || fm.Output != "note" {
			t.Errorf("%s: frontmatter %+v", n, fm)
		}
		for _, want := range []string{"summary_md", "suggested_reply", "priority", "links", "untrusted"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: body lacks %q", n, want)
			}
		}
	}
}

package content

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"hal9001/internal/transform"
)

type flowDef struct {
	ID            string    `yaml:"id"` // must stay empty: the file name is the id
	Description   string    `yaml:"description"`
	Enabled       bool      `yaml:"enabled"`
	DryRun        bool      `yaml:"dry_run"`
	Subscribe     []string  `yaml:"subscribe"`
	MaxConcurrent int       `yaml:"max_concurrent"`
	Nodes         []nodeDef `yaml:"nodes"`
	Edges         []edgeDef `yaml:"edges"`
}

type nodeDef struct {
	ID       string           `yaml:"id"`
	Type     string           `yaml:"type"`
	Lang     string           `yaml:"lang"`
	Script   string           `yaml:"script"`
	Steps    []transform.Step `yaml:"steps"`
	Ports    []string         `yaml:"ports"`
	Runtime  string           `yaml:"runtime"`
	Mode     string           `yaml:"mode"`
	Prompt   string           `yaml:"prompt"`
	Timeout  string           `yaml:"timeout"`
	Title    string           `yaml:"title"`
	Priority string           `yaml:"priority"`
	Summary  string           `yaml:"summary_md"`
	Cwd      map[string]any   `yaml:"cwd"`
	Allow    map[string]any   `yaml:"allow"`
	UI       map[string]any   `yaml:"ui"`
}

type edgeDef struct {
	From string `yaml:"from"`
	Port string `yaml:"port"`
	To   string `yaml:"to"`
}

func strictDecode(t *testing.T, path string, out any) {
	t.Helper()
	raw, err := FS.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func names(t *testing.T, dir, ext string) []string {
	t.Helper()
	entries, err := fs.ReadDir(FS, dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ext) {
			out = append(out, strings.TrimSuffix(e.Name(), ext))
		}
	}
	return out
}

func loadFlows(t *testing.T) map[string]flowDef {
	t.Helper()
	flows := map[string]flowDef{}
	for _, n := range names(t, "flows", ".yaml") {
		var f flowDef
		strictDecode(t, "flows/"+n+".yaml", &f)
		if f.ID != "" {
			t.Errorf("flows/%s.yaml has an id line (%q): the file name is the id", n, f.ID)
		}
		flows[n] = f
	}
	return flows
}

// jqNode runs a flow node's jq script against one payload, as the runner does (jq -c -f).
func jqNode(t *testing.T, script, input string) string {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	file := filepath.Join(t.TempDir(), "node.jq")
	if err := os.WriteFile(file, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("jq", "-c", "-f", file)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jq: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func node(t *testing.T, flow, id string) nodeDef {
	t.Helper()
	for _, n := range loadFlows(t)[flow].Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("%s/%s not found", flow, id)
	return nodeDef{}
}

// compiled returns the jq a transform node's steps compile to.
func compiled(t *testing.T, flow, id string) string {
	t.Helper()
	n := node(t, flow, id)
	if n.Type != "transform" {
		t.Fatalf("%s/%s is a %s node, want transform", flow, id, n.Type)
	}
	jq, err := transform.Compile(n.Steps)
	if err != nil {
		t.Fatalf("%s/%s: %v", flow, id, err)
	}
	return jq
}

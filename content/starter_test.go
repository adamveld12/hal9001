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

	"hal9001/internal/spec"
	"hal9001/internal/transform"
)

type flowDef struct {
	ID            string    `yaml:"id"`
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
		if f.ID != n {
			t.Errorf("flows/%s.yaml has id %q", n, f.ID)
		}
		flows[n] = f
	}
	return flows
}

func TestEmbeddedLayout(t *testing.T) {
	for _, p := range []string{"config.yaml", "env.example"} {
		if _, err := fs.Stat(FS, p); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	for dir, want := range map[string][]string{
		"sensors": {"slack-mentions-dms", "github-notifications", "linear-assigned", "gmail-unread", "imap-mail", "demo-ticker"},
		"flows":   {"triage-github", "triage-slack", "linear-triage", "email-secretary", "demo"},
		"prompts": {"triage-slack", "pr-review", "linear-triage", "email-secretary"},
	} {
		ext := ".yaml"
		if dir == "prompts" {
			ext = ".md"
		}
		got := map[string]bool{}
		for _, n := range names(t, dir, ext) {
			got[n] = true
		}
		for _, w := range want {
			if !got[w] {
				t.Errorf("%s/%s%s missing", dir, w, ext)
			}
		}
	}
}

func TestSensorsDeclareKeysAndBaseline(t *testing.T) {
	wantKey := map[string]string{
		"slack-mentions-dms":   "{{.channel}}:{{.ts}}",
		"github-notifications": "{{.id}}:{{.updated_at}}",
		"linear-assigned":      "{{.id}}:{{.updatedAt}}",
		"gmail-unread":         "{{.id}}",
		"imap-mail":            "{{.id}}",
		"demo-ticker":          "{{.id}}:{{.updated_at}}",
	}
	for id, key := range wantKey {
		def := loadSensor(t, id)
		wantLang := "bash"
		if id == "imap-mail" {
			wantLang = "python"
		}
		if def.ID != id || def.Key != key || def.Lang != wantLang || def.Script == "" || def.Timeout == "" || def.Interval == "" {
			t.Errorf("%s: unexpected definition %+v", id, def)
		}
		wantFirst := "baseline"
		if id == "demo-ticker" {
			wantFirst = "fire"
		}
		if def.FirstRun != wantFirst {
			t.Errorf("%s: first_run = %q, want %q", id, def.FirstRun, wantFirst)
		}
	}
}

func TestEnvExampleListsEveryCredential(t *testing.T) {
	raw, err := FS.ReadFile("env.example")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"SLACK_USER_TOKEN=", "LINEAR_API_KEY=", "search:read", "im:history", "im:read", "users:read", "GOG_ACCOUNT", "IMAP_USER", "IMAP_PASSWORD", "IMAP_HOST", "IMAP_PORT", "IMAP_TLS", "IMAP_MAILBOX", "IMAP_SEARCH", "IMAP_MAX"} {
		if !strings.Contains(text, want) {
			t.Errorf("env.example lacks %q", want)
		}
	}
	// Every credential a sensor requires must be documented.
	for _, sensor := range names(t, "sensors", ".yaml") {
		for _, v := range []string{"SLACK_USER_TOKEN", "LINEAR_API_KEY"} {
			if strings.Contains(loadSensor(t, sensor).Script, "${"+v+":?") && !strings.Contains(text, v+"=") {
				t.Errorf("%s needs %s but env.example lacks it", sensor, v)
			}
		}
	}
}

func TestFlowsAreConsistent(t *testing.T) {
	sensors := map[string]bool{}
	for _, n := range names(t, "sensors", ".yaml") {
		sensors[n] = true
	}
	prompts := map[string]bool{}
	for _, n := range names(t, "prompts", ".md") {
		prompts[n] = true
	}
	var cfg struct {
		Runtimes map[string]any `yaml:"runtimes"`
	}
	raw, err := FS.ReadFile("config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}

	for id, f := range loadFlows(t) {
		if f.Enabled != (id == "demo") {
			t.Errorf("%s: enabled = %v; only demo ships enabled", id, f.Enabled)
		}
		for _, s := range f.Subscribe {
			if !sensors[s] {
				t.Errorf("%s subscribes to unknown sensor %q", id, s)
			}
		}
		nodes := map[string]nodeDef{}
		hasAgent := false
		for _, n := range f.Nodes {
			nodes[n.ID] = n
			if n.Type == "agent" {
				hasAgent = true
				if _, ok := cfg.Runtimes[n.Runtime]; !ok {
					t.Errorf("%s/%s: runtime %q not in config.yaml", id, n.ID, n.Runtime)
				}
				if !prompts[n.Prompt] {
					t.Errorf("%s/%s: unknown prompt %q", id, n.ID, n.Prompt)
				}
				if n.Mode != "readonly" {
					t.Errorf("%s/%s: starter agents run readonly, got %q", id, n.ID, n.Mode)
				}
			}
		}
		if hasAgent && !f.DryRun {
			t.Errorf("%s has agent nodes and must ship with dry_run: true", id)
		}
		incoming := map[string]bool{}
		for _, e := range f.Edges {
			from, ok := nodes[e.From]
			if !ok || nodes[e.To].ID == "" {
				t.Errorf("%s: edge %+v references an unknown node", id, e)
				continue
			}
			incoming[e.To] = true
			declared := from.Ports
			if from.Type == "transform" {
				declared = transform.Ports(from.Steps)
			}
			switch {
			case e.Port == "":
				if from.Type != "transform" && len(from.Ports) > 0 {
					t.Errorf("%s: node %s declares ports, edge %+v must name one", id, from.ID, e)
				}
			case e.Port != "error" && e.Port != "denied":
				found := false
				for _, p := range declared {
					found = found || p == e.Port
				}
				if !found {
					t.Errorf("%s: edge %+v uses undeclared port", id, e)
				}
			}
		}
		for _, n := range f.Nodes {
			if len(f.Nodes) > 1 && !incoming[n.ID] && n.ID != f.Nodes[0].ID {
				t.Errorf("%s: node %s is unreachable", id, n.ID)
			}
		}
	}
}

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
		if fm.Name != n || fm.Description == "" || fm.Output != "note" {
			t.Errorf("%s: frontmatter %+v", n, fm)
		}
		for _, want := range []string{"summary_md", "suggested_reply", "priority", "links", "untrusted"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: body lacks %q", n, want)
			}
		}
	}
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

func TestDemoFlowFiltersAndRoutes(t *testing.T) {
	drop := compiled(t, "demo", "drop-noise")
	route := compiled(t, "demo", "route")
	if out := jqNode(t, drop, `{"id":"3","severity":"noise"}`); out != "" {
		t.Errorf("noise must be dropped, got %s", out)
	}
	if out := jqNode(t, drop, `{"id":"1","severity":"urgent"}`); out != `{"id":"1","severity":"urgent"}` {
		t.Errorf("a kept item must pass through unchanged: %s", out)
	}
	if out := jqNode(t, route, `{"severity":"urgent"}`); !strings.Contains(out, `"_port":"urgent"`) {
		t.Errorf("urgent routed wrongly: %s", out)
	}
	if out := jqNode(t, route, `{"severity":"normal"}`); !strings.Contains(out, `"_port":"normal"`) {
		t.Errorf("normal routed wrongly: %s", out)
	}
}

func TestFilterNodes(t *testing.T) {
	cases := []struct {
		flow, node, input string
		keep              bool
	}{
		{"triage-github", "only-review-requests", `{"reason":"review_requested","type":"PullRequest"}`, true},
		{"triage-github", "only-review-requests", `{"reason":"mention","type":"PullRequest"}`, false},
		{"triage-github", "only-review-requests", `{"reason":"review_requested","type":"Issue"}`, false},
		{"triage-slack", "filter", `{"text":"hello"}`, true},
		{"triage-slack", "filter", `{"text":"  \n"}`, false},
		{"linear-triage", "skip-closed", `{"state_type":"started"}`, true},
		{"linear-triage", "skip-closed", `{"state_type":"completed"}`, false},
		{"linear-triage", "skip-closed", `{"kind":"comment"}`, true},
		{"email-secretary", "skip-automated", `{"from":"Alice <alice@example.com>"}`, true},
		{"email-secretary", "skip-automated", `{"from":"GitHub <notifications@github.com>"}`, false},
		{"email-secretary", "skip-automated", `{"from":"App <NoReply@example.com>"}`, false},
	}
	for _, tc := range cases {
		out := jqNode(t, node(t, tc.flow, tc.node).Script, tc.input)
		if (out != "") != tc.keep {
			t.Errorf("%s/%s on %s: keep=%v, output %q", tc.flow, tc.node, tc.input, tc.keep, out)
		}
	}
}

func TestClassifyRoutesBySize(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	script := node(t, "triage-github", "classify").Script
	const event = `{"id":"1","subject":{"url":"https://api.github.com/repos/acme/widgets/pulls/42"}}`
	cases := []struct {
		name, gh, port string
	}{
		{"small", "echo '{\"additions\":120,\"deletions\":79}'", "small"},
		{"boundary is large", "echo '{\"additions\":150,\"deletions\":50}'", "large"},
		{"gh failure goes to a human", "echo 'gh: HTTP 404' >&2; exit 1", "large"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			ghPath := filepath.Join(dir, "gh")
			if err := os.WriteFile(ghPath, []byte("#!/bin/bash\n"+tc.gh+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "classify.py")
			if err := os.WriteFile(file, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(py, file)
			cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin"}
			cmd.Stdin = strings.NewReader(event)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("classify: %v", err)
			}
			if !strings.Contains(string(out), `"_port": "`+tc.port+`"`) {
				t.Errorf("want port %s, got %s", tc.port, out)
			}
		})
	}
}

// env.example is copied to the global env file as is: it must not mark anything plain by accident.
func TestEnvExampleMarksNothingPlain(t *testing.T) {
	raw, err := FS.ReadFile("env.example")
	if err != nil {
		t.Fatal(err)
	}
	env, ds := spec.ParseEnv(raw)
	if spec.HasErrors(ds) {
		t.Fatal(ds)
	}
	if len(env.Plain) != 0 {
		t.Errorf("env.example marks %v plain", env.Plain)
	}
}

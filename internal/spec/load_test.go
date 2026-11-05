package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func put(t *testing.T, home, rel, content string) {
	t.Helper()
	p := filepath.Join(home, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fullHome writes the REQUIREMENTS samples as a complete home dir.
func fullHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	put(t, home, "config.yaml", requirementsConfig)
	put(t, home, "env", "SLACK_USER_TOKEN=xoxp-1\nLINEAR_API_KEY=lin_2\n")
	put(t, home, "sensors/github-notifications.yaml", requirementsSensor)
	put(t, home, "prompts/pr-review.md", requirementsPrompt)
	put(t, home, "flows/triage-github.yaml", requirementsFlow)
	return home
}

func TestLoadRequirementsHomeIsClean(t *testing.T) {
	w := Load(fullHome(t))
	if ds := w.AllDiagnostics(); len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	if len(w.Sensors) != 1 || len(w.Flows) != 1 || len(w.Prompts) != 1 {
		t.Fatalf("counts: %d sensors, %d flows, %d prompts", len(w.Sensors), len(w.Flows), len(w.Prompts))
	}
	f := w.Flow("triage-github")
	if f == nil || !f.Valid() || f.Hash != Hash([]byte(requirementsFlow)) || f.Path != "flows/triage-github.yaml" {
		t.Fatalf("flow file = %+v", f)
	}
	if string(f.Raw) != requirementsFlow {
		t.Error("Raw does not hold the file bytes for flow_versions snapshots")
	}
	if got := strings.Join(w.Env.Value.Keys, ","); got != "SLACK_USER_TOKEN,LINEAR_API_KEY" {
		t.Errorf("env keys = %s", got)
	}
	if w.Env.Value.Vars["LINEAR_API_KEY"] != "lin_2" {
		t.Error("env values must be available to the engine for injection and redaction")
	}
}

func TestLoadOneBadFileDoesNotHideOthers(t *testing.T) {
	home := fullHome(t)
	put(t, home, "flows/broken-syntax.yaml", "id: broken-syntax\nnodes: [unclosed\n")
	put(t, home, "flows/empty.yaml", "")
	put(t, home, "flows/bad-node.yaml", "id: bad-node\nsubscribe: [github-notifications]\nnodes:\n  - {id: a, type: nonsense}\nedges: []\n")
	put(t, home, "sensors/bad-sensor.yaml", "id: bad-sensor\ninterval: 1m\nlang: bash\n")
	put(t, home, "prompts/bad-prompt.md", "---\nname: bad-prompt\nbody")
	put(t, home, "env", "GOOD=1\nthis line is bad\nALSO_GOOD=2\n")

	w := Load(home)

	if f := w.Flow("triage-github"); f == nil || !f.Valid() || len(f.Diagnostics) != 0 {
		t.Errorf("good flow affected by bad siblings: %+v", f)
	}
	if s := w.Sensor("github-notifications"); s == nil || !s.Valid() {
		t.Errorf("good sensor affected: %+v", s)
	}
	if p := w.Prompt("pr-review"); p == nil || !p.Valid() {
		t.Errorf("good prompt affected: %+v", p)
	}

	syntax := w.Flow("broken-syntax")
	if syntax.Value != nil || syntax.Valid() || len(syntax.Diagnostics) != 1 || syntax.Diagnostics[0].Code != CodeYAMLSyntax || syntax.Diagnostics[0].File != "flows/broken-syntax.yaml" {
		t.Errorf("syntax error file: %+v", syntax)
	}
	if e := w.Flow("empty"); e.Value != nil || e.Diagnostics[0].Code != CodeEmptyFile {
		t.Errorf("empty file: %+v", e)
	}
	if b := w.Flow("bad-node"); b.Value == nil || b.Valid() || b.Diagnostics[0].Code != CodeInvalidEnum {
		t.Errorf("bad node: %+v", b)
	}
	if s := w.Sensor("bad-sensor"); s.Valid() {
		t.Errorf("bad sensor should be invalid: %+v", s.Diagnostics)
	}
	if p := w.Prompt("bad-prompt"); p.Valid() {
		t.Errorf("bad prompt should be invalid: %+v", p.Diagnostics)
	}
	if w.Env.Value.Vars["GOOD"] != "1" || w.Env.Value.Vars["ALSO_GOOD"] != "2" || !HasErrors(w.Env.Diagnostics) {
		t.Errorf("env: %v %v", w.Env.Value.Vars, w.Env.Diagnostics)
	}
}

func TestLoadResolvesCrossFileReferences(t *testing.T) {
	home := t.TempDir()
	put(t, home, "flows/f.yaml", "id: f\nsubscribe: [s1, missing]\nnodes:\n  - {id: a, type: agent, runtime: claude, mode: auto, prompt: p1}\nedges: []\n")
	put(t, home, "sensors/s1.yaml", "id: s1\ninterval: 1m\nlang: bash\nscript: echo\nkey: k\n")
	put(t, home, "prompts/p1.md", "---\nname: p1\n---\nbody\n")
	put(t, home, "config.yaml", "runtimes: {claude: {command: [claude-agent-acp]}}\n")

	w := Load(home)
	f := w.Flow("f")
	if len(f.Diagnostics) != 1 || f.Diagnostics[0].Code != CodeUnknownSensor || f.Diagnostics[0].Path != "subscribe[1]" {
		t.Fatalf("diagnostics = %v", f.Diagnostics)
	}

	// An invalid sensor still counts as existing: the flow reports nothing extra.
	put(t, home, "sensors/missing.yaml", "id: missing\n")
	f = Load(home).Flow("f")
	if len(f.Diagnostics) != 0 {
		t.Errorf("reference to an existing but invalid sensor: %v", f.Diagnostics)
	}
}

func TestLoadMissingHome(t *testing.T) {
	w := Load(filepath.Join(t.TempDir(), "nope"))
	if w.Config.Value == nil || w.Config.Value.MaxAgentRuns != 2 {
		t.Errorf("config should fall back to defaults: %+v", w.Config.Value)
	}
	if len(w.Flows)+len(w.Sensors)+len(w.Prompts) != 0 || HasErrors(w.AllDiagnostics()) {
		t.Errorf("missing home is not an error: %v", w.AllDiagnostics())
	}
	if len(w.Config.Diagnostics) != 1 || w.Config.Diagnostics[0].Severity != SeverityInfo {
		t.Errorf("config diagnostics = %v", w.Config.Diagnostics)
	}
}

func TestLoadBadConfigKeepsDefaults(t *testing.T) {
	home := t.TempDir()
	put(t, home, "config.yaml", "max_agent_runs: [1\n")
	w := Load(home)
	if w.Config.Value == nil || w.Config.Value.MaxAgentRuns != 2 || !HasErrors(w.Config.Diagnostics) {
		t.Errorf("config = %+v %v", w.Config.Value, w.Config.Diagnostics)
	}
}

func TestLoadFileNameRules(t *testing.T) {
	home := t.TempDir()
	put(t, home, "flows/.hidden.yaml", "x")
	put(t, home, "flows/a.yaml~", "x")
	put(t, home, "flows/README.txt", "x")
	put(t, home, "flows/old.yml", "id: old\n")
	put(t, home, "flows/bad name.yaml", "id: x\n")
	put(t, home, "flows/sub/ignored.yaml", "x")
	w := Load(home)
	if len(w.Flows) != 1 || w.Flows[0].Name != "bad name" {
		t.Fatalf("flows = %v", w.Flows)
	}
	if d := w.Flows[0].Diagnostics; !HasErrors(d) || d[len(d)-1].Code != CodeInvalidID {
		t.Errorf("invalid file name diagnostics: %v", d)
	}
	var warned bool
	for _, d := range w.Diagnostics {
		if d.Code == CodeIgnoredFile && d.File == "flows/old.yml" && d.Severity == SeverityWarning {
			warned = true
		}
	}
	if !warned {
		t.Errorf(".yml file should be reported as ignored: %v", w.Diagnostics)
	}
}

func TestInitCreatesTreeAndSeedsWithoutOverwriting(t *testing.T) {
	home := filepath.Join(t.TempDir(), "hal")
	seed := fstest.MapFS{
		"config.yaml":          {Data: []byte("max_agent_runs: 5\n")},
		"sensors/s.yaml":       {Data: []byte("id: s\ninterval: 1m\nlang: bash\nscript: echo\nkey: k\n")},
		"prompts/p.md":         {Data: []byte("---\nname: p\n---\nb\n")},
		"data/should-not-copy": {Data: []byte("x")},
		"flows/demo.yaml":      {Data: []byte("id: demo\n")},
	}
	created, err := Init(home, seed)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"sensors", "flows", "prompts", "data/blobs", "data/state", "data/worktrees"} {
		if st, err := os.Stat(filepath.Join(home, d)); err != nil || !st.IsDir() {
			t.Errorf("missing directory %s: %v", d, err)
		}
	}
	want := ".gitignore,config.yaml,flows/demo.yaml,prompts/p.md,sensors/s.yaml"
	if got := strings.Join(created, ","); got != want {
		t.Errorf("created = %s, want %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(home, "data", "should-not-copy")); err == nil {
		t.Error("seed files under data/ must not be copied")
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".gitignore")); !strings.Contains(string(b), "data/") || !strings.Contains(string(b), "env") {
		t.Errorf(".gitignore = %q", b)
	}

	// The user edits a seeded file; a second Init must keep it and create nothing.
	put(t, home, "config.yaml", "max_agent_runs: 9\n")
	created, err = Init(home, seed)
	if err != nil || len(created) != 0 {
		t.Fatalf("second Init created %v, err %v", created, err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "config.yaml")); string(b) != "max_agent_runs: 9\n" {
		t.Errorf("Init overwrote user edit: %q", b)
	}

	// A seeded home loads without errors.
	if HasErrors(Load(home).AllDiagnostics()) {
		t.Errorf("seeded home has errors: %v", Load(home).AllDiagnostics())
	}
}

func TestInitWithoutSeed(t *testing.T) {
	home := filepath.Join(t.TempDir(), "hal")
	if _, err := Init(home, nil); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(home, "flows")); err != nil || !st.IsDir() {
		t.Errorf("flows dir: %v", err)
	}
}

func TestWorkspaceJSONNeverContainsEnvValues(t *testing.T) {
	out := mustJSON(t, Load(fullHome(t)))
	if strings.Contains(out, "xoxp-1") || strings.Contains(out, "lin_2") {
		t.Fatal("workspace JSON leaks env values")
	}
	if !strings.Contains(out, `"keys":["SLACK_USER_TOKEN","LINEAR_API_KEY"]`) {
		t.Errorf("workspace JSON lacks env key names: %s", out)
	}
}

package api

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hal9001/internal/spec"
	"hal9001/internal/store"
)

func (e *env) readHome(rel string) string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.home, filepath.FromSlash(rel)))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

func (e *env) writeHome(rel, content string) {
	e.t.Helper()
	must(e.t, os.WriteFile(filepath.Join(e.home, filepath.FromSlash(rel)), []byte(content), 0o600))
}

func flowBody(id string, nodes ...string) map[string]any {
	ns := []map[string]any{}
	for _, n := range nodes {
		ns = append(ns, map[string]any{"id": n, "type": "command", "lang": "bash", "script": "echo '{}'"})
	}
	edges := []map[string]any{}
	for i := 1; i < len(nodes); i++ {
		edges = append(edges, map[string]any{"from": nodes[i-1], "to": nodes[i]})
	}
	return map[string]any{"id": id, "enabled": true, "subscribe": []string{"demo-ticker"}, "nodes": ns, "edges": edges}
}

type fileDetail struct {
	ID          string `json:"id"`
	Hash        string `json:"hash"`
	YAML        string `json:"yaml"`
	Diagnostics []struct {
		Severity string `json:"severity"`
		Code     string `json:"code"`
		Path     string `json:"path"`
	} `json:"diagnostics"`
}

func diagCodes(r *resp) []string {
	var p struct {
		Diagnostics []struct {
			Code string `json:"code"`
		} `json:"diagnostics"`
	}
	r.decode(&p)
	var out []string
	for _, d := range p.Diagnostics {
		out = append(out, d.Code)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestFlowSaveConflictsValidationAndReload(t *testing.T) {
	e := newEnv(t)

	// The seeded flows are listed, valid ones included.
	var list struct {
		Items []struct {
			ID        string `json:"id"`
			NodeCount int    `json:"node_count"`
			Health    struct {
				Runs int `json:"runs"`
			} `json:"health"`
		} `json:"items"`
	}
	e.get("/api/flows").want(200).decode(&list)
	if len(list.Items) < 4 || list.Items[0].NodeCount == 0 {
		t.Fatalf("seeded flows = %+v", list.Items)
	}

	// Create: written to disk, reloaded before the response.
	created := e.do("POST", "/api/flows", map[string]any{"flow": flowBody("mine", "a", "b")}).want(201)
	if loc := created.Header.Get("Location"); loc != "/api/flows/mine" {
		t.Errorf("Location = %q", loc)
	}
	var v1 fileDetail
	created.decode(&v1)
	if e.reloads.Load() != 1 || e.readHome("flows/mine.yaml") != v1.YAML || v1.Hash != spec.Hash([]byte(v1.YAML)) {
		t.Fatalf("create: reloads %d, file/hash disagree with response", e.reloads.Load())
	}
	if e.ws.Load().Flow("mine") == nil {
		t.Error("the response was sent before the workspace was reloaded")
	}
	e.do("POST", "/api/flows", map[string]any{"flow": flowBody("mine", "a")}).want(409)
	if r := e.do("POST", "/api/flows", map[string]any{"id": "mine", "yaml": v1.YAML}).want(409); r.problem() != "already-exists" {
		t.Errorf("duplicate create: %s", r.problem())
	}
	if e.reloads.Load() != 1 {
		t.Error("a rejected write must not reload")
	}

	// Update with the hash from the last read.
	updated := e.do("PUT", "/api/flows/mine", map[string]any{"expected_hash": v1.Hash, "flow": flowBody("mine", "a", "b", "c")}).want(200)
	var v2 fileDetail
	updated.decode(&v2)
	if v2.Hash == v1.Hash || e.readHome("flows/mine.yaml") != v2.YAML || e.reloads.Load() != 2 || !strings.Contains(v2.YAML, "- id: c") {
		t.Fatalf("update: hash unchanged or file stale (reloads %d)", e.reloads.Load())
	}

	// A save based on the old version is a conflict that names both hashes.
	r := e.do("PUT", "/api/flows/mine", map[string]any{"expected_hash": v1.Hash, "flow": flowBody("mine", "x")}).want(409)
	body := r.json()
	if r.problem() != "conflict" || body["expected_hash"] != v1.Hash || body["current_hash"] != v2.Hash {
		t.Errorf("conflict body = %s", r.Body)
	}
	if e.readHome("flows/mine.yaml") != v2.YAML || e.reloads.Load() != 2 {
		t.Error("a conflicting save changed the file or reloaded")
	}

	// Editing the file by hand makes the next UI save conflict too (scenario 16).
	e.writeHome("flows/mine.yaml", v2.YAML+"# edited by hand\n")
	e.do("PUT", "/api/flows/mine", map[string]any{"expected_hash": v2.Hash, "yaml": v2.YAML}).want(409)
	var fresh fileDetail
	e.get("/api/flows/mine").want(200).decode(&fresh)
	if !strings.Contains(fresh.YAML, "# edited by hand") || fresh.Hash == v2.Hash {
		t.Error("GET must return the file as it is on disk")
	}

	// Validation errors come back as diagnostics and write nothing.
	cycle := flowBody("mine", "a", "b")
	cycle["edges"] = []map[string]any{{"from": "a", "to": "b"}, {"from": "b", "to": "a"}}
	r = e.do("PUT", "/api/flows/mine", map[string]any{"expected_hash": fresh.Hash, "flow": cycle}).want(422)
	if r.problem() != "validation-failed" || !contains(diagCodes(r), "cycle") {
		t.Errorf("cycle: %s", r.Body)
	}
	badEdge := flowBody("mine", "a")
	badEdge["edges"] = []map[string]any{{"from": "a", "to": "ghost"}}
	if r = e.do("PUT", "/api/flows/mine", map[string]any{"expected_hash": fresh.Hash, "flow": badEdge}).want(422); !contains(diagCodes(r), "unknown_node") {
		t.Errorf("unknown node: %s", r.Body)
	}
	if r = e.do("POST", "/api/flows", map[string]any{"flow": flowBody("Bad Name", "a")}).want(422); !contains(diagCodes(r), "invalid_id") {
		t.Errorf("invalid id: %s", r.Body)
	}
	if r = e.do("POST", "/api/flows", map[string]any{"yaml": "id: [unclosed"}).want(422); !contains(diagCodes(r), "yaml_syntax") {
		t.Errorf("yaml syntax: %s", r.Body)
	}
	if e.readHome("flows/mine.yaml") != fresh.YAML {
		t.Error("an invalid save changed the file")
	}
	for _, bad := range []any{map[string]any{"expected_hash": fresh.Hash}, map[string]any{"expected_hash": fresh.Hash, "yaml": "x", "flow": flowBody("mine", "a")},
		map[string]any{"expected_hash": "short", "yaml": "id: mine"}, `{"yaml": "x", "expected_hash": "` + fresh.Hash + `", "extra": 1}`, `not json`} {
		e.do("PUT", "/api/flows/mine", bad).want(400)
	}
	e.do("PUT", "/api/flows/nothing-here", map[string]any{"expected_hash": fresh.Hash, "flow": flowBody("nothing-here", "a")}).want(404)

	// Validation without saving.
	var val struct {
		Valid bool    `json:"valid"`
		YAML  *string `json:"yaml"`
		Flow  *struct {
			ID string `json:"id"`
		} `json:"flow"`
	}
	e.do("POST", "/api/flows/validate", map[string]any{"flow": flowBody("preview", "a")}).want(200).decode(&val)
	if !val.Valid || val.YAML == nil || strings.HasPrefix(*val.YAML, "id:") || val.Flow == nil {
		t.Errorf("validate valid flow = %+v", val)
	}
	r = e.do("POST", "/api/flows/validate", map[string]any{"flow": cycle}).want(200)
	if r.json()["valid"] != false || !contains(diagCodes(r), "cycle") {
		t.Errorf("validate cycle: %s", r.Body)
	}
	if _, err := os.Stat(filepath.Join(e.home, "flows", "preview.yaml")); err == nil {
		t.Error("validate wrote a file")
	}

	// Delete needs the current hash.
	e.do("DELETE", "/api/flows/mine", nil).want(400)
	if r = e.do("DELETE", "/api/flows/mine?expected_hash="+v2.Hash, nil).want(409); r.json()["current_hash"] != fresh.Hash {
		t.Errorf("delete conflict: %s", r.Body)
	}
	before := e.reloads.Load()
	e.do("DELETE", "/api/flows/mine?expected_hash="+fresh.Hash, nil).want(204)
	if _, err := os.Stat(filepath.Join(e.home, "flows", "mine.yaml")); err == nil || e.reloads.Load() != before+1 {
		t.Error("delete left the file or did not reload")
	}
	e.get("/api/flows/mine").want(404)
	e.do("DELETE", "/api/flows/mine?expected_hash="+fresh.Hash, nil).want(404)
}

func TestSensorFilesAndStatus(t *testing.T) {
	e := newEnv(t)
	yamlText := "id: probe\ninterval: 5m\nlang: bash\nscript: echo '[]'\nkey: '{{.id}}'\n"
	created := e.do("POST", "/api/sensors", map[string]any{"yaml": yamlText}).want(201)
	var d struct {
		Hash   string `json:"hash"`
		Sensor struct {
			Interval string `json:"interval"`
			FirstRun string `json:"first_run"`
		} `json:"sensor"`
		Status struct {
			State      string  `json:"state"`
			NextPollAt *string `json:"next_poll_at"`
			LastRun    any     `json:"last_run"`
		} `json:"status"`
	}
	created.decode(&d)
	if raw := e.readHome("sensors/probe.yaml"); strings.Contains(raw, "id:") {
		t.Errorf("a legacy id line was stored: %q", raw)
	}
	if d.Sensor.Interval != "5m" || d.Sensor.FirstRun != "baseline" || d.Status.State != "idle" || d.Status.LastRun != nil {
		t.Errorf("new sensor = %+v", d)
	}
	// No enabled flow subscribes to it, so the engine does not schedule it.
	if d.Status.NextPollAt != nil {
		t.Errorf("unsubscribed sensor next_poll_at = %v, want null", *d.Status.NextPollAt)
	}
	if e.reloads.Load() != 1 {
		t.Errorf("reloads = %d", e.reloads.Load())
	}

	body := flowBody("watcher", "a")
	body["subscribe"] = []string{"probe"}
	e.do("POST", "/api/flows", map[string]any{"flow": body}).want(201)
	e.ws.Store(spec.Load(e.home))
	var sub struct {
		Status struct {
			NextPollAt *string `json:"next_poll_at"`
		} `json:"status"`
	}
	e.get("/api/sensors/probe").want(200).decode(&sub)
	if sub.Status.NextPollAt == nil {
		t.Error("sensor with an enabled subscribing flow has no next_poll_at")
	}

	// Status follows what the engine recorded.
	e.event("probe", "k1", `{}`)
	e.event("probe", "k2", `{}`)
	run := &store.SensorRun{SensorID: "probe", Status: store.SensorFailed, Error: "exit_nonzero"}
	must(t, e.st.CreateSensorRun(ctx(), run))
	must(t, e.st.RecordSensorOutcome(ctx(), "probe", run.ID, time.Now(), false, "exit 2"))
	var got struct {
		Status struct {
			State       string              `json:"state"`
			NewItems24h int                 `json:"new_items_24h"`
			LastRun     struct{ ID string } `json:"last_run"`
		} `json:"status"`
	}
	e.get("/api/sensors/probe").want(200).decode(&got)
	if got.Status.State != "failing" || got.Status.NewItems24h != 2 || got.Status.LastRun.ID != run.ID {
		t.Errorf("status = %+v", got.Status)
	}
	must(t, e.st.SetKillSwitch(ctx(), store.KillSwitch{Paused: true, Since: time.Now()}))
	e.get("/api/sensors/probe").want(200).decode(&got)
	if got.Status.State != "paused" {
		t.Errorf("state while paused = %q", got.Status.State)
	}

	// An invalid file is still listed, flagged invalid.
	e.writeHome("sensors/broken.yaml", "id: broken\nlang: ruby\n")
	e.ws.Store(spec.Load(e.home))
	var list struct {
		Items []struct {
			ID     string                 `json:"id"`
			Status struct{ State string } `json:"status"`
		} `json:"items"`
	}
	e.get("/api/sensors").want(200).decode(&list)
	states := map[string]string{}
	for _, it := range list.Items {
		states[it.ID] = it.Status.State
	}
	if states["broken"] != "invalid" || states["probe"] != "paused" || states["demo-ticker"] != "paused" {
		t.Errorf("states = %v", states)
	}

	// Writes.
	r := e.do("PUT", "/api/sensors/probe", map[string]any{"expected_hash": "0000000000000000000000000000000000000000000000000000000000000000", "yaml": yamlText}).want(409)
	if r.problem() != "conflict" {
		t.Errorf("stale hash: %s", r.problem())
	}
	e.do("PUT", "/api/sensors/probe", map[string]any{"expected_hash": d.Hash, "sensor": map[string]any{
		"id": "probe", "cron": "*/10 * * * *", "lang": "bash", "script": "echo '[]'", "key": "{{.id}}", "first_run": "fire", "timeout": "30s",
	}}).want(200)
	if !strings.Contains(e.readHome("sensors/probe.yaml"), "cron:") || !strings.Contains(e.readHome("sensors/probe.yaml"), "first_run: fire") {
		t.Errorf("file = %q", e.readHome("sensors/probe.yaml"))
	}
	var cur fileDetail
	e.get("/api/sensors/probe").want(200).decode(&cur)
	if r = e.do("PUT", "/api/sensors/probe", map[string]any{"expected_hash": cur.Hash, "yaml": "id: probe\nlang: ruby\n"}).want(422); !contains(diagCodes(r), "invalid_enum") {
		t.Errorf("invalid lang: %s", r.Body)
	}
	if r = e.do("POST", "/api/sensors/validate", map[string]any{"yaml": "id: x\nlang: bash\nscript: ls\ninterval: 1m\nkey: k"}).want(200); r.json()["valid"] != true {
		t.Errorf("validate: %s", r.Body)
	}
	if r = e.do("POST", "/api/sensors/validate", map[string]any{"yaml": "id: x\nlang: bash\n"}).want(200); r.json()["valid"] != false || r.json()["yaml"] == nil {
		t.Errorf("validate invalid: %s", r.Body)
	}
	e.do("DELETE", "/api/sensors/probe?expected_hash="+cur.Hash, nil).want(204)
	e.get("/api/sensors/probe").want(404)
	e.do("DELETE", "/api/sensors/probe?expected_hash="+cur.Hash, nil).want(404)
}

func TestPromptFilesAndRendering(t *testing.T) {
	e := newEnv(t)
	md := "---\nname: greet\ndescription: Say hi\noutput: none\n---\nHello {{.input.name}}.\n"
	created := e.do("POST", "/api/prompts", map[string]any{"markdown": md}).want(201)
	var p struct {
		Name   string `json:"name"`
		Hash   string `json:"hash"`
		Prompt struct {
			Description string `json:"description"`
			Output      string `json:"output"`
			Body        string `json:"body"`
		} `json:"prompt"`
		Markdown string `json:"markdown"`
	}
	created.decode(&p)
	if p.Name != "greet" || p.Prompt.Description != "Say hi" || p.Prompt.Body != "Hello {{.input.name}}.\n" || p.Markdown != strings.Replace(md, "name: greet\n", "", 1) || e.reloads.Load() != 1 {
		t.Errorf("prompt = %+v", p)
	}
	e.do("POST", "/api/prompts", map[string]any{"markdown": md}).want(409)
	e.do("POST", "/api/prompts", map[string]any{"markdown": "no name here\n"}).want(422)

	// Render against a pasted input and a stored event.
	render := func(body map[string]any) map[string]any {
		return e.do("POST", "/api/prompts/render", body).want(200).json()
	}
	if got := render(map[string]any{"name": "greet", "input": map[string]any{"name": "Ada"}}); got["rendered"] != "Hello Ada.\n" {
		t.Errorf("render input = %v", got)
	}
	ev := e.event("gh", "k", `{"name":"Grace"}`)
	if got := render(map[string]any{"name": "greet", "event_id": ev.ID, "extra_prompt": "Be brief."}); got["rendered"] != "Hello Grace.\n\n\nBe brief." {
		t.Errorf("render event = %q", got["rendered"])
	}
	if got := render(map[string]any{"markdown": "Hi {{.input.missing}}", "input": map[string]any{}}); got["rendered"] != nil || !contains(diagCodes(&resp{t: t, Body: mustMarshal(t, got)}), "invalid_template") {
		t.Errorf("template error should be a diagnostic, got %v", got)
	}
	e.do("POST", "/api/prompts/render", map[string]any{"name": "nope", "input": 1}).want(404)
	e.do("POST", "/api/prompts/render", map[string]any{"name": "greet"}).want(400)
	e.do("POST", "/api/prompts/render", map[string]any{"name": "greet", "markdown": "x", "input": 1}).want(400)

	// Update, conflict, delete.
	e.do("PUT", "/api/prompts/greet", map[string]any{"expected_hash": p.Hash, "prompt": map[string]any{"name": "greet", "description": "Say hello", "output": "note", "body": "Hi.\n"}}).want(200)
	if !strings.Contains(e.readHome("prompts/greet.md"), "output: note") {
		t.Errorf("file = %q", e.readHome("prompts/greet.md"))
	}
	e.do("PUT", "/api/prompts/greet", map[string]any{"expected_hash": p.Hash, "markdown": md}).want(409)
	var cur struct{ Hash string }
	e.get("/api/prompts/greet").want(200).decode(&cur)
	e.do("DELETE", "/api/prompts/greet?expected_hash="+cur.Hash, nil).want(204)
	e.get("/api/prompts/greet").want(404)
	var list struct{ Items []struct{ Name string } }
	e.get("/api/prompts").want(200).decode(&list)
	for _, it := range list.Items {
		if it.Name == "greet" {
			t.Error("deleted prompt is still listed")
		}
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	must(t, err)
	return b
}

const configWithSecrets = `# keep this comment
max_agent_runs: 3
runtimes:
  claude:
    command: [claude-agent-acp]
    modes: {ask: default, full: bypassPermissions}
    env: {ANTHROPIC_API_KEY: sk-live-runtime-secret}
auth:
  stacks:
    default:
      - {module: localhost-trust, control: sufficient}
      - module: password
        control: sufficient
        options:
          hash: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$aGFzaGhhc2g"
          nested: {api_token: tok-nested-secret, label: visible}
`

func TestSettingsAreRedactedAndPatchedInPlace(t *testing.T) {
	e := newEnv(t)
	e.writeHome("config.yaml", configWithSecrets)

	r := e.get("/api/settings").want(200)
	for _, secret := range []string{"argon2id", "sk-live-runtime-secret", "tok-nested-secret", "aGFzaGhhc2g"} {
		if strings.Contains(string(r.Body), secret) {
			t.Errorf("settings leak %q: %s", secret, r.Body)
		}
	}
	for _, want := range []string{redacted, `"label":"visible"`, `"max_agent_runs":3`, "claude-agent-acp"} {
		if !strings.Contains(string(r.Body), want) {
			t.Errorf("settings lack %q: %s", want, r.Body)
		}
	}
	var s0 struct{ Hash string }
	r.decode(&s0)

	patched := e.do("PATCH", "/api/settings", map[string]any{
		"expected_hash": s0.Hash, "max_agent_runs": 5, "approval_timeout": "90m", "notify_min_priority": "urgent", "max_steers_per_run": 7,
	}).want(200)
	if strings.Contains(string(patched.Body), "argon2id") {
		t.Errorf("patch response leaks the hash: %s", patched.Body)
	}
	var s1 struct {
		Hash   string
		Config map[string]any
	}
	patched.decode(&s1)
	if s1.Hash == s0.Hash || s1.Config["max_agent_runs"] != float64(5) || s1.Config["approval_timeout"] != "1h30m" || s1.Config["notify_min_priority"] != "urgent" || s1.Config["max_steers_per_run"] != float64(7) {
		t.Errorf("patched config = %v", s1)
	}
	file := e.readHome("config.yaml")
	for _, keep := range []string{"# keep this comment", "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$aGFzaGhhc2g", "sk-live-runtime-secret", "tok-nested-secret"} {
		if !strings.Contains(file, keep) {
			t.Errorf("patching rewrote the file and lost %q:\n%s", keep, file)
		}
	}
	if !strings.Contains(file, "max_agent_runs: 5") || !strings.Contains(file, "max_steers_per_run: 7") || e.reloads.Load() != 1 {
		t.Errorf("file not patched or no reload (%d):\n%s", e.reloads.Load(), file)
	}

	if r := e.do("PATCH", "/api/settings", map[string]any{"expected_hash": s0.Hash, "max_agent_runs": 9}).want(409); r.problem() != "conflict" {
		t.Errorf("stale patch: %s", r.Body)
	}
	for _, bad := range []map[string]any{{"max_agent_runs": 0}, {"retention_days": -3}, {"notify_min_priority": "loud"}, {"approval_timeout": "0s"}, {"max_steers_per_run": 0}, {"max_steers_per_run": 101}, {}} {
		if r := e.do("PATCH", "/api/settings", bad); r.Status != 422 {
			t.Errorf("PATCH %v -> %d %s", bad, r.Status, r.Body)
		}
	}
	e.do("PATCH", "/api/settings", map[string]any{"runtimes": map[string]any{}}).want(400)
	if got := e.readHome("config.yaml"); got != file {
		t.Error("rejected patches changed config.yaml")
	}
	// Without expected_hash the patch applies to whatever is on disk.
	e.do("PATCH", "/api/settings", map[string]any{"retention_days": 14}).want(200)
	if !strings.Contains(e.readHome("config.yaml"), "retention_days: 14") {
		t.Error("retention_days not written")
	}
}

func TestEnvValuesAreWriteOnly(t *testing.T) {
	e := newEnv(t)
	e.writeHome("env", "# secrets\nSLACK_TOKEN=xoxp-old-value # trailing\nLINEAR_API_KEY=lin-key\n")

	e.do("PUT", "/api/env/SLACK_TOKEN", map[string]any{"value": "xoxp-new-value"}).want(204)
	e.do("PUT", "/api/env/GITHUB_TOKEN", map[string]any{"value": `tricky "quoted" \ back $HOME` + "\nsecond line"}).want(204)
	if e.reloads.Load() != 2 {
		t.Errorf("reloads = %d", e.reloads.Load())
	}
	file := e.readHome("env")
	if !strings.HasPrefix(file, "# secrets\n") || strings.Contains(file, "xoxp-old-value") || !strings.Contains(file, "LINEAR_API_KEY=lin-key") {
		t.Errorf("env file after set:\n%s", file)
	}
	parsed, ds := spec.ParseEnv([]byte(file))
	if spec.HasErrors(ds) || parsed.Vars["SLACK_TOKEN"] != "xoxp-new-value" || parsed.Vars["GITHUB_TOKEN"] != `tricky "quoted" \ back $HOME`+"\nsecond line" {
		t.Errorf("env round trip: %v %v", parsed.Vars, ds)
	}
	if info, err := os.Stat(filepath.Join(e.home, "env")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("env mode = %v %v", info, err)
	}

	var keys struct{ Items []struct{ Key string } }
	r := e.get("/api/env").want(200)
	r.decode(&keys)
	if len(keys.Items) != 3 || keys.Items[0].Key != "GITHUB_TOKEN" || keys.Items[1].Key != "LINEAR_API_KEY" || keys.Items[2].Key != "SLACK_TOKEN" {
		t.Errorf("keys = %+v", keys)
	}

	// No endpoint returns a value.
	for _, path := range []string{"/api/env", "/api/settings", "/api/flows", "/api/sensors", "/api/prompts", "/api/runtimes", "/api/version", "/api/status"} {
		body := string(e.get(path).want(200).Body)
		for _, secret := range []string{"xoxp-new-value", "lin-key", "tricky", "second line"} {
			if strings.Contains(body, secret) {
				t.Errorf("GET %s returned the secret %q", path, secret)
			}
		}
	}
	// GET on a single key does not exist: values cannot be read back.
	e.raw("GET", "/api/env/SLACK_TOKEN").want(404)

	e.do("DELETE", "/api/env/LINEAR_API_KEY", nil).want(204)
	if strings.Contains(e.readHome("env"), "LINEAR_API_KEY") || !strings.Contains(e.readHome("env"), "# secrets") {
		t.Errorf("env after delete:\n%s", e.readHome("env"))
	}
	e.do("DELETE", "/api/env/LINEAR_API_KEY", nil).want(404)
	for _, key := range []string{"1BAD", "has-dash", strings.Repeat("K", 129)} {
		if r := e.do("PUT", "/api/env/"+url.PathEscape(key), map[string]any{"value": "x"}); r.Status != 422 {
			t.Errorf("key %q -> %d", key, r.Status)
		}
	}
	e.do("PUT", "/api/env/OK_KEY", map[string]any{}).want(422)
	e.do("PUT", "/api/env/OK_KEY", map[string]any{"value": "a", "extra": 1}).want(400)
}

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type sandboxDetailJSON struct {
	Backend    string `json:"backend"`
	Configured bool   `json:"configured"`
	Hash       string `json:"hash"`
	Config     map[string]any
	Diag       []map[string]any `json:"diagnostics"`
}

func (d *Daemon) sandbox(backend string) sandboxDetailJSON {
	d.t.Helper()
	var s sandboxDetailJSON
	resp := d.Get("/api/sandboxes/" + backend).Want(200)
	resp.JSON(&s)
	var raw struct {
		Config map[string]any `json:"config"`
	}
	resp.JSON(&raw)
	s.Config = raw.Config
	return s
}

// Sandbox backends are configured through the API: defaults before a file exists, optimistic-concurrency
// writes, validation, per-backend env with plain/secret flags, and a connectivity check (local: the root is
// writable). The CLI drives the same endpoints.
func TestSandboxConfigAndCheck(t *testing.T) {
	d := newDaemon(t)
	d.Start()

	var list struct {
		Items []struct {
			Backend    string `json:"backend"`
			Configured bool   `json:"configured"`
			Enabled    bool   `json:"enabled"`
		} `json:"items"`
	}
	d.Get("/api/sandboxes").Want(200).JSON(&list)
	var names []string
	for _, it := range list.Items {
		names = append(names, it.Backend)
		if it.Configured || it.Enabled != (it.Backend == "local") { // local is always on
			t.Errorf("%s is configured or enabled before any write: %+v", it.Backend, it)
		}
	}
	if got := strings.Join(names, ","); got != "local,docker,depot,kubernetes,ssh" {
		t.Fatalf("backends = %s", got)
	}
	d.Get("/api/sandboxes/nope").Want(404)

	cur := d.sandbox("local")
	if cur.Configured || cur.Config["lifecycle"] != "per_run" || cur.Config["approval"] != "rules" || cur.Config["keep_alive"] != "1m" {
		t.Fatalf("defaults = %+v", cur)
	}
	root := filepath.Join(t.TempDir(), "sbx")
	cfg := cur.Config
	cfg["enabled"], cfg["root"], cfg["approval"], cfg["keep_alive"] = true, root, "rules", "90s"
	cfg["approval_rules"] = map[string]any{
		"shell":    []any{map[string]any{"match": "^ls\\b", "action": "auto"}},
		"fs_write": []any{map[string]any{"path": "notes/**", "action": "auto"}},
	}
	d.Put("/api/sandboxes/local", map[string]any{"expected_hash": cur.Hash, "config": cfg}).Want(200)

	saved := d.sandbox("local")
	if !saved.Configured || saved.Hash == cur.Hash || saved.Config["root"] != root {
		t.Fatalf("after write: %+v", saved)
	}
	if yaml := d.Read("sandboxes/local.yaml"); !strings.Contains(yaml, "root: "+root) || !strings.Contains(yaml, "match: ^ls\\b") || !strings.Contains(yaml, "keep_alive: 1m30s") {
		t.Fatalf("file:\n%s", yaml)
	}
	// A write based on the stale hash conflicts.
	d.Put("/api/sandboxes/local", map[string]any{"expected_hash": cur.Hash, "config": cfg}).Want(409)
	// Invalid content is a 422 with diagnostics and writes nothing.
	bad := map[string]any{}
	for k, v := range cfg {
		bad[k] = v
	}
	bad["lifecycle"] = "forever"
	bad["approval_rules"] = map[string]any{"shell": []any{map[string]any{"match": "(", "action": "auto"}}, "fs_write": []any{}}
	resp := d.Put("/api/sandboxes/local", map[string]any{"expected_hash": saved.Hash, "config": bad}).Want(422)
	if !strings.Contains(resp.String(), "lifecycle") || !strings.Contains(resp.String(), "invalid_regex") {
		t.Errorf("422 body: %s", resp)
	}
	// keep_alive takes any duration from 0 up; a negative one is refused and 0 is kept as 0, not turned into the default.
	neg := map[string]any{}
	for k, v := range cfg {
		neg[k] = v
	}
	neg["keep_alive"] = "-1m"
	if resp := d.Put("/api/sandboxes/local", map[string]any{"expected_hash": saved.Hash, "config": neg}).Want(422); !strings.Contains(resp.String(), "keep_alive") {
		t.Errorf("422 body: %s", resp)
	}
	if d.sandbox("local").Hash != saved.Hash {
		t.Error("a rejected write changed the file")
	}
	neg["keep_alive"] = "0s"
	d.Put("/api/sandboxes/local", map[string]any{"expected_hash": saved.Hash, "config": neg}).Want(200)
	if got := d.sandbox("local").Config["keep_alive"]; got != "0s" {
		t.Errorf("keep_alive 0s was stored as %v", got)
	}
	cfg["keep_alive"] = "0s"
	saved = d.sandbox("local")
	d.Put("/api/sandboxes/ssh", map[string]any{"expected_hash": d.sandbox("ssh").Hash,
		"config": func() map[string]any { c := d.sandbox("ssh").Config; c["enabled"] = true; c["host"] = ""; return c }()}).Want(422)
	if d.sandbox("local").Hash != saved.Hash {
		t.Error("a rejected write changed the file")
	}

	var check struct {
		OK          bool    `json:"ok"`
		Message     string  `json:"message"`
		Fingerprint *string `json:"fingerprint"`
	}
	d.Post("/api/sandboxes/local/check", nil).Want(200).JSON(&check)
	if !check.OK || check.Fingerprint != nil {
		t.Fatalf("check = %+v", check)
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("check did not create the root: %v", err)
	}
	cfg["root"] = "relative/dir"
	d.Post("/api/sandboxes/local/check", map[string]any{"config": cfg}).Want(200).JSON(&check)
	if check.OK || !strings.Contains(check.Message, "absolute") {
		t.Fatalf("check of a relative root = %+v", check)
	}

	// Per-backend env: secret by default, plain returns its value, never leaks into the config.
	d.Put("/api/sandboxes/ssh/env/SSH_PASSWORD", map[string]any{"value": "hunter2-pass"}).Want(204)
	d.Put("/api/sandboxes/ssh/env/MODE", map[string]any{"value": "fast", "secret": false}).Want(204)
	var envs struct {
		Items []struct {
			Key    string  `json:"key"`
			Secret bool    `json:"secret"`
			Value  *string `json:"value"`
		} `json:"items"`
	}
	r := d.Get("/api/sandboxes/ssh/env").Want(200)
	r.JSON(&envs)
	if strings.Contains(r.String(), "hunter2") || len(envs.Items) != 2 || envs.Items[0].Key != "MODE" || envs.Items[0].Value == nil || *envs.Items[0].Value != "fast" || !envs.Items[1].Secret {
		t.Fatalf("env list: %s", r)
	}
	if got := d.Read("env.d/sandboxes/ssh.env"); !strings.Contains(got, "SSH_PASSWORD") {
		t.Errorf("env file: %q", got)
	}
	d.Delete("/api/sandboxes/ssh/env/MODE").Want(204)
	d.Delete("/api/sandboxes/ssh/env/MODE").Want(404)
	d.Put("/api/sandboxes/nope/env/X", map[string]any{"value": "1"}).Want(404)

	// The CLI is a client of the same endpoints.
	d.sameAsAPI("sandboxes get", d.cliJSON("", "sandboxes", "get", "local"), "/api/sandboxes/local")
	d.sameAsAPI("sandboxes list", d.cliJSON("", "sandboxes", "list"), "/api/sandboxes")
	if out := d.cliJSON(`{"timeout": "90s"}`, "sandboxes", "set", "local").(map[string]any); out["config"].(map[string]any)["timeout"] != "1m30s" {
		t.Fatalf("sandboxes set: %v", out)
	}
	if got := d.sandbox("local"); got.Config["enabled"] != true || got.Config["timeout"] != "1m30s" || got.Config["root"] != root {
		t.Fatalf("set did not merge over the current config: %+v", got.Config)
	}
	if out := d.cliJSON("", "sandboxes", "check", "local").(map[string]any); out["ok"] != true {
		t.Fatalf("sandboxes check: %v", out)
	}
	if _, _, code := d.CLI("", "sandboxes", "env", "set", "docker", "DOCKER_HOST", "unix:///x.sock", "--plain"); code != 0 {
		t.Fatal("sandboxes env set failed")
	}
	if out := d.cliJSON("", "sandboxes", "env", "list", "docker").(map[string]any); len(out["items"].([]any)) != 1 {
		t.Fatalf("sandboxes env list: %v", out)
	}
	if _, _, code := d.CLI("", "sandboxes", "get", "nope"); code == 0 {
		t.Error("unknown backend must fail")
	}
}

// Enabling depot needs DEPOT_TOKEN in env.d/sandboxes/depot.env; the token is never part of depot.yaml.
func TestDepotRequiresToken(t *testing.T) {
	d := newDaemon(t)
	d.Start()
	cur := d.sandbox("depot")
	cfg := cur.Config
	cfg["enabled"], cfg["org_id"] = true, "org-1"
	resp := d.Put("/api/sandboxes/depot", map[string]any{"expected_hash": cur.Hash, "config": cfg}).Want(422)
	if !strings.Contains(resp.String(), "Depot token is required") {
		t.Fatalf("422 body: %s", resp)
	}
	d.Put("/api/sandboxes/depot/env/DEPOT_TOKEN", map[string]any{"value": "tok-123456"}).Want(204)
	d.Put("/api/sandboxes/depot", map[string]any{"expected_hash": cur.Hash, "config": cfg}).Want(200)
	if yaml := d.Read("sandboxes/depot.yaml"); strings.Contains(yaml, "tok-123456") {
		t.Fatalf("token leaked into depot.yaml:\n%s", yaml)
	}
}

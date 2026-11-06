package e2e

import (
	"os"
	"strings"
	"testing"
)

type toolViewJSON struct {
	ID     string         `json:"id"`
	Hash   string         `json:"hash"`
	YAML   string         `json:"yaml"`
	Schema map[string]any `json:"schema"`
	Tool   *struct {
		Approve any `json:"approve"`
	} `json:"tool"`
	Diagnostics []map[string]any `json:"diagnostics"`
}

type toolTestJSON struct {
	OK         bool           `json:"ok"`
	Stdout     string         `json:"stdout"`
	Stderr     string         `json:"stderr"`
	Structured map[string]any `json:"structured"`
	ExitCode   int            `json:"exit_code"`
	Args       map[string]any `json:"args"`
	Error      *string        `json:"error"`
}

const greetTool = `id: greet
description: Greet someone
params:
  name: {type: string, required: true}
  times: {type: integer, default: 2}
lang: bash
script: |
  echo "{\"hello\": \"$ARG_NAME\", \"times\": $ARG_TIMES, \"token\": \"$GREET_TOKEN\", \"stdin\": $(cat)}"
approve: never
`

// A custom tool is created, read, tested (args on stdin and ARG_ env, secrets
// redacted, failures reported), updated with a hash check and deleted with its env.
func TestToolsCRUDAndTest(t *testing.T) {
	d := newDaemon(t)
	d.Start()

	var created toolViewJSON
	d.Post("/api/tools", map[string]any{"yaml": greetTool}).Want(201).JSON(&created)
	if created.ID != "greet" || created.Schema["type"] != "object" || created.Hash == "" {
		t.Fatalf("created: %+v", created)
	}
	d.Post("/api/tools", map[string]any{"yaml": greetTool}).Want(409)
	d.Post("/api/tools", map[string]any{"yaml": "id: bad\nlang: bash\n"}).Want(422)

	var list struct {
		Items []toolViewJSON `json:"items"`
	}
	d.Get("/api/tools").Want(200).JSON(&list)
	if len(list.Items) != 1 || list.Items[0].ID != "greet" {
		t.Fatalf("list: %+v", list)
	}

	d.Put("/api/tools/greet/env/GREET_TOKEN", map[string]any{"value": "tool-secret-value-42"}).Want(204)
	resp := d.Get("/api/tools/greet/env").Want(200)
	if strings.Contains(string(resp.Body), "tool-secret-value-42") {
		t.Fatalf("env list leaked the secret: %s", resp.Body)
	}
	if _, err := os.Stat(d.Path("env.d/tools/greet.env")); err != nil {
		t.Fatalf("tool env file: %v", err)
	}

	var res toolTestJSON
	d.Post("/api/tools/greet/test", map[string]any{"args": map[string]any{"name": "ada"}}).Want(200).JSON(&res)
	if !res.OK || res.Structured["hello"] != "ada" || res.Structured["times"] != float64(2) {
		t.Fatalf("test result: %+v", res)
	}
	if res.Structured["token"] != "«GREET_TOKEN»" || strings.Contains(res.Stdout, "tool-secret") {
		t.Errorf("secret not redacted: %s", res.Stdout)
	}
	if stdin, _ := res.Structured["stdin"].(map[string]any); stdin["name"] != "ada" {
		t.Errorf("stdin args: %v", res.Structured["stdin"])
	}
	d.Post("/api/tools/greet/test", map[string]any{"args": map[string]any{}}).Want(422)
	d.Post("/api/tools/nope/test", map[string]any{}).Want(404)

	// Edit through the structured form: a failing script is a 200 with ok=false.
	var updated toolViewJSON
	d.Put("/api/tools/greet", map[string]any{"expected_hash": created.Hash, "tool": map[string]any{
		"id": "greet", "description": "Fails", "lang": "bash",
		"script":  "echo broken >&2\nexit 4\n",
		"approve": map[string]any{"when": map[string]any{"conditions": []any{map[string]any{"field": "x", "op": "exists"}}}},
	}}).Want(200).JSON(&updated)
	if updated.Hash == created.Hash || updated.Tool == nil {
		t.Fatalf("updated: %+v", updated)
	}
	d.Put("/api/tools/greet", map[string]any{"expected_hash": created.Hash, "yaml": greetTool}).Want(409)
	d.Post("/api/tools/greet/test", map[string]any{}).Want(200).JSON(&res)
	if res.OK || res.ExitCode != 4 || res.Error == nil || !strings.Contains(*res.Error, "broken") {
		t.Fatalf("failing tool: %+v", res)
	}

	var v struct {
		Valid bool `json:"valid"`
	}
	d.Post("/api/tools/validate", map[string]any{"yaml": "id: x\ndescription: d\nlang: bash\n"}).Want(200).JSON(&v)
	if v.Valid {
		t.Errorf("a tool without a script validated")
	}

	d.Delete("/api/tools/greet?expected_hash=" + updated.Hash).Want(204)
	d.Get("/api/tools/greet").Want(404)
	if _, err := os.Stat(d.Path("env.d/tools/greet.env")); !os.IsNotExist(err) {
		t.Errorf("tool env survived the tool: %v", err)
	}
}

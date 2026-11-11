package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hal9001/internal/agent/fakeagent"
)

// A runtime with only a `kind` has no agent until the adapter is installed: its node fails with the fix, the CLI
// installs the adapter (a fake npm lays it out), and the same flow then runs against it. The "adapter" is a stub
// node that starts the scripted fake ACP agent, so no network or real model is involved.
func TestRuntimeWithoutCommandRunsOnceItsAdapterIsInstalled(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "note", "Triage {{.input.id}}")

	dir := filepath.Join(d.Home, "fake", "claude")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(dir, "script.json")
	s := script{
		Modes:       []fakeagent.Mode{{ID: "default"}},
		SessionsDir: filepath.Join(dir, "sessions"), LogPath: filepath.Join(dir, "calls.jsonl"),
		Turns: []turn{{Steps: []step{say(`Done. {"title":"From the managed adapter","priority":"low","summary_md":"ok"}`)}}},
	}
	if err := s.Write(scriptPath); err != nil {
		t.Fatal(err)
	}

	d.Script("claude", `case "$1" in --version) echo "2.1.311 (Claude Code)";; auth) echo '{"loggedIn": true}';; esac`)
	d.Script("node", `case "$1" in --version) echo v24.1.0; exit 0;; esac
exec `+shq(fakeAgentBin)+` --script `+shq(scriptPath))
	d.Script("npm", `[ "$1" = "--version" ] && { echo 10.9.0; exit 0; }
dir="$3"; pkgspec="$4"; pkg="${pkgspec%@*}"; ver="${pkgspec##*@}"
mkdir -p "$dir/node_modules/$pkg/dist"
echo "{\"name\":\"$pkg\",\"version\":\"$ver\",\"bin\":{\"${pkg##*/}\":\"dist/index.js\"}}" > "$dir/node_modules/$pkg/package.json"
echo "// adapter" > "$dir/node_modules/$pkg/dist/index.js"`)
	d.Config("runtimes:\n  claude: {kind: claude}\n")
	d.Flow("f", `id: f
enabled: true
subscribe: [src]
nodes:
  - {id: work, type: agent, runtime: claude, mode: readonly, prompt: p}
`)
	d.Start()

	var list struct {
		Items []struct {
			Name      string `json:"name"`
			Detection struct {
				Status  string `json:"status"`
				Adapter struct {
					Source string `json:"source"`
				} `json:"adapter"`
			} `json:"detection"`
		} `json:"items"`
	}
	d.Get("/api/runtimes").Want(200).JSON(&list)
	if len(list.Items) != 1 || list.Items[0].Detection.Status != "unavailable" || list.Items[0].Detection.Adapter.Source != "none" {
		t.Fatalf("before install: %+v", list.Items)
	}

	failed := d.InjectRun("src", map[string]any{"id": "1"})
	det := d.WaitRun(failed, "failed")
	if reason := d.Node(det, "work").Reason; !strings.Contains(reason, "hal9001 runtimes install claude") {
		t.Errorf("the failed node does not name the fix: %q", reason)
	}

	if out, errOut, code := d.CLI("", "runtimes", "install", "claude"); code != 0 || !strings.Contains(out, "installed the ACP adapter for claude") {
		t.Fatalf("runtimes install: exit %d\n%s\n%s", code, out, errOut)
	}
	if out, _, code := d.CLI("", "runtimes", "status"); code != 0 || !strings.Contains(out, "claude: READY") || !strings.Contains(out, "managed, installed 0.100.0") {
		t.Errorf("runtimes status: exit %d\n%s", code, out)
	}

	run := d.RunOf("src", map[string]any{"id": "2"})
	if d.Node(run, "work").Status != "done" {
		t.Errorf("node: %+v", d.Node(run, "work"))
	}
	if items := d.Inbox(); len(items) != 1 || items[0].Title != "From the managed adapter" {
		t.Errorf("inbox: %+v", items)
	}

	if out, errOut, code := d.CLI("", "runtimes", "uninstall", "claude"); code != 0 {
		t.Fatalf("runtimes uninstall: exit %d\n%s\n%s", code, out, errOut)
	}
	d.Get("/api/runtimes").Want(200).JSON(&list)
	if list.Items[0].Detection.Status != "unavailable" {
		t.Errorf("after uninstall: %+v", list.Items[0])
	}
}

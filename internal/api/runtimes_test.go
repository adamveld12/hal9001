package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hal9001/internal/runtimes"
	"hal9001/internal/spec"
)

// runtimeLab is a PATH of stub executables for the runtime manager of an env.
type runtimeLab struct {
	t   *testing.T
	bin string
}

func newRuntimeLab(t *testing.T) *runtimeLab {
	t.Helper()
	return &runtimeLab{t: t, bin: t.TempDir()}
}

func (l *runtimeLab) stub(name, body string) {
	l.t.Helper()
	if err := os.WriteFile(filepath.Join(l.bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil { //nolint:gosec // test stub
		l.t.Fatal(err)
	}
}

// fakeNPM lays an adapter package out as npm does; gate, when set, holds the install until the file exists.
func (l *runtimeLab) fakeNPM(gate string) {
	wait := ""
	if gate != "" {
		wait = `while [ ! -f "` + gate + `" ]; do sleep 0.02; done`
	}
	l.stub("npm", `[ "$1" = "--version" ] && { echo 10.9.0; exit 0; }
`+wait+`
echo "fake npm $@"
dir="$3"; pkgspec="$4"; pkg="${pkgspec%@*}"; ver="${pkgspec##*@}"
mkdir -p "$dir/node_modules/$pkg/dist"
echo "{\"name\":\"$pkg\",\"version\":\"$ver\",\"bin\":{\"${pkg##*/}\":\"dist/index.js\"}}" > "$dir/node_modules/$pkg/package.json"
echo "// js" > "$dir/node_modules/$pkg/dist/index.js"`)
}

func (l *runtimeLab) env() func(*envOptions) {
	return func(eo *envOptions) {
		eo.runtimes = func(e *env) *runtimes.Manager {
			return runtimes.New(runtimes.Options{
				Data: filepath.Join(e.home, "data"), Path: l.bin + ":/usr/bin:/bin", ExactPath: true, UserHome: l.t.TempDir(),
			})
		}
	}
}

type runtimeItemBody struct {
	Name      string  `json:"name"`
	Kind      *string `json:"kind"`
	Command   []string
	Detection struct {
		Status string `json:"status"`
		Auth   struct {
			Status string `json:"status"`
		} `json:"auth"`
		Adapter struct {
			Source      string  `json:"source"`
			Installed   bool    `json:"installed"`
			Installable bool    `json:"installable"`
			Installing  bool    `json:"installing"`
			Version     *string `json:"version"`
		} `json:"adapter"`
		Command  []string `json:"command"`
		Warnings []struct {
			Code string `json:"code"`
		} `json:"warnings"`
	} `json:"detection"`
}

func (r runtimeItemBody) codes() string {
	var c []string
	for _, w := range r.Detection.Warnings {
		c = append(c, w.Code)
	}
	return strings.Join(c, ",")
}

func runtimeItems(t *testing.T, r *resp) map[string]runtimeItemBody {
	t.Helper()
	var list struct {
		Items []runtimeItemBody `json:"items"`
	}
	r.decode(&list)
	out := map[string]runtimeItemBody{}
	for _, it := range list.Items {
		out[it.Name] = it
	}
	return out
}

func TestRuntimesListDetectsTheStarterRuntimes(t *testing.T) {
	l := newRuntimeLab(t)
	l.stub("claude", `case "$1" in --version) echo "2.1.311 (Claude Code)";; auth) echo '{"loggedIn": false}'; exit 1;; esac`)
	l.stub("omp", `case "$1" in --version) echo omp/18.18.7;; models) echo '{"models":[{"provider":"anthropic"}]}';; esac`)
	l.stub("node", "echo v24.1.0")
	e := newEnv(t, l.env())

	items := runtimeItems(t, e.get("/api/runtimes").want(200))
	if len(items) != 3 {
		t.Fatalf("items = %v", items)
	}
	claude, codex, omp := items["claude"], items["codex"], items["omp"]
	if claude.Kind == nil || *claude.Kind != "claude" || len(claude.Command) != 0 {
		t.Errorf("claude kind/command = %v %v", claude.Kind, claude.Command)
	}
	if claude.Detection.Status != "unavailable" || claude.codes() != "adapter_missing,auth_missing" || !claude.Detection.Adapter.Installable {
		t.Errorf("claude = %+v codes %s", claude.Detection, claude.codes())
	}
	if codex.Detection.Status != "unavailable" || !strings.Contains(codex.codes(), "cli_missing") {
		t.Errorf("codex = %+v codes %s", codex.Detection, codex.codes())
	}
	if omp.Detection.Status != "ready" || omp.Detection.Adapter.Source != "builtin" || len(omp.Detection.Command) != 2 || omp.Detection.Command[1] != "acp" {
		t.Errorf("omp = %+v", omp.Detection)
	}
}

func TestInstallAdapterThroughTheAPIStreamsProgressAndMakesTheRuntimeRunnable(t *testing.T) {
	l := newRuntimeLab(t)
	l.stub("claude", `case "$1" in --version) echo "2.1.311 (Claude Code)";; auth) echo '{"loggedIn": true}';; esac`)
	l.stub("node", "echo v24.1.0")
	gate := filepath.Join(t.TempDir(), "gate")
	l.fakeNPM(gate)
	e := newEnv(t, l.env())
	ts := httptest.NewServer(e.srv)
	t.Cleanup(ts.Close)
	stream := e.openStream(ts, "?types=runtime.adapter", nil)
	stream.expect("stream.ready")

	started := e.do("POST", "/api/runtimes/claude/adapter", nil).want(202)
	var item runtimeItemBody
	started.decode(&item)
	if !item.Detection.Adapter.Installing || item.Detection.Adapter.Installed {
		t.Errorf("accepted item = %+v", item.Detection.Adapter)
	}
	e.do("POST", "/api/runtimes/claude/adapter", nil).want(409) // already installing
	e.do("DELETE", "/api/runtimes/claude/adapter", nil).want(409)
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var sawLine bool
	for {
		m := stream.expect("runtime.adapter")
		if m.Data["data"].(map[string]any)["name"] != "claude" {
			t.Fatalf("event = %v", m.Data)
		}
		d := m.Data["data"].(map[string]any)
		if d["phase"] == "installing" && strings.HasPrefix(d["line"].(string), "fake npm install --prefix") {
			sawLine = true
		}
		if d["phase"] == "done" {
			break
		}
		if d["phase"] == "failed" {
			t.Fatalf("install failed: %v", d["error"])
		}
	}
	if !sawLine {
		t.Error("no npm output line reached the stream")
	}

	claude := runtimeItems(t, e.get("/api/runtimes").want(200))["claude"]
	if claude.Detection.Status != "ready" || claude.Detection.Adapter.Source != "managed" || !claude.Detection.Adapter.Installed ||
		len(claude.Detection.Command) != 2 || !strings.HasSuffix(claude.Detection.Command[1], filepath.Join("claude-agent-acp", "dist", "index.js")) {
		t.Errorf("after install = %+v", claude.Detection)
	}

	var removed runtimeItemBody
	e.do("DELETE", "/api/runtimes/claude/adapter", nil).want(200).decode(&removed)
	if removed.Detection.Adapter.Installed || removed.Detection.Status != "unavailable" {
		t.Errorf("after uninstall = %+v", removed.Detection)
	}
}

func TestInstallAdapterRefusals(t *testing.T) {
	l := newRuntimeLab(t)
	l.fakeNPM("")
	e := newEnv(t, l.env())
	e.do("POST", "/api/runtimes/omp/adapter", nil).want(409)
	e.do("DELETE", "/api/runtimes/omp/adapter", nil).want(409)
	e.do("POST", "/api/runtimes/ghost/adapter", nil).want(404)
	e.do("DELETE", "/api/runtimes/ghost/adapter", nil).want(404)
	// No Node.js on PATH.
	r := e.do("POST", "/api/runtimes/claude/adapter", nil).want(422)
	if body := r.json(); body["code"] != "node_missing" {
		t.Errorf("422 body = %v", body)
	}
	// A runtime with its own command has nothing to install.
	e.writeHome("config.yaml", "runtimes:\n  claude: {kind: claude, command: [my-acp]}\n")
	e.ws.Store(spec.Load(e.home))
	e.do("POST", "/api/runtimes/claude/adapter", nil).want(409)
}

func TestInstallFailureShowsUpAsAWarning(t *testing.T) {
	l := newRuntimeLab(t)
	l.stub("node", "echo v24.1.0")
	l.stub("npm", `[ "$1" = "--version" ] && { echo 10.9.0; exit 0; }
echo "npm ERR! network" >&2; exit 1`)
	e := newEnv(t, l.env())
	ts := httptest.NewServer(e.srv)
	t.Cleanup(ts.Close)
	stream := e.openStream(ts, "?types=runtime.adapter", nil)
	stream.expect("stream.ready")
	e.do("POST", "/api/runtimes/codex/adapter", nil).want(202)
	var failed map[string]any
	for failed == nil {
		d := stream.expect("runtime.adapter").Data["data"].(map[string]any)
		if d["phase"] == "failed" {
			failed = d
		}
	}
	if !strings.Contains(failed["error"].(string), "npm ERR! network") {
		t.Errorf("failed event = %v", failed)
	}
	codex := runtimeItems(t, e.get("/api/runtimes").want(200))["codex"]
	if !strings.Contains(codex.codes(), "adapter_install_failed") || codex.Detection.Adapter.Installing {
		t.Errorf("codex = %+v codes %s", codex.Detection, codex.codes())
	}
}

func TestDetectChecksAgainWhileListIsCached(t *testing.T) {
	l := newRuntimeLab(t)
	l.stub("omp", `case "$1" in --version) echo omp/1;; models) echo '{"models":[{"provider":"apple"}]}';; esac`)
	e := newEnv(t, l.env())
	if omp := runtimeItems(t, e.get("/api/runtimes").want(200))["omp"]; omp.Detection.Auth.Status != "missing" {
		t.Fatalf("omp auth = %s", omp.Detection.Auth.Status)
	}
	l.stub("omp", `case "$1" in --version) echo omp/1;; models) echo '{"models":[{"provider":"anthropic"}]}';; esac`)
	if omp := runtimeItems(t, e.get("/api/runtimes").want(200))["omp"]; omp.Detection.Auth.Status != "missing" {
		t.Errorf("GET re-ran the login check: %s", omp.Detection.Auth.Status)
	}
	if omp := runtimeItems(t, e.do("POST", "/api/runtimes/detect", nil).want(200))["omp"]; omp.Detection.Auth.Status != "ok" || omp.Detection.Status != "ready" {
		t.Errorf("detect = %+v", omp.Detection)
	}
}

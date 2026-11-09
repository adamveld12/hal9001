package assist

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeHome(t *testing.T, files map[string]string) string {
	t.Helper()
	home := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// The reference files and the preamble summary name the enabled sandboxes with their images, the runtimes and the
// custom tools, say where to enable a disabled backend, and never carry env values.
func TestConfigReferenceAndSummary(t *testing.T) {
	home := writeHome(t, map[string]string{
		"config.yaml":                "runtimes:\n  claude:\n    command: [claude-agent-acp]\n    env: {API_KEY: sk-secret-runtime}\n  odd:\n    command: [my-agent]\n",
		"sandboxes/docker.yaml":      "enabled: true\nimages: [alpine:3, ubuntu:24.04]\n",
		"sandboxes/kubernetes.yaml":  "enabled: false\nimages: [team/img:1]\nkubeconfig: /secret/kubeconfig\n",
		"tools/lint.yaml":            "description: Lint a repo.\nparams:\n  path: {type: string, required: true}\nlang: bash\nscript: echo $TOKEN\napprove: never\n",
		"env.d/sandboxes/docker.env": "DOCKER_SECRET=hunter2\n",
		".env":                       "GLOBAL=sk-secret-global\n",
	})
	m := &Manager{home: home}
	dir := t.TempDir()
	if err := m.writeConfigReference(dir); err != nil {
		t.Fatal(err)
	}
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(dir, ReferenceDir, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	all := read("runtimes.txt") + read("sandboxes.md") + read("tools.md") + read("settings.md") + read("docs/agent-nodes.md") + m.configSummary()
	for _, secret := range []string{"sk-secret-runtime", "sk-secret-global", "hunter2", "/secret/kubeconfig", "echo $TOKEN"} {
		if strings.Contains(all, secret) {
			t.Errorf("configuration context leaks %q", secret)
		}
	}
	for file, wants := range map[string][]string{
		"runtimes.txt": {"claude\tkind=claude\tlockdown=ok", "odd\tkind=unknown\tlockdown=none"},
		"sandboxes.md": {"## `docker`: enabled", "`alpine:3`, `ubuntu:24.04`", "## `kubernetes`: disabled", "Configure > Sandboxes > Kubernetes", "## `local`: enabled"},
		"tools.md":     {"### `lint`", "param `path` (string, required)", "runs_in_sandbox: false", "`now`"},
		"settings.md":  {"`max_agent_runs` | 2", "`notify_min_priority` | normal"},
	} {
		got := read(file)
		for _, w := range wants {
			if !strings.Contains(got, w) {
				t.Errorf("%s lacks %q:\n%s", file, w, got)
			}
		}
	}
	sum := m.configSummary()
	for _, w := range []string{"enabled sandboxes: `docker`, `local`", "docker: alpine:3, ubuntu:24.04", "`kubernetes`", "runtimes: `claude`, `odd`", "custom tools: `lint`"} {
		if !strings.Contains(sum, w) {
			t.Errorf("summary lacks %q: %s", w, sum)
		}
	}
}

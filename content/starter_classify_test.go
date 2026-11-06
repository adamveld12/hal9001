package content

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

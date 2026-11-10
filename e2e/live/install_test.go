package live

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hal9001/internal/runner"
)

// `hal9001 service install --dry-run` run from a bare environment (as from a fresh launchd-like login) prints a plist
// that starts the daemon at load and keeps it alive with HAL9001_LAUNCHD=1, which makes the daemon capture the
// login-shell PATH itself; the tools nodes need resolve and run with that PATH from a non-repo working directory.
func TestLiveServiceInstallDryRunPATH(t *testing.T) {
	requireLive(t)
	bin := filepath.Join(t.TempDir(), "hal9001")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/hal9001")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "service", "install", "--dry-run")
	cmd.Env = []string{"HOME=" + home, "USER=" + os.Getenv("USER"), "SHELL=" + shellOrZsh(), "PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("service install --dry-run: %v\n%s", err, out)
	}
	plist := string(out)
	evidence(t, "service-install-dry-run", map[string]string{"plist": plist})
	for _, want := range []string{
		"<key>RunAtLoad</key>\n\t<true/>", "<key>KeepAlive</key>\n\t<true/>", "<string>serve</string>",
		"<key>HAL9001_LAUNCHD</key>\n\t\t<string>1</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist lacks %q", want)
		}
	}

	// What `serve` does under HAL9001_LAUNCHD=1: capture the PATH of a login shell, from launchd's minimal PATH.
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	path, err := runner.LoginShellPATH(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	// launchd starts the daemon in a directory that is never inside a repo, so use an empty temp dir as its stand-in.
	workdir := t.TempDir()
	if !strings.Contains(path, "mise/shims") {
		t.Errorf("PATH has no mise shims dir: %s", path)
	}
	for _, tool := range []string{"gh", "jq", "git", "node", "mise"} {
		c := exec.Command("sh", "-c", "command -v "+tool+" && "+tool+" --version")
		c.Env = []string{"HOME=" + home, "PATH=" + path}
		c.Dir = workdir
		if out, err := c.CombinedOutput(); err != nil {
			t.Errorf("%s does not resolve and run with the login-shell PATH from a non-repo working dir: %v\n%s", tool, err, out)
		} else {
			t.Logf("%s -> %s", tool, strings.SplitN(string(out), "\n", 2)[0])
		}
	}
}

func shellOrZsh() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/zsh"
}

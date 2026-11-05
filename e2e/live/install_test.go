package live

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// `hal9001 install --dry-run` run from a bare environment (as from a fresh
// launchd-like login) prints a plist whose PATH is the login-shell PATH, and
// the tools nodes need resolve and run with that PATH from the plist's
// WorkingDirectory.
func TestLiveInstallDryRunPATH(t *testing.T) {
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
	cmd := exec.Command(bin, "install", "--dry-run")
	cmd.Env = []string{"HOME=" + home, "USER=" + os.Getenv("USER"), "SHELL=" + shellOrZsh(), "PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install --dry-run: %v\n%s", err, out)
	}
	plist := string(out)
	evidence(t, "install-dry-run", map[string]string{"plist": plist})

	value := func(key string) string {
		m := regexp.MustCompile(`<key>` + key + `</key>\s*<string>([^<]*)</string>`).FindStringSubmatch(plist)
		if m == nil {
			t.Fatalf("plist has no string key %s:\n%s", key, plist)
		}
		return m[1]
	}
	for _, want := range []string{"<key>RunAtLoad</key>\n\t<true/>", "<key>KeepAlive</key>\n\t<true/>", "<string>serve</string>"} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist lacks %q", want)
		}
	}
	path := value("PATH")
	// launchd starts the daemon in the home dir, which `serve` creates; it is
	// never inside a repo, so use an empty temp dir as its stand-in.
	workdir := t.TempDir()
	if !strings.Contains(path, "mise/shims") {
		t.Errorf("PATH has no mise shims dir: %s", path)
	}
	for _, tool := range []string{"gh", "jq", "git", "node", "mise"} {
		c := exec.Command("sh", "-c", "command -v "+tool+" && "+tool+" --version")
		c.Env = []string{"HOME=" + home, "PATH=" + path}
		c.Dir = workdir
		if out, err := c.CombinedOutput(); err != nil {
			t.Errorf("%s does not resolve and run with the plist PATH from a non-repo working dir: %v\n%s", tool, err, out)
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

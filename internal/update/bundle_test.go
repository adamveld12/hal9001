package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// infoPlist is shaped like the Info.plist cmd/hal9001-app-pack writes.
func bundlePlistFixture(id, version, build string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key><string>` + id + `</string>
	<key>CFBundleName</key><string>HAL9001 &amp; co</string>
	<key>CFBundleShortVersionString</key><string>` + version + `</string>
	<key>CFBundleVersion</key><string>` + build + `</string>
	<key>LSUIElement</key><true/>
	<key>NSAppTransportSecurity</key><dict><key>CFBundleVersion</key><string>nested, ignored</string></dict>
</dict>
</plist>
`
}

// fakeBundle writes <dir>/<name>/Contents/{Info.plist,Resources/hal9001} and returns the bundle path.
func bundleFixture(t *testing.T, dir, name, plist string) string {
	t.Helper()
	root := filepath.Join(dir, name)
	for path, content := range map[string]string{
		"Contents/Info.plist":        plist,
		"Contents/Resources/hal9001": "daemon",
		"Contents/MacOS/HAL9001":     "app",
	} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o755); err != nil { //nolint:gosec // test fixture
			t.Fatal(err)
		}
	}
	return root
}

func TestReadBundleReadsTheIdentityOfAnAppBundle(t *testing.T) {
	root := bundleFixture(t, t.TempDir(), "HAL9001.app", bundlePlistFixture("dev.hal9001", "0.1.12", "12"))
	got, err := ReadBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != (Current{BundleID: "dev.hal9001", Version: "0.1.12", Build: 12}) {
		t.Fatalf("ReadBundle = %+v (a nested dict must not override the top-level keys)", got)
	}

	info, err := ReadInfoPlist(filepath.Join(root, "Contents", "Info.plist"))
	if err != nil || info["CFBundleName"] != "HAL9001 & co" || info["LSUIElement"] != "true" {
		t.Fatalf("ReadInfoPlist = %v, %v", info, err)
	}
	if _, err := ParseInfoPlist([]byte("bplist00\x00")); err == nil {
		t.Error("a binary plist must be refused, not misread")
	}
	bad := bundleFixture(t, t.TempDir(), "Bad.app", bundlePlistFixture("dev.hal9001", "0.1.1", "twelve"))
	if _, err := ReadBundle(bad); err == nil {
		t.Error("a non-numeric CFBundleVersion must be an error")
	}
}

func TestBundleRootFollowsTheCLISymlink(t *testing.T) {
	dir := t.TempDir()
	root := bundleFixture(t, dir, "HAL9001.app", bundlePlistFixture("dev.hal9001", "0.1.12", "12"))
	daemon := filepath.Join(root, "Contents", "Resources", "hal9001")
	link := filepath.Join(dir, "bin-hal9001")
	if err := os.Symlink(daemon, link); err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(root)
	for _, exe := range []string{daemon, link, filepath.Join(root, "Contents", "MacOS", "HAL9001")} {
		got, err := BundleRoot(exe)
		if err != nil {
			t.Fatalf("%s: %v", exe, err)
		}
		if g, _ := filepath.EvalSymlinks(got); g != want {
			t.Errorf("BundleRoot(%s) = %s, want %s", exe, got, want)
		}
	}
	if _, err := BundleRoot(filepath.Join(dir, "somewhere", "hal9001")); err == nil {
		t.Error("a binary outside a bundle must be ErrNotBundled")
	}
}

func TestDetectDecidesWhetherUpdatesMayRun(t *testing.T) {
	dir := t.TempDir()
	exeOf := func(name, id, build string) string {
		return filepath.Join(bundleFixture(t, dir, name, bundlePlistFixture(id, "0.1."+build, build)), "Contents", "Resources", "hal9001")
	}
	hasKey, noKey := func() bool { return true }, func() bool { return false }
	for _, c := range []struct {
		name   string
		o      DetectOptions
		reason string // substring; empty means enabled
		build  int
	}{
		{"release build with a key", DetectOptions{Exe: exeOf("A.app", "dev.hal9001", "7"), HasPublicKey: hasKey}, "", 7},
		{"outside a bundle", DetectOptions{Exe: filepath.Join(dir, "bin", "hal9001"), HasPublicKey: hasKey}, "not running from an app bundle", 0},
		{"dev app", DetectOptions{Exe: exeOf("B.app", "dev.hal9001.dev", "7"), HasPublicKey: hasKey}, "dev build", 7},
		{"local build", DetectOptions{Exe: exeOf("C.app", "dev.hal9001", "0"), HasPublicKey: hasKey}, "local build", 0},
		{"no signing key", DetectOptions{Exe: exeOf("D.app", "dev.hal9001", "7"), HasPublicKey: noKey}, "no update signing key", 7},
		{"test override skips the bundle and the key", DetectOptions{Exe: "/nowhere/hal9001", Env: "dev.hal9001:0.1.5:5", HasPublicKey: noKey}, "", 5},
		{"override cannot make a dev build updatable", DetectOptions{Env: "dev.hal9001.dev:0.1.5:5"}, "dev build", 5},
		{"malformed override", DetectOptions{Env: "dev.hal9001:0.1.5"}, EnvCurrent, 0},
	} {
		cur, reason := Detect(c.o)
		if c.reason == "" && reason != "" || c.reason != "" && !strings.Contains(reason, c.reason) {
			t.Errorf("%s: reason = %q, want %q", c.name, reason, c.reason)
		}
		if cur.Build != c.build {
			t.Errorf("%s: build = %d, want %d", c.name, cur.Build, c.build)
		}
	}
}

func TestCompareVersionsIsNumericPerComponent(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.1.10", "0.1.9", 1}, {"0.1.9", "0.1.10", -1}, {"0.1", "0.1.0", 0}, {"14.5", "13.0", 1}, {"13.0", "13.0.1", -1}, {"1.2.3", "1.2.3", 0},
	} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

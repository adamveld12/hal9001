//go:build darwin

package update

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"hal9001/internal/service"
)

// applyRun runs a macOS tool on files under the test's temp directories and fails the test if it fails.
func applyRun(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func applyNeedTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := os.Stat(tool); err != nil {
			t.Skipf("%s is not available", tool)
		}
	}
}

func TestSwapExchangesTheFoldersAtomically(t *testing.T) {
	a, b := applyTwoFolders(t)
	if err := unix.RenamexNp(a, b, unix.RENAME_SWAP); err != nil {
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) {
			t.Skipf("this filesystem has no RENAME_SWAP (%v); the rename fallback has its own tests", err)
		}
		t.Fatal(err)
	}
	if err := unix.RenamexNp(a, b, unix.RENAME_SWAP); err != nil { // back to the start
		t.Fatal(err)
	}

	restore, err := Swap(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if applyWho(t, a) != "B" || applyWho(t, b) != "A" {
		t.Fatalf("after Swap a holds %s, b holds %s", applyWho(t, a), applyWho(t, b))
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if applyWho(t, a) != "A" || applyWho(t, b) != "B" {
		t.Fatalf("after restore a holds %s, b holds %s", applyWho(t, a), applyWho(t, b))
	}
}

// applySignedArchive builds a fake HAL9001 app of build 6, signs it ad hoc and zips it the way CI does.
// tamper, if set, changes the app after it was signed.
func applySignedArchive(t *testing.T, tamper func(app string)) []byte {
	t.Helper()
	app := applyApp(t, t.TempDir(), service.ReleaseBundleID, "0.1.6", 6, "six")
	applyRun(t, "/usr/bin/codesign", "--force", "--deep", "-s", "-", app)
	if tamper != nil {
		tamper(app)
	}
	zipPath := filepath.Join(t.TempDir(), "HAL9001-6.zip")
	applyRun(t, "/usr/bin/ditto", "-c", "-k", "--keepParent", app, zipPath)
	archive, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

func TestApplyWithTheRealMacOSTools(t *testing.T) {
	applyNeedTools(t, "/usr/bin/ditto", "/usr/bin/codesign", "/usr/bin/xattr")
	e := newApplyEnv(t)
	m := e.publish(6, applySignedArchive(t, nil))

	// Only the archive and the installed app are fake: the unpacking, the signature check, the quarantine step and
	// the swap are the real ones.
	cfg := ApplyConfig{InstalledBundle: e.installed, CacheDir: e.cache, PublicKey: e.pub}
	if err := Apply(context.Background(), cfg, m, nil); err != nil {
		t.Fatal(err)
	}
	if got := applyMarker(t, e.installed); got != "six" || applyBuild(t, e.installed) != 6 {
		t.Fatalf("installed app: marker %q build %d", got, applyBuild(t, e.installed))
	}
	applyRun(t, "/usr/bin/codesign", "--verify", "--deep", "--strict", e.installed)
	if got := applyMarker(t, filepath.Join(e.apps, ".HAL9001-previous.app")); got != "old" {
		t.Fatalf("previous marker %q", got)
	}
	applyListing(t, e.apps, ".HAL9001-previous.app", "HAL9001.app")
	applyListing(t, e.cache)
}

func TestApplyRefusesAnAppWhoseSignatureDoesNotHold(t *testing.T) {
	applyNeedTools(t, "/usr/bin/ditto", "/usr/bin/codesign", "/usr/bin/xattr")
	e := newApplyEnv(t)
	// The archive itself is genuine (its hash and signature are the manifest's); the app inside was altered after it
	// was signed, which only the code signature check catches.
	m := e.publish(6, applySignedArchive(t, func(app string) {
		exe := filepath.Join(app, "Contents", "MacOS", "HAL9001")
		if err := os.WriteFile(exe, []byte("#!/bin/sh\n# tampered\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}))
	err := Apply(context.Background(), ApplyConfig{InstalledBundle: e.installed, CacheDir: e.cache, PublicKey: e.pub}, m, nil)
	if err == nil || !strings.Contains(err.Error(), "code signature") {
		t.Fatalf("err = %v", err)
	}
	e.assertUntouched()
}

func TestStripQuarantineRemovesTheAttributeEverywhere(t *testing.T) {
	applyNeedTools(t, "/usr/bin/xattr")
	dir := t.TempDir()
	nested := filepath.Join(dir, "Contents", "MacOS", "HAL9001")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	applyRun(t, "/usr/bin/xattr", "-w", "com.apple.quarantine", "0083;5f5e1000;Safari;", nested)

	if err := stripQuarantine(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/bin/xattr", "-p", "com.apple.quarantine", nested).CombinedOutput(); err == nil {
		t.Fatalf("the quarantine attribute is still there: %s", out)
	}
	if err := stripQuarantine(context.Background(), dir); err != nil { // nothing left to remove
		t.Fatalf("a bundle without the attribute is fine: %v", err)
	}
}

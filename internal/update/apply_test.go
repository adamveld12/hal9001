package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"hal9001/internal/service"
)

const applyPlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
	<key>CFBundleIdentifier</key><string>%s</string>
	<key>CFBundleShortVersionString</key><string>%s</string>
	<key>CFBundleVersion</key><string>%d</string>
	<key>CFBundleExecutable</key><string>HAL9001</string>
</dict></plist>
`

// applyApp writes a fake app bundle dir/HAL9001.app whose executable carries marker, and returns its path.
func applyApp(t *testing.T, dir, bundleID, version string, build int, marker string) string {
	t.Helper()
	root := filepath.Join(dir, "HAL9001.app")
	if err := os.MkdirAll(filepath.Join(root, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := fmt.Sprintf(applyPlist, bundleID, version, build)
	if err := os.WriteFile(filepath.Join(root, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Contents", "MacOS", "HAL9001"), []byte("#!/bin/sh\n# "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func applyMarker(t *testing.T, bundle string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(bundle, "Contents", "MacOS", "HAL9001"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(strings.TrimPrefix(string(b), "#!/bin/sh\n#"))
}

func applyBuild(t *testing.T, bundle string) int {
	t.Helper()
	cur, err := ReadBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return cur.Build
}

// applyListing fails unless dir holds exactly the named entries.
func applyListing(t *testing.T, dir string, want ...string) {
	t.Helper()
	if got := dlLeftovers(t, dir); !slices.Equal(got, want) {
		t.Fatalf("%s holds %v, want %v", dir, got, want)
	}
}

// applyEnv is a folder standing for /Applications with an installed fake HAL9001 (build 5, marker "old"), a download
// folder, and a signing key.
type applyEnv struct {
	t         *testing.T
	apps      string
	cache     string
	installed string
	pub       ed25519.PublicKey
	priv      ed25519.PrivateKey
	hits      atomic.Int32 // requests the archive servers answered
}

func newApplyEnv(t *testing.T) *applyEnv {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	e := &applyEnv{t: t, apps: t.TempDir(), cache: filepath.Join(t.TempDir(), "updates"), pub: pub, priv: priv}
	e.installed = applyApp(t, e.apps, service.ReleaseBundleID, "0.1.5", 5, "old")
	return e
}

func applyPayload(build int) []byte { return fmt.Appendf(nil, "archive of build %d", build) }

// publish serves payload on loopback and returns the signed manifest of build. The tests that do not need a real zip
// unpack with a fake Extract, so any bytes will do.
func (e *applyEnv) publish(build int, payload []byte) Manifest {
	e.t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		e.hits.Add(1)
		_, _ = w.Write(payload)
	}))
	e.t.Cleanup(srv.Close)
	sum := sha256.Sum256(payload)
	return Manifest{
		Schema: 1, Channel: "stable", Version: fmt.Sprintf("0.1.%d", build), Build: build,
		URL: srv.URL + "/HAL9001.zip", Size: int64(len(payload)), SHA256: hex.EncodeToString(sum[:]),
		Signature: Sign(e.priv, payload),
	}
}

func (e *applyEnv) release(build int) Manifest { return e.publish(build, applyPayload(build)) }

// config is an ApplyConfig whose Extract unpacks a fake app of m with marker, and whose codesign and quarantine steps
// succeed. The swap is the real one.
func (e *applyEnv) config(m Manifest, marker string) ApplyConfig {
	return ApplyConfig{
		InstalledBundle: e.installed, CacheDir: e.cache, PublicKey: e.pub,
		Extract: func(_ context.Context, _, dir string) error {
			applyApp(e.t, dir, service.ReleaseBundleID, m.Version, m.Build, marker)
			return nil
		},
		Codesign:        func(context.Context, string) error { return nil },
		StripQuarantine: func(context.Context, string) error { return nil },
	}
}

// assertUntouched fails unless the installed app is still the old one and nothing is left next to it or in the
// download folder.
func (e *applyEnv) assertUntouched() {
	e.t.Helper()
	if got := applyMarker(e.t, e.installed); got != "old" || applyBuild(e.t, e.installed) != 5 {
		e.t.Fatalf("installed app changed: marker %q build %d", got, applyBuild(e.t, e.installed))
	}
	applyListing(e.t, e.apps, "HAL9001.app")
	applyListing(e.t, e.cache)
}

// applyReports records the phases Apply reported, without the repeats of download progress.
type applyReports struct{ states []string }

func (r *applyReports) report(state, _ string) {
	if n := len(r.states); n == 0 || r.states[n-1] != state {
		r.states = append(r.states, state)
	}
}

func TestApplyInstallsTheUpdateAndKeepsThePreviousApp(t *testing.T) {
	e := newApplyEnv(t)
	var rec applyReports

	m6 := e.release(6)
	if err := Apply(context.Background(), e.config(m6, "six"), m6, rec.report); err != nil {
		t.Fatal(err)
	}
	if got := applyMarker(t, e.installed); got != "six" || applyBuild(t, e.installed) != 6 {
		t.Fatalf("installed app: marker %q build %d, want the update", got, applyBuild(t, e.installed))
	}
	previous := filepath.Join(e.apps, ".HAL9001-previous.app")
	if got := applyMarker(t, previous); got != "old" || applyBuild(t, previous) != 5 {
		t.Fatalf("previous app: marker %q build %d, want the replaced one", got, applyBuild(t, previous))
	}
	applyListing(t, e.apps, ".HAL9001-previous.app", "HAL9001.app") // the staging folder is gone
	applyListing(t, e.cache)                                        // so is the download
	if got := strings.Join(rec.states, ","); got != "downloading,verifying,installing" {
		t.Fatalf("reported phases %s", got)
	}
	if e.hits.Load() != 1 {
		t.Fatalf("the archive was requested %d times", e.hits.Load())
	}

	// The next update replaces the previous app.
	m7 := e.release(7)
	if err := Apply(context.Background(), e.config(m7, "seven"), m7, nil); err != nil {
		t.Fatal(err)
	}
	if got := applyMarker(t, e.installed); got != "seven" {
		t.Fatalf("installed marker %q", got)
	}
	if got := applyMarker(t, previous); got != "six" || applyBuild(t, previous) != 6 {
		t.Fatalf("previous app: marker %q build %d, want build 6", got, applyBuild(t, previous))
	}
	applyListing(t, e.apps, ".HAL9001-previous.app", "HAL9001.app")
}

// applySwapByRename is Swap through the fallback renames, as a filesystem without RENAME_SWAP gets.
func applySwapByRename(installed, staged string) (func() error, error) {
	if err := swapByRename(installed, staged); err != nil {
		return nil, err
	}
	return func() error { return swapByRename(installed, staged) }, nil
}

func TestApplyInstallsThroughTheRenameFallback(t *testing.T) {
	e := newApplyEnv(t)
	m := e.release(6)
	cfg := e.config(m, "six")
	cfg.Swap = applySwapByRename
	if err := Apply(context.Background(), cfg, m, nil); err != nil {
		t.Fatal(err)
	}
	if got := applyMarker(t, e.installed); got != "six" {
		t.Fatalf("installed marker %q", got)
	}
	if got := applyMarker(t, filepath.Join(e.apps, ".HAL9001-previous.app")); got != "old" {
		t.Fatalf("previous marker %q", got)
	}
	applyListing(t, e.apps, ".HAL9001-previous.app", "HAL9001.app")
}

func TestApplyRefusesToReplaceWhatItShouldNot(t *testing.T) {
	e := newApplyEnv(t)
	for _, build := range []int{5, 4} {
		m := e.release(build)
		err := Apply(context.Background(), e.config(m, "x"), m, nil)
		if err == nil || !strings.Contains(err.Error(), "already installed") {
			t.Errorf("build %d over build 5: err = %v", build, err)
		}
	}

	dev := t.TempDir()
	devApp := applyApp(t, dev, "dev.hal9001.dev", "0.1.5", 5, "dev")
	m := e.release(6)
	cfg := e.config(m, "x")
	cfg.InstalledBundle = devApp
	if err := Apply(context.Background(), cfg, m, nil); err == nil || !strings.Contains(err.Error(), "not HAL9001") {
		t.Errorf("a dev app must not be updated: err = %v", err)
	}
	if e.hits.Load() != 0 {
		t.Errorf("refused updates still downloaded %d times", e.hits.Load())
	}
	e.assertUntouched()
}

func TestApplyWithoutAPublicKeyDownloadsNothing(t *testing.T) {
	e := newApplyEnv(t)
	m := e.release(6)
	cfg := e.config(m, "x")
	cfg.PublicKey = nil
	err := Apply(context.Background(), cfg, m, nil)
	if !errors.Is(err, ErrNoPublicKey) {
		t.Fatalf("err = %v, want ErrNoPublicKey", err)
	}
	if e.hits.Load() != 0 {
		t.Fatal("downloaded an update that cannot be verified")
	}
	e.assertUntouched()
}

func TestApplyDiscardsADownloadThatFailsVerification(t *testing.T) {
	e := newApplyEnv(t)
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		corrupt func(m *Manifest)
		is      error  // the error Apply must wrap, when VerifyFile is what refuses
		message string // or a text it must contain
	}{
		{"signed by another key", func(m *Manifest) {
			m.Signature = Sign(other, applyPayload(6))
		}, ErrSignatureMismatch, ""},
		{"another hash", func(m *Manifest) { m.SHA256 = strings.Repeat("0", 64) }, ErrSHA256Mismatch, ""},
		{"another size", func(m *Manifest) { m.Size++ }, nil, "announces"},
	} {
		m := e.release(6)
		tc.corrupt(&m)
		cfg := e.config(m, "x")
		cfg.Extract = func(context.Context, string, string) error {
			t.Errorf("%s: unpacked an archive that failed verification", tc.name)
			return nil
		}
		var rec applyReports
		err := Apply(context.Background(), cfg, m, rec.report)
		switch {
		case err == nil:
			t.Errorf("%s: want an error", tc.name)
		case tc.is != nil:
			if !errors.Is(err, tc.is) || strings.Join(rec.states, ",") != "downloading,verifying" {
				t.Errorf("%s: err = %v after phases %v, want %v after downloading,verifying", tc.name, err, rec.states, tc.is)
			}
		case !strings.Contains(err.Error(), tc.message):
			t.Errorf("%s: err = %v, want one containing %q", tc.name, err, tc.message)
		}
		e.assertUntouched()
	}
}

func TestApplyRejectsAnUnpackedAppThatIsNotTheAnnouncedUpdate(t *testing.T) {
	e := newApplyEnv(t)
	m := e.release(6)
	for _, tc := range []struct {
		name    string
		extract func(dir string) error
		want    string
	}{
		{"another bundle id", func(dir string) error {
			applyApp(t, dir, "dev.hal9001.dev", "0.1.6", 6, "x")
			return nil
		}, `"dev.hal9001.dev", not HAL9001`},
		{"another build", func(dir string) error {
			applyApp(t, dir, service.ReleaseBundleID, "0.1.6", 7, "x")
			return nil
		}, "build 7, not the announced build 6"},
		{"another version", func(dir string) error {
			applyApp(t, dir, service.ReleaseBundleID, "9.9.9", 6, "x")
			return nil
		}, "version 9.9.9, not the announced 0.1.6"},
		{"no app in the archive", func(dir string) error { return os.Mkdir(filepath.Join(dir, "__MACOSX"), 0o755) }, "contains no app"},
		{"two apps in the archive", func(dir string) error {
			applyApp(t, dir, service.ReleaseBundleID, "0.1.6", 6, "x")
			if err := os.Rename(filepath.Join(dir, "HAL9001.app"), filepath.Join(dir, "A.app")); err != nil {
				return err
			}
			applyApp(t, dir, service.ReleaseBundleID, "0.1.6", 6, "x")
			return nil
		}, "2 apps"},
		{"a failing unpack", func(string) error { return errors.New("ditto: the archive is damaged") }, "could not unpack the update: ditto: the archive is damaged"},
	} {
		cfg := e.config(m, "x")
		cfg.Extract = func(_ context.Context, _, dir string) error { return tc.extract(dir) }
		err := Apply(context.Background(), cfg, m, nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one containing %q", tc.name, err, tc.want)
		}
		e.assertUntouched()
	}
}

func TestCheckStagedRequiresANewerBuildAndRunsTheToolsInOrder(t *testing.T) {
	e := newApplyEnv(t)
	m := e.release(6)
	staged := applyApp(t, t.TempDir(), service.ReleaseBundleID, "0.1.6", 6, "six")

	var calls []string
	cfg := e.config(m, "x")
	cfg.Codesign = func(_ context.Context, b string) error { calls = append(calls, "codesign "+b); return nil }
	cfg.StripQuarantine = func(_ context.Context, b string) error { calls = append(calls, "strip "+b); return nil }

	if err := cfg.CheckStaged(context.Background(), staged, m, Current{BundleID: service.ReleaseBundleID, Build: 6}); err == nil ||
		!strings.Contains(err.Error(), "not newer") {
		t.Fatalf("a build that is not newer than the installed one: err = %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("tools ran before the build checks passed: %v", calls)
	}
	if err := cfg.CheckStaged(context.Background(), staged, m, Current{BundleID: service.ReleaseBundleID, Build: 5}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"codesign " + staged, "strip " + staged}; !slices.Equal(calls, want) {
		t.Fatalf("calls %v, want %v", calls, want)
	}

	cfg.StripQuarantine = func(context.Context, string) error { return errors.New("Operation not permitted") }
	if err := cfg.CheckStaged(context.Background(), staged, m, Current{Build: 5}); err == nil || !strings.Contains(err.Error(), "quarantine") {
		t.Fatalf("a quarantine flag that cannot be cleared must fail the update: err = %v", err)
	}
}

func TestApplyKeepsTheInstalledAppWhenTheSignatureIsBroken(t *testing.T) {
	e := newApplyEnv(t)
	m := e.release(6)
	cfg := e.config(m, "six")
	cfg.Codesign = func(context.Context, string) error { return errors.New("a sealed resource is missing or invalid") }
	cfg.StripQuarantine = func(context.Context, string) error {
		t.Error("cleared the quarantine of an app that failed the signature check")
		return nil
	}
	err := Apply(context.Background(), cfg, m, nil)
	if err == nil || !strings.Contains(err.Error(), "code signature") || !strings.Contains(err.Error(), "sealed resource") {
		t.Fatalf("err = %v", err)
	}
	e.assertUntouched()
}

func TestStripQuarantineIgnoresAMissingAttributeOnly(t *testing.T) {
	var args []string
	run := func(out string, err error) toolRunner {
		return func(_ context.Context, name string, a ...string) ([]byte, error) {
			args = append([]string{name}, a...)
			return []byte(out), err
		}
	}
	exit1 := errors.New("exit status 1")
	if err := stripQuarantineWith(context.Background(), run("xattr: x: No such xattr: com.apple.quarantine\n", exit1), "/x/HAL9001.app"); err != nil {
		t.Fatalf("an absent attribute is not an error: %v", err)
	}
	if want := []string{"/usr/bin/xattr", "-dr", "com.apple.quarantine", "/x/HAL9001.app"}; !slices.Equal(args, want) {
		t.Fatalf("ran %v, want %v", args, want)
	}
	if err := stripQuarantineWith(context.Background(), run("", nil), "/x"); err != nil {
		t.Fatal(err)
	}
	err := stripQuarantineWith(context.Background(), run("xattr: x: Permission denied\n", exit1), "/x")
	if err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("any other failure is an error that says why: %v", err)
	}
}

func TestApplyInAnUnwritableFolderPointsToTheManualDownload(t *testing.T) {
	e := newApplyEnv(t)
	if err := os.Chmod(e.apps, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(e.apps, 0o700) })
	if f, err := os.Create(filepath.Join(e.apps, "probe")); err == nil {
		_ = f.Close()
		t.Skip("the folder is writable despite chmod (running as root?)")
	}
	m := e.release(6)
	err := Apply(context.Background(), e.config(m, "six"), m, nil)
	if !errors.Is(err, ErrInstallDirNotWritable) {
		t.Fatalf("err = %v, want ErrInstallDirNotWritable", err)
	}
	if !strings.Contains(err.Error(), "download the update manually from https://github.com/adamveld12/hal9001/releases/latest") {
		t.Fatalf("the message must say where to get the update by hand: %v", err)
	}
	if got := applyMarker(t, e.installed); got != "old" {
		t.Fatalf("installed marker %q", got)
	}
	applyListing(t, e.cache)
}

func TestApplyChecksTheInstalledAppAgainRightBeforeTheSwap(t *testing.T) {
	e := newApplyEnv(t)
	m := e.release(6)
	cfg := e.config(m, "six")
	unpack := cfg.Extract
	cfg.Extract = func(ctx context.Context, zipPath, dir string) error {
		// The user drags a newer build into place while this update is being prepared.
		applyApp(t, e.apps, service.ReleaseBundleID, "0.1.9", 9, "nine")
		return unpack(ctx, zipPath, dir)
	}
	if err := Apply(context.Background(), cfg, m, nil); err == nil || !strings.Contains(err.Error(), "build 9 is already installed") {
		t.Fatalf("err = %v", err)
	}
	if got := applyMarker(t, e.installed); got != "nine" {
		t.Fatalf("the newer app that was put there must stay, marker %q", got)
	}
	applyListing(t, e.apps, "HAL9001.app")
	applyListing(t, e.cache)
}

func TestApplyPutsTheOldAppBackWhenTheAppInPlaceIsNotTheUpdate(t *testing.T) {
	e := newApplyEnv(t)
	m := e.release(6)
	cfg := e.config(m, "six")
	cfg.Swap = func(installed, staged string) (func() error, error) {
		restore, err := Swap(installed, staged)
		if err == nil { // the new app turns out to claim another build
			plist := fmt.Sprintf(applyPlist, service.ReleaseBundleID, "0.1.6", 99)
			err = os.WriteFile(filepath.Join(installed, "Contents", "Info.plist"), []byte(plist), 0o644)
		}
		return restore, err
	}
	err := Apply(context.Background(), cfg, m, nil)
	if err == nil || !strings.Contains(err.Error(), "put back") {
		t.Fatalf("err = %v", err)
	}
	e.assertUntouched()
}

func TestApplyKeepsEverythingWhenASwapDiesHalfWay(t *testing.T) {
	e := newApplyEnv(t)
	m := e.release(6)
	cfg := e.config(m, "six")
	var staging string
	cfg.Swap = func(installed, staged string) (func() error, error) {
		// Like the fallback renames failing after the first one: the installed app is only in the staging folder.
		staging = filepath.Dir(staged)
		if err := os.Rename(installed, staged+".swap"); err != nil {
			t.Fatal(err)
		}
		return nil, errors.New("rename: input/output error")
	}
	err := Apply(context.Background(), cfg, m, nil)
	if err == nil || !strings.Contains(err.Error(), staging) {
		t.Fatalf("the error must say where the old app is: %v", err)
	}
	if got := applyMarker(t, filepath.Join(staging, "HAL9001.app.swap")); got != "old" {
		t.Fatalf("the only copy of the old app was deleted or changed: marker %q", got)
	}
}

func TestApplyStillSucceedsWhenThePreviousAppCannotBeReplaced(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can remove anything")
	}
	e := newApplyEnv(t)
	sub := filepath.Join(e.apps, ".HAL9001-previous.app", "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "f"), []byte("older"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0o555); err != nil { // its content cannot be unlinked
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	m := e.release(6)
	if err := Apply(context.Background(), e.config(m, "six"), m, nil); err != nil {
		t.Fatalf("a missing backup must not fail the update: %v", err)
	}
	if got := applyMarker(t, e.installed); got != "six" {
		t.Fatalf("installed marker %q", got)
	}
	applyListing(t, e.apps, ".HAL9001-previous.app", "HAL9001.app") // no staging folder left behind
}

func TestStageRemovesLeftoversOfEarlierAttempts(t *testing.T) {
	e := newApplyEnv(t)
	m := e.release(6)
	if err := os.MkdirAll(filepath.Join(e.apps, ".HAL9001-update-3", "HAL9001.app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.apps, ".HAL9001-update-junk"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(e.apps, "Other.app"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged, err := e.config(m, "six").Stage(context.Background(), "unused.zip", m, Current{BundleID: service.ReleaseBundleID, Build: 5})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(e.apps, ".HAL9001-update-6", "HAL9001.app"); staged != want {
		t.Fatalf("staged %s, want %s", staged, want)
	}
	applyListing(t, e.apps, ".HAL9001-update-6", "HAL9001.app", "Other.app")
}

// applyTwoFolders makes two folders whose file "who" says A and B.
func applyTwoFolders(t *testing.T) (a, b string) {
	t.Helper()
	root := t.TempDir()
	for _, n := range []string{"A", "B"} {
		if err := os.MkdirAll(filepath.Join(root, n), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, n, "who"), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, "A"), filepath.Join(root, "B")
}

func applyWho(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "who"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSwapByRenameExchangesTheFolders(t *testing.T) {
	a, b := applyTwoFolders(t)
	if err := swapByRename(a, b); err != nil {
		t.Fatal(err)
	}
	if applyWho(t, a) != "B" || applyWho(t, b) != "A" {
		t.Fatalf("a holds %s, b holds %s", applyWho(t, a), applyWho(t, b))
	}
	if _, err := os.Lstat(b + ".swap"); err == nil {
		t.Fatal("scratch name left behind")
	}
}

func TestSwapByRenameUndoesItselfWhenAStepFails(t *testing.T) {
	a, b := applyTwoFolders(t)
	if err := os.RemoveAll(b); err != nil { // the second rename has nothing to move
		t.Fatal(err)
	}
	if err := swapByRename(a, b); err == nil {
		t.Fatal("want an error")
	}
	if applyWho(t, a) != "A" {
		t.Fatal("the first folder was not put back")
	}

	a, b = applyTwoFolders(t)
	if err := os.Mkdir(b+".swap", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := swapByRename(a, b); err == nil || applyWho(t, a) != "A" || applyWho(t, b) != "B" {
		t.Fatalf("a taken scratch name must stop the swap before anything moves: err = %v", err)
	}
}

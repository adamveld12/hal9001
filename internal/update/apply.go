package update

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"hal9001/internal/service"
)

// Names next to the installed app. Both start with a dot so that Finder does not list them.
const (
	// stagingPrefix starts the folder an update is unpacked into: <dir>/.HAL9001-update-<build>/HAL9001.app. It lies
	// in the folder of the installed app because swapping the two bundles is then a rename on one volume.
	stagingPrefix = ".HAL9001-update-"
	// previousName is the app the last update replaced, kept for a manual rollback.
	previousName = ".HAL9001-previous.app"

	// downloadHeartbeat is how often a running download is reported even when the percentage did not change: the
	// daemon treats an install as dead after ten minutes without a report.
	downloadHeartbeat = 30 * time.Second
	// toolTimeout bounds one run of ditto, codesign or xattr.
	toolTimeout = 5 * time.Minute
)

// ErrInstallDirNotWritable is wrapped by the errors of Stage and Apply when the folder the app is installed in cannot
// be written to, for example because the app runs from a disk image. The user then has to install the update by hand.
var ErrInstallDirNotWritable = errors.New("the folder HAL9001 is installed in is not writable")

// ApplyConfig is everything Apply needs. The function fields are the external operations; nil runs the real macOS
// tool, tests put fakes there.
type ApplyConfig struct {
	// InstalledBundle is the app bundle to replace (<dir>/HAL9001.app), normally the running one.
	InstalledBundle string
	// CacheDir receives the download (created 0700) and is left without it.
	CacheDir string
	// PublicKey verifies the archive's signature.
	PublicKey ed25519.PublicKey
	// HTTP downloads the archive; nil uses a client with connect and response-header timeouts.
	HTTP *http.Client

	// Extract unpacks the zip into the existing directory dir (default: ditto -x -k).
	Extract func(ctx context.Context, zipPath, dir string) error
	// Codesign checks the signature of the staged bundle (default: codesign --verify --deep --strict).
	Codesign func(ctx context.Context, bundle string) error
	// StripQuarantine removes the quarantine attribute from the staged bundle, if it has one (default: xattr -dr).
	StripQuarantine func(ctx context.Context, bundle string) error
	// Swap exchanges the installed and the staged bundle (default: Swap).
	Swap func(installed, staged string) (restore func() error, err error)
}

// Apply installs the build m over cfg.InstalledBundle: it downloads the archive, verifies its size, hash and
// signature, unpacks and checks the app inside, swaps it with the installed one and keeps the old one as
// .HAL9001-previous.app next to it. report is told the phase before each step (PhaseDownloading, PhaseVerifying,
// PhaseInstalling; during the download also with the percentage as msg). A failure is returned in words fit for the
// menu and the installed app is as it was; the caller reports it as failed. On success the new app is in place but not
// running: the caller relaunches.
func Apply(ctx context.Context, cfg ApplyConfig, m Manifest, report func(state, msg string)) error {
	if report == nil {
		report = func(string, string) {}
	}
	cur, err := ReadBundle(cfg.InstalledBundle)
	if err != nil {
		return fmt.Errorf("cannot read the installed app: %w", err)
	}
	if err := checkReplaceable(cur, m); err != nil {
		return err
	}
	// ed25519.Verify panics on a key of the wrong length, so this is checked before anything is downloaded.
	if len(cfg.PublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("this build cannot verify updates, download HAL9001 %s from %s: %w", m.Version, ReleasesURL, ErrNoPublicKey)
	}

	if err := os.MkdirAll(cfg.CacheDir, 0o700); err != nil {
		return fmt.Errorf("cannot create the download folder: %w", err)
	}
	clearDownloads(cfg.CacheDir)
	zipPath := filepath.Join(cfg.CacheDir, fmt.Sprintf("HAL9001-%d.app.zip", m.Build))
	defer func() { _ = os.Remove(zipPath) }()

	report(PhaseDownloading, "")
	if err := Download(ctx, cfg.HTTP, m.URL, zipPath, m.Size, downloadProgress(m.Size, report)); err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	report(PhaseVerifying, "")
	if err := VerifyFile(zipPath, m, cfg.PublicKey); err != nil {
		return fmt.Errorf("the downloaded update did not pass verification: %w", err)
	}
	staged, err := cfg.Stage(ctx, zipPath, m, cur)
	if err != nil {
		return err
	}
	return cfg.install(ctx, staged, m, report)
}

// install swaps the staged bundle in. On every failure the installed app is the one that was there before, and the
// staging folder is removed unless it may hold the only copy of the old app.
func (c ApplyConfig) install(ctx context.Context, staged string, m Manifest, report func(state, msg string)) error {
	staging := filepath.Dir(staged)
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(staging)
		}
	}()

	// The app may have been replaced while the update downloaded, so check again right before the swap.
	if err := ctx.Err(); err != nil {
		return err
	}
	now, err := ReadBundle(c.InstalledBundle)
	if err != nil {
		return fmt.Errorf("cannot read the installed app: %w", err)
	}
	if err := checkReplaceable(now, m); err != nil {
		return err
	}

	report(PhaseInstalling, "")
	swap := c.Swap
	if swap == nil {
		swap = Swap
	}
	restore, err := swap(c.InstalledBundle, staged)
	if err != nil {
		// A swap that failed half-way (the fallback renames) can leave the old app only in the staging folder.
		if back, rerr := ReadBundle(c.InstalledBundle); rerr != nil || back != now {
			keep = true
			return fmt.Errorf("could not replace the app, the previous version may be in %s: %w", staging, err)
		}
		return fmt.Errorf("could not replace the app: %w", err)
	}

	if err := verifyInstalled(c.InstalledBundle, m); err != nil {
		if rerr := restore(); rerr != nil {
			keep = true
			return fmt.Errorf("%w; putting the previous version back failed too, it is in %s: %w", err, staging, rerr)
		}
		return fmt.Errorf("%w; the previous version was put back", err)
	}
	// The update is in place and working; failing to keep the old copy must not undo that.
	if err := KeepPrevious(staged, filepath.Dir(c.InstalledBundle)); err != nil {
		slog.Warn("update: could not keep the previous version", "err", err)
	}
	return nil
}

// checkReplaceable reports why an update to m may not replace the installed app: it must be the release app and
// older than m.
func checkReplaceable(installed Current, m Manifest) error {
	switch {
	case installed.BundleID != service.ReleaseBundleID:
		return fmt.Errorf("the installed app is %q, not HAL9001, so it is not updated", installed.BundleID)
	case installed.Build >= m.Build:
		return fmt.Errorf("HAL9001 build %d is already installed; the update is build %d", installed.Build, m.Build)
	}
	return nil
}

// verifyInstalled checks that the bundle at root is the update m.
func verifyInstalled(root string, m Manifest) error {
	got, err := ReadBundle(root)
	switch {
	case err != nil:
		return fmt.Errorf("the update was not installed correctly: %w", err)
	case got.BundleID != service.ReleaseBundleID || got.Build != m.Build:
		return fmt.Errorf("the update was not installed correctly: the app in place is build %d of %s", got.Build, got.BundleID)
	}
	return nil
}

// downloadProgress turns byte counts into reports: one per 10% step, and one every downloadHeartbeat otherwise.
func downloadProgress(size int64, report func(state, msg string)) func(done int64) {
	lastStep, lastAt := 0, time.Now()
	return func(done int64) {
		step := int(done * 10 / size)
		if step == lastStep && time.Since(lastAt) < downloadHeartbeat {
			return
		}
		lastStep, lastAt = step, time.Now()
		report(PhaseDownloading, fmt.Sprintf("%d%%", step*10))
	}
}

// clearDownloads removes what an earlier attempt left in the download folder: a crash mid-download leaves a .part file.
func clearDownloads(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if n := e.Name(); strings.HasPrefix(n, "HAL9001-") && (strings.HasSuffix(n, ".app.zip") || strings.HasSuffix(n, ".app.zip.part")) {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
}

// Stage unpacks the verified archive zipPath into .HAL9001-update-<build>/ next to the installed app and returns the
// bundle inside, after CheckStaged accepted it. Leftover .HAL9001-update-* folders of earlier attempts are removed
// first. If the folder cannot be written the error wraps ErrInstallDirNotWritable and says where to download the
// update by hand. On any failure the staging folder is removed.
func (c ApplyConfig) Stage(ctx context.Context, zipPath string, m Manifest, cur Current) (staged string, err error) {
	dir := filepath.Dir(c.InstalledBundle)
	if entries, rerr := os.ReadDir(dir); rerr == nil {
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), stagingPrefix) {
				continue
			}
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return "", stagingDirError(err)
			}
		}
	}
	staging := filepath.Join(dir, fmt.Sprintf("%s%d", stagingPrefix, m.Build))
	if err := os.Mkdir(staging, 0o750); err != nil {
		return "", stagingDirError(err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(staging)
		}
	}()

	extract := c.Extract
	if extract == nil {
		extract = dittoExtract
	}
	if err := extract(ctx, zipPath, staging); err != nil {
		return "", fmt.Errorf("could not unpack the update: %w", err)
	}
	staged, err = onlyApp(staging)
	if err != nil {
		return "", err
	}
	if err := c.CheckStaged(ctx, staged, m, cur); err != nil {
		return "", err
	}
	return staged, nil
}

// stagingDirError explains a failure to prepare the staging folder. A permission or read-only error means the user
// has to install by hand; anything else (a full disk, say) is reported as it is.
func stagingDirError(err error) error {
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS) {
		return fmt.Errorf("%w (%w): download the update manually from %s", ErrInstallDirNotWritable, err, ReleasesURL)
	}
	return fmt.Errorf("cannot prepare the update folder: %w", err)
}

// onlyApp returns the one .app directory at the top of dir.
func onlyApp(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("cannot read the unpacked update: %w", err)
	}
	var apps []string
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".app") {
			apps = append(apps, e.Name())
		}
	}
	switch len(apps) {
	case 1:
		return filepath.Join(dir, apps[0]), nil
	case 0:
		return "", errors.New("the downloaded update contains no app")
	}
	return "", fmt.Errorf("the downloaded update contains %d apps, expected one", len(apps))
}

// CheckStaged accepts the unpacked bundle only if it is the release app, at the build and version the manifest names
// and newer than cur, and its code signature is intact. It also removes the quarantine attribute, so that Gatekeeper
// does not block the first launch of the new app.
func (c ApplyConfig) CheckStaged(ctx context.Context, staged string, m Manifest, cur Current) error {
	got, err := ReadBundle(staged)
	if err != nil {
		return fmt.Errorf("the downloaded update is not a valid app: %w", err)
	}
	switch {
	case got.BundleID != service.ReleaseBundleID:
		return fmt.Errorf("the downloaded update is %q, not HAL9001", got.BundleID)
	case got.Build != m.Build:
		return fmt.Errorf("the downloaded update is build %d, not the announced build %d", got.Build, m.Build)
	case got.Build <= cur.Build:
		return fmt.Errorf("the downloaded update (build %d) is not newer than the installed build %d", got.Build, cur.Build)
	case got.Version != m.Version:
		return fmt.Errorf("the downloaded update is version %s, not the announced %s", got.Version, m.Version)
	}
	verify := c.Codesign
	if verify == nil {
		verify = codesignVerify
	}
	if err := verify(ctx, staged); err != nil {
		return fmt.Errorf("the downloaded update has no valid code signature: %w", err)
	}
	strip := c.StripQuarantine
	if strip == nil {
		strip = stripQuarantine
	}
	if err := strip(ctx, staged); err != nil {
		return fmt.Errorf("cannot clear the quarantine flag of the downloaded update: %w", err)
	}
	return nil
}

// Swap exchanges the installed bundle and the staged one atomically (renamex_np RENAME_SWAP), so the installed path
// never points at nothing. Afterwards installed is the update and staged is the old app. On a filesystem without
// that call it uses plain renames and undoes them if one fails. restore swaps back.
func Swap(installed, staged string) (restore func() error, err error) {
	if err := swapPaths(installed, staged); err != nil {
		return nil, err
	}
	return func() error { return swapPaths(installed, staged) }, nil
}

// swapByRename exchanges a and b with renames, for filesystems without an atomic exchange. While it runs a does not
// exist for a moment; if a step fails, the ones before it are undone.
func swapByRename(a, b string) error {
	tmp := b + ".swap"
	if _, err := os.Lstat(tmp); err == nil {
		return fmt.Errorf("%s is in the way", tmp)
	}
	if err := os.Rename(a, tmp); err != nil {
		return err
	}
	if err := os.Rename(b, a); err != nil {
		return errors.Join(err, os.Rename(tmp, a))
	}
	if err := os.Rename(tmp, b); err != nil {
		return errors.Join(err, os.Rename(a, b), os.Rename(tmp, a))
	}
	return nil
}

// KeepPrevious moves the old app (the staged path after Swap) to .HAL9001-previous.app in installedDir, replacing the
// previous one, and removes the staging folder it was in.
func KeepPrevious(oldBundle, installedDir string) error {
	previous := filepath.Join(installedDir, previousName)
	if err := os.RemoveAll(previous); err != nil {
		return fmt.Errorf("cannot remove the older previous version: %w", err)
	}
	if err := os.Rename(oldBundle, previous); err != nil {
		return fmt.Errorf("cannot keep the previous version: %w", err)
	}
	if staging := filepath.Dir(oldBundle); strings.HasPrefix(filepath.Base(staging), stagingPrefix) {
		_ = os.Remove(staging) // empty now; if not, the next update removes it
	}
	return nil
}

// toolRunner runs a macOS tool and returns its combined output.
type toolRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func runSystemTool(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // fixed system tools; the arguments are paths this package built
}

// toolError adds what a tool printed to its failure.
func toolError(err error, out []byte) error {
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return fmt.Errorf("%w: %s", err, msg)
	}
	return err
}

func dittoExtract(ctx context.Context, zipPath, dir string) error {
	out, err := runSystemTool(ctx, "/usr/bin/ditto", "-x", "-k", zipPath, dir)
	if err != nil {
		return toolError(err, out)
	}
	return nil
}

func codesignVerify(ctx context.Context, bundle string) error {
	out, err := runSystemTool(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", bundle)
	if err != nil {
		return toolError(err, out)
	}
	return nil
}

// stripQuarantine removes com.apple.quarantine from the bundle and everything in it.
func stripQuarantine(ctx context.Context, bundle string) error {
	return stripQuarantineWith(ctx, runSystemTool, bundle)
}

// stripQuarantineWith is stripQuarantine through run. xattr can fail for want of the attribute, which is the normal
// case for a download made by this process, so that is not an error.
func stripQuarantineWith(ctx context.Context, run toolRunner, bundle string) error {
	out, err := run(ctx, "/usr/bin/xattr", "-dr", "com.apple.quarantine", bundle)
	if err != nil && !strings.Contains(string(out), "No such xattr") {
		return toolError(err, out)
	}
	return nil
}

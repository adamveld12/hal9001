package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Legacy is an agent written by an older hal9001 (`hal9001 install`, `hal9001-menubar install`).
type Legacy struct {
	Label string
	Path  string
}

// Found is what is installed in ~/Library/LaunchAgents besides the app's own bundled agent.
type Found struct {
	// Headless reports the daemon-only agent of `hal9001 service install`.
	Headless     bool
	HeadlessPath string
	// Legacy lists the older agents present.
	Legacy []Legacy
}

// Conflicts reports whether anything found would fight the app's service for the daemon's port or the menubar.
func (f Found) Conflicts() bool { return f.Headless || len(f.Legacy) > 0 }

// PlistPath is where an agent with label is installed for userHome.
func PlistPath(userHome, label string) string {
	return filepath.Join(LaunchAgentsDir(userHome), label+".plist")
}

// Detect looks for the headless agent and the legacy agents under userHome.
func Detect(userHome string) (Found, error) {
	var f Found
	exists := func(label string) (string, bool, error) {
		p := PlistPath(userHome, label)
		_, err := os.Lstat(p)
		switch {
		case err == nil:
			return p, true, nil
		case errors.Is(err, fs.ErrNotExist):
			return p, false, nil
		}
		return p, false, err
	}
	p, ok, err := exists(HeadlessLabel)
	if err != nil {
		return f, err
	}
	f.Headless, f.HeadlessPath = ok, p
	for _, label := range []string{LegacyDaemonLabel, LegacyMenubarLabel} {
		p, ok, err := exists(label)
		if err != nil {
			return f, err
		}
		if ok {
			f.Legacy = append(f.Legacy, Legacy{Label: label, Path: p})
		}
	}
	return f, nil
}

// Remove boots label out of uid's GUI domain and deletes its plist. A job that is not loaded is not an error (bootout
// fails for it); a plist that cannot be deleted is.
func Remove(ctx context.Context, run Launchctl, uid int, label, plistPath string) error {
	_, _ = run(ctx, "bootout", Target(uid, label))
	if err := os.Remove(plistPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", plistPath, err)
	}
	return nil
}

// RemoveLegacy removes every legacy agent in list.
func RemoveLegacy(ctx context.Context, run Launchctl, uid int, list []Legacy) error {
	var errs []error
	for _, l := range list {
		if err := Remove(ctx, run, uid, l.Label, l.Path); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

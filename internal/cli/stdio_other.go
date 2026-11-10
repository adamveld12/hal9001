//go:build !darwin

package cli

import "os"

// redirectStdio points os.Stdout and os.Stderr at the file at path. Unlike the darwin version it cannot capture output
// the Go runtime writes straight to file descriptor 2; launchd only exists on macOS, so this is a development fallback.
func redirectStdio(path string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // the log path comes from the daemon's own environment
	if err != nil {
		return err
	}
	os.Stdout, os.Stderr = f, f
	return nil
}

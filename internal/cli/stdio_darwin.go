//go:build darwin

package cli

import (
	"fmt"
	"os"
	"syscall"
)

// redirectStdio appends everything this process writes to stdout and stderr (including a Go runtime crash report) to
// the file at path.
func redirectStdio(path string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // the log path comes from the daemon's own environment
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	for _, fd := range []int{syscall.Stdout, syscall.Stderr} {
		if err := syscall.Dup2(int(f.Fd()), fd); err != nil { //nolint:gosec // file descriptors fit in int
			return fmt.Errorf("redirect fd %d to %s: %w", fd, path, err)
		}
	}
	return nil
}

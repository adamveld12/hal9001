//go:build darwin

package update

import (
	"errors"

	"golang.org/x/sys/unix"
)

// swapPaths exchanges a and b atomically with renamex_np(RENAME_SWAP). A filesystem that cannot (ENOTSUP, or EINVAL
// for a flag it does not know) gets plain renames instead.
func swapPaths(a, b string) error {
	err := unix.RenamexNp(a, b, unix.RENAME_SWAP)
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) {
		return swapByRename(a, b)
	}
	return err
}

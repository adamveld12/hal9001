//go:build !darwin

package update

// swapPaths exchanges a and b with plain renames: the atomic exchange is a macOS call.
func swapPaths(a, b string) error { return swapByRename(a, b) }

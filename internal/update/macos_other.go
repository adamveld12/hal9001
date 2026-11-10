//go:build !darwin

package update

// MacOSVersion is empty off macOS.
func MacOSVersion() string { return "" }

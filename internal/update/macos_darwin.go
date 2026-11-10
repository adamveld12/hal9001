//go:build darwin

package update

import "golang.org/x/sys/unix"

// MacOSVersion is the running macOS version ("14.5"); empty when it cannot be read.
func MacOSVersion() string {
	v, err := unix.Sysctl("kern.osproductversion")
	if err != nil {
		return ""
	}
	return v
}

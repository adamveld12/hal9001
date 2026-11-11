package cli

import (
	"testing"
	"time"
)

// SetInstallPollInterval shortens how often `runtimes install` polls for the end of an install, for one test.
func SetInstallPollInterval(t *testing.T, d time.Duration) {
	t.Helper()
	old := installPollInterval
	installPollInterval = d
	t.Cleanup(func() { installPollInterval = old })
}

//go:build !embedui

package webui

import "io/fs"

// embedded reports that this binary has no embedded UI (built without the
// embedui tag); the UI is then served from a directory.
func embedded() (fs.FS, bool) { return nil, false }

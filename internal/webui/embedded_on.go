//go:build embedui

package webui

import (
	"io/fs"

	"hal9001/web"
)

// embedded returns the UI compiled into the binary.
func embedded() (fs.FS, bool) {
	fsys, err := web.Dist()
	if err != nil {
		return nil, false
	}
	return fsys, true
}

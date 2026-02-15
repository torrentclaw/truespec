package internal

import (
	"os"
	"runtime"
)

// atomicRename renames src to dst. On Windows, removes dst first since
// os.Rename cannot overwrite an existing file on Windows.
// On Unix systems, os.Rename is atomic when src and dst are on the same filesystem.
func atomicRename(src, dst string) error {
	if runtime.GOOS == "windows" {
		os.Remove(dst)
	}
	return os.Rename(src, dst)
}

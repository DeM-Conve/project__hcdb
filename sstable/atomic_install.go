package sstable

import (
	"os"
	"path/filepath"
)

// atomicInstall renames tmpPath to finalPath and fsyncs the containing
// directory. The rename() itself is atomic at the filesystem level, but
// the rename can still be lost on crash if the directory entry isn't
// fsync'd — a crash before that would leave finalPath missing or stale
// even though rename() already returned success.
func atomicInstall(tmpPath, finalPath string) error {
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return err
	}

	dir, err := os.Open(filepath.Dir(finalPath))
	if err != nil {
		return err
	}
	defer dir.Close()

	return dir.Sync()
}

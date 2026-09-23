//go:build !linux && !darwin

package checkpoint

import (
	"os"
	"path/filepath"
	"runtime"
)

func openCheckpointImportDirectory(path string) (*os.File, error) { return os.Open(path) }

// Windows cannot flush directory handles through os.File.Sync. Files themselves
// are still synced before publication; other systems retain directory syncing.
func syncCheckpointImportDirectory(dir *os.File) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	return dir.Sync()
}

func renameCheckpointImport(parent *os.File, stage, target string, replace bool) error {
	if replace {
		return ErrCheckpointImportAtomicUnavailable
	}
	dest := filepath.Join(parent.Name(), target)
	if _, err := os.Lstat(dest); err == nil {
		return os.ErrExist
	} else if !os.IsNotExist(err) {
		return err
	}
	// Every import publishes a nonempty directory. os.Rename cannot overwrite
	// another completed import here, even if two create-only calls race. Do not
	// emulate an exchange by removing the existing checkpoint on these systems.
	return os.Rename(filepath.Join(parent.Name(), stage), dest)
}

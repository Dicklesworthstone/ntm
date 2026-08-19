package session

import (
	"os"
	"path/filepath"
)

// IsArchived reports whether an archived session with the given name exists
// (test-only helper replicating the removed production accessor).
func IsArchived(name string) bool {
	name, err := normalizeSavedSessionName(name)
	if err != nil {
		return false
	}
	path := filepath.Join(ArchiveDir(), name+fileExtension)
	_, err = os.Stat(path)
	return err == nil
}

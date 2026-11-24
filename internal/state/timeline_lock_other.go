//go:build !linux && !darwin && !dragonfly && !freebsd && !illumos && !netbsd && !openbsd && !windows

package state

import (
	"errors"
	"os"
)

func tryTimelineFileLock(*os.File) (bool, error) {
	return false, errors.New("timeline mutation requires process-shared file locking on this platform")
}

func unlockTimelineFile(*os.File) error { return nil }

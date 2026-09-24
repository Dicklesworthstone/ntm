//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly && !windows

package supervisor

import (
	"errors"
	"os"
)

func lockDaemonOwnership(string) (*os.File, error) {
	return nil, errors.New("daemon ownership locking is unavailable on this platform")
}

func daemonProcessMayLive(int) (bool, error) {
	return true, errors.New("daemon process inspection is unavailable on this platform")
}

func syncDaemonOwnershipDirectory(string) error { return nil }

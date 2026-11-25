//go:build !linux && !darwin

package pressure

import (
	"errors"
	"os"
)

func openSpawnAdmissionFile(string) (*os.File, error) {
	return nil, errors.New("cross-process spawn admission is supported on Linux and macOS only")
}

func trySpawnAdmissionLock(*os.File) (bool, error) {
	return false, errors.New("cross-process spawn admission is unavailable on this platform")
}

func unlockSpawnAdmissionLock(*os.File) {}

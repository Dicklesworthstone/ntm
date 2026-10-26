//go:build windows

package tmux

import (
	"context"
	"errors"
	"os"
)

// createFIFO is not supported on Windows.
// Named pipes on Windows use a different API (CreateNamedPipe).
// Since tmux is Unix-only, this functionality is not available on Windows.
func createFIFO(path string) error {
	return errors.New("FIFO/named pipes not supported on Windows (tmux is Unix-only)")
}

func openLaunchEnvironmentWriter(context.Context, string) (*os.File, error) {
	return nil, errors.New("recovery environment pipes are not supported on Windows")
}

func openLaunchEnvironmentAbort(string) (*os.File, error) {
	return nil, errors.New("recovery environment pipes are not supported on Windows")
}

// OpenLaunchEnvironmentFile fails closed where no safe local tmux recovery
// transport is available.
func OpenLaunchEnvironmentFile(string) (*os.File, error) {
	return nil, errors.New("recovery environment files are not supported on Windows")
}

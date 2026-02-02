//go:build windows

package tmux

import "errors"

// createFIFO is not supported on Windows.
// Named pipes on Windows use a different API (CreateNamedPipe).
// Since tmux is Unix-only, this functionality is not available on Windows.
func createFIFO(path string) error {
	return errors.New("FIFO/named pipes not supported on Windows (tmux is Unix-only)")
}

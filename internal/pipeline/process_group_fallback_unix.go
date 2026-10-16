//go:build !windows && !linux

package pipeline

import "errors"

// A numeric procfs sweep is not a safe fallback on platforms without pidfds.
// The leader kill is still attempted by signalCommandProcessGroup; preserve
// the failed group signal so the launch receipt remains unresolved.
func killProcessGroupDescendants(int) error {
	return errors.New("identity-safe command descendant cleanup is unavailable on this platform")
}

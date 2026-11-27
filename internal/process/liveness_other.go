//go:build !darwin

package process

// nativeChildPIDs is the non-darwin stub: no native process-table snapshot,
// callers use the /proc walk or pgrep fallback.
func nativeChildPIDs(int) ([]int, bool) { return nil, false }

// nativeHasChildAlive is the non-darwin stub: no native process-table
// snapshot, callers use the portable getChildPIDs+IsAlive path.
func nativeHasChildAlive(int) (bool, bool) { return false, false }

// nativeIsAlive is the non-darwin stub: callers use the /proc or signal-0
// path.
func nativeIsAlive(int) (bool, bool) { return false, false }

// nativeProcessState is the non-darwin stub: callers use the /proc or ps
// path.
func nativeProcessState(int) (string, bool) { return "", false }

// nativeSessionTerminalForeground is the non-darwin fallback. Linux uses
// procfs; other platforms cannot establish terminal ownership here.
func nativeSessionTerminalForeground(int) (int, int, int, bool) {
	return 0, 0, 0, false
}

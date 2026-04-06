//go:build !unix

package alerts

// checkDiskSpace on non-Unix platforms (Windows, Plan 9, etc.) is a no-op.
//
// NTM targets Linux (and to a lesser extent macOS) where syscall.Statfs is
// available. On unsupported platforms this intentionally returns (nil, nil)
// so the alert generator treats disk-space as healthy rather than failed.
//
// If Windows support is ever needed, use golang.org/x/sys/windows to call
// GetDiskFreeSpaceExW and populate an Alert with the same fields as the
// Unix implementation in generator_unix.go.
func (g *Generator) checkDiskSpace() (*Alert, error) {
	return nil, nil
}

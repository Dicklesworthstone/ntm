//go:build windows

package robot

import "golang.org/x/sys/windows"

// statDiskUsage reports filesystem usage for path (Windows implementation).
//
// Mirrors internal/alerts/generator_windows.go: GetDiskFreeSpaceExW reports
// free bytes available to the calling user (honoring per-user quotas), which
// keeps available_bytes consistent with the Unix Bavail semantics.
func statDiskUsage(path string) (diskUsage, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return diskUsage{}, err
	}
	var availToCaller, totalBytes, totalFreeBytes uint64
	if err := windows.GetDiskFreeSpaceEx(pathPtr, &availToCaller, &totalBytes, &totalFreeBytes); err != nil {
		return diskUsage{}, err
	}
	used := uint64(0)
	if totalBytes > totalFreeBytes {
		used = totalBytes - totalFreeBytes
	}
	return diskUsage{
		Path:       path,
		UsedBytes:  used,
		AvailBytes: availToCaller,
	}, nil
}

//go:build unix

package robot

import "syscall"

// statDiskUsage reports filesystem usage for path (Unix implementation).
//
// Mirrors internal/alerts/generator_unix.go: values convert through float64
// because Statfs_t field types vary across Unix variants (Bavail is int64 on
// Linux/FreeBSD and can be negative under root reserve, uint64 on macOS).
func statDiskUsage(path string) (diskUsage, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return diskUsage{}, err
	}
	bsize := float64(stat.Bsize)
	avail := float64(stat.Bavail) * bsize
	if avail < 0 {
		avail = 0
	}
	used := (float64(stat.Blocks) - float64(stat.Bfree)) * bsize
	if used < 0 {
		used = 0
	}
	return diskUsage{
		Path:       path,
		UsedBytes:  uint64(used),
		AvailBytes: uint64(avail),
	}, nil
}

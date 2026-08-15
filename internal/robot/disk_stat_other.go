//go:build !unix && !windows

package robot

import "errors"

// statDiskUsage on genuinely unsupported platforms (Plan 9, JS, wasip1, ...)
// reports an error so callers omit the disk section rather than fabricating
// usage. Mirrors internal/alerts/generator_other.go's degrade-to-healthy
// stance for the same platform set.
func statDiskUsage(string) (diskUsage, error) {
	return diskUsage{}, errors.New("disk usage sampling unsupported on this platform")
}

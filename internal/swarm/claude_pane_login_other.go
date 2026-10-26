//go:build !unix

package swarm

import (
	"io/fs"
	"os"
)

// openSingleLinkRegularFile does not read a file's link count on this platform
// (Windows), so it never reports a single-link file. A .credentials.json in a
// pane dir is then never taken for the pane's own login and keeps counting as
// exposed: failing closed beats accepting a hard link it cannot detect.
func openSingleLinkRegularFile(_ string, _ fs.FileInfo) (*os.File, fs.FileInfo, error) {
	return nil, nil, nil
}

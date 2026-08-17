// Package webui embeds the static export of the Next.js dashboard.
//
// The dist/ tree is the committed output of `next build` in web/ (output:
// "export"), copied verbatim by scripts/build_web.sh. Embedding it makes
// UI/binary version lockstep structural: the UI ships inside every binary,
// stamped with web/package.json's version, which the lockstep test in this
// package pins to the repo VERSION file (F1, bd-ws5-ship-or-cut-jv0rc.1).
//
// Kill criterion (Q4, recorded on the parent bead): demoting the web UI to
// source-only is one commit — delete the embed directive below and have
// `ntm web` return NOT_AVAILABLE.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the embedded static site rooted at the export directory
// (index.html and friends at the top level).
func FS() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}

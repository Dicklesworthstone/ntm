package agentsession

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Bound metadata work on the status/rotation polling path. A workspace larger
// than this needs an exact process binding, not a partial "newest" answer.
const geminiUsageDiscoveryLimit = 4096

// FindGeminiTranscriptForCwd locates a workspace's newest native Gemini
// transcript without invoking CASR or inspecting processes. Registry and
// legacy hash-directory resolution are shared with capture/resume discovery.
// Unlike session-ID discovery, usage discovery never decodes every candidate's
// header: it chooses by file metadata, then the accounting reader opens just
// that snapshot. Callers must establish that cwd attribution is unambiguous.
// A nonzero newerThan is a strict mtime lower bound, not a freshness claim
// about the usage record inside the file.
func FindGeminiTranscriptForCwd(ctx context.Context, home, cwd string, newerThan time.Time) (string, bool) {
	if ctx == nil || ctx.Err() != nil || home == "" || !filepath.IsAbs(cwd) {
		return "", false
	}
	root := filepath.Join(home, ".gemini", "tmp")
	var newest string
	var modified time.Time
	examined := 0
	for _, dir := range geminiWorkspaceChatDirsContext(ctx, root, filepath.Clean(cwd)) {
		// Avoid opening a non-directory path (particularly a named pipe).
		info, err := os.Stat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", false
		}
		if !info.IsDir() {
			return "", false
		}
		file, err := os.Open(dir)
		if err != nil {
			return "", false
		}
		for {
			if ctx.Err() != nil {
				_ = file.Close()
				return "", false
			}
			entries, readErr := file.ReadDir(128)
			for _, entry := range entries {
				examined++
				if examined > geminiUsageDiscoveryLimit || ctx.Err() != nil {
					_ = file.Close()
					return "", false
				}
				name := entry.Name()
				if !strings.HasPrefix(name, "session-") ||
					(!strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".jsonl")) {
					continue
				}
				info, err := entry.Info()
				if err != nil {
					_ = file.Close()
					return "", false // An unreadable candidate may be the newest.
				}
				if !info.Mode().IsRegular() || (!newerThan.IsZero() && !info.ModTime().After(newerThan)) {
					continue
				}
				path := filepath.Join(dir, name)
				if newest == "" || info.ModTime().After(modified) ||
					(info.ModTime().Equal(modified) && path < newest) {
					newest, modified = path, info.ModTime()
				}
			}
			if readErr != nil {
				_ = file.Close()
				if readErr != io.EOF {
					return "", false
				}
				break
			}
		}
	}
	if ctx.Err() != nil {
		return "", false
	}
	return newest, newest != ""
}

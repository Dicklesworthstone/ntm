package handoff

import (
	"log/slog"
	"path/filepath"
)

// NewWriterWithOptions creates a Writer with custom options (test-only helper
// replicating the removed production constructor, used by rotation tests).
func NewWriterWithOptions(projectDir string, maxPerDir int, logger *slog.Logger) *Writer {
	if maxPerDir <= 0 {
		maxPerDir = DefaultMaxPerDir
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Writer{
		baseDir:   filepath.Join(projectDir, ".ntm", "handoffs"),
		maxPerDir: maxPerDir,
		logger:    logger.With("component", "handoff.writer"),
	}
}

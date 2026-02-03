// Package checkpoint provides scrollback capture with compression for checkpoints.
package checkpoint

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// ScrollbackCapture holds the captured scrollback data for a pane.
type ScrollbackCapture struct {
	// PaneID is the tmux pane identifier
	PaneID string
	// Lines is the number of lines captured
	Lines int
	// Content is the raw scrollback content
	Content string
	// Compressed is the gzip-compressed content
	Compressed []byte
	// Size is the size of the compressed content in bytes
	Size int64
	// Skipped indicates if capture was skipped (e.g., due to size limits)
	Skipped bool
	// SkipReason explains why capture was skipped
	SkipReason string
}

// ScrollbackConfig holds configuration for scrollback capture.
type ScrollbackConfig struct {
	// Lines is the number of lines to capture (default 5000)
	Lines int
	// Compress enables gzip compression (default true)
	Compress bool
	// MaxSizeMB is the maximum size in megabytes (0 = no limit)
	MaxSizeMB int
	// Timeout is the capture timeout (default 30s)
	Timeout time.Duration
}

// DefaultScrollbackConfig returns the default scrollback configuration.
func DefaultScrollbackConfig() ScrollbackConfig {
	return ScrollbackConfig{
		Lines:     5000,
		Compress:  true,
		MaxSizeMB: 10,
		Timeout:   30 * time.Second,
	}
}

// CaptureScrollback captures scrollback from a tmux pane with optional compression.
func CaptureScrollback(session, paneID string, config ScrollbackConfig) (*ScrollbackCapture, error) {
	ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
	defer cancel()

	return CaptureScrollbackContext(ctx, session, paneID, config)
}

// CaptureScrollbackContext captures scrollback with context for cancellation.
func CaptureScrollbackContext(ctx context.Context, session, paneID string, config ScrollbackConfig) (*ScrollbackCapture, error) {
	capture := &ScrollbackCapture{
		PaneID: paneID,
		Lines:  config.Lines,
	}

	// Use pane ID directly - tmux pane IDs (e.g., %123) are globally unique
	// and don't need session prefix. The session parameter is kept for API
	// compatibility but unused for capture.
	_ = session

	// Capture pane output
	content, err := tmux.CapturePaneOutputContext(ctx, paneID, config.Lines)
	if err != nil {
		return nil, fmt.Errorf("capturing pane output: %w", err)
	}

	capture.Content = content

	// Check raw size limit before compression
	rawSizeMB := float64(len(content)) / (1024 * 1024)
	if config.MaxSizeMB > 0 && rawSizeMB > float64(config.MaxSizeMB)*10 {
		// If raw content is 10x the max, skip entirely (won't compress well enough)
		capture.Skipped = true
		capture.SkipReason = fmt.Sprintf("raw content too large: %.2f MB (limit %d MB)", rawSizeMB, config.MaxSizeMB)
		return capture, nil
	}

	// Compress if enabled
	if config.Compress {
		compressed, err := gzipCompress([]byte(content))
		if err != nil {
			return nil, fmt.Errorf("compressing scrollback: %w", err)
		}
		capture.Compressed = compressed
		capture.Size = int64(len(compressed))

		// Check compressed size limit
		compressedSizeMB := float64(capture.Size) / (1024 * 1024)
		if config.MaxSizeMB > 0 && compressedSizeMB > float64(config.MaxSizeMB) {
			capture.Skipped = true
			capture.SkipReason = fmt.Sprintf("compressed size exceeds limit: %.2f MB > %d MB", compressedSizeMB, config.MaxSizeMB)
			capture.Compressed = nil // Don't keep oversized data
			return capture, nil
		}
	} else {
		capture.Size = int64(len(content))
	}

	return capture, nil
}

// gzipCompress compresses data using gzip.
func gzipCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)

	if _, err := writer.Write(data); err != nil {
		return nil, err
	}

	if err := writer.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// gzipDecompress decompresses gzip data.
func gzipDecompress(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return io.ReadAll(reader)
}

// SaveCompressedScrollback saves compressed scrollback to a file.
func (s *Storage) SaveCompressedScrollback(sessionName, checkpointID, paneID string, data []byte) (string, error) {
	panesDir := s.PanesDirPath(sessionName, checkpointID)
	if err := os.MkdirAll(panesDir, 0755); err != nil {
		return "", fmt.Errorf("creating panes directory: %w", err)
	}

	// Use .txt.gz extension for compressed files
	filename := fmt.Sprintf("pane_%s.txt.gz", sanitizeName(paneID))
	fullPath := filepath.Join(panesDir, filename)

	if err := util.AtomicWriteFile(fullPath, data, 0600); err != nil {
		return "", fmt.Errorf("saving compressed scrollback: %w", err)
	}

	return filepath.Join(PanesDir, filename), nil
}

// LoadCompressedScrollback reads and decompresses scrollback from a file.
func (s *Storage) LoadCompressedScrollback(sessionName, checkpointID, paneID string) (string, error) {
	// Try compressed file first
	filename := fmt.Sprintf("pane_%s.txt.gz", sanitizeName(paneID))
	fullPath := filepath.Join(s.PanesDirPath(sessionName, checkpointID), filename)

	data, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Fall back to uncompressed file
			return s.LoadScrollback(sessionName, checkpointID, paneID)
		}
		return "", fmt.Errorf("reading compressed scrollback: %w", err)
	}

	decompressed, err := gzipDecompress(data)
	if err != nil {
		return "", fmt.Errorf("decompressing scrollback: %w", err)
	}

	return string(decompressed), nil
}

// captureScrollbackEnhanced captures scrollback with compression support.
func (c *Capturer) captureScrollbackEnhanced(cp *Checkpoint, config ScrollbackConfig) error {
	for i := range cp.Session.Panes {
		pane := &cp.Session.Panes[i]

		capture, err := CaptureScrollback(cp.SessionName, pane.ID, config)
		if err != nil {
			// Log error but continue with other panes
			slog.Warn("failed to capture scrollback", "pane", pane.Index, "error", err)
			continue
		}

		if capture.Skipped {
			slog.Warn("skipped scrollback", "pane", pane.Index, "reason", capture.SkipReason)
			continue
		}

		// Save scrollback
		var relativePath string
		var saveErr error

		if config.Compress && len(capture.Compressed) > 0 {
			relativePath, saveErr = c.storage.SaveCompressedScrollback(cp.SessionName, cp.ID, pane.ID, capture.Compressed)
		} else {
			relativePath, saveErr = c.storage.SaveScrollback(cp.SessionName, cp.ID, pane.ID, capture.Content)
		}

		if saveErr != nil {
			slog.Warn("failed to save scrollback", "pane", pane.Index, "error", saveErr)
			continue
		}

		pane.ScrollbackFile = relativePath
		pane.ScrollbackLines = countLines(capture.Content)
	}

	return nil
}

package checkpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGzipCompressDecompress(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"empty", ""},
		{"small", "hello world"},
		{"multiline", "line1\nline2\nline3\n"},
		{"large", strings.Repeat("this is a test line\n", 1000)},
		{"binary-like", string([]byte{0, 1, 2, 255, 254, 253})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compressed, err := gzipCompress([]byte(tt.data))
			if err != nil {
				t.Fatalf("gzipCompress failed: %v", err)
			}

			decompressed, err := gzipDecompress(compressed)
			if err != nil {
				t.Fatalf("gzipDecompress failed: %v", err)
			}

			if string(decompressed) != tt.data {
				t.Errorf("round-trip failed: got %q, want %q", string(decompressed), tt.data)
			}
		})
	}
}

func TestGzipDecompressLimited_RejectsOversizedOutput(t *testing.T) {
	data := []byte("0123456789")
	compressed, err := gzipCompress(data)
	if err != nil {
		t.Fatalf("gzipCompress failed: %v", err)
	}

	got, err := gzipDecompressLimited(compressed, int64(len(data)))
	if err != nil {
		t.Fatalf("gzipDecompressLimited exact limit failed: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("gzipDecompressLimited exact limit = %q, want %q", string(got), string(data))
	}

	_, err = gzipDecompressLimited(compressed, int64(len(data)-1))
	if err == nil {
		t.Fatal("gzipDecompressLimited oversized output error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "decompressed scrollback exceeds limit") {
		t.Fatalf("gzipDecompressLimited oversized output error = %v, want limit failure", err)
	}
}

func TestGzipCompressionRatio(t *testing.T) {
	// Highly compressible data (repeated pattern)
	data := strings.Repeat("hello world this is a test\n", 1000)
	compressed, err := gzipCompress([]byte(data))
	if err != nil {
		t.Fatalf("gzipCompress failed: %v", err)
	}

	ratio := float64(len(compressed)) / float64(len(data))
	t.Logf("Compression ratio: %.2f%% (original: %d, compressed: %d)",
		ratio*100, len(data), len(compressed))

	// For highly repetitive data, expect significant compression
	if ratio > 0.1 { // Should compress to less than 10%
		t.Errorf("Expected better compression ratio, got %.2f%%", ratio*100)
	}
}

func TestScrollbackConfig_Defaults(t *testing.T) {
	config := DefaultScrollbackConfig()

	if config.Lines != 5000 {
		t.Errorf("Default lines = %d, want 5000", config.Lines)
	}
	if !config.Compress {
		t.Error("Default compress should be true")
	}
	if config.MaxSizeMB != 10 {
		t.Errorf("Default MaxSizeMB = %d, want 10", config.MaxSizeMB)
	}
}

func TestStorage_SaveCompressedScrollback(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-scrollback-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)

	// Test data
	sessionName := "test-session"
	checkpointID := "test-checkpoint"
	paneID := "%0"
	content := "Hello, this is scrollback content\nLine 2\nLine 3\n"

	// Compress the content
	compressed, err := gzipCompress([]byte(content))
	if err != nil {
		t.Fatalf("gzipCompress failed: %v", err)
	}

	// Save compressed scrollback
	relativePath, err := storage.SaveCompressedScrollback(sessionName, checkpointID, paneID, compressed)
	if err != nil {
		t.Fatalf("SaveCompressedScrollback failed: %v", err)
	}

	// Verify path format
	if !strings.HasSuffix(relativePath, ".txt.gz") {
		t.Errorf("Expected .txt.gz suffix, got %s", relativePath)
	}

	// Verify file exists
	fullPath := filepath.Join(tmpDir, sessionName, checkpointID, relativePath)
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		t.Errorf("Compressed scrollback file not created at %s", fullPath)
	}

	// Load and verify content
	loaded, err := storage.LoadCompressedScrollback(sessionName, checkpointID, paneID)
	if err != nil {
		t.Fatalf("LoadCompressedScrollback failed: %v", err)
	}

	if loaded != content {
		t.Errorf("Loaded content mismatch: got %q, want %q", loaded, content)
	}
}

func TestStorage_SaveCompressedScrollback_RejectsInvalidIdentifiers(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	compressed, err := gzipCompress([]byte("content"))
	if err != nil {
		t.Fatalf("gzipCompress failed: %v", err)
	}

	tests := []struct {
		name        string
		sessionName string
		checkpoint  string
		wantErr     string
	}{
		{
			name:        "invalid session",
			sessionName: "../escape",
			checkpoint:  "valid-id",
			wantErr:     "invalid session name",
		},
		{
			name:        "invalid checkpoint",
			sessionName: "valid_session",
			checkpoint:  "../escape",
			wantErr:     "invalid checkpoint ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := storage.SaveCompressedScrollback(tt.sessionName, tt.checkpoint, "%0", compressed)
			if err == nil {
				t.Fatal("SaveCompressedScrollback() error = nil, want validation failure")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("SaveCompressedScrollback() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestStorage_LoadCompressedScrollback_FallbackToUncompressed(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-scrollback-fallback-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)

	// Test data
	sessionName := "test-session"
	checkpointID := "test-checkpoint"
	paneID := "%0"
	content := "Uncompressed scrollback content\n"

	// Save uncompressed scrollback using the old method
	relativePath, err := storage.SaveScrollback(sessionName, checkpointID, paneID, content)
	if err != nil {
		t.Fatalf("SaveScrollback failed: %v", err)
	}

	// Verify it's a .txt file
	if !strings.HasSuffix(relativePath, ".txt") {
		t.Errorf("Expected .txt suffix, got %s", relativePath)
	}

	// Load using compressed method (should fall back to uncompressed)
	loaded, err := storage.LoadCompressedScrollback(sessionName, checkpointID, paneID)
	if err != nil {
		t.Fatalf("LoadCompressedScrollback fallback failed: %v", err)
	}

	if loaded != content {
		t.Errorf("Fallback content mismatch: got %q, want %q", loaded, content)
	}
}

func TestStorage_LoadScrollback_RejectsSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "test-session"
	checkpointID := "test-checkpoint"
	paneID := "%0"

	cp := &Checkpoint{
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	outsidePath := filepath.Join(t.TempDir(), "outside-scrollback.txt")
	if err := os.WriteFile(outsidePath, []byte("secret"), 0600); err != nil {
		t.Fatalf("WriteFile(outside scrollback) failed: %v", err)
	}

	relPath := filepath.Join(PanesDir, fmt.Sprintf("pane_%s.txt", sanitizeName(paneID)))
	if err := os.Symlink(outsidePath, filepath.Join(storage.CheckpointDir(sessionName, checkpointID), relPath)); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	_, err := storage.LoadScrollback(sessionName, checkpointID, paneID)
	if err == nil {
		t.Fatal("LoadScrollback() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("LoadScrollback() error = %v, want symlink rejection", err)
	}
}

func TestStorage_LoadCompressedScrollback_RejectsSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "test-session"
	checkpointID := "test-checkpoint"
	paneID := "%0"

	cp := &Checkpoint{
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	outsidePath := filepath.Join(t.TempDir(), "outside-scrollback.txt.gz")
	if err := os.WriteFile(outsidePath, []byte("not really gzip"), 0600); err != nil {
		t.Fatalf("WriteFile(outside compressed scrollback) failed: %v", err)
	}

	relPath := filepath.Join(PanesDir, fmt.Sprintf("pane_%s.txt.gz", sanitizeName(paneID)))
	if err := os.Symlink(outsidePath, filepath.Join(storage.CheckpointDir(sessionName, checkpointID), relPath)); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	_, err := storage.LoadCompressedScrollback(sessionName, checkpointID, paneID)
	if err == nil {
		t.Fatal("LoadCompressedScrollback() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("LoadCompressedScrollback() error = %v, want symlink rejection", err)
	}
}

func TestStorage_LoadPaneScrollback_RejectsInvalidIdentifiers(t *testing.T) {
	storage := NewStorageWithDir(t.TempDir())
	pane := PaneState{
		ID:             "%0",
		ScrollbackFile: filepath.Join(PanesDir, "pane__0.txt"),
	}

	tests := []struct {
		name        string
		sessionName string
		checkpoint  string
		wantErr     string
	}{
		{
			name:        "invalid session",
			sessionName: "../escape",
			checkpoint:  "valid-id",
			wantErr:     "invalid session name",
		},
		{
			name:        "invalid checkpoint",
			sessionName: "valid_session",
			checkpoint:  "../escape",
			wantErr:     "invalid checkpoint ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := storage.LoadPaneScrollback(tt.sessionName, tt.checkpoint, pane)
			if err == nil {
				t.Fatal("LoadPaneScrollback() error = nil, want validation failure")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("LoadPaneScrollback() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestScrollbackCapture_SizeLimit(t *testing.T) {
	// Test that the size limit check works correctly
	config := ScrollbackConfig{
		Lines:     1000,
		Compress:  true,
		MaxSizeMB: 1, // 1 MB limit
	}

	// Create content that's larger than 10x the limit (should be skipped)
	largeContent := strings.Repeat("x", 11*1024*1024) // 11 MB

	// Simulate the size check logic
	rawSizeMB := float64(len(largeContent)) / (1024 * 1024)
	maxAllowed := float64(config.MaxSizeMB) * 10

	if rawSizeMB <= maxAllowed {
		t.Errorf("Expected rawSizeMB (%.2f) > maxAllowed (%.2f)", rawSizeMB, maxAllowed)
	}
}

func TestScrollbackArtifactSummary_RecordsCompressedArtifact(t *testing.T) {
	content := strings.Repeat("agent output line\n", 256)
	compressed, err := gzipCompress([]byte(content))
	if err != nil {
		t.Fatalf("gzipCompress failed: %v", err)
	}
	config := ScrollbackConfig{
		Lines:     5000,
		Compress:  true,
		MaxSizeMB: 10,
	}
	capture := &ScrollbackCapture{
		PaneID:     "%0",
		Lines:      config.Lines,
		Content:    content,
		Compressed: compressed,
		Size:       int64(len(compressed)),
	}

	got := scrollbackArtifactSummary(capture, config, filepath.Join(PanesDir, "pane__0.txt.gz"))

	if !got.Captured || !got.ArtifactPreserved {
		t.Fatalf("summary captured/preserved = %v/%v, want true/true", got.Captured, got.ArtifactPreserved)
	}
	if !got.Compacted || got.Compression != scrollbackCompressionGzip {
		t.Fatalf("summary compaction = %v/%q, want gzip", got.Compacted, got.Compression)
	}
	if got.LineCount != countLines(content) {
		t.Fatalf("summary LineCount = %d, want %d", got.LineCount, countLines(content))
	}
	if got.RawBytes != len(content) {
		t.Fatalf("summary RawBytes = %d, want %d", got.RawBytes, len(content))
	}
	if got.StoredBytes != int64(len(compressed)) {
		t.Fatalf("summary StoredBytes = %d, want %d", got.StoredBytes, len(compressed))
	}
	if got.RequestedLines != config.Lines || got.MaxSizeMB != config.MaxSizeMB {
		t.Fatalf("summary config = lines %d max %d, want %d/%d", got.RequestedLines, got.MaxSizeMB, config.Lines, config.MaxSizeMB)
	}
	if got.Skipped || got.Degraded || got.Reason != "" {
		t.Fatalf("summary skip/degraded/reason = %v/%v/%q, want false/false/empty", got.Skipped, got.Degraded, got.Reason)
	}
}

func TestScrollbackArtifactSummary_RecordsSkippedCapture(t *testing.T) {
	config := ScrollbackConfig{
		Lines:     2000,
		Compress:  true,
		MaxSizeMB: 1,
	}
	capture := &ScrollbackCapture{
		PaneID:     "%0",
		Lines:      config.Lines,
		Content:    strings.Repeat("x", 1024),
		Skipped:    true,
		SkipReason: "compressed size exceeds limit",
	}

	got := scrollbackArtifactSummary(capture, config, "")

	if got.Captured || got.ArtifactPreserved {
		t.Fatalf("summary captured/preserved = %v/%v, want false/false", got.Captured, got.ArtifactPreserved)
	}
	if got.Compacted || got.Compression != "" || got.StoredBytes != 0 {
		t.Fatalf("summary compaction = %v/%q stored=%d, want no compaction", got.Compacted, got.Compression, got.StoredBytes)
	}
	if !got.Skipped || !got.Degraded || got.Reason != capture.SkipReason {
		t.Fatalf("summary skip/degraded/reason = %v/%v/%q, want true/true/%q", got.Skipped, got.Degraded, got.Reason, capture.SkipReason)
	}
	if got.RawBytes != len(capture.Content) {
		t.Fatalf("summary RawBytes = %d, want %d", got.RawBytes, len(capture.Content))
	}
}

func TestCheckpointOptions_ScrollbackConfig(t *testing.T) {
	// Test default options
	opts := defaultOptions()
	if opts.scrollbackLines != 5000 {
		t.Errorf("Default scrollbackLines = %d, want 5000", opts.scrollbackLines)
	}
	if !opts.scrollbackCompress {
		t.Error("Default scrollbackCompress should be true")
	}
	if opts.scrollbackMaxSizeMB != 10 {
		t.Errorf("Default scrollbackMaxSizeMB = %d, want 10", opts.scrollbackMaxSizeMB)
	}

	// Test option functions
	opts = defaultOptions()
	WithScrollbackLines(2000)(&opts)
	if opts.scrollbackLines != 2000 {
		t.Errorf("scrollbackLines = %d, want 2000", opts.scrollbackLines)
	}

	opts = defaultOptions()
	WithScrollbackCompress(false)(&opts)
	if opts.scrollbackCompress {
		t.Error("scrollbackCompress should be false after WithScrollbackCompress(false)")
	}

	opts = defaultOptions()
	WithScrollbackMaxSizeMB(5)(&opts)
	if opts.scrollbackMaxSizeMB != 5 {
		t.Errorf("scrollbackMaxSizeMB = %d, want 5", opts.scrollbackMaxSizeMB)
	}
}

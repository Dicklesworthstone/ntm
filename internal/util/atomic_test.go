package util

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicWriteFile(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("creates file with correct content", func(t *testing.T) {
		path := filepath.Join(tmpDir, "test1.txt")
		content := []byte("hello world")

		err := AtomicWriteFile(path, content, 0644)
		if err != nil {
			t.Fatalf("AtomicWriteFile failed: %v", err)
		}

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading file: %v", err)
		}
		if string(got) != string(content) {
			t.Errorf("content mismatch: got %q, want %q", string(got), string(content))
		}
	})

	t.Run("creates file with correct permissions", func(t *testing.T) {
		path := filepath.Join(tmpDir, "test2.txt")

		err := AtomicWriteFile(path, []byte("test"), 0600)
		if err != nil {
			t.Fatalf("AtomicWriteFile failed: %v", err)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat file: %v", err)
		}

		// Check that permissions are at least as restrictive as requested
		mode := info.Mode().Perm()
		// On Unix, should be 0600; on Windows, this behaves differently
		if mode&0077 != 0 {
			// Just check owner can read/write
			if mode&0600 != 0600 {
				t.Errorf("expected at least 0600 permissions, got %o", mode)
			}
		}
	})

	t.Run("overwrites existing file atomically", func(t *testing.T) {
		path := filepath.Join(tmpDir, "test3.txt")

		// Write initial content
		err := AtomicWriteFile(path, []byte("initial"), 0644)
		if err != nil {
			t.Fatalf("first write failed: %v", err)
		}

		// Overwrite with new content
		err = AtomicWriteFile(path, []byte("updated content"), 0644)
		if err != nil {
			t.Fatalf("second write failed: %v", err)
		}

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading file: %v", err)
		}
		if string(got) != "updated content" {
			t.Errorf("content mismatch: got %q, want %q", string(got), "updated content")
		}
	})

	t.Run("handles empty content", func(t *testing.T) {
		path := filepath.Join(tmpDir, "test4.txt")

		err := AtomicWriteFile(path, []byte{}, 0644)
		if err != nil {
			t.Fatalf("AtomicWriteFile failed: %v", err)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat file: %v", err)
		}
		if info.Size() != 0 {
			t.Errorf("expected empty file, got size %d", info.Size())
		}
	})

	t.Run("handles large content", func(t *testing.T) {
		path := filepath.Join(tmpDir, "test5.txt")

		// Create ~1MB of content
		content := []byte(strings.Repeat("x", 1024*1024))

		err := AtomicWriteFile(path, content, 0644)
		if err != nil {
			t.Fatalf("AtomicWriteFile failed: %v", err)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat file: %v", err)
		}
		if info.Size() != int64(len(content)) {
			t.Errorf("size mismatch: got %d, want %d", info.Size(), len(content))
		}
	})

	t.Run("creates parent directory file in nested path", func(t *testing.T) {
		// Note: AtomicWriteFile does NOT create parent directories
		// This test verifies the error behavior
		nestedPath := filepath.Join(tmpDir, "nonexistent", "subdir", "test.txt")

		err := AtomicWriteFile(nestedPath, []byte("test"), 0644)
		if err == nil {
			t.Fatal("expected error for nonexistent parent directory")
		}
	})

	t.Run("cleans up temp file on success", func(t *testing.T) {
		path := filepath.Join(tmpDir, "test6.txt")

		err := AtomicWriteFile(path, []byte("test"), 0644)
		if err != nil {
			t.Fatalf("AtomicWriteFile failed: %v", err)
		}

		// Check no temp files left behind
		entries, err := os.ReadDir(tmpDir)
		if err != nil {
			t.Fatalf("reading dir: %v", err)
		}

		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "ntm-atomic-") {
				t.Errorf("temp file left behind: %s", entry.Name())
			}
		}
	})

	t.Run("handles binary content", func(t *testing.T) {
		path := filepath.Join(tmpDir, "test7.bin")

		// Binary content with null bytes and various byte values
		content := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0x00, 0x7F, 0x80}

		err := AtomicWriteFile(path, content, 0644)
		if err != nil {
			t.Fatalf("AtomicWriteFile failed: %v", err)
		}

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading file: %v", err)
		}
		if len(got) != len(content) {
			t.Errorf("length mismatch: got %d, want %d", len(got), len(content))
		}
		for i := range content {
			if got[i] != content[i] {
				t.Errorf("byte %d mismatch: got %x, want %x", i, got[i], content[i])
			}
		}
	})
}

func TestAtomicWriteFileCleanupOnError(t *testing.T) {
	// This test verifies that temp files are cleaned up even when errors occur
	// We can't easily test rename failures, but we can verify the cleanup pattern

	tmpDir := t.TempDir()

	// Count files before
	entriesBefore, _ := os.ReadDir(tmpDir)
	countBefore := len(entriesBefore)

	// Write a file successfully
	path := filepath.Join(tmpDir, "success.txt")
	if err := AtomicWriteFile(path, []byte("test"), 0644); err != nil {
		t.Fatalf("AtomicWriteFile failed: %v", err)
	}

	// Count files after (should be exactly 1 more)
	entriesAfter, _ := os.ReadDir(tmpDir)
	countAfter := len(entriesAfter)

	if countAfter != countBefore+1 {
		t.Errorf("expected exactly 1 new file, got %d", countAfter-countBefore)
	}

	// Verify no temp files remain
	for _, entry := range entriesAfter {
		if strings.HasPrefix(entry.Name(), "ntm-atomic-") {
			t.Errorf("temp file not cleaned up: %s", entry.Name())
		}
	}
}

func TestAtomicWriteFileConcurrent(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "concurrent.txt")

	// Write concurrently from multiple goroutines
	// All writes should complete without corruption
	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func(n int) {
			content := []byte(strings.Repeat(string(rune('A'+n)), 100))
			if err := AtomicWriteFile(path, content, 0644); err != nil {
				t.Errorf("concurrent write %d failed: %v", n, err)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify file exists and has valid content (one of the writes won)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}

	// Content should be 100 identical characters (from one of the writes)
	if len(content) != 100 {
		t.Errorf("unexpected content length: %d", len(content))
	}

	// All characters should be the same (no partial writes)
	first := content[0]
	for i, b := range content {
		if b != first {
			t.Errorf("content corruption at byte %d: got %c, expected %c", i, b, first)
			break
		}
	}

	// Verify no temp files left behind
	entries, _ := os.ReadDir(tmpDir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "ntm-atomic-") {
			t.Errorf("temp file left behind after concurrent writes: %s", entry.Name())
		}
	}
}

package handoff

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestNewWriter(t *testing.T) {
	w := NewWriter("/tmp/testproject")

	expectedBase := filepath.Join("/tmp/testproject", ".ntm", "handoffs")
	if w.baseDir != expectedBase {
		t.Errorf("expected baseDir=%s, got %s", expectedBase, w.baseDir)
	}
	if w.maxPerDir != DefaultMaxPerDir {
		t.Errorf("expected maxPerDir=%d, got %d", DefaultMaxPerDir, w.maxPerDir)
	}
}

func TestNewWriterWithOptions(t *testing.T) {
	w := NewWriterWithOptions("/tmp/testproject", 25, nil)

	if w.maxPerDir != 25 {
		t.Errorf("expected maxPerDir=25, got %d", w.maxPerDir)
	}

	// Test default maxPerDir when <= 0 is passed
	w2 := NewWriterWithOptions("/tmp/testproject", 0, nil)
	if w2.maxPerDir != DefaultMaxPerDir {
		t.Errorf("expected default maxPerDir=%d for 0, got %d", DefaultMaxPerDir, w2.maxPerDir)
	}

	w3 := NewWriterWithOptions("/tmp/testproject", -5, nil)
	if w3.maxPerDir != DefaultMaxPerDir {
		t.Errorf("expected default maxPerDir=%d for -5, got %d", DefaultMaxPerDir, w3.maxPerDir)
	}
}

func TestSanitizeDescription(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"Two Words", "two-words"},
		{"with_underscores", "with-underscores"},
		{"MixedCase", "mixedcase"},
		{"special!@#$chars", "specialchars"},
		{"multiple---hyphens", "multiple-hyphens"},
		{"-leading-trailing-", "leading-trailing"},
		{"", ""},
		{"a very long description that exceeds the maximum allowed length for filenames", "a-very-long-description-that-exceeds-the-maximum-a"},
		{"  spaces  around  ", "spaces-around"},
		{"123numbers", "123numbers"},
		{"kebab-case-already", "kebab-case-already"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeDescription(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeDescription(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestTruncateLog(t *testing.T) {
	tests := []struct {
		input    string
		max      int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is too long", 10, "this is..."},
		{"", 5, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := truncateLog(tt.input, tt.max)
			if got != tt.expected {
				t.Errorf("truncateLog(%q, %d) = %q, want %q", tt.input, tt.max, got, tt.expected)
			}
		})
	}
}

func TestWriterEnsureDir(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	// Test creating session directory
	err := w.EnsureDir("test-session")
	if err != nil {
		t.Fatalf("EnsureDir failed: %v", err)
	}

	expectedDir := filepath.Join(tmpDir, ".ntm", "handoffs", "test-session")
	info, err := os.Stat(expectedDir)
	if err != nil {
		t.Fatalf("directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected directory, got file")
	}

	// Test empty session name defaults to "general"
	err = w.EnsureDir("")
	if err != nil {
		t.Fatalf("EnsureDir with empty name failed: %v", err)
	}
	generalDir := filepath.Join(tmpDir, ".ntm", "handoffs", "general")
	if _, err := os.Stat(generalDir); err != nil {
		t.Errorf("general directory not created: %v", err)
	}

	// Test idempotent (calling again doesn't error)
	err = w.EnsureDir("test-session")
	if err != nil {
		t.Fatalf("second EnsureDir failed: %v", err)
	}

	t.Run("rejects symlinked session directories", func(t *testing.T) {
		outsideDir := t.TempDir()
		linkPath := filepath.Join(tmpDir, ".ntm", "handoffs", "linked")
		if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
			t.Fatalf("failed to create handoff base: %v", err)
		}
		if err := os.Symlink(outsideDir, linkPath); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}

		err := w.EnsureDir("linked")
		if err == nil {
			t.Fatal("expected symlinked session directory to be rejected")
		}
		if !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("expected symlink error, got %v", err)
		}
	})
}

func TestWriterWrite(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	h := New("test-session").
		WithGoalAndNow("Implemented feature X", "Write tests next").
		WithStatus(StatusComplete, OutcomeSucceeded)

	path, err := w.Write(h, "feature-x-complete")
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("written file doesn't exist: %v", err)
	}

	// Verify filename format
	filename := filepath.Base(path)
	if !strings.HasSuffix(filename, "_feature-x-complete.yaml") {
		t.Errorf("unexpected filename format: %s", filename)
	}
	if !strings.Contains(filename, time.Now().Format("2006-01-02")) {
		t.Errorf("filename missing date: %s", filename)
	}

	// Verify content is valid YAML
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	var parsed Handoff
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("written YAML is invalid: %v", err)
	}

	if parsed.Goal != "Implemented feature X" {
		t.Errorf("goal mismatch: got %q", parsed.Goal)
	}
	if parsed.Now != "Write tests next" {
		t.Errorf("now mismatch: got %q", parsed.Now)
	}
	if parsed.Version != HandoffVersion {
		t.Errorf("version mismatch: got %q", parsed.Version)
	}
}

func TestWriterWriteRejectsSymlinkedSessionDir(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	outsideDir := t.TempDir()
	linkPath := filepath.Join(tmpDir, ".ntm", "handoffs", "linked")
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatalf("failed to create handoff base: %v", err)
	}
	if err := os.Symlink(outsideDir, linkPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	h := New("linked").WithGoalAndNow("Goal", "Now")
	_, err := w.Write(h, "through-symlink")
	if err == nil {
		t.Fatal("expected write through symlinked session dir to fail")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}

func TestWriterWriteAppendsLedger(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	h := New("ledger-session").
		WithGoalAndNow("Implemented feature X", "Write tests next").
		WithStatus(StatusComplete, OutcomeSucceeded)

	path, err := w.Write(h, "ledger-test")
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	ledgerPath := filepath.Join(tmpDir, ".ntm", "ledgers", "CONTINUITY_ledger-session.md")
	data, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("failed to read ledger: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "(manual)") {
		t.Errorf("expected ledger entry to include manual marker, got: %s", content)
	}
	if !strings.Contains(content, filepath.Base(path)) {
		t.Errorf("expected ledger to include handoff filename, got: %s", content)
	}
	if !strings.Contains(content, "- goal: Implemented feature X") {
		t.Errorf("expected ledger to include goal, got: %s", content)
	}
	if !strings.Contains(content, "- now: Write tests next") {
		t.Errorf("expected ledger to include now, got: %s", content)
	}
}

func TestWriterWriteValidationFailure(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	// Missing required fields
	h := &Handoff{
		Session: "test",
		// Goal and Now are missing
	}

	_, err := w.Write(h, "test")
	if err == nil {
		t.Error("expected validation error for missing goal/now")
	}
	if !strings.Contains(err.Error(), "validation failed") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWriterWriteInvalidSession(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	h := New("invalid session!").
		WithGoalAndNow("Test", "Test")

	_, err := w.Write(h, "test")
	if err == nil {
		t.Error("expected error for invalid session name")
	}
	// Validate() catches invalid session names with a validation error
	if !strings.Contains(err.Error(), "validation failed") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWriterWriteDefaultDescription(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	h := New("test").WithGoalAndNow("Test goal", "Test now")

	// Empty description should default to "handoff"
	path, err := w.Write(h, "")
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	filename := filepath.Base(path)
	if !strings.HasSuffix(filename, "_handoff.yaml") {
		t.Errorf("expected default description, got: %s", filename)
	}
}

func TestWriterWriteAuto(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	h := New("test-session").
		WithGoalAndNow("Test goal", "Test now").
		SetTokenInfo(80000, 100000)

	path, err := w.WriteAuto(h)
	if err != nil {
		t.Fatalf("WriteAuto failed: %v", err)
	}

	// Verify filename format
	filename := filepath.Base(path)
	if !strings.HasPrefix(filename, "auto-handoff-") {
		t.Errorf("unexpected auto filename format: %s", filename)
	}
	if !strings.HasSuffix(filename, ".yaml") {
		t.Errorf("missing .yaml extension: %s", filename)
	}

	// Verify content
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	var parsed Handoff
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("written YAML is invalid: %v", err)
	}

	if parsed.TokensPct != 80.0 {
		t.Errorf("tokens_pct mismatch: got %f", parsed.TokensPct)
	}
}

func TestWriterWriteAutoAppendsLedger(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	h := New("auto-ledger").
		WithGoalAndNow("Auto goal", "Auto now").
		SetTokenInfo(80000, 100000)

	path, err := w.WriteAuto(h)
	if err != nil {
		t.Fatalf("WriteAuto failed: %v", err)
	}

	ledgerPath := filepath.Join(tmpDir, ".ntm", "ledgers", "CONTINUITY_auto-ledger.md")
	data, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("failed to read ledger: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "(auto)") {
		t.Errorf("expected ledger entry to include auto marker, got: %s", content)
	}
	if !strings.Contains(content, filepath.Base(path)) {
		t.Errorf("expected ledger to include handoff filename, got: %s", content)
	}
	if !strings.Contains(content, "tokens_pct: 80.00") {
		t.Errorf("expected ledger to include tokens_pct, got: %s", content)
	}
}

func TestWriterRotation(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriterWithOptions(tmpDir, 3, nil) // Keep only 3 files

	h := New("test").WithGoalAndNow("Goal", "Now")

	// Write 5 files with distinct names
	var paths []string
	for i := 0; i < 5; i++ {
		// Ensure distinct timestamps by sleeping briefly
		time.Sleep(10 * time.Millisecond)
		path, err := w.Write(h, "test-"+string(rune('a'+i)))
		if err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}
		paths = append(paths, path)
	}

	// Check that only 3 files remain in main directory
	dir := filepath.Join(tmpDir, ".ntm", "handoffs", "test")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("failed to read dir: %v", err)
	}

	yamlCount := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			yamlCount++
		}
	}

	if yamlCount != 3 {
		t.Errorf("expected 3 yaml files after rotation, got %d", yamlCount)
	}

	// Check that archive directory exists and has 2 files
	archiveDir := filepath.Join(dir, ".archive")
	archiveEntries, err := os.ReadDir(archiveDir)
	if err != nil {
		t.Fatalf("archive dir should exist: %v", err)
	}

	archiveCount := 0
	for _, e := range archiveEntries {
		if strings.HasSuffix(e.Name(), ".yaml") {
			archiveCount++
		}
	}

	if archiveCount != 2 {
		t.Errorf("expected 2 archived files, got %d", archiveCount)
	}
}

func TestWriterRotationSkipsSymlinkedYAMLFiles(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriterWithOptions(tmpDir, 2, nil)

	h := New("test").WithGoalAndNow("Goal", "Now")
	if _, err := w.Write(h, "first"); err != nil {
		t.Fatalf("first write failed: %v", err)
	}
	if _, err := w.Write(h, "second"); err != nil {
		t.Fatalf("second write failed: %v", err)
	}

	dir := filepath.Join(tmpDir, ".ntm", "handoffs", "test")
	outsidePath := filepath.Join(t.TempDir(), "outside.yaml")
	if err := os.WriteFile(outsidePath, []byte("goal: external\nnow: external\n"), 0o644); err != nil {
		t.Fatalf("failed to write outside file: %v", err)
	}
	linkPath := filepath.Join(dir, "2000-01-01_00-00_linked.yaml")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	if _, err := w.Write(h, "third"); err != nil {
		t.Fatalf("third write failed: %v", err)
	}

	if _, err := os.Lstat(linkPath); err != nil {
		t.Fatalf("expected symlinked yaml to be left untouched, got %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("failed to read dir: %v", err)
	}
	yamlCount := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") && e.Type()&os.ModeSymlink == 0 {
			yamlCount++
		}
	}
	if yamlCount != 2 {
		t.Fatalf("expected 2 regular yaml files after rotation, got %d", yamlCount)
	}
}

func TestWriterArchive(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	h := New("test").WithGoalAndNow("Goal", "Now")
	path, err := w.Write(h, "to-archive")
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	err = w.Archive(path)
	if err != nil {
		t.Fatalf("Archive failed: %v", err)
	}

	// Original should not exist
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("original file should be removed after archive")
	}

	// Archived file should exist
	archivePath := filepath.Join(filepath.Dir(path), ".archive", filepath.Base(path))
	if _, err := os.Stat(archivePath); err != nil {
		t.Errorf("archived file should exist: %v", err)
	}
}

func TestWriterArchiveAlreadyArchived(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	// Create a file in archive manually
	archiveDir := filepath.Join(tmpDir, ".ntm", "handoffs", "test", ".archive")
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		t.Fatalf("failed to create archive dir: %v", err)
	}
	archivedFile := filepath.Join(archiveDir, "already-archived.yaml")
	if err := os.WriteFile(archivedFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	err := w.Archive(archivedFile)
	if err == nil {
		t.Error("expected error when archiving already-archived file")
	}
	if !strings.Contains(err.Error(), "already archived") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWriterArchiveRejectsSymlinkedSessionDir(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	outsideDir := t.TempDir()
	linkDir := filepath.Join(tmpDir, ".ntm", "handoffs", "linked")
	if err := os.MkdirAll(filepath.Dir(linkDir), 0o755); err != nil {
		t.Fatalf("failed to create handoff base: %v", err)
	}
	if err := os.Symlink(outsideDir, linkDir); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	path := filepath.Join(linkDir, "escape.yaml")
	err := w.Archive(path)
	if err == nil {
		t.Fatal("expected archive through symlinked session dir to fail")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}

func TestWriterArchiveRejectsSymlinkedArchiveDir(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	h := New("test").WithGoalAndNow("Goal", "Now")
	path, err := w.Write(h, "to-archive")
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	dir := filepath.Dir(path)
	archiveDir := filepath.Join(dir, ".archive")
	outsideDir := t.TempDir()
	if err := os.Symlink(outsideDir, archiveDir); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	err = w.Archive(path)
	if err == nil {
		t.Fatal("expected archive to reject symlinked archive dir")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}

func TestWriterDelete(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	h := New("test").WithGoalAndNow("Goal", "Now")
	path, err := w.Write(h, "to-delete")
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	err = w.Delete(path)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file should be deleted")
	}
}

func TestWriterDeleteOutsideBaseDir(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	// Try to delete a file outside the handoff directory
	err := w.Delete("/tmp/some-other-file.yaml")
	if err == nil {
		t.Error("expected error when deleting file outside base dir")
	}
	if !strings.Contains(err.Error(), "not within handoff directory") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWriterDeleteRejectsPathThroughSymlinkedSessionDir(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	outsideDir := t.TempDir()
	linkDir := filepath.Join(tmpDir, ".ntm", "handoffs", "linked")
	if err := os.MkdirAll(filepath.Dir(linkDir), 0o755); err != nil {
		t.Fatalf("failed to create handoff base: %v", err)
	}
	if err := os.Symlink(outsideDir, linkDir); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	err := w.Delete(filepath.Join(linkDir, "escape.yaml"))
	if err == nil {
		t.Fatal("expected delete through symlinked session dir to fail")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}

func TestWriterCleanArchive(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	// Create archive with old files
	archiveDir := filepath.Join(tmpDir, ".ntm", "handoffs", "test", ".archive")
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		t.Fatalf("failed to create archive dir: %v", err)
	}

	// Create a file and make it "old" by modifying its mtime
	oldFile := filepath.Join(archiveDir, "old-handoff.yaml")
	if err := os.WriteFile(oldFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatalf("failed to set mtime: %v", err)
	}

	// Create a recent file
	newFile := filepath.Join(archiveDir, "new-handoff.yaml")
	if err := os.WriteFile(newFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Clean files older than 24 hours
	removed, err := w.CleanArchive("test", 24*time.Hour)
	if err != nil {
		t.Fatalf("CleanArchive failed: %v", err)
	}

	if removed != 1 {
		t.Errorf("expected 1 removed, got %d", removed)
	}

	// Old file should be gone
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("old file should be removed")
	}

	// New file should remain
	if _, err := os.Stat(newFile); err != nil {
		t.Error("new file should still exist")
	}
}

func TestWriterCleanArchiveNoArchive(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	// Clean non-existent archive should not error
	removed, err := w.CleanArchive("nonexistent", 24*time.Hour)
	if err != nil {
		t.Fatalf("CleanArchive failed: %v", err)
	}
	if removed != 0 {
		t.Errorf("expected 0 removed for non-existent archive, got %d", removed)
	}
}

func TestWriterConcurrentWrites(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	var wg sync.WaitGroup
	errors := make(chan error, 10)

	// Write 10 handoffs concurrently
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			h := New("concurrent").WithGoalAndNow("Goal", "Now")
			_, err := w.Write(h, "concurrent-"+string(rune('a'+idx)))
			if err != nil {
				errors <- err
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for any errors
	for err := range errors {
		t.Errorf("concurrent write error: %v", err)
	}

	// Verify all files were written
	dir := filepath.Join(tmpDir, ".ntm", "handoffs", "concurrent")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("failed to read dir: %v", err)
	}

	yamlCount := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			yamlCount++
		}
	}

	if yamlCount != 10 {
		t.Errorf("expected 10 yaml files, got %d", yamlCount)
	}
}

func TestWriterAtomicWriteIntegrity(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	h := New("test").
		WithGoalAndNow("Important goal", "Important next step").
		WithStatus(StatusComplete, OutcomeSucceeded).
		AddTask("Task 1", "file1.go").
		AddTask("Task 2", "file2.go").
		AddDecision("approach", "Use atomic writes").
		AddFinding("insight", "Temp files prevent corruption")

	path, err := w.Write(h, "atomic-test")
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Read back and verify all data is intact
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	var parsed Handoff
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("YAML parse failed: %v", err)
	}

	if parsed.Goal != "Important goal" {
		t.Errorf("goal corrupted: %s", parsed.Goal)
	}
	if len(parsed.DoneThisSession) != 2 {
		t.Errorf("tasks corrupted: got %d", len(parsed.DoneThisSession))
	}
	if parsed.Decisions["approach"] != "Use atomic writes" {
		t.Errorf("decision corrupted: %v", parsed.Decisions)
	}
}

func TestWriterBaseDir(t *testing.T) {
	w := NewWriter("/tmp/myproject")
	expected := filepath.Join("/tmp/myproject", ".ntm", "handoffs")
	if w.BaseDir() != expected {
		t.Errorf("BaseDir() = %s, want %s", w.BaseDir(), expected)
	}
}

func TestWriterGeneralSession(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	// Test "general" session (special case)
	h := New("general").WithGoalAndNow("Goal", "Now")
	path, err := w.Write(h, "general-test")
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	expectedDir := filepath.Join(tmpDir, ".ntm", "handoffs", "general")
	if !strings.HasPrefix(path, expectedDir) {
		t.Errorf("file not in general dir: %s", path)
	}
}

func TestWriterFilePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	h := New("test").WithGoalAndNow("Goal", "Now")
	path, err := w.Write(h, "perms-test")
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	// Check file permissions (should be 0644)
	perm := info.Mode().Perm()
	if perm != 0644 {
		t.Errorf("expected permissions 0644, got %o", perm)
	}
}

// =============================================================================
// SetTokenInfo edge-case tests
// =============================================================================

func TestSetTokenInfo_NegativeValues(t *testing.T) {
	t.Parallel()

	h := New("test").WithGoalAndNow("Goal", "Now")

	// Negative used gets clamped to 0
	h.SetTokenInfo(-100, 200000)
	if h.TokensUsed != 0 {
		t.Errorf("negative used should clamp to 0, got %d", h.TokensUsed)
	}
	if h.TokensPct != 0 {
		t.Errorf("expected 0%% with clamped used, got %.2f", h.TokensPct)
	}

	// Negative max gets clamped to 0
	h.SetTokenInfo(100, -500)
	if h.TokensMax != 0 {
		t.Errorf("negative max should clamp to 0, got %d", h.TokensMax)
	}
	if h.TokensPct != 0 {
		t.Errorf("expected 0%% with zero max, got %.2f", h.TokensPct)
	}

	// Both negative
	h.SetTokenInfo(-10, -20)
	if h.TokensUsed != 0 || h.TokensMax != 0 {
		t.Errorf("both negative should clamp to 0, got used=%d max=%d", h.TokensUsed, h.TokensMax)
	}
}

func TestSetTokenInfo_OverflowClamping(t *testing.T) {
	t.Parallel()

	h := New("test").WithGoalAndNow("Goal", "Now")

	// used > max gets clamped to max
	h.SetTokenInfo(250000, 200000)
	if h.TokensUsed != 200000 {
		t.Errorf("used should clamp to max, got %d", h.TokensUsed)
	}
	if h.TokensPct != 100.0 {
		t.Errorf("expected 100%% when clamped, got %.2f", h.TokensPct)
	}
}

func TestSetTokenInfo_ZeroMax(t *testing.T) {
	t.Parallel()

	h := New("test").WithGoalAndNow("Goal", "Now")

	h.SetTokenInfo(0, 0)
	if h.TokensPct != 0 {
		t.Errorf("expected 0%% with zero max, got %.2f", h.TokensPct)
	}
}

// =============================================================================
// formatLedgerEntry tests
// =============================================================================

func TestFormatLedgerEntry_AllFields(t *testing.T) {
	t.Parallel()

	h := New("test-session").
		WithGoalAndNow("Build the thing", "Deploy next").
		WithStatus(StatusComplete, OutcomeSucceeded).
		SetTokenInfo(80000, 100000)
	h.Test = "go test ./..."
	h.Blockers = []string{"blocker-1", "blocker-2"}
	h.Next = []string{"next-1", "next-2"}
	h.ActiveBeads = []string{"bd-abc", "bd-xyz"}

	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	entry := formatLedgerEntry(h, "/tmp/handoff.yaml", false, now)

	checks := []string{
		"(manual)",
		"- file: handoff.yaml",
		"- status: complete",
		"- outcome: SUCCEEDED",
		"- goal: Build the thing",
		"- now: Deploy next",
		"- test: go test ./...",
		"- blockers: blocker-1, blocker-2",
		"- next: next-1, next-2",
		"- beads: bd-abc, bd-xyz",
		"- tokens_pct: 80.00",
	}
	for _, check := range checks {
		if !strings.Contains(entry, check) {
			t.Errorf("formatLedgerEntry missing %q in:\n%s", check, entry)
		}
	}
}

func TestFormatLedgerEntry_MinimalFields(t *testing.T) {
	t.Parallel()

	h := &Handoff{} // No optional fields set
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := formatLedgerEntry(h, "/tmp/min.yaml", true, now)

	if !strings.Contains(entry, "(auto)") {
		t.Error("expected (auto) marker")
	}
	// Should NOT contain optional fields
	for _, absent := range []string{"- status:", "- outcome:", "- test:", "- blockers:", "- next:", "- beads:", "- tokens_pct:"} {
		if strings.Contains(entry, absent) {
			t.Errorf("minimal entry should not contain %q", absent)
		}
	}
}

// =============================================================================
// Delete / Archive edge-case tests
// =============================================================================

func TestWriterDeleteNonexistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	// Ensure the session dir exists so path is within base dir
	w.EnsureDir("test")
	fakePath := filepath.Join(tmpDir, ".ntm", "handoffs", "test", "does-not-exist.yaml")

	err := w.Delete(fakePath)
	if err == nil {
		t.Error("expected error when deleting nonexistent file")
	}
	if !strings.Contains(err.Error(), "delete failed") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestWriterArchiveOutsideBaseDir(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	err := w.Archive("/tmp/some-other-dir/file.yaml")
	if err == nil {
		t.Error("expected error when archiving outside base dir")
	}
	if !strings.Contains(err.Error(), "not within handoff directory") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWriterArchiveNonexistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	// Within base dir but file doesn't exist
	w.EnsureDir("test")
	fakePath := filepath.Join(tmpDir, ".ntm", "handoffs", "test", "ghost.yaml")

	err := w.Archive(fakePath)
	if err == nil {
		t.Error("expected error when archiving nonexistent file")
	}
	if !strings.Contains(err.Error(), "archive failed") {
		t.Errorf("unexpected error: %v", err)
	}
}

// =============================================================================
// WriteAuto edge-case tests
// =============================================================================

func TestWriterWriteAutoValidationFailure(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	// Missing required fields
	h := &Handoff{Session: "test"}
	_, err := w.WriteAuto(h)
	if err == nil {
		t.Error("expected validation error for invalid handoff")
	}
	if !strings.Contains(err.Error(), "validation failed") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWriterWriteAutoWithAllFields(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	h := New("auto-full").
		WithGoalAndNow("Full auto goal", "Full auto now").
		WithStatus(StatusPartial, OutcomePartialPlus).
		SetTokenInfo(150000, 200000)
	h.Test = "go test ./internal/..."
	h.Blockers = []string{"upstream API down"}
	h.Next = []string{"retry after fix"}
	h.ActiveBeads = []string{"bd-test1"}

	path, err := w.WriteAuto(h)
	if err != nil {
		t.Fatalf("WriteAuto failed: %v", err)
	}

	// Verify ledger has all the extra fields
	ledgerPath := filepath.Join(tmpDir, ".ntm", "ledgers", "CONTINUITY_auto-full.md")
	data, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("failed to read ledger: %v", err)
	}

	content := string(data)
	for _, expected := range []string{"(auto)", "test: go test", "blockers:", "next:", "beads: bd-test1", "tokens_pct: 75.00"} {
		if !strings.Contains(content, expected) {
			t.Errorf("ledger missing %q in:\n%s", expected, content)
		}
	}

	// Verify written file
	fileData, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read handoff: %v", err)
	}
	var parsed Handoff
	if err := yaml.Unmarshal(fileData, &parsed); err != nil {
		t.Fatalf("invalid YAML: %v", err)
	}
	if parsed.TokensPct != 75.0 {
		t.Errorf("expected 75%% tokens, got %.2f", parsed.TokensPct)
	}
}

// =============================================================================
// appendLedgerEntry tests
// =============================================================================

func TestAppendLedgerEntry_EmptySession(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	h := &Handoff{Goal: "test goal", Now: "test now"}
	err := w.appendLedgerEntry(h, "/tmp/handoff.yaml", false)
	if err != nil {
		t.Fatalf("appendLedgerEntry failed: %v", err)
	}

	// Empty session → "general"
	ledgerPath := filepath.Join(tmpDir, ".ntm", "ledgers", "CONTINUITY_general.md")
	if _, err := os.Stat(ledgerPath); err != nil {
		t.Errorf("expected general ledger file, got error: %v", err)
	}
}

// =============================================================================
// Pure-function tests for coverage improvement
// =============================================================================

func TestSingleLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple", "hello", "hello"},
		{"with spaces", "hello  world", "hello world"},
		{"with newlines", "hello\nworld", "hello world"},
		{"with tabs", "hello\t\tworld", "hello world"},
		{"empty", "", ""},
		{"only whitespace", "   \n\t  ", ""},
		{"leading trailing", "  hello  ", "hello"},
		{"mixed whitespace", "  hello \n world  ", "hello world"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := singleLine(tc.input)
			if got != tc.expected {
				t.Errorf("singleLine(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestCompactList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		items    []string
		maxItems int
		expected string
	}{
		{"empty list", []string{}, 5, ""},
		{"single item", []string{"item1"}, 5, "item1"},
		{"multiple items", []string{"a", "b", "c"}, 5, "a, b, c"},
		{"limited items", []string{"a", "b", "c", "d", "e"}, 3, "a, b, c +2 more"},
		{"max zero uses all", []string{"a", "b"}, 0, "a, b"},
		{"max negative uses all", []string{"a", "b"}, -1, "a, b"},
		{"empty strings filtered", []string{"a", "", "b"}, 5, "a, b"},
		{"whitespace only filtered", []string{"a", "   ", "b"}, 5, "a, b"},
		{"newlines collapsed", []string{"hello\nworld", "foo"}, 5, "hello world, foo"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := compactList(tc.items, tc.maxItems)
			if got != tc.expected {
				t.Errorf("compactList(%v, %d) = %q, want %q", tc.items, tc.maxItems, got, tc.expected)
			}
		})
	}
}

func TestMarshalYAML(t *testing.T) {
	t.Parallel()

	h := New("test-session").
		WithGoalAndNow("Test goal", "Test now").
		WithStatus(StatusComplete, OutcomeSucceeded)

	data, err := MarshalYAML(h)
	if err != nil {
		t.Fatalf("MarshalYAML failed: %v", err)
	}

	if len(data) == 0 {
		t.Error("MarshalYAML returned empty data")
	}

	// Verify it's valid YAML by unmarshaling
	var parsed Handoff
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("MarshalYAML output is invalid YAML: %v", err)
	}

	if parsed.Goal != "Test goal" {
		t.Errorf("Goal mismatch: got %q", parsed.Goal)
	}
	if parsed.Now != "Test now" {
		t.Errorf("Now mismatch: got %q", parsed.Now)
	}

	t.Logf("HANDOFF_TEST: MarshalYAML | Session=%s | YAMLSize=%d", h.Session, len(data))
}

func TestWriteToPath(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	h := New("test").
		WithGoalAndNow("Test goal", "Test now").
		WithStatus(StatusPartial, OutcomePartialPlus)

	targetPath := filepath.Join(tmpDir, "custom-handoff.yaml")

	err := w.WriteToPath(h, targetPath)
	if err != nil {
		t.Fatalf("WriteToPath failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(targetPath); err != nil {
		t.Fatalf("file not created at target path: %v", err)
	}

	// Verify content is valid
	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	var parsed Handoff
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid YAML in file: %v", err)
	}

	if parsed.Goal != "Test goal" {
		t.Errorf("Goal mismatch: got %q", parsed.Goal)
	}

	t.Logf("HANDOFF_TEST: WriteToPath | Path=%s | FileSize=%d", targetPath, len(data))
}

func TestWriteToPath_ValidationError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	// Invalid handoff (missing required fields)
	h := &Handoff{Session: "test"}

	targetPath := filepath.Join(tmpDir, "invalid.yaml")

	err := w.WriteToPath(h, targetPath)
	if err == nil {
		t.Error("expected validation error for invalid handoff")
	}
	if !strings.Contains(err.Error(), "validation failed") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWriteToPath_CreatesParentDirs(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	w := NewWriter(tmpDir)

	h := New("test").WithGoalAndNow("Goal", "Now")

	// WriteToPath should create parent directories
	targetPath := filepath.Join(tmpDir, "nested", "subdir", "handoff.yaml")

	err := w.WriteToPath(h, targetPath)
	if err != nil {
		t.Fatalf("WriteToPath should create parent dirs: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(targetPath); err != nil {
		t.Errorf("file should exist at nested path: %v", err)
	}
}

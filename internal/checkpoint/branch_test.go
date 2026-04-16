package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
)

// =============================================================================
// shortHash — len <= 8 branch (restore.go:379)
// =============================================================================

func TestShortHash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty string", "", ""},
		{"1 char", "a", "a"},
		{"exactly 8 chars", "abcdefgh", "abcdefgh"},
		{"9 chars truncates", "abcdefghi", "abcdefgh"},
		{"full SHA", "abc123def456789012345678901234567890", "abc123de"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := shortHash(tc.input)
			if got != tc.want {
				t.Errorf("shortHash(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// =============================================================================
// isPathWithinDirResolved — symlink and traversal branches (export.go:695)
// =============================================================================

func TestIsPathWithinDirResolved_TraversalAttack(t *testing.T) {
	t.Parallel()

	// Textual validation catches path traversal before symlink resolution.
	_, err := isPathWithinDirResolved("/base/dir", "../../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal, got nil")
	}
}

func TestIsPathWithinDirResolved_ValidPath(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	got, err := isPathWithinDirResolved(tmpDir, "sub/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// isPathWithinDirResolved calls filepath.EvalSymlinks which on macOS
	// resolves /tmp -> /private/tmp and /var -> /private/var. Evaluate
	// symlinks on the expected path the same way so the comparison is
	// canonical-vs-canonical on every platform.
	wantResolved, err := filepath.EvalSymlinks(subDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(subDir): %v", err)
	}
	want := filepath.Join(wantResolved, "file.txt")
	if got != want {
		t.Errorf("isPathWithinDirResolved = %q, want %q", got, want)
	}
}

func TestIsPathWithinDirResolved_SymlinkEscape(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outsideDir := t.TempDir()

	// Create a symlink inside tmpDir that points outside.
	symlinkPath := filepath.Join(tmpDir, "escape")
	if err := os.Symlink(outsideDir, symlinkPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	_, err := isPathWithinDirResolved(tmpDir, "escape/secret.txt")
	if err == nil {
		t.Error("expected error for symlink escape, got nil")
	}
}

func TestIsPathWithinDirResolved_NonexistentBase(t *testing.T) {
	t.Parallel()

	// When baseDir doesn't exist, EvalSymlinks fails and falls back to Clean.
	got, err := isPathWithinDirResolved("/nonexistent/base/dir", "sub/file.txt")
	if err != nil {
		t.Fatalf("unexpected error for nonexistent base: %v", err)
	}
	want := "/nonexistent/base/dir/sub/file.txt"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// =============================================================================
// gzipDecompress — error branch for invalid data (scrollback.go:138)
// =============================================================================

func TestGzipDecompress_InvalidData(t *testing.T) {
	t.Parallel()

	_, err := gzipDecompress([]byte("not gzip data at all"))
	if err == nil {
		t.Error("expected error for invalid gzip data, got nil")
	}
}

// =============================================================================
// redactSecrets — nil config branch (export.go:654)
// =============================================================================

func TestRedactSecrets_NilConfig(t *testing.T) {
	// When no config is set, redactSecrets should use defaults.
	SetRedactionConfig(nil)
	t.Cleanup(func() { SetRedactionConfig(nil) })

	// Normal text should pass through.
	input := "Hello, this is normal text without secrets"
	got := string(redactSecrets([]byte(input)))
	if got != input {
		t.Errorf("redactSecrets changed normal text: %q → %q", input, got)
	}
}

// =============================================================================
// sanitizeName — edge cases (storage.go:92)
// =============================================================================

func TestSanitizeName_Empty(t *testing.T) {
	t.Parallel()

	got := sanitizeName("")
	if got != "" {
		t.Errorf("sanitizeName(\"\") = %q, want empty", got)
	}
}

func TestSanitizeName_ExactlyAtLimit(t *testing.T) {
	t.Parallel()

	// A string of exactly 50 bytes should not be truncated.
	input := "aaaaaaaaaabbbbbbbbbbccccccccccddddddddddeeeeeeeeee" // 50 chars
	got := sanitizeName(input)
	if got != input {
		t.Errorf("sanitizeName 50-byte input = %q, want %q", got, input)
	}
}

func TestSanitizeName_AllUnsafeChars(t *testing.T) {
	t.Parallel()

	input := "a/b\\c:d*e?f\"g<h>i|j%k l"
	got := sanitizeName(input)
	// All unsafe chars replaced: / \ : * ? " < > | → -, % space → _
	want := "a-b-c-d-e-f-g-h-i-j_k_l"
	if got != want {
		t.Errorf("sanitizeName(%q) = %q, want %q", input, got, want)
	}
}

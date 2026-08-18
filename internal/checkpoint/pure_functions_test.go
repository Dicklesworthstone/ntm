package checkpoint

import (
	"strings"
	"testing"
	"time"
)

// =============================================================================
// splitLines — 0% → 100%
// =============================================================================

func TestSplitLines_Basic(t *testing.T) {
	t.Parallel()
	lines := splitLines("a\nb\nc")
	if len(lines) != 3 {
		t.Fatalf("len = %d, want 3", len(lines))
	}
	if lines[0] != "a" || lines[1] != "b" || lines[2] != "c" {
		t.Errorf("got %v", lines)
	}
}

func TestSplitLines_Empty(t *testing.T) {
	t.Parallel()
	if lines := splitLines(""); lines != nil {
		t.Errorf("got %v, want nil", lines)
	}
}

func TestSplitLines_SingleLine(t *testing.T) {
	t.Parallel()
	lines := splitLines("hello")
	if len(lines) != 1 || lines[0] != "hello" {
		t.Errorf("got %v", lines)
	}
}

func TestSplitLines_TrailingNewline(t *testing.T) {
	t.Parallel()
	lines := splitLines("a\nb\n")
	// Trailing newline produces no extra element since start < len(s) is false
	if len(lines) != 2 {
		t.Errorf("len = %d, want 2", len(lines))
	}
}

// =============================================================================
// joinLines — 0% → 100%
// =============================================================================

func TestJoinLines_Basic(t *testing.T) {
	t.Parallel()
	result := joinLines([]string{"a", "b", "c"})
	if result != "a\nb\nc" {
		t.Errorf("got %q", result)
	}
}

func TestJoinLines_Empty(t *testing.T) {
	t.Parallel()
	if result := joinLines(nil); result != "" {
		t.Errorf("got %q, want empty", result)
	}
}

func TestJoinLines_Single(t *testing.T) {
	t.Parallel()
	if result := joinLines([]string{"only"}); result != "only" {
		t.Errorf("got %q", result)
	}
}

// =============================================================================
// truncateToLines — 0% → 100%
// =============================================================================

func TestTruncateToLines_FitsWithinLimit(t *testing.T) {
	t.Parallel()
	content := "a\nb\nc"
	result := truncateToLines(content, 5)
	if result != content {
		t.Errorf("got %q, want %q", result, content)
	}
}

func TestTruncateToLines_Truncates(t *testing.T) {
	t.Parallel()
	content := "line1\nline2\nline3\nline4\nline5"
	result := truncateToLines(content, 2)
	if result != "line4\nline5" {
		t.Errorf("got %q", result)
	}
}

func TestTruncateToLines_ExactMatch(t *testing.T) {
	t.Parallel()
	content := "a\nb\nc"
	result := truncateToLines(content, 3)
	if result != content {
		t.Errorf("got %q, want original content", result)
	}
}

// =============================================================================
// trimSpace — 0% → 100%
// =============================================================================

func TestTrimSpace_Basic(t *testing.T) {
	t.Parallel()
	if result := trimSpace("  hello  "); result != "hello" {
		t.Errorf("got %q", result)
	}
}

func TestTrimSpace_Tabs(t *testing.T) {
	t.Parallel()
	if result := trimSpace("\t\nhello\r\n"); result != "hello" {
		t.Errorf("got %q", result)
	}
}

func TestTrimSpace_NoWhitespace(t *testing.T) {
	t.Parallel()
	if result := trimSpace("clean"); result != "clean" {
		t.Errorf("got %q", result)
	}
}

func TestTrimSpace_AllWhitespace(t *testing.T) {
	t.Parallel()
	if result := trimSpace("   \t\n "); result != "" {
		t.Errorf("got %q, want empty", result)
	}
}

func TestTrimSpace_Empty(t *testing.T) {
	t.Parallel()
	if result := trimSpace(""); result != "" {
		t.Errorf("got %q", result)
	}
}

// =============================================================================
// shortHash — 0% → 100%
// =============================================================================

func TestShortHash_Long(t *testing.T) {
	t.Parallel()
	if result := shortHash("abc123def456"); result != "abc123de" {
		t.Errorf("got %q", result)
	}
}

func TestShortHash_ExactlyEight(t *testing.T) {
	t.Parallel()
	if result := shortHash("12345678"); result != "12345678" {
		t.Errorf("got %q", result)
	}
}

func TestShortHash_Short(t *testing.T) {
	t.Parallel()
	if result := shortHash("abc"); result != "abc" {
		t.Errorf("got %q", result)
	}
}

func TestShortHash_Empty(t *testing.T) {
	t.Parallel()
	if result := shortHash(""); result != "" {
		t.Errorf("got %q", result)
	}
}

// =============================================================================
// formatDuration — 0% → 100%
// =============================================================================

func TestFormatDuration_JustNow(t *testing.T) {
	t.Parallel()
	if result := formatDuration(30 * time.Second); result != "just now" {
		t.Errorf("got %q", result)
	}
}

func TestFormatDuration_Minutes(t *testing.T) {
	t.Parallel()
	if result := formatDuration(5 * time.Minute); result != "5m" {
		t.Errorf("got %q", result)
	}
}

func TestFormatDuration_Hours(t *testing.T) {
	t.Parallel()
	if result := formatDuration(3 * time.Hour); result != "3h" {
		t.Errorf("got %q", result)
	}
}

func TestFormatDuration_Days(t *testing.T) {
	t.Parallel()
	if result := formatDuration(48 * time.Hour); result != "2d" {
		t.Errorf("got %q", result)
	}
}

// =============================================================================
// formatContextInjection — 0% → 100%
// =============================================================================

func TestFormatContextInjection_HeaderAndContent(t *testing.T) {
	t.Parallel()
	// Use a checkpoint time far enough in the past for a stable duration
	checkpointTime := time.Now().Add(-2 * time.Hour)
	result := formatContextInjection("scrollback content", checkpointTime)
	if !strings.HasPrefix(result, "# Context from checkpoint (") {
		t.Errorf("missing header prefix, got %q", result)
	}
	if !strings.HasSuffix(result, "scrollback content") {
		t.Errorf("missing content suffix, got %q", result)
	}
	if !strings.Contains(result, "2h") {
		t.Errorf("expected 2h duration in output, got %q", result)
	}
}

// =============================================================================
// parseGitStatus — 0% → 100%
// =============================================================================

func TestParseGitStatus_Mixed(t *testing.T) {
	t.Parallel()
	// git status --porcelain format: XY filename
	// X=index status, Y=worktree status
	status := "M  staged.go\n" + // staged (M in index, space in worktree)
		" M unstaged.go\n" + // unstaged (space in index, M in worktree)
		"?? newfile.go\n" + // untracked
		"MM both.go\n" // both staged and unstaged
	staged, unstaged, untracked := parseGitStatus(status)
	if staged != 2 { // M_ and MM
		t.Errorf("staged = %d, want 2", staged)
	}
	if unstaged != 2 { // _M and MM
		t.Errorf("unstaged = %d, want 2", unstaged)
	}
	if untracked != 1 {
		t.Errorf("untracked = %d, want 1", untracked)
	}
}

func TestParseGitStatus_Empty(t *testing.T) {
	t.Parallel()
	staged, unstaged, untracked := parseGitStatus("")
	if staged != 0 || unstaged != 0 || untracked != 0 {
		t.Errorf("expected all zeros for empty status, got %d %d %d", staged, unstaged, untracked)
	}
}

func TestParseGitStatus_OnlyUntracked(t *testing.T) {
	t.Parallel()
	status := "?? file1.go\n?? file2.go\n"
	staged, unstaged, untracked := parseGitStatus(status)
	if staged != 0 || unstaged != 0 {
		t.Errorf("staged=%d unstaged=%d, want 0 0", staged, unstaged)
	}
	if untracked != 2 {
		t.Errorf("untracked = %d, want 2", untracked)
	}
}

func TestParseGitStatus_OnlyStaged(t *testing.T) {
	t.Parallel()
	status := "A  new.go\nM  modified.go\n"
	staged, unstaged, untracked := parseGitStatus(status)
	if staged != 2 {
		t.Errorf("staged = %d, want 2", staged)
	}
	if unstaged != 0 || untracked != 0 {
		t.Errorf("unstaged=%d untracked=%d, want 0 0", unstaged, untracked)
	}
}

// =============================================================================
// countLines — 0% → 100%
// =============================================================================

func TestCountLines_Basic(t *testing.T) {
	t.Parallel()
	if n := countLines("a\nb\nc"); n != 3 {
		t.Errorf("got %d, want 3", n)
	}
}

func TestCountLines_Empty(t *testing.T) {
	t.Parallel()
	if n := countLines(""); n != 0 {
		t.Errorf("got %d, want 0", n)
	}
}

func TestCountLines_TrailingNewline(t *testing.T) {
	t.Parallel()
	if n := countLines("a\nb\n"); n != 2 {
		t.Errorf("got %d, want 2", n)
	}
}

func TestCountLines_JustNewline(t *testing.T) {
	t.Parallel()
	if n := countLines("\n"); n != 0 {
		t.Errorf("got %d, want 0", n)
	}
}

func TestCountLines_SingleLine(t *testing.T) {
	t.Parallel()
	if n := countLines("hello"); n != 1 {
		t.Errorf("got %d, want 1", n)
	}
}

// =============================================================================
// matchWildcard — 0% → 100%
// =============================================================================

func TestMatchWildcard_ExactMatch(t *testing.T) {
	t.Parallel()
	if !matchWildcard("hello", "hello") {
		t.Error("expected exact match")
	}
}

func TestMatchWildcard_CaseInsensitive(t *testing.T) {
	t.Parallel()
	if !matchWildcard("Hello", "hello") {
		t.Error("expected case-insensitive match")
	}
}

func TestMatchWildcard_StarPrefix(t *testing.T) {
	t.Parallel()
	if !matchWildcard("my-checkpoint", "*checkpoint") {
		t.Error("expected *checkpoint to match")
	}
}

func TestMatchWildcard_StarSuffix(t *testing.T) {
	t.Parallel()
	if !matchWildcard("checkpoint-v2", "checkpoint*") {
		t.Error("expected checkpoint* to match")
	}
}

func TestMatchWildcard_StarMiddle(t *testing.T) {
	t.Parallel()
	if !matchWildcard("pre-mid-post", "pre*post") {
		t.Error("expected pre*post to match")
	}
}

func TestMatchWildcard_NoMatch(t *testing.T) {
	t.Parallel()
	if matchWildcard("other", "checkpoint*") {
		t.Error("should not match")
	}
}

// =============================================================================
// gzipCompress / gzipDecompress — 0% → 100%
// =============================================================================

func TestGzipRoundtrip(t *testing.T) {
	t.Parallel()
	original := []byte("hello world, this is a test of gzip compression in checkpoint scrollback")

	compressed, err := gzipCompress(original)
	if err != nil {
		t.Fatalf("gzipCompress: %v", err)
	}

	if len(compressed) == 0 {
		t.Fatal("compressed data is empty")
	}

	decompressed, err := gzipDecompress(compressed)
	if err != nil {
		t.Fatalf("gzipDecompress: %v", err)
	}

	if string(decompressed) != string(original) {
		t.Errorf("roundtrip failed: got %q", decompressed)
	}
}

func TestGzipCompress_Empty(t *testing.T) {
	t.Parallel()
	compressed, err := gzipCompress([]byte{})
	if err != nil {
		t.Fatalf("gzipCompress: %v", err)
	}
	decompressed, err := gzipDecompress(compressed)
	if err != nil {
		t.Fatalf("gzipDecompress: %v", err)
	}
	if len(decompressed) != 0 {
		t.Errorf("expected empty, got %d bytes", len(decompressed))
	}
}

func TestGzipDecompress_BadInput(t *testing.T) {
	t.Parallel()
	_, err := gzipDecompress([]byte("not gzip data"))
	if err == nil {
		t.Error("expected error for invalid gzip data")
	}
}

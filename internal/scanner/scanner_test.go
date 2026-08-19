package scanner

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
)

func TestIsAvailable(t *testing.T) {
	// This test checks if UBS is installed on the system
	available := IsAvailable()
	t.Logf("UBS available: %v", available)
	// We don't fail if UBS is not installed - it's optional
}

func TestNew(t *testing.T) {
	scanner, err := New()
	if err != nil {
		if err == ErrNotInstalled {
			t.Skip("UBS not installed, skipping")
		}
		t.Fatalf("unexpected error: %v", err)
	}
	if scanner == nil {
		t.Fatal("scanner is nil")
	}
	if scanner.binaryPath == "" {
		t.Fatal("binaryPath is empty")
	}
}

func TestVersion(t *testing.T) {
	scanner, err := New()
	if err != nil {
		t.Skip("UBS not installed")
	}

	version, err := scanner.Version()
	if err != nil {
		t.Fatalf("getting version: %v", err)
	}
	if version == "" {
		t.Fatal("version is empty")
	}
	t.Logf("UBS version: %s", version)
}

func TestScanFile(t *testing.T) {
	scanner, err := New()
	if err != nil {
		t.Skip("UBS not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Scan a real file in the project
	result, err := scanner.ScanFile(ctx, "types.go")
	if err != nil {
		// Skip on timeout since UBS may be slow in CI (bd-1ihar)
		if err == ErrTimeout {
			t.Skipf("UBS scan timed out after 30s: %v", err)
		}
		t.Fatalf("scanning file: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}

	t.Logf("Scan result: files=%d, critical=%d, warning=%d, info=%d",
		result.Totals.Files, result.Totals.Critical, result.Totals.Warning, result.Totals.Info)
}

func TestScanDirectory(t *testing.T) {
	scanner, err := New()
	if err != nil {
		t.Skip("UBS not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Scan the scanner package itself
	result, err := scanner.ScanDirectory(ctx, ".")
	if err != nil {
		// UBS is an optional external process and can exceed its own 60s
		// deadline on a contended CI host.
		if errors.Is(err, ErrTimeout) {
			t.Skipf("UBS directory scan timed out after 60s: %v", err)
		}
		t.Fatalf("scanning directory: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}

	t.Logf("Directory scan: files=%d, critical=%d, warning=%d, info=%d",
		result.Totals.Files, result.Totals.Critical, result.Totals.Warning, result.Totals.Info)
}

func TestScanOptions(t *testing.T) {
	scanner, err := New()
	if err != nil {
		t.Skip("UBS not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opts := ScanOptions{
		Languages:     []string{"golang"},
		CI:            true,
		FailOnWarning: false,
		Timeout:       30 * time.Second,
	}

	result, err := scanner.Scan(ctx, ".", opts)
	if err != nil {
		// Skip on timeout since UBS may be slow in CI (bd-1ihar)
		if err == ErrTimeout {
			t.Skipf("UBS scan with options timed out after 30s: %v", err)
		}
		t.Fatalf("scan with options: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}

	t.Logf("Scan with options: files=%d, duration=%v",
		result.Totals.Files, result.Duration)
}

func TestScanResultMethods(t *testing.T) {
	result := &ScanResult{
		Totals: ScanTotals{
			Critical: 2,
			Warning:  5,
			Info:     10,
			Files:    3,
		},
		Findings: []Finding{
			{File: "a.go", Severity: SeverityCritical, Message: "critical 1"},
			{File: "a.go", Severity: SeverityCritical, Message: "critical 2"},
			{File: "b.go", Severity: SeverityWarning, Message: "warning 1"},
			{File: "b.go", Severity: SeverityInfo, Message: "info 1"},
		},
	}

	if result.IsHealthy() {
		t.Error("expected IsHealthy() to be false")
	}
	if !result.HasCritical() {
		t.Error("expected HasCritical() to be true")
	}
	if !result.HasWarning() {
		t.Error("expected HasWarning() to be true")
	}
	if result.TotalIssues() != 17 {
		t.Errorf("expected TotalIssues() = 17, got %d", result.TotalIssues())
	}

	criticals := result.FilterBySeverity(SeverityCritical)
	if len(criticals) != 2 {
		t.Errorf("expected 2 critical findings, got %d", len(criticals))
	}

	fileAFindings := result.FilterByFile("a.go")
	if len(fileAFindings) != 2 {
		t.Errorf("expected 2 findings for a.go, got %d", len(fileAFindings))
	}
}

func TestDefaultOptions(t *testing.T) {
	opts := DefaultOptions()
	if opts.Timeout != 60*time.Second {
		t.Errorf("expected timeout 60s, got %v", opts.Timeout)
	}
}

func TestBuildArgs(t *testing.T) {
	scanner := &Scanner{binaryPath: "ubs"}

	tests := []struct {
		name     string
		path     string
		opts     ScanOptions
		expected []string
	}{
		{
			name:     "default",
			path:     ".",
			opts:     ScanOptions{},
			expected: []string{"--format=json", "."},
		},
		{
			name: "with languages",
			path: "src/",
			opts: ScanOptions{
				Languages: []string{"golang", "rust"},
			},
			expected: []string{"--format=json", "--only=golang,rust", "src/"},
		},
		{
			name: "CI mode",
			path: ".",
			opts: ScanOptions{
				CI:            true,
				FailOnWarning: true,
			},
			expected: []string{"--format=json", "--ci", "--fail-on-warning", "."},
		},
		{
			name: "staged only",
			path: ".",
			opts: ScanOptions{
				StagedOnly: true,
			},
			expected: []string{"--format=json", "--staged", "."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := scanner.buildArgs(tt.path, tt.opts)
			if len(args) != len(tt.expected) {
				t.Errorf("expected %d args, got %d: %v", len(tt.expected), len(args), args)
				return
			}
			for i, arg := range args {
				if arg != tt.expected[i] {
					t.Errorf("arg[%d]: expected %q, got %q", i, tt.expected[i], arg)
				}
			}
		})
	}
}

func TestParseOutput_WithWarningsPrefix(t *testing.T) {
	scanner := &Scanner{binaryPath: "ubs"}
	jsonPayload := `{"project":"test","timestamp":"2026-01-01T00:00:00Z","scanners":[],"totals":{"critical":0,"warning":0,"info":0,"files":0},"findings":[],"exit_code":0}`
	output := []byte("ℹ Created filtered scan workspace at /tmp\n" + jsonPayload + "\n")

	result, warnings, err := scanner.parseOutput(output)
	if err != nil {
		t.Fatalf("parseOutput error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if result.Project != "test" {
		t.Fatalf("expected project=test, got %q", result.Project)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(warnings))
	}
	if warnings[0] != "ℹ Created filtered scan workspace at /tmp" {
		t.Fatalf("unexpected warning: %q", warnings[0])
	}
}

func TestParseOutput_WarningsOnly(t *testing.T) {
	scanner := &Scanner{binaryPath: "ubs"}
	output := []byte("✓ No changed files to scan.\n")

	result, warnings, err := scanner.parseOutput(output)
	if err == nil || !errors.Is(err, ErrOutputNotJSON) {
		t.Fatalf("expected ErrOutputNotJSON, got %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result when no JSON, got %+v", result)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(warnings))
	}
	if warnings[0] != "✓ No changed files to scan." {
		t.Fatalf("unexpected warning: %q", warnings[0])
	}
}

func TestCollectAssignmentMatches(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	session := "testproj"
	store := assignment.NewStore(session)
	if _, err := store.Assign("bd-1", "Fix internal/scanner", 1, "claude", "testproj_claude_1", "Work on internal/scanner"); err != nil {
		t.Fatalf("assign failed: %v", err)
	}

	findings := []Finding{
		{
			File:     "internal/scanner/scanner.go",
			Line:     10,
			Severity: SeverityWarning,
			Message:  "test warning",
			RuleID:   "rule-1",
		},
	}

	projectKey := filepath.Join(tmpDir, session)
	matches, err := collectAssignmentMatches(projectKey, findings)
	if err != nil {
		t.Fatalf("collectAssignmentMatches error: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("expected matches, got none")
	}

	items := matches["testproj_claude_1"]
	if len(items) != 1 {
		t.Fatalf("expected 1 match, got %d", len(items))
	}
	if items[0].Finding.File != findings[0].File {
		t.Fatalf("unexpected matched file: %s", items[0].Finding.File)
	}
}

func TestMatchAssignmentPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		file    string
		want    bool
	}{
		{
			name:    "double star deep match",
			pattern: "internal/**/*.go",
			file:    "internal/scanner/notify.go",
			want:    true,
		},
		{
			name:    "double star any depth",
			pattern: "**/*.go",
			file:    "internal/scanner/notify.go",
			want:    true,
		},
		{
			name:    "single star segment mismatch",
			pattern: "internal/*.go",
			file:    "internal/scanner/notify.go",
			want:    false,
		},
		{
			name:    "suffix under dir",
			pattern: "internal/**",
			file:    "internal/scanner/notify.go",
			want:    true,
		},
		{
			name:    "basename match",
			pattern: "notify.go",
			file:    "internal/scanner/notify.go",
			want:    true,
		},
		{
			name:    "prefix mismatch",
			pattern: "internal/**",
			file:    "cmd/ntm/main.go",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchAssignmentPattern(tt.pattern, tt.file)
			if got != tt.want {
				t.Fatalf("matchAssignmentPattern(%q, %q) = %v, want %v", tt.pattern, tt.file, got, tt.want)
			}
		})
	}
}

func TestSeverityMeetsThreshold(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		sev       Severity
		threshold Severity
		want      bool
	}{
		{"critical meets critical", SeverityCritical, SeverityCritical, true},
		{"critical meets warning", SeverityCritical, SeverityWarning, true},
		{"critical meets info", SeverityCritical, SeverityInfo, true},
		{"warning meets warning", SeverityWarning, SeverityWarning, true},
		{"warning meets info", SeverityWarning, SeverityInfo, true},
		{"warning does not meet critical", SeverityWarning, SeverityCritical, false},
		{"info meets info", SeverityInfo, SeverityInfo, true},
		{"info does not meet warning", SeverityInfo, SeverityWarning, false},
		{"info does not meet critical", SeverityInfo, SeverityCritical, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := SeverityMeetsThreshold(tc.sev, tc.threshold)
			if got != tc.want {
				t.Errorf("SeverityMeetsThreshold(%q, %q) = %v, want %v", tc.sev, tc.threshold, got, tc.want)
			}
		})
	}
}

func TestTruncateMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		msg    string
		maxLen int
		want   string
	}{
		{"empty string", "", 10, ""},
		{"within limit", "hello", 10, "hello"},
		{"exact limit", "hello", 5, "hello"},
		{"truncated", "hello world!", 8, "hello..."},
		{"max zero", "hello", 0, ""},
		{"max negative", "hello", -1, ""},
		{"max 1", "hello", 1, "."},
		{"max 2", "hello", 2, ".."},
		{"max 3", "hello", 3, "..."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := truncateMessage(tc.msg, tc.maxLen)
			if got != tc.want {
				t.Errorf("truncateMessage(%q, %d) = %q, want %q", tc.msg, tc.maxLen, got, tc.want)
			}
		})
	}
}

func TestShortenPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want string
	}{
		{"short path", "file.go", "file.go"},
		{"two components", "dir/file.go", "dir/file.go"},
		{"three components", "a/b/file.go", "b/file.go"},
		{"deep path", "/usr/local/src/project/main.go", "project/main.go"},
		{"empty", "", ""},
		{"single slash", "/", "/"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := shortenPath(tc.path)
			if got != tc.want {
				t.Errorf("shortenPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// =============================================================================
// splitJSONAndWarnings
// =============================================================================

func TestSplitJSONAndWarnings(t *testing.T) {
	t.Parallel()

	t.Run("empty input", func(t *testing.T) {
		t.Parallel()
		jsonBlob, warnings := splitJSONAndWarnings(nil)
		if jsonBlob != nil {
			t.Errorf("expected nil json, got %q", jsonBlob)
		}
		if warnings != nil {
			t.Errorf("expected nil warnings, got %v", warnings)
		}
	})

	t.Run("only whitespace", func(t *testing.T) {
		t.Parallel()
		jsonBlob, warnings := splitJSONAndWarnings([]byte("   \n  "))
		if jsonBlob != nil {
			t.Errorf("expected nil json, got %q", jsonBlob)
		}
		if warnings != nil {
			t.Errorf("expected nil warnings, got %v", warnings)
		}
	})

	t.Run("pure JSON", func(t *testing.T) {
		t.Parallel()
		input := []byte(`{"key":"value"}`)
		jsonBlob, warnings := splitJSONAndWarnings(input)
		if string(jsonBlob) != `{"key":"value"}` {
			t.Errorf("json = %q, want %q", jsonBlob, `{"key":"value"}`)
		}
		if len(warnings) != 0 {
			t.Errorf("expected no warnings, got %v", warnings)
		}
	})

	t.Run("warnings before JSON", func(t *testing.T) {
		t.Parallel()
		input := []byte("Warning: some issue\n{\"key\":\"value\"}")
		jsonBlob, warnings := splitJSONAndWarnings(input)
		if string(jsonBlob) != `{"key":"value"}` {
			t.Errorf("json = %q", jsonBlob)
		}
		if len(warnings) != 1 || warnings[0] != "Warning: some issue" {
			t.Errorf("warnings = %v", warnings)
		}
	})

	t.Run("warnings after JSON", func(t *testing.T) {
		t.Parallel()
		input := []byte("{\"key\":\"value\"}\nSome trailing text")
		jsonBlob, warnings := splitJSONAndWarnings(input)
		if string(jsonBlob) != `{"key":"value"}` {
			t.Errorf("json = %q", jsonBlob)
		}
		if len(warnings) != 1 || warnings[0] != "Some trailing text" {
			t.Errorf("warnings = %v", warnings)
		}
	})

	t.Run("no JSON braces", func(t *testing.T) {
		t.Parallel()
		input := []byte("Just a warning line\nAnother warning")
		jsonBlob, warnings := splitJSONAndWarnings(input)
		if jsonBlob != nil {
			t.Errorf("expected nil json, got %q", jsonBlob)
		}
		if len(warnings) != 2 {
			t.Errorf("expected 2 warnings, got %d", len(warnings))
		}
	})
}

// =============================================================================
// extractWarningLines
// =============================================================================

func TestExtractWarningLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data []byte
		want int
	}{
		{"empty", nil, 0},
		{"single line", []byte("warning"), 1},
		{"multiple lines", []byte("line1\nline2\nline3"), 3},
		{"blank lines filtered", []byte("line1\n\n\nline2"), 2},
		{"only whitespace", []byte("  \n  \n  "), 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractWarningLines(tc.data)
			if len(got) != tc.want {
				t.Errorf("extractWarningLines(%q) returned %d lines, want %d", tc.data, len(got), tc.want)
			}
		})
	}
}

// =============================================================================
// sessionFromProjectKey
// =============================================================================

func TestSessionFromProjectKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
		want string
	}{
		{"empty", "", ""},
		{"simple path", "/data/projects/myapp", "myapp"},
		{"trailing slash", "/data/projects/myapp/", "myapp"},
		{"root", "/", ""},
		{"dot", ".", ""},
		{"nested", "/home/user/repos/backend", "backend"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := sessionFromProjectKey(tc.key)
			if got != tc.want {
				t.Errorf("sessionFromProjectKey(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

// =============================================================================
// splitPathSegments
// =============================================================================

func TestSplitPathSegments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want []string
	}{
		{"simple", "a/b/c", []string{"a", "b", "c"}},
		{"leading slash", "/a/b", []string{"a", "b"}},
		{"trailing slash", "a/b/", []string{"a", "b"}},
		{"double slash", "a//b", []string{"a", "b"}},
		{"single segment", "file.go", []string{"file.go"}},
		{"empty", "", nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := splitPathSegments(tc.path)
			if len(got) != len(tc.want) {
				t.Fatalf("splitPathSegments(%q) = %v (len %d), want %v (len %d)", tc.path, got, len(got), tc.want, len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// =============================================================================
// matchPatternSegments
// =============================================================================

func TestMatchPatternSegments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern []string
		file    []string
		want    bool
	}{
		{"both empty", nil, nil, true},
		{"pattern empty file not", nil, []string{"a"}, false},
		{"file empty pattern not", []string{"a"}, nil, false},
		{"exact match", []string{"a", "b"}, []string{"a", "b"}, true},
		{"mismatch", []string{"a", "b"}, []string{"a", "c"}, false},
		{"double star matches zero", []string{"**", "file.go"}, []string{"file.go"}, true},
		{"double star matches one", []string{"**", "file.go"}, []string{"dir", "file.go"}, true},
		{"double star matches many", []string{"**", "file.go"}, []string{"a", "b", "c", "file.go"}, true},
		{"double star alone", []string{"**"}, []string{"a", "b"}, true},
		{"wildcard segment", []string{"*.go"}, []string{"main.go"}, true},
		{"wildcard segment no match", []string{"*.go"}, []string{"main.rs"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := matchPatternSegments(tc.pattern, tc.file)
			if got != tc.want {
				t.Errorf("matchPatternSegments(%v, %v) = %v, want %v", tc.pattern, tc.file, got, tc.want)
			}
		})
	}
}

// =============================================================================
// matchSegment
// =============================================================================

func TestMatchSegment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		segment string
		want    bool
	}{
		{"exact match", "file.go", "file.go", true},
		{"no match", "file.go", "file.rs", false},
		{"star glob", "*.go", "main.go", true},
		{"star glob no match", "*.go", "main.rs", false},
		{"question mark", "file.?o", "file.go", true},
		{"question mark no match", "file.?o", "file.rs", false},
		{"empty pattern", "", "", true},
		{"invalid pattern", "[", "x", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := matchSegment(tc.pattern, tc.segment)
			if got != tc.want {
				t.Errorf("matchSegment(%q, %q) = %v, want %v", tc.pattern, tc.segment, got, tc.want)
			}
		})
	}
}

// =============================================================================
// matchesAnyPattern
// =============================================================================

func TestMatchesAnyPattern(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		patterns []string
		file     string
		want     bool
	}{
		{"empty file", []string{"*.go"}, "", false},
		{"empty patterns", nil, "file.go", false},
		{"match first", []string{"*.go", "*.rs"}, "main.go", true},
		{"match second", []string{"*.rs", "*.go"}, "main.go", true},
		{"no match", []string{"*.rs", "*.py"}, "main.go", false},
		{"glob match", []string{"internal/**/*.go"}, "internal/scanner/scanner.go", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := matchesAnyPattern(tc.patterns, tc.file)
			if got != tc.want {
				t.Errorf("matchesAnyPattern(%v, %q) = %v, want %v", tc.patterns, tc.file, got, tc.want)
			}
		})
	}
}

// =============================================================================
// buildAssignmentMessage
// =============================================================================

func TestBuildAssignmentMessage(t *testing.T) {
	t.Parallel()

	t.Run("empty items", func(t *testing.T) {
		t.Parallel()
		got := buildAssignmentMessage(nil)
		if got != "No matching findings." {
			t.Errorf("got %q", got)
		}
	})

	t.Run("single finding", func(t *testing.T) {
		t.Parallel()
		items := []assignmentFinding{
			{
				Assignment: &assignment.Assignment{
					BeadID:    "bd-123",
					BeadTitle: "Fix scanner",
				},
				Finding: Finding{
					File:     "scanner.go",
					Line:     10,
					Severity: SeverityWarning,
					RuleID:   "rule-1",
					Message:  "unused variable",
				},
			},
		}
		got := buildAssignmentMessage(items)
		if !strings.Contains(got, "bd-123") {
			t.Error("should contain bead ID")
		}
		if !strings.Contains(got, "Fix scanner") {
			t.Error("should contain bead title")
		}
		if !strings.Contains(got, "rule-1") {
			t.Error("should contain rule ID")
		}
		if !strings.Contains(got, "scanner.go:10") {
			t.Error("should contain file:line")
		}
	})

	t.Run("critical finding has error icon", func(t *testing.T) {
		t.Parallel()
		items := []assignmentFinding{
			{
				Assignment: &assignment.Assignment{BeadID: "bd-456"},
				Finding: Finding{
					File: "a.go", Line: 1, Severity: SeverityCritical,
					RuleID: "crit-1", Message: "critical issue",
				},
			},
		}
		got := buildAssignmentMessage(items)
		if !strings.Contains(got, "\u274c") { // ❌
			t.Error("critical finding should have error icon")
		}
	})

	t.Run("bead without title", func(t *testing.T) {
		t.Parallel()
		items := []assignmentFinding{
			{
				Assignment: &assignment.Assignment{BeadID: "bd-789"},
				Finding: Finding{
					File: "a.go", Line: 1, Severity: SeverityWarning,
					RuleID: "rule-2", Message: "warning",
				},
			},
		}
		got := buildAssignmentMessage(items)
		if !strings.Contains(got, "### bd-789\n") {
			t.Errorf("bead without title should show just ID, got %q", got)
		}
	})
}

// =============================================================================
// buildTargetedMessage
// =============================================================================

func TestBuildTargetedMessage(t *testing.T) {
	t.Parallel()

	t.Run("basic findings", func(t *testing.T) {
		t.Parallel()
		findings := []Finding{
			{File: "a.go", Line: 5, Severity: SeverityWarning, RuleID: "r1", Message: "msg1"},
			{File: "b.go", Line: 10, Severity: SeverityCritical, RuleID: "r2", Message: "msg2"},
		}
		got := buildTargetedMessage(findings, "internal/**")
		if !strings.Contains(got, "2 issues") {
			t.Error("should contain count")
		}
		if !strings.Contains(got, "internal/**") {
			t.Error("should contain pattern")
		}
		if !strings.Contains(got, "r1") {
			t.Error("should contain rule ID")
		}
		if !strings.Contains(got, "\u274c") { // ❌ for critical
			t.Error("should contain critical icon")
		}
	})

	t.Run("more than 10 truncated", func(t *testing.T) {
		t.Parallel()
		findings := make([]Finding, 15)
		for i := range findings {
			findings[i] = Finding{File: "a.go", Line: i, Severity: SeverityWarning, RuleID: "r", Message: "msg"}
		}
		got := buildTargetedMessage(findings, "*.go")
		if !strings.Contains(got, "...and 5 more") {
			t.Errorf("should truncate at 10, got %q", got)
		}
	})
}

// =============================================================================
// buildSummaryMessage
// =============================================================================

func TestBuildSummaryMessage(t *testing.T) {
	t.Parallel()

	t.Run("empty scan", func(t *testing.T) {
		t.Parallel()
		result := &ScanResult{
			Totals: ScanTotals{},
		}
		got := buildSummaryMessage(result)
		if !strings.Contains(got, "## Scan Summary") {
			t.Error("should contain header")
		}
		if !strings.Contains(got, "**Critical**: 0") {
			t.Error("should show zero critical")
		}
	})

	t.Run("with findings shows top issues", func(t *testing.T) {
		t.Parallel()
		result := &ScanResult{
			Totals: ScanTotals{Critical: 2, Warning: 3, Info: 5, Files: 10},
			Findings: []Finding{
				{File: "a.go", Severity: SeverityCritical, RuleID: "crit-1", Message: "critical bug"},
				{File: "b.go", Severity: SeverityWarning, RuleID: "warn-1", Message: "unused import"},
				{File: "c.go", Severity: SeverityInfo, RuleID: "info-1", Message: "style issue"},
			},
		}
		got := buildSummaryMessage(result)
		if !strings.Contains(got, "**Critical**: 2") {
			t.Error("should show critical count")
		}
		if !strings.Contains(got, "**Warnings**: 3") {
			t.Error("should show warning count")
		}
		if !strings.Contains(got, "## Top Issues") {
			t.Error("should contain top issues header")
		}
		if !strings.Contains(got, "crit-1") {
			t.Error("should include critical finding")
		}
		if !strings.Contains(got, "warn-1") {
			t.Error("should include warning finding")
		}
		// Info findings should NOT appear in top issues
		if strings.Contains(got, "info-1") {
			t.Error("info findings should not appear in top issues")
		}
	})

	t.Run("top issues capped at 5", func(t *testing.T) {
		t.Parallel()
		findings := make([]Finding, 10)
		for i := range findings {
			findings[i] = Finding{
				File: "a.go", Severity: SeverityCritical,
				RuleID: "r", Message: "msg",
			}
		}
		result := &ScanResult{
			Totals:   ScanTotals{Critical: 10},
			Findings: findings,
		}
		got := buildSummaryMessage(result)
		// 4 summary bullets (Critical, Warnings, Info, Files) + max 5 top issues = 9
		topIssuesSection := got[strings.Index(got, "## Top Issues"):]
		issueCount := strings.Count(topIssuesSection, "- ")
		if issueCount > 5 {
			t.Errorf("top issues should cap at 5, found %d in section", issueCount)
		}
	})
}

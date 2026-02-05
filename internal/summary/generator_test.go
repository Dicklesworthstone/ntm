package summary

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type stubSummarizer struct {
	text string
	fail bool
}

func (s stubSummarizer) Summarize(ctx context.Context, prompt string, maxTokens int) (string, error) {
	if s.fail {
		return "", context.Canceled
	}
	return s.text, nil
}

func TestSummarizeSessionMissingOutput(t *testing.T) {
	_, err := SummarizeSession(context.Background(), Options{})
	if err == nil {
		t.Fatalf("expected error for missing outputs")
	}
}

func TestSummarizeSessionBriefFallback(t *testing.T) {
	output := strings.Join([]string{
		"## Accomplishments",
		"- Implemented SummarizeSession",
		"- Added tests",
		"",
		"## Changes",
		"- Updated internal/summary/generator.go",
		"- Modified internal/cli/summary.go",
		"",
		"## Pending",
		"- Wire into CLI",
		"- Add docs",
		"",
		"## Errors",
		"- Failed: lint error in file",
		"",
		"## Decisions",
		"- Using regex parsing",
		"",
		"Created internal/summary/generator.go",
		"Modified internal/cli/summary.go",
	}, "\n")

	summary, err := SummarizeSession(context.Background(), Options{
		Session: "demo",
		Outputs: []AgentOutput{{AgentID: "a1", AgentType: "cc", Output: output}},
		Format:  FormatBrief,
	})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}

	if len(summary.Accomplishments) == 0 {
		t.Fatalf("expected accomplishments parsed")
	}
	if summary.Text == "" {
		t.Fatalf("expected summary text")
	}
	if !strings.Contains(summary.Text, "Accomplishments") {
		t.Fatalf("expected brief format to include accomplishments")
	}

	var created, modified bool
	for _, f := range summary.Files {
		if f.Path == "internal/summary/generator.go" && f.Action == FileActionCreated {
			created = true
		}
		if f.Path == "internal/cli/summary.go" && f.Action == FileActionModified {
			modified = true
		}
	}
	if !created || !modified {
		t.Fatalf("expected file changes extracted (created=%v modified=%v)", created, modified)
	}
}

func TestSummarizeSessionIncludesGitChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	projectDir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = projectDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git init failed: %v", err)
	}

	filePath := filepath.Join(projectDir, "new.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	summary, err := SummarizeSession(context.Background(), Options{
		Session:        "demo",
		Outputs:        []AgentOutput{{AgentID: "a1", AgentType: "cc", Output: "did work"}},
		Format:         FormatBrief,
		ProjectDir:     projectDir,
		IncludeGitDiff: true,
	})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}

	found := false
	for _, fc := range summary.Files {
		if fc.Path == "new.txt" && fc.Action == FileActionCreated {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected git-created file in summary, got: %#v", summary.Files)
	}
}

func TestSummarizeSessionStructuredJSON(t *testing.T) {
	output := `{
  "summary": {
    "accomplishments": ["Implemented API"],
    "changes": ["Refactored router"],
    "pending": ["Add tests"],
    "errors": ["Error: foo"],
    "decisions": ["Use cobra"],
    "files": {
      "created": ["cmd/ntm/main.go"],
      "modified": ["internal/cli/root.go"],
      "deleted": ["old.txt"]
    }
  }
}`

	summary, err := SummarizeSession(context.Background(), Options{
		Session: "json",
		Outputs: []AgentOutput{{AgentID: "a1", AgentType: "cc", Output: output}},
		Format:  FormatDetailed,
	})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}

	if len(summary.Accomplishments) != 1 || summary.Accomplishments[0] != "Implemented API" {
		t.Fatalf("unexpected accomplishments: %+v", summary.Accomplishments)
	}
	if len(summary.Files) == 0 {
		t.Fatalf("expected file changes from json")
	}
	var hasCreated, hasModified, hasDeleted bool
	for _, f := range summary.Files {
		switch f.Path {
		case "cmd/ntm/main.go":
			hasCreated = f.Action == FileActionCreated
		case "internal/cli/root.go":
			hasModified = f.Action == FileActionModified
		case "old.txt":
			hasDeleted = f.Action == FileActionDeleted
		}
	}
	if !hasCreated || !hasModified || !hasDeleted {
		t.Fatalf("expected created/modified/deleted entries")
	}
}

func TestSummarizeSessionHandoffFormat(t *testing.T) {
	output := strings.Join([]string{
		"Completed: Implemented session summarizer",
		"Next: Add wiring",
	}, "\n")

	summary, err := SummarizeSession(context.Background(), Options{
		Session: "handoff",
		Outputs: []AgentOutput{{AgentID: "a1", AgentType: "cc", Output: output}},
		Format:  FormatHandoff,
	})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if summary.Handoff == nil {
		t.Fatalf("expected handoff output")
	}
	if !strings.Contains(summary.Text, "goal:") || !strings.Contains(summary.Text, "now:") {
		t.Fatalf("expected yaml text to include goal/now")
	}
}

func TestSummarizeSessionUsesSummarizer(t *testing.T) {
	summary, err := SummarizeSession(context.Background(), Options{
		Session:    "llm",
		Outputs:    []AgentOutput{{AgentID: "a1", AgentType: "cc", Output: "done"}},
		Format:     FormatBrief,
		Summarizer: stubSummarizer{text: "LLM summary"},
	})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if summary.Text != "LLM summary" {
		t.Fatalf("expected summarizer text, got %q", summary.Text)
	}
}

func TestSummarizeSessionTruncation(t *testing.T) {
	longText := strings.Repeat("a", 800)
	summary, err := SummarizeSession(context.Background(), Options{
		Session:    "truncate",
		Outputs:    []AgentOutput{{AgentID: "a1", AgentType: "cc", Output: "done"}},
		Format:     FormatBrief,
		MaxTokens:  10,
		Summarizer: stubSummarizer{text: longText},
	})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if !strings.Contains(summary.Text, "Summary truncated") {
		t.Fatalf("expected truncation note")
	}
	if summary.TokenEstimate > 10 {
		t.Fatalf("expected token estimate <= 10, got %d", summary.TokenEstimate)
	}
}

// =============================================================================
// Section Extraction Tests
// =============================================================================

func TestExtractSectionItems(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestExtractSectionItems | Testing section header parsing")

	tests := []struct {
		name     string
		text     string
		headers  []string
		expected []string
	}{
		{
			name: "basic accomplishments",
			text: `## Accomplishments
- Did thing one
- Did thing two
## Changes
- Changed something`,
			headers:  accomplishmentHeaders,
			expected: []string{"Did thing one", "Did thing two"},
		},
		{
			name: "alternative header format",
			text: `Completed:
- Task A
- Task B`,
			headers:  accomplishmentHeaders,
			expected: []string{"Task A", "Task B"},
		},
		{
			name: "numbered list",
			text: `Done
1. First item
2. Second item`,
			headers:  accomplishmentHeaders,
			expected: []string{"First item", "Second item"},
		},
		{
			name: "checkbox items",
			text: `Summary
[x] Completed task
[ ] Incomplete task`,
			headers:  accomplishmentHeaders,
			expected: []string{"Completed task", "Incomplete task"},
		},
		{
			name: "empty section",
			text: `## Accomplishments
## Changes`,
			headers:  accomplishmentHeaders,
			expected: nil,
		},
		{
			name:     "no matching section",
			text:     "Some random text without sections",
			headers:  accomplishmentHeaders,
			expected: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("SUMMARY_TEST: %s | Headers=%v", tc.name, tc.headers)
			result := extractSectionItems(tc.text, tc.headers)
			if len(result) != len(tc.expected) {
				t.Fatalf("expected %d items, got %d: %v", len(tc.expected), len(result), result)
			}
			for i, exp := range tc.expected {
				if result[i] != exp {
					t.Errorf("item[%d]: expected %q, got %q", i, exp, result[i])
				}
			}
		})
	}
}

// =============================================================================
// Key Action Extraction Tests
// =============================================================================

func TestExtractKeyActions(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestExtractKeyActions | Testing key action pattern matching")

	tests := []struct {
		name     string
		lines    []string
		expected int
	}{
		{
			name: "implemented patterns",
			lines: []string{
				"Implemented the new API endpoint",
				"Fixed the login bug",
				"Added unit tests for auth module",
			},
			expected: 3,
		},
		{
			name: "created patterns",
			lines: []string{
				"Created new config file",
				"Completed the migration",
				"Resolved merge conflict",
			},
			expected: 3,
		},
		{
			name: "no matching patterns",
			lines: []string{
				"This is just a comment",
				"Looking at the code",
			},
			expected: 0,
		},
		{
			name: "long lines filtered",
			lines: []string{
				"Implemented " + strings.Repeat("x", 250),
			},
			expected: 0,
		},
		{
			name:     "empty lines",
			lines:    []string{"", "  ", "\t"},
			expected: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("SUMMARY_TEST: %s | Lines=%d", tc.name, len(tc.lines))
			result := extractKeyActions(tc.lines)
			if len(result) != tc.expected {
				t.Fatalf("expected %d actions, got %d: %v", tc.expected, len(result), result)
			}
		})
	}
}

// =============================================================================
// Change Highlights Extraction Tests
// =============================================================================

func TestExtractChangeHighlights(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestExtractChangeHighlights | Testing change pattern matching")

	tests := []struct {
		name     string
		lines    []string
		expected int
	}{
		{
			name: "update patterns",
			lines: []string{
				"Updated the database schema",
				"Modified the config parser",
				"Refactored authentication logic",
			},
			expected: 3,
		},
		{
			name: "change patterns",
			lines: []string{
				"Changed error handling",
				"Rewrote the test suite",
				"Renamed variable for clarity",
			},
			expected: 3,
		},
		{
			name: "no matching patterns",
			lines: []string{
				"This is documentation",
				"A simple comment",
			},
			expected: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("SUMMARY_TEST: %s | Lines=%d", tc.name, len(tc.lines))
			result := extractChangeHighlights(tc.lines)
			if len(result) != tc.expected {
				t.Fatalf("expected %d changes, got %d: %v", tc.expected, len(result), result)
			}
		})
	}
}

// =============================================================================
// Error Line Extraction Tests
// =============================================================================

func TestExtractErrorLines(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestExtractErrorLines | Testing error pattern matching")

	tests := []struct {
		name     string
		lines    []string
		expected int
	}{
		{
			name: "error patterns",
			lines: []string{
				"Error: connection refused",
				"Failed to compile module",
				"Panic: nil pointer dereference",
			},
			expected: 3,
		},
		{
			name: "exception patterns",
			lines: []string{
				"Exception in thread main",
			},
			expected: 1,
		},
		{
			name: "no matching patterns",
			lines: []string{
				"All tests passed",
				"Build successful",
			},
			expected: 0,
		},
		{
			name:     "empty lines",
			lines:    []string{"", "  "},
			expected: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("SUMMARY_TEST: %s | Lines=%d", tc.name, len(tc.lines))
			result := extractErrorLines(tc.lines)
			if len(result) != tc.expected {
				t.Fatalf("expected %d errors, got %d: %v", tc.expected, len(result), result)
			}
		})
	}
}

// =============================================================================
// Pending Inline Extraction Tests
// =============================================================================

func TestExtractPendingInline(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestExtractPendingInline | Testing inline pending/todo detection")

	tests := []struct {
		name     string
		lines    []string
		expected int
	}{
		{
			name: "todo patterns",
			lines: []string{
				"TODO: fix the bug",
				"todo implement feature",
			},
			expected: 2,
		},
		{
			name: "next patterns",
			lines: []string{
				"Next: add tests",
				"next: refactor code",
			},
			expected: 2,
		},
		{
			name: "pending patterns",
			lines: []string{
				"Pending: review changes",
				"Remaining: documentation",
			},
			expected: 2,
		},
		{
			name: "no matching patterns",
			lines: []string{
				"This is done",
				"Complete",
			},
			expected: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("SUMMARY_TEST: %s | Lines=%d", tc.name, len(tc.lines))
			result := extractPendingInline(tc.lines)
			if len(result) != tc.expected {
				t.Fatalf("expected %d pending items, got %d: %v", tc.expected, len(result), result)
			}
		})
	}
}

// =============================================================================
// File Change Extraction Tests
// =============================================================================

func TestExtractFileChanges(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestExtractFileChanges | Testing file path extraction from text")

	tests := []struct {
		name      string
		lines     []string
		expectMin int
	}{
		{
			name: "go file paths",
			lines: []string{
				"Created internal/summary/generator.go",
				"Modified cmd/ntm/main.go",
			},
			expectMin: 2,
		},
		{
			name: "various extensions",
			lines: []string{
				"Updated src/components/Button.tsx",
				"Added tests/unit/test_main.py",
				"Changed config/settings.yaml",
			},
			expectMin: 3,
		},
		{
			name: "relative paths",
			lines: []string{
				"Edited ./internal/cli/root.go",
			},
			expectMin: 1,
		},
		{
			name: "no file paths",
			lines: []string{
				"Just some text without files",
				"More text here",
			},
			expectMin: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("SUMMARY_TEST: %s | Lines=%d", tc.name, len(tc.lines))
			result := extractFileChanges(tc.lines)
			if len(result) < tc.expectMin {
				t.Fatalf("expected at least %d file changes, got %d: %v", tc.expectMin, len(result), result)
			}
		})
	}
}

// =============================================================================
// Path Extraction Tests
// =============================================================================

func TestExtractPathsFromLine(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestExtractPathsFromLine | Testing path regex patterns")

	tests := []struct {
		name      string
		line      string
		expectMin int
	}{
		{
			name:      "internal path",
			line:      "Modified internal/cli/root.go",
			expectMin: 1,
		},
		{
			name:      "src path",
			line:      "Updated src/components/App.tsx",
			expectMin: 1,
		},
		{
			name:      "cmd path",
			line:      "Created cmd/ntm/main.go",
			expectMin: 1,
		},
		{
			name:      "relative path",
			line:      "Changed ./config.yaml",
			expectMin: 1,
		},
		{
			name:      "multiple paths",
			line:      "Compare internal/a.go and internal/b.go",
			expectMin: 2,
		},
		{
			name:      "URL filtered out",
			line:      "See https://example.com/path.js for details",
			expectMin: 0,
		},
		{
			name:      "no path",
			line:      "This is just text",
			expectMin: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("SUMMARY_TEST: %s | Line=%q", tc.name, tc.line)
			result := extractPathsFromLine(tc.line)
			if len(result) < tc.expectMin {
				t.Fatalf("expected at least %d paths, got %d: %v", tc.expectMin, len(result), result)
			}
		})
	}
}

// =============================================================================
// File Action Inference Tests
// =============================================================================

func TestInferFileAction(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestInferFileAction | Testing action inference from context")

	tests := []struct {
		name     string
		line     string
		path     string
		expected string
	}{
		{
			name:     "created explicit",
			line:     "Created internal/foo.go",
			path:     "internal/foo.go",
			expected: FileActionCreated,
		},
		{
			name:     "modified explicit",
			line:     "Modified internal/bar.go",
			path:     "internal/bar.go",
			expected: FileActionModified,
		},
		{
			name:     "deleted explicit",
			line:     "Deleted old/file.go",
			path:     "old/file.go",
			expected: FileActionDeleted,
		},
		{
			name:     "read explicit",
			line:     "Read config.yaml",
			path:     "config.yaml",
			expected: FileActionRead,
		},
		{
			name:     "creating keyword",
			line:     "Creating a new file",
			path:     "new.go",
			expected: FileActionCreated,
		},
		{
			name:     "editing keyword",
			line:     "Editing the configuration",
			path:     "config.yaml",
			expected: FileActionModified,
		},
		{
			name:     "removing keyword",
			line:     "Removing deprecated code",
			path:     "old.go",
			expected: FileActionDeleted,
		},
		{
			name:     "unknown action",
			line:     "Processed the data",
			path:     "data.json",
			expected: FileActionUnknown,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("SUMMARY_TEST: %s | Line=%q Path=%q", tc.name, tc.line, tc.path)
			result := inferFileAction(tc.line, tc.path)
			if result != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, result)
			}
		})
	}
}

// =============================================================================
// Structured JSON Parsing Tests
// =============================================================================

func TestParseStructuredJSON(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestParseStructuredJSON | Testing JSON block extraction")

	tests := []struct {
		name           string
		text           string
		expectedAccomp int
		expectedFiles  int
	}{
		{
			name:           "basic json",
			text:           `{"accomplishments": ["Task A", "Task B"], "pending": ["Next task"]}`,
			expectedAccomp: 2,
			expectedFiles:  0,
		},
		{
			name:           "nested summary",
			text:           `{"summary": {"accomplishments": ["Done"], "files": {"created": ["new.go"]}}}`,
			expectedAccomp: 1,
			expectedFiles:  1,
		},
		{
			name:           "file_changes key",
			text:           `{"file_changes": {"created": ["a.go"], "modified": ["b.go"]}}`,
			expectedAccomp: 0,
			expectedFiles:  2,
		},
		{
			name:           "invalid json",
			text:           `{not valid json}`,
			expectedAccomp: 0,
			expectedFiles:  0,
		},
		{
			name:           "empty text",
			text:           "",
			expectedAccomp: 0,
			expectedFiles:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("SUMMARY_TEST: %s | TextLen=%d", tc.name, len(tc.text))
			data := parseStructuredJSON(tc.text)
			if len(data.accomplishments) != tc.expectedAccomp {
				t.Errorf("expected %d accomplishments, got %d", tc.expectedAccomp, len(data.accomplishments))
			}
			if len(data.files) != tc.expectedFiles {
				t.Errorf("expected %d files, got %d", tc.expectedFiles, len(data.files))
			}
		})
	}
}

// =============================================================================
// JSON Block Extraction Tests
// =============================================================================

func TestExtractJSONBlocks(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestExtractJSONBlocks | Testing JSON block detection")

	tests := []struct {
		name     string
		text     string
		expected int
	}{
		{
			name:     "single json object",
			text:     `{"key": "value"}`,
			expected: 1,
		},
		{
			name:     "single json array",
			text:     `["a", "b", "c"]`,
			expected: 1,
		},
		{
			name:     "multiple json blocks",
			text:     "{\"a\": 1}\nSome text\n{\"b\": 2}",
			expected: 2,
		},
		{
			name:     "nested json",
			text:     `{"outer": {"inner": {"deep": 1}}}`,
			expected: 1,
		},
		{
			name:     "no json",
			text:     "Just plain text without JSON",
			expected: 0,
		},
		{
			name:     "malformed json",
			text:     `{"unclosed": `,
			expected: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("SUMMARY_TEST: %s | TextLen=%d", tc.name, len(tc.text))
			result := extractJSONBlocks(tc.text)
			if len(result) != tc.expected {
				t.Fatalf("expected %d blocks, got %d", tc.expected, len(result))
			}
		})
	}
}

// =============================================================================
// Formatting Tests
// =============================================================================

func TestFormatBrief(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestFormatBrief | Testing brief format output")

	summary := &SessionSummary{
		Session:         "test-session",
		Accomplishments: []string{"Task A", "Task B", "Task C", "Task D"},
		Changes:         []string{"Change 1"},
		Pending:         []string{"Next 1"},
		Errors:          []string{"Error 1"},
		Files: []FileChange{
			{Path: "a.go", Action: FileActionCreated},
			{Path: "b.go", Action: FileActionModified},
		},
	}

	result := formatBrief(summary)
	t.Logf("SUMMARY_TEST: Brief output length=%d", len(result))

	if !strings.Contains(result, "test-session") {
		t.Error("expected session name in output")
	}
	if !strings.Contains(result, "Accomplishments") {
		t.Error("expected accomplishments label")
	}
	if !strings.Contains(result, "Changes") {
		t.Error("expected changes label")
	}
	if !strings.Contains(result, "Files") {
		t.Error("expected files label")
	}
}

func TestFormatDetailed(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestFormatDetailed | Testing detailed format output")

	summary := &SessionSummary{
		Session:         "test-session",
		Accomplishments: []string{"Task A", "Task B"},
		Changes:         []string{"Change 1"},
		Pending:         []string{"Next 1"},
		Errors:          []string{"Error 1"},
		Decisions:       []string{"Decision 1"},
		Files: []FileChange{
			{Path: "a.go", Action: FileActionCreated, Context: "new file"},
		},
	}

	result := formatDetailed(summary)
	t.Logf("SUMMARY_TEST: Detailed output length=%d", len(result))

	if !strings.Contains(result, "## Session Summary: test-session") {
		t.Error("expected markdown header with session name")
	}
	if !strings.Contains(result, "## Accomplishments") {
		t.Error("expected accomplishments section")
	}
	if !strings.Contains(result, "## Decisions") {
		t.Error("expected decisions section")
	}
	if !strings.Contains(result, "- Task A") {
		t.Error("expected bullet points")
	}
}

// =============================================================================
// Handoff Summary Building Tests
// =============================================================================

func TestBuildHandoffSummary(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestBuildHandoffSummary | Testing handoff generation")

	summary := &SessionSummary{
		Session:         "handoff-test",
		Accomplishments: []string{"Completed auth system"},
		Pending:         []string{"Add unit tests"},
		Errors:          []string{"Build failed once"},
		Files: []FileChange{
			{Path: "internal/auth.go", Action: FileActionCreated},
			{Path: "internal/cli.go", Action: FileActionModified},
			{Path: "old.go", Action: FileActionDeleted},
		},
	}

	handoff := buildHandoffSummary(summary)
	t.Logf("SUMMARY_TEST: Handoff session=%s", handoff.Session)

	if handoff.Session != "handoff-test" {
		t.Errorf("expected session 'handoff-test', got %q", handoff.Session)
	}
	if handoff.Goal == "" {
		t.Error("expected goal to be set")
	}
	if handoff.Now == "" {
		t.Error("expected now to be set")
	}
	if len(handoff.Blockers) == 0 {
		t.Error("expected blockers from errors")
	}
	if len(handoff.Files.Created) == 0 {
		t.Error("expected created files")
	}
	if len(handoff.Files.Modified) == 0 {
		t.Error("expected modified files")
	}
	if len(handoff.Files.Deleted) == 0 {
		t.Error("expected deleted files")
	}
}

// =============================================================================
// Token Truncation Tests
// =============================================================================

func TestTruncateToTokens(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestTruncateToTokens | Testing token-based truncation")

	tests := []struct {
		name      string
		text      string
		maxTokens int
		expectLen int
	}{
		{
			name:      "short text no truncation",
			text:      "Hello world",
			maxTokens: 100,
			expectLen: 11,
		},
		{
			name:      "long text truncated",
			text:      strings.Repeat("a", 1000),
			maxTokens: 10,
			expectLen: 40 + len("\n\n[Summary truncated due to token limit]"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("SUMMARY_TEST: %s | MaxTokens=%d", tc.name, tc.maxTokens)
			result := truncateToTokens(tc.text, tc.maxTokens)
			if len(tc.text) > tc.maxTokens*4 {
				if !strings.Contains(result, "truncated") {
					t.Error("expected truncation note for long text")
				}
			}
		})
	}
}

func TestTruncateAtRuneBoundary(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestTruncateAtRuneBoundary | Testing rune-safe truncation")

	tests := []struct {
		name     string
		text     string
		maxBytes int
		expected string
	}{
		{
			name:     "ascii truncation",
			text:     "hello world",
			maxBytes: 5,
			expected: "hello",
		},
		{
			name:     "no truncation needed",
			text:     "short",
			maxBytes: 100,
			expected: "short",
		},
		{
			name:     "unicode safe",
			text:     "héllo",
			maxBytes: 3,
			expected: "hé",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("SUMMARY_TEST: %s | MaxBytes=%d", tc.name, tc.maxBytes)
			result := truncateAtRuneBoundary(tc.text, tc.maxBytes)
			if result != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, result)
			}
		})
	}
}

// =============================================================================
// Helper Function Tests
// =============================================================================

func TestAppendUnique(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestAppendUnique | Testing deduplication")

	list := []string{"a", "b"}
	list = appendUnique(list, "c")
	if len(list) != 3 {
		t.Fatalf("expected 3 items, got %d", len(list))
	}

	list = appendUnique(list, "b")
	if len(list) != 3 {
		t.Fatalf("expected 3 items after duplicate, got %d", len(list))
	}

	list = appendUnique(list, "  ")
	if len(list) != 3 {
		t.Fatalf("expected 3 items after empty, got %d", len(list))
	}
}

func TestAppendUniqueList(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestAppendUniqueList | Testing batch deduplication")

	list := []string{"a"}
	list = appendUniqueList(list, []string{"b", "a", "c"})
	if len(list) != 3 {
		t.Fatalf("expected 3 items, got %d: %v", len(list), list)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestFirstNonEmpty | Testing first non-empty selection")

	tests := []struct {
		name     string
		items    []string
		expected string
	}{
		{
			name:     "first item",
			items:    []string{"first", "second"},
			expected: "first",
		},
		{
			name:     "skip empty",
			items:    []string{"", "  ", "valid"},
			expected: "valid",
		},
		{
			name:     "all empty",
			items:    []string{"", "  "},
			expected: "",
		},
		{
			name:     "nil slice",
			items:    nil,
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := firstNonEmpty(tc.items)
			if result != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, result)
			}
		})
	}
}

// =============================================================================
// Multi-Agent Output Aggregation Tests
// =============================================================================

func TestAggregateOutputs(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestAggregateOutputs | Testing multi-agent output merging")

	outputs := []AgentOutput{
		{AgentID: "agent1", AgentType: "cc", Output: `## Accomplishments
- Fixed bug A
## Changes
- Updated handler.go`},
		{AgentID: "agent2", AgentType: "cod", Output: `{"summary": {"accomplishments": ["Implemented feature B"]}}`},
		{AgentID: "agent3", AgentType: "gmi", Output: "TODO: review changes"},
	}

	data := aggregateOutputs(outputs)
	t.Logf("SUMMARY_TEST: Aggregated | Accomplishments=%d Changes=%d Pending=%d",
		len(data.accomplishments), len(data.changes), len(data.pending))

	if len(data.accomplishments) < 2 {
		t.Errorf("expected at least 2 accomplishments, got %d", len(data.accomplishments))
	}
	if len(data.pending) == 0 {
		t.Error("expected pending items from TODO")
	}
}

// =============================================================================
// Edge Case Tests
// =============================================================================

func TestSummarizeSessionEmptyOutput(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestSummarizeSessionEmptyOutput | Testing empty output handling")

	summary, err := SummarizeSession(context.Background(), Options{
		Session: "empty",
		Outputs: []AgentOutput{{AgentID: "a1", AgentType: "cc", Output: "   "}},
		Format:  FormatBrief,
	})
	if err != nil {
		t.Fatalf("should not error on whitespace output: %v", err)
	}
	if summary.Session != "empty" {
		t.Errorf("expected session 'empty', got %q", summary.Session)
	}
}

func TestSummarizeSessionNilContext(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestSummarizeSessionNilContext | Testing nil context handling")

	summary, err := SummarizeSession(nil, Options{
		Session: "nilctx",
		Outputs: []AgentOutput{{AgentID: "a1", AgentType: "cc", Output: "done"}},
	})
	if err != nil {
		t.Fatalf("should handle nil context: %v", err)
	}
	if summary == nil {
		t.Fatal("expected non-nil summary")
	}
}

func TestMergeFileChanges(t *testing.T) {
	t.Logf("SUMMARY_TEST: TestMergeFileChanges | Testing file change deduplication")

	existing := []FileChange{
		{Path: "a.go", Action: FileActionCreated, Context: ""},
	}
	incoming := []FileChange{
		{Path: "a.go", Action: FileActionCreated, Context: "new file"},
		{Path: "b.go", Action: FileActionModified, Context: "updated"},
	}

	result := mergeFileChanges(existing, incoming)
	if len(result) != 2 {
		t.Fatalf("expected 2 files, got %d", len(result))
	}

	// Check that context was merged for duplicate
	for _, fc := range result {
		if fc.Path == "a.go" && fc.Context != "new file" {
			t.Error("expected context to be merged for duplicate path")
		}
	}
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkSummarizeSession(b *testing.B) {
	output := strings.Join([]string{
		"## Accomplishments",
		"- Task 1",
		"- Task 2",
		"## Changes",
		"- Change 1",
		"Created internal/foo.go",
		"Modified internal/bar.go",
	}, "\n")

	opts := Options{
		Session: "bench",
		Outputs: []AgentOutput{{AgentID: "a1", AgentType: "cc", Output: output}},
		Format:  FormatBrief,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = SummarizeSession(context.Background(), opts)
	}
}

func BenchmarkExtractFileChanges(b *testing.B) {
	lines := []string{
		"Created internal/summary/generator.go",
		"Modified cmd/ntm/main.go",
		"Updated src/components/Button.tsx",
		"Added tests/unit/test_main.py",
		"Changed config/settings.yaml",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = extractFileChanges(lines)
	}
}

// =============================================================================
// RenderSummary tests
// =============================================================================

func TestRenderSummary_Nil(t *testing.T) {
	t.Parallel()
	if got := RenderSummary(nil, FormatBrief); got != "" {
		t.Errorf("RenderSummary(nil) = %q, want empty", got)
	}
}

func TestRenderSummary_Brief(t *testing.T) {
	t.Parallel()
	summary := &SessionSummary{
		Session:         "test-session",
		Format:          FormatBrief,
		Accomplishments: []string{"Implemented auth flow"},
		Pending:         []string{"Add tests"},
	}

	got := RenderSummary(summary, FormatBrief)
	if got == "" {
		t.Fatal("expected non-empty brief summary")
	}
	if !strings.Contains(got, "Implemented auth flow") {
		t.Error("expected summary to contain accomplishment")
	}
}

func TestRenderSummary_Detailed(t *testing.T) {
	t.Parallel()
	summary := &SessionSummary{
		Session:         "detail-session",
		Format:          FormatDetailed,
		Accomplishments: []string{"Built API endpoint"},
		Changes:         []string{"Modified api/routes.go"},
		Files: []FileChange{
			{Path: "api/routes.go", Action: "modified"},
		},
		Errors: []string{"Test timeout on CI"},
	}

	got := RenderSummary(summary, FormatDetailed)
	if got == "" {
		t.Fatal("expected non-empty detailed summary")
	}
	if !strings.Contains(got, "Built API endpoint") {
		t.Error("expected summary to contain accomplishment")
	}
}

func TestRenderSummary_Handoff(t *testing.T) {
	t.Parallel()
	summary := &SessionSummary{
		Session:         "handoff-session",
		Accomplishments: []string{"Refactored module"},
		Pending:         []string{"Review PR"},
		Files: []FileChange{
			{Path: "internal/module.go", Action: "modified"},
			{Path: "internal/module_test.go", Action: "created"},
		},
	}

	got := RenderSummary(summary, FormatHandoff)
	if got == "" {
		t.Fatal("expected non-empty handoff summary")
	}
}

func TestRenderSummary_DefaultFormat(t *testing.T) {
	t.Parallel()
	summary := &SessionSummary{
		Session: "default-session",
		Format:  FormatBrief,
		Text:    "Pre-rendered summary text",
	}

	// Empty format string → falls through to summary.Format
	got := RenderSummary(summary, "")
	if got == "" {
		t.Fatal("expected non-empty summary with empty format")
	}
}

func TestRenderSummary_UnknownFormat_FallbackToText(t *testing.T) {
	t.Parallel()
	summary := &SessionSummary{
		Session: "fallback-session",
		Text:    "Existing text content",
	}

	got := RenderSummary(summary, "unknown")
	if got != "Existing text content" {
		t.Errorf("expected fallback to Text field, got %q", got)
	}
}

func TestRenderSummary_UnknownFormat_NoText(t *testing.T) {
	t.Parallel()
	summary := &SessionSummary{
		Session:         "no-text-session",
		Accomplishments: []string{"Did things"},
	}

	got := RenderSummary(summary, "unknown")
	if got == "" {
		t.Error("expected brief fallback when Text is empty")
	}
}

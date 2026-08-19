package robot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

func TestNewConflictDetector(t *testing.T) {

	t.Run("nil config uses defaults", func(t *testing.T) {
		cd := NewConflictDetector(nil)
		if cd.activityWindows == nil {
			t.Error("activityWindows not initialized")
		}
	})

	t.Run("with custom config", func(t *testing.T) {
		cfg := &ConflictDetectorConfig{
			RepoPath:   "/custom/path",
			ProjectKey: "test-project",
		}
		cd := NewConflictDetector(cfg)
		if cd.repoPath != "/custom/path" {
			t.Errorf("repoPath = %v, want /custom/path", cd.repoPath)
		}
		if cd.projectKey != "test-project" {
			t.Errorf("projectKey = %v, want test-project", cd.projectKey)
		}
	})
}

func TestParseGitStatusPorcelain(t *testing.T) {

	tests := []struct {
		name   string
		output string
		want   []GitFileStatus
	}{
		{
			name:   "empty output",
			output: "",
			want:   nil,
		},
		{
			name:   "modified file",
			output: " M file.go",
			want: []GitFileStatus{
				{Path: "file.go", Status: "M", Staged: false},
			},
		},
		{
			name:   "staged file",
			output: "M  file.go",
			want: []GitFileStatus{
				{Path: "file.go", Status: "M", Staged: true},
			},
		},
		{
			name:   "untracked file",
			output: "?? newfile.go",
			want: []GitFileStatus{
				{Path: "newfile.go", Status: "??", Staged: false},
			},
		},
		{
			name:   "multiple files",
			output: " M file1.go\nA  file2.go\n?? file3.go",
			want: []GitFileStatus{
				{Path: "file1.go", Status: "M", Staged: false},
				{Path: "file2.go", Status: "A", Staged: true},
				{Path: "file3.go", Status: "??", Staged: false},
			},
		},
		{
			name:   "renamed file",
			output: "R  old.go -> new.go",
			want: []GitFileStatus{
				{Path: "new.go", Status: "R", Staged: true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseGitStatusPorcelain(tt.output, "/nonexistent")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d results, want %d", len(got), len(tt.want))
			}
			for i, g := range got {
				w := tt.want[i]
				if g.Path != w.Path || g.Status != w.Status || g.Staged != w.Staged {
					t.Errorf("result[%d] = %+v, want %+v", i, g, w)
				}
			}
		})
	}
}

func TestMatchesPattern(t *testing.T) {

	tests := []struct {
		filePath string
		pattern  string
		want     bool
	}{
		// Exact matches
		{"file.go", "file.go", true},
		{"dir/file.go", "dir/file.go", true},
		{"file.go", "other.go", false},

		// Directory prefix matches
		{"dir/file.go", "dir", true},
		{"dir/sub/file.go", "dir", true},
		{"other/file.go", "dir", false},
		{"dir/file.go", "dir/", true},
		{"dir/sub/file.go", "dir/", true},
		{"other/file.go", "dir/", false},

		// Glob patterns
		{"file.go", "*.go", true},
		{"file.txt", "*.go", false},
		{"dir/file.go", "*.go", true}, // matches basename
		{"dir/sub/file.go", "*.go", true},
		{"dir/file.go", "dir/*.go", true},
		{"dir/sub/file.go", "dir/*.go", false},
		{"dir/sub/nested/file.go", "dir/*/*.go", false},

		// Directory glob patterns
		{"internal/robot/file.go", "internal/**", true},
		{"internal/file.go", "internal/**", true},
		{"external/file.go", "internal/**", false},
		{"/tmp/ntm/file.go", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.filePath+"_vs_"+tt.pattern, func(t *testing.T) {
			if got := matchesPattern(tt.filePath, tt.pattern); got != tt.want {
				t.Errorf("matchesPattern(%q, %q) = %v, want %v", tt.filePath, tt.pattern, got, tt.want)
			}
		})
	}
}

func FuzzMatchesPattern(f *testing.F) {
	seeds := [][2]string{
		{"", ""},
		{"/tmp/ntm/file.go", ""},
		{"internal/robot/routing.go", "internal/robot/routing.go"},
		{"internal/robot/routing.go", "internal/robot/*.go"},
		{"internal/robot/routing.go", "internal/**"},
		{"internal/robot/routing_test.go", "internal/**/*_test.go"},
		{"dir/sub/file.go", "*.go"},
		{"dir/sub/file.go", "dir/*.go"},
		{"dir/sub/file.go", "[bad"},
	}
	for _, seed := range seeds {
		f.Add(seed[0], seed[1])
	}

	f.Fuzz(func(t *testing.T, filePath, pattern string) {
		if len(filePath) > 1024 || len(pattern) > 1024 {
			return
		}

		matched := matchesPattern(filePath, pattern)
		if filePath == pattern && !matched {
			t.Fatalf("matchesPattern(%q, %q) = false, want exact matches to match", filePath, pattern)
		}
		if pattern == "" && filePath != "" && matched {
			t.Fatalf("matchesPattern(%q, empty pattern) = true, want false", filePath)
		}
		if strings.HasSuffix(pattern, "/") && !strings.Contains(pattern, "*") && strings.HasPrefix(filePath, pattern) && !matched {
			t.Fatalf("matchesPattern(%q, %q) = false, want directory prefix match", filePath, pattern)
		}
	})
}

func TestContainsAny(t *testing.T) {

	tests := []struct {
		name string
		a    []string
		b    []string
		want bool
	}{
		{"empty slices", nil, nil, false},
		{"a empty", nil, []string{"x"}, false},
		{"b empty", []string{"x"}, nil, false},
		{"no overlap", []string{"a", "b"}, []string{"c", "d"}, false},
		{"one overlap", []string{"a", "b"}, []string{"b", "c"}, true},
		{"all overlap", []string{"a", "b"}, []string{"a", "b"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := containsAny(tt.a, tt.b); got != tt.want {
				t.Errorf("containsAny() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConflictDetector_ScoreConflict(t *testing.T) {

	tests := []struct {
		name           string
		modifiers      []string
		holders        []string
		wantConfidence float64
		wantReason     ConflictReason
	}{
		{
			name:           "multiple modifiers - high conflict",
			modifiers:      []string{"%1", "%2"},
			holders:        nil,
			wantConfidence: 0.9,
			wantReason:     ReasonConcurrentActivity,
		},
		{
			name:           "single modifier with reservation - not holder",
			modifiers:      []string{"%1"},
			holders:        []string{"AgentB"},
			wantConfidence: 0.85,
			wantReason:     ReasonReservationViolation,
		},
		{
			name:           "no modifier, multiple holders",
			modifiers:      nil,
			holders:        []string{"AgentA", "AgentB"},
			wantConfidence: 0.75,
			wantReason:     ReasonOverlappingReservations,
		},
		{
			name:           "no modifier, no holders",
			modifiers:      nil,
			holders:        nil,
			wantConfidence: 0.6,
			wantReason:     ReasonUnclaimedModification,
		},
		{
			name:           "single modifier, no holders - normal",
			modifiers:      []string{"%1"},
			holders:        nil,
			wantConfidence: 0.4,
			wantReason:     ReasonConcurrentActivity,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cd := NewConflictDetector(nil)
			conflict := &DetectedConflict{
				LikelyModifiers:    tt.modifiers,
				ReservationHolders: tt.holders,
			}
			cd.scoreConflict(conflict, len(tt.modifiers), len(tt.holders))

			if conflict.Confidence != tt.wantConfidence {
				t.Errorf("Confidence = %v, want %v", conflict.Confidence, tt.wantConfidence)
			}
			if conflict.Reason != tt.wantReason {
				t.Errorf("Reason = %v, want %v", conflict.Reason, tt.wantReason)
			}
		})
	}
}

func TestConflictDetector_FindLikelyModifiers(t *testing.T) {

	cd := NewConflictDetector(nil)
	now := time.Now()

	// Record activity for two panes
	cd.RecordActivity("%1", "claude", now.Add(-2*time.Minute), now.Add(-1*time.Minute), true)
	cd.RecordActivity("%2", "codex", now.Add(-30*time.Second), now.Add(30*time.Second), true)

	tests := []struct {
		name       string
		modifiedAt time.Time
		wantCount  int
	}{
		{
			name:       "modification during pane 1 activity",
			modifiedAt: now.Add(-90 * time.Second),
			wantCount:  1,
		},
		{
			name:       "modification during pane 2 activity",
			modifiedAt: now,
			wantCount:  1,
		},
		{
			name:       "modification during both activities",
			modifiedAt: now.Add(-30 * time.Second), // within tolerance of both
			wantCount:  2,
		},
		{
			name:       "modification outside all activities",
			modifiedAt: now.Add(-10 * time.Minute),
			wantCount:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := GitFileStatus{Path: "test.go", ModifiedAt: tt.modifiedAt}
			modifiers := cd.findLikelyModifiers(file)
			if len(modifiers) != tt.wantCount {
				t.Errorf("found %d modifiers, want %d", len(modifiers), tt.wantCount)
			}
		})
	}
}

func TestConflictDetector_FindReservationHolders(t *testing.T) {

	cd := NewConflictDetector(nil)
	now := time.Now()

	ftNow := agentmail.FlexTime{Time: now}
	reservations := []agentmail.FileReservation{
		{
			PathPattern: "internal/**",
			AgentName:   "AgentA",
			ExpiresTS:   agentmail.FlexTime{Time: now.Add(1 * time.Hour)},
		},
		{
			PathPattern: "*.go",
			AgentName:   "AgentB",
			ExpiresTS:   agentmail.FlexTime{Time: now.Add(1 * time.Hour)},
		},
		{
			PathPattern: "cmd/**",
			AgentName:   "AgentC",
			ExpiresTS:   agentmail.FlexTime{Time: now.Add(-1 * time.Hour)}, // expired
		},
		{
			PathPattern: "docs/**",
			AgentName:   "AgentD",
			ExpiresTS:   agentmail.FlexTime{Time: now.Add(1 * time.Hour)},
			ReleasedTS:  &ftNow, // released
		},
	}

	tests := []struct {
		filePath  string
		wantCount int
	}{
		{"internal/robot/file.go", 2}, // matches internal/** and *.go
		{"main.go", 1},                // matches *.go only
		{"cmd/app/main.go", 1},        // would match cmd/** but expired
		{"docs/readme.md", 0},         // would match docs/** but released
		{"external/lib.c", 0},         // no matches
	}

	for _, tt := range tests {
		t.Run(tt.filePath, func(t *testing.T) {
			holders := cd.findReservationHolders(tt.filePath, reservations)
			if len(holders) != tt.wantCount {
				t.Errorf("found %d holders, want %d: %v", len(holders), tt.wantCount, holders)
			}
		})
	}
}

func TestConflictDetector_DetectConflicts_Integration(t *testing.T) {
	// This test requires a git repository, skip if not in one
	if _, err := os.Stat(".git"); os.IsNotExist(err) {
		// Try parent directories
		wd, _ := os.Getwd()
		for i := 0; i < 5; i++ {
			wd = filepath.Dir(wd)
			if _, err := os.Stat(filepath.Join(wd, ".git")); err == nil {
				break
			}
			if i == 4 {
				t.Skip("not in a git repository")
			}
		}
	}

	cd := NewConflictDetector(&ConflictDetectorConfig{})
	now := time.Now()

	// Record some activity
	cd.RecordActivity("%1", "claude", now.Add(-5*time.Minute), now, true)

	// Detect conflicts (may be empty if working tree is clean)
	conflicts, err := cd.DetectConflicts(context.Background())
	if err != nil {
		t.Logf("DetectConflicts returned error (may be expected): %v", err)
		return
	}

	// Just verify the function runs without panic
	t.Logf("Detected %d potential conflicts", len(conflicts))
}

func TestExtractCodeBlocks(t *testing.T) {

	tests := []struct {
		name    string
		content string
		want    []CodeBlock
	}{
		{
			name:    "empty content",
			content: "",
			want:    nil,
		},
		{
			name:    "no code blocks",
			content: "Just some text\nwithout code blocks",
			want:    nil,
		},
		{
			name:    "single code block with language",
			content: "Some text\n```go\nfunc main() {}\n```\nMore text",
			want: []CodeBlock{
				{Language: "go", Content: "func main() {}", LineStart: 2, LineEnd: 4},
			},
		},
		{
			name:    "code block without language",
			content: "```\nplain text\n```",
			want: []CodeBlock{
				{Language: "", Content: "plain text", LineStart: 1, LineEnd: 3},
			},
		},
		{
			name:    "multiple code blocks",
			content: "```python\nprint('hello')\n```\nSome text\n```javascript\nconsole.log('hi');\n```",
			want: []CodeBlock{
				{Language: "python", Content: "print('hello')", LineStart: 1, LineEnd: 3},
				{Language: "javascript", Content: "console.log('hi');", LineStart: 5, LineEnd: 7},
			},
		},
		{
			name:    "multiline code block",
			content: "```go\npackage main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```",
			want: []CodeBlock{
				{Language: "go", Content: "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}", LineStart: 1, LineEnd: 9},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractCodeBlocks(tt.content)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d blocks, want %d", len(got), len(tt.want))
			}
			for i, g := range got {
				w := tt.want[i]
				if g.Language != w.Language {
					t.Errorf("block[%d].Language = %q, want %q", i, g.Language, w.Language)
				}
				if g.Content != w.Content {
					t.Errorf("block[%d].Content = %q, want %q", i, g.Content, w.Content)
				}
				if g.LineStart != w.LineStart {
					t.Errorf("block[%d].LineStart = %d, want %d", i, g.LineStart, w.LineStart)
				}
				if g.LineEnd != w.LineEnd {
					t.Errorf("block[%d].LineEnd = %d, want %d", i, g.LineEnd, w.LineEnd)
				}
			}
		})
	}
}

func TestExtractFileMentions(t *testing.T) {

	tests := []struct {
		name      string
		content   string
		wantCount int
	}{
		{
			name:      "empty content",
			content:   "",
			wantCount: 0,
		},
		{
			name:      "no file paths",
			content:   "Just some regular text",
			wantCount: 0,
		},
		{
			name:      "single file path",
			content:   "Modified internal/robot/file.go",
			wantCount: 1,
		},
		{
			name:      "relative path",
			content:   "Reading ./config.yaml",
			wantCount: 1,
		},
		{
			name:      "multiple paths",
			content:   "Updated src/main.go and internal/api/handler.go",
			wantCount: 2,
		},
		{
			name:      "path with extension only",
			content:   "Check main.go for details",
			wantCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractFileMentions(tt.content)
			if len(got) != tt.wantCount {
				t.Errorf("got %d mentions, want %d: %+v", len(got), tt.wantCount, got)
			}
		})
	}
}

func TestInferFileAction(t *testing.T) {

	tests := []struct {
		line       string
		path       string
		wantAction string
		minConf    float64
	}{
		{"Created internal/robot/file.go", "internal/robot/file.go", FileActionCreated, 0.8},
		{"Creating new file", "file.go", FileActionCreated, 0.8},
		{"Modified src/main.go", "src/main.go", FileActionModified, 0.8},
		{"Updating the handler", "handler.go", FileActionModified, 0.5},
		{"Deleted old/file.go", "old/file.go", FileActionDeleted, 0.8},
		{"Reading config.yaml", "config.yaml", FileActionRead, 0.8},
		{"Some file mentioned", "file.go", FileActionUnknown, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			action, conf := inferFileAction(tt.line, tt.path)
			if action != tt.wantAction {
				t.Errorf("action = %q, want %q", action, tt.wantAction)
			}
			if conf < tt.minConf {
				t.Errorf("confidence = %.2f, want >= %.2f", conf, tt.minConf)
			}
		})
	}
}

func TestExtractCommands(t *testing.T) {

	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "empty content",
			content: "",
			want:    nil,
		},
		{
			name:    "no commands",
			content: "Just some text",
			want:    nil,
		},
		{
			name:    "dollar command",
			content: "$ go test ./...",
			want:    []string{"go test ./..."},
		},
		{
			name:    "percent command",
			content: "% ls -la",
			want:    []string{"ls -la"},
		},
		{
			name:    "multiple commands",
			content: "$ git status\n$ git add .\n$ git commit -m 'test'",
			want:    []string{"git status", "git add .", "git commit -m 'test'"},
		},
		{
			name:    "skip python REPL",
			content: ">>> print('hello')",
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractCommands(tt.content)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d commands, want %d", len(got), len(tt.want))
			}
			for i, g := range got {
				if g.Command != tt.want[i] {
					t.Errorf("command[%d] = %q, want %q", i, g.Command, tt.want[i])
				}
			}
		})
	}
}

func TestParseExitCode(t *testing.T) {

	tests := []struct {
		line string
		want *int
	}{
		{"exit code: 0", intPtr(0)},
		{"exit code: 1", intPtr(1)},
		{"Exit: 127", intPtr(127)},
		{"returned 0", intPtr(0)},
		{"status: 1", intPtr(1)},
		{"[0]", intPtr(0)},
		{"no exit code here", nil},
		{"", nil},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := parseExitCode(tt.line)
			if tt.want == nil && got != nil {
				t.Errorf("got %d, want nil", *got)
			} else if tt.want != nil && got == nil {
				t.Errorf("got nil, want %d", *tt.want)
			} else if tt.want != nil && got != nil && *got != *tt.want {
				t.Errorf("got %d, want %d", *got, *tt.want)
			}
		})
	}
}

func intPtr(i int) *int {
	return &i
}

// =============================================================================
// countErrors
// =============================================================================

func TestCountErrors(t *testing.T) {

	tests := []struct {
		name  string
		lines []string
		want  int
	}{
		{"empty", nil, 0},
		{"no errors", []string{"ok", "success", "done"}, 0},
		{"error:", []string{"error: something went wrong"}, 1},
		{"Error!", []string{"Error! compilation failed"}, 1},
		{"failed:", []string{"build failed: exit 1"}, 1},
		{"fatal:", []string{"fatal: ref not found"}, 1},
		{"panic:", []string{"panic: nil pointer"}, 1},
		{"multiple", []string{
			"starting build",
			"error: syntax error",
			"error: undefined var",
			"panic: runtime error",
		}, 3},
		{"case insensitive", []string{"ERROR: caps", "Failed: mixed"}, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countErrors(tt.lines)
			if got != tt.want {
				t.Errorf("countErrors() = %d, want %d", got, tt.want)
			}
		})
	}
}

// =============================================================================
// extractKeyActions
// =============================================================================

func TestExtractKeyActions(t *testing.T) {

	t.Run("empty", func(t *testing.T) {
		actions := extractKeyActions(nil, 5)
		if len(actions) != 0 {
			t.Errorf("expected empty, got %v", actions)
		}
	})

	t.Run("summary prefix", func(t *testing.T) {
		lines := []string{
			"some output",
			"Summary: Added authentication middleware",
			"more output",
		}
		actions := extractKeyActions(lines, 5)
		found := false
		for _, a := range actions {
			if strings.Contains(a, "Added authentication middleware") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected summary content, got %v", actions)
		}
	})

	t.Run("action verbs", func(t *testing.T) {
		lines := []string{
			"Created new handler for /api/users",
			"Fixed authentication bug in middleware",
			"Modified the config parser",
			"Implemented retry logic",
		}
		actions := extractKeyActions(lines, 10)
		if len(actions) < 4 {
			t.Errorf("expected at least 4 actions, got %d: %v", len(actions), actions)
		}
	})

	t.Run("respects max", func(t *testing.T) {
		lines := []string{
			"Created file1",
			"Created file2",
			"Created file3",
			"Created file4",
			"Created file5",
		}
		actions := extractKeyActions(lines, 3)
		if len(actions) > 3 {
			t.Errorf("expected max 3 actions, got %d", len(actions))
		}
	})

	t.Run("skips short lines", func(t *testing.T) {
		lines := []string{
			"ok",       // too short
			"Created.", // too short
			"Created a new file for handling authentication requests",
		}
		actions := extractKeyActions(lines, 5)
		if len(actions) != 1 {
			t.Errorf("expected 1 action (long one), got %d: %v", len(actions), actions)
		}
	})

	t.Run("priority ordering", func(t *testing.T) {
		// Priority 1 items should come before priority 2/3
		lines := []string{
			"Testing the new feature now",     // testing = priority 3
			"Fixed critical security bug",     // fixed = priority 1
			"Added helper function for utils", // added = priority 2
		}
		actions := extractKeyActions(lines, 3)
		// Fixed should appear before Testing due to priority
		fixedIdx, testingIdx := -1, -1
		for i, a := range actions {
			if strings.Contains(strings.ToLower(a), "fixed") {
				fixedIdx = i
			}
			if strings.Contains(strings.ToLower(a), "testing") {
				testingIdx = i
			}
		}
		if fixedIdx >= 0 && testingIdx >= 0 && fixedIdx > testingIdx {
			t.Errorf("fixed (priority 1) should come before testing (priority 3), got actions: %v", actions)
		}
	})
}

// =============================================================================
// cleanActionLine
// =============================================================================

func TestCleanActionLine(t *testing.T) {

	tests := []struct {
		input string
		want  string
	}{
		{"simple text", "simple text"},
		{"- bullet point", "bullet point"},
		{"* asterisk point", "asterisk point"},
		{"• unicode bullet", "unicode bullet"},
		{"1. numbered item", "numbered item"},
		{"[x] checkbox done", "checkbox done"},
		{"[ ] checkbox empty", "checkbox empty"},
		{"  leading spaces  ", "leading spaces"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := cleanActionLine(tt.input)
			if got != tt.want {
				t.Errorf("cleanActionLine(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCleanActionLine_Truncation(t *testing.T) {

	longLine := strings.Repeat("a", 100)
	result := cleanActionLine(longLine)
	if len(result) > 83 { // 80 + "..."
		t.Errorf("expected truncation, got len=%d", len(result))
	}
	if !strings.HasSuffix(result, "...") {
		t.Errorf("expected ... suffix, got %q", result)
	}
}

// =============================================================================
// formatDuration (synthesis.go version)
// =============================================================================

// =============================================================================
// FormatSessionSummaryText
// =============================================================================

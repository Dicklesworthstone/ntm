package robot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

func TestDetectedConflict_ConfidenceLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		confidence float64
		want       ConflictConfidence
	}{
		{"high at 0.9", 0.9, ConfidenceHigh},
		{"high at 0.95", 0.95, ConfidenceHigh},
		{"high at 1.0", 1.0, ConfidenceHigh},
		{"medium at 0.7", 0.7, ConfidenceMedium},
		{"medium at 0.89", 0.89, ConfidenceMedium},
		{"low at 0.5", 0.5, ConfidenceLow},
		{"low at 0.69", 0.69, ConfidenceLow},
		{"none at 0.49", 0.49, ConfidenceNone},
		{"none at 0.0", 0.0, ConfidenceNone},
		{"none negative", -0.1, ConfidenceNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dc := &DetectedConflict{Confidence: tt.confidence}
			if got := dc.ConfidenceLevel(); got != tt.want {
				t.Errorf("ConfidenceLevel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestActivityWindow_Overlaps(t *testing.T) {
	t.Parallel()

	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		a    ActivityWindow
		b    ActivityWindow
		want bool
	}{
		{
			name: "no overlap - a before b",
			a:    ActivityWindow{Start: base, End: base.Add(10 * time.Minute)},
			b:    ActivityWindow{Start: base.Add(20 * time.Minute), End: base.Add(30 * time.Minute)},
			want: false,
		},
		{
			name: "no overlap - b before a",
			a:    ActivityWindow{Start: base.Add(20 * time.Minute), End: base.Add(30 * time.Minute)},
			b:    ActivityWindow{Start: base, End: base.Add(10 * time.Minute)},
			want: false,
		},
		{
			name: "overlap - partial",
			a:    ActivityWindow{Start: base, End: base.Add(20 * time.Minute)},
			b:    ActivityWindow{Start: base.Add(10 * time.Minute), End: base.Add(30 * time.Minute)},
			want: true,
		},
		{
			name: "overlap - a contains b",
			a:    ActivityWindow{Start: base, End: base.Add(30 * time.Minute)},
			b:    ActivityWindow{Start: base.Add(10 * time.Minute), End: base.Add(20 * time.Minute)},
			want: true,
		},
		{
			name: "overlap - b contains a",
			a:    ActivityWindow{Start: base.Add(10 * time.Minute), End: base.Add(20 * time.Minute)},
			b:    ActivityWindow{Start: base, End: base.Add(30 * time.Minute)},
			want: true,
		},
		{
			name: "adjacent - no overlap",
			a:    ActivityWindow{Start: base, End: base.Add(10 * time.Minute)},
			b:    ActivityWindow{Start: base.Add(10 * time.Minute), End: base.Add(20 * time.Minute)},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.a.Overlaps(&tt.b); got != tt.want {
				t.Errorf("Overlaps() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestActivityWindow_Contains(t *testing.T) {
	t.Parallel()

	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	window := ActivityWindow{
		Start: base,
		End:   base.Add(10 * time.Minute),
	}

	tests := []struct {
		name string
		time time.Time
		want bool
	}{
		{"before window", base.Add(-1 * time.Minute), false},
		{"at start", base, true},
		{"in middle", base.Add(5 * time.Minute), true},
		{"at end", base.Add(10 * time.Minute), true},
		{"after window", base.Add(11 * time.Minute), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := window.Contains(tt.time); got != tt.want {
				t.Errorf("Contains() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewConflictDetector(t *testing.T) {
	t.Parallel()

	t.Run("nil config uses defaults", func(t *testing.T) {
		t.Parallel()
		cd := NewConflictDetector(nil)
		if cd.activityWindows == nil {
			t.Error("activityWindows not initialized")
		}
	})

	t.Run("with custom config", func(t *testing.T) {
		t.Parallel()
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

func TestConflictDetector_RecordActivity(t *testing.T) {
	t.Parallel()

	cd := NewConflictDetector(nil)
	now := time.Now()

	// Record activity
	cd.RecordActivity("%1", "claude", now.Add(-5*time.Minute), now, true)
	cd.RecordActivity("%1", "claude", now.Add(-2*time.Minute), now.Add(1*time.Minute), true)
	cd.RecordActivity("%2", "codex", now.Add(-3*time.Minute), now, false)

	windows := cd.GetActivityWindows()
	if len(windows["%1"]) != 2 {
		t.Errorf("pane %%1 should have 2 windows, got %d", len(windows["%1"]))
	}
	if len(windows["%2"]) != 1 {
		t.Errorf("pane %%2 should have 1 window, got %d", len(windows["%2"]))
	}
}

func TestConflictDetector_ClearActivityWindows(t *testing.T) {
	t.Parallel()

	cd := NewConflictDetector(nil)
	now := time.Now()

	cd.RecordActivity("%1", "claude", now.Add(-5*time.Minute), now, true)
	if len(cd.GetActivityWindows()) == 0 {
		t.Error("should have activity windows before clear")
	}

	cd.ClearActivityWindows()
	if len(cd.GetActivityWindows()) != 0 {
		t.Error("should have no activity windows after clear")
	}
}

func TestParseGitStatusPorcelain(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
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
	t.Parallel()

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

		// Directory glob patterns
		{"internal/robot/file.go", "internal/**", true},
		{"internal/file.go", "internal/**", true},
		{"external/file.go", "internal/**", false},
	}

	for _, tt := range tests {
		t.Run(tt.filePath+"_vs_"+tt.pattern, func(t *testing.T) {
			t.Parallel()
			if got := matchesPattern(tt.filePath, tt.pattern); got != tt.want {
				t.Errorf("matchesPattern(%q, %q) = %v, want %v", tt.filePath, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestContainsAny(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
			if got := containsAny(tt.a, tt.b); got != tt.want {
				t.Errorf("containsAny() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConflictDetector_ScoreConflict(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
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
	t.Parallel()

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
	t.Parallel()

	cd := NewConflictDetector(nil)
	now := time.Now()

	reservations := []agentmail.FileReservation{
		{
			PathPattern: "internal/**",
			AgentName:   "AgentA",
			ExpiresTS:   now.Add(1 * time.Hour),
		},
		{
			PathPattern: "*.go",
			AgentName:   "AgentB",
			ExpiresTS:   now.Add(1 * time.Hour),
		},
		{
			PathPattern: "cmd/**",
			AgentName:   "AgentC",
			ExpiresTS:   now.Add(-1 * time.Hour), // expired
		},
		{
			PathPattern: "docs/**",
			AgentName:   "AgentD",
			ExpiresTS:   now.Add(1 * time.Hour),
			ReleasedTS:  &now, // released
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

func TestSummarizeConflicts(t *testing.T) {
	t.Parallel()

	conflicts := []DetectedConflict{
		{Path: "file1.go", Confidence: 0.95, Reason: ReasonConcurrentActivity},
		{Path: "file2.go", Confidence: 0.75, Reason: ReasonReservationViolation},
		{Path: "file3.go", Confidence: 0.55, Reason: ReasonUnclaimedModification},
		{Path: "file4.go", Confidence: 0.85, Reason: ReasonConcurrentActivity},
	}

	summary := SummarizeConflicts(conflicts)

	if summary.TotalConflicts != 4 {
		t.Errorf("TotalConflicts = %d, want 4", summary.TotalConflicts)
	}
	if summary.HighConfidence != 1 {
		t.Errorf("HighConfidence = %d, want 1", summary.HighConfidence)
	}
	if summary.MedConfidence != 2 {
		t.Errorf("MedConfidence = %d, want 2", summary.MedConfidence)
	}
	if summary.LowConfidence != 1 {
		t.Errorf("LowConfidence = %d, want 1", summary.LowConfidence)
	}
	if summary.ByReason["concurrent_activity"] != 2 {
		t.Errorf("ByReason[concurrent_activity] = %d, want 2", summary.ByReason["concurrent_activity"])
	}
}

func TestNewConflictDetectionResponse(t *testing.T) {
	t.Parallel()

	t.Run("no conflicts", func(t *testing.T) {
		t.Parallel()
		resp := NewConflictDetectionResponse(nil)
		if !resp.Success {
			t.Error("Success should be true")
		}
		if resp.Summary != nil {
			t.Error("Summary should be nil for no conflicts")
		}
	})

	t.Run("with conflicts", func(t *testing.T) {
		t.Parallel()
		conflicts := []DetectedConflict{
			{Path: "file.go", Confidence: 0.9},
		}
		resp := NewConflictDetectionResponse(conflicts)
		if !resp.Success {
			t.Error("Success should be true")
		}
		if resp.Summary == nil {
			t.Error("Summary should not be nil")
		}
		if resp.Summary.TotalConflicts != 1 {
			t.Errorf("TotalConflicts = %d, want 1", resp.Summary.TotalConflicts)
		}
	})
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

func TestConflictDetector_PruneOldWindows(t *testing.T) {
	t.Parallel()

	cd := NewConflictDetector(nil)
	now := time.Now()

	// Record old activity (more than 1 hour ago)
	cd.RecordActivity("%1", "claude", now.Add(-2*time.Hour), now.Add(-90*time.Minute), true)
	// Record recent activity
	cd.RecordActivity("%2", "codex", now.Add(-30*time.Minute), now, true)

	// The pruning happens automatically in RecordActivity
	// Record another activity to trigger pruning
	cd.RecordActivity("%3", "gemini", now, now.Add(1*time.Minute), true)

	windows := cd.GetActivityWindows()

	// Old window should be pruned
	if len(windows["%1"]) != 0 {
		t.Errorf("pane %%1 should have 0 windows (pruned), got %d", len(windows["%1"]))
	}
	// Recent windows should remain
	if len(windows["%2"]) != 1 {
		t.Errorf("pane %%2 should have 1 window, got %d", len(windows["%2"]))
	}
	if len(windows["%3"]) != 1 {
		t.Errorf("pane %%3 should have 1 window, got %d", len(windows["%3"]))
	}
}

// ============================================================================
// Output Capture & Extraction Tests
// ============================================================================

func TestExtractCodeBlocks(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
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

func TestExtractJSONOutputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    int // number of JSON outputs
		isArray []bool
	}{
		{
			name:    "empty content",
			content: "",
			want:    0,
		},
		{
			name:    "no JSON",
			content: "Just some text",
			want:    0,
		},
		{
			name:    "simple object",
			content: `{"key": "value"}`,
			want:    1,
			isArray: []bool{false},
		},
		{
			name:    "simple array",
			content: `[1, 2, 3]`,
			want:    1,
			isArray: []bool{true},
		},
		{
			name:    "multiline object",
			content: "{\n  \"key\": \"value\",\n  \"num\": 42\n}",
			want:    1,
			isArray: []bool{false},
		},
		{
			name:    "object with nested",
			content: `{"outer": {"inner": true}}`,
			want:    1,
			isArray: []bool{false},
		},
		{
			name:    "invalid JSON",
			content: `{"key": value}`,
			want:    0,
		},
		{
			name:    "mixed content",
			content: "Output:\n{\"success\": true}\nDone",
			want:    1,
			isArray: []bool{false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ExtractJSONOutputs(tt.content)
			if len(got) != tt.want {
				t.Fatalf("got %d JSON outputs, want %d", len(got), tt.want)
			}
			for i, g := range got {
				if i < len(tt.isArray) && g.IsArray != tt.isArray[i] {
					t.Errorf("output[%d].IsArray = %v, want %v", i, g.IsArray, tt.isArray[i])
				}
			}
		})
	}
}

func TestIsValidJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  bool
	}{
		{`{}`, true},
		{`[]`, true},
		{`{"key": "value"}`, true},
		{`[1, 2, 3]`, true},
		{`{"nested": {"deep": true}}`, true},
		{``, false},
		{`{invalid}`, false},
		{`{"key": }`, false},
		{`just text`, false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			if got := isValidJSON(tt.input); got != tt.want {
				t.Errorf("isValidJSON(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractFileMentions(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
			got := ExtractFileMentions(tt.content)
			if len(got) != tt.wantCount {
				t.Errorf("got %d mentions, want %d: %+v", len(got), tt.wantCount, got)
			}
		})
	}
}

func TestInferFileAction(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
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
	t.Parallel()

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
			t.Parallel()
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
	t.Parallel()

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
			t.Parallel()
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

func TestNewOutputCapture(t *testing.T) {
	t.Parallel()

	t.Run("nil config uses defaults", func(t *testing.T) {
		t.Parallel()
		oc := NewOutputCapture(nil)
		if oc.config == nil {
			t.Fatal("config should not be nil")
		}
		if oc.config.MaxCapturesPerPane != 100 {
			t.Errorf("MaxCapturesPerPane = %d, want 100", oc.config.MaxCapturesPerPane)
		}
	})

	t.Run("custom config", func(t *testing.T) {
		t.Parallel()
		cfg := &OutputCaptureConfig{MaxCapturesPerPane: 50}
		oc := NewOutputCapture(cfg)
		if oc.config.MaxCapturesPerPane != 50 {
			t.Errorf("MaxCapturesPerPane = %d, want 50", oc.config.MaxCapturesPerPane)
		}
	})
}

func TestOutputCapture_CaptureAndExtract(t *testing.T) {
	t.Parallel()

	oc := NewOutputCapture(nil)

	content := "Output:\n```go\nfunc test() {}\n```\n{\"success\": true}\n$ go test"
	capture := oc.CaptureAndExtract("%1", "claude", content, "Run tests")

	if capture.PaneID != "%1" {
		t.Errorf("PaneID = %q, want %%1", capture.PaneID)
	}
	if capture.AgentType != "claude" {
		t.Errorf("AgentType = %q, want claude", capture.AgentType)
	}
	if capture.Prompt != "Run tests" {
		t.Errorf("Prompt = %q, want 'Run tests'", capture.Prompt)
	}
	if len(capture.CodeBlocks) != 1 {
		t.Errorf("got %d code blocks, want 1", len(capture.CodeBlocks))
	}
	if len(capture.JSONOutputs) != 1 {
		t.Errorf("got %d JSON outputs, want 1", len(capture.JSONOutputs))
	}
	if len(capture.Commands) != 1 {
		t.Errorf("got %d commands, want 1", len(capture.Commands))
	}
}

func TestOutputCapture_RingBuffer(t *testing.T) {
	t.Parallel()

	cfg := &OutputCaptureConfig{
		MaxCapturesPerPane: 3,
		MaxRetention:       1 * time.Hour,
	}
	oc := NewOutputCapture(cfg)

	// Add 5 captures
	for i := 0; i < 5; i++ {
		oc.CaptureAndExtract("%1", "claude", "content", "prompt")
	}

	// Should only have 3 (ring buffer limit)
	captures := oc.GetCaptures("%1", 0, nil)
	if len(captures) != 3 {
		t.Errorf("got %d captures, want 3", len(captures))
	}
}

func TestOutputCapture_GetCaptures(t *testing.T) {
	t.Parallel()

	oc := NewOutputCapture(nil)

	// Add captures
	oc.CaptureAndExtract("%1", "claude", "content1", "prompt1")
	time.Sleep(10 * time.Millisecond)
	oc.CaptureAndExtract("%1", "claude", "content2", "prompt2")

	t.Run("no limit", func(t *testing.T) {
		t.Parallel()
		captures := oc.GetCaptures("%1", 0, nil)
		if len(captures) != 2 {
			t.Errorf("got %d captures, want 2", len(captures))
		}
	})

	t.Run("with limit", func(t *testing.T) {
		t.Parallel()
		captures := oc.GetCaptures("%1", 1, nil)
		if len(captures) != 1 {
			t.Errorf("got %d captures, want 1", len(captures))
		}
	})

	t.Run("unknown pane", func(t *testing.T) {
		t.Parallel()
		captures := oc.GetCaptures("%unknown", 0, nil)
		if len(captures) != 0 {
			t.Errorf("got %d captures, want 0", len(captures))
		}
	})
}

func TestOutputCapture_GetLatestCapture(t *testing.T) {
	t.Parallel()

	t.Run("no captures", func(t *testing.T) {
		t.Parallel()
		oc := NewOutputCapture(nil)
		latest := oc.GetLatestCapture("%empty")
		if latest != nil {
			t.Error("expected nil for no captures")
		}
	})

	t.Run("returns latest", func(t *testing.T) {
		t.Parallel()
		oc := NewOutputCapture(nil)
		oc.CaptureAndExtract("%1", "claude", "first", "")
		oc.CaptureAndExtract("%1", "claude", "second", "")

		latest := oc.GetLatestCapture("%1")
		if latest == nil {
			t.Fatal("expected non-nil capture")
		}
	})
}

func TestOutputCapture_ClearCaptures(t *testing.T) {
	t.Parallel()

	oc := NewOutputCapture(nil)
	oc.CaptureAndExtract("%1", "claude", "content", "")
	oc.CaptureAndExtract("%2", "codex", "content", "")

	oc.ClearCaptures("%1")

	if len(oc.GetCaptures("%1", 0, nil)) != 0 {
		t.Error("pane %1 should be cleared")
	}
	if len(oc.GetCaptures("%2", 0, nil)) != 1 {
		t.Error("pane %2 should still have captures")
	}
}

func TestOutputCapture_Stats(t *testing.T) {
	t.Parallel()

	oc := NewOutputCapture(nil)
	oc.CaptureAndExtract("%1", "claude", "content", "")
	oc.CaptureAndExtract("%1", "claude", "content", "")
	oc.CaptureAndExtract("%2", "codex", "content", "")

	stats := oc.Stats()

	if stats.PaneCount != 2 {
		t.Errorf("PaneCount = %d, want 2", stats.PaneCount)
	}
	if stats.TotalCaptures != 3 {
		t.Errorf("TotalCaptures = %d, want 3", stats.TotalCaptures)
	}
}

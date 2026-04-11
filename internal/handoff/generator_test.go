package handoff

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

func TestNewGenerator(t *testing.T) {
	g := NewGenerator("/tmp/testproject")

	if g.projectDir != "/tmp/testproject" {
		t.Errorf("expected projectDir=/tmp/testproject, got %s", g.projectDir)
	}
	if g.logger == nil {
		t.Error("expected non-nil logger")
	}
}

func TestNewGeneratorWithLogger(t *testing.T) {
	g := NewGeneratorWithLogger("/tmp/testproject", nil)
	if g.logger == nil {
		t.Error("expected non-nil logger even when nil passed")
	}
}

func TestGeneratorProjectDir(t *testing.T) {
	g := NewGenerator("/tmp/myproject")
	if g.ProjectDir() != "/tmp/myproject" {
		t.Errorf("ProjectDir() = %s, want /tmp/myproject", g.ProjectDir())
	}
}

// Test patterns for Claude agent output
func TestAnalyzeOutputClaudePatterns(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantGoal     string
		wantNext     string
		wantBlockers int
	}{
		{
			name:     "Claude I've completed pattern",
			input:    "I've completed implementing the authentication module.",
			wantGoal: "implementing the authentication module",
		},
		{
			name:     "Claude Done pattern",
			input:    "Done: refactored the login handler",
			wantGoal: "refactored the login handler",
		},
		{
			name:     "Claude Finished pattern",
			input:    "Finished: unit tests for the API",
			wantGoal: "unit tests for the API",
		},
		{
			name:     "Claude checkmark pattern",
			input:    "✓ Fixed the null pointer bug",
			wantGoal: "Fixed the null pointer bug",
		},
		{
			name:     "Claude successfully pattern",
			input:    "Successfully migrated the database schema.",
			wantGoal: "migrated the database schema",
		},
		{
			name:     "Claude implemented pattern",
			input:    "Implemented rate limiting for the API.",
			wantGoal: "rate limiting for the API",
		},
		{
			name:     "Next step pattern",
			input:    "Done: feature X\nNext: add tests",
			wantGoal: "feature X",
			wantNext: "add tests",
		},
		{
			name:     "TODO pattern",
			input:    "TODO: refactor the error handling",
			wantNext: "refactor the error handling",
		},
		{
			name:         "Error blocker",
			input:        "Error: connection refused to database",
			wantBlockers: 1,
		},
		{
			name:         "Multiple blockers",
			input:        "Error: first issue\nFailed: second issue\nBlocked by: third issue",
			wantBlockers: 3,
		},
	}

	g := NewGenerator("/tmp")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := g.analyzeOutput([]byte(tt.input))

			if tt.wantGoal != "" && result.accomplishment != tt.wantGoal {
				t.Errorf("accomplishment = %q, want %q", result.accomplishment, tt.wantGoal)
			}
			if tt.wantNext != "" && result.nextStep != tt.wantNext {
				t.Errorf("nextStep = %q, want %q", result.nextStep, tt.wantNext)
			}
			if tt.wantBlockers > 0 && len(result.blockers) != tt.wantBlockers {
				t.Errorf("blockers = %d, want %d", len(result.blockers), tt.wantBlockers)
			}
		})
	}
}

// Test patterns for Codex agent output
func TestAnalyzeOutputCodexPatterns(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantGoal string
	}{
		{
			name:     "Codex DONE pattern",
			input:    "[DONE] Implemented the caching layer",
			wantGoal: "Implemented the caching layer",
		},
		{
			name:     "Codex Completed task pattern",
			input:    "Completed task: user registration flow",
			wantGoal: "user registration flow",
		},
	}

	g := NewGenerator("/tmp")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := g.analyzeOutput([]byte(tt.input))

			if result.accomplishment != tt.wantGoal {
				t.Errorf("accomplishment = %q, want %q", result.accomplishment, tt.wantGoal)
			}
		})
	}
}

// Test patterns for Gemini agent output
func TestAnalyzeOutputGeminiPatterns(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantGoal string
	}{
		{
			name:     "Gemini Task complete pattern",
			input:    "Task complete: database migration",
			wantGoal: "database migration",
		},
	}

	g := NewGenerator("/tmp")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := g.analyzeOutput([]byte(tt.input))

			if result.accomplishment != tt.wantGoal {
				t.Errorf("accomplishment = %q, want %q", result.accomplishment, tt.wantGoal)
			}
		})
	}
}

func TestAnalyzeOutputDecisions(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantDecisions map[string]string
	}{
		{
			name:  "Decided to pattern",
			input: "I decided to use Redis because it's faster for our use case.",
			wantDecisions: map[string]string{
				"use Redis": "it's faster for our use case",
			},
		},
		{
			name:  "Chose over pattern",
			input: "Chose PostgreSQL over MySQL because of better JSON support.",
			wantDecisions: map[string]string{
				"PostgreSQL": "MySQL",
			},
		},
		{
			name:  "Using for pattern",
			input: "Using JWT for authentication",
			wantDecisions: map[string]string{
				"JWT": "authentication",
			},
		},
	}

	g := NewGenerator("/tmp")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := g.analyzeOutput([]byte(tt.input))

			for key, wantVal := range tt.wantDecisions {
				if got, ok := result.decisions[key]; !ok || got != wantVal {
					t.Errorf("decision[%q] = %q, want %q", key, got, wantVal)
				}
			}
		})
	}
}

func TestGenerateFromOutputBasic(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewGenerator(tmpDir)

	output := []byte("I've completed implementing the user authentication.\nNext: add unit tests for the login flow")

	h, err := g.GenerateFromOutput("test-session", output)
	if err != nil {
		t.Fatalf("GenerateFromOutput failed: %v", err)
	}

	if h.Session != "test-session" {
		t.Errorf("Session = %s, want test-session", h.Session)
	}
	if h.Goal != "implementing the user authentication" {
		t.Errorf("Goal = %q, want implementing the user authentication", h.Goal)
	}
	if h.Now != "add unit tests for the login flow" {
		t.Errorf("Now = %q, want add unit tests for the login flow", h.Now)
	}
	if h.Status != StatusComplete {
		t.Errorf("Status = %s, want complete", h.Status)
	}
	if h.Outcome != OutcomeSucceeded {
		t.Errorf("Outcome = %s, want SUCCEEDED", h.Outcome)
	}
}

func TestGenerateFromOutputWithBlockers(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewGenerator(tmpDir)

	output := []byte("Error: could not connect to database\nBlocked by: missing credentials")

	h, err := g.GenerateFromOutput("test-session", output)
	if err != nil {
		t.Fatalf("GenerateFromOutput failed: %v", err)
	}

	if len(h.Blockers) != 2 {
		t.Errorf("Blockers count = %d, want 2", len(h.Blockers))
	}
	if h.Status != StatusBlocked {
		t.Errorf("Status = %s, want blocked", h.Status)
	}
	if h.Outcome != OutcomePartialMinus {
		t.Errorf("Outcome = %s, want PARTIAL_MINUS", h.Outcome)
	}
}

func TestGenerateFromOutputEmptyInput(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewGenerator(tmpDir)

	h, err := g.GenerateFromOutput("test-session", []byte(""))
	if err != nil {
		t.Fatalf("GenerateFromOutput failed: %v", err)
	}

	// Should still create handoff with partial status
	if h.Session != "test-session" {
		t.Errorf("Session = %s, want test-session", h.Session)
	}
	if h.Status != StatusPartial {
		t.Errorf("Status = %s, want partial", h.Status)
	}
}

func TestGenerateFromTranscriptBasic(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewGenerator(tmpDir)

	// Create a sample transcript file
	transcriptPath := filepath.Join(tmpDir, "session.jsonl")
	entries := []map[string]interface{}{
		{
			"role":    "user",
			"content": "Fix the login bug",
		},
		{
			"role":    "assistant",
			"content": "I've completed fixing the login bug.\nNext: add tests",
			"tool_calls": []map[string]interface{}{
				{
					"name": "Edit",
					"arguments": map[string]interface{}{
						"file_path": "/src/auth.go",
					},
				},
			},
		},
		{
			"role":    "tool",
			"name":    "Edit",
			"content": "Success",
		},
	}

	var lines []string
	for _, e := range entries {
		data, _ := json.Marshal(e)
		lines = append(lines, string(data))
	}
	if err := os.WriteFile(transcriptPath, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		t.Fatalf("failed to create transcript: %v", err)
	}

	h, err := g.GenerateFromTranscript("test-session", transcriptPath)
	if err != nil {
		t.Fatalf("GenerateFromTranscript failed: %v", err)
	}

	if h.Session != "test-session" {
		t.Errorf("Session = %s, want test-session", h.Session)
	}
	if h.Goal != "fixing the login bug" {
		t.Errorf("Goal = %q", h.Goal)
	}
	if h.Now != "add tests" {
		t.Errorf("Now = %q", h.Now)
	}
	// Check that file modifications were tracked
	if len(h.Files.Modified) == 0 {
		t.Error("expected file modifications to be tracked")
	}
}

func TestGenerateFromTranscriptWithErrors(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewGenerator(tmpDir)

	// Create a transcript with errors
	transcriptPath := filepath.Join(tmpDir, "session.jsonl")
	entries := []map[string]interface{}{
		{
			"role":    "assistant",
			"content": "Working on the task",
		},
		{
			"error": "Permission denied",
		},
		{
			"error": "File not found",
		},
	}

	var lines []string
	for _, e := range entries {
		data, _ := json.Marshal(e)
		lines = append(lines, string(data))
	}
	if err := os.WriteFile(transcriptPath, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		t.Fatalf("failed to create transcript: %v", err)
	}

	h, err := g.GenerateFromTranscript("test-session", transcriptPath)
	if err != nil {
		t.Fatalf("GenerateFromTranscript failed: %v", err)
	}

	if len(h.Blockers) != 2 {
		t.Errorf("Blockers = %d, want 2", len(h.Blockers))
	}
	if h.Status != StatusBlocked {
		t.Errorf("Status = %s, want blocked", h.Status)
	}
}

func TestGenerateFromTranscriptMissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewGenerator(tmpDir)

	_, err := g.GenerateFromTranscript("test-session", "/nonexistent/path.jsonl")
	if err == nil {
		t.Error("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "open transcript") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGenerateFromTranscriptMalformedJSON(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewGenerator(tmpDir)

	// Create a transcript with malformed JSON
	transcriptPath := filepath.Join(tmpDir, "session.jsonl")
	content := "not valid json\n{\"role\":\"assistant\",\"content\":\"Done: test\"}\nalso invalid"
	if err := os.WriteFile(transcriptPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create transcript: %v", err)
	}

	// Should not fail - malformed lines are skipped
	h, err := g.GenerateFromTranscript("test-session", transcriptPath)
	if err != nil {
		t.Fatalf("GenerateFromTranscript failed: %v", err)
	}

	// Should still extract from valid lines
	if h.Goal != "test" {
		t.Errorf("Goal = %q, want test", h.Goal)
	}
}

func TestGenerateFromTranscriptLargeBuffer(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewGenerator(tmpDir)

	// Create a transcript with a very long line (> 1MB)
	transcriptPath := filepath.Join(tmpDir, "session.jsonl")
	longContent := strings.Repeat("a", 2*1024*1024) // 2MB of content
	// Put the pattern on its own line so it can be matched by line-start regex
	entry := map[string]interface{}{
		"role":    "assistant",
		"content": longContent + "\nDone: large test completed",
	}
	data, _ := json.Marshal(entry)
	if err := os.WriteFile(transcriptPath, data, 0644); err != nil {
		t.Fatalf("failed to create transcript: %v", err)
	}

	// Should handle large lines without error
	h, err := g.GenerateFromTranscript("test-session", transcriptPath)
	if err != nil {
		t.Fatalf("GenerateFromTranscript failed: %v", err)
	}

	if h.Goal != "large test completed" {
		t.Errorf("Goal = %q, want large test completed", h.Goal)
	}
}

func TestEnrichWithGitState(t *testing.T) {
	// Skip if not in a git repo
	tmpDir := t.TempDir()
	g := NewGenerator(tmpDir)

	// Initialize git repo
	if err := os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755); err != nil {
		t.Skip("could not create .git dir")
	}

	h := New("test-session").WithGoalAndNow("Goal", "Now")

	// EnrichWithGitState may fail in temp dir without real git, but shouldn't panic
	err := g.EnrichWithGitState(h)
	// We just verify it doesn't panic - error is expected in non-git dirs
	_ = err
}

func TestEnrichWithGitStateInGitRepo(t *testing.T) {
	// Use the actual project directory which should be a git repo
	projectDir := "/data/projects/ntm"
	if _, err := os.Stat(filepath.Join(projectDir, ".git")); err != nil {
		t.Skip("not in ntm git repo")
	}

	g := NewGenerator(projectDir)
	h := New("test-session").WithGoalAndNow("Goal", "Now")

	err := g.EnrichWithGitState(h)
	if err != nil {
		t.Logf("EnrichWithGitState returned error (may be expected): %v", err)
	}

	// Should have populated git_branch finding
	if h.Findings != nil {
		if branch, ok := h.Findings["git_branch"]; ok {
			if branch == "" {
				t.Error("git_branch finding should not be empty")
			}
		}
	}
}

func TestGenerateAutoHandoff(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewGenerator(tmpDir)

	output := []byte("I've completed implementing the feature.\nNext: write tests")

	h, err := g.GenerateAutoHandoff("test-session", "cc", "test__cc_1", output, 80000, 100000)
	if err != nil {
		t.Fatalf("GenerateAutoHandoff failed: %v", err)
	}

	if h.AgentType != "cc" {
		t.Errorf("AgentType = %s, want cc", h.AgentType)
	}
	if h.PaneID != "test__cc_1" {
		t.Errorf("PaneID = %s, want test__cc_1", h.PaneID)
	}
	if h.TokensUsed != 80000 {
		t.Errorf("TokensUsed = %d, want 80000", h.TokensUsed)
	}
	if h.TokensMax != 100000 {
		t.Errorf("TokensMax = %d, want 100000", h.TokensMax)
	}
	if h.TokensPct != 80.0 {
		t.Errorf("TokensPct = %f, want 80.0", h.TokensPct)
	}
}

// Utility function tests

func TestParseLines(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected []string
	}{
		{
			name:     "simple lines",
			input:    []byte("line1\nline2\nline3"),
			expected: []string{"line1", "line2", "line3"},
		},
		{
			name:     "with empty lines",
			input:    []byte("line1\n\nline2\n\n\nline3"),
			expected: []string{"line1", "line2", "line3"},
		},
		{
			name:     "with whitespace",
			input:    []byte("  line1  \n\tline2\t\n  line3  "),
			expected: []string{"line1", "line2", "line3"},
		},
		{
			name:     "empty input",
			input:    []byte(""),
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseLines(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("len = %d, want %d", len(result), len(tt.expected))
				return
			}
			for i, v := range result {
				if v != tt.expected[i] {
					t.Errorf("result[%d] = %q, want %q", i, v, tt.expected[i])
				}
			}
		})
	}
}

func TestUniqueStrings(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "with duplicates",
			input:    []string{"a", "b", "a", "c", "b"},
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "no duplicates",
			input:    []string{"a", "b", "c"},
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "empty",
			input:    []string{},
			expected: nil,
		},
		{
			name:     "all same",
			input:    []string{"a", "a", "a"},
			expected: []string{"a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := uniqueStrings(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("len = %d, want %d", len(result), len(tt.expected))
				return
			}
			for i, v := range result {
				if v != tt.expected[i] {
					t.Errorf("result[%d] = %q, want %q", i, v, tt.expected[i])
				}
			}
		})
	}
}

func TestTruncateGen(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		max      int
		expected string
	}{
		{"short", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"truncate", "hello world", 5, "hello"},
		{"empty", "", 5, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateGen(tt.input, tt.max)
			if result != tt.expected {
				t.Errorf("truncateGen(%q, %d) = %q, want %q", tt.input, tt.max, result, tt.expected)
			}
		})
	}
}

func TestSummarizeToolCalls(t *testing.T) {
	tests := []struct {
		name     string
		calls    []string
		expected string
	}{
		{
			name:     "mixed calls",
			calls:    []string{"Read", "Edit", "Read", "Write", "Edit", "Edit"},
			expected: "Edit:3,Read:2,Write:1",
		},
		{
			name:     "single call",
			calls:    []string{"Read"},
			expected: "Read:1",
		},
		{
			name:     "empty",
			calls:    []string{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := summarizeToolCalls(tt.calls)
			if result != tt.expected {
				t.Errorf("summarizeToolCalls = %q, want %q", result, tt.expected)
			}
		})
	}
}

// Benchmark tests

func BenchmarkAnalyzeOutput(b *testing.B) {
	g := NewGenerator("/tmp")
	output := []byte(`
I've completed implementing the authentication module. This included:
- Added JWT token validation
- Implemented refresh token flow
- Added rate limiting

Successfully migrated the database schema.

Next: Add comprehensive unit tests for the login flow

TODO: Update the API documentation

Error: Some edge case failed during testing
Failed: One integration test is flaky
	`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.analyzeOutput(output)
	}
}

// =============================================================================
// GenerateHandoff Tests
// =============================================================================

func TestGenerateHandoffBasic(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewGenerator(tmpDir)
	ctx := context.Background()

	opts := GenerateHandoffOptions{
		SessionName: "test-session",
		AgentName:   "test-agent",
		AgentType:   "cc",
		PaneID:      "test__cc_1",
		Goal:        "Implemented feature X",
		Now:         "Write tests next",
	}

	h, err := g.GenerateHandoff(ctx, opts)
	if err != nil {
		t.Fatalf("GenerateHandoff failed: %v", err)
	}

	// Verify basic fields
	if h.Session != "test-session" {
		t.Errorf("Session = %q, want test-session", h.Session)
	}
	if h.Goal != "Implemented feature X" {
		t.Errorf("Goal = %q, want 'Implemented feature X'", h.Goal)
	}
	if h.Now != "Write tests next" {
		t.Errorf("Now = %q, want 'Write tests next'", h.Now)
	}
	if h.AgentType != "cc" {
		t.Errorf("AgentType = %q, want 'cc'", h.AgentType)
	}
	if h.PaneID != "test__cc_1" {
		t.Errorf("PaneID = %q, want 'test__cc_1'", h.PaneID)
	}

	// With explicit goal, should be marked complete
	if h.Status != StatusComplete {
		t.Errorf("Status = %q, want %q", h.Status, StatusComplete)
	}
}

func TestGenerateHandoffWithOutputAnalysis(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewGenerator(tmpDir)
	ctx := context.Background()

	opts := GenerateHandoffOptions{
		SessionName: "test-session",
		Output:      []byte("I've completed implementing the auth module.\nNext: add tests"),
	}

	h, err := g.GenerateHandoff(ctx, opts)
	if err != nil {
		t.Fatalf("GenerateHandoff failed: %v", err)
	}

	// Should extract goal from output
	if h.Goal != "implementing the auth module" {
		t.Errorf("Goal = %q, want 'implementing the auth module'", h.Goal)
	}
}

func TestGenerateHandoffExplicitGoalOverridesAnalysis(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewGenerator(tmpDir)
	ctx := context.Background()

	opts := GenerateHandoffOptions{
		SessionName: "test-session",
		Goal:        "Explicit goal",
		Output:      []byte("I've completed implementing something else.\nNext: do stuff"),
	}

	h, err := g.GenerateHandoff(ctx, opts)
	if err != nil {
		t.Fatalf("GenerateHandoff failed: %v", err)
	}

	// Explicit goal should override analysis
	if h.Goal != "Explicit goal" {
		t.Errorf("Goal = %q, want 'Explicit goal'", h.Goal)
	}
}

func TestGenerateHandoffWithTokenInfo(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewGenerator(tmpDir)
	ctx := context.Background()

	opts := GenerateHandoffOptions{
		SessionName: "test-session",
		Goal:        "Test goal",
		Now:         "Test now",
		TokensUsed:  80000,
		TokensMax:   100000,
	}

	h, err := g.GenerateHandoff(ctx, opts)
	if err != nil {
		t.Fatalf("GenerateHandoff failed: %v", err)
	}

	if h.TokensUsed != 80000 {
		t.Errorf("TokensUsed = %d, want 80000", h.TokensUsed)
	}
	if h.TokensMax != 100000 {
		t.Errorf("TokensMax = %d, want 100000", h.TokensMax)
	}
	if h.TokensPct != 80.0 {
		t.Errorf("TokensPct = %f, want 80.0", h.TokensPct)
	}
}

func TestGenerateHandoffDisableIntegrations(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewGenerator(tmpDir)
	ctx := context.Background()

	falseVal := false
	opts := GenerateHandoffOptions{
		SessionName:      "test-session",
		AgentName:        "test-agent",
		Goal:             "Test goal",
		Now:              "Test now",
		IncludeBeads:     &falseVal,
		IncludeAgentMail: &falseVal,
	}

	h, err := g.GenerateHandoff(ctx, opts)
	if err != nil {
		t.Fatalf("GenerateHandoff failed: %v", err)
	}

	// Should not have beads or agent mail data
	if len(h.ActiveBeads) > 0 {
		t.Errorf("expected no active beads when disabled, got %d", len(h.ActiveBeads))
	}
	if len(h.AgentMailThreads) > 0 {
		t.Errorf("expected no agent mail threads when disabled, got %d", len(h.AgentMailThreads))
	}
}

func TestGenerateHandoffStatusInference(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewGenerator(tmpDir)
	ctx := context.Background()

	tests := []struct {
		name        string
		output      []byte
		goal        string
		wantStatus  string
		wantOutcome string
	}{
		{
			name:        "complete with goal",
			goal:        "Completed task",
			wantStatus:  StatusComplete,
			wantOutcome: OutcomeSucceeded,
		},
		{
			name:        "blocked with errors",
			output:      []byte("Error: something failed\nFailed: another thing"),
			wantStatus:  StatusBlocked,
			wantOutcome: OutcomePartialMinus,
		},
		{
			name:        "partial without goal",
			output:      []byte("Did some work but no completion marker"),
			wantStatus:  StatusPartial,
			wantOutcome: OutcomePartialPlus,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			falseVal := false
			opts := GenerateHandoffOptions{
				SessionName:      "test",
				Goal:             tt.goal,
				Output:           tt.output,
				IncludeBeads:     &falseVal,
				IncludeAgentMail: &falseVal,
			}

			h, err := g.GenerateHandoff(ctx, opts)
			if err != nil {
				t.Fatalf("GenerateHandoff failed: %v", err)
			}

			if h.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", h.Status, tt.wantStatus)
			}
			if h.Outcome != tt.wantOutcome {
				t.Errorf("Outcome = %q, want %q", h.Outcome, tt.wantOutcome)
			}
		})
	}
}

func TestGenerateHandoffJSONSerializable(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewGenerator(tmpDir)
	ctx := context.Background()

	falseVal := false
	opts := GenerateHandoffOptions{
		SessionName:      "test-session",
		AgentName:        "test-agent",
		AgentType:        "cc",
		Goal:             "Implemented feature",
		Now:              "Write tests",
		TokensUsed:       50000,
		TokensMax:        100000,
		IncludeBeads:     &falseVal,
		IncludeAgentMail: &falseVal,
	}

	h, err := g.GenerateHandoff(ctx, opts)
	if err != nil {
		t.Fatalf("GenerateHandoff failed: %v", err)
	}

	// Add some data to verify serialization
	h.AddDecision("architecture", "microservices")
	h.AddFinding("bottleneck", "database")
	h.ActiveBeads = []string{"bd-1234: Test bead"}
	h.AgentMailThreads = []string{"[thread-1] Test message (from: agent-a)"}

	// Should serialize to JSON without error
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}

	// Verify it's valid JSON by unmarshaling
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	// Verify key fields are present (Go's json.Marshal uses field names when no json tags)
	// The struct uses yaml tags, so json.Marshal uses exported field names: Session, Goal, etc.
	if parsed["Session"] != "test-session" {
		t.Errorf("Session = %v, want test-session", parsed["Session"])
	}
	if parsed["Goal"] != "Implemented feature" {
		t.Errorf("Goal = %v, want 'Implemented feature'", parsed["Goal"])
	}

	// Also verify JSON is non-empty and has expected structure
	if len(data) < 100 {
		t.Errorf("JSON output too short: %d bytes", len(data))
	}
}

func TestGenerateHandoffOptionsDefault(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewGenerator(tmpDir)
	ctx := context.Background()

	// Minimal options - should still work
	opts := GenerateHandoffOptions{
		SessionName: "minimal-session",
	}

	h, err := g.GenerateHandoff(ctx, opts)
	if err != nil {
		t.Fatalf("GenerateHandoff failed: %v", err)
	}

	if h.Session != "minimal-session" {
		t.Errorf("Session = %q, want minimal-session", h.Session)
	}

	// Should default to partial status when no goal
	if h.Status != StatusPartial {
		t.Errorf("Status = %q, want %q", h.Status, StatusPartial)
	}
}

func TestFetchAgentMailThreadsPrefersNewestUniqueThreadEntry(t *testing.T) {
	g := NewGenerator(t.TempDir())
	threadID := "thread-1"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      interface{}     `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		result, err := json.Marshal(map[string]interface{}{
			"result": []agentmail.InboxMessage{
				{
					ID:         1,
					Subject:    "Old status",
					From:       "BlueLake",
					ThreadID:   &threadID,
					CreatedTS:  agentmail.FlexTime{Time: time.Date(2026, 3, 31, 17, 0, 0, 0, time.UTC)},
					Importance: "normal",
				},
				{
					ID:         2,
					Subject:    "Second thread",
					From:       "GreenStone",
					CreatedTS:  agentmail.FlexTime{Time: time.Date(2026, 3, 31, 18, 0, 0, 0, time.UTC)},
					Importance: "normal",
				},
				{
					ID:         3,
					Subject:    "Latest status",
					From:       "BlueLake",
					ThreadID:   &threadID,
					CreatedTS:  agentmail.FlexTime{Time: time.Date(2026, 3, 31, 19, 0, 0, 0, time.UTC)},
					Importance: "URGENT",
				},
			},
		})
		if err != nil {
			t.Fatalf("marshal response: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(agentmail.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  result,
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer server.Close()

	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
	threads, err := g.fetchAgentMailThreads(context.Background(), client, "/data/projects/ntm", "BlueLake")
	if err != nil {
		t.Fatalf("fetchAgentMailThreads: %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("len(threads) = %d, want 2 (%v)", len(threads), threads)
	}
	if got := threads[0]; got != "⚠️ [thread-1] Latest status (from: BlueLake)" {
		t.Fatalf("threads[0] = %q, want newest urgent thread entry", got)
	}
	if got := threads[1]; got != "Second thread (from: GreenStone)" {
		t.Fatalf("threads[1] = %q, want second newest distinct thread", got)
	}
}

func TestGetInProgressBeadsNoBeads(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewGenerator(tmpDir)

	// In temp dir with no beads, should return nil
	beads, err := g.getInProgressBeads("")
	if err != nil {
		t.Fatalf("getInProgressBeads failed: %v", err)
	}

	// br not available or no beads - should return nil
	if len(beads) > 0 {
		t.Logf("beads found (may be expected in dev environment): %v", beads)
	}
}

func BenchmarkGenerateFromOutput(b *testing.B) {
	tmpDir := b.TempDir()
	g := NewGenerator(tmpDir)
	output := []byte("I've completed implementing the feature.\nNext: write tests")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = g.GenerateFromOutput("bench-session", output)
	}
}

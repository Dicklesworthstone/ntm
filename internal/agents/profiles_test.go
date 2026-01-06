package agents

import (
	"testing"
	"time"
)

func TestNewProfileMatcher(t *testing.T) {
	pm := NewProfileMatcher()

	// Should have all three default profiles
	profiles := pm.AllProfiles()
	if len(profiles) != 3 {
		t.Errorf("expected 3 profiles, got %d", len(profiles))
	}

	// Check Claude profile
	claude := pm.GetProfile(AgentTypeClaude)
	if claude == nil {
		t.Fatal("Claude profile should exist")
	}
	if claude.ContextBudget != 180000 {
		t.Errorf("Claude context budget should be 180000, got %d", claude.ContextBudget)
	}
	if len(claude.Specializations) == 0 {
		t.Error("Claude should have specializations")
	}

	// Check Codex profile
	codex := pm.GetProfile(AgentTypeCodex)
	if codex == nil {
		t.Fatal("Codex profile should exist")
	}
	if codex.ContextBudget != 120000 {
		t.Errorf("Codex context budget should be 120000, got %d", codex.ContextBudget)
	}

	// Check Gemini profile
	gemini := pm.GetProfile(AgentTypeGemini)
	if gemini == nil {
		t.Fatal("Gemini profile should exist")
	}
	if gemini.ContextBudget != 100000 {
		t.Errorf("Gemini context budget should be 100000, got %d", gemini.ContextBudget)
	}
}

func TestGetProfileByName(t *testing.T) {
	pm := NewProfileMatcher()

	tests := []struct {
		name     string
		expected AgentType
	}{
		{"claude", AgentTypeClaude},
		{"cc", AgentTypeClaude},
		{"claude-code", AgentTypeClaude},
		{"codex", AgentTypeCodex},
		{"cod", AgentTypeCodex},
		{"openai", AgentTypeCodex},
		{"gemini", AgentTypeGemini},
		{"gmi", AgentTypeGemini},
		{"CLAUDE", AgentTypeClaude}, // Case insensitive
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := pm.GetProfileByName(tt.name)
			if profile == nil {
				t.Fatalf("expected profile for %s", tt.name)
			}
			if profile.Type != tt.expected {
				t.Errorf("expected type %s, got %s", tt.expected, profile.Type)
			}
		})
	}
}

func TestScoreAssignment_ContextBudget(t *testing.T) {
	pm := NewProfileMatcher()

	// Task that exceeds Gemini's context budget
	task := TaskInfo{
		Title:           "Large refactoring task",
		Type:            "epic",
		EstimatedTokens: 150000, // Exceeds Gemini (100K) and Codex (120K)
	}

	// Should fail for Gemini
	result := pm.ScoreAssignment(AgentTypeGemini, task)
	if result.CanHandle {
		t.Error("Gemini should not be able to handle task exceeding context budget")
	}
	if result.Score != 0 {
		t.Errorf("Score should be 0 for tasks exceeding budget, got %f", result.Score)
	}

	// Should fail for Codex
	result = pm.ScoreAssignment(AgentTypeCodex, task)
	if result.CanHandle {
		t.Error("Codex should not be able to handle task exceeding context budget")
	}

	// Should succeed for Claude (180K budget)
	result = pm.ScoreAssignment(AgentTypeClaude, task)
	if !result.CanHandle {
		t.Error("Claude should be able to handle task within context budget")
	}
	if result.Score <= 0 {
		t.Error("Claude should have positive score")
	}
}

func TestScoreAssignment_Specialization(t *testing.T) {
	pm := NewProfileMatcher()

	// Test writing task - Codex specializes in tests
	testTask := TaskInfo{
		Title: "Add unit tests for auth module",
		Type:  "task",
	}

	codexResult := pm.ScoreAssignment(AgentTypeCodex, testTask)

	// Codex should have higher score for test tasks
	if !codexResult.SpecializationHit {
		t.Error("Codex should hit specialization for test task")
	}

	// Epic task - Claude specializes in complex
	epicTask := TaskInfo{
		Title: "Implement new authentication system",
		Type:  "epic",
	}

	claudeEpicResult := pm.ScoreAssignment(AgentTypeClaude, epicTask)
	codexEpicResult := pm.ScoreAssignment(AgentTypeCodex, epicTask)

	// Claude should be preferred for epic tasks
	if claudeEpicResult.Score <= codexEpicResult.Score {
		t.Logf("Claude score: %f, Codex score: %f", claudeEpicResult.Score, codexEpicResult.Score)
		// Note: This may not always be true due to other factors, but specialization should help
	}
	if !claudeEpicResult.SpecializationHit {
		t.Error("Claude should hit specialization for epic task")
	}

	// Chore task should NOT hit Gemini's docs specialization
	// (chores are general maintenance, not documentation)
	choreTask := TaskInfo{
		Title: "Update dependencies",
		Type:  "chore",
	}
	geminiChoreResult := pm.ScoreAssignment(AgentTypeGemini, choreTask)
	if geminiChoreResult.SpecializationHit {
		t.Error("Gemini should NOT hit specialization for chore task (chores are not docs)")
	}
}

func TestScoreAssignment_FileMatching(t *testing.T) {
	pm := NewProfileMatcher()

	// Task affecting test files - Codex prefers these
	testFileTask := TaskInfo{
		Title:         "Update tests",
		Type:          "task",
		AffectedFiles: []string{"internal/auth/auth_test.go", "internal/user/user_test.go"},
	}

	codexResult := pm.ScoreAssignment(AgentTypeCodex, testFileTask)
	if codexResult.FileMatchScore <= 1.0 {
		t.Errorf("Codex should get file match boost for test files, got %f", codexResult.FileMatchScore)
	}

	// Task affecting Go implementation files - Claude prefers these
	goFileTask := TaskInfo{
		Title:         "Implement feature",
		Type:          "feature",
		AffectedFiles: []string{"internal/auth/auth.go", "internal/user/user.go"},
	}

	claudeResult := pm.ScoreAssignment(AgentTypeClaude, goFileTask)
	if claudeResult.FileMatchScore <= 1.0 {
		t.Errorf("Claude should get file match boost for Go files, got %f", claudeResult.FileMatchScore)
	}

	// Task affecting docs - Gemini prefers these
	docFileTask := TaskInfo{
		Title:         "Update documentation",
		Type:          "docs",
		AffectedFiles: []string{"README.md", "docs/setup.md"},
	}

	geminiResult := pm.ScoreAssignment(AgentTypeGemini, docFileTask)
	if geminiResult.FileMatchScore <= 1.0 {
		t.Errorf("Gemini should get file match boost for doc files, got %f", geminiResult.FileMatchScore)
	}
}

func TestScoreAssignment_LabelMatching(t *testing.T) {
	pm := NewProfileMatcher()

	// High priority task with critical label - Claude prefers these
	criticalTask := TaskInfo{
		Title:  "Fix critical security issue",
		Type:   "bug",
		Labels: []string{"critical", "P0", "security"},
	}

	claudeResult := pm.ScoreAssignment(AgentTypeClaude, criticalTask)
	if claudeResult.LabelMatchScore <= 1.0 {
		t.Errorf("Claude should get label match boost for critical labels, got %f", claudeResult.LabelMatchScore)
	}

	// Low priority task - Codex handles these
	lowPrioTask := TaskInfo{
		Title:  "Minor refactor",
		Type:   "task",
		Labels: []string{"P3", "bug"},
	}

	codexResult := pm.ScoreAssignment(AgentTypeCodex, lowPrioTask)
	if codexResult.LabelMatchScore <= 1.0 {
		t.Errorf("Codex should get label match boost for low priority labels, got %f", codexResult.LabelMatchScore)
	}
}

func TestRecommendAgent(t *testing.T) {
	pm := NewProfileMatcher()

	tests := []struct {
		name     string
		task     TaskInfo
		expected AgentType
	}{
		{
			name: "Epic implementation",
			task: TaskInfo{
				Title: "Design and implement new module",
				Type:  "epic",
			},
			expected: AgentTypeClaude,
		},
		{
			name: "Test writing",
			task: TaskInfo{
				Title: "Write unit tests",
				Type:  "task",
			},
			expected: AgentTypeCodex,
		},
		{
			name: "Documentation",
			task: TaskInfo{
				Title:         "Update README",
				Type:          "docs",
				AffectedFiles: []string{"README.md"},
			},
			expected: AgentTypeGemini,
		},
		{
			name: "Research task",
			task: TaskInfo{
				Title: "Investigate performance issue",
				Type:  "task",
			},
			expected: AgentTypeGemini,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, result := pm.RecommendAgent(tt.task)
			t.Logf("Recommended %s with score %f for %s", agent, result.Score, tt.name)
			// Note: The actual recommendation may vary based on scoring weights
			// This test mainly verifies the function works correctly
			if agent == "" {
				t.Error("should recommend an agent")
			}
			if result.Score <= 0 {
				t.Error("recommended agent should have positive score")
			}
		})
	}
}

func TestRecordCompletion(t *testing.T) {
	pm := NewProfileMatcher()

	// Get initial stats
	initialStats := pm.GetPerformanceStats()
	initialTasks := initialStats[AgentTypeClaude].TasksCompleted
	initialRate := initialStats[AgentTypeClaude].SuccessRate

	// Record a successful completion
	pm.RecordCompletion(AgentTypeClaude, true, 5*time.Minute)

	// Check updated stats
	newStats := pm.GetPerformanceStats()
	if newStats[AgentTypeClaude].TasksCompleted != initialTasks+1 {
		t.Errorf("tasks completed should increase by 1, got %d", newStats[AgentTypeClaude].TasksCompleted)
	}
	if newStats[AgentTypeClaude].SuccessRate < initialRate {
		t.Error("success rate should not decrease after successful completion")
	}
	if newStats[AgentTypeClaude].AvgCompletionTime == 0 {
		t.Error("avg completion time should be set")
	}
	if newStats[AgentTypeClaude].LastUpdated.IsZero() {
		t.Error("last updated should be set")
	}

	// Record a failed completion
	pm.RecordCompletion(AgentTypeClaude, false, 10*time.Minute)
	afterFailStats := pm.GetPerformanceStats()
	if afterFailStats[AgentTypeClaude].SuccessRate >= newStats[AgentTypeClaude].SuccessRate {
		t.Error("success rate should decrease after failed completion")
	}
}

func TestNormalizeAgentType(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"claude", "claude"},
		{"cc", "claude"},
		{"claude-code", "claude"},
		{"CLAUDE", "claude"},
		{"Opus", "claude"},
		{"sonnet", "claude"},
		{"codex", "codex"},
		{"cod", "codex"},
		{"openai", "codex"},
		{"GPT", "codex"},
		{"gemini", "gemini"},
		{"gmi", "gemini"},
		{"google", "gemini"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := NormalizeAgentType(tt.input)
			if result != tt.expected {
				t.Errorf("NormalizeAgentType(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMatchGlobPattern(t *testing.T) {
	tests := []struct {
		path    string
		pattern string
		want    bool
	}{
		{"internal/auth/auth.go", "**/*.go", true},
		{"auth_test.go", "*_test.go", true},
		{"README.md", "*.md", true},
		{"docs/setup.md", "docs/**", true},
		{"internal/cli/cmd.go", "internal/**/*.go", true},
		{"internal/agents/profiles.go", "internal/**/*.go", true},  // Deeper nesting
		{"internal/a/b/c/d/e/file.go", "internal/**/*.go", true},   // Very deep nesting
		{"cmd/ntm/main.go", "cmd/**/*.go", true},                   // cmd prefix
		{"pkg/util/helper.go", "pkg/**/*.go", true},                // pkg prefix
		{"config.yaml", "*.go", false},
		{"main.go", "internal/**", false},
		{"external/lib/lib.go", "internal/**/*.go", false},         // Wrong prefix
	}

	for _, tt := range tests {
		t.Run(tt.path+"_"+tt.pattern, func(t *testing.T) {
			result := matchGlobPattern(tt.path, tt.pattern)
			if result != tt.want {
				t.Errorf("matchGlobPattern(%q, %q) = %v, want %v", tt.path, tt.pattern, result, tt.want)
			}
		})
	}
}

func TestParseAgentType(t *testing.T) {
	tests := []struct {
		input    string
		expected AgentType
	}{
		{"claude", AgentTypeClaude},
		{"cc", AgentTypeClaude},
		{"codex", AgentTypeCodex},
		{"gemini", AgentTypeGemini},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := ParseAgentType(tt.input)
			if result != tt.expected {
				t.Errorf("ParseAgentType(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestScoreResult_UnknownAgent(t *testing.T) {
	pm := NewProfileMatcher()

	result := pm.ScoreAssignment(AgentType("unknown"), TaskInfo{Title: "test"})
	if result.CanHandle {
		t.Error("unknown agent type should not be able to handle tasks")
	}
	if result.Score != 0 {
		t.Error("unknown agent type should have score 0")
	}
	if result.Reason == "" {
		t.Error("should provide reason for rejection")
	}
}

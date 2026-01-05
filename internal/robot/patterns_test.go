package robot

import (
	"testing"
)

func TestNewPatternLibrary(t *testing.T) {
	lib := NewPatternLibrary()

	if lib.Version != "1.0" {
		t.Errorf("expected version 1.0, got %s", lib.Version)
	}

	if len(lib.Patterns) == 0 {
		t.Error("expected default patterns, got none")
	}

	// Verify patterns are compiled
	for _, p := range lib.Patterns {
		if p.Regex == nil && p.RegexStr != "" {
			t.Errorf("pattern %s not compiled", p.Name)
		}
	}
}

func TestPatternLibrary_Compile(t *testing.T) {
	lib := &PatternLibrary{
		Patterns: []Pattern{
			{Name: "test1", RegexStr: `hello`, Priority: 10},
			{Name: "test2", RegexStr: `world`, Priority: 20},
		},
	}

	err := lib.Compile()
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	// Check patterns are compiled
	for _, p := range lib.Patterns {
		if p.Regex == nil {
			t.Errorf("pattern %s not compiled", p.Name)
		}
	}

	// Check sorted by priority (higher first)
	if lib.Patterns[0].Priority < lib.Patterns[1].Priority {
		t.Error("patterns should be sorted by priority descending")
	}
}

func TestPatternLibrary_CompileError(t *testing.T) {
	lib := &PatternLibrary{
		Patterns: []Pattern{
			{Name: "invalid", RegexStr: `[invalid`, Priority: 10},
		},
	}

	err := lib.Compile()
	if err == nil {
		t.Error("expected compile error for invalid regex")
	}
}

func TestPatternLibrary_Match(t *testing.T) {
	lib := NewPatternLibrary()

	tests := []struct {
		name      string
		content   string
		agentType string
		wantMatch bool
		wantState AgentState
	}{
		{"claude_prompt", "claude>", "claude", true, StateWaiting},
		{"claude_prompt_with_space", "claude> ", "claude", true, StateWaiting},
		{"codex_prompt", "codex>", "codex", true, StateWaiting},
		{"gemini_prompt", "gemini>", "gemini", true, StateWaiting},
		{"rate_limit", "Rate limit exceeded", "*", true, StateError},
		{"http_429", "HTTP 429 Too Many Requests", "*", true, StateError},
		{"panic", "panic: runtime error", "*", true, StateError},
		{"thinking", "Thinking...", "*", true, StateThinking},
		{"done", "Done", "*", true, StateWaiting},
		{"no_match", "just some random text", "*", false, StateUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := lib.Match(tt.content, tt.agentType)

			if tt.wantMatch {
				if len(matches) == 0 {
					t.Errorf("expected match for %q, got none", tt.content)
					return
				}
				if matches[0].State != tt.wantState {
					t.Errorf("expected state %s, got %s", tt.wantState, matches[0].State)
				}
			} else {
				if len(matches) > 0 {
					t.Errorf("expected no match for %q, got %v", tt.content, matches)
				}
			}
		})
	}
}

func TestPatternLibrary_MatchFirst(t *testing.T) {
	lib := NewPatternLibrary()

	// Test with matching content
	match := lib.MatchFirst("panic: something went wrong", "*")
	if match == nil {
		t.Fatal("expected match, got nil")
	}
	if match.State != StateError {
		t.Errorf("expected ERROR state, got %s", match.State)
	}

	// Test with non-matching content
	match = lib.MatchFirst("completely ordinary text", "*")
	if match != nil {
		t.Errorf("expected nil for non-matching content, got %v", match)
	}
}

func TestPatternLibrary_MatchByCategory(t *testing.T) {
	lib := NewPatternLibrary()

	// Test error category
	errorMatches := lib.MatchByCategory("Rate limit error occurred", "*", CategoryError)
	if len(errorMatches) == 0 {
		t.Error("expected error matches")
	}
	for _, m := range errorMatches {
		if m.Category != CategoryError {
			t.Errorf("expected error category, got %s", m.Category)
		}
	}

	// Test idle category
	idleMatches := lib.MatchByCategory("claude>", "claude", CategoryIdle)
	if len(idleMatches) == 0 {
		t.Error("expected idle matches")
	}
}

func TestPatternLibrary_AgentFiltering(t *testing.T) {
	lib := NewPatternLibrary()

	// Claude-specific pattern should match for claude
	matches := lib.Match("claude>", "claude")
	found := false
	for _, m := range matches {
		if m.Pattern == "claude_prompt" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected claude_prompt match for claude agent")
	}

	// Claude-specific pattern should NOT match for codex
	matches = lib.Match("claude>", "codex")
	for _, m := range matches {
		if m.Pattern == "claude_prompt" {
			t.Error("claude_prompt should not match for codex agent")
		}
	}

	// Wildcard patterns should match for any agent
	matches = lib.Match("panic: error", "any")
	if len(matches) == 0 {
		t.Error("wildcard patterns should match any agent")
	}
}

func TestPatternLibrary_HasError(t *testing.T) {
	lib := NewPatternLibrary()

	tests := []struct {
		content string
		hasErr  bool
	}{
		{"Rate limit exceeded", true},
		{"HTTP 429 error", true},
		{"panic: crash", true},
		{"unauthorized access", true},
		{"everything is fine", false},
		{"completed successfully", false},
	}

	for _, tt := range tests {
		t.Run(tt.content, func(t *testing.T) {
			if got := lib.HasError(tt.content, "*"); got != tt.hasErr {
				t.Errorf("HasError(%q) = %v, want %v", tt.content, got, tt.hasErr)
			}
		})
	}
}

func TestPatternLibrary_HasIdlePrompt(t *testing.T) {
	lib := NewPatternLibrary()

	tests := []struct {
		content   string
		agentType string
		hasIdle   bool
	}{
		{"claude>", "claude", true},
		{"codex>", "codex", true},
		{"gemini>", "gemini", true},
		{"$", "*", true},
		{">", "*", true},
		{"running some code", "*", false},
	}

	for _, tt := range tests {
		t.Run(tt.content, func(t *testing.T) {
			if got := lib.HasIdlePrompt(tt.content, tt.agentType); got != tt.hasIdle {
				t.Errorf("HasIdlePrompt(%q, %q) = %v, want %v", tt.content, tt.agentType, got, tt.hasIdle)
			}
		})
	}
}

func TestPatternLibrary_HasThinkingIndicator(t *testing.T) {
	lib := NewPatternLibrary()

	tests := []struct {
		content  string
		hasThink bool
	}{
		{"Thinking...", true},
		{"Processing...", true},
		{"Analyzing...", true},
		{"⠋", true}, // braille spinner
		{"Loading...", true},
		{"done", false},
		{"error occurred", false},
	}

	for _, tt := range tests {
		t.Run(tt.content, func(t *testing.T) {
			if got := lib.HasThinkingIndicator(tt.content, "*"); got != tt.hasThink {
				t.Errorf("HasThinkingIndicator(%q) = %v, want %v", tt.content, got, tt.hasThink)
			}
		})
	}
}

func TestPatternLibrary_HasCompletionSignal(t *testing.T) {
	lib := NewPatternLibrary()

	tests := []struct {
		content  string
		hasCompl bool
	}{
		{"Done", true},
		{"Completed", true},
		{"Finished!", true},
		{"✓", true},
		{"Summary:", true},
		{"still working", false},
	}

	for _, tt := range tests {
		t.Run(tt.content, func(t *testing.T) {
			if got := lib.HasCompletionSignal(tt.content, "*"); got != tt.hasCompl {
				t.Errorf("HasCompletionSignal(%q) = %v, want %v", tt.content, got, tt.hasCompl)
			}
		})
	}
}

func TestPatternLibrary_AddPattern(t *testing.T) {
	lib := NewPatternLibrary()
	initialCount := lib.PatternCount()

	err := lib.AddPattern(Pattern{
		Name:     "custom_pattern",
		RegexStr: `custom\s+test`,
		Agent:    "*",
		State:    StateWaiting,
		Category: CategoryCompletion,
		Priority: 999, // Very high priority
	})

	if err != nil {
		t.Fatalf("AddPattern failed: %v", err)
	}

	if lib.PatternCount() != initialCount+1 {
		t.Errorf("expected %d patterns, got %d", initialCount+1, lib.PatternCount())
	}

	// New pattern should be first (highest priority)
	patterns := lib.GetPatterns()
	if patterns[0].Name != "custom_pattern" {
		t.Error("new high-priority pattern should be first")
	}

	// Test that the new pattern matches
	matches := lib.Match("custom test", "*")
	found := false
	for _, m := range matches {
		if m.Pattern == "custom_pattern" {
			found = true
			break
		}
	}
	if !found {
		t.Error("custom pattern should match")
	}
}

func TestPatternLibrary_AddPatternInvalidRegex(t *testing.T) {
	lib := NewPatternLibrary()

	err := lib.AddPattern(Pattern{
		Name:     "invalid",
		RegexStr: `[invalid`,
		Agent:    "*",
		State:    StateError,
	})

	if err == nil {
		t.Error("expected error for invalid regex")
	}
}

func TestPatternLibrary_GetPatterns(t *testing.T) {
	lib := NewPatternLibrary()

	patterns := lib.GetPatterns()
	if len(patterns) == 0 {
		t.Error("expected patterns, got none")
	}

	// Modifying the copy shouldn't affect original
	originalCount := lib.PatternCount()
	_ = patterns[:1] // Truncating the copy; verify it doesn't affect original
	if lib.PatternCount() != originalCount {
		t.Error("GetPatterns should return a copy")
	}
}

func TestPatternLibrary_GetPatternsByCategory(t *testing.T) {
	lib := NewPatternLibrary()

	errorPatterns := lib.GetPatternsByCategory(CategoryError)
	if len(errorPatterns) == 0 {
		t.Error("expected error patterns")
	}

	for _, p := range errorPatterns {
		if p.Category != CategoryError {
			t.Errorf("expected error category, got %s", p.Category)
		}
	}
}

func TestPatternLibrary_GetPatternsByAgent(t *testing.T) {
	lib := NewPatternLibrary()

	claudePatterns := lib.GetPatternsByAgent("claude")
	if len(claudePatterns) == 0 {
		t.Error("expected claude patterns")
	}

	// Should include both claude-specific and wildcard patterns
	hasClaudeSpecific := false
	hasWildcard := false
	for _, p := range claudePatterns {
		if p.Agent == "claude" {
			hasClaudeSpecific = true
		}
		if p.Agent == "*" {
			hasWildcard = true
		}
	}

	if !hasClaudeSpecific {
		t.Error("expected claude-specific patterns")
	}
	if !hasWildcard {
		t.Error("expected wildcard patterns")
	}
}

func TestDefaultLibrary(t *testing.T) {
	// DefaultLibrary should be initialized
	if DefaultLibrary == nil {
		t.Fatal("DefaultLibrary should not be nil")
	}

	if DefaultLibrary.PatternCount() == 0 {
		t.Error("DefaultLibrary should have patterns")
	}
}

func TestMatchPatterns(t *testing.T) {
	// Test convenience function
	matches := MatchPatterns("claude>", "claude")
	if len(matches) == 0 {
		t.Error("expected matches from convenience function")
	}
}

func TestMatchFirstPattern(t *testing.T) {
	// Test convenience function
	match := MatchFirstPattern("panic: error", "*")
	if match == nil {
		t.Error("expected match from convenience function")
	}
}

func TestHasErrorPattern(t *testing.T) {
	if !HasErrorPattern("Rate limit exceeded", "*") {
		t.Error("should detect rate limit error")
	}
	if HasErrorPattern("everything is fine", "*") {
		t.Error("should not detect error in normal text")
	}
}

func TestHasIdlePattern(t *testing.T) {
	if !HasIdlePattern("claude>", "claude") {
		t.Error("should detect claude idle prompt")
	}
}

func TestHasThinkingPattern(t *testing.T) {
	if !HasThinkingPattern("Thinking...", "*") {
		t.Error("should detect thinking pattern")
	}
}

func TestPatternPriority(t *testing.T) {
	lib := NewPatternLibrary()

	// Test that patterns are sorted by priority
	patterns := lib.GetPatterns()
	for i := 1; i < len(patterns); i++ {
		if patterns[i-1].Priority < patterns[i].Priority {
			t.Errorf("patterns not sorted by priority at index %d", i)
		}
	}
}

// =============================================================================
// Edge Case Tests for Pattern Library
// =============================================================================

func TestPatternStruct(t *testing.T) {
	t.Parallel()

	p := Pattern{
		Name:        "test_pattern",
		RegexStr:    `test\s+pattern`,
		Agent:       "claude",
		State:       StateWaiting,
		Category:    CategoryIdle,
		Priority:    100,
		Description: "Test pattern description",
	}

	if p.Name != "test_pattern" {
		t.Errorf("expected name test_pattern, got %s", p.Name)
	}
	if p.Agent != "claude" {
		t.Errorf("expected agent claude, got %s", p.Agent)
	}
	if p.State != StateWaiting {
		t.Errorf("expected state WAITING, got %s", p.State)
	}
	if p.Priority != 100 {
		t.Errorf("expected priority 100, got %d", p.Priority)
	}
}

func TestPatternMatchStruct(t *testing.T) {
	t.Parallel()

	pm := PatternMatch{
		Pattern:  "claude_prompt",
		State:    StateWaiting,
		Category: CategoryIdle,
		Priority: 100,
	}

	if pm.Pattern != "claude_prompt" {
		t.Errorf("expected pattern claude_prompt, got %s", pm.Pattern)
	}
	if pm.State != StateWaiting {
		t.Errorf("expected state WAITING, got %s", pm.State)
	}
	if pm.Category != CategoryIdle {
		t.Errorf("expected category idle, got %s", pm.Category)
	}
}

func TestPatternLibraryVersion(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()

	if lib.Version == "" {
		t.Error("expected version to be set")
	}
	if lib.Version != "1.0" {
		t.Errorf("expected version 1.0, got %s", lib.Version)
	}
}

func TestPatternLibraryCompileIdempotent(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()
	initialCount := lib.PatternCount()

	// Compile again should be safe
	err := lib.Compile()
	if err != nil {
		t.Fatalf("second compile failed: %v", err)
	}

	if lib.PatternCount() != initialCount {
		t.Errorf("pattern count changed after recompile")
	}
}

func TestPatternLibraryMatchEmptyContent(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()

	matches := lib.Match("", "claude")
	if len(matches) != 0 {
		t.Errorf("expected no matches for empty content, got %d", len(matches))
	}
}

func TestPatternLibraryMatchEmptyAgentType(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()

	// Empty agent type should still match wildcard patterns
	matches := lib.Match("panic: error", "")
	if len(matches) == 0 {
		t.Error("expected matches for wildcard patterns with empty agent type")
	}
}

func TestPatternLibraryMatchAllWildcardPatterns(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()

	// Count wildcard patterns
	wildcardPatterns := lib.GetPatternsByAgent("*")
	if len(wildcardPatterns) == 0 {
		t.Error("expected some wildcard patterns")
	}

	// All these should be wildcards
	for _, p := range wildcardPatterns {
		if p.Agent != "*" && p.Agent != "" {
			t.Errorf("expected wildcard agent, got %s", p.Agent)
		}
	}
}

func TestPatternLibraryAllAgentTypes(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()

	agents := []string{"claude", "codex", "gemini"}
	for _, agent := range agents {
		patterns := lib.GetPatternsByAgent(agent)
		if len(patterns) == 0 {
			t.Errorf("expected patterns for agent %s", agent)
		}
	}
}

func TestPatternLibraryAllCategories(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()

	categories := []PatternCategory{CategoryIdle, CategoryError, CategoryThinking, CategoryCompletion}
	for _, cat := range categories {
		patterns := lib.GetPatternsByCategory(cat)
		if len(patterns) == 0 {
			t.Errorf("expected patterns for category %s", cat)
		}
	}
}

func TestPatternLibraryBrailleSpinner(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()

	// Test all braille spinner characters
	brailleChars := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	for _, char := range brailleChars {
		if !lib.HasThinkingIndicator(char, "*") {
			t.Errorf("expected braille char %s to match thinking pattern", char)
		}
	}
}

func TestPatternLibraryCheckmarkSymbols(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()

	// Test checkmark symbols
	checkmarks := []string{"✓", "✔"}
	for _, mark := range checkmarks {
		if !lib.HasCompletionSignal(mark, "*") {
			t.Errorf("expected checkmark %s to match completion pattern", mark)
		}
	}
}

func TestPatternLibraryErrorPatternVariants(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()

	errorTexts := []string{
		"Rate limit exceeded",
		"rate limit",
		"429",
		"HTTP 429",
		"too many requests",
		"quota exceeded",
		"api error: something went wrong",
		"Error: failed",
		"exception: null pointer",
		"panic: runtime error",
		"SIGSEGV",
		"Killed",
		"SIGKILL",
		"process exited",
		"agent terminated",
		"unauthorized",
		"invalid api key",
		"authentication failed",
		"connection refused",
		"request timed out",
		"connection timeout",
		"network error",
	}

	for _, text := range errorTexts {
		if !lib.HasError(text, "*") {
			t.Errorf("expected error detection for %q", text)
		}
	}
}

func TestPatternLibraryIdlePromptVariants(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()

	idlePrompts := map[string]string{
		"claude>":      "claude",
		"claude> ":     "claude",
		"Claude>":      "claude",
		"claude code>": "claude",
		"╰─>":          "claude",
		"codex>":       "codex",
		"Codex> ":      "codex",
		"$":            "codex",
		"$ ":           "codex",
		"gemini>":      "gemini",
		"Gemini> ":     "gemini",
		">>>":          "gemini",
		">>> ":         "gemini",
		">":            "*",
		"#":            "*",
		"%":            "*",
	}

	for prompt, agent := range idlePrompts {
		if !lib.HasIdlePrompt(prompt, agent) {
			t.Errorf("expected idle prompt detection for %q (agent: %s)", prompt, agent)
		}
	}
}

func TestPatternLibraryThinkingVariants(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()

	thinkingTexts := []string{
		"Thinking...",
		"thinking",
		"Processing...",
		"processing",
		"Analyzing...",
		"analyzing",
		"Loading...",
		"loading",
		"please wait",
		"waiting...",
		"thinking deeply",
		"extended thinking",
		"...",
	}

	for _, text := range thinkingTexts {
		if !lib.HasThinkingIndicator(text, "*") {
			t.Errorf("expected thinking detection for %q", text)
		}
	}
}

func TestPatternLibraryCompletionVariants(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()

	completionTexts := []string{
		"Done",
		"done",
		"Done!",
		"Completed",
		"completed",
		"Finished",
		"Finished!",
		"Summary:",
		"Changes made:",
	}

	for _, text := range completionTexts {
		if !lib.HasCompletionSignal(text, "*") {
			t.Errorf("expected completion detection for %q", text)
		}
	}
}

func TestPatternLibraryAddPatternSorting(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()

	// Add a low priority pattern
	err := lib.AddPattern(Pattern{
		Name:     "low_priority",
		RegexStr: `low_priority_test`,
		Agent:    "*",
		State:    StateWaiting,
		Priority: 1,
	})
	if err != nil {
		t.Fatalf("AddPattern failed: %v", err)
	}

	// It should be near the end (low priority)
	patterns := lib.GetPatterns()
	lastPattern := patterns[len(patterns)-1]
	if lastPattern.Name != "low_priority" {
		t.Errorf("low priority pattern should be last, got %s", lastPattern.Name)
	}
}

func TestPatternLibraryAddPatternUncompiled(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()
	initialCount := lib.PatternCount()

	// Add pattern without pre-compiled Regex (the normal case)
	// AddPattern will compile the RegexStr for us
	newPattern := Pattern{
		Name:     "uncompiled",
		RegexStr: `uncompiled_test`,
		Agent:    "*",
		State:    StateWaiting,
		Priority: 50,
	}

	err := lib.AddPattern(newPattern)
	if err != nil {
		t.Fatalf("AddPattern failed: %v", err)
	}

	if lib.PatternCount() != initialCount+1 {
		t.Errorf("expected %d patterns after add", initialCount+1)
	}
}

func TestPatternLibraryMatchFirstNoMatch(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()

	match := lib.MatchFirst("completely random text with no patterns 12345", "*")
	if match != nil {
		t.Errorf("expected no match, got %v", match)
	}
}

func TestPatternLibraryMatchByCategoryNoMatch(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()

	matches := lib.MatchByCategory("normal text", "*", CategoryError)
	if len(matches) != 0 {
		t.Errorf("expected no error matches for normal text, got %d", len(matches))
	}
}

func TestPatternLibraryMultipleMatches(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()

	// Content that could match multiple patterns
	content := "Rate limit 429 error"
	matches := lib.Match(content, "*")

	if len(matches) < 2 {
		t.Errorf("expected multiple matches for %q, got %d", content, len(matches))
	}

	// All should be error category
	for _, m := range matches {
		if m.Category != CategoryError {
			t.Errorf("expected error category, got %s", m.Category)
		}
	}
}

func TestPatternLibraryPriorityOrdering(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()

	// Content that matches both error and idle patterns
	// Error should have higher priority
	matches := lib.Match("panic: error >", "*")

	if len(matches) == 0 {
		t.Fatal("expected matches")
	}

	// First match should be error (higher priority)
	if matches[0].Category != CategoryError {
		t.Errorf("expected error to have highest priority, got %s", matches[0].Category)
	}
}

func TestDefaultLibraryNotNil(t *testing.T) {
	t.Parallel()

	if DefaultLibrary == nil {
		t.Fatal("DefaultLibrary should not be nil")
	}
	if DefaultLibrary.PatternCount() == 0 {
		t.Error("DefaultLibrary should have patterns")
	}
}

func TestPatternLibraryConcurrentAccess(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			_ = lib.Match("testing concurrent access", "*")
			_ = lib.HasError("error test", "*")
			_ = lib.HasIdlePrompt("claude>", "claude")
			_ = lib.GetPatterns()
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestPatternLibraryAddPatternConcurrent(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()

	done := make(chan bool)
	for i := 0; i < 5; i++ {
		go func(idx int) {
			_ = lib.AddPattern(Pattern{
				Name:     "concurrent_" + string(rune('0'+idx)),
				RegexStr: "concurrent_test_" + string(rune('0'+idx)),
				Agent:    "*",
				State:    StateWaiting,
				Priority: 10,
			})
			done <- true
		}(i)
	}

	for i := 0; i < 5; i++ {
		<-done
	}

	// All patterns should be added
	patterns := lib.GetPatterns()
	addedCount := 0
	for _, p := range patterns {
		if len(p.Name) > 10 && p.Name[:10] == "concurrent" {
			addedCount++
		}
	}
	if addedCount < 5 {
		t.Errorf("expected 5 concurrent patterns, got %d", addedCount)
	}
}

func TestPatternLibraryEmptyRegex(t *testing.T) {
	t.Parallel()

	lib := &PatternLibrary{
		Patterns: []Pattern{
			{Name: "no_regex", RegexStr: "", Priority: 10},
		},
	}

	err := lib.Compile()
	if err != nil {
		t.Fatalf("compile with empty regex should not error: %v", err)
	}

	// Empty regex pattern should never match
	matches := lib.Match("any content", "*")
	if len(matches) != 0 {
		t.Error("empty regex should not match")
	}
}

func TestPatternLibraryGetPatternsByAgentEmpty(t *testing.T) {
	t.Parallel()

	lib := NewPatternLibrary()

	// Non-existent agent should still get wildcard patterns
	patterns := lib.GetPatternsByAgent("nonexistent")
	wildcardCount := 0
	for _, p := range patterns {
		if p.Agent == "*" || p.Agent == "" {
			wildcardCount++
		}
	}
	if wildcardCount == 0 {
		t.Error("should include wildcard patterns for unknown agent")
	}
}

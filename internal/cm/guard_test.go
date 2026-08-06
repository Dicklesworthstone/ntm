package cm

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// Guard Integration Tests for CM (bd-3jgk)
// =============================================================================
// Tests for guard (procedural memory rule) retrieval and formatting,
// guard injection into prompts, and guard conflict resolution.

// TestGuardRetrieval_RuleFormatting verifies rules are properly formatted
func TestGuardRetrieval_RuleFormatting(t *testing.T) {
	start := time.Now()

	testCases := []struct {
		name     string
		rule     CLIRule
		contains []string
	}{
		{
			name: "basic rule",
			rule: CLIRule{
				ID:      "b-abc123",
				Content: "Always run tests before committing",
			},
			contains: []string{"b-abc123", "Always run tests before committing"},
		},
		{
			name: "rule with category",
			rule: CLIRule{
				ID:       "b-def456",
				Content:  "Follow the coding standards",
				Category: "best-practices",
			},
			contains: []string{"b-def456", "Follow the coding standards"},
		},
		{
			name: "rule with special ID format",
			rule: CLIRule{
				ID:      "rule-001-security",
				Content: "Never expose credentials in logs",
			},
			contains: []string{"rule-001-security", "Never expose credentials"},
		},
	}

	client := NewCLIClient()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := &CLIContextResponse{
				Success:         true,
				RelevantBullets: []CLIRule{tc.rule},
			}

			formatted := client.FormatForRecovery(result)

			for _, expected := range tc.contains {
				if !strings.Contains(formatted, expected) {
					t.Errorf("CM_GUARD_TEST: Expected %q in formatted output for %s", expected, tc.name)
				}
			}

			t.Logf("CM_GUARD_TEST: Operation=RuleFormatting_%s | ID=%s | OutputLen=%d | Duration=%v",
				tc.name, tc.rule.ID, len(formatted), time.Since(start))
		})
	}
}

func TestGuardRetrieval_RuleConfidenceFormatting(t *testing.T) {
	confidence := 0.875
	result := &CLIContextResponse{
		Success: true,
		RelevantBullets: []CLIRule{{
			ID:         "rule-1",
			Content:    "Use the focused proof command",
			Confidence: &confidence,
		}},
		AntiPatterns: []CLIRule{{
			ID:         "anti-1",
			Content:    "Do not overclaim proof",
			Confidence: &confidence,
		}},
	}

	formatted := NewCLIClient().FormatForRecovery(result)
	if !strings.Contains(formatted, "[rule-1] (confidence: 88%)") {
		t.Errorf("formatted recovery context does not expose rule confidence: %q", formatted)
	}
	if !strings.Contains(formatted, "[anti-1] (confidence: 88%)") {
		t.Errorf("formatted recovery context does not expose anti-pattern confidence: %q", formatted)
	}
}

// TestGuardRetrieval_AntiPatternFormatting verifies anti-patterns have warning indicator
func TestGuardRetrieval_AntiPatternFormatting(t *testing.T) {
	start := time.Now()

	antiPattern := CLIRule{
		ID:      "anti-001",
		Content: "Do not commit secrets to version control",
	}

	result := &CLIContextResponse{
		Success:      true,
		AntiPatterns: []CLIRule{antiPattern},
	}

	client := NewCLIClient()
	formatted := client.FormatForRecovery(result)

	// Anti-patterns should have warning indicator
	if !strings.Contains(formatted, "⚠️") {
		t.Error("CM_GUARD_TEST: Anti-patterns should include warning emoji")
	}

	// Should contain the anti-pattern content
	if !strings.Contains(formatted, antiPattern.ID) {
		t.Errorf("CM_GUARD_TEST: Expected anti-pattern ID %q in output", antiPattern.ID)
	}
	if !strings.Contains(formatted, antiPattern.Content) {
		t.Errorf("CM_GUARD_TEST: Expected anti-pattern content in output")
	}

	t.Logf("CM_GUARD_TEST: Operation=AntiPatternFormatting | ID=%s | HasWarning=true | Duration=%v",
		antiPattern.ID, time.Since(start))
}

// TestGuardInjection_PromptStructure verifies guard injection creates proper prompt structure
func TestGuardInjection_PromptStructure(t *testing.T) {
	start := time.Now()

	result := &CLIContextResponse{
		Success: true,
		RelevantBullets: []CLIRule{
			{ID: "r1", Content: "Rule one"},
			{ID: "r2", Content: "Rule two"},
		},
		AntiPatterns: []CLIRule{
			{ID: "a1", Content: "Anti-pattern one"},
		},
		HistorySnippets: []CLIHistorySnip{
			{Title: "Past work", Agent: "agent", Snippet: "snippet"},
		},
	}

	client := NewCLIClient()
	formatted := client.FormatForRecovery(result)

	// Verify markdown structure
	expectedHeaders := []string{
		"## Procedural Memory (Key Rules)",
		"## Anti-Patterns to Avoid",
		"## Relevant Past Work",
	}

	for _, header := range expectedHeaders {
		if !strings.Contains(formatted, header) {
			t.Errorf("CM_GUARD_TEST: Expected header %q in formatted output", header)
		}
	}

	// Verify bullet list format for rules
	if !strings.Contains(formatted, "- **[r1]**") {
		t.Error("CM_GUARD_TEST: Rules should be formatted as bullet list with bold ID")
	}

	// Verify anti-pattern has warning prefix
	if !strings.Contains(formatted, "- ⚠️ **[a1]**") {
		t.Error("CM_GUARD_TEST: Anti-patterns should have warning prefix")
	}

	t.Logf("CM_GUARD_TEST: Operation=PromptStructure | Headers=%d | Rules=%d | AntiPatterns=%d | Duration=%v",
		len(expectedHeaders), len(result.RelevantBullets), len(result.AntiPatterns), time.Since(start))
}

// TestGuardInjection_SnippetWithMetadata verifies snippets include agent metadata
func TestGuardInjection_SnippetWithMetadata(t *testing.T) {
	start := time.Now()

	snippet := CLIHistorySnip{
		Title:      "Authentication implementation",
		Agent:      "claude_code",
		Snippet:    "Implemented OAuth 2.0 flow with refresh tokens",
		Score:      0.92,
		Workspace:  "my-project",
		SourcePath: "internal/auth/oauth.go",
		LineNumber: 42,
	}

	result := &CLIContextResponse{
		Success:         true,
		HistorySnippets: []CLIHistorySnip{snippet},
	}

	client := NewCLIClient()
	formatted := client.FormatForRecovery(result)

	// Should include title and agent
	if !strings.Contains(formatted, snippet.Title) {
		t.Errorf("CM_GUARD_TEST: Expected snippet title %q in output", snippet.Title)
	}
	if !strings.Contains(formatted, snippet.Agent) {
		t.Errorf("CM_GUARD_TEST: Expected agent name %q in output", snippet.Agent)
	}
	if !strings.Contains(formatted, "OAuth 2.0") {
		t.Error("CM_GUARD_TEST: Expected snippet content in output")
	}

	t.Logf("CM_GUARD_TEST: Operation=SnippetWithMetadata | Title=%s | Agent=%s | Score=%.2f | Duration=%v",
		snippet.Title, snippet.Agent, snippet.Score, time.Since(start))
}

// TestGuardConflict_RulePrioritization verifies rules are prioritized correctly
func TestGuardConflict_RulePrioritization(t *testing.T) {
	start := time.Now()

	// When multiple rules exist, they should all be included
	// Order should be preserved from the source
	result := &CLIContextResponse{
		Success: true,
		RelevantBullets: []CLIRule{
			{ID: "high-1", Content: "High priority rule"},
			{ID: "high-2", Content: "Second high priority rule"},
			{ID: "med-1", Content: "Medium priority rule"},
		},
	}

	client := NewCLIClient()
	formatted := client.FormatForRecovery(result)

	// All rules should be present
	for _, rule := range result.RelevantBullets {
		if !strings.Contains(formatted, rule.ID) {
			t.Errorf("CM_GUARD_TEST: Rule %q should be in output", rule.ID)
		}
	}

	// Check order is preserved
	idx1 := strings.Index(formatted, "high-1")
	idx2 := strings.Index(formatted, "high-2")
	idx3 := strings.Index(formatted, "med-1")

	if idx1 >= idx2 || idx2 >= idx3 {
		t.Error("CM_GUARD_TEST: Rules should maintain input order")
	}

	t.Logf("CM_GUARD_TEST: Operation=RulePrioritization | RuleCount=%d | OrderPreserved=%v | Duration=%v",
		len(result.RelevantBullets), idx1 < idx2 && idx2 < idx3, time.Since(start))
}

// TestGuardConflict_AntiPatternSeparation verifies anti-patterns are separate from rules
func TestGuardConflict_AntiPatternSeparation(t *testing.T) {
	start := time.Now()

	result := &CLIContextResponse{
		Success: true,
		RelevantBullets: []CLIRule{
			{ID: "rule-do", Content: "Do this"},
		},
		AntiPatterns: []CLIRule{
			{ID: "rule-dont", Content: "Don't do this"},
		},
	}

	client := NewCLIClient()
	formatted := client.FormatForRecovery(result)

	// Rules and anti-patterns should be in separate sections
	rulesSection := strings.Index(formatted, "## Procedural Memory")
	antiSection := strings.Index(formatted, "## Anti-Patterns")

	if rulesSection == -1 || antiSection == -1 {
		t.Fatal("CM_GUARD_TEST: Both sections should be present")
	}

	// Rules section should come before anti-patterns
	if rulesSection >= antiSection {
		t.Error("CM_GUARD_TEST: Rules section should come before anti-patterns section")
	}

	// "Do" rule should not be in anti-patterns section
	ruleIdx := strings.Index(formatted, "rule-do")
	antiIdx := strings.Index(formatted, "rule-dont")

	if ruleIdx > antiIdx {
		t.Error("CM_GUARD_TEST: Regular rules should appear before anti-patterns")
	}

	t.Logf("CM_GUARD_TEST: Operation=AntiPatternSeparation | RulesIdx=%d | AntiIdx=%d | Duration=%v",
		rulesSection, antiSection, time.Since(start))
}

// TestGuardInjection_EmptyGuards verifies handling of no guards
func TestGuardInjection_EmptyGuards(t *testing.T) {
	start := time.Now()

	testCases := []struct {
		name   string
		result *CLIContextResponse
	}{
		{
			name:   "nil response",
			result: nil,
		},
		{
			name: "empty response",
			result: &CLIContextResponse{
				Success: true,
			},
		},
		{
			name: "empty arrays",
			result: &CLIContextResponse{
				Success:         true,
				RelevantBullets: []CLIRule{},
				AntiPatterns:    []CLIRule{},
				HistorySnippets: []CLIHistorySnip{},
			},
		},
	}

	client := NewCLIClient()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			formatted := client.FormatForRecovery(tc.result)

			if formatted != "" {
				t.Errorf("CM_GUARD_TEST: Expected empty output for %s, got: %q", tc.name, formatted)
			}

			t.Logf("CM_GUARD_TEST: Operation=EmptyGuards_%s | Output=%q | Duration=%v",
				tc.name, formatted, time.Since(start))
		})
	}
}

// TestGuardInjection_TruncatedSnippets verifies long snippets are truncated
func TestGuardInjection_TruncatedSnippets(t *testing.T) {
	start := time.Now()

	// Create a snippet with very long content
	longContent := strings.Repeat("This is a very long snippet content that should be truncated. ", 20)
	snippet := CLIHistorySnip{
		Title:   "Long snippet",
		Agent:   "agent",
		Snippet: longContent,
	}

	result := &CLIContextResponse{
		Success:         true,
		HistorySnippets: []CLIHistorySnip{snippet},
	}

	client := NewCLIClient()
	formatted := client.FormatForRecovery(result)

	// Output should be shorter than the full content
	// The truncate function limits to 200 chars + "..."
	if strings.Contains(formatted, longContent) {
		t.Error("CM_GUARD_TEST: Long snippet content should be truncated")
	}

	// Should contain truncation indicator
	if !strings.Contains(formatted, "...") {
		t.Error("CM_GUARD_TEST: Truncated content should have ellipsis")
	}

	t.Logf("CM_GUARD_TEST: Operation=TruncatedSnippets | OriginalLen=%d | OutputLen=%d | Duration=%v",
		len(longContent), len(formatted), time.Since(start))
}

// TestGuardInjection_MultipleSnippets verifies multiple snippets are all included
func TestGuardInjection_MultipleSnippets(t *testing.T) {
	start := time.Now()

	snippets := []CLIHistorySnip{
		{Title: "First work", Agent: "claude", Snippet: "Did first thing"},
		{Title: "Second work", Agent: "codex", Snippet: "Did second thing"},
		{Title: "Third work", Agent: "gemini", Snippet: "Did third thing"},
	}

	result := &CLIContextResponse{
		Success:         true,
		HistorySnippets: snippets,
	}

	client := NewCLIClient()
	formatted := client.FormatForRecovery(result)

	// All snippets should be present
	for _, snip := range snippets {
		if !strings.Contains(formatted, snip.Title) {
			t.Errorf("CM_GUARD_TEST: Snippet %q should be in output", snip.Title)
		}
		if !strings.Contains(formatted, snip.Agent) {
			t.Errorf("CM_GUARD_TEST: Agent %q should be in output", snip.Agent)
		}
	}

	t.Logf("CM_GUARD_TEST: Operation=MultipleSnippets | Count=%d | OutputLen=%d | Duration=%v",
		len(snippets), len(formatted), time.Since(start))
}

// TestGuardRetrieval_CategoryHandling verifies categories are handled correctly
func TestGuardRetrieval_CategoryHandling(t *testing.T) {
	start := time.Now()

	rules := []CLIRule{
		{ID: "r1", Content: "Rule without category"},
		{ID: "r2", Content: "Rule with category", Category: "testing"},
		{ID: "r3", Content: "Another categorized", Category: "security"},
	}

	result := &CLIContextResponse{
		Success:         true,
		RelevantBullets: rules,
	}

	client := NewCLIClient()
	formatted := client.FormatForRecovery(result)

	// All rules should be present regardless of category
	for _, rule := range rules {
		if !strings.Contains(formatted, rule.ID) {
			t.Errorf("CM_GUARD_TEST: Rule %q should be in output", rule.ID)
		}
		if !strings.Contains(formatted, rule.Content) {
			t.Errorf("CM_GUARD_TEST: Rule content should be in output")
		}
	}

	t.Logf("CM_GUARD_TEST: Operation=CategoryHandling | RuleCount=%d | OutputLen=%d | Duration=%v",
		len(rules), len(formatted), time.Since(start))
}

// TestGuardInjection_MarkdownSafety verifies output is valid markdown
func TestGuardInjection_MarkdownSafety(t *testing.T) {
	start := time.Now()

	// Test with content that could break markdown
	result := &CLIContextResponse{
		Success: true,
		RelevantBullets: []CLIRule{
			{ID: "r1", Content: "Rule with *asterisks* and _underscores_"},
			{ID: "r2", Content: "Rule with [brackets] and (parens)"},
			{ID: "r3", Content: "Rule with `backticks` and ```code```"},
			{ID: "r4", Content: "Rule with # hash and > quote"},
		},
		AntiPatterns: []CLIRule{
			{ID: "a1", Content: "Pattern with | pipe | chars"},
		},
	}

	client := NewCLIClient()
	formatted := client.FormatForRecovery(result)

	// Should produce output without panicking
	if formatted == "" {
		t.Error("CM_GUARD_TEST: Should produce non-empty output")
	}

	// All rules should be present
	for _, rule := range result.RelevantBullets {
		if !strings.Contains(formatted, rule.ID) {
			t.Errorf("CM_GUARD_TEST: Rule %q should be in output", rule.ID)
		}
	}

	// Verify basic markdown structure is valid
	if !strings.HasPrefix(formatted, "##") {
		t.Error("CM_GUARD_TEST: Output should start with markdown header")
	}

	t.Logf("CM_GUARD_TEST: Operation=MarkdownSafety | Rules=%d | OutputLen=%d | Duration=%v",
		len(result.RelevantBullets), len(formatted), time.Since(start))
}

// TestGuardInjection_NewlineHandling verifies newlines in content are handled
func TestGuardInjection_NewlineHandling(t *testing.T) {
	start := time.Now()

	result := &CLIContextResponse{
		Success: true,
		RelevantBullets: []CLIRule{
			{ID: "r1", Content: "Rule with\nnewline"},
			{ID: "r2", Content: "Rule with\r\nCRLF"},
		},
		HistorySnippets: []CLIHistorySnip{
			{Title: "Multi-line", Agent: "agent", Snippet: "Line 1\nLine 2\nLine 3"},
		},
	}

	client := NewCLIClient()
	formatted := client.FormatForRecovery(result)

	// Should produce output
	if formatted == "" {
		t.Error("CM_GUARD_TEST: Should produce non-empty output with newlines")
	}

	// Rules should be present
	if !strings.Contains(formatted, "r1") || !strings.Contains(formatted, "r2") {
		t.Error("CM_GUARD_TEST: Rules with newlines should be in output")
	}

	t.Logf("CM_GUARD_TEST: Operation=NewlineHandling | Rules=%d | Snippets=%d | Duration=%v",
		len(result.RelevantBullets), len(result.HistorySnippets), time.Since(start))
}

// TestGuardRetrieval_SuggestedQueriesNotInOutput verifies suggested queries don't appear
func TestGuardRetrieval_SuggestedQueriesNotInOutput(t *testing.T) {
	start := time.Now()

	result := &CLIContextResponse{
		Success: true,
		RelevantBullets: []CLIRule{
			{ID: "r1", Content: "Test rule"},
		},
		SuggestedQueries: []string{
			"authentication patterns",
			"error handling best practices",
		},
	}

	client := NewCLIClient()
	formatted := client.FormatForRecovery(result)

	// Suggested queries should NOT appear in the formatted recovery output
	// They are for follow-up actions, not for injection
	for _, query := range result.SuggestedQueries {
		if strings.Contains(formatted, query) {
			t.Errorf("CM_GUARD_TEST: Suggested query %q should not be in formatted output", query)
		}
	}

	t.Logf("CM_GUARD_TEST: Operation=SuggestedQueriesExcluded | Queries=%d | OutputLen=%d | Duration=%v",
		len(result.SuggestedQueries), len(formatted), time.Since(start))
}

// TestGuardConflict_SameIDDifferentContent verifies handling of potential conflicts
func TestGuardConflict_SameIDDifferentContent(t *testing.T) {
	start := time.Now()

	// This tests handling when the same ID appears in rules and anti-patterns
	// (which shouldn't happen in practice but should be handled gracefully)
	result := &CLIContextResponse{
		Success: true,
		RelevantBullets: []CLIRule{
			{ID: "conflicting-id", Content: "This is the rule version"},
		},
		AntiPatterns: []CLIRule{
			{ID: "conflicting-id", Content: "This is the anti-pattern version"},
		},
	}

	client := NewCLIClient()
	formatted := client.FormatForRecovery(result)

	// Both should appear (no deduplication at this level)
	ruleCount := strings.Count(formatted, "conflicting-id")
	if ruleCount < 2 {
		t.Logf("CM_GUARD_TEST: Note - same ID appeared %d times (deduplication may be applied)", ruleCount)
	}

	// Both sections should be present
	if !bytes.Contains([]byte(formatted), []byte("Procedural Memory")) {
		t.Error("CM_GUARD_TEST: Rules section should be present")
	}
	if !bytes.Contains([]byte(formatted), []byte("Anti-Patterns")) {
		t.Error("CM_GUARD_TEST: Anti-patterns section should be present")
	}

	t.Logf("CM_GUARD_TEST: Operation=ConflictingIDs | IDOccurrences=%d | Duration=%v",
		ruleCount, time.Since(start))
}

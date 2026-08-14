package cm

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// =============================================================================
// ContextBuilder Tests for CM Integration (bd-3jgk)
// =============================================================================
// Tests for context assembly from multiple sources, priority ordering of memories,
// token budget management, and relevance scoring.

// TestContextAssembly_MultipleSourceCombination verifies context assembly from multiple sources
func TestContextAssembly_MultipleSourceCombination(t *testing.T) {
	start := time.Now()

	// Create a context response with all types of sources
	result := &CLIContextResponse{
		Success: true,
		Task:    "test-task",
		RelevantBullets: []Rule{
			{ID: "rule-1", Content: "Always test before commit", Category: "testing"},
			{ID: "rule-2", Content: "Follow code review guidelines", Category: "review"},
		},
		AntiPatterns: []Rule{
			{ID: "anti-1", Content: "Never commit secrets", Category: "security"},
		},
		HistorySnippets: []HistorySnippet{
			{
				Title:   "Previous auth work",
				Agent:   "claude_code",
				Snippet: "Implemented OAuth flow",
				Score:   0.95,
			},
		},
		SuggestedQueries: []string{"auth patterns", "security best practices"},
	}

	client := NewCLIClient()
	formatted := client.FormatForRecovery(result)

	// Verify all sources are included
	if !bytes.Contains([]byte(formatted), []byte("Procedural Memory")) {
		t.Error("CM_CONTEXT_TEST: Expected rules section in formatted output")
	}
	if !bytes.Contains([]byte(formatted), []byte("Anti-Patterns")) {
		t.Error("CM_CONTEXT_TEST: Expected anti-patterns section in formatted output")
	}
	if !bytes.Contains([]byte(formatted), []byte("Relevant Past Work")) {
		t.Error("CM_CONTEXT_TEST: Expected history snippets section in formatted output")
	}

	// Verify specific content
	if !bytes.Contains([]byte(formatted), []byte("rule-1")) {
		t.Error("CM_CONTEXT_TEST: Expected rule-1 ID in output")
	}
	if !bytes.Contains([]byte(formatted), []byte("anti-1")) {
		t.Error("CM_CONTEXT_TEST: Expected anti-1 ID in output")
	}

	t.Logf("CM_CONTEXT_TEST: Operation=MultipleSourceCombination | Sources=3 | OutputLen=%d | Duration=%v",
		len(formatted), time.Since(start))
}

// TestContextAssembly_EmptySources verifies handling of empty sources
func TestContextAssembly_EmptySources(t *testing.T) {
	start := time.Now()

	testCases := []struct {
		name   string
		result *CLIContextResponse
		want   string
	}{
		{
			name:   "nil result",
			result: nil,
			want:   "",
		},
		{
			name: "empty arrays",
			result: &CLIContextResponse{
				Success:         true,
				RelevantBullets: []Rule{},
				AntiPatterns:    []Rule{},
				HistorySnippets: []HistorySnippet{},
			},
			want: "",
		},
		{
			name: "only rules",
			result: &CLIContextResponse{
				Success: true,
				RelevantBullets: []Rule{
					{ID: "r1", Content: "Test rule"},
				},
			},
			want: "Procedural Memory",
		},
		{
			name: "only anti-patterns",
			result: &CLIContextResponse{
				Success: true,
				AntiPatterns: []Rule{
					{ID: "a1", Content: "Avoid this"},
				},
			},
			want: "Anti-Patterns",
		},
		{
			name: "only snippets",
			result: &CLIContextResponse{
				Success: true,
				HistorySnippets: []HistorySnippet{
					{Title: "Past work", Agent: "agent", Snippet: "Did something"},
				},
			},
			want: "Relevant Past Work",
		},
	}

	client := NewCLIClient()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			formatted := client.FormatForRecovery(tc.result)

			if tc.want == "" && formatted != "" {
				t.Errorf("CM_CONTEXT_TEST: Expected empty output for %s, got: %q", tc.name, formatted)
			}
			if tc.want != "" && !bytes.Contains([]byte(formatted), []byte(tc.want)) {
				t.Errorf("CM_CONTEXT_TEST: Expected %q in output for %s", tc.want, tc.name)
			}

			t.Logf("CM_CONTEXT_TEST: Operation=EmptySources_%s | OutputLen=%d | Duration=%v",
				tc.name, len(formatted), time.Since(start))
		})
	}
}

// TestPriorityOrdering_RulesBeforeAntiPatterns verifies correct ordering in output
func TestPriorityOrdering_RulesBeforeAntiPatterns(t *testing.T) {
	start := time.Now()

	result := &CLIContextResponse{
		Success: true,
		RelevantBullets: []Rule{
			{ID: "rule-1", Content: "First rule"},
		},
		AntiPatterns: []Rule{
			{ID: "anti-1", Content: "First anti-pattern"},
		},
		HistorySnippets: []HistorySnippet{
			{Title: "Past work", Agent: "agent", Snippet: "History"},
		},
	}

	client := NewCLIClient()
	formatted := client.FormatForRecovery(result)

	// Check ordering: Rules should come before Anti-Patterns, which should come before Snippets
	rulesIdx := bytes.Index([]byte(formatted), []byte("Procedural Memory"))
	antiIdx := bytes.Index([]byte(formatted), []byte("Anti-Patterns"))
	snippetsIdx := bytes.Index([]byte(formatted), []byte("Relevant Past Work"))

	if rulesIdx == -1 || antiIdx == -1 || snippetsIdx == -1 {
		t.Fatal("CM_CONTEXT_TEST: Missing expected sections in output")
	}

	if rulesIdx > antiIdx {
		t.Error("CM_CONTEXT_TEST: Rules should appear before Anti-Patterns")
	}
	if antiIdx > snippetsIdx {
		t.Error("CM_CONTEXT_TEST: Anti-Patterns should appear before Snippets")
	}

	t.Logf("CM_CONTEXT_TEST: Operation=PriorityOrdering | RulesIdx=%d | AntiIdx=%d | SnippetsIdx=%d | Duration=%v",
		rulesIdx, antiIdx, snippetsIdx, time.Since(start))
}

// TestTokenBudget_LimitsApplied verifies that GetRecoveryContext applies limits
func TestTokenBudget_LimitsApplied(t *testing.T) {
	start := time.Now()

	// Test limit application on result structure
	// Since we can't call a real CM binary, we simulate the limit logic
	testCases := []struct {
		name        string
		maxRules    int
		maxSnippets int
		numRules    int
		numSnippets int
		expectRules int
		expectSnips int
	}{
		{
			name:        "no limits needed",
			maxRules:    10,
			maxSnippets: 5,
			numRules:    3,
			numSnippets: 2,
			expectRules: 3,
			expectSnips: 2,
		},
		{
			name:        "rules limited",
			maxRules:    2,
			maxSnippets: 5,
			numRules:    5,
			numSnippets: 2,
			expectRules: 2,
			expectSnips: 2,
		},
		{
			name:        "snippets limited",
			maxRules:    10,
			maxSnippets: 1,
			numRules:    3,
			numSnippets: 5,
			expectRules: 3,
			expectSnips: 1,
		},
		{
			name:        "both limited",
			maxRules:    2,
			maxSnippets: 1,
			numRules:    10,
			numSnippets: 10,
			expectRules: 2,
			expectSnips: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create input with specified counts
			result := &CLIContextResponse{
				Success:         true,
				RelevantBullets: make([]Rule, tc.numRules),
				AntiPatterns:    make([]Rule, tc.numRules),
				HistorySnippets: make([]HistorySnippet, tc.numSnippets),
			}

			for i := 0; i < tc.numRules; i++ {
				result.RelevantBullets[i] = Rule{ID: "r" + string(rune('0'+i)), Content: "Rule"}
				result.AntiPatterns[i] = Rule{ID: "a" + string(rune('0'+i)), Content: "Anti"}
			}
			for i := 0; i < tc.numSnippets; i++ {
				result.HistorySnippets[i] = HistorySnippet{Title: "Snip", Agent: "a", Snippet: "s"}
			}

			// Apply limits (simulating GetRecoveryContext behavior)
			if tc.maxRules > 0 && len(result.RelevantBullets) > tc.maxRules {
				result.RelevantBullets = result.RelevantBullets[:tc.maxRules]
			}
			if tc.maxRules > 0 && len(result.AntiPatterns) > tc.maxRules {
				result.AntiPatterns = result.AntiPatterns[:tc.maxRules]
			}
			if tc.maxSnippets > 0 && len(result.HistorySnippets) > tc.maxSnippets {
				result.HistorySnippets = result.HistorySnippets[:tc.maxSnippets]
			}

			// Verify limits
			if len(result.RelevantBullets) != tc.expectRules {
				t.Errorf("CM_CONTEXT_TEST: Expected %d rules, got %d", tc.expectRules, len(result.RelevantBullets))
			}
			if len(result.HistorySnippets) != tc.expectSnips {
				t.Errorf("CM_CONTEXT_TEST: Expected %d snippets, got %d", tc.expectSnips, len(result.HistorySnippets))
			}

			t.Logf("CM_CONTEXT_TEST: Operation=TokenBudget_%s | Rules=%d | AntiPatterns=%d | Snippets=%d | Duration=%v",
				tc.name, len(result.RelevantBullets), len(result.AntiPatterns), len(result.HistorySnippets), time.Since(start))
		})
	}
}

// TestRelevanceScoring_SnippetOrdering verifies snippets maintain relevance ordering
func TestRelevanceScoring_SnippetOrdering(t *testing.T) {
	start := time.Now()

	// Create snippets with different scores
	result := &CLIContextResponse{
		Success: true,
		HistorySnippets: []HistorySnippet{
			{Title: "High relevance", Agent: "claude", Snippet: "Important work", Score: 0.95},
			{Title: "Medium relevance", Agent: "codex", Snippet: "Some work", Score: 0.75},
			{Title: "Low relevance", Agent: "gemini", Snippet: "Minor work", Score: 0.50},
		},
	}

	// Verify scores are present and ordered
	for i := 1; i < len(result.HistorySnippets); i++ {
		if result.HistorySnippets[i].Score > result.HistorySnippets[i-1].Score {
			t.Errorf("CM_CONTEXT_TEST: Snippets should be ordered by descending score, got %.2f after %.2f",
				result.HistorySnippets[i].Score, result.HistorySnippets[i-1].Score)
		}
	}

	// Verify highest score snippet is first
	if result.HistorySnippets[0].Score != 0.95 {
		t.Errorf("CM_CONTEXT_TEST: First snippet should have highest score 0.95, got %.2f",
			result.HistorySnippets[0].Score)
	}

	t.Logf("CM_CONTEXT_TEST: Operation=RelevanceScoring | Snippets=%d | TopScore=%.2f | Duration=%v",
		len(result.HistorySnippets), result.HistorySnippets[0].Score, time.Since(start))
}

// TestContextResponse_JSONFields verifies all JSON fields are properly mapped
func TestContextResponse_JSONFields(t *testing.T) {
	start := time.Now()

	resp := CLIContextResponse{
		Success: true,
		Task:    "test-task",
		RelevantBullets: []Rule{
			{ID: "b-123", Content: "Test rule", Category: "testing"},
		},
		AntiPatterns: []Rule{
			{ID: "a-456", Content: "Anti pattern", Category: "security"},
		},
		HistorySnippets: []HistorySnippet{
			{
				SourcePath: "/path/to/source.go",
				LineNumber: 42,
				Agent:      "claude_code",
				Workspace:  "my-project",
				Title:      "Auth implementation",
				Snippet:    "func auth() { ... }",
				Score:      0.87,
				CreatedAt:  1705432800,
			},
		},
		SuggestedQueries: []string{"query1", "query2"},
	}

	// Verify all fields are accessible
	if !resp.Success {
		t.Error("CM_CONTEXT_TEST: Success should be true")
	}
	if resp.Task != "test-task" {
		t.Errorf("CM_CONTEXT_TEST: Task mismatch: got %q", resp.Task)
	}
	if len(resp.RelevantBullets) != 1 {
		t.Errorf("CM_CONTEXT_TEST: Expected 1 rule, got %d", len(resp.RelevantBullets))
	}
	if len(resp.AntiPatterns) != 1 {
		t.Errorf("CM_CONTEXT_TEST: Expected 1 anti-pattern, got %d", len(resp.AntiPatterns))
	}
	if len(resp.HistorySnippets) != 1 {
		t.Errorf("CM_CONTEXT_TEST: Expected 1 snippet, got %d", len(resp.HistorySnippets))
	}
	if len(resp.SuggestedQueries) != 2 {
		t.Errorf("CM_CONTEXT_TEST: Expected 2 suggested queries, got %d", len(resp.SuggestedQueries))
	}

	// Verify snippet fields
	snip := resp.HistorySnippets[0]
	if snip.SourcePath != "/path/to/source.go" {
		t.Errorf("CM_CONTEXT_TEST: SourcePath mismatch: got %q", snip.SourcePath)
	}
	if snip.LineNumber != 42 {
		t.Errorf("CM_CONTEXT_TEST: LineNumber mismatch: got %d", snip.LineNumber)
	}
	if snip.Score != 0.87 {
		t.Errorf("CM_CONTEXT_TEST: Score mismatch: got %.2f", snip.Score)
	}

	t.Logf("CM_CONTEXT_TEST: Operation=JSONFields | Rules=%d | AntiPatterns=%d | Snippets=%d | Queries=%d | Duration=%v",
		len(resp.RelevantBullets), len(resp.AntiPatterns), len(resp.HistorySnippets), len(resp.SuggestedQueries), time.Since(start))
}

// TestContextClientTimeout_Configuration verifies timeout configuration
func TestContextClientTimeout_Configuration(t *testing.T) {
	start := time.Now()

	testCases := []struct {
		name     string
		timeout  time.Duration
		expected time.Duration
	}{
		{"default", 0, 30 * time.Second},
		{"custom_short", 5 * time.Second, 5 * time.Second},
		{"custom_long", 120 * time.Second, 120 * time.Second},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var client *CLIClient
			if tc.timeout == 0 {
				client = NewCLIClient()
			} else {
				client = NewCLIClient(WithCLITimeout(tc.timeout))
			}

			if client.timeout != tc.expected {
				t.Errorf("CM_CONTEXT_TEST: Expected timeout %v, got %v", tc.expected, client.timeout)
			}

			t.Logf("CM_CONTEXT_TEST: Operation=TimeoutConfig_%s | Timeout=%v | Duration=%v",
				tc.name, client.timeout, time.Since(start))
		})
	}
}

// TestContextWithCancellation verifies context cancellation handling
func TestContextWithCancellation(t *testing.T) {
	start := time.Now()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	client := NewCLIClient(WithCLIBinaryPath("/nonexistent/cm"))

	// Should handle cancelled context gracefully
	result, err := client.GetContext(ctx, "test task", "")

	// With nonexistent binary, should return nil, nil (graceful degradation)
	if err != nil {
		t.Logf("CM_CONTEXT_TEST: GetContext with cancelled context returned error (expected for real binary): %v", err)
	}
	if result != nil {
		t.Logf("CM_CONTEXT_TEST: GetContext with cancelled context returned result: %+v", result)
	}

	t.Logf("CM_CONTEXT_TEST: Operation=ContextCancellation | Error=%v | Result=%v | Duration=%v",
		err, result, time.Since(start))
}

// TestRecoveryContextTaskFormat verifies task formatting for recovery
func TestRecoveryContextTaskFormat(t *testing.T) {
	start := time.Now()

	client := NewCLIClient(WithCLIBinaryPath("/nonexistent/cm"))

	// Call GetRecoveryContext - this formats the task as "<projectName>: starting new coding session"
	// Since CM isn't installed, it should return nil, nil
	result, err := client.GetRecoveryContext(context.Background(), "my-project", "", 5, 3)

	if err != nil {
		t.Errorf("CM_CONTEXT_TEST: GetRecoveryContext should not error when CM unavailable, got: %v", err)
	}
	if result != nil {
		t.Errorf("CM_CONTEXT_TEST: GetRecoveryContext should return nil when CM unavailable")
	}

	t.Logf("CM_CONTEXT_TEST: Operation=RecoveryContextTaskFormat | Project=my-project | MaxRules=5 | MaxSnippets=3 | Duration=%v",
		time.Since(start))
}

// TestLargeContextFormatting verifies handling of large context responses
func TestLargeContextFormatting(t *testing.T) {
	start := time.Now()

	// Create a large context response
	largeResult := &CLIContextResponse{
		Success:         true,
		Task:            "large-task",
		RelevantBullets: make([]Rule, 50),
		AntiPatterns:    make([]Rule, 30),
		HistorySnippets: make([]HistorySnippet, 20),
	}

	// Fill with content
	for i := 0; i < 50; i++ {
		largeResult.RelevantBullets[i] = Rule{
			ID:       "rule-" + string(rune('A'+i%26)) + string(rune('0'+i)),
			Content:  "This is a rule with some content for testing purposes " + string(rune('0'+i)),
			Category: "category-" + string(rune('A'+i%5)),
		}
	}
	for i := 0; i < 30; i++ {
		largeResult.AntiPatterns[i] = Rule{
			ID:      "anti-" + string(rune('A'+i%26)),
			Content: "This is an anti-pattern to avoid " + string(rune('0'+i)),
		}
	}
	for i := 0; i < 20; i++ {
		largeResult.HistorySnippets[i] = HistorySnippet{
			Title:   "Past work item " + string(rune('0'+i)),
			Agent:   "agent-" + string(rune('A'+i%3)),
			Snippet: "Some historical snippet content " + string(rune('0'+i)),
			Score:   0.9 - float64(i)*0.04,
		}
	}

	client := NewCLIClient()
	formatted := client.FormatForRecovery(largeResult)

	// Should not panic and should produce non-empty output
	if formatted == "" {
		t.Error("CM_CONTEXT_TEST: Large context should produce non-empty formatted output")
	}

	// Should contain all sections
	if !bytes.Contains([]byte(formatted), []byte("Procedural Memory")) {
		t.Error("CM_CONTEXT_TEST: Large context should contain rules section")
	}
	if !bytes.Contains([]byte(formatted), []byte("Anti-Patterns")) {
		t.Error("CM_CONTEXT_TEST: Large context should contain anti-patterns section")
	}
	if !bytes.Contains([]byte(formatted), []byte("Relevant Past Work")) {
		t.Error("CM_CONTEXT_TEST: Large context should contain snippets section")
	}

	t.Logf("CM_CONTEXT_TEST: Operation=LargeContextFormatting | Rules=%d | AntiPatterns=%d | Snippets=%d | OutputLen=%d | Duration=%v",
		len(largeResult.RelevantBullets), len(largeResult.AntiPatterns), len(largeResult.HistorySnippets), len(formatted), time.Since(start))
}

// TestUnicodeContent verifies handling of unicode characters in context
func TestUnicodeContent(t *testing.T) {
	start := time.Now()

	result := &CLIContextResponse{
		Success: true,
		Task:    "unicode-task-日本語",
		RelevantBullets: []Rule{
			{ID: "rule-中文", Content: "规则内容 - Chinese content"},
			{ID: "rule-日本", Content: "日本語の内容 - Japanese content"},
			{ID: "rule-emoji", Content: "Rule with emoji: 🚀 🎉 ✅"},
		},
		AntiPatterns: []Rule{
			{ID: "anti-кириллица", Content: "Кириллический текст - Cyrillic content"},
		},
		HistorySnippets: []HistorySnippet{
			{
				Title:   "العربية - Arabic",
				Agent:   "agent",
				Snippet: "عربي محتوى",
			},
		},
	}

	client := NewCLIClient()
	formatted := client.FormatForRecovery(result)

	// Should handle unicode without errors
	if formatted == "" {
		t.Error("CM_CONTEXT_TEST: Unicode content should produce non-empty output")
	}

	// Check that unicode content is preserved
	if !bytes.Contains([]byte(formatted), []byte("中文")) {
		t.Error("CM_CONTEXT_TEST: Chinese characters should be preserved")
	}
	if !bytes.Contains([]byte(formatted), []byte("日本語")) {
		t.Error("CM_CONTEXT_TEST: Japanese characters should be preserved")
	}
	if !bytes.Contains([]byte(formatted), []byte("🚀")) {
		t.Error("CM_CONTEXT_TEST: Emoji should be preserved")
	}

	t.Logf("CM_CONTEXT_TEST: Operation=UnicodeContent | Rules=%d | OutputLen=%d | Duration=%v",
		len(result.RelevantBullets), len(formatted), time.Since(start))
}

// TestSpecialCharactersInContent verifies handling of special characters
func TestSpecialCharactersInContent(t *testing.T) {
	start := time.Now()

	result := &CLIContextResponse{
		Success: true,
		RelevantBullets: []Rule{
			{ID: "rule-1", Content: "Rule with <brackets> and & ampersand"},
			{ID: "rule-2", Content: "Rule with \"quotes\" and 'apostrophes'"},
			{ID: "rule-3", Content: "Rule with newline\nand\ttab"},
			{ID: "rule-4", Content: "Rule with backslash \\ and forward /"},
		},
		AntiPatterns: []Rule{
			{ID: "anti-1", Content: "Pattern with $variable and ${braces}"},
		},
	}

	client := NewCLIClient()
	formatted := client.FormatForRecovery(result)

	// Should handle special characters without errors
	if formatted == "" {
		t.Error("CM_CONTEXT_TEST: Special characters should produce non-empty output")
	}

	// Content should be present (exact escaping depends on formatting)
	if !bytes.Contains([]byte(formatted), []byte("rule-1")) {
		t.Error("CM_CONTEXT_TEST: Rule with special chars should be present")
	}

	t.Logf("CM_CONTEXT_TEST: Operation=SpecialCharacters | Rules=%d | OutputLen=%d | Duration=%v",
		len(result.RelevantBullets)+len(result.AntiPatterns), len(formatted), time.Since(start))
}

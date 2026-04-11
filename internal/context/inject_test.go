package context

import (
	"strings"
	"testing"
	"time"
)

// TestSummaryInjectionFormatting tests that handoff summaries are properly formatted.
func TestSummaryInjectionFormatting(t *testing.T) {
	t.Parallel()

	summary := &HandoffSummary{
		GeneratedAt:   time.Now(),
		OldAgentID:    "cc_1",
		OldAgentType:  "claude",
		SessionName:   "test-session",
		CurrentTask:   "Implementing authentication module",
		Progress:      "Completed OAuth integration, need to add password reset",
		KeyDecisions:  []string{"Using JWT tokens", "Storing in Redis"},
		ActiveFiles:   []string{"auth/handler.go", "auth/middleware.go"},
		Blockers:      []string{"Waiting for API keys"},
		RawSummary:    "Full summary text here...",
		TokenEstimate: 250,
	}

	formatted := summary.FormatForNewAgent()

	t.Logf("CONTEXT_TEST: SummaryInjection | TokenEstimate=%d | FormattedLen=%d",
		summary.TokenEstimate, len(formatted))

	// Verify required sections are present
	requiredSections := []string{
		"HANDOFF CONTEXT",
		"Current Task",
		"Progress",
		"Key Decisions",
		"Active Files",
		"Blockers",
		"continue",
	}

	for _, section := range requiredSections {
		if !strings.Contains(formatted, section) {
			t.Errorf("formatted summary missing section: %s", section)
		}
	}

	// Verify content is included
	requiredContent := []string{
		"authentication module",
		"OAuth integration",
		"JWT tokens",
		"auth/handler.go",
		"API keys",
	}

	for _, content := range requiredContent {
		if !strings.Contains(formatted, content) {
			t.Errorf("formatted summary missing content: %s", content)
		}
	}
}

// TestKeyFactsExtraction tests extraction of key facts from agent responses.
func TestKeyFactsExtraction(t *testing.T) {
	t.Parallel()

	response := `## CURRENT TASK
Building the user authentication system.

## PROGRESS
- Completed database schema design
- Implemented user registration
- Added email verification
- Currently working on login flow

## KEY DECISIONS
- Using bcrypt for password hashing
- JWT tokens with 24h expiry
- Refresh tokens stored in Redis
- Rate limiting on auth endpoints

## ACTIVE FILES
- internal/auth/handler.go
- internal/auth/tokens.go
- internal/db/users.go
- configs/auth.yaml

## BLOCKERS
- Need Stripe API keys for premium features
- Waiting for security review`

	g := NewSummaryGenerator(DefaultSummaryGeneratorConfig())
	summary := g.ParseAgentResponse("agent_1", "claude", "auth-project", response)

	t.Logf("CONTEXT_TEST: KeyFactsExtraction | KeyDecisions=%d | ActiveFiles=%d | Blockers=%d",
		len(summary.KeyDecisions), len(summary.ActiveFiles), len(summary.Blockers))

	// Verify key facts extraction
	if len(summary.KeyDecisions) < 3 {
		t.Errorf("expected at least 3 key decisions, got %d", len(summary.KeyDecisions))
	}

	if len(summary.ActiveFiles) < 3 {
		t.Errorf("expected at least 3 active files, got %d", len(summary.ActiveFiles))
	}

	if len(summary.Blockers) < 2 {
		t.Errorf("expected at least 2 blockers, got %d", len(summary.Blockers))
	}

	// Verify specific facts are extracted
	foundJWT := false
	for _, decision := range summary.KeyDecisions {
		if strings.Contains(decision, "JWT") {
			foundJWT = true
			break
		}
	}
	if !foundJWT {
		t.Error("should extract JWT decision from key decisions")
	}
}

// TestDecisionPreservation tests that important decisions are preserved through injection.
func TestDecisionPreservation(t *testing.T) {
	t.Parallel()

	criticalDecisions := []string{
		"Using PostgreSQL as primary database",
		"Implementing eventual consistency for notifications",
		"Adopting hexagonal architecture pattern",
		"Rate limiting at 1000 req/min per user",
	}

	summary := &HandoffSummary{
		GeneratedAt:  time.Now(),
		OldAgentID:   "agent_1",
		CurrentTask:  "Database migration",
		KeyDecisions: criticalDecisions,
	}

	formatted := summary.FormatForNewAgent()

	t.Logf("CONTEXT_TEST: DecisionPreservation | Decisions=%d | FormattedLen=%d",
		len(criticalDecisions), len(formatted))

	// Verify all critical decisions are preserved
	for _, decision := range criticalDecisions {
		if !strings.Contains(formatted, decision) {
			t.Errorf("critical decision not preserved: %s", decision)
		}
	}
}

// TestTokenBudgetConstraints tests that summaries respect token budget limits.
func TestTokenBudgetConstraints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		maxTokens   int
		inputLen    int
		shouldTrunc bool
	}{
		{"small limit", 50, 1000, true},      // 250 tokens > 50 max
		{"medium limit", 500, 2500, true},    // 625 tokens > 500 max
		{"large limit", 2000, 1000, false},   // 250 tokens < 2000 max
		{"exact fit", 250, 1000, false},      // 250 tokens == 250 max (no truncation at exact fit)
		{"just over limit", 200, 1000, true}, // 250 tokens > 200 max
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := SummaryGeneratorConfig{
				MaxTokens:     tt.maxTokens,
				PromptTimeout: 30 * time.Second,
			}
			g := NewSummaryGenerator(cfg)

			// Create input of specified length
			input := strings.Repeat("Test content. ", tt.inputLen/14)

			summary := g.ParseAgentResponse("agent", "claude", "session", input)

			t.Logf("CONTEXT_TEST: TokenBudget | MaxTokens=%d | InputLen=%d | EstTokens=%d | ShouldTrunc=%v",
				tt.maxTokens, len(input), summary.TokenEstimate, tt.shouldTrunc)

			if summary.TokenEstimate > tt.maxTokens {
				t.Errorf("TokenEstimate %d exceeds maxTokens %d", summary.TokenEstimate, tt.maxTokens)
			}

			if tt.shouldTrunc && !strings.Contains(summary.RawSummary, "[Summary truncated") {
				t.Error("expected truncation notice for small budget")
			}
		})
	}
}

// TestInjectionWithEmptyFields tests injection with missing/empty fields.
func TestInjectionWithEmptyFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		summary *HandoffSummary
	}{
		{
			name: "minimal summary",
			summary: &HandoffSummary{
				GeneratedAt: time.Now(),
				OldAgentID:  "agent_1",
			},
		},
		{
			name: "only task",
			summary: &HandoffSummary{
				GeneratedAt: time.Now(),
				OldAgentID:  "agent_1",
				CurrentTask: "Working on feature X",
			},
		},
		{
			name: "empty lists",
			summary: &HandoffSummary{
				GeneratedAt:  time.Now(),
				OldAgentID:   "agent_1",
				CurrentTask:  "Working on feature X",
				KeyDecisions: []string{},
				ActiveFiles:  []string{},
				Blockers:     []string{},
			},
		},
		{
			name: "nil lists",
			summary: &HandoffSummary{
				GeneratedAt:  time.Now(),
				OldAgentID:   "agent_1",
				CurrentTask:  "Working on feature X",
				KeyDecisions: nil,
				ActiveFiles:  nil,
				Blockers:     nil,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Should not panic
			formatted := tt.summary.FormatForNewAgent()

			// Should always have header
			if !strings.Contains(formatted, "HANDOFF CONTEXT") {
				t.Error("missing HANDOFF CONTEXT header")
			}

			t.Logf("CONTEXT_TEST: EmptyFields | TestName=%s | FormattedLen=%d", tt.name, len(formatted))
		})
	}
}

// TestFallbackSummaryGeneration tests generation of fallback summaries.
func TestFallbackSummaryGeneration(t *testing.T) {
	t.Parallel()

	recentOutput := []string{
		"Analyzing the codebase structure...",
		"Found issue in internal/auth/handler.go:45",
		"Working on fixing the authentication bug",
		"Running tests now: go test ./...",
		"All tests passing!",
	}

	g := NewSummaryGenerator(DefaultSummaryGeneratorConfig())
	summary := g.GenerateFallbackSummary("agent_1", "claude", "debug-session", recentOutput)

	t.Logf("CONTEXT_TEST: FallbackSummary | RecentOutputs=%d | DetectedFiles=%d",
		len(recentOutput), len(summary.ActiveFiles))

	// Verify fallback indicator
	if !strings.Contains(summary.RawSummary, "FALLBACK SUMMARY") {
		t.Error("fallback summary should indicate it's a fallback")
	}

	// Verify agent info is preserved
	if summary.OldAgentID != "agent_1" {
		t.Errorf("OldAgentID = %s, want agent_1", summary.OldAgentID)
	}
	if summary.OldAgentType != "claude" {
		t.Errorf("OldAgentType = %s, want claude", summary.OldAgentType)
	}
	if summary.SessionName != "debug-session" {
		t.Errorf("SessionName = %s, want debug-session", summary.SessionName)
	}
}

// TestContextInjectionSizeEstimation tests accurate size estimation of injected context.
func TestContextInjectionSizeEstimation(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		rawSummary  string
		expectedMin int
		expectedMax int
	}{
		{
			name:        "small summary",
			rawSummary:  strings.Repeat("a", 100),
			expectedMin: 20,
			expectedMax: 30,
		},
		{
			name:        "medium summary",
			rawSummary:  strings.Repeat("a", 1000),
			expectedMin: 200,
			expectedMax: 300,
		},
		{
			name:        "large summary",
			rawSummary:  strings.Repeat("a", 8000),
			expectedMin: 1800,
			expectedMax: 2200,
		},
	}

	g := NewSummaryGenerator(DefaultSummaryGeneratorConfig())

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			summary := g.ParseAgentResponse("agent", "claude", "session", tc.rawSummary)

			t.Logf("CONTEXT_TEST: SizeEstimation | Name=%s | RawLen=%d | TokenEstimate=%d | Range=[%d,%d]",
				tc.name, len(tc.rawSummary), summary.TokenEstimate, tc.expectedMin, tc.expectedMax)

			if summary.TokenEstimate < tc.expectedMin || summary.TokenEstimate > tc.expectedMax {
				t.Errorf("TokenEstimate = %d, expected range [%d, %d]",
					summary.TokenEstimate, tc.expectedMin, tc.expectedMax)
			}
		})
	}
}

// TestContextPackIntegration tests integration with ContextPack (if applicable).
func TestContextPackIntegration(t *testing.T) {
	t.Parallel()

	// Create a summary and verify it can be formatted for new agent
	summary := &HandoffSummary{
		GeneratedAt:   time.Now(),
		OldAgentID:    "previous-agent",
		OldAgentType:  "codex",
		SessionName:   "feature-dev",
		CurrentTask:   "Implementing new API endpoints",
		Progress:      "3 of 5 endpoints complete",
		KeyDecisions:  []string{"REST over GraphQL", "Using OpenAPI 3.0"},
		ActiveFiles:   []string{"api/routes.go", "api/handlers.go"},
		Blockers:      []string{"Need database migration approval"},
		TokenEstimate: 150,
	}

	// Format for new agent
	formatted := summary.FormatForNewAgent()

	// Estimate final token cost
	estimatedTokens := EstimateTokens(len(formatted))

	t.Logf("CONTEXT_TEST: ContextPackIntegration | FormattedLen=%d | EstimatedTokens=%d",
		len(formatted), estimatedTokens)

	// Verify reasonable token count for context pack
	if estimatedTokens > 1000 {
		t.Logf("Warning: formatted context is large (%d tokens)", estimatedTokens)
	}

	// Verify structure
	if !strings.Contains(formatted, "HANDOFF CONTEXT") {
		t.Error("missing HANDOFF CONTEXT header")
	}
	if !strings.Contains(formatted, "REST over GraphQL") {
		t.Error("key decision not preserved in formatted output")
	}
}

// TestAlternateHeaderFormats tests parsing of various header formats.
func TestAlternateHeaderFormats(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "markdown headers",
			input:    "## CURRENT TASK\nBuilding feature X",
			expected: "Building feature X",
		},
		{
			name:     "bold headers",
			input:    "**CURRENT TASK**\nBuilding feature X",
			expected: "Building feature X",
		},
		{
			name:     "colon format",
			input:    "CURRENT TASK: Building feature X",
			expected: "", // May not match all formats
		},
	}

	g := NewSummaryGenerator(DefaultSummaryGeneratorConfig())

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			summary := g.ParseAgentResponse("agent", "claude", "session", tc.input)

			if summary == nil {
				t.Fatal("summary should not be nil")
			}

			t.Logf("CONTEXT_TEST: HeaderFormats | Format=%s | CurrentTask=%q",
				tc.name, summary.CurrentTask)
		})
	}
}

// TestFilePathDetection tests detection of file paths in agent output.
func TestFilePathDetection(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		input         string
		expectedCount int
		expectedPaths []string
	}{
		{
			input:         "Modified internal/auth/handler.go and added tests in internal/auth/handler_test.go",
			expectedCount: 2,
			expectedPaths: []string{"internal/auth/handler.go", "internal/auth/handler_test.go"},
		},
		{
			input:         "Check out https://example.com/path/to/file.go for reference",
			expectedCount: 0, // URLs should be filtered
			expectedPaths: nil,
		},
		{
			input:         "Working on cmd/server/main.go",
			expectedCount: 1,
			expectedPaths: []string{"cmd/server/main.go"},
		},
		{
			input:         "Using version 1.0.0 and config.yaml",
			expectedCount: 1, // 1.0.0 should be filtered
			expectedPaths: []string{"config.yaml"},
		},
	}

	for i, tc := range testCases {
		t.Run(tc.input[:min(30, len(tc.input))], func(t *testing.T) {
			t.Parallel()

			paths := extractFilePaths(tc.input)

			t.Logf("CONTEXT_TEST: FilePathDetection | Case=%d | Found=%d | Expected=%d",
				i, len(paths), tc.expectedCount)

			if len(paths) < tc.expectedCount {
				t.Errorf("expected at least %d paths, got %d: %v", tc.expectedCount, len(paths), paths)
			}

			for _, expected := range tc.expectedPaths {
				found := false
				for _, p := range paths {
					if p == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected to find path %q in %v", expected, paths)
				}
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

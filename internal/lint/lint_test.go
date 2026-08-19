package lint

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestLinterBasic(t *testing.T) {
	l := New()
	result := l.Lint("Hello, world!")

	if !result.Success {
		t.Errorf("expected success for benign prompt, got %v findings", len(result.Findings))
	}
	if result.Stats.ByteCount != 13 {
		t.Errorf("expected 13 bytes, got %d", result.Stats.ByteCount)
	}
	if result.Stats.LineCount != 1 {
		t.Errorf("expected 1 line, got %d", result.Stats.LineCount)
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input     string
		minTokens int
		maxTokens int
	}{
		{"", 0, 0},
		{"hello", 1, 3},
		{"hello world", 2, 5},
		{"The quick brown fox jumps over the lazy dog", 8, 15},
		{strings.Repeat("a", 1000), 200, 300},
	}

	for _, tt := range tests {
		estimate := EstimateTokens(tt.input)
		if estimate < tt.minTokens || estimate > tt.maxTokens {
			t.Errorf("EstimateTokens(%q) = %d, want between %d and %d",
				truncate(tt.input, 20), estimate, tt.minTokens, tt.maxTokens)
		}
	}
}

func TestCheckDestructive(t *testing.T) {
	tests := []struct {
		prompt  string
		wantLen int
		desc    string
	}{
		{"rm -rf /", 1, "delete root"},
		{"rm -rf ~", 1, "delete home"},
		{"rm -rf *", 1, "delete wildcard"},
		{"git reset --hard", 1, "git hard reset"},
		{"git push --force", 1, "git force push"},
		{"git push -f", 1, "git force push short"},
		{"git push --force-with-lease", 0, "force with lease is safe"},
		{"rm -rf node_modules", 0, "node_modules is safe"},
		{"rm -rf dist/", 0, "dist is safe"},
		{"DROP TABLE users", 1, "drop table"},
		{"kubectl delete ns production", 1, "k8s namespace delete"},
		{"echo hello", 0, "safe command"},
		{"git commit -m 'test'", 0, "git commit is safe"},
	}

	for _, tt := range tests {
		findings := CheckDestructive(tt.prompt, SeverityWarning)
		if len(findings) != tt.wantLen {
			t.Errorf("CheckDestructive(%q) [%s] got %d findings, want %d",
				tt.prompt, tt.desc, len(findings), tt.wantLen)
			for _, f := range findings {
				t.Logf("  finding: %s", f.Message)
			}
		}
	}
}

func TestCheckSize(t *testing.T) {
	rules := DefaultRuleSet()

	// Test under warning threshold
	smallPrompt := "Hello"
	findings := CheckSize(smallPrompt, rules)
	if len(findings) != 0 {
		t.Errorf("small prompt should have no findings, got %d", len(findings))
	}

	// Test over warning threshold but under max
	setRuleConfigForTest(rules, RuleOversizedPromptBytes, ConfigKeyWarnBytes, 10)
	setRuleConfigForTest(rules, RuleOversizedPromptBytes, ConfigKeyMaxBytes, 100)
	mediumPrompt := strings.Repeat("a", 50)
	findings = CheckSize(mediumPrompt, rules)
	if len(findings) != 1 {
		t.Errorf("medium prompt should have 1 warning finding, got %d", len(findings))
	} else if findings[0].Severity != SeverityWarning {
		t.Errorf("expected warning severity, got %s", findings[0].Severity)
	}

	// Test over max threshold
	largePrompt := strings.Repeat("b", 150)
	findings = CheckSize(largePrompt, rules)
	var hasError bool
	for _, f := range findings {
		if f.Severity == SeverityError {
			hasError = true
		}
	}
	if !hasError {
		t.Error("large prompt should have error severity finding")
	}
}

func TestCheckMissingContext(t *testing.T) {
	rules := DefaultRuleSet()

	// Disabled by default
	findings := CheckMissingContext("prompt without tags", rules)
	if len(findings) != 0 {
		t.Error("disabled rule should produce no findings")
	}

	// Enable and configure
	enableRuleForTest(rules, RuleMissingContext)
	setRuleConfigForTest(rules, RuleMissingContext, ConfigKeyRequiredTags, []string{"[CONTEXT]", "[TASK]"})

	// Missing all tags
	findings = CheckMissingContext("prompt without tags", rules)
	if len(findings) != 2 {
		t.Errorf("expected 2 missing tag findings, got %d", len(findings))
	}

	// Has one tag
	findings = CheckMissingContext("[CONTEXT] some context here", rules)
	if len(findings) != 1 {
		t.Errorf("expected 1 missing tag finding, got %d", len(findings))
	}

	// Has all tags
	findings = CheckMissingContext("[CONTEXT] context [TASK] task", rules)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings when all tags present, got %d", len(findings))
	}
}

func TestLinterSecrets(t *testing.T) {
	l := New()

	// Test with a fake API key pattern
	prompt := `Here is my config:
API_KEY=sk-ant-api03-abcdefghijklmnopqrstuvwxyz123456789012345678901234567890`

	result := l.Lint(prompt)

	secretFindings := findingsByIDForTest(result, RuleSecretDetected)
	if len(secretFindings) == 0 {
		t.Error("expected secret detection for API key pattern")
	}
}

func TestLinterPII(t *testing.T) {
	l := New()

	tests := []struct {
		prompt  string
		wantPII bool
		piiType string
	}{
		{"Contact me at test@example.com", true, "email_address"},
		{"Call me at 555-123-4567", true, "phone_number"},
		{"My SSN is 123-45-6789", true, "ssn"},
		{"No personal info here", false, ""},
	}

	for _, tt := range tests {
		result := l.Lint(tt.prompt)
		piiFindings := findingsByIDForTest(result, RulePIIDetected)

		if tt.wantPII && len(piiFindings) == 0 {
			t.Errorf("expected PII detection for %q", tt.prompt)
		}
		if !tt.wantPII && len(piiFindings) > 0 {
			t.Errorf("unexpected PII detection for %q: %v", tt.prompt, piiFindings)
		}
	}
}

func TestStrictRuleSet(t *testing.T) {
	strict := StrictRuleSet()

	// Verify escalated severities
	if strict.Rules[RuleOversizedPromptBytes].Severity != SeverityError {
		t.Error("strict mode should have error severity for oversized bytes")
	}
	if strict.Rules[RuleDestructiveCommand].Severity != SeverityError {
		t.Error("strict mode should have error severity for destructive commands")
	}

	// Verify missing context is enabled
	if !strict.Rules[RuleMissingContext].Enabled {
		t.Error("strict mode should enable missing context rule")
	}
}

func TestCompilePattern_Concurrent(t *testing.T) {
	// This test intentionally runs pattern compilation concurrently to ensure
	// the internal regex cache is goroutine-safe (important for concurrent REST/robot usage).
	patternCacheMu.Lock()
	patternCache = make(map[string]*compiledPattern)
	patternCacheMu.Unlock()

	const goroutines = 128
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start
			_, _ = compilePattern(fmt.Sprintf("concurrent_%d", i))
		}()
	}

	close(start)
	wg.Wait()
}

// =============================================================================
// RuleSet.Disable (bd-8gkp7)
// =============================================================================

func TestRuleSet_Disable(t *testing.T) {
	t.Parallel()
	rs := DefaultRuleSet()

	// Verify rule is enabled by default
	if !rs.Rules[RuleSecretDetected].Enabled {
		t.Fatal("RuleSecretDetected should be enabled by default")
	}

	// Disable it
	rs.Disable(RuleSecretDetected)
	if rs.Rules[RuleSecretDetected].Enabled {
		t.Error("RuleSecretDetected should be disabled after Disable()")
	}

	// Re-enable it
	enableRuleForTest(rs, RuleSecretDetected)
	if !rs.Rules[RuleSecretDetected].Enabled {
		t.Error("RuleSecretDetected should be re-enabled after Enable()")
	}
}

func TestRuleSet_Disable_UnknownRule(t *testing.T) {
	t.Parallel()
	rs := DefaultRuleSet()

	// Should not panic for unknown rule ID
	rs.Disable(RuleID("nonexistent-rule"))
}

// TestHasErrors_NoErrors tests the false branch of HasErrors (no errors present).
func TestHasErrors_NoErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		findings []Finding
	}{
		{"empty findings", nil},
		{"only warnings", []Finding{
			{ID: RuleDestructiveCommand, Severity: SeverityWarning},
		}},
		{"only info", []Finding{
			{ID: RuleMissingContext, Severity: SeverityInfo},
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := &Result{Findings: tc.findings}
			if result.HasErrors() {
				t.Error("HasErrors() = true, want false")
			}
		})
	}
}

// TestIsSafeMatch_Branches tests both branches of isSafeMatch.
func TestIsSafeMatch_Branches(t *testing.T) {
	t.Parallel()

	// Safe matches (force-with-lease is in safe patterns)
	safeInputs := []string{
		"git push --force-with-lease",
	}
	for _, input := range safeInputs {
		if !isSafeMatch(input) {
			t.Errorf("isSafeMatch(%q) = false, want true", input)
		}
	}

	// Unsafe matches (no safe pattern applies)
	unsafeInputs := []string{
		"rm -rf /",
		"git push --force",
		"random text",
		"",
	}
	for _, input := range unsafeInputs {
		if isSafeMatch(input) {
			t.Errorf("isSafeMatch(%q) = true, want false", input)
		}
	}
}

// =============================================================================
// SetConfig — all branches (bd-4b4zf)
// =============================================================================

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// =============================================================================
// LintWithRedaction — with configured redactor (bd-1ced7)
// =============================================================================

// =============================================================================
// getConfigInt — all branches (bd-1ced7)
// =============================================================================

func TestGetConfigInt_NilConfig(t *testing.T) {
	t.Parallel()
	got := getConfigInt(nil, "key", 42)
	if got != 42 {
		t.Errorf("nil config: got %d, want 42", got)
	}
}

func TestGetConfigInt_MissingKey(t *testing.T) {
	t.Parallel()
	cfg := map[string]any{"other": 10}
	got := getConfigInt(cfg, "missing", 99)
	if got != 99 {
		t.Errorf("missing key: got %d, want 99", got)
	}
}

func TestGetConfigInt_IntValue(t *testing.T) {
	t.Parallel()
	cfg := map[string]any{"key": 123}
	got := getConfigInt(cfg, "key", 0)
	if got != 123 {
		t.Errorf("int value: got %d, want 123", got)
	}
}

func TestGetConfigInt_Int64Value(t *testing.T) {
	t.Parallel()
	cfg := map[string]any{"key": int64(456)}
	got := getConfigInt(cfg, "key", 0)
	if got != 456 {
		t.Errorf("int64 value: got %d, want 456", got)
	}
}

func TestGetConfigInt_Float64Value(t *testing.T) {
	t.Parallel()
	cfg := map[string]any{"key": float64(789)}
	got := getConfigInt(cfg, "key", 0)
	if got != 789 {
		t.Errorf("float64 value: got %d, want 789", got)
	}
}

func TestGetConfigInt_UnknownType(t *testing.T) {
	t.Parallel()
	cfg := map[string]any{"key": "not_a_number"}
	got := getConfigInt(cfg, "key", 55)
	if got != 55 {
		t.Errorf("string value: got %d, want 55 (default)", got)
	}
}

// =============================================================================
// findPatternMatches — uncovered branches (bd-1ced7)
// =============================================================================

func TestLint_OversizedPromptBytes(t *testing.T) {
	t.Parallel()
	rs := DefaultRuleSet()
	// Set a very low byte warning threshold
	setRuleConfigForTest(rs, RuleOversizedPromptBytes, ConfigKeyWarnBytes, 10)
	l := New(WithRuleSet(rs))

	result := l.Lint("This is a prompt that exceeds the byte threshold by a lot")
	findings := findingsByIDForTest(result, RuleOversizedPromptBytes)
	if len(findings) == 0 {
		t.Error("expected oversized prompt finding")
	}
}

func TestLint_OversizedPromptTokens(t *testing.T) {
	t.Parallel()
	rs := DefaultRuleSet()
	// Set a very low token threshold
	setRuleConfigForTest(rs, RuleOversizedPromptTokens, ConfigKeyWarnTokens, 1)
	l := New(WithRuleSet(rs))

	result := l.Lint("This is a prompt that should exceed 1 token threshold easily")
	findings := findingsByIDForTest(result, RuleOversizedPromptTokens)
	if len(findings) == 0 {
		t.Error("expected oversized prompt tokens finding")
	}
}

// Test-local helpers standing in for removed RuleSet/Result convenience methods.
func setRuleConfigForTest(rs *RuleSet, id RuleID, key string, value any) {
	rule, ok := rs.Rules[id]
	if !ok {
		return
	}
	if rule.Config == nil {
		rule.Config = make(map[string]any)
	}
	rule.Config[key] = value
}

func enableRuleForTest(rs *RuleSet, id RuleID) {
	if rule, ok := rs.Rules[id]; ok {
		rule.Enabled = true
	}
}

func findingsByIDForTest(r *Result, id RuleID) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.ID == id {
			out = append(out, f)
		}
	}
	return out
}

func findingsBySeverityForTest(r *Result, severity Severity) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Severity == severity {
			out = append(out, f)
		}
	}
	return out
}

func hasWarningsForTest(r *Result) bool {
	for _, f := range r.Findings {
		if f.Severity == SeverityWarning {
			return true
		}
	}
	return false
}

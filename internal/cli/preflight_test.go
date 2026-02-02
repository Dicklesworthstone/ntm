package cli

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/lint"
)

// TestPreflightTableDriven covers the core preflight scenarios with table-driven tests.
func TestPreflightTableDriven(t *testing.T) {
	tests := []struct {
		name          string
		prompt        string
		strict        bool
		wantSuccess   bool
		wantFindingID string
		wantMinCount  int
		wantSeverity  string
		checkMetadata func(map[string]any) bool
	}{
		{
			name:          "benign_prompt",
			prompt:        "Hello, this is a safe prompt",
			strict:        false,
			wantSuccess:   true,
			wantFindingID: "",
			wantMinCount:  0,
		},
		{
			name:          "destructive_rm_rf",
			prompt:        "Run rm -rf / to clean up",
			strict:        false,
			wantSuccess:   true, // warnings don't block in default mode
			wantFindingID: "destructive_command",
			wantMinCount:  1,
			wantSeverity:  "warning",
		},
		{
			name:          "destructive_git_reset_hard",
			prompt:        "Execute git reset --hard HEAD~5",
			strict:        false,
			wantSuccess:   true,
			wantFindingID: "destructive_command",
			wantMinCount:  1,
			wantSeverity:  "warning",
		},
		{
			name:          "destructive_drop_table",
			prompt:        "DROP TABLE users;",
			strict:        false,
			wantSuccess:   true,
			wantFindingID: "destructive_command",
			wantMinCount:  1,
			wantSeverity:  "warning",
		},
		{
			name:          "destructive_git_force_push",
			prompt:        "git push --force origin main",
			strict:        false,
			wantSuccess:   true,
			wantFindingID: "destructive_command",
			wantMinCount:  1,
			wantSeverity:  "warning",
		},
		{
			name:          "destructive_strict_mode_blocks",
			prompt:        "rm -rf /tmp/important",
			strict:        true,
			wantSuccess:   false, // strict mode escalates to error
			wantFindingID: "destructive_command",
			wantMinCount:  1,
			wantSeverity:  "error",
		},
		{
			name:          "safe_force_with_lease",
			prompt:        "git push --force-with-lease",
			strict:        false,
			wantSuccess:   true,
			wantFindingID: "",
			wantMinCount:  0,
		},
		{
			name:          "safe_soft_reset",
			prompt:        "git reset --soft HEAD~1",
			strict:        false,
			wantSuccess:   true,
			wantFindingID: "",
			wantMinCount:  0,
		},
		{
			name:          "safe_rm_node_modules",
			prompt:        "rm -rf node_modules",
			strict:        false,
			wantSuccess:   true,
			wantFindingID: "",
			wantMinCount:  0,
		},
		{
			name:          "pii_email_detected",
			prompt:        "Contact john@example.com for help",
			strict:        false,
			wantSuccess:   true,
			wantFindingID: "pii_detected",
			wantMinCount:  1,
			wantSeverity:  "warning",
		},
		{
			name:          "pii_phone_detected",
			prompt:        "Call me at 555-123-4567",
			strict:        false,
			wantSuccess:   true,
			wantFindingID: "pii_detected",
			wantMinCount:  1,
			wantSeverity:  "warning",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := runPreflight(tt.prompt, tt.strict)
			if err != nil {
				t.Fatalf("runPreflight failed: %v", err)
			}

			if result.Success != tt.wantSuccess {
				t.Errorf("success = %v, want %v", result.Success, tt.wantSuccess)
			}

			if tt.wantFindingID != "" {
				var found bool
				for _, f := range result.Findings {
					if f.ID == tt.wantFindingID {
						found = true
						if tt.wantSeverity != "" && f.Severity != tt.wantSeverity {
							t.Errorf("finding severity = %s, want %s", f.Severity, tt.wantSeverity)
						}
						if tt.checkMetadata != nil && !tt.checkMetadata(f.Metadata) {
							t.Error("metadata check failed")
						}
						break
					}
				}
				if !found {
					t.Errorf("expected finding with ID %q not found", tt.wantFindingID)
					t.Logf("findings: %+v", result.Findings)
				}
			}

			if len(result.Findings) < tt.wantMinCount {
				t.Errorf("got %d findings, want at least %d", len(result.Findings), tt.wantMinCount)
			}
		})
	}
}

// TestPreflightSecretDetection tests secret detection with synthetic fixtures.
func TestPreflightSecretDetection(t *testing.T) {
	tests := []struct {
		name         string
		prompt       string
		wantSecret   bool
		wantCategory string
	}{
		{
			name:         "anthropic_api_key",
			prompt:       "Use key sk-ant-api03-abcdefghijklmnopqrstuvwxyz1234567890abcdefghijklmnop",
			wantSecret:   true,
			wantCategory: "ANTHROPIC_KEY",
		},
		{
			name:         "github_token_ghp",
			prompt:       "Token: ghp_1234567890abcdefghijklmnopqrstuvwx",
			wantSecret:   true,
			wantCategory: "GITHUB_TOKEN",
		},
		{
			name:         "aws_access_key",
			prompt:       "AWS key: AKIAIOSFODNN7EXAMPLE",
			wantSecret:   true,
			wantCategory: "AWS_ACCESS_KEY",
		},
		{
			name:         "jwt_token",
			prompt:       "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
			wantSecret:   true,
			wantCategory: "JWT",
		},
		{
			name:       "no_secrets",
			prompt:     "This is a safe prompt without any secrets",
			wantSecret: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := runPreflight(tt.prompt, false)
			if err != nil {
				t.Fatalf("runPreflight failed: %v", err)
			}

			var foundSecret bool
			var foundCategory string
			for _, f := range result.Findings {
				if f.ID == "secret_detected" {
					foundSecret = true
					if cat, ok := f.Metadata["category"].(string); ok {
						foundCategory = cat
					}
					break
				}
			}

			if tt.wantSecret != foundSecret {
				t.Errorf("secret detected = %v, want %v", foundSecret, tt.wantSecret)
			}

			if tt.wantSecret && tt.wantCategory != "" && foundCategory != tt.wantCategory {
				t.Errorf("secret category = %q, want %q", foundCategory, tt.wantCategory)
			}
		})
	}
}

// TestPreflightOversizeWarning tests the oversize prompt warning threshold.
func TestPreflightOversizeWarning(t *testing.T) {
	// Create a prompt that exceeds the warning threshold (50KB default)
	// but not the max threshold (100KB default)
	largePrompt := strings.Repeat("This is test content. ", 3000) // ~66KB

	result, err := runPreflight(largePrompt, false)
	if err != nil {
		t.Fatalf("runPreflight failed: %v", err)
	}

	var foundOversizeWarning bool
	for _, f := range result.Findings {
		if f.ID == "oversized_prompt_bytes" || f.ID == "oversized_prompt_tokens" {
			foundOversizeWarning = true
			if f.Severity != "warning" {
				t.Errorf("expected warning severity for oversize, got %s", f.Severity)
			}
			break
		}
	}

	if !foundOversizeWarning {
		t.Error("expected oversize warning for large prompt")
		t.Logf("prompt size: %d bytes, findings: %+v", len(largePrompt), result.Findings)
	}
}

// TestPreflightHashStability verifies that preview hash is stable for identical inputs.
func TestPreflightHashStability(t *testing.T) {
	prompt := "Test prompt for hash stability verification"

	// Run preflight multiple times
	hashes := make([]string, 5)
	for i := 0; i < 5; i++ {
		result, err := runPreflight(prompt, false)
		if err != nil {
			t.Fatalf("runPreflight failed on iteration %d: %v", i, err)
		}
		hashes[i] = result.PreviewHash
	}

	// All hashes should be identical
	for i := 1; i < len(hashes); i++ {
		if hashes[i] != hashes[0] {
			t.Errorf("hash mismatch: iteration 0 = %s, iteration %d = %s", hashes[0], i, hashes[i])
		}
	}

	// Different prompt should have different hash
	differentResult, err := runPreflight("A completely different prompt", false)
	if err != nil {
		t.Fatalf("runPreflight failed for different prompt: %v", err)
	}

	if differentResult.PreviewHash == hashes[0] {
		t.Error("different prompts should have different hashes")
	}
}

// TestPreflightResultStructure verifies the result structure matches robot output spec.
func TestPreflightResultStructure(t *testing.T) {
	result, err := runPreflight("Test prompt", false)
	if err != nil {
		t.Fatalf("runPreflight failed: %v", err)
	}

	// Required fields from robot spec
	if result.Timestamp == "" {
		t.Error("timestamp is required")
	}
	if result.Version == "" {
		t.Error("version is required")
	}

	// Preflight-specific required fields
	if result.PreviewHash == "" {
		t.Error("preview_hash is required")
	}
	if result.PreviewLen == 0 {
		t.Error("preview_len should be non-zero")
	}
	if result.Findings == nil {
		t.Error("findings should not be nil (use empty slice)")
	}

	// Counts should be consistent with findings
	var errCount, warnCount, infoCount int
	for _, f := range result.Findings {
		switch f.Severity {
		case "error":
			errCount++
		case "warning":
			warnCount++
		case "info":
			infoCount++
		}
	}

	if result.ErrorCount != errCount {
		t.Errorf("error_count = %d, but counted %d errors", result.ErrorCount, errCount)
	}
	if result.WarningCount != warnCount {
		t.Errorf("warning_count = %d, but counted %d warnings", result.WarningCount, warnCount)
	}
	if result.InfoCount != infoCount {
		t.Errorf("info_count = %d, but counted %d info", result.InfoCount, infoCount)
	}
}

// TestPreflightHelperFunction tests the RunPreflightCheck helper used by send command.
func TestPreflightHelperFunction(t *testing.T) {
	tests := []struct {
		name         string
		prompt       string
		strict       bool
		wantBlocked  bool
		wantWarnings bool
	}{
		{
			name:         "benign_not_blocked",
			prompt:       "Simple safe prompt",
			strict:       false,
			wantBlocked:  false,
			wantWarnings: false,
		},
		{
			name:         "destructive_not_blocked_default",
			prompt:       "rm -rf /tmp/cache",
			strict:       false,
			wantBlocked:  false,
			wantWarnings: true,
		},
		{
			name:         "destructive_blocked_strict",
			prompt:       "rm -rf /tmp/cache",
			strict:       true,
			wantBlocked:  true,
			wantWarnings: true, // warnings array still populated
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocked, warnings, err := RunPreflightCheck(tt.prompt, tt.strict)
			if err != nil {
				t.Fatalf("RunPreflightCheck failed: %v", err)
			}

			if blocked != tt.wantBlocked {
				t.Errorf("blocked = %v, want %v", blocked, tt.wantBlocked)
			}

			hasWarnings := len(warnings) > 0
			if hasWarnings != tt.wantWarnings {
				t.Errorf("has warnings = %v, want %v", hasWarnings, tt.wantWarnings)
			}
		})
	}
}

// TestLintPackageIntegration verifies the preflight uses the lint package correctly.
func TestLintPackageIntegration(t *testing.T) {
	// Test that lint package functions are being called
	// by checking behavior that comes from the lint package

	// Test token estimation (from lint.EstimateTokens)
	prompt := "Hello world test prompt"
	result, err := runPreflight(prompt, false)
	if err != nil {
		t.Fatalf("runPreflight failed: %v", err)
	}

	// Token estimate should be roughly chars/4
	expectedMin := len(prompt) / 6
	expectedMax := len(prompt) / 2
	if result.EstimatedTokens < expectedMin || result.EstimatedTokens > expectedMax {
		t.Errorf("token estimate %d out of expected range [%d, %d]",
			result.EstimatedTokens, expectedMin, expectedMax)
	}

	// Test that strict mode changes severities (from lint.StrictRuleSet)
	destructivePrompt := "rm -rf /"
	normalResult, _ := runPreflight(destructivePrompt, false)
	strictResult, _ := runPreflight(destructivePrompt, true)

	var normalSeverity, strictSeverity string
	for _, f := range normalResult.Findings {
		if f.ID == "destructive_command" {
			normalSeverity = f.Severity
			break
		}
	}
	for _, f := range strictResult.Findings {
		if f.ID == "destructive_command" {
			strictSeverity = f.Severity
			break
		}
	}

	if normalSeverity != "warning" {
		t.Errorf("normal mode should have warning severity, got %s", normalSeverity)
	}
	if strictSeverity != "error" {
		t.Errorf("strict mode should have error severity, got %s", strictSeverity)
	}
}

// Verify lint package imports work
var _ = lint.DefaultRuleSet

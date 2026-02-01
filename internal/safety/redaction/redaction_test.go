package redaction

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// Reset patterns to ensure fresh compilation
	ResetPatterns()
	os.Exit(m.Run())
}

// testOpenAIKey constructs a fake OpenAI API key for testing.
// Split into parts to avoid triggering GitHub's secret scanner.
func testOpenAIKey() string {
	// Format: sk-{20 chars}T3BlbkFJ{24 chars}
	return "sk-abc123defghijklmnop" + "T3Blbk" + "FJ" + "xyz789abcdefghijklmnop"
}

// testOpenAILegacyKey constructs a fake legacy OpenAI API key for testing.
// Split into parts to avoid triggering secret scanners.
func testOpenAILegacyKey() string {
	// Format: sk-{48 chars}
	return "sk-" + "abc123defghijklmnop" + "qrstuvwxyz0123456789" + "ABCDE12345"
}

// TestFixtures holds the test fixture data structure
type TestFixtures struct {
	Version        string `json:"version"`
	TruePositives  []struct {
		Input           string   `json:"input"`
		ExpectedCategory string  `json:"expected_category"`
		Description     string   `json:"description"`
	} `json:"true_positives"`
	TrueNegatives []struct {
		Input       string `json:"input"`
		Description string `json:"description"`
	} `json:"true_negatives"`
	EdgeCases []struct {
		Input              string   `json:"input"`
		ExpectedCategory   string   `json:"expected_category,omitempty"`
		ExpectedCategories []string `json:"expected_categories,omitempty"`
		Description        string   `json:"description"`
	} `json:"edge_cases"`
}

func loadFixtures(t *testing.T) *TestFixtures {
	t.Helper()

	// Look for fixtures in testdata directory
	path := filepath.Join("..", "..", "..", "testdata", "redaction_fixtures.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("fixtures not found at %s: %v", path, err)
	}

	// Replace placeholder with actual test key to avoid GitHub secret scanning
	// The placeholder is used in the JSON file to avoid triggering push protection
	dataStr := strings.ReplaceAll(string(data), "<<OPENAI_TEST_KEY>>", testOpenAIKey())
	dataStr = strings.ReplaceAll(dataStr, "<<OPENAI_LEGACY_KEY>>", testOpenAILegacyKey())

	var fixtures TestFixtures
	if err := json.Unmarshal([]byte(dataStr), &fixtures); err != nil {
		t.Fatalf("failed to parse fixtures: %v", err)
	}
	return &fixtures
}

func TestScanAndRedact_TruePositives(t *testing.T) {
	fixtures := loadFixtures(t)
	cfg := DefaultConfig()
	cfg.Mode = ModeWarn

	for _, tc := range fixtures.TruePositives {
		t.Run(tc.Description, func(t *testing.T) {
			result := ScanAndRedact(tc.Input, cfg)

			if len(result.Findings) == 0 {
				t.Errorf("expected to detect %s, got no findings", tc.ExpectedCategory)
				return
			}

			// Check that the expected category was found
			found := false
			for _, f := range result.Findings {
				if string(f.Category) == tc.ExpectedCategory {
					found = true
					break
				}
			}
			if !found {
				categories := make([]string, len(result.Findings))
				for i, f := range result.Findings {
					categories[i] = string(f.Category)
				}
				t.Errorf("expected category %s, got %v", tc.ExpectedCategory, categories)
			}
		})
	}
}

func TestScanAndRedact_TrueNegatives(t *testing.T) {
	fixtures := loadFixtures(t)
	cfg := DefaultConfig()
	cfg.Mode = ModeWarn

	for _, tc := range fixtures.TrueNegatives {
		t.Run(tc.Description, func(t *testing.T) {
			result := ScanAndRedact(tc.Input, cfg)

			if len(result.Findings) > 0 {
				categories := make([]string, len(result.Findings))
				for i, f := range result.Findings {
					categories[i] = string(f.Category)
				}
				t.Errorf("expected no findings, got %v", categories)
			}
		})
	}
}

func TestScanAndRedact_ModeOff(t *testing.T) {
	input := testOpenAIKey()
	cfg := Config{Mode: ModeOff}

	result := ScanAndRedact(input, cfg)

	if result.Output != input {
		t.Errorf("expected output unchanged, got %s", result.Output)
	}
	if len(result.Findings) != 0 {
		t.Errorf("expected no findings in off mode, got %d", len(result.Findings))
	}
}

func TestScanAndRedact_ModeWarn(t *testing.T) {
	input := "key: " + testOpenAIKey()
	cfg := Config{Mode: ModeWarn}

	result := ScanAndRedact(input, cfg)

	if result.Output != input {
		t.Errorf("expected output unchanged in warn mode")
	}
	if len(result.Findings) == 0 {
		t.Error("expected findings in warn mode")
	}
	if result.Blocked {
		t.Error("should not be blocked in warn mode")
	}
}

func TestScanAndRedact_ModeRedact(t *testing.T) {
	input := "key: " + testOpenAIKey()
	cfg := Config{Mode: ModeRedact}

	result := ScanAndRedact(input, cfg)

	if strings.Contains(result.Output, "sk-abc") {
		t.Error("expected key to be redacted")
	}
	if !strings.Contains(result.Output, "[REDACTED:OPENAI_KEY:") {
		t.Errorf("expected redaction placeholder, got %s", result.Output)
	}
	if len(result.Findings) == 0 {
		t.Error("expected findings")
	}
}

func TestScanAndRedact_ModeBlock(t *testing.T) {
	input := testOpenAIKey()
	cfg := Config{Mode: ModeBlock}

	result := ScanAndRedact(input, cfg)

	if !result.Blocked {
		t.Error("expected blocked=true")
	}
	if result.Output != input {
		t.Error("output should be unchanged in block mode")
	}
}

func TestScanAndRedact_Allowlist(t *testing.T) {
	// Test that allowlisted patterns are not flagged
	input := testOpenAIKey()
	cfg := Config{
		Mode:      ModeWarn,
		Allowlist: []string{`sk-abc123.*`}, // Pattern that matches our test key
	}

	result := ScanAndRedact(input, cfg)

	// The key should be allowlisted and not reported
	if len(result.Findings) > 0 {
		t.Errorf("expected allowlisted key to not be flagged, got %d findings", len(result.Findings))
	}
}

func TestScanAndRedact_Allowlist_NoMatch(t *testing.T) {
	// Test that non-matching allowlist doesn't suppress findings
	input := testOpenAIKey()
	cfg := Config{
		Mode:      ModeWarn,
		Allowlist: []string{`sk-DIFFERENT.*`}, // Pattern that doesn't match
	}

	result := ScanAndRedact(input, cfg)

	// The key should still be detected
	if len(result.Findings) == 0 {
		t.Error("expected key to be detected when allowlist doesn't match")
	}
}

func TestScanAndRedact_DisabledCategories(t *testing.T) {
	input := testOpenAIKey() + " AKIAIOSFODNN7EXAMPLE"
	cfg := Config{
		Mode: ModeWarn,
		DisabledCategories: []Category{CategoryOpenAIKey},
	}

	result := ScanAndRedact(input, cfg)

	for _, f := range result.Findings {
		if f.Category == CategoryOpenAIKey {
			t.Error("OpenAI key should be disabled")
		}
	}

	// AWS key should still be detected
	found := false
	for _, f := range result.Findings {
		if f.Category == CategoryAWSAccessKey {
			found = true
			break
		}
	}
	if !found {
		t.Error("AWS key should be detected")
	}
}

func TestGeneratePlaceholder_Deterministic(t *testing.T) {
	cat := CategoryOpenAIKey
	content := "sk-test123"

	p1 := generatePlaceholder(cat, content)
	p2 := generatePlaceholder(cat, content)

	if p1 != p2 {
		t.Errorf("placeholders should be deterministic: %s != %s", p1, p2)
	}
}

func TestGeneratePlaceholder_Format(t *testing.T) {
	cat := CategoryJWT
	content := "eyJtest"

	p := generatePlaceholder(cat, content)

	// Expected format: [REDACTED:JWT:a1b2c3d4]
	if !strings.HasPrefix(p, "[REDACTED:JWT:") {
		t.Errorf("placeholder should start with [REDACTED:JWT:, got %s", p)
	}
	if !strings.HasSuffix(p, "]") {
		t.Errorf("placeholder should end with ], got %s", p)
	}

	// Extract hash: remove prefix "[REDACTED:JWT:" and suffix "]"
	prefix := "[REDACTED:JWT:"
	hash := strings.TrimSuffix(strings.TrimPrefix(p, prefix), "]")
	if len(hash) != 8 {
		t.Errorf("hash should be 8 hex chars, got %d chars: %s", len(hash), hash)
	}

	// Verify hash is valid hex
	for _, c := range hash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("hash should be lowercase hex, got: %s", hash)
			break
		}
	}
}

func TestAddLineInfo(t *testing.T) {
	input := "line1\nkey: " + testOpenAIKey() + "\nline3"
	cfg := Config{Mode: ModeWarn}

	result := ScanAndRedact(input, cfg)
	AddLineInfo(input, result.Findings)

	if len(result.Findings) == 0 {
		t.Fatal("expected findings")
	}

	f := result.Findings[0]
	if f.Line != 2 {
		t.Errorf("expected line 2, got %d", f.Line)
	}
	if f.Column < 5 {
		t.Errorf("expected column >= 5, got %d", f.Column)
	}
}

func TestContainsSensitive(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"normal text", false},
		{testOpenAIKey(), true},
		{"AKIAIOSFODNN7EXAMPLE", true},
		{"", false},
	}

	cfg := DefaultConfig()
	for _, tc := range tests {
		result := ContainsSensitive(tc.input, cfg)
		if result != tc.expected {
			t.Errorf("ContainsSensitive(%q) = %v, want %v", tc.input, result, tc.expected)
		}
	}
}

func TestScan(t *testing.T) {
	input := "key: " + testOpenAIKey()
	cfg := DefaultConfig()

	findings := Scan(input, cfg)

	if len(findings) == 0 {
		t.Error("expected findings")
	}
}

func TestRedact(t *testing.T) {
	input := "key: " + testOpenAIKey()
	cfg := DefaultConfig()

	output, findings := Redact(input, cfg)

	if strings.Contains(output, "sk-abc") {
		t.Error("key should be redacted")
	}
	if len(findings) == 0 {
		t.Error("expected findings")
	}
}

func TestMultipleFindings(t *testing.T) {
	input := "OPENAI=" + testOpenAIKey() + " AWS=AKIAIOSFODNN7EXAMPLE"
	cfg := Config{Mode: ModeWarn}

	result := ScanAndRedact(input, cfg)

	if len(result.Findings) < 2 {
		t.Errorf("expected at least 2 findings, got %d", len(result.Findings))
	}

	categories := make(map[Category]bool)
	for _, f := range result.Findings {
		categories[f.Category] = true
	}

	if !categories[CategoryOpenAIKey] {
		t.Error("expected OPENAI_KEY finding")
	}
	if !categories[CategoryAWSAccessKey] {
		t.Error("expected AWS_ACCESS_KEY finding")
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		mode    Mode
		wantErr bool
	}{
		{ModeOff, false},
		{ModeWarn, false},
		{ModeRedact, false},
		{ModeBlock, false},
		{"invalid", true},
	}

	for _, tc := range tests {
		cfg := Config{Mode: tc.mode}
		err := cfg.Validate()
		if (err != nil) != tc.wantErr {
			t.Errorf("Validate(mode=%q) error = %v, wantErr %v", tc.mode, err, tc.wantErr)
		}
	}
}

func TestOverlappingMatches(t *testing.T) {
	// Test that overlapping patterns are handled correctly
	// Higher priority patterns should take precedence

	// This string could match both GENERIC_SECRET and OPENAI_KEY
	// OPENAI_KEY has higher priority and should win
	input := "token=" + testOpenAIKey()
	cfg := Config{Mode: ModeRedact}

	result := ScanAndRedact(input, cfg)

	// Should only have one finding (OpenAI key, not generic secret)
	if len(result.Findings) != 1 {
		t.Errorf("expected exactly 1 finding (no overlaps), got %d", len(result.Findings))
		for _, f := range result.Findings {
			t.Logf("  - %s at [%d:%d]", f.Category, f.Start, f.End)
		}
	}

	// The finding should be categorized as OPENAI_KEY (higher priority)
	if len(result.Findings) > 0 && result.Findings[0].Category != CategoryOpenAIKey {
		t.Errorf("expected OPENAI_KEY category, got %s", result.Findings[0].Category)
	}

	// Verify redaction was applied
	if strings.Contains(result.Output, "sk-abc") {
		t.Error("expected key to be redacted in output")
	}
}

func TestDeduplicationPreservesOrder(t *testing.T) {
	// Multiple non-overlapping secrets should all be found and in order
	input := "first=AKIAIOSFODNN7EXAMPLE second=" + testOpenAIKey()
	cfg := Config{Mode: ModeWarn}

	result := ScanAndRedact(input, cfg)

	if len(result.Findings) != 2 {
		t.Errorf("expected 2 findings, got %d", len(result.Findings))
		return
	}

	// First finding should be AWS (earlier in string)
	if result.Findings[0].Category != CategoryAWSAccessKey {
		t.Errorf("first finding should be AWS, got %s", result.Findings[0].Category)
	}

	// Second should be OpenAI
	if result.Findings[1].Category != CategoryOpenAIKey {
		t.Errorf("second finding should be OPENAI_KEY, got %s", result.Findings[1].Category)
	}

	// Positions should be in order
	if result.Findings[0].Start >= result.Findings[1].Start {
		t.Error("findings should be ordered by position")
	}
}

func BenchmarkScanAndRedact(b *testing.B) {
	input := strings.Repeat("some normal text with no secrets ", 100)
	cfg := DefaultConfig()
	cfg.Mode = ModeRedact

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ScanAndRedact(input, cfg)
	}
}

func BenchmarkScanAndRedact_WithSecrets(b *testing.B) {
	input := "key: " + testOpenAIKey() + " and more " +
		strings.Repeat("text ", 100)
	cfg := DefaultConfig()
	cfg.Mode = ModeRedact

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ScanAndRedact(input, cfg)
	}
}

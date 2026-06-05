package redaction

import (
	"regexp"
	"strings"
	"testing"
)

// resetPatternsForTest ensures pattern state is clean for each test.
func resetPatternsForTest(t *testing.T) {
	t.Helper()
	ResetPatterns()
}

// ----------------------------------------------------------------------------
// Mode behavior tests
// ----------------------------------------------------------------------------

func TestModeOff_SkipsScanning(t *testing.T) {
	resetPatternsForTest(t)

	// Construct a secret at runtime to avoid push-protection scanners.
	secret := "gh" + "p_" + strings.Repeat("a", 40)
	input := "token=" + secret

	result := ScanAndRedact(input, Config{Mode: ModeOff})

	if result.Output != input {
		t.Errorf("ModeOff should return input unchanged; got %q", result.Output)
	}
	if len(result.Findings) != 0 {
		t.Errorf("ModeOff should have no findings; got %d", len(result.Findings))
	}
}

func TestModeWarn_DoesNotModifyOutput(t *testing.T) {
	resetPatternsForTest(t)

	secret := "gh" + "p_" + strings.Repeat("b", 40)
	input := "my_token=" + secret

	result := ScanAndRedact(input, Config{Mode: ModeWarn})

	if result.Output != input {
		t.Errorf("ModeWarn should not modify output; got %q, want %q", result.Output, input)
	}
	if len(result.Findings) == 0 {
		t.Error("ModeWarn should detect findings")
	}
	if result.Blocked {
		t.Error("ModeWarn should not set Blocked")
	}
}

func TestModeRedact_ReplacesContent(t *testing.T) {
	resetPatternsForTest(t)

	secret := "gh" + "p_" + strings.Repeat("c", 40)
	input := "token=" + secret

	result := ScanAndRedact(input, Config{Mode: ModeRedact})

	if result.Output == input {
		t.Error("ModeRedact should modify output")
	}
	if !strings.Contains(result.Output, "[REDACTED:GITHUB_TOKEN:") {
		t.Errorf("ModeRedact output should contain placeholder; got %q", result.Output)
	}
	if strings.Contains(result.Output, secret) {
		t.Error("ModeRedact should remove the secret from output")
	}
	if len(result.Findings) == 0 {
		t.Error("ModeRedact should report findings")
	}
}

func TestModeBlock_SetsBlockedFlag(t *testing.T) {
	resetPatternsForTest(t)

	secret := "gh" + "p_" + strings.Repeat("d", 40)
	input := "token=" + secret

	result := ScanAndRedact(input, Config{Mode: ModeBlock})

	if !result.Blocked {
		t.Error("ModeBlock should set Blocked=true when findings exist")
	}
	if result.Output != input {
		t.Errorf("ModeBlock should not modify output; got %q", result.Output)
	}
	if len(result.Findings) == 0 {
		t.Error("ModeBlock should report findings")
	}
}

func TestModeBlock_NoFindingsNoBlock(t *testing.T) {
	resetPatternsForTest(t)

	input := "hello world no secrets here"

	result := ScanAndRedact(input, Config{Mode: ModeBlock})

	if result.Blocked {
		t.Error("ModeBlock should not set Blocked when no findings")
	}
}

// ----------------------------------------------------------------------------
// Category detection tests (one per category)
// ----------------------------------------------------------------------------

func TestCategoryDetection(t *testing.T) {
	resetPatternsForTest(t)

	// Build test inputs at runtime to avoid security scanners.
	// Each entry: category name, constructor function for input.
	tests := []struct {
		name     string
		category Category
		input    func() string
	}{
		{
			name:     "OpenAI key with T3Bl marker",
			category: CategoryOpenAIKey,
			input: func() string {
				// sk-[chars]T3BlbkFJ[chars] format
				prefix := "s" + "k-"
				marker := "T3Blbk" + "FJ"
				return prefix + strings.Repeat("a", 20) + marker + strings.Repeat("b", 20)
			},
		},
		{
			name:     "OpenAI project key",
			category: CategoryOpenAIKey,
			input: func() string {
				// sk-proj-[40+ chars]
				return "s" + "k-proj-" + strings.Repeat("x", 45)
			},
		},
		{
			name:     "Anthropic key",
			category: CategoryAnthropicKey,
			input: func() string {
				// sk-ant-[40+ chars]
				return "s" + "k-ant-" + strings.Repeat("y", 45)
			},
		},
		{
			name:     "GitHub personal token (ghp)",
			category: CategoryGitHubToken,
			input: func() string {
				return "gh" + "p_" + strings.Repeat("z", 36)
			},
		},
		{
			name:     "GitHub OAuth token (gho)",
			category: CategoryGitHubToken,
			input: func() string {
				return "gh" + "o_" + strings.Repeat("a", 36)
			},
		},
		{
			name:     "GitHub PAT fine-grained",
			category: CategoryGitHubToken,
			input: func() string {
				return "github_pat_" + strings.Repeat("a", 22) + "_" + strings.Repeat("b", 45)
			},
		},
		{
			name:     "Google API key",
			category: CategoryGoogleAPIKey,
			input: func() string {
				return "AIza" + strings.Repeat("c", 35)
			},
		},
		{
			name:     "AWS Access Key ID (AKIA)",
			category: CategoryAWSAccessKey,
			input: func() string {
				return "AKIA" + strings.Repeat("A", 16)
			},
		},
		{
			name:     "AWS Temp Access Key (ASIA)",
			category: CategoryAWSAccessKey,
			input: func() string {
				return "ASIA" + strings.Repeat("B", 16)
			},
		},
		{
			name:     "AWS Secret Key",
			category: CategoryAWSSecretKey,
			input: func() string {
				return "aws_secret=" + strings.Repeat("C", 40)
			},
		},
		{
			name:     "JWT token",
			category: CategoryJWT,
			input: func() string {
				// eyJ[header].eyJ[payload].[signature]
				header := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"
				payload := "eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4ifQ"
				sig := "SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
				return header + "." + payload + "." + sig
			},
		},
		{
			name:     "Bearer token",
			category: CategoryBearerToken,
			input: func() string {
				return "Bearer " + strings.Repeat("token", 10)
			},
		},
		{
			name:     "Private key header",
			category: CategoryPrivateKey,
			input: func() string {
				return "-----BEGIN RSA PRIVATE KEY-----"
			},
		},
		{
			name:     "Database URL with credentials",
			category: CategoryDatabaseURL,
			input: func() string {
				return "postgres://user:pass123@localhost:5432/db"
			},
		},
		{
			name:     "Password assignment",
			category: CategoryPassword,
			input: func() string {
				return "password=secretpass123"
			},
		},
		{
			name:     "Generic API key",
			category: CategoryGenericAPIKey,
			input: func() string {
				return "api_key=" + strings.Repeat("k", 20)
			},
		},
		{
			name:     "Generic secret",
			category: CategoryGenericSecret,
			input: func() string {
				return "secret=" + strings.Repeat("s", 20)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ResetPatterns()
			input := tt.input()

			result := ScanAndRedact(input, Config{Mode: ModeWarn})

			if len(result.Findings) == 0 {
				t.Errorf("expected to detect %s in %q", tt.category, input)
				return
			}

			found := false
			for _, f := range result.Findings {
				if f.Category == tt.category {
					found = true
					t.Logf("detected %s: match=%q", f.Category, f.Match)
					break
				}
			}
			if !found {
				t.Errorf("expected category %s, got %v", tt.category, result.Findings)
			}
		})
	}
}

// ----------------------------------------------------------------------------
// Deterministic placeholder hashing
// ----------------------------------------------------------------------------

func TestPlaceholderHashingIsDeterministic(t *testing.T) {
	resetPatternsForTest(t)

	secret := "gh" + "p_" + strings.Repeat("e", 40)
	input := "token=" + secret

	// Run multiple times and verify same output.
	var firstOutput string
	for i := 0; i < 5; i++ {
		ResetPatterns()
		result := ScanAndRedact(input, Config{Mode: ModeRedact})
		if i == 0 {
			firstOutput = result.Output
		} else if result.Output != firstOutput {
			t.Errorf("iteration %d: placeholder not deterministic\n  first: %q\n  got:   %q",
				i, firstOutput, result.Output)
		}
	}

	// Verify placeholder format.
	if !strings.Contains(firstOutput, "[REDACTED:GITHUB_TOKEN:") {
		t.Errorf("unexpected placeholder format: %q", firstOutput)
	}
}

func TestPlaceholderHashingIsDifferentForDifferentInputs(t *testing.T) {
	resetPatternsForTest(t)

	secret1 := "gh" + "p_" + strings.Repeat("f", 40)
	secret2 := "gh" + "p_" + strings.Repeat("g", 40)

	result1 := ScanAndRedact(secret1, Config{Mode: ModeRedact})
	ResetPatterns()
	result2 := ScanAndRedact(secret2, Config{Mode: ModeRedact})

	if result1.Output == result2.Output {
		t.Error("different inputs should produce different placeholder hashes")
	}
}

// ----------------------------------------------------------------------------
// Overlapping matches behavior
// ----------------------------------------------------------------------------

func TestOverlappingMatches_HigherPriorityWins(t *testing.T) {
	resetPatternsForTest(t)

	// Create input that could match multiple patterns.
	// A GitHub token also looks like a generic secret pattern.
	secret := "gh" + "p_" + strings.Repeat("h", 40)
	input := "secret=" + secret

	result := ScanAndRedact(input, Config{Mode: ModeWarn})

	// Should detect both, but GitHub token is higher priority.
	var hasGitHub, hasGenericAtSamePos bool
	for _, f := range result.Findings {
		if f.Category == CategoryGitHubToken {
			hasGitHub = true
		}
		// Check if any generic match overlaps with GitHub match.
		for _, other := range result.Findings {
			if other.Category == CategoryGenericSecret && f.Category == CategoryGitHubToken {
				if other.Start >= f.Start && other.Start < f.End {
					hasGenericAtSamePos = true
				}
			}
		}
	}

	if !hasGitHub {
		t.Error("expected to detect GitHub token")
	}
	if hasGenericAtSamePos {
		t.Error("generic match should not overlap with higher-priority GitHub match")
	}
}

func TestOverlappingMatches_MultipleNonOverlapping(t *testing.T) {
	resetPatternsForTest(t)

	// Two distinct secrets should both be detected.
	gh := "gh" + "p_" + strings.Repeat("i", 40)
	aws := "AKIA" + strings.Repeat("J", 16)
	input := "github=" + gh + " aws=" + aws

	result := ScanAndRedact(input, Config{Mode: ModeWarn})

	hasGitHub := false
	hasAWS := false
	for _, f := range result.Findings {
		switch f.Category {
		case CategoryGitHubToken:
			hasGitHub = true
		case CategoryAWSAccessKey:
			hasAWS = true
		}
	}

	if !hasGitHub || !hasAWS {
		t.Errorf("expected both GitHub and AWS; got GitHub=%v AWS=%v, findings=%v",
			hasGitHub, hasAWS, result.Findings)
	}
}

// ----------------------------------------------------------------------------
// Allowlist suppression
// ----------------------------------------------------------------------------

func TestAllowlistSuppressesOverlappingLowerPriorityMatches(t *testing.T) {
	ResetPatterns()

	// Construct a key-shaped value at runtime to avoid embedding secret-looking
	// literals in the repo (which can trigger push-protection scanners).
	prefix := "s" + "k" + "-" + "proj" + "-"
	key := prefix + strings.Repeat("a", 40)
	input := "token=" + key

	// Sanity check: input should be detected without allowlist.
	if got := Scan(input, Config{}); len(got) == 0 {
		t.Fatalf("expected findings without allowlist, got none")
	}

	cfg := Config{
		Allowlist: []string{"^" + regexp.QuoteMeta(key) + "$"},
	}
	if got := Scan(input, cfg); len(got) != 0 {
		t.Fatalf("expected no findings with allowlist, got %d: %#v", len(got), got)
	}
}

func TestAllowlist_PartialMatch(t *testing.T) {
	resetPatternsForTest(t)

	secret := "gh" + "p_" + strings.Repeat("k", 40)
	input := "token=" + secret

	// Allowlist pattern that matches part of the secret.
	cfg := Config{
		Mode:      ModeWarn,
		Allowlist: []string{secret},
	}

	result := ScanAndRedact(input, cfg)

	if len(result.Findings) != 0 {
		t.Errorf("allowlist should suppress match; got %d findings", len(result.Findings))
	}
}

func TestAllowlist_DoesNotSuppressOtherSecrets(t *testing.T) {
	resetPatternsForTest(t)

	gh := "gh" + "p_" + strings.Repeat("l", 40)
	aws := "AKIA" + strings.Repeat("M", 16)
	input := "github=" + gh + " aws=" + aws

	// Only allowlist the GitHub token.
	cfg := Config{
		Mode:      ModeWarn,
		Allowlist: []string{gh},
	}

	result := ScanAndRedact(input, cfg)

	hasAWS := false
	hasGitHub := false
	for _, f := range result.Findings {
		switch f.Category {
		case CategoryAWSAccessKey:
			hasAWS = true
		case CategoryGitHubToken:
			hasGitHub = true
		}
	}

	if !hasAWS {
		t.Error("AWS key should still be detected")
	}
	if hasGitHub {
		t.Error("GitHub token should be allowlisted")
	}
}

// ----------------------------------------------------------------------------
// DisabledCategories
// ----------------------------------------------------------------------------

func TestDisabledCategories_SkipsSpecifiedCategories(t *testing.T) {
	resetPatternsForTest(t)

	gh := "gh" + "p_" + strings.Repeat("n", 40)
	input := "token=" + gh

	cfg := Config{
		Mode:               ModeWarn,
		DisabledCategories: []Category{CategoryGitHubToken},
	}

	result := ScanAndRedact(input, cfg)

	for _, f := range result.Findings {
		if f.Category == CategoryGitHubToken {
			t.Error("disabled category should not be detected")
		}
	}
}

// ----------------------------------------------------------------------------
// Convenience functions
// ----------------------------------------------------------------------------

func TestScan_ReturnsFindings(t *testing.T) {
	resetPatternsForTest(t)

	secret := "gh" + "p_" + strings.Repeat("o", 40)
	input := "token=" + secret

	findings := Scan(input, Config{})

	if len(findings) == 0 {
		t.Error("Scan should return findings")
	}
}

func TestRedact_ReturnsRedactedAndFindings(t *testing.T) {
	resetPatternsForTest(t)

	secret := "gh" + "p_" + strings.Repeat("p", 40)
	input := "token=" + secret

	output, findings := Redact(input, Config{})

	if len(findings) == 0 {
		t.Error("Redact should return findings")
	}
	if output == input {
		t.Error("Redact should modify output")
	}
	if strings.Contains(output, secret) {
		t.Error("Redact should remove secret from output")
	}
}

func TestContainsSensitive_ReturnsBool(t *testing.T) {
	resetPatternsForTest(t)

	secretInput := "token=" + "gh" + "p_" + strings.Repeat("q", 40)
	safeInput := "hello world"

	if !ContainsSensitive(secretInput, Config{}) {
		t.Error("ContainsSensitive should return true for secret input")
	}
	if ContainsSensitive(safeInput, Config{}) {
		t.Error("ContainsSensitive should return false for safe input")
	}
}

// ----------------------------------------------------------------------------
// AddLineInfo
// ----------------------------------------------------------------------------

func TestAddLineInfo_PopulatesLineAndColumn(t *testing.T) {
	resetPatternsForTest(t)

	secret := "gh" + "p_" + strings.Repeat("r", 40)
	input := "line1\nline2\ntoken=" + secret + "\nline4"

	result := ScanAndRedact(input, Config{Mode: ModeWarn})

	if len(result.Findings) == 0 {
		t.Fatal("expected findings")
	}

	AddLineInfo(input, result.Findings)

	for _, f := range result.Findings {
		if f.Line == 0 {
			t.Error("Line should be set")
		}
		if f.Column == 0 {
			t.Error("Column should be set")
		}
		t.Logf("finding at line %d, column %d", f.Line, f.Column)
	}

	// Token is on line 3.
	if result.Findings[0].Line != 3 {
		t.Errorf("expected line 3, got %d", result.Findings[0].Line)
	}
}

func TestAddLineInfo_EmptyFindings(t *testing.T) {
	// Should not panic on empty findings.
	AddLineInfo("hello world", nil)
	AddLineInfo("hello world", []Finding{})
}

// ----------------------------------------------------------------------------
// Edge cases
// ----------------------------------------------------------------------------

func TestEmptyInput(t *testing.T) {
	resetPatternsForTest(t)

	result := ScanAndRedact("", Config{Mode: ModeRedact})

	if result.Output != "" {
		t.Errorf("empty input should produce empty output; got %q", result.Output)
	}
	if len(result.Findings) != 0 {
		t.Error("empty input should have no findings")
	}
}

func TestNoSecrets(t *testing.T) {
	resetPatternsForTest(t)

	input := "This is normal text without any secrets."

	result := ScanAndRedact(input, Config{Mode: ModeRedact})

	if result.Output != input {
		t.Error("input without secrets should be unchanged")
	}
	if len(result.Findings) != 0 {
		t.Error("should have no findings")
	}
}

func TestMultipleSecretsRedaction(t *testing.T) {
	resetPatternsForTest(t)

	gh := "gh" + "p_" + strings.Repeat("s", 40)
	aws := "AKIA" + strings.Repeat("T", 16)
	input := "GITHUB=" + gh + "\nAWS=" + aws

	result := ScanAndRedact(input, Config{Mode: ModeRedact})

	if strings.Contains(result.Output, gh) {
		t.Error("GitHub token should be redacted")
	}
	if strings.Contains(result.Output, aws) {
		t.Error("AWS key should be redacted")
	}
	if !strings.Contains(result.Output, "[REDACTED:GITHUB_TOKEN:") {
		t.Error("should contain GitHub placeholder")
	}
	if !strings.Contains(result.Output, "[REDACTED:AWS_ACCESS_KEY:") {
		t.Error("should contain AWS placeholder")
	}
}

func TestResult_OriginalLength(t *testing.T) {
	resetPatternsForTest(t)

	input := "test input with " + "gh" + "p_" + strings.Repeat("u", 40)

	result := ScanAndRedact(input, Config{Mode: ModeRedact})

	if result.OriginalLength != len(input) {
		t.Errorf("OriginalLength=%d, want %d", result.OriginalLength, len(input))
	}
}

// ----------------------------------------------------------------------------
// Config validation
// ----------------------------------------------------------------------------

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		mode    Mode
		wantErr bool
	}{
		{ModeOff, false},
		{ModeWarn, false},
		{ModeRedact, false},
		{ModeBlock, false},
		{"invalid", true},
		{"", true},
	}

	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			cfg := Config{Mode: tt.mode}
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Mode != ModeWarn {
		t.Errorf("DefaultConfig().Mode = %q, want %q", cfg.Mode, ModeWarn)
	}
}

func TestConfigError_Error(t *testing.T) {
	e := &ConfigError{Field: "mode", Message: "invalid value"}
	got := e.Error()
	want := "redaction config error: mode: invalid value"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// bd-pmdpn: Config.DeepCopy must produce a value whose reference-typed
// fields (Allowlist, ExtraPatterns, DisabledCategories) do not alias
// the original. Mutating the copy must not reach back into the source.
func TestConfigDeepCopy_IndependentSlicesAndMaps(t *testing.T) {
	src := Config{
		Mode:               ModeRedact,
		Allowlist:          []string{"^foo$", "^bar$"},
		ExtraPatterns:      map[Category][]string{"api_key": {"^xyz$"}},
		DisabledCategories: []Category{"email", "phone"},
	}

	cp := src.DeepCopy()

	// Mutate every reference-typed field on the copy.
	cp.Allowlist[0] = "MUTATED"
	cp.Allowlist = append(cp.Allowlist, "extra")
	cp.ExtraPatterns["api_key"][0] = "MUTATED"
	cp.ExtraPatterns["new"] = []string{"new"}
	cp.DisabledCategories[0] = "MUTATED"

	// Source must be untouched.
	if src.Allowlist[0] != "^foo$" {
		t.Errorf("Allowlist[0] leaked: %q", src.Allowlist[0])
	}
	if len(src.Allowlist) != 2 {
		t.Errorf("Allowlist length leaked: %d, want 2", len(src.Allowlist))
	}
	if got := src.ExtraPatterns["api_key"][0]; got != "^xyz$" {
		t.Errorf("ExtraPatterns inner slice leaked: %q", got)
	}
	if _, ok := src.ExtraPatterns["new"]; ok {
		t.Errorf("ExtraPatterns map gained key 'new' from copy mutation")
	}
	if src.DisabledCategories[0] != "email" {
		t.Errorf("DisabledCategories[0] leaked: %q", src.DisabledCategories[0])
	}
	// Mode is a value type — should round-trip cleanly.
	if cp.Mode != ModeRedact {
		t.Errorf("Mode = %q, want %q", cp.Mode, ModeRedact)
	}
}

// bd-ztb6a: ExtraPatterns flowed through every layer (config TOML
// parse, Set/Get round-trip, DeepCopy) but ScanAndRedact silently
// dropped them — the user's custom patterns never ran. These tests
// pin that they participate in the same machinery as the built-in
// patterns.
func TestScanAndRedact_ExtraPatternsAreApplied(t *testing.T) {
	resetPatternsForTest(t)
	cfg := Config{
		Mode: ModeRedact,
		ExtraPatterns: map[Category][]string{
			"CUSTOM_TOKEN": {`MYORG-[A-Z0-9]{16}`},
		},
	}
	input := "user uploaded MYORG-AB12CD34EF56GH78 today"
	result := ScanAndRedact(input, cfg)

	if len(result.Findings) == 0 {
		t.Fatalf("ExtraPatterns silently dropped: no findings on a string that matches the user pattern; input=%q", input)
	}
	found := false
	for _, f := range result.Findings {
		if f.Category == "CUSTOM_TOKEN" && f.Match == "MYORG-AB12CD34EF56GH78" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a CUSTOM_TOKEN finding, got %+v", result.Findings)
	}
	if !strings.Contains(result.Output, "[REDACTED:CUSTOM_TOKEN:") {
		t.Errorf("Output not redacted with the user's category: %q", result.Output)
	}
}

// DisabledCategories must suppress extras for the same category, just
// like it does for built-ins.
func TestScanAndRedact_ExtraPatternsRespectDisabledCategories(t *testing.T) {
	resetPatternsForTest(t)
	cfg := Config{
		Mode: ModeRedact,
		ExtraPatterns: map[Category][]string{
			"CUSTOM_TOKEN": {`MYORG-[A-Z0-9]{16}`},
		},
		DisabledCategories: []Category{"CUSTOM_TOKEN"},
	}
	input := "user uploaded MYORG-AB12CD34EF56GH78 today"
	result := ScanAndRedact(input, cfg)
	for _, f := range result.Findings {
		if f.Category == "CUSTOM_TOKEN" {
			t.Errorf("DisabledCategories did not suppress extra: got %+v", f)
		}
	}
}

// Allowlist must filter out matches from extras too.
func TestScanAndRedact_ExtraPatternsRespectAllowlist(t *testing.T) {
	resetPatternsForTest(t)
	cfg := Config{
		Mode: ModeRedact,
		ExtraPatterns: map[Category][]string{
			"CUSTOM_TOKEN": {`MYORG-[A-Z0-9]{16}`},
		},
		Allowlist: []string{`^MYORG-AB12CD34EF56GH78$`},
	}
	input := "user uploaded MYORG-AB12CD34EF56GH78 today"
	result := ScanAndRedact(input, cfg)
	for _, f := range result.Findings {
		if f.Category == "CUSTOM_TOKEN" {
			t.Errorf("Allowlist did not suppress allowlisted extra match: got %+v", f)
		}
	}
}

// A malformed extra-pattern regex must not crash and must not disable
// the user's other extras OR the built-ins.
func TestScanAndRedact_ExtraPatternsMalformedDoesNotBlockOthers(t *testing.T) {
	resetPatternsForTest(t)
	cfg := Config{
		Mode: ModeRedact,
		ExtraPatterns: map[Category][]string{
			"CUSTOM_TOKEN": {
				`[unclosed-bracket`,  // malformed
				`MYORG-[A-Z0-9]{16}`, // valid; should still run
			},
		},
	}
	input := "user uploaded MYORG-AB12CD34EF56GH78 today and bad sk-anth-mock"
	result := ScanAndRedact(input, cfg)
	// The valid extra should still match.
	foundExtra := false
	for _, f := range result.Findings {
		if f.Category == "CUSTOM_TOKEN" {
			foundExtra = true
		}
	}
	if !foundExtra {
		t.Errorf("malformed extra suppressed valid sibling extra: %+v", result.Findings)
	}
}

// Symmetric: nil reference-typed fields stay nil on the copy.
func TestConfigDeepCopy_NilFieldsStayNil(t *testing.T) {
	src := Config{Mode: ModeWarn}
	cp := src.DeepCopy()
	if cp.Allowlist != nil {
		t.Errorf("Allowlist became non-nil after deep-copy of zero value: %v", cp.Allowlist)
	}
	if cp.ExtraPatterns != nil {
		t.Errorf("ExtraPatterns became non-nil after deep-copy of zero value: %v", cp.ExtraPatterns)
	}
	if cp.DisabledCategories != nil {
		t.Errorf("DisabledCategories became non-nil after deep-copy of zero value: %v", cp.DisabledCategories)
	}
}

package redaction

import (
	"encoding/json"
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

// Issue #372: JSON/JSONL keys have a closing quote before the separator. Each
// match must include the complete string value, even across escaped quotes.
func TestJSONCredentialDetection(t *testing.T) {
	cases := []struct {
		name     string
		field    string
		category Category
	}{
		{"password", `"password":"synthetic-never-valid-json-value"`, CategoryPassword},
		{"passwd", `"passwd":"synthetic-passwd-value"`, CategoryPassword},
		{"pwd credential", `"pwd":"synthetic-pwd-value"`, CategoryPassword},
		{"prefixed password", `"DB_PASSWORD":"synthetic-db-password"`, CategoryPassword},
		{"prefixed pwd path credential", `"MYSQL_PWD":"/hunter2isnotapath"`, CategoryPassword},
		{"mixed case and whitespace", "\"PaSsWoRd\" \n : \n \"synthetic-mixed-value\"", CategoryPassword},
		{"escaped quote", `"password":"secret-prefix\"secret-suffix"`, CategoryPassword},
		{"escaped backslash and slash", `"password":"secret-prefix\\middle\/secret-suffix"`, CategoryPassword},
		{"escaped controls", `"password":"secret-prefix\n\t\rsecret-suffix"`, CategoryPassword},
		{"spaces and apostrophe", `"password":"secret prefix isn't public"`, CategoryPassword},
		{"eight characters", `"password":"1234567\u0061"`, CategoryPassword},
		{"unicode characters", `"password":"密码密码密码密码"`, CategoryPassword},
		{"API key", `"api_key":"synthetic_api_value_123"`, CategoryGenericAPIKey},
		{"prefixed API key", `"SERVICE_APIKEY":"synthetic-api-value-123"`, CategoryGenericAPIKey},
		{"escaped API key", `"api_key":"synthetic\u005fapi\u005fvalue_123"`, CategoryGenericAPIKey},
		{"secret", `"secret":"synthetic/secret+value="`, CategoryGenericSecret},
		{"private key", `"private_key":"synthetic-private-value"`, CategoryGenericSecret},
		{"token", `"token":"synthetic-token-value"`, CategoryGenericSecret},
		{"prefixed token", `"access_token":"synthetic-access-value"`, CategoryGenericSecret},
		{"escaped secret", `"client_secret":"synthetic\/secret\u002bvalue="`, CategoryGenericSecret},
		{"AWS secret", `"aws_secret":"` + strings.Repeat("X", 40) + `"`, CategoryAWSSecretKey},
		{"AWS prefixed secret", `"aws_secret_access_key":"` + strings.Repeat("Y", 40) + `"`, CategoryAWSSecretKey},
		{"AWS escaped secret", `"secret_access_key":"` + strings.Repeat("Z", 36) + `a\/b="`, CategoryAWSSecretKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const before = `{"before":"public-before","auth":{`
			const after = `},"after":"public-after"}`
			input := before + tc.field + after
			if !json.Valid([]byte(input)) {
				t.Fatal("invalid JSON fixture")
			}
			result := ScanAndRedact(input, Config{Mode: ModeRedact})
			if len(result.Findings) != 1 {
				t.Fatalf("findings=%d, want 1", len(result.Findings))
			}
			f := result.Findings[0]
			if f.Category != tc.category || f.Match != tc.field {
				t.Errorf("got %s match %q, want %s match %q", f.Category, f.Match, tc.category, tc.field)
			}
			if f.Start != len(before) || f.End != len(before)+len(tc.field) {
				t.Errorf("unexpected source range [%d:%d]", f.Start, f.End)
			}
			if result.Output != before+f.Redacted+after {
				t.Error("redaction left part of the credential or changed a neighboring field")
			}
		})
	}
}

func TestJSONCredentialNegativeFields(t *testing.T) {
	fields := []string{
		`"password":""`, `"password":"1234567"`, `"password":"12345\u0061"`,
		`"password":"密码密码"`, `"password":1234567`, `"password":null`,
		`"password":false`, `"password":{}`, `"password":["public-array-value"]`,
		`"password_hint":"public-password-description"`, `"notpassword":"public-description"`,
		`"token_count":"public-token-description"`, `"api_key_name":"public-key-description"`,
		`"secret_name":"public-secret-description"`, `"private_key_path":"/public/path/to/key"`,
		`"api_key":"123456789012345"`, `"api_key":"12345678901234\u0061"`,
		`"api_key":"invalid-key-value!"`, `"token":"invalid.token.value"`,
		`"secret_access_key":"` + strings.Repeat("a", 39) + `"`,
		`"secret_access_key":"` + strings.Repeat("a", 41) + `"`,
		`"password":"<redacted>"`, `"passwd":"\u003cset\u003e"`,
		`"password":"************"`, `"pwd":"<hidden>"`,
		`"PWD":"/home/user/project"`, `"OLDPWD":"/home/user/previous"`,
		`"pwd":"\/home\/user\/project"`, `"pwd":"\u002fhome/user/project"`,
		`"PWD":"C:\\Users\\user\\project"`, `"OLDPWD":"$HOME/projects/example"`,
	}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			input := `{` + field + `,"message":"a long harmless neighboring value"}`
			if !json.Valid([]byte(input)) {
				t.Fatal("invalid JSON fixture")
			}
			result := ScanAndRedact(input, Config{Mode: ModeRedact})
			if len(result.Findings) != 0 || result.Output != input {
				t.Errorf("non-credential field produced findings: %+v", result.Findings)
			}
		})
	}
}

func TestJSONLCredentialsAndAssignments(t *testing.T) {
	lines := []string{
		`{"password":"synthetic-first-value","password":"synthetic-second-value"}`,
		`{"password":"escaped-prefix\"escaped-suffix","status":"ready"}`,
		`PASSWORD=synthetic-never-valid-control-value`,
		`passwd: 'synthetic-assignment-value'`,
		`DB_PWD=synthetic-database-value`,
	}
	input := strings.Join(lines, "\n")
	findings := Scan(input, Config{})
	AddLineInfo(input, findings)
	wantLines := []int{1, 1, 2, 3, 4, 5}
	if len(findings) != len(wantLines) {
		t.Fatalf("findings=%d, want %d", len(findings), len(wantLines))
	}
	for i, f := range findings {
		if f.Category != CategoryPassword || f.Line != wantLines[i] || input[f.Start:f.End] != f.Match {
			t.Errorf("finding %d has wrong category, line, or source range: %+v", i, f)
		}
	}
	output, _ := Redact(input, Config{})
	for _, fragment := range []string{"synthetic-", "escaped-prefix", "escaped-suffix"} {
		if strings.Contains(output, fragment) {
			t.Error("redaction exposed a secret fragment")
		}
	}
	if !strings.Contains(output, `"status":"ready"`) {
		t.Error("redaction consumed the neighboring public field")
	}
}

func TestJSONCredentialsPreserveAllowlistAndPriorities(t *testing.T) {
	provider := "gh" + "p_" + strings.Repeat("q", 40)
	for _, key := range []string{"password", "api_key", "token"} {
		input := `{"` + key + `":"` + provider + `"}`
		findings := Scan(input, Config{})
		if len(findings) != 1 || findings[0].Category != CategoryGitHubToken || findings[0].Match != provider {
			t.Fatalf("provider priority changed for %s: %+v", key, findings)
		}
		cfg := Config{Allowlist: []string{"^" + regexp.QuoteMeta(provider) + "$"}}
		if got := Scan(input, cfg); len(got) != 0 {
			t.Errorf("allowlisted provider exposed lower-priority matches for %s: %+v", key, got)
		}
	}
	const assignment = "TOKEN=fixture-never-log-this"
	if got := Scan(assignment, Config{Allowlist: []string{"^" + assignment + "$"}}); len(got) != 0 {
		t.Error("anchored assignment allowlist changed")
	}
	const allowedField = `"password":"synthetic-allowed-value"`
	input := `{` + allowedField + `,"passwd":"synthetic-unlisted-value"}`
	findings := Scan(input, Config{Allowlist: []string{"^" + regexp.QuoteMeta(allowedField) + "$"}})
	if len(findings) != 1 || findings[0].Match != `"passwd":"synthetic-unlisted-value"` {
		t.Errorf("JSON allowlist did not suppress only the intended match: %+v", findings)
	}
	if got := Scan(input, Config{DisabledCategories: []Category{CategoryPassword}}); len(got) != 0 {
		t.Error("disabled password category still reported JSON fields")
	}
}

func TestJSONCredentialProviderOverlapRedactsCompleteValue(t *testing.T) {
	github := "gh" + "p_" + strings.Repeat("q", 40)
	partialGitHub := "gh" + "p_" + strings.Repeat("q", 31)
	google := "AI" + "za" + strings.Repeat("q", 35)
	aws := "AK" + "IA" + strings.Repeat("Q", 16)
	tests := []struct {
		name      string
		key       string
		rawValue  string
		category  Category
		providers map[string]Category
		fragments []string
	}{
		{
			name: "escaped token suffix", key: "token",
			rawValue: partialGitHub + `\u0071SECRETL8`, category: CategoryGenericSecret,
			providers: map[string]Category{partialGitHub: CategoryGitHubToken},
			fragments: []string{`\u0071`, "SECRETL8"},
		},
		{
			name: "escaped api key suffix", key: "api_key",
			rawValue: partialGitHub + `\u0071SECRETL8`, category: CategoryGenericAPIKey,
			providers: map[string]Category{partialGitHub: CategoryGitHubToken},
			fragments: []string{`\u0071`, "SECRETL8"},
		},
		{
			name: "escaped password suffix", key: "password",
			rawValue: partialGitHub + `\u0071SECRETL8`, category: CategoryPassword,
			providers: map[string]Category{partialGitHub: CategoryGitHubToken},
			fragments: []string{`\u0071`, "SECRETL8"},
		},
		{
			name: "password prefix and suffix", key: "password",
			rawValue: "sensitive-prefix:" + github + ":sensitive-suffix", category: CategoryPassword,
			providers: map[string]Category{github: CategoryGitHubToken},
			fragments: []string{"sensitive-prefix", "sensitive-suffix"},
		},
		{
			name: "escaped password prefix and suffix", key: "password",
			rawValue: `sensitive-prefix\"` + github + `\\sensitive-suffix`, category: CategoryPassword,
			providers: map[string]Category{github: CategoryGitHubToken},
			fragments: []string{"sensitive-prefix", "sensitive-suffix"},
		},
		{
			name: "multiple providers in password", key: "password",
			rawValue: "sensitive-prefix:" + github + ":sensitive-middle:" + aws + ":sensitive-suffix", category: CategoryPassword,
			providers: map[string]Category{github: CategoryGitHubToken, aws: CategoryAWSAccessKey},
			fragments: []string{"sensitive-prefix", "sensitive-middle", "sensitive-suffix"},
		},
		{
			name: "fixed length provider in api key", key: "api_key",
			rawValue: google + "sensitiveSuffix", category: CategoryGenericAPIKey,
			providers: map[string]Category{google: CategoryGoogleAPIKey},
			fragments: []string{"sensitiveSuffix"},
		},
		{
			name: "fixed length provider in aws secret", key: "secret_key",
			rawValue: google + "Z", category: CategoryAWSSecretKey,
			providers: map[string]Category{google: CategoryGoogleAPIKey},
			fragments: []string{"Z"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			field := `"` + tc.key + `":"` + tc.rawValue + `"`
			input := `{"status":"public",` + field + `,"count":2}`
			if !json.Valid([]byte(input)) {
				t.Fatal("invalid JSON test fixture")
			}
			result := ScanAndRedact(input, Config{Mode: ModeRedact})
			for _, fragment := range tc.fragments {
				if strings.Contains(result.Output, fragment) {
					t.Errorf("redacted output exposed a credential fragment %q", fragment)
				}
			}
			if !strings.Contains(result.Output, `"status":"public"`) || !strings.Contains(result.Output, `"count":2`) {
				t.Error("redaction consumed a neighboring public field")
			}
			residuals := 0
			for _, f := range result.Findings {
				if f.Match != input[f.Start:f.End] {
					t.Errorf("finding no longer identifies its raw source range: %+v", f)
				}
				if f.Category == tc.category {
					residuals++
				}
			}
			if residuals == 0 {
				t.Error("missing lower-priority credential portions outside the provider match")
			}
			for provider, category := range tc.providers {
				found := false
				for _, f := range result.Findings {
					if f.Category == category && f.Match == provider {
						found = true
						start := strings.Index(input, provider)
						if f.Start != start || f.End != start+len(provider) {
							t.Error("provider finding offsets changed")
						}
					}
				}
				if !found {
					t.Errorf("missing original %s provider finding", category)
				}
			}
		})
	}
}

func TestJSONCredentialProviderOverlapAllowlistsAndDisabledCategories(t *testing.T) {
	provider := "gh" + "p_" + strings.Repeat("q", 31)
	suffix := `\u0071SECRETL8`
	field := `"token":"` + provider + suffix + `"`
	input := `{` + field + `}`
	providerAllowlist := "^" + regexp.QuoteMeta(provider) + "$"
	fieldAllowlist := "^" + regexp.QuoteMeta(field) + "$"
	tests := []struct {
		name          string
		cfg           Config
		wantProvider  bool
		wantResiduals bool
	}{
		{name: "default", wantProvider: true, wantResiduals: true},
		{name: "partial provider allowed", cfg: Config{Allowlist: []string{providerAllowlist}}, wantResiduals: true},
		{name: "original field allowed", cfg: Config{Allowlist: []string{fieldAllowlist}}, wantProvider: true},
		{name: "provider and field allowed", cfg: Config{Allowlist: []string{providerAllowlist, fieldAllowlist}}},
		{name: "residual alone is not the allowlist context", cfg: Config{Allowlist: []string{"^" + regexp.QuoteMeta(suffix) + "$"}}, wantProvider: true, wantResiduals: true},
		{name: "generic category disabled", cfg: Config{DisabledCategories: []Category{CategoryGenericSecret}}, wantProvider: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output, findings := Redact(input, tc.cfg)
			var hasProvider, hasResiduals bool
			for _, f := range findings {
				switch f.Category {
				case CategoryGitHubToken:
					hasProvider = true
					if f.Match != provider {
						t.Error("provider match changed")
					}
				case CategoryGenericSecret:
					hasResiduals = true
					if f.Match != suffix || input[f.Start:f.End] != suffix {
						t.Error("residual no longer identifies the raw escaped suffix")
					}
				}
			}
			if hasProvider != tc.wantProvider || hasResiduals != tc.wantResiduals {
				t.Errorf("provider=%v, residuals=%v; want provider=%v, residuals=%v", hasProvider, hasResiduals, tc.wantProvider, tc.wantResiduals)
			}
			if tc.wantResiduals && strings.Contains(output, "SECRETL8") {
				t.Error("redaction exposed a residual despite the selected allowlist")
			}
			if !tc.wantProvider && !tc.wantResiduals && output != input {
				t.Error("allowlisted input changed")
			}
		})
	}

	for _, input := range []string{`{"token":"` + provider + `"}`, "token=" + provider} {
		findings := Scan(input, Config{})
		if len(findings) != 1 || findings[0].Category != CategoryGitHubToken || findings[0].Match != provider {
			t.Error("a complete provider value no longer has exactly its original provider finding")
		}
		output, findings := Redact(input, Config{Allowlist: []string{providerAllowlist}})
		if len(findings) != 0 || output != input {
			t.Error("anchored allowlist changed for a complete provider value")
		}
	}
}

func TestJSONCredentialProviderOverlapWithZeroWidthExtraPatterns(t *testing.T) {
	provider := "gh" + "p_" + strings.Repeat("q", 40)
	for _, input := range []string{
		"password=sensitive-never-valid-value",
		`{"password":"sensitive-prefix:` + provider + `:sensitive-suffix"}`,
	} {
		cfg := Config{
			Mode:          ModeRedact,
			ExtraPatterns: map[Category][]string{"CUSTOM_ANCHOR": {`^`, `\b`, `$`}},
		}
		result := ScanAndRedact(input, cfg)
		anchors := 0
		for _, f := range result.Findings {
			if f.Start == f.End {
				anchors++
			}
			if f.Match != input[f.Start:f.End] {
				t.Error("finding source range changed with zero-width patterns")
			}
		}
		if anchors == 0 {
			t.Fatal("test did not retain zero-width matches")
		}
		for _, fragment := range []string{"sensitive", strings.Repeat("q", 8)} {
			if strings.Contains(result.Output, fragment) {
				t.Error("zero-width pattern caused a credential fragment to remain in ScanAndRedact output")
			}
		}
		output, findings := Redact(input, cfg)
		if output != result.Output || len(findings) != len(result.Findings) {
			t.Error("Redact and ScanAndRedact differ with zero-width patterns")
		}
	}
}

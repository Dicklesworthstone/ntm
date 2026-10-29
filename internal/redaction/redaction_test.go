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

func assertJSONRawFinding(t *testing.T, input, rawMatch string, category Category) Finding {
	t.Helper()
	start := strings.Index(input, rawMatch)
	if start < 0 {
		t.Fatal("expected raw match is absent from the test fixture")
	}
	result := ScanAndRedact(input, Config{Mode: ModeRedact})
	if len(result.Findings) != 1 {
		t.Fatalf("findings=%d, want exactly one %s finding", len(result.Findings), category)
	}
	f := result.Findings[0]
	if f.Category != category || f.Match != rawMatch || f.Start != start || f.End != start+len(rawMatch) {
		t.Errorf("finding has wrong category, raw match, or byte range: %+v", f)
	}
	wantOutput := input[:start] + f.Redacted + input[start+len(rawMatch):]
	if result.Output != wantOutput {
		t.Error("redacted output did not replace exactly the original source span")
	}
	return f
}

func TestJSONEscapedContentDetection(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		rawMatch string
	}{
		{
			name:     "quoted logged assignment",
			input:    `{"message":"PASSWORD=\"synthetic-never-valid-quoted\"","status":"ready"}`,
			rawMatch: `PASSWORD=\"synthetic-never-valid-quoted\"`,
		},
		{
			name:     "fully encoded keyword and separator",
			input:    `{"message":"\u0050\u0041\u0053\u0053\u0057\u004f\u0052\u0044\u003dsynthetic-never-valid-encoded","status":"ready"}`,
			rawMatch: `\u0050\u0041\u0053\u0053\u0057\u004f\u0052\u0044\u003dsynthetic-never-valid-encoded`,
		},
		{
			name:     "encoded separator",
			input:    `{"message":"PASSWORD\u003dsynthetic-never-valid-separator","status":"ready"}`,
			rawMatch: `PASSWORD\u003dsynthetic-never-valid-separator`,
		},
		{
			name:     "encoded value",
			input:    `{"message":"PASSWORD=synthetic\u002dnever\u002dvalid\u002dvalue","status":"ready"}`,
			rawMatch: `PASSWORD=synthetic\u002dnever\u002dvalid\u002dvalue`,
		},
		{
			name:     "logged nested field",
			input:    `{"message":"{\"password\":\"synthetic-never-valid-nested\"}","status":"ready"}`,
			rawMatch: `\"password\":\"synthetic-never-valid-nested\"`,
		},
		{
			name:     "escaped whitespace around assignment",
			input:    `{"message":"first line\nPASSWORD=synthetic-never-valid-spaces\tpublic","status":"ready"}`,
			rawMatch: `PASSWORD=synthetic-never-valid-spaces`,
		},
		{
			name:     "database PWD path remains a credential",
			input:    `{"message":"MYSQL_PWD=\u002fhome\u002fagent\u002fproject","status":"ready"}`,
			rawMatch: `PWD=\u002fhome\u002fagent\u002fproject`,
		},
		{
			name:     "escaped quote key does not hide message content",
			input:    `{"not\"password":"\u0050ASSWORD\u003dsynthetic-never-valid-message","status":"ready"}`,
			rawMatch: `\u0050ASSWORD\u003dsynthetic-never-valid-message`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !json.Valid([]byte(tc.input)) {
				t.Fatal("invalid JSON test fixture")
			}
			assertJSONRawFinding(t, tc.input, tc.rawMatch, CategoryPassword)
		})
	}
}

func TestJSONEscapedContentRejectsDecodedFalsePositives(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"seven decoded characters", `{"message":"PASSWORD=\u0061\u0062\u0063\u0064\u0065\u0066\u0067"}`},
		{"decoded whitespace ends a short value", `{"message":"PASSWORD=abc\u0020defg"}`},
		{"shell PWD path", `{"message":"PWD=\u002fhome\u002fagent\u002fproject"}`},
		{"shell OLDPWD path", `{"message":"OLDPWD=\u002fhome\u002fagent\u002fproject"}`},
		{"Windows PWD path", `{"message":"PWD=\u0043\u003a\u005cUsers\u005cagent\u005cproject"}`},
		{"shell expansion", `{"message":"PWD=\u0024HOME/project"}`},
		{"star placeholder", `{"message":"PASSWORD=\u002a\u002a\u002a\u002a\u002a\u002a\u002a\u002a"}`},
		{"redacted placeholder", `{"message":"PASSWORD=\u003credacted\u003e"}`},
		{"seven surrogate pair runes", `{"message":"PASSWORD=` + strings.Repeat(`\ud83d\ude00`, 7) + `"}`},
		{"literal backslash u separator", `{"message":"PASSWORD\\u003dsynthetic-never-valid-double"}`},
		{"nested key is not decoded twice", `{"message":"{\"pass\\u0077ord\":\"synthetic-never-valid-double\"}"}`},
		{"false key cannot suppress decoded short-value filtering", `{"not\"password":"PASSWORD=\u0061\u0062\u0063\u0064\u0065\u0066\u0067"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !json.Valid([]byte(tc.input)) {
				t.Fatal("invalid JSON test fixture")
			}
			output, findings := Redact(tc.input, Config{})
			if len(findings) != 0 || output != tc.input {
				t.Errorf("decoded non-credential produced findings: %+v", findings)
			}
		})
	}
}

func TestJSONEscapedContentUnicodeSourceOffsets(t *testing.T) {
	tests := []struct {
		name     string
		rawValue string
	}{
		{"paired surrogates", `\u96ea` + strings.Repeat(`\ud83d\ude00`, 7)},
		{"unpaired high surrogate", `abcdefg\ud800`},
		{"unpaired low surrogate", `abcdefg\udc00`},
		{"unpaired then paired surrogate", `abcdef\ud800\ud83d\ude00`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rawMatch := "PASSWORD=" + tc.rawValue
			input := `{"message":"raw:雪 escaped:\u96ea paired:\ud83d\ude00 ` + rawMatch + `\tpublic","status":"ready"}`
			if !json.Valid([]byte(input)) {
				t.Fatal("invalid JSON test fixture")
			}
			assertJSONRawFinding(t, input, rawMatch, CategoryPassword)
		})
	}
}

func TestJSONEncodedCredentialKeysDetection(t *testing.T) {
	tests := []struct {
		name     string
		field    string
		category Category
	}{
		{"password", `"pass\u0077ord":"synthetic-never-valid-password"`, CategoryPassword},
		{"uppercase password", `"\u0050ASSWORD":"synthetic-never-valid-password"`, CategoryPassword},
		{"API key", `"api\u005fkey":"syntheticNeverValidApiKey123"`, CategoryGenericAPIKey},
		{"prefixed token", `"access_\u0074oken":"syntheticNeverValidToken123"`, CategoryGenericSecret},
		{"AWS key", `"aws_\u0073ecret_access_key":"` + strings.Repeat("Q", 40) + `"`, CategoryAWSSecretKey},
		{"database PWD path", `"mysql_\u0070wd":"\u002fhome/agent/project"`, CategoryPassword},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := `{"status":"ready",` + tc.field + `,"count":2}`
			if !json.Valid([]byte(input)) {
				t.Fatal("invalid JSON test fixture")
			}
			assertJSONRawFinding(t, input, tc.field, tc.category)
		})
	}
}

func TestJSONEncodedCredentialKeysRejectNonCredentials(t *testing.T) {
	fields := []string{
		`"notpass\u0077ord":"synthetic-never-valid-neighbor"`,
		`"pass\u0077ord_hint":"synthetic-never-valid-neighbor"`,
		`"not\"password":"synthetic-never-valid-neighbor"`,
		`"not\u0022password":"synthetic-never-valid-neighbor"`,
		`"pass\\u0077ord":"synthetic-never-valid-double"`,
		`"pass\u0077ord":12345678`,
		`"pass\u0077ord":"\u0061\u0062\u0063\u0064\u0065\u0066\u0067"`,
		`"pass\u0077ord":"\u003credacted\u003e"`,
		`"\u0050WD":"\u002fhome/agent/project"`,
		`"OLD\u0050WD":"\u002fhome/agent/project"`,
	}
	for _, field := range fields {
		input := "{" + field + "}"
		if !json.Valid([]byte(input)) {
			t.Fatal("invalid JSON test fixture")
		}
		output, findings := Redact(input, Config{})
		if len(findings) != 0 || output != input {
			t.Errorf("non-credential key produced findings: %+v", findings)
		}
	}
}

func TestJSONEncodedCredentialKeysJSONLDuplicates(t *testing.T) {
	first := `"password":"synthetic-never-valid-first"`
	second := `"pass\u0077ord":"synthetic-never-valid-second"`
	third := `PASSWORD=\"synthetic-never-valid-third\"`
	input := "\n{" + first + "," + second + ",\"status\":\"ready\"}\r\n \t\n" + `{"message":"` + third + `"}` + "\n"
	findings := Scan(input, Config{})
	if len(findings) != 3 {
		t.Fatalf("findings=%d, want both duplicate properties and the next record", len(findings))
	}
	AddLineInfo(input, findings)
	for i, want := range []string{first, second, third} {
		f := findings[i]
		start := strings.Index(input, want)
		wantLine := []int{2, 2, 4}[i]
		if f.Category != CategoryPassword || f.Match != want || f.Start != start || f.End != start+len(want) || f.Line != wantLine {
			t.Errorf("finding %d has wrong raw source or physical line: %+v", i, f)
		}
	}
	output, _ := Redact(input, Config{})
	if strings.Contains(output, "synthetic-never-valid") || !strings.Contains(output, `"status":"ready"`) {
		t.Error("JSONL redaction exposed credential content or consumed a public field")
	}
}

func TestJSONEscapedContentPreservesRawInvalidInput(t *testing.T) {
	assignment := `PASSWORD=\u0061\u0062\u0063\u0064\u0065\u0066\u0067`
	tests := []struct {
		name     string
		input    string
		rawMatch string
	}{
		{"plaintext quoted text", `log message="` + assignment + `"`, assignment + `"`},
		{"malformed single record", `{"message":"` + assignment + `",BROKEN`, assignment + `"`},
		{"malformed pretty document", "{\n\"" + assignment + "\"\nBROKEN\n}", assignment + `"`},
		{"invalid JSON escape", `{"message":"PASSWORD=abcdefg\q"}`, `PASSWORD=abcdefg\q"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if json.Valid([]byte(tc.input)) {
				t.Fatal("test fixture unexpectedly contains valid whole JSON")
			}
			assertJSONRawFinding(t, tc.input, tc.rawMatch, CategoryPassword)
		})
	}
}

func TestJSONLCompleteRecordsSurviveMalformedNeighbors(t *testing.T) {
	logged := `PASSWORD=\"synthetic-never-valid-complete-record\"`
	field := `"pass\u0077ord":"synthetic-never-valid-encoded-key"`
	assignment := "PASSWORD=synthetic-never-valid-raw-assignment"
	first := `{"message":"` + logged + `","note":"public-neighbor"}`
	second := `{"status":"ready",` + field + `}`
	tests := []struct {
		name    string
		input   string
		matches []string
		lines   []int
	}{
		{
			name:    "incomplete trailing record",
			input:   first + "\n" + `{"message":`,
			matches: []string{logged},
			lines:   []int{1},
		},
		{
			name:    "incomplete leading record",
			input:   `{"message":` + "\n" + second + "\n",
			matches: []string{field},
			lines:   []int{2},
		},
		{
			name:    "interleaved plain text and malformed records",
			input:   "INFO starting\r\n \t\n" + first + "\r\n" + `{"broken":"\q"}` + "\n" + second + "\nINFO finished",
			matches: []string{logged, field},
			lines:   []int{3, 5},
		},
		{
			name:    "ordinary assignments outside JSON still detected",
			input:   assignment + "\n" + first + "\n" + `{"message":`,
			matches: []string{assignment, logged},
			lines:   []int{1, 2},
		},
		{
			name:    "complete array record",
			input:   "INFO prefix\n[" + second + "]\n" + `{"message":`,
			matches: []string{field},
			lines:   []int{2},
		},
		{
			name:    "valid scalar JSONL retains support",
			input:   `"` + logged + `"` + "\nfalse\n" + second + "\n",
			matches: []string{logged, field},
			lines:   []int{1, 3},
		},
		{
			name:    "pretty document takes precedence",
			input:   "{\n\"records\": [\n" + first + ",\n" + second + "\n]\n}\n",
			matches: []string{logged, field},
			lines:   []int{3, 4},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output, findings := Redact(tc.input, Config{})
			if len(findings) != len(tc.matches) {
				t.Fatalf("findings=%d, want %d from complete records and raw assignments", len(findings), len(tc.matches))
			}
			AddLineInfo(tc.input, findings)
			for i, raw := range tc.matches {
				f := findings[i]
				start := strings.Index(tc.input, raw)
				if f.Category != CategoryPassword || f.Match != raw || f.Start != start || f.End != start+len(raw) || f.Line != tc.lines[i] {
					t.Errorf("finding %d lost its category, raw source span, or physical line: %+v", i, f)
				}
			}
			if strings.Contains(output, "synthetic-never-valid") {
				t.Error("complete record or ordinary assignment leaked credential bytes")
			}
			for _, public := range []string{"public-neighbor", `"status":"ready"`, "INFO starting", "INFO finished", `{"message":`, `{"broken":"\q"}`} {
				if strings.Contains(tc.input, public) && !strings.Contains(output, public) {
					t.Errorf("redaction consumed neighboring content %q", public)
				}
			}
		})
	}
}

func TestJSONLCompleteRecordsKeepFalsePositiveProtections(t *testing.T) {
	input := "INFO starting\n" +
		`{"message":"PASSWORD=\u0061\u0062\u0063\u0064\u0065\u0066\u0067"}` + "\n" +
		`{"message":"PWD=\u002fhome/agent/project"}` + "\n" +
		`{"message":"PASSWORD=\u003credacted\u003e"}` + "\n" +
		`{"not\"password":"public-neighbor-long"}` + "\n" +
		`{"pass\u0077ord_hint":"public-neighbor-long"}` + "\n" +
		`{"message":`
	output, findings := Redact(input, Config{})
	if len(findings) != 0 || output != input {
		t.Errorf("malformed neighbors disabled decoded false-positive protections: %+v", findings)
	}
}

func TestJSONLCompleteRecordsKeepAllowlistsAndDisabledCategories(t *testing.T) {
	logged := `PASSWORD=\"synthetic-never-valid-allowlisted\"`
	input := "INFO starting\n" + `{"message":"` + logged + `"}` + "\n" + `{"message":`
	for _, allowed := range []string{logged, `PASSWORD="synthetic-never-valid-allowlisted"`} {
		output, findings := Redact(input, Config{Allowlist: []string{"^" + regexp.QuoteMeta(allowed) + "$"}})
		if len(findings) != 0 || output != input {
			t.Error("raw or logical allowlist failed for a complete record in a mixed log")
		}
	}
	output, findings := Redact(input, Config{DisabledCategories: []Category{CategoryPassword}})
	if len(findings) != 0 || output != input {
		t.Error("disabled password category was detected in a complete record")
	}
}

func TestJSONEscapedContentAllowlistsAndDisabledCategories(t *testing.T) {
	provider := "gh" + "p_" + strings.Repeat("q", 40)
	encodedProvider := `\u0067` + provider[1:]
	encodedField := `"pass\u0077ord":"synthetic\u002dnever\u002dvalid"`
	tests := []struct {
		name         string
		input        string
		rawMatch     string
		logicalMatch string
		category     Category
	}{
		{
			name:         "quoted assignment",
			input:        `{"message":"PASSWORD=\"synthetic\u002dnever\u002dvalid\""}`,
			rawMatch:     `PASSWORD=\"synthetic\u002dnever\u002dvalid\"`,
			logicalMatch: `PASSWORD="synthetic-never-valid"`,
			category:     CategoryPassword,
		},
		{
			name:         "provider keeps priority in a logged assignment",
			input:        `{"message":"token=` + encodedProvider + `"}`,
			rawMatch:     encodedProvider,
			logicalMatch: provider,
			category:     CategoryGitHubToken,
		},
		{
			name:         "encoded property key retains raw value",
			input:        "{" + encodedField + "}",
			rawMatch:     encodedField,
			logicalMatch: `"password":"synthetic\u002dnever\u002dvalid"`,
			category:     CategoryPassword,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertJSONRawFinding(t, tc.input, tc.rawMatch, tc.category)
			for _, allowed := range []string{tc.rawMatch, tc.logicalMatch} {
				output, findings := Redact(tc.input, Config{Allowlist: []string{"^" + regexp.QuoteMeta(allowed) + "$"}})
				if len(findings) != 0 || output != tc.input {
					t.Error("anchored raw or logical allowlist did not suppress the complete match")
				}
			}
			cfg := Config{DisabledCategories: []Category{tc.category}}
			if tc.category == CategoryGitHubToken {
				// The lower-priority generic assignment is still eligible when
				// only the provider category is disabled.
				cfg.DisabledCategories = append(cfg.DisabledCategories, CategoryGenericSecret)
			}
			output, findings := Redact(tc.input, cfg)
			if len(findings) != 0 || output != tc.input {
				t.Error("disabled categories were detected in decoded content")
			}
		})
	}
}

func TestJSONEscapedContentExtraPatternsStayRaw(t *testing.T) {
	rawMarker := `\u0050ASSWORD`
	input := `{"message":"` + rawMarker + `\u003dsynthetic-never-valid-custom"}`
	cfg := Config{
		DisabledCategories: []Category{CategoryPassword},
		ExtraPatterns:      map[Category][]string{"CUSTOM_RAW": {regexp.QuoteMeta(rawMarker)}},
	}
	findings := Scan(input, cfg)
	start := strings.Index(input, rawMarker)
	if len(findings) != 1 || findings[0].Category != "CUSTOM_RAW" || findings[0].Match != rawMarker || findings[0].Start != start || findings[0].End != start+len(rawMarker) {
		t.Errorf("raw custom pattern was changed or suppressed: %+v", findings)
	}
	cfg.ExtraPatterns = map[Category][]string{"CUSTOM_LOGICAL": {regexp.QuoteMeta("PASSWORD=synthetic-never-valid-custom")}}
	if findings := Scan(input, cfg); len(findings) != 0 {
		t.Errorf("custom pattern was incorrectly applied to decoded content: %+v", findings)
	}
}

func TestJSONEscapedContentNestedFieldPreservesResiduals(t *testing.T) {
	provider := "gh" + "p_" + strings.Repeat("q", 40)
	logicalField := `"password":"sensitive-prefix:` + provider + `:sensitive-suffix"`
	rawField := `\"password\":\"sensitive-prefix:` + provider + `:sensitive-suffix\"`
	input := `{"message":"{` + rawField + `}","status":"ready"}`
	output, findings := Redact(input, Config{})
	if len(findings) != 3 {
		t.Fatalf("findings=%d, want provider and two residual spans", len(findings))
	}
	for _, f := range findings {
		if f.Match != input[f.Start:f.End] {
			t.Error("nested field finding lost its original raw range")
		}
	}
	if findings[0].Category != CategoryPassword || findings[1].Category != CategoryGitHubToken || findings[1].Match != provider || findings[2].Category != CategoryPassword {
		t.Error("nested field changed provider priority or residual categories")
	}
	if strings.Contains(output, "sensitive-") || strings.Contains(output, provider) || !strings.Contains(output, `"status":"ready"`) {
		t.Error("nested field redaction exposed credential content or consumed a public field")
	}
	for _, allowed := range []string{rawField, logicalField} {
		findings := Scan(input, Config{Allowlist: []string{"^" + regexp.QuoteMeta(allowed) + "$"}})
		if len(findings) != 1 || findings[0].Category != CategoryGitHubToken || findings[0].Match != provider {
			t.Error("nested residuals did not inherit the original raw and logical field allowlist contexts")
		}
	}
}

func TestJSONEscapedContentQuotedAssignmentPreservesResiduals(t *testing.T) {
	provider := "gh" + "p_" + strings.Repeat("q", 40)
	logicalAssignment := `PASSWORD="sensitive-prefix:` + provider + `:sensitive-suffix"`
	rawAssignment := `PASSWORD=\"sensitive-prefix:` + provider + `:sensitive-suffix\"`
	input := `{"message":"` + rawAssignment + `","status":"ready"}`
	if !json.Valid([]byte(input)) {
		t.Fatal("invalid JSON test fixture")
	}
	output, findings := Redact(input, Config{})
	if len(findings) != 3 {
		t.Fatalf("findings=%d, want provider and two quoted-value residuals", len(findings))
	}
	for i, want := range []string{"sensitive-prefix:", provider, ":sensitive-suffix"} {
		f := findings[i]
		category := CategoryPassword
		if i == 1 {
			category = CategoryGitHubToken
		}
		start := strings.Index(input, want)
		if f.Category != category || f.Match != want || f.Start != start || f.End != start+len(want) {
			t.Errorf("finding %d changed provider priority or raw quoted-value bounds: %+v", i, f)
		}
	}
	if strings.Contains(output, "sensitive-") || strings.Contains(output, provider) || !strings.Contains(output, `"status":"ready"`) {
		t.Error("quoted assignment exposed credential content or consumed a public field")
	}

	for _, allowed := range []string{rawAssignment, logicalAssignment} {
		findings := Scan(input, Config{Allowlist: []string{"^" + regexp.QuoteMeta(allowed) + "$"}})
		if len(findings) != 1 || findings[0].Category != CategoryGitHubToken || findings[0].Match != provider {
			t.Error("quoted-value residuals lost their full raw or logical assignment allowlist context")
		}
	}
	providerAllowlist := "^" + regexp.QuoteMeta(provider) + "$"
	output, findings = Redact(input, Config{Allowlist: []string{providerAllowlist}})
	if len(findings) != 2 || findings[0].Category != CategoryPassword || findings[1].Category != CategoryPassword {
		t.Error("allowlisting the provider suppressed uncovered password portions")
	}
	if strings.Contains(output, "sensitive-") || !strings.Contains(output, provider) {
		t.Error("provider-only allowlist did not preserve protection of the remaining password")
	}
	output, findings = Redact(input, Config{Allowlist: []string{providerAllowlist, "^" + regexp.QuoteMeta(logicalAssignment) + "$"}})
	if len(findings) != 0 || output != input {
		t.Error("allowlisting the provider and complete assignment changed the input")
	}
}

func TestPrivateKeyCompleteBlocks(t *testing.T) {
	body := strings.Repeat("c3ludGhldGlj", 8)
	for _, kind := range []string{"PRIVATE KEY", "RSA PRIVATE KEY", "DSA PRIVATE KEY", "EC PRIVATE KEY", "OPENSSH PRIVATE KEY", "ENCRYPTED PRIVATE KEY"} {
		t.Run(kind, func(t *testing.T) {
			block := "-----BEGIN " + kind + "-----\n" + body + "\n-----END " + kind + "-----"
			assertJSONRawFinding(t, "public-before\n"+block+"\npublic-after", block, CategoryPrivateKey)
			assertJSONRawFinding(t, "-----BEGIN "+kind+"-----", "-----BEGIN "+kind+"-----", CategoryPrivateKey)
		})
	}
	block := "-----BEGIN RSA PRIVATE KEY-----\r\nProc-Type: 4,ENCRYPTED\r\nDEK-Info: AES-256-CBC,00112233445566778899AABBCCDDEEFF\r\n\r\n" + body + "\r\n-----END RSA PRIVATE KEY-----"
	assertJSONRawFinding(t, "public-before\r\n"+block+"\r\npublic-after", block, CategoryPrivateKey)
	spacedMetadata := strings.Replace(block, "Proc-Type:", " \tProc-Type \t:", 1)
	spacedMetadata = strings.Replace(spacedMetadata, "DEK-Info:", "\tDEK-Info :", 1)
	assertJSONRawFinding(t, "public-before\r\n"+spacedMetadata+"\r\npublic-after", spacedMetadata, CategoryPrivateKey)
	block = "-----BEGIN PRIVATE KEY----- \t\n" + body + "\n-----END PRIVATE KEY-----"
	assertJSONRawFinding(t, "public-before\n"+block+"\npublic-after", block, CategoryPrivateKey)
}

func TestPrivateKeyJSONAndJSONL(t *testing.T) {
	block := "-----BEGIN PRIVATE KEY-----\n" + strings.Repeat("c3ludGhldGlj", 8) + "\n-----END PRIVATE KEY-----"
	encoded, err := json.Marshal(block)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(encoded[1 : len(encoded)-1])
	tests := []struct {
		name  string
		key   string
		value string
		cfg   Config
	}{
		{name: "private key field", key: "private_key", value: raw},
		{name: "password field", key: "password", value: raw},
		{name: "disabled password field", key: "password", value: raw, cfg: Config{DisabledCategories: []Category{CategoryPassword}}},
		{name: "Unicode escapes in protected field", key: "password", value: strings.Replace(strings.ReplaceAll(raw, `\n`, `\u000a`), "c3ludGhldGlj", `\u00633ludGhldGlj`, 1), cfg: Config{DisabledCategories: []Category{CategoryPassword}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := `{"` + tc.key + `":"` + tc.value + `","public":"ready"}`
			if !json.Valid([]byte(input)) {
				t.Fatal("invalid JSON test fixture")
			}
			output, findings := Redact(input, tc.cfg)
			start := strings.Index(input, tc.value)
			if len(findings) != 1 || findings[0].Category != CategoryPrivateKey || findings[0].Match != tc.value || findings[0].Start != start || findings[0].End != start+len(tc.value) {
				t.Fatalf("complete PEM did not retain its raw source range: %+v", findings)
			}
			if strings.Contains(output, "c3ludGhldGlj") || !strings.Contains(output, `"public":"ready"`) {
				t.Error("PEM body leaked or neighboring public field was consumed")
			}
		})
	}
	input := `{"private_key":"` + raw + `","public":"first"}` + "\n" + `{"password":"` + raw + `","public":"second"}` + "\n"
	findings := Scan(input, Config{})
	if len(findings) != 2 {
		t.Fatalf("JSONL findings=%d, want two complete private keys", len(findings))
	}
	AddLineInfo(input, findings)
	for i, f := range findings {
		if f.Category != CategoryPrivateKey || f.Match != raw || f.Line != i+1 || f.Match != input[f.Start:f.End] {
			t.Errorf("PEM JSONL finding has wrong source range or physical line: %+v", f)
		}
	}
}

func TestPrivateKeyAllowlistsAndProviderOverlap(t *testing.T) {
	body := strings.Repeat("c3ludGhldGlj", 8)
	header := "-----BEGIN PRIVATE KEY-----"
	block := header + "\n" + body + "\n-----END PRIVATE KEY-----"
	encoded, _ := json.Marshal(block)
	raw := string(encoded[1 : len(encoded)-1])
	for _, input := range []string{block, `{"password":"` + raw + `","public":"ready"}`} {
		for _, allowed := range []string{block, raw} {
			if input == block && allowed == raw {
				continue
			}
			output, findings := Redact(input, Config{Allowlist: []string{"^" + regexp.QuoteMeta(allowed) + "$"}})
			if len(findings) != 0 || output != input {
				t.Error("anchored full raw or logical PEM allowlist did not suppress the complete key")
			}
		}
		output, findings := Redact(input, Config{Allowlist: []string{"^" + regexp.QuoteMeta(header) + "$"}})
		if len(findings) != 1 || findings[0].Category != CategoryPrivateKey || strings.Contains(output, body) {
			t.Error("allowlisting only a header exempted the private body")
		}
	}

	provider := "AI" + "za" + strings.Repeat("Q", 35)
	block = header + "\n" + body + "\n" + provider + "A\n" + body + "\n-----END PRIVATE KEY-----"
	encoded, _ = json.Marshal(block)
	raw = string(encoded[1 : len(encoded)-1])
	for _, input := range []string{block, `{"password":"` + raw + `","public":"ready"}`} {
		output, findings := Redact(input, Config{})
		if len(findings) != 3 || findings[0].Category != CategoryPrivateKey || findings[1].Category != CategoryGoogleAPIKey || findings[1].Match != provider || findings[2].Category != CategoryPrivateKey {
			t.Fatalf("provider priority discarded part of a private key: %+v", findings)
		}
		for _, f := range findings {
			if f.Match != input[f.Start:f.End] {
				t.Error("private key residual lost its raw source range")
			}
		}
		if strings.Contains(output, body) || strings.Contains(output, provider) {
			t.Error("provider overlap exposed private key material")
		}
		output, findings = Redact(input, Config{Allowlist: []string{"^" + regexp.QuoteMeta(provider) + "$"}})
		if len(findings) != 2 || findings[0].Category != CategoryPrivateKey || findings[1].Category != CategoryPrivateKey || strings.Contains(output, body) {
			t.Error("provider-only allowlist exempted the rest of the private key")
		}
		findings = Scan(input, Config{Allowlist: []string{"^" + regexp.QuoteMeta(block) + "$"}})
		if len(findings) != 1 || findings[0].Category != CategoryGoogleAPIKey || findings[0].Match != provider {
			t.Error("whole-block allowlist did not preserve the provider's independent category")
		}
	}
}

func TestPrivateKeyBlockBoundaries(t *testing.T) {
	body := strings.Repeat("c3ludGhldGlj", 8)
	rsaHeader := "-----BEGIN RSA PRIVATE KEY-----"
	ec := "-----BEGIN EC PRIVATE KEY-----\n" + body + "\n-----END EC PRIVATE KEY-----"
	for _, prefix := range []string{
		rsaHeader + "\n" + body + "\n-----END EC PRIVATE KEY-----",
		rsaHeader + "\n" + body,
	} {
		input := prefix + "\npublic-neighbor\n" + ec + "\npublic-after"
		output, findings := Redact(input, Config{})
		if len(findings) != 2 || findings[0].Match != rsaHeader || findings[1].Match != ec {
			t.Errorf("mismatched or truncated PEM swallowed neighboring content: %+v", findings)
		}
		if !strings.Contains(output, "public-neighbor") || !strings.Contains(output, "public-after") {
			t.Error("PEM matching consumed a public neighbor")
		}
	}
}

func TestCredentialBodyOverlapCoverage(t *testing.T) {
	provider := "gh" + "p_" + strings.Repeat("q", 40)
	tests := []struct {
		name     string
		input    string
		category Category
	}{
		{"bare password", "PASSWORD=sensitive-prefix:" + provider + ":sensitive-suffix", CategoryPassword},
		{"logged bare password", `{"message":"PASSWORD=sensitive-prefix:` + provider + `:sensitive-suffix","public":"ready"}`, CategoryPassword},
		{"encoded bare password", `{"message":"PASSWORD\u003dsensitive-prefix:` + provider + `:sensitive-suffix","public":"ready"}`, CategoryPassword},
		{"bare API key", "api_key=sensitive-prefix_" + provider + "_sensitive-suffix", CategoryGenericAPIKey},
		{"bare generic secret", "token=sensitive-prefix_" + provider + "_sensitive-suffix", CategoryGenericSecret},
		{"bearer token", `{"authorization":"Bearer sensitive-prefix.` + provider + `.sensitive-suffix","public":"ready"}`, CategoryBearerToken},
		{"JWT", "eyJhbGciOiJub25lIn0.eyJzdWIiOiJzeW50aGV0aWMifQ.sensitive-prefix_" + provider + "_sensitive-suffix", CategoryJWT},
		{"database URL", `{"url":"postgres://synthetic-user:sensitive-prefix:` + provider + `:sensitive-suffix@db.invalid/project","public":"ready"}`, CategoryDatabaseURL},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output, findings := Redact(tc.input, Config{})
			if len(findings) != 3 || findings[0].Category != tc.category || findings[1].Category != CategoryGitHubToken || findings[1].Match != provider || findings[2].Category != tc.category {
				t.Fatalf("provider overlap dropped credential bytes: %+v", findings)
			}
			for _, f := range findings {
				if f.Match != tc.input[f.Start:f.End] {
					t.Error("credential residual lost its raw source range")
				}
			}
			if strings.Contains(output, "sensitive-prefix") || strings.Contains(output, "sensitive-suffix") {
				t.Error("redaction exposed a credential fragment")
			}
			if strings.Contains(tc.input, `"public":"ready"`) && !strings.Contains(output, `"public":"ready"`) {
				t.Error("credential redaction consumed a neighboring public field")
			}
			output, findings = Redact(tc.input, Config{Allowlist: []string{"^" + regexp.QuoteMeta(provider) + "$"}})
			if len(findings) != 2 || strings.Contains(output, "sensitive-prefix") || strings.Contains(output, "sensitive-suffix") {
				t.Error("provider-only allowlist exempted uncovered credential bytes")
			}
		})
	}
	google := "AI" + "za" + strings.Repeat("Q", 35)
	output, findings := Redact("aws_secret="+google+"Z", Config{})
	if len(findings) != 2 || findings[0].Category != CategoryGoogleAPIKey || findings[0].Match != google || findings[1].Category != CategoryAWSSecretKey || findings[1].Match != "Z" || strings.Contains(output, "Z") {
		t.Error("provider overlap exposed the remainder of an AWS secret assignment")
	}
}

func TestCredentialBodyExactProviderAllowlistsAndRawExtras(t *testing.T) {
	provider := "gh" + "p_" + strings.Repeat("q", 40)
	for _, input := range []string{
		"PASSWORD=" + provider,
		"PASSWORD=\"" + provider + "\"",
		"api_key='" + provider + "'",
		"token=" + provider,
		"Bearer " + provider,
		`{"message":"PASSWORD=` + provider + `","public":"ready"}`,
	} {
		findings := Scan(input, Config{})
		if len(findings) != 1 || findings[0].Category != CategoryGitHubToken || findings[0].Match != provider {
			t.Error("public assignment or Bearer context produced a spurious residual")
		}
		output, findings := Redact(input, Config{Allowlist: []string{"^" + regexp.QuoteMeta(provider) + "$"}})
		if len(findings) != 0 || output != input {
			t.Error("anchored exact-provider allowlist no longer suppresses the complete value")
		}
	}
	input := "custom-prefix:" + provider + ":custom-suffix"
	for _, category := range []Category{CategoryPassword, CategoryPrivateKey, CategoryBearerToken} {
		cfg := Config{ExtraPatterns: map[Category][]string{category: {regexp.QuoteMeta(input)}}}
		findings := Scan(input, cfg)
		if len(findings) != 1 || findings[0].Category != CategoryGitHubToken || findings[0].Match != provider {
			t.Error("custom pattern incorrectly inherited builtin credential-body protection")
		}
	}
	input = `{"not\"` + provider + `":"public"}`
	assertJSONRawFinding(t, input, provider, CategoryGitHubToken)
}

func TestJSONTruncatedContentDetection(t *testing.T) {
	logged := `PASSWORD=\"synthetic-never-valid-open-record\"`
	encodedField := `"pass\u0077ord":"synthetic-never-valid-open-field"`
	tests := []struct {
		name, input, rawMatch string
	}{
		{"missing object brace", `{"note":"public-neighbor","message":"` + logged + `"`, logged},
		{"missing brace after encoded key", `{"note":"public-neighbor",` + encodedField, encodedField},
		{"trailing object comma", `{"message":"` + logged + `",`, logged},
		{"unfinished later field", `{"message":"` + logged + `","next":"unfinished`, logged},
		{"unfinished later number", `{"message":"` + logged + `","attempt":1e`, logged},
		{"large numeric exponent before message", `{"attempt":1e999999,"message":"` + logged + `"`, logged},
		{"open array", `[{"message":"` + logged + `"},`, logged},
		{"open nested containers", `{"records":[{"message":"` + logged + `"}`, logged},
		{"pretty unfinished root", "{\n  \"records\": [\n    {" + encodedField + "}\n", encodedField},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if json.Valid([]byte(tc.input)) {
				t.Fatal("fixture must be an unfinished JSON container")
			}
			assertJSONRawFinding(t, tc.input, tc.rawMatch, CategoryPassword)
		})
	}
}

func TestJSONTruncatedCredentialValues(t *testing.T) {
	tests := []struct {
		name, field, rawMatch string
		category              Category
	}{
		{"password", `"password":"synthetic-never-valid-open-password`, "", CategoryPassword},
		{"encoded password key", `"pass\u0077ord":"synthetic-never-valid-open-password`, "", CategoryPassword},
		{"database PWD path", `"MYSQL_PWD":"\u002fhome/agent/synthetic-project`, "", CategoryPassword},
		{"API key", `"api_key":"syntheticNeverValidApiKey123`, "", CategoryGenericAPIKey},
		{"encoded API key", `"api\u005fkey":"syntheticNeverValidApiKey123`, "", CategoryGenericAPIKey},
		{"generic secret", `"access_token":"syntheticNeverValidToken123`, "", CategoryGenericSecret},
		{"AWS secret", `"aws_secret_access_key":"` + strings.Repeat("Q", 40), "", CategoryAWSSecretKey},
		{"escaped quote and backslash", `"password":"synthetic\"never\\valid`, "", CategoryPassword},
		{"Unicode and surrogate pairs", `"password":"\u96ea` + strings.Repeat(`\ud83d\ude00`, 7), "", CategoryPassword},
		{"encoded message assignment", `"message":"\u0050ASSWORD\u003dsynthetic-never-valid-open-message`, `\u0050ASSWORD\u003dsynthetic-never-valid-open-message`, CategoryPassword},
		{"quoted message assignment", `"message":"PASSWORD=\"synthetic-never-valid-open-message`, `PASSWORD=\"synthetic-never-valid-open-message`, CategoryPassword},
	}
	for _, tail := range []string{`\`, `\u`, `\u1`, `\u12`, `\u123`} {
		tests = append(tests, struct {
			name, field, rawMatch string
			category              Category
		}{"pending field escape " + tail, `"password":"synthetic-never-valid-pending` + tail, "", CategoryPassword})
		raw := `PASSWORD\u003dsynthetic-never-valid-pending` + tail
		tests = append(tests, struct {
			name, field, rawMatch string
			category              Category
		}{"pending message escape " + tail, `"message":"` + raw, raw, CategoryPassword})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := `{"note":"public-neighbor",` + tc.field
			rawMatch := tc.rawMatch
			if rawMatch == "" {
				rawMatch = tc.field
			}
			assertJSONRawFinding(t, input, rawMatch, tc.category)
		})
	}
}

func TestJSONTruncatedFalsePositiveProtections(t *testing.T) {
	short := `\u0061\u0062\u0063\u0064\u0065\u0066\u0067`
	tests := []struct {
		name, input string
	}{
		{"short complete message in prefix", `{"message":"PASSWORD=` + short + `",`},
		{"short unfinished field", `{"password":"` + short},
		{"short encoded field", `{"pass\u0077ord":"` + short},
		{"short unfinished message", `{"message":"PASSWORD=` + short},
		{"seven surrogate pair runes", `{"password":"` + strings.Repeat(`\ud83d\ude00`, 7)},
		{"short decoded message before whitespace", `{"message":"PASSWORD=abc\u0020defg`},
		{"short API key", `{"api_key":"123456789abcdef`},
		{"short generic secret", `{"token":"123456789abcdef`},
		{"short AWS secret", `{"aws_secret_access_key":"` + strings.Repeat("Q", 39)},
		{"long AWS secret", `{"aws_secret_access_key":"` + strings.Repeat("Q", 41)},
		{"PWD path", `{"PWD":"\u002fhome/agent/project`},
		{"encoded OLDPWD path", `{"OLD\u0050WD":"\u002fhome/agent/project`},
		{"logged PWD path", `{"message":"PWD=\u002fhome/agent/project`},
		{"placeholder", `{"password":"\u003credacted\u003e`},
		{"placeholder before pending escape", `{"password":"\u003credacted\u003e\u12`},
		{"PWD before pending escape", `{"PWD":"\u002fhome/agent/project\u12`},
		{"logged placeholder", `{"message":"PASSWORD=\u003credacted\u003e`},
		{"star placeholder", `{"password":"********`},
		{"neighboring key", `{"password_hint":"public-neighbor-long`},
		{"escaped quote in key", `{"not\"password":"public-neighbor-long`},
		{"encoded quote in key", `{"not\u0022password":"public-neighbor-long`},
		{"literal backslash-u key", `{"pass\\u0077ord":"public-neighbor-long`},
		{"literal backslash-u separator", `{"message":"PASSWORD\\u003dsynthetic-never-valid-literal`},
		{"unfinished property key", `{"\u0050ASSWORD=synthetic-never-valid-key`},
		{"complete property key awaiting colon", `{"PASSWORD=\"synthetic-never-valid-key\""`},
		{"complete encoded property key awaiting colon", `{"\u0050ASSWORD=synthetic-never-valid-key"`},
		{"unfinished root string", `"\u0050ASSWORD=synthetic-never-valid-scalar`},
		{"unfinished array string", `["\u0050ASSWORD=synthetic-never-valid-array`},
		{"non-string password field", `{"password":12345678`},
	}
	for _, tail := range []string{`\`, `\u`, `\u1`, `\u12`, `\u123`} {
		tests = append(tests,
			struct{ name, input string }{"short field with pending escape " + tail, `{"password":"` + short + tail},
			struct{ name, input string }{"short message with pending escape " + tail, `{"message":"PASSWORD=` + short + tail},
		)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output, findings := Redact(tc.input, Config{})
			if len(findings) != 0 || output != tc.input {
				t.Errorf("unfinished non-credential produced findings: %+v", findings)
			}
		})
	}
}

func TestJSONTruncatedDoesNotDecodeMalformedInput(t *testing.T) {
	encoded := `\u0050ASSWORD\u003dsynthetic-never-valid-malformed`
	for _, input := range []string{
		`log message="` + encoded,
		`{"message":"` + encoded + `",BROKEN`,
		`{"message":"` + encoded + `\q`,
		`{"message":"` + encoded + `\u12x`,
		`{"message":"` + encoded + "\tinvalid-control",
		`{"message":"` + encoded + `" "missing-comma`,
		`{} {"message":"` + encoded,
		`{"message":"` + encoded + `"} {`,
	} {
		output, findings := Redact(input, Config{})
		if len(findings) != 0 || output != input {
			t.Errorf("definite malformed syntax was decoded as an unfinished record: %+v", findings)
		}
	}
	for _, tail := range []string{`\q`, `\u12x`} {
		raw := `PASSWORD=synthetic-never-valid-raw` + tail
		assertJSONRawFinding(t, `{"message":"`+raw, raw, CategoryPassword)
	}
}

func TestJSONTruncatedJSONLSourceOffsets(t *testing.T) {
	complete := `"pass\u0077ord":"synthetic-never-valid-complete"`
	message := `\u0050ASSWORD\u003dsynthetic-never-valid-message\u12`
	partial := `"pass\u0077ord":"synthetic-never-valid-partial`
	ordinary := "PASSWORD=synthetic-never-valid-ordinary"
	input := "INFO start\r\n" + "{" + complete + ",\"note\":\"public-neighbor\"}\r\n" +
		`{"message":"` + message + "\r\n" + `{"broken":"\q"}` + "\r\n" +
		"{" + partial + "\r\n" + ordinary + "\nINFO end"
	output, findings := Redact(input, Config{})
	wantMatches := []string{complete, message, partial, ordinary}
	if len(findings) != len(wantMatches) {
		t.Fatalf("findings=%d, want %d complete and unfinished record credentials", len(findings), len(wantMatches))
	}
	AddLineInfo(input, findings)
	for i, raw := range wantMatches {
		f := findings[i]
		start := strings.Index(input, raw)
		wantLine := []int{2, 3, 5, 6}[i]
		if f.Category != CategoryPassword || f.Match != raw || f.Start != start || f.End != start+len(raw) || f.Line != wantLine {
			t.Errorf("finding %d lost its exact raw bytes or physical line: %+v", i, f)
		}
	}
	if strings.Contains(output, "synthetic-never-valid") {
		t.Error("unfinished JSONL record leaked available credential bytes")
	}
	for _, public := range []string{"INFO start", "INFO end", "public-neighbor", `{"broken":"\q"}`} {
		if !strings.Contains(output, public) {
			t.Errorf("redaction consumed neighboring content %q", public)
		}
	}
}

func TestJSONTruncatedAllowlistsAndDisabledCategories(t *testing.T) {
	tests := []struct {
		name, input, rawMatch, logicalMatch string
	}{
		{"partial field", `{"password":"synthetic-never-valid-allowed`, `"password":"synthetic-never-valid-allowed`, `"password":"synthetic-never-valid-allowed`},
		{"partial encoded field", `{"pass\u0077ord":"synthetic-never-valid-allowed`, `"pass\u0077ord":"synthetic-never-valid-allowed`, `"password":"synthetic-never-valid-allowed`},
		{"partial encoded message", `{"message":"\u0050ASSWORD=synthetic\u002dnever\u002dvalid`, `\u0050ASSWORD=synthetic\u002dnever\u002dvalid`, `PASSWORD=synthetic-never-valid`},
		{"complete quoted message", `{"message":"PASSWORD=\"synthetic-never-valid-allowed\""`, `PASSWORD=\"synthetic-never-valid-allowed\"`, `PASSWORD="synthetic-never-valid-allowed"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertJSONRawFinding(t, tc.input, tc.rawMatch, CategoryPassword)
			for _, allowed := range []string{tc.rawMatch, tc.logicalMatch} {
				output, findings := Redact(tc.input, Config{Allowlist: []string{"^" + regexp.QuoteMeta(allowed) + "$"}})
				if len(findings) != 0 || output != tc.input {
					t.Error("raw or logical allowlist did not match the actual unfinished credential span")
				}
			}
			output, findings := Redact(tc.input, Config{DisabledCategories: []Category{CategoryPassword}})
			if len(findings) != 0 || output != tc.input {
				t.Error("disabled password category was detected in an unfinished record")
			}
		})
	}
	field := `"password":"synthetic-never-valid-allowed\u12`
	input := "{" + field
	output, findings := Redact(input, Config{Allowlist: []string{"^" + regexp.QuoteMeta(field) + "$"}})
	if len(findings) != 0 || output != input {
		t.Error("raw allowlist did not retain pending escape bytes in its source context")
	}
	for _, completed := range []string{
		`"password":"synthetic-never-valid-allowed"`,
		field + `"`,
	} {
		output, findings = Redact(input, Config{Allowlist: []string{"^" + regexp.QuoteMeta(completed) + "$"}})
		if len(findings) != 1 || findings[0].Match != field || strings.Contains(output, "synthetic") || strings.Contains(output, `\u12`) {
			t.Error("synthetically completed field leaked into allowlist context")
		}
	}
	raw := `\u0050ASSWORD=synthetic\u002dnever\u002dvalid\u12`
	input = `{"message":"` + raw
	output, findings = Redact(input, Config{Allowlist: []string{"^" + regexp.QuoteMeta(raw) + "$"}})
	if len(findings) != 0 || output != input {
		t.Error("raw message allowlist did not match all available pending bytes")
	}
	output, findings = Redact(input, Config{Allowlist: []string{`^PASSWORD=synthetic-never-valid$`}})
	if len(findings) != 1 || findings[0].Match != raw || strings.Contains(output, "synthetic") || strings.Contains(output, `\u12`) {
		t.Error("logical allowlist exempted an unknown unfinished escape suffix")
	}
	input = `{"note":"public\u12`
	cfg := Config{ExtraPatterns: map[Category][]string{"CUSTOM_RAW": {regexp.QuoteMeta(`\u12`)}}}
	output, findings = Redact(input, cfg)
	if len(findings) != 1 || findings[0].Category != "CUSTOM_RAW" || findings[0].Match != `\u12` || strings.Contains(output, `\u12`) {
		t.Error("custom pattern lost raw-input semantics in an unfinished JSON string")
	}
}

func TestJSONTruncatedProviderOverlap(t *testing.T) {
	provider := "gh" + "p_" + strings.Repeat("q", 40)
	for _, field := range []string{
		`"password":"sensitive-prefix:` + provider + `:sensitive-suffix\u12`,
		`"message":"PASSWORD\u003dsensitive-prefix:` + provider + `:sensitive-suffix\u12`,
	} {
		input := `{"note":"public-neighbor",` + field
		output, findings := Redact(input, Config{})
		if len(findings) != 3 || findings[0].Category != CategoryPassword || findings[1].Category != CategoryGitHubToken || findings[1].Match != provider || findings[2].Category != CategoryPassword {
			t.Fatalf("provider overlap dropped an unfinished credential span: %+v", findings)
		}
		for _, f := range findings {
			if f.Match != input[f.Start:f.End] {
				t.Error("unfinished credential residual lost its raw source range")
			}
		}
		if findings[0].Match != "sensitive-prefix:" || findings[2].Match != `:sensitive-suffix\u12` {
			t.Error("unfinished credential residual included public context or omitted pending bytes")
		}
		if strings.Contains(output, "sensitive-") || strings.Contains(output, `\u12`) || !strings.Contains(output, "public-neighbor") {
			t.Error("unfinished credential redaction exposed a fragment or consumed public text")
		}
		output, findings = Redact(input, Config{Allowlist: []string{"^" + regexp.QuoteMeta(provider) + "$"}})
		if len(findings) != 2 || strings.Contains(output, "sensitive-") || strings.Contains(output, `\u12`) {
			t.Error("provider-only allowlist exempted an unfinished credential residual")
		}
	}
	for _, tail := range []string{"", `\u12`} {
		input := `{"password":"` + provider + tail
		output, findings := Redact(input, Config{Allowlist: []string{"^" + regexp.QuoteMeta(provider) + "$"}})
		if tail == "" {
			if len(findings) != 0 || output != input {
				t.Error("exact-provider allowlist created a public-context residual in an unfinished field")
			}
		} else if len(findings) != 1 || findings[0].Category != CategoryPassword || findings[0].Match != tail || strings.Contains(output, tail) {
			t.Error("allowlisted provider exposed a pending credential escape tail")
		}
	}
}

func TestJSONTruncatedKeepsCrossRecordRawMatches(t *testing.T) {
	body := strings.Repeat("c3ludGhldGlj", 8)
	tests := []struct {
		name, raw, sensitive string
		category             Category
	}{
		{
			name:      "complete PEM crosses unfinished message record",
			raw:       "-----BEGIN PRIVATE KEY-----\n" + body + "\n-----END PRIVATE KEY-----",
			sensitive: body,
			category:  CategoryPrivateKey,
		},
		{
			name:      "assignment separator crosses unfinished message record",
			raw:       "PASSWORD\n=synthetic-never-valid-cross-line",
			sensitive: "synthetic-never-valid-cross-line",
			category:  CategoryPassword,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := `{"note":"public-before","message":"` + tc.raw + "\npublic-after"
			assertJSONRawFinding(t, input, tc.raw, tc.category)
			output, _ := Redact(input, Config{})
			if strings.Contains(output, tc.sensitive) || !strings.Contains(output, "public-before") || !strings.Contains(output, "public-after") {
				t.Error("decoding an unfinished record discarded a longer raw credential match")
			}
		})
	}
}

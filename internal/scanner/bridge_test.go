package scanner

import (
	"strings"
	"testing"
)

func TestDefaultBridgeConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultBridgeConfig()

	if cfg.MinSeverity != SeverityWarning {
		t.Errorf("MinSeverity = %v, want %v", cfg.MinSeverity, SeverityWarning)
	}
	if cfg.DryRun {
		t.Error("DryRun should be false by default")
	}
	if cfg.Verbose {
		t.Error("Verbose should be false by default")
	}
}

func TestParseCreatedBeadID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "single object",
			input: `{"id":"bd-123"}`,
			want:  "bd-123",
		},
		{
			name:  "array fallback",
			input: `[{"id":"bd-456"}]`,
			want:  "bd-456",
		},
		{
			name:    "empty id",
			input:   `{"id":""}`,
			wantErr: true,
		},
		{
			name:    "invalid json",
			input:   `not-json`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseCreatedBeadID(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseCreatedBeadID(%q) error = nil, want error", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCreatedBeadID(%q) error = %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("parseCreatedBeadID(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSeverityToPriorityBridge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		severity Severity
		want     BeadPriority
	}{
		{"critical maps to P0", SeverityCritical, BeadPriorityP0},
		{"warning maps to P1", SeverityWarning, BeadPriorityP1},
		{"info maps to P3", SeverityInfo, BeadPriorityP3},
		{"unknown maps to P2", Severity("unknown"), BeadPriorityP2},
		{"empty maps to P2", Severity(""), BeadPriorityP2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := SeverityToPriority(tc.severity)
			if got != tc.want {
				t.Errorf("SeverityToPriority(%q) = %d, want %d", tc.severity, got, tc.want)
			}
		})
	}
}

// Note: TestSeverityMeetsThreshold exists in scanner_test.go

func TestFindingSignature(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		finding Finding
		want    string // partial match expected
	}{
		{
			name: "with rule ID",
			finding: Finding{
				File:   "src/main.go",
				Line:   42,
				RuleID: "G101",
			},
			want: "src/main.go:42:G101",
		},
		{
			name: "without rule ID uses message hash",
			finding: Finding{
				File:     "test.py",
				Line:     10,
				Category: "security",
				Message:  "hardcoded secret",
			},
			want: "test.py:10:security:", // starts with this, followed by hash
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := FindingSignature(tc.finding)
			if !strings.HasPrefix(got, tc.want) {
				t.Errorf("FindingSignature() = %q, want prefix %q", got, tc.want)
			}
		})
	}
}

func TestFindingSignatureConsistency(t *testing.T) {
	t.Parallel()

	// Same finding should produce same signature
	f1 := Finding{File: "a.go", Line: 1, RuleID: "R1"}
	f2 := Finding{File: "a.go", Line: 1, RuleID: "R1"}

	sig1 := FindingSignature(f1)
	sig2 := FindingSignature(f2)

	if sig1 != sig2 {
		t.Errorf("Same findings should produce same signature: %q != %q", sig1, sig2)
	}

	// Different findings should produce different signatures
	f3 := Finding{File: "a.go", Line: 2, RuleID: "R1"}
	sig3 := FindingSignature(f3)

	if sig1 == sig3 {
		t.Errorf("Different findings should produce different signatures: both = %q", sig1)
	}
}

func TestBeadFromFinding(t *testing.T) {
	t.Parallel()

	finding := Finding{
		File:       "internal/auth/login.go",
		Line:       42,
		Column:     15,
		Severity:   SeverityCritical,
		Category:   "security",
		RuleID:     "G401",
		Message:    "Use of weak cryptographic primitive",
		Suggestion: "Use crypto/rand instead",
	}

	spec := BeadFromFinding(finding, "myproject")

	// Check title format
	if !strings.HasPrefix(spec.Title, "[CRITICAL]") {
		t.Errorf("Title should start with [CRITICAL], got: %s", spec.Title)
	}
	if !strings.Contains(spec.Title, "G401") {
		t.Errorf("Title should contain rule ID G401, got: %s", spec.Title)
	}

	// Check type
	if spec.Type != "bug" {
		t.Errorf("Type = %q, want %q", spec.Type, "bug")
	}

	// Check priority maps correctly
	if spec.Priority != int(BeadPriorityP0) {
		t.Errorf("Priority = %d, want %d (P0 for critical)", spec.Priority, BeadPriorityP0)
	}

	// Check description contains file location
	if !strings.Contains(spec.Description, "internal/auth/login.go:42:15") {
		t.Errorf("Description should contain file:line:col, got: %s", spec.Description)
	}

	// Check description contains rule
	if !strings.Contains(spec.Description, "G401") {
		t.Errorf("Description should contain rule ID, got: %s", spec.Description)
	}

	// Check description contains suggestion
	if !strings.Contains(spec.Description, "crypto/rand") {
		t.Errorf("Description should contain suggestion, got: %s", spec.Description)
	}

	// Check signature is present
	if spec.Signature == "" {
		t.Error("Signature should not be empty")
	}

	// Check labels
	hasUBSLabel := false
	hasSeverityLabel := false
	for _, label := range spec.Labels {
		if label == "ubs-scan" {
			hasUBSLabel = true
		}
		if label == string(SeverityCritical) {
			hasSeverityLabel = true
		}
	}
	if !hasUBSLabel {
		t.Error("Labels should contain 'ubs-scan'")
	}
	if !hasSeverityLabel {
		t.Error("Labels should contain severity")
	}
}

func TestBeadFromFindingMinimal(t *testing.T) {
	t.Parallel()

	// Minimal finding without optional fields
	finding := Finding{
		File:     "test.go",
		Line:     1,
		Severity: SeverityInfo,
		Message:  "Test message",
	}

	spec := BeadFromFinding(finding, "")

	if spec.Title == "" {
		t.Error("Title should not be empty")
	}
	if spec.Type != "bug" {
		t.Errorf("Type = %q, want %q", spec.Type, "bug")
	}
	if spec.Priority != int(BeadPriorityP3) {
		t.Errorf("Priority = %d, want %d (P3 for info)", spec.Priority, BeadPriorityP3)
	}
}

func TestExtractSignatureFromDesc(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		desc string
		want string
	}{
		{
			name: "explicit signature line",
			desc: "**File:** `test.go:42`\n\n**Signature:** src/main.go:10:G101\n\n---",
			want: "src/main.go:10:G101",
		},
		{
			name: "explicit signature with surrounding whitespace",
			desc: "Some text\n**Signature:**   spaced/sig:1:R1  \nMore text",
			want: "spaced/sig:1:R1",
		},
		{
			name: "legacy format fallback",
			desc: "**File:** `old/path.go:99`\n\nOther content",
			want: "old/path.go:99:ubs-legacy",
		},
		{
			name: "legacy format with column",
			desc: "**File:** `old/path.go:99:5`\n\nOther content",
			want: "old/path.go:99:5:ubs-legacy",
		},
		{
			name: "no signature detectable",
			desc: "Just some random text without any markers",
			want: "",
		},
		{
			name: "empty description",
			desc: "",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractSignatureFromDesc(tc.desc)
			if got != tc.want {
				t.Errorf("extractSignatureFromDesc() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBeadPriorityConstants(t *testing.T) {
	t.Parallel()

	// Verify priority values are correctly ordered
	if BeadPriorityP0 >= BeadPriorityP1 {
		t.Error("P0 should be less than P1")
	}
	if BeadPriorityP1 >= BeadPriorityP2 {
		t.Error("P1 should be less than P2")
	}
	if BeadPriorityP2 >= BeadPriorityP3 {
		t.Error("P2 should be less than P3")
	}
}

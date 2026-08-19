package redaction

import (
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzScanAndRedact ensures ScanAndRedact never panics and produces valid output.
func FuzzScanAndRedact(f *testing.F) {
	// Seed corpus with representative inputs.
	seeds := []string{
		// Empty and simple inputs.
		"",
		"hello world",
		"no secrets here",

		// Inputs with secret-like patterns (built at runtime).
		"token=" + "gh" + "p_" + strings.Repeat("a", 40),
		"key=" + "s" + "k-proj-" + strings.Repeat("b", 45),
		"pass=" + "s" + "k-ant-" + strings.Repeat("c", 45),
		"aws=" + "AKIA" + strings.Repeat("D", 16),

		// Multiple secrets.
		"github=" + "gh" + "p_" + strings.Repeat("e", 40) + " aws=" + "AKIA" + strings.Repeat("F", 16),

		// Edge cases.
		"password=secretpass123",
		"api_key=" + strings.Repeat("g", 20),
		"secret=" + strings.Repeat("h", 20),
		"Bearer " + strings.Repeat("i", 30),

		// JWT-like.
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",

		// Private key header.
		"-----BEGIN RSA PRIVATE KEY-----",

		// Database URL.
		"postgres://user:pass@localhost:5432/db",

		// Unicode.
		"password=\u0000\u00ff\u1234\u5678secretpass",
		"emoji \U0001F600 test",

		// Large inputs.
		strings.Repeat("a", 1000),
		strings.Repeat("a b c password=secret123 ", 100),

		// Malformed/edge patterns.
		"gh",
		"ghp",
		"ghp_",
		"AKIA",
		"sk-",
		"sk-ant",
		"sk-ant-",
		"eyJ",
		"eyJhbGciOiJIUzI1NiJ9.",

		// Newlines and special chars.
		"line1\nline2\npassword=test123\nline4",
		"tab\tseparated\tpassword=val",
		"carriage\rreturn\rpassword=val",

		// Nested patterns.
		"outer password=inner password=secret end",
	}

	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		// Reset patterns for clean state.
		ResetPatterns()

		modes := []Mode{ModeOff, ModeWarn, ModeRedact, ModeBlock}

		for _, mode := range modes {
			cfg := Config{Mode: mode}

			// Must not panic.
			result := ScanAndRedact(input, cfg)

			// Output must be valid UTF-8 if input was valid UTF-8.
			// (If input contains invalid UTF-8 bytes, output may also contain them.)
			if utf8.ValidString(input) && !utf8.ValidString(result.Output) {
				t.Errorf("mode %s: output is not valid UTF-8 for valid UTF-8 input %q", mode, truncateForLog(input))
			}

			// Findings must have valid positions.
			for i, f := range result.Findings {
				if f.Start < 0 {
					t.Errorf("mode %s: finding %d has negative start: %d", mode, i, f.Start)
				}
				if f.End < f.Start {
					t.Errorf("mode %s: finding %d has end < start: end=%d start=%d", mode, i, f.End, f.Start)
				}
				if f.End > len(input) {
					t.Errorf("mode %s: finding %d has end > input length: end=%d len=%d", mode, i, f.End, len(input))
				}
			}

			// Findings must not overlap.
			if len(result.Findings) > 1 {
				sorted := make([]Finding, len(result.Findings))
				copy(sorted, result.Findings)
				sort.Slice(sorted, func(i, j int) bool {
					return sorted[i].Start < sorted[j].Start
				})

				for i := 1; i < len(sorted); i++ {
					if sorted[i].Start < sorted[i-1].End {
						t.Errorf("mode %s: findings overlap: [%d,%d) and [%d,%d)",
							mode, sorted[i-1].Start, sorted[i-1].End, sorted[i].Start, sorted[i].End)
					}
				}
			}

			// Mode-specific checks.
			switch mode {
			case ModeOff:
				if result.Output != input {
					t.Errorf("ModeOff should return unchanged input")
				}
				if len(result.Findings) != 0 {
					t.Errorf("ModeOff should have no findings")
				}
			case ModeWarn:
				if result.Output != input {
					t.Errorf("ModeWarn should not modify output")
				}
			case ModeRedact:
				// Output should differ if there were findings.
				if len(result.Findings) > 0 && result.Output == input {
					t.Errorf("ModeRedact with findings should modify output")
				}
				// Each finding should be replaced with placeholder.
				for _, f := range result.Findings {
					if !strings.Contains(result.Output, f.Redacted) {
						t.Errorf("ModeRedact output missing placeholder %q", f.Redacted)
					}
				}
			case ModeBlock:
				if result.Output != input {
					t.Errorf("ModeBlock should not modify output")
				}
				if len(result.Findings) > 0 && !result.Blocked {
					t.Errorf("ModeBlock with findings should set Blocked=true")
				}
			}
		}
	})
}

// FuzzScan ensures Scan never panics.
func FuzzScan(f *testing.F) {
	f.Add("")
	f.Add("hello world")
	f.Add("password=secret123")
	f.Add("ghp_" + strings.Repeat("x", 40))

	f.Fuzz(func(t *testing.T, input string) {
		ResetPatterns()
		// Must not panic.
		findings := Scan(input, Config{})

		// All findings must have valid positions.
		for i, f := range findings {
			if f.Start < 0 || f.End < f.Start || f.End > len(input) {
				t.Errorf("finding %d has invalid position: start=%d end=%d input_len=%d",
					i, f.Start, f.End, len(input))
			}
		}
	})
}

// FuzzRedact ensures Redact never panics.
func FuzzRedact(f *testing.F) {
	f.Add("")
	f.Add("hello world")
	f.Add("api_key=" + strings.Repeat("y", 20))

	f.Fuzz(func(t *testing.T, input string) {
		ResetPatterns()
		// Must not panic.
		output, findings := Redact(input, Config{})

		// Output must be valid UTF-8 if input was valid UTF-8.
		if utf8.ValidString(input) && !utf8.ValidString(output) {
			t.Errorf("output is not valid UTF-8 for valid UTF-8 input")
		}

		// If there were findings, output should be different.
		if len(findings) > 0 && output == input {
			t.Errorf("Redact with findings should modify output")
		}
	})
}

// FuzzAddLineInfo ensures AddLineInfo never panics.
func FuzzAddLineInfo(f *testing.F) {
	f.Add("")
	f.Add("line1\nline2\nline3")
	f.Add("password=secret123")
	f.Add("line1\npassword=test\nline3")

	f.Fuzz(func(t *testing.T, input string) {
		ResetPatterns()
		result := ScanAndRedact(input, Config{Mode: ModeWarn})
		// Must not panic.
		AddLineInfo(input, result.Findings)

		// All line/column must be positive.
		for i, f := range result.Findings {
			if f.Line < 1 {
				t.Errorf("finding %d has line < 1: %d", i, f.Line)
			}
			if f.Column < 1 {
				t.Errorf("finding %d has column < 1: %d", i, f.Column)
			}
		}
	})
}

// truncateForLog shortens a string for error messages.
func truncateForLog(s string) string {
	if len(s) <= 50 {
		return s
	}
	return s[:50] + "..."
}

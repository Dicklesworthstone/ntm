package serve

import (
	"strings"
	"testing"
)

func TestSafetyEscapeYAMLSingleQuote(t *testing.T) {

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no quotes", "hello world", "hello world"},
		{"single quote", "it's fine", "it''s fine"},
		{"multiple quotes", "it's Bob's", "it''s Bob''s"},
		{"empty string", "", ""},
		{"only quote", "'", "''"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := safetyEscapeYAMLSingleQuote(tc.input)
			if got != tc.want {
				t.Errorf("safetyEscapeYAMLSingleQuote(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSafetyEscapeYAMLDoubleQuote(t *testing.T) {

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no special chars", "hello world", "hello world"},
		{"backslash", `path\to\file`, `path\\to\\file`},
		{"double quote", `say "hello"`, `say \"hello\"`},
		{"newline", "line1\nline2", `line1\nline2`},
		{"carriage return", "line1\rline2", `line1\rline2`},
		{"tab", "col1\tcol2", `col1\tcol2`},
		{"empty string", "", ""},
		{"all specials", "\"\\\n\r\t", `\"\\\n\r\t`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := safetyEscapeYAMLDoubleQuote(tc.input)
			if got != tc.want {
				t.Errorf("safetyEscapeYAMLDoubleQuote(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestClaudeHookScriptReadsCurrentStdinPayload(t *testing.T) {
	for _, want := range []string{
		"HOOK_INPUT=\"$(cat)\"",
		"'.tool_name // empty'",
		"'.tool_input.command // empty'",
		"exit 2",
	} {
		if !strings.Contains(claudeHookScript, want) {
			t.Fatalf("claude hook script missing %q", want)
		}
	}
	if strings.Contains(claudeHookScript, "exit 1\nfi\n\nexit 0") {
		t.Fatal("claude hook script still uses non-blocking exit 1 for denied commands")
	}
}

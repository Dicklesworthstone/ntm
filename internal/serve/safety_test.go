package serve

import "testing"

func TestSafetyEscapeYAMLSingleQuote(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
			got := safetyEscapeYAMLSingleQuote(tc.input)
			if got != tc.want {
				t.Errorf("safetyEscapeYAMLSingleQuote(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSafetyEscapeYAMLDoubleQuote(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
			got := safetyEscapeYAMLDoubleQuote(tc.input)
			if got != tc.want {
				t.Errorf("safetyEscapeYAMLDoubleQuote(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

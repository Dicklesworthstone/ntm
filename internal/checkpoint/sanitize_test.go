package checkpoint

import (
	"testing"
	"unicode/utf8"
)

func TestSanitizeName_UTF8AndLength(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string // We might not know exact expected if it truncates, but we can check validity
	}{
		{
			name:     "ASCII safe",
			input:    "simple-name",
			expected: "simple-name",
		},
		{
			name:     "Replace chars",
			input:    "foo/bar:baz",
			expected: "foo-bar-baz",
		},
		{
			name:     "Multi-byte short",
			input:    "Hello 🌍",
			expected: "Hello_🌍",
		},
		{
			name: "Multi-byte long truncation (invalid split)",
			// 49 'a's (49 bytes) + '€' (3 bytes) = 52 bytes
			// cutting at 50 will leave 49 'a's + 1 byte of '€' -> invalid utf8
			input: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa€",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeName(tt.input)

			// Check if valid UTF-8
			if !utf8.ValidString(got) {
				t.Errorf("sanitizeName(%q) returned invalid UTF-8 string", tt.input)
			}

			if len(got) > 50 {
				t.Errorf("sanitizeName(%q) length = %d; want <= 50", tt.input, len(got))
			}

			if tt.expected != "" {
				// For the truncation case, we might need more loose check
				if len(tt.input) <= 50 && got != tt.expected {
					t.Errorf("sanitizeName(%q) = %q; want %q", tt.input, got, tt.expected)
				}
			}
		})
	}
}

func TestGenerateIDProducesValidCheckpointIDForSpecialNames(t *testing.T) {
	t.Parallel()
	names := []string{
		"fix #42 (urgent)",
		"don't break .. traversal",
		"Hello 🌍 世界",
		"///...###",
		"",
	}
	for _, name := range names {
		id := GenerateID(name)
		if err := validateCheckpointID(id); err != nil {
			t.Errorf("GenerateID(%q) = %q failed validateCheckpointID: %v", name, id, err)
		}
	}
}

package cli

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func TestHighlightMatch(t *testing.T) {

	th := theme.Current()

	tests := []struct {
		name            string
		line            string
		pattern         string
		caseInsensitive bool
		wantContains    string
	}{
		{"simple match", "hello world", "world", false, "world"},
		{"no match", "hello world", "xyz", false, "hello world"},
		{"case sensitive no match", "Hello World", "hello", false, "Hello World"},
		{"case insensitive match", "Hello World", "hello", true, "Hello"},
		{"regex pattern", "file123.go", `file\d+`, false, "file123"},
		{"invalid regex falls back to literal", "a(b", "(", false, "("},
		{"multiple matches", "abcabc", "abc", false, "abc"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := highlightMatch(tc.line, tc.pattern, tc.caseInsensitive, th)
			if !strings.Contains(got, tc.wantContains) {
				t.Errorf("highlightMatch(%q, %q, %v) = %q, want to contain %q",
					tc.line, tc.pattern, tc.caseInsensitive, got, tc.wantContains)
			}
		})
	}

	t.Run("highlighted text has ANSI codes", func(t *testing.T) {
		got := highlightMatch("hello world", "world", false, th)
		if got == "hello world" {
			t.Error("highlighted text should differ from original when match exists")
		}
		if !strings.Contains(got, "\033[") {
			t.Error("highlighted text should contain ANSI escape codes")
		}
	})
}

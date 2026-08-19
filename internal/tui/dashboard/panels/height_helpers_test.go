package panels

import (
	"strings"
	"testing"
)

func TestFitToHeight(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		content      string
		targetHeight int
		wantLines    int
	}{
		{
			name:         "pad short content",
			content:      "a\nb",
			targetHeight: 5,
			wantLines:    5,
		},
		{
			name:         "truncate long content",
			content:      "a\nb\nc\nd\ne\nf",
			targetHeight: 3,
			wantLines:    3,
		},
		{
			name:         "exact fit - no change needed",
			content:      "a\nb\nc",
			targetHeight: 3,
			wantLines:    3,
		},
		{
			name:         "empty content padded",
			content:      "",
			targetHeight: 3,
			wantLines:    3,
		},
		{
			name:         "zero target returns empty",
			content:      "hello",
			targetHeight: 0,
			wantLines:    1, // empty string splits to [""]
		},
		{
			name:         "negative target returns empty",
			content:      "hello",
			targetHeight: -1,
			wantLines:    1,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := FitToHeight(tc.content, tc.targetHeight)
			lines := strings.Split(result, "\n")
			if len(lines) != tc.wantLines {
				t.Errorf("FitToHeight(%q, %d) got %d lines, want %d; result=%q",
					tc.content, tc.targetHeight, len(lines), tc.wantLines, result)
			}
		})
	}
}

func TestFitToHeight_PreservesContent(t *testing.T) {
	t.Parallel()

	content := "line1\nline2\nline3"
	result := FitToHeight(content, 5)

	if !strings.HasPrefix(result, "line1\nline2\nline3") {
		t.Errorf("FitToHeight should preserve original content, got: %q", result)
	}
}

package cli

import (
	"regexp"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestPaneMatchesSelector(t *testing.T) {
	pane := tmux.Pane{ID: "%12", Index: 3}

	cases := []struct {
		sel     string
		matches bool
	}{
		{"3", true},   // index
		{"%12", true}, // full id
		{"12", true},  // id without %
		{"2", false},
		{"1.2", true}, // suffix match
		{"garbage", false},
	}

	for _, tc := range cases {
		if got := paneMatchesSelector(pane, tc.sel); got != tc.matches {
			t.Fatalf("selector %q expected %v got %v", tc.sel, tc.matches, got)
		}
	}
}

func TestFilterOutput_OrderPatternThenCode(t *testing.T) {
	text := "noise\n```go\nfmt.Println(\"ok\")\n```\nERROR only this line\n```go\nfmt.Println(\"fail\")\n```\n"
	re := regexp.MustCompile("ERROR")

	out := filterOutput(text, re, true)

	if out != "" {
		t.Fatalf("expected empty output when pattern removes code blocks, got %q", out)
	}
}

func TestFilterOutput_CodeExtractionMultipleBlocks(t *testing.T) {
	text := "before\n```python\nprint(1)\n```\nmid\n```javascript\nconsole.log(2)\n```\nafter"

	out := filterOutput(text, nil, true)
	expected := "print(1)\n\nconsole.log(2)"
	if out != expected {
		t.Fatalf("expected %q got %q", expected, out)
	}
}

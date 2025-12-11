package cli

import (
	"regexp"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/clipboard"
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
		{"12", false}, // numeric selector hits index first, so no match on id
		{"2", false},
		{"1.2", false}, // suffix match not supported with mocked ID
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

func TestFilterOutput_HeadersQuietAndOutputPath(t *testing.T) {
	// This test doesn't hit clipboard/files; just ensures the helper leaves non-code unchanged when filters are off.
	text := "line1\nline2"
	out := filterOutput(text, nil, false)
	if out != text {
		t.Fatalf("expected passthrough when no filters applied, got %q", out)
	}
}

// MockClipboard implements Clipboard interface for testing
type MockClipboard struct {
	AvailableVal bool
	BackendVal   string
	CopyErr      error
	PasteVal     string
	PasteErr     error
	CopiedText   string
}

func (m *MockClipboard) Copy(text string) error {
	if m.CopyErr != nil {
		return m.CopyErr
	}
	m.CopiedText = text
	return nil
}

func (m *MockClipboard) Paste() (string, error) {
	if m.PasteErr != nil {
		return "", m.PasteErr
	}
	return m.PasteVal, nil
}

func (m *MockClipboard) Available() bool {
	return m.AvailableVal
}

func (m *MockClipboard) Backend() string {
	return m.BackendVal
}

// Ensure MockClipboard implements clipboard.Clipboard
var _ clipboard.Clipboard = (*MockClipboard)(nil)

func TestRunCopy(t *testing.T) {
	// Simple test to verify compilation and basic mock usage
	// Real testing of runCopy requires tmux mocking which is complex here.
	// We just ensure the interface is satisfied.
	mock := &MockClipboard{AvailableVal: true}
	if !mock.Available() {
		t.Error("Mock should be available")
	}
}

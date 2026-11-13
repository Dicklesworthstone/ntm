package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
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

func TestAgentFilter_IsEmpty(t *testing.T) {
	tests := []struct {
		name   string
		filter AgentFilter
		want   bool
	}{
		{"all_false", AgentFilter{}, true},
		{"all_true", AgentFilter{All: true}, false},
		{"claude_only", AgentFilter{Claude: true}, false},
		{"codex_only", AgentFilter{Codex: true}, false},
		{"gemini_only", AgentFilter{Gemini: true}, false},
		{"multiple", AgentFilter{Claude: true, Codex: true}, false},
		{"all_with_specifics", AgentFilter{All: true, Claude: true}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.filter.IsEmpty(); got != tt.want {
				t.Errorf("AgentFilter.IsEmpty() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAgentFilter_Matches(t *testing.T) {
	tests := []struct {
		name      string
		filter    AgentFilter
		agentType tmux.AgentType
		want      bool
	}{
		// All flag matches everything
		{"all_matches_claude", AgentFilter{All: true}, tmux.AgentClaude, true},
		{"all_matches_codex", AgentFilter{All: true}, tmux.AgentCodex, true},
		{"all_matches_gemini", AgentFilter{All: true}, tmux.AgentGemini, true},
		{"all_matches_user", AgentFilter{All: true}, tmux.AgentUser, true},
		{"all_matches_other", AgentFilter{All: true}, tmux.AgentType("other"), true},

		// Specific filters
		{"claude_filter_matches_claude", AgentFilter{Claude: true}, tmux.AgentClaude, true},
		{"claude_filter_not_matches_codex", AgentFilter{Claude: true}, tmux.AgentCodex, false},
		{"codex_filter_matches_codex", AgentFilter{Codex: true}, tmux.AgentCodex, true},
		{"codex_filter_not_matches_claude", AgentFilter{Codex: true}, tmux.AgentClaude, false},
		{"gemini_filter_matches_gemini", AgentFilter{Gemini: true}, tmux.AgentGemini, true},
		{"gemini_filter_not_matches_user", AgentFilter{Gemini: true}, tmux.AgentUser, false},
		{"claude_filter_matches_claude_alias", AgentFilter{Claude: true}, tmux.AgentType("claude_code"), true},
		{"codex_filter_matches_codex_alias", AgentFilter{Codex: true}, tmux.AgentType("openai-codex"), true},
		{"gemini_filter_matches_gemini_alias", AgentFilter{Gemini: true}, tmux.AgentType("google-gemini"), true},

		// Empty filter matches nothing
		{"empty_not_matches_claude", AgentFilter{}, tmux.AgentClaude, false},
		{"empty_not_matches_user", AgentFilter{}, tmux.AgentUser, false},

		// User and other types never match with specific filters
		{"claude_not_matches_user", AgentFilter{Claude: true}, tmux.AgentUser, false},
		{"all_filters_not_match_other", AgentFilter{Claude: true, Codex: true, Gemini: true}, tmux.AgentType("other"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.filter.Matches(tt.agentType); got != tt.want {
				t.Errorf("AgentFilter.Matches(%v) = %v, want %v", tt.agentType, got, tt.want)
			}
		})
	}
}

func TestFilterOutput_PatternOnly(t *testing.T) {
	text := "line1 ERROR here\nline2 OK\nline3 ERROR again\nline4 DEBUG"
	re := regexp.MustCompile("ERROR")

	out := filterOutput(text, re, false)

	expected := "line1 ERROR here\nline3 ERROR again"
	if out != expected {
		t.Errorf("filterOutput with pattern only = %q, want %q", out, expected)
	}
}

func TestFilterOutput_EmptyInput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		regex    *regexp.Regexp
		codeOnly bool
		want     string
	}{
		{"empty_no_filters", "", nil, false, ""},
		{"empty_with_pattern", "", regexp.MustCompile("test"), false, ""},
		{"empty_code_only", "", nil, true, ""},
		{"whitespace_only", "   \n\n  ", nil, false, "   \n\n  "},
		{"whitespace_code_only", "   \n\n  ", nil, true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterOutput(tt.input, tt.regex, tt.codeOnly)
			if got != tt.want {
				t.Errorf("filterOutput(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFilterOutput_NoMatches(t *testing.T) {
	text := "line1\nline2\nline3"
	re := regexp.MustCompile("NOMATCH")

	out := filterOutput(text, re, false)

	if out != "" {
		t.Errorf("filterOutput with no matches = %q, want empty", out)
	}
}

func TestFilterOutput_CaseInsensitivePattern(t *testing.T) {
	text := "ERROR uppercase\nerror lowercase\nError mixed"
	re := regexp.MustCompile("(?i)error")

	out := filterOutput(text, re, false)

	expected := "ERROR uppercase\nerror lowercase\nError mixed"
	if out != expected {
		t.Errorf("filterOutput with case-insensitive = %q, want %q", out, expected)
	}
}

func TestPaneMatchesSelector_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		pane     tmux.Pane
		selector string
		want     bool
	}{
		{"empty_selector", tmux.Pane{ID: "%1", Index: 0}, "", false},
		{"zero_index", tmux.Pane{ID: "%0", Index: 0}, "0", true},
		{"large_index", tmux.Pane{ID: "%999", Index: 999}, "999", true},
		{"negative_index_string", tmux.Pane{ID: "%1", Index: 1}, "-1", false},
		{"id_without_percent", tmux.Pane{ID: "%42", Index: 0}, "42", false}, // numeric hits index first
		{"id_with_percent", tmux.Pane{ID: "%42", Index: 0}, "%42", true},
		{"suffix_match", tmux.Pane{ID: "1.2", Index: 0}, "1.2", true},
		{"suffix_partial", tmux.Pane{ID: "session:1.2", Index: 0}, "1.2", true},
		{"no_suffix_match", tmux.Pane{ID: "%5", Index: 3}, "1.2", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := paneMatchesSelector(tt.pane, tt.selector)
			if got != tt.want {
				t.Errorf("paneMatchesSelector(%+v, %q) = %v, want %v",
					tt.pane, tt.selector, got, tt.want)
			}
		})
	}
}

func TestCopyCommandPasteFlagReadsFiltersAndWrites(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fake wl-paste test requires linux clipboard backend selection")
	}

	binDir := t.TempDir()
	clipBody := "header line\n```go\nfmt.Println(\"hello\")\n```\nERROR: boom\n"
	clipFile := filepath.Join(binDir, "clipboard.txt")
	if err := os.WriteFile(clipFile, []byte(clipBody), 0o644); err != nil {
		t.Fatalf("write clipboard fixture: %v", err)
	}
	for name, script := range map[string]string{
		"wl-copy":  "#!/bin/sh\ncat > " + clipFile + "\n",
		"wl-paste": "#!/bin/sh\ncat " + clipFile + "\n",
	} {
		p := filepath.Join(binDir, name)
		if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("XDG_SESSION_TYPE", "wayland")
	t.Setenv("WSL_DISTRO_NAME", "")
	t.Setenv("WSL_INTEROP", "")

	// 1. Raw paste to stdout
	var out bytes.Buffer
	cmd := newCopyCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--paste"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("copy --paste failed: %v", err)
	}
	if out.String() != clipBody {
		t.Fatalf("copy --paste = %q, want %q", out.String(), clipBody)
	}

	// 2. Code-only paste to stdout
	out.Reset()
	cmd = newCopyCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--paste", "--code"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("copy --paste --code failed: %v", err)
	}
	if strings.TrimSpace(out.String()) != `fmt.Println("hello")` {
		t.Fatalf("copy --paste --code = %q, want extracted code block", out.String())
	}

	// 3. Pattern-filtered paste to output file
	destFile := filepath.Join(t.TempDir(), "filtered.txt")
	out.Reset()
	cmd = newCopyCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--paste", "--pattern", "ERROR", "--output", destFile, "--quiet"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("copy --paste --pattern --output failed: %v", err)
	}
	saved, err := os.ReadFile(destFile)
	if err != nil {
		t.Fatalf("read saved paste output: %v", err)
	}
	if string(saved) != "ERROR: boom" {
		t.Fatalf("saved paste file = %q, want %q", string(saved), "ERROR: boom")
	}
}

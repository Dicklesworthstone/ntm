package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// TestSendRealSession tests sending a prompt to a real tmux session
func TestSendRealSession(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Setup temp dir for projects
	tmpDir, err := os.MkdirTemp("", "ntm-test-send")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Save/Restore global config
	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = config.Default()
	cfg.ProjectsBase = tmpDir
	jsonOutput = true // Use JSON output to avoid polluting test logs

	// Use a simple echo command that persists for a bit so we can capture it
	// We use 'read' to keep the pane open/active if needed, or just sleep
	cfg.Agents.Claude = "cat" // Simple cat will echo whatever we send to stdin/tty?
	// Actually, SendKeys sends keystrokes. "cat" will print them back. Perfect.

	sessionName := fmt.Sprintf("ntm-test-send-%d", time.Now().UnixNano())
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	// Define agents
	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1, Model: "test-model"},
	}

	// Create project dir
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Spawn session
	opts := SpawnOptions{
		Session:  sessionName,
		Agents:   agents,
		CCCount:  1,
		UserPane: true,
	}
	err = spawnSessionLogic(opts)
	if err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	// Wait for session to settle
	time.Sleep(500 * time.Millisecond)

	// Send a prompt
	prompt := "Hello NTM Test"
	targets := SendTargets{} // Empty targets = default behavior (all agents)

	// Send to all agents (skip user pane default)
	err = runSendWithTargets(SendOptions{
		Session:   sessionName,
		Prompt:    prompt,
		Targets:   targets,
		TargetAll: true,
		SkipFirst: false,
		PaneIndex: -1,
	})
	if err != nil {
		t.Fatalf("runSendWithTargets failed: %v", err)
	}

	// Wait for keys to be processed by tmux/shell
	time.Sleep(500 * time.Millisecond)

	// Verify output in pane
	// We spawned 1 Claude agent, so it should be at index 1 (index 0 is user)
	// We need to find the pane ID or just use index
	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("failed to get panes: %v", err)
	}

	var agentPane *tmux.Pane
	for i := range panes {
		if panes[i].Type == tmux.AgentClaude {
			agentPane = &panes[i]
			break
		}
	}

	if agentPane == nil {
		t.Fatal("Agent pane not found")
	}

	output, err := tmux.CapturePaneOutput(agentPane.ID, 10)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	if !strings.Contains(output, prompt) {
		t.Errorf("Pane output did not contain prompt %q. Got:\n%s", prompt, output)
	}
}

// TestGetPromptContentFromArgs tests reading prompt from positional arguments
func TestGetPromptContentFromArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		prefix    string
		suffix    string
		want      string
		wantError bool
	}{
		{
			name: "single arg",
			args: []string{"hello world"},
			want: "hello world",
		},
		{
			name: "multiple args joined",
			args: []string{"hello", "world"},
			want: "hello world",
		},
		{
			name:      "no args error",
			args:      []string{},
			wantError: true,
		},
		{
			name:   "prefix/suffix ignored for args",
			args:   []string{"hello"},
			prefix: "PREFIX",
			suffix: "SUFFIX",
			want:   "hello", // prefix/suffix don't apply to args
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getPromptContent(tt.args, "", tt.prefix, tt.suffix)
			if tt.wantError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestGetPromptContentFromFile tests reading prompt from a file
func TestGetPromptContentFromFile(t *testing.T) {
	// Create a temp file with content
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "prompt.txt")
	content := "This is the prompt content"
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Create empty file for error test
	emptyFile := filepath.Join(tmpDir, "empty.txt")
	if err := os.WriteFile(emptyFile, []byte(""), 0644); err != nil {
		t.Fatalf("Failed to write empty file: %v", err)
	}

	tests := []struct {
		name       string
		promptFile string
		prefix     string
		suffix     string
		want       string
		wantError  bool
	}{
		{
			name:       "file content",
			promptFile: testFile,
			want:       content,
		},
		{
			name:       "file with prefix",
			promptFile: testFile,
			prefix:     "PREFIX:",
			want:       "PREFIX:\n" + content,
		},
		{
			name:       "file with suffix",
			promptFile: testFile,
			suffix:     ":SUFFIX",
			want:       content + "\n:SUFFIX",
		},
		{
			name:       "file with prefix and suffix",
			promptFile: testFile,
			prefix:     "START",
			suffix:     "END",
			want:       "START\n" + content + "\nEND",
		},
		{
			name:       "nonexistent file error",
			promptFile: "/nonexistent/path/file.txt",
			wantError:  true,
		},
		{
			name:       "empty file error",
			promptFile: emptyFile,
			wantError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getPromptContent([]string{}, tt.promptFile, tt.prefix, tt.suffix)
			if tt.wantError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestBuildPrompt tests the buildPrompt helper function
func TestBuildPrompt(t *testing.T) {
	tests := []struct {
		name    string
		content string
		prefix  string
		suffix  string
		want    string
	}{
		{
			name:    "content only",
			content: "hello",
			want:    "hello",
		},
		{
			name:    "with prefix",
			content: "hello",
			prefix:  "PREFIX:",
			want:    "PREFIX:\nhello",
		},
		{
			name:    "with suffix",
			content: "hello",
			suffix:  ":SUFFIX",
			want:    "hello\n:SUFFIX",
		},
		{
			name:    "with both",
			content: "hello",
			prefix:  "START",
			suffix:  "END",
			want:    "START\nhello\nEND",
		},
		{
			name:    "content with whitespace trimmed",
			content: "  hello  \n",
			want:    "hello",
		},
		{
			name:    "multiline content",
			content: "line1\nline2\nline3",
			prefix:  "BEGIN",
			suffix:  "DONE",
			want:    "BEGIN\nline1\nline2\nline3\nDONE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildPrompt(tt.content, tt.prefix, tt.suffix)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestTruncatePrompt tests the truncatePrompt helper
func TestTruncatePrompt(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a longer prompt", 10, "this is..."},
		{"", 10, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "..."},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := truncatePrompt(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncatePrompt(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestExtractLikelyCommand(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   string
		wantOK bool
	}{
		{
			name:   "simple git command",
			input:  "git status",
			want:   "git status",
			wantOK: true,
		},
		{
			name:   "prefixed shell prompt",
			input:  "  $ rm -rf /tmp",
			want:   "rm -rf /tmp",
			wantOK: true,
		},
		{
			name:   "command in fenced block",
			input:  "```bash\nrm -rf /var/tmp\n```",
			want:   "rm -rf /var/tmp",
			wantOK: true,
		},
		{
			name:   "flag-only heuristic",
			input:  "deploy --force",
			want:   "deploy --force",
			wantOK: true,
		},
		{
			name:   "non-command text",
			input:  "please review the changes",
			want:   "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := extractLikelyCommand(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("extractLikelyCommand ok=%v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Fatalf("extractLikelyCommand = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLooksLikeShellCommand(t *testing.T) {
	tests := []struct {
		line   string
		expect bool
	}{
		{"git status", true},
		{"sudo rm -rf /", true},
		{"echo hello", false},
		{"foo && bar", true},
		{"use --force when needed", true},
		{"just some words", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := looksLikeShellCommand(tt.line)
			if got != tt.expect {
				t.Fatalf("looksLikeShellCommand(%q) = %v, want %v", tt.line, got, tt.expect)
			}
		})
	}
}

// TestSendFlagNoOptDefVal verifies that --cc/--cod/--gmi flags work without consuming
// the next positional argument as the flag value. This tests the NoOptDefVal fix.
// Before the fix: "ntm send session --cod hello" would fail because "hello" was consumed by --cod
// After the fix: "ntm send session --cod hello" correctly parses "hello" as the prompt
func TestSendFlagNoOptDefVal(t *testing.T) {
	cmd := newSendCmd()

	tests := []struct {
		name     string
		args     []string
		wantErr  bool
		checkMsg string
	}{
		{
			name:     "cod flag before prompt",
			args:     []string{"testsession", "--cod", "hello world"},
			wantErr:  false, // Should NOT error - prompt should be parsed correctly
			checkMsg: "flag before prompt should work with NoOptDefVal",
		},
		{
			name:     "cc flag before prompt",
			args:     []string{"testsession", "--cc", "test prompt"},
			wantErr:  false,
			checkMsg: "cc flag before prompt should work",
		},
		{
			name:     "gmi flag before prompt",
			args:     []string{"testsession", "--gmi", "another prompt"},
			wantErr:  false,
			checkMsg: "gmi flag before prompt should work",
		},
		{
			name:     "multiple flags before prompt",
			args:     []string{"testsession", "--cc", "--cod", "multi agent prompt"},
			wantErr:  false,
			checkMsg: "multiple flags before prompt should work",
		},
		{
			name:     "flag with variant value",
			args:     []string{"testsession", "--cc=opus", "prompt with variant"},
			wantErr:  false,
			checkMsg: "flag with explicit variant should work",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fresh command for each test
			testCmd := newSendCmd()
			testCmd.SetArgs(tt.args)

			// Just parse flags - don't execute (would need tmux)
			err := testCmd.ParseFlags(tt.args)
			if tt.wantErr && err == nil {
				t.Errorf("%s: expected error but got nil", tt.checkMsg)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("%s: unexpected error: %v", tt.checkMsg, err)
			}

			// Verify the prompt wasn't consumed by the flag
			// After parsing flags, remaining args should contain the prompt
			remainingArgs := testCmd.Flags().Args()
			if !tt.wantErr && len(remainingArgs) < 2 {
				t.Errorf("%s: expected prompt in remaining args, got: %v", tt.checkMsg, remainingArgs)
			}
		})
	}

	_ = cmd // silence unused warning
}

// TestParseBatchFile tests the batch file parser
func TestParseBatchFile(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name      string
		content   string
		want      []string
		wantError bool
	}{
		{
			name:    "simple one per line",
			content: "prompt one\nprompt two\nprompt three",
			want:    []string{"prompt one", "prompt two", "prompt three"},
		},
		{
			name:    "with comments",
			content: "# This is a comment\nprompt one\n# Another comment\nprompt two",
			want:    []string{"prompt one", "prompt two"},
		},
		{
			name:    "with empty lines",
			content: "prompt one\n\n\nprompt two\n\n",
			want:    []string{"prompt one", "prompt two"},
		},
		{
			name:    "separator format",
			content: "First prompt\nwith multiple lines\n---\nSecond prompt",
			want:    []string{"First prompt\nwith multiple lines", "Second prompt"},
		},
		{
			name:    "separator with comments",
			content: "# Header comment\nFirst prompt\n---\n# Comment in second\nSecond prompt",
			want:    []string{"First prompt", "Second prompt"},
		},
		{
			name:    "leading separator",
			content: "---\nFirst prompt\n---\nSecond prompt",
			want:    []string{"First prompt", "Second prompt"},
		},
		{
			name:      "empty file",
			content:   "",
			wantError: true,
		},
		{
			name:      "only whitespace",
			content:   "   \n\n   ",
			wantError: true,
		},
		{
			name:      "only comments",
			content:   "# comment 1\n# comment 2",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp file with content
			testFile := filepath.Join(tmpDir, fmt.Sprintf("batch_%s.txt", tt.name))
			if err := os.WriteFile(testFile, []byte(tt.content), 0644); err != nil {
				t.Fatalf("Failed to write test file: %v", err)
			}

			got, err := parseBatchFile(testFile)
			if tt.wantError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d prompts, want %d: %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("prompt %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}

	// Test nonexistent file
	t.Run("nonexistent file", func(t *testing.T) {
		_, err := parseBatchFile("/nonexistent/path/file.txt")
		if err == nil {
			t.Error("Expected error for nonexistent file")
		}
	})
}

// TestRemoveComments tests the comment removal helper
func TestRemoveComments(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"no comments", "no comments"},
		{"# full line comment", ""},
		{"text\n# comment\nmore text", "text\nmore text"},
		{"  # indented comment", ""},
		{"text # not a comment", "text # not a comment"},
		{"line1\nline2\nline3", "line1\nline2\nline3"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := removeComments(tt.input)
			if got != tt.want {
				t.Errorf("removeComments(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestTruncateForPreview tests the preview truncation helper
func TestTruncateForPreview(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly ten", 11, "exactly ten"},
		{"this is a longer string", 10, "this is..."},
		{"", 10, ""},
		{"multi\nline\ntext", 20, "multi line text"},
		{"  whitespace  ", 15, "whitespace"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := truncateForPreview(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateForPreview(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

// TestBuildTargetDescription tests the target description builder
func TestBuildTargetDescription(t *testing.T) {
	tests := []struct {
		name      string
		cc        bool
		cod       bool
		gmi       bool
		all       bool
		skipFirst bool
		paneIdx   int
		want      string
	}{
		{"specific pane", false, false, false, false, false, 2, "pane:2"},
		{"all panes", false, false, false, true, false, -1, "all"},
		{"claude only", true, false, false, false, false, -1, "cc"},
		{"codex only", false, true, false, false, false, -1, "cod"},
		{"gemini only", false, false, true, false, false, -1, "gmi"},
		{"cc and cod", true, true, false, false, false, -1, "cc,cod"},
		{"all types", true, true, true, false, false, -1, "cc,cod,gmi"},
		{"no filter skip first", false, false, false, false, true, -1, "agents"},
		{"no filter no skip", false, false, false, false, false, -1, "all-agents"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildTargetDescription(tt.cc, tt.cod, tt.gmi, tt.all, tt.skipFirst, tt.paneIdx, nil)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

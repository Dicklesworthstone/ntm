package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	jsonOutput = true // Use JSON output to avoid polluting test logs

	// Use /bin/cat explicitly to avoid shell aliases (e.g., cat -> bat) which
	// have different input/output behavior and can cause test flakiness.
	cfg.Agents.Claude = testAgentBinCatCommandTemplate

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
	err = os.MkdirAll(projectDir, 0755)
	if err != nil {
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

	// Wait for session to settle - needs enough time for:
	// 1. Shell to initialize (load zshrc, plugins, etc.)
	// 2. The cd && agent command to be processed
	// 3. Agent (cat) to be ready to receive input
	time.Sleep(1500 * time.Millisecond)

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

	// Redaction: redact mode should replace sensitive substrings before sending to panes.
	cfg.Redaction.Mode = "redact"
	redactSecret := "prefix password=hunter2hunter2 suffix"
	if err := runSendWithTargets(SendOptions{
		Session:   sessionName,
		Prompt:    redactSecret,
		Targets:   SendTargets{},
		TargetAll: true,
		SkipFirst: false,
		PaneIndex: -1,
	}); err != nil {
		t.Fatalf("runSendWithTargets (redact) failed: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	redactedOutput, err := tmux.CapturePaneOutput(agentPane.ID, 20)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}
	if strings.Contains(redactedOutput, "hunter2hunter2") {
		t.Fatalf("expected secret to be redacted in pane output, got:\n%s", redactedOutput)
	}
	if !strings.Contains(redactedOutput, "[REDACTED:PASSWORD:") {
		t.Fatalf("expected redaction placeholder in pane output, got:\n%s", redactedOutput)
	}

	// Redaction: block mode should abort send (and not leak secrets to panes).
	cfg.Redaction.Mode = "block"
	blockSecret := "prefix password=blocksecretblocksecret suffix"
	err = runSendWithTargets(SendOptions{
		Session:   sessionName,
		Prompt:    blockSecret,
		Targets:   SendTargets{},
		TargetAll: true,
		SkipFirst: false,
		PaneIndex: -1,
	})
	if err == nil {
		t.Fatalf("expected error in block mode")
	}
	var blocked redactionBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("expected redactionBlockedError, got %T: %v", err, err)
	}

	time.Sleep(500 * time.Millisecond)
	blockedOutput, err := tmux.CapturePaneOutput(agentPane.ID, 20)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}
	if strings.Contains(blockedOutput, "blocksecretblocksecret") {
		t.Fatalf("expected secret not to appear in pane output when blocked, got:\n%s", blockedOutput)
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
		wantSrc   string
		wantError bool
	}{
		{
			name:    "single arg",
			args:    []string{"hello world"},
			want:    "hello world",
			wantSrc: "args",
		},
		{
			name:    "multiple args joined",
			args:    []string{"hello", "world"},
			want:    "hello world",
			wantSrc: "args",
		},
		{
			name:      "no args error",
			args:      []string{},
			wantError: true,
		},
		{
			name:    "prefix/suffix ignored for args",
			args:    []string{"hello"},
			prefix:  "PREFIX",
			suffix:  "SUFFIX",
			want:    "hello", // prefix/suffix don't apply to args
			wantSrc: "args",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotSrc, err := getPromptContent(tt.args, "", tt.prefix, tt.suffix)
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
			if tt.wantSrc != "" && gotSrc != tt.wantSrc {
				t.Errorf("source: got %q, want %q", gotSrc, tt.wantSrc)
			}
		})
	}
}

func TestGetSessionWorkingDirRejectsWorkspaceFallbackForExplicitSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := t.TempDir()
	cfg = &config.Config{ProjectsBase: projectsBase}

	cwdRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatal(err)
	}

	if got := getSessionWorkingDir("ntm", false); got != "" {
		t.Fatalf("getSessionWorkingDir explicit = %q, want empty", got)
	}
}

func TestGetSessionWorkingDirAllowsWorkspaceFallbackForInferredSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := t.TempDir()
	cfg = &config.Config{ProjectsBase: projectsBase}

	cwdRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatal(err)
	}

	if got := getSessionWorkingDir("ntm", true); got != cwdRepo {
		t.Fatalf("getSessionWorkingDir inferred = %q, want %q", got, cwdRepo)
	}
}

func TestResolveSendSessionForCommandNormalizesExplicitPrefix(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	sessionName := fmt.Sprintf("ntm-send-prefix-%d", time.Now().UnixNano())
	prefix := sessionName[:len(sessionName)-4]
	workDir := t.TempDir()

	_ = tmux.KillSession(sessionName)
	if err := tmux.CreateSession(sessionName, workDir); err != nil {
		t.Fatalf("CreateSession(%q): %v", sessionName, err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(sessionName) })

	gotSession, inferred, err := resolveSendSessionForCommand(prefix)
	if err != nil {
		t.Fatalf("resolveSendSessionForCommand() error = %v", err)
	}
	if inferred {
		t.Fatal("expected explicit prefix not to be inferred")
	}
	if gotSession != sessionName {
		t.Fatalf("resolveSendSessionForCommand() session = %q, want %q", gotSession, sessionName)
	}
}

// TestGetPromptContentFromFile tests reading prompt from a file
func TestGetPromptContentFromFile(t *testing.T) {
	// Create a temp file with content
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "prompt.txt")
	content := "This is the prompt content"
	writeErr := os.WriteFile(testFile, []byte(content), 0644)
	if writeErr != nil {
		t.Fatalf("Failed to write test file: %v", writeErr)
	}

	// Create empty file for error test
	emptyFile := filepath.Join(tmpDir, "empty.txt")
	writeErr = os.WriteFile(emptyFile, []byte(""), 0644)
	if writeErr != nil {
		t.Fatalf("Failed to write empty file: %v", writeErr)
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
			got, gotSrc, err := getPromptContent([]string{}, tt.promptFile, tt.prefix, tt.suffix)
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
			wantSrc := "file:" + tt.promptFile
			if gotSrc != wantSrc {
				t.Errorf("source: got %q, want %q", gotSrc, wantSrc)
			}
		})
	}
}

func TestShuffledPermutation_DeterministicSeed(t *testing.T) {

	seed := int64(12345)
	seedUsed1, perm1 := shuffledPermutation(32, seed)
	seedUsed2, perm2 := shuffledPermutation(32, seed)

	if seedUsed1 != seed || seedUsed2 != seed {
		t.Fatalf("expected seedUsed to match provided seed, got %d and %d", seedUsed1, seedUsed2)
	}
	if len(perm1) != len(perm2) {
		t.Fatalf("perm length mismatch: %d vs %d", len(perm1), len(perm2))
	}
	for i := range perm1 {
		if perm1[i] != perm2[i] {
			t.Fatalf("expected identical permutations for same seed, mismatch at %d: %v vs %v", i, perm1, perm2)
		}
	}
}

func TestShuffledPermutation_IsPermutation(t *testing.T) {

	_, perm := shuffledPermutation(100, 999)
	seen := make(map[int]bool, len(perm))
	for _, v := range perm {
		if v < 0 || v >= 100 {
			t.Fatalf("perm contains out-of-range value %d", v)
		}
		if seen[v] {
			t.Fatalf("perm contains duplicate value %d", v)
		}
		seen[v] = true
	}
}

func TestPermutePanes_AppliesPermutation(t *testing.T) {

	panes := []tmux.Pane{
		{Index: 10},
		{Index: 11},
		{Index: 12},
		{Index: 13},
	}
	perm := []int{2, 0, 3, 1}
	out := permutePanes(panes, perm)
	if len(out) != len(panes) {
		t.Fatalf("permutePanes length = %d, want %d", len(out), len(panes))
	}
	got := []int{out[0].Index, out[1].Index, out[2].Index, out[3].Index}
	want := []int{12, 10, 13, 11}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("permutePanes[%d] = %d, want %d (got=%v)", i, got[i], want[i], got)
		}
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

func TestExtractLikelyCommands(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "simple git command",
			input: "git status",
			want:  []string{"git status"},
		},
		{
			name:  "prefixed shell prompt",
			input: "  $ rm -rf /tmp",
			want:  []string{"rm -rf /tmp"},
		},
		{
			name:  "command in fenced block",
			input: "```bash\nrm -rf /var/tmp\n```",
			want:  []string{"rm -rf /var/tmp"},
		},
		{
			name:  "flag-only heuristic",
			input: "deploy --force",
			want:  []string{"deploy --force"},
		},
		{
			name:  "non-command text",
			input: "please review the changes",
			want:  nil,
		},
		{
			name:  "multiple commands",
			input: "git status\nrm -rf /tmp\njust some text",
			want:  []string{"git status", "rm -rf /tmp"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractLikelyCommands(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("extractLikelyCommands got %d commands, want %d: got=%v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("extractLikelyCommands[%d] = %q, want %q", i, got[i], tt.want[i])
				}
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
		wantSrc   []string
		wantError bool
	}{
		{
			name:    "simple one per line",
			content: "prompt one\nprompt two\nprompt three",
			want:    []string{"prompt one", "prompt two", "prompt three"},
			wantSrc: []string{"line:1", "line:2", "line:3"},
		},
		{
			name:    "with comments",
			content: "# This is a comment\nprompt one\n# Another comment\nprompt two",
			want:    []string{"prompt one", "prompt two"},
			wantSrc: []string{"line:2", "line:4"},
		},
		{
			name:    "with empty lines",
			content: "prompt one\n\n\nprompt two\n\n",
			want:    []string{"prompt one", "prompt two"},
			wantSrc: []string{"line:1", "line:4"},
		},
		{
			name:    "separator format",
			content: "First prompt\nwith multiple lines\n---\nSecond prompt",
			want:    []string{"First prompt\nwith multiple lines", "Second prompt"},
			wantSrc: []string{"line:1", "line:4"},
		},
		{
			name:    "separator with comments",
			content: "# Header comment\nFirst prompt\n---\n# Comment in second\nSecond prompt",
			want:    []string{"First prompt", "Second prompt"},
			wantSrc: []string{"line:2", "line:5"},
		},
		{
			name:    "leading separator",
			content: "---\nFirst prompt\n---\nSecond prompt",
			want:    []string{"First prompt", "Second prompt"},
			wantSrc: []string{"line:2", "line:4"},
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
			writeErr := os.WriteFile(testFile, []byte(tt.content), 0644)
			if writeErr != nil {
				t.Fatalf("Failed to write test file: %v", writeErr)
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
				if got[i].Text != tt.want[i] {
					t.Errorf("prompt %d: got %q, want %q", i, got[i].Text, tt.want[i])
				}
				if len(tt.wantSrc) > 0 && got[i].Source != tt.wantSrc[i] {
					t.Errorf("prompt %d source: got %q, want %q", i, got[i].Source, tt.wantSrc[i])
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

func TestSendDryRunDoesNotSendToPane(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	tmpDir, err := os.MkdirTemp("", "ntm-test-send-dry-run")
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

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Checkpoints.Enabled = false
	jsonOutput = true // avoid polluting test logs

	// Use a simple echoing agent so we can detect sends
	cfg.Agents.Claude = testAgentCatCommandTemplate

	sessionName := fmt.Sprintf("ntm-test-send-dry-run-%d", time.Now().UnixNano())
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	projectDir := filepath.Join(tmpDir, sessionName)
	err = os.MkdirAll(projectDir, 0755)
	if err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1, Model: "test-model"},
	}
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

	time.Sleep(500 * time.Millisecond)

	prompt := "NTM_TEST_DRY_RUN_SHOULD_NOT_SEND"
	err = runSendWithTargets(SendOptions{
		Session:      sessionName,
		Prompt:       prompt,
		PromptSource: "args",
		Targets:      SendTargets{}, // default targeting = agent panes
		PaneIndex:    -1,
		DryRun:       true,
	})
	if err != nil {
		t.Fatalf("runSendWithTargets failed: %v", err)
	}

	time.Sleep(300 * time.Millisecond)

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

	output, err := tmux.CapturePaneOutput(agentPane.ID, 30)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}
	if strings.Contains(output, prompt) {
		t.Errorf("Dry-run should not send prompt %q, but it appeared in pane output. Got:\n%s", prompt, output)
	}
}

func TestSendSmartRouteIsDisabledWhenPanesSpecified(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	tmpDir, err := os.MkdirTemp("", "ntm-test-send-smart-route-panes")
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

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Checkpoints.Enabled = false
	jsonOutput = true // avoid polluting test logs
	cfg.Agents.Claude = testAgentCatCommandTemplate

	sessionName := fmt.Sprintf("ntm-test-send-smart-route-panes-%d", time.Now().UnixNano())
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	projectDir := filepath.Join(tmpDir, sessionName)
	err = os.MkdirAll(projectDir, 0755)
	if err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1, Model: "test-model"},
	}
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

	time.Sleep(500 * time.Millisecond)

	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("failed to get panes: %v", err)
	}

	var userPaneID string
	var userPaneIndex int
	for _, p := range panes {
		if p.Type == tmux.AgentUser {
			userPaneID = p.ID
			userPaneIndex = p.Index
		}
	}
	if userPaneID == "" {
		t.Fatal("User pane not found")
	}

	// If smart routing were applied, it would ignore the user pane and select an agent pane.
	// With --panes specified, we expect routing to be skipped and the command to be sent to the explicitly selected pane.
	prompt := "echo $PWD"

	// Capture only the send command's JSON output (stdout) and assert that it targeted the explicitly specified pane.
	// This avoids relying on the user shell actually executing the command.
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe: %v", err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = oldStdout
	}()

	sendErr := runSendWithTargets(SendOptions{
		Session:        sessionName,
		Prompt:         prompt,
		PromptSource:   "args",
		Targets:        SendTargets{},
		PanesSpecified: true,
		Panes:          []int{userPaneIndex},
		SmartRoute:     true,
		PaneIndex:      -1,
	})

	_ = w.Close()
	os.Stdout = oldStdout

	stdoutBytes, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		t.Fatalf("failed reading stdout: %v", readErr)
	}
	if sendErr != nil {
		t.Fatalf("runSendWithTargets failed: %v (stdout=%q)", sendErr, strings.TrimSpace(string(stdoutBytes)))
	}

	var res SendResult
	err = json.Unmarshal(stdoutBytes, &res)
	if err != nil {
		t.Fatalf("failed to parse send JSON: %v (stdout=%q)", err, strings.TrimSpace(string(stdoutBytes)))
	}
	if len(res.Targets) != 1 || res.Targets[0] != userPaneIndex {
		t.Fatalf("expected targets [%d], got %v", userPaneIndex, res.Targets)
	}
	if res.RoutedTo != nil {
		t.Fatalf("expected routed_to to be omitted when --panes is explicitly set, got %+v", *res.RoutedTo)
	}
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

func TestBuildObservedTargetTypes(t *testing.T) {
	panes := []tmux.Pane{
		{Type: tmux.AgentUser},
		{Type: tmux.AgentType("claude_code")},
		{Type: tmux.AgentType("openai-codex")},
		{Type: tmux.AgentCodex},
		{Type: tmux.AgentType("ws")},
		{Type: tmux.AgentOllama},
		{Type: tmux.AgentUnknown},
	}

	got := buildObservedTargetTypes(panes)
	if got != "claude,codex,windsurf,ollama" {
		t.Fatalf("buildObservedTargetTypes() = %q, want %q", got, "claude,codex,windsurf,ollama")
	}
}

func TestFilterPanesForBatch(t *testing.T) {
	// Sample panes for testing
	panes := []tmux.Pane{
		{Index: 0, Type: tmux.AgentUser, Title: "user_0"},
		{Index: 1, Type: tmux.AgentClaude, Title: "cc_1", Tags: []string{"frontend"}},
		{Index: 2, Type: tmux.AgentCodex, Title: "cod_2", Tags: []string{"backend", "api"}},
		{Index: 3, Type: tmux.AgentGemini, Title: "gmi_3", Tags: []string{"docs"}},
		{Index: 4, Type: tmux.AgentClaude, Title: "cc_4", Tags: []string{"backend"}},
	}

	tests := []struct {
		name     string
		opts     SendOptions
		wantLen  int
		wantIdxs []int // expected pane indices in result
	}{
		{
			name:     "no filters excludes user pane",
			opts:     SendOptions{},
			wantLen:  4,
			wantIdxs: []int{1, 2, 3, 4},
		},
		{
			name:     "TargetAll includes everything",
			opts:     SendOptions{TargetAll: true},
			wantLen:  5,
			wantIdxs: []int{0, 1, 2, 3, 4},
		},
		{
			name: "filter by tag frontend",
			opts: SendOptions{
				Tags: []string{"frontend"},
			},
			wantLen:  1,
			wantIdxs: []int{1},
		},
		{
			name: "filter by tag backend (multiple matches)",
			opts: SendOptions{
				Tags: []string{"backend"},
			},
			wantLen:  2,
			wantIdxs: []int{2, 4},
		},
		{
			name: "filter by multiple tags (OR logic)",
			opts: SendOptions{
				Tags: []string{"frontend", "docs"},
			},
			wantLen:  2,
			wantIdxs: []int{1, 3},
		},
		{
			name: "filter by agent type claude",
			opts: SendOptions{
				Targets: SendTargets{{Type: AgentTypeClaude}},
			},
			wantLen:  2,
			wantIdxs: []int{1, 4},
		},
		{
			name: "filter by agent type codex",
			opts: SendOptions{
				Targets: SendTargets{{Type: AgentTypeCodex}},
			},
			wantLen:  1,
			wantIdxs: []int{2},
		},
		{
			name: "filter by agent type gemini",
			opts: SendOptions{
				Targets: SendTargets{{Type: AgentTypeGemini}},
			},
			wantLen:  1,
			wantIdxs: []int{3},
		},
		{
			name: "combined tag and type filter",
			opts: SendOptions{
				Tags:    []string{"backend"},
				Targets: SendTargets{{Type: AgentTypeClaude}},
			},
			wantLen:  1,
			wantIdxs: []int{4}, // cc_4 has backend tag
		},
		{
			name: "filter with no matches",
			opts: SendOptions{
				Tags: []string{"nonexistent"},
			},
			wantLen:  0,
			wantIdxs: []int{},
		},
		{
			name: "multiple agent types",
			opts: SendOptions{
				Targets: SendTargets{
					{Type: AgentTypeClaude},
					{Type: AgentTypeGemini},
				},
			},
			wantLen:  3,
			wantIdxs: []int{1, 3, 4},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterPanesForBatch(panes, tt.opts)

			if len(got) != tt.wantLen {
				t.Errorf("filterPanesForBatch() returned %d panes, want %d", len(got), tt.wantLen)
				return
			}

			for i, idx := range tt.wantIdxs {
				if i >= len(got) {
					t.Errorf("missing pane at position %d", i)
					continue
				}
				if got[i].Index != idx {
					t.Errorf("pane[%d].Index = %d, want %d", i, got[i].Index, idx)
				}
			}
		})
	}
}

func TestFilterPanesForBatchEmpty(t *testing.T) {
	// Test with empty panes slice
	got := filterPanesForBatch([]tmux.Pane{}, SendOptions{})
	if len(got) != 0 {
		t.Errorf("filterPanesForBatch(empty) returned %d panes, want 0", len(got))
	}
}

func TestFilterPanesForBatchAllUser(t *testing.T) {
	// Test with only user panes (should return empty without TargetAll)
	userPanes := []tmux.Pane{
		{Index: 0, Type: tmux.AgentUser},
		{Index: 1, Type: tmux.AgentUser},
	}

	got := filterPanesForBatch(userPanes, SendOptions{})
	if len(got) != 0 {
		t.Errorf("filterPanesForBatch(user panes) returned %d panes, want 0", len(got))
	}

	// With TargetAll, should return all
	got = filterPanesForBatch(userPanes, SendOptions{TargetAll: true})
	if len(got) != 2 {
		t.Errorf("filterPanesForBatch(user panes, TargetAll) returned %d panes, want 2", len(got))
	}
}

// --- Tests for base prompt feature (bd-3ejl) ---

func TestApplyBasePrompt(t *testing.T) {
	tests := []struct {
		name string
		base string
		user string
		want string
	}{
		{
			name: "empty base returns user unchanged",
			base: "",
			user: "do the thing",
			want: "do the thing",
		},
		{
			name: "empty user returns base",
			base: "Always run tests",
			user: "",
			want: "Always run tests",
		},
		{
			name: "both empty returns empty",
			base: "",
			user: "",
			want: "",
		},
		{
			name: "base prepended with separator",
			base: "Follow coding standards",
			user: "Implement feature X",
			want: "Follow coding standards\n\nImplement feature X",
		},
		{
			name: "multiline base",
			base: "Rule 1: run tests\nRule 2: use br",
			user: "Fix the bug",
			want: "Rule 1: run tests\nRule 2: use br\n\nFix the bug",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyBasePrompt(tt.base, tt.user)
			if got != tt.want {
				t.Errorf("applyBasePrompt(%q, %q) = %q, want %q", tt.base, tt.user, got, tt.want)
			}
		})
	}
}

func TestResolveBasePrompt_FlagPriority(t *testing.T) {
	got, err := resolveBasePrompt("from flag", "", "from config", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "from flag" {
		t.Errorf("expected flag value, got %q", got)
	}
}

func TestResolveBasePrompt_FlagFilePriority(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "base.txt")
	if err := os.WriteFile(path, []byte("from flag file\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := resolveBasePrompt("", path, "from config", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "from flag file" {
		t.Errorf("expected flag file contents, got %q", got)
	}
}

func TestResolveBasePrompt_ConfigValue(t *testing.T) {
	got, err := resolveBasePrompt("", "", "from config", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "from config" {
		t.Errorf("expected config value, got %q", got)
	}
}

func TestResolveBasePrompt_ConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "base_config.txt")
	if err := os.WriteFile(path, []byte("  from config file  \n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := resolveBasePrompt("", "", "", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "from config file" {
		t.Errorf("expected trimmed config file contents, got %q", got)
	}
}

func TestResolveBasePrompt_AllEmpty(t *testing.T) {
	got, err := resolveBasePrompt("", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestResolveBasePrompt_MissingFlagFile(t *testing.T) {
	_, err := resolveBasePrompt("", "/nonexistent/base.txt", "", "")
	if err == nil {
		t.Fatal("expected error for missing flag file")
	}
	if !strings.Contains(err.Error(), "--base-prompt-file") {
		t.Errorf("error should mention --base-prompt-file, got: %v", err)
	}
}

func TestResolveBasePrompt_MissingConfigFile(t *testing.T) {
	_, err := resolveBasePrompt("", "", "", "/nonexistent/base.txt")
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
	if !strings.Contains(err.Error(), "send.base_prompt_file") {
		t.Errorf("error should mention config, got: %v", err)
	}
}

// --- bd-2wzs: priority-order tests ---

func TestParsePriorityAnnotation(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{"no annotation", "just text", -1},
		{"priority 0", "# priority: 0", 0},
		{"priority 4", "# priority: 4", 4},
		{"priority 2 with text", "# priority: 2\nDo something", 2},
		{"mixed comments", "# note\n# priority: 1\nwork", 1},
		{"uppercase", "# PRIORITY: 1", 1},
		{"no space after colon", "# priority:3", 3},
		{"out of range", "# priority: 9", -1},
		{"negative", "# priority: -1", -1},
		{"empty value", "# priority: ", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePriorityAnnotation(tt.text)
			if got != tt.want {
				t.Errorf("parsePriorityAnnotation(%q) = %d, want %d", tt.text, got, tt.want)
			}
		})
	}
}

func TestSortBatchByPriority(t *testing.T) {

	t.Run("sorts P0 before P2 before unset", func(t *testing.T) {
		prompts := []BatchPrompt{
			{Text: "unset", Source: "line:1", Priority: -1},
			{Text: "p2", Source: "line:2", Priority: 2},
			{Text: "p0", Source: "line:3", Priority: 0},
		}
		sortBatchByPriority(prompts)
		if prompts[0].Priority != 0 {
			t.Errorf("first should be P0, got P%d", prompts[0].Priority)
		}
		if prompts[1].Priority != 2 {
			t.Errorf("second should be P2, got P%d", prompts[1].Priority)
		}
		if prompts[2].Priority != -1 {
			t.Errorf("third should be unset, got P%d", prompts[2].Priority)
		}
	})

	t.Run("stable sort preserves order within same priority", func(t *testing.T) {
		prompts := []BatchPrompt{
			{Text: "first-p1", Source: "line:1", Priority: 1},
			{Text: "second-p1", Source: "line:2", Priority: 1},
			{Text: "third-p1", Source: "line:3", Priority: 1},
		}
		sortBatchByPriority(prompts)
		if prompts[0].Text != "first-p1" || prompts[1].Text != "second-p1" || prompts[2].Text != "third-p1" {
			t.Errorf("stable sort broken: %s, %s, %s", prompts[0].Text, prompts[1].Text, prompts[2].Text)
		}
	})

	t.Run("all unset preserves order", func(t *testing.T) {
		prompts := []BatchPrompt{
			{Text: "a", Priority: -1},
			{Text: "b", Priority: -1},
			{Text: "c", Priority: -1},
		}
		sortBatchByPriority(prompts)
		if prompts[0].Text != "a" || prompts[1].Text != "b" || prompts[2].Text != "c" {
			t.Errorf("order changed: %s, %s, %s", prompts[0].Text, prompts[1].Text, prompts[2].Text)
		}
	})

	t.Run("single element", func(t *testing.T) {
		prompts := []BatchPrompt{{Text: "only", Priority: 3}}
		sortBatchByPriority(prompts)
		if prompts[0].Text != "only" {
			t.Error("single element sort failed")
		}
	})
}

func TestParseBatchFile_PriorityAnnotations(t *testing.T) {

	t.Run("multi-line format extracts priority", func(t *testing.T) {
		content := "---\n# priority: 0\nCritical fix\n---\n# priority: 2\nMedium task\n---\nNo priority\n"
		dir := t.TempDir()
		path := dir + "/batch.txt"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		prompts, err := parseBatchFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(prompts) != 3 {
			t.Fatalf("expected 3 prompts, got %d", len(prompts))
		}
		if prompts[0].Priority != 0 {
			t.Errorf("prompt 0: want priority 0, got %d", prompts[0].Priority)
		}
		if prompts[1].Priority != 2 {
			t.Errorf("prompt 1: want priority 2, got %d", prompts[1].Priority)
		}
		if prompts[2].Priority != -1 {
			t.Errorf("prompt 2: want priority -1, got %d", prompts[2].Priority)
		}
	})

	t.Run("simple format extracts priority from preceding comment", func(t *testing.T) {
		content := "# priority: 1\nHigh priority task\nRegular task\n# priority: 3\nLow priority task\n"
		dir := t.TempDir()
		path := dir + "/batch.txt"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		prompts, err := parseBatchFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(prompts) != 3 {
			t.Fatalf("expected 3 prompts, got %d", len(prompts))
		}
		if prompts[0].Priority != 1 {
			t.Errorf("prompt 0: want priority 1, got %d", prompts[0].Priority)
		}
		if prompts[1].Priority != -1 {
			t.Errorf("prompt 1: want priority -1 (no annotation), got %d", prompts[1].Priority)
		}
		if prompts[2].Priority != 3 {
			t.Errorf("prompt 2: want priority 3, got %d", prompts[2].Priority)
		}
	})
}

func TestFilterPanesForBatchCanonicalizesAliasTypes(t *testing.T) {

	panes := []tmux.Pane{
		{Index: 0, Type: tmux.AgentUser, Title: "user_0"},
		{Index: 1, Type: tmux.AgentType("openai-codex"), Title: "cod_1"},
		{Index: 2, Type: tmux.AgentType("google-gemini"), Title: "gmi_2"},
		{Index: 3, Type: tmux.AgentType("claude_code"), Title: "cc_3"},
	}

	got := filterPanesForBatch(panes, SendOptions{Targets: SendTargets{{Type: AgentTypeCodex}, {Type: AgentTypeGemini}}})
	if len(got) != 2 {
		t.Fatalf("filterPanesForBatch(alias types) len = %d, want 2", len(got))
	}
	if got[0].Index != 1 || got[1].Index != 2 {
		t.Fatalf("filterPanesForBatch(alias types) = %+v, want pane indices [1 2]", got)
	}
}

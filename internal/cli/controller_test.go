package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// ---------------------------------------------------------------------------
// resolveControllerPrompt – template variable substitution
// ---------------------------------------------------------------------------

func TestResolveControllerPrompt_DefaultTemplate(t *testing.T) {
	opts := ControllerInput{Session: "myproject"}
	agentList := "- Pane 1: cc\n- Pane 2: cod"
	projectDir := "/home/user/projects/myproject"

	content, source, err := resolveControllerPrompt(opts, "myproject", agentList, projectDir)
	if err != nil {
		t.Fatalf("resolveControllerPrompt returned error: %v", err)
	}

	if source != "default" {
		t.Errorf("expected source 'default', got %q", source)
	}

	// Verify session name is substituted
	if !strings.Contains(content, "myproject") {
		t.Error("prompt should contain session name 'myproject'")
	}

	// Verify agent list is substituted
	if !strings.Contains(content, "- Pane 1: cc") {
		t.Error("prompt should contain agent list")
	}
	if !strings.Contains(content, "- Pane 2: cod") {
		t.Error("prompt should contain second agent entry")
	}

	// Verify the prompt mentions coordination commands with session name
	if !strings.Contains(content, "ntm status myproject") {
		t.Error("prompt should contain 'ntm status myproject'")
	}
	if !strings.Contains(content, "ntm view myproject") {
		t.Error("prompt should contain 'ntm view myproject'")
	}
}

func TestResolveControllerPrompt_EmptyAgentList(t *testing.T) {
	opts := ControllerInput{}
	content, source, err := resolveControllerPrompt(opts, "empty-session", "", "/tmp/proj")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if source != "default" {
		t.Errorf("expected source 'default', got %q", source)
	}
	if !strings.Contains(content, "empty-session") {
		t.Error("prompt should contain session name")
	}
}

func TestResolveControllerPrompt_CustomPromptFile(t *testing.T) {
	tmpDir := t.TempDir()
	promptPath := filepath.Join(tmpDir, "custom_prompt.txt")

	customContent := `Controller for {{.Session}} in {{.ProjectDir}}.
Agents:
{{.AgentList}}
Done.`
	if err := os.WriteFile(promptPath, []byte(customContent), 0644); err != nil {
		t.Fatalf("failed to write prompt file: %v", err)
	}

	opts := ControllerInput{
		Session:    "test-session",
		PromptFile: promptPath,
	}

	content, source, err := resolveControllerPrompt(opts, "test-session", "- Pane 3: gmi", "/data/projects/test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if source != "custom_prompt.txt" {
		t.Errorf("expected source 'custom_prompt.txt', got %q", source)
	}

	if !strings.Contains(content, "Controller for test-session in /data/projects/test") {
		t.Errorf("template variables not substituted correctly, got:\n%s", content)
	}
	if !strings.Contains(content, "- Pane 3: gmi") {
		t.Error("agent list not substituted in custom prompt")
	}
}

func TestResolveControllerPrompt_MissingFile(t *testing.T) {
	opts := ControllerInput{
		PromptFile: "/nonexistent/path/prompt.txt",
	}

	_, _, err := resolveControllerPrompt(opts, "sess", "", "/tmp")
	if err == nil {
		t.Fatal("expected error for missing prompt file")
	}
	if !strings.Contains(err.Error(), "reading prompt file") {
		t.Errorf("error should mention reading prompt file, got: %v", err)
	}
}

func TestResolveControllerPrompt_InvalidTemplate(t *testing.T) {
	tmpDir := t.TempDir()
	promptPath := filepath.Join(tmpDir, "bad.txt")

	// Write a broken Go template
	if err := os.WriteFile(promptPath, []byte("Hello {{.Broken"), 0644); err != nil {
		t.Fatalf("failed to write prompt file: %v", err)
	}

	opts := ControllerInput{
		PromptFile: promptPath,
	}

	_, _, err := resolveControllerPrompt(opts, "sess", "", "/tmp")
	if err == nil {
		t.Fatal("expected error for invalid template syntax")
	}
	if !strings.Contains(err.Error(), "parsing prompt template") {
		t.Errorf("error should mention parsing, got: %v", err)
	}
}

func TestResolveControllerPrompt_SpecialCharsInSession(t *testing.T) {
	opts := ControllerInput{}
	content, _, err := resolveControllerPrompt(opts, "my-project_v2", "- Pane 1: cc", "/home/user/my-project_v2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(content, "my-project_v2") {
		t.Error("session name with special characters should be preserved")
	}
}

// ---------------------------------------------------------------------------
// ControllerInput validation
// ---------------------------------------------------------------------------

func TestControllerInput_AgentTypeDefaults(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantFull string
		wantErr  bool
	}{
		{"cc maps to claude", "cc", "claude", false},
		{"claude maps to claude", "claude", "claude", false},
		{"claude_code maps to claude", "claude_code", "claude", false},
		{"cod maps to codex", "cod", "codex", false},
		{"codex maps to codex", "codex", "codex", false},
		{"codex-cli maps to codex", "codex-cli", "codex", false},
		{"gmi maps to gemini", "gmi", "gemini", false},
		{"gemini maps to gemini", "gemini", "gemini", false},
		{"google-gemini maps to gemini", "google-gemini", "gemini", false},
		{"unknown type errors", "foo", "", true},
		{"empty defaults to cc", "", "claude", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agentType := tt.input
			if agentType == "" {
				agentType = "cc"
			}
			var agentTypeFull string
			var err error
			switch robot.ResolveAgentType(agentType) {
			case "claude":
				agentTypeFull = "claude"
			case "codex":
				agentTypeFull = "codex"
			case "gemini":
				agentTypeFull = "gemini"
			case "cursor":
				agentTypeFull = "cursor"
			case "windsurf":
				agentTypeFull = "windsurf"
			case "aider":
				agentTypeFull = "aider"
			case "ollama":
				agentTypeFull = "ollama"
			default:
				err = &agentTypeError{agentType: agentType}
			}

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for agent type %q", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if agentTypeFull != tt.wantFull {
				t.Errorf("agent type %q -> %q, want %q", tt.input, agentTypeFull, tt.wantFull)
			}
		})
	}
}

// agentTypeError is a helper for the test above.
type agentTypeError struct {
	agentType string
}

func (e *agentTypeError) Error() string {
	return "unknown agent type: " + e.agentType
}

// ---------------------------------------------------------------------------
// Pane selection logic (unit-level, no tmux required)
// ---------------------------------------------------------------------------

func TestPaneSelectionLogic_FindsPane1(t *testing.T) {
	// Simulate a pane list and the logic from buildControllerResponse
	// that searches for pane index 1.
	type testPane struct {
		ID    string
		Index int
	}

	panes := []testPane{
		{ID: "%0", Index: 0},
		{ID: "%1", Index: 1},
		{ID: "%2", Index: 2},
	}

	var targetPaneID string
	var targetPaneIndex int
	found := false

	for _, p := range panes {
		if p.Index == 1 {
			found = true
			targetPaneID = p.ID
			targetPaneIndex = p.Index
			break
		}
	}

	if !found {
		t.Fatal("expected to find pane with index 1")
	}
	if targetPaneID != "%1" {
		t.Errorf("expected pane ID '%%1', got %q", targetPaneID)
	}
	if targetPaneIndex != 1 {
		t.Errorf("expected pane index 1, got %d", targetPaneIndex)
	}
}

func TestPaneSelectionLogic_NoPane1(t *testing.T) {
	type testPane struct {
		ID    string
		Index int
	}

	panes := []testPane{
		{ID: "%0", Index: 0},
		{ID: "%5", Index: 5},
	}

	found := false
	for _, p := range panes {
		if p.Index == 1 {
			found = true
			break
		}
	}

	if found {
		t.Error("should not find pane 1 when none exists")
	}
}

func TestPaneSelectionLogic_EmptyPanes(t *testing.T) {
	type testPane struct {
		ID    string
		Index int
	}

	panes := []testPane{}

	found := false
	for _, p := range panes {
		if p.Index == 1 {
			found = true
			break
		}
	}

	if found {
		t.Error("should not find pane 1 in empty list")
	}
}

// ---------------------------------------------------------------------------
// Command registration
// ---------------------------------------------------------------------------

func TestControllerCmdRegistered(t *testing.T) {
	// Verify the controller command is registered in rootCmd
	cmd, _, err := rootCmd.Find([]string{"controller"})
	if err != nil {
		t.Fatalf("controller command not found: %v", err)
	}
	if cmd == nil {
		t.Fatal("controller command is nil")
	}
	if cmd.Use != "controller <session>" {
		t.Errorf("unexpected Use string: %q", cmd.Use)
	}
}

func TestControllerCmdFlags(t *testing.T) {
	cmd := newControllerCmd()

	// Check --agent-type flag
	f := cmd.Flags().Lookup("agent-type")
	if f == nil {
		t.Fatal("--agent-type flag not found")
	}
	if f.DefValue != "cc" {
		t.Errorf("--agent-type default = %q, want 'cc'", f.DefValue)
	}

	// Check --prompt flag
	f = cmd.Flags().Lookup("prompt")
	if f == nil {
		t.Fatal("--prompt flag not found")
	}
	if f.DefValue != "" {
		t.Errorf("--prompt default = %q, want ''", f.DefValue)
	}

	// Check --no-prompt flag
	f = cmd.Flags().Lookup("no-prompt")
	if f == nil {
		t.Fatal("--no-prompt flag not found")
	}
	if f.DefValue != "false" {
		t.Errorf("--no-prompt default = %q, want 'false'", f.DefValue)
	}
}

func TestControllerCmdRequiresExactlyOneArg(t *testing.T) {
	cmd := newControllerCmd()

	// Test with no args - should fail
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Error("expected error when no session argument provided")
	}

	// Test with too many args - should fail
	cmd2 := newControllerCmd()
	cmd2.SetArgs([]string{"session1", "session2"})
	err = cmd2.Execute()
	if err == nil {
		t.Error("expected error when too many arguments provided")
	}
}

// ---------------------------------------------------------------------------
// ControllerResponse structure
// ---------------------------------------------------------------------------

func TestControllerResponseFields(t *testing.T) {
	resp := ControllerResponse{
		Session:    "test-proj",
		PaneID:     "%5",
		PaneIndex:  1,
		AgentType:  "claude",
		PromptUsed: "default",
		AgentCount: 3,
		AgentList:  "- Pane 2: cc\n- Pane 3: cod\n- Pane 4: gmi",
	}

	if resp.Session != "test-proj" {
		t.Errorf("Session = %q, want 'test-proj'", resp.Session)
	}
	if resp.PaneIndex != 1 {
		t.Errorf("PaneIndex = %d, want 1", resp.PaneIndex)
	}
	if resp.AgentType != "claude" {
		t.Errorf("AgentType = %q, want 'claude'", resp.AgentType)
	}
	if resp.AgentCount != 3 {
		t.Errorf("AgentCount = %d, want 3", resp.AgentCount)
	}
}

// ---------------------------------------------------------------------------
// Default prompt content checks
// ---------------------------------------------------------------------------

func TestDefaultControllerPromptContent(t *testing.T) {
	// Verify the default prompt template contains all expected placeholders
	if !strings.Contains(defaultControllerPrompt, "{{.Session}}") {
		t.Error("default prompt should contain {{.Session}} placeholder")
	}
	if !strings.Contains(defaultControllerPrompt, "{{.AgentList}}") {
		t.Error("default prompt should contain {{.AgentList}} placeholder")
	}

	// Verify it contains key coordination responsibilities
	if !strings.Contains(defaultControllerPrompt, "coordinate") {
		t.Error("default prompt should mention coordination")
	}
	if !strings.Contains(defaultControllerPrompt, "ntm status") {
		t.Error("default prompt should mention ntm status command")
	}
	if !strings.Contains(defaultControllerPrompt, "ntm view") {
		t.Error("default prompt should mention ntm view command")
	}
	if !strings.Contains(defaultControllerPrompt, "ntm send") {
		t.Error("default prompt should mention ntm send command")
	}
}

// ---------------------------------------------------------------------------
// Robot controller spawn flags
// ---------------------------------------------------------------------------

func TestRobotControllerSpawnFlagRegistered(t *testing.T) {
	f := rootCmd.Flags().Lookup("robot-controller-spawn")
	if f == nil {
		t.Fatal("--robot-controller-spawn flag not found on rootCmd")
	}
	if f.DefValue != "" {
		t.Errorf("--robot-controller-spawn default = %q, want empty", f.DefValue)
	}
}

func TestRobotControllerAgentTypeFlagRegistered(t *testing.T) {
	f := rootCmd.Flags().Lookup("controller-agent-type")
	if f == nil {
		t.Fatal("--controller-agent-type flag not found on rootCmd")
	}
	if f.DefValue != "cc" {
		t.Errorf("--controller-agent-type default = %q, want 'cc'", f.DefValue)
	}
}

func TestRobotControllerPromptFlagRegistered(t *testing.T) {
	f := rootCmd.Flags().Lookup("controller-prompt")
	if f == nil {
		t.Fatal("--controller-prompt flag not found on rootCmd")
	}
}

func TestRobotControllerNoPromptFlagRegistered(t *testing.T) {
	f := rootCmd.Flags().Lookup("controller-no-prompt")
	if f == nil {
		t.Fatal("--controller-no-prompt flag not found on rootCmd")
	}
	if f.DefValue != "false" {
		t.Errorf("--controller-no-prompt default = %q, want 'false'", f.DefValue)
	}
}

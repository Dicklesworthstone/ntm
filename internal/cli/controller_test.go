package cli

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TestControllerBootstrapsIdentityBeforeAgentCommand prevents controller
// launches from regressing to the old post-launch registration race. The
// check is structural because the production dispatch is a real tmux command:
// the identity coordinator must run before controller SendKeys.
func TestControllerBootstrapsIdentityBeforeAgentCommand(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate controller test source")
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(filepath.Dir(testFile), "controller.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse controller.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if candidate, ok := decl.(*ast.FuncDecl); ok && candidate.Name.Name == "buildControllerResponse" {
			fn = candidate
			break
		}
	}
	if fn == nil {
		t.Fatal("buildControllerResponse not found")
	}

	var prepare, launch token.Pos
	ast.Inspect(fn, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch selector.Sel.Name {
		case "prepareAgent":
			prepare = call.Pos()
		case "SendKeys":
			if launch == token.NoPos {
				launch = call.Pos()
			}
		}
		return true
	})
	if prepare == token.NoPos || launch == token.NoPos {
		t.Fatalf("controller bootstrap/launch calls missing: prepare=%v launch=%v", prepare, launch)
	}
	if fset.Position(prepare).Offset >= fset.Position(launch).Offset {
		t.Fatalf("controller identity bootstrap at %v must precede agent command at %v", fset.Position(prepare), fset.Position(launch))
	}
}

func TestControllerUsesDetachedDedicatedWindowWithoutTargetingExistingPanes(t *testing.T) {
	existing := []tmux.Pane{
		{ID: "%44", Index: 1, WindowIndex: 1, PID: 4400, Title: "bbi-infrastructure__cod_1"},
		{ID: "%51", Index: 0, WindowIndex: 2, PID: 5100, Title: "bbi-infrastructure__cod_2"},
	}
	before := append([]tmux.Pane(nil), existing...)
	previousCreate := controllerCreateDetachedWindow
	controllerCreateDetachedWindow = func(_ context.Context, session, directory string) (string, error) {
		if session != "bbi-infrastructure" || directory != "/home/ubuntu/work/bbi-infrastructure" {
			t.Fatalf("detached window target = %q %q", session, directory)
		}
		return "%200", nil
	}
	t.Cleanup(func() { controllerCreateDetachedWindow = previousCreate })
	newPaneID, err := createDedicatedControllerPane(t.Context(), "bbi-infrastructure", "/home/ubuntu/work/bbi-infrastructure", existing)
	if err != nil {
		t.Fatalf("createDedicatedControllerPane: %v", err)
	}
	if newPaneID != "%200" {
		t.Fatalf("controller pane = %q, want newly-created %%200", newPaneID)
	}
	args := newControllerWindowArgs("bbi-infrastructure", "/home/ubuntu/work/bbi-infrastructure")

	want := []string{
		"new-window", "-d", "-t", "=bbi-infrastructure", "-c", "/home/ubuntu/work/bbi-infrastructure",
		"-n", "ntm-controller", "-P", "-F", "#{pane_id}",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("controller window args = %#v, want %#v", args, want)
	}
	if !reflect.DeepEqual(existing, before) {
		t.Fatalf("controller planning mutated existing panes: got %+v want %+v", existing, before)
	}
	for _, arg := range args {
		if arg == "%44" || arg == "%51" || arg == "split-window" || arg == "send-keys" {
			t.Fatalf("detached controller creation targets an existing pane/process via %q", arg)
		}
	}
}

func TestResolveControllerProjectDirPreservesConfiguredTargetInMixedSession(t *testing.T) {
	projectsBase := t.TempDir()
	target := filepath.Join(projectsBase, "bbi-infrastructure")
	if err := os.MkdirAll(filepath.Join(target, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	previousCfg := cfg
	configured := config.Default()
	configured.ProjectsBase = projectsBase
	cfg = configured
	t.Cleanup(func() { cfg = previousCfg })

	previousCurrentDir := controllerPaneCurrentDir
	controllerPaneCurrentDir = func(paneID string) (string, error) {
		switch paneID {
		case "%44":
			return target, nil
		case "%66":
			return "/home/ubuntu/work/mereka-lms", nil
		default:
			return "", os.ErrNotExist
		}
	}
	t.Cleanup(func() { controllerPaneCurrentDir = previousCurrentDir })

	panes := []tmux.Pane{
		{ID: "%44", PID: 4400, Title: "bbi-infrastructure__cod_1"},
		{ID: "%66", PID: 6600, Title: "mereka-lms__cc_1"},
	}
	got, err := resolveControllerProjectDir(t.Context(), "bbi-infrastructure", panes)
	if err != nil {
		t.Fatalf("resolveControllerProjectDir: %v", err)
	}
	if got != target {
		t.Fatalf("controller project dir = %q, want configured BBI checkout %q", got, target)
	}
}

func TestRenderControllerAgentCommandCarriesExplicitSolUltraAndProfile(t *testing.T) {
	t.Setenv("NTM_CODEX_PROFILE", "g1")
	template := `exec /opt/ntm/codex ${NTM_CODEX_PROFILE:-g3} -m {{shellQuote .Model}} -c model_reasoning_effort={{shellQuote .ReasoningEffort}}`
	opts := ControllerInput{Model: "gpt-5.6-sol", ReasoningEffort: "ultra"}

	got, err := renderControllerAgentCommand(template, opts, "cod", "codex", "bbi-infrastructure", "/home/ubuntu/work/bbi-infrastructure")
	if err != nil {
		t.Fatalf("renderControllerAgentCommand: %v", err)
	}
	for _, want := range []string{
		"export NTM_CODEX_PROFILE='g1';",
		"'gpt-5.6-sol'",
		"model_reasoning_effort='ultra'",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered controller command %q does not contain %q", got, want)
		}
	}
}

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
	if !strings.Contains(content, "--robot-activity=myproject") {
		t.Error("prompt should contain '--robot-activity=myproject'")
	}
	if !strings.Contains(content, "ntm send myproject --pane") {
		t.Error("prompt should contain 'ntm send myproject --pane'")
	}
	// Verify stale/disruptive commands are NOT in the prompt
	if strings.Contains(content, "ntm view myproject") {
		t.Error("prompt should NOT contain 'ntm view myproject' (changes human layout; use --robot-tail)")
	}
	// The bug in #109: the `ntm send` SUBCOMMAND does not accept --msg. Only
	// the --robot-send GLOBAL flag does. Walk the rendered prompt line-by-line
	// and fail if any single line mixes the subcommand with --msg — this
	// catches the exact regression shape while still allowing the legitimate
	// `ntm --robot-send=... --msg=...` example on its own line.
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "ntm send myproject") && strings.Contains(line, "--msg") {
			t.Errorf("prompt line mixes 'ntm send' subcommand with invalid --msg flag: %q", line)
		}
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

func TestControllerAgentListCanonicalizesAliasesAndIncludesOllama(t *testing.T) {

	panes := []tmux.Pane{
		{Index: 2, Type: tmux.AgentType("claude_code")},
		{Index: 3, Type: tmux.AgentType("openai-codex")},
		{Index: 4, Type: tmux.AgentType("google-gemini")},
		{Index: 5, Type: tmux.AgentType("ws")},
		{Index: 6, Type: tmux.AgentOllama},
		{Index: 7, Type: tmux.AgentType("controller_codex")},
		{Index: 8, Type: tmux.AgentUser},
	}

	list, count := controllerAgentList(panes)
	if count != 5 {
		t.Fatalf("controllerAgentList() count = %d, want 5", count)
	}

	want := []string{
		"- Pane 2: cc",
		"- Pane 3: cod",
		"- Pane 4: gmi",
		"- Pane 5: windsurf",
		"- Pane 6: ollama",
	}
	if len(list) != len(want) {
		t.Fatalf("controllerAgentList() len = %d, want %d (%v)", len(list), len(want), list)
	}
	for i := range want {
		if list[i] != want[i] {
			t.Fatalf("controllerAgentList()[%d] = %q, want %q", i, list[i], want[i])
		}
	}
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

	// Explicit model and effort flags must reach AgentTemplateVars rather than
	// silently falling back to the controller template's defaults.
	f = cmd.Flags().Lookup("model")
	if f == nil {
		t.Fatal("--model flag not found")
	}
	if f.DefValue != "" {
		t.Errorf("--model default = %q, want ''", f.DefValue)
	}
	f = cmd.Flags().Lookup("reasoning-effort")
	if f == nil {
		t.Fatal("--reasoning-effort flag not found")
	}
	if f.DefValue != "" {
		t.Errorf("--reasoning-effort default = %q, want ''", f.DefValue)
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
		Session:         "test-proj",
		PaneID:          "%5",
		PaneIndex:       1,
		AgentType:       "codex",
		Model:           "gpt-5.6-sol",
		ReasoningEffort: "ultra",
		PromptUsed:      "default",
		AgentCount:      3,
		AgentList:       "- Pane 2: cc\n- Pane 3: cod\n- Pane 4: gmi",
	}

	if resp.Session != "test-proj" {
		t.Errorf("Session = %q, want 'test-proj'", resp.Session)
	}
	if resp.PaneIndex != 1 {
		t.Errorf("PaneIndex = %d, want 1", resp.PaneIndex)
	}
	if resp.AgentType != "codex" {
		t.Errorf("AgentType = %q, want 'codex'", resp.AgentType)
	}
	if resp.Model != "gpt-5.6-sol" || resp.ReasoningEffort != "ultra" {
		t.Errorf("controller model/effort = %q/%q, want gpt-5.6-sol/ultra", resp.Model, resp.ReasoningEffort)
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
	// Prefer structured --robot-* commands over interactive TUIs (fix: #109)
	if !strings.Contains(defaultControllerPrompt, "--robot-snapshot") {
		t.Error("default prompt should mention --robot-snapshot for structured state")
	}
	if !strings.Contains(defaultControllerPrompt, "--robot-attention") {
		t.Error("default prompt should mention --robot-attention for event waiting")
	}
	if !strings.Contains(defaultControllerPrompt, "--robot-tail") {
		t.Error("default prompt should mention --robot-tail for pane output inspection")
	}
	if !strings.Contains(defaultControllerPrompt, "ntm send") {
		t.Error("default prompt should mention ntm send command")
	}
	// Verify the stale/disruptive examples from issue #109 are NOT present.
	// The bug: ntm send (subcommand) does not accept --msg. Only --robot-send
	// (global flag) does. Fail if any line mixes 'ntm send' subcommand with --msg.
	for _, line := range strings.Split(defaultControllerPrompt, "\n") {
		if strings.Contains(line, "ntm send ") && strings.Contains(line, "--msg") {
			t.Errorf("default prompt line mixes 'ntm send' subcommand with invalid --msg flag: %q (issue #109)", line)
		}
	}
	// 'ntm view' changes the human operator's tmux layout; controller agents must not run it
	if strings.Contains(defaultControllerPrompt, "- ntm view") {
		t.Error("default prompt must not recommend 'ntm view' to the controller agent (issue #109)")
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

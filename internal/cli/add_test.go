package cli

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func TestNewAgentLifecycleFailureResponseClassifiesPromptFailureAndMutation(t *testing.T) {
	underlying := errors.New("injected prompt send failure")
	response := newAgentLifecycleFailureResponse(
		newPromptSendFailure(underlying),
		"truthful-session",
		true,
		true,
		[]string{"%7", "%7", "", " %8 "},
		nil,
	)

	if response.Success || response.ErrorCode != robot.ErrCodePromptSendFailed || response.Code != robot.ErrCodePromptSendFailed {
		t.Fatalf("prompt failure response = %+v", response)
	}
	if !response.PartialMutation || !response.SessionMayExist || response.Session != "truthful-session" {
		t.Fatalf("prompt failure mutation state = %+v", response)
	}
	if len(response.AffectedPaneIDs) != 2 || response.AffectedPaneIDs[0] != "%7" || response.AffectedPaneIDs[1] != "%8" {
		t.Fatalf("affected pane IDs = %v, want deduplicated [%%7 %%8]", response.AffectedPaneIDs)
	}
	if !strings.Contains(response.Error, underlying.Error()) || response.GeneratedAt.IsZero() {
		t.Fatalf("prompt failure error/timestamp = %+v", response)
	}
}

func TestNewAgentLifecycleFailureResponseCancellationTakesPrecedence(t *testing.T) {
	err := newPromptSendFailure(errors.Join(errors.New("dispatch interrupted"), context.Canceled))
	response := newAgentLifecycleFailureResponse(err, "cancel-session", true, true, nil, nil)
	if response.Success || response.ErrorCode != robot.ErrCodeTimeout || response.Code != robot.ErrCodeTimeout {
		t.Fatalf("canceled prompt response = %+v, want TIMEOUT", response)
	}
	if response.AffectedPaneIDs == nil || len(response.AffectedPaneIDs) != 0 {
		t.Fatalf("canceled prompt affected panes = %v, want checked-empty []", response.AffectedPaneIDs)
	}
}

func TestNewAgentLifecycleFailureResponseErrorCodePrecedence(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "cancellation beats prompt failure",
			err:  newPromptSendFailure(errors.Join(errors.New("dispatch interrupted"), context.Canceled)),
			want: robot.ErrCodeTimeout,
		},
		{
			name: "deadline beats invalid input",
			err:  markCLIInvalidInput(errors.Join(errors.New("policy load timed out"), context.DeadlineExceeded)),
			want: robot.ErrCodeTimeout,
		},
		{
			name: "prompt failure beats invalid input",
			err:  newPromptSendFailure(markCLIInvalidInput(errors.New("prompt target is invalid"))),
			want: robot.ErrCodePromptSendFailed,
		},
		{
			name: "invalid input",
			err:  markCLIInvalidInput(errors.New("invalid target policy")),
			want: robot.ErrCodeInvalidFlag,
		},
		{
			name: "internal error fallback",
			err:  errors.New("unexpected lifecycle failure"),
			want: robot.ErrCodeInternalError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := newAgentLifecycleFailureResponse(tt.err, "classified-session", false, false, nil, nil)
			if response.ErrorCode != tt.want || response.Code != tt.want {
				t.Fatalf("lifecycle failure code = (%q, %q), want (%q, %q)", response.ErrorCode, response.Code, tt.want, tt.want)
			}
		})
	}
}

func TestNewAgentLifecycleFailureResponseReportsAffectedWorktrees(t *testing.T) {
	response := newAgentLifecycleFailureResponse(
		errors.New("second worktree failed"),
		"worktree-session",
		true,
		false,
		nil,
		[]string{" /repo/.ntm/worktrees/worktree-session/cc_1 ", "", "/repo/.ntm/worktrees/worktree-session/cc_1"},
	)

	if len(response.AffectedWorktreePaths) != 1 || response.AffectedWorktreePaths[0] != "/repo/.ntm/worktrees/worktree-session/cc_1" {
		t.Fatalf("affected worktree paths = %v, want one normalized path", response.AffectedWorktreePaths)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal lifecycle failure: %v", err)
	}
	if !strings.Contains(string(encoded), `"affected_worktree_paths":["/repo/.ntm/worktrees/worktree-session/cc_1"]`) {
		t.Fatalf("lifecycle failure JSON omits affected worktree paths: %s", encoded)
	}
}

func TestPrepareRequiredPersonaSystemPromptFailsClosed(t *testing.T) {
	t.Run("malformed persona name", func(t *testing.T) {
		_, err := prepareRequiredPersonaSystemPrompt(&persona.Persona{
			Name:         "../reviewer",
			SystemPrompt: "review carefully",
		}, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "invalid characters") {
			t.Fatalf("malformed persona prompt error = %v", err)
		}
	})

	t.Run("prompt destination is not a directory", func(t *testing.T) {
		projectDir := t.TempDir()
		ntmDir := filepath.Join(projectDir, ".ntm")
		if err := os.MkdirAll(ntmDir, 0o700); err != nil {
			t.Fatalf("create .ntm directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(ntmDir, "prompts"), []byte("collision"), 0o600); err != nil {
			t.Fatalf("create prompt path collision: %v", err)
		}
		_, err := prepareRequiredPersonaSystemPrompt(&persona.Persona{
			Name:         "reviewer",
			SystemPrompt: "review carefully",
		}, projectDir)
		if err == nil || !strings.Contains(err.Error(), "prompts path is not a directory") {
			t.Fatalf("prompt path collision error = %v", err)
		}
	})
}

func TestResolveAddAgentCommandTemplate_Ollama(t *testing.T) {

	oldCfg := cfg
	defer func() {
		cfg = oldCfg
	}()

	cfg = config.Default()
	cfg.Agents.Ollama = "ollama run {{shellQuote (.Model | default \"codellama:latest\")}}"

	cmd, env, err := resolveAddAgentCommandTemplate(AgentTypeOllama, nil, "http://127.0.0.1:11434")
	if err != nil {
		t.Fatalf("resolveAddAgentCommandTemplate() error = %v", err)
	}
	if cmd != cfg.Agents.Ollama {
		t.Fatalf("resolveAddAgentCommandTemplate() cmd = %q, want %q", cmd, cfg.Agents.Ollama)
	}
	if env["OLLAMA_HOST"] != "http://127.0.0.1:11434" {
		t.Fatalf("resolveAddAgentCommandTemplate() env OLLAMA_HOST = %q", env["OLLAMA_HOST"])
	}
}

func TestNewAddCmd_RegistersOllamaFlag(t *testing.T) {

	cmd := newAddCmd()
	if cmd.Flags().Lookup("ollama") == nil {
		t.Fatal("expected add command to register --ollama")
	}
}

// TestAddThreadsReasoningEffort is the ntm#195 regression guard. The `add`
// command parses the `:effort` segment of `--cc=N:model:effort` into the
// AgentSpec/FlatAgent, but runAdd previously omitted ReasoningEffort from the
// AgentTemplateVars handed to GenerateAgentCommand. The Claude template only
// emits `--effort` under `{{if .ReasoningEffort}}`, so the segment was
// silently dropped and the added pane launched at the CLI default — the same
// class of bug fixed for `spawn` in ntm#188. This drives the real
// parse→Flatten→render path the add loop uses and asserts the effort flows
// through, with a negative control proving an unset effort leaves no flag.
func TestAddThreadsReasoningEffort(t *testing.T) {
	oldCfg := cfg
	defer func() { cfg = oldCfg }()
	cfg = config.Default()

	// Parse exactly as the --cc flag would, then flatten to the per-pane agent
	// the runAdd loop iterates over.
	spec, err := ParseAgentSpec("1:claude-opus-4-8:xhigh")
	if err != nil {
		t.Fatalf("ParseAgentSpec error = %v", err)
	}
	spec.Type = AgentTypeClaude
	flat := AgentSpecs{spec}.Flatten()
	if len(flat) != 1 {
		t.Fatalf("Flatten() len = %d, want 1", len(flat))
	}
	agent := flat[0]
	if agent.ReasoningEffort != "xhigh" {
		t.Fatalf("FlatAgent.ReasoningEffort = %q, want xhigh", agent.ReasoningEffort)
	}

	// Mirror runAdd's render: thread the flattened agent's effort into the vars.
	withEffort, err := config.GenerateAgentCommand(cfg.Agents.Claude, config.AgentTemplateVars{
		Model:           ResolveModel(agent.Type, agent.Model),
		ReasoningEffort: agent.ReasoningEffort,
	})
	if err != nil {
		t.Fatalf("GenerateAgentCommand (with effort) error = %v", err)
	}
	// The Claude template shell-quotes the value: `--effort 'xhigh'`.
	if !strings.Contains(withEffort, "--effort 'xhigh'") {
		t.Errorf("add render dropped reasoning effort: got %q, want it to contain %q", withEffort, "--effort 'xhigh'")
	}

	// Negative control: no effort parsed → no dangling --effort flag.
	noEffortSpec, err := ParseAgentSpec("1:claude-opus-4-8")
	if err != nil {
		t.Fatalf("ParseAgentSpec (no effort) error = %v", err)
	}
	noEffortSpec.Type = AgentTypeClaude
	noEffortAgent := AgentSpecs{noEffortSpec}.Flatten()[0]
	noEffort, err := config.GenerateAgentCommand(cfg.Agents.Claude, config.AgentTemplateVars{
		Model:           ResolveModel(noEffortAgent.Type, noEffortAgent.Model),
		ReasoningEffort: noEffortAgent.ReasoningEffort,
	})
	if err != nil {
		t.Fatalf("GenerateAgentCommand (no effort) error = %v", err)
	}
	if strings.Contains(noEffort, "--effort") {
		t.Errorf("unset effort left a dangling flag: %q", noEffort)
	}
}

// TestAddThreadsCodexReasoningEffort is the ntm#208 regression guard. Issue
// #208 reproduced against v1.18.3 (commit 6615dd7), which predates the ntm#195
// `add` fix: `ntm add --cod=1:MODEL:EFFORT` parsed the third spec field but
// runAdd handed GenerateAgentCommand an empty ReasoningEffort, so the default
// Codex template always emitted the fallback rather than the requested effort.
// This drives the real
// parse(--cod=1:model:low)→Flatten→render path the add loop uses against the
// default Codex template and asserts the requested effort reaches
// `model_reasoning_effort='low'`, with a negative control proving an unset
// effort falls back to the template default.
func TestAddThreadsCodexReasoningEffort(t *testing.T) {
	oldCfg := cfg
	defer func() { cfg = oldCfg }()
	cfg = config.Default()

	// Parse exactly as the --cod flag would, then flatten to the per-pane agent
	// the runAdd loop iterates over.
	spec, err := ParseAgentSpec("1:gpt-5.3-codex-spark:low")
	if err != nil {
		t.Fatalf("ParseAgentSpec error = %v", err)
	}
	spec.Type = AgentTypeCodex
	flat := AgentSpecs{spec}.Flatten()
	if len(flat) != 1 {
		t.Fatalf("Flatten() len = %d, want 1", len(flat))
	}
	agent := flat[0]
	if agent.ReasoningEffort != "low" {
		t.Fatalf("FlatAgent.ReasoningEffort = %q, want low", agent.ReasoningEffort)
	}

	// Mirror runAdd's render: thread the flattened agent's effort into the vars.
	withEffort, err := config.GenerateAgentCommand(cfg.Agents.Codex, config.AgentTemplateVars{
		Model:           ResolveModel(agent.Type, agent.Model),
		ReasoningEffort: agent.ReasoningEffort,
	})
	if err != nil {
		t.Fatalf("GenerateAgentCommand (with effort) error = %v", err)
	}
	// The Codex template shell-quotes the value: `model_reasoning_effort='low'`.
	if !strings.Contains(withEffort, "model_reasoning_effort='low'") {
		t.Errorf("add render dropped Codex reasoning effort: got %q, want it to contain %q", withEffort, "model_reasoning_effort='low'")
	}

	// Negative control: no effort parsed → template default (not 'low').
	noEffortSpec, err := ParseAgentSpec("1:gpt-5.3-codex-spark")
	if err != nil {
		t.Fatalf("ParseAgentSpec (no effort) error = %v", err)
	}
	noEffortSpec.Type = AgentTypeCodex
	noEffortAgent := AgentSpecs{noEffortSpec}.Flatten()[0]
	noEffort, err := config.GenerateAgentCommand(cfg.Agents.Codex, config.AgentTemplateVars{
		Model:           ResolveModel(noEffortAgent.Type, noEffortAgent.Model),
		ReasoningEffort: noEffortAgent.ReasoningEffort,
	})
	if err != nil {
		t.Fatalf("GenerateAgentCommand (no effort) error = %v", err)
	}
	if strings.Contains(noEffort, "model_reasoning_effort='low'") {
		t.Errorf("unset effort should not render low: %q", noEffort)
	}
	if !strings.Contains(noEffort, "model_reasoning_effort="+config.ShellQuote(config.DefaultCodexReasoningEffort)) {
		t.Errorf("unset effort should render default effort: %q", noEffort)
	}
}

// TestAddRegistersAgentsWithAgentMail is the ntm#240 regression guard. `ntm add`
// launched agents into a live session but never called registerSpawnedAgents,
// so — unlike spawn — the added panes got no Agent Mail identity and could
// neither send nor receive mail. The launch loop lives inside executeAdd and
// needs a real tmux session to drive, so this asserts structurally (the same
// technique as TestRootDispatcherDoesNotExitProcess) that executeAdd collects
// the registration inputs for every pane it launches and hands them to the
// shared helper. Pre-fix, add.go contained zero references to either symbol.
func TestAddRegistersAgentsWithAgentMail(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate cli test source")
	}
	addFile := filepath.Join(filepath.Dir(testFile), "add.go")
	file, err := parser.ParseFile(token.NewFileSet(), addFile, nil, 0)
	if err != nil {
		t.Fatalf("parse add command: %v", err)
	}

	var executeAddDecl *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == "executeAdd" {
			executeAddDecl = fn
			break
		}
	}
	if executeAddDecl == nil {
		t.Fatal("executeAdd not found in add.go")
	}

	registerCalls := 0
	// The six fields registerSpawnedAgents needs to create (or reuse) an
	// identity: anything missing here silently degrades the registration.
	wantFields := map[string]bool{
		"paneIndex": false, "paneID": false, "paneTitle": false,
		"agentType": false, "model": false, "resolvedModel": false,
	}
	ast.Inspect(executeAddDecl, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.CallExpr:
			if ident, ok := n.Fun.(*ast.Ident); ok && ident.Name == "registerSpawnedAgents" {
				registerCalls++
			}
		case *ast.CompositeLit:
			ident, ok := n.Type.(*ast.Ident)
			if !ok || ident.Name != "spawnedAgentInfo" {
				return true
			}
			for _, elt := range n.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				if _, tracked := wantFields[key.Name]; tracked {
					wantFields[key.Name] = true
				}
			}
		}
		return true
	})

	if registerCalls != 1 {
		t.Fatalf("executeAdd calls registerSpawnedAgents %d time(s), want exactly 1 (added agents must be registered with Agent Mail)", registerCalls)
	}
	for field, seen := range wantFields {
		if !seen {
			t.Errorf("executeAdd does not populate spawnedAgentInfo.%s for added panes", field)
		}
	}
}

// TestAddResponseCarriesAgentMailStatus locks the ntm#240 response contract:
// AddResponse gained the agent_mail block SpawnResponse already had, so
// automation can read back the pane_id -> agent name mapping for added panes.
func TestAddResponseCarriesAgentMailStatus(t *testing.T) {
	data, err := json.Marshal(output.AddResponse{
		TotalAdded: 1,
		NewPanes:   []output.PaneResponse{{PaneID: "%42", Title: "proj__cc_2", Type: "cc"}},
		AgentMail: &output.AgentMailSpawnStatus{
			Available:         true,
			ProjectRegistered: true,
			AgentsRegistered:  1,
			AgentMap:          map[string]string{"%42": "Aurelia"},
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal(AddResponse) error = %v", err)
	}
	encoded := string(data)
	if !strings.Contains(encoded, `"agent_mail"`) || !strings.Contains(encoded, `"agents_registered":1`) {
		t.Fatalf("AddResponse JSON = %s, want agent_mail registration status", encoded)
	}
	if !strings.Contains(encoded, `"%42":"Aurelia"`) {
		t.Fatalf("AddResponse JSON = %s, want the pane_id -> agent name mapping", encoded)
	}

	// Negative control: sessions where Agent Mail is disabled must not grow an
	// empty agent_mail object in the response.
	bare, err := json.Marshal(output.AddResponse{TotalAdded: 1})
	if err != nil {
		t.Fatalf("json.Marshal(bare AddResponse) error = %v", err)
	}
	if strings.Contains(string(bare), "agent_mail") {
		t.Fatalf("AddResponse JSON = %s, want agent_mail omitted when unset", bare)
	}
}

func TestAddResponseJSONIncludesModernAgentCounts(t *testing.T) {

	data, err := json.Marshal(output.AddResponse{
		AddedClaude: 1,
		AddedGrok:   2,
		AddedOllama: 3,
		TotalAdded:  6,
	})
	if err != nil {
		t.Fatalf("json.Marshal(AddResponse) error = %v", err)
	}

	encoded := string(data)
	if !strings.Contains(encoded, "\"added_grok\":2") {
		t.Fatalf("AddResponse JSON = %s, want added_grok field", encoded)
	}
	if !strings.Contains(encoded, "\"added_ollama\":3") {
		t.Fatalf("AddResponse JSON = %s, want added_ollama field", encoded)
	}
}

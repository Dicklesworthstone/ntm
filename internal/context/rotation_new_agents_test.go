package context

import (
	stdcontext "context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestDeriveAgentTypeFromID_NewAgents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		agentID string
		want    string
	}{
		{"myproject__cursor_1", "cursor"},
		{"myproject__windsurf_2", "windsurf"},
		{"myproject__ws_2", "windsurf"},
		{"myproject__aider_3", "aider"},
		{"myproject__ollama_4", "ollama"},
		{"myproject__cursor_1_variant", "cursor"},
	}

	for _, tt := range tests {
		t.Run(tt.agentID, func(t *testing.T) {
			got := deriveAgentTypeFromID(tt.agentID)
			if got != tt.want {
				t.Errorf("deriveAgentTypeFromID(%q) = %q, want %q", tt.agentID, got, tt.want)
			}
		})
	}
}

func TestAgentTypeShort_NewAgents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		agentType string
		want      string
	}{
		{"cursor", "cursor"},
		{"windsurf", "windsurf"},
		{"ws", "windsurf"},
		{"aider", "aider"},
		{"ollama", "ollama"},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			got := agentTypeShort(tt.agentType)
			if got != tt.want {
				t.Errorf("agentTypeShort(%q) = %q, want %q", tt.agentType, got, tt.want)
			}
		})
	}
}

func TestAgentTypeLong_NewAgents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		shortType string
		want      string
	}{
		{"cursor", "cursor"},
		{"windsurf", "windsurf"},
		{"ws", "windsurf"},
		{"aider", "aider"},
		{"ollama", "ollama"},
	}

	for _, tt := range tests {
		t.Run(tt.shortType, func(t *testing.T) {
			got := agentTypeLong(tt.shortType)
			if got != tt.want {
				t.Errorf("agentTypeLong(%q) = %q, want %q", tt.shortType, got, tt.want)
			}
		})
	}
}

func TestDefaultPaneSpawnerGetAgentCommand_NewAgents(t *testing.T) {
	t.Parallel()

	// Without config
	spawner := NewDefaultPaneSpawner(nil)
	defaults := config.DefaultAgentTemplates()

	tests := []struct {
		agentType string
		want      string
	}{
		{"cursor", defaults.Cursor},
		{"windsurf", defaults.Windsurf},
		{"ws", defaults.Windsurf},
		{"aider", defaults.Aider},
		{"ollama", defaults.Ollama},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			got := spawner.getAgentCommand(tt.agentType)
			if got != tt.want {
				t.Errorf("getAgentCommand(%q) = %q, want %q", tt.agentType, got, tt.want)
			}
		})
	}
}

func TestDefaultPaneSpawner_RestoresLaunchSpec(t *testing.T) {
	for _, tc := range []struct {
		name      string
		agentType string
		variant   string
		nilConfig bool
		want      []string
	}{
		{"claude_model_effort", "claude", "opus@high", false, []string{"claude --dangerously-skip-permissions", "--model '" + config.DefaultModels().Claude["opus"] + "'", "--effort 'high'"}},
		{"custom_codex_model", "codex", "private_model_2026@medium", false, []string{"codex --dangerously-bypass-approvals-and-sandbox", "-m 'private_model_2026'", "model_reasoning_effort='medium'"}},
		{"bare_custom_model", "codex", "private-model", false, []string{"-m 'private-model'"}},
		{"default_without_config", "codex", "", true, []string{"codex --dangerously-bypass-approvals-and-sandbox", "-m '" + config.DefaultCodexModel + "'"}},
		{"cursor_cli_without_config", "cursor", "", true, []string{"cursor-agent --yolo"}},
		{"omp_model_effort", "omp", "private-model@high", true, []string{"omp --auto-approve", "--model 'private-model'", "--thinking 'high'"}},
		{"opencode_cli", "oc", "custom-model", true, []string{"opencode", "'custom-model'"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workDir, logPath := setupRotationTmux(t)
			cfg := config.Default()
			if tc.nilConfig {
				cfg = nil
			}
			spawner := NewDefaultPaneSpawner(cfg)
			paneID, err := spawner.SpawnAgent("test", tc.agentType, 7, tc.variant, workDir)
			if err != nil {
				t.Fatalf("SpawnAgent() error = %v", err)
			}
			if paneID != "%99" {
				t.Fatalf("SpawnAgent() pane = %q, want %%99", paneID)
			}
			commands, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			log := string(commands)
			if strings.Contains(log, "{{") || strings.Contains(log, "}}") {
				t.Errorf("unrendered template reached tmux: %s", log)
			}
			for _, want := range tc.want {
				if !strings.Contains(log, want) {
					t.Errorf("launch missing %q:\n%s", want, log)
				}
			}
			if !strings.Contains(log, "test__"+agentTypeShort(tc.agentType)+"_7") {
				t.Errorf("replacement title lost logical agent index: %s", log)
			}
		})
	}
}

func TestDefaultPaneSpawner_RestoresPersona(t *testing.T) {
	workDir, logPath := setupRotationTmux(t)
	if err := os.MkdirAll(filepath.Join(workDir, ".ntm"), 0755); err != nil {
		t.Fatal(err)
	}
	personaConfig := `[[personas]]
name = "rotation-reviewer"
agent_type = "codex"
model = "private-model"
reasoning_effort = "high"
system_prompt = "Review changes against the project's correctness requirements."
`
	if err := os.WriteFile(filepath.Join(workDir, ".ntm", "personas.toml"), []byte(personaConfig), 0644); err != nil {
		t.Fatal(err)
	}
	spawner := NewDefaultPaneSpawner(config.Default())
	if _, err := spawner.SpawnAgent("test", "codex", 1, "rotation-reviewer", workDir); err != nil {
		t.Fatalf("SpawnAgent() error = %v", err)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-m 'private-model'", "model_reasoning_effort='high'", "CODEX_SYSTEM_PROMPT=", "rotation-reviewer.md"} {
		if !strings.Contains(string(commands), want) {
			t.Errorf("persona launch missing %q:\n%s", want, commands)
		}
	}
	if strings.Contains(string(commands), "-m 'rotation-reviewer'") {
		t.Errorf("persona name was launched as a model: %s", commands)
	}
}

func TestDefaultPaneSpawner_ReplaysDurableLaunchInsteadOfTitleOrCurrentConfig(t *testing.T) {
	workDir, logPath := setupRotationTmux(t)
	t.Setenv("ROTATION_ORIGINAL_PRESENT", "1")
	worktree := filepath.Join(workDir, "agent worktree")
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROTATION_PREDECESSOR_CWD", worktree)
	spec := tmux.AgentLaunchSpec{
		Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentClaude,
		Command: "/custom/agent-wrapper --model 'private-model' --effort high --persona 'creation-time persona'",
		Model:   "private-model", Persona: "architect", ReasoningEffort: "high",
	}
	record, err := json.Marshal(struct {
		PaneID string               `json:"pane_id"`
		Spec   tmux.AgentLaunchSpec `json:"spec"`
	}{PaneID: "%1", Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROTATION_LAUNCH_SPEC", base64.StdEncoding.EncodeToString(record))
	cfg := config.Default()
	cfg.Agents.Claude = "invalid current template {{.Missing"
	spawner := NewDefaultPaneSpawner(cfg)
	original := tmux.Pane{ID: "%1", PID: 123, Title: "test__cc_1_architect", Type: tmux.AgentClaude, Command: "node", Variant: "architect"}
	pane, err := spawner.SpawnReplacementContext(stdcontext.Background(), "test", original, 1, workDir)
	if err != nil || pane.ID != "%99" || pane.PID != 321 {
		t.Fatalf("replacement = %+v, %v", pane, err)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(commands), spec.Command) || strings.Contains(string(commands), "invalid current template") {
		t.Fatalf("launch did not preserve exact creation command:\n%s", commands)
	}
	if !strings.Contains(string(commands), "-c "+worktree) || !strings.Contains(string(commands), "cd "+tmux.ShellQuote(worktree)) {
		t.Fatalf("replacement lost its predecessor's worktree in favor of caller directory %s:\n%s", workDir, commands)
	}
	foundRecord := false
	for _, line := range strings.Split(string(commands), "\n") {
		if !strings.Contains(line, tmux.PaneLaunchSpecOption+" ") {
			continue
		}
		fields := strings.Fields(line)
		if fields[0] != "set-option" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(fields[len(fields)-1])
		if err != nil {
			t.Fatal(err)
		}
		var saved struct {
			PaneID string               `json:"pane_id"`
			Spec   tmux.AgentLaunchSpec `json:"spec"`
		}
		if err := json.Unmarshal(data, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.PaneID != "%99" || saved.Spec.Command != spec.Command || saved.Spec.Persona != "architect" {
			t.Fatalf("replacement saved incorrect launch metadata: %+v", saved)
		}
		foundRecord = true
	}
	if !foundRecord {
		t.Fatal("replacement lost durable launch metadata")
	}
}

func TestDefaultPaneSpawner_LegacyIsolationRecordsAbsoluteTokenReference(t *testing.T) {
	workDir := t.TempDir()
	t.Chdir(workDir)
	tokenFile := filepath.Join(workDir, "setup-token")
	if err := os.WriteFile(tokenFile, []byte("test-token-reference-only\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Agents.ClaudeIsolateCredentials = true
	cfg.Agents.ClaudeTokenFile = "setup-token"
	spec, err := NewDefaultPaneSpawner(cfg).agentLaunchSpec("test", "claude", 1, "", workDir)
	if err != nil {
		t.Fatalf("legacy launch specification: %v", err)
	}
	if !spec.ClaudeIsolateCredentials || spec.ClaudeTokenFile != tokenFile {
		t.Fatalf("token reference = %q, isolation=%v; want %q", spec.ClaudeTokenFile, spec.ClaudeIsolateCredentials, tokenFile)
	}
	if err := spec.ValidateReplay(tmux.AgentClaude); err != nil {
		t.Fatalf("resolved token reference is not replayable: %v", err)
	}
	if strings.Contains(spec.Command, "test-token-reference-only") {
		t.Fatal("token value leaked into durable command")
	}
}

func TestDefaultPaneSpawner_RejectsUnreadableOrIncompleteLaunchBeforeReplacement(t *testing.T) {
	for _, tc := range []struct {
		name    string
		encoded string
		readErr bool
		want    string
	}{
		{"corrupt", "invalid base64", false, "reading original launch settings"},
		{"read_failure", "", true, "reading original launch settings"},
		{"wrong_provider", base64.StdEncoding.EncodeToString([]byte(`{"pane_id":"%1","spec":{"version":1,"agent_type":"cod","command":"codex"}}`)), false, "does not match"},
		{"omitted_environment", base64.StdEncoding.EncodeToString([]byte(`{"pane_id":"%1","spec":{"version":1,"agent_type":"cc","command":"claude","omitted_env":["API_KEY"]}}`)), false, "API_KEY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workDir, logPath := setupRotationTmux(t)
			t.Setenv("ROTATION_ORIGINAL_PRESENT", "1")
			t.Setenv("ROTATION_LAUNCH_SPEC", tc.encoded)
			if tc.readErr {
				t.Setenv("ROTATION_SPEC_ERROR", "1")
			}
			spawner := NewDefaultPaneSpawner(config.Default())
			original := tmux.Pane{ID: "%1", PID: 123, Type: tmux.AgentClaude, Command: "node"}
			pane, err := spawner.SpawnReplacementContext(stdcontext.Background(), "test", original, 1, workDir)
			if err == nil || pane.ID != "" || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("replacement = %+v, %v; want %q", pane, err, tc.want)
			}
			commands, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(commands), "split-window") || strings.Contains(string(commands), "send-keys") {
				t.Fatalf("invalid launch metadata mutated tmux:\n%s", commands)
			}
		})
	}
}

func TestDefaultPaneSpawner_ObservesReadinessAndHandoffSubmission(t *testing.T) {
	for _, tc := range []struct {
		name       string
		afterShell bool
		wantErr    string
	}{
		{name: "working_after_submission"},
		{name: "agent_exits_during_delivery", afterShell: true, wantErr: "exited during handoff"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, logPath := setupRotationTmux(t)
			t.Setenv("ROTATION_RECORD_DELIVERY", "1")
			t.Setenv("ROTATION_READY_CAPTURE", rotationReadyClaudeScreen)
			t.Setenv("ROTATION_AFTER_CAPTURE", "Welcome to Claude Code\n✻ Sautéing… (ctrl+c to interrupt · 12s · thinking)\n────────────────────────\n❯ \n────────────────────────\n  ⏵⏵ bypass permissions on")
			if tc.afterShell {
				t.Setenv("ROTATION_AFTER_SHELL", "1")
			}
			for suffix, value := range map[string]string{".type": "cc", ".title": "test__cc_1", ".launched": "1"} {
				if err := os.WriteFile(logPath+suffix, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			spawner := NewDefaultPaneSpawner(config.Default())
			expected := tmux.Pane{ID: "%99", PID: 321, Type: tmux.AgentClaude, Command: "node"}
			err := spawner.DeliverHandoffContext(stdcontext.Background(), "test", expected, "Handoff context: continue the durable scheduler task.")
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("handoff error = %v, want %q", err, tc.wantErr)
			}
			commands, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(string(commands), "\n")
			capturesBeforePaste := 0
			for _, line := range lines {
				if strings.HasPrefix(line, "paste-buffer ") {
					break
				}
				if strings.HasPrefix(line, "capture-pane ") {
					capturesBeforePaste++
				}
			}
			if capturesBeforePaste < 2 || !strings.Contains(string(commands), "paste-buffer") || strings.Contains(string(commands), "kill-pane") {
				t.Fatalf("handoff skipped stable readiness or retired a pane directly:\n%s", commands)
			}
		})
	}
}

func TestConfirmRotationDefaultTransportPreservesSourceOnSummaryEcho(t *testing.T) {
	workDir, logPath := setupRotationTmux(t)
	t.Setenv("ROTATION_ORIGINAL_PRESENT", "1")
	t.Setenv("ROTATION_CAPTURE_REQUEST", "1")
	t.Setenv("ROTATION_READY_CAPTURE", rotationReadyClaudeScreen)
	const agentID = "test__cc_1_architect"
	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent(agentID, "%1", "claude-opus-4")
	monitor.RecordMessage(agentID, 1000, 1000)
	original := *monitor.GetState(agentID)
	cfg := config.DefaultContextRotationConfig()
	cfg.TryCompactFirst = false
	r := NewRotator(RotatorConfig{
		Monitor: monitor, Spawner: NewDefaultPaneSpawner(config.Default()), Config: cfg,
		Summary: NewSummaryGenerator(SummaryGeneratorConfig{PromptTimeout: 30 * time.Millisecond}),
	})
	r.EnqueuePendingRotation("test", agentID, "%1", 95, workDir)
	result := r.ConfirmRotationContext(stdcontext.Background(), agentID, ConfirmRotate, 0)
	if result.Success || !strings.Contains(result.Error, "complete handoff summary") || !strings.Contains(result.Error, "original agent preserved") {
		t.Fatalf("summary echo result = %+v", result)
	}
	if current := monitor.GetState(agentID); current == nil || *current != original {
		t.Fatalf("summary echo reset original monitor: %+v", current)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commands), "split-window") || strings.Contains(string(commands), "kill-pane") {
		t.Fatalf("summary echo replaced the source:\n%s", commands)
	}
}

func TestNativeCompactionRejectsProviderInAnotherSession(t *testing.T) {
	workDir, logPath := setupRotationTmux(t)
	t.Setenv("HOME", workDir)
	t.Setenv("ROTATION_ORIGINAL_PRESENT", "1")
	t.Setenv("ROTATION_GLOBAL_CONFLICT", "1")
	t.Setenv("ROTATION_CAPTURE", rotationReadyClaudeScreen)
	transcript := filepath.Join(workDir, ".claude", "projects", MungeProjectPath(workDir), "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte(`{"type":"assistant","message":{"model":"claude-opus-4","usage":{"input_tokens":150000}}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("test__cc_1_architect", "%1", "claude-opus-4")
	r := NewRotator(RotatorConfig{Monitor: monitor, Spawner: NewDefaultPaneSpawner(config.Default()), Config: config.DefaultContextRotationConfig()})
	result := r.tryCompactionContext(t.Context(), "test", "test__cc_1_architect", "%1", tmux.AgentClaude)
	if result == nil || result.Success || !strings.Contains(result.Error, "ambiguous across tmux sessions") {
		t.Fatalf("cross-session attribution = %+v", result)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(commands), "list-panes -a") || strings.Contains(string(commands), "send-keys") || strings.Contains(string(commands), "paste-buffer") {
		t.Fatalf("ambiguous transcript allowed compaction input:\n%s", commands)
	}
}

func TestDefaultPaneSpawner_RejectsInvalidLaunchBeforePaneCreation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		variant string
		wantErr string
	}{
		{"invalid_template", "codex {{.Missing", "", "preparing replacement agent"},
		{"dropped_model", "codex --default", "private-model", "model override"},
		{"dropped_effort", "codex -m {{shellQuote .Model}}", "private-model@high", "reasoning effort"},
		{"empty_template", "{{if false}}codex{{end}}", "", "rendered empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workDir, logPath := setupRotationTmux(t)
			cfg := config.Default()
			cfg.Agents.Codex = tc.command
			spawner := NewDefaultPaneSpawner(cfg)
			paneID, err := spawner.SpawnAgent("test", "codex", 1, tc.variant, workDir)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("SpawnAgent() = %q, %v, want error containing %q", paneID, err, tc.wantErr)
			}
			if _, err := os.Stat(logPath); !os.IsNotExist(err) {
				t.Fatalf("tmux was invoked before invalid launch was rejected: %v", err)
			}
		})
	}
}

func TestDefaultPaneSpawner_RejectsAmbiguousPersonaVariant(t *testing.T) {
	for _, tc := range []struct {
		name      string
		agentType string
		variant   string
		alias     string
		model     string
	}{
		{name: "default_architect_alias", agentType: "claude", variant: "architect"},
		{name: "custom_alias", agentType: "codex", variant: "rotation-reviewer", alias: "rotation-reviewer", model: "selected-model"},
		{name: "custom_full_model", agentType: "codex", variant: "rotation-reviewer", alias: "custom", model: "rotation-reviewer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workDir, logPath := setupRotationTmux(t)
			cfg := config.Default()
			if tc.alias != "" {
				cfg.Models.Codex[tc.alias] = tc.model
				if err := os.MkdirAll(filepath.Join(workDir, ".ntm"), 0755); err != nil {
					t.Fatal(err)
				}
				const conflictingPersona = `[[personas]]
name = "rotation-reviewer"
agent_type = "codex"
model = "different-persona-model"
system_prompt = "This prompt must not be injected into a model-only agent."
`
				if err := os.WriteFile(filepath.Join(workDir, ".ntm", "personas.toml"), []byte(conflictingPersona), 0644); err != nil {
					t.Fatal(err)
				}
			}
			spawner := NewDefaultPaneSpawner(cfg)
			paneID, err := spawner.SpawnAgent("test", tc.agentType, 1, tc.variant, workDir)
			if err == nil || !strings.Contains(err.Error(), "ambiguous replacement variant") {
				t.Fatalf("SpawnAgent() = %q, %v, want explicit ambiguity error", paneID, err)
			}
			if _, err := os.Stat(logPath); !os.IsNotExist(err) {
				t.Fatalf("tmux was invoked for an ambiguous launch selection: %v", err)
			}
		})
	}
}

// Exercise the production PaneSpawner against a recording tmux executable so
// tests cover the command actually sent to the pane, including preflight order.
func setupRotationTmux(t *testing.T) (workDir, logPath string) {
	t.Helper()
	workDir = t.TempDir()
	originalPending, originalHistory := DefaultPendingRotationStore, DefaultRotationHistoryStore
	DefaultPendingRotationStore = NewPendingRotationStoreWithPath(filepath.Join(workDir, "pending.jsonl"))
	DefaultRotationHistoryStore = NewRotationHistoryStoreWithPath(filepath.Join(workDir, "rotations.jsonl"))
	t.Cleanup(func() {
		DefaultPendingRotationStore, DefaultRotationHistoryStore = originalPending, originalHistory
	})
	logPath = filepath.Join(workDir, "tmux.log")
	stub := filepath.Join(workDir, "tmux")
	const script = `#!/bin/sh
printf '%s\n' "$*" >> "$ROTATION_TMUX_LOG"
case "$1" in
  list-windows) printf '0\n' ;;
  split-window) printf '%%99\n' ;;
  display-message)
    case "$*" in
      *'#{pane_current_path}'*) printf '%s\n' "$ROTATION_PREDECESSOR_CWD" ;;
      *) printf '0\n' ;;
    esac
    ;;
  select-pane)
    for value do last="$value"; done
    printf '%s' "$last" > "$ROTATION_TMUX_LOG.title"
    ;;
  set-option)
    case "$*" in
      *'@ntm_agent_type '*)
        for value do last="$value"; done
        printf '%s' "$last" > "$ROTATION_TMUX_LOG.type"
        ;;
    esac
    ;;
  list-panes)
    prefix=
    case " $* " in *' -a '*) prefix=test_NTM_SEP_ ;; esac
    if [ "$ROTATION_ORIGINAL_PRESENT" = 1 ]; then
      printf '%s%%1_NTM_SEP_1_NTM_SEP_test__cc_1_architect_NTM_SEP_node_NTM_SEP_120_NTM_SEP_40_NTM_SEP_0_NTM_SEP_123_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0\n' "$prefix"
    fi
    if [ -n "$prefix" ] && [ "$ROTATION_GLOBAL_CONFLICT" = 1 ]; then
      printf 'other_NTM_SEP_%%2_NTM_SEP_1_NTM_SEP_other__cc_1_NTM_SEP_node_NTM_SEP_120_NTM_SEP_40_NTM_SEP_0_NTM_SEP_456_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0\n'
    fi
    if [ -f "$ROTATION_TMUX_LOG.type" ]; then
      agent_type=$(cat "$ROTATION_TMUX_LOG.type")
      title=$(cat "$ROTATION_TMUX_LOG.title")
      command=bash
      if [ -f "$ROTATION_TMUX_LOG.launched" ]; then command=node; fi
      if [ "$ROTATION_AFTER_SHELL" = 1 ] && [ -f "$ROTATION_TMUX_LOG.handoff" ]; then command=bash; fi
      printf '%s%%99_NTM_SEP_0_NTM_SEP_%s_NTM_SEP_%s_NTM_SEP_120_NTM_SEP_40_NTM_SEP_0_NTM_SEP_321_NTM_SEP_0_NTM_SEP_%s_NTM_SEP__NTM_SEP__NTM_SEP_0\n' "$prefix" "$title" "$command" "$agent_type"
    fi
    ;;
  capture-pane)
    if [ "$ROTATION_CAPTURE_REQUEST" = 1 ]; then
      if [ -f "$ROTATION_TMUX_LOG.buffer" ]; then
        cat "$ROTATION_TMUX_LOG.buffer"
      else
        printf '%s\n' "$ROTATION_READY_CAPTURE"
      fi
    elif [ "$ROTATION_RECORD_DELIVERY" = 1 ]; then
      if [ -f "$ROTATION_TMUX_LOG.handoff" ]; then
        printf '%s\n' "$ROTATION_AFTER_CAPTURE"
      else
        printf '%s\n' "$ROTATION_READY_CAPTURE"
      fi
    else
      printf '%s\n' "$ROTATION_CAPTURE"
    fi
    ;;
  load-buffer) cat > "$ROTATION_TMUX_LOG.buffer" ;;
  paste-buffer)
    if [ "$ROTATION_RECORD_DELIVERY" = 1 ]; then printf '1' > "$ROTATION_TMUX_LOG.handoff"; fi
    ;;
  show-options)
    if [ "$ROTATION_SPEC_ERROR" = 1 ]; then exit 1; fi
    if [ -z "$ROTATION_LAUNCH_SPEC" ]; then
      printf 'invalid option: @ntm_agent_launch\n' >&2
      exit 1
    fi
    printf '%s\n' "$ROTATION_LAUNCH_SPEC"
    ;;
esac
`
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", stub)
	t.Setenv("ROTATION_TMUX_LOG", logPath)
	t.Setenv("ROTATION_PREDECESSOR_CWD", workDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(workDir, "config"))
	return workDir, logPath
}

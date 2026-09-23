package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/auth"
	"github.com/Dicklesworthstone/ntm/internal/config"
	ctxmon "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/quota"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Exercise the public command against the actual rotation engine and recording
// tmux transport. The default coordinator policy requires manual confirmation;
// successful JSON must correspond to a submitted operation, not dequeuing it.
func TestContextConfirmCommandExecutesPersistedChoice(t *testing.T) {
	for _, mode := range []string{"rotate", "compact", "launch-failure", "changed-pid", "busy", "canceled", "cross-session"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			workDir := filepath.Join(root, "project")
			if err := os.MkdirAll(workDir, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", root)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
			t.Setenv("NTM_CONFIRM_ROOT", root)
			t.Setenv("NTM_CONFIRM_WORKDIR", workDir)
			t.Setenv("NTM_CONFIRM_MODE", mode)
			stub := filepath.Join(root, "tmux")
			if err := os.WriteFile(stub, []byte(contextConfirmTmuxFixture), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NTM_TMUX_BINARY", stub)
			transcript := filepath.Join(root, ".claude", "projects", ctxmon.MungeProjectPath(workDir), "agent.jsonl")
			if err := os.MkdirAll(filepath.Dir(transcript), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(transcript, []byte("{\"type\":\"assistant\",\"message\":{\"model\":\"claude-opus-4\",\"usage\":{\"input_tokens\":150000}}}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			past := time.Now().Add(-time.Second)
			if err := os.Chtimes(transcript, past, past); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NTM_CONFIRM_TRANSCRIPT", transcript)
			oldCfg, oldJSON, oldClient := cfg, jsonOutput, tmux.DefaultClient
			oldStore, oldHistory := ctxmon.DefaultPendingRotationStore, ctxmon.DefaultRotationHistoryStore
			cfg, jsonOutput, tmux.DefaultClient = config.Default(), true, tmux.NewClient("")
			if cfg.Rotation.AutoConfirm {
				t.Fatal("fixture must exercise manual confirmation with default auto-confirm disabled")
			}
			if !cfg.ContextRotation.TryCompactFirst {
				t.Fatal("fixture must preserve the default automatic compaction preference")
			}
			ctxmon.DefaultPendingRotationStore = ctxmon.NewPendingRotationStoreWithPath(filepath.Join(root, "pending.jsonl"))
			ctxmon.DefaultRotationHistoryStore = ctxmon.NewRotationHistoryStore()
			t.Cleanup(func() {
				cfg, jsonOutput, tmux.DefaultClient = oldCfg, oldJSON, oldClient
				ctxmon.DefaultPendingRotationStore, ctxmon.DefaultRotationHistoryStore = oldStore, oldHistory
			})
			pending := &ctxmon.PendingRotation{AgentID: "demo__cc_1", SessionName: "demo", PaneID: "%1", PanePID: 123, PaneType: "cc", WorkDir: workDir, ContextPercent: 75, CreatedAt: time.Now(), TimeoutAt: time.Now().Add(time.Hour), DefaultAction: ctxmon.ConfirmRotate}
			if err := ctxmon.AddPendingRotation(pending); err != nil {
				t.Fatal(err)
			}
			action := "rotate"
			if mode == "compact" || mode == "cross-session" {
				action = "compact"
			}
			run := func(ctx context.Context) (ConfirmRotationResult, error) {
				command := newRotateContextConfirmCmd()
				// Match the production root policy when exercising the command
				// directly: failures retain one parseable JSON outcome.
				command.SilenceUsage, command.SilenceErrors = rootCmd.SilenceUsage, rootCmd.SilenceErrors
				var out bytes.Buffer
				command.SetOut(&out)
				command.SetErr(new(bytes.Buffer))
				command.SetArgs([]string{pending.AgentID, "--action=" + action})
				err := command.ExecuteContext(ctx)
				var result ConfirmRotationResult
				if decodeErr := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &result); decodeErr != nil {
					t.Fatalf("confirmation did not emit structured outcome: %v, command error=%v, output=%q", decodeErr, err, out.String())
				}
				return result, err
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			result, err := run(ctx)
			commands, _ := os.ReadFile(filepath.Join(root, "commands"))
			log := string(commands)
			wantSuccess := mode == "rotate" || mode == "compact"
			if wantSuccess != result.Success || wantSuccess && err != nil || !wantSuccess && err == nil {
				t.Fatalf("confirmation success=%v, err=%v, result=%+v\n%s", wantSuccess, err, result, log)
			}
			stored, getErr := ctxmon.GetPendingRotationByID(pending.AgentID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			if wantSuccess {
				if stored != nil || result.Rotation == nil {
					t.Fatalf("successful operation did not persist acknowledgment: %+v / %+v", result, stored)
				}
				if mode == "rotate" {
					if strings.Contains(log, "/compact") {
						t.Fatalf("explicit rotation was replaced by automatic compaction:\n%s", log)
					}
					payload, readErr := os.ReadFile(filepath.Join(root, "delivered"))
					if readErr != nil || !strings.Contains(string(payload), "Continue durable scheduler implementation") || result.Rotation.NewPaneID != "%99" {
						t.Fatalf("replacement did not receive source handoff: %+v, %v, %q", result, readErr, payload)
					}
					deliveredAt, retiredAt := strings.Index(log, "HANDOFF_DELIVERED"), strings.Index(log, "kill-pane -t %1")
					if deliveredAt < 0 || retiredAt < 0 || deliveredAt > retiredAt {
						t.Fatalf("source retired before handoff submission:\n%s", log)
					}
				} else if !strings.Contains(log, "/compact") || strings.Contains(log, "/clear") || strings.Contains(log, "split-window") || strings.Contains(log, "kill-pane") {
					t.Fatalf("compact failed to preserve native context:\n%s", log)
				}
				before := len(commands)
				replayed, replayErr := run(t.Context())
				after, _ := os.ReadFile(filepath.Join(root, "commands"))
				if replayErr != nil || !replayed.Success || len(after) != before {
					t.Fatalf("repeated confirmation executed again: %+v, %v\n%s", replayed, replayErr, after)
				}
			} else {
				if stored == nil {
					t.Fatal("failed confirmation consumed request")
				}
				if mode == "launch-failure" && (!strings.Contains(result.Message, "failed to spawn replacement") || !strings.Contains(log, "split-window") || strings.Contains(log, "HANDOFF_DELIVERED") || strings.Contains(log, "kill-pane")) {
					t.Fatalf("launch failure did not preserve the summarized predecessor: %+v\n%s", result, log)
				}
				if mode == "canceled" && (stored.SelectedAction != "" || len(commands) != 0) {
					t.Fatalf("pre-canceled command changed state: %+v\n%s", stored, commands)
				}
				if mode != "launch-failure" && (strings.Contains(log, "send-keys") || strings.Contains(log, "paste-buffer") || strings.Contains(log, "kill-pane")) {
					t.Fatalf("unsafe or canceled confirmation delivered input:\n%s", log)
				}
			}
		})
	}
}

const contextConfirmTmuxFixture = `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_CONFIRM_ROOT/commands"
target=
previous=
for value do
  if [ "$previous" = -t ]; then target="$value"; fi
  previous="$value"
done
ready() { printf 'Welcome to Claude Code\n────────────────────────\n❯ \n────────────────────────\n  ⏵⏵ bypass permissions on (shift+tab to cycle)\n'; }
case "$1" in
  list-windows) printf '0\n' ;;
  has-session) exit 0 ;;
  display-message) printf '%s\n' "$NTM_CONFIRM_WORKDIR" ;;
  list-panes)
    prefix=
    case "$*" in *' -a '*) prefix=demo_NTM_SEP_ ;; esac
    if [ ! -f "$NTM_CONFIRM_ROOT/retired" ]; then
      pid=123
      if [ "$NTM_CONFIRM_MODE" = changed-pid ]; then pid=456; fi
      printf '%s%%1_NTM_SEP_1_NTM_SEP_demo__cc_1_NTM_SEP_node_NTM_SEP_120_NTM_SEP_40_NTM_SEP_0_NTM_SEP_%s_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0\n' "$prefix" "$pid"
    fi
    if [ -f "$NTM_CONFIRM_ROOT/created" ]; then
      command=bash
      if [ -f "$NTM_CONFIRM_ROOT/launched" ]; then command=node; fi
      printf '%s%%99_NTM_SEP_2_NTM_SEP_demo__cc_1_NTM_SEP_%s_NTM_SEP_120_NTM_SEP_40_NTM_SEP_0_NTM_SEP_321_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0\n' "$prefix" "$command"
    fi
    if [ -n "$prefix" ] && [ "$NTM_CONFIRM_MODE" = cross-session ]; then
      printf 'other_NTM_SEP_%%2_NTM_SEP_1_NTM_SEP_other__cc_1_NTM_SEP_node_NTM_SEP_120_NTM_SEP_40_NTM_SEP_0_NTM_SEP_456_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0\n'
    fi
    ;;
  capture-pane)
    if [ "$NTM_CONFIRM_MODE" = busy ]; then
      printf 'Welcome to Claude Code\n✻ Sautéing… (ctrl+c to interrupt · 12s · thinking)\n────────────────────────\n❯ \n────────────────────────\n  ⏵⏵ bypass permissions on\n'
    elif [ "$target" = '%99' ] && [ -f "$NTM_CONFIRM_ROOT/delivered" ]; then
      printf 'Welcome to Claude Code\n✻ Sautéing… (ctrl+c to interrupt · 12s · thinking)\n────────────────────────\n❯ \n────────────────────────\n  ⏵⏵ bypass permissions on\n'
    else
      if [ "$target" = '%1' ] && [ -f "$NTM_CONFIRM_ROOT/summary" ]; then cat "$NTM_CONFIRM_ROOT/summary"; fi
      ready
    fi
    ;;
  load-buffer) cat > "$NTM_CONFIRM_ROOT/buffer" ;;
  paste-buffer)
    if [ "$target" = '%1' ]; then
      start_marker=$(sed -n 's/.*\(NTM_START_[A-Za-z0-9]*\).*/\1/p' "$NTM_CONFIRM_ROOT/buffer")
      end_marker=$(sed -n 's/.*\(NTM_END_[A-Za-z0-9]*\).*/\1/p' "$NTM_CONFIRM_ROOT/buffer")
      printf '%s\n' "$start_marker" > "$NTM_CONFIRM_ROOT/summary"
      printf '## Current Task\nContinue durable scheduler implementation\n## Progress\nScheduling state is persisted.\n## Next Steps\nAdd recovery tests.\n' >> "$NTM_CONFIRM_ROOT/summary"
      printf '%s\n' "$end_marker" >> "$NTM_CONFIRM_ROOT/summary"
    else
      cat "$NTM_CONFIRM_ROOT/buffer" > "$NTM_CONFIRM_ROOT/delivered"
      printf 'HANDOFF_DELIVERED\n' >> "$NTM_CONFIRM_ROOT/commands"
    fi
    ;;
  show-options) printf 'invalid option: @ntm_agent_launch\n' >&2; exit 1 ;;
  split-window)
    if [ "$NTM_CONFIRM_MODE" = launch-failure ]; then exit 1; fi
    printf '1' > "$NTM_CONFIRM_ROOT/created"
    printf '%%99\n'
    ;;
  send-keys)
    case "$*" in
      *'/compact'*) printf '{"type":"assistant","message":{"model":"claude-opus-4","usage":{"input_tokens":30000}}}\n' > "$NTM_CONFIRM_TRANSCRIPT" ;;
    esac
    if [ "$target" = '%99' ]; then printf '1' > "$NTM_CONFIRM_ROOT/launched"; fi
    ;;
  kill-pane) if [ "$target" = '%1' ]; then printf '1' > "$NTM_CONFIRM_ROOT/retired"; fi ;;
esac
`

type rotateTestQuotaFetcher struct {
	info *quota.QuotaInfo
	err  error
}

func (f rotateTestQuotaFetcher) FetchQuota(context.Context, string, quota.Provider) (*quota.QuotaInfo, error) {
	return f.info, f.err
}

type rotateTestOrchestrator struct {
	terminateErr error
	waitErr      error
	startErr     error
	terminated   int
	waited       int
	started      int
}

func (o *rotateTestOrchestrator) TerminateSession(string, string) error {
	o.terminated++
	return o.terminateErr
}

func (o *rotateTestOrchestrator) WaitForShellPrompt(string, time.Duration) error {
	o.waited++
	return o.waitErr
}

func (o *rotateTestOrchestrator) StartNewAgentSession(auth.RestartContext) error {
	o.started++
	return o.startErr
}

func TestRotateCmdValidation(t *testing.T) {
	tests := []struct {
		name                     string
		args                     []string
		flags                    map[string]string
		wantError                string
		wantErrorAny             []string
		skipIfAutoSelectPossible bool // Skip if exactly one session is running (auto-select applies)
	}{
		{
			name:                     "missing session and not in tmux",
			args:                     []string{},
			wantError:                "session",
			skipIfAutoSelectPossible: true, // Session auto-selected when only one exists
		},
		{
			name: "missing pane index",
			args: []string{"mysession"},
			wantErrorAny: []string{
				"pane index required",
				"session", // session may not exist in shared tmux environment
			},
		},
		{
			name: "dry run requires valid session/pane",
			args: []string{"mysession"},
			flags: map[string]string{
				"pane":    "0",
				"dry-run": "true",
			},
			// Dry run still needs to look up pane info, which fails without tmux
			wantErrorAny: []string{
				"getting panes",
				"session", // session may not exist in shared tmux environment
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Change to a temp dir to prevent CWD-based session inference
			tmpDir := t.TempDir()
			oldWd, _ := os.Getwd()
			if err := os.Chdir(tmpDir); err != nil {
				t.Fatalf("chdir failed: %v", err)
			}
			defer os.Chdir(oldWd)

			// Unset TMUX env var to prevent auto-detection from environment
			oldTmux := os.Getenv("TMUX")
			os.Unsetenv("TMUX")
			defer os.Setenv("TMUX", oldTmux)

			if tt.skipIfAutoSelectPossible && sessionAutoSelectPossible() {
				t.Skip("Skipping: exactly one tmux session running (auto-selection applies)")
			}

			cmd := newRotateCmd()
			// Redirect output to buffer to ensure non-interactive mode
			var buf bytes.Buffer
			cmd.SetOut(&buf)
			cmd.SetErr(&buf)

			// Set args
			if len(tt.args) > 0 {
				cmd.SetArgs(tt.args)
			} else {
				cmd.SetArgs([]string{})
			}

			// Set flags
			for k, v := range tt.flags {
				_ = cmd.Flags().Set(k, v)
			}

			// Execute
			err := cmd.Execute()

			if tt.wantError != "" || len(tt.wantErrorAny) > 0 {
				if err == nil {
					if tt.wantError != "" {
						t.Errorf("expected error containing %q, got nil", tt.wantError)
					} else {
						t.Errorf("expected error containing one of %q, got nil", tt.wantErrorAny)
					}
				} else if !errorMatchesAny(err.Error(), append(tt.wantErrorAny, tt.wantError)) {
					if tt.wantError != "" {
						t.Errorf("expected error containing %q, got %q", tt.wantError, err.Error())
					} else {
						t.Errorf("expected error containing one of %q, got %q", tt.wantErrorAny, err.Error())
					}
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func errorMatchesAny(err string, matches []string) bool {
	for _, match := range matches {
		if match == "" {
			continue
		}
		if strings.Contains(err, match) {
			return true
		}
	}
	return false
}

func TestQuotaProviderForAgentType_CanonicalizesAliases(t *testing.T) {

	tests := []struct {
		name      string
		agentType tmux.AgentType
		want      quota.Provider
		ok        bool
	}{
		{name: "claude alias", agentType: tmux.AgentType("claude_code"), want: quota.ProviderClaude, ok: true},
		{name: "codex alias", agentType: tmux.AgentType("openai-codex"), want: quota.ProviderCodex, ok: true},
		{name: "gemini alias", agentType: tmux.AgentType("google-gemini"), want: quota.ProviderGemini, ok: true},
		{name: "omp is reported, not skipped", agentType: tmux.AgentType("oh-my-pi"), want: quota.ProviderOmp, ok: true},
		{name: "unsupported cursor", agentType: tmux.AgentCursor, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := quotaProviderForAgentType(tt.agentType)
			if ok != tt.ok {
				t.Fatalf("quotaProviderForAgentType(%q) ok = %v, want %v", tt.agentType, ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("quotaProviderForAgentType(%q) = %q, want %q", tt.agentType, got, tt.want)
			}
		})
	}
}

func TestNormalizedProviderName_CanonicalizesFallbacks(t *testing.T) {

	tests := []struct {
		name      string
		agentType tmux.AgentType
		want      string
	}{
		{name: "claude alias", agentType: tmux.AgentType("claude_code"), want: "claude"},
		{name: "codex alias", agentType: tmux.AgentType("openai-codex"), want: "codex"},
		{name: "gemini alias", agentType: tmux.AgentType("google-gemini"), want: "gemini"},
		{name: "windsurf short alias", agentType: tmux.AgentType("ws"), want: "windsurf"},
		{name: "unknown falls back raw", agentType: tmux.AgentType("mystery"), want: "mystery"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizedProviderName(tt.agentType); got != tt.want {
				t.Fatalf("normalizedProviderName(%q) = %q, want %q", tt.agentType, got, tt.want)
			}
		})
	}
}

func TestResolveRotationProjectDirRejectsWorkspaceFallbackForExplicitSession(t *testing.T) {
	isolateSessionAgentStorage(t)
	session := "ntm-rotate-explicit-missing-project-test"

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

	_, err := resolveRotationProjectDir(t.Context(), session, false)
	if err == nil {
		t.Fatal("expected missing session project error")
	}
	if !strings.Contains(err.Error(), "getting project root failed") {
		t.Fatalf("expected project root error, got %v", err)
	}
}

func TestResolveRotationProjectDirAllowsWorkspaceFallbackForInferredSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := canonicalTempDir(t)
	cfg = &config.Config{ProjectsBase: projectsBase}

	cwdRepo := canonicalTempDir(t)
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatal(err)
	}

	got, err := resolveRotationProjectDir(t.Context(), "ntm", true)
	if err != nil {
		t.Fatalf("resolveRotationProjectDir() error = %v", err)
	}
	if got != cwdRepo {
		t.Fatalf("resolveRotationProjectDir() = %q, want %q", got, cwdRepo)
	}
}

func TestRotateAllLimitedStopsBeforePromptWhenTerminationFails(t *testing.T) {
	previousGetPanes := rotateGetPanes
	previousQuotaFetcher := newRotateQuotaFetcher
	previousOrchestrator := newRotateOrchestrator
	t.Cleanup(func() {
		rotateGetPanes = previousGetPanes
		newRotateQuotaFetcher = previousQuotaFetcher
		newRotateOrchestrator = previousOrchestrator
	})

	rotateGetPanes = func(string) ([]tmux.Pane, error) {
		return []tmux.Pane{{ID: "%11", Index: 1, Title: "limited", Type: tmux.AgentClaude}}, nil
	}
	newRotateQuotaFetcher = func() quota.Fetcher {
		return rotateTestQuotaFetcher{info: &quota.QuotaInfo{IsLimited: true}}
	}
	orch := &rotateTestOrchestrator{terminateErr: errors.New("interrupt failed")}
	newRotateOrchestrator = func(*config.Config) rotationOrchestrator { return orch }

	err := rotateAllLimited(t.Context(), "proj", "backup@example.com", false, false)
	if err == nil || !strings.Contains(err.Error(), "terminate limited pane 1 (%11)") {
		t.Fatalf("rotateAllLimited() error = %v, want termination context", err)
	}
	if orch.terminated != 1 || orch.waited != 0 || orch.started != 0 {
		t.Fatalf("orchestrator calls = terminate:%d wait:%d start:%d, want 1:0:0", orch.terminated, orch.waited, orch.started)
	}
}

func TestRotateAllLimitedStopsBeforePromptWhenShellIsNotReady(t *testing.T) {
	previousGetPanes := rotateGetPanes
	previousQuotaFetcher := newRotateQuotaFetcher
	previousOrchestrator := newRotateOrchestrator
	t.Cleanup(func() {
		rotateGetPanes = previousGetPanes
		newRotateQuotaFetcher = previousQuotaFetcher
		newRotateOrchestrator = previousOrchestrator
	})

	rotateGetPanes = func(string) ([]tmux.Pane, error) {
		return []tmux.Pane{{ID: "%12", Index: 2, Title: "limited", Type: tmux.AgentCodex}}, nil
	}
	newRotateQuotaFetcher = func() quota.Fetcher {
		return rotateTestQuotaFetcher{info: &quota.QuotaInfo{IsLimited: true}}
	}
	orch := &rotateTestOrchestrator{waitErr: errors.New("shell still running")}
	newRotateOrchestrator = func(*config.Config) rotationOrchestrator { return orch }

	err := rotateAllLimited(t.Context(), "proj", "backup@example.com", false, false)
	if err == nil || !strings.Contains(err.Error(), "wait for shell prompt in limited pane 2 (%12)") {
		t.Fatalf("rotateAllLimited() error = %v, want shell-readiness context", err)
	}
	if orch.terminated != 1 || orch.waited != 1 || orch.started != 0 {
		t.Fatalf("orchestrator calls = terminate:%d wait:%d start:%d, want 1:1:0", orch.terminated, orch.waited, orch.started)
	}
}

func TestRotateAllLimitedReportsRestartFailure(t *testing.T) {
	isolateSessionAgentStorage(t)

	previousGetPanes := rotateGetPanes
	previousQuotaFetcher := newRotateQuotaFetcher
	previousOrchestrator := newRotateOrchestrator
	previousCfg := cfg
	previousStdin := os.Stdin
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		rotateGetPanes = previousGetPanes
		newRotateQuotaFetcher = previousQuotaFetcher
		newRotateOrchestrator = previousOrchestrator
		cfg = previousCfg
		os.Stdin = previousStdin
		if err := os.Chdir(previousDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	workspace := canonicalTempDir(t)
	if err := os.MkdirAll(filepath.Join(workspace, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatal(err)
	}
	cfg = &config.Config{ProjectsBase: canonicalTempDir(t)}

	stdin, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdin = stdin
	t.Cleanup(func() { _ = stdin.Close() })

	restartErr := errors.New("relaunch failed")
	rotateGetPanes = func(string) ([]tmux.Pane, error) {
		return []tmux.Pane{
			{ID: "%13", Index: 3, Title: "limited claude", Type: tmux.AgentClaude},
			{ID: "%14", Index: 4, Title: "limited codex", Type: tmux.AgentCodex},
		}, nil
	}
	newRotateQuotaFetcher = func() quota.Fetcher {
		return rotateTestQuotaFetcher{info: &quota.QuotaInfo{IsLimited: true}}
	}
	orch := &rotateTestOrchestrator{startErr: restartErr}
	newRotateOrchestrator = func(*config.Config) rotationOrchestrator { return orch }

	err = rotateAllLimited(t.Context(), "proj", "backup@example.com", false, true)
	if !errors.Is(err, restartErr) {
		t.Fatalf("rotateAllLimited() error = %v, want wrapped %v", err, restartErr)
	}
	if orch.terminated != 2 || orch.waited != 2 || orch.started != 2 {
		t.Fatalf("orchestrator calls = terminate:%d wait:%d start:%d, want 2:2:2", orch.terminated, orch.waited, orch.started)
	}
}

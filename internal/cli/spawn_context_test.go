package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/output"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestSpawnLifecycleContextCancellationShortCircuitsOperations(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := preflightOllamaSpawnContext(ctx, SpawnOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ollama preflight error = %v, want cancellation", err)
	}
	if err := preflightCodexAccountSupportContext(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Codex preflight error = %v, want cancellation", err)
	}
	if _, err := ResolveCassContextWithContext(ctx, "query", t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("CASS context error = %v, want cancellation", err)
	}
	if err := spawnSessionLogicContext(ctx, SpawnOptions{Session: "canceled-spawn"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("spawn lifecycle error = %v, want cancellation before side effects", err)
	}
}

func TestSpawnLifecycleComposableCancellationDoesNotEmit(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	stdout, err := captureStdout(t, func() error {
		return spawnSessionLogicComposable(ctx, SpawnOptions{Session: "silent-canceled-spawn"})
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("composable spawn error = %v, want cancellation", err)
	}
	if stdout != "" {
		t.Fatalf("composable spawn wrote stdout %q", stdout)
	}
}

func TestNewSpawnContext(t *testing.T) {
	ctx := NewSpawnContext(4)

	if ctx.TotalAgents != 4 {
		t.Errorf("expected TotalAgents=4, got %d", ctx.TotalAgents)
	}
	if ctx.BatchID == "" {
		t.Error("expected non-empty BatchID")
	}
	if !strings.HasPrefix(ctx.BatchID, "spawn-") {
		t.Errorf("expected BatchID to start with 'spawn-', got %s", ctx.BatchID)
	}
	if ctx.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
}

func TestSpawnContextForAgent(t *testing.T) {
	ctx := NewSpawnContext(3)

	agentCtx := ctx.ForAgent(2, 5*time.Second)

	if agentCtx.Order != 2 {
		t.Errorf("expected Order=2, got %d", agentCtx.Order)
	}
	if agentCtx.StaggerDelay != 5*time.Second {
		t.Errorf("expected StaggerDelay=5s, got %v", agentCtx.StaggerDelay)
	}
	if agentCtx.TotalAgents != 3 {
		t.Errorf("expected TotalAgents=3, got %d", agentCtx.TotalAgents)
	}
	if agentCtx.BatchID != ctx.BatchID {
		t.Errorf("expected inherited BatchID %s, got %s", ctx.BatchID, agentCtx.BatchID)
	}
}

func TestAgentSpawnContextEnvVarPrefix(t *testing.T) {
	ctx := NewSpawnContext(3)
	agentCtx := ctx.ForAgent(1, 0)

	prefix := agentCtx.EnvVarPrefix()

	if !strings.HasPrefix(prefix, "NTM_SPAWN_ORDER=1 ") {
		t.Errorf("expected prefix to start with 'NTM_SPAWN_ORDER=1 ', got %s", prefix)
	}
	if !strings.Contains(prefix, "NTM_SPAWN_ORDER=1") {
		t.Errorf("expected prefix to contain NTM_SPAWN_ORDER=1, got %s", prefix)
	}
	if !strings.Contains(prefix, "NTM_SPAWN_TOTAL=3") {
		t.Errorf("expected prefix to contain NTM_SPAWN_TOTAL=3, got %s", prefix)
	}
	if !strings.Contains(prefix, "NTM_SPAWN_BATCH_ID=") {
		t.Errorf("expected prefix to contain NTM_SPAWN_BATCH_ID=, got %s", prefix)
	}
	if strings.Contains(prefix, ";") {
		t.Errorf("expected prefix not to contain ';' (to preserve `cd && cmd` semantics), got %s", prefix)
	}
	if !strings.HasSuffix(prefix, " ") {
		t.Errorf("expected prefix to end with a space, got %s", prefix)
	}
}

func TestAgentSpawnContextPromptAnnotation(t *testing.T) {
	ctx := NewSpawnContext(4)
	agentCtx := ctx.ForAgent(2, time.Second)

	annotation := agentCtx.PromptAnnotation()

	if !strings.HasPrefix(annotation, "[Spawn context:") {
		t.Errorf("expected annotation to start with '[Spawn context:', got %s", annotation)
	}
	if !strings.Contains(annotation, "Agent 2/4") {
		t.Errorf("expected annotation to contain 'Agent 2/4', got %s", annotation)
	}
	if !strings.Contains(annotation, ctx.BatchID) {
		t.Errorf("expected annotation to contain batch ID %s, got %s", ctx.BatchID, annotation)
	}
}

func TestAnnotatePrompt(t *testing.T) {
	ctx := NewSpawnContext(2)
	agentCtx := ctx.ForAgent(1, 0)

	tests := []struct {
		name              string
		prompt            string
		includeAnnotation bool
		expectAnnotation  bool
	}{
		{
			name:              "with annotation",
			prompt:            "Do the task",
			includeAnnotation: true,
			expectAnnotation:  true,
		},
		{
			name:              "without annotation",
			prompt:            "Do the task",
			includeAnnotation: false,
			expectAnnotation:  false,
		},
		{
			name:              "empty prompt with annotation",
			prompt:            "",
			includeAnnotation: true,
			expectAnnotation:  false, // Empty prompt returns empty
		},
		{
			name:              "empty prompt without annotation",
			prompt:            "",
			includeAnnotation: false,
			expectAnnotation:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := agentCtx.AnnotatePrompt(tt.prompt, tt.includeAnnotation)

			hasAnnotation := strings.Contains(result, "[Spawn context:")

			if hasAnnotation != tt.expectAnnotation {
				t.Errorf("expected annotation=%v, got %v (result: %s)", tt.expectAnnotation, hasAnnotation, result)
			}

			if tt.prompt != "" && tt.includeAnnotation {
				if !strings.Contains(result, tt.prompt) {
					t.Errorf("expected result to contain original prompt %q, got %s", tt.prompt, result)
				}
			}
		})
	}
}

func TestAnnotatePromptPreservesOriginal(t *testing.T) {
	ctx := NewSpawnContext(3)
	agentCtx := ctx.ForAgent(2, time.Second)

	original := "Implement the feature\nwith multiple lines"
	result := agentCtx.AnnotatePrompt(original, true)

	if !strings.Contains(result, original) {
		t.Errorf("expected result to contain original prompt, got %s", result)
	}
	if !strings.HasPrefix(result, "[Spawn context:") {
		t.Errorf("expected result to start with annotation, got %s", result)
	}
}

func TestGenerateBatchID(t *testing.T) {
	id1 := generateBatchID()
	id2 := generateBatchID()

	if id1 == id2 {
		t.Error("expected unique batch IDs, got duplicates")
	}

	if !strings.HasPrefix(id1, "spawn-") {
		t.Errorf("expected batch ID to start with 'spawn-', got %s", id1)
	}

	// Check format: spawn-YYYYMMDD-HHMMSS-XXXX
	parts := strings.Split(id1, "-")
	if len(parts) < 4 {
		t.Errorf("expected batch ID format spawn-date-time-random, got %s", id1)
	}
}

func TestSpawnContextFirstAndLastAgent(t *testing.T) {
	ctx := NewSpawnContext(5)

	// First agent
	first := ctx.ForAgent(1, 0)
	if first.Order != 1 {
		t.Errorf("expected first agent Order=1, got %d", first.Order)
	}

	// Last agent
	last := ctx.ForAgent(5, 4*time.Second)
	if last.Order != 5 {
		t.Errorf("expected last agent Order=5, got %d", last.Order)
	}

	// Both should have same batch ID
	if first.BatchID != last.BatchID {
		t.Errorf("expected same BatchID for all agents, got %s vs %s", first.BatchID, last.BatchID)
	}
}

func TestBuildSpawnPromptSequencePreservesContextOrdering(t *testing.T) {
	t.Run("user prompt combines cass after recovery", func(t *testing.T) {
		steps := buildSpawnPromptSequence("cass", "recovery", "user", 250*time.Millisecond)
		if len(steps) != 2 {
			t.Fatalf("steps = %#v, want recovery and combined user prompt", steps)
		}
		if steps[0].Kind != "recovery_context" || steps[0].Message != "recovery" {
			t.Fatalf("first step = %#v, want recovery", steps[0])
		}
		if steps[1].Kind != "user_prompt" || steps[1].Message != "cass\n\nuser" || steps[1].Delay != 250*time.Millisecond {
			t.Fatalf("second step = %#v, want delayed cass+user", steps[1])
		}
	})

	t.Run("context-only delivery keeps cass before recovery", func(t *testing.T) {
		steps := buildSpawnPromptSequence("cass", "recovery", "", time.Second)
		if len(steps) != 2 || steps[0].Kind != "cass_context" || steps[1].Kind != "recovery_context" {
			t.Fatalf("steps = %#v, want cass then recovery", steps)
		}
		if steps[0].Delay != 0 || steps[1].Delay != 0 {
			t.Fatalf("context-only delays = %v/%v, want zero", steps[0].Delay, steps[1].Delay)
		}
	})
}

// Only tmux's external process and observed readiness are simulated. These
// tests execute the Cobra spawn command, real Agent Mail HTTP client, project
// resolution, prompt assembly, redaction, and tmux delivery protocol.
func setupThreadSpawnCommand(t *testing.T) (project, session, transportDir string) {
	t.Helper()
	base := t.TempDir()
	project = filepath.Join(base, "thread-project")
	session = "thread-project--resume"
	transportDir = t.TempDir()
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("AGENT_MAIL_URL", "")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("NTM_THREAD_TEST_ROOT", transportDir)
	t.Setenv("NTM_THREAD_TEST_PROJECT", project)
	t.Setenv("NTM_THREAD_TEST_SESSION", session)
	tmuxScript := filepath.Join(t.TempDir(), "tmux")
	const script = `#!/bin/sh
set -eu
root="$NTM_THREAD_TEST_ROOT"
session="$NTM_THREAD_TEST_SESSION"
while [ "${1:-}" = "-L" ] || [ "${1:-}" = "-S" ] || [ "${1:-}" = "-f" ]; do shift 2; done
action="${1:-}"
shift
printf '%s\n' "$action $*" >> "$root/commands"
target='%91'
buffer=''
previous=''
literal=0
last=''
for arg do
  if [ "$previous" = '-t' ]; then target="$arg"; fi
  if [ "$previous" = '-b' ]; then buffer="$arg"; fi
  if [ "$arg" = '-l' ]; then literal=1; fi
  last="$arg"
  previous="$arg"
done
case "$action" in
  -V) printf 'tmux 3.4\n' ;;
  has-session)
    if [ ! -f "$root/created" ]; then printf "can't find session: %s\n" "$session" >&2; exit 1; fi ;;
  new-session) : > "$root/created" ;;
  list-windows) printf '0\n' ;;
  list-panes)
    for number in 1 2; do
      id=$((90 + number))
      index=$((number - 1))
      pid=$((4241 + number))
      current=''
      if [ -f "$root/launched-$id" ]; then current='claude'; fi
      printf '%%%s_NTM_SEP_%s_NTM_SEP_%s__cc_%s_NTM_SEP_%s_NTM_SEP_100_NTM_SEP_30_NTM_SEP_1_NTM_SEP_%s_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[]\n' "$id" "$index" "$session" "$number" "$current" "$pid"
    done ;;
  display-message)
    case "$last" in
      '#{pane_pid}') if [ "$target" = '%92' ]; then printf '4243\n'; else printf '4242\n'; fi ;;
      '#{pane_current_path}') printf '%s\n' "$NTM_THREAD_TEST_PROJECT" ;;
      '#{pane_id}') printf '%s\n' "$target" ;;
      '#{session_name}') printf '%s\n' "$session" ;;
      '#{pane_title}') printf '%s__cc_1\n' "$session" ;;
      '[#{@ntm_rotation_token}]') printf '[]\n' ;;
      *) printf '0\n' ;;
    esac ;;
  send-keys)
    if [ "$literal" = 1 ]; then
      printf '\n---delivery---\n%s\n' "$last" >> "$root/deliveries-${target#%}"
      : > "$root/launched-${target#%}"
    fi ;;
  capture-pane) printf 'Claude Code v0.0.0\n❯ \n' ;;
  load-buffer) cat > "$root/buffer-$buffer" ;;
  paste-buffer) cat "$root/buffer-$buffer" >> "$root/deliveries-${target#%}" ;;
  set-option|set-window-option|set-environment|select-pane|select-layout|resize-pane|show-options|show-option|show-environment|delete-buffer) ;;
  *) printf 'unsupported test tmux action: %s\n' "$action" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(tmuxScript, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", tmuxScript)
	oldCfg, oldJSON, oldClient, oldObserver := cfg, jsonOutput, tmux.DefaultClient, newSpawnSessionObserver
	t.Cleanup(func() {
		cfg, jsonOutput, tmux.DefaultClient, newSpawnSessionObserver = oldCfg, oldJSON, oldClient, oldObserver
	})
	cfg = newTmuxIntegrationTestConfig(base)
	cfg.AgentMail.Enabled = true
	cfg.AgentMail.AutoRegister = false
	cfg.Redaction.Mode = "redact"
	jsonOutput = true
	tmux.DefaultClient = tmux.NewClient("")
	newSpawnSessionObserver = func() spawnSessionObserver {
		now := time.Now()
		panes := make([]statuspkg.PaneObservation, 0, 2)
		for number := 1; number <= 2; number++ {
			pane := tmux.Pane{ID: fmt.Sprintf("%%%d", 90+number), Index: number - 1, WindowIndex: 0,
				Title: fmt.Sprintf("%s__cc_%d", session, number), Type: tmux.AgentClaude, Command: "claude", PID: 4241 + number}
			panes = append(panes, testSpawnPaneObservation(now, pane, statuspkg.StateIdle))
		}
		observation := testSpawnSessionObservation(now, panes...)
		observation.Session = session
		return &scriptedSpawnObserver{observations: []statuspkg.SessionObservation{observation}}
	}
	return project, session, transportDir
}

func TestSpawnCommandThreadContinuation(t *testing.T) {
	for _, mode := range []string{"thread-only", "prompt-and-marching-orders", "authenticated", "authenticated-symlink"} {
		t.Run(mode, func(t *testing.T) {
			project, _, transportDir := setupThreadSpawnCommand(t)
			const secret = "hunter2hunter2"
			const token = "cached-thread-reader-token"
			authenticated := strings.HasPrefix(mode, "authenticated")
			if mode == "authenticated-symlink" {
				realBase := t.TempDir()
				if err := os.Mkdir(filepath.Join(realBase, "thread-project"), 0o755); err != nil {
					t.Fatal(err)
				}
				aliasBase := filepath.Join(t.TempDir(), "projects")
				if err := os.Symlink(realBase, aliasBase); err != nil {
					t.Fatal(err)
				}
				cfg.ProjectsBase = aliasBase
				project = filepath.Join(aliasBase, "thread-project")
				t.Setenv("NTM_THREAD_TEST_PROJECT", project)
			}
			canonicalProject := agentmail.CanonicalProjectKey(project)
			if mode == "authenticated-symlink" && canonicalProject == project {
				t.Fatal("symlink fixture did not produce a distinct canonical project key")
			}
			if authenticated {
				registry := agentmail.NewSessionAgentRegistry("earlier-session", project)
				registry.SetRegistrationToken("BlueLake", token)
				if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
					t.Fatal(err)
				}
			}
			var mu sync.Mutex
			var calls []map[string]interface{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					ID     interface{} `json:"id"`
					Method string      `json:"method"`
					Params struct {
						Name      string                 `json:"name"`
						Arguments map[string]interface{} `json:"arguments"`
					} `json:"params"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("decode Agent Mail request: %v", err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				if req.Method != "tools/call" || req.Params.Name != "summarize_thread" {
					t.Errorf("unexpected Agent Mail side effect: %s %s", req.Method, req.Params.Name)
					http.Error(w, "unexpected tool", http.StatusBadRequest)
					return
				}
				mu.Lock()
				calls = append(calls, req.Params.Arguments)
				mu.Unlock()
				if _, err := os.Stat(filepath.Join(transportDir, "created")); !errors.Is(err, os.ErrNotExist) {
					t.Error("thread was fetched after session creation")
				}
				result := agentmail.ThreadSummaryResponse{
					ThreadID: "FEAT-123",
					Summary: agentmail.ThreadSummary{
						Participants: []string{"BlueLake", "RedStone"},
						KeyPoints:    []string{"Keep the existing transaction boundary", "password=" + secret},
						ActionItems:  []string{"Test interrupted writes"},
					},
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result})
			}))
			t.Cleanup(server.Close)
			cfg.AgentMail.URL = server.URL + "/"
			args := []string{"thread-project", "--label=resume", "--cc=2", "--no-user", "--no-hooks", "--no-recovery", "--no-cass-context", "--thread=FEAT-123"}
			if mode == "prompt-and-marching-orders" {
				cfg.Prompts.CCDefault = "Read AGENTS.md first"
				ordersPath := filepath.Join(t.TempDir(), "orders.txt")
				if err := os.WriteFile(ordersPath, []byte("pane:1 Review the recovery tests\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--prompt=Implement the agreed changes", "--marching-orders="+ordersPath)
			}
			if authenticated {
				args = append(args, "--thread-agent=BlueLake")
			}
			cmd := newSpawnCmd()
			cmd.SetArgs(args)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			stdout, err := captureStdout(t, func() error { return cmd.ExecuteContext(ctx) })
			if err != nil {
				commands, _ := os.ReadFile(filepath.Join(transportDir, "commands"))
				t.Fatalf("spawn: %v\nstdout: %s\ntmux: %s", err, stdout, commands)
			}
			var result output.SpawnResponse
			if err := json.Unmarshal([]byte(stdout), &result); err != nil || result.ThreadID != "FEAT-123" {
				t.Fatalf("missing thread provenance: result=%+v err=%v stdout=%s", result, err, stdout)
			}
			mu.Lock()
			if len(calls) != 1 {
				t.Errorf("summary requests = %d, want one for both agents", len(calls))
			} else {
				call := calls[0]
				if call["project_key"] != canonicalProject || call["thread_id"] != "FEAT-123" || call["llm_mode"] != false || call["include_examples"] != false {
					t.Errorf("wrong summary scope or mode: %v", call)
				}
				if authenticated {
					if call["agent_name"] != "BlueLake" || call["registration_token"] != token {
						t.Error("summary did not use the existing identity's cached token")
					}
				} else if call["agent_name"] != nil || call["registration_token"] != nil {
					t.Error("unauthenticated read invented an agent identity")
				}
			}
			mu.Unlock()
			for number := 1; number <= 2; number++ {
				data, err := os.ReadFile(filepath.Join(transportDir, fmt.Sprintf("deliveries-%d", 90+number)))
				if err != nil {
					t.Fatal(err)
				}
				deliveries := string(data)
				for _, want := range []string{"FEAT-123", canonicalProject, "Keep the existing transaction boundary", "Test interrupted writes", "[REDACTED"} {
					if !strings.Contains(deliveries, want) {
						t.Errorf("pane %d did not receive %q: %s", number, want, deliveries)
					}
				}
				if strings.Count(deliveries, "# Agent Mail thread continuation") != 1 || strings.Contains(deliveries, secret) || strings.Contains(deliveries, token) {
					t.Errorf("pane %d received duplicate or unredacted thread context", number)
				}
				if mode == "prompt-and-marching-orders" {
					want := "Implement the agreed changes"
					if number == 2 {
						want = "Review the recovery tests"
					}
					if !strings.Contains(deliveries, "Read AGENTS.md first") || !strings.Contains(deliveries, want) || strings.Index(deliveries, "Test interrupted writes") > strings.Index(deliveries, want) {
						t.Errorf("pane %d lost per-pane instructions or context order: %s", number, deliveries)
					}
				}
			}
			if strings.Contains(stdout, secret) || strings.Contains(stdout, token) || strings.Contains(stdout, "Keep the existing transaction boundary") {
				t.Fatal("spawn output exposed thread contents or registration token")
			}
		})
	}
}

func TestSpawnCommandThreadPreflightRefusesBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		result     interface{}
		errCode    int
		errMessage string
		status     int
		flags      []string
		wantError  string
		wantCalls  int
	}{
		{name: "missing", result: agentmail.ThreadSummaryResponse{ThreadID: "FEAT-123"}, wantError: "no usable", wantCalls: 1},
		{name: "malformed", result: "not a summary", wantError: "read Agent Mail thread", wantCalls: 1},
		{name: "wrong thread", result: agentmail.ThreadSummaryResponse{ThreadID: "OTHER"}, wantError: "mismatched summary", wantCalls: 1},
		{name: "participants only", result: agentmail.ThreadSummaryResponse{ThreadID: "FEAT-123", Summary: agentmail.ThreadSummary{Participants: []string{"BlueLake"}, KeyPoints: []string{"  "}}}, wantError: "no usable", wantCalls: 1},
		{name: "malformed text", result: agentmail.ThreadSummaryResponse{ThreadID: "FEAT-123", Summary: agentmail.ThreadSummary{KeyPoints: []string{"bad\x00text"}}}, wantError: "malformed summary text", wantCalls: 1},
		{name: "oversized", result: agentmail.ThreadSummaryResponse{ThreadID: "FEAT-123", Summary: agentmail.ThreadSummary{KeyPoints: []string{strings.Repeat("x", maxSpawnThreadContextBytes)}}}, wantError: "continuation limit", wantCalls: 1},
		{name: "unsupported", errCode: -32601, errMessage: "unknown tool summarize_thread", wantError: "read Agent Mail thread", wantCalls: 1},
		{name: "authentication refused", errCode: -32000, errMessage: "AUTHENTICATION_REQUIRED", wantError: "AUTHENTICATION_REQUIRED", wantCalls: 1},
		{name: "unavailable", status: http.StatusServiceUnavailable, wantError: "read Agent Mail thread", wantCalls: 1},
		{name: "disabled", wantError: "agent_mail.enabled"},
		{name: "empty flag", flags: []string{"--thread="}, wantError: "non-empty"},
		{name: "multiple threads", flags: []string{"--thread=ONE,TWO"}, wantError: "one non-empty"},
		{name: "control character", flags: []string{"--thread=BAD\nID"}, wantError: "control characters"},
		{name: "unknown reader", flags: []string{"--thread-agent=BlueLake"}, wantError: "no cached registration token"},
		{name: "reader without thread", flags: []string{"--thread-agent=BlueLake"}, wantError: "requires --thread"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project, _, transportDir := setupThreadSpawnCommand(t)
			if tc.name == "unknown reader" {
				registry := agentmail.NewSessionAgentRegistry("unrelated-session", t.TempDir())
				registry.SetRegistrationToken("BlueLake", "unrelated-project-token")
				if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
					t.Fatal(err)
				}
			}
			var mu sync.Mutex
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				calls++
				mu.Unlock()
				if tc.status != 0 {
					http.Error(w, "temporarily unavailable", tc.status)
					return
				}
				var req struct {
					ID interface{} `json:"id"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				response := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID}
				if tc.errCode != 0 {
					response["error"] = map[string]interface{}{"code": tc.errCode, "message": tc.errMessage}
				} else {
					response["result"] = tc.result
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(response)
			}))
			t.Cleanup(server.Close)
			cfg.AgentMail.URL = server.URL + "/"
			cfg.AgentMail.Enabled = tc.name != "disabled"
			args := []string{"thread-project", "--label=resume", "--cc=2", "--no-user", "--create-dir"}
			if tc.name != "reader without thread" {
				args = append(args, "--thread=FEAT-123")
			}
			args = append(args, tc.flags...)
			cmd := newSpawnCmd()
			cmd.SetArgs(args)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			_, err := captureStdout(t, func() error { return cmd.ExecuteContext(t.Context()) })
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("spawn error=%v, want %q", err, tc.wantError)
			}
			mu.Lock()
			if calls != tc.wantCalls {
				t.Errorf("Agent Mail requests = %d, want %d", calls, tc.wantCalls)
			}
			mu.Unlock()
			for _, path := range []string{project, filepath.Join(transportDir, "commands"), filepath.Join(transportDir, "created")} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("thread refusal allowed spawn side effects at %s: %v", path, err)
				}
			}
		})
	}
}

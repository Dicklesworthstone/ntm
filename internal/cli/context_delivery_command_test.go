package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestContextInjectCobraPackModes(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	for _, scenario := range []string{"build", "stored", "dry_run", "partial_failure", "invalid_flags"} {
		t.Run(scenario, func(t *testing.T) {
			deps := packInjectionTestDeps(t)
			calls := 0
			deps.send = func(_ context.Context, pane tmux.Pane, _ string) error {
				calls++
				if scenario == "partial_failure" && pane.ID == "%2" {
					return errors.New("transport unavailable")
				}
				return nil
			}
			deps.load = func(id string) (*state.ContextPack, error) {
				return &state.ContextPack{ID: id, AgentType: "cc", RenderedPrompt: "stored artifact"}, nil
			}
			args := []string{"demo", "--build", "--files", "main.go"}
			switch scenario {
			case "stored":
				args = []string{"demo", "--pack", "pack-stored", "--pane", "0"}
			case "dry_run":
				args = append(args, "--dry-run")
			case "invalid_flags":
				args = append(args, "--pack", "pack-stored")
			}
			cmd := newContextInjectCmdWithDeps(&deps)
			var stdout bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(io.Discard)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetArgs(args)
			err := cmd.Execute()
			var result ContextInjectResult
			dec := json.NewDecoder(&stdout)
			if decodeErr := dec.Decode(&result); decodeErr != nil {
				t.Fatalf("Cobra did not emit the result to its writer: %v", decodeErr)
			}
			var extra any
			if decodeErr := dec.Decode(&extra); decodeErr != io.EOF {
				t.Fatalf("expected one JSON document: %v", decodeErr)
			}
			switch scenario {
			case "build":
				if err != nil || !result.Success || calls != 3 || result.Mode != "build" {
					t.Fatalf("build surface: %+v err=%v calls=%d", result, err, calls)
				}
			case "stored":
				if err != nil || !result.Success || calls != 1 || result.Deliveries[0].PackID != "pack-stored" {
					t.Fatalf("stored surface: %+v err=%v calls=%d", result, err, calls)
				}
			case "dry_run":
				if err != nil || !result.DryRun || calls != 0 || len(result.PanesInjected) != 0 || len(result.PanesPlanned) != 3 {
					t.Fatalf("dry-run surface: %+v err=%v calls=%d", result, err, calls)
				}
			case "partial_failure":
				if err == nil || result.Success || len(result.PanesInjected) != 2 || result.Deliveries[2].Status != "uncertain" {
					t.Fatalf("failure envelope/exit: %+v err=%v", result, err)
				}
			case "invalid_flags":
				if err == nil || result.Success || calls != 0 {
					t.Fatalf("invalid flags admitted: %+v err=%v calls=%d", result, err, calls)
				}
			}
		})
	}
}

func TestContextPackInjectionFlagsReachableFromContextCommand(t *testing.T) {
	cmd, _, err := newContextCmd().Find([]string{"inject"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"build", "pack", "files", "task", "bead", "pane", "dry-run"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("context inject is missing --%s", name)
		}
	}
}

func TestContextInjectCobraUsesCanonicalTargetsAcrossModes(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	for _, mode := range []string{"files", "build", "pack"} {
		t.Run(mode, func(t *testing.T) {
			for _, scenario := range []string{"default", "qualified", "pane-id", "ambiguous", "all", "no-agents"} {
				t.Run(scenario, func(t *testing.T) {
					deps := packInjectionTestDeps(t)
					projectDir, err := deps.project(t.Context(), "demo")
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(projectDir, "README.md"), []byte("Context for the selected project"), 0600); err != nil {
						t.Fatal(err)
					}
					panes := []tmux.Pane{
						{ID: "%10", WindowIndex: 0, Index: 0, Type: tmux.AgentClaude},
						{ID: "%11", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude},
						{ID: "%20", WindowIndex: 1, Index: 0, Type: "claude-code"},
						{ID: "%21", WindowIndex: 1, Index: 1, Type: tmux.AgentClaude},
						{ID: "%30", WindowIndex: 2, Index: 0, Type: tmux.AgentUser},
						{ID: "%31", WindowIndex: 2, Index: 1, Type: "unknown"},
					}
					if scenario == "no-agents" {
						panes = panes[4:]
					}
					deps.panes = func(string) ([]tmux.Pane, error) { return panes, nil }
					deps.load = func(id string) (*state.ContextPack, error) {
						return &state.ContextPack{ID: id, AgentType: "cc", RenderedPrompt: "stored context"}, nil
					}
					var sent []string
					deps.send = func(_ context.Context, pane tmux.Pane, content string) error {
						if strings.TrimSpace(content) == "" {
							t.Fatal("selected pane received empty context")
						}
						sent = append(sent, pane.ID)
						return nil
					}
					args := []string{"demo"}
					switch mode {
					case "build":
						args = append(args, "--build", "--files", "main.go")
					case "pack":
						args = append(args, "--pack", "saved-context")
					}
					want := []string{"%10", "%11", "%20", "%21"}
					wantError := ""
					switch scenario {
					case "qualified":
						args = append(args, "--pane", "1.1")
						want = []string{"%21"}
					case "pane-id":
						args = append(args, "--pane", "%20")
						want = []string{"%20"}
					case "ambiguous":
						args = append(args, "--pane", "1")
						wantError = "matched 2 panes"
					case "all":
						args = append(args, "--all")
						if mode == "files" {
							want = append(want, "%30", "%31")
						} else {
							wantError = "not a known agent"
						}
					case "no-agents":
						wantError = "no target panes"
					}
					cmd := newContextInjectCmdWithDeps(&deps)
					var stdout bytes.Buffer
					cmd.SetOut(&stdout)
					cmd.SetErr(io.Discard)
					cmd.SilenceErrors, cmd.SilenceUsage = true, true
					cmd.SetArgs(args)
					err = cmd.ExecuteContext(t.Context())
					var result ContextInjectResult
					if decodeErr := json.Unmarshal(stdout.Bytes(), &result); decodeErr != nil {
						t.Fatalf("command did not emit one result: %v; output=%q; error=%v", decodeErr, stdout.String(), err)
					}
					if wantError != "" {
						if err == nil || result.Success || len(sent) != 0 || !strings.Contains(result.Error, wantError) {
							t.Fatalf("invalid target reached delivery: result=%+v sent=%v error=%v", result, sent, err)
						}
						if scenario == "ambiguous" && (!strings.Contains(result.Error, "1.0") || !strings.Contains(result.Error, "1.1")) {
							t.Fatalf("ambiguity error omitted exact target alternatives: %s", result.Error)
						}
						return
					}
					if err != nil || !result.Success || !reflect.DeepEqual(sent, want) || len(result.Deliveries) != len(want) {
						t.Fatalf("command selected wrong targets: result=%+v sent=%v want=%v error=%v", result, sent, want, err)
					}
					for i, receipt := range result.Deliveries {
						if receipt.Target != want[i] || receipt.Status != "delivered" {
							t.Fatalf("receipt lost selected physical target: %+v", receipt)
						}
					}
				})
			}
		})
	}
}

func installContextMutationTMUX(t *testing.T, kind tmux.AgentType, afterWrite string) (string, string) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("requires /bin/sh")
	}

	dir := t.TempDir()
	capture := "❯ \n"
	if kind == tmux.AgentCodex {
		capture = "› \n"
	}
	for name, content := range map[string]string{
		"pid":       "321\n",
		"rotation":  "\n",
		"command":   "agent\n",
		"capture":   capture,
		"enters":    "0\n",
		"calls":     "",
		"mutations": "",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}

	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$NTM_CONTEXT_GUARD_DIR/calls"
record_input() {
  printf '%s\n' "$1" >> "$NTM_CONTEXT_GUARD_DIR/mutations"
  if [ ! -f "$NTM_CONTEXT_GUARD_DIR/first" ]; then
    : > "$NTM_CONTEXT_GUARD_DIR/first"
    case "$NTM_CONTEXT_GUARD_AFTER_WRITE" in
      replace) printf '999\n' > "$NTM_CONTEXT_GUARD_DIR/pid" ;;
      shell) printf 'bash\n' > "$NTM_CONTEXT_GUARD_DIR/command" ;;
      codex_*) printf '› [Pasted 2 lines]\n' > "$NTM_CONTEXT_GUARD_DIR/capture" ;;
    esac
  fi
  case "$1" in
    *"'Enter'")
      read -r enters < "$NTM_CONTEXT_GUARD_DIR/enters"
      enters=$((enters + 1))
      printf '%s\n' "$enters" > "$NTM_CONTEXT_GUARD_DIR/enters"
      if [ "$NTM_CONTEXT_GUARD_AFTER_WRITE" = codex_rescue ] && [ "$enters" -ge 3 ]; then
        printf '› \n' > "$NTM_CONTEXT_GUARD_DIR/capture"
      fi
      ;;
  esac
}
case "$1" in
  -V) printf 'tmux 3.4\n' ;;
  has-session) ;;
  list-sessions) printf 'demo_NTM_SEP_1_NTM_SEP_0_NTM_SEP_1700000000\n' ;;
  list-panes)
    read -r current_pid < "$NTM_CONTEXT_GUARD_DIR/pid"
    printf '%%41_NTM_SEP_1_NTM_SEP_demo__%s_1_NTM_SEP_agent_NTM_SEP_120_NTM_SEP_40_NTM_SEP_1_NTM_SEP_%s_NTM_SEP_0_NTM_SEP_%s_NTM_SEP__NTM_SEP__NTM_SEP_0\n' "$NTM_CONTEXT_GUARD_TYPE" "$current_pid" "$NTM_CONTEXT_GUARD_TYPE"
    ;;
  load-buffer)
    cat > "$NTM_CONTEXT_GUARD_DIR/payload"
    ;;
  display-message)
    case "$*" in
      *'@ntm_rotation_token'*)
        read -r rotation < "$NTM_CONTEXT_GUARD_DIR/rotation"
        printf '[%s]\n' "$rotation"
        ;;
      *'pane_current_path'*) printf '%s\n' "$NTM_CONTEXT_GUARD_DIR" ;;
      *) printf '[%s]\n' "$NTM_CONTEXT_GUARD_TYPE" ;;
    esac
    ;;
  capture-pane)
    read -r enters < "$NTM_CONTEXT_GUARD_DIR/enters"
    if [ "$NTM_CONTEXT_GUARD_AFTER_WRITE" = codex_rescue_replaced ] && [ "$enters" -ge 2 ]; then
      printf '999\n' > "$NTM_CONTEXT_GUARD_DIR/pid"
    fi
    cat "$NTM_CONTEXT_GUARD_DIR/capture"
    ;;
  if-shell)
    if [ "$2" != "-F" ] || [ "$3" != "-t" ] || [ "$4" != "%41" ]; then
      exit 2
    fi
    read -r current_pid < "$NTM_CONTEXT_GUARD_DIR/pid"
    read -r rotation < "$NTM_CONTEXT_GUARD_DIR/rotation"
    read -r current_command < "$NTM_CONTEXT_GUARD_DIR/command"
    case "$5" in
      *"#{==:#{pane_pid},$current_pid}"*) ;;
      *)
        printf 'ntm_pane_identity_changed\n'
        exit 0
        ;;
    esac
    case "$5" in
      *'#{==:#{@ntm_rotation_token},}'*)
        if [ -n "$rotation" ]; then
          printf 'ntm_pane_identity_changed\n'
          exit 0
        fi
        ;;
      *) exit 2 ;;
    esac
    case "$5" in
      *'#{!=:#{pane_current_command},}'*)
        case "$current_command" in
          ""|sh|bash|zsh|fish|dash|ksh)
            printf 'ntm_pane_identity_changed\n'
            exit 0
            ;;
        esac
        ;;
    esac
    record_input "$6"
    ;;
  paste-buffer|send-keys)
    record_input "unguarded: $*"
    ;;
  delete-buffer)
    ;;
  show-options|show-environment) ;;
  *)
    exit 2
    ;;
esac
`
	binary := filepath.Join(dir, "tmux")
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", binary)
	t.Setenv("NTM_CONTEXT_GUARD_DIR", dir)
	t.Setenv("NTM_CONTEXT_GUARD_TYPE", string(kind))
	t.Setenv("NTM_CONTEXT_GUARD_AFTER_WRITE", afterWrite)

	oldClient := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = oldClient })
	return dir, filepath.Join(dir, "mutations")
}

func TestContextInjectionRetainsPaneIdentityThroughPreparationAndSubmission(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	protocols := []struct {
		name    string
		kind    tmux.AgentType
		content string
		writes  int
	}{
		{
			name:    "bracketed_paste",
			kind:    tmux.AgentClaude,
			content: "project context\nsecond line",
			writes:  3,
		},
		{
			name:    "literal_chunks",
			kind:    tmux.AgentGrok,
			content: strings.Repeat("x", 8193),
			writes:  5,
		},
	}
	scenarios := []struct {
		name       string
		afterWrite string
		fail       bool
		partial    bool
	}{
		{name: "live"},
		{name: "replaced_during_preparation", fail: true},
		{name: "rotation_during_preparation", fail: true},
		{name: "replaced_after_first_write", afterWrite: "replace", fail: true, partial: true},
		{name: "exited_after_first_write", afterWrite: "shell", fail: true, partial: true},
	}

	for _, surface := range []string{"cobra_build", "robot_adapter"} {
		t.Run(surface, func(t *testing.T) {
			for _, protocol := range protocols {
				t.Run(protocol.name, func(t *testing.T) {
					for _, scenario := range scenarios {
						t.Run(scenario.name, func(t *testing.T) {
							dir, logPath := installContextMutationTMUX(t, protocol.kind, scenario.afterWrite)
							pane := tmux.Pane{
								ID: "%41", Index: 1, PID: 321,
								Type: protocol.kind, Command: "agent",
							}
							prepared := false
							prepare := func() {
								prepared = true
								var name, value string
								switch scenario.name {
								case "replaced_during_preparation":
									name, value = "pid", "999\n"
								case "rotation_during_preparation":
									name, value = "rotation", "rotation-owner\n"
								}
								if name != "" {
									if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
										t.Fatal(err)
									}
								}
							}

							var injected []int
							var deliveryErr error
							var failureText string
							if surface == "cobra_build" {
								deps := packInjectionTestDeps(t)
								deps.panes = func(string) ([]tmux.Pane, error) {
									return []tmux.Pane{pane}, nil
								}
								baseBuild := deps.build
								deps.build = func(ctx context.Context, opts ntmctx.BuildOptions) (*ntmctx.ContextPackFull, error) {
									prepare()
									pack, err := baseBuild(ctx, opts)
									if err != nil {
										return nil, err
									}
									pack.RenderedPrompt = protocol.content
									return pack, nil
								}
								deps.send = sendContextToPane

								cmd := newContextInjectCmdWithDeps(&deps)
								var stdout bytes.Buffer
								cmd.SetOut(&stdout)
								cmd.SetErr(io.Discard)
								cmd.SilenceErrors, cmd.SilenceUsage = true, true
								cmd.SetArgs([]string{"demo", "--build", "--files", "main.go"})
								deliveryErr = cmd.ExecuteContext(t.Context())

								var result ContextInjectResult
								if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
									t.Fatalf("invalid command result: %v; output=%q", err, stdout.String())
								}
								if result.Success == scenario.fail || len(result.Deliveries) != 1 {
									t.Fatalf("wrong command outcome: %+v; error=%v", result, deliveryErr)
								}
								wantStatus := "delivered"
								if scenario.fail {
									wantStatus = "uncertain"
									if scenario.name == "rotation_during_preparation" {
										wantStatus = "not_attempted"
									}
								}
								if result.Deliveries[0].Status != wantStatus {
									t.Fatalf("wrong delivery receipt: %+v", result.Deliveries[0])
								}
								injected = result.PanesInjected
								failureText = result.Error
							} else {
								prepare()
								var result ContextInjectResult
								result, deliveryErr = injectContextIntoPanes(
									t.Context(), "demo", []tmux.Pane{pane},
									protocol.content, 0, false, sendContextToPane,
								)
								injected = result.PanesInjected
								if deliveryErr != nil {
									failureText = deliveryErr.Error()
								}
							}

							if !prepared {
								t.Fatal("test did not reach context preparation")
							}
							if (deliveryErr != nil) != scenario.fail {
								t.Fatalf("wrong delivery error: %v; injected=%v", deliveryErr, injected)
							}
							if scenario.fail {
								wantError := tmux.ErrPaneIdentityChanged.Error()
								if scenario.name == "rotation_during_preparation" {
									wantError = "rotation_pending"
								}
								if len(injected) != 0 || !strings.Contains(failureText, wantError) {
									t.Fatalf("changed target was reported delivered: injected=%v error=%q", injected, failureText)
								}
							} else if !reflect.DeepEqual(injected, []int{1}) {
								t.Fatalf("valid target was not delivered: %v", injected)
							}

							data, err := os.ReadFile(logPath)
							if err != nil {
								t.Fatal(err)
							}
							log := string(data)
							if strings.Contains(log, "unguarded:") {
								t.Fatalf("context delivery bypassed the mutation guard: %s", log)
							}
							wantWrites := protocol.writes
							if scenario.fail {
								wantWrites = 0
								if scenario.partial {
									wantWrites = 1
								}
							}
							if got := strings.Count(log, "\n"); got != wantWrites {
								t.Fatalf("accepted %d input mutations, want %d: %s", got, wantWrites, log)
							}
							wantEnters := 2
							if scenario.fail {
								wantEnters = 0
							}
							if got := strings.Count(log, "'Enter'"); got != wantEnters {
								t.Fatalf("submitted %d Enter keys, want %d: %s", got, wantEnters, log)
							}
						})
					}
				})
			}
		})
	}
}

func TestContextInjectCobraUsesPerTargetRemainingCapacity(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	deps := packInjectionTestDeps(t)
	dir, err := deps.project(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(strings.Repeat("// context-capacity-source\n", 40000)), 0600); err != nil {
		t.Fatal(err)
	}
	panes := []tmux.Pane{
		{ID: "%10", Index: 0, PID: 110, Type: tmux.AgentClaude},
		{ID: "%11", Index: 1, PID: 111, Type: tmux.AgentClaude},
		{ID: "%12", Index: 2, PID: 112, Type: tmux.AgentClaude},
		{ID: "%13", Index: 3, PID: 113, Type: tmux.AgentGemini},
	}
	deps.panes = func(string) ([]tmux.Pane, error) { return panes, nil }
	now := time.Now()
	usage := map[string]ntmctx.TranscriptUsage{
		"%10": {Path: "/test/large", Model: "claude-opus-5", ContextWindow: 1000000, UpdatedAt: now},
		"%11": {Path: "/test/full", Model: "claude-opus-5", ContextWindow: 1000000, Tokens: 999000, UpdatedAt: now},
		"%12": {Path: "/test/small", Model: "claude-opus-4", ContextWindow: 4000, Tokens: 1000, UpdatedAt: now},
		"%13": {Path: "/test/gemini", Model: "gemini-2.0-flash", ContextWindow: 1000000, Tokens: 200000, UpdatedAt: now},
	}
	budgets := map[string]int{"%10": 900000, "%11": 900, "%12": 2700, "%13": 80000}
	observed := 0
	deps.usage = func(_ context.Context, pane tmux.Pane) (*ntmctx.TranscriptUsage, bool) {
		observed++
		value := usage[pane.ID]
		return &value, true
	}
	builder := ntmctx.NewContextPackBuilder(nil)
	builder.ClearCache()
	t.Cleanup(builder.ClearCache)
	var builds []ntmctx.BuildOptions
	deps.build = func(ctx context.Context, opts ntmctx.BuildOptions) (*ntmctx.ContextPackFull, error) {
		builds = append(builds, opts)
		return builder.Build(ctx, opts)
	}
	sent := make(map[string]string)
	deps.send = func(_ context.Context, pane tmux.Pane, text string) error {
		if len(builds) != len(panes) || observed != 2*len(panes) {
			t.Fatalf("delivery preceded complete preparation/admission: builds=%d observations=%d", len(builds), observed)
		}
		if len(text) > budgets[pane.ID]*4 || !strings.Contains(text, "context-capacity-source") {
			t.Fatalf("wrong source or oversized payload for %s: %d bytes", pane.ID, len(text))
		}
		sent[pane.ID] = text
		return nil
	}
	cmd := newContextInjectCmdWithDeps(&deps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"demo", "--build", "--files", "main.go"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("capacity-aware injection failed: %v: %s", err, out.String())
	}
	var result ContextInjectResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Success || len(sent) != len(panes) || len(result.Deliveries) != len(panes) {
		t.Fatalf("incomplete capacity-aware batch: %+v", result)
	}
	for i, pane := range panes {
		want := usage[pane.ID]
		if builds[i].Model != want.Model || builds[i].ContextWindow != want.ContextWindow || builds[i].MaxTokens != budgets[pane.ID] {
			t.Fatalf("target %s reused another target's budget: %+v", pane.ID, builds[i])
		}
		capacity := result.Deliveries[i].Capacity
		if capacity == nil || capacity.Status != "measured" || capacity.BudgetSource != "remaining_context" ||
			capacity.BudgetTokens != budgets[pane.ID] || capacity.UsedTokens == nil || *capacity.UsedTokens != want.Tokens {
			t.Fatalf("target capacity provenance lost: %+v", result.Deliveries[i])
		}
	}
	if len(sent["%10"]) <= ntmctx.GetTokenBudget("cc")*4 {
		t.Fatal("large-model fixture did not exercise delivery beyond the old agent-default limit")
	}
	if result.Deliveries[0].PackID == result.Deliveries[1].PackID {
		t.Fatal("same model with different remaining capacity shared a pack")
	}
}

func writeOMPContextUsage(t *testing.T, path, provider, model string, tokens int, stamp time.Time) {
	t.Helper()
	line := fmt.Sprintf(`{"type":"message","timestamp":%q,"message":{"role":"assistant","provider":%q,"model":%q,"usage":{"input":%d,"cacheRead":7,"cacheWrite":5,"output":5,"totalTokens":%d}}}`,
		stamp.UTC().Format(time.RFC3339Nano), provider, model, tokens-17, tokens)
	if err := os.WriteFile(path, []byte(line+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestContextInjectCobraUsesProviderQualifiedNativeCapacity(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	deps := packInjectionTestDeps(t)
	dir, err := deps.project(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(strings.Repeat("// qualified-context-source\n", 4000)), 0600); err != nil {
		t.Fatal(err)
	}
	panes := []tmux.Pane{
		{ID: "%10", Index: 0, PID: 110, Type: tmux.AgentOmp},
		{ID: "%11", Index: 1, PID: 111, Type: tmux.AgentOmp},
		{ID: "%12", Index: 2, PID: 112, Type: tmux.AgentOmp},
	}
	deps.panes = func(string) ([]tmux.Pane, error) { return panes, nil }
	targets := []struct {
		provider, model      string
		used, window, budget int
	}{
		{"anthropic", "claude-opus-4-6", 999000, 1000000, 780},
		{"openrouter", "anthropic/claude-opus-4-6", 998000, 1000000, 1560},
		{"google", "gemini-pro", 31000, 32000, 780},
	}
	paths := make(map[string]string)
	for i, target := range targets {
		path := filepath.Join(t.TempDir(), "transcript.jsonl")
		writeOMPContextUsage(t, path, target.provider, target.model, target.used, time.Now().Add(-time.Second))
		paths[panes[i].ID] = path
	}
	observed := 0
	deps.usage = func(_ context.Context, pane tmux.Pane) (*ntmctx.TranscriptUsage, bool) {
		observed++
		usage, err := ntmctx.ReadLatestTranscriptUsage(paths[pane.ID])
		if err != nil || usage == nil || usage.ContextWindow != 0 {
			t.Fatalf("native OMP fixture did not require model resolution: %+v, %v", usage, err)
		}
		return usage, true
	}
	builder := ntmctx.NewContextPackBuilder(nil)
	builder.ClearCache()
	t.Cleanup(builder.ClearCache)
	var builds []ntmctx.BuildOptions
	deps.build = func(ctx context.Context, opts ntmctx.BuildOptions) (*ntmctx.ContextPackFull, error) {
		builds = append(builds, opts)
		return builder.Build(ctx, opts)
	}
	sent := make(map[string]string)
	deps.send = func(_ context.Context, pane tmux.Pane, text string) error {
		if len(builds) != len(panes) || observed != 2*len(panes) {
			t.Fatalf("delivery preceded whole-batch native admission: builds=%d observations=%d", len(builds), observed)
		}
		if len(text) > targets[pane.Index].budget*4 || !strings.Contains(text, "qualified-context-source") {
			t.Fatalf("incorrect or oversized payload for %s: %d bytes", pane.ID, len(text))
		}
		sent[pane.ID] = text
		return nil
	}
	cmd := newContextInjectCmdWithDeps(&deps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"demo", "--build", "--files", "main.go"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("qualified native context injection failed: %v: %s", err, out.String())
	}
	var result ContextInjectResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Success || len(sent) != len(panes) || len(result.Deliveries) != len(panes) || len(builds) != len(panes) {
		t.Fatalf("incomplete qualified-model delivery: %+v", result)
	}
	for i, target := range targets {
		model := target.provider + "/" + target.model
		capacity := result.Deliveries[i].Capacity
		if capacity == nil || capacity.Status != "measured" || capacity.BudgetSource != "remaining_context" ||
			capacity.Model != model || capacity.ContextWindow != target.window || capacity.BudgetTokens != target.budget ||
			capacity.UsedTokens == nil || *capacity.UsedTokens != target.used {
			t.Fatalf("provider identity or measured capacity lost for %s: %+v", panes[i].ID, capacity)
		}
		if builds[i].Model != model || builds[i].ContextWindow != target.window || builds[i].MaxTokens != target.budget {
			t.Fatalf("builder did not receive native model/window/budget: %+v", builds[i])
		}
	}
	if result.Deliveries[0].PackID == result.Deliveries[1].PackID {
		t.Fatal("different provider identities shared a context artifact")
	}
}

func TestContextInjectCobraRejectsFullOrChangedQualifiedNativeSession(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	for _, scenario := range []string{"full", "filled_during_build", "provider_changed_during_build"} {
		t.Run(scenario, func(t *testing.T) {
			deps := packInjectionTestDeps(t)
			dir, err := deps.project(t.Context(), "demo")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("// native-session-admission\n"), 0600); err != nil {
				t.Fatal(err)
			}
			panes := []tmux.Pane{
				{ID: "%1", Index: 0, PID: 101, Type: tmux.AgentOmp},
				{ID: "%2", Index: 1, PID: 102, Type: tmux.AgentOmp},
			}
			deps.panes = func(string) ([]tmux.Pane, error) { return panes, nil }
			paths := map[string]string{"%1": filepath.Join(t.TempDir(), "first.jsonl"), "%2": filepath.Join(t.TempDir(), "second.jsonl")}
			stamp := time.Now().Add(-time.Second)
			writeOMPContextUsage(t, paths["%1"], "anthropic", "claude-opus-4-6", 998000, stamp)
			used := 999000
			if scenario == "full" {
				used = 1000000
			}
			writeOMPContextUsage(t, paths["%2"], "anthropic", "claude-opus-4-6", used, stamp)
			deps.usage = func(_ context.Context, pane tmux.Pane) (*ntmctx.TranscriptUsage, bool) {
				usage, err := ntmctx.ReadLatestTranscriptUsage(paths[pane.ID])
				if err != nil || usage == nil {
					t.Fatalf("read native fixture: %+v, %v", usage, err)
				}
				return usage, true
			}
			builder := ntmctx.NewContextPackBuilder(nil)
			builder.ClearCache()
			t.Cleanup(builder.ClearCache)
			builds := 0
			deps.build = func(ctx context.Context, opts ntmctx.BuildOptions) (*ntmctx.ContextPackFull, error) {
				builds++
				pack, err := builder.Build(ctx, opts)
				switch scenario {
				case "filled_during_build":
					writeOMPContextUsage(t, paths["%2"], "anthropic", "claude-opus-4-6", 1000000, stamp.Add(time.Millisecond))
				case "provider_changed_during_build":
					writeOMPContextUsage(t, paths["%2"], "openrouter", "anthropic/claude-opus-4-6", 999000, stamp.Add(time.Millisecond))
				}
				return pack, err
			}
			sends := 0
			deps.send = func(context.Context, tmux.Pane, string) error { sends++; return nil }
			cmd := newContextInjectCmdWithDeps(&deps)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(io.Discard)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetArgs([]string{"demo", "--build", "--files", "main.go"})
			err = cmd.ExecuteContext(t.Context())
			var result ContextInjectResult
			if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil {
				t.Fatalf("invalid refusal envelope: %v: %s", decodeErr, out.String())
			}
			if err == nil || result.Success || sends != 0 || len(result.Deliveries) != len(panes) || len(result.PanesInjected) != 0 {
				t.Fatalf("full/changed native session admitted a partial batch: %+v; sends=%d err=%v", result, sends, err)
			}
			wantBuilds := len(panes)
			if scenario == "full" {
				wantBuilds = 0
			}
			if builds != wantBuilds {
				t.Fatalf("native admission reached %d builds, want %d", builds, wantBuilds)
			}
			wantError := "no remaining context capacity"
			if scenario == "provider_changed_during_build" {
				wantError = "context session or model changed"
			}
			if !strings.Contains(result.Error, wantError) {
				t.Fatalf("refused for the wrong reason: result.Error=%q err=%v; want %q", result.Error, err, wantError)
			}
			for _, receipt := range result.Deliveries {
				if receipt.Status != "not_attempted" {
					t.Fatalf("refused native batch claimed a send: %+v", receipt)
				}
			}
		})
	}
}

func TestContextInjectCobraRechecksWholeStoredPackBatch(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	for _, scenario := range []string{
		"filled", "lost", "stale", "session_changed", "model_changed", "window_changed",
		"accounting_regressed", "newly_known", "replaced", "dead", "service", "rotation", "canceled",
	} {
		t.Run(scenario, func(t *testing.T) {
			deps := packInjectionTestDeps(t)
			panes := []tmux.Pane{
				{ID: "%1", Index: 0, PID: 101, Type: tmux.AgentClaude},
				{ID: "%2", Index: 1, PID: 102, Type: tmux.AgentClaude},
			}
			lists := 0
			deps.panes = func(string) ([]tmux.Pane, error) {
				lists++
				current := append([]tmux.Pane(nil), panes...)
				if lists == 2 {
					switch scenario {
					case "replaced":
						current[1].PID++
					case "dead":
						current[1].Dead = true
					case "service":
						current[1].Service = "cm"
					case "rotation":
						current[1].RotationToken = "handoff-owner"
					}
				}
				return current, nil
			}
			artifact := strings.Repeat("x", 800)
			stored := &state.ContextPack{ID: "pack-kept", AgentType: "cc", RenderedPrompt: artifact, TokenCount: 1}
			deps.load = func(string) (*state.ContextPack, error) { return stored, nil }
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			counts := make(map[string]int)
			stamp := time.Now().Add(-time.Second)
			deps.usage = func(_ context.Context, pane tmux.Pane) (*ntmctx.TranscriptUsage, bool) {
				counts[pane.ID]++
				u := &ntmctx.TranscriptUsage{
					Path: "/test/" + pane.ID, Model: "claude-opus-4",
					ContextWindow: 4000, Tokens: 1000, UpdatedAt: stamp,
				}
				if pane.ID != "%2" {
					return u, true
				}
				if counts[pane.ID] == 1 {
					if scenario == "newly_known" {
						return nil, false
					}
					return u, true
				}
				switch scenario {
				case "filled":
					u.Tokens = 3900
				case "lost":
					return nil, false
				case "stale":
					u.UpdatedAt = time.Now().Add(-ntmctx.TranscriptFreshness - time.Second)
				case "session_changed":
					u.Path = "/test/restarted"
				case "model_changed":
					u.Model = "claude-opus-5"
				case "window_changed":
					u.ContextWindow = 8000
				case "accounting_regressed":
					u.UpdatedAt = stamp.Add(-time.Second)
				case "newly_known":
					u.ContextWindow, u.Tokens = 1000, 900
				case "canceled":
					cancel()
				}
				return u, true
			}
			sends := 0
			deps.send = func(context.Context, tmux.Pane, string) error { sends++; return nil }
			cmd := newContextInjectCmdWithDeps(&deps)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(io.Discard)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetArgs([]string{"demo", "--pack", "pack-kept"})
			err := cmd.ExecuteContext(ctx)
			var result ContextInjectResult
			if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil {
				t.Fatalf("invalid refusal result: %v: %s", decodeErr, out.String())
			}
			if err == nil || result.Success || sends != 0 || len(result.Deliveries) != 2 || len(result.PanesInjected) != 0 {
				t.Fatalf("changed later target allowed a partial batch: result=%+v sends=%d err=%v", result, sends, err)
			}
			for _, receipt := range result.Deliveries {
				if receipt.Status != "not_attempted" {
					t.Fatalf("preflight refusal claimed an attempted send: %+v", receipt)
				}
			}
			if stored.RenderedPrompt != artifact || stored.TokenCount != 1 {
				t.Fatal("capacity refusal rewrote the stored artifact")
			}
		})
	}
}

func TestContextInjectCobraUsesSharedDeliveryGates(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	for _, mode := range []string{"files", "build", "pack"} {
		t.Run(mode, func(t *testing.T) {
			for _, tc := range []struct {
				name      string
				kind      tmux.AgentType
				capture   string
				wantError string
			}{
				{
					name: "claude trust", kind: tmux.AgentClaude,
					capture:   "Quick safety check: Is this a project you created or one you trust?\n❯ No, exit\n  Yes, I trust this folder\nEnter to confirm · Esc to cancel\n",
					wantError: "PANE_INTERACTIVE_GATE",
				},
				{
					name: "codex trust", kind: tmux.AgentCodex,
					capture:   "Do you trust the contents of this directory?\n› 1. Yes, continue\n  2. No, quit\n",
					wantError: "composer not visible",
				},
				{
					name: "antigravity trust", kind: tmux.AgentAntigravity,
					capture:   "Do you trust the contents of this project?\n> Yes, I trust this folder\n  No, exit\n↑/↓ Navigate · enter Confirm\n",
					wantError: "PANE_INTERACTIVE_GATE",
				},
				{
					name: "grok trust", kind: tmux.AgentGrok,
					capture:   "Grok Build may run or modify contents in this directory,\nposing security risks.\nYes, proceed  y\nNo, quit  n\n",
					wantError: "PANE_INTERACTIVE_GATE",
				},
				{
					name: "codex transcript history", kind: tmux.AgentCodex,
					capture:   "› an earlier user prompt\nBrowsing transcript · ↑↓/jk scroll · ←→/hl prompts · ⌃t details · ↵ rewind · esc back\n",
					wantError: "Enter would rewind",
				},
				{name: "claude initializing", kind: tmux.AgentClaude, capture: "Loading agent configuration...\n", wantError: "composer not visible"},
				{name: "omp initializing", kind: tmux.AgentOmp, wantError: "composer not drawn yet"},
				{name: "ready claude", kind: tmux.AgentClaude, capture: "❯ \n"},
				{name: "ready codex", kind: tmux.AgentCodex, capture: "› \n"},
				{name: "ready grok", kind: tmux.AgentGrok, capture: "❯ \n"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					dir, mutationsPath := installContextMutationTMUX(t, tc.kind, "")
					if err := os.WriteFile(filepath.Join(dir, "capture"), []byte(tc.capture), 0600); err != nil {
						t.Fatal(err)
					}
					pane := tmux.Pane{ID: "%41", Index: 1, PID: 321, Type: tc.kind, Command: "agent", Width: 120}
					deps := packInjectionTestDeps(t)
					deps.panes = func(string) ([]tmux.Pane, error) { return []tmux.Pane{pane}, nil }
					deps.usage = nil
					deps.send = defaultContextInjectDeps(io.Discard).send
					deps.load = func(id string) (*state.ContextPack, error) {
						return &state.ContextPack{
							ID: id, AgentType: state.AgentType(tc.kind.Canonical()),
							RenderedPrompt: "new project context\nsecond line",
						}, nil
					}
					args := []string{"demo"}
					switch mode {
					case "files":
						projectDir, err := deps.project(t.Context(), "demo")
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(projectDir, "context.txt"), []byte("new project context\nsecond line"), 0600); err != nil {
							t.Fatal(err)
						}
						args = append(args, "--files", "context.txt")
					case "build":
						args = append(args, "--build", "--files", "main.go")
					case "pack":
						args = append(args, "--pack", "saved-context")
					}

					cmd := newContextInjectCmdWithDeps(&deps)
					var stdout bytes.Buffer
					cmd.SetOut(&stdout)
					cmd.SetErr(io.Discard)
					cmd.SilenceErrors, cmd.SilenceUsage = true, true
					cmd.SetArgs(args)
					runErr := cmd.ExecuteContext(t.Context())
					var result ContextInjectResult
					if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
						t.Fatalf("invalid command result: %v; output=%q", err, stdout.String())
					}
					mutations, err := os.ReadFile(mutationsPath)
					if err != nil {
						t.Fatal(err)
					}
					calls, err := os.ReadFile(filepath.Join(dir, "calls"))
					if err != nil {
						t.Fatal(err)
					}
					if len(result.Deliveries) != 1 || result.Deliveries[0].Target != pane.ID {
						t.Fatalf("lost physical target receipt: %+v", result)
					}
					if !strings.Contains(string(calls), "capture-pane") {
						t.Fatalf("production sender never inspected the input surface: %s", calls)
					}
					if tc.wantError != "" {
						if runErr == nil || result.Success || len(result.PanesInjected) != 0 ||
							result.Deliveries[0].Status != "not_attempted" || !strings.Contains(result.Error, tc.wantError) {
							t.Fatalf("unsafe context input was not refused: result=%+v error=%v", result, runErr)
						}
						if len(mutations) != 0 || strings.Contains(string(calls), "load-buffer") {
							t.Fatalf("refused context reached transport: mutations=%s calls=%s", mutations, calls)
						}
						return
					}
					if runErr != nil || !result.Success || !reflect.DeepEqual(result.PanesInjected, []int{1}) ||
						result.Deliveries[0].Status != "delivered" {
						t.Fatalf("ready agent did not receive context: result=%+v error=%v", result, runErr)
					}
					if strings.Contains(string(mutations), "unguarded:") || strings.Count(string(mutations), "'Enter'") != 2 {
						t.Fatalf("context bypassed the guarded agent submission protocol: %s", mutations)
					}
					for _, key := range []string{"'Escape'", "'C-u'", "'C-c'", "'C-e'"} {
						if strings.Contains(string(mutations), key) {
							t.Fatalf("context unexpectedly cleared or interrupted the composer: %s", mutations)
						}
					}
				})
			}
		})
	}
}

func TestContextInjectCobraVerifiesSubmissionWithPaneGuard(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	for _, tc := range []struct {
		name       string
		wantError  string
		wantEnters int
	}{
		{name: "codex_rescue", wantEnters: 3},
		{name: "codex_rescue_replaced", wantError: tmux.ErrPaneIdentityChanged.Error(), wantEnters: 2},
		{name: "codex_stuck", wantError: "submission unconfirmed", wantEnters: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, mutationsPath := installContextMutationTMUX(t, tmux.AgentCodex, tc.name)
			pane := tmux.Pane{ID: "%41", Index: 1, PID: 321, Type: tmux.AgentCodex, Command: "codex", Width: 120}
			deps := packInjectionTestDeps(t)
			deps.panes = func(string) ([]tmux.Pane, error) { return []tmux.Pane{pane}, nil }
			deps.usage = nil
			deps.send = defaultContextInjectDeps(io.Discard).send
			deps.load = func(id string) (*state.ContextPack, error) {
				return &state.ContextPack{ID: id, AgentType: "cod", RenderedPrompt: "project context\nsecond line"}, nil
			}
			cmd := newContextInjectCmdWithDeps(&deps)
			var stdout bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(io.Discard)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetArgs([]string{"demo", "--pack", "saved-context"})
			runErr := cmd.ExecuteContext(t.Context())
			var result ContextInjectResult
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatalf("invalid command result: %v; output=%q", err, stdout.String())
			}
			if len(result.Deliveries) != 1 {
				t.Fatalf("missing submission receipt: %+v", result)
			}
			if tc.wantError == "" {
				if runErr != nil || !result.Success || result.Deliveries[0].Status != "delivered" {
					t.Fatalf("stranded context was not submitted: result=%+v error=%v", result, runErr)
				}
			} else if runErr == nil || result.Success || len(result.PanesInjected) != 0 ||
				result.Deliveries[0].Status != "uncertain" || !strings.Contains(result.Error, tc.wantError) {
				t.Fatalf("unconfirmed context was reported delivered: result=%+v error=%v", result, runErr)
			}
			mutations, err := os.ReadFile(mutationsPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(mutations), "unguarded:") || strings.Count(string(mutations), "'Enter'") != tc.wantEnters {
				t.Fatalf("submission rescue lost its original pane binding: %s", mutations)
			}
			if strings.Count(string(mutations), "'paste-buffer'") != 1 {
				t.Fatalf("submission verification replayed the context payload: %s", mutations)
			}
		})
	}
}

func TestContextRobotInjectionPreservesExplicitShellProtocol(t *testing.T) {
	dir, mutationsPath := installContextMutationTMUX(t, tmux.AgentUser, "")
	if err := os.WriteFile(filepath.Join(dir, "command"), []byte("bash\n"), 0600); err != nil {
		t.Fatal(err)
	}
	pane := tmux.Pane{ID: "%41", Index: 0, PID: 321, Type: tmux.AgentUser, Command: "bash"}
	result, err := injectContextIntoPanes(t.Context(), "demo", []tmux.Pane{pane}, "explicit shell input", 0, false, sendContextToPane)
	if err != nil || !reflect.DeepEqual(result.PanesInjected, []int{0}) {
		t.Fatalf("explicit shell target was blocked: result=%+v error=%v", result, err)
	}
	mutations, err := os.ReadFile(mutationsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mutations), "unguarded:") || strings.Count(string(mutations), "'Enter'") != 1 {
		t.Fatalf("explicit shell lost its single-Enter identity guard: %s", mutations)
	}
}

func TestContextInjectCobraAppliesFinalMessageRedaction(t *testing.T) {
	oldJSON, oldCfg := jsonOutput, cfg
	jsonOutput = true
	t.Cleanup(func() { jsonOutput, cfg = oldJSON, oldCfg })
	secret := "ghp_" + strings.Repeat("A", 36)
	for _, mode := range []string{"files", "build", "pack"} {
		for _, policy := range []string{"off", "warn", "redact", "block"} {
			for _, dryRun := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/dry=%t", mode, policy, dryRun), func(t *testing.T) {
					cfg = config.Default()
					cfg.Redaction.Mode = policy
					deps := packInjectionTestDeps(t)
					projectDir, err := deps.project(t.Context(), "demo")
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(projectDir, "README.md"), []byte("Project context\n"+secret), 0600); err != nil {
						t.Fatal(err)
					}
					artifacts := make(map[*state.ContextPack]string)
					build := deps.build
					deps.build = func(ctx context.Context, opts ntmctx.BuildOptions) (*ntmctx.ContextPackFull, error) {
						pack, err := build(ctx, opts)
						if err != nil {
							return nil, err
						}
						// Only the last target has sensitive enrichment. Block must
						// prevent the earlier clean targets from being written too.
						if opts.AgentType == "cod" {
							pack.RenderedPrompt += "\n" + secret
						}
						artifacts[&pack.ContextPack] = pack.RenderedPrompt
						return pack, nil
					}
					stored := &state.ContextPack{ID: "stored-context", AgentType: "cc", RenderedPrompt: "Saved context\n" + secret}
					artifacts[stored] = stored.RenderedPrompt
					deps.load = func(string) (*state.ContextPack, error) { return stored, nil }
					sent := make(map[string]string)
					deps.send = func(_ context.Context, pane tmux.Pane, message string) error {
						sent[pane.ID] = message
						return nil
					}
					args := []string{"demo", "--files", "README.md"}
					if mode == "build" {
						args = append(args, "--build")
					} else if mode == "pack" {
						args = []string{"demo", "--pack", stored.ID, "--pane", "0"}
					}
					if dryRun {
						args = append(args, "--dry-run")
					}
					cmd := newContextInjectCmdWithDeps(&deps)
					var out bytes.Buffer
					cmd.SetOut(&out)
					cmd.SetErr(io.Discard)
					cmd.SilenceErrors, cmd.SilenceUsage = true, true
					cmd.SetArgs(args)
					err = cmd.ExecuteContext(t.Context())
					var result ContextInjectResult
					if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil {
						t.Fatalf("invalid context response: %v: %s", decodeErr, out.String())
					}
					if strings.Contains(out.String(), secret) || (err != nil && strings.Contains(err.Error(), secret)) {
						t.Fatal("redaction receipts exposed a matched credential")
					}
					for pack, original := range artifacts {
						if pack.RenderedPrompt != original {
							t.Fatal("delivery redaction modified a reusable context artifact")
						}
					}
					if policy == "block" {
						if err == nil || result.Success || result.ErrorCode != "SENSITIVE_DATA_BLOCKED" || len(sent) != 0 {
							t.Fatalf("blocked batch reached a target: result=%+v sent=%v error=%v", result, sent, err)
						}
						blocked := false
						for _, receipt := range result.Deliveries {
							if receipt.Status != "not_attempted" {
								t.Fatalf("whole-batch block reported an attempted delivery: %+v", receipt)
							}
							blocked = blocked || (receipt.Redaction != nil && receipt.Redaction.Blocked)
						}
						if !blocked || !strings.Contains(result.Hint, "--allow-secret") {
							t.Fatalf("blocked batch lost its actionable policy receipt: %+v", result)
						}
						return
					}
					if err != nil || !result.Success || len(result.Deliveries) == 0 {
						t.Fatalf("permitted context was rejected: %+v, %v", result, err)
					}
					if dryRun {
						if len(sent) != 0 || len(result.PanesInjected) != 0 || len(result.PanesPlanned) != len(result.Deliveries) {
							t.Fatalf("dry-run actuated or claimed a delivery: %+v sent=%v", result, sent)
						}
					} else if len(sent) != len(result.Deliveries) {
						t.Fatalf("permitted batch was incomplete: %+v sent=%v", result, sent)
					}
					findings, largest := 0, 0
					for _, receipt := range result.Deliveries {
						if receipt.Redaction == nil || receipt.Redaction.Mode != policy {
							t.Fatalf("final policy was not reported: %+v", receipt)
						}
						findings += receipt.Redaction.Findings
						if !dryRun {
							message := sent[receipt.Target]
							if receipt.Bytes != len(message) {
								t.Fatalf("receipt describes pre-redaction bytes: %+v; sent %d bytes", receipt, len(message))
							}
							if policy == "redact" && (strings.Contains(message, secret) || (receipt.Redaction.Findings > 0 && !strings.Contains(message, "[REDACTED:GITHUB_TOKEN:"))) {
								t.Fatal("redact policy did not transform the actual outbound message")
							}
							if policy != "redact" && (mode != "build" || receipt.AgentType == "cod") && !strings.Contains(message, secret) {
								t.Fatal("warn/off unexpectedly changed the outbound message")
							}
						}
						largest = max(largest, receipt.Bytes)
					}
					if result.TotalBytes != largest || (policy == "off" && findings != 0) || (policy != "off" && findings == 0) {
						t.Fatalf("final redaction/accounting evidence is wrong: %+v", result)
					}
					if policy != "off" && !strings.Contains(strings.Join(result.Warnings, "\n"), "secrets") {
						t.Fatalf("redaction policy silently affected the context: %+v", result)
					}
				})
			}
		}
	}
}

func TestContextInjectCobraChecksRedactedPayloadLimits(t *testing.T) {
	oldJSON, oldCfg := jsonOutput, cfg
	jsonOutput = true
	t.Cleanup(func() { jsonOutput, cfg = oldJSON, oldCfg })
	for _, mode := range []string{"files", "build", "pack"} {
		for _, limit := range []string{"bytes", "capacity"} {
			if mode == "files" && limit == "capacity" {
				continue
			}
			t.Run(mode+"/"+limit, func(t *testing.T) {
				cfg = config.Default()
				cfg.Redaction.Mode = "redact"
				cfg.Redaction.ExtraPatterns = map[string][]string{"CONTEXT_TEST": {"needle"}}
				deps := packInjectionTestDeps(t)
				pane := tmux.Pane{ID: "%1", Index: 1, Type: tmux.AgentClaude, PID: 321}
				deps.panes = func(string) ([]tmux.Pane, error) { return []tmux.Pane{pane}, nil }
				payload := strings.Repeat("x", 130) + "needle"
				projectDir, err := deps.project(t.Context(), "demo")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(projectDir, "README.md"), []byte(payload), 0600); err != nil {
					t.Fatal(err)
				}
				build := deps.build
				deps.build = func(ctx context.Context, opts ntmctx.BuildOptions) (*ntmctx.ContextPackFull, error) {
					pack, err := build(ctx, opts)
					if pack != nil {
						pack.RenderedPrompt = payload
					}
					return pack, err
				}
				deps.load = func(id string) (*state.ContextPack, error) {
					return &state.ContextPack{ID: id, AgentType: "cc", RenderedPrompt: payload}, nil
				}
				calls := 0
				deps.send = func(context.Context, tmux.Pane, string) error { calls++; return nil }
				args := []string{"demo", "--files", "README.md"}
				if mode == "build" {
					args = append(args, "--build")
				} else if mode == "pack" {
					args = []string{"demo", "--pack", "saved"}
				}
				if limit == "bytes" {
					bound := len(payload)
					if mode == "files" {
						raw, _, _, err := formatContextInjectContent(projectDir, []string{"README.md"}, 0)
						if err != nil {
							t.Fatal(err)
						}
						bound = len(raw)
					}
					args = append(args, "--max-bytes", fmt.Sprint(bound))
				} else {
					deps.usage = func(context.Context, tmux.Pane) (*ntmctx.TranscriptUsage, bool) {
						return &ntmctx.TranscriptUsage{Path: "/native/context", ContextWindow: 40, UpdatedAt: time.Now()}, true
					}
				}
				cmd := newContextInjectCmdWithDeps(&deps)
				var out bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(io.Discard)
				cmd.SilenceErrors, cmd.SilenceUsage = true, true
				cmd.SetArgs(args)
				err = cmd.ExecuteContext(t.Context())
				var result ContextInjectResult
				if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil {
					t.Fatalf("invalid context response: %v: %s", decodeErr, out.String())
				}
				if err == nil || result.Success || calls != 0 || !strings.Contains(result.Error, "after redaction") || len(result.Deliveries) != 1 {
					t.Fatalf("redaction expansion exceeded the final limit: result=%+v calls=%d error=%v", result, calls, err)
				}
				receipt := result.Deliveries[0]
				if receipt.Status != "not_attempted" || receipt.Redaction == nil || receipt.Redaction.Categories["CONTEXT_TEST"] != 1 {
					t.Fatalf("limit failure lost final-payload policy evidence: %+v", receipt)
				}
			})
		}
	}
}

// Drive both public entry points through real root startup and inherited flags.
// Only tmux is substituted; file reads, config merging, redaction, receipts,
// error exits and the guarded delivery protocol execute as shipped.
func TestContextInjectionProcessHonorsRedactionConfigAndFlags(t *testing.T) {
	secret := "ghp_" + strings.Repeat("B", 36)
	for _, surface := range []string{"cli", "robot"} {
		for _, tc := range []struct {
			name     string
			config   string
			flags    []string
			mode     string
			blocked  bool
			dryRun   bool
			findings bool
			text     string
		}{
			{name: "configured_block", mode: "block", blocked: true, findings: true},
			{name: "flag_redact", flags: []string{"--redact=redact"}, mode: "redact", findings: true},
			{name: "allow_secret", flags: []string{"--redact=block", "--allow-secret"}, mode: "warn", findings: true},
			{name: "flag_off", flags: []string{"--redact=off"}, mode: "off"},
			{name: "blocked_dry_run", mode: "block", blocked: true, dryRun: true, findings: true},
			{name: "redacted_dry_run", flags: []string{"--redact=redact"}, mode: "redact", dryRun: true, findings: true},
			{name: "allowlist", config: "allowlist = [\"" + secret + "\"]\n", mode: "block"},
			{name: "disabled_category", config: "disabled_categories = [\"GITHUB_TOKEN\"]\n", mode: "block"},
			{name: "custom_pattern", config: "[redaction.extra_patterns]\nCONTEXT_TOKEN = [\"acme-private-credential\"]\n", flags: []string{"--redact=redact"}, mode: "redact", findings: true, text: "acme-private-credential"},
		} {
			t.Run(surface+"/"+tc.name, func(t *testing.T) {
				dir, mutationsPath := installContextMutationTMUX(t, tmux.AgentClaude, "")
				text := tc.text
				if text == "" {
					text = secret
				}
				if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("Project context\n"+text), 0600); err != nil {
					t.Fatal(err)
				}
				configPath := filepath.Join(dir, "config.toml")
				if err := os.WriteFile(configPath, []byte("[redaction]\nmode = \"block\"\n"+tc.config), 0600); err != nil {
					t.Fatal(err)
				}
				args := append([]string{"--config=" + configPath}, tc.flags...)
				if surface == "robot" {
					args = append(args, "--robot-format=json", "--robot-context-inject=demo", "--inject-files=README.md")
					if tc.dryRun {
						args = append(args, "--inject-dry-run")
					}
				} else {
					args = append(args, "--json", "context", "inject", "demo", "--files=README.md")
					if tc.dryRun {
						args = append(args, "--dry-run")
					}
				}
				raw, code := newRobotProcessRunner(t)(args...)
				encoded, err := json.Marshal(raw)
				if err != nil {
					t.Fatal(err)
				}
				var result ContextInjectResult
				if err := json.Unmarshal(encoded, &result); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(encoded), text) {
					t.Fatal("public response leaked the credential")
				}
				if result.Success == tc.blocked || result.DryRun != tc.dryRun || len(result.Deliveries) != 1 || (code != 0) != tc.blocked {
					t.Fatalf("root surface policy/exit mismatch: code=%d response=%s", code, encoded)
				}
				receipt := result.Deliveries[0]
				if receipt.Redaction == nil || receipt.Redaction.Mode != tc.mode || (receipt.Redaction.Findings > 0) != tc.findings {
					t.Fatalf("configured policy was not applied: %+v", receipt)
				}
				mutations, err := os.ReadFile(mutationsPath)
				if err != nil {
					t.Fatal(err)
				}
				if tc.blocked || tc.dryRun {
					if len(mutations) != 0 || len(result.PanesInjected) != 0 {
						t.Fatalf("block/dry-run performed delivery: result=%+v mutations=%s", result, mutations)
					}
					if tc.blocked && (result.ErrorCode != "SENSITIVE_DATA_BLOCKED" || receipt.Status != "not_attempted") {
						t.Fatalf("block lost structured refusal: %+v", result)
					}
					if !tc.blocked && (!reflect.DeepEqual(result.PanesPlanned, []int{1}) || receipt.Status != "planned") {
						t.Fatalf("robot dry-run claimed delivery: %+v", result)
					}
					return
				}
				payload, err := os.ReadFile(filepath.Join(dir, "payload"))
				if err != nil {
					t.Fatal(err)
				}
				if receipt.Status != "delivered" || !reflect.DeepEqual(result.PanesInjected, []int{1}) || receipt.Bytes != len(payload) || result.TotalBytes != len(payload) {
					t.Fatalf("delivery receipt describes different bytes: result=%+v payload size=%d", result, len(payload))
				}
				if tc.mode == "redact" {
					if strings.Contains(string(payload), text) || !strings.Contains(string(payload), "[REDACTED:") {
						t.Fatal("final-message redaction was bypassed by actual tmux delivery")
					}
				} else if !strings.Contains(string(payload), text) {
					t.Fatal("explicit passthrough policy changed the actual payload")
				}
				if tc.findings && len(result.Warnings) == 0 {
					t.Fatal("warn/redact policy did not report its findings")
				}
			})
		}
	}
}

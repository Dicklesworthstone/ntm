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
	for name, content := range map[string]string{
		"pid":       "321\n",
		"rotation":  "\n",
		"command":   "agent\n",
		"mutations": "",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}

	script := `#!/bin/sh
set -eu
record_input() {
  printf '%s\n' "$1" >> "$NTM_CONTEXT_GUARD_DIR/mutations"
  if [ ! -f "$NTM_CONTEXT_GUARD_DIR/first" ]; then
    : > "$NTM_CONTEXT_GUARD_DIR/first"
    case "$NTM_CONTEXT_GUARD_AFTER_WRITE" in
      replace) printf '999\n' > "$NTM_CONTEXT_GUARD_DIR/pid" ;;
      shell) printf 'bash\n' > "$NTM_CONTEXT_GUARD_DIR/command" ;;
    esac
  fi
}
case "$1" in
  load-buffer)
    cat > /dev/null
    ;;
  display-message)
    printf '[%s]\n' "$NTM_CONTEXT_GUARD_TYPE"
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
			writes:  2,
		},
		{
			name:    "literal_chunks",
			kind:    tmux.AgentGrok,
			content: strings.Repeat("x", 8193),
			writes:  4,
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
								}
								if result.Deliveries[0].Status != wantStatus {
									t.Fatalf("wrong delivery receipt: %+v", result.Deliveries[0])
								}
								injected = result.PanesInjected
								failureText = result.Error
							} else {
								prepare()
								injected, deliveryErr = injectContextIntoPanes(
									t.Context(), "demo", []tmux.Pane{pane},
									protocol.content, false, sendContextToPane,
								)
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
								if len(injected) != 0 || !strings.Contains(failureText, tmux.ErrPaneIdentityChanged.Error()) {
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
							wantEnters := 1
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
			if !strings.Contains(err.Error(), wantError) {
				t.Fatalf("refused for the wrong reason: %v; want %q", err, wantError)
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

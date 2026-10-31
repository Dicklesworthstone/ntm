package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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

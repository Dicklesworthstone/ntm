package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/auth"
	"github.com/Dicklesworthstone/ntm/internal/config"
	ctxmon "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/quota"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Exercise the public command against the actual rotation engine and recording
// tmux transport. The default coordinator policy requires manual confirmation;
// successful JSON must correspond to a submitted operation, not dequeuing it.
func TestContextConfirmCommandExecutesPersistedChoice(t *testing.T) {
	for _, mode := range []string{"rotate", "rotate-assigned", "rotate-unregistered", "manifest-race", "handoff-unknown", "compact", "launch-failure", "changed-pid", "busy", "canceled", "cross-session"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			workDir := filepath.Join(root, "project")
			if err := os.MkdirAll(workDir, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", root)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
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
			// Provider accounting timestamps establish freshness; file mtimes
			// alone must not make old token usage authorize compaction.
			past := time.Now().Add(-time.Second)
			baseline := `{"timestamp":"` + past.UTC().Format(time.RFC3339Nano) + `","type":"assistant","message":{"model":"claude-opus-4","usage":{"input_tokens":150000}}}`
			if err := os.WriteFile(transcript, []byte(baseline+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(transcript, past, past); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NTM_CONFIRM_TRANSCRIPT", transcript)
			t.Setenv("NTM_CONFIRM_COMPACTED_LINE", `{"timestamp":"`+time.Now().UTC().Format(time.RFC3339Nano)+`","type":"assistant","message":{"model":"claude-opus-4","usage":{"input_tokens":30000}}}`)
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
			pending := &ctxmon.PendingRotation{AgentID: "demo__cc_1", SessionName: "demo", PaneID: "%1", PanePID: 123, PaneType: "cc", WorkDir: workDir, ContextPercent: 75, CreatedAt: time.Now(), TimeoutAt: time.Now().Add(time.Hour)}
			if err := ctxmon.AddPendingRotation(pending); err != nil {
				t.Fatal(err)
			}
			tracked := mode == "rotate-assigned" || mode == "rotate-unregistered" || mode == "manifest-race" || mode == "handoff-unknown"
			registered := tracked && mode != "rotate-unregistered"
			var originalAssignment *assignment.Assignment
			var originalManifest *resilience.SpawnManifest
			if tracked {
				now := time.Now().UTC()
				originalAssignment = &assignment.Assignment{
					BeadID: "bd-rotation", BeadTitle: "Continue durable scheduling", Pane: 1, AgentType: "claude", AgentName: "local-pane-1",
					Status: assignment.StatusWorking, AssignedAt: now, StartedAt: &now,
					IdempotencyKey: "original-generation", ClaimActor: "original-claim-actor", ClaimState: assignment.ClaimClaimed,
					ClaimStatus: "in_progress", ClaimedAt: &now, DispatchState: assignment.DispatchSent,
					DispatchTarget: "%1", OccupancyKey: "%1", DispatchReceiptID: "original-delivery-receipt", DispatchedAt: &now,
					PromptSent: "Continue durable scheduling", PromptSHA256: strings.Repeat("a", 64), IntentSHA256: strings.Repeat("b", 64),
				}
				if registered {
					expires := now.Add(time.Hour)
					originalAssignment.AgentName = "BlueLake"
					originalAssignment.ReservationRequired = true
					originalAssignment.ReservationState = assignment.ReservationReserved
					originalAssignment.ReservationCompleted = true
					originalAssignment.ReservationAgent, originalAssignment.ReservationTarget = "BlueLake", "%1"
					originalAssignment.ReservationRequested = []string{"internal/scheduler.go"}
					originalAssignment.ReservedPaths = []string{"internal/scheduler.go"}
					originalAssignment.ReservationIDs, originalAssignment.ReservationExpiresAt = []int{41}, &expires
					registry := agentmail.NewSessionAgentRegistry("demo", workDir)
					registry.AddAgent(pending.AgentID, "%1", "BlueLake")
					registry.SetPanePID("%1", 123)
					registry.SetRegistrationToken("BlueLake", "original-registration-token")
					if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
						t.Fatal(err)
					}
					t.Setenv("NTM_CONFIRM_IDENTITY", agentmail.CanonicalIdentityPath(workDir, "%99"))
				}
				store := assignment.NewStore("demo")
				store.Assignments[originalAssignment.BeadID] = originalAssignment
				if err := store.Save(); err != nil {
					t.Fatal(err)
				}
				t.Setenv("NTM_CONFIRM_ASSIGNMENTS", filepath.Join(assignment.StorageDir(), "demo", "assignments.json"))
				originalManifest = &resilience.SpawnManifest{Session: "demo", ProjectDir: workDir, AutoRestart: true, ConfigPath: "/operator-config.toml",
					Agents: []resilience.AgentConfig{{PaneID: "%1", PaneIndex: 1, ProjectDir: workDir, Type: "claude", Model: "claude-opus-4", Command: "claude --original-model"}}}
				if err := resilience.SaveManifest(originalManifest); err != nil {
					t.Fatal(err)
				}
				if mode == "manifest-race" {
					changed := *originalManifest
					changed.Agents = append([]resilience.AgentConfig(nil), originalManifest.Agents...)
					changed.Agents[0].Command = "concurrently changed launch command"
					encoded, err := json.Marshal(changed)
					if err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(root, "changed-manifest.json")
					if err := os.WriteFile(path, encoded, 0600); err != nil {
						t.Fatal(err)
					}
					t.Setenv("NTM_CONFIRM_CHANGED_MANIFEST", path)
					t.Setenv("NTM_CONFIRM_MANIFEST", filepath.Join(resilience.ManifestDir(), "demo.json"))
				}
			}
			assertCommittedOwnership := func() {
				t.Helper()
				store, err := assignment.LoadStoreStrict("demo")
				if err != nil {
					t.Fatal(err)
				}
				current := store.Get(originalAssignment.BeadID)
				if current == nil || current.DispatchTarget != "%99" || current.OccupancyKey != "%99" || current.Pane != 1 || current.PaneContinuation == nil || current.PaneContinuation.CompletedAt == nil {
					t.Fatalf("physical assignment ownership did not continue: %+v", current)
				}
				if registered && current.ReservationTarget != "%99" {
					t.Fatalf("lease ownership remained on predecessor: %+v", current)
				}
				current.Pane, current.DispatchTarget, current.OccupancyKey, current.ReservationTarget = originalAssignment.Pane, originalAssignment.DispatchTarget, originalAssignment.OccupancyKey, originalAssignment.ReservationTarget
				current.PaneContinuation = nil
				if !reflect.DeepEqual(current, originalAssignment) {
					t.Fatalf("rotation changed task generation, claim, lease, or delivery evidence:\n got %+v\nwant %+v", current, originalAssignment)
				}
				manifest, err := resilience.LoadManifest("demo")
				if err != nil || len(manifest.Agents) != 1 || manifest.Agents[0].PaneID != "%99" || manifest.Agents[0].PaneIndex != 1 || !manifest.AutoRestart || manifest.ConfigPath != originalManifest.ConfigPath || manifest.Agents[0].Command != originalManifest.Agents[0].Command {
					t.Fatalf("restart ownership or operator policy changed: %+v, %v", manifest, err)
				}
				if registered {
					registry, err := agentmail.LoadSessionAgentRegistry("demo", workDir)
					if err != nil || registry == nil || registry.PaneIDMap["%99"] != "BlueLake" || registry.PaneIDMap["%1"] != "" || registry.PanePID("%99") != 321 || registry.RegistrationToken("BlueLake") != "original-registration-token" {
						t.Fatalf("Agent Mail identity did not follow the process: %+v, %v", registry, err)
					}
				}
				for _, suffix := range []string{"1", "99"} {
					if suffix == "1" {
						if _, err := os.Stat(filepath.Join(root, "retired")); err != nil {
							t.Fatal("committed rotation did not retire its predecessor")
						}
						continue
					}
					if data, err := os.ReadFile(filepath.Join(root, "token."+suffix)); err != nil || len(data) != 0 {
						t.Fatalf("replacement remained quarantined after durable commit: %q, %v", data, err)
					}
				}
			}
			action := "rotate"
			if mode == "compact" || mode == "cross-session" {
				action = "compact"
			}
			run := func(ctx context.Context, extra ...string) (ConfirmRotationResult, error) {
				command := newRotateContextConfirmCmd()
				// Match the production root policy when exercising the command
				// directly: failures retain one parseable JSON outcome.
				command.SilenceUsage, command.SilenceErrors = rootCmd.SilenceUsage, rootCmd.SilenceErrors
				var out bytes.Buffer
				command.SetOut(&out)
				command.SetErr(new(bytes.Buffer))
				command.SetArgs(append([]string{pending.AgentID, "--action=" + action}, extra...))
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
			if tracked && !strings.Contains(log, "OWNERSHIP_BEFORE_LAUNCH") {
				t.Fatalf("replacement boot did not observe its durable assignment barrier and original identity:\n%s", log)
			}
			wantSuccess := strings.HasPrefix(mode, "rotate") || mode == "compact"
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
				if strings.HasPrefix(mode, "rotate") {
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
					if tracked {
						assertCommittedOwnership()
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
				if mode != "launch-failure" && mode != "manifest-race" && mode != "handoff-unknown" && (strings.Contains(log, "send-keys") || strings.Contains(log, "paste-buffer") || strings.Contains(log, "kill-pane")) {
					t.Fatalf("unsafe or canceled confirmation delivered input:\n%s", log)
				}
				if mode == "manifest-race" || mode == "handoff-unknown" {
					if strings.Contains(log, "kill-pane") || stored.Continuation == nil || stored.Continuation.Replacement.ID != "%99" {
						t.Fatalf("partial continuation lost its recovery source: %+v\n%s", stored, log)
					}
					store, loadErr := assignment.LoadStoreStrict("demo")
					if loadErr != nil {
						t.Fatal(loadErr)
					}
					current := store.Get(originalAssignment.BeadID)
					if current == nil || current.DispatchTarget != "%1" || !current.HasPendingPaneContinuation() || current.PaneContinuation.TargetPane != "%99" || current.IdempotencyKey != originalAssignment.IdempotencyKey {
						t.Fatalf("partial handoff released or replaced assignment ownership: %+v", current)
					}
					for _, suffix := range []string{"1", "99"} {
						if data, readErr := os.ReadFile(filepath.Join(root, "token."+suffix)); readErr != nil || strings.TrimSpace(string(data)) != stored.ExecutionID {
							t.Fatalf("partial handoff lost pane %s quarantine: %q, %v", suffix, data, readErr)
						}
					}
					if mode == "manifest-race" {
						if stored.Continuation.Phase != "handoff_accepted" {
							t.Fatalf("accepted handoff was not recoverable: %+v", stored.Continuation)
						}
						if err := resilience.SaveManifest(originalManifest); err != nil {
							t.Fatal(err)
						}
						recovered, recoverErr := run(t.Context(), "--retry")
						if recoverErr != nil || !recovered.Success {
							t.Fatalf("verified handoff did not resume ownership finalization: %+v, %v", recovered, recoverErr)
						}
						assertCommittedOwnership()
						after, _ := os.ReadFile(filepath.Join(root, "commands"))
						if strings.Count(string(after), "HANDOFF_DELIVERED") != 1 || strings.Count(string(after), "split-window ") != 1 {
							t.Fatalf("ownership recovery replayed launch or task delivery:\n%s", after)
						}
					} else {
						before := len(commands)
						for _, extra := range [][]string{{"--retry"}, {"--action=ignore", "--retry"}} {
							repeated, repeatErr := run(t.Context(), extra...)
							if repeatErr == nil || repeated.Success {
								t.Fatalf("unknown handoff allowed replay or unsafe ignore: %+v, %v", repeated, repeatErr)
							}
						}
						after, _ := os.ReadFile(filepath.Join(root, "commands"))
						if len(after) != before {
							t.Fatalf("unknown handoff retry performed more tmux operations:\n%s", after)
						}
					}
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
  display-message)
    case "$*" in
      *'#{@ntm_rotation_token}'*) printf '['; if [ -f "$NTM_CONFIRM_ROOT/token.${target#%}" ]; then cat "$NTM_CONFIRM_ROOT/token.${target#%}"; fi; printf ']\n' ;;
      *) printf '%s\n' "$NTM_CONFIRM_WORKDIR" ;;
    esac
    ;;
  if-shell)
    for value do
      case "$value" in
        'set-option -p -u '*) : > "$NTM_CONFIRM_ROOT/token.${target#%}" ;;
        'set-option -p -t '*) printf '%s' "${value##* }" > "$NTM_CONFIRM_ROOT/token.${target#%}" ;;
      esac
    done
    ;;
  list-panes)
    prefix=
    case "$*" in *' -a '*) prefix=demo_NTM_SEP_ ;; esac
    if [ ! -f "$NTM_CONFIRM_ROOT/retired" ]; then
      pid=123
      if [ "$NTM_CONFIRM_MODE" = changed-pid ]; then pid=456; fi
      index=1
      if [ -f "$NTM_CONFIRM_ROOT/created" ]; then index=2; fi
      token=
      if [ -f "$NTM_CONFIRM_ROOT/token.1" ]; then token=$(cat "$NTM_CONFIRM_ROOT/token.1"); fi
      printf '%s%%1_NTM_SEP_%s_NTM_SEP_demo__cc_1_NTM_SEP_node_NTM_SEP_120_NTM_SEP_40_NTM_SEP_0_NTM_SEP_%s_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[%s]\n' "$prefix" "$index" "$pid" "$token"
    fi
    if [ -f "$NTM_CONFIRM_ROOT/created" ]; then
      command=bash
      if [ -f "$NTM_CONFIRM_ROOT/launched" ]; then command=node; fi
      token=
      if [ -f "$NTM_CONFIRM_ROOT/token.99" ]; then token=$(cat "$NTM_CONFIRM_ROOT/token.99"); fi
      printf '%s%%99_NTM_SEP_1_NTM_SEP_demo__cc_1_NTM_SEP_%s_NTM_SEP_120_NTM_SEP_40_NTM_SEP_0_NTM_SEP_321_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[%s]\n' "$prefix" "$command" "$token"
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
      if [ "$NTM_CONFIRM_MODE" = manifest-race ]; then cp "$NTM_CONFIRM_CHANGED_MANIFEST" "$NTM_CONFIRM_MANIFEST"; fi
      if [ "$NTM_CONFIRM_MODE" = handoff-unknown ]; then exit 1; fi
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
      *'/compact'*) printf '%s\n' "$NTM_CONFIRM_COMPACTED_LINE" > "$NTM_CONFIRM_TRANSCRIPT" ;;
    esac
    if [ "$target" = '%99' ]; then
      if [ ! -f "$NTM_CONFIRM_ROOT/launched" ] && [ -n "$NTM_CONFIRM_ASSIGNMENTS" ]; then
        [ -s "$NTM_CONFIRM_ROOT/token.1" ] && [ -s "$NTM_CONFIRM_ROOT/token.99" ] || exit 1
        grep -q '"target_pane": "%99"' "$NTM_CONFIRM_ASSIGNMENTS" || exit 1
        if [ -n "$NTM_CONFIRM_IDENTITY" ]; then
          [ -f "$NTM_CONFIRM_IDENTITY" ] && [ "$(cat "$NTM_CONFIRM_IDENTITY")" = BlueLake ] || exit 1
        fi
        printf 'OWNERSHIP_BEFORE_LAUNCH\n' >> "$NTM_CONFIRM_ROOT/commands"
      fi
      printf '1' > "$NTM_CONFIRM_ROOT/launched"
    fi
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

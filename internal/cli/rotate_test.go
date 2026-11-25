package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/auth"
	"github.com/Dicklesworthstone/ntm/internal/config"
	ctxmon "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/quota"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/rotation"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Exercise the public command against the actual rotation engine and recording
// tmux transport. The default coordinator policy requires manual confirmation;
// successful JSON must correspond to a submitted operation, not dequeuing it.
func TestContextConfirmCommandExecutesPersistedChoice(t *testing.T) {
	// recover-accept inspects the whole pending store expecting exactly 1 entry;
	// compact and cross-session call server-wide `list-panes -a` and probe every
	// Claude pane's current path via `display-message` (which would log into
	// sibling subtests' command logs if run concurrently). Run those 3 modes
	// sequentially with isolated stores, then run the 14 session-scoped modes
	// concurrently via the keyed tmux fixture.
	runSingleMode := func(t *testing.T, mode string, parallel bool, sharedRoot string, idx int) {
		t.Helper()
		recovery := strings.HasPrefix(mode, "recover-")
		var root, stageDir, workDir, session, agentID, oldPane, newPane, oldNumStr, newNumStr, pendingPath string
		if !parallel {
			root = t.TempDir()
			stageDir = root
			workDir = filepath.Join(root, "project")
			session = "demo"
			oldNumStr, newNumStr = "1", "99"
			oldPane, newPane = "%1", "%99"
			agentID = "demo__cc_1"
			pendingPath = filepath.Join(root, "pending.jsonl")
		} else {
			root = filepath.Join(sharedRoot, "cases", mode)
			stageDir = filepath.Join(sharedRoot, "staging-"+mode)
			workDir = filepath.Join(root, "project")
			session = "demo-" + mode
			oldNum := 10 + 2*idx
			newNum := 11 + 2*idx
			oldNumStr, newNumStr = strconv.Itoa(oldNum), strconv.Itoa(newNum)
			oldPane, newPane = "%"+oldNumStr, "%"+newNumStr
			agentID = session + "__cc_1"
			pendingPath = filepath.Join(sharedRoot, "pending.jsonl")
		}
		if err := os.MkdirAll(filepath.Join(stageDir, "project"), 0700); err != nil {
			t.Fatal(err)
		}
		effectiveMode := mode
		if recovery {
			// Submission happened, but the transport lost its acknowledgement.
			// Recovery must use the original execution without resending work.
			effectiveMode = "handoff-unknown"
		}
		transcript := filepath.Join(os.Getenv("HOME"), ".claude", "projects", ctxmon.MungeProjectPath(workDir), "agent.jsonl")
		if !parallel {
			t.Setenv("HOME", root)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
			t.Setenv("NTM_CONFIRM_ROOT", root)
			t.Setenv("NTM_CONFIRM_WORKDIR", workDir)
			t.Setenv("NTM_CONFIRM_MODE", effectiveMode)
			stub := filepath.Join(root, "tmux")
			if err := os.WriteFile(stub, []byte(contextConfirmTmuxFixture), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NTM_TMUX_BINARY", stub)
			transcript = filepath.Join(root, ".claude", "projects", ctxmon.MungeProjectPath(workDir), "agent.jsonl")
		}
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
		compactedLine := `{"timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","type":"assistant","message":{"model":"claude-opus-4","usage":{"input_tokens":30000}}}`
		writeCaseFile := func(name, content string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if !parallel {
			t.Setenv("NTM_CONFIRM_TRANSCRIPT", transcript)
			t.Setenv("NTM_CONFIRM_COMPACTED_LINE", compactedLine)
			oldCfg, oldJSON, oldClient := cfg, jsonOutput, tmux.DefaultClient
			oldStore, oldHistory := ctxmon.DefaultPendingRotationStore, ctxmon.DefaultRotationHistoryStore
			oldRotDelay := ctxmon.MaxRotationDelay
			fastClient := tmux.NewClient("")
			fastClient.MaxSendDelay = 1 * time.Millisecond
			cfg, jsonOutput, tmux.DefaultClient = config.Default(), true, fastClient
			ctxmon.MaxRotationDelay = 1 * time.Millisecond
			if cfg.Rotation.AutoConfirm {
				t.Fatal("fixture must exercise manual confirmation with default auto-confirm disabled")
			}
			if !cfg.ContextRotation.TryCompactFirst {
				t.Fatal("fixture must preserve the default automatic compaction preference")
			}
			ctxmon.DefaultPendingRotationStore = ctxmon.NewPendingRotationStoreWithPath(pendingPath)
			ctxmon.DefaultRotationHistoryStore = ctxmon.NewRotationHistoryStore()
			t.Cleanup(func() {
				cfg, jsonOutput, tmux.DefaultClient = oldCfg, oldJSON, oldClient
				ctxmon.DefaultPendingRotationStore, ctxmon.DefaultRotationHistoryStore = oldStore, oldHistory
				ctxmon.MaxRotationDelay = oldRotDelay
			})
		} else {
			for name, val := range map[string]string{
				"mode":       effectiveMode,
				"workdir":    workDir,
				"session":    session,
				"agent_id":   agentID,
				"oldnum":     oldNumStr,
				"newnum":     newNumStr,
				"transcript": transcript,
				"compacted":  compactedLine,
			} {
				if err := os.WriteFile(filepath.Join(stageDir, name), []byte(val+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Publish by-target entries atomically before atomically renaming the
			// fully populated case directory into cases/<mode>.
			for _, key := range []string{session, oldPane, newPane} {
				tmpTarget := filepath.Join(sharedRoot, "by-target-tmp-"+mode+"-"+key)
				if err := os.WriteFile(tmpTarget, []byte(root+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(tmpTarget, filepath.Join(sharedRoot, "by-target", key)); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Rename(stageDir, root); err != nil {
				t.Fatal(err)
			}
		}
		pending := &ctxmon.PendingRotation{AgentID: agentID, SessionName: session, PaneID: oldPane, PanePID: 123, PaneType: "cc", WorkDir: workDir, ContextPercent: 75, CreatedAt: time.Now(), TimeoutAt: time.Now().Add(time.Hour)}
		if err := ctxmon.AddPendingRotation(pending); err != nil {
			t.Fatal(err)
		}
		tracked := mode == "rotate-assigned" || mode == "rotate-unregistered" || mode == "manifest-race" || mode == "handoff-unknown" || recovery
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
				DispatchTarget: oldPane, OccupancyKey: oldPane, DispatchReceiptID: "original-delivery-receipt", DispatchedAt: &now,
				PromptSent: "Continue durable scheduling", PromptSHA256: strings.Repeat("a", 64), IntentSHA256: strings.Repeat("b", 64),
			}
			if registered {
				expires := now.Add(time.Hour)
				originalAssignment.AgentName = "BlueLake"
				originalAssignment.ReservationRequired = true
				originalAssignment.ReservationState = assignment.ReservationReserved
				originalAssignment.ReservationCompleted = true
				originalAssignment.ReservationAgent, originalAssignment.ReservationTarget = "BlueLake", oldPane
				originalAssignment.ReservationRequested = []string{"internal/scheduler.go"}
				originalAssignment.ReservedPaths = []string{"internal/scheduler.go"}
				originalAssignment.ReservationIDs, originalAssignment.ReservationExpiresAt = []int{41}, &expires
				registry := agentmail.NewSessionAgentRegistry(session, workDir)
				registry.AddAgent(pending.AgentID, oldPane, "BlueLake")
				registry.SetPanePID(oldPane, 123)
				registry.SetRegistrationToken("BlueLake", "original-registration-token")
				if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
					t.Fatal(err)
				}
				identityPath := agentmail.CanonicalIdentityPath(workDir, newPane)
				if !parallel {
					t.Setenv("NTM_CONFIRM_IDENTITY", identityPath)
				} else {
					writeCaseFile("identity", identityPath)
				}
			}
			store := assignment.NewStore(session)
			store.Assignments[originalAssignment.BeadID] = originalAssignment
			if err := store.Save(); err != nil {
				t.Fatal(err)
			}
			assignmentsPath := filepath.Join(assignment.StorageDir(), session, "assignments.json")
			if !parallel {
				t.Setenv("NTM_CONFIRM_ASSIGNMENTS", assignmentsPath)
			} else {
				writeCaseFile("assignments", assignmentsPath)
			}
			originalManifest = &resilience.SpawnManifest{Session: session, ProjectDir: workDir, AutoRestart: true, ConfigPath: "/operator-config.toml",
				Agents: []resilience.AgentConfig{{PaneID: oldPane, PaneIndex: 1, ProjectDir: workDir, Type: "claude", Model: "claude-opus-4", Command: "claude --original-model"}}}
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
				manifestPath := filepath.Join(resilience.ManifestDir(), session+".json")
				if !parallel {
					t.Setenv("NTM_CONFIRM_CHANGED_MANIFEST", path)
					t.Setenv("NTM_CONFIRM_MANIFEST", manifestPath)
				} else {
					writeCaseFile("changed-manifest-path", path)
					writeCaseFile("manifest-path", manifestPath)
				}
			}
		}
		assertCommittedOwnership := func() {
			t.Helper()
			store, err := assignment.LoadStoreStrict(session)
			if err != nil {
				t.Fatal(err)
			}
			current := store.Get(originalAssignment.BeadID)
			if current == nil || current.DispatchTarget != newPane || current.OccupancyKey != newPane || current.Pane != 1 || current.PaneContinuation == nil || current.PaneContinuation.CompletedAt == nil {
				t.Fatalf("physical assignment ownership did not continue: %+v", current)
			}
			if registered && current.ReservationTarget != newPane {
				t.Fatalf("lease ownership remained on predecessor: %+v", current)
			}
			current.Pane, current.DispatchTarget, current.OccupancyKey, current.ReservationTarget = originalAssignment.Pane, originalAssignment.DispatchTarget, originalAssignment.OccupancyKey, originalAssignment.ReservationTarget
			current.PaneContinuation = nil
			if !reflect.DeepEqual(current, originalAssignment) {
				t.Fatalf("rotation changed task generation, claim, lease, or delivery evidence:\n got %+v\nwant %+v", current, originalAssignment)
			}
			manifest, err := resilience.LoadManifest(session)
			if err != nil || len(manifest.Agents) != 1 || manifest.Agents[0].PaneID != newPane || manifest.Agents[0].PaneIndex != 1 || !manifest.AutoRestart || manifest.ConfigPath != originalManifest.ConfigPath || manifest.Agents[0].Command != originalManifest.Agents[0].Command {
				t.Fatalf("restart ownership or operator policy changed: %+v, %v", manifest, err)
			}
			if registered {
				registry, err := agentmail.LoadSessionAgentRegistry(session, workDir)
				if err != nil || registry == nil || registry.PaneIDMap[newPane] != "BlueLake" || registry.PaneIDMap[oldPane] != "" || registry.PanePID(newPane) != 321 || registry.RegistrationToken("BlueLake") != "original-registration-token" {
					t.Fatalf("Agent Mail identity did not follow the process: %+v, %v", registry, err)
				}
			}
			for _, suffix := range []string{oldNumStr, newNumStr} {
				if suffix == oldNumStr {
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
				if readErr != nil || !strings.Contains(string(payload), "Continue durable scheduler implementation") || result.Rotation.NewPaneID != newPane {
					t.Fatalf("replacement did not receive source handoff: %+v, %v, %q", result, readErr, payload)
				}
				deliveredAt, retiredAt := strings.Index(log, "HANDOFF_DELIVERED"), strings.Index(log, "kill-pane -t "+oldPane)
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
			if mode != "launch-failure" && mode != "manifest-race" && mode != "handoff-unknown" && !recovery && (strings.Contains(log, "send-keys") || strings.Contains(log, "paste-buffer") || strings.Contains(log, "kill-pane")) {
				t.Fatalf("unsafe or canceled confirmation delivered input:\n%s", log)
			}
			if mode == "manifest-race" || mode == "handoff-unknown" || recovery {
				if strings.Contains(log, "kill-pane") || stored.Continuation == nil || stored.Continuation.Replacement.ID != newPane {
					t.Fatalf("partial continuation lost its recovery source: %+v\n%s", stored, log)
				}
				store, loadErr := assignment.LoadStoreStrict(session)
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				current := store.Get(originalAssignment.BeadID)
				if current == nil || current.DispatchTarget != oldPane || !current.HasPendingPaneContinuation() || current.PaneContinuation.TargetPane != newPane || current.IdempotencyKey != originalAssignment.IdempotencyKey {
					t.Fatalf("partial handoff released or replaced assignment ownership: %+v", current)
				}
				for _, suffix := range []string{oldNumStr, newNumStr} {
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
					if recovery {
						accepted := exerciseContextRotationRecovery(t, mode, parallel, root, pendingPath, session, oldPane, newPane, oldNumStr, newNumStr, stored, originalAssignment, originalManifest)
						if accepted {
							assertCommittedOwnership()
						} else {
							before, _ := os.ReadFile(filepath.Join(root, "commands"))
							replayed, replayErr := run(t.Context())
							after, _ := os.ReadFile(filepath.Join(root, "commands"))
							if replayErr != nil || !replayed.Success || !strings.Contains(replayed.Message, "aborted") || strings.Contains(replayed.Message, "compaction") || !bytes.Equal(before, after) {
								t.Fatalf("confirmation misreported or repeated operator abort: %+v, %v\n%s", replayed, replayErr, after)
							}
						}
					}
				}
			}
		}
	}

	for _, seqMode := range []string{"recover-accept", "compact", "cross-session"} {
		t.Run(seqMode, func(t *testing.T) {
			runSingleMode(t, seqMode, false, "", 0)
		})
	}

	sharedRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sharedRoot, "by-target"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sharedRoot, "cases"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", sharedRoot)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(sharedRoot, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(sharedRoot, "data"))
	t.Setenv("NTM_CONFIRM_CASES_ROOT", sharedRoot)
	keyedStub := filepath.Join(sharedRoot, "tmux")
	if err := os.WriteFile(keyedStub, []byte(contextConfirmKeyedTmuxFixture), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", keyedStub)
	oldCfg, oldJSON, oldClient := cfg, jsonOutput, tmux.DefaultClient
	oldStore, oldHistory := ctxmon.DefaultPendingRotationStore, ctxmon.DefaultRotationHistoryStore
	oldRotDelay := ctxmon.MaxRotationDelay
	fastClient := tmux.NewClient("")
	fastClient.MaxSendDelay = 1 * time.Millisecond
	cfg, jsonOutput, tmux.DefaultClient = config.Default(), true, fastClient
	ctxmon.MaxRotationDelay = 1 * time.Millisecond
	if cfg.Rotation.AutoConfirm {
		t.Fatal("fixture must exercise manual confirmation with default auto-confirm disabled")
	}
	if !cfg.ContextRotation.TryCompactFirst {
		t.Fatal("fixture must preserve the default automatic compaction preference")
	}
	ctxmon.DefaultPendingRotationStore = ctxmon.NewPendingRotationStoreWithPath(filepath.Join(sharedRoot, "pending.jsonl"))
	ctxmon.DefaultRotationHistoryStore = ctxmon.NewRotationHistoryStore()
	t.Cleanup(func() {
		cfg, jsonOutput, tmux.DefaultClient = oldCfg, oldJSON, oldClient
		ctxmon.DefaultPendingRotationStore, ctxmon.DefaultRotationHistoryStore = oldStore, oldHistory
		ctxmon.MaxRotationDelay = oldRotDelay
	})

	parallelModes := []string{
		"rotate", "rotate-assigned", "rotate-unregistered", "manifest-race", "handoff-unknown",
		"recover-abort", "recover-abort-kill-retry", "recover-abort-absence-retry",
		"recover-accept-manifest-race", "recover-changed-pid",
		"launch-failure", "changed-pid", "busy", "canceled",
	}
	for i, mode := range parallelModes {
		i, mode := i, mode
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			runSingleMode(t, mode, true, sharedRoot, i)
		})
	}
}

// Drive recovery through the registered public subcommand, after a real
// confirmation has reached an uncertain transport boundary.
func exerciseContextRotationRecovery(t *testing.T, mode string, parallel bool, root, pendingPath, session, oldPane, newPane, oldNumStr, newNumStr string, pending *ctxmon.PendingRotation, original *assignment.Assignment, manifest *resilience.SpawnManifest) bool {
	t.Helper()
	const reason = "Inspected both panes and chose the recorded recovery action"
	executionID := pending.ExecutionID
	if executionID == "" || pending.Continuation == nil || pending.Continuation.Phase != "handoff_started" {
		t.Fatalf("fixture did not produce an uncertain durable execution: %+v", pending)
	}
	run := func(ctx context.Context, token, action, explanation string) (RecoverRotationResult, error) {
		t.Helper()
		command := newRotateContextCmd()
		command.SilenceUsage, command.SilenceErrors = true, true
		var out bytes.Buffer
		command.SetOut(&out)
		command.SetErr(new(bytes.Buffer))
		command.SetArgs([]string{"recover", pending.AgentID, "--execution-id=" + token, "--action=" + action, "--reason=" + explanation})
		err := command.ExecuteContext(ctx)
		var result RecoverRotationResult
		if decodeErr := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &result); decodeErr != nil {
			t.Fatalf("recovery did not emit one structured outcome: %v, command error=%v, output=%q", decodeErr, err, out.String())
		}
		return result, err
	}
	assertFenced := func() {
		t.Helper()
		store, err := assignment.LoadStoreStrict(session)
		if err != nil {
			t.Fatal(err)
		}
		current := store.Get(original.BeadID)
		if current == nil || current.DispatchTarget != oldPane || !current.HasPendingPaneContinuation() || current.IdempotencyKey != original.IdempotencyKey {
			t.Fatalf("failed recovery released the original assignment: %+v", current)
		}
		for _, suffix := range []string{oldNumStr, newNumStr} {
			data, err := os.ReadFile(filepath.Join(root, "token."+suffix))
			if err != nil || strings.TrimSpace(string(data)) != executionID {
				t.Fatalf("failed recovery lost pane %s quarantine: %q, %v", suffix, data, err)
			}
		}
		if _, err := os.Stat(filepath.Join(root, "retired")); !os.IsNotExist(err) {
			t.Fatalf("failed recovery retired the original: %v", err)
		}
	}

	if mode == "recover-accept" {
		text, err := captureStdout(t, func() error {
			command := newRotateContextCmd()
			command.SetArgs([]string{"pending"})
			return command.ExecuteContext(t.Context())
		})
		var visible PendingRotationsResult
		if err != nil || json.Unmarshal([]byte(text), &visible) != nil || len(visible.Pending) != 1 {
			t.Fatalf("pending command did not expose recoverable execution: %v, %s", err, text)
		}
		info := visible.Pending[0]
		if info.ExecutionID != executionID || info.ContinuationPhase != "handoff_started" || info.SourcePaneID != oldPane || info.SourcePanePID != 123 || info.ReplacementPaneID != newPane || info.ReplacementPanePID != 321 || info.RetryAllowed || !reflect.DeepEqual(info.RecoveryActions, []ctxmon.RotationResolutionAction{ctxmon.RotationResolutionAccept, ctxmon.RotationResolutionAbort}) {
			t.Fatalf("pending command gave incomplete or unsafe recovery guidance: %+v", info)
		}
		if strings.Contains(text, "original-registration-token") || strings.Contains(text, "claude --original-model") || strings.Contains(text, "/operator-config.toml") {
			t.Fatalf("pending output disclosed private launch or registry state: %s", text)
		}
		before, _ := os.ReadFile(filepath.Join(root, "commands"))
		journalBefore, err := os.ReadFile(pendingPath)
		if err != nil {
			t.Fatal(err)
		}
		for _, invalid := range []struct{ token, action, explanation string }{
			{"stale-execution", "accept", reason},
			{executionID, "accept", "  "},
			{executionID, "rotate", reason},
		} {
			result, err := run(t.Context(), invalid.token, invalid.action, invalid.explanation)
			if err == nil || result.Success {
				t.Fatalf("invalid recovery request succeeded: %+v, %v", result, err)
			}
		}
		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		result, err := run(canceled, executionID, "accept", reason)
		if err == nil || result.Success {
			t.Fatalf("pre-canceled recovery succeeded: %+v, %v", result, err)
		}
		after, _ := os.ReadFile(filepath.Join(root, "commands"))
		journalAfter, _ := os.ReadFile(pendingPath)
		if !bytes.Equal(before, after) || !bytes.Equal(journalBefore, journalAfter) {
			t.Fatalf("invalid or canceled recovery changed execution state:\n%s", after)
		}
	}

	accepted := !strings.HasPrefix(mode, "recover-abort")
	action := "abort"
	opposite := "accept"
	if accepted {
		action, opposite = opposite, action
	}
	setToggle := func(envKey, fileName, val string) {
		t.Helper()
		if !parallel {
			t.Setenv(envKey, val)
			return
		}
		targetPath := filepath.Join(root, fileName)
		tmpPath := targetPath + ".tmp"
		if err := os.WriteFile(tmpPath, []byte(val), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmpPath, targetPath); err != nil {
			t.Fatal(err)
		}
	}
	switch mode {
	case "recover-abort-kill-retry":
		setToggle("NTM_CONFIRM_KILL_FAILURE", "kill-failure", "1")
	case "recover-abort-absence-retry":
		setToggle("NTM_CONFIRM_KEEP_REPLACEMENT", "keep-replacement", "1")
	case "recover-changed-pid":
		setToggle("NTM_CONFIRM_REPLACEMENT_PID", "replacement-pid", "654")
	case "recover-accept-manifest-race":
		changed := *manifest
		changed.Agents = append([]resilience.AgentConfig(nil), manifest.Agents...)
		changed.Agents[0].Command = "concurrently changed launch command"
		if err := resilience.SaveManifest(&changed); err != nil {
			t.Fatal(err)
		}
	}
	if mode != "recover-accept" && mode != "recover-abort" {
		result, err := run(t.Context(), executionID, action, reason)
		if err == nil || result.Success {
			t.Fatalf("recovery ignored a changed process or incomplete side effect: %+v, %v", result, err)
		}
		assertFenced()
		if mode == "recover-changed-pid" {
			commands, _ := os.ReadFile(filepath.Join(root, "commands"))
			if strings.Contains(string(commands), "kill-pane") {
				t.Fatalf("recovery killed a changed process:\n%s", commands)
			}
		}
		setToggle("NTM_CONFIRM_KILL_FAILURE", "kill-failure", "")
		setToggle("NTM_CONFIRM_KEEP_REPLACEMENT", "keep-replacement", "")
		setToggle("NTM_CONFIRM_REPLACEMENT_PID", "replacement-pid", "")
		if mode == "recover-accept-manifest-race" {
			if err := resilience.SaveManifest(manifest); err != nil {
				t.Fatal(err)
			}
		}
	}
	result, err := run(t.Context(), executionID, action, reason)
	if err != nil || !result.Success || result.Rotation == nil || result.ExecutionID != executionID || string(result.Rotation.ResolutionAction) != action {
		t.Fatalf("explicit recovery did not complete: %+v, %v", result, err)
	}
	journal, err := os.ReadFile(pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	var durable ctxmon.StoredPendingRotation
	foundDurable := false
	for _, line := range bytes.Split(bytes.TrimSpace(journal), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var candidate ctxmon.StoredPendingRotation
		if uerr := json.Unmarshal(line, &candidate); uerr != nil {
			t.Fatalf("corrupt pending journal line %q: %v", line, uerr)
		}
		if candidate.AgentID == pending.AgentID {
			durable = candidate
			foundDurable = true
		}
	}
	if !foundDurable || durable.Continuation == nil || durable.Continuation.Resolution == nil || durable.Continuation.Resolution.Reason != reason || durable.Continuation.Resolution.RecordedAt.IsZero() || string(durable.Continuation.Resolution.Action) != action || durable.ExecutionID != executionID || durable.ExecutionState != ctxmon.RotationStateCompleted {
		t.Fatalf("operator decision or execution was not durable: found=%t %+v", foundDurable, durable)
	}
	if !accepted {
		if result.Rotation.State != ctxmon.RotationStateAborted || result.Rotation.NewPaneID != "" || result.Rotation.OldPaneID != oldPane {
			t.Fatalf("abort receipt claimed a completed replacement: %+v", result.Rotation)
		}
		store, err := assignment.LoadStoreStrict(session)
		if err != nil || !reflect.DeepEqual(store.Get(original.BeadID), original) {
			t.Fatalf("abort changed the original task, claim, or lease: %+v, %v", store, err)
		}
		currentManifest, err := resilience.LoadManifest(session)
		if err != nil || !reflect.DeepEqual(currentManifest, manifest) {
			t.Fatalf("abort changed restart ownership: %+v, %v", currentManifest, err)
		}
		registry, err := agentmail.LoadSessionAgentRegistry(session, pending.WorkDir)
		if err != nil || registry == nil || registry.PaneIDMap[oldPane] != "BlueLake" || registry.PanePID(oldPane) != 123 || registry.PaneIDMap[newPane] != "" || registry.RegistrationToken("BlueLake") != "original-registration-token" {
			t.Fatalf("abort changed the original registered identity: %+v, %v", registry, err)
		}
		if _, err := os.Stat(filepath.Join(root, "replacement-retired")); err != nil {
			t.Fatalf("abort completed without stopping the replacement: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "retired")); !os.IsNotExist(err) {
			t.Fatalf("abort retired the original: %v", err)
		}
		if token, err := os.ReadFile(filepath.Join(root, "token."+oldNumStr)); err != nil || len(token) != 0 {
			t.Fatalf("abort did not release the original after replacement removal: %q, %v", token, err)
		}
	}
	before, _ := os.ReadFile(filepath.Join(root, "commands"))
	if strings.Count(string(before), "HANDOFF_DELIVERED") != 1 || strings.Count(string(before), "split-window ") != 1 {
		t.Fatalf("recovery relaunched an agent or resent its handoff:\n%s", before)
	}
	replayed, replayErr := run(t.Context(), executionID, action, reason)
	if replayErr != nil || !reflect.DeepEqual(replayed.Rotation, result.Rotation) {
		t.Fatalf("same recovery decision did not replay its saved result: %+v, %v", replayed, replayErr)
	}
	for _, invalid := range []struct{ action, explanation string }{{opposite, reason}, {action, "rewrite the original decision"}} {
		conflict, err := run(t.Context(), executionID, invalid.action, invalid.explanation)
		if err == nil || conflict.Success {
			t.Fatalf("completed operator decision was mutable: %+v, %v", conflict, err)
		}
	}
	after, _ := os.ReadFile(filepath.Join(root, "commands"))
	if !bytes.Equal(before, after) {
		t.Fatalf("replaying or contradicting a completed decision performed more effects:\n%s", after)
	}
	return accepted
}

// A narrow terminal can omit every handoff marker even though the agent has
// completed its reply. Exercise the public confirmation command with a real
// process/session binding and the production transcript reader. A transcript
// in the right directory, or markers in a user/tool record, cannot retire the
// only agent that still owns the original context.
func TestContextConfirmCommandUsesBoundTranscriptSummary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Claude session records require Linux process start-time verification")
	}
	sharedRoot := t.TempDir()
	for _, sub := range []string{"cases", "by-target"} {
		if err := os.MkdirAll(filepath.Join(sharedRoot, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", sharedRoot)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(sharedRoot, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(sharedRoot, "data"))
	t.Setenv("NTM_CONFIRM_CASES_ROOT", sharedRoot)
	keyedStub := filepath.Join(sharedRoot, "tmux")
	if err := os.WriteFile(keyedStub, []byte(contextConfirmKeyedTmuxFixture), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", keyedStub)

	oldCfg, oldJSON, oldClient := cfg, jsonOutput, tmux.DefaultClient
	oldStore, oldHistory := ctxmon.DefaultPendingRotationStore, ctxmon.DefaultRotationHistoryStore
	oldDelay := ctxmon.MaxRotationDelay
	fastClient := tmux.NewClient("")
	fastClient.MaxSendDelay = 1 * time.Millisecond
	cfg, jsonOutput, tmux.DefaultClient = config.Default(), true, fastClient
	ctxmon.MaxRotationDelay = 1 * time.Millisecond
	ctxmon.DefaultPendingRotationStore = ctxmon.NewPendingRotationStoreWithPath(filepath.Join(sharedRoot, "pending.jsonl"))
	ctxmon.DefaultRotationHistoryStore = ctxmon.NewRotationHistoryStore()
	t.Cleanup(func() {
		cfg, jsonOutput, tmux.DefaultClient = oldCfg, oldJSON, oldClient
		ctxmon.DefaultPendingRotationStore, ctxmon.DefaultRotationHistoryStore = oldStore, oldHistory
		ctxmon.MaxRotationDelay = oldDelay
	})

	cases := []struct {
		name      string
		bound     bool
		kind      string
		sessionID string
	}{
		{name: "assistant_reply", bound: true, kind: "assistant", sessionID: "11111111-1111-4111-8111-111111111111"},
		{name: "unbound_transcript", kind: "assistant", sessionID: "22222222-2222-4222-8222-222222222222"},
		{name: "user_message", bound: true, kind: "user", sessionID: "33333333-3333-4333-8333-333333333333"},
		{name: "tool_payload", bound: true, kind: "tool", sessionID: "44444444-4444-4444-8444-444444444444"},
	}
	for idx, tc := range cases {
		idx, tc := idx, tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			oldNum := 50 + idx*2
			newNum := oldNum + 1
			oldNumStr := strconv.Itoa(oldNum)
			newNumStr := strconv.Itoa(newNum)
			oldPane := "%" + oldNumStr
			newPane := "%" + newNumStr
			session := "demo-transcript-" + tc.name
			agentID := session + "__cc_1"
			caseDir := filepath.Join(sharedRoot, "cases", tc.name)
			stageDir := filepath.Join(sharedRoot, "staging-"+tc.name)
			workDir := filepath.Join(caseDir, "project")
			if err := os.MkdirAll(filepath.Join(stageDir, "project"), 0700); err != nil {
				t.Fatal(err)
			}
			transcript := filepath.Join(sharedRoot, ".claude", "projects", ctxmon.MungeProjectPath(workDir), tc.sessionID+".jsonl")
			if err := os.MkdirAll(filepath.Dir(transcript), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(transcript, []byte("{}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			process := exec.Command(os.Args[0], "-test.run=^TestContextConfirmTranscriptAgentProcess$")
			process.Args[0] = "claude"
			process.Env = append(os.Environ(), "NTM_CONFIRM_AGENT_HELPER=1")
			if err := process.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = process.Process.Kill()
				_ = process.Wait()
			})
			pid := process.Process.Pid
			if tc.bound {
				stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
				if err != nil {
					t.Fatal(err)
				}
				end := bytes.LastIndexByte(stat, ')')
				if end < 0 {
					t.Fatalf("missing process command in stat: %q", stat)
				}
				fields := strings.Fields(string(stat[end+1:]))
				if len(fields) < 20 {
					t.Fatalf("missing process start time in stat: %q", stat)
				}
				record, err := json.Marshal(map[string]any{"pid": pid, "sessionId": tc.sessionID, "cwd": workDir, "procStart": fields[19]})
				if err != nil {
					t.Fatal(err)
				}
				sessions := filepath.Join(sharedRoot, ".claude", "sessions")
				if err := os.MkdirAll(sessions, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(sessions, strconv.Itoa(pid)+".json"), record, 0600); err != nil {
					t.Fatal(err)
				}
			}
			for name, val := range map[string]string{
				"session":         session,
				"agent_id":        agentID,
				"oldnum":          oldNumStr,
				"newnum":          newNumStr,
				"mode":            "transcript",
				"workdir":         workDir,
				"source-pid":      strconv.Itoa(pid),
				"transcript":      transcript,
				"transcript-kind": tc.kind,
				"session-id":      tc.sessionID,
			} {
				if err := os.WriteFile(filepath.Join(stageDir, name), []byte(val+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, key := range []string{session, oldPane, newPane} {
				tmpTarget := filepath.Join(sharedRoot, "by-target-tmp-"+tc.name+"-"+key)
				if err := os.WriteFile(tmpTarget, []byte(caseDir+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(tmpTarget, filepath.Join(sharedRoot, "by-target", key)); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Rename(stageDir, caseDir); err != nil {
				t.Fatal(err)
			}
			binding := agentsession.DiscoverProcessSession(t.Context(), "cc", pid)
			if tc.bound && (binding == nil || binding.Source != agentsession.DiscoverySourceProcessTree || binding.SessionID != tc.sessionID || binding.SourcePath != transcript) {
				t.Fatalf("fixture has no exact live transcript binding: %+v", binding)
			}
			if !tc.bound && binding != nil {
				t.Fatalf("unbound fixture unexpectedly discovered a session: %+v", binding)
			}

			pending := &ctxmon.PendingRotation{AgentID: agentID, SessionName: session, PaneID: oldPane, PanePID: pid, PaneType: "cc", WorkDir: workDir, ContextPercent: 75, CreatedAt: time.Now(), TimeoutAt: time.Now().Add(time.Hour)}
			if err := ctxmon.AddPendingRotation(pending); err != nil {
				t.Fatal(err)
			}
			command := newRotateContextConfirmCmd()
			command.SilenceUsage, command.SilenceErrors = true, true
			var out bytes.Buffer
			command.SetOut(&out)
			command.SetErr(new(bytes.Buffer))
			command.SetArgs([]string{pending.AgentID, "--action=rotate"})
			wantSuccess := tc.bound && tc.kind == "assistant"
			var ctx context.Context
			var cancel context.CancelFunc
			stopCancelWatcher := make(chan struct{})
			var cancelWatcherDone chan struct{}
			if wantSuccess {
				ctx, cancel = context.WithTimeout(t.Context(), 15*time.Second)
			} else {
				ctx, cancel = context.WithTimeout(t.Context(), 10*time.Second)
				commandsPath := filepath.Join(caseDir, "commands")
				cancelWatcherDone = make(chan struct{})
				go func() {
					defer close(cancelWatcherDone)
					ticker := time.NewTicker(10 * time.Millisecond)
					defer ticker.Stop()
					for {
						if data, readErr := os.ReadFile(commandsPath); readErr == nil {
							if _, after, ok := strings.Cut(string(data), "TRANSCRIPT_SUMMARY_WRITTEN"); ok && strings.Count(after, "capture-pane") >= 2 {
								cancel()
								return
							}
						}
						select {
						case <-stopCancelWatcher:
							return
						case <-ctx.Done():
							return
						case <-ticker.C:
						}
					}
				}()
			}
			defer func() {
				close(stopCancelWatcher)
				cancel()
				if cancelWatcherDone != nil {
					<-cancelWatcherDone
				}
			}()
			err := command.ExecuteContext(ctx)
			var result ConfirmRotationResult
			if decodeErr := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &result); decodeErr != nil {
				t.Fatalf("invalid command outcome: %v, error=%v, output=%q", decodeErr, err, out.String())
			}
			commands, _ := os.ReadFile(filepath.Join(caseDir, "commands"))
			log := string(commands)
			if !strings.Contains(log, "TRANSCRIPT_SUMMARY_WRITTEN") {
				t.Fatalf("command never requested a fresh transcript-only summary: %+v, %v\n%s", result, err, log)
			}
			stored, getErr := ctxmon.GetPendingRotationByID(pending.AgentID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			if wantSuccess {
				if err != nil || !result.Success || result.Rotation == nil || result.Rotation.NewPaneID != newPane || stored != nil {
					t.Fatalf("exact assistant reply did not complete rotation: %+v, %v; pending=%+v\n%s", result, err, stored, log)
				}
				payload, readErr := os.ReadFile(filepath.Join(caseDir, "delivered"))
				if readErr != nil || !strings.Contains(string(payload), "Continue transcript-bound recovery") || !strings.Contains(string(payload), "Original context is preserved in the transcript.") {
					t.Fatalf("replacement did not receive exact source transcript: %q, %v", payload, readErr)
				}
				deliveredAt, retiredAt := strings.Index(log, "HANDOFF_DELIVERED"), strings.Index(log, "kill-pane -t "+oldPane)
				if deliveredAt < 0 || retiredAt < 0 || deliveredAt > retiredAt {
					t.Fatalf("original was retired before the transcript handoff: %s", log)
				}
			} else if err == nil || result.Success || stored == nil || !strings.Contains(result.Message, "waiting for handoff summary") || strings.Contains(log, "split-window") || strings.Contains(log, "kill-pane") || strings.Contains(log, "HANDOFF_DELIVERED") {
				t.Fatalf("untrusted transcript affected original or replacement: %+v, %v; pending=%+v\n%s", result, err, stored, log)
			}
		})
	}
}

func TestContextConfirmTranscriptAgentProcess(t *testing.T) {
	if os.Getenv("NTM_CONFIRM_AGENT_HELPER") != "1" {
		return
	}
	// The parent owns this subprocess and stops it after the public CLI check.
	// Its argv and live /proc start time provide the real provider binding.
	time.Sleep(time.Minute)
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
      *'#{@ntm_rotation_token}'*)
        tok=
        if [ -f "$NTM_CONFIRM_ROOT/token.${target#%}" ]; then IFS= read -r tok < "$NTM_CONFIRM_ROOT/token.${target#%}"; fi
        printf '[%s]\n' "$tok"
        ;;
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
      pid=${NTM_CONFIRM_SOURCE_PID:-123}
      if [ "$NTM_CONFIRM_MODE" = changed-pid ]; then pid=456; fi
      index=1
      if [ -f "$NTM_CONFIRM_ROOT/created" ] && [ ! -f "$NTM_CONFIRM_ROOT/replacement-retired" ]; then index=2; fi
      token=
      if [ -f "$NTM_CONFIRM_ROOT/token.1" ]; then IFS= read -r token < "$NTM_CONFIRM_ROOT/token.1"; fi
      printf '%s%%1_NTM_SEP_%s_NTM_SEP_demo__cc_1_NTM_SEP_node_NTM_SEP_120_NTM_SEP_40_NTM_SEP_0_NTM_SEP_%s_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[%s]\n' "$prefix" "$index" "$pid" "$token"
    fi
    if [ -f "$NTM_CONFIRM_ROOT/created" ] && [ ! -f "$NTM_CONFIRM_ROOT/replacement-retired" ]; then
      command=bash
      if [ -f "$NTM_CONFIRM_ROOT/launched" ]; then command=node; fi
      token=
      if [ -f "$NTM_CONFIRM_ROOT/token.99" ]; then IFS= read -r token < "$NTM_CONFIRM_ROOT/token.99"; fi
      printf '%s%%99_NTM_SEP_1_NTM_SEP_demo__cc_1_NTM_SEP_%s_NTM_SEP_120_NTM_SEP_40_NTM_SEP_0_NTM_SEP_%s_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[%s]\n' "$prefix" "$command" "${NTM_CONFIRM_REPLACEMENT_PID:-321}" "$token"
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
      if [ "$NTM_CONFIRM_MODE" = transcript ]; then
        text="$start_marker\n## Current Task\nContinue transcript-bound recovery\n## Progress\nOriginal context is preserved in the transcript.\n## Next Steps\nVerify the continuation.\n$end_marker"
        case "$NTM_CONFIRM_TRANSCRIPT_KIND" in
          user) printf '{"type":"user","sessionId":"%s","message":{"role":"user","content":[{"type":"text","text":"%s"}]}}\n' "$NTM_CONFIRM_SESSION_ID" "$text" >> "$NTM_CONFIRM_TRANSCRIPT" ;;
          tool) printf '{"type":"assistant","sessionId":"%s","message":{"role":"assistant","content":[{"type":"tool_use","id":"fixture","name":"Read","input":{"text":"%s"}}]}}\n' "$NTM_CONFIRM_SESSION_ID" "$text" >> "$NTM_CONFIRM_TRANSCRIPT" ;;
          *) printf '{"type":"assistant","sessionId":"%s","message":{"role":"assistant","content":[{"type":"text","text":"%s"}]}}\n' "$NTM_CONFIRM_SESSION_ID" "$text" >> "$NTM_CONFIRM_TRANSCRIPT" ;;
        esac
        printf 'TRANSCRIPT_SUMMARY_WRITTEN\n' >> "$NTM_CONFIRM_ROOT/commands"
      else
        printf '%s\n' "$start_marker" > "$NTM_CONFIRM_ROOT/summary"
        printf '## Current Task\nContinue durable scheduler implementation\n## Progress\nScheduling state is persisted.\n## Next Steps\nAdd recovery tests.\n' >> "$NTM_CONFIRM_ROOT/summary"
        printf '%s\n' "$end_marker" >> "$NTM_CONFIRM_ROOT/summary"
      fi
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
  kill-pane)
    if [ "$target" = '%1' ]; then printf '1' > "$NTM_CONFIRM_ROOT/retired"; fi
    if [ "$target" = '%99' ]; then
      if [ "$NTM_CONFIRM_KILL_FAILURE" = 1 ]; then exit 1; fi
      if [ "$NTM_CONFIRM_KEEP_REPLACEMENT" != 1 ]; then printf '1' > "$NTM_CONFIRM_ROOT/replacement-retired"; fi
    fi
    ;;
esac
`

const contextConfirmKeyedTmuxFixture = `#!/bin/sh
target=
bufname=
previous=
for value do
  if [ "$previous" = -t ]; then target="$value"; fi
  if [ "$previous" = -b ]; then bufname="$value"; fi
  previous="$value"
done
ready() { printf 'Welcome to Claude Code\n────────────────────────\n❯ \n────────────────────────\n  ⏵⏵ bypass permissions on (shift+tab to cycle)\n'; }
read_val() {
  if [ -f "$1" ]; then IFS= read -r _rv < "$1"; printf '%s' "$_rv"; fi
}
case "$1" in
  -V)
    echo 'tmux 3.4'
    exit 0
    ;;
  load-buffer)
    cat > "$NTM_CONFIRM_CASES_ROOT/buf-${bufname:-default}"
    exit 0
    ;;
  delete-buffer)
    exit 0
    ;;
esac
lookup_key="${target#=}"
lookup_key="${lookup_key%%:*}"
case_dir=
if [ -n "$lookup_key" ] && [ -f "$NTM_CONFIRM_CASES_ROOT/by-target/$lookup_key" ]; then
  IFS= read -r case_dir < "$NTM_CONFIRM_CASES_ROOT/by-target/$lookup_key"
fi
if [ -n "$case_dir" ] && [ -d "$case_dir" ]; then
  printf '%s\n' "$*" >> "$case_dir/commands"
fi
emit_case_panes() {
  _cd="$1"
  _all="$2"
  [ -f "$_cd/session" ] || return 0
  _sess=$(read_val "$_cd/session")
  _agent=$(read_val "$_cd/agent_id")
  _oldnum=$(read_val "$_cd/oldnum")
  _newnum=$(read_val "$_cd/newnum")
  _mode=$(read_val "$_cd/mode")
  _prefix=
  if [ "$_all" = 1 ]; then _prefix="${_sess}_NTM_SEP_"; fi
  if [ ! -f "$_cd/retired" ]; then
    _pid=$(read_val "$_cd/source-pid")
    if [ -z "$_pid" ]; then _pid=123; fi
    if [ "$_mode" = changed-pid ]; then _pid=456; fi
    _idx=1
    if [ -f "$_cd/created" ] && [ ! -f "$_cd/replacement-retired" ]; then _idx=2; fi
    _tok=$(read_val "$_cd/token.${_oldnum}")
    printf '%s%%%s_NTM_SEP_%s_NTM_SEP_%s_NTM_SEP_node_NTM_SEP_120_NTM_SEP_40_NTM_SEP_0_NTM_SEP_%s_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[%s]\n' "$_prefix" "$_oldnum" "$_idx" "$_agent" "$_pid" "$_tok"
  fi
  if [ -f "$_cd/created" ] && [ ! -f "$_cd/replacement-retired" ]; then
    _cmd=bash
    if [ -f "$_cd/launched" ]; then _cmd=node; fi
    _tok=$(read_val "$_cd/token.${_newnum}")
    _rpid=$(read_val "$_cd/replacement-pid")
    if [ -z "$_rpid" ]; then _rpid=321; fi
    printf '%s%%%s_NTM_SEP_1_NTM_SEP_%s_NTM_SEP_%s_NTM_SEP_120_NTM_SEP_40_NTM_SEP_0_NTM_SEP_%s_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[%s]\n' "$_prefix" "$_newnum" "$_agent" "$_cmd" "$_rpid" "$_tok"
  fi
  if [ "$_all" = 1 ] && [ "$_mode" = cross-session ]; then
    printf 'other_NTM_SEP_%%2_NTM_SEP_1_NTM_SEP_other__cc_1_NTM_SEP_node_NTM_SEP_120_NTM_SEP_40_NTM_SEP_0_NTM_SEP_456_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0\n'
  fi
}
case "$1" in
  list-windows) printf '0\n'; exit 0 ;;
  has-session)
    [ -n "$case_dir" ] && [ -d "$case_dir" ] || exit 1
    exit 0
    ;;
  list-panes)
    case " $* " in
      *' -a '*)
        for _d in "$NTM_CONFIRM_CASES_ROOT/cases"/*; do
          [ -d "$_d" ] && emit_case_panes "$_d" 1
        done
        exit 0
        ;;
    esac
    ;;
esac
if [ -z "$case_dir" ] || [ ! -d "$case_dir" ]; then
  exit 2
fi
case "$1" in
  display-message)
    case "$*" in
      *'#{@ntm_rotation_token}'*)
        tok=$(read_val "$case_dir/token.${target#%}")
        printf '[%s]\n' "$tok"
        ;;
      *)
        printf '%s\n' "$(read_val "$case_dir/workdir")"
        ;;
    esac
    ;;
  if-shell)
    for value do
      _tok_tmp="$case_dir/.token.${target#%}.$$.tmp"
      case "$value" in
        'set-option -p -u '*) : > "$_tok_tmp" && mv -f "$_tok_tmp" "$case_dir/token.${target#%}" ;;
        'set-option -p -t '*) printf '%s' "${value##* }" > "$_tok_tmp" && mv -f "$_tok_tmp" "$case_dir/token.${target#%}" ;;
      esac
    done
    ;;
  list-panes)
    emit_case_panes "$case_dir" 0
    ;;
  capture-pane)
    _mode=$(read_val "$case_dir/mode")
    _oldnum=$(read_val "$case_dir/oldnum")
    _newnum=$(read_val "$case_dir/newnum")
    if [ "$_mode" = busy ]; then
      printf 'Welcome to Claude Code\n✻ Sautéing… (ctrl+c to interrupt · 12s · thinking)\n────────────────────────\n❯ \n────────────────────────\n  ⏵⏵ bypass permissions on\n'
    elif [ "$target" = "%${_newnum}" ] && [ -f "$case_dir/delivered" ]; then
      printf 'Welcome to Claude Code\n✻ Sautéing… (ctrl+c to interrupt · 12s · thinking)\n────────────────────────\n❯ \n────────────────────────\n  ⏵⏵ bypass permissions on\n'
    else
      if [ "$target" = "%${_oldnum}" ] && [ -f "$case_dir/summary" ]; then cat "$case_dir/summary"; fi
      ready
    fi
    ;;
  paste-buffer)
    _mode=$(read_val "$case_dir/mode")
    _oldnum=$(read_val "$case_dir/oldnum")
    _buf="$NTM_CONFIRM_CASES_ROOT/buf-${bufname:-default}"
    if [ "$target" = "%${_oldnum}" ]; then
      start_marker=$(sed -n 's/.*\(NTM_START_[A-Za-z0-9]*\).*/\1/p' "$_buf")
      end_marker=$(sed -n 's/.*\(NTM_END_[A-Za-z0-9]*\).*/\1/p' "$_buf")
      if [ "$_mode" = transcript ]; then
        _tkind=$(read_val "$case_dir/transcript-kind")
        _tsid=$(read_val "$case_dir/session-id")
        _tpath=$(read_val "$case_dir/transcript")
        text="$start_marker\n## Current Task\nContinue transcript-bound recovery\n## Progress\nOriginal context is preserved in the transcript.\n## Next Steps\nVerify the continuation.\n$end_marker"
        case "$_tkind" in
          user) printf '{"type":"user","sessionId":"%s","message":{"role":"user","content":[{"type":"text","text":"%s"}]}}\n' "$_tsid" "$text" >> "$_tpath" ;;
          tool) printf '{"type":"assistant","sessionId":"%s","message":{"role":"assistant","content":[{"type":"tool_use","id":"fixture","name":"Read","input":{"text":"%s"}}]}}\n' "$_tsid" "$text" >> "$_tpath" ;;
          *) printf '{"type":"assistant","sessionId":"%s","message":{"role":"assistant","content":[{"type":"text","text":"%s"}]}}\n' "$_tsid" "$text" >> "$_tpath" ;;
        esac
        printf 'TRANSCRIPT_SUMMARY_WRITTEN\n' >> "$case_dir/commands"
      else
        printf '%s\n' "$start_marker" > "$case_dir/summary"
        printf '## Current Task\nContinue durable scheduler implementation\n## Progress\nScheduling state is persisted.\n## Next Steps\nAdd recovery tests.\n' >> "$case_dir/summary"
        printf '%s\n' "$end_marker" >> "$case_dir/summary"
      fi
    else
      cat "$_buf" > "$case_dir/delivered"
      printf 'HANDOFF_DELIVERED\n' >> "$case_dir/commands"
      if [ "$_mode" = manifest-race ]; then
        cp "$(read_val "$case_dir/changed-manifest-path")" "$(read_val "$case_dir/manifest-path")"
      fi
      if [ "$_mode" = handoff-unknown ]; then exit 1; fi
    fi
    ;;
  show-options) printf 'invalid option: @ntm_agent_launch\n' >&2; exit 1 ;;
  split-window)
    _mode=$(read_val "$case_dir/mode")
    _newnum=$(read_val "$case_dir/newnum")
    if [ "$_mode" = launch-failure ]; then exit 1; fi
    printf '1' > "$case_dir/created"
    printf '%%%s\n' "$_newnum"
    ;;
  send-keys)
    _newnum=$(read_val "$case_dir/newnum")
    case "$*" in
      *'/compact'*) printf '%s\n' "$(read_val "$case_dir/compacted")" > "$(read_val "$case_dir/transcript")" ;;
    esac
    if [ "$target" = "%${_newnum}" ]; then
      _asgn=$(read_val "$case_dir/assignments")
      if [ ! -f "$case_dir/launched" ] && [ -n "$_asgn" ]; then
        _oldnum=$(read_val "$case_dir/oldnum")
        [ -s "$case_dir/token.${_oldnum}" ] && [ -s "$case_dir/token.${_newnum}" ] || exit 1
        grep -q "\"target_pane\": \"%${_newnum}\"" "$_asgn" || exit 1
        _ident=$(read_val "$case_dir/identity")
        if [ -n "$_ident" ]; then
          [ -f "$_ident" ] && [ "$(cat "$_ident")" = BlueLake ] || exit 1
        fi
        printf 'OWNERSHIP_BEFORE_LAUNCH\n' >> "$case_dir/commands"
      fi
      printf '1' > "$case_dir/launched"
    fi
    ;;
  kill-pane)
    _oldnum=$(read_val "$case_dir/oldnum")
    _newnum=$(read_val "$case_dir/newnum")
    if [ "$target" = "%${_oldnum}" ]; then printf '1' > "$case_dir/retired"; fi
    if [ "$target" = "%${_newnum}" ]; then
      if [ "$(read_val "$case_dir/kill-failure")" = 1 ]; then exit 1; fi
      if [ "$(read_val "$case_dir/keep-replacement")" != 1 ]; then printf '1' > "$case_dir/replacement-retired"; fi
    fi
    ;;
  select-pane|set-option|resize-pane|rename-window|select-layout)
    ;;
  *)
    echo "unexpected tmux action: $*" >&2
    exit 2
    ;;
esac
`

type rotateTestQuotaFetcher struct {
	info       *quota.QuotaInfo
	err        error
	afterFetch func()
}

func (f rotateTestQuotaFetcher) FetchQuota(context.Context, string, quota.Provider) (*quota.QuotaInfo, error) {
	if f.afterFetch != nil {
		f.afterFetch()
	}
	return f.info, f.err
}

type rotateTestOrchestrator struct {
	terminateErr   error
	waitErr        error
	startErr       error
	startErrors    map[string]error
	prompt         func(string) error
	afterTerminate func(string)
	afterWait      func(string)
	afterStart     func(auth.RestartContext)
	terminated     int
	waited         int
	prompted       int
	started        int
	restarts       []auth.RestartContext
}

func (o *rotateTestOrchestrator) TerminateSession(paneID, _ string) error {
	o.terminated++
	if o.afterTerminate != nil {
		o.afterTerminate(paneID)
	}
	return o.terminateErr
}

func (o *rotateTestOrchestrator) WaitForShellPrompt(paneID string, _ time.Duration) error {
	o.waited++
	if o.afterWait != nil {
		o.afterWait(paneID)
	}
	return o.waitErr
}

func (o *rotateTestOrchestrator) PromptBrowserAuth(email string) error {
	o.prompted++
	if o.prompt != nil {
		return o.prompt(email)
	}
	return nil
}

func (o *rotateTestOrchestrator) StartNewAgentSession(ctx auth.RestartContext) error {
	o.started++
	o.restarts = append(o.restarts, ctx)
	if o.afterStart != nil {
		o.afterStart(ctx)
	}
	if err := o.startErrors[ctx.PaneID]; err != nil {
		return err
	}
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
	t.Parallel()

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
	t.Parallel()

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

func setupRotateAllLimitedTest(t *testing.T, panes []tmux.Pane, orch *rotateTestOrchestrator) string {
	t.Helper()
	isolateSessionAgentStorage(t)

	previousGetPanes := rotateGetPanes
	previousQuotaFetcher := newRotateQuotaFetcher
	previousOrchestrator := newRotateOrchestrator
	previousCfg := cfg
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		rotateGetPanes = previousGetPanes
		newRotateQuotaFetcher = previousQuotaFetcher
		newRotateOrchestrator = previousOrchestrator
		cfg = previousCfg
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
	cfg = config.Default()
	cfg.ProjectsBase = canonicalTempDir(t)
	// Explicit-session failure probes must never consult a developer's tmux.
	t.Setenv("NTM_TMUX_BINARY", filepath.Join(t.TempDir(), "unavailable-tmux"))
	rotateGetPanes = func(string) ([]tmux.Pane, error) {
		return append([]tmux.Pane(nil), panes...), nil
	}
	newRotateQuotaFetcher = func() quota.Fetcher {
		return rotateTestQuotaFetcher{info: &quota.QuotaInfo{IsLimited: true}}
	}
	newRotateOrchestrator = func(*config.Config) rotationOrchestrator { return orch }
	return workspace
}

func TestRotateAllLimitedStopsBeforePromptWhenTerminationFails(t *testing.T) {
	orch := &rotateTestOrchestrator{terminateErr: errors.New("interrupt failed")}
	setupRotateAllLimitedTest(t, []tmux.Pane{{ID: "%11", Index: 1, Title: "limited", Type: tmux.AgentClaude}}, orch)

	err := rotateAllLimited(t.Context(), "proj", "backup@example.com", false, true)
	if err == nil || !strings.Contains(err.Error(), "terminate limited pane 1 (%11)") {
		t.Fatalf("rotateAllLimited() error = %v, want termination context", err)
	}
	if orch.terminated != 1 || orch.waited != 0 || orch.prompted != 0 || orch.started != 0 {
		t.Fatalf("orchestrator calls = terminate:%d wait:%d prompt:%d start:%d, want 1:0:0:0", orch.terminated, orch.waited, orch.prompted, orch.started)
	}
}

func TestRotateAllLimitedStopsBeforePromptWhenShellIsNotReady(t *testing.T) {
	orch := &rotateTestOrchestrator{waitErr: errors.New("shell still running")}
	setupRotateAllLimitedTest(t, []tmux.Pane{{ID: "%12", Index: 2, Title: "limited", Type: tmux.AgentCodex}}, orch)

	err := rotateAllLimited(t.Context(), "proj", "backup@example.com", false, true)
	if err == nil || !strings.Contains(err.Error(), "wait for shell prompt in limited pane 2 (%12)") {
		t.Fatalf("rotateAllLimited() error = %v, want shell-readiness context", err)
	}
	if orch.terminated != 1 || orch.waited != 1 || orch.prompted != 0 || orch.started != 0 {
		t.Fatalf("orchestrator calls = terminate:%d wait:%d prompt:%d start:%d, want 1:1:0:0", orch.terminated, orch.waited, orch.prompted, orch.started)
	}
}

func TestRotateAllLimitedReportsRestartFailure(t *testing.T) {
	restartErr := errors.New("relaunch failed")
	orch := &rotateTestOrchestrator{startErr: restartErr}
	panes := []tmux.Pane{
		{ID: "%13", Index: 3, Title: "limited claude", Type: tmux.AgentClaude},
		{ID: "%14", Index: 4, Title: "limited codex", Type: tmux.AgentCodex},
	}
	setupRotateAllLimitedTest(t, panes, orch)

	err := rotateAllLimited(t.Context(), "proj", "backup@example.com", false, true)
	if !errors.Is(err, restartErr) {
		t.Fatalf("rotateAllLimited() error = %v, want wrapped %v", err, restartErr)
	}
	if orch.terminated != 2 || orch.waited != 2 || orch.prompted != 1 || orch.started != 2 {
		t.Fatalf("orchestrator calls = terminate:%d wait:%d prompt:%d start:%d, want 2:2:1:2", orch.terminated, orch.waited, orch.prompted, orch.started)
	}
	for _, pane := range panes {
		snapshot, ok := rotation.DefaultLifecycleCoordinator().Snapshot(pane.ID)
		if !ok || snapshot.State != rotation.StateFailedRecoverable || snapshot.LastCheckpoint != "start_new_session" {
			t.Fatalf("failed restart for %s lost its recovery checkpoint: %+v", pane.ID, snapshot)
		}
	}
}

func TestRotateAllLimitedResolvesProjectBeforeStoppingAgents(t *testing.T) {
	orch := &rotateTestOrchestrator{}
	setupRotateAllLimitedTest(t, []tmux.Pane{{ID: "%7001", Index: 1, Title: "limited", Type: tmux.AgentClaude}}, orch)

	err := rotateAllLimited(t.Context(), "ntm-rotation-missing-project-test", "backup@example.com", false, false)
	if err == nil || !strings.Contains(err.Error(), "getting project root failed") {
		t.Fatalf("rotateAllLimited() error = %v, want unavailable project", err)
	}
	if orch.terminated != 0 || orch.waited != 0 || orch.prompted != 0 || orch.started != 0 {
		t.Fatalf("unresolved workspace stopped agents: %+v", orch)
	}
}

func TestRotateAllLimitedPreflightsEntireTargetBatch(t *testing.T) {
	tests := []struct {
		name   string
		change func([]tmux.Pane)
	}{
		{name: "missing physical identity", change: func(panes []tmux.Pane) { panes[1].ID = "" }},
		{name: "duplicate physical identity", change: func(panes []tmux.Pane) { panes[1].ID = panes[0].ID }},
		{name: "dead pane", change: func(panes []tmux.Pane) { panes[1].Dead = true }},
		{name: "pending context rotation", change: func(panes []tmux.Pane) { panes[1].RotationToken = "rotation-in-flight" }},
		{name: "unsupported auth provider", change: func(panes []tmux.Pane) { panes[1].Type = tmux.AgentOmp }},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panes := []tmux.Pane{
				{ID: "%" + strconv.Itoa(7020+2*i), Index: 1, Title: "first", Type: tmux.AgentClaude},
				{ID: "%" + strconv.Itoa(7021+2*i), Index: 2, Title: "second", Type: tmux.AgentCodex},
			}
			tt.change(panes)
			orch := &rotateTestOrchestrator{}
			setupRotateAllLimitedTest(t, panes, orch)

			if err := rotateAllLimited(t.Context(), "proj", "backup@example.com", false, true); err == nil {
				t.Fatal("unsafe target batch was accepted")
			}
			if orch.terminated != 0 || orch.waited != 0 || orch.prompted != 0 || orch.started != 0 {
				t.Fatalf("failed preflight partially stopped the batch: %+v", orch)
			}
		})
	}
}

func TestRotateAllLimitedHonorsCancellationAtActuationBoundaries(t *testing.T) {
	tests := []struct {
		stage      string
		terminated int
		waited     int
		prompted   int
		started    int
	}{
		{stage: "before scan"},
		{stage: "quota"},
		{stage: "terminate", terminated: 1},
		{stage: "wait", terminated: 2, waited: 1},
		{stage: "prompt", terminated: 2, waited: 2, prompted: 1},
		{stage: "start", terminated: 2, waited: 2, prompted: 1, started: 1},
	}
	for i, tt := range tests {
		t.Run(tt.stage, func(t *testing.T) {
			panes := []tmux.Pane{
				{ID: "%" + strconv.Itoa(7100+2*i), Index: 1, Title: "first", Type: tmux.AgentClaude},
				{ID: "%" + strconv.Itoa(7101+2*i), Index: 2, Title: "second", Type: tmux.AgentCodex},
			}
			orch := &rotateTestOrchestrator{}
			setupRotateAllLimitedTest(t, panes, orch)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch tt.stage {
			case "before scan":
				cancel()
			case "quota":
				newRotateQuotaFetcher = func() quota.Fetcher {
					return rotateTestQuotaFetcher{info: &quota.QuotaInfo{IsLimited: true}, afterFetch: cancel}
				}
			case "terminate":
				orch.afterTerminate = func(string) { cancel() }
			case "wait":
				orch.afterWait = func(string) { cancel() }
			case "prompt":
				orch.prompt = func(string) error {
					cancel()
					return nil
				}
			case "start":
				orch.afterStart = func(auth.RestartContext) { cancel() }
			}

			err := rotateAllLimited(ctx, "proj", "backup@example.com", false, true)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("rotateAllLimited() error = %v, want context cancellation", err)
			}
			if orch.terminated != tt.terminated || orch.waited != tt.waited || orch.prompted != tt.prompted || orch.started != tt.started {
				t.Fatalf("orchestrator calls = terminate:%d wait:%d prompt:%d start:%d, want %d:%d:%d:%d", orch.terminated, orch.waited, orch.prompted, orch.started, tt.terminated, tt.waited, tt.prompted, tt.started)
			}
			fsm := rotation.DefaultLifecycleCoordinator()
			for _, pane := range panes {
				if fsm.IsActive(pane.ID) {
					t.Fatalf("cancellation left pane %s lifecycle active", pane.ID)
				}
			}
			if tt.stage == "terminate" {
				started, _ := fsm.Snapshot(panes[0].ID)
				untouched, _ := fsm.Snapshot(panes[1].ID)
				if started.State != rotation.StateFailedRecoverable || started.LastCheckpoint != "terminate_session" ||
					untouched.State != rotation.StateIdle || untouched.LastCheckpoint != "aborted" {
					t.Fatalf("cancellation confused changed and untouched panes: %+v / %+v", started, untouched)
				}
			}
			if tt.stage == "start" {
				completed, _ := fsm.Snapshot(panes[0].ID)
				unstarted, _ := fsm.Snapshot(panes[1].ID)
				if completed.State != rotation.StateIdle || completed.LastCheckpoint != "completed" ||
					unstarted.State != rotation.StateFailedRecoverable || unstarted.LastCheckpoint != "browser_auth" {
					t.Fatalf("cancellation lost completed or pending relaunch evidence: %+v / %+v", completed, unstarted)
				}
			}
		})
	}
}

func TestRotateAllLimitedLeaseConflictLeavesOtherOwnerUntouched(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%7201", Index: 1, Title: "first", Type: tmux.AgentClaude},
		{ID: "%7202", Index: 2, Title: "second", Type: tmux.AgentCodex},
	}
	orch := &rotateTestOrchestrator{}
	setupRotateAllLimitedTest(t, panes, orch)
	fsm := rotation.DefaultLifecycleCoordinator()
	held, err := fsm.Acquire(panes[1].ID, "context-rotator-test", t.Name(), "context_rotation")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fsm.Abort(held, "test cleanup"); err != nil {
			t.Errorf("release test lease: %v", err)
		}
	})

	err = rotateAllLimited(t.Context(), "proj", "backup@example.com", false, true)
	if !errors.Is(err, rotation.ErrLifecycleLeaseHeld) {
		t.Fatalf("rotateAllLimited() error = %v, want lifecycle ownership conflict", err)
	}
	if orch.terminated != 0 || orch.waited != 0 || orch.prompted != 0 || orch.started != 0 {
		t.Fatalf("ownership conflict partially stopped the batch: %+v", orch)
	}
	first, _ := fsm.Snapshot(panes[0].ID)
	second, _ := fsm.Snapshot(panes[1].ID)
	if first.State != rotation.StateIdle || first.LastCheckpoint != "aborted" {
		t.Fatalf("unmutated batch lease was not released: %+v", first)
	}
	if second.State != rotation.StateDraining || second.Token != held.Token || second.OwnerID != held.OwnerID {
		t.Fatalf("batch conflict disturbed another lifecycle owner: %+v", second)
	}
}

func TestRotateAllLimitedRequiresExplicitBrowserConfirmation(t *testing.T) {
	for i, input := range []string{"", "confirmed without newline"} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			paneID := "%" + strconv.Itoa(7300+i)
			orch := &rotateTestOrchestrator{}
			setupRotateAllLimitedTest(t, []tmux.Pane{{ID: paneID, Index: 1, Title: "limited", Type: tmux.AgentClaude}}, orch)
			// Exercise the production confirmation method with EOF and with a
			// partial line. Neither is the ENTER required by the auth contract.
			orch.prompt = auth.NewOrchestrator(config.Default()).PromptBrowserAuth
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			previousStdin := os.Stdin
			t.Cleanup(func() {
				os.Stdin = previousStdin
				_ = reader.Close()
				_ = writer.Close()
			})
			if _, err := writer.WriteString(input); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			os.Stdin = reader

			err = rotateAllLimited(t.Context(), "proj", "backup@example.com", false, true)
			if err == nil || !strings.Contains(err.Error(), "confirmation not received") {
				t.Fatalf("rotateAllLimited() error = %v, want explicit confirmation failure", err)
			}
			if orch.terminated != 1 || orch.waited != 1 || orch.prompted != 1 || orch.started != 0 {
				t.Fatalf("unconfirmed browser account relaunched agents: %+v", orch)
			}
			snapshot, _ := rotation.DefaultLifecycleCoordinator().Snapshot(paneID)
			if snapshot.State != rotation.StateFailedRecoverable || snapshot.LastCheckpoint != "browser_auth" {
				t.Fatalf("confirmation failure lost recovery state: %+v", snapshot)
			}
		})
	}
}

func TestRotateAllLimitedSharesLifecycleThroughSuccessfulBatch(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%7401", Index: 1, Title: "first", Type: tmux.AgentClaude},
		{ID: "%7402", Index: 2, Title: "second", Type: tmux.AgentCodex},
	}
	orch := &rotateTestOrchestrator{}
	workspace := setupRotateAllLimitedTest(t, panes, orch)
	fsm := rotation.DefaultLifecycleCoordinator()
	assertStates := func(want rotation.LifecycleState) {
		t.Helper()
		for _, pane := range panes {
			snapshot, ok := fsm.Snapshot(pane.ID)
			if !ok || snapshot.State != want || snapshot.Kind != "auth_restart" {
				t.Fatalf("pane %s lifecycle = %+v, want %s auth_restart", pane.ID, snapshot, want)
			}
		}
	}
	orch.afterTerminate = func(string) { assertStates(rotation.StateDraining) }
	orch.prompt = func(email string) error {
		if email != "backup@example.com" {
			t.Fatalf("confirmation target = %q", email)
		}
		assertStates(rotation.StateHandingOff)
		return nil
	}

	if err := rotateAllLimited(t.Context(), "proj", "backup@example.com", false, true); err != nil {
		t.Fatal(err)
	}
	if orch.terminated != 2 || orch.waited != 2 || orch.prompted != 1 || orch.started != 2 {
		t.Fatalf("successful batch calls = %+v", orch)
	}
	for i, restart := range orch.restarts {
		if restart.PaneID != panes[i].ID || restart.ProjectDir != workspace ||
			restart.AgentType != string(panes[i].Type.Canonical()) {
			t.Fatalf("relaunch changed target or workspace: %+v", restart)
		}
		snapshot, _ := fsm.Snapshot(restart.PaneID)
		if snapshot.State != rotation.StateIdle || snapshot.LastCheckpoint != "completed" {
			t.Fatalf("successful relaunch retained lifecycle ownership: %+v", snapshot)
		}
	}
}

func TestRotateAllLimitedPreservesMixedRestartOutcomes(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%7501", Index: 1, Title: "first", Type: tmux.AgentClaude},
		{ID: "%7502", Index: 2, Title: "second", Type: tmux.AgentCodex},
	}
	restartErr := errors.New("first pane command failed")
	orch := &rotateTestOrchestrator{startErrors: map[string]error{panes[0].ID: restartErr}}
	setupRotateAllLimitedTest(t, panes, orch)

	err := rotateAllLimited(t.Context(), "proj", "backup@example.com", false, true)
	if !errors.Is(err, restartErr) || orch.started != 2 {
		t.Fatalf("batch did not preserve the first failure and attempt the second restart: err=%v, starts=%d", err, orch.started)
	}
	fsm := rotation.DefaultLifecycleCoordinator()
	failed, _ := fsm.Snapshot(panes[0].ID)
	completed, _ := fsm.Snapshot(panes[1].ID)
	if failed.State != rotation.StateFailedRecoverable || failed.LastCheckpoint != "start_new_session" {
		t.Fatalf("failed restart lost its recovery state: %+v", failed)
	}
	if completed.State != rotation.StateIdle || completed.LastCheckpoint != "completed" {
		t.Fatalf("unrelated restart failure changed the completed pane: %+v", completed)
	}
}

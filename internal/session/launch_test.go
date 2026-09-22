package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func savedLaunchSpec(command string) *tmux.AgentLaunchSpec {
	return &tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentCodex,
		Command: command, Model: "saved-model", ModelAlias: "saved", Persona: "reviewer", ReasoningEffort: "high"}
}

func TestSavedLaunchCaptureSaveRestoreRoundTrip(t *testing.T) {
	logPath := savedSessionRestoreFixture(t)
	directory := t.TempDir()
	t.Setenv("NTM_SESSION_TEST_CWD", directory)
	spec := savedLaunchSpec("/opt/codex --model saved-model -c 'model_reasoning_effort=high' --custom=literal")
	if err := tmux.SetPaneLaunchSpecContext(t.Context(), "%0", *spec); err != nil {
		t.Fatal(err)
	}
	panes := []PaneState{{Index: 0, PaneID: "%0", AgentType: "cod"}, {Index: 1, PaneID: "%1", AgentType: "user"}}
	if err := capturePaneLaunchState(t.Context(), panes); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(panes[0].LaunchSpec, spec) || panes[0].Command != spec.Command || panes[0].WorkDir != directory || panes[1].WorkDir != directory {
		t.Fatalf("capture lost exact command or worktree: %+v", panes)
	}
	state := &SessionState{Name: "saved_launch", WorkDir: t.TempDir(), Panes: panes, Version: StateVersion}
	if _, err := Save(state, SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(state.Name)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(loaded)
	result, err := RestoreWithAgents(t.Context(), loaded, AgentCommands{Codex: "codex --model wrong-model"}, config.Default(), RestoreOptions{Name: "recovered", Force: true})
	if err != nil || result == nil || result.Launched != 1 || result.Skipped != 1 {
		t.Fatalf("restore failed: %+v, %v", result, err)
	}
	after, _ := json.Marshal(loaded)
	if string(before) != string(after) {
		t.Fatal("restore mutated the saved snapshot")
	}
	recorded, err := tmux.ReadPaneLaunchSpecContext(t.Context(), "%0")
	if err != nil || !reflect.DeepEqual(recorded, spec) {
		t.Fatalf("restored pane did not retain its original specification: %+v, %v", recorded, err)
	}
	calls, _ := os.ReadFile(logPath)
	launch := "cd " + tmux.ShellQuote(directory) + " && " + spec.Command
	if !strings.Contains(string(calls), launch) || strings.Contains(string(calls), "wrong-model") {
		t.Fatalf("restore lost saved settings or per-pane cwd: %s", calls)
	}
	if strings.LastIndex(string(calls), "set-option -p -t %0 @ntm_agent_launch") > strings.Index(string(calls), "send-keys -t %0") {
		t.Fatalf("launch settings were persisted after dispatch: %s", calls)
	}
}

func TestSavedLaunchCaptureRejectsCorruptOrReplacedMetadata(t *testing.T) {
	for _, scenario := range []string{"corrupt", "wrong provider", "relative cwd", "changed pid", "changed spec", "removed spec", "changed cwd"} {
		t.Run(scenario, func(t *testing.T) {
			savedSessionRestoreFixture(t)
			spec := savedLaunchSpec("codex --model exact")
			if err := tmux.SetPaneLaunchSpecContext(t.Context(), "%0", *spec); err != nil {
				t.Fatal(err)
			}
			panes := []PaneState{{Index: 0, PaneID: "%0", AgentType: "cod"}, {Index: 1, PaneID: "%1", AgentType: "user"}}
			if scenario == "corrupt" {
				if err := os.WriteFile(filepath.Join(os.Getenv("NTM_SESSION_TEST_RECORD_DIR"), "%0"), []byte("bad-base64"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "wrong provider" {
				panes[0].AgentType = "cc"
			} else if scenario == "relative cwd" {
				t.Setenv("NTM_SESSION_TEST_CWD", "relative")
			}
			err := capturePaneLaunchState(t.Context(), panes)
			if scenario == "corrupt" || scenario == "wrong provider" || scenario == "relative cwd" {
				if err == nil {
					t.Fatal("capture silently accepted incomplete recovery data")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			original, err := tmux.GetPanes("saved_launch")
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "changed pid":
				t.Setenv("NTM_SESSION_TEST_PID", "9999999")
			case "changed spec":
				spec.Command = "codex --model replacement"
				if err := tmux.SetPaneLaunchSpecContext(t.Context(), "%0", *spec); err != nil {
					t.Fatal(err)
				}
			case "removed spec":
				if err := os.Rename(filepath.Join(os.Getenv("NTM_SESSION_TEST_RECORD_DIR"), "%0"), filepath.Join(os.Getenv("NTM_SESSION_TEST_RECORD_DIR"), "old-record")); err != nil {
					t.Fatal(err)
				}
			case "changed cwd":
				t.Setenv("NTM_SESSION_TEST_CWD", t.TempDir())
			}
			if err := verifyCapturedPanes(t.Context(), "saved_launch", original, panes); err == nil {
				t.Fatal("capture mixed different pane generations")
			}
		})
	}
}

func TestSavedLaunchPreflightProtectsExistingSession(t *testing.T) {
	for _, operation := range []string{"restore", "resume"} {
		for _, scenario := range []string{"omitted env", "wrong provider", "future schema", "missing worktree", "changed prompt", "missing account launcher", "opaque resume", "wrong resume provider", "cancelled"} {
			t.Run(operation+"/"+scenario, func(t *testing.T) {
				logPath := savedSessionRestoreFixture(t)
				state := &SessionState{Name: "protect_me", WorkDir: t.TempDir(), Panes: []PaneState{
					{Index: 0, AgentType: "cod", LaunchSpec: savedLaunchSpec("codex --model first")},
					{Index: 1, AgentType: "cod", LaunchSpec: savedLaunchSpec("codex --model second")},
				}}
				pane := &state.Panes[1]
				cfg := config.Default()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				switch scenario {
				case "omitted env":
					pane.LaunchSpec.OmittedEnv = []string{"EXPLICIT_TOKEN"}
				case "wrong provider":
					pane.LaunchSpec.AgentType = tmux.AgentClaude
				case "future schema":
					pane.LaunchSpec.Version++
				case "missing worktree":
					pane.WorkDir = filepath.Join(t.TempDir(), "missing-worktree")
				case "changed prompt":
					path := filepath.Join(t.TempDir(), "persona.txt")
					if err := os.WriteFile(path, []byte("new instructions"), 0600); err != nil {
						t.Fatal(err)
					}
					digest := sha256.Sum256([]byte("original instructions"))
					pane.LaunchSpec.SystemPromptFile, pane.LaunchSpec.SystemPromptSHA256 = path, hex.EncodeToString(digest[:])
				case "missing account launcher":
					pane.LaunchSpec.CAAMProfile = "saved-profile"
					cfg.Integrations.CAAM.BinaryPath = filepath.Join(t.TempDir(), "missing-caam")
				case "opaque resume", "wrong resume provider":
					if operation == "restore" {
						t.Skip("native-resume-specific preflight")
					}
					pane.SessionID, pane.SessionFreshness, pane.SessionConfidence = "native-id", agentsession.BindingFresh, 1
					if scenario == "opaque resume" {
						pane.LaunchSpec.Command = "wrapper codex --model saved"
					} else {
						pane.SessionProvider = "claude"
					}
				case "cancelled":
					cancel()
				}
				var err error
				if operation == "restore" {
					_, err = RestoreWithAgents(ctx, state, AgentCommands{}, cfg, RestoreOptions{Force: true})
				} else {
					_, err = ResumeContext(ctx, state, AgentCommands{}, ResumeOptions{Force: true, Config: cfg})
				}
				if err == nil {
					t.Fatal("invalid later pane passed whole-batch preflight")
				}
				calls, _ := os.ReadFile(logPath)
				if len(calls) != 0 {
					t.Fatalf("preflight failure touched the existing session: %s", calls)
				}
			})
		}
	}
}

func TestSavedNativeResumePreservesSettingsAndReplacesPriorID(t *testing.T) {
	logPath := savedSessionRestoreFixture(t)
	directory := t.TempDir()
	spec := savedLaunchSpec("/opt/codex resume 'old-id' --model saved-model -c 'model_reasoning_effort=high' --custom=value")
	state := &SessionState{Name: "original", WorkDir: t.TempDir(), Panes: []PaneState{{Index: 0, AgentType: "cod", WorkDir: directory,
		LaunchSpec: spec, SessionID: "current-id", SessionProvider: "codex", SessionFreshness: agentsession.BindingFresh, SessionConfidence: 1}}}
	before, _ := json.Marshal(state)
	result, err := ResumeContext(t.Context(), state, AgentCommands{Codex: "wrong-default"}, ResumeOptions{Name: "resumed", Force: true, PreferCASR: true})
	if err != nil || result == nil || result.Resumed != 1 || result.Launched != 0 || result.Panes[0].PaneID != "%0" {
		t.Fatalf("resume failed: %+v, %v", result, err)
	}
	expected := "/opt/codex resume 'current-id' --model saved-model -c 'model_reasoning_effort=high' --custom=value"
	recorded, err := tmux.ReadPaneLaunchSpecContext(t.Context(), "%0")
	if err != nil || recorded.Command != expected || recorded.Model != spec.Model || recorded.ReasoningEffort != spec.ReasoningEffort || recorded.Persona != spec.Persona {
		t.Fatalf("native resume lost exact settings: %+v, %v", recorded, err)
	}
	after, _ := json.Marshal(state)
	if string(before) != string(after) {
		t.Fatal("resume changed saved state")
	}
	calls, _ := os.ReadFile(logPath)
	if !strings.Contains(string(calls), "cd "+tmux.ShellQuote(directory)+" && "+expected) || strings.Contains(string(calls), "wrong-default") {
		t.Fatalf("resume used the wrong command or directory: %s", calls)
	}
}

func TestSavedLaunchMetadataFailureNeverDispatches(t *testing.T) {
	logPath := savedSessionRestoreFixture(t)
	t.Setenv("NTM_SESSION_TEST_METADATA_FAIL", "%1")
	state := &SessionState{Name: "partial", WorkDir: t.TempDir(), Panes: []PaneState{{Index: 0, AgentType: "cod", Command: "codex"}, {Index: 1, AgentType: "cod", Command: "codex"}}}
	result, err := RestoreWithAgents(t.Context(), state, AgentCommands{}, nil, RestoreOptions{Force: true})
	if err == nil || result == nil || result.Launched != 1 || result.Failed != 1 {
		t.Fatalf("metadata failure was not a partial launch failure: %+v, %v", result, err)
	}
	calls, _ := os.ReadFile(logPath)
	if strings.Contains(string(calls), "send-keys -t %1") || !strings.Contains(string(calls), "send-keys -t %0") {
		t.Fatalf("command dispatched without durable launch settings: %s", calls)
	}
}

func TestSavedLaunchRejectsReplacementShell(t *testing.T) {
	savedSessionRestoreFixture(t)
	panes, err := tmux.GetPanes("recovery")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_SESSION_TEST_PID", "9999999")
	if err := validateSavedLaunchTarget(t.Context(), "recovery", panes[0]); err == nil {
		t.Fatal("launch accepted a replacement shell under the original physical pane ID")
	}
}

func TestSavedLaunchRejectsMissingProcessIdentity(t *testing.T) {
	logPath := savedSessionRestoreFixture(t)
	t.Setenv("NTM_SESSION_TEST_PID", "0")
	state := &SessionState{Name: "unverified", WorkDir: t.TempDir(), Panes: []PaneState{{Index: 0, AgentType: "cod", Command: "codex"}}}
	result, err := RestoreWithAgents(t.Context(), state, AgentCommands{}, nil, RestoreOptions{Force: true})
	if err == nil || result == nil || result.Launched != 0 || result.Failed != 1 {
		t.Fatalf("missing process identity was not refused: %+v, %v", result, err)
	}
	calls, _ := os.ReadFile(logPath)
	if strings.Contains(string(calls), "send-keys") || strings.Contains(string(calls), "@ntm_agent_launch") {
		t.Fatalf("unverified process received launch metadata or a command: %s", calls)
	}
}

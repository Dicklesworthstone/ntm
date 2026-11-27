package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
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
				previousStop := stopRestoreSessionMonitor
				t.Cleanup(func() { stopRestoreSessionMonitor = previousStop })
				stops := 0
				stopRestoreSessionMonitor = func(context.Context, string) error {
					stops++
					return nil
				}
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
				if stops != 0 {
					t.Fatal("invalid saved launch stopped the existing resident monitor")
				}
				calls, _ := os.ReadFile(logPath)
				if len(calls) != 0 {
					t.Fatalf("preflight failure touched the existing session: %s", calls)
				}
			})
		}
	}
}

func TestSavedLaunchMissingCommandProtectsExistingSession(t *testing.T) {
	for _, operation := range []string{"restore", "resume"} {
		for _, kind := range []string{"pi", "cod", "saved-session-legacy-plugin"} {
			t.Run(operation+"/"+kind, func(t *testing.T) {
				logPath := savedSessionRestoreFixture(t)
				if kind == "saved-session-legacy-plugin" {
					if err := agentpkg.RegisterPlugin(kind, nil, nil, nil); err != nil {
						t.Fatal(err)
					}
				}
				state := &SessionState{Name: "protect_live", WorkDir: t.TempDir(), Panes: []PaneState{
					{Index: 0, AgentType: "cod", Command: "codex"},
					{Index: 1, AgentType: kind},
				}}
				var result *ResumeResult
				var err error
				if operation == "restore" {
					result, err = RestoreWithAgents(t.Context(), state, AgentCommands{}, nil, RestoreOptions{Force: true})
				} else {
					result, err = ResumeContext(t.Context(), state, AgentCommands{}, ResumeOptions{Force: true})
				}
				if err == nil || result != nil || !strings.Contains(err.Error(), "0.1") || !strings.Contains(err.Error(), "no usable launch command") {
					t.Fatalf("missing %s launch was silently skipped: result=%+v err=%v", kind, result, err)
				}
				calls, readErr := os.ReadFile(logPath)
				if readErr != nil && !os.IsNotExist(readErr) {
					t.Fatal(readErr)
				}
				if len(calls) != 0 {
					t.Fatalf("missing launch command replaced or touched the live session: %s", calls)
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

func TestSavedPiNativeResumeUsesExistingHeaderIdentity(t *testing.T) {
	logPath := savedSessionRestoreFixture(t)
	if err := agentpkg.RegisterPlugin("pi", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	store := filepath.Join(t.TempDir(), "pi sessions")
	if err := os.MkdirAll(store, 0700); err != nil {
		t.Fatal(err)
	}
	// Renamed transcripts must resume by the header identity, not the filename.
	transcript := filepath.Join(store, "2026-10-23T12-00-00-000Z_wrong-filename-id.jsonl")
	header, err := json.Marshal(map[string]any{"type": "session", "version": 3, "id": "saved-pi-id", "cwd": directory, "timestamp": "2026-10-23T12:00:00.000Z"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, append(header, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	spec := &tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentType("pi"),
		Command: "PI_CODING_AGENT_SESSION_DIR=" + tmux.ShellQuote(store) + " pi --session-id=prior-id --model 'provider/custom' --thinking high --approve",
		Model:   "provider/custom", ReasoningEffort: "high", Persona: "reviewer"}
	state := &SessionState{Name: "saved_pi", WorkDir: directory, Version: StateVersion, Panes: []PaneState{{
		Index: 0, AgentType: "pi", WorkDir: directory, LaunchSpec: spec,
		SessionID: "saved-pi-id", SessionProvider: "pi", SessionFreshness: agentsession.BindingFresh, SessionConfidence: 1,
	}}}
	if _, err := Save(state, SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(state.Name)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(loaded)
	result, err := ResumeContext(t.Context(), loaded, AgentCommands{}, ResumeOptions{Name: "resumed_pi", Force: true, PreferCASR: true})
	if err != nil || result == nil || result.Resumed != 1 {
		t.Fatalf("pi resume failed: result=%+v error=%v", result, err)
	}
	if result.Panes[0].SessionID != "saved-pi-id" || result.Panes[0].Provider != "pi" {
		t.Fatalf("resume reported a filename or wrong provider: %+v", result.Panes[0])
	}
	recorded, err := tmux.ReadPaneLaunchSpecContext(t.Context(), "%0")
	if err != nil || recorded == nil {
		t.Fatalf("read restored launch: %v", err)
	}
	for _, retained := range []string{"PI_CODING_AGENT_SESSION_DIR=" + tmux.ShellQuote(store), "--session " + tmux.ShellQuote(transcript), "--model 'provider/custom'", "--thinking high", "--approve"} {
		if !strings.Contains(recorded.Command, retained) {
			t.Fatalf("pi resume lost %q: %s", retained, recorded.Command)
		}
	}
	if strings.Contains(recorded.Command, "prior-id") || strings.Contains(recorded.Command, "--session-id") || recorded.Model != spec.Model || recorded.Persona != spec.Persona {
		t.Fatalf("pi resume changed settings or retained a conflicting selector: %+v", recorded)
	}
	after, _ := json.Marshal(loaded)
	if string(before) != string(after) {
		t.Fatal("pi resume mutated the saved snapshot")
	}
	calls, _ := os.ReadFile(logPath)
	if !strings.Contains(string(calls), "send-keys -t %0") || !strings.Contains(string(calls), "cd "+tmux.ShellQuote(directory)+" && "+recorded.Command) {
		t.Fatalf("pi launch did not cross the tmux dispatch surface: %s", calls)
	}
}

func TestSavedPiResumePreservesInheritedStoreAcrossSaveLoad(t *testing.T) {
	for _, source := range []string{"session directory", "agent directory", "default home"} {
		t.Run(source, func(t *testing.T) {
			logPath := savedSessionRestoreFixture(t)
			if err := agentpkg.RegisterPlugin("pi", nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
			t.Setenv("PI_CODING_AGENT_DIR", "")
			directory := t.TempDir()
			store := filepath.Join(os.Getenv("HOME"), ".pi", "agent", "sessions", "encoded-project")
			switch source {
			case "session directory":
				store = t.TempDir()
				t.Setenv("PI_CODING_AGENT_SESSION_DIR", store)
			case "agent directory":
				agentDir := t.TempDir()
				store = filepath.Join(agentDir, "sessions", "encoded-project")
				t.Setenv("PI_CODING_AGENT_DIR", agentDir)
			}
			if err := os.MkdirAll(store, 0700); err != nil {
				t.Fatal(err)
			}
			transcript := filepath.Join(store, "renamed-session.jsonl")
			header, err := json.Marshal(map[string]any{"type": "session", "version": 3, "id": "inherited-pi-id", "cwd": directory, "timestamp": time.Now().UTC().Format(time.RFC3339Nano)})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(transcript, append(header, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			state := &SessionState{Name: "inherited_pi", WorkDir: directory, Version: StateVersion, Panes: []PaneState{{
				Index: 0, AgentType: "pi", WorkDir: directory,
				LaunchSpec: &tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: "pi", Command: "pi --session-id inherited-pi-id --model provider/custom --thinking high"},
			}}}
			binding := agentsession.NewDiscoverer().ObserveBinding(t.Context(), "pi", directory, 0, time.Now())
			applyPaneSessionBindings(state.Panes, []agentsession.BindingObservation{binding})
			if !state.Panes[0].HasFreshSessionBinding() || state.Panes[0].SessionFile != transcript {
				t.Fatalf("inherited store was not discovered: %+v", binding)
			}
			savedPath, err := Save(state, SaveOptions{})
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(savedPath)
			if err != nil || info.Mode().Perm()&0077 != 0 {
				t.Fatalf("private resume reference is not in an owner-only save: %v, %v", info, err)
			}
			// A later caller inherits a different store containing the same
			// authoritative ID/cwd. It must not replace the captured transcript.
			ambient := t.TempDir()
			if err := os.WriteFile(filepath.Join(ambient, "collision.jsonl"), append(header, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PI_CODING_AGENT_SESSION_DIR", ambient)
			t.Setenv("PI_CODING_AGENT_DIR", "")
			loaded, err := Load(state.Name)
			if err != nil || loaded.Panes[0].SessionFile != transcript {
				t.Fatalf("save/load lost the process's exact store: %+v, %v", loaded, err)
			}
			public, err := json.Marshal(loaded)
			if err != nil || strings.Contains(string(public), transcript) || strings.Contains(string(public), "pi_resume_transcripts") {
				t.Fatalf("public session JSON exposed a private transcript reference: %s, %v", public, err)
			}
			result, err := ResumeContext(t.Context(), loaded, AgentCommands{}, ResumeOptions{Name: "resumed_pi", Force: true})
			if err != nil || result == nil || result.Resumed != 1 {
				t.Fatalf("resume from a different environment failed: %+v, %v", result, err)
			}
			command := result.Panes[0].Command
			if !strings.Contains(command, "--session "+tmux.ShellQuote(transcript)) || strings.Contains(command, ambient) ||
				!strings.Contains(command, "--model provider/custom --thinking high") {
				t.Fatalf("resume substituted a store or changed launch settings: %s", command)
			}
			calls, _ := os.ReadFile(logPath)
			if !strings.Contains(string(calls), "cd "+tmux.ShellQuote(directory)+" && "+command) {
				t.Fatalf("validated native resume never reached pane dispatch: %s", calls)
			}
		})
	}
}

func TestSavedPiResumeReferenceFailureCannotFallBackToAmbientStore(t *testing.T) {
	for _, scenario := range []string{"missing", "wrong id", "wrong cwd", "corrupt"} {
		t.Run(scenario, func(t *testing.T) {
			logPath := savedSessionRestoreFixture(t)
			if err := agentpkg.RegisterPlugin("pi", nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			directory, store, ambient := t.TempDir(), t.TempDir(), t.TempDir()
			transcript := filepath.Join(store, "original.jsonl")
			header, err := json.Marshal(map[string]any{"type": "session", "version": 3, "id": "saved-pi-id", "cwd": directory})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(ambient, "collision.jsonl"), append(header, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PI_CODING_AGENT_SESSION_DIR", ambient)
			state := &SessionState{Name: "protect_pi_reference", WorkDir: directory, Panes: []PaneState{{
				AgentType: "pi", Command: "pi --session-id saved-pi-id", SessionID: "saved-pi-id", SessionFile: transcript,
				SessionProvider: "pi", SessionFreshness: agentsession.BindingFresh, SessionConfidence: 1,
			}}}
			if _, err := Save(state, SaveOptions{}); err != nil {
				t.Fatal(err)
			}
			if scenario != "missing" {
				id, cwd := "saved-pi-id", directory
				if scenario == "wrong id" {
					id = "other-pi-id"
				}
				if scenario == "wrong cwd" {
					cwd = t.TempDir()
				}
				header, err = json.Marshal(map[string]any{"type": "session", "version": 3, "id": id, "cwd": cwd})
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "corrupt" {
					header = []byte("{invalid")
				}
				if err := os.WriteFile(transcript, append(header, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
			loaded, err := Load(state.Name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ResumeContext(t.Context(), loaded, AgentCommands{}, ResumeOptions{Force: true}); err == nil {
				t.Fatal("unavailable captured transcript was replaced by another store's session")
			}
			calls, _ := os.ReadFile(logPath)
			if len(calls) != 0 {
				t.Fatalf("failed pi reference validation touched tmux: %s", calls)
			}
		})
	}
}

func TestSavedPiResumeStorageRejectsUnboundReferences(t *testing.T) {
	for _, scenario := range []string{"unknown pane", "duplicate pane", "wrong id", "wrong provider", "relative path", "stale binding"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			pane := PaneState{AgentType: "pi", SessionID: "saved-id", SessionProvider: "pi",
				SessionFreshness: agentsession.BindingFresh, SessionConfidence: 1}
			reference := map[string]any{"pane": 0, "session_id": "saved-id", "path": filepath.Join(t.TempDir(), "original.jsonl")}
			references := []map[string]any{reference}
			switch scenario {
			case "unknown pane":
				reference["pane"] = 1
			case "duplicate pane":
				references = append(references, reference)
			case "wrong id":
				reference["session_id"] = "other-id"
			case "wrong provider":
				pane.AgentType, pane.SessionProvider = "omp", "omp"
			case "relative path":
				reference["path"] = "relative.jsonl"
			case "stale binding":
				pane.SessionFreshness = agentsession.BindingStale
			}
			data, err := json.Marshal(map[string]any{"name": "invalid_pi_reference", "panes": []PaneState{pane}, "pi_resume_transcripts": references})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(StorageDir(), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(StorageDir(), "invalid_pi_reference.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load("invalid_pi_reference"); err == nil {
				t.Fatal("private transcript reference was not bound to its saved pi pane")
			}
		})
	}
}

func TestSavedPiResumeRejectsUnverifiableTranscriptBeforeReplacingSession(t *testing.T) {
	for _, scenario := range []string{"missing", "wrong cwd", "wrong header id", "corrupt", "duplicate id"} {
		t.Run(scenario, func(t *testing.T) {
			logPath := savedSessionRestoreFixture(t)
			if err := agentpkg.RegisterPlugin("pi", nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			directory, store := t.TempDir(), t.TempDir()
			id, cwd := "saved-pi-id", directory
			if scenario == "wrong cwd" {
				cwd = t.TempDir()
			}
			if scenario == "wrong header id" {
				id = "other-pi-id"
			}
			header, err := json.Marshal(map[string]any{"type": "session", "version": 3, "id": id, "cwd": cwd})
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "corrupt" {
				header = []byte("{invalid")
			}
			if scenario != "missing" {
				if err := os.WriteFile(filepath.Join(store, "saved-pi-id.jsonl"), append(header, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "duplicate id" {
				if err := os.WriteFile(filepath.Join(store, "another-name.jsonl"), append(header, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
			state := &SessionState{Name: "protect_pi", WorkDir: directory, Panes: []PaneState{{
				AgentType: "pi", Command: "pi --session-dir " + tmux.ShellQuote(store), SessionID: "saved-pi-id",
				SessionProvider: "pi", SessionFreshness: agentsession.BindingFresh, SessionConfidence: 1,
			}}}
			if _, err := ResumeContext(t.Context(), state, AgentCommands{}, ResumeOptions{Force: true}); err == nil {
				t.Fatal("unverifiable pi transcript passed launch preflight")
			}
			calls, _ := os.ReadFile(logPath)
			if len(calls) != 0 {
				t.Fatalf("pi preflight failure touched existing tmux session: %s", calls)
			}
		})
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
	if err := validateSavedLaunchTarget(t.Context(), "recovery", panes[0], tmux.AgentCodex); err == nil {
		t.Fatal("launch accepted a replacement shell under the original physical pane ID")
	}
}

func TestSavedLaunchRejectsConcurrentlyAdoptedDifferentAgent(t *testing.T) {
	savedSessionRestoreFixture(t)
	panes, err := tmux.GetPanes("recovery")
	if err != nil {
		t.Fatal(err)
	}
	// Fresh restored shell pane starts unlabeled (AgentUser), while live tmux
	// reports it was concurrently adopted as Codex ("cod").
	fresh := panes[0]
	fresh.Type = tmux.AgentUser
	if err := validateSavedLaunchTarget(t.Context(), "recovery", fresh, tmux.AgentClaude); err == nil {
		t.Fatal("launch accepted a fresh shell pane concurrently adopted as a different agent type")
	}
	// Same target agent type remains allowed.
	if err := validateSavedLaunchTarget(t.Context(), "recovery", fresh, tmux.AgentCodex); err != nil {
		t.Fatalf("launch rejected a pane matching the target agent type: %v", err)
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

func TestSavedSessionForceWaitsForDestinationMonitor(t *testing.T) {
	for _, operation := range []string{"topology", "restore", "resume"} {
		t.Run(operation, func(t *testing.T) {
			logPath := savedSessionRestoreFixture(t)
			previousStop := stopRestoreSessionMonitor
			t.Cleanup(func() { stopRestoreSessionMonitor = previousStop })
			entered, release := make(chan string, 1), make(chan struct{})
			stopRestoreSessionMonitor = func(ctx context.Context, session string) error {
				entered <- session
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-release:
					return nil
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			state := &SessionState{Name: "source", WorkDir: t.TempDir(), Panes: []PaneState{{Index: 0, AgentType: "cod", Command: "codex"}}}
			done, completed := make(chan error, 1), make(chan struct{})
			t.Cleanup(func() {
				cancel()
				<-completed
			})
			go func() {
				defer close(completed)
				var err error
				switch operation {
				case "topology":
					err = RestoreContext(ctx, state, RestoreOptions{Name: "destination", Force: true})
				case "restore":
					_, err = RestoreWithAgents(ctx, state, AgentCommands{}, nil, RestoreOptions{Name: "destination", Force: true})
				case "resume":
					_, err = ResumeContext(ctx, state, AgentCommands{}, ResumeOptions{Name: "destination", Force: true})
				}
				done <- err
			}()
			select {
			case session := <-entered:
				if session != "destination" {
					t.Fatalf("stopped monitor for %q, want destination", session)
				}
			case err := <-done:
				t.Fatalf("restore returned before resident shutdown: %v", err)
			case <-ctx.Done():
				t.Fatal("restore did not reach resident shutdown")
			}
			calls, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(calls), "kill-session") || strings.Contains(string(calls), "new-session") {
				t.Fatalf("session was replaced before the resident stopped: %s", calls)
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatalf("restore after resident shutdown: %v", err)
			}
			calls, err = os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			kill, create := strings.Index(string(calls), "kill-session -t =destination"), strings.Index(string(calls), "new-session")
			if kill < 0 || create <= kill || strings.Contains(string(calls), "-t =source") {
				t.Fatalf("restore did not replace only its destination after shutdown: %s", calls)
			}
		})
	}
}

func TestSavedSessionForcePreservesExistingSessionWhenMonitorCannotStop(t *testing.T) {
	for _, mode := range []string{"failure", "canceled", "canceled-after-join"} {
		t.Run(mode, func(t *testing.T) {
			logPath := savedSessionRestoreFixture(t)
			previousStop := stopRestoreSessionMonitor
			t.Cleanup(func() { stopRestoreSessionMonitor = previousStop })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stopError := errors.New("resident recovery has not stopped")
			stops := 0
			stopRestoreSessionMonitor = func(stopCtx context.Context, session string) error {
				stops++
				if stopCtx != ctx || session != "existing" {
					t.Fatalf("monitor stop lost target or context: %q, %v", session, stopCtx)
				}
				if mode == "failure" {
					return stopError
				}
				cancel()
				if mode == "canceled-after-join" {
					return nil
				}
				return stopCtx.Err()
			}
			state := &SessionState{Name: "existing", WorkDir: t.TempDir()}
			err := RestoreContext(ctx, state, RestoreOptions{Force: true})
			wantError := stopError
			if mode != "failure" {
				wantError = context.Canceled
			}
			if !errors.Is(err, wantError) || stops != 1 {
				t.Fatalf("error=%v stops=%d, want %v and 1", err, stops, wantError)
			}
			calls, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(calls), "kill-session") || strings.Contains(string(calls), "new-session") {
				t.Fatalf("shutdown refusal or cancellation still replaced the session: %s", calls)
			}
		})
	}
}

func TestSavedSessionMonitorStopOnlyAppliesToForcedReplacement(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		exists bool
		force  bool
	}{
		{name: "existing without force", exists: true},
		{name: "new session"},
		{name: "new session with force", force: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			logPath := savedSessionRestoreFixture(t)
			if !scenario.exists {
				t.Setenv("NTM_SESSION_TEST_EXISTS", "0")
			}
			previousStop := stopRestoreSessionMonitor
			t.Cleanup(func() { stopRestoreSessionMonitor = previousStop })
			stopRestoreSessionMonitor = func(context.Context, string) error {
				t.Fatal("restore stopped a monitor without replacing an existing session")
				return nil
			}
			state := &SessionState{Name: "destination", WorkDir: t.TempDir()}
			err := RestoreContext(t.Context(), state, RestoreOptions{Force: scenario.force})
			if scenario.exists != (err != nil) {
				t.Fatalf("restore error=%v, existing=%t force=%t", err, scenario.exists, scenario.force)
			}
			calls, readErr := os.ReadFile(logPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.Contains(string(calls), "kill-session") || strings.Contains(string(calls), "new-session") == scenario.exists {
				t.Fatalf("restore performed the wrong topology mutation: %s", calls)
			}
		})
	}
}

func TestSavedSessionForceOnRemoteHostLeavesLocalMonitorRunning(t *testing.T) {
	logPath := savedSessionRestoreFixture(t)
	dir := filepath.Dir(logPath)
	sshLog := savedSessionRemoteFixture(t, dir)
	previousStop := stopRestoreSessionMonitor
	t.Cleanup(func() { stopRestoreSessionMonitor = previousStop })
	stops := 0
	stopRestoreSessionMonitor = func(context.Context, string) error {
		stops++
		return errors.New("local monitor belongs to another host")
	}
	state := &SessionState{Name: "same-name", WorkDir: dir}
	if err := RestoreContext(t.Context(), state, RestoreOptions{Force: true}); err != nil {
		t.Fatalf("remote restore was blocked by a local monitor: %v", err)
	}
	if stops != 0 {
		t.Fatalf("remote replacement stopped %d local monitors", stops)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	kill, create := strings.Index(string(calls), "kill-session -t =same-name"), strings.Index(string(calls), "new-session")
	if kill < 0 || create <= kill {
		t.Fatalf("remote replacement did not complete: %s", calls)
	}
	transport, err := os.ReadFile(sshLog)
	if err != nil || !strings.Contains(string(transport), "-- operator@remote.example") || !strings.Contains(string(transport), "'kill-session'") {
		t.Fatalf("restore did not use its SSH target: calls=%s error=%v", transport, err)
	}
}

func savedSessionRemoteFixture(t *testing.T, dir string) string {
	t.Helper()
	sshLog := filepath.Join(dir, "ssh-calls")
	t.Setenv("NTM_SESSION_SSH_LOG", sshLog)
	t.Setenv("NTM_SESSION_REMOTE_DIRECTORIES", "check")
	const script = `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$NTM_SESSION_SSH_LOG"
[ "$1" = '--' ] && [ "$2" = 'operator@remote.example' ] || exit 42
case "$3" in
  *"test "*)
    case "$NTM_SESSION_REMOTE_DIRECTORIES" in
      allow) exit 0 ;;
      deny) echo 'remote directory unavailable' >&2; exit 1 ;;
    esac ;;
esac
exec /bin/sh -c "$3"
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	tmux.DefaultClient = tmux.NewClient("operator@remote.example")
	return sshLog
}

func TestRemoteCaptureDoesNotAdoptLocalProviderOrGitEvidence(t *testing.T) {
	logPath := savedSessionRestoreFixture(t)
	dir := filepath.Dir(logPath)
	savedSessionRemoteFixture(t, dir)
	t.Setenv("NTM_SESSION_TEST_PANES", "1")
	localProbes := filepath.Join(dir, "local-probes")
	t.Setenv("NTM_SESSION_LOCAL_PROBES", localProbes)
	for _, tool := range []string{"git", "casr"} {
		if err := os.WriteFile(filepath.Join(dir, tool), []byte("#!/bin/sh\nprintf '%s\\n' \"$0 $*\" >> \"$NTM_SESSION_LOCAL_PROBES\"\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	dayDir := filepath.Join(dir, ".codex", "sessions", "2026", "11", "11")
	if err := os.MkdirAll(dayDir, 0700); err != nil {
		t.Fatal(err)
	}
	const localSessionID = "019f4e56-c2ba-7652-91d4-e4173bc0f302"
	metadata, err := json.Marshal(map[string]interface{}{"type": "session_meta", "payload": map[string]string{"id": localSessionID, "cwd": dir}})
	if err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(dayDir, "rollout-2026-11-11T10-00-00-"+localSessionID+".jsonl")
	if err := os.WriteFile(transcript, append(metadata, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	localBinding := agentsession.NewDiscoverer().ObserveBinding(t.Context(), "cod", dir, 0, time.Now())
	if localBinding.SessionID != localSessionID || localBinding.Freshness != agentsession.BindingFresh {
		t.Fatalf("local collision fixture is not a fresh provider binding: %+v", localBinding)
	}
	probesBefore, _ := os.ReadFile(localProbes)
	state, err := Capture("remote_capture")
	if err != nil {
		t.Fatal(err)
	}
	if state.RemoteHost != "operator@remote.example" || state.WorkDir != dir || len(state.Panes) != 1 {
		t.Fatalf("remote topology lost its host or path: %+v", state)
	}
	pane := state.Panes[0]
	if pane.SessionID != "" || pane.SessionProvider != "" || pane.HasFreshSessionBinding() || pane.SessionFreshness != agentsession.BindingUnavailable || pane.SessionFailureCode != "remote_host" {
		t.Fatalf("remote save borrowed local provider evidence: %+v", pane)
	}
	if state.GitBranch != "" || state.GitRemote != "" || state.GitCommit != "" {
		t.Fatalf("remote save borrowed local git metadata: %+v", state)
	}
	if probesAfter, _ := os.ReadFile(localProbes); string(probesAfter) != string(probesBefore) {
		t.Fatalf("remote capture invoked local provider or git tools: %s", probesAfter)
	}
	if _, err := Save(state, SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(state.Name)
	if err != nil || loaded.RemoteHost != state.RemoteHost || loaded.Panes[0].SessionFailureCode != "remote_host" {
		t.Fatalf("saved remote provenance did not survive reload: %+v, %v", loaded, err)
	}
}

func TestRemoteTopologyRestoreUsesTargetHostDirectories(t *testing.T) {
	for _, scenario := range []string{"remote only", "quoted path"} {
		t.Run(scenario, func(t *testing.T) {
			logPath := savedSessionRestoreFixture(t)
			dir := filepath.Dir(logPath)
			sshLog := savedSessionRemoteFixture(t, dir)
			remoteDir := filepath.Join(dir, "remote-only-project")
			paneDir := filepath.Join(dir, "remote-only-worktree")
			if scenario == "remote only" {
				t.Setenv("NTM_SESSION_REMOTE_DIRECTORIES", "allow")
			} else {
				remoteDir = filepath.Join(dir, "work 'quote' $(false)")
				paneDir = remoteDir
				if err := os.MkdirAll(remoteDir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			state := &SessionState{Name: "remote_restore", RemoteHost: "operator@remote.example", WorkDir: remoteDir,
				Panes: []PaneState{{Index: 0, AgentType: "cod", WorkDir: paneDir}}}
			if err := RestoreContext(t.Context(), state, RestoreOptions{Force: true}); err != nil {
				t.Fatalf("remote topology restore: %v", err)
			}
			if scenario == "remote only" {
				for _, directory := range []string{remoteDir, paneDir} {
					if _, err := os.Stat(directory); !os.IsNotExist(err) {
						t.Fatalf("remote restore created a local directory %q: %v", directory, err)
					}
				}
			}
			calls, err := os.ReadFile(logPath)
			if err != nil || !strings.Contains(string(calls), "kill-session") || !strings.Contains(string(calls), "new-session") || !strings.Contains(string(calls), paneDir) || strings.Contains(string(calls), "send-keys") {
				t.Fatalf("remote topology did not restore without agent dispatch: %s, %v", calls, err)
			}
			transport, err := os.ReadFile(sshLog)
			check, kill := strings.Index(string(transport), "test "), strings.Index(string(transport), "kill-session")
			if err != nil || check < 0 || kill <= check {
				t.Fatalf("remote directory proof was not acquired before replacement: %s, %v", transport, err)
			}
		})
	}
}

func TestRemoteTopologyPreflightPreservesExistingSession(t *testing.T) {
	for _, scenario := range []string{"local target", "different host", "missing directory", "empty directory", "relative directory", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			logPath := savedSessionRestoreFixture(t)
			dir := filepath.Dir(logPath)
			savedSessionRemoteFixture(t, dir)
			state := &SessionState{Name: "keep_me", RemoteHost: "operator@remote.example", WorkDir: dir}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch scenario {
			case "local target":
				tmux.DefaultClient = tmux.NewClient("")
			case "different host":
				tmux.DefaultClient = tmux.NewClient("operator@other.example")
			case "missing directory":
				t.Setenv("NTM_SESSION_REMOTE_DIRECTORIES", "deny")
			case "empty directory":
				state.WorkDir = ""
			case "relative directory":
				state.WorkDir = "relative/project"
			case "cancelled":
				cancel()
			}
			if err := RestoreContext(ctx, state, RestoreOptions{Force: true}); err == nil {
				t.Fatal("unproven remote restore was allowed")
			}
			if calls, _ := os.ReadFile(logPath); len(calls) != 0 {
				t.Fatalf("remote preflight failure touched existing tmux session: %s", calls)
			}
		})
	}
}

func TestRemoteAgentRelaunchRefusesBeforeForceReplacement(t *testing.T) {
	for _, operation := range []string{"restore", "resume"} {
		for _, target := range []string{"ssh", "local from remote save"} {
			t.Run(operation+"/"+target, func(t *testing.T) {
				logPath := savedSessionRestoreFixture(t)
				dir := filepath.Dir(logPath)
				state := &SessionState{Name: "keep_agents", WorkDir: filepath.Join(dir, "must-not-create"),
					Panes: []PaneState{{Index: 0, AgentType: "cod", Command: "codex", SessionID: "foreign-session", SessionFreshness: agentsession.BindingFresh, SessionConfidence: 1}}}
				if target == "ssh" {
					savedSessionRemoteFixture(t, dir)
				} else {
					state.RemoteHost = "operator@remote.example"
				}
				var result *ResumeResult
				var err error
				if operation == "restore" {
					result, err = RestoreWithAgents(t.Context(), state, AgentCommands{}, nil, RestoreOptions{Force: true})
				} else {
					result, err = ResumeContext(t.Context(), state, AgentCommands{}, ResumeOptions{Force: true})
				}
				if result != nil || err == nil || !strings.Contains(err.Error(), "SSH") {
					t.Fatalf("remote launch was not refused before effects: %+v, %v", result, err)
				}
				if calls, _ := os.ReadFile(logPath); len(calls) != 0 {
					t.Fatalf("remote relaunch touched existing session: %s", calls)
				}
				if _, statErr := os.Stat(state.WorkDir); !os.IsNotExist(statErr) {
					t.Fatalf("remote relaunch performed local filesystem preparation: %v", statErr)
				}
			})
		}
	}
}

func TestSavedLaunchFencesMetadataInputAndEnterRaces(t *testing.T) {
	for _, operation := range []string{"restore", "resume"} {
		for _, phase := range []string{"metadata", "retag", "input", "enter"} {
			t.Run(operation+"/"+phase, func(t *testing.T) {
				logPath := savedSessionRestoreFixture(t)
				if phase == "retag" {
					t.Setenv("NTM_SESSION_TEST_RETAG_PANE", "%1")
				} else {
					t.Setenv("NTM_SESSION_TEST_RACE_PANE", "%1")
					t.Setenv("NTM_SESSION_TEST_RACE_PHASE", phase)
				}
				workDir := t.TempDir()
				state := &SessionState{
					Name:    "race_guard",
					WorkDir: workDir,
					Version: StateVersion,
					Panes: []PaneState{
						{
							Index:             0,
							AgentType:         "cod",
							LaunchSpec:        savedLaunchSpec("codex --model first"),
							SessionID:         "sess-first",
							SessionProvider:   "codex",
							SessionFreshness:  agentsession.BindingFresh,
							SessionConfidence: 1,
						},
						{
							Index:             1,
							AgentType:         "cod",
							LaunchSpec:        savedLaunchSpec("codex --model second"),
							SessionID:         "sess-second",
							SessionProvider:   "codex",
							SessionFreshness:  agentsession.BindingFresh,
							SessionConfidence: 1,
						},
					},
				}
				var (
					result *ResumeResult
					err    error
				)
				if operation == "restore" {
					result, err = RestoreWithAgents(t.Context(), state, AgentCommands{}, nil, RestoreOptions{Force: true})
				} else {
					result, err = ResumeContext(t.Context(), state, AgentCommands{}, ResumeOptions{Force: true})
				}
				if !errors.Is(err, tmux.ErrPaneIdentityChanged) {
					t.Fatalf("%s %s race did not return ErrPaneIdentityChanged: result=%+v err=%v", operation, phase, result, err)
				}
				if result == nil || result.Launched+result.Resumed != 1 || result.Failed != 1 || len(result.Panes) != 2 {
					t.Fatalf("%s %s race lost partial outcomes: %+v", operation, phase, result)
				}
				wantFirstAction := "launched"
				if operation == "resume" {
					wantFirstAction = "resumed"
				}
				if result.Panes[0].Action != wantFirstAction || result.Panes[0].PaneID != "%0" {
					t.Fatalf("pane 0 outcome = %+v, want %s on %%0", result.Panes[0], wantFirstAction)
				}
				if result.Panes[1].Action != "failed" || result.Panes[1].PaneID != "%1" || !strings.Contains(result.Panes[1].Error, tmux.ErrPaneIdentityChanged.Error()) {
					t.Fatalf("pane 1 outcome = %+v, want failed with identity change on %%1", result.Panes[1])
				}
				callsBytes, readErr := os.ReadFile(logPath)
				if readErr != nil {
					t.Fatal(readErr)
				}
				calls := string(callsBytes)
				if !strings.Contains(calls, "if-shell -F -t %1") {
					t.Fatalf("pane %%1 launch was not fenced by if-shell: %s", calls)
				}
				if !strings.Contains(calls, "send-keys -t %0 -l -- ") || !strings.Contains(calls, "send-keys -t %0 Enter") {
					t.Fatalf("pane %%0 did not complete its guarded launch: %s", calls)
				}
				if strings.Count(calls, "kill-session") != 1 || strings.Index(calls, "kill-session") > strings.Index(calls, "new-session") {
					t.Fatalf("partial race failure killed the restored session: %s", calls)
				}
				switch phase {
				case "metadata", "retag":
					if strings.Contains(calls, "set-option -p -t %1 @ntm_agent_launch") {
						t.Fatalf("%s race still wrote @ntm_agent_launch on %%1: %s", phase, calls)
					}
					if strings.Contains(calls, "send-keys -t %1") || strings.Contains(calls, "pasted %1") {
						t.Fatalf("%s race still dispatched input to %%1: %s", phase, calls)
					}
					if stored, readSpecErr := tmux.ReadPaneLaunchSpecContext(t.Context(), "%1"); readSpecErr != nil || stored != nil {
						t.Fatalf("%s race persisted launch spec on %%1: stored=%+v err=%v", phase, stored, readSpecErr)
					}
				case "input":
					if !strings.Contains(calls, "set-option -p -t %1 @ntm_agent_launch") {
						t.Fatalf("expected guarded metadata write before input race: %s", calls)
					}
					if strings.Contains(calls, "send-keys -t %1") || strings.Contains(calls, "pasted %1") {
						t.Fatalf("input race still delivered text or Enter to %%1: %s", calls)
					}
				case "enter":
					if !strings.Contains(calls, "send-keys -t %1 -l -- ") {
						t.Fatalf("expected guarded literal input before enter race: %s", calls)
					}
					if strings.Contains(calls, "send-keys -t %1 Enter") {
						t.Fatalf("enter race still sent stale Enter to %%1: %s", calls)
					}
				}
			})
		}
	}
}

func TestSavedLaunchLongCommandPreflightAndPrivateHandoff(t *testing.T) {
	longArg := strings.Repeat("x", tmux.MaxTypedLaunchCommandBytes+256)
	longCmd := "codex --model long-model --custom=" + longArg

	for _, operation := range []string{"restore", "resume"} {
		t.Run(operation+"/preflight_failure_preserves_live_session", func(t *testing.T) {
			logPath := savedSessionRestoreFixture(t)
			workDir := t.TempDir()
			missingTmp := filepath.Join(t.TempDir(), "no-such-tmp-dir")
			t.Setenv("TMPDIR", missingTmp)
			state := &SessionState{
				Name:    "protect_long",
				WorkDir: workDir,
				Version: StateVersion,
				Panes: []PaneState{
					{Index: 0, AgentType: "cod", Command: "codex --model short"},
					{Index: 1, AgentType: "cod", Command: longCmd},
				},
			}
			var (
				result *ResumeResult
				err    error
			)
			if operation == "restore" {
				result, err = RestoreWithAgents(t.Context(), state, AgentCommands{}, nil, RestoreOptions{Force: true})
			} else {
				result, err = ResumeContext(t.Context(), state, AgentCommands{}, ResumeOptions{Force: true})
			}
			if err == nil || result != nil || !strings.Contains(err.Error(), "private launch handoff") {
				t.Fatalf("long-command handoff failure was not caught in preflight: result=%+v err=%v", result, err)
			}
			calls, readErr := os.ReadFile(logPath)
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			if len(calls) != 0 {
				t.Fatalf("long-command preflight failure touched the live session: %s", calls)
			}
		})

		t.Run(operation+"/private_handoff_delivers_and_cleans_up", func(t *testing.T) {
			logPath := savedSessionRestoreFixture(t)
			recordDir := os.Getenv("NTM_SESSION_TEST_RECORD_DIR")
			tmpBase := t.TempDir()
			t.Setenv("TMPDIR", tmpBase)
			workDir := t.TempDir()
			state := &SessionState{
				Name:    "long_handoff",
				WorkDir: workDir,
				Version: StateVersion,
				Panes: []PaneState{
					{
						Index:             0,
						AgentType:         "cod",
						LaunchSpec:        savedLaunchSpec(longCmd),
						SessionID:         "long-sess-id",
						SessionProvider:   "codex",
						SessionFreshness:  agentsession.BindingFresh,
						SessionConfidence: 1,
					},
				},
			}
			var (
				result *ResumeResult
				err    error
			)
			if operation == "restore" {
				result, err = RestoreWithAgents(t.Context(), state, AgentCommands{}, nil, RestoreOptions{Force: true})
			} else {
				result, err = ResumeContext(t.Context(), state, AgentCommands{}, ResumeOptions{Force: true})
			}
			if err != nil || result == nil || result.Failed != 0 || result.Launched+result.Resumed != 1 {
				t.Fatalf("%s long command handoff failed: result=%+v err=%v", operation, result, err)
			}
			calls, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(calls), "/bin/sh -p ") || strings.Contains(string(calls), longArg) {
				t.Fatalf("long command was typed directly into tmux instead of private FIFO handoff: %s", calls)
			}
			payloadPath := filepath.Join(recordDir, "handoff-payload-%0")
			var payloadBytes []byte
			deadline := time.Now().Add(2 * time.Second)
			for {
				var readPayloadErr error
				payloadBytes, readPayloadErr = os.ReadFile(payloadPath)
				if readPayloadErr == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("private FIFO payload was not delivered: %v", readPayloadErr)
				}
				time.Sleep(10 * time.Millisecond)
			}
			payload := string(payloadBytes)
			if !strings.Contains(payload, longArg) {
				t.Fatalf("private FIFO payload truncated long command: len=%d", len(payload))
			}
			if operation == "resume" && !strings.Contains(payload, "resume 'long-sess-id'") {
				t.Fatalf("private FIFO resume payload lost session ID: %s", payload)
			}
			entries, err := os.ReadDir(tmpBase)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("private FIFO handoff leaked temporary directory entries: %+v", entries)
			}
		})

		t.Run(operation+"/handoff_enter_race_refuses_and_cleans_up", func(t *testing.T) {
			logPath := savedSessionRestoreFixture(t)
			tmpBase := t.TempDir()
			t.Setenv("TMPDIR", tmpBase)
			t.Setenv("NTM_SESSION_TEST_RACE_PANE", "%0")
			t.Setenv("NTM_SESSION_TEST_RACE_PHASE", "enter")
			state := &SessionState{
				Name:    "long_race",
				WorkDir: t.TempDir(),
				Version: StateVersion,
				Panes:   []PaneState{{Index: 0, AgentType: "cod", Command: longCmd}},
			}
			var (
				result *ResumeResult
				err    error
			)
			if operation == "restore" {
				result, err = RestoreWithAgents(t.Context(), state, AgentCommands{}, nil, RestoreOptions{Force: true})
			} else {
				result, err = ResumeContext(t.Context(), state, AgentCommands{}, ResumeOptions{Force: true})
			}
			if !errors.Is(err, tmux.ErrPaneIdentityChanged) || result == nil || result.Failed != 1 {
				t.Fatalf("long-command enter race was not refused: result=%+v err=%v", result, err)
			}
			calls, _ := os.ReadFile(logPath)
			if strings.Contains(string(calls), "send-keys -t %0 Enter") {
				t.Fatalf("long-command enter race still sent Enter: %s", calls)
			}
			entries, err := os.ReadDir(tmpBase)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("refused private handoff leaked temporary directory entries: %+v", entries)
			}
		})
	}
}

func TestRealSavedLaunchGuardedAgainstReplacementAndLongCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("real tmux test requires POSIX")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	oldClient := tmux.DefaultClient
	client := tmux.NewClient("")
	client.MaxSendDelay = 50 * time.Millisecond
	tmux.DefaultClient = client
	t.Cleanup(func() { tmux.DefaultClient = oldClient })

	t.Run("long_command_executes_via_real_tmux_handoff", func(t *testing.T) {
		sessionName := fmt.Sprintf("ntm_test_long_%d", time.Now().UnixNano())
		t.Cleanup(func() { _ = tmux.DefaultClient.RunSilent("kill-session", "-t", tmux.TargetSession(sessionName)) })
		workDir := t.TempDir()
		marker := filepath.Join(workDir, "long-output")
		want := strings.Repeat("Z", tmux.MaxTypedLaunchCommandBytes+300)
		command := fmt.Sprintf("printf '%%s' %s > %s", tmux.ShellQuote(want), tmux.ShellQuote(marker))
		state := &SessionState{
			Name:    sessionName,
			WorkDir: workDir,
			Version: StateVersion,
			Panes:   []PaneState{{Index: 0, AgentType: "cod", Command: command}},
		}
		result, err := RestoreWithAgents(t.Context(), state, AgentCommands{}, nil, RestoreOptions{Force: true})
		if err != nil || result == nil || result.Launched != 1 || result.Failed != 0 {
			t.Fatalf("real tmux long restore failed: result=%+v err=%v", result, err)
		}
		deadline := time.Now().Add(4 * time.Second)
		for {
			data, readErr := os.ReadFile(marker)
			if readErr == nil && string(data) == want {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("real tmux private handoff did not execute full long command: err=%v len=%d want=%d", readErr, len(data), len(want))
			}
			time.Sleep(25 * time.Millisecond)
		}
	})

	t.Run("real_tmux_replacement_refuses_metadata_and_input", func(t *testing.T) {
		sessionName := fmt.Sprintf("ntm_test_race_%d", time.Now().UnixNano())
		t.Cleanup(func() { _ = tmux.DefaultClient.RunSilent("kill-session", "-t", tmux.TargetSession(sessionName)) })
		workDir := t.TempDir()
		if err := tmux.CreateSessionContext(t.Context(), sessionName, workDir); err != nil {
			t.Fatal(err)
		}
		panes, err := tmux.GetPanesContext(t.Context(), sessionName)
		if err != nil || len(panes) != 1 {
			t.Fatalf("get panes: %+v, %v", panes, err)
		}
		orig := panes[0]
		if err := tmux.SetPaneAgentIdentityContext(t.Context(), orig.ID, "race__cod_1", tmux.AgentCodex); err != nil {
			t.Fatal(err)
		}
		orig.Type = tmux.AgentCodex
		marker := filepath.Join(workDir, "must-not-exist")
		state := &SessionState{
			Name:    sessionName,
			WorkDir: workDir,
			Version: StateVersion,
			Panes:   []PaneState{{Index: 0, AgentType: "cod", Command: "printf bad > " + tmux.ShellQuote(marker)}},
		}
		launches, err := preflightSavedLaunches(t.Context(), sessionName, state, AgentCommands{}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer closeSavedLaunches(launches)
		// Replace the shell process in the same physical pane before dispatch.
		if err := tmux.DefaultClient.RespawnPaneIfCurrentContext(t.Context(), orig.ID, orig.PID, true, workDir, true); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			cur, gErr := tmux.GetPanesContext(t.Context(), sessionName)
			if gErr == nil && len(cur) == 1 && cur[0].PID != orig.PID && cur[0].IdleShell() {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("pane did not respawn with a new PID")
			}
			time.Sleep(15 * time.Millisecond)
		}
		// Prove direct guarded metadata and send refuse the stale PID inside tmux's queue.
		if err := tmux.DefaultClient.SetPaneLaunchSpecIfCurrentContext(t.Context(), orig.ID, orig.PID, *launches[0].spec); !errors.Is(err, tmux.ErrPaneIdentityChanged) {
			t.Fatalf("real tmux metadata guard did not refuse replaced PID: %v", err)
		}
		if err := tmux.DefaultClient.SendKeysForAgentIfCurrentContext(t.Context(), orig.ID, launches[0].delivery, true, launches[0].spec.AgentType, orig.PID, true); !errors.Is(err, tmux.ErrPaneIdentityChanged) {
			t.Fatalf("real tmux input guard did not refuse replaced PID: %v", err)
		}
		// Prove dispatchSavedLaunches refuses the replaced pane and leaves spec & marker untouched.
		result, err := dispatchSavedLaunches(t.Context(), sessionName, launches, []tmux.Pane{orig})
		if err == nil || result == nil || result.Failed != 1 || result.Launched != 0 {
			t.Fatalf("dispatchSavedLaunches did not fail closed on replaced pane: result=%+v err=%v", result, err)
		}
		if stored, readErr := tmux.ReadPaneLaunchSpecContext(t.Context(), orig.ID); readErr != nil || stored != nil {
			t.Fatalf("replaced pane received launch metadata: stored=%+v err=%v", stored, readErr)
		}
		if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("replaced pane executed refused launch command: %v", statErr)
		}
	})
}

func TestWaitForFreshRestoreShells_ImmediateAndCanceled(t *testing.T) {
	t.Parallel()

	// Already-idle, dead, claimed, or nil-plan panes return immediately without polling tmux.
	launches := []savedPaneLaunch{
		{plan: &resilience.AgentLaunchPlan{}},
		{plan: &resilience.AgentLaunchPlan{}},
		{plan: nil},
	}
	panes := []tmux.Pane{
		{ID: "%1", PID: 0, Command: "zsh"},
		{ID: "%2", PID: 999999, Dead: true, Command: "sleep"},
		{ID: "%3", PID: 999999, Command: "sleep"},
	}
	start := time.Now()
	waitForFreshRestoreShells(context.Background(), "nonexistent-session", launches, panes)
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("waitForFreshRestoreShells stalled %v on non-pending panes", elapsed)
	}

	// Canceled context must return immediately even when a pane is not yet idle.
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	start = time.Now()
	waitForFreshRestoreShells(canceledCtx, "nonexistent-session",
		[]savedPaneLaunch{{plan: &resilience.AgentLaunchPlan{}}},
		[]tmux.Pane{{ID: "%9", PID: os.Getpid(), Command: "sleep"}},
	)
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("waitForFreshRestoreShells stalled %v on canceled context", elapsed)
	}
}

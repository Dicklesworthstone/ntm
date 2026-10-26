package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
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
	sshLog := filepath.Join(dir, "ssh-calls")
	t.Setenv("NTM_SESSION_SSH_LOG", sshLog)
	const script = `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$NTM_SESSION_SSH_LOG"
[ "$1" = '--' ] && [ "$2" = 'operator@remote.example' ] || exit 42
exec /bin/sh -c "$3"
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	tmux.DefaultClient = tmux.NewClient("operator@remote.example")
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

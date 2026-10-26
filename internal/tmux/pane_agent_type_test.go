package tmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func waitGuardedTestPane(t *testing.T, session string, accept func(Pane) bool) Pane {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		panes, err := GetPanesContext(t.Context(), session)
		if err != nil {
			t.Fatal(err)
		}
		if len(panes) == 1 && accept(panes[0]) {
			return panes[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane did not reach the expected guarded-test state: %+v", panes)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestGuardedPaneMutationsRejectInvalidInputs(t *testing.T) {
	client := NewClient("")
	spec := AgentLaunchSpec{Version: AgentLaunchSpecVersion, AgentType: AgentCodex, Command: "codex"}
	for _, target := range []string{"", "session:0.1", "%7;kill-server", "%7\n"} {
		if err := client.RespawnPaneIfCurrentContext(t.Context(), target, 1, true, "", false); err == nil {
			t.Fatalf("unsafe guarded target %q accepted", target)
		}
	}
	for _, pid := range []int{0, -1} {
		for _, err := range []error{
			client.RespawnPaneIfCurrentContext(t.Context(), "%7", pid, true, "", false),
			client.SetPaneLaunchSpecIfCurrentContext(t.Context(), "%7", pid, spec),
			client.SetPaneAgentTypeIfCurrentContext(t.Context(), "%7", pid, AgentCodex),
			client.SendKeysForAgentIfCurrentContext(t.Context(), "%7", "true", true, AgentCodex, pid, true),
		} {
			if err == nil || !strings.Contains(err.Error(), "positive expected PID") {
				t.Fatalf("invalid expected PID reached tmux: %v", err)
			}
		}
	}
	for _, directory := range []string{"relative", "/private\npath", "/private\tpath"} {
		if err := client.RespawnPaneIfCurrentContext(t.Context(), "%7", 1, true, directory, false); err == nil || strings.Contains(err.Error(), directory) {
			t.Fatalf("unsafe working directory did not fail before mutation: %v", err)
		}
	}
	oversized := strings.Repeat("private-value", MaxTypedLaunchCommandBytes)
	if err := client.SendKeysForAgentIfCurrentContext(t.Context(), "%7", oversized, true, AgentCodex, 1, true); err == nil || !strings.Contains(err.Error(), "terminal input limit") || strings.Contains(err.Error(), "private-value") {
		t.Fatalf("oversized launch did not fail before contacting tmux: %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := client.PreflightRestartShellContext(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled shell preflight returned %v", err)
	}
}

func TestRealGuardedPaneMutationsRefuseChangedIdentityAndOwners(t *testing.T) {
	session := createTestSession(t)
	pane := waitGuardedTestPane(t, session, func(p Pane) bool { return p.PID > 0 && p.IdleShell() })
	spec := AgentLaunchSpec{Version: AgentLaunchSpecVersion, AgentType: AgentCodex, Command: "codex --model retained-model"}
	marker := filepath.Join(t.TempDir(), "must-not-launch")
	launch := "printf rejected > " + ShellQuote(marker)
	assertRefused := func(pid int) {
		t.Helper()
		operations := []struct {
			name string
			run  func() error
		}{
			{"respawn", func() error {
				return DefaultClient.RespawnPaneIfCurrentContext(t.Context(), pane.ID, pid, true, "", true)
			}},
			{"launch metadata", func() error { return DefaultClient.SetPaneLaunchSpecIfCurrentContext(t.Context(), pane.ID, pid, spec) }},
			{"agent type", func() error {
				return DefaultClient.SetPaneAgentTypeIfCurrentContext(t.Context(), pane.ID, pid, AgentCodex)
			}},
			{"literal launch", func() error {
				return DefaultClient.SendKeysForAgentIfCurrentContext(t.Context(), pane.ID, launch, true, AgentCodex, pid, true)
			}},
			{"buffered launch", func() error {
				return DefaultClient.SendKeysForAgentIfCurrentContext(t.Context(), pane.ID, launch, true, AgentClaude, pid, true)
			}},
		}
		for _, operation := range operations {
			if err := operation.run(); !errors.Is(err, ErrPaneIdentityChanged) {
				t.Fatalf("%s did not refuse the changed/claimed pane: %v", operation.name, err)
			}
		}
		current := waitGuardedTestPane(t, session, func(p Pane) bool { return p.PID > 0 })
		if current.PID != pane.PID {
			t.Fatal("a refused operation replaced the pane process")
		}
		if stored, err := ReadPaneLaunchSpecContext(t.Context(), pane.ID); err != nil || stored != nil {
			t.Fatalf("a refused operation published launch metadata: stored=%v error=%v", stored != nil, err)
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("a refused operation delivered a launch command")
		}
	}
	assertRefused(pane.PID + 1000000)
	for _, option := range []string{PaneOptionRotationToken, PaneOptionNTMService, PaneOptionACFSService} {
		if err := DefaultClient.RunSilentContext(t.Context(), "set-option", "-p", "-t", pane.ID, option, "claimed"); err != nil {
			t.Fatal(err)
		}
		assertRefused(pane.PID)
		if err := DefaultClient.RunSilentContext(t.Context(), "set-option", "-p", "-u", "-t", pane.ID, option); err != nil {
			t.Fatal(err)
		}
	}
	if err := DefaultClient.SetPaneLaunchSpecIfCurrentContext(t.Context(), pane.ID, pane.PID, spec); err != nil {
		t.Fatal(err)
	}
	if err := DefaultClient.SetPaneAgentTypeIfCurrentContext(t.Context(), pane.ID, pane.PID, AgentCodex); err != nil {
		t.Fatal(err)
	}
	stored, err := ReadPaneLaunchSpecContext(t.Context(), pane.ID)
	if err != nil || stored == nil || stored.Command != spec.Command {
		t.Fatalf("matching-pane publication did not persist the saved launch: %v", err)
	}
	// Explicit adoption is authoritative even when a prior supervisor's
	// service tag remains. Preserve the same precedence as GetPanes.
	if err := DefaultClient.RunSilentContext(t.Context(), "set-option", "-p", "-t", pane.ID, PaneOptionACFSService, "retained-service"); err != nil {
		t.Fatal(err)
	}
	adopted := waitGuardedTestPane(t, session, func(p Pane) bool { return p.Type == AgentCodex })
	if adopted.IsServicePane() {
		t.Fatal("fixture is not an explicitly adopted agent")
	}
	if err := DefaultClient.SetPaneLaunchSpecIfCurrentContext(t.Context(), pane.ID, pane.PID, spec); err != nil {
		t.Fatalf("guard rejected explicit adoption over a retained service tag: %v", err)
	}
}

func TestRealGuardedRespawnUsesFreshShellOnlyForAgentRecovery(t *testing.T) {
	session := createTestSession(t)
	pane := waitGuardedTestPane(t, session, func(p Pane) bool { return p.PID > 0 && p.IdleShell() })
	// A pane restored with an explicit provider command retains that argv.
	// sleep models a direct, non-shell provider without requiring a live CLI.
	if err := DefaultClient.RunSilentContext(t.Context(), "respawn-pane", "-k", "-t", pane.ID, "/bin/sleep", "30"); err != nil {
		t.Fatal(err)
	}
	direct := waitGuardedTestPane(t, session, func(p Pane) bool { return p.Command == "sleep" })
	if err := DefaultClient.RespawnPaneIfCurrentContext(t.Context(), direct.ID, direct.PID, true, "", false); err != nil {
		t.Fatal(err)
	}
	retained := waitGuardedTestPane(t, session, func(p Pane) bool { return p.Command == "sleep" && p.PID != direct.PID })
	directory := filepath.Join(t.TempDir(), "recovery directory's worktree")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := DefaultClient.PreflightRestartShellContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := DefaultClient.RespawnPaneIfCurrentContext(t.Context(), retained.ID, retained.PID, true, directory, true); err != nil {
		t.Fatal(err)
	}
	fresh := waitGuardedTestPane(t, session, func(p Pane) bool { return p.PID != retained.PID && p.IdleShell() })
	path, err := DefaultClient.RunContext(t.Context(), "display-message", "-p", "-t", fresh.ID, "#{pane_current_path}")
	if err != nil || path != directory {
		t.Fatalf("fresh shell lost the observed worktree: %q, %v", path, err)
	}
	for _, tc := range []struct {
		name string
		kind AgentType
		want string
	}{
		{"literal", AgentGemini, "literal ';#{}$HOME\\"},
		{"buffered", AgentClaude, "buffered ';#{}$HOME\\"},
		{"long private handoff", AgentGemini, strings.Repeat("λ", 2048) + ";end'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := filepath.Join(t.TempDir(), "result")
			// The final semicolon must be data for the tmux parser and remain a
			// shell terminator; conditional command quoting must not add '\'.
			command := "printf '%s' " + ShellQuote(tc.want) + " > " + ShellQuote(result) + ";"
			if len(command) > MaxTypedLaunchCommandBytes {
				// Reproduce the >4KiB dash line limit through the same private
				// handoff used by restart preflight, with no environment values.
				if err := DefaultClient.SendKeysForAgentIfCurrentContext(t.Context(), fresh.ID, command, true, tc.kind, fresh.PID, true); err == nil {
					t.Fatal("oversized launch was allowed onto the shell's terminal input")
				}
				handoff, err := NewLaunchEnvironment(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = handoff.Close() })
				if err := handoff.SendIfCurrent(t.Context(), fresh.ID, command, true, tc.kind, fresh.PID); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := DefaultClient.SendKeysForAgentIfCurrentContext(t.Context(), fresh.ID, command, true, tc.kind, fresh.PID, true); err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				data, err := os.ReadFile(result)
				if err == nil && string(data) == tc.want {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("guarded %s changed the delivered command or did not execute: %v", tc.name, err)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestRealGuardedSendRechecksPIDBeforeEnter(t *testing.T) {
	for _, tc := range []struct {
		name        string
		doubleEnter bool
		exitToShell bool
	}{
		{"launch process replaced", false, false},
		{"prompt process replaced", true, false},
		{"provider exits to same shell", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := createTestSession(t)
			pane := waitGuardedTestPane(t, session, func(p Pane) bool { return p.PID > 0 && p.IdleShell() })
			if tc.doubleEnter {
				// cat stands in for an agent that owns the terminal and accepts
				// a staged prompt, while pane_pid remains the parent shell.
				if err := DefaultClient.SendKeysContext(t.Context(), pane.ID, "/bin/cat", true); err != nil {
					t.Fatal(err)
				}
				pane = waitGuardedTestPane(t, session, func(p Pane) bool { return p.Command == "cat" })
			}
			marker := filepath.Join(t.TempDir(), "must-not-run")
			command := "printf ntm_staged_guard_marker > " + ShellQuote(marker)
			result := make(chan error, 1)
			go func() {
				if tc.doubleEnter {
					result <- DefaultClient.SendKeysForAgentDoubleEnterIfCurrentContext(t.Context(), pane.ID, command, AgentCodex, pane.PID)
					return
				}
				result <- DefaultClient.sendKeysForAgentIfCurrentContext(t.Context(), pane.ID, command, true, time.Second, AgentCodex, pane.PID, true)
			}()
			deadline := time.Now().Add(800 * time.Millisecond)
			for {
				capture, err := DefaultClient.CapturePaneOutputContext(t.Context(), pane.ID, 20)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(capture, "ntm_staged_guard_marker") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("guarded command was not staged before its Enter delay")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if tc.exitToShell {
				if err := DefaultClient.RunSilentContext(t.Context(), "send-keys", "-t", pane.ID, "C-c"); err != nil {
					t.Fatal(err)
				}
				idle := waitGuardedTestPane(t, session, func(p Pane) bool { return p.IdleShell() })
				if idle.PID != pane.PID {
					t.Fatal("test did not preserve the parent shell PID")
				}
			} else {
				if err := DefaultClient.RespawnPaneIfCurrentContext(t.Context(), pane.ID, pane.PID, true, "", true); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-result:
				if !errors.Is(err, ErrPaneIdentityChanged) {
					t.Fatalf("Enter did not refuse the replacement process: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("guarded send did not return after the pane changed")
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("a stale Enter executed the staged launch")
			}
		})
	}
}

func TestPaneMutationGuardRejectsUnboundTargetsAndCancellation(t *testing.T) {
	guarded, err := WithPaneMutationGuard(t.Context(), "%7", 123)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient("")
	for _, args := range [][]string{
		{"send-keys", "Enter"},
		{"send-keys", "-t", "%8", "Enter"},
		{"paste-buffer", "-b", "private", "-t", "%8"},
		{"send-keys", "-t", "%7", "-t", "%8", "Enter"},
	} {
		if err := client.RunSilentContext(guarded, args...); err == nil || !strings.Contains(err.Error(), "guarded pane input") {
			t.Fatalf("unbound mutation reached tmux: %v", err)
		}
	}
	if _, err := WithPaneMutationGuard(guarded, "%8", 123); err == nil {
		t.Fatal("a nested helper replaced the guarded pane binding")
	}
	canceled, cancel := context.WithCancel(guarded)
	cancel()
	if err := client.RunSilentContext(canceled, "send-keys", "-t", "%7", "Enter"); !errors.Is(err, context.Canceled) {
		t.Fatalf("guard swallowed caller cancellation: %v", err)
	}
}

func TestRealPaneMutationGuardProtectsSharedSendAndRescueKeys(t *testing.T) {
	session := createTestSession(t)
	pane := waitGuardedTestPane(t, session, func(p Pane) bool { return p.PID > 0 && p.IdleShell() })
	if err := DefaultClient.SendKeysContext(t.Context(), pane.ID, "/bin/cat", true); err != nil {
		t.Fatal(err)
	}
	pane = waitGuardedTestPane(t, session, func(p Pane) bool { return p.Command == "cat" })
	guarded, err := WithPaneMutationGuard(t.Context(), pane.ID, pane.PID)
	if err != nil {
		t.Fatal(err)
	}
	// The normal send API still works through the scoped guard while the
	// provider owns the pane; its conditional quoting must preserve ';'.
	if err := DefaultClient.SendKeysForAgentContext(guarded, pane.ID, "ntm_context_guard;", false, AgentGemini); err != nil {
		t.Fatal(err)
	}
	if err := DefaultClient.RunSilentContext(t.Context(), "send-keys", "-t", pane.ID, "C-c"); err != nil {
		t.Fatal(err)
	}
	idle := waitGuardedTestPane(t, session, func(p Pane) bool { return p.IdleShell() })
	if idle.PID != pane.PID {
		t.Fatal("provider exit did not preserve the shell identity")
	}
	marker := filepath.Join(t.TempDir(), "unguarded-still-works")
	command := "printf safe > " + ShellQuote(marker)
	for _, attempt := range []func() error{
		func() error {
			return DefaultClient.SendKeysForAgentContext(guarded, pane.ID, command, true, AgentCodex)
		},
		func() error { return DefaultClient.SendKeyNameContext(guarded, pane.ID, "Enter") },
		func() error { return DefaultClient.RunSilentContext(guarded, "send-keys", "-t", pane.ID, "Escape") },
		func() error {
			return DefaultClient.RunSilentContext(guarded, "paste-buffer", "-b", "not-loaded", "-t", pane.ID)
		},
	} {
		if err := attempt(); !errors.Is(err, ErrPaneIdentityChanged) {
			t.Fatalf("shared protocol input escaped the pane guard: %v", err)
		}
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("guarded prompt executed in the bare shell")
	}
	// The scope is explicit: ordinary shell callers retain their behavior.
	if err := DefaultClient.SendKeysContext(t.Context(), pane.ID, command, true); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if data, err := os.ReadFile(marker); err == nil && string(data) == "safe" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("an opt-in guard changed ordinary shell delivery")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRealLaunchEnvironmentGuardRefusesChangedPane(t *testing.T) {
	session := createTestSession(t)
	pane := waitGuardedTestPane(t, session, func(p Pane) bool { return p.PID > 0 && p.IdleShell() })
	launch, err := NewLaunchEnvironment(t.Context(), map[string]string{"API_TOKEN": "private-never-delivered"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = launch.Close() })
	if err := launch.SendIfCurrent(t.Context(), pane.ID, "true", true, AgentCodex, pane.PID+1000000); !errors.Is(err, ErrPaneIdentityChanged) {
		t.Fatalf("FIFO launcher did not preserve identity refusal: %v", err)
	}
	if _, err := os.Stat(launch.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused FIFO launch left its private handoff behind")
	}
	capture, err := DefaultClient.CapturePaneOutputContext(t.Context(), pane.ID, 20)
	if err != nil || strings.Contains(capture, launch.pipe) || strings.Contains(capture, "private-never-delivered") {
		t.Fatalf("refused FIFO launcher reached the pane: %v", err)
	}
}

func TestRealPaneRotationQuarantineOwnershipAndTopology(t *testing.T) {
	skipIfNoTmux(t)
	session := createTestSession(t)
	panes, err := GetPanes(session)
	if err != nil || len(panes) != 1 {
		t.Fatalf("initial panes=%+v err=%v", panes, err)
	}
	id := panes[0].ID
	if err := SetPaneRotationTokenContext(t.Context(), id, "rotation-owner"); err != nil {
		t.Fatal(err)
	}
	if err := SetPaneRotationTokenContext(t.Context(), id, "rotation-owner"); err != nil {
		t.Fatalf("idempotent claim: %v", err)
	}
	if err := SetPaneRotationTokenContext(t.Context(), id, "foreign-owner"); err == nil {
		t.Fatal("a competing rotation overwrote the quarantine")
	}
	if err := ClearPaneRotationTokenContext(t.Context(), id, "foreign-owner"); err == nil {
		t.Fatal("a competing rotation cleared the quarantine")
	}
	if err := SetPaneAgentIdentityContext(t.Context(), id, "custom title", AgentCodex); err != nil {
		t.Fatal(err)
	}
	check := func(panes []Pane) {
		t.Helper()
		if len(panes) != 1 || panes[0].ID != id || panes[0].RotationToken != "rotation-owner" || panes[0].Type != AgentCodex {
			t.Fatalf("topology lost the independent quarantine: %+v", panes)
		}
	}
	panes, err = GetPanesContext(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	check(panes)
	all, err := GetAllPanesContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	check(all[session])
	activity, err := GetPanesWithActivityContext(t.Context(), session)
	if err != nil || len(activity) != 1 {
		t.Fatalf("activity=%+v err=%v", activity, err)
	}
	check([]Pane{activity[0].Pane})
	if err := ClearPaneRotationTokenContext(t.Context(), id, "rotation-owner"); err != nil {
		t.Fatal(err)
	}
	if err := ClearPaneRotationTokenContext(t.Context(), id, "rotation-owner"); err != nil {
		t.Fatalf("idempotent release: %v", err)
	}
	if got, err := ReadPaneRotationTokenContext(t.Context(), id); err != nil || got != "" {
		t.Fatalf("released token=%q err=%v", got, err)
	}
	// Two independent clients race for one pane. The tmux-side conditional
	// must admit exactly one execution token, not whichever last wrote it.
	var wg sync.WaitGroup
	results := make(chan string, 2)
	start := make(chan struct{})
	for _, token := range []string{"candidate-a", "candidate-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := NewClient("").SetPaneRotationTokenContext(t.Context(), id, token); err == nil {
				results <- token
			}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var winners []string
	for winner := range results {
		winners = append(winners, winner)
	}
	got, err := ReadPaneRotationTokenContext(t.Context(), id)
	if err != nil || len(winners) != 1 || got != winners[0] {
		t.Fatalf("competing claims: winners=%v actual=%q err=%v", winners, got, err)
	}
	if err := ClearPaneRotationTokenContext(t.Context(), id, winners[0]); err != nil {
		t.Fatal(err)
	}
	// A whitespace-only hand edit cannot be mistaken for an absent fence.
	if err := DefaultClient.RunSilentContext(t.Context(), "set-option", "-p", "-t", id, PaneOptionRotationToken, " "); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPaneRotationTokenContext(t.Context(), id); err == nil {
		t.Fatal("malformed nonempty quarantine was read as absent")
	}
	if _, err := GetPanesContext(t.Context(), session); err == nil {
		t.Fatal("topology accepted malformed nonempty quarantine")
	}
}

// The @ntm_agent_type pane option recorded by adopt must outrank the pane
// title: OpenCode rewrites its own title continuously, so a title-only
// registration is undone within seconds (ntm#266).

func paneLine(id, index, title, command, pid, window, recordedType string) string {
	sep := FieldSeparator
	return strings.Join([]string{id, index, title, command, "80", "24", "1", pid, window, recordedType}, sep)
}

func TestParsePaneLine_RecordedAgentTypeOutranksRewrittenTitle(t *testing.T) {
	t.Parallel()

	// OpenCode has already replaced the "sess__oc_1" title adopt set. The
	// current command is a shell wrapper so the command heuristic cannot
	// rescue it either; only the recorded option can.
	pane, err := parsePaneLine(paneLine("%7", "2", "opencode | dev", "bash", "0", "1", "oc"), FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine: %v", err)
	}
	if pane.Type != AgentOpencode {
		t.Fatalf("Type = %q, want %q (recorded pane option)", pane.Type, AgentOpencode)
	}
}

func TestParsePaneLine_RecordedAgentTypeWinsOverNTMTitle(t *testing.T) {
	t.Parallel()

	// The option is what ntm itself recorded most recently; a stale NTM
	// title (for example from an earlier adopt as a different type) must not
	// override it, but index/variant/tags still come from the title.
	pane, err := parsePaneLine(paneLine("%3", "0", "sess__cc_4_opus[api]", "zsh", "0", "0", "codex"), FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine: %v", err)
	}
	if pane.Type != AgentCodex {
		t.Fatalf("Type = %q, want %q", pane.Type, AgentCodex)
	}
	if pane.NTMIndex != 4 || pane.Variant != "opus" || len(pane.Tags) != 1 || pane.Tags[0] != "api" {
		t.Fatalf("title metadata lost: index=%d variant=%q tags=%v", pane.NTMIndex, pane.Variant, pane.Tags)
	}
}

func TestParsePaneLine_UnknownRecordedTypeFallsBackToHeuristics(t *testing.T) {
	t.Parallel()

	// A hand-edited or stale option naming a type ntm does not know must
	// not be trusted; the normal title/command chain applies instead.
	pane, err := parsePaneLine(paneLine("%3", "0", "sess__cc_1", "claude", "0", "0", "not-an-agent"), FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine: %v", err)
	}
	if pane.Type != AgentClaude {
		t.Fatalf("Type = %q, want %q", pane.Type, AgentClaude)
	}
}

func TestParsePaneLine_AbsentOptionKeepsLegacyBehaviour(t *testing.T) {
	t.Parallel()

	// tmux servers without pane options expand #{@ntm_agent_type} to "".
	pane, err := parsePaneLine(paneLine("%1", "0", "sess__gmi_2", "gemini", "0", "0", ""), FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine: %v", err)
	}
	if pane.Type != AgentGemini || pane.NTMIndex != 2 {
		t.Fatalf("pane = %+v, want gemini index 2", pane)
	}

	// Older 9-field callers of parsePaneFromParts remain valid.
	legacy, err := parsePaneFromParts(
		[]string{"%1", "0", "sess__cod_1", "codex", "80", "24", "1"},
		[]string{"0", "0"},
	)
	if err != nil {
		t.Fatalf("parsePaneFromParts legacy shape: %v", err)
	}
	if legacy.Type != AgentCodex {
		t.Fatalf("legacy Type = %q, want %q", legacy.Type, AgentCodex)
	}
}

func TestParsePaneLine_OpenCodeSelfTitleAndCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		title   string
		command string
		want    AgentType
	}{
		{"opencode binary as current command", "some shell prompt", "opencode", AgentOpencode},
		{"opencode binary by path", "whatever", "/usr/local/bin/opencode", AgentOpencode},
		{"opencode self-set title under a wrapper", "opencode | dev", "bash", AgentOpencode},
		{"opencode self-set title, case-insensitive", "OpenCode | /work/repo", "sh", AgentOpencode},
		{"opencode self-set title without directory", "opencode", "zsh", AgentOpencode},
		{"project merely named opencode is not an agent", "my-opencode-notes | dev", "zsh", AgentUser},
		{"plain shell pane", "user | sess | dev", "zsh", AgentUser},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pane, err := parsePaneLine(paneLine("%9", "1", tc.title, tc.command, "0", "0", ""), FieldSeparator)
			if err != nil {
				t.Fatalf("parsePaneLine: %v", err)
			}
			if pane.Type != tc.want {
				t.Fatalf("Type = %q, want %q", pane.Type, tc.want)
			}
		})
	}
}

func TestParsePaneAgentTypeOption(t *testing.T) {
	t.Parallel()

	cases := map[string]AgentType{
		"":          AgentUnknown,
		"   ":       AgentUnknown,
		"oc":        AgentOpencode,
		"opencode":  AgentOpencode,
		" cc ":      AgentClaude,
		"codex":     AgentCodex,
		"user":      AgentUser,
		"unknown":   AgentUnknown,
		"garbage-x": AgentUnknown,
	}
	for raw, want := range cases {
		if got := ParsePaneAgentTypeOption(raw); got != want {
			t.Errorf("ParsePaneAgentTypeOption(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestSetPaneAgentTypeRejectsUnknownType(t *testing.T) {
	t.Parallel()

	c := &Client{}
	if err := c.SetPaneAgentType("%1", AgentType("nope")); err == nil {
		t.Fatal("SetPaneAgentType accepted an unknown agent type")
	}
	if err := c.SetPaneAgentType("", AgentOpencode); err == nil {
		t.Fatal("SetPaneAgentType accepted an empty pane ID")
	}
	if err := c.SetPaneAgentIdentityContext(t.Context(), "%1", "proj__bad_1", AgentType("nope")); err == nil {
		t.Fatal("SetPaneAgentIdentityContext accepted an unknown agent type")
	}
}

// GH#268: lifecycle code knows the provider before launch. Persisting that
// knowledge must survive both a wrapper becoming pane_current_command and a
// TUI replacing the visible title.
func TestRealSetPaneAgentIdentitySurvivesWrapperAndTitleRewrite(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)
	panes, err := GetPanes(session)
	if err != nil || len(panes) == 0 {
		t.Fatalf("GetPanes: panes=%d err=%v", len(panes), err)
	}
	paneID := panes[0].ID

	for _, want := range []AgentType{AgentClaude, AgentCodex, AgentGrok} {
		title := FormatPaneName(session, string(want), 1, "")
		if err := SetPaneAgentIdentityContext(t.Context(), paneID, title, want); err != nil {
			t.Fatalf("SetPaneAgentIdentityContext(%s): %v", want, err)
		}
		if err := DefaultClient.RunSilent("select-pane", "-t", ExactTarget(paneID), "-T", "wrapper | changed by TUI"); err != nil {
			t.Fatalf("rewrite title for %s: %v", want, err)
		}

		got, err := GetPanes(session)
		if err != nil {
			t.Fatalf("GetPanes after %s title rewrite: %v", want, err)
		}
		if len(got) != 1 || got[0].Type != want {
			t.Fatalf("panes after %s title rewrite = %+v, want durable type %s", want, got, want)
		}
	}
}

// TestRealSetPaneAgentTypeSurvivesTitleRewrite drives a real tmux server: the
// recorded type must be reported by GetPanes even after the pane title is
// rewritten to OpenCode's own shape, which is exactly the ntm#266 scenario.
func TestRealSetPaneAgentTypeSurvivesTitleRewrite(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)
	panes, err := GetPanes(session)
	if err != nil || len(panes) == 0 {
		t.Fatalf("GetPanes: panes=%d err=%v", len(panes), err)
	}
	paneID := panes[0].ID

	if err := SetPaneAgentType(paneID, AgentOpencode); err != nil {
		t.Skipf("pane options unsupported by this tmux: %v", err)
	}
	// Simulate OpenCode overwriting the title after adopt renamed it.
	if err := DefaultClient.RunSilent("select-pane", "-t", ExactTarget(paneID), "-T", "opencode | dev"); err != nil {
		t.Fatalf("rewrite title: %v", err)
	}

	for _, lookup := range []struct {
		name string
		get  func() ([]Pane, error)
	}{
		{"GetPanes", func() ([]Pane, error) { return GetPanes(session) }},
		{"GetAllPanes", func() ([]Pane, error) {
			all, err := GetAllPanes()
			if err != nil {
				return nil, err
			}
			return all[session], nil
		}},
		{"GetPanesWithActivity", func() ([]Pane, error) {
			acts, err := GetPanesWithActivity(session)
			if err != nil {
				return nil, err
			}
			out := make([]Pane, 0, len(acts))
			for _, a := range acts {
				out = append(out, a.Pane)
			}
			return out, nil
		}},
	} {
		got, err := lookup.get()
		if err != nil {
			t.Fatalf("%s: %v", lookup.name, err)
		}
		found := false
		for _, p := range got {
			if p.ID != paneID {
				continue
			}
			found = true
			if p.Type != AgentOpencode {
				t.Fatalf("%s: Type = %q after title rewrite, want %q", lookup.name, p.Type, AgentOpencode)
			}
		}
		if !found {
			t.Fatalf("%s: pane %s missing from listing", lookup.name, paneID)
		}
	}
}

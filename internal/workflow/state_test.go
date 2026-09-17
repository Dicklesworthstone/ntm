package workflow

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the same store used by --robot-sequence from independent processes.
// Goroutine-only tests cannot detect a process-local mutex protecting disk state.
func TestPaneSequenceSubprocess(t *testing.T) {
	mode := os.Getenv("NTM_SEQUENCE_TEST_MODE")
	if mode == "" {
		t.Skip("subprocess helper")
	}
	store, err := NewPaneSequenceStore(os.Getenv("NTM_SEQUENCE_TEST_PROJECT"))
	if err != nil {
		t.Fatal(err)
	}
	gate := os.Getenv("NTM_SEQUENCE_TEST_GATE")
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(gate); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("start gate timed out")
		}
		time.Sleep(time.Millisecond)
	}
	// Enlarge the read-modify-write window without mocking persistence or locks.
	store.Now = func() time.Time {
		time.Sleep(2 * time.Millisecond)
		return time.Now()
	}
	switch mode {
	case "hold":
		path, err := store.path("shared")
		if err != nil {
			t.Fatal(err)
		}
		unlock, err := store.lockSequence(path)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		fmt.Println("LOCKED")
		var input [1]byte
		_, _ = os.Stdin.Read(input[:])
	case "create":
		store.Now = func() time.Time {
			time.Sleep(40 * time.Millisecond)
			return time.Now()
		}
		_, err := store.Create("shared", []string{os.Getenv("NTM_SEQUENCE_TEST_PANE")})
		if err == nil {
			fmt.Println("CREATED")
		} else if !strings.Contains(err.Error(), "already exists") {
			t.Fatal(err)
		}
	case "advance":
		for i := 0; i < 20; i++ {
			if _, err := store.Advance("shared", os.Getenv("NTM_SEQUENCE_TEST_PANE")); err != nil {
				t.Fatal(err)
			}
		}
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func runPaneSequenceProcesses(t *testing.T, projectDir, mode string, samePane bool) int {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	gate := filepath.Join(t.TempDir(), "start")
	const workers = 8
	commands := make([]*exec.Cmd, workers)
	outputs := make([]bytes.Buffer, workers)
	for i := range commands {
		pane := fmt.Sprintf("%%%d", i)
		if samePane {
			pane = "%0"
		}
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestPaneSequenceSubprocess$")
		cmd.Env = append(os.Environ(),
			"NTM_SEQUENCE_TEST_MODE="+mode,
			"NTM_SEQUENCE_TEST_PROJECT="+projectDir,
			"NTM_SEQUENCE_TEST_PANE="+pane,
			"NTM_SEQUENCE_TEST_GATE="+gate,
		)
		cmd.Stdout = &outputs[i]
		cmd.Stderr = &outputs[i]
		if err := cmd.Start(); err != nil {
			cancel()
			for _, started := range commands[:i] {
				_ = started.Wait()
			}
			t.Fatal(err)
		}
		commands[i] = cmd
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		cancel()
		for _, cmd := range commands {
			_ = cmd.Wait()
		}
		t.Fatal(err)
	}
	created := 0
	for i, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Errorf("worker %d: %v\n%s", i, err, outputs[i].String())
		}
		if strings.Contains(outputs[i].String(), "CREATED") {
			created++
		}
	}
	return created
}

func TestPaneSequenceConcurrentProcessesPreserveProgress(t *testing.T) {
	for _, samePane := range []bool{false, true} {
		t.Run(fmt.Sprintf("same_pane_%t", samePane), func(t *testing.T) {
			store, err := NewPaneSequenceStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			steps := make([]string, 160)
			for i := range steps {
				steps[i] = fmt.Sprintf("review step %d", i)
			}
			if _, err := store.Create("shared", steps); err != nil {
				t.Fatal(err)
			}
			runPaneSequenceProcesses(t, store.ProjectDir, "advance", samePane)
			sequence, err := store.Load("shared")
			if err != nil {
				t.Fatal(err)
			}
			if samePane {
				if sequence.Positions["%0"] != 160 {
					t.Fatalf("lost concurrent advances: got %d, want 160", sequence.Positions["%0"])
				}
				return
			}
			for i := 0; i < 8; i++ {
				pane := fmt.Sprintf("%%%d", i)
				if sequence.Positions[pane] != 20 {
					t.Errorf("lost progress for %s: got %d, want 20", pane, sequence.Positions[pane])
				}
			}
		})
	}
}

func TestPaneSequenceConcurrentCreateHasOneWinner(t *testing.T) {
	if created := runPaneSequenceProcesses(t, t.TempDir(), "create", false); created != 1 {
		t.Fatalf("concurrent create succeeded %d times, want exactly one", created)
	}
}

func TestPaneSequenceLockReleasedAfterProcessExit(t *testing.T) {
	projectDir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(projectDir, "start")
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestPaneSequenceSubprocess$")
	cmd.Env = append(os.Environ(), "NTM_SEQUENCE_TEST_MODE=hold",
		"NTM_SEQUENCE_TEST_PROJECT="+projectDir, "NTM_SEQUENCE_TEST_GATE="+gate)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, readErr := bufio.NewReader(stdout).ReadString('\n')
	// Kill rather than closing stdin: normal return would run defer unlock().
	killErr := cmd.Process.Kill()
	_ = cmd.Wait()
	if readErr != nil || strings.TrimSpace(line) != "LOCKED" || killErr != nil {
		t.Fatalf("helper lock: line=%q read=%v kill=%v", line, readErr, killErr)
	}
	lockPath := filepath.Join(projectDir, ".ntm", "workflows", "sequences", "shared.json.lock")
	retryCtx, retryCancel := context.WithTimeout(context.Background(), time.Second)
	defer retryCancel()
	unlock, err := lockPaneSequenceFile(retryCtx, lockPath)
	if err != nil {
		t.Fatalf("crashed process left lock held: %v", err)
	}
	unlock()
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("stable lock sidecar was removed: %v", err)
	}
}

func TestPaneSequenceLocksAreBoundedAndIndependent(t *testing.T) {
	store, err := NewPaneSequenceStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"busy", "other"} {
		if _, err := store.Create(name, []string{"inspect"}); err != nil {
			t.Fatal(err)
		}
	}
	path, err := store.path("busy")
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := store.lockSequence(path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if release, err := lockPaneSequenceFile(ctx, path+".lock"); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		t.Fatalf("contended lock error = %v, want deadline exceeded", err)
	}
	if _, err := store.Next("busy", "%1"); err != nil {
		t.Fatalf("read blocked by writer lock: %v", err)
	}
	if result, err := store.Advance("other", "%1"); err != nil || !result.Complete {
		t.Fatalf("unrelated sequence blocked: result=%+v error=%v", result, err)
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if release, err := lockPaneSequenceFile(canceled, path+".unused.lock"); !errors.Is(err, context.Canceled) {
		if release != nil {
			release()
		}
		t.Fatalf("canceled lock error = %v", err)
	}
}

func TestPaneSequenceRejectsMismatchedPersistedName(t *testing.T) {
	store, err := NewPaneSequenceStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("other", []string{"protected prompt"}); err != nil {
		t.Fatal(err)
	}
	path, err := store.path("review")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"name":"other","steps":["wrong prompt"],"positions":{}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Advance("review", "%1"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched name accepted: %v", err)
	}
	other, err := store.Next("other", "%1")
	if err != nil || other.Position != 0 || other.Prompt != "protected prompt" {
		t.Fatalf("another sequence was overwritten: %+v, %v", other, err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("invalid source document changed: %q, %v", got, err)
	}
	sequence, err := store.Create(" canonical ", []string{"  preserve prompt whitespace  "})
	if err != nil || sequence.Name != "canonical" {
		t.Fatalf("canonical sequence = %+v, %v", sequence, err)
	}
	position, err := store.Next(" canonical ", "%1")
	if err != nil || position.Sequence != "canonical" || position.Prompt != "  preserve prompt whitespace  " {
		t.Fatalf("canonical read = %+v, %v", position, err)
	}
}

func TestStateStoreSaveLoadAndTransitions(t *testing.T) {
	store := &StateStore{Dir: t.TempDir()}
	started := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	state := &WorkflowState{WorkflowName: "review", SessionName: "session", CurrentStage: "review", StageStartedAt: started, Agents: map[string]string{"reviewer": "r1"}, Variables: map[string]string{"branch": "main"}}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("session")
	if err != nil || loaded == nil {
		t.Fatalf("Load() = (%+v, %v)", loaded, err)
	}
	if loaded.Variables["branch"] != "main" || loaded.Agents["reviewer"] != "r1" {
		t.Fatalf("loaded = %+v", loaded)
	}
	pauseAt := started.Add(time.Minute)
	if err := store.Pause(loaded, "operator", pauseAt); err != nil {
		t.Fatal(err)
	}
	if !loaded.Paused || loaded.PausedAt == nil || loaded.PauseReason != "operator" {
		t.Fatalf("paused state = %+v", loaded)
	}
	if err := store.Resume(loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Paused || loaded.PausedAt != nil || loaded.PauseReason != "" {
		t.Fatalf("resumed state = %+v", loaded)
	}
	if err := store.RecordStage(loaded, "completed", "manual", started.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(loaded.StageHistory) != 1 || loaded.StageHistory[0].DurationSec != 300 {
		t.Fatalf("history = %+v", loaded.StageHistory)
	}
}

func TestStateStoreRejectsInvalidOrMissingState(t *testing.T) {
	store := &StateStore{Dir: t.TempDir()}
	if state, err := store.Load("missing"); err != nil || state != nil {
		t.Fatalf("missing Load() = (%+v, %v)", state, err)
	}
	if err := store.Save(&WorkflowState{SessionName: "../escape"}); err == nil {
		t.Fatal("Save accepted traversal session")
	}
	if err := store.Resume(&WorkflowState{}); err == nil {
		t.Fatal("Resume accepted an active workflow")
	}
	if err := store.Save(nil); err == nil {
		t.Fatal("Save accepted nil state")
	}
	if _, err := filepath.Abs(store.Dir); err != nil {
		t.Fatal(err)
	}
}

func TestPaneSequenceStoreAdvancesEachPaneIndependentlyAndSurvivesReload(t *testing.T) {
	projectDir := t.TempDir()
	store, err := NewPaneSequenceStore(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return clock }

	sequence, err := store.Create("review", []string{"inspect", "challenge", "summarize"})
	if err != nil {
		t.Fatal(err)
	}
	if sequence.CreatedAt != clock || sequence.UpdatedAt != clock {
		t.Fatalf("timestamps = (%s, %s), want %s", sequence.CreatedAt, sequence.UpdatedAt, clock)
	}

	first, err := store.Next("review", "%12")
	if err != nil {
		t.Fatal(err)
	}
	if first.Position != 0 || first.Prompt != "inspect" || first.Complete || first.Advanced {
		t.Fatalf("initial pane state = %+v", first)
	}
	advanced, err := store.Advance("review", "%12")
	if err != nil {
		t.Fatal(err)
	}
	if advanced.Position != 1 || advanced.Prompt != "challenge" || !advanced.Advanced || advanced.Complete {
		t.Fatalf("advanced pane state = %+v", advanced)
	}

	otherPane, err := store.Next("review", "%13")
	if err != nil {
		t.Fatal(err)
	}
	if otherPane.Position != 0 || otherPane.Prompt != "inspect" {
		t.Fatalf("second pane inherited progress: %+v", otherPane)
	}

	reloaded, err := NewPaneSequenceStore(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	afterReload, err := reloaded.Next("review", "%12")
	if err != nil {
		t.Fatal(err)
	}
	if afterReload.Position != 1 || afterReload.Prompt != "challenge" {
		t.Fatalf("reloaded pane state = %+v", afterReload)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".ntm", "workflows", "sequences", "review.json")); err != nil {
		t.Fatalf("durable sequence state missing: %v", err)
	}
}

func TestPaneSequenceStoreCompletionIsIdempotent(t *testing.T) {
	store, err := NewPaneSequenceStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("single", []string{"only step"}); err != nil {
		t.Fatal(err)
	}
	completed, err := store.Advance("single", "0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !completed.Complete || !completed.Advanced || completed.Position != 1 || completed.Prompt != "" {
		t.Fatalf("completion = %+v", completed)
	}
	retry, err := store.Advance("single", "0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !retry.Complete || retry.Advanced || retry.Position != 1 {
		t.Fatalf("idempotent completion retry = %+v", retry)
	}
}

func TestPaneSequenceStoreRejectsUnsafeNamesAndInvalidPrompts(t *testing.T) {
	store, err := NewPaneSequenceStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", ".", "..", "../escape", "nested/name", "nested\\name"} {
		if _, err := store.Create(name, []string{"prompt"}); err == nil {
			t.Errorf("Create(%q) succeeded", name)
		}
	}
	for _, steps := range [][]string{nil, {}, {""}, {"prompt", "  "}} {
		if _, err := store.Create("invalid", steps); err == nil {
			t.Errorf("Create with steps %#v succeeded", steps)
		}
	}
	if _, err := store.Create("valid", []string{"prompt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("valid", []string{"different"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate Create error = %v", err)
	}
	if _, err := store.Next("valid", ""); err == nil {
		t.Fatal("Next accepted an empty pane")
	}
	if _, err := store.Advance("missing", "%1"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing sequence error = %v", err)
	}
}

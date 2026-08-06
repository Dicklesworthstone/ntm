package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

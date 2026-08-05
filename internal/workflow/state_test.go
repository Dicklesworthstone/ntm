package workflow

import (
	"path/filepath"
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

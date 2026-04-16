package ensemble

import (
	"os"
	"sync"
	"testing"
	"time"
)

func resetDefaultStateStoreForTest() {
	CloseDefaultStateStore()
	defaultStateStore = struct {
		mu    sync.Mutex
		store *StateStore
	}{}
}

func TestSessionStore_SaveLoad_DefaultPath(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	resetDefaultStateStoreForTest()

	session := &EnsembleSession{
		SessionName:       "default-session",
		Question:          "Question",
		Status:            EnsembleActive,
		SynthesisStrategy: StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
	}

	if err := SaveSession("", session); err != nil {
		t.Fatalf("SaveSession error: %v", err)
	}
	loaded, err := LoadSession("default-session")
	if err != nil {
		t.Fatalf("LoadSession error: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected loaded session")
	}
	if loaded.Question != session.Question {
		t.Fatalf("Question = %q, want %q", loaded.Question, session.Question)
	}

	resetDefaultStateStoreForTest()

	// ensure no accidental writes to real home
	if _, err := os.Stat(tmpHome); err != nil {
		t.Fatalf("temp home missing: %v", err)
	}
}

func TestSessionStore_ReopensAfterCloseDefaultStateStore(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	resetDefaultStateStoreForTest()

	first := &EnsembleSession{
		SessionName:       "first-session",
		Question:          "First question",
		Status:            EnsembleReady,
		SynthesisStrategy: StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
	}
	if err := SaveSession("", first); err != nil {
		t.Fatalf("first SaveSession error: %v", err)
	}

	CloseDefaultStateStore()

	second := &EnsembleSession{
		SessionName:       "second-session",
		Question:          "Second question",
		Status:            EnsembleReady,
		SynthesisStrategy: StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
	}
	if err := SaveSession("", second); err != nil {
		t.Fatalf("second SaveSession after close error: %v", err)
	}

	loaded, err := LoadSession("second-session")
	if err != nil {
		t.Fatalf("LoadSession after reopen error: %v", err)
	}
	if loaded == nil || loaded.Question != second.Question {
		t.Fatalf("loaded second session = %#v, want question %q", loaded, second.Question)
	}
}

func TestSessionStore_RetriesAfterOpenFailure(t *testing.T) {
	tempRoot := t.TempDir()
	badHome := tempRoot + "/home-file"
	if err := os.WriteFile(badHome, []byte("not-a-directory"), 0o644); err != nil {
		t.Fatalf("write bad home file: %v", err)
	}

	resetDefaultStateStoreForTest()
	t.Setenv("HOME", badHome)
	// Ensure DefaultPath() falls through to HOME instead of using ambient
	// XDG_CONFIG_HOME / NTM_CONFIG that CI runners may pre-populate with a
	// valid directory (which would defeat the "invalid HOME path" scenario).
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("NTM_CONFIG", "")

	bad := &EnsembleSession{
		SessionName:       "bad-open",
		Question:          "Should fail first",
		Status:            EnsembleReady,
		SynthesisStrategy: StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
	}
	if err := SaveSession("", bad); err == nil {
		t.Fatal("expected SaveSession to fail with invalid HOME path")
	}

	goodHome := t.TempDir()
	t.Setenv("HOME", goodHome)

	good := &EnsembleSession{
		SessionName:       "good-open",
		Question:          "Should succeed second",
		Status:            EnsembleReady,
		SynthesisStrategy: StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
	}
	if err := SaveSession("", good); err != nil {
		t.Fatalf("SaveSession after transient open failure error: %v", err)
	}

	loaded, err := LoadSession("good-open")
	if err != nil {
		t.Fatalf("LoadSession after transient open failure error: %v", err)
	}
	if loaded == nil || loaded.Question != good.Question {
		t.Fatalf("loaded session = %#v, want question %q", loaded, good.Question)
	}
}

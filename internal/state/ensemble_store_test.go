package state

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// testStoreFile creates a file-backed test store. This avoids in-memory SQLite's
// per-connection database isolation, which breaks nested queries (e.g. ListEnsembles
// iterating ensemble_sessions while fetching mode_assignments on a second connection).
func testStoreFile(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite3", dbPath+"?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store := &Store{db: db, path: dbPath}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

func TestNewEnsembleStore(t *testing.T) {
	t.Parallel()

	t.Run("nil store returns nil", func(t *testing.T) {
		t.Parallel()
		es := NewEnsembleStore(nil)
		if es != nil {
			t.Fatal("expected nil")
		}
	})

	t.Run("valid store returns non-nil", func(t *testing.T) {
		t.Parallel()
		store := testStore(t)
		es := NewEnsembleStore(store)
		if es == nil {
			t.Fatal("expected non-nil")
		}
	})
}

func TestEnsembleStore_SaveAndGet(t *testing.T) {
	t.Parallel()
	store := testStoreFile(t)
	es := NewEnsembleStore(store)

	now := time.Now().UTC().Truncate(time.Second)

	t.Run("save minimal ensemble", func(t *testing.T) {
		session := &EnsembleSession{
			SessionName: "test-session",
			Question:    "What is the answer?",
			Status:      "pending",
			CreatedAt:   now,
		}

		if err := es.SaveEnsemble(session); err != nil {
			t.Fatalf("save: %v", err)
		}

		if session.ID == 0 {
			t.Error("expected non-zero ID after save")
		}
	})

	t.Run("get saved ensemble", func(t *testing.T) {
		got, err := es.GetEnsemble("test-session")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got == nil {
			t.Fatal("expected non-nil result")
		}
		if got.SessionName != "test-session" {
			t.Errorf("SessionName: want %q, got %q", "test-session", got.SessionName)
		}
		if got.Question != "What is the answer?" {
			t.Errorf("Question: want %q, got %q", "What is the answer?", got.Question)
		}
		if got.Status != "pending" {
			t.Errorf("Status: want %q, got %q", "pending", got.Status)
		}
	})

	t.Run("get nonexistent returns nil", func(t *testing.T) {
		got, err := es.GetEnsemble("no-such-session")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil, got %+v", got)
		}
	})
}

func TestEnsembleStore_SaveWithAssignments(t *testing.T) {
	t.Parallel()
	store := testStoreFile(t)
	es := NewEnsembleStore(store)

	now := time.Now().UTC().Truncate(time.Second)

	session := &EnsembleSession{
		SessionName:       "ensemble-with-assignments",
		Question:          "Analyze this code",
		Status:            "collecting",
		PresetUsed:        "three-way",
		SynthesisStrategy: "majority",
		CreatedAt:         now,
		Assignments: []ModeAssignment{
			{
				ModeID:    "creative",
				PaneName:  "cc_1",
				AgentType: "cc",
				Status:    "pending",
			},
			{
				ModeID:     "analytical",
				PaneName:   "cod_1",
				AgentType:  "cod",
				Status:     "completed",
				OutputPath: "/tmp/output.txt",
				AssignedAt: now.Add(-5 * time.Minute),
			},
		},
	}

	if err := es.SaveEnsemble(session); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := es.GetEnsemble("ensemble-with-assignments")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil")
	}

	if len(got.Assignments) != 2 {
		t.Fatalf("expected 2 assignments, got %d", len(got.Assignments))
	}

	if got.Assignments[0].ModeID != "creative" {
		t.Errorf("assignment[0].ModeID: want %q, got %q", "creative", got.Assignments[0].ModeID)
	}
	if got.Assignments[0].PaneName != "cc_1" {
		t.Errorf("assignment[0].PaneName: want %q, got %q", "cc_1", got.Assignments[0].PaneName)
	}
	if got.Assignments[1].ModeID != "analytical" {
		t.Errorf("assignment[1].ModeID: want %q, got %q", "analytical", got.Assignments[1].ModeID)
	}
	if got.Assignments[1].Status != "completed" {
		t.Errorf("assignment[1].Status: want %q, got %q", "completed", got.Assignments[1].Status)
	}
	if got.Assignments[1].OutputPath != "/tmp/output.txt" {
		t.Errorf("assignment[1].OutputPath: want %q, got %q", "/tmp/output.txt", got.Assignments[1].OutputPath)
	}

	if got.PresetUsed != "three-way" {
		t.Errorf("PresetUsed: want %q, got %q", "three-way", got.PresetUsed)
	}
	if got.SynthesisStrategy != "majority" {
		t.Errorf("SynthesisStrategy: want %q, got %q", "majority", got.SynthesisStrategy)
	}
}

func TestEnsembleStore_SaveUpdatesExisting(t *testing.T) {
	t.Parallel()
	store := testStoreFile(t)
	es := NewEnsembleStore(store)

	now := time.Now().UTC().Truncate(time.Second)

	// Save initial version
	session := &EnsembleSession{
		SessionName: "updatable",
		Question:    "First question",
		Status:      "pending",
		CreatedAt:   now,
	}
	if err := es.SaveEnsemble(session); err != nil {
		t.Fatalf("save initial: %v", err)
	}

	// Update with new question and status
	synthTime := now.Add(10 * time.Minute)
	session.Question = "Updated question"
	session.Status = "synthesized"
	session.SynthesizedAt = &synthTime
	session.SynthesisOutput = "The answer is 42."
	if err := es.SaveEnsemble(session); err != nil {
		t.Fatalf("save update: %v", err)
	}

	got, err := es.GetEnsemble("updatable")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Question != "Updated question" {
		t.Errorf("Question: want %q, got %q", "Updated question", got.Question)
	}
	if got.Status != "synthesized" {
		t.Errorf("Status: want %q, got %q", "synthesized", got.Status)
	}
	if got.SynthesizedAt == nil {
		t.Error("SynthesizedAt: want non-nil")
	}
	if got.SynthesisOutput != "The answer is 42." {
		t.Errorf("SynthesisOutput: want %q, got %q", "The answer is 42.", got.SynthesisOutput)
	}
}

func TestEnsembleStore_SaveValidation(t *testing.T) {
	t.Parallel()
	store := testStoreFile(t)
	es := NewEnsembleStore(store)

	t.Run("nil session", func(t *testing.T) {
		t.Parallel()
		if err := es.SaveEnsemble(nil); err == nil {
			t.Error("expected error for nil session")
		}
	})

	t.Run("empty session name", func(t *testing.T) {
		t.Parallel()
		if err := es.SaveEnsemble(&EnsembleSession{Question: "q"}); err == nil {
			t.Error("expected error for empty session name")
		}
	})

	t.Run("empty question", func(t *testing.T) {
		t.Parallel()
		if err := es.SaveEnsemble(&EnsembleSession{SessionName: "s"}); err == nil {
			t.Error("expected error for empty question")
		}
	})

	t.Run("nil ensemble store", func(t *testing.T) {
		t.Parallel()
		var nilStore *EnsembleStore
		if err := nilStore.SaveEnsemble(&EnsembleSession{SessionName: "s", Question: "q"}); err == nil {
			t.Error("expected error for nil store")
		}
	})

	t.Run("assignment missing mode_id", func(t *testing.T) {
		t.Parallel()
		err := es.SaveEnsemble(&EnsembleSession{
			SessionName: "bad-assignment",
			Question:    "q",
			Status:      "pending",
			Assignments: []ModeAssignment{
				{PaneName: "cc_1"},
			},
		})
		if err == nil {
			t.Error("expected error for missing mode_id")
		}
	})

	t.Run("assignment missing pane_name", func(t *testing.T) {
		t.Parallel()
		err := es.SaveEnsemble(&EnsembleSession{
			SessionName: "bad-pane",
			Question:    "q",
			Status:      "pending",
			Assignments: []ModeAssignment{
				{ModeID: "creative"},
			},
		})
		if err == nil {
			t.Error("expected error for missing pane_name")
		}
	})
}

func TestEnsembleStore_UpdateStatus(t *testing.T) {
	t.Parallel()
	store := testStoreFile(t)
	es := NewEnsembleStore(store)

	// Setup
	es.SaveEnsemble(&EnsembleSession{
		SessionName: "status-test",
		Question:    "q",
		Status:      "pending",
	})

	t.Run("update existing", func(t *testing.T) {
		if err := es.UpdateStatus("status-test", "collecting"); err != nil {
			t.Fatalf("update: %v", err)
		}
		got, _ := es.GetEnsemble("status-test")
		if got.Status != "collecting" {
			t.Errorf("Status: want %q, got %q", "collecting", got.Status)
		}
	})

	t.Run("update nonexistent", func(t *testing.T) {
		err := es.UpdateStatus("no-such", "done")
		if err == nil {
			t.Error("expected error for nonexistent session")
		}
	})

	t.Run("empty session name", func(t *testing.T) {
		t.Parallel()
		err := es.UpdateStatus("", "done")
		if err == nil {
			t.Error("expected error for empty name")
		}
	})

	t.Run("nil store", func(t *testing.T) {
		t.Parallel()
		var nilStore *EnsembleStore
		err := nilStore.UpdateStatus("x", "done")
		if err == nil {
			t.Error("expected error for nil store")
		}
	})
}

func TestEnsembleStore_UpdateAssignmentStatus(t *testing.T) {
	t.Parallel()
	store := testStoreFile(t)
	es := NewEnsembleStore(store)

	// Setup
	es.SaveEnsemble(&EnsembleSession{
		SessionName: "assign-test",
		Question:    "q",
		Status:      "collecting",
		Assignments: []ModeAssignment{
			{ModeID: "creative", PaneName: "cc_1", AgentType: "cc", Status: "pending"},
		},
	})

	t.Run("update existing assignment", func(t *testing.T) {
		if err := es.UpdateAssignmentStatus("assign-test", "creative", "completed"); err != nil {
			t.Fatalf("update: %v", err)
		}
		got, _ := es.GetEnsemble("assign-test")
		if got.Assignments[0].Status != "completed" {
			t.Errorf("Status: want %q, got %q", "completed", got.Assignments[0].Status)
		}
	})

	t.Run("nonexistent assignment", func(t *testing.T) {
		err := es.UpdateAssignmentStatus("assign-test", "no-mode", "done")
		if err == nil {
			t.Error("expected error for nonexistent mode")
		}
	})

	t.Run("nonexistent session", func(t *testing.T) {
		err := es.UpdateAssignmentStatus("no-session", "creative", "done")
		if err == nil {
			t.Error("expected error")
		}
	})

	t.Run("empty params", func(t *testing.T) {
		t.Parallel()
		err := es.UpdateAssignmentStatus("", "creative", "done")
		if err == nil {
			t.Error("expected error for empty session name")
		}
		err = es.UpdateAssignmentStatus("assign-test", "", "done")
		if err == nil {
			t.Error("expected error for empty mode id")
		}
	})

	t.Run("nil store", func(t *testing.T) {
		t.Parallel()
		var nilStore *EnsembleStore
		err := nilStore.UpdateAssignmentStatus("x", "y", "done")
		if err == nil {
			t.Error("expected error for nil store")
		}
	})
}

func TestEnsembleStore_ListEnsembles(t *testing.T) {
	t.Parallel()
	store := testStoreFile(t)
	es := NewEnsembleStore(store)

	t.Run("empty list", func(t *testing.T) {
		sessions, err := es.ListEnsembles()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(sessions) != 0 {
			t.Fatalf("expected 0, got %d", len(sessions))
		}
	})

	// Save a few sessions
	now := time.Now().UTC().Truncate(time.Second)
	if err := es.SaveEnsemble(&EnsembleSession{
		SessionName: "first",
		Question:    "q1",
		Status:      "done",
		CreatedAt:   now.Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("save first: %v", err)
	}
	if err := es.SaveEnsemble(&EnsembleSession{
		SessionName: "second",
		Question:    "q2",
		Status:      "pending",
		CreatedAt:   now,
		Assignments: []ModeAssignment{
			{ModeID: "m1", PaneName: "p1", AgentType: "cc"},
		},
	}); err != nil {
		t.Fatalf("save second: %v", err)
	}

	t.Run("list returns all sorted desc", func(t *testing.T) {
		sessions, err := es.ListEnsembles()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(sessions) != 2 {
			t.Fatalf("expected 2, got %d", len(sessions))
		}
		// Most recent first
		if sessions[0].SessionName != "second" {
			t.Errorf("first: want %q, got %q", "second", sessions[0].SessionName)
		}
		if sessions[1].SessionName != "first" {
			t.Errorf("second: want %q, got %q", "first", sessions[1].SessionName)
		}
		// Assignments loaded
		if len(sessions[0].Assignments) != 1 {
			t.Errorf("expected 1 assignment on second, got %d", len(sessions[0].Assignments))
		}
	})

	t.Run("nil store", func(t *testing.T) {
		t.Parallel()
		var nilStore *EnsembleStore
		_, err := nilStore.ListEnsembles()
		if err == nil {
			t.Error("expected error for nil store")
		}
	})
}

func TestEnsembleStore_DeleteEnsemble(t *testing.T) {
	t.Parallel()
	store := testStoreFile(t)
	es := NewEnsembleStore(store)

	// Save session with assignments
	es.SaveEnsemble(&EnsembleSession{
		SessionName: "to-delete",
		Question:    "q",
		Status:      "done",
		Assignments: []ModeAssignment{
			{ModeID: "m1", PaneName: "p1", AgentType: "cc"},
		},
	})

	t.Run("delete existing", func(t *testing.T) {
		if err := es.DeleteEnsemble("to-delete"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		got, err := es.GetEnsemble("to-delete")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got != nil {
			t.Error("expected nil after delete")
		}
	})

	t.Run("delete nonexistent", func(t *testing.T) {
		err := es.DeleteEnsemble("no-such")
		if err == nil {
			t.Error("expected error for nonexistent session")
		}
	})

	t.Run("empty session name", func(t *testing.T) {
		t.Parallel()
		err := es.DeleteEnsemble("")
		if err == nil {
			t.Error("expected error for empty name")
		}
	})

	t.Run("nil store", func(t *testing.T) {
		t.Parallel()
		var nilStore *EnsembleStore
		err := nilStore.DeleteEnsemble("x")
		if err == nil {
			t.Error("expected error for nil store")
		}
	})
}

func TestEnsembleStore_GetValidation(t *testing.T) {
	t.Parallel()
	store := testStoreFile(t)
	es := NewEnsembleStore(store)

	t.Run("empty session name", func(t *testing.T) {
		t.Parallel()
		_, err := es.GetEnsemble("")
		if err == nil {
			t.Error("expected error for empty name")
		}
	})

	t.Run("nil store", func(t *testing.T) {
		t.Parallel()
		var nilStore *EnsembleStore
		_, err := nilStore.GetEnsemble("x")
		if err == nil {
			t.Error("expected error for nil store")
		}
	})
}

func TestEnsembleStore_SaveDefaultsStatus(t *testing.T) {
	t.Parallel()
	store := testStoreFile(t)
	es := NewEnsembleStore(store)

	// Assignment with empty status should default to "pending"
	es.SaveEnsemble(&EnsembleSession{
		SessionName: "default-status",
		Question:    "q",
		Status:      "collecting",
		Assignments: []ModeAssignment{
			{ModeID: "m1", PaneName: "p1", AgentType: "cc"}, // no Status set
		},
	})

	got, _ := es.GetEnsemble("default-status")
	if got == nil {
		t.Fatal("expected non-nil")
	}
	if len(got.Assignments) != 1 {
		t.Fatalf("expected 1 assignment, got %d", len(got.Assignments))
	}
	if got.Assignments[0].Status != "pending" {
		t.Errorf("Status: want %q (default), got %q", "pending", got.Assignments[0].Status)
	}
}

func TestEnsembleStore_CompletedAtTimestamp(t *testing.T) {
	t.Parallel()
	store := testStoreFile(t)
	es := NewEnsembleStore(store)

	now := time.Now().UTC().Truncate(time.Second)
	completed := now.Add(-1 * time.Minute)

	es.SaveEnsemble(&EnsembleSession{
		SessionName: "completed-ts",
		Question:    "q",
		Status:      "done",
		Assignments: []ModeAssignment{
			{
				ModeID:      "m1",
				PaneName:    "p1",
				AgentType:   "cc",
				Status:      "completed",
				AssignedAt:  now.Add(-5 * time.Minute),
				CompletedAt: &completed,
			},
		},
	})

	got, _ := es.GetEnsemble("completed-ts")
	if got == nil {
		t.Fatal("expected non-nil")
	}
	if len(got.Assignments) != 1 {
		t.Fatalf("expected 1 assignment, got %d", len(got.Assignments))
	}
	if got.Assignments[0].CompletedAt == nil {
		t.Error("CompletedAt: want non-nil")
	}
}

func TestEnsembleStore_SaveSetsCreatedAt(t *testing.T) {
	t.Parallel()
	store := testStoreFile(t)
	es := NewEnsembleStore(store)

	// Save with zero CreatedAt - should auto-fill
	session := &EnsembleSession{
		SessionName: "auto-created",
		Question:    "q",
		Status:      "pending",
	}
	before := time.Now().UTC()
	if err := es.SaveEnsemble(session); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, _ := es.GetEnsemble("auto-created")
	if got == nil {
		t.Fatal("expected non-nil")
	}
	if got.CreatedAt.Before(before.Add(-1 * time.Second)) {
		t.Errorf("CreatedAt too early: %v", got.CreatedAt)
	}
}

func TestEnsembleStore_SaveReplacesAssignments(t *testing.T) {
	t.Parallel()
	store := testStoreFile(t)
	es := NewEnsembleStore(store)

	// Save with 2 assignments
	es.SaveEnsemble(&EnsembleSession{
		SessionName: "replace-test",
		Question:    "q",
		Status:      "collecting",
		Assignments: []ModeAssignment{
			{ModeID: "a", PaneName: "p1", AgentType: "cc"},
			{ModeID: "b", PaneName: "p2", AgentType: "cod"},
		},
	})

	// Re-save with 1 assignment (should replace, not append)
	es.SaveEnsemble(&EnsembleSession{
		SessionName: "replace-test",
		Question:    "q",
		Status:      "done",
		Assignments: []ModeAssignment{
			{ModeID: "c", PaneName: "p3", AgentType: "gmi"},
		},
	})

	got, _ := es.GetEnsemble("replace-test")
	if got == nil {
		t.Fatal("expected non-nil")
	}
	if len(got.Assignments) != 1 {
		t.Fatalf("expected 1 assignment after replace, got %d", len(got.Assignments))
	}
	if got.Assignments[0].ModeID != "c" {
		t.Errorf("ModeID: want %q, got %q", "c", got.Assignments[0].ModeID)
	}
}

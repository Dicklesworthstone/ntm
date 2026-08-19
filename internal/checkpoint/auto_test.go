package checkpoint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAutoCheckpointReason_String(t *testing.T) {
	tests := []struct {
		reason AutoCheckpointReason
		want   string
	}{
		{ReasonBroadcast, "broadcast"},
		{ReasonAddAgents, "add_agents"},
		{ReasonSpawn, "spawn"},
		{ReasonRiskyOp, "risky_op"},
		{ReasonInterval, "interval"},
		{ReasonRotation, "rotation"},
		{ReasonError, "error"},
	}

	for _, tt := range tests {
		if string(tt.reason) != tt.want {
			t.Errorf("AutoCheckpointReason = %q, want %q", tt.reason, tt.want)
		}
	}
}

func TestIsAutoCheckpoint(t *testing.T) {
	tests := []struct {
		name       string
		checkpoint *Checkpoint
		want       bool
	}{
		{
			name:       "auto prefix in name",
			checkpoint: &Checkpoint{Name: "auto-broadcast"},
			want:       true,
		},
		{
			name:       "auto-checkpoint in description",
			checkpoint: &Checkpoint{Name: "manual", Description: "Auto-checkpoint: test"},
			want:       true,
		},
		{
			name:       "manual checkpoint",
			checkpoint: &Checkpoint{Name: "before-refactor", Description: "Manual save"},
			want:       false,
		},
		{
			name:       "empty checkpoint",
			checkpoint: &Checkpoint{},
			want:       false,
		},
		{
			name:       "automation name should not match",
			checkpoint: &Checkpoint{Name: "automation-backup", Description: "User created"},
			want:       false,
		},
		{
			name:       "automatic name should not match",
			checkpoint: &Checkpoint{Name: "automatic", Description: "User created"},
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAutoCheckpoint(tt.checkpoint); got != tt.want {
				t.Errorf("isAutoCheckpoint() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAutoCheckpointOptions_ReasonNaming(t *testing.T) {
	// Test that checkpoint names are generated correctly from reasons
	tests := []struct {
		reason   AutoCheckpointReason
		wantName string
	}{
		{ReasonBroadcast, "auto-broadcast"},
		{ReasonInterval, "auto-interval"},
		{ReasonRotation, "auto-rotation"},
		{ReasonError, "auto-error"},
	}

	for _, tt := range tests {
		t.Run(string(tt.reason), func(t *testing.T) {
			name := AutoCheckpointPrefix + "-" + string(tt.reason)
			if name != tt.wantName {
				t.Errorf("Name = %q, want %q", name, tt.wantName)
			}
		})
	}
}

func TestAutoCheckpointer_RotateAutoCheckpoints_RejectsInvalidRetainedAutoCheckpoint(t *testing.T) {
	t.Parallel()

	storage := NewStorageWithDir(t.TempDir())
	checkpointer := &AutoCheckpointer{
		capturer: NewCapturerWithStorage(storage),
		storage:  storage,
	}
	session := "auto-rotate-retained-session"

	valid := &Checkpoint{
		ID:          "20260101-120000-0001-auto-interval",
		Name:        "auto-interval",
		SessionName: session,
		CreatedAt:   time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Session:     SessionState{},
	}
	if err := storage.Save(valid); err != nil {
		t.Fatalf("Save(%s): %v", valid.ID, err)
	}
	validDir := storage.CheckpointDir(session, valid.ID)
	validTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(validDir, validTime, validTime); err != nil {
		t.Fatalf("Chtimes(%s): %v", validDir, err)
	}

	sessionDir := filepath.Join(storage.BaseDir, session)
	invalidID := "20260101-130000-0002-auto-error"
	invalidDir := filepath.Join(sessionDir, invalidID)
	if err := os.MkdirAll(invalidDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", invalidDir, err)
	}
	if err := os.WriteFile(filepath.Join(invalidDir, MetadataFile), []byte("{"), 0o600); err != nil {
		t.Fatalf("WriteFile(metadata): %v", err)
	}
	invalidTime := time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)
	if err := os.Chtimes(invalidDir, invalidTime, invalidTime); err != nil {
		t.Fatalf("Chtimes(%s): %v", invalidDir, err)
	}

	err := checkpointer.rotateAutoCheckpoints(session, 1)
	if err == nil {
		t.Fatal("rotateAutoCheckpoints() error = nil, want invalid retained auto-checkpoint rejection")
	}
	if !strings.Contains(err.Error(), "auto-checkpoint rotation blocked by invalid retained checkpoint") {
		t.Fatalf("rotateAutoCheckpoints() error = %v, want retained invalid checkpoint context", err)
	}
	if !storage.Exists(session, valid.ID) {
		t.Fatal("valid older auto-checkpoint was deleted despite retained invalid checkpoint error")
	}
}

func TestAutoCheckpointer_RotateAutoCheckpoints_DeletesInvalidOverflowAutoCheckpoint(t *testing.T) {
	t.Parallel()

	storage := NewStorageWithDir(t.TempDir())
	checkpointer := &AutoCheckpointer{
		capturer: NewCapturerWithStorage(storage),
		storage:  storage,
	}
	session := "auto-rotate-overflow-session"

	newest := &Checkpoint{
		ID:          "20260101-140000-0001-auto-interval",
		Name:        "auto-interval",
		SessionName: session,
		CreatedAt:   time.Date(2026, 1, 1, 14, 0, 0, 0, time.UTC),
		Session:     SessionState{},
	}
	if err := storage.Save(newest); err != nil {
		t.Fatalf("Save(%s): %v", newest.ID, err)
	}
	newestDir := storage.CheckpointDir(session, newest.ID)
	newestTime := time.Date(2026, 1, 1, 14, 0, 0, 0, time.UTC)
	if err := os.Chtimes(newestDir, newestTime, newestTime); err != nil {
		t.Fatalf("Chtimes(%s): %v", newestDir, err)
	}

	mid := &Checkpoint{
		ID:          "20260101-130000-0002-auto-error",
		Name:        "auto-error",
		SessionName: session,
		CreatedAt:   time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC),
		Session:     SessionState{},
	}
	if err := storage.Save(mid); err != nil {
		t.Fatalf("Save(%s): %v", mid.ID, err)
	}
	midDir := storage.CheckpointDir(session, mid.ID)
	midTime := time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)
	if err := os.Chtimes(midDir, midTime, midTime); err != nil {
		t.Fatalf("Chtimes(%s): %v", midDir, err)
	}

	sessionDir := filepath.Join(storage.BaseDir, session)
	overflowID := "20260101-120000-0003-auto-rotation"
	overflowPath := filepath.Join(sessionDir, overflowID)
	if err := os.WriteFile(overflowPath, []byte("broken auto-checkpoint path"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", overflowPath, err)
	}
	overflowTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(overflowPath, overflowTime, overflowTime); err != nil {
		t.Fatalf("Chtimes(%s): %v", overflowPath, err)
	}

	if err := checkpointer.rotateAutoCheckpoints(session, 2); err != nil {
		t.Fatalf("rotateAutoCheckpoints(): %v", err)
	}

	exists, err := storage.HasCheckpointPath(session, overflowID)
	if err != nil {
		t.Fatalf("HasCheckpointPath(%s): %v", overflowID, err)
	}
	if exists {
		t.Fatal("invalid overflow auto-checkpoint path still exists after rotation")
	}
	if !storage.Exists(session, newest.ID) || !storage.Exists(session, mid.ID) {
		t.Fatal("valid retained auto-checkpoints were not preserved")
	}
}

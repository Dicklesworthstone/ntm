package cli

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

func TestRunStateGCPrunesExpiredAttentionEventsAndCheckpoints(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("NTM_CONFIG", "")

	store, err := state.Open("")
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}

	now := time.Now().UTC()
	expiredAt := now.Add(-time.Minute)
	liveExpiry := now.Add(time.Hour)
	if _, err := store.AppendAttentionEvent(&state.StoredAttentionEvent{
		Ts:            now.Add(-2 * time.Minute),
		Category:      "system",
		EventType:     "expired",
		Source:        "test",
		Actionability: state.ActionabilityBackground,
		Severity:      state.SeverityInfo,
		Summary:       "expired",
		ExpiresAt:     &expiredAt,
	}); err != nil {
		t.Fatalf("AppendAttentionEvent(expired) error: %v", err)
	}
	if _, err := store.AppendAttentionEvent(&state.StoredAttentionEvent{
		Ts:            now,
		Category:      "system",
		EventType:     "live",
		Source:        "test",
		Actionability: state.ActionabilityBackground,
		Severity:      state.SeverityInfo,
		Summary:       "live",
		ExpiresAt:     &liveExpiry,
	}); err != nil {
		t.Fatalf("AppendAttentionEvent(live) error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	resp, err := runStateGC(stateGCOptions{Checkpoint: true, CheckpointMode: "truncate"})
	if err != nil {
		t.Fatalf("runStateGC() error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("runStateGC() success=false: %+v", resp)
	}
	if resp.GC.ExpiredAttentionEvents != 1 {
		t.Fatalf("ExpiredAttentionEvents = %d, want 1", resp.GC.ExpiredAttentionEvents)
	}
	if resp.Checkpoint == nil {
		t.Fatal("Checkpoint is nil")
	}
	if resp.Checkpoint.Mode != "TRUNCATE" {
		t.Fatalf("Checkpoint.Mode = %q, want TRUNCATE", resp.Checkpoint.Mode)
	}

	verify, err := state.Open("")
	if err != nil {
		t.Fatalf("Open(verify) error: %v", err)
	}
	t.Cleanup(func() { _ = verify.Close() })
	if err := verify.Migrate(); err != nil {
		t.Fatalf("Migrate(verify) error: %v", err)
	}
	events, err := verify.GetAttentionEventsSince(0, 10)
	if err != nil {
		t.Fatalf("GetAttentionEventsSince() error: %v", err)
	}
	if len(events) != 1 || events[0].EventType != "live" {
		t.Fatalf("expected only live event after GC, got %+v", events)
	}
}

func TestRunStateGCRejectsInvalidCheckpointModeBeforeOpeningStore(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
	t.Setenv("NTM_CONFIG", "")

	resp, err := runStateGC(stateGCOptions{Checkpoint: true, CheckpointMode: "delete everything"})
	if err == nil {
		t.Fatal("runStateGC() error = nil, want invalid checkpoint mode")
	}
	if resp.Success {
		t.Fatalf("runStateGC() success=true on invalid mode: %+v", resp)
	}
	if resp.Path != "" {
		t.Fatalf("runStateGC() path = %q, want empty because validation should run before Open", resp.Path)
	}
}

package context

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func TestRotator_GetPendingRotations_Empty(t *testing.T) {
	t.Parallel()
	r := NewRotator(RotatorConfig{})

	got := r.GetPendingRotations()
	if len(got) != 0 {
		t.Errorf("GetPendingRotations() on empty rotator returned %d items; want 0", len(got))
	}
}

func TestRotator_GetPendingRotations_Multiple(t *testing.T) {
	t.Parallel()
	r := NewRotator(RotatorConfig{})

	r.pending["agent-1"] = &PendingRotation{
		AgentID:     "agent-1",
		SessionName: "session-a",
		PaneID:      "pane-1",
		CreatedAt:   time.Now(),
	}
	r.pending["agent-2"] = &PendingRotation{
		AgentID:     "agent-2",
		SessionName: "session-b",
		PaneID:      "pane-2",
		CreatedAt:   time.Now(),
	}

	got := r.GetPendingRotations()
	if len(got) != 2 {
		t.Fatalf("GetPendingRotations() returned %d items; want 2", len(got))
	}

	ids := map[string]bool{}
	for _, p := range got {
		ids[p.AgentID] = true
	}
	if !ids["agent-1"] || !ids["agent-2"] {
		t.Errorf("GetPendingRotations() missing expected agent IDs; got %v", ids)
	}
}

func TestRotator_GetPendingRotation_Exists(t *testing.T) {
	t.Parallel()
	r := NewRotator(RotatorConfig{})

	expected := &PendingRotation{
		AgentID:     "agent-1",
		SessionName: "test-session",
		PaneID:      "pane-5",
	}
	r.pending["agent-1"] = expected

	got := r.GetPendingRotation("agent-1")
	if got != expected {
		t.Errorf("GetPendingRotation(%q) returned different pointer", "agent-1")
	}
	if got.SessionName != "test-session" {
		t.Errorf("GetPendingRotation(%q).SessionName = %q; want %q", "agent-1", got.SessionName, "test-session")
	}
}

func TestRotator_GetPendingRotation_Missing(t *testing.T) {
	t.Parallel()
	r := NewRotator(RotatorConfig{})

	got := r.GetPendingRotation("nonexistent")
	if got != nil {
		t.Errorf("GetPendingRotation(%q) = %v; want nil", "nonexistent", got)
	}
}

func TestRotator_HasPendingRotation(t *testing.T) {
	t.Parallel()
	r := NewRotator(RotatorConfig{})

	r.pending["agent-1"] = &PendingRotation{AgentID: "agent-1"}

	tests := []struct {
		agentID string
		want    bool
	}{
		{"agent-1", true},
		{"agent-2", false},
		{"", false},
	}

	for _, tc := range tests {
		got := r.HasPendingRotation(tc.agentID)
		if got != tc.want {
			t.Errorf("HasPendingRotation(%q) = %v; want %v", tc.agentID, got, tc.want)
		}
	}
}

// TestRotator_EnqueuePendingRotation covers the external-trigger entry point
// used by the coordinator's transcript-usage check (bd-rpmg8): it must land in
// both the rotator's in-memory pending map (so ConfirmRotation can execute it)
// and the persistent store the CLI 'rotate context pending' surface reads.
func TestRotator_EnqueuePendingRotation(t *testing.T) {
	// Not parallel: redirects the package-level DefaultPendingRotationStore.
	origStore := DefaultPendingRotationStore
	DefaultPendingRotationStore = NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	t.Cleanup(func() { DefaultPendingRotationStore = origStore })

	r := NewRotator(RotatorConfig{Config: config.ContextRotationConfig{
		ConfirmTimeoutSec:    300,
		DefaultConfirmAction: "rotate",
	}})

	p := r.EnqueuePendingRotation("sess", "sess__cc_1", "%3", 91.5, "/work")
	if p == nil {
		t.Fatal("EnqueuePendingRotation returned nil")
	}
	if p.AgentID != "sess__cc_1" || p.SessionName != "sess" || p.PaneID != "%3" ||
		p.ContextPercent != 91.5 || p.WorkDir != "/work" {
		t.Fatalf("pending = %+v", p)
	}
	if p.DefaultAction != ConfirmRotate {
		t.Errorf("default action = %q, want rotate", p.DefaultAction)
	}
	if !r.HasPendingRotation("sess__cc_1") {
		t.Error("pending rotation missing from rotator memory")
	}
	stored, err := GetPendingRotationByID("sess__cc_1")
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.ContextPercent != 91.5 {
		t.Fatalf("stored pending = %+v, want persisted entry at 91.5%%", stored)
	}

	// Re-enqueueing while pending returns the existing entry unchanged.
	again := r.EnqueuePendingRotation("sess", "sess__cc_1", "%3", 95.0, "/work")
	if again != p {
		t.Error("re-enqueue created a new pending rotation instead of returning the existing one")
	}
	if again.ContextPercent != 91.5 {
		t.Errorf("re-enqueue mutated pending: %+v", again)
	}

	// An expired in-memory entry is replaced with a fresh one.
	r.mu.Lock()
	r.pending["sess__cc_1"].TimeoutAt = time.Now().Add(-time.Minute)
	r.mu.Unlock()
	replaced := r.EnqueuePendingRotation("sess", "sess__cc_1", "%3", 97.0, "/work")
	if replaced == p {
		t.Fatal("expired pending rotation was not replaced")
	}
	if replaced.ContextPercent != 97.0 || replaced.IsExpired() {
		t.Fatalf("replacement pending = %+v", replaced)
	}
}

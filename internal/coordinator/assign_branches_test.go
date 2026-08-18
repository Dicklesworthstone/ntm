package coordinator

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/bv"
)

// ---------------------------------------------------------------------------
// estimateTaskComplexity — unknown type and clamp branches
// ---------------------------------------------------------------------------

func TestEstimateTaskComplexity_UnknownType(t *testing.T) {
	t.Parallel()
	rec := &bv.TriageRecommendation{Type: "unknown-type", Priority: 1}
	got := estimateTaskComplexity(rec)
	// Unknown type gets no bonus/penalty → stays at 0.5 base
	if got != 0.5 {
		t.Errorf("estimateTaskComplexity(unknown type) = %f, want 0.5", got)
	}
}

func TestEstimateTaskComplexity_ClampsToOne(t *testing.T) {
	t.Parallel()
	// epic (0.3) + priority>=3 (0.1) + 5 unblocks (0.15) = 0.5+0.3+0.1+0.15 = 1.05 → clamped to 1.0
	rec := &bv.TriageRecommendation{
		Type:        "epic",
		Priority:    4,
		UnblocksIDs: []string{"a", "b", "c", "d", "e"},
	}
	got := estimateTaskComplexity(rec)
	if got != 1.0 {
		t.Errorf("estimateTaskComplexity() = %f, want 1.0 (clamped)", got)
	}
}

func TestEstimateTaskComplexity_ClampsToZero(t *testing.T) {
	t.Parallel()
	// chore (-0.2) + priority==0 (-0.1) = 0.5-0.2-0.1 = 0.2, not below 0
	rec := &bv.TriageRecommendation{Type: "chore", Priority: 0}
	got := estimateTaskComplexity(rec)
	if got < 0.0 || got > 0.3 {
		t.Errorf("estimateTaskComplexity(chore, p0) = %f, want ~0.2", got)
	}
}

func TestEstimateTaskComplexity_ThreeUnblocks(t *testing.T) {
	t.Parallel()
	rec := &bv.TriageRecommendation{
		Type:        "task",
		Priority:    1,
		UnblocksIDs: []string{"a", "b", "c"},
	}
	got := estimateTaskComplexity(rec)
	// task(-0.1) + 3 unblocks(+0.1) = 0.5-0.1+0.1 = 0.5
	if got < 0.45 || got > 0.55 {
		t.Errorf("estimateTaskComplexity(task, 3 unblocks) = %f, want ~0.5", got)
	}
}

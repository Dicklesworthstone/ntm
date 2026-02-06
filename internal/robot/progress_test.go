package robot

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/bv"
)

func TestComputeProgress(t *testing.T) {
	t.Parallel()

	t.Run("nil beads returns nil", func(t *testing.T) {
		t.Parallel()
		if got := ComputeProgress(nil); got != nil {
			t.Fatalf("expected nil, got %+v", got)
		}
	})

	t.Run("unavailable beads returns nil", func(t *testing.T) {
		t.Parallel()
		got := ComputeProgress(&bv.BeadsSummary{Available: false, Total: 10})
		if got != nil {
			t.Fatalf("expected nil, got %+v", got)
		}
	})

	t.Run("zero total returns nil", func(t *testing.T) {
		t.Parallel()
		got := ComputeProgress(&bv.BeadsSummary{Available: true, Total: 0})
		if got != nil {
			t.Fatalf("expected nil, got %+v", got)
		}
	})

	t.Run("basic progress computation", func(t *testing.T) {
		t.Parallel()
		got := ComputeProgress(&bv.BeadsSummary{
			Available:  true,
			Total:      100,
			Open:       20,
			InProgress: 10,
			Closed:     70,
		})
		if got == nil {
			t.Fatal("expected non-nil progress")
		}
		if got.Assigned != 10 {
			t.Errorf("Assigned: want 10, got %d", got.Assigned)
		}
		if got.Completed != 70 {
			t.Errorf("Completed: want 70, got %d", got.Completed)
		}
		if got.Remaining != 30 {
			t.Errorf("Remaining: want 30, got %d", got.Remaining)
		}
		if got.Total != 100 {
			t.Errorf("Total: want 100, got %d", got.Total)
		}
		if got.CompletionRatio != 0.7 {
			t.Errorf("CompletionRatio: want 0.7, got %f", got.CompletionRatio)
		}
	})

	t.Run("all closed", func(t *testing.T) {
		t.Parallel()
		got := ComputeProgress(&bv.BeadsSummary{
			Available: true,
			Total:     50,
			Closed:    50,
		})
		if got == nil {
			t.Fatal("expected non-nil progress")
		}
		if got.CompletionRatio != 1.0 {
			t.Errorf("CompletionRatio: want 1.0, got %f", got.CompletionRatio)
		}
		if got.Remaining != 0 {
			t.Errorf("Remaining: want 0, got %d", got.Remaining)
		}
	})

	t.Run("none closed", func(t *testing.T) {
		t.Parallel()
		got := ComputeProgress(&bv.BeadsSummary{
			Available:  true,
			Total:      10,
			Open:       8,
			InProgress: 2,
		})
		if got == nil {
			t.Fatal("expected non-nil progress")
		}
		if got.CompletionRatio != 0.0 {
			t.Errorf("CompletionRatio: want 0.0, got %f", got.CompletionRatio)
		}
		if got.Remaining != 10 {
			t.Errorf("Remaining: want 10, got %d", got.Remaining)
		}
	})

	t.Run("ratio rounds to 4 decimals", func(t *testing.T) {
		t.Parallel()
		got := ComputeProgress(&bv.BeadsSummary{
			Available: true,
			Total:     3,
			Closed:    1,
			Open:      2,
		})
		if got == nil {
			t.Fatal("expected non-nil progress")
		}
		// 1/3 = 0.33333... should round to 0.3333
		if got.CompletionRatio != 0.3333 {
			t.Errorf("CompletionRatio: want 0.3333, got %f", got.CompletionRatio)
		}
	})
}

package swarm

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ---------------------------------------------------------------------------
// NewAgentLauncherWithLogger — 0% → 100%
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// BeadScanner WithLogger option — 0% → 100%
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// NewLimitDetectorWithClient — 0% → 100%
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// NewSessionOrchestratorWithClient — 0% → 100%
// ---------------------------------------------------------------------------

func TestNewSessionOrchestratorWithClient(t *testing.T) {
	t.Parallel()

	client := &tmux.Client{}
	so := NewSessionOrchestratorWithClient(client)

	if so == nil {
		t.Fatal("expected non-nil SessionOrchestrator")
	}
	if so.TmuxClient != client {
		t.Error("expected TmuxClient to be set")
	}
	if so.StaggerDelay == 0 {
		t.Error("expected non-zero StaggerDelay default")
	}
}

// ---------------------------------------------------------------------------
// NewPromptInjectorWithClient — 0% → 100%
// ---------------------------------------------------------------------------

func TestNewPromptInjectorWithClient(t *testing.T) {
	t.Parallel()

	client := &tmux.Client{}
	pi := NewPromptInjectorWithClient(client)

	if pi == nil {
		t.Fatal("expected non-nil PromptInjector")
	}
	if pi.TmuxClient != client {
		t.Error("expected TmuxClient to be set")
	}
}

// ---------------------------------------------------------------------------
// ReviewPromptGenerator.WithReviewLogger — 0% → 100%
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// PaneLauncher.WithRateLimitTracker — 0% → 100%
// ---------------------------------------------------------------------------

func TestPaneLauncher_WithRateLimitTracker(t *testing.T) {
	t.Parallel()

	pl := NewPaneLauncher()
	result := pl.WithRateLimitTracker(nil)

	if result != pl {
		t.Error("expected WithRateLimitTracker to return same pointer for chaining")
	}
}

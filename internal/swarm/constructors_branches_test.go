package swarm

import (
	"log/slog"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ---------------------------------------------------------------------------
// NewAgentLauncherWithLogger — 0% → 100%
// ---------------------------------------------------------------------------

func TestNewAgentLauncherWithLogger(t *testing.T) {
	t.Parallel()

	logger := slog.Default()
	al := NewAgentLauncherWithLogger(logger)

	if al == nil {
		t.Fatal("expected non-nil AgentLauncher")
	}
	if al.Logger != logger {
		t.Error("expected logger to be set")
	}
	if al.TmuxClient != nil {
		t.Error("expected nil TmuxClient for logger-only constructor")
	}
}

func TestNewAgentLauncherWithLogger_Nil(t *testing.T) {
	t.Parallel()

	al := NewAgentLauncherWithLogger(nil)
	if al == nil {
		t.Fatal("expected non-nil AgentLauncher")
	}
	if al.Logger != nil {
		t.Error("expected nil logger when nil passed")
	}
}

// ---------------------------------------------------------------------------
// BeadScanner WithLogger option — 0% → 100%
// ---------------------------------------------------------------------------

func TestBeadScannerWithLogger(t *testing.T) {
	t.Parallel()

	logger := slog.Default()
	bs := NewBeadScanner("/tmp", WithLogger(logger))

	if bs == nil {
		t.Fatal("expected non-nil BeadScanner")
	}
	if bs.Logger != logger {
		t.Error("expected logger to be set via WithLogger option")
	}
}

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

func TestReviewPromptGenerator_WithReviewLogger(t *testing.T) {
	t.Parallel()

	g := NewReviewPromptGenerator()
	logger := slog.Default()

	result := g.WithReviewLogger(logger)
	if result != g {
		t.Error("expected WithReviewLogger to return same pointer for chaining")
	}
	if g.Logger != logger {
		t.Error("expected Logger to be set")
	}
}

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

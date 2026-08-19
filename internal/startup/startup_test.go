package startup

import (
	"testing"
)

func TestPhaseTransitions(t *testing.T) {
	// Reset state before test
	Reset()

	// Initially at PhaseNone
	if CurrentPhase() != PhaseNone {
		t.Errorf("Expected PhaseNone, got %v", CurrentPhase())
	}

	// Begin Phase 1
	BeginPhase1()
	if CurrentPhase() != Phase1 {
		t.Errorf("Expected Phase1, got %v", CurrentPhase())
	}

	// End Phase 1
	EndPhase1()
	if !IsPhase1Complete() {
		t.Error("Phase 1 should be complete")
	}
	if CurrentPhase() != Phase2 {
		t.Errorf("Expected Phase2 after Phase1 complete, got %v", CurrentPhase())
	}

	// Begin Phase 2
	BeginPhase2()

	// End Phase 2
	EndPhase2()
	if !IsPhase2Complete() {
		t.Error("Phase 2 should be complete")
	}
	if CurrentPhase() != PhaseComplete {
		t.Errorf("Expected PhaseComplete, got %v", CurrentPhase())
	}
}

package panels

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSpawnWizardBackspaceDoesNotLeaveCountsStep(t *testing.T) {
	t.Parallel()

	sw := NewSpawnWizard("test", 120, 30)
	sw.step = SpawnStepCounts
	sw.ccStr = "12"
	sw.codStr = "1"
	sw.gmiStr = "0"
	sw.initCountsForm()

	updated, _ := sw.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	sw = updated.(*SpawnWizard)

	if sw.step != SpawnStepCounts {
		t.Fatalf("expected backspace to stay on counts step, got %v", sw.step)
	}
}

func TestSpawnWizardShiftTabMovesBackOneStep(t *testing.T) {
	t.Parallel()

	sw := NewSpawnWizard("test", 120, 30)
	sw.step = SpawnStepCounts
	sw.initCountsForm()

	updated, cmd := sw.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	sw = updated.(*SpawnWizard)

	if sw.step != SpawnStepMethod {
		t.Fatalf("expected shift+tab to return to method step, got %v", sw.step)
	}
	if cmd == nil {
		t.Fatal("expected shift+tab back navigation to reinitialize the method form")
	}
}

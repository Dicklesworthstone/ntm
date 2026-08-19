package robot

import (
	"testing"
)

// TestSessionStructure_findPrimaryWindow tests window selection.
func TestSessionStructure_findPrimaryWindow(t *testing.T) {
	tests := []struct {
		name      string
		windowIDs []int
		expected  int
	}{
		{"standard NTM", []int{1}, 1},
		{"window 1 preferred", []int{0, 1, 2}, 1},
		{"no window 1", []int{0, 2}, 0},
		{"empty", []int{}, 0},
		{"non-standard", []int{3, 4, 5}, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := SessionStructure{WindowIDs: tt.windowIDs}
			got := s.findPrimaryWindow()
			t.Logf("TEST: %s | WindowIDs: %v | Expected: %d | Got: %d", tt.name, tt.windowIDs, tt.expected, got)
			if got != tt.expected {
				t.Errorf("findPrimaryWindow() = %d, want %d", got, tt.expected)
			}
		})
	}
}

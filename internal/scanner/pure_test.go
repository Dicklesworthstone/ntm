package scanner

import (
	"strings"
	"testing"
)

func expectStringEqual(t *testing.T, label, got, want string) {
	t.Helper()
	if strings.Compare(got, want) != 0 {
		t.Errorf("%s = %q, want %q", label, got, want)
	}
}

func expectBoolEqual(t *testing.T, label string, got, want bool) {
	t.Helper()
	if got {
		if !want {
			t.Errorf("%s = %v, want %v", label, got, want)
		}
		return
	}
	if want {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

func TestEstimateFileCentrality(t *testing.T) {
	t.Parallel()

	result := estimateFileCentrality("any/file.go", map[string]float64{"any/file.go": 1.25})
	if result != 1.25 {
		t.Errorf("estimateFileCentrality = %f, want 1.25", result)
	}

	result = estimateFileCentrality("any/file.go", map[string]float64{"other.go": 1.0})
	if result != 0.0 {
		t.Errorf("estimateFileCentrality with unmatched file = %f, want 0.0", result)
	}

	result = estimateFileCentrality("file.go", nil)
	if result != 0.0 {
		t.Errorf("estimateFileCentrality with nil map = %f, want 0.0", result)
	}

	result = estimateFileCentrality("file.go", map[string]float64{"file.go": -1.0})
	if result != 0.0 {
		t.Errorf("estimateFileCentrality with negative score = %f, want 0.0", result)
	}
}

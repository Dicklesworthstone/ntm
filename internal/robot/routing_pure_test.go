package robot

import "testing"

// =============================================================================
// contextUsageForPane tests
// =============================================================================

func TestContextUsageForPane(t *testing.T) {

	tests := []struct {
		name   string
		usage  map[string]float64
		paneID string
		want   float64
	}{
		{"nil map", nil, "%41", 0},
		{"empty map", map[string]float64{}, "%41", 0},
		{"key exists", map[string]float64{"%41": 45.5, "%51": 80.0}, "%41", 45.5},
		{"other window", map[string]float64{"%41": 45.5, "%51": 80.0}, "%51", 80.0},
		{"key missing", map[string]float64{"%41": 45.5}, "%51", 0},
		{"zero value exists", map[string]float64{"%0": 0.0}, "%0", 0},
		{"missing identity", map[string]float64{"%41": 45.5}, "", 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := contextUsageForPane(tc.usage, tc.paneID)
			if got != tc.want {
				t.Errorf("contextUsageForPane(_, %q) = %f, want %f", tc.paneID, got, tc.want)
			}
		})
	}
}

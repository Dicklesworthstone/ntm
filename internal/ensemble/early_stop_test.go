package ensemble

import (
	"testing"
)

// TestEarlyStopConfig_Defaults tests default configuration behavior.
func TestEarlyStopConfig_Defaults(t *testing.T) {
	cfg := EarlyStopConfig{}

	if cfg.Enabled {
		t.Error("Default Enabled should be false")
	}
	if cfg.MinAgentsBeforeStop != 0 {
		t.Errorf("Default MinAgentsBeforeStop = %d, want 0", cfg.MinAgentsBeforeStop)
	}
	if cfg.FindingsThreshold != 0 {
		t.Errorf("Default FindingsThreshold = %f, want 0", cfg.FindingsThreshold)
	}
	if cfg.SimilarityThreshold != 0 {
		t.Errorf("Default SimilarityThreshold = %f, want 0", cfg.SimilarityThreshold)
	}
	if cfg.WindowSize != 0 {
		t.Errorf("Default WindowSize = %d, want 0", cfg.WindowSize)
	}
}

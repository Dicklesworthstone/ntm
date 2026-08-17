package quota

// WS6-wire behavior proof (bd-ws6-config-truth-ienmd.1): flipping
// [rotation.thresholds] warning_percent / critical_percent changes how the
// rotation engine classifies the same quota reading.

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func resetRotationThresholds() {
	rotationThresholdMu.Lock()
	rotationWarningPercent = defaultWarningPercent
	rotationCriticalPct = defaultCriticalPercent
	rotationThresholdMu.Unlock()
}

func TestClassifyRotationGovernedByThresholds(t *testing.T) {
	defer resetRotationThresholds()

	reading := &QuotaInfo{SessionUsage: 85} // fixed reading; only config flips

	cases := []struct {
		name       string
		thresholds config.RotationThresholds
		want       RotationClass
	}{
		{
			// Shipping defaults (DefaultRotationConfig): warn 80, critical 95.
			name:       "default_thresholds_warning",
			thresholds: config.DefaultRotationConfig().Thresholds,
			want:       RotationWarning,
		},
		{
			name:       "raised_warning_threshold_ok",
			thresholds: config.RotationThresholds{WarningPercent: 90, CriticalPercent: 95},
			want:       RotationOK,
		},
		{
			name:       "lowered_critical_threshold_limited",
			thresholds: config.RotationThresholds{WarningPercent: 50, CriticalPercent: 80},
			want:       RotationLimited,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetRotationThresholds()
			ApplyRotationThresholds(tc.thresholds.WarningPercent, tc.thresholds.CriticalPercent)
			if got := ClassifyRotation(reading); got != tc.want {
				t.Fatalf("ClassifyRotation(85%%) = %s, want %s (thresholds must govern classification)", got, tc.want)
			}
		})
	}
}

func TestClassifyRotationHardLimitAndUnknown(t *testing.T) {
	defer resetRotationThresholds()
	resetRotationThresholds()

	if got := ClassifyRotation(&QuotaInfo{IsLimited: true}); got != RotationLimited {
		t.Errorf("hard provider limit must classify limited regardless of usage, got %s", got)
	}
	if got := ClassifyRotation(nil); got != RotationOK {
		t.Errorf("nil reading must classify ok (absence of data is not evidence), got %s", got)
	}
	if got := ClassifyRotation(&QuotaInfo{Error: "fetch failed", SessionUsage: 99}); got != RotationOK {
		t.Errorf("errored reading must classify ok, got %s", got)
	}
}

package quota

// WS6-wire (bd-ws6-config-truth-ienmd.1): [rotation.thresholds]
// warning_percent / critical_percent govern the rotation engine's quota
// classification (`ntm rotate all-limited` pane selection and warnings).
// ApplyRotationThresholds is invoked once per process after config load
// (internal/cli/root.go) with cfg.Rotation.Thresholds; compiled-in defaults
// match DefaultRotationConfig (warn at 80%, critical at 95%).

import (
	"sync"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

const (
	defaultWarningPercent  = 80
	defaultCriticalPercent = 95
)

var (
	rotationThresholdMu    sync.RWMutex
	rotationWarningPercent = defaultWarningPercent
	rotationCriticalPct    = defaultCriticalPercent
)

// ApplyRotationThresholds configures the usage percentages at which the
// rotation engine warns about and rotates an account. Non-positive values
// keep the compiled-in defaults.
func ApplyRotationThresholds(warningPercent, criticalPercent int) {
	rotationThresholdMu.Lock()
	defer rotationThresholdMu.Unlock()
	if warningPercent > 0 {
		rotationWarningPercent = warningPercent
	}
	if criticalPercent > 0 {
		rotationCriticalPct = criticalPercent
	}
}

// RotationClass classifies a quota reading for the rotation engine.
type RotationClass string

const (
	// RotationOK means the account is comfortably under the thresholds.
	RotationOK RotationClass = "ok"
	// RotationWarning means usage is at/over warning_percent but under
	// critical_percent: surfaced to the operator, not rotated.
	RotationWarning RotationClass = "warning"
	// RotationLimited means the provider reports a hard rate limit or usage
	// is at/over critical_percent: the pane is selected for rotation.
	RotationLimited RotationClass = "limited"
)

// ClassifyRotation classifies a quota reading against the configured
// [rotation.thresholds]. A nil or errored reading classifies as OK — absence
// of data is never grounds for rotating an account.
func ClassifyRotation(info *QuotaInfo) RotationClass {
	if info == nil || info.Error != "" {
		return RotationOK
	}
	rotationThresholdMu.RLock()
	warning, critical := rotationWarningPercent, rotationCriticalPct
	rotationThresholdMu.RUnlock()

	if info.IsLimited {
		return RotationLimited
	}
	usage := info.HighestUsage()
	if usage >= float64(critical) {
		return RotationLimited
	}
	if usage >= float64(warning) {
		return RotationWarning
	}
	return RotationOK
}

func init() {
	// G2 config-key liveness claims: this package reads
	// rotation.thresholds.{warning,critical}_percent via
	// ApplyRotationThresholds at startup.
	config.RegisterReader("rotation.thresholds.warning_percent", ApplyRotationThresholds)
	config.RegisterReader("rotation.thresholds.critical_percent", ApplyRotationThresholds)
}

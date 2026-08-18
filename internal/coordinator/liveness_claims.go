package coordinator

import (
	"github.com/Dicklesworthstone/ntm/internal/config"
)

// WS0-G2 config-key liveness claims for keys consumed by internal/coordinator
// (bd-g2-claims-backlog-o787y). rotation.thresholds.restart_* claims predate
// this file (rotation.go). See internal/config/liveness.go.
func init() {
	// CAAM auto-failover (caam_failover.go, coordinator.go).
	config.RegisterReader("integrations.caam.auto_failover", newFailoverChecker)
	config.RegisterReader("integrations.caam.failover_providers", newFailoverChecker)
	config.RegisterReader("integrations.caam.reset_horizon_minutes", newFailoverChecker)
	config.RegisterReader("integrations.caam.binary_path", newFailoverChecker)
}

package pt

import (
	"github.com/Dicklesworthstone/ntm/internal/config"
)

// WS0-G2 config-key liveness claims for keys consumed by
// internal/integrations/pt (bd-g2-claims-backlog-o787y). The enabled gate is
// claimed by internal/cli (runServe). See internal/config/liveness.go.
func init() {
	config.RegisterReader("integrations.process_triage.check_interval", NewHealthMonitor)
	config.RegisterReader("integrations.process_triage.idle_threshold", NewHealthMonitor)
	config.RegisterReader("integrations.process_triage.stuck_threshold", NewHealthMonitor)
	config.RegisterReader("integrations.process_triage.use_rano_data", NewHealthMonitor)
}

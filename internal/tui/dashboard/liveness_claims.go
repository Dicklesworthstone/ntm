package dashboard

import (
	"github.com/Dicklesworthstone/ntm/internal/config"
)

// WS0-G2 config-key liveness claims for keys consumed by
// internal/tui/dashboard (bd-g2-claims-backlog-o787y). See
// internal/config/liveness.go.
func init() {
	// Theme + help verbosity applied on config (re)load (dashboard.go Update).
	config.RegisterReader("theme", Model.Update)
	config.RegisterReader("help_verbosity", Model.Update)

	// Rano network stats polling (dashboard.go).
	config.RegisterReader("integrations.rano.enabled", (*Model).fetchRanoNetworkStats)
	config.RegisterReader("integrations.rano.poll_interval_ms", (*Model).fetchRanoNetworkStats)

	// Compaction recovery wiring (dashboard.go).
	for _, key := range []string{
		"context_rotation.recovery.enabled",
		"context_rotation.recovery.cooldown_seconds",
		"context_rotation.recovery.max_recoveries_per_pane",
		"context_rotation.recovery.prompt",
		"context_rotation.recovery.include_bead_context",
	} {
		config.RegisterReader(key, compactionRecoveryConfigToRuntime)
	}
}

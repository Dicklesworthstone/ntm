package privacy

import (
	"github.com/Dicklesworthstone/ntm/internal/config"
)

// WS0-G2 config-key liveness claims for keys consumed by internal/privacy
// (bd-g2-claims-backlog-o787y). Wired from cli root as
// privacy.SetDefaultManager(privacy.New(cfg.Privacy)). See
// internal/config/liveness.go.
func init() {
	for _, key := range []string{
		"privacy.enabled",
		"privacy.disable_checkpoints",
		"privacy.disable_event_logs",
		"privacy.disable_prompt_history",
		"privacy.disable_scrollback_capture",
		"privacy.require_explicit_persist",
	} {
		config.RegisterReader(key, New)
	}
}

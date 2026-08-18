package context

import (
	"github.com/Dicklesworthstone/ntm/internal/config"
)

// WS0-G2 config-key liveness claims for keys consumed by internal/context
// (bd-g2-claims-backlog-o787y). The rotator receives cfg.ContextRotation via
// internal/coordinator/rotation.go → NewRotator. See
// internal/config/liveness.go.
func init() {
	for _, key := range []string{
		"context_rotation.enabled",
		"context_rotation.warning_threshold",
		"context_rotation.rotate_threshold",
		"context_rotation.require_confirm",
	} {
		config.RegisterReader(key, (*Rotator).CheckAndRotate)
	}
	config.RegisterReader("context_rotation.min_session_age_sec", (*Rotator).CheckAndRotate)
	config.RegisterReader("context_rotation.confirm_timeout_sec", (*Rotator).CheckAndRotate)
	config.RegisterReader("context_rotation.default_confirm_action", (*Rotator).CheckAndRotate)
	config.RegisterReader("context_rotation.summary_max_tokens", NewRotator)
	config.RegisterReader("context_rotation.try_compact_first", (*Rotator).CheckAndRotate)
}

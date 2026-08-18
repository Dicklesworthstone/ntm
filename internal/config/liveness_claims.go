package config

import (
	"github.com/Dicklesworthstone/ntm/internal/models"
)

// WS0-G2 claims registered from inside internal/config
// (bd-g2-claims-backlog-o787y): models.context_limits is consumed by
// internal/models.ApplyOverrides, but that package cannot claim it without an
// import cycle (internal/config imports internal/models), and the field's
// only dereference is loadWithCWD handing it to ApplyOverrides. Same
// placement rationale as recovery_alias.go's memory.* claims.
func init() {
	RegisterReader("models.context_limits", models.ApplyOverrides)
}

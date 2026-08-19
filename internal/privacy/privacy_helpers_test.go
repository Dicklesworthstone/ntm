package privacy

import "github.com/Dicklesworthstone/ntm/internal/config"

// DefaultManager creates a Manager with default privacy config (test-only
// convenience constructor).
func DefaultManager() *Manager {
	return New(config.DefaultPrivacyConfig())
}

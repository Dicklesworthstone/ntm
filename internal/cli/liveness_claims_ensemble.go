//go:build !liveness_audit

package cli

import (
	"github.com/Dicklesworthstone/ntm/internal/config"
)

// WS0-G2 config-key liveness claims for the graduated ensemble.* spawn/cache keys.
//
// applyEnsembleConfigOverrides (ensemble_spawn.go) folds these config values
// into the ensemble.EnsembleConfig used by `ntm ensemble spawn` (and the
// robot spawn surface mirrors it in internal/robot/ensemble_spawn.go) in every
// build now that `ensemble_experimental` has been graduated.
func init() {
	config.RegisterReader("ensemble.synthesis.strategy", applyEnsembleConfigOverrides)
	config.RegisterReader("ensemble.cache.ttl_minutes", applyEnsembleConfigOverrides)
	config.RegisterReader("ensemble.cache.cache_dir", applyEnsembleConfigOverrides)
	config.RegisterReader("ensemble.cache.max_entries", applyEnsembleConfigOverrides)
	config.RegisterReader("ensemble.cache.share_across_modes", applyEnsembleConfigOverrides)
}

//go:build ensemble_experimental
// +build ensemble_experimental

package cli

import (
	"github.com/Dicklesworthstone/ntm/internal/config"
)

// WS0-G2 config-key liveness claims for the ensemble.* keys whose ONLY
// readers live behind the ensemble_experimental build tag (bd-6otuk).
//
// applyEnsembleConfigOverrides (ensemble_spawn.go) folds these config values
// into the ensemble.EnsembleConfig used by `ntm ensemble spawn` (and the
// robot spawn surface mirrors it in internal/robot/ensemble_spawn.go). These
// init() registrations compile — and therefore register — only when the tag
// is set, so the untagged liveness walk cannot see them; the same keys carry
// `permanent` entries in ci/allowlists/config.txt naming this build-tag
// blind spot. Keys read in every build (default_ensemble, agent_mix,
// assignment, allow_advanced, mode_tier_default, budget.total,
// budget.per_agent, cache.enabled) are claimed untagged in
// liveness_claims.go instead.
func init() {
	config.RegisterReader("ensemble.synthesis.strategy", applyEnsembleConfigOverrides)
	config.RegisterReader("ensemble.synthesis.min_confidence", applyEnsembleConfigOverrides)
	config.RegisterReader("ensemble.synthesis.max_findings", applyEnsembleConfigOverrides)
	config.RegisterReader("ensemble.synthesis.include_raw_outputs", applyEnsembleConfigOverrides)
	config.RegisterReader("ensemble.synthesis.conflict_resolution", applyEnsembleConfigOverrides)
	config.RegisterReader("ensemble.budget.synthesis", applyEnsembleConfigOverrides)
	config.RegisterReader("ensemble.budget.context_pack", applyEnsembleConfigOverrides)
	config.RegisterReader("ensemble.cache.ttl_minutes", applyEnsembleConfigOverrides)
	config.RegisterReader("ensemble.cache.cache_dir", applyEnsembleConfigOverrides)
	config.RegisterReader("ensemble.cache.max_entries", applyEnsembleConfigOverrides)
	config.RegisterReader("ensemble.cache.share_across_modes", applyEnsembleConfigOverrides)
	config.RegisterReader("ensemble.early_stop.enabled", applyEnsembleConfigOverrides)
	config.RegisterReader("ensemble.early_stop.min_agents", applyEnsembleConfigOverrides)
	config.RegisterReader("ensemble.early_stop.findings_threshold", applyEnsembleConfigOverrides)
	config.RegisterReader("ensemble.early_stop.similarity_threshold", applyEnsembleConfigOverrides)
	config.RegisterReader("ensemble.early_stop.window_size", applyEnsembleConfigOverrides)
}

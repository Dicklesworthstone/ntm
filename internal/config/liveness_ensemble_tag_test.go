//go:build ensemble_experimental
// +build ensemble_experimental

package config

// bd-6otuk ensemble disposition proof (tagged side): under
// -tags ensemble_experimental the internal/cli tag-gated claims file
// (liveness_claims_ensemble.go) must register a RegisterReader claim for
// every ensemble.* key whose only reader is tag-gated. The claims reach the
// registry through the blank import of internal/cli in
// liveness_claims_link_test.go, exactly like every other claim. Untagged
// builds cover the same keys with `permanent` allowlist entries (build-tag
// blind spot); TestConfigKeyLiveness enforces both sides.

import "testing"

func TestEnsembleTagGatedClaimsRegistered(t *testing.T) {
	wantClaimed := []string{
		// Tag-gated readers (liveness_claims_ensemble.go).
		"ensemble.synthesis.strategy",
		"ensemble.synthesis.min_confidence",
		"ensemble.synthesis.max_findings",
		"ensemble.synthesis.include_raw_outputs",
		"ensemble.synthesis.conflict_resolution",
		"ensemble.budget.synthesis",
		"ensemble.budget.context_pack",
		"ensemble.cache.ttl_minutes",
		"ensemble.cache.cache_dir",
		"ensemble.cache.max_entries",
		"ensemble.cache.share_across_modes",
		"ensemble.early_stop.enabled",
		"ensemble.early_stop.min_agents",
		"ensemble.early_stop.findings_threshold",
		"ensemble.early_stop.similarity_threshold",
		"ensemble.early_stop.window_size",
		// All-build readers (liveness_claims.go), also present when tagged.
		"ensemble.default_ensemble",
		"ensemble.agent_mix",
		"ensemble.assignment",
		"ensemble.allow_advanced",
		"ensemble.mode_tier_default",
		"ensemble.budget.total",
		"ensemble.budget.per_agent",
		"ensemble.cache.enabled",
	}

	readerMu.Lock()
	defer readerMu.Unlock()
	for _, key := range wantClaimed {
		if _, ok := readerRegistry[key]; !ok {
			t.Errorf("ensemble_experimental build must claim %s (missing from readerRegistry)", key)
		}
	}
}

package config

// Graduated ensemble disposition proof (bd-rc-track-c-surfaces-h2e1n.4):
// Every ensemble.* leaf key must be claimed unconditionally in default
// untagged builds via internal/ensemble/liveness_claims.go and
// internal/cli/liveness_claims*.go, with zero build-tag exemptions in
// ci/allowlists/config.txt.

import "testing"

func TestEnsembleClaimsRegisteredInDefaultBuild(t *testing.T) {
	wantClaimed := []string{
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
			t.Errorf("default build must claim %s (missing from readerRegistry)", key)
		}
	}
}

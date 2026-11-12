//go:build !liveness_audit

package ensemble

import "github.com/Dicklesworthstone/ntm/internal/config"

func init() {
	config.RegisterReader("ensemble.synthesis.min_confidence", ApplyTOMLConfig)
	config.RegisterReader("ensemble.synthesis.max_findings", ApplyTOMLConfig)
	config.RegisterReader("ensemble.synthesis.include_raw_outputs", ApplyTOMLConfig)
	config.RegisterReader("ensemble.synthesis.conflict_resolution", ApplyTOMLConfig)
	config.RegisterReader("ensemble.budget.synthesis", ApplyTOMLConfig)
	config.RegisterReader("ensemble.budget.context_pack", ApplyTOMLConfig)
	config.RegisterReader("ensemble.early_stop.enabled", EarlyStopConfigFromAppConfig)
	config.RegisterReader("ensemble.early_stop.min_agents", EarlyStopConfigFromAppConfig)
	config.RegisterReader("ensemble.early_stop.findings_threshold", EarlyStopConfigFromAppConfig)
	config.RegisterReader("ensemble.early_stop.similarity_threshold", EarlyStopConfigFromAppConfig)
	config.RegisterReader("ensemble.early_stop.window_size", EarlyStopConfigFromAppConfig)
}

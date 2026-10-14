package serve

import (
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// spawnPromptContextOptions binds an explicit API enrichment request to the
// freshly loaded launch-project policy. Clients select whether to enrich, not
// executable paths, workspace identity, or unbounded retrieval parameters.
// As on the CLI, disabled integrations retain the request flag so the shared
// engine reports a skip instead of silently bypassing the operator's policy.
func spawnPromptContextOptions(request robot.PromptContextOptions, cfg *config.Config) robot.PromptContextOptions {
	opts := robot.PromptContextOptions{WithCASS: request.WithCASS, WithMemory: request.WithMemory}
	if cfg == nil {
		return opts
	}
	if opts.WithCASS {
		query := robot.DefaultCASSConfig()
		filter := robot.DefaultFilterConfig()
		inject := robot.DefaultInjectConfig()
		query.Enabled = cfg.CASS.Enabled
		if cfg.CASS.BinaryPath != "" {
			query.BinaryPath = cfg.CASS.BinaryPath
		}
		if cfg.CASS.Timeout > 0 {
			query.Timeout = time.Duration(cfg.CASS.Timeout) * time.Second
		}
		cc := cfg.CASS.Context
		if cc.MaxSessions > 0 {
			query.MaxResults, filter.MaxItems = cc.MaxSessions, cc.MaxSessions
		}
		if cc.LookbackDays > 0 {
			query.MaxAgeDays, filter.MaxAgeDays = cc.LookbackDays, cc.LookbackDays
		}
		if cc.MinRelevance > 0 {
			query.MinRelevance, filter.MinRelevance = cc.MinRelevance, cc.MinRelevance
		}
		query.PreferSameProject, filter.PreferSameProject = cc.PreferSameProject, cc.PreferSameProject
		if cc.MaxTokens > 0 {
			inject.MaxTokens = cc.MaxTokens
		}
		if cc.SkipIfContextAbove > 0 {
			inject.SkipThreshold = int(cc.SkipIfContextAbove)
		}
		opts.CASSConfig, opts.FilterConfig, opts.InjectConfig = &query, &filter, &inject
	}
	if opts.WithMemory {
		inject := robot.DefaultCMInjectConfig()
		inject.Enabled = cfg.Memory.Enabled
		if cfg.Memory.SendMaxRules > 0 {
			inject.MaxRules = cfg.Memory.SendMaxRules
		}
		if cfg.Memory.SendBudgetTokens > 0 {
			inject.BudgetTokens = cfg.Memory.SendBudgetTokens
		}
		if cfg.Memory.QueryTimeoutSeconds > 0 {
			inject.QueryTimeout = time.Duration(cfg.Memory.QueryTimeoutSeconds) * time.Second
		}
		opts.MemoryInject = &inject
	}
	// EnrichAssignmentPrompt supplies the authoritative bead project/session
	// immediately before retrieval; never copy a client-selected workspace.
	return opts
}

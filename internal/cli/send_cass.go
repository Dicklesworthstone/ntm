package cli

// Send-time CASS context injection wiring (C10, bd-ws2-wire-or-delete-ykmcz.11).
//
// `--with-cass` / `--no-cass` on `ntm send` and `--robot-send` wire the
// long-dormant injection engines: the robot surface drives
// robot.SendOptions.WithCASS (whose engine and cass_injection envelope block
// were already built and tested), and the cobra surface drives the
// internal/cass engine directly. Flag/config/envelope conventions mirror
// --with-memory (internal/cli/robot_memory.go): the per-call flag alone
// drives injection, [cass.context] enabled=true makes injection the default,
// --no-cass always wins, and [cass] enabled=false degrades an explicit
// --with-cass to a recorded skip instead of failing the send. CASS being
// missing or wedged is enrichment lost, never a gate.

import (
	"time"

	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func init() {
	// G2 config-key liveness claims (bd-ws2-wire-or-delete-ykmcz.11): the
	// send-scoped [cass.context] keys are read here for both send surfaces.
	config.RegisterReader("cass.context.enabled", robotSendCASSOptions)
	config.RegisterReader("cass.context.max_sessions", robotSendCASSOptions)
	config.RegisterReader("cass.context.lookback_days", robotSendCASSOptions)
	config.RegisterReader("cass.context.max_tokens", robotSendCASSOptions)
	config.RegisterReader("cass.context.min_relevance", robotSendCASSOptions)
	config.RegisterReader("cass.context.skip_if_context_above", robotSendCASSOptions)
	config.RegisterReader("cass.context.prefer_same_project", robotSendCASSOptions)
}

// resolveSendCASSEnabled applies the shared flag/config precedence:
// --no-cass > --with-cass > [cass] enabled && [cass.context] enabled.
func resolveSendCASSEnabled(withFlag, noFlag bool, cfg *config.Config) bool {
	if noFlag {
		return false
	}
	if withFlag {
		return true
	}
	return cfg != nil && cfg.CASS.Enabled && cfg.CASS.Context.Enabled
}

// robotSendCASSOptions resolves the effective CASS injection state and the
// config-derived engine parameters for a robot send (--robot-send).
func robotSendCASSOptions(withFlag, noFlag bool, cfg *config.Config) (bool, *robot.CASSConfig, *robot.FilterConfig, *robot.InjectConfig) {
	enabled := resolveSendCASSEnabled(withFlag, noFlag, cfg)
	if cfg == nil {
		return enabled, nil, nil, nil
	}

	query := robot.DefaultCASSConfig()
	filter := robot.DefaultFilterConfig()
	inject := robot.DefaultInjectConfig()

	// [cass] enabled=false records a skip inside the engine rather than
	// vetoing the call (QueryCASS returns success with zero hits).
	query.Enabled = cfg.CASS.Enabled
	if cfg.CASS.BinaryPath != "" {
		query.BinaryPath = cfg.CASS.BinaryPath
	}
	if cfg.CASS.Timeout > 0 {
		query.Timeout = time.Duration(cfg.CASS.Timeout) * time.Second
	}

	cc := cfg.CASS.Context
	if cc.MaxSessions > 0 {
		query.MaxResults = cc.MaxSessions
		filter.MaxItems = cc.MaxSessions
	}
	if cc.LookbackDays > 0 {
		query.MaxAgeDays = cc.LookbackDays
		filter.MaxAgeDays = cc.LookbackDays
	}
	if cc.MinRelevance > 0 {
		query.MinRelevance = cc.MinRelevance
		filter.MinRelevance = cc.MinRelevance
	}
	query.PreferSameProject = cc.PreferSameProject
	filter.PreferSameProject = cc.PreferSameProject
	if cc.MaxTokens > 0 {
		inject.MaxTokens = cc.MaxTokens
	}
	if cc.SkipIfContextAbove > 0 {
		inject.SkipThreshold = int(cc.SkipIfContextAbove)
	}

	return enabled, &query, &filter, &inject
}

// sendCASSInjectionConfigs resolves the cobra `ntm send` CASS injection state
// and internal/cass engine parameters from the same flag/config precedence.
func sendCASSInjectionConfigs(withFlag, noFlag bool, cfg *config.Config) (bool, cass.CASSConfig, cass.FilterConfig, cass.InjectConfig) {
	enabled := resolveSendCASSEnabled(withFlag, noFlag, cfg)

	query := cass.DefaultCASSConfig()
	// internal/cass has no DefaultFilterConfig; mirror the robot engine's
	// defaults (robot.DefaultFilterConfig) for identical behavior. Topic
	// filtering carries its own defaults (disabled, neutral boosts).
	filter := cass.FilterConfig{
		MinRelevance:      0.7,
		MaxItems:          5,
		PreferSameProject: true,
		MaxAgeDays:        30,
		RecencyBoost:      0.3,
		TopicFilter:       cass.DefaultTopicFilterConfig(),
	}
	inject := cass.DefaultInjectConfig()
	if cfg == nil {
		return enabled, query, filter, inject
	}

	query.Enabled = cfg.CASS.Enabled
	if cfg.CASS.BinaryPath != "" {
		query.BinaryPath = cfg.CASS.BinaryPath
	}

	cc := cfg.CASS.Context
	if cc.MaxSessions > 0 {
		query.MaxResults = cc.MaxSessions
		filter.MaxItems = cc.MaxSessions
	}
	if cc.LookbackDays > 0 {
		query.MaxAgeDays = cc.LookbackDays
		filter.MaxAgeDays = cc.LookbackDays
	}
	if cc.MinRelevance > 0 {
		query.MinRelevance = cc.MinRelevance
		filter.MinRelevance = cc.MinRelevance
	}
	query.PreferSameProject = cc.PreferSameProject
	filter.PreferSameProject = cc.PreferSameProject
	if cc.MaxTokens > 0 {
		inject.MaxTokens = cc.MaxTokens
	}
	if cc.SkipIfContextAbove > 0 {
		inject.SkipThreshold = int(cc.SkipIfContextAbove)
	}

	return enabled, query, filter, inject
}

// sendCASSInjectionInfo converts an internal/cass injection outcome into the
// shared cass_injection envelope block (the same JSON contract the robot
// surface emits), recording a skip reason on the degraded path.
func sendCASSInjectionInfo(result cass.InjectionResult, query string, hits []cass.ScoredHit) *robot.CASSInjectionInfo {
	info := &robot.CASSInjectionInfo{
		Enabled:       result.Metadata.Enabled,
		Query:         query,
		ItemsFound:    result.Metadata.ItemsFound,
		ItemsInjected: result.Metadata.ItemsInjected,
		TokensAdded:   result.Metadata.TokensAdded,
		SkippedReason: result.Metadata.SkippedReason,
		Sources:       make([]robot.CASSSource, 0, len(hits)),
	}
	if !result.Success && info.SkippedReason == "" && result.Error != "" {
		info.SkippedReason = result.Error
	}
	now := time.Now()
	for _, hit := range hits {
		sessionDate := cass.ExtractSessionDate(hit.SourcePath)
		ageDays := 0
		if !sessionDate.IsZero() {
			ageDays = int(now.Sub(sessionDate).Hours() / 24)
		}
		info.Sources = append(info.Sources, robot.CASSSource{
			Session:   cass.ExtractSessionName(hit.SourcePath),
			Relevance: int(hit.ComputedScore * 100),
			AgeDays:   ageDays,
		})
	}
	return info
}

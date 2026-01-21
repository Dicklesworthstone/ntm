// Package robot provides machine-readable output for AI agents.
// agent_health.go implements the --robot-agent-health command for comprehensive health checks.
package robot

import (
	"context"
	"strconv"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/caut"
)

// =============================================================================
// Robot Agent-Health Command (bd-2pwzf)
// =============================================================================
//
// The agent-health command combines local agent state (from parser) with
// provider usage data (from caut) to provide a comprehensive health picture.
//
// This enables sophisticated controller decisions like:
// - "Agent is idle but account is at 90% - wait before sending more work"
// - "Agent looks idle but provider is at capacity - switch accounts"

// AgentHealthOptions configures the agent-health command.
type AgentHealthOptions struct {
	Session       string        // Session name (required)
	Panes         []int         // Pane indices to check (empty = all non-control panes)
	LinesCaptured int           // Number of lines to capture (default: 100)
	IncludeCaut   bool          // Whether to query caut for provider usage (default: true)
	CautTimeout   time.Duration // Timeout for caut queries (default: 10s)
	Verbose       bool          // Include raw sample in output
}

// DefaultAgentHealthOptions returns sensible defaults.
func DefaultAgentHealthOptions() AgentHealthOptions {
	return AgentHealthOptions{
		LinesCaptured: 100,
		IncludeCaut:   true,
		CautTimeout:   10 * time.Second,
		Verbose:       false,
	}
}

// LocalStateInfo contains the parsed local agent state.
type LocalStateInfo struct {
	IsWorking        bool           `json:"is_working"`
	IsIdle           bool           `json:"is_idle"`
	IsRateLimited    bool           `json:"is_rate_limited"`
	IsContextLow     bool           `json:"is_context_low"`
	ContextRemaining *float64       `json:"context_remaining,omitempty"`
	Confidence       float64        `json:"confidence"`
	Indicators       WorkIndicators `json:"indicators"`
}

// ProviderUsageInfo contains the caut provider usage data.
type ProviderUsageInfo struct {
	Provider      string             `json:"provider"`
	Account       string             `json:"account,omitempty"`
	Source        string             `json:"source,omitempty"`
	PrimaryWindow *RateWindowInfo    `json:"primary_window,omitempty"`
	Status        *ProviderStatusInfo `json:"status,omitempty"`
}

// RateWindowInfo contains rate window details from caut.
type RateWindowInfo struct {
	UsedPercent      *float64 `json:"used_percent,omitempty"`
	WindowMinutes    *int     `json:"window_minutes,omitempty"`
	ResetsAt         string   `json:"resets_at,omitempty"`
	ResetDescription string   `json:"reset_description,omitempty"`
}

// ProviderStatusInfo contains provider operational status.
type ProviderStatusInfo struct {
	Operational bool   `json:"operational"`
	Message     string `json:"message,omitempty"`
}

// PaneHealthStatus contains the full health status for a single pane.
type PaneHealthStatus struct {
	AgentType            string             `json:"agent_type"`
	LocalState           LocalStateInfo     `json:"local_state"`
	ProviderUsage        *ProviderUsageInfo `json:"provider_usage,omitempty"`
	HealthScore          int                `json:"health_score"`
	HealthGrade          string             `json:"health_grade"`
	Issues               []string           `json:"issues"`
	Recommendation       string             `json:"recommendation"`
	RecommendationReason string             `json:"recommendation_reason"`
	RawSample            string             `json:"raw_sample,omitempty"` // Only with --verbose
}

// ProviderStats contains aggregated statistics for a provider.
type ProviderStats struct {
	Accounts       int     `json:"accounts"`
	AvgUsedPercent float64 `json:"avg_used_percent"`
	PanesUsing     []int   `json:"panes_using"`
}

// FleetHealthSummary contains overall health statistics across all panes.
type FleetHealthSummary struct {
	TotalPanes     int     `json:"total_panes"`
	HealthyCount   int     `json:"healthy_count"`
	WarningCount   int     `json:"warning_count"`
	CriticalCount  int     `json:"critical_count"`
	AvgHealthScore float64 `json:"avg_health_score"`
	OverallGrade   string  `json:"overall_grade"`
}

// AgentHealthQuery contains query parameters for reproducibility.
type AgentHealthQuery struct {
	PanesRequested []int `json:"panes_requested"`
	LinesCaptured  int   `json:"lines_captured"`
	CautEnabled    bool  `json:"caut_enabled"`
}

// AgentHealthOutput is the response for --robot-agent-health.
type AgentHealthOutput struct {
	RobotResponse
	Session         string                     `json:"session"`
	Query           AgentHealthQuery           `json:"query"`
	CautAvailable   bool                       `json:"caut_available"`
	Panes           map[string]PaneHealthStatus `json:"panes"`
	ProviderSummary map[string]ProviderStats   `json:"provider_summary"`
	FleetHealth     FleetHealthSummary         `json:"fleet_health"`
}

// PrintAgentHealth outputs the health state for specified panes in a session.
func PrintAgentHealth(opts AgentHealthOptions) error {
	output, err := AgentHealth(opts)
	if err != nil {
		// AgentHealth already sets error fields on output
		return encodeJSON(output)
	}
	return encodeJSON(output)
}

// AgentHealth is the core function for programmatic use.
// It returns the structured result instead of printing JSON.
func AgentHealth(opts AgentHealthOptions) (*AgentHealthOutput, error) {
	output := &AgentHealthOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		Query: AgentHealthQuery{
			PanesRequested: opts.Panes,
			LinesCaptured:  opts.LinesCaptured,
			CautEnabled:    opts.IncludeCaut,
		},
		Panes:           make(map[string]PaneHealthStatus),
		ProviderSummary: make(map[string]ProviderStats),
		FleetHealth:     FleetHealthSummary{},
	}

	// Step 1: Get local state for all panes using IsWorking
	isWorkingOpts := IsWorkingOptions{
		Session:       opts.Session,
		Panes:         opts.Panes,
		LinesCaptured: opts.LinesCaptured,
		Verbose:       opts.Verbose,
	}

	isWorkingResult, err := IsWorking(isWorkingOpts)
	if err != nil {
		output.Success = false
		output.Error = err.Error()
		output.ErrorCode = isWorkingResult.ErrorCode
		output.Hint = isWorkingResult.Hint
		return output, err
	}

	// Step 2: Query caut for provider usage (if enabled)
	var cautClient *caut.CachedClient
	providerCache := make(map[string]*caut.ProviderPayload)

	if opts.IncludeCaut {
		client := caut.NewClient(caut.WithTimeout(opts.CautTimeout))
		if client.IsInstalled() {
			cautClient = caut.NewCachedClient(client, 5*time.Minute)
			output.CautAvailable = true

			// Pre-fetch all supported providers
			ctx, cancel := context.WithTimeout(context.Background(), opts.CautTimeout)
			defer cancel()

			for _, provider := range caut.SupportedProviders() {
				if payload, err := cautClient.GetProviderUsage(ctx, provider); err == nil {
					providerCache[provider] = payload
				}
			}
		}
	}

	// Step 3: Build health status for each pane
	totalScore := 0
	for paneStr, workStatus := range isWorkingResult.Panes {
		// Convert IsWorking result to our local state structure
		localState := LocalStateInfo{
			IsWorking:        workStatus.IsWorking,
			IsIdle:           workStatus.IsIdle,
			IsRateLimited:    workStatus.IsRateLimited,
			IsContextLow:     workStatus.IsContextLow,
			ContextRemaining: workStatus.ContextRemaining,
			Confidence:       workStatus.Confidence,
			Indicators:       workStatus.Indicators,
		}

		healthStatus := PaneHealthStatus{
			AgentType:  workStatus.AgentType,
			LocalState: localState,
			Issues:     []string{},
		}

		// Get provider usage if available
		var providerUsage *caut.ProviderPayload
		if output.CautAvailable {
			provider := caut.AgentTypeToProvider(workStatus.AgentType)
			if provider != "" {
				if cached, ok := providerCache[provider]; ok {
					providerUsage = cached
					healthStatus.ProviderUsage = convertProviderUsage(cached)

					// Track in provider summary
					paneNum, _ := strconv.Atoi(paneStr)
					updateProviderSummary(output.ProviderSummary, provider, cached, paneNum)
				}
			}
		}

		// Calculate health score and recommendation
		healthStatus.HealthScore = CalculateHealthScore(&workStatus, providerUsage)
		healthStatus.HealthGrade = HealthGrade(healthStatus.HealthScore)
		healthStatus.Issues = CollectIssues(&workStatus, providerUsage)
		rec, reason := DeriveHealthRecommendation(&workStatus, providerUsage, healthStatus.HealthScore)
		healthStatus.Recommendation = string(rec)
		healthStatus.RecommendationReason = reason

		// Include raw sample if verbose
		if opts.Verbose {
			healthStatus.RawSample = workStatus.RawSample
		}

		output.Panes[paneStr] = healthStatus
		totalScore += healthStatus.HealthScore

		// Update fleet health counts
		switch {
		case healthStatus.HealthScore >= 70:
			output.FleetHealth.HealthyCount++
		case healthStatus.HealthScore >= 50:
			output.FleetHealth.WarningCount++
		default:
			output.FleetHealth.CriticalCount++
		}
	}

	// Step 4: Calculate fleet health summary
	output.FleetHealth.TotalPanes = len(output.Panes)
	if output.FleetHealth.TotalPanes > 0 {
		output.FleetHealth.AvgHealthScore = float64(totalScore) / float64(output.FleetHealth.TotalPanes)
	}
	output.FleetHealth.OverallGrade = HealthGrade(int(output.FleetHealth.AvgHealthScore))
	output.Query.PanesRequested = isWorkingResult.Query.PanesRequested

	return output, nil
}

// convertProviderUsage converts caut.ProviderPayload to our ProviderUsageInfo.
func convertProviderUsage(payload *caut.ProviderPayload) *ProviderUsageInfo {
	if payload == nil {
		return nil
	}

	info := &ProviderUsageInfo{
		Provider: payload.Provider,
		Source:   payload.Source,
	}

	if payload.Account != nil {
		info.Account = *payload.Account
	}

	// Convert primary rate window
	if payload.Usage.PrimaryRateWindow != nil {
		window := payload.Usage.PrimaryRateWindow
		info.PrimaryWindow = &RateWindowInfo{
			UsedPercent:      window.UsedPercent,
			WindowMinutes:    window.WindowMinutes,
			ResetDescription: payload.GetResetDescription(),
		}
		if window.ResetsAt != nil {
			info.PrimaryWindow.ResetsAt = window.ResetsAt.Format(time.RFC3339)
		}
	}

	// Convert status
	if payload.Status != nil {
		info.Status = &ProviderStatusInfo{
			Operational: payload.Status.Operational,
		}
		if payload.Status.Message != nil {
			info.Status.Message = *payload.Status.Message
		}
	}

	return info
}

// updateProviderSummary updates the provider summary with usage data.
func updateProviderSummary(summary map[string]ProviderStats, provider string, payload *caut.ProviderPayload, paneNum int) {
	stats, exists := summary[provider]
	if !exists {
		stats = ProviderStats{
			PanesUsing: []int{},
		}
	}

	// Add this pane if not already tracked
	found := false
	for _, p := range stats.PanesUsing {
		if p == paneNum {
			found = true
			break
		}
	}
	if !found {
		stats.PanesUsing = append(stats.PanesUsing, paneNum)
	}

	// Update usage stats
	if pct := payload.UsedPercent(); pct != nil {
		// Simple running average calculation
		currentTotal := stats.AvgUsedPercent * float64(stats.Accounts)
		stats.Accounts++
		stats.AvgUsedPercent = (currentTotal + *pct) / float64(stats.Accounts)
	}

	summary[provider] = stats
}


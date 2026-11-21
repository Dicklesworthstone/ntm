// Package robot provides machine-readable output for AI agents.
// agent_health.go implements the --robot-agent-health command for comprehensive health checks.
package robot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/caut"
	"github.com/Dicklesworthstone/ntm/internal/integrations/pt"
	"github.com/Dicklesworthstone/ntm/internal/tools"
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
	Panes         []int         // Legacy bare pane indices (empty = all non-control panes)
	PaneSelectors []string      // N, W.P, or %N selectors; takes precedence over Panes
	LinesCaptured int           // Number of lines to capture (default: 100)
	IncludeCaut   bool          // Whether to query caut for provider usage (default: true)
	IncludePT     bool          // Whether to query process_triage for health states (default: true)
	CautTimeout   time.Duration // Timeout for caut queries (default: 10s)
	PTTimeout     time.Duration // Timeout for PT queries (default: 10s)
	Verbose       bool          // Include raw sample in output
}

// DefaultAgentHealthOptions returns sensible defaults.
func DefaultAgentHealthOptions() AgentHealthOptions {
	return AgentHealthOptions{
		LinesCaptured: 100,
		IncludeCaut:   true,
		IncludePT:     true,
		CautTimeout:   10 * time.Second,
		PTTimeout:     10 * time.Second,
		Verbose:       false,
	}
}

// LocalStateInfo contains the parsed local agent state.
type LocalStateInfo struct {
	IsWorking             bool           `json:"is_working"`
	IsIdle                bool           `json:"is_idle"`
	IsRateLimited         bool           `json:"is_rate_limited"`
	IsContextLow          bool           `json:"is_context_low"`
	ContextRemaining      *float64       `json:"context_remaining,omitempty"`
	Confidence            float64        `json:"confidence"`
	Indicators            WorkIndicators `json:"indicators"`
	ObservationState      string         `json:"observation_state"`
	ObservationFreshness  string         `json:"observation_freshness"`
	ObservationObservedAt string         `json:"observation_observed_at"`
	ObservationError      string         `json:"observation_error,omitempty"`
	SafeToDispatch        bool           `json:"safe_to_dispatch"`
}

// ProviderUsageInfo contains the caut provider usage data.
type ProviderUsageInfo struct {
	Provider      string              `json:"provider"`
	Account       string              `json:"account,omitempty"`
	Source        string              `json:"source,omitempty"`
	PrimaryWindow *RateWindowInfo     `json:"primary_window,omitempty"`
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

// PTHealthSignals contains process_triage signals for an agent.
type PTHealthSignals struct {
	CPUPercent    *float64 `json:"cpu_percent,omitempty"` // CPU usage percentage (if available)
	IOActive      bool     `json:"io_active"`             // Whether IO is active
	NetworkActive bool     `json:"network_active"`        // Whether network is active (from rano)
	OutputRecent  bool     `json:"output_recent"`         // Whether there was recent output
}

// PTHealthInfo contains process_triage classification data for a pane.
type PTHealthInfo struct {
	Classification         string           `json:"classification"`             // useful, waiting, idle, stuck, zombie, unknown
	Since                  string           `json:"since,omitempty"`            // RFC3339 timestamp when classification started
	DurationSeconds        int              `json:"duration_seconds,omitempty"` // Seconds in current state
	Signals                *PTHealthSignals `json:"signals,omitempty"`          // Underlying signals
	Confidence             float64          `json:"confidence"`                 // 0.0 to 1.0
	Reason                 string           `json:"reason,omitempty"`           // Classification reason
	Source                 string           `json:"source,omitempty"`
	ObservedAt             string           `json:"observed_at,omitempty"`
	Recommendation         string           `json:"recommendation,omitempty"` // PT advice, never authorization to act
	AbandonmentProbability *float64         `json:"abandonment_probability,omitempty"`
}

// PTHealthSummary contains counts by classification.
type PTHealthSummary struct {
	Useful  int `json:"useful"`
	Waiting int `json:"waiting"`
	Idle    int `json:"idle"`
	Stuck   int `json:"stuck"`
	Zombie  int `json:"zombie"`
	Unknown int `json:"unknown"`
}

// PaneHealthStatus contains the full health status for a single pane.
type PaneHealthStatus struct {
	AgentType            string             `json:"agent_type"`
	LocalState           LocalStateInfo     `json:"local_state"`
	ProviderUsage        *ProviderUsageInfo `json:"provider_usage,omitempty"`
	PTHealth             *PTHealthInfo      `json:"pt_health,omitempty"` // process_triage health state
	HealthScore          int                `json:"health_score"`
	HealthGrade          string             `json:"health_grade"`
	Issues               []string           `json:"issues"`
	Recommendation       string             `json:"recommendation"`
	RecommendationReason string             `json:"recommendation_reason"`
	RawSample            string             `json:"raw_sample,omitempty"` // Only with --verbose
}

// ProviderStats contains aggregated statistics for a provider.
type ProviderStats struct {
	Accounts       int      `json:"accounts"`
	AvgUsedPercent float64  `json:"avg_used_percent"`
	PanesUsing     []string `json:"panes_using"`
}

// FleetHealthSummary contains overall health statistics across the graded
// agent panes. Non-agent panes (AgentHealthOutput.NonAgentPanes) are not
// counted in TotalPanes; with no agent panes OverallGrade is
// FleetGradeNoAgents rather than "F".
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
	PanesRequested     []string `json:"panes_requested"`
	SelectorsRequested []string `json:"selectors_requested,omitempty"`
	LinesCaptured      int      `json:"lines_captured"`
	CautEnabled        bool     `json:"caut_enabled"`
	PTEnabled          bool     `json:"pt_enabled"`
}

// PTAvailability describes whether process_triage can supply health data for
// this response. A binary alone is not enough: a resident or one-shot sample
// must provide observations for the current panes.
type PTAvailability string

const (
	PTAvailabilityDisabled          PTAvailability = "disabled"
	PTAvailabilityUnavailable       PTAvailability = "unavailable"
	PTAvailabilityMonitorNotRunning PTAvailability = "monitor_not_running"
	PTAvailabilityAvailable         PTAvailability = "available"
	PTAvailabilityNoObservations    PTAvailability = "no_observations"
	PTAvailabilitySampleFailed      PTAvailability = "sample_failed"
	PTAvailabilityTimedOut          PTAvailability = "timed_out"
)

// AgentHealthOutput is the response for --robot-agent-health.
type AgentHealthOutput struct {
	RobotResponse
	Session           string                      `json:"session"`
	Query             AgentHealthQuery            `json:"query"`
	CautAvailable     bool                        `json:"caut_available"`
	PTAvailable       bool                        `json:"pt_available"` // True only when PT observations are available.
	PTStatus          PTAvailability              `json:"pt_status"`
	PTObservationMode string                      `json:"pt_observation_mode,omitempty"` // monitor or snapshot
	Panes             map[string]PaneHealthStatus `json:"panes"`
	// NonAgentPanes lists selected panes that run no agent: a plain user
	// shell, or a pane ntm cannot attribute to an agent CLI. They carry no
	// agent health, so they are kept out of Panes and FleetHealth instead of
	// being graded F for lacking an agent prompt (ntm#335).
	NonAgentPanes   map[string]NonAgentPane  `json:"non_agent_panes,omitempty"`
	ProviderSummary map[string]ProviderStats `json:"provider_summary"`
	PTSummary       *PTHealthSummary         `json:"pt_summary,omitempty"`
	FleetHealth     FleetHealthSummary       `json:"fleet_health"`
}

// NonAgentPane is a selected pane that is not running an agent.
type NonAgentPane struct {
	AgentType string `json:"agent_type"`
	Reason    string `json:"reason"`
}

// PrintAgentHealth outputs the health state for specified panes in a session
// and returns the process exit code (0 on success, nonzero when the result
// reports success:false — see ExitCodeForResponse and ntm#207). The failure
// envelope is always emitted as JSON so callers still receive the payload.
func PrintAgentHealth(opts AgentHealthOptions) int {
	output, err := GetAgentHealth(opts)
	if err != nil {
		// GetAgentHealth returns a partially-populated output alongside an
		// unexpected internal error; force the failure envelope so the emitted
		// JSON and the exit code agree.
		if output == nil {
			response := NewErrorResponse(err, ErrCodeInternalError, "")
			return printLegacyRobotOutput(&response, response, 1, "robot agent health failed")
		}
		output.Success = false
		if output.Error == "" {
			output.Error = err.Error()
		}
		if output.ErrorCode == "" {
			output.ErrorCode = ErrCodeInternalError
		}
		return printLegacyRobotOutput(output, output.RobotResponse, 1, "robot agent health failed")
	}
	return printLegacyRobotOutput(output, output.RobotResponse, ExitCodeForResponse(output.RobotResponse), "robot agent health failed")
}

// GetAgentHealth returns the health state for specified panes in a session.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetAgentHealth(opts AgentHealthOptions) (*AgentHealthOutput, error) {
	output := &AgentHealthOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		Query: AgentHealthQuery{
			SelectorsRequested: append([]string(nil), opts.PaneSelectors...),
			LinesCaptured:      opts.LinesCaptured,
			CautEnabled:        opts.IncludeCaut,
			PTEnabled:          opts.IncludePT,
		},
		PTStatus:        PTAvailabilityDisabled,
		Panes:           make(map[string]PaneHealthStatus),
		ProviderSummary: make(map[string]ProviderStats),
		FleetHealth:     FleetHealthSummary{},
	}

	// Step 1: Get local state for all panes using IsWorking
	isWorkingOpts := IsWorkingOptions{
		Session:       opts.Session,
		Panes:         opts.Panes,
		PaneSelectors: opts.PaneSelectors,
		LinesCaptured: opts.LinesCaptured,
		Verbose:       opts.Verbose,
	}

	isWorkingResult, err := GetIsWorking(context.Background(), isWorkingOpts)
	if err != nil {
		return output, err
	}
	if !isWorkingResult.Success {
		output.Success = false
		output.Error = isWorkingResult.Error
		output.ErrorCode = isWorkingResult.ErrorCode
		output.Hint = isWorkingResult.Hint
		return output, nil
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

	// Step 2.5: Reuse a resident sample, or obtain one bounded passive sample
	// for a standalone CLI/API reader. Neither path starts a background worker.
	var ptStates map[string]*pt.AgentState
	var ptSummary *PTHealthSummary
	if opts.IncludePT {
		output.PTStatus = PTAvailabilityNoObservations
		if hasPTHealthTargets(isWorkingResult.Panes) {
			timeout := opts.PTTimeout
			if timeout <= 0 {
				timeout = DefaultAgentHealthOptions().PTTimeout
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			ptAdapter := tools.NewPTAdapter()
			binaryAvailable := ptAdapter.IsAvailable(ctx)
			monitor := pt.GetGlobalMonitor()
			monitorRunning := monitor.Running()
			output.PTStatus, output.PTAvailable = ptAvailability(true, binaryAvailable, monitorRunning)
			if binaryAvailable {
				if monitorRunning {
					output.PTObservationMode = "monitor"
					ptStates = monitor.GetAllStates()
				} else {
					output.PTObservationMode = "snapshot"
					var sampleErr error
					ptStates, sampleErr = pt.SampleSession(ctx, opts.Session)
					if sampleErr != nil {
						output.PTStatus, output.PTAvailable = PTAvailabilitySampleFailed, false
						if errors.Is(sampleErr, context.DeadlineExceeded) || errors.Is(sampleErr, tools.ErrTimeout) {
							output.PTStatus = PTAvailabilityTimedOut
						}
					} else {
						output.PTStatus, output.PTAvailable = PTAvailabilityAvailable, true
					}
				}
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				output.PTStatus, output.PTAvailable = PTAvailabilityTimedOut, false
				ptStates = nil
			}
			if output.PTAvailable {
				ptSummary = &PTHealthSummary{}
			}
			cancel()
		}
	}
	// Step 3: Build health status for each pane
	totalScore := 0
	for paneStr, workStatus := range isWorkingResult.Panes {
		if nonAgent, ok := nonAgentPaneFor(workStatus); ok {
			if output.NonAgentPanes == nil {
				output.NonAgentPanes = make(map[string]NonAgentPane)
			}
			output.NonAgentPanes[paneStr] = nonAgent
			continue
		}

		// Convert IsWorking result to our local state structure
		localState := LocalStateInfo{
			IsWorking:             workStatus.IsWorking,
			IsIdle:                workStatus.IsIdle,
			IsRateLimited:         workStatus.IsRateLimited,
			IsContextLow:          workStatus.IsContextLow,
			ContextRemaining:      workStatus.ContextRemaining,
			Confidence:            workStatus.Confidence,
			Indicators:            workStatus.Indicators,
			ObservationState:      workStatus.ObservationState,
			ObservationFreshness:  workStatus.ObservationFreshness,
			ObservationObservedAt: workStatus.ObservationObservedAt,
			ObservationError:      workStatus.ObservationError,
			SafeToDispatch:        workStatus.SafeToDispatch,
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
					updateProviderSummary(output.ProviderSummary, provider, cached, paneStr)
				}
			}
		}

		// Get PT health state if available
		if output.PTAvailable && ptStates != nil {
			// Try to find state by pane identifier (e.g., "myproject__cc_1")
			if state := findPTState(ptStates, opts.Session, paneStr, workStatus.AgentType); ptHealthMatchesObservation(state, workStatus) {
				healthStatus.PTHealth = convertPTState(state, workStatus.IsWorking)
				// Update PT summary
				if ptSummary != nil {
					updatePTSummary(ptSummary, state.Classification)
				}
			}
		}

		// Calculate health score and recommendation
		if !paneObservationUsableForHealth(workStatus) {
			healthStatus.HealthScore = 0
			healthStatus.HealthGrade = HealthGrade(healthStatus.HealthScore)
			healthStatus.Issues = append(healthStatus.Issues, "Current pane observation unavailable")
			healthStatus.Recommendation = string(RecommendMonitor)
			healthStatus.RecommendationReason = "Live state is unavailable; inspect the pane before acting"
		} else {
			healthStatus.HealthScore = CalculateHealthScore(&workStatus, providerUsage)
			healthStatus.HealthGrade = HealthGrade(healthStatus.HealthScore)
			healthStatus.Issues = CollectIssues(&workStatus, providerUsage)
			rec, reason := DeriveHealthRecommendation(&workStatus, providerUsage, healthStatus.HealthScore)
			healthStatus.Recommendation = string(rec)
			healthStatus.RecommendationReason = reason
		}

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
	finalizeFleetHealth(&output.FleetHealth, len(output.Panes), totalScore)
	output.Query.PanesRequested = isWorkingResult.Query.PanesRequested

	// Include PT summary if we have PT data
	if output.PTAvailable && ptSummary != nil {
		if ptSummary.Useful+ptSummary.Waiting+ptSummary.Idle+ptSummary.Stuck+ptSummary.Zombie+ptSummary.Unknown == 0 {
			// A running process with no matching sample is not evidence about
			// this selection. In particular a failed monitor poll clears data.
			output.PTStatus, output.PTAvailable = PTAvailabilityNoObservations, false
		} else {
			output.PTSummary = ptSummary
		}
	}

	return output, nil
}

// FleetGradeNoAgents is the overall_grade of a selection with no graded agent
// panes, such as a session of plain shells (ntm#335). An average over zero
// panes is not a failing grade.
const FleetGradeNoAgents = "N/A"

// finalizeFleetHealth fills in the fleet totals from the graded agent panes.
func finalizeFleetHealth(fleet *FleetHealthSummary, gradedPanes, totalScore int) {
	fleet.TotalPanes = gradedPanes
	if gradedPanes == 0 {
		fleet.AvgHealthScore = 0
		fleet.OverallGrade = FleetGradeNoAgents
		return
	}
	fleet.AvgHealthScore = float64(totalScore) / float64(gradedPanes)
	fleet.OverallGrade = HealthGrade(int(fleet.AvgHealthScore))
}

// nonAgentPaneFor reports whether a pane is a non-agent pane by its
// tmux-recorded type. A pane missing from the observation has no recorded
// type and is not exempted: it keeps the "observation unavailable" grade.
func nonAgentPaneFor(workStatus PaneWorkStatus) (NonAgentPane, bool) {
	if workStatus.paneType == "" || !isNonAgentPaneType(workStatus.paneType) {
		return NonAgentPane{}, false
	}
	paneType := normalizeAgentType(workStatus.paneType)
	reason := "pane runs a user shell, not an agent"
	if paneType != "user" {
		reason = "no agent CLI detected in this pane"
	}
	return NonAgentPane{AgentType: paneType, Reason: reason}, true
}

func ptAvailability(enabled, binaryAvailable, monitorRunning bool) (PTAvailability, bool) {
	if !enabled {
		return PTAvailabilityDisabled, false
	}
	if !binaryAvailable {
		return PTAvailabilityUnavailable, false
	}
	if !monitorRunning {
		return PTAvailabilityMonitorNotRunning, false
	}
	return PTAvailabilityAvailable, true
}

func paneObservationUsableForHealth(workStatus PaneWorkStatus) bool {
	return workStatus.ObservationFreshness == "fresh" &&
		workStatus.ObservationState != "unknown" &&
		workStatus.ObservationError == ""
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
			Operational: payload.Status.Operational(),
		}
		if payload.Status.Description != nil {
			info.Status.Message = *payload.Status.Description
		}
	}

	return info
}

// updateProviderSummary updates the provider summary with usage data.
func updateProviderSummary(summary map[string]ProviderStats, provider string, payload *caut.ProviderPayload, paneTarget string) {
	stats, exists := summary[provider]
	if !exists {
		stats = ProviderStats{
			PanesUsing: []string{},
		}
	}

	// Add this pane if not already tracked
	found := false
	for _, p := range stats.PanesUsing {
		if p == paneTarget {
			found = true
			break
		}
	}
	if !found {
		stats.PanesUsing = append(stats.PanesUsing, paneTarget)
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

// findPTState resolves live monitor samples by session and physical topology,
// or by exact tmux ID. A bare index matching several windows is ambiguous.
// Metadata-bearing samples never fall back to mutable title/suffix guesses.
func findPTState(ptStates map[string]*pt.AgentState, session, paneStr, agentType string) *pt.AgentState {
	if ptStates == nil {
		return nil
	}
	var matched *pt.AgentState
	hasTopology := false
	for key, state := range ptStates {
		if state == nil || state.Session == "" {
			continue
		}
		hasTopology = true
		if state.Session != session || state.Pane != key || !strings.HasPrefix(key, "%") {
			continue
		}
		if paneStr != key && paneStr != fmt.Sprintf("%d.%d", state.WindowIndex, state.PaneIndex) &&
			paneStr != fmt.Sprint(state.PaneIndex) {
			continue
		}
		if matched != nil {
			return nil
		}
		matched = state
	}
	if hasTopology {
		return matched
	}

	// Try direct pane string match first
	if state, ok := ptStates[paneStr]; ok {
		return state
	}

	// Try session__agenttype_pane pattern (e.g., "myproject__cc_1")
	agentPrefix := ""
	switch agent.AgentType(agentType).Canonical() {
	case agent.AgentTypeClaudeCode:
		agentPrefix = "cc"
	case agent.AgentTypeCodex:
		agentPrefix = "cod"
	case agent.AgentTypeGemini:
		agentPrefix = "gmi"
	case agent.AgentTypeAntigravity:
		agentPrefix = "agy"
	case agent.AgentTypeGrok:
		agentPrefix = "grok"
	case agent.AgentTypeCursor:
		agentPrefix = "cursor"
	case agent.AgentTypeWindsurf:
		agentPrefix = "windsurf"
	case agent.AgentTypeAider:
		agentPrefix = "aider"
	case agent.AgentTypeOpencode:
		agentPrefix = "oc"
	case agent.AgentTypeOmp:
		agentPrefix = "omp"
	case agent.AgentTypePi:
		agentPrefix = "pi"
	case agent.AgentTypeOllama:
		agentPrefix = "ollama"
	}

	if agentPrefix != "" {
		key := fmt.Sprintf("%s__%s_%s", session, agentPrefix, paneStr)
		if state, ok := ptStates[key]; ok {
			return state
		}
	}

	// Try matching by pane number suffix
	for key, state := range ptStates {
		if strings.HasSuffix(key, "_"+paneStr) {
			return state
		}
	}

	return nil
}

// convertPTState converts a pt.AgentState to PTHealthInfo for JSON output.
func convertPTState(state *pt.AgentState, isWorking bool) *PTHealthInfo {
	if state == nil {
		return nil
	}

	info := &PTHealthInfo{
		Classification:  string(state.Classification),
		Confidence:      state.Confidence,
		DurationSeconds: int(time.Since(state.Since).Seconds()),
	}
	if !state.LastCheck.IsZero() {
		info.ObservedAt = state.LastCheck.UTC().Format(time.RFC3339Nano)
	}

	if !state.Since.IsZero() {
		info.Since = state.Since.Format(time.RFC3339)
	}

	// Extract signals from recent history if available
	if len(state.History) > 0 {
		latest := state.History[len(state.History)-1]
		info.Reason = latest.Reason
		info.Source = latest.Source
		info.Recommendation = latest.Recommendation
		if latest.Recommendation != "" {
			probability := latest.AbandonmentProbability
			info.AbandonmentProbability = &probability
		}
		info.Signals = &PTHealthSignals{
			NetworkActive: latest.NetworkActive,
			OutputRecent:  isWorking, // Use local state as proxy for output activity
		}
	}

	return info
}

// updatePTSummary updates the PT health summary with a classification.
func updatePTSummary(summary *PTHealthSummary, classification pt.Classification) {
	if summary == nil {
		return
	}

	switch classification {
	case pt.ClassUseful:
		summary.Useful++
	case pt.ClassWaiting:
		summary.Waiting++
	case pt.ClassIdle:
		summary.Idle++
	case pt.ClassStuck:
		summary.Stuck++
	case pt.ClassZombie:
		summary.Zombie++
	default:
		summary.Unknown++
	}
}

// Package robot provides machine-readable output for AI agents.
// diagnose.go implements the --robot-diagnose command for comprehensive health diagnosis.
package robot

import (
	"fmt"
	"sort"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// Robot Diagnose Command (bd-31e1f)
// =============================================================================
//
// The diagnose command provides a single comprehensive health check answering:
// "What's wrong and how do I fix it?"
//
// Output includes:
//   - overall_health: healthy, degraded, or critical
//   - summary: counts by health state
//   - panes: pane indices grouped by health state
//   - recommendations: actionable fix commands per pane
//   - auto_fix_available: whether --fix can help
//   - auto_fix_command: the command to run for auto-fix

// DiagnoseOutput is the response for --robot-diagnose
type DiagnoseOutput struct {
	RobotResponse
	Session         string                   `json:"session"`
	OverallHealth   string                   `json:"overall_health"` // healthy, degraded, critical
	Summary         DiagnoseSummary          `json:"summary"`
	Panes           DiagnosePanes            `json:"panes"`
	Recommendations []DiagnoseRecommendation `json:"recommendations"`
	AutoFixAvail    bool                     `json:"auto_fix_available"`
	AutoFixCommand  string                   `json:"auto_fix_command,omitempty"`
}

// DiagnoseSummary contains counts by health state
type DiagnoseSummary struct {
	TotalPanes   int `json:"total_panes"`
	Healthy      int `json:"healthy"`
	RateLimited  int `json:"rate_limited"`
	Unresponsive int `json:"unresponsive"`
	Crashed      int `json:"crashed"`
	Unknown      int `json:"unknown"`
}

// DiagnosePanes groups pane indices by health state
type DiagnosePanes struct {
	Healthy      []int `json:"healthy"`
	RateLimited  []int `json:"rate_limited"`
	Unresponsive []int `json:"unresponsive"`
	Crashed      []int `json:"crashed"`
	Unknown      []int `json:"unknown"`
}

// DiagnoseRecommendation is an actionable fix for a pane issue
type DiagnoseRecommendation struct {
	Pane        int    `json:"pane"`
	Status      string `json:"status"`       // rate_limited, unresponsive, crashed, unknown
	Action      string `json:"action"`       // wait, restart, switch_account, investigate
	Reason      string `json:"reason"`       // human-readable explanation
	AutoFixable bool   `json:"auto_fixable"` // can --fix handle this?
	FixCommand  string `json:"fix_command"`  // command to fix (manual or auto)
}

// DiagnoseOptions configures the diagnose output
type DiagnoseOptions struct {
	Session string // session name (required)
	Pane    int    // specific pane to diagnose (-1 for all)
	Fix     bool   // attempt auto-fix
	Brief   bool   // minimal output
}

// PrintDiagnose outputs comprehensive health diagnosis for a session
func PrintDiagnose(opts DiagnoseOptions) error {
	output := DiagnoseOutput{
		RobotResponse:   NewRobotResponse(true),
		Session:         opts.Session,
		OverallHealth:   "healthy",
		Panes:           DiagnosePanes{},
		Recommendations: []DiagnoseRecommendation{},
	}

	// Initialize empty slices (never nil per envelope spec)
	output.Panes.Healthy = []int{}
	output.Panes.RateLimited = []int{}
	output.Panes.Unresponsive = []int{}
	output.Panes.Crashed = []int{}
	output.Panes.Unknown = []int{}

	// Check if session exists
	if !tmux.SessionExists(opts.Session) {
		output.Success = false
		output.Error = fmt.Sprintf("session '%s' not found", opts.Session)
		output.ErrorCode = ErrCodeSessionNotFound
		output.Hint = "Use 'ntm list' to see available sessions"
		return encodeJSON(output)
	}

	// Get all panes in session
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		output.Success = false
		output.Error = fmt.Sprintf("failed to get panes: %v", err)
		output.ErrorCode = ErrCodeInternalError
		return encodeJSON(output)
	}

	// Filter to specific pane if requested
	if opts.Pane >= 0 {
		filtered := []tmux.Pane{}
		for _, p := range panes {
			if p.Index == opts.Pane {
				filtered = append(filtered, p)
			}
		}
		if len(filtered) == 0 {
			output.Success = false
			output.Error = fmt.Sprintf("pane %d not found in session '%s'", opts.Pane, opts.Session)
			output.ErrorCode = ErrCodePaneNotFound
			output.Hint = fmt.Sprintf("Use 'ntm --robot-status' to list panes in session '%s'", opts.Session)
			return encodeJSON(output)
		}
		panes = filtered
	}

	// Analyze each pane
	for _, pane := range panes {
		agentType := detectAgentTypeFromPane(pane)

		// Skip user panes unless specifically requested
		if agentType == "user" && opts.Pane < 0 {
			continue
		}

		output.Summary.TotalPanes++

		// Perform comprehensive health check
		check, err := CheckAgentHealthWithActivity(pane.ID, agentType)
		if err != nil {
			// Error during health check - mark as unknown
			output.Summary.Unknown++
			output.Panes.Unknown = append(output.Panes.Unknown, pane.Index)
			output.Recommendations = append(output.Recommendations, DiagnoseRecommendation{
				Pane:        pane.Index,
				Status:      "unknown",
				Action:      "investigate",
				Reason:      fmt.Sprintf("Health check failed: %v", err),
				AutoFixable: false,
				FixCommand:  fmt.Sprintf("ntm inspect %s --pane=%d", opts.Session, pane.Index),
			})
			continue
		}

		// Classify based on health state
		switch check.HealthState {
		case HealthHealthy:
			output.Summary.Healthy++
			output.Panes.Healthy = append(output.Panes.Healthy, pane.Index)

		case HealthDegraded:
			// Degraded could be approaching rate limit or minor issues
			// Check if it's rate-limit related
			if check.ErrorCheck != nil && check.ErrorCheck.RateLimited {
				output.Summary.RateLimited++
				output.Panes.RateLimited = append(output.Panes.RateLimited, pane.Index)
				rec := buildRateLimitRecommendation(pane.Index, opts.Session, check)
				output.Recommendations = append(output.Recommendations, rec)
			} else {
				// Treat as healthy but note it
				output.Summary.Healthy++
				output.Panes.Healthy = append(output.Panes.Healthy, pane.Index)
			}

		case HealthRateLimited:
			output.Summary.RateLimited++
			output.Panes.RateLimited = append(output.Panes.RateLimited, pane.Index)
			rec := buildRateLimitRecommendation(pane.Index, opts.Session, check)
			output.Recommendations = append(output.Recommendations, rec)

		case HealthUnhealthy:
			// Determine if crashed or just unresponsive
			if check.ProcessCheck != nil && check.ProcessCheck.Crashed {
				output.Summary.Crashed++
				output.Panes.Crashed = append(output.Panes.Crashed, pane.Index)
				output.Recommendations = append(output.Recommendations, DiagnoseRecommendation{
					Pane:        pane.Index,
					Status:      "crashed",
					Action:      "restart",
					Reason:      check.Reason,
					AutoFixable: true,
					FixCommand:  fmt.Sprintf("ntm --robot-restart-pane=%s --panes=%d", opts.Session, pane.Index),
				})
			} else if check.StallCheck != nil && check.StallCheck.Stalled {
				output.Summary.Unresponsive++
				output.Panes.Unresponsive = append(output.Panes.Unresponsive, pane.Index)
				output.Recommendations = append(output.Recommendations, DiagnoseRecommendation{
					Pane:        pane.Index,
					Status:      "unresponsive",
					Action:      "interrupt",
					Reason:      fmt.Sprintf("Stalled for %d seconds", check.StallCheck.IdleSeconds),
					AutoFixable: true,
					FixCommand:  fmt.Sprintf("ntm --robot-interrupt=%s --panes=%d", opts.Session, pane.Index),
				})
			} else {
				// Generic unhealthy
				output.Summary.Unresponsive++
				output.Panes.Unresponsive = append(output.Panes.Unresponsive, pane.Index)
				output.Recommendations = append(output.Recommendations, DiagnoseRecommendation{
					Pane:        pane.Index,
					Status:      "unresponsive",
					Action:      "investigate",
					Reason:      check.Reason,
					AutoFixable: false,
					FixCommand:  fmt.Sprintf("ntm inspect %s --pane=%d", opts.Session, pane.Index),
				})
			}

		default:
			output.Summary.Unknown++
			output.Panes.Unknown = append(output.Panes.Unknown, pane.Index)
		}
	}

	// Sort all pane lists for consistent output
	sort.Ints(output.Panes.Healthy)
	sort.Ints(output.Panes.RateLimited)
	sort.Ints(output.Panes.Unresponsive)
	sort.Ints(output.Panes.Crashed)
	sort.Ints(output.Panes.Unknown)

	// Sort recommendations by pane index
	sort.Slice(output.Recommendations, func(i, j int) bool {
		return output.Recommendations[i].Pane < output.Recommendations[j].Pane
	})

	// Determine overall health
	output.OverallHealth = determineOverallHealth(output.Summary)

	// Check if auto-fix is available
	for _, rec := range output.Recommendations {
		if rec.AutoFixable {
			output.AutoFixAvail = true
			break
		}
	}
	if output.AutoFixAvail {
		output.AutoFixCommand = fmt.Sprintf("ntm --robot-diagnose=%s --fix", opts.Session)
	}

	// Handle --fix mode
	if opts.Fix && output.AutoFixAvail {
		return executeDiagnoseFix(output, opts)
	}

	return encodeJSON(output)
}

// determineOverallHealth calculates the overall session health
func determineOverallHealth(summary DiagnoseSummary) string {
	if summary.TotalPanes == 0 {
		return "healthy" // No agent panes = nothing to diagnose
	}

	// Critical: any crashed panes or majority unhealthy
	if summary.Crashed > 0 {
		return "critical"
	}
	if summary.Unresponsive > summary.Healthy {
		return "critical"
	}

	// Degraded: any rate-limited or unresponsive panes
	if summary.RateLimited > 0 || summary.Unresponsive > 0 || summary.Unknown > 0 {
		return "degraded"
	}

	return "healthy"
}

// buildRateLimitRecommendation creates a recommendation for rate-limited panes
func buildRateLimitRecommendation(paneIndex int, session string, check *HealthCheck) DiagnoseRecommendation {
	waitSeconds := 0
	if check.ErrorCheck != nil && check.ErrorCheck.WaitSeconds > 0 {
		waitSeconds = check.ErrorCheck.WaitSeconds
	}

	rec := DiagnoseRecommendation{
		Pane:        paneIndex,
		Status:      "rate_limited",
		AutoFixable: false, // Rate limits typically need manual intervention
	}

	if waitSeconds > 0 {
		rec.Action = "wait"
		rec.Reason = fmt.Sprintf("Rate limited, wait %d seconds", waitSeconds)
		rec.FixCommand = fmt.Sprintf("sleep %d && ntm --robot-diagnose=%s --pane=%d", waitSeconds, session, paneIndex)
	} else {
		rec.Action = "wait_or_switch"
		rec.Reason = "Rate limited, consider switching accounts or waiting"
		rec.FixCommand = "caam switch  # or wait for rate limit to reset"
	}

	return rec
}

// executeDiagnoseFix attempts to fix auto-fixable issues
func executeDiagnoseFix(diag DiagnoseOutput, opts DiagnoseOptions) error {
	// Build a fix report
	type FixAttempt struct {
		Pane    int    `json:"pane"`
		Action  string `json:"action"`
		Success bool   `json:"success"`
		Message string `json:"message"`
	}

	fixReport := struct {
		RobotResponse
		Session     string       `json:"session"`
		FixMode     bool         `json:"fix_mode"`
		FixAttempts []FixAttempt `json:"fix_attempts"`
		Summary     string       `json:"summary"`
	}{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		FixMode:       true,
		FixAttempts:   []FixAttempt{},
	}

	fixedCount := 0
	failedCount := 0

	for _, rec := range diag.Recommendations {
		if !rec.AutoFixable {
			continue
		}

		attempt := FixAttempt{
			Pane:   rec.Pane,
			Action: rec.Action,
		}

		switch rec.Action {
		case "restart":
			// Attempt to restart the pane
			target := fmt.Sprintf("%s:%d", opts.Session, rec.Pane)
			err := tmux.RespawnPane(target, true)
			if err != nil {
				attempt.Success = false
				attempt.Message = fmt.Sprintf("Failed to restart: %v", err)
				failedCount++
			} else {
				attempt.Success = true
				attempt.Message = "Pane restarted successfully"
				fixedCount++
			}

		case "interrupt":
			// Send Ctrl+C to interrupt
			paneID := fmt.Sprintf("%s:%d", opts.Session, rec.Pane)
			err := tmux.SendKeys(paneID, "C-c", false)
			if err != nil {
				attempt.Success = false
				attempt.Message = fmt.Sprintf("Failed to interrupt: %v", err)
				failedCount++
			} else {
				attempt.Success = true
				attempt.Message = "Interrupt sent (Ctrl+C)"
				fixedCount++
			}

		default:
			attempt.Success = false
			attempt.Message = "Action not supported for auto-fix"
			failedCount++
		}

		fixReport.FixAttempts = append(fixReport.FixAttempts, attempt)
	}

	// Generate summary
	if failedCount == 0 && fixedCount > 0 {
		fixReport.Summary = fmt.Sprintf("Fixed %d issue(s) successfully", fixedCount)
	} else if fixedCount > 0 && failedCount > 0 {
		fixReport.Summary = fmt.Sprintf("Fixed %d issue(s), %d failed", fixedCount, failedCount)
		fixReport.Success = true // Partial success
	} else if failedCount > 0 {
		fixReport.Summary = fmt.Sprintf("Failed to fix %d issue(s)", failedCount)
		fixReport.Success = false
	} else {
		fixReport.Summary = "No auto-fixable issues found"
	}

	return encodeJSON(fixReport)
}

// =============================================================================
// Brief Output Mode
// =============================================================================

// DiagnoseBriefOutput is a minimal version of diagnose output
type DiagnoseBriefOutput struct {
	RobotResponse
	Session       string `json:"session"`
	OverallHealth string `json:"overall_health"`
	Summary       string `json:"summary"` // e.g., "12/16 healthy, 2 rate_limited, 1 unresponsive, 1 crashed"
	HasIssues     bool   `json:"has_issues"`
	FixAvailable  bool   `json:"fix_available"`
}

// PrintDiagnoseBrief outputs a minimal health summary
func PrintDiagnoseBrief(session string) error {
	// Get full diagnosis first
	opts := DiagnoseOptions{Session: session, Pane: -1}
	_ = opts

	output := DiagnoseBriefOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       session,
	}

	// Check if session exists
	if !tmux.SessionExists(session) {
		output.Success = false
		output.Error = fmt.Sprintf("session '%s' not found", session)
		output.ErrorCode = ErrCodeSessionNotFound
		output.Hint = "Use 'ntm list' to see available sessions"
		return encodeJSON(output)
	}

	// Get panes and check health
	panes, err := tmux.GetPanes(session)
	if err != nil {
		output.Success = false
		output.Error = fmt.Sprintf("failed to get panes: %v", err)
		return encodeJSON(output)
	}

	var summary DiagnoseSummary
	hasAutoFix := false

	for _, pane := range panes {
		agentType := detectAgentTypeFromPane(pane)
		if agentType == "user" {
			continue
		}

		summary.TotalPanes++

		check, err := CheckAgentHealthWithActivity(pane.ID, agentType)
		if err != nil {
			summary.Unknown++
			continue
		}

		switch check.HealthState {
		case HealthHealthy:
			summary.Healthy++
		case HealthDegraded:
			if check.ErrorCheck != nil && check.ErrorCheck.RateLimited {
				summary.RateLimited++
			} else {
				summary.Healthy++
			}
		case HealthRateLimited:
			summary.RateLimited++
		case HealthUnhealthy:
			if check.ProcessCheck != nil && check.ProcessCheck.Crashed {
				summary.Crashed++
				hasAutoFix = true
			} else {
				summary.Unresponsive++
				if check.StallCheck != nil && check.StallCheck.Stalled {
					hasAutoFix = true
				}
			}
		default:
			summary.Unknown++
		}
	}

	output.OverallHealth = determineOverallHealth(summary)
	output.HasIssues = summary.RateLimited > 0 || summary.Unresponsive > 0 || summary.Crashed > 0 || summary.Unknown > 0
	output.FixAvailable = hasAutoFix

	// Build summary string
	parts := []string{fmt.Sprintf("%d/%d healthy", summary.Healthy, summary.TotalPanes)}
	if summary.RateLimited > 0 {
		parts = append(parts, fmt.Sprintf("%d rate_limited", summary.RateLimited))
	}
	if summary.Unresponsive > 0 {
		parts = append(parts, fmt.Sprintf("%d unresponsive", summary.Unresponsive))
	}
	if summary.Crashed > 0 {
		parts = append(parts, fmt.Sprintf("%d crashed", summary.Crashed))
	}
	if summary.Unknown > 0 {
		parts = append(parts, fmt.Sprintf("%d unknown", summary.Unknown))
	}

	output.Summary = ""
	for i, part := range parts {
		if i > 0 {
			output.Summary += ", "
		}
		output.Summary += part
	}

	return encodeJSON(output)
}

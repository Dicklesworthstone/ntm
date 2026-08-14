// Package robot provides machine-readable output for AI agents.
// diagnose.go implements the --robot-diagnose command for comprehensive health diagnosis.
package robot

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/Dicklesworthstone/ntm/internal/agent"
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
	Degraded     int `json:"degraded"`
	RateLimited  int `json:"rate_limited"`
	Unresponsive int `json:"unresponsive"`
	Crashed      int `json:"crashed"`
	Unknown      int `json:"unknown"`
}

// DiagnosePanes groups pane indices by health state
type DiagnosePanes struct {
	Healthy      []int `json:"healthy"`
	Degraded     []int `json:"degraded"`
	RateLimited  []int `json:"rate_limited"`
	Unresponsive []int `json:"unresponsive"`
	Crashed      []int `json:"crashed"`
	Unknown      []int `json:"unknown"`
}

// DiagnoseRecommendation is an actionable fix for a pane issue
type DiagnoseRecommendation struct {
	Pane int `json:"pane"`
	// PaneTarget is the unambiguous selector for this pane (%ID, or W.P on a
	// multi-window session). Pane alone is only unique within a window, so --fix
	// keyed on it acted on whichever pane with that index happened to be last in
	// the map — respawning an unrelated window's pane and validating the wrong
	// pane's agent type.
	PaneTarget  string `json:"pane_target"`
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

type diagnoseDependencies struct {
	sessionExists func(context.Context, string) (bool, error)
	listPanes     func(context.Context, string) ([]tmux.Pane, error)
	restartPane   diagnoseRestartPaneFunc
	sendKeys      func(context.Context, string, string, bool) error
}

func defaultDiagnoseDependencies() diagnoseDependencies {
	return diagnoseDependencies{
		sessionExists: tmux.SessionExistsContext,
		listPanes:     tmux.GetPanesContext,
		restartPane:   GetRestartPaneContext,
		sendKeys:      tmux.SendKeysContext,
	}
}

func (deps diagnoseDependencies) withDefaults() diagnoseDependencies {
	defaults := defaultDiagnoseDependencies()
	if deps.sessionExists == nil {
		deps.sessionExists = defaults.sessionExists
	}
	if deps.listPanes == nil {
		deps.listPanes = defaults.listPanes
	}
	if deps.restartPane == nil {
		deps.restartPane = defaults.restartPane
	}
	if deps.sendKeys == nil {
		deps.sendKeys = defaults.sendKeys
	}
	return deps
}

func diagnoseCancellationError(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func classifyDiagnoseOperationError(ctx context.Context, err error) (string, string, error) {
	if cancelErr := diagnoseCancellationError(ctx, err); cancelErr != nil {
		return ErrCodeTimeout, "Retry after the cancellation or timeout condition clears", cancelErr
	}
	return ErrCodeInternalError, "Check tmux session health and retry", err
}

// GetDiagnose collects comprehensive health diagnosis for a session.
// This function returns the data struct directly, enabling CLI/REST parity.
// Note: This does not execute fixes; use PrintDiagnose with opts.Fix=true for that.
// The context bounds any tmux ListPanes call so a hung tmux daemon doesn't
// outlive a cancelled caller.
func GetDiagnose(ctx context.Context, opts DiagnoseOptions) (*DiagnoseOutput, error) {
	return getDiagnoseWithDependencies(ctx, opts, defaultDiagnoseDependencies())
}

func getDiagnoseWithDependencies(ctx context.Context, opts DiagnoseOptions, deps diagnoseDependencies) (*DiagnoseOutput, error) {
	output := &DiagnoseOutput{
		RobotResponse:   NewRobotResponse(true),
		Session:         opts.Session,
		Panes:           DiagnosePanes{},
		Recommendations: []DiagnoseRecommendation{},
	}

	// Initialize empty slices (never nil per envelope spec)
	output.Panes.Healthy = []int{}
	output.Panes.Degraded = []int{}
	output.Panes.RateLimited = []int{}
	output.Panes.Unresponsive = []int{}
	output.Panes.Crashed = []int{}
	output.Panes.Unknown = []int{}
	if ctx == nil {
		output.RobotResponse = NewErrorResponse(errors.New("diagnose context is required"), ErrCodeInternalError, "Retry the command")
		return output, nil
	}
	if err := ctx.Err(); err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeTimeout, "Retry after the cancellation or timeout condition clears")
		return output, nil
	}
	deps = deps.withDefaults()

	// Check if session exists
	exists, err := deps.sessionExists(ctx, opts.Session)
	if err != nil {
		code, hint, cause := classifyDiagnoseOperationError(ctx, err)
		output.RobotResponse = NewErrorResponse(fmt.Errorf("failed to check session: %w", cause), code, hint)
		return output, nil
	}
	if !exists {
		output.Success = false
		output.Error = fmt.Sprintf("session '%s' not found", opts.Session)
		output.ErrorCode = ErrCodeSessionNotFound
		output.Hint = "Use 'ntm list' to see available sessions"
		return output, nil
	}

	// Get all panes in session
	panes, err := deps.listPanes(ctx, opts.Session)
	if err != nil {
		code, hint, cause := classifyDiagnoseOperationError(ctx, err)
		output.RobotResponse = NewErrorResponse(fmt.Errorf("failed to get panes: %w", cause), code, hint)
		return output, nil
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
			return output, nil
		}
		panes = filtered
	}

	// Pane.Index is only unique within a window, so every recommendation carries
	// a topology-aware target that --fix can act on unambiguously.
	multiWindow := tmux.PanesSpanMultipleWindows(panes)

	// Analyze each pane
	for _, pane := range panes {
		agentType := detectAgentTypeFromPane(pane)

		// Skip user panes unless specifically requested
		if agentType == "user" && opts.Pane < 0 {
			continue
		}

		output.Summary.TotalPanes++

		// Perform comprehensive health check (pass shell PID for authoritative liveness)
		check, err := CheckAgentHealthWithActivity(pane.ID, agentType, pane.PID, pane.Width)
		if err != nil {
			// Error during health check - mark as unknown
			output.Summary.Unknown++
			output.Panes.Unknown = append(output.Panes.Unknown, pane.Index)
			output.Recommendations = append(output.Recommendations, DiagnoseRecommendation{
				Pane:        pane.Index,
				PaneTarget:  paneTargetKey(pane, multiWindow),
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
				rec := buildRateLimitRecommendation(pane.Index, paneTargetKey(pane, multiWindow), opts.Session, check)
				output.Recommendations = append(output.Recommendations, rec)
			} else {
				// Treat as degraded
				output.Summary.Degraded++
				output.Panes.Degraded = append(output.Panes.Degraded, pane.Index)

				// If stalled, add recommendation
				if check.StallCheck != nil && check.StallCheck.Stalled {
					output.Recommendations = append(output.Recommendations, DiagnoseRecommendation{
						Pane:        pane.Index,
						PaneTarget:  paneTargetKey(pane, multiWindow),
						Status:      "stalled",
						Action:      "investigate",
						Reason:      check.Reason,
						AutoFixable: false,
						FixCommand:  fmt.Sprintf("ntm inspect %s --pane=%d", opts.Session, pane.Index),
					})
				}
			}

		case HealthRateLimited:
			output.Summary.RateLimited++
			output.Panes.RateLimited = append(output.Panes.RateLimited, pane.Index)
			rec := buildRateLimitRecommendation(pane.Index, paneTargetKey(pane, multiWindow), opts.Session, check)
			output.Recommendations = append(output.Recommendations, rec)

		case HealthUnhealthy:
			// Determine if crashed or just unresponsive
			if check.ProcessCheck != nil && check.ProcessCheck.Crashed {
				output.Summary.Crashed++
				output.Panes.Crashed = append(output.Panes.Crashed, pane.Index)
				output.Recommendations = append(output.Recommendations, DiagnoseRecommendation{
					Pane:        pane.Index,
					PaneTarget:  paneTargetKey(pane, multiWindow),
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
					PaneTarget:  paneTargetKey(pane, multiWindow),
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
					PaneTarget:  paneTargetKey(pane, multiWindow),
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
	sort.Ints(output.Panes.Degraded)
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

	return output, nil
}

// PrintDiagnose outputs comprehensive health diagnosis for a session.
// This is a thin wrapper around GetDiagnose() for CLI output.
// When opts.Fix is true and auto-fix is available, it executes fixes.
// The context bounds tmux calls so a hung daemon doesn't outlive a
// cancelled caller (e.g. Ctrl-C during CLI execution).
func PrintDiagnose(ctx context.Context, opts DiagnoseOptions) error {
	output, err := GetDiagnose(ctx, opts)
	if err != nil {
		return err
	}

	// Handle --fix mode
	if opts.Fix && output.AutoFixAvail {
		return executeDiagnoseFix(ctx, *output, opts)
	}

	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot diagnose failed")
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

	// Degraded: any rate-limited, degraded, or unresponsive panes
	if summary.RateLimited > 0 || summary.Degraded > 0 || summary.Unresponsive > 0 || summary.Unknown > 0 {
		return "degraded"
	}

	return "healthy"
}

// buildRateLimitRecommendation creates a recommendation for rate-limited panes
func buildRateLimitRecommendation(paneIndex int, paneTarget string, session string, check *HealthCheck) DiagnoseRecommendation {
	waitSeconds := 0
	if check.ErrorCheck != nil && check.ErrorCheck.WaitSeconds > 0 {
		waitSeconds = check.ErrorCheck.WaitSeconds
	}

	rec := DiagnoseRecommendation{
		Pane:        paneIndex,
		PaneTarget:  paneTarget,
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

// executeDiagnoseFix attempts to fix auto-fixable issues. The context
// bounds tmux ListPanes so cancellation propagates into the fix loop.
func executeDiagnoseFix(ctx context.Context, diag DiagnoseOutput, opts DiagnoseOptions) error {
	return executeDiagnoseFixWithDependencies(ctx, diag, opts, defaultDiagnoseDependencies())
}

func executeDiagnoseFixWithDependencies(ctx context.Context, diag DiagnoseOutput, opts DiagnoseOptions, deps diagnoseDependencies) error {
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
	finishCancellation := func(err error, attempt *FixAttempt) error {
		if attempt != nil {
			fixReport.FixAttempts = append(fixReport.FixAttempts, *attempt)
		}
		fixReport.RobotResponse = NewErrorResponse(err, ErrCodeTimeout, "Retry after the cancellation or timeout condition clears")
		fixReport.Summary = fmt.Sprintf(
			"Fix loop cancelled (%v); %d issue(s) attempted before cancellation",
			err, fixedCount+failedCount,
		)
		return encodeTerminalRobotOutput(&fixReport, fixReport.RobotResponse, "robot diagnose fix failed")
	}
	if ctx == nil {
		fixReport.RobotResponse = NewErrorResponse(errors.New("diagnose fix context is required"), ErrCodeInternalError, "Retry the command")
		fixReport.Summary = "No fixes attempted because the command context is missing"
		return encodeTerminalRobotOutput(&fixReport, fixReport.RobotResponse, "robot diagnose fix failed")
	}
	if err := ctx.Err(); err != nil {
		return finishCancellation(err, nil)
	}
	deps = deps.withDefaults()

	// Pre-fetch panes once so we can look up each recommendation by index
	// and dispatch tmux ops against the base-index-independent pane ID
	// (`%N`) rather than the naive `<session>:<paneIdx>` form, which tmux
	// interprets as a *window* index and breaks on hosts with
	// `base-index = 1` (#141). Context-aware so callers can cancel a hung
	// tmux daemon (matches the HTTP `handlePaneInputV1` shape).
	fixPanes, fixPanesErr := deps.listPanes(ctx, opts.Session)
	if fixPanesErr != nil {
		code, hint, cause := classifyDiagnoseOperationError(ctx, fixPanesErr)
		fixReport.RobotResponse = NewErrorResponse(fmt.Errorf("failed to list panes: %w", cause), code, hint)
		fixReport.Summary = "No fixes attempted because pane discovery failed"
		return encodeTerminalRobotOutput(&fixReport, fixReport.RobotResponse, "robot diagnose fix failed")
	}
	// Keyed by the unambiguous target, not the bare index: two windows can each
	// hold a pane 1, and an index-keyed map silently kept only the last one — so
	// --fix respawned a pane in the wrong window.
	fixMultiWindow := tmux.PanesSpanMultipleWindows(fixPanes)
	paneIDByTarget := map[string]string{}
	for _, p := range fixPanes {
		paneIDByTarget[paneTargetKey(p, fixMultiWindow)] = p.ID
	}
	if err := validateDiagnoseFixTargets(diag, fixPanes); err != nil {
		fixReport.RobotResponse = NewErrorResponse(err, ErrCodeNotImplemented, agent.GrokPhaseOneCapabilityHint)
		fixReport.Summary = "No fixes attempted because the target batch contains an unsupported agent lifecycle"
		return encodeTerminalRobotOutput(&fixReport, fixReport.RobotResponse, "robot diagnose fix failed")
	}

	for _, rec := range diag.Recommendations {
		// Honor cancellation between iterations. Restart and interrupt
		// operations receive the same context so cancellation also stops
		// their in-flight tmux subprocesses.
		if err := ctx.Err(); err != nil {
			return finishCancellation(err, nil)
		}

		if !rec.AutoFixable {
			continue
		}

		attempt := FixAttempt{
			Pane:   rec.Pane,
			Action: rec.Action,
		}

		paneTarget, paneFound := paneIDByTarget[diagnoseRecommendationKey(rec)]
		if !paneFound {
			attempt.Success = false
			attempt.Message = fmt.Sprintf("Pane %s not found in session %q", diagnoseRecommendationLabel(rec), opts.Session)
			failedCount++
			fixReport.FixAttempts = append(fixReport.FixAttempts, attempt)
			continue
		}

		switch rec.Action {
		case "restart":
			var restartErr error
			attempt.Success, attempt.Message, restartErr = executeDiagnoseRestart(ctx, opts.Session, paneTarget, deps.restartPane)
			if attempt.Success {
				fixedCount++
			} else {
				failedCount++
			}
			if cancelErr := diagnoseCancellationError(ctx, restartErr); cancelErr != nil {
				return finishCancellation(cancelErr, &attempt)
			}

		case "interrupt":
			// Send Ctrl+C to interrupt via the pane ID.
			interruptErr := deps.sendKeys(ctx, paneTarget, "C-c", false)
			if interruptErr != nil {
				attempt.Success = false
				attempt.Message = fmt.Sprintf("Failed to interrupt: %v", interruptErr)
				failedCount++
			} else {
				attempt.Success = true
				attempt.Message = "Interrupt sent (Ctrl+C)"
				fixedCount++
			}
			if cancelErr := diagnoseCancellationError(ctx, interruptErr); cancelErr != nil {
				return finishCancellation(cancelErr, &attempt)
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

	return encodeTerminalRobotOutput(&fixReport, fixReport.RobotResponse, "robot diagnose fix failed")
}

type diagnoseRestartPaneFunc func(context.Context, RestartPaneOptions) (*RestartPaneOutput, error)

func executeDiagnoseRestart(ctx context.Context, session, paneTarget string, restart diagnoseRestartPaneFunc) (bool, string, error) {
	if restart == nil {
		return false, "Failed to restart: restart service is unavailable", nil
	}
	result, err := restart(ctx, RestartPaneOptions{Session: session, Panes: []string{paneTarget}})
	if err != nil {
		return false, fmt.Sprintf("Failed to restart: %v", err), err
	}
	if result == nil {
		return false, "Failed to restart: empty restart response", nil
	}
	if len(result.Restarted) == 0 {
		message := result.Error
		if message == "" {
			message = "no pane was restarted"
		}
		if result.ErrorCode == ErrCodeTimeout {
			cause := error(context.DeadlineExceeded)
			if ctx != nil && ctx.Err() != nil {
				cause = ctx.Err()
			}
			return false, "Failed to restart: " + message, fmt.Errorf("restart reported timeout: %w", cause)
		}
		return false, "Failed to restart: " + message, nil
	}
	if !result.Success || len(result.Failed) > 0 {
		message := result.Error
		if message == "" && len(result.Failed) > 0 {
			message = result.Failed[0].Reason
		}
		if message == "" {
			message = "restart did not complete cleanly"
		}
		return false, "Failed to restart: " + message, nil
	}
	for pane, relaunched := range result.AgentRelaunched {
		if !relaunched {
			return false, fmt.Sprintf("Failed to restart: agent in pane %s was not relaunched", pane), nil
		}
	}
	return true, "Pane and agent restarted successfully", nil
}

// diagnoseRecommendationLabel names a recommendation's pane for messages,
// preferring the unambiguous target over the window-local index.
func diagnoseRecommendationLabel(rec DiagnoseRecommendation) string {
	return diagnoseRecommendationKey(rec)
}

// diagnoseRecommendationKey is the map key for a recommendation's pane.
//
// GetDiagnose always sets PaneTarget, but a recommendation built by hand or
// carried over from an older response may not have it. Falling back to the bare
// index is safe there: paneTargetKey produces exactly that for a single-window
// session, which is the only topology where an index is unambiguous.
func diagnoseRecommendationKey(rec DiagnoseRecommendation) string {
	if rec.PaneTarget != "" {
		return rec.PaneTarget
	}
	return fmt.Sprintf("%d", rec.Pane)
}

func validateDiagnoseFixTargets(diag DiagnoseOutput, panes []tmux.Pane) error {
	// Keyed by target, not index: an index-keyed map let one window's pane
	// overwrite another's, so this validated the wrong pane's agent type — either
	// letting a grok pane through or blocking a supported one.
	multiWindow := tmux.PanesSpanMultipleWindows(panes)
	typesByTarget := make(map[string]string, len(panes))
	for _, pane := range panes {
		typesByTarget[paneTargetKey(pane, multiWindow)] = detectAgentTypeFromPane(pane)
	}
	for _, rec := range diag.Recommendations {
		if !rec.AutoFixable || (rec.Action != "restart" && rec.Action != "interrupt") {
			continue
		}
		agentType, ok := typesByTarget[diagnoseRecommendationKey(rec)]
		if !ok {
			continue
		}
		if err := agent.AgentType(agentType).ValidateAutomatedRelaunch(); err != nil {
			return fmt.Errorf("pane %s (%s) diagnose action %q: %w",
				diagnoseRecommendationLabel(rec), agentType, rec.Action, err)
		}
	}
	return nil
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

// GetDiagnoseBrief collects a minimal health summary for a session.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetDiagnoseBrief(ctx context.Context, session string) (*DiagnoseBriefOutput, error) {
	return getDiagnoseBriefWithDependencies(ctx, session, defaultDiagnoseDependencies())
}

func getDiagnoseBriefWithDependencies(ctx context.Context, session string, deps diagnoseDependencies) (*DiagnoseBriefOutput, error) {
	output := &DiagnoseBriefOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       session,
	}
	if ctx == nil {
		output.RobotResponse = NewErrorResponse(errors.New("diagnose brief context is required"), ErrCodeInternalError, "Retry the command")
		return output, nil
	}
	if err := ctx.Err(); err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeTimeout, "Retry after the cancellation or timeout condition clears")
		return output, nil
	}
	deps = deps.withDefaults()

	// Check if session exists
	exists, err := deps.sessionExists(ctx, session)
	if err != nil {
		code, hint, cause := classifyDiagnoseOperationError(ctx, err)
		output.RobotResponse = NewErrorResponse(fmt.Errorf("failed to check session: %w", cause), code, hint)
		return output, nil
	}
	if !exists {
		output.Success = false
		output.Error = fmt.Sprintf("session '%s' not found", session)
		output.ErrorCode = ErrCodeSessionNotFound
		output.Hint = "Use 'ntm list' to see available sessions"
		return output, nil
	}

	// Get panes and check health
	panes, err := deps.listPanes(ctx, session)
	if err != nil {
		code, hint, cause := classifyDiagnoseOperationError(ctx, err)
		output.RobotResponse = NewErrorResponse(fmt.Errorf("failed to get panes: %w", cause), code, hint)
		return output, nil
	}

	var summary DiagnoseSummary
	hasAutoFix := false

	for _, pane := range panes {
		agentType := detectAgentTypeFromPane(pane)
		if agentType == "user" {
			continue
		}

		summary.TotalPanes++

		check, err := CheckAgentHealthWithActivity(pane.ID, agentType, pane.PID, pane.Width)
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
				summary.Degraded++
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
	output.HasIssues = summary.Degraded > 0 || summary.RateLimited > 0 || summary.Unresponsive > 0 || summary.Crashed > 0 || summary.Unknown > 0
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

	return output, nil
}

// PrintDiagnoseBrief outputs a minimal health summary.
// This is a thin wrapper around GetDiagnoseBrief() for CLI output.
func PrintDiagnoseBrief(ctx context.Context, session string) error {
	output, err := GetDiagnoseBrief(ctx, session)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot diagnose brief failed")
}

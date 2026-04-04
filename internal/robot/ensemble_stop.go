// Package robot provides machine-readable output for AI agents.
// ensemble_stop.go implements --robot-ensemble-stop for stopping ensembles.
package robot

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// EnsembleStopOptions configures --robot-ensemble-stop behavior.
type EnsembleStopOptions struct {
	Force     bool // Skip graceful shutdown
	NoCollect bool // Don't collect partial outputs
}

// EnsembleStopOutput is the structured response for --robot-ensemble-stop.
type EnsembleStopOutput struct {
	RobotResponse
	Result     EnsembleStopResult `json:"result"`
	AgentHints *AgentHints        `json:"_agent_hints,omitempty"`
}

// EnsembleStopResult contains the stop operation details.
type EnsembleStopResult struct {
	Session     string `json:"session"`
	PrevStatus  string `json:"prev_status"`
	FinalStatus string `json:"final_status"`
	Stopped     int    `json:"stopped"`
	Captured    int    `json:"captured,omitempty"`
	Message     string `json:"message,omitempty"`
}

// GetEnsembleStop stops an ensemble and returns the result.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetEnsembleStop(session string, opts EnsembleStopOptions) (*EnsembleStopOutput, error) {
	correlationID := audit.NewCorrelationID()
	auditStart := time.Now()
	output := &EnsembleStopOutput{
		RobotResponse: NewRobotResponse(true),
		Result: EnsembleStopResult{
			Session: session,
		},
	}
	_ = audit.LogEvent(session, audit.EventTypeCommand, audit.ActorSystem, "ensemble.stop", map[string]interface{}{
		"phase":          "start",
		"session":        session,
		"force":          opts.Force,
		"no_collect":     opts.NoCollect,
		"correlation_id": correlationID,
	}, nil)
	defer func() {
		success := output != nil && output.Success
		payload := map[string]interface{}{
			"phase":          "finish",
			"session":        session,
			"force":          opts.Force,
			"no_collect":     opts.NoCollect,
			"stopped":        output.Result.Stopped,
			"captured":       output.Result.Captured,
			"final_status":   output.Result.FinalStatus,
			"message":        output.Result.Message,
			"success":        success,
			"duration_ms":    time.Since(auditStart).Milliseconds(),
			"correlation_id": correlationID,
		}
		if output != nil && output.Error != "" {
			payload["error"] = output.Error
		}
		_ = audit.LogEvent(session, audit.EventTypeCommand, audit.ActorSystem, "ensemble.stop", payload, nil)
	}()

	if strings.TrimSpace(session) == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session name is required"),
			ErrCodeInvalidFlag,
			"Provide a session name: ntm --robot-ensemble-stop=myproject",
		)
		return output, nil
	}

	state, err := ensemble.LoadSession(session)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if !tmux.SessionExists(session) {
				output.RobotResponse = NewErrorResponse(
					fmt.Errorf("session '%s' not found", session),
					ErrCodeSessionNotFound,
					"Use 'ntm list' to see available sessions",
				)
				return output, nil
			}
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("ensemble state not found for session '%s'", session),
				ErrCodeEnsembleNotFound,
				"No ensemble running in this session",
			)
			return output, nil
		}
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to load ensemble state: %w", err),
			ErrCodeInternalError,
			"Check state store availability",
		)
		return output, nil
	}

	output.Result.PrevStatus = state.Status.String()
	sessionLive := tmux.SessionExists(session)

	// Check if already stopped
	if state.Status.IsTerminal() {
		output.Result.FinalStatus = state.Status.String()
		output.Result.Message = fmt.Sprintf("Ensemble already in terminal state: %s", state.Status)
		output.AgentHints = &AgentHints{
			Summary: output.Result.Message,
		}
		return output, nil
	}

	if !sessionLive {
		state.Status = ensemble.EnsembleStopped
		if err := ensemble.SaveSession(session, state); err != nil {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("failed to save stopped state: %w", err),
				ErrCodeInternalError,
				"State store may require manual cleanup",
			)
			output.Result.FinalStatus = output.Result.PrevStatus
			return output, nil
		}
		output.Result.FinalStatus = ensemble.EnsembleStopped.String()
		output.Result.Message = fmt.Sprintf("Ensemble session already absent; marked state stopped (previously %s)", output.Result.PrevStatus)
		output.AgentHints = &AgentHints{
			Summary: output.Result.Message,
		}
		return output, nil
	}

	// Collect partial outputs if requested
	captured := 0
	if !opts.NoCollect {
		capture := ensemble.NewOutputCapture(tmux.DefaultClient)
		capturedOutputs, err := capture.CaptureAll(state)
		if err == nil {
			captured = len(capturedOutputs)
		}
	}
	output.Result.Captured = captured

	// Get panes for counting
	panes, _ := tmux.GetPanes(session)
	stoppedCount := len(panes)

	// Graceful shutdown: send Ctrl+C to each pane
	if !opts.Force && len(panes) > 0 {
		for _, pane := range panes {
			_ = tmux.SendKeys(pane.ID, "C-c", false)
		}
		time.Sleep(5 * time.Second)
	}

	// Kill the session
	if err := tmux.KillSession(session); err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to kill session: %w", err),
			ErrCodeInternalError,
			"Session may require manual cleanup",
		)
		output.Result.FinalStatus = state.Status.String()
		return output, nil
	}

	// Update ensemble state to stopped
	state.Status = ensemble.EnsembleStopped
	if err := ensemble.SaveSession(session, state); err != nil {
		// Non-fatal: session is already killed
		output.Result.Message = fmt.Sprintf("Stopped %d panes, but failed to save state: %v", stoppedCount, err)
	} else {
		output.Result.Message = fmt.Sprintf("Stopped %d panes", stoppedCount)
		if captured > 0 {
			output.Result.Message += fmt.Sprintf(", captured %d partial outputs", captured)
		}
	}

	output.Result.Stopped = stoppedCount
	output.Result.FinalStatus = ensemble.EnsembleStopped.String()

	output.AgentHints = &AgentHints{
		Summary: output.Result.Message,
		SuggestedActions: []RobotAction{
			{
				Action:   "review",
				Target:   "partial outputs",
				Reason:   "ensemble was stopped before completion",
				Priority: 1,
			},
		},
	}

	return output, nil
}

// PrintEnsembleStop outputs ensemble stop result for a session.
func PrintEnsembleStop(session string, opts EnsembleStopOptions) error {
	output, err := GetEnsembleStop(session, opts)
	if err != nil {
		return err
	}
	return outputJSON(output)
}

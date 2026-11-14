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
	Session          string   `json:"session"`
	PrevStatus       string   `json:"prev_status"`
	FinalStatus      string   `json:"final_status"`
	Stopped          int      `json:"stopped"`
	Captured         int      `json:"captured,omitempty"`
	SavedOutputPaths []string `json:"saved_output_paths,omitempty"`
	Message          string   `json:"message,omitempty"`
}

type ensembleStopDependencies struct {
	SessionExists  func(string) bool
	CaptureAndSave func(*ensemble.EnsembleSession) ([]ensemble.CapturedOutput, error)
	GetPanes       func(string) ([]tmux.Pane, error)
	SendKeys       func(string, string, bool) error
	KillSession    func(string) error
	Sleep          func(time.Duration)
}

func defaultEnsembleStopDependencies() ensembleStopDependencies {
	return ensembleStopDependencies{
		SessionExists: tmux.SessionExists,
		CaptureAndSave: func(state *ensemble.EnsembleSession) ([]ensemble.CapturedOutput, error) {
			return ensemble.NewOutputCapture(tmux.DefaultClient).CaptureAndSave(state)
		},
		GetPanes: tmux.GetPanes,
		// A key NAME ("C-c", "Escape"): tmux.SendKeys is literal (-l) and
		// would type the characters instead of interrupting.
		SendKeys: func(target, key string, _ bool) error {
			return tmux.SendKeyName(target, key)
		},
		KillSession: tmux.KillSession,
		Sleep:       time.Sleep,
	}
}

// GetEnsembleStop stops an ensemble and returns the result.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetEnsembleStop(session string, opts EnsembleStopOptions) (*EnsembleStopOutput, error) {
	return getEnsembleStop(session, opts, defaultEnsembleStopDependencies())
}

func getEnsembleStop(session string, opts EnsembleStopOptions, deps ensembleStopDependencies) (*EnsembleStopOutput, error) {
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
			"phase":              "finish",
			"session":            session,
			"force":              opts.Force,
			"no_collect":         opts.NoCollect,
			"stopped":            output.Result.Stopped,
			"captured":           output.Result.Captured,
			"saved_output_paths": output.Result.SavedOutputPaths,
			"final_status":       output.Result.FinalStatus,
			"message":            output.Result.Message,
			"success":            success,
			"duration_ms":        time.Since(auditStart).Milliseconds(),
			"correlation_id":     correlationID,
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
			if !deps.SessionExists(session) {
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
	sessionLive := deps.SessionExists(session)

	// A failed spawn can leave a live tmux session even after state becomes terminal.
	if state.Status.IsTerminal() && !sessionLive {
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

	// Preserve both structured results and unfinished transcripts before shutdown.
	preserveOutputs := func() bool {
		if opts.NoCollect {
			return true
		}
		capturedOutputs, err := deps.CaptureAndSave(state)
		if err != nil {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("failed to preserve partial ensemble output: %w", err),
				ErrCodeInternalError,
				"Partial outputs could not be saved; the ensemble is still running so you can retry or inspect panes",
			)
			output.Result.FinalStatus = state.Status.String()
			output.Result.Message = "Partial output preservation failed; ensemble was left running"
			return false
		}
		output.Result.Captured = len(capturedOutputs)
		for _, captured := range capturedOutputs {
			if captured.OutputPath != "" {
				output.Result.SavedOutputPaths = append(output.Result.SavedOutputPaths, captured.OutputPath)
			}
		}
		return true
	}
	if !preserveOutputs() {
		return output, nil
	}

	// An inspection failure must not turn an unknown session into a successful stop.
	panes, err := deps.GetPanes(session)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to inspect ensemble panes: %w", err),
			ErrCodeInternalError,
			"The ensemble is still running; retry after tmux is available",
		)
		output.Result.FinalStatus = state.Status.String()
		output.Result.Message = "Pane inspection failed; ensemble was left running"
		return output, nil
	}
	stoppedCount := len(panes)

	// Graceful shutdown: send each pane its agent's interrupt key (Ctrl+C;
	// Escape for omp)
	if !opts.Force && len(panes) > 0 {
		for _, pane := range panes {
			_ = deps.SendKeys(pane.ID, tmux.InterruptKeyForAgent(pane.Type), false)
		}
		deps.Sleep(5 * time.Second)
		// Agents may flush their final response after receiving the interrupt.
		if !preserveOutputs() {
			return output, nil
		}
	}

	// Kill the session
	if err := deps.KillSession(session); err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to kill session: %w", err),
			ErrCodeInternalError,
			"Session may require manual cleanup",
		)
		output.Result.FinalStatus = state.Status.String()
		return output, nil
	}

	// Keep valid completed results, and do not report interrupted assignments as running.
	state.Status = ensemble.EnsembleStopped
	for i := range state.Assignments {
		if !state.Assignments[i].Status.IsTerminal() {
			state.Assignments[i].Status = ensemble.AssignmentError
			state.Assignments[i].Error = "ensemble stopped before a valid mode output was collected"
		}
	}
	output.Result.Stopped = stoppedCount
	output.Result.FinalStatus = ensemble.EnsembleStopped.String()
	if err := ensemble.SaveSession(session, state); err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("stopped ensemble but failed to save stopped state: %w", err),
			ErrCodeInternalError,
			"The tmux session is stopped; repair the state store before retrying",
		)
		output.Result.Message = fmt.Sprintf("Stopped %d panes, but failed to save state: %v", stoppedCount, err)
		return output, nil
	}
	output.Result.Message = fmt.Sprintf("Stopped %d panes", stoppedCount)
	if output.Result.Captured > 0 {
		output.Result.Message += fmt.Sprintf(", captured %d partial outputs", output.Result.Captured)
	}

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
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot ensemble stop failed")
}

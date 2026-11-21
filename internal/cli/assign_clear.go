package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/assign"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ============================================================================
// Clear Assignments - --clear and --clear-pane functionality
// ============================================================================

const (
	clearErrNotAssigned       = "NOT_ASSIGNED"
	clearErrAlreadyCompleted  = "ALREADY_COMPLETED"
	clearErrPaneNotFound      = "PANE_NOT_FOUND"
	clearErrInvalidFlag       = "INVALID_FLAG"
	clearErrInternal          = "INTERNAL_ERROR"
	clearErrCompletionPending = "COMPLETION_EVENT_PENDING"
)

func clearAssignmentFailureCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return robot.ErrCodeTimeout
	case errors.Is(err, assignment.ErrCompletionEventPending):
		return clearErrCompletionPending
	default:
		return ""
	}
}

// ClearAssignmentResult represents the result of clearing a single assignment.
type ClearAssignmentResult struct {
	BeadID                   string   `json:"bead_id"`
	BeadTitle                string   `json:"bead_title,omitempty"`
	PreviousPane             int      `json:"previous_pane,omitempty"`
	PreviousAgent            string   `json:"previous_agent,omitempty"`
	PreviousAgentType        string   `json:"previous_agent_type,omitempty"`
	PreviousStatus           string   `json:"previous_status,omitempty"`
	AssignmentFound          bool     `json:"assignment_found"`
	FileReservationsReleased bool     `json:"file_reservations_released"`
	FilesReleased            []string `json:"files_released,omitempty"`
	Success                  bool     `json:"success"`
	Error                    string   `json:"error,omitempty"`
	ErrorCode                string   `json:"error_code,omitempty"`
}

// ClearAllResult represents result of clearing all assignments for a pane.
type ClearAllResult struct {
	Pane         int                     `json:"pane"`
	AgentType    string                  `json:"agent_type"`
	Success      bool                    `json:"success"`
	Error        string                  `json:"error,omitempty"`
	ClearedBeads []ClearAssignmentResult `json:"cleared_beads"`
}

// ClearAssignmentsSummary provides a summary of a clear operation.
type ClearAssignmentsSummary struct {
	ClearedCount         int `json:"cleared_count"`
	ReservationsReleased int `json:"reservations_released"`
	FailedCount          int `json:"failed_count,omitempty"`
}

// ClearAssignmentsData is the data payload for clear operations.
type ClearAssignmentsData struct {
	Cleared   []ClearAssignmentResult `json:"cleared"`
	Summary   ClearAssignmentsSummary `json:"summary"`
	Pane      *int                    `json:"pane,omitempty"`
	AgentType string                  `json:"agent_type,omitempty"`
}

// ClearAssignmentsError represents an error in the clear envelope.
type ClearAssignmentsError struct {
	Code    string                 `json:"code"`
	Message string                 `json:"message"`
	Details map[string]interface{} `json:"details,omitempty"`
}

// ClearAssignmentsEnvelope is the standard JSON envelope for clear operations.
type ClearAssignmentsEnvelope struct {
	Command    string                 `json:"command"`
	Subcommand string                 `json:"subcommand"`
	Session    string                 `json:"session"`
	Timestamp  string                 `json:"timestamp"`
	Success    bool                   `json:"success"`
	Data       *ClearAssignmentsData  `json:"data,omitempty"`
	Warnings   []string               `json:"warnings"`
	Error      *ClearAssignmentsError `json:"error,omitempty"`
}

var releaseReservations = releaseFileReservations
var releaseAssignmentLeases = releaseAssignmentReservationsForClear
var getClearPanePanes = tmux.GetPanesContext

// runClearAssignments handles exactly one clear operation.
func runClearAssignments(cmd *cobra.Command, session string) error {
	makeClearErrorEnvelope := func(code, msg string) ClearAssignmentsEnvelope {
		return ClearAssignmentsEnvelope{
			Command:    "assign",
			Subcommand: "clear",
			Session:    session,
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
			Success:    false,
			Warnings:   []string{},
			Error: &ClearAssignmentsError{
				Code:    code,
				Message: msg,
			},
		}
	}

	operationCount := 0
	if strings.TrimSpace(assignClear) != "" {
		operationCount++
	}
	if strings.TrimSpace(assignClearPane) != "" {
		operationCount++
	}
	if assignClearFailed {
		operationCount++
	}
	if operationCount > 1 {
		err := fmt.Errorf("use only one of --clear, --clear-pane, or --clear-failed")
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeClearErrorEnvelope("INVALID_ARGS", err.Error()))
		}
		return err
	}

	if assignClear != "" {
		return runClearSpecificBeads(cmd, session, assignClear)
	}

	if strings.TrimSpace(assignClearPane) != "" {
		return runClearPaneAssignments(cmd, session, assignClearPane)
	}

	if assignClearFailed {
		return runClearFailedAssignments(cmd, session)
	}

	err := fmt.Errorf("no clear operation specified")
	if IsJSONOutput() {
		return emitJSONFailureEnvelope(makeClearErrorEnvelope("INVALID_ARGS", err.Error()))
	}
	return err
}

func clearStoredAssignment(ctx context.Context, store *assignment.AssignmentStore, session string, current *assignment.Assignment) ([]string, error) {
	return clearStoredAssignmentIfStatus(ctx, store, session, current)
}

func clearStoredAssignmentIfStatus(ctx context.Context, store *assignment.AssignmentStore, session string, current *assignment.Assignment, expected ...assignment.AssignmentStatus) ([]string, error) {
	if current == nil {
		return nil, errors.New("assignment is required")
	}
	if store == nil {
		return nil, errors.New("assignment store is required")
	}
	cleanupUnlock, err := store.AcquireExternalCleanupLock(ctx, current.BeadID)
	if err != nil {
		return nil, fmt.Errorf("lock assignment external cleanup %s: %w", current.BeadID, err)
	}
	defer cleanupUnlock()
	if err := store.LoadStrict(); err != nil {
		return nil, fmt.Errorf("refresh assignment external cleanup %s: %w", current.BeadID, err)
	}
	refreshed := store.Get(current.BeadID)
	if refreshed == nil {
		return nil, nil
	}
	if !assignment.SameAssignmentGeneration(current, refreshed) {
		return nil, fmt.Errorf("assignment %s generation changed while waiting to clear", current.BeadID)
	}
	if assignmentHasPendingTerminalReconciliation(refreshed) {
		return nil, fmt.Errorf("assignment %s is awaiting terminal reconciliation", current.BeadID)
	}
	if !assignmentStatusIsTerminalForCleanup(current.Status) && assignmentStatusIsTerminalForCleanup(refreshed.Status) {
		return nil, fmt.Errorf("assignment %s reached terminal status %s while waiting to clear", current.BeadID, refreshed.Status)
	}
	current = refreshed
	if assignForce && current.DispatchState == assignment.DispatchSending && !IsJSONOutput() {
		output.PrintWarningf(
			"Force-clearing %s while its dispatch outcome is unknown: the original message may already have been delivered, so re-assigning can duplicate work. Verify pane %d before re-dispatching.",
			current.BeadID, current.Pane,
		)
	}
	var clearing *assignment.Assignment
	switch {
	case assignForce && len(expected) == 0:
		clearing, err = store.BeginClearForce(ctx, current.BeadID, time.Now().UTC())
	case assignForce:
		clearing, err = store.BeginClearForceIfStatus(ctx, current.BeadID, time.Now().UTC(), expected...)
	case len(expected) == 0:
		clearing, err = store.BeginClear(ctx, current.BeadID, time.Now().UTC())
	default:
		clearing, err = store.BeginClearIfStatus(ctx, current.BeadID, time.Now().UTC(), expected...)
	}
	if err != nil {
		return nil, err
	}
	if assignmentHasPendingTerminalReconciliation(clearing) {
		return nil, fmt.Errorf("assignment %s is awaiting terminal reconciliation", current.BeadID)
	}
	var released []string
	if clearing.ClearState != assignment.ClearStateLeasesReleased {
		var releaseErr error
		released, releaseErr = releaseAssignmentLeases(ctx, session, clearing)
		if releaseErr != nil {
			if persistErr := store.RecordClearReleaseFailed(ctx, current.BeadID, releaseErr); persistErr != nil {
				return nil, errors.Join(releaseErr, fmt.Errorf("persist clear failure: %w", persistErr))
			}
			return nil, releaseErr
		}
		clearing, err = store.RecordClearLeasesReleased(ctx, current.BeadID)
		if err != nil {
			return released, fmt.Errorf("persist completed reservation release: %w", err)
		}
	}
	if strings.TrimSpace(clearing.ClaimActor) != "" {
		projectDir, projectErr := resolveAssignProjectDir(ctx, session)
		if projectErr != nil {
			claimErr := fmt.Errorf("resolve project for Beads claim release: %w", projectErr)
			if persistErr := store.RecordClearReleaseFailed(ctx, current.BeadID, claimErr); persistErr != nil {
				return released, errors.Join(claimErr, fmt.Errorf("persist clear failure: %w", persistErr))
			}
			return released, claimErr
		}
		if _, claimErr := releaseBeadClaimForAssignment(ctx, projectDir, clearing.BeadID, clearing.ClaimActor); claimErr != nil {
			claimErr = fmt.Errorf("release Beads claim for %s: %w", clearing.BeadID, claimErr)
			if persistErr := store.RecordClearReleaseFailed(ctx, current.BeadID, claimErr); persistErr != nil {
				return released, errors.Join(claimErr, fmt.Errorf("persist clear failure: %w", persistErr))
			}
			return released, claimErr
		}
	}
	if err := store.CompleteClear(ctx, current.BeadID); err != nil {
		removalErr := fmt.Errorf("reservations released but assignment removal failed: %w", err)
		if persistErr := store.RecordClearReleaseFailed(ctx, current.BeadID, removalErr); persistErr != nil {
			return released, errors.Join(removalErr, fmt.Errorf("persist clear failure: %w", persistErr))
		}
		return released, removalErr
	}
	return released, nil
}

func assignmentHasPendingTerminalReconciliation(current *assignment.Assignment) bool {
	return current != nil &&
		(current.PendingTerminalStatus == assignment.StatusCompleted || current.PendingTerminalStatus == assignment.StatusFailed)
}

func assignmentStatusIsTerminalForCleanup(status assignment.AssignmentStatus) bool {
	return status == assignment.StatusCompleted || status == assignment.StatusFailed || status == assignment.StatusReassigned
}

// runClearSpecificBeads handles --clear flag (clear specific bead assignments).
func runClearSpecificBeads(cmd *cobra.Command, session string, clearBeads string) error {
	beadIDs := strings.Split(clearBeads, ",")
	for i := range beadIDs {
		beadIDs[i] = strings.TrimSpace(beadIDs[i])
	}
	return runClearSelectedAssignments(cmd, session, beadIDs, "clear")
}

func runClearFailedAssignments(cmd *cobra.Command, session string) error {
	store, err := assignment.LoadStoreStrict(session)
	if err != nil {
		return emitClearStoreError(session, "clear-failed", err)
	}
	beadIDs := make([]string, 0)
	for _, current := range store.GetAll() {
		if current.Status == assignment.StatusFailed {
			beadIDs = append(beadIDs, current.BeadID)
		}
	}
	return runClearSelectedAssignmentsFromStore(cmd, store, session, beadIDs, "clear-failed")
}

func emitClearStoreError(session, subcommand string, storeErr error) error {
	err := fmt.Errorf("failed to load assignment store: %w", storeErr)
	if IsJSONOutput() {
		return emitJSONFailureEnvelope(ClearAssignmentsEnvelope{
			Command:    "assign",
			Subcommand: subcommand,
			Session:    session,
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
			Success:    false,
			Warnings:   []string{},
			Error: &ClearAssignmentsError{
				Code:    "STORE_ERROR",
				Message: err.Error(),
			},
		})
	}
	return err
}

func runClearSelectedAssignments(cmd *cobra.Command, session string, beadIDs []string, subcommand string) error {
	store, err := assignment.LoadStoreStrict(session)
	if err != nil {
		return emitClearStoreError(session, subcommand, err)
	}
	return runClearSelectedAssignmentsFromStore(cmd, store, session, beadIDs, subcommand)
}

func runClearSelectedAssignmentsFromStore(cmd *cobra.Command, store *assignment.AssignmentStore, session string, beadIDs []string, subcommand string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	results := make([]ClearAssignmentResult, 0, len(beadIDs))
	successCount := 0
	var cancellationErr error
	var typedFailureCode string
	var firstFailureErr error

	for _, beadID := range beadIDs {
		result := ClearAssignmentResult{
			BeadID: beadID,
		}

		assignments := store.GetAll()
		var foundAssignment *assignment.Assignment
		for _, a := range assignments {
			if a.BeadID == beadID && (a.Status != assignment.StatusCompleted || assignForce) {
				foundAssignment = &a
				break
			}
		}

		if foundAssignment == nil {
			result.Success = false
			result.Error = "assignment not found or already completed"
		} else {
			result.PreviousPane = foundAssignment.Pane
			result.PreviousAgent = foundAssignment.AgentName
			result.PreviousAgentType = foundAssignment.AgentType
			result.PreviousStatus = string(foundAssignment.Status)
			result.AssignmentFound = true

			var releasedFiles []string
			clearErr := cancellationErr
			if clearErr == nil {
				clearErr = ctx.Err()
			}
			if clearErr == nil {
				if subcommand == "clear-failed" {
					releasedFiles, clearErr = clearStoredAssignmentIfStatus(ctx, store, session, foundAssignment, assignment.StatusFailed)
				} else {
					releasedFiles, clearErr = clearStoredAssignment(ctx, store, session, foundAssignment)
				}
			}
			clearErr = preserveCommandContextError(ctx, clearErr)
			if clearErr != nil {
				if firstFailureErr == nil {
					firstFailureErr = clearErr
				}
				result.Success = false
				result.Error = clearErr.Error()
				result.ErrorCode = clearAssignmentFailureCode(clearErr)
				if typedFailureCode == "" && result.ErrorCode != "" {
					typedFailureCode = result.ErrorCode
				}
				if result.ErrorCode == robot.ErrCodeTimeout {
					if cancellationErr == nil {
						cancellationErr = clearErr
					}
				}
			} else {
				result.FilesReleased = releasedFiles
				result.FileReservationsReleased = len(releasedFiles) > 0
				result.Success = true
				successCount++
			}
		}

		results = append(results, result)
	}

	if IsJSONOutput() {
		reservationsReleased := 0
		for _, r := range results {
			if r.FileReservationsReleased {
				reservationsReleased += len(r.FilesReleased)
			}
		}

		envelope := ClearAssignmentsEnvelope{
			Command:    "assign",
			Subcommand: subcommand,
			Session:    session,
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
			Success:    successCount == len(beadIDs),
			Data: &ClearAssignmentsData{
				Cleared: results,
				Summary: ClearAssignmentsSummary{
					ClearedCount:         successCount,
					ReservationsReleased: reservationsReleased,
					FailedCount:          len(beadIDs) - successCount,
				},
			},
			Warnings: []string{},
		}
		if !envelope.Success {
			code := "CLEAR_FAILED"
			message := fmt.Sprintf("%d of %d assignments failed to clear", len(beadIDs)-successCount, len(beadIDs))
			if cancellationErr != nil {
				code = robot.ErrCodeTimeout
				message = fmt.Sprintf("assignment clear canceled: %v", cancellationErr)
			} else if successCount == 0 && typedFailureCode != "" {
				code = typedFailureCode
				message = results[0].Error
			}
			envelope.Error = &ClearAssignmentsError{Code: code, Message: message}
		}
		if encErr := json.NewEncoder(os.Stdout).Encode(envelope); encErr != nil {
			return encErr
		}
		if !envelope.Success {
			return errors.Join(jsonFailureExit(), firstFailureErr)
		}
		return nil
	}

	if !assignQuiet {
		fmt.Printf("Cleared %d of %d bead assignments:\n\n", successCount, len(beadIDs))
		for _, result := range results {
			if result.Success {
				fmt.Printf("  ✓ %s (pane %d, %s)\n", result.BeadID, result.PreviousPane, result.PreviousAgentType)
				if len(result.FilesReleased) > 0 {
					fmt.Printf("    Released files: %v\n", result.FilesReleased)
				}
			} else {
				fmt.Printf("  ✗ %s: %s\n", result.BeadID, result.Error)
			}
		}
	}

	if successCount != len(beadIDs) {
		if cancellationErr != nil {
			return fmt.Errorf("assignment clear canceled: %w", cancellationErr)
		}
		return errors.Join(fmt.Errorf("%d of %d assignments failed to clear", len(beadIDs)-successCount, len(beadIDs)), firstFailureErr)
	}
	return nil
}

func assignmentMatchesPhysicalPane(current assignment.Assignment, target tmux.Pane) (bool, error) {
	paneID, err := assignment.CanonicalPaneIdentity(&current)
	if err != nil {
		return false, err
	}
	return paneID == strings.TrimSpace(target.ID), nil
}

func resolveClearPaneTarget(panes []tmux.Pane, selector string) (tmux.Pane, error) {
	resolved, err := tmux.ResolvePaneSelectors(panes, []string{selector}, true)
	if err != nil {
		return tmux.Pane{}, err
	}
	return resolved[0], nil
}

// runClearPaneAssignments handles --clear-pane for one canonical physical pane.
func runClearPaneAssignments(cmd *cobra.Command, session, selector string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	paneDisplay := -1
	makeClearPaneErrorEnvelope := func(code, msg string) ClearAssignmentsEnvelope {
		panePtr := paneDisplay
		return ClearAssignmentsEnvelope{
			Command:    "assign",
			Subcommand: "clear-pane",
			Session:    session,
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
			Success:    false,
			Data:       &ClearAssignmentsData{Pane: &panePtr},
			Warnings:   []string{},
			Error: &ClearAssignmentsError{
				Code:    code,
				Message: msg,
			},
		}
	}

	panes, err := getClearPanePanes(ctx, session)
	if err != nil {
		err = preserveCommandContextError(ctx, fmt.Errorf("failed to get panes: %w", err))
		code := "TMUX_ERROR"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = robot.ErrCodeTimeout
		}
		if IsJSONOutput() {
			return emitTypedAssignmentFailure(err, makeClearPaneErrorEnvelope(code, err.Error()))
		}
		return err
	}

	targetPane, err := resolveClearPaneTarget(panes, selector)
	if err != nil {
		err = fmt.Errorf("resolve pane %q in session %s: %w", selector, session, err)
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeClearPaneErrorEnvelope(directPaneSelectorErrorCode(err), err.Error()))
		}
		return err
	}
	paneDisplay = targetPane.Index

	agentType := agentTypeForPane(targetPane)

	store, err := assignment.LoadStoreStrict(session)
	if err != nil {
		err = fmt.Errorf("failed to load assignment store: %w", err)
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeClearPaneErrorEnvelope("STORE_ERROR", err.Error()))
		}
		return err
	}

	assignments := store.GetAll()
	var paneAssignments []assignment.Assignment
	for _, a := range assignments {
		matches, identityErr := assignmentMatchesPhysicalPane(a, targetPane)
		if identityErr != nil {
			wrapped := fmt.Errorf("resolve assignment %s physical pane: %w", a.BeadID, identityErr)
			return emitTypedAssignmentFailure(wrapped, makeClearPaneErrorEnvelope("PANE_IDENTITY_MIGRATION_REQUIRED", wrapped.Error()))
		}
		if matches && (a.Status != assignment.StatusCompleted || assignForce) {
			paneAssignments = append(paneAssignments, a)
		}
	}

	result := ClearAllResult{
		Pane:      targetPane.Index,
		AgentType: agentType,
		Success:   true,
	}
	var cancellationErr error
	var typedFailureCode string
	var firstFailureErr error

	for _, a := range paneAssignments {
		beadResult := ClearAssignmentResult{
			BeadID:            a.BeadID,
			PreviousPane:      a.Pane,
			PreviousAgent:     a.AgentName,
			PreviousAgentType: a.AgentType,
			AssignmentFound:   true,
			PreviousStatus:    string(a.Status),
		}

		var releasedFiles []string
		clearErr := cancellationErr
		if clearErr == nil {
			clearErr = ctx.Err()
		}
		if clearErr == nil {
			releasedFiles, clearErr = clearStoredAssignment(ctx, store, session, &a)
		}
		clearErr = preserveCommandContextError(ctx, clearErr)
		if clearErr != nil {
			if firstFailureErr == nil {
				firstFailureErr = clearErr
			}
			beadResult.Success = false
			beadResult.Error = clearErr.Error()
			beadResult.ErrorCode = clearAssignmentFailureCode(clearErr)
			if typedFailureCode == "" && beadResult.ErrorCode != "" {
				typedFailureCode = beadResult.ErrorCode
			}
			if beadResult.ErrorCode == robot.ErrCodeTimeout {
				if cancellationErr == nil {
					cancellationErr = clearErr
				}
			}
			result.Success = false
		} else {
			beadResult.FilesReleased = releasedFiles
			beadResult.FileReservationsReleased = len(releasedFiles) > 0
			beadResult.Success = true
		}

		result.ClearedBeads = append(result.ClearedBeads, beadResult)
	}

	if len(paneAssignments) == 0 {
		result.Error = "no active assignments found for this pane"
	}

	if IsJSONOutput() {
		reservationsReleased := 0
		for _, r := range result.ClearedBeads {
			if r.FileReservationsReleased {
				reservationsReleased += len(r.FilesReleased)
			}
		}

		successCount := 0
		for _, cleared := range result.ClearedBeads {
			if cleared.Success {
				successCount++
			}
		}
		failedCount := len(result.ClearedBeads) - successCount
		panePtr := targetPane.Index
		envelope := ClearAssignmentsEnvelope{
			Command:    "assign",
			Subcommand: "clear-pane",
			Session:    session,
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
			Success:    failedCount == 0,
			Data: &ClearAssignmentsData{
				Cleared:   result.ClearedBeads,
				Pane:      &panePtr,
				AgentType: agentType,
				Summary: ClearAssignmentsSummary{
					ClearedCount:         successCount,
					ReservationsReleased: reservationsReleased,
					FailedCount:          failedCount,
				},
			},
			Warnings: []string{},
		}
		if !envelope.Success {
			code := "CLEAR_FAILED"
			message := fmt.Sprintf("%d of %d pane assignments failed to clear", failedCount, len(result.ClearedBeads))
			if cancellationErr != nil {
				code = robot.ErrCodeTimeout
				message = fmt.Sprintf("pane assignment clear canceled: %v", cancellationErr)
			} else if successCount == 0 && typedFailureCode != "" {
				code = typedFailureCode
				message = result.ClearedBeads[0].Error
			}
			envelope.Error = &ClearAssignmentsError{Code: code, Message: message}
		}
		if err := json.NewEncoder(os.Stdout).Encode(envelope); err != nil {
			return err
		}
		if !envelope.Success {
			return errors.Join(jsonFailureExit(), firstFailureErr)
		}
		return nil
	}

	if !assignQuiet {
		if len(paneAssignments) == 0 {
			fmt.Printf("No active assignments found for pane %s (%s)\n", assignmentPaneTarget(targetPane), agentType)
		} else {
			fmt.Printf("Cleared all assignments for pane %s (%s):\n\n", assignmentPaneTarget(targetPane), agentType)
			for _, beadResult := range result.ClearedBeads {
				if beadResult.Success {
					fmt.Printf("  ✓ %s\n", beadResult.BeadID)
					if len(beadResult.FilesReleased) > 0 {
						fmt.Printf("    Released files: %v\n", beadResult.FilesReleased)
					}
				} else {
					fmt.Printf("  ✗ %s: %s\n", beadResult.BeadID, beadResult.Error)
				}
			}
		}
	}

	if !result.Success {
		err := fmt.Errorf("one or more assignments for pane %s failed to clear", assignmentPaneTarget(targetPane))
		return errors.Join(err, firstFailureErr)
	}
	return nil
}

// findAssignmentForPhysicalPane finds the active assignment occupying one
// physical tmux pane. Every active row must carry a canonical tmux pane ID;
// ambiguous legacy rows fail closed because they may own the requested pane.
func findAssignmentForPhysicalPane(store *assignment.AssignmentStore, pane tmux.Pane) (*assignment.Assignment, error) {
	var found *assignment.Assignment
	for _, a := range store.ListActive() {
		if a == nil {
			continue
		}
		matches, err := assignmentMatchesPhysicalPane(*a, pane)
		if err != nil {
			return nil, err
		}
		if !matches {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("physical tmux pane %s has multiple active assignments %s and %s", pane.ID, found.BeadID, a.BeadID)
		}
		found = a
	}
	return found, nil
}

func releaseAssignmentReservationsForClear(ctx context.Context, session string, current *assignment.Assignment) ([]string, error) {
	if ctx == nil {
		return nil, errors.New("assignment reservation release context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if current == nil {
		return nil, errors.New("assignment is required")
	}
	if current.ClearState != assignment.ClearStateReservationReleasing && current.ClearState != assignment.ClearStateLeasesReleased {
		return nil, fmt.Errorf("assignment %s has no durable release barrier", current.BeadID)
	}
	if current.ClearState == assignment.ClearStateLeasesReleased || current.ReservationState == assignment.ReservationReleased {
		return nil, nil
	}
	if current.IdempotencyKey != "" && !current.ReservationRequired {
		return nil, nil
	}
	if current.IdempotencyKey != "" && current.ReservationRequired {
		if assignment.ReservationOutcomeNeedsReconciliation(current) {
			return reconcileAndReleaseAssignmentReservationsForClear(ctx, session, current)
		}
		switch current.ReservationState {
		case assignment.ReservationFailed:
			return nil, nil
		case assignment.ReservationPending:
			if current.ReservationAttempts == 0 {
				return nil, nil
			}
		}
	}
	if len(current.ReservationIDs) > 0 {
		paths := append([]string(nil), current.ReservedPaths...)
		if len(paths) == 0 {
			paths = append(paths, current.ReservationRequested...)
		}
		if len(paths) == 0 {
			paths = append(paths, current.ReservationInputPaths...)
		}
		return releaseFileReservationsWithIDs(ctx, session, current, current.ReservationIDs, paths)
	}
	if len(current.ReservedPaths) > 0 {
		return releaseFileReservationsByStoredPaths(ctx, session, current, current.ReservedPaths)
	}
	return releaseReservations(ctx, session, current)
}

func reconcileAndReleaseAssignmentReservationsForClear(ctx context.Context, session string, current *assignment.Assignment) ([]string, error) {
	projectKey, err := resolveAssignProjectDir(ctx, session)
	if err != nil {
		return nil, err
	}
	amClient := newAgentMailClient(projectKey)
	if !amClient.IsAvailable() {
		return nil, fmt.Errorf("cannot reconcile reservation outcome for %s: Agent Mail is unavailable", current.BeadID)
	}
	requested := append([]string(nil), current.ReservationRequested...)
	if len(requested) == 0 {
		requested = append(requested, current.ReservationInputPaths...)
	}
	if len(requested) == 0 {
		requested = assign.ExtractFilePaths(current.BeadTitle, "")
	}
	if len(requested) == 0 {
		return nil, fmt.Errorf("cannot reconcile reservation outcome for %s without durable requested paths", current.BeadID)
	}
	manager := assign.NewFileReservationManager(amClient, projectKey)
	releaseCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(assignTimeout))
	defer cancel()
	result, err := manager.ReconcileForBead(releaseCtx, current.BeadID, current.AgentName, requested)
	if err != nil {
		return nil, fmt.Errorf("reconcile reservation outcome for %s: %w", current.BeadID, err)
	}
	if result == nil || (len(result.ReservationIDs) == 0 && len(result.GrantedPaths) == 0) {
		return nil, nil
	}
	if len(result.ReservationIDs) > 0 {
		releasedPaths, err := manager.ReleaseExactForBead(releaseCtx, current, result.ReservationIDs, requested)
		if err != nil {
			return nil, fmt.Errorf("release reconciled reservations for %s: %w", current.BeadID, err)
		}
		if len(releasedPaths) > 0 {
			return releasedPaths, nil
		}
		return []string{fmt.Sprintf("%d reservations", len(result.ReservationIDs))}, nil
	}
	releasedPaths, err := manager.ReleaseExactForBead(releaseCtx, current, nil, result.GrantedPaths)
	if err != nil {
		return nil, fmt.Errorf("release reconciled reservation paths for %s: %w", current.BeadID, err)
	}
	return releasedPaths, nil
}

func releaseFileReservationsWithIDs(ctx context.Context, session string, current *assignment.Assignment, reservationIDs []int, paths []string) ([]string, error) {
	if len(reservationIDs) == 0 {
		return nil, nil
	}
	if current == nil {
		return nil, errors.New("assignment release barrier is required")
	}
	beadID := current.BeadID
	projectKey, err := resolveAssignProjectDir(ctx, session)
	if err != nil {
		return nil, err
	}

	amClient := newAgentMailClient(projectKey)
	if !amClient.IsAvailable() {
		return nil, fmt.Errorf("cannot release %d stored reservations for %s: Agent Mail is unavailable", len(reservationIDs), beadID)
	}

	manager := assign.NewFileReservationManager(amClient, projectKey)
	releaseCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(assignTimeout))
	defer cancel()

	releasedPaths, releaseErr := manager.ReleaseExactForBead(releaseCtx, current, reservationIDs, paths)
	if releaseErr != nil {
		return nil, fmt.Errorf("failed to release reservations: %w", releaseErr)
	}
	if assignHumanDiagnostics(assignVerbose) {
		fmt.Fprintf(os.Stderr, "[RESERVE] Released %d reservations for %s\n", len(reservationIDs), beadID)
	}
	if len(releasedPaths) > 0 {
		return releasedPaths, nil
	}
	return []string{fmt.Sprintf("%d reservations", len(reservationIDs))}, nil
}

func releaseFileReservationsByStoredPaths(ctx context.Context, session string, current *assignment.Assignment, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if current == nil {
		return nil, errors.New("assignment release barrier is required")
	}
	beadID := current.BeadID
	projectKey, err := resolveAssignProjectDir(ctx, session)
	if err != nil {
		return nil, err
	}
	amClient := newAgentMailClient(projectKey)
	if !amClient.IsAvailable() {
		return nil, fmt.Errorf("cannot release stored reservation paths for %s: Agent Mail is unavailable", beadID)
	}
	manager := assign.NewFileReservationManager(amClient, projectKey)
	releaseCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(assignTimeout))
	defer cancel()
	releasedPaths, releaseErr := manager.ReleaseExactForBead(releaseCtx, current, nil, paths)
	if releaseErr != nil {
		return nil, fmt.Errorf("failed to release reservation paths: %w", releaseErr)
	}
	return releasedPaths, nil
}

// releaseFileReservations releases file reservations for a bead via Agent Mail
// when no reservation IDs were persisted.
func releaseFileReservations(ctx context.Context, session string, current *assignment.Assignment) ([]string, error) {
	if current == nil {
		return nil, errors.New("assignment release barrier is required")
	}
	beadID := current.BeadID
	projectKey, err := resolveAssignProjectDir(ctx, session)
	if err != nil {
		return nil, err
	}

	amClient := newAgentMailClient(projectKey)
	if !amClient.IsAvailable() {
		return nil, nil
	}

	beadTitle, titleErr := getBeadTitle(ctx, projectKey, beadID)
	if titleErr != nil {
		return nil, fmt.Errorf("look up bead %s title for reservation release: %w", beadID, titleErr)
	}
	if beadTitle == "" {
		if assignHumanDiagnostics(assignVerbose) {
			fmt.Fprintf(os.Stderr, "[RESERVE] No bead title found for %s, cannot determine paths to release\n", beadID)
		}
		return nil, nil
	}

	paths := assign.ExtractFilePaths(beadTitle, "")
	if len(paths) == 0 {
		if assignHumanDiagnostics(assignVerbose) {
			fmt.Fprintf(os.Stderr, "[RESERVE] No file paths found in bead %s title, nothing to release\n", beadID)
		}
		return nil, nil
	}

	manager := assign.NewFileReservationManager(amClient, projectKey)
	releaseCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(assignTimeout))
	defer cancel()

	releasedPaths, err := manager.ReleaseExactForBead(releaseCtx, current, nil, paths)
	if err != nil {
		return nil, fmt.Errorf("failed to release reservations by paths: %w", err)
	}

	if assignHumanDiagnostics(assignVerbose) {
		fmt.Fprintf(os.Stderr, "[RESERVE] Released reservations for paths: %v (bead: %s)\n", paths, beadID)
	}

	return releasedPaths, nil
}

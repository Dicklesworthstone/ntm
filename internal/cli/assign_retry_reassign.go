package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assign"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ReassignData holds the successful reassignment data.
type ReassignData struct {
	BeadID                       string                   `json:"bead_id"`
	BeadTitle                    string                   `json:"bead_title"`
	Pane                         int                      `json:"pane"`
	AgentType                    string                   `json:"agent_type"`
	AgentName                    string                   `json:"agent_name,omitempty"`
	Status                       string                   `json:"status"`
	PromptSent                   bool                     `json:"prompt_sent"`
	AssignedAt                   string                   `json:"assigned_at"`
	PreviousPane                 int                      `json:"previous_pane"`
	PreviousAgent                string                   `json:"previous_agent,omitempty"`
	PreviousAgentType            string                   `json:"previous_agent_type"`
	PreviousStatus               string                   `json:"previous_status"`
	FileReservationsTransferred  bool                     `json:"file_reservations_transferred"`
	FileReservationsReleasedFrom int                      `json:"file_reservations_released_from,omitempty"`
	FileReservationsCreatedFor   int                      `json:"file_reservations_created_for,omitempty"`
	CASSInjection                *robot.CASSInjectionInfo `json:"cass_injection,omitempty"`
	MemoryInjection              *robot.CMInjectionInfo   `json:"memory_injection,omitempty"`
}

// ReassignError represents an error in the reassignment envelope.
type ReassignError struct {
	Code    string                 `json:"code"`
	Message string                 `json:"message"`
	Details map[string]interface{} `json:"details,omitempty"`
}

// ReassignEnvelope is the standard JSON envelope for reassignment operations.
type ReassignEnvelope struct {
	Command    string         `json:"command"`
	Subcommand string         `json:"subcommand"`
	Session    string         `json:"session"`
	Timestamp  string         `json:"timestamp"`
	Success    bool           `json:"success"`
	Data       *ReassignData  `json:"data,omitempty"`
	Warnings   []string       `json:"warnings"`
	Error      *ReassignError `json:"error,omitempty"`
}

// RetryItem holds data for a single retried assignment.
type RetryItem struct {
	BeadID             string                   `json:"bead_id"`
	BeadTitle          string                   `json:"bead_title"`
	Pane               int                      `json:"pane"`
	AgentType          string                   `json:"agent_type"`
	AgentName          string                   `json:"agent_name,omitempty"`
	Status             string                   `json:"status"`
	PromptSent         bool                     `json:"prompt_sent"`
	AssignedAt         string                   `json:"assigned_at"`
	PreviousPane       int                      `json:"previous_pane"`
	PreviousAgent      string                   `json:"previous_agent,omitempty"`
	PreviousFailReason string                   `json:"previous_fail_reason,omitempty"`
	RetryCount         int                      `json:"retry_count"`
	CASSInjection      *robot.CASSInjectionInfo `json:"cass_injection,omitempty"`
	MemoryInjection    *robot.CMInjectionInfo   `json:"memory_injection,omitempty"`
}

// RetrySkippedItem holds data for a skipped retry.
type RetrySkippedItem struct {
	BeadID string `json:"bead_id"`
	Reason string `json:"reason"`
}

// RetrySummary provides summary statistics for retry operations.
type RetrySummary struct {
	TotalFailed  int `json:"total_failed"`
	RetriedCount int `json:"retried_count"`
	SkippedCount int `json:"skipped_count"`
}

// RetryData holds the data payload for retry operations.
type RetryData struct {
	Retried []RetryItem        `json:"retried"`
	Skipped []RetrySkippedItem `json:"skipped"`
	Summary RetrySummary       `json:"summary"`
}

// makeRetryEnvelope creates a standard RetryEnvelope for JSON output.
func makeRetryEnvelope(session string, success bool, data *RetryData, errCode, errMsg string, warnings []string) AssignEnvelope[RetryData] {
	envelope := AssignEnvelope[RetryData]{
		Command:    "assign",
		Subcommand: "retry",
		Session:    session,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Success:    success,
		Data:       data,
		Warnings:   warnings,
	}
	if warnings == nil {
		envelope.Warnings = []string{}
	}
	if errCode != "" {
		envelope.Error = &AssignError{
			Code:    errCode,
			Message: errMsg,
		}
	}
	return envelope
}

func emitRetryFailure(session, defaultCode string, err error) error {
	if IsJSONOutput() {
		code := defaultCode
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = robot.ErrCodeTimeout
		}
		return errors.Join(
			emitJSONFailureEnvelope(makeRetryEnvelope(session, false, nil, code, err.Error(), nil)),
			err,
		)
	}
	return err
}

// runRetryAssignments handles --retry and --retry-failed operations.
func runRetryAssignments(ctx context.Context, session string) error {
	if ctx == nil {
		return emitRetryFailure(session, robot.ErrCodeInternalError, errors.New("assignment retry context is required"))
	}
	if err := ctx.Err(); err != nil {
		return emitRetryFailure(session, robot.ErrCodeTimeout, fmt.Errorf("assignment retry canceled: %w", err))
	}
	store, err := assignment.LoadStoreStrict(session)
	if err != nil {
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeRetryEnvelope(
				session, false, nil, "STORE_LOAD_ERROR",
				fmt.Sprintf("failed to load assignment store: %v", err), nil,
			))
		}
		return fmt.Errorf("failed to load assignment store: %w", err)
	}

	allAssignments := store.GetAll()
	if len(allAssignments) == 0 {
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeRetryEnvelope(
				session, false, nil, "NO_ASSIGNMENTS", "no assignments found", nil,
			))
		}
		return fmt.Errorf("no assignments found")
	}

	var failedAssignments []assignment.Assignment
	for _, a := range allAssignments {
		if a.Status == assignment.StatusFailed ||
			(a.Status == assignment.StatusClaimed && a.DispatchState == assignment.DispatchPending) {
			failedAssignments = append(failedAssignments, a)
		}
	}

	if assignRetry != "" {
		var found *assignment.Assignment
		for i := range failedAssignments {
			if failedAssignments[i].BeadID == assignRetry {
				found = &failedAssignments[i]
				break
			}
		}
		if found == nil {
			for _, a := range allAssignments {
				if a.BeadID == assignRetry {
					if IsJSONOutput() {
						return emitJSONFailureEnvelope(makeRetryEnvelope(
							session, false, nil, "NOT_FAILED",
							fmt.Sprintf("bead %s is not in a retryable state (status: %s)", assignRetry, a.Status), nil,
						))
					}
					return fmt.Errorf("bead %s is not in a retryable state (status: %s)", assignRetry, a.Status)
				}
			}
			if IsJSONOutput() {
				return emitJSONFailureEnvelope(makeRetryEnvelope(
					session, false, nil, "NOT_FOUND",
					fmt.Sprintf("bead %s not found in assignments", assignRetry), nil,
				))
			}
			return fmt.Errorf("bead %s not found in assignments", assignRetry)
		}
		failedAssignments = []assignment.Assignment{*found}
	}

	if len(failedAssignments) == 0 {
		if IsJSONOutput() {
			return json.NewEncoder(os.Stdout).Encode(makeRetryEnvelope(
				session, true, &RetryData{
					Summary: RetrySummary{TotalFailed: 0, RetriedCount: 0, SkippedCount: 0},
					Retried: []RetryItem{},
					Skipped: []RetrySkippedItem{},
				}, "", "", nil,
			))
		}
		fmt.Println("No failed assignments to retry")
		return nil
	}

	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return emitRetryFailure(session, "PANE_ERROR", fmt.Errorf("failed to get panes: %w", err))
	}
	projectDir, err := resolveAssignProjectDir(ctx, session)
	if err != nil {
		return emitRetryFailure(session, "PROJECT_ERROR", fmt.Errorf("resolve bead project for atomic retry: %w", err))
	}
	multiWindow := tmux.PanesSpanMultipleWindows(panes)
	promptContext := resolvePromptContextOptions(assignWithCASS, assignNoCASS, assignWithMemory, cfg)

	needsIdleAgent := false
	for _, failed := range failedAssignments {
		recoverAtomic := failed.Status == assignment.StatusClaimed &&
			failed.DispatchState == assignment.DispatchPending &&
			failed.IdempotencyKey != "" && failed.ClaimActor != "" && failed.PendingPrompt != ""
		if !recoverAtomic && strings.TrimSpace(assignToPane) == "" {
			needsIdleAgent = true
			break
		}
	}
	var idleAgents []assignAgentInfo
	if needsIdleAgent {
		idleAgents, err = getIdleAgents(ctx, session, assignToType, assignVerbose)
		if err != nil {
			if IsJSONOutput() {
				return emitJSONFailureEnvelope(makeRetryEnvelope(
					session, false, nil, "AGENT_ERROR",
					fmt.Sprintf("failed to get idle agents: %v", err), nil,
				))
			}
			return fmt.Errorf("failed to get idle agents: %w", err)
		}
		idleAgents, err = assignRoleEligibleAgents(idleAgents, projectDir, assignTemplate)
		if err != nil {
			return emitRetryFailure(session, "PERSONA_ERROR", err)
		}
	}

	retriedItems := make([]RetryItem, 0, len(failedAssignments))
	skippedItems := make([]RetrySkippedItem, 0, len(failedAssignments))
	var warnings []string

	for _, failed := range failedAssignments {
		if err := ctx.Err(); err != nil {
			return emitRetryFailure(session, robot.ErrCodeTimeout, fmt.Errorf("assignment retry canceled before %s: %w", failed.BeadID, err))
		}
		var targetPane *tmux.Pane
		var targetAgentType string
		recoverAtomic := failed.Status == assignment.StatusClaimed &&
			failed.DispatchState == assignment.DispatchPending &&
			failed.IdempotencyKey != "" && failed.ClaimActor != "" && failed.PendingPrompt != ""

		if recoverAtomic {
			resolvedPane, resolveErr := resolvePendingAssignmentPane(panes, failed)
			if resolveErr != nil {
				skippedItems = append(skippedItems, RetrySkippedItem{
					BeadID: failed.BeadID,
					Reason: resolveErr.Error(),
				})
				continue
			}
			if identityErr := pendingCLIRecoveryIdentityError(projectDir, session, resolvedPane, failed, multiWindow); identityErr != nil {
				skippedItems = append(skippedItems, RetrySkippedItem{
					BeadID: failed.BeadID,
					Reason: identityErr.Error(),
				})
				continue
			}
			if selector := strings.TrimSpace(assignToPane); selector != "" {
				requestedPane, _, selectorErr := resolveDirectAssignmentPane(panes, selector)
				if selectorErr != nil {
					skippedItems = append(skippedItems, RetrySkippedItem{BeadID: failed.BeadID, Reason: selectorErr.Error()})
					continue
				}
				if requestedPane.ID != resolvedPane.ID {
					skippedItems = append(skippedItems, RetrySkippedItem{
						BeadID: failed.BeadID,
						Reason: fmt.Sprintf("pending atomic recovery is bound to pane %s and cannot retarget to %s", assignmentPaneTarget(resolvedPane), assignmentPaneTarget(requestedPane)),
					})
					continue
				}
			}
			targetPane = &resolvedPane
			targetAgentType = failed.AgentType
		} else if selector := strings.TrimSpace(assignToPane); selector != "" {
			resolvedPane, _, resolveErr := resolveDirectAssignmentPane(panes, selector)
			if resolveErr != nil {
				skippedItems = append(skippedItems, RetrySkippedItem{
					BeadID: failed.BeadID,
					Reason: resolveErr.Error(),
				})
				continue
			}
			targetPane = &resolvedPane
			targetAgentType = agentTypeForPane(resolvedPane)
		} else if assignToType != "" {
			for i := range idleAgents {
				if idleAgents[i].agentType == assignToType {
					targetPane = &idleAgents[i].pane
					targetAgentType = assignToType
					idleAgents = append(idleAgents[:i], idleAgents[i+1:]...)
					break
				}
			}
			if targetPane == nil {
				skippedItems = append(skippedItems, RetrySkippedItem{
					BeadID: failed.BeadID,
					Reason: fmt.Sprintf("no idle agent of type %s", assignToType),
				})
				continue
			}
		} else {
			if len(idleAgents) == 0 {
				skippedItems = append(skippedItems, RetrySkippedItem{
					BeadID: failed.BeadID,
					Reason: "no idle agents available",
				})
				continue
			}
			targetPane = &idleAgents[0].pane
			targetAgentType = idleAgents[0].agentType
			idleAgents = idleAgents[1:]
		}
		if targetAgentType == "" {
			targetAgentType = agentTypeForPane(*targetPane)
		}
		if targetAgentType == "user" || targetAgentType == "unknown" {
			skippedItems = append(skippedItems, RetrySkippedItem{
				BeadID: failed.BeadID,
				Reason: fmt.Sprintf("pane %s is not an agent pane (type: %s)", assignmentPaneTarget(*targetPane), targetAgentType),
			})
			continue
		}

		existingAssignment, identityErr := findAssignmentForPhysicalPane(store, *targetPane)
		if identityErr != nil {
			wrapped := fmt.Errorf("resolve active assignment for pane %s: %w", targetPane.ID, identityErr)
			data := &RetryData{
				Summary: RetrySummary{TotalFailed: len(failedAssignments), RetriedCount: len(retriedItems), SkippedCount: len(skippedItems)},
				Retried: retriedItems,
				Skipped: skippedItems,
			}
			return emitTypedAssignmentFailure(wrapped, makeRetryEnvelope(session, false, data, "PANE_IDENTITY_MIGRATION_REQUIRED", wrapped.Error(), warnings))
		}
		if existingAssignment != nil && existingAssignment.BeadID != failed.BeadID {
			skippedItems = append(skippedItems, RetrySkippedItem{
				BeadID: failed.BeadID,
				Reason: fmt.Sprintf("pane %d already has assignment %s", targetPane.Index, existingAssignment.BeadID),
			})
			continue
		}

		liveDetails, detailsErr := readBeadAssignmentDetailsForMutation(ctx, projectDir, failed.BeadID)
		if detailsErr != nil {
			if errors.Is(detailsErr, context.Canceled) || errors.Is(detailsErr, context.DeadlineExceeded) {
				return emitRetryFailure(session, robot.ErrCodeTimeout, detailsErr)
			}
			skippedItems = append(skippedItems, RetrySkippedItem{BeadID: failed.BeadID, Reason: fmt.Sprintf("read bead before retry: %v", detailsErr)})
			continue
		}
		beadTitle := failed.BeadTitle
		if beadTitle == "" && !recoverAtomic {
			beadTitle = liveDetails.Title
			if strings.TrimSpace(beadTitle) == "" {
				skippedItems = append(skippedItems, RetrySkippedItem{BeadID: failed.BeadID, Reason: "live bead title is empty"})
				continue
			}
		}

		newAgentName := assignmentAgentIdentityForPane(projectDir, session, targetAgentType, *targetPane, multiWindow)
		prompt := expandPromptTemplate(failed.BeadID, beadTitle, assignTemplate, assignTemplateFile)
		actor := newAgentName
		idempotencyKey := ""
		baseIntentSHA256 := ""
		var cassInjection *robot.CASSInjectionInfo
		var memoryInjection *robot.CMInjectionInfo
		if recoverAtomic {
			newAgentName = failed.AgentName
			actor = failed.ClaimActor
			idempotencyKey = failed.IdempotencyKey
			prompt = failed.PendingPrompt
		} else {
			idempotencyKey, err = assignment.NewAssignmentIdempotencyKey()
			if err != nil {
				skippedItems = append(skippedItems, RetrySkippedItem{BeadID: failed.BeadID, Reason: err.Error()})
				continue
			}
			prompt, baseIntentSHA256, cassInjection, memoryInjection = enrichAssignmentPrompt(ctx, promptContext, robot.AssignmentPrompt{
				Prompt: prompt, Title: beadTitle, AgentType: targetAgentType, Session: session, ProjectDir: projectDir,
			}, failed.BeadID, nil, assignTimeout)
		}
		reservationRequired := assignReserveFiles
		reservationDiscovery := assignReserveFiles
		var requestedPaths []string
		if recoverAtomic {
			reservationRequired = failed.ReservationRequired
			reservationDiscovery = failed.ReservationDiscovery
			requestedPaths = append([]string(nil), failed.ReservationInputPaths...)
		}
		reservationNeedsRefresh := !failed.ReservationCompleted ||
			(failed.ReservationExpiresAt != nil && !failed.ReservationExpiresAt.After(time.Now().UTC()))
		var retryReservationMgr *assign.FileReservationManager
		if reservationRequired && reservationNeedsRefresh {
			amClient := newAgentMailClient(projectDir)
			if !amClient.IsAvailable() {
				skippedItems = append(skippedItems, RetrySkippedItem{
					BeadID: failed.BeadID,
					Reason: "Agent Mail is unavailable; refusing to bypass the pending file reservation",
				})
				continue
			}
			retryReservationMgr = assign.NewFileReservationManager(amClient, projectDir)
		}
		atomicCoordinator := newCLIAtomicAssignmentCoordinator(store, projectDir, retryReservationMgr)
		assignmentCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(assignTimeout))
		recoveredIntentSHA256 := ""
		if recoverAtomic {
			recoveredIntentSHA256 = failed.IntentSHA256
			if recoveredIntentSHA256 == "" {
				recoveredIntentSHA256 = failed.PromptSHA256
			}
		}
		atomicResult, assignErr := atomicCoordinator.Execute(assignmentCtx, assignment.AtomicRequest{
			BeadID:                    failed.BeadID,
			BeadTitle:                 beadTitle,
			Target:                    targetPane.ID,
			OccupancyKey:              targetPane.ID,
			Pane:                      targetPane.Index,
			AgentType:                 targetAgentType,
			AgentName:                 newAgentName,
			Actor:                     actor,
			Prompt:                    prompt,
			IdempotencyKey:            idempotencyKey,
			RecoveredIntentSHA256:     recoveredIntentSHA256,
			BaseIntentSHA256:          baseIntentSHA256,
			RequireReservation:        reservationRequired,
			AllowReservationDiscovery: reservationDiscovery,
			RequestedPaths:            requestedPaths,
			ReservationTTL:            time.Hour,
		})
		cancel()
		if err := ctx.Err(); err != nil {
			return emitRetryFailure(session, robot.ErrCodeTimeout, fmt.Errorf("assignment retry canceled while assigning %s: %w", failed.BeadID, err))
		}
		if assignErr != nil {
			skippedItems = append(skippedItems, RetrySkippedItem{
				BeadID: failed.BeadID,
				Reason: assignErr.Error(),
			})
			continue
		}
		promptSent := atomicResult.Sent

		retryCount := failed.RetryCount + 1
		update := assignment.AssignmentUpdate{
			RetryCount: &retryCount,
		}
		if err := store.Update(failed.BeadID, update); err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to update assignment metadata for %s: %v", failed.BeadID, err))
		}

		now := time.Now().UTC()
		retriedItems = append(retriedItems, RetryItem{
			BeadID:             failed.BeadID,
			BeadTitle:          forcedRedactedAssignmentText(beadTitle),
			Pane:               targetPane.Index,
			AgentType:          targetAgentType,
			AgentName:          newAgentName,
			Status:             string(assignment.StatusAssigned),
			PromptSent:         promptSent,
			AssignedAt:         now.Format(time.RFC3339),
			PreviousPane:       failed.Pane,
			PreviousAgent:      failed.AgentName,
			PreviousFailReason: failed.FailReason,
			RetryCount:         failed.RetryCount + 1,
			CASSInjection:      cassInjection,
			MemoryInjection:    memoryInjection,
		})
	}

	if err := store.Save(); err != nil {
		warnings = append(warnings, fmt.Sprintf("failed to save assignment store: %v", err))
	}
	retryErrCode, retryErr := classifyRetryOutcome(retriedItems, skippedItems)

	if IsJSONOutput() {
		data := &RetryData{
			Summary: RetrySummary{
				TotalFailed:  len(failedAssignments),
				RetriedCount: len(retriedItems),
				SkippedCount: len(skippedItems),
			},
			Retried: retriedItems,
			Skipped: skippedItems,
		}
		if retryErr != nil {
			return emitJSONFailureEnvelope(makeRetryEnvelope(
				session, false, data, retryErrCode, retryErr.Error(), warnings,
			))
		}
		return json.NewEncoder(os.Stdout).Encode(makeRetryEnvelope(
			session, true, data, "", "", warnings,
		))
	}

	if !assignQuiet {
		fmt.Printf("Retry summary: %d failed, %d retried, %d skipped\n",
			len(failedAssignments), len(retriedItems), len(skippedItems))
		for _, item := range retriedItems {
			fmt.Printf("  Retried %s: pane %d → pane %d (%s)\n",
				item.BeadID, item.PreviousPane, item.Pane, item.AgentType)
		}
		for _, item := range skippedItems {
			fmt.Printf("  Skipped %s: %s\n", item.BeadID, item.Reason)
		}
		for _, w := range warnings {
			fmt.Printf("  Warning: %s\n", w)
		}
	}
	if retryErr != nil {
		return retryErr
	}

	return nil
}

func classifyRetryOutcome(retried []RetryItem, skipped []RetrySkippedItem) (string, error) {
	if len(skipped) == 0 {
		return "", nil
	}
	if len(retried) == 0 {
		if len(skipped) == 1 {
			return "RETRY_SKIPPED", fmt.Errorf("retry %s was skipped: %s", skipped[0].BeadID, skipped[0].Reason)
		}
		return "RETRY_SKIPPED", fmt.Errorf("all %d retryable assignments were skipped", len(skipped))
	}
	return "RETRY_PARTIAL", fmt.Errorf("%d of %d retryable assignments were skipped", len(skipped), len(retried)+len(skipped))
}

// runReassignment handles the --reassign flag for moving a bead between agents.
func runReassignment(ctx context.Context, session string) error {
	if ctx == nil {
		return emitReassignFailure(session, robot.ErrCodeInternalError, "reassignment context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		cancelErr := fmt.Errorf("reassignment canceled: %w", err)
		return emitTypedAssignmentFailure(cancelErr, makeReassignErrorEnvelope(session, robot.ErrCodeTimeout, cancelErr.Error(), nil))
	}
	beadID := strings.TrimSpace(assignReassign)
	if beadID == "" {
		return emitReassignFailure(session, "INVALID_ARGS", "bead ID required for --reassign", nil)
	}
	paneSelector := strings.TrimSpace(assignToPane)
	if paneSelector == "" && assignToType == "" {
		return emitReassignFailure(session, "INVALID_ARGS", "either --to-pane or --to-type must be specified with --reassign", nil)
	}
	if paneSelector != "" && assignToType != "" {
		return emitReassignFailure(session, "INVALID_ARGS", "cannot specify both --to-pane and --to-type", nil)
	}

	store, err := assignment.LoadStoreStrict(session)
	if err != nil {
		return emitReassignFailure(session, "STORE_ERROR", fmt.Sprintf("failed to load assignment store: %v", err), nil)
	}
	projectDir, err := resolveAssignProjectDir(ctx, session)
	if err != nil {
		return emitReassignFailure(session, "PROJECT_ERROR", err.Error(), nil)
	}
	currentAssignment := store.Get(beadID)
	if currentAssignment == nil {
		return emitReassignFailure(session, "NOT_ASSIGNED", fmt.Sprintf("bead %s does not have an active assignment", beadID), nil)
	}
	if currentAssignment.Status != assignment.StatusWorking {
		return emitReassignFailure(session, "INVALID_STATE", fmt.Sprintf("bead %s assignment is not reassignable from status %s", beadID, currentAssignment.Status), map[string]interface{}{
			"current_status": string(currentAssignment.Status),
		})
	}

	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return emitContextAwareReassignFailure(ctx, session, "TMUX_ERROR", fmt.Errorf("failed to get panes: %w", err), nil)
	}
	var targetPane *tmux.Pane
	var targetAgentType string
	if paneSelector != "" {
		resolvedPane, _, resolveErr := resolveDirectAssignmentPane(panes, paneSelector)
		if resolveErr != nil {
			return emitReassignFailure(session, directPaneSelectorErrorCode(resolveErr), resolveErr.Error(), nil)
		}
		targetPane = &resolvedPane
		targetAgentType = agentTypeForPane(resolvedPane)
		if targetAgentType == "user" || targetAgentType == "unknown" {
			return emitReassignFailure(session, "PANE_NOT_FOUND", fmt.Sprintf("pane %s is not an agent pane (type: %s)", assignmentPaneTarget(resolvedPane), targetAgentType), nil)
		}
	} else {
		idleAgents, idleErr := getIdleAgents(ctx, session, assignToType, assignVerbose)
		if idleErr != nil {
			return emitContextAwareReassignFailure(ctx, session, "TMUX_ERROR", idleErr, nil)
		}
		idleAgents, idleErr = assignRoleEligibleAgents(idleAgents, projectDir, assignTemplate)
		if idleErr != nil {
			return emitReassignFailure(session, "PERSONA_ERROR", idleErr.Error(), nil)
		}
		if len(idleAgents) == 0 {
			return emitReassignFailure(session, "NO_IDLE_AGENT", fmt.Sprintf("no idle %s agents available", assignToType), map[string]interface{}{"agent_type": assignToType})
		}
		targetPane = &idleAgents[0].pane
		targetAgentType = idleAgents[0].agentType
	}

	multiWindow := tmux.PanesSpanMultipleWindows(panes)
	alreadyAssigned, identityErr := assignmentMatchesPhysicalPane(*currentAssignment, *targetPane)
	if identityErr != nil {
		wrapped := fmt.Errorf("resolve current assignment %s physical pane: %w", beadID, identityErr)
		return emitTypedAssignmentFailure(wrapped, makeReassignErrorEnvelope(session, "PANE_IDENTITY_MIGRATION_REQUIRED", wrapped.Error(), nil))
	}
	if alreadyAssigned {
		return emitReassignFailure(session, "ALREADY_ASSIGNED", fmt.Sprintf("bead %s is already assigned to pane %s", beadID, assignmentPaneTarget(*targetPane)), nil)
	}
	existingAssignment, identityErr := findAssignmentForPhysicalPane(store, *targetPane)
	if identityErr != nil {
		wrapped := fmt.Errorf("resolve active assignment for pane %s: %w", targetPane.ID, identityErr)
		return emitTypedAssignmentFailure(wrapped, makeReassignErrorEnvelope(session, "PANE_IDENTITY_MIGRATION_REQUIRED", wrapped.Error(), nil))
	}
	if existingAssignment != nil && existingAssignment.BeadID != beadID {
		return emitReassignFailure(session, "TARGET_BUSY", fmt.Sprintf("pane %s already has assignment %s", assignmentPaneTarget(*targetPane), existingAssignment.BeadID), map[string]interface{}{
			"current_bead":   existingAssignment.BeadID,
			"current_status": string(existingAssignment.Status),
		})
	}
	observation, observeErr := observeAssignSessionWithTimeout(ctx, session, assignTimeout)
	if observeErr != nil {
		return emitContextAwareReassignFailure(ctx, session, "OBSERVATION_ERROR", observeErr, nil)
	}
	paneObservation, observeErr := currentAssignPaneObservation(observation, targetPane.ID, time.Now())
	if observeErr != nil {
		return emitContextAwareReassignFailure(ctx, session, "OBSERVATION_ERROR", observeErr, map[string]interface{}{"pane_id": targetPane.ID})
	}
	state := string(paneObservation.Current.Status.State)
	if !paneObservation.SafeToDispatch() && !assignForce {
		return emitReassignFailure(session, "TARGET_BUSY", fmt.Sprintf("pane %s is busy (state: %s), use --force to override", assignmentPaneTarget(*targetPane), state), map[string]interface{}{"pane_state": state})
	}

	liveDetails, detailsErr := readBeadAssignmentDetailsForMutation(ctx, projectDir, beadID)
	if detailsErr != nil {
		return emitContextAwareReassignFailure(ctx, session, "BEAD_LOOKUP_FAILED", detailsErr, nil)
	}
	beadTitle := currentAssignment.BeadTitle
	if beadTitle == "" {
		beadTitle = liveDetails.Title
		if strings.TrimSpace(beadTitle) == "" {
			return emitReassignFailure(session, "BEAD_LOOKUP_FAILED", fmt.Sprintf("br show returned an empty title for %s", beadID), nil)
		}
	}
	prompt := assignPrompt
	if prompt == "" {
		prompt = expandPromptTemplate(beadID, beadTitle, assignTemplate, assignTemplateFile)
	}
	newAgentName := assignmentAgentIdentityForPane(projectDir, session, targetAgentType, *targetPane, multiWindow)
	idempotencyKey, err := assignment.NewAssignmentIdempotencyKey()
	if err != nil {
		return emitReassignFailure(session, "STORE_ERROR", fmt.Sprintf("generate reassignment identity: %v", err), nil)
	}
	prompt, baseIntentSHA256, cassInjection, memoryInjection := enrichAssignmentPrompt(
		ctx, resolvePromptContextOptions(assignWithCASS, assignNoCASS, assignWithMemory, cfg),
		robot.AssignmentPrompt{Prompt: prompt, Title: beadTitle, AgentType: targetAgentType, Session: session, ProjectDir: projectDir},
		beadID, nil, assignTimeout,
	)

	reservationRequired := currentAssignment.ReservationRequired || assignReserveFiles
	reservationDiscovery := currentAssignment.ReservationDiscovery || assignReserveFiles
	requestedPaths := append([]string(nil), currentAssignment.ReservationRequested...)
	if len(requestedPaths) == 0 {
		requestedPaths = append(requestedPaths, currentAssignment.ReservationInputPaths...)
	}
	if len(requestedPaths) > 0 {
		reservationDiscovery = false
	}
	var reservationMgr *assign.FileReservationManager
	if reservationRequired {
		amClient := newAgentMailClient(projectDir)
		if !amClient.IsAvailable() {
			return emitReassignFailure(session, "RESERVATION_REQUIRED", "Agent Mail is unavailable; refusing to transfer a reservation-required assignment", nil)
		}
		reservationMgr = assign.NewFileReservationManager(amClient, projectDir)
		reservationMgr.SetTTL(max(3600, int(resolveAssignTimeout(assignTimeout).Seconds())*2))
	}

	assignmentCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(assignTimeout))
	defer cancel()
	atomicCoordinator := newCLIAtomicAssignmentCoordinator(store, projectDir, reservationMgr, assignForce)
	atomicResult, reassignErr := atomicCoordinator.Execute(assignmentCtx, assignment.AtomicRequest{
		BeadID:                    beadID,
		BeadTitle:                 beadTitle,
		Target:                    targetPane.ID,
		OccupancyKey:              targetPane.ID,
		Pane:                      targetPane.Index,
		AgentType:                 targetAgentType,
		AgentName:                 newAgentName,
		Actor:                     currentAssignment.ClaimActor,
		Prompt:                    prompt,
		IdempotencyKey:            idempotencyKey,
		BaseIntentSHA256:          baseIntentSHA256,
		RequireReservation:        reservationRequired,
		AllowReservationDiscovery: reservationDiscovery,
		RequestedPaths:            requestedPaths,
		ReservationTTL:            time.Hour,
		ReplaceWorkingAssignment:  true,
	})
	reassignErr = preserveCommandContextError(assignmentCtx, reassignErr)
	if reassignErr != nil {
		details := map[string]interface{}{"target": targetPane.ID}
		durableResult := matchingReassignmentDurableResult(atomicResult.Assignment, beadID, idempotencyKey)
		if durableResult != nil {
			details["durable_status"] = string(durableResult.Status)
			details["dispatch_state"] = string(durableResult.DispatchState)
			details["idempotency_key"] = durableResult.IdempotencyKey
		}
		return emitReassignFailure(session, classifyReassignError(reassignErr, durableResult), reassignErr.Error(), details)
	}
	if atomicResult.Assignment == nil || !atomicResult.Sent {
		return emitReassignFailure(session, "SEND_ERROR", "reassignment completed without a durable dispatch receipt", nil)
	}

	durable := atomicResult.Assignment
	releasedReservationCount := len(atomicResult.ReleasedPaths)
	if len(atomicResult.ReleasedReservationIDs) > 0 {
		releasedReservationCount = len(atomicResult.ReleasedReservationIDs)
	}
	data := &ReassignData{
		BeadID:                       beadID,
		BeadTitle:                    durable.BeadTitle,
		Pane:                         durable.Pane,
		AgentType:                    durable.AgentType,
		AgentName:                    durable.AgentName,
		Status:                       string(durable.Status),
		PromptSent:                   atomicResult.Sent,
		AssignedAt:                   durable.AssignedAt.UTC().Format(time.RFC3339),
		PreviousPane:                 currentAssignment.Pane,
		PreviousAgent:                currentAssignment.AgentName,
		PreviousAgentType:            currentAssignment.AgentType,
		PreviousStatus:               string(currentAssignment.Status),
		FileReservationsTransferred:  releasedReservationCount > 0 || len(atomicResult.Lease.Granted) > 0,
		FileReservationsReleasedFrom: releasedReservationCount,
		FileReservationsCreatedFor:   len(atomicResult.Lease.Granted),
		CASSInjection:                cassInjection,
		MemoryInjection:              memoryInjection,
	}
	if IsJSONOutput() {
		return json.NewEncoder(os.Stdout).Encode(ReassignEnvelope{
			Command: "assign", Subcommand: "reassign", Session: session,
			Timestamp: time.Now().UTC().Format(time.RFC3339), Success: true, Data: data, Warnings: []string{},
		})
	}
	if !assignQuiet {
		fmt.Printf("Reassigned %s to pane %s (%s)\n", beadID, assignmentPaneTarget(*targetPane), targetAgentType)
		fmt.Printf("  Previous: pane %d (%s)\n", currentAssignment.Pane, currentAssignment.AgentType)
		displayPrompt := durable.PromptSent
		if displayPrompt == "" {
			displayPrompt = durable.PendingPrompt
		}
		fmt.Printf("  Prompt sent: %s...\n", truncateString(displayPrompt, 50))
		if data.FileReservationsTransferred {
			fmt.Println("  File reservations transferred")
		}
	}
	return nil
}

func matchingReassignmentDurableResult(recorded *assignment.Assignment, beadID, idempotencyKey string) *assignment.Assignment {
	if recorded == nil || recorded.BeadID != strings.TrimSpace(beadID) ||
		strings.TrimSpace(recorded.IdempotencyKey) == "" || recorded.IdempotencyKey != strings.TrimSpace(idempotencyKey) {
		return nil
	}
	return recorded
}

func emitReassignFailure(session, code, message string, details map[string]interface{}) error {
	err := errors.New(message)
	if IsJSONOutput() {
		return emitJSONFailureEnvelope(makeReassignErrorEnvelope(session, code, message, details))
	}
	return err
}

func emitTypedAssignmentFailure(err error, envelope interface{}) error {
	if !IsJSONOutput() {
		return err
	}
	return errors.Join(emitJSONFailureEnvelope(envelope), err)
}

func preserveCommandContextError(ctx context.Context, err error) error {
	if ctx == nil || err == nil {
		return err
	}
	ctxErr := ctx.Err()
	if ctxErr == nil || errors.Is(err, ctxErr) {
		return err
	}
	return errors.Join(err, ctxErr)
}

func emitContextAwareReassignFailure(ctx context.Context, session, fallbackCode string, err error, details map[string]interface{}) error {
	err = preserveCommandContextError(ctx, err)
	code := fallbackCode
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		code = robot.ErrCodeTimeout
	}
	return emitTypedAssignmentFailure(err, makeReassignErrorEnvelope(session, code, err.Error(), details))
}

func classifyReassignError(err error, durable *assignment.Assignment) string {
	var dispatchErr *dispatchsvc.Error
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return robot.ErrCodeTimeout
	case errors.Is(err, assignment.ErrTargetOccupied):
		return "TARGET_BUSY"
	case errors.Is(err, assignment.ErrClaimIneligible):
		return "BEAD_INELIGIBLE"
	case errors.Is(err, assignment.ErrClaimConflict):
		return "CLAIM_CONFLICT"
	case errors.Is(err, assignment.ErrReservationRequired), errors.Is(err, assignment.ErrReservationReleaseRequired):
		return "RESERVATION_REQUIRED"
	case errors.Is(err, assignment.ErrDispatchOutcomeUnknown):
		return "DISPATCH_UNKNOWN"
	case errors.Is(err, assignment.ErrWorkingReplacementNotAllowed), errors.Is(err, assignment.ErrAssignmentStatusMismatch):
		return "INVALID_STATE"
	case errors.As(err, &dispatchErr) && dispatchErr.Code == dispatchsvc.ErrRedactionBlocked:
		return "SENSITIVE_DATA_BLOCKED"
	case durable != nil && durable.ClearState == assignment.ClearStateReservationReleasing && strings.TrimSpace(durable.ClearError) != "":
		return "RESERVATION_RELEASE_FAILED"
	case durable != nil && durable.DispatchAttempts > 0:
		return "SEND_ERROR"
	default:
		return "REASSIGN_ERROR"
	}
}

func makeReassignErrorEnvelope(session, code, message string, details map[string]interface{}) AssignEnvelope[ReassignData] {
	return AssignEnvelope[ReassignData]{
		Command:    "assign",
		Subcommand: "reassign",
		Session:    session,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Success:    false,
		Warnings:   []string{},
		Error: &AssignError{
			Code:    code,
			Message: message,
			Details: details,
		},
	}
}

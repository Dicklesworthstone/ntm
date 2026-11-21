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
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ============================================================================
// Direct Pane Assignment - ntm assign --pane
// ============================================================================

// DirectAssignItem represents a single direct assignment in JSON output.
type DirectAssignItem struct {
	BeadID          string                   `json:"bead_id"`
	BeadTitle       string                   `json:"bead_title"`
	Pane            int                      `json:"pane"` // Window-local display index.
	PaneTarget      string                   `json:"pane_target"`
	PaneID          string                   `json:"pane_id"`
	AgentType       string                   `json:"agent_type"`
	Status          string                   `json:"status"`
	Prompt          string                   `json:"prompt"`
	PromptSent      bool                     `json:"prompt_sent"`
	AssignedAt      string                   `json:"assigned_at"`
	PaneWasBusy     bool                     `json:"pane_was_busy,omitempty"`
	DepsIgnored     bool                     `json:"deps_ignored,omitempty"`
	BlockedByIDs    []string                 `json:"blocked_by_ids,omitempty"`
	CASSInjection   *robot.CASSInjectionInfo `json:"cass_injection,omitempty"`
	MemoryInjection *robot.CMInjectionInfo   `json:"memory_injection,omitempty"`
}

// DirectAssignFileReservations holds file reservation details for direct assignment.
type DirectAssignFileReservations struct {
	Requested []string `json:"requested"`
	Granted   []string `json:"granted"`
	Denied    []string `json:"denied"`
}

// DirectAssignData holds the data for a direct pane assignment.
type DirectAssignData struct {
	Assignment       *DirectAssignItem             `json:"assignment"`
	FileReservations *DirectAssignFileReservations `json:"file_reservations,omitempty"`
	Receipt          *DispatchReceipt              `json:"receipt,omitempty"`
}

// DirectAssignResult is the result of a direct pane assignment.
type DirectAssignResult struct {
	BeadID         string                        `json:"bead_id"`
	BeadTitle      string                        `json:"bead_title,omitempty"`
	Pane           int                           `json:"pane"` // Window-local display index.
	PaneTarget     string                        `json:"pane_target"`
	PaneID         string                        `json:"pane_id"`
	AgentType      string                        `json:"agent_type"`
	AgentName      string                        `json:"agent_name,omitempty"`
	PromptSent     string                        `json:"prompt_sent"`
	Success        bool                          `json:"success"`
	Error          string                        `json:"error,omitempty"`
	Reservations   *assign.FileReservationResult `json:"reservations,omitempty"`
	PaneWasBusy    bool                          `json:"pane_was_busy,omitempty"`
	DepsIgnored    bool                          `json:"deps_ignored,omitempty"`
	BlockedByBeads []string                      `json:"blocked_by_beads,omitempty"`
	Receipt        *DispatchReceipt              `json:"receipt,omitempty"`
}

// DispatchReceipt is the stable per-dispatch envelope wrappers consume.
type DispatchReceipt struct {
	WorkItemID  string                  `json:"work_item_id"`
	Pane        DispatchPaneRef         `json:"pane"`
	Prompt      DispatchPromptInfo      `json:"prompt"`
	Reservation *DispatchReservation    `json:"reservation,omitempty"`
	Transport   DispatchTransportStatus `json:"transport"`
	Timestamp   string                  `json:"timestamp"`
	DryRun      bool                    `json:"dry_run,omitempty"`
}

// DispatchPaneRef identifies the pane the work was dispatched to.
type DispatchPaneRef struct {
	Session     string `json:"session"`
	Target      string `json:"target"`
	WindowIndex int    `json:"window_index"`
	Index       int    `json:"index"`
	ID          string `json:"id"`
	Title       string `json:"title,omitempty"`
}

// DispatchPromptInfo summarizes the prompt that was sent.
type DispatchPromptInfo struct {
	Length     int    `json:"length"`
	HashSHA256 string `json:"hash_sha256"`
	Source     string `json:"source,omitempty"`
}

// DispatchReservation summarizes the file-reservation outcome at dispatch time.
type DispatchReservation struct {
	Requested []string `json:"requested,omitempty"`
	Granted   []string `json:"granted,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
}

// DispatchTransportStatus captures whether the prompt actually reached the pane.
type DispatchTransportStatus struct {
	Sent       bool   `json:"sent"`
	DeliveryID string `json:"delivery_id,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

func makeDirectAssignEnvelope(session string, success bool, data *DirectAssignData, errCode, errMsg string, warnings []string) AssignEnvelope[DirectAssignData] {
	if warnings == nil {
		warnings = []string{}
	}
	envelope := AssignEnvelope[DirectAssignData]{
		Command:    "assign",
		Subcommand: "pane",
		Session:    session,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Success:    success,
		Data:       data,
		Warnings:   warnings,
		Error:      nil,
	}
	if errCode != "" {
		envelope.Error = &AssignError{
			Code:    errCode,
			Message: errMsg,
		}
	}
	return envelope
}

func resolveDirectAssignmentPane(panes []tmux.Pane, selector string) (tmux.Pane, string, error) {
	resolved, err := tmux.ResolvePaneSelectors(panes, []string{selector}, true)
	if err != nil {
		return tmux.Pane{}, "", err
	}
	pane := resolved[0]
	return pane, tmux.PaneTargetKey(pane, tmux.PanesSpanMultipleWindows(panes)), nil
}

func directPaneSelectorErrorCode(err error) string {
	var selectorErr *tmux.PaneSelectorError
	if !errors.As(err, &selectorErr) {
		return "PANE_NOT_FOUND"
	}
	switch selectorErr.Kind {
	case tmux.PaneSelectorAmbiguous:
		return "PANE_AMBIGUOUS"
	case tmux.PaneSelectorNotFound:
		return "PANE_NOT_FOUND"
	default:
		return "INVALID_ARGS"
	}
}

type directAssignmentIntent struct {
	idempotencyKey string
	promptSource   string
	explicitPrompt bool
}

func snapshotDirectAssignmentIntent(opts *AssignCommandOptions, beadID, paneID string, clearedGeneration ...uint64) directAssignmentIntent {
	intent := directAssignmentIntent{
		promptSource:   opts.Prompt,
		explicitPrompt: opts.Prompt != "",
	}
	if !intent.explicitPrompt {
		intent.promptSource = promptTemplateSource(opts.Template, opts.TemplateFile)
	}
	keyParts := []string{
		"ntm/direct-assign/v3",
		opts.Session,
		beadID,
		paneID,
		assignment.PromptSHA256(intent.promptSource),
		strings.ToLower(strings.TrimSpace(opts.Template)),
		strings.TrimSpace(opts.TemplateFile),
		fmt.Sprintf("force=%t", opts.Force),
		fmt.Sprintf("ignore-deps=%t", opts.IgnoreDeps),
		fmt.Sprintf("reserve-files=%t", opts.ReserveFiles),
	}
	if len(clearedGeneration) > 0 && clearedGeneration[0] > 0 {
		keyParts = append(keyParts, fmt.Sprintf("cleared-generation=%d", clearedGeneration[0]))
	}
	intent.idempotencyKey = assignment.AssignmentIdempotencyKey(keyParts...)
	return intent
}

func (intent directAssignmentIntent) prompt(beadID, beadTitle string) string {
	if intent.explicitPrompt {
		return intent.promptSource
	}
	return renderPromptTemplate(beadID, beadTitle, intent.promptSource)
}

func directAssignmentReservationManager(opts *AssignCommandOptions, projectDir string) (*assign.FileReservationManager, error) {
	if !opts.ReserveFiles {
		return nil, nil
	}
	amClient := newAgentMailClient(projectDir)
	if !amClient.IsAvailable() {
		return nil, assignment.ErrReservationRequired
	}
	manager := assign.NewFileReservationManager(amClient, projectDir)
	ttlSeconds := int(resolveAssignTimeout(opts.Timeout).Seconds()) * 2
	if ttlSeconds < 3600 {
		ttlSeconds = 3600
	}
	manager.SetTTL(ttlSeconds)
	return manager, nil
}

func directAssignmentActor(session, paneID string) string {
	return fmt.Sprintf("%s-pane-%s", session, strings.TrimPrefix(strings.TrimSpace(paneID), "%"))
}

// runDirectPaneAssignment handles the --pane flag for direct bead-to-pane assignment.
func runDirectPaneAssignment(ctx context.Context, opts *AssignCommandOptions) error {
	if ctx == nil {
		err := errors.New("direct assignment context is required")
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, robot.ErrCodeInternalError, err.Error(), nil))
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		cancelErr := fmt.Errorf("direct assignment canceled: %w", err)
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, robot.ErrCodeTimeout, cancelErr.Error(), nil))
		}
		return cancelErr
	}
	var warnings []string

	if len(opts.BeadIDs) != 1 {
		err := fmt.Errorf("--pane requires exactly one bead (use --beads=bd-xxx)")
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, "INVALID_ARGS", err.Error(), nil))
		}
		return err
	}

	beadID := opts.BeadIDs[0]
	projectDir := strings.TrimSpace(opts.ProjectDir)
	if projectDir == "" {
		var err error
		projectDir, err = resolveAssignProjectDir(ctx, opts.Session)
		if err != nil {
			err = fmt.Errorf("resolve bead project for atomic claim: %w", err)
			if IsJSONOutput() {
				return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, "PROJECT_ERROR", err.Error(), warnings))
			}
			return err
		}
		opts.ProjectDir = projectDir
	}
	if err := ensureAuthoritativeAssignmentPolicy(projectDir, &opts.policyProject); err != nil {
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, robot.ErrCodeInvalidFlag, err.Error(), warnings))
		}
		return err
	}

	panes, err := tmux.GetPanesContext(ctx, opts.Session)
	if err != nil {
		err = fmt.Errorf("failed to get panes: %w", err)
		if IsJSONOutput() {
			code := "TMUX_ERROR"
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				code = robot.ErrCodeTimeout
			}
			return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, code, err.Error(), nil))
		}
		return err
	}

	targetPane, canonicalTarget, err := resolveDirectAssignmentPane(panes, opts.PaneSelector)
	if err != nil {
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, directPaneSelectorErrorCode(err), err.Error(), nil))
		}
		return err
	}
	store, err := assignment.LoadStoreStrict(opts.Session)
	if err != nil {
		err = fmt.Errorf("load assignment store: %w", err)
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, "STORE_ERROR", err.Error(), warnings))
		}
		return err
	}
	intent := snapshotDirectAssignmentIntent(opts, beadID, targetPane.ID, store.ClearedGeneration(beadID))
	idempotencyKey := intent.idempotencyKey

	prior := store.Get(beadID)
	if prior != nil && prior.IdempotencyKey == idempotencyKey {
		switch prior.Status {
		case assignment.StatusCompleted, assignment.StatusFailed, assignment.StatusReassigned:
			idempotencyKey, err = assignment.NewAssignmentIdempotencyKey()
			if err != nil {
				err = fmt.Errorf("generate reopened assignment identity: %w", err)
				if IsJSONOutput() {
					return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, "STORE_ERROR", err.Error(), warnings))
				}
				return err
			}
			intent.idempotencyKey = idempotencyKey
		}
	}
	sameIntent := prior != nil && prior.IdempotencyKey == idempotencyKey
	activeDifferentIntent := false
	if prior != nil && prior.IdempotencyKey != "" && !sameIntent {
		switch prior.Status {
		case assignment.StatusClaiming, assignment.StatusClaimed, assignment.StatusAssigned, assignment.StatusWorking:
			activeDifferentIntent = true
		}
	}
	executeBeforePreflight := sameIntent || activeDifferentIntent

	multiWindow := tmux.PanesSpanMultipleWindows(panes)
	agentType := agentTypeForPane(targetPane)
	agentName := assignmentAgentIdentityForPane(opts.ProjectDir, opts.Session, agentType, targetPane, multiWindow)
	beadTitle := ""
	var directRequestedPaths []string
	if executeBeforePreflight {
		beadTitle = prior.BeadTitle
		if sameIntent {
			if prior.AgentType != "" {
				agentType = prior.AgentType
			}
			if prior.AgentName != "" {
				agentName = prior.AgentName
			}
		}
	}
	prompt := intent.prompt(beadID, beadTitle)

	assignItem := &DirectAssignItem{
		BeadID:      beadID,
		BeadTitle:   "",
		Pane:        targetPane.Index,
		PaneTarget:  canonicalTarget,
		PaneID:      targetPane.ID,
		AgentType:   agentType,
		Status:      "planned",
		DepsIgnored: opts.IgnoreDeps,
	}

	baseIntentSHA256 := ""
	if !executeBeforePreflight {
		if agentType == "user" || agentType == "unknown" {
			err = fmt.Errorf("pane %s is not an agent pane (type: %s)", canonicalTarget, agentType)
			if IsJSONOutput() {
				return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, "NOT_AGENT_PANE", err.Error(), nil))
			}
			return err
		}

		observation, observationErr := observeAssignSessionWithTimeout(ctx, opts.Session, opts.Timeout)
		if observationErr != nil {
			if IsJSONOutput() {
				data := &DirectAssignData{Assignment: assignItem}
				return emitTypedAssignmentFailure(observationErr, makeDirectAssignEnvelope(
					opts.Session, false, data, assignmentObservationFailureCode(observationErr), observationErr.Error(), nil,
				))
			}
			return observationErr
		}
		paneObservation, observationErr := currentAssignPaneObservation(observation, targetPane.ID, time.Now())
		if observationErr != nil {
			if IsJSONOutput() {
				data := &DirectAssignData{Assignment: assignItem}
				return emitTypedAssignmentFailure(observationErr, makeDirectAssignEnvelope(
					opts.Session, false, data, assignmentObservationFailureCode(observationErr), observationErr.Error(), nil,
				))
			}
			return observationErr
		}
		state := string(paneObservation.Current.Status.State)
		if !paneObservation.SafeToDispatch() && !opts.Force {
			assignItem.PaneWasBusy = true
			errMsg := fmt.Sprintf("pane %s is busy (state: %s), use --force to override", canonicalTarget, state)
			if IsJSONOutput() {
				data := &DirectAssignData{Assignment: assignItem}
				return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, data, "PANE_BUSY", errMsg, nil))
			}
			return errors.New(errMsg)
		}
		assignItem.PaneWasBusy = !paneObservation.SafeToDispatch()

		if !opts.IgnoreDeps {
			blockers, blockersErr := bv.ReadForMutationContext(ctx, projectDir, func() ([]string, error) {
				return getBeadBlockers(ctx, projectDir, beadID)
			})
			if blockersErr != nil {
				errMsg := fmt.Sprintf("could not verify dependencies for %s: %v; use --ignore-deps to override", beadID, blockersErr)
				if IsJSONOutput() {
					data := &DirectAssignData{Assignment: assignItem}
					return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, data, "DEPENDENCY_LOOKUP_FAILED", errMsg, nil))
				}
				return errors.New(errMsg)
			}
			if len(blockers) > 0 {
				assignItem.BlockedByIDs = blockers
				errMsg := fmt.Sprintf("bead %s is blocked by: %v, use --ignore-deps to override", beadID, blockers)
				if IsJSONOutput() {
					data := &DirectAssignData{Assignment: assignItem}
					return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, data, "BLOCKED", errMsg, nil))
				}
				return errors.New(errMsg)
			}
		}

		if opts.Prompt == "" || opts.ReserveFiles {
			beadTitle, err = bv.ReadForMutationContext(ctx, projectDir, func() (string, error) {
				return getBeadTitle(ctx, projectDir, beadID)
			})
			if err != nil {
				errMsg := fmt.Sprintf("look up bead %s title: %v", beadID, err)
				if IsJSONOutput() {
					return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, &DirectAssignData{Assignment: assignItem}, "BEAD_LOOKUP_FAILED", errMsg, nil))
				}
				return errors.New(errMsg)
			}
		}
		prompt = intent.prompt(beadID, beadTitle)

		var liveDetails *bv.BeadAssignmentDetails
		if opts.ReserveFiles {
			if details, detailsErr := getBeadAssignmentDetailsForAssignment(ctx, projectDir, beadID); detailsErr == nil && details != nil {
				liveDetails = details
				paths, pathsErr := assign.ReservationPathsForAssignment(details.Title, details.Description)
				if pathsErr != nil {
					errMsg := fmt.Sprintf("not assigning %s: %v", beadID, pathsErr)
					if IsJSONOutput() {
						return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, &DirectAssignData{Assignment: assignItem}, "RESERVATION_REQUIRED", errMsg, warnings))
					}
					return errors.New(errMsg)
				}
				directRequestedPaths = paths
			}
		}

		prompt, baseIntentSHA256, assignItem.CASSInjection, assignItem.MemoryInjection = enrichAssignmentPrompt(
			ctx, resolvePromptContextOptions(opts.WithCASS, opts.NoCASS, opts.WithMemory, cfg),
			robot.AssignmentPrompt{Prompt: prompt, Title: beadTitle, AgentType: agentType, Session: opts.Session, ProjectDir: projectDir},
			beadID, liveDetails, opts.Timeout,
		)
		if !opts.Quiet && !IsJSONOutput() {
			printAssignmentPromptContext(beadID, assignItem.CASSInjection, assignItem.MemoryInjection)
		}
	}

	request := assignment.AtomicRequest{
		BeadID:                    beadID,
		BeadTitle:                 beadTitle,
		Target:                    targetPane.ID,
		OccupancyKey:              targetPane.ID,
		Pane:                      targetPane.Index,
		AgentType:                 agentType,
		AgentName:                 agentName,
		Actor:                     directAssignmentActor(opts.Session, targetPane.ID),
		Prompt:                    prompt,
		IdempotencyKey:            idempotencyKey,
		BaseIntentSHA256:          baseIntentSHA256,
		RequireReservation:        opts.ReserveFiles,
		RequestedPaths:            directRequestedPaths,
		AllowReservationDiscovery: opts.ReserveFiles,
		ReservationTTL:            time.Hour,
	}
	if sameIntent {
		request.Pane = prior.Pane
		request.RecoveredIntentSHA256 = strings.TrimSpace(prior.IntentSHA256)
		if request.RecoveredIntentSHA256 == "" {
			request.RecoveredIntentSHA256 = strings.TrimSpace(prior.PromptSHA256)
		}
		if prior.OccupancyKey != "" {
			request.OccupancyKey = prior.OccupancyKey
			request.Target = prior.OccupancyKey
		} else if prior.DispatchTarget != "" {
			request.Target = prior.DispatchTarget
		}
	}

	var reservationMgr *assign.FileReservationManager
	needsReservationPort := !executeBeforePreflight || (sameIntent && prior.DispatchState != assignment.DispatchSent && prior.DispatchState != assignment.DispatchSending)
	if needsReservationPort {
		if _, readErr := readBeadAssignmentDetailsForMutation(ctx, projectDir, beadID); readErr != nil {
			if IsJSONOutput() {
				code := "BEAD_LOOKUP_FAILED"
				if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
					code = robot.ErrCodeTimeout
				}
				return emitTypedAssignmentFailure(readErr, makeDirectAssignEnvelope(opts.Session, false, &DirectAssignData{Assignment: assignItem}, code, readErr.Error(), warnings))
			}
			return readErr
		}
		reservationMgr, err = directAssignmentReservationManager(opts, projectDir)
	}
	if err != nil {
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, "RESERVATION_REQUIRED", err.Error(), warnings))
		}
		return err
	}
	atomicCoordinator := newCLIAtomicAssignmentCoordinator(store, projectDir, reservationMgr, opts.Force || opts.Relax)
	assignmentCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(opts.Timeout))
	atomicResult, assignErr := atomicCoordinator.Execute(assignmentCtx, request)
	cancel()
	if err := ctx.Err(); err != nil {
		cancelErr := fmt.Errorf("direct assignment canceled while assigning %s: %w", beadID, err)
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeDirectAssignEnvelope(
				opts.Session, false, &DirectAssignData{Assignment: assignItem}, robot.ErrCodeTimeout, cancelErr.Error(), warnings,
			))
		}
		return cancelErr
	}
	assignItem.PromptSent = atomicResult.Sent
	durablePrompt := ""
	durableAssignment := atomicResult.Assignment
	if durableAssignment != nil && (durableAssignment.BeadID != beadID || durableAssignment.IdempotencyKey != idempotencyKey) {
		durableAssignment = nil
	}
	if durableAssignment != nil {
		assignItem.BeadTitle = forcedRedactedAssignmentText(durableAssignment.BeadTitle)
		assignItem.Pane = durableAssignment.Pane
		assignItem.AgentType = durableAssignment.AgentType
		assignItem.Status = string(durableAssignment.Status)
		assignItem.AssignedAt = durableAssignment.AssignedAt.UTC().Format(time.RFC3339)
		if durableAssignment.OccupancyKey != "" {
			assignItem.PaneID = durableAssignment.OccupancyKey
		}
		durablePrompt = durableAssignment.PromptSent
		if durablePrompt == "" {
			durablePrompt = durableAssignment.PendingPrompt
		}
	}
	assignItem.Prompt = forcedRedactedAssignmentText(durablePrompt)

	leaseRequested := atomicResult.Lease.Requested
	leaseGranted := atomicResult.Lease.Granted
	if durableAssignment != nil && len(leaseRequested) == 0 && len(leaseGranted) == 0 {
		leaseRequested = durableAssignment.ReservationRequested
		leaseGranted = durableAssignment.ReservedPaths
	}
	var fileReservations *DirectAssignFileReservations
	if len(leaseRequested) > 0 || len(leaseGranted) > 0 {
		grantedSet := make(map[string]struct{}, len(leaseGranted))
		for _, path := range leaseGranted {
			grantedSet[path] = struct{}{}
		}
		var denied []string
		for _, path := range leaseRequested {
			if _, ok := grantedSet[path]; !ok {
				denied = append(denied, path)
			}
		}
		fileReservations = &DirectAssignFileReservations{
			Requested: leaseRequested,
			Granted:   leaseGranted,
			Denied:    denied,
		}
	}
	var receipt *DispatchReceipt
	attemptedThisCall := prior == nil || prior.IdempotencyKey == "" || (sameIntent && prior.DispatchAttempts == 0)
	if durableAssignment != nil && durableAssignment.DispatchAttempts > 0 &&
		(atomicResult.Replayed || atomicResult.Sent || attemptedThisCall) {
		receipt = buildDispatchReceipt(
			opts.Session, beadID, targetPane, assignItem.PaneTarget, durablePrompt, opts.Template, fileReservations,
			atomicResult.Dispatch.DeliveryID, atomicResult.Sent, assignErr, atomicResult.Dispatch.Duration.Milliseconds(), atomicResult.Assignment.DispatchedAt, false,
		)
	}
	if assignErr != nil {
		code := "ASSIGN_ERROR"
		switch {
		case errors.Is(assignErr, context.Canceled), errors.Is(assignErr, context.DeadlineExceeded):
			code = robot.ErrCodeTimeout
		case errors.Is(assignErr, assignment.ErrClaimIneligible):
			code = "BEAD_INELIGIBLE"
		case errors.Is(assignErr, assignment.ErrClaimConflict):
			code = "CLAIM_CONFLICT"
		case errors.Is(assignErr, assignment.ErrPaneIdentityMigrationRequired):
			code = "PANE_IDENTITY_MIGRATION_REQUIRED"
		case errors.Is(assignErr, assignment.ErrTargetOccupied):
			code = "TARGET_BUSY"
		case errors.Is(assignErr, assignment.ErrTerminalAssignmentAttempt):
			code = "BEAD_NOT_REOPENED"
		case errors.Is(assignErr, assignment.ErrDispatchOutcomeUnknown):
			code = "DISPATCH_UNKNOWN"
		case durableAssignment != nil && durableAssignment.DispatchAttempts > 0:
			code = "SEND_ERROR"
		}
		if IsJSONOutput() {
			data := &DirectAssignData{Assignment: assignItem, FileReservations: fileReservations, Receipt: receipt}
			return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, data, code, assignErr.Error(), warnings))
		}
		return assignErr
	}

	if IsJSONOutput() {
		data := &DirectAssignData{
			Assignment:       assignItem,
			FileReservations: fileReservations,
			Receipt:          receipt,
		}
		return json.NewEncoder(os.Stdout).Encode(makeDirectAssignEnvelope(opts.Session, true, data, "", "", warnings))
	}

	if !opts.Quiet {
		fmt.Printf("✓ Assigned %s to pane %s (%s)\n", beadID, assignItem.PaneTarget, agentType)
		if assignItem.BeadTitle != "" {
			fmt.Printf("  Title: %s\n", assignItem.BeadTitle)
		}
		if assignItem.PaneWasBusy {
			fmt.Printf("  Note: Pane was busy (--force used)\n")
		}
		if assignItem.DepsIgnored {
			fmt.Printf("  Note: Dependencies ignored (--ignore-deps used)\n")
		}
		if fileReservations != nil && len(fileReservations.Granted) > 0 {
			fmt.Printf("  Reserved: %v\n", fileReservations.Granted)
		}
		fmt.Printf("  Prompt: %s\n", assignItem.Prompt)
	}

	return nil
}

func getBeadBlockers(ctx context.Context, projectDir, beadID string) ([]string, error) {
	details, err := getBeadAssignmentDetailsForAssignment(ctx, projectDir, beadID)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), details.BlockedBy...), nil
}

func getBeadTitle(ctx context.Context, projectDir, beadID string) (string, error) {
	details, err := getBeadAssignmentDetailsForAssignment(ctx, projectDir, beadID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(details.Title) == "" {
		return "", fmt.Errorf("br show returned an empty title for %s", beadID)
	}
	return details.Title, nil
}

func buildDispatchReceipt(
	session, workItemID string,
	pane tmux.Pane,
	paneTarget string,
	prompt, templateSource string,
	res *DirectAssignFileReservations,
	deliveryID string,
	sent bool,
	transportErr error,
	durationMs int64,
	dispatchedAt *time.Time,
	dryRun bool,
) *DispatchReceipt {
	timestamp := time.Now().UTC()
	if dispatchedAt != nil {
		timestamp = dispatchedAt.UTC()
	}
	r := &DispatchReceipt{
		WorkItemID: workItemID,
		Pane: DispatchPaneRef{
			Session:     session,
			Target:      paneTarget,
			WindowIndex: pane.WindowIndex,
			Index:       pane.Index,
			ID:          pane.ID,
			Title:       pane.Title,
		},
		Prompt: DispatchPromptInfo{
			Length:     len(prompt),
			HashSHA256: dispatchPromptHash(prompt),
			Source:     templateSource,
		},
		Transport: DispatchTransportStatus{
			Sent:       sent && transportErr == nil && !dryRun,
			DeliveryID: deliveryID,
			DurationMs: durationMs,
		},
		Timestamp: timestamp.Format(time.RFC3339Nano),
		DryRun:    dryRun,
	}
	if transportErr != nil {
		r.Transport.Error = transportErr.Error()
	}
	if res != nil {
		r.Reservation = &DispatchReservation{
			Requested: res.Requested,
			Granted:   res.Granted,
			Conflicts: res.Denied,
		}
	}
	return r
}

func dispatchPromptHash(prompt string) string {
	return assign.PromptHashSHA256(prompt)
}

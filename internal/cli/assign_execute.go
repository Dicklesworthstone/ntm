package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/assign"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func resolveAssignmentItemPane(panes []tmux.Pane, item AssignmentItem) (tmux.Pane, error) {
	selectors := make([]string, 0, 2)
	if paneID := strings.TrimSpace(item.PaneID); paneID != "" {
		selectors = append(selectors, paneID)
	}
	if target := strings.TrimSpace(item.PaneTarget); target != "" {
		selectors = append(selectors, target)
	}
	if len(selectors) > 0 {
		resolved, err := tmux.ResolvePaneSelectors(panes, selectors, true)
		if err != nil {
			return tmux.Pane{}, err
		}
		return resolved[0], nil
	}

	return tmux.Pane{}, &assignment.PaneIdentityMigrationError{BeadID: item.BeadID, Pane: item.Pane}
}

func resolvePendingAssignmentPane(panes []tmux.Pane, pending assignment.Assignment) (tmux.Pane, error) {
	physicalID, err := assignment.CanonicalPaneIdentity(&pending)
	if err != nil {
		return tmux.Pane{}, fmt.Errorf("pending assignment has no canonical physical pane ID: %w", err)
	}
	for _, pane := range panes {
		if pane.ID == physicalID {
			return pane, nil
		}
	}
	return tmux.Pane{}, fmt.Errorf("original physical pane %s is unavailable; refusing to transfer an atomic claim", physicalID)
}

func pendingCLIRecoveryIdentityError(projectDir, session string, pane tmux.Pane, pending assignment.Assignment, multiWindow bool) error {
	currentType := agentTypeForPane(pane)
	if currentType == "user" || currentType == "unknown" {
		return fmt.Errorf("pending atomic recovery target %s is not an agent pane (type: %s)", assignmentPaneTarget(pane), currentType)
	}
	if agent.AgentType(currentType).Canonical() != agent.AgentType(pending.AgentType).Canonical() {
		return fmt.Errorf("pending atomic recovery target %s changed agent type from %s to %s", assignmentPaneTarget(pane), pending.AgentType, currentType)
	}
	currentAgentName := assignmentAgentIdentityForPane(projectDir, session, currentType, pane, multiWindow)
	if strings.TrimSpace(pending.AgentName) != "" && currentAgentName != pending.AgentName {
		return fmt.Errorf("pending atomic recovery target %s changed Agent Mail identity from %s to %s", assignmentPaneTarget(pane), pending.AgentName, currentAgentName)
	}
	return nil
}

func executeAssignmentsEnhanced(ctx context.Context, session string, out *AssignOutputEnhanced, opts *AssignCommandOptions) error {
	if ctx == nil {
		return errors.New("assignment execution context is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("assignment execution canceled: %w", err)
	}
	projectDir := strings.TrimSpace(opts.ProjectDir)
	if projectDir == "" {
		var err error
		projectDir, err = resolveAssignProjectDir(ctx, session)
		if err != nil {
			return fmt.Errorf("resolve bead project for atomic claim: %w", err)
		}
		opts.ProjectDir = projectDir
	}
	if err := ensureAuthoritativeAssignmentPolicy(projectDir, &opts.policyProject); err != nil {
		return err
	}
	if !opts.Quiet {
		fmt.Println()
		fmt.Println("Executing assignments...")
	}

	store, err := assignment.LoadStoreStrict(session)
	if err != nil {
		return fmt.Errorf("load assignment store: %w", err)
	}

	var reservationMgr *assign.FileReservationManager
	if opts.ReserveFiles {
		amClient := newAgentMailClient(projectDir)
		if !amClient.IsAvailable() {
			return assignment.ErrReservationRequired
		}
		reservationMgr = assign.NewFileReservationManager(amClient, projectDir)
		ttlSeconds := int(opts.Timeout.Seconds()) * 2
		if ttlSeconds < 3600 {
			ttlSeconds = 3600
		}
		reservationMgr.SetTTL(ttlSeconds)
		if !opts.Quiet && opts.Verbose {
			fmt.Println("  File reservation enabled via Agent Mail")
		}
	}
	atomicCoordinator := newCLIAtomicAssignmentCoordinator(store, projectDir, reservationMgr, opts.Force || opts.Relax)
	promptContext := resolvePromptContextOptions(opts.WithCASS, opts.NoCASS, opts.WithMemory, cfg)
	activeBeads := make(map[string]struct{})
	for _, active := range store.ListActive() {
		if active != nil && strings.TrimSpace(active.BeadID) != "" {
			activeBeads[active.BeadID] = struct{}{}
		}
	}

	var successCount, failCount, reservedCount int
	out.Summary.AssignedCount = 0

	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return fmt.Errorf("failed to get panes: %w", err)
	}
	observer := newAssignSessionObserver()
	for i := range out.Assignments {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("assignment execution canceled before item %d: %w", i+1, err)
		}
		item := &out.Assignments[i]
		detailsCtx, detailsCancel := context.WithTimeout(ctx, resolveAssignTimeout(opts.Timeout))
		liveDetails, detailsErr := readBeadAssignmentDetailsForMutation(detailsCtx, projectDir, item.BeadID)
		detailsCancel()
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("assignment execution canceled while validating %s: %w", item.BeadID, err)
		}
		if detailsErr != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("%s: live bead validation: %v", item.BeadID, detailsErr))
			failCount++
			continue
		}
		if skip := classifyLiveAssignmentDetailsForProject(projectDir, liveDetails, activeBeads); skip != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("%s: live bead validation: %s", item.BeadID, skip.Reason))
			failCount++
			continue
		}
		item.rawBeadTitle = liveDetails.Title
		item.BeadTitle = forcedRedactedAssignmentText(liveDetails.Title)
		rawBeadTitle := liveDetails.Title

		var requestedPaths []string
		if opts.ReserveFiles {
			var reservationErr error
			requestedPaths, reservationErr = assign.ReservationPathsForAssignment(liveDetails.Title, liveDetails.Description)
			if reservationErr != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("%s: reserve files: %v", item.BeadID, reservationErr))
				if !opts.Quiet {
					fmt.Printf("  Not assigning %s: %v\n", item.BeadID, reservationErr)
				}
				failCount++
				continue
			}
		}

		prompt := expandPromptTemplate(item.BeadID, rawBeadTitle, opts.Template, opts.TemplateFile)

		pn, resolveErr := resolveAssignmentItemPane(panes, *item)
		if resolveErr != nil {
			failure := fmt.Sprintf("%s: resolve pane: %v", item.BeadID, resolveErr)
			out.Errors = append(out.Errors, failure)
			if !opts.Quiet {
				fmt.Printf("  Failed to assign %s to pane %s: %v\n", item.BeadID, item.PaneTarget, resolveErr)
			}
			failCount++
			continue
		}
		paneID := assignmentPaneStableKey(pn)
		item.Pane = pn.Index
		item.PaneTarget = assignmentPaneTarget(pn)
		item.PaneID = pn.ID
		observeCtx, observeCancel := context.WithTimeout(ctx, resolveAssignTimeout(opts.Timeout))
		observation, observeErr := observer.Observe(observeCtx, session)
		observeCancel()
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("assignment execution canceled while observing %s: %w", item.BeadID, err)
		}
		observeNow := time.Now()
		if observeErr != nil || pn.ID == "" ||
			!statuspkg.DispatchObservationIsCurrent(observation.ObservedAt, observeNow) ||
			(!observation.SafeToDispatch(pn.ID) && !(opts.Relax && isRelaxableThinkingPane(observation, pn, agentTypeForPane(pn), observeNow))) {
			if observeErr == nil {
				observeErr = fmt.Errorf("pane %s is not freshly and confidently idle", item.PaneTarget)
			}
			out.Errors = append(out.Errors, fmt.Sprintf("%s: observation gate: %v", item.BeadID, observeErr))
			if !opts.Quiet {
				fmt.Printf("  Failed to assign %s to pane %s: %v\n", item.BeadID, item.PaneTarget, observeErr)
			}
			failCount++
			continue
		}

		var baseIntentSHA256 string
		prompt, baseIntentSHA256, item.CASSInjection, item.MemoryInjection = enrichAssignmentPrompt(ctx, promptContext, robot.AssignmentPrompt{
			Prompt: prompt, Title: rawBeadTitle, AgentType: item.AgentType, Session: session, ProjectDir: projectDir,
		}, item.BeadID, liveDetails, opts.Timeout)
		if !opts.Quiet {
			printAssignmentPromptContext(item.BeadID, item.CASSInjection, item.MemoryInjection)
		}

		promptForPane := stampMarchingOrders(prompt, session, pn.WindowIndex, pn.Index)

		actor := item.AgentName
		if strings.TrimSpace(actor) == "" {
			actor = fmt.Sprintf("%s-%s-pane-%d-%d", session, item.AgentType, pn.WindowIndex, pn.Index)
		}
		handoffAgent := item.AgentName
		if strings.TrimSpace(handoffAgent) == "" {
			handoffAgent = actor
		}
		idempotencyKey, keyErr := assignment.NewAssignmentIdempotencyKey()
		if keyErr != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("%s: idempotency key: %v", item.BeadID, keyErr))
			if !opts.Quiet {
				fmt.Printf("  ✗ Failed to prepare assignment %s: %v\n", item.BeadID, keyErr)
			}
			failCount++
			continue
		}
		assignmentCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(opts.Timeout))
		atomicResult, assignErr := atomicCoordinator.Execute(assignmentCtx, assignment.AtomicRequest{
			BeadID:             item.BeadID,
			BeadTitle:          rawBeadTitle,
			Target:             paneID,
			OccupancyKey:       paneID,
			Pane:               item.Pane,
			AgentType:          item.AgentType,
			AgentName:          handoffAgent,
			Actor:              actor,
			Prompt:             promptForPane,
			IdempotencyKey:     idempotencyKey,
			BaseIntentSHA256:   baseIntentSHA256,
			RequireReservation: opts.ReserveFiles,
			RequestedPaths:     requestedPaths,
			ReservationTTL:     time.Hour,
		})
		cancel()
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("assignment execution canceled while assigning %s: %w", item.BeadID, err)
		}
		if assignErr != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("%s: atomic assignment: %v", item.BeadID, assignErr))
			if !opts.Quiet {
				fmt.Printf("  Failed to assign %s to pane %s: %v\n", item.BeadID, item.PaneTarget, assignErr)
			}
			failCount++
			continue
		}
		if len(atomicResult.Lease.Granted) > 0 {
			reservedCount++
			if !opts.Quiet && opts.Verbose {
				fmt.Printf("    Reserved files: %v\n", atomicResult.Lease.Granted)
			}
		}
		if durable := atomicResult.Assignment; durable != nil && durable.BeadID == item.BeadID && durable.IdempotencyKey == idempotencyKey {
			item.BeadTitle = forcedRedactedAssignmentText(durable.BeadTitle)
			item.AgentType = durable.AgentType
			item.AgentName = durable.AgentName
			item.Status = string(durable.Status)
			item.AssignedAt = durable.AssignedAt.UTC().Format(time.RFC3339)
		}

		item.PromptSent = atomicResult.Sent
		activeBeads[item.BeadID] = struct{}{}
		successCount++
		out.Summary.AssignedCount = successCount

		if !atomicResult.Replayed {
			bestEffortStampBeadLabel(projectDir, item.BeadID, session, pn.WindowIndex, pn.Index)
		}

		if !opts.Quiet {
			fmt.Printf("  Assigned %s to pane %s (%s)\n", item.BeadID, item.PaneTarget, item.AgentType)
		}
	}

	if !opts.Quiet {
		fmt.Println()
		if failCount == 0 {
			fmt.Printf("✓ Successfully assigned %d beads\n", successCount)
		} else {
			fmt.Printf("Assigned %d beads (%d failed)\n", successCount, failCount)
		}
		if reservedCount > 0 {
			fmt.Printf("  File reservations: %d beads with reserved paths\n", reservedCount)
		}
		fmt.Println("Use 'ntm status --assignments' to monitor progress.")
	}
	if failCount > 0 {
		return fmt.Errorf("%d of %d assignments failed", failCount, successCount+failCount)
	}

	return nil
}

func newCLIAtomicReservationPort(reservationMgr *assign.FileReservationManager, projectDir string) *assign.AtomicReservationPort {
	return assign.NewAtomicReservationPort(reservationMgr, projectDir, func(discoverCtx context.Context, req assignment.ReservationRequest) []string {
		return assign.DiscoverReservationPaths(discoverCtx, projectDir, req, bv.BeadTextReader(getBeadAssignmentDetailsForAssignment))
	})
}

func newCLIAtomicAssignmentCoordinator(store *assignment.AssignmentStore, projectDir string, reservationMgr *assign.FileReservationManager, allowBusy ...bool) *assignment.AtomicCoordinator {
	operatorGatedLabels := bv.OperatorGatedLabelsForProject(projectDir)
	claimPort := assignment.ClaimFunc(func(ctx context.Context, beadID, actor string) (assignment.ClaimReceipt, error) {
		claim, err := claimBeadForAssignmentWithPolicy(ctx, projectDir, beadID, actor, operatorGatedLabels)
		if err != nil {
			switch {
			case errors.Is(err, bv.ErrBeadAssignmentIneligible):
				return assignment.ClaimReceipt{}, fmt.Errorf("%w: %v", assignment.ErrClaimIneligible, err)
			case errors.Is(err, bv.ErrBeadAlreadyClaimed):
				return assignment.ClaimReceipt{}, fmt.Errorf("%w: %v", assignment.ErrClaimConflict, err)
			}
			return assignment.ClaimReceipt{}, err
		}
		return assignment.ClaimReceipt{
			BeadID:    claim.ID,
			Actor:     claim.Actor,
			Status:    claim.Status,
			ClaimedAt: claim.ClaimedAt,
		}, nil
	})

	var reservationPort assignment.ReservationPort
	if reservationMgr != nil {
		reservationPort = newCLIAtomicReservationPort(reservationMgr, projectDir)
	}

	redactionConfig := config.Default().Redaction.ToRedactionLibConfig()
	if cfg != nil {
		redactionConfig = cfg.Redaction.ToRedactionLibConfig()
	}
	bypassIdleGate := len(allowBusy) > 0 && allowBusy[0]
	dispatchPort := &cliAtomicPaneDispatchPort{
		session:         store.SessionName,
		redactionConfig: redactionConfig,
		observer:        newAssignSessionObserver(),
		bypassIdleGate:  bypassIdleGate,
	}
	readLiveDetails := func(detailsCtx context.Context, beadID string) (*bv.BeadAssignmentDetails, error) {
		details, err := getBeadAssignmentDetailsForAssignment(detailsCtx, projectDir, beadID)
		if err != nil {
			return nil, err
		}
		if details == nil {
			return nil, errors.New("live work-item details are missing")
		}
		return details, nil
	}
	coordinator := assignment.NewAtomicCoordinator(store, claimPort, reservationPort, dispatchPort, dispatchPort).
		WithWorkItemStatusPort(assignment.WorkItemStatusFunc(func(statusCtx context.Context, beadID string) (string, error) {
			return getBeadStatusForAssignment(statusCtx, projectDir, beadID)
		})).
		WithAssignmentEligibilityAuthorizationPort(assignment.AssignmentEligibilityAuthorizationFunc(
			func(detailsCtx context.Context, request assignment.AssignmentEligibilityAuthorizationRequest) error {
				details, err := readLiveDetails(detailsCtx, request.BeadID)
				if err != nil {
					return err
				}
				return validateCLIAtomicAssignmentDetailsWithPolicy(details, request, operatorGatedLabels)
			},
		)).
		WithWorkingReplacementAuthorizationPort(assignment.WorkingReplacementAuthorizationFunc(
			func(statusCtx context.Context, beadID string) (assignment.WorkingReplacementAuthorization, error) {
				details, err := readLiveDetails(statusCtx, beadID)
				if err != nil {
					return assignment.WorkingReplacementAuthorization{}, err
				}
				if err := validateCLIAtomicAssignmentDetailsWithPolicy(details, assignment.AssignmentEligibilityAuthorizationRequest{
					BeadID: beadID, ClaimActor: details.Assignee, AllowOwnedInProgress: true,
				}, operatorGatedLabels); err != nil {
					return assignment.WorkingReplacementAuthorization{}, err
				}
				return assignment.WorkingReplacementAuthorization{Status: details.Status, Assignee: details.Assignee}, nil
			},
		))
	return coordinator.WithWorkingReplacementReleasePort(assignment.WorkingReplacementReleaseFunc(
		func(releaseCtx context.Context, current *assignment.Assignment) (assignment.WorkingReplacementReleaseReceipt, error) {
			released, err := releaseAssignmentLeases(releaseCtx, store.SessionName, current)
			return cliWorkingReplacementReleaseReceipt(current, released, err), err
		},
	))
}

func validateCLIAtomicAssignmentDetailsWithPolicy(details *bv.BeadAssignmentDetails, request assignment.AssignmentEligibilityAuthorizationRequest, operatorGatedLabels []string) error {
	err := bv.ValidateBeadAssignmentAuthorizationWithOperatorGatedLabels(details, bv.BeadAssignmentAuthorization{
		BeadID: request.BeadID, ExpectedAssignee: request.ClaimActor,
		AllowUnassignedOpen:  request.AllowUnassignedOpen,
		AllowOwnedOpen:       request.AllowOwnedOpen,
		AllowOwnedInProgress: request.AllowOwnedInProgress,
	}, operatorGatedLabels)
	if errors.Is(err, bv.ErrBeadAssignmentIneligible) {
		return fmt.Errorf("%w: %v", assignment.ErrClaimIneligible, err)
	}
	return err
}

func cliWorkingReplacementReleaseReceipt(current *assignment.Assignment, released []string, releaseErr error) assignment.WorkingReplacementReleaseReceipt {
	return assign.WorkingReplacementReleaseReceipt(current, released, releaseErr)
}

type cliAtomicPaneDispatchPort struct {
	session         string
	redactionConfig redaction.Config
	observer        assignSessionObserver
	bypassIdleGate  bool
}

func (p *cliAtomicPaneDispatchPort) prepare(ctx context.Context, req assignment.DispatchRequest) (*dispatchsvc.Service, *dispatchsvc.Prepared, error) {
	panes, err := tmux.GetPanesContext(ctx, p.session)
	if err != nil {
		return nil, nil, fmt.Errorf("load dispatch topology: %w", err)
	}
	service, err := dispatchsvc.NewService(dispatchsvc.Ports{
		Redactor: shellFinalMessageRedactor(p.redactionConfig), Protocols: shellDispatchProtocolPlanner{},
		Deliverer: dispatchsvc.TMUXDeliverer{},
	})
	if err != nil {
		return nil, nil, err
	}
	prepared, err := service.Prepare(ctx, dispatchsvc.Request{
		Session: p.session, Panes: panes, Selectors: []string{req.Target}, RequireSingleSelector: true,
		IncludeUser: true, Message: req.Prompt, Submit: true, StopOnFailure: true,
	})
	if err != nil {
		return nil, prepared, err
	}
	return service, prepared, nil
}

func (p *cliAtomicPaneDispatchPort) Preflight(ctx context.Context, req assignment.DispatchRequest) (assignment.PromptPreflightResult, error) {
	titleResult := redaction.ScanAndRedact(req.BeadTitle, p.redactionConfig)
	if titleResult.Blocked {
		return assignment.PromptPreflightResult{}, &dispatchsvc.Error{
			Code: dispatchsvc.ErrRedactionBlocked,
			Err:  fmt.Errorf("assignment title blocked by redaction policy (%d findings)", len(titleResult.Findings)),
		}
	}
	for _, requestedPath := range req.RequestedPaths {
		pathResult := redaction.ScanAndRedact(requestedPath, p.redactionConfig)
		if len(pathResult.Findings) > 0 {
			return assignment.PromptPreflightResult{}, &dispatchsvc.Error{
				Code: dispatchsvc.ErrRedactionBlocked,
				Err:  fmt.Errorf("assignment reservation path blocked by redaction policy (%d findings)", len(pathResult.Findings)),
			}
		}
	}
	_, prepared, err := p.prepare(ctx, req)
	if err != nil {
		return assignment.PromptPreflightResult{}, err
	}
	dispatchPrompt, err := prepared.FinalMessageForSingleTarget()
	if err != nil {
		return assignment.PromptPreflightResult{}, err
	}
	durableConfig := p.redactionConfig.DeepCopy()
	durableConfig.Mode = redaction.ModeRedact
	durablePrompt := redaction.ScanAndRedact(req.Prompt, durableConfig).Output
	durableTitle := redaction.ScanAndRedact(req.BeadTitle, durableConfig).Output
	return assignment.PromptPreflightResult{DispatchPrompt: dispatchPrompt, DurablePrompt: durablePrompt, DurableTitle: durableTitle}, nil
}

func (p *cliAtomicPaneDispatchPort) Dispatch(ctx context.Context, req assignment.DispatchRequest) (assignment.DispatchReceipt, error) {
	started := time.Now()
	if !p.bypassIdleGate {
		observer := p.observer
		if observer == nil {
			observer = newAssignSessionObserver()
		}
		observation, observeErr := observer.Observe(ctx, p.session)
		if observeErr != nil {
			return assignment.DispatchReceipt{Duration: time.Since(started)}, assignment.GuaranteeNoActuation(
				fmt.Errorf("fresh dispatch observation for pane %s: %w", req.Target, observeErr),
			)
		}
		if !statuspkg.DispatchObservationIsCurrent(observation.ObservedAt, time.Now()) || !observation.SafeToDispatch(req.Target) {
			return assignment.DispatchReceipt{Duration: time.Since(started)}, assignment.GuaranteeNoActuation(
				fmt.Errorf("pane %s is not freshly and confidently idle at dispatch", req.Target),
			)
		}
	}
	service, prepared, prepareErr := p.prepare(ctx, req)
	if prepareErr != nil {
		return assignment.DispatchReceipt{Duration: time.Since(started)}, assignment.GuaranteeNoActuation(prepareErr)
	}
	result, dispatchErr := service.Dispatch(ctx, prepared)
	receipt := assignment.DispatchReceipt{Duration: time.Since(started)}
	if dispatchErr != nil {
		if dispatchsvc.IsGuaranteedNoDeliveryActuation(dispatchErr) {
			return receipt, assignment.GuaranteeNoActuation(dispatchErr)
		}
		return receipt, dispatchErr
	}
	delivery, err := validateSinglePaneDispatchResult(result, req.Target)
	if err != nil {
		return receipt, err
	}
	receipt.DeliveryID = assignment.DispatchDeliveryID(delivery.Target.Ref.StableKey(), string(delivery.Protocol), req.IdempotencyKey)
	return receipt, nil
}

func validateSinglePaneDispatchResult(result dispatchsvc.Result, requestedTarget string) (dispatchsvc.Receipt, error) {
	if result.Delivered != 1 || len(result.Receipts) != 1 || result.Receipts[0].Status != dispatchsvc.ReceiptDelivered {
		return dispatchsvc.Receipt{}, fmt.Errorf("dispatch delivered %d panes, want 1", result.Delivered)
	}
	delivery := result.Receipts[0]
	actualTarget := strings.TrimSpace(delivery.Target.Ref.StableKey())
	if actualTarget != strings.TrimSpace(requestedTarget) {
		return dispatchsvc.Receipt{}, fmt.Errorf("dispatch receipt target %s does not match requested pane %s", actualTarget, requestedTarget)
	}
	return delivery, nil
}

func promptTemplateSource(templateName, templateFile string) string {
	return assign.PromptTemplateSource(templateName, templateFile)
}

func renderPromptTemplate(beadID, title, template string) string {
	return assign.RenderPromptTemplate(beadID, title, template)
}

func expandPromptTemplate(beadID, title, templateName, templateFile string) string {
	return assign.ExpandPromptTemplate(beadID, title, templateName, templateFile)
}

func enrichAssignmentPrompt(
	ctx context.Context,
	opts robot.PromptContextOptions,
	request robot.AssignmentPrompt,
	beadID string,
	details *bv.BeadAssignmentDetails,
	timeout time.Duration,
) (string, string, *robot.CASSInjectionInfo, *robot.CMInjectionInfo) {
	if !opts.Enabled() {
		return request.Prompt, "", nil, nil
	}
	if details == nil {
		detailsCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(timeout))
		details, _ = getBeadAssignmentDetailsForAssignment(detailsCtx, request.ProjectDir, beadID)
		cancel()
	}
	if details != nil {
		if strings.TrimSpace(request.Title) == "" {
			request.Title = details.Title
		}
		request.Labels, request.Description = details.Labels, details.Description
	}
	enriched, cassInjection, memoryInjection := robot.EnrichAssignmentPrompt(ctx, request, opts)
	return enriched, assignment.PromptSHA256(request.Prompt), cassInjection, memoryInjection
}

func printAssignmentPromptContext(beadID string, cassInjection *robot.CASSInjectionInfo, memoryInjection *robot.CMInjectionInfo) {
	if cassInjection != nil {
		switch {
		case cassInjection.ItemsInjected > 0:
			fmt.Printf("  CASS context injected for %s: %d item(s), ~%d tokens\n", beadID, cassInjection.ItemsInjected, cassInjection.TokensAdded)
		case cassInjection.SkippedReason != "":
			fmt.Printf("  CASS context injection skipped for %s: %s\n", beadID, cassInjection.SkippedReason)
		}
	}
	if memoryInjection != nil {
		switch {
		case memoryInjection.TokensAdded > 0:
			fmt.Printf("  CM rules injected for %s: ~%d tokens\n", beadID, memoryInjection.TokensAdded)
		case memoryInjection.SkippedReason != "":
			fmt.Printf("  CM rule injection skipped for %s: %s\n", beadID, memoryInjection.SkippedReason)
		}
	}
}

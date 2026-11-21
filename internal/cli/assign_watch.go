package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/assign"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/completion"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/coordinator"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ============================================================================
// Dependency Awareness, Auto-Reassignment & Watch Mode
// ============================================================================

// UnblockedBead represents a bead that was previously blocked but is now actionable.
type UnblockedBead struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Priority      int      `json:"priority"`
	PrevBlockers  []string `json:"previous_blockers"`
	UnblockedByID string   `json:"unblocked_by_id"`
}

// DependencyAwareResult contains the result of an unblock check.
type DependencyAwareResult struct {
	CompletedBeadID string          `json:"completed_bead_id"`
	NewlyUnblocked  []UnblockedBead `json:"newly_unblocked"`
	CyclesDetected  [][]string      `json:"cycles_detected,omitempty"`
	Errors          []string        `json:"errors,omitempty"`
}

// AutoReassignOptions contains options for auto-reassignment.
type AutoReassignOptions struct {
	Session         string
	ProjectDir      string
	Strategy        string
	Relax           bool
	Template        string
	TemplateFile    string
	ReserveFiles    bool
	Verbose         bool
	Quiet           bool
	Timeout         time.Duration
	Limit           int
	AgentTypeFilter string
	DryRun          bool
	IdleThreshold   time.Duration
	WithCASS        bool
	NoCASS          bool
	WithMemory      bool

	policyProject string
}

// AutoReassignResult contains the result of an auto-reassignment operation.
type AutoReassignResult struct {
	TriggerBeadID  string           `json:"trigger_bead_id"`
	NewlyUnblocked []UnblockedBead  `json:"newly_unblocked"`
	Assignments    []AssignmentItem `json:"assignments"`
	Skipped        []SkippedItem    `json:"skipped"`
	IdleAgents     int              `json:"idle_agents"`
	Errors         []string         `json:"errors,omitempty"`
	CyclesDetected [][]string       `json:"cycles_detected,omitempty"`
	CompletionTime time.Time        `json:"completion_time"`
}

var performAutoReassignmentForTrigger = PerformAutoReassignment

type assignWatchOverlayPreparation struct {
	Hint    string
	Warning string
}

func prepareAndAnnounceAssignWatchOverlay(logf func(string, ...interface{}), session string, inTmux bool, currentSession string, isBound func(string) bool, ensureBinding func(string) error) assignWatchOverlayPreparation {
	prep := prepareAssignWatchOverlay(session, inTmux, currentSession, isBound, ensureBinding)
	announceAssignWatchOverlay(logf, prep)
	return prep
}

func announceAssignWatchOverlay(logf func(string, ...interface{}), prep assignWatchOverlayPreparation) {
	if logf == nil {
		return
	}
	if prep.Warning != "" {
		logf("%s", prep.Warning)
	}
	if prep.Hint != "" {
		logf("%s", prep.Hint)
	}
}

func prepareAssignWatchOverlay(session string, inTmux bool, currentSession string, isBound func(string) bool, ensureBinding func(string) error) assignWatchOverlayPreparation {
	if !shouldOfferAssignWatchOverlay(session, inTmux, currentSession) {
		return assignWatchOverlayPreparation{}
	}
	if isBound == nil {
		return assignWatchOverlayPreparation{}
	}

	if isBound(assignWatchOverlayKey) {
		return assignWatchOverlayPreparation{
			Hint: buildAssignWatchOverlayHint(assignWatchOverlayKey, false),
		}
	}
	if ensureBinding == nil {
		return assignWatchOverlayPreparation{}
	}

	if err := ensureBinding(assignWatchOverlayKey); err != nil {
		return assignWatchOverlayPreparation{
			Warning: buildAssignWatchOverlayWarning(assignWatchOverlayKey, err),
		}
	}

	return assignWatchOverlayPreparation{
		Hint: buildAssignWatchOverlayHint(assignWatchOverlayKey, true),
	}
}

func shouldOfferAssignWatchOverlay(session string, inTmux bool, currentSession string) bool {
	return inTmux && session != "" && currentSession != "" && currentSession == session
}

func buildAssignWatchOverlayHint(key string, installedNow bool) string {
	if installedNow {
		return fmt.Sprintf("Hint: installed the %s overlay binding. Press %s for the attention-aware dashboard overlay while assign --watch is running.", key, key)
	}
	return fmt.Sprintf("Hint: press %s for the attention-aware dashboard overlay while assign --watch is running.", key)
}

func buildAssignWatchOverlayWarning(key string, err error) string {
	return fmt.Sprintf("Warning: Could not auto-set up the %s overlay binding (%v); run 'ntm bind --overlay' if you want the attention-aware dashboard overlay shortcut.", key, err)
}

// runWatchMode implements the --watch flag for continuous auto-assignment.
func runWatchMode(cmd *cobra.Command, session, projectDir, policyProject string) error {
	agentTypeFilter := resolveAgentTypeFilter()

	idleThreshold := assignIdleThreshold
	if idleThreshold <= 0 {
		idleThreshold = loadSelectedConfigOrDefault().Assign.IdleThresholdDuration()
	}

	opts := &AutoReassignOptions{
		Session:         session,
		ProjectDir:      projectDir,
		Strategy:        assignStrategy,
		Relax:           assignRelax,
		Template:        assignTemplate,
		TemplateFile:    assignTemplateFile,
		ReserveFiles:    assignReserveFiles,
		Verbose:         assignVerbose,
		Quiet:           assignQuiet,
		Timeout:         assignTimeout,
		Limit:           assignLimit,
		AgentTypeFilter: agentTypeFilter,
		DryRun:          assignDryRun,
		IdleThreshold:   idleThreshold,
		WithCASS:        assignWithCASS,
		NoCASS:          assignNoCASS,
		WithMemory:      assignWithMemory,
		policyProject:   policyProject,
	}

	store, err := assignment.LoadStoreStrict(session)
	if err != nil {
		return fmt.Errorf("failed to load assignment store: %w", err)
	}

	watchLoop := NewWatchLoop(session, store, opts)

	prepareAndAnnounceAssignWatchOverlay(
		watchLoop.logf,
		session,
		tmux.InTmux(),
		tmux.GetCurrentSession(),
		isOverlayKeyBound,
		setupOverlayBindingQuiet,
	)

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	sigDone := make(chan struct{})
	defer close(sigDone)

	go func() {
		select {
		case <-sigCh:
			watchLoop.logf("Received interrupt signal, shutting down gracefully...")
			cancel()
		case <-sigDone:
		}
	}()

	if assignDryRun {
		watchLoop.logf("Performing initial assignment pass... (dry-run: previewing only, no dispatch)")
	} else {
		watchLoop.logf("Performing initial assignment pass...")
	}

	assignOpts := &AssignCommandOptions{
		Session:         session,
		ProjectDir:      projectDir,
		Strategy:        assignStrategy,
		Relax:           assignRelax,
		Limit:           assignLimit,
		AgentTypeFilter: agentTypeFilter,
		Template:        assignTemplate,
		TemplateFile:    assignTemplateFile,
		Verbose:         assignVerbose,
		Quiet:           true,
		Auto:            !assignDryRun,
		Timeout:         assignTimeout,
		ReserveFiles:    assignReserveFiles,
		WithCASS:        assignWithCASS,
		NoCASS:          assignNoCASS,
		WithMemory:      assignWithMemory,
		policyProject:   policyProject,
	}

	initialScanDeferred := false
	initialOutput, err := getAssignOutputEnhanced(ctx, assignOpts)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if watchLoop.deferSourceChange("initial assignment", err) {
			initialScanDeferred = true
		} else if isAutomatedAssignmentSafetyError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		} else {
			watchLoop.logf("Warning: Initial assignment failed: %v", err)
		}
	} else if len(initialOutput.Assignments) > 0 {
		assignOpts.Quiet = assignQuiet
		if assignDryRun {
			watchLoop.logf("Initial assignment (dry-run): %d beads would be assigned (no dispatch)", len(initialOutput.Assignments))
			for _, assigned := range initialOutput.Assignments {
				watchLoop.logf("  Would assign %s -> pane %d (%s)", assigned.BeadID, assigned.Pane, assigned.AgentType)
			}
		} else {
			execErr := executeAssignmentsEnhanced(ctx, session, initialOutput, assignOpts)
			if execErr != nil {
				watchLoop.logf("Warning: Failed to execute initial assignments: %v", execErr)
			}
			sent := 0
			for _, assigned := range initialOutput.Assignments {
				if assigned.PromptSent {
					sent++
				}
			}
			if sent > 0 || execErr == nil {
				watchLoop.logf("Initial assignment: %d beads dispatched", sent)
			}
			for _, assigned := range initialOutput.Assignments {
				if !assigned.PromptSent {
					continue
				}
				watchLoop.mu.Lock()
				watchLoop.totalAssigned++
				watchLoop.lastAssignmentAt = time.Now()
				watchLoop.mu.Unlock()
				watchLoop.logf("  %s -> pane %d (%s)", assigned.BeadID, assigned.Pane, assigned.AgentType)
			}
		}
	} else {
		watchLoop.logf("Initial assignment: No beads to assign (no idle agents or no ready work)")
	}

	if assignStopWhenDone && !initialScanDeferred {
		stop, err := watchLoop.shouldStop(ctx)
		if err != nil {
			return fmt.Errorf("check initial watch completion: %w", err)
		}
		if stop {
			watchLoop.logf("No work available. Exiting watch mode.")
			fmt.Println(watchLoop.Summary())
			return nil
		}
	}

	if err := watchLoop.Run(ctx); err != nil && err != context.Canceled {
		return err
	}

	fmt.Println()
	fmt.Println(watchLoop.Summary())

	if err := store.Save(); err != nil {
		watchLoop.logf("Warning: Failed to save assignment store: %v", err)
	}

	return nil
}

// GetNewlyUnblockedBeads checks what beads are now unblocked after a bead completion.
func GetNewlyUnblockedBeads(ctx context.Context, projectDir, completedBeadID string, verbose bool) (*DependencyAwareResult, error) {
	if ctx == nil {
		return nil, errors.New("dependency refresh context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("dependency refresh canceled: %w", err)
	}
	result := &DependencyAwareResult{
		CompletedBeadID: completedBeadID,
		NewlyUnblocked:  make([]UnblockedBead, 0),
	}

	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return nil, errors.New("assignment project directory is required")
	}

	completedBeadID = strings.TrimSpace(completedBeadID)
	if completedBeadID == "" {
		return nil, errors.New("completed bead ID is required")
	}
	result.CompletedBeadID = completedBeadID

	if assignHumanDiagnostics(verbose) {
		fmt.Fprintf(os.Stderr, "[DEP] Checking for beads unblocked by %s\n", completedBeadID)
	}

	dependents, err := bv.GetBeadBlockingDependentsContext(ctx, projectDir, completedBeadID)
	if err != nil {
		return result, fmt.Errorf("read dependents for completed bead %s: %w", completedBeadID, err)
	}

	for _, dependent := range dependents {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("dependency refresh canceled before validating %s: %w", dependent.ID, err)
		}
		details, detailsErr := bv.GetBeadAssignmentDetailsContext(ctx, projectDir, dependent.ID)
		if detailsErr != nil {
			return result, fmt.Errorf("read live dependency and eligibility state for %s: %w", dependent.ID, detailsErr)
		}
		candidate, skipReason, unresolved := newlyUnblockedCandidateForProject(projectDir, completedBeadID, dependent, details)
		if skipReason == "unrelated" {
			return result, fmt.Errorf("dependent %s no longer reports blocking relationship to %s", dependent.ID, completedBeadID)
		}
		if len(unresolved) > 0 {
			if assignHumanDiagnostics(verbose) {
				fmt.Fprintf(os.Stderr, "[DEP] %s still blocked by: %v\n", dependent.ID, unresolved)
			}
			continue
		}
		if candidate == nil {
			if assignHumanDiagnostics(verbose) {
				fmt.Fprintf(os.Stderr, "[DEP] %s is related but not assignable: %s\n", dependent.ID, skipReason)
			}
			continue
		}
		result.NewlyUnblocked = append(result.NewlyUnblocked, *candidate)

		if assignHumanDiagnostics(verbose) {
			fmt.Fprintf(os.Stderr, "[UNBLOCK] %s now ready (was blocked by %s)\n", dependent.ID, completedBeadID)
		}
	}

	client := bv.NewBVClient()
	client.WorkspacePath = projectDir
	insights, err := client.GetInsightsContext(ctx)
	if err != nil {
		return result, fmt.Errorf("inspect dependency cycles after %s completed: %w", completedBeadID, err)
	}
	if insights != nil && len(insights.Cycles) > 0 {
		result.CyclesDetected = insights.Cycles
		if assignHumanDiagnostics(verbose) {
			fmt.Fprintf(os.Stderr, "[DEP] Warning: %d dependency cycles detected\n", len(insights.Cycles))
		}
	}

	return result, nil
}

func dependencyRelationToCompleted(states []bv.BeadDependencyState, completedBeadID string) (bool, []string) {
	converted := make([]assign.DependencyStatus, len(states))
	for i, state := range states {
		converted[i] = assign.DependencyStatus{ID: state.ID, Status: state.Status}
	}
	return assign.DependencyRelationToCompleted(converted, completedBeadID)
}

func newlyUnblockedCandidateForProject(projectDir, completedBeadID string, dependent bv.BeadDependentState, details *bv.BeadAssignmentDetails) (*UnblockedBead, string, []string) {
	return newlyUnblockedCandidateWithGate(completedBeadID, dependent, details, func(label string) bool {
		return bv.IsOperatorGatedLabelForProject(projectDir, label)
	})
}

func newlyUnblockedCandidateWithGate(completedBeadID string, dependent bv.BeadDependentState, details *bv.BeadAssignmentDetails, operatorGated func(string) bool) (*UnblockedBead, string, []string) {
	if details == nil {
		return nil, "live_details_required", nil
	}
	related, unresolved := dependencyRelationToCompleted(details.BlockingDependencies, completedBeadID)
	if !related {
		return nil, "unrelated", nil
	}
	if len(unresolved) > 0 {
		return nil, "blocked_by_dependency", unresolved
	}
	if details.ID != dependent.ID {
		return nil, "live_details_mismatch", nil
	}
	if skip := classifyLiveAssignmentDetailsWithGate(details, nil, operatorGated); skip != nil {
		return nil, skip.Reason, nil
	}
	return &UnblockedBead{
		ID: dependent.ID, Title: details.Title, Priority: details.Priority,
		PrevBlockers: []string{completedBeadID}, UnblockedByID: completedBeadID,
	}, "", nil
}

// CheckCycles returns any dependency cycles detected in the current project.
func CheckCycles(ctx context.Context, projectDir string, verbose bool) ([][]string, error) {
	if ctx == nil {
		return nil, errors.New("cycle inspection context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("cycle inspection canceled: %w", err)
	}
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return nil, errors.New("assignment project directory is required")
	}
	client := bv.NewBVClient()
	client.WorkspacePath = projectDir

	maxRetries := 2
	var insights *bv.Insights
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		insights, lastErr = client.GetInsightsContext(ctx)
		if lastErr == nil {
			break
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("cycle inspection canceled: %w", ctxErr)
		}

		if assignHumanDiagnostics(verbose) && attempt < maxRetries-1 {
			fmt.Fprintf(os.Stderr, "[DEP] BV insights failed (attempt %d/%d): %v, retrying...\n",
				attempt+1, maxRetries, lastErr)
		}

		if attempt < maxRetries-1 {
			if waitErr := waitContextDelay(ctx, 500*time.Millisecond*time.Duration(attempt+1)); waitErr != nil {
				return nil, fmt.Errorf("cycle inspection canceled during retry backoff: %w", waitErr)
			}
		}
	}

	if lastErr != nil {
		if assignHumanDiagnostics(verbose) {
			fmt.Fprintf(os.Stderr, "[DEP] Failed to get insights after %d attempts: %v\n", maxRetries, lastErr)
			fmt.Fprintf(os.Stderr, "[DEP] Cycle detection unavailable - proceeding without cycle filtering\n")
		}
		return nil, fmt.Errorf("failed to get insights after %d attempts: %w", maxRetries, lastErr)
	}

	if insights == nil {
		if assignHumanDiagnostics(verbose) {
			fmt.Fprintf(os.Stderr, "[DEP] BV insights returned nil - no cycle information available\n")
		}
		return nil, nil
	}

	validCycles, invalidWarnings := assign.ValidateCycles(insights.Cycles)
	if assignHumanDiagnostics(verbose) {
		for _, w := range invalidWarnings {
			fmt.Fprintf(os.Stderr, "[DEP] Warning: %s\n", w)
		}
		if len(validCycles) > 0 {
			fmt.Fprintf(os.Stderr, "[DEP] Detected %d valid dependency cycles:\n", len(validCycles))
			for i, cycle := range validCycles {
				fmt.Fprintf(os.Stderr, "  Cycle %d: %v\n", i+1, cycle)
			}
		}
		if len(validCycles) != len(insights.Cycles) {
			fmt.Fprintf(os.Stderr, "[DEP] Filtered out %d invalid cycles\n", len(insights.Cycles)-len(validCycles))
		}
	}

	return validCycles, nil
}

// IsBeadInCycle checks if a bead ID is part of any detected dependency cycle.
func IsBeadInCycle(beadID string, cycles [][]string) bool {
	return assign.IsBeadInCycle(beadID, cycles)
}

// FilterCyclicBeads removes beads that are part of dependency cycles from the list.
func FilterCyclicBeads(ctx context.Context, projectDir string, beads []bv.BeadPreview, verbose bool) ([]bv.BeadPreview, int, error) {
	cycles, err := CheckCycles(ctx, projectDir, false)
	if err != nil {
		return nil, 0, err
	}
	returnValues, excluded := filterCyclicBeads(beads, cycles, verbose)
	return returnValues, excluded, nil
}

func filterCyclicBeads(beads []bv.BeadPreview, cycles [][]string, verbose bool) ([]bv.BeadPreview, int) {
	if len(cycles) == 0 {
		return beads, 0
	}

	var filtered []bv.BeadPreview
	excluded := 0

	for _, bead := range beads {
		if IsBeadInCycle(bead.ID, cycles) {
			excluded++
			if assignHumanDiagnostics(verbose) {
				fmt.Fprintf(os.Stderr, "[DEP] Excluding %s from assignment (in dependency cycle)\n", bead.ID)
			}
			continue
		}
		filtered = append(filtered, bead)
	}

	return filtered, excluded
}

// PerformAutoReassignment handles automatic reassignment when a bead completes.
func PerformAutoReassignment(ctx context.Context, completedBeadID string, opts *AutoReassignOptions) (*AutoReassignResult, error) {
	if ctx == nil {
		return nil, errors.New("auto-reassignment context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("auto-reassignment canceled: %w", err)
	}
	if opts == nil {
		return nil, errors.New("auto-reassignment options are required")
	}
	result := &AutoReassignResult{
		TriggerBeadID:  completedBeadID,
		CompletionTime: time.Now(),
		Assignments:    make([]AssignmentItem, 0),
		Skipped:        make([]SkippedItem, 0),
	}

	if assignHumanDiagnostics(opts.Verbose) {
		fmt.Fprintf(os.Stderr, "[AUTO] Auto-reassignment triggered by completion of %s\n", completedBeadID)
	}

	projectDir := strings.TrimSpace(opts.ProjectDir)
	if projectDir == "" {
		var err error
		projectDir, err = resolveAssignProjectDir(ctx, opts.Session)
		if err != nil {
			return nil, fmt.Errorf("resolve auto-reassignment project: %w", err)
		}
		opts.ProjectDir = projectDir
	}
	if err := ensureAuthoritativeAssignmentPolicy(projectDir, &opts.policyProject); err != nil {
		return nil, err
	}

	depResult, err := readAssignmentPreflight(ctx, projectDir, !opts.DryRun, func() (*DependencyAwareResult, error) {
		return GetNewlyUnblockedBeads(ctx, projectDir, completedBeadID, opts.Verbose)
	})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, fmt.Errorf("auto-reassignment canceled while checking newly unblocked work: %w", ctxErr)
	}
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("failed to check unblocked beads: %v", err))
		return result, nil
	}

	result.NewlyUnblocked = depResult.NewlyUnblocked
	result.CyclesDetected = depResult.CyclesDetected

	if len(depResult.Errors) > 0 {
		result.Errors = append(result.Errors, depResult.Errors...)
	}

	if len(result.NewlyUnblocked) == 0 {
		if assignHumanDiagnostics(opts.Verbose) {
			fmt.Fprintf(os.Stderr, "[AUTO] No beads were unblocked by completion of %s\n", completedBeadID)
		}
		return result, nil
	}

	verifiedActionable, err := readAssignmentPreflight(ctx, projectDir, !opts.DryRun, func() ([]bv.TriageRecommendation, error) {
		return getActionableRecommendationsForWatch(ctx, projectDir, 0)
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, fmt.Errorf("auto-reassignment canceled while verifying actionable work: %w", ctxErr)
		}
		return result, fmt.Errorf("automated assignment stopped because newly unblocked work could not be verified: %w", err)
	}
	activeAssignments, err := loadActiveAssignmentBeadIDs(opts.Session)
	if err != nil {
		return result, err
	}
	result.NewlyUnblocked, result.Skipped = filterNewlyUnblockedByVerifiedPlanForProject(
		projectDir,
		result.NewlyUnblocked,
		verifiedActionable,
		activeAssignments,
		result.Skipped,
	)
	if len(result.NewlyUnblocked) == 0 {
		return result, nil
	}

	if assignHumanDiagnostics(opts.Verbose) {
		fmt.Fprintf(os.Stderr, "[AUTO] Found %d newly unblocked beads: %v\n",
			len(result.NewlyUnblocked),
			func() []string {
				var ids []string
				for _, ub := range result.NewlyUnblocked {
					ids = append(ids, ub.ID)
				}
				return ids
			}())
	}

	idleAgents, err := getIdleAgents(ctx, opts.Session, opts.AgentTypeFilter, opts.Verbose)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("failed to get idle agents: %v", err))
		return result, nil
	}

	result.IdleAgents = len(idleAgents)

	if len(idleAgents) == 0 {
		for _, unblocked := range result.NewlyUnblocked {
			result.Skipped = append(result.Skipped, SkippedItem{
				BeadID: unblocked.ID,
				Reason: "no_idle_agents",
			})
		}
		if assignHumanDiagnostics(opts.Verbose) {
			fmt.Fprintf(os.Stderr, "[AUTO] No idle agents available for reassignment\n")
		}
		return result, nil
	}

	var unblockedBeads []bv.BeadPreview
	for _, unblocked := range result.NewlyUnblocked {
		unblockedBeads = append(unblockedBeads, bv.BeadPreview{
			ID:       unblocked.ID,
			Title:    unblocked.Title,
			Priority: fmt.Sprintf("P%d", unblocked.Priority),
		})
	}

	filteredBeads, excluded, err := FilterCyclicBeads(ctx, projectDir, unblockedBeads, opts.Verbose)
	if err != nil {
		return result, fmt.Errorf("filter auto-reassignment dependency cycles: %w", err)
	}
	if excluded > 0 {
		kept := make(map[string]struct{}, len(filteredBeads))
		for _, fb := range filteredBeads {
			kept[fb.ID] = struct{}{}
		}
		for _, unblocked := range result.NewlyUnblocked {
			if _, ok := kept[unblocked.ID]; !ok {
				result.Skipped = append(result.Skipped, SkippedItem{
					BeadID: unblocked.ID,
					Reason: "in_dependency_cycle",
				})
			}
		}
	}

	if len(filteredBeads) == 0 {
		if assignHumanDiagnostics(opts.Verbose) {
			fmt.Fprintf(os.Stderr, "[AUTO] No assignable beads after filtering cycles\n")
		}
		return result, nil
	}

	assignOpts := &AssignCommandOptions{
		Session:         opts.Session,
		ProjectDir:      projectDir,
		Strategy:        opts.Strategy,
		Relax:           opts.Relax,
		Limit:           opts.Limit,
		Template:        opts.Template,
		TemplateFile:    opts.TemplateFile,
		AgentTypeFilter: opts.AgentTypeFilter,
		Verbose:         opts.Verbose,
		Quiet:           opts.Quiet,
		Timeout:         opts.Timeout,
		ReserveFiles:    opts.ReserveFiles,
		WithCASS:        opts.WithCASS,
		NoCASS:          opts.NoCASS,
		WithMemory:      opts.WithMemory,
		policyProject:   opts.policyProject,
	}

	idleAgents, err = assignRoleEligibleAgents(idleAgents, projectDir, assignOpts.Template)
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		return result, nil
	}
	result.IdleAgents = len(idleAgents)

	assignments := generateAssignmentsEnhanced(ctx, idleAgents, filteredBeads, assignOpts)
	if opts.Limit > 0 && len(assignments) > opts.Limit {
		for _, item := range assignments[opts.Limit:] {
			result.Skipped = append(result.Skipped, SkippedItem{
				BeadID: item.BeadID,
				Reason: "limit_reached",
			})
		}
		assignments = assignments[:opts.Limit]
	}
	result.Assignments = assignments

	if len(assignments) > 0 && !opts.DryRun {
		enhancedOut := &AssignOutputEnhanced{
			Strategy:    opts.Strategy,
			Assignments: assignments,
			Skipped:     result.Skipped,
		}

		if err := executeAssignmentsEnhanced(ctx, opts.Session, enhancedOut, assignOpts); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, fmt.Errorf("auto-reassignment canceled during assignment execution: %w", ctxErr)
			}
			result.Errors = append(result.Errors, fmt.Sprintf("failed to execute assignments: %v", err))
		} else {
			if assignHumanDiagnostics(opts.Verbose) {
				fmt.Fprintf(os.Stderr, "[AUTO] Successfully assigned %d unblocked beads\n", len(assignments))
			}
		}
	} else if len(assignments) > 0 && opts.DryRun && assignHumanDiagnostics(opts.Verbose) {
		fmt.Fprintf(os.Stderr, "[AUTO] Dry-run: would assign %d unblocked beads (no dispatch)\n", len(assignments))
	}

	return result, nil
}

func filterNewlyUnblockedByVerifiedPlanForProject(
	projectDir string,
	newlyUnblocked []UnblockedBead,
	verifiedActionable []bv.TriageRecommendation,
	activeAssignments map[string]struct{},
	skipped []SkippedItem,
) ([]UnblockedBead, []SkippedItem) {
	return filterNewlyUnblockedByVerifiedPlanWithGate(newlyUnblocked, verifiedActionable, activeAssignments, skipped, func(label string) bool {
		return bv.IsOperatorGatedLabelForProject(projectDir, label)
	})
}

func filterNewlyUnblockedByVerifiedPlanWithGate(
	newlyUnblocked []UnblockedBead,
	verifiedActionable []bv.TriageRecommendation,
	activeAssignments map[string]struct{},
	skipped []SkippedItem,
	operatorGated func(string) bool,
) ([]UnblockedBead, []SkippedItem) {
	verifiedByID := make(map[string]bv.TriageRecommendation, len(verifiedActionable))
	for _, recommendation := range verifiedActionable {
		id := strings.TrimSpace(recommendation.ID)
		if id == "" {
			continue
		}
		recommendation.ID = id
		verifiedByID[id] = recommendation
	}

	authorized := make([]UnblockedBead, 0, len(newlyUnblocked))
	for _, candidate := range newlyUnblocked {
		id := strings.TrimSpace(candidate.ID)
		recommendation, verified := verifiedByID[id]
		if !verified {
			skipped = append(skipped, SkippedItem{
				BeadID:    id,
				BeadTitle: candidate.Title,
				Reason:    "not_in_actionable_plan",
			})
			continue
		}
		if skip := classifyTriageRecForAssignmentWithGate(recommendation, activeAssignments, operatorGated); skip != nil {
			skipped = append(skipped, *skip)
			continue
		}
		candidate.ID = id
		candidate.Title = recommendation.Title
		candidate.Priority = recommendation.Priority
		authorized = append(authorized, candidate)
	}
	return authorized, skipped
}

func getIdleAgents(ctx context.Context, session, agentTypeFilter string, verbose bool) ([]assignAgentInfo, error) {
	if ctx == nil {
		return nil, errors.New("idle-agent observation context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("idle-agent observation canceled: %w", err)
	}
	normalizedFilter := normalizeAgentTypeAlias(agentTypeFilter)
	if agentTypeFilter != "" && (normalizedFilter == "unknown" || normalizedFilter == "user") {
		return nil, fmt.Errorf("invalid agent type filter %q", agentTypeFilter)
	}

	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to get panes: %w", err)
	}
	observationCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(assignTimeout))
	defer cancel()
	observation, err := observeAssignSession(observationCtx, session)
	if err != nil {
		return nil, err
	}

	activePanes, err := loadActiveAssignmentPanes(session)
	if err != nil {
		return nil, err
	}

	idleAgents, verdicts, err := evaluateAssignPanePlaceability(panes, activePanes, observation, normalizedFilter, time.Now())
	if err != nil {
		return nil, err
	}

	if assignHumanDiagnostics(verbose) {
		for _, verdict := range verdicts {
			switch verdict.Verdict {
			case PlaceabilityNotAgentPane:
				continue
			case PlaceabilityPlaceable:
				fmt.Fprintf(os.Stderr, "[AUTO] Found idle agent: pane %s (%s)\n", verdict.PaneTarget, verdict.AgentType)
			default:
				detail := verdict.Verdict
				if verdict.Detail != "" {
					detail += " (" + verdict.Detail + ")"
				}
				fmt.Fprintf(os.Stderr, "[AUTO] Skipping pane %s (%s): %s\n", verdict.PaneTarget, verdict.AgentType, detail)
			}
		}
		fmt.Fprintf(os.Stderr, "[AUTO] Total idle agents available: %d\n", len(idleAgents))
	}

	return idleAgents, nil
}

func reconcilePendingTerminalAssignment(ctx context.Context, store *assignment.AssignmentStore, session string, current *assignment.Assignment) (bool, error) {
	if store == nil || current == nil {
		return false, errors.New("pending terminal assignment is required")
	}
	if current.PendingTerminalStatus != assignment.StatusCompleted && current.PendingTerminalStatus != assignment.StatusFailed {
		return false, nil
	}
	desiredStatus := current.PendingTerminalStatus
	desiredReason := current.PendingTerminalReason
	cleanupUnlock, err := store.AcquireExternalCleanupLock(ctx, current.BeadID)
	if err != nil {
		return false, fmt.Errorf("lock terminal external cleanup %s: %w", current.BeadID, err)
	}
	defer cleanupUnlock()
	if err := store.LoadStrict(); err != nil {
		return false, fmt.Errorf("refresh terminal external cleanup %s: %w", current.BeadID, err)
	}
	refreshed := store.Get(current.BeadID)
	if refreshed == nil {
		return true, nil
	}
	if !assignment.SameAssignmentGeneration(current, refreshed) {
		return false, nil
	}
	if refreshed.PendingTerminalStatus != desiredStatus || refreshed.PendingTerminalReason != desiredReason {
		return terminalReconciliationAlreadyCompleted(refreshed, desiredStatus, desiredReason), nil
	}
	current = refreshed
	if current.ClearState == assignment.ClearStateReservationReleasing {
		if _, err := releaseAssignmentLeases(ctx, session, current); err != nil {
			if persistErr := store.RecordClearReleaseFailed(ctx, current.BeadID, err); persistErr != nil {
				return false, errors.Join(err, fmt.Errorf("persist terminal reservation-release failure: %w", persistErr))
			}
			return false, err
		}
		var err error
		current, err = store.RecordClearLeasesReleased(ctx, current.BeadID)
		if err != nil {
			return false, fmt.Errorf("persist terminal reservation-release checkpoint: %w", err)
		}
	}
	if current.ClearState != assignment.ClearStateLeasesReleased {
		return false, fmt.Errorf("assignment %s is not ready for terminal claim release", current.BeadID)
	}
	if !current.TerminalClaimReleased {
		if strings.TrimSpace(current.ClaimActor) != "" {
			projectDir, err := resolveAssignProjectDir(ctx, session)
			if err != nil {
				claimErr := fmt.Errorf("resolve project for terminal Beads claim release: %w", err)
				if persistErr := store.RecordClearReleaseFailed(ctx, current.BeadID, claimErr); persistErr != nil {
					return false, errors.Join(claimErr, fmt.Errorf("persist terminal claim-release failure: %w", persistErr))
				}
				return false, claimErr
			}
			if _, err := releaseBeadClaimForAssignment(ctx, projectDir, current.BeadID, current.ClaimActor); err != nil {
				claimErr := fmt.Errorf("release terminal Beads claim for %s: %w", current.BeadID, err)
				if persistErr := store.RecordClearReleaseFailed(ctx, current.BeadID, claimErr); persistErr != nil {
					return false, errors.Join(claimErr, fmt.Errorf("persist terminal claim-release failure: %w", persistErr))
				}
				return false, claimErr
			}
		}
		var err error
		current, err = store.RecordTerminalClaimReleased(ctx, current.BeadID)
		if err != nil {
			return false, fmt.Errorf("persist terminal claim-release checkpoint: %w", err)
		}
	}
	if err := store.CompleteTerminalReconciliation(ctx, current.BeadID, current.PendingTerminalStatus, current.PendingTerminalReason); err != nil {
		if persistErr := store.RecordClearReleaseFailed(ctx, current.BeadID, err); persistErr != nil {
			return false, errors.Join(err, fmt.Errorf("persist terminal completion failure: %w", persistErr))
		}
		return false, err
	}
	return true, nil
}

func terminalReconciliationAlreadyCompleted(current *assignment.Assignment, status assignment.AssignmentStatus, reason string) bool {
	if current == nil || current.ClearState != assignment.ClearStateNone || current.PendingTerminalStatus != "" || current.Status != status {
		return false
	}
	if status == assignment.StatusFailed {
		return current.FailReason == reason
	}
	return status == assignment.StatusCompleted
}

type assignWatchMaintainer interface {
	MaintainAssignments(context.Context) error
}

func newAssignWatchMaintainer(ctx context.Context, session, projectDir string) (assignWatchMaintainer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		var err error
		projectDir, err = resolveAssignProjectDir(ctx, session)
		if err != nil {
			return nil, fmt.Errorf("resolve assignment maintenance project: %w", err)
		}
	}
	if projectDir == "" {
		return nil, errors.New("assignment maintenance requires an authoritative project directory")
	}
	projectDir, err := filepath.Abs(projectDir)
	if err != nil {
		return nil, fmt.Errorf("resolve assignment maintenance project: %w", err)
	}
	projectDir = agentmail.CanonicalProjectKey(projectDir)
	name := resolveCoordinatorIdentity(ctx, nil, session, projectDir)
	return coordinator.New(session, projectDir, newAgentMailClient(projectDir), name).WithCompletionEvents(), nil
}

// WatchLoop manages the continuous auto-assignment watch mode.
type WatchLoop struct {
	session  string
	strategy string
	store    *assignment.AssignmentStore
	detector *completion.CompletionDetector
	opts     *AutoReassignOptions

	stopWhenDone  bool
	delay         time.Duration
	limit         int
	quiet         bool
	verbose       bool
	idleThreshold time.Duration

	scanInterval time.Duration
	scanOpts     *AssignCommandOptions
	scanFn       func(context.Context) error

	maintenanceInterval time.Duration
	maintenanceTimeout  time.Duration
	newMaintainer       func(context.Context, string, string) (assignWatchMaintainer, error)
	maintenanceMu       sync.Mutex
	maintenanceError    string

	completionCh            chan completion.CompletionEvent
	stopCh                  chan struct{}
	stopOnce                sync.Once
	wg                      sync.WaitGroup
	lifecycleMu             sync.Mutex
	runStarted              bool
	runDone                 chan struct{}
	handledCompletionEvents map[string]struct{}
	handleCompletionFn      func(context.Context, completion.CompletionEvent) error
	ackCompletionEventFn    func(context.Context, string, string, string) (bool, error)
	renewCompletionEventFn  func(context.Context, string, string, string, time.Duration) (bool, error)

	mu                   sync.Mutex
	totalAssigned        int
	totalCompleted       int
	totalFailed          int
	startTime            time.Time
	lastAssignmentAt     time.Time
	deferredSourceChecks int
	sourceCheckDeferred  bool
}

// NewWatchLoop creates a new watch loop for a session.
func NewWatchLoop(session string, store *assignment.AssignmentStore, opts *AutoReassignOptions) *WatchLoop {
	scanInterval := assignWatchInterval
	if scanInterval <= 0 {
		scanInterval = 30 * time.Second
	}

	return &WatchLoop{
		session:             session,
		strategy:            opts.Strategy,
		store:               store,
		opts:                opts,
		stopWhenDone:        assignStopWhenDone,
		delay:               assignDelay,
		limit:               assignLimit,
		quiet:               opts.Quiet,
		verbose:             opts.Verbose,
		idleThreshold:       opts.IdleThreshold,
		scanInterval:        scanInterval,
		maintenanceInterval: 30 * time.Second,
		maintenanceTimeout:  30 * time.Second,
		scanOpts: &AssignCommandOptions{
			Session:         session,
			ProjectDir:      opts.ProjectDir,
			Strategy:        opts.Strategy,
			Relax:           opts.Relax,
			Limit:           assignLimit,
			AgentTypeFilter: opts.AgentTypeFilter,
			Template:        opts.Template,
			TemplateFile:    opts.TemplateFile,
			Verbose:         opts.Verbose,
			Quiet:           opts.Quiet,
			Auto:            !opts.DryRun,
			Timeout:         opts.Timeout,
			ReserveFiles:    opts.ReserveFiles,
			WithCASS:        opts.WithCASS,
			NoCASS:          opts.NoCASS,
			WithMemory:      opts.WithMemory,
			policyProject:   opts.policyProject,
		},
		stopCh:                  make(chan struct{}),
		runDone:                 make(chan struct{}),
		startTime:               time.Now(),
		handledCompletionEvents: make(map[string]struct{}),
	}
}

func (w *WatchLoop) logf(format string, args ...interface{}) {
	if w.quiet {
		return
	}
	timestamp := time.Now().Format("15:04:05")
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("[%s] %s\n", timestamp, msg)
}

func (w *WatchLoop) deferSourceChange(phase string, err error) bool {
	if !isRetryableAssignmentSourceChange(err) {
		return false
	}
	bv.ExpireTriageCache()
	w.mu.Lock()
	w.deferredSourceChecks++
	w.sourceCheckDeferred = true
	w.mu.Unlock()
	w.logf("Deferred %s after tracker or checkout changed; refreshing on the next ready-work scan: %v", phase, err)
	return true
}

func (w *WatchLoop) sourceCheckRecovered() {
	w.mu.Lock()
	deferred := w.sourceCheckDeferred
	w.sourceCheckDeferred = false
	w.mu.Unlock()
	if deferred {
		w.logf("Work-source verification recovered")
	}
}

// Run starts the watch loop and blocks until stopped.
func (w *WatchLoop) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("watch-loop context is required")
	}
	w.lifecycleMu.Lock()
	if w.runStarted {
		w.lifecycleMu.Unlock()
		return errors.New("watch loop is already running")
	}
	w.runStarted = true
	if w.runDone == nil {
		w.runDone = make(chan struct{})
	}
	runDone := w.runDone
	w.lifecycleMu.Unlock()
	defer close(runDone)
	if err := ctx.Err(); err != nil {
		return err
	}

	var maintainer assignWatchMaintainer
	if w.opts != nil && w.opts.ReserveFiles && !w.opts.DryRun {
		claim, claimErr := resilience.ClaimSessionCoordinator(ctx, w.session)
		if claimErr != nil {
			return fmt.Errorf("claim session coordination for assignment maintenance: %w", claimErr)
		}
		defer claim.Release()
		if claim.Preempted {
			w.logf("Paused the session monitor's coordinator while watch mode maintains assignments")
		}
		factory := w.newMaintainer
		if factory == nil {
			factory = newAssignWatchMaintainer
		}
		initCtx, cancel := context.WithTimeout(ctx, w.assignmentMaintenanceTimeout())
		var err error
		maintainer, err = factory(initCtx, w.session, w.opts.ProjectDir)
		contextErr := initCtx.Err()
		cancel()
		if err != nil || contextErr != nil {
			return fmt.Errorf("initialize assignment maintenance: %w", errors.Join(err, contextErr))
		}
		if maintainer == nil {
			return errors.New("assignment maintenance is unavailable")
		}
	}

	idleThreshold := w.idleThreshold
	if idleThreshold <= 0 {
		idleThreshold = config.DefaultAssignIdleThreshold
	}
	detectorCfg := completion.DetectionConfig{
		PollInterval:      assignWatchInterval,
		IdleThreshold:     idleThreshold,
		RetryOnError:      true,
		RetryInterval:     10 * time.Second,
		MaxRetries:        3,
		DedupWindow:       5 * time.Second,
		GracefulDegrading: true,
		CaptureLines:      50,
	}
	if w.opts != nil {
		detectorCfg.ProjectDir = w.opts.ProjectDir
	}
	w.detector = completion.NewWithConfig(w.session, w.store, detectorCfg)
	if err := w.detector.InitializationError(); err != nil {
		return fmt.Errorf("initialize completion detector: %w", err)
	}
	w.detector.SetTerminalReconciler(func(ctx context.Context, current *assignment.Assignment) (bool, error) {
		return reconcilePendingTerminalAssignment(ctx, w.store, w.session, current)
	})

	watchCtx, watchCancel := context.WithCancel(ctx)

	w.completionCh = make(chan completion.CompletionEvent, 10)
	eventsCh := w.detector.Watch(watchCtx)

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer close(w.completionCh)
		for {
			select {
			case event, ok := <-eventsCh:
				if !ok {
					return
				}
				select {
				case w.completionCh <- event:
				case <-watchCtx.Done():
					return
				case <-w.stopCh:
					return
				}
			case <-watchCtx.Done():
				return
			case <-w.stopCh:
				return
			}
		}
	}()
	var scanWG sync.WaitGroup
	var maintenanceWG sync.WaitGroup
	defer func() {
		watchCancel()
		w.wg.Wait()
		for range eventsCh {
		}
		scanWG.Wait()
		maintenanceWG.Wait()
	}()
	if maintainer != nil {
		maintenanceWG.Add(1)
		go func() {
			defer maintenanceWG.Done()
			w.maintainAssignmentLeases(watchCtx, maintainer)
		}()
	}

	w.logf("Starting watch mode with strategy=%s", w.strategy)

	scanInterval := w.scanInterval
	if scanInterval <= 0 {
		scanInterval = 30 * time.Second
	}
	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()
	scanDone := make(chan error, 1)
	scanInFlight := false

	for {
		select {
		case event, ok := <-w.completionCh:
			if !ok {
				if err := watchCtx.Err(); err != nil {
					return err
				}
				return nil
			}

			if err := w.consumeCompletionEvent(watchCtx, event); err != nil {
				if watchCtx.Err() != nil {
					return watchCtx.Err()
				}
				w.logf("Error handling completion: %v", err)
			}

			if w.stopWhenDone && !scanInFlight {
				stop, stopErr := w.shouldStop(watchCtx)
				if stopErr != nil {
					return fmt.Errorf("check watch completion: %w", stopErr)
				}
				if stop {
					w.logf("All beads complete. Exiting watch mode.")
					return nil
				}
			}

		case <-ticker.C:
			if scanInFlight {
				continue
			}
			scanInFlight = true
			scan := w.scanFn
			if scan == nil {
				scan = w.scanReadyWork
			}
			scanWG.Add(1)
			go func() {
				defer scanWG.Done()
				err := scan(watchCtx)
				select {
				case scanDone <- err:
				case <-watchCtx.Done():
				case <-w.stopCh:
				}
			}()

		case scanErr := <-scanDone:
			scanInFlight = false
			if scanErr != nil {
				if watchCtx.Err() != nil {
					return watchCtx.Err()
				}
				if w.deferSourceChange("ready-work scan", scanErr) {
					ticker.Reset(scanInterval)
					continue
				}
				if isAutomatedAssignmentSafetyError(scanErr) || errors.Is(scanErr, context.Canceled) || errors.Is(scanErr, context.DeadlineExceeded) {
					return scanErr
				}
				w.logf("Error during ready-work scan: %v", scanErr)
			} else {
				w.sourceCheckRecovered()
			}

			if w.stopWhenDone {
				stop, stopErr := w.shouldStop(watchCtx)
				if stopErr != nil {
					return fmt.Errorf("check watch completion after scan: %w", stopErr)
				}
				if stop {
					w.logf("All beads complete. Exiting watch mode.")
					return nil
				}
			}

		case <-watchCtx.Done():
			w.logf("Watch mode interrupted. Shutting down...")
			return watchCtx.Err()

		case <-w.stopCh:
			w.logf("Watch mode stopped.")
			return nil
		}
	}
}

func (w *WatchLoop) assignmentMaintenanceTimeout() time.Duration {
	if w.maintenanceTimeout > 0 && w.maintenanceTimeout < 30*time.Second {
		return w.maintenanceTimeout
	}
	return 30 * time.Second
}

func (w *WatchLoop) maintainAssignmentLeases(ctx context.Context, maintainer assignWatchMaintainer) {
	interval := w.maintenanceInterval
	if interval <= 0 || interval > 30*time.Second {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		passCtx, cancel := context.WithTimeout(ctx, w.assignmentMaintenanceTimeout())
		err := maintainer.MaintainAssignments(passCtx)
		err = errors.Join(err, passCtx.Err())
		cancel()
		if ctx.Err() != nil {
			return
		}
		w.reportAssignmentMaintenance(err)
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-ticker.C:
		}
	}
}

func (w *WatchLoop) reportAssignmentMaintenance(err error) {
	message := ""
	if err != nil {
		message = err.Error()
	}
	w.maintenanceMu.Lock()
	previous := w.maintenanceError
	w.maintenanceError = message
	w.maintenanceMu.Unlock()
	if message == previous {
		return
	}
	if message != "" {
		fmt.Fprintf(os.Stderr, "[%s] Warning: assignment reservation maintenance degraded: %s\n", time.Now().Format("15:04:05"), message)
	} else {
		w.logf("Assignment reservation maintenance recovered")
	}
}

func (w *WatchLoop) scanReadyWork(ctx context.Context) error {
	if w.scanOpts == nil {
		return nil
	}

	dryRun := w.opts != nil && w.opts.DryRun

	out, err := getAssignOutputEnhanced(ctx, w.scanOpts)
	if err != nil {
		return err
	}
	if out == nil || len(out.Assignments) == 0 {
		return nil
	}

	if dryRun {
		w.logf("Ready-work scan (dry-run): %d beads would be assigned (no dispatch)", len(out.Assignments))
		for _, assigned := range out.Assignments {
			w.logf("  Would assign %s -> pane %d (%s)", assigned.BeadID, assigned.Pane, assigned.AgentType)
		}
		return nil
	}

	execErr := executeAssignmentsEnhanced(ctx, w.session, out, w.scanOpts)

	w.mu.Lock()
	for _, assigned := range out.Assignments {
		if !assigned.PromptSent {
			continue
		}
		w.totalAssigned++
		w.lastAssignmentAt = time.Now()
		w.logf("Ready-work scan assigned: %s -> pane %d (%s)", assigned.BeadID, assigned.Pane, assigned.AgentType)
	}
	w.mu.Unlock()

	return execErr
}

func (w *WatchLoop) consumeCompletionEvent(ctx context.Context, event completion.CompletionEvent) error {
	eventID := strings.TrimSpace(event.EventID)
	consumerToken := strings.TrimSpace(event.ConsumerToken)
	if eventID != "" && (consumerToken == "" || event.LeaseDuration <= 0) {
		return fmt.Errorf("completion event %s has no durable consumer lease", eventID)
	}
	if w.handledCompletionEvents == nil {
		w.handledCompletionEvents = make(map[string]struct{})
	}
	_, alreadyHandled := w.handledCompletionEvents[eventID]
	if eventID == "" || !alreadyHandled {
		handle := w.handleCompletionFn
		if handle == nil {
			handle = w.handleCompletion
		}
		if eventID != "" {
			renewed, err := w.renewCompletionEventLease(ctx, event)
			if err != nil {
				return fmt.Errorf("validate completion event %s consumer lease: %w", eventID, err)
			}
			if !renewed {
				return fmt.Errorf("completion event %s consumer lease was lost before handling", eventID)
			}
		}
		handlerCtx := ctx
		cancelHandler := func() {}
		leaseDone := make(chan error, 1)
		if eventID != "" {
			handlerCtx, cancelHandler = context.WithCancel(ctx)
			go func() {
				leaseDone <- w.maintainCompletionEventLease(handlerCtx, cancelHandler, event)
			}()
		}
		handleErr := handle(handlerCtx, event)
		cancelHandler()
		if eventID != "" {
			if leaseErr := <-leaseDone; leaseErr != nil {
				return leaseErr
			}
		}
		if handleErr != nil {
			return handleErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if eventID != "" {
			w.handledCompletionEvents[eventID] = struct{}{}
		}
	}
	if eventID == "" {
		return nil
	}
	acknowledge := w.ackCompletionEventFn
	if acknowledge == nil {
		acknowledge = w.store.AcknowledgeCompletionEvent
	}
	acknowledged, err := acknowledge(ctx, event.BeadID, eventID, event.ConsumerToken)
	if err != nil {
		return fmt.Errorf("acknowledge completion event %s: %w", eventID, err)
	}
	if !acknowledged {
		return fmt.Errorf("completion event %s was superseded before acknowledgement", eventID)
	}
	return nil
}

func (w *WatchLoop) renewCompletionEventLease(ctx context.Context, event completion.CompletionEvent) (bool, error) {
	renew := w.renewCompletionEventFn
	if renew == nil {
		if w.store == nil {
			return false, errors.New("completion event lease store is required")
		}
		renew = w.store.RenewPendingCompletionEventLease
	}
	return renew(ctx, event.BeadID, event.EventID, event.ConsumerToken, event.LeaseDuration)
}

func (w *WatchLoop) maintainCompletionEventLease(ctx context.Context, cancelHandler context.CancelFunc, event completion.CompletionEvent) error {
	interval := event.LeaseDuration / 3
	if interval <= 0 {
		return fmt.Errorf("completion event %s has invalid lease duration %s", event.EventID, event.LeaseDuration)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			renewed, err := w.renewCompletionEventLease(ctx, event)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				cancelHandler()
				return fmt.Errorf("renew completion event %s lease: %w", event.EventID, err)
			}
			if !renewed {
				cancelHandler()
				return fmt.Errorf("completion event %s consumer lease was lost", event.EventID)
			}
		}
	}
}

func (w *WatchLoop) handleCompletion(ctx context.Context, event completion.CompletionEvent) error {
	if rawDelay := strings.TrimSpace(os.Getenv("NTM_TEST_COMPLETION_HANDLER_DELAY")); rawDelay != "" {
		delay, err := time.ParseDuration(rawDelay)
		if err != nil || delay < 0 {
			return fmt.Errorf("invalid completion handler E2E delay %q", rawDelay)
		}
		if err := waitContextDelay(ctx, delay); err != nil {
			return fmt.Errorf("completion handler E2E delay canceled: %w", err)
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	duration := event.Duration.Round(time.Second)

	if event.IsFailed {
		w.totalFailed++
		w.logf("Failed: %s by pane %d (%s) - %s", event.BeadID, event.Pane, event.AgentType, event.FailReason)
		return nil
	}

	w.totalCompleted++
	w.logf("Completion: %s by pane %d (%s, %v)", event.BeadID, event.Pane, event.AgentType, duration)

	dryRun := w.opts != nil && w.opts.DryRun

	if !dryRun && w.delay > 0 && !w.lastAssignmentAt.IsZero() {
		elapsed := time.Since(w.lastAssignmentAt)
		if elapsed < w.delay {
			sleepTime := w.delay - elapsed
			w.logf("Waiting %v before next assignment...", sleepTime.Round(time.Millisecond))
			if err := waitContextDelay(ctx, sleepTime); err != nil {
				return fmt.Errorf("assignment pacing canceled: %w", err)
			}
		}
	}

	if assignAutoReassign {
		var autoOpts *AutoReassignOptions
		if w.opts != nil {
			copied := *w.opts
			if copied.Limit <= 0 && w.limit > 0 {
				copied.Limit = w.limit
			}
			autoOpts = &copied
		}
		result, err := PerformAutoReassignment(ctx, event.BeadID, autoOpts)
		if err != nil {
			return fmt.Errorf("auto-reassignment failed: %w", err)
		}

		if len(result.NewlyUnblocked) > 0 {
			var ids []string
			for _, ub := range result.NewlyUnblocked {
				ids = append(ids, ub.ID)
			}
			w.logf("Unblocked: %s", strings.Join(ids, ", "))
		}

		processed := 0
		for _, assigned := range result.Assignments {
			if dryRun {
				w.logf("Would assign (dry-run): %s -> pane %d (%s)", assigned.BeadID, assigned.Pane, assigned.AgentType)
			} else {
				if !assigned.PromptSent {
					continue
				}
				w.totalAssigned++
				w.lastAssignmentAt = time.Now()
				w.logf("Assigned: %s -> pane %d (%s)", assigned.BeadID, assigned.Pane, assigned.AgentType)
			}
			processed++

			if w.limit > 0 && processed >= w.limit {
				w.logf("Assignment limit (%d) reached for this cycle", w.limit)
				break
			}
		}

		for _, errMsg := range result.Errors {
			w.logf("Warning: %s", errMsg)
		}
	}

	return nil
}

func (w *WatchLoop) shouldStop(ctx context.Context) (bool, error) {
	if ctx == nil {
		return false, errors.New("watch completion context is required")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	store, err := assignment.LoadStoreStrictReadOnly(w.session)
	if err != nil {
		return false, fmt.Errorf("read assignment ledger before stopping watch: %w", err)
	}
	active := store.ListActive()
	if len(active) > 0 {
		return false, nil
	}
	if len(store.ListPendingCompletionEvents()) > 0 {
		return false, nil
	}

	projectDir := ""
	if w.scanOpts != nil {
		projectDir = strings.TrimSpace(w.scanOpts.ProjectDir)
	}
	if projectDir == "" {
		var err error
		projectDir, err = resolveAssignProjectDir(ctx, w.session)
		if err != nil {
			return false, fmt.Errorf("resolve watch project: %w", err)
		}
	}
	var policyProject *string
	if w.scanOpts != nil {
		policyProject = &w.scanOpts.policyProject
	} else if w.opts != nil {
		policyProject = &w.opts.policyProject
	}
	if err := ensureAuthoritativeAssignmentPolicy(projectDir, policyProject); err != nil {
		return false, err
	}
	actionable, err := readAssignmentPreflight(ctx, projectDir, w.opts != nil && !w.opts.DryRun, func() ([]bv.TriageRecommendation, error) {
		return getActionableRecommendationsForWatch(ctx, projectDir, 0)
	})
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if w.deferSourceChange("watch completion check", err) {
			return false, nil
		}
		if isAutomatedAssignmentSafetyError(err) {
			return false, fmt.Errorf("automated assignment stopped because actionable work could not be verified: %w", err)
		}
		return false, fmt.Errorf("read actionable work for watch completion: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	w.sourceCheckRecovered()
	for _, rec := range actionable {
		if classifyTriageRecForAssignmentForProject(projectDir, rec, nil) == nil {
			return false, nil
		}
	}

	agentTypeFilter := ""
	if w.opts != nil {
		agentTypeFilter = w.opts.AgentTypeFilter
	}
	idleAgents, err := getIdleAgentsForWatchStop(ctx, w.session, agentTypeFilter, false)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, err
	}
	if err == nil && len(idleAgents) == 0 {
		w.logf("Warning: No idle agents available")
	}

	return true, nil
}

// Summary returns statistics about the watch session.
func (w *WatchLoop) Summary() string {
	w.maintenanceMu.Lock()
	maintenanceError := w.maintenanceError
	w.maintenanceMu.Unlock()
	w.mu.Lock()
	defer w.mu.Unlock()

	duration := time.Since(w.startTime).Round(time.Second)
	suffix := ""
	if w.opts != nil && w.opts.DryRun {
		suffix = " [dry-run: no panes were dispatched]"
	}
	if maintenanceError != "" {
		suffix += " [reservation maintenance degraded: " + maintenanceError + "]"
	}
	if w.deferredSourceChecks > 0 {
		suffix += fmt.Sprintf(" [source verification deferred %d times]", w.deferredSourceChecks)
	}
	return fmt.Sprintf("Watch session: %d assigned, %d completed, %d failed in %v%s",
		w.totalAssigned, w.totalCompleted, w.totalFailed, duration, suffix)
}

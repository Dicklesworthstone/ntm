package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/assign"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// RebalanceTransfer represents a suggested task transfer
type RebalanceTransfer struct {
	BeadID       string `json:"bead_id"`
	BeadTitle    string `json:"bead_title"`
	FromPane     int    `json:"from_pane"`
	FromTarget   string `json:"from_target"`
	FromPaneID   string `json:"from_pane_id"`
	FromAgent    string `json:"from_agent"`
	ToPane       int    `json:"to_pane"`
	ToTarget     string `json:"to_target"`
	ToPaneID     string `json:"to_pane_id"`
	ToAgent      string `json:"to_agent"`
	Reason       string `json:"reason"`
	sourceKey    string
	operationKey string
	prompt       string
}

// RebalanceWorkload represents workload for a single pane/agent
type RebalanceWorkload struct {
	Pane       int      `json:"pane"`
	PaneTarget string   `json:"pane_target"`
	PaneID     string   `json:"pane_id"`
	AgentType  string   `json:"agent_type"`
	AgentName  string   `json:"agent_name,omitempty"`
	TaskCount  int      `json:"task_count"`
	TaskIDs    []string `json:"task_ids,omitempty"`
	IsHealthy  bool     `json:"is_healthy"`
	IsIdle     bool     `json:"is_idle"`
	Status     string   `json:"status,omitempty"`
}

// RebalanceResponse is the JSON response for rebalance command
type RebalanceResponse struct {
	output.TimestampedResponse
	Success        bool                `json:"success"`
	Session        string              `json:"session"`
	ImbalanceScore float64             `json:"imbalance_score"`
	Recommendation string              `json:"recommendation"`
	Transfers      []RebalanceTransfer `json:"transfers"`
	Workloads      []RebalanceWorkload `json:"workloads"`
	Before         map[string]int      `json:"before"` // physical pane ID -> task count
	After          map[string]int      `json:"after"`  // physical pane ID -> task count after transfers
	Applied        bool                `json:"applied,omitempty"`
	DryRun         bool                `json:"dry_run,omitempty"`
}

const (
	rebalanceOperationKeySuffix = ":ntm-rebalance-v1"
	rebalancePromptMarker       = "This work was transferred by ntm rebalance."
	rebalanceRecoveryReason     = "recover_pending_dispatch"
)

var (
	resolveRebalanceProjectDir   = resolveAssignProjectDir
	loadRebalanceAssignmentStore = assignment.LoadStoreStrict
	getRebalancePanes            = tmux.GetPanesContext
)

type rebalanceCommandError struct {
	code string
	err  error
}

func (e *rebalanceCommandError) Error() string { return e.err.Error() }
func (e *rebalanceCommandError) Unwrap() error { return e.err }

func newRebalanceCommandError(code string, err error) error {
	return &rebalanceCommandError{code: code, err: err}
}

func newRebalanceCmd() *cobra.Command {
	var (
		dryRun    bool
		apply     bool
		filter    string
		threshold float64
		formatOut string
	)

	cmd := &cobra.Command{
		Use:   "rebalance [session]",
		Short: "Analyze workload distribution and suggest reassignments",
		Long: `Analyze workload distribution across agents and suggest reassignments.

The rebalance command analyzes current task assignments and identifies imbalances
where some agents are overloaded while others are idle. It produces recommendations
for transferring tasks to balance the workload.

Imbalance Score:
  0.0 = perfectly balanced
  0.5 = moderate imbalance
  1.0+ = severe imbalance (rebalance recommended)

The score is calculated as: stddev(workloads) / mean(workloads)

Examples:
  ntm rebalance myproject              # Show rebalance suggestions
  ntm rebalance myproject --dry-run    # Preview without prompting
  ntm rebalance myproject --apply      # Apply after confirmation
  ntm rebalance myproject --filter cc  # Only consider Claude agents
  ntm rebalance myproject --threshold 0.5  # Only suggest if score > 0.5
  ntm rebalance myproject --format json    # Robot mode JSON output`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			machineJSON := IsJSONOutput() || strings.EqualFold(strings.TrimSpace(formatOut), "json")
			session := ""
			if len(args) > 0 {
				session = args[0]
			}
			res, err := ResolveSessionWithOptions(session, cmd.OutOrStdout(), SessionResolveOptions{
				TreatAsJSON: machineJSON,
			})
			if err != nil {
				return err
			}
			if res.Session == "" {
				return nil
			}
			res.ExplainIfInferredForOutput(cmd.ErrOrStderr(), machineJSON)
			session = res.Session

			return runRebalance(cmd.Context(), session, dryRun, apply, filter, threshold, formatOut)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show suggestions without prompting for confirmation")
	cmd.Flags().BoolVar(&apply, "apply", false, "Prompt for confirmation before applying")
	cmd.Flags().StringVar(&filter, "filter", "", "Filter by agent type alias (claude|cc, codex|cod, gemini|gmi, antigravity|agy, cursor, windsurf|ws, aider, ollama)")
	cmd.Flags().Float64Var(&threshold, "threshold", 0.0, "Only suggest if imbalance score exceeds threshold")
	cmd.Flags().StringVar(&formatOut, "format", "", "Output format: json for robot mode")

	return cmd
}

func runRebalance(ctx context.Context, session string, dryRun, apply bool, filter string, threshold float64, formatOut string) error {
	isJSON := IsJSONOutput() || strings.EqualFold(strings.TrimSpace(formatOut), "json")
	if ctx == nil {
		err := errors.New("rebalance requires a command context")
		if isJSON {
			return outputRebalanceError(session, err)
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		err = fmt.Errorf("rebalance canceled: %w", err)
		if isJSON {
			return outputRebalanceError(session, err)
		}
		return err
	}
	projectDir, err := resolveRebalanceProjectDir(ctx, session)
	if err != nil {
		err = fmt.Errorf("resolve rebalance project: %w", err)
		if isJSON {
			return outputRebalanceError(session, err)
		}
		return err
	}
	if err := configureAuthoritativeAssignmentPolicy(projectDir); err != nil {
		err = newRebalanceCommandError(robot.ErrCodeInvalidFlag, err)
		if isJSON {
			return outputRebalanceError(session, err)
		}
		return err
	}
	normalizedFilter, err := normalizeAgentTypeFilter(filter)
	if err != nil {
		err = newRebalanceCommandError(robot.ErrCodeInvalidFlag, err)
		if isJSON {
			return outputRebalanceError(session, err)
		}
		return err
	}

	// Load assignment store
	store, err := loadRebalanceAssignmentStore(session)
	if err != nil {
		err = fmt.Errorf("failed to load assignments: %w", err)
		if isJSON {
			return outputRebalanceError(session, err)
		}
		return err
	}

	// Get pane information
	panes, err := getRebalancePanes(ctx, session)
	if err != nil {
		err = fmt.Errorf("failed to list panes: %w", err)
		if isJSON {
			return outputRebalanceError(session, err)
		}
		return err
	}

	if len(panes) == 0 {
		err = newRebalanceCommandError(robot.ErrCodePaneNotFound, fmt.Errorf("no panes found in session %s", session))
		if isJSON {
			return outputRebalanceError(session, err)
		}
		return err
	}

	// Build workload map
	workloads, err := buildRebalanceWorkloads(store, panes, normalizedFilter)
	if err != nil {
		err = fmt.Errorf("failed to build canonical rebalance topology: %w", err)
		if isJSON {
			return outputRebalanceError(session, err)
		}
		return err
	}

	// Calculate imbalance score
	imbalanceScore := calculateImbalanceScore(workloads)

	// A known-unsent rebalance generation is already durable at its target and
	// must be recovered even when the remaining workload is numerically balanced.
	recoveries := discoverPendingRebalanceTransfers(workloads, store)

	// Check threshold. Recovery is a correctness operation, not a new balancing
	// suggestion, so an imbalance threshold never suppresses it.
	if threshold > 0 && imbalanceScore < threshold && len(recoveries) == 0 {
		if isJSON {
			resp := RebalanceResponse{
				TimestampedResponse: output.NewTimestamped(),
				Success:             true,
				Session:             session,
				ImbalanceScore:      imbalanceScore,
				Recommendation:      "balanced",
				Transfers:           []RebalanceTransfer{},
				Workloads:           workloads,
				Before:              rebalanceWorkloadCounts(workloads),
				After:               rebalanceWorkloadCounts(workloads),
			}
			return outputRebalanceJSON(resp)
		}
		fmt.Printf("Session %s is balanced (score: %.2f < threshold: %.2f)\n", session, imbalanceScore, threshold)
		return nil
	}

	// Generate transfer suggestions
	transfers := make([]RebalanceTransfer, 0, len(recoveries)+len(workloads))
	transfers = append(transfers, recoveries...)
	transfers = append(transfers, suggestTransfers(workloads, store)...)

	// Calculate after state
	after := calculateAfterState(workloads, transfers)

	// Build response
	resp := RebalanceResponse{
		TimestampedResponse: output.NewTimestamped(),
		Success:             true,
		Session:             session,
		ImbalanceScore:      imbalanceScore,
		Recommendation:      getRecommendation(imbalanceScore),
		Transfers:           transfers,
		Workloads:           workloads,
		Before:              rebalanceWorkloadCounts(workloads),
		After:               after,
		DryRun:              dryRun,
	}

	if apply && len(transfers) > 0 && !dryRun && isJSON {
		if err := applyRebalanceTransfers(ctx, session, projectDir, store, transfers); err != nil {
			return outputRebalanceError(session, fmt.Errorf("failed to apply transfers: %w", err))
		}
		resp.Applied = true
	}

	if isJSON {
		return outputRebalanceJSON(resp)
	}

	// Human-readable output
	printRebalanceReport(resp)

	// If --apply, prompt for confirmation
	if apply && len(transfers) > 0 && !dryRun {
		var confirmed bool
		err := huh.NewConfirm().
			Title(fmt.Sprintf("Apply %d transfers?", len(transfers))).
			Description("This will reassign beads between agents.").
			Affirmative("Yes, apply").
			Negative("Cancel").
			Value(&confirmed).
			WithTheme(theme.HuhTheme()).
			Run()
		if err != nil {
			return fmt.Errorf("confirmation dialog: %w", err)
		}
		if confirmed {
			if err := applyRebalanceTransfers(ctx, session, projectDir, store, transfers); err != nil {
				return fmt.Errorf("failed to apply transfers: %w", err)
			}
			th := theme.Current()
			successStyle := lipgloss.NewStyle().Foreground(th.Success)
			fmt.Println(successStyle.Render("✓ Transfers applied successfully"))
		} else {
			fmt.Println("Cancelled.")
		}
	}

	return nil
}

func buildRebalanceWorkloads(store *assignment.AssignmentStore, panes []tmux.Pane, filter string) ([]RebalanceWorkload, error) {
	// Get active assignments
	active := store.ListActive()
	livePanes := make(map[string]tmux.Pane, len(panes))
	for _, pane := range panes {
		paneID := canonicalRebalancePaneID(pane)
		if paneID == "" {
			return nil, newRebalanceCommandError(robot.ErrCodePaneNotFound, fmt.Errorf("live pane %s has invalid physical identity %q", assignmentPaneTarget(pane), pane.ID))
		}
		if previous, exists := livePanes[paneID]; exists {
			return nil, newRebalanceCommandError(robot.ErrCodePaneNotFound, fmt.Errorf("live panes %s and %s share physical identity %s", assignmentPaneTarget(previous), assignmentPaneTarget(pane), paneID))
		}
		livePanes[paneID] = pane
	}

	// Count tasks by physical pane. Window-local indexes are display values and
	// may repeat across tmux windows, so they must never own rebalance state.
	paneTaskCount := make(map[string]int)
	paneTaskIDs := make(map[string][]string)
	paneAgentType := make(map[string]string)
	paneAgentName := make(map[string]string)

	for _, a := range active {
		pane, err := rebalanceAssignmentPane(livePanes, a)
		if err != nil {
			return nil, fmt.Errorf("active assignment %s: %w", a.BeadID, err)
		}
		paneID := canonicalRebalancePaneID(pane)
		paneTaskCount[paneID]++
		paneTaskIDs[paneID] = append(paneTaskIDs[paneID], a.BeadID)
		paneAgentType[paneID] = a.AgentType
		paneAgentName[paneID] = a.AgentName
	}

	workloads := make([]RebalanceWorkload, 0, len(panes))
	for _, pane := range panes {
		paneID := canonicalRebalancePaneID(pane)
		if paneID == "" {
			continue
		}
		agentType := paneAgentType[paneID]
		if agentType == "" {
			agentType = agentTypeForPane(pane)
		} else {
			agentType = robot.ResolveAgentType(agentType)
		}
		if agentType == "user" || agentType == "unknown" {
			continue
		}

		// Apply filter
		if filter != "" && !matchesRebalanceFilter(agentType, filter) {
			continue
		}

		workloads = append(workloads, RebalanceWorkload{
			Pane:       pane.Index,
			PaneTarget: assignmentPaneTarget(pane),
			PaneID:     paneID,
			AgentType:  agentType,
			AgentName:  paneAgentName[paneID],
			TaskCount:  paneTaskCount[paneID],
			TaskIDs:    paneTaskIDs[paneID],
			// GetPanes returned a canonical live agent pane. pane.Active only
			// means selected in tmux; dispatch health is proved by the fresh observer.
			IsHealthy: true,
			IsIdle:    paneTaskCount[paneID] == 0,
		})
	}

	// Sort by physical identity. Local pane indexes repeat across windows.
	sort.Slice(workloads, func(i, j int) bool {
		if workloads[i].PaneID != workloads[j].PaneID {
			return workloads[i].PaneID < workloads[j].PaneID
		}
		return workloads[i].PaneTarget < workloads[j].PaneTarget
	})
	for index := range workloads {
		sort.Strings(workloads[index].TaskIDs)
	}

	return workloads, nil
}

func rebalanceAssignmentPane(livePanes map[string]tmux.Pane, current *assignment.Assignment) (tmux.Pane, error) {
	if current == nil {
		return tmux.Pane{}, errors.New("assignment is nil")
	}
	physicalID, err := assignment.CanonicalPaneIdentity(current)
	if err != nil {
		return tmux.Pane{}, err
	}
	pane, ok := livePanes[physicalID]
	if !ok {
		return tmux.Pane{}, newRebalanceCommandError(robot.ErrCodePaneNotFound, fmt.Errorf("physical pane %s is not present in the live session topology", physicalID))
	}
	return pane, nil
}

func canonicalRebalancePaneID(pane tmux.Pane) string {
	paneID := strings.TrimSpace(pane.ID)
	if !strings.HasPrefix(paneID, "%") {
		return ""
	}
	return paneID
}

func matchesRebalanceFilter(agentType, filter string) bool {
	normalizedFilter, err := normalizeAgentTypeFilter(filter)
	if err != nil {
		return false
	}
	if normalizedFilter == "" {
		return true
	}
	return normalizeAgentTypeLike(agentType) == normalizedFilter
}

func normalizeAgentTypeFilter(filter string) (string, error) {
	trimmed := strings.TrimSpace(filter)
	if trimmed == "" {
		return "", nil
	}
	normalized := normalizeAgentTypeLike(trimmed)
	if normalized == "" {
		return "", fmt.Errorf("invalid agent filter %q: must be one of claude|cc, codex|cod, gemini|gmi, antigravity|agy, cursor, windsurf|ws, aider, ollama", filter)
	}
	return normalized, nil
}

func normalizeAgentTypeLike(value string) string {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" {
		return ""
	}
	if canonical := agentpkg.AgentType(trimmed).Canonical(); isSupportedWorkAgentType(canonical) {
		return string(canonical)
	}
	if head, _, ok := strings.Cut(trimmed, "_"); ok {
		if canonical := agentpkg.AgentType(head).Canonical(); isSupportedWorkAgentType(canonical) {
			return string(canonical)
		}
	}
	return ""
}

func isSupportedWorkAgentType(agentType agentpkg.AgentType) bool {
	switch agentType {
	case agentpkg.AgentTypeClaudeCode, agentpkg.AgentTypeCodex, agentpkg.AgentTypeGemini,
		agentpkg.AgentTypeAntigravity, agentpkg.AgentTypeCursor, agentpkg.AgentTypeWindsurf,
		agentpkg.AgentTypeAider, agentpkg.AgentTypeOpencode, agentpkg.AgentTypeOllama:
		return true
	default:
		return false
	}
}

func calculateImbalanceScore(workloads []RebalanceWorkload) float64 {
	if len(workloads) == 0 {
		return 0
	}

	// Calculate mean
	var sum float64
	for _, w := range workloads {
		sum += float64(w.TaskCount)
	}
	mean := sum / float64(len(workloads))

	if mean == 0 {
		return 0 // No tasks, perfectly balanced
	}

	// Calculate standard deviation
	var variance float64
	for _, w := range workloads {
		diff := float64(w.TaskCount) - mean
		variance += diff * diff
	}
	variance /= float64(len(workloads))
	stddev := math.Sqrt(variance)

	// Imbalance score = stddev / mean (coefficient of variation)
	return stddev / mean
}

func suggestTransfers(workloads []RebalanceWorkload, store *assignment.AssignmentStore) []RebalanceTransfer {
	if len(workloads) < 2 {
		return nil
	}

	// Calculate mean workload
	var total int
	for _, w := range workloads {
		total += w.TaskCount
	}
	mean := float64(total) / float64(len(workloads))

	// Find overloaded sources and unoccupied targets. Atomic assignment owns one
	// active bead per physical pane, so a pane with even one task is not a target.
	var sources, targets []RebalanceWorkload
	for _, w := range workloads {
		if float64(w.TaskCount) > mean+0.5 && w.TaskCount > 1 {
			sources = append(sources, w)
		} else if w.TaskCount == 0 && w.IsHealthy {
			targets = append(targets, w)
		}
	}

	// Sort sources by task count descending (move from most overloaded first)
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].TaskCount != sources[j].TaskCount {
			return sources[i].TaskCount > sources[j].TaskCount
		}
		return sources[i].PaneID < sources[j].PaneID
	})

	// Sort targets by task count ascending (move to most idle first)
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].TaskCount != targets[j].TaskCount {
			return targets[i].TaskCount < targets[j].TaskCount
		}
		return targets[i].PaneID < targets[j].PaneID
	})

	var transfers []RebalanceTransfer

	for i := range sources {
		source := &sources[i]
		if len(targets) == 0 {
			break
		}

		// Only a durable working generation has enough ownership metadata for
		// AtomicCoordinator's exact replacement protocol. Assigned rows may be
		// between dispatch and observation, so moving them would race live work.
		var transferable []*assignment.Assignment
		for _, beadID := range source.TaskIDs {
			a := store.Get(beadID)
			if a == nil || a.Status != assignment.StatusWorking ||
				strings.TrimSpace(a.IdempotencyKey) == "" || strings.TrimSpace(a.ClaimActor) == "" {
				continue
			}
			if rebalanceAssignmentMatchesWorkload(a, *source) {
				transferable = append(transferable, a)
			}
		}

		// Transfer tasks to targets
		for _, task := range transferable {
			if len(targets) == 0 || source.TaskCount <= int(mean) {
				break
			}

			target := &targets[0]

			// Prefer same agent type
			reason := "source_overloaded"
			if target.AgentType == source.AgentType {
				reason = "same_type_balance"
			} else if target.IsIdle {
				reason = "target_idle"
			}

			prompt := rebalanceTransferPrompt(task.BeadID, task.BeadTitle)
			operationKey := rebalanceOperationKey(task.BeadID, task.IdempotencyKey, target.PaneID, prompt)
			transfers = append(transfers, RebalanceTransfer{
				BeadID:       task.BeadID,
				BeadTitle:    task.BeadTitle,
				FromPane:     source.Pane,
				FromTarget:   source.PaneTarget,
				FromPaneID:   source.PaneID,
				FromAgent:    source.AgentType,
				ToPane:       target.Pane,
				ToTarget:     target.PaneTarget,
				ToPaneID:     target.PaneID,
				ToAgent:      target.AgentType,
				Reason:       reason,
				sourceKey:    task.IdempotencyKey,
				operationKey: operationKey,
				prompt:       prompt,
			})

			// One active assignment owns one physical pane. Once a target accepts
			// a transfer it is no longer available to this plan.
			source.TaskCount--
			target.TaskCount++
			targets = targets[1:]
		}
	}

	return transfers
}

func rebalanceAssignmentMatchesWorkload(current *assignment.Assignment, workload RebalanceWorkload) bool {
	if current == nil || strings.TrimSpace(workload.PaneID) == "" {
		return false
	}
	physicalID, err := assignment.CanonicalPaneIdentity(current)
	return err == nil && physicalID == workload.PaneID
}

func rebalanceTransferPrompt(beadID, beadTitle string) string {
	return fmt.Sprintf(
		"Work on bead %s: %s. %s Check dependencies first with `br dep tree %s`.",
		beadID, beadTitle, rebalancePromptMarker, beadID,
	)
}

func rebalanceOperationKey(beadID, sourceKey, targetPaneID, prompt string) string {
	return assignment.AssignmentIdempotencyKey(
		"ntm/rebalance/v1", beadID, sourceKey, targetPaneID, assignment.PromptSHA256(prompt),
	) + rebalanceOperationKeySuffix
}

func discoverPendingRebalanceTransfers(workloads []RebalanceWorkload, store *assignment.AssignmentStore) []RebalanceTransfer {
	if store == nil {
		return nil
	}
	workloadByPaneID := make(map[string]RebalanceWorkload, len(workloads))
	for _, workload := range workloads {
		if workload.PaneID != "" {
			workloadByPaneID[workload.PaneID] = workload
		}
	}

	var transfers []RebalanceTransfer
	for _, current := range store.ListActive() {
		if current == nil || current.Status != assignment.StatusClaimed || current.ClaimState != assignment.ClaimClaimed ||
			current.DispatchState != assignment.DispatchPending || current.DispatchAttempts < 1 ||
			strings.TrimSpace(current.LastDispatchError) == "" || strings.TrimSpace(current.PendingPrompt) == "" ||
			strings.TrimSpace(current.IdempotencyKey) == "" || !strings.HasSuffix(current.IdempotencyKey, rebalanceOperationKeySuffix) ||
			strings.TrimSpace(current.ClaimActor) == "" || current.DispatchReceiptID != "" || current.DispatchedAt != nil ||
			current.PromptSent != "" || !strings.Contains(current.PendingPrompt, rebalancePromptMarker) {
			continue
		}
		paneID, err := assignment.CanonicalPaneIdentity(current)
		if err != nil {
			continue
		}
		workload, ok := workloadByPaneID[paneID]
		if !ok {
			continue
		}
		transfers = append(transfers, RebalanceTransfer{
			BeadID: current.BeadID, BeadTitle: current.BeadTitle,
			FromPane: workload.Pane, FromTarget: workload.PaneTarget, FromPaneID: paneID, FromAgent: workload.AgentType,
			ToPane: workload.Pane, ToTarget: workload.PaneTarget, ToPaneID: paneID, ToAgent: workload.AgentType,
			Reason: rebalanceRecoveryReason, operationKey: current.IdempotencyKey, prompt: current.PendingPrompt,
		})
	}
	sort.Slice(transfers, func(i, j int) bool {
		if transfers[i].ToPaneID != transfers[j].ToPaneID {
			return transfers[i].ToPaneID < transfers[j].ToPaneID
		}
		return transfers[i].BeadID < transfers[j].BeadID
	})
	return transfers
}

func calculateAfterState(workloads []RebalanceWorkload, transfers []RebalanceTransfer) map[string]int {
	after := make(map[string]int)
	for _, w := range workloads {
		after[w.PaneID] = w.TaskCount
	}

	for _, t := range transfers {
		after[t.FromPaneID]--
		after[t.ToPaneID]++
	}

	return after
}

func rebalanceWorkloadCounts(workloads []RebalanceWorkload) map[string]int {
	counts := make(map[string]int)
	for _, w := range workloads {
		counts[w.PaneID] = w.TaskCount
	}
	return counts
}

func getRecommendation(score float64) string {
	if score < 0.3 {
		return "balanced"
	} else if score < 0.7 {
		return "moderate_imbalance"
	}
	return "rebalance_recommended"
}

type rebalanceAtomicExecutor interface {
	Execute(context.Context, assignment.AtomicRequest) (assignment.AtomicResult, error)
}

func applyRebalanceTransfers(ctx context.Context, session, projectDir string, store *assignment.AssignmentStore, transfers []RebalanceTransfer) error {
	if len(transfers) == 0 {
		return nil
	}
	if store == nil {
		return fmt.Errorf("atomic rebalance assignment store is not configured")
	}
	if err := store.LoadStrict(); err != nil {
		return fmt.Errorf("refresh assignment store before rebalance: %w", err)
	}
	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return fmt.Errorf("refresh rebalance topology: %w", err)
	}

	var reservationMgr *assign.FileReservationManager
	if rebalanceRequiresReservationManager(store, transfers) {
		amClient := newAgentMailClient(projectDir)
		if !amClient.IsAvailable() {
			return assignment.ErrReservationRequired
		}
		reservationMgr = assign.NewFileReservationManager(amClient, projectDir)
		reservationMgr.SetTTL(3600)
	}

	coordinator := newCLIAtomicAssignmentCoordinator(store, projectDir, reservationMgr)
	return applyTransfers(ctx, projectDir, session, store, panes, transfers, coordinator, newAssignSessionObserver())
}

func rebalanceRequiresReservationManager(store *assignment.AssignmentStore, transfers []RebalanceTransfer) bool {
	now := time.Now().UTC()
	for _, transfer := range transfers {
		current := store.Get(transfer.BeadID)
		if current == nil || !current.ReservationRequired {
			continue
		}
		recovering := strings.TrimSpace(transfer.operationKey) != "" && current.IdempotencyKey == transfer.operationKey
		reserved := current.ReservationState == assignment.ReservationReserved ||
			(current.ReservationState == "" && (current.ReservationCompleted || len(current.ReservationIDs) > 0 || len(current.ReservedPaths) > 0))
		expired := current.ReservationExpiresAt != nil && !current.ReservationExpiresAt.After(now)
		if !recovering || !reserved || expired {
			return true
		}
	}
	return false
}

func applyTransfers(
	ctx context.Context,
	projectDir, session string,
	store *assignment.AssignmentStore,
	panes []tmux.Pane,
	transfers []RebalanceTransfer,
	executor rebalanceAtomicExecutor,
	observer assignSessionObserver,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if store == nil || executor == nil || observer == nil {
		return fmt.Errorf("atomic rebalance is not fully configured")
	}
	if err := store.LoadStrict(); err != nil {
		return fmt.Errorf("refresh assignment store before rebalance: %w", err)
	}

	type preparedTransfer struct {
		request  assignment.AtomicRequest
		transfer *RebalanceTransfer
	}
	prepared := make([]preparedTransfer, 0, len(transfers))
	requests := make([]assignment.AtomicRequest, 0, len(transfers))
	for index := range transfers {
		request, err := prepareRebalanceTransfer(projectDir, session, store, panes, &transfers[index])
		if err != nil {
			return fmt.Errorf("prepare transfer %s: %w", transfers[index].BeadID, err)
		}
		prepared = append(prepared, preparedTransfer{request: request, transfer: &transfers[index]})
		requests = append(requests, request)
	}
	if err := requireFreshRebalanceTargets(ctx, session, requests, observer); err != nil {
		return err
	}

	for _, item := range prepared {
		request := item.request
		// Reobserve immediately before each external handoff. The initial pass
		// proves the whole plan was safe before mutation; this pass closes the
		// gap between that snapshot and each dispatch boundary.
		if err := requireFreshRebalanceTargets(ctx, session, []assignment.AtomicRequest{request}, observer); err != nil {
			return fmt.Errorf("transfer %s: %w", request.BeadID, err)
		}
		result, err := executor.Execute(ctx, request)
		if err != nil {
			err = preserveCommandContextError(ctx, err)
			return fmt.Errorf("transfer %s: %w", request.BeadID, err)
		}
		if result.Assignment == nil || result.Assignment.BeadID != request.BeadID ||
			result.Assignment.IdempotencyKey != request.IdempotencyKey || !result.Sent {
			return fmt.Errorf("transfer %s completed without its durable dispatch receipt", request.BeadID)
		}
		item.transfer.operationKey = request.IdempotencyKey
	}
	return nil
}

func prepareRebalanceTransfer(projectDir, session string, store *assignment.AssignmentStore, panes []tmux.Pane, transfer *RebalanceTransfer) (assignment.AtomicRequest, error) {
	var request assignment.AtomicRequest
	if transfer == nil || strings.TrimSpace(transfer.BeadID) == "" {
		return request, fmt.Errorf("transfer bead ID is required")
	}
	current := store.Get(transfer.BeadID)
	if current == nil {
		return request, fmt.Errorf("assignment no longer exists")
	}

	targetPane, err := resolveRebalanceTargetPane(panes, *transfer)
	if err != nil {
		return request, err
	}
	targetAgentType := agentTypeForPane(targetPane)
	if targetAgentType == "user" || targetAgentType == "unknown" {
		return request, fmt.Errorf("target %s is not an agent pane (type: %s)", assignmentPaneTarget(targetPane), targetAgentType)
	}
	if plannedType := normalizeAgentTypeLike(transfer.ToAgent); plannedType != "" && normalizeAgentTypeLike(targetAgentType) != plannedType {
		return request, fmt.Errorf("target %s changed agent type from %s to %s", assignmentPaneTarget(targetPane), transfer.ToAgent, targetAgentType)
	}

	operationKey := strings.TrimSpace(transfer.operationKey)
	if operationKey == "" {
		sourceKey := strings.TrimSpace(transfer.sourceKey)
		if sourceKey == "" {
			sourceKey = strings.TrimSpace(current.IdempotencyKey)
		}
		prompt := strings.TrimSpace(transfer.prompt)
		if prompt == "" {
			prompt = rebalanceTransferPrompt(transfer.BeadID, transfer.BeadTitle)
		}
		operationKey = rebalanceOperationKey(transfer.BeadID, sourceKey, targetPane.ID, prompt)
		transfer.sourceKey = sourceKey
		transfer.operationKey = operationKey
		transfer.prompt = prompt
	}

	recovering := current.IdempotencyKey == operationKey
	if !recovering {
		if current.Status != assignment.StatusWorking {
			return request, fmt.Errorf("assignment is %s, expected %s", current.Status, assignment.StatusWorking)
		}
		if transfer.sourceKey != "" && current.IdempotencyKey != transfer.sourceKey {
			return request, fmt.Errorf("assignment generation changed since planning")
		}
		if err := validateRebalanceSource(current, *transfer); err != nil {
			return request, err
		}
	} else {
		currentTarget, targetErr := assignment.CanonicalPaneIdentity(current)
		if targetErr != nil {
			return request, fmt.Errorf("recovery generation has no canonical target: %w", targetErr)
		}
		if currentTarget != targetPane.ID {
			return request, fmt.Errorf("recovery generation target changed from %s to %s", targetPane.ID, currentTarget)
		}
	}

	beadTitle := current.BeadTitle
	if strings.TrimSpace(beadTitle) == "" {
		beadTitle = transfer.BeadTitle
	}
	prompt := transfer.prompt
	if strings.TrimSpace(prompt) == "" {
		prompt = rebalanceTransferPrompt(transfer.BeadID, beadTitle)
		transfer.prompt = prompt
	}
	requestedPaths := append([]string(nil), current.ReservationRequested...)
	if len(requestedPaths) == 0 {
		requestedPaths = append(requestedPaths, current.ReservationInputPaths...)
	}
	discovery := current.ReservationDiscovery
	if len(requestedPaths) > 0 {
		discovery = false
	}
	multiWindow := tmux.PanesSpanMultipleWindows(panes)
	agentName := assignmentAgentIdentityForPane(projectDir, session, targetAgentType, targetPane, multiWindow)
	if recovering && strings.TrimSpace(current.AgentName) != "" {
		agentName = current.AgentName
		targetAgentType = current.AgentType
	}

	recoveredIntentSHA256 := ""
	if recovering {
		recoveredIntentSHA256 = strings.TrimSpace(current.IntentSHA256)
		if recoveredIntentSHA256 == "" {
			recoveredIntentSHA256 = strings.TrimSpace(current.PromptSHA256)
		}
	}

	return assignment.AtomicRequest{
		BeadID:                    transfer.BeadID,
		BeadTitle:                 beadTitle,
		Target:                    targetPane.ID,
		OccupancyKey:              targetPane.ID,
		Pane:                      targetPane.Index,
		AgentType:                 targetAgentType,
		AgentName:                 agentName,
		Actor:                     current.ClaimActor,
		Prompt:                    prompt,
		IdempotencyKey:            operationKey,
		RecoveredIntentSHA256:     recoveredIntentSHA256,
		RequireReservation:        current.ReservationRequired,
		AllowReservationDiscovery: discovery,
		RequestedPaths:            requestedPaths,
		ReservationTTL:            time.Hour,
		ReplaceWorkingAssignment:  true,
	}, nil
}

func resolveRebalanceTargetPane(panes []tmux.Pane, transfer RebalanceTransfer) (tmux.Pane, error) {
	selector := strings.TrimSpace(transfer.ToPaneID)
	if selector == "" {
		return tmux.Pane{}, fmt.Errorf("transfer has no canonical physical target")
	}
	resolved, err := tmux.ResolvePaneSelectors(panes, []string{selector}, true)
	if err != nil {
		return tmux.Pane{}, fmt.Errorf("resolve target %s: %w", selector, err)
	}
	if len(resolved) != 1 || strings.TrimSpace(resolved[0].ID) == "" {
		return tmux.Pane{}, fmt.Errorf("target %s did not resolve to one physical pane", selector)
	}
	if plannedID := strings.TrimSpace(transfer.ToPaneID); plannedID != "" && resolved[0].ID != plannedID {
		return tmux.Pane{}, fmt.Errorf("target %s changed physical pane ID from %s to %s", transfer.ToTarget, plannedID, resolved[0].ID)
	}
	return resolved[0], nil
}

func validateRebalanceSource(current *assignment.Assignment, transfer RebalanceTransfer) error {
	if current == nil {
		return fmt.Errorf("source assignment is missing")
	}
	plannedID := strings.TrimSpace(transfer.FromPaneID)
	if plannedID == "" {
		return fmt.Errorf("transfer has no canonical source")
	}
	currentID, err := assignment.CanonicalPaneIdentity(current)
	if err != nil {
		return fmt.Errorf("source has no canonical physical pane: %w", err)
	}
	if currentID != plannedID {
		return fmt.Errorf("source changed physical pane from %s to %s", plannedID, currentID)
	}
	return nil
}

func requireFreshRebalanceTargets(ctx context.Context, session string, requests []assignment.AtomicRequest, observer assignSessionObserver) error {
	observation, err := observer.Observe(ctx, session)
	if err != nil {
		return fmt.Errorf("fresh rebalance observation: %w", err)
	}
	now := time.Now().UTC()
	if !statuspkg.DispatchObservationIsCurrent(observation.ObservedAt, now) {
		return fmt.Errorf("rebalance observation is stale")
	}
	for _, request := range requests {
		if !observation.SafeToDispatch(request.Target) {
			return fmt.Errorf("target %s is not freshly and confidently idle", request.Target)
		}
	}
	return nil
}

func printRebalanceReport(resp RebalanceResponse) {
	th := theme.Current()
	titleStyle := lipgloss.NewStyle().Foreground(th.Blue).Bold(true)

	fmt.Printf("\n%s Workload Analysis for '%s'\n\n", titleStyle.Render("📊"), resp.Session)

	// Imbalance score
	var scoreStyle lipgloss.Style
	if resp.ImbalanceScore > 0.7 {
		scoreStyle = lipgloss.NewStyle().Foreground(th.Error)
	} else if resp.ImbalanceScore > 0.3 {
		scoreStyle = lipgloss.NewStyle().Foreground(th.Warning)
	} else {
		scoreStyle = lipgloss.NewStyle().Foreground(th.Success)
	}
	fmt.Printf("Imbalance Score: %s (%.2f)\n\n", scoreStyle.Render(resp.Recommendation), resp.ImbalanceScore)

	// Current workload distribution
	fmt.Println("Current Workload Distribution:")
	maxTasks := 0
	for _, w := range resp.Workloads {
		if w.TaskCount > maxTasks {
			maxTasks = w.TaskCount
		}
	}
	if maxTasks == 0 {
		maxTasks = 1
	}

	for _, w := range resp.Workloads {
		barLen := (w.TaskCount * 20) / maxTasks
		bar := strings.Repeat("█", barLen) + strings.Repeat("░", 20-barLen)

		status := ""
		if !w.IsHealthy {
			status = " (UNHEALTHY)"
		} else if w.IsIdle {
			status = " (idle)"
		}

		fmt.Printf("  pane %s [%s] (%s): %s %d tasks%s\n", w.PaneTarget, w.PaneID, w.AgentType, bar, w.TaskCount, status)
	}

	// Transfer suggestions
	if len(resp.Transfers) > 0 {
		fmt.Printf("\n%s Suggested Transfers:\n\n", titleStyle.Render("🔄"))
		for i, t := range resp.Transfers {
			fmt.Printf("  %d. [%s] \"%s\"\n", i+1, t.BeadID, t.BeadTitle)
			if t.Reason == rebalanceRecoveryReason {
				fmt.Printf("     retry pending dispatch on pane %s [%s] (%s)\n", t.ToTarget, t.ToPaneID, t.ToAgent)
			} else {
				fmt.Printf("     pane %s [%s] (%s) → pane %s [%s] (%s)\n", t.FromTarget, t.FromPaneID, t.FromAgent, t.ToTarget, t.ToPaneID, t.ToAgent)
			}
			fmt.Printf("     Reason: %s\n\n", t.Reason)
		}

		// After state
		fmt.Println("After Rebalance:")
		for _, w := range resp.Workloads {
			afterCount := resp.After[w.PaneID]
			barLen := (afterCount * 20) / maxTasks
			bar := strings.Repeat("█", barLen) + strings.Repeat("░", 20-barLen)
			fmt.Printf("  pane %s [%s] (%s): %s %d tasks\n", w.PaneTarget, w.PaneID, w.AgentType, bar, afterCount)
		}
	} else {
		fmt.Println("\nNo transfers suggested.")
	}
}

func outputRebalanceError(session string, err error) error {
	resp := struct {
		output.TimestampedResponse
		Success   bool                `json:"success"`
		Session   string              `json:"session"`
		Workloads []RebalanceWorkload `json:"workloads"`
		Transfers []RebalanceTransfer `json:"transfers"`
		ErrorCode string              `json:"error_code,omitempty"`
		Error     string              `json:"error"`
	}{
		TimestampedResponse: output.NewTimestamped(),
		Success:             false,
		Session:             session,
		Workloads:           []RebalanceWorkload{},
		Transfers:           []RebalanceTransfer{},
		Error:               err.Error(),
	}
	resp.ErrorCode = classifyRebalanceError(err)
	return emitJSONFailureEnvelope(resp)
}

func classifyRebalanceError(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return robot.ErrCodeTimeout
	}
	var commandErr *rebalanceCommandError
	if errors.As(err, &commandErr) && commandErr.code != "" {
		return commandErr.code
	}
	if errors.Is(err, assignment.ErrPaneIdentityMigrationRequired) {
		return "PANE_IDENTITY_MIGRATION_REQUIRED"
	}
	if errors.Is(err, assignment.ErrClaimIneligible) {
		return "BEAD_INELIGIBLE"
	}
	return robot.ErrCodeInternalError
}

func outputRebalanceJSON(v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

package cli

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// RebalanceTransfer represents a suggested task transfer
type RebalanceTransfer struct {
	BeadID    string `json:"bead_id"`
	BeadTitle string `json:"bead_title"`
	FromPane  int    `json:"from_pane"`
	FromAgent string `json:"from_agent"`
	ToPane    int    `json:"to_pane"`
	ToAgent   string `json:"to_agent"`
	Reason    string `json:"reason"`
}

// RebalanceWorkload represents workload for a single pane/agent
type RebalanceWorkload struct {
	Pane      int      `json:"pane"`
	AgentType string   `json:"agent_type"`
	AgentName string   `json:"agent_name,omitempty"`
	TaskCount int      `json:"task_count"`
	TaskIDs   []string `json:"task_ids,omitempty"`
	IsHealthy bool     `json:"is_healthy"`
	IsIdle    bool     `json:"is_idle"`
	Status    string   `json:"status,omitempty"`
}

// RebalanceResponse is the JSON response for rebalance command
type RebalanceResponse struct {
	output.TimestampedResponse
	Session        string              `json:"session"`
	ImbalanceScore float64             `json:"imbalance_score"`
	Recommendation string              `json:"recommendation"`
	Transfers      []RebalanceTransfer `json:"transfers"`
	Workloads      []RebalanceWorkload `json:"workloads"`
	Before         map[int]int         `json:"before"` // pane -> task count
	After          map[int]int         `json:"after"`  // pane -> task count after transfers
	Applied        bool                `json:"applied,omitempty"`
	DryRun         bool                `json:"dry_run,omitempty"`
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
			session := ""
			if len(args) > 0 {
				session = args[0]
			}
			res, err := ResolveSessionWithOptions(session, cmd.OutOrStdout(), SessionResolveOptions{
				TreatAsJSON: IsJSONOutput() || strings.EqualFold(formatOut, "json"),
			})
			if err != nil {
				return err
			}
			if res.Session == "" {
				return nil
			}
			res.ExplainIfInferred(cmd.ErrOrStderr())
			session = res.Session

			return runRebalance(session, dryRun, apply, filter, threshold, formatOut)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show suggestions without prompting for confirmation")
	cmd.Flags().BoolVar(&apply, "apply", false, "Prompt for confirmation before applying")
	cmd.Flags().StringVar(&filter, "filter", "", "Filter by agent type alias (claude|cc, codex|cod, gemini|gmi, cursor, windsurf|ws, aider, ollama)")
	cmd.Flags().Float64Var(&threshold, "threshold", 0.0, "Only suggest if imbalance score exceeds threshold")
	cmd.Flags().StringVar(&formatOut, "format", "", "Output format: json for robot mode")

	return cmd
}

func runRebalance(session string, dryRun, apply bool, filter string, threshold float64, formatOut string) error {
	isJSON := formatOut == "json"
	normalizedFilter, err := normalizeAgentTypeFilter(filter)
	if err != nil {
		if isJSON {
			return outputRebalanceError(session, err.Error())
		}
		return err
	}

	// Load assignment store
	store, err := assignment.LoadStore(session)
	if err != nil {
		if isJSON {
			return outputRebalanceError(session, fmt.Sprintf("failed to load assignments: %v", err))
		}
		return fmt.Errorf("failed to load assignment store: %w", err)
	}

	// Get pane information
	panes, err := tmux.GetPanes(session)
	if err != nil {
		if isJSON {
			return outputRebalanceError(session, fmt.Sprintf("failed to list panes: %v", err))
		}
		return fmt.Errorf("failed to list panes: %w", err)
	}

	if len(panes) == 0 {
		if isJSON {
			return outputRebalanceError(session, "no panes found in session")
		}
		return fmt.Errorf("no panes found in session %s", session)
	}

	// Build workload map
	workloads := buildRebalanceWorkloads(store, panes, normalizedFilter)

	// Calculate imbalance score
	imbalanceScore := calculateImbalanceScore(workloads)

	// Check threshold
	if threshold > 0 && imbalanceScore < threshold {
		if isJSON {
			resp := RebalanceResponse{
				TimestampedResponse: output.NewTimestamped(),
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
	transfers := suggestTransfers(workloads, store)

	// Calculate after state
	after := calculateAfterState(workloads, transfers)

	// Build response
	resp := RebalanceResponse{
		TimestampedResponse: output.NewTimestamped(),
		Session:             session,
		ImbalanceScore:      imbalanceScore,
		Recommendation:      getRecommendation(imbalanceScore),
		Transfers:           transfers,
		Workloads:           workloads,
		Before:              rebalanceWorkloadCounts(workloads),
		After:               after,
		DryRun:              dryRun,
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
			if err := applyTransfers(store, transfers); err != nil {
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

func buildRebalanceWorkloads(store *assignment.AssignmentStore, panes []tmux.Pane, filter string) []RebalanceWorkload {
	// Get active assignments
	active := store.ListActive()

	// Count tasks per pane
	paneTaskCount := make(map[int]int)
	paneTaskIDs := make(map[int][]string)
	paneAgentType := make(map[int]string)
	paneAgentName := make(map[int]string)

	for _, a := range active {
		paneTaskCount[a.Pane]++
		paneTaskIDs[a.Pane] = append(paneTaskIDs[a.Pane], a.BeadID)
		paneAgentType[a.Pane] = a.AgentType
		paneAgentName[a.Pane] = a.AgentName
	}

	var workloads []RebalanceWorkload
	for _, pane := range panes {
		agentType := paneAgentType[pane.Index]
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
			Pane:      pane.Index,
			AgentType: agentType,
			AgentName: paneAgentName[pane.Index],
			TaskCount: paneTaskCount[pane.Index],
			TaskIDs:   paneTaskIDs[pane.Index],
			IsHealthy: pane.Active,
			IsIdle:    paneTaskCount[pane.Index] == 0,
		})
	}

	// Sort by pane index for consistent output
	sort.Slice(workloads, func(i, j int) bool {
		return workloads[i].Pane < workloads[j].Pane
	})

	return workloads
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
		return "", fmt.Errorf("invalid agent filter %q: must be one of claude|cc, codex|cod, gemini|gmi, cursor, windsurf|ws, aider, ollama", filter)
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
		agentpkg.AgentTypeCursor, agentpkg.AgentTypeWindsurf, agentpkg.AgentTypeAider, agentpkg.AgentTypeOllama:
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

	// Find overloaded (sources) and underloaded (targets)
	var sources, targets []RebalanceWorkload
	for _, w := range workloads {
		if float64(w.TaskCount) > mean+0.5 && w.TaskCount > 1 {
			sources = append(sources, w)
		} else if float64(w.TaskCount) < mean-0.5 || (w.TaskCount == 0 && w.IsHealthy) {
			targets = append(targets, w)
		}
	}

	// Sort sources by task count descending (move from most overloaded first)
	sort.Slice(sources, func(i, j int) bool {
		return sources[i].TaskCount > sources[j].TaskCount
	})

	// Sort targets by task count ascending (move to most idle first)
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].TaskCount < targets[j].TaskCount
	})

	var transfers []RebalanceTransfer

	for i := range sources {
		source := &sources[i]
		if len(targets) == 0 {
			break
		}

		// Get assignments for this pane
		assignments := store.ListByPane(source.Pane)

		// Find transferable tasks (not in-progress/working)
		var transferable []*assignment.Assignment
		for _, a := range assignments {
			if a.Status == assignment.StatusAssigned {
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

			transfers = append(transfers, RebalanceTransfer{
				BeadID:    task.BeadID,
				BeadTitle: task.BeadTitle,
				FromPane:  source.Pane,
				FromAgent: source.AgentType,
				ToPane:    target.Pane,
				ToAgent:   target.AgentType,
				Reason:    reason,
			})

			// Update counts
			source.TaskCount--
			target.TaskCount++

			// Re-sort targets
			sort.Slice(targets, func(i, j int) bool {
				return targets[i].TaskCount < targets[j].TaskCount
			})
		}
	}

	return transfers
}

func calculateAfterState(workloads []RebalanceWorkload, transfers []RebalanceTransfer) map[int]int {
	after := make(map[int]int)
	for _, w := range workloads {
		after[w.Pane] = w.TaskCount
	}

	for _, t := range transfers {
		after[t.FromPane]--
		after[t.ToPane]++
	}

	return after
}

func rebalanceWorkloadCounts(workloads []RebalanceWorkload) map[int]int {
	counts := make(map[int]int)
	for _, w := range workloads {
		counts[w.Pane] = w.TaskCount
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

func applyTransfers(store *assignment.AssignmentStore, transfers []RebalanceTransfer) error {
	for _, t := range transfers {
		// Mark old assignment as reassigned
		if err := store.UpdateStatus(t.BeadID, assignment.StatusReassigned); err != nil {
			return fmt.Errorf("failed to update status for %s: %w", t.BeadID, err)
		}

		// Create new assignment
		_, err := store.Assign(t.BeadID, t.BeadTitle, t.ToPane, t.ToAgent, "", "")
		if err != nil {
			return fmt.Errorf("failed to reassign %s: %w", t.BeadID, err)
		}
	}

	return store.Save()
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

		fmt.Printf("  pane %d (%s): %s %d tasks%s\n", w.Pane, w.AgentType, bar, w.TaskCount, status)
	}

	// Transfer suggestions
	if len(resp.Transfers) > 0 {
		fmt.Printf("\n%s Suggested Transfers:\n\n", titleStyle.Render("🔄"))
		for i, t := range resp.Transfers {
			fmt.Printf("  %d. [%s] \"%s\"\n", i+1, t.BeadID, t.BeadTitle)
			fmt.Printf("     pane %d (%s) → pane %d (%s)\n", t.FromPane, t.FromAgent, t.ToPane, t.ToAgent)
			fmt.Printf("     Reason: %s\n\n", t.Reason)
		}

		// After state
		fmt.Println("After Rebalance:")
		for _, w := range resp.Workloads {
			afterCount := resp.After[w.Pane]
			barLen := (afterCount * 20) / maxTasks
			bar := strings.Repeat("█", barLen) + strings.Repeat("░", 20-barLen)
			fmt.Printf("  pane %d (%s): %s %d tasks\n", w.Pane, w.AgentType, bar, afterCount)
		}
	} else {
		fmt.Println("\nNo transfers suggested.")
	}
}

func outputRebalanceError(session, errMsg string) error {
	resp := struct {
		output.TimestampedResponse
		Success bool   `json:"success"`
		Session string `json:"session"`
		Error   string `json:"error"`
	}{
		TimestampedResponse: output.NewTimestamped(),
		Success:             false,
		Session:             session,
		Error:               errMsg,
	}
	data, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Println(string(data))
	return fmt.Errorf("%s", errMsg)
}

func outputRebalanceJSON(v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

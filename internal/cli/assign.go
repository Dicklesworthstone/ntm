package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/assign"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

var (
	assignAuto         bool
	assignStrategy     string
	assignBeads        string
	assignLimit        int
	assignAgentType    string // Filter by agent type
	assignCCOnly       bool   // Alias for --agent=claude
	assignCodOnly      bool   // Alias for --agent=codex
	assignGmiOnly      bool   // Alias for --agent=gemini
	assignTemplate     string // Prompt template: impl, review, custom
	assignTemplateFile string // Custom template file path
	assignVerbose      bool
	assignQuiet        bool
	assignTimeout      time.Duration
	assignDryRun       bool // Alias for no --auto
	assignReserveFiles bool // Enable Agent Mail file reservations

	// Direct pane assignment flags
	assignPane       int    // Direct pane assignment (0 = disabled, since pane 0 is valid we use -1 as default)
	assignForce      bool   // Force assignment even if pane busy
	assignIgnoreDeps bool   // Ignore dependency checks
	assignPrompt     string // Custom prompt for direct assignment

	// Clear assignment flags
	assignClear       string // Clear specific bead assignments (comma-separated)
	assignClearPane   int    // Clear all assignments for a pane (-1 = disabled)
	assignClearFailed bool   // Clear all failed assignments

	// Watch mode flags for continuous auto-assignment
	assignWatch         bool          // Enable watch mode for continuous auto-assignment on completion
	assignAutoReassign  bool          // Enable auto-reassignment of newly unblocked beads (default true in watch mode)
	assignWatchInterval time.Duration // How often to check for completions (default 30s)
)

// assignAgentInfo holds information about an agent pane for assignment matching
type assignAgentInfo struct {
	pane      tmux.Pane
	agentType string
	model     string
	state     string
}

func newAssignCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "assign [session]",
		Short: "Intelligently assign work to agents based on BV triage",
		Long: `Analyze ready work from BV and recommend or execute task-to-agent assignments.

This command queries BV for prioritized ready work and matches tasks to idle agents
based on agent type strengths and the selected strategy.

Strategies:
  balanced    - Balance workload across agents (default)
  speed       - Prioritize quick task completion
  quality     - Prioritize agent-task match quality
  dependency  - Prioritize unblocking downstream work
  round-robin - Deterministic even distribution

Prompt Templates:
  impl   - "Work on bead {BEAD_ID}: {TITLE}. Check dependencies first."
  review - "Review and verify bead {BEAD_ID}: {TITLE}. Run tests if applicable."
  custom - User provides template file (--template-file)

Direct Pane Assignment:
  Use --pane to assign a specific bead to a specific pane. This bypasses the
  normal strategy-based matching and directly assigns the bead to the pane.

  ntm assign myproject --pane=3 --beads=bd-123   # Assign bd-123 to pane 3
  ntm assign myproject --pane=3 --beads=bd-123 --prompt="Focus on API changes"
  ntm assign myproject --pane=0 --beads=bd-123 --force  # Force even if busy
  ntm assign myproject --pane=2 --beads=bd-123 --ignore-deps  # Skip dep checks

Clear Assignments:
  Use --clear to remove assignment from agents and release file reservations.
  Use --clear-pane to clear all assignments for a specific pane (agent crashed).
  Use --clear-failed to clear all failed assignments.
  Use --force to clear completed assignments.

  ntm assign myproject --clear bd-xyz             # Clear single assignment
  ntm assign myproject --clear bd-xyz,bd-abc      # Clear multiple assignments
  ntm assign myproject --clear-pane=3             # Clear all assignments for pane 3

Watch Mode (Dependency-Aware Auto-Assignment):
  Use --watch to enable continuous monitoring for task completions and automatic
  reassignment of newly unblocked beads to idle agents.

  ntm assign myproject --watch                    # Watch mode with auto-reassignment
  ntm assign myproject --watch --no-auto-reassign # Watch mode without auto-reassignment
  ntm assign myproject --watch --watch-interval=10s # Check every 10 seconds

Examples:
  ntm assign myproject                         # Show assignment recommendations
  ntm assign myproject --auto                  # Execute assignments without confirmation
  ntm assign myproject --strategy=quality      # Use quality-focused matching
  ntm assign myproject --strategy=round-robin  # Even distribution
  ntm assign myproject --beads=bd-123,bd-456   # Assign specific beads only
  ntm assign myproject --limit=5               # Limit to 5 assignments
  ntm assign myproject --cc-only               # Only assign to Claude agents
  ntm assign myproject --agent=codex           # Only assign to Codex agents
  ntm assign myproject --template=impl         # Use impl prompt template
  ntm assign myproject --json                  # Output as JSON
  ntm assign myproject --dry-run               # Preview without executing
  ntm assign myproject --clear bd-123          # Clear assignment for bead bd-123
  ntm assign myproject --clear-pane=3          # Clear all assignments for pane 3
  ntm assign myproject --clear-failed          # Clear all failed assignments
  ntm assign myproject --clear bd-123 --force  # Clear completed assignment`,
		Args: cobra.MaximumNArgs(1),
		RunE: runAssign,
	}

	// Core flags
	cmd.Flags().BoolVar(&assignAuto, "auto", false, "Execute assignments without confirmation")
	cmd.Flags().StringVar(&assignStrategy, "strategy", "balanced", "Assignment strategy: balanced, speed, quality, dependency, round-robin")
	cmd.Flags().StringVar(&assignBeads, "beads", "", "Comma-separated list of specific bead IDs to assign")
	cmd.Flags().IntVar(&assignLimit, "limit", 0, "Maximum number of assignments (0 = unlimited)")

	// Agent type filters
	cmd.Flags().StringVar(&assignAgentType, "agent", "", "Filter by agent type: claude, codex, gemini")
	cmd.Flags().BoolVar(&assignCCOnly, "cc-only", false, "Only assign to Claude agents (alias for --agent=claude)")
	cmd.Flags().BoolVar(&assignCodOnly, "cod-only", false, "Only assign to Codex agents (alias for --agent=codex)")
	cmd.Flags().BoolVar(&assignGmiOnly, "gmi-only", false, "Only assign to Gemini agents (alias for --agent=gemini)")

	// Prompt template flags
	cmd.Flags().StringVar(&assignTemplate, "template", "impl", "Prompt template: impl, review, custom")
	cmd.Flags().StringVar(&assignTemplateFile, "template-file", "", "Custom template file path (for --template=custom)")

	// Common flags
	cmd.Flags().BoolVarP(&assignVerbose, "verbose", "v", false, "Show detailed scoring/decision logs")
	cmd.Flags().BoolVarP(&assignQuiet, "quiet", "q", false, "Suppress non-essential output")
	cmd.Flags().DurationVar(&assignTimeout, "timeout", 30*time.Second, "Timeout for external calls (bv, br, Agent Mail)")
	cmd.Flags().BoolVar(&assignDryRun, "dry-run", false, "Preview mode (alias for no --auto)")
	cmd.Flags().BoolVar(&assignReserveFiles, "reserve-files", true, "Reserve file paths via Agent Mail before assignment")

	// Direct pane assignment flags
	cmd.Flags().IntVar(&assignPane, "pane", -1, "Assign bead directly to a specific pane (requires --beads)")
	cmd.Flags().BoolVar(&assignForce, "force", false, "Force assignment even if pane is busy (also allows --clear to remove completed assignments)")
	cmd.Flags().BoolVar(&assignIgnoreDeps, "ignore-deps", false, "Ignore dependency checks for assignment")
	cmd.Flags().StringVar(&assignPrompt, "prompt", "", "Custom prompt for direct assignment")

	// Clear assignment flags
	cmd.Flags().StringVar(&assignClear, "clear", "", "Clear specific bead assignments (comma-separated bead IDs)")
	cmd.Flags().IntVar(&assignClearPane, "clear-pane", -1, "Clear all assignments for a pane (use when agent crashed)")
	cmd.Flags().BoolVar(&assignClearFailed, "clear-failed", false, "Clear all failed assignments")

	// Watch mode flags
	cmd.Flags().BoolVar(&assignWatch, "watch", false, "Enable watch mode for continuous auto-assignment on completion")
	cmd.Flags().BoolVar(&assignAutoReassign, "auto-reassign", true, "Enable auto-reassignment of newly unblocked beads in watch mode")
	cmd.Flags().DurationVar(&assignWatchInterval, "watch-interval", 30*time.Second, "How often to check for completions in watch mode")

	return cmd
}

func runAssign(cmd *cobra.Command, args []string) error {
	var session string
	if len(args) > 0 {
		session = args[0]
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	// Resolve session
	res, err := ResolveSession(session, cmd.OutOrStdout())
	if err != nil {
		return err
	}
	if res.Session == "" {
		return nil
	}
	res.ExplainIfInferred(cmd.ErrOrStderr())
	session = res.Session

	// Handle clear operations first
	if assignClear != "" || assignClearPane >= 0 || assignClearFailed {
		return runClearAssignments(cmd, session)
	}

	// Handle watch mode for continuous auto-assignment
	if assignWatch {
		return runWatchMode(cmd, session)
	}

	// BV is preferred for dependency-aware assignment, but we can fall back to bd-ready
	// data when BV is unavailable.

	// Resolve agent type filter from flags
	agentTypeFilter := resolveAgentTypeFilter()

	// Parse beads if specified
	var beadIDs []string
	if assignBeads != "" {
		beadIDs = strings.Split(assignBeads, ",")
		for i := range beadIDs {
			beadIDs[i] = strings.TrimSpace(beadIDs[i])
		}
	}

	// --dry-run is an alias for no --auto
	if assignDryRun {
		assignAuto = false
	}

	// Build assign options
	assignOpts := &AssignCommandOptions{
		Session:         session,
		BeadIDs:         beadIDs,
		Strategy:        assignStrategy,
		Limit:           assignLimit,
		AgentTypeFilter: agentTypeFilter,
		Template:        assignTemplate,
		TemplateFile:    assignTemplateFile,
		Verbose:         assignVerbose,
		Quiet:           assignQuiet,
		Timeout:         assignTimeout,
		ReserveFiles:    assignReserveFiles,
		Pane:            assignPane,
		Force:           assignForce,
		IgnoreDeps:      assignIgnoreDeps,
		Prompt:          assignPrompt,
	}

	// Handle direct pane assignment if --pane is specified
	if assignPane >= 0 {
		return runDirectPaneAssignment(cmd, assignOpts)
	}

	// For JSON output, use enhanced JSON output
	if IsJSONOutput() {
		return runAssignJSON(assignOpts)
	}

	// For text output, get the data and format it nicely
	assignOutput, err := getAssignOutputEnhanced(assignOpts)
	if err != nil {
		return err
	}

	// Display the recommendations
	if !assignQuiet {
		displayAssignOutputEnhanced(assignOutput, assignVerbose)
	}

	// If no recommendations, we're done
	if len(assignOutput.Assigned) == 0 {
		return nil
	}

	// If auto mode, execute assignments
	if assignAuto {
		return executeAssignmentsEnhanced(session, assignOutput, assignOpts)
	}

	// Otherwise, prompt for confirmation
	if !assignQuiet {
		fmt.Println()
		fmt.Print("Execute all assignments? [y/N] ")
	}
	reader := bufio.NewReader(os.Stdin)
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(strings.ToLower(response))

	if response == "y" || response == "yes" {
		return executeAssignmentsEnhanced(session, assignOutput, assignOpts)
	}

	if !assignQuiet {
		fmt.Println("Assignments cancelled.")
	}
	return nil
}

// resolveAgentTypeFilter determines the agent type filter from flags
func resolveAgentTypeFilter() string {
	// Explicit --agent flag takes precedence
	if assignAgentType != "" {
		return strings.ToLower(assignAgentType)
	}
	// Convenience flags
	if assignCCOnly {
		return "claude"
	}
	if assignCodOnly {
		return "codex"
	}
	if assignGmiOnly {
		return "gemini"
	}
	return "" // No filter
}

// AssignCommandOptions holds all options for the assign command
type AssignCommandOptions struct {
	Session         string
	BeadIDs         []string
	Strategy        string
	Limit           int
	AgentTypeFilter string
	Template        string
	TemplateFile    string
	Verbose         bool
	Quiet           bool
	Timeout         time.Duration
	ReserveFiles    bool // Reserve file paths via Agent Mail before assignment

	// Direct pane assignment options
	Pane       int    // Direct pane assignment (-1 = disabled)
	Force      bool   // Force assignment even if pane busy
	IgnoreDeps bool   // Ignore dependency checks
	Prompt     string // Custom prompt for direct assignment

	// Clear assignment options
	Clear     string // Clear specific bead assignments (comma-separated)
	ClearPane int    // Clear all assignments for a pane (-1 = disabled)
}

// AssignOutputEnhanced is the enhanced output structure matching the spec
type AssignOutputEnhanced struct {
	Strategy string                `json:"strategy"`
	Assigned []AssignedItem        `json:"assigned"`
	Skipped  []SkippedItem         `json:"skipped"`
	Summary  AssignSummaryEnhanced `json:"summary"`
	Errors   []string              `json:"errors,omitempty"`
}

// AssignedItem represents a single assignment
type AssignedItem struct {
	BeadID     string  `json:"bead_id"`
	BeadTitle  string  `json:"bead_title"`
	Pane       int     `json:"pane"`
	AgentType  string  `json:"agent_type"`
	AgentName  string  `json:"agent_name,omitempty"`
	Score      float64 `json:"score"`
	PromptSent bool    `json:"prompt_sent"`
	Reasoning  string  `json:"reasoning,omitempty"`
}

// SkippedItem represents a skipped bead
type SkippedItem struct {
	BeadID       string   `json:"bead_id"`
	BeadTitle    string   `json:"bead_title,omitempty"`
	Reason       string   `json:"reason"`
	BlockedByIDs []string `json:"blocked_by_ids,omitempty"` // Only set when reason is "blocked"
}

// AssignSummaryEnhanced contains summary statistics
type AssignSummaryEnhanced struct {
	TotalBeads    int `json:"total_beads"`
	ActionableC   int `json:"actionable"` // Beads with no blockers
	BlockedCount  int `json:"blocked"`    // Beads blocked by dependencies
	Assigned      int `json:"assigned"`
	Skipped       int `json:"skipped"`
	IdleAgents    int `json:"idle_agents"`
	CycleWarnings int `json:"cycle_warnings,omitempty"` // Beads in dependency cycles
}

// getAssignOutput builds the assignment output without printing
func getAssignOutput(opts robot.AssignOptions) (*robot.AssignOutput, error) {
	if !tmux.SessionExists(opts.Session) {
		return nil, fmt.Errorf("session '%s' not found", opts.Session)
	}

	// Get panes from tmux
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		return nil, fmt.Errorf("failed to get panes: %w", err)
	}

	// Build agent info similar to robot.PrintAssign
	var idleAgentPanes []string
	totalAgents := 0

	for _, pane := range panes {
		agentType := detectAgentTypeFromTitle(pane.Title)
		if agentType == "user" || agentType == "unknown" {
			continue
		}
		totalAgents++

		// Capture state
		scrollback, _ := tmux.CapturePaneOutput(pane.ID, 10)
		state := determineAgentState(scrollback, agentType)
		if state == "idle" {
			idleAgentPanes = append(idleAgentPanes, fmt.Sprintf("%d", pane.Index))
		}
	}

	// Get beads from bv
	wd, _ := os.Getwd()
	readyBeads := bv.GetReadyPreview(wd, 50)
	inProgress := bv.GetInProgressList(wd, 50)

	// Filter to specific beads if requested
	if len(opts.Beads) > 0 {
		beadSet := make(map[string]bool)
		for _, b := range opts.Beads {
			beadSet[b] = true
		}
		var filtered []bv.BeadPreview
		for _, b := range readyBeads {
			if beadSet[b.ID] {
				filtered = append(filtered, b)
			}
		}
		readyBeads = filtered
	}

	// Generate recommendations
	recommendations := generateRecommendations(panes, readyBeads, opts.Strategy, idleAgentPanes)

	output := &robot.AssignOutput{
		Session:         opts.Session,
		Strategy:        opts.Strategy,
		Recommendations: recommendations,
		IdleAgents:      idleAgentPanes,
		Summary: robot.AssignSummary{
			TotalAgents:     totalAgents,
			IdleAgents:      len(idleAgentPanes),
			WorkingAgents:   totalAgents - len(idleAgentPanes),
			ReadyBeads:      len(readyBeads),
			Recommendations: len(recommendations),
		},
	}

	// Add warnings
	hints := &robot.AssignAgentHints{}
	if len(recommendations) == 0 && len(readyBeads) == 0 {
		hints.Summary = "No work available to assign"
	} else if len(recommendations) == 0 && len(idleAgentPanes) == 0 {
		hints.Summary = fmt.Sprintf("%d beads ready but no idle agents available", len(readyBeads))
	} else if len(recommendations) > 0 {
		hints.Summary = fmt.Sprintf("%d assignments recommended for %d idle agents", len(recommendations), len(idleAgentPanes))
	}

	if len(readyBeads) > len(idleAgentPanes) && len(idleAgentPanes) > 0 {
		diff := len(readyBeads) - len(idleAgentPanes)
		hints.Warnings = append(hints.Warnings,
			fmt.Sprintf("%d beads won't be assigned - not enough idle agents", diff))
	}

	for _, b := range inProgress {
		if b.UpdatedAt.IsZero() {
			continue
		}
		// Check for stale beads (simplified)
	}

	output.AgentHints = hints

	return output, nil
}

// generateRecommendations creates assignment recommendations
func generateRecommendations(panes []tmux.Pane, beads []bv.BeadPreview, strategy string, idleAgents []string) []robot.AssignRecommend {
	var recommendations []robot.AssignRecommend

	// Create a map of idle agents
	idleSet := make(map[string]bool)
	for _, a := range idleAgents {
		idleSet[a] = true
	}

	// Get idle pane details
	var idlePanes []tmux.Pane
	for _, p := range panes {
		paneKey := fmt.Sprintf("%d", p.Index)
		if idleSet[paneKey] {
			idlePanes = append(idlePanes, p)
		}
	}

	// Match beads to idle agents
	beadIdx := 0
	for _, pane := range idlePanes {
		if beadIdx >= len(beads) {
			break
		}

		bead := beads[beadIdx]
		agentType := detectAgentTypeFromTitle(pane.Title)
		model := detectModelFromTitle(agentType, pane.Title)

		confidence := calculateMatchConfidence(agentType, bead, strategy)
		reasoning := buildReasoning(agentType, bead, strategy)

		recommendations = append(recommendations, robot.AssignRecommend{
			Agent:      fmt.Sprintf("%d", pane.Index),
			AgentType:  agentType,
			Model:      model,
			AssignBead: bead.ID,
			BeadTitle:  bead.Title,
			Priority:   bead.Priority,
			Confidence: confidence,
			Reasoning:  reasoning,
		})

		beadIdx++
	}

	return recommendations
}

// detectAgentTypeFromTitle determines agent type from pane title
func detectAgentTypeFromTitle(title string) string {
	title = strings.ToLower(title)
	if strings.Contains(title, "__cc") || strings.Contains(title, "claude") {
		return "claude"
	}
	if strings.Contains(title, "__cod") || strings.Contains(title, "codex") {
		return "codex"
	}
	if strings.Contains(title, "__gmi") || strings.Contains(title, "gemini") {
		return "gemini"
	}
	if strings.Contains(title, "__user") || strings.Contains(title, "user") {
		return "user"
	}
	return "unknown"
}

// detectModelFromTitle extracts model variant from title
func detectModelFromTitle(agentType, title string) string {
	// Simplified model detection
	title = strings.ToLower(title)
	if strings.Contains(title, "opus") {
		return "opus"
	}
	if strings.Contains(title, "sonnet") {
		return "sonnet"
	}
	if strings.Contains(title, "haiku") {
		return "haiku"
	}
	return ""
}

// determineAgentState checks if agent is idle or working
func determineAgentState(scrollback, agentType string) string {
	lines := strings.Split(scrollback, "\n")
	if len(lines) == 0 {
		return "unknown"
	}

	lastLine := strings.TrimSpace(lines[len(lines)-1])

	// Look for common idle patterns
	idlePatterns := []string{
		"$", ">", ">>> ", "claude>", "codex>", "gemini>",
		"What would you like", "How can I help",
		"Ready for", "Waiting for",
	}

	for _, p := range idlePatterns {
		if strings.HasSuffix(lastLine, p) || strings.Contains(lastLine, p) {
			return "idle"
		}
	}

	return "working"
}

func assignmentAgentName(session, agentType string, paneIndex int) string {
	if session == "" {
		return ""
	}
	return fmt.Sprintf("%s_%s_%d", session, agentType, paneIndex)
}

// calculateMatchConfidence calculates how well an agent matches a task
func calculateMatchConfidence(agentType string, bead bv.BeadPreview, strategy string) float64 {
	baseConfidence := 0.7

	// Task type inference
	title := strings.ToLower(bead.Title)
	taskType := "task"

	taskPatterns := map[string][]string{
		"bug":           {"bug", "fix", "broken", "error", "crash"},
		"testing":       {"test", "spec", "coverage"},
		"documentation": {"doc", "readme", "comment"},
		"refactor":      {"refactor", "cleanup", "improve"},
		"analysis":      {"analyze", "investigate", "research"},
		"feature":       {"feature", "implement", "add", "new"},
	}

	for tt, patterns := range taskPatterns {
		for _, p := range patterns {
			if strings.Contains(title, p) {
				taskType = tt
				break
			}
		}
	}

	// Agent strengths
	strengths := map[string]map[string]float64{
		"claude": {"analysis": 0.9, "refactor": 0.9, "documentation": 0.8, "feature": 0.8, "bug": 0.7},
		"codex":  {"feature": 0.9, "bug": 0.8, "task": 0.8, "refactor": 0.6},
		"gemini": {"documentation": 0.9, "analysis": 0.8, "feature": 0.8},
	}

	if agentStrengths, ok := strengths[agentType]; ok {
		if strength, ok := agentStrengths[taskType]; ok {
			baseConfidence = strength
		}
	}

	// Strategy adjustments
	switch strategy {
	case "speed":
		baseConfidence = (baseConfidence + 0.9) / 2
	case "dependency":
		priority := parsePriorityString(bead.Priority)
		if priority <= 1 {
			baseConfidence = min(baseConfidence+0.1, 0.95)
		}
	}

	return baseConfidence
}

// parsePriorityString converts "P0"-"P4" to integer
func parsePriorityString(p string) int {
	if len(p) == 2 && p[0] == 'P' {
		if n := p[1] - '0'; n <= 4 {
			return int(n)
		}
	}
	return 2
}

// buildReasoning creates explanation for assignment
func buildReasoning(agentType string, bead bv.BeadPreview, strategy string) string {
	var reasons []string

	title := strings.ToLower(bead.Title)
	priority := parsePriorityString(bead.Priority)

	// Task-agent match
	if agentType == "claude" && (strings.Contains(title, "refactor") || strings.Contains(title, "analyze")) {
		reasons = append(reasons, "Claude excels at analysis/refactoring")
	} else if agentType == "codex" && (strings.Contains(title, "feature") || strings.Contains(title, "implement")) {
		reasons = append(reasons, "Codex excels at implementations")
	} else if agentType == "gemini" && strings.Contains(title, "doc") {
		reasons = append(reasons, "Gemini excels at documentation")
	}

	// Priority
	if priority == 0 {
		reasons = append(reasons, "critical priority")
	} else if priority == 1 {
		reasons = append(reasons, "high priority")
	}

	// Strategy
	switch strategy {
	case "balanced":
		reasons = append(reasons, "balanced workload")
	case "speed":
		reasons = append(reasons, "optimizing for speed")
	case "quality":
		reasons = append(reasons, "optimizing for quality")
	case "dependency":
		reasons = append(reasons, "prioritizing unblocks")
	}

	if len(reasons) == 0 {
		return "available agent matched to available work"
	}

	return strings.Join(reasons, "; ")
}

// displayAssignOutput renders the assignment output as formatted text
func displayAssignOutput(output *robot.AssignOutput) {
	th := theme.Current()

	// Header
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(th.Primary)

	subtitleStyle := lipgloss.NewStyle().
		Foreground(th.Subtext)

	fmt.Println()
	fmt.Println(titleStyle.Render(fmt.Sprintf("Task Assignment Recommendations for %s", output.Session)))
	fmt.Println(strings.Repeat("━", 50))

	// Summary
	fmt.Println()
	fmt.Printf("Strategy: %s\n", output.Strategy)
	fmt.Printf("Agents: %d total, %d idle, %d working\n",
		output.Summary.TotalAgents,
		output.Summary.IdleAgents,
		output.Summary.WorkingAgents)
	fmt.Printf("Beads: %d ready\n", output.Summary.ReadyBeads)

	// Hints summary
	if output.AgentHints != nil && output.AgentHints.Summary != "" {
		fmt.Println()
		fmt.Println(subtitleStyle.Render(output.AgentHints.Summary))
	}

	// Recommendations
	if len(output.Recommendations) > 0 {
		fmt.Println()
		fmt.Println(titleStyle.Render("Recommended Assignments:"))
		fmt.Println()

		for _, rec := range output.Recommendations {
			agentStyle := getAgentStyle(rec.AgentType, th)
			priorityStyle := getPriorityStyle(rec.Priority, th)

			// Agent badge
			agentBadge := agentStyle.Render(fmt.Sprintf("[%s pane %s]", rec.AgentType, rec.Agent))
			if rec.Model != "" {
				agentBadge = agentStyle.Render(fmt.Sprintf("[%s/%s pane %s]", rec.AgentType, rec.Model, rec.Agent))
			}

			// Priority badge
			priorityBadge := priorityStyle.Render(fmt.Sprintf("[%s]", rec.Priority))

			// Confidence
			confStr := fmt.Sprintf("(%.0f%% confidence)", rec.Confidence*100)

			fmt.Printf("  %s → %s %s %s\n",
				agentBadge,
				rec.AssignBead,
				priorityBadge,
				confStr)
			fmt.Printf("     %s\n", rec.BeadTitle)
			if rec.Reasoning != "" {
				fmt.Printf("     %s\n", subtitleStyle.Render(rec.Reasoning))
			}
			fmt.Println()
		}
	} else {
		fmt.Println()
		fmt.Println(subtitleStyle.Render("No assignments to recommend."))
	}

	// Warnings
	if output.AgentHints != nil && len(output.AgentHints.Warnings) > 0 {
		warnStyle := lipgloss.NewStyle().Foreground(th.Warning)
		fmt.Println(warnStyle.Render("Warnings:"))
		for _, w := range output.AgentHints.Warnings {
			fmt.Printf("  - %s\n", w)
		}
	}
}

// getAgentStyle returns a style for an agent type
func getAgentStyle(agentType string, th theme.Theme) lipgloss.Style {
	var color lipgloss.Color
	switch agentType {
	case "claude":
		color = th.Claude
	case "codex":
		color = th.Codex
	case "gemini":
		color = th.Gemini
	default:
		color = th.Text
	}
	return lipgloss.NewStyle().Foreground(color).Bold(true)
}

// getPriorityStyle returns a style for a priority level
func getPriorityStyle(priority string, th theme.Theme) lipgloss.Style {
	var color lipgloss.Color
	switch priority {
	case "P0":
		color = th.Error
	case "P1":
		color = th.Warning
	case "P2":
		color = th.Info
	default:
		color = th.Subtext
	}
	return lipgloss.NewStyle().Foreground(color)
}

// executeAssignments sends the assignments to agents
func executeAssignments(session string, recommendations []robot.AssignRecommend) error {
	fmt.Println()
	fmt.Println("Executing assignments...")

	for _, rec := range recommendations {
		// Build the prompt to send to the agent
		prompt := fmt.Sprintf("Please work on bead %s: %s", rec.AssignBead, rec.BeadTitle)

		// Send to the pane
		paneID := fmt.Sprintf("%s:%s", session, rec.Agent)
		if err := tmux.SendKeys(paneID, prompt, true); err != nil {
			fmt.Printf("  Failed to assign to pane %s: %v\n", rec.Agent, err)
			continue
		}

		fmt.Printf("  Assigned %s to pane %s (%s)\n", rec.AssignBead, rec.Agent, rec.AgentType)
	}

	fmt.Println()
	fmt.Println("Assignments sent. Use 'ntm status' to monitor progress.")
	return nil
}

// marshalAssignOutput converts output to JSON bytes (for testing)
func marshalAssignOutput(output *robot.AssignOutput) ([]byte, error) {
	return json.MarshalIndent(output, "", "  ")
}

// runAssignJSON handles JSON output for the assign command
func runAssignJSON(opts *AssignCommandOptions) error {
	assignOutput, err := getAssignOutputEnhanced(opts)
	if err != nil {
		// Return error as JSON
		errResp := output.NewError(err.Error())
		return json.NewEncoder(os.Stdout).Encode(errResp)
	}

	// Build full JSON response
	resp := struct {
		output.TimestampedResponse
		*AssignOutputEnhanced
	}{
		TimestampedResponse:  output.NewTimestamped(),
		AssignOutputEnhanced: assignOutput,
	}

	return json.NewEncoder(os.Stdout).Encode(resp)
}

// getAssignOutputEnhanced builds the enhanced assignment output
func getAssignOutputEnhanced(opts *AssignCommandOptions) (*AssignOutputEnhanced, error) {
	if !tmux.SessionExists(opts.Session) {
		return nil, fmt.Errorf("session '%s' not found", opts.Session)
	}

	// Get panes from tmux
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		return nil, fmt.Errorf("failed to get panes: %w", err)
	}

	// Build agent info and filter by type if needed
	var idleAgents []assignAgentInfo

	for _, pane := range panes {
		at := detectAgentTypeFromTitle(pane.Title)
		if at == "user" || at == "unknown" {
			continue
		}

		// Apply agent type filter
		if opts.AgentTypeFilter != "" && at != opts.AgentTypeFilter {
			continue
		}

		model := detectModelFromTitle(at, pane.Title)
		scrollback, _ := tmux.CapturePaneOutput(pane.ID, 10)
		state := determineAgentState(scrollback, at)

		if state == "idle" {
			idleAgents = append(idleAgents, assignAgentInfo{
				pane:      pane,
				agentType: at,
				model:     model,
				state:     state,
			})
		}
	}

	// Get beads from bv using triage recommendations for dependency awareness
	wd, _ := os.Getwd()
	allRecs, err := bv.GetTriageRecommendations(wd, 100)

	// Enhanced error handling for BV unavailability and stale graphs
	var readyBeads []bv.BeadPreview
	var blockedBeads []SkippedItem
	var fallbackReason string

	if err != nil {
		fallbackReason = fmt.Sprintf("BV triage unavailable: %v", err)
		result.Errors = append(result.Errors, fallbackReason)

		// Try alternative dependency checking methods
		if opts.Verbose {
			fmt.Fprintf(os.Stderr, "[DEP] %s, attempting fallbacks\n", fallbackReason)
		}

		// Fallback 1: Try BV insights to at least get cycle information
		cycles, cycleErr := CheckCycles(false)
		if cycleErr == nil && len(cycles) > 0 {
			if opts.Verbose {
				fmt.Fprintf(os.Stderr, "[DEP] Got cycle info from BV insights, filtering %d cycles\n", len(cycles))
			}
		}

		// Fallback 2: Use GetReadyPreview (no dependency info)
		readyBeads = bv.GetReadyPreview(wd, 50)
		if len(readyBeads) == 0 {
			if opts.Verbose {
				fmt.Fprintf(os.Stderr, "[DEP] No beads available from br list either\n")
			}
		}

		// Apply cycle filtering to fallback beads if we have cycle info
		if len(cycles) > 0 {
			filteredBeads, excluded := FilterCyclicBeads(readyBeads, false)
			for i := 0; i < excluded; i++ {
				// Add excluded beads to skipped list
				for _, bead := range readyBeads {
					if IsBeadInCycle(bead.ID, cycles) {
						blockedBeads = append(blockedBeads, SkippedItem{
							BeadID: bead.ID,
							Reason: "in_dependency_cycle",
						})
						break
					}
				}
			}
			readyBeads = filteredBeads
		}
	} else {
		// Filter blocked beads from recommendations
		for _, rec := range allRecs {
			// Skip if blocked by other beads
			if len(rec.BlockedBy) > 0 {
				blockedBeads = append(blockedBeads, SkippedItem{
					BeadID:       rec.ID,
					BeadTitle:    rec.Title,
					Reason:       "blocked_by_dependency",
					BlockedByIDs: rec.BlockedBy,
				})
				if opts.Verbose {
					fmt.Fprintf(os.Stderr, "[DEP] Skipping %s - blocked by: %v\n", rec.ID, rec.BlockedBy)
				}
				continue
			}
			// Convert TriageRecommendation to BeadPreview
			readyBeads = append(readyBeads, bv.BeadPreview{
				ID:       rec.ID,
				Title:    rec.Title,
				Priority: fmt.Sprintf("P%d", rec.Priority),
			})
		}
		if opts.Verbose && len(blockedBeads) > 0 {
			fmt.Fprintf(os.Stderr, "[DEP] Filtered %d blocked beads, %d actionable\n", len(blockedBeads), len(readyBeads))
		}
	}

	// Filter to specific beads if requested
	if len(opts.BeadIDs) > 0 {
		beadSet := make(map[string]bool)
		for _, b := range opts.BeadIDs {
			beadSet[b] = true
		}
		var filtered []bv.BeadPreview
		for _, b := range readyBeads {
			if beadSet[b.ID] {
				filtered = append(filtered, b)
			}
		}
		readyBeads = filtered
		// Also filter blockedBeads to only include requested ones
		var filteredBlocked []SkippedItem
		for _, b := range blockedBeads {
			if beadSet[b.BeadID] {
				filteredBlocked = append(filteredBlocked, b)
			}
		}
		blockedBeads = filteredBlocked
	}

	// Filter out beads in dependency cycles (with warning)
	var cycleWarnings int
	var cyclicBeads []SkippedItem
	cycles, _ := CheckCycles(false)
	if len(cycles) > 0 {
		var nonCyclic []bv.BeadPreview
		for _, bead := range readyBeads {
			if IsBeadInCycle(bead.ID, cycles) {
				cyclicBeads = append(cyclicBeads, SkippedItem{
					BeadID:    bead.ID,
					BeadTitle: bead.Title,
					Reason:    "in_dependency_cycle",
				})
				cycleWarnings++
				if opts.Verbose {
					fmt.Fprintf(os.Stderr, "[DEP] Excluding %s from assignment (in dependency cycle)\n", bead.ID)
				}
			} else {
				nonCyclic = append(nonCyclic, bead)
			}
		}
		readyBeads = nonCyclic
	}

	// Limit ready beads to 50
	if len(readyBeads) > 50 {
		readyBeads = readyBeads[:50]
	}

	// Combine all skipped beads
	allSkipped := append(blockedBeads, cyclicBeads...)

	result := &AssignOutputEnhanced{
		Strategy: opts.Strategy,
		Assigned: make([]AssignedItem, 0),
		Skipped:  allSkipped, // Blocked + cyclic beads
		Summary: AssignSummaryEnhanced{
			TotalBeads:    len(readyBeads) + len(blockedBeads) + cycleWarnings,
			ActionableC:   len(readyBeads),
			BlockedCount:  len(blockedBeads),
			IdleAgents:    len(idleAgents),
			CycleWarnings: cycleWarnings,
		},
	}

	// No idle agents available
	if len(idleAgents) == 0 {
		for _, bead := range readyBeads {
			result.Skipped = append(result.Skipped, SkippedItem{
				BeadID: bead.ID,
				Reason: "no_idle_agents",
			})
		}
		result.Summary.Skipped = len(readyBeads)
		return result, nil
	}

	// No beads to assign
	if len(readyBeads) == 0 {
		return result, nil
	}

	// Generate assignments using strategy
	assignments := generateAssignmentsEnhanced(idleAgents, readyBeads, opts)

	// Apply limit
	if opts.Limit > 0 && len(assignments) > opts.Limit {
		// Mark excess as skipped
		for _, item := range assignments[opts.Limit:] {
			result.Skipped = append(result.Skipped, SkippedItem{
				BeadID: item.BeadID,
				Reason: "limit_reached",
			})
		}
		assignments = assignments[:opts.Limit]
	}

	result.Assigned = assignments
	result.Summary.Assigned = len(assignments)
	result.Summary.Skipped = len(result.Skipped)

	return result, nil
}

// generateAssignmentsEnhanced creates assignment recommendations using the enhanced strategy logic
func generateAssignmentsEnhanced(agents []assignAgentInfo, beads []bv.BeadPreview, opts *AssignCommandOptions) []AssignedItem {
	var assignments []AssignedItem

	switch strings.ToLower(opts.Strategy) {
	case "round-robin":
		// Deterministic round-robin: bead[i] -> agent[i % N]
		// Score is always 1.0 (all assignments equally valid in round-robin)
		// Distribution: beads evenly spread, first agents get +1 if uneven
		if opts.Verbose && len(agents) > 0 {
			// Log distribution plan
			base := len(beads) / len(agents)
			extra := len(beads) % len(agents)
			fmt.Fprintf(os.Stderr, "Round-robin distribution plan: %d beads across %d agents\n", len(beads), len(agents))
			for i, a := range agents {
				count := base
				if i < extra {
					count++
				}
				fmt.Fprintf(os.Stderr, "  Agent %d (%s): %d beads\n", a.pane.Index, a.agentType, count)
			}
		}
		for i, bead := range beads {
			if len(agents) == 0 {
				break
			}
			agent := agents[i%len(agents)]
			assignments = append(assignments, AssignedItem{
				BeadID:    bead.ID,
				BeadTitle: bead.Title,
				Pane:      agent.pane.Index,
				AgentType: agent.agentType,
				AgentName: assignmentAgentName(opts.Session, agent.agentType, agent.pane.Index),
				Score:     1.0, // Round-robin: all assignments equally valid
				Reasoning: fmt.Sprintf("round-robin slot %d → agent %d", i+1, i%len(agents)),
			})
		}

	case "quality":
		// Quality: assign each bead to the best-matching available agent
		usedAgents := make(map[int]bool)
		for _, bead := range beads {
			var bestAgent *assignAgentInfo
			var bestScore float64

			for i := range agents {
				if usedAgents[agents[i].pane.Index] {
					continue
				}
				score := assign.GetAgentScoreByString(agents[i].agentType, inferTaskTypeFromBead(bead))
				if score > bestScore {
					bestScore = score
					bestAgent = &agents[i]
				}
			}

			if bestAgent != nil {
				assignments = append(assignments, AssignedItem{
					BeadID:    bead.ID,
					BeadTitle: bead.Title,
					Pane:      bestAgent.pane.Index,
					AgentType: bestAgent.agentType,
					AgentName: assignmentAgentName(opts.Session, bestAgent.agentType, bestAgent.pane.Index),
					Score:     bestScore,
					Reasoning: buildReasoning(bestAgent.agentType, bead, "quality"),
				})
				usedAgents[bestAgent.pane.Index] = true
			}
		}

	case "speed":
		// Speed: assign to first available agent
		usedAgents := make(map[int]bool)
		for _, bead := range beads {
			for i := range agents {
				if usedAgents[agents[i].pane.Index] {
					continue
				}
				score := (calculateMatchConfidence(agents[i].agentType, bead, "speed") + 0.9) / 2
				assignments = append(assignments, AssignedItem{
					BeadID:    bead.ID,
					BeadTitle: bead.Title,
					Pane:      agents[i].pane.Index,
					AgentType: agents[i].agentType,
					AgentName: assignmentAgentName(opts.Session, agents[i].agentType, agents[i].pane.Index),
					Score:     score,
					Reasoning: buildReasoning(agents[i].agentType, bead, "speed"),
				})
				usedAgents[agents[i].pane.Index] = true
				break
			}
		}

	case "dependency":
		// Dependency: prioritize by unblocks count (already sorted by bv)
		usedAgents := make(map[int]bool)
		for _, bead := range beads {
			var bestAgent *assignAgentInfo
			var bestScore float64

			for i := range agents {
				if usedAgents[agents[i].pane.Index] {
					continue
				}
				score := calculateMatchConfidence(agents[i].agentType, bead, "dependency")
				// Boost for high priority
				priority := parsePriorityString(bead.Priority)
				if priority <= 1 {
					score = min(score+0.1, 0.95)
				}
				if score > bestScore {
					bestScore = score
					bestAgent = &agents[i]
				}
			}

			if bestAgent != nil {
				assignments = append(assignments, AssignedItem{
					BeadID:    bead.ID,
					BeadTitle: bead.Title,
					Pane:      bestAgent.pane.Index,
					AgentType: bestAgent.agentType,
					AgentName: assignmentAgentName(opts.Session, bestAgent.agentType, bestAgent.pane.Index),
					Score:     bestScore,
					Reasoning: buildReasoning(bestAgent.agentType, bead, "dependency"),
				})
				usedAgents[bestAgent.pane.Index] = true
			}
		}

	default: // balanced
		// Balanced: spread work evenly, considering existing load
		agentAssignCounts := make(map[int]int)
		for _, bead := range beads {
			var bestAgent *assignAgentInfo
			var bestScore float64
			minAssigns := int(^uint(0) >> 1)

			for i := range agents {
				count := agentAssignCounts[agents[i].pane.Index]
				score := calculateMatchConfidence(agents[i].agentType, bead, "balanced")

				// Prefer agents with fewer assignments, then by score
				if count < minAssigns || (count == minAssigns && score > bestScore) {
					minAssigns = count
					bestScore = score
					bestAgent = &agents[i]
				}
			}

			if bestAgent != nil {
				assignments = append(assignments, AssignedItem{
					BeadID:    bead.ID,
					BeadTitle: bead.Title,
					Pane:      bestAgent.pane.Index,
					AgentType: bestAgent.agentType,
					AgentName: assignmentAgentName(opts.Session, bestAgent.agentType, bestAgent.pane.Index),
					Score:     bestScore,
					Reasoning: buildReasoning(bestAgent.agentType, bead, "balanced"),
				})
				agentAssignCounts[bestAgent.pane.Index]++
			}
		}
	}

	return assignments
}

// inferTaskTypeFromBead determines task type from bead metadata
func inferTaskTypeFromBead(bead bv.BeadPreview) string {
	title := strings.ToLower(bead.Title)
	rules := []struct {
		typ string
		kws []string
	}{
		{"bug", []string{"bug", "fix", "broken", "error", "crash"}},
		{"testing", []string{"test", "spec", "coverage"}},
		{"documentation", []string{"doc", "readme", "comment"}},
		{"refactor", []string{"refactor", "cleanup", "improve"}},
		{"analysis", []string{"analyze", "investigate", "research"}},
		{"feature", []string{"feature", "implement", "add", "new"}},
	}
	for _, r := range rules {
		for _, kw := range r.kws {
			if strings.Contains(title, kw) {
				return r.typ
			}
		}
	}
	return "task"
}

// displayAssignOutputEnhanced renders the enhanced assignment output
func displayAssignOutputEnhanced(out *AssignOutputEnhanced, verbose bool) {
	th := theme.Current()

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(th.Primary)
	subtitleStyle := lipgloss.NewStyle().Foreground(th.Subtext)

	fmt.Println()
	fmt.Println(titleStyle.Render("Task Assignment Recommendations"))
	fmt.Println(strings.Repeat("━", 50))

	// Summary
	fmt.Println()
	fmt.Printf("Strategy: %s\n", out.Strategy)
	fmt.Printf("Idle Agents: %d | Actionable Beads: %d", out.Summary.IdleAgents, out.Summary.ActionableC)
	if out.Summary.BlockedCount > 0 {
		fmt.Printf(" | Blocked: %d", out.Summary.BlockedCount)
	}
	fmt.Println()

	// Assignments
	if len(out.Assigned) > 0 {
		fmt.Println()
		fmt.Println(titleStyle.Render("Recommended Assignments:"))
		fmt.Println()

		for _, item := range out.Assigned {
			agentStyle := getAgentStyle(item.AgentType, th)
			agentBadge := agentStyle.Render(fmt.Sprintf("[%s pane %d]", item.AgentType, item.Pane))
			confStr := fmt.Sprintf("(%.0f%% score)", item.Score*100)

			fmt.Printf("  %s → %s %s\n", agentBadge, item.BeadID, confStr)
			fmt.Printf("     %s\n", item.BeadTitle)
			if verbose && item.Reasoning != "" {
				fmt.Printf("     %s\n", subtitleStyle.Render(item.Reasoning))
			}
			fmt.Println()
		}
	} else {
		fmt.Println()
		fmt.Println(subtitleStyle.Render("No assignments to recommend."))
		if out.Summary.IdleAgents == 0 {
			fmt.Println(subtitleStyle.Render("  Reason: No idle agents available"))
		} else if out.Summary.TotalBeads == 0 {
			fmt.Println(subtitleStyle.Render("  Reason: No ready beads to assign"))
		}
	}

	// Blocked beads summary (always show if there are blocked beads)
	blockedCount := 0
	for _, s := range out.Skipped {
		if s.Reason == "blocked_by_dependency" {
			blockedCount++
		}
	}
	if blockedCount > 0 {
		fmt.Println()
		warnStyle := lipgloss.NewStyle().Foreground(th.Warning)
		fmt.Println(warnStyle.Render(fmt.Sprintf("Blocked by dependencies (%d):", blockedCount)))
		for _, s := range out.Skipped {
			if s.Reason == "blocked_by_dependency" {
				if len(s.BlockedByIDs) > 0 {
					fmt.Printf("  - %s (blocked by: %s)\n", s.BeadID, strings.Join(s.BlockedByIDs, ", "))
				} else {
					fmt.Printf("  - %s\n", s.BeadID)
				}
			}
		}
	}

	// Other skipped items (only in verbose mode)
	if verbose && len(out.Skipped) > blockedCount {
		fmt.Println()
		warnStyle := lipgloss.NewStyle().Foreground(th.Warning)
		fmt.Println(warnStyle.Render("Other skipped:"))
		for _, s := range out.Skipped {
			if s.Reason != "blocked_by_dependency" {
				fmt.Printf("  - %s: %s\n", s.BeadID, s.Reason)
			}
		}
	}
}

// executeAssignmentsEnhanced sends assignments to agents and tracks them
func executeAssignmentsEnhanced(session string, out *AssignOutputEnhanced, opts *AssignCommandOptions) error {
	if !opts.Quiet {
		fmt.Println()
		fmt.Println("Executing assignments...")
	}

	// Load or create assignment store
	store, err := assignment.LoadStore(session)
	if err != nil {
		// Continue without store - log warning
		if !opts.Quiet {
			fmt.Printf("  Warning: Could not load assignment store: %v\n", err)
		}
		store = nil
	}

	// Set up file reservation manager if enabled
	var reservationMgr *assign.FileReservationManager
	if opts.ReserveFiles {
		wd, _ := os.Getwd()
		amClient := agentmail.NewClient(agentmail.WithProjectKey(wd))
		if amClient.IsAvailable() {
			reservationMgr = assign.NewFileReservationManager(amClient, wd)
			// Set TTL to 2x timeout, minimum 1 hour
			ttlSeconds := int(opts.Timeout.Seconds()) * 2
			if ttlSeconds < 3600 {
				ttlSeconds = 3600
			}
			reservationMgr.SetTTL(ttlSeconds)
			if !opts.Quiet && opts.Verbose {
				fmt.Println("  File reservation enabled via Agent Mail")
			}
		} else if !opts.Quiet && opts.Verbose {
			fmt.Println("  Warning: Agent Mail not available, skipping file reservations")
		}
	}

	var successCount, failCount, reservedCount int

	for _, item := range out.Assigned {
		// Try to reserve file paths if manager is available
		if reservationMgr != nil {
			ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
			result, reserveErr := reservationMgr.ReserveForBead(
				ctx,
				item.BeadID,
				item.BeadTitle,
				"", // No description available from bv
				item.AgentName,
			)
			cancel()

			if reserveErr != nil && result == nil {
				// Hard error - skip this assignment
				if !opts.Quiet {
					fmt.Printf("  ✗ Failed to reserve files for %s: %v\n", item.BeadID, reserveErr)
				}
				failCount++
				continue
			}
			if result != nil && len(result.Conflicts) > 0 {
				// Conflicts detected - skip this assignment
				if !opts.Quiet {
					conflictPaths := make([]string, len(result.Conflicts))
					for i, c := range result.Conflicts {
						conflictPaths[i] = c.Path
					}
					fmt.Printf("  ⚠ Skipping %s due to file conflicts: %v\n", item.BeadID, conflictPaths)
				}
				failCount++
				continue
			}
			if result != nil && len(result.GrantedPaths) > 0 {
				reservedCount++
				if !opts.Quiet && opts.Verbose {
					fmt.Printf("    Reserved files: %v\n", result.GrantedPaths)
				}
			}
		}

		// Build the prompt using template
		prompt := expandPromptTemplate(item.BeadID, item.BeadTitle, opts.Template, opts.TemplateFile)

		// Send to the pane
		paneID := fmt.Sprintf("%s:%d", session, item.Pane)
		if err := tmux.SendKeys(paneID, prompt, true); err != nil {
			if !opts.Quiet {
				fmt.Printf("  ✗ Failed to assign %s to pane %d: %v\n", item.BeadID, item.Pane, err)
			}
			failCount++
			continue
		}

		item.PromptSent = true
		successCount++

		// Track in assignment store
		if store != nil {
			_, _ = store.Assign(item.BeadID, item.BeadTitle, item.Pane, item.AgentType, item.AgentName, prompt)
		}

		if !opts.Quiet {
			fmt.Printf("  ✓ Assigned %s to pane %d (%s)\n", item.BeadID, item.Pane, item.AgentType)
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

	return nil
}

// expandPromptTemplate expands a prompt template with bead variables
func expandPromptTemplate(beadID, title, templateName, templateFile string) string {
	var template string

	switch strings.ToLower(templateName) {
	case "impl":
		template = "Work on bead {BEAD_ID}: {TITLE}. Check dependencies first with `br dep tree {BEAD_ID}`."
	case "review":
		template = "Review and verify bead {BEAD_ID}: {TITLE}. Run tests if applicable."
	case "custom":
		if templateFile != "" {
			data, err := os.ReadFile(templateFile)
			if err == nil {
				template = string(data)
			} else {
				// Fall back to impl
				template = "Work on bead {BEAD_ID}: {TITLE}. Check dependencies first."
			}
		} else {
			template = "Work on bead {BEAD_ID}: {TITLE}."
		}
	default:
		template = "Work on bead {BEAD_ID}: {TITLE}. Check dependencies first with `br dep tree {BEAD_ID}`."
	}

	// Expand variables
	result := template
	result = strings.ReplaceAll(result, "{BEAD_ID}", beadID)
	result = strings.ReplaceAll(result, "{TITLE}", title)

	return result
}

// ============================================================================
// Clear Assignments - --clear and --clear-pane functionality
// ============================================================================

const (
	clearErrNotAssigned      = "NOT_ASSIGNED"
	clearErrAlreadyCompleted = "ALREADY_COMPLETED"
	clearErrPaneNotFound     = "PANE_NOT_FOUND"
	clearErrInvalidFlag      = "INVALID_FLAG"
	clearErrInternal         = "INTERNAL_ERROR"
)

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
	Code    string `json:"code"`
	Message string `json:"message"`
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

// runClearAssignments handles --clear and --clear-pane operations
func runClearAssignments(cmd *cobra.Command, session string) error {
	if assignClear != "" && assignClearPane >= 0 {
		err := fmt.Errorf("cannot use both --clear and --clear-pane at the same time")
		if IsJSONOutput() {
			return json.NewEncoder(os.Stdout).Encode(output.NewError(err.Error()))
		}
		return err
	}

	if assignClear != "" {
		return runClearSpecificBeads(cmd, session, assignClear)
	}

	if assignClearPane >= 0 {
		return runClearPaneAssignments(cmd, session, assignClearPane)
	}

	err := fmt.Errorf("no clear operation specified")
	if IsJSONOutput() {
		return json.NewEncoder(os.Stdout).Encode(output.NewError(err.Error()))
	}
	return err
}

// runClearSpecificBeads handles --clear flag (clear specific bead assignments)
func runClearSpecificBeads(cmd *cobra.Command, session string, clearBeads string) error {
	beadIDs := strings.Split(clearBeads, ",")
	for i := range beadIDs {
		beadIDs[i] = strings.TrimSpace(beadIDs[i])
	}

	// Load assignment store
	store, err := assignment.LoadStore(session)
	if err != nil {
		err = fmt.Errorf("failed to load assignment store: %w", err)
		if IsJSONOutput() {
			return json.NewEncoder(os.Stdout).Encode(output.NewError(err.Error()))
		}
		return err
	}

	var results []ClearAssignmentResult
	successCount := 0

	for _, beadID := range beadIDs {
		result := ClearAssignmentResult{
			BeadID: beadID,
		}

		// Find the assignment
		assignments := store.GetAll()
		var foundAssignment *assignment.Assignment
		for _, a := range assignments {
			if a.BeadID == beadID && a.Status != "completed" {
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
			result.AssignmentFound = true

			// Release file reservations via Agent Mail
			releasedFiles, releaseErr := releaseFileReservations(session, beadID, foundAssignment.AgentName)
			if releaseErr != nil && assignVerbose {
				fmt.Fprintf(os.Stderr, "[CLEAR] Warning: could not release file reservations for %s: %v\n", beadID, releaseErr)
			}
			result.FilesReleased = releasedFiles

			// Clear the assignment in the store
			store.Remove(beadID)
			result.Success = true
			successCount++
		}

		results = append(results, result)
	}

	// Output results
	if IsJSONOutput() {
		resp := struct {
			output.TimestampedResponse
			Results      []ClearAssignmentResult `json:"results"`
			SuccessCount int                     `json:"success_count"`
			TotalCount   int                     `json:"total_count"`
		}{
			TimestampedResponse: output.NewTimestamped(),
			Results:             results,
			SuccessCount:        successCount,
			TotalCount:          len(beadIDs),
		}
		return json.NewEncoder(os.Stdout).Encode(resp)
	}

	// Text output
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

	return nil
}

// runClearPaneAssignments handles --clear-pane flag (clear all assignments for a pane)
func runClearPaneAssignments(cmd *cobra.Command, session string, pane int) error {
	// Get panes to validate the pane exists
	panes, err := tmux.GetPanes(session)
	if err != nil {
		err = fmt.Errorf("failed to get panes: %w", err)
		if IsJSONOutput() {
			return json.NewEncoder(os.Stdout).Encode(output.NewError(err.Error()))
		}
		return err
	}

	// Find the target pane
	var targetPane *tmux.Pane
	for i := range panes {
		if panes[i].Index == pane {
			targetPane = &panes[i]
			break
		}
	}

	if targetPane == nil {
		err := fmt.Errorf("pane %d not found in session %s", pane, session)
		if IsJSONOutput() {
			return json.NewEncoder(os.Stdout).Encode(output.NewError(err.Error()))
		}
		return err
	}

	agentType := detectAgentTypeFromTitle(targetPane.Title)

	// Load assignment store
	store, err := assignment.LoadStore(session)
	if err != nil {
		err = fmt.Errorf("failed to load assignment store: %w", err)
		if IsJSONOutput() {
			return json.NewEncoder(os.Stdout).Encode(output.NewError(err.Error()))
		}
		return err
	}

	// Find all assignments for this pane
	assignments := store.GetAll()
	var paneAssignments []assignment.Assignment
	for _, a := range assignments {
		if a.Pane == pane && a.Status != "completed" {
			paneAssignments = append(paneAssignments, a)
		}
	}

	result := ClearAllResult{
		Pane:      pane,
		AgentType: agentType,
		Success:   true,
	}

	// Clear each assignment
	for _, a := range paneAssignments {
		beadResult := ClearAssignmentResult{
			BeadID:            a.BeadID,
			PreviousPane:      a.Pane,
			PreviousAgent:     a.AgentName,
			PreviousAgentType: a.AgentType,
			AssignmentFound:   true,
		}

		// Release file reservations
		releasedFiles, releaseErr := releaseFileReservations(session, a.BeadID, a.AgentName)
		if releaseErr != nil && assignVerbose {
			fmt.Fprintf(os.Stderr, "[CLEAR] Warning: could not release file reservations for %s: %v\n", a.BeadID, releaseErr)
		}
		beadResult.FilesReleased = releasedFiles

		// Clear the assignment
		store.Remove(a.BeadID)
		beadResult.Success = true

		result.ClearedBeads = append(result.ClearedBeads, beadResult)
	}

	if len(paneAssignments) == 0 {
		result.Error = "no active assignments found for this pane"
	}

	// Output result
	if IsJSONOutput() {
		resp := struct {
			output.TimestampedResponse
			*ClearAllResult
		}{
			TimestampedResponse: output.NewTimestamped(),
			ClearAllResult:      &result,
		}
		return json.NewEncoder(os.Stdout).Encode(resp)
	}

	// Text output
	if !assignQuiet {
		if len(paneAssignments) == 0 {
			fmt.Printf("No active assignments found for pane %d (%s)\n", pane, agentType)
		} else {
			fmt.Printf("Cleared all assignments for pane %d (%s):\n\n", pane, agentType)
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

	return nil
}

// releaseFileReservationsWithIDs releases file reservations using stored reservation IDs
func releaseFileReservationsWithIDs(session, beadID, agentName string, reservationIDs []int) ([]string, error) {
	// Get project key
	projectKey, _ := os.Getwd()

	// Create Agent Mail client
	amClient := agentmail.NewClient(agentmail.WithProjectKey(projectKey))
	if !amClient.IsAvailable() {
		return nil, nil // No error if Agent Mail isn't available
	}

	// Create a reservation manager
	manager := assign.NewFileReservationManager(amClient, projectKey)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Release reservations by IDs
	if len(reservationIDs) > 0 {
		if err := manager.ReleaseForBead(ctx, agentName, reservationIDs); err != nil {
			return nil, fmt.Errorf("failed to release reservations: %w", err)
		}
		if assignVerbose {
			fmt.Fprintf(os.Stderr, "[RESERVE] Released %d reservations for %s\n", len(reservationIDs), beadID)
		}
		// Return the count as a string since we don't have the actual paths
		return []string{fmt.Sprintf("%d reservations", len(reservationIDs))}, nil
	}

	return nil, nil
}

// releaseFileReservations releases file reservations for a bead via Agent Mail
// This is used when we don't have reservation IDs stored
func releaseFileReservations(session, beadID, agentName string) ([]string, error) {
	// Get project key
	projectKey, _ := os.Getwd()

	// Create Agent Mail client
	amClient := agentmail.NewClient(agentmail.WithProjectKey(projectKey))
	if !amClient.IsAvailable() {
		return nil, nil // No error if Agent Mail isn't available
	}

	// Get bead details to extract file paths
	beadTitle := getBeadTitle(beadID)
	if beadTitle == "" {
		if assignVerbose {
			fmt.Fprintf(os.Stderr, "[RESERVE] No bead title found for %s, cannot determine paths to release\n", beadID)
		}
		return nil, nil
	}

	// Extract file paths that would have been reserved
	paths := assign.ExtractFilePaths(beadTitle, "")
	if len(paths) == 0 {
		if assignVerbose {
			fmt.Fprintf(os.Stderr, "[RESERVE] No file paths found in bead %s title, nothing to release\n", beadID)
		}
		return nil, nil
	}

	// Create a reservation manager
	manager := assign.NewFileReservationManager(amClient, projectKey)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Release reservations by paths
	if err := manager.ReleaseByPaths(ctx, agentName, paths); err != nil {
		return nil, fmt.Errorf("failed to release reservations by paths: %w", err)
	}

	if assignVerbose {
		fmt.Fprintf(os.Stderr, "[RESERVE] Released reservations for paths: %v (bead: %s)\n", paths, beadID)
	}

	return paths, nil
}

// ============================================================================
// Dependency Awareness - Completion Detection and Auto-Reassignment
// ============================================================================

// UnblockedBead represents a bead that was previously blocked but is now actionable
type UnblockedBead struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Priority      int      `json:"priority"`
	PrevBlockers  []string `json:"previous_blockers"`
	UnblockedByID string   `json:"unblocked_by_id"` // The blocker that was completed
}

// DependencyAwareResult contains the result of an unblock check
type DependencyAwareResult struct {
	CompletedBeadID string          `json:"completed_bead_id"`
	NewlyUnblocked  []UnblockedBead `json:"newly_unblocked"`
	CyclesDetected  [][]string      `json:"cycles_detected,omitempty"`
	Errors          []string        `json:"errors,omitempty"`
}

// GetNewlyUnblockedBeads checks what beads are now unblocked after a bead completion.
// This is the core function for dependency-aware reassignment.
// It refreshes the dependency graph from bv and identifies beads that:
// 1. Were previously blocked by the completed bead
// 2. Have no remaining blockers (all their dependencies are now completed)
func GetNewlyUnblockedBeads(completedBeadID string, verbose bool) (*DependencyAwareResult, error) {
	result := &DependencyAwareResult{
		CompletedBeadID: completedBeadID,
		NewlyUnblocked:  make([]UnblockedBead, 0),
	}

	wd, _ := os.Getwd()

	// Force refresh the dependency graph by fetching fresh triage data
	if verbose {
		fmt.Fprintf(os.Stderr, "[DEP] Checking for beads unblocked by %s\n", completedBeadID)
	}

	// Get current triage recommendations (fresh data) with retry logic
	var recommendations []bv.TriageRecommendation
	maxRetries := 3
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		recommendations, lastErr = bv.GetTriageRecommendations(wd, 100)
		if lastErr == nil {
			break
		}

		if verbose && attempt < maxRetries-1 {
			fmt.Fprintf(os.Stderr, "[DEP] BV triage failed (attempt %d/%d): %v, retrying...\n",
				attempt+1, maxRetries, lastErr)
		}

		// Brief delay before retry
		time.Sleep(time.Duration(attempt+1) * time.Second)
	}

	if lastErr != nil {
		// All retries failed - try alternative approaches
		errMsg := fmt.Sprintf("failed to get triage data after %d attempts: %v", maxRetries, lastErr)
		result.Errors = append(result.Errors, errMsg)

		if verbose {
			fmt.Fprintf(os.Stderr, "[DEP] %s\n", errMsg)
			fmt.Fprintf(os.Stderr, "[DEP] Attempting alternative dependency detection...\n")
		}

		// Alternative: Check if we can at least validate that the completed bead exists
		// and try to infer potential unblocks from available data
		fallbackBeads := bv.GetReadyPreview(wd, 50)
		if len(fallbackBeads) > 0 {
			if verbose {
				fmt.Fprintf(os.Stderr, "[DEP] Found %d ready beads, but cannot determine dependencies\n", len(fallbackBeads))
			}
			result.Errors = append(result.Errors, "dependency information unavailable - manual verification recommended")
		}

		return result, nil // Return partial result, not error
	}

	if len(recommendations) == 0 {
		if verbose {
			fmt.Fprintf(os.Stderr, "[DEP] No triage recommendations returned - dependency graph may be empty\n")
		}
		result.Errors = append(result.Errors, "no beads found in triage - dependency graph may be empty or stale")
		return result, nil
	}

	// Validate dependency graph freshness by checking if completed bead is known
	// This helps detect stale graphs where the completion hasn't been processed yet
	foundCompletedBead := false
	for _, rec := range recommendations {
		if rec.ID == completedBeadID {
			foundCompletedBead = true
			if len(rec.BlockedBy) > 0 {
				// The completed bead still shows as blocked - graph is stale
				if verbose {
					fmt.Fprintf(os.Stderr, "[DEP] Warning: completed bead %s still shows blockers: %v (stale graph)\n",
						completedBeadID, rec.BlockedBy)
				}
				result.Errors = append(result.Errors,
					fmt.Sprintf("stale dependency graph detected - %s still shows as blocked", completedBeadID))
			}
			break
		}
	}

	if !foundCompletedBead {
		if verbose {
			fmt.Fprintf(os.Stderr, "[DEP] Completed bead %s not found in triage recommendations\n", completedBeadID)
		}
		// This could be normal if the bead was already processed, so just note it
		result.Errors = append(result.Errors,
			fmt.Sprintf("completed bead %s not in current triage (may be normal)", completedBeadID))
	}

	// Find beads that were blocked by the completed bead and are now actionable
	for _, rec := range recommendations {
		// Check if this bead was blocked by the completed bead
		wasBlockedByCompleted := false
		for _, blockerID := range rec.BlockedBy {
			if blockerID == completedBeadID {
				wasBlockedByCompleted = true
				break
			}
		}

		if !wasBlockedByCompleted {
			continue
		}

		// Check if all blockers are now resolved (only the completed one remained)
		// Note: BlockedBy in current triage shows current blockers
		// If the bead still has blockers, it's not unblocked yet
		if len(rec.BlockedBy) > 0 {
			// Still blocked by other beads
			if verbose {
				otherBlockers := make([]string, 0)
				for _, b := range rec.BlockedBy {
					if b != completedBeadID {
						otherBlockers = append(otherBlockers, b)
					}
				}
				if len(otherBlockers) > 0 {
					fmt.Fprintf(os.Stderr, "[DEP] %s still blocked by: %v\n", rec.ID, otherBlockers)
				}
			}
			continue
		}

		// This bead is now unblocked!
		result.NewlyUnblocked = append(result.NewlyUnblocked, UnblockedBead{
			ID:            rec.ID,
			Title:         rec.Title,
			Priority:      rec.Priority,
			PrevBlockers:  []string{completedBeadID}, // We know it was blocked by this
			UnblockedByID: completedBeadID,
		})

		if verbose {
			fmt.Fprintf(os.Stderr, "[UNBLOCK] %s now ready (was blocked by %s)\n", rec.ID, completedBeadID)
		}
	}

	// Check for cycles using BV insights
	client := bv.NewBVClient()
	client.WorkspacePath = wd
	insights, err := client.GetInsights()
	if err == nil && insights != nil && len(insights.Cycles) > 0 {
		result.CyclesDetected = insights.Cycles
		if verbose {
			fmt.Fprintf(os.Stderr, "[DEP] Warning: %d dependency cycles detected\n", len(insights.Cycles))
		}
	}

	return result, nil
}

// OnBeadCompletion is called when an assigned bead is marked as completed.
// It performs the unblock check and returns beads ready for assignment.
// This is designed to be called from:
// - Assignment store status update (when bead marked completed)
// - Watch mode polling
// - Manual completion notification
func OnBeadCompletion(session string, completedBeadID string, verbose bool) ([]bv.BeadPreview, error) {
	result, err := GetNewlyUnblockedBeads(completedBeadID, verbose)
	if err != nil {
		return nil, err
	}

	// Convert unblocked beads to BeadPreview for assignment
	var readyBeads []bv.BeadPreview
	for _, unblocked := range result.NewlyUnblocked {
		readyBeads = append(readyBeads, bv.BeadPreview{
			ID:       unblocked.ID,
			Title:    unblocked.Title,
			Priority: fmt.Sprintf("P%d", unblocked.Priority),
		})
	}

	return readyBeads, nil
}

// CheckCycles returns any dependency cycles detected in the current project.
// Beads in cycles should be excluded from automatic assignment.
// Enhanced with retry logic and comprehensive error handling.
func CheckCycles(verbose bool) ([][]string, error) {
	wd, _ := os.Getwd()
	client := bv.NewBVClient()
	client.WorkspacePath = wd

	maxRetries := 2
	var insights *bv.Insights
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		insights, lastErr = client.GetInsights()
		if lastErr == nil {
			break
		}

		if verbose && attempt < maxRetries-1 {
			fmt.Fprintf(os.Stderr, "[DEP] BV insights failed (attempt %d/%d): %v, retrying...\n",
				attempt+1, maxRetries, lastErr)
		}

		// Brief delay before retry
		time.Sleep(500 * time.Millisecond * time.Duration(attempt+1))
	}

	if lastErr != nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "[DEP] Failed to get insights after %d attempts: %v\n", maxRetries, lastErr)
			fmt.Fprintf(os.Stderr, "[DEP] Cycle detection unavailable - proceeding without cycle filtering\n")
		}
		return nil, fmt.Errorf("failed to get insights after %d attempts: %w", maxRetries, lastErr)
	}

	if insights == nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "[DEP] BV insights returned nil - no cycle information available\n")
		}
		return nil, nil
	}

	// Validate cycle data integrity
	var validCycles [][]string
	for i, cycle := range insights.Cycles {
		if len(cycle) < 2 {
			if verbose {
				fmt.Fprintf(os.Stderr, "[DEP] Warning: invalid cycle %d with < 2 nodes: %v\n", i+1, cycle)
			}
			continue
		}

		// Check for duplicate nodes in the cycle (indicates data corruption)
		seen := make(map[string]bool)
		valid := true
		for _, node := range cycle {
			if seen[node] {
				if verbose {
					fmt.Fprintf(os.Stderr, "[DEP] Warning: cycle %d has duplicate node %s: %v\n", i+1, node, cycle)
				}
				valid = false
				break
			}
			seen[node] = true
		}

		if valid {
			validCycles = append(validCycles, cycle)
		}
	}

	if verbose && len(validCycles) > 0 {
		fmt.Fprintf(os.Stderr, "[DEP] Detected %d valid dependency cycles:\n", len(validCycles))
		for i, cycle := range validCycles {
			fmt.Fprintf(os.Stderr, "  Cycle %d: %v\n", i+1, cycle)
		}
	}

	if len(validCycles) != len(insights.Cycles) && verbose {
		fmt.Fprintf(os.Stderr, "[DEP] Filtered out %d invalid cycles\n",
			len(insights.Cycles)-len(validCycles))
	}

	return validCycles, nil
}

// IsBeadInCycle checks if a bead ID is part of any detected dependency cycle.
func IsBeadInCycle(beadID string, cycles [][]string) bool {
	for _, cycle := range cycles {
		for _, id := range cycle {
			if id == beadID {
				return true
			}
		}
	}
	return false
}

// FilterCyclicBeads removes beads that are part of dependency cycles from the list.
func FilterCyclicBeads(beads []bv.BeadPreview, verbose bool) ([]bv.BeadPreview, int) {
	cycles, err := CheckCycles(false) // Don't log twice if verbose
	if err != nil || len(cycles) == 0 {
		return beads, 0
	}

	var filtered []bv.BeadPreview
	excluded := 0

	for _, bead := range beads {
		if IsBeadInCycle(bead.ID, cycles) {
			excluded++
			if verbose {
				fmt.Fprintf(os.Stderr, "[DEP] Excluding %s from assignment (in dependency cycle)\n", bead.ID)
			}
			continue
		}
		filtered = append(filtered, bead)
	}

	return filtered, excluded
}

// ============================================================================
// Direct Pane Assignment - ntm assign --pane
// ============================================================================

// DirectAssignResult is the result of a direct pane assignment
type DirectAssignResult struct {
	BeadID         string                        `json:"bead_id"`
	BeadTitle      string                        `json:"bead_title,omitempty"`
	Pane           int                           `json:"pane"`
	AgentType      string                        `json:"agent_type"`
	AgentName      string                        `json:"agent_name,omitempty"`
	PromptSent     string                        `json:"prompt_sent"`
	Success        bool                          `json:"success"`
	Error          string                        `json:"error,omitempty"`
	Reservations   *assign.FileReservationResult `json:"reservations,omitempty"`
	PaneWasBusy    bool                          `json:"pane_was_busy,omitempty"`
	DepsIgnored    bool                          `json:"deps_ignored,omitempty"`
	BlockedByBeads []string                      `json:"blocked_by_beads,omitempty"`
}

// runDirectPaneAssignment handles the --pane flag for direct bead-to-pane assignment
func runDirectPaneAssignment(cmd *cobra.Command, opts *AssignCommandOptions) error {
	// Validate: exactly one bead must be specified
	if len(opts.BeadIDs) != 1 {
		err := fmt.Errorf("--pane requires exactly one bead (use --beads=bd-xxx)")
		if IsJSONOutput() {
			return json.NewEncoder(os.Stdout).Encode(output.NewError(err.Error()))
		}
		return err
	}

	beadID := opts.BeadIDs[0]

	// Get panes from tmux
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		err = fmt.Errorf("failed to get panes: %w", err)
		if IsJSONOutput() {
			return json.NewEncoder(os.Stdout).Encode(output.NewError(err.Error()))
		}
		return err
	}

	// Find the target pane
	var targetPane *tmux.Pane
	for i := range panes {
		if panes[i].Index == opts.Pane {
			targetPane = &panes[i]
			break
		}
	}

	if targetPane == nil {
		err = fmt.Errorf("pane %d not found in session %s", opts.Pane, opts.Session)
		if IsJSONOutput() {
			return json.NewEncoder(os.Stdout).Encode(output.NewError(err.Error()))
		}
		return err
	}

	// Detect agent type and state
	agentType := detectAgentTypeFromTitle(targetPane.Title)
	if agentType == "user" || agentType == "unknown" {
		err = fmt.Errorf("pane %d is not an agent pane (type: %s)", opts.Pane, agentType)
		if IsJSONOutput() {
			return json.NewEncoder(os.Stdout).Encode(output.NewError(err.Error()))
		}
		return err
	}

	scrollback, _ := tmux.CapturePaneOutput(targetPane.ID, 10)
	state := determineAgentState(scrollback, agentType)

	result := &DirectAssignResult{
		BeadID:    beadID,
		Pane:      opts.Pane,
		AgentType: agentType,
	}

	// Check if pane is busy (unless --force)
	if state != "idle" && !opts.Force {
		result.Success = false
		result.Error = fmt.Sprintf("pane %d is busy (state: %s), use --force to override", opts.Pane, state)
		result.PaneWasBusy = true

		if IsJSONOutput() {
			return json.NewEncoder(os.Stdout).Encode(struct {
				output.TimestampedResponse
				*DirectAssignResult
			}{
				TimestampedResponse: output.NewTimestamped(),
				DirectAssignResult:  result,
			})
		}
		return fmt.Errorf("%s", result.Error)
	}
	result.PaneWasBusy = state != "idle"

	// Check dependencies (unless --ignore-deps)
	if !opts.IgnoreDeps {
		blockers, err := getBeadBlockers(beadID)
		if err != nil && opts.Verbose {
			fmt.Fprintf(os.Stderr, "[DEP] Warning: could not check dependencies: %v\n", err)
		}
		if len(blockers) > 0 {
			result.Success = false
			result.Error = fmt.Sprintf("bead %s is blocked by: %v, use --ignore-deps to override", beadID, blockers)
			result.BlockedByBeads = blockers

			if IsJSONOutput() {
				return json.NewEncoder(os.Stdout).Encode(struct {
					output.TimestampedResponse
					*DirectAssignResult
				}{
					TimestampedResponse: output.NewTimestamped(),
					DirectAssignResult:  result,
				})
			}
			return fmt.Errorf("%s", result.Error)
		}
	} else {
		result.DepsIgnored = true
	}

	// Get bead title
	beadTitle := getBeadTitle(beadID)
	result.BeadTitle = beadTitle

	// Reserve files via Agent Mail (if enabled)
	if opts.ReserveFiles {
		reservationResult := reserveFilesForBead(opts.Session, beadID, beadTitle, agentType, opts.Verbose)
		result.Reservations = reservationResult
	}

	// Build prompt
	var prompt string
	if opts.Prompt != "" {
		prompt = opts.Prompt
	} else {
		prompt = expandPromptTemplate(beadID, beadTitle, opts.Template, opts.TemplateFile)
	}
	result.PromptSent = prompt

	// Execute the assignment
	paneID := fmt.Sprintf("%s:%d", opts.Session, opts.Pane)
	if err := tmux.SendKeys(paneID, prompt, true); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("failed to send prompt: %v", err)

		if IsJSONOutput() {
			return json.NewEncoder(os.Stdout).Encode(struct {
				output.TimestampedResponse
				*DirectAssignResult
			}{
				TimestampedResponse: output.NewTimestamped(),
				DirectAssignResult:  result,
			})
		}
		return err
	}

	result.Success = true

	// Track in assignment store
	store, err := assignment.LoadStore(opts.Session)
	if err == nil && store != nil {
		_, _ = store.Assign(beadID, beadTitle, opts.Pane, agentType, "", prompt)
	}

	// Output result
	if IsJSONOutput() {
		return json.NewEncoder(os.Stdout).Encode(struct {
			output.TimestampedResponse
			*DirectAssignResult
		}{
			TimestampedResponse: output.NewTimestamped(),
			DirectAssignResult:  result,
		})
	}

	// Text output
	if !opts.Quiet {
		fmt.Printf("✓ Assigned %s to pane %d (%s)\n", beadID, opts.Pane, agentType)
		if beadTitle != "" {
			fmt.Printf("  Title: %s\n", beadTitle)
		}
		if result.PaneWasBusy {
			fmt.Printf("  Note: Pane was busy (--force used)\n")
		}
		if result.DepsIgnored {
			fmt.Printf("  Note: Dependencies ignored (--ignore-deps used)\n")
		}
		if result.Reservations != nil && len(result.Reservations.GrantedPaths) > 0 {
			fmt.Printf("  Reserved: %v\n", result.Reservations.GrantedPaths)
		}
		fmt.Printf("  Prompt: %s\n", prompt)
	}

	return nil
}

// getBeadBlockers returns the list of beads blocking the given bead
func getBeadBlockers(beadID string) ([]string, error) {
	wd, _ := os.Getwd()
	recommendations, err := bv.GetTriageRecommendations(wd, 100)
	if err != nil {
		return nil, err
	}

	for _, rec := range recommendations {
		if rec.ID == beadID {
			return rec.BlockedBy, nil
		}
	}

	return nil, nil
}

// getBeadTitle retrieves the title for a bead
func getBeadTitle(beadID string) string {
	wd, _ := os.Getwd()
	recommendations, err := bv.GetTriageRecommendations(wd, 100)
	if err != nil {
		return ""
	}

	for _, rec := range recommendations {
		if rec.ID == beadID {
			return rec.Title
		}
	}

	// Fallback to ready preview
	readyBeads := bv.GetReadyPreview(wd, 50)
	for _, b := range readyBeads {
		if b.ID == beadID {
			return b.Title
		}
	}

	return ""
}

// reserveFilesForBead reserves files mentioned in a bead for an agent
func reserveFilesForBead(session, beadID, beadTitle, agentType string, verbose bool) *assign.FileReservationResult {
	// Get project key (use working directory)
	projectKey, _ := os.Getwd()

	// Create agent name from session and agent type
	agentName := fmt.Sprintf("%s_%s", session, agentType)

	// Create reservation manager
	manager := assign.NewFileReservationManager(nil, projectKey)

	// Attempt reservation (will return result even without client)
	result, err := manager.ReserveForBead(context.Background(), beadID, beadTitle, "", agentName)
	if err != nil && verbose {
		fmt.Fprintf(os.Stderr, "[RESERVE] Warning: %v\n", err)
	}

	return result
}

// ============================================================================
// Auto-Reassignment Logic
// ============================================================================

// AutoReassignOptions contains options for auto-reassignment
type AutoReassignOptions struct {
	Session         string
	Strategy        string
	Template        string
	TemplateFile    string
	ReserveFiles    bool
	Verbose         bool
	Quiet           bool
	Timeout         time.Duration
	AgentTypeFilter string
}

// AutoReassignResult contains the result of an auto-reassignment operation
type AutoReassignResult struct {
	TriggerBeadID  string          `json:"trigger_bead_id"`
	NewlyUnblocked []UnblockedBead `json:"newly_unblocked"`
	Assigned       []AssignedItem  `json:"assigned"`
	Skipped        []SkippedItem   `json:"skipped"`
	IdleAgents     int             `json:"idle_agents"`
	Errors         []string        `json:"errors,omitempty"`
	CyclesDetected [][]string      `json:"cycles_detected,omitempty"`
	CompletionTime time.Time       `json:"completion_time"`
}

// PerformAutoReassignment handles automatic reassignment when a bead completes.
// This is the main entry point for dependency-aware auto-reassignment.
// It:
// 1. Detects newly unblocked beads after the completion
// 2. Finds idle agents that can take new work
// 3. Assigns unblocked beads to idle agents using the specified strategy
// 4. Handles file reservations and prompt generation
func PerformAutoReassignment(completedBeadID string, opts *AutoReassignOptions) (*AutoReassignResult, error) {
	result := &AutoReassignResult{
		TriggerBeadID:  completedBeadID,
		CompletionTime: time.Now(),
		Assigned:       make([]AssignedItem, 0),
		Skipped:        make([]SkippedItem, 0),
	}

	if opts.Verbose {
		fmt.Fprintf(os.Stderr, "[AUTO] Auto-reassignment triggered by completion of %s\n", completedBeadID)
	}

	// Step 1: Get newly unblocked beads
	depResult, err := GetNewlyUnblockedBeads(completedBeadID, opts.Verbose)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("failed to check unblocked beads: %v", err))
		return result, nil // Return partial result, not error
	}

	result.NewlyUnblocked = depResult.NewlyUnblocked
	result.CyclesDetected = depResult.CyclesDetected

	if len(depResult.Errors) > 0 {
		result.Errors = append(result.Errors, depResult.Errors...)
	}

	if len(result.NewlyUnblocked) == 0 {
		if opts.Verbose {
			fmt.Fprintf(os.Stderr, "[AUTO] No beads were unblocked by completion of %s\n", completedBeadID)
		}
		return result, nil
	}

	if opts.Verbose {
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

	// Step 2: Get idle agents
	idleAgents, err := getIdleAgents(opts.Session, opts.AgentTypeFilter, opts.Verbose)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("failed to get idle agents: %v", err))
		return result, nil
	}

	result.IdleAgents = len(idleAgents)

	if len(idleAgents) == 0 {
		// No idle agents - mark all unblocked beads as skipped
		for _, unblocked := range result.NewlyUnblocked {
			result.Skipped = append(result.Skipped, SkippedItem{
				BeadID: unblocked.ID,
				Reason: "no_idle_agents",
			})
		}
		if opts.Verbose {
			fmt.Fprintf(os.Stderr, "[AUTO] No idle agents available for reassignment\n")
		}
		return result, nil
	}

	// Step 3: Convert unblocked beads to BeadPreview format for assignment
	var unblockedBeads []bv.BeadPreview
	for _, unblocked := range result.NewlyUnblocked {
		unblockedBeads = append(unblockedBeads, bv.BeadPreview{
			ID:       unblocked.ID,
			Title:    unblocked.Title,
			Priority: fmt.Sprintf("P%d", unblocked.Priority),
		})
	}

	// Step 4: Filter out beads in dependency cycles
	filteredBeads, excluded := FilterCyclicBeads(unblockedBeads, opts.Verbose)
	for i := 0; i < excluded; i++ {
		// Find the excluded bead to add to skipped
		for _, unblocked := range result.NewlyUnblocked {
			if IsBeadInCycle(unblocked.ID, result.CyclesDetected) {
				result.Skipped = append(result.Skipped, SkippedItem{
					BeadID: unblocked.ID,
					Reason: "in_dependency_cycle",
				})
				break
			}
		}
	}

	if len(filteredBeads) == 0 {
		if opts.Verbose {
			fmt.Fprintf(os.Stderr, "[AUTO] No assignable beads after filtering cycles\n")
		}
		return result, nil
	}

	// Step 5: Generate assignments using strategy
	assignOpts := &AssignCommandOptions{
		Session:         opts.Session,
		Strategy:        opts.Strategy,
		Template:        opts.Template,
		TemplateFile:    opts.TemplateFile,
		AgentTypeFilter: opts.AgentTypeFilter,
		Verbose:         opts.Verbose,
		Quiet:           opts.Quiet,
		Timeout:         opts.Timeout,
		ReserveFiles:    opts.ReserveFiles,
	}

	assignments := generateAssignmentsEnhanced(idleAgents, filteredBeads, assignOpts)
	result.Assigned = assignments

	// Step 6: Execute assignments
	if len(assignments) > 0 {
		// Create a mock enhanced output for execution
		enhancedOut := &AssignOutputEnhanced{
			Strategy: opts.Strategy,
			Assigned: assignments,
			Skipped:  result.Skipped,
		}

		if err := executeAssignmentsEnhanced(opts.Session, enhancedOut, assignOpts); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("failed to execute assignments: %v", err))
		} else {
			if opts.Verbose {
				fmt.Fprintf(os.Stderr, "[AUTO] Successfully assigned %d unblocked beads\n", len(assignments))
			}
		}
	}

	return result, nil
}

// getIdleAgents returns a list of idle agents that can take new assignments
func getIdleAgents(session, agentTypeFilter string, verbose bool) ([]assignAgentInfo, error) {
	// Get panes from tmux
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return nil, fmt.Errorf("failed to get panes: %w", err)
	}

	var idleAgents []assignAgentInfo

	for _, pane := range panes {
		agentType := detectAgentTypeFromTitle(pane.Title)
		if agentType == "user" || agentType == "unknown" {
			continue
		}

		// Apply agent type filter
		if agentTypeFilter != "" && agentType != agentTypeFilter {
			continue
		}

		model := detectModelFromTitle(agentType, pane.Title)
		scrollback, _ := tmux.CapturePaneOutput(pane.ID, 10)
		state := determineAgentState(scrollback, agentType)

		if state == "idle" {
			idleAgents = append(idleAgents, assignAgentInfo{
				pane:      pane,
				agentType: agentType,
				model:     model,
				state:     state,
			})
			if verbose {
				fmt.Fprintf(os.Stderr, "[AUTO] Found idle agent: pane %d (%s)\n", pane.Index, agentType)
			}
		}
	}

	if verbose {
		fmt.Fprintf(os.Stderr, "[AUTO] Total idle agents available: %d\n", len(idleAgents))
	}

	return idleAgents, nil
}

// WatchForCompletions starts a background watcher that monitors for bead completions
// and triggers auto-reassignment. This implements the active monitoring component.
func WatchForCompletions(opts *AutoReassignOptions) error {
	if opts.Verbose {
		fmt.Fprintf(os.Stderr, "[WATCH] Starting completion watcher for session %s\n", opts.Session)
	}

	// Load assignment store to monitor completions
	store, err := assignment.LoadStore(opts.Session)
	if err != nil {
		return fmt.Errorf("failed to load assignment store: %w", err)
	}

	// Get initial state of assignments
	lastCheckTime := time.Now()
	knownAssignments := make(map[string]assignment.Assignment)
	for _, a := range store.GetAll() {
		knownAssignments[a.BeadID] = a
	}

	// Watch loop
	ticker := time.NewTicker(30 * time.Second) // Check every 30 seconds
	defer ticker.Stop()

	for range ticker.C {
		// Check for completed assignments
		currentAssignments := store.GetAll()
		for _, current := range currentAssignments {
			if prev, existed := knownAssignments[current.BeadID]; existed {
				// Check if status changed from non-completed to completed
				if prev.Status != "completed" && current.Status == "completed" {
					if opts.Verbose {
						fmt.Fprintf(os.Stderr, "[WATCH] Detected completion: %s\n", current.BeadID)
					}

					// Trigger auto-reassignment
					result, err := PerformAutoReassignment(current.BeadID, opts)
					if err != nil {
						fmt.Fprintf(os.Stderr, "[WATCH] Auto-reassignment failed for %s: %v\n", current.BeadID, err)
					} else if len(result.Assigned) > 0 {
						if !opts.Quiet {
							fmt.Printf("[AUTO] Assigned %d newly unblocked beads after %s completion\n",
								len(result.Assigned), current.BeadID)
						}
					}
				}
			}
			knownAssignments[current.BeadID] = current
		}

		lastCheckTime = time.Now()
	}

	return nil
}

// TriggerCompletionCheck manually triggers a completion check and auto-reassignment.
// This can be called from external completion notifications or manual triggers.
func TriggerCompletionCheck(session, completedBeadID string, opts *AutoReassignOptions) (*AutoReassignResult, error) {
	if opts.Session == "" {
		opts.Session = session
	}

	result, err := PerformAutoReassignment(completedBeadID, opts)
	if err != nil {
		return result, err
	}

	// Update assignment store to mark the bead as completed if it's tracked
	store, err := assignment.LoadStore(session)
	if err == nil && store != nil {
		assignments := store.GetAll()
		for _, a := range assignments {
			if a.BeadID == completedBeadID && a.Status != "completed" {
				store.UpdateStatus(completedBeadID, "completed")
				if opts.Verbose {
					fmt.Fprintf(os.Stderr, "[AUTO] Marked %s as completed in assignment store\n", completedBeadID)
				}
				break
			}
		}
	}

	return result, nil
}

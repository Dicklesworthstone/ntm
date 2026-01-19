package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/assign"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

var (
	assignAuto           bool
	assignStrategy       string
	assignBeads          string
	assignLimit          int
	assignAgentType      string // Filter by agent type
	assignCCOnly         bool   // Alias for --agent=claude
	assignCodOnly        bool   // Alias for --agent=codex
	assignGmiOnly        bool   // Alias for --agent=gemini
	assignTemplate       string // Prompt template: impl, review, custom
	assignTemplateFile   string // Custom template file path
	assignVerbose        bool
	assignQuiet          bool
	assignTimeout        time.Duration
	assignDryRun         bool // Alias for no --auto
	assignReserveFiles   bool // Enable Agent Mail file reservations
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
  ntm assign myproject --dry-run               # Preview without executing`,
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

	// Check if bv is available
	if !bv.IsInstalled() {
		return fmt.Errorf("bv is not installed - required for work assignment")
	}

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
}

// AssignOutputEnhanced is the enhanced output structure matching the spec
type AssignOutputEnhanced struct {
	Strategy string                       `json:"strategy"`
	Assigned []AssignedItem               `json:"assigned"`
	Skipped  []SkippedItem                `json:"skipped"`
	Summary  AssignSummaryEnhanced        `json:"summary"`
	Errors   []string                     `json:"errors,omitempty"`
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
	ActionableC   int `json:"actionable"`   // Beads with no blockers
	BlockedCount  int `json:"blocked"`      // Beads blocked by dependencies
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
		TimestampedResponse: output.NewTimestamped(),
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
	var agents []assignAgentInfo
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

		ai := assignAgentInfo{
			pane:      pane,
			agentType: at,
			model:     model,
			state:     state,
		}
		agents = append(agents, ai)

		if state == "idle" {
			idleAgents = append(idleAgents, ai)
		}
	}

	// Get beads from bv using triage recommendations for dependency awareness
	wd, _ := os.Getwd()
	allRecs, err := bv.GetTriageRecommendations(wd, 100)

	// Fallback to GetReadyPreview if triage fails
	var readyBeads []bv.BeadPreview
	var blockedBeads []SkippedItem
	if err != nil {
		// Fallback: use GetReadyPreview (no dependency info)
		if opts.Verbose {
			fmt.Fprintf(os.Stderr, "[DEP] BV triage unavailable (%v), using br list fallback\n", err)
		}
		readyBeads = bv.GetReadyPreview(wd, 50)
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

	var successCount, failCount int

	for _, item := range out.Assigned {
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
// Dependency Awareness - Completion Detection and Unblock Checking
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

	// Get current triage recommendations (fresh data)
	recommendations, err := bv.GetTriageRecommendations(wd, 100)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("failed to get triage data: %v", err))
		return result, nil // Return partial result, not error
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
func CheckCycles(verbose bool) ([][]string, error) {
	wd, _ := os.Getwd()
	client := bv.NewBVClient()
	client.WorkspacePath = wd

	insights, err := client.GetInsights()
	if err != nil {
		return nil, fmt.Errorf("failed to get insights: %w", err)
	}

	if insights == nil {
		return nil, nil
	}

	if verbose && len(insights.Cycles) > 0 {
		fmt.Fprintf(os.Stderr, "[DEP] Detected %d dependency cycles:\n", len(insights.Cycles))
		for i, cycle := range insights.Cycles {
			fmt.Fprintf(os.Stderr, "  Cycle %d: %v\n", i+1, cycle)
		}
	}

	return insights.Cycles, nil
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

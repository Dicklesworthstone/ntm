package robot

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assign"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// AssignOptions configures work assignment analysis
type AssignOptions struct {
	Session  string   // tmux session name
	Beads    []string // Specific bead IDs to assign (empty = all ready)
	Strategy string   // balanced, speed, quality, dependency
}

// AssignOutput is the structured output for --robot-assign
type AssignOutput struct {
	RobotResponse
	Session           string             `json:"session"`
	Strategy          string             `json:"strategy"`
	GeneratedAt       time.Time          `json:"generated_at"`
	Recommendations   []AssignRecommend  `json:"recommendations"`
	BlockedBeads      []BlockedBead      `json:"blocked_beads"`
	IdleAgents        []string           `json:"idle_agents"`
	UnassignableBeads []UnassignableBead `json:"unassignable_beads,omitempty"`
	Summary           AssignSummary      `json:"summary"`
	AgentHints        *AssignAgentHints  `json:"_agent_hints,omitempty"`
}

// AssignRecommend is a single assignment recommendation
type AssignRecommend struct {
	Agent      string  `json:"agent"`      // Pane index (e.g., "1")
	AgentType  string  `json:"agent_type"` // claude, codex, gemini
	Model      string  `json:"model,omitempty"`
	AssignBead string  `json:"assign_bead"` // Bead ID to assign
	BeadTitle  string  `json:"bead_title"`
	Priority   string  `json:"priority"`   // P0-P4
	Confidence float64 `json:"confidence"` // 0.0-1.0
	Reasoning  string  `json:"reasoning"`
}

// BlockedBead represents a bead that can't be assigned due to dependencies
type BlockedBead struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	BlockedBy []string `json:"blocked_by"`
}

// UnassignableBead represents a bead that can't be assigned for other reasons
type UnassignableBead struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

// AssignSummary provides assignment statistics
type AssignSummary struct {
	TotalAgents       int `json:"total_agents"`
	IdleAgents        int `json:"idle_agents"`
	WorkingAgents     int `json:"working_agents"`
	ReadyBeads        int `json:"ready_beads"`
	BlockedBeads      int `json:"blocked_beads"`
	Recommendations   int `json:"recommendations"`
	UnassignableBeads int `json:"unassignable_beads"`
}

// AssignAgentHints provides actionable suggestions for AI agents
type AssignAgentHints struct {
	Summary           string   `json:"summary,omitempty"`
	SuggestedCommands []string `json:"suggested_commands,omitempty"`
	Warnings          []string `json:"warnings,omitempty"`
}

// AgentStrength returns the task type affinity score for an agent/task combination.
// This delegates to the assign package's capability matrix which supports
// configuration overrides and learned score adjustments.
func AgentStrength(agentType, taskType string) float64 {
	return assign.GetAgentScoreByString(agentType, taskType)
}

// PrintAssign outputs work assignment recommendations as JSON
func PrintAssign(opts AssignOptions) error {
	if opts.Session == "" {
		return RobotError(
			fmt.Errorf("session name is required"),
			ErrCodeInvalidFlag,
			"Provide session name: ntm --robot-assign=myproject",
		)
	}

	if !tmux.SessionExists(opts.Session) {
		return RobotError(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		)
	}

	// Normalize strategy
	strategy := strings.ToLower(opts.Strategy)
	if strategy == "" {
		strategy = "balanced"
	}
	validStrategies := map[string]bool{"balanced": true, "speed": true, "quality": true, "dependency": true}
	if !validStrategies[strategy] {
		return RobotError(
			fmt.Errorf("invalid strategy '%s'", opts.Strategy),
			ErrCodeInvalidFlag,
			"Valid strategies: balanced, speed, quality, dependency",
		)
	}

	// Get agents from tmux panes
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		return RobotError(
			fmt.Errorf("failed to get panes: %w", err),
			ErrCodeInternalError,
			"Check tmux is running and session is accessible",
		)
	}

	// Build agent info
	var agents []assignAgentInfo
	var idleAgentPanes []string

	for _, pane := range panes {
		agentType := detectAgentType(pane.Title)
		if agentType == "user" || agentType == "unknown" {
			continue // Skip non-agent panes
		}

		model := detectModel(agentType, pane.Title)

		// Capture state (simplified - just check last few lines)
		scrollback, _ := tmux.CapturePaneOutput(pane.ID, 10)
		state := determineState(scrollback, agentType)

		agents = append(agents, assignAgentInfo{
			paneIdx:   pane.Index,
			agentType: agentType,
			model:     model,
			state:     state,
		})
		if state == "idle" {
			idleAgentPanes = append(idleAgentPanes, fmt.Sprintf("%d", pane.Index))
		}
	}

	// Get beads from bv
	wd, _ := os.Getwd()
	readyBeads := bv.GetReadyPreview(wd, 50)   // Get up to 50 ready beads
	inProgress := bv.GetInProgressList(wd, 50) // Get in-progress for context

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

	output := AssignOutput{
		RobotResponse:   NewRobotResponse(true),
		Session:         opts.Session,
		Strategy:        strategy,
		GeneratedAt:     time.Now().UTC(),
		Recommendations: make([]AssignRecommend, 0),
		BlockedBeads:    make([]BlockedBead, 0),
		IdleAgents:      idleAgentPanes,
	}

	// Build working agents set from in-progress beads
	workingAgents := len(agents) - len(idleAgentPanes)

	// Generate recommendations based on strategy
	recommendations := generateAssignments(agents, readyBeads, strategy, idleAgentPanes)
	output.Recommendations = recommendations

	// Add blocked beads (beads with unmet dependencies)
	// Note: GetReadyPreview already filters to ready beads, so blocked beads
	// would come from a separate query. For now, we'll leave this empty.
	// In a future enhancement, we could query bd blocked.

	// Build summary
	output.Summary = AssignSummary{
		TotalAgents:     len(agents),
		IdleAgents:      len(idleAgentPanes),
		WorkingAgents:   workingAgents,
		ReadyBeads:      len(readyBeads),
		BlockedBeads:    len(output.BlockedBeads),
		Recommendations: len(recommendations),
	}

	// Generate agent hints
	output.AgentHints = generateAssignHints(recommendations, idleAgentPanes, readyBeads, inProgress)

	return encodeJSON(output)
}

// assignAgentInfo holds agent data for assignment processing
type assignAgentInfo struct {
	paneIdx   int
	agentType string
	model     string
	state     string
}

// generateAssignments creates assignment recommendations based on strategy
func generateAssignments(agents []assignAgentInfo, beads []bv.BeadPreview, strategy string, idleAgents []string) []AssignRecommend {
	var recommendations []AssignRecommend

	// Create a map of idle agents for quick lookup
	idleSet := make(map[string]bool)
	for _, a := range idleAgents {
		idleSet[a] = true
	}

	// Get idle agent details
	var idleAgentDetails []assignAgentInfo
	for _, a := range agents {
		paneKey := fmt.Sprintf("%d", a.paneIdx)
		if idleSet[paneKey] {
			idleAgentDetails = append(idleAgentDetails, a)
		}
	}

	// Assign beads to idle agents based on strategy
	beadIdx := 0
	for _, agent := range idleAgentDetails {
		if beadIdx >= len(beads) {
			break // No more beads to assign
		}

		bead := beads[beadIdx]
		paneKey := fmt.Sprintf("%d", agent.paneIdx)

		// Calculate confidence based on strategy
		confidence := calculateConfidence(agent.agentType, bead, strategy)
		reasoning := generateReasoning(agent.agentType, bead, strategy)

		recommendations = append(recommendations, AssignRecommend{
			Agent:      paneKey,
			AgentType:  agent.agentType,
			Model:      agent.model,
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

// calculateConfidence determines assignment confidence based on agent-task match
func calculateConfidence(agentType string, bead bv.BeadPreview, strategy string) float64 {
	// Extract task type from bead title/priority
	taskType := inferTaskType(bead)

	// Get capability score from the assign package
	baseConfidence := AgentStrength(agentType, taskType)

	// Adjust based on strategy
	switch strategy {
	case "quality":
		// Quality strategy favors better agent-task matches
		// Using capability matrix scores
	case "speed":
		// Speed strategy slightly favors any available agent
		baseConfidence = (baseConfidence + 0.9) / 2
	case "dependency":
		// Dependency strategy favors high-priority items
		priority := parsePriority(bead.Priority)
		if priority <= 1 { // P0 or P1
			baseConfidence = min(baseConfidence+0.1, 0.95)
		}
	}

	return baseConfidence
}

// inferTaskType attempts to determine task type from bead metadata
func inferTaskType(bead bv.BeadPreview) string {
	title := strings.ToLower(bead.Title)

	// Check for common keywords in priority order
	// Order matters! Check specific types before generic ones.
	type rule struct {
		typ string
		kws []string
	}

	rules := []rule{
		{"bug", []string{"bug", "fix", "broken", "error", "crash"}},
		{"testing", []string{"test", "spec", "coverage"}},
		{"documentation", []string{"doc", "readme", "comment", "documentation"}},
		{"refactor", []string{"refactor", "cleanup", "improve", "consolidate"}},
		{"analysis", []string{"analyze", "investigate", "research", "design"}},
		{"feature", []string{"feature", "implement", "add", "new"}},
	}

	for _, r := range rules {
		for _, kw := range r.kws {
			if strings.Contains(title, kw) {
				return r.typ
			}
		}
	}

	return "task" // Default
}

// parsePriority converts "P0"-"P4" to integer
func parsePriority(p string) int {
	if len(p) == 2 && p[0] == 'P' {
		if n := p[1] - '0'; n <= 4 { // n is byte (unsigned), so >= 0 is always true
			return int(n)
		}
	}
	return 2 // Default to P2
}

// generateReasoning creates a human-readable explanation for the assignment
func generateReasoning(agentType string, bead bv.BeadPreview, strategy string) string {
	taskType := inferTaskType(bead)
	priority := parsePriority(bead.Priority)

	var reasons []string

	// Add task-agent match reasoning
	strength := AgentStrength(agentType, taskType)
	if strength >= 0.8 {
		reasons = append(reasons, fmt.Sprintf("%s excels at %s tasks", agentType, taskType))
	}

	// Add priority reasoning
	switch priority {
	case 0:
		reasons = append(reasons, "critical priority")
	case 1:
		reasons = append(reasons, "high priority")
	}

	// Add strategy-specific reasoning
	switch strategy {
	case "balanced":
		reasons = append(reasons, "balanced workload distribution")
	case "speed":
		reasons = append(reasons, "optimizing for speed")
	case "quality":
		reasons = append(reasons, "optimizing for quality")
	case "dependency":
		reasons = append(reasons, "prioritizing dependency unblocking")
	}

	if len(reasons) == 0 {
		return "available agent matched to available work"
	}

	return strings.Join(reasons, "; ")
}

// generateAssignHints creates actionable hints for AI agents
func generateAssignHints(recs []AssignRecommend, idleAgents []string, readyBeads []bv.BeadPreview, inProgress []bv.BeadInProgress) *AssignAgentHints {
	hints := &AssignAgentHints{}

	// Build summary
	if len(recs) == 0 && len(readyBeads) == 0 {
		hints.Summary = "No work available to assign"
	} else if len(recs) == 0 && len(idleAgents) == 0 {
		hints.Summary = fmt.Sprintf("%d beads ready but no idle agents available", len(readyBeads))
	} else if len(recs) > 0 {
		hints.Summary = fmt.Sprintf("%d assignments recommended for %d idle agents", len(recs), len(idleAgents))
	}

	// Generate suggested commands
	for _, rec := range recs {
		cmd := fmt.Sprintf("bd update %s --assignee=pane%s", rec.AssignBead, rec.Agent)
		hints.SuggestedCommands = append(hints.SuggestedCommands, cmd)
	}

	// Add warnings
	if len(readyBeads) > len(idleAgents) {
		diff := len(readyBeads) - len(idleAgents)
		hints.Warnings = append(hints.Warnings,
			fmt.Sprintf("%d beads won't be assigned - not enough idle agents", diff))
	}

	if len(inProgress) > 0 {
		staleCount := 0
		for _, b := range inProgress {
			if time.Since(b.UpdatedAt) > 24*time.Hour {
				staleCount++
			}
		}
		if staleCount > 0 {
			hints.Warnings = append(hints.Warnings,
				fmt.Sprintf("%d in-progress beads are stale (>24h since update)", staleCount))
		}
	}

	return hints
}

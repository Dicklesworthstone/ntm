package coordinator

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// AssignmentStrategy controls how tasks are distributed to agents.
type AssignmentStrategy string

const (
	// StrategyBalanced spreads work evenly across agents.
	StrategyBalanced AssignmentStrategy = "balanced"
	// StrategySpeed assigns tasks to any available agent as fast as possible.
	StrategySpeed AssignmentStrategy = "speed"
	// StrategyQuality assigns tasks to the highest-scoring agent for quality.
	StrategyQuality AssignmentStrategy = "quality"
	// StrategyDependency prioritizes blockers and critical path items.
	StrategyDependency AssignmentStrategy = "dependency"
	// StrategyRoundRobin distributes tasks evenly in deterministic order.
	// All assignments get score 1.0. First agents get +1 if counts are uneven.
	StrategyRoundRobin AssignmentStrategy = "round-robin"
)

// Assignment represents an agent-task pairing with reasoning.
type Assignment struct {
	Bead       *bv.TriageRecommendation `json:"bead"`
	Agent      *AgentState              `json:"agent"`
	Score      float64                  `json:"score"`
	Reason     string                   `json:"reason"`
	Confidence float64                  `json:"confidence"` // 0-1 confidence in this assignment
	Breakdown  AssignmentScoreBreakdown `json:"breakdown"`
}

// ScoreConfig controls how work assignments are scored.
type ScoreConfig struct {
	PreferCriticalPath      bool    // Weight critical path items higher
	PenalizeFileOverlap     bool    // Avoid assigning overlapping files
	UseAgentProfiles        bool    // Match work to agent capabilities
	BudgetAware             bool    // Consider token budgets
	ContextThreshold        float64 // Max context usage before penalizing (percentage 0-100, default 80)
	ProfileTagBoostWeight   float64 // Weight for profile tag matches (default 0.15)
	FocusPatternBoostWeight float64 // Weight for focus pattern matches (default 0.10)
}

// DefaultScoreConfig returns a reasonable default configuration.
func DefaultScoreConfig() ScoreConfig {
	return ScoreConfig{
		PreferCriticalPath:  true,
		PenalizeFileOverlap: true,
		UseAgentProfiles:    true,
		BudgetAware:         true,
		ContextThreshold:    80,
	}
}

// ScoredAssignment pairs an assignment with its computed score breakdown.
type ScoredAssignment struct {
	Assignment     *WorkAssignment
	Recommendation *bv.TriageRecommendation
	Agent          *AgentState
	TotalScore     float64
	ScoreBreakdown AssignmentScoreBreakdown
}

// AssignmentScoreBreakdown shows how the score was computed.
type AssignmentScoreBreakdown struct {
	BaseScore          float64 `json:"base_score"`           // From bv triage score
	AgentTypeBonus     float64 `json:"agent_type_bonus"`     // Bonus for agent-task match
	CriticalPathBonus  float64 `json:"critical_path_bonus"`  // Bonus for critical path items
	FileOverlapPenalty float64 `json:"file_overlap_penalty"` // Penalty for file conflicts
	ContextPenalty     float64 `json:"context_penalty"`      // Penalty for high context usage
	ProfileTagBonus    float64 `json:"profile_tag_bonus"`    // Bonus for profile tag matches
	FocusPatternBonus  float64 `json:"focus_pattern_bonus"`  // Bonus for focus pattern matches
}

// WorkAssignment represents a work assignment to an agent.
type WorkAssignment struct {
	BeadID         string    `json:"bead_id"`
	BeadTitle      string    `json:"bead_title"`
	AgentPaneID    string    `json:"agent_pane_id"`
	AgentMailName  string    `json:"agent_mail_name,omitempty"`
	AgentType      string    `json:"agent_type"`
	AssignedAt     time.Time `json:"assigned_at"`
	Priority       int       `json:"priority"`
	Score          float64   `json:"score"`
	FilesToReserve []string  `json:"files_to_reserve,omitempty"`
}

// AssignmentResult contains the result of an assignment attempt.
type AssignmentResult struct {
	Success      bool            `json:"success"`
	Assignment   *WorkAssignment `json:"assignment,omitempty"`
	Error        string          `json:"error,omitempty"`
	Reservations []string        `json:"reservations,omitempty"`
	MessageSent  bool            `json:"message_sent"`
}

// AssignWork assigns work to idle agents based on bv triage.
func (c *SessionCoordinator) AssignWork(ctx context.Context) ([]AssignmentResult, error) {
	if !c.config.AutoAssign {
		return nil, nil
	}

	// Get idle agents
	idleAgents := c.GetIdleAgents()
	if len(idleAgents) == 0 {
		return nil, nil
	}

	// Get triage recommendations
	triage, err := bv.GetTriage(c.projectKey)
	if err != nil {
		return nil, fmt.Errorf("getting triage: %w", err)
	}

	if triage == nil || len(triage.Triage.Recommendations) == 0 {
		return nil, nil
	}

	var results []AssignmentResult

	// Match agents to recommendations
	for _, agent := range idleAgents {
		if len(triage.Triage.Recommendations) == 0 {
			break // No more work to assign
		}

		// Find best match for this agent
		assignment, rec := c.findBestMatch(agent, triage.Triage.Recommendations)
		if assignment == nil {
			continue
		}

		// Attempt the assignment
		result := c.attemptAssignment(ctx, assignment, rec)
		results = append(results, result)

		if result.Success {
			// Remove this recommendation from the list
			triage.Triage.Recommendations = removeRecommendation(triage.Triage.Recommendations, rec.ID)

			// Emit event
			select {
			case c.events <- CoordinatorEvent{
				Type:      EventWorkAssigned,
				Timestamp: time.Now(),
				AgentID:   agent.PaneID,
				Details: map[string]any{
					"bead_id":    assignment.BeadID,
					"bead_title": assignment.BeadTitle,
					"agent_type": agent.AgentType,
					"score":      assignment.Score,
				},
			}:
			default:
			}
		}
	}

	return results, nil
}

// findBestMatch finds the best work recommendation for an agent.
func (c *SessionCoordinator) findBestMatch(agent *AgentState, recommendations []bv.TriageRecommendation) (*WorkAssignment, *bv.TriageRecommendation) {
	for _, rec := range recommendations {
		// Skip if blocked (status indicates blocked state)
		if rec.Status == "blocked" {
			continue
		}

		// Create assignment
		assignment := &WorkAssignment{
			BeadID:      rec.ID,
			BeadTitle:   rec.Title,
			AgentPaneID: agent.PaneID,
			AgentType:   agent.AgentType,
			AssignedAt:  time.Now(),
			Priority:    rec.Priority,
			Score:       rec.Score,
		}

		// Check agent mail name mapping
		if agent.AgentMailName != "" {
			assignment.AgentMailName = agent.AgentMailName
		}

		return assignment, &rec
	}

	return nil, nil
}

// attemptAssignment attempts to assign work to an agent.
func (c *SessionCoordinator) attemptAssignment(ctx context.Context, assignment *WorkAssignment, rec *bv.TriageRecommendation) AssignmentResult {
	result := AssignmentResult{
		Assignment: assignment,
	}

	// Reserve files if we know what files will be touched
	// For now, we don't pre-reserve since we don't know the files yet
	// The agent should reserve files when it starts working

	// Send assignment message if mail client available
	if c.mailClient != nil && assignment.AgentMailName != "" {
		body := c.formatAssignmentMessage(assignment, rec)
		_, err := c.mailClient.SendMessage(ctx, agentmail.SendMessageOptions{
			ProjectKey:  c.projectKey,
			SenderName:  c.agentName,
			To:          []string{assignment.AgentMailName},
			Subject:     fmt.Sprintf("Work Assignment: %s", assignment.BeadTitle),
			BodyMD:      body,
			Importance:  "normal",
			AckRequired: true,
		})

		if err != nil {
			result.Error = fmt.Sprintf("sending message: %v", err)
			return result
		}
		result.MessageSent = true
	}

	result.Success = true
	return result
}

// formatAssignmentMessage formats a work assignment message.
func (c *SessionCoordinator) formatAssignmentMessage(assignment *WorkAssignment, rec *bv.TriageRecommendation) string {
	var sb strings.Builder

	sb.WriteString("# Work Assignment\n\n")
	sb.WriteString(fmt.Sprintf("**Bead:** %s\n", assignment.BeadID))
	sb.WriteString(fmt.Sprintf("**Title:** %s\n", assignment.BeadTitle))
	sb.WriteString(fmt.Sprintf("**Priority:** P%d\n", assignment.Priority))
	sb.WriteString(fmt.Sprintf("**Score:** %.2f\n\n", assignment.Score))

	if len(rec.Reasons) > 0 {
		sb.WriteString("## Why This Task\n\n")
		for _, reason := range rec.Reasons {
			sb.WriteString(fmt.Sprintf("- %s\n", reason))
		}
		sb.WriteString("\n")
	}

	if len(rec.UnblocksIDs) > 0 {
		sb.WriteString("## Impact\n\n")
		sb.WriteString(fmt.Sprintf("Completing this will unblock %d other tasks:\n", len(rec.UnblocksIDs)))
		for _, id := range rec.UnblocksIDs {
			if sb.Len() > 1500 {
				sb.WriteString("- ...\n")
				break
			}
			sb.WriteString(fmt.Sprintf("- %s\n", id))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Instructions\n\n")
	sb.WriteString("1. Review the bead with `br show " + assignment.BeadID + "`\n")
	sb.WriteString("2. Claim the work with `br update " + assignment.BeadID + " --status in_progress`\n")
	sb.WriteString("3. Reserve any files you'll modify\n")
	sb.WriteString("4. Implement and test\n")
	sb.WriteString("5. Close with `br close " + assignment.BeadID + "`\n")
	sb.WriteString("6. Commit with `.beads/` changes\n\n")

	sb.WriteString("Please acknowledge this message when you begin work.\n")

	return sb.String()
}

// removeRecommendation removes a recommendation by ID from the list.
func removeRecommendation(recs []bv.TriageRecommendation, id string) []bv.TriageRecommendation {
	if len(recs) == 0 {
		return nil
	}
	result := make([]bv.TriageRecommendation, 0, len(recs))
	for _, r := range recs {
		if r.ID != id {
			result = append(result, r)
		}
	}
	return result
}

// GetAssignableWork returns work items that could be assigned to idle agents.
func (c *SessionCoordinator) GetAssignableWork(ctx context.Context) ([]bv.TriageRecommendation, error) {
	triage, err := bv.GetTriage(c.projectKey)
	if err != nil {
		return nil, err
	}

	if triage == nil {
		return nil, nil
	}

	// Filter to unblocked items
	var assignable []bv.TriageRecommendation
	for _, rec := range triage.Triage.Recommendations {
		if rec.Status != "blocked" {
			assignable = append(assignable, rec)
		}
	}

	return assignable, nil
}

// SuggestAssignment suggests the best work for a specific agent without assigning.
func (c *SessionCoordinator) SuggestAssignment(ctx context.Context, paneID string) (*WorkAssignment, error) {
	agent := c.GetAgentByPaneID(paneID)
	if agent == nil {
		return nil, fmt.Errorf("agent not found: %s", paneID)
	}

	triage, err := bv.GetTriage(c.projectKey)
	if err != nil {
		return nil, err
	}

	if triage == nil || len(triage.Triage.Recommendations) == 0 {
		return nil, nil
	}

	assignment, _ := c.findBestMatch(agent, triage.Triage.Recommendations)
	return assignment, nil
}

// ScoreAndSelectAssignments computes optimal agent-task pairings using multi-factor scoring.
// It returns a list of scored assignments sorted by total score (highest first).
func ScoreAndSelectAssignments(
	idleAgents []*AgentState,
	triage *bv.TriageResponse,
	config ScoreConfig,
	existingReservations map[string][]string, // agent -> reserved file patterns
) []ScoredAssignment {
	if len(idleAgents) == 0 || triage == nil || len(triage.Triage.Recommendations) == 0 {
		return nil
	}

	var candidates []ScoredAssignment

	// Score all possible agent-task combinations
	for _, agent := range idleAgents {
		for i := range triage.Triage.Recommendations {
			rec := &triage.Triage.Recommendations[i]

			// Skip blocked items
			if rec.Status == "blocked" {
				continue
			}

			scored := scoreAssignment(agent, rec, config, existingReservations)
			if scored.TotalScore > 0 {
				candidates = append(candidates, scored)
			}
		}
	}

	// Sort by total score (highest first)
	sortScoredAssignments(candidates)

	// Select non-conflicting assignments (each agent gets at most one task)
	var selected []ScoredAssignment
	assignedAgents := make(map[string]bool)
	assignedTasks := make(map[string]bool)

	for _, candidate := range candidates {
		agentID := candidate.Agent.PaneID
		taskID := candidate.Recommendation.ID

		if assignedAgents[agentID] || assignedTasks[taskID] {
			continue
		}

		selected = append(selected, candidate)
		assignedAgents[agentID] = true
		assignedTasks[taskID] = true
	}

	return selected
}

// sortScoredAssignments sorts assignments by total score (highest first).
func sortScoredAssignments(candidates []ScoredAssignment) {
	for i := 0; i < len(candidates)-1; i++ {
		for j := i + 1; j < len(candidates); j++ {
			if candidates[j].TotalScore > candidates[i].TotalScore {
				candidates[i], candidates[j] = candidates[j], candidates[i]
			}
		}
	}
}

// scoreAssignment computes the score for a single agent-task pairing.
func scoreAssignment(
	agent *AgentState,
	rec *bv.TriageRecommendation,
	config ScoreConfig,
	existingReservations map[string][]string,
) ScoredAssignment {
	breakdown := AssignmentScoreBreakdown{
		BaseScore: rec.Score,
	}

	// Agent type matching
	if config.UseAgentProfiles {
		breakdown.AgentTypeBonus = computeAgentTypeBonus(agent.AgentType, rec)
	}

	// Profile-based routing bonuses
	if config.UseAgentProfiles && agent.Profile != nil {
		// Extract task tags from title and any available description
		taskTags := ExtractTaskTags(rec.Title, "")

		// Compute profile tag bonus based on tag overlap
		tagWeight := config.ProfileTagBoostWeight
		if tagWeight == 0 {
			tagWeight = 0.15 // Default 15% weight
		}
		breakdown.ProfileTagBonus = computeProfileTagBonus(agent.Profile, taskTags, tagWeight)

		// Extract mentioned files from task title
		mentionedFiles := ExtractMentionedFiles(rec.Title, "")

		// Compute focus pattern bonus based on file pattern matching
		patternWeight := config.FocusPatternBoostWeight
		if patternWeight == 0 {
			patternWeight = 0.10 // Default 10% weight
		}
		breakdown.FocusPatternBonus = computeFocusPatternBonus(agent.Profile, mentionedFiles, patternWeight)
	}

	// Critical path bonus
	if config.PreferCriticalPath && rec.Breakdown != nil {
		breakdown.CriticalPathBonus = computeCriticalPathBonus(rec.Breakdown)
	}

	// File overlap penalty
	// Note: computeFileOverlapPenalty falls back to agent.Reservations if map is nil
	if config.PenalizeFileOverlap {
		breakdown.FileOverlapPenalty = computeFileOverlapPenalty(agent, existingReservations)
	}

	// Context/budget penalty
	// Note: ContextUsage is in percentage scale (0-100), not ratio (0-1)
	if config.BudgetAware {
		threshold := config.ContextThreshold
		if threshold == 0 {
			threshold = 80 // 80% threshold (percentage scale)
		}
		breakdown.ContextPenalty = computeContextPenalty(agent.ContextUsage, threshold)
	}

	totalScore := breakdown.BaseScore +
		breakdown.AgentTypeBonus +
		breakdown.CriticalPathBonus +
		breakdown.ProfileTagBonus +
		breakdown.FocusPatternBonus -
		breakdown.FileOverlapPenalty -
		breakdown.ContextPenalty

	return ScoredAssignment{
		Assignment: &WorkAssignment{
			BeadID:        rec.ID,
			BeadTitle:     rec.Title,
			AgentPaneID:   agent.PaneID,
			AgentMailName: agent.AgentMailName,
			AgentType:     agent.AgentType,
			AssignedAt:    time.Now(),
			Priority:      rec.Priority,
			Score:         totalScore,
		},
		Recommendation: rec,
		Agent:          agent,
		TotalScore:     totalScore,
		ScoreBreakdown: breakdown,
	}
}

// computeAgentTypeBonus returns a bonus based on agent-task compatibility.
// Claude (cc) is better for complex tasks (epics, features), Codex (cod) for quick fixes.
func computeAgentTypeBonus(agentType string, rec *bv.TriageRecommendation) float64 {
	taskComplexity := estimateTaskComplexity(rec)

	switch agentType {
	case "cc", "claude":
		// Claude excels at complex, multi-step work
		if taskComplexity >= 0.7 {
			return 0.15 // 15% bonus for complex tasks
		} else if taskComplexity <= 0.3 {
			return -0.05 // Small penalty for simple tasks (overkill)
		}
	case "cod", "codex":
		// Codex is great for quick, focused fixes
		if taskComplexity <= 0.3 {
			return 0.15 // 15% bonus for simple tasks
		} else if taskComplexity >= 0.7 {
			return -0.1 // Penalty for complex tasks
		}
	case "gmi", "gemini":
		// Gemini is balanced
		if taskComplexity >= 0.4 && taskComplexity <= 0.6 {
			return 0.05 // Small bonus for medium complexity
		}
	}

	return 0
}

// estimateTaskComplexity returns a 0-1 score based on task characteristics.
func estimateTaskComplexity(rec *bv.TriageRecommendation) float64 {
	complexity := 0.5 // Start with medium

	// Task type affects complexity
	switch rec.Type {
	case "epic":
		complexity += 0.3
	case "feature":
		complexity += 0.2
	case "bug":
		complexity += 0.0 // Varies
	case "task":
		complexity -= 0.1
	case "chore":
		complexity -= 0.2
	}

	// Priority affects perceived complexity (urgent items often simpler)
	if rec.Priority == 0 {
		complexity -= 0.1 // Critical items often need quick fixes
	} else if rec.Priority >= 3 {
		complexity += 0.1 // Backlog items often bigger
	}

	// Number of items unblocked indicates scope
	if len(rec.UnblocksIDs) >= 5 {
		complexity += 0.15
	} else if len(rec.UnblocksIDs) >= 3 {
		complexity += 0.1
	}

	// Clamp to 0-1
	if complexity < 0 {
		complexity = 0
	} else if complexity > 1 {
		complexity = 1
	}

	return complexity
}

// computeCriticalPathBonus gives bonus for items with high graph centrality.
func computeCriticalPathBonus(breakdown *bv.ScoreBreakdown) float64 {
	bonus := 0.0

	// High PageRank means central to the project
	if breakdown.Pagerank > 0.05 {
		bonus += breakdown.Pagerank * 2 // Up to ~0.15 bonus
	}

	// High blocker ratio means it unblocks many things
	if breakdown.BlockerRatio > 0.05 {
		bonus += breakdown.BlockerRatio * 1.5
	}

	// Time-to-impact indicates depth in critical path
	if breakdown.TimeToImpact > 0.04 {
		bonus += 0.05
	}

	return bonus
}

// computeFileOverlapPenalty penalizes agents who already have many file reservations.
func computeFileOverlapPenalty(agent *AgentState, reservations map[string][]string) float64 {
	agentReservations := reservations[agent.PaneID]
	if len(agentReservations) == 0 {
		agentReservations = agent.Reservations
	}

	// Penalty increases with number of reservations
	// This encourages spreading work across agents
	count := len(agentReservations)
	if count == 0 {
		return 0
	} else if count <= 2 {
		return 0.05
	} else if count <= 5 {
		return 0.1
	}
	return 0.2
}

// computeContextPenalty penalizes agents with high context window usage.
// Both contextUsage and threshold are in percentage scale (0-100).
func computeContextPenalty(contextUsage float64, threshold float64) float64 {
	if contextUsage <= threshold {
		return 0
	}

	// Linear penalty above threshold, normalized to score scale (0-1)
	// e.g., 10% over threshold → 0.05 penalty; 20% over → 0.10 penalty
	excess := contextUsage - threshold
	return (excess / 100) * 0.5
}

// taskTagKeywords maps keywords to profile tags for task routing.
var taskTagKeywords = map[string]string{
	// Testing keywords
	"test":      "testing",
	"tests":     "testing",
	"testing":   "testing",
	"unittest":  "testing",
	"unit test": "testing",
	"e2e":       "testing",
	"qa":        "testing",
	"coverage":  "testing",

	// Architecture keywords
	"refactor":     "architecture",
	"restructure":  "architecture",
	"redesign":     "architecture",
	"architecture": "architecture",
	"pattern":      "architecture",
	"design":       "architecture",

	// Documentation keywords
	"document":      "documentation",
	"documentation": "documentation",
	"readme":        "documentation",
	"docs":          "documentation",
	"docstring":     "documentation",
	"comment":       "documentation",

	// Implementation keywords
	"implement": "implementation",
	"add":       "implementation",
	"create":    "implementation",
	"build":     "implementation",
	"feature":   "implementation",
	"develop":   "implementation",

	// Review keywords
	"review":  "review",
	"audit":   "review",
	"inspect": "review",
	"check":   "review",

	// Bug/fix keywords
	"fix":   "bugs",
	"bug":   "bugs",
	"patch": "bugs",
	"error": "bugs",
	"crash": "bugs",
}

// ExtractTaskTags extracts relevant profile tags from task title and description.
func ExtractTaskTags(title, description string) []string {
	text := strings.ToLower(title + " " + description)
	tagSet := make(map[string]bool)

	for keyword, tag := range taskTagKeywords {
		if strings.Contains(text, keyword) {
			tagSet[tag] = true
		}
	}

	tags := make([]string, 0, len(tagSet))
	for tag := range tagSet {
		tags = append(tags, tag)
	}
	return tags
}

// ExtractMentionedFiles extracts file paths mentioned in task text.
func ExtractMentionedFiles(title, description string) []string {
	text := title + " " + description
	words := strings.Fields(text)
	var files []string

	for _, word := range words {
		// Clean punctuation
		word = strings.Trim(word, ",.;:()[]{}\"'`")
		if isFilePath(word) {
			files = append(files, word)
		}
	}
	return files
}

// isFilePath checks if a string looks like a file path.
func isFilePath(s string) bool {
	if len(s) < 3 {
		return false
	}

	// Contains path separator or file extension
	if strings.Contains(s, "/") || strings.Contains(s, "\\") {
		return true
	}

	// Has common file extensions
	extensions := []string{".go", ".ts", ".js", ".py", ".rs", ".md", ".yaml", ".yml", ".json", ".toml"}
	for _, ext := range extensions {
		if strings.HasSuffix(s, ext) {
			return true
		}
	}

	// Contains glob patterns
	if strings.Contains(s, "*") || strings.Contains(s, "**") {
		return true
	}

	// Starts with dot (hidden file/directory)
	if strings.HasPrefix(s, ".") && len(s) > 1 {
		return true
	}

	return false
}

// computeProfileTagBonus computes bonus based on matching persona tags.
func computeProfileTagBonus(profile *persona.Persona, taskTags []string, weight float64) float64 {
	if profile == nil || len(profile.Tags) == 0 || len(taskTags) == 0 {
		return 0
	}

	// Create a set of profile tags for quick lookup
	profileTags := make(map[string]bool)
	for _, tag := range profile.Tags {
		profileTags[strings.ToLower(tag)] = true
	}

	// Count matching tags
	matches := 0
	for _, tag := range taskTags {
		if profileTags[strings.ToLower(tag)] {
			matches++
		}
	}

	if matches == 0 {
		return 0
	}

	// Score based on proportion of profile tags matched
	matchRatio := float64(matches) / float64(len(profile.Tags))
	return matchRatio * weight
}

// computeFocusPatternBonus computes bonus based on file pattern matches.
func computeFocusPatternBonus(profile *persona.Persona, mentionedFiles []string, weight float64) float64 {
	if profile == nil || len(profile.FocusPatterns) == 0 || len(mentionedFiles) == 0 {
		return 0
	}

	// Count how many mentioned files match any focus pattern
	matches := 0
	for _, file := range mentionedFiles {
		for _, pattern := range profile.FocusPatterns {
			if matchFocusPattern(pattern, file) {
				matches++
				break // Count each file only once
			}
		}
	}

	if matches == 0 {
		return 0
	}

	// Score based on proportion of files matched
	matchRatio := float64(matches) / float64(len(mentionedFiles))
	return matchRatio * weight
}

// matchFocusPattern checks if a file matches a focus pattern using glob-style matching.
func matchFocusPattern(pattern, file string) bool {
	// Handle ** (any path depth)
	if strings.Contains(pattern, "**") {
		// Convert ** to regex-style matching
		parts := strings.Split(pattern, "**")
		if len(parts) == 2 {
			prefix := parts[0]
			suffix := strings.TrimPrefix(parts[1], "/")

			// File must start with prefix
			if prefix != "" && !strings.HasPrefix(file, prefix) {
				return false
			}

			// File must end with suffix (if any)
			if suffix != "" {
				// Remove leading * from suffix for extension matching
				suffix = strings.TrimPrefix(suffix, "*")
				return strings.HasSuffix(file, suffix)
			}
			return true
		}
	}

	// Use filepath.Match for simple glob patterns
	matched, err := filepath.Match(pattern, file)
	if err != nil {
		return false
	}
	return matched
}

// AssignTasks matches beads to agents using capability scores and availability.
// It returns optimal assignments based on the specified strategy.
//
// The strategy parameter controls how tasks are distributed:
//   - "balanced": spread work evenly across agents
//   - "speed": assign tasks to any available agent quickly
//   - "quality": assign tasks to the highest-scoring agent
//   - "dependency": prioritize blockers and critical path items
//
// The function handles:
//   - More beads than agents (some beads unassigned)
//   - More agents than beads (some agents idle)
//   - Agent availability filtering (idle, sufficient context)
func AssignTasks(
	beads []*bv.TriageRecommendation,
	agents []*AgentState,
	strategy AssignmentStrategy,
	reservations map[string][]string,
) []Assignment {
	if len(beads) == 0 || len(agents) == 0 {
		return nil
	}

	// Filter to available agents (idle with sufficient context)
	availableAgents := filterAvailableAgents(agents)
	if len(availableAgents) == 0 {
		return nil
	}

	// Build score config based on strategy
	config := buildStrategyConfig(strategy)

	// Score all agent-task combinations
	scoredPairs := scoreAllPairs(availableAgents, beads, config, reservations)
	if len(scoredPairs) == 0 {
		return nil
	}

	// Apply strategy-specific selection
	selected := applyStrategySelection(scoredPairs, strategy, len(availableAgents), len(beads))

	// Convert to Assignment results with reasoning
	return buildAssignments(selected, strategy)
}

// filterAvailableAgents returns agents that are idle with sufficient context.
func filterAvailableAgents(agents []*AgentState) []*AgentState {
	var available []*AgentState
	for _, agent := range agents {
		if !isAgentAvailable(agent) {
			continue
		}
		available = append(available, agent)
	}
	return available
}

// isAgentAvailable checks if an agent can accept new work.
func isAgentAvailable(agent *AgentState) bool {
	// Must be idle
	if agent.Status != robot.StateWaiting {
		return false
	}

	// Must have sufficient context remaining (less than 90% used)
	if agent.ContextUsage > 90 {
		return false
	}

	return true
}

// buildStrategyConfig creates a ScoreConfig tuned for the given strategy.
func buildStrategyConfig(strategy AssignmentStrategy) ScoreConfig {
	base := DefaultScoreConfig()

	switch strategy {
	case StrategyBalanced:
		// Balanced: moderate penalties for overlap to spread work
		base.PenalizeFileOverlap = true
		base.PreferCriticalPath = true

	case StrategySpeed:
		// Speed: minimize scoring overhead, accept first available
		base.PenalizeFileOverlap = false
		base.UseAgentProfiles = false
		base.PreferCriticalPath = false

	case StrategyQuality:
		// Quality: maximize agent-task matching
		base.UseAgentProfiles = true
		base.ProfileTagBoostWeight = 0.25 // Increase profile importance
		base.FocusPatternBoostWeight = 0.15
		base.PreferCriticalPath = true

	case StrategyDependency:
		// Dependency: heavily weight critical path and blockers
		base.PreferCriticalPath = true
		base.PenalizeFileOverlap = true
	}

	return base
}

// scoredPair holds a scored agent-task pairing for selection.
type scoredPair struct {
	agent     *AgentState
	bead      *bv.TriageRecommendation
	score     float64
	breakdown AssignmentScoreBreakdown
}

// scoreAllPairs scores all valid agent-task combinations.
func scoreAllPairs(
	agents []*AgentState,
	beads []*bv.TriageRecommendation,
	config ScoreConfig,
	reservations map[string][]string,
) []scoredPair {
	var pairs []scoredPair

	for _, agent := range agents {
		for _, bead := range beads {
			// Skip blocked beads
			if bead.Status == "blocked" {
				continue
			}

			scored := scoreAssignment(agent, bead, config, reservations)
			if scored.TotalScore > 0 {
				pairs = append(pairs, scoredPair{
					agent:     agent,
					bead:      bead,
					score:     scored.TotalScore,
					breakdown: scored.ScoreBreakdown,
				})
			}
		}
	}

	return pairs
}

// applyStrategySelection selects optimal assignments based on strategy.
func applyStrategySelection(
	pairs []scoredPair,
	strategy AssignmentStrategy,
	numAgents, numBeads int,
) []scoredPair {
	switch strategy {
	case StrategySpeed:
		return selectGreedy(pairs, numAgents, numBeads)

	case StrategyBalanced:
		return selectBalanced(pairs, numAgents, numBeads)

	case StrategyQuality:
		return selectQuality(pairs, numAgents, numBeads)

	case StrategyDependency:
		return selectDependency(pairs, numAgents, numBeads)

	case StrategyRoundRobin:
		return selectBalanced(pairs, numAgents, numBeads)

	default:
		return selectGreedy(pairs, numAgents, numBeads)
	}
}

// selectGreedy picks assignments greedily by score (fastest).
func selectGreedy(pairs []scoredPair, numAgents, numBeads int) []scoredPair {
	// Sort by score descending
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].score > pairs[j].score
	})

	var selected []scoredPair
	assignedAgents := make(map[string]bool)
	assignedBeads := make(map[string]bool)

	for _, p := range pairs {
		if assignedAgents[p.agent.PaneID] || assignedBeads[p.bead.ID] {
			continue
		}

		selected = append(selected, p)
		assignedAgents[p.agent.PaneID] = true
		assignedBeads[p.bead.ID] = true

		// Stop when we've assigned all we can
		if len(selected) >= numAgents || len(selected) >= numBeads {
			break
		}
	}

	return selected
}

// selectBalanced spreads work evenly, avoiding heavily loaded agents.
// It uses live assignment tracking data from AgentState.Assignments when available
// and applies tie-breakers: (1) fewer active assignments, (2) idle status,
// (3) least-recent assignment timestamp, (4) best capability score.
// Falls back to local tracking when assignment data is unavailable (Assignments == -1).
func selectBalanced(pairs []scoredPair, numAgents, numBeads int) []scoredPair {
	// Track workload per agent during this selection round.
	// Initialize from live assignment counts if available.
	agentLoad := make(map[string]int)
	for _, p := range pairs {
		if _, seen := agentLoad[p.agent.PaneID]; !seen {
			if p.agent.Assignments >= 0 {
				// Use live assignment count
				agentLoad[p.agent.PaneID] = p.agent.Assignments
			} else {
				// Fallback: tracking unavailable, start at 0
				agentLoad[p.agent.PaneID] = 0
			}
		}
	}

	// Sort using stable sort for deterministic ordering with multi-level tie-breakers:
	// 1. Fewer active assignments (lower load first)
	// 2. Idle agents first (Status == Idle)
	// 3. Least-recent assignment timestamp (earlier LastAssignedAt first)
	// 4. Higher capability score
	// 5. PaneID as final deterministic tie-breaker
	sort.SliceStable(pairs, func(i, j int) bool {
		ai, aj := pairs[i].agent, pairs[j].agent
		loadI := agentLoad[ai.PaneID]
		loadJ := agentLoad[aj.PaneID]

		// Tie-breaker 1: Fewer assignments first
		if loadI != loadJ {
			return loadI < loadJ
		}

		// Tie-breaker 2: Idle agents first (StateWaiting = idle/ready for input)
		idleI := ai.Status == robot.StateWaiting
		idleJ := aj.Status == robot.StateWaiting
		if idleI != idleJ {
			return idleI
		}

		// Tie-breaker 3: Least-recent assignment timestamp first
		// (zero time means never assigned, treated as oldest)
		if !ai.LastAssignedAt.Equal(aj.LastAssignedAt) {
			return ai.LastAssignedAt.Before(aj.LastAssignedAt)
		}

		// Tie-breaker 4: Higher score first
		if pairs[i].score != pairs[j].score {
			return pairs[i].score > pairs[j].score
		}

		// Tie-breaker 5: Deterministic by PaneID for consistent ordering
		return ai.PaneID < aj.PaneID
	})

	var selected []scoredPair
	assignedAgents := make(map[string]bool)
	assignedBeads := make(map[string]bool)

	for _, p := range pairs {
		if assignedAgents[p.agent.PaneID] || assignedBeads[p.bead.ID] {
			continue
		}

		selected = append(selected, p)
		assignedAgents[p.agent.PaneID] = true
		assignedBeads[p.bead.ID] = true
		agentLoad[p.agent.PaneID]++

		if len(selected) >= numAgents || len(selected) >= numBeads {
			break
		}
	}

	return selected
}

// selectQuality picks the highest-scoring agent for each task.
func selectQuality(pairs []scoredPair, numAgents, numBeads int) []scoredPair {
	// Group by bead, find best agent for each
	beadBestAgent := make(map[string]scoredPair)

	for _, p := range pairs {
		existing, exists := beadBestAgent[p.bead.ID]
		if !exists || p.score > existing.score {
			beadBestAgent[p.bead.ID] = p
		}
	}

	// Collect all best matches
	var bestMatches []scoredPair
	for _, p := range beadBestAgent {
		bestMatches = append(bestMatches, p)
	}

	// Sort by score descending
	sort.Slice(bestMatches, func(i, j int) bool {
		return bestMatches[i].score > bestMatches[j].score
	})

	// Select ensuring no agent duplication
	var selected []scoredPair
	assignedAgents := make(map[string]bool)

	for _, p := range bestMatches {
		if assignedAgents[p.agent.PaneID] {
			// Find next best agent for this bead
			continue
		}

		selected = append(selected, p)
		assignedAgents[p.agent.PaneID] = true

		if len(selected) >= numAgents {
			break
		}
	}

	return selected
}

// selectDependency prioritizes blockers and critical path items.
func selectDependency(pairs []scoredPair, numAgents, numBeads int) []scoredPair {
	// Sort by: number of items unblocked, then by score
	sort.Slice(pairs, func(i, j int) bool {
		blocksI := len(pairs[i].bead.UnblocksIDs)
		blocksJ := len(pairs[j].bead.UnblocksIDs)

		if blocksI != blocksJ {
			return blocksI > blocksJ // More blockers first
		}

		// Priority (lower is higher priority)
		if pairs[i].bead.Priority != pairs[j].bead.Priority {
			return pairs[i].bead.Priority < pairs[j].bead.Priority
		}

		return pairs[i].score > pairs[j].score
	})

	// Greedy selection
	var selected []scoredPair
	assignedAgents := make(map[string]bool)
	assignedBeads := make(map[string]bool)

	for _, p := range pairs {
		if assignedAgents[p.agent.PaneID] || assignedBeads[p.bead.ID] {
			continue
		}

		selected = append(selected, p)
		assignedAgents[p.agent.PaneID] = true
		assignedBeads[p.bead.ID] = true

		if len(selected) >= numAgents || len(selected) >= numBeads {
			break
		}
	}

	return selected
}

// buildAssignments converts selected pairs into Assignment results with reasoning.
func buildAssignments(selected []scoredPair, strategy AssignmentStrategy) []Assignment {
	assignments := make([]Assignment, len(selected))

	for i, p := range selected {
		reason := buildAssignmentReason(p, strategy)
		confidence := computeConfidence(p)

		assignments[i] = Assignment{
			Bead:       p.bead,
			Agent:      p.agent,
			Score:      p.score,
			Reason:     reason,
			Confidence: confidence,
			Breakdown:  p.breakdown,
		}
	}

	return assignments
}

// buildAssignmentReason generates human-readable reasoning for an assignment.
func buildAssignmentReason(p scoredPair, strategy AssignmentStrategy) string {
	var reasons []string

	// Strategy-specific lead reason
	switch strategy {
	case StrategyDependency:
		if len(p.bead.UnblocksIDs) > 0 {
			reasons = append(reasons, fmt.Sprintf("unblocks %d tasks", len(p.bead.UnblocksIDs)))
		}
	case StrategyQuality:
		reasons = append(reasons, "best capability match")
	case StrategyBalanced:
		reasons = append(reasons, "even workload distribution")
	case StrategySpeed:
		reasons = append(reasons, "fastest available agent")
	}

	// Add breakdown insights
	if p.breakdown.AgentTypeBonus > 0.05 {
		reasons = append(reasons, fmt.Sprintf("agent type bonus +%.0f%%", p.breakdown.AgentTypeBonus*100))
	}
	if p.breakdown.ProfileTagBonus > 0.05 {
		reasons = append(reasons, "matching profile tags")
	}
	if p.breakdown.CriticalPathBonus > 0.05 {
		reasons = append(reasons, "on critical path")
	}

	if len(reasons) == 0 {
		return "available and qualified"
	}

	return strings.Join(reasons, "; ")
}

// computeConfidence calculates confidence level for an assignment (0-1).
func computeConfidence(p scoredPair) float64 {
	// Base confidence from normalized score
	// Most scores are in 0-2 range, normalize to 0-1
	confidence := p.score / 2.0
	if confidence > 1.0 {
		confidence = 1.0
	}

	// Boost for positive factors
	if p.breakdown.AgentTypeBonus > 0 {
		confidence += 0.1
	}
	if p.breakdown.ProfileTagBonus > 0 {
		confidence += 0.1
	}

	// Penalty for negative factors
	if p.breakdown.ContextPenalty > 0 {
		confidence -= p.breakdown.ContextPenalty
	}
	if p.breakdown.FileOverlapPenalty > 0 {
		confidence -= p.breakdown.FileOverlapPenalty / 2
	}

	// Clamp to 0-1
	if confidence < 0.1 {
		confidence = 0.1
	}
	if confidence > 0.95 {
		confidence = 0.95
	}

	return confidence
}

// ParseStrategy converts a string to an AssignmentStrategy.
func ParseStrategy(s string) AssignmentStrategy {
	switch strings.ToLower(s) {
	case "balanced":
		return StrategyBalanced
	case "speed", "fast":
		return StrategySpeed
	case "quality", "best":
		return StrategyQuality
	case "dependency", "deps", "blockers":
		return StrategyDependency
	case "round-robin", "roundrobin", "rr":
		return StrategyRoundRobin
	default:
		return StrategyBalanced // Default to balanced
	}
}

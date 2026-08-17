package assign

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Strategy represents work assignment strategies.
type Strategy string

const (
	// StrategyBalanced spreads work evenly across agents.
	StrategyBalanced Strategy = "balanced"
	// StrategySpeed assigns to any available agent quickly.
	StrategySpeed Strategy = "speed"
	// StrategyQuality assigns to the highest-scoring agent for the task.
	StrategyQuality Strategy = "quality"
	// StrategyDependency prioritizes blockers and dependency chains.
	StrategyDependency Strategy = "dependency"
	// StrategyRoundRobin distributes work evenly across agents in round-robin fashion.
	StrategyRoundRobin Strategy = "round-robin"
)

// ParseStrategy converts a string to Strategy with validation.
func ParseStrategy(s string) Strategy {
	switch strings.ToLower(s) {
	case "balanced":
		return StrategyBalanced
	case "speed":
		return StrategySpeed
	case "quality":
		return StrategyQuality
	case "dependency":
		return StrategyDependency
	case "round-robin", "roundrobin", "rr":
		return StrategyRoundRobin
	default:
		return StrategyBalanced // Default
	}
}

// Bead represents a work item to be assigned.
type Bead struct {
	ID          string   // Unique identifier (e.g., "bd-1tv6")
	Title       string   // Human-readable title
	Priority    int      // 0-4 (P0 = critical, P4 = low)
	TaskType    TaskType // Inferred or explicit task type
	UnblocksIDs []string // IDs of items this unblocks when completed
	Labels      []string // Additional labels/tags
}

// Agent represents an available worker agent.
type Agent struct {
	ID           string         // Unique identifier (pane ID or name)
	AgentType    tmux.AgentType // cc, cod, gmi
	Model        string         // Specific model if known
	ContextUsage float64        // 0.0-1.0 (how much context is used)
	Idle         bool           // Whether agent is idle and available
	CurrentTask  string         // ID of current task if not idle
	Assignments  int            // Number of tasks already assigned this session
}

// Assignment represents a recommended bead-to-agent assignment.
type Assignment struct {
	Bead       Bead    `json:"bead"`
	Agent      Agent   `json:"agent"`
	Score      float64 `json:"score"`      // Combined score (0.0-1.0)
	Reason     string  `json:"reason"`     // Human-readable explanation
	Confidence float64 `json:"confidence"` // Confidence in this assignment (0.0-1.0)
}

// MatcherConfig configures the assignment algorithm.
type MatcherConfig struct {
	MaxContextUsage float64 // Maximum context usage to consider agent available (default: 0.9)
	MinConfidence   float64 // Minimum confidence to include in results (default: 0.3)
}

// DefaultMatcherConfig returns sensible defaults.
func DefaultMatcherConfig() MatcherConfig {
	return MatcherConfig{
		MaxContextUsage: 0.9,
		MinConfidence:   0.3,
	}
}

// Matcher performs bead-to-agent assignment.
type Matcher struct {
	matrix *CapabilityMatrix
	config MatcherConfig
}

// NewMatcher creates a new Matcher with the global capability matrix.
func NewMatcher() *Matcher {
	return &Matcher{
		matrix: GlobalMatrix(),
		config: DefaultMatcherConfig(),
	}
}

// AssignTasks matches beads to agents based on the specified strategy.
// Returns assignments sorted by score (highest first).
func (m *Matcher) AssignTasks(beads []Bead, agents []Agent, strategy Strategy) []Assignment {
	if len(beads) == 0 || len(agents) == 0 {
		return nil
	}

	// Filter to available agents
	available := m.filterAvailableAgents(agents)
	if len(available) == 0 {
		return nil
	}

	// Sort beads by priority (P0 first) for consistent processing
	sortedBeads := make([]Bead, len(beads))
	copy(sortedBeads, beads)
	sort.Slice(sortedBeads, func(i, j int) bool {
		return sortedBeads[i].Priority < sortedBeads[j].Priority
	})

	// Apply strategy-specific assignment
	switch strategy {
	case StrategySpeed:
		return m.assignSpeed(sortedBeads, available)
	case StrategyQuality:
		return m.assignQuality(sortedBeads, available)
	case StrategyDependency:
		return m.assignDependency(sortedBeads, available)
	case StrategyRoundRobin:
		return m.assignRoundRobin(sortedBeads, available)
	default: // StrategyBalanced
		return m.assignBalanced(sortedBeads, available)
	}
}

// filterAvailableAgents returns agents that are idle and have sufficient context.
func (m *Matcher) filterAvailableAgents(agents []Agent) []Agent {
	var available []Agent
	for _, agent := range agents {
		if agent.Idle && agent.ContextUsage <= m.config.MaxContextUsage {
			available = append(available, agent)
		}
	}
	return available
}

// assignBalanced spreads work evenly across agents.
func (m *Matcher) assignBalanced(beads []Bead, agents []Agent) []Assignment {
	var assignments []Assignment
	agentAssignCounts := make(map[string]int)

	// Initialize counts from existing assignments
	for _, agent := range agents {
		agentAssignCounts[agent.ID] = agent.Assignments
	}

	for _, bead := range beads {
		// Find agent with lowest assignment count that has good capability
		var bestAgent *Agent
		bestScore := -1.0
		minAssignments := int(^uint(0) >> 1) // Max int

		for i := range agents {
			agent := &agents[i]
			score := m.scoreAgentForBead(agent, &bead)

			// Prefer agents with fewer assignments, then by score
			assignCount := agentAssignCounts[agent.ID]
			if assignCount < minAssignments || (assignCount == minAssignments && score > bestScore) {
				minAssignments = assignCount
				bestScore = score
				bestAgent = agent
			}
		}

		if bestAgent != nil && bestScore >= m.config.MinConfidence {
			assignments = append(assignments, Assignment{
				Bead:       bead,
				Agent:      *bestAgent,
				Score:      bestScore,
				Confidence: bestScore,
				Reason:     m.buildReason(bestAgent, &bead, "balanced workload distribution"),
			})
			agentAssignCounts[bestAgent.ID]++
		}
	}

	return assignments
}

// assignSpeed assigns to any available agent as quickly as possible.
func (m *Matcher) assignSpeed(beads []Bead, agents []Agent) []Assignment {
	var assignments []Assignment
	usedAgents := make(map[string]bool)

	for _, bead := range beads {
		// Find first available agent with acceptable score
		for i := range agents {
			agent := &agents[i]
			if usedAgents[agent.ID] {
				continue
			}

			score := m.scoreAgentForBead(agent, &bead)
			// Speed strategy has lower threshold
			minScore := m.config.MinConfidence * 0.5

			if score >= minScore {
				// Boost confidence slightly since we're optimizing for speed
				confidence := (score + 0.9) / 2

				assignments = append(assignments, Assignment{
					Bead:       bead,
					Agent:      *agent,
					Score:      score,
					Confidence: confidence,
					Reason:     m.buildReason(agent, &bead, "optimizing for speed"),
				})
				usedAgents[agent.ID] = true
				break
			}
		}
	}

	return assignments
}

// assignQuality assigns to the highest-scoring agent for each task.
func (m *Matcher) assignQuality(beads []Bead, agents []Agent) []Assignment {
	var assignments []Assignment
	usedAgents := make(map[string]bool)

	for _, bead := range beads {
		// Find best scoring agent
		var bestAgent *Agent
		bestScore := 0.0

		for i := range agents {
			agent := &agents[i]
			if usedAgents[agent.ID] {
				continue
			}

			score := m.scoreAgentForBead(agent, &bead)
			if score > bestScore {
				bestScore = score
				bestAgent = agent
			}
		}

		if bestAgent != nil && bestScore >= m.config.MinConfidence {
			assignments = append(assignments, Assignment{
				Bead:       bead,
				Agent:      *bestAgent,
				Score:      bestScore,
				Confidence: bestScore,
				Reason:     m.buildReason(bestAgent, &bead, "optimizing for quality"),
			})
			usedAgents[bestAgent.ID] = true
		}
	}

	return assignments
}

// assignDependency prioritizes blockers and dependency chains.
func (m *Matcher) assignDependency(beads []Bead, agents []Agent) []Assignment {
	var assignments []Assignment
	usedAgents := make(map[string]bool)

	// Re-sort beads by: priority first, then number of things they unblock
	sortedBeads := make([]Bead, len(beads))
	copy(sortedBeads, beads)
	sort.Slice(sortedBeads, func(i, j int) bool {
		// Primary: priority (P0 first)
		if sortedBeads[i].Priority != sortedBeads[j].Priority {
			return sortedBeads[i].Priority < sortedBeads[j].Priority
		}
		// Secondary: unblocks count (more first)
		return len(sortedBeads[i].UnblocksIDs) > len(sortedBeads[j].UnblocksIDs)
	})

	for _, bead := range sortedBeads {
		// Find best scoring agent
		var bestAgent *Agent
		bestScore := 0.0

		for i := range agents {
			agent := &agents[i]
			if usedAgents[agent.ID] {
				continue
			}

			score := m.scoreAgentForBead(agent, &bead)

			// Boost score for high-priority items
			if bead.Priority <= 1 {
				score = min(score+0.1, 1.0)
			}

			// Boost score for blockers
			if len(bead.UnblocksIDs) > 0 {
				blockerBoost := min(float64(len(bead.UnblocksIDs))*0.05, 0.15)
				score = min(score+blockerBoost, 1.0)
			}

			if score > bestScore {
				bestScore = score
				bestAgent = agent
			}
		}

		if bestAgent != nil && bestScore >= m.config.MinConfidence {
			reason := "prioritizing dependency unblocking"
			if len(bead.UnblocksIDs) > 0 {
				reason = fmt.Sprintf("unblocks %d items; %s", len(bead.UnblocksIDs), reason)
			}

			assignments = append(assignments, Assignment{
				Bead:       bead,
				Agent:      *bestAgent,
				Score:      bestScore,
				Confidence: bestScore,
				Reason:     m.buildReason(bestAgent, &bead, reason),
			})
			usedAgents[bestAgent.ID] = true
		}
	}

	return assignments
}

// assignRoundRobin distributes work evenly across agents in round-robin fashion.
// Each bead is assigned to agents in order, cycling back to the first agent after the last.
// This ensures even distribution: 12 beads / 4 agents = 3, 3, 3, 3
// With uneven counts, first agents get +1: 13 beads / 4 agents = 4, 3, 3, 3
// Beads are assigned in BV priority order (already sorted by caller), so:
// - Agent 1 gets: bead 1, 5, 9, 13...
// - Agent 2 gets: bead 2, 6, 10, 14...
// - Agent 3 gets: bead 3, 7, 11, 15...
// - Agent 4 gets: bead 4, 8, 12, 16...
// Score is 1.0 for all assignments since round-robin doesn't score.
func (m *Matcher) assignRoundRobin(beads []Bead, agents []Agent) []Assignment {
	if len(agents) == 0 {
		return nil
	}

	assignments := make([]Assignment, 0, len(beads))
	numAgents := len(agents)

	for i, bead := range beads {
		agent := agents[i%numAgents]
		assignments = append(assignments, Assignment{
			Bead:       bead,
			Agent:      agent,
			Score:      1.0, // Round-robin assigns all equally
			Confidence: 1.0, // Deterministic assignment
			Reason:     fmt.Sprintf("round-robin assignment: bead %d → agent %d (%s)", i+1, (i%numAgents)+1, agent.AgentType),
		})
	}

	return assignments
}

// scoreAgentForBead computes a combined score for an agent-bead pair.
// Formula: capability_score * (1 - context_usage)
func (m *Matcher) scoreAgentForBead(agent *Agent, bead *Bead) float64 {
	capabilityScore := m.matrix.GetScore(agent.AgentType, bead.TaskType)
	availabilityFactor := 1.0 - agent.ContextUsage

	return capabilityScore * availabilityFactor
}

// buildReason creates a human-readable explanation for an assignment.
func (m *Matcher) buildReason(agent *Agent, bead *Bead, strategyNote string) string {
	var parts []string

	// Add capability reasoning
	capScore := m.matrix.GetScore(agent.AgentType, bead.TaskType)
	if capScore >= 0.85 {
		parts = append(parts, fmt.Sprintf("%s excels at %s tasks (%.0f%%)", agent.AgentType, bead.TaskType, capScore*100))
	} else if capScore >= 0.7 {
		parts = append(parts, fmt.Sprintf("%s is good at %s tasks (%.0f%%)", agent.AgentType, bead.TaskType, capScore*100))
	}

	// Add priority reasoning
	switch bead.Priority {
	case 0:
		parts = append(parts, "critical priority")
	case 1:
		parts = append(parts, "high priority")
	}

	// Add context usage if significant
	if agent.ContextUsage >= 0.5 {
		parts = append(parts, fmt.Sprintf("context %.0f%% used", agent.ContextUsage*100))
	}

	// Add strategy note
	parts = append(parts, strategyNote)

	if len(parts) == 0 {
		return "available agent matched to available work"
	}

	return strings.Join(parts, "; ")
}

// AssignTasksFunc is the function signature matching the bead requirements.
// This is a convenience wrapper around Matcher.AssignTasks.
func AssignTasksFunc(beads []Bead, agents []Agent, strategy string) []Assignment {
	m := NewMatcher()
	return m.AssignTasks(beads, agents, ParseStrategy(strategy))
}

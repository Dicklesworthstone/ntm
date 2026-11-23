package assign

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// LegacyPlanBead is the normalized bead input for non-allocation strategies
// (round-robin, quality, speed, dependency, and legacy balanced fallback).
type LegacyPlanBead struct {
	ID       string
	Title    string
	Priority string
}

// LegacyPlanAgent is the normalized pane candidate for legacy strategy planning.
type LegacyPlanAgent struct {
	Pane      tmux.Pane
	AgentType string
	AgentName string
}

// LegacyPlanOptions configures PlanLegacyStrategy.
type LegacyPlanOptions struct {
	Strategy   string
	AssignedAt string
	Session    string
	// InitialCounts and InitialLastAssigned optionally seed balanced load
	// tracking from active assignments in the session store.
	InitialCounts       map[string]int
	InitialLastAssigned map[string]time.Time
}

// LegacyPlanItem is one planned assignment emitted by PlanLegacyStrategy.
type LegacyPlanItem struct {
	BeadID     string
	BeadTitle  string
	Pane       int
	PaneTarget string
	PaneID     string
	AgentType  string
	AgentName  string
	Status     string
	AssignedAt string
	Score      float64
	Reasoning  string
}

// DetectAgentTypeFromTitle determines the canonical CLI agent type from a tmux
// pane title.
func DetectAgentTypeFromTitle(title string) string {
	title = strings.ToLower(title)

	if idx := strings.LastIndex(title, "__"); idx >= 0 && idx+2 < len(title) {
		typePart := title[idx+2:]
		if underscore := strings.IndexByte(typePart, '_'); underscore >= 0 {
			typePart = typePart[:underscore]
		}
		switch agent.AgentType(typePart).Canonical() {
		case agent.AgentTypeClaudeCode:
			return "claude"
		case agent.AgentTypeCodex:
			return "codex"
		case agent.AgentTypeGemini:
			return "gemini"
		case agent.AgentTypeAntigravity:
			return "antigravity"
		case agent.AgentTypeCursor:
			return "cursor"
		case agent.AgentTypeWindsurf:
			return "windsurf"
		case agent.AgentTypeAider:
			return "aider"
		case agent.AgentTypeOpencode:
			return "oc"
		case agent.AgentTypeGrok:
			return "grok"
		case agent.AgentTypeOmp:
			return "omp"
		case agent.AgentTypePi:
			return "pi"
		case agent.AgentTypeOllama:
			return "ollama"
		case agent.AgentTypeUser:
			return "user"
		}
	}

	switch {
	case strings.Contains(title, "claude"):
		return "claude"
	case strings.Contains(title, "codex"):
		return "codex"
	case strings.Contains(title, "antigravity"):
		return "antigravity"
	case strings.Contains(title, "gemini"):
		return "gemini"
	case strings.Contains(title, "grok"):
		return "grok"
	case strings.Contains(title, "oh-my-pi") || strings.Contains(title, "__omp_") || strings.HasSuffix(title, "__omp") || strings.HasPrefix(title, "omp_") || title == "omp":
		return "omp"
	case strings.Contains(title, "cursor"):
		return "cursor"
	case strings.Contains(title, "windsurf"):
		return "windsurf"
	case strings.Contains(title, "aider"):
		return "aider"
	case strings.Contains(title, "opencode"):
		return "oc"
	case strings.Contains(title, "ollama"):
		return "ollama"
	case strings.Contains(title, "__pi_") || strings.HasSuffix(title, "__pi") || strings.HasPrefix(title, "pi_") || title == "pi":
		return "pi"
	case strings.Contains(title, "user"):
		return "user"
	}
	return "unknown"
}

// DetectModelFromTitle extracts a known model variant token from a pane title.
func DetectModelFromTitle(title string) string {
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

// ParsePriorityString converts "P0"-"P4" to an integer priority, defaulting to 2.
func ParsePriorityString(p string) int {
	if len(p) == 2 && p[0] == 'P' {
		if n := p[1] - '0'; n <= 4 {
			return int(n)
		}
	}
	return 2
}

// InferTaskType infers a canonical TaskType from a bead title.
func InferTaskType(title string) TaskType {
	lower := strings.ToLower(title)
	rules := []struct {
		typ TaskType
		kws []string
	}{
		{TaskBug, []string{"bug", "fix", "broken", "error", "crash"}},
		{TaskTesting, []string{"test", "spec", "coverage"}},
		{TaskDocumentation, []string{"doc", "readme", "comment"}},
		{TaskRefactor, []string{"refactor", "cleanup", "improve"}},
		{TaskAnalysis, []string{"analyze", "investigate", "research"}},
		{TaskFeature, []string{"feature", "implement", "add", "new"}},
	}
	for _, r := range rules {
		for _, kw := range r.kws {
			if strings.Contains(lower, kw) {
				return r.typ
			}
		}
	}
	return TaskTask
}

// CalculateMatchConfidence scores how well agentType matches a bead under the
// requested assignment strategy.
func CalculateMatchConfidence(agentType, beadTitle, priority, strategy string) float64 {
	baseConfidence := 0.7
	taskType := string(InferTaskType(beadTitle))

	strengths := map[string]map[string]float64{
		"claude": {"analysis": 0.9, "refactor": 0.9, "documentation": 0.8, "feature": 0.8, "bug": 0.7},
		"codex":  {"feature": 0.9, "bug": 0.8, "task": 0.8, "refactor": 0.6},
		"gemini": {"documentation": 0.9, "analysis": 0.8, "feature": 0.8},
		"omp":    {"analysis": 0.8, "refactor": 0.75, "documentation": 0.75, "feature": 0.85, "bug": 0.75, "task": 0.75},
	}

	if agentStrengths, ok := strengths[agentType]; ok {
		if strength, ok := agentStrengths[taskType]; ok {
			baseConfidence = strength
		}
	}

	switch strategy {
	case "speed":
		baseConfidence = (baseConfidence + 0.9) / 2
	case "dependency":
		if ParsePriorityString(priority) <= 1 {
			baseConfidence = min(baseConfidence+0.1, 0.95)
		}
	}

	return baseConfidence
}

// BuildReasoning constructs a concise human-readable explanation for pairing an
// agent type with a bead under the given strategy.
func BuildReasoning(agentType, beadTitle, priorityStr, strategy string) string {
	var reasons []string

	title := strings.ToLower(beadTitle)
	priority := ParsePriorityString(priorityStr)

	if agentType == "claude" && (strings.Contains(title, "refactor") || strings.Contains(title, "analyze")) {
		reasons = append(reasons, "Claude excels at analysis/refactoring")
	} else if agentType == "codex" && (strings.Contains(title, "feature") || strings.Contains(title, "implement")) {
		reasons = append(reasons, "Codex excels at implementations")
	} else if agentType == "gemini" && strings.Contains(title, "doc") {
		reasons = append(reasons, "Gemini excels at documentation")
	}

	switch priority {
	case 0:
		reasons = append(reasons, "critical priority")
	case 1:
		reasons = append(reasons, "high priority")
	}

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

// EstimateBeadResourceCost estimates normalized [0,1] compute cost for a bead.
func EstimateBeadResourceCost(title string, taskType TaskType) float64 {
	lower := strings.ToLower(title)
	if strings.Contains(lower, "performance") ||
		strings.Contains(lower, "load") ||
		strings.Contains(lower, "large") ||
		strings.Contains(lower, "swarm") ||
		strings.Contains(lower, "benchmark") {
		return 0.80
	}
	switch taskType {
	case TaskBug, TaskDocs, TaskDocumentation, TaskTesting, TaskChore:
		return 0.35
	case TaskFeature, TaskRefactor, TaskAnalysis:
		return 0.65
	default:
		return 0.50
	}
}

// EstimateAgentResourceHeadroom computes normalized [0,1] headroom from context
// usage, scrollback backlog, and active assignments unless explicitHeadroom is set.
func EstimateAgentResourceHeadroom(explicitHeadroom, contextUsage float64, scrollback string, activeAssignments int) float64 {
	if explicitHeadroom > 0 {
		return ClampScore(explicitHeadroom)
	}
	contextPenalty := ClampScore(contextUsage) * 0.35
	backlogPenalty := ScrollbackBacklog(scrollback) * 0.25
	activePenalty := min(float64(max(activeAssignments, 0))*0.20, 0.60)
	return ClampScore(1.0 - contextPenalty - backlogPenalty - activePenalty)
}

// ScrollbackBacklog estimates normalized [0,1] pane scrollback pressure.
func ScrollbackBacklog(scrollback string) float64 {
	if scrollback == "" {
		return 0
	}
	linePressure := min(float64(strings.Count(scrollback, "\n"))/800.0, 1.0)
	bytePressure := min(float64(len(scrollback))/200000.0, 1.0)
	return max(linePressure, bytePressure)
}

// PressureAgentHeadroom translates a system pressure level string to an agent
// admission headroom count.
func PressureAgentHeadroom(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "critical":
		return 0
	case "high":
		return 1
	case "elevated":
		return 2
	default:
		return 8
	}
}

// ClampScore constrains score to [0, 1].
func ClampScore(score float64) float64 {
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

// PlanLegacyStrategy plans assignments using the explicit strategy selector
// (round-robin, quality, speed, dependency, or balanced fallback).
func PlanLegacyStrategy(agents []LegacyPlanAgent, beads []LegacyPlanBead, opts LegacyPlanOptions) []LegacyPlanItem {
	var items []LegacyPlanItem
	assignedAt := opts.AssignedAt
	if assignedAt == "" {
		assignedAt = time.Now().UTC().Format(time.RFC3339)
	}
	defaultStatus := string(assignment.StatusAssigned)

	switch strings.ToLower(opts.Strategy) {
	case "round-robin":
		for i, bead := range beads {
			if len(agents) == 0 {
				break
			}
			ag := agents[i%len(agents)]
			items = append(items, LegacyPlanItem{
				BeadID:     bead.ID,
				BeadTitle:  bead.Title,
				Pane:       ag.Pane.Index,
				PaneTarget: ag.Pane.Ref().Physical(),
				PaneID:     ag.Pane.ID,
				AgentType:  ag.AgentType,
				AgentName:  ag.AgentName,
				Status:     defaultStatus,
				AssignedAt: assignedAt,
				Score:      1.0,
				Reasoning:  fmt.Sprintf("round-robin slot %d → agent %d", i+1, i%len(agents)),
			})
		}

	case "quality":
		usedAgents := make(map[string]bool)
		for _, bead := range beads {
			var bestAgent *LegacyPlanAgent
			var bestScore float64

			for i := range agents {
				if usedAgents[agents[i].Pane.Ref().StableKey()] {
					continue
				}
				score := GetAgentScoreByString(agents[i].AgentType, string(InferTaskType(bead.Title)))
				if score > bestScore {
					bestScore = score
					bestAgent = &agents[i]
				}
			}

			if bestAgent != nil {
				items = append(items, LegacyPlanItem{
					BeadID:     bead.ID,
					BeadTitle:  bead.Title,
					Pane:       bestAgent.Pane.Index,
					PaneTarget: bestAgent.Pane.Ref().Physical(),
					PaneID:     bestAgent.Pane.ID,
					AgentType:  bestAgent.AgentType,
					AgentName:  bestAgent.AgentName,
					Status:     defaultStatus,
					AssignedAt: assignedAt,
					Score:      bestScore,
					Reasoning:  BuildReasoning(bestAgent.AgentType, bead.Title, bead.Priority, "quality"),
				})
				usedAgents[bestAgent.Pane.Ref().StableKey()] = true
			}
		}

	case "speed":
		usedAgents := make(map[string]bool)
		for _, bead := range beads {
			for i := range agents {
				if usedAgents[agents[i].Pane.Ref().StableKey()] {
					continue
				}
				score := CalculateMatchConfidence(agents[i].AgentType, bead.Title, bead.Priority, "speed")
				items = append(items, LegacyPlanItem{
					BeadID:     bead.ID,
					BeadTitle:  bead.Title,
					Pane:       agents[i].Pane.Index,
					PaneTarget: agents[i].Pane.Ref().Physical(),
					PaneID:     agents[i].Pane.ID,
					AgentType:  agents[i].AgentType,
					AgentName:  agents[i].AgentName,
					Status:     defaultStatus,
					AssignedAt: assignedAt,
					Score:      score,
					Reasoning:  BuildReasoning(agents[i].AgentType, bead.Title, bead.Priority, "speed"),
				})
				usedAgents[agents[i].Pane.Ref().StableKey()] = true
				break
			}
		}

	case "dependency":
		usedAgents := make(map[string]bool)
		for _, bead := range beads {
			var bestAgent *LegacyPlanAgent
			var bestScore float64

			for i := range agents {
				if usedAgents[agents[i].Pane.Ref().StableKey()] {
					continue
				}
				score := CalculateMatchConfidence(agents[i].AgentType, bead.Title, bead.Priority, "dependency")
				if score > bestScore {
					bestScore = score
					bestAgent = &agents[i]
				}
			}

			if bestAgent != nil {
				items = append(items, LegacyPlanItem{
					BeadID:     bead.ID,
					BeadTitle:  bead.Title,
					Pane:       bestAgent.Pane.Index,
					PaneTarget: bestAgent.Pane.Ref().Physical(),
					PaneID:     bestAgent.Pane.ID,
					AgentType:  bestAgent.AgentType,
					AgentName:  bestAgent.AgentName,
					Status:     defaultStatus,
					AssignedAt: assignedAt,
					Score:      bestScore,
					Reasoning:  BuildReasoning(bestAgent.AgentType, bead.Title, bead.Priority, "dependency"),
				})
				usedAgents[bestAgent.Pane.Ref().StableKey()] = true
			}
		}

	default: // balanced
		agentAssignCounts := make(map[string]int, len(opts.InitialCounts))
		for k, v := range opts.InitialCounts {
			agentAssignCounts[k] = v
		}
		agentLastAssigned := make(map[string]time.Time, len(opts.InitialLastAssigned))
		for k, v := range opts.InitialLastAssigned {
			agentLastAssigned[k] = v
		}

		for _, bead := range beads {
			var bestAgent *LegacyPlanAgent
			var bestScore float64
			minAssigns := int(^uint(0) >> 1)
			var leastRecentTime time.Time

			for i := range agents {
				paneKey := agents[i].Pane.Ref().StableKey()
				count := agentAssignCounts[paneKey]
				score := CalculateMatchConfidence(agents[i].AgentType, bead.Title, bead.Priority, "balanced")
				lastAssign := agentLastAssigned[paneKey]

				shouldPick := false
				if count < minAssigns {
					shouldPick = true
				} else if count == minAssigns {
					if score > bestScore {
						shouldPick = true
					} else if score == bestScore {
						if bestAgent == nil {
							shouldPick = true
						} else if lastAssign.IsZero() && !leastRecentTime.IsZero() {
							shouldPick = true
						} else if !lastAssign.IsZero() && !leastRecentTime.IsZero() && lastAssign.Before(leastRecentTime) {
							shouldPick = true
						} else if lastAssign.Equal(leastRecentTime) && agents[i].Pane.Ref().Physical() < bestAgent.Pane.Ref().Physical() {
							shouldPick = true
						}
					}
				}

				if shouldPick {
					minAssigns = count
					bestScore = score
					bestAgent = &agents[i]
					leastRecentTime = lastAssign
				}
			}

			if bestAgent != nil {
				items = append(items, LegacyPlanItem{
					BeadID:     bead.ID,
					BeadTitle:  bead.Title,
					Pane:       bestAgent.Pane.Index,
					PaneTarget: bestAgent.Pane.Ref().Physical(),
					PaneID:     bestAgent.Pane.ID,
					AgentType:  bestAgent.AgentType,
					AgentName:  bestAgent.AgentName,
					Status:     defaultStatus,
					AssignedAt: assignedAt,
					Score:      bestScore,
					Reasoning:  BuildReasoning(bestAgent.AgentType, bead.Title, bead.Priority, "balanced"),
				})
				bestKey := bestAgent.Pane.Ref().StableKey()
				agentAssignCounts[bestKey]++
				agentLastAssigned[bestKey] = time.Now()
			}
		}
	}

	return items
}

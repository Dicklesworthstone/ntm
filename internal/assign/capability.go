// Package assign implements intelligent work assignment for multi-agent workflows.
package assign

import (
	"strings"
	"sync"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TaskType represents categories of work for capability matching.
type TaskType string

const (
	TaskRefactor      TaskType = "refactor"
	TaskAnalysis      TaskType = "analysis"
	TaskDocs          TaskType = "docs"
	TaskDocumentation TaskType = "documentation" // alias for docs
	TaskBug           TaskType = "bug"
	TaskFeature       TaskType = "feature"
	TaskTesting       TaskType = "testing"
	TaskTask          TaskType = "task"
	TaskChore         TaskType = "chore"
	TaskEpic          TaskType = "epic"
)

// DefaultCapabilities defines baseline scores for agent/task combinations.
// Scores range from 0.0 (unsuitable) to 1.0 (optimal).
var DefaultCapabilities = map[tmux.AgentType]map[TaskType]float64{
	tmux.AgentClaude: {
		TaskRefactor:      0.95, // Excellent at large-scale refactoring
		TaskAnalysis:      0.90, // Strong code analysis and architecture
		TaskDocs:          0.85, // Good documentation
		TaskDocumentation: 0.85,
		TaskBug:           0.80,
		TaskFeature:       0.85,
		TaskTesting:       0.75,
		TaskTask:          0.80,
		TaskChore:         0.70,
		TaskEpic:          0.90, // Good at epic-level planning
	},
	tmux.AgentCodex: {
		TaskRefactor:      0.75,
		TaskAnalysis:      0.70,
		TaskDocs:          0.70,
		TaskDocumentation: 0.70,
		TaskBug:           0.90, // Excellent at bug fixes
		TaskFeature:       0.90, // Excellent at feature implementation
		TaskTesting:       0.85,
		TaskTask:          0.85,
		TaskChore:         0.80,
		TaskEpic:          0.60,
	},
	tmux.AgentGemini: {
		TaskRefactor:      0.75,
		TaskAnalysis:      0.85, // Strong analysis
		TaskDocs:          0.90, // Excellent documentation
		TaskDocumentation: 0.90,
		TaskBug:           0.75,
		TaskFeature:       0.80,
		TaskTesting:       0.80,
		TaskTask:          0.75,
		TaskChore:         0.75,
		TaskEpic:          0.75,
	},
}

// CapabilityMatrix manages agent capability scores with support for
// configuration overrides and learned adjustments.
type CapabilityMatrix struct {
	mu        sync.RWMutex
	base      map[tmux.AgentType]map[TaskType]float64
	overrides map[tmux.AgentType]map[TaskType]float64
	learned   map[tmux.AgentType]map[TaskType]float64
}

// NewCapabilityMatrix creates a new matrix initialized with default capabilities.
func NewCapabilityMatrix() *CapabilityMatrix {
	m := &CapabilityMatrix{
		base:      make(map[tmux.AgentType]map[TaskType]float64),
		overrides: make(map[tmux.AgentType]map[TaskType]float64),
		learned:   make(map[tmux.AgentType]map[TaskType]float64),
	}

	// Deep copy defaults
	for agent, tasks := range DefaultCapabilities {
		m.base[agent] = make(map[TaskType]float64)
		for task, score := range tasks {
			m.base[agent][task] = score
		}
	}

	return m
}

// GetScore returns the effective score for an agent/task combination.
// Priority: learned > overrides > base > 0.5 (default)
func (m *CapabilityMatrix) GetScore(agentType tmux.AgentType, taskType TaskType) float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Check learned scores first
	if tasks, ok := m.learned[agentType]; ok {
		if score, ok := tasks[taskType]; ok {
			return score
		}
	}

	// Check overrides
	if tasks, ok := m.overrides[agentType]; ok {
		if score, ok := tasks[taskType]; ok {
			return score
		}
	}

	// Check base
	if tasks, ok := m.base[agentType]; ok {
		if score, ok := tasks[taskType]; ok {
			return score
		}
	}

	return 0.5 // Default for unknown combinations
}

// SetOverride sets a configuration override for a specific agent/task.
func (m *CapabilityMatrix) SetOverride(agentType tmux.AgentType, taskType TaskType, score float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.overrides[agentType] == nil {
		m.overrides[agentType] = make(map[TaskType]float64)
	}
	m.overrides[agentType][taskType] = clampScore(score)
}

// SetLearned sets a learned score adjustment based on agent performance.
func (m *CapabilityMatrix) SetLearned(agentType tmux.AgentType, taskType TaskType, score float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.learned[agentType] == nil {
		m.learned[agentType] = make(map[TaskType]float64)
	}
	m.learned[agentType][taskType] = clampScore(score)
}

// ClearLearned removes all learned scores.
func (m *CapabilityMatrix) ClearLearned() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.learned = make(map[tmux.AgentType]map[TaskType]float64)
}

// ClearOverrides removes all configuration overrides.
func (m *CapabilityMatrix) ClearOverrides() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.overrides = make(map[tmux.AgentType]map[TaskType]float64)
}

// clampScore ensures score is within [0.0, 1.0].
func clampScore(s float64) float64 {
	if s < 0.0 {
		return 0.0
	}
	if s > 1.0 {
		return 1.0
	}
	return s
}

// Global matrix instance for convenience functions.
var globalMatrix = NewCapabilityMatrix()

// GetAgentScore returns the score for a given agent type and task type
// using the global capability matrix.
func GetAgentScore(agentType tmux.AgentType, taskType TaskType) float64 {
	return globalMatrix.GetScore(agentType, taskType)
}

// GetAgentScoreByString returns the score using string identifiers,
// useful for integration with existing code that uses strings.
func GetAgentScoreByString(agentType string, taskType string) float64 {
	return globalMatrix.GetScore(ParseAgentType(agentType), ParseTaskType(taskType))
}

// ParseAgentType converts a string to AgentType.
// Supports both short codes (cc, cod, gmi) and full names (claude, codex, gemini).
func ParseAgentType(s string) tmux.AgentType {
	s = strings.ToLower(s)
	switch s {
	case "cc", "claude":
		return tmux.AgentClaude
	case "cod", "codex":
		return tmux.AgentCodex
	case "gmi", "gemini":
		return tmux.AgentGemini
	default:
		return tmux.AgentType(s)
	}
}

// ParseTaskType converts a string to TaskType.
func ParseTaskType(s string) TaskType {
	s = strings.ToLower(s)
	switch s {
	case "refactor", "refactoring":
		return TaskRefactor
	case "analysis", "analyze", "investigate", "research", "design":
		return TaskAnalysis
	case "docs", "doc", "documentation", "readme", "comment":
		return TaskDocs
	case "bug", "fix", "broken", "error", "crash":
		return TaskBug
	case "feature", "implement", "add", "new":
		return TaskFeature
	case "test", "testing", "spec", "coverage":
		return TaskTesting
	case "chore":
		return TaskChore
	case "epic":
		return TaskEpic
	default:
		return TaskTask
	}
}

// GlobalMatrix returns the global capability matrix for advanced configuration.
func GlobalMatrix() *CapabilityMatrix {
	return globalMatrix
}

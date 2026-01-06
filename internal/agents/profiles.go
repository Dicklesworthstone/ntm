// Package agents provides agent capability profiles and matching for work distribution.
// This package enables intelligent routing of tasks to agents based on their capabilities,
// specializations, and historical performance.
package agents

import (
	"math"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AgentType represents the type of AI coding agent.
type AgentType string

const (
	AgentTypeClaude AgentType = "claude"
	AgentTypeCodex  AgentType = "codex"
	AgentTypeGemini AgentType = "gemini"
)

// Specialization represents task types an agent excels at.
type Specialization string

const (
	SpecComplex       Specialization = "complex"       // Multi-step reasoning
	SpecArchitecture  Specialization = "architecture"  // System design
	SpecRefactorLarge Specialization = "refactor-large" // Large-scale refactoring
	SpecQuick         Specialization = "quick"         // Fast, focused tasks
	SpecRefactorSmall Specialization = "refactor-small" // Small refactoring
	SpecTests         Specialization = "tests"         // Test writing
	SpecResearch      Specialization = "research"      // Investigation tasks
	SpecDocs          Specialization = "docs"          // Documentation
	SpecAnalysis      Specialization = "analysis"      // Code analysis
	SpecDebug         Specialization = "debug"         // Debugging
)

// AgentProfile defines the static capabilities and preferences of an agent type.
type AgentProfile struct {
	Type            AgentType        `json:"type"`            // claude, codex, gemini
	Model           string           `json:"model"`           // opus-4.5, gpt-5, gemini-ultra
	ContextBudget   int              `json:"context_budget"`  // Max tokens
	Specializations []Specialization `json:"specializations"` // Task types agent excels at
	Preferences     Preferences      `json:"preferences"`
	Performance     Performance      `json:"performance"`
}

// Preferences defines file and label preferences for an agent.
type Preferences struct {
	PreferredFiles  []string `json:"preferred_files"`  // Glob patterns agent excels at
	AvoidFiles      []string `json:"avoid_files"`      // Glob patterns agent struggles with
	PreferredLabels []string `json:"preferred_labels"` // Bead labels agent prefers
}

// Performance tracks historical performance metrics for an agent.
type Performance struct {
	AvgCompletionTime time.Duration `json:"avg_completion_time"`
	SuccessRate       float64       `json:"success_rate"`    // 0.0 to 1.0
	TasksCompleted    int           `json:"tasks_completed"`
	LastUpdated       time.Time     `json:"last_updated"`
}

// ProfileMatcher matches tasks to the best available agents based on capabilities.
type ProfileMatcher struct {
	profiles map[AgentType]*AgentProfile
	mu       sync.RWMutex
}

// NewProfileMatcher creates a new ProfileMatcher with default profiles.
func NewProfileMatcher() *ProfileMatcher {
	pm := &ProfileMatcher{
		profiles: make(map[AgentType]*AgentProfile),
	}
	pm.loadDefaults()
	return pm
}

// loadDefaults initializes the default agent profiles.
func (pm *ProfileMatcher) loadDefaults() {
	pm.profiles[AgentTypeClaude] = &AgentProfile{
		Type:          AgentTypeClaude,
		Model:         "opus-4.5",
		ContextBudget: 180000,
		Specializations: []Specialization{
			SpecComplex,
			SpecArchitecture,
			SpecRefactorLarge,
			SpecDebug,
		},
		Preferences: Preferences{
			PreferredFiles:  []string{"internal/**/*.go", "cmd/**/*.go", "pkg/**/*.go"},
			AvoidFiles:      []string{"*.md", "docs/**", "*.json"},
			PreferredLabels: []string{"epic", "feature", "critical", "P0", "P1"},
		},
		Performance: Performance{
			SuccessRate: 0.9, // Default assumption
		},
	}

	pm.profiles[AgentTypeCodex] = &AgentProfile{
		Type:          AgentTypeCodex,
		Model:         "gpt-5-codex",
		ContextBudget: 120000,
		Specializations: []Specialization{
			SpecQuick,
			SpecRefactorSmall,
			SpecTests,
		},
		Preferences: Preferences{
			PreferredFiles:  []string{"*_test.go", "test/**", "tests/**"},
			AvoidFiles:      []string{"docs/**", "*.md"},
			PreferredLabels: []string{"task", "bug", "test", "P2", "P3"},
		},
		Performance: Performance{
			SuccessRate: 0.85,
		},
	}

	pm.profiles[AgentTypeGemini] = &AgentProfile{
		Type:          AgentTypeGemini,
		Model:         "gemini-ultra",
		ContextBudget: 100000,
		Specializations: []Specialization{
			SpecResearch,
			SpecDocs,
			SpecAnalysis,
		},
		Preferences: Preferences{
			PreferredFiles:  []string{"*.md", "docs/**", "README*", "*.txt"},
			AvoidFiles:      []string{"*_test.go"},
			PreferredLabels: []string{"docs", "research", "spike", "chore"},
		},
		Performance: Performance{
			SuccessRate: 0.85,
		},
	}
}

// GetProfile returns a copy of the profile for an agent type.
// Returns a copy to prevent callers from modifying internal state.
func (pm *ProfileMatcher) GetProfile(agentType AgentType) *AgentProfile {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	p := pm.profiles[agentType]
	if p == nil {
		return nil
	}
	return p.copy()
}

// GetProfileByName returns the profile for an agent type string.
func (pm *ProfileMatcher) GetProfileByName(name string) *AgentProfile {
	normalized := NormalizeAgentType(name)
	return pm.GetProfile(AgentType(normalized))
}

// AllProfiles returns copies of all registered profiles.
// Returns copies to prevent callers from modifying internal state.
func (pm *ProfileMatcher) AllProfiles() []*AgentProfile {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	profiles := make([]*AgentProfile, 0, len(pm.profiles))
	for _, p := range pm.profiles {
		profiles = append(profiles, p.copy())
	}
	return profiles
}

// copy creates a deep copy of an AgentProfile.
func (p *AgentProfile) copy() *AgentProfile {
	if p == nil {
		return nil
	}
	cp := *p // Shallow copy

	// Deep copy slices
	if p.Specializations != nil {
		cp.Specializations = make([]Specialization, len(p.Specializations))
		copy(cp.Specializations, p.Specializations)
	}
	if p.Preferences.PreferredFiles != nil {
		cp.Preferences.PreferredFiles = make([]string, len(p.Preferences.PreferredFiles))
		copy(cp.Preferences.PreferredFiles, p.Preferences.PreferredFiles)
	}
	if p.Preferences.AvoidFiles != nil {
		cp.Preferences.AvoidFiles = make([]string, len(p.Preferences.AvoidFiles))
		copy(cp.Preferences.AvoidFiles, p.Preferences.AvoidFiles)
	}
	if p.Preferences.PreferredLabels != nil {
		cp.Preferences.PreferredLabels = make([]string, len(p.Preferences.PreferredLabels))
		copy(cp.Preferences.PreferredLabels, p.Preferences.PreferredLabels)
	}

	return &cp
}

// TaskInfo represents information about a task for scoring purposes.
type TaskInfo struct {
	Title           string   // Task title/description
	Type            string   // epic, feature, task, bug, chore
	Priority        int      // 0-4
	Labels          []string // Associated labels
	EstimatedTokens int      // Estimated context tokens needed
	AffectedFiles   []string // Predicted files that will be modified
}

// ScoreResult contains the scoring breakdown for an agent-task pair.
type ScoreResult struct {
	AgentType         AgentType `json:"agent_type"`
	Score             float64   `json:"score"`
	CanHandle         bool      `json:"can_handle"` // Context budget allows
	SpecializationHit bool      `json:"specialization_hit"`
	FileMatchScore    float64   `json:"file_match_score"`
	LabelMatchScore   float64   `json:"label_match_score"`
	PerformanceBonus  float64   `json:"performance_bonus"`
	Reason            string    `json:"reason,omitempty"`
}

// ScoreAssignment calculates how well an agent matches a task.
// Returns a score where higher is better, or 0 if the agent cannot handle the task.
func (pm *ProfileMatcher) ScoreAssignment(agentType AgentType, task TaskInfo) ScoreResult {
	pm.mu.RLock()
	profile := pm.profiles[agentType]
	pm.mu.RUnlock()

	if profile == nil {
		return ScoreResult{
			AgentType: agentType,
			Score:     0,
			CanHandle: false,
			Reason:    "unknown agent type",
		}
	}

	result := ScoreResult{
		AgentType: agentType,
		CanHandle: true,
	}

	// 1. Context budget check (hard constraint)
	if task.EstimatedTokens > 0 && task.EstimatedTokens > profile.ContextBudget {
		result.CanHandle = false
		result.Score = 0
		result.Reason = "task exceeds context budget"
		return result
	}

	score := 1.0

	// 2. Specialization match (1.5x multiplier per match)
	for _, spec := range profile.Specializations {
		if taskMatchesSpecialization(task, spec) {
			score *= 1.5
			result.SpecializationHit = true
		}
	}

	// 3. File preference match
	fileScore := pm.calculateFileScore(profile, task.AffectedFiles)
	result.FileMatchScore = fileScore
	score *= fileScore

	// 4. Label match (1.3x multiplier per match)
	labelScore := pm.calculateLabelScore(profile, task.Labels)
	result.LabelMatchScore = labelScore
	score *= labelScore

	// 5. Historical performance bonus
	if profile.Performance.SuccessRate > 0.9 {
		score *= 1.1
		result.PerformanceBonus = 0.1
	} else if profile.Performance.SuccessRate < 0.7 {
		score *= 0.9 // Slight penalty for low success rate
		result.PerformanceBonus = -0.1
	}

	result.Score = math.Round(score*100) / 100
	return result
}

// taskMatchesSpecialization checks if a task matches a specialization.
func taskMatchesSpecialization(task TaskInfo, spec Specialization) bool {
	titleLower := strings.ToLower(task.Title)
	typeLower := strings.ToLower(task.Type)

	switch spec {
	case SpecComplex:
		return typeLower == "epic" || strings.Contains(titleLower, "implement") ||
			strings.Contains(titleLower, "architect") || strings.Contains(titleLower, "design")
	case SpecArchitecture:
		return strings.Contains(titleLower, "architect") || strings.Contains(titleLower, "design") ||
			strings.Contains(titleLower, "structure") || strings.Contains(titleLower, "refactor")
	case SpecRefactorLarge:
		return strings.Contains(titleLower, "refactor") && typeLower == "epic"
	case SpecQuick:
		return typeLower == "task" || typeLower == "bug" || task.Priority >= 3
	case SpecRefactorSmall:
		return strings.Contains(titleLower, "refactor") && typeLower != "epic"
	case SpecTests:
		return strings.Contains(titleLower, "test") || typeLower == "test"
	case SpecResearch:
		return strings.Contains(titleLower, "research") || strings.Contains(titleLower, "investigate") ||
			strings.Contains(titleLower, "spike") || strings.Contains(titleLower, "explore")
	case SpecDocs:
		return strings.Contains(titleLower, "doc") || strings.Contains(titleLower, "readme") ||
			typeLower == "docs"
	case SpecAnalysis:
		return strings.Contains(titleLower, "analy") || strings.Contains(titleLower, "review") ||
			strings.Contains(titleLower, "audit")
	case SpecDebug:
		return strings.Contains(titleLower, "debug") || strings.Contains(titleLower, "fix") ||
			typeLower == "bug"
	}
	return false
}

// calculateFileScore computes a score based on file pattern matching.
func (pm *ProfileMatcher) calculateFileScore(profile *AgentProfile, files []string) float64 {
	if len(files) == 0 {
		return 1.0 // Neutral if no files specified
	}

	score := 1.0
	preferredMatches := 0
	avoidMatches := 0

	for _, file := range files {
		// Check preferred patterns
		for _, pattern := range profile.Preferences.PreferredFiles {
			if matchGlobPattern(file, pattern) {
				preferredMatches++
				break
			}
		}
		// Check avoid patterns
		for _, pattern := range profile.Preferences.AvoidFiles {
			if matchGlobPattern(file, pattern) {
				avoidMatches++
				break
			}
		}
	}

	// Boost for preferred files (up to 1.5x)
	if preferredMatches > 0 {
		boost := 1.0 + float64(preferredMatches)*0.1
		if boost > 1.5 {
			boost = 1.5
		}
		score *= boost
	}

	// Penalty for avoided files (down to 0.5x)
	if avoidMatches > 0 {
		penalty := 1.0 - float64(avoidMatches)*0.15
		if penalty < 0.5 {
			penalty = 0.5
		}
		score *= penalty
	}

	return score
}

// calculateLabelScore computes a score based on label matching.
func (pm *ProfileMatcher) calculateLabelScore(profile *AgentProfile, labels []string) float64 {
	if len(labels) == 0 || len(profile.Preferences.PreferredLabels) == 0 {
		return 1.0 // Neutral if no labels
	}

	score := 1.0
	for _, label := range labels {
		labelLower := strings.ToLower(label)
		for _, preferred := range profile.Preferences.PreferredLabels {
			if strings.EqualFold(label, preferred) || strings.Contains(labelLower, strings.ToLower(preferred)) {
				score *= 1.15 // 15% boost per label match
				break
			}
		}
	}

	// Cap at 2.0x
	if score > 2.0 {
		score = 2.0
	}
	return score
}

// matchGlobPattern performs glob matching with support for ** patterns.
func matchGlobPattern(path, pattern string) bool {
	// Handle **/*.ext patterns (match any path with the extension)
	if strings.HasPrefix(pattern, "**/") {
		suffix := strings.TrimPrefix(pattern, "**/")
		if strings.Contains(suffix, "*") {
			// Pattern like **/*.go - match any file with that extension
			matched, _ := filepath.Match(suffix, filepath.Base(path))
			return matched
		}
		// Pattern like **/foo - match if path contains or ends with foo
		return strings.HasSuffix(path, suffix) || strings.Contains(path, "/"+suffix)
	}

	// Handle prefix/**/*.ext patterns (e.g., internal/**/*.go)
	if strings.Contains(pattern, "/**/") {
		parts := strings.SplitN(pattern, "/**/", 2)
		prefix := parts[0]
		suffix := parts[1]
		if !strings.HasPrefix(path, prefix+"/") && path != prefix {
			return false
		}
		// Check if the remaining path matches the suffix
		remaining := strings.TrimPrefix(path, prefix+"/")
		if strings.Contains(suffix, "*") {
			matched, _ := filepath.Match(suffix, filepath.Base(remaining))
			return matched
		}
		return strings.HasSuffix(remaining, suffix)
	}

	// Handle suffix/** patterns (match any path under directory)
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return strings.HasPrefix(path, prefix+"/") || path == prefix
	}

	// Handle simple * patterns
	if strings.Contains(pattern, "*") {
		// Use filepath.Match for other patterns
		matched, _ := filepath.Match(pattern, filepath.Base(path))
		return matched
	}

	// Exact match
	return path == pattern || filepath.Base(path) == pattern
}

// RecommendAgent returns the best agent type for a task.
func (pm *ProfileMatcher) RecommendAgent(task TaskInfo) (AgentType, ScoreResult) {
	// Collect agent types under lock to avoid race condition during iteration
	pm.mu.RLock()
	agentTypes := make([]AgentType, 0, len(pm.profiles))
	for agentType := range pm.profiles {
		agentTypes = append(agentTypes, agentType)
	}
	pm.mu.RUnlock()

	var bestAgent AgentType
	var bestResult ScoreResult

	for _, agentType := range agentTypes {
		result := pm.ScoreAssignment(agentType, task)
		if result.CanHandle && result.Score > bestResult.Score {
			bestAgent = agentType
			bestResult = result
		}
	}

	if bestAgent == "" {
		// Default to Claude for complex/unknown tasks
		return AgentTypeClaude, pm.ScoreAssignment(AgentTypeClaude, task)
	}

	return bestAgent, bestResult
}

// RecordCompletion updates an agent's performance metrics after task completion.
func (pm *ProfileMatcher) RecordCompletion(agentType AgentType, success bool, duration time.Duration) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	profile := pm.profiles[agentType]
	if profile == nil {
		return
	}

	profile.Performance.TasksCompleted++

	// Exponential moving average for success rate
	alpha := 0.1
	if success {
		profile.Performance.SuccessRate = alpha + (1-alpha)*profile.Performance.SuccessRate
	} else {
		profile.Performance.SuccessRate = (1 - alpha) * profile.Performance.SuccessRate
	}

	// Running average for completion time
	if profile.Performance.AvgCompletionTime == 0 {
		profile.Performance.AvgCompletionTime = duration
	} else {
		profile.Performance.AvgCompletionTime = (profile.Performance.AvgCompletionTime + duration) / 2
	}

	profile.Performance.LastUpdated = time.Now()
}

// GetPerformanceStats returns performance statistics for all agents.
func (pm *ProfileMatcher) GetPerformanceStats() map[AgentType]Performance {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	stats := make(map[AgentType]Performance)
	for agentType, profile := range pm.profiles {
		stats[agentType] = profile.Performance
	}
	return stats
}

// NormalizeAgentType converts various agent type strings to canonical form.
func NormalizeAgentType(t string) string {
	switch strings.ToLower(t) {
	case "claude", "cc", "claude-code", "opus", "sonnet":
		return "claude"
	case "codex", "cod", "openai", "gpt", "gpt-5":
		return "codex"
	case "gemini", "gmi", "google", "gemini-ultra":
		return "gemini"
	default:
		return strings.ToLower(t)
	}
}

// ParseAgentType converts a string to AgentType.
func ParseAgentType(s string) AgentType {
	normalized := NormalizeAgentType(s)
	switch normalized {
	case "claude":
		return AgentTypeClaude
	case "codex":
		return AgentTypeCodex
	case "gemini":
		return AgentTypeGemini
	default:
		return AgentType(normalized)
	}
}

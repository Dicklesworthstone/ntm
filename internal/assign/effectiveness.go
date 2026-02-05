// Package assign implements intelligent work assignment for multi-agent workflows.
// effectiveness.go integrates historical effectiveness scores into assignment decisions.
package assign

import (
	"fmt"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/scoring"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// AssignmentMode controls how effectiveness scores influence assignments.
type AssignmentMode string

const (
	// ModeExploitation uses effectiveness scores to maximize expected performance.
	// Assigns tasks to agents with best historical performance.
	ModeExploitation AssignmentMode = "exploitation"

	// ModeLearning reduces reliance on effectiveness to explore new combinations.
	// Uses lower weight for effectiveness scores to allow capability discovery.
	ModeLearning AssignmentMode = "learning"

	// ModeBalanced uses moderate effectiveness weight for mixed exploration/exploitation.
	ModeBalanced AssignmentMode = "balanced"
)

// EffectivenessConfig controls effectiveness score integration.
type EffectivenessConfig struct {
	// Enabled controls whether effectiveness scores are used.
	Enabled bool `json:"enabled"`

	// Mode controls exploration vs exploitation tradeoff.
	Mode AssignmentMode `json:"mode"`

	// WindowDays is the lookback window for effectiveness scores.
	WindowDays int `json:"window_days"`

	// MinSamples is minimum historical scores needed to use effectiveness.
	MinSamples int `json:"min_samples"`

	// ExploitWeight is effectiveness weight in exploitation mode (0-1).
	ExploitWeight float64 `json:"exploit_weight"`

	// LearnWeight is effectiveness weight in learning mode (0-1).
	LearnWeight float64 `json:"learn_weight"`

	// BalancedWeight is effectiveness weight in balanced mode (0-1).
	BalancedWeight float64 `json:"balanced_weight"`
}

// DefaultEffectivenessConfig returns sensible defaults.
func DefaultEffectivenessConfig() *EffectivenessConfig {
	return &EffectivenessConfig{
		Enabled:        true,
		Mode:           ModeBalanced,
		WindowDays:     14,
		MinSamples:     3,
		ExploitWeight:  0.6,
		LearnWeight:    0.2,
		BalancedWeight: 0.4,
	}
}

// EffectivenessWeight returns the weight to apply to effectiveness scores
// based on the current mode.
func (c *EffectivenessConfig) EffectivenessWeight() float64 {
	if !c.Enabled {
		return 0
	}
	switch c.Mode {
	case ModeExploitation:
		return c.ExploitWeight
	case ModeLearning:
		return c.LearnWeight
	case ModeBalanced:
		return c.BalancedWeight
	default:
		return c.BalancedWeight
	}
}

// EffectivenessIntegrator integrates historical scores into assignment decisions.
type EffectivenessIntegrator struct {
	mu      sync.RWMutex
	config  *EffectivenessConfig
	tracker *scoring.Tracker
	matrix  *CapabilityMatrix
	cache   map[string]map[string]*scoring.AgentTaskEffectiveness
	cacheAt time.Time
}

// NewEffectivenessIntegrator creates an integrator with the given config.
func NewEffectivenessIntegrator(config *EffectivenessConfig) *EffectivenessIntegrator {
	if config == nil {
		config = DefaultEffectivenessConfig()
	}
	return &EffectivenessIntegrator{
		config:  config,
		tracker: scoring.DefaultTracker(),
		matrix:  GlobalMatrix(),
		cache:   make(map[string]map[string]*scoring.AgentTaskEffectiveness),
	}
}

// RefreshCapabilities updates the capability matrix with effectiveness scores.
// Should be called periodically or before assignment decisions.
func (ei *EffectivenessIntegrator) RefreshCapabilities() error {
	if !ei.config.Enabled {
		return nil
	}

	// Query all effectiveness scores
	scores, err := ei.tracker.QueryAllEffectiveness(ei.config.WindowDays)
	if err != nil {
		return fmt.Errorf("querying effectiveness: %w", err)
	}

	ei.mu.Lock()
	defer ei.mu.Unlock()

	// Update cache
	ei.cache = scores
	ei.cacheAt = time.Now()

	// Update capability matrix with learned scores
	weight := ei.config.EffectivenessWeight()
	for agentType, taskScores := range scores {
		for taskType, eff := range taskScores {
			if !eff.HasData || eff.SampleCount < ei.config.MinSamples {
				continue
			}

			// Blend effectiveness with base capability
			// learned = base * (1 - weight) + effectiveness * weight
			agent := ParseAgentType(agentType)
			task := ParseTaskType(taskType)
			base := ei.matrix.GetScore(agent, task)

			// Apply confidence adjustment
			// Lower confidence = less deviation from base
			adjustedWeight := weight * eff.Confidence
			learned := base*(1-adjustedWeight) + eff.Score*adjustedWeight

			ei.matrix.SetLearned(agent, task, learned)
		}
	}

	return nil
}

// GetEffectivenessScore returns the effectiveness score for an agent-task pair.
func (ei *EffectivenessIntegrator) GetEffectivenessScore(agentType, taskType string) (*scoring.AgentTaskEffectiveness, error) {
	ei.mu.RLock()

	// Check cache first
	if ei.cache != nil && time.Since(ei.cacheAt) < 5*time.Minute {
		if taskScores, ok := ei.cache[agentType]; ok {
			if eff, ok := taskScores[taskType]; ok {
				ei.mu.RUnlock()
				return eff, nil
			}
		}
	}
	ei.mu.RUnlock()

	// Query fresh if not in cache
	return ei.tracker.QueryEffectiveness(agentType, taskType, ei.config.WindowDays)
}

// GetEffectivenessBonus calculates an assignment bonus based on effectiveness.
// Returns a bonus value and explanation.
func (ei *EffectivenessIntegrator) GetEffectivenessBonus(agentType, taskType string) (float64, string) {
	if !ei.config.Enabled {
		return 0, "effectiveness scoring disabled"
	}

	eff, err := ei.GetEffectivenessScore(agentType, taskType)
	if err != nil || !eff.HasData {
		return 0, "insufficient historical data"
	}

	// Calculate bonus relative to baseline (0.5)
	// Score > 0.5 = positive bonus, Score < 0.5 = negative bonus
	baseline := 0.5
	bonus := (eff.Score - baseline) * ei.config.EffectivenessWeight()

	reason := fmt.Sprintf("effectiveness %.2f (%d samples, %.0f%% confidence, mode=%s)",
		eff.Score, eff.SampleCount, eff.Confidence*100, ei.config.Mode)

	return bonus, reason
}

// SetMode changes the assignment mode.
func (ei *EffectivenessIntegrator) SetMode(mode AssignmentMode) {
	ei.mu.Lock()
	defer ei.mu.Unlock()
	ei.config.Mode = mode
}

// GetMode returns the current assignment mode.
func (ei *EffectivenessIntegrator) GetMode() AssignmentMode {
	ei.mu.RLock()
	defer ei.mu.RUnlock()
	return ei.config.Mode
}

// EffectivenessRanking provides a ranked list of agents for a task type.
type EffectivenessRanking struct {
	TaskType string                   `json:"task_type"`
	Rankings []AgentEffectivenessRank `json:"rankings"`
	Mode     AssignmentMode           `json:"mode"`
	HasData  bool                     `json:"has_data"`
}

// AgentEffectivenessRank represents one agent's ranking for a task.
type AgentEffectivenessRank struct {
	AgentType   string  `json:"agent_type"`
	Score       float64 `json:"score"`      // Combined score (base + effectiveness)
	BaseScore   float64 `json:"base_score"` // Static capability score
	EffScore    float64 `json:"eff_score"`  // Effectiveness adjustment
	SampleCount int     `json:"sample_count"`
	Confidence  float64 `json:"confidence"`
	Rank        int     `json:"rank"`
	Explanation string  `json:"explanation"`
}

// RankAgentsForTask returns agents ranked by combined capability + effectiveness.
func (ei *EffectivenessIntegrator) RankAgentsForTask(taskType string) (*EffectivenessRanking, error) {
	task := ParseTaskType(taskType)

	ranking := &EffectivenessRanking{
		TaskType: taskType,
		Mode:     ei.GetMode(),
	}

	agentTypes := []tmux.AgentType{tmux.AgentClaude, tmux.AgentCodex, tmux.AgentGemini}

	for _, agent := range agentTypes {
		baseScore := ei.matrix.GetScore(agent, task)

		rank := AgentEffectivenessRank{
			AgentType: string(agent),
			BaseScore: baseScore,
			Score:     baseScore, // Default to base
		}

		// Get effectiveness adjustment
		eff, _ := ei.GetEffectivenessScore(string(agent), taskType)
		if eff != nil && eff.HasData {
			ranking.HasData = true
			rank.EffScore = eff.Score
			rank.SampleCount = eff.SampleCount
			rank.Confidence = eff.Confidence

			// Apply effectiveness weight
			weight := ei.config.EffectivenessWeight() * eff.Confidence
			rank.Score = baseScore*(1-weight) + eff.Score*weight

			rank.Explanation = fmt.Sprintf("base=%.2f, eff=%.2f (%d samples), weight=%.2f",
				baseScore, eff.Score, eff.SampleCount, weight)
		} else {
			rank.Explanation = fmt.Sprintf("base=%.2f (no effectiveness data)", baseScore)
		}

		ranking.Rankings = append(ranking.Rankings, rank)
	}

	// Sort by combined score descending
	for i := 0; i < len(ranking.Rankings)-1; i++ {
		for j := i + 1; j < len(ranking.Rankings); j++ {
			if ranking.Rankings[j].Score > ranking.Rankings[i].Score {
				ranking.Rankings[i], ranking.Rankings[j] = ranking.Rankings[j], ranking.Rankings[i]
			}
		}
	}

	// Assign ranks
	for i := range ranking.Rankings {
		ranking.Rankings[i].Rank = i + 1
	}

	return ranking, nil
}

// Global integrator instance
var (
	globalIntegrator     *EffectivenessIntegrator
	globalIntegratorOnce sync.Once
)

// DefaultIntegrator returns the global effectiveness integrator.
func DefaultIntegrator() *EffectivenessIntegrator {
	globalIntegratorOnce.Do(func() {
		globalIntegrator = NewEffectivenessIntegrator(DefaultEffectivenessConfig())
	})
	return globalIntegrator
}

// RefreshEffectivenessCapabilities updates the global capability matrix.
func RefreshEffectivenessCapabilities() error {
	return DefaultIntegrator().RefreshCapabilities()
}

// GetEffectivenessBonus returns effectiveness bonus using the global integrator.
func GetEffectivenessBonus(agentType, taskType string) (float64, string) {
	return DefaultIntegrator().GetEffectivenessBonus(agentType, taskType)
}

// SetAssignmentMode sets the global assignment mode.
func SetAssignmentMode(mode AssignmentMode) {
	DefaultIntegrator().SetMode(mode)
}

// GetAssignmentMode returns the current global assignment mode.
func GetAssignmentMode() AssignmentMode {
	return DefaultIntegrator().GetMode()
}

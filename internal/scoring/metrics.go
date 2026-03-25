// Package scoring provides effectiveness metrics for NTM agent evaluation.
// metrics.go defines the quantitative metrics used to measure agent session effectiveness.
//
// # Metric Philosophy
//
// Metrics are designed to be:
//   - Objective: Based on measurable outcomes, not subjective assessments
//   - Comparable: Normalized to 0-1 scale for cross-agent comparison
//   - Actionable: Indicate specific areas for improvement
//   - Gaming-resistant: Difficult to optimize without genuine improvement
//
// # Core Metrics
//
// Completion Rate (0-1): Fraction of assigned tasks completed successfully.
// Measured by: completed_tasks / assigned_tasks
// Higher is better. A completion of 1.0 means all tasks finished.
//
// Retry Count (normalized): How many retries were needed per task.
// Measured by: 1 - (retries / max_expected_retries)
// Higher is better. Fewer retries = more efficient first-attempt success.
//
// Time Efficiency (0-1): How quickly tasks were completed vs. estimated.
// Measured by: min(1, estimated_duration / actual_duration)
// Higher is better. Faster completion = better time efficiency.
//
// Token Efficiency (0-1): How economically tokens were used.
// Measured by: min(1, baseline_tokens / actual_tokens)
// Higher is better. Fewer tokens for same outcome = better efficiency.
//
// Quality Score (0-1): Quality of work produced.
// Measured by: (tests_passing + no_regressions + code_review_score) / 3
// This metric requires external validation (CI, review).
//
// # Weight Configuration
//
// Weights determine how individual metrics combine into an overall score.
// Default weights emphasize completion and quality over raw speed.
// Teams can adjust weights to match their priorities.
package scoring

import (
	"encoding/json"
	"os"
)

// MetricName identifies a specific effectiveness metric.
type MetricName string

const (
	// MetricCompletion measures task completion rate.
	MetricCompletion MetricName = "completion"

	// MetricRetries measures efficiency in first-attempt success.
	MetricRetries MetricName = "retries"

	// MetricTimeEfficiency measures speed relative to estimates.
	MetricTimeEfficiency MetricName = "time_efficiency"

	// MetricTokenEfficiency measures token economy.
	MetricTokenEfficiency MetricName = "token_efficiency"

	// MetricQuality measures output quality (requires external validation).
	MetricQuality MetricName = "quality"

	// MetricErrorRate measures rate of errors during work.
	MetricErrorRate MetricName = "error_rate"

	// MetricContextUsage measures how efficiently context window was used.
	MetricContextUsage MetricName = "context_usage"
)

// MetricDefinition describes a metric and how it's measured.
type MetricDefinition struct {
	// Name is the metric identifier
	Name MetricName `json:"name"`

	// Description explains what this metric measures
	Description string `json:"description"`

	// Unit describes the measurement unit (e.g., "ratio", "count", "minutes")
	Unit string `json:"unit"`

	// HigherIsBetter indicates if higher values are preferable
	HigherIsBetter bool `json:"higher_is_better"`

	// MinValue is the minimum possible value (typically 0)
	MinValue float64 `json:"min_value"`

	// MaxValue is the maximum possible value (typically 1 for normalized)
	MaxValue float64 `json:"max_value"`

	// MeasurementGuide describes how to measure this metric
	MeasurementGuide string `json:"measurement_guide"`
}

// CoreMetrics returns the definitions for all core effectiveness metrics.
func CoreMetrics() []MetricDefinition {
	return []MetricDefinition{
		{
			Name:           MetricCompletion,
			Description:    "Fraction of assigned tasks completed successfully",
			Unit:           "ratio",
			HigherIsBetter: true,
			MinValue:       0,
			MaxValue:       1,
			MeasurementGuide: `Calculated as: completed_tasks / assigned_tasks
- Count tasks marked as "completed" or "closed"
- Include partially completed with prorated credit
- Exclude tasks reassigned to other agents`,
		},
		{
			Name:           MetricRetries,
			Description:    "First-attempt success rate (inverse of retry frequency)",
			Unit:           "ratio",
			HigherIsBetter: true,
			MinValue:       0,
			MaxValue:       1,
			MeasurementGuide: `Calculated as: 1 - (retry_count / max_expected_retries)
- A retry is when the same prompt/instruction is resent
- Max expected retries defaults to 3 (configurable)
- Score of 1.0 means no retries needed`,
		},
		{
			Name:           MetricTimeEfficiency,
			Description:    "Speed of completion relative to complexity estimate",
			Unit:           "ratio",
			HigherIsBetter: true,
			MinValue:       0,
			MaxValue:       1,
			MeasurementGuide: `Calculated as: min(1, estimated_duration / actual_duration)
- Estimate based on task complexity or historical averages
- Capped at 1.0 (finishing early doesn't exceed perfect)
- Adjusted for interruptions and wait times`,
		},
		{
			Name:           MetricTokenEfficiency,
			Description:    "Economy of token usage for comparable outcomes",
			Unit:           "ratio",
			HigherIsBetter: true,
			MinValue:       0,
			MaxValue:       1,
			MeasurementGuide: `Calculated as: min(1, baseline_tokens / actual_tokens)
- Baseline derived from historical task-type averages
- Includes both prompt and completion tokens
- Adjust baseline by task complexity`,
		},
		{
			Name:           MetricQuality,
			Description:    "Quality of work output based on validation",
			Unit:           "ratio",
			HigherIsBetter: true,
			MinValue:       0,
			MaxValue:       1,
			MeasurementGuide: `Composite of:
- Tests passing: fraction of tests that pass
- No regressions: 1.0 if no new failures, 0.0 if regressions
- Review score: code review approval (optional)
Quality = (tests_passing + no_regressions + review_score) / 3`,
		},
		{
			Name:           MetricErrorRate,
			Description:    "Frequency of errors during task execution",
			Unit:           "ratio",
			HigherIsBetter: true, // Inverted: higher = fewer errors
			MinValue:       0,
			MaxValue:       1,
			MeasurementGuide: `Calculated as: 1 - (error_count / (error_count + success_count))
- Errors include: compilation failures, test failures, crashes
- Success is any action that completed without error
- Higher score means fewer errors`,
		},
		{
			Name:           MetricContextUsage,
			Description:    "Efficiency of context window utilization",
			Unit:           "ratio",
			HigherIsBetter: true,
			MinValue:       0,
			MaxValue:       1,
			MeasurementGuide: `Calculated as: 1 - (avg_context_usage_at_completion)
- Measures how much context headroom remained at task end
- Score of 1.0 means completed with minimal context used
- Penalizes tasks that exhausted context without completing`,
		},
	}
}

// Weights defines how individual metrics combine into an overall score.
type Weights struct {
	Completion      float64 `json:"completion"`
	Retries         float64 `json:"retries,omitempty"`
	TimeEfficiency  float64 `json:"time_efficiency,omitempty"`
	TokenEfficiency float64 `json:"token_efficiency,omitempty"`
	Quality         float64 `json:"quality,omitempty"`
	ErrorRate       float64 `json:"error_rate,omitempty"`
	ContextUsage    float64 `json:"context_usage,omitempty"`
}

// DefaultWeights returns the standard weight configuration.
// Emphasizes completion and quality over raw speed.
func DefaultWeights() Weights {
	return Weights{
		Completion:      0.30, // Task completion is fundamental
		Quality:         0.25, // Quality matters
		TokenEfficiency: 0.15, // Economy of resources
		TimeEfficiency:  0.10, // Speed is less critical
		Retries:         0.10, // First-attempt success
		ErrorRate:       0.05, // Error avoidance
		ContextUsage:    0.05, // Context management
	}
}

// SpeedFocusedWeights returns weights that prioritize speed over quality.
// Use for time-sensitive tasks where "good enough" is acceptable.
func SpeedFocusedWeights() Weights {
	return Weights{
		Completion:      0.25,
		Quality:         0.15,
		TokenEfficiency: 0.10,
		TimeEfficiency:  0.30, // Speed emphasized
		Retries:         0.10,
		ErrorRate:       0.05,
		ContextUsage:    0.05,
	}
}

// QualityFocusedWeights returns weights that prioritize quality over speed.
// Use for critical code paths or complex features.
func QualityFocusedWeights() Weights {
	return Weights{
		Completion:      0.25,
		Quality:         0.35, // Quality emphasized
		TokenEfficiency: 0.10,
		TimeEfficiency:  0.05,
		Retries:         0.10,
		ErrorRate:       0.10, // Errors penalized more
		ContextUsage:    0.05,
	}
}

// EconomyFocusedWeights returns weights that prioritize token efficiency.
// Use when optimizing for cost or context window constraints.
func EconomyFocusedWeights() Weights {
	return Weights{
		Completion:      0.25,
		Quality:         0.20,
		TokenEfficiency: 0.25, // Token economy emphasized
		TimeEfficiency:  0.05,
		Retries:         0.10,
		ErrorRate:       0.05,
		ContextUsage:    0.10, // Context usage emphasized
	}
}

// Sum returns the sum of all weights (should equal 1.0 for normalized scores).
func (w Weights) Sum() float64 {
	return w.Completion + w.Retries + w.TimeEfficiency +
		w.TokenEfficiency + w.Quality + w.ErrorRate + w.ContextUsage
}

// Normalize adjusts weights to sum to 1.0.
// Negative sums (malformed config) are treated as zero — weights are left unchanged.
func (w *Weights) Normalize() {
	sum := w.Sum()
	if sum <= 0 || sum == 1.0 {
		return
	}
	w.Completion /= sum
	w.Retries /= sum
	w.TimeEfficiency /= sum
	w.TokenEfficiency /= sum
	w.Quality /= sum
	w.ErrorRate /= sum
	w.ContextUsage /= sum
}

// EffectivenessScore combines individual metrics with weights.
type EffectivenessScore struct {
	// Individual metric values (all 0-1 normalized)
	Completion      float64 `json:"completion"`
	Retries         float64 `json:"retries,omitempty"`
	TimeEfficiency  float64 `json:"time_efficiency,omitempty"`
	TokenEfficiency float64 `json:"token_efficiency,omitempty"`
	Quality         float64 `json:"quality,omitempty"`
	ErrorRate       float64 `json:"error_rate,omitempty"`
	ContextUsage    float64 `json:"context_usage,omitempty"`

	// Weighted overall score
	Overall float64 `json:"overall"`

	// Weights used for calculation
	Weights Weights `json:"weights"`
}

// ComputeOverall calculates the weighted overall score.
func (e *EffectivenessScore) ComputeOverall() float64 {
	e.Weights.Normalize()
	e.Overall = (e.Completion * e.Weights.Completion) +
		(e.Retries * e.Weights.Retries) +
		(e.TimeEfficiency * e.Weights.TimeEfficiency) +
		(e.TokenEfficiency * e.Weights.TokenEfficiency) +
		(e.Quality * e.Weights.Quality) +
		(e.ErrorRate * e.Weights.ErrorRate) +
		(e.ContextUsage * e.Weights.ContextUsage)
	return e.Overall
}

// NewEffectivenessScore creates a score with the given metrics and default weights.
func NewEffectivenessScore(completion, quality, tokenEfficiency float64) *EffectivenessScore {
	score := &EffectivenessScore{
		Completion:      completion,
		Quality:         quality,
		TokenEfficiency: tokenEfficiency,
		Weights:         DefaultWeights(),
	}
	score.ComputeOverall()
	return score
}

// WithWeights creates a copy of the score with different weights and recomputes overall.
func (e *EffectivenessScore) WithWeights(w Weights) *EffectivenessScore {
	copy := *e
	copy.Weights = w
	copy.ComputeOverall()
	return &copy
}

// WeightsConfig holds weight configuration that can be loaded from a file.
type WeightsConfig struct {
	// Default weights for general use
	Default Weights `json:"default"`

	// TaskTypeWeights maps task types to specific weights
	TaskTypeWeights map[string]Weights `json:"task_types,omitempty"`
}

// LoadWeightsConfig loads weight configuration from a JSON file.
func LoadWeightsConfig(path string) (*WeightsConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config WeightsConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

// GetWeights returns weights for a task type, falling back to default.
func (c *WeightsConfig) GetWeights(taskType string) Weights {
	if c.TaskTypeWeights != nil {
		if w, ok := c.TaskTypeWeights[taskType]; ok {
			return w
		}
	}
	return c.Default
}

// RawMetrics contains the raw (non-normalized) metric measurements.
type RawMetrics struct {
	// TasksAssigned is total tasks assigned to the agent
	TasksAssigned int `json:"tasks_assigned"`

	// TasksCompleted is tasks successfully completed
	TasksCompleted int `json:"tasks_completed"`

	// RetryCount is number of retry prompts sent
	RetryCount int `json:"retry_count"`

	// EstimatedMinutes is estimated task duration
	EstimatedMinutes int `json:"estimated_minutes,omitempty"`

	// ActualMinutes is actual task duration
	ActualMinutes int `json:"actual_minutes,omitempty"`

	// BaselineTokens is expected token usage for task type
	BaselineTokens int `json:"baseline_tokens,omitempty"`

	// ActualTokens is tokens actually used
	ActualTokens int `json:"actual_tokens,omitempty"`

	// TestsPassing is fraction of tests passing (0-1)
	TestsPassing float64 `json:"tests_passing,omitempty"`

	// HasRegressions indicates if new test failures occurred
	HasRegressions bool `json:"has_regressions,omitempty"`

	// ErrorCount is number of errors encountered
	ErrorCount int `json:"error_count,omitempty"`

	// SuccessCount is number of successful operations
	SuccessCount int `json:"success_count,omitempty"`

	// AvgContextUsage is average context utilization (0-1)
	AvgContextUsage float64 `json:"avg_context_usage,omitempty"`
}

// ToEffectivenessScore converts raw metrics to a normalized effectiveness score.
func (r *RawMetrics) ToEffectivenessScore(w Weights) *EffectivenessScore {
	score := &EffectivenessScore{Weights: w}

	// Completion
	if r.TasksAssigned > 0 {
		score.Completion = float64(r.TasksCompleted) / float64(r.TasksAssigned)
	}

	// Retries (assuming max expected is 3)
	maxRetries := 3.0
	if r.RetryCount >= 0 {
		score.Retries = 1 - min(1, float64(r.RetryCount)/maxRetries)
	}

	// Time efficiency
	if r.EstimatedMinutes > 0 && r.ActualMinutes > 0 {
		score.TimeEfficiency = min(1, float64(r.EstimatedMinutes)/float64(r.ActualMinutes))
	}

	// Token efficiency
	if r.BaselineTokens > 0 && r.ActualTokens > 0 {
		score.TokenEfficiency = min(1, float64(r.BaselineTokens)/float64(r.ActualTokens))
	}

	// Quality
	qualityComponents := 0
	qualitySum := 0.0
	if r.TestsPassing > 0 {
		qualitySum += r.TestsPassing
		qualityComponents++
	}
	if r.HasRegressions {
		qualitySum += 0
		qualityComponents++
	} else if r.TestsPassing > 0 { // Only count if we have test info
		qualitySum += 1.0
		qualityComponents++
	}
	if qualityComponents > 0 {
		score.Quality = qualitySum / float64(qualityComponents)
	}

	// Error rate (no data → assume no errors rather than all errors)
	totalOps := r.ErrorCount + r.SuccessCount
	if totalOps > 0 {
		score.ErrorRate = 1 - (float64(r.ErrorCount) / float64(totalOps))
	} else {
		score.ErrorRate = 1.0 // no data = no errors observed
	}

	// Context usage (only score when data actually exists)
	if r.AvgContextUsage > 0 {
		score.ContextUsage = 1 - r.AvgContextUsage
	}

	score.ComputeOverall()
	return score
}

// min() removed — use Go 1.25 builtin min()

// Package scoring provides effectiveness score tracking for NTM agents.
// Scores are persisted to JSONL files and support historical analysis.
package scoring

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	// DefaultScorePath is the default location for score data.
	DefaultScorePath = "~/.config/ntm/analytics/scores.jsonl"

	// DefaultRetentionDays is how long to keep score records.
	DefaultRetentionDays = 90

	// TrendWindowDays is the default window for trend calculations.
	TrendWindowDays = 14

	// MinSamplesForTrend is the minimum samples needed to calculate a trend.
	MinSamplesForTrend = 3
)

// Score represents a single effectiveness measurement for an agent or session.
type Score struct {
	// Timestamp when the score was recorded
	Timestamp time.Time `json:"timestamp"`

	// Session name where this score was measured
	Session string `json:"session"`

	// AgentType is the type of agent (claude, codex, gemini)
	AgentType string `json:"agent_type"`

	// AgentName is the optional identifier for a specific agent
	AgentName string `json:"agent_name,omitempty"`

	// TaskType categorizes the work (e.g., "bug_fix", "feature", "refactor")
	TaskType string `json:"task_type,omitempty"`

	// BeadID is the optional bead this score relates to
	BeadID string `json:"bead_id,omitempty"`

	// Metrics contains the actual score values
	Metrics ScoreMetrics `json:"metrics"`

	// Context provides additional information about the scoring context
	Context map[string]interface{} `json:"context,omitempty"`
}

// ScoreMetrics contains the quantitative effectiveness measures.
type ScoreMetrics struct {
	// Completion is 0-1 indicating how much of the task was completed
	Completion float64 `json:"completion"`

	// Quality is 0-1 indicating the quality of work produced
	Quality float64 `json:"quality,omitempty"`

	// Efficiency is 0-1 measuring resource efficiency (tokens, time)
	Efficiency float64 `json:"efficiency,omitempty"`

	// PromptsUsed is the number of prompts sent to this agent
	PromptsUsed int `json:"prompts_used,omitempty"`

	// TokensUsed is the estimated token consumption
	TokensUsed int `json:"tokens_used,omitempty"`

	// DurationMinutes is how long the agent worked on this task
	DurationMinutes int `json:"duration_minutes,omitempty"`

	// ErrorCount is the number of errors encountered
	ErrorCount int `json:"error_count,omitempty"`

	// Overall is the computed overall effectiveness score (0-1)
	Overall float64 `json:"overall"`
}

// ComputeOverall calculates the overall score from individual metrics.
// Weights: Completion 40%, Quality 30%, Efficiency 30%
func (m *ScoreMetrics) ComputeOverall() float64 {
	// Default quality and efficiency to completion if not set
	quality := m.Quality
	if quality == 0 {
		quality = m.Completion
	}
	efficiency := m.Efficiency
	if efficiency == 0 {
		efficiency = m.Completion
	}

	m.Overall = (m.Completion * 0.4) + (quality * 0.3) + (efficiency * 0.3)
	return m.Overall
}

// Trend represents the direction of score changes over time.
type Trend string

const (
	TrendImproving Trend = "improving"
	TrendDeclining Trend = "declining"
	TrendStable    Trend = "stable"
	TrendUnknown   Trend = "unknown"
)

// TrendAnalysis provides statistical trend information.
type TrendAnalysis struct {
	// Trend is the overall direction
	Trend Trend `json:"trend"`

	// SampleCount is the number of scores analyzed
	SampleCount int `json:"sample_count"`

	// AvgScore is the mean score in the window
	AvgScore float64 `json:"avg_score"`

	// RecentAvg is the average of the most recent half
	RecentAvg float64 `json:"recent_avg"`

	// EarlierAvg is the average of the earlier half
	EarlierAvg float64 `json:"earlier_avg"`

	// ChangePercent is the percentage change from earlier to recent
	ChangePercent float64 `json:"change_percent"`

	// StdDev is the standard deviation of scores
	StdDev float64 `json:"std_dev,omitempty"`
}

// Tracker manages score persistence and analysis.
type Tracker struct {
	path          string
	retentionDays int
	enabled       bool
	mu            sync.Mutex
}

// TrackerOptions configures the score tracker.
type TrackerOptions struct {
	Path          string
	RetentionDays int
	Enabled       bool
}

// DefaultTrackerOptions returns default options.
func DefaultTrackerOptions() TrackerOptions {
	return TrackerOptions{
		Path:          util.ExpandPath(DefaultScorePath),
		RetentionDays: DefaultRetentionDays,
		Enabled:       true,
	}
}

// NewTracker creates a new score tracker.
func NewTracker(opts TrackerOptions) (*Tracker, error) {
	if opts.Path == "" {
		opts.Path = util.ExpandPath(DefaultScorePath)
	}
	if opts.RetentionDays == 0 {
		opts.RetentionDays = DefaultRetentionDays
	}

	t := &Tracker{
		path:          opts.Path,
		retentionDays: opts.RetentionDays,
		enabled:       opts.Enabled,
	}

	if !t.enabled {
		return t, nil
	}

	// Ensure directory exists
	dir := filepath.Dir(t.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating score directory: %w", err)
	}

	return t, nil
}

// Record persists a score to the tracker.
func (t *Tracker) Record(score *Score) error {
	if !t.enabled {
		return nil
	}

	now := time.Now().UTC()
	// Ensure timestamp is set
	if score.Timestamp.IsZero() {
		score.Timestamp = now
	}

	// Compute overall if not set
	if score.Metrics.Overall == 0 {
		score.Metrics.ComputeOverall()
	}

	data, err := json.Marshal(score)
	if err != nil {
		return fmt.Errorf("marshaling score: %w", err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.pruneLocked(now); err != nil {
		return err
	}

	f, err := os.OpenFile(t.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening score file: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("writing score: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing score file: %w", err)
	}

	return nil
}

// RecordSessionEnd records scores for all agents at the end of a session.
func (t *Tracker) RecordSessionEnd(session string, agentScores []Score) error {
	for i := range agentScores {
		agentScores[i].Session = session
		if err := t.Record(&agentScores[i]); err != nil {
			return fmt.Errorf("recording agent score: %w", err)
		}
	}
	return nil
}

// Close closes the tracker file.
func (t *Tracker) Close() error {
	return nil
}

// Prune removes score records older than the retention window.
func (t *Tracker) Prune() error {
	return t.pruneAt(time.Now().UTC())
}

func (t *Tracker) pruneAt(now time.Time) error {
	if !t.enabled {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pruneLocked(now)
}

func (t *Tracker) pruneLocked(now time.Time) error {
	if t.retentionDays <= 0 || t.path == "" {
		return nil
	}

	cutoff := now.AddDate(0, 0, -t.retentionDays)

	f, err := os.Open(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("opening score file: %w", err)
	}
	defer f.Close()

	var kept [][]byte
	pruned := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var score Score
		if err := json.Unmarshal(line, &score); err != nil {
			kept = append(kept, append([]byte(nil), line...))
			continue
		}
		if score.Timestamp.IsZero() || !score.Timestamp.Before(cutoff) {
			kept = append(kept, append([]byte(nil), line...))
		} else {
			pruned = true
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanning scores: %w", err)
	}

	if !pruned {
		return nil
	}

	dir := filepath.Dir(t.path)
	tmpFile, err := os.CreateTemp(dir, "scores-prune-*.jsonl")
	if err != nil {
		return fmt.Errorf("creating prune temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	for _, line := range kept {
		if _, err := tmpFile.Write(line); err != nil {
			tmpFile.Close()
			return fmt.Errorf("writing prune temp file: %w", err)
		}
		if _, err := tmpFile.Write([]byte("\n")); err != nil {
			tmpFile.Close()
			return fmt.Errorf("writing prune temp file: %w", err)
		}
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("closing prune temp file: %w", err)
	}

	if err := os.Rename(tmpPath, t.path); err != nil {
		return fmt.Errorf("replacing score file: %w", err)
	}
	tmpPath = "" // Prevent defer from removing the successfully renamed file

	return nil
}

// Query retrieves scores matching the given criteria.
type Query struct {
	// Since filters to scores after this time
	Since time.Time

	// AgentType filters by agent type (empty = all)
	AgentType string

	// TaskType filters by task type (empty = all)
	TaskType string

	// Session filters by session name (empty = all)
	Session string

	// Limit caps the number of results (0 = unlimited)
	Limit int
}

// QueryScores returns scores matching the query.
func (t *Tracker) QueryScores(q Query) ([]*Score, error) {
	if !t.enabled {
		return nil, nil
	}

	f, err := os.Open(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening score file: %w", err)
	}
	defer f.Close()

	var scores []*Score
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var score Score
		if err := json.Unmarshal(line, &score); err != nil {
			continue // Skip malformed
		}

		// Apply filters
		if !q.Since.IsZero() && !score.Timestamp.After(q.Since) {
			continue
		}
		if q.AgentType != "" && score.AgentType != q.AgentType {
			continue
		}
		if q.TaskType != "" && score.TaskType != q.TaskType {
			continue
		}
		if q.Session != "" && score.Session != q.Session {
			continue
		}

		scores = append(scores, &score)

		if q.Limit > 0 && len(scores) >= q.Limit {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning scores: %w", err)
	}

	return scores, nil
}

// RollingAverage computes the rolling average of overall scores.
func (t *Tracker) RollingAverage(q Query, windowDays int) (float64, error) {
	if windowDays <= 0 {
		windowDays = TrendWindowDays
	}

	q.Since = time.Now().AddDate(0, 0, -windowDays)
	scores, err := t.QueryScores(q)
	if err != nil {
		return 0, err
	}

	if len(scores) == 0 {
		return 0, nil
	}

	var sum float64
	for _, s := range scores {
		sum += s.Metrics.Overall
	}

	return sum / float64(len(scores)), nil
}

// AnalyzeTrend determines if scores are improving, declining, or stable.
func (t *Tracker) AnalyzeTrend(q Query, windowDays int) (*TrendAnalysis, error) {
	if windowDays <= 0 {
		windowDays = TrendWindowDays
	}

	q.Since = time.Now().AddDate(0, 0, -windowDays)
	scores, err := t.QueryScores(q)
	if err != nil {
		return nil, err
	}

	analysis := &TrendAnalysis{
		Trend:       TrendUnknown,
		SampleCount: len(scores),
	}

	if len(scores) < MinSamplesForTrend {
		return analysis, nil
	}

	// Sort by timestamp
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].Timestamp.Before(scores[j].Timestamp)
	})

	// Calculate overall average
	var sum float64
	for _, s := range scores {
		sum += s.Metrics.Overall
	}
	analysis.AvgScore = sum / float64(len(scores))

	// Split into earlier and recent halves
	mid := len(scores) / 2
	earlier := scores[:mid]
	recent := scores[mid:]

	var earlierSum, recentSum float64
	for _, s := range earlier {
		earlierSum += s.Metrics.Overall
	}
	for _, s := range recent {
		recentSum += s.Metrics.Overall
	}

	analysis.EarlierAvg = earlierSum / float64(len(earlier))
	analysis.RecentAvg = recentSum / float64(len(recent))

	// Calculate change percentage
	if analysis.EarlierAvg > 0 {
		analysis.ChangePercent = ((analysis.RecentAvg - analysis.EarlierAvg) / analysis.EarlierAvg) * 100
	} else if analysis.RecentAvg > 0 {
		// Earlier was zero but recent is positive → treat as 100% improvement
		analysis.ChangePercent = 100
	}

	// Calculate standard deviation
	var sqDiffSum float64
	for _, s := range scores {
		diff := s.Metrics.Overall - analysis.AvgScore
		sqDiffSum += diff * diff
	}
	analysis.StdDev = sqrt(sqDiffSum / float64(len(scores)))

	// Determine trend based on change percentage and significance
	// A change is significant if > 1 standard deviation
	threshold := 5.0 // minimum 5% threshold
	if analysis.AvgScore > 0 {
		adaptiveThreshold := analysis.StdDev * 100 / analysis.AvgScore // as percentage
		if adaptiveThreshold > threshold {
			threshold = adaptiveThreshold
		}
	}

	if analysis.ChangePercent > threshold {
		analysis.Trend = TrendImproving
	} else if analysis.ChangePercent < -threshold {
		analysis.Trend = TrendDeclining
	} else {
		analysis.Trend = TrendStable
	}

	return analysis, nil
}

// AgentSummary provides aggregate statistics for an agent type.
type AgentSummary struct {
	AgentType     string         `json:"agent_type"`
	TotalScores   int            `json:"total_scores"`
	AvgCompletion float64        `json:"avg_completion"`
	AvgQuality    float64        `json:"avg_quality"`
	AvgEfficiency float64        `json:"avg_efficiency"`
	AvgOverall    float64        `json:"avg_overall"`
	Trend         *TrendAnalysis `json:"trend,omitempty"`
}

// SummarizeByAgent provides aggregate stats grouped by agent type.
func (t *Tracker) SummarizeByAgent(since time.Time) (map[string]*AgentSummary, error) {
	scores, err := t.QueryScores(Query{Since: since})
	if err != nil {
		return nil, err
	}

	// Group by agent type
	byAgent := make(map[string][]*Score)
	for _, s := range scores {
		byAgent[s.AgentType] = append(byAgent[s.AgentType], s)
	}

	summaries := make(map[string]*AgentSummary)
	for agentType, agentScores := range byAgent {
		summary := &AgentSummary{
			AgentType:   agentType,
			TotalScores: len(agentScores),
		}

		var compSum, qualSum, effSum, overallSum float64
		for _, s := range agentScores {
			compSum += s.Metrics.Completion
			qualSum += s.Metrics.Quality
			effSum += s.Metrics.Efficiency
			overallSum += s.Metrics.Overall
		}

		n := float64(len(agentScores))
		summary.AvgCompletion = compSum / n
		summary.AvgQuality = qualSum / n
		summary.AvgEfficiency = effSum / n
		summary.AvgOverall = overallSum / n

		// Get trend
		trend, err := t.AnalyzeTrend(Query{AgentType: agentType}, TrendWindowDays)
		if err == nil {
			summary.Trend = trend
		}

		summaries[agentType] = summary
	}

	return summaries, nil
}

// SummarizeByAgentList returns summaries sorted by agent type for deterministic ordering.
func (t *Tracker) SummarizeByAgentList(since time.Time) ([]*AgentSummary, error) {
	summaries, err := t.SummarizeByAgent(since)
	if err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(summaries))
	for key := range summaries {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	ordered := make([]*AgentSummary, 0, len(keys))
	for _, key := range keys {
		ordered = append(ordered, summaries[key])
	}

	return ordered, nil
}

// Export writes all scores to a JSON file for external analysis.
func (t *Tracker) Export(outputPath string, since time.Time) error {
	scores, err := t.QueryScores(Query{Since: since})
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(scores, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling export: %w", err)
	}

	if err := os.WriteFile(outputPath, data, 0644); err != nil {
		return fmt.Errorf("writing export: %w", err)
	}

	return nil
}

// Global tracker instance
var (
	globalTracker     *Tracker
	globalTrackerOnce sync.Once
)

// DefaultTracker returns the global default tracker instance.
func DefaultTracker() *Tracker {
	globalTrackerOnce.Do(func() {
		var err error
		globalTracker, err = NewTracker(DefaultTrackerOptions())
		if err != nil {
			globalTracker = &Tracker{enabled: false}
		}
	})
	return globalTracker
}

// sqrt computes square root. Uses math.Sqrt for correctness across all input
// ranges (the previous Newton's method diverged to NaN for large inputs).
func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	return math.Sqrt(x)
}

// DefaultDecayFactor is the half-life in days for score decay.
// After this many days, a score's weight is halved.
const DefaultDecayFactor = 7

// DefaultMinSamplesForEffectiveness is the minimum samples needed
// to use effectiveness scores for assignment decisions.
const DefaultMinSamplesForEffectiveness = 3

// AgentTaskEffectiveness represents an agent's effectiveness for a task type.
// This is distinct from EffectivenessScore in metrics.go which handles metric computation.
type AgentTaskEffectiveness struct {
	AgentType    string  `json:"agent_type"`
	TaskType     string  `json:"task_type"`
	Score        float64 `json:"score"`         // Weighted average score (0-1)
	SampleCount  int     `json:"sample_count"`  // Number of scores used
	Confidence   float64 `json:"confidence"`    // Confidence in score (0-1)
	HasData      bool    `json:"has_data"`      // Whether sufficient data exists
	DecayApplied bool    `json:"decay_applied"` // Whether decay was applied
}

// DecayedAverage computes a time-weighted average where recent scores
// count more than older scores. Uses exponential decay with half-life.
func (t *Tracker) DecayedAverage(q Query, windowDays int, halfLifeDays int) (float64, int, error) {
	if windowDays <= 0 {
		windowDays = TrendWindowDays
	}
	if halfLifeDays <= 0 {
		halfLifeDays = DefaultDecayFactor
	}

	q.Since = time.Now().AddDate(0, 0, -windowDays)
	scores, err := t.QueryScores(q)
	if err != nil {
		return 0, 0, err
	}

	if len(scores) == 0 {
		return 0, 0, nil
	}

	now := time.Now()
	var weightedSum, totalWeight float64

	for _, s := range scores {
		// Calculate age in days
		ageDays := now.Sub(s.Timestamp).Hours() / 24

		// Exponential decay: weight = 2^(-age/halfLife)
		// After halfLifeDays, weight is 0.5
		// After 2*halfLifeDays, weight is 0.25, etc.
		weight := exp2(-ageDays / float64(halfLifeDays))

		weightedSum += s.Metrics.Overall * weight
		totalWeight += weight
	}

	if totalWeight == 0 {
		return 0, len(scores), nil
	}

	return weightedSum / totalWeight, len(scores), nil
}

// QueryEffectiveness retrieves the effectiveness score for an agent-task pair.
// Returns a score suitable for use in assignment decisions.
func (t *Tracker) QueryEffectiveness(agentType, taskType string, windowDays int) (*AgentTaskEffectiveness, error) {
	if windowDays <= 0 {
		windowDays = TrendWindowDays
	}

	result := &AgentTaskEffectiveness{
		AgentType: agentType,
		TaskType:  taskType,
	}

	// Query historical scores
	q := Query{
		AgentType: agentType,
		TaskType:  taskType,
		Since:     time.Now().AddDate(0, 0, -windowDays),
	}

	scores, err := t.QueryScores(q)
	if err != nil {
		return result, err
	}

	result.SampleCount = len(scores)

	// Check minimum samples threshold
	if len(scores) < DefaultMinSamplesForEffectiveness {
		result.HasData = false
		result.Confidence = 0
		return result, nil
	}

	result.HasData = true

	// Calculate decayed average
	score, _, err := t.DecayedAverage(q, windowDays, DefaultDecayFactor)
	if err != nil {
		return result, err
	}

	result.Score = score
	result.DecayApplied = true

	// Calculate confidence based on sample count
	// More samples = higher confidence, caps at 1.0
	// 10 samples = 90% confidence, 20+ = 100%
	result.Confidence = minFloat(1.0, float64(len(scores))/20.0)

	return result, nil
}

// QueryAllEffectiveness returns effectiveness scores for all agent-task pairs
// with sufficient data.
func (t *Tracker) QueryAllEffectiveness(windowDays int) (map[string]map[string]*AgentTaskEffectiveness, error) {
	if windowDays <= 0 {
		windowDays = TrendWindowDays
	}

	// Get all scores in window
	scores, err := t.QueryScores(Query{
		Since: time.Now().AddDate(0, 0, -windowDays),
	})
	if err != nil {
		return nil, err
	}

	// Build unique agent-task pairs
	pairs := make(map[string]map[string]bool)
	for _, s := range scores {
		if pairs[s.AgentType] == nil {
			pairs[s.AgentType] = make(map[string]bool)
		}
		pairs[s.AgentType][s.TaskType] = true
	}

	// Query effectiveness for each pair
	result := make(map[string]map[string]*AgentTaskEffectiveness)
	for agentType, taskTypes := range pairs {
		result[agentType] = make(map[string]*AgentTaskEffectiveness)
		for taskType := range taskTypes {
			eff, err := t.QueryEffectiveness(agentType, taskType, windowDays)
			if err != nil {
				continue
			}
			if eff.HasData {
				result[agentType][taskType] = eff
			}
		}
	}

	return result, nil
}

// exp2 computes 2^x. Uses math.Exp2 for correctness (the previous Taylor
// series approximation lost precision for negative exponents near -10).
func exp2(x float64) float64 {
	if x < -10 {
		return 0
	}
	if x > 10 {
		return 1024
	}
	return math.Exp2(x)
}

// minFloat returns the smaller of two float64 values.
// Named differently from min in metrics.go to avoid redeclaration.
func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

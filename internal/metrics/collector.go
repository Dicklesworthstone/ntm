// Package metrics provides success metrics tracking for NTM orchestration.
// It tracks API calls, latencies, blocked commands, and file conflicts
// to measure progress against improvement targets.
package metrics

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

// Collector tracks success metrics for NTM orchestration.
// It subscribes to the event bus and persists metrics to the state store.
type Collector struct {
	store       *state.Store
	sessionID   string
	mu          sync.RWMutex
	unsubscribe events.UnsubscribeFunc

	// In-memory counters for fast access
	apiCalls        map[string]int64 // tool:operation -> count
	latencies       map[string][]float64
	blockedCommands int64
	fileConflicts   int64
}

// NewCollector creates a new metrics collector for the given session.
func NewCollector(store *state.Store, sessionID string) *Collector {
	c := &Collector{
		store:     store,
		sessionID: sessionID,
		apiCalls:  make(map[string]int64),
		latencies: make(map[string][]float64),
	}
	c.subscribeToEvents()
	return c
}

// subscribeToEvents registers handlers for relevant events.
func (c *Collector) subscribeToEvents() {
	c.unsubscribe = events.SubscribeAll(func(e events.BusEvent) {
		switch evt := e.(type) {
		case events.AgentErrorEvent:
			if evt.ErrorType == "blocked_command" {
				c.RecordBlockedCommand(evt.AgentID, evt.Message, "policy")
			}
		}
	})
}

// Close releases resources and unsubscribes from events.
func (c *Collector) Close() {
	if c.unsubscribe != nil {
		c.unsubscribe()
	}
}

// RecordAPICall records an API call for a tool and operation.
func (c *Collector) RecordAPICall(tool, operation string) {
	c.mu.Lock()
	key := fmt.Sprintf("%s:%s", tool, operation)
	c.apiCalls[key]++
	c.mu.Unlock()

	// Persist to database
	if c.store != nil {
		c.upsertCounter("api_call", tool, operation)
	}
}

// RecordLatency records a latency measurement for an operation.
func (c *Collector) RecordLatency(operation string, duration time.Duration) {
	ms := float64(duration.Milliseconds())

	c.mu.Lock()
	c.latencies[operation] = append(c.latencies[operation], ms)
	// Keep only last 1000 samples per operation
	if len(c.latencies[operation]) > 1000 {
		c.latencies[operation] = c.latencies[operation][1:]
	}
	c.mu.Unlock()

	// Persist to database
	if c.store != nil {
		c.insertLatency(operation, ms)
	}
}

// RecordBlockedCommand records a blocked command event.
func (c *Collector) RecordBlockedCommand(agentID, command, reason string) {
	c.mu.Lock()
	c.blockedCommands++
	c.mu.Unlock()

	// Persist to database
	if c.store != nil {
		c.insertBlockedCommand(agentID, command, reason)
	}
}

// RecordFileConflict records a file reservation conflict.
func (c *Collector) RecordFileConflict(requestingAgent, holdingAgent, pathPattern string) {
	c.mu.Lock()
	c.fileConflicts++
	c.mu.Unlock()

	// Persist to database
	if c.store != nil {
		c.insertFileConflict(requestingAgent, holdingAgent, pathPattern)
	}
}

// MetricsReport contains aggregated metrics data.
type MetricsReport struct {
	SessionID        string                  `json:"session_id"`
	GeneratedAt      time.Time               `json:"generated_at"`
	APICallCounts    map[string]int64        `json:"api_call_counts"`
	LatencyStats     map[string]LatencyStats `json:"latency_stats"`
	BlockedCommands  int64                   `json:"blocked_commands"`
	FileConflicts    int64                   `json:"file_conflicts"`
	TargetComparison []TargetComparison      `json:"target_comparison"`
}

// LatencyStats contains statistical summaries for latency data.
type LatencyStats struct {
	Count int     `json:"count"`
	MinMs float64 `json:"min_ms"`
	MaxMs float64 `json:"max_ms"`
	AvgMs float64 `json:"avg_ms"`
	P50Ms float64 `json:"p50_ms"`
	P95Ms float64 `json:"p95_ms"`
	P99Ms float64 `json:"p99_ms"`
}

// TargetComparison compares a metric against its target.
type TargetComparison struct {
	Metric   string  `json:"metric"`
	Current  float64 `json:"current"`
	Target   float64 `json:"target"`
	Baseline float64 `json:"baseline,omitempty"`
	Status   string  `json:"status"` // "met", "improving", "regressing"
}

// Tier0Targets defines the critical success metrics targets.
var Tier0Targets = map[string]float64{
	"agent_bootstrap_calls":     1.0,  // Target: 1 per agent (was 4-5)
	"bv_triage_calls":           1.0,  // Target: 1 per analysis (was 4)
	"destructive_cmd_incidents": 0.0,  // Target: 0
	"file_conflicts":            0.0,  // Target: 0
	"cm_query_latency_ms":       50.0, // Target: <50ms (was ~500ms)
}

// Tier0Baselines defines the original baseline values.
var Tier0Baselines = map[string]float64{
	"agent_bootstrap_calls":     4.5,   // 4-5 per agent
	"bv_triage_calls":           4.0,   // 4 per analysis
	"destructive_cmd_incidents": 0.0,   // Unknown baseline
	"file_conflicts":            0.0,   // Unknown baseline
	"cm_query_latency_ms":       500.0, // ~500ms subprocess
}

// GenerateReport generates a comprehensive metrics report.
func (c *Collector) GenerateReport() (*MetricsReport, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	report := &MetricsReport{
		SessionID:     c.sessionID,
		GeneratedAt:   time.Now().UTC(),
		APICallCounts: make(map[string]int64),
		LatencyStats:  make(map[string]LatencyStats),
	}

	// Copy API call counts
	for k, v := range c.apiCalls {
		report.APICallCounts[k] = v
	}

	// Calculate latency statistics
	for op, samples := range c.latencies {
		if len(samples) == 0 {
			continue
		}
		report.LatencyStats[op] = calculateLatencyStats(samples)
	}

	report.BlockedCommands = c.blockedCommands
	report.FileConflicts = c.fileConflicts

	// Generate target comparisons
	report.TargetComparison = c.generateTargetComparisons()

	return report, nil
}

// generateTargetComparisons compares current metrics against targets.
func (c *Collector) generateTargetComparisons() []TargetComparison {
	comparisons := make([]TargetComparison, 0)

	// Blocked commands
	comparisons = append(comparisons, TargetComparison{
		Metric:   "destructive_cmd_incidents",
		Current:  float64(c.blockedCommands),
		Target:   Tier0Targets["destructive_cmd_incidents"],
		Baseline: Tier0Baselines["destructive_cmd_incidents"],
		Status:   getTargetStatus(float64(c.blockedCommands), Tier0Targets["destructive_cmd_incidents"], true),
	})

	// File conflicts
	comparisons = append(comparisons, TargetComparison{
		Metric:   "file_conflicts",
		Current:  float64(c.fileConflicts),
		Target:   Tier0Targets["file_conflicts"],
		Baseline: Tier0Baselines["file_conflicts"],
		Status:   getTargetStatus(float64(c.fileConflicts), Tier0Targets["file_conflicts"], true),
	})

	// CM query latency
	if stats, ok := c.latencies["cm_query"]; ok && len(stats) > 0 {
		avg := average(stats)
		comparisons = append(comparisons, TargetComparison{
			Metric:   "cm_query_latency_ms",
			Current:  avg,
			Target:   Tier0Targets["cm_query_latency_ms"],
			Baseline: Tier0Baselines["cm_query_latency_ms"],
			Status:   getTargetStatus(avg, Tier0Targets["cm_query_latency_ms"], true),
		})
	}

	return comparisons
}

// getTargetStatus determines if a metric meets its target.
// lowerIsBetter indicates if lower values are desirable.
func getTargetStatus(current, target float64, lowerIsBetter bool) string {
	if lowerIsBetter {
		if current <= target {
			return "met"
		}
		return "regressing"
	}
	if current >= target {
		return "met"
	}
	return "regressing"
}

// calculateLatencyStats computes statistical summaries for latency samples.
func calculateLatencyStats(samples []float64) LatencyStats {
	if len(samples) == 0 {
		return LatencyStats{}
	}

	// Make a copy for sorting
	sorted := make([]float64, len(samples))
	copy(sorted, samples)
	sortFloat64s(sorted)

	stats := LatencyStats{
		Count: len(samples),
		MinMs: sorted[0],
		MaxMs: sorted[len(sorted)-1],
		AvgMs: average(sorted),
		P50Ms: percentile(sorted, 50),
		P95Ms: percentile(sorted, 95),
		P99Ms: percentile(sorted, 99),
	}
	return stats
}

// sortFloat64s sorts a slice of float64 in place.
func sortFloat64s(s []float64) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// average calculates the mean of a slice.
func average(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	var sum float64
	for _, v := range s {
		sum += v
	}
	return sum / float64(len(s))
}

// percentile calculates the p-th percentile of a sorted slice.
func percentile(sorted []float64, p int) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := (p * len(sorted)) / 100
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// SaveSnapshot saves the current metrics as a named snapshot for later comparison.
func (c *Collector) SaveSnapshot(name string) error {
	report, err := c.GenerateReport()
	if err != nil {
		return fmt.Errorf("generate report: %w", err)
	}

	data, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}

	if c.store == nil {
		return nil
	}

	return c.insertSnapshot(name, string(data))
}

// LoadSnapshot loads a previously saved snapshot.
func (c *Collector) LoadSnapshot(name string) (*MetricsReport, error) {
	if c.store == nil {
		return nil, fmt.Errorf("no store configured")
	}

	data, err := c.querySnapshot(name)
	if err != nil {
		return nil, err
	}

	var report MetricsReport
	if err := json.Unmarshal([]byte(data), &report); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot: %w", err)
	}

	return &report, nil
}

// CompareSnapshots compares two snapshots and returns the differences.
func (c *Collector) CompareSnapshots(baseline, current *MetricsReport) *ComparisonResult {
	if baseline == nil || current == nil {
		return &ComparisonResult{
			APICallDeltas: make(map[string]int64),
			Improvements:  make([]string, 0),
			Regressions:   make([]string, 0),
		}
	}
	result := &ComparisonResult{
		BaselineTime:  baseline.GeneratedAt,
		CurrentTime:   current.GeneratedAt,
		APICallDeltas: make(map[string]int64),
		Improvements:  make([]string, 0),
		Regressions:   make([]string, 0),
	}

	// Compare API calls
	for k, v := range current.APICallCounts {
		if baselineVal, ok := baseline.APICallCounts[k]; ok {
			result.APICallDeltas[k] = v - baselineVal
		} else {
			result.APICallDeltas[k] = v
		}
	}

	// Compare latencies
	for op, currentStats := range current.LatencyStats {
		if baselineStats, ok := baseline.LatencyStats[op]; ok {
			if currentStats.AvgMs < baselineStats.AvgMs*0.9 {
				result.Improvements = append(result.Improvements,
					fmt.Sprintf("%s latency improved: %.1fms -> %.1fms", op, baselineStats.AvgMs, currentStats.AvgMs))
			} else if currentStats.AvgMs > baselineStats.AvgMs*1.1 {
				result.Regressions = append(result.Regressions,
					fmt.Sprintf("%s latency regressed: %.1fms -> %.1fms", op, baselineStats.AvgMs, currentStats.AvgMs))
			}
		}
	}

	// Compare blocked commands
	if current.BlockedCommands > baseline.BlockedCommands {
		result.Regressions = append(result.Regressions,
			fmt.Sprintf("blocked commands increased: %d -> %d", baseline.BlockedCommands, current.BlockedCommands))
	}

	// Compare file conflicts
	if current.FileConflicts > baseline.FileConflicts {
		result.Regressions = append(result.Regressions,
			fmt.Sprintf("file conflicts increased: %d -> %d", baseline.FileConflicts, current.FileConflicts))
	}

	return result
}

// ComparisonResult contains the result of comparing two snapshots.
type ComparisonResult struct {
	BaselineTime  time.Time        `json:"baseline_time"`
	CurrentTime   time.Time        `json:"current_time"`
	APICallDeltas map[string]int64 `json:"api_call_deltas"`
	Improvements  []string         `json:"improvements"`
	Regressions   []string         `json:"regressions"`
}

// Database helper methods

func (c *Collector) upsertCounter(metricName, tool, operation string) {
	// Use raw SQL since state.Store doesn't have metrics methods yet
	db := c.getDB()
	if db == nil {
		return
	}

	_, _ = db.Exec(`
		INSERT INTO metric_counters (session_id, metric_name, tool, operation, count, updated_at)
		VALUES (?, ?, ?, ?, 1, CURRENT_TIMESTAMP)
		ON CONFLICT(session_id, metric_name, COALESCE(tool, ''), COALESCE(operation, ''))
		DO UPDATE SET count = count + 1, updated_at = CURRENT_TIMESTAMP`,
		c.sessionID, metricName, tool, operation)
}

func (c *Collector) insertLatency(operation string, durationMs float64) {
	db := c.getDB()
	if db == nil {
		return
	}

	_, _ = db.Exec(`
		INSERT INTO metric_latencies (session_id, operation, duration_ms)
		VALUES (?, ?, ?)`,
		c.sessionID, operation, durationMs)
}

func (c *Collector) insertBlockedCommand(agentID, command, reason string) {
	db := c.getDB()
	if db == nil {
		return
	}

	_, _ = db.Exec(`
		INSERT INTO blocked_commands (session_id, agent_id, command, reason)
		VALUES (?, ?, ?, ?)`,
		c.sessionID, agentID, command, reason)
}

func (c *Collector) insertFileConflict(requestingAgent, holdingAgent, pathPattern string) {
	db := c.getDB()
	if db == nil {
		return
	}

	_, _ = db.Exec(`
		INSERT INTO file_conflicts (session_id, requesting_agent_id, holding_agent_id, path_pattern)
		VALUES (?, ?, ?, ?)`,
		c.sessionID, requestingAgent, holdingAgent, pathPattern)
}

func (c *Collector) insertSnapshot(name, data string) error {
	db := c.getDB()
	if db == nil {
		return fmt.Errorf("no database connection")
	}

	_, err := db.Exec(`
		INSERT INTO metric_snapshots (session_id, snapshot_name, snapshot_data)
		VALUES (?, ?, ?)`,
		c.sessionID, name, data)
	return err
}

func (c *Collector) querySnapshot(name string) (string, error) {
	db := c.getDB()
	if db == nil {
		return "", fmt.Errorf("no database connection")
	}

	var data string
	err := db.QueryRow(`
		SELECT snapshot_data FROM metric_snapshots
		WHERE session_id = ? AND snapshot_name = ?
		ORDER BY created_at DESC LIMIT 1`,
		c.sessionID, name).Scan(&data)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("snapshot not found: %s", name)
	}
	return data, err
}

// getDB returns the underlying database connection.
// This is a workaround until state.Store exposes metrics methods.
func (c *Collector) getDB() *sql.DB {
	// Use reflection or interface assertion if needed
	// For now, we'll use type assertion assuming Store exposes DB()
	type dbGetter interface {
		DB() *sql.DB
	}
	if getter, ok := interface{}(c.store).(dbGetter); ok {
		return getter.DB()
	}
	return nil
}

// Export formats for CLI commands

// ExportJSON exports the report as JSON.
func (r *MetricsReport) ExportJSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// ExportCSV exports latency data as CSV.
func (r *MetricsReport) ExportCSV() string {
	var sb string
	sb += "operation,count,min_ms,max_ms,avg_ms,p50_ms,p95_ms,p99_ms\n"
	// Sort keys for deterministic output
	keys := make([]string, 0, len(r.LatencyStats))
	for k := range r.LatencyStats {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, op := range keys {
		stats := r.LatencyStats[op]
		sb += fmt.Sprintf("%s,%d,%.2f,%.2f,%.2f,%.2f,%.2f,%.2f\n",
			op, stats.Count, stats.MinMs, stats.MaxMs, stats.AvgMs,
			stats.P50Ms, stats.P95Ms, stats.P99Ms)
	}
	return sb
}

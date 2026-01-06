package coordinator

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/scanner"
)

// QualityMonitor tracks code quality metrics and agent performance.
// It monitors UBS scan results, test pass/fail rates, error rates per agent,
// and context usage patterns. This data is used for adjusting agent assignments,
// triggering alerts, and feeding into session digests.
type QualityMonitor struct {
	mu sync.RWMutex

	// Project context
	projectDir string

	// UBS scanner (nil if UBS not available)
	scanner *scanner.Scanner

	// Aggregated metrics
	lastScanResult    *scanner.ScanResult
	lastScanTime      time.Time
	scanHistory       []ScanMetrics
	agentMetrics      map[string]*AgentQualityMetrics
	testHistory       []TestRunMetrics
	contextHistory    map[string][]ContextSnapshot
	qualityTrend      QualityTrend
	scanErrorCount    int
	lastScanError     error
	scanErrorCountRun int // Consecutive errors
}

// ScanMetrics summarizes a single UBS scan for historical tracking.
type ScanMetrics struct {
	Timestamp time.Time `json:"timestamp"`
	Duration  int64     `json:"duration_ms"`
	Critical  int       `json:"critical"`
	Warning   int       `json:"warning"`
	Info      int       `json:"info"`
	Files     int       `json:"files"`
	ExitCode  int       `json:"exit_code"`
}

// AgentQualityMetrics tracks quality metrics specific to an agent.
type AgentQualityMetrics struct {
	PaneID          string      `json:"pane_id"`
	AgentType       string      `json:"agent_type"`
	TestsPassed     int         `json:"tests_passed"`
	TestsFailed     int         `json:"tests_failed"`
	TestsTotal      int         `json:"tests_total"`
	ErrorCount      int         `json:"error_count"`
	RecoveryCount   int         `json:"recovery_count"`
	BugsIntroduced  int         `json:"bugs_introduced"`
	BugsFixed       int         `json:"bugs_fixed"`
	AvgContextUsage float64     `json:"avg_context_usage"`
	PeakContext     float64     `json:"peak_context_usage"`
	ContextSamples  int         `json:"context_samples"`
	LastActivity    time.Time   `json:"last_activity"`
	LastError       time.Time   `json:"last_error,omitempty"`
	ErrorHistory    []time.Time `json:"error_history,omitempty"` // Timestamps of errors for trend analysis
}

// TestRunMetrics captures results from a test execution.
type TestRunMetrics struct {
	Timestamp   time.Time `json:"timestamp"`
	AgentPaneID string    `json:"agent_pane_id,omitempty"`
	Passed      int       `json:"passed"`
	Failed      int       `json:"failed"`
	Skipped     int       `json:"skipped"`
	Duration    int64     `json:"duration_ms"`
	Package     string    `json:"package,omitempty"`
}

// ContextSnapshot captures context usage at a point in time.
type ContextSnapshot struct {
	Timestamp time.Time `json:"timestamp"`
	Usage     float64   `json:"usage"`
}

// QualityTrend summarizes the direction of quality metrics.
type QualityTrend struct {
	BugTrend     TrendDirection `json:"bug_trend"`     // Are bugs increasing/decreasing?
	TestTrend    TrendDirection `json:"test_trend"`    // Is test pass rate improving?
	ErrorTrend   TrendDirection `json:"error_trend"`   // Are agent errors increasing?
	ContextTrend TrendDirection `json:"context_trend"` // Is context usage increasing?
	LastUpdated  time.Time      `json:"last_updated"`
}

// TrendDirection indicates whether a metric is improving, declining, or stable.
type TrendDirection string

const (
	TrendImproving TrendDirection = "improving"
	TrendStable    TrendDirection = "stable"
	TrendDeclining TrendDirection = "declining"
	TrendUnknown   TrendDirection = "unknown"
)

// QualitySummary provides a snapshot of current quality metrics for use in digests and alerts.
type QualitySummary struct {
	Timestamp time.Time `json:"timestamp"`

	// Code quality from UBS
	UBSAvailable     bool `json:"ubs_available"`
	CriticalBugs     int  `json:"critical_bugs"`
	Warnings         int  `json:"warnings"`
	TotalFindings    int  `json:"total_findings"`
	LastScanAge      int  `json:"last_scan_age_minutes"` // Minutes since last scan
	ScanHealthy      bool `json:"scan_healthy"`
	ConsecutiveError int  `json:"consecutive_scan_errors,omitempty"`

	// Test metrics
	TestPassRate   float64 `json:"test_pass_rate"`   // 0-100
	RecentTestRuns int     `json:"recent_test_runs"` // In last hour
	TotalTestRuns  int     `json:"total_test_runs"`
	TestsPassedAll int     `json:"tests_passed_all"`
	TestsFailedAll int     `json:"tests_failed_all"`

	// Agent error metrics
	TotalAgentErrors int     `json:"total_agent_errors"`
	ErrorRate        float64 `json:"error_rate"` // Errors per hour

	// Context usage
	AvgContextUsage  float64 `json:"avg_context_usage"`
	PeakContextUsage float64 `json:"peak_context_usage"`
	HighContextCount int     `json:"high_context_count"` // Agents > 80%

	// Trends
	Trend QualityTrend `json:"trend"`

	// Alerts generated from quality metrics
	Alerts []string `json:"alerts,omitempty"`
}

// NewQualityMonitor creates a new quality monitor for a project.
func NewQualityMonitor(projectDir string) *QualityMonitor {
	qm := &QualityMonitor{
		projectDir:     projectDir,
		agentMetrics:   make(map[string]*AgentQualityMetrics),
		contextHistory: make(map[string][]ContextSnapshot),
		qualityTrend: QualityTrend{
			BugTrend:     TrendUnknown,
			TestTrend:    TrendUnknown,
			ErrorTrend:   TrendUnknown,
			ContextTrend: TrendUnknown,
		},
	}

	// Try to initialize UBS scanner (graceful degradation if not available)
	if s, err := scanner.New(); err == nil {
		qm.scanner = s
	}

	return qm
}

// IsUBSAvailable returns true if UBS scanning is available.
func (qm *QualityMonitor) IsUBSAvailable() bool {
	return qm.scanner != nil
}

// RunScan executes a UBS scan on the project directory.
func (qm *QualityMonitor) RunScan(ctx context.Context) (*scanner.ScanResult, error) {
	if qm.scanner == nil {
		return nil, scanner.ErrNotInstalled
	}

	opts := scanner.DefaultOptions()
	opts.DiffOnly = true // Only scan modified files for efficiency
	opts.Timeout = 30 * time.Second

	result, err := qm.scanner.Scan(ctx, qm.projectDir, opts)
	if err != nil {
		qm.mu.Lock()
		qm.scanErrorCount++
		qm.scanErrorCountRun++
		qm.lastScanError = err
		qm.mu.Unlock()
		return nil, err
	}

	qm.mu.Lock()
	qm.lastScanResult = result
	qm.lastScanTime = time.Now()
	qm.scanErrorCountRun = 0 // Reset consecutive error count on success

	// Record in history (keep last 100 scans)
	metrics := ScanMetrics{
		Timestamp: time.Now(),
		Duration:  result.Duration.Milliseconds(),
		Critical:  result.Totals.Critical,
		Warning:   result.Totals.Warning,
		Info:      result.Totals.Info,
		Files:     result.Totals.Files,
		ExitCode:  result.ExitCode,
	}
	qm.scanHistory = append(qm.scanHistory, metrics)
	if len(qm.scanHistory) > 100 {
		qm.scanHistory = qm.scanHistory[len(qm.scanHistory)-100:]
	}

	qm.updateBugTrend()
	qm.mu.Unlock()

	return result, nil
}

// GetLastScanResult returns the most recent scan result.
func (qm *QualityMonitor) GetLastScanResult() *scanner.ScanResult {
	qm.mu.RLock()
	defer qm.mu.RUnlock()
	return qm.lastScanResult
}

// RecordTestRun records the results of a test execution.
func (qm *QualityMonitor) RecordTestRun(agentPaneID string, passed, failed, skipped int, duration time.Duration, pkg string) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	metrics := TestRunMetrics{
		Timestamp:   time.Now(),
		AgentPaneID: agentPaneID,
		Passed:      passed,
		Failed:      failed,
		Skipped:     skipped,
		Duration:    duration.Milliseconds(),
		Package:     pkg,
	}

	qm.testHistory = append(qm.testHistory, metrics)
	// Keep last 500 test runs
	if len(qm.testHistory) > 500 {
		qm.testHistory = qm.testHistory[len(qm.testHistory)-500:]
	}

	// Update agent metrics if agent specified
	if agentPaneID != "" {
		agent := qm.getOrCreateAgentMetrics(agentPaneID)
		agent.TestsPassed += passed
		agent.TestsFailed += failed
		agent.TestsTotal += passed + failed + skipped
		agent.LastActivity = time.Now()
	}

	qm.updateTestTrend()
}

// RecordAgentError records an error occurrence for an agent.
func (qm *QualityMonitor) RecordAgentError(paneID, agentType string) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	agent := qm.getOrCreateAgentMetrics(paneID)
	agent.AgentType = agentType
	agent.ErrorCount++
	now := time.Now()
	agent.LastError = now

	// Track error history for trend analysis (keep last 100 errors per agent)
	agent.ErrorHistory = append(agent.ErrorHistory, now)
	if len(agent.ErrorHistory) > 100 {
		agent.ErrorHistory = agent.ErrorHistory[len(agent.ErrorHistory)-100:]
	}

	qm.updateErrorTrend()
}

// RecordAgentRecovery records when an agent recovers from an error state.
func (qm *QualityMonitor) RecordAgentRecovery(paneID string) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	agent := qm.getOrCreateAgentMetrics(paneID)
	agent.RecoveryCount++
	agent.LastActivity = time.Now()
}

// RecordContextUsage records context window usage for an agent.
func (qm *QualityMonitor) RecordContextUsage(paneID, agentType string, usage float64) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	agent := qm.getOrCreateAgentMetrics(paneID)
	agent.AgentType = agentType
	agent.ContextSamples++
	agent.AvgContextUsage = ((agent.AvgContextUsage * float64(agent.ContextSamples-1)) + usage) / float64(agent.ContextSamples)
	if usage > agent.PeakContext {
		agent.PeakContext = usage
	}
	agent.LastActivity = time.Now()

	// Record snapshot for history
	snapshot := ContextSnapshot{
		Timestamp: time.Now(),
		Usage:     usage,
	}
	qm.contextHistory[paneID] = append(qm.contextHistory[paneID], snapshot)
	// Keep last 1000 samples per agent
	if len(qm.contextHistory[paneID]) > 1000 {
		qm.contextHistory[paneID] = qm.contextHistory[paneID][len(qm.contextHistory[paneID])-1000:]
	}

	qm.updateContextTrend()
}

// RecordBugIntroduced records that an agent introduced a bug (detected by UBS).
func (qm *QualityMonitor) RecordBugIntroduced(paneID string) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	agent := qm.getOrCreateAgentMetrics(paneID)
	agent.BugsIntroduced++
}

// RecordBugFixed records that an agent fixed a bug.
func (qm *QualityMonitor) RecordBugFixed(paneID string) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	agent := qm.getOrCreateAgentMetrics(paneID)
	agent.BugsFixed++
}

// GetAgentMetrics returns quality metrics for a specific agent.
func (qm *QualityMonitor) GetAgentMetrics(paneID string) *AgentQualityMetrics {
	qm.mu.RLock()
	defer qm.mu.RUnlock()

	if m, ok := qm.agentMetrics[paneID]; ok {
		copy := *m
		return &copy
	}
	return nil
}

// GetAllAgentMetrics returns quality metrics for all agents.
func (qm *QualityMonitor) GetAllAgentMetrics() map[string]*AgentQualityMetrics {
	qm.mu.RLock()
	defer qm.mu.RUnlock()

	result := make(map[string]*AgentQualityMetrics, len(qm.agentMetrics))
	for k, v := range qm.agentMetrics {
		copy := *v
		result[k] = &copy
	}
	return result
}

// GetSummary generates a quality summary for use in digests and alerts.
func (qm *QualityMonitor) GetSummary() QualitySummary {
	qm.mu.RLock()
	defer qm.mu.RUnlock()

	summary := QualitySummary{
		Timestamp:    time.Now(),
		UBSAvailable: qm.scanner != nil,
		Trend:        qm.qualityTrend,
	}

	// UBS metrics
	if qm.lastScanResult != nil {
		summary.CriticalBugs = qm.lastScanResult.Totals.Critical
		summary.Warnings = qm.lastScanResult.Totals.Warning
		summary.TotalFindings = qm.lastScanResult.TotalIssues()
		summary.ScanHealthy = qm.lastScanResult.IsHealthy()
		summary.LastScanAge = int(time.Since(qm.lastScanTime).Minutes())
	}
	summary.ConsecutiveError = qm.scanErrorCountRun

	// Test metrics
	var totalPassed, totalFailed int
	recentCutoff := time.Now().Add(-1 * time.Hour)
	for _, tr := range qm.testHistory {
		totalPassed += tr.Passed
		totalFailed += tr.Failed
		if tr.Timestamp.After(recentCutoff) {
			summary.RecentTestRuns++
		}
	}
	summary.TotalTestRuns = len(qm.testHistory)
	summary.TestsPassedAll = totalPassed
	summary.TestsFailedAll = totalFailed
	if totalPassed+totalFailed > 0 {
		summary.TestPassRate = float64(totalPassed) / float64(totalPassed+totalFailed) * 100
	}

	// Agent error metrics
	var totalErrors int
	var contextSum float64
	var contextCount int
	for _, agent := range qm.agentMetrics {
		totalErrors += agent.ErrorCount
		if agent.ContextSamples > 0 {
			contextSum += agent.AvgContextUsage
			contextCount++
			if agent.PeakContext > summary.PeakContextUsage {
				summary.PeakContextUsage = agent.PeakContext
			}
			if agent.AvgContextUsage > 80 {
				summary.HighContextCount++
			}
		}
	}
	summary.TotalAgentErrors = totalErrors
	if contextCount > 0 {
		summary.AvgContextUsage = contextSum / float64(contextCount)
	}

	// Calculate error rate (errors per hour based on tracking duration)
	// Use time since first recorded metric as baseline
	if len(qm.testHistory) > 0 && totalErrors > 0 {
		firstRecord := qm.testHistory[0].Timestamp
		hours := time.Since(firstRecord).Hours()
		if hours > 0 {
			summary.ErrorRate = float64(totalErrors) / hours
		}
	}

	// Generate alerts based on metrics
	summary.Alerts = qm.generateAlerts(summary)

	return summary
}

// GetQualityScore returns an overall quality score (0-100) for agent assignment consideration.
// Higher is better.
func (qm *QualityMonitor) GetQualityScore() float64 {
	qm.mu.RLock()
	defer qm.mu.RUnlock()

	score := 100.0

	// Deduct for critical bugs
	if qm.lastScanResult != nil {
		score -= float64(qm.lastScanResult.Totals.Critical) * 10
		score -= float64(qm.lastScanResult.Totals.Warning) * 2
	}

	// Deduct for test failures
	var totalPassed, totalFailed int
	for _, tr := range qm.testHistory {
		totalPassed += tr.Passed
		totalFailed += tr.Failed
	}
	if totalPassed+totalFailed > 0 {
		failRate := float64(totalFailed) / float64(totalPassed+totalFailed)
		score -= failRate * 30 // Up to 30 point deduction for 100% failure rate
	}

	// Deduct for agent errors
	var totalErrors int
	for _, agent := range qm.agentMetrics {
		totalErrors += agent.ErrorCount
	}
	score -= float64(totalErrors) * 0.5 // Small deduction per error

	// Clamp to 0-100
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}

	return score
}

// GetAgentQualityScore returns a quality score for a specific agent (0-100).
func (qm *QualityMonitor) GetAgentQualityScore(paneID string) float64 {
	qm.mu.RLock()
	defer qm.mu.RUnlock()

	agent, ok := qm.agentMetrics[paneID]
	if !ok {
		return 50.0 // Neutral score for unknown agents
	}

	score := 100.0

	// Test pass rate factor (exclude skipped tests from denominator)
	testsRun := agent.TestsPassed + agent.TestsFailed
	if testsRun > 0 {
		passRate := float64(agent.TestsPassed) / float64(testsRun)
		score = score * (0.5 + 0.5*passRate) // Scale by pass rate (50-100%)
	}

	// Bug factor
	bugBalance := agent.BugsFixed - agent.BugsIntroduced
	score += float64(bugBalance) * 5 // +5 per bug fixed, -5 per bug introduced

	// Error factor
	errorRatio := float64(agent.ErrorCount) / float64(max(agent.RecoveryCount, 1))
	if errorRatio > 1 {
		score -= (errorRatio - 1) * 10 // Deduct for unrecovered errors
	}

	// Context usage factor (penalize consistently high usage)
	if agent.AvgContextUsage > 80 {
		score -= (agent.AvgContextUsage - 80) * 0.5
	}

	// Clamp to 0-100
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}

	return score
}

// getOrCreateAgentMetrics gets or creates metrics for an agent (must hold write lock).
func (qm *QualityMonitor) getOrCreateAgentMetrics(paneID string) *AgentQualityMetrics {
	if m, ok := qm.agentMetrics[paneID]; ok {
		return m
	}
	m := &AgentQualityMetrics{
		PaneID: paneID,
	}
	qm.agentMetrics[paneID] = m
	return m
}

// updateBugTrend analyzes scan history to determine bug trend (must hold write lock).
func (qm *QualityMonitor) updateBugTrend() {
	if len(qm.scanHistory) < 3 {
		qm.qualityTrend.BugTrend = TrendUnknown
		return
	}

	// Compare last 3 scans
	recent := qm.scanHistory[len(qm.scanHistory)-3:]
	firstTotal := recent[0].Critical + recent[0].Warning
	lastTotal := recent[2].Critical + recent[2].Warning

	if lastTotal < firstTotal {
		qm.qualityTrend.BugTrend = TrendImproving
	} else if lastTotal > firstTotal {
		qm.qualityTrend.BugTrend = TrendDeclining
	} else {
		qm.qualityTrend.BugTrend = TrendStable
	}
	qm.qualityTrend.LastUpdated = time.Now()
}

// updateTestTrend analyzes test history to determine test pass rate trend (must hold write lock).
func (qm *QualityMonitor) updateTestTrend() {
	if len(qm.testHistory) < 10 {
		qm.qualityTrend.TestTrend = TrendUnknown
		return
	}

	// Compare first half vs second half of last 20 runs
	n := min(20, len(qm.testHistory))
	runs := qm.testHistory[len(qm.testHistory)-n:]
	mid := n / 2

	var firstPassed, firstTotal, secondPassed, secondTotal int
	for i, r := range runs {
		if i < mid {
			firstPassed += r.Passed
			firstTotal += r.Passed + r.Failed
		} else {
			secondPassed += r.Passed
			secondTotal += r.Passed + r.Failed
		}
	}

	if firstTotal == 0 || secondTotal == 0 {
		qm.qualityTrend.TestTrend = TrendUnknown
		return
	}

	firstRate := float64(firstPassed) / float64(firstTotal)
	secondRate := float64(secondPassed) / float64(secondTotal)

	if secondRate > firstRate+0.05 {
		qm.qualityTrend.TestTrend = TrendImproving
	} else if secondRate < firstRate-0.05 {
		qm.qualityTrend.TestTrend = TrendDeclining
	} else {
		qm.qualityTrend.TestTrend = TrendStable
	}
	qm.qualityTrend.LastUpdated = time.Now()
}

// updateErrorTrend analyzes error history to determine error trend (must hold write lock).
func (qm *QualityMonitor) updateErrorTrend() {
	// Compare error count in recent hour vs previous hour across all agents
	now := time.Now()
	recentCutoff := now.Add(-1 * time.Hour)
	olderCutoff := now.Add(-2 * time.Hour)

	var recentErrors, olderErrors int
	for _, agent := range qm.agentMetrics {
		for _, errTime := range agent.ErrorHistory {
			if errTime.After(recentCutoff) {
				recentErrors++
			} else if errTime.After(olderCutoff) {
				olderErrors++
			}
		}
	}

	if recentErrors < olderErrors {
		qm.qualityTrend.ErrorTrend = TrendImproving
	} else if recentErrors > olderErrors {
		qm.qualityTrend.ErrorTrend = TrendDeclining
	} else {
		qm.qualityTrend.ErrorTrend = TrendStable
	}
	qm.qualityTrend.LastUpdated = time.Now()
}

// updateContextTrend analyzes context usage to determine trend (must hold write lock).
func (qm *QualityMonitor) updateContextTrend() {
	// Aggregate context samples across agents
	var allSamples []ContextSnapshot
	for _, samples := range qm.contextHistory {
		allSamples = append(allSamples, samples...)
	}

	if len(allSamples) < 20 {
		qm.qualityTrend.ContextTrend = TrendUnknown
		return
	}

	// Sort samples by timestamp to ensure correct time-based analysis
	sort.Slice(allSamples, func(i, j int) bool {
		return allSamples[i].Timestamp.Before(allSamples[j].Timestamp)
	})

	// Compare recent average vs older average
	n := min(100, len(allSamples))
	recent := allSamples[len(allSamples)-n:]
	mid := n / 2

	var firstSum, secondSum float64
	for i, s := range recent {
		if i < mid {
			firstSum += s.Usage
		} else {
			secondSum += s.Usage
		}
	}

	firstAvg := firstSum / float64(mid)
	secondAvg := secondSum / float64(n-mid)

	if secondAvg < firstAvg-5 {
		qm.qualityTrend.ContextTrend = TrendImproving
	} else if secondAvg > firstAvg+5 {
		qm.qualityTrend.ContextTrend = TrendDeclining
	} else {
		qm.qualityTrend.ContextTrend = TrendStable
	}
	qm.qualityTrend.LastUpdated = time.Now()
}

// generateAlerts creates alerts based on quality metrics.
func (qm *QualityMonitor) generateAlerts(summary QualitySummary) []string {
	var alerts []string

	// Critical bugs alert
	if summary.CriticalBugs > 0 {
		alerts = append(alerts, "Critical bugs detected by UBS - address immediately")
	}

	// Test failure alert
	if summary.TestPassRate < 80 && summary.TotalTestRuns > 5 {
		alerts = append(alerts, "Test pass rate below 80% - investigate failures")
	}

	// High context usage alert
	if summary.HighContextCount > 0 {
		alerts = append(alerts, "Agents with high context usage (>80%) detected")
	}

	// Stale scan alert
	if summary.UBSAvailable && summary.LastScanAge > 30 {
		alerts = append(alerts, "Last UBS scan was over 30 minutes ago")
	}

	// Scan error alert
	if summary.ConsecutiveError >= 3 {
		alerts = append(alerts, "Multiple consecutive UBS scan failures")
	}

	// Declining trends alert
	if summary.Trend.BugTrend == TrendDeclining {
		alerts = append(alerts, "Bug count trending upward")
	}
	if summary.Trend.TestTrend == TrendDeclining {
		alerts = append(alerts, "Test pass rate trending downward")
	}
	if summary.Trend.ErrorTrend == TrendDeclining {
		alerts = append(alerts, "Agent error rate trending upward")
	}

	return alerts
}

// max returns the larger of two integers.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// min returns the smaller of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

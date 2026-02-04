package coordinator

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/scanner"
)

func TestNewQualityMonitor(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test-project")

	if qm == nil {
		t.Fatal("expected non-nil QualityMonitor")
	}
	if qm.projectDir != "/tmp/test-project" {
		t.Errorf("expected projectDir '/tmp/test-project', got %s", qm.projectDir)
	}
	if qm.agentMetrics == nil {
		t.Error("expected agentMetrics map to be initialized")
	}
	if qm.contextHistory == nil {
		t.Error("expected contextHistory map to be initialized")
	}
}

func TestRecordTestRun(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	// Record a test run with agent
	qm.RecordTestRun("%1", 10, 2, 1, 5*time.Second, "github.com/example/pkg")

	if len(qm.testHistory) != 1 {
		t.Fatalf("expected 1 test run in history, got %d", len(qm.testHistory))
	}

	tr := qm.testHistory[0]
	if tr.Passed != 10 {
		t.Errorf("expected 10 passed, got %d", tr.Passed)
	}
	if tr.Failed != 2 {
		t.Errorf("expected 2 failed, got %d", tr.Failed)
	}
	if tr.AgentPaneID != "%1" {
		t.Errorf("expected agent '%%1', got %s", tr.AgentPaneID)
	}

	// Verify agent metrics updated
	agent := qm.GetAgentMetrics("%1")
	if agent == nil {
		t.Fatal("expected agent metrics to be created")
	}
	if agent.TestsPassed != 10 {
		t.Errorf("expected agent TestsPassed 10, got %d", agent.TestsPassed)
	}
	if agent.TestsFailed != 2 {
		t.Errorf("expected agent TestsFailed 2, got %d", agent.TestsFailed)
	}
}

func TestRecordAgentError(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	qm.RecordAgentError("%1", "cc")
	qm.RecordAgentError("%1", "cc")

	agent := qm.GetAgentMetrics("%1")
	if agent == nil {
		t.Fatal("expected agent metrics")
	}
	if agent.ErrorCount != 2 {
		t.Errorf("expected 2 errors, got %d", agent.ErrorCount)
	}
	if agent.AgentType != "cc" {
		t.Errorf("expected agent type 'cc', got %s", agent.AgentType)
	}
	if agent.LastError.IsZero() {
		t.Error("expected LastError to be set")
	}
}

func TestRecordAgentRecovery(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	qm.RecordAgentError("%1", "cc")
	qm.RecordAgentRecovery("%1")

	agent := qm.GetAgentMetrics("%1")
	if agent.RecoveryCount != 1 {
		t.Errorf("expected 1 recovery, got %d", agent.RecoveryCount)
	}
}

func TestRecordContextUsage(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	qm.RecordContextUsage("%1", "cc", 50.0)
	qm.RecordContextUsage("%1", "cc", 70.0)
	qm.RecordContextUsage("%1", "cc", 90.0)

	agent := qm.GetAgentMetrics("%1")
	if agent == nil {
		t.Fatal("expected agent metrics")
	}

	// Average should be (50+70+90)/3 = 70
	expectedAvg := 70.0
	if agent.AvgContextUsage < expectedAvg-1 || agent.AvgContextUsage > expectedAvg+1 {
		t.Errorf("expected avg context ~%.0f, got %.2f", expectedAvg, agent.AvgContextUsage)
	}

	if agent.PeakContext != 90.0 {
		t.Errorf("expected peak context 90, got %.2f", agent.PeakContext)
	}

	if agent.ContextSamples != 3 {
		t.Errorf("expected 3 samples, got %d", agent.ContextSamples)
	}

	// Check context history
	history := qm.contextHistory["%1"]
	if len(history) != 3 {
		t.Errorf("expected 3 history entries, got %d", len(history))
	}
}

func TestRecordBugs(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	qm.RecordBugIntroduced("%1")
	qm.RecordBugIntroduced("%1")
	qm.RecordBugFixed("%1")

	agent := qm.GetAgentMetrics("%1")
	if agent.BugsIntroduced != 2 {
		t.Errorf("expected 2 bugs introduced, got %d", agent.BugsIntroduced)
	}
	if agent.BugsFixed != 1 {
		t.Errorf("expected 1 bug fixed, got %d", agent.BugsFixed)
	}
}

func TestGetAllAgentMetrics(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	qm.RecordAgentError("%1", "cc")
	qm.RecordAgentError("%2", "cod")
	qm.RecordAgentError("%3", "gmi")

	all := qm.GetAllAgentMetrics()
	if len(all) != 3 {
		t.Errorf("expected 3 agents, got %d", len(all))
	}

	// Verify it's a copy (modifications don't affect original)
	all["%1"].ErrorCount = 999
	original := qm.GetAgentMetrics("%1")
	if original.ErrorCount == 999 {
		t.Error("GetAllAgentMetrics should return copies")
	}
}

func TestGetSummary(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	// Record some data
	qm.RecordTestRun("%1", 8, 2, 0, time.Second, "pkg1")
	qm.RecordTestRun("%1", 9, 1, 0, time.Second, "pkg2")
	qm.RecordAgentError("%1", "cc")
	qm.RecordContextUsage("%1", "cc", 85.0)
	qm.RecordContextUsage("%2", "cod", 50.0)

	summary := qm.GetSummary()

	if summary.TotalTestRuns != 2 {
		t.Errorf("expected 2 test runs, got %d", summary.TotalTestRuns)
	}

	// Pass rate: (8+9)/(8+2+9+1) = 17/20 = 85%
	expectedPassRate := 85.0
	if summary.TestPassRate < expectedPassRate-1 || summary.TestPassRate > expectedPassRate+1 {
		t.Errorf("expected pass rate ~%.0f%%, got %.2f%%", expectedPassRate, summary.TestPassRate)
	}

	if summary.TotalAgentErrors != 1 {
		t.Errorf("expected 1 agent error, got %d", summary.TotalAgentErrors)
	}

	if summary.HighContextCount != 1 {
		t.Errorf("expected 1 agent with high context, got %d", summary.HighContextCount)
	}
}

func TestGetQualityScore(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	// Empty monitor should have perfect score
	score := qm.GetQualityScore()
	if score != 100.0 {
		t.Errorf("expected score 100 for empty monitor, got %.2f", score)
	}

	// Add some test failures
	qm.RecordTestRun("", 7, 3, 0, time.Second, "pkg") // 70% pass rate
	score = qm.GetQualityScore()
	if score >= 100 {
		t.Error("score should decrease with test failures")
	}

	// Add agent errors
	qm.RecordAgentError("%1", "cc")
	qm.RecordAgentError("%1", "cc")
	newScore := qm.GetQualityScore()
	if newScore >= score {
		t.Error("score should decrease with agent errors")
	}
}

func TestGetAgentQualityScore(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	// Unknown agent should get neutral score
	score := qm.GetAgentQualityScore("%unknown")
	if score != 50.0 {
		t.Errorf("expected 50 for unknown agent, got %.2f", score)
	}

	// Agent with good metrics
	qm.RecordTestRun("%1", 10, 0, 0, time.Second, "pkg")
	qm.RecordBugFixed("%1")
	score = qm.GetAgentQualityScore("%1")
	if score <= 100 {
		// Score should be good (might exceed 100 due to bug fixes)
	}

	// Agent with bad metrics
	qm.RecordTestRun("%2", 2, 8, 0, time.Second, "pkg")
	qm.RecordBugIntroduced("%2")
	qm.RecordBugIntroduced("%2")
	qm.RecordAgentError("%2", "cod")
	score = qm.GetAgentQualityScore("%2")
	if score >= 80 {
		t.Errorf("expected lower score for problematic agent, got %.2f", score)
	}
}

func TestTrendCalculation(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	// Record enough test runs to calculate trend
	for i := 0; i < 15; i++ {
		if i < 10 {
			// Earlier runs have more failures
			qm.RecordTestRun("", 5, 5, 0, time.Second, "pkg")
		} else {
			// Later runs have fewer failures
			qm.RecordTestRun("", 9, 1, 0, time.Second, "pkg")
		}
	}

	summary := qm.GetSummary()
	if summary.Trend.TestTrend != TrendImproving {
		t.Errorf("expected improving test trend, got %s", summary.Trend.TestTrend)
	}
}

func TestContextTrend(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	// Record context samples showing decrease
	for i := 0; i < 30; i++ {
		usage := 80.0 - float64(i) // Decreasing from 80 to 50
		qm.RecordContextUsage("%1", "cc", usage)
	}

	summary := qm.GetSummary()
	if summary.Trend.ContextTrend != TrendImproving {
		t.Errorf("expected improving context trend, got %s", summary.Trend.ContextTrend)
	}
}

func TestGenerateAlerts(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	// Record bad metrics to trigger alerts
	for i := 0; i < 10; i++ {
		qm.RecordTestRun("", 5, 5, 0, time.Second, "pkg") // 50% pass rate
	}
	qm.RecordContextUsage("%1", "cc", 95.0) // High context

	summary := qm.GetSummary()

	// Should have alerts for test pass rate and high context
	if len(summary.Alerts) == 0 {
		t.Error("expected alerts to be generated")
	}

	foundTestAlert := false
	foundContextAlert := false
	for _, alert := range summary.Alerts {
		if alert == "Test pass rate below 80% - investigate failures" {
			foundTestAlert = true
		}
		if alert == "Agents with high context usage (>80%) detected" {
			foundContextAlert = true
		}
	}

	if !foundTestAlert {
		t.Error("expected test pass rate alert")
	}
	if !foundContextAlert {
		t.Error("expected high context alert")
	}
}

func TestGenerateAlerts_CriticalBugs(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	summary := QualitySummary{
		CriticalBugs: 3,
	}

	alerts := qm.generateAlerts(summary)
	found := false
	for _, a := range alerts {
		if a == "Critical bugs detected by UBS - address immediately" {
			found = true
		}
	}
	if !found {
		t.Error("expected critical bugs alert")
	}
}

func TestGenerateAlerts_StaleScan(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	summary := QualitySummary{
		UBSAvailable: true,
		LastScanAge:  45, // 45 minutes since last scan
	}

	alerts := qm.generateAlerts(summary)
	found := false
	for _, a := range alerts {
		if a == "Last UBS scan was over 30 minutes ago" {
			found = true
		}
	}
	if !found {
		t.Error("expected stale scan alert")
	}
}

func TestGenerateAlerts_ConsecutiveErrors(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	summary := QualitySummary{
		ConsecutiveError: 3,
	}

	alerts := qm.generateAlerts(summary)
	found := false
	for _, a := range alerts {
		if a == "Multiple consecutive UBS scan failures" {
			found = true
		}
	}
	if !found {
		t.Error("expected consecutive error alert")
	}
}

func TestGenerateAlerts_DecliningTrends(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	summary := QualitySummary{
		Trend: QualityTrend{
			BugTrend:   TrendDeclining,
			TestTrend:  TrendDeclining,
			ErrorTrend: TrendDeclining,
		},
	}

	alerts := qm.generateAlerts(summary)
	if len(alerts) != 3 {
		t.Errorf("expected 3 declining trend alerts, got %d: %v", len(alerts), alerts)
	}
}

func TestGenerateAlerts_NoAlerts(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	summary := QualitySummary{
		TestPassRate: 95,
		Trend: QualityTrend{
			BugTrend:   TrendStable,
			TestTrend:  TrendStable,
			ErrorTrend: TrendStable,
		},
	}

	alerts := qm.generateAlerts(summary)
	if len(alerts) != 0 {
		t.Errorf("expected no alerts, got %v", alerts)
	}
}

func TestUpdateBugTrend_InsufficientHistory(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	// Only 2 scans - not enough
	qm.scanHistory = []ScanMetrics{
		{Critical: 5, Warning: 3},
		{Critical: 3, Warning: 2},
	}

	qm.updateBugTrend()
	if qm.qualityTrend.BugTrend != TrendUnknown {
		t.Errorf("expected TrendUnknown with <3 scans, got %s", qm.qualityTrend.BugTrend)
	}
}

func TestUpdateBugTrend_Improving(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	qm.scanHistory = []ScanMetrics{
		{Critical: 5, Warning: 5}, // total 10
		{Critical: 3, Warning: 3}, // total 6
		{Critical: 1, Warning: 1}, // total 2 (improving)
	}

	qm.updateBugTrend()
	if qm.qualityTrend.BugTrend != TrendImproving {
		t.Errorf("expected TrendImproving, got %s", qm.qualityTrend.BugTrend)
	}
}

func TestUpdateBugTrend_Declining(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	qm.scanHistory = []ScanMetrics{
		{Critical: 1, Warning: 1}, // total 2
		{Critical: 3, Warning: 3}, // total 6
		{Critical: 5, Warning: 5}, // total 10 (declining)
	}

	qm.updateBugTrend()
	if qm.qualityTrend.BugTrend != TrendDeclining {
		t.Errorf("expected TrendDeclining, got %s", qm.qualityTrend.BugTrend)
	}
}

func TestUpdateBugTrend_Stable(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	qm.scanHistory = []ScanMetrics{
		{Critical: 3, Warning: 2}, // total 5
		{Critical: 1, Warning: 4}, // total 5
		{Critical: 2, Warning: 3}, // total 5 (stable)
	}

	qm.updateBugTrend()
	if qm.qualityTrend.BugTrend != TrendStable {
		t.Errorf("expected TrendStable, got %s", qm.qualityTrend.BugTrend)
	}
}

func TestUpdateTestTrend_InsufficientHistory(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	// Only 5 test runs - not enough (needs 10)
	for i := 0; i < 5; i++ {
		qm.testHistory = append(qm.testHistory, TestRunMetrics{Passed: 10, Failed: 0})
	}

	qm.updateTestTrend()
	if qm.qualityTrend.TestTrend != TrendUnknown {
		t.Errorf("expected TrendUnknown with <10 runs, got %s", qm.qualityTrend.TestTrend)
	}
}

func TestUpdateErrorTrend(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	now := time.Now()
	// 3 errors in last hour, 1 error in previous hour -> declining
	qm.agentMetrics["a1"] = &AgentQualityMetrics{
		ErrorHistory: []time.Time{
			now.Add(-30 * time.Minute),
			now.Add(-20 * time.Minute),
			now.Add(-10 * time.Minute),
			now.Add(-90 * time.Minute), // previous hour
		},
	}

	qm.updateErrorTrend()
	if qm.qualityTrend.ErrorTrend != TrendDeclining {
		t.Errorf("expected TrendDeclining (more recent errors), got %s", qm.qualityTrend.ErrorTrend)
	}
}

func TestUpdateErrorTrend_Improving(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	now := time.Now()
	// 1 error in last hour, 3 errors in previous hour -> improving
	qm.agentMetrics["a1"] = &AgentQualityMetrics{
		ErrorHistory: []time.Time{
			now.Add(-30 * time.Minute),  // recent hour: 1
			now.Add(-90 * time.Minute),  // older hour: 3
			now.Add(-100 * time.Minute),
			now.Add(-110 * time.Minute),
		},
	}

	qm.updateErrorTrend()
	if qm.qualityTrend.ErrorTrend != TrendImproving {
		t.Errorf("expected TrendImproving (fewer recent errors), got %s", qm.qualityTrend.ErrorTrend)
	}
}

func TestTrendConstants(t *testing.T) {
	t.Parallel()

	// Verify constants are non-empty and distinct
	trends := []TrendDirection{TrendImproving, TrendStable, TrendDeclining, TrendUnknown}
	seen := make(map[TrendDirection]bool)
	for _, td := range trends {
		if string(td) == "" {
			t.Error("TrendDirection constant is empty")
		}
		if seen[td] {
			t.Errorf("Duplicate TrendDirection: %q", td)
		}
		seen[td] = true
	}
}

func TestHistoryLimits(t *testing.T) {
	qm := NewQualityMonitor("/tmp/test")

	// Record more than the limit
	for i := 0; i < 600; i++ {
		qm.RecordTestRun("", 10, 0, 0, time.Second, "pkg")
	}

	if len(qm.testHistory) > 500 {
		t.Errorf("test history should be capped at 500, got %d", len(qm.testHistory))
	}

	// Context history limit
	for i := 0; i < 1100; i++ {
		qm.RecordContextUsage("%1", "cc", 50.0)
	}

	if len(qm.contextHistory["%1"]) > 1000 {
		t.Errorf("context history should be capped at 1000, got %d", len(qm.contextHistory["%1"]))
	}
}

// =============================================================================
// IsUBSAvailable / GetLastScanResult tests
// =============================================================================

func TestIsUBSAvailable(t *testing.T) {
	t.Parallel()

	qm := &QualityMonitor{}
	if qm.IsUBSAvailable() {
		t.Error("expected false when scanner is nil")
	}

	// Cannot easily set a real scanner in unit tests (requires UBS binary),
	// but we verify the nil-check path works.
}

func TestGetLastScanResult_Nil(t *testing.T) {
	t.Parallel()

	qm := NewQualityMonitor("/tmp/test")
	result := qm.GetLastScanResult()
	if result != nil {
		t.Errorf("expected nil when no scan has been run, got %v", result)
	}
}

func TestGetLastScanResult_WithResult(t *testing.T) {
	t.Parallel()

	qm := NewQualityMonitor("/tmp/test")

	// Directly set the lastScanResult (bypassing RunScan which needs UBS)
	expected := &scanner.ScanResult{
		Project: "test-project",
		Totals: scanner.ScanTotals{
			Critical: 2,
			Warning:  5,
			Info:     10,
			Files:    20,
		},
		ExitCode: 0,
	}
	qm.mu.Lock()
	qm.lastScanResult = expected
	qm.mu.Unlock()

	result := qm.GetLastScanResult()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Project != "test-project" {
		t.Errorf("expected project 'test-project', got %q", result.Project)
	}
	if result.Totals.Critical != 2 {
		t.Errorf("expected 2 critical, got %d", result.Totals.Critical)
	}
	if result.Totals.Warning != 5 {
		t.Errorf("expected 5 warnings, got %d", result.Totals.Warning)
	}
}

func TestGetAgentMetrics_Nil(t *testing.T) {
	t.Parallel()

	qm := NewQualityMonitor("/tmp/test")
	m := qm.GetAgentMetrics("%nonexistent")
	if m != nil {
		t.Errorf("expected nil for unknown agent, got %v", m)
	}
}

func TestErrorHistoryLimit(t *testing.T) {
	t.Parallel()

	qm := NewQualityMonitor("/tmp/test")

	// Record more than 100 errors to test history cap
	for i := 0; i < 110; i++ {
		qm.RecordAgentError("%1", "cc")
	}

	agent := qm.GetAgentMetrics("%1")
	if len(agent.ErrorHistory) > 100 {
		t.Errorf("error history should be capped at 100, got %d", len(agent.ErrorHistory))
	}
}

func TestUpdateErrorTrend_Stable(t *testing.T) {
	t.Parallel()

	qm := NewQualityMonitor("/tmp/test")

	now := time.Now()
	// Same errors in both hours -> stable
	qm.agentMetrics["a1"] = &AgentQualityMetrics{
		ErrorHistory: []time.Time{
			now.Add(-30 * time.Minute),  // recent hour: 1
			now.Add(-90 * time.Minute),  // older hour: 1
		},
	}

	qm.updateErrorTrend()
	if qm.qualityTrend.ErrorTrend != TrendStable {
		t.Errorf("expected TrendStable, got %s", qm.qualityTrend.ErrorTrend)
	}
}

func TestUpdateContextTrend_InsufficientData(t *testing.T) {
	t.Parallel()

	qm := NewQualityMonitor("/tmp/test")

	// Only 5 samples - not enough (needs 20)
	for i := 0; i < 5; i++ {
		qm.RecordContextUsage("%1", "cc", 50.0)
	}

	// Force re-evaluation
	qm.mu.Lock()
	qm.updateContextTrend()
	qm.mu.Unlock()

	if qm.qualityTrend.ContextTrend != TrendUnknown {
		t.Errorf("expected TrendUnknown with <20 samples, got %s", qm.qualityTrend.ContextTrend)
	}
}

func TestUpdateContextTrend_Declining(t *testing.T) {
	t.Parallel()

	qm := NewQualityMonitor("/tmp/test")

	// Record context samples showing increase (declining = usage going up)
	for i := 0; i < 30; i++ {
		usage := 30.0 + float64(i)*2 // Increasing from 30 to 88
		qm.RecordContextUsage("%1", "cc", usage)
	}

	if qm.qualityTrend.ContextTrend != TrendDeclining {
		t.Errorf("expected TrendDeclining (context usage increasing), got %s", qm.qualityTrend.ContextTrend)
	}
}

func TestUpdateContextTrend_Stable(t *testing.T) {
	t.Parallel()

	qm := NewQualityMonitor("/tmp/test")

	// Record stable context samples
	for i := 0; i < 30; i++ {
		qm.RecordContextUsage("%1", "cc", 50.0)
	}

	if qm.qualityTrend.ContextTrend != TrendStable {
		t.Errorf("expected TrendStable, got %s", qm.qualityTrend.ContextTrend)
	}
}

// Package robot provides machine-readable output for AI agents.
package robot

import (
	"testing"
	"time"
)

// =============================================================================
// Trend Tracker Tests
// =============================================================================

func TestNewTrendTracker(t *testing.T) {
	tracker := NewTrendTracker(10)
	if tracker == nil {
		t.Fatal("NewTrendTracker returned nil")
	}
	if tracker.maxSamples != 10 {
		t.Errorf("maxSamples = %d, want 10", tracker.maxSamples)
	}
}

func TestNewTrendTracker_MinSamples(t *testing.T) {
	// Should enforce minimum of 2 samples
	tracker := NewTrendTracker(1)
	if tracker.maxSamples != 2 {
		t.Errorf("maxSamples = %d, want 2 (minimum)", tracker.maxSamples)
	}
}

func TestTrendTracker_AddSample(t *testing.T) {
	tracker := NewTrendTracker(5)
	ctx := 80.0

	tracker.AddSample(1, TrendSample{
		Timestamp:        time.Now(),
		ContextRemaining: &ctx,
	})

	if tracker.GetSampleCount(1) != 1 {
		t.Errorf("sample count = %d, want 1", tracker.GetSampleCount(1))
	}
}

func TestTrendTracker_MaxSamplesEnforced(t *testing.T) {
	tracker := NewTrendTracker(3)

	for i := 0; i < 10; i++ {
		ctx := float64(100 - i*10)
		tracker.AddSample(1, TrendSample{
			Timestamp:        time.Now(),
			ContextRemaining: &ctx,
		})
	}

	// Should only keep last 3
	if tracker.GetSampleCount(1) != 3 {
		t.Errorf("sample count = %d, want 3", tracker.GetSampleCount(1))
	}
}

func TestTrendTracker_DecliningTrend(t *testing.T) {
	tracker := NewTrendTracker(10)

	// Simulate declining context: 80 -> 75 -> 70 -> 65 -> 60
	samples := []float64{80, 75, 70, 65, 60}
	for _, pct := range samples {
		p := pct
		tracker.AddSample(1, TrendSample{
			Timestamp:        time.Now(),
			ContextRemaining: &p,
		})
	}

	trend, count := tracker.GetTrend(1)
	if trend != TrendDeclining {
		t.Errorf("trend = %s, want %s", trend, TrendDeclining)
	}
	if count != 5 {
		t.Errorf("count = %d, want 5", count)
	}
}

func TestTrendTracker_StableTrend(t *testing.T) {
	tracker := NewTrendTracker(10)

	// Simulate stable context: 50 -> 51 -> 49 -> 50 -> 51
	samples := []float64{50, 51, 49, 50, 51}
	for _, pct := range samples {
		p := pct
		tracker.AddSample(1, TrendSample{
			Timestamp:        time.Now(),
			ContextRemaining: &p,
		})
	}

	trend, _ := tracker.GetTrend(1)
	if trend != TrendStable {
		t.Errorf("trend = %s, want %s", trend, TrendStable)
	}
}

func TestTrendTracker_RisingTrend(t *testing.T) {
	tracker := NewTrendTracker(10)

	// Simulate rising context (rare, after restart): 20 -> 30 -> 40 -> 50
	samples := []float64{20, 30, 40, 50}
	for _, pct := range samples {
		p := pct
		tracker.AddSample(1, TrendSample{
			Timestamp:        time.Now(),
			ContextRemaining: &p,
		})
	}

	trend, _ := tracker.GetTrend(1)
	if trend != TrendRising {
		t.Errorf("trend = %s, want %s", trend, TrendRising)
	}
}

func TestTrendTracker_UnknownTrend_NoSamples(t *testing.T) {
	tracker := NewTrendTracker(10)

	trend, count := tracker.GetTrend(1)
	if trend != TrendUnknown {
		t.Errorf("trend = %s, want %s", trend, TrendUnknown)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestTrendTracker_UnknownTrend_OneSample(t *testing.T) {
	tracker := NewTrendTracker(10)
	ctx := 50.0
	tracker.AddSample(1, TrendSample{
		Timestamp:        time.Now(),
		ContextRemaining: &ctx,
	})

	trend, count := tracker.GetTrend(1)
	if trend != TrendUnknown {
		t.Errorf("trend = %s, want %s", trend, TrendUnknown)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestTrendTracker_ClearPane(t *testing.T) {
	tracker := NewTrendTracker(10)
	ctx := 50.0
	tracker.AddSample(1, TrendSample{
		Timestamp:        time.Now(),
		ContextRemaining: &ctx,
	})
	tracker.AddSample(2, TrendSample{
		Timestamp:        time.Now(),
		ContextRemaining: &ctx,
	})

	tracker.ClearPane(1)

	if tracker.GetSampleCount(1) != 0 {
		t.Errorf("pane 1 count = %d, want 0", tracker.GetSampleCount(1))
	}
	if tracker.GetSampleCount(2) != 1 {
		t.Errorf("pane 2 count = %d, want 1", tracker.GetSampleCount(2))
	}
}

func TestTrendTracker_ClearAll(t *testing.T) {
	tracker := NewTrendTracker(10)
	ctx := 50.0
	tracker.AddSample(1, TrendSample{Timestamp: time.Now(), ContextRemaining: &ctx})
	tracker.AddSample(2, TrendSample{Timestamp: time.Now(), ContextRemaining: &ctx})
	tracker.AddSample(3, TrendSample{Timestamp: time.Now(), ContextRemaining: &ctx})

	tracker.ClearAll()

	for pane := 1; pane <= 3; pane++ {
		if tracker.GetSampleCount(pane) != 0 {
			t.Errorf("pane %d count = %d, want 0", pane, tracker.GetSampleCount(pane))
		}
	}
}

func TestTrendTracker_GetLastSample(t *testing.T) {
	tracker := NewTrendTracker(10)
	ctx := 50.0
	tracker.AddSample(1, TrendSample{
		Timestamp:        time.Now(),
		ContextRemaining: &ctx,
	})

	sample, ok := tracker.GetLastSample(1)
	if !ok {
		t.Fatal("GetLastSample returned false")
	}
	if sample.ContextRemaining == nil || *sample.ContextRemaining != 50.0 {
		t.Errorf("last sample context = %v, want 50.0", sample.ContextRemaining)
	}
}

func TestTrendTracker_GetLastSample_Empty(t *testing.T) {
	tracker := NewTrendTracker(10)

	_, ok := tracker.GetLastSample(1)
	if ok {
		t.Error("GetLastSample should return false for empty pane")
	}
}

func TestTrendTracker_GetTrendInfo(t *testing.T) {
	tracker := NewTrendTracker(10)

	// Add declining samples
	for i := 5; i >= 1; i-- {
		ctx := float64(i * 10)
		tracker.AddSample(1, TrendSample{
			Timestamp:        time.Now(),
			ContextRemaining: &ctx,
		})
	}

	info := tracker.GetTrendInfo(1)
	if info.Trend != TrendDeclining {
		t.Errorf("trend = %s, want %s", info.Trend, TrendDeclining)
	}
	if info.SampleCount != 5 {
		t.Errorf("sample count = %d, want 5", info.SampleCount)
	}
	if info.AvgDelta >= 0 {
		t.Errorf("avg delta = %f, want negative", info.AvgDelta)
	}
}

func TestTrendTracker_GetDecliningPanes(t *testing.T) {
	tracker := NewTrendTracker(10)

	// Pane 1: declining
	for i := 5; i >= 1; i-- {
		ctx := float64(i * 10)
		tracker.AddSample(1, TrendSample{Timestamp: time.Now(), ContextRemaining: &ctx})
	}

	// Pane 2: stable
	for i := 0; i < 5; i++ {
		ctx := float64(50 + (i % 2))
		tracker.AddSample(2, TrendSample{Timestamp: time.Now(), ContextRemaining: &ctx})
	}

	declining := tracker.GetDecliningPanes()
	if len(declining) != 1 || declining[0] != 1 {
		t.Errorf("declining panes = %v, want [1]", declining)
	}
}

// =============================================================================
// Warning Level Tests
// =============================================================================

func TestWarningLevel_Types(t *testing.T) {
	tests := []struct {
		level    WarningLevel
		expected string
	}{
		{LevelInfo, "INFO"},
		{LevelWarning, "WARNING"},
		{LevelCritical, "CRITICAL"},
		{LevelAlert, "ALERT"},
	}

	for _, tt := range tests {
		if string(tt.level) != tt.expected {
			t.Errorf("level = %s, want %s", tt.level, tt.expected)
		}
	}
}

func TestGetWarningLevel(t *testing.T) {
	config := DefaultMonitorConfig()

	tests := []struct {
		contextPct float64
		expected   WarningLevel
	}{
		{50, ""},            // No warning
		{39, LevelInfo},     // Below 40%
		{24, LevelWarning},  // Below 25%
		{14, LevelCritical}, // Below 15%
		{5, LevelCritical},  // Well below all thresholds
	}

	for _, tt := range tests {
		level := getWarningLevel(tt.contextPct, config)
		if level != tt.expected {
			t.Errorf("contextPct=%.0f: level = %s, want %s", tt.contextPct, level, tt.expected)
		}
	}
}

func TestGetWarningLevel_CustomThresholds(t *testing.T) {
	config := MonitorConfig{
		InfoThreshold: 50,
		WarnThreshold: 30,
		CritThreshold: 10,
	}

	tests := []struct {
		contextPct float64
		expected   WarningLevel
	}{
		{60, ""},           // No warning
		{45, LevelInfo},    // Below 50%
		{25, LevelWarning}, // Below 30%
		{5, LevelCritical}, // Below 10%
	}

	for _, tt := range tests {
		level := getWarningLevel(tt.contextPct, config)
		if level != tt.expected {
			t.Errorf("contextPct=%.0f: level = %s, want %s", tt.contextPct, level, tt.expected)
		}
	}
}

func TestGetSuggestedAction(t *testing.T) {
	tests := []struct {
		level    WarningLevel
		expected string
	}{
		{LevelCritical, "Restart agent soon"},
		{LevelWarning, "Prepare restart, let current task finish"},
		{LevelInfo, "Monitor context usage"},
		{LevelAlert, "Consider caam account switch"},
		{"", ""},
	}

	for _, tt := range tests {
		action := getSuggestedAction(tt.level)
		if action != tt.expected {
			t.Errorf("level=%s: action = %s, want %s", tt.level, action, tt.expected)
		}
	}
}

// =============================================================================
// Warning Struct Tests
// =============================================================================

func TestNewWarning(t *testing.T) {
	w := NewWarning(LevelWarning, "myproject", 2, "cc", "Test message", "Do something")

	if w.Level != LevelWarning {
		t.Errorf("level = %s, want %s", w.Level, LevelWarning)
	}
	if w.Session != "myproject" {
		t.Errorf("session = %s, want myproject", w.Session)
	}
	if w.Pane != 2 {
		t.Errorf("pane = %d, want 2", w.Pane)
	}
	if w.AgentType != "cc" {
		t.Errorf("agent type = %s, want cc", w.AgentType)
	}
	if w.Message != "Test message" {
		t.Errorf("message = %s, want 'Test message'", w.Message)
	}
	if w.SuggestedAction != "Do something" {
		t.Errorf("action = %s, want 'Do something'", w.SuggestedAction)
	}
	if w.Timestamp == "" {
		t.Error("timestamp should not be empty")
	}
}

func TestWarning_WithContext(t *testing.T) {
	ctx := 25.0
	w := NewWarning(LevelWarning, "myproject", 2, "cc", "Test", "Action").
		WithContext(&ctx, "declining", 5)

	if w.ContextRemaining == nil || *w.ContextRemaining != 25.0 {
		t.Errorf("context = %v, want 25.0", w.ContextRemaining)
	}
	if w.ContextTrend != "declining" {
		t.Errorf("trend = %s, want declining", w.ContextTrend)
	}
	if w.TrendSamples != 5 {
		t.Errorf("samples = %d, want 5", w.TrendSamples)
	}
}

func TestWarning_WithProvider(t *testing.T) {
	pct := 85.0
	w := NewWarning(LevelAlert, "myproject", 2, "cc", "Test", "Action").
		WithProvider("claude", &pct)

	if w.Provider != "claude" {
		t.Errorf("provider = %s, want claude", w.Provider)
	}
	if w.ProviderUsedPct == nil || *w.ProviderUsedPct != 85.0 {
		t.Errorf("provider pct = %v, want 85.0", w.ProviderUsedPct)
	}
}

// =============================================================================
// Monitor Config Tests
// =============================================================================

func TestDefaultMonitorConfig(t *testing.T) {
	config := DefaultMonitorConfig()

	if config.Interval != 30*time.Second {
		t.Errorf("interval = %v, want 30s", config.Interval)
	}
	if config.InfoThreshold != 40.0 {
		t.Errorf("info threshold = %f, want 40.0", config.InfoThreshold)
	}
	if config.WarnThreshold != 25.0 {
		t.Errorf("warn threshold = %f, want 25.0", config.WarnThreshold)
	}
	if config.CritThreshold != 15.0 {
		t.Errorf("crit threshold = %f, want 15.0", config.CritThreshold)
	}
	if config.AlertThreshold != 80.0 {
		t.Errorf("alert threshold = %f, want 80.0", config.AlertThreshold)
	}
	if config.IncludeCaut {
		t.Error("IncludeCaut should be false by default")
	}
	if config.CautInterval != 2*time.Minute {
		t.Errorf("caut interval = %v, want 2m", config.CautInterval)
	}
	if config.LinesCaptured != 100 {
		t.Errorf("lines captured = %d, want 100", config.LinesCaptured)
	}
}

// =============================================================================
// Parse Arg Tests
// =============================================================================

func TestParseIntervalArg(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
		wantErr  bool
	}{
		{"", 30 * time.Second, false}, // Default
		{"30s", 30 * time.Second, false},
		{"1m", time.Minute, false},
		{"5m", 5 * time.Minute, false},
		{"500ms", 0, true}, // Too short
		{"invalid", 0, true},
	}

	for _, tt := range tests {
		d, err := ParseIntervalArg(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseIntervalArg(%q) should error", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseIntervalArg(%q) unexpected error: %v", tt.input, err)
			continue
		}
		if d != tt.expected {
			t.Errorf("ParseIntervalArg(%q) = %v, want %v", tt.input, d, tt.expected)
		}
	}
}

func TestParseThresholdArg(t *testing.T) {
	tests := []struct {
		input      string
		defaultVal float64
		expected   float64
		wantErr    bool
	}{
		{"", 25.0, 25.0, false}, // Use default
		{"30", 25.0, 30.0, false},
		{"15.5", 25.0, 15.5, false},
		{"-5", 25.0, 0, true},  // Below 0
		{"150", 25.0, 0, true}, // Above 100
		{"abc", 25.0, 0, true}, // Invalid
	}

	for _, tt := range tests {
		v, err := ParseThresholdArg(tt.input, tt.defaultVal)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseThresholdArg(%q) should error", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseThresholdArg(%q) unexpected error: %v", tt.input, err)
			continue
		}
		if v != tt.expected {
			t.Errorf("ParseThresholdArg(%q) = %f, want %f", tt.input, v, tt.expected)
		}
	}
}

// =============================================================================
// Trend Classification Tests
// =============================================================================

func TestClassifyTrend(t *testing.T) {
	tests := []struct {
		avgDelta float64
		expected TrendType
	}{
		{-5.0, TrendDeclining},
		{-2.1, TrendDeclining},
		{-1.9, TrendStable},
		{0.0, TrendStable},
		{1.9, TrendStable},
		{2.1, TrendRising},
		{5.0, TrendRising},
	}

	for _, tt := range tests {
		result := classifyTrend(tt.avgDelta)
		if result != tt.expected {
			t.Errorf("classifyTrend(%f) = %s, want %s", tt.avgDelta, result, tt.expected)
		}
	}
}

func TestCalculateAvgDelta(t *testing.T) {
	tests := []struct {
		deltas   []float64
		expected float64
	}{
		{[]float64{}, 0},
		{[]float64{-5}, -5},
		{[]float64{-5, -5, -5}, -5},
		{[]float64{-10, 0, 10}, 0},
		{[]float64{1, 2, 3, 4}, 2.5},
	}

	for _, tt := range tests {
		result := calculateAvgDelta(tt.deltas)
		if result != tt.expected {
			t.Errorf("calculateAvgDelta(%v) = %f, want %f", tt.deltas, result, tt.expected)
		}
	}
}

// =============================================================================
// Monitor Output Tests
// =============================================================================

func TestMonitorThresh_Fields(t *testing.T) {
	thresh := MonitorThresh{
		Info:     40.0,
		Warning:  25.0,
		Critical: 15.0,
		Alert:    80.0,
	}

	if thresh.Info != 40.0 {
		t.Errorf("info = %f, want 40.0", thresh.Info)
	}
	if thresh.Warning != 25.0 {
		t.Errorf("warning = %f, want 25.0", thresh.Warning)
	}
	if thresh.Critical != 15.0 {
		t.Errorf("critical = %f, want 15.0", thresh.Critical)
	}
	if thresh.Alert != 80.0 {
		t.Errorf("alert = %f, want 80.0", thresh.Alert)
	}
}

func TestMonitorOutput_Fields(t *testing.T) {
	output := MonitorOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "myproject",
		Panes:         []int{2, 3, 4},
		Interval:      "30s",
		Thresholds: MonitorThresh{
			Info:     40.0,
			Warning:  25.0,
			Critical: 15.0,
			Alert:    80.0,
		},
		CautEnabled: true,
		Message:     "Monitor started",
	}

	if !output.Success {
		t.Error("success should be true")
	}
	if output.Session != "myproject" {
		t.Errorf("session = %s, want myproject", output.Session)
	}
	if len(output.Panes) != 3 {
		t.Errorf("panes count = %d, want 3", len(output.Panes))
	}
	if !output.CautEnabled {
		t.Error("caut enabled should be true")
	}
}

// =============================================================================
// Format Provider Usage Message Test
// =============================================================================

func TestFormatProviderUsageMessage(t *testing.T) {
	msg := formatProviderUsageMessage(80.0)
	expected := "Provider usage above 80%"
	if msg != expected {
		t.Errorf("message = %s, want %s", msg, expected)
	}
}

// =============================================================================
// Warning Message Tests
// =============================================================================

func TestGetWarningMessage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		level     WarningLevel
		threshold float64
		want      string
	}{
		{LevelCritical, 15.0, "Context below 15% threshold"},
		{LevelWarning, 25.0, "Context below 25% threshold"},
		{LevelInfo, 40.0, "Context below 40% threshold"},
		{LevelCritical, 10.0, "Context below threshold"}, // Non-standard threshold
		{"", 25.0, ""},                                   // Empty level
		{LevelAlert, 80.0, ""},                           // Alert not in switch
	}

	for _, tt := range tests {
		name := string(tt.level)
		if name == "" {
			name = "empty_level"
		}
		t.Run(name, func(t *testing.T) {
			got := getWarningMessage(tt.level, tt.threshold)
			if got != tt.want {
				t.Errorf("getWarningMessage(%q, %.0f) = %q, want %q", tt.level, tt.threshold, got, tt.want)
			}
		})
	}
}

func TestFormatWarningMessage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		threshold float64
		want      string
	}{
		{"critical_15", 15.0, "Context below 15% threshold"},
		{"warning_25", 25.0, "Context below 25% threshold"},
		{"info_40", 40.0, "Context below 40% threshold"},
		{"custom_10", 10.0, "Context below threshold"},
		{"custom_50", 50.0, "Context below threshold"},
		{"custom_80", 80.0, "Context below threshold"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatWarningMessage("Context below %.0f%% threshold", tt.threshold)
			if got != tt.want {
				t.Errorf("formatWarningMessage(%.0f) = %q, want %q", tt.threshold, got, tt.want)
			}
		})
	}
}

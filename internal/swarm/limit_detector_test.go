package swarm

import (
	"context"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
)

func TestNewLimitDetector(t *testing.T) {
	detector := NewLimitDetector()

	if detector == nil {
		t.Fatal("NewLimitDetector returned nil")
	}

	if detector.TmuxClient != nil {
		t.Error("expected TmuxClient to be nil for default client")
	}

	if detector.CheckInterval != 5*time.Second {
		t.Errorf("expected CheckInterval of 5s, got %v", detector.CheckInterval)
	}

	if detector.CaptureLines != 50 {
		t.Errorf("expected CaptureLines of 50, got %d", detector.CaptureLines)
	}

	if detector.eventChan == nil {
		t.Error("expected eventChan to be initialized")
	}

	if detector.monitoredPanes == nil {
		t.Error("expected monitoredPanes to be initialized")
	}
}

func TestLimitDetectorEvents(t *testing.T) {
	detector := NewLimitDetector()
	eventChan := detector.Events()

	if eventChan == nil {
		t.Error("Events() returned nil channel")
	}
}

func TestLimitDetectorStartNilPlan(t *testing.T) {
	detector := NewLimitDetector()
	ctx := context.Background()

	err := detector.Start(ctx, nil)
	if err != nil {
		t.Errorf("Start with nil plan should not error, got: %v", err)
	}
}

func TestLimitDetectorStartEmptyPlan(t *testing.T) {
	detector := NewLimitDetector()
	ctx := context.Background()

	plan := &SwarmPlan{
		Sessions: []SessionSpec{},
	}

	err := detector.Start(ctx, plan)
	if err != nil {
		t.Errorf("Start with empty plan should not error, got: %v", err)
	}

	// Should have no monitored panes
	panes := detector.MonitoredPanes()
	if len(panes) != 0 {
		t.Errorf("expected 0 monitored panes, got %d", len(panes))
	}

	detector.mu.RLock()
	started := detector.cancel != nil
	detector.mu.RUnlock()
	if started {
		t.Fatal("empty plan should not mark detector as started")
	}
}

func TestLimitDetectorStartPlanWithoutPanesDoesNotBlockRestart(t *testing.T) {
	detector := NewLimitDetector()
	ctx := context.Background()

	emptyPlan := &SwarmPlan{
		Sessions: []SessionSpec{
			{Name: "empty"},
		},
	}
	if err := detector.Start(ctx, emptyPlan); err != nil {
		t.Fatalf("Start with no-pane plan failed: %v", err)
	}

	plan := &SwarmPlan{
		Sessions: []SessionSpec{
			{
				Name:      "test_session",
				AgentType: "cc",
				Panes: []PaneSpec{
					{Index: 1, AgentType: "cc"},
				},
			},
		},
	}
	if err := detector.Start(ctx, plan); err != nil {
		t.Fatalf("restart after no-pane plan failed: %v", err)
	}
	detector.Stop()
}

func TestLimitDetectorParentContextCancelCleansUpAndAllowsRestart(t *testing.T) {
	detector := NewLimitDetector()
	ctx, cancel := context.WithCancel(context.Background())

	plan := &SwarmPlan{
		Sessions: []SessionSpec{
			{
				Name:      "test_session",
				AgentType: "cc",
				Panes: []PaneSpec{
					{Index: 1, AgentType: "cc"},
				},
			},
		},
	}

	if err := detector.Start(ctx, plan); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	cancel()

	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		detector.mu.RLock()
		cancelCleared := detector.cancel == nil
		paneCount := len(detector.monitoredPanes)
		detector.mu.RUnlock()
		if cancelCleared && paneCount == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	detector.mu.RLock()
	cancelCleared := detector.cancel == nil
	paneCount := len(detector.monitoredPanes)
	detector.mu.RUnlock()
	if !cancelCleared || paneCount != 0 {
		t.Fatalf("detector did not clean up after parent context cancel: cancelCleared=%v paneCount=%d", cancelCleared, paneCount)
	}

	restartCtx, restartCancel := context.WithCancel(context.Background())
	defer restartCancel()

	if err := detector.Start(restartCtx, plan); err != nil {
		t.Fatalf("restart after parent context cancel failed: %v", err)
	}
	detector.Stop()
}

func TestLimitDetectorStop(t *testing.T) {
	detector := NewLimitDetector()
	ctx := context.Background()

	plan := &SwarmPlan{
		Sessions: []SessionSpec{
			{
				Name:      "test_session",
				AgentType: "cc",
				Panes: []PaneSpec{
					{Index: 1, AgentType: "cc"},
				},
			},
		},
	}

	// Start and then stop
	_ = detector.Start(ctx, plan)
	detector.Stop()

	// Should have no monitored panes after stop
	panes := detector.MonitoredPanes()
	if len(panes) != 0 {
		t.Errorf("expected 0 monitored panes after Stop, got %d", len(panes))
	}
}

func TestLimitDetectorIsMonitoring(t *testing.T) {
	detector := NewLimitDetector()

	// Should not be monitoring anything initially
	if detector.IsMonitoring("test:1.1") {
		t.Error("expected IsMonitoring to return false for unmonitored pane")
	}
}

func TestLimitDetectorMonitoredPanes(t *testing.T) {
	detector := NewLimitDetector()

	panes := detector.MonitoredPanes()
	if panes == nil {
		t.Error("MonitoredPanes() returned nil")
	}
	if len(panes) != 0 {
		t.Errorf("expected 0 monitored panes initially, got %d", len(panes))
	}
}

func TestLimitDetectorStopPane(t *testing.T) {
	detector := NewLimitDetector()

	// StopPane on non-existent pane should not panic
	detector.StopPane("nonexistent:1.1")
}

func TestLimitDetectorStartPaneNilContext(t *testing.T) {
	detector := NewLimitDetector()

	if err := detector.StartPane(context.TODO(), "test:1.1", "cc"); err != nil {
		t.Fatalf("StartPane with nil context failed: %v", err)
	}
	t.Cleanup(func() {
		detector.StopPane("test:1.1")
	})

	if !detector.IsMonitoring("test:1.1") {
		t.Fatal("expected pane to be monitored after StartPane(nil, ...)")
	}
}

func TestLimitDetectorStartPaneParentContextCancelCleansUpAndAllowsRestart(t *testing.T) {
	detector := NewLimitDetector()

	ctx, cancel := context.WithCancel(context.Background())
	if err := detector.StartPane(ctx, "test:1.1", "openai-codex"); err != nil {
		t.Fatalf("StartPane failed: %v", err)
	}

	cancel()

	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !detector.IsMonitoring("test:1.1") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if detector.IsMonitoring("test:1.1") {
		t.Fatal("pane monitor entry was not cleared after parent context cancellation")
	}

	if err := detector.StartPane(context.Background(), "test:1.1", "openai-codex"); err != nil {
		t.Fatalf("restart after parent context cancellation failed: %v", err)
	}
	detector.StopPane("test:1.1")
}

func TestLimitEvent(t *testing.T) {
	event := LimitEvent{
		SessionPane: "test:1.5",
		AgentType:   "cc",
		Pattern:     "rate limit",
		RawOutput:   "You've hit your rate limit",
		DetectedAt:  time.Now(),
	}

	if event.SessionPane != "test:1.5" {
		t.Errorf("unexpected SessionPane: %s", event.SessionPane)
	}
	if event.AgentType != "cc" {
		t.Errorf("unexpected AgentType: %s", event.AgentType)
	}
	if event.Pattern != "rate limit" {
		t.Errorf("unexpected Pattern: %s", event.Pattern)
	}
}

func TestGetPatternsForAgent(t *testing.T) {
	detector := NewLimitDetector()

	claudePatterns := agent.GetPatternSet(agent.AgentTypeClaudeCode).RateLimitPatterns
	codexPatterns := agent.GetPatternSet(agent.AgentTypeCodex).RateLimitPatterns
	geminiPatterns := agent.GetPatternSet(agent.AgentTypeGemini).RateLimitPatterns
	cursorPatterns := agent.GetPatternSet(agent.AgentTypeCursor).RateLimitPatterns
	windsurfPatterns := agent.GetPatternSet(agent.AgentTypeWindsurf).RateLimitPatterns
	aiderPatterns := agent.GetPatternSet(agent.AgentTypeAider).RateLimitPatterns
	ollamaPatterns := agent.GetPatternSet(agent.AgentTypeOllama).RateLimitPatterns

	tests := []struct {
		agentType string
		expected  []string
	}{
		{"cc", claudePatterns},
		{"claude", claudePatterns},
		{"claude-code", claudePatterns},
		{"claude_code", claudePatterns},
		{"cod", codexPatterns},
		{"codex", codexPatterns},
		{"codex-cli", codexPatterns},
		{"openai", codexPatterns},
		{"openai-codex", codexPatterns},
		{"openai_codex", codexPatterns},
		{"gmi", geminiPatterns},
		{"gemini", geminiPatterns},
		{"gemini-cli", geminiPatterns},
		{"gemini_cli", geminiPatterns},
		{"google", geminiPatterns},
		{"google-gemini", geminiPatterns},
		{"google_gemini", geminiPatterns},
		{"cursor", cursorPatterns},
		{"ws", windsurfPatterns},
		{"windsurf", windsurfPatterns},
		{"aider", aiderPatterns},
		{"ollama", ollamaPatterns},
		{"unknown", defaultLimitPatterns},
		{"", defaultLimitPatterns},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			patterns := detector.getPatternsForAgent(tt.agentType)
			if !slices.Equal(patterns, tt.expected) {
				t.Fatalf("patterns for %q = %v, want %v", tt.agentType, patterns, tt.expected)
			}
		})
	}
}

func TestCheckOutputEmpty(t *testing.T) {
	detector := NewLimitDetector()

	event := detector.checkOutput("test:1.1", "cc", "")
	if event != nil {
		t.Error("expected nil event for empty output")
	}
}

func TestCheckOutputNoMatch(t *testing.T) {
	detector := NewLimitDetector()

	output := "Normal agent output\nNo issues here\nJust working on code"
	event := detector.checkOutput("test:1.1", "cc", output)
	if event != nil {
		t.Error("expected nil event for output with no rate limit patterns")
	}
}

func TestCheckOutputMatch(t *testing.T) {
	detector := NewLimitDetector()

	output := "Working on task...\nYou've hit your rate limit. Please wait.\nTry again later."
	event := detector.checkOutput("test:1.1", "cc", output)

	if event == nil {
		t.Fatal("expected non-nil event for output with rate limit pattern")
	}

	if event.SessionPane != "test:1.1" {
		t.Errorf("unexpected SessionPane: %s", event.SessionPane)
	}
	if event.AgentType != "cc" {
		t.Errorf("unexpected AgentType: %s", event.AgentType)
	}
	if event.RawOutput != output {
		t.Error("expected RawOutput to match input")
	}
}

func TestCheckOutputPatternsByAgent(t *testing.T) {
	detector := NewLimitDetector()

	tests := []struct {
		name        string
		agentType   string
		output      string
		wantMatch   bool
		wantPattern string
	}{
		{
			name:        "cc_rate_limit_exceeded",
			agentType:   "cc",
			output:      "Error: rate limit exceeded. Please wait before trying again.",
			wantMatch:   true,
			wantPattern: "rate limit exceeded",
		},
		{
			name:        "cc_hit_limit",
			agentType:   "claude",
			output:      "You've hit your limit for now.",
			wantMatch:   true,
			wantPattern: "you've hit your limit",
		},
		{
			name:        "cc_too_many_requests",
			agentType:   "claude-code",
			output:      "API Error: Too many requests. Try again later.",
			wantMatch:   true,
			wantPattern: "too many requests",
		},
		{
			name:        "cc_alias_claude_code",
			agentType:   "claude_code",
			output:      "You've hit your limit for now.",
			wantMatch:   true,
			wantPattern: "you've hit your limit",
		},
		{
			name:        "cod_usage_limit",
			agentType:   "cod",
			output:      "You've reached your usage limit for this period.",
			wantMatch:   true,
			wantPattern: "you've reached your usage limit",
		},
		{
			name:        "cod_quota_exceeded",
			agentType:   "codex",
			output:      "OpenAI error: quota exceeded on your account.",
			wantMatch:   true,
			wantPattern: "quota exceeded",
		},
		{
			name:        "cod_alias_openai_codex",
			agentType:   "openai-codex",
			output:      "OpenAI error: you've reached your usage limit for this period.",
			wantMatch:   true,
			wantPattern: "you've reached your usage limit",
		},
		{
			name:        "gmi_resource_exhausted",
			agentType:   "gmi",
			output:      "google.api_core.exceptions.ResourceExhausted: 429 Resource exhausted",
			wantMatch:   true,
			wantPattern: "resource exhausted",
		},
		{
			name:        "gmi_limit_reached",
			agentType:   "gemini",
			output:      "Limit reached for this model. Please try again.",
			wantMatch:   true,
			wantPattern: "limit reached",
		},
		{
			name:        "gmi_alias_google_gemini",
			agentType:   "google_gemini",
			output:      "google.api_core.exceptions.ResourceExhausted: 429 Resource exhausted",
			wantMatch:   true,
			wantPattern: "resource exhausted",
		},
		{
			name:        "unknown_agent_default_patterns",
			agentType:   "unknown",
			output:      "Rate limit exceeded by upstream provider.",
			wantMatch:   true,
			wantPattern: "rate limit",
		},
		{
			name:        "unknown_agent_limit_exceeded_literal",
			agentType:   "unknown",
			output:      "Request failed because limit exceeded for upstream provider.",
			wantMatch:   true,
			wantPattern: "limit exceeded",
		},
		{
			name:      "partial_match_no_limit",
			agentType: "cc",
			output:    "The word rate appears but limit does not follow immediately.",
			wantMatch: false,
		},
		{
			name:      "empty_output",
			agentType: "cc",
			output:    "",
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("AgentType=%s Output=%q", tt.agentType, tt.output)
			event := detector.checkOutput("test:1.1", tt.agentType, tt.output)

			if tt.wantMatch {
				if event == nil {
					t.Fatalf("expected match, got nil event")
				}
				if tt.wantPattern != "" && event.Pattern != tt.wantPattern {
					t.Fatalf("unexpected pattern: got %q want %q", event.Pattern, tt.wantPattern)
				}
				if event.RawOutput != tt.output {
					t.Fatalf("raw output mismatch: got %q want %q", event.RawOutput, tt.output)
				}
			} else if event != nil {
				t.Fatalf("expected no match, got pattern %q", event.Pattern)
			}
		})
	}
}

func TestCheckOutputAllKnownPatterns(t *testing.T) {
	detector := NewLimitDetector()

	ccSet := agent.GetPatternSet(agent.AgentTypeClaudeCode)
	if ccSet == nil {
		t.Fatal("expected Claude pattern set")
	}
	codSet := agent.GetPatternSet(agent.AgentTypeCodex)
	if codSet == nil {
		t.Fatal("expected Codex pattern set")
	}
	gmiSet := agent.GetPatternSet(agent.AgentTypeGemini)
	if gmiSet == nil {
		t.Fatal("expected Gemini pattern set")
	}

	cases := []struct {
		name      string
		agentType string
		patterns  []string
	}{
		{
			name:      "claude",
			agentType: "cc",
			patterns:  ccSet.RateLimitPatterns,
		},
		{
			name:      "codex",
			agentType: "cod",
			patterns:  codSet.RateLimitPatterns,
		},
		{
			name:      "gemini",
			agentType: "gmi",
			patterns:  gmiSet.RateLimitPatterns,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.patterns) == 0 {
				t.Fatalf("expected patterns for %s", tc.name)
			}
			for _, pattern := range tc.patterns {
				output := "prefix " + pattern + " suffix"
				t.Logf("AgentType=%s Pattern=%q Output=%q", tc.agentType, pattern, output)

				event := detector.checkOutput("test:1.1", tc.agentType, output)
				if event == nil {
					t.Fatalf("expected match for pattern %q", pattern)
				}
				if event.Pattern != pattern {
					t.Fatalf("pattern mismatch: got %q want %q", event.Pattern, pattern)
				}
			}
		})
	}
}

func TestCheckOutputCaseInsensitive(t *testing.T) {
	detector := NewLimitDetector()

	// Test case insensitivity
	output := "RATE LIMIT EXCEEDED"
	event := detector.checkOutput("test:1.1", "cc", output)

	if event == nil {
		t.Error("expected pattern matching to be case insensitive")
	}
}

func TestCheckOutputMultiline(t *testing.T) {
	detector := NewLimitDetector()

	output := "Processing...\nWorking...\nError: rate limit exceeded\nRetrying..."
	t.Logf("Output=%q", output)

	event := detector.checkOutput("test:1.1", "cc", output)
	if event == nil {
		t.Fatal("expected match for multiline output")
	}
	if event.RawOutput != output {
		t.Fatalf("raw output mismatch: got %q want %q", event.RawOutput, output)
	}
}

func TestLimitDetectorCheckPaneWithMockClient(t *testing.T) {
	mock := &MockTmuxClient{
		t:               t,
		CaptureSequence: []string{"Normal agent output", "Error: rate limit exceeded"},
	}
	detector := NewLimitDetector()
	detector.TmuxClient = mock
	detector.CaptureLines = 5

	t.Logf("Capturing pane output for test:1.1")
	event, err := detector.CheckPane("test:1.1", "cc")
	if err != nil {
		t.Fatalf("unexpected error on first capture: %v", err)
	}
	if event != nil {
		t.Fatalf("expected no event on first capture, got %v", event)
	}

	t.Logf("Capturing pane output for test:1.1 (limit expected)")
	event, err = detector.CheckPane("test:1.1", "cc")
	if err != nil {
		t.Fatalf("unexpected error on second capture: %v", err)
	}
	if event == nil {
		t.Fatal("expected rate limit event on second capture, got nil")
	}
	if event.Pattern == "" {
		t.Fatal("expected non-empty pattern on rate limit event")
	}
	t.Logf("Detected pattern=%q", event.Pattern)
}

func TestLimitDetectorMonitorPaneEmitsEvent(t *testing.T) {
	mock := &MockTmuxClient{
		t:               t,
		CaptureSequence: []string{"Normal output", "Error: rate limit exceeded"},
	}
	detector := NewLimitDetector()
	detector.TmuxClient = mock
	detector.CheckInterval = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	if err := detector.StartPane(ctx, "test:1.1", "cc"); err != nil {
		t.Fatalf("StartPane failed: %v", err)
	}

	select {
	case event := <-detector.Events():
		t.Logf("Event received: %+v", event)
		if event.AgentType != "cc" {
			t.Fatalf("unexpected agent type: %s", event.AgentType)
		}
		if event.Pattern == "" {
			t.Fatal("expected non-empty pattern in event")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for limit event")
	}
}

func TestLimitDetectorStartPaneZeroCheckIntervalDoesNotPanic(t *testing.T) {
	mock := &MockTmuxClient{
		t:               t,
		CaptureSequence: []string{"Normal output"},
	}
	detector := NewLimitDetector()
	detector.TmuxClient = mock
	detector.CheckInterval = 0

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if err := detector.StartPane(ctx, "test:1.1", "cc"); err != nil {
		t.Fatalf("StartPane failed: %v", err)
	}

	time.Sleep(10 * time.Millisecond)
	detector.StopPane("test:1.1")
}

func TestDefaultLimitPatterns(t *testing.T) {
	if len(defaultLimitPatterns) == 0 {
		t.Error("expected non-empty defaultLimitPatterns")
	}

	expectedPatterns := []string{
		"rate limit",
		"usage limit",
		"quota exceeded",
		"too many requests",
	}

	for _, expected := range expectedPatterns {
		found := false
		for _, pattern := range defaultLimitPatterns {
			if pattern == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected pattern %q in defaultLimitPatterns", expected)
		}
	}
}

func TestLimitDetectorTmuxClientHelper(t *testing.T) {
	// With nil client, should return default
	detector := NewLimitDetector()
	client := detector.tmuxClient()
	if client == nil {
		t.Error("expected non-nil client from tmuxClient()")
	}
}

func TestLimitDetectorLoggerHelper(t *testing.T) {
	// With nil logger, should return default
	detector := &LimitDetector{}
	logger := detector.logger()
	if logger == nil {
		t.Error("expected non-nil logger from logger()")
	}
}

func TestLimitDetectorHandleLimitEventUpdatesTracker(t *testing.T) {
	tracker := ratelimit.NewRateLimitTracker(t.TempDir())
	detector := NewLimitDetectorWithTracker(tracker)

	event := &LimitEvent{
		SessionPane: "test:1.1",
		AgentType:   "cod",
		Pattern:     "rate limit",
		RawOutput:   "retry-after: 2",
		DetectedAt:  time.Now(),
	}

	detector.handleLimitEvent(event)

	remaining := tracker.CooldownRemaining("openai")
	if remaining <= 0 {
		t.Fatalf("expected cooldown to be set, got %v", remaining)
	}
	if remaining < time.Second || remaining > 3*time.Second {
		t.Fatalf("expected cooldown near 2s, got %v", remaining)
	}
}

func TestLimitDetectorHandleLimitEventConcurrentStopDoesNotPanic(t *testing.T) {
	const emitters = 8
	const emitsPerWorker = 200

	detector := NewLimitDetector()
	event := &LimitEvent{
		SessionPane: "test:1.1",
		AgentType:   "cc",
		Pattern:     "rate limit",
		RawOutput:   "rate limit exceeded",
		DetectedAt:  time.Now(),
	}

	panicCh := make(chan any, 1)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for range emitters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			defer func() {
				if r := recover(); r != nil {
					select {
					case panicCh <- r:
					default:
					}
				}
			}()

			for range emitsPerWorker {
				detector.handleLimitEvent(event)
				runtime.Gosched()
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		runtime.Gosched()
		detector.Stop()
	}()

	close(start)
	wg.Wait()

	select {
	case p := <-panicCh:
		t.Fatalf("handleLimitEvent panicked during concurrent Stop: %v", p)
	default:
	}
}

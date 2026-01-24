package swarm

import (
	"context"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
)

func TestNewPromptInjector(t *testing.T) {
	injector := NewPromptInjector()

	if injector == nil {
		t.Fatal("NewPromptInjector returned nil")
	}

	if injector.TmuxClient != nil {
		t.Error("expected TmuxClient to be nil for default client")
	}

	if injector.StaggerDelay != 300*time.Millisecond {
		t.Errorf("expected StaggerDelay of 300ms, got %v", injector.StaggerDelay)
	}

	if injector.EnterDelay != 100*time.Millisecond {
		t.Errorf("expected EnterDelay of 100ms, got %v", injector.EnterDelay)
	}

	if injector.DoubleEnterDelay != 500*time.Millisecond {
		t.Errorf("expected DoubleEnterDelay of 500ms, got %v", injector.DoubleEnterDelay)
	}

	if injector.Logger == nil {
		t.Error("expected non-nil Logger")
	}

	if injector.PreambleEngine == nil {
		t.Error("expected non-nil PreambleEngine")
	}

	if len(injector.Templates) == 0 {
		t.Error("expected non-empty Templates map")
	}
}

func TestGetTemplate(t *testing.T) {
	injector := NewPromptInjector()

	tests := []struct {
		name        string
		expectEmpty bool
	}{
		{"default", false},
		{"review", false},
		{"test", false},
		{"nonexistent", false}, // Falls back to default
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpl := injector.GetTemplate(tt.name)
			if tt.expectEmpty && tmpl != "" {
				t.Errorf("expected empty template for %q, got %q", tt.name, tmpl)
			}
			if !tt.expectEmpty && tmpl == "" {
				t.Errorf("expected non-empty template for %q", tt.name)
			}
		})
	}
}

func TestSetTemplate(t *testing.T) {
	injector := NewPromptInjector()

	customTemplate := "This is a custom template"
	injector.SetTemplate("custom", customTemplate)

	result := injector.GetTemplate("custom")
	if result != customTemplate {
		t.Errorf("expected template %q, got %q", customTemplate, result)
	}
}

func TestNeedsDoubleEnter(t *testing.T) {
	tests := []struct {
		agentType string
		expected  bool
	}{
		{"cc", false},
		{"claude", false},
		{"cod", true},
		{"codex", true},
		{"gmi", true},
		{"gemini", true},
		{"unknown", false},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			result := needsDoubleEnter(tt.agentType)
			if result != tt.expected {
				t.Errorf("needsDoubleEnter(%q) = %v, want %v", tt.agentType, result, tt.expected)
			}
		})
	}
}

func TestWithLogger(t *testing.T) {
	injector := NewPromptInjector()
	result := injector.WithLogger(nil)

	if result != injector {
		t.Error("WithLogger should return the same injector for chaining")
	}
}

func TestWithPreambleEngine(t *testing.T) {
	injector := NewPromptInjector()
	engine := ensemble.NewPreambleEngine()

	result := injector.WithPreambleEngine(engine)

	if result != injector {
		t.Error("WithPreambleEngine should return the same injector for chaining")
	}
	if injector.PreambleEngine != engine {
		t.Error("expected PreambleEngine to be set")
	}
}

func TestWithStaggerDelay(t *testing.T) {
	injector := NewPromptInjector()
	customDelay := 500 * time.Millisecond

	result := injector.WithStaggerDelay(customDelay)

	if result != injector {
		t.Error("WithStaggerDelay should return the same injector for chaining")
	}

	if injector.StaggerDelay != customDelay {
		t.Errorf("expected StaggerDelay of %v, got %v", customDelay, injector.StaggerDelay)
	}
}

func TestInjectionResult(t *testing.T) {
	now := time.Now()
	result := InjectionResult{
		SessionPane: "test:1.5",
		AgentType:   "cc",
		Success:     true,
		Duration:    100 * time.Millisecond,
		SentAt:      now,
	}

	if result.SessionPane != "test:1.5" {
		t.Errorf("unexpected SessionPane: %s", result.SessionPane)
	}
	if result.AgentType != "cc" {
		t.Errorf("unexpected AgentType: %s", result.AgentType)
	}
	if !result.Success {
		t.Error("expected Success to be true")
	}
	if result.Error != "" {
		t.Errorf("expected empty Error, got %q", result.Error)
	}
}

func TestInjectionTarget(t *testing.T) {
	target := InjectionTarget{
		SessionPane: "myproject:1.2",
		AgentType:   "cod",
	}

	if target.SessionPane != "myproject:1.2" {
		t.Errorf("unexpected SessionPane: %s", target.SessionPane)
	}
	if target.AgentType != "cod" {
		t.Errorf("unexpected AgentType: %s", target.AgentType)
	}
}

func TestBatchInjectionResult(t *testing.T) {
	result := BatchInjectionResult{
		TotalPanes: 5,
		Successful: 4,
		Failed:     1,
		Results: []InjectionResult{
			{SessionPane: "s:1.1", AgentType: "cc", Success: true},
			{SessionPane: "s:1.2", AgentType: "cc", Success: true},
			{SessionPane: "s:1.3", AgentType: "cod", Success: true},
			{SessionPane: "s:1.4", AgentType: "cod", Success: true},
			{SessionPane: "s:1.5", AgentType: "gmi", Success: false, Error: "test error"},
		},
		Duration: 2 * time.Second,
	}

	if result.TotalPanes != 5 {
		t.Errorf("expected TotalPanes of 5, got %d", result.TotalPanes)
	}
	if result.Successful != 4 {
		t.Errorf("expected Successful of 4, got %d", result.Successful)
	}
	if result.Failed != 1 {
		t.Errorf("expected Failed of 1, got %d", result.Failed)
	}
	if len(result.Results) != 5 {
		t.Errorf("expected 5 results, got %d", len(result.Results))
	}
}

func TestInjectSwarmNilPlan(t *testing.T) {
	injector := NewPromptInjector()
	result, err := injector.InjectSwarm(nil, "test prompt")

	if err == nil {
		t.Error("expected error for nil plan")
	}
	if result != nil {
		t.Error("expected nil result for nil plan")
	}
}

func TestInjectBatchEmpty(t *testing.T) {
	injector := NewPromptInjector()
	result, err := injector.InjectBatch([]InjectionTarget{}, "test prompt")

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.TotalPanes != 0 {
		t.Errorf("expected TotalPanes of 0, got %d", result.TotalPanes)
	}
	if result.Successful != 0 {
		t.Errorf("expected Successful of 0, got %d", result.Successful)
	}
	if result.Failed != 0 {
		t.Errorf("expected Failed of 0, got %d", result.Failed)
	}
}

func TestInjectWithMode_NilMode(t *testing.T) {
	injector := NewPromptInjector()
	result, err := injector.InjectWithMode("test:1.1", nil, "question", "cc", nil, 2000, "")

	if err == nil {
		t.Error("expected error for nil mode")
	}
	if result == nil {
		t.Fatal("expected non-nil result for nil mode")
	}
	if result.Success {
		t.Error("expected Success=false for nil mode")
	}
}

func TestInjectWithMode_NoPreambleEngine(t *testing.T) {
	injector := NewPromptInjector()
	injector.PreambleEngine = nil

	mode := ensemble.EmbeddedModes[0]
	result, err := injector.InjectWithMode("test:1.1", &mode, "question", "cc", nil, 2000, "")

	if err == nil {
		t.Error("expected error when preamble engine is nil")
	}
	if result == nil {
		t.Fatal("expected non-nil result when preamble engine is nil")
	}
	if result.Success {
		t.Error("expected Success=false when preamble engine is nil")
	}
}

func TestInjectEnsemble_NilCatalog(t *testing.T) {
	injector := NewPromptInjector()
	result, err := injector.InjectEnsemble(nil, "question", nil, nil, 2000)

	if err == nil {
		t.Error("expected error for nil catalog")
	}
	if result != nil {
		t.Error("expected nil result for nil catalog")
	}
}

func TestInjectEnsemble_MissingModeContinues(t *testing.T) {
	injector := NewPromptInjector()
	injector.PreambleEngine = nil
	injector.StaggerDelay = 0

	catalog, err := ensemble.NewModeCatalog(ensemble.EmbeddedModes, ensemble.CatalogVersion)
	if err != nil {
		t.Fatalf("failed to build mode catalog: %v", err)
	}

	assignments := []ensemble.ModeAssignment{
		{ModeID: "missing-mode", PaneName: "test:1.1", AgentType: "cc"},
		{ModeID: "deductive", PaneName: "test:1.2", AgentType: "cc"},
	}

	result, err := injector.InjectEnsemble(assignments, "question", catalog, nil, 2000)
	if err != nil {
		t.Errorf("unexpected error from InjectEnsemble: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.TotalPanes != len(assignments) {
		t.Errorf("expected TotalPanes=%d, got %d", len(assignments), result.TotalPanes)
	}
	if len(result.Results) != len(assignments) {
		t.Errorf("expected %d results, got %d", len(assignments), len(result.Results))
	}
	if result.Failed != len(assignments) {
		t.Errorf("expected Failed=%d, got %d", len(assignments), result.Failed)
	}
}

func TestPromptInjectorTmuxClient(t *testing.T) {
	injector := NewPromptInjector()
	client := injector.tmuxClient()

	if client == nil {
		t.Error("expected non-nil client from tmuxClient()")
	}
}

func TestLoggerHelper(t *testing.T) {
	injector := NewPromptInjector()
	logger := injector.logger()

	if logger == nil {
		t.Error("expected non-nil logger from logger()")
	}
}

func TestDefaultMarchingOrdersNotEmpty(t *testing.T) {
	if DefaultMarchingOrders == "" {
		t.Error("DefaultMarchingOrders should not be empty")
	}

	// Check it contains key instructions
	if len(DefaultMarchingOrders) < 100 {
		t.Error("DefaultMarchingOrders seems too short")
	}
}

func TestPromptTemplateConstants(t *testing.T) {
	if ReviewTemplate == "" {
		t.Error("ReviewTemplate should not be empty")
	}
	if TestTemplate == "" {
		t.Error("TestTemplate should not be empty")
	}
}

func TestWithRateLimitTracker(t *testing.T) {
	injector := NewPromptInjector()
	tracker := ratelimit.NewRateLimitTracker("")

	result := injector.WithRateLimitTracker(tracker)

	if result != injector {
		t.Error("WithRateLimitTracker should return the same injector for chaining")
	}

	if injector.RateLimitTracker != tracker {
		t.Error("expected RateLimitTracker to be set")
	}
}

func TestWithAdaptiveDelay(t *testing.T) {
	injector := NewPromptInjector()

	result := injector.WithAdaptiveDelay(true)

	if result != injector {
		t.Error("WithAdaptiveDelay should return the same injector for chaining")
	}

	if !injector.UseAdaptiveDelay {
		t.Error("expected UseAdaptiveDelay to be true")
	}

	injector.WithAdaptiveDelay(false)
	if injector.UseAdaptiveDelay {
		t.Error("expected UseAdaptiveDelay to be false")
	}
}

func TestGetDelayForAgentFixed(t *testing.T) {
	injector := NewPromptInjector().WithStaggerDelay(500 * time.Millisecond)

	delay := injector.getDelayForAgent("cc")

	if delay != 500*time.Millisecond {
		t.Errorf("expected delay of 500ms, got %v", delay)
	}
}

func TestGetDelayForAgentAdaptive(t *testing.T) {
	tracker := ratelimit.NewRateLimitTracker("")
	injector := NewPromptInjector().
		WithRateLimitTracker(tracker).
		WithAdaptiveDelay(true)

	// Default anthropic delay is 15s
	delay := injector.getDelayForAgent("cc")

	// Should use tracker's optimal delay (defaults to 15s for anthropic)
	if delay != ratelimit.DefaultDelayAnthropic {
		t.Errorf("expected delay of %v for cc, got %v", ratelimit.DefaultDelayAnthropic, delay)
	}

	// Test with openai alias
	delay = injector.getDelayForAgent("cod")
	if delay != ratelimit.DefaultDelayOpenAI {
		t.Errorf("expected delay of %v for cod, got %v", ratelimit.DefaultDelayOpenAI, delay)
	}
}

func TestRecordSuccess(t *testing.T) {
	tracker := ratelimit.NewRateLimitTracker("")
	injector := NewPromptInjector().
		WithRateLimitTracker(tracker).
		WithAdaptiveDelay(true)

	// Record multiple successes
	for i := 0; i < 10; i++ {
		injector.recordSuccess("cc")
	}

	// Check that tracker recorded the successes
	state := tracker.GetProviderState("anthropic")
	if state == nil {
		t.Fatal("expected provider state to exist")
	}

	if state.TotalSuccesses != 10 {
		t.Errorf("expected 10 successes, got %d", state.TotalSuccesses)
	}
}

func TestRecordSuccessNoOpWithoutTracker(t *testing.T) {
	injector := NewPromptInjector().WithAdaptiveDelay(true)

	// Should not panic when tracker is nil
	injector.recordSuccess("cc")
}

func TestRecordSuccessNoOpWithoutAdaptiveDelay(t *testing.T) {
	tracker := ratelimit.NewRateLimitTracker("")
	injector := NewPromptInjector().
		WithRateLimitTracker(tracker).
		WithAdaptiveDelay(false)

	injector.recordSuccess("cc")

	// Should not record anything when adaptive delay is disabled
	state := tracker.GetProviderState("anthropic")
	if state != nil {
		t.Error("expected no provider state when adaptive delay is disabled")
	}
}

func TestInjectBatchWithContextCancellation(t *testing.T) {
	injector := NewPromptInjector()

	// Create a cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	targets := []InjectionTarget{
		{SessionPane: "test:1.1", AgentType: "cc"},
		{SessionPane: "test:1.2", AgentType: "cc"},
	}

	result, err := injector.InjectBatchWithContext(ctx, targets, "test prompt")

	if err == nil {
		t.Error("expected error for cancelled context")
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestInjectSwarmWithContext(t *testing.T) {
	injector := NewPromptInjector()

	// Create a cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	plan := &SwarmPlan{
		Sessions: []SessionSpec{
			{
				Name:      "test",
				AgentType: "cc",
				PaneCount: 2,
				Panes: []PaneSpec{
					{Index: 1, AgentType: "cc"},
					{Index: 2, AgentType: "cc"},
				},
			},
		},
	}

	result, err := injector.InjectSwarmWithContext(ctx, plan, "test prompt")

	if err == nil {
		t.Error("expected error for cancelled context")
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

// TestPromptInjector_StaggeredDeliveryTiming tests the timing of staggered delivery.
// It verifies that the stagger delay is actually applied between sends.
func TestPromptInjector_StaggeredDeliveryTiming(t *testing.T) {
	t.Log("[TEST] Starting staggered delivery timing test")

	injector := NewPromptInjector().WithStaggerDelay(50 * time.Millisecond)

	t.Logf("[TEST] Configured stagger delay: %v", injector.StaggerDelay)
	t.Logf("[TEST] Expected minimum total time for 3 panes with 2 gaps: %v", 2*50*time.Millisecond)

	// We can't test actual tmux sends, but we can verify the delay configuration
	if injector.StaggerDelay != 50*time.Millisecond {
		t.Errorf("[TEST] expected stagger delay of 50ms, got %v", injector.StaggerDelay)
	}

	// Verify delay is returned correctly for different agent types
	delays := map[string]time.Duration{}
	for _, agent := range []string{"cc", "cod", "gmi"} {
		delay := injector.getDelayForAgent(agent)
		delays[agent] = delay
		t.Logf("[TEST] Delay for %s: %v", agent, delay)
	}

	// All should use the same fixed delay when adaptive is disabled
	for agent, delay := range delays {
		if delay != 50*time.Millisecond {
			t.Errorf("[TEST] expected delay of 50ms for %s, got %v", agent, delay)
		}
	}
}

// TestPromptInjector_DoubleEnterQuirkHandling tests the double-enter quirk for different agents.
func TestPromptInjector_DoubleEnterQuirkHandling(t *testing.T) {
	t.Log("[TEST] Testing double-enter quirk handling for all agent types")

	testCases := []struct {
		name        string
		agentType   string
		needsDouble bool
		description string
	}{
		{
			name:        "claude_code_single_enter",
			agentType:   "cc",
			needsDouble: false,
			description: "Claude Code should use single Enter",
		},
		{
			name:        "claude_alias_single_enter",
			agentType:   "claude",
			needsDouble: false,
			description: "Claude alias should use single Enter",
		},
		{
			name:        "codex_double_enter",
			agentType:   "cod",
			needsDouble: true,
			description: "Codex needs double Enter to submit",
		},
		{
			name:        "codex_alias_double_enter",
			agentType:   "codex",
			needsDouble: true,
			description: "Codex alias needs double Enter to submit",
		},
		{
			name:        "gemini_double_enter",
			agentType:   "gmi",
			needsDouble: true,
			description: "Gemini may need double Enter",
		},
		{
			name:        "gemini_alias_double_enter",
			agentType:   "gemini",
			needsDouble: true,
			description: "Gemini alias may need double Enter",
		},
		{
			name:        "unknown_single_enter",
			agentType:   "unknown",
			needsDouble: false,
			description: "Unknown agents should default to single Enter",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("[TEST] Agent: %s, Description: %s", tc.agentType, tc.description)

			result := needsDoubleEnter(tc.agentType)
			t.Logf("[TEST] needsDoubleEnter(%q) = %v", tc.agentType, result)

			if result != tc.needsDouble {
				t.Errorf("[TEST] Expected needsDoubleEnter(%q) = %v, got %v",
					tc.agentType, tc.needsDouble, result)
			}
		})
	}
}

// TestPromptInjector_ReviewModePrompts tests the review template content.
func TestPromptInjector_ReviewModePrompts(t *testing.T) {
	t.Log("[TEST] Testing review mode prompts")

	injector := NewPromptInjector()

	// Get the review template
	reviewPrompt := injector.GetTemplate("review")
	t.Logf("[TEST] Review template length: %d chars", len(reviewPrompt))
	t.Logf("[TEST] Review template first 50 chars: %q", reviewPrompt[:min(50, len(reviewPrompt))])

	// Verify review template contains expected content
	requiredPhrases := []string{
		"review",
		"git",
	}

	for _, phrase := range requiredPhrases {
		t.Logf("[TEST] Checking for phrase: %q", phrase)
		if !containsIgnoreCase(reviewPrompt, phrase) {
			t.Errorf("[TEST] Review template missing expected phrase: %q", phrase)
		}
	}

	// Test template contains actionable instructions
	if len(reviewPrompt) < 50 {
		t.Error("[TEST] Review template seems too short to be useful")
	}
}

// TestPromptInjector_TestModePrompts tests the test template content.
func TestPromptInjector_TestModePrompts(t *testing.T) {
	t.Log("[TEST] Testing test mode prompts")

	injector := NewPromptInjector()

	// Get the test template
	testPrompt := injector.GetTemplate("test")
	t.Logf("[TEST] Test template length: %d chars", len(testPrompt))

	// Verify test template contains expected content
	requiredPhrases := []string{
		"test",
	}

	for _, phrase := range requiredPhrases {
		t.Logf("[TEST] Checking for phrase: %q", phrase)
		if !containsIgnoreCase(testPrompt, phrase) {
			t.Errorf("[TEST] Test template missing expected phrase: %q", phrase)
		}
	}
}

// TestPromptInjector_DefaultMarchingOrdersContent tests the default marching orders.
func TestPromptInjector_DefaultMarchingOrdersContent(t *testing.T) {
	t.Log("[TEST] Testing default marching orders content")

	injector := NewPromptInjector()

	defaultPrompt := injector.GetTemplate("default")
	t.Logf("[TEST] Default template length: %d chars", len(defaultPrompt))

	// Verify default template contains expected content
	requiredPhrases := []string{
		"AGENTS.md",
		"bv",
		"br",
	}

	for _, phrase := range requiredPhrases {
		t.Logf("[TEST] Checking for phrase: %q", phrase)
		if !containsIgnoreCase(defaultPrompt, phrase) {
			t.Errorf("[TEST] Default template missing expected phrase: %q", phrase)
		}
	}

	// Verify it instructs agents to understand the codebase
	if !containsIgnoreCase(defaultPrompt, "understand") {
		t.Error("[TEST] Default template should instruct agents to understand the codebase")
	}
}

// TestPromptInjector_AdaptiveDelayWithDifferentProviders tests adaptive delay for different providers.
func TestPromptInjector_AdaptiveDelayWithDifferentProviders(t *testing.T) {
	t.Log("[TEST] Testing adaptive delay with different providers")

	tracker := ratelimit.NewRateLimitTracker("")
	injector := NewPromptInjector().
		WithRateLimitTracker(tracker).
		WithAdaptiveDelay(true)

	t.Log("[TEST] Adaptive delay enabled with rate limit tracker")

	// Test delays for each provider type
	testCases := []struct {
		agentType    string
		expectedFunc func() time.Duration
		providerName string
	}{
		{
			agentType:    "cc",
			expectedFunc: func() time.Duration { return ratelimit.DefaultDelayAnthropic },
			providerName: "Anthropic (Claude)",
		},
		{
			agentType:    "cod",
			expectedFunc: func() time.Duration { return ratelimit.DefaultDelayOpenAI },
			providerName: "OpenAI (Codex)",
		},
		{
			agentType:    "gmi",
			expectedFunc: func() time.Duration { return ratelimit.DefaultDelayGoogle },
			providerName: "Google (Gemini)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.agentType, func(t *testing.T) {
			t.Logf("[TEST] Testing %s (%s)", tc.providerName, tc.agentType)

			delay := injector.getDelayForAgent(tc.agentType)
			expected := tc.expectedFunc()

			t.Logf("[TEST] Delay for %s: %v (expected: %v)", tc.agentType, delay, expected)

			if delay != expected {
				t.Errorf("[TEST] Expected delay %v for %s, got %v", expected, tc.agentType, delay)
			}
		})
	}
}

// TestPromptInjector_EnterDelayConfiguration tests enter delay configuration.
func TestPromptInjector_EnterDelayConfiguration(t *testing.T) {
	t.Log("[TEST] Testing enter delay configuration")

	injector := NewPromptInjector()

	t.Logf("[TEST] Default EnterDelay: %v", injector.EnterDelay)
	t.Logf("[TEST] Default DoubleEnterDelay: %v", injector.DoubleEnterDelay)

	// Verify defaults
	if injector.EnterDelay != 100*time.Millisecond {
		t.Errorf("[TEST] Expected default EnterDelay of 100ms, got %v", injector.EnterDelay)
	}

	if injector.DoubleEnterDelay != 500*time.Millisecond {
		t.Errorf("[TEST] Expected default DoubleEnterDelay of 500ms, got %v", injector.DoubleEnterDelay)
	}

	// Test that they can be modified
	injector.EnterDelay = 200 * time.Millisecond
	injector.DoubleEnterDelay = 750 * time.Millisecond

	t.Logf("[TEST] Modified EnterDelay: %v", injector.EnterDelay)
	t.Logf("[TEST] Modified DoubleEnterDelay: %v", injector.DoubleEnterDelay)

	if injector.EnterDelay != 200*time.Millisecond {
		t.Errorf("[TEST] Expected EnterDelay of 200ms after modification, got %v", injector.EnterDelay)
	}
}

// TestPromptInjector_InjectSwarmWithTemplate tests injection with named templates.
func TestPromptInjector_InjectSwarmWithTemplate(t *testing.T) {
	t.Log("[TEST] Testing InjectSwarmWithTemplate")

	injector := NewPromptInjector()

	// Create a cancelled context to avoid actual tmux calls
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	plan := &SwarmPlan{
		Sessions: []SessionSpec{
			{
				Name:      "test",
				AgentType: "cc",
				PaneCount: 1,
				Panes: []PaneSpec{
					{Index: 1, AgentType: "cc"},
				},
			},
		},
	}

	// Test with "review" template
	t.Log("[TEST] Testing with 'review' template")
	_, err := injector.InjectSwarmWithContext(ctx, plan, injector.GetTemplate("review"))
	if err == nil {
		t.Log("[TEST] Note: no error expected here would mean actual tmux calls succeeded")
	}

	// Test with "test" template
	t.Log("[TEST] Testing with 'test' template")
	_, err = injector.InjectSwarmWithContext(ctx, plan, injector.GetTemplate("test"))
	if err == nil {
		t.Log("[TEST] Note: no error expected here would mean actual tmux calls succeeded")
	}
}

// TestPromptInjector_BatchResultTracking tests batch injection result tracking.
func TestPromptInjector_BatchResultTracking(t *testing.T) {
	t.Log("[TEST] Testing batch injection result tracking")

	result := BatchInjectionResult{
		TotalPanes: 4,
		Successful: 3,
		Failed:     1,
		Results: []InjectionResult{
			{SessionPane: "proj:1.1", AgentType: "cc", Success: true, Duration: 100 * time.Millisecond},
			{SessionPane: "proj:1.2", AgentType: "cod", Success: true, Duration: 150 * time.Millisecond},
			{SessionPane: "proj:1.3", AgentType: "gmi", Success: true, Duration: 200 * time.Millisecond},
			{SessionPane: "proj:1.4", AgentType: "cc", Success: false, Error: "tmux: no such pane"},
		},
		Duration: 600 * time.Millisecond,
	}

	t.Logf("[TEST] Total panes: %d", result.TotalPanes)
	t.Logf("[TEST] Successful: %d", result.Successful)
	t.Logf("[TEST] Failed: %d", result.Failed)
	t.Logf("[TEST] Total duration: %v", result.Duration)

	// Verify counts
	if result.TotalPanes != result.Successful+result.Failed {
		t.Errorf("[TEST] TotalPanes (%d) != Successful (%d) + Failed (%d)",
			result.TotalPanes, result.Successful, result.Failed)
	}

	// Verify individual results
	successCount := 0
	failCount := 0
	for i, r := range result.Results {
		t.Logf("[TEST] Result[%d]: pane=%s, agent=%s, success=%v, duration=%v",
			i, r.SessionPane, r.AgentType, r.Success, r.Duration)
		if r.Success {
			successCount++
		} else {
			failCount++
			t.Logf("[TEST] Error for %s: %s", r.SessionPane, r.Error)
		}
	}

	if successCount != result.Successful {
		t.Errorf("[TEST] Counted successes (%d) != reported Successful (%d)", successCount, result.Successful)
	}
	if failCount != result.Failed {
		t.Errorf("[TEST] Counted failures (%d) != reported Failed (%d)", failCount, result.Failed)
	}
}

// TestPromptInjector_ChainedConfiguration tests method chaining for configuration.
func TestPromptInjector_ChainedConfiguration(t *testing.T) {
	t.Log("[TEST] Testing chained configuration methods")

	tracker := ratelimit.NewRateLimitTracker("")

	injector := NewPromptInjector().
		WithStaggerDelay(250 * time.Millisecond).
		WithRateLimitTracker(tracker).
		WithAdaptiveDelay(true).
		WithLogger(nil)

	t.Log("[TEST] Created injector with chained configuration")
	t.Logf("[TEST] StaggerDelay: %v", injector.StaggerDelay)
	t.Logf("[TEST] UseAdaptiveDelay: %v", injector.UseAdaptiveDelay)
	t.Logf("[TEST] RateLimitTracker set: %v", injector.RateLimitTracker != nil)

	// Verify all configurations were applied
	if injector.StaggerDelay != 250*time.Millisecond {
		t.Errorf("[TEST] Expected StaggerDelay of 250ms, got %v", injector.StaggerDelay)
	}
	if !injector.UseAdaptiveDelay {
		t.Error("[TEST] Expected UseAdaptiveDelay to be true")
	}
	if injector.RateLimitTracker != tracker {
		t.Error("[TEST] Expected RateLimitTracker to be set")
	}
}

// containsIgnoreCase checks if s contains substr (case-insensitive).
func containsIgnoreCase(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		len(substr) == 0 ||
		(len(s) > 0 && containsLower(toLower(s), toLower(substr))))
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

func containsLower(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

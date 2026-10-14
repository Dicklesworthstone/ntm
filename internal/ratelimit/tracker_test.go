// Package ratelimit provides rate limit tracking and adaptive delay management for AI agents.
package ratelimit

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestNewRateLimitTracker(t *testing.T) {
	tracker := NewRateLimitTracker("/tmp/test")
	if tracker == nil {
		t.Fatal("NewRateLimitTracker returned nil")
	}
	if tracker.dataDir != "/tmp/test" {
		t.Errorf("dataDir = %q, want %q", tracker.dataDir, "/tmp/test")
	}
	if tracker.state == nil {
		t.Error("state map should not be nil")
	}
	if tracker.history == nil {
		t.Error("history map should not be nil")
	}
}

func TestGetDefaultDelay(t *testing.T) {
	tests := []struct {
		provider string
		want     time.Duration
	}{
		{"anthropic", DefaultDelayAnthropic},
		{"claude", DefaultDelayAnthropic},
		{"openai", DefaultDelayOpenAI},
		{"gpt", DefaultDelayOpenAI},
		{"google", DefaultDelayGoogle},
		{"gemini", DefaultDelayGoogle},
		{"unknown", DefaultDelayOpenAI},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := getDefaultDelay(tt.provider)
			if got != tt.want {
				t.Errorf("getDefaultDelay(%q) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}

func TestGetMinDelay(t *testing.T) {
	tests := []struct {
		provider string
		want     time.Duration
	}{
		{"anthropic", MinDelayAnthropic},
		{"openai", MinDelayOpenAI},
		{"google", MinDelayGoogle},
		{"unknown", MinDelayOpenAI},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := getMinDelay(tt.provider)
			if got != tt.want {
				t.Errorf("getMinDelay(%q) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}

func TestGetOptimalDelay_Default(t *testing.T) {
	tracker := NewRateLimitTracker("")

	// Should return default for unknown provider
	delay := tracker.GetOptimalDelay("anthropic")
	if delay != DefaultDelayAnthropic {
		t.Errorf("GetOptimalDelay() = %v, want %v", delay, DefaultDelayAnthropic)
	}
}

func TestRecordRateLimit(t *testing.T) {
	tracker := NewRateLimitTracker("")

	// Record a rate limit
	tracker.RecordRateLimit("anthropic", "spawn")

	// Check that delay increased by 50%
	state := tracker.GetProviderState("anthropic")
	if state == nil {
		t.Fatal("state should not be nil")
	}

	expectedDelay := time.Duration(float64(DefaultDelayAnthropic) * delayIncreaseRate)
	if state.CurrentDelay != expectedDelay {
		t.Errorf("CurrentDelay = %v, want %v", state.CurrentDelay, expectedDelay)
	}
	if state.TotalRateLimits != 1 {
		t.Errorf("TotalRateLimits = %d, want 1", state.TotalRateLimits)
	}
	if state.ConsecutiveSuccess != 0 {
		t.Errorf("ConsecutiveSuccess = %d, want 0", state.ConsecutiveSuccess)
	}
}

func TestRecordSuccess(t *testing.T) {
	tracker := NewRateLimitTracker("")

	// Record 9 successes - should not decrease yet
	for i := 0; i < 9; i++ {
		tracker.RecordSuccess("anthropic")
	}

	state := tracker.GetProviderState("anthropic")
	if state.CurrentDelay != DefaultDelayAnthropic {
		t.Errorf("Delay should not change before %d successes, got %v", successesBeforeDecrease, state.CurrentDelay)
	}

	// 10th success should trigger decrease
	tracker.RecordSuccess("anthropic")

	state = tracker.GetProviderState("anthropic")
	expectedDelay := time.Duration(float64(DefaultDelayAnthropic) * delayDecreaseRate)
	if state.CurrentDelay != expectedDelay {
		t.Errorf("CurrentDelay = %v, want %v after 10 successes", state.CurrentDelay, expectedDelay)
	}
	if state.TotalSuccesses != 10 {
		t.Errorf("TotalSuccesses = %d, want 10", state.TotalSuccesses)
	}
}

func TestRecordSuccess_RespectMinDelay(t *testing.T) {
	tracker := NewRateLimitTracker("")

	// Manually set delay to just above minimum
	tracker.mu.Lock()
	tracker.state["anthropic"] = &ProviderState{
		CurrentDelay: MinDelayAnthropic + time.Second,
	}
	tracker.mu.Unlock()

	// Record 10 successes - should not go below min
	for i := 0; i < 10; i++ {
		tracker.RecordSuccess("anthropic")
	}

	state := tracker.GetProviderState("anthropic")
	if state.CurrentDelay < MinDelayAnthropic {
		t.Errorf("Delay should not go below min %v, got %v", MinDelayAnthropic, state.CurrentDelay)
	}
}

func TestRecordRateLimit_ResetsConsecutiveSuccess(t *testing.T) {
	tracker := NewRateLimitTracker("")

	// Build up some successes
	for i := 0; i < 5; i++ {
		tracker.RecordSuccess("anthropic")
	}

	state := tracker.GetProviderState("anthropic")
	if state.ConsecutiveSuccess != 5 {
		t.Errorf("ConsecutiveSuccess = %d, want 5", state.ConsecutiveSuccess)
	}

	// Rate limit should reset consecutive successes
	tracker.RecordRateLimit("anthropic", "send")

	state = tracker.GetProviderState("anthropic")
	if state.ConsecutiveSuccess != 0 {
		t.Errorf("ConsecutiveSuccess should reset to 0 after rate limit, got %d", state.ConsecutiveSuccess)
	}
}

func TestGetRecentEvents(t *testing.T) {
	tracker := NewRateLimitTracker("")

	// Record some events
	tracker.RecordRateLimit("anthropic", "spawn")
	tracker.RecordRateLimit("anthropic", "send")
	tracker.RecordRateLimit("anthropic", "spawn")

	events := tracker.GetRecentEvents("anthropic", 2)
	if len(events) != 2 {
		t.Errorf("GetRecentEvents() returned %d events, want 2", len(events))
	}

	// Should be most recent events
	if events[1].Action != "spawn" {
		t.Errorf("Most recent event action = %q, want 'spawn'", events[1].Action)
	}
}

func TestGetRecentEvents_Empty(t *testing.T) {
	tracker := NewRateLimitTracker("")

	events := tracker.GetRecentEvents("anthropic", 5)
	if events != nil {
		t.Error("GetRecentEvents should return nil for empty history")
	}
}

func TestGetAllProviders(t *testing.T) {
	tracker := NewRateLimitTracker("")

	tracker.RecordSuccess("anthropic")
	tracker.RecordSuccess("openai")
	tracker.RecordSuccess("google")

	providers := tracker.GetAllProviders()
	if len(providers) != 3 {
		t.Errorf("GetAllProviders() returned %d providers, want 3", len(providers))
	}
}

func TestReset(t *testing.T) {
	tracker := NewRateLimitTracker("")

	tracker.RecordRateLimit("anthropic", "spawn")
	tracker.RecordSuccess("anthropic")

	tracker.Reset("anthropic")

	state := tracker.GetProviderState("anthropic")
	if state != nil {
		t.Error("state should be nil after Reset")
	}

	events := tracker.GetRecentEvents("anthropic", 10)
	if events != nil {
		t.Error("events should be nil after Reset")
	}
}

func TestResetAll(t *testing.T) {
	tracker := NewRateLimitTracker("")

	tracker.RecordRateLimit("anthropic", "spawn")
	tracker.RecordRateLimit("openai", "send")

	tracker.ResetAll()

	providers := tracker.GetAllProviders()
	if len(providers) != 0 {
		t.Errorf("GetAllProviders() should return empty after ResetAll, got %d", len(providers))
	}
}

func TestPersistence(t *testing.T) {
	tmpDir := t.TempDir()

	// Create tracker and record data
	tracker1 := NewRateLimitTracker(tmpDir)
	tracker1.RecordRateLimit("anthropic", "spawn")
	tracker1.RecordSuccess("anthropic")
	tracker1.RecordSuccess("openai")

	// Save
	if err := tracker1.SaveToDir(tmpDir); err != nil {
		t.Fatalf("SaveToDir failed: %v", err)
	}

	// Verify file exists
	path := filepath.Join(tmpDir, ".ntm", "rate_limits.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("rate_limits.json not created")
	}

	// Create new tracker and load
	tracker2 := NewRateLimitTracker(tmpDir)
	if err := tracker2.LoadFromDir(tmpDir); err != nil {
		t.Fatalf("LoadFromDir failed: %v", err)
	}

	// Verify data was loaded correctly
	state := tracker2.GetProviderState("anthropic")
	if state == nil {
		t.Fatal("anthropic state not loaded")
	}
	if state.TotalRateLimits != 1 {
		t.Errorf("TotalRateLimits = %d, want 1", state.TotalRateLimits)
	}
	if state.TotalSuccesses != 1 {
		t.Errorf("TotalSuccesses = %d, want 1", state.TotalSuccesses)
	}

	// Check openai was also loaded
	openaiState := tracker2.GetProviderState("openai")
	if openaiState == nil {
		t.Fatal("openai state not loaded")
	}
}

func TestLoadFromDir_NoFile(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewRateLimitTracker(tmpDir)

	// Should not error when file doesn't exist
	if err := tracker.LoadFromDir(tmpDir); err != nil {
		t.Errorf("LoadFromDir should not error for missing file: %v", err)
	}
}

func TestConcurrent(t *testing.T) {
	tracker := NewRateLimitTracker("")
	var wg sync.WaitGroup

	// Simulate concurrent access from multiple goroutines
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			provider := "anthropic"
			if id%2 == 0 {
				provider = "openai"
			}
			for j := 0; j < 100; j++ {
				if j%10 == 0 {
					tracker.RecordRateLimit(provider, "spawn")
				} else {
					tracker.RecordSuccess(provider)
				}
				_ = tracker.GetOptimalDelay(provider)
			}
		}(i)
	}

	wg.Wait()

	// Should have tracked both providers without panic
	providers := tracker.GetAllProviders()
	if len(providers) != 2 {
		t.Errorf("Expected 2 providers, got %d", len(providers))
	}
}

func TestFormatDelay(t *testing.T) {
	tests := []struct {
		delay time.Duration
		want  string
	}{
		{500 * time.Millisecond, "500ms"},
		{1 * time.Second, "1.0s"},
		{5500 * time.Millisecond, "5.5s"},
		{90 * time.Second, "1.5m"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := FormatDelay(tt.delay)
			if got != tt.want {
				t.Errorf("FormatDelay(%v) = %q, want %q", tt.delay, got, tt.want)
			}
		})
	}
}

func TestNormalizeProvider(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"anthropic", "anthropic"},
		{"claude", "anthropic"},
		{"claude-code", "anthropic"},
		{"cc", "anthropic"},
		{"openai", "openai"},
		{"gpt", "openai"},
		{"chatgpt", "openai"},
		{"codex", "openai"},
		{"cod", "openai"},
		{"openai-codex", "openai"},
		{" OpenAI-Codex ", "openai"},
		{"google", "google"},
		{"gemini", "google"},
		{"gmi", "google"},
		{"google-gemini", "google"},
		{" CLAUDE_CODE ", "anthropic"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeProvider(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeProvider(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestHistoryTruncation(t *testing.T) {
	tracker := NewRateLimitTracker("")

	// Record more than 100 events
	for i := 0; i < 150; i++ {
		tracker.RecordRateLimit("anthropic", "spawn")
	}

	events := tracker.GetRecentEvents("anthropic", 200)
	if len(events) > 100 {
		t.Errorf("History should be truncated to 100, got %d", len(events))
	}
}

func TestGetProviderState_ReturnsCopy(t *testing.T) {
	tracker := NewRateLimitTracker("")
	tracker.RecordSuccess("anthropic")

	// Get state
	state1 := tracker.GetProviderState("anthropic")

	// Modify the returned state
	state1.TotalSuccesses = 999

	// Get state again - should not reflect the modification
	state2 := tracker.GetProviderState("anthropic")
	if state2.TotalSuccesses == 999 {
		t.Error("GetProviderState should return a copy, not the original")
	}
}

func TestAdaptiveLearning_MultipleRateLimits(t *testing.T) {
	tracker := NewRateLimitTracker("")

	// Record 3 consecutive rate limits
	tracker.RecordRateLimit("anthropic", "spawn")
	tracker.RecordRateLimit("anthropic", "spawn")
	tracker.RecordRateLimit("anthropic", "spawn")

	state := tracker.GetProviderState("anthropic")

	// Delay should be: 15s * 1.5 * 1.5 * 1.5 = 50.625s
	expectedDelay := time.Duration(float64(DefaultDelayAnthropic) * delayIncreaseRate * delayIncreaseRate * delayIncreaseRate)
	if state.CurrentDelay != expectedDelay {
		t.Errorf("CurrentDelay = %v, want %v after 3 rate limits", state.CurrentDelay, expectedDelay)
	}
}

func TestParseWaitSeconds(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   int
	}{
		{"retry_after", "Retry-After: 15", 15},
		{"try_again_seconds", "try again in 3s", 3},
		{"wait_seconds", "wait 5 seconds", 5},
		{"retry_minutes", "retry in 2m", 120},
		{"cooldown_seconds", "10 seconds cooldown", 10},
		{"no_wait", "rate limit exceeded", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseWaitSeconds(tt.output); got != tt.want {
				t.Errorf("ParseWaitSeconds(%q) = %d, want %d", tt.output, got, tt.want)
			}
		})
	}
}

// =============================================================================
// RecordRateLimitWithCooldown (bd-8gkp7)
// =============================================================================

func TestRecordRateLimitWithCooldown(t *testing.T) {
	t.Parallel()
	tracker := NewRateLimitTracker("")

	// With explicit positive waitSeconds
	cooldown := tracker.RecordRateLimitWithCooldown("anthropic", "spawn", 30)
	if cooldown != 30*time.Second {
		t.Errorf("cooldown = %v, want 30s", cooldown)
	}
	if !tracker.IsInCooldown("anthropic") {
		t.Error("expected IsInCooldown=true after setting cooldown")
	}
	remaining := tracker.CooldownRemaining("anthropic")
	if remaining <= 0 || remaining > 30*time.Second {
		t.Errorf("CooldownRemaining = %v, expected (0, 30s]", remaining)
	}
}

func TestRecordRateLimitWithCooldown_ZeroWait(t *testing.T) {
	t.Parallel()
	tracker := NewRateLimitTracker("")

	// With waitSeconds <= 0, should use adaptive delay
	cooldown := tracker.RecordRateLimitWithCooldown("anthropic", "send", 0)
	if cooldown <= 0 {
		t.Errorf("expected positive cooldown from adaptive delay, got %v", cooldown)
	}
	// The adaptive delay after one rate limit should be default * 1.5
	expectedApprox := time.Duration(float64(DefaultDelayAnthropic) * delayIncreaseRate)
	if cooldown != expectedApprox {
		t.Errorf("cooldown = %v, want ~%v (default * increase rate)", cooldown, expectedApprox)
	}
}

func TestRecordRateLimitWithCooldown_ExtendsNotShrinks(t *testing.T) {
	t.Parallel()
	tracker := NewRateLimitTracker("")

	// Set a long cooldown first
	tracker.RecordRateLimitWithCooldown("anthropic", "spawn", 60)
	// Then a shorter one — should not shrink
	tracker.RecordRateLimitWithCooldown("anthropic", "spawn", 5)
	remaining := tracker.CooldownRemaining("anthropic")
	if remaining < 50*time.Second {
		t.Errorf("cooldown should not shrink: remaining = %v", remaining)
	}
}

// =============================================================================
// CooldownRemaining / IsInCooldown / ClearCooldown (bd-8gkp7)
// =============================================================================

func TestCooldownRemaining_UnknownProvider(t *testing.T) {
	t.Parallel()
	tracker := NewRateLimitTracker("")

	remaining := tracker.CooldownRemaining("nonexistent")
	if remaining != 0 {
		t.Errorf("CooldownRemaining for unknown provider = %v, want 0", remaining)
	}
}

func TestIsInCooldown_NoCooldownSet(t *testing.T) {
	t.Parallel()
	tracker := NewRateLimitTracker("")

	// Just record a rate limit without cooldown
	tracker.RecordRateLimit("anthropic", "send")
	if tracker.IsInCooldown("anthropic") {
		t.Error("IsInCooldown should be false without explicit cooldown")
	}
}

func TestClearCooldown(t *testing.T) {
	t.Parallel()
	tracker := NewRateLimitTracker("")

	// Set cooldown
	tracker.RecordRateLimitWithCooldown("anthropic", "spawn", 60)
	if !tracker.IsInCooldown("anthropic") {
		t.Fatal("expected cooldown to be active")
	}

	// Clear it
	tracker.ClearCooldown("anthropic")
	if tracker.IsInCooldown("anthropic") {
		t.Error("IsInCooldown should be false after ClearCooldown")
	}
	if tracker.CooldownRemaining("anthropic") != 0 {
		t.Error("CooldownRemaining should be 0 after ClearCooldown")
	}
}

func TestClearCooldown_UnknownProvider(t *testing.T) {
	t.Parallel()
	tracker := NewRateLimitTracker("")

	// Should not panic for unknown provider
	tracker.ClearCooldown("nonexistent")
}

// =============================================================================
// Existing tests below
// =============================================================================

func TestDetectRateLimit(t *testing.T) {
	tests := []struct {
		name            string
		output          string
		wantRateLimited bool
		wantSource      string
		wantWait        int
	}{
		{
			name:            "exit_code_429",
			output:          "process exited with code 429",
			wantRateLimited: true,
			wantSource:      detectionSourceExitCode,
		},
		{
			name:            "rate_limit_text",
			output:          "Error: rate limit exceeded. Retry-After: 12",
			wantRateLimited: true,
			wantSource:      detectionSourceOutput,
			wantWait:        12,
		},
		{
			name:            "no_rate_limit",
			output:          "all good",
			wantRateLimited: false,
			wantSource:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detection := DetectRateLimit(tt.output)
			if detection.RateLimited != tt.wantRateLimited {
				t.Fatalf("DetectRateLimit(%q) RateLimited=%v, want %v", tt.output, detection.RateLimited, tt.wantRateLimited)
			}
			if detection.Source != tt.wantSource {
				t.Fatalf("DetectRateLimit(%q) Source=%q, want %q", tt.output, detection.Source, tt.wantSource)
			}
			if detection.WaitSeconds != tt.wantWait {
				t.Fatalf("DetectRateLimit(%q) WaitSeconds=%d, want %d", tt.output, detection.WaitSeconds, tt.wantWait)
			}
		})
	}
}

// =============================================================================
// Codex-specific rate-limit detection (bd-3qoly)
// =============================================================================

func TestDetectCodexRateLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want bool
	}{
		{"empty", "", false},
		{"normal_output", "Codex is working on the task...", false},
		{"openai_rate_limit", "Error: OpenAI rate limit exceeded", true},
		{"codex_rate_limit", "codex rate-limit hit, wait 30s", true},
		{"tokens_per_min", "Error: tokens per min exceeded", true},
		{"requests_per_min", "Error: requests per min limit", true},
		{"api_429", "api.openai.com returned 429", true},
		{"insufficient_quota", "Error: insufficient_quota", true},
		{"billing_limit", "billing limit reached", true},
		{"exceeded_token_limit", "exceeded token limit", true},
		{"please_try_again", "Error: please try again later", true},
		{"usage_cap_reached", "usage cap reached", true},
		{"RateLimitError", "RateLimitError: too many requests", true},
		{"not_rate_limit", "error: file not found", false},
		{"partial_match_no_trigger", "limit of items per page: 50", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := DetectCodexRateLimit(tt.text)
			if got != tt.want {
				t.Errorf("DetectCodexRateLimit(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestDetectRateLimitForAgent_CodSpecific(t *testing.T) {
	t.Parallel()

	// Codex-specific pattern that generic DetectRateLimit won't catch
	output := "Error: OpenAI rate limit triggered for codex"
	// Generic detection should catch "rate limit" substring
	generic := DetectRateLimit(output)
	if !generic.RateLimited {
		// If generic doesn't catch it, agent-specific should
		agented := DetectRateLimitForAgent(output, "cod")
		if !agented.RateLimited {
			t.Error("DetectRateLimitForAgent should detect Codex rate limit")
		}
		if agented.AgentType != "cod" {
			t.Errorf("AgentType = %q, want %q", agented.AgentType, "cod")
		}
	}

	// Ensure non-cod agents don't trigger Codex patterns
	ccOutput := "Some normal output"
	ccDetection := DetectRateLimitForAgent(ccOutput, "cc")
	if ccDetection.RateLimited {
		t.Error("Non-rate-limit output should not be detected for cc agent")
	}
}

func TestDetectRateLimitForAgent_PreservesAgentType(t *testing.T) {
	t.Parallel()

	detection := DetectRateLimitForAgent("rate limit exceeded", "cod")
	if detection.AgentType != "cod" {
		t.Errorf("AgentType = %q, want %q", detection.AgentType, "cod")
	}
	if !detection.RateLimited {
		t.Error("Expected rate limit to be detected")
	}
}

func TestDetectRateLimitForAgent_CodexAliases(t *testing.T) {
	t.Parallel()

	output := "OpenAI rate limit triggered for codex"

	for _, agentType := range []string{"codex", "openai-codex", " codex "} {
		detection := DetectRateLimitForAgent(output, agentType)
		if !detection.RateLimited {
			t.Fatalf("expected rate limit to be detected for alias %q", agentType)
		}
		if detection.Source != detectionSourceOutput {
			t.Fatalf("expected output detection source for alias %q, got %q", agentType, detection.Source)
		}
	}
}

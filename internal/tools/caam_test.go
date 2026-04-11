package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewCAAMAdapter(t *testing.T) {
	adapter := NewCAAMAdapter()
	if adapter == nil {
		t.Fatal("NewCAAMAdapter returned nil")
	}

	if adapter.Name() != ToolCAAM {
		t.Errorf("Expected name %s, got %s", ToolCAAM, adapter.Name())
	}

	if adapter.BinaryName() != "caam" {
		t.Errorf("Expected binary name 'caam', got %s", adapter.BinaryName())
	}
}

func TestCAAMAdapterImplementsInterface(t *testing.T) {
	// Ensure CAAMAdapter implements the Adapter interface
	var _ Adapter = (*CAAMAdapter)(nil)
}

func TestCAAMAdapterDetect(t *testing.T) {
	adapter := NewCAAMAdapter()

	// Test detection - result depends on whether caam is installed
	path, installed := adapter.Detect()

	// If installed, path should be non-empty
	if installed && path == "" {
		t.Error("caam detected but path is empty")
	}

	// If not installed, path should be empty
	if !installed && path != "" {
		t.Errorf("caam not detected but path is %s", path)
	}
}

func TestCAAMStatusStruct(t *testing.T) {
	status := CAAMStatus{
		Available:     true,
		Version:       "1.0.0",
		AccountsCount: 2,
		Providers:     []string{"claude", "openai"},
	}

	if !status.Available {
		t.Error("Expected Available to be true")
	}

	if status.AccountsCount != 2 {
		t.Errorf("Expected AccountsCount 2, got %d", status.AccountsCount)
	}

	if len(status.Providers) != 2 {
		t.Errorf("Expected 2 providers, got %d", len(status.Providers))
	}
}

func TestCAAMAccountStruct(t *testing.T) {
	account := CAAMAccount{
		ID:          "acc-123",
		Provider:    "claude",
		Email:       "test@example.com",
		Active:      true,
		RateLimited: false,
	}

	if account.ID != "acc-123" {
		t.Errorf("Expected ID 'acc-123', got %s", account.ID)
	}

	if account.Provider != "claude" {
		t.Errorf("Expected Provider 'claude', got %s", account.Provider)
	}

	if !account.Active {
		t.Error("Expected Active to be true")
	}
}

func TestCAAMStatusApplyAccountsTracksActiveAccountIdentity(t *testing.T) {
	status := &CAAMStatus{}
	accounts := []CAAMAccount{
		{ID: "acc-1", Provider: "openai", Active: true, Name: "Primary"},
		{ID: "acc-2", Provider: "claude", Active: false, Name: "Secondary"},
	}

	status.applyAccounts(accounts)

	if status.AccountsCount != 2 {
		t.Fatalf("AccountsCount = %d, want 2", status.AccountsCount)
	}
	if status.ActiveAccount == nil {
		t.Fatal("ActiveAccount = nil, want non-nil")
	}
	if status.ActiveAccount != &status.Accounts[0] {
		t.Fatal("ActiveAccount should point to the matching account in status.Accounts")
	}
	if status.ActiveAccount.ID != "acc-1" {
		t.Fatalf("ActiveAccount.ID = %q, want %q", status.ActiveAccount.ID, "acc-1")
	}
	if len(status.Providers) != 2 {
		t.Fatalf("Providers len = %d, want 2", len(status.Providers))
	}
	if status.Providers[0] != "claude" || status.Providers[1] != "openai" {
		t.Fatalf("Providers = %v, want [claude openai] (deterministic order)", status.Providers)
	}

	// Regression check: active account must not be a detached range-loop copy.
	status.Accounts[0].Name = "Renamed"
	if status.ActiveAccount.Name != "Renamed" {
		t.Fatalf("ActiveAccount.Name = %q, want %q", status.ActiveAccount.Name, "Renamed")
	}
}

func TestCAAMAdapterCacheInvalidation(t *testing.T) {
	adapter := NewCAAMAdapter()

	// Invalidate cache should not panic
	adapter.InvalidateCache()

	// Call again to ensure it's safe to call multiple times
	adapter.InvalidateCache()
}

func TestCAAMAdapterHealthWhenNotInstalled(t *testing.T) {
	adapter := NewCAAMAdapter()

	// If caam is not installed, Health should return non-healthy status
	_, installed := adapter.Detect()
	if installed {
		t.Skip("caam is installed, skipping not-installed test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	health, err := adapter.Health(ctx)
	if err != nil {
		t.Fatalf("Health returned unexpected error: %v", err)
	}

	if health.Healthy {
		t.Error("Expected unhealthy status when caam not installed")
	}

	if health.Message != "caam not installed" {
		t.Errorf("Expected message 'caam not installed', got %s", health.Message)
	}
}

func TestCAAMAdapterIsAvailable(t *testing.T) {
	adapter := NewCAAMAdapter()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// IsAvailable should not panic regardless of whether caam is installed
	available := adapter.IsAvailable(ctx)

	// If caam is not installed, should return false
	_, installed := adapter.Detect()
	if !installed && available {
		t.Error("IsAvailable returned true but caam is not installed")
	}
}

func TestCAAMAdapterHasMultipleAccounts(t *testing.T) {
	adapter := NewCAAMAdapter()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// HasMultipleAccounts should not panic regardless of whether caam is installed
	hasMultiple := adapter.HasMultipleAccounts(ctx)

	// If caam is not installed, should return false
	_, installed := adapter.Detect()
	if !installed && hasMultiple {
		t.Error("HasMultipleAccounts returned true but caam is not installed")
	}
}

func TestCAAMAdapterInRegistry(t *testing.T) {
	// Ensure CAAM adapter is registered in the global registry
	adapter, ok := Get(ToolCAAM)
	if !ok {
		t.Fatal("CAAM adapter not found in global registry")
	}

	if adapter.Name() != ToolCAAM {
		t.Errorf("Expected tool name %s, got %s", ToolCAAM, adapter.Name())
	}
}

func TestToolCAAMInAllTools(t *testing.T) {
	tools := AllTools()
	found := false
	for _, tool := range tools {
		if tool == ToolCAAM {
			found = true
			break
		}
	}

	if !found {
		t.Error("ToolCAAM not found in AllTools()")
	}
}

// RateLimitDetector tests

func TestNewRateLimitDetector(t *testing.T) {
	adapter := NewCAAMAdapter()
	detector := NewRateLimitDetector(adapter)

	if detector == nil {
		t.Fatal("NewRateLimitDetector returned nil")
	}

	if detector.adapter != adapter {
		t.Error("detector adapter not set correctly")
	}

	// Check that patterns are initialized for all providers
	providers := []string{"claude", "openai", "gemini"}
	for _, p := range providers {
		if patterns, ok := detector.patterns[p]; !ok {
			t.Errorf("patterns not initialized for provider %s", p)
		} else if len(patterns) == 0 {
			t.Errorf("no patterns for provider %s", p)
		}
	}
}

// TestRateLimitDetection tests that various rate limit messages are detected.
// Provider attribution is best-effort since many patterns overlap.
func TestRateLimitDetection(t *testing.T) {
	detector := NewRateLimitDetector(nil)
	detector.SetCooldownPeriod(0) // Disable cooldown for testing

	// Messages that SHOULD be detected as rate limits
	rateLimitMessages := []string{
		// Claude-like messages
		"You've hit your limit for the day.",
		"Anthropic API limit reached.",
		"Claude usage limit exceeded.",
		"Too many requests, please slow down.",
		"Please wait before trying again.",
		"Try again later.",
		// OpenAI-like messages
		"Rate limit exceeded. Try again later.",
		"HTTP 429: Too many requests.",
		"Tokens per minute limit exceeded.",
		"OpenAI API limit reached.",
		"Error: Quota exceeded for this organization.",
		// Gemini-like messages
		"Error: RESOURCE_EXHAUSTED",
		"API error: resource exhausted",
		"Gemini API limit reached.",
		"Google AI limit exceeded.",
		"Request limit reached.",
	}

	for _, msg := range rateLimitMessages {
		t.Run(msg[:min(30, len(msg))], func(t *testing.T) {
			event := detector.Check(msg, "test-pane")
			if event == nil {
				t.Errorf("Check(%q) should detect rate limit", msg)
			}
		})
	}
}

// TestRateLimitDetectorNoFalsePositives tests that normal messages aren't detected.
func TestRateLimitDetectorNoFalsePositives(t *testing.T) {
	detector := NewRateLimitDetector(nil)
	detector.SetCooldownPeriod(0) // Disable cooldown for testing

	normalMessages := []string{
		"Here is your response. Let me help you with that.",
		"The code looks correct.",
		"```go\nfunc main() { }\n```",
		"I've created the file successfully.",
		"Running the tests now...",
		"Build completed.",
		"Let me analyze this for you.",
		"The error was in the configuration.",
	}

	for _, msg := range normalMessages {
		t.Run(msg[:min(30, len(msg))], func(t *testing.T) {
			event := detector.Check(msg, "test-pane")
			if event != nil {
				t.Errorf("Check(%q) should NOT detect rate limit, got provider=%s", msg, event.Provider)
			}
		})
	}
}

// TestRateLimitDetectorProviderSpecific tests provider-specific patterns.
func TestRateLimitDetectorProviderSpecific(t *testing.T) {
	detector := NewRateLimitDetector(nil)
	detector.SetCooldownPeriod(0) // Disable cooldown for testing

	tests := []struct {
		name         string
		output       string
		wantProvider string
	}{
		// Claude-specific (checked first)
		{"claude hit limit", "You've hit your limit for the day.", "claude"},
		{"claude anthropic", "Anthropic API limit reached.", "claude"},
		{"claude too many", "Too many requests", "claude"},
		{"claude limit exceeded", "rate limit exceeded", "claude"}, // Claude matches "limit.*exceeded"
		// OpenAI-specific
		{"openai 429", "HTTP 429 error", "openai"},
		{"openai quota", "quota exceeded", "openai"},
		{"openai tokens per min", "Tokens per min limit", "openai"},
		// Gemini-specific
		{"gemini resource exhausted", "RESOURCE_EXHAUSTED", "gemini"},
		{"gemini resource exhausted lower", "resource exhausted", "gemini"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := detector.Check(tt.output, "test-pane")
			if event == nil {
				t.Fatalf("Check(%q) should detect rate limit", tt.output)
			}
			if event.Provider != tt.wantProvider {
				t.Errorf("Check(%q) provider = %q, want %q", tt.output, event.Provider, tt.wantProvider)
			}
		})
	}
}

func TestRateLimitDetectorCooldown(t *testing.T) {
	detector := NewRateLimitDetector(nil)
	detector.SetCooldownPeriod(100 * time.Millisecond)

	output := "You've hit your limit. Please wait."

	// First detection should succeed
	event1 := detector.Check(output, "pane-1")
	if event1 == nil {
		t.Fatal("First detection should succeed")
	}

	// Immediate second detection should fail (cooldown)
	event2 := detector.Check(output, "pane-1")
	if event2 != nil {
		t.Error("Second detection should be blocked by cooldown")
	}

	// Wait for cooldown to expire
	time.Sleep(150 * time.Millisecond)

	// Third detection should succeed
	event3 := detector.Check(output, "pane-1")
	if event3 == nil {
		t.Error("Third detection should succeed after cooldown")
	}
}

func TestRateLimitDetectorCallback(t *testing.T) {
	detector := NewRateLimitDetector(nil)
	detector.SetCooldownPeriod(0) // Disable cooldown for testing

	var callbackCalled bool
	var callbackProvider string
	var callbackPaneID string
	var callbackPatterns []string

	detector.SetCallback(func(provider, paneID string, patterns []string) {
		callbackCalled = true
		callbackProvider = provider
		callbackPaneID = paneID
		callbackPatterns = patterns
	})

	output := "Rate limit exceeded. Try again in 30 seconds."
	event := detector.Check(output, "my-pane")

	if event == nil {
		t.Fatal("Expected rate limit detection")
	}

	if !callbackCalled {
		t.Error("Callback should have been called")
	}

	if callbackProvider == "" {
		t.Error("Callback provider should be set")
	}

	if callbackPaneID != "my-pane" {
		t.Errorf("Callback paneID = %q, want 'my-pane'", callbackPaneID)
	}

	if len(callbackPatterns) == 0 {
		t.Error("Callback patterns should not be empty")
	}
}

func TestParseWaitTimeFromOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   int
	}{
		{
			name:   "try again in seconds",
			output: "Rate limited. Try again in 30s.",
			want:   30,
		},
		{
			name:   "wait seconds",
			output: "Please wait 60 seconds before retrying.",
			want:   60,
		},
		{
			name:   "retry after",
			output: "Retry after 45 seconds.",
			want:   45,
		},
		{
			name:   "cooldown",
			output: "A 120 second cooldown is in effect.",
			want:   120,
		},
		{
			name:   "no wait time",
			output: "Rate limit exceeded. Please try again later.",
			want:   0,
		},
		{
			name:   "normal text",
			output: "Here is your response.",
			want:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseWaitTimeFromOutput(tt.output)
			if got != tt.want {
				t.Errorf("parseWaitTimeFromOutput(%q) = %d, want %d", tt.output, got, tt.want)
			}
		})
	}
}

func TestDetectProvider(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "claude mention",
			output: "Claude is analyzing your request...",
			want:   "claude",
		},
		{
			name:   "anthropic mention",
			output: "Powered by Anthropic",
			want:   "claude",
		},
		{
			name:   "sonnet model",
			output: "Using claude-3-sonnet model",
			want:   "claude",
		},
		{
			name:   "opus model",
			output: "Using claude-opus-4 for this task",
			want:   "claude",
		},
		{
			name:   "openai mention",
			output: "OpenAI API response received",
			want:   "openai",
		},
		{
			name:   "codex mention",
			output: "Codex CLI processing...",
			want:   "openai",
		},
		{
			name:   "gpt-4 model",
			output: "Using gpt-4-turbo model",
			want:   "openai",
		},
		{
			name:   "gemini mention",
			output: "Gemini generating response...",
			want:   "gemini",
		},
		{
			name:   "google ai",
			output: "Powered by Google AI",
			want:   "gemini",
		},
		{
			name:   "unknown provider",
			output: "Processing your request...",
			want:   "unknown",
		},
		{
			name:   "empty output",
			output: "",
			want:   "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectProvider(tt.output)
			if got != tt.want {
				t.Errorf("DetectProvider(%q) = %q, want %q", tt.output, got, tt.want)
			}
		})
	}
}

func TestRateLimitEventStruct(t *testing.T) {
	event := RateLimitEvent{
		Provider:      "claude",
		PaneID:        "pane-1",
		DetectedAt:    time.Now(),
		Patterns:      []string{"rate limit", "too many"},
		WaitSeconds:   60,
		AccountBefore: "acc-1",
		AccountAfter:  "acc-2",
		SwitchSuccess: true,
	}

	if event.Provider != "claude" {
		t.Errorf("Expected Provider 'claude', got %s", event.Provider)
	}

	if event.PaneID != "pane-1" {
		t.Errorf("Expected PaneID 'pane-1', got %s", event.PaneID)
	}

	if len(event.Patterns) != 2 {
		t.Errorf("Expected 2 patterns, got %d", len(event.Patterns))
	}

	if event.WaitSeconds != 60 {
		t.Errorf("Expected WaitSeconds 60, got %d", event.WaitSeconds)
	}

	if !event.SwitchSuccess {
		t.Error("Expected SwitchSuccess to be true")
	}
}

func TestRateLimitDetectorNilAdapter(t *testing.T) {
	// Should not panic with nil adapter
	detector := NewRateLimitDetector(nil)
	if detector == nil {
		t.Fatal("NewRateLimitDetector(nil) returned nil")
	}

	// Check should still work
	event := detector.Check("Rate limit exceeded", "pane-1")
	if event == nil {
		t.Error("Check should detect rate limit even with nil adapter")
	}

	// TriggerAccountSwitch should return safely with nil adapter
	ctx := context.Background()
	result := detector.TriggerAccountSwitch(ctx, event)
	if result == nil {
		t.Fatal("TriggerAccountSwitch should return event even with nil adapter")
	}
	if result.SwitchSuccess {
		t.Error("SwitchSuccess should be false with nil adapter")
	}
}

// SwitchResult and SwitchToNextAccount tests

func TestSwitchResultStruct(t *testing.T) {
	result := SwitchResult{
		Success:           true,
		Provider:          "claude",
		PreviousAccount:   "account1",
		NewAccount:        "account2",
		AccountsRemaining: 2,
	}

	if !result.Success {
		t.Error("Expected Success to be true")
	}

	if result.Provider != "claude" {
		t.Errorf("Expected Provider 'claude', got %s", result.Provider)
	}

	if result.PreviousAccount != "account1" {
		t.Errorf("Expected PreviousAccount 'account1', got %s", result.PreviousAccount)
	}

	if result.NewAccount != "account2" {
		t.Errorf("Expected NewAccount 'account2', got %s", result.NewAccount)
	}

	if result.AccountsRemaining != 2 {
		t.Errorf("Expected AccountsRemaining 2, got %d", result.AccountsRemaining)
	}
}

func TestSwitchResultWithError(t *testing.T) {
	result := SwitchResult{
		Success:           false,
		Provider:          "claude",
		PreviousAccount:   "account1",
		Error:             "no accounts available",
		AccountsRemaining: 0,
	}

	if result.Success {
		t.Error("Expected Success to be false")
	}

	if result.Error != "no accounts available" {
		t.Errorf("Expected Error 'no accounts available', got %s", result.Error)
	}
}

func TestSwitchToNextAccountNotInstalled(t *testing.T) {
	adapter := NewCAAMAdapter()

	// If caam is not installed, should return error
	_, installed := adapter.Detect()
	if installed {
		t.Skip("caam is installed, skipping not-installed test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := adapter.SwitchToNextAccount(ctx, "claude")
	if err == nil {
		t.Error("Expected error when caam not installed")
	}

	if err != ErrToolNotInstalled {
		t.Errorf("Expected ErrToolNotInstalled, got %v", err)
	}
}

func TestSwitchToNextAccountReturnsSchemaErrorOnInvalidJSONSuccess(t *testing.T) {
	dir := t.TempDir()
	fakeCAAM := filepath.Join(dir, "caam")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then\n" +
		"  echo \"caam 1.2.3\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"switch\" ]; then\n" +
		"  echo \"not-json\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo \"[]\"\n"
	if err := os.WriteFile(fakeCAAM, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write fake caam: %v", err)
	}

	t.Setenv("PATH", dir)
	adapter := NewCAAMAdapter()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := adapter.SwitchToNextAccount(ctx, "claude")
	if err == nil {
		t.Fatal("expected schema validation error for invalid JSON")
	}
	if !strings.Contains(err.Error(), ErrSchemaValidation.Error()) {
		t.Fatalf("error = %v, want schema validation error", err)
	}
}

func TestSwitchToNextAccountReturnsSchemaErrorOnSuccessFalseWithoutError(t *testing.T) {
	dir := t.TempDir()
	fakeCAAM := filepath.Join(dir, "caam")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then\n" +
		"  echo \"caam 1.2.3\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"switch\" ]; then\n" +
		"  echo '{\"success\":false,\"provider\":\"claude\"}'\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo \"[]\"\n"
	if err := os.WriteFile(fakeCAAM, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write fake caam: %v", err)
	}

	t.Setenv("PATH", dir)
	adapter := NewCAAMAdapter()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := adapter.SwitchToNextAccount(ctx, "claude")
	if err == nil {
		t.Fatal("expected error for success=false without error details")
	}
	if result == nil {
		t.Fatal("expected parsed result payload")
	}
	if result.Success {
		t.Fatal("expected success=false in parsed result")
	}
	if !strings.Contains(err.Error(), ErrSchemaValidation.Error()) {
		t.Fatalf("error = %v, want schema validation error", err)
	}
}

func TestTriggerAccountSwitchUnknownProvider(t *testing.T) {
	adapter := NewCAAMAdapter()
	detector := NewRateLimitDetector(adapter)

	event := &RateLimitEvent{
		Provider:   "unknown",
		PaneID:     "pane-1",
		DetectedAt: time.Now(),
		Patterns:   []string{"rate limit"},
	}

	ctx := context.Background()
	result := detector.TriggerAccountSwitch(ctx, event)

	// Should return early without attempting switch for unknown provider
	if result.SwitchSuccess {
		t.Error("SwitchSuccess should be false for unknown provider")
	}
}

func TestTriggerAccountSwitchEmptyProvider(t *testing.T) {
	adapter := NewCAAMAdapter()
	detector := NewRateLimitDetector(adapter)

	event := &RateLimitEvent{
		Provider:   "",
		PaneID:     "pane-1",
		DetectedAt: time.Now(),
		Patterns:   []string{"rate limit"},
	}

	ctx := context.Background()
	result := detector.TriggerAccountSwitch(ctx, event)

	// Should return early without attempting switch for empty provider
	if result.SwitchSuccess {
		t.Error("SwitchSuccess should be false for empty provider")
	}
}

// Credential and environment tests

func TestCAAMCredentialsStruct(t *testing.T) {
	creds := CAAMCredentials{
		Provider:    "claude",
		AccountID:   "acc-123",
		APIKey:      "sk-ant-xxx",
		TokenPath:   "/home/user/.config/caam/current/claude.json",
		EnvVarName:  "ANTHROPIC_API_KEY",
		RateLimited: false,
	}

	if creds.Provider != "claude" {
		t.Errorf("Expected Provider 'claude', got %s", creds.Provider)
	}

	if creds.AccountID != "acc-123" {
		t.Errorf("Expected AccountID 'acc-123', got %s", creds.AccountID)
	}

	if creds.EnvVarName != "ANTHROPIC_API_KEY" {
		t.Errorf("Expected EnvVarName 'ANTHROPIC_API_KEY', got %s", creds.EnvVarName)
	}
}

func TestGetCredentialEnvVar(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{"claude", "ANTHROPIC_API_KEY"},
		{"openai", "OPENAI_API_KEY"},
		{"gemini", "GOOGLE_API_KEY"},
		{"unknown", "UNKNOWN_API_KEY"}, // Default pattern
		{"custom", "CUSTOM_API_KEY"},   // Default pattern
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := GetCredentialEnvVar(tt.provider)
			if got != tt.want {
				t.Errorf("GetCredentialEnvVar(%q) = %q, want %q", tt.provider, got, tt.want)
			}
		})
	}
}

func TestGetCredentialPath(t *testing.T) {
	tests := []struct {
		provider   string
		wantSuffix string
	}{
		{"claude", ".config/caam/current/claude.json"},
		{"openai", ".config/caam/current/openai.json"},
		{"gemini", ".config/caam/current/gemini.json"},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := GetCredentialPath(tt.provider)
			if got == "" {
				t.Skip("Could not determine home directory")
			}
			if !strings.HasSuffix(got, tt.wantSuffix) {
				t.Errorf("GetCredentialPath(%q) = %q, want suffix %q", tt.provider, got, tt.wantSuffix)
			}
		})
	}
}

func TestProviderEnvVarsMap(t *testing.T) {
	// Ensure all expected providers are mapped
	expectedProviders := []string{"claude", "openai", "gemini"}
	for _, provider := range expectedProviders {
		if _, ok := ProviderEnvVars[provider]; !ok {
			t.Errorf("ProviderEnvVars missing mapping for %q", provider)
		}
	}

	// Verify specific mappings
	if ProviderEnvVars["claude"] != "ANTHROPIC_API_KEY" {
		t.Errorf("ProviderEnvVars[claude] = %q, want ANTHROPIC_API_KEY", ProviderEnvVars["claude"])
	}
	if ProviderEnvVars["openai"] != "OPENAI_API_KEY" {
		t.Errorf("ProviderEnvVars[openai] = %q, want OPENAI_API_KEY", ProviderEnvVars["openai"])
	}
	if ProviderEnvVars["gemini"] != "GOOGLE_API_KEY" {
		t.Errorf("ProviderEnvVars[gemini] = %q, want GOOGLE_API_KEY", ProviderEnvVars["gemini"])
	}
}

func TestGetCurrentCredentialsNotInstalled(t *testing.T) {
	adapter := NewCAAMAdapter()

	_, installed := adapter.Detect()
	if installed {
		t.Skip("caam is installed, skipping not-installed test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := adapter.GetCurrentCredentials(ctx, "claude")
	if err == nil {
		t.Error("Expected error when caam not installed")
	}

	if err != ErrToolNotInstalled {
		t.Errorf("Expected ErrToolNotInstalled, got %v", err)
	}
}

func TestConstructCredentialsHelper(t *testing.T) {
	adapter := NewCAAMAdapter()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// constructCredentials is a fallback that should work even without caam
	// It builds credentials from available status info
	creds, err := adapter.constructCredentials(ctx, "claude")

	// May error if caam not installed (GetAccounts fails)
	_, installed := adapter.Detect()
	if !installed {
		if err == nil {
			// Even if GetAccounts fails, it should return partial credentials
			if creds == nil {
				t.Error("Expected partial credentials even on error")
			}
		}
		return
	}

	// If caam is installed, should return credentials
	if err != nil {
		t.Logf("constructCredentials error (may be expected): %v", err)
	}
	if creds != nil {
		if creds.Provider != "claude" {
			t.Errorf("Expected Provider 'claude', got %s", creds.Provider)
		}
		if creds.EnvVarName != "ANTHROPIC_API_KEY" {
			t.Errorf("Expected EnvVarName 'ANTHROPIC_API_KEY', got %s", creds.EnvVarName)
		}
	}
}

package caut

import (
	"context"
	"errors"
	"testing"
	"time"
)

// MockExecutor is a test executor that returns predefined responses
type MockExecutor struct {
	response  []byte
	err       error
	callCount int
}

func (m *MockExecutor) Run(_ context.Context, _ ...string) ([]byte, error) {
	m.callCount++
	return m.response, m.err
}

func TestNewClient(t *testing.T) {
	c := NewClient()
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
	if c.timeout != 30*time.Second {
		t.Errorf("expected default timeout 30s, got %v", c.timeout)
	}
}

func TestNewClientWithOptions(t *testing.T) {
	c := NewClient(
		WithTimeout(10 * time.Second),
	)

	if c.timeout != 10*time.Second {
		t.Errorf("expected timeout 10s, got %v", c.timeout)
	}

	if _, ok := c.executor.(*DefaultExecutor); !ok {
		t.Fatal("executor is not DefaultExecutor")
	}
}

func TestFetchUsage(t *testing.T) {
	mockResponse := `{
		"schema_version": "caut.v1",
		"command": "usage",
		"timestamp": "2026-01-20T15:30:00Z",
		"data": {
			"payloads": [{
				"provider": "claude",
				"account": "user@example.com",
				"source": "web",
				"usage": {
					"primary_rate_window": {
						"used_percent": 67.5,
						"window_minutes": 480,
						"resets_at": "2026-01-20T23:00:00Z",
						"reset_description": "8-hour rolling window"
					},
					"identity": {
						"account_email": "user@example.com",
						"plan_name": "Claude Max"
					}
				}
			}]
		},
		"errors": []
	}`

	c := NewClient(WithExecutor(&MockExecutor{response: []byte(mockResponse)}))

	result, err := c.FetchUsage(context.Background(), []string{"claude"})
	if err != nil {
		t.Fatalf("FetchUsage failed: %v", err)
	}

	if result.SchemaVersion != "caut.v1" {
		t.Errorf("expected schema_version 'caut.v1', got %s", result.SchemaVersion)
	}

	if len(result.Payloads) != 1 {
		t.Fatalf("expected 1 payload, got %d", len(result.Payloads))
	}

	payload := result.Payloads[0]
	if payload.Provider != "claude" {
		t.Errorf("expected provider 'claude', got %s", payload.Provider)
	}

	usedPct := payload.UsedPercent()
	if usedPct == nil || *usedPct != 67.5 {
		t.Errorf("expected used_percent 67.5, got %v", usedPct)
	}

	if !payload.IsRateLimited(60) {
		t.Error("expected IsRateLimited(60) to return true for 67.5% usage")
	}
	if payload.IsRateLimited(70) {
		t.Error("expected IsRateLimited(70) to return false for 67.5% usage")
	}

	if payload.GetPlanName() != "Claude Max" {
		t.Errorf("expected plan name 'Claude Max', got %s", payload.GetPlanName())
	}
}

func TestFetchUsageWithErrors(t *testing.T) {
	mockResponse := `{
		"schema_version": "caut.v1",
		"command": "usage",
		"timestamp": "2026-01-20T15:30:00Z",
		"data": {
			"payloads": []
		},
		"errors": ["provider claude not configured", "network timeout"]
	}`

	c := NewClient(WithExecutor(&MockExecutor{response: []byte(mockResponse)}))

	result, err := c.FetchUsage(context.Background(), []string{"claude"})
	if err != nil {
		t.Fatalf("FetchUsage failed: %v", err)
	}

	if len(result.Errors) != 2 {
		t.Errorf("expected 2 errors, got %d", len(result.Errors))
	}
}

func TestFetchUsageExecutorError(t *testing.T) {
	c := NewClient(WithExecutor(&MockExecutor{
		err: errors.New("command failed"),
	}))

	_, err := c.FetchUsage(context.Background(), []string{"claude"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestFetchUsageInvalidJSON(t *testing.T) {
	c := NewClient(WithExecutor(&MockExecutor{
		response: []byte("not valid json"),
	}))

	_, err := c.FetchUsage(context.Background(), []string{"claude"})
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestGetProviderUsage(t *testing.T) {
	mockResponse := `{
		"schema_version": "caut.v1",
		"command": "usage",
		"timestamp": "2026-01-20T15:30:00Z",
		"data": {
			"payloads": [{
				"provider": "codex",
				"source": "api",
				"usage": {
					"primary_rate_window": {
						"used_percent": 45.0
					}
				}
			}]
		},
		"errors": []
	}`

	c := NewClient(WithExecutor(&MockExecutor{response: []byte(mockResponse)}))

	payload, err := c.GetProviderUsage(context.Background(), "codex")
	if err != nil {
		t.Fatalf("GetProviderUsage failed: %v", err)
	}

	if payload.Provider != "codex" {
		t.Errorf("expected provider 'codex', got %s", payload.Provider)
	}
}

func TestGetProviderUsageNoData(t *testing.T) {
	mockResponse := `{
		"schema_version": "caut.v1",
		"command": "usage",
		"timestamp": "2026-01-20T15:30:00Z",
		"data": {
			"payloads": []
		},
		"errors": []
	}`

	c := NewClient(WithExecutor(&MockExecutor{response: []byte(mockResponse)}))

	_, err := c.GetProviderUsage(context.Background(), "claude")
	if !errors.Is(err, ErrNoData) {
		t.Errorf("expected ErrNoData, got %v", err)
	}
}

func TestAgentTypeToProvider(t *testing.T) {
	tests := []struct {
		agentType string
		expected  string
	}{
		{"cc", "claude"},
		{"claude-code", "claude"},
		{"cod", "codex"},
		{"openai-codex", "codex"},
		{"gmi", "gemini"},
		{"google-gemini", "gemini"},
		{"ws", "windsurf"},
		{"unknown", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			got := AgentTypeToProvider(tt.agentType)
			if got != tt.expected {
				t.Errorf("AgentTypeToProvider(%q) = %q, want %q", tt.agentType, got, tt.expected)
			}
		})
	}
}

func TestProviderPayloadHelpers(t *testing.T) {
	usedPct := 75.5
	windowMins := 480
	resetDesc := "8-hour rolling window"
	email := "user@example.com"
	planName := "Claude Max"

	payload := &ProviderPayload{
		Provider: "claude",
		Source:   "web",
		Usage: UsageSnapshot{
			PrimaryRateWindow: &RateWindow{
				UsedPercent:      &usedPct,
				WindowMinutes:    &windowMins,
				ResetDescription: &resetDesc,
			},
			Identity: &Identity{
				AccountEmail: &email,
				PlanName:     &planName,
			},
		},
	}

	if !payload.HasUsageData() {
		t.Error("HasUsageData should return true")
	}

	if got := payload.GetWindowMinutes(); got == nil || *got != 480 {
		t.Errorf("GetWindowMinutes = %v, want 480", got)
	}

	if got := payload.GetResetDescription(); got != resetDesc {
		t.Errorf("GetResetDescription = %q, want %q", got, resetDesc)
	}

	if got := payload.GetAccountEmail(); got != email {
		t.Errorf("GetAccountEmail = %q, want %q", got, email)
	}

	if got := payload.GetPlanName(); got != planName {
		t.Errorf("GetPlanName = %q, want %q", got, planName)
	}

	if !payload.IsOperational() {
		t.Error("IsOperational should return true when Status is nil")
	}
}

func TestProviderPayloadNoData(t *testing.T) {
	payload := &ProviderPayload{
		Provider: "claude",
		Source:   "web",
		Usage:    UsageSnapshot{},
	}

	if payload.HasUsageData() {
		t.Error("HasUsageData should return false")
	}

	if payload.UsedPercent() != nil {
		t.Error("UsedPercent should return nil")
	}

	if payload.GetResetTime() != nil {
		t.Error("GetResetTime should return nil")
	}

	if payload.IsRateLimited(50) {
		t.Error("IsRateLimited should return false when no data")
	}
}

func TestCachedClient(t *testing.T) {
	mockResponse := `{
		"schema_version": "caut.v1",
		"command": "usage",
		"timestamp": "2026-01-20T15:30:00Z",
		"data": {
			"payloads": [{
				"provider": "claude",
				"source": "web",
				"usage": {
					"primary_rate_window": {
						"used_percent": 50.0
					}
				}
			}]
		},
		"errors": []
	}`

	mockExec := &MockExecutor{response: []byte(mockResponse)}
	client := NewClient(WithExecutor(mockExec))
	cachedClient := NewCachedClient(client, 5*time.Minute)

	// First call - should hit executor
	_, err := cachedClient.GetProviderUsage(context.Background(), "claude")
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if mockExec.callCount != 1 {
		t.Errorf("expected 1 call, got %d", mockExec.callCount)
	}

	// Second call - should use cache
	_, err = cachedClient.GetProviderUsage(context.Background(), "claude")
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if mockExec.callCount != 1 {
		t.Errorf("expected still 1 call (cached), got %d", mockExec.callCount)
	}

}

func TestSupportedProviders(t *testing.T) {
	providers := SupportedProviders()
	if len(providers) != 6 {
		t.Errorf("expected 6 providers, got %d", len(providers))
	}

	expected := map[string]bool{
		"claude": true, "codex": true, "gemini": true,
		"cursor": true, "windsurf": true, "aider": true,
	}
	for _, p := range providers {
		if !expected[p] {
			t.Errorf("unexpected provider: %s", p)
		}
	}
}

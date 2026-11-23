package caut

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// MockExecutor is a test executor that returns predefined responses
type MockExecutor struct {
	response  []byte
	err       error
	callCount int
	args      []string
}

func (m *MockExecutor) Run(_ context.Context, args ...string) ([]byte, error) {
	m.callCount++
	m.args = args
	return m.response, m.err
}

// cautUsageDocument is the full usage document from caut's own schema
// contract test (coding_agent_usage_tracker tests/schema_contract_test.rs,
// test_provider_payload_full), which caut validates against its published
// schemas/caut-v1.schema.json.
const cautUsageDocument = `{
	"schemaVersion": "caut.v1",
	"generatedAt": "2026-01-18T10:30:00Z",
	"command": "usage",
	"data": [{
		"provider": "claude",
		"account": "test@example.com",
		"version": "1.0.0",
		"source": "oauth",
		"status": {
			"indicator": "none",
			"description": "All systems operational",
			"url": "https://status.anthropic.com"
		},
		"usage": {
			"primary": {
				"usedPercent": 30.0,
				"windowMinutes": 180,
				"resetsAt": "2026-01-18T12:30:00Z",
				"resetDescription": "in 2 hours"
			},
			"secondary": {
				"usedPercent": 15.0,
				"windowMinutes": 10080,
				"resetDescription": "in 5 days"
			},
			"updatedAt": "2026-01-18T10:30:00Z",
			"identity": {
				"accountEmail": "test@example.com",
				"loginMethod": "oauth"
			}
		}
	}],
	"errors": [],
	"meta": { "format": "json", "flags": [], "runtime": "cli" }
}`

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

// ntm parsed a snake_case envelope with data.payloads, which caut has never
// emitted: every real caut response failed to unmarshal and usage-aware health
// saw no provider data at all.
func TestFetchUsageParsesCautContractDocument(t *testing.T) {
	exec := &MockExecutor{response: []byte(cautUsageDocument)}
	c := NewClient(WithExecutor(exec))

	result, err := c.FetchUsage(context.Background(), "claude")
	if err != nil {
		t.Fatalf("FetchUsage failed: %v", err)
	}
	if got := strings.Join(exec.args, " "); got != "usage --format json --provider claude" {
		t.Errorf("caut args = %q, want usage --format json --provider claude", got)
	}
	if result.SchemaVersion != "caut.v1" {
		t.Errorf("expected schemaVersion 'caut.v1', got %s", result.SchemaVersion)
	}
	if len(result.Payloads) != 1 {
		t.Fatalf("expected 1 payload, got %d", len(result.Payloads))
	}

	payload := result.Payloads[0]
	if payload.Provider != "claude" || payload.Source != "oauth" {
		t.Errorf("provider/source = %s/%s, want claude/oauth", payload.Provider, payload.Source)
	}
	if usedPct := payload.UsedPercent(); usedPct == nil || *usedPct != 30.0 {
		t.Errorf("expected usedPercent 30, got %v", usedPct)
	}
	if got := payload.GetWindowMinutes(); got == nil || *got != 180 {
		t.Errorf("GetWindowMinutes = %v, want 180", got)
	}
	if reset := payload.GetResetTime(); reset == nil || !reset.Equal(time.Date(2026, 1, 18, 12, 30, 0, 0, time.UTC)) {
		t.Errorf("GetResetTime = %v, want 2026-01-18T12:30:00Z", reset)
	}
	if got := payload.GetResetDescription(); got != "in 2 hours" {
		t.Errorf("GetResetDescription = %q, want %q", got, "in 2 hours")
	}
	if payload.Usage.SecondaryRateWindow == nil || *payload.Usage.SecondaryRateWindow.UsedPercent != 15.0 {
		t.Errorf("secondary window = %+v, want 15%% used", payload.Usage.SecondaryRateWindow)
	}
	if got := payload.GetAccountEmail(); got != "test@example.com" {
		t.Errorf("GetAccountEmail = %q, want test@example.com", got)
	}
	if !payload.IsOperational() || payload.Status.Description == nil || *payload.Status.Description != "All systems operational" {
		t.Errorf("status = %+v, want operational with its description", payload.Status)
	}
	if !payload.IsRateLimited(25) || payload.IsRateLimited(35) {
		t.Error("IsRateLimited should compare the primary window's 30% against the threshold")
	}
}

func TestStatusInfoOperationalByIndicator(t *testing.T) {
	for indicator, want := range map[string]bool{
		"none": true, "minor": true, "maintenance": true, "unknown": true, "": true,
		"major": false, "critical": false, "CRITICAL": false,
	} {
		if got := (&StatusInfo{Indicator: indicator}).Operational(); got != want {
			t.Errorf("Operational(%q) = %v, want %v", indicator, got, want)
		}
	}
}

func TestFetchUsageWithErrors(t *testing.T) {
	mockResponse := `{
		"schemaVersion": "caut.v1",
		"generatedAt": "2026-01-20T15:30:00Z",
		"command": "usage",
		"data": [],
		"errors": ["provider claude not configured", "network timeout"]
	}`

	c := NewClient(WithExecutor(&MockExecutor{response: []byte(mockResponse)}))

	result, err := c.FetchUsage(context.Background(), "claude")
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

	_, err := c.FetchUsage(context.Background(), "claude")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestFetchUsageInvalidJSON(t *testing.T) {
	c := NewClient(WithExecutor(&MockExecutor{
		response: []byte("not valid json"),
	}))

	_, err := c.FetchUsage(context.Background(), "claude")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestGetProviderUsage(t *testing.T) {
	mockResponse := `{
		"schemaVersion": "caut.v1",
		"generatedAt": "2026-01-20T15:30:00Z",
		"command": "usage",
		"data": [{
			"provider": "codex",
			"source": "api",
			"usage": {
				"primary": {"usedPercent": 45.0},
				"updatedAt": "2026-01-20T15:30:00Z"
			}
		}],
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
	if used := payload.UsedPercent(); used == nil || *used != 45.0 {
		t.Errorf("UsedPercent = %v, want 45", used)
	}
}

func TestGetProviderUsageNoData(t *testing.T) {
	mockResponse := `{
		"schemaVersion": "caut.v1",
		"generatedAt": "2026-01-20T15:30:00Z",
		"command": "usage",
		"data": [],
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
		{"ws", ""}, // caut has no windsurf provider
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
	mockExec := &MockExecutor{response: []byte(cautUsageDocument)}
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
	if len(providers) != 4 {
		t.Errorf("expected 4 providers, got %d", len(providers))
	}

	// Every one must be a caut provider name: caut rejects others outright.
	expected := map[string]bool{
		"claude": true, "codex": true, "gemini": true, "cursor": true,
	}
	for _, p := range providers {
		if !expected[p] {
			t.Errorf("unexpected provider: %s", p)
		}
	}
}

func TestLimitedBuffer_IOCopyEnforcesLimit(t *testing.T) {
	t.Parallel()
	buf := &limitedBuffer{limit: 16}
	if _, ok := any(buf).(io.ReaderFrom); ok {
		t.Fatal("caut limitedBuffer must not implement io.ReaderFrom")
	}
	_, err := io.Copy(buf, bytes.NewReader(bytes.Repeat([]byte("x"), 128)))
	if err == nil || !strings.Contains(err.Error(), "exceeded limit") {
		t.Fatalf("io.Copy error = %v, want limit exceeded error", err)
	}
}

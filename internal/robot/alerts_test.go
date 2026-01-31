package robot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAlerterBasic(t *testing.T) {
	config := &AlerterConfig{
		Enabled:          true,
		DebounceInterval: 0, // No debouncing for tests
		AlertOn:          []AlertType{AlertUnhealthy, AlertDegraded, AlertRecovered},
		DesktopEnabled:   false,
		LogToStderr:      false,
	}

	alerter := NewAlerter(config)

	alert := &Alert{
		Timestamp: time.Now(),
		Type:      AlertUnhealthy,
		Session:   "test-session",
		PaneID:    "%1",
		AgentType: "claude",
		Message:   "Test alert",
	}

	// Should not error even with no channels
	err := alerter.Send(context.Background(), alert)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAlerterDebouncing(t *testing.T) {
	config := &AlerterConfig{
		Enabled:          true,
		DebounceInterval: 100 * time.Millisecond,
		AlertOn:          []AlertType{AlertUnhealthy},
		DesktopEnabled:   false,
		LogToStderr:      false,
	}

	alerter := NewAlerter(config)

	// Track how many alerts pass through
	var alertCount int32
	mockChannel := &mockAlertChannel{
		name:      "mock",
		available: true,
		sendFunc: func(ctx context.Context, alert *Alert) error {
			atomic.AddInt32(&alertCount, 1)
			return nil
		},
	}
	alerter.channels = append(alerter.channels, mockChannel)

	alert := &Alert{
		Timestamp: time.Now(),
		Type:      AlertUnhealthy,
		PaneID:    "%1",
	}

	// First alert should go through
	alerter.Send(context.Background(), alert)
	if atomic.LoadInt32(&alertCount) != 1 {
		t.Errorf("expected 1 alert, got %d", alertCount)
	}

	// Second alert immediately should be debounced
	alerter.Send(context.Background(), alert)
	if atomic.LoadInt32(&alertCount) != 1 {
		t.Errorf("expected 1 alert (debounced), got %d", alertCount)
	}

	// Wait for debounce window to pass
	time.Sleep(150 * time.Millisecond)

	// Third alert should go through
	alerter.Send(context.Background(), alert)
	if atomic.LoadInt32(&alertCount) != 2 {
		t.Errorf("expected 2 alerts after debounce, got %d", alertCount)
	}
}

func TestAlerterEventFiltering(t *testing.T) {
	config := &AlerterConfig{
		Enabled:          true,
		DebounceInterval: 0,
		AlertOn:          []AlertType{AlertUnhealthy}, // Only unhealthy
		DesktopEnabled:   false,
		LogToStderr:      false,
	}

	alerter := NewAlerter(config)

	var alertCount int32
	mockChannel := &mockAlertChannel{
		name:      "mock",
		available: true,
		sendFunc: func(ctx context.Context, alert *Alert) error {
			atomic.AddInt32(&alertCount, 1)
			return nil
		},
	}
	alerter.channels = append(alerter.channels, mockChannel)

	// Unhealthy should go through
	alerter.Send(context.Background(), &Alert{Type: AlertUnhealthy, PaneID: "%1"})
	if atomic.LoadInt32(&alertCount) != 1 {
		t.Errorf("expected 1 alert for unhealthy, got %d", alertCount)
	}

	// Degraded should be filtered
	alerter.Send(context.Background(), &Alert{Type: AlertDegraded, PaneID: "%2"})
	if atomic.LoadInt32(&alertCount) != 1 {
		t.Errorf("expected 1 alert (degraded filtered), got %d", alertCount)
	}
}

func TestAlerterDisabled(t *testing.T) {
	config := &AlerterConfig{
		Enabled: false,
	}

	alerter := NewAlerter(config)

	var alertCount int32
	mockChannel := &mockAlertChannel{
		name:      "mock",
		available: true,
		sendFunc: func(ctx context.Context, alert *Alert) error {
			atomic.AddInt32(&alertCount, 1)
			return nil
		},
	}
	alerter.channels = append(alerter.channels, mockChannel)

	alerter.Send(context.Background(), &Alert{Type: AlertUnhealthy, PaneID: "%1"})
	if atomic.LoadInt32(&alertCount) != 0 {
		t.Errorf("expected 0 alerts when disabled, got %d", alertCount)
	}
}

func TestAlerterSendStateChange(t *testing.T) {
	config := &AlerterConfig{
		Enabled:          true,
		DebounceInterval: 0,
		AlertOn:          []AlertType{AlertUnhealthy, AlertDegraded, AlertRecovered},
		DesktopEnabled:   false,
		LogToStderr:      false,
	}

	alerter := NewAlerter(config)

	var lastAlert *Alert
	mockChannel := &mockAlertChannel{
		name:      "mock",
		available: true,
		sendFunc: func(ctx context.Context, alert *Alert) error {
			lastAlert = alert
			return nil
		},
	}
	alerter.channels = append(alerter.channels, mockChannel)

	// Test healthy -> unhealthy
	err := alerter.SendStateChange(context.Background(), "test-session", "%1", "claude",
		HealthHealthy, HealthUnhealthy, "crashed")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if lastAlert == nil {
		t.Fatal("expected alert to be sent")
	}
	if lastAlert.Type != AlertUnhealthy {
		t.Errorf("expected AlertUnhealthy, got %v", lastAlert.Type)
	}
	if lastAlert.PrevState != HealthHealthy {
		t.Errorf("expected prev state HealthHealthy, got %v", lastAlert.PrevState)
	}
}

func TestAlerterSendRestart(t *testing.T) {
	config := &AlerterConfig{
		Enabled:          true,
		DebounceInterval: 0,
		AlertOn:          []AlertType{AlertRestart, AlertRestartFailed},
		DesktopEnabled:   false,
		LogToStderr:      false,
	}

	alerter := NewAlerter(config)

	var lastAlert *Alert
	mockChannel := &mockAlertChannel{
		name:      "mock",
		available: true,
		sendFunc: func(ctx context.Context, alert *Alert) error {
			lastAlert = alert
			return nil
		},
	}
	alerter.channels = append(alerter.channels, mockChannel)

	// Test successful restart with context loss
	err := alerter.SendRestart(context.Background(), "test-session", "%1", "claude", true, true)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if lastAlert == nil {
		t.Fatal("expected alert to be sent")
	}
	if lastAlert.Type != AlertRestart {
		t.Errorf("expected AlertRestart, got %v", lastAlert.Type)
	}
	if !lastAlert.ContextLoss {
		t.Error("expected ContextLoss to be true")
	}

	// Test failed restart
	err = alerter.SendRestart(context.Background(), "test-session", "%2", "codex", false, false)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if lastAlert.Type != AlertRestartFailed {
		t.Errorf("expected AlertRestartFailed, got %v", lastAlert.Type)
	}
}

func TestWebhookChannel(t *testing.T) {
	var receivedAlerts []*Alert

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var alert Alert
		if err := json.NewDecoder(r.Body).Decode(&alert); err != nil {
			t.Errorf("failed to decode alert: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		receivedAlerts = append(receivedAlerts, &alert)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	channel := NewWebhookChannel(WebhookConfig{
		URL:        server.URL,
		MaxRetries: 1,
		Timeout:    5 * time.Second,
	})

	if !channel.Available() {
		t.Error("expected channel to be available")
	}

	alert := &Alert{
		Timestamp: time.Now(),
		Type:      AlertUnhealthy,
		Session:   "test-session",
		PaneID:    "%1",
		Message:   "Test webhook alert",
	}

	err := channel.Send(context.Background(), alert)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if len(receivedAlerts) != 1 {
		t.Errorf("expected 1 alert, got %d", len(receivedAlerts))
	}
	if receivedAlerts[0].Message != "Test webhook alert" {
		t.Errorf("unexpected message: %s", receivedAlerts[0].Message)
	}
}

func TestWebhookChannelEventFiltering(t *testing.T) {
	var alertCount int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		alertCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	channel := NewWebhookChannel(WebhookConfig{
		URL:    server.URL,
		Events: []AlertType{AlertUnhealthy}, // Only unhealthy
	})

	// Unhealthy should be sent
	channel.Send(context.Background(), &Alert{Type: AlertUnhealthy})
	if alertCount != 1 {
		t.Errorf("expected 1 alert for unhealthy, got %d", alertCount)
	}

	// Degraded should be filtered
	channel.Send(context.Background(), &Alert{Type: AlertDegraded})
	if alertCount != 1 {
		t.Errorf("expected 1 alert (degraded filtered), got %d", alertCount)
	}
}

func TestWebhookChannelRetry(t *testing.T) {
	var attempts int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	channel := NewWebhookChannel(WebhookConfig{
		URL:        server.URL,
		MaxRetries: 3,
		Timeout:    1 * time.Second,
	})

	err := channel.Send(context.Background(), &Alert{Type: AlertUnhealthy})
	if err != nil {
		t.Errorf("unexpected error after retries: %v", err)
	}

	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestLogChannel(t *testing.T) {
	channel := &LogChannel{}

	if !channel.Available() {
		t.Error("expected log channel to be available")
	}

	// Just verify it doesn't panic/error
	err := channel.Send(context.Background(), &Alert{
		Timestamp: time.Now(),
		Type:      AlertUnhealthy,
		Message:   "Test log alert",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDesktopChannelAvailable(t *testing.T) {
	channel := NewDesktopChannel("normal")

	// Just check it doesn't panic - availability depends on OS
	_ = channel.Available()
	_ = channel.Name()
}

func TestClearDebounce(t *testing.T) {
	config := &AlerterConfig{
		Enabled:          true,
		DebounceInterval: time.Hour, // Long debounce
		AlertOn:          []AlertType{AlertUnhealthy},
	}

	alerter := NewAlerter(config)

	var alertCount int32
	mockChannel := &mockAlertChannel{
		name:      "mock",
		available: true,
		sendFunc: func(ctx context.Context, alert *Alert) error {
			atomic.AddInt32(&alertCount, 1)
			return nil
		},
	}
	alerter.channels = append(alerter.channels, mockChannel)

	// First alert
	alerter.Send(context.Background(), &Alert{Type: AlertUnhealthy, PaneID: "%1"})
	if atomic.LoadInt32(&alertCount) != 1 {
		t.Errorf("expected 1 alert, got %d", alertCount)
	}

	// Second should be debounced
	alerter.Send(context.Background(), &Alert{Type: AlertUnhealthy, PaneID: "%1"})
	if atomic.LoadInt32(&alertCount) != 1 {
		t.Errorf("expected 1 alert (debounced), got %d", alertCount)
	}

	// Clear debounce
	alerter.ClearDebounce("%1")

	// Third should go through
	alerter.Send(context.Background(), &Alert{Type: AlertUnhealthy, PaneID: "%1"})
	if atomic.LoadInt32(&alertCount) != 2 {
		t.Errorf("expected 2 alerts after clear, got %d", alertCount)
	}
}

func TestGlobalAlerter(t *testing.T) {
	// Clear global state first
	SetGlobalAlerter(nil)

	alerter1 := GetAlerter()
	if alerter1 == nil {
		t.Fatal("expected alerter to be created")
	}

	alerter2 := GetAlerter()
	if alerter1 != alerter2 {
		t.Error("expected same alerter to be returned")
	}

	// Set custom alerter
	custom := NewAlerter(&AlerterConfig{Enabled: false})
	SetGlobalAlerter(custom)

	alerter3 := GetAlerter()
	if alerter3 != custom {
		t.Error("expected custom alerter to be returned")
	}

	// Cleanup
	SetGlobalAlerter(nil)
}

// mockAlertChannel is a test helper
type mockAlertChannel struct {
	name      string
	available bool
	sendFunc  func(ctx context.Context, alert *Alert) error
}

func (m *mockAlertChannel) Name() string    { return m.name }
func (m *mockAlertChannel) Available() bool { return m.available }
func (m *mockAlertChannel) Send(ctx context.Context, alert *Alert) error {
	if m.sendFunc != nil {
		return m.sendFunc(ctx, alert)
	}
	return nil
}

// =============================================================================
// Pure Function Tests
// =============================================================================

func TestEscapeAppleScript(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty string", "", ""},
		{"simple text", "hello", "hello"},
		{"with backslash", `hello\world`, `hello\\world`},
		{"with double quote", `say "hello"`, `say \"hello\"`},
		{"with newline", "line1\nline2", `line1\nline2`},
		{"mixed escapes", "say \"hi\"\nfoo\\bar", `say \"hi\"\nfoo\\bar`},
		{"unicode", "héllo wörld", "héllo wörld"},
		{"only backslash", `\`, `\\`},
		{"only quote", `"`, `\"`},
		{"only newline", "\n", `\n`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := escapeAppleScript(tt.input)
			if got != tt.want {
				t.Errorf("escapeAppleScript(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestGetSuggestion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		alertType AlertType
		wantNonEmpty bool
	}{
		{AlertUnhealthy, true},
		{AlertDegraded, true},
		{AlertRateLimited, true},
		{AlertRestart, true},
		{AlertRestartFailed, true},
		{AlertMaxRestarts, true},
		{AlertRecovered, true},
		{AlertType("unknown"), false},
		{AlertType(""), false},
	}

	for _, tt := range tests {
		name := string(tt.alertType)
		if name == "" {
			name = "empty_alert_type"
		}
		t.Run(name, func(t *testing.T) {
			got := getSuggestion(tt.alertType)
			if tt.wantNonEmpty && got == "" {
				t.Errorf("getSuggestion(%q) returned empty string, expected non-empty", tt.alertType)
			}
			if !tt.wantNonEmpty && got != "" {
				t.Errorf("getSuggestion(%q) = %q, expected empty string", tt.alertType, got)
			}
		})
	}
}

func TestGetSuggestionContent(t *testing.T) {
	t.Parallel()
	// Verify specific suggestions contain expected keywords
	tests := []struct {
		alertType    AlertType
		wantContains string
	}{
		{AlertUnhealthy, "restart"},
		{AlertDegraded, "slow"},
		{AlertRateLimited, "rate"},
		{AlertRestart, "restart"},
		{AlertRestartFailed, "failed"},
		{AlertMaxRestarts, "restarts"},
		{AlertRecovered, "healthy"},
	}

	for _, tt := range tests {
		t.Run(string(tt.alertType), func(t *testing.T) {
			got := getSuggestion(tt.alertType)
			gotLower := strings.ToLower(got)
			if !strings.Contains(gotLower, tt.wantContains) {
				t.Errorf("getSuggestion(%q) = %q, expected to contain %q", tt.alertType, got, tt.wantContains)
			}
		})
	}
}

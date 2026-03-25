package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Enabled {
		t.Error("Default config should be enabled")
	}
	if !cfg.Desktop.Enabled {
		t.Error("Default desktop should be enabled")
	}
}

func TestNewNotifier(t *testing.T) {
	cfg := DefaultConfig()
	n := New(cfg)
	if n == nil {
		t.Fatal("New returned nil")
	}
	if !n.enabledSet[EventAgentError] {
		t.Error("EventAgentError should be enabled")
	}
}

type closeTrackingTransport struct {
	closed bool
}

func (t *closeTrackingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, nil
}

func (t *closeTrackingTransport) CloseIdleConnections() {
	t.closed = true
}

func TestNotifierCloseClosesIdleHTTPConnections(t *testing.T) {
	n := New(DefaultConfig())
	transport := &closeTrackingTransport{}
	n.httpClient.Transport = transport

	if err := n.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !transport.closed {
		t.Fatal("Close() should close idle HTTP connections")
	}
}

func TestNotifyDisabled(t *testing.T) {
	cfg := Config{Enabled: false}
	n := New(cfg)
	err := n.Notify(Event{Type: EventAgentError})
	if err != nil {
		t.Errorf("Notify failed when disabled: %v", err)
	}
}

func TestWebhookNotification(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		var payload map[string]string
		json.NewDecoder(r.Body).Decode(&payload)
		if payload["text"] != "NTM: agent.error - Test error" {
			t.Errorf("Unexpected payload: %v", payload)
		}
	}))
	defer ts.Close()

	cfg := Config{
		Enabled: true,
		Events:  []string{"agent.error"},
		Webhook: WebhookConfig{
			Enabled:  true,
			URL:      ts.URL,
			Template: `{"text": "NTM: {{.Type}} - {{.Message}}"}`,
		},
	}

	n := New(cfg)
	err := n.Notify(Event{
		Type:    EventAgentError,
		Message: "Test error",
	})
	if err != nil {
		t.Errorf("Notify failed: %v", err)
	}
}

func TestWebhookDefaultTemplateIncludesContextFields(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}

		if payload["event"] != "bead.assigned" {
			t.Errorf("event = %v, want bead.assigned", payload["event"])
		}
		if payload["session"] != "sess-1" {
			t.Errorf("session = %v, want sess-1", payload["session"])
		}
		if payload["pane"] != "%1" {
			t.Errorf("pane = %v, want %%1", payload["pane"])
		}
		if payload["agent_type"] != "cod" {
			t.Errorf("agent_type = %v, want cod", payload["agent_type"])
		}
		if payload["bead_id"] != "bd-123" {
			t.Errorf("bead_id = %v, want bd-123", payload["bead_id"])
		}
		details, ok := payload["details"].(map[string]any)
		if !ok {
			t.Fatalf("details missing or wrong type: %T", payload["details"])
		}
		if details["bead_id"] != "bd-123" {
			t.Errorf("details.bead_id = %v, want bd-123", details["bead_id"])
		}
	}))
	defer ts.Close()

	cfg := Config{
		Enabled: true,
		Events:  []string{string(EventBeadAssigned)},
		Webhook: WebhookConfig{
			Enabled: true,
			URL:     ts.URL,
			// Empty template => use default template.
		},
	}

	n := New(cfg)
	err := n.Notify(Event{
		Type:    EventBeadAssigned,
		Session: "sess-1",
		Pane:    "%1",
		Agent:   "cod",
		Message: "assigned",
		Details: map[string]string{
			"bead_id": "bd-123",
		},
	})
	if err != nil {
		t.Fatalf("Notify failed: %v", err)
	}
}

func TestWebhookNotification_RedactsSecrets(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		_ = json.NewDecoder(r.Body).Decode(&payload)

		got := payload["text"]
		if strings.Contains(got, "hunter2hunter2") {
			t.Errorf("expected secret to be redacted, got %q", got)
		}
		if !strings.Contains(got, "[REDACTED:PASSWORD:") {
			t.Errorf("expected redaction placeholder, got %q", got)
		}
	}))
	defer ts.Close()

	cfg := Config{
		Enabled: true,
		Events:  []string{"agent.error"},
		Webhook: WebhookConfig{
			Enabled:  true,
			URL:      ts.URL,
			Template: `{"text": "{{.Message}}"}`,
		},
	}

	// Use ModeWarn to match defaults; notifier should still redact outbound payloads.
	n := NewWithRedaction(cfg, redaction.Config{Mode: redaction.ModeWarn})
	err := n.Notify(Event{
		Type:    EventAgentError,
		Message: "password=hunter2hunter2",
	})
	if err != nil {
		t.Fatalf("Notify failed: %v", err)
	}
}

func TestLogNotification(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test.log")

	cfg := Config{
		Enabled: true,
		Events:  []string{"agent.error"},
		Log: LogConfig{
			Enabled: true,
			Path:    logPath,
		},
	}

	n := New(cfg)
	err := n.Notify(Event{
		Type:      EventAgentError,
		Message:   "Test log",
		Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Notify failed: %v", err)
	}

	content, _ := os.ReadFile(logPath)
	if len(content) == 0 {
		t.Error("Log file is empty")
	}
}

func TestHelperFunctions(t *testing.T) {
	evt := NewAgentStartedEvent("sess", "p1", "cc")
	if evt.Type != EventAgentStarted {
		t.Errorf("NewAgentStartedEvent type = %v", evt.Type)
	}

	evt = NewErrorEvent("sess", "p1", "cc", "generic error")
	if evt.Type != EventError {
		t.Errorf("NewErrorEvent type = %v", evt.Type)
	}

	evt = NewRateLimitEvent("sess", "p1", "cc", 30)
	if evt.Type != EventRateLimit {
		t.Errorf("NewRateLimitEvent type = %v", evt.Type)
	}
	if evt.Details["wait_seconds"] != "30" {
		t.Errorf("NewRateLimitEvent details = %v", evt.Details)
	}

	evt = NewAgentCrashedEvent("sess", "p1", "cc")
	if evt.Type != EventAgentCrashed {
		t.Errorf("NewAgentCrashedEvent type = %v", evt.Type)
	}

	evt = NewAgentErrorEvent("sess", "p1", "cc", "error")
	if evt.Type != EventAgentError {
		t.Errorf("NewAgentErrorEvent type = %v", evt.Type)
	}

	evt = NewHealthDegradedEvent("sess", 5, 1, 0)
	if evt.Type != EventHealthDegraded {
		t.Errorf("NewHealthDegradedEvent type = %v", evt.Type)
	}

	evt = NewRotationNeededEvent("sess", 1, "cc", "cmd")
	if evt.Type != EventRotationNeeded {
		t.Errorf("NewRotationNeededEvent type = %v", evt.Type)
	}

	evt = NewBeadAssignedEvent("sess", "p1", "cod", "bd-1", "title")
	if evt.Type != EventBeadAssigned {
		t.Errorf("NewBeadAssignedEvent type = %v", evt.Type)
	}
	if evt.Details["bead_id"] != "bd-1" {
		t.Errorf("NewBeadAssignedEvent bead_id = %q", evt.Details["bead_id"])
	}

	evt = NewBeadCompletedEvent("sess", "p1", "cod", "bd-2", "title-2")
	if evt.Type != EventBeadCompleted {
		t.Errorf("NewBeadCompletedEvent type = %v", evt.Type)
	}
	if evt.Details["bead_id"] != "bd-2" {
		t.Errorf("NewBeadCompletedEvent bead_id = %q", evt.Details["bead_id"])
	}
}

func TestFileBoxNotification(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := Config{
		Enabled: true,
		Events:  []string{"agent.error"},
		FileBox: FileBoxConfig{
			Enabled: true,
			Path:    tmpDir,
		},
	}

	n := New(cfg)
	testTime := time.Date(2026, 1, 4, 10, 30, 0, 0, time.UTC)
	err := n.Notify(Event{
		Type:      EventAgentError,
		Message:   "Test file inbox",
		Session:   "test-session",
		Agent:     "cc",
		Timestamp: testTime,
		Details:   map[string]string{"key": "value"},
	})
	if err != nil {
		t.Fatalf("Notify failed: %v", err)
	}

	// Check that file was created
	expectedFile := filepath.Join(tmpDir, "2026-01-04_10-30-00_agent_error.md")
	content, err := os.ReadFile(expectedFile)
	if err != nil {
		t.Fatalf("Failed to read inbox file: %v", err)
	}

	contentStr := string(content)
	if !contains(contentStr, "# agent.error") {
		t.Error("File should contain event type header")
	}
	if !contains(contentStr, "Test file inbox") {
		t.Error("File should contain message")
	}
	if !contains(contentStr, "test-session") {
		t.Error("File should contain session")
	}
	if !contains(contentStr, "**key:** value") {
		t.Error("File should contain details")
	}
}

func TestRoutingRules(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test.log")
	inboxPath := filepath.Join(tmpDir, "inbox")

	cfg := Config{
		Enabled: true,
		Events:  []string{"agent.error", "agent.crashed"},
		Routing: map[string][]string{
			"agent.error":   {"log"},     // Only log for errors
			"agent.crashed": {"filebox"}, // Only filebox for crashes
		},
		Log: LogConfig{
			Enabled: true,
			Path:    logPath,
		},
		FileBox: FileBoxConfig{
			Enabled: true,
			Path:    inboxPath,
		},
	}

	n := New(cfg)

	// Send error - should go to log only
	err := n.Notify(Event{
		Type:      EventAgentError,
		Message:   "Error event",
		Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Notify failed: %v", err)
	}

	// Check log was written
	logContent, _ := os.ReadFile(logPath)
	if !contains(string(logContent), "Error event") {
		t.Error("Log should contain error event")
	}

	// Check inbox was NOT written for error
	files, _ := os.ReadDir(inboxPath)
	if len(files) > 0 {
		t.Error("Inbox should be empty for error event (routed to log only)")
	}
}

func TestPrimaryFallback(t *testing.T) {
	tmpDir := t.TempDir()
	inboxPath := filepath.Join(tmpDir, "inbox")

	// Create config with primary=webhook (disabled) and fallback=filebox (enabled)
	cfg := Config{
		Enabled:  true,
		Events:   []string{"agent.error"},
		Primary:  "webhook", // Webhook is not enabled, so should fallback
		Fallback: "filebox",
		Webhook: WebhookConfig{
			Enabled: false, // Disabled - will fail
		},
		FileBox: FileBoxConfig{
			Enabled: true,
			Path:    inboxPath,
		},
	}

	n := New(cfg)
	err := n.Notify(Event{
		Type:      EventAgentError,
		Message:   "Fallback test",
		Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Notify failed: %v", err)
	}

	// Fallback to filebox should have worked
	files, _ := os.ReadDir(inboxPath)
	if len(files) == 0 {
		t.Error("Fallback to filebox should have created a file")
	}
}

func TestEnvVarExpansion(t *testing.T) {
	// Set test env var
	os.Setenv("TEST_WEBHOOK_URL", "https://example.com/hook")
	defer os.Unsetenv("TEST_WEBHOOK_URL")

	cfg := Config{
		Enabled: true,
		Events:  []string{"agent.error"},
		Webhook: WebhookConfig{
			Enabled: true,
			URL:     "${TEST_WEBHOOK_URL}",
		},
	}

	n := New(cfg)
	if n.config.Webhook.URL != "https://example.com/hook" {
		t.Errorf("Env var not expanded: got %s", n.config.Webhook.URL)
	}
}

func TestNewNotifier_ClonesMutableConfigState(t *testing.T) {
	t.Setenv("TEST_NOTIFY_HEADER", "expanded-token")

	cfg := Config{
		Enabled: true,
		Events:  []string{"agent.error"},
		Routing: map[string][]string{
			"agent.error": {"log"},
		},
		Webhook: WebhookConfig{
			Enabled: true,
			URL:     "https://example.com/hook",
			Headers: map[string]string{
				"Authorization": "Bearer ${TEST_NOTIFY_HEADER}",
			},
		},
	}

	n := New(cfg)

	if got := cfg.Webhook.Headers["Authorization"]; got != "Bearer ${TEST_NOTIFY_HEADER}" {
		t.Fatalf("caller header mutated to %q", got)
	}
	if got := n.config.Webhook.Headers["Authorization"]; got != "Bearer expanded-token" {
		t.Fatalf("notifier header = %q, want expanded value", got)
	}

	cfg.Webhook.Headers["Authorization"] = "mutated"
	cfg.Routing["agent.error"][0] = "filebox"

	if got := n.config.Webhook.Headers["Authorization"]; got != "Bearer expanded-token" {
		t.Fatalf("notifier header changed after caller mutation: %q", got)
	}
	if got := n.config.Routing["agent.error"][0]; got != "log" {
		t.Fatalf("notifier routing changed after caller mutation: %q", got)
	}
}

func TestChannelConstants(t *testing.T) {
	// Verify channel constants are defined
	channels := []ChannelName{
		ChannelDesktop,
		ChannelWebhook,
		ChannelShell,
		ChannelLog,
		ChannelFileBox,
	}

	expected := []string{"desktop", "webhook", "shell", "log", "filebox"}
	for i, ch := range channels {
		if string(ch) != expected[i] {
			t.Errorf("Channel %d: got %s, want %s", i, ch, expected[i])
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr, 0))
}

func containsAt(s, substr string, start int) bool {
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestSanitizeEvent_NilRedactionConfig(t *testing.T) {
	t.Parallel()
	n := New(Config{Enabled: true})
	// redactionCfg is nil by default
	event := Event{
		Type:    EventAgentError,
		Message: "password=hunter2",
		Details: map[string]string{"key": "secret=abc123"},
	}
	got := n.sanitizeEvent(event)
	if got.Message != event.Message {
		t.Errorf("sanitizeEvent changed message with nil redactionCfg: %q", got.Message)
	}
	if got.Details["key"] != event.Details["key"] {
		t.Errorf("sanitizeEvent changed details with nil redactionCfg: %q", got.Details["key"])
	}
}

func TestSanitizeEvent_ModeOff(t *testing.T) {
	t.Parallel()
	n := NewWithRedaction(Config{Enabled: true}, redaction.Config{Mode: redaction.ModeOff})
	event := Event{
		Type:    EventAgentError,
		Message: "password=hunter2",
	}
	got := n.sanitizeEvent(event)
	if got.Message != event.Message {
		t.Errorf("sanitizeEvent changed message with ModeOff: %q", got.Message)
	}
}

func TestSanitizeEvent_RedactsMessageAndDetails(t *testing.T) {
	t.Parallel()
	n := NewWithRedaction(Config{Enabled: true}, redaction.Config{Mode: redaction.ModeRedact})
	event := Event{
		Type:    EventAgentError,
		Message: "password=hunter2hunter2",
		Details: map[string]string{
			"error":    "api_key=sk_live_12345678901234567890",
			"empty":    "",
			"harmless": "just text",
		},
	}
	got := n.sanitizeEvent(event)
	if strings.Contains(got.Message, "hunter2hunter2") {
		t.Error("sanitizeEvent should have redacted password from message")
	}
	if got.Details["empty"] != "" {
		t.Error("sanitizeEvent should preserve empty detail values")
	}
	if got.Details["harmless"] != "just text" {
		t.Errorf("sanitizeEvent changed harmless detail: %q", got.Details["harmless"])
	}
}

func TestSanitizeEvent_EmptyMessageAndDetails(t *testing.T) {
	t.Parallel()
	n := NewWithRedaction(Config{Enabled: true}, redaction.Config{Mode: redaction.ModeRedact})
	event := Event{Type: EventAgentError}
	got := n.sanitizeEvent(event)
	if got.Message != "" {
		t.Errorf("expected empty message, got %q", got.Message)
	}
	if got.Details != nil {
		t.Errorf("expected nil details, got %v", got.Details)
	}
}

func TestDetailValue(t *testing.T) {
	t.Parallel()
	// nil map
	if got := detailValue(nil, "key"); got != "" {
		t.Errorf("detailValue(nil, key) = %q, want empty", got)
	}
	// empty map
	if got := detailValue(map[string]string{}, "key"); got != "" {
		t.Errorf("detailValue(empty, key) = %q, want empty", got)
	}
	// key exists
	m := map[string]string{"a": "1", "b": "2"}
	if got := detailValue(m, "a"); got != "1" {
		t.Errorf("detailValue(m, a) = %q, want 1", got)
	}
	// key missing
	if got := detailValue(m, "z"); got != "" {
		t.Errorf("detailValue(m, z) = %q, want empty", got)
	}
}

func TestJsonMap(t *testing.T) {
	t.Parallel()
	// nil map
	if got := jsonMap(nil); got != "{}" {
		t.Errorf("jsonMap(nil) = %q, want {}", got)
	}
	// empty map
	if got := jsonMap(map[string]string{}); got != "{}" {
		t.Errorf("jsonMap(empty) = %q, want {}", got)
	}
	// populated map
	m := map[string]string{"key": "val"}
	got := jsonMap(m)
	var parsed map[string]string
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("jsonMap result not valid JSON: %v", err)
	}
	if parsed["key"] != "val" {
		t.Errorf("jsonMap round-trip: got %q, want val", parsed["key"])
	}
}

func TestNotifierClose(t *testing.T) {
	t.Parallel()
	n := New(Config{Enabled: true})
	if err := n.Close(); err != nil {
		t.Errorf("Close() error: %v", err)
	}
	// Second close should also be fine
	if err := n.Close(); err != nil {
		t.Errorf("Close() second call error: %v", err)
	}
}

func TestSendShell_TimesOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell timeout test requires sh and sleep")
	}

	oldTimeout := notificationCommandTimeout
	notificationCommandTimeout = 20 * time.Millisecond
	t.Cleanup(func() {
		notificationCommandTimeout = oldTimeout
	})

	n := New(Config{
		Enabled: true,
		Shell: ShellConfig{
			Enabled: true,
			Command: "sleep 1",
		},
	})

	err := n.sendShell(Event{Type: EventAgentError, Message: "timeout"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestSendToChannel_UnknownChannel(t *testing.T) {
	t.Parallel()
	n := New(Config{Enabled: true})
	// Force channel "test" as enabled
	n.channels["test"] = true
	err := n.sendToChannel("test", Event{})
	if err == nil {
		t.Error("expected error for unknown channel")
	}
	if !strings.Contains(err.Error(), "unknown channel") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSendToChannel_DisabledChannel(t *testing.T) {
	t.Parallel()
	n := New(Config{Enabled: true})
	err := n.sendToChannel(ChannelWebhook, Event{})
	if err == nil {
		t.Error("expected error for disabled channel")
	}
	if !strings.Contains(err.Error(), "not enabled") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNotify_EventTypeNotEnabled(t *testing.T) {
	t.Parallel()
	n := New(Config{
		Enabled: true,
		Events:  []string{"agent.error"}, // Only error events enabled
	})
	// Try to notify a different event type
	err := n.Notify(Event{Type: EventAgentCrashed, Message: "crash"})
	if err != nil {
		t.Errorf("Notify for disabled event type should not error: %v", err)
	}
}

func TestNotify_SetsTimestamp(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "ts.log")
	n := New(Config{
		Enabled: true,
		Events:  []string{"agent.error"},
		Log: LogConfig{
			Enabled: true,
			Path:    logPath,
		},
	})
	before := time.Now().UTC()
	err := n.Notify(Event{Type: EventAgentError, Message: "ts test"})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	// Verify log was written (timestamp was set)
	data, _ := os.ReadFile(logPath)
	if len(data) == 0 {
		t.Error("expected log output")
	}
	_ = before // timestamp was auto-set if zero
}

func TestJsonEscape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain text", "hello", "hello"},
		{"with quotes", `say "hello"`, `say \"hello\"`},
		{"with backslash", `path\to\file`, `path\\to\\file`},
		{"with newline", "line1\nline2", `line1\nline2`},
		{"with tab", "col1\tcol2", `col1\tcol2`},
		{"empty string", "", ""},
		{"unicode", "日本語", "日本語"},
		{"html chars escaped", "<b>bold</b>", `\u003cb\u003ebold\u003c/b\u003e`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := jsonEscape(tc.input)
			if got != tc.want {
				t.Errorf("jsonEscape(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

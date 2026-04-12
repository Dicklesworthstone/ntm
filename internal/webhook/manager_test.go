package webhook

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNewManager(t *testing.T) {
	t.Parallel()

	// Test with zero config (should use defaults)
	m := NewManager(ManagerConfig{})
	if m.config.QueueSize != DefaultQueueSize {
		t.Errorf("expected queue size %d, got %d", DefaultQueueSize, m.config.QueueSize)
	}
	if m.config.WorkerCount != DefaultWorkerCount {
		t.Errorf("expected worker count %d, got %d", DefaultWorkerCount, m.config.WorkerCount)
	}

	// Test with custom config
	m2 := NewManager(ManagerConfig{
		QueueSize:   100,
		WorkerCount: 5,
	})
	if m2.config.QueueSize != 100 {
		t.Errorf("expected queue size 100, got %d", m2.config.QueueSize)
	}
	if m2.config.WorkerCount != 5 {
		t.Errorf("expected worker count 5, got %d", m2.config.WorkerCount)
	}
}

func TestRegisterWebhook(t *testing.T) {
	t.Parallel()

	m := NewManager(DefaultManagerConfig())

	// Test valid registration
	err := m.Register(WebhookConfig{
		ID:      "test-webhook",
		URL:     "https://example.com/webhook",
		Enabled: true,
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Verify webhook was registered
	m.webhooksMu.RLock()
	wh, ok := m.webhooks["test-webhook"]
	m.webhooksMu.RUnlock()
	if !ok {
		t.Error("webhook not found after registration")
	}
	if wh.Method != "POST" {
		t.Errorf("expected method POST, got %s", wh.Method)
	}

	// Test registration without URL
	err = m.Register(WebhookConfig{
		ID:      "no-url",
		Enabled: true,
	})
	if err == nil {
		t.Error("expected error for webhook without URL")
	}

	// Test auto-generated ID
	err = m.Register(WebhookConfig{
		URL:     "https://example.com/webhook2",
		Enabled: true,
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRegisterRejectsInvalidTemplate(t *testing.T) {
	t.Parallel()

	m := NewManager(DefaultManagerConfig())

	err := m.Register(WebhookConfig{
		ID:       "bad-template",
		URL:      "https://example.com/webhook",
		Enabled:  true,
		Template: `{{if}`,
	})
	if err == nil {
		t.Fatal("expected invalid template error")
	}
}

func TestUnregisterWebhook(t *testing.T) {
	t.Parallel()

	m := NewManager(DefaultManagerConfig())

	// Register and then unregister
	err := m.Register(WebhookConfig{
		ID:      "test-webhook",
		URL:     "https://example.com/webhook",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	err = m.Unregister("test-webhook")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Verify webhook was removed
	m.webhooksMu.RLock()
	_, ok := m.webhooks["test-webhook"]
	m.webhooksMu.RUnlock()
	if ok {
		t.Error("webhook still exists after unregistration")
	}

	// Test unregistering non-existent webhook
	err = m.Unregister("non-existent")
	if err == nil {
		t.Error("expected error for non-existent webhook")
	}
}

func TestDispatchWithoutStart(t *testing.T) {
	t.Parallel()

	m := NewManager(DefaultManagerConfig())

	err := m.Dispatch(Event{Type: "test"})
	if err == nil {
		t.Error("expected error when dispatching before start")
	}
}

func TestDispatch_QueueOverflowDropsOldest(t *testing.T) {
	t.Parallel()

	m := NewManager(ManagerConfig{
		QueueSize:   1,
		WorkerCount: 1,
	})
	// Register a webhook so Dispatch() produces a delivery.
	if err := m.Register(WebhookConfig{
		ID:      "wh",
		URL:     "https://example.com/webhook",
		Enabled: true,
	}); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	// Dispatch() requires Start(), but for queue overflow behavior we only need the queue
	// and registered webhooks. Mark as started without launching worker goroutines.
	m.started.Store(true)

	// Pre-fill the queue to force overflow. Keep the pending count aligned with
	// a real accepted delivery so the test matches production invariants.
	m.pending.Add(1)
	m.queue <- Delivery{ID: "old_delivery"}

	if err := m.Dispatch(Event{
		ID:      "new_evt",
		Type:    "test.event",
		Message: "new",
	}); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	if got := m.queueFull.Load(); got != 1 {
		t.Fatalf("expected 1 dropped delivery, got %d", got)
	}
	if got := m.pending.Load(); got != 1 {
		t.Fatalf("expected 1 pending delivery after overflow replacement, got %d", got)
	}

	select {
	case d := <-m.queue:
		if d.Event.ID != "new_evt" {
			t.Fatalf("expected newest event to be queued, got %q", d.Event.ID)
		}
	default:
		t.Fatal("expected queued delivery, got none")
	}
}

func TestBasicDispatch(t *testing.T) {
	t.Parallel()

	var received atomic.Int32
	var mu sync.Mutex
	var receivedPayload Event
	done := make(chan struct{}, 1)

	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)

		// Verify headers
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("X-NTM-Event-Type") == "" {
			t.Error("missing X-NTM-Event-Type header")
		}

		// Parse payload
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		json.Unmarshal(body, &receivedPayload)
		mu.Unlock()

		w.WriteHeader(http.StatusOK)

		// Signal completion
		select {
		case done <- struct{}{}:
		default:
		}
	}))
	defer ts.Close()

	// Create and start manager
	m := NewManager(ManagerConfig{
		QueueSize:   10,
		WorkerCount: 2,
	})

	err := m.Register(WebhookConfig{
		ID:      "test",
		URL:     ts.URL,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	if err := m.Start(); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer m.Stop()

	// Dispatch event
	err = m.Dispatch(Event{
		Type:    "test.event",
		Message: "Hello webhook",
		Session: "test-session",
	})
	if err != nil {
		t.Errorf("dispatch failed: %v", err)
	}

	// Wait for delivery with timeout
	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for delivery, got %d deliveries", received.Load())
	}

	if received.Load() != 1 {
		t.Errorf("expected 1 delivery, got %d", received.Load())
	}

	mu.Lock()
	if receivedPayload.Type != "test.event" {
		t.Errorf("expected event type test.event, got %s", receivedPayload.Type)
	}
	if receivedPayload.Message != "Hello webhook" {
		t.Errorf("expected message 'Hello webhook', got %s", receivedPayload.Message)
	}
	mu.Unlock()
}

func TestDispatch_UsesBuiltInFormat(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var receivedBody []byte
	done := make(chan struct{}, 1)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		receivedBody = body
		mu.Unlock()

		w.WriteHeader(http.StatusOK)

		select {
		case done <- struct{}{}:
		default:
		}
	}))
	defer ts.Close()

	m := NewManager(ManagerConfig{
		QueueSize:   10,
		WorkerCount: 1,
	})

	if err := m.Register(WebhookConfig{
		ID:      "test",
		URL:     ts.URL,
		Enabled: true,
		Format:  "slack",
	}); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	if err := m.Start(); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer m.Stop()

	if err := m.Dispatch(Event{
		Type:    "agent.completed",
		Message: "all good",
		Session: "myproj",
		Pane:    "myproj__cc_1",
		Agent:   "claude",
		Details: map[string]string{"result": "pass"},
	}); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for delivery")
	}

	mu.Lock()
	body := append([]byte(nil), receivedBody...)
	mu.Unlock()

	if len(body) == 0 {
		t.Fatalf("expected non-empty webhook body")
	}

	var payload slackPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal slack payload: %v", err)
	}
	if payload.Text == "" {
		t.Fatalf("expected slack text fallback")
	}
	if len(payload.Blocks) < 2 {
		t.Fatalf("expected slack blocks, got %d", len(payload.Blocks))
	}
}

func TestDispatch_RedactsSecrets(t *testing.T) {
	t.Parallel()

	var received atomic.Int32
	var mu sync.Mutex
	var receivedPayload Event
	done := make(chan struct{}, 1)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)

		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(body, &receivedPayload)
		mu.Unlock()

		w.WriteHeader(http.StatusOK)

		select {
		case done <- struct{}{}:
		default:
		}
	}))
	defer ts.Close()

	m := NewManagerWithRedaction(ManagerConfig{
		QueueSize:   10,
		WorkerCount: 1,
	}, redaction.Config{Mode: redaction.ModeWarn})

	if err := m.Register(WebhookConfig{
		ID:      "test",
		URL:     ts.URL,
		Enabled: true,
	}); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	if err := m.Start(); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer m.Stop()

	if err := m.Dispatch(Event{
		Type:    "test.event",
		Message: "password=hunter2hunter2",
	}); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for delivery, got %d deliveries", received.Load())
	}

	mu.Lock()
	got := receivedPayload.Message
	mu.Unlock()
	if strings.Contains(got, "hunter2hunter2") {
		t.Fatalf("expected secret to be redacted, got %q", got)
	}
	if !strings.Contains(got, "[REDACTED:PASSWORD:") {
		t.Fatalf("expected redaction placeholder, got %q", got)
	}
}

func TestDispatch_RedactsSecrets_SlackFormat(t *testing.T) {
	t.Parallel()

	var received atomic.Int32
	var mu sync.Mutex
	var receivedBody []byte
	done := make(chan struct{}, 1)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		receivedBody = append([]byte(nil), body...)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		select {
		case done <- struct{}{}:
		default:
		}
	}))
	defer ts.Close()

	m := NewManagerWithRedaction(ManagerConfig{
		QueueSize:   10,
		WorkerCount: 1,
	}, redaction.Config{Mode: redaction.ModeWarn})

	if err := m.Register(WebhookConfig{
		ID:      "slack",
		URL:     ts.URL,
		Format:  "slack",
		Enabled: true,
	}); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	if err := m.Start(); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer m.Stop()

	secretPassword := "password=hunter2hunter2"
	secretKey := "sk-proj-FAKEtestkey1234567890123456789012345678901234"

	if err := m.Dispatch(Event{
		Type:    "test.event",
		Message: secretPassword,
		Details: map[string]string{"api_key": secretKey},
	}); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for delivery, got %d deliveries", received.Load())
	}

	mu.Lock()
	body := append([]byte(nil), receivedBody...)
	mu.Unlock()

	if len(body) == 0 {
		t.Fatalf("expected non-empty webhook body")
	}

	if err := json.Unmarshal(body, new(any)); err != nil {
		t.Fatalf("expected valid JSON payload, got unmarshal error: %v\nbody=%s", err, string(body))
	}

	bodyStr := string(body)
	if strings.Contains(bodyStr, "hunter2hunter2") {
		t.Fatalf("expected password secret to be redacted from slack payload")
	}
	if strings.Contains(bodyStr, "sk-proj-FAKE") {
		t.Fatalf("expected API key secret to be redacted from slack payload")
	}
	if !strings.Contains(bodyStr, "[REDACTED:") {
		t.Fatalf("expected redaction placeholder in slack payload")
	}
}

func TestDispatch_RedactsSecrets_TemplatePayload(t *testing.T) {
	t.Parallel()

	var received atomic.Int32
	var mu sync.Mutex
	var receivedBody []byte
	done := make(chan struct{}, 1)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		receivedBody = append([]byte(nil), body...)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		select {
		case done <- struct{}{}:
		default:
		}
	}))
	defer ts.Close()

	m := NewManagerWithRedaction(ManagerConfig{
		QueueSize:   10,
		WorkerCount: 1,
	}, redaction.Config{Mode: redaction.ModeWarn})

	if err := m.Register(WebhookConfig{
		ID:       "tmpl",
		URL:      ts.URL,
		Template: `{"message": {{json .Message}}, "details": {{json .Details}}}`,
		Enabled:  true,
	}); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	if err := m.Start(); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer m.Stop()

	secretPassword := "password=hunter2hunter2"
	secretKey := "sk-proj-FAKEtestkey1234567890123456789012345678901234"

	if err := m.Dispatch(Event{
		Type:    "test.event",
		Message: secretPassword,
		Details: map[string]string{"api_key": secretKey},
	}); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for delivery, got %d deliveries", received.Load())
	}

	mu.Lock()
	body := append([]byte(nil), receivedBody...)
	mu.Unlock()

	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("expected valid JSON payload, got unmarshal error: %v\nbody=%s", err, string(body))
	}

	bodyStr := string(body)
	if strings.Contains(bodyStr, "hunter2hunter2") {
		t.Fatalf("expected password secret to be redacted from template payload")
	}
	if strings.Contains(bodyStr, "sk-proj-FAKE") {
		t.Fatalf("expected API key secret to be redacted from template payload")
	}
	if !strings.Contains(bodyStr, "[REDACTED:") {
		t.Fatalf("expected redaction placeholder in template payload")
	}
}

func TestRetryLogic(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32

	// Create test server that fails first 2 attempts
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		if attempt <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// Create manager with fast retry for testing
	m := NewManager(ManagerConfig{
		QueueSize:   10,
		WorkerCount: 2,
	})

	err := m.Register(WebhookConfig{
		ID:      "retry-test",
		URL:     ts.URL,
		Enabled: true,
		Retry: RetryConfig{
			Enabled:    true,
			MaxRetries: 5,
			BaseDelay:  10 * time.Millisecond,
			MaxDelay:   50 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	if err := m.Start(); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer m.Stop()

	// Dispatch event
	err = m.Dispatch(Event{Type: "test.retry"})
	if err != nil {
		t.Errorf("dispatch failed: %v", err)
	}

	// Poll for 3 attempts (2 failures + 1 success)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if attempts.Load() >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if attempts.Load() != 3 {
		t.Errorf("expected 3 attempts (2 failures + 1 success), got %d", attempts.Load())
	}

	// Poll for stats delivery count
	var stats Stats
	for time.Now().Before(deadline) {
		stats = m.Stats()
		if stats.Deliveries >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if stats.Deliveries != 1 {
		t.Errorf("expected 1 successful delivery, got %d", stats.Deliveries)
	}
}

func TestNoRetryOn4xx(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32

	// Create test server that returns 400 Bad Request
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer ts.Close()

	m := NewManager(ManagerConfig{
		QueueSize:   10,
		WorkerCount: 2,
	})

	err := m.Register(WebhookConfig{
		ID:      "no-retry-test",
		URL:     ts.URL,
		Enabled: true,
		Retry: RetryConfig{
			Enabled:    true,
			MaxRetries: 5,
			BaseDelay:  10 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	if err := m.Start(); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer m.Stop()

	// Dispatch event
	err = m.Dispatch(Event{Type: "test.no-retry"})
	if err != nil {
		t.Errorf("dispatch failed: %v", err)
	}

	// Poll for dead letter to appear (4xx goes directly to dead letter, no retry)
	deadline := time.Now().Add(5 * time.Second)
	var stats Stats
	for time.Now().Before(deadline) {
		stats = m.Stats()
		if stats.DeadLetterCount >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Should only attempt once (no retry on 4xx)
	if attempts.Load() != 1 {
		t.Errorf("expected 1 attempt (no retry on 4xx), got %d", attempts.Load())
	}

	if stats.Failures != 1 {
		t.Errorf("expected 1 failure, got %d", stats.Failures)
	}
	if stats.DeadLetterCount != 1 {
		t.Errorf("expected 1 dead letter, got %d", stats.DeadLetterCount)
	}
}

func TestRetryOn429(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32

	// Create test server that returns 429 then succeeds
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		if attempt == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	m := NewManager(ManagerConfig{
		QueueSize:   10,
		WorkerCount: 2,
	})

	err := m.Register(WebhookConfig{
		ID:      "retry-429-test",
		URL:     ts.URL,
		Enabled: true,
		Retry: RetryConfig{
			Enabled:    true,
			MaxRetries: 5,
			BaseDelay:  10 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	if err := m.Start(); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer m.Stop()

	// Dispatch event
	err = m.Dispatch(Event{Type: "test.rate-limit"})
	if err != nil {
		t.Errorf("dispatch failed: %v", err)
	}

	// Poll for second attempt (retry on 429)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if attempts.Load() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Should retry on 429
	if attempts.Load() != 2 {
		t.Errorf("expected 2 attempts (retry on 429), got %d", attempts.Load())
	}
}

func TestDeadLetterQueue(t *testing.T) {
	t.Parallel()

	// Create test server that always fails
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	m := NewManager(ManagerConfig{
		QueueSize:       10,
		WorkerCount:     2,
		DeadLetterLimit: 5,
	})

	err := m.Register(WebhookConfig{
		ID:      "dead-letter-test",
		URL:     ts.URL,
		Enabled: true,
		Retry: RetryConfig{
			Enabled:    true,
			MaxRetries: 2,
			BaseDelay:  5 * time.Millisecond,
			MaxDelay:   10 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	if err := m.Start(); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer m.Stop()

	// Dispatch event that will fail all retries
	err = m.Dispatch(Event{Type: "test.fail"})
	if err != nil {
		t.Errorf("dispatch failed: %v", err)
	}

	// Poll for dead letter to appear after all retries exhaust
	deadline := time.Now().Add(5 * time.Second)
	var deadLetters []DeadLetter
	for time.Now().Before(deadline) {
		deadLetters = m.DeadLetters()
		if len(deadLetters) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(deadLetters) != 1 {
		t.Errorf("expected 1 dead letter, got %d", len(deadLetters))
	}

	if len(deadLetters) > 0 && deadLetters[0].LastError == "" {
		t.Error("dead letter should have last error")
	}

	// Test clearing dead letters
	cleared := m.ClearDeadLetters()
	if cleared != 1 {
		t.Errorf("expected to clear 1 dead letter, cleared %d", cleared)
	}

	deadLetters = m.DeadLetters()
	if len(deadLetters) != 0 {
		t.Errorf("expected 0 dead letters after clear, got %d", len(deadLetters))
	}
}

func TestEventFiltering(t *testing.T) {
	t.Parallel()

	var received atomic.Int32
	done := make(chan struct{}, 1)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
		// Signal on first delivery
		select {
		case done <- struct{}{}:
		default:
		}
	}))
	defer ts.Close()

	m := NewManager(ManagerConfig{
		QueueSize:   10,
		WorkerCount: 2,
	})

	// Register webhook that only receives "event.a" events
	err := m.Register(WebhookConfig{
		ID:      "filtered-webhook",
		URL:     ts.URL,
		Enabled: true,
		Events:  []string{"event.a"},
	})
	if err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	if err := m.Start(); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer m.Stop()

	// Dispatch matching event
	m.Dispatch(Event{Type: "event.a"})
	// Dispatch non-matching event
	m.Dispatch(Event{Type: "event.b"})

	// Wait for matching event delivery
	select {
	case <-done:
		// Success - wait a bit more to ensure non-matching event doesn't arrive
		time.Sleep(50 * time.Millisecond)
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for delivery, got %d deliveries", received.Load())
	}

	// Should only receive the matching event
	if received.Load() != 1 {
		t.Errorf("expected 1 delivery (filtered), got %d", received.Load())
	}
}

func TestHMACSignature(t *testing.T) {
	t.Parallel()

	var receivedSignature string
	var mu sync.Mutex
	done := make(chan struct{}, 1)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedSignature = r.Header.Get("X-NTM-Signature")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		select {
		case done <- struct{}{}:
		default:
		}
	}))
	defer ts.Close()

	m := NewManager(ManagerConfig{
		QueueSize:   10,
		WorkerCount: 2,
	})

	err := m.Register(WebhookConfig{
		ID:      "signed-webhook",
		URL:     ts.URL,
		Enabled: true,
		Secret:  "test-secret-key",
	})
	if err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	if err := m.Start(); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer m.Stop()

	// Dispatch event
	m.Dispatch(Event{Type: "test.signed"})

	// Wait for delivery
	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for delivery")
	}

	mu.Lock()
	sig := receivedSignature
	mu.Unlock()

	if sig == "" {
		t.Error("expected X-NTM-Signature header")
	}
	if len(sig) < 10 || sig[:7] != "sha256=" {
		t.Errorf("expected sha256= prefix in signature, got %s", sig)
	}
}

func TestCustomTemplate(t *testing.T) {
	t.Parallel()

	var receivedBody string
	var mu sync.Mutex
	done := make(chan struct{}, 1)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		receivedBody = string(body)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		select {
		case done <- struct{}{}:
		default:
		}
	}))
	defer ts.Close()

	m := NewManager(ManagerConfig{
		QueueSize:   10,
		WorkerCount: 2,
	})

	err := m.Register(WebhookConfig{
		ID:       "templated-webhook",
		URL:      ts.URL,
		Enabled:  true,
		Template: `{"text": "Event: {{.Type}}, Message: {{jsonEscape .Message}}"}`,
	})
	if err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	if err := m.Start(); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer m.Stop()

	// Dispatch event with special characters
	m.Dispatch(Event{
		Type:    "test.template",
		Message: `Hello "world"`,
	})

	// Wait for delivery
	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for delivery")
	}

	mu.Lock()
	body := receivedBody
	mu.Unlock()

	expected := `{"text": "Event: test.template, Message: Hello \"world\""}`
	if body != expected {
		t.Errorf("expected %s, got %s", expected, body)
	}
}

func TestStats(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	m := NewManager(ManagerConfig{
		QueueSize:   100,
		WorkerCount: 2,
	})

	err := m.Register(WebhookConfig{
		ID:      "stats-test",
		URL:     ts.URL,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	if err := m.Start(); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer m.Stop()

	// Dispatch multiple events
	for i := 0; i < 5; i++ {
		m.Dispatch(Event{Type: "test.stats"})
	}

	// Poll for stats to reach expected value (stats are updated after HTTP response)
	deadline := time.Now().Add(5 * time.Second)
	var stats Stats
	for time.Now().Before(deadline) {
		stats = m.Stats()
		if stats.Deliveries >= 5 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if stats.Deliveries != 5 {
		t.Errorf("expected 5 deliveries, got %d", stats.Deliveries)
	}
	if stats.WebhookCount != 1 {
		t.Errorf("expected 1 webhook, got %d", stats.WebhookCount)
	}
	if stats.QueueCapacity != 100 {
		t.Errorf("expected queue capacity 100, got %d", stats.QueueCapacity)
	}
}

func TestStartStop(t *testing.T) {
	t.Parallel()

	m := NewManager(DefaultManagerConfig())

	// Start
	if err := m.Start(); err != nil {
		t.Fatalf("start failed: %v", err)
	}

	// Double start should fail
	if err := m.Start(); err == nil {
		t.Error("expected error on double start")
	}

	// Stop
	if err := m.Stop(); err != nil {
		t.Fatalf("stop failed: %v", err)
	}

	// Dispatch after stop should fail
	if err := m.Dispatch(Event{Type: "test"}); err == nil {
		t.Error("expected error dispatching after stop")
	}
}

func TestStopWaitsForInFlightDelivery(t *testing.T) {
	t.Parallel()

	started := make(chan struct{}, 1)
	release := make(chan struct{})

	m := NewManager(ManagerConfig{
		QueueSize:   10,
		WorkerCount: 1,
	})
	if err := m.Register(WebhookConfig{
		ID:      "slow-stop",
		URL:     "https://example.com/webhook",
		Enabled: true,
	}); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	m.httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		select {
		case started <- struct{}{}:
		default:
		}

		select {
		case <-release:
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			}, nil
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	})

	if err := m.Start(); err != nil {
		t.Fatalf("start failed: %v", err)
	}

	if err := m.Dispatch(Event{Type: "test.stop"}); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for webhook request to start")
	}

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- m.Stop()
	}()

	time.Sleep(20 * time.Millisecond)
	close(release)

	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("stop failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for stop")
	}

	stats := m.Stats()
	if stats.Deliveries != 1 {
		t.Fatalf("expected 1 successful delivery during stop, got %+v", stats)
	}
	if stats.Failures != 0 {
		t.Fatalf("expected 0 failures during stop, got %+v", stats)
	}
}

func TestStartWaitsForConcurrentStop(t *testing.T) {
	t.Parallel()

	started := make(chan struct{}, 2)
	release := make(chan struct{})

	m := NewManager(ManagerConfig{
		QueueSize:   10,
		WorkerCount: 1,
	})
	if err := m.Register(WebhookConfig{
		ID:      "restart-during-stop",
		URL:     "https://example.com/webhook",
		Enabled: true,
	}); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	m.httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		select {
		case started <- struct{}{}:
		default:
		}

		select {
		case <-release:
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			}, nil
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	})

	if err := m.Start(); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer m.Stop()

	if err := m.Dispatch(Event{Type: "test.restart-blocking"}); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for initial webhook request to start")
	}

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- m.Stop()
	}()

	deadline := time.Now().Add(time.Second)
	for !m.stopping.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !m.stopping.Load() {
		t.Fatal("stop did not begin before timeout")
	}

	startDone := make(chan error, 1)
	go func() {
		startDone <- m.Start()
	}()

	select {
	case err := <-startDone:
		t.Fatalf("Start returned before Stop completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)

	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("stop failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for stop")
	}

	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("restart failed after stop completed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restart did not resume after stop completed")
	}

	if err := m.Dispatch(Event{Type: "test.restart-after-stop"}); err != nil {
		t.Fatalf("dispatch after restart failed: %v", err)
	}

	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m.Stats().Deliveries >= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	stats := m.Stats()
	t.Fatalf("expected restarted manager to deliver a second event, got %+v", stats)
}

func TestConcurrentDispatch(t *testing.T) {
	t.Parallel()

	var received atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		time.Sleep(10 * time.Millisecond) // Simulate some processing
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	m := NewManager(ManagerConfig{
		QueueSize:   1000,
		WorkerCount: 10,
	})

	err := m.Register(WebhookConfig{
		ID:      "concurrent-test",
		URL:     ts.URL,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	if err := m.Start(); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer m.Stop()

	// Dispatch many events concurrently
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			m.Dispatch(Event{
				Type:    "test.concurrent",
				Message: "Event " + string(rune('0'+n%10)),
			})
		}(i)
	}
	wg.Wait()

	// Poll for all 100 deliveries
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if received.Load() >= 100 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if received.Load() != 100 {
		t.Errorf("expected 100 deliveries, got %d", received.Load())
	}
}

func TestExponentialBackoff(t *testing.T) {
	t.Parallel()

	m := NewManager(DefaultManagerConfig())

	// Create a delivery with custom retry config
	wh := &WebhookConfig{
		Retry: RetryConfig{
			Enabled:    true,
			MaxRetries: 5,
			BaseDelay:  1 * time.Second,
			MaxDelay:   30 * time.Second,
		},
	}

	testCases := []struct {
		attempt  int
		expected time.Duration
	}{
		{1, 1 * time.Second},  // 1s
		{2, 2 * time.Second},  // 2s
		{3, 4 * time.Second},  // 4s
		{4, 8 * time.Second},  // 8s
		{5, 16 * time.Second}, // 16s
		{6, 30 * time.Second}, // Capped at max
	}

	for _, tc := range testCases {
		d := &Delivery{Webhook: wh, Attempt: tc.attempt}
		nextRetry := m.calculateNextRetry(d)
		delay := time.Until(nextRetry)

		// Allow some tolerance for timing
		minExpected := tc.expected - 100*time.Millisecond
		maxExpected := tc.expected + 100*time.Millisecond

		if delay < minExpected || delay > maxExpected {
			t.Errorf("attempt %d: expected delay ~%v, got %v", tc.attempt, tc.expected, delay)
		}
	}
}

func TestMatchesEvent(t *testing.T) {
	t.Parallel()

	m := NewManager(DefaultManagerConfig())

	tests := []struct {
		events   []string
		event    string
		expected bool
	}{
		{nil, "any.event", true},              // Empty = all events
		{[]string{}, "any.event", true},       // Empty = all events
		{[]string{"*"}, "any.event", true},    // Wildcard
		{[]string{"a.b"}, "a.b", true},        // Exact match
		{[]string{"a.b"}, "a.c", false},       // No match
		{[]string{"a.b", "a.c"}, "a.c", true}, // One of multiple
		{[]string{"agent.*"}, "agent.started", true},
		{[]string{"agent.*"}, "session.created", false},
		{[]string{" AGENT.* "}, "agent.started", true}, // Case + whitespace tolerant
	}

	for _, tt := range tests {
		wh := &WebhookConfig{Events: tt.events}
		result := m.matchesEvent(wh, tt.event)
		if result != tt.expected {
			t.Errorf("matchesEvent(%v, %s) = %v, expected %v",
				tt.events, tt.event, result, tt.expected)
		}
	}
}

// =============================================================================
// matchesEvent — missing branches (bd-4b4zf)
// =============================================================================

func TestMatchesEvent_MissingBranches(t *testing.T) {
	t.Parallel()

	m := NewManager(DefaultManagerConfig())

	tests := []struct {
		name     string
		events   []string
		event    string
		expected bool
	}{
		{"empty eventType", []string{"agent.started"}, "", false},
		{"empty event in list skipped", []string{"", "agent.started"}, "agent.started", true},
		{"whitespace-only event in list skipped", []string{"  ", "agent.started"}, "agent.started", true},
		{"no match returns false", []string{"agent.started"}, "session.created", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			wh := &WebhookConfig{Events: tt.events}
			got := m.matchesEvent(wh, tt.event)
			if got != tt.expected {
				t.Errorf("matchesEvent(%v, %q) = %v, want %v", tt.events, tt.event, got, tt.expected)
			}
		})
	}
}

// =============================================================================
// jsonEscape — all branches (bd-4b4zf)
// =============================================================================

func TestJsonEscape_AllBranches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple string", "hello", "hello"},
		{"with quotes", `say "hi"`, `say \"hi\"`},
		{"with newline", "line1\nline2", `line1\nline2`},
		{"with tab", "col1\tcol2", `col1\tcol2`},
		{"with backslash", `a\b`, `a\\b`},
		{"empty string", "", ""},
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

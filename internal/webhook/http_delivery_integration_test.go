package webhook

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the same Register/Start/Dispatch path used by the event-bus bridge,
// not just the HTTP helper, so wiring and retry/dead-letter accounting are pinned.
func TestManagerDispatchHonorsRetryAfter(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	m := NewManager(ManagerConfig{WorkerCount: 1, QueueSize: 2})
	if err := m.Register(WebhookConfig{
		ID: "cooldown", URL: srv.URL, Enabled: true,
		Retry: RetryConfig{Enabled: true, MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	started := time.Now()
	if err := m.Dispatch(Event{ID: "event", Type: "agent.error"}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.retryQueueMu.Lock()
		var retry Delivery
		if len(m.retryQueue) > 0 {
			retry = m.retryQueue[0]
		}
		m.retryQueueMu.Unlock()
		if retry.ID != "" {
			if retry.ID != "event_cooldown" || retry.Attempt != 1 || retry.NextRetry.Before(started.Add(time.Hour)) {
				t.Fatalf("lost delivery identity or receiver cooldown: %+v", retry)
			}
			if requests.Load() != 1 || m.deliveries.Load() != 0 || m.pending.Load() != 1 {
				t.Fatal("rate-limited event retried early or recorded as completed")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("delivery was not scheduled for retry")
}

func TestManagerDispatchDeadLettersRedirect(t *testing.T) {
	t.Parallel()
	var forwarded atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	m := NewManager(ManagerConfig{WorkerCount: 1, QueueSize: 2})
	if err := m.Register(WebhookConfig{
		ID: "redirect", URL: origin.URL, Enabled: true, Secret: "private",
		Retry: RetryConfig{Enabled: true, MaxRetries: 3},
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	if err := m.Dispatch(Event{ID: "event", Type: "agent.error"}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for m.pending.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if m.pending.Load() != 0 || m.failures.Load() != 1 || m.deliveries.Load() != 0 || forwarded.Load() != 0 {
		t.Fatalf("redirect was not a terminal failure: pending=%d failed=%d delivered=%d forwarded=%d",
			m.pending.Load(), m.failures.Load(), m.deliveries.Load(), forwarded.Load())
	}
	m.deadLettersMu.Lock()
	defer m.deadLettersMu.Unlock()
	if len(m.deadLetters) != 1 || m.deadLetters[0].Delivery.ID != "event_redirect" ||
		m.deadLetters[0].AttemptLog[0].StatusCode != http.StatusTemporaryRedirect {
		t.Fatal("redirect failure has no useful dead-letter receipt")
	}
}

package webhook

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

func TestManagerSlowRetryDoesNotBlockOtherEndpoints(t *testing.T) {
	t.Parallel()
	slowStarted := make(chan struct{}, 1)
	fastFinished := make(chan struct{}, 1)
	release := make(chan struct{})
	var slowAttempts, fastAttempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			if slowAttempts.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			slowStarted <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
			}
		} else {
			if fastAttempts.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fastFinished <- struct{}{}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	m := NewManager(ManagerConfig{WorkerCount: 2, QueueSize: 4})
	for _, id := range []string{"slow", "fast"} {
		if err := m.Register(WebhookConfig{
			ID: id, URL: srv.URL + "/" + id, Enabled: true, Events: []string{id},
			Retry: RetryConfig{Enabled: true, MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop() })
	t.Cleanup(func() { close(release) })
	if err := m.Dispatch(Event{Type: "slow"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-slowStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("slow endpoint never reached its retry")
	}
	if err := m.Dispatch(Event{Type: "fast"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fastFinished:
	case <-time.After(5 * time.Second):
		t.Fatal("slow retry blocked the unrelated endpoint's retry")
	}
	if slowAttempts.Load() != 2 || fastAttempts.Load() != 2 {
		t.Fatal("unexpected duplicate or missing attempts")
	}
}

func TestManagerRetryAdmissionIsBoundedAndClosedOnStop(t *testing.T) {
	t.Parallel()
	m := NewManager(ManagerConfig{QueueSize: 2, DeadLetterLimit: 10})
	for i := 0; i < 3; i++ {
		m.pending.Add(1)
		accepted := m.scheduleRetry(Delivery{
			ID: fmt.Sprintf("retry-%d", i), Attempt: 1, NextRetry: time.Now().Add(time.Hour),
			Error: &webhookHTTPError{statusCode: 503, body: "unavailable"},
		})
		if accepted != (i < 2) {
			t.Fatalf("retry %d admission=%v", i, accepted)
		}
	}
	if len(m.retryQueue) != 2 || m.pending.Load() != 2 || m.failures.Load() != 1 || m.queueFull.Load() != 1 {
		t.Fatal("retry overflow did not preserve bounded queue and terminal accounting")
	}
	if len(m.deadLetters) != 1 || m.deadLetters[0].Delivery.ID != "retry-2" ||
		m.deadLetters[0].AttemptLog[0].StatusCode != 503 || !strings.Contains(m.deadLetters[0].LastError, "retry queue full") {
		t.Fatal("overflow lost the last HTTP failure receipt")
	}

	// Reproduce the worker/shutdown interleaving: processDelivery decided to
	// retry, Stop drained the queue, and only then the worker calls scheduleRetry.
	m.stopping.Store(true)
	m.abandonPendingRetries()
	m.pending.Add(1)
	if m.scheduleRetry(Delivery{ID: "late", Attempt: 1}) {
		t.Fatal("retry admitted after the shutdown drain")
	}
	if len(m.retryQueue) != 0 || m.pending.Load() != 0 || m.failures.Load() != 4 || len(m.deadLetters) != 4 {
		t.Fatal("shutdown stranded a retry or counted a delivery twice")
	}
}

func TestManagerSnapshotsPayloadAndConfigAcrossRetries(t *testing.T) {
	t.Parallel()
	blockStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	type receipt struct{ body, key, signature, attempt string }
	receipts := make(chan receipt, 2)
	var targetAttempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var event Event
		if err := json.Unmarshal(body, &event); err != nil {
			t.Error(err)
			return
		}
		if event.ID == "block" {
			blockStarted <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		receipts <- receipt{string(body), r.Header.Get("X-API-Key"), r.Header.Get("X-NTM-Signature"), r.Header.Get("X-NTM-Attempt")}
		if targetAttempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(srv.Close)
	m := NewManager(ManagerConfig{WorkerCount: 1, QueueSize: 4})
	headers := map[string]string{"X-API-Key": "original-key"}
	events := []string{"snapshot"}
	if err := m.Register(WebhookConfig{
		ID: "snapshot", URL: srv.URL, Enabled: true, Headers: headers, Events: events, Secret: "signing-secret",
		Retry: RetryConfig{Enabled: true, MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
	}); err != nil {
		t.Fatal(err)
	}
	headers["X-API-Key"] = "changed-key"
	events[0] = "different-subscription"
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop() })
	t.Cleanup(unblock)
	if err := m.Dispatch(Event{ID: "block", Type: "snapshot"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blockStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("caller changed the registered subscription")
	}
	details := map[string]string{"value": "original-value"}
	if err := m.Dispatch(Event{ID: "target", Type: "snapshot", Details: details}); err != nil {
		t.Fatal(err)
	}
	details["value"] = "changed-value"
	unblock()
	var first receipt
	for i := 1; i <= 2; i++ {
		select {
		case got := <-receipts:
			var event Event
			if err := json.Unmarshal([]byte(got.body), &event); err != nil {
				t.Fatal(err)
			}
			if event.Details["value"] != "original-value" || got.key != "original-key" || got.attempt != fmt.Sprint(i) {
				t.Fatalf("caller mutation changed async delivery: %+v", got)
			}
			if got.signature != "sha256="+m.sign([]byte(got.body), "signing-secret") {
				t.Fatal("snapshot signature did not match its payload")
			}
			if i == 1 {
				first = got
			} else if got.body != first.body || got.signature != first.signature {
				t.Fatal("retry changed the signed event payload")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("snapshot delivery or retry missing")
		}
	}
}

func TestManagerConcurrentStopLeavesEveryAcceptedDeliveryAccountedFor(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	m := NewManager(ManagerConfig{WorkerCount: 2, QueueSize: 4})
	if err := m.Register(WebhookConfig{ID: "stop", URL: srv.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int64
	for generation := 0; generation < 5; generation++ {
		if err := m.Start(); err != nil {
			t.Fatal(err)
		}
		if err := m.Dispatch(Event{Type: "first"}); err != nil {
			t.Fatal(err)
		}
		accepted.Add(1)
		gate := make(chan struct{})
		var producers sync.WaitGroup
		for i := 0; i < 100; i++ {
			producers.Add(1)
			go func() {
				defer producers.Done()
				<-gate
				if m.Dispatch(Event{Type: "concurrent"}) == nil {
					accepted.Add(1)
				}
			}()
		}
		close(gate)
		if err := m.Stop(); err != nil {
			t.Fatal(err)
		}
		producers.Wait()
		if m.pending.Load() != 0 || len(m.queue) != 0 || len(m.retryQueue) != 0 {
			t.Fatal("Stop left accepted deliveries for the next generation")
		}
		if got := m.deliveries.Load() + m.failures.Load(); got != accepted.Load() {
			t.Fatalf("terminal outcomes=%d, accepted=%d", got, accepted.Load())
		}
	}
}

func TestManagerCancellationDrainsQueuedDeliveriesBeforeRestart(t *testing.T) {
	t.Parallel()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	m := NewManager(ManagerConfig{WorkerCount: 1, QueueSize: 4})
	if err := m.Register(WebhookConfig{ID: "cancel", URL: srv.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	if err := m.Dispatch(Event{ID: "in-flight"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery did not start")
	}
	for i := 0; i < 3; i++ {
		if err := m.Dispatch(Event{ID: fmt.Sprintf("queued-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	m.lifecycleMu.Lock()
	m.cancel()
	m.lifecycleMu.Unlock()
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	if m.pending.Load() != 0 || len(m.queue) != 0 || m.failures.Load() != 4 {
		t.Fatal("cancelled generation left queued deliveries without terminal outcomes")
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	if m.failures.Load() != 4 || m.deliveries.Load() != 0 {
		t.Fatal("restart replayed an old generation's delivery")
	}
}

func TestManagerRetriesShareTheConfiguredConcurrencyLimit(t *testing.T) {
	t.Parallel()
	const workers, count = 2, 6
	entered := make(chan struct{}, count)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var active, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	m := NewManager(ManagerConfig{WorkerCount: workers, QueueSize: count})
	wh := &WebhookConfig{URL: srv.URL, Method: http.MethodPost, Timeout: 5 * time.Second}
	for i := 0; i < count; i++ {
		m.pending.Add(1)
		if !m.scheduleRetry(Delivery{ID: fmt.Sprint(i), Webhook: wh, Attempt: 1}) {
			t.Fatal("retry was rejected before the queue filled")
		}
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop() })
	t.Cleanup(unblock)
	for i := 0; i < workers; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("retry dispatcher did not use the available workers")
		}
	}
	select {
	case <-entered:
		t.Fatal("retries exceeded the configured worker count")
	case <-time.After(50 * time.Millisecond):
	}
	unblock()
	deadline := time.Now().Add(5 * time.Second)
	for m.pending.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if m.pending.Load() != 0 || m.deliveries.Load() != count || peak.Load() != workers {
		t.Fatalf("pending=%d delivered=%d peak concurrency=%d", m.pending.Load(), m.deliveries.Load(), peak.Load())
	}
}

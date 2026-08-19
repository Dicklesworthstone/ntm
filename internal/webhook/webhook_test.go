package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestQueueOverflow_DropsOldest(t *testing.T) {
	t.Parallel()

	var (
		mu       sync.Mutex
		received []Event
	)

	block := make(chan struct{})
	firstStarted := make(chan struct{}, 1)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var ev Event
		_ = json.Unmarshal(body, &ev)

		mu.Lock()
		received = append(received, ev)
		n := len(received)
		mu.Unlock()

		if n == 1 {
			// Block the first delivery so the worker can't drain the queue.
			select {
			case firstStarted <- struct{}{}:
			default:
			}
			<-block
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	mgr := NewManager(ManagerConfig{
		QueueSize:   1,
		WorkerCount: 1,
	})
	if err := mgr.Register(WebhookConfig{
		ID:      "test",
		URL:     ts.URL,
		Enabled: true,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := mgr.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer mgr.Stop()

	if err := mgr.Dispatch(Event{Type: "test.queue", Message: "first"}); err != nil {
		t.Fatalf("dispatch first: %v", err)
	}

	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for first delivery to start")
	}

	// This will sit in the queue while the worker is blocked on the first delivery.
	_ = mgr.Dispatch(Event{Type: "test.queue", Message: "second"})
	// This should force a queue overflow and drop the queued "second" delivery.
	_ = mgr.Dispatch(Event{Type: "test.queue", Message: "third"})

	if managerStatsForTest(mgr).DroppedEvents == 0 {
		close(block)
		t.Fatalf("expected dropped events when queue is full")
	}

	close(block)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	got := append([]Event(nil), received...)
	mu.Unlock()

	if len(got) != 2 {
		t.Fatalf("expected 2 deliveries (first + newest), got %d", len(got))
	}
	if got[0].Message != "first" {
		t.Fatalf("expected first delivery to be first, got %q", got[0].Message)
	}
	if got[1].Message != "third" {
		t.Fatalf("expected overflow to keep newest delivery (third), got %q", got[1].Message)
	}
}

func TestHMACSignature_Deterministic(t *testing.T) {
	t.Parallel()

	m := NewManager(DefaultManagerConfig())
	secret := "test-secret"
	payload := []byte(`{"hello":"world"}`)

	got1 := m.sign(payload, secret)
	got2 := m.sign(payload, secret)
	if got1 != got2 {
		t.Fatalf("expected deterministic signature, got %q vs %q", got1, got2)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	want := hex.EncodeToString(mac.Sum(nil))
	if got1 != want {
		t.Fatalf("expected signature %q, got %q", want, got1)
	}

	gotOther := m.sign(payload, "different-secret")
	if gotOther == got1 {
		t.Fatalf("expected signature to differ with different secret")
	}
}

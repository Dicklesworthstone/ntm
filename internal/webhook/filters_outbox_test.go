//go:build !windows

package webhook

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebhookFiltersExcludeBeforeDurableFanout(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var received []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event Event
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Error(err)
		}
		mu.Lock()
		received = append(received, event.ID+":"+r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusBadRequest) // Retain terminal receipts for inspection.
	}))
	defer srv.Close()
	m := NewManager(ManagerConfig{OutboxDir: dir, WorkerCount: 1, QueueSize: 10})
	for _, session := range []string{"prod-*", "dev-*"} {
		wh := WebhookConfig{ID: session, URL: srv.URL + "/" + session, Enabled: true,
			Filter: &EventFilter{Session: session, AgentType: []string{"codex"}, Severity: []string{"error"}}}
		if err := m.Register(wh); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	for _, event := range []Event{
		{ID: "prod", Session: "prod-api", Type: "agent.error", Agent: "OpenAI"},
		{ID: "dev", Session: "dev-api", Type: "agent.crashed", Agent: "cod"},
		{ID: "wrong-session", Session: "staging-api", Type: "agent.error", Agent: "cod"},
		{ID: "wrong-agent", Session: "prod-api", Type: "agent.error", Agent: "cc"},
		{ID: "wrong-severity", Session: "prod-api", Type: "agent.started", Agent: "cod"},
	} {
		if err := m.Dispatch(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	sort.Strings(received)
	got := append([]string(nil), received...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "dev:/dev-*" || got[1] != "prod:/prod-*" {
		t.Fatalf("unintended endpoint delivery: %v", got)
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(paths) != 2 {
		t.Fatalf("persisted receipts = %v, err=%v; excluded events must not consume disk capacity", paths, err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("wrong-")) {
			t.Fatalf("excluded event persisted in %s", path)
		}
	}
}

func TestWebhookFilterChangePreservesReceiptUntilPolicyRestored(t *testing.T) {
	dir := t.TempDir()
	var ready atomic.Bool
	requests := make(chan outboxRequest, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- captureOutboxRequest(r)
		if !ready.Load() {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	wh := outboxTestWebhook(srv.URL)
	wh.Filter = &EventFilter{Session: "prod-*", AgentType: []string{"codex"}}
	first := startOutboxManager(t, dir, wh)
	if err := first.Dispatch(Event{ID: "retained", Session: "prod-api", Agent: "cod", Type: "agent.error"}); err != nil {
		t.Fatal(err)
	}
	awaitOutbox(t, func() bool { return len(requests) == 1 && first.pending.Load() == 0 })
	originalRequest := <-requests
	if err := first.Stop(); err != nil {
		t.Fatal(err)
	}
	path, record, before := readOnlyOutboxRecord(t, dir)
	if record.Attempt != 1 || record.Failed || record.NextRetry.IsZero() {
		t.Fatalf("missing pending receiver cooldown: %+v", record)
	}
	ready.Store(true)
	changed := wh
	changed.Filter = &EventFilter{Session: "dev-*", AgentType: []string{"codex"}}
	second := startOutboxManager(t, dir, changed)
	entry := adminEntry(t, second)
	if entry.BlockedBy != "configuration_changed" || entry.RetryAllowed {
		t.Fatalf("changed restriction not exposed: %+v", entry)
	}
	// Cross the real receiver deadline before synchronously scanning. A test
	// that only checked during Retry-After could pass without enforcing policy.
	if wait := time.Until(record.NextRetry); wait > 0 {
		time.Sleep(wait + 10*time.Millisecond)
	}
	second.feedOutbox()
	if err := second.Stop(); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 0 {
		t.Fatal("changed restriction replayed retained work")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("policy-blocked receipt was modified: %v", err)
	}
	// Equivalent provider aliases must not strand the receipt on restoration.
	wh.Filter = &EventFilter{Session: "prod-*", AgentType: []string{"OpenAI", "cod"}}
	third := startOutboxManager(t, dir, wh)
	awaitOutbox(t, func() bool { return third.deliveries.Load() == 1 })
	if err := third.Stop(); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatalf("restored delivery count = %d, want 1", len(requests))
	}
	recovered := <-requests
	if recovered.id != originalRequest.id || !bytes.Equal(recovered.body, originalRequest.body) || recovered.attempt != "2" {
		t.Fatalf("restored request lost identity, bytes, or attempt: %+v", recovered)
	}
}

func TestWebhookOutboxRechecksFiltersBeyondFingerprint(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "terminal"}[failed], func(t *testing.T) {
			dir := t.TempDir()
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()
			wh := outboxTestWebhook(srv.URL)
			wh.Filter = &EventFilter{Session: "prod-*"}
			m := startOutboxManager(t, dir, wh)
			if err := m.Stop(); err != nil {
				t.Fatal(err)
			}
			// Retained records are untrusted input. Matching configuration alone
			// is not evidence that the embedded event satisfies that policy.
			event := Event{ID: "filtered-evidence", Type: "agent.error", Session: "dev-api", Timestamp: time.Now().UTC()}
			payload, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			record := outboxRecord{Version: outboxVersion, ID: event.ID + "_hook", WebhookID: "hook",
				Fingerprint: m.outboxFingerprint(m.webhooks["hook"]), Event: event, Payload: payload, Failed: failed}
			if failed {
				record.Attempt = 1
				record.LastAttempt = &AttemptLog{Attempt: 1, Timestamp: time.Now().UTC(), StatusCode: 400, Error: "HTTP status 400"}
			}
			key := outboxKey(record.ID)
			if err := m.outbox.save(key, record); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, key)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			entry := adminEntry(t, m)
			if entry.BlockedBy != "event_filtered" || entry.RetryAllowed {
				t.Fatalf("inspection authorized filtered evidence: %+v", entry)
			}
			if _, err := m.RetryOutbox(key, entry.Revision); err == nil {
				t.Fatal("operator recovery authorized a policy-excluded event")
			}
			if err := m.Start(); err != nil {
				t.Fatal(err)
			}
			// Offer directly to exercise the worker's final claim, not only the
			// feeder's filter. The ordinary bounded worker must still refuse.
			m.lifecycleMu.Lock()
			m.offerOutbox(record, m.webhooks["hook"])
			m.lifecycleMu.Unlock()
			if err := m.Stop(); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) || requests.Load() != 0 {
				t.Fatalf("filtered evidence was sent or changed: requests=%d err=%v", requests.Load(), err)
			}
		})
	}
}

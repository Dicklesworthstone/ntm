package serve

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// bd-vq37v (D4): single-flight per scoped key, bounded cache, body
// fingerprinting, and streaming pass-through for the idempotency middleware.

// idempotencyStoreLen reports the number of cached entries.
func idempotencyStoreLen(s *IdempotencyStore) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// TestIdempotencyConcurrentDuplicateSingleExecution: two concurrent POSTs with
// the same Idempotency-Key must execute the handler exactly once; the loser
// waits and receives the replay.
func TestIdempotencyConcurrentDuplicateSingleExecution(t *testing.T) {
	srv, _ := setupTestServer(t)

	var executions atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		executions.Add(1)
		entered <- struct{}{}
		<-release // hold the flight open so the duplicate arrives mid-execution
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":"once"}`))
	})
	handler := srv.idempotencyMiddleware(inner)

	do := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/spawn", strings.NewReader(`{"n":1}`))
		req.Header.Set("Idempotency-Key", "race-key")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	var wg sync.WaitGroup
	results := make([]*httptest.ResponseRecorder, 2)
	wg.Add(1)
	go func() { defer wg.Done(); results[0] = do() }()

	<-entered // leader is inside the handler
	wg.Add(1)
	go func() { defer wg.Done(); results[1] = do() }()

	// Give the duplicate time to reach the follower wait, then release.
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := executions.Load(); got != 1 {
		t.Fatalf("handler executed %d times, want exactly 1", got)
	}
	replays := 0
	for i, rec := range results {
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status=%d, want 200: %s", i, rec.Code, rec.Body.String())
		}
		if rec.Body.String() != `{"result":"once"}` {
			t.Fatalf("request %d body=%q, want the single execution's body", i, rec.Body.String())
		}
		if rec.Header().Get("X-Idempotent-Replay") == "true" {
			replays++
		}
	}
	if replays != 1 {
		t.Fatalf("replay responses=%d, want exactly 1 (one execute, one replay)", replays)
	}
}

// TestIdempotencyStoreEviction: the cache must stay bounded, evicting the
// least-recently-used entry once idempotencyMaxEntries is reached.
func TestIdempotencyStoreEviction(t *testing.T) {
	store := NewIdempotencyStore(time.Hour)
	defer store.Stop()
	store.maxEntries = 8

	for i := 0; i < 8; i++ {
		store.SetWithFingerprint(fmt.Sprintf("key-%d", i), "", []byte("resp"), http.StatusOK, nil)
	}
	if got := idempotencyStoreLen(store); got != 8 {
		t.Fatalf("Len=%d, want 8", got)
	}
	// Touch key-0 so key-1 becomes the LRU victim.
	if _, _, _, _, ok := store.GetWithFingerprint("key-0"); !ok {
		t.Fatal("key-0 missing before eviction")
	}
	store.SetWithFingerprint("key-8", "", []byte("resp"), http.StatusOK, nil)
	if got := idempotencyStoreLen(store); got != 8 {
		t.Fatalf("Len=%d after insert at cap, want 8 (bounded)", got)
	}
	if _, _, _, _, ok := store.GetWithFingerprint("key-1"); ok {
		t.Fatal("key-1 should have been evicted as LRU")
	}
	if _, _, _, _, ok := store.GetWithFingerprint("key-0"); !ok {
		t.Fatal("recently used key-0 should have survived eviction")
	}
	if _, _, _, _, ok := store.GetWithFingerprint("key-8"); !ok {
		t.Fatal("newly inserted key-8 missing")
	}
}

// TestIdempotencyStoreRejectsOversizedResponse: responses over the byte cap
// are never cached.
func TestIdempotencyStoreRejectsOversizedResponse(t *testing.T) {
	store := NewIdempotencyStore(time.Hour)
	defer store.Stop()
	big := make([]byte, idempotencyMaxBodyBytes+1)
	store.SetWithFingerprint("big", "", big, http.StatusOK, nil)
	if _, _, _, _, ok := store.GetWithFingerprint("big"); ok {
		t.Fatal("oversized response must not be cached")
	}
}

// TestIdempotencyBodyMismatchRejected: same key + different body must 422
// instead of silently echoing the first response.
func TestIdempotencyBodyMismatchRejected(t *testing.T) {
	srv, _ := setupTestServer(t)
	var executions atomic.Int32
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		executions.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	handler := srv.idempotencyMiddleware(inner)

	do := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/spawn", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "fingerprint-key")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	first := do(`{"agents":1}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d, want 200", first.Code)
	}
	mismatch := do(`{"agents":9}`)
	if mismatch.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mismatched-body replay status=%d, want 422: %s", mismatch.Code, mismatch.Body.String())
	}
	if got := executions.Load(); got != 1 {
		t.Fatalf("handler executed %d times, want 1 (mismatch must not execute)", got)
	}
	same := do(`{"agents":1}`)
	if same.Code != http.StatusOK || same.Header().Get("X-Idempotent-Replay") != "true" {
		t.Fatalf("same-body replay status=%d replay=%q, want 200 replay", same.Code, same.Header().Get("X-Idempotent-Replay"))
	}
}

// TestIdempotencyBodyPreservedForHandler: fingerprinting must not consume the
// request body seen by the handler.
func TestIdempotencyBodyPreservedForHandler(t *testing.T) {
	srv, _ := setupTestServer(t)
	var seen string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 64)
		n, _ := r.Body.Read(b)
		seen = string(b[:n])
		w.WriteHeader(http.StatusOK)
	})
	handler := srv.idempotencyMiddleware(inner)
	req := httptest.NewRequest(http.MethodPost, "/spawn", strings.NewReader(`{"payload":"intact"}`))
	req.Header.Set("Idempotency-Key", "body-preserved")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if seen != `{"payload":"intact"}` {
		t.Fatalf("handler saw body %q, want original payload", seen)
	}
}

// TestResponseRecorderFlusherPassThrough: the recorder must forward Flush so
// streaming mutating handlers keep working when a key is present.
func TestResponseRecorderFlusherPassThrough(t *testing.T) {
	rec := httptest.NewRecorder() // implements http.Flusher
	rr := &responseRecorder{ResponseWriter: rec, statusCode: http.StatusOK}
	if _, ok := interface{}(rr).(http.Flusher); !ok {
		t.Fatal("responseRecorder must implement http.Flusher")
	}
	rr.Flush()
	if !rec.Flushed {
		t.Fatal("Flush was not forwarded to the underlying writer")
	}
	if unwrapped := rr.Unwrap(); unwrapped != rec {
		t.Fatal("Unwrap must return the underlying ResponseWriter")
	}
}

// TestResponseRecorderOverflowSkipsCaching: bodies over the cap stop buffering
// and mark the response uncacheable.
func TestResponseRecorderOverflowSkipsCaching(t *testing.T) {
	rec := httptest.NewRecorder()
	rr := &responseRecorder{ResponseWriter: rec, statusCode: http.StatusOK}
	chunk := make([]byte, 512*1024)
	for i := 0; i < 3; i++ { // 1.5 MiB total > 1 MiB cap
		if _, err := rr.Write(chunk); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if !rr.overflow {
		t.Fatal("overflow flag not set for oversized response")
	}
	if len(rr.body) != 0 {
		t.Fatalf("buffer holds %d bytes after overflow, want 0 (memory released)", len(rr.body))
	}
	if rec.Body.Len() != 3*512*1024 {
		t.Fatalf("client received %d bytes, want full stream", rec.Body.Len())
	}
}

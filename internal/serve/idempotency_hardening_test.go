package serve

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
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

func TestIdempotencyFlightRechecksCompletionAfterCacheMiss(t *testing.T) {
	store := NewIdempotencyStore(time.Hour)
	defer store.Stop()
	const key, fingerprint = "completed-between-lookup-and-flight", "original-body"
	if _, leader := store.BeginFlight(key); !leader {
		t.Fatal("first request did not acquire its flight")
	}
	// This is the exact interleaving at the old race: a second caller observes
	// a miss, then the original leader publishes its result and releases the
	// flight before that caller attempts acquisition. No scheduler timing or
	// test-only production hook is needed to reproduce the store state.
	if _, _, _, _, found := store.GetWithFingerprint(key); found {
		t.Fatal("in-flight request unexpectedly had a completed result")
	}
	want := []byte(`{"operation":"completed-once"}`)
	store.SetWithFingerprint(key, fingerprint, want, http.StatusCreated, http.Header{"Location": {"/operations/original"}})
	store.EndFlight(key)
	wait, leader := store.BeginFlight(key)
	if leader {
		store.EndFlight(key)
		t.Fatal("cache-miss follower became a new executor after the original request completed")
	}
	select {
	case <-wait:
	default:
		t.Fatal("completed result did not wake the follower to recheck the cache")
	}
	body, status, headers, gotFingerprint, found := store.GetWithFingerprint(key)
	if !found || status != http.StatusCreated || !bytes.Equal(body, want) || gotFingerprint != fingerprint || headers.Get("Location") != "/operations/original" {
		t.Fatal("completed result or its request identity was lost during acquisition")
	}
	// Expiration still permits a new operation under the documented cache
	// lifetime; an old map entry must not cause a busy retry loop forever.
	store.mu.Lock()
	store.entries[key].createdAt = time.Now().Add(-2 * time.Hour)
	store.mu.Unlock()
	if _, leader := store.BeginFlight(key); !leader {
		t.Fatal("expired result prevented a new flight")
	}
	store.EndFlight(key)
}

func TestIdempotencyOverflowRetainsTerminalResult(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		retain       bool
		wantRetained bool
	}{
		{"successful mutation", http.StatusAccepted, false, true},
		{"explicit terminal failure", http.StatusInternalServerError, true, true},
		{"ordinary refusal remains retryable", http.StatusBadRequest, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newWiringTestServer(t, nil)
			executions := 0
			payload := strings.Repeat("large operation result ", idempotencyMaxBodyBytes/len("large operation result ")+1)
			handler := s.requestIDMiddlewareFunc(s.idempotencyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				executions++
				if tc.retain {
					markIdempotencyResultRetained(r)
				}
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("Location", "/operations/original")
				w.WriteHeader(tc.status)
				if _, err := io.WriteString(w, payload); err != nil {
					t.Fatal(err)
				}
			})))
			do := func(body, requestID string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, "/operations", strings.NewReader(body))
				req.Header.Set("Idempotency-Key", "large-result")
				req.Header.Set(requestIDHeader, requestID)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				return rec
			}
			first := do(`{"action":"once"}`, "original-request")
			if first.Code != tc.status || first.Body.String() != payload || first.Header().Get("Content-Type") != "text/plain" || first.Header().Get("X-Idempotent-Replay") != "" {
				t.Fatal("retention altered the original response status, representation, or headers")
			}
			if !tc.wantRetained {
				retry := do(`{"action":"once"}`, "retry-request")
				if retry.Code != tc.status || retry.Body.String() != payload || retry.Header().Get("X-Idempotent-Replay") != "" || executions != 2 || idempotencyStoreLen(s.idempotencyStore) != 0 {
					t.Fatal("oversized ordinary refusal was incorrectly retained as a completed operation")
				}
				return
			}
			var retained []byte
			for attempt := 0; attempt < 2; attempt++ {
				retry := do(`{"action":"once"}`, "retry-request")
				var result APIError
				if err := json.Unmarshal(retry.Body.Bytes(), &result); err != nil {
					t.Fatalf("replay returned an invalid or truncated JSON receipt: %v", err)
				}
				if retry.Code != http.StatusConflict || retry.Header().Get("X-Idempotent-Replay") != "true" || executions != 1 ||
					result.Success || result.ErrorCode != "IDEMPOTENCY_RESULT_UNAVAILABLE" || result.Details["original_status"] != float64(tc.status) ||
					result.Details["response_unavailable"] != true || result.Details["inspection_required"] != true || result.Details["retry_safe"] != false ||
					!strings.Contains(result.Hint, "handler has already run") || retry.Body.Len() >= idempotencyMaxBodyBytes || strings.Contains(retry.Body.String(), "large operation result") {
					t.Fatalf("oversized terminal result was not safely retained: executions=%d status=%d body=%s", executions, retry.Code, retry.Body)
				}
				if retry.Header().Get("Content-Type") != "application/json" || retry.Header().Get("Location") != "/operations/original" ||
					retry.Header().Get(requestIDHeader) != "retry-request" || retry.Header().Get(idempotencyOriginalRequestIDHeader) != "original-request" ||
					result.RequestID != "original-request" {
					t.Fatal("inspection receipt lost its representation, operation location, or request correlation")
				}
				if attempt > 0 && !bytes.Equal(retry.Body.Bytes(), retained) {
					t.Fatal("same-key inspection receipt changed between retries")
				}
				retained = append([]byte(nil), retry.Body.Bytes()...)
			}
			changed := do(`{"action":"different"}`, "changed-request")
			if changed.Code != http.StatusUnprocessableEntity || executions != 1 || !strings.Contains(changed.Body.String(), ErrCodeIdempotentReplay) {
				t.Fatal("oversized result failed to retain the original request fingerprint")
			}
			if got := idempotencyStoreLen(s.idempotencyStore); got != 1 {
				t.Fatalf("retries created %d retained entries, want 1", got)
			}
		})
	}
}

func TestIdempotencyOversizedCheckpointImportDoesNotOverwriteOnRetry(t *testing.T) {
	cp, _ := checkpointDisclosureFixture(t, "clean")
	cp.Description = strings.Repeat("public recovery notes ", idempotencyMaxBodyBytes/len("public recovery notes ")+1)
	storage := checkpoint.NewStorage()
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "large-description.zip")
	if _, err := storage.Export(cp.SessionName, cp.ID, archivePath, checkpoint.ExportOptions{Format: checkpoint.FormatZip}); err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	s := newWiringTestServer(t, &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeBlock}})
	body := ImportCheckpointRequest{Data: base64.StdEncoding.EncodeToString(archive), AllowOverwrite: true}
	do := func(body ImportCheckpointRequest) *httptest.ResponseRecorder {
		req := wiringJSONRequest(t, http.MethodPost, "/api/v1/sessions/large-import/checkpoints/import", body)
		req.Header.Set("Idempotency-Key", "large-import-result")
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, req)
		return rec
	}
	first := do(body)
	if first.Code != http.StatusCreated || first.Body.Len() <= idempotencyMaxBodyBytes {
		t.Fatalf("fixture did not publish a checkpoint with an oversized success: status=%d response_bytes=%d", first.Code, first.Body.Len())
	}
	var original struct {
		Checkpoint CheckpointResponse `json:"checkpoint"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &original); err != nil || original.Checkpoint.Description != cp.Description {
		t.Fatal("first import response did not preserve its complete description")
	}
	stored, err := storage.Load("large-import", cp.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.Name = "intervening legitimate recovery state"
	if err := storage.Save(stored); err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(storage.CheckpointDir(stored.SessionName, stored.ID), checkpoint.MetadataFile)
	beforeRetry, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	retry := do(body)
	afterReplay, err := os.ReadFile(metadataPath)
	if err != nil || !bytes.Equal(beforeRetry, afterReplay) {
		t.Fatal("same-key retry overwrote intervening checkpoint state after an oversized success")
	}
	var result APIError
	if err := json.Unmarshal(retry.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if retry.Code != http.StatusConflict || retry.Header().Get("X-Idempotent-Replay") != "true" ||
		result.ErrorCode != "IDEMPOTENCY_RESULT_UNAVAILABLE" || result.Details["original_status"] != float64(http.StatusCreated) {
		t.Fatalf("retry did not report the already-published import: status=%d response_bytes=%d", retry.Code, retry.Body.Len())
	}
	body.AllowOverwrite = false
	changed := do(body)
	if changed.Code != http.StatusUnprocessableEntity || !strings.Contains(changed.Body.String(), ErrCodeIdempotentReplay) {
		t.Fatal("changed import body reused the original operation's retained identity")
	}
	afterRetry, err := os.ReadFile(metadataPath)
	if err != nil || !bytes.Equal(beforeRetry, afterRetry) {
		t.Fatal("same-key retry or changed-body reuse overwrote intervening checkpoint state")
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

// A policy update is a real persistent mutation. Two accepted JSON documents
// with the same long prefix must not share a successful replay when their
// policy content differs after the former 8 MiB fingerprint boundary.
func TestIdempotencyPolicyUpdateComparesCompleteAcceptedBody(t *testing.T) {
	padding := strings.Repeat(" ", 8<<20)
	firstPolicy := "version: 1\nblocked:\n  - pattern: original-danger\n    reason: original reason\n"
	secondPolicy := "version: 1\nblocked:\n  - pattern: revised-danger\n    reason: revised reason\n"
	encode := func(content string) string {
		body, err := json.Marshal(PolicyUpdateRequest{Content: content})
		if err != nil {
			t.Fatal(err)
		}
		return padding + string(body)
	}
	firstBody, secondBody := encode(firstPolicy), encode(secondPolicy)

	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		t.Run(string(mode), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Chdir(home)
			policyPath := filepath.Join(home, ".ntm", "policy.yaml")
			s := newWiringTestServer(t, &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: mode}})
			writes := 0
			s.policyWriteFile = func(path string, content []byte, permissions os.FileMode) error {
				writes++
				return os.WriteFile(path, content, permissions)
			}
			router := s.Router()
			do := func(body, key string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPut, "/api/v1/policy/", strings.NewReader(body))
				req.RemoteAddr = "127.0.0.1:12345"
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Idempotency-Key", key)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				return rec
			}
			assertPolicy := func(want string, wantWrites int) {
				t.Helper()
				got, err := os.ReadFile(policyPath)
				if err != nil || string(got) != want || writes != wantWrites {
					t.Fatalf("policy=%q writes=%d, want policy=%q writes=%d, err=%v", got, writes, want, wantWrites, err)
				}
			}

			first := do(firstBody, "complete-policy-body")
			if first.Code != http.StatusOK || first.Header().Get("X-Idempotent-Replay") != "" {
				t.Fatalf("initial update status=%d replay=%q: %s", first.Code, first.Header().Get("X-Idempotent-Replay"), first.Body)
			}
			assertPolicy(firstPolicy, 1)

			changed := do(secondBody, "complete-policy-body")
			var refusal APIError
			if err := json.Unmarshal(changed.Body.Bytes(), &refusal); err != nil || changed.Code != http.StatusUnprocessableEntity || refusal.ErrorCode != ErrCodeIdempotentReplay {
				t.Fatalf("changed policy beyond common prefix status=%d replay=%q, want 422 conflict: %s (decode=%v)", changed.Code, changed.Header().Get("X-Idempotent-Replay"), changed.Body, err)
			}
			assertPolicy(firstPolicy, 1)

			retry := do(firstBody, "complete-policy-body")
			if retry.Code != http.StatusOK || retry.Header().Get("X-Idempotent-Replay") != "true" || retry.Body.String() != first.Body.String() {
				t.Fatalf("exact retry status=%d replay=%q: %s", retry.Code, retry.Header().Get("X-Idempotent-Replay"), retry.Body)
			}
			assertPolicy(firstPolicy, 1)

			fresh := do(secondBody, "revised-policy-body")
			if fresh.Code != http.StatusOK || fresh.Header().Get("X-Idempotent-Replay") != "" {
				t.Fatalf("fresh-key update status=%d replay=%q: %s", fresh.Code, fresh.Header().Get("X-Idempotent-Replay"), fresh.Body)
			}
			assertPolicy(secondPolicy, 2)
		})
	}
}

// A transport may return complete-looking JSON together with a read error.
// Decoding that prefix, or treating its missing fingerprint as a cache hit,
// would acknowledge an incomplete request as a successful mutation.
type idempotencyErrorBody struct {
	data []byte
	err  error
}

func (b *idempotencyErrorBody) Read(p []byte) (int, error) {
	n := copy(p, b.data)
	b.data = b.data[n:]
	if len(b.data) == 0 {
		return n, b.err
	}
	return n, nil
}

func (b *idempotencyErrorBody) Close() error { return nil }

func TestIdempotencyRejectsIncompleteOrOversizedRequests(t *testing.T) {
	const acceptedBody = `{"operation":"accepted"}`
	const partialBody = `{"operation":"unreadable"}`
	const limit = 10 << 20
	oversizedBody := partialBody + strings.Repeat(" ", limit+1-len(partialBody))
	tests := []struct {
		name          string
		body          func(http.ResponseWriter) io.ReadCloser
		requestCtx    func() (context.Context, context.CancelFunc)
		contentLength int64
		wantStatus    int
		wantCode      string
	}{
		{
			name: "read error with complete JSON prefix",
			body: func(http.ResponseWriter) io.ReadCloser {
				return &idempotencyErrorBody{data: []byte(partialBody), err: errors.New("transport failed: " + wiringFakeSecret)}
			},
			contentLength: -1,
			wantStatus:    http.StatusBadRequest,
			wantCode:      ErrCodeBadRequest,
		},
		{
			name: "canceled before fingerprinting",
			body: func(http.ResponseWriter) io.ReadCloser {
				return io.NopCloser(strings.NewReader(partialBody))
			},
			requestCtx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			contentLength: -1,
			wantStatus:    http.StatusRequestTimeout,
			wantCode:      ErrCodeTimeout,
		},
		{
			name: "deadline exceeded before fingerprinting",
			body: func(http.ResponseWriter) io.ReadCloser {
				return io.NopCloser(strings.NewReader(partialBody))
			},
			requestCtx: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			contentLength: -1,
			wantStatus:    http.StatusRequestTimeout,
			wantCode:      ErrCodeTimeout,
		},
		{
			name: "oversized chunked body",
			body: func(http.ResponseWriter) io.ReadCloser {
				return io.NopCloser(strings.NewReader(oversizedBody))
			},
			contentLength: -1,
			wantStatus:    http.StatusRequestEntityTooLarge,
			wantCode:      "REQUEST_TOO_LARGE",
		},
		{
			name: "oversized body with misleading length",
			body: func(http.ResponseWriter) io.ReadCloser {
				return io.NopCloser(strings.NewReader(oversizedBody))
			},
			contentLength: 1,
			wantStatus:    http.StatusRequestEntityTooLarge,
			wantCode:      "REQUEST_TOO_LARGE",
		},
		{
			name: "MaxBytesReader reports overflow at the limit",
			body: func(w http.ResponseWriter) io.ReadCloser {
				return http.MaxBytesReader(w, io.NopCloser(strings.NewReader(oversizedBody)), limit)
			},
			contentLength: -1,
			wantStatus:    http.StatusRequestEntityTooLarge,
			wantCode:      "REQUEST_TOO_LARGE",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, cached := range []bool{false, true} {
				t.Run(fmt.Sprintf("cached=%t", cached), func(t *testing.T) {
					store := NewIdempotencyStore(time.Hour)
					t.Cleanup(store.Stop)
					s := &Server{idempotencyStore: store}
					executions := 0
					persisted := ""
					handler := s.idempotencyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var input struct {
							Operation string `json:"operation"`
						}
						if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
							t.Errorf("failed request reached handler: %v", err)
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						executions++
						persisted = input.Operation
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"success":true}`))
					}))
					doAccepted := func() *httptest.ResponseRecorder {
						req := httptest.NewRequest(http.MethodPost, "/mutation", strings.NewReader(acceptedBody))
						req.Header.Set("Idempotency-Key", "read-integrity")
						rec := httptest.NewRecorder()
						handler.ServeHTTP(rec, req)
						return rec
					}
					if cached {
						if first := doAccepted(); first.Code != http.StatusOK {
							t.Fatalf("seed success status=%d: %s", first.Code, first.Body)
						}
					}
					beforeExecutions, beforeValue := executions, persisted
					req := httptest.NewRequest(http.MethodPost, "/mutation", nil)
					rec := httptest.NewRecorder()
					req.Body = tt.body(rec)
					req.ContentLength = tt.contentLength
					req.Header.Set("Idempotency-Key", "read-integrity")
					if tt.requestCtx != nil {
						ctx, cancel := tt.requestCtx()
						t.Cleanup(cancel)
						req = req.WithContext(ctx)
					}
					handler.ServeHTTP(rec, req)
					var refusal APIError
					if err := json.Unmarshal(rec.Body.Bytes(), &refusal); err != nil || rec.Code != tt.wantStatus || refusal.ErrorCode != tt.wantCode {
						t.Fatalf("failed input status=%d code=%q, want %d %s: %s (decode=%v)", rec.Code, refusal.ErrorCode, tt.wantStatus, tt.wantCode, rec.Body, err)
					}
					if rec.Header().Get("X-Idempotent-Replay") != "" || executions != beforeExecutions || persisted != beforeValue {
						t.Fatalf("failed input replayed or mutated state: replay=%q executions=%d value=%q", rec.Header().Get("X-Idempotent-Replay"), executions, persisted)
					}
					if strings.Contains(rec.Body.String(), wiringFakeSecret) {
						t.Fatal("transport error disclosed credential contents")
					}
					// Failed admission neither consumes a new key nor overwrites
					// the successful response already associated with one.
					retry := doAccepted()
					if retry.Code != http.StatusOK || (retry.Header().Get("X-Idempotent-Replay") == "true") != cached || executions != 1 || persisted != "accepted" {
						t.Fatalf("intact retry status=%d replay=%q executions=%d value=%q", retry.Code, retry.Header().Get("X-Idempotent-Replay"), executions, persisted)
					}
				})
			}
		})
	}
}

func TestIdempotencyAcceptsAndPreservesMaximumRequestBody(t *testing.T) {
	const input = `{"payload":"intact"}`
	body := input + strings.Repeat(" ", (10<<20)-len(input))
	store := NewIdempotencyStore(time.Hour)
	t.Cleanup(store.Stop)
	s := &Server{idempotencyStore: store}
	executions := 0
	handler := s.idempotencyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil || string(data) != body {
			t.Errorf("maximum body changed before handler: length=%d err=%v", len(data), err)
		}
		executions++
		w.WriteHeader(http.StatusNoContent)
	}))
	for attempt := 0; attempt < 2; attempt++ {
		req := httptest.NewRequest(http.MethodPost, "/mutation", strings.NewReader(body))
		req.ContentLength = -1
		req.Header.Set("Idempotency-Key", "maximum-request")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent || executions != 1 || (rec.Header().Get("X-Idempotent-Replay") == "true") != (attempt == 1) {
			t.Fatalf("maximum request attempt=%d status=%d executions=%d replay=%q", attempt, rec.Code, executions, rec.Header().Get("X-Idempotent-Replay"))
		}
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

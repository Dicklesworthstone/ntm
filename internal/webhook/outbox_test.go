//go:build !windows

package webhook

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

type outboxRequest struct {
	id, attempt, signature string
	body                   []byte
	at                     time.Time
}

func captureOutboxRequest(r *http.Request) outboxRequest {
	body, _ := io.ReadAll(r.Body)
	return outboxRequest{r.Header.Get("X-NTM-Delivery-ID"), r.Header.Get("X-NTM-Attempt"), r.Header.Get("X-NTM-Signature"), body, time.Now()}
}

func outboxTestWebhook(url string) WebhookConfig {
	return WebhookConfig{ID: "hook", URL: url, Enabled: true, Timeout: 30 * time.Second,
		Secret: "outbox-signing-fixture", Headers: map[string]string{"Authorization": "Bearer outbox-auth-fixture"},
		Retry: RetryConfig{Enabled: true, MaxRetries: 3, BaseDelay: 25 * time.Millisecond, MaxDelay: 25 * time.Millisecond}}
}

func awaitOutbox(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for webhook outbox condition")
}

func startOutboxManager(t *testing.T, dir string, wh WebhookConfig) *WebhookManager {
	t.Helper()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	m := NewManager(ManagerConfig{OutboxDir: dir, WorkerCount: 1, QueueSize: 1, OutboxLimit: 20})
	if err := m.Register(wh); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Stop(); err != nil {
			t.Error(err)
		}
	})
	return m
}

func readOnlyOutboxRecord(t *testing.T, dir string) (string, outboxRecord, []byte) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("expected one durable receipt, paths=%v err=%v", paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var record outboxRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	return paths[0], record, data
}

// Uses public manager operations and a real receiver. Reflection keeps this
// probe runnable against the original manager as well: that manager has no
// OutboxDir, and the receiver observes that restart lost the accepted retry.
func TestWebhookRestartPreservesRetryAndFrozenRequest(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var ready atomic.Bool
	requests := make(chan outboxRequest, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- captureOutboxRequest(r)
		if !ready.Load() {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cfg := DefaultManagerConfig()
	cfg.WorkerCount = 1
	field := reflect.ValueOf(&cfg).Elem().FieldByName("OutboxDir")
	if field.IsValid() {
		field.SetString(dir)
	}
	wh := outboxTestWebhook(server.URL)
	first := NewManager(cfg)
	if err := first.Register(wh); err != nil {
		t.Fatal(err)
	}
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Stop() })
	event := Event{ID: "restart", Type: "bead.completed", Message: "original", Details: map[string]string{"result": "frozen"}}
	if err := first.Dispatch(event); err != nil {
		t.Fatal(err)
	}
	var before outboxRequest
	select {
	case before = <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("first attempt never arrived")
	}
	event.Details["result"] = "changed after acceptance"
	if err := first.Stop(); err != nil {
		t.Fatal(err)
	}
	ready.Store(true)
	second := NewManager(cfg)
	if err := second.Register(wh); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Stop() })
	var after outboxRequest
	select {
	case after = <-requests:
	case <-time.After(4 * time.Second):
		t.Fatal("restart lost the accepted webhook retry")
	}
	if after.id != before.id || after.attempt != "2" || !bytes.Equal(after.body, before.body) || after.signature != before.signature {
		t.Fatalf("retry identity or payload changed: before=%+v after=%+v", before, after)
	}
	if after.at.Sub(before.at) < time.Second {
		t.Fatalf("restart shortened receiver Retry-After: %v", after.at.Sub(before.at))
	}
	mac := hmac.New(sha256.New, []byte(wh.Secret))
	_, _ = mac.Write(after.body)
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); after.signature != want {
		t.Fatalf("signature=%s want=%s", after.signature, want)
	}
	if err := second.Stop(); err != nil {
		t.Fatal(err)
	}
	third := NewManager(cfg)
	if err := third.Register(wh); err != nil {
		t.Fatal(err)
	}
	if err := third.Start(); err != nil {
		t.Fatal(err)
	}
	defer third.Stop()
	select {
	case r := <-requests:
		t.Fatalf("acknowledged webhook replayed: %+v", r)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestWebhookOutboxImmediateStopDrainsAcceptedFirstAttempt(t *testing.T) {
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); w.WriteHeader(204) }))
	defer server.Close()
	m := startOutboxManager(t, t.TempDir(), outboxTestWebhook(server.URL))
	if err := m.Dispatch(Event{ID: "closing", Type: "session.killed", Message: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := received.Load(); got != 1 {
		t.Fatalf("Stop left a first attempt for the next process: received=%d", got)
	}
}

// These helpers are actual independent manager processes, not simulated lock
// holders. The parent kills the sender in the network/acknowledgement window.
func TestWebhookOutboxProcessHelper(t *testing.T) {
	mode := os.Getenv("NTM_OUTBOX_TEST_HELPER")
	if mode == "" {
		return
	}
	m := startOutboxManager(t, os.Getenv("NTM_OUTBOX_TEST_DIR"), outboxTestWebhook(os.Getenv("NTM_OUTBOX_TEST_URL")))
	if mode == "send" {
		if err := m.Dispatch(Event{ID: "crashed", Type: "bead.assigned", Message: "must survive SIGKILL"}); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println("outbox-helper-ready")
	select {}
}

func startOutboxProcess(t *testing.T, mode, dir, url string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestWebhookOutboxProcessHelper$")
	cmd.Env = append(os.Environ(), "NTM_OUTBOX_TEST_HELPER="+mode, "NTM_OUTBOX_TEST_DIR="+dir, "NTM_OUTBOX_TEST_URL="+url)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if scanner.Text() == "outbox-helper-ready" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("outbox helper exited without starting")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("outbox helper failed to start")
	}
	return cmd
}

func TestWebhookOutboxCrashRecoveryAndCrossProcessOwnership(t *testing.T) {
	dir := t.TempDir()
	requests := make(chan outboxRequest, 8)
	cancelled := make(chan struct{})
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		requests <- captureOutboxRequest(r)
		if n == 1 {
			<-r.Context().Done()
			close(cancelled)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	first := startOutboxProcess(t, "send", dir, server.URL)
	var before outboxRequest
	select {
	case before = <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("sender never reached HTTP")
	}
	second := startOutboxProcess(t, "recover", dir, server.URL)
	select {
	case duplicate := <-requests:
		t.Fatalf("live managers sent the same receipt concurrently: %+v", duplicate)
	case <-time.After(1200 * time.Millisecond):
	}
	if err := first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = first.Wait()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("killed sender kept its HTTP connection")
	}
	var after outboxRequest
	select {
	case after = <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("surviving process did not recover crashed ownership")
	}
	if before.id != after.id || after.attempt != "2" || !bytes.Equal(before.body, after.body) || before.signature != after.signature {
		t.Fatalf("crash replay changed request identity: before=%+v after=%+v", before, after)
	}
	awaitOutbox(t, func() bool { paths, _ := filepath.Glob(filepath.Join(dir, "*.json")); return len(paths) == 0 })
	if err := second.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = second.Wait()
}

func TestWebhookOutboxDiskCapacityAndIdempotentAdmission(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(503)
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := ManagerConfig{OutboxDir: dir, OutboxLimit: 1, WorkerCount: 1, QueueSize: 1}
	m := NewManager(cfg)
	wh := outboxTestWebhook(server.URL)
	if err := m.Register(wh); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop() })
	event := Event{ID: "stable", Type: "agent.error", Message: "unchanged", Timestamp: time.Now().UTC()}
	if err := m.Dispatch(event); err != nil {
		t.Fatal(err)
	}
	awaitOutbox(t, func() bool { return m.pending.Load() == 0 })
	_, before, _ := readOnlyOutboxRecord(t, dir)
	if before.Attempt != 1 || before.NextRetry.Before(time.Now().Add(30*time.Minute)) {
		t.Fatalf("missing durable cooldown: %+v", before)
	}
	if err := m.Dispatch(event); err != nil {
		t.Fatalf("same event was not idempotently admitted: %v", err)
	}
	changed := event
	changed.Message = "different"
	if err := m.Dispatch(changed); err == nil {
		t.Fatal("same delivery identity accepted different content")
	}
	changed.ID = "overflow"
	if err := m.Dispatch(changed); err == nil {
		t.Fatal("outbox exceeded its disk capacity")
	}
	_, after, _ := readOnlyOutboxRecord(t, dir)
	if after.Attempt != before.Attempt || !after.NextRetry.Equal(before.NextRetry) || after.Event.Message != before.Event.Message {
		t.Fatal("re-admission or overflow rewrote the existing receipt")
	}
}

func TestWebhookOutboxQueuePressureRetainsAcceptedDeliveries(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release; received.Add(1); w.WriteHeader(204) }))
	defer func() { once.Do(func() { close(release) }); server.Close() }()
	m := startOutboxManager(t, t.TempDir(), outboxTestWebhook(server.URL))
	for i := 0; i < 12; i++ {
		if err := m.Dispatch(Event{ID: fmt.Sprintf("queued-%d", i), Type: "bead.completed", Message: "queued"}); err != nil {
			t.Fatal(err)
		}
	}
	paths, _ := filepath.Glob(filepath.Join(m.outbox.dir, "*.json"))
	if len(paths) != 12 {
		t.Fatalf("queue pressure lost persisted deliveries: %d", len(paths))
	}
	once.Do(func() { close(release) })
	awaitOutbox(t, func() bool { return received.Load() == 12 })
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	paths, _ = filepath.Glob(filepath.Join(m.outbox.dir, "*.json"))
	if len(paths) != 0 {
		t.Fatalf("acknowledged receipts remain: %v", paths)
	}
}

func TestWebhookOutboxPersistsRedactedContentNotCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(503)
		_, _ = io.WriteString(w, "endpoint-uri-fixture outbox-auth-fixture")
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	redact := redaction.Config{Mode: redaction.ModeRedact, ExtraPatterns: map[redaction.Category][]string{"LOCAL": {`private-marker-[0-9]+`}}}
	m := NewManagerWithRedaction(ManagerConfig{OutboxDir: dir, WorkerCount: 1}, redact)
	redact.ExtraPatterns["LOCAL"][0] = "pattern-mutated-after-construction"
	wh := outboxTestWebhook(server.URL + "?token=endpoint-uri-fixture")
	if err := m.Register(wh); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop() })
	if err := m.Dispatch(Event{ID: "redacted", Type: "agent.error", Message: "private-marker-123", Details: map[string]string{"note": "private-marker-456"}}); err != nil {
		t.Fatal(err)
	}
	awaitOutbox(t, func() bool { return m.pending.Load() == 0 })
	path, record, data := readOnlyOutboxRecord(t, dir)
	for _, secret := range []string{"private-marker-123", "private-marker-456", wh.URL, wh.Secret, "outbox-auth-fixture", "endpoint-uri-fixture"} {
		if bytes.Contains(data, []byte(secret)) || bytes.Contains(record.Payload, []byte(secret)) {
			t.Errorf("durable receipt leaked %q", secret)
		}
	}
	if !strings.Contains(record.Event.Message, "[REDACTED:LOCAL:") {
		t.Fatalf("redaction not applied before acceptance: %+v", record.Event)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("receipt permissions=%o", info.Mode().Perm())
	}
}

func TestWebhookOutboxConfigChangeCannotReroutePendingPayload(t *testing.T) {
	var oldCalls, newCalls atomic.Int32
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		oldCalls.Add(1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(503)
	}))
	defer old.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { newCalls.Add(1); w.WriteHeader(204) }))
	defer other.Close()
	dir := t.TempDir()
	first := startOutboxManager(t, dir, outboxTestWebhook(old.URL))
	if err := first.Dispatch(Event{ID: "bound", Type: "agent.error", Message: "private project payload"}); err != nil {
		t.Fatal(err)
	}
	awaitOutbox(t, func() bool { return first.pending.Load() == 0 })
	if err := first.Stop(); err != nil {
		t.Fatal(err)
	}
	_, before, data := readOnlyOutboxRecord(t, dir)
	second := startOutboxManager(t, dir, outboxTestWebhook(other.URL))
	time.Sleep(time.Until(before.NextRetry) + 50*time.Millisecond)
	second.feedOutbox()
	time.Sleep(100 * time.Millisecond)
	if newCalls.Load() != 0 || oldCalls.Load() != 1 {
		t.Fatal("changed configuration rerouted or replayed a pending payload")
	}
	_, _, after := readOnlyOutboxRecord(t, dir)
	if !bytes.Equal(data, after) {
		t.Fatal("configuration mismatch rewrote the retained receipt")
	}
}

func TestWebhookOutboxTransientAndPrivacyGateDoNotPersist(t *testing.T) {
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); w.WriteHeader(204) }))
	defer server.Close()
	m := startOutboxManager(t, t.TempDir(), outboxTestWebhook(server.URL))
	if err := m.Dispatch(Event{ID: "private", Type: "agent.error", Transient: true, Message: "not for disk"}); err != nil {
		t.Fatal(err)
	}
	awaitOutbox(t, func() bool { return m.pending.Load() == 0 })
	paths, _ := filepath.Glob(filepath.Join(m.outbox.dir, "*.json"))
	if len(paths) != 0 {
		t.Fatal("transient event persisted")
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	m = NewManager(ManagerConfig{OutboxDir: filepath.Join(t.TempDir(), "private"), PersistenceAllowed: func(string) bool { return false }})
	if err := m.Register(outboxTestWebhook(server.URL)); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	if err := m.Dispatch(Event{ID: "policy-private", Session: "private", Type: "agent.error", Message: "not for disk"}); err != nil {
		t.Fatal(err)
	}
	awaitOutbox(t, func() bool { return m.pending.Load() == 0 })
	paths, _ = filepath.Glob(filepath.Join(m.outbox.dir, "*.json"))
	if len(paths) != 0 || received.Load() != 2 {
		t.Fatalf("privacy fallback failed: receipts=%v deliveries=%d", paths, received.Load())
	}
}

func TestWebhookOutboxPermanentFailuresSurviveRestartWithoutReplay(t *testing.T) {
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); w.WriteHeader(400) }))
	defer server.Close()
	dir := t.TempDir()
	m := startOutboxManager(t, dir, outboxTestWebhook(server.URL))
	if err := m.Dispatch(Event{ID: "failed", Type: "agent.error", Message: "inspect me"}); err != nil {
		t.Fatal(err)
	}
	awaitOutbox(t, func() bool { return m.pending.Load() == 0 })
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	_, record, data := readOnlyOutboxRecord(t, dir)
	if !record.Failed || record.LastAttempt == nil || record.LastAttempt.StatusCode != 400 {
		t.Fatalf("permanent failure not retained: %+v", record)
	}
	second := startOutboxManager(t, dir, outboxTestWebhook(server.URL))
	second.feedOutbox()
	time.Sleep(100 * time.Millisecond)
	_, _, after := readOnlyOutboxRecord(t, dir)
	if received.Load() != 1 || !bytes.Equal(data, after) {
		t.Fatal("terminal delivery was replayed or erased on restart")
	}
}

func TestWebhookOutboxRejectsCorruptReceiptsAndStorageFailure(t *testing.T) {
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); w.WriteHeader(204) }))
	defer server.Close()
	dir := t.TempDir()
	m := startOutboxManager(t, dir, outboxTestWebhook(server.URL))
	path := filepath.Join(dir, outboxKey("corrupt_hook"))
	bad := []byte(`{"version":999,"partial":`)
	if err := os.WriteFile(path, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Dispatch(Event{ID: "corrupt", Type: "agent.error", Message: "new"}); err == nil {
		t.Fatal("corrupt receipt was overwritten")
	}
	m.feedOutbox()
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, bad) {
		t.Fatalf("corrupt record changed: %q %v", data, err)
	}
	// Replace only this test's directory path with a regular file. Every write
	// must now fail visibly, without falling back to unjournaled HTTP delivery.
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	backup := dir + "-preserved"
	if err := os.Rename(dir, backup); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(dir); _ = os.Rename(backup, dir) }()
	if err := os.WriteFile(dir, []byte("storage unavailable"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err == nil {
		t.Fatal("manager accepted an unusable outbox")
	}
	if received.Load() != 0 {
		t.Fatal("unpersisted or corrupt work was sent")
	}
}

func TestWebhookOutboxEmptyTemplateAndPrivatePathValidation(t *testing.T) {
	bodies := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies <- b
		w.WriteHeader(204)
	}))
	defer server.Close()
	wh := outboxTestWebhook(server.URL)
	wh.Template = "{{/* intentionally empty */}}"
	m := startOutboxManager(t, t.TempDir(), wh)
	if err := m.Dispatch(Event{ID: "empty", Type: "agent.idle"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-bodies:
		if len(body) != 0 {
			t.Fatalf("empty frozen template became %q", body)
		}
	default:
		t.Fatal("empty payload was not delivered")
	}
	insecure := t.TempDir()
	if err := os.Chmod(insecure, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := newDeliveryOutbox(insecure, 10); err == nil {
		t.Fatal("insecure receipt directory accepted")
	}
}

func TestWebhookOutboxProjectNamespacesAndSymlinkLockRejection(t *testing.T) {
	root := t.TempDir()
	a := t.TempDir()
	b := t.TempDir()
	t.Setenv("NTM_WEBHOOK_OUTBOX_DIR", root)
	pa, err := projectWebhookOutboxDir(a)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := projectWebhookOutboxDir(b)
	if err != nil {
		t.Fatal(err)
	}
	if pa == pb || filepath.Dir(pa) != root {
		t.Fatal("shared outbox root did not isolate projects")
	}
	alias := filepath.Join(t.TempDir(), "project")
	if err := os.Symlink(a, alias); err != nil {
		t.Fatal(err)
	}
	aliasPath, err := projectWebhookOutboxDir(alias)
	if err != nil || aliasPath != pa {
		t.Fatalf("project alias has different namespace: %s %v", aliasPath, err)
	}
	store, err := newDeliveryOutbox(filepath.Join(t.TempDir(), "private"), 10)
	if err != nil {
		t.Fatal(err)
	}
	key := outboxKey("link")
	prefix, _ := hex.DecodeString(key[:2])
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(store.dir, fmt.Sprintf("shard-%02x.lock", prefix[0]&63))); err != nil {
		t.Fatal(err)
	}
	if lock, err := store.lockKey(key); err == nil {
		_ = lock.Close()
		t.Fatal("followed an outbox lock symlink")
	}
	data, _ := os.ReadFile(victim)
	if string(data) != "untouched" {
		t.Fatal("changed lock symlink target")
	}
	if _, err := store.read("../victim"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatal("outbox path traversal accepted")
	}
}

func TestWebhookOutboxCustomHeadersCannotReplaceDeliveryIdentity(t *testing.T) {
	requests := make(chan outboxRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests <- captureOutboxRequest(r); w.WriteHeader(204) }))
	defer server.Close()
	wh := outboxTestWebhook(server.URL)
	wh.Headers["X-NTM-Delivery-ID"] = "overridden"
	wh.Headers["X-NTM-Attempt"] = "999"
	m := startOutboxManager(t, t.TempDir(), wh)
	if err := m.Dispatch(Event{ID: "canonical", Type: "agent.idle", Message: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-requests:
		if r.id != "canonical_hook" || r.attempt != "1" {
			t.Fatalf("custom headers replaced delivery identity: %+v", r)
		}
	default:
		t.Fatal("request not delivered")
	}
}

func TestWebhookOutboxReplayRechecksPrivacyPolicy(t *testing.T) {
	var received atomic.Int32
	var allowed atomic.Bool
	allowed.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if received.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	m := NewManager(ManagerConfig{OutboxDir: filepath.Join(t.TempDir(), "outbox"), WorkerCount: 1, PersistenceAllowed: func(string) bool { return allowed.Load() }})
	if err := m.Register(outboxTestWebhook(server.URL)); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	if err := m.Dispatch(Event{ID: "policy-changed", Session: "s", Type: "agent.error", Message: "retained"}); err != nil {
		t.Fatal(err)
	}
	awaitOutbox(t, func() bool { return m.pending.Load() == 0 })
	allowed.Store(false)
	_, record, data := readOnlyOutboxRecord(t, m.outbox.dir)
	time.Sleep(time.Until(record.NextRetry) + 25*time.Millisecond)
	m.feedOutbox()
	if received.Load() != 1 {
		t.Fatal("replayed a receipt after persistence permission was revoked")
	}
	_, _, after := readOnlyOutboxRecord(t, m.outbox.dir)
	if !bytes.Equal(data, after) {
		t.Fatal("updated a receipt while persistence was forbidden")
	}
	allowed.Store(true)
	m.feedOutbox()
	awaitOutbox(t, func() bool { return received.Load() == 2 })
}

func TestWebhookOutboxDirectoryReplacementFencesLiveManager(t *testing.T) {
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); w.WriteHeader(204) }))
	defer server.Close()
	dir := t.TempDir()
	m := startOutboxManager(t, dir, outboxTestWebhook(server.URL))
	preserved := dir + "-original"
	if err := os.Rename(dir, preserved); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Stop(); _ = os.Remove(dir); _ = os.Rename(preserved, dir) }()
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.Dispatch(Event{ID: "replacement", Type: "agent.error", Message: "must not enter replacement"}); err == nil {
		t.Fatal("manager adopted an unrelated replacement outbox")
	}
	if received.Load() != 0 {
		t.Fatal("dispatched without the original durable store")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("mutated the replacement directory")
	}
}

func TestWebhookOutboxRechecksRegistrationBeforeQueuedSend(t *testing.T) {
	requests := make(chan outboxRequest, 4)
	release := make(chan struct{})
	var unblock sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := captureOutboxRequest(r)
		requests <- req
		if req.id == "first_hook" {
			<-release
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	defer unblock.Do(func() { close(release) })
	wh := outboxTestWebhook(server.URL)
	m := startOutboxManager(t, t.TempDir(), wh)
	if err := m.Dispatch(Event{ID: "first", Type: "agent.error"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("first request never started")
	}
	if err := m.Dispatch(Event{ID: "second", Type: "agent.error"}); err != nil {
		t.Fatal(err)
	}
	wh.Enabled = false
	if err := m.Register(wh); err != nil {
		t.Fatal(err)
	}
	unblock.Do(func() { close(release) })
	awaitOutbox(t, func() bool { return m.pending.Load() == 0 })
	select {
	case req := <-requests:
		t.Fatalf("queued event used a disabled endpoint: %+v", req)
	default:
	}
	_, record, _ := readOnlyOutboxRecord(t, m.outbox.dir)
	if record.ID != "second_hook" || record.Attempt != 0 || record.Failed {
		t.Fatalf("undispatched event not retained: %+v", record)
	}
	wh.Enabled = true
	if err := m.Register(wh); err != nil {
		t.Fatal(err)
	}
	m.feedOutbox()
	select {
	case req := <-requests:
		if req.id != "second_hook" || req.attempt != "1" {
			t.Fatalf("wrong resumed delivery: %+v", req)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("matching restored endpoint did not recover its event")
	}
}

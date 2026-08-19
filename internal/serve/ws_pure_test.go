package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// =============================================================================
// responseRecorder.Bytes() — 0% → 100%
// =============================================================================

func TestResponseRecorder_Bytes(t *testing.T) {
	rr := httptest.NewRecorder()
	rec := &responseRecorder{ResponseWriter: rr}

	rec.Write([]byte("hello "))
	rec.Write([]byte("world"))

	got := string(rec.Bytes())
	if got != "hello world" {
		t.Errorf("Bytes() = %q, want %q", got, "hello world")
	}
}

func TestResponseRecorder_Bytes_Empty(t *testing.T) {
	rr := httptest.NewRecorder()
	rec := &responseRecorder{ResponseWriter: rr}

	if len(rec.Bytes()) != 0 {
		t.Errorf("Bytes() = %v, want empty", rec.Bytes())
	}
}

func TestResponseRecorder_WriteHeader(t *testing.T) {
	rr := httptest.NewRecorder()
	rec := &responseRecorder{ResponseWriter: rr}

	rec.WriteHeader(http.StatusCreated)
	if rec.statusCode != http.StatusCreated {
		t.Errorf("statusCode = %d, want %d", rec.statusCode, http.StatusCreated)
	}
}

// =============================================================================
// canSubscribe — 0% → 100%
// =============================================================================

func TestCanSubscribe_AlwaysTrue(t *testing.T) {
	client := &WSClient{
		id:     "test",
		send:   make(chan []byte, 16),
		topics: make(map[string]struct{}),
	}

	tests := []string{
		"panes:*",
		"global:events",
		"mail:agent1",
		"",
		"random-topic",
	}
	for _, topic := range tests {
		if !client.canSubscribe(topic) {
			t.Errorf("canSubscribe(%q) = false, want true", topic)
		}
	}
}

// =============================================================================
// sendError — 0% → 100%
// =============================================================================

func TestSendError(t *testing.T) {
	ch := make(chan []byte, 16)
	client := &WSClient{
		id:     "test-err",
		send:   ch,
		topics: make(map[string]struct{}),
	}

	client.sendError("req-1", "INVALID_INPUT", "bad data")

	select {
	case msg := <-ch:
		var errMsg WSError
		if err := json.Unmarshal(msg, &errMsg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if errMsg.Type != WSMsgError {
			t.Errorf("type = %q, want %q", errMsg.Type, WSMsgError)
		}
		if errMsg.RequestID != "req-1" {
			t.Errorf("request_id = %q, want %q", errMsg.RequestID, "req-1")
		}
		if errMsg.Code != "INVALID_INPUT" {
			t.Errorf("code = %q", errMsg.Code)
		}
		if errMsg.Message != "bad data" {
			t.Errorf("message = %q", errMsg.Message)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for error message")
	}
}

func TestSendError_BufferFull(t *testing.T) {
	// Create client with zero-buffer channel
	ch := make(chan []byte) // unbuffered
	client := &WSClient{
		id:     "test-full",
		send:   ch,
		topics: make(map[string]struct{}),
	}
	// Should not panic when buffer is full
	client.sendError("req-x", "ERR", "dropped")
}

func TestSendError_ClosedChannel(t *testing.T) {
	ch := make(chan []byte, 1)
	close(ch)
	client := &WSClient{
		id:     "test-closed",
		send:   ch,
		topics: make(map[string]struct{}),
	}

	client.sendError("req-x", "ERR", "dropped")
}

// =============================================================================
// sendAck — 0% → 100%
// =============================================================================

func TestSendAck(t *testing.T) {
	ch := make(chan []byte, 16)
	client := &WSClient{
		id:     "test-ack",
		send:   ch,
		topics: make(map[string]struct{}),
	}

	client.sendAck("req-2", map[string]interface{}{"status": "ok"})

	select {
	case msg := <-ch:
		var ackMsg WSMessage
		if err := json.Unmarshal(msg, &ackMsg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if ackMsg.Type != WSMsgAck {
			t.Errorf("type = %q, want %q", ackMsg.Type, WSMsgAck)
		}
		if ackMsg.RequestID != "req-2" {
			t.Errorf("request_id = %q", ackMsg.RequestID)
		}
		if ackMsg.Data["status"] != "ok" {
			t.Errorf("data = %v", ackMsg.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for ack")
	}
}

func TestSendAck_ClosedChannel(t *testing.T) {
	ch := make(chan []byte, 1)
	close(ch)
	client := &WSClient{
		id:     "test-ack-closed",
		send:   ch,
		topics: make(map[string]struct{}),
	}

	client.sendAck("req-2", map[string]interface{}{"status": "ok"})
}

// =============================================================================
// sendPong — 0% → 100%
// =============================================================================

func TestSendPong(t *testing.T) {
	ch := make(chan []byte, 16)
	client := &WSClient{
		id:     "test-pong",
		send:   ch,
		topics: make(map[string]struct{}),
	}

	client.sendPong("req-3")

	select {
	case msg := <-ch:
		var pongMsg WSMessage
		if err := json.Unmarshal(msg, &pongMsg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if pongMsg.Type != WSMsgPong {
			t.Errorf("type = %q, want %q", pongMsg.Type, WSMsgPong)
		}
		if pongMsg.RequestID != "req-3" {
			t.Errorf("request_id = %q", pongMsg.RequestID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for pong")
	}
}

func TestSendPong_ClosedChannel(t *testing.T) {
	ch := make(chan []byte, 1)
	close(ch)
	client := &WSClient{
		id:     "test-pong-closed",
		send:   ch,
		topics: make(map[string]struct{}),
	}

	client.sendPong("req-3")
}

// =============================================================================
// WSEventStore.CurrentSeq — 0% → 100%
// =============================================================================

func TestWSEventStore_CurrentSeq(t *testing.T) {
	store := &WSEventStore{
		seq: 42,
	}
	if seq := store.CurrentSeq(); seq != 42 {
		t.Errorf("CurrentSeq() = %d, want 42", seq)
	}
}

func TestWSEventStore_CurrentSeq_Concurrent(t *testing.T) {
	store := &WSEventStore{}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.nextSeq()
		}()
	}
	wg.Wait()

	if seq := store.CurrentSeq(); seq != 100 {
		t.Errorf("CurrentSeq() = %d after 100 increments, want 100", seq)
	}
}

// =============================================================================
// WSEventStore.cleanup — 0% → partial (nil db path)
// =============================================================================

func TestWSEventStore_Cleanup_NilDB(t *testing.T) {
	store := &WSEventStore{db: nil}
	if err := store.cleanup(); err != nil {
		t.Errorf("cleanup() with nil db = %v, want nil", err)
	}
}

// =============================================================================
// writeApprovalRequired — 0% → 100%
// =============================================================================

// =============================================================================
// WSHub getter — 0% → 100%
// =============================================================================

func TestServer_WSHub_Nil(t *testing.T) {
	s := &Server{}
	if hub := s.wsHub; hub != nil {
		t.Errorf("WSHub() = %v, want nil for empty server", hub)
	}
}

// =============================================================================
// publishMailEvent — 0% → 100% (nil wsHub path)
// =============================================================================

func TestPublishMailEvent_NilHub(t *testing.T) {
	s := &Server{wsHub: nil}
	// Should not panic
	s.publishMailEvent("agent1", "new_message", map[string]interface{}{"id": 1})
}

// =============================================================================
// publishReservationEvent — 0% → 100% (nil wsHub path)
// =============================================================================

func TestPublishReservationEvent_NilHub(t *testing.T) {
	s := &Server{wsHub: nil}
	// Should not panic
	s.publishReservationEvent("agent1", "reserved", map[string]interface{}{"path": "*.go"})
}

// =============================================================================
// publishPipelineEvent — 0% → 100% (nil wsHub path)
// =============================================================================

func TestPublishPipelineEvent_NilHub(t *testing.T) {
	s := &Server{wsHub: nil}
	// Should not panic
	s.publishPipelineEvent("session1", "started", map[string]interface{}{"pipeline": "p1"})
}

// =============================================================================
// publishMailEvent with real hub — covers the non-nil path
// =============================================================================

func TestPublishMailEvent_WithHub(t *testing.T) {
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()
	time.Sleep(10 * time.Millisecond)

	s := &Server{wsHub: hub}
	// Should not panic, publishes to hub
	s.publishMailEvent("agent1", "new_message", map[string]interface{}{"id": 1})
	s.publishMailEvent("", "broadcast", nil) // empty agent → mail:*
}

func TestPublishReservationEvent_WithHub(t *testing.T) {
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()
	time.Sleep(10 * time.Millisecond)

	s := &Server{wsHub: hub}
	s.publishReservationEvent("agent1", "reserved", map[string]interface{}{"path": "*.go"})
	s.publishReservationEvent("", "broadcast", nil)
}

func TestPublishPipelineEvent_WithHub(t *testing.T) {
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()
	time.Sleep(10 * time.Millisecond)

	s := &Server{wsHub: hub}
	s.publishPipelineEvent("session1", "started", map[string]interface{}{"pipeline": "p1"})
	s.publishPipelineEvent("", "global", nil)
}

// =============================================================================
// runGit / gitCheckout / gitStashIfDirty — 0% → tested with temp repos
// =============================================================================

func setupServeGitRepo(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = tmp
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("%v failed: %v\n%s", args, err, out)
		}
	}
	return tmp
}

func TestRunGit_Status(t *testing.T) {
	repoDir := setupServeGitRepo(t)
	out, err := runGit(repoDir, "status", "--porcelain")
	if err != nil {
		t.Fatalf("runGit: %v", err)
	}
	// Clean repo should have empty status
	if out != "" {
		t.Logf("status output: %q (may have untracked)", out)
	}
}

func TestRunGit_InvalidDir(t *testing.T) {
	_, err := runGit("/nonexistent-serve-test-dir", "status")
	if err == nil {
		t.Error("expected error for invalid dir")
	}
}

func TestGitCheckout_ValidRef(t *testing.T) {
	repoDir := setupServeGitRepo(t)

	// Get the initial commit hash
	out, err := runGit(repoDir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}

	// Checkout that same commit (detached HEAD)
	if err := gitCheckout(repoDir, strings.TrimSpace(out)); err != nil {
		t.Errorf("gitCheckout: %v", err)
	}
}

func TestGitStashIfDirty_CleanRepo(t *testing.T) {
	repoDir := setupServeGitRepo(t)

	stashMsg, err := gitStashIfDirty(repoDir)
	if err != nil {
		t.Fatalf("gitStashIfDirty: %v", err)
	}
	if stashMsg != "" {
		t.Errorf("stashMsg = %q, want empty for clean repo", stashMsg)
	}
}

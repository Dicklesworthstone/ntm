//go:build !windows

package webhook

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/privacy"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

// Exercise the actual project YAML + event-bus surface, not just a manager
// constructed with its internal persistence option. No second event is
// published after restart: recovery must come from the accepted receipt.
func TestWebhookOutboxProjectBridgeRecoversAcceptedEvent(t *testing.T) {
	t.Setenv("NTM_WEBHOOK_OUTBOX_DIR", filepath.Join(t.TempDir(), "receipts"))
	projectDir := t.TempDir()
	var recovered atomic.Bool
	requests := make(chan outboxRequest, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- captureOutboxRequest(r)
		if !recovered.Load() {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	configuration := "webhooks:\n  - name: durable\n    url: " + server.URL + "\n    events: [agent.error]\n    formatter: json\n    retry:\n      max_attempts: 3\n"
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm.yaml"), []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	bus := events.NewEventBus(10)
	bridge, err := StartBridgeFromProjectConfig(projectDir, "outbox-project-restart", bus, &redaction.Config{Mode: redaction.ModeOff})
	if err != nil || bridge == nil {
		t.Fatalf("open project bridge: %v", err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	bus.PublishSync(events.NewWebhookEvent(events.WebhookAgentError, "outbox-project-restart", "%1", "codex", "recover me", nil))
	var first outboxRequest
	select {
	case first = <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("project event was not dispatched")
	}
	if err := bridge.Close(); err != nil {
		t.Fatal(err)
	}
	recovered.Store(true)
	second, err := StartBridgeFromProjectConfig(projectDir, "outbox-project-restart", events.NewEventBus(10), &redaction.Config{Mode: redaction.ModeOff})
	if err != nil || second == nil {
		t.Fatalf("restart project bridge: %v", err)
	}
	defer second.Close()
	select {
	case next := <-requests:
		if first.id != next.id || next.attempt != "2" || !bytes.Equal(first.body, next.body) {
			t.Fatalf("project bridge changed recovered request: first=%+v next=%+v", first, next)
		}
		if next.at.Sub(first.at) < time.Second {
			t.Fatal("project restart shortened receiver cooldown")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("project bridge lost its accepted receipt on restart")
	}
}

func TestWebhookOutboxProjectBridgeRespectsSessionPrivacy(t *testing.T) {
	t.Setenv("NTM_WEBHOOK_OUTBOX_DIR", filepath.Join(t.TempDir(), "receipts"))
	const session = "outbox-project-private"
	privacy.GetDefaultManager().RegisterSession(session, true, false)
	defer privacy.GetDefaultManager().RegisterSession(session, false, false)
	requests := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	projectDir := t.TempDir()
	configuration := "webhooks:\n  - name: private\n    url: " + server.URL + "\n    events: [agent.error]\n"
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm.yaml"), []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	bus := events.NewEventBus(10)
	bridge, err := StartBridgeFromProjectConfig(projectDir, session, bus, &redaction.Config{Mode: redaction.ModeOff})
	if err != nil || bridge == nil {
		t.Fatalf("private project bridge: %v", err)
	}
	defer bridge.Close()
	bus.PublishSync(events.NewWebhookEvent(events.WebhookAgentError, session, "", "", "not for disk", nil))
	select {
	case <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("private event lost existing volatile delivery")
	}
	if err := bridge.Close(); err != nil {
		t.Fatal(err)
	}
	dir, err := projectWebhookOutboxDir(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(receipts) != 0 {
		t.Fatalf("private event was persisted: receipts=%v err=%v", receipts, err)
	}
}

//go:build !windows

package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/webhook"
)

// Exercise the public command, the real project YAML loader, and the existing
// manager together. No replacement sender or configuration parser is used.
func TestWebhookOutboxCommandInspectsAndAuthorizesRecovery(t *testing.T) {
	oldConfig := cfg
	cfg = config.Default()
	t.Cleanup(func() { cfg = oldConfig })
	project := t.TempDir()
	t.Setenv("NTM_WEBHOOK_OUTBOX_DIR", filepath.Join(t.TempDir(), "outbox"))
	var received atomic.Int32
	var repaired atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if repaired.Load() {
			w.WriteHeader(http.StatusNoContent)
		} else {
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer server.Close()
	if err := os.WriteFile(filepath.Join(project, ".ntm.yaml"), []byte("webhooks:\n  - name: recovery\n    url: "+server.URL+"\n    events: [agent.error]\n    formatter: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	redactCfg := cfg.Redaction.ToRedactionLibConfig()
	m, err := webhook.NewProjectWebhookManager(project, &redactCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop() })
	if err := m.Dispatch(webhook.Event{ID: "cli-recovery", Type: "agent.error", Message: "confidential-body"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot, err := m.ListOutbox()
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Entries) == 1 && snapshot.Entries[0].State == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("receiver failure was not retained")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	execute := func(args ...string) ([]byte, error) {
		command := newWebhookOutboxCmd()
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetErr(&output)
		command.SetArgs(args)
		err := command.Execute()
		return output.Bytes(), err
	}
	data, err := execute("list", "--project", project)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot webhook.OutboxSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil || len(snapshot.Entries) != 1 {
		t.Fatalf("list JSON = %s, %v", data, err)
	}
	if bytes.Contains(data, []byte("confidential-body")) || bytes.Contains(data, []byte(server.URL)) {
		t.Fatal("list exposed payload or endpoint")
	}
	entry := snapshot.Entries[0]
	if _, err := execute("retry", entry.Record, "--project", project); err == nil {
		t.Fatal("retry accepted without inspection revision")
	}
	data, err = execute("retry", entry.Record, "--revision", entry.Revision, "--project", project)
	if err != nil {
		t.Fatalf("retry: %v\n%s", err, data)
	}
	if _, err := execute("retry", entry.Record, "--revision", entry.Revision, "--project", project); err == nil {
		t.Fatal("stale retry authorization was accepted")
	}
	if got := received.Load(); got != 1 {
		t.Fatalf("administration started HTTP workers: %d requests", got)
	}
	repaired.Store(true)
	next, err := webhook.NewProjectWebhookManager(project, &redactCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Stop() })
	deadline = time.Now().Add(5 * time.Second)
	for received.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := next.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := received.Load(); got != 2 {
		t.Fatalf("authorized receipt did not resume through project configuration: requests=%d", got)
	}
}

func TestWebhookOutboxCommandIsRegistered(t *testing.T) {
	for _, command := range rootCmd.Commands() {
		if command.Name() == "webhook-outbox" {
			return
		}
	}
	t.Fatal("webhook-outbox is not reachable from ntm")
}

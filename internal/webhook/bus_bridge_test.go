package webhook

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

func TestBusBridge_DispatchesWebhookEvents(t *testing.T) {
	t.Parallel()

	recv := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		select {
		case recv <- payload:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm.yaml"), []byte(`
webhooks:
  - name: test
    url: `+srv.URL+`
    events: ["agent.error"]
    formatter: json
`), 0o644); err != nil {
		t.Fatalf("write .ntm.yaml: %v", err)
	}

	bus := events.NewEventBus(10)
	bridge, err := StartBridgeFromProjectConfig(projectDir, "mysession", bus, &redaction.Config{Mode: redaction.ModeOff})
	if err != nil {
		t.Fatalf("StartBridgeFromProjectConfig: %v", err)
	}
	if bridge == nil {
		t.Fatalf("expected bridge, got nil")
	}
	t.Cleanup(func() { _ = bridge.Close() })

	bus.PublishSync(events.NewWebhookEvent(
		events.WebhookAgentError,
		"mysession",
		"%1",
		"codex",
		"boom",
		map[string]string{"k": "v"},
	))

	select {
	case payload := <-recv:
		if payload["type"] != "agent.error" {
			t.Fatalf("type=%v, want agent.error", payload["type"])
		}
		if payload["session"] != "mysession" {
			t.Fatalf("session=%v, want mysession", payload["session"])
		}
		if payload["message"] != "boom" {
			t.Fatalf("message=%v, want boom", payload["message"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for webhook delivery")
	}
}

// =============================================================================
// toWebhookEvent — all branches (bd-1ced7)
// =============================================================================

func TestToWebhookEvent_Nil(t *testing.T) {
	t.Parallel()
	_, ok := toWebhookEvent(nil)
	if ok {
		t.Error("toWebhookEvent(nil) should return false")
	}
}

func TestToWebhookEvent_WebhookEventValue(t *testing.T) {
	t.Parallel()
	we := events.NewWebhookEvent("agent.error", "mysession", "%1", "claude", "boom", map[string]string{"k": "v"})
	got, ok := toWebhookEvent(we)
	if !ok {
		t.Fatal("toWebhookEvent(WebhookEvent) should return true")
	}
	if got.Type != "agent.error" {
		t.Errorf("Type = %q, want 'agent.error'", got.Type)
	}
	if got.Session != "mysession" {
		t.Errorf("Session = %q, want 'mysession'", got.Session)
	}
	if got.Pane != "%1" {
		t.Errorf("Pane = %q, want '%%1'", got.Pane)
	}
	if got.Agent != "claude" {
		t.Errorf("Agent = %q, want 'claude'", got.Agent)
	}
	if got.Message != "boom" {
		t.Errorf("Message = %q, want 'boom'", got.Message)
	}
	if got.Details["k"] != "v" {
		t.Errorf("Details[k] = %q, want 'v'", got.Details["k"])
	}
}

func TestToWebhookEvent_WebhookEventPointer(t *testing.T) {
	t.Parallel()
	we := events.NewWebhookEvent("agent.completed", "s", "%0", "codex", "done", nil)
	got, ok := toWebhookEvent(&we)
	if !ok {
		t.Fatal("toWebhookEvent(*WebhookEvent) should return true")
	}
	if got.Type != "agent.completed" {
		t.Errorf("Type = %q, want 'agent.completed'", got.Type)
	}
}

func TestToWebhookEvent_NilPointer(t *testing.T) {
	t.Parallel()
	var we *events.WebhookEvent
	_, ok := toWebhookEvent(we)
	if ok {
		t.Error("toWebhookEvent(nil *WebhookEvent) should return false")
	}
}

// =============================================================================
// stableWebhookID — all branches (bd-4b4zf)
// =============================================================================

func TestStableWebhookID_AllBranches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
		{"simple lowercase", "slack", "wh_slack"},
		{"uppercase", "SLACK", "wh_slack"},
		{"with spaces", "  My Hook  ", "wh_my_hook"},
		{"with digits", "hook123", "wh_hook123"},
		{"special chars replaced", "hook!@#$%", "wh_hook_____"},
		{"mixed", "My-Hook.v2", "wh_my_hook_v2"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := stableWebhookID(tc.input)
			if got != tc.want {
				t.Errorf("stableWebhookID(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// =============================================================================
// trimStrings — all branches (bd-4b4zf)
// =============================================================================

func TestTrimStrings_AllBranches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, nil},
		{"empty slice", []string{}, nil},
		{"all non-empty", []string{"a", "b"}, []string{"a", "b"}},
		{"with whitespace", []string{"  a  ", " b "}, []string{"a", "b"}},
		{"skip empty strings", []string{"a", "", "b"}, []string{"a", "b"}},
		{"skip whitespace-only", []string{"  ", "a", "\t"}, []string{"a"}},
		{"all empty", []string{"", "  ", "\t"}, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := trimStrings(tc.in)
			// nil and empty slice should both be nil
			if tc.want == nil {
				if len(got) != 0 {
					t.Errorf("trimStrings(%v) = %v, want nil", tc.in, got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("trimStrings(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

//go:build !windows

package webhook

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

type filterBridgeProviderEvent struct {
	events.BaseEvent
	Agent     string `json:"agent"`
	AgentType string `json:"agent_type"`
	Message   string `json:"message"`
}

// Exercise the existing project YAML -> event bus -> HTTP user path, rather
// than just constructing a manager's filter directly.
func TestProjectWebhookFiltersReachDelivery(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(fmt.Sprintf("durable=%v", durable), func(t *testing.T) {
			t.Setenv("NTM_WEBHOOK_OUTBOX_DIR", "")
			if durable {
				t.Setenv("NTM_WEBHOOK_OUTBOX_DIR", t.TempDir())
			}
			var mu sync.Mutex
			var delivered []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var event Event
				if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
					t.Error(err)
				}
				mu.Lock()
				delivered = append(delivered, event.Message)
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()
			project := t.TempDir()
			data := fmt.Sprintf(`webhooks:
  - name: restricted
    url: %s
    events: [agent.error, agent.started]
    filter:
      session: prod-*
      agent_type: [OpenAI, codex]
      severity: [error]
`, srv.URL)
			if err := os.WriteFile(filepath.Join(project, ".ntm.yaml"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			bus := events.NewEventBus(16)
			bridge, err := StartBridgeFromProjectConfig(project, "", bus, &redaction.Config{Mode: redaction.ModeOff})
			if err != nil || bridge == nil {
				t.Fatalf("start project bridge: bridge=%v err=%v", bridge, err)
			}
			defer bridge.Close()
			for _, event := range []events.WebhookEvent{
				events.NewWebhookEvent("agent.error", "prod-api", "%1", "BlueFox", "accepted-details", map[string]string{"agent_type": "codex"}),
				events.NewWebhookEvent("agent.error", "dev-api", "%1", "cod", "excluded-session", nil),
				events.NewWebhookEvent("agent.error", "prod-api", "%1", "gmi", "excluded-provider", nil),
				events.NewWebhookEvent("agent.started", "prod-api", "%1", "cod", "excluded-severity", nil),
			} {
				bus.PublishSync(event)
			}
			bus.PublishSync(filterBridgeProviderEvent{BaseEvent: events.BaseEvent{Type: "agent.error", Session: "prod-api"},
				Agent: "BlueFox", AgentType: "codex", Message: "accepted-explicit"})
			bus.PublishSync(filterBridgeProviderEvent{BaseEvent: events.BaseEvent{Type: "agent.error", Session: "prod-api"},
				Agent: "cod", AgentType: "unknown", Message: "excluded-unknown"})
			if err := bridge.Close(); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			sort.Strings(delivered)
			if !reflect.DeepEqual(delivered, []string{"accepted-details", "accepted-explicit"}) {
				t.Fatalf("project filter delivered %v", delivered)
			}
		})
	}
}

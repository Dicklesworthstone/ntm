package webhook

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
)

// This probe uses only the pre-existing public manager API and JSON decoding.
// It must fail unchanged against the implementation that discarded filters.
func TestWebhookFiltersPublicDispatch(t *testing.T) {
	cases := []struct {
		name   string
		filter string
		events []string
		want   []string
	}{
		{"session", `{"session":"prod-*"}`, []string{
			`{"id":"keep","session":"prod-api"}`, `{"id":"wrong","session":"dev-api"}`, `{"id":"missing"}`,
		}, []string{"keep"}},
		{"agent aliases", `{"agent_type":[" CLAUDE-Code ","openai"]}`, []string{
			`{"id":"cc","agent":"cc"}`, `{"id":"cod","agent":"codex"}`, `{"id":"other","agent":"gmi"}`,
			`{"id":"identity","agent":"BlueFox"}`, `{"id":"not-a-type","agent":"cc_1"}`,
			`{"id":"details","agent":"BlueFox","details":{"agent_type":"claude_code"}}`,
			`{"id":"explicit","agent":"BlueFox","agent_type":"codex-cli"}`,
			`{"id":"override","agent":"codex","agent_type":"gemini"}`,
			`{"id":"unknown","agent":"codex","agent_type":"unknown"}`,
			`{"id":"unknown-details","agent":"codex","details":{"agent_type":"unknown"}}`,
		}, []string{"cc", "cod", "details", "explicit"}},
		{"severity", `{"severity":[" WARN ","success"]}`, []string{
			`{"id":"limited","type":"agent.rate_limit"}`, `{"id":"degraded","type":"health.degraded"}`,
			`{"id":"complete","type":"agent.completed"}`, `{"id":"error","type":"agent.error"}`,
			`{"id":"info","type":"agent.started","details":{"severity":"success"}}`,
		}, []string{"limited", "degraded", "complete"}},
		{"intersection", `{"session":"prod-*","agent_type":["cod"],"severity":["error"]}`, []string{
			`{"id":"keep","type":"agent.crashed","session":"prod-api","agent":"OpenAI"}`,
			`{"id":"session","type":"agent.crashed","session":"dev-api","agent":"cod"}`,
			`{"id":"agent","type":"agent.crashed","session":"prod-api","agent":"cc"}`,
			`{"id":"severity","type":"agent.started","session":"prod-api","agent":"cod"}`,
		}, []string{"keep"}},
		{"empty", `{}`, []string{`{"id":"unrestricted"}`}, []string{"unrestricted"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var received []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var event Event
				if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
					t.Errorf("decode delivered event: %v", err)
				}
				mu.Lock()
				received = append(received, event.ID)
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()
			var cfg WebhookConfig
			if err := json.Unmarshal([]byte(`{"id":"filtered","enabled":true,"filter":`+tc.filter+`}`), &cfg); err != nil {
				t.Fatal(err)
			}
			cfg.URL = srv.URL
			manager := NewManager(ManagerConfig{WorkerCount: 1})
			if err := manager.Register(cfg); err != nil {
				t.Fatal(err)
			}
			if err := manager.Start(); err != nil {
				t.Fatal(err)
			}
			defer manager.Stop()
			for _, raw := range tc.events {
				var event Event
				if err := json.Unmarshal([]byte(raw), &event); err != nil {
					t.Fatal(err)
				}
				if err := manager.Dispatch(event); err != nil {
					t.Fatal(err)
				}
			}
			if err := manager.Stop(); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(received, tc.want) {
				t.Fatalf("delivered IDs = %v, want %v; filtered events reached the receiver", received, tc.want)
			}
		})
	}
}

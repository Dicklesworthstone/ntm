package webhook

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestWebhookFilterRegistrationSnapshotAndValidation(t *testing.T) {
	m := NewManager(DefaultManagerConfig())
	original := &EventFilter{Session: "prod-*", AgentType: []string{"codex", "CC", "openai"}, Severity: []string{"warn", "success", "WARNING"}}
	wh := WebhookConfig{ID: "hook", URL: "https://example.com/hook", Enabled: true, Filter: original}
	if err := m.Register(wh); err != nil {
		t.Fatal(err)
	}
	snapshot := m.webhooks[wh.ID]
	want := &EventFilter{Session: "prod-*", AgentType: []string{"cc", "cod"}, Severity: []string{"success", "warning"}}
	if !reflect.DeepEqual(snapshot.Filter, want) {
		t.Fatalf("normalized filter = %+v, want %+v", snapshot.Filter, want)
	}
	fingerprint := m.outboxFingerprint(snapshot)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			original.Session = "dev-*"
			original.AgentType[0] = "gmi"
			original.Severity[0] = "info"
		}
	}()
	for i := 0; i < 1000; i++ {
		if !m.matchesWebhook(snapshot, Event{Session: "prod-api", Agent: "cod", Type: "agent.completed"}) {
			t.Error("caller mutation altered registered policy")
			break
		}
	}
	wg.Wait()
	if m.outboxFingerprint(snapshot) != fingerprint {
		t.Fatal("caller mutation altered retained configuration")
	}

	for _, raw := range []string{
		`{"session":"["}`, `{"agent_type":[""]}`, `{"agent_type":["unknown"]}`,
		`{"agent_type":["user"]}`, `{"agent_type":["cc_1"]}`, `{"severity":[""]}`, `{"severity":["garbage"]}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var filter EventFilter
			if err := json.Unmarshal([]byte(raw), &filter); err != nil {
				t.Fatal(err)
			}
			wh.Filter = &filter
			if err := m.Register(wh); err == nil {
				t.Fatal("invalid restriction accepted")
			}
			if m.outboxFingerprint(m.webhooks[wh.ID]) != fingerprint {
				t.Fatal("failed registration replaced the current policy")
			}
		})
	}
}

func TestWebhookFilterFingerprintsPreserveUnfilteredReceipts(t *testing.T) {
	m := NewManager(DefaultManagerConfig())
	wh := WebhookConfig{ID: "legacy", URL: "https://example.com/hook", Enabled: true,
		Timeout: 10 * time.Second, Retry: RetryConfig{BaseDelay: time.Second, MaxDelay: 30 * time.Second}}
	// Recorded by running this same registration against the original manager
	// on a7efe57. A nil/empty filter must not strand its pending receipts.
	const legacy = "e87d8397e2c12ef307aecf12e0161ad734cf5aea3f03b2bf9904fb20f1d2b92e"
	for _, filter := range []*EventFilter{nil, {}, {Session: " "}} {
		wh.Filter = filter
		if err := m.Register(wh); err != nil {
			t.Fatal(err)
		}
		if got := m.outboxFingerprint(m.webhooks[wh.ID]); got != legacy {
			t.Fatalf("unfiltered fingerprint %s differs from original %s", got, legacy)
		}
	}
	wh.Filter = &EventFilter{AgentType: []string{"claude", "codex", "CC"}, Severity: []string{"warn", "error"}}
	if err := m.Register(wh); err != nil {
		t.Fatal(err)
	}
	first := m.outboxFingerprint(m.webhooks[wh.ID])
	wh.Filter = &EventFilter{AgentType: []string{"openai", "claude_code"}, Severity: []string{"error", "WARNING"}}
	if err := m.Register(wh); err != nil {
		t.Fatal(err)
	}
	if got := m.outboxFingerprint(m.webhooks[wh.ID]); got != first || got == legacy {
		t.Fatalf("equivalent restrictions must share a distinct fingerprint: %s vs %s", got, first)
	}
}

func TestWebhookFilterCombinesEventSubscriptionAndProviderMetadata(t *testing.T) {
	m := NewManager(DefaultManagerConfig())
	wh := WebhookConfig{ID: "hook", URL: "https://example.com/hook", Enabled: true, Events: []string{"agent.*"}, Filter: &EventFilter{AgentType: []string{"omp"}}}
	if err := m.Register(wh); err != nil {
		t.Fatal(err)
	}
	registered := m.webhooks[wh.ID]
	for _, tc := range []struct {
		event Event
		want  bool
	}{
		{Event{Type: "agent.completed", Agent: "Oh-My-Pi"}, true},
		{Event{Type: "session.ended", Agent: "omp"}, false},
		{Event{Type: "agent.completed", Agent: "omp", AgentType: "codex"}, false},
		{Event{Type: "agent.completed", Agent: "BlueFox", AgentType: "oh_my_pi"}, true},
		{Event{Type: "agent.completed", Agent: "BlueFox", Details: map[string]string{"agent_type": "ohmypi"}}, true},
	} {
		if got := m.matchesWebhook(registered, tc.event); got != tc.want {
			t.Errorf("matchesWebhook(%+v) = %v, want %v", tc.event, got, tc.want)
		}
	}
}

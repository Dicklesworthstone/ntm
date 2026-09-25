package handoff

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"gopkg.in/yaml.v3"
)

// Exercise capture through the existing public generator and real Agent Mail
// client, then the YAML representation used by persisted handoffs.
func TestGenerateHandoffPersistsSourceLeaseIdentity(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		var rpc struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string `json:"name"`
				URI  string `json:"uri"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
			t.Errorf("decode capture request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		var result any
		switch {
		case rpc.Method == "tools/call" && rpc.Params.Name == "fetch_inbox":
			result = []any{}
		case rpc.Method == "resources/read" && strings.HasPrefix(rpc.Params.URI, "resource://file_reservations/"):
			result = map[string]any{"contents": []map[string]any{{"text": `[{"id":91,"project_id":73,"agent_name":"BlueLake","path_pattern":"internal/**","exclusive":true,"reason":"implementation","created_ts":"2026-01-02T03:04:05.123456789Z","expires_ts":"2099-01-01T00:00:00Z"}]`}}}
		case rpc.Method == "resources/read" && strings.HasPrefix(rpc.Params.URI, "resource://project/"):
			result = map[string]any{"contents": []map[string]any{{"text": `{"id":73,"slug":"project","human_key":"/project"}`}}}
		default:
			t.Errorf("capture must be read-only: unexpected %s %s %s", rpc.Method, rpc.Params.Name, rpc.Params.URI)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": result}); err != nil {
			t.Errorf("encode capture response: %v", err)
		}
	}))
	defer server.Close()
	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL), agentmail.WithToken(""))
	no := false
	h, err := NewGenerator("").GenerateHandoff(context.Background(), GenerateHandoffOptions{
		SessionName: "lease-capture", AgentName: "BlueLake", AgentType: "cc", ProjectKey: "/project",
		Goal: "Preserve reservation identity", Now: "Continue with the captured source leases",
		IncludeBeads: &no, IncludeCASS: &no, AgentMailClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	var restored Handoff
	if err := yaml.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	transfer := restored.ReservationTransfer
	if transfer == nil || transfer.FromAgent != "BlueLake" || transfer.ProjectKey != "/project" || len(transfer.Reservations) != 1 {
		t.Fatalf("generated YAML lost transfer metadata: %s", data)
	}
	lease := transfer.Reservations[0]
	if lease.ID != 91 || lease.ProjectID != 73 || lease.AgentName != "BlueLake" || !lease.CreatedAt.Equal(created) ||
		lease.PathPattern != "internal/**" || !lease.Exclusive || lease.Reason != "implementation" || lease.ExpiresAt.Year() != 2099 {
		t.Fatalf("generated YAML lost captured lease identity: %+v", lease)
	}

	// Older context artifacts must still load; reading them does not invent a
	// lease ID or ownership proof to authorize a later mutation.
	var legacy Handoff
	if err := yaml.Unmarshal([]byte("reservation_transfer:\n  from_agent: BlueLake\n  reservations:\n    - path_pattern: internal/**\n"), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.ReservationTransfer == nil || len(legacy.ReservationTransfer.Reservations) != 1 || legacy.ReservationTransfer.Reservations[0].ID != 0 {
		t.Fatalf("legacy context was rejected or assigned an invented lease: %+v", legacy.ReservationTransfer)
	}
}

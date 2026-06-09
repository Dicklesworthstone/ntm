package agentmail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadResource(t *testing.T) {
	// Mock JSON-RPC server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}

		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req.JSONRPC != "2.0" {
			t.Errorf("expected jsonrpc 2.0, got %s", req.JSONRPC)
		}
		if req.Method != "resources/read" {
			t.Errorf("expected method resources/read, got %s", req.Method)
		}

		params, ok := req.Params.(map[string]interface{})
		if !ok {
			t.Fatal("expected params to be a map")
		}
		if params["uri"] != "resource://test" {
			t.Errorf("expected uri resource://test, got %v", params["uri"])
		}

		// Return success response
		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(`{"contents": [{"uri": "resource://test", "mimeType": "application/json", "text": "{\"key\": \"value\"}"}]}`),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	result, err := c.ReadResource(context.Background(), "resource://test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resourceResp struct {
		Contents []struct {
			Text string `json:"text"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(result, &resourceResp); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if len(resourceResp.Contents) != 1 {
		t.Errorf("expected 1 content item, got %d", len(resourceResp.Contents))
	}

	if resourceResp.Contents[0].Text != `{"key": "value"}` {
		t.Errorf("unexpected text content: %s", resourceResp.Contents[0].Text)
	}
}

func TestListProjectAgents(t *testing.T) {
	// Mock JSON-RPC server for resources/read
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req.Method != "resources/read" {
			t.Errorf("expected method resources/read, got %s", req.Method)
		}

		// Return agents list
		agents := []Agent{
			{ID: 1, Name: "Agent1", Program: "prog1"},
			{ID: 2, Name: "Agent2", Program: "prog2"},
		}
		agentsJSON, _ := json.Marshal(agents)

		respContent := map[string]interface{}{
			"contents": []map[string]interface{}{
				{
					"uri":      "resource://agents/test-project",
					"mimeType": "application/json",
					"text":     string(agentsJSON),
				},
			},
		}
		contentJSON, _ := json.Marshal(respContent)

		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(contentJSON),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	agents, err := c.ListProjectAgents(context.Background(), "test-project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(agents) != 2 {
		t.Errorf("expected 2 agents, got %d", len(agents))
	}
	if agents[0].Name != "Agent1" {
		t.Errorf("expected agent 1 name 'Agent1', got %s", agents[0].Name)
	}
}

func TestListReservations_ResourcePathAndFiltering(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if req.Method != "resources/read" {
			t.Fatalf("expected method resources/read, got %s", req.Method)
		}
		params, ok := req.Params.(map[string]interface{})
		if !ok {
			t.Fatal("expected params to be a map")
		}
		uri, _ := params["uri"].(string)
		if !strings.HasPrefix(uri, "resource://file_reservations/") || !strings.Contains(uri, "active_only=true") {
			t.Fatalf("unexpected uri: %q", uri)
		}

		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: json.RawMessage(`{
				"contents": [
					{
						"uri": "resource://file_reservations/test",
						"mimeType": "application/json",
						"text": "[{\"id\":1,\"project_id\":777,\"agent\":\"BlueLake\",\"path_pattern\":\"internal/agentmail/*\",\"exclusive\":true,\"reason\":\"bd-mthe9\",\"created_ts\":\"2026-01-01T00:00:00Z\",\"expires_ts\":\"2099-01-01T01:00:00Z\"},{\"id\":2,\"agent_name\":\"RedStone\",\"path_pattern\":\"internal/tui/*\",\"exclusive\":false,\"reason\":\"bd-zzz\",\"created_ts\":\"2026-01-01T00:00:00Z\",\"expires_ts\":\"2099-01-01T01:00:00Z\"},{\"id\":3,\"agent\":\"BlueLake\",\"path_pattern\":\"expired/*\",\"exclusive\":true,\"reason\":\"old\",\"created_ts\":\"2026-01-01T00:00:00Z\",\"expires_ts\":\"2026-01-01T01:00:00Z\"},{\"id\":4,\"agent\":\"BlueLake\",\"path_pattern\":\"released/*\",\"exclusive\":true,\"reason\":\"released\",\"created_ts\":\"2026-01-01T00:00:00Z\",\"expires_ts\":\"2099-01-01T01:00:00Z\",\"released_ts\":\"2026-01-01T00:10:00Z\"}]"
					}
				]
			}`),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))

	filtered, err := c.ListReservations(context.Background(), "/test/project", "BlueLake", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(filtered) != 1 {
		t.Fatalf("expected 1 reservation after filtering, got %d", len(filtered))
	}
	if filtered[0].ID != 1 || filtered[0].AgentName != "BlueLake" || filtered[0].ProjectID != 777 {
		t.Fatalf("unexpected reservation: %+v", filtered[0])
	}
}

func TestListReservations_FallbackToToolsWhenResourceFails(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		switch req.Method {
		case "resources/read":
			resp := JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &JSONRPCError{
					Code:    -32000,
					Message: "resource view not supported",
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case "tools/call":
			params, ok := req.Params.(map[string]interface{})
			if !ok {
				t.Fatal("expected params to be a map")
			}
			if params["name"] != "list_file_reservations" {
				t.Fatalf("expected list_file_reservations, got %v", params["name"])
			}

			resp := JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: json.RawMessage(`[
					{"id":1,"path_pattern":"internal/agentmail/*","agent_name":"BlueLake","exclusive":true,"reason":"bd-mthe9","created_ts":"2026-01-01T00:00:00Z","expires_ts":"2099-01-01T01:00:00Z"},
					{"id":2,"path_pattern":"internal/tui/*","agent_name":"RedStone","exclusive":false,"reason":"bd-zzz","created_ts":"2026-01-01T00:00:00Z","expires_ts":"2099-01-01T01:00:00Z"},
					{"id":3,"path_pattern":"expired/*","agent_name":"BlueLake","exclusive":true,"reason":"old","created_ts":"2026-01-01T00:00:00Z","expires_ts":"2026-01-01T01:00:00Z"},
					{"id":4,"path_pattern":"released/*","agent_name":"BlueLake","exclusive":true,"reason":"released","created_ts":"2026-01-01T00:00:00Z","expires_ts":"2099-01-01T01:00:00Z","released_ts":"2026-01-01T00:10:00Z"}
				]`),
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		default:
			t.Fatalf("unexpected method %s", req.Method)
		}
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))

	filtered, err := c.ListReservations(context.Background(), "/test/project", "BlueLake", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(filtered) != 1 {
		t.Fatalf("expected 1 reservation after filtering, got %d", len(filtered))
	}
	if filtered[0].AgentName != "BlueLake" {
		t.Fatalf("unexpected agent name: %q", filtered[0].AgentName)
	}
	if filtered[0].PathPattern != "internal/agentmail/*" {
		t.Fatalf("unexpected path pattern: %q", filtered[0].PathPattern)
	}
}

func TestListReservations_FallbackToArchiveWhenResourceAndToolsFail(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectKey := "/Users/josh/Developer/skillos"
	archiveDir := filepath.Join(home, DefaultArchivePath, "projects", ProjectSlugFromPath(projectKey), "file_reservations")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		t.Fatalf("create archive dir: %v", err)
	}

	fixtures := map[string]string{
		"id-42.json": `{
			"id": 42,
			"agent": "BlueLake",
			"path_pattern": "internal/agentmail/*",
			"exclusive": true,
			"reason": "skillos-fr9is",
			"created_ts": "2026-01-01T00:00:00Z",
			"expires_ts": "2099-01-01T01:00:00Z"
		}`,
		"id-43.json": `{
			"id": 43,
			"agent": "RedStone",
			"path_pattern": "internal/tui/*",
			"exclusive": true,
			"created_ts": "2026-01-01T00:00:00Z",
			"expires_ts": "2099-01-01T01:00:00Z"
		}`,
		"id-44.json": `{
			"id": 44,
			"agent": "BlueLake",
			"path_pattern": "docs/*",
			"exclusive": true,
			"created_ts": "2026-01-01T00:00:00Z",
			"expires_ts": "2099-01-01T01:00:00Z",
			"released_ts": "2026-01-01T00:10:00Z"
		}`,
	}
	for name, body := range fixtures {
		if err := os.WriteFile(filepath.Join(archiveDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &JSONRPCError{Code: -32601, Message: "missing " + req.Method},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	filtered, err := c.ListReservations(context.Background(), projectKey, "BlueLake", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(filtered) != 1 {
		t.Fatalf("expected 1 active BlueLake reservation, got %d: %+v", len(filtered), filtered)
	}
	if filtered[0].ID != 42 || filtered[0].AgentName != "BlueLake" || filtered[0].Reason != "skillos-fr9is" {
		t.Fatalf("unexpected reservation: %+v", filtered[0])
	}
}

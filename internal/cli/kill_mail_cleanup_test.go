package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// fakeMailCall records one tools/call the fake Agent Mail server received.
type fakeMailCall struct {
	Tool string
	Args map[string]interface{}
}

// newFakeAgentMailServer returns an MCP JSON-RPC server (root endpoint, same
// contract as mcp-agent-mail) that records every tools/call and answers
// release_file_reservations and cleanup_pane_identities successfully.
func newFakeAgentMailServer(t *testing.T) (*httptest.Server, func() []fakeMailCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []fakeMailCall

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			JSONRPC string      `json:"jsonrpc"`
			ID      interface{} `json:"id"`
			Method  string      `json:"method"`
			Params  struct {
				Name      string                 `json:"name"`
				Arguments map[string]interface{} `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")

		if req.Method != "tools/call" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]interface{}{"code": -32601, "message": "unknown method"},
			})
			return
		}

		mu.Lock()
		calls = append(calls, fakeMailCall{Tool: req.Params.Name, Args: req.Params.Arguments})
		mu.Unlock()

		var result interface{}
		switch req.Params.Name {
		case "release_file_reservations":
			result = map[string]interface{}{"released": 2, "released_at": time.Now().UTC().Format(time.RFC3339)}
		case "cleanup_pane_identities":
			result = map[string]interface{}{"removed_count": 1, "removed_paths": []string{"/tmp/x"}}
		default:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]interface{}{"code": -32601, "message": "unknown tool: " + req.Params.Name},
			})
			return
		}
		raw, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(raw),
		})
	}))
	t.Cleanup(srv.Close)

	return srv, func() []fakeMailCall {
		mu.Lock()
		defer mu.Unlock()
		out := make([]fakeMailCall, len(calls))
		copy(out, calls)
		return out
	}
}

// writeKillTestRegistry persists a session agent registry with the given
// pane agents under the (test-isolated) sessions base dir.
func writeKillTestRegistry(t *testing.T, session, projectKey string, agents map[string]string) {
	t.Helper()
	registry := agentmail.NewSessionAgentRegistry(session, projectKey)
	i := 0
	for title, name := range agents {
		registry.AddAgent(title, "%"+string(rune('1'+i)), name)
		i++
	}
	if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
		t.Fatalf("SaveSessionAgentRegistry: %v", err)
	}
}

// TestCleanupAgentMailOnKill_ReleasesSessionAgents asserts that killing a
// session best-effort releases every registered pane agent's reservations and
// triggers server-side stale pane-identity cleanup (bd-1bdvy).
func TestCleanupAgentMailOnKill_ReleasesSessionAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")

	session := "killclean_test"
	projectKey := t.TempDir()
	writeKillTestRegistry(t, session, projectKey, map[string]string{
		"cc_1":  "GreenCastle",
		"cod_2": "BlueLake",
	})

	srv, getCalls := newFakeAgentMailServer(t)
	t.Setenv("AGENT_MAIL_URL", srv.URL)

	cleanupAgentMailOnKill(context.Background(), session, projectKey)

	calls := getCalls()
	releasedAgents := map[string]bool{}
	cleanupCalled := false
	for _, c := range calls {
		switch c.Tool {
		case "release_file_reservations":
			agent, _ := c.Args["agent_name"].(string)
			releasedAgents[agent] = true
			if pk, _ := c.Args["project_key"].(string); pk != projectKey {
				t.Errorf("release_file_reservations project_key = %q, want %q", pk, projectKey)
			}
			if _, hasPaths := c.Args["paths"]; hasPaths {
				t.Error("release_file_reservations should omit paths to release ALL reservations")
			}
			if _, hasIDs := c.Args["file_reservation_ids"]; hasIDs {
				t.Error("release_file_reservations should omit ids to release ALL reservations")
			}
		case "cleanup_pane_identities":
			cleanupCalled = true
			if pk, _ := c.Args["project_key"].(string); pk != projectKey {
				t.Errorf("cleanup_pane_identities project_key = %q, want %q", pk, projectKey)
			}
		}
	}
	if !releasedAgents["GreenCastle"] || !releasedAgents["BlueLake"] {
		t.Errorf("expected release_file_reservations for GreenCastle and BlueLake, got %v (calls: %+v)", releasedAgents, calls)
	}
	if !cleanupCalled {
		t.Errorf("cleanup_pane_identities was never called (calls: %+v)", calls)
	}
}

// TestCleanupAgentMailOnKill_MailDown asserts the cleanup is best-effort:
// with the Agent Mail server unreachable it returns promptly (bounded by
// agentMailKillCleanupTimeout) without error/panic, so the kill itself
// always succeeds.
func TestCleanupAgentMailOnKill_MailDown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")

	session := "killclean_down"
	projectKey := t.TempDir()
	writeKillTestRegistry(t, session, projectKey, map[string]string{
		"cc_1": "GreenCastle",
	})

	// Unreachable server: connection refused, no listener.
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1/")

	start := time.Now()
	cleanupAgentMailOnKill(context.Background(), session, projectKey) // must not panic or return error
	elapsed := time.Since(start)

	if elapsed > agentMailKillCleanupTimeout+2*time.Second {
		t.Fatalf("cleanup with mail down took %v, exceeds best-effort budget %v", elapsed, agentMailKillCleanupTimeout)
	}
}

// TestCleanupAgentMailOnKill_NoRegistry asserts a session with no Agent Mail
// registrations is a silent no-op (no server calls, no failure).
func TestCleanupAgentMailOnKill_NoRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")

	srv, getCalls := newFakeAgentMailServer(t)
	t.Setenv("AGENT_MAIL_URL", srv.URL)

	cleanupAgentMailOnKill(context.Background(), "killclean_none", t.TempDir())

	if calls := getCalls(); len(calls) != 0 {
		t.Errorf("expected no Agent Mail calls for unregistered session, got %+v", calls)
	}
}

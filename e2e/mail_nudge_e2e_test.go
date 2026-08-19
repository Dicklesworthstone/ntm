//go:build e2e
// +build e2e

package e2e

// mail_nudge_e2e_test.go proves the coordinator's Agent Mail idle-pane nudge
// (GH#231) end-to-end against a REAL tmux pane: a fakeagent Claude persona
// idles at its composer, a fake Agent Mail MCP server reports unread mail for
// the pane's registered agent, and one coordinator cycle delivers the nudge
// through the gated dispatch path — asserted composer-verified via the
// fixture's "submit" event (only logged when the prompt actually submitted).
// A second immediate cycle proves the per-pane cooldown, and a working pane
// proves the never-inject gate.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/coordinator"
)

// newMailNudgeMCPServer serves fetch_inbox for one agent with one unread
// message; every other tool call gets a JSON-RPC error (best-effort callers
// tolerate that).
func newMailNudgeMCPServer(t *testing.T, agentName string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agentmail.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		params, _ := req.Params.(map[string]interface{})
		toolName, _ := params["name"].(string)
		if req.Method != "tools/call" || toolName != "fetch_inbox" {
			_ = json.NewEncoder(w).Encode(agentmail.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID,
				Error: &agentmail.JSONRPCError{Code: -32601, Message: "not supported in fixture"}})
			return
		}
		args, _ := params["arguments"].(map[string]interface{})
		name, _ := args["agent_name"].(string)
		var inbox []map[string]interface{}
		if name == agentName {
			inbox = []map[string]interface{}{
				{"id": 1, "subject": "please pick up the baton", "from": "GreenCastle"},
			}
		}
		payload, _ := json.Marshal(map[string]interface{}{"result": inbox})
		_ = json.NewEncoder(w).Encode(agentmail.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(payload)})
	}))
}

// startMailNudgeCoordinator isolates registry + runtime-store state, spawns a
// fakeagent Claude pane, registers its Agent Mail identity, and returns the
// pane plus a coordinator configured with mail_nudge=true.
func startMailNudgeCoordinator(t *testing.T, serverURL string) (*fakeagentPane, *coordinator.SessionCoordinator) {
	t.Helper()
	// Build the fixture binary BEFORE overriding HOME: `go build` would
	// otherwise drop a read-only module cache under the temp HOME and break
	// t.TempDir cleanup.
	if _, err := ensureFakeagentBin(); err != nil {
		t.Fatalf("build fakeagent: %v", err)
	}
	// Isolate the session registry (HOME) and the mail-nudge cooldown
	// watermark store (XDG_CONFIG_HOME) from the operator's real state.
	stateRoot := t.TempDir()
	t.Setenv("HOME", stateRoot)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(stateRoot, ".config"))

	pane := startFakeagentSession(t, "claude", 0, 0)
	projectKey := t.TempDir()

	registry := agentmail.NewSessionAgentRegistry(pane.Session, projectKey)
	registry.AddAgent(pane.Session+"__cc_1", pane.PaneID, "BlueLake")
	if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
		t.Fatalf("SaveSessionAgentRegistry: %v", err)
	}

	client := agentmail.NewClient(agentmail.WithBaseURL(serverURL + "/"))
	cfg := coordinator.DefaultCoordinatorConfig()
	cfg.MailNudge = true
	coord := coordinator.New(pane.Session, projectKey, client, "CoordinatorE2E").WithConfig(cfg)
	return pane, coord
}

func TestMailNudgeE2E_IdlePaneNudgedComposerVerifiedWithCooldown(t *testing.T) {
	server := newMailNudgeMCPServer(t, "BlueLake")
	defer server.Close()
	pane, coord := startMailNudgeCoordinator(t, server.URL)

	// One coordinator cycle: observe, then nudge the idle pane.
	if _, err := coord.RunCycle(t.Context()); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	// The fixture logs "submit" only when the prompt actually SUBMITTED
	// (Enter accepted by the composer), which is the composer-verified claim.
	if _, ok := pane.WaitForEvent("submit", "unread Agent Mail", 20*time.Second); !ok {
		t.Fatalf("nudge did not land composer-verified; fixture events: %+v", pane.Events())
	}
	if got := pane.CountEvents("submit"); got != 1 {
		t.Fatalf("submit events = %d, want exactly 1", got)
	}

	// A second cycle inside the 60s cooldown must not re-nudge, even though
	// the fake server still reports unread mail.
	if _, err := coord.RunCycle(t.Context()); err != nil {
		t.Fatalf("second RunCycle: %v", err)
	}
	time.Sleep(2 * time.Second)
	if got := pane.CountEvents("submit"); got != 1 {
		t.Fatalf("submit events after cooldown cycle = %d, want still 1", got)
	}
}

func TestMailNudgeE2E_WorkingPaneNeverInjected(t *testing.T) {
	server := newMailNudgeMCPServer(t, "BlueLake")
	defer server.Close()
	pane, coord := startMailNudgeCoordinator(t, server.URL)

	// Put the fixture into a long working state (spinner on screen).
	pane.Control("work 120")

	if _, err := coord.RunCycle(t.Context()); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	time.Sleep(2 * time.Second)
	if got := pane.CountEvents("submit"); got != 0 {
		t.Fatalf("working pane received %d submits, want 0; events: %+v", got, pane.Events())
	}
}

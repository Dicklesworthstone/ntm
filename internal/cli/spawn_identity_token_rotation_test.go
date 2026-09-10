package cli

// Regression tests for GitHub issue #321.
//
// Re-registering a REUSED Agent Mail identity can rotate its registration
// token. spawnIdentityCoordinator.prepareAgent used to throw the
// re-registration response away (`_, _ = c.client.RegisterAgent(...)`), so the
// session registry — the durable store a later ntm process and the restarted
// worker both read — kept the superseded credential. The fresh-identity branch
// right below it had always persisted its token, which is what made the gap
// invisible.
//
// The contract pinned here: a nonempty replacement token is persisted through
// the existing registry path before prepareAgent returns (and therefore before
// the pane's agent process is launched); a refused registration, an
// empty-token response, or a response naming a different identity all preserve
// the recorded token; unrelated identities are never touched.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// rotationMailServer is an MCP JSON-RPC stub whose register_agent response is
// configurable, so a test can model a server that rotates the token, one that
// returns no token, and one that refuses the re-claim outright.
type rotationMailServer struct {
	// registerToken is returned as registration_token by register_agent.
	registerToken string
	// registerName overrides the name in the register_agent response ("" ->
	// echo the requested name).
	registerName string
	// registerFails makes register_agent answer with a JSON-RPC error.
	registerFails bool

	mu    sync.Mutex
	calls []string
}

func (s *rotationMailServer) recordCall(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, name)
}

func (s *rotationMailServer) callCount(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, call := range s.calls {
		if call == name {
			n++
		}
	}
	return n
}

func (s *rotationMailServer) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     interface{} `json:"id"`
			Method string      `json:"method"`
			Params struct {
				Name      string                 `json:"name"`
				Arguments map[string]interface{} `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		writeErr := func(msg string) {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]interface{}{"code": -32000, "message": msg},
			})
		}
		if req.Method != "tools/call" {
			writeErr("unknown method")
			return
		}
		s.recordCall(req.Params.Name)

		var result interface{}
		switch req.Params.Name {
		case "ensure_project":
			result = map[string]interface{}{"id": 1, "slug": "proj", "human_key": "proj"}
		case "register_agent":
			if s.registerFails {
				writeErr("re-claim refused")
				return
			}
			name := s.registerName
			if name == "" {
				if requested, ok := req.Params.Arguments["name"].(string); ok {
					name = requested
				}
			}
			agent := map[string]interface{}{
				"id":      42,
				"name":    name,
				"program": req.Params.Arguments["program"],
				"model":   req.Params.Arguments["model"],
			}
			if s.registerToken != "" {
				agent["registration_token"] = s.registerToken
			}
			result = agent
		case "create_agent_identity":
			result = map[string]interface{}{
				"id": 43, "name": "FreshMintedAgent",
				"program": req.Params.Arguments["program"],
				"model":   req.Params.Arguments["model"],
			}
		default:
			writeErr("unknown tool: " + req.Params.Name)
			return
		}
		raw, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(raw),
		})
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/"
}

// rotationFixture seeds a session registry that already binds paneID to
// agentName with oldToken recorded, plus an unrelated identity whose token
// must survive untouched. It returns the project key and session name.
func rotationFixture(t *testing.T, srvURL, agentName, paneTitle, paneID, oldToken string) (string, string) {
	t.Helper()
	isolateIdentityDirs(t)

	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })
	cfg = config.Default()
	cfg.AgentMail.Enabled = true
	cfg.AgentMail.AutoRegister = true
	cfg.AgentMail.URL = srvURL

	// Keep the coordinator off a real tmux server: pane liveness is
	// irrelevant here because ResolveForPane matches on the pane id first.
	oldProbe := spawnIdentityPaneProbe
	t.Cleanup(func() { spawnIdentityPaneProbe = oldProbe })
	spawnIdentityPaneProbe = func(context.Context, string) ([]tmux.Pane, error) {
		return []tmux.Pane{{ID: paneID, PID: 4242}}, nil
	}

	projectKey := t.TempDir()
	session := "spawn_identity_token_rotation_test"

	registry := agentmail.NewSessionAgentRegistry(session, projectKey)
	registry.AddAgent(paneTitle, paneID, agentName)
	registry.SetRegistrationToken(agentName, oldToken)
	registry.AddAgent(session+"__cc_9", "%99", "UnrelatedNeighbor")
	registry.SetRegistrationToken("UnrelatedNeighbor", "TOK-UNRELATED")
	if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	return projectKey, session
}

// reloadRegistry reads the registry back off disk, which is what a later ntm
// process (and the restarted worker's credential lookup) actually sees.
func reloadRegistry(t *testing.T, session, projectKey string) *agentmail.SessionAgentRegistry {
	t.Helper()
	registry, err := agentmail.LoadSessionAgentRegistry(session, projectKey)
	if err != nil || registry == nil {
		t.Fatalf("LoadSessionAgentRegistry: %v (registry=%v)", err, registry)
	}
	return registry
}

// TestReusedIdentityPersistsRotatedRegistrationToken is the core #321
// property: when re-registration hands back a replacement token, the
// persisted registry holds the replacement, not the token it was primed with.
func TestReusedIdentityPersistsRotatedRegistrationToken(t *testing.T) {
	const (
		agentName    = "BraveFalcon"
		paneID       = "%7"
		oldToken     = "TOK-OLD-SUPERSEDED"
		rotatedToken = "TOK-ROTATED-REPLACEMENT"
	)
	srv := &rotationMailServer{registerToken: rotatedToken}
	url := srv.start(t)
	paneTitle := "spawn_identity_token_rotation_test__cc_1"
	projectKey, session := rotationFixture(t, url, agentName, paneTitle, paneID, oldToken)

	coordinator := newSpawnIdentityCoordinator(projectKey, session)
	coordinator.prepareAgent(context.Background(), spawnedAgentInfo{
		paneIndex: 1,
		paneID:    paneID,
		paneTitle: paneTitle,
		agentType: "cc",
		model:     "opus",
	})

	// The reuse branch must be the one that ran.
	status := coordinator.finalStatus()
	if status == nil || status.AgentMap[paneID] != agentName {
		t.Fatalf("status = %+v, want the reused identity %s bound to %s", status, agentName, paneID)
	}
	if got := srv.callCount("create_agent_identity"); got != 0 {
		t.Fatalf("create_agent_identity calls = %d, want 0 (the identity must be reused, not re-minted)", got)
	}
	if got := srv.callCount("register_agent"); got != 1 {
		t.Fatalf("register_agent calls = %d, want exactly 1", got)
	}

	registry := reloadRegistry(t, session, projectKey)
	if got := registry.RegistrationToken(agentName); got != rotatedToken {
		t.Errorf("persisted token for %s = %q, want %q — the rotated credential must reach the registry before the agent process starts (#321)",
			agentName, got, rotatedToken)
	}
	if got := registry.RegistrationToken("UnrelatedNeighbor"); got != "TOK-UNRELATED" {
		t.Errorf("unrelated identity token = %q, want TOK-UNRELATED (untouched)", got)
	}
}

// TestReusedIdentityKeepsTokenWhenResponseCarriesNone: an empty
// registration_token means "unchanged", never "clear it". SetRegistrationToken
// deletes on an empty value, so this is the case a naive fix breaks.
func TestReusedIdentityKeepsTokenWhenResponseCarriesNone(t *testing.T) {
	const (
		agentName = "SteadyHeron"
		paneID    = "%11"
		oldToken  = "TOK-STILL-VALID"
	)
	srv := &rotationMailServer{registerToken: ""}
	url := srv.start(t)
	paneTitle := "spawn_identity_token_rotation_test__cc_2"
	projectKey, session := rotationFixture(t, url, agentName, paneTitle, paneID, oldToken)

	coordinator := newSpawnIdentityCoordinator(projectKey, session)
	coordinator.prepareAgent(context.Background(), spawnedAgentInfo{
		paneIndex: 2, paneID: paneID, paneTitle: paneTitle, agentType: "cc", model: "opus",
	})

	registry := reloadRegistry(t, session, projectKey)
	if got := registry.RegistrationToken(agentName); got != oldToken {
		t.Errorf("persisted token = %q, want the preserved %q (an empty response must not clear the credential)", got, oldToken)
	}
}

// TestReusedIdentityKeepsTokenWhenReRegistrationFails: reuse deliberately does
// not depend on the server (#69), so a refused or timed-out re-claim must
// leave the recorded credential exactly as it was.
func TestReusedIdentityKeepsTokenWhenReRegistrationFails(t *testing.T) {
	const (
		agentName = "QuietBadger"
		paneID    = "%13"
		oldToken  = "TOK-SURVIVES-OUTAGE"
	)
	srv := &rotationMailServer{registerFails: true}
	url := srv.start(t)
	paneTitle := "spawn_identity_token_rotation_test__cc_3"
	projectKey, session := rotationFixture(t, url, agentName, paneTitle, paneID, oldToken)

	coordinator := newSpawnIdentityCoordinator(projectKey, session)
	coordinator.prepareAgent(context.Background(), spawnedAgentInfo{
		paneIndex: 3, paneID: paneID, paneTitle: paneTitle, agentType: "cc", model: "opus",
	})

	// Reuse still succeeds locally even though the server refused.
	status := coordinator.finalStatus()
	if status == nil || status.AgentMap[paneID] != agentName {
		t.Fatalf("status = %+v, want reuse of %s to survive a refused re-registration", status, agentName)
	}
	registry := reloadRegistry(t, session, projectKey)
	if got := registry.RegistrationToken(agentName); got != oldToken {
		t.Errorf("persisted token = %q, want the preserved %q (a failed re-claim must not disturb it)", got, oldToken)
	}
}

// TestReusedIdentityIgnoresTokenForDifferentName: a response naming another
// identity is a server anomaly; recording its credential under our name would
// hand the pane a token that authenticates as somebody else.
func TestReusedIdentityIgnoresTokenForDifferentName(t *testing.T) {
	const (
		agentName = "AmberVole"
		paneID    = "%17"
		oldToken  = "TOK-OURS"
	)
	srv := &rotationMailServer{registerToken: "TOK-SOMEONE-ELSE", registerName: "NotOurAgent"}
	url := srv.start(t)
	paneTitle := "spawn_identity_token_rotation_test__cc_4"
	projectKey, session := rotationFixture(t, url, agentName, paneTitle, paneID, oldToken)

	coordinator := newSpawnIdentityCoordinator(projectKey, session)
	coordinator.prepareAgent(context.Background(), spawnedAgentInfo{
		paneIndex: 4, paneID: paneID, paneTitle: paneTitle, agentType: "cc", model: "opus",
	})

	registry := reloadRegistry(t, session, projectKey)
	if got := registry.RegistrationToken(agentName); got != oldToken {
		t.Errorf("persisted token = %q, want the preserved %q (a token issued for a different name is not ours to record)", got, oldToken)
	}
	if got := registry.RegistrationToken("NotOurAgent"); got != "" {
		t.Errorf("token recorded for the unexpected name %q = %q, want none", "NotOurAgent", got)
	}
}

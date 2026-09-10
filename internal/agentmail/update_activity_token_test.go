package agentmail

// Sibling-instance regression test for GitHub issue #321.
//
// #321 was reported against the spawn identity coordinator, which dropped the
// response of the re-registration it performs for a REUSED identity and so
// kept a superseded registration token. Auditing every other place that
// re-registers an existing agent by name turned up the same shape in
// UpdateSessionActivity: it re-registers the session coordinator identity
// purely to refresh last_active_ts and discarded the returned agent, so a
// rotated credential never reached SessionAgentInfo on disk. (The
// already-registered branch of EnsureSessionAgent right above it had always
// persisted its rotated token — that asymmetry is what hid this one.)

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// mcpWithHealth serves the plain GET /health readiness endpoint the
// availability probe uses, and delegates every tool call to mockMCPHandler.
func mcpWithHealth(t *testing.T, handlers map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError)) http.Handler {
	t.Helper()
	tools := mockMCPHandler(t, handlers)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ready"}`))
			return
		}
		tools.ServeHTTP(w, r)
	})
}

// sessionActivityFixture isolates the session store under a temp HOME and
// seeds a persisted coordinator identity carrying oldToken.
func sessionActivityFixture(t *testing.T, sessionName, projectKey, agentName, oldToken string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".state"))

	info := &SessionAgentInfo{
		AgentName:         agentName,
		ProjectKey:        projectKey,
		RegistrationToken: oldToken,
	}
	if err := SaveSessionAgent(sessionName, projectKey, info); err != nil {
		t.Fatalf("seed session agent: %v", err)
	}
}

// TestUpdateSessionActivityPersistsRotatedToken: when the activity refresh
// comes back with a replacement credential, the on-disk record must hold the
// replacement — otherwise every later ntm process authenticates with a token
// the server has already retired.
func TestUpdateSessionActivityPersistsRotatedToken(t *testing.T) {
	const (
		session      = "update_activity_rotation"
		agentName    = "CoordinatorOne"
		oldToken     = "TOK-OLD"
		rotatedToken = "TOK-ROTATED"
	)
	projectKey := t.TempDir()
	sessionActivityFixture(t, session, projectKey, agentName, oldToken)

	var sawToken string
	server := httptest.NewServer(mcpWithHealth(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"register_agent": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			sawToken, _ = args["registration_token"].(string)
			return Agent{ID: 3, Name: agentName, RegistrationToken: rotatedToken}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	if err := c.UpdateSessionActivity(context.Background(), session, projectKey); err != nil {
		t.Fatalf("UpdateSessionActivity: %v", err)
	}

	// The re-claim must have authenticated as the existing identity, which it
	// can only do if the persisted token was pushed into the client cache
	// before the call.
	if sawToken != oldToken {
		t.Errorf("register_agent saw registration_token %q, want the persisted %q — the re-claim must authenticate as the existing identity", sawToken, oldToken)
	}

	reloaded, err := LoadSessionAgent(session, projectKey)
	if err != nil || reloaded == nil {
		t.Fatalf("LoadSessionAgent: %v (info=%v)", err, reloaded)
	}
	if reloaded.RegistrationToken != rotatedToken {
		t.Errorf("persisted token = %q, want %q (a rotated credential must be durable, #321)", reloaded.RegistrationToken, rotatedToken)
	}
}

// TestUpdateSessionActivityKeepsTokenWhenResponseCarriesNone: an omitted
// registration_token means "unchanged" and must never blank the record.
func TestUpdateSessionActivityKeepsTokenWhenResponseCarriesNone(t *testing.T) {
	const (
		session   = "update_activity_no_token"
		agentName = "CoordinatorTwo"
		oldToken  = "TOK-STILL-VALID"
	)
	projectKey := t.TempDir()
	sessionActivityFixture(t, session, projectKey, agentName, oldToken)

	server := httptest.NewServer(mcpWithHealth(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"register_agent": func(map[string]interface{}) (interface{}, *JSONRPCError) {
			return Agent{ID: 3, Name: agentName}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	if err := c.UpdateSessionActivity(context.Background(), session, projectKey); err != nil {
		t.Fatalf("UpdateSessionActivity: %v", err)
	}

	reloaded, err := LoadSessionAgent(session, projectKey)
	if err != nil || reloaded == nil {
		t.Fatalf("LoadSessionAgent: %v (info=%v)", err, reloaded)
	}
	if reloaded.RegistrationToken != oldToken {
		t.Errorf("persisted token = %q, want the preserved %q", reloaded.RegistrationToken, oldToken)
	}
}

// TestUpdateSessionActivityKeepsTokenWhenServerRefuses: a failed refresh
// surfaces its error and leaves the recorded credential alone.
func TestUpdateSessionActivityKeepsTokenWhenServerRefuses(t *testing.T) {
	const (
		session   = "update_activity_refused"
		agentName = "CoordinatorThree"
		oldToken  = "TOK-SURVIVES"
	)
	projectKey := t.TempDir()
	sessionActivityFixture(t, session, projectKey, agentName, oldToken)

	server := httptest.NewServer(mcpWithHealth(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"register_agent": func(map[string]interface{}) (interface{}, *JSONRPCError) {
			return nil, &JSONRPCError{Code: -32000, Message: "re-claim refused"}
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	if err := c.UpdateSessionActivity(context.Background(), session, projectKey); err == nil {
		t.Fatal("UpdateSessionActivity must surface a refused activity refresh")
	}

	reloaded, err := LoadSessionAgent(session, projectKey)
	if err != nil || reloaded == nil {
		t.Fatalf("LoadSessionAgent: %v (info=%v)", err, reloaded)
	}
	if reloaded.RegistrationToken != oldToken {
		t.Errorf("persisted token = %q, want the preserved %q", reloaded.RegistrationToken, oldToken)
	}
}

package agentmail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestRegisterAgent_CapturesRegistrationToken verifies that the
// `registration_token` field returned by the server in
// create_agent_identity / register_agent is captured into the Agent
// struct AND cached on the Client so later identity-scoped calls
// pick it up automatically (ntm#146).
func TestRegisterAgent_CapturesRegistrationToken(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"register_agent": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return Agent{
				ID:                7,
				Name:              "SilverOtter",
				Program:           "claude-code",
				Model:             "opus-4.7",
				RegistrationToken: "YXvGAEPgggp7TXqNbGaWwdJjlf14DXKOPPQ_x1BdQHg",
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	agent, err := c.RegisterAgent(context.Background(), RegisterAgentOptions{
		ProjectKey: "/abs/project",
		Program:    "claude-code",
		Model:      "opus-4.7",
		Name:       "SilverOtter",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agent.RegistrationToken == "" {
		t.Fatal("Agent.RegistrationToken must be populated from server response")
	}
	if got := c.RegistrationToken("/abs/project", "SilverOtter"); got != agent.RegistrationToken {
		t.Errorf("client cache should remember token; got %q want %q", got, agent.RegistrationToken)
	}
}

// TestCreateAgentIdentity_CapturesRegistrationToken is the sibling test
// for the create_agent_identity tool — same behavior, different MCP
// method name.
func TestCreateAgentIdentity_CapturesRegistrationToken(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"create_agent_identity": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return Agent{
				ID:                3,
				Name:              "StormyOwl",
				Program:           args["program"].(string),
				Model:             args["model"].(string),
				RegistrationToken: "ZZZ-from-create-identity",
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	agent, err := c.CreateAgentIdentity(context.Background(), RegisterAgentOptions{
		ProjectKey: "/abs/project",
		Program:    "codex",
		Model:      "gpt-5",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agent.RegistrationToken != "ZZZ-from-create-identity" {
		t.Errorf("agent token = %q, want ZZZ-from-create-identity", agent.RegistrationToken)
	}
	if got := c.RegistrationToken("/abs/project", "StormyOwl"); got != "ZZZ-from-create-identity" {
		t.Errorf("client cache token = %q, want ZZZ-from-create-identity", got)
	}
}

// TestAttachRegistrationToken_PassedThroughOnFetchInbox verifies that
// once a token is cached for an agent, fetch_inbox (an identity-scoped
// MCP call) actually carries that token in its args — which is the
// concrete server behavior that was broken in #146.
func TestAttachRegistrationToken_PassedThroughOnFetchInbox(t *testing.T) {
	t.Parallel()

	var observedToken atomic.Value // string
	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"fetch_inbox": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			tok, _ := args["registration_token"].(string)
			observedToken.Store(tok)
			// Mirror the server contract: succeed iff a token is present.
			if tok == "" {
				return nil, &JSONRPCError{
					Code:    -32602,
					Message: "fetch_inbox requires registration_token",
				}
			}
			return map[string]interface{}{"result": []InboxMessage{}}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	c.SetRegistrationToken("/abs/project", "StormyOwl", "TOK-12345")

	if _, err := c.FetchInbox(context.Background(), FetchInboxOptions{
		ProjectKey: "/abs/project",
		AgentName:  "StormyOwl",
	}); err != nil {
		t.Fatalf("fetch_inbox should succeed once token is cached: %v", err)
	}
	if got, _ := observedToken.Load().(string); got != "TOK-12345" {
		t.Errorf("server-observed token = %q, want TOK-12345", got)
	}
}

// TestAttachSenderToken_PassedThroughOnSendMessage verifies that
// send_message uses the server's sender_token parameter, not the
// registration_token parameter used by read/reservation tools.
func TestAttachSenderToken_PassedThroughOnSendMessage(t *testing.T) {
	t.Parallel()

	var observedSenderToken atomic.Value // string
	var sawRegistrationToken atomic.Bool
	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"send_message": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			tok, _ := args["sender_token"].(string)
			observedSenderToken.Store(tok)
			if _, ok := args["registration_token"]; ok {
				sawRegistrationToken.Store(true)
			}
			if tok == "" {
				return nil, &JSONRPCError{
					Code:    -32602,
					Message: "send_message requires sender_token for verified sender identity",
				}
			}
			return SendResult{
				Deliveries: []MessageDelivery{{Project: "abs-project", Payload: &Message{ID: 55}}},
				Count:      1,
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	c.SetRegistrationToken("/abs/project", "GreenCastle", "TOK-SENDER")

	if _, err := c.SendMessage(context.Background(), SendMessageOptions{
		ProjectKey: "/abs/project",
		SenderName: "GreenCastle",
		To:         []string{"BlueLake"},
		Subject:    "Status",
		BodyMD:     "Done.",
	}); err != nil {
		t.Fatalf("send_message should succeed once sender token is cached: %v", err)
	}
	if got, _ := observedSenderToken.Load().(string); got != "TOK-SENDER" {
		t.Errorf("server-observed sender_token = %q, want TOK-SENDER", got)
	}
	if sawRegistrationToken.Load() {
		t.Error("send_message must not receive registration_token; use sender_token")
	}
}

func TestContactToolsAttachCorrectRegistrationToken(t *testing.T) {
	t.Parallel()

	var requestToken atomic.Value // string
	var respondToken atomic.Value // string
	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"request_contact": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			tok, _ := args["registration_token"].(string)
			requestToken.Store(tok)
			return ContactRequestResult{Status: "pending"}, nil
		},
		"respond_contact": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			tok, _ := args["registration_token"].(string)
			respondToken.Store(tok)
			return ContactRespondResult{Status: "approved"}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	c.SetRegistrationToken("/abs/project", "GreenCastle", "TOK-REQUESTER")
	c.SetRegistrationToken("/abs/project", "BlueLake", "TOK-TARGET")

	if _, err := c.RequestContact(context.Background(), RequestContactOptions{
		ProjectKey: "/abs/project",
		FromAgent:  "GreenCastle",
		ToAgent:    "BlueLake",
	}); err != nil {
		t.Fatalf("request_contact failed: %v", err)
	}
	if _, err := c.RespondContact(context.Background(), RespondContactOptions{
		ProjectKey: "/abs/project",
		ToAgent:    "BlueLake",
		FromAgent:  "GreenCastle",
		Accept:     true,
	}); err != nil {
		t.Fatalf("respond_contact failed: %v", err)
	}

	if got, _ := requestToken.Load().(string); got != "TOK-REQUESTER" {
		t.Errorf("request_contact registration_token = %q, want TOK-REQUESTER", got)
	}
	if got, _ := respondToken.Load().(string); got != "TOK-TARGET" {
		t.Errorf("respond_contact registration_token = %q, want TOK-TARGET", got)
	}
}

func TestContactHandshakeAttachesRequesterAndTargetTokens(t *testing.T) {
	t.Parallel()

	var requesterToken atomic.Value // string
	var targetToken atomic.Value    // string
	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"macro_contact_handshake": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			req, _ := args["requester_registration_token"].(string)
			tgt, _ := args["target_registration_token"].(string)
			requesterToken.Store(req)
			targetToken.Store(tgt)
			return ContactHandshakeResult{ContactStatus: "approved"}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	c.SetRegistrationToken("/abs/project", "GreenCastle", "TOK-REQUESTER")
	c.SetRegistrationToken("/abs/project", "BlueLake", "TOK-TARGET")

	if _, err := c.ContactHandshake(context.Background(), ContactHandshakeOptions{
		ProjectKey: "/abs/project",
		AgentName:  "GreenCastle",
		ToAgent:    "BlueLake",
		AutoAccept: true,
		TTLSeconds: 3600,
		Reason:     "test",
		Program:    "codex",
		Model:      "gpt-5",
	}); err != nil {
		t.Fatalf("macro_contact_handshake failed: %v", err)
	}

	if got, _ := requesterToken.Load().(string); got != "TOK-REQUESTER" {
		t.Errorf("requester_registration_token = %q, want TOK-REQUESTER", got)
	}
	if got, _ := targetToken.Load().(string); got != "TOK-TARGET" {
		t.Errorf("target_registration_token = %q, want TOK-TARGET", got)
	}
}

// TestAttachRegistrationToken_OmittedForUnknownAgent verifies that we
// don't invent or leak a token onto calls for agents we don't have one
// for — that would make the server's auth check meaningless.
func TestAttachRegistrationToken_OmittedForUnknownAgent(t *testing.T) {
	t.Parallel()

	var sawTokenKey atomic.Bool
	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"fetch_inbox": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			if _, ok := args["registration_token"]; ok {
				sawTokenKey.Store(true)
			}
			return map[string]interface{}{"result": []InboxMessage{}}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	// Note: no SetRegistrationToken — the cache is empty.
	if _, err := c.FetchInbox(context.Background(), FetchInboxOptions{
		ProjectKey: "/abs/project",
		AgentName:  "UnknownAgent",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sawTokenKey.Load() {
		t.Error("registration_token must not be present when nothing is cached")
	}
}

// TestSetAndClearRegistrationToken sanity-checks the cache accessors,
// including the clear-on-empty-string contract.
func TestSetAndClearRegistrationToken(t *testing.T) {
	t.Parallel()
	c := NewClient()
	c.SetRegistrationToken("/p", "A", "tok-A")
	c.SetRegistrationToken("/p", "B", "tok-B")
	c.SetRegistrationToken("/q", "A", "tok-qA") // distinct project namespace

	if got := c.RegistrationToken("/p", "A"); got != "tok-A" {
		t.Errorf("p/A = %q, want tok-A", got)
	}
	if got := c.RegistrationToken("/p", "B"); got != "tok-B" {
		t.Errorf("p/B = %q, want tok-B", got)
	}
	if got := c.RegistrationToken("/q", "A"); got != "tok-qA" {
		t.Errorf("q/A = %q, want tok-qA", got)
	}
	if got := c.RegistrationToken("/p", "missing"); got != "" {
		t.Errorf("p/missing should be empty, got %q", got)
	}

	c.SetRegistrationToken("/p", "A", "") // clear
	if got := c.RegistrationToken("/p", "A"); got != "" {
		t.Errorf("after clear, p/A should be empty, got %q", got)
	}

	// Nil-receiver safe (mirrors the helpers that operate on possibly
	// uninitialised clients).
	var cnil *Client
	cnil.SetRegistrationToken("/p", "A", "tok") // must not panic
	if got := cnil.RegistrationToken("/p", "A"); got != "" {
		t.Errorf("nil client should always return empty token, got %q", got)
	}
}

// The MCP-only legacy route uses an existing authorized identity. It must
// never attempt ordinary registration of the reserved HumanOverseer name.
func TestSendOverseerMessage_MCPPath(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	var sawEnsureProject, sawRegisterOverseer atomic.Bool
	var sentImportance atomic.Value // string
	var sentSubject atomic.Value    // string
	var sentBody atomic.Value       // string

	mcp := mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"ensure_project": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			sawEnsureProject.Store(true)
			calls.Add(1)
			return Project{ID: 1, Slug: "abs-project"}, nil
		},
		"register_agent": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			calls.Add(1)
			if name, _ := args["name"].(string); name == HumanOverseerAgentName {
				sawRegisterOverseer.Store(true)
			}
			return Agent{
				ID:                99,
				Name:              HumanOverseerAgentName,
				Program:           "ntm",
				Model:             "human",
				RegistrationToken: "tok-overseer",
			}, nil
		},
		"send_message": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			calls.Add(1)
			if v, ok := args["importance"]; ok {
				if s, _ := v.(string); s != "" {
					sentImportance.Store(s)
				}
			}
			if v, _ := args["subject"].(string); v != "" {
				sentSubject.Store(v)
			}
			if v, _ := args["body_md"].(string); v != "" {
				sentBody.Store(v)
			}
			// Verify the overseer's token rode along under the
			// `sender_token` arg (send_message's identity-scoped param;
			// fetch_inbox / acknowledge_message etc. use
			// `registration_token` keyed off `agent_name`). The
			// existing identity token is cached, and attachSenderToken should
			// add it here because sender_name matches the cache key.
			if tok, _ := args["sender_token"].(string); tok != "tok-overseer" {
				return nil, &JSONRPCError{
					Code:    -32602,
					Message: "send_message expected cached sender_token, got " + tok,
				}
			}
			return SendResult{
				Deliveries: []MessageDelivery{
					{Project: "abs-project", Payload: &Message{ID: 1234, Subject: args["subject"].(string)}},
				},
				Count: 1,
			}, nil
		},
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/overseer/send") {
			http.NotFound(w, r)
			return
		}
		mcp.ServeHTTP(w, r)
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	c.SetRegistrationToken("/abs/project", HumanOverseerAgentName, "tok-overseer")
	result, err := c.SendOverseerMessage(context.Background(), OverseerMessageOptions{
		ProjectKey: "/abs/project",
		Recipients: []string{"StormyOwl", "SilverOtter"},
		Subject:    "Please pause the deploy",
		BodyMD:     "Auth issue under investigation.",
	})
	if err != nil {
		t.Fatalf("MCP overseer path failed: %v", err)
	}
	if !sawEnsureProject.Load() {
		t.Error("ensure_project must be called as part of overseer flow")
	}
	if sawRegisterOverseer.Load() {
		t.Error("reserved overseer identity must never be registered as an ordinary agent")
	}
	if got, _ := sentImportance.Load().(string); got != "high" {
		t.Errorf("send_message importance = %q, want high", got)
	}
	if got, _ := sentSubject.Load().(string); got != "Please pause the deploy" {
		t.Errorf("subject = %q, want 'Please pause the deploy'", got)
	}
	if got, _ := sentBody.Load().(string); !strings.Contains(got, HumanOverseerPreamble) {
		t.Errorf("body must contain the overseer preamble; got %q", got)
	}
	if result.MessageID != 1234 {
		t.Errorf("result.MessageID = %d, want 1234", result.MessageID)
	}
	if !result.Success {
		t.Error("result.Success must be true on the happy path")
	}
}

// TestSendOverseerMessage_FallsBackToHTTPWithoutProjectKey verifies
// the legacy code path still works when callers haven't been updated
// to pass ProjectKey. This is what keeps the old TestSendOverseerMessage_*
// fixtures passing.
func TestSendOverseerMessage_FallsBackToHTTPWithoutProjectKey(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mail/some-slug/overseer/send" {
			t.Errorf("expected HTTP overseer path, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message_id": 7})
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/mcp/"))
	result, err := c.SendOverseerMessage(context.Background(), OverseerMessageOptions{
		// No ProjectKey — must drop to HTTP fallback.
		ProjectSlug: "some-slug",
		Recipients:  []string{"x"},
		Subject:     "fallback",
		BodyMD:      "test",
	})
	if err != nil {
		t.Fatalf("HTTP fallback failed: %v", err)
	}
	if result.MessageID != 7 {
		t.Errorf("MessageID = %d, want 7", result.MessageID)
	}
}

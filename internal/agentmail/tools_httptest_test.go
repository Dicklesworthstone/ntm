package agentmail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mockMCPHandler returns an http.Handler that dispatches JSON-RPC tools/call
// requests to tool-specific handlers. Each handler receives the raw arguments
// map and returns a JSON-encodable result or an error string.
func mockMCPHandler(t *testing.T, handlers map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError)) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
			return
		}

		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")

		if req.Method == "tools/call" {
			params, _ := req.Params.(map[string]interface{})
			toolName, _ := params["name"].(string)
			args, _ := params["arguments"].(map[string]interface{})

			handler, ok := handlers[toolName]
			if !ok {
				resp := JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   &JSONRPCError{Code: -32601, Message: "unknown tool: " + toolName},
				}
				json.NewEncoder(w).Encode(resp)
				return
			}

			result, rpcErr := handler(args)
			if rpcErr != nil {
				resp := JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   rpcErr,
				}
				json.NewEncoder(w).Encode(resp)
				return
			}

			resultJSON, _ := json.Marshal(result)
			resp := JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(resultJSON),
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		if req.Method == "resources/read" {
			params, _ := req.Params.(map[string]interface{})
			uri, _ := params["uri"].(string)

			handler, ok := handlers["resource:"+uri]
			if !ok {
				// Default: return not-found style error
				resp := JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   &JSONRPCError{Code: -32602, Message: "resource not found: " + uri},
				}
				json.NewEncoder(w).Encode(resp)
				return
			}

			result, rpcErr := handler(nil)
			if rpcErr != nil {
				resp := JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   rpcErr,
				}
				json.NewEncoder(w).Encode(resp)
				return
			}

			resultJSON, _ := json.Marshal(result)
			resp := JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(resultJSON),
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &JSONRPCError{Code: -32601, Message: "unknown method: " + req.Method},
		}
		json.NewEncoder(w).Encode(resp)
	})
}

func TestEnsureProject(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"ensure_project": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			key, _ := args["human_key"].(string)
			return Project{
				ID:       1,
				Slug:     "ntm",
				HumanKey: key,
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	project, err := c.EnsureProject(context.Background(), "/data/projects/ntm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if project.Slug != "ntm" {
		t.Errorf("slug = %q, want ntm", project.Slug)
	}
	if project.HumanKey != "/data/projects/ntm" {
		t.Errorf("human_key = %q, want /data/projects/ntm", project.HumanKey)
	}
}

func TestEnsureProject_Error(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"ensure_project": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return nil, &JSONRPCError{Code: -32000, Message: "project creation failed"}
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	_, err := c.EnsureProject(context.Background(), "/bad")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCreateAgentIdentity(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"create_agent_identity": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return Agent{
				ID:      42,
				Name:    "GreenCastle",
				Program: args["program"].(string),
				Model:   args["model"].(string),
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	agent, err := c.CreateAgentIdentity(context.Background(), RegisterAgentOptions{
		ProjectKey:      "/test",
		Program:         "claude-code",
		Model:           "opus-4.5",
		Name:            "GreenCastle",
		TaskDescription: "testing",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agent.Name != "GreenCastle" {
		t.Errorf("name = %q, want GreenCastle", agent.Name)
	}
	if agent.Program != "claude-code" {
		t.Errorf("program = %q, want claude-code", agent.Program)
	}
}

func TestWhois(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"whois": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return Agent{
				ID:              10,
				Name:            args["agent_name"].(string),
				Program:         "claude-code",
				Model:           "opus-4.5",
				TaskDescription: "test coverage",
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	agent, err := c.Whois(context.Background(), "/test", "BlueLake", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agent.Name != "BlueLake" {
		t.Errorf("name = %q, want BlueLake", agent.Name)
	}
}

func TestSendMessage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"send_message": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return SendResult{
				Deliveries: []MessageDelivery{
					{Project: "ntm", Payload: &Message{ID: 100, Subject: args["subject"].(string)}},
				},
				Count: 1,
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	convertImages := true
	result, err := c.SendMessage(context.Background(), SendMessageOptions{
		ProjectKey:    "/test",
		SenderName:    "TestAgent",
		To:            []string{"BlueLake"},
		Subject:       "Hello",
		BodyMD:        "Test body",
		CC:            []string{"RedStone"},
		BCC:           []string{"GreenCastle"},
		Importance:    "high",
		AckRequired:   true,
		ThreadID:      "thread-1",
		ConvertImages: &convertImages,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Count != 1 {
		t.Errorf("count = %d, want 1", result.Count)
	}
	if result.Deliveries[0].Payload.Subject != "Hello" {
		t.Errorf("subject = %q, want Hello", result.Deliveries[0].Payload.Subject)
	}
}

func TestReplyMessage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"reply_message": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return Message{
				ID:      200,
				Subject: "Re: Hello",
				BodyMD:  args["body_md"].(string),
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	msg, err := c.ReplyMessage(context.Background(), ReplyMessageOptions{
		ProjectKey:    "/test",
		MessageID:     100,
		SenderName:    "TestAgent",
		BodyMD:        "Reply body",
		To:            []string{"BlueLake"},
		CC:            []string{"RedStone"},
		BCC:           []string{"GreenCastle"},
		SubjectPrefix: "RE:",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.ID != 200 {
		t.Errorf("msg.ID = %d, want 200", msg.ID)
	}
	if msg.BodyMD != "Reply body" {
		t.Errorf("body = %q, want Reply body", msg.BodyMD)
	}
}

func TestMarkMessageRead(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"mark_message_read": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return map[string]interface{}{"message_id": args["message_id"], "read": true}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	err := c.MarkMessageRead(context.Background(), "/test", "BlueLake", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAcknowledgeMessage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"acknowledge_message": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return map[string]interface{}{"message_id": args["message_id"], "acknowledged": true}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	err := c.AcknowledgeMessage(context.Background(), "/test", "BlueLake", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRequestContact(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"request_contact": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return ContactRequestResult{
				Status: "pending",
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	result, err := c.RequestContact(context.Background(), RequestContactOptions{
		ProjectKey: "/test",
		FromAgent:  "TestAgent",
		ToAgent:    "BlueLake",
		ToProject:  "/other",
		Reason:     "coordination",
		TTLSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "pending" {
		t.Errorf("status = %q, want pending", result.Status)
	}
}

func TestRespondContact(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"respond_contact": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return map[string]interface{}{"status": "approved"}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	err := c.RespondContact(context.Background(), RespondContactOptions{
		ProjectKey: "/test",
		ToAgent:    "BlueLake",
		FromAgent:  "TestAgent",
		Accept:     true,
		TTLSeconds: 7200,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestListContacts(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"list_contacts": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return []ContactLink{
				{FromAgent: "TestAgent", ToAgent: "BlueLake", Status: "approved"},
				{FromAgent: "TestAgent", ToAgent: "RedStone", Status: "pending"},
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	contacts, err := c.ListContacts(context.Background(), "/test", "TestAgent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(contacts) != 2 {
		t.Fatalf("len(contacts) = %d, want 2", len(contacts))
	}
	if contacts[0].ToAgent != "BlueLake" {
		t.Errorf("contacts[0].ToAgent = %q, want BlueLake", contacts[0].ToAgent)
	}
}

func TestSearchMessages(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"search_messages": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return map[string]interface{}{
				"result": []SearchResult{
					{ID: 1, Subject: "Build plan", From: "BlueLake"},
					{ID: 2, Subject: "Build update", From: "RedStone"},
				},
				"assistance": "use recency ranking for recent activity",
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	results, err := c.SearchMessages(context.Background(), SearchOptions{
		ProjectKey: "/test",
		Query:      "build",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
}

func TestSummarizeThread(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"summarize_thread": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return ThreadSummaryResponse{
				ThreadID: args["thread_id"].(string),
				Summary: ThreadSummary{
					Participants: []string{"BlueLake", "RedStone"},
					KeyPoints:    []string{"Discussed build plan"},
					ActionItems:  []string{"Write tests"},
				},
				Examples: []InboxMessage{
					{ID: 1, Subject: "Build plan"},
				},
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	summary, err := c.SummarizeThread(context.Background(), SummarizeThreadOptions{
		ProjectKey:      "/test",
		ThreadID:        "TKT-123",
		IncludeExamples: true,
		LLMMode:         true,
		LLMModel:        "opus-4.5",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary.ThreadID != "TKT-123" {
		t.Errorf("thread_id = %q, want TKT-123", summary.ThreadID)
	}
	if len(summary.Participants) != 2 {
		t.Errorf("len(participants) = %d, want 2", len(summary.Participants))
	}
}

func TestReservePaths(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"file_reservation_paths": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return ReservationResult{
				Granted: []FileReservation{
					{ID: 1, PathPattern: "internal/agentmail/*", AgentName: "TestAgent", Exclusive: true},
				},
				Conflicts: nil,
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	result, err := c.ReservePaths(context.Background(), FileReservationOptions{
		ProjectKey: "/test",
		AgentName:  "TestAgent",
		Paths:      []string{"internal/agentmail/*"},
		TTLSeconds: 3600,
		Exclusive:  true,
		Reason:     "testing",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Granted) != 1 {
		t.Fatalf("len(granted) = %d, want 1", len(result.Granted))
	}
	if result.Granted[0].PathPattern != "internal/agentmail/*" {
		t.Errorf("path = %q, want internal/agentmail/*", result.Granted[0].PathPattern)
	}
}

func TestReservePaths_Conflict(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"file_reservation_paths": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return ReservationResult{
				Granted: nil,
				Conflicts: []ReservationConflict{
					{Path: "internal/agentmail/*", Holders: []string{"BlueLake"}},
				},
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	result, err := c.ReservePaths(context.Background(), FileReservationOptions{
		ProjectKey: "/test",
		AgentName:  "TestAgent",
		Paths:      []string{"internal/agentmail/*"},
	})
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !IsReservationConflict(err) {
		t.Errorf("expected reservation conflict error, got: %v", err)
	}
	if result == nil {
		t.Fatal("expected result even with conflict")
	}
	if len(result.Conflicts) != 1 {
		t.Errorf("len(conflicts) = %d, want 1", len(result.Conflicts))
	}
}

func TestReleaseReservations(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"release_file_reservations": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return map[string]interface{}{"released": 2}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	result, err := c.ReleaseReservations(context.Background(), "/test", "TestAgent", []string{"a/*"}, []int{1, 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Released != 2 {
		t.Fatalf("released = %d, want 2", result.Released)
	}
}

func TestRenewReservations(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"renew_file_reservations": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return RenewReservationsResult{
				Renewed: 1,
				Reservations: []RenewedReservation{
					{ID: 1, PathPattern: "a/*"},
				},
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	result, err := c.RenewReservations(context.Background(), RenewReservationsOptions{
		ProjectKey:     "/test",
		AgentName:      "TestAgent",
		ExtendSeconds:  1800,
		ReservationIDs: []int{1},
		Paths:          []string{"a/*"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Renewed != 1 {
		t.Errorf("renewed = %d, want 1", result.Renewed)
	}
	if len(result.Reservations) != 1 {
		t.Fatalf("len(reservations) = %d, want 1", len(result.Reservations))
	}
	if result.Reservations[0].PathPattern != "a/*" {
		t.Errorf("path_pattern = %q, want %q", result.Reservations[0].PathPattern, "a/*")
	}
}

func TestStartSession(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"macro_start_session": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return SessionStartResult{
				Project: &Project{ID: 1, Slug: "ntm"},
				Agent:   &Agent{ID: 5, Name: "BlueLake"},
				Inbox:   []InboxMessage{},
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	result, err := c.StartSession(context.Background(), "/test", "claude-code", "opus-4.5", "testing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Project.Slug != "ntm" {
		t.Errorf("project slug = %q, want ntm", result.Project.Slug)
	}
	if result.Agent.Name != "BlueLake" {
		t.Errorf("agent name = %q, want BlueLake", result.Agent.Name)
	}
}

func TestPrepareThread(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"macro_prepare_thread": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return PrepareThreadResult{
				Agent: &Agent{ID: 5, Name: "BlueLake"},
				ThreadSummary: &ThreadSummary{
					ThreadID:    args["thread_id"].(string),
					KeyPoints:   []string{"point1"},
					ActionItems: []string{"item1"},
				},
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	trueVal := true
	falseVal := false
	result, err := c.PrepareThread(context.Background(), PrepareThreadOptions{
		ProjectKey:         "/test",
		ThreadID:           "TKT-99",
		Program:            "claude-code",
		Model:              "opus-4.5",
		AgentName:          "BlueLake",
		TaskDescription:    "testing",
		LLMModel:           "opus-4.5",
		InboxLimit:         5,
		IncludeExamples:    &trueVal,
		IncludeInboxBodies: &falseVal,
		LLMMode:            &trueVal,
		RegisterIfMissing:  &trueVal,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ThreadSummary.ThreadID != "TKT-99" {
		t.Errorf("thread_id = %q, want TKT-99", result.ThreadSummary.ThreadID)
	}
}

func TestContactHandshake(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"macro_contact_handshake": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return ContactHandshakeResult{
				Agent:         &Agent{ID: 5, Name: "BlueLake"},
				ContactStatus: "approved",
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	result, err := c.ContactHandshake(context.Background(), ContactHandshakeOptions{
		ProjectKey:      "/test",
		AgentName:       "TestAgent",
		ToAgent:         "BlueLake",
		ToProject:       "/other",
		Reason:          "coordination",
		Program:         "claude-code",
		Model:           "opus-4.5",
		TaskDescription: "testing",
		AutoAccept:      true,
		WelcomeSubject:  "Hello!",
		WelcomeBody:     "Hi there",
		TTLSeconds:      7200,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ContactStatus != "approved" {
		t.Errorf("status = %q, want approved", result.ContactStatus)
	}
}

func TestSendOverseerMessage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/overseer/send") {
			// Handle MCP requests normally (not used here)
			w.WriteHeader(http.StatusNotFound)
			return
		}

		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(OverseerSendResult{
			Success:    true,
			MessageID:  300,
			Recipients: []string{"BlueLake"},
		})
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/mcp/"))
	result, err := c.SendOverseerMessage(context.Background(), OverseerMessageOptions{
		ProjectSlug: "ntm",
		Recipients:  []string{"BlueLake"},
		Subject:     "Urgent task",
		BodyMD:      "Please do this",
		ThreadID:    "thread-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Error("expected success=true")
	}
	if result.MessageID != 300 {
		t.Errorf("messageID = %d, want 300", result.MessageID)
	}
}

func TestSendOverseerMessage_Unauthorized(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/mcp/"))
	_, err := c.SendOverseerMessage(context.Background(), OverseerMessageOptions{
		ProjectSlug: "ntm",
		Recipients:  []string{"BlueLake"},
		Subject:     "test",
		BodyMD:      "test",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !IsUnauthorized(err) {
		t.Errorf("expected unauthorized error, got: %v", err)
	}
}

func TestSendOverseerMessage_BadRequest(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"detail": "invalid recipients"})
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/mcp/"))
	_, err := c.SendOverseerMessage(context.Background(), OverseerMessageOptions{
		ProjectSlug: "ntm",
		Recipients:  []string{},
		Subject:     "test",
		BodyMD:      "test",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid recipients") {
		t.Errorf("expected 'invalid recipients' in error, got: %v", err)
	}
}

func TestSendOverseerMessage_BadRequestNoDetail(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("not json"))
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/mcp/"))
	_, err := c.SendOverseerMessage(context.Background(), OverseerMessageOptions{
		ProjectSlug: "ntm",
		Recipients:  []string{"x"},
		Subject:     "test",
		BodyMD:      "test",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "bad request") {
		t.Errorf("expected 'bad request' in error, got: %v", err)
	}
}

func TestSendOverseerMessage_ServerError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/mcp/"))
	_, err := c.SendOverseerMessage(context.Background(), OverseerMessageOptions{
		ProjectSlug: "ntm",
		Recipients:  []string{"x"},
		Subject:     "test",
		BodyMD:      "test",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestSendOverseerMessage_Timeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	c := NewClient(WithBaseURL(server.URL + "/mcp/"))
	_, err := c.SendOverseerMessage(ctx, OverseerMessageOptions{
		ProjectSlug: "ntm",
		Recipients:  []string{"x"},
		Subject:     "test",
		BodyMD:      "test",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestInstallPrecommitGuard(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"install_precommit_guard": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return map[string]interface{}{"installed": true}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	err := c.InstallPrecommitGuard(context.Background(), "/test", "/data/projects/ntm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUninstallPrecommitGuard(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"uninstall_precommit_guard": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return map[string]interface{}{"uninstalled": true}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	err := c.UninstallPrecommitGuard(context.Background(), "/data/projects/ntm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetMessage_NotImplemented(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	_, err := c.GetMessage(context.Background(), "/test", 42)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !IsNotImplemented(err) {
		t.Fatalf("expected not-implemented error, got %v", err)
	}
}

func TestSetContactPolicy(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"set_contact_policy": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return map[string]interface{}{"policy": args["policy"]}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	err := c.SetContactPolicy(context.Background(), "/test", "BlueLake", "open")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckConflicts(t *testing.T) {
	t.Parallel()

	mustFlexTime := func(value string) FlexTime {
		t.Helper()
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			t.Fatalf("parse time %q: %v", value, err)
		}
		return FlexTime{Time: parsed}
	}

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"list_file_reservations": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return []FileReservation{
				{
					ID:          1,
					PathPattern: "internal/**",
					AgentName:   "BlueLake",
					Exclusive:   true,
					CreatedTS:   mustFlexTime("2026-01-01T00:00:00Z"),
					ExpiresTS:   mustFlexTime("2099-01-01T00:00:00Z"),
				},
				{
					ID:          2,
					PathPattern: "internal/cli/*.go",
					AgentName:   "RedStone",
					Exclusive:   false,
					CreatedTS:   mustFlexTime("2026-01-01T00:00:00Z"),
					ExpiresTS:   mustFlexTime("2099-01-01T00:00:00Z"),
				},
				{
					ID:          3,
					PathPattern: "docs/**",
					AgentName:   "GreenLake",
					Exclusive:   false,
					CreatedTS:   mustFlexTime("2026-01-01T00:00:00Z"),
					ExpiresTS:   mustFlexTime("2099-01-01T00:00:00Z"),
				},
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	conflicts, err := c.CheckConflicts(context.Background(), "/test", []string{"internal/cli/*.go", "docs/**"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("len(conflicts) = %d, want 1", len(conflicts))
	}
	if conflicts[0].Path != "internal/cli/*.go" {
		t.Errorf("path = %q, want internal/cli/*.go", conflicts[0].Path)
	}
	if got := strings.Join(conflicts[0].Holders, ","); got != "BlueLake,RedStone" {
		t.Errorf("holders = %q, want BlueLake,RedStone", got)
	}
}

func TestForceReleaseReservation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"force_release_file_reservation": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return ForceReleaseResult{
				Success:        true,
				PreviousHolder: "BlueLake",
				PathPattern:    "a/*",
				Notified:       true,
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	result, err := c.ForceReleaseReservation(context.Background(), ForceReleaseOptions{
		ProjectKey:     "/test",
		AgentName:      "TestAgent",
		ReservationID:  1,
		Note:           "stale lock",
		NotifyPrevious: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Error("expected success=true")
	}
	if result.PreviousHolder != "BlueLake" {
		t.Errorf("previous holder = %q, want BlueLake", result.PreviousHolder)
	}
}

func TestCallToolWithTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"test_tool": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return map[string]interface{}{"ok": true}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	result, err := c.callToolWithTimeout(context.Background(), "test_tool", nil, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(result), "true") {
		t.Errorf("unexpected result: %s", string(result))
	}
}

func TestRenewReservationsWithOptions_Alias(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"renew_file_reservations": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return RenewReservationsResult{Renewed: 1}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	result, err := c.RenewReservationsWithOptions(context.Background(), RenewReservationsOptions{
		ProjectKey:    "/test",
		AgentName:     "TestAgent",
		ExtendSeconds: 900,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Renewed != 1 {
		t.Errorf("renewed = %d, want 1", result.Renewed)
	}
}

func TestListAgents_Alias(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"resource:resource://agents/%2Ftest": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return map[string]interface{}{
				"contents": []map[string]interface{}{
					{"text": `[{"id":1,"name":"BlueLake","program":"claude-code","model":"opus-4.5"}]`},
				},
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	agents, err := c.ListAgents(context.Background(), "/test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("len(agents) = %d, want 1", len(agents))
	}
	if agents[0].Name != "BlueLake" {
		t.Errorf("agent name = %q, want BlueLake", agents[0].Name)
	}
}

func TestRegisterAgent_WithOptionalFields(t *testing.T) {
	t.Parallel()

	var receivedArgs map[string]interface{}
	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"register_agent": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			receivedArgs = args
			return Agent{ID: 1, Name: "TestAgent", Program: "ntm", Model: "opus"}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	_, err := c.RegisterAgent(context.Background(), RegisterAgentOptions{
		ProjectKey:      "/test",
		Program:         "claude-code",
		Model:           "opus-4.5",
		Name:            "TestAgent",
		TaskDescription: "testing",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify optional fields were passed
	if receivedArgs["name"] != "TestAgent" {
		t.Errorf("name = %v, want TestAgent", receivedArgs["name"])
	}
	if receivedArgs["task_description"] != "testing" {
		t.Errorf("task_description = %v, want testing", receivedArgs["task_description"])
	}
}

func TestFetchInbox_WithAllOptions(t *testing.T) {
	t.Parallel()

	var receivedArgs map[string]interface{}
	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"fetch_inbox": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			receivedArgs = args
			return map[string]interface{}{
				"result": []InboxMessage{
					{ID: 1, Subject: "test", From: "BlueLake"},
				},
			}, nil
		},
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	sinceTS := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	msgs, err := c.FetchInbox(context.Background(), FetchInboxOptions{
		ProjectKey:    "/test",
		AgentName:     "TestAgent",
		UrgentOnly:    true,
		SinceTS:       &sinceTS,
		Limit:         5,
		IncludeBodies: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}

	// Verify optional args were set
	if receivedArgs["urgent_only"] != true {
		t.Errorf("urgent_only = %v, want true", receivedArgs["urgent_only"])
	}
	if receivedArgs["include_bodies"] != true {
		t.Errorf("include_bodies = %v, want true", receivedArgs["include_bodies"])
	}
	if receivedArgs["limit"] == nil {
		t.Error("expected limit to be set")
	}
	if receivedArgs["since_ts"] == nil {
		t.Error("expected since_ts to be set")
	}
}

func TestCallTool_ServerDown(t *testing.T) {
	t.Parallel()

	c := NewClient(WithBaseURL("http://localhost:1/"))
	_, err := c.callTool(context.Background(), "test_tool", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !IsServerUnavailable(err) {
		t.Errorf("expected server unavailable error, got: %v", err)
	}
}

func TestCallTool_ContextTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	c := NewClient(WithBaseURL(server.URL + "/"))
	_, err := c.callTool(ctx, "test_tool", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !IsTimeout(err) {
		t.Errorf("expected timeout error, got: %v", err)
	}
}

func TestCallTool_Non200Status(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("error"))
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	_, err := c.callTool(context.Background(), "test_tool", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCallTool_InvalidJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	_, err := c.callTool(context.Background(), "test_tool", nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

func TestCallTool_WithBearerToken(t *testing.T) {
	t.Parallel()

	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result:  json.RawMessage(`{"ok":true}`),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL+"/"), WithToken("my-secret"))
	_, err := c.callTool(context.Background(), "test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedAuth != "Bearer my-secret" {
		t.Errorf("auth = %q, want 'Bearer my-secret'", receivedAuth)
	}
}

func TestReadResource_Errors(t *testing.T) {
	t.Parallel()

	t.Run("unauthorized", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		c := NewClient(WithBaseURL(server.URL + "/"))
		_, err := c.ReadResource(context.Background(), "resource://agents/test")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !IsUnauthorized(err) {
			t.Errorf("expected unauthorized, got: %v", err)
		}
	})

	t.Run("server_error", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("error"))
		}))
		defer server.Close()

		c := NewClient(WithBaseURL(server.URL + "/"))
		_, err := c.ReadResource(context.Background(), "resource://agents/test")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond)
		}))
		defer server.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()

		c := NewClient(WithBaseURL(server.URL + "/"))
		_, err := c.ReadResource(ctx, "resource://agents/test")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !IsTimeout(err) {
			t.Errorf("expected timeout, got: %v", err)
		}
	})

	t.Run("json_rpc_error", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      1,
				Error:   &JSONRPCError{Code: -32000, Message: "resource unavailable"},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		c := NewClient(WithBaseURL(server.URL + "/"))
		_, err := c.ReadResource(context.Background(), "resource://agents/test")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestNewClient_EnvVars(t *testing.T) {
	// Test env var fallback for base URL
	t.Setenv("AGENT_MAIL_URL", "http://envhost:9999/mcp/")
	t.Setenv("AGENT_MAIL_TOKEN", "env-token")

	c := NewClient()
	if c.baseURL != "http://envhost:9999/mcp/" {
		t.Errorf("baseURL = %q, want http://envhost:9999/mcp/", c.baseURL)
	}
	if c.bearerToken != "env-token" {
		t.Errorf("bearerToken = %q, want env-token", c.bearerToken)
	}
}

func TestNewClient_EnvURLNoTrailingSlash(t *testing.T) {
	t.Setenv("AGENT_MAIL_URL", "http://envhost:9999/mcp")
	t.Setenv("AGENT_MAIL_TOKEN", "")

	c := NewClient()
	if c.baseURL != "http://envhost:9999/mcp/" {
		t.Errorf("baseURL = %q, want http://envhost:9999/mcp/", c.baseURL)
	}
}

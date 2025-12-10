package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

type mailStub struct {
	server       *httptest.Server
	inbox        []agentmail.InboxMessage
	fetchCalls   []fetchCall
	readIDs      []int
	ackIDs       []int
	readAgents   []string
	ackAgents    []string
	ensureCalled int
	failIDs      map[int]string // messageID -> error message
}

type fetchCall struct {
	Agent   string
	Limit   int
	Urgent  bool
	From    string
	Project string
}

func newMailStub(t *testing.T, inbox []agentmail.InboxMessage) *mailStub {
	t.Helper()
	stub := &mailStub{inbox: inbox, failIDs: make(map[int]string)}

	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}

		var rpc agentmail.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		params, ok := rpc.Params.(map[string]interface{})
		if !ok {
			http.Error(w, "invalid params", http.StatusBadRequest)
			return
		}

		name, _ := params["name"].(string)
		args, _ := params["arguments"].(map[string]interface{})

		writeResponse := func(result interface{}) {
			resp := agentmail.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      rpc.ID,
				Result:  mustMarshalRaw(t, result),
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(&resp); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}

		writeError := func(w http.ResponseWriter, id interface{}, code int, msg string) {
			resp := agentmail.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      id,
				Error: &agentmail.JSONRPCError{
					Code:    code,
					Message: msg,
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(&resp)
		}

		switch name {
		case "ensure_project":
			stub.ensureCalled++
			project := map[string]interface{}{
				"id":        1,
				"slug":      "stub",
				"human_key": args["human_key"],
			}
			writeResponse(project)
		case "fetch_inbox":
			call := fetchCall{
				Agent:   toString(args["agent_name"]),
				Project: toString(args["project_key"]),
				Urgent:  toBool(args["urgent_only"]),
				Limit:   toInt(args["limit"]),
			}
			stub.fetchCalls = append(stub.fetchCalls, call)
			messages := stub.inbox
			if call.Urgent {
				filtered := make([]agentmail.InboxMessage, 0, len(messages))
				for _, m := range messages {
					if m.Importance == "urgent" {
						filtered = append(filtered, m)
					}
				}
				messages = filtered
			}
			writeResponse(map[string]interface{}{"result": messages})
		case "mark_message_read":
			id := toInt(args["message_id"])
			stub.readIDs = append(stub.readIDs, id)
			stub.readAgents = append(stub.readAgents, toString(args["agent_name"]))
			if msg, ok := stub.failIDs[id]; ok {
				writeError(w, rpc.ID, -32000, msg)
				return
			}
			writeResponse(map[string]interface{}{})
		case "acknowledge_message":
			id := toInt(args["message_id"])
			stub.ackIDs = append(stub.ackIDs, id)
			stub.ackAgents = append(stub.ackAgents, toString(args["agent_name"]))
			if msg, ok := stub.failIDs[id]; ok {
				writeError(w, rpc.ID, -32000, msg)
				return
			}
			writeResponse(map[string]interface{}{})
		default:
			http.Error(w, "unknown tool "+name, http.StatusNotFound)
		}
	}))

	return stub
}

func (s *mailStub) Close() {
	if s.server != nil {
		s.server.Close()
	}
}

func mustMarshalRaw(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func toInt(v interface{}) int {
	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	default:
		return 0
	}
}

func toBool(v interface{}) bool {
	val, _ := v.(bool)
	return val
}

func toString(v interface{}) string {
	val, _ := v.(string)
	return val
}

func execCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	resetFlags()
	rootCmd.SetArgs(args)
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	err := rootCmd.Execute()
	return buf.String(), err
}

func TestMailMarkRequiresSelector(t *testing.T) {
	inbox := []agentmail.InboxMessage{}
	stub := newMailStub(t, inbox)
	defer stub.Close()

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Setenv("AGENT_NAME", "EnvAgent")

	_, err := execCommand(t, "mail", "read", "mysession", "--agent", "EnvAgent")
	if err == nil {
		t.Fatalf("expected error when no ids/filters/all provided")
	}
}

func TestMailMarkRequiresAgentOrEnv(t *testing.T) {
	inbox := []agentmail.InboxMessage{}
	stub := newMailStub(t, inbox)
	defer stub.Close()

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")

	_, err := execCommand(t, "mail", "ack", "mysession", "5")
	if err == nil {
		t.Fatalf("expected error when agent is missing")
	}
}

func TestMailAckUsesEnvAgent(t *testing.T) {
	inbox := []agentmail.InboxMessage{}
	stub := newMailStub(t, inbox)
	defer stub.Close()

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Setenv("AGENT_NAME", "EnvAgent")

	if _, err := execCommand(t, "mail", "ack", "mysession", "42", "--json"); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if len(stub.ackIDs) != 1 || stub.ackIDs[0] != 42 {
		t.Fatalf("expected ack of id 42, got %v", stub.ackIDs)
	}
	if len(stub.ackAgents) != 1 || stub.ackAgents[0] != "EnvAgent" {
		t.Fatalf("expected agent EnvAgent, got %v", stub.ackAgents)
	}
}

func TestMailMarkReportsErrorsInJSON(t *testing.T) {
	inbox := []agentmail.InboxMessage{}
	stub := newMailStub(t, inbox)
	stub.failIDs[99] = "already acknowledged"
	defer stub.Close()

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Setenv("AGENT_NAME", "EnvAgent")

	out, err := execCommand(t, "mail", "ack", "mysession", "99", "--json")
	if err != nil {
		t.Fatalf("expected command to finish with JSON summary, got error: %v", err)
	}
	if !strings.Contains(out, `"errors": 1`) {
		t.Fatalf("expected JSON summary to report errors, got: %s", out)
	}
}

func TestMailReadWithFilters(t *testing.T) {
	inbox := []agentmail.InboxMessage{
		{ID: 1, From: "BlueBear", Importance: "urgent", CreatedTS: time.Now()},
		{ID: 2, From: "LilacDog", Importance: "urgent", CreatedTS: time.Now()},
		{ID: 3, From: "BlueBear", Importance: "normal", CreatedTS: time.Now()},
	}
	stub := newMailStub(t, inbox)
	defer stub.Close()

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")

	if _, err := execCommand(t, "mail", "read", "mysession", "--agent", "TestAgent", "--urgent", "--from", "BlueBear"); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if len(stub.readIDs) != 1 {
		t.Fatalf("expected 1 message marked, got %d", len(stub.readIDs))
	}
	if stub.readIDs[0] != 1 {
		t.Fatalf("unexpected ids: %v", stub.readIDs)
	}
	if len(stub.fetchCalls) != 1 || !stub.fetchCalls[0].Urgent {
		t.Fatalf("expected urgent fetch, got %+v", stub.fetchCalls)
	}
}

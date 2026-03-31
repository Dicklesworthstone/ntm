package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/startup"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

type mailStub struct {
	server             *httptest.Server
	inbox              []agentmail.InboxMessage
	listAgents         []agentmail.Agent
	reservations       []agentmail.FileReservation
	fetchCalls         []fetchCall
	listCalls          []listCall
	reserveCalls       []reserveCall
	readIDs            []int
	ackIDs             []int
	readAgents         []string
	ackAgents          []string
	ensureCalled       int
	ensureProjectKeys  []string
	overseerCalls      []overseerCall
	releaseCalls       []releaseCall
	renewCalls         []renewCall
	forceReleaseCalls  []forceReleaseCall
	releaseResult      agentmail.ReleaseReservationsResult
	renewResult        agentmail.RenewReservationsResult
	forceReleaseResult agentmail.ForceReleaseResult
	failIDs            map[int]string // messageID -> error message
}

type fetchCall struct {
	Agent   string
	Limit   int
	Urgent  bool
	From    string
	Project string
}

type releaseCall struct {
	Agent   string
	Project string
	Paths   []string
	IDs     []int
}

type listCall struct {
	Project string
	URI     string
}

type reserveCall struct {
	Agent     string
	Project   string
	Paths     []string
	TTL       int
	Exclusive bool
	Reason    string
}

type renewCall struct {
	Agent         string
	Project       string
	ExtendSeconds int
	Paths         []string
	IDs           []int
}

type forceReleaseCall struct {
	Agent          string
	Project        string
	ReservationID  int
	Note           string
	NotifyPrevious bool
}

type overseerCall struct {
	Recipients []string `json:"recipients"`
	Subject    string   `json:"subject"`
	BodyMD     string   `json:"body_md"`
	ThreadID   string   `json:"thread_id,omitempty"`
}

func newMailStub(t *testing.T, inbox []agentmail.InboxMessage) *mailStub {
	t.Helper()
	stub := &mailStub{
		inbox:              inbox,
		listAgents:         []agentmail.Agent{{Name: "BlueLake"}},
		failIDs:            make(map[int]string),
		releaseResult:      agentmail.ReleaseReservationsResult{Released: 1},
		renewResult:        agentmail.RenewReservationsResult{Renewed: 1},
		forceReleaseResult: agentmail.ForceReleaseResult{Success: true},
	}

	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/health/liveness" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}

		if r.Method == http.MethodPost && r.URL.Path == "/mail/stub/overseer/send" {
			var call overseerCall
			if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			stub.overseerCalls = append(stub.overseerCalls, call)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success":    true,
				"message_id": 123,
				"recipients": call.Recipients,
				"sent_at":    "2026-02-01T00:00:00Z",
			})
			return
		}

		var rpc agentmail.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

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

		if rpc.Method == "resources/read" {
			params, _ := rpc.Params.(map[string]interface{})
			uri := toString(params["uri"])
			projectKey := ""
			resourceText := ""
			if strings.HasPrefix(uri, "resource://file_reservations/") {
				projectKey = strings.TrimPrefix(uri, "resource://file_reservations/")
				if idx := strings.Index(projectKey, "?"); idx >= 0 {
					projectKey = projectKey[:idx]
				}
				if decoded, err := url.PathUnescape(projectKey); err == nil {
					projectKey = filepath.Clean(decoded)
				}
				rows := make([]map[string]interface{}, 0, len(stub.reservations))
				for _, reservation := range stub.reservations {
					rows = append(rows, map[string]interface{}{
						"id":           reservation.ID,
						"agent":        reservation.AgentName,
						"agent_name":   reservation.AgentName,
						"path_pattern": reservation.PathPattern,
						"exclusive":    reservation.Exclusive,
						"reason":       reservation.Reason,
						"expires_ts":   reservation.ExpiresTS,
						"created_ts":   reservation.CreatedTS,
					})
				}
				resourceText = mustJSONString(t, rows)
			}
			if strings.HasPrefix(uri, "resource://agents/") {
				projectKey = strings.TrimPrefix(uri, "resource://agents/")
				if idx := strings.Index(projectKey, "?"); idx >= 0 {
					projectKey = projectKey[:idx]
				}
				if decoded, err := url.PathUnescape(projectKey); err == nil {
					projectKey = filepath.Clean(decoded)
				}
				resourceText = mustJSONString(t, map[string]interface{}{
					"agents": stub.listAgents,
				})
			}
			stub.listCalls = append(stub.listCalls, listCall{Project: projectKey, URI: uri})
			writeResponse(map[string]interface{}{
				"contents": []map[string]interface{}{
					{
						"text": resourceText,
					},
				},
			})
			return
		}

		params, ok := rpc.Params.(map[string]interface{})
		if !ok {
			http.Error(w, "invalid params", http.StatusBadRequest)
			return
		}

		name, _ := params["name"].(string)
		args, _ := params["arguments"].(map[string]interface{})

		switch name {
		case "health_check":
			writeResponse(map[string]interface{}{"status": "ok"})
		case "ensure_project":
			stub.ensureCalled++
			stub.ensureProjectKeys = append(stub.ensureProjectKeys, toString(args["human_key"]))
			project := map[string]interface{}{
				"id":        1,
				"slug":      "stub",
				"human_key": args["human_key"],
			}
			writeResponse(project)
		case "list_agents":
			writeResponse(stub.listAgents)
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
		case "release_file_reservations":
			stub.releaseCalls = append(stub.releaseCalls, releaseCall{
				Agent:   toString(args["agent_name"]),
				Project: toString(args["project_key"]),
				Paths:   toStringSlice(args["paths"]),
				IDs:     toIntSlice(args["file_reservation_ids"]),
			})
			writeResponse(stub.releaseResult)
		case "file_reservation_paths":
			call := reserveCall{
				Agent:     toString(args["agent_name"]),
				Project:   toString(args["project_key"]),
				Paths:     toStringSlice(args["paths"]),
				TTL:       toInt(args["ttl_seconds"]),
				Exclusive: toBool(args["exclusive"]),
				Reason:    toString(args["reason"]),
			}
			stub.reserveCalls = append(stub.reserveCalls, call)
			granted := make([]map[string]interface{}, 0, len(call.Paths))
			for i, path := range call.Paths {
				granted = append(granted, map[string]interface{}{
					"id":           i + 1,
					"path_pattern": path,
					"agent_name":   call.Agent,
					"project_id":   1,
					"exclusive":    call.Exclusive,
					"reason":       call.Reason,
					"expires_ts":   "2026-02-01T01:00:00Z",
					"created_ts":   "2026-02-01T00:00:00Z",
				})
			}
			writeResponse(map[string]interface{}{
				"granted":   granted,
				"conflicts": []map[string]interface{}{},
			})
		case "renew_file_reservations":
			stub.renewCalls = append(stub.renewCalls, renewCall{
				Agent:         toString(args["agent_name"]),
				Project:       toString(args["project_key"]),
				ExtendSeconds: toInt(args["extend_seconds"]),
				Paths:         toStringSlice(args["paths"]),
				IDs:           toIntSlice(args["file_reservation_ids"]),
			})
			writeResponse(stub.renewResult)
		case "force_release_file_reservation":
			stub.forceReleaseCalls = append(stub.forceReleaseCalls, forceReleaseCall{
				Agent:          toString(args["agent_name"]),
				Project:        toString(args["project_key"]),
				ReservationID:  toInt(args["file_reservation_id"]),
				Note:           toString(args["note"]),
				NotifyPrevious: toBool(args["notify_previous"]),
			})
			writeResponse(stub.forceReleaseResult)
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

func (s *mailStub) IsAvailable() bool {
	return true
}

func (s *mailStub) ListProjectAgents(ctx context.Context, projectKey string) ([]agentmail.Agent, error) {
	return append([]agentmail.Agent(nil), s.listAgents...), nil
}

func (s *mailStub) FetchInbox(ctx context.Context, opts agentmail.FetchInboxOptions) ([]agentmail.InboxMessage, error) {
	call := fetchCall{
		Agent:   opts.AgentName,
		Project: opts.ProjectKey,
		Urgent:  opts.UrgentOnly,
		Limit:   opts.Limit,
	}
	s.fetchCalls = append(s.fetchCalls, call)

	messages := append([]agentmail.InboxMessage(nil), s.inbox...)
	if call.Urgent {
		filtered := make([]agentmail.InboxMessage, 0, len(messages))
		for _, m := range messages {
			if m.Importance == "urgent" {
				filtered = append(filtered, m)
			}
		}
		messages = filtered
	}
	return messages, nil
}

func mustMarshalRaw(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func mustJSONString(t *testing.T, v interface{}) string {
	t.Helper()
	return string(mustMarshalRaw(t, v))
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

func toStringSlice(v interface{}) []string {
	raw, ok := v.([]interface{})
	if !ok {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if str, ok := item.(string); ok {
			result = append(result, str)
		}
	}
	return result
}

func toIntSlice(v interface{}) []int {
	raw, ok := v.([]interface{})
	if !ok {
		return nil
	}
	result := make([]int, 0, len(raw))
	for _, item := range raw {
		result = append(result, toInt(item))
	}
	return result
}

func saveSessionAgentForTest(t *testing.T, session, projectKey, agentName string) {
	t.Helper()
	now := time.Now()
	info := &agentmail.SessionAgentInfo{
		AgentName:    agentName,
		ProjectKey:   projectKey,
		RegisteredAt: now,
		LastActiveAt: now,
	}
	if err := agentmail.SaveSessionAgent(session, projectKey, info); err != nil {
		t.Fatalf("save session agent: %v", err)
	}
}

func execCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	resetFlags()
	// Reset startup config cache so AGENT_MAIL_URL env var is picked up
	// when config is re-loaded during command execution.
	startup.ResetConfig()
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

func TestMailSendOverseer_RedactModeScrubsBodyAndSubject(t *testing.T) {
	stub := newMailStub(t, nil)
	defer stub.Close()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")

	_, err := execCommand(t, "--redact=redact", "mail", "send", "mysession", "--to", "BlueLake", "prefix password=hunter2hunter2 suffix")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(stub.overseerCalls) != 1 {
		t.Fatalf("expected 1 overseer call, got %d", len(stub.overseerCalls))
	}
	call := stub.overseerCalls[0]
	if strings.Contains(call.BodyMD, "hunter2hunter2") {
		t.Fatalf("expected body to be redacted, got %q", call.BodyMD)
	}
	if !strings.Contains(call.BodyMD, "[REDACTED:PASSWORD:") {
		t.Fatalf("expected redaction placeholder in body, got %q", call.BodyMD)
	}
	if strings.Contains(call.Subject, "hunter2hunter2") {
		t.Fatalf("expected subject to be redacted, got %q", call.Subject)
	}
}

func TestMailSendOverseer_BlockModeRefusesBeforeSend(t *testing.T) {
	stub := newMailStub(t, nil)
	defer stub.Close()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")

	_, err := execCommand(t, "--redact=block", "mail", "send", "mysession", "--to", "BlueLake", "password=hunter2hunter2")
	if err == nil {
		t.Fatalf("expected error")
	}
	if len(stub.overseerCalls) != 0 {
		t.Fatalf("expected no overseer calls when blocked, got %d", len(stub.overseerCalls))
	}
}

func TestMailSendOverseer_WarnModeSendsUnmodifiedButWarns(t *testing.T) {
	stub := newMailStub(t, nil)
	defer stub.Close()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")

	out, err := execCommand(t, "mail", "send", "mysession", "--to", "BlueLake", "password=hunter2hunter2")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(stub.overseerCalls) != 1 {
		t.Fatalf("expected 1 overseer call, got %d", len(stub.overseerCalls))
	}
	call := stub.overseerCalls[0]
	if !strings.Contains(call.BodyMD, "hunter2hunter2") {
		t.Fatalf("expected body to be unmodified in warn mode, got %q", call.BodyMD)
	}
	if !strings.Contains(out, "Warning: detected") {
		t.Fatalf("expected warning output, got %q", out)
	}
}

func TestMailMarkJSONPartialSuccess(t *testing.T) {
	inbox := []agentmail.InboxMessage{}
	stub := newMailStub(t, inbox)
	stub.failIDs[7] = "already read"
	defer stub.Close()

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Setenv("AGENT_NAME", "EnvAgent")

	out, err := execCommand(t, "mail", "read", "mysession", "7", "8", "--json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dec := json.NewDecoder(strings.NewReader(out))
	var summary markSummary
	if err := dec.Decode(&summary); err != nil {
		t.Fatalf("decode summary: %v (out=%s)", err, out)
	}
	if summary.Processed != 1 || summary.Errors != 1 || summary.Skipped != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if len(summary.IDs) != 2 || summary.IDs[0] != 7 || summary.IDs[1] != 8 {
		t.Fatalf("unexpected ids: %+v", summary.IDs)
	}
}

func TestMailReadWithFilters(t *testing.T) {
	inbox := []agentmail.InboxMessage{
		{ID: 1, From: "BlueBear", Importance: "urgent", CreatedTS: agentmail.FlexTime{Time: time.Now()}},
		{ID: 2, From: "LilacDog", Importance: "urgent", CreatedTS: agentmail.FlexTime{Time: time.Now()}},
		{ID: 3, From: "BlueBear", Importance: "normal", CreatedTS: agentmail.FlexTime{Time: time.Now()}},
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

func TestMailReadWithAllSkipsAlreadyReadMessages(t *testing.T) {
	readAt := &agentmail.FlexTime{Time: time.Now()}
	inbox := []agentmail.InboxMessage{
		{ID: 1, From: "BlueBear", Importance: "urgent", CreatedTS: agentmail.FlexTime{Time: time.Now()}, ReadAt: readAt},
		{ID: 2, From: "BlueBear", Importance: "normal", CreatedTS: agentmail.FlexTime{Time: time.Now()}},
	}
	stub := newMailStub(t, inbox)
	defer stub.Close()

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	if err := runMailMark(cmd, "mysession", "TestAgent", mailActionRead, nil, false, "", true, 50); err != nil {
		t.Fatalf("runMailMark: %v", err)
	}

	if len(stub.readIDs) != 1 || stub.readIDs[0] != 2 {
		t.Fatalf("expected only unread message to be marked read, got %v", stub.readIDs)
	}
}

func TestMailAckWithAllSkipsMessagesWithoutAckRequirement(t *testing.T) {
	inbox := []agentmail.InboxMessage{
		{ID: 1, From: "BlueBear", Importance: "urgent", CreatedTS: agentmail.FlexTime{Time: time.Now()}},
		{ID: 2, From: "BlueBear", Importance: "normal", CreatedTS: agentmail.FlexTime{Time: time.Now()}, AckRequired: true},
	}
	stub := newMailStub(t, inbox)
	defer stub.Close()

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	if err := runMailMark(cmd, "mysession", "TestAgent", mailActionAck, nil, false, "", true, 50); err != nil {
		t.Fatalf("runMailMark: %v", err)
	}

	if len(stub.ackIDs) != 1 || stub.ackIDs[0] != 2 {
		t.Fatalf("expected only ack-required message to be acknowledged, got %v", stub.ackIDs)
	}
}

func TestRunUnlockErrorsOnZeroSpecificRelease(t *testing.T) {
	resetFlags()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	projectKey := GetProjectRoot()
	session := "unlock-zero"
	agentName := "BlueLake"
	saveSessionAgentForTest(t, session, projectKey, agentName)

	stub := newMailStub(t, nil)
	stub.releaseResult = agentmail.ReleaseReservationsResult{Released: 0}
	defer stub.Close()

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")

	err := runUnlock(session, []string{"internal/cli/*.go"}, false)
	if err == nil {
		t.Fatal("expected unlock to fail when no requested reservations were released")
	}
	if !strings.Contains(err.Error(), "released 0 reservations") {
		t.Fatalf("expected zero-release error, got %v", err)
	}
	if len(stub.releaseCalls) != 1 {
		t.Fatalf("expected one release call, got %d", len(stub.releaseCalls))
	}
	if got := stub.releaseCalls[0].Paths; len(got) != 1 || got[0] != "internal/cli/*.go" {
		t.Fatalf("expected release call for requested pattern, got %v", got)
	}
}

func TestRunRenewLocksUsesProjectRootFromSubdir(t *testing.T) {
	resetFlags()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	projectKey := GetProjectRoot()
	session := "renew-root"
	agentName := "GreenLake"
	saveSessionAgentForTest(t, session, projectKey, agentName)

	stub := newMailStub(t, nil)
	stub.renewResult = agentmail.RenewReservationsResult{Renewed: 2}
	defer stub.Close()

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Chdir(filepath.Join(projectKey, "internal"))

	if err := runRenewLocks(session, 30); err != nil {
		t.Fatalf("runRenewLocks: %v", err)
	}
	if len(stub.renewCalls) != 1 {
		t.Fatalf("expected one renew call, got %d", len(stub.renewCalls))
	}
	if stub.renewCalls[0].Project != projectKey {
		t.Fatalf("expected renew project %q, got %q", projectKey, stub.renewCalls[0].Project)
	}
}

func TestRunRenewLocksErrorsOnZeroRenewed(t *testing.T) {
	resetFlags()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	projectKey := GetProjectRoot()
	session := "renew-zero"
	agentName := "RedStone"
	saveSessionAgentForTest(t, session, projectKey, agentName)

	stub := newMailStub(t, nil)
	stub.renewResult = agentmail.RenewReservationsResult{Renewed: 0}
	defer stub.Close()

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")

	err := runRenewLocks(session, 30)
	if err == nil {
		t.Fatal("expected renew to fail when no reservations were renewed")
	}
	if !strings.Contains(err.Error(), "no active reservations were renewed") {
		t.Fatalf("expected zero-renew error, got %v", err)
	}
}

func TestMailInboxUsesProjectRootFromSubdir(t *testing.T) {
	resetFlags()
	t.Setenv("AGENT_MAIL_URL", "")
	stub := newMailStub(t, []agentmail.InboxMessage{
		{ID: 7, Subject: "Inbox subject", From: "BlueLake", CreatedTS: agentmail.FlexTime{Time: time.Now()}},
	})
	defer stub.Close()

	projectKey := GetProjectRoot()
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Chdir(filepath.Join(projectKey, "internal"))

	if _, err := execCommand(t, "mail", "inbox", "--json"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(stub.fetchCalls) != 1 {
		t.Fatalf("expected one fetch call, got %d", len(stub.fetchCalls))
	}
	if stub.fetchCalls[0].Project != projectKey {
		t.Fatalf("expected inbox project %q, got %q", projectKey, stub.fetchCalls[0].Project)
	}
}

func TestMailReadUsesProjectRootFromSubdir(t *testing.T) {
	resetFlags()
	stub := newMailStub(t, []agentmail.InboxMessage{
		{ID: 9, Subject: "Read me", From: "BlueLake", CreatedTS: agentmail.FlexTime{Time: time.Now()}},
	})
	defer stub.Close()

	projectKey := GetProjectRoot()
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Chdir(filepath.Join(projectKey, "internal"))

	if _, err := execCommand(t, "mail", "read", "mysession", "--agent", "BlueLake", "--all"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(stub.fetchCalls) != 1 {
		t.Fatalf("expected one fetch call, got %d", len(stub.fetchCalls))
	}
	if stub.fetchCalls[0].Project != projectKey {
		t.Fatalf("expected read project %q, got %q", projectKey, stub.fetchCalls[0].Project)
	}
	if len(stub.ensureProjectKeys) != 1 || stub.ensureProjectKeys[0] != projectKey {
		t.Fatalf("expected ensure_project for %q, got %v", projectKey, stub.ensureProjectKeys)
	}
}

func TestRunMailInboxUsesSessionProjectDir(t *testing.T) {
	stub := newMailStub(t, []agentmail.InboxMessage{
		{ID: 11, Subject: "Scoped inbox", From: "BlueLake", CreatedTS: agentmail.FlexTime{Time: time.Now()}},
	})
	defer stub.Close()

	projectsBase := t.TempDir()
	projectKey := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(projectKey, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Chdir(t.TempDir())

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := runMailInbox(cmd, stub, "mysession", false, "", false, 50, true); err != nil {
		t.Fatalf("runMailInbox() error = %v", err)
	}
	if len(stub.fetchCalls) != 1 {
		t.Fatalf("expected one fetch call, got %d", len(stub.fetchCalls))
	}
	if stub.fetchCalls[0].Project != projectKey {
		t.Fatalf("expected inbox project %q, got %q", projectKey, stub.fetchCalls[0].Project)
	}
}

func TestRunMailInboxSanitizesDisplayFields(t *testing.T) {
	stub := newMailStub(t, []agentmail.InboxMessage{
		{
			ID:         12,
			Subject:    "Build failed\n\x1b[31mspoofed\x1b[0m",
			From:       "Mallory\r\nTeam",
			Importance: "high",
			CreatedTS:  agentmail.FlexTime{Time: time.Now()},
		},
	})
	stub.listAgents = []agentmail.Agent{{Name: "Blue\tLake"}}
	defer stub.Close()

	projectKey := GetProjectRoot()
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Chdir(projectKey)

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := runMailInbox(cmd, stub, "", false, "", false, 50, false); err != nil {
		t.Fatalf("runMailInbox() error = %v", err)
	}

	rendered := out.String()
	if strings.Contains(rendered, "\x1b[31m") {
		t.Fatalf("rendered output still contains ANSI escape: %q", rendered)
	}
	if strings.Contains(rendered, "Build failed\nspoofed") {
		t.Fatalf("rendered output still contains injected newline: %q", rendered)
	}
	if strings.Contains(rendered, "Mallory\r\nTeam") {
		t.Fatalf("rendered output still contains raw CRLF sender: %q", rendered)
	}
	if !strings.Contains(rendered, "[URGENT] Build failed spoofed") {
		t.Fatalf("expected sanitized subject, got %q", rendered)
	}
	if !strings.Contains(rendered, "Mallory Team → Blue Lake") {
		t.Fatalf("expected sanitized sender/recipient line, got %q", rendered)
	}
}

func TestRunMailMarkUsesSessionProjectDir(t *testing.T) {
	stub := newMailStub(t, nil)
	defer stub.Close()

	projectsBase := t.TempDir()
	projectKey := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(projectKey, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Chdir(t.TempDir())

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := runMailMark(cmd, "mysession", "BlueLake", mailActionRead, []int{42}, false, "", false, 50); err != nil {
		t.Fatalf("runMailMark() error = %v", err)
	}
	if len(stub.ensureProjectKeys) != 1 || stub.ensureProjectKeys[0] != projectKey {
		t.Fatalf("expected ensure_project for %q, got %v", projectKey, stub.ensureProjectKeys)
	}
}

func TestRunMailSendOverseerUsesSessionProjectDir(t *testing.T) {
	stub := newMailStub(t, nil)
	defer stub.Close()

	projectsBase := t.TempDir()
	projectKey := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(projectKey, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Chdir(t.TempDir())

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := runMailSendOverseer(cmd, "mysession", []string{"BlueLake"}, "Subject", "Body", "", false); err != nil {
		t.Fatalf("runMailSendOverseer() error = %v", err)
	}
	if len(stub.ensureProjectKeys) != 1 || stub.ensureProjectKeys[0] != projectKey {
		t.Fatalf("expected ensure_project for %q, got %v", projectKey, stub.ensureProjectKeys)
	}
}

func TestResolveAgentMailProjectKeyRejectsInvalidSessionName(t *testing.T) {
	_, err := resolveAgentMailProjectKey("../escape")
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveAgentMailProjectKeyWithPreferenceUsesCWDForInferredSession(t *testing.T) {
	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := t.TempDir()
	configProject := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(configProject, 0o755); err != nil {
		t.Fatalf("mkdir configured project: %v", err)
	}

	cwdRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir cwd repo: %v", err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatalf("chdir cwd repo: %v", err)
	}

	cfg = &config.Config{ProjectsBase: projectsBase}

	projectKey, err := resolveAgentMailProjectKeyWithPreference("mysession", false)
	if err != nil {
		t.Fatalf("resolveAgentMailProjectKeyWithPreference() error = %v", err)
	}
	if projectKey != cwdRepo {
		t.Fatalf("resolveAgentMailProjectKeyWithPreference() = %q, want cwd repo %q", projectKey, cwdRepo)
	}
}

func TestResolveAgentMailScopeWithPreferenceNormalizesExplicitPrefix(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	fullSession := "mailscopeprefixsession"
	prefix := "mailscopeprefix"
	workDir := t.TempDir()
	_ = tmux.KillSession(fullSession)
	if err := tmux.CreateSession(fullSession, workDir); err != nil {
		t.Fatalf("CreateSession(%q): %v", fullSession, err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(fullSession) })

	projectsBase := t.TempDir()
	projectKey := filepath.Join(projectsBase, fullSession)
	if err := os.MkdirAll(projectKey, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	resolvedSession, resolvedProjectKey, err := resolveAgentMailScopeWithPreference(prefix, true)
	if err != nil {
		t.Fatalf("resolveAgentMailScopeWithPreference() error = %v", err)
	}
	if resolvedSession != fullSession {
		t.Fatalf("resolved session = %q, want %q", resolvedSession, fullSession)
	}
	if resolvedProjectKey != projectKey {
		t.Fatalf("resolved project key = %q, want %q", resolvedProjectKey, projectKey)
	}
}

func TestRunLockUsesSessionProjectDir(t *testing.T) {
	resetFlags()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	projectsBase := t.TempDir()
	projectKey := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(projectKey, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	session := "mysession"
	agentName := "BlueLake"
	saveSessionAgentForTest(t, session, projectKey, agentName)

	stub := newMailStub(t, nil)
	defer stub.Close()

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Chdir(t.TempDir())

	if err := runLock(session, []string{"internal/**/*.go"}, "scope test", "1h", false); err != nil {
		t.Fatalf("runLock: %v", err)
	}
	if len(stub.reserveCalls) != 1 {
		t.Fatalf("expected one reserve call, got %d", len(stub.reserveCalls))
	}
	if got := stub.reserveCalls[0].Project; got != projectKey {
		t.Fatalf("expected reserve project %q, got %q", projectKey, got)
	}
}

func TestRunLocksUsesSessionProjectDir(t *testing.T) {
	resetFlags()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	projectsBase := t.TempDir()
	projectKey := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(projectKey, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	session := "mysession"
	agentName := "BlueLake"
	saveSessionAgentForTest(t, session, projectKey, agentName)

	stub := newMailStub(t, nil)
	defer stub.Close()

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Chdir(t.TempDir())

	if err := runLocks(session, false); err != nil {
		t.Fatalf("runLocks: %v", err)
	}
	if len(stub.listCalls) != 1 {
		t.Fatalf("expected one reservation list call, got %d", len(stub.listCalls))
	}
	if got := stub.listCalls[0].Project; got != projectKey {
		t.Fatalf("expected list project %q, got %q", projectKey, got)
	}
}

func TestRunUnlockUsesSessionProjectDir(t *testing.T) {
	resetFlags()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	projectsBase := t.TempDir()
	projectKey := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(projectKey, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	session := "mysession"
	agentName := "BlueLake"
	saveSessionAgentForTest(t, session, projectKey, agentName)

	stub := newMailStub(t, nil)
	defer stub.Close()

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Chdir(t.TempDir())

	if err := runUnlock(session, []string{"internal/cli/*.go"}, false); err != nil {
		t.Fatalf("runUnlock: %v", err)
	}
	if len(stub.releaseCalls) != 1 {
		t.Fatalf("expected one release call, got %d", len(stub.releaseCalls))
	}
	if got := stub.releaseCalls[0].Project; got != projectKey {
		t.Fatalf("expected release project %q, got %q", projectKey, got)
	}
}

func TestRunForceReleaseUsesSessionProjectDir(t *testing.T) {
	resetFlags()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	projectsBase := t.TempDir()
	projectKey := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(projectKey, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	session := "mysession"
	agentName := "BlueLake"
	saveSessionAgentForTest(t, session, projectKey, agentName)

	stub := newMailStub(t, nil)
	defer stub.Close()

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Chdir(t.TempDir())

	if err := runForceRelease(session, 42, "stale", true, true); err != nil {
		t.Fatalf("runForceRelease: %v", err)
	}
	if len(stub.forceReleaseCalls) != 1 {
		t.Fatalf("expected one force-release call, got %d", len(stub.forceReleaseCalls))
	}
	if got := stub.forceReleaseCalls[0].Project; got != projectKey {
		t.Fatalf("expected force-release project %q, got %q", projectKey, got)
	}
}

func TestRunRenewLocksUsesSessionProjectDir(t *testing.T) {
	resetFlags()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	projectsBase := t.TempDir()
	projectKey := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(projectKey, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	session := "mysession"
	agentName := "BlueLake"
	saveSessionAgentForTest(t, session, projectKey, agentName)

	stub := newMailStub(t, nil)
	defer stub.Close()

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Chdir(t.TempDir())

	if err := runRenewLocks(session, 30); err != nil {
		t.Fatalf("runRenewLocks: %v", err)
	}
	if len(stub.renewCalls) != 1 {
		t.Fatalf("expected one renew call, got %d", len(stub.renewCalls))
	}
	if got := stub.renewCalls[0].Project; got != projectKey {
		t.Fatalf("expected renew project %q, got %q", projectKey, got)
	}
}

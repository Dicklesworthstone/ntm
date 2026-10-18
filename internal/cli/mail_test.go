package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
	projectSlug        string
	overseerCalls      []overseerCall
	overseerLegacy     bool
	overseerStatus     int
	overseerError      string
	overseerAuthToken  string
	overseerAuth       []string
	overseerInbox      map[string][]agentmail.InboxMessage
	ordinarySendCalls  []string
	releaseCalls       []releaseCall
	renewCalls         []renewCall
	forceReleaseCalls  []forceReleaseCall
	releaseResult      agentmail.ReleaseReservationsResult
	keepOnRelease      bool
	renewResult        agentmail.RenewReservationsResult
	forceReleaseResult agentmail.ForceReleaseResult
	failIDs            map[int]string // messageID -> error message
	afterMark          func(int)
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
	Recipients      []string `json:"recipients"`
	Subject         string   `json:"subject"`
	BodyMD          string   `json:"body_md"`
	ThreadID        string   `json:"thread_id,omitempty"`
	Intent          string   `json:"intent"`
	Reason          string   `json:"reason"`
	ConfirmMultiple bool     `json:"confirm_multiple"`
	Broadcast       bool     `json:"broadcast"`
}

type mailAvailabilityDiagnosticStub struct {
	err error
}

func (s mailAvailabilityDiagnosticStub) LastAvailabilityError() error {
	return s.err
}

type legacyMailAvailabilityStub struct{}

func TestAgentMailUnavailableErrorUsesOptionalDiagnostic(t *testing.T) {
	reason := errors.New("health endpoint returned 503")
	err := agentMailUnavailableError(
		t.Context(),
		mailAvailabilityDiagnosticStub{err: reason},
		"agent mail server not available",
	)

	if !errors.Is(err, reason) {
		t.Fatalf("agentMailUnavailableError() error = %v, want wrapped diagnostic reason", err)
	}
	if !strings.Contains(err.Error(), "reason: health endpoint returned 503") {
		t.Fatalf("agentMailUnavailableError() error = %q, want diagnostic reason", err)
	}
}

func TestAgentMailUnavailableErrorPreservesLegacyFallback(t *testing.T) {
	const fallback = "agent mail server not available"
	err := agentMailUnavailableError(t.Context(), legacyMailAvailabilityStub{}, fallback)
	if err == nil || err.Error() != fallback {
		t.Fatalf("agentMailUnavailableError() error = %v, want %q", err, fallback)
	}
}

func TestAgentMailUnavailableErrorPrioritizesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := agentMailUnavailableError(
		ctx,
		mailAvailabilityDiagnosticStub{err: errors.New("health endpoint returned 503")},
		"agent mail server not available",
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("agentMailUnavailableError() error = %v, want context.Canceled", err)
	}
	if strings.Contains(err.Error(), "health endpoint returned 503") {
		t.Fatalf("agentMailUnavailableError() error = %q, diagnostic overrode caller cancellation", err)
	}
}

func newMailStub(t *testing.T, inbox []agentmail.InboxMessage) *mailStub {
	t.Helper()
	stub := &mailStub{
		inbox:              inbox,
		listAgents:         []agentmail.Agent{{Name: "BlueLake"}},
		projectSlug:        "stub",
		failIDs:            make(map[int]string),
		releaseResult:      agentmail.ReleaseReservationsResult{Released: 1},
		renewResult:        agentmail.RenewReservationsResult{Renewed: 1},
		forceReleaseResult: agentmail.ForceReleaseResult{Success: true},
		overseerInbox:      make(map[string][]agentmail.InboxMessage),
	}

	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/health/liveness" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}

		if r.Method == http.MethodPost && r.URL.Path == "/mail/stub/overseer/send" {
			w.Header().Set("Content-Type", "application/json")
			writeOverseerError := func(status int, message string) {
				field := "error"
				if stub.overseerLegacy {
					field = "detail"
				}
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]string{field: message})
			}
			stub.overseerAuth = append(stub.overseerAuth, r.Header.Get("Authorization"))
			if stub.overseerAuthToken != "" && r.Header.Get("Authorization") != "Bearer "+stub.overseerAuthToken {
				writeOverseerError(http.StatusUnauthorized, "A valid bearer token is required for human overseer interventions")
				return
			}
			if stub.overseerStatus != 0 {
				writeOverseerError(stub.overseerStatus, stub.overseerError)
				return
			}
			var call overseerCall
			if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
				writeOverseerError(http.StatusBadRequest, err.Error())
				return
			}
			stub.overseerCalls = append(stub.overseerCalls, call)
			if !stub.overseerLegacy {
				if call.Intent != "human_overseer_send" {
					writeOverseerError(http.StatusBadRequest, "intent must be human_overseer_send")
					return
				}
				if strings.TrimSpace(call.Reason) == "" || utf8.RuneCountInString(call.Reason) > 500 {
					writeOverseerError(http.StatusBadRequest, "reason must contain 1-500 characters")
					return
				}
				if len(call.Recipients) > 1 && !call.ConfirmMultiple {
					writeOverseerError(http.StatusBadRequest, "confirm_multiple is required for multiple recipients")
					return
				}
				if call.Broadcast {
					writeOverseerError(http.StatusBadRequest, "broadcast is not supported; choose explicit recipients")
					return
				}
			}
			if len(call.Recipients) == 0 || strings.TrimSpace(call.Subject) == "" || strings.TrimSpace(call.BodyMD) == "" {
				writeOverseerError(http.StatusBadRequest, "recipients, subject, and body_md are required")
				return
			}
			var deliveredRecipients []string
			for _, recipient := range call.Recipients {
				index := slices.IndexFunc(stub.listAgents, func(agent agentmail.Agent) bool {
					return strings.EqualFold(agent.Name, recipient)
				})
				if index < 0 {
					// Legacy Python silently omits unknown recipients when at
					// least one requested agent exists. The receipt must expose
					// the actual delivered set so NTM can report partial delivery.
					if stub.overseerLegacy {
						continue
					}
					writeOverseerError(http.StatusBadRequest, "Unknown recipient: "+recipient)
					return
				}
				deliveredRecipients = append(deliveredRecipients, stub.listAgents[index].Name)
			}
			if len(deliveredRecipients) == 0 {
				writeOverseerError(http.StatusBadRequest, "No valid recipients found")
				return
			}

			// The intervention route owns sender creation and adds its preamble
			// once. Persist the delivery so a subsequent CLI inbox command must
			// traverse MCP fetch_inbox and read the same message back.
			messageID := 122 + len(stub.overseerCalls)
			const sentAt = "2026-02-01T00:00:00Z"
			threadID := call.ThreadID
			for _, recipient := range deliveredRecipients {
				stub.overseerInbox[recipient] = append(stub.overseerInbox[recipient], agentmail.InboxMessage{
					ID: messageID, From: "HumanOverseer", Subject: call.Subject,
					BodyMD: "[Human Overseer]\n\n" + call.BodyMD, ThreadID: &threadID,
					Importance: "high", AckRequired: false,
					CreatedTS: agentmail.FlexTime{Time: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)},
				})
			}
			if !slices.ContainsFunc(stub.listAgents, func(agent agentmail.Agent) bool {
				return strings.EqualFold(strings.TrimSpace(agent.Name), "HumanOverseer")
			}) {
				stub.listAgents = append(stub.listAgents, agentmail.Agent{Name: "HumanOverseer"})
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success":    true,
				"message_id": messageID,
				"recipients": deliveredRecipients,
				"sent_at":    sentAt,
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
						"project_id":   reservation.ProjectID,
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
			if strings.HasPrefix(uri, "resource://project/") {
				// Read-only project identity, as served by Agent Mail's
				// resource://project/{slug} (id, slug, human_key, agents).
				// Reservation grant verification resolves ownership here.
				key := strings.TrimPrefix(uri, "resource://project/")
				if decoded, err := url.PathUnescape(key); err == nil {
					key = decoded
				}
				writeResponse(map[string]interface{}{
					"contents": []map[string]interface{}{{
						"text": mustJSONString(t, map[string]interface{}{
							"id": 1, "slug": "stub", "human_key": key, "agents": stub.listAgents,
						}),
					}},
				})
				return
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
					"agents": append([]agentmail.Agent{}, stub.listAgents...),
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
		case "register_agent", "send_message":
			stub.ordinarySendCalls = append(stub.ordinarySendCalls, name)
			if toString(args["name"]) == "HumanOverseer" || toString(args["sender_name"]) == "HumanOverseer" {
				writeError(w, rpc.ID, -32602, "HumanOverseer is reserved; use the human overseer intervention route")
			} else {
				http.Error(w, "unknown tool "+name, http.StatusNotFound)
			}
		case "ensure_project":
			stub.ensureCalled++
			stub.ensureProjectKeys = append(stub.ensureProjectKeys, toString(args["human_key"]))
			project := map[string]interface{}{
				"id":        1,
				"slug":      stub.projectSlug,
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
			messages := make([]agentmail.InboxMessage, 0, len(stub.inbox)+len(stub.overseerInbox[call.Agent]))
			messages = append(messages, stub.inbox...)
			for _, message := range stub.overseerInbox[call.Agent] {
				if !toBool(args["include_bodies"]) {
					message.BodyMD = ""
				}
				messages = append(messages, message)
			}
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
			if stub.afterMark != nil {
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				stub.afterMark(id)
			}
		case "acknowledge_message":
			id := toInt(args["message_id"])
			stub.ackIDs = append(stub.ackIDs, id)
			stub.ackAgents = append(stub.ackAgents, toString(args["agent_name"]))
			if msg, ok := stub.failIDs[id]; ok {
				writeError(w, rpc.ID, -32000, msg)
				return
			}
			writeResponse(map[string]interface{}{})
			if stub.afterMark != nil {
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				stub.afterMark(id)
			}
		case "release_file_reservations":
			call := releaseCall{
				Agent:   toString(args["agent_name"]),
				Project: toString(args["project_key"]),
				Paths:   toStringSlice(args["paths"]),
				IDs:     toIntSlice(args["file_reservation_ids"]),
			}
			stub.releaseCalls = append(stub.releaseCalls, call)
			if stub.releaseResult.Released > 0 && !stub.keepOnRelease {
				ids := make(map[int]struct{}, len(call.IDs))
				for _, id := range call.IDs {
					ids[id] = struct{}{}
				}
				paths := make(map[string]struct{}, len(call.Paths))
				for _, path := range call.Paths {
					paths[path] = struct{}{}
				}
				remaining := stub.reservations[:0]
				for _, reservation := range stub.reservations {
					_, releaseID := ids[reservation.ID]
					_, releasePath := paths[reservation.PathPattern]
					if reservation.AgentName != call.Agent || (!releaseID && !releasePath) {
						remaining = append(remaining, reservation)
					}
				}
				stub.reservations = remaining
			}
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
			// Like the real server, a grant is a live reservation row that the
			// client independently reads back before trusting the receipt.
			nextID := 1
			for _, reservation := range stub.reservations {
				if reservation.ID >= nextID {
					nextID = reservation.ID + 1
				}
			}
			granted := make([]map[string]interface{}, 0, len(call.Paths))
			for i, path := range call.Paths {
				stub.reservations = append(stub.reservations, agentmail.FileReservation{
					ID: nextID + i, ProjectID: 1, AgentName: call.Agent, PathPattern: path,
					Exclusive: call.Exclusive, Reason: call.Reason,
					CreatedTS: agentmail.FlexTime{Time: time.Date(2030, 2, 1, 0, 0, 0, 0, time.UTC)}, ExpiresTS: agentmail.FlexTime{Time: time.Date(2030, 2, 1, 1, 0, 0, 0, time.UTC)},
				})
				granted = append(granted, map[string]interface{}{
					"id":           nextID + i,
					"path_pattern": path,
					"agent_name":   call.Agent,
					"project_id":   1,
					"exclusive":    call.Exclusive,
					"reason":       call.Reason,
					"expires_ts":   "2030-02-01T01:00:00Z",
					"created_ts":   "2030-02-01T00:00:00Z",
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

func (s *mailStub) IsAvailableContext(ctx context.Context) bool {
	return ctx != nil && ctx.Err() == nil
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

func saveSessionAgentRegistryForTest(t *testing.T, session, projectKey, paneTitle, paneID, agentName string) {
	t.Helper()
	registry := agentmail.NewSessionAgentRegistry(session, projectKey)
	registry.AddAgent(paneTitle, paneID, agentName)
	if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
		t.Fatalf("save session agent registry: %v", err)
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

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Setenv("AGENT_NAME", "EnvAgent")

	_, err := execCommand(t, "mail", "read", "mysession", "--agent", "EnvAgent")
	if err == nil {
		t.Fatalf("expected error when no ids/filters/all provided")
	}
}

func TestMailMarkRequiresAgentOrEnv(t *testing.T) {
	// isolateSessionAgentStorage sets HOME *and* XDG_CONFIG_HOME, which is
	// required on macOS (os.UserConfigDir ignores XDG_CONFIG_HOME and uses
	// $HOME/Library/Application Support). Without HOME isolation, this
	// test would write/read session registries in the real user home and
	// leak state into / out of other tests in the same package.
	isolateSessionAgentStorage(t)

	inbox := []agentmail.InboxMessage{}
	stub := newMailStub(t, inbox)
	defer stub.Close()

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")

	_, err := execCommand(t, "mail", "ack", "mysession", "5")
	if err == nil {
		t.Fatalf("expected error when agent is missing")
	}
}

func TestMailAckUsesSavedSessionAgentWhenEnvMissing(t *testing.T) {
	// Isolate HOME (and XDG_CONFIG_HOME) so the session agent we save
	// here lands in a sandboxed Application Support dir on macOS. Without
	// this the registry was leaking to ~/Library/Application Support and
	// poisoning every later TestRunMail* test in the same package.
	isolateSessionAgentStorage(t)

	inbox := []agentmail.InboxMessage{}
	stub := newMailStub(t, inbox)
	defer stub.Close()

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")

	projectKey := GetProjectRoot()
	saveSessionAgentForTest(t, "mysession", projectKey, "GreenCastle")

	if _, err := execCommand(t, "mail", "ack", "mysession", "42", "--json"); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if len(stub.ackIDs) != 1 || stub.ackIDs[0] != 42 {
		t.Fatalf("expected ack of id 42, got %v", stub.ackIDs)
	}
	if len(stub.ackAgents) != 1 || stub.ackAgents[0] != "GreenCastle" {
		t.Fatalf("expected saved session agent GreenCastle, got %v", stub.ackAgents)
	}
}

func TestResolveMailAgentIdentityUsesCurrentPaneRegistryIdentity(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	isolateSessionAgentStorage(t)

	projectsBase := t.TempDir()
	projectKey := filepath.Join(projectsBase, "mailpaneidentity")
	if err := os.MkdirAll(projectKey, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	session := "mailpaneidentity"
	_ = tmux.KillSession(session)
	if err := tmux.CreateSession(session, projectKey); err != nil {
		t.Fatalf("CreateSession(%q): %v", session, err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(session) })

	panes, err := tmux.GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes(%q): %v", session, err)
	}
	if len(panes) == 0 {
		t.Fatal("expected at least one pane")
	}

	saveSessionAgentForTest(t, session, projectKey, "BlueLake")
	saveSessionAgentRegistryForTest(t, session, projectKey, "", panes[0].ID, "GreenCastle")
	t.Setenv("TMUX_PANE", panes[0].ID)
	t.Setenv("AGENT_NAME", "EnvAgent")

	got, err := resolveMailAgentIdentity(t.Context(), session, "")
	if err != nil {
		t.Fatalf("resolveMailAgentIdentity() error = %v", err)
	}
	if got != "GreenCastle" {
		t.Fatalf("resolveMailAgentIdentity() = %q, want %q", got, "GreenCastle")
	}
}

func TestMailAckUsesEnvAgent(t *testing.T) {
	isolateSessionAgentStorage(t)

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

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
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

func TestMailSendOverseerCurrentAndLegacyInboxReadback(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		serverName := "current"
		if legacy {
			serverName = "legacy"
		}
		for _, tc := range []struct {
			name       string
			flags      []string
			recipients []string
			reason     string
		}{
			{
				name: "single_default_reason", flags: []string{"--to", "BlueLake"},
				recipients: []string{"BlueLake"}, reason: agentmail.DefaultOverseerReason,
			},
			{
				name: "multiple_explicit_recipients", flags: []string{"--to", "BlueLake", "--to", "GreenCastle", "--reason", "Coordinate API rollout"},
				recipients: []string{"BlueLake", "GreenCastle"}, reason: "Coordinate API rollout",
			},
			{
				name: "all_registered_agents", flags: []string{"--all", "--reason", " Checkpoint before maintenance "},
				recipients: []string{"BlueLake", "GreenCastle"}, reason: "Checkpoint before maintenance",
			},
			{
				name: "duplicate_recipient", flags: []string{"--to", " BlueLake ", "--to", "bluelake"},
				recipients: []string{"BlueLake"}, reason: agentmail.DefaultOverseerReason,
			},
		} {
			t.Run(serverName+"/"+tc.name, func(t *testing.T) {
				isolateSessionAgentStorage(t)
				stub := newMailStub(t, nil)
				defer stub.Close()
				stub.overseerLegacy = legacy
				stub.listAgents = []agentmail.Agent{{Name: "BlueLake"}, {Name: "GreenCastle"}, {Name: "HumanOverseer"}}
				stub.overseerAuthToken = "test-overseer-token"
				t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/mcp/")
				t.Setenv("AGENT_MAIL_TOKEN", stub.overseerAuthToken)

				args := []string{"mail", "send", "mysession", "--json", "--subject", "API checkpoint", "--thread", "FEAT-355"}
				args = append(args, tc.flags...)
				args = append(args, "Please report the API rollout status.")
				out, err := execCommand(t, args...)
				if err != nil {
					t.Fatalf("mail send: %v\n%s", err, out)
				}
				var sent struct {
					Success    bool     `json:"success"`
					MessageID  int      `json:"message_id"`
					Recipients []string `json:"recipients"`
					Overseer   bool     `json:"overseer"`
				}
				if err := json.Unmarshal([]byte(out), &sent); err != nil {
					t.Fatalf("decoding send result: %v\n%s", err, out)
				}
				if !sent.Success || !sent.Overseer || sent.MessageID != 123 || !slices.Equal(sent.Recipients, tc.recipients) {
					t.Fatalf("unexpected send receipt: %+v", sent)
				}
				if len(stub.overseerCalls) != 1 || len(stub.ordinarySendCalls) != 0 {
					t.Fatalf("expected one intervention and no ordinary MCP registration/send, got intervention=%d MCP=%v", len(stub.overseerCalls), stub.ordinarySendCalls)
				}
				call := stub.overseerCalls[0]
				if call.Intent != "human_overseer_send" || call.Reason != tc.reason || call.ConfirmMultiple != (len(tc.recipients) > 1) || call.Broadcast {
					t.Fatalf("unexpected intervention audit fields: %+v", call)
				}
				if call.ThreadID != "FEAT-355" || call.BodyMD != "Please report the API rollout status." {
					t.Fatalf("thread or unadorned body changed before server delivery: %+v", call)
				}
				if !slices.Equal(stub.overseerAuth, []string{"Bearer " + stub.overseerAuthToken}) {
					t.Fatalf("intervention request did not preserve configured authorization")
				}

				inboxOut, err := execCommand(t, "mail", "inbox", "mysession", "--json")
				if err != nil {
					t.Fatalf("mail inbox after send: %v\n%s", err, inboxOut)
				}
				var inbox []aggregatedMessage
				if err := json.Unmarshal([]byte(inboxOut), &inbox); err != nil {
					t.Fatalf("decoding inbox: %v\n%s", err, inboxOut)
				}
				if len(inbox) != 1 {
					t.Fatalf("want one delivered message aggregated across recipients, got %+v", inbox)
				}
				message := inbox[0]
				if message.ID != sent.MessageID || message.From != "HumanOverseer" || message.Subject != "API checkpoint" || message.Importance != "high" || message.AckRequired {
					t.Fatalf("inbox lost overseer delivery semantics: %+v", message)
				}
				if !slices.Equal(message.Recipients, tc.recipients) {
					t.Fatalf("inbox recipients = %v, want %v", message.Recipients, tc.recipients)
				}
				if message.BodyMD != "" {
					t.Fatalf("CLI inbox requested metadata but received a body: %q", message.BodyMD)
				}
				client := newAgentMailClient(stub.ensureProjectKeys[0])
				for _, recipient := range tc.recipients {
					fullInbox, err := client.FetchInbox(t.Context(), agentmail.FetchInboxOptions{
						ProjectKey: stub.ensureProjectKeys[0], AgentName: recipient, IncludeBodies: true,
					})
					if err != nil || len(fullInbox) != 1 {
						t.Fatalf("full inbox readback for %s: messages=%+v err=%v", recipient, fullInbox, err)
					}
					fullMessage := fullInbox[0]
					if fullMessage.ID != sent.MessageID || fullMessage.ThreadID == nil || *fullMessage.ThreadID != "FEAT-355" {
						t.Fatalf("thread continuity lost for %s: %+v", recipient, fullMessage)
					}
					if strings.Count(fullMessage.BodyMD, "[Human Overseer]") != 1 || !strings.HasSuffix(fullMessage.BodyMD, call.BodyMD) {
						t.Fatalf("server preamble/body was missing or duplicated: %q", fullMessage.BodyMD)
					}
				}
			})
		}
	}
}

func TestMailSendOverseerAllExcludesReservedIdentityOnRepeat(t *testing.T) {
	isolateSessionAgentStorage(t)
	stub := newMailStub(t, nil)
	defer stub.Close()
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/mcp/")
	stub.listAgents = []agentmail.Agent{{Name: "BlueLake"}, {Name: "GreenCastle"}}

	for i := 0; i < 2; i++ {
		if _, err := execCommand(t, "mail", "send", "mysession", "--all", "Checkpoint the active work."); err != nil {
			t.Fatalf("broadcast %d: %v", i+1, err)
		}
		// The server creates the reserved sender during the first send.
		// A differently cased resource representation must also stay excluded.
		stub.listAgents = []agentmail.Agent{{Name: "BlueLake"}, {Name: "GreenCastle"}, {Name: " humanoverseer "}}
	}
	if len(stub.overseerCalls) != 2 {
		t.Fatalf("expected exactly two broadcast deliveries, got %d", len(stub.overseerCalls))
	}
	for _, call := range stub.overseerCalls {
		if !slices.Equal(call.Recipients, []string{"BlueLake", "GreenCastle"}) || !call.ConfirmMultiple {
			t.Fatalf("broadcast included the reserved sender or lacked confirmation: %+v", call)
		}
	}
}

func TestMailSendOverseerRejectsInvalidInputs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		body  string
		want  string
	}{
		{name: "missing_recipients", body: "Report status", want: "no recipients specified"},
		{name: "empty_recipient", flags: []string{"--to", " "}, body: "Report status", want: "recipient cannot be empty"},
		{name: "invalid_utf8_recipient", flags: []string{"--to", string([]byte{0xff})}, body: "Report status", want: "--to recipient must be valid UTF-8"},
		{name: "conflicting_recipients", flags: []string{"--to", "BlueLake", "--all"}, body: "Report status", want: "--all and --to"},
		{name: "empty_body", flags: []string{"--to", "BlueLake"}, body: " \n\t", want: "message body cannot be empty"},
		{name: "empty_subject", flags: []string{"--to", "BlueLake", "--subject", " "}, body: "Report status", want: "message subject cannot be empty"},
		{name: "long_subject", flags: []string{"--to", "BlueLake", "--subject", strings.Repeat("é", 201)}, body: "Report status", want: "message subject cannot exceed 200 characters"},
		{name: "invalid_utf8_subject", flags: []string{"--to", "BlueLake", "--subject", string([]byte{0xff})}, body: "Report status", want: "message subject must be valid UTF-8"},
		{name: "invalid_utf8_thread", flags: []string{"--to", "BlueLake", "--thread", string([]byte{0xff})}, body: "Report status", want: "--thread must be valid UTF-8"},
		{name: "empty_reason", flags: []string{"--to", "BlueLake", "--reason", ""}, body: "Report status", want: "--reason cannot be empty"},
		{name: "blank_reason", flags: []string{"--to", "BlueLake", "--reason", " \n\t"}, body: "Report status", want: "--reason cannot be empty"},
		{name: "long_reason", flags: []string{"--to", "BlueLake", "--reason", strings.Repeat("é", 501)}, body: "Report status", want: "--reason cannot exceed 500 characters"},
		{name: "invalid_utf8_reason", flags: []string{"--to", "BlueLake", "--reason", string([]byte{0xff})}, body: "Report status", want: "--reason must be valid UTF-8"},
		{name: "conflicting_body_sources", flags: []string{"--to", "BlueLake", "--file", "/nonexistent-message.md"}, body: "Report status", want: "--file and positional body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateSessionAgentStorage(t)
			stub := newMailStub(t, nil)
			defer stub.Close()
			t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/mcp/")
			args := append([]string{"mail", "send", "mysession"}, tc.flags...)
			args = append(args, tc.body)
			_, err := execCommand(t, args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if stub.ensureCalled != 0 || len(stub.overseerCalls) != 0 || len(stub.ordinarySendCalls) != 0 {
				t.Fatalf("invalid inputs reached a server mutation: ensure=%d sends=%d MCP=%v", stub.ensureCalled, len(stub.overseerCalls), stub.ordinarySendCalls)
			}
		})
	}
}

func TestMailSendOverseerRequiresServerProjectSlug(t *testing.T) {
	for _, tc := range []struct {
		name string
		slug string
	}{
		{name: "missing"},
		{name: "blank", slug: " \t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateSessionAgentStorage(t)
			stub := newMailStub(t, nil)
			defer stub.Close()
			stub.projectSlug = tc.slug
			t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/mcp/")
			_, err := execCommand(t, "mail", "send", "mysession", "--to", "BlueLake", "Report status")
			if err == nil || !strings.Contains(err.Error(), "server returned no project slug") {
				t.Fatalf("error = %v, want missing server slug rejection", err)
			}
			if stub.ensureCalled != 1 || len(stub.overseerCalls) != 0 || len(stub.ordinarySendCalls) != 0 {
				t.Fatalf("missing slug triggered a guessed delivery: ensure=%d sends=%d MCP=%v", stub.ensureCalled, len(stub.overseerCalls), stub.ordinarySendCalls)
			}
		})
	}
}

func TestMailSendOverseerAllRequiresRegisteredAgents(t *testing.T) {
	for _, agents := range [][]agentmail.Agent{nil, {{Name: "HumanOverseer"}}} {
		isolateSessionAgentStorage(t)
		stub := newMailStub(t, nil)
		defer stub.Close()
		stub.listAgents = agents
		t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/mcp/")
		_, err := execCommand(t, "mail", "send", "mysession", "--all", "Report status")
		if err == nil || !strings.Contains(err.Error(), "no agents registered") {
			t.Fatalf("agents=%v: error = %v, want no registered agents", agents, err)
		}
		if len(stub.overseerCalls) != 0 || len(stub.ordinarySendCalls) != 0 {
			t.Fatalf("empty broadcast attempted delivery")
		}
	}
}

func TestMailSendOverseerUnknownRecipientDoesNotDeliver(t *testing.T) {
	isolateSessionAgentStorage(t)
	stub := newMailStub(t, nil)
	defer stub.Close()
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/mcp/")
	out, err := execCommand(t, "mail", "send", "mysession", "--to", "BlueLake", "--to", "MissingAgent", "--json", "Report status")
	if err == nil || !strings.Contains(err.Error(), "Unknown recipient: MissingAgent") {
		t.Fatalf("error = %v, want unknown recipient diagnostic", err)
	}
	if strings.Contains(out, `"success": true`) || len(stub.overseerInbox) != 0 || len(stub.overseerCalls) != 1 || len(stub.ordinarySendCalls) != 0 {
		t.Fatalf("invalid recipient selection was delivered, retried, or reported as success")
	}
}

func TestMailSendOverseerLegacyPartialDeliveryIsReportedWithoutRetry(t *testing.T) {
	isolateSessionAgentStorage(t)
	stub := newMailStub(t, nil)
	defer stub.Close()
	stub.overseerLegacy = true
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/mcp/")
	out, err := execCommand(t, "mail", "send", "mysession", "--to", "BlueLake", "--to", "MissingAgent", "--json", "Report status")
	if err == nil || !strings.Contains(err.Error(), "message 123 delivery mismatch") || !strings.Contains(err.Error(), "MissingAgent") {
		t.Fatalf("error = %v, want partial delivery diagnostic with receipt and missing recipient", err)
	}
	if strings.Contains(out, `"success": true`) || len(stub.overseerCalls) != 1 || len(stub.ordinarySendCalls) != 0 || len(stub.overseerInbox["BlueLake"]) != 1 {
		t.Fatalf("partial delivery was reported as complete or retried: out=%q sends=%d", out, len(stub.overseerCalls))
	}
	inboxOut, err := execCommand(t, "mail", "inbox", "mysession", "--json")
	if err != nil {
		t.Fatalf("mail inbox after partial send: %v", err)
	}
	var inbox []aggregatedMessage
	if err := json.Unmarshal([]byte(inboxOut), &inbox); err != nil || len(inbox) != 1 || !slices.Equal(inbox[0].Recipients, []string{"BlueLake"}) {
		t.Fatalf("partial-delivery inbox readback = %q, err=%v", inboxOut, err)
	}
}

func TestMailSendOverseerPreservesActionableServerErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		legacy bool
		status int
		body   string
	}{
		{name: "current_validation", status: http.StatusBadRequest, body: "Unknown recipient: MissingAgent"},
		{name: "current_authorization", status: http.StatusForbidden, body: "Human overseer interventions require an authorized operator"},
		{name: "legacy_validation", legacy: true, status: http.StatusBadRequest, body: "No valid recipients found"},
		{name: "legacy_authentication", legacy: true, status: http.StatusUnauthorized, body: "Missing bearer token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateSessionAgentStorage(t)
			stub := newMailStub(t, nil)
			defer stub.Close()
			stub.overseerLegacy, stub.overseerStatus, stub.overseerError = tc.legacy, tc.status, tc.body
			t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/mcp/")
			out, err := execCommand(t, "mail", "send", "mysession", "--to", "BlueLake", "--json", "Report status")
			if err == nil || !strings.Contains(err.Error(), tc.body) {
				t.Fatalf("error = %v, want actionable server error %q", err, tc.body)
			}
			if strings.Contains(out, `"success": true`) || len(stub.overseerAuth) != 1 || len(stub.ordinarySendCalls) != 0 || len(stub.overseerInbox) != 0 {
				t.Fatalf("failed intervention retried, delivered, or reported success: out=%q requests=%d MCP=%v", out, len(stub.overseerAuth), stub.ordinarySendCalls)
			}
		})
	}
}

func TestMailSendOverseerAuthenticationFailures(t *testing.T) {
	for _, token := range []string{"", "wrong-overseer-token"} {
		name := "missing_token"
		if token != "" {
			name = "wrong_token"
		}
		t.Run(name, func(t *testing.T) {
			isolateSessionAgentStorage(t)
			stub := newMailStub(t, nil)
			defer stub.Close()
			stub.overseerAuthToken = "correct-overseer-token"
			t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/mcp/")
			t.Setenv("AGENT_MAIL_TOKEN", token)
			_, err := execCommand(t, "mail", "send", "mysession", "--to", "BlueLake", "Report status")
			if err == nil || !strings.Contains(err.Error(), "A valid bearer token is required") {
				t.Fatalf("error = %v, want server authentication diagnostic", err)
			}
			if len(stub.overseerAuth) != 1 || len(stub.overseerInbox) != 0 || len(stub.ordinarySendCalls) != 0 {
				t.Fatalf("authentication rejection triggered delivery or a fallback")
			}
		})
	}
}

func TestMailSendOverseerReasonBoundaryAndReset(t *testing.T) {
	isolateSessionAgentStorage(t)
	stub := newMailStub(t, nil)
	defer stub.Close()
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/mcp/")
	reason := strings.Repeat("é", 500)
	if _, err := execCommand(t, "mail", "send", "mysession", "--to", "BlueLake", "--reason", reason, "Report status"); err != nil {
		t.Fatalf("reason at Unicode character limit should send: %v", err)
	}
	if _, err := execCommand(t, "mail", "send", "mysession", "--to", "BlueLake", "Report status again"); err != nil {
		t.Fatalf("send after explicit reason: %v", err)
	}
	if len(stub.overseerCalls) != 2 || stub.overseerCalls[0].Reason != reason || stub.overseerCalls[1].Reason != agentmail.DefaultOverseerReason {
		t.Fatalf("reason leaked across repeated command executions: %+v", stub.overseerCalls)
	}
}

func TestMailSendOverseerDerivesValidUnicodeSubject(t *testing.T) {
	isolateSessionAgentStorage(t)
	stub := newMailStub(t, nil)
	defer stub.Close()
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/mcp/")
	body := "\n\n# " + strings.Repeat("📌", 70) + "\nPlease report status."
	if _, err := execCommand(t, "mail", "send", "mysession", "--to", "BlueLake", body); err != nil {
		t.Fatalf("send with a Unicode body and leading blank lines: %v", err)
	}
	if len(stub.overseerCalls) != 1 || stub.overseerCalls[0].Subject != strings.Repeat("📌", 57)+"..." || stub.overseerCalls[0].BodyMD != body {
		t.Fatalf("auto-derived subject corrupted UTF-8 or changed the body: %+v", stub.overseerCalls)
	}
}

func TestMailSendOverseerInvalidFlagsPreservePreparedBody(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		want  string
	}{
		{name: "empty_reason", flags: []string{"--reason", ""}, want: "--reason cannot be empty"},
		{name: "long_reason", flags: []string{"--reason", strings.Repeat("é", 501)}, want: "--reason cannot exceed 500 characters"},
		{name: "invalid_utf8_reason", flags: []string{"--reason", string([]byte{0xff})}, want: "--reason must be valid UTF-8"},
		{name: "long_subject", flags: []string{"--subject", strings.Repeat("é", 201)}, want: "message subject cannot exceed 200 characters"},
		{name: "invalid_utf8_subject", flags: []string{"--subject", string([]byte{0xff})}, want: "message subject must be valid UTF-8"},
		{name: "invalid_utf8_recipient", flags: []string{"--to", string([]byte{0xff})}, want: "--to recipient must be valid UTF-8"},
		{name: "invalid_utf8_thread", flags: []string{"--thread", string([]byte{0xff})}, want: "--thread must be valid UTF-8"},
		{name: "redaction_expands_subject", flags: []string{"--redact=redact", "--subject", strings.Repeat("x", 180) + " password=12345678"}, want: "message subject cannot exceed 200 characters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateSessionAgentStorage(t)
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
			handle, err := stashPreparedRedaction("one-use sensitive body", "[REDACTED]", nil)
			if err != nil {
				t.Fatalf("stash: %v", err)
			}
			args := []string{"mail", "send", "mysession", "--to", "BlueLake", "--subject", "Private update", "--prepared-redaction", handle}
			args = append(args, tc.flags...)
			_, err = execCommand(t, args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if raw, _, _, err := consumePreparedRedaction(handle); err != nil || raw != "one-use sensitive body" {
				t.Fatalf("invalid flags consumed the one-use body: raw=%q err=%v", raw, err)
			}
		})
	}
}

func TestMailSendOverseerSubjectLengthAfterRedaction(t *testing.T) {
	isolateSessionAgentStorage(t)
	stub := newMailStub(t, nil)
	defer stub.Close()
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/mcp/")

	boundarySubject := strings.Repeat("é", 200)
	if _, err := execCommand(t, "mail", "send", "mysession", "--to", "BlueLake", "--subject", boundarySubject, "Report status"); err != nil {
		t.Fatalf("subject at Unicode limit should send: %v", err)
	}
	if len(stub.overseerCalls) != 1 || stub.overseerCalls[0].Subject != boundarySubject {
		t.Fatalf("Unicode subject was changed or not sent: %+v", stub.overseerCalls)
	}

	secret := strings.Repeat("a", 220)
	out, err := execCommand(t, "--redact=redact", "--json", "mail", "send", "mysession", "--to", "BlueLake", "--subject", "password="+secret, "Report status")
	if err != nil {
		t.Fatalf("redaction should shorten an otherwise overlong subject: %v", err)
	}
	if len(stub.overseerCalls) != 2 || !strings.Contains(stub.overseerCalls[1].Subject, "[REDACTED:PASSWORD:") || strings.Contains(stub.overseerCalls[1].Subject, secret) || strings.Contains(out, secret) {
		t.Fatalf("subject was not safely redacted: calls=%+v output=%s", stub.overseerCalls, out)
	}
	var payload struct {
		Redaction *RedactionSummary `json:"redaction"`
	}
	// execCommand captures stderr and stdout together, including the safe
	// redaction notice that precedes the JSON result.
	jsonStart := strings.IndexByte(out, '{')
	if jsonStart < 0 {
		t.Fatalf("missing JSON send result: %s", out)
	}
	if err := json.Unmarshal([]byte(out[jsonStart:]), &payload); err != nil {
		t.Fatalf("decoding send result: %v", err)
	}
	if payload.Redaction == nil || payload.Redaction.Findings != 1 || payload.Redaction.Action != "redact" {
		t.Fatalf("preflight changed the final redaction summary: %+v", payload.Redaction)
	}
}

func TestMailSendOverseer_RedactModeScrubsBodyAndSubject(t *testing.T) {
	stub := newMailStub(t, nil)
	defer stub.Close()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")

	_, err := execCommand(t, "--redact=redact", "mail", "send", "mysession", "--to", "BlueLake", "--reason", "Rotate password=hunter2hunter2", "prefix password=hunter2hunter2 suffix")
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
	if strings.Contains(call.Reason, "hunter2hunter2") || !strings.Contains(call.Reason, "[REDACTED:PASSWORD:") {
		t.Fatalf("expected audit reason to be redacted, got %q", call.Reason)
	}
}

// Mail transport tests use an offline session probe so they need neither a
// running tmux server nor permission to create Unix sockets.
func isolateOverseerSessionProbe(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("session probe fixture requires /bin/sh")
	}
	path := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(path, []byte("#!/bin/sh\ncase \"$1\" in\n-V) echo 'tmux 3.4';;\nlist-sessions|list-panes|has-session) exit 0;;\n*) exit 1;;\nesac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", path)
	oldClient := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = oldClient })
}

func TestMailOverseerSendReadback(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, mode := range []string{"single", "multiple", "all"} {
			t.Run(fmt.Sprintf("legacy=%v/%s", legacy, mode), func(t *testing.T) {
				isolateOverseerSessionProbe(t)
				isolateSessionAgentStorage(t)
				stub := newMailStub(t, nil)
				defer stub.Close()
				stub.overseerLegacy = legacy
				stub.listAgents = []agentmail.Agent{{Name: "BlueLake"}, {Name: "RedStone"}, {Name: agentmail.HumanOverseerAgentName}}
				t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/mcp/")
				args := []string{"mail", "send", "mysession", "--subject", "Pause deployment", "--thread", "incident-355",
					"--reason", "Investigating production failure", "--json"}
				wantCount := 1
				if mode == "all" {
					args = append(args, "--all")
					wantCount = 2
				} else {
					args = append(args, "--to", "BlueLake")
					if mode == "multiple" {
						args = append(args, "--to", "RedStone")
						wantCount = 2
					}
				}
				out, err := execCommand(t, append(args, "Checkpoint current work")...)
				if err != nil {
					t.Fatalf("send: %v; %s", err, out)
				}
				var receipt agentmail.OverseerSendResult
				if err := json.Unmarshal([]byte(out), &receipt); err != nil || !receipt.Success || receipt.MessageID != 123 || len(receipt.Recipients) != wantCount {
					t.Fatalf("receipt: %s (%v)", out, err)
				}
				if len(stub.overseerCalls) != 1 {
					t.Fatalf("expected exactly one send, got %d", len(stub.overseerCalls))
				}
				call := stub.overseerCalls[0]
				if call.Reason != "Investigating production failure" || call.ThreadID != "incident-355" ||
					call.ConfirmMultiple != (wantCount > 1) || call.Broadcast {
					t.Fatalf("audit fields: %+v", call)
				}
				for _, recipient := range receipt.Recipients {
					if recipient == agentmail.HumanOverseerAgentName {
						t.Fatal("--all included overseer")
					}
					out, err := execCommand(t, "mail", "inbox", "mysession", "--agent", recipient, "--json")
					if err != nil || !strings.Contains(out, "HumanOverseer") || !strings.Contains(out, "Pause deployment") || !strings.Contains(out, "high") {
						t.Fatalf("CLI inbox %s: %v; %s", recipient, err, out)
					}
					client := agentmail.NewClient(agentmail.WithBaseURL(stub.server.URL + "/mcp/"))
					msgs, err := client.FetchInbox(t.Context(), agentmail.FetchInboxOptions{
						ProjectKey: stub.ensureProjectKeys[0], AgentName: recipient, IncludeBodies: true,
					})
					if err != nil || len(msgs) != 1 || !strings.Contains(msgs[0].BodyMD, "Checkpoint current work") ||
						msgs[0].ThreadID == nil || *msgs[0].ThreadID != "incident-355" {
						t.Fatalf("full inbox %s: %+v, %v", recipient, msgs, err)
					}
				}
			})
		}
	}
}

func TestMailOverseerFailures(t *testing.T) {
	for _, status := range []int{400, 401, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			isolateOverseerSessionProbe(t)
			stub := newMailStub(t, nil)
			defer stub.Close()
			stub.overseerStatus = status
			stub.overseerError = "operator intervention denied"
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/mcp/")
			_, err := execCommand(t, "mail", "send", "mysession", "--to", "BlueLake", "instructions")
			if err == nil || !strings.Contains(err.Error(), "operator intervention denied") || len(stub.overseerAuth) != 1 || len(stub.overseerInbox) != 0 {
				t.Fatalf("err=%v, requests=%d, delivered=%v", err, len(stub.overseerAuth), stub.overseerInbox)
			}
		})
	}
	for _, flags := range [][]string{
		{}, {"--all", "--to", "BlueLake"}, {"--to", " "},
		{"--to", "BlueLake", "--reason", " "}, {"--to", "BlueLake", "--reason", strings.Repeat("界", 501)},
		{"--to", "BlueLake", "--subject", strings.Repeat("界", 201)},
	} {
		t.Run(strings.Join(flags, "/"), func(t *testing.T) {
			isolateOverseerSessionProbe(t)
			stub := newMailStub(t, nil)
			defer stub.Close()
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/mcp/")
			args := append([]string{"mail", "send", "mysession"}, flags...)
			if _, err := execCommand(t, append(args, "instructions")...); err == nil || len(stub.overseerCalls) != 0 {
				t.Fatalf("invalid input sent: err=%v, calls=%d", err, len(stub.overseerCalls))
			}
		})
	}
}

func TestMailOverseerReasonRedactionAndReset(t *testing.T) {
	isolateOverseerSessionProbe(t)
	stub := newMailStub(t, nil)
	defer stub.Close()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/mcp/")
	if _, err := execCommand(t, "--redact=redact", "mail", "send", "mysession", "--to", "BlueLake", "--reason", "investigate password=hunter2hunter2", "instructions"); err != nil {
		t.Fatal(err)
	}
	if len(stub.overseerCalls) != 1 || strings.Contains(stub.overseerCalls[0].Reason, "hunter2hunter2") {
		t.Fatalf("secret in audit: %+v", stub.overseerCalls)
	}
	if _, err := execCommand(t, "mail", "send", "mysession", "--to", "BlueLake", "second"); err != nil {
		t.Fatal(err)
	}
	if len(stub.overseerCalls) != 2 || stub.overseerCalls[1].Reason != agentmail.DefaultOverseerReason {
		t.Fatalf("reason leaked between executions: %+v", stub.overseerCalls)
	}
	if _, err := execCommand(t, "--redact=block", "mail", "send", "mysession", "--to", "BlueLake", "--reason", "investigate password=hunter2hunter2", "instructions"); err == nil || len(stub.overseerCalls) != 2 {
		t.Fatalf("blocked reason sent: err=%v, calls=%d", err, len(stub.overseerCalls))
	}
}

func TestMailOverseerAllWithoutAgents(t *testing.T) {
	isolateOverseerSessionProbe(t)
	stub := newMailStub(t, nil)
	defer stub.Close()
	stub.listAgents = []agentmail.Agent{{Name: agentmail.HumanOverseerAgentName}}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/mcp/")
	_, err := execCommand(t, "mail", "send", "mysession", "--all", "instructions")
	if err == nil || !strings.Contains(err.Error(), "no agents registered") || len(stub.overseerCalls) != 0 {
		t.Fatalf("empty --all selection: err=%v, calls=%d", err, len(stub.overseerCalls))
	}
}

// TestMailSend_PreparedRedactionRequiresExplicitSubject pins the
// regression for the leak that fresh-eyes review uncovered: when the
// body source is a prepared-redaction handle, the auto-derived
// subject (truncateSubject(body, 60)) would otherwise dump up to 60
// chars of the raw token into the audit log and the JSON envelope
// whenever the configured redaction patterns failed to match the
// caller's specific token shape. The send must refuse without an
// explicit `--subject`.
func TestMailSend_PreparedRedactionRequiresExplicitSubject(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	handle, err := stashPreparedRedaction("aVeryLongSecretValueThatShouldNotLeakIntoTheSubject", "[REDACTED]", nil)
	if err != nil {
		t.Fatalf("stash: %v", err)
	}

	out, execErr := execCommand(t, "mail", "send", "mysession", "--to", "BlueLake", "--prepared-redaction", handle)
	if execErr == nil {
		t.Fatalf("expected refusal when --subject is missing; got out=%q", out)
	}
	if !strings.Contains(execErr.Error(), "explicit --subject") {
		t.Fatalf("expected error to mention explicit --subject, got %q", execErr.Error())
	}

	gotRaw, _, _, err := consumePreparedRedaction(handle)
	if err != nil {
		t.Fatalf("missing-subject refusal should leave handle reusable, got consume error: %v", err)
	}
	if gotRaw != "aVeryLongSecretValueThatShouldNotLeakIntoTheSubject" {
		t.Fatalf("unexpected raw handle body after refusal: %q", gotRaw)
	}
}

func TestMailSend_LocalFlagsDoNotLeakBetweenExecutions(t *testing.T) {
	stub := newMailStub(t, nil)
	defer stub.Close()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	if _, err := execCommand(t, "mail", "send", "mysession", "--to", "BlueLake", "--subject", "prior subject", "ordinary body"); err != nil {
		t.Fatalf("seed send: %v", err)
	}

	handle, err := stashPreparedRedaction("freshSecretBody", "[REDACTED]", nil)
	if err != nil {
		t.Fatalf("stash: %v", err)
	}

	out, execErr := execCommand(t, "mail", "send", "mysession", "--to", "BlueLake", "--prepared-redaction", handle)
	if execErr == nil {
		t.Fatalf("expected missing explicit subject to fail; out=%q", out)
	}
	if !strings.Contains(execErr.Error(), "explicit --subject") {
		t.Fatalf("expected explicit subject error, got %v", execErr)
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

func TestMailSendOverseer_BlockModeRefusesSecretsInAuditReason(t *testing.T) {
	isolateSessionAgentStorage(t)
	stub := newMailStub(t, nil)
	defer stub.Close()
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/mcp/")
	_, err := execCommand(t, "--redact=block", "mail", "send", "mysession", "--to", "BlueLake", "--reason", "Rotate password=hunter2hunter2", "Report status")
	if err == nil || len(stub.overseerCalls) != 0 {
		t.Fatalf("expected audit secret to block intervention, got err=%v sends=%d", err, len(stub.overseerCalls))
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

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
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
	cmd.SetContext(t.Context())
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
	cmd.SetContext(t.Context())
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

func TestRunMailMarkStopsImmediatelyWhenContextCancelsBetweenMessages(t *testing.T) {
	stub := newMailStub(t, nil)
	defer stub.Close()
	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stub.afterMark = func(int) { cancel() }
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)

	err := runMailMark(cmd, "mysession", "TestAgent", mailActionRead, []int{1, 2}, false, "", false, 50)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runMailMark() error=%v, want context.Canceled", err)
	}
	if len(stub.readIDs) != 1 || stub.readIDs[0] != 1 {
		t.Fatalf("mark calls=%v, want only first message before cancellation", stub.readIDs)
	}
}

func TestRunUnlockErrorsOnZeroSpecificRelease(t *testing.T) {
	resetFlags()
	// HOME isolation is required on macOS so the saved session registry
	// does not leak into other tests that look up "unlock-zero".
	isolateSessionAgentStorage(t)

	projectKey := GetProjectRoot()
	session := "unlock-zero"
	agentName := "BlueLake"
	saveSessionAgentForTest(t, session, projectKey, agentName)

	stub := newMailStub(t, nil)
	stub.releaseResult = agentmail.ReleaseReservationsResult{Released: 0}
	defer stub.Close()

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")

	err := runUnlock(t.Context(), session, []string{"internal/cli/*.go"}, false, -1, "")
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
	isolateSessionAgentStorage(t)

	projectKey := GetProjectRoot()
	session := "renew-root"
	agentName := "GreenLake"
	saveSessionAgentForTest(t, session, projectKey, agentName)

	stub := newMailStub(t, nil)
	stub.renewResult = agentmail.RenewReservationsResult{Renewed: 2}
	defer stub.Close()

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Chdir(filepath.Join(projectKey, "internal"))

	if err := runRenewLocks(t.Context(), session, 30); err != nil {
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
	isolateSessionAgentStorage(t)

	projectKey := GetProjectRoot()
	session := "renew-zero"
	agentName := "RedStone"
	saveSessionAgentForTest(t, session, projectKey, agentName)

	stub := newMailStub(t, nil)
	stub.renewResult = agentmail.RenewReservationsResult{Renewed: 0}
	defer stub.Close()

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")

	err := runRenewLocks(t.Context(), session, 30)
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
	// Isolate session-agent storage so stale registries written by other
	// tests under HOME (or macOS Application Support) cannot leak in and
	// flip projectKey to a high-scoring git repo like /Users/runner/work.
	isolateSessionAgentStorage(t)
	stub := newMailStub(t, []agentmail.InboxMessage{
		{ID: 11, Subject: "Scoped inbox", From: "BlueLake", CreatedTS: agentmail.FlexTime{Time: time.Now()}},
	})
	defer stub.Close()

	projectsBase := canonicalTempDir(t)
	projectKey := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(projectKey, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Chdir(canonicalTempDir(t))

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
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
	cmd.SetContext(t.Context())
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
	isolateSessionAgentStorage(t)
	stub := newMailStub(t, nil)
	defer stub.Close()

	projectsBase := canonicalTempDir(t)
	projectKey := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(projectKey, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Chdir(canonicalTempDir(t))

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
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
	isolateSessionAgentStorage(t)
	stub := newMailStub(t, nil)
	defer stub.Close()

	projectsBase := canonicalTempDir(t)
	projectKey := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(projectKey, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Chdir(canonicalTempDir(t))

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := runMailSendOverseer(cmd, "mysession", []string{"BlueLake"}, "Subject", "Body", "", agentmail.DefaultOverseerReason, false); err != nil {
		t.Fatalf("runMailSendOverseer() error = %v", err)
	}
	// Resolve the project once, then deliver through its intervention route.
	if !slices.Equal(stub.ensureProjectKeys, []string{projectKey}) {
		t.Fatalf("expected one ensure_project for %q, got %v", projectKey, stub.ensureProjectKeys)
	}
	if len(stub.ordinarySendCalls) != 0 {
		t.Fatalf("overseer route unexpectedly registered or sent as an ordinary agent: %v", stub.ordinarySendCalls)
	}
}

func TestRunMailMutationHelpersRejectMissingOrCanceledContext(t *testing.T) {
	tests := []struct {
		name string
		run  func(*cobra.Command) error
	}{
		{
			name: "mark",
			run: func(cmd *cobra.Command) error {
				return runMailMark(cmd, "mysession", "BlueLake", mailActionRead, []int{42}, false, "", false, 50)
			},
		},
		{
			name: "overseer send",
			run: func(cmd *cobra.Command) error {
				return runMailSendOverseer(cmd, "mysession", []string{"BlueLake"}, "Subject", "Body", "", agentmail.DefaultOverseerReason, false)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Run("nil command", func(t *testing.T) {
				if err := tt.run(nil); err == nil || !strings.Contains(err.Error(), "requires a command context") {
					t.Fatalf("helper error = %v, want missing context error", err)
				}
			})

			t.Run("nil context", func(t *testing.T) {
				if err := tt.run(&cobra.Command{}); err == nil || !strings.Contains(err.Error(), "requires a command context") {
					t.Fatalf("helper error = %v, want missing context error", err)
				}
			})

			t.Run("pre-canceled context", func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				cmd := &cobra.Command{}
				cmd.SetContext(ctx)
				if err := tt.run(cmd); !errors.Is(err, context.Canceled) {
					t.Fatalf("helper error = %v, want context.Canceled", err)
				}
			})
		})
	}
}

func TestResolveAgentMailScopesPreservePreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name string
		run  func() error
	}{
		{
			name: "command scope",
			run: func() error {
				_, _, err := resolveAgentMailCommandScope(ctx, "mysession")
				return err
			},
		},
		{
			name: "preferred scope",
			run: func() error {
				_, _, err := resolveAgentMailScopeWithPreference(ctx, "mysession", true)
				return err
			},
		},
		{
			name: "explicit agent identity",
			run: func() error {
				_, err := resolveMailAgentIdentity(ctx, "mysession", "ExplicitAgent")
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); !errors.Is(err, context.Canceled) {
				t.Fatalf("scope error=%v, want context.Canceled", err)
			}
		})
	}
}

func TestUpdateSessionActivityUsesSavedSessionAgentProjectKey(t *testing.T) {
	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))

	cfg = &config.Config{ProjectsBase: filepath.Join(t.TempDir(), "projects")}

	cwdDir := t.TempDir()
	if err := os.Chdir(cwdDir); err != nil {
		t.Fatalf("chdir cwd: %v", err)
	}

	session := "mysession"
	actualProject := filepath.Join(t.TempDir(), "actual-project")
	if err := os.MkdirAll(filepath.Join(actualProject, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir actual project git dir: %v", err)
	}

	oldTime := time.Now().Add(-2 * time.Hour)
	info := &agentmail.SessionAgentInfo{
		AgentName:    "GreenCastle",
		ProjectKey:   actualProject,
		RegisteredAt: oldTime,
		LastActiveAt: oldTime,
	}
	if err := agentmail.SaveSessionAgent(session, actualProject, info); err != nil {
		t.Fatalf("save session agent: %v", err)
	}

	updateSessionActivity(t.Context(), session)

	updated, err := agentmail.LoadSessionAgent(session, actualProject)
	if err != nil {
		t.Fatalf("load updated session agent: %v", err)
	}
	if updated == nil {
		t.Fatal("expected updated session agent")
	}
	if time.Since(updated.LastActiveAt) > time.Minute {
		t.Fatalf("expected saved-session-agent project to be updated, last active at %v", updated.LastActiveAt)
	}
}

func TestUpdateSessionActivityIgnoresCWDMatchedWrongProject(t *testing.T) {
	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))

	cfg = &config.Config{ProjectsBase: filepath.Join(t.TempDir(), "projects")}

	wrongProject := filepath.Join(t.TempDir(), "wrong-project")
	if err := os.MkdirAll(filepath.Join(wrongProject, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir wrong project git dir: %v", err)
	}
	actualProject := filepath.Join(t.TempDir(), "actual-project")
	if err := os.MkdirAll(filepath.Join(actualProject, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir actual project git dir: %v", err)
	}
	if err := os.Chdir(wrongProject); err != nil {
		t.Fatalf("chdir wrong project: %v", err)
	}

	session := "mysession"
	wrongTime := time.Now().Add(-2 * time.Hour)
	wrongInfo := &agentmail.SessionAgentInfo{
		AgentName:    "WrongLake",
		ProjectKey:   wrongProject,
		RegisteredAt: wrongTime,
		LastActiveAt: wrongTime,
	}
	if err := agentmail.SaveSessionAgent(session, wrongProject, wrongInfo); err != nil {
		t.Fatalf("save wrong session agent: %v", err)
	}
	actualTime := time.Now().Add(-time.Hour)
	actualInfo := &agentmail.SessionAgentInfo{
		AgentName:    "RightLake",
		ProjectKey:   actualProject,
		RegisteredAt: actualTime,
		LastActiveAt: actualTime,
	}
	if err := agentmail.SaveSessionAgent(session, actualProject, actualInfo); err != nil {
		t.Fatalf("save actual session agent: %v", err)
	}

	updateSessionActivity(t.Context(), session)

	updatedWrong, err := agentmail.LoadSessionAgent(session, wrongProject)
	if err != nil {
		t.Fatalf("load wrong session agent: %v", err)
	}
	if updatedWrong == nil {
		t.Fatal("expected wrong session agent")
	}
	if !updatedWrong.LastActiveAt.Equal(wrongTime) {
		t.Fatalf("expected wrong project timestamp to remain %v, got %v", wrongTime, updatedWrong.LastActiveAt)
	}

	updatedActual, err := agentmail.LoadSessionAgent(session, actualProject)
	if err != nil {
		t.Fatalf("load actual session agent: %v", err)
	}
	if updatedActual == nil {
		t.Fatal("expected actual session agent")
	}
	if !updatedActual.LastActiveAt.After(actualTime) {
		t.Fatalf("expected actual project timestamp to advance beyond %v, got %v", actualTime, updatedActual.LastActiveAt)
	}
}

func TestResolveAgentMailScopeWithPreferenceNormalizesExplicitPrefix(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	isolateSessionAgentStorage(t)

	fullSession := "mailscopeprefixsession"
	prefix := "mailscopeprefix"
	projectsBase := canonicalTempDir(t)
	projectKey := filepath.Join(projectsBase, fullSession)
	if err := os.MkdirAll(projectKey, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	_ = tmux.KillSession(fullSession)
	if err := tmux.CreateSession(fullSession, projectKey); err != nil {
		t.Fatalf("CreateSession(%q): %v", fullSession, err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(fullSession) })

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	resolvedSession, resolvedProjectKey, err := resolveAgentMailScopeWithPreference(t.Context(), prefix, true)
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

func TestResolveAgentMailScopeWithPreferenceRejectsWorkspaceFallbackForExplicitSession(t *testing.T) {
	isolateSessionAgentStorage(t)
	session := "ntm-mail-explicit-missing-project-test"

	origCfg := cfg
	origWd, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		_ = os.Chdir(origWd)
	})

	cfg = &config.Config{ProjectsBase: t.TempDir()}

	wd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wd, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir wd git: %v", err)
	}
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	_, _, err := resolveAgentMailScopeWithPreference(t.Context(), session, true)
	if err == nil {
		t.Fatal("expected missing session project error")
	}
	if !strings.Contains(err.Error(), "getting project root failed") {
		t.Fatalf("expected project root error, got %v", err)
	}
}

func TestResolveAgentMailCommandScopeFallsBackToCurrentProjectRootForExplicitSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origWd, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		_ = os.Chdir(origWd)
	})

	cfg = &config.Config{ProjectsBase: canonicalTempDir(t)}

	projectRoot := canonicalTempDir(t)
	if err := os.MkdirAll(filepath.Join(projectRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir project root git: %v", err)
	}
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	resolvedSession, projectKey, err := resolveAgentMailCommandScope(t.Context(), "mysession")
	if err != nil {
		t.Fatalf("resolveAgentMailCommandScope() error = %v", err)
	}
	if resolvedSession != "mysession" {
		t.Fatalf("resolved session = %q, want %q", resolvedSession, "mysession")
	}
	if projectKey != projectRoot {
		t.Fatalf("project key = %q, want %q", projectKey, projectRoot)
	}
}

func TestResolveAgentMailCommandScopePrefersSavedSessionAgentProject(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origWd, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		_ = os.Chdir(origWd)
	})

	cfg = &config.Config{ProjectsBase: t.TempDir()}

	cwdProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdProject, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir cwd project git: %v", err)
	}
	if err := os.Chdir(cwdProject); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	actualProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(actualProject, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir actual project git: %v", err)
	}
	saveSessionAgentForTest(t, "mysession", actualProject, "GreenCastle")

	_, projectKey, err := resolveAgentMailCommandScope(t.Context(), "mysession")
	if err != nil {
		t.Fatalf("resolveAgentMailCommandScope() error = %v", err)
	}
	if projectKey != actualProject {
		t.Fatalf("project key = %q, want %q", projectKey, actualProject)
	}
}

func TestRefineAgentMailProjectKeyIgnoresUnusableSavedProject(t *testing.T) {
	isolateSessionAgentStorage(t)

	cwdProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdProject, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir cwd project git: %v", err)
	}

	staleProject := filepath.Join(t.TempDir(), "missing-project")
	saveSessionAgentForTest(t, "mysession", staleProject, "GreenCastle")

	got := refineAgentMailProjectKey("mysession", cwdProject)
	if got != cwdProject {
		t.Fatalf("refineAgentMailProjectKey() = %q, want %q", got, cwdProject)
	}
}

func TestRunLockUsesSessionProjectDir(t *testing.T) {
	resetFlags()
	isolateSessionAgentStorage(t)

	projectsBase := canonicalTempDir(t)
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
	t.Chdir(canonicalTempDir(t))

	if err := runLock(t.Context(), session, []string{"internal/**/*.go"}, "scope test", "1h", false); err != nil {
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
	isolateSessionAgentStorage(t)

	projectsBase := canonicalTempDir(t)
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
	t.Chdir(canonicalTempDir(t))

	if err := runLocks(t.Context(), session, false, false); err != nil {
		t.Fatalf("runLocks: %v", err)
	}
	if len(stub.listCalls) != 1 {
		t.Fatalf("expected one reservation list call, got %d", len(stub.listCalls))
	}
	if got := stub.listCalls[0].Project; got != projectKey {
		t.Fatalf("expected list project %q, got %q", projectKey, got)
	}
}

func TestRunLocksRequiresSessionAgentUnlessAllAgents(t *testing.T) {
	resetFlags()
	isolateSessionAgentStorage(t)

	projectsBase := canonicalTempDir(t)
	projectKey := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(projectKey, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	stub := newMailStub(t, nil)
	defer stub.Close()

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Chdir(canonicalTempDir(t))

	err := runLocks(t.Context(), "mysession", false, false)
	if err == nil {
		t.Fatal("expected missing session-agent identity error")
	}
	if !strings.Contains(err.Error(), "has no Agent Mail identity") {
		t.Fatalf("expected missing identity error, got %v", err)
	}
	if len(stub.listCalls) != 0 {
		t.Fatalf("expected no list call when identity is missing, got %d", len(stub.listCalls))
	}
}

func TestRunUnlockUsesSessionProjectDir(t *testing.T) {
	resetFlags()
	isolateSessionAgentStorage(t)

	projectsBase := canonicalTempDir(t)
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
	t.Chdir(canonicalTempDir(t))

	if err := runUnlock(t.Context(), session, []string{"internal/cli/*.go"}, false, -1, ""); err != nil {
		t.Fatalf("runUnlock: %v", err)
	}
	if len(stub.releaseCalls) != 1 {
		t.Fatalf("expected one release call, got %d", len(stub.releaseCalls))
	}
	if got := stub.releaseCalls[0].Project; got != projectKey {
		t.Fatalf("expected release project %q, got %q", projectKey, got)
	}
}

func TestRunUnlockUsesSavedSessionAgentIdentityAndProjectKey(t *testing.T) {
	resetFlags()
	isolateSessionAgentStorage(t)

	projectsBase := canonicalTempDir(t)
	configuredProject := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(configuredProject, 0o755); err != nil {
		t.Fatalf("mkdir configured project: %v", err)
	}

	actualProject := canonicalTempDir(t)
	session := "mysession"
	saveSessionAgentForTest(t, session, actualProject, "GreenCastle")

	stub := newMailStub(t, nil)
	defer stub.Close()

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	t.Setenv("AGENT_MAIL_URL", stub.server.URL+"/")
	t.Chdir(canonicalTempDir(t))

	if err := runUnlock(t.Context(), session, []string{"internal/cli/*.go"}, false, -1, ""); err != nil {
		t.Fatalf("runUnlock: %v", err)
	}
	if len(stub.releaseCalls) != 1 {
		t.Fatalf("expected one release call, got %d", len(stub.releaseCalls))
	}
	if got := stub.releaseCalls[0].Project; got != actualProject {
		t.Fatalf("expected release project %q, got %q", actualProject, got)
	}
	if got := stub.releaseCalls[0].Agent; got != "GreenCastle" {
		t.Fatalf("expected release agent %q, got %q", "GreenCastle", got)
	}
}

func TestRunForceReleaseUsesSessionProjectDir(t *testing.T) {
	resetFlags()
	isolateSessionAgentStorage(t)

	projectsBase := canonicalTempDir(t)
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
	t.Chdir(canonicalTempDir(t))

	// bd-2y2on: force-release is approval-gated (default policy blocks and
	// enqueues an approval request). This test exercises the project-dir
	// plumbing BEYOND the gate, so the hermetic HOME gets an auto policy;
	// the gate itself is covered by force_release_gate_test.go and the
	// TestSLBApproval_ForceReleaseGated e2e.
	ntmDir := filepath.Join(os.Getenv("HOME"), ".ntm")
	if err := os.MkdirAll(ntmDir, 0o755); err != nil {
		t.Fatalf("mkdir hermetic .ntm: %v", err)
	}
	autoPolicy := "version: 1\nautomation:\n  force_release: auto\n"
	if err := os.WriteFile(filepath.Join(ntmDir, "policy.yaml"), []byte(autoPolicy), 0o644); err != nil {
		t.Fatalf("write auto policy: %v", err)
	}

	if err := runForceRelease(t.Context(), session, 42, "stale", true, true, ""); err != nil {
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
	isolateSessionAgentStorage(t)

	projectsBase := canonicalTempDir(t)
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
	t.Chdir(canonicalTempDir(t))

	if err := runRenewLocks(t.Context(), session, 30); err != nil {
		t.Fatalf("runRenewLocks: %v", err)
	}
	if len(stub.renewCalls) != 1 {
		t.Fatalf("expected one renew call, got %d", len(stub.renewCalls))
	}
	if got := stub.renewCalls[0].Project; got != projectKey {
		t.Fatalf("expected renew project %q, got %q", projectKey, got)
	}
}

// TestAgentMailUnavailableMessageSurfacesProbeReason is the #295 regression on
// the reporting side: reservation verbs must name the probe's terminal error
// in their JSON `error` instead of emitting a bare, misleading fixed string.
func TestAgentMailUnavailableMessageSurfacesProbeReason(t *testing.T) {
	reason := errors.New("liveness: context deadline exceeded")
	got := agentMailUnavailableMessage(mailAvailabilityDiagnosticStub{err: reason})
	want := "Agent Mail server unavailable: liveness: context deadline exceeded"
	if got != want {
		t.Fatalf("agentMailUnavailableMessage() = %q, want %q", got, want)
	}

	if got := agentMailUnavailableMessage(mailAvailabilityDiagnosticStub{}); got != "Agent Mail server unavailable" {
		t.Fatalf("agentMailUnavailableMessage() without a reason = %q", got)
	}
	if got := agentMailUnavailableMessage(legacyMailAvailabilityStub{}); got != "Agent Mail server unavailable" {
		t.Fatalf("agentMailUnavailableMessage() for a client without diagnostics = %q", got)
	}
}

func TestAgentMailUnavailableCLIErrorWrapsProbeReason(t *testing.T) {
	reason := errors.New("liveness: connection refused")
	err := agentMailUnavailableCLIError(mailAvailabilityDiagnosticStub{err: reason})
	if !errors.Is(err, reason) {
		t.Fatalf("agentMailUnavailableCLIError() = %v, want wrapped reason", err)
	}
	if !strings.HasPrefix(err.Error(), "agent mail server unavailable: ") {
		t.Fatalf("agentMailUnavailableCLIError() = %q, want lowercase prefixed message", err)
	}
	if err := agentMailUnavailableCLIError(legacyMailAvailabilityStub{}); err == nil || err.Error() != "agent mail server unavailable" {
		t.Fatalf("agentMailUnavailableCLIError() without diagnostics = %v", err)
	}
}

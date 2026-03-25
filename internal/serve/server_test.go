package serve

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

func setupTestServer(t *testing.T) (*Server, *state.Store) {
	t.Helper()

	// Create temp database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}

	if err := store.Migrate(); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	t.Cleanup(func() {
		store.Close()
		os.Remove(dbPath)
	})

	eventBus := events.NewEventBus(100)

	srv := New(Config{
		Port:       0, // Will use default
		EventBus:   eventBus,
		StateStore: store,
	})

	t.Cleanup(func() {
		srv.Stop()
	})

	return srv, store
}

func requireRegisterWSClient(tb testing.TB, hub *WSHub, client *WSClient) {
	tb.Helper()
	if ok := hub.RegisterClient(client); !ok {
		tb.Fatalf("register client %q: hub stopped", client.id)
	}
}

func newMockAgentMailMCPServer(
	t *testing.T,
	handlers map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError),
) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeRPCResponse := func(resp agentmail.JSONRPCResponse) {
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				t.Errorf("encode response: %v", err)
			}
		}

		var req agentmail.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			writeRPCResponse(agentmail.JSONRPCResponse{
				JSONRPC: "2.0",
				Error: &agentmail.JSONRPCError{
					Code:    -32700,
					Message: "parse error",
				},
			})
			return
		}

		params, _ := req.Params.(map[string]interface{})
		toolName, _ := params["name"].(string)
		args, _ := params["arguments"].(map[string]interface{})

		handler, ok := handlers[toolName]
		if !ok {
			writeRPCResponse(agentmail.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &agentmail.JSONRPCError{
					Code:    -32601,
					Message: "unknown tool: " + toolName,
				},
			})
			return
		}

		result, rpcErr := handler(args)
		if rpcErr != nil {
			writeRPCResponse(agentmail.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   rpcErr,
			})
			return
		}

		resultJSON, err := json.Marshal(result)
		if err != nil {
			t.Errorf("marshal result: %v", err)
			writeRPCResponse(agentmail.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &agentmail.JSONRPCError{
					Code:    -32603,
					Message: "internal error",
				},
			})
			return
		}
		writeRPCResponse(agentmail.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  resultJSON,
		})
	}))
}

func TestNew(t *testing.T) {
	srv := New(Config{})
	if srv == nil {
		t.Fatal("New returned nil")
	}
	if srv.Port() != 7337 {
		t.Errorf("Default port = %d, want 7337", srv.Port())
	}
}

func TestNewWithCustomPort(t *testing.T) {
	srv := New(Config{Port: 8080})
	if srv.Port() != 8080 {
		t.Errorf("Port = %d, want 8080", srv.Port())
	}
}

func TestValidateConfigDefaults(t *testing.T) {
	if err := ValidateConfig(Config{}); err != nil {
		t.Fatalf("ValidateConfig default should succeed, got %v", err)
	}
}

func TestValidateConfigRejectsExternalLocalAuth(t *testing.T) {
	cfg := Config{
		Host: "0.0.0.0",
		Auth: AuthConfig{Mode: AuthModeLocal},
	}
	if err := ValidateConfig(cfg); err == nil || !strings.Contains(err.Error(), "refusing to bind") {
		t.Fatalf("expected bind refusal error, got %v", err)
	}
}

func TestValidateConfigAllowsExternalWithAuth(t *testing.T) {
	cfg := Config{
		Host: "0.0.0.0",
		Auth: AuthConfig{Mode: AuthModeAPIKey, APIKey: "test-key"},
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("expected external bind with auth to succeed, got %v", err)
	}
}

func TestValidateConfigPublicBaseURL(t *testing.T) {
	cfg := Config{
		PublicBaseURL: "https://ntm.example.com",
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("expected valid public base URL, got %v", err)
	}

	cfg.PublicBaseURL = "not-a-url"
	if err := ValidateConfig(cfg); err == nil {
		t.Fatalf("expected invalid public base URL error")
	}
}

func TestHandleGetMessage_NotImplemented(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/messages/42", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleGetMessage(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusNotImplemented, rec.Body.String())
	}

	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorCode != ErrCodeNotImplemented {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, ErrCodeNotImplemented)
	}
	if !strings.Contains(resp.Hint, "include_bodies") {
		t.Fatalf("hint = %q, want include_bodies guidance", resp.Hint)
	}
}

func TestHandleMailInbox_SanitizesDisclosureFields(t *testing.T) {
	t.Parallel()

	secret := strings.Repeat("s", 20)
	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"fetch_inbox": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return []agentmail.InboxMessage{
				{
					ID:         7,
					Subject:    "Rotate credential",
					BodyMD:     "Need token=" + secret + " before merge",
					From:       "BlueLake",
					Importance: "urgent",
					Kind:       "to",
					CreatedTS:  agentmail.FlexTime{},
				},
			}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/inbox?agent_name=GreenStone&include_bodies=true", nil)

	srv.handleMailInbox(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Success  bool                       `json:"success"`
		Messages []MailInboxMessageResponse `json:"messages"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(resp.Messages))
	}
	msg := resp.Messages[0]
	if msg.Subject != "Rotate credential" {
		t.Fatalf("subject = %q", msg.Subject)
	}
	if msg.SubjectDisclosure == nil || msg.SubjectDisclosure.DisclosureState != "visible" {
		t.Fatalf("unexpected subject disclosure: %+v", msg.SubjectDisclosure)
	}
	if !strings.Contains(msg.Preview, "[REDACTED:GENERIC_SECRET:") {
		t.Fatalf("expected redacted preview, got %q", msg.Preview)
	}
	if msg.PreviewDisclosure == nil || msg.PreviewDisclosure.DisclosureState != "redacted" || msg.PreviewDisclosure.Findings != 1 {
		t.Fatalf("unexpected preview disclosure: %+v", msg.PreviewDisclosure)
	}
	if msg.BodyMD == nil || !strings.Contains(*msg.BodyMD, "[REDACTED:GENERIC_SECRET:") {
		t.Fatalf("expected redacted body_md, got %+v", msg.BodyMD)
	}
	if msg.BodyDisclosure == nil || msg.BodyDisclosure.DisclosureState != "redacted" || msg.BodyDisclosure.RedactionMode != "redact" || msg.BodyDisclosure.Findings != 1 {
		t.Fatalf("unexpected body disclosure: %+v", msg.BodyDisclosure)
	}
}

func TestHandleGetMessage_SanitizesDisclosureFields(t *testing.T) {
	t.Parallel()

	secret := strings.Repeat("s", 20)
	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"get_message": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return &agentmail.Message{
				ID:          42,
				ProjectID:   1,
				SenderID:    2,
				Subject:     "Rotate credential",
				BodyMD:      "Need token=" + secret + " before merge",
				From:        "BlueLake",
				To:          []string{"GreenStone"},
				Importance:  "urgent",
				AckRequired: true,
				CreatedTS:   agentmail.FlexTime{},
			}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/messages/42", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleGetMessage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Success bool                 `json:"success"`
		Message *MailMessageResponse `json:"message"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Message == nil {
		t.Fatal("expected message payload")
	}
	if resp.Message.Subject != "Rotate credential" {
		t.Fatalf("subject = %q", resp.Message.Subject)
	}
	if resp.Message.SubjectDisclosure == nil || resp.Message.SubjectDisclosure.DisclosureState != "visible" {
		t.Fatalf("unexpected subject disclosure: %+v", resp.Message.SubjectDisclosure)
	}
	if !strings.Contains(resp.Message.Preview, "[REDACTED:GENERIC_SECRET:") {
		t.Fatalf("expected redacted preview, got %q", resp.Message.Preview)
	}
	if resp.Message.BodyMD == nil || !strings.Contains(*resp.Message.BodyMD, "[REDACTED:GENERIC_SECRET:") {
		t.Fatalf("expected redacted body_md, got %+v", resp.Message.BodyMD)
	}
	if resp.Message.BodyDisclosure == nil || resp.Message.BodyDisclosure.DisclosureState != "redacted" || resp.Message.BodyDisclosure.Findings != 1 {
		t.Fatalf("unexpected body disclosure: %+v", resp.Message.BodyDisclosure)
	}
}

func TestHandleReplyMessage_MessageNotFound(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"reply_message": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return nil, &agentmail.JSONRPCError{Code: -32000, Message: "Message not found"}
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/42/reply", strings.NewReader(`{"sender_name":"BlueLake","body_md":"reply"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleReplyMessage(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorCode != ErrCodeMessageNotFound {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, ErrCodeMessageNotFound)
	}
}

func TestHandleSendMessage_ContactBlocked(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"send_message": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return nil, &agentmail.JSONRPCError{
				Code:    -32000,
				Message: "CONTACT_BLOCKED: target agent only accepts approved contacts",
			}
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages", strings.NewReader(`{"sender_name":"BlueLake","to":["RedStone"],"subject":"hello","body_md":"hi"}`))

	srv.handleSendMessage(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}

	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorCode != ErrCodeContactDenied {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, ErrCodeContactDenied)
	}
}

func TestHandleReplyMessage_ContactBlocked(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"reply_message": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return nil, &agentmail.JSONRPCError{
				Code:    -32000,
				Message: "CONTACT_BLOCKED: target agent only accepts approved contacts",
			}
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/42/reply", strings.NewReader(`{"sender_name":"BlueLake","body_md":"reply"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleReplyMessage(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}

	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorCode != ErrCodeContactDenied {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, ErrCodeContactDenied)
	}
}

func TestHandleReservePaths_ConflictReturnsStructured409(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"file_reservation_paths": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.ReservationResult{
				Granted: []agentmail.FileReservation{
					{ID: 7, PathPattern: "internal/serve/*.go", AgentName: "BlueLake", Exclusive: true},
				},
				Conflicts: []agentmail.ReservationConflict{
					{Path: "internal/serve/*.go", Holders: []string{"RedStone"}},
				},
			}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations", strings.NewReader(`{"agent_name":"BlueLake","paths":["internal/serve/*.go"],"exclusive":true}`))

	srv.handleReservePaths(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusConflict, rec.Body.String())
	}

	var resp struct {
		Success   bool                            `json:"success"`
		Granted   []agentmail.FileReservation     `json:"granted"`
		Conflicts []agentmail.ReservationConflict `json:"conflicts"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Success {
		t.Fatal("expected success=true for structured conflict response")
	}
	if len(resp.Granted) != 1 {
		t.Fatalf("len(granted) = %d, want 1", len(resp.Granted))
	}
	if len(resp.Conflicts) != 1 {
		t.Fatalf("len(conflicts) = %d, want 1", len(resp.Conflicts))
	}
}

func TestHandleGetReservation_NotImplemented(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/reservations/42", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleGetReservation(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusNotImplemented, rec.Body.String())
	}

	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorCode != ErrCodeNotImplemented {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, ErrCodeNotImplemented)
	}
}

func TestHandleGetReservation_NotFound(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"list_file_reservations": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return []agentmail.FileReservation{}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/reservations/42", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleGetReservation(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorCode != ErrCodeNotFound {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, ErrCodeNotFound)
	}
}

func TestHandleRenewReservation_ReturnsRenewedReservations(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"renew_file_reservations": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.RenewReservationsResult{
				Renewed: 1,
				Reservations: []agentmail.RenewedReservation{
					{
						ID:          42,
						PathPattern: "internal/serve/*.go",
					},
				},
			}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations/42/renew", strings.NewReader(`{"agent_name":"BlueLake","extend_seconds":900}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleRenewReservation(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Success      bool                           `json:"success"`
		Renewed      int                            `json:"renewed"`
		Reservations []agentmail.RenewedReservation `json:"reservations"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Success {
		t.Fatal("expected success=true")
	}
	if resp.Renewed != 1 {
		t.Fatalf("renewed = %d, want 1", resp.Renewed)
	}
	if len(resp.Reservations) != 1 {
		t.Fatalf("len(reservations) = %d, want 1", len(resp.Reservations))
	}
	if resp.Reservations[0].PathPattern != "internal/serve/*.go" {
		t.Fatalf("path_pattern = %q, want %q", resp.Reservations[0].PathPattern, "internal/serve/*.go")
	}
}

func TestHandleReleaseReservations_ReturnsReleaseCount(t *testing.T) {
	t.Parallel()

	releasedAt := "2026-03-23T02:03:04Z"
	releasedTime, err := time.Parse(time.RFC3339, releasedAt)
	if err != nil {
		t.Fatalf("parse released_at: %v", err)
	}
	releasedFlexTime := agentmail.FlexTime{Time: releasedTime}
	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"release_file_reservations": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.ReleaseReservationsResult{Released: 2, ReleasedAt: &releasedFlexTime}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/reservations", strings.NewReader(`{"agent_name":"BlueLake","paths":["a/*","b/*"]}`))

	srv.handleReleaseReservations(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Success    bool   `json:"success"`
		Released   int    `json:"released"`
		ReleasedAt string `json:"released_at"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Success {
		t.Fatal("expected success=true")
	}
	if resp.Released != 2 {
		t.Fatalf("released = %d, want 2", resp.Released)
	}
	if resp.ReleasedAt != releasedAt {
		t.Fatalf("released_at = %q, want %q", resp.ReleasedAt, releasedAt)
	}
}

func TestHandleReleaseReservationByID_NotFoundOnZeroRelease(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"release_file_reservations": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.ReleaseReservationsResult{Released: 0}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations/42/release?agent_name=BlueLake", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleReleaseReservationByID(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorCode != ErrCodeNotFound {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, ErrCodeNotFound)
	}
}

func TestHandleMarkMessageRead_AgentNotFound(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"mark_message_read": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return nil, &agentmail.JSONRPCError{Code: -32000, Message: "Agent not registered in project"}
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/42/read?agent_name=BlueLake", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleMarkMessageRead(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorCode != ErrCodeAgentNotFound {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, ErrCodeAgentNotFound)
	}
}

func TestHandleMarkMessageRead_ReturnsReadMetadata(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"mark_message_read": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.MessageReadResult{
				MessageID: 42,
				Read:      true,
				ReadAt:    &agentmail.FlexTime{Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
			}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/42/read?agent_name=BlueLake", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleMarkMessageRead(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Success   bool   `json:"success"`
		MessageID int    `json:"message_id"`
		Read      bool   `json:"read"`
		ReadAt    string `json:"read_at"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Success || !resp.Read || resp.MessageID != 42 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.ReadAt == "" {
		t.Fatal("expected read_at in response")
	}
}

func TestHandleMarkMessageRead_DefaultsMessageIDWhenToolReturnsNull(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"mark_message_read": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return nil, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/42/read?agent_name=BlueLake", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleMarkMessageRead(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Success   bool `json:"success"`
		MessageID int  `json:"message_id"`
		Read      bool `json:"read"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Success || resp.Read {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.MessageID != 42 {
		t.Fatalf("message_id = %d, want 42", resp.MessageID)
	}
}

func TestHandleAckMessage_ReturnsAckMetadata(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"acknowledge_message": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.MessageAckResult{
				MessageID:      42,
				Acknowledged:   true,
				AcknowledgedAt: &agentmail.FlexTime{Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
				ReadAt:         &agentmail.FlexTime{Time: time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)},
			}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/42/ack?agent_name=BlueLake", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleAckMessage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Success        bool   `json:"success"`
		MessageID      int    `json:"message_id"`
		Acknowledged   bool   `json:"acknowledged"`
		AcknowledgedAt string `json:"acknowledged_at"`
		ReadAt         string `json:"read_at"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Success || !resp.Acknowledged || resp.MessageID != 42 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.AcknowledgedAt == "" || resp.ReadAt == "" {
		t.Fatal("expected acknowledged_at and read_at in response")
	}
}

func TestHandleAckMessage_DefaultsMessageIDWhenToolReturnsNull(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"acknowledge_message": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return nil, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/42/ack?agent_name=BlueLake", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleAckMessage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Success      bool `json:"success"`
		MessageID    int  `json:"message_id"`
		Acknowledged bool `json:"acknowledged"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Success || resp.Acknowledged {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.MessageID != 42 {
		t.Fatalf("message_id = %d, want 42", resp.MessageID)
	}
}

func TestHandleSearchMessages_NotImplemented(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/search?q=test", nil)

	srv.handleSearchMessages(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusNotImplemented, rec.Body.String())
	}

	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorCode != ErrCodeNotImplemented {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, ErrCodeNotImplemented)
	}
}

func TestHandleMailInbox_InvalidSinceTS(t *testing.T) {
	t.Parallel()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL("http://127.0.0.1:8765/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/inbox?agent_name=BlueLake&since_ts=not-a-timestamp", nil)

	srv.handleMailInbox(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandleMailInbox_InvalidLimit(t *testing.T) {
	t.Parallel()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL("http://127.0.0.1:8765/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/inbox?agent_name=BlueLake&limit=zero", nil)

	srv.handleMailInbox(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandleMailInbox_InvalidUrgentOnly(t *testing.T) {
	t.Parallel()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL("http://127.0.0.1:8765/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/inbox?agent_name=BlueLake&urgent_only=maybe", nil)

	srv.handleMailInbox(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandleMailInbox_InvalidIncludeBodies(t *testing.T) {
	t.Parallel()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL("http://127.0.0.1:8765/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/inbox?agent_name=BlueLake&include_bodies=maybe", nil)

	srv.handleMailInbox(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandleSearchMessages_InvalidLimit(t *testing.T) {
	t.Parallel()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL("http://127.0.0.1:8765/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/search?q=test&limit=bogus", nil)

	srv.handleSearchMessages(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandleThreadSummary_InvalidLLMMode(t *testing.T) {
	t.Parallel()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL("http://127.0.0.1:8765/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/threads/TKT-123/summary?llm_mode=definitely", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "TKT-123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleThreadSummary(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandleThreadSummary_InvalidIncludeExamples(t *testing.T) {
	t.Parallel()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL("http://127.0.0.1:8765/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/threads/TKT-123/summary?include_examples=maybe", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "TKT-123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleThreadSummary(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandleThreadSummary_ForwardsExplicitLLMModeFalse(t *testing.T) {
	t.Parallel()

	var receivedArgs map[string]interface{}
	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"summarize_thread": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			receivedArgs = args
			return agentmail.ThreadSummaryResponse{
				ThreadID: args["thread_id"].(string),
				Summary: agentmail.ThreadSummary{
					Participants: []string{"BlueLake"},
				},
			}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/threads/TKT-123/summary?llm_mode=false", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "TKT-123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleThreadSummary(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got, ok := receivedArgs["llm_mode"]; !ok {
		t.Fatal("expected llm_mode argument to be forwarded")
	} else if got != false {
		t.Fatalf("llm_mode = %#v, want false", got)
	}
}

func TestHandleThreadSummary_OmitsIncludeExamplesWhenUnset(t *testing.T) {
	t.Parallel()

	var receivedArgs map[string]interface{}
	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"summarize_thread": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			receivedArgs = args
			return agentmail.ThreadSummaryResponse{
				ThreadID: args["thread_id"].(string),
				Summary: agentmail.ThreadSummary{
					Participants: []string{"BlueLake"},
				},
			}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/threads/TKT-123/summary", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "TKT-123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleThreadSummary(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if _, ok := receivedArgs["include_examples"]; ok {
		t.Fatal("did not expect include_examples argument when query param is omitted")
	}
}

func TestHandleThreadSummary_ReturnsExamplesWhenRequested(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"summarize_thread": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.ThreadSummaryResponse{
				ThreadID: args["thread_id"].(string),
				Summary: agentmail.ThreadSummary{
					ThreadID:     args["thread_id"].(string),
					Participants: []string{"BlueLake"},
				},
				Examples: []agentmail.InboxMessage{
					{ID: 7, Subject: "Example"},
				},
			}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/threads/TKT-123/summary?include_examples=true", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "TKT-123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleThreadSummary(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Success  bool                     `json:"success"`
		Summary  agentmail.ThreadSummary  `json:"summary"`
		Examples []agentmail.InboxMessage `json:"examples"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Success {
		t.Fatal("expected success=true")
	}
	if len(resp.Examples) != 1 {
		t.Fatalf("len(examples) = %d, want 1", len(resp.Examples))
	}
	if resp.Examples[0].Subject != "Example" {
		t.Fatalf("examples[0].subject = %q, want Example", resp.Examples[0].Subject)
	}
}

func TestHandleMarkMessageRead_DoesNotPublishEventWhenReadFalse(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"mark_message_read": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.MessageReadResult{
				MessageID: 42,
				Read:      false,
			}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()
	srv.wsHub = hub

	client := &WSClient{
		id:     "mail-read-watcher",
		hub:    hub,
		send:   make(chan []byte, 10),
		topics: make(map[string]struct{}),
	}
	client.Subscribe([]string{"mail:BlueLake"})
	requireRegisterWSClient(t, hub, client)
	time.Sleep(20 * time.Millisecond)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/42/read?agent_name=BlueLake", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleMarkMessageRead(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	time.Sleep(50 * time.Millisecond)
	select {
	case msg := <-client.send:
		t.Fatalf("unexpected mail.read event published: %s", string(msg))
	default:
	}
}

func TestHandleAckMessage_DoesNotPublishEventWhenAckFalse(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"acknowledge_message": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.MessageAckResult{
				MessageID:    42,
				Acknowledged: false,
			}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()
	srv.wsHub = hub

	client := &WSClient{
		id:     "mail-ack-watcher",
		hub:    hub,
		send:   make(chan []byte, 10),
		topics: make(map[string]struct{}),
	}
	client.Subscribe([]string{"mail:BlueLake"})
	requireRegisterWSClient(t, hub, client)
	time.Sleep(20 * time.Millisecond)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/42/ack?agent_name=BlueLake", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleAckMessage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	time.Sleep(50 * time.Millisecond)
	select {
	case msg := <-client.send:
		t.Fatalf("unexpected mail.acknowledged event published: %s", string(msg))
	default:
	}
}

func TestHandleListMailProjects_NotImplemented(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/projects", nil)

	srv.handleListMailProjects(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusNotImplemented, rec.Body.String())
	}

	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorCode != ErrCodeNotImplemented {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, ErrCodeNotImplemented)
	}
}

func TestHandleCreateMailAgent_InvalidRequest(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"register_agent": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return nil, &agentmail.JSONRPCError{
				Code:    -32602,
				Message: "invalid params: name must be adjective+noun",
			}
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/agents", strings.NewReader(`{"program":"claude-code","model":"opus-4.5","name":"bad-name"}`))

	srv.handleCreateMailAgent(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorCode != ErrCodeBadRequest {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, ErrCodeBadRequest)
	}
}

func TestHandleCreateMailAgent_TransientBusy(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"register_agent": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return nil, &agentmail.JSONRPCError{
				Code:    -32000,
				Message: "resource temporarily busy",
			}
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/agents", strings.NewReader(`{"program":"claude-code","model":"opus-4.5","name":"BlueLake"}`))

	srv.handleCreateMailAgent(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}

	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorCode != ErrCodeServiceUnavail {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, ErrCodeServiceUnavail)
	}
}

func TestHandleMailInbox_AgentNotFound(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"fetch_inbox": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return nil, &agentmail.JSONRPCError{
				Code:    -32000,
				Message: "agent not registered in project",
			}
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/inbox?agent_name=BlueLake", nil)

	srv.handleMailInbox(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorCode != ErrCodeAgentNotFound {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, ErrCodeAgentNotFound)
	}
}

func TestHandleMailInbox_DoesNotEmitMailReceivedEvent(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"fetch_inbox": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return []agentmail.InboxMessage{
				{ID: 1, Subject: "Hello", From: "BlueLake", Importance: "normal", Kind: "to"},
			}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))
	srv.wsHub = NewWSHub()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/inbox?agent_name=GreenStone", nil)

	srv.handleMailInbox(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(srv.wsHub.broadcast); got != 0 {
		t.Fatalf("expected inbox reads to emit no mail event, found %d queued events", got)
	}
}

func TestHandleRequestContact_ReturnsLinkAndExpiry(t *testing.T) {
	t.Parallel()

	expiresAt := "2026-03-22T12:34:56Z"
	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"request_contact": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.ContactRequestResult{
				Status: "pending",
				Link: &agentmail.ContactLink{
					FromAgent: "BlueLake",
					ToAgent:   "RedStone",
					Status:    "pending",
				},
				ExpiresTS: &expiresAt,
			}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/contacts/request", strings.NewReader(`{"from_agent":"BlueLake","to_agent":"RedStone","reason":"coordination"}`))

	srv.handleRequestContact(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "pending" {
		t.Fatalf("status = %v, want pending", resp["status"])
	}
	if resp["expires_ts"] != expiresAt {
		t.Fatalf("expires_ts = %v, want %s", resp["expires_ts"], expiresAt)
	}
	link, ok := resp["link"].(map[string]interface{})
	if !ok {
		t.Fatalf("link = %#v, want object", resp["link"])
	}
	if link["from_agent"] != "BlueLake" || link["to_agent"] != "RedStone" {
		t.Fatalf("unexpected link payload: %#v", link)
	}
}

func TestHandleRequestContact_NotImplemented(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/contacts/request", strings.NewReader(`{"from_agent":"BlueLake","to_agent":"RedStone"}`))

	srv.handleRequestContact(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusNotImplemented, rec.Body.String())
	}

	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorCode != ErrCodeNotImplemented {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, ErrCodeNotImplemented)
	}
}

func TestHandleRequestContact_AgentNotFound(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"request_contact": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return nil, &agentmail.JSONRPCError{
				Code:    -32000,
				Message: "agent not registered: RedStone",
			}
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/contacts/request", strings.NewReader(`{"from_agent":"BlueLake","to_agent":"RedStone"}`))

	srv.handleRequestContact(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorCode != ErrCodeAgentNotFound {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, ErrCodeAgentNotFound)
	}
}

func TestHandleRespondContact_NotImplemented(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/contacts/respond", strings.NewReader(`{"to_agent":"BlueLake","from_agent":"RedStone","accept":true}`))

	srv.handleRespondContact(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusNotImplemented, rec.Body.String())
	}

	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorCode != ErrCodeNotImplemented {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, ErrCodeNotImplemented)
	}
}

func TestHandleRespondContact_ReturnsStatus(t *testing.T) {
	t.Parallel()

	expiresAt := "2026-03-23T00:00:00Z"
	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"respond_contact": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.ContactRespondResult{
				Status: "approved",
				Link: &agentmail.ContactLink{
					FromAgent: "RedStone",
					ToAgent:   "BlueLake",
					Status:    "approved",
				},
				ExpiresTS: &expiresAt,
			}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/contacts/respond", strings.NewReader(`{"to_agent":"BlueLake","from_agent":"RedStone","accept":true}`))

	srv.handleRespondContact(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "approved" {
		t.Fatalf("status = %v, want approved", resp["status"])
	}
	if resp["expires_ts"] != expiresAt {
		t.Fatalf("expires_ts = %v, want %s", resp["expires_ts"], expiresAt)
	}
	link, ok := resp["link"].(map[string]interface{})
	if !ok {
		t.Fatalf("link = %#v, want object", resp["link"])
	}
	if link["from_agent"] != "RedStone" || link["to_agent"] != "BlueLake" {
		t.Fatalf("unexpected link payload: %#v", link)
	}
}

func TestHandleRespondContact_NotFound(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"respond_contact": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return nil, &agentmail.JSONRPCError{
				Code:    -32000,
				Message: "contact request not found",
			}
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/contacts/respond", strings.NewReader(`{"to_agent":"BlueLake","from_agent":"RedStone","accept":true}`))

	srv.handleRespondContact(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorCode != ErrCodeNotFound {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, ErrCodeNotFound)
	}
}

func TestHandleForceReleaseReservation_ReturnsMCPResultFields(t *testing.T) {
	t.Parallel()

	releasedAt := "2026-03-23T01:02:03Z"
	releasedTime, err := time.Parse(time.RFC3339, releasedAt)
	if err != nil {
		t.Fatalf("parse released_at: %v", err)
	}
	releasedFlexTime := agentmail.FlexTime{Time: releasedTime}
	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"force_release_file_reservation": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.ForceReleaseResult{
				Success:        true,
				ReleasedAt:     &releasedFlexTime,
				PreviousHolder: "BlueLake",
				PathPattern:    "internal/serve/*",
				Notified:       false,
			}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations/17/force-release", strings.NewReader(`{"agent_name":"RedStone","notify_previous":true}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "17")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleForceReleaseReservation(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["force_released"] != true {
		t.Fatalf("force_released = %#v, want true", resp["force_released"])
	}
	if resp["previous_holder"] != "BlueLake" {
		t.Fatalf("previous_holder = %#v, want BlueLake", resp["previous_holder"])
	}
	if resp["path_pattern"] != "internal/serve/*" {
		t.Fatalf("path_pattern = %#v, want internal/serve/*", resp["path_pattern"])
	}
	if resp["notified"] != false {
		t.Fatalf("notified = %#v, want false", resp["notified"])
	}
	if resp["released_at"] != releasedAt {
		t.Fatalf("released_at = %#v, want %s", resp["released_at"], releasedAt)
	}
}

func TestHandleForceReleaseReservation_DeniedReturnsConflict(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"force_release_file_reservation": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.ForceReleaseResult{
				Success:        false,
				PreviousHolder: "BlueLake",
				PathPattern:    "internal/serve/*",
				Notified:       false,
			}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations/17/force-release", strings.NewReader(`{"agent_name":"RedStone","notify_previous":true}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "17")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleForceReleaseReservation(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusConflict, rec.Body.String())
	}

	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorCode != ErrCodeConflict {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, ErrCodeConflict)
	}
	if resp.Details["previous_holder"] != "BlueLake" {
		t.Fatalf("previous_holder = %#v, want BlueLake", resp.Details["previous_holder"])
	}
	if resp.Details["path_pattern"] != "internal/serve/*" {
		t.Fatalf("path_pattern = %#v, want internal/serve/*", resp.Details["path_pattern"])
	}
}

func TestHandleRenewReservation_NotFoundOnZeroRenew(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"renew_file_reservations": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.RenewReservationsResult{}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations/42/renew", strings.NewReader(`{"agent_name":"BlueLake","extend_seconds":900}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleRenewReservation(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ErrorCode != ErrCodeNotFound {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, ErrCodeNotFound)
	}
}

func TestHandleSetContactPolicy_ReturnsMCPResultFields(t *testing.T) {
	t.Parallel()

	mcpServer := newMockAgentMailMCPServer(t, map[string]func(map[string]interface{}) (interface{}, *agentmail.JSONRPCError){
		"set_contact_policy": func(args map[string]interface{}) (interface{}, *agentmail.JSONRPCError) {
			return agentmail.ContactPolicyResult{
				AgentName: "BlueLake",
				Policy:    "contacts_only",
			}, nil
		},
	})
	defer mcpServer.Close()

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	srv.mailClient = agentmail.NewClient(agentmail.WithBaseURL(mcpServer.URL + "/"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/mail/contacts/policy", strings.NewReader(`{"agent_name":"BlueLake","policy":"open"}`))

	srv.handleSetContactPolicy(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["agent_name"] != "BlueLake" {
		t.Fatalf("agent_name = %#v, want BlueLake", resp["agent_name"])
	}
	if resp["policy"] != "contacts_only" {
		t.Fatalf("policy = %#v, want contacts_only", resp["policy"])
	}
}

func TestParseAuthMode(t *testing.T) {
	tests := []struct {
		raw     string
		expect  AuthMode
		wantErr bool
	}{
		{"", AuthModeLocal, false},
		{"local", AuthModeLocal, false},
		{"LOCAL", AuthModeLocal, false},
		{"api_key", AuthModeAPIKey, false},
		{"oidc", AuthModeOIDC, false},
		{"mtls", AuthModeMTLS, false},
		{"invalid", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			mode, err := ParseAuthMode(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.raw, err)
			}
			if mode != tt.expect {
				t.Fatalf("ParseAuthMode(%q) = %q, want %q", tt.raw, mode, tt.expect)
			}
		})
	}
}

func TestDefaultLocalOrigins(t *testing.T) {
	origins := defaultLocalOrigins()
	if len(origins) == 0 {
		t.Fatal("expected defaultLocalOrigins to return entries")
	}
	want := []string{
		"http://localhost",
		"http://127.0.0.1",
		"http://[::1]",
		"https://localhost",
		"https://127.0.0.1",
		"https://[::1]",
	}
	for _, expected := range want {
		found := false
		for _, origin := range origins {
			if origin == expected {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing default origin %q", expected)
		}
	}
}

func TestHealthEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	srv.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}
	if resp["status"] != "healthy" {
		t.Error("Expected status=healthy")
	}
}

func TestHealthEndpointMethodNotAllowed(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	rec := httptest.NewRecorder()

	srv.handleHealth(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestSessionsEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rec := httptest.NewRecorder()

	srv.handleSessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}
	if _, ok := resp["sessions"]; !ok {
		t.Error("Expected sessions field")
	}
}

func TestHandleListBeadsStub(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub br uses sh")
	}
	writeStubBr(t, "bd-1")

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads", nil)
	rec := httptest.NewRecorder()
	srv.handleListBeads(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := int(resp["count"].(float64)); got != 1 {
		t.Fatalf("count=%d, want 1", got)
	}
}

func TestHandleListBeadsStubWithLabelFilter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub br uses sh")
	}
	writeStubBr(t, "bd-1")

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads?label=api", nil)
	rec := httptest.NewRecorder()
	srv.handleListBeads(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := int(resp["count"].(float64)); got != 1 {
		t.Fatalf("count=%d, want 1", got)
	}
}

func TestHandleCreateBeadSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub br uses sh")
	}
	writeStubBr(t, "bd-2")

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/beads", strings.NewReader(`{"title":"Test bead","priority":"P2","labels":["api","triaged"],"blocked_by":["bd-dep"]}`))
	rec := httptest.NewRecorder()
	srv.handleCreateBead(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusCreated)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["bead"]; !ok {
		t.Fatalf("missing bead in response")
	}
	bead, ok := resp["bead"].(map[string]interface{})
	if !ok {
		t.Fatalf("bead payload type = %T, want object", resp["bead"])
	}
	if bead["id"] != "bd-2" {
		t.Fatalf("bead id = %v, want %q", bead["id"], "bd-2")
	}
}

func TestHandleBeadsStatsAndReady(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub br uses sh")
	}
	writeStubBr(t, "bd-3")

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()

	statsReq := httptest.NewRequest(http.MethodGet, "/api/v1/beads/stats", nil)
	statsRec := httptest.NewRecorder()
	srv.handleBeadsStats(statsRec, statsReq)
	if statsRec.Code != http.StatusOK {
		t.Fatalf("stats status=%d, want %d", statsRec.Code, http.StatusOK)
	}
	var statsResp map[string]interface{}
	if err := json.NewDecoder(statsRec.Body).Decode(&statsResp); err != nil {
		t.Fatalf("decode stats response: %v", err)
	}
	if _, ok := statsResp["stats"]; !ok {
		t.Fatalf("missing stats in response")
	}

	readyReq := httptest.NewRequest(http.MethodGet, "/api/v1/beads/ready", nil)
	readyRec := httptest.NewRecorder()
	srv.handleBeadsReady(readyRec, readyReq)
	if readyRec.Code != http.StatusOK {
		t.Fatalf("ready status=%d, want %d", readyRec.Code, http.StatusOK)
	}
	var readyResp map[string]interface{}
	if err := json.NewDecoder(readyRec.Body).Decode(&readyResp); err != nil {
		t.Fatalf("decode ready response: %v", err)
	}
	if got := int(readyResp["count"].(float64)); got != 0 {
		t.Fatalf("ready count=%d, want 0", got)
	}
}

func TestHandleBeadsBlockedAndInProgress(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub br uses sh")
	}
	writeStubBr(t, "bd-4")

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()

	blockedReq := httptest.NewRequest(http.MethodGet, "/api/v1/beads/blocked", nil)
	blockedRec := httptest.NewRecorder()
	srv.handleBeadsBlocked(blockedRec, blockedReq)
	if blockedRec.Code != http.StatusOK {
		t.Fatalf("blocked status=%d, want %d", blockedRec.Code, http.StatusOK)
	}
	var blockedResp map[string]interface{}
	if err := json.NewDecoder(blockedRec.Body).Decode(&blockedResp); err != nil {
		t.Fatalf("decode blocked response: %v", err)
	}
	if got := int(blockedResp["count"].(float64)); got != 0 {
		t.Fatalf("blocked count=%d, want 0", got)
	}

	inProgressReq := httptest.NewRequest(http.MethodGet, "/api/v1/beads/in-progress", nil)
	inProgressRec := httptest.NewRecorder()
	srv.handleBeadsInProgress(inProgressRec, inProgressReq)
	if inProgressRec.Code != http.StatusOK {
		t.Fatalf("in-progress status=%d, want %d", inProgressRec.Code, http.StatusOK)
	}
	var inProgressResp map[string]interface{}
	if err := json.NewDecoder(inProgressRec.Body).Decode(&inProgressResp); err != nil {
		t.Fatalf("decode in-progress response: %v", err)
	}
	if got := int(inProgressResp["count"].(float64)); got != 1 {
		t.Fatalf("in-progress count=%d, want 1", got)
	}
}

func TestHandleGetUpdateCloseClaimBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub br uses sh")
	}
	writeStubBr(t, "bd-5")

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/beads/bd-5", nil)
	getCtx := chi.NewRouteContext()
	getCtx.URLParams.Add("id", "bd-5")
	getReq = getReq.WithContext(context.WithValue(getReq.Context(), chi.RouteCtxKey, getCtx))
	getRec := httptest.NewRecorder()
	srv.handleGetBead(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status=%d, want %d", getRec.Code, http.StatusOK)
	}
	var getResp map[string]interface{}
	if err := json.NewDecoder(getRec.Body).Decode(&getResp); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	bead, ok := getResp["bead"].(map[string]interface{})
	if !ok {
		t.Fatalf("get bead payload type = %T, want object", getResp["bead"])
	}
	if bead["id"] != "bd-5" {
		t.Fatalf("get bead id = %v, want %q", bead["id"], "bd-5")
	}

	updateReq := httptest.NewRequest(http.MethodPatch, "/api/v1/beads/bd-5", strings.NewReader(`{"title":"Updated","labels":["api","triaged"]}`))
	upCtx := chi.NewRouteContext()
	upCtx.URLParams.Add("id", "bd-5")
	updateReq = updateReq.WithContext(context.WithValue(updateReq.Context(), chi.RouteCtxKey, upCtx))
	updateRec := httptest.NewRecorder()
	srv.handleUpdateBead(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status=%d, want %d", updateRec.Code, http.StatusOK)
	}
	var updateResp map[string]interface{}
	if err := json.NewDecoder(updateRec.Body).Decode(&updateResp); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	updatedBead, ok := updateResp["bead"].(map[string]interface{})
	if !ok {
		t.Fatalf("update bead payload type = %T, want object", updateResp["bead"])
	}
	if updatedBead["title"] != "Updated" {
		t.Fatalf("updated bead title = %v, want %q", updatedBead["title"], "Updated")
	}

	closeReq := httptest.NewRequest(http.MethodPost, "/api/v1/beads/bd-5/close", nil)
	closeCtx := chi.NewRouteContext()
	closeCtx.URLParams.Add("id", "bd-5")
	closeReq = closeReq.WithContext(context.WithValue(closeReq.Context(), chi.RouteCtxKey, closeCtx))
	closeRec := httptest.NewRecorder()
	srv.handleCloseBead(closeRec, closeReq)
	if closeRec.Code != http.StatusOK {
		t.Fatalf("close status=%d, want %d", closeRec.Code, http.StatusOK)
	}

	claimReq := httptest.NewRequest(http.MethodPost, "/api/v1/beads/bd-5/claim", strings.NewReader(`{"assignee":"tester"}`))
	claimCtx := chi.NewRouteContext()
	claimCtx.URLParams.Add("id", "bd-5")
	claimReq = claimReq.WithContext(context.WithValue(claimReq.Context(), chi.RouteCtxKey, claimCtx))
	claimRec := httptest.NewRecorder()
	srv.handleClaimBead(claimRec, claimReq)
	if claimRec.Code != http.StatusOK {
		t.Fatalf("claim status=%d, want %d", claimRec.Code, http.StatusOK)
	}
}

func TestHandleListAddRemoveBeadDepsWithStubBr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub br uses sh")
	}
	writeStubBr(t, "bd-7")

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/beads/bd-7/deps", nil)
	listCtx := chi.NewRouteContext()
	listCtx.URLParams.Add("id", "bd-7")
	listReq = listReq.WithContext(context.WithValue(listReq.Context(), chi.RouteCtxKey, listCtx))
	listRec := httptest.NewRecorder()
	srv.handleListBeadDeps(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list deps status=%d, want %d", listRec.Code, http.StatusOK)
	}
	var listResp map[string]interface{}
	if err := json.NewDecoder(listRec.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode list deps response: %v", err)
	}
	deps, ok := listResp["dependencies"].([]interface{})
	if !ok || len(deps) != 1 {
		t.Fatalf("dependencies payload = %#v, want single dependency row", listResp["dependencies"])
	}

	addReq := httptest.NewRequest(http.MethodPost, "/api/v1/beads/bd-7/deps", strings.NewReader(`{"blocked_by":"bd-dep"}`))
	addCtx := chi.NewRouteContext()
	addCtx.URLParams.Add("id", "bd-7")
	addReq = addReq.WithContext(context.WithValue(addReq.Context(), chi.RouteCtxKey, addCtx))
	addRec := httptest.NewRecorder()
	srv.handleAddBeadDep(addRec, addReq)
	if addRec.Code != http.StatusCreated {
		t.Fatalf("add dep status=%d, want %d", addRec.Code, http.StatusCreated)
	}

	removeReq := httptest.NewRequest(http.MethodDelete, "/api/v1/beads/bd-7/deps/bd-dep", nil)
	removeCtx := chi.NewRouteContext()
	removeCtx.URLParams.Add("id", "bd-7")
	removeCtx.URLParams.Add("depId", "bd-dep")
	removeReq = removeReq.WithContext(context.WithValue(removeReq.Context(), chi.RouteCtxKey, removeCtx))
	removeRec := httptest.NewRecorder()
	srv.handleRemoveBeadDep(removeRec, removeReq)
	if removeRec.Code != http.StatusOK {
		t.Fatalf("remove dep status=%d, want %d", removeRec.Code, http.StatusOK)
	}
}

func TestHandleBeadsDaemonAndSync(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub br uses sh")
	}
	writeStubBr(t, "bd-6")

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()

	statusReq := httptest.NewRequest(http.MethodGet, "/api/v1/beads/daemon/status", nil)
	statusRec := httptest.NewRecorder()
	srv.handleBeadsDaemonStatus(statusRec, statusReq)
	if statusRec.Code != http.StatusNotImplemented {
		t.Fatalf("daemon status=%d, want %d", statusRec.Code, http.StatusNotImplemented)
	}
	var statusResp map[string]interface{}
	if err := json.NewDecoder(statusRec.Body).Decode(&statusResp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if statusResp["error_code"] != ErrCodeNotImplemented {
		t.Fatalf("daemon status error_code=%v, want %q", statusResp["error_code"], ErrCodeNotImplemented)
	}

	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/beads/daemon/start", nil)
	startRec := httptest.NewRecorder()
	srv.handleBeadsDaemonStart(startRec, startReq)
	if startRec.Code != http.StatusNotImplemented {
		t.Fatalf("daemon start=%d, want %d", startRec.Code, http.StatusNotImplemented)
	}

	stopReq := httptest.NewRequest(http.MethodPost, "/api/v1/beads/daemon/stop", nil)
	stopRec := httptest.NewRecorder()
	srv.handleBeadsDaemonStop(stopRec, stopReq)
	if stopRec.Code != http.StatusNotImplemented {
		t.Fatalf("daemon stop=%d, want %d", stopRec.Code, http.StatusNotImplemented)
	}

	syncReq := httptest.NewRequest(http.MethodPost, "/api/v1/beads/sync", nil)
	syncRec := httptest.NewRecorder()
	srv.handleBeadsSync(syncRec, syncReq)
	if syncRec.Code != http.StatusOK {
		t.Fatalf("sync status=%d, want %d", syncRec.Code, http.StatusOK)
	}
}

func TestSessionsEndpointNoStore(t *testing.T) {
	srv := New(Config{})

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rec := httptest.NewRecorder()

	srv.handleSessions(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestSessionEndpointNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/nonexistent", nil)
	rec := httptest.NewRecorder()

	srv.handleSession(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestSessionEndpointMissingID(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/", nil)
	rec := httptest.NewRecorder()

	srv.handleSession(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestRobotStatusEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/robot/status", nil)
	rec := httptest.NewRecorder()

	srv.handleRobotStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}
}

func TestKernelCommandsEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/kernel/commands", nil)
	rec := httptest.NewRecorder()

	srv.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp struct {
		Success  bool `json:"success"`
		Count    int  `json:"count"`
		Commands []struct {
			Name string `json:"name"`
		} `json:"commands"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if !resp.Success {
		t.Fatal("Expected success=true")
	}
	if resp.Count != len(resp.Commands) {
		t.Fatalf("Count = %d, want len(commands)=%d", resp.Count, len(resp.Commands))
	}

	expected := kernel.List()
	if resp.Count != len(expected) {
		t.Fatalf("Count = %d, want %d", resp.Count, len(expected))
	}
	for i, cmd := range expected {
		if i >= len(resp.Commands) {
			t.Fatalf("missing command at index %d", i)
		}
		if resp.Commands[i].Name != cmd.Name {
			t.Fatalf("Command[%d] = %q, want %q", i, resp.Commands[i].Name, cmd.Name)
		}
	}
}

func TestRobotHealthEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/robot/health", nil)
	rec := httptest.NewRecorder()

	srv.handleRobotHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestSessionEventsEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Publish an event
	srv.eventBus.Publish(events.BaseEvent{
		Type:      "test_event",
		Timestamp: time.Now().UTC(),
		Session:   "test-session",
	})

	// Give event time to be recorded
	time.Sleep(10 * time.Millisecond)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/test-session/events", nil)
	rec := httptest.NewRecorder()

	srv.handleSessionEvents(rec, req, "test-session")

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}
}

func TestSSEClientManagement(t *testing.T) {
	srv, _ := setupTestServer(t)

	ch := make(chan events.BusEvent, 10)
	srv.addSSEClient(ch)

	srv.sseClientsMu.RLock()
	clientCount := len(srv.sseClients)
	srv.sseClientsMu.RUnlock()

	if clientCount != 1 {
		t.Errorf("Client count = %d, want 1", clientCount)
	}

	srv.removeSSEClient(ch)

	srv.sseClientsMu.RLock()
	clientCount = len(srv.sseClients)
	srv.sseClientsMu.RUnlock()

	if clientCount != 0 {
		t.Errorf("Client count = %d, want 0 after removal", clientCount)
	}
}

func TestBroadcastEvent(t *testing.T) {
	srv, _ := setupTestServer(t)

	ch := make(chan events.BusEvent, 10)
	srv.addSSEClient(ch)
	defer srv.removeSSEClient(ch)

	testEvent := events.BaseEvent{
		Type:      "broadcast_test",
		Timestamp: time.Now().UTC(),
		Session:   "test",
	}

	srv.broadcastEvent(testEvent)

	select {
	case e := <-ch:
		if e.EventType() != "broadcast_test" {
			t.Errorf("Event type = %s, want broadcast_test", e.EventType())
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Timeout waiting for broadcast")
	}
}

func TestEventStreamSSE(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Create a request with a cancelable context
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	// Start the handler in a goroutine
	done := make(chan struct{})
	go func() {
		srv.handleEventStream(rec, req)
		close(done)
	}()

	// Give time for connection setup
	time.Sleep(50 * time.Millisecond)

	// Cancel to end the request
	cancel()

	// Wait for handler to complete
	select {
	case <-done:
		// OK
	case <-time.After(1 * time.Second):
		t.Error("Handler did not complete after context cancel")
	}

	// Check headers
	contentType := rec.Header().Get("Content-Type")
	if contentType != "text/event-stream" {
		t.Errorf("Content-Type = %s, want text/event-stream", contentType)
	}

	// Check for connected event
	body, _ := io.ReadAll(rec.Body)
	if len(body) == 0 {
		t.Error("Expected some output from SSE stream")
	}
}

func installServeTestAttentionFeed(t *testing.T) (*robot.AttentionFeed, robot.JournalStats) {
	t.Helper()

	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{
		JournalSize:       2,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	oldFeed := robot.GetAttentionFeed()
	robot.SetAttentionFeed(feed)
	t.Cleanup(func() {
		robot.SetAttentionFeed(oldFeed)
		feed.Stop()
	})

	for i := 0; i < 4; i++ {
		feed.Append(robot.AttentionEvent{
			Session:       "proj",
			Pane:          2,
			Category:      robot.EventCategoryAlert,
			Type:          robot.EventTypeAlertAttentionRequired,
			Actionability: robot.ActionabilityActionRequired,
			Severity:      robot.SeverityWarning,
			Summary:       "operator attention item",
		})
	}

	stats := feed.Stats()
	if stats.Count != 2 {
		t.Fatalf("journal count = %d, want 2 retained events", stats.Count)
	}
	if stats.OldestCursor < 2 {
		t.Fatalf("oldest cursor = %d, want wrapped journal", stats.OldestCursor)
	}

	return feed, stats
}

func expiredServeTestAttentionCursor(t *testing.T, feed *robot.AttentionFeed) (robot.JournalStats, int64) {
	t.Helper()

	stats := feed.Stats()
	for stats.OldestCursor < 3 {
		feed.Append(robot.AttentionEvent{
			Session:       "proj",
			Pane:          2,
			Category:      robot.EventCategoryAlert,
			Type:          robot.EventTypeAlertAttentionRequired,
			Actionability: robot.ActionabilityActionRequired,
			Severity:      robot.SeverityWarning,
			Summary:       "operator attention item",
		})
		stats = feed.Stats()
	}

	sinceCursor := stats.OldestCursor - 2
	if sinceCursor <= 0 {
		t.Fatalf("expired test cursor = %d, need positive expired cursor", sinceCursor)
	}
	return stats, sinceCursor
}

func TestAttentionEventsAcceptsBoundaryCursor(t *testing.T) {
	srv, _ := setupTestServer(t)
	_, stats := installServeTestAttentionFeed(t)

	sinceCursor := stats.OldestCursor - 1
	req := httptest.NewRequest(http.MethodGet, "/api/v1/attention/events", nil)
	query := req.URL.Query()
	query.Set("since_cursor", strconv.FormatInt(sinceCursor, 10))
	req.URL.RawQuery = query.Encode()

	rec := httptest.NewRecorder()
	srv.handleAttentionEventsV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Events       []robot.AttentionEvent `json:"events"`
		SinceCursor  int64                  `json:"since_cursor"`
		NewestCursor int64                  `json:"newest_cursor"`
		OldestCursor int64                  `json:"oldest_cursor"`
		EventCount   int                    `json:"event_count"`
		Truncated    bool                   `json:"truncated"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.SinceCursor != sinceCursor {
		t.Fatalf("since_cursor = %d, want %d", resp.SinceCursor, sinceCursor)
	}
	if resp.OldestCursor != stats.OldestCursor {
		t.Fatalf("oldest_cursor = %d, want %d", resp.OldestCursor, stats.OldestCursor)
	}
	if resp.NewestCursor != stats.NewestCursor {
		t.Fatalf("newest_cursor = %d, want %d", resp.NewestCursor, stats.NewestCursor)
	}
	if resp.EventCount != stats.Count {
		t.Fatalf("event_count = %d, want %d", resp.EventCount, stats.Count)
	}
	if len(resp.Events) != stats.Count {
		t.Fatalf("events = %d, want %d", len(resp.Events), stats.Count)
	}
	if resp.Events[0].Cursor != stats.OldestCursor {
		t.Fatalf("first replay cursor = %d, want %d", resp.Events[0].Cursor, stats.OldestCursor)
	}
	if resp.Truncated {
		t.Fatal("boundary replay should not be truncated")
	}
}

func TestAttentionDigestAcceptsBoundaryCursor(t *testing.T) {
	srv, _ := setupTestServer(t)
	_, stats := installServeTestAttentionFeed(t)

	sinceCursor := stats.OldestCursor - 1
	req := httptest.NewRequest(http.MethodGet, "/api/v1/attention/digest", nil)
	query := req.URL.Query()
	query.Set("since_cursor", strconv.FormatInt(sinceCursor, 10))
	req.URL.RawQuery = query.Encode()

	rec := httptest.NewRecorder()
	srv.handleAttentionDigestV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		CursorStart int64 `json:"cursor_start"`
		CursorEnd   int64 `json:"cursor_end"`
		EventCount  int   `json:"event_count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.CursorStart != stats.OldestCursor {
		t.Fatalf("cursor_start = %d, want %d", resp.CursorStart, stats.OldestCursor)
	}
	if resp.CursorEnd != stats.NewestCursor {
		t.Fatalf("cursor_end = %d, want %d", resp.CursorEnd, stats.NewestCursor)
	}
	if resp.EventCount != stats.Count {
		t.Fatalf("event_count = %d, want %d", resp.EventCount, stats.Count)
	}
}

func TestAttentionStreamAcceptsBoundaryCursor(t *testing.T) {
	srv, _ := setupTestServer(t)
	_, stats := installServeTestAttentionFeed(t)

	sinceCursor := stats.OldestCursor - 1
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/attention/stream", nil).WithContext(ctx)
	query := req.URL.Query()
	query.Set("since_cursor", strconv.FormatInt(sinceCursor, 10))
	req.URL.RawQuery = query.Encode()

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.handleAttentionStreamV1(rec, req)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("attention stream handler did not exit after cancel")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: connected") {
		t.Fatalf("stream body missing connected event: %s", body)
	}
	if !strings.Contains(body, "event: attention") {
		t.Fatalf("stream body missing replayed attention event: %s", body)
	}
	if strings.Contains(body, robot.ErrCodeCursorExpired) {
		t.Fatalf("stream incorrectly treated boundary cursor as expired: %s", body)
	}
}

func TestAttentionStreamSuppressesFeedHeartbeatEvents(t *testing.T) {
	srv, _ := setupTestServer(t)

	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{
		JournalSize:       10,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 5 * time.Millisecond,
	})
	oldFeed := robot.GetAttentionFeed()
	robot.SetAttentionFeed(feed)
	t.Cleanup(func() {
		robot.SetAttentionFeed(oldFeed)
		feed.Stop()
	})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/attention/stream", nil).WithContext(ctx)
	query := req.URL.Query()
	query.Set("heartbeat", "0")
	req.URL.RawQuery = query.Encode()

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.handleAttentionStreamV1(rec, req)
		close(done)
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("attention stream handler did not exit after cancel")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: connected") {
		t.Fatalf("stream body missing connected event: %s", body)
	}
	if strings.Contains(body, `"type":"system.heartbeat"`) {
		t.Fatalf("stream body should not include feed heartbeat as attention event: %s", body)
	}
}

func TestAttentionStreamHeartbeatIncludesCounters(t *testing.T) {
	srv, _ := setupTestServer(t)

	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{
		JournalSize:       10,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	oldFeed := robot.GetAttentionFeed()
	robot.SetAttentionFeed(feed)
	t.Cleanup(func() {
		robot.SetAttentionFeed(oldFeed)
		feed.Stop()
	})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/attention/stream", nil).WithContext(ctx)
	query := req.URL.Query()
	query.Set("heartbeat", "1")
	req.URL.RawQuery = query.Encode()

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.handleAttentionStreamV1(rec, req)
		close(done)
	}()

	time.Sleep(1100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("attention stream handler did not exit after cancel")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: heartbeat") {
		t.Fatalf("stream body missing heartbeat event: %s", body)
	}
	for _, want := range []string{
		`"stream_id":"watch_`,
		`"cursor_position":0`,
		`"events_since_start":0`,
		`"subscriber_count":1`,
		`"next_heartbeat_ms":1000`,
		`"events_since_last_heartbeat":0`,
		`"filtered_since_last":0`,
		`"dropped_since_last":0`,
		`"subscription_active":true`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream body missing %s: %s", want, body)
		}
	}
}

func TestAttentionStreamHeartbeatIncludesSourceHealth(t *testing.T) {
	srv, store := setupTestServer(t)

	now := time.Now().UTC()
	successAt := now.Add(-30 * time.Second)
	failureAt := now.Add(-15 * time.Second)
	for _, health := range []*state.SourceHealth{
		{
			SourceName:    "beads",
			Available:     true,
			Healthy:       true,
			LastSuccessAt: &successAt,
			LastCheckAt:   now,
		},
		{
			SourceName:          "mail",
			Available:           true,
			Healthy:             false,
			Reason:              "rate limited",
			LastFailureAt:       &failureAt,
			LastCheckAt:         now,
			ConsecutiveFailures: 2,
			LastError:           "mail quota exceeded",
			LastErrorCode:       "quota:rate_limited",
		},
		{
			SourceName:          "tmux",
			Available:           false,
			Healthy:             false,
			Reason:              "socket unavailable",
			LastFailureAt:       &failureAt,
			LastCheckAt:         now,
			ConsecutiveFailures: 1,
			LastError:           "tmux socket missing",
			LastErrorCode:       "tmux:disconnected",
		},
	} {
		if err := store.UpsertSourceHealth(health); err != nil {
			t.Fatalf("UpsertSourceHealth(%s): %v", health.SourceName, err)
		}
	}

	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{
		JournalSize:       10,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	oldFeed := robot.GetAttentionFeed()
	robot.SetAttentionFeed(feed)
	t.Cleanup(func() {
		robot.SetAttentionFeed(oldFeed)
		feed.Stop()
	})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/attention/stream", nil).WithContext(ctx)
	query := req.URL.Query()
	query.Set("heartbeat", "1")
	req.URL.RawQuery = query.Encode()

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.handleAttentionStreamV1(rec, req)
		close(done)
	}()

	time.Sleep(1100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("attention stream handler did not exit after cancel")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: heartbeat") {
		t.Fatalf("stream body missing heartbeat event: %s", body)
	}
	for _, want := range []string{
		`"sources_healthy":1`,
		`"sources_degraded":2`,
		`"sources_unavailable":1`,
		`"degraded_reasons":["quota:rate_limited","tmux:disconnected"]`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream body missing %s: %s", want, body)
		}
	}
}

func TestAttentionStreamHeartbeatIncludesLastEventTimeAfterRealEvent(t *testing.T) {
	srv, _ := setupTestServer(t)

	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{
		JournalSize:       10,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	feed.Append(robot.AttentionEvent{
		Session:       "proj",
		Pane:          2,
		Category:      robot.EventCategoryAlert,
		Type:          robot.EventTypeAlertAttentionRequired,
		Actionability: robot.ActionabilityActionRequired,
		Severity:      robot.SeverityWarning,
		Summary:       "operator attention item",
	})
	oldFeed := robot.GetAttentionFeed()
	robot.SetAttentionFeed(feed)
	t.Cleanup(func() {
		robot.SetAttentionFeed(oldFeed)
		feed.Stop()
	})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/attention/stream", nil).WithContext(ctx)
	query := req.URL.Query()
	query.Set("heartbeat", "1")
	req.URL.RawQuery = query.Encode()

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.handleAttentionStreamV1(rec, req)
		close(done)
	}()

	time.Sleep(1100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("attention stream handler did not exit after cancel")
	}

	body := rec.Body.String()
	for _, want := range []string{
		`"cursor_position":1`,
		`"events_since_start":1`,
		`"last_event_time":"`,
		`"idle_ms":`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream body missing %s: %s", want, body)
		}
	}
}

func TestAttentionEventsAsOfIncludesHistoricalMetadata(t *testing.T) {
	srv, store := setupTestServer(t)

	robot.SetProjectionStore(store)
	t.Cleanup(func() {
		robot.SetProjectionStore(nil)
	})

	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	}, robot.WithAttentionStore(store))
	oldFeed := robot.GetAttentionFeed()
	robot.SetAttentionFeed(feed)
	t.Cleanup(func() {
		robot.SetAttentionFeed(oldFeed)
		feed.Stop()
	})

	now := time.Now().UTC().Truncate(time.Second)
	appendStored := func(ts time.Time, summary string) {
		t.Helper()
		if _, err := store.AppendAttentionEvent(&state.StoredAttentionEvent{
			Ts:            ts,
			SessionName:   "proj",
			Category:      "incident",
			EventType:     "incident.replayed",
			Source:        "test",
			Actionability: state.ActionabilityInteresting,
			Severity:      state.SeverityWarning,
			Summary:       summary,
		}); err != nil {
			t.Fatalf("AppendAttentionEvent(%q): %v", summary, err)
		}
	}

	appendStored(now.Add(-40*time.Second), "older")
	appendStored(now.Add(-20*time.Second), "target-1")
	appendStored(now.Add(-15*time.Second), "target-2")
	appendStored(now.Add(-5*time.Second), "future")

	asOf := now.Add(-10 * time.Second)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/attention/events", nil)
	query := req.URL.Query()
	query.Set("as_of", asOf.Format(time.RFC3339))
	query.Set("limit", "2")
	req.URL.RawQuery = query.Encode()

	rec := httptest.NewRecorder()
	srv.handleAttentionEventsV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Events         []robot.AttentionEvent        `json:"events"`
		SinceCursor    int64                         `json:"since_cursor"`
		EventCount     int                           `json:"event_count"`
		Truncated      bool                          `json:"truncated"`
		ReplayTarget   *robot.HistoricalReplayTarget `json:"replay_target"`
		Reconstruction *robot.ReconstructionMeta     `json:"reconstruction"`
		Boundedness    *robot.HistoricalBoundedness  `json:"boundedness"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.SinceCursor != 0 {
		t.Fatalf("since_cursor = %d, want 0", resp.SinceCursor)
	}
	if resp.EventCount != 2 || len(resp.Events) != 2 {
		t.Fatalf("event_count/events = %d/%d, want 2/2", resp.EventCount, len(resp.Events))
	}
	if resp.ReplayTarget == nil || resp.ReplayTarget.Mode != "as_of" {
		t.Fatalf("ReplayTarget = %#v, want as_of", resp.ReplayTarget)
	}
	if resp.Reconstruction == nil || resp.Reconstruction.Method != "event_replay" {
		t.Fatalf("Reconstruction = %#v, want event_replay", resp.Reconstruction)
	}
	if resp.Boundedness == nil || !resp.Boundedness.Truncated {
		t.Fatalf("Boundedness = %#v, want truncated historical metadata", resp.Boundedness)
	}
	if resp.Truncated {
		t.Fatalf("truncated = %v, want false when pagination is complete", resp.Truncated)
	}
}

func TestAttentionEventsIncidentNotFoundIncludesRobotErrorEnvelope(t *testing.T) {
	srv, store := setupTestServer(t)

	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	}, robot.WithAttentionStore(store))
	oldFeed := robot.GetAttentionFeed()
	robot.SetAttentionFeed(feed)
	t.Cleanup(func() {
		robot.SetAttentionFeed(oldFeed)
		feed.Stop()
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/attention/events?incident_id=missing-incident", nil)
	rec := httptest.NewRecorder()

	srv.handleAttentionEventsV1(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var resp struct {
		Success     bool                   `json:"success"`
		Error       string                 `json:"error"`
		ErrorCode   string                 `json:"error_code"`
		SinceCursor int64                  `json:"since_cursor"`
		EventCount  int                    `json:"event_count"`
		Truncated   bool                   `json:"truncated"`
		Events      []robot.AttentionEvent `json:"events"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Success {
		t.Fatalf("success = %v, want false", resp.Success)
	}
	if resp.ErrorCode != robot.ErrCodeNotFound {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, robot.ErrCodeNotFound)
	}
	if resp.Error == "" {
		t.Fatal("error = empty, want incident-not-found message")
	}
	if resp.SinceCursor != 0 || resp.EventCount != 0 || resp.Truncated {
		t.Fatalf("since_cursor/event_count/truncated = %d/%d/%v, want 0/0/false", resp.SinceCursor, resp.EventCount, resp.Truncated)
	}
	if len(resp.Events) != 0 {
		t.Fatalf("len(events) = %d, want 0", len(resp.Events))
	}
}

func TestAttentionEventsCursorExpiredUsesResyncCommand(t *testing.T) {
	srv, _ := setupTestServer(t)
	feed, _ := installServeTestAttentionFeed(t)
	stats, sinceCursor := expiredServeTestAttentionCursor(t, feed)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/attention/events", nil)
	query := req.URL.Query()
	query.Set("since_cursor", strconv.FormatInt(sinceCursor, 10))
	req.URL.RawQuery = query.Encode()

	rec := httptest.NewRecorder()
	srv.handleAttentionEventsV1(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusConflict, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := resp["error_code"]; got != robot.ErrCodeCursorExpired {
		t.Fatalf("error_code = %v, want %q", got, robot.ErrCodeCursorExpired)
	}
	if got := resp["resync_command"]; got != "ntm --robot-snapshot" {
		t.Fatalf("resync_command = %v, want %q", got, "ntm --robot-snapshot")
	}
	if _, ok := resp["resync_hint"]; ok {
		t.Fatalf("unexpected legacy resync_hint field in response: %#v", resp)
	}
	if got := int64(resp["oldest_cursor"].(float64)); got != stats.OldestCursor {
		t.Fatalf("oldest_cursor = %d, want %d", got, stats.OldestCursor)
	}
}

func TestAttentionDigestCursorExpiredUsesResyncCommand(t *testing.T) {
	srv, _ := setupTestServer(t)
	feed, _ := installServeTestAttentionFeed(t)
	stats, sinceCursor := expiredServeTestAttentionCursor(t, feed)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/attention/digest", nil)
	query := req.URL.Query()
	query.Set("since_cursor", strconv.FormatInt(sinceCursor, 10))
	req.URL.RawQuery = query.Encode()

	rec := httptest.NewRecorder()
	srv.handleAttentionDigestV1(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusConflict, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := resp["resync_command"]; got != "ntm --robot-snapshot" {
		t.Fatalf("resync_command = %v, want %q", got, "ntm --robot-snapshot")
	}
	if _, ok := resp["resync_hint"]; ok {
		t.Fatalf("unexpected legacy resync_hint field in response: %#v", resp)
	}
	if got := int64(resp["oldest_cursor"].(float64)); got != stats.OldestCursor {
		t.Fatalf("oldest_cursor = %d, want %d", got, stats.OldestCursor)
	}
}

func TestAttentionStreamCursorExpiredUsesResyncCommand(t *testing.T) {
	srv, _ := setupTestServer(t)
	feed, _ := installServeTestAttentionFeed(t)
	_, sinceCursor := expiredServeTestAttentionCursor(t, feed)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/attention/stream", nil)
	query := req.URL.Query()
	query.Set("since_cursor", strconv.FormatInt(sinceCursor, 10))
	req.URL.RawQuery = query.Encode()

	rec := httptest.NewRecorder()
	srv.handleAttentionStreamV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"error_code":"CURSOR_EXPIRED"`) {
		t.Fatalf("stream body missing cursor-expired error: %s", body)
	}
	if !strings.Contains(body, `"resync_command":"ntm --robot-snapshot"`) {
		t.Fatalf("stream body missing resync_command: %s", body)
	}
	if strings.Contains(body, `"resync_hint"`) {
		t.Fatalf("stream body still exposes legacy resync_hint: %s", body)
	}
}

func appendServeTestAttentionEvent(t *testing.T, store *state.Store, summary string) int64 {
	t.Helper()

	cursor, err := store.AppendAttentionEvent(&state.StoredAttentionEvent{
		Ts:            time.Now().UTC(),
		SessionName:   "proj",
		Pane:          "2",
		Category:      "alert",
		EventType:     "alert_warning",
		Source:        "serve_test",
		Actionability: state.ActionabilityActionRequired,
		Severity:      state.SeverityWarning,
		ReasonCode:    "test_attention",
		Summary:       summary,
		Details:       `{"kind":"serve_test","count":1}`,
		DedupKey:      "serve-test:" + strings.ReplaceAll(summary, " ", "-"),
		DedupCount:    1,
	})
	if err != nil {
		t.Fatalf("AppendAttentionEvent() error: %v", err)
	}
	return cursor
}

func newAttentionStateRequest(method string, cursor int64, body string, requestID string, userID string, role Role) *http.Request {
	req := httptest.NewRequest(method, fmt.Sprintf("/api/v1/attention/items/%d/state", cursor), strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("cursor", strconv.FormatInt(cursor, 10))
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, requestIDKey, requestID)
	ctx = withRoleContext(ctx, &RoleContext{Role: role, UserID: userID})
	return req.WithContext(ctx)
}

func TestAttentionItemStateAcknowledgePersistsAudit(t *testing.T) {
	srv, store := setupTestServer(t)
	cursor := appendServeTestAttentionEvent(t, store, "operator attention ack")

	req := newAttentionStateRequest(
		http.MethodPost,
		cursor,
		`{"action":"acknowledge","reason":"handled in operator loop"}`,
		"req-att-ack",
		"operator-alice",
		RoleOperator,
	)
	rec := httptest.NewRecorder()

	srv.handleAttentionItemStateV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Success bool   `json:"success"`
		Request string `json:"request_id"`
		Result  struct {
			Action         string                   `json:"action"`
			PreviousState  string                   `json:"previous_state"`
			NewState       string                   `json:"new_state"`
			AttentionState state.AttentionItemState `json:"attention_state"`
		} `json:"result"`
		Audit struct {
			EventID    int64  `json:"event_id"`
			DecisionID int64  `json:"decision_id"`
			ActorType  string `json:"actor_type"`
			ActorID    string `json:"actor_id"`
		} `json:"audit"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if !resp.Success {
		t.Fatal("expected success=true")
	}
	if resp.Result.Action != "acknowledge" {
		t.Fatalf("action = %q, want acknowledge", resp.Result.Action)
	}
	if resp.Result.PreviousState != string(state.AttentionStateNew) {
		t.Fatalf("previous_state = %q, want %q", resp.Result.PreviousState, state.AttentionStateNew)
	}
	if resp.Result.NewState != string(state.AttentionStateAcknowledged) {
		t.Fatalf("new_state = %q, want %q", resp.Result.NewState, state.AttentionStateAcknowledged)
	}
	if resp.Audit.ActorID != "operator-alice" {
		t.Fatalf("audit.actor_id = %q, want operator-alice", resp.Audit.ActorID)
	}
	if resp.Audit.EventID == 0 || resp.Audit.DecisionID == 0 {
		t.Fatalf("expected non-zero audit ids, got event=%d decision=%d", resp.Audit.EventID, resp.Audit.DecisionID)
	}

	itemState, err := store.GetAttentionItemStateByCursor(cursor)
	if err != nil {
		t.Fatalf("GetAttentionItemStateByCursor() error: %v", err)
	}
	if itemState == nil {
		t.Fatal("expected persisted attention item state")
	}
	if itemState.State != state.AttentionStateAcknowledged {
		t.Fatalf("persisted state = %q, want %q", itemState.State, state.AttentionStateAcknowledged)
	}
	if itemState.AcknowledgedBy != "operator-alice" {
		t.Fatalf("acknowledged_by = %q, want operator-alice", itemState.AcknowledgedBy)
	}
	if itemState.Fingerprint == "" {
		t.Fatal("expected persisted fingerprint to be set")
	}

	itemKey, err := store.ResolveAttentionItemKey(cursor)
	if err != nil {
		t.Fatalf("ResolveAttentionItemKey() error: %v", err)
	}
	events, err := store.GetAuditHistory("attention_item", itemKey, 10)
	if err != nil {
		t.Fatalf("GetAuditHistory() error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(events))
	}
	if events[0].RequestID != "req-att-ack" {
		t.Fatalf("audit request_id = %q, want req-att-ack", events[0].RequestID)
	}
	if events[0].ActorID != "operator-alice" {
		t.Fatalf("audit actor_id = %q, want operator-alice", events[0].ActorID)
	}

	decisions, err := store.GetDecisionHistory("attention_item", itemKey, 10)
	if err != nil {
		t.Fatalf("GetDecisionHistory() error: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("decision history = %d, want 1", len(decisions))
	}
	if decisions[0].DecisionType != "acknowledge" {
		t.Fatalf("decision_type = %q, want acknowledge", decisions[0].DecisionType)
	}
}

func TestAttentionItemStateSnoozeRestoreClearsState(t *testing.T) {
	srv, store := setupTestServer(t)
	cursor := appendServeTestAttentionEvent(t, store, "operator attention snooze")

	snoozeUntil := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)
	req := newAttentionStateRequest(
		http.MethodPost,
		cursor,
		fmt.Sprintf(`{"action":"snooze","until":"%s","reason":"later"}`, snoozeUntil),
		"req-att-snooze",
		"operator-bob",
		RoleOperator,
	)
	rec := httptest.NewRecorder()
	srv.handleAttentionItemStateV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("snooze status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	snoozed, err := store.GetAttentionItemStateByCursor(cursor)
	if err != nil {
		t.Fatalf("GetAttentionItemStateByCursor() after snooze error: %v", err)
	}
	if snoozed == nil || snoozed.State != state.AttentionStateSnoozed || snoozed.SnoozedUntil == nil {
		t.Fatalf("expected snoozed state with until, got %#v", snoozed)
	}

	restoreReq := newAttentionStateRequest(
		http.MethodPost,
		cursor,
		`{"action":"restore","reason":"back in scope"}`,
		"req-att-restore",
		"operator-bob",
		RoleOperator,
	)
	restoreRec := httptest.NewRecorder()
	srv.handleAttentionItemStateV1(restoreRec, restoreReq)
	if restoreRec.Code != http.StatusOK {
		t.Fatalf("restore status = %d, want %d: %s", restoreRec.Code, http.StatusOK, restoreRec.Body.String())
	}

	restored, err := store.GetAttentionItemStateByCursor(cursor)
	if err != nil {
		t.Fatalf("GetAttentionItemStateByCursor() after restore error: %v", err)
	}
	if restored == nil {
		t.Fatal("expected restored state to persist")
	}
	if restored.State != state.AttentionStateNew {
		t.Fatalf("restored state = %q, want %q", restored.State, state.AttentionStateNew)
	}
	if restored.SnoozedUntil != nil {
		t.Fatalf("restore should clear snoozed_until, got %v", restored.SnoozedUntil)
	}
	if restored.DismissedAt != nil || restored.DismissedBy != "" {
		t.Fatalf("restore should clear dismissed fields, got %#v", restored)
	}
}

func TestAttentionItemStatePinAndEscalateSetFlags(t *testing.T) {
	srv, store := setupTestServer(t)
	cursor := appendServeTestAttentionEvent(t, store, "operator attention priority")

	pinReq := newAttentionStateRequest(
		http.MethodPost,
		cursor,
		`{"action":"pin","reason":"watch closely"}`,
		"req-att-pin",
		"operator-carol",
		RoleOperator,
	)
	pinRec := httptest.NewRecorder()
	srv.handleAttentionItemStateV1(pinRec, pinReq)
	if pinRec.Code != http.StatusOK {
		t.Fatalf("pin status = %d, want %d: %s", pinRec.Code, http.StatusOK, pinRec.Body.String())
	}

	escalateReq := newAttentionStateRequest(
		http.MethodPost,
		cursor,
		`{"action":"escalate","override_priority":"critical","reason":"force visibility"}`,
		"req-att-escalate",
		"operator-carol",
		RoleOperator,
	)
	escalateRec := httptest.NewRecorder()
	srv.handleAttentionItemStateV1(escalateRec, escalateReq)
	if escalateRec.Code != http.StatusOK {
		t.Fatalf("escalate status = %d, want %d: %s", escalateRec.Code, http.StatusOK, escalateRec.Body.String())
	}

	itemState, err := store.GetAttentionItemStateByCursor(cursor)
	if err != nil {
		t.Fatalf("GetAttentionItemStateByCursor() error: %v", err)
	}
	if itemState == nil {
		t.Fatal("expected item state to persist")
	}
	if !itemState.Pinned {
		t.Fatal("expected pinned=true after pin action")
	}
	if itemState.PinnedBy != "operator-carol" {
		t.Fatalf("pinned_by = %q, want operator-carol", itemState.PinnedBy)
	}
	if itemState.OverridePriority != "critical" {
		t.Fatalf("override_priority = %q, want critical", itemState.OverridePriority)
	}
	if itemState.OverrideReason != "force visibility" {
		t.Fatalf("override_reason = %q, want force visibility", itemState.OverrideReason)
	}
}

func TestCORSMiddleware(t *testing.T) {
	srv := New(Config{})
	handler := srv.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Test preflight OPTIONS request
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Error("Expected CORS allowlist header")
	}
	if methods := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(methods, http.MethodPatch) {
		t.Fatalf("expected PATCH in Access-Control-Allow-Methods, got %q", methods)
	}
	if headers := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(headers, "Idempotency-Key") {
		t.Fatalf("expected Idempotency-Key in Access-Control-Allow-Headers, got %q", headers)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("OPTIONS Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestCORSMiddlewareRejectsOrigin(t *testing.T) {
	srv := New(Config{})
	handler := srv.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestLoggingMiddleware(t *testing.T) {
	handler := loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestAuthMiddlewareAPIKey(t *testing.T) {
	srv := New(Config{
		Auth: AuthConfig{
			Mode:   AuthModeAPIKey,
			APIKey: "secret",
		},
	})
	handler := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("missing api key status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("valid api key status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestAuthMiddlewareOIDC(t *testing.T) {
	issuer := "https://issuer.example.com"
	audience := "ntm"
	key := mustGenerateKey(t)
	jwksURL := startJWKS(t, key, "kid1")
	token := signJWT(t, key, "kid1", issuer, audience, time.Now().Add(1*time.Hour))

	srv := New(Config{
		Auth: AuthConfig{
			Mode: AuthModeOIDC,
			OIDC: OIDCConfig{
				Issuer:   issuer,
				Audience: audience,
				JWKSURL:  jwksURL,
			},
		},
	})
	handler := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("missing oidc token status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("valid oidc token status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestAuthMiddlewareMTLS(t *testing.T) {
	srv := New(Config{
		Auth: AuthConfig{
			Mode: AuthModeMTLS,
		},
	})
	handler := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("missing mtls cert status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("valid mtls cert status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestWebSocketRejectedWithoutToken(t *testing.T) {
	srv := New(Config{
		Auth: AuthConfig{
			Mode:   AuthModeAPIKey,
			APIKey: "secret",
		},
	})
	handler := srv.authMiddleware(http.HandlerFunc(srv.handleWS))

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("ws unauth status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestCheckWSOriginAllowsConfiguredOrigin(t *testing.T) {
	srv := New(Config{
		Auth: AuthConfig{
			Mode:   AuthModeAPIKey,
			APIKey: "secret",
		},
		AllowedOrigins: []string{"https://ui.example.com"},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ws", nil)
	req.Header.Set("Origin", "https://ui.example.com")

	if !srv.checkWSOrigin(req) {
		t.Fatalf("expected origin to be allowed")
	}
}

func TestCheckWSOriginRejectsUnlistedOrigin(t *testing.T) {
	srv := New(Config{
		Auth: AuthConfig{
			Mode:   AuthModeAPIKey,
			APIKey: "secret",
		},
		AllowedOrigins: []string{"https://ui.example.com"},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ws", nil)
	req.Header.Set("Origin", "https://evil.example.com")

	if srv.checkWSOrigin(req) {
		t.Fatalf("expected origin to be rejected")
	}
}

func mustGenerateKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func startJWKS(t *testing.T, key *rsa.PrivateKey, kid string) string {
	t.Helper()
	n := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes())
	payload := map[string]interface{}{
		"keys": []map[string]string{
			{
				"kty": "RSA",
				"kid": kid,
				"n":   n,
				"e":   e,
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func signJWT(t *testing.T, key *rsa.PrivateKey, kid, issuer, audience string, exp time.Time) string {
	t.Helper()
	header := map[string]string{
		"alg": "RS256",
		"kid": kid,
		"typ": "JWT",
	}
	claims := map[string]interface{}{
		"iss": issuer,
		"aud": audience,
		"exp": exp.Unix(),
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	headerEnc := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsEnc := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := headerEnc + "." + claimsEnc
	hash := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	sigEnc := base64.RawURLEncoding.EncodeToString(sig)
	return signingInput + "." + sigEnc
}

// =============================================================================
// API v1 Tests
// =============================================================================

func TestHealthV1Endpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}
	if resp["status"] != "healthy" {
		t.Error("Expected status=healthy")
	}
	if _, ok := resp["timestamp"]; !ok {
		t.Error("Expected timestamp field")
	}
}

func TestPerformDoctorCheckAPI_DaemonList(t *testing.T) {
	report := performDoctorCheckAPI(context.Background())

	daemonsAny, ok := report["daemons"]
	if !ok {
		t.Fatal("expected daemons in doctor report")
	}
	daemons, ok := daemonsAny.([]map[string]interface{})
	if !ok {
		t.Fatalf("daemons type = %T, want []map[string]interface{}", daemonsAny)
	}
	if len(daemons) != 2 {
		t.Fatalf("daemon count = %d, want 2", len(daemons))
	}

	names := map[string]bool{}
	for _, daemon := range daemons {
		name, _ := daemon["name"].(string)
		names[name] = true
	}

	if !names["agent-mail"] {
		t.Fatal("expected agent-mail daemon probe")
	}
	if !names["cm-server"] {
		t.Fatal("expected cm-server daemon probe")
	}
	if names["bd-daemon"] {
		t.Fatal("did not expect bd-daemon probe")
	}
}

func TestVersionV1Endpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}
	if resp["api_version"] != "v1" {
		t.Error("Expected api_version=v1")
	}
}

func TestCapabilitiesV1Endpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}
	if _, ok := resp["version"]; !ok {
		t.Error("Expected version field")
	}
	if _, ok := resp["commands"]; !ok {
		t.Error("Expected commands field")
	}
	if _, ok := resp["categories"]; !ok {
		t.Error("Expected categories field")
	}
}

func TestSessionsV1Endpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}
	// Verify sessions is an array (never null)
	sessions, ok := resp["sessions"].([]interface{})
	if !ok {
		t.Error("Expected sessions to be an array")
	}
	if sessions == nil {
		t.Error("Expected sessions to be non-nil array")
	}
}

// =============================================================================
// Jobs API Tests
// =============================================================================

func TestListJobsEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}
	// Verify jobs is an array (never null)
	jobs, ok := resp["jobs"].([]interface{})
	if !ok {
		t.Error("Expected jobs to be an array")
	}
	if jobs == nil {
		t.Error("Expected jobs to be non-nil array")
	}
}

func TestCreateJobEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	body := `{"type": "spawn", "session": "test-session"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusAccepted)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}

	job, ok := resp["job"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected job object in response")
	}

	if job["type"] != "spawn" {
		t.Errorf("Job type = %v, want spawn", job["type"])
	}
	// Status can be "pending" or "running" due to the goroutine race
	status, _ := job["status"].(string)
	if status != "pending" && status != "running" {
		t.Errorf("Job status = %v, want pending or running", job["status"])
	}
}

func TestCreateJobInvalidType(t *testing.T) {
	srv, _ := setupTestServer(t)

	body := `{"type": "invalid"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestGetJobEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Create a job first
	job := srv.jobStore.Create("spawn")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+job.ID, nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}
}

func TestGetJobNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/nonexistent", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestCancelJobEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Create a job first
	job := srv.jobStore.Create("spawn")

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+job.ID, nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	// Verify job is cancelled
	updatedJob := srv.jobStore.Get(job.ID)
	if updatedJob.Status != JobStatusCancelled {
		t.Errorf("Job status = %v, want cancelled", updatedJob.Status)
	}
}

// =============================================================================
// Idempotency Tests
// =============================================================================

func TestIdempotencyStore(t *testing.T) {
	store := NewIdempotencyStore(time.Hour)

	// Test set and get
	store.Set("key1", []byte(`{"test": true}`), 200)

	resp, status, ok := store.Get("key1")
	if !ok {
		t.Fatal("Expected to find key1")
	}
	if status != 200 {
		t.Errorf("Status = %d, want 200", status)
	}
	if string(resp) != `{"test": true}` {
		t.Errorf("Response = %s, want {\"test\": true}", string(resp))
	}

	// Test non-existent key
	_, _, ok = store.Get("nonexistent")
	if ok {
		t.Error("Expected not to find nonexistent key")
	}
}

func TestIdempotencyMiddleware(t *testing.T) {
	srv, _ := setupTestServer(t)

	// First request
	body := `{"type": "spawn"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "test-key-123")
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Errorf("First request status = %d, want %d", rec.Code, http.StatusAccepted)
	}

	// Capture the job ID from first response
	var firstResp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&firstResp); err != nil {
		t.Fatalf("Failed to decode first response: %v", err)
	}
	firstJob := firstResp["job"].(map[string]interface{})
	firstJobID := firstJob["id"].(string)

	// Second request with same idempotency key
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Idempotency-Key", "test-key-123")
	rec2 := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec2, req2)

	// Should get same response (replay)
	if rec2.Header().Get("X-Idempotent-Replay") != "true" {
		t.Error("Expected X-Idempotent-Replay header")
	}

	var secondResp map[string]interface{}
	if err := json.NewDecoder(rec2.Body).Decode(&secondResp); err != nil {
		t.Fatalf("Failed to decode second response: %v", err)
	}
	secondJob := secondResp["job"].(map[string]interface{})
	secondJobID := secondJob["id"].(string)

	// Should return same job ID
	if firstJobID != secondJobID {
		t.Errorf("Idempotent replay returned different job ID: %s vs %s", firstJobID, secondJobID)
	}
}

// =============================================================================
// Panic Recovery Tests
// =============================================================================

func TestRecovererMiddleware(t *testing.T) {
	srv := New(Config{})

	// Create a handler that panics
	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})

	// Wrap with recoverer
	handler := srv.recovererMiddleware(panicHandler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	// Should not panic
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != false {
		t.Error("Expected success=false")
	}
	if resp["error_code"] != "INTERNAL_ERROR" {
		t.Errorf("Error code = %v, want INTERNAL_ERROR", resp["error_code"])
	}
}

// =============================================================================
// WebSocket Hub Tests
// =============================================================================

func TestWSHub(t *testing.T) {
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	// Check initial state
	if hub.ClientCount() != 0 {
		t.Errorf("ClientCount = %d, want 0", hub.ClientCount())
	}

	// Test nextSeq
	seq1 := hub.nextSeq()
	seq2 := hub.nextSeq()
	if seq2 != seq1+1 {
		t.Errorf("nextSeq not incrementing: got %d, want %d", seq2, seq1+1)
	}
}

func TestWSHubRegisterClientAfterStop(t *testing.T) {
	hub := NewWSHub()
	hub.Stop()

	client := &WSClient{
		id:     "stopped-client",
		hub:    hub,
		send:   make(chan []byte, 1),
		topics: make(map[string]struct{}),
	}

	if ok := hub.RegisterClient(client); ok {
		t.Fatal("RegisterClient() on stopped hub = true, want false")
	}
}

func TestWSHubUnregisterClientAfterStopDoesNotBlock(t *testing.T) {
	hub := NewWSHub()
	hub.Stop()

	client := &WSClient{
		id:     "stopped-client",
		hub:    hub,
		send:   make(chan []byte, 1),
		topics: make(map[string]struct{}),
	}

	done := make(chan struct{})
	go func() {
		hub.UnregisterClient(client)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("UnregisterClient() blocked after hub stop")
	}
}

func TestWSHubStopClosesClientChannelsAndClearsClients(t *testing.T) {
	hub := NewWSHub()
	client := &WSClient{
		id:     "stop-client",
		hub:    hub,
		send:   make(chan []byte, 1),
		topics: make(map[string]struct{}),
	}

	hub.clientsMu.Lock()
	hub.clients[client] = struct{}{}
	hub.clientsMu.Unlock()

	hub.Stop()

	if got := hub.ClientCount(); got != 0 {
		t.Fatalf("ClientCount() after Stop = %d, want 0", got)
	}

	select {
	case _, ok := <-client.send:
		if ok {
			t.Fatal("client send channel still open after Stop")
		}
	default:
		t.Fatal("client send channel was not closed by Stop")
	}
}

func TestTopicMatching(t *testing.T) {
	tests := []struct {
		pattern string
		topic   string
		want    bool
	}{
		{"*", "anything", true},
		{"*", "sessions:test", true},
		{"global", "global", true},
		{"global", "sessions:test", false},
		{"sessions:*", "sessions:test", true},
		{"sessions:*", "sessions:my-session", true},
		{"sessions:*", "panes:test:0", false},
		{"panes:*", "panes:test:0", true},
		{"panes:*", "panes:other:5", true},
		{"panes:test:*", "panes:test:0", true},
		{"panes:test:*", "panes:other:0", false},
		{"sessions:test", "sessions:test", true},
		{"sessions:test", "sessions:other", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.topic, func(t *testing.T) {
			got := matchTopic(tt.pattern, tt.topic)
			if got != tt.want {
				t.Errorf("matchTopic(%q, %q) = %v, want %v", tt.pattern, tt.topic, got, tt.want)
			}
		})
	}
}

func TestWSClientSubscription(t *testing.T) {
	client := &WSClient{
		id:     "test-client",
		topics: make(map[string]struct{}),
	}

	// Initially no subscriptions
	if client.isSubscribed("global") {
		t.Error("Expected not subscribed to global")
	}

	// Subscribe to global
	client.Subscribe([]string{"global", "sessions:test"})
	if !client.isSubscribed("global") {
		t.Error("Expected subscribed to global")
	}
	if !client.isSubscribed("sessions:test") {
		t.Error("Expected subscribed to sessions:test")
	}

	// Unsubscribe
	client.Unsubscribe([]string{"global"})
	if client.isSubscribed("global") {
		t.Error("Expected not subscribed to global after unsubscribe")
	}
	if !client.isSubscribed("sessions:test") {
		t.Error("Expected still subscribed to sessions:test")
	}

	// Test wildcard subscription
	client.Subscribe([]string{"panes:*"})
	if !client.isSubscribed("panes:test:0") {
		t.Error("Expected wildcard to match panes:test:0")
	}
	if !client.isSubscribed("panes:other:5") {
		t.Error("Expected wildcard to match panes:other:5")
	}
}

func TestIsValidTopic(t *testing.T) {
	valid := []string{
		"*",
		"global",
		"global:*",
		"scanner",
		"memory",
		"sessions:*",
		"sessions:my-session",
		"panes:*",
		"panes:test:0",
		"agent:claude",
		"beads:*",
		"beads:bd-123",
		"mail:*",
		"mail:CoralOtter",
		"reservations:*",
		"reservations:CoralOtter",
		"pipelines:*",
		"pipelines:my-session",
		"approvals:*",
		"approvals:my-session",
		"accounts:anthropic",
	}

	invalid := []string{
		"",
		"invalid",
		"random:topic",
		"foo:bar:baz",
	}

	for _, topic := range valid {
		if !isValidTopic(topic) {
			t.Errorf("isValidTopic(%q) = false, want true", topic)
		}
	}

	for _, topic := range invalid {
		if isValidTopic(topic) {
			t.Errorf("isValidTopic(%q) = true, want false", topic)
		}
	}
}

func TestPipelineEventTypeFromProgressType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		progressType string
		want         string
		wantOK       bool
	}{
		{progressType: "workflow_start", want: "pipeline.started", wantOK: true},
		{progressType: "step_complete", want: "pipeline.step_completed", wantOK: true},
		{progressType: "step_error", want: "pipeline.step_failed", wantOK: true},
		{progressType: "workflow_complete", want: "pipeline.complete", wantOK: true},
		{progressType: "workflow_error", want: "pipeline.complete", wantOK: true},
		{progressType: "step_start", want: "", wantOK: false},
		{progressType: "", want: "", wantOK: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.progressType, func(t *testing.T) {
			t.Parallel()
			got, ok := pipelineEventTypeFromProgressType(tc.progressType)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v, want %v", ok, tc.wantOK)
			}
			if got != tc.want {
				t.Fatalf("eventType=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestApprovals_WebSocketEvents(t *testing.T) {
	srv := New(Config{})
	srv.ensureWSHubRunning()
	defer srv.wsHub.Stop()

	testClient := &WSClient{
		id:     "test-client",
		hub:    srv.wsHub,
		send:   make(chan []byte, 256),
		topics: map[string]struct{}{"approvals:*": {}},
	}
	requireRegisterWSClient(t, srv.wsHub, testClient)

	time.Sleep(10 * time.Millisecond)

	approvalsLock.Lock()
	approvals = make(map[string]*Approval)
	approvalIDSeq = 0
	approvalsLock.Unlock()

	reqBody := strings.NewReader(`{"action":"git status","resource":"repo","reason":"test"}`)
	req := httptest.NewRequest(http.MethodPost, "/approvals/request", reqBody)
	req = req.WithContext(withRoleContext(req.Context(), &RoleContext{
		Role:   RoleOperator,
		UserID: "requestor-1",
	}))
	rec := httptest.NewRecorder()
	srv.handleApprovalRequestV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handleApprovalRequestV1 status=%d, want %d", rec.Code, http.StatusOK)
	}

	var ev1 struct {
		Topic     string                 `json:"topic"`
		EventType string                 `json:"event_type"`
		Data      map[string]interface{} `json:"data"`
	}
	select {
	case msg := <-testClient.send:
		if err := json.Unmarshal(msg, &ev1); err != nil {
			t.Fatalf("unmarshal ws event: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for approval.requested ws event")
	}
	if ev1.EventType != "approval.requested" {
		t.Fatalf("event_type=%q, want %q", ev1.EventType, "approval.requested")
	}
	if got, _ := ev1.Data["approval_id"].(string); got != "apr-1" {
		t.Fatalf("approval_id=%q, want %q", got, "apr-1")
	}

	req2 := httptest.NewRequest(http.MethodPost, "/approvals/apr-1/approve", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "apr-1")
	req2 = req2.WithContext(context.WithValue(req2.Context(), chi.RouteCtxKey, rctx))
	req2 = req2.WithContext(withRoleContext(req2.Context(), &RoleContext{
		Role:   RoleAdmin,
		UserID: "approver-1",
	}))
	rec2 := httptest.NewRecorder()
	srv.handleApprovalApproveV1(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("handleApprovalApproveV1 status=%d, want %d", rec2.Code, http.StatusOK)
	}

	var ev2 struct {
		Topic     string                 `json:"topic"`
		EventType string                 `json:"event_type"`
		Data      map[string]interface{} `json:"data"`
	}
	select {
	case msg := <-testClient.send:
		if err := json.Unmarshal(msg, &ev2); err != nil {
			t.Fatalf("unmarshal ws event: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for approval.resolved ws event")
	}
	if ev2.EventType != "approval.resolved" {
		t.Fatalf("event_type=%q, want %q", ev2.EventType, "approval.resolved")
	}
	if got, _ := ev2.Data["approval_id"].(string); got != "apr-1" {
		t.Fatalf("approval_id=%q, want %q", got, "apr-1")
	}
	if got, _ := ev2.Data["decision"].(string); got != "approved" {
		t.Fatalf("decision=%q, want %q", got, "approved")
	}

	srv.wsHub.UnregisterClient(testClient)
}

func TestHandleWebSocket_StartsHubWithoutStart(t *testing.T) {
	t.Parallel()

	srv := New(Config{})
	defer srv.Stop()

	httpSrv := httptest.NewServer(srv.Router())
	defer httpSrv.Close()

	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/api/v1/ws"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		if srv.WSHub().ClientCount() == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("expected hub client count 1, got %d", srv.WSHub().ClientCount())
}

func TestWSHubPublish(t *testing.T) {
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	// Give hub time to start
	time.Sleep(10 * time.Millisecond)

	// Publish should not block even with no clients
	hub.Publish("global:events", "test_event", map[string]interface{}{
		"message": "hello",
	})

	// Give time for event to be processed
	time.Sleep(20 * time.Millisecond)

	// Verify sequence incremented (happens in broadcastEvent)
	hub.seqMu.Lock()
	seq := hub.seq
	hub.seqMu.Unlock()
	if seq != 1 {
		t.Errorf("seq = %d, want 1", seq)
	}
}

// =============================================================================
// WebSocket Streaming Tests (bd-3ef1l)
// Tests for streaming event delivery and client subscription behavior
// =============================================================================

func TestWSHub_PaneOutputSequence(t *testing.T) {
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	time.Sleep(10 * time.Millisecond)

	// Publish multiple pane output events
	for i := 0; i < 10; i++ {
		hub.Publish("panes:test:0", "pane.output", map[string]interface{}{
			"lines": []string{"output line " + string(rune('A'+i))},
			"seq":   i,
		})
	}

	time.Sleep(50 * time.Millisecond)

	// Verify sequence numbers
	hub.seqMu.Lock()
	finalSeq := hub.seq
	hub.seqMu.Unlock()

	if finalSeq != 10 {
		t.Errorf("expected final seq 10, got %d", finalSeq)
	}

	t.Logf("WS_STREAMING_TEST: hub assigned sequences 1-%d for 10 pane output events", finalSeq)
}

func TestWSHub_MultiClientFanOut(t *testing.T) {
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	time.Sleep(10 * time.Millisecond)

	// Create simulated clients
	clients := make([]*WSClient, 3)
	for i := 0; i < 3; i++ {
		clients[i] = &WSClient{
			id:     "client-" + string(rune('A'+i)),
			hub:    hub,
			send:   make(chan []byte, 100),
			topics: make(map[string]struct{}),
		}
		// Subscribe to pane output
		clients[i].Subscribe([]string{"panes:*"})

		// Register with hub
		requireRegisterWSClient(t, hub, clients[i])
	}

	time.Sleep(20 * time.Millisecond)

	// Verify client count
	if hub.ClientCount() != 3 {
		t.Errorf("expected 3 clients, got %d", hub.ClientCount())
	}

	// Publish an event
	hub.Publish("panes:test:0", "pane.output", map[string]interface{}{
		"lines": []string{"test output"},
	})

	time.Sleep(50 * time.Millisecond)

	// Each client should receive the message
	for i, client := range clients {
		select {
		case msg := <-client.send:
			if len(msg) == 0 {
				t.Errorf("client %d received empty message", i)
			}
			t.Logf("WS_STREAMING_TEST: client %s received %d bytes", client.id, len(msg))
		default:
			t.Errorf("client %d did not receive message", i)
		}
	}

	// Cleanup
	for _, client := range clients {
		hub.UnregisterClient(client)
	}
}

func TestWSHub_TopicFiltering(t *testing.T) {
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	time.Sleep(10 * time.Millisecond)

	// Create two clients with different subscriptions
	paneClient := &WSClient{
		id:     "pane-watcher",
		hub:    hub,
		send:   make(chan []byte, 100),
		topics: make(map[string]struct{}),
	}
	paneClient.Subscribe([]string{"panes:*"})
	requireRegisterWSClient(t, hub, paneClient)

	sessionClient := &WSClient{
		id:     "session-watcher",
		hub:    hub,
		send:   make(chan []byte, 100),
		topics: make(map[string]struct{}),
	}
	sessionClient.Subscribe([]string{"sessions:*"})
	requireRegisterWSClient(t, hub, sessionClient)

	time.Sleep(20 * time.Millisecond)

	// Publish a pane event
	hub.Publish("panes:test:0", "pane.output", map[string]interface{}{"data": "pane"})

	// Publish a session event
	hub.Publish("sessions:test", "session.started", map[string]interface{}{"data": "session"})

	time.Sleep(50 * time.Millisecond)

	// Pane client should only receive pane event
	paneCount := 0
	for {
		select {
		case <-paneClient.send:
			paneCount++
		default:
			goto donePane
		}
	}
donePane:

	// Session client should only receive session event
	sessionCount := 0
	for {
		select {
		case <-sessionClient.send:
			sessionCount++
		default:
			goto doneSession
		}
	}
doneSession:

	if paneCount != 1 {
		t.Errorf("pane client expected 1 message, got %d", paneCount)
	}
	if sessionCount != 1 {
		t.Errorf("session client expected 1 message, got %d", sessionCount)
	}

	t.Logf("WS_STREAMING_TEST: topic filtering verified, pane_client=%d session_client=%d", paneCount, sessionCount)

	// Cleanup
	hub.UnregisterClient(paneClient)
	hub.UnregisterClient(sessionClient)
}

func TestWSHub_ClientBufferFull(t *testing.T) {
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	time.Sleep(10 * time.Millisecond)

	// Create client with tiny buffer (1)
	slowClient := &WSClient{
		id:     "slow-client",
		hub:    hub,
		send:   make(chan []byte, 1), // Very small buffer
		topics: make(map[string]struct{}),
	}
	slowClient.Subscribe([]string{"panes:*"})
	requireRegisterWSClient(t, hub, slowClient)

	time.Sleep(20 * time.Millisecond)

	// Publish many events quickly - some should be dropped due to full buffer
	for i := 0; i < 50; i++ {
		hub.Publish("panes:test:0", "pane.output", map[string]interface{}{"idx": i})
	}

	time.Sleep(100 * time.Millisecond)

	// Count received messages
	received := 0
	for {
		select {
		case <-slowClient.send:
			received++
		default:
			goto done
		}
	}
done:

	// Should receive some but not all (buffer was full)
	if received >= 50 {
		t.Errorf("expected some messages to be dropped, but received all %d", received)
	}
	if received == 0 {
		t.Error("expected at least some messages to be received")
	}

	t.Logf("WS_STREAMING_TEST: backpressure test - sent 50, received %d (dropped %d)", received, 50-received)

	hub.UnregisterClient(slowClient)
}

func TestWSHub_GlobalWildcard(t *testing.T) {
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	time.Sleep(10 * time.Millisecond)

	// Client subscribed to everything
	globalClient := &WSClient{
		id:     "global-watcher",
		hub:    hub,
		send:   make(chan []byte, 100),
		topics: make(map[string]struct{}),
	}
	globalClient.Subscribe([]string{"*"})
	requireRegisterWSClient(t, hub, globalClient)

	time.Sleep(20 * time.Millisecond)

	// Publish to different topics
	hub.Publish("panes:test:0", "pane.output", map[string]interface{}{})
	hub.Publish("sessions:test", "session.started", map[string]interface{}{})
	hub.Publish("global:events", "system.event", map[string]interface{}{})

	time.Sleep(50 * time.Millisecond)

	// Global client should receive all 3
	received := 0
	for {
		select {
		case <-globalClient.send:
			received++
		default:
			goto done
		}
	}
done:

	if received != 3 {
		t.Errorf("global wildcard client expected 3 messages, got %d", received)
	}

	t.Logf("WS_STREAMING_TEST: global wildcard subscription received all %d events", received)

	hub.UnregisterClient(globalClient)
}

func TestMatchTopic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		topic   string
		want    bool
	}{
		{"wildcard matches anything", "*", "sessions:foo", true},
		{"wildcard matches empty-like", "*", "x", true},
		{"exact match", "global", "global", true},
		{"exact mismatch", "global", "sessions:foo", false},
		{"prefix wildcard match", "sessions:*", "sessions:my-session", true},
		{"prefix wildcard mismatch", "sessions:*", "panes:foo", false},
		{"prefix wildcard exact prefix", "panes:*", "panes:test:0", true},
		{"empty pattern no match", "", "global", false},
		{"empty topic no match", "global", "", false},
		{"both empty", "", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := matchTopic(tc.pattern, tc.topic)
			if got != tc.want {
				t.Errorf("matchTopic(%q, %q) = %v, want %v", tc.pattern, tc.topic, got, tc.want)
			}
		})
	}
}

func TestSanitizeRequestID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"alphanumeric", "abc123", "abc123"},
		{"with dashes", "req-123-abc", "req-123-abc"},
		{"with underscores", "req_123_abc", "req_123_abc"},
		{"with dots", "req.123.abc", "req.123.abc"},
		{"with colons", "req:123", "req.123"},
		{"with slashes", "req/path", "req/path"},
		{"strips special chars", "req<script>alert", "reqscriptalert"},
		{"strips spaces", "req 123", "req123"},
		{"truncates long input", strings.Repeat("a", 100), strings.Repeat("a", 64)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := sanitizeRequestID(tc.input)
			if got != tc.want {
				t.Errorf("sanitizeRequestID(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestIsLoopbackHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		host string
		want bool
	}{
		{"empty", "", true},
		{"localhost", "localhost", true},
		{"LOCALHOST", "LOCALHOST", true},
		{"127.0.0.1", "127.0.0.1", true},
		{"::1", "::1", true},
		{"bracketed ::1", "[::1]", true},
		{"external IP", "192.168.1.1", false},
		{"domain", "example.com", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isLoopbackHost(tc.host)
			if got != tc.want {
				t.Errorf("isLoopbackHost(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

func TestOriginAllowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		origin    string
		allowlist []string
		want      bool
	}{
		{"empty origin always allowed", "", []string{"example.com"}, true},
		{"empty allowlist rejects", "http://evil.com", []string{}, false},
		{"wildcard allows all", "http://evil.com", []string{"*"}, true},
		{"hostname match", "http://example.com", []string{"example.com"}, true},
		{"hostname mismatch", "http://evil.com", []string{"example.com"}, false},
		{"full URL match", "http://localhost:3000", []string{"http://localhost:3000"}, true},
		{"port mismatch in full URL", "http://localhost:3001", []string{"http://localhost:3000"}, false},
		{"host:port match", "http://localhost:8080", []string{"localhost:8080"}, true},
		{"case insensitive", "http://Example.Com", []string{"example.com"}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := originAllowed(tc.origin, tc.allowlist)
			if got != tc.want {
				t.Errorf("originAllowed(%q, %v) = %v, want %v", tc.origin, tc.allowlist, got, tc.want)
			}
		})
	}
}

func TestFormatAge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"seconds", 30 * time.Second, "just now"},
		{"minutes", 5 * time.Minute, "5m ago"},
		{"hours", 3 * time.Hour, "3h ago"},
		{"days", 48 * time.Hour, "2d ago"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := formatAge(tc.d)
			if got != tc.want {
				t.Errorf("formatAge(%v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}

func TestAttentionHeartbeatInterval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		delivered int
		recovery  bool
		degraded  int
		base      time.Duration
		override  bool
		want      time.Duration
		streamAge time.Duration
	}{
		{
			name:     "override keeps explicit interval",
			base:     12 * time.Second,
			override: true,
			want:     12 * time.Second,
		},
		{
			name:      "recovery mode uses fast heartbeat",
			recovery:  true,
			base:      attentionHeartbeatIdleInterval,
			want:      attentionHeartbeatRecoveryInterval,
			streamAge: 500 * time.Millisecond,
		},
		{
			name:     "degraded sources stay chatty",
			degraded: 1,
			base:     attentionHeartbeatIdleInterval,
			want:     attentionHeartbeatIdleInterval,
		},
		{
			name:      "recent delivered activity stretches heartbeat",
			delivered: 2,
			base:      attentionHeartbeatIdleInterval,
			want:      attentionHeartbeatHighActivityInterval,
		},
		{
			name: "idle falls back to idle interval",
			base: attentionHeartbeatIdleInterval,
			want: attentionHeartbeatIdleInterval,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := attentionHeartbeatInterval(
				tc.streamAge,
				tc.delivered,
				tc.recovery,
				attentionHeartbeatSourceSummary{degraded: tc.degraded},
				tc.base,
				tc.override,
			)
			if got != tc.want {
				t.Fatalf("attentionHeartbeatInterval() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestClaimString(t *testing.T) {
	t.Parallel()

	claims := map[string]interface{}{
		"iss":    "https://auth.example.com",
		"number": float64(42),
		"empty":  "",
	}

	tests := []struct {
		name   string
		key    string
		wantV  string
		wantOK bool
	}{
		{"present string", "iss", "https://auth.example.com", true},
		{"missing key", "sub", "", false},
		{"non-string value", "number", "", false},
		{"empty string", "empty", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v, ok := claimString(claims, tc.key)
			if ok != tc.wantOK || v != tc.wantV {
				t.Errorf("claimString(claims, %q) = (%q, %v), want (%q, %v)", tc.key, v, ok, tc.wantV, tc.wantOK)
			}
		})
	}
}

func TestClaimInt64(t *testing.T) {
	t.Parallel()

	claims := map[string]interface{}{
		"exp":    float64(1700000000),
		"string": "not a number",
		"number": json.Number("42"),
	}

	tests := []struct {
		name   string
		key    string
		wantV  int64
		wantOK bool
	}{
		{"float64 value", "exp", 1700000000, true},
		{"json.Number value", "number", 42, true},
		{"string value", "string", 0, false},
		{"missing key", "nbf", 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v, ok := claimInt64(claims, tc.key)
			if ok != tc.wantOK || v != tc.wantV {
				t.Errorf("claimInt64(claims, %q) = (%d, %v), want (%d, %v)", tc.key, v, ok, tc.wantV, tc.wantOK)
			}
		})
	}
}

func TestClaimAudienceContains(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		claims   map[string]interface{}
		expected string
		want     bool
	}{
		{"string aud match", map[string]interface{}{"aud": "my-app"}, "my-app", true},
		{"string aud mismatch", map[string]interface{}{"aud": "other-app"}, "my-app", false},
		{"array aud match", map[string]interface{}{"aud": []interface{}{"app1", "my-app"}}, "my-app", true},
		{"array aud mismatch", map[string]interface{}{"aud": []interface{}{"app1", "app2"}}, "my-app", false},
		{"missing aud", map[string]interface{}{}, "my-app", false},
		{"wrong type", map[string]interface{}{"aud": 42}, "my-app", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := claimAudienceContains(tc.claims, tc.expected)
			if got != tc.want {
				t.Errorf("claimAudienceContains(%v, %q) = %v, want %v", tc.claims, tc.expected, got, tc.want)
			}
		})
	}
}

func TestExtractBearerToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		auth string
		want string
	}{
		{"valid bearer", "Bearer my-token-123", "my-token-123"},
		{"bearer lowercase", "bearer my-token", "my-token"},
		{"BEARER uppercase", "BEARER my-token", "my-token"},
		{"empty header", "", ""},
		{"no bearer prefix", "Basic dXNlcjpwYXNz", ""},
		{"just bearer", "Bearer", ""},
		{"extra parts", "Bearer token extra", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, _ := http.NewRequest("GET", "/", nil)
			if tc.auth != "" {
				r.Header.Set("Authorization", tc.auth)
			}
			got := extractBearerToken(r)
			if got != tc.want {
				t.Errorf("extractBearerToken(auth=%q) = %q, want %q", tc.auth, got, tc.want)
			}
		})
	}
}

func TestExtractAPIKey(t *testing.T) {
	t.Parallel()

	t.Run("from X-API-Key header", func(t *testing.T) {
		t.Parallel()
		r, _ := http.NewRequest("GET", "/", nil)
		r.Header.Set("X-API-Key", "api-key-123")
		got := extractAPIKey(r)
		if got != "api-key-123" {
			t.Errorf("extractAPIKey() = %q, want %q", got, "api-key-123")
		}
	})

	t.Run("falls back to bearer token", func(t *testing.T) {
		t.Parallel()
		r, _ := http.NewRequest("GET", "/", nil)
		r.Header.Set("Authorization", "Bearer fallback-token")
		got := extractAPIKey(r)
		if got != "fallback-token" {
			t.Errorf("extractAPIKey() = %q, want %q", got, "fallback-token")
		}
	})

	t.Run("X-API-Key takes priority", func(t *testing.T) {
		t.Parallel()
		r, _ := http.NewRequest("GET", "/", nil)
		r.Header.Set("X-API-Key", "api-key")
		r.Header.Set("Authorization", "Bearer bearer-token")
		got := extractAPIKey(r)
		if got != "api-key" {
			t.Errorf("extractAPIKey() = %q, want %q (X-API-Key should take priority)", got, "api-key")
		}
	})

	t.Run("no key", func(t *testing.T) {
		t.Parallel()
		r, _ := http.NewRequest("GET", "/", nil)
		got := extractAPIKey(r)
		if got != "" {
			t.Errorf("extractAPIKey() = %q, want empty", got)
		}
	})
}

func TestIsWebSocketUpgrade(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		upgrade    string
		connection string
		want       bool
	}{
		{"valid websocket", "websocket", "Upgrade", true},
		{"case insensitive upgrade", "WebSocket", "upgrade", true},
		{"missing upgrade header", "", "Upgrade", false},
		{"wrong upgrade", "http2", "Upgrade", false},
		{"missing connection", "websocket", "", false},
		{"connection without upgrade", "websocket", "keep-alive", false},
		{"connection with multiple values", "websocket", "keep-alive, Upgrade", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, _ := http.NewRequest("GET", "/ws", nil)
			if tc.upgrade != "" {
				r.Header.Set("Upgrade", tc.upgrade)
			}
			if tc.connection != "" {
				r.Header.Set("Connection", tc.connection)
			}
			got := isWebSocketUpgrade(r)
			if got != tc.want {
				t.Errorf("isWebSocketUpgrade(upgrade=%q, connection=%q) = %v, want %v", tc.upgrade, tc.connection, got, tc.want)
			}
		})
	}
}

func TestParseJWT(t *testing.T) {
	t.Parallel()

	// Build a valid JWT: header.payload.signature (base64url encoded)
	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"key1"}`))
	payloadB64 := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://auth.example.com","sub":"user-1"}`))
	sigB64 := base64.RawURLEncoding.EncodeToString([]byte("fakesig"))
	validToken := headerB64 + "." + payloadB64 + "." + sigB64

	t.Run("valid JWT", func(t *testing.T) {
		t.Parallel()
		header, claims, sigInput, sig, err := parseJWT(validToken)
		if err != nil {
			t.Fatalf("parseJWT() error: %v", err)
		}
		if header.Alg != "RS256" {
			t.Errorf("header.Alg = %q, want RS256", header.Alg)
		}
		if header.Kid != "key1" {
			t.Errorf("header.Kid = %q, want key1", header.Kid)
		}
		if claims["iss"] != "https://auth.example.com" {
			t.Errorf("claims[iss] = %v, want https://auth.example.com", claims["iss"])
		}
		if sigInput != headerB64+"."+payloadB64 {
			t.Error("signing input mismatch")
		}
		if len(sig) == 0 {
			t.Error("signature should not be empty")
		}
	})

	t.Run("invalid format", func(t *testing.T) {
		t.Parallel()
		_, _, _, _, err := parseJWT("not.a.jwt.token")
		if err == nil {
			t.Error("expected error for invalid JWT format")
		}
	})

	t.Run("only two parts", func(t *testing.T) {
		t.Parallel()
		_, _, _, _, err := parseJWT("two.parts")
		if err == nil {
			t.Error("expected error for two-part token")
		}
	})

	t.Run("invalid base64 header", func(t *testing.T) {
		t.Parallel()
		_, _, _, _, err := parseJWT("!!!invalid." + payloadB64 + "." + sigB64)
		if err == nil {
			t.Error("expected error for invalid base64 header")
		}
	})
}

// TestToJSONMap tests the toJSONMap function for converting values to maps via JSON round-trip.
func TestToJSONMap(t *testing.T) {
	t.Parallel()

	t.Run("struct to map", func(t *testing.T) {
		t.Parallel()
		type sample struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		}
		m, err := toJSONMap(sample{Name: "test", Count: 42})
		if err != nil {
			t.Fatalf("toJSONMap() error: %v", err)
		}
		if m["name"] != "test" {
			t.Errorf("m[name] = %v, want test", m["name"])
		}
		if m["count"] != float64(42) { // JSON numbers become float64
			t.Errorf("m[count] = %v, want 42", m["count"])
		}
	})

	t.Run("map passthrough", func(t *testing.T) {
		t.Parallel()
		input := map[string]interface{}{"key": "value", "nested": map[string]interface{}{"inner": 123}}
		m, err := toJSONMap(input)
		if err != nil {
			t.Fatalf("toJSONMap() error: %v", err)
		}
		if m["key"] != "value" {
			t.Errorf("m[key] = %v, want value", m["key"])
		}
	})

	t.Run("nil input", func(t *testing.T) {
		t.Parallel()
		m, err := toJSONMap(nil)
		if err != nil {
			t.Fatalf("toJSONMap(nil) error: %v", err)
		}
		if m != nil {
			t.Errorf("toJSONMap(nil) = %v, want nil", m)
		}
	})

	t.Run("empty struct", func(t *testing.T) {
		t.Parallel()
		type empty struct{}
		m, err := toJSONMap(empty{})
		if err != nil {
			t.Fatalf("toJSONMap(empty{}) error: %v", err)
		}
		if len(m) != 0 {
			t.Errorf("len(m) = %d, want 0", len(m))
		}
	})

	t.Run("unmarshalable type returns error", func(t *testing.T) {
		t.Parallel()
		// Channels cannot be marshaled to JSON
		ch := make(chan int)
		_, err := toJSONMap(ch)
		if err == nil {
			t.Error("expected error for unmarshalable type")
		}
	})

	t.Run("non-object JSON returns error", func(t *testing.T) {
		t.Parallel()
		// A slice marshals to JSON array, which cannot unmarshal to map
		slice := []string{"a", "b", "c"}
		_, err := toJSONMap(slice)
		if err == nil {
			t.Error("expected error when JSON result is not an object")
		}
	})
}

// TestIsLoopbackHostExtended adds edge cases for isLoopbackHost.
func TestIsLoopbackHostExtended(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		host string
		want bool
	}{
		// Note: localhost:port returns false because after SplitHostPort extracts "localhost",
		// ParseIP("localhost") returns nil since localhost is not a valid IP address.
		// Only literal IPs are recognized after port stripping.
		{"localhost with port returns false", "localhost:8080", false},
		{"127.0.0.1 with port", "127.0.0.1:3000", true},
		{"[::1] with port", "[::1]:8080", true},
		{"whitespace padded", "  localhost  ", true},
		{"whitespace padded IP with port", "  127.0.0.1:8080  ", true},
		{"external IP with port", "192.168.1.1:8080", false},
		{"invalid host:port:extra", "host:port:extra", false},
		{"only port number", ":8080", false},
		{"IPv6 no brackets with port (invalid)", "::1:8080", false}, // ambiguous, treated as IPv6
		{"loopback IPv6 long form", "0:0:0:0:0:0:0:1", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isLoopbackHost(tc.host)
			if got != tc.want {
				t.Errorf("isLoopbackHost(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

// TestOriginAllowedExtended adds edge cases for originAllowed.
func TestOriginAllowedExtended(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		origin    string
		allowlist []string
		want      bool
	}{
		{"invalid origin URL", "not-a-valid-url", []string{"example.com"}, false},
		{"origin with path ignored", "http://example.com/path", []string{"example.com"}, true},
		{"allowlist with empty strings", "http://example.com", []string{"", "  ", "example.com"}, true},
		{"scheme mismatch in full URL", "https://localhost:3000", []string{"http://localhost:3000"}, false},
		{"port in allowlist but not origin", "http://localhost", []string{"localhost:3000"}, false},
		{"multiple allowlist entries", "http://api.example.com", []string{"web.example.com", "api.example.com"}, true},
		{"allowlist with invalid URL", "http://example.com", []string{"://invalid", "example.com"}, true},
		// "://" parses with error (missing protocol scheme), so originAllowed returns false
		{"malformed origin with empty host", "://", []string{"*"}, false},
		{"URL with userinfo", "http://user:pass@example.com", []string{"example.com"}, true},
		{"allowlist full URL without port matches any port", "http://localhost:9999", []string{"http://localhost"}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := originAllowed(tc.origin, tc.allowlist)
			if got != tc.want {
				t.Errorf("originAllowed(%q, %v) = %v, want %v", tc.origin, tc.allowlist, got, tc.want)
			}
		})
	}
}

// TestGenerateRequestID tests the generateRequestID function.
func TestGenerateRequestID(t *testing.T) {
	t.Parallel()

	t.Run("returns non-empty string", func(t *testing.T) {
		t.Parallel()
		id := generateRequestID()
		if id == "" {
			t.Error("generateRequestID() returned empty string")
		}
	})

	t.Run("returns 24 character hex string", func(t *testing.T) {
		t.Parallel()
		id := generateRequestID()
		if len(id) != 24 { // 12 bytes = 24 hex chars
			t.Errorf("len(generateRequestID()) = %d, want 24", len(id))
		}
		// Verify it's valid hex
		for _, c := range id {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				t.Errorf("generateRequestID() contains non-hex char: %c", c)
			}
		}
	})

	t.Run("generates unique IDs", func(t *testing.T) {
		t.Parallel()
		seen := make(map[string]bool)
		for i := 0; i < 100; i++ {
			id := generateRequestID()
			if seen[id] {
				t.Errorf("duplicate ID generated: %s", id)
			}
			seen[id] = true
		}
	})
}

// TestRequestIDFromContext tests the requestIDFromContext function.
func TestRequestIDFromContext(t *testing.T) {
	t.Parallel()

	t.Run("nil context returns empty", func(t *testing.T) {
		t.Parallel()
		id := requestIDFromContext(nil)
		if id != "" {
			t.Errorf("requestIDFromContext(nil) = %q, want empty", id)
		}
	})

	t.Run("context without request ID returns empty", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		id := requestIDFromContext(ctx)
		if id != "" {
			t.Errorf("requestIDFromContext(background) = %q, want empty", id)
		}
	})

	t.Run("context with request ID returns value", func(t *testing.T) {
		t.Parallel()
		ctx := context.WithValue(context.Background(), requestIDKey, "test-request-123")
		id := requestIDFromContext(ctx)
		if id != "test-request-123" {
			t.Errorf("requestIDFromContext() = %q, want test-request-123", id)
		}
	})

	t.Run("context with wrong type returns empty", func(t *testing.T) {
		t.Parallel()
		ctx := context.WithValue(context.Background(), requestIDKey, 12345) // wrong type
		id := requestIDFromContext(ctx)
		if id != "" {
			t.Errorf("requestIDFromContext() = %q, want empty for wrong type", id)
		}
	})
}

// =============================================================================
// checkWSOrigin tests
// =============================================================================

func TestCheckWSOrigin_LocalMode(t *testing.T) {
	t.Parallel()
	srv := &Server{auth: AuthConfig{Mode: AuthModeLocal}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.com")
	if !srv.checkWSOrigin(req) {
		t.Error("local mode should accept any origin")
	}
}

func TestCheckWSOrigin_EmptyMode(t *testing.T) {
	t.Parallel()
	srv := &Server{auth: AuthConfig{Mode: ""}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.com")
	if !srv.checkWSOrigin(req) {
		t.Error("empty auth mode should accept any origin")
	}
}

func TestCheckWSOrigin_NoOriginHeader(t *testing.T) {
	t.Parallel()
	srv := &Server{
		auth:               AuthConfig{Mode: AuthModeAPIKey, APIKey: "key"},
		corsAllowedOrigins: []string{"https://example.com"},
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// No Origin header
	if !srv.checkWSOrigin(req) {
		t.Error("no origin header should be accepted for non-browser clients")
	}
}

func TestCheckWSOrigin_AllowedOrigin(t *testing.T) {
	t.Parallel()
	srv := &Server{
		auth:               AuthConfig{Mode: AuthModeAPIKey, APIKey: "key"},
		corsAllowedOrigins: []string{"https://example.com", "https://app.example.com:8080"},
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://example.com")
	if !srv.checkWSOrigin(req) {
		t.Error("allowed origin should be accepted")
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("Origin", "https://app.example.com:8080")
	if !srv.checkWSOrigin(req2) {
		t.Error("allowed origin with port should be accepted")
	}
}

func TestCheckWSOrigin_RejectedOrigin(t *testing.T) {
	t.Parallel()
	srv := &Server{
		auth:               AuthConfig{Mode: AuthModeAPIKey, APIKey: "key"},
		corsAllowedOrigins: []string{"https://example.com"},
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.com")
	if srv.checkWSOrigin(req) {
		t.Error("rejected origin should return false")
	}
}

func TestCheckWSOrigin_MalformedOrigin(t *testing.T) {
	t.Parallel()
	srv := &Server{
		auth:               AuthConfig{Mode: AuthModeAPIKey, APIKey: "key"},
		corsAllowedOrigins: []string{"https://example.com"},
	}

	// Missing scheme
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "//example.com")
	if srv.checkWSOrigin(req) {
		t.Error("origin with missing scheme should be rejected")
	}
}

func TestCheckWSOrigin_MalformedAllowedOrigin(t *testing.T) {
	t.Parallel()
	srv := &Server{
		auth:               AuthConfig{Mode: AuthModeAPIKey, APIKey: "key"},
		corsAllowedOrigins: []string{"not-a-url", "https://good.com"},
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://good.com")
	if !srv.checkWSOrigin(req) {
		t.Error("should skip malformed allowed origins and still match valid ones")
	}
}

// =============================================================================
// extractAuthClaims tests
// =============================================================================

func TestExtractAuthClaims_NoClaims(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	claims := extractAuthClaims(req)
	if len(claims) != 0 {
		t.Errorf("expected empty claims, got %v", claims)
	}
}

func TestExtractAuthClaims_WithClaims(t *testing.T) {
	t.Parallel()
	authData := map[string]interface{}{
		"sub":   "user-123",
		"email": "user@example.com",
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := context.WithValue(req.Context(), authContextKey, authData)
	req = req.WithContext(ctx)

	claims := extractAuthClaims(req)
	if claims["sub"] != "user-123" {
		t.Errorf("expected sub=user-123, got %v", claims["sub"])
	}
	if claims["email"] != "user@example.com" {
		t.Errorf("expected email, got %v", claims["email"])
	}
}

func TestExtractAuthClaims_WrongType(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := context.WithValue(req.Context(), authContextKey, "not-a-map")
	req = req.WithContext(ctx)

	claims := extractAuthClaims(req)
	if len(claims) != 0 {
		t.Errorf("expected empty claims for wrong type, got %v", claims)
	}
}

// =============================================================================
// ValidateConfig additional branch tests
// =============================================================================

func TestValidateConfig_APIKeyNoKey(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Auth: AuthConfig{Mode: AuthModeAPIKey}, // No APIKey
	}
	err := ValidateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "requires --api-key") {
		t.Errorf("expected api-key error, got %v", err)
	}
}

func TestValidateConfig_OIDCMissingIssuer(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Host: "0.0.0.0",
		Auth: AuthConfig{
			Mode: AuthModeOIDC,
			OIDC: OIDCConfig{JWKSURL: "https://example.com/.well-known/jwks.json"},
		},
	}
	err := ValidateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "requires --oidc-issuer") {
		t.Errorf("expected oidc-issuer error, got %v", err)
	}
}

func TestValidateConfig_OIDCMissingJWKS(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Host: "0.0.0.0",
		Auth: AuthConfig{
			Mode: AuthModeOIDC,
			OIDC: OIDCConfig{Issuer: "https://example.com"},
		},
	}
	err := ValidateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "requires --oidc-jwks-url") {
		t.Errorf("expected oidc-jwks error, got %v", err)
	}
}

func TestValidateConfig_MTLSMissing(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Host: "0.0.0.0",
		Auth: AuthConfig{Mode: AuthModeMTLS},
	}
	err := ValidateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "requires --mtls") {
		t.Errorf("expected mtls error, got %v", err)
	}
}

func TestValidateConfig_InvalidAuthMode(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Auth: AuthConfig{Mode: "bogus_mode"},
	}
	err := ValidateConfig(cfg)
	if err == nil {
		t.Error("expected error for invalid auth mode")
	}
}

// ---------------------------------------------------------------------------
// corsMiddlewareFunc (33.3% → target higher)
// ---------------------------------------------------------------------------

func TestCorsMiddlewareFunc_ForbiddenOrigin(t *testing.T) {
	t.Parallel()
	srv := &Server{corsAllowedOrigins: []string{"https://good.com"}}
	handler := srv.corsMiddlewareFunc(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called for forbidden origin")
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestCorsMiddlewareFunc_AllowedOriginSetsHeaders(t *testing.T) {
	t.Parallel()
	srv := &Server{corsAllowedOrigins: []string{"https://good.com"}}
	called := false
	handler := srv.corsMiddlewareFunc(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("Origin", "https://good.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if !called {
		t.Error("next handler should be called for allowed origin")
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://good.com" {
		t.Errorf("expected ACAO=https://good.com, got %q", got)
	}
	if got := w.Header().Get("Vary"); got != "Origin" {
		t.Errorf("expected Vary=Origin, got %q", got)
	}
}

func TestCorsMiddlewareFunc_OptionsRequest(t *testing.T) {
	t.Parallel()
	srv := &Server{corsAllowedOrigins: []string{"https://good.com"}}
	handler := srv.corsMiddlewareFunc(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called on options")
	}))
	req := httptest.NewRequest(http.MethodOptions, "/api", nil)
	req.Header.Set("Origin", "https://good.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if methods := w.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(methods, http.MethodPatch) {
		t.Fatalf("expected PATCH in Access-Control-Allow-Methods, got %q", methods)
	}
	if headers := w.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(headers, "Idempotency-Key") {
		t.Fatalf("expected Idempotency-Key in Access-Control-Allow-Headers, got %q", headers)
	}
}

func TestCorsMiddlewareFunc_NoOriginPassesThrough(t *testing.T) {
	t.Parallel()
	srv := &Server{corsAllowedOrigins: []string{"https://good.com"}}
	called := false
	handler := srv.corsMiddlewareFunc(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	// No Origin header
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if !called {
		t.Error("next handler should be called when no Origin header")
	}
}

// ---------------------------------------------------------------------------
// authMiddlewareFunc (40.0% → target higher)
// ---------------------------------------------------------------------------

func TestAuthMiddlewareFunc_LocalMode(t *testing.T) {
	t.Parallel()
	srv := &Server{auth: AuthConfig{Mode: AuthModeLocal}}
	called := false
	handler := srv.authMiddlewareFunc(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if !called {
		t.Error("local mode should pass through")
	}
}

func TestAuthMiddlewareFunc_EmptyMode(t *testing.T) {
	t.Parallel()
	srv := &Server{auth: AuthConfig{Mode: ""}}
	called := false
	handler := srv.authMiddlewareFunc(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if !called {
		t.Error("empty mode should pass through")
	}
}

func TestAuthMiddlewareFunc_OptionsPassthrough(t *testing.T) {
	t.Parallel()
	srv := &Server{auth: AuthConfig{Mode: AuthModeAPIKey, APIKey: "secret"}}
	called := false
	handler := srv.authMiddlewareFunc(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest(http.MethodOptions, "/api", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if !called {
		t.Error("OPTIONS should pass through regardless of auth mode")
	}
}

func TestAuthMiddlewareFunc_FailedAuth(t *testing.T) {
	t.Parallel()
	srv := &Server{auth: AuthConfig{Mode: AuthModeAPIKey, APIKey: "secret"}}
	handler := srv.authMiddlewareFunc(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called on auth failure")
	}))
	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuthMiddlewareFunc_SuccessfulAuth(t *testing.T) {
	t.Parallel()
	srv := &Server{auth: AuthConfig{Mode: AuthModeAPIKey, APIKey: "secret"}}
	called := false
	handler := srv.authMiddlewareFunc(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("X-API-Key", "secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if !called {
		t.Error("next handler should be called after successful auth")
	}
}

// ---------------------------------------------------------------------------
// authenticateRequest (66.7% → covers unsupported mode path)
// ---------------------------------------------------------------------------

func TestAuthenticateRequest_UnsupportedMode(t *testing.T) {
	t.Parallel()
	srv := &Server{auth: AuthConfig{Mode: "foobar"}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	err := srv.authenticateRequest(req)
	if err == nil {
		t.Error("expected error for unsupported auth mode")
	}
	if !strings.Contains(err.Error(), "unsupported auth mode") {
		t.Errorf("expected 'unsupported auth mode' error, got: %v", err)
	}
}

func TestAuthenticateRequest_APIKeyPath(t *testing.T) {
	t.Parallel()
	srv := &Server{auth: AuthConfig{Mode: AuthModeAPIKey, APIKey: "key123"}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "key123")
	if err := srv.authenticateRequest(req); err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
}

func TestAuthenticateRequest_MTLSPath(t *testing.T) {
	t.Parallel()
	srv := &Server{auth: AuthConfig{Mode: AuthModeMTLS}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{}},
	}
	if err := srv.authenticateRequest(req); err != nil {
		t.Errorf("expected nil error for mTLS, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// authenticateAPIKey (75.0% → covers invalid key branch)
// ---------------------------------------------------------------------------

func TestAuthenticateAPIKey_InvalidKey(t *testing.T) {
	t.Parallel()
	srv := &Server{auth: AuthConfig{Mode: AuthModeAPIKey, APIKey: "correct-key"}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	err := srv.authenticateAPIKey(req)
	if err == nil || !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("expected 'invalid api key' error, got: %v", err)
	}
}

func TestAuthenticateAPIKey_MissingKey(t *testing.T) {
	t.Parallel()
	srv := &Server{auth: AuthConfig{Mode: AuthModeAPIKey, APIKey: "correct-key"}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	err := srv.authenticateAPIKey(req)
	if err == nil || !strings.Contains(err.Error(), "missing api key") {
		t.Errorf("expected 'missing api key' error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// parseJWT (78.9% → covers error branches)
// ---------------------------------------------------------------------------

func TestParseJWT_InvalidFormat(t *testing.T) {
	t.Parallel()
	_, _, _, _, err := parseJWT("not.a.valid.jwt.too.many.parts")
	if err == nil || !strings.Contains(err.Error(), "invalid jwt format") {
		t.Errorf("expected 'invalid jwt format' error, got: %v", err)
	}
}

func TestParseJWT_BadHeaderBase64(t *testing.T) {
	t.Parallel()
	_, _, _, _, err := parseJWT("!!!bad-base64.eyJ0ZXN0IjoxfQ.sig")
	if err == nil || !strings.Contains(err.Error(), "decode jwt header") {
		t.Errorf("expected header decode error, got: %v", err)
	}
}

func TestParseJWT_BadPayloadBase64(t *testing.T) {
	t.Parallel()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	_, _, _, _, err := parseJWT(header + ".!!!bad.sig")
	if err == nil || !strings.Contains(err.Error(), "decode jwt payload") {
		t.Errorf("expected payload decode error, got: %v", err)
	}
}

func TestParseJWT_BadSignatureBase64(t *testing.T) {
	t.Parallel()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user"}`))
	_, _, _, _, err := parseJWT(header + "." + payload + ".!!!bad")
	if err == nil || !strings.Contains(err.Error(), "decode jwt signature") {
		t.Errorf("expected signature decode error, got: %v", err)
	}
}

func TestParseJWT_InvalidHeaderJSON(t *testing.T) {
	t.Parallel()
	header := base64.RawURLEncoding.EncodeToString([]byte(`not-json`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user"}`))
	sig := base64.RawURLEncoding.EncodeToString([]byte(`sig`))
	_, _, _, _, err := parseJWT(header + "." + payload + "." + sig)
	if err == nil || !strings.Contains(err.Error(), "parse jwt header") {
		t.Errorf("expected header parse error, got: %v", err)
	}
}

func TestParseJWT_InvalidPayloadJSON(t *testing.T) {
	t.Parallel()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`not-json`))
	sig := base64.RawURLEncoding.EncodeToString([]byte(`sig`))
	_, _, _, _, err := parseJWT(header + "." + payload + "." + sig)
	if err == nil || !strings.Contains(err.Error(), "parse jwt payload") {
		t.Errorf("expected payload parse error, got: %v", err)
	}
}

func TestParseJWT_ValidToken(t *testing.T) {
	t.Parallel()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"key1"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user-123","iss":"https://example.com"}`))
	sig := base64.RawURLEncoding.EncodeToString([]byte(`fakesig`))
	h, claims, signingInput, sigBytes, err := parseJWT(header + "." + payload + "." + sig)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Alg != "RS256" {
		t.Errorf("expected alg=RS256, got %q", h.Alg)
	}
	if h.Kid != "key1" {
		t.Errorf("expected kid=key1, got %q", h.Kid)
	}
	if claims["sub"] != "user-123" {
		t.Errorf("expected sub=user-123, got %v", claims["sub"])
	}
	if signingInput != header+"."+payload {
		t.Errorf("unexpected signing input: %q", signingInput)
	}
	if len(sigBytes) == 0 {
		t.Error("expected non-empty signature bytes")
	}
}

// ---------------------------------------------------------------------------
// parseRSAPublicKey (72.7% → covers error branches)
// ---------------------------------------------------------------------------

func TestParseRSAPublicKey_Valid(t *testing.T) {
	t.Parallel()
	// Generate a real RSA key and extract n/e
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	nStr := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	eStr := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
	pub, err := parseRSAPublicKey(nStr, eStr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pub.N.Cmp(key.N) != 0 {
		t.Error("modulus mismatch")
	}
	if pub.E != key.E {
		t.Errorf("exponent mismatch: got %d, want %d", pub.E, key.E)
	}
}

func TestParseRSAPublicKey_BadN(t *testing.T) {
	t.Parallel()
	_, err := parseRSAPublicKey("!!!bad-base64", base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}))
	if err == nil || !strings.Contains(err.Error(), "decode jwk n") {
		t.Errorf("expected n decode error, got: %v", err)
	}
}

func TestParseRSAPublicKey_BadE(t *testing.T) {
	t.Parallel()
	nStr := base64.RawURLEncoding.EncodeToString([]byte{0x01})
	_, err := parseRSAPublicKey(nStr, "!!!bad-base64")
	if err == nil || !strings.Contains(err.Error(), "decode jwk e") {
		t.Errorf("expected e decode error, got: %v", err)
	}
}

func TestParseRSAPublicKey_ZeroExponent(t *testing.T) {
	t.Parallel()
	nStr := base64.RawURLEncoding.EncodeToString([]byte{0x01})
	eStr := base64.RawURLEncoding.EncodeToString([]byte{}) // empty = zero exponent
	_, err := parseRSAPublicKey(nStr, eStr)
	if err == nil || !strings.Contains(err.Error(), "invalid jwk exponent") {
		t.Errorf("expected exponent error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// claimInt64 (90.0% → covers json.Number error path)
// ---------------------------------------------------------------------------

func TestClaimInt64_JsonNumberError(t *testing.T) {
	t.Parallel()
	claims := map[string]interface{}{
		"val": json.Number("not-a-number"),
	}
	_, ok := claimInt64(claims, "val")
	if ok {
		t.Error("expected ok=false for non-numeric json.Number")
	}
}

func TestClaimInt64_UnsupportedType(t *testing.T) {
	t.Parallel()
	claims := map[string]interface{}{
		"val": "string-value",
	}
	_, ok := claimInt64(claims, "val")
	if ok {
		t.Error("expected ok=false for string type")
	}
}

// ---------------------------------------------------------------------------
// writeSuccessResponse (85.7% → covers nil data + empty requestID)
// ---------------------------------------------------------------------------

func TestWriteSuccessResponse_NilData(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	writeSuccessResponse(w, http.StatusOK, nil, "")
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json decode error: %v", err)
	}
	if resp["success"] != true {
		t.Error("expected success=true")
	}
	if _, exists := resp["request_id"]; exists {
		t.Error("expected no request_id when empty")
	}
}

func TestWriteSuccessResponse_WithRequestID(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	writeSuccessResponse(w, http.StatusCreated, map[string]interface{}{"foo": "bar"}, "req-123")
	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d", w.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json decode error: %v", err)
	}
	if resp["request_id"] != "req-123" {
		t.Errorf("expected request_id=req-123, got %v", resp["request_id"])
	}
	if resp["foo"] != "bar" {
		t.Errorf("expected foo=bar, got %v", resp["foo"])
	}
}

// ---------------------------------------------------------------------------
// writeErrorResponse (90.0% → covers hint extraction path)
// ---------------------------------------------------------------------------

func TestWriteErrorResponse_WithHint(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	details := map[string]interface{}{
		"hint":  "try using a different key",
		"extra": "detail",
	}
	writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "bad input", details, "req-456")
	var resp APIError
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json decode error: %v", err)
	}
	if resp.Hint != "try using a different key" {
		t.Errorf("expected hint, got %q", resp.Hint)
	}
	if resp.Details == nil || resp.Details["extra"] != "detail" {
		t.Errorf("expected extra detail preserved, got %v", resp.Details)
	}
}

func TestWriteErrorResponse_HintOnlyDetail(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	details := map[string]interface{}{
		"hint": "only hint, no other details",
	}
	writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "bad", details, "")
	var resp APIError
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json decode error: %v", err)
	}
	if resp.Hint != "only hint, no other details" {
		t.Errorf("expected hint, got %q", resp.Hint)
	}
	// When hint is the only detail, details should be nil after extraction
	if resp.Details != nil {
		t.Errorf("expected nil details after hint extraction, got %v", resp.Details)
	}
}

// ---------------------------------------------------------------------------
// loggingMiddleware (75.0% → covers reqID branch)
// ---------------------------------------------------------------------------

func TestLoggingMiddleware_WithRequestID(t *testing.T) {
	t.Parallel()
	called := false
	handler := loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := context.WithValue(req.Context(), requestIDKey, "test-req-id")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if !called {
		t.Error("next handler should be called")
	}
}

func TestLoggingMiddleware_WithoutRequestID(t *testing.T) {
	t.Parallel()
	called := false
	handler := loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if !called {
		t.Error("next handler should be called")
	}
}

type fakeAttentionStreamFeed struct {
	stats        robot.JournalStats
	replayEvents []robot.AttentionEvent
	replayNewest int64
	replayErr    error
	onReplay     func()
	subscriber   robot.AttentionHandler
}

func (f *fakeAttentionStreamFeed) Stats() robot.JournalStats {
	return f.stats
}

func (f *fakeAttentionStreamFeed) Replay(_ int64, _ int) ([]robot.AttentionEvent, int64, error) {
	if f.onReplay != nil {
		f.onReplay()
	}
	events := append([]robot.AttentionEvent(nil), f.replayEvents...)
	return events, f.replayNewest, f.replayErr
}

func (f *fakeAttentionStreamFeed) Subscribe(handler robot.AttentionHandler) func() {
	f.subscriber = handler
	return func() {
		f.subscriber = nil
	}
}

func (f *fakeAttentionStreamFeed) emit(event robot.AttentionEvent) {
	if f.subscriber != nil {
		f.subscriber(event)
	}
}

func TestPrepareAttentionStream_ReplayBoundaryPreservesLiveEvents(t *testing.T) {
	t.Parallel()

	liveEvent := robot.AttentionEvent{Cursor: 2, Summary: "live"}
	feed := &fakeAttentionStreamFeed{
		stats: robot.JournalStats{
			Count:           1,
			OldestCursor:    1,
			NewestCursor:    1,
			RetentionPeriod: time.Hour,
		},
		replayEvents: []robot.AttentionEvent{
			{Cursor: 1, Summary: "baseline"},
			liveEvent,
		},
		replayNewest: 2,
	}
	feed.onReplay = func() {
		feed.emit(liveEvent)
	}

	prepared, err := prepareAttentionStream(feed, 0, 4)
	if err != nil {
		t.Fatalf("prepareAttentionStream() error = %v", err)
	}
	defer prepared.unsubscribe()

	if prepared.replayBoundary != 1 {
		t.Fatalf("replayBoundary = %d, want 1", prepared.replayBoundary)
	}
	if len(prepared.replayEvents) != 1 || prepared.replayEvents[0].Cursor != 1 {
		t.Fatalf("replayEvents = %+v, want only cursor 1", prepared.replayEvents)
	}

	select {
	case got := <-prepared.eventCh:
		if got.Cursor != liveEvent.Cursor {
			t.Fatalf("live event cursor = %d, want %d", got.Cursor, liveEvent.Cursor)
		}
	default:
		t.Fatal("expected live event queued during replay boundary")
	}
}

func TestWriteAttentionReplay_AdvancesCursorToLastDeliveredEvent(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	events := []robot.AttentionEvent{
		{Cursor: 4, Session: "alpha", Summary: "first"},
		{Cursor: 7, Session: "alpha", Summary: "second"},
	}

	finalCursor, delivered, err := writeAttentionReplay(rec, rec, events, nil, "", nil, 2)
	if err != nil {
		t.Fatalf("writeAttentionReplay() error = %v", err)
	}
	if delivered != 2 {
		t.Fatalf("delivered = %d, want 2", delivered)
	}
	if finalCursor != 7 {
		t.Fatalf("finalCursor = %d, want 7", finalCursor)
	}

	body := rec.Body.String()
	if strings.Count(body, "event: attention") != 2 {
		t.Fatalf("expected 2 replayed attention events, got body %q", body)
	}
	if !rec.Flushed {
		t.Fatal("expected replay writer to flush")
	}
}

func TestWriteAttentionReplay_SuppressesReplayHeartbeatEventsAndPreservesCursor(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	events := []robot.AttentionEvent{
		{Cursor: 12, Type: robot.EventType(robot.DefaultTransportLiveness.HeartbeatType), Summary: "heartbeat"},
		{Cursor: 7, Session: "alpha", Summary: "real"},
	}

	finalCursor, delivered, err := writeAttentionReplay(rec, rec, events, nil, "", nil, 10)
	if err != nil {
		t.Fatalf("writeAttentionReplay() error = %v", err)
	}
	if delivered != 1 {
		t.Fatalf("delivered = %d, want 1", delivered)
	}
	if finalCursor != 12 {
		t.Fatalf("finalCursor = %d, want 12", finalCursor)
	}

	body := rec.Body.String()
	if strings.Contains(body, "heartbeat") {
		t.Fatalf("replay should not emit transport heartbeat events: %q", body)
	}
	if strings.Count(body, "event: attention") != 1 {
		t.Fatalf("expected 1 replayed attention event, got body %q", body)
	}
}

func TestPrepareAttentionStream_CursorExpired(t *testing.T) {
	t.Parallel()

	feed := &fakeAttentionStreamFeed{
		stats: robot.JournalStats{
			Count:           3,
			OldestCursor:    10,
			NewestCursor:    12,
			RetentionPeriod: time.Hour,
		},
	}

	prepared, err := prepareAttentionStream(feed, 5, 4)
	if err == nil {
		t.Fatal("expected cursor-expired error")
	}
	if prepared != nil {
		t.Fatalf("prepared stream = %#v, want nil on error", prepared)
	}

	var cursorErr *robot.CursorExpiredError
	if !errors.As(err, &cursorErr) {
		t.Fatalf("error = %T, want *robot.CursorExpiredError", err)
	}
	if cursorErr.EarliestCursor != 10 {
		t.Fatalf("EarliestCursor = %d, want 10", cursorErr.EarliestCursor)
	}
	if feed.subscriber != nil {
		t.Fatal("subscriber should be released after cursor-expired setup failure")
	}
}

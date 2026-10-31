package serve

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

func TestRedactionMiddleware_Disabled(t *testing.T) {
	t.Log("TEST: TestRedactionMiddleware_Disabled - starting")

	// Server with redaction disabled
	s := &Server{}

	handler := s.redactionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"message":"sk-proj-FAKEtestkey1234567890123456789012345678901234"}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"key":"sk-proj-FAKEtestkey1234567890123456789012345678901234"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	t.Logf("TEST: TestRedactionMiddleware_Disabled - got response: %s", body)

	// Secret should pass through unchanged when redaction is disabled
	if !strings.Contains(body, "sk-proj-FAKE") {
		t.Errorf("expected secret to pass through when redaction disabled, got: %s", body)
	}
	t.Log("TEST: TestRedactionMiddleware_Disabled - passed")
}

func TestRedactionMiddleware_ModeOff(t *testing.T) {
	t.Log("TEST: TestRedactionMiddleware_ModeOff - starting")

	s := &Server{
		redactionCfg: &RedactionConfig{
			Enabled: true,
			Config:  redaction.Config{Mode: redaction.ModeOff},
		},
	}

	handler := s.redactionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"message":"sk-proj-FAKEtestkey1234567890123456789012345678901234"}`))
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	t.Logf("TEST: TestRedactionMiddleware_ModeOff - got response: %s", body)

	if !strings.Contains(body, "sk-proj-FAKE") {
		t.Errorf("expected secret to pass through in off mode, got: %s", body)
	}
	t.Log("TEST: TestRedactionMiddleware_ModeOff - passed")
}

func TestRedactionMiddleware_RedactRequestRejectsInsteadOfSubstituting(t *testing.T) {
	testSecret := "sk-proj-FAKEtestkey1234567890123456789012345678901234"
	s := &Server{
		redactionCfg: &RedactionConfig{
			Enabled: true,
			Config:  redaction.Config{Mode: redaction.ModeRedact},
		},
	}

	handlerCalled := false
	handler := s.redactionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusNoContent)
	}))

	reqBody := `{"key":"` + testSecret + `"}`
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if handlerCalled {
		t.Fatal("handler received a credential-bearing request that could save or execute a placeholder")
	}
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rr.Code, rr.Body)
	}
	var response APIError
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("rejection must be valid JSON: %v", err)
	}
	if response.ErrorCode != "SECRETS_DETECTED" || response.Success {
		t.Errorf("unexpected rejection envelope: %+v", response)
	}
	if strings.Contains(rr.Body.String(), testSecret) {
		t.Error("rejection exposed the submitted credential")
	}
}

func TestRedactionMiddleware_RedactResponse(t *testing.T) {
	t.Log("TEST: TestRedactionMiddleware_RedactResponse - starting")

	testSecret := "sk-proj-FAKEtestkey1234567890123456789012345678901234"
	s := &Server{
		redactionCfg: &RedactionConfig{
			Enabled: true,
			Config:  redaction.Config{Mode: redaction.ModeRedact},
		},
	}

	handler := s.redactionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"message":"Error with key: ` + testSecret + `"}`))
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	t.Logf("TEST: TestRedactionMiddleware_RedactResponse - got response: %s", body)

	// Response body should have secret redacted
	if strings.Contains(body, testSecret) {
		t.Errorf("response body still contains secret: %s", body)
	}
	if !strings.Contains(body, "[REDACTED:") {
		t.Errorf("response body should contain redaction marker: %s", body)
	}
	t.Log("TEST: TestRedactionMiddleware_RedactResponse - passed")
}

func TestRedactionMiddleware_BlockRequest(t *testing.T) {
	t.Log("TEST: TestRedactionMiddleware_BlockRequest - starting")

	testSecret := "sk-proj-FAKEtestkey1234567890123456789012345678901234"
	s := &Server{
		redactionCfg: &RedactionConfig{
			Enabled: true,
			Config:  redaction.Config{Mode: redaction.ModeBlock},
		},
	}

	handlerCalled := false
	handler := s.redactionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	reqBody := `{"key":"` + testSecret + `"}`
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	t.Logf("TEST: TestRedactionMiddleware_BlockRequest - status: %d, body: %s", rr.Code, rr.Body.String())

	// Request should be blocked
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected status 422, got: %d", rr.Code)
	}
	if handlerCalled {
		t.Error("handler should not have been called when request is blocked")
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp["error_code"] != "SECRETS_DETECTED" {
		t.Errorf("expected error_code SECRETS_DETECTED, got: %v", resp["error_code"])
	}
	t.Log("TEST: TestRedactionMiddleware_BlockRequest - passed")
}

func TestRedactionMiddleware_NonJSONPassthrough(t *testing.T) {
	t.Log("TEST: TestRedactionMiddleware_NonJSONPassthrough - starting")

	testSecret := "sk-proj-FAKEtestkey1234567890123456789012345678901234"
	s := &Server{
		redactionCfg: &RedactionConfig{
			Enabled: true,
			Config:  redaction.Config{Mode: redaction.ModeRedact},
		},
	}

	handler := s.redactionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Secret: " + testSecret))
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	t.Logf("TEST: TestRedactionMiddleware_NonJSONPassthrough - got response: %s", body)

	// Non-JSON content should pass through without redaction
	if !strings.Contains(body, testSecret) {
		t.Errorf("non-JSON content should pass through unchanged, got: %s", body)
	}
	t.Log("TEST: TestRedactionMiddleware_NonJSONPassthrough - passed")
}

func TestRedactionMiddleware_WarnMode(t *testing.T) {
	t.Log("TEST: TestRedactionMiddleware_WarnMode - starting")

	testSecret := "sk-proj-FAKEtestkey1234567890123456789012345678901234"
	s := &Server{
		redactionCfg: &RedactionConfig{
			Enabled: true,
			Config:  redaction.Config{Mode: redaction.ModeWarn},
		},
	}

	handler := s.redactionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"message":"` + testSecret + `"}`))
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	t.Logf("TEST: TestRedactionMiddleware_WarnMode - got response: %s", body)

	// Warn mode should not redact, just log
	if !strings.Contains(body, testSecret) {
		t.Errorf("warn mode should not modify content, got: %s", body)
	}
	t.Log("TEST: TestRedactionMiddleware_WarnMode - passed")
}

func TestIsJSONContent(t *testing.T) {
	tests := []struct {
		contentType string
		want        bool
	}{
		{"application/json", true},
		{"application/json; charset=utf-8", true},
		{"Application/JSON", true},                // case-insensitive per RFC 2616
		{"APPLICATION/JSON", true},                // case-insensitive per RFC 2616
		{"Application/Json; charset=utf-8", true}, // case-insensitive per RFC 2616
		{"text/plain", false},
		{"text/html", false},
		{"application/xml", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.contentType, func(t *testing.T) {
			got := isJSONContent(tt.contentType)
			if got != tt.want {
				t.Errorf("isJSONContent(%q) = %v, want %v", tt.contentType, got, tt.want)
			}
		})
	}
}

func TestRedactionSummary_Logging(t *testing.T) {
	// Just ensure the function doesn't panic
	summary := &RedactionSummary{
		RequestID:     "test-123",
		Path:          "/api/test",
		Method:        "POST",
		RequestFinds:  2,
		ResponseFinds: 1,
		Categories:    map[string]int{"OPENAI_KEY": 2, "GITHUB_TOKEN": 1},
		Blocked:       false,
	}

	// This should not panic
	logRedactionSummary(summary)
}

func TestRedactingResponseWriter(t *testing.T) {
	t.Log("TEST: TestRedactingResponseWriter - starting")

	testSecret := "sk-proj-FAKEtestkey1234567890123456789012345678901234"
	cfg := redaction.Config{Mode: redaction.ModeRedact}

	t.Run("basic_redaction", func(t *testing.T) {
		rr := httptest.NewRecorder()
		summary := &RedactionSummary{}
		categories := make(map[string]int)

		rw := &redactingResponseWriter{
			ResponseWriter: rr,
			cfg:            cfg,
			summary:        summary,
			categories:     categories,
			buffer:         &bytes.Buffer{},
		}

		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusOK)
		rw.Write([]byte(`{"key":"` + testSecret + `"}`))
		rw.finalize()

		body := rr.Body.String()
		t.Logf("TEST: TestRedactingResponseWriter/basic_redaction - body: %s", body)

		if strings.Contains(body, testSecret) {
			t.Errorf("response should be redacted: %s", body)
		}
		if summary.ResponseFinds == 0 {
			t.Error("expected response findings > 0")
		}
	})

	t.Run("empty_response", func(t *testing.T) {
		rr := httptest.NewRecorder()
		summary := &RedactionSummary{}
		categories := make(map[string]int)

		rw := &redactingResponseWriter{
			ResponseWriter: rr,
			cfg:            cfg,
			summary:        summary,
			categories:     categories,
			buffer:         &bytes.Buffer{},
		}

		rw.WriteHeader(http.StatusNoContent)
		rw.finalize()

		if rr.Code != http.StatusNoContent {
			t.Errorf("expected 204, got: %d", rr.Code)
		}
	})

	t.Log("TEST: TestRedactingResponseWriter - passed")
}

type hijackableRecorder struct {
	*httptest.ResponseRecorder
	writeCalls       int
	writeHeaderCalls int
	conn             net.Conn
	peer             net.Conn
}

func newHijackableRecorder() *hijackableRecorder {
	return &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *hijackableRecorder) WriteHeader(code int) {
	r.writeHeaderCalls++
	r.ResponseRecorder.WriteHeader(code)
}

func (r *hijackableRecorder) Write(b []byte) (int, error) {
	r.writeCalls++
	return r.ResponseRecorder.Write(b)
}

func (r *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	serverConn, clientConn := net.Pipe()
	r.conn = serverConn
	r.peer = clientConn
	return serverConn, bufio.NewReadWriter(bufio.NewReader(serverConn), bufio.NewWriter(serverConn)), nil
}

func (r *hijackableRecorder) closeConns() {
	if r.conn != nil {
		_ = r.conn.Close()
	}
	if r.peer != nil {
		_ = r.peer.Close()
	}
}

func TestRedactionMiddleware_PreservesHijacker(t *testing.T) {
	s := &Server{
		redactionCfg: &RedactionConfig{
			Enabled: true,
			Config:  redaction.Config{Mode: redaction.ModeRedact},
		},
	}

	handler := s.redactionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("wrapped response writer does not implement http.Hijacker")
		}
		if _, _, err := hijacker.Hijack(); err != nil {
			t.Fatalf("Hijack() error = %v", err)
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	rr := newHijackableRecorder()
	t.Cleanup(rr.closeConns)

	handler.ServeHTTP(rr, req)

	if rr.writeCalls != 0 {
		t.Fatalf("expected no body writes after hijack, got %d", rr.writeCalls)
	}
	if rr.writeHeaderCalls != 0 {
		t.Fatalf("expected no header writes after hijack, got %d", rr.writeHeaderCalls)
	}
}

func TestRedactingResponseWriter_FlushPreservesStatusWithoutBufferedBody(t *testing.T) {
	rr := httptest.NewRecorder()
	rw := &redactingResponseWriter{
		ResponseWriter: rr,
		cfg:            redaction.Config{Mode: redaction.ModeRedact},
		summary:        &RedactionSummary{},
		categories:     make(map[string]int),
		buffer:         &bytes.Buffer{},
	}

	rw.Header().Set("Content-Type", "text/event-stream")
	rw.WriteHeader(http.StatusAccepted)
	rw.Flush()
	if _, err := rw.Write([]byte("event: ping\n\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status code = %d, want %d", rr.Code, http.StatusAccepted)
	}
	if got := rr.Body.String(); got != "event: ping\n\n" {
		t.Fatalf("body = %q, want %q", got, "event: ping\n\n")
	}
}

// =============================================================================
// WebSocket Event Redaction Tests (bd-1s8x2)
// =============================================================================

func TestWSHubRedaction_Disabled(t *testing.T) {
	t.Log("TEST: TestWSHubRedaction_Disabled - starting")

	hub := NewWSHub()
	// No redaction config set

	testSecret := "sk-proj-FAKEtestkey1234567890123456789012345678901234"
	data := map[string]interface{}{
		"message": "Error with key: " + testSecret,
	}

	// redactWSEventData should return unchanged data when disabled
	result, err := redactWSEventData(data, redaction.Config{Mode: redaction.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	resultMap := result.(map[string]interface{})

	t.Logf("TEST: TestWSHubRedaction_Disabled - result: %v", resultMap)

	if resultMap["message"] != data["message"] {
		t.Errorf("data should be unchanged when redaction disabled")
	}

	// Verify hub getter returns nil
	if hub.getRedactionCfgForTest() != nil {
		t.Error("expected nil config initially")
	}

	t.Log("TEST: TestWSHubRedaction_Disabled - passed")
}

func TestWSHubRedaction_RedactMode(t *testing.T) {
	t.Log("TEST: TestWSHubRedaction_RedactMode - starting")

	testSecret := "sk-proj-FAKEtestkey1234567890123456789012345678901234"
	cfg := redaction.Config{Mode: redaction.ModeRedact}

	data := map[string]interface{}{
		"message": "Error with key: " + testSecret,
		"count":   42,
		"nested": map[string]interface{}{
			"secret": testSecret,
			"safe":   "no secrets here",
		},
	}

	result, err := redactWSEventData(data, cfg)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var resultMap map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&resultMap); err != nil {
		t.Fatal(err)
	}

	t.Logf("TEST: TestWSHubRedaction_RedactMode - result: %v", resultMap)

	// Check top-level message is redacted
	msg := resultMap["message"].(string)
	if strings.Contains(msg, testSecret) {
		t.Errorf("message should be redacted: %s", msg)
	}
	if !strings.Contains(msg, "[REDACTED:") {
		t.Errorf("message should contain redaction marker: %s", msg)
	}

	// Check count is preserved
	if resultMap["count"] != json.Number("42") {
		t.Errorf("count should be preserved: %v", resultMap["count"])
	}

	// Check nested map
	nested := resultMap["nested"].(map[string]interface{})
	nestedSecret := nested["secret"].(string)
	if strings.Contains(nestedSecret, testSecret) {
		t.Errorf("nested secret should be redacted: %s", nestedSecret)
	}
	if nested["safe"] != "no secrets here" {
		t.Errorf("nested safe should be preserved: %v", nested["safe"])
	}

	t.Log("TEST: TestWSHubRedaction_RedactMode - passed")
}

func TestWSHubRedaction_ArrayData(t *testing.T) {
	t.Log("TEST: TestWSHubRedaction_ArrayData - starting")

	testSecret := "sk-proj-FAKEtestkey1234567890123456789012345678901234"
	cfg := redaction.Config{Mode: redaction.ModeRedact}

	data := []interface{}{
		"safe string",
		testSecret,
		map[string]interface{}{"key": testSecret},
	}

	result, err := redactWSEventData(data, cfg)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var resultSlice []interface{}
	if err := json.Unmarshal(encoded, &resultSlice); err != nil {
		t.Fatal(err)
	}

	t.Logf("TEST: TestWSHubRedaction_ArrayData - result: %v", resultSlice)

	// First element should be unchanged
	if resultSlice[0] != "safe string" {
		t.Errorf("first element should be unchanged: %v", resultSlice[0])
	}

	// Second element should be redacted
	if strings.Contains(resultSlice[1].(string), testSecret) {
		t.Errorf("second element should be redacted: %v", resultSlice[1])
	}

	// Third element map should be redacted
	mapItem := resultSlice[2].(map[string]interface{})
	if strings.Contains(mapItem["key"].(string), testSecret) {
		t.Errorf("nested map key should be redacted: %v", mapItem["key"])
	}

	t.Log("TEST: TestWSHubRedaction_ArrayData - passed")
}

func TestWSHubRedaction_WarnMode(t *testing.T) {
	t.Log("TEST: TestWSHubRedaction_WarnMode - starting")

	testSecret := "sk-proj-FAKEtestkey1234567890123456789012345678901234"
	cfg := redaction.Config{Mode: redaction.ModeWarn}

	data := map[string]interface{}{
		"message": "Secret: " + testSecret,
	}

	result, err := redactWSEventData(data, cfg)
	if err != nil {
		t.Fatal(err)
	}
	resultMap := result.(map[string]interface{})

	t.Logf("TEST: TestWSHubRedaction_WarnMode - result: %v", resultMap)

	// Warn mode should not modify content
	if !strings.Contains(resultMap["message"].(string), testSecret) {
		t.Errorf("warn mode should not modify content: %v", resultMap["message"])
	}

	t.Log("TEST: TestWSHubRedaction_WarnMode - passed")
}

func TestWSHubSetGetRedactionConfig(t *testing.T) {
	t.Log("TEST: TestWSHubSetGetRedactionConfig - starting")

	hub := NewWSHub()

	// Initially nil
	if hub.getRedactionCfgForTest() != nil {
		t.Error("expected nil config initially")
	}

	// Set config
	cfg := &RedactionConfig{
		Enabled: true,
		Config:  redaction.Config{Mode: redaction.ModeRedact},
	}
	hub.setRedactionCfgForTest(cfg)

	got := hub.getRedactionCfgForTest()
	if got == nil {
		t.Fatal("expected non-nil config")
	}
	if !got.Enabled {
		t.Error("expected enabled = true")
	}
	if got.Config.Mode != redaction.ModeRedact {
		t.Errorf("mode = %q, want %q", got.Config.Mode, redaction.ModeRedact)
	}

	// Clear config
	hub.setRedactionCfgForTest(nil)
	if hub.getRedactionCfgForTest() != nil {
		t.Error("expected nil config after clearing")
	}

	t.Log("TEST: TestWSHubSetGetRedactionConfig - passed")
}

func TestWSHubRedaction_NonStringTypes(t *testing.T) {
	t.Log("TEST: TestWSHubRedaction_NonStringTypes - starting")

	cfg := redaction.Config{Mode: redaction.ModeRedact}

	// Test various non-string types
	tests := []struct {
		name  string
		input interface{}
	}{
		{"int", 42},
		{"float", 3.14},
		{"bool", true},
		{"nil", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := redactWSEventData(tt.input, cfg)
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("non-string JSON type should be unchanged: got %s, want %s", got, want)
			}
		})
	}

	t.Log("TEST: TestWSHubRedaction_NonStringTypes - passed")
}

// =============================================================================
// Integration Tests: REST + WebSocket Redaction (bd-15zf0)
// End-to-end coverage ensuring secrets don't leak via API or WebSocket
// =============================================================================

func TestIntegration_RESTRedaction(t *testing.T) {
	t.Log("TEST: TestIntegration_RESTRedaction - starting")

	testSecret := "sk-proj-FAKEtestkey1234567890123456789012345678901234"

	// Create server with redaction enabled
	s := &Server{
		redactionCfg: &RedactionConfig{
			Enabled: true,
			Config:  redaction.Config{Mode: redaction.ModeRedact},
		},
	}

	// Successful input admission must still reach response redaction. Echo an
	// ordinary request unchanged and add a server-side secret to the response.
	var receivedBody string
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read admitted request: %v", err)
		}
		receivedBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		resp := map[string]interface{}{
			"received":      string(body),
			"server_secret": testSecret,
		}
		json.NewEncoder(w).Encode(resp)
	})

	// Wrap with redaction middleware
	handler := s.redactionMiddleware(testHandler)

	// Preserve exact request bytes while redacting the separate response value.
	reqBody := " \n{ \"safe\" : \"no secret here\" }\t\n"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/test", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	t.Logf("TEST: TestIntegration_RESTRedaction - status: %d", rr.Code)
	t.Logf("TEST: TestIntegration_RESTRedaction - response: %s", rr.Body.String())

	// Verify status
	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got: %d", rr.Code)
	}
	if receivedBody != reqBody {
		t.Error("ordinary request bytes changed before reaching the handler")
	}

	// Parse response
	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp["received"] != reqBody {
		t.Errorf("response changed ordinary echoed request: %v", resp["received"])
	}

	// CRITICAL: Verify no raw secret in response
	fullResp := rr.Body.String()
	if strings.Contains(fullResp, testSecret) {
		t.Errorf("SECURITY: response contains raw secret! response: %s", fullResp)
	}

	// Verify redaction markers present
	if !strings.Contains(fullResp, "[REDACTED:") {
		t.Errorf("expected redaction markers in response: %s", fullResp)
	}

	// Verify safe content preserved
	if !strings.Contains(fullResp, "no secret here") {
		t.Errorf("safe content should be preserved: %s", fullResp)
	}

	t.Log("TEST: TestIntegration_RESTRedaction - PASSED: No secrets leaked via REST")
}

func TestIntegration_WSEventRedaction(t *testing.T) {
	t.Log("TEST: TestIntegration_WSEventRedaction - starting")

	testSecret := "ghp_FAKEtesttokenvalue12345678901234567"

	// Create hub with redaction enabled
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	hub.setRedactionCfgForTest(&RedactionConfig{
		Enabled: true,
		Config:  redaction.Config{Mode: redaction.ModeRedact},
	})

	// Create a test client
	client := &WSClient{
		id:     "test-integration-client",
		hub:    hub,
		send:   make(chan []byte, 10),
		topics: make(map[string]struct{}),
	}
	client.Subscribe([]string{"test:*"})
	requireRegisterWSClient(t, hub, client)

	// Give hub time to register client
	time.Sleep(20 * time.Millisecond)

	// Publish an event with a secret
	hub.Publish("test:integration", "test.event", map[string]interface{}{
		"message":     "Error: authentication failed with token " + testSecret,
		"safe_field":  "This is safe",
		"nested_data": map[string]interface{}{"secret": testSecret},
	})

	// Wait for event to be delivered
	select {
	case data := <-client.send:
		t.Logf("TEST: TestIntegration_WSEventRedaction - received event: %s", string(data))

		// CRITICAL: Verify no raw secret in event
		eventStr := string(data)
		if strings.Contains(eventStr, testSecret) {
			t.Errorf("SECURITY: WebSocket event contains raw secret! event: %s", eventStr)
		}

		// Verify redaction marker present
		if !strings.Contains(eventStr, "[REDACTED:") {
			t.Errorf("expected redaction markers in event: %s", eventStr)
		}

		// Verify safe content preserved
		if !strings.Contains(eventStr, "This is safe") {
			t.Errorf("safe content should be preserved: %s", eventStr)
		}

		// Verify structure is valid JSON
		var event WSEvent
		if err := json.Unmarshal(data, &event); err != nil {
			t.Errorf("event should be valid JSON: %v", err)
		}

	case <-time.After(500 * time.Millisecond):
		t.Error("timeout waiting for WebSocket event")
	}

	hub.UnregisterClient(client)
	t.Log("TEST: TestIntegration_WSEventRedaction - PASSED: No secrets leaked via WebSocket")
}

func TestIntegration_BlockModeRejectsSecrets(t *testing.T) {
	t.Log("TEST: TestIntegration_BlockModeRejectsSecrets - starting")

	testSecret := "sk-ant-api03-FAKEkey1234567890123456789012345678901234567890"

	// Create server with block mode
	s := &Server{
		redactionCfg: &RedactionConfig{
			Enabled: true,
			Config:  redaction.Config{Mode: redaction.ModeBlock},
		},
	}

	handlerCalled := false
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	handler := s.redactionMiddleware(testHandler)

	// Send request with secret
	reqBody := `{"key":"` + testSecret + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/send", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	t.Logf("TEST: TestIntegration_BlockModeRejectsSecrets - status: %d, body: %s", rr.Code, rr.Body.String())

	// Request should be blocked
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected status 422 (blocked), got: %d", rr.Code)
	}

	// Handler should not have been called
	if handlerCalled {
		t.Error("handler should NOT be called when request is blocked")
	}

	// Verify error response
	var errResp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to parse error response: %v", err)
	}

	if errResp["error_code"] != "SECRETS_DETECTED" {
		t.Errorf("expected error_code SECRETS_DETECTED, got: %v", errResp["error_code"])
	}

	// CRITICAL: Verify secret not in error response
	if strings.Contains(rr.Body.String(), testSecret) {
		t.Errorf("SECURITY: error response should NOT contain the secret!")
	}

	t.Log("TEST: TestIntegration_BlockModeRejectsSecrets - PASSED: Block mode correctly rejects secrets")
}

func TestIntegration_MultipleSecretTypes(t *testing.T) {
	t.Log("TEST: TestIntegration_MultipleSecretTypes - starting")

	// Test various secret patterns
	secrets := map[string]string{
		"openai":    "sk-proj-FAKEtestkey1234567890123456789012345678901234",
		"anthropic": "sk-ant-api03-FAKEkey1234567890123456789012345678901234567890",
		"github":    "ghp_FAKEtesttokenvalue12345678901234567",
		"aws":       "AKIAFAKETEST12345678",
	}

	s := &Server{
		redactionCfg: &RedactionConfig{
			Enabled: true,
			Config:  redaction.Config{Mode: redaction.ModeRedact},
		},
	}

	for name, secret := range secrets {
		t.Run(name, func(t *testing.T) {
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]string{"secret": secret})
			})

			handler := s.redactionMiddleware(testHandler)

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			resp := rr.Body.String()
			t.Logf("TEST: %s secret response: %s", name, resp)

			if strings.Contains(resp, secret) {
				t.Errorf("SECURITY: %s secret leaked in response: %s", name, resp)
			}
			if !strings.Contains(resp, "[REDACTED:") {
				t.Errorf("%s secret should be redacted: %s", name, resp)
			}
		})
	}

	t.Log("TEST: TestIntegration_MultipleSecretTypes - PASSED")
}

// Test-only accessors for WSHub redaction config (the exported accessors were
// removed as dead code; live paths read h.redactionCfg directly).
func (h *WSHub) setRedactionCfgForTest(cfg *RedactionConfig) {
	h.redactionMu.Lock()
	defer h.redactionMu.Unlock()
	h.redactionCfg = cfg
}

func (h *WSHub) getRedactionCfgForTest() *RedactionConfig {
	h.redactionMu.RLock()
	defer h.redactionMu.RUnlock()
	return h.redactionCfg
}

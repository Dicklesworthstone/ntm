package serve

import (
	"bytes"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// The rest of redact_test.go builds `&Server{redactionCfg: ...}` by hand, which
// exercises the middleware but says nothing about whether anything ever puts a
// config there. Nothing did: redactionMiddleware was mounted in b5d45e20 and the
// only writers of the field lived in tests, so the G1 dead-code gate deleted the
// Set/GetRedactionConfig setters in 670f6380 and REST/WS redaction was a no-op in
// every shipped build. These tests go through New() so that regression cannot
// return silently.

const wiringFakeSecret = "sk-proj-FAKEtestkey1234567890123456789012345678901234"

func newWiringTestServer(t *testing.T, rcfg *RedactionConfig) *Server {
	t.Helper()
	s := New(Config{Host: "127.0.0.1", Port: 0, Redaction: rcfg})
	t.Cleanup(s.Stop)
	return s
}

func TestNewWiresRedactionIntoRESTMiddleware(t *testing.T) {
	s := newWiringTestServer(t, &RedactionConfig{
		Enabled: true,
		Config:  redaction.Config{Mode: redaction.ModeRedact},
	})

	handler := s.redactionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{"message":"` + wiringFakeSecret + `"}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/test", nil))

	if body := rr.Body.String(); strings.Contains(body, wiringFakeSecret) {
		t.Errorf("New did not wire Config.Redaction into redactionMiddleware; secret survived: %s", body)
	}
}

func TestNewWiresRedactionIntoWSHub(t *testing.T) {
	s := newWiringTestServer(t, &RedactionConfig{
		Enabled: true,
		Config:  redaction.Config{Mode: redaction.ModeRedact},
	})

	got := s.wsHub.getRedactionCfgForTest()
	if got == nil {
		t.Fatal("New did not wire Config.Redaction into the WebSocket hub; broadcastEvent would ship secrets to subscribers")
	}
	if !got.Enabled || got.Config.Mode != redaction.ModeRedact {
		t.Errorf("hub got enabled=%v mode=%v, want enabled=true mode=%v", got.Enabled, got.Config.Mode, redaction.ModeRedact)
	}
}

func TestNewWithoutRedactionLeavesBothConsumersInert(t *testing.T) {
	s := newWiringTestServer(t, nil)

	if s.redactionCfg != nil {
		t.Errorf("nil Config.Redaction must leave the middleware inert, got %+v", s.redactionCfg)
	}
	if got := s.wsHub.getRedactionCfgForTest(); got != nil {
		t.Errorf("nil Config.Redaction must leave the hub inert, got %+v", got)
	}
}

// New deep-copies so the caller's reference-typed fields cannot be mutated out
// from under either consumer, and so the two consumers cannot alias each other
// (the hazard bd-oekc2 fixed for the since-deleted setters in fa8045ea/0b0f0874).
func TestNewDeepCopiesRedactionConfig(t *testing.T) {
	caller := &RedactionConfig{
		Enabled: true,
		Config: redaction.Config{
			Mode:               redaction.ModeRedact,
			Allowlist:          []string{"keep-me"},
			ExtraPatterns:      map[redaction.Category][]string{"CUSTOM": {"pat"}},
			DisabledCategories: []redaction.Category{"JWT"},
		},
	}
	s := newWiringTestServer(t, caller)

	caller.Enabled = false
	caller.Config.Mode = redaction.ModeOff
	caller.Config.Allowlist[0] = "mutated"
	caller.Config.ExtraPatterns["CUSTOM"][0] = "mutated"
	caller.Config.DisabledCategories[0] = "MUTATED"

	hub := s.wsHub.getRedactionCfgForTest()
	if hub == nil {
		t.Fatal("hub config missing")
	}
	for name, got := range map[string]*RedactionConfig{"server": s.redactionCfg, "hub": hub} {
		if !got.Enabled || got.Config.Mode != redaction.ModeRedact {
			t.Errorf("%s: caller mutation leaked into stored config: enabled=%v mode=%v", name, got.Enabled, got.Config.Mode)
		}
		if got.Config.Allowlist[0] != "keep-me" {
			t.Errorf("%s: Allowlist aliases the caller's slice: %q", name, got.Config.Allowlist[0])
		}
		if got.Config.ExtraPatterns["CUSTOM"][0] != "pat" {
			t.Errorf("%s: ExtraPatterns aliases the caller's map: %q", name, got.Config.ExtraPatterns["CUSTOM"][0])
		}
		if got.Config.DisabledCategories[0] != "JWT" {
			t.Errorf("%s: DisabledCategories aliases the caller's slice: %q", name, got.Config.DisabledCategories[0])
		}
	}

	// The two consumers must not share backing arrays with each other either.
	s.redactionCfg.Config.Allowlist[0] = "server-only"
	if hub.Config.Allowlist[0] != "keep-me" {
		t.Errorf("server and hub configs alias the same Allowlist backing array: %q", hub.Config.Allowlist[0])
	}
}

func wiringJSONRequest(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"
	return req
}

func assertWiringSecretRefusal(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body)
	}
	var result APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON refusal: %v", err)
	}
	if result.Success || result.ErrorCode != "SECRETS_DETECTED" || result.Error == "" {
		t.Fatalf("missing explicit secret refusal: %+v", result)
	}
	if strings.Contains(rec.Body.String(), wiringFakeSecret) {
		t.Fatal("secret appeared in refusal response")
	}
}

// Policy text is written verbatim to the effective policy file. A placeholder
// inside the YAML remains syntactically valid and would silently change the
// policy, so merely preserving the surrounding JSON is insufficient.
func TestRedactionRouterPreservesPolicyOnRejectedRequest(t *testing.T) {
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		t.Run(string(mode), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			policyPath := filepath.Join(home, ".ntm", "policy.yaml")
			if err := os.MkdirAll(filepath.Dir(policyPath), 0700); err != nil {
				t.Fatal(err)
			}
			original := "version: 1\nblocked:\n  - pattern: original-danger\n    reason: original reason\n"
			if err := os.WriteFile(policyPath, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			s := newWiringTestServer(t, &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: mode}})
			content := "version: 1\nblocked:\n  - pattern: danger\n    reason: " + wiringFakeSecret + "\n"
			for _, contentType := range []string{"application/json", "", "text/plain", "application/vnd.ntm+json"} {
				req := wiringJSONRequest(t, http.MethodPut, "/api/v1/policy/", PolicyUpdateRequest{Content: content})
				req.Header.Set("Content-Type", contentType)
				req.Header.Set("Idempotency-Key", "policy-redaction-integrity")
				rec := httptest.NewRecorder()
				s.Router().ServeHTTP(rec, req)
				assertWiringSecretRefusal(t, rec)
				got, err := os.ReadFile(policyPath)
				if err != nil || string(got) != original {
					t.Fatalf("rejected request with Content-Type %q changed the policy file: %q, %v", contentType, got, err)
				}
			}

			// A corrected request can reuse the key: refusal must not reserve a
			// successful replay entry or write a replacement credential.
			content = "version: 1\nblocked:\n  - pattern: danger\n    reason: keep this exact reason\n"
			req := wiringJSONRequest(t, http.MethodPut, "/api/v1/policy/", PolicyUpdateRequest{Content: content})
			req.Header.Set("Idempotency-Key", "policy-redaction-integrity")
			rec := httptest.NewRecorder()
			s.Router().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || !json.Valid(rec.Body.Bytes()) {
				t.Fatalf("ordinary policy update failed: %d %s", rec.Code, rec.Body)
			}
			got, err := os.ReadFile(policyPath)
			if err != nil || string(got) != content {
				t.Fatalf("ordinary policy was not saved exactly: %q, %v", got, err)
			}
		})
	}
}

// Approval actions, resources and reasons reach the database, and the handler
// logs the action. Refusal must happen before either side effect, without
// changing an existing approval or poisoning the corrected request's replay.
func TestRedactionRouterPreservesApprovalStoreAndLogs(t *testing.T) {
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		t.Run(string(mode), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			s, store := setupTestServer(t)
			s.redactionCfg = &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: mode}}
			var logs lockedBuffer
			previous := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(previous) })

			original := ApprovalRequestRequest{Action: "echo original", Resource: "original-resource", Reason: "original reason"}
			rec := httptest.NewRecorder()
			s.Router().ServeHTTP(rec, wiringJSONRequest(t, http.MethodPost, "/api/v1/approvals/request", original))
			if rec.Code != http.StatusOK {
				t.Fatalf("seed approval: %d %s", rec.Code, rec.Body)
			}
			before, err := store.ListApprovalHistory()
			if err != nil || len(before) != 1 {
				t.Fatalf("seed approval was not persisted: count=%d, err=%v", len(before), err)
			}

			for _, field := range []string{"action", "resource", "reason"} {
				body := map[string]string{"action": "echo proposed", "resource": "new-resource", "reason": "new reason"}
				body[field] += " " + wiringFakeSecret
				req := wiringJSONRequest(t, http.MethodPost, "/api/v1/approvals/request", body)
				req.Header.Set("Idempotency-Key", "approval-redaction-integrity")
				rec = httptest.NewRecorder()
				s.Router().ServeHTTP(rec, req)
				assertWiringSecretRefusal(t, rec)
				after, err := store.ListApprovalHistory()
				if err != nil || len(after) != 1 {
					t.Fatalf("rejected %s created an approval: count=%d, err=%v", field, len(after), err)
				}
				if after[0].ID != before[0].ID || after[0].Action != original.Action || after[0].Resource != original.Resource || after[0].Reason != original.Reason {
					t.Fatalf("rejected %s changed the original approval: %+v", field, after[0])
				}
			}

			corrected := ApprovalRequestRequest{Action: "echo corrected", Resource: "exact-resource", Reason: "  keep whitespace and Unicode: café  "}
			for attempt := 0; attempt < 2; attempt++ {
				req := wiringJSONRequest(t, http.MethodPost, "/api/v1/approvals/request", corrected)
				req.Header.Set("Idempotency-Key", "approval-redaction-integrity")
				rec = httptest.NewRecorder()
				s.Router().ServeHTTP(rec, req)
				if rec.Code != http.StatusOK || !json.Valid(rec.Body.Bytes()) {
					t.Fatalf("corrected approval/replay: %d %s", rec.Code, rec.Body)
				}
			}
			after, err := store.ListApprovalHistory()
			if err != nil || len(after) != 2 {
				t.Fatalf("corrected approval was not persisted exactly once: count=%d, err=%v", len(after), err)
			}
			for _, approval := range after {
				if approval.ID != before[0].ID && (approval.Action != corrected.Action || approval.Resource != corrected.Resource || approval.Reason != corrected.Reason) {
					t.Fatalf("ordinary approval values changed: %+v", approval)
				}
			}
			if strings.Contains(logs.String(), wiringFakeSecret) {
				t.Fatal("rejected credential reached request or approval logs")
			}
		})
	}
}

func TestRedactionRouterRefusesSecretsBeforeAgentDispatch(t *testing.T) {
	s := newWiringTestServer(t, &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeRedact}})
	var delivered []string
	s.sendAgents = func(opts robot.SendOptions) (*robot.SendOutput, error) {
		delivered = append(delivered, opts.Message)
		return &robot.SendOutput{RobotResponse: robot.RobotResponse{Success: true}, Session: opts.Session}, nil
	}
	path := "/api/v1/sessions/redaction/agents/send"
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, wiringJSONRequest(t, http.MethodPost, path, AgentSendRequest{Message: "Use " + wiringFakeSecret}))
	assertWiringSecretRefusal(t, rec)
	if len(delivered) != 0 {
		t.Fatal("credential or replacement placeholder reached agent dispatch")
	}

	message := "  Please review the code.\nKeep the original whitespace.  "
	rec = httptest.NewRecorder()
	s.Router().ServeHTTP(rec, wiringJSONRequest(t, http.MethodPost, path, AgentSendRequest{Message: message}))
	if rec.Code != http.StatusOK || !json.Valid(rec.Body.Bytes()) || len(delivered) != 1 || delivered[0] != message {
		t.Fatalf("ordinary prompt not dispatched exactly once and unchanged: status=%d, delivered=%q, body=%s", rec.Code, delivered, rec.Body)
	}
}

func wiringAttachAuditStore(t *testing.T, s *Server) (*AuditStore, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	audit, err := NewAuditStore(AuditStoreConfig{
		DBPath: filepath.Join(dir, "audit.db"), JSONLPath: path,
		Retention: time.Hour, CleanupInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = audit.Close() })
	s.auditStore = audit
	s.router = s.buildRouter()
	return audit, path
}

func wiringReadAuditRecord(t *testing.T, audit *AuditStore, path string) (AuditRecord, string, string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record AuditRecord
	if err := json.Unmarshal(bytes.TrimSpace(data), &record); err != nil {
		t.Fatalf("expected one valid JSONL audit record: %v", err)
	}
	var requestID, requestPath, userAgent string
	var status int
	if err := audit.db.QueryRow(`SELECT request_id, path, user_agent, status_code FROM audit_records`).Scan(
		&requestID, &requestPath, &userAgent, &status); err != nil {
		t.Fatal(err)
	}
	if requestID != record.RequestID || requestPath != record.Path || userAgent != record.UserAgent || status != record.StatusCode {
		t.Fatal("JSONL and SQLite audit observations disagree")
	}
	return record, string(data), requestID + "\n" + requestPath + "\n" + userAgent
}

// A refused body must not disclose credentials through the metadata writers
// surrounding admission. The actual handlers retain their original inputs;
// only observations and the server's correlation ID may be made safe.
func TestRedactionRouterProtectsRejectedRequestMetadata(t *testing.T) {
	requestSecret := "ghp_" + strings.Repeat("A", 36)
	pathSecret := "ghp_" + strings.Repeat("B", 36)
	querySecret := "ghp_" + strings.Repeat("C", 36)
	const customSecret = "NTM_META_CREDENTIAL_SYNTHETIC12345"
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		for _, kind := range []string{"approval", "agent path", "configured metadata", "truncated request id"} {
			t.Run(string(mode)+"/"+kind, func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				s, store := setupTestServer(t)
				cfg := redaction.Config{Mode: mode}
				id := requestSecret
				if kind == "configured metadata" {
					id = customSecret
					cfg.ExtraPatterns = map[redaction.Category][]string{"CUSTOM_META": {`NTM_META_CREDENTIAL_[A-Z0-9]+`}}
				} else if kind == "truncated request id" {
					id = strings.Repeat("t", 64) + requestSecret
				}
				s.redactionCfg = &RedactionConfig{Enabled: true, Config: cfg}
				audit, auditPath := wiringAttachAuditStore(t, s)
				delivered := 0
				s.sendAgents = func(opts robot.SendOptions) (*robot.SendOutput, error) {
					delivered++
					return &robot.SendOutput{RobotResponse: robot.RobotResponse{Success: true}}, nil
				}
				var logs lockedBuffer
				previous := log.Writer()
				log.SetOutput(&logs)
				t.Cleanup(func() { log.SetOutput(previous) })

				path := "/api/v1/approvals/request"
				resource := "approvals"
				var body any = ApprovalRequestRequest{Action: "echo " + wiringFakeSecret, Resource: "target"}
				if kind == "agent path" {
					path = "/api/v1/sessions/" + pathSecret + "/agents/send"
					resource = "sessions"
					body = AgentSendRequest{Message: "Use " + wiringFakeSecret}
				}
				path += "?api_token=" + url.QueryEscape(querySecret)
				req := wiringJSONRequest(t, http.MethodPost, path, body)
				req.Header.Set(requestIDHeader, id)
				req.Header.Set("User-Agent", "MetadataProbe/1.0 "+id)
				originalURI, originalAgent := req.URL.RequestURI(), req.UserAgent()
				rec := httptest.NewRecorder()
				s.Router().ServeHTTP(rec, req)
				if rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("credential-bearing body status=%d, want 422", rec.Code)
				}
				approvals, err := store.ListApprovalHistory()
				if err != nil || len(approvals) != 0 || delivered != 0 {
					t.Fatalf("refused request mutated state: approvals=%d deliveries=%d err=%v", len(approvals), delivered, err)
				}
				if req.URL.RequestURI() != originalURI || req.UserAgent() != originalAgent || req.Header.Get(requestIDHeader) != id {
					t.Fatal("observational protection changed original request metadata")
				}
				var response APIError
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.ErrorCode != "SECRETS_DETECTED" {
					t.Fatalf("refusal lost its nonsecret error diagnostic: %v", err)
				}
				record, jsonl, sqlite := wiringReadAuditRecord(t, audit, auditPath)
				if record.StatusCode != http.StatusUnprocessableEntity || record.Method != http.MethodPost || record.Resource != resource || record.Action != AuditActionCreate {
					t.Fatal("refusal lost useful audit status, method, resource or action")
				}
				responseID := rec.Header().Get(requestIDHeader)
				if responseID == "" || strings.Contains(responseID, "REDACTED") || response.RequestID != responseID || record.RequestID != responseID {
					t.Error("response and audit must share a nonempty opaque correlation ID")
				}
				if responseID == sanitizeRequestID(id) {
					t.Error("sensitive original correlation ID was trusted after sanitization or truncation")
				}
				for surface, output := range map[string]string{
					"response body": rec.Body.String(), "response headers": rec.Header().Get(requestIDHeader),
					"logs": logs.String(), "audit JSONL": jsonl, "audit SQLite": sqlite,
				} {
					for _, secret := range []string{requestSecret, pathSecret, querySecret, customSecret, wiringFakeSecret} {
						if strings.Contains(output, secret) {
							t.Errorf("refused request disclosed a synthetic credential through %s", surface)
						}
					}
				}
			})
		}
	}
}

func TestRedactionRouterProtectsMetadataBeforeAdmission(t *testing.T) {
	secret := "ghp_" + strings.Repeat("D", 36)
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		for _, failure := range []string{"authentication", "origin", "allowed credential origin"} {
			t.Run(string(mode)+"/"+failure, func(t *testing.T) {
				s := newWiringTestServer(t, &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: mode}})
				var logs lockedBuffer
				previous := log.Writer()
				log.SetOutput(&logs)
				t.Cleanup(func() { log.SetOutput(previous) })
				req := wiringJSONRequest(t, http.MethodPost, "/api/v1/sessions/"+secret+"/agents/send", AgentSendRequest{Message: "ordinary"})
				req.Header.Set(requestIDHeader, secret)
				wantStatus := http.StatusUnauthorized
				if failure == "authentication" {
					s.auth = AuthConfig{Mode: AuthModeAPIKey, APIKey: "server-test-credential"}
				} else {
					s.corsAllowedOrigins = []string{"https://allowed.example"}
					req.Header.Set("Origin", "https://refused.example")
					if failure == "allowed credential origin" {
						origin := "https://" + secret + ".example"
						s.corsAllowedOrigins = []string{origin}
						req.Header.Set("Origin", origin)
					}
					wantStatus = http.StatusForbidden
				}
				rec := httptest.NewRecorder()
				s.Router().ServeHTTP(rec, req)
				if rec.Code != wantStatus {
					t.Fatalf("pre-admission refusal status=%d, want %d", rec.Code, wantStatus)
				}
				var response APIError
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Error == "" || response.ErrorCode == "" {
					t.Fatalf("pre-admission refusal lost its diagnostic: %v", err)
				}
				if response.RequestID == "" || response.RequestID != rec.Header().Get(requestIDHeader) {
					t.Error("pre-admission response correlation is inconsistent")
				}
				for surface, output := range map[string]string{
					"response body": rec.Body.String(), "response header": rec.Header().Get(requestIDHeader),
					"reflected origin": rec.Header().Get("Access-Control-Allow-Origin"), "logs": logs.String(),
				} {
					if strings.Contains(output, secret) {
						t.Errorf("pre-admission refusal disclosed a synthetic credential through %s", surface)
					}
				}
			})
		}
	}
}

func TestRedactionRouterMetadataPolicyPreservesPermittedValues(t *testing.T) {
	secret := "ghp_" + strings.Repeat("E", 36)
	tests := []struct {
		name, id string
		enabled  bool
		cfg      redaction.Config
	}{
		{"ordinary redact", "trace.approved-123", true, redaction.Config{Mode: redaction.ModeRedact}},
		{"ordinary block", "trace.approved-123", true, redaction.Config{Mode: redaction.ModeBlock}},
		{"disabled", secret, false, redaction.Config{Mode: redaction.ModeRedact}},
		{"off", secret, true, redaction.Config{Mode: redaction.ModeOff}},
		{"warn", secret, true, redaction.Config{Mode: redaction.ModeWarn}},
		{"allowlisted redact", secret, true, redaction.Config{Mode: redaction.ModeRedact, Allowlist: []string{regexp.QuoteMeta(secret)}}},
		{"allowlisted block", secret, true, redaction.Config{Mode: redaction.ModeBlock, Allowlist: []string{regexp.QuoteMeta(secret)}}},
		{"category disabled redact", secret, true, redaction.Config{Mode: redaction.ModeRedact, DisabledCategories: []redaction.Category{redaction.CategoryGitHubToken}}},
		{"category disabled block", secret, true, redaction.Config{Mode: redaction.ModeBlock, DisabledCategories: []redaction.Category{redaction.CategoryGitHubToken}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			s, store := setupTestServer(t)
			s.redactionCfg = &RedactionConfig{Enabled: tt.enabled, Config: tt.cfg}
			audit, auditPath := wiringAttachAuditStore(t, s)
			body := ApprovalRequestRequest{Action: "echo original", Resource: "exact resource", Reason: "  café\nkeep whitespace  "}
			req := wiringJSONRequest(t, http.MethodPost, "/api/v1/approvals/request?trace="+url.QueryEscape(tt.id), body)
			req.Header.Set(requestIDHeader, tt.id)
			req.Header.Set("User-Agent", "MetadataProbe/1.0 "+tt.id)
			originalURI, originalAgent := req.URL.RequestURI(), req.UserAgent()
			rec := httptest.NewRecorder()
			s.Router().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("permitted metadata blocked ordinary storage: status=%d", rec.Code)
			}
			if req.URL.RequestURI() != originalURI || req.UserAgent() != originalAgent || req.Header.Get(requestIDHeader) != tt.id {
				t.Fatal("permitted request metadata changed")
			}
			record, _, _ := wiringReadAuditRecord(t, audit, auditPath)
			if rec.Header().Get(requestIDHeader) != tt.id || record.RequestID != tt.id || record.UserAgent != originalAgent || record.Path != req.URL.Path {
				t.Fatal("explicitly permitted metadata changed in response or audit")
			}
			approvals, err := store.ListApprovalHistory()
			if err != nil || len(approvals) != 1 || approvals[0].Action != body.Action || approvals[0].Resource != body.Resource || approvals[0].Reason != body.Reason {
				t.Fatalf("ordinary approval values were not stored exactly once: count=%d err=%v", len(approvals), err)
			}
		})
	}
}

// Path and query credentials have no JSON body for body admission to reject.
// These real handlers log their provider/query/path values before calling
// dependencies, and mutating approval requests can otherwise save their body.
func TestRedactionRouterRefusesSensitiveURLMetadata(t *testing.T) {
	secret := "ghp_" + strings.Repeat("F", 36)
	const generic = "abcdefghijklmnopqrstuv"
	const custom = "NTM_URL_CREDENTIAL_SYNTHETIC12345"
	nested := `{"api_\u0074oken":"` + generic + `"}`
	tests := []struct {
		name, method, path, source string
		body                       any
		custom                     bool
	}{
		{"accounts provider", http.MethodGet, "/api/v1/accounts/" + secret, "path", nil, false},
		{"encoded accounts provider", http.MethodGet, "/api/v1/accounts/%67" + secret[1:], "path", nil, false},
		{"mail search", http.MethodGet, "/api/v1/mail/search?q=" + url.QueryEscape(secret), "query", nil, false},
		{"first repeated query", http.MethodGet, "/api/v1/mail/search?q=" + secret + "&q=ordinary", "query", nil, false},
		{"last repeated query", http.MethodGet, "/api/v1/mail/search?q=ordinary&q=" + secret, "query", nil, false},
		{"generic query key", http.MethodGet, "/api/v1/mail/search?q=ordinary&api_%74oken=" + generic, "query", nil, false},
		{"encoded query key", http.MethodGet, "/api/v1/mail/search?q=ordinary&%67" + secret[1:] + "=ordinary", "query", nil, false},
		{"embedded JSON query", http.MethodGet, "/api/v1/mail/search?q=" + url.QueryEscape(nested), "query", nil, false},
		{"reservation path list", http.MethodGet, "/api/v1/reservations/conflicts?paths=ordinary&paths=" + secret, "query", nil, false},
		{"normal approval body", http.MethodPost, "/api/v1/approvals/request?api_token=" + generic, "query", ApprovalRequestRequest{Action: "echo ordinary", Resource: "target"}, false},
		{"configured query value", http.MethodGet, "/api/v1/mail/search?q=" + custom, "query", nil, true},
	}
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		for _, tt := range tests {
			t.Run(string(mode)+"/"+tt.name, func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				s, store := setupTestServer(t)
				cfg := redaction.Config{Mode: mode}
				if tt.custom {
					cfg.ExtraPatterns = map[redaction.Category][]string{"CUSTOM_URL": {`NTM_URL_CREDENTIAL_[A-Z0-9]+`}}
				}
				s.redactionCfg = &RedactionConfig{Enabled: true, Config: cfg}
				var logs lockedBuffer
				previousLog, previousSlog := log.Writer(), slog.Default()
				slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
				log.SetOutput(&logs)
				t.Cleanup(func() {
					slog.SetDefault(previousSlog)
					log.SetOutput(previousLog)
				})
				var req *http.Request
				if tt.body == nil {
					req = httptest.NewRequest(tt.method, tt.path, nil)
				} else {
					req = wiringJSONRequest(t, tt.method, tt.path, tt.body)
				}
				req.Header.Set(requestIDHeader, "url-policy-probe")
				originalURI := req.URL.RequestURI()
				rec := httptest.NewRecorder()
				s.Router().ServeHTTP(rec, req)
				if rec.Code != http.StatusUnprocessableEntity {
					t.Errorf("sensitive URL reached handler: status=%d, want 422", rec.Code)
				}
				var response APIError
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.ErrorCode != "SECRETS_DETECTED" || response.Details["source"] != tt.source {
					t.Errorf("URL refusal lacks static source and nonsecret diagnostic: parse error=%v", err)
				}
				if req.URL.RequestURI() != originalURI {
					t.Error("URL admission substituted a request value instead of refusing it")
				}
				approvals, err := store.ListApprovalHistory()
				if err != nil || len(approvals) != 0 {
					t.Errorf("URL refusal allowed approval storage: count=%d err=%v", len(approvals), err)
				}
				for _, credential := range []string{secret, generic, custom} {
					if strings.Contains(logs.String(), credential) || strings.Contains(rec.Body.String(), credential) {
						t.Error("sensitive URL disclosed credential contents through logs or response")
					}
				}
			})
		}
	}
}

func TestRedactionRouterRejectsMalformedQueryBeforeStorage(t *testing.T) {
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		for _, query := range []string{"q=%zz", "q=ordinary;api_token=abcdefghijklmnopqrstuv"} {
			t.Run(string(mode)+"/"+query[:4], func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				s, store := setupTestServer(t)
				s.redactionCfg = &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: mode}}
				req := wiringJSONRequest(t, http.MethodPost, "/api/v1/approvals/request?"+query,
					ApprovalRequestRequest{Action: "echo ordinary", Resource: "target"})
				rec := httptest.NewRecorder()
				s.Router().ServeHTTP(rec, req)
				var response APIError
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusBadRequest || response.ErrorCode != ErrCodeBadRequest {
					t.Fatalf("malformed query status=%d error_code=%s parse_error=%v", rec.Code, response.ErrorCode, err)
				}
				if strings.Contains(rec.Body.String(), query) || strings.Contains(rec.Body.String(), "abcdefghijklmnopqrstuv") {
					t.Fatal("malformed query appeared in parser diagnostics")
				}
				approvals, err := store.ListApprovalHistory()
				if err != nil || len(approvals) != 0 {
					t.Fatalf("malformed query reached approval storage: count=%d err=%v", len(approvals), err)
				}
			})
		}
	}
}

func TestRedactionRouterPreservesPermittedURLValues(t *testing.T) {
	secret := "ghp_" + strings.Repeat("G", 36)
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		for _, policy := range []string{"ordinary", "allowlisted", "category disabled"} {
			t.Run(string(mode)+"/"+policy, func(t *testing.T) {
				cfg := redaction.Config{Mode: mode}
				session := "ordinary-session"
				if policy == "allowlisted" {
					session = secret
					cfg.Allowlist = []string{regexp.QuoteMeta(secret)}
				} else if policy == "category disabled" {
					session = secret
					cfg.DisabledCategories = []redaction.Category{redaction.CategoryGitHubToken}
				}
				s := newWiringTestServer(t, &RedactionConfig{Enabled: true, Config: cfg})
				var delivered []robot.SendOptions
				s.sendAgents = func(opts robot.SendOptions) (*robot.SendOutput, error) {
					delivered = append(delivered, opts)
					return &robot.SendOutput{RobotResponse: robot.RobotResponse{Success: true}}, nil
				}
				message := "  preserve this prompt\nexactly  "
				req := wiringJSONRequest(t, http.MethodPost, "/api/v1/sessions/"+session+"/agents/send?label="+url.QueryEscape(session)+"&label=ordinary",
					AgentSendRequest{Message: message})
				originalURI := req.URL.RequestURI()
				rec := httptest.NewRecorder()
				s.Router().ServeHTTP(rec, req)
				if rec.Code != http.StatusOK || len(delivered) != 1 || delivered[0].Session != session || delivered[0].Message != message {
					t.Fatalf("permitted URL or body was not dispatched unchanged: status=%d deliveries=%d", rec.Code, len(delivered))
				}
				if req.URL.RequestURI() != originalURI {
					t.Fatal("permitted query order, encoding or duplicate values changed")
				}
			})
		}
	}
}

type wiringAttentionMarshalBarrier struct {
	entered chan struct{}
	release <-chan struct{}
}

func (b *wiringAttentionMarshalBarrier) MarshalJSON() ([]byte, error) {
	close(b.entered)
	<-b.release
	return []byte(`"ordinary diagnostic"`), nil
}

func TestWSRedactionAttentionFailureDuringReplayStopsSubscription(t *testing.T) {
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		t.Run(string(mode), func(t *testing.T) {
			hub := NewWSHub()
			defer hub.Stop()
			hub.setRedactionCfgForTest(&RedactionConfig{Enabled: true, Config: redaction.Config{
				Mode: mode, ExtraPatterns: map[redaction.Category][]string{"CUSTOM": {`"classified"\s*:\s*"[^"]*"`}},
			}})
			release := make(chan struct{})
			unblock := func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}
			defer unblock()
			barrier := &wiringAttentionMarshalBarrier{entered: make(chan struct{}), release: release}
			feed := &fakeAttentionStreamFeed{
				stats: robot.JournalStats{Count: 1, OldestCursor: 1, NewestCursor: 1, RetentionPeriod: time.Hour},
				replayEvents: []robot.AttentionEvent{{
					Cursor: 1, Type: "mail.pending", Details: map[string]any{"diagnostic": barrier},
				}},
				replayNewest: 1,
			}
			client := &WSClient{id: "attention-replay-race", hub: hub, send: make(chan []byte, 4), topics: make(map[string]struct{})}
			completed := make(chan map[string]any, 1)
			go func() {
				completed <- client.handleAttentionSubscribeWithFeed(feed, WSMessage{Data: map[string]any{"since_cursor": float64(0)}}, []string{"attention"})
			}()
			select {
			case <-barrier.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("replay never reached the serialization barrier")
			}

			// The feed can publish on another goroutine while subscribe is
			// replaying. A withheld live event must stop that entire subscription,
			// including the replay already being prepared for the wire.
			feed.emit(robot.AttentionEvent{Cursor: 2, Type: "mail.pending", Details: map[string]any{"classified": "private material"}})
			failure := queuedWSReplayFrame(t, client)
			if failure["type"] != "error" || failure["code"] != "REDACTION_FAILED" {
				t.Fatalf("live failure did not report resynchronization: %v", failure)
			}
			client.attentionSubMu.Lock()
			sub := client.attentionSub
			stopped := sub != nil && !sub.Active && sub.Cursor == 0
			client.attentionSubMu.Unlock()
			if !stopped {
				t.Fatal("live failure did not stop the subscription at its admitted cursor")
			}
			unblock()
			var result map[string]any
			select {
			case result = <-completed:
			case <-time.After(5 * time.Second):
				t.Fatal("subscription did not finish after releasing replay")
			}
			if result["error_code"] != "REDACTION_FAILED" || result["subscribed"] != nil {
				t.Fatalf("stopped subscription acknowledged success: %v", result)
			}
			if len(client.send) != 0 || sub.Cursor != 0 || sub.EventCount != 0 {
				t.Fatal("in-flight replay delivered a frame or advanced the cursor after redaction failure")
			}
			if client.attentionSub != nil || feed.subscriber != nil || len(client.Topics()) != 0 {
				t.Fatal("failed subscription retained its feed callback or topic membership")
			}
		})
	}
}

func TestWSRedactionAttentionReplacementDiscardsPreparedOldEvent(t *testing.T) {
	hub := NewWSHub()
	defer hub.Stop()
	hub.setRedactionCfgForTest(&RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeRedact}})
	release := make(chan struct{})
	unblock := func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}
	defer unblock()
	barrier := &wiringAttentionMarshalBarrier{entered: make(chan struct{}), release: release}
	old := &WSAttentionSub{Active: true, topics: []string{"attention:old"}}
	client := &WSClient{
		id: "attention-replacement", hub: hub, send: make(chan []byte, 4),
		topics: map[string]struct{}{"attention:old": {}}, attentionSub: old,
	}
	completed := make(chan struct{})
	go func() {
		client.deliverAttentionEvent(robot.AttentionEvent{
			Cursor: 41, Session: "old", Type: "mail.pending", Details: map[string]any{"diagnostic": barrier},
		}, old)
		close(completed)
	}()
	select {
	case <-barrier.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("old delivery never reached the serialization barrier")
	}

	feed := &fakeAttentionStreamFeed{}
	result := client.handleAttentionSubscribeWithFeed(feed, WSMessage{Data: map[string]any{"since_cursor": float64(-1)}}, []string{"attention:new"})
	if result["subscribed"] != true {
		t.Fatalf("replacement subscription failed: %v", result)
	}
	client.attentionSubMu.Lock()
	replacement := client.attentionSub
	client.attentionSubMu.Unlock()
	unblock()
	select {
	case <-completed:
	case <-time.After(5 * time.Second):
		t.Fatal("old delivery did not finish after releasing serialization")
	}
	if len(client.send) != 0 || replacement == old || replacement.Cursor != -1 || replacement.EventCount != 0 || !replacement.Active {
		t.Fatal("old subscription delivered a stale event or changed the replacement's cursor/state")
	}
	client.cancelAttentionSubscription()
}

package serve

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

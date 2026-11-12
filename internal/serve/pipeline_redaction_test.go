package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

func requirePipelineOuterJSONClean(t *testing.T, content string, cfg redaction.Config) {
	t.Helper()
	encoded, err := json.Marshal(PipelineValidateRequest{WorkflowContent: content})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Mode = redaction.ModeBlock
	result := redaction.ScanJSONAndRedactWithLimits(string(encoded), cfg, redaction.DefaultScanLimits())
	if result.Blocked || result.LimitExceeded || len(result.Findings) != 0 {
		t.Fatalf("fixture did not reach YAML decoding: blocked=%t limited=%t findings=%d",
			result.Blocked, result.LimitExceeded, len(result.Findings))
	}
}

func pipelineDecodedSecretYAML() string {
	return "schema_version: \"2.0\"\n" +
		"name: decoded-secret\n" +
		"steps:\n" +
		"  - id: one\n" +
		"    command: \"\\x73" + wiringFakeSecret[1:] + "\"\n"
}

func TestPipelineValidateRejectsCredentialRevealedByYAMLDecoding(t *testing.T) {
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		t.Run(string(mode), func(t *testing.T) {
			cfg := redaction.Config{Mode: mode}
			content := pipelineDecodedSecretYAML()
			requirePipelineOuterJSONClean(t, content, cfg)
			s := newWiringTestServer(t, &RedactionConfig{Enabled: true, Config: cfg})

			do := func(content string) *httptest.ResponseRecorder {
				req := wiringJSONRequest(t, http.MethodPost, "/api/v1/pipelines/validate",
					PipelineValidateRequest{WorkflowContent: content})
				req.Header.Set("Idempotency-Key", "decoded-workflow")
				rec := httptest.NewRecorder()
				s.Router().ServeHTTP(rec, req)
				return rec
			}

			rec := do(content)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("decoded credential status=%d, want 422: %s", rec.Code, rec.Body)
			}
			var refusal APIError
			if err := json.Unmarshal(rec.Body.Bytes(), &refusal); err != nil {
				t.Fatal(err)
			}
			if refusal.ErrorCode != "SECRETS_DETECTED" || refusal.Details["source"] != "workflow" ||
				strings.Contains(rec.Body.String(), wiringFakeSecret) {
				t.Fatalf("decoded workflow refusal lost safe diagnostics: %s", rec.Body)
			}
			if _, hasOffsets := refusal.Details["findings"]; hasOffsets {
				t.Fatal("decoded workflow refusal claimed byte offsets in the outer JSON request")
			}

			corrected := "schema_version: \"2.0\"\nname: corrected\nsteps:\n  - id: one\n    command: \"printf ready\"\n"
			for attempt := 0; attempt < 2; attempt++ {
				rec = do(corrected)
				if rec.Code != http.StatusOK ||
					(rec.Header().Get("X-Idempotent-Replay") == "true") != (attempt == 1) {
					t.Fatalf("corrected workflow attempt=%d status=%d replay=%q body=%s",
						attempt, rec.Code, rec.Header().Get("X-Idempotent-Replay"), rec.Body)
				}
			}
		})
	}
}

func TestPipelineValidateYAMLParseErrorDoesNotEchoDecodedSecret(t *testing.T) {
	content := "schema_version: \"2.0\"\nname: invalid\n\"\\x73" + wiringFakeSecret[1:] + "\": true\nsteps: []\n"
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		t.Run(string(mode), func(t *testing.T) {
			cfg := redaction.Config{Mode: mode}
			requirePipelineOuterJSONClean(t, content, cfg)
			s := newWiringTestServer(t, &RedactionConfig{Enabled: true, Config: cfg})
			rec := httptest.NewRecorder()
			s.Router().ServeHTTP(rec, wiringJSONRequest(t, http.MethodPost, "/api/v1/pipelines/validate",
				PipelineValidateRequest{WorkflowContent: content}))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("parse failure status=%d, want 400: %s", rec.Code, rec.Body)
			}
			var response APIError
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			parseError, _ := response.Details["parse_error"].(string)
			if response.ErrorCode != ErrCodeInvalidWorkflow ||
				parseError != "workflow YAML could not be parsed; check syntax and supported fields" ||
				strings.Contains(rec.Body.String(), wiringFakeSecret) ||
				strings.Contains(rec.Body.String(), wiringFakeSecret[:7]) {
				t.Fatalf("unsafe workflow parse diagnostic: %s", rec.Body)
			}
		})
	}
}

func TestPipelineValidateDecodedWorkflowPermittedByExplicitPolicy(t *testing.T) {
	content := pipelineDecodedSecretYAML()
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		t.Run(string(mode), func(t *testing.T) {
			cfg := redaction.Config{
				Mode:      mode,
				Allowlist: []string{"^" + regexp.QuoteMeta(wiringFakeSecret) + "$"},
			}
			requirePipelineOuterJSONClean(t, content, cfg)
			s := newWiringTestServer(t, &RedactionConfig{Enabled: true, Config: cfg})
			rec := httptest.NewRecorder()
			s.Router().ServeHTTP(rec, wiringJSONRequest(t, http.MethodPost, "/api/v1/pipelines/validate",
				PipelineValidateRequest{WorkflowContent: content}))
			if rec.Code != http.StatusOK {
				t.Fatalf("explicitly permitted workflow was refused: status=%d body=%s", rec.Code, rec.Body)
			}
			var envelope map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope["success"] != true {
				t.Fatalf("permitted validation lost success envelope: %s", rec.Body)
			}
		})
	}
}

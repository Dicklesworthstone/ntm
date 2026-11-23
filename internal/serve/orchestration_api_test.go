package serve

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrchestrationAndSessionLifecycleOpenAPIReflection(t *testing.T) {
	spec, err := GenerateOpenAPISpecFromRouter("dev", DefaultOpenAPIServerURL)
	if err != nil {
		t.Fatalf("GenerateOpenAPISpecFromRouter failed: %v", err)
	}

	wantOps := []struct {
		path   string
		method string
	}{
		{path: "/api/v1/sessions/{id}", method: http.MethodDelete},
		{path: "/api/v1/sessions/{id}/health", method: http.MethodGet},
		{path: "/api/v1/sessions/{id}/interrupt", method: http.MethodPost},
		{path: "/api/v1/sessions/{id}/controller", method: http.MethodPost},
		{path: "/api/v1/doctor", method: http.MethodGet},
		{path: "/api/v1/prompt/preflight", method: http.MethodPost},
		{path: "/api/v1/assign", method: http.MethodPost},
		{path: "/api/v1/route", method: http.MethodPost},
		{path: "/api/v1/quota", method: http.MethodGet},
	}

	for _, tc := range wantOps {
		item, ok := spec.Paths[tc.path]
		if !ok {
			t.Fatalf("OpenAPI spec missing path %s", tc.path)
		}
		var op *Operation
		switch tc.method {
		case http.MethodGet:
			op = item.Get
		case http.MethodPost:
			op = item.Post
		case http.MethodDelete:
			op = item.Delete
		}
		if op == nil {
			t.Fatalf("OpenAPI path %s missing %s operation", tc.path, tc.method)
		}
		okResp, has200 := op.Responses["200"]
		if !has200 {
			t.Fatalf("%s %s missing 200 response", tc.method, tc.path)
		}
		mt, hasJSON := okResp.Content["application/json"]
		if !hasJSON || mt.Schema == nil || mt.Schema.Ref == "" {
			t.Fatalf("%s %s missing typed $ref response schema: %+v", tc.method, tc.path, okResp)
		}
	}

	for _, wantSchema := range []string{
		"DeleteSessionEnvelopeResponse",
		"SessionHealthEnvelopeResponse",
		"SessionInterruptEnvelopeResponse",
		"SessionControllerEnvelopeResponse",
		"ServerDoctorEnvelopeResponse",
		"PromptPreflightEnvelopeResponse",
		"AssignPlanEnvelopeResponse",
		"RoutePlanEnvelopeResponse",
		"QuotaStatusEnvelopeResponse",
	} {
		if _, exists := spec.Components.Schemas[wantSchema]; !exists {
			t.Errorf("OpenAPI components.schemas missing %s", wantSchema)
		}
	}
}

func TestPromptPreflightRESTEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)
	router := srv.Router()

	t.Run("clean_prompt", func(t *testing.T) {
		body := `{"prompt":"Implement typed OpenAPI reflection and run go test ./internal/serve","strict":false}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/prompt/preflight", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp PromptPreflightEnvelopeResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if !resp.Success || !resp.Allowed {
			t.Fatalf("expected clean prompt allowed=true, got %+v", resp)
		}
		if resp.PreviewHash == "" || resp.EstimatedTokens <= 0 || resp.Findings == nil {
			t.Fatalf("unexpected preflight metadata: %+v", resp)
		}
	})

	t.Run("oversized_prompt_returns_200_with_allowed_false", func(t *testing.T) {
		hugePrompt := strings.Repeat("A", 120000)
		payload, _ := json.Marshal(PromptPreflightRequest{Prompt: hugePrompt, Strict: true})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/prompt/preflight", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp PromptPreflightEnvelopeResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if !resp.Success {
			t.Fatalf("transport success must stay true for inline UI rendering: %+v", resp)
		}
		if resp.Allowed || resp.ErrorCount == 0 || len(resp.Findings) == 0 {
			t.Fatalf("expected oversized prompt under strict mode to report allowed=false and blocking findings: %+v", resp)
		}
	})

	t.Run("empty_and_malformed_rejected_400", func(t *testing.T) {
		for _, raw := range []string{`{"prompt":"   "}`, `{not-json`} {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/prompt/preflight", strings.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("payload %q: status = %d, want 400", raw, rec.Code)
			}
		}
	})
}

func TestAssignRouteQuotaAndUnifiedDoctorRESTEndpoints(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)
	router := srv.Router()

	t.Run("assign_validation_and_missing_session", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/assign", strings.NewReader(`{"session":""}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("empty session assign status = %d, want 400", rec.Code)
		}

		reqBadStrategy := httptest.NewRequest(http.MethodPost, "/api/v1/assign", strings.NewReader(`{"session":"nonexistent_wave12_sess","strategy":"bogus"}`))
		reqBadStrategy.Header.Set("Content-Type", "application/json")
		recBadStrategy := httptest.NewRecorder()
		router.ServeHTTP(recBadStrategy, reqBadStrategy)
		if recBadStrategy.Code != http.StatusBadRequest {
			t.Fatalf("invalid strategy assign status = %d, want 400; body=%s", recBadStrategy.Code, recBadStrategy.Body.String())
		}
	})

	t.Run("route_validation_and_missing_session", func(t *testing.T) {
		reqBadStrategy := httptest.NewRequest(http.MethodPost, "/api/v1/route", strings.NewReader(`{"session":"nonexistent_wave12_sess","strategy":"bogus"}`))
		reqBadStrategy.Header.Set("Content-Type", "application/json")
		recBadStrategy := httptest.NewRecorder()
		router.ServeHTTP(recBadStrategy, reqBadStrategy)
		if recBadStrategy.Code != http.StatusBadRequest {
			t.Fatalf("invalid strategy route status = %d, want 400; body=%s", recBadStrategy.Code, recBadStrategy.Body.String())
		}

		reqAffinityNoPrompt := httptest.NewRequest(http.MethodPost, "/api/v1/route", strings.NewReader(`{"session":"nonexistent_wave12_sess","strategy":"affinity"}`))
		reqAffinityNoPrompt.Header.Set("Content-Type", "application/json")
		recAffinityNoPrompt := httptest.NewRecorder()
		router.ServeHTTP(recAffinityNoPrompt, reqAffinityNoPrompt)
		if recAffinityNoPrompt.Code != http.StatusBadRequest {
			t.Fatalf("affinity without prompt route status = %d, want 400; body=%s", recAffinityNoPrompt.Code, recAffinityNoPrompt.Body.String())
		}

		reqMissing := httptest.NewRequest(http.MethodPost, "/api/v1/route", strings.NewReader(`{"session":"nonexistent_wave12_sess","strategy":"least-loaded"}`))
		reqMissing.Header.Set("Content-Type", "application/json")
		recMissing := httptest.NewRecorder()
		router.ServeHTTP(recMissing, reqMissing)
		if recMissing.Code != http.StatusNotFound {
			t.Fatalf("missing session route status = %d, want 404; body=%s", recMissing.Code, recMissing.Body.String())
		}
	})

	t.Run("quota_status_and_provider_detail", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/quota", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("quota status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp QuotaStatusEnvelopeResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode quota response: %v", err)
		}
		if !resp.Success || resp.Quota.Providers == nil {
			t.Fatalf("unexpected quota payload: %+v", resp)
		}

		reqUnknown := httptest.NewRequest(http.MethodGet, "/api/v1/quota?provider=nonexistent_provider_xyz", nil)
		recUnknown := httptest.NewRecorder()
		router.ServeHTTP(recUnknown, reqUnknown)
		if recUnknown.Code != http.StatusNotFound {
			t.Fatalf("unknown provider quota status = %d, want 404; body=%s", recUnknown.Code, recUnknown.Body.String())
		}
	})

	t.Run("unified_ori_doctor_endpoint", func(t *testing.T) {
		reqBadQuery := httptest.NewRequest(http.MethodGet, "/api/v1/doctor?contention=notabool", nil)
		recBadQuery := httptest.NewRecorder()
		router.ServeHTTP(recBadQuery, reqBadQuery)
		if recBadQuery.Code != http.StatusBadRequest {
			t.Fatalf("invalid contention query status = %d, want 400", recBadQuery.Code)
		}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/doctor?verbose=true", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("doctor status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp ServerDoctorEnvelopeResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode doctor response: %v", err)
		}
		if !resp.Success || resp.Overall == "" || len(resp.Tools) == 0 || len(resp.Invariants) == 0 || resp.SafetyDefaults == nil {
			t.Fatalf("expected unified ORI doctor fields (tools, invariants, safety_defaults): %+v", resp)
		}
	})
}

func TestSessionControllerPathTraversalAndQueryValidation(t *testing.T) {
	projDir := t.TempDir()
	insidePrompt := filepath.Join(projDir, "prompts", "controller.txt")
	if err := os.MkdirAll(filepath.Dir(insidePrompt), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(insidePrompt, []byte("Coordinate agents."), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := resolveBoundedProjectFile("", "prompts/controller.txt"); err == nil {
		t.Fatal("expected unscoped projectDir to reject prompt_file")
	}
	if _, err := resolveBoundedProjectFile(projDir, "../../etc/passwd"); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
	if _, err := resolveBoundedProjectFile(projDir, "/etc/passwd"); err == nil {
		t.Fatal("expected absolute path outside projectDir to be rejected")
	}
	wantInsidePrompt := insidePrompt
	if resolved, err := filepath.EvalSymlinks(insidePrompt); err == nil {
		wantInsidePrompt = resolved
	}
	got, err := resolveBoundedProjectFile(projDir, "prompts/controller.txt")
	if err != nil || got != wantInsidePrompt {
		t.Fatalf("resolveBoundedProjectFile valid relative path = (%q, %v), want (%q, nil)", got, err, wantInsidePrompt)
	}
	symlinkBase := filepath.Join(t.TempDir(), "proj-link")
	if err := os.Symlink(projDir, symlinkBase); err == nil {
		absViaLink := filepath.Join(symlinkBase, "prompts", "controller.txt")
		gotViaLink, err := resolveBoundedProjectFile(symlinkBase, absViaLink)
		if err != nil || gotViaLink != wantInsidePrompt {
			t.Fatalf("resolveBoundedProjectFile absolute path under symlinked projectDir = (%q, %v), want (%q, nil)", gotViaLink, err, wantInsidePrompt)
		}
	}
	outsideSecret := filepath.Join(t.TempDir(), "outside-secret.txt")
	if err := os.WriteFile(outsideSecret, []byte("top-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	escapeLink := filepath.Join(projDir, "prompts", "escape-link.txt")
	if err := os.Symlink(outsideSecret, escapeLink); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveBoundedProjectFile(projDir, "prompts/escape-link.txt"); err == nil {
		t.Fatal("expected symlink escaping projectDir to be rejected")
	}

	srv, _ := setupTestServer(t)
	router := srv.Router()

	// Unscoped server rejects prompt_file over HTTP before invoking kernel
	reqTraversal := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/demo/controller", strings.NewReader(`{"agent_type":"cc","prompt_file":"../secret.txt"}`))
	reqTraversal.Header.Set("Content-Type", "application/json")
	recTraversal := httptest.NewRecorder()
	router.ServeHTTP(recTraversal, reqTraversal)
	if recTraversal.Code != http.StatusBadRequest {
		t.Fatalf("controller traversal status = %d, want 400; body=%s", recTraversal.Code, recTraversal.Body.String())
	}

	// Negative pane filter on /health rejected 400
	reqNegPane := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/demo/health?pane=-1", nil)
	recNegPane := httptest.NewRecorder()
	router.ServeHTTP(recNegPane, reqNegPane)
	if recNegPane.Code != http.StatusBadRequest {
		t.Fatalf("negative pane health status = %d, want 400", recNegPane.Code)
	}

	// Invalid boolean query on DELETE /sessions/{id} rejected 400
	reqBadForce := httptest.NewRequest(http.MethodDelete, "/api/v1/sessions/demo?force=maybe", nil)
	recBadForce := httptest.NewRecorder()
	router.ServeHTTP(recBadForce, reqBadForce)
	if recBadForce.Code != http.StatusBadRequest {
		t.Fatalf("invalid force delete status = %d, want 400", recBadForce.Code)
	}
}

package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/hooks"
)

func TestSessionIntelOpenAPISchemaContracts(t *testing.T) {
	spec, err := GenerateOpenAPISpecFromRouter("1.39.0-wave14", "http://127.0.0.1:7337")
	if err != nil {
		t.Fatalf("GenerateOpenAPISpecFromRouter: %v", err)
	}

	checks := []struct {
		method  string
		path    string
		wantRef string
		wantReq string
	}{
		{http.MethodPost, "/api/v1/sessions/{id}/adopt", "#/components/schemas/SessionAdoptEnvelopeResponse", "#/components/schemas/SessionAdoptRequest"},
		{http.MethodGet, "/api/v1/sessions/{id}/context", "#/components/schemas/SessionContextEnvelopeResponse", ""},
		{http.MethodGet, "/api/v1/sessions/{id}/diff", "#/components/schemas/SessionDiffEnvelopeResponse", ""},
		{http.MethodGet, "/api/v1/sessions/{id}/files", "#/components/schemas/SessionFilesEnvelopeResponse", ""},
		{http.MethodGet, "/api/v1/personas", "#/components/schemas/PersonasEnvelopeResponse", ""},
		{http.MethodGet, "/api/v1/recipes", "#/components/schemas/RecipesEnvelopeResponse", ""},
		{http.MethodGet, "/api/v1/hooks", "#/components/schemas/HooksEnvelopeResponse", ""},
		{http.MethodPost, "/api/v1/hooks/run", "#/components/schemas/HooksEnvelopeResponse", "#/components/schemas/HooksRunRequest"},
		{http.MethodPost, "/api/v1/redact/preview", "#/components/schemas/RedactPreviewEnvelopeResponse", "#/components/schemas/RedactPreviewRequest"},
	}

	for _, tc := range checks {
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
		}
		if op == nil {
			t.Fatalf("OpenAPI spec missing %s operation on %s", tc.method, tc.path)
		}
		resp200, ok := op.Responses["200"]
		if !ok {
			t.Fatalf("%s %s missing 200 response", tc.method, tc.path)
		}
		gotRef := resp200.Content["application/json"].Schema.Ref
		if strings.Compare(gotRef, tc.wantRef) != 0 {
			t.Fatalf("%s %s 200 schema ref = %q, want %q", tc.method, tc.path, gotRef, tc.wantRef)
		}
		if strings.Compare(tc.wantReq, "") != 0 {
			if op.RequestBody == nil {
				t.Fatalf("%s %s missing requestBody", tc.method, tc.path)
			}
			gotReq := op.RequestBody.Content["application/json"].Schema.Ref
			if strings.Compare(gotReq, tc.wantReq) != 0 {
				t.Fatalf("%s %s requestBody schema ref = %q, want %q", tc.method, tc.path, gotReq, tc.wantReq)
			}
		}
	}
}

func TestSessionIntelRESTEndpointsEndToEnd(t *testing.T) {
	repoDir := t.TempDir()
	gitInit := exec.Command("git", "init", repoDir)
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, string(out))
	}
	gitCfg := exec.Command("git", "-C", repoDir, "config", "core.hooksPath", ".git/hooks")
	if out, err := gitCfg.CombinedOutput(); err != nil {
		t.Fatalf("git config core.hooksPath: %v (%s)", err, string(out))
	}

	hookMgr, err := hooks.NewManager(repoDir)
	if err != nil {
		t.Fatalf("hooks.NewManager: %v", err)
	}
	if err := hookMgr.Install(hooks.HookPreCommit, true); err != nil {
		t.Fatalf("Install(pre-commit): %v", err)
	}

	if err := os.MkdirAll(filepath.Join(repoDir, ".ntm"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.ntm): %v", err)
	}
	personasToml := `
[[personas]]
name = "wave14-rest-grok"
agent_type = "xai-grok-build"
description = "Grok Build REST persona"
tags = ["wave14-rest"]
`
	if err := os.WriteFile(filepath.Join(repoDir, ".ntm", "personas.toml"), []byte(personasToml), 0o644); err != nil {
		t.Fatalf("WriteFile(personas.toml): %v", err)
	}

	srv := NewHermeticServer("1.39.0-wave14")
	t.Cleanup(func() { srv.Stop() })
	router := srv.Router()

	doReq := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if strings.Compare(body, "") != 0 {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	statusOf := func(rec *httptest.ResponseRecorder) int {
		return rec.Result().StatusCode
	}

	// 1. POST /api/v1/redact/preview: redacts secret without leaking synthetic credential; rejects empty body.
	syntheticCred := "sk" + "-live-" + strings.Repeat("c3", 16)
	redactRec := doReq(http.MethodPost, "/api/v1/redact/preview", `{"text":"Authorization: Bearer `+syntheticCred+`"}`)
	if statusOf(redactRec) != http.StatusOK {
		t.Fatalf("POST /api/v1/redact/preview status = %d, body = %s", statusOf(redactRec), redactRec.Body.String())
	}
	if strings.Contains(redactRec.Body.String(), syntheticCred) {
		t.Fatalf("redact preview leaked synthetic credential: %s", redactRec.Body.String())
	}
	var redactResp RedactPreviewEnvelopeResponse
	if err := json.Unmarshal(redactRec.Body.Bytes(), &redactResp); err != nil {
		t.Fatalf("decode redact response: %v", err)
	}
	if !redactResp.Success || redactResp.FindingCount == 0 || len(redactResp.Findings) == 0 {
		t.Fatalf("expected >=1 redact finding, got %+v", redactResp)
	}
	if emptyRedact := doReq(http.MethodPost, "/api/v1/redact/preview", `{}`); statusOf(emptyRedact) != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty redact preview body, got %d", statusOf(emptyRedact))
	}

	// 2. GET /api/v1/personas & GET /api/v1/recipes
	personasRec := doReq(http.MethodGet, "/api/v1/personas?project_dir="+repoDir+"&agent=grok&tag=wave14-rest", "")
	if statusOf(personasRec) != http.StatusOK {
		t.Fatalf("GET /api/v1/personas status = %d, body = %s", statusOf(personasRec), personasRec.Body.String())
	}
	var personasResp PersonasEnvelopeResponse
	if err := json.Unmarshal(personasRec.Body.Bytes(), &personasResp); err != nil {
		t.Fatalf("decode personas response: %v", err)
	}
	if !personasResp.Success || personasResp.Count != 1 || strings.Compare(personasResp.Personas[0].Name, "wave14-rest-grok") != 0 {
		t.Fatalf("unexpected personas response: %+v", personasResp)
	}

	recipesRec := doReq(http.MethodGet, "/api/v1/recipes", "")
	if statusOf(recipesRec) != http.StatusOK {
		t.Fatalf("GET /api/v1/recipes status = %d, body = %s", statusOf(recipesRec), recipesRec.Body.String())
	}
	var recipesResp RecipesEnvelopeResponse
	if err := json.Unmarshal(recipesRec.Body.Bytes(), &recipesResp); err != nil {
		t.Fatalf("decode recipes response: %v", err)
	}
	if !recipesResp.Success || recipesResp.Recipes == nil {
		t.Fatalf("unexpected recipes response: %+v", recipesResp)
	}

	// 3. GET /api/v1/hooks & POST /api/v1/hooks/run
	hooksStatusRec := doReq(http.MethodGet, "/api/v1/hooks?project_dir="+repoDir, "")
	if statusOf(hooksStatusRec) != http.StatusOK {
		t.Fatalf("GET /api/v1/hooks status = %d, body = %s", statusOf(hooksStatusRec), hooksStatusRec.Body.String())
	}
	var hooksStatusResp HooksEnvelopeResponse
	if err := json.Unmarshal(hooksStatusRec.Body.Bytes(), &hooksStatusResp); err != nil {
		t.Fatalf("decode hooks status response: %v", err)
	}
	if !hooksStatusResp.Success || hooksStatusResp.InstalledCount == nil || *hooksStatusResp.InstalledCount != 1 || len(hooksStatusResp.Hooks) != 5 {
		t.Fatalf("unexpected hooks status response: %+v", hooksStatusResp)
	}

	hooksRunRec := doReq(http.MethodPost, "/api/v1/hooks/run", `{"hook_type":"post-commit","project_dir":"`+repoDir+`"}`)
	if statusOf(hooksRunRec) != http.StatusOK {
		t.Fatalf("POST /api/v1/hooks/run status = %d, body = %s", statusOf(hooksRunRec), hooksRunRec.Body.String())
	}
	var hooksRunResp HooksEnvelopeResponse
	if err := json.Unmarshal(hooksRunRec.Body.Bytes(), &hooksRunResp); err != nil {
		t.Fatalf("decode hooks run response: %v", err)
	}
	if hooksRunResp.Passed == nil || !*hooksRunResp.Passed {
		t.Fatalf("expected post-commit hook check to pass, got %+v", hooksRunResp)
	}

	badHookRec := doReq(http.MethodPost, "/api/v1/hooks/run", `{"hook_type":"bogus-hook","project_dir":"`+repoDir+`"}`)
	if statusOf(badHookRec) != http.StatusBadRequest {
		t.Fatalf("expected 400 for bogus hook_type, got %d (%s)", statusOf(badHookRec), badHookRec.Body.String())
	}

	// 4. Session files, context, diff, and adopt validation
	filesRec := doReq(http.MethodGet, "/api/v1/sessions/demo_wave14/files?window=15m&limit=25", "")
	if statusOf(filesRec) != http.StatusOK {
		t.Fatalf("GET /api/v1/sessions/demo_wave14/files status = %d, body = %s", statusOf(filesRec), filesRec.Body.String())
	}
	var filesResp SessionFilesEnvelopeResponse
	if err := json.Unmarshal(filesRec.Body.Bytes(), &filesResp); err != nil {
		t.Fatalf("decode files response: %v", err)
	}
	if !filesResp.Success || filesResp.Changes == nil || !strings.EqualFold(filesResp.TimeWindow, "15m") {
		t.Fatalf("unexpected files response: %+v", filesResp)
	}
	if badWin := doReq(http.MethodGet, "/api/v1/sessions/demo_wave14/files?window=invalid_win", ""); statusOf(badWin) != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid files window, got %d", statusOf(badWin))
	}

	if ctxRec := doReq(http.MethodGet, "/api/v1/sessions/nonexistent_wave14_999/context", ""); statusOf(ctxRec) != http.StatusNotFound {
		t.Fatalf("expected 404 for nonexistent session context, got %d (%s)", statusOf(ctxRec), ctxRec.Body.String())
	}
	if badLines := doReq(http.MethodGet, "/api/v1/sessions/demo_wave14/context?lines=0", ""); statusOf(badLines) != http.StatusBadRequest {
		t.Fatalf("expected 400 for lines=0, got %d", statusOf(badLines))
	}

	if diffRec := doReq(http.MethodGet, "/api/v1/sessions/nonexistent_wave14_999/diff", ""); statusOf(diffRec) != http.StatusNotFound {
		t.Fatalf("expected 404 for nonexistent session diff, got %d (%s)", statusOf(diffRec), diffRec.Body.String())
	}
	if badSince := doReq(http.MethodGet, "/api/v1/sessions/demo_wave14/diff?since=not_a_duration", ""); statusOf(badSince) != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid diff since, got %d", statusOf(badSince))
	}

	if emptyAdopt := doReq(http.MethodPost, "/api/v1/sessions/demo_wave14/adopt", `{}`); statusOf(emptyAdopt) != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty adopt body, got %d", statusOf(emptyAdopt))
	}
}

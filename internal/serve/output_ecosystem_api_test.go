package serve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestOutputEcosystemOpenAPISchemaContracts(t *testing.T) {
	spec, err := GenerateOpenAPISpecFromRouter("1.39.0-wave16", "http://127.0.0.1:7337")
	if err != nil {
		t.Fatalf("GenerateOpenAPISpecFromRouter: %v", err)
	}

	checks := []struct {
		method  string
		path    string
		wantRef string
		wantReq string
	}{
		{http.MethodGet, "/api/v1/sessions/{id}/grep", "#/components/schemas/SessionGrepEnvelopeResponse", ""},
		{http.MethodGet, "/api/v1/grep", "#/components/schemas/SessionGrepEnvelopeResponse", ""},
		{http.MethodGet, "/api/v1/sessions/{id}/extract", "#/components/schemas/SessionExtractEnvelopeResponse", ""},
		{http.MethodGet, "/api/v1/sessions/{id}/errors", "#/components/schemas/SessionErrorsEnvelopeResponse", ""},
		{http.MethodGet, "/api/v1/config/validate", "#/components/schemas/ConfigValidateEnvelopeResponse", ""},
		{http.MethodPost, "/api/v1/config/validate", "#/components/schemas/ConfigValidateEnvelopeResponse", "#/components/schemas/ConfigValidateRequest"},
		{http.MethodGet, "/api/v1/plugins", "#/components/schemas/PluginsEnvelopeResponse", ""},
		{http.MethodGet, "/api/v1/models", "#/components/schemas/LocalModelsEnvelopeResponse", ""},
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
		if gotRef != tc.wantRef {
			t.Fatalf("%s %s 200 schema ref = %q, want %q", tc.method, tc.path, gotRef, tc.wantRef)
		}
		if tc.wantReq != "" {
			if op.RequestBody == nil {
				t.Fatalf("%s %s missing requestBody", tc.method, tc.path)
			}
			gotReq := op.RequestBody.Content["application/json"].Schema.Ref
			if gotReq != tc.wantReq {
				t.Fatalf("%s %s requestBody schema ref = %q, want %q", tc.method, tc.path, gotReq, tc.wantReq)
			}
		}
	}
}

func TestOutputEcosystemRESTEndpointsEndToEnd(t *testing.T) {
	homeDir := t.TempDir()
	cfgRoot := filepath.Join(homeDir, ".config", "ntm")
	if err := os.MkdirAll(filepath.Join(cfgRoot, "agents"), 0o755); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cfgRoot, "commands"), 0o755); err != nil {
		t.Fatalf("mkdir commands: %v", err)
	}

	missingProjectsDir := filepath.Join(homeDir, "auto-projects")
	mainCfgPath := filepath.Join(cfgRoot, "config.toml")
	if err := os.WriteFile(mainCfgPath, []byte("projects_base = \""+missingProjectsDir+"\"\n"), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	agentTOML := "[agent]\nname = \"rest_plugin\"\nalias = \"rp\"\ncommand = \"echo\"\ndescription = \"REST plugin test\"\n[agent.env]\nSECRET_KEY = \"never-leak-this\"\n"
	if err := os.WriteFile(filepath.Join(cfgRoot, "agents", "rest.toml"), []byte(agentTOML), 0o644); err != nil {
		t.Fatalf("write agent plugin: %v", err)
	}

	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".ntm"), 0o755); err != nil {
		t.Fatalf("mkdir .ntm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".ntm", "policy.yaml"), []byte("version: 1\nblocked:\n  - pattern: \"rm -rf /\"\n    reason: \"forbidden\"\n"), 0o644); err != nil {
		t.Fatalf("write policy.yaml: %v", err)
	}

	t.Setenv("HOME", homeDir)
	t.Setenv("NTM_CONFIG", mainCfgPath)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(homeDir, ".config"))
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("NTM_LOCAL_VRAM_GB", "24")

	srv := NewHermeticServer("1.39.0-wave16")
	t.Cleanup(func() { srv.Stop() })
	router := srv.Router()

	doReq := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	// 1. GET & POST /api/v1/config/validate (including --fix directory auto-creation + invalid TOML reporting)
	valGetRec := doReq(http.MethodGet, "/api/v1/config/validate?all=true&project_dir="+url.QueryEscape(repoDir), "")
	if valGetRec.Result().StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/config/validate status=%d body=%s", valGetRec.Result().StatusCode, valGetRec.Body.String())
	}
	var valGetResp ConfigValidateEnvelopeResponse
	if err := json.Unmarshal(valGetRec.Body.Bytes(), &valGetResp); err != nil || !valGetResp.Success || !valGetResp.Valid {
		t.Fatalf("unexpected GET /api/v1/config/validate: %+v (%v)", valGetResp, err)
	}

	valPostBody, _ := json.Marshal(ConfigValidateRequest{All: true, Fix: true, ProjectDir: repoDir})
	valPostRec := doReq(http.MethodPost, "/api/v1/config/validate", string(valPostBody))
	if valPostRec.Result().StatusCode != http.StatusOK {
		t.Fatalf("POST /api/v1/config/validate status=%d body=%s", valPostRec.Result().StatusCode, valPostRec.Body.String())
	}
	if _, err := os.Stat(missingProjectsDir); err != nil {
		t.Fatalf("expected POST /api/v1/config/validate with fix=true to create %s: %v", missingProjectsDir, err)
	}

	// Planted invalid recipes.toml returns HTTP 200 with valid=false and structured results[].errors
	if err := os.WriteFile(filepath.Join(repoDir, ".ntm", "recipes.toml"), []byte("[broken\n"), 0o644); err != nil {
		t.Fatalf("write broken recipes.toml: %v", err)
	}
	valInvalidRec := doReq(http.MethodGet, "/api/v1/config/validate?project_dir="+url.QueryEscape(repoDir), "")
	if valInvalidRec.Result().StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/config/validate (invalid) status=%d body=%s", valInvalidRec.Result().StatusCode, valInvalidRec.Body.String())
	}
	var valInvalidResp ConfigValidateEnvelopeResponse
	if err := json.Unmarshal(valInvalidRec.Body.Bytes(), &valInvalidResp); err != nil || valInvalidResp.Valid || valInvalidResp.Summary.ErrorCount == 0 {
		t.Fatalf("expected valid=false with error_count > 0, got %+v (%v)", valInvalidResp, err)
	}

	// 2. GET /api/v1/plugins (verifying secret env values are never serialized)
	plugRec := doReq(http.MethodGet, "/api/v1/plugins", "")
	if plugRec.Result().StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/plugins status=%d body=%s", plugRec.Result().StatusCode, plugRec.Body.String())
	}
	if strings.Contains(plugRec.Body.String(), "never-leak-this") {
		t.Fatalf("GET /api/v1/plugins leaked secret env value: %s", plugRec.Body.String())
	}
	var plugResp PluginsEnvelopeResponse
	if err := json.Unmarshal(plugRec.Body.Bytes(), &plugResp); err != nil || plugResp.TotalCount != 1 || plugResp.AgentPlugins[0].Name != "rest_plugin" {
		t.Fatalf("unexpected GET /api/v1/plugins: %+v (%v)", plugResp, err)
	}

	// 3. GET /api/v1/models (recommend, live loopback list, and SSRF refusal)
	modRec := doReq(http.MethodGet, "/api/v1/models?action=recommend", "")
	if modRec.Result().StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/models?action=recommend status=%d body=%s", modRec.Result().StatusCode, modRec.Body.String())
	}
	var modResp LocalModelsEnvelopeResponse
	if err := json.Unmarshal(modRec.Body.Bytes(), &modResp); err != nil || modResp.DetectedVRAMGB != 24 || len(modResp.Recommendations) == 0 {
		t.Fatalf("unexpected GET /api/v1/models?action=recommend: %+v (%v)", modResp, err)
	}

	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{
					"name":        "codellama:70b",
					"size":        int64(38 * 1024 * 1024 * 1024),
					"digest":      "sha256:222",
					"modified_at": time.Now().UTC().Format(time.RFC3339),
					"details":     map[string]any{"family": "llama", "parameter_size": "70B"},
				},
			},
		})
	}))
	defer ollamaSrv.Close()

	modListRec := doReq(http.MethodGet, "/api/v1/models?action=list&host="+url.QueryEscape(ollamaSrv.URL), "")
	if modListRec.Result().StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/models?action=list status=%d body=%s", modListRec.Result().StatusCode, modListRec.Body.String())
	}
	var modListResp LocalModelsEnvelopeResponse
	if err := json.Unmarshal(modListRec.Body.Bytes(), &modListResp); err != nil || modListResp.ModelCount != 1 || modListResp.Models[0].Name != "codellama:70b" {
		t.Fatalf("unexpected GET /api/v1/models?action=list: %+v (%v)", modListResp, err)
	}

	ssrfRec := doReq(http.MethodGet, "/api/v1/models?action=list&host="+url.QueryEscape("http://169.254.169.254/latest/meta-data"), "")
	if ssrfRec.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request on SSRF host override, got %d (%s)", ssrfRec.Result().StatusCode, ssrfRec.Body.String())
	}

	// 4. Negative checks on grep/extract/errors + live tmux pane capture exercise
	missingPatRec := doReq(http.MethodGet, "/api/v1/grep", "")
	if missingPatRec.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when pattern omitted, got %d", missingPatRec.Result().StatusCode)
	}
	if badLinesRec := doReq(http.MethodGet, "/api/v1/grep?pattern=ok&lines=99999", ""); badLinesRec.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when grep lines exceeds cap, got %d (%s)", badLinesRec.Result().StatusCode, badLinesRec.Body.String())
	}
	if badCtxRec := doReq(http.MethodGet, "/api/v1/grep?pattern=ok&context=99", ""); badCtxRec.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when grep context exceeds cap, got %d (%s)", badCtxRec.Result().StatusCode, badCtxRec.Body.String())
	}
	if badExtractRec := doReq(http.MethodGet, "/api/v1/sessions/demo/extract?lines=99999", ""); badExtractRec.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when extract lines exceeds cap, got %d (%s)", badExtractRec.Result().StatusCode, badExtractRec.Body.String())
	}
	if badErrorsRec := doReq(http.MethodGet, "/api/v1/sessions/demo/errors?context=99", ""); badErrorsRec.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when errors context exceeds cap, got %d (%s)", badErrorsRec.Result().StatusCode, badErrorsRec.Body.String())
	}
	missingSessRec := doReq(http.MethodGet, "/api/v1/sessions/nonexistent_wave16_sess/errors", "")
	if missingSessRec.Result().StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for missing session errors, got %d (%s)", missingSessRec.Result().StatusCode, missingSessRec.Body.String())
	}

	testutil.RequireTmuxThrottled(t)
	sessName := fmt.Sprintf("ntm_rest_wave16_%d", time.Now().UnixNano())
	if err := tmux.CreateSession(sessName, repoDir); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(sessName) })

	panes, err := tmux.GetPanes(sessName)
	if err != nil || len(panes) == 0 {
		t.Fatalf("GetPanes: %v (%d)", err, len(panes))
	}
	pane := panes[0]
	if _, err := tmux.DefaultClient.Run("select-pane", "-t", pane.ID, "-T", sessName+"__cod_1"); err != nil {
		t.Fatalf("select-pane title: %v", err)
	}

	samplePath := filepath.Join(repoDir, "rest_pane_output.txt")
	sampleText := "panic: wave16 simulated failure\nWAVE16_REST_GREP_TOKEN\n```python\n# app/wave16.py\ndef wave16_rest():\n    return 'ok'\n```\n"
	if err := os.WriteFile(samplePath, []byte(sampleText), 0o644); err != nil {
		t.Fatalf("write samplePath: %v", err)
	}
	if err := tmux.SendKeys(pane.ID, "cat "+samplePath, true); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := tmux.CapturePaneOutput(pane.ID, 50)
		if strings.Contains(out, "wave16_rest") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	grepRec := doReq(http.MethodGet, "/api/v1/sessions/"+sessName+"/grep?pattern=WAVE16_REST_GREP_TOKEN&type=cod&context=1", "")
	if grepRec.Result().StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/sessions/%s/grep status=%d body=%s", sessName, grepRec.Result().StatusCode, grepRec.Body.String())
	}
	var grepResp SessionGrepEnvelopeResponse
	if err := json.Unmarshal(grepRec.Body.Bytes(), &grepResp); err != nil || grepResp.MatchCount == 0 {
		t.Fatalf("unexpected session grep response: %+v (%v, raw=%s)", grepResp, err, grepRec.Body.String())
	}

	extRec := doReq(http.MethodGet, "/api/v1/sessions/"+sessName+"/extract?lang=python&lines=100", "")
	if extRec.Result().StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/sessions/%s/extract status=%d body=%s", sessName, extRec.Result().StatusCode, extRec.Body.String())
	}
	var extResp SessionExtractEnvelopeResponse
	if err := json.Unmarshal(extRec.Body.Bytes(), &extResp); err != nil || extResp.TotalFound == 0 || !strings.Contains(extResp.Blocks[0].Content, "wave16_rest") {
		t.Fatalf("unexpected session extract response: %+v (%v, raw=%s)", extResp, err, extRec.Body.String())
	}

	errRec := doReq(http.MethodGet, "/api/v1/sessions/"+sessName+"/errors?agent_type=cod&context=1", "")
	if errRec.Result().StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/sessions/%s/errors status=%d body=%s", sessName, errRec.Result().StatusCode, errRec.Body.String())
	}
	var errResp SessionErrorsEnvelopeResponse
	if err := json.Unmarshal(errRec.Body.Bytes(), &errResp); err != nil || errResp.Summary.TotalErrors == 0 {
		t.Fatalf("unexpected session errors response: %+v (%v, raw=%s)", errResp, err, errRec.Body.String())
	}
}

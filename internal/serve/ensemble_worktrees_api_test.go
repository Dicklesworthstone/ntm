package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	statepkg "github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestEnsembleAndWorktreesOpenAPIReflection(t *testing.T) {
	spec, err := GenerateOpenAPISpecFromRouter("dev", "http://127.0.0.1:7337")
	if err != nil {
		t.Fatalf("GenerateOpenAPISpecFromRouter: %v", err)
	}

	if spec.OpenAPI != "3.1.0" {
		t.Fatalf("openapi = %q, want 3.1.0", spec.OpenAPI)
	}
	if len(spec.Servers) == 0 || spec.Servers[0].URL != "http://127.0.0.1:7337" {
		t.Fatalf("servers = %+v, want http://127.0.0.1:7337", spec.Servers)
	}

	expectedRoutes := []struct {
		method      string
		path        string
		wantRespRef string
		wantReqRef  string
		wantProp    string
	}{
		{"GET", "/api/v1/ensemble/modes", "#/components/schemas/EnsembleModesEnvelopeResponse", "", "modes"},
		{"GET", "/api/v1/ensemble/presets", "#/components/schemas/EnsemblePresetsEnvelopeResponse", "", "presets"},
		{"POST", "/api/v1/ensemble/suggest", "#/components/schemas/EnsembleSuggestEnvelopeResponse", "#/components/schemas/EnsembleSuggestRequest", "question"},
		{"GET", "/api/v1/ensemble/sessions/{session}", "#/components/schemas/EnsembleSessionEnvelopeResponse", "", "ensemble"},
		{"POST", "/api/v1/ensemble/spawn", "#/components/schemas/EnsembleSpawnEnvelopeResponse", "#/components/schemas/EnsembleSpawnRequest", "modes"},
		{"POST", "/api/v1/ensemble/sessions/{session}/synthesize", "#/components/schemas/EnsembleSynthesizeEnvelopeResponse", "#/components/schemas/EnsembleSynthesizeRequest", "report"},
		{"POST", "/api/v1/ensemble/sessions/{session}/stop", "#/components/schemas/EnsembleStopEnvelopeResponse", "#/components/schemas/EnsembleStopRequest", "result"},
		{"GET", "/api/v1/worktrees", "#/components/schemas/WorktreesListEnvelopeResponse", "", "worktrees"},
		{"POST", "/api/v1/worktrees", "#/components/schemas/WorktreeCreateEnvelopeResponse", "#/components/schemas/CreateWorktreeRequest", "worktree"},
		{"POST", "/api/v1/worktrees/{agent}/merge", "#/components/schemas/WorktreeMergeEnvelopeResponse", "#/components/schemas/MergeWorktreeRequest", "merged"},
		{"DELETE", "/api/v1/worktrees/{agent}", "#/components/schemas/WorktreeDeleteEnvelopeResponse", "", "removed"},
		{"GET", "/api/v1/health/backpressure", "#/components/schemas/HealthBackpressureEnvelopeResponse", "", "backpressure"},
		{"GET", "/api/v1/swarm/lab/scenarios", "#/components/schemas/SwarmLabEnvelopeResponse", "", "scenarios"},
		{"POST", "/api/v1/swarm/lab/run", "#/components/schemas/SwarmLabEnvelopeResponse", "#/components/schemas/SwarmLabRunRequest", "action"},
	}

	for _, tc := range expectedRoutes {
		item, ok := spec.Paths[tc.path]
		if !ok {
			t.Fatalf("%s %s missing from OpenAPI paths", tc.method, tc.path)
		}
		var op *Operation
		switch tc.method {
		case "GET":
			op = item.Get
		case "POST":
			op = item.Post
		case "DELETE":
			op = item.Delete
		}
		if op == nil {
			t.Fatalf("%s %s operation is nil", tc.method, tc.path)
		}

		resp200, ok := op.Responses["200"]
		if !ok {
			t.Fatalf("%s %s missing 200 response", tc.method, tc.path)
		}
		media, ok := resp200.Content["application/json"]
		if !ok || media.Schema == nil {
			t.Fatalf("%s %s missing application/json schema", tc.method, tc.path)
		}
		if media.Schema.Ref == "" || media.Schema.Ref == "#/components/schemas/SuccessEnvelope" {
			t.Fatalf("%s %s has generic or empty response schema ref %q", tc.method, tc.path, media.Schema.Ref)
		}
		if media.Schema.Ref != tc.wantRespRef {
			t.Fatalf("%s %s response ref = %q, want %q", tc.method, tc.path, media.Schema.Ref, tc.wantRespRef)
		}

		compName := strings.TrimPrefix(media.Schema.Ref, "#/components/schemas/")
		compSchema, ok := spec.Components.Schemas[compName]
		if !ok || compSchema == nil {
			t.Fatalf("%s %s component %q missing from components.schemas", tc.method, tc.path, compName)
		}
		if _, hasProp := compSchema.Properties[tc.wantProp]; !hasProp {
			t.Fatalf("%s %s component %q missing expected property %q", tc.method, tc.path, compName, tc.wantProp)
		}

		if tc.wantReqRef != "" {
			if op.RequestBody == nil {
				t.Fatalf("%s %s missing requestBody", tc.method, tc.path)
			}
			reqMedia, ok := op.RequestBody.Content["application/json"]
			if !ok || reqMedia.Schema == nil {
				t.Fatalf("%s %s missing requestBody application/json schema", tc.method, tc.path)
			}
			if reqMedia.Schema.Ref != tc.wantReqRef {
				t.Fatalf("%s %s requestBody ref = %q, want %q", tc.method, tc.path, reqMedia.Schema.Ref, tc.wantReqRef)
			}
			reqComp := strings.TrimPrefix(reqMedia.Schema.Ref, "#/components/schemas/")
			if _, ok := spec.Components.Schemas[reqComp]; !ok {
				t.Fatalf("%s %s request component %q missing from components.schemas", tc.method, tc.path, reqComp)
			}
		}
	}

	// Validate JSON round-trip, >=98% typed /api/v1/* coverage, and full $ref resolution in-memory.
	rawSpec, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal OpenAPI spec: %v", err)
	}
	var roundTrip OpenAPISpec
	if err := json.Unmarshal(rawSpec, &roundTrip); err != nil {
		t.Fatalf("unmarshal OpenAPI spec: %v", err)
	}

	v1Total, v1Typed := 0, 0
	for path, item := range roundTrip.Paths {
		if !strings.HasPrefix(path, "/api/v1/") {
			continue
		}
		for _, op := range []*Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete} {
			if op == nil {
				continue
			}
			v1Total++
			if _, ok := op.Responses["101"]; ok {
				v1Typed++
				continue
			}
			for _, code := range []string{"200", "201", "202"} {
				resp, ok := op.Responses[code]
				if !ok {
					continue
				}
				if _, sse := resp.Content["text/event-stream"]; sse {
					v1Typed++
					break
				}
				if _, gz := resp.Content["application/gzip"]; gz {
					v1Typed++
					break
				}
				if m, ok := resp.Content["application/json"]; ok && m.Schema != nil &&
					m.Schema.Ref != "" && m.Schema.Ref != "#/components/schemas/SuccessEnvelope" {
					v1Typed++
					break
				}
			}
		}
	}
	coverage := float64(v1Typed) * 100.0 / float64(v1Total)
	if coverage < 98.0 {
		t.Fatalf("/api/v1/* typed coverage = %.2f%% (%d/%d), want >= 98.0%%", coverage, v1Typed, v1Total)
	}

	// Verify Kernel catalog convergence picks up all 11 REST routes.
	EnsureKernelCatalogRegistered()
	restSet := make(map[string]bool)
	for _, cmd := range kernel.List() {
		if cmd.REST != nil {
			restSet[strings.ToUpper(cmd.REST.Method)+" "+cmd.REST.Path] = true
		}
	}
	for _, tc := range expectedRoutes {
		key := tc.method + " " + tc.path
		if !restSet[key] {
			t.Errorf("kernel catalog missing REST binding for %s", key)
		}
	}
}

func TestEnsembleAndWorktreesRESTEndpoints(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("NTM_CONFIG", "")
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	repoDir := initTempGitRepoForWorktreesTest(t)

	srv := New(Config{ProjectDir: repoDir})
	t.Cleanup(srv.Stop)
	router := srv.Router()

	doJSON := func(method, target string, body interface{}) (*httptest.ResponseRecorder, map[string]interface{}) {
		t.Helper()
		var reader *bytes.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal request body: %v", err)
			}
			reader = bytes.NewReader(raw)
		} else {
			reader = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, target, reader)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var decoded map[string]interface{}
		if rec.Body.Len() > 0 {
			if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("%s %s invalid JSON (%d): %v\nbody=%s", method, target, rec.Code, err, rec.Body.String())
			}
		}
		return rec, decoded
	}

	// 1. GET /api/v1/ensemble/modes
	t.Run("EnsembleModes", func(t *testing.T) {
		rec, body := doJSON(http.MethodGet, "/api/v1/ensemble/modes?tier=core&limit=3", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/ensemble/modes status=%d body=%v", rec.Code, body)
		}
		modes, ok := body["modes"].([]interface{})
		if !ok || len(modes) == 0 || len(modes) > 3 {
			t.Fatalf("expected 1..3 modes, got %#v", body["modes"])
		}

		badRec, badBody := doJSON(http.MethodGet, "/api/v1/ensemble/modes?limit=-1", nil)
		if badRec.Code != http.StatusBadRequest || badBody["error_code"] != ErrCodeInvalidRequest {
			t.Fatalf("expected 400 INVALID_REQUEST on negative limit, got %d %v", badRec.Code, badBody)
		}
	})

	// 2. GET /api/v1/ensemble/presets
	t.Run("EnsemblePresets", func(t *testing.T) {
		rec, body := doJSON(http.MethodGet, "/api/v1/ensemble/presets", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/ensemble/presets status=%d body=%v", rec.Code, body)
		}
		presets, ok := body["presets"].([]interface{})
		if !ok || len(presets) == 0 {
			t.Fatalf("expected non-empty presets array, got %#v", body["presets"])
		}
	})

	// 3. POST /api/v1/ensemble/suggest
	t.Run("EnsembleSuggest", func(t *testing.T) {
		badRec, badBody := doJSON(http.MethodPost, "/api/v1/ensemble/suggest", map[string]interface{}{
			"question": "   ",
		})
		if badRec.Code != http.StatusBadRequest || badBody["error_code"] != ErrCodeInvalidRequest {
			t.Fatalf("expected 400 INVALID_REQUEST on blank question, got %d %v", badRec.Code, badBody)
		}

		rec, body := doJSON(http.MethodPost, "/api/v1/ensemble/suggest", map[string]interface{}{
			"question": "What security vulnerabilities or auth bypass risks exist in this service?",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("POST /api/v1/ensemble/suggest status=%d body=%v", rec.Code, body)
		}
		if _, ok := body["suggestions"].([]interface{}); !ok {
			t.Fatalf("expected suggestions array, got %#v", body["suggestions"])
		}

		idRec, idBody := doJSON(http.MethodPost, "/api/v1/ensemble/suggest", map[string]interface{}{
			"question": "What security vulnerabilities exist in authentication?",
			"id_only":  true,
		})
		if idRec.Code != http.StatusOK {
			t.Fatalf("POST /api/v1/ensemble/suggest (id_only) status=%d body=%v", idRec.Code, idBody)
		}
		if _, hasPresetName := idBody["preset_name"]; !hasPresetName {
			t.Fatalf("expected preset_name in id_only response, got %#v", idBody)
		}
	})

	// 4. POST /api/v1/ensemble/spawn validation
	t.Run("EnsembleSpawnValidation", func(t *testing.T) {
		cases := []map[string]interface{}{
			{"question": "missing session", "preset": "project-diagnosis"},
			{"session": "ens-val", "preset": "project-diagnosis"},
			{"session": "ens-val", "question": "missing preset and modes"},
			{"session": "ens-val", "question": "conflict", "preset": "project-diagnosis", "modes": []string{"deductive", "bayesian"}},
			{"session": "ens-val", "question": "bad strategy", "preset": "project-diagnosis", "synthesis_strategy": "nonexistent-strategy"},
		}
		for i, payload := range cases {
			rec, body := doJSON(http.MethodPost, "/api/v1/ensemble/spawn", payload)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("case %d: expected 400, got %d (%v)", i, rec.Code, body)
			}
		}
	})

	// 5. Ensemble session GET, Synthesize, and Stop over real persisted ensemble state & outputs
	t.Run("EnsembleSessionSynthesizeAndStop", func(t *testing.T) {
		missRec, _ := doJSON(http.MethodGet, "/api/v1/ensemble/sessions/missing-ens-session", nil)
		if missRec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 on missing ensemble session, got %d", missRec.Code)
		}
		synthMissRec, _ := doJSON(http.MethodPost, "/api/v1/ensemble/sessions/missing-ens-session/synthesize", nil)
		if synthMissRec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 on missing session synthesize, got %d", synthMissRec.Code)
		}
		stopMissRec, _ := doJSON(http.MethodPost, "/api/v1/ensemble/sessions/missing-ens-session/stop", nil)
		if stopMissRec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 on missing session stop, got %d", stopMissRec.Code)
		}

		outDir := t.TempDir()
		outPathA := filepath.Join(outDir, "deductive.json")
		outPathB := filepath.Join(outDir, "bayesian.json")
		now := time.Now().UTC()

		writeModeOutputFile(t, outPathA, ensemble.ModeOutput{
			ModeID: "deductive",
			Thesis: "Token validation must enforce strict audience checks on every API route.",
			TopFindings: []ensemble.Finding{
				{
					Finding:         "JWT audience claim is verified inconsistently across legacy endpoints",
					Impact:          ensemble.ImpactHigh,
					Confidence:      0.9,
					EvidencePointer: "internal/serve/auth.go:42",
					Reasoning:       "Direct inspection of token verification paths",
				},
			},
			Risks: []ensemble.Risk{
				{
					Risk:       "Cross-tenant token replay if audience check is skipped",
					Impact:     ensemble.ImpactHigh,
					Likelihood: 0.7,
					Mitigation: "Centralize token audience verification in middleware",
				},
			},
			Recommendations: []ensemble.Recommendation{
				{
					Recommendation: "Enforce audience validation in authMiddlewareFunc",
					Priority:       ensemble.ImpactHigh,
					Rationale:      "Eliminates per-handler verification drift",
				},
			},
			Confidence:  0.88,
			GeneratedAt: now,
		})

		writeModeOutputFile(t, outPathB, ensemble.ModeOutput{
			ModeID: "bayesian",
			Thesis: "Prior auth regressions cluster around legacy handler paths lacking shared middleware.",
			TopFindings: []ensemble.Finding{
				{
					Finding:         "Worktree endpoints require explicit path boundary checks on session and agent names",
					Impact:          ensemble.ImpactMedium,
					Confidence:      0.82,
					EvidencePointer: "internal/worktrees/manager.go:116",
					Reasoning:       "Posterior risk elevated for user-controlled path components",
				},
			},
			Confidence:  0.84,
			GeneratedAt: now,
		})

		// First save with one active assignment to verify 409 SYNTHESIS_NOT_READY without force.
		sessName := "ens-synth-session"
		completedAt := now
		state := &ensemble.EnsembleSession{
			SessionName:       sessName,
			Question:          "Audit API security and worktree isolation boundaries",
			PresetUsed:        "security-review",
			Status:            ensemble.EnsembleActive,
			SynthesisStrategy: ensemble.StrategyConsensus,
			CreatedAt:         now,
			Assignments: []ensemble.ModeAssignment{
				{
					ModeID:      "deductive",
					PaneName:    sessName + "__cc_1",
					AgentType:   "cc",
					Status:      ensemble.AssignmentDone,
					OutputPath:  outPathA,
					AssignedAt:  now,
					CompletedAt: &completedAt,
				},
				{
					ModeID:     "bayesian",
					PaneName:   sessName + "__cod_1",
					AgentType:  "cod",
					Status:     ensemble.AssignmentActive,
					OutputPath: outPathB,
					AssignedAt: now,
				},
			},
		}
		if err := ensemble.SaveSession(sessName, state); err != nil {
			t.Fatalf("SaveSession: %v", err)
		}

		// GET /api/v1/ensemble/sessions/{session} -> 200 (offline saved state)
		getRec, getBody := doJSON(http.MethodGet, "/api/v1/ensemble/sessions/"+sessName, nil)
		if getRec.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/ensemble/sessions/%s status=%d body=%v", sessName, getRec.Code, getBody)
		}

		// Synthesize without force while 1 mode is still active -> 409 Conflict
		notReadyRec, notReadyBody := doJSON(http.MethodPost, "/api/v1/ensemble/sessions/"+sessName+"/synthesize", map[string]interface{}{})
		if notReadyRec.Code != http.StatusConflict {
			t.Fatalf("expected 409 when active mode still running, got %d (%v)", notReadyRec.Code, notReadyBody)
		}

		// Mark second assignment done and run real synthesis + diversity/redundancy analysis.
		state.Assignments[1].Status = ensemble.AssignmentDone
		state.Assignments[1].CompletedAt = &completedAt
		state.Status = ensemble.EnsembleReady
		if err := ensemble.SaveSession(sessName, state); err != nil {
			t.Fatalf("SaveSession ready: %v", err)
		}

		synthRec, synthBody := doJSON(http.MethodPost, "/api/v1/ensemble/sessions/"+sessName+"/synthesize", map[string]interface{}{
			"strategy":      "consensus",
			"output_format": "markdown",
		})
		if synthRec.Code != http.StatusOK {
			t.Fatalf("POST synthesize status=%d body=%v", synthRec.Code, synthBody)
		}
		report, ok := synthBody["report"].(map[string]interface{})
		if !ok || report["summary"] == "" {
			t.Fatalf("expected non-empty synthesis report, got %#v", synthBody["report"])
		}
		redundancy, ok := synthBody["redundancy"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected redundancy diversity analysis in response, got %#v", synthBody["redundancy"])
		}
		if pairs, ok := redundancy["pairwise_scores"].([]interface{}); !ok || len(pairs) != 1 {
			t.Fatalf("expected 1 pairwise diversity score for 2 modes, got %#v", redundancy["pairwise_scores"])
		}
		if formatted, _ := synthBody["formatted_output"].(string); strings.TrimSpace(formatted) == "" {
			t.Fatalf("expected non-empty formatted_output")
		}

		// Stop persisted ensemble session -> 200
		stopRec, stopBody := doJSON(http.MethodPost, "/api/v1/ensemble/sessions/"+sessName+"/stop", map[string]interface{}{
			"no_collect": true,
		})
		if stopRec.Code != http.StatusOK {
			t.Fatalf("POST stop status=%d body=%v", stopRec.Code, stopBody)
		}
		stopResult, ok := stopBody["result"].(map[string]interface{})
		if !ok || stopResult["final_status"] != "stopped" {
			t.Fatalf("expected result.final_status=stopped, got %#v", stopBody)
		}
	})

	// 6. Worktrees lifecycle: unscoped 400 -> list empty -> create -> list -> dirty delete 409 -> merge preview -> merge -> delete -> list empty
	t.Run("WorktreesLifecycle", func(t *testing.T) {
		unscopedSrv := New(Config{})
		t.Cleanup(unscopedSrv.Stop)
		unscopedRec := httptest.NewRecorder()
		unscopedSrv.Router().ServeHTTP(unscopedRec, httptest.NewRequest(http.MethodGet, "/api/v1/worktrees", nil))
		if unscopedRec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 on unscoped GET /api/v1/worktrees, got %d", unscopedRec.Code)
		}

		relRec, _ := doJSON(http.MethodGet, "/api/v1/worktrees?project=relative/path", nil)
		if relRec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 on relative ?project=, got %d", relRec.Code)
		}

		// Initial list -> empty []
		listRec, listBody := doJSON(http.MethodGet, "/api/v1/worktrees", nil)
		if listRec.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/worktrees status=%d body=%v", listRec.Code, listBody)
		}
		initialWTs, ok := listBody["worktrees"].([]interface{})
		if !ok || len(initialWTs) != 0 {
			t.Fatalf("expected empty worktrees slice [], got %#v", listBody["worktrees"])
		}

		// Validation 400 on POST /api/v1/worktrees
		badCreateRec, _ := doJSON(http.MethodPost, "/api/v1/worktrees", map[string]interface{}{
			"session": "wt-sess",
		})
		if badCreateRec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 when agent is missing, got %d", badCreateRec.Code)
		}

		// Create worktree for agent claude-1
		createRec, createBody := doJSON(http.MethodPost, "/api/v1/worktrees", map[string]interface{}{
			"session": "wt-sess",
			"agent":   "claude-1",
		})
		if createRec.Code != http.StatusOK {
			t.Fatalf("POST /api/v1/worktrees status=%d body=%v", createRec.Code, createBody)
		}
		wtObj, ok := createBody["worktree"].(map[string]interface{})
		if !ok || wtObj["created"] != true {
			t.Fatalf("expected created=true worktree, got %#v", createBody["worktree"])
		}
		wtPath, _ := wtObj["path"].(string)
		if wtPath == "" {
			t.Fatalf("expected non-empty worktree path")
		}

		// List worktrees -> 1 entry
		listRec2, listBody2 := doJSON(http.MethodGet, "/api/v1/worktrees?session=wt-sess", nil)
		if listRec2.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/worktrees?session=wt-sess status=%d body=%v", listRec2.Code, listBody2)
		}
		listedWTs, ok := listBody2["worktrees"].([]interface{})
		if !ok || len(listedWTs) != 1 {
			t.Fatalf("expected 1 worktree, got %#v", listBody2["worktrees"])
		}

		// Commit a change inside the agent worktree
		featurePath := filepath.Join(wtPath, "feature.txt")
		if err := os.WriteFile(featurePath, []byte("isolated agent work\n"), 0o644); err != nil {
			t.Fatalf("write feature.txt: %v", err)
		}
		runGitTestCmd(t, wtPath, "add", "feature.txt")
		runGitTestCmd(t, wtPath, "commit", "-m", "feat: add isolated agent feature")

		// Attempt delete without force while unmerged commit exists -> 409 Conflict
		dirtyDelRec, _ := doJSON(http.MethodDelete, "/api/v1/worktrees/claude-1?session=wt-sess", nil)
		if dirtyDelRec.Code != http.StatusConflict {
			t.Fatalf("expected 409 Conflict removing unmerged worktree without force, got %d", dirtyDelRec.Code)
		}

		// Merge preview (auto-resolving session!)
		previewRec, previewBody := doJSON(http.MethodPost, "/api/v1/worktrees/claude-1/merge", map[string]interface{}{
			"preview": true,
		})
		if previewRec.Code != http.StatusOK {
			t.Fatalf("POST /api/v1/worktrees/claude-1/merge (preview) status=%d body=%v", previewRec.Code, previewBody)
		}
		if previewBody["merged"] != false {
			t.Fatalf("expected merged=false on preview, got %#v", previewBody["merged"])
		}
		previewObj, ok := previewBody["preview"].(map[string]interface{})
		if !ok || previewObj["can_merge"] != true {
			t.Fatalf("expected preview.can_merge=true, got %#v", previewBody["preview"])
		}

		// Real merge back onto main
		mergeRec, mergeBody := doJSON(http.MethodPost, "/api/v1/worktrees/claude-1/merge", map[string]interface{}{
			"session": "wt-sess",
			"preview": false,
		})
		if mergeRec.Code != http.StatusOK {
			t.Fatalf("POST /api/v1/worktrees/claude-1/merge status=%d body=%v", mergeRec.Code, mergeBody)
		}
		if mergeBody["merged"] != true {
			t.Fatalf("expected merged=true, got %#v", mergeBody)
		}
		if _, err := os.Stat(filepath.Join(repoDir, "feature.txt")); err != nil {
			t.Fatalf("expected feature.txt merged onto main in %s: %v", repoDir, err)
		}

		// Delete worktree -> 200
		delRec, delBody := doJSON(http.MethodDelete, "/api/v1/worktrees/claude-1?session=wt-sess&force=true", nil)
		if delRec.Code != http.StatusOK {
			t.Fatalf("DELETE /api/v1/worktrees/claude-1 status=%d body=%v", delRec.Code, delBody)
		}
		if delBody["removed"] != true {
			t.Fatalf("expected removed=true, got %#v", delBody)
		}

		// Deleting again -> 404
		delAgainRec, _ := doJSON(http.MethodDelete, "/api/v1/worktrees/claude-1?session=wt-sess", nil)
		if delAgainRec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 deleting already-removed worktree, got %d", delAgainRec.Code)
		}

		// Final list -> empty again
		finalRec, finalBody := doJSON(http.MethodGet, "/api/v1/worktrees", nil)
		if finalRec.Code != http.StatusOK {
			t.Fatalf("final GET /api/v1/worktrees status=%d body=%v", finalRec.Code, finalBody)
		}
		finalWTs, ok := finalBody["worktrees"].([]interface{})
		if !ok || len(finalWTs) != 0 {
			t.Fatalf("expected 0 worktrees after delete, got %#v", finalBody["worktrees"])
		}
	})
}

func writeModeOutputFile(t *testing.T, path string, out ensemble.ModeOutput) {
	t.Helper()
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("marshal ModeOutput: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write ModeOutput %s: %v", path, err)
	}
}

func initTempGitRepoForWorktreesTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err == nil {
		dir = resolved
	}
	runGitTestCmd(t, dir, "init", "-b", "main")
	runGitTestCmd(t, dir, "config", "user.name", "NTM Test")
	runGitTestCmd(t, dir, "config", "user.email", "ntm-test@example.com")
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("# Test Repo\n"), 0o644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	runGitTestCmd(t, dir, "add", "README.md")
	runGitTestCmd(t, dir, "commit", "-m", "chore: initial commit")
	return dir
}

func runGitTestCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s failed: %v\n%s", strings.Join(args, " "), dir, err, string(out))
	}
}

func TestBackpressureAndSwarmLabRESTEndpoints(t *testing.T) {
	srv := NewHermeticServer("dev")
	t.Cleanup(srv.Stop)
	router := srv.Router()

	doJSON := func(method, target string, body interface{}) (*httptest.ResponseRecorder, map[string]interface{}) {
		t.Helper()
		var reader *bytes.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal request body: %v", err)
			}
			reader = bytes.NewReader(raw)
		} else {
			reader = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, target, reader)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var decoded map[string]interface{}
		if rec.Body.Len() > 0 {
			if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("%s %s invalid JSON (%d): %v\nbody=%s", method, target, rec.Code, err, rec.Body.String())
			}
		}
		return rec, decoded
	}

	t.Run("GET /api/v1/health/backpressure includes live REST, SSE, and WebSocket surfaces", func(t *testing.T) {
		rec, body := doJSON(http.MethodGet, "/api/v1/health/backpressure", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/health/backpressure status=%d body=%v", rec.Code, body)
		}
		if body["success"] != true {
			t.Fatalf("expected success=true, got %#v", body)
		}
		bp, ok := body["backpressure"].(map[string]interface{})
		if !ok {
			t.Fatalf("missing backpressure object: %#v", body)
		}
		surfaces, ok := bp["surfaces"].([]interface{})
		if !ok || len(surfaces) == 0 {
			t.Fatalf("expected non-empty backpressure.surfaces, got %#v", bp["surfaces"])
		}
		seenSurfaces := make(map[string]bool)
		for _, rawSurface := range surfaces {
			if row, ok := rawSurface.(map[string]interface{}); ok {
				if name, _ := row["surface"].(string); name != "" {
					seenSurfaces[name] = true
				}
			}
		}
		for _, want := range []string{"rest_handler", "sse_stream", "websocket"} {
			if !seenSurfaces[want] {
				t.Errorf("backpressure.surfaces missing live server surface %q (seen=%v)", want, seenSurfaces)
			}
		}
		if _, ok := body["dashboard"].(map[string]interface{}); !ok {
			t.Errorf("missing dashboard projection: %#v", body["dashboard"])
		}
		if _, ok := body["heatmap"].(map[string]interface{}); !ok {
			t.Errorf("missing heatmap projection: %#v", body["heatmap"])
		}
		if _, ok := body["load_shedding"].(map[string]interface{}); !ok {
			t.Errorf("missing load_shedding contract: %#v", body["load_shedding"])
		}

		badRec, _ := doJSON(http.MethodGet, "/api/v1/health/backpressure?session=bad:name", nil)
		if badRec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for invalid session name, got %d", badRec.Code)
		}
	})

	t.Run("GET /api/v1/swarm/lab/scenarios and POST /api/v1/swarm/lab/run", func(t *testing.T) {
		listRec, listBody := doJSON(http.MethodGet, "/api/v1/swarm/lab/scenarios?gate=short", nil)
		if listRec.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/swarm/lab/scenarios status=%d body=%v", listRec.Code, listBody)
		}
		scenarios, ok := listBody["scenarios"].([]interface{})
		if !ok || len(scenarios) == 0 {
			t.Fatalf("expected scenarios list, got %#v", listBody["scenarios"])
		}

		runRec, runBody := doJSON(http.MethodPost, "/api/v1/swarm/lab/run", map[string]interface{}{
			"action":        "run",
			"pane_count":    4,
			"command_count": 8,
		})
		if runRec.Code != http.StatusOK {
			t.Fatalf("POST /api/v1/swarm/lab/run (run) status=%d body=%v", runRec.Code, runBody)
		}
		if _, ok := runBody["run_result"].(map[string]interface{}); !ok {
			t.Fatalf("expected run_result object, got %#v", runBody["run_result"])
		}

		corpusRec, corpusBody := doJSON(http.MethodPost, "/api/v1/swarm/lab/run", map[string]interface{}{
			"action": "corpus",
		})
		if corpusRec.Code != http.StatusOK {
			t.Fatalf("POST /api/v1/swarm/lab/run (corpus) status=%d body=%v", corpusRec.Code, corpusBody)
		}
		if _, ok := corpusBody["corpus_summary"].(map[string]interface{}); !ok {
			t.Fatalf("expected corpus_summary object, got %#v", corpusBody["corpus_summary"])
		}

		calRec, calBody := doJSON(http.MethodPost, "/api/v1/swarm/lab/run", map[string]interface{}{
			"action":     "calibrate",
			"profile_id": "ci-rest-probe",
		})
		if calRec.Code != http.StatusOK {
			t.Fatalf("POST /api/v1/swarm/lab/run (calibrate) status=%d body=%v", calRec.Code, calBody)
		}
		if _, ok := calBody["calibration"].(map[string]interface{}); !ok {
			t.Fatalf("expected calibration object, got %#v", calBody["calibration"])
		}

		badActionRec, _ := doJSON(http.MethodPost, "/api/v1/swarm/lab/run", map[string]interface{}{
			"action": "list",
		})
		if badActionRec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for unsupported action, got %d", badActionRec.Code)
		}
	})
}

func TestAsyncJobsEnsembleSpawnAndSynthesizeValidationAndLifecycle(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("NTM_CONFIG", "")
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	projectDir := t.TempDir()
	srv, _ := newJournalHTTPServer(t, filepath.Join(t.TempDir(), "jobs-ensemble.db"))
	srv.mu.Lock()
	srv.projectDir = projectDir
	srv.mu.Unlock()

	doRaw := func(method, target, rawBody string) (*httptest.ResponseRecorder, map[string]interface{}) {
		t.Helper()
		var bodyReader *bytes.Reader
		if rawBody != "" {
			bodyReader = bytes.NewReader([]byte(rawBody))
		} else {
			bodyReader = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, target, bodyReader)
		if rawBody != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		var decoded map[string]interface{}
		if rec.Body.Len() > 0 {
			if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("%s %s invalid JSON (%d): %v\nbody=%s", method, target, rec.Code, err, rec.Body.String())
			}
		}
		return rec, decoded
	}

	t.Run("Admission400EnvelopeAndSessionValidation", func(t *testing.T) {
		badEnvelopes := []struct {
			name string
			body string
		}{
			{
				name: "ensemble_spawn session conflict",
				body: `{"type":"ensemble_spawn","session":"ens-a","params":{"session":"ens-b","question":"q","preset":"project-diagnosis"}}`,
			},
			{
				name: "ensemble_spawn whitespace session",
				body: `{"type":"ensemble_spawn","session":"   ","params":{"question":"q","preset":"project-diagnosis"}}`,
			},
			{
				name: "ensemble_synthesize session conflict",
				body: `{"type":"ensemble_synthesize","session":"ens-a","params":{"session":"ens-b"}}`,
			},
			{
				name: "ensemble_synthesize non-string params.session",
				body: `{"type":"ensemble_synthesize","session":"ens-a","params":{"session":123}}`,
			},
			{
				name: "ensemble_synthesize whitespace session",
				body: `{"type":"ensemble_synthesize","session":"   "}`,
			},
		}
		for _, tc := range badEnvelopes {
			rec, body := doRaw(http.MethodPost, "/api/v1/jobs", tc.body)
			if rec.Code != http.StatusBadRequest || body["error_code"] != ErrCodeBadRequest {
				t.Fatalf("%s: status=%d body=%v, want 400 BAD_REQUEST", tc.name, rec.Code, body)
			}
		}
	})

	t.Run("ParameterValidationWrapsInvalidJobRequestAndFailsClosed", func(t *testing.T) {
		invalidCases := []struct {
			name     string
			jobType  string
			params   map[string]interface{}
			wantFrag string
		}{
			{
				name:     "spawn missing session",
				jobType:  JobTypeEnsembleSpawn,
				params:   map[string]interface{}{"question": "What broke?", "preset": "project-diagnosis"},
				wantFrag: "session is required",
			},
			{
				name:     "spawn invalid session name",
				jobType:  JobTypeEnsembleSpawn,
				params:   map[string]interface{}{"session": "bad:name", "question": "What broke?", "preset": "project-diagnosis"},
				wantFrag: "invalid session name",
			},
			{
				name:     "spawn missing question",
				jobType:  JobTypeEnsembleSpawn,
				params:   map[string]interface{}{"session": "ens-val-1", "preset": "project-diagnosis"},
				wantFrag: "question is required",
			},
			{
				name:     "spawn missing preset and modes",
				jobType:  JobTypeEnsembleSpawn,
				params:   map[string]interface{}{"session": "ens-val-1", "question": "What broke?"},
				wantFrag: "either preset or modes is required",
			},
			{
				name:     "spawn preset and modes mutually exclusive",
				jobType:  JobTypeEnsembleSpawn,
				params:   map[string]interface{}{"session": "ens-val-1", "question": "What broke?", "preset": "project-diagnosis", "modes": []string{"deductive", "bayesian"}},
				wantFrag: "preset and modes are mutually exclusive",
			},
			{
				name:     "spawn negative budget",
				jobType:  JobTypeEnsembleSpawn,
				params:   map[string]interface{}{"session": "ens-val-1", "question": "What broke?", "preset": "project-diagnosis", "budget_total": -5},
				wantFrag: "budget_total and budget_per_agent must be non-negative",
			},
			{
				name:     "synthesize missing session",
				jobType:  JobTypeEnsembleSynthesize,
				params:   map[string]interface{}{"strategy": "consensus"},
				wantFrag: "session name is required",
			},
			{
				name:     "synthesize invalid session",
				jobType:  JobTypeEnsembleSynthesize,
				params:   map[string]interface{}{"session": "bad:name"},
				wantFrag: "invalid session name",
			},
			{
				name:     "synthesize unsupported output_format",
				jobType:  JobTypeEnsembleSynthesize,
				params:   map[string]interface{}{"session": "ens-val-1", "output_format": "xml"},
				wantFrag: "unsupported output_format",
			},
			{
				name:     "synthesize invalid strategy",
				jobType:  JobTypeEnsembleSynthesize,
				params:   map[string]interface{}{"session": "ens-val-1", "strategy": "not-a-strategy"},
				wantFrag: "invalid",
			},
		}

		for _, tc := range invalidCases {
			_, err := srv.executeJobOperation(context.Background(), "val-check", CreateJobRequest{
				Type:   tc.jobType,
				Params: tc.params,
			})
			if !errors.Is(err, errInvalidJobRequest) {
				t.Fatalf("%s: executeJobOperation err = %v, want errors.Is(..., errInvalidJobRequest)", tc.name, err)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.wantFrag)) {
				t.Fatalf("%s: err = %q, want substring %q", tc.name, err.Error(), tc.wantFrag)
			}

			rawReq, marshalErr := json.Marshal(CreateJobRequest{Type: tc.jobType, Params: tc.params})
			if marshalErr != nil {
				t.Fatalf("%s: marshal: %v", tc.name, marshalErr)
			}
			env := postJob(t, srv, string(rawReq))
			final := pollJobTerminal(t, srv, env.Job.ID)
			if JobStatus(final.Job.Status) != JobStatusFailed || !strings.Contains(strings.ToLower(final.Job.Error), strings.ToLower(tc.wantFrag)) {
				t.Fatalf("%s: final job = %+v, want failed containing %q", tc.name, final.Job, tc.wantFrag)
			}
		}
	})

	t.Run("MissingSessionSynthesizeReachesFailedOutcome", func(t *testing.T) {
		rec, body := doRaw(http.MethodPost, "/api/v1/jobs", `{"type":"ensemble_synthesize","params":{"session":"missing-async-ens"}}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("POST /api/v1/jobs status=%d body=%v, want 202", rec.Code, body)
		}
		loc := rec.Header().Get("Location")
		jobObj, _ := body["job"].(map[string]interface{})
		jobID, _ := jobObj["id"].(string)
		if jobID == "" || loc != "/api/v1/jobs/"+jobID {
			t.Fatalf("Location=%q jobID=%q, want /api/v1/jobs/<id>", loc, jobID)
		}
		final := pollJobTerminal(t, srv, jobID)
		if JobStatus(final.Job.Status) != JobStatusFailed {
			t.Fatalf("missing session synthesize status=%s, want failed (result=%#v)", final.Job.Status, final.Job.Result)
		}
		if !strings.Contains(final.Job.Error, "SESSION_NOT_FOUND") || !strings.Contains(final.Job.Error, "missing-async-ens") {
			t.Fatalf("expected real SESSION_NOT_FOUND error, got %q", final.Job.Error)
		}
	})

	t.Run("SynthesizeCompletedFromPersistedEnsembleFixture", func(t *testing.T) {
		outDir := t.TempDir()
		outPathA := filepath.Join(outDir, "deductive.json")
		outPathB := filepath.Join(outDir, "bayesian.json")
		now := time.Now().UTC()

		writeModeOutputFile(t, outPathA, ensemble.ModeOutput{
			ModeID: "deductive",
			Thesis: "Async ensemble jobs must share the synchronous REST execution path.",
			TopFindings: []ensemble.Finding{
				{
					Finding:         "Shared execution guarantees identical validation and synthesis payloads",
					Impact:          ensemble.ImpactHigh,
					Confidence:      0.91,
					EvidencePointer: "internal/serve/server_ensemble.go:475",
					Reasoning:       "Single implementation eliminates drift between sync and async surfaces",
				},
			},
			Confidence:  0.9,
			GeneratedAt: now,
		})
		writeModeOutputFile(t, outPathB, ensemble.ModeOutput{
			ModeID: "bayesian",
			Thesis: "Durable progress checkpoints improve crash observability during synthesis.",
			TopFindings: []ensemble.Finding{
				{
					Finding:         "Progress checkpoints preserve session identity if a worker stops early",
					Impact:          ensemble.ImpactMedium,
					Confidence:      0.85,
					EvidencePointer: "internal/serve/jobs_dispatch.go:132",
					Reasoning:       "Journaled progress merges cleanly with final outcomes",
				},
			},
			Confidence:  0.86,
			GeneratedAt: now,
		})

		sessName := "async-ens-synth-ok"
		completedAt := now
		state := &ensemble.EnsembleSession{
			SessionName:       sessName,
			Question:          "How should async ensemble synthesis preserve parity and durability?",
			PresetUsed:        "architecture-review",
			Status:            ensemble.EnsembleReady,
			SynthesisStrategy: ensemble.StrategyConsensus,
			CreatedAt:         now,
			Assignments: []ensemble.ModeAssignment{
				{
					ModeID:      "deductive",
					PaneName:    sessName + "__cc_1",
					AgentType:   "cc",
					Status:      ensemble.AssignmentDone,
					OutputPath:  outPathA,
					AssignedAt:  now,
					CompletedAt: &completedAt,
				},
				{
					ModeID:      "bayesian",
					PaneName:    sessName + "__cod_1",
					AgentType:   "cod",
					Status:      ensemble.AssignmentDone,
					OutputPath:  outPathB,
					AssignedAt:  now,
					CompletedAt: &completedAt,
				},
			},
		}
		if err := ensemble.SaveSession(sessName, state); err != nil {
			t.Fatalf("SaveSession: %v", err)
		}

		rec, body := doRaw(http.MethodPost, "/api/v1/jobs",
			`{"type":"ensemble_synthesize","session":"`+sessName+`","params":{"strategy":"consensus","output_format":"markdown"}}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("POST /api/v1/jobs status=%d body=%v, want 202", rec.Code, body)
		}
		jobObj, _ := body["job"].(map[string]interface{})
		jobID, _ := jobObj["id"].(string)
		if rec.Header().Get("Location") != "/api/v1/jobs/"+jobID {
			t.Fatalf("Location=%q, want /api/v1/jobs/%s", rec.Header().Get("Location"), jobID)
		}

		final := pollJobTerminal(t, srv, jobID)
		if JobStatus(final.Job.Status) != JobStatusCompleted || final.Job.Progress != 100 {
			t.Fatalf("job status=%s progress=%v error=%q, want completed/100", final.Job.Status, final.Job.Progress, final.Job.Error)
		}
		if final.Job.Result["status"] != "complete" || final.Job.Result["session"] != sessName {
			t.Fatalf("unexpected job result metadata: %#v", final.Job.Result)
		}
		report, ok := final.Job.Result["report"].(map[string]interface{})
		if !ok || report["summary"] == "" {
			t.Fatalf("expected non-empty synthesis report in job result, got %#v", final.Job.Result["report"])
		}
		if formatted, _ := final.Job.Result["formatted_output"].(string); strings.TrimSpace(formatted) == "" {
			t.Fatalf("expected non-empty formatted_output in job result")
		}
		if _, ok := final.Job.Result["redundancy"].(map[string]interface{}); !ok {
			t.Fatalf("expected redundancy analysis in job result, got %#v", final.Job.Result["redundancy"])
		}
	})

	t.Run("DeleteJobCancelsEnsembleJobsAndRespectsContext", func(t *testing.T) {
		cancelledCtx, cancelNow := context.WithCancel(context.Background())
		cancelNow()
		for _, jt := range []string{JobTypeEnsembleSpawn, JobTypeEnsembleSynthesize} {
			out, err := srv.executeJobOperation(cancelledCtx, "precancelled-"+jt, CreateJobRequest{
				Type:   jt,
				Params: map[string]interface{}{"session": "never-runs"},
			})
			if !errors.Is(err, context.Canceled) || out != nil {
				t.Fatalf("%s with cancelled ctx returned (%v, %v), want (nil, context.Canceled)", jt, out, err)
			}
		}

		blockerStarted := make(chan struct{})
		releaseBlocker := make(chan struct{})
		srv.spawnAgents = func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
			close(blockerStarted)
			select {
			case <-releaseBlocker:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			out := &robot.SpawnOutput{Session: opts.Session}
			out.Success = true
			return out, nil
		}
		t.Cleanup(func() {
			select {
			case <-releaseBlocker:
			default:
				close(releaseBlocker)
			}
		})

		blockerEnv := postJob(t, srv, `{"type":"swarm_spawn","params":{"session":"barrier-holder","cc_count":1}}`)
		select {
		case <-blockerStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("barrier holder job did not start")
		}

		// Queue an ensemble_spawn job behind the barrier, then cancel it via DELETE /api/v1/jobs/{id}.
		ensRec, ensBody := doRaw(http.MethodPost, "/api/v1/jobs",
			`{"type":"ensemble_spawn","params":{"session":"cancel-me","question":"Should cancel?","preset":"project-diagnosis"}}`)
		if ensRec.Code != http.StatusAccepted {
			t.Fatalf("POST ensemble_spawn status=%d body=%v, want 202", ensRec.Code, ensBody)
		}
		ensJobObj, _ := ensBody["job"].(map[string]interface{})
		ensJobID, _ := ensJobObj["id"].(string)

		delRec, delBody := doRaw(http.MethodDelete, "/api/v1/jobs/"+ensJobID, "")
		if delRec.Code != http.StatusOK {
			t.Fatalf("DELETE /api/v1/jobs/%s status=%d body=%v, want 200", ensJobID, delRec.Code, delBody)
		}

		close(releaseBlocker)
		_ = pollJobTerminal(t, srv, blockerEnv.Job.ID)
		cancelledFinal := pollJobTerminal(t, srv, ensJobID)
		if JobStatus(cancelledFinal.Job.Status) != JobStatusCancelled {
			t.Fatalf("cancelled ensemble_spawn status=%s, want cancelled (%+v)", cancelledFinal.Job.Status, cancelledFinal.Job)
		}
	})
}

func TestAsyncEnsembleSpawnCancellationStopsRunningTmuxWork(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("NTM_CONFIG", "")
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	tmuxLog := installFakeTmux(t)
	t.Setenv("NTM_JOB_RESTORE_BLOCK", "new-session")
	srv, closeServer := newJournalHTTPServer(t, filepath.Join(t.TempDir(), "jobs-ensemble-cancel.db"))
	srv.mu.Lock()
	srv.projectDir = t.TempDir()
	srv.mu.Unlock()

	const session = "cancel-running-ensemble"
	env := postJob(t, srv, "{\"type\":\"ensemble_spawn\",\"params\":{\"session\":\"cancel-running-ensemble\",\"question\":\"What should we investigate?\",\"modes\":[\"deductive\",\"bayesian\"],\"allow_advanced\":true}}")
	t.Cleanup(func() { _, _ = srv.cancelJob(env.Job.ID) })

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(os.Getenv("NTM_JOB_RESTORE_READY")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ensemble spawn did not reach new-session: %+v", srv.jobStore.Get(env.Job.ID))
		}
		time.Sleep(5 * time.Millisecond)
	}

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+env.Job.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE job status=%d body=%s", rec.Code, rec.Body.String())
	}

	deadline = time.Now().Add(5 * time.Second)
	for !srv.jobExecutor.idle() {
		if time.Now().After(deadline) {
			t.Fatal("cancellation changed the job row but did not stop the ensemble worker")
		}
		time.Sleep(5 * time.Millisecond)
	}
	final := pollJobTerminal(t, srv, env.Job.ID)
	if JobStatus(final.Job.Status) != JobStatusCancelled {
		t.Fatalf("cancelled ensemble status=%s error=%q", final.Job.Status, final.Job.Error)
	}
	if final.Job.Result["session"] != session {
		t.Fatalf("cancelled ensemble lost its partial result: %#v", final.Job.Result)
	}
	if final.Job.Result["_execution_in_progress"] == true {
		t.Fatalf("cancelled ensemble worker still appears active: %#v", final.Job.Result)
	}

	saved, err := ensemble.LoadSession(session)
	if err != nil {
		t.Fatalf("load interrupted ensemble: %v", err)
	}
	if saved.Error == "" || saved.Status == ensemble.EnsembleActive || saved.Status == ensemble.EnsembleSpawning {
		t.Fatalf("interrupted ensemble lost its failure state: %+v", saved)
	}

	closeServer()
	calls, err := os.ReadFile(tmuxLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(calls), "new-session") != 1 {
		t.Fatalf("expected exactly one attempted session creation, got:\n%s", calls)
	}
	for _, command := range []string{"split-window", "send-keys", "kill-session"} {
		if strings.Contains(string(calls), command) {
			t.Fatalf("cancelled ensemble continued with %s:\n%s", command, calls)
		}
	}
}

func TestEnsembleStopHTTPSavesRecoverableOutputsBeforeKill(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      ensemble.EnsembleStatus
		failPersist bool
	}{
		{name: "active ensemble", status: ensemble.EnsembleActive},
		{name: "live session after spawn failure", status: ensemble.EnsembleError},
		{name: "output persistence failure", status: ensemble.EnsembleActive, failPersist: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("NTM_CONFIG", "")
			ensemble.CloseDefaultStateStore()
			t.Cleanup(ensemble.CloseDefaultStateStore)

			const session = "durable-ensemble-stop"
			now := time.Now().UTC()
			modeOutput := ensemble.ModeOutput{
				ModeID: "deductive",
				Thesis: "Persist completed reasoning before stopping an ensemble.",
				TopFindings: []ensemble.Finding{
					{
						Finding:         "Stopping tmux destroys the only copy of unsaved agent output",
						Impact:          ensemble.ImpactHigh,
						Confidence:      0.95,
						EvidencePointer: "internal/robot/ensemble_stop.go",
						Reasoning:       "A durable snapshot must precede session destruction",
					},
				},
				Confidence:  0.95,
				GeneratedAt: now,
			}
			validJSON, err := json.Marshal(modeOutput)
			if err != nil {
				t.Fatal(err)
			}
			rawA := "Analysis before result\n```json\n" + string(validJSON) + "\n```"
			rawB := "Still investigating the second hypothesis; no final structured result yet."
			dir := t.TempDir()
			captureA, captureB := filepath.Join(dir, "capture-a.txt"), filepath.Join(dir, "capture-b.txt")
			if err := os.WriteFile(captureA, []byte(rawA+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(captureB, []byte(rawB+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			logPath, killedPath := filepath.Join(dir, "tmux.log"), filepath.Join(dir, "killed")
			t.Setenv("NTM_STOP_LOG", logPath)
			t.Setenv("NTM_STOP_KILLED", killedPath)
			t.Setenv("NTM_STOP_CAPTURE_A", captureA)
			t.Setenv("NTM_STOP_CAPTURE_B", captureB)
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_STOP_LOG"
case "$1" in
  -V) echo 'tmux 3.5a' ;;
  has-session) if [ -e "$NTM_STOP_KILLED" ]; then exit 1; fi ;;
  list-sessions) echo 'durable-ensemble-stop' ;;
  list-panes)
    printf '%s\n' '%1_NTM_SEP_0_NTM_SEP_durable-ensemble-stop__cc_1_NTM_SEP_claude_NTM_SEP_80_NTM_SEP_24_NTM_SEP_1_NTM_SEP_0_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0'
    printf '%s\n' '%2_NTM_SEP_1_NTM_SEP_durable-ensemble-stop__cod_1_NTM_SEP_codex_NTM_SEP_80_NTM_SEP_24_NTM_SEP_0_NTM_SEP_0_NTM_SEP_0_NTM_SEP_cod_NTM_SEP__NTM_SEP__NTM_SEP_0'
    ;;
  capture-pane)
    for arg in "$@"; do
      case "$arg" in
        %1) cat "$NTM_STOP_CAPTURE_A"; exit 0 ;;
        %2) cat "$NTM_STOP_CAPTURE_B"; exit 0 ;;
      esac
    done
    exit 1
    ;;
  kill-session) touch "$NTM_STOP_KILLED" ;;
esac
exit 0
`
			binPath := filepath.Join(dir, "tmux")
			if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NTM_TMUX_BINARY", binPath)
			oldClient := tmux.DefaultClient
			tmux.DefaultClient = tmux.NewClient("")
			t.Cleanup(func() { tmux.DefaultClient = oldClient })

			state := &ensemble.EnsembleSession{
				SessionName:       session,
				Question:          "Can stopped ensemble results be recovered?",
				Status:            tc.status,
				SynthesisStrategy: ensemble.StrategyConsensus,
				CreatedAt:         now,
				Assignments: []ensemble.ModeAssignment{
					{ModeID: "deductive", PaneName: session + "__cc_1", AgentType: "cc", Status: ensemble.AssignmentActive, AssignedAt: now},
					{ModeID: "bayesian", PaneName: session + "__cod_1", AgentType: "cod", Status: ensemble.AssignmentActive, AssignedAt: now},
				},
			}
			if err := ensemble.SaveSession(session, state); err != nil {
				t.Fatal(err)
			}
			if tc.failPersist {
				// A file where the output directory must be makes the failure
				// deterministic, including when tests run as root.
				blocker := filepath.Join(filepath.Dir(statepkg.DefaultPath()), "ensemble-outputs")
				if err := os.WriteFile(blocker, []byte("blocked"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			srv := NewHermeticServer("ensemble-stop-output-test")
			t.Cleanup(srv.Stop)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/ensemble/sessions/"+session+"/stop", strings.NewReader("{\"force\":true}"))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)

			if tc.failPersist {
				if rec.Code != http.StatusInternalServerError {
					t.Fatalf("persistence failure status=%d body=%s", rec.Code, rec.Body.String())
				}
				if _, err := os.Stat(killedPath); !os.IsNotExist(err) {
					t.Fatalf("session was killed after output persistence failed: %v", err)
				}
				saved, err := ensemble.LoadSession(session)
				if err != nil {
					t.Fatal(err)
				}
				if saved.Status != tc.status || saved.Assignments[0].OutputPath != "" || saved.Assignments[1].OutputPath != "" {
					t.Fatalf("failed preservation changed durable state: %+v", saved)
				}
				return
			}

			if rec.Code != http.StatusOK {
				t.Fatalf("stop status=%d body=%s", rec.Code, rec.Body.String())
			}
			var out robot.EnsembleStopOutput
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if !out.Success || out.Result.Stopped != 2 || out.Result.Captured != 2 || len(out.Result.SavedOutputPaths) != 2 {
				t.Fatalf("stop did not report both saved outputs: %+v", out)
			}
			if _, err := os.Stat(killedPath); err != nil {
				t.Fatalf("session was not killed: %v", err)
			}
			for i, want := range []string{rawA, rawB} {
				raw, err := os.ReadFile(out.Result.SavedOutputPaths[i])
				if err != nil || string(raw) != want {
					t.Fatalf("saved raw output %d = %q, err=%v, want %q", i, raw, err, want)
				}
			}

			ensemble.CloseDefaultStateStore()
			saved, err := ensemble.LoadSession(session)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Status != ensemble.EnsembleStopped || saved.Assignments[0].Status != ensemble.AssignmentDone || saved.Assignments[1].Status != ensemble.AssignmentError {
				t.Fatalf("reopened state lost completed or partial output status: %+v", saved)
			}
			collector, err := ensemble.CollectModeOutputs(saved, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(collector.Outputs) != 1 || collector.Outputs[0].Thesis != modeOutput.Thesis {
				t.Fatalf("saved completed output cannot be recovered: %+v", collector.Outputs)
			}
			calls, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(calls), "kill-session") != 1 || strings.Index(string(calls), "capture-pane") > strings.Index(string(calls), "kill-session") {
				t.Fatalf("outputs were not captured before one session kill:\n%s", calls)
			}
		})
	}
}

const standaloneHTTPModeOutput = "Analysis complete.\n" +
	"```yaml\nmode_id: deductive\nthesis: Preserve member reasoning before lead synthesis.\n" +
	"top_findings:\n  - finding: Durable capture must precede lead dispatch.\n" +
	"    impact: high\n    confidence: 0.9\n    evidence_pointer: internal/ensemble/capture.go\n" +
	"    reasoning: Sending the next task replaces the only live result.\nconfidence: 0.9\n```\nclaude>\n"

func setupStandaloneHTTPSynthesis(t *testing.T, output string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake tmux requires a POSIX shell")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("NTM_CONFIG", "")
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	const session = "standalone-http-synthesis"
	dir := t.TempDir()
	t.Setenv("NTM_SYNTH_ROOT", dir)
	t.Setenv("NTM_SYNTH_SESSION", session)
	t.Setenv("NTM_SYNTH_HOLD", "")
	t.Setenv("NTM_SYNTH_PANES", "%1_NTM_SEP_0_NTM_SEP_"+session+"__cc_1_NTM_SEP_claude_NTM_SEP_120_NTM_SEP_40_NTM_SEP_1_NTM_SEP_0_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[]")
	for name, content := range map[string]string{
		"member.txt": output,
		"lead.txt": "\n```json\n" +
			`{"summary":"Fresh synthesis from the live lead agent.","findings":[{"finding":"Save completed reasoning before dispatch.","impact":"high","confidence":0.94}],"confidence":0.94}` +
			"\n```\nclaude>\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
printf '%s\n' "$1" >> "$NTM_SYNTH_ROOT/tmux.log"
case "$1" in
  -V) printf 'tmux 3.5a\n' ;;
  has-session) ;;
  list-sessions) printf '%s_NTM_SEP_1_NTM_SEP_0_NTM_SEP_now\n' "$NTM_SYNTH_SESSION" ;;
  list-panes) printf '%s\n' "$NTM_SYNTH_PANES" ;;
  display-message)
    case "$*" in
      *session_name*) printf '%s\n' "$NTM_SYNTH_SESSION" ;;
      *rotation_token*) printf '[]\n' ;;
      *) exit 1 ;;
    esac
    ;;
  capture-pane)
    cat "$NTM_SYNTH_ROOT/member.txt"
    if [ -e "$NTM_SYNTH_ROOT/submitted" ]; then cat "$NTM_SYNTH_ROOT/lead.txt"; fi
    ;;
  load-buffer)
    cat > "$NTM_SYNTH_ROOT/prompt.txt"
    : > "$NTM_SYNTH_ROOT/before-prompt"
    if [ "$NTM_SYNTH_HOLD" = "1" ]; then
      while [ ! -e "$NTM_SYNTH_ROOT/release" ]; do sleep 0.01; done
    fi
    ;;
  paste-buffer|delete-buffer) ;;
  send-keys)
    case "$*" in
      *Enter*) : > "$NTM_SYNTH_ROOT/submitted" ;;
      *) exit 1 ;;
    esac
    ;;
  *) printf 'unexpected tmux command: %s\n' "$1" >&2; exit 1 ;;
esac
`
	binPath := filepath.Join(dir, "tmux")
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", binPath)
	oldClient := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = oldClient })
	now := time.Now().UTC()
	state := &ensemble.EnsembleSession{
		SessionName:       session,
		Question:          "How should completed ensemble reasoning be preserved?",
		Status:            ensemble.EnsembleActive,
		SynthesisStrategy: ensemble.StrategyConsensus,
		CreatedAt:         now,
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "deductive", PaneName: session + "__cc_1", AgentType: "cc", Status: ensemble.AssignmentActive, AssignedAt: now},
		},
	}
	if err := ensemble.SaveSession(session, state); err != nil {
		t.Fatal(err)
	}
	return session, dir
}

// This barrier is inside the real tmux buffer send. It proves that the API
// published the member's durable result before the next prompt is submitted.
func awaitStandaloneHTTPLead(t *testing.T, ctx context.Context, session, dir string, done <-chan struct{}) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(dir, "before-prompt")); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-done:
			t.Fatal("synthesis ended before reaching the lead dispatcher")
		case <-ctx.Done():
			t.Fatal("synthesis never reached the lead dispatcher")
		case <-ticker.C:
		}
	}
	saved, err := ensemble.LoadSession(session)
	if err != nil {
		t.Fatal(err)
	}
	assignment := saved.Assignments[0]
	if assignment.Status != ensemble.AssignmentDone || assignment.OutputPath == "" || assignment.CompletedAt == nil {
		t.Fatalf("member result was not durable before lead dispatch: %+v", assignment)
	}
	recovered, err := ensemble.CollectModeOutputs(saved, nil)
	if err != nil || len(recovered.Outputs) != 1 || recovered.Outputs[0].Thesis != "Preserve member reasoning before lead synthesis." {
		t.Fatalf("saved member result cannot be recovered before dispatch: %+v, err=%v", recovered, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "submitted")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lead prompt submitted before release: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStandaloneEnsembleSynthesisHTTPDispatchesLiveLeadWithoutForce(t *testing.T) {
	session, dir := setupStandaloneHTTPSynthesis(t, standaloneHTTPModeOutput)
	t.Setenv("NTM_SYNTH_HOLD", "1")
	srv := NewHermeticServer("standalone-synthesis-test")
	t.Cleanup(srv.Stop)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ensemble/sessions/"+session+"/synthesize",
		strings.NewReader(`{"output_format":"json"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Router().ServeHTTP(rec, req)
	}()
	t.Cleanup(func() {
		cancel()
		_ = os.WriteFile(filepath.Join(dir, "release"), nil, 0o600)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("synthesis request did not stop after cancellation")
		}
	})
	awaitStandaloneHTTPLead(t, ctx, session, dir, done)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("synthesis request did not finish after the lead reply")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("synthesize without force: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response EnsembleSynthesizeEnvelopeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Success || response.Result == nil || response.Result.SynthesizedBy != "agent" ||
		response.Result.FallbackReason != "" || response.Result.Summary != "Fresh synthesis from the live lead agent." {
		t.Fatalf("expected a fresh lead synthesis, got %+v", response)
	}
	prompt, err := os.ReadFile(filepath.Join(dir, "prompt.txt"))
	if err != nil || !strings.Contains(string(prompt), "How should completed ensemble reasoning be preserved?") ||
		!strings.Contains(string(prompt), "Durable capture must precede lead dispatch.") {
		t.Fatalf("lead did not receive the question and captured reasoning: %q, err=%v", prompt, err)
	}
}

func TestStandaloneEnsembleSynthesisAsyncJobUsesLiveLead(t *testing.T) {
	session, dir := setupStandaloneHTTPSynthesis(t, standaloneHTTPModeOutput)
	t.Setenv("NTM_SYNTH_HOLD", "1")
	srv, _ := newJournalHTTPServer(t, filepath.Join(t.TempDir(), "standalone-synthesis-jobs.db"))
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(func() {
		cancel()
		_ = os.WriteFile(filepath.Join(dir, "release"), nil, 0o600)
	})
	env := postJob(t, srv, `{"type":"ensemble_synthesize","session":"`+session+`","params":{"output_format":"json"}}`)
	awaitStandaloneHTTPLead(t, ctx, session, dir, nil)
	final := pollJobTerminal(t, srv, env.Job.ID)
	if JobStatus(final.Job.Status) != JobStatusCompleted || final.Job.Progress != 100 {
		t.Fatalf("live synthesis job did not complete: %+v", final.Job)
	}
	result, ok := final.Job.Result["result"].(map[string]interface{})
	if !ok || result["synthesized_by"] != "agent" || result["summary"] != "Fresh synthesis from the live lead agent." {
		t.Fatalf("async job did not retain the live agent result: %#v", final.Job.Result)
	}
	if reason, _ := result["fallback_reason"].(string); reason != "" {
		t.Fatalf("async job unexpectedly fell back: %s", reason)
	}
}

func TestStandaloneEnsembleSynthesisHTTPRefusesUnfinishedOutput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
	}{
		{name: "complete schema while still busy", output: strings.TrimSuffix(standaloneHTTPModeOutput, "claude>\n") + "Continuing analysis...\n"},
		{name: "idle with incomplete output", output: "Partial analysis with no structured result.\nclaude>\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, dir := setupStandaloneHTTPSynthesis(t, tc.output)
			srv := NewHermeticServer("unfinished-synthesis-test")
			t.Cleanup(srv.Stop)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/ensemble/sessions/"+session+"/synthesize",
				strings.NewReader(`{"output_format":"json"}`)).WithContext(t.Context())
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)
			var response APIError
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusConflict || response.Success || response.ErrorCode != robot.ErrCodeSynthesisNotReady {
				t.Fatalf("unfinished result was not rejected: status=%d body=%s", rec.Code, rec.Body.String())
			}
			saved, err := ensemble.LoadSession(session)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Assignments[0].Status != ensemble.AssignmentActive || saved.Assignments[0].OutputPath != "" {
				t.Fatalf("unfinished result was promoted: %+v", saved.Assignments[0])
			}
			if _, err := os.Stat(filepath.Join(dir, "prompt.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unfinished member received a synthesis prompt: %v", err)
			}
		})
	}
}

func TestStandaloneEnsembleSynthesisHTTPCancellationDoesNotDispatch(t *testing.T) {
	session, dir := setupStandaloneHTTPSynthesis(t, standaloneHTTPModeOutput)
	srv := NewHermeticServer("canceled-synthesis-test")
	t.Cleanup(srv.Stop)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ensemble/sessions/"+session+"/synthesize",
		strings.NewReader(`{"output_format":"json"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code < http.StatusBadRequest || !strings.Contains(rec.Body.String(), context.Canceled.Error()) {
		t.Fatalf("canceled request reported success: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "prompt.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled request dispatched a prompt: %v", err)
	}
	saved, err := ensemble.LoadSession(session)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Assignments[0].Status != ensemble.AssignmentActive {
		t.Fatalf("canceled request changed assignment state: %+v", saved.Assignments[0])
	}
}

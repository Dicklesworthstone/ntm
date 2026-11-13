package serve

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
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

package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/workflow"
)

func TestPersistenceWorkflowsOpenAPISchemaContracts(t *testing.T) {
	spec, err := GenerateOpenAPISpecFromRouter("1.39.0-wave15", "http://127.0.0.1:7337")
	if err != nil {
		t.Fatalf("GenerateOpenAPISpecFromRouter: %v", err)
	}

	checks := []struct {
		method  string
		path    string
		wantRef string
		wantReq string
	}{
		{http.MethodGet, "/api/v1/saved-sessions", "#/components/schemas/SavedSessionsEnvelopeResponse", ""},
		{http.MethodPost, "/api/v1/saved-sessions", "#/components/schemas/SavedSessionsEnvelopeResponse", "#/components/schemas/SavedSessionSaveRequest"},
		{http.MethodGet, "/api/v1/saved-sessions/{name}", "#/components/schemas/SavedSessionsEnvelopeResponse", ""},
		{http.MethodPost, "/api/v1/saved-sessions/{name}/archive", "#/components/schemas/SavedSessionsEnvelopeResponse", ""},
		{http.MethodPost, "/api/v1/saved-sessions/{name}/unarchive", "#/components/schemas/SavedSessionsEnvelopeResponse", ""},
		{http.MethodPost, "/api/v1/saved-sessions/{name}/restore", "#/components/schemas/SavedSessionRestoreEnvelopeResponse", "#/components/schemas/SavedSessionRestoreRequest"},
		{http.MethodGet, "/api/v1/sessions/{id}/mapping", "#/components/schemas/SessionMappingEnvelopeResponse", ""},
		{http.MethodGet, "/api/v1/workflows", "#/components/schemas/WorkflowsEnvelopeResponse", ""},
		{http.MethodGet, "/api/v1/workflows/{name}", "#/components/schemas/WorkflowsEnvelopeResponse", ""},
		{http.MethodPost, "/api/v1/workflows/{name}/run", "#/components/schemas/WorkflowControlEnvelopeResponse", "#/components/schemas/WorkflowRunRequest"},
		{http.MethodPost, "/api/v1/workflows/{name}/trigger", "#/components/schemas/WorkflowControlEnvelopeResponse", "#/components/schemas/WorkflowTriggerRequest"},
		{http.MethodPost, "/api/v1/workflows/{name}/stop", "#/components/schemas/WorkflowControlEnvelopeResponse", "#/components/schemas/WorkflowStopRequest"},
		{http.MethodPost, "/api/v1/sequences", "#/components/schemas/SequenceEnvelopeResponse", "#/components/schemas/SequenceCreateRequest"},
		{http.MethodGet, "/api/v1/sequences/{name}/next", "#/components/schemas/SequenceEnvelopeResponse", ""},
		{http.MethodPost, "/api/v1/sequences/{name}/advance", "#/components/schemas/SequenceEnvelopeResponse", "#/components/schemas/SequenceAdvanceRequest"},
		{http.MethodGet, "/api/v1/session-templates", "#/components/schemas/SessionTemplatesEnvelopeResponse", ""},
		{http.MethodGet, "/api/v1/session-templates/{name}", "#/components/schemas/SessionTemplatesEnvelopeResponse", ""},
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

func TestPersistenceWorkflowsRESTEndpointsEndToEnd(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".ntm", "templates"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.ntm/templates): %v", err)
	}
	templateYAML := `apiVersion: v1
kind: SessionTemplate
metadata:
  name: wave15-rest-template
  description: REST API template inspection test
spec:
  agents:
    claude:
      count: 2
    antigravity:
      count: 1
`
	if err := os.WriteFile(filepath.Join(repoDir, ".ntm", "templates", "wave15-rest-template.yaml"), []byte(templateYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(template): %v", err)
	}

	// Seed a saved session snapshot with 11-harness counts and fresh casr binding
	snapshot := &session.SessionState{
		Name:      "wave15_saved",
		SavedAt:   time.Now().UTC().Truncate(time.Second),
		WorkDir:   repoDir,
		GitBranch: "main",
		Layout:    "tiled",
		Version:   session.StateVersion,
		Agents: session.AgentConfig{
			Claude:      2,
			Antigravity: 1,
			Opencode:    1,
			User:        1,
		},
		Panes: []session.PaneState{
			{
				Title:             "wave15_saved__cc_1",
				Index:             0,
				AgentType:         "cc",
				SessionID:         "claude-rest-binding-1",
				SessionProvider:   "claude",
				SessionFreshness:  agentsession.BindingFresh,
				SessionConfidence: 0.96,
			},
		},
	}
	if _, err := session.Save(snapshot, session.SaveOptions{Name: "wave15-rest-snap", Overwrite: true}); err != nil {
		t.Fatalf("session.Save: %v", err)
	}

	srv := NewHermeticServer("1.39.0-wave15")
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

	// 1. Saved-sessions list -> show -> archive -> archived list -> unarchive -> missing 404
	listRec := doReq(http.MethodGet, "/api/v1/saved-sessions", "")
	if statusOf(listRec) != http.StatusOK {
		t.Fatalf("GET /api/v1/saved-sessions status = %d, body = %s", statusOf(listRec), listRec.Body.String())
	}
	var listResp SavedSessionsEnvelopeResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode saved-sessions list: %v", err)
	}
	if !listResp.Success || listResp.Count == nil || *listResp.Count != 1 || listResp.Sessions[0].Agents != 5 {
		t.Fatalf("unexpected saved-sessions list: %+v", listResp)
	}

	showRec := doReq(http.MethodGet, "/api/v1/saved-sessions/wave15-rest-snap", "")
	if statusOf(showRec) != http.StatusOK {
		t.Fatalf("GET /api/v1/saved-sessions/wave15-rest-snap status = %d, body = %s", statusOf(showRec), showRec.Body.String())
	}
	var showResp SavedSessionsEnvelopeResponse
	if err := json.Unmarshal(showRec.Body.Bytes(), &showResp); err != nil {
		t.Fatalf("decode saved-session show: %v", err)
	}
	if !showResp.Success || showResp.ResumablePanes == nil || *showResp.ResumablePanes != 1 || showResp.State == nil {
		t.Fatalf("unexpected saved-session show: %+v", showResp)
	}

	archRec := doReq(http.MethodPost, "/api/v1/saved-sessions/wave15-rest-snap/archive", "")
	if statusOf(archRec) != http.StatusOK {
		t.Fatalf("POST archive status = %d, body = %s", statusOf(archRec), archRec.Body.String())
	}

	archListRec := doReq(http.MethodGet, "/api/v1/saved-sessions?archived=true", "")
	if statusOf(archListRec) != http.StatusOK {
		t.Fatalf("GET archived saved-sessions status = %d, body = %s", statusOf(archListRec), archListRec.Body.String())
	}

	unarchRec := doReq(http.MethodPost, "/api/v1/saved-sessions/wave15-rest-snap/unarchive", "")
	if statusOf(unarchRec) != http.StatusOK {
		t.Fatalf("POST unarchive status = %d, body = %s", statusOf(unarchRec), unarchRec.Body.String())
	}

	missingSavedRec := doReq(http.MethodGet, "/api/v1/saved-sessions/missing-snap-xyz", "")
	if statusOf(missingSavedRec) != http.StatusNotFound {
		t.Fatalf("expected 404 for missing saved session, got %d (%s)", statusOf(missingSavedRec), missingSavedRec.Body.String())
	}

	saveMissingLiveRec := doReq(http.MethodPost, "/api/v1/saved-sessions", `{"session":"missing_live_tmux_session_99999"}`)
	if statusOf(saveMissingLiveRec) != http.StatusNotFound && statusOf(saveMissingLiveRec) != http.StatusInternalServerError {
		t.Fatalf("expected 404/500 saving nonexistent live session, got %d (%s)", statusOf(saveMissingLiveRec), saveMissingLiveRec.Body.String())
	}

	// 2. Session mapping 404 for missing session
	mapMissingRec := doReq(http.MethodGet, "/api/v1/sessions/missing_mapping_session_99999/mapping", "")
	if statusOf(mapMissingRec) != http.StatusNotFound {
		t.Fatalf("expected 404 for missing session mapping, got %d (%s)", statusOf(mapMissingRec), mapMissingRec.Body.String())
	}

	// 3. Workflows list, show with trigger validation, and 404 on unknown workflow
	wfListRec := doReq(http.MethodGet, "/api/v1/workflows", "")
	if statusOf(wfListRec) != http.StatusOK {
		t.Fatalf("GET /api/v1/workflows status = %d, body = %s", statusOf(wfListRec), wfListRec.Body.String())
	}
	var wfListResp WorkflowsEnvelopeResponse
	if err := json.Unmarshal(wfListRec.Body.Bytes(), &wfListResp); err != nil || !wfListResp.Success || wfListResp.Total == nil || *wfListResp.Total == 0 {
		t.Fatalf("unexpected workflows list: %+v (%v)", wfListResp, err)
	}

	wfShowRec := doReq(http.MethodGet, "/api/v1/workflows/red-green", "")
	if statusOf(wfShowRec) != http.StatusOK {
		t.Fatalf("GET /api/v1/workflows/red-green status = %d, body = %s", statusOf(wfShowRec), wfShowRec.Body.String())
	}
	var wfShowResp WorkflowsEnvelopeResponse
	if err := json.Unmarshal(wfShowRec.Body.Bytes(), &wfShowResp); err != nil || wfShowResp.Workflow == nil || strings.Compare(wfShowResp.Workflow.Name, "red-green") != 0 {
		t.Fatalf("unexpected workflow show: %+v (%v)", wfShowResp, err)
	}

	wfMissingRec := doReq(http.MethodGet, "/api/v1/workflows/missing-wf-xyz", "")
	if statusOf(wfMissingRec) != http.StatusNotFound {
		t.Fatalf("expected 404 for missing workflow, got %d", statusOf(wfMissingRec))
	}

	// 3b. Saved-session restore validation (404 on missing snapshot, 400 on invalid target_name)
	restoreMissingRec := doReq(http.MethodPost, "/api/v1/saved-sessions/missing-snap-xyz/restore", `{}`)
	if statusOf(restoreMissingRec) != http.StatusNotFound {
		t.Fatalf("expected 404 for missing saved-session restore, got %d (%s)", statusOf(restoreMissingRec), restoreMissingRec.Body.String())
	}
	restoreBadTargetRec := doReq(http.MethodPost, "/api/v1/saved-sessions/wave15-rest-snap/restore", `{"target_name":"bad:name"}`)
	if statusOf(restoreBadTargetRec) != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid target_name restore, got %d (%s)", statusOf(restoreBadTargetRec), restoreBadTargetRec.Body.String())
	}

	// 3c. Workflow run (dry-run + real checkpoint with manual gate) -> trigger -> stop lifecycle
	if err := os.MkdirAll(filepath.Join(repoDir, ".ntm", "workflows"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.ntm/workflows): %v", err)
	}
	manualWfTOML := `name = "rest-manual-gate"
description = "REST workflow control E2E"
coordination = "pipeline"

[[agents]]
profile = "build"
role = "build"

[flow]
initial = "review"
stages = ["review", "ship"]

[[flow.transitions]]
from = "review"
to = "ship"
[flow.transitions.trigger]
type = "manual"
label = "Approve release"
`
	if err := os.WriteFile(filepath.Join(repoDir, ".ntm", "workflows", "rest-manual-gate.toml"), []byte(manualWfTOML), 0o644); err != nil {
		t.Fatalf("WriteFile(rest-manual-gate.toml): %v", err)
	}

	wfDryRunRec := doReq(http.MethodPost, "/api/v1/workflows/rest-manual-gate/run", `{"session":"wf_rest_sess","dry_run":true,"project_dir":`+strconvQuote(repoDir)+`}`)
	if statusOf(wfDryRunRec) != http.StatusOK {
		t.Fatalf("POST /api/v1/workflows/rest-manual-gate/run dry_run = %d, body = %s", statusOf(wfDryRunRec), wfDryRunRec.Body.String())
	}
	var wfDryRunResp WorkflowControlEnvelopeResponse
	if err := json.Unmarshal(wfDryRunRec.Body.Bytes(), &wfDryRunResp); err != nil || !wfDryRunResp.DryRun || wfDryRunResp.Stage != "review" {
		t.Fatalf("unexpected dry_run response: %+v (%v)", wfDryRunResp, err)
	}

	wfRunRec := doReq(http.MethodPost, "/api/v1/workflows/rest-manual-gate/run", `{"session":"wf_rest_sess","project_dir":`+strconvQuote(repoDir)+`}`)
	if statusOf(wfRunRec) != http.StatusOK {
		t.Fatalf("POST /api/v1/workflows/rest-manual-gate/run = %d, body = %s", statusOf(wfRunRec), wfRunRec.Body.String())
	}
	var wfRunResp WorkflowControlEnvelopeResponse
	if err := json.Unmarshal(wfRunRec.Body.Bytes(), &wfRunResp); err != nil || wfRunResp.CheckpointToken == "" || wfRunResp.State == nil || wfRunResp.State.TemplateHash == "" {
		t.Fatalf("expected saved checkpoint with template hash from workflow run, got %+v (%v)", wfRunResp, err)
	}

	// Mark stage deliveries completed on the durable checkpoint (preserving the
	// delivery-journal safety invariant) so RefreshManualGate publishes a gate token.
	wfStore := workflowStateStoreForProject(repoDir)
	wfLoader := workflow.NewLoader()
	wfLoader.ProjectDir = repoDir
	wfTmpl, err := wfLoader.Get("rest-manual-gate")
	if err != nil {
		t.Fatalf("reload rest-manual-gate: %v", err)
	}
	loadedWfState, err := wfStore.Load("wf_rest_sess")
	if err != nil || loadedWfState == nil {
		t.Fatalf("load wf_rest_sess checkpoint: %+v (%v)", loadedWfState, err)
	}
	loadedWfState.Agents = map[string]string{"%1": "build"}
	loadedWfState.PanePIDs = map[string]int{"%1": 1001}
	loadedWfState.Evidence = &workflow.StageEvidence{
		Version:   1,
		Stage:     loadedWfState.CurrentStage,
		StartedAt: loadedWfState.StageStartedAt,
		Round:     loadedWfState.Turn,
		Panes: map[string]workflow.StagePaneEvidence{
			"%1": {PID: 1001},
		},
	}
	loadedWfState.Dispatches = []workflow.StageDispatch{{Pane: "%1", Role: "build", Turn: 1, Status: "delivered"}}
	if err := loadedWfState.RefreshManualGate(wfTmpl); err != nil {
		t.Fatalf("RefreshManualGate: %v", err)
	}
	if err := wfStore.Save(loadedWfState); err != nil {
		t.Fatalf("Save manual gate checkpoint: %v", err)
	}
	if loadedWfState.ManualGate == nil || loadedWfState.ManualGate.Token == "" {
		t.Fatalf("expected published manual gate token, got %+v", loadedWfState.ManualGate)
	}

	wfTrigRec := doReq(http.MethodPost, "/api/v1/workflows/wf_rest_sess/trigger", `{"token":`+strconvQuote(loadedWfState.ManualGate.Token)+`,"transition":0,"reason":"Approved via REST","project_dir":`+strconvQuote(repoDir)+`}`)
	if statusOf(wfTrigRec) != http.StatusOK {
		t.Fatalf("POST /api/v1/workflows/wf_rest_sess/trigger = %d, body = %s", statusOf(wfTrigRec), wfTrigRec.Body.String())
	}
	var wfTrigResp WorkflowControlEnvelopeResponse
	if err := json.Unmarshal(wfTrigRec.Body.Bytes(), &wfTrigResp); err != nil || wfTrigResp.Decision == nil || wfTrigResp.Decision.To != "ship" {
		t.Fatalf("unexpected trigger response: %+v (%v)", wfTrigResp, err)
	}

	wfStopRec := doReq(http.MethodPost, "/api/v1/workflows/wf_rest_sess/stop", `{"reason":"Paused by operator","project_dir":`+strconvQuote(repoDir)+`}`)
	if statusOf(wfStopRec) != http.StatusOK {
		t.Fatalf("POST /api/v1/workflows/wf_rest_sess/stop = %d, body = %s", statusOf(wfStopRec), wfStopRec.Body.String())
	}
	var wfStopResp WorkflowControlEnvelopeResponse
	if err := json.Unmarshal(wfStopRec.Body.Bytes(), &wfStopResp); err != nil || wfStopResp.State == nil || !wfStopResp.State.Paused {
		t.Fatalf("expected paused state after stop, got %+v (%v)", wfStopResp, err)
	}

	wfStopMissingRec := doReq(http.MethodPost, "/api/v1/workflows/no_such_wf_session/stop", `{"project_dir":`+strconvQuote(repoDir)+`}`)
	if statusOf(wfStopMissingRec) != http.StatusNotFound {
		t.Fatalf("expected 404 stopping missing workflow session, got %d (%s)", statusOf(wfStopMissingRec), wfStopMissingRec.Body.String())
	}

	// 4. Sequences create -> next -> advance (expected_position=0) -> idempotent retry -> 409 conflict on future expected_position
	qDir := url.QueryEscape(repoDir)
	seqCreateRec := doReq(http.MethodPost, "/api/v1/sequences", `{"name":"review-seq","steps":["inspect","challenge","summarize"],"project_dir":`+strconvQuote(repoDir)+`}`)
	if statusOf(seqCreateRec) != http.StatusOK {
		t.Fatalf("POST /api/v1/sequences status = %d, body = %s", statusOf(seqCreateRec), seqCreateRec.Body.String())
	}

	seqNextRec := doReq(http.MethodGet, "/api/v1/sequences/review-seq/next?pane=%251&project_dir="+qDir, "")
	if statusOf(seqNextRec) != http.StatusOK {
		t.Fatalf("GET /api/v1/sequences/review-seq/next status = %d, body = %s", statusOf(seqNextRec), seqNextRec.Body.String())
	}
	var seqNextResp SequenceEnvelopeResponse
	if err := json.Unmarshal(seqNextRec.Body.Bytes(), &seqNextResp); err != nil || seqNextResp.Pane == nil || seqNextResp.Pane.Position != 0 {
		t.Fatalf("unexpected sequence next: %+v (%v)", seqNextResp, err)
	}

	seqAdvRec := doReq(http.MethodPost, "/api/v1/sequences/review-seq/advance", `{"pane":"%1","expected_position":0,"project_dir":`+strconvQuote(repoDir)+`}`)
	if statusOf(seqAdvRec) != http.StatusOK {
		t.Fatalf("POST /api/v1/sequences/review-seq/advance status = %d, body = %s", statusOf(seqAdvRec), seqAdvRec.Body.String())
	}
	var seqAdvResp SequenceEnvelopeResponse
	if err := json.Unmarshal(seqAdvRec.Body.Bytes(), &seqAdvResp); err != nil || seqAdvResp.Pane == nil || !seqAdvResp.Pane.Advanced || seqAdvResp.Pane.Position != 1 {
		t.Fatalf("unexpected sequence advance: %+v (%v)", seqAdvResp, err)
	}

	seqConflictRec := doReq(http.MethodPost, "/api/v1/sequences/review-seq/advance", `{"pane":"%1","expected_position":2,"project_dir":`+strconvQuote(repoDir)+`}`)
	if statusOf(seqConflictRec) != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for out-of-order expected_position, got %d (%s)", statusOf(seqConflictRec), seqConflictRec.Body.String())
	}

	// 5. Session templates list, custom project template show, and 404 on unknown template
	stListRec := doReq(http.MethodGet, "/api/v1/session-templates?project_dir="+qDir, "")
	if statusOf(stListRec) != http.StatusOK {
		t.Fatalf("GET /api/v1/session-templates status = %d, body = %s", statusOf(stListRec), stListRec.Body.String())
	}
	stShowRec := doReq(http.MethodGet, "/api/v1/session-templates/wave15-rest-template?project_dir="+qDir, "")
	if statusOf(stShowRec) != http.StatusOK {
		t.Fatalf("GET /api/v1/session-templates/wave15-rest-template status = %d, body = %s", statusOf(stShowRec), stShowRec.Body.String())
	}
	var stShowResp SessionTemplatesEnvelopeResponse
	if err := json.Unmarshal(stShowRec.Body.Bytes(), &stShowResp); err != nil || stShowResp.Template == nil || stShowResp.Template.Agents == nil || stShowResp.Template.Agents.Antigravity == nil {
		t.Fatalf("unexpected session-template show: %+v (%v)", stShowResp, err)
	}
	stMissingRec := doReq(http.MethodGet, "/api/v1/session-templates/missing-tmpl-xyz?project_dir="+qDir, "")
	if statusOf(stMissingRec) != http.StatusNotFound {
		t.Fatalf("expected 404 for missing session template, got %d", statusOf(stMissingRec))
	}
}

func strconvQuote(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

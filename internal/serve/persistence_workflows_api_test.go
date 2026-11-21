package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
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
		{http.MethodGet, "/api/v1/workflows/{name}/status", "#/components/schemas/WorkflowControlEnvelopeResponse", ""},
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

	// Non-dry-run against a nonexistent live tmux session must fail closed (404
	// SESSION_NOT_FOUND) without creating a fabricated checkpoint on disk.
	wfMissingSessRec := doReq(http.MethodPost, "/api/v1/workflows/rest-manual-gate/run", `{"session":"wf_rest_sess","project_dir":`+strconvQuote(repoDir)+`}`)
	if statusOf(wfMissingSessRec) != http.StatusNotFound {
		t.Fatalf("POST /api/v1/workflows/rest-manual-gate/run for nonexistent session = %d, want 404, body = %s", statusOf(wfMissingSessRec), wfMissingSessRec.Body.String())
	}
	qDir := url.QueryEscape(repoDir)
	wfMissingStatusRec := doReq(http.MethodGet, "/api/v1/workflows/wf_rest_sess/status?project_dir="+qDir, "")
	if statusOf(wfMissingStatusRec) != http.StatusNotFound {
		t.Fatalf("GET /api/v1/workflows/wf_rest_sess/status without run/checkpoint = %d, want 404, body = %s", statusOf(wfMissingStatusRec), wfMissingStatusRec.Body.String())
	}

	// Offline durable checkpoint status -> manual trigger -> stop lifecycle
	// (e.g. pausing/approving a CLI-started or prior-server checkpoint).
	wfStore := workflowStateStoreForProject(repoDir)
	wfLoader := workflow.NewLoader()
	wfLoader.ProjectDir = repoDir
	wfTmpl, err := wfLoader.Get("rest-manual-gate")
	if err != nil {
		t.Fatalf("reload rest-manual-gate: %v", err)
	}
	tmplHash, err := wfTmpl.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	offlineWfState := &workflow.WorkflowState{
		WorkflowName:   wfTmpl.Name,
		SessionName:    "wf_rest_sess",
		CurrentStage:   "review",
		StageStartedAt: now,
		Agents:         map[string]string{"%1": "build"},
		PanePIDs:       map[string]int{"%1": 1001},
		Variables:      map[string]string{},
		ResumeVersion:  1,
		TemplateHash:   tmplHash,
		ProjectRoot:    repoDir,
		TmuxEndpoint:   "local",
		Turn:           1,
		Evidence: &workflow.StageEvidence{
			Version:   1,
			Stage:     "review",
			StartedAt: now,
			Round:     1,
			Panes:     map[string]workflow.StagePaneEvidence{"%1": {PID: 1001, LastActivity: now}},
		},
		Dispatches: []workflow.StageDispatch{{Pane: "%1", Role: "build", Turn: 1, Status: "delivered"}},
	}
	if err := offlineWfState.RefreshManualGate(wfTmpl); err != nil {
		t.Fatalf("RefreshManualGate: %v", err)
	}
	if err := wfStore.Save(offlineWfState); err != nil {
		t.Fatalf("Save offline checkpoint: %v", err)
	}
	if offlineWfState.ManualGate == nil || offlineWfState.ManualGate.Token == "" {
		t.Fatalf("expected published manual gate token, got %+v", offlineWfState.ManualGate)
	}

	wfStatusRec := doReq(http.MethodGet, "/api/v1/workflows/wf_rest_sess/status?project_dir="+qDir, "")
	if statusOf(wfStatusRec) != http.StatusOK {
		t.Fatalf("GET /api/v1/workflows/wf_rest_sess/status = %d, body = %s", statusOf(wfStatusRec), wfStatusRec.Body.String())
	}
	var wfStatusResp WorkflowControlEnvelopeResponse
	if err := json.Unmarshal(wfStatusRec.Body.Bytes(), &wfStatusResp); err != nil || wfStatusResp.ManualGate == nil || wfStatusResp.ManualGate.Token != offlineWfState.ManualGate.Token || wfStatusResp.Running {
		t.Fatalf("unexpected offline workflow status: %+v (%v)", wfStatusResp, err)
	}

	wfTrigRec := doReq(http.MethodPost, "/api/v1/workflows/wf_rest_sess/trigger", `{"token":`+strconvQuote(offlineWfState.ManualGate.Token)+`,"transition":0,"reason":"Approved via REST","project_dir":`+strconvQuote(repoDir)+`}`)
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

type workflowProductionTmuxFixture struct {
	root          string
	session       string
	callsPath     string
	deliveredPath string
}

func setupWorkflowProductionTmuxFixture(t *testing.T, sessionName string) *workflowProductionTmuxFixture {
	t.Helper()
	root := t.TempDir()
	callsPath := filepath.Join(root, "tmux-calls.log")
	deliveredPath := filepath.Join(root, "deliveries.log")
	pid := os.Getpid()
	// Exact 14-field list-panes output matching tmux.GetPanesContext.
	pane1 := fmt.Sprintf("%%1_NTM_SEP_1_NTM_SEP_%s__cc_1_NTM_SEP_claude_NTM_SEP_120_NTM_SEP_40_NTM_SEP_1_NTM_SEP_%d_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[]", sessionName, pid)
	pane2 := fmt.Sprintf("%%2_NTM_SEP_2_NTM_SEP_%s__cc_2_NTM_SEP_claude_NTM_SEP_120_NTM_SEP_40_NTM_SEP_0_NTM_SEP_%d_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[]", sessionName, pid)

	script := fmt.Sprintf(`#!/bin/sh
set -eu
ROOT=%q
EXPECTED_SESS=%q
printf '%%s\n' "$*" >> "$ROOT/tmux-calls.log"
cmd="$1"
shift
case "$cmd" in
  -V)
    echo 'tmux 3.4'
    ;;
  has-session)
    target=""
    while [ "$#" -gt 0 ]; do
      case "$1" in
        -t) target="$2"; shift 2 ;;
        *) shift ;;
      esac
    done
    if [ "$target" != "=$EXPECTED_SESS" ] && [ "$target" != "$EXPECTED_SESS" ]; then
      echo "can't find session: $target" >&2
      exit 1
    fi
    ;;
  list-panes)
    target=""
    while [ "$#" -gt 0 ]; do
      case "$1" in
        -t) target="$2"; shift 2 ;;
        -F) shift 2 ;;
        *) shift ;;
      esac
    done
    if [ "$target" != "=$EXPECTED_SESS" ]; then
      echo "can't find session: $target" >&2
      exit 1
    fi
    printf '%%s\n' %q
    printf '%%s\n' %q
    ;;
  display-message)
    fmt=""
    while [ "$#" -gt 0 ]; do
      case "$1" in
        -t) shift 2 ;;
        -p) shift ;;
        *) fmt="$1"; shift ;;
      esac
    done
    case "$fmt" in
      *ntm_rotation_token*) printf '[]\n' ;;
      *) printf '%%s\n' "$ROOT" ;;
    esac
    ;;
  capture-pane)
    printf 'Claude Code ready\n❯ \n'
    ;;
  load-buffer)
    buf="default"
    src=""
    while [ "$#" -gt 0 ]; do
      case "$1" in
        -b) buf="$2"; shift 2 ;;
        *) src="$1"; shift ;;
      esac
    done
    if [ "$src" = "-" ] || [ -z "$src" ]; then
      cat > "$ROOT/buf-$buf"
    else
      cp "$src" "$ROOT/buf-$buf"
    fi
    ;;
  paste-buffer)
    buf="default"
    target=""
    while [ "$#" -gt 0 ]; do
      case "$1" in
        -b) buf="$2"; shift 2 ;;
        -t) target="$2"; shift 2 ;;
        *) shift ;;
      esac
    done
    printf '===DELIVERY pane=%%s===\n' "$target" >> "$ROOT/deliveries.log"
    if [ -f "$ROOT/buf-$buf" ]; then
      cat "$ROOT/buf-$buf" >> "$ROOT/deliveries.log"
      printf '\n' >> "$ROOT/deliveries.log"
    fi
    ;;
  send-keys)
    target=""
    literal=0
    text=""
    while [ "$#" -gt 0 ]; do
      case "$1" in
        -t) target="$2"; shift 2 ;;
        -l) literal=1; shift ;;
        --) shift; text="$*"; break ;;
        *) text="$1"; shift ;;
      esac
    done
    if [ "$literal" -eq 1 ] && [ -n "$text" ]; then
      printf '===DELIVERY pane=%%s===\n%%s\n' "$target" "$text" >> "$ROOT/deliveries.log"
    fi
    ;;
  delete-buffer)
    ;;
esac
`, root, sessionName, pane1, pane2)

	binPath := filepath.Join(root, "fake-tmux")
	if err := os.WriteFile(binPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("NTM_TMUX_BINARY", binPath)
	oldClient := tmux.DefaultClient
	fastClient := tmux.NewClient("")
	fastClient.MaxSendDelay = 2 * time.Millisecond
	tmux.DefaultClient = fastClient
	t.Cleanup(func() {
		tmux.DefaultClient = oldClient
	})
	return &workflowProductionTmuxFixture{
		root:          root,
		session:       sessionName,
		callsPath:     callsPath,
		deliveredPath: deliveredPath,
	}
}

func (f *workflowProductionTmuxFixture) deliveries() string {
	data, _ := os.ReadFile(f.deliveredPath)
	return string(data)
}

func (f *workflowProductionTmuxFixture) callCount() int {
	data, err := os.ReadFile(f.callsPath)
	if err != nil || len(data) == 0 {
		return 0
	}
	return strings.Count(string(data), "\n")
}

func writeWorkflowFixtureTOML(t *testing.T, repoDir, filename, content string) {
	t.Helper()
	wfDir := filepath.Join(repoDir, ".ntm", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(.ntm/workflows): %v", err)
	}
	if err := os.WriteFile(filepath.Join(wfDir, filename), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", filename, err)
	}
}

func waitWorkflowStatusCondition(
	t *testing.T,
	doReq func(method, path, body string) *httptest.ResponseRecorder,
	sessionName, projectDir string,
	timeout time.Duration,
	cond func(WorkflowControlEnvelopeResponse) bool,
) WorkflowControlEnvelopeResponse {
	t.Helper()
	qDir := url.QueryEscape(projectDir)
	deadline := time.Now().Add(timeout)
	var last WorkflowControlEnvelopeResponse
	var lastStatus int
	var lastBody string
	for time.Now().Before(deadline) {
		rec := doReq(http.MethodGet, "/api/v1/workflows/"+sessionName+"/status?project_dir="+qDir, "")
		lastStatus = rec.Result().StatusCode
		lastBody = rec.Body.String()
		if lastStatus == http.StatusOK {
			var env WorkflowControlEnvelopeResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err == nil {
				last = env
				if cond(env) {
					return env
				}
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for workflow status condition on %s (last HTTP %d): %+v (body=%s)", sessionName, lastStatus, last, lastBody)
	return WorkflowControlEnvelopeResponse{}
}

// TestWorkflowsRESTProductionTransportManualGateAndCompletion proves that
// POST /api/v1/workflows/{name}/run launches a real managed job running the
// production workflow coordinator, dispatches stage prompts through the real
// dispatchsvc -> TMUXDeliverer -> tmux transport, exposes live manual gates on
// GET /api/v1/workflows/{session}/status, advances live stages when
// POST /api/v1/workflows/{session}/trigger is called, and completes cleanly.
func TestWorkflowsRESTProductionTransportManualGateAndCompletion(t *testing.T) {
	fx := setupWorkflowProductionTmuxFixture(t, "wf_prod_live")
	repoDir := t.TempDir()
	writeWorkflowFixtureTOML(t, repoDir, "prod-manual-flow.toml", `name = "prod-manual-flow"
description = "Two-stage production REST workflow"
coordination = "pipeline"

[[agents]]
profile = "builder"
role = "build"

[[agents]]
profile = "reviewer"
role = "review"

[[prompts]]
key = "feature"
question = "Feature to implement?"
required = true

[flow]
initial = "build"
stages = ["build", "review", "done"]

[[flow.transitions]]
from = "build"
to = "review"
[flow.transitions.trigger]
type = "manual"
label = "Approve build"

[[flow.transitions]]
from = "review"
to = "done"
[flow.transitions.trigger]
type = "manual"
label = "Ship release"
`)

	srv := NewHermeticServer("1.39.0-bd-mu7qd")
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

	// Exact session check: a prefix of the live session must NOT match.
	prefixRec := doReq(http.MethodPost, "/api/v1/workflows/prod-manual-flow/run",
		`{"session":"wf_prod","project_dir":`+strconvQuote(repoDir)+`,"variables":{"feature":"rest-live"}}`)
	if prefixRec.Code != http.StatusNotFound {
		t.Fatalf("prefix session match = %d, want 404 (%s)", prefixRec.Code, prefixRec.Body.String())
	}

	// Missing required variable fails 400 before touching tmux or jobs.
	missingVarRec := doReq(http.MethodPost, "/api/v1/workflows/prod-manual-flow/run",
		`{"session":"wf_prod_live","project_dir":`+strconvQuote(repoDir)+`}`)
	if missingVarRec.Code != http.StatusBadRequest {
		t.Fatalf("missing required var = %d, want 400 (%s)", missingVarRec.Code, missingVarRec.Body.String())
	}

	// Start the real managed workflow job.
	runRec := doReq(http.MethodPost, "/api/v1/workflows/prod-manual-flow/run",
		`{"session":"wf_prod_live","project_dir":`+strconvQuote(repoDir)+`,"variables":{"feature":"rest-live"},"interval":"15ms"}`)
	if runRec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/v1/workflows/prod-manual-flow/run = %d, want 202 (%s)", runRec.Code, runRec.Body.String())
	}
	var runEnv WorkflowControlEnvelopeResponse
	if err := json.Unmarshal(runRec.Body.Bytes(), &runEnv); err != nil {
		t.Fatalf("decode run response: %v", err)
	}
	if runEnv.JobID == "" || !runEnv.Running || runEnv.Workflow != "prod-manual-flow" || runEnv.Session != "wf_prod_live" {
		t.Fatalf("unexpected run envelope: %+v", runEnv)
	}
	if gotLoc := runRec.Header().Get("Location"); gotLoc != "/api/v1/jobs/"+runEnv.JobID {
		t.Fatalf("Location header = %q, want %q", gotLoc, "/api/v1/jobs/"+runEnv.JobID)
	}

	// Concurrent second run on the exact same (project_dir, session) must 409.
	dupRec := doReq(http.MethodPost, "/api/v1/workflows/prod-manual-flow/run",
		`{"session":"wf_prod_live","project_dir":`+strconvQuote(repoDir)+`,"variables":{"feature":"rest-live"}}`)
	if dupRec.Code != http.StatusConflict {
		t.Fatalf("duplicate concurrent run = %d, want 409 (%s)", dupRec.Code, dupRec.Body.String())
	}

	// Wait for the real coordinator to deliver stage "build" and publish its live manual gate.
	buildStatus := waitWorkflowStatusCondition(t, doReq, "wf_prod_live", repoDir, 8*time.Second, func(env WorkflowControlEnvelopeResponse) bool {
		return env.Stage == "build" && env.ManualGate != nil && env.ManualGate.Token != "" && len(env.Dispatches) == 1 && env.Dispatches[0].Status == "delivered"
	})
	if !buildStatus.Running || buildStatus.JobID != runEnv.JobID {
		t.Fatalf("expected live running job on build stage, got %+v", buildStatus)
	}
	deliveries := fx.deliveries()
	if !strings.Contains(deliveries, "===DELIVERY pane=%1===") || !strings.Contains(deliveries, "[ntm workflow prod-manual-flow] stage build turn 1") || !strings.Contains(deliveries, "feature: rest-live") {
		t.Fatalf("production transport did not receive build prompt on %%1:\n%s", deliveries)
	}

	// Trigger stage 1 manual gate -> live coordinator consumes it and advances to "review".
	trig1Rec := doReq(http.MethodPost, "/api/v1/workflows/wf_prod_live/trigger",
		`{"token":`+strconvQuote(buildStatus.ManualGate.Token)+`,"transition":0,"reason":"Build verified","project_dir":`+strconvQuote(repoDir)+`}`)
	if trig1Rec.Code != http.StatusOK {
		t.Fatalf("trigger build->review = %d (%s)", trig1Rec.Code, trig1Rec.Body.String())
	}

	reviewStatus := waitWorkflowStatusCondition(t, doReq, "wf_prod_live", repoDir, 8*time.Second, func(env WorkflowControlEnvelopeResponse) bool {
		return env.Stage == "review" && env.ManualGate != nil && env.ManualGate.Token != "" && env.ManualGate.Token != buildStatus.ManualGate.Token && len(env.Dispatches) == 1 && env.Dispatches[0].Status == "delivered"
	})
	deliveries = fx.deliveries()
	if !strings.Contains(deliveries, "===DELIVERY pane=%2===") || !strings.Contains(deliveries, "[ntm workflow prod-manual-flow] stage review turn 2") {
		t.Fatalf("production transport did not receive review prompt on %%2:\n%s", deliveries)
	}

	// Trigger stage 2 manual gate -> live coordinator reaches terminal "done" and completes managed job.
	trig2Rec := doReq(http.MethodPost, "/api/v1/workflows/wf_prod_live/trigger",
		`{"token":`+strconvQuote(reviewStatus.ManualGate.Token)+`,"transition":1,"reason":"Ready to ship","project_dir":`+strconvQuote(repoDir)+`}`)
	if trig2Rec.Code != http.StatusOK {
		t.Fatalf("trigger review->done = %d (%s)", trig2Rec.Code, trig2Rec.Body.String())
	}

	doneStatus := waitWorkflowStatusCondition(t, doReq, "wf_prod_live", repoDir, 8*time.Second, func(env WorkflowControlEnvelopeResponse) bool {
		return env.Completed && !env.Running && env.JobStatus == JobStatusCompleted
	})
	if doneStatus.Stage != "done" || doneStatus.JobID != runEnv.JobID {
		t.Fatalf("unexpected completed workflow status: %+v", doneStatus)
	}
	// Exactly two prompts dispatched through production transport (build -> %1, review -> %2).
	if gotCount := strings.Count(fx.deliveries(), "===DELIVERY "); gotCount != 2 {
		t.Fatalf("delivery count = %d, want 2:\n%s", gotCount, fx.deliveries())
	}
}

// TestWorkflowsRESTStopCancelsActualRunningJobAndEnforcesProjectIdentity proves:
//  1. Stop scoped to a different project_dir cannot cancel a running workflow job.
//  2. PATCH /api/v1/config cannot redirect an already-admitted workflow job.
//  3. POST /api/v1/workflows/{session}/stop genuinely cancels the live coordinator
//     goroutine (tmux polling ceases), marks the managed job cancelled, and
//     durably pauses the checkpoint.
//  4. Subsequent run with resume=true continues cleanly without resending the
//     already-delivered stage prompt.
func TestWorkflowsRESTStopCancelsActualRunningJobAndEnforcesProjectIdentity(t *testing.T) {
	fx := setupWorkflowProductionTmuxFixture(t, "wf_stop_live")
	projectA := t.TempDir()
	projectB := t.TempDir()
	writeWorkflowFixtureTOML(t, projectA, "stop-flow.toml", `name = "stop-flow"
description = "True stop cancellation and resume verification"
coordination = "pipeline"

[[agents]]
profile = "builder"
role = "build"

[flow]
initial = "build"
stages = ["build", "done"]

[[flow.transitions]]
from = "build"
to = "done"
[flow.transitions.trigger]
type = "manual"
label = "Complete build"
`)

	srv := NewHermeticServer("1.39.0-bd-mu7qd")
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

	runRec := doReq(http.MethodPost, "/api/v1/workflows/stop-flow/run",
		`{"session":"wf_stop_live","project_dir":`+strconvQuote(projectA)+`,"interval":"15ms"}`)
	if runRec.Code != http.StatusAccepted {
		t.Fatalf("start stop-flow = %d (%s)", runRec.Code, runRec.Body.String())
	}
	var runEnv WorkflowControlEnvelopeResponse
	if err := json.Unmarshal(runRec.Body.Bytes(), &runEnv); err != nil {
		t.Fatalf("decode run: %v", err)
	}

	liveStatus := waitWorkflowStatusCondition(t, doReq, "wf_stop_live", projectA, 8*time.Second, func(env WorkflowControlEnvelopeResponse) bool {
		return env.Running && env.ManualGate != nil && env.ManualGate.Token != ""
	})
	gateToken := liveStatus.ManualGate.Token

	// Switch the server's default project_dir to projectB via PATCH /config.
	patchRec := doReq(http.MethodPatch, "/api/v1/config", `{"project_dir":`+strconvQuote(projectB)+`}`)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("PATCH /api/v1/config = %d (%s)", patchRec.Code, patchRec.Body.String())
	}

	// Stopping against projectB MUST NOT stop the running job in projectA.
	wrongProjStop := doReq(http.MethodPost, "/api/v1/workflows/wf_stop_live/stop",
		`{"reason":"Wrong project stop","project_dir":`+strconvQuote(projectB)+`}`)
	if wrongProjStop.Code != http.StatusNotFound {
		t.Fatalf("stop with different project_dir = %d, want 404 (%s)", wrongProjStop.Code, wrongProjStop.Body.String())
	}
	stillLiveRec := doReq(http.MethodGet, "/api/v1/workflows/wf_stop_live/status?project_dir="+url.QueryEscape(projectA), "")
	var stillLive WorkflowControlEnvelopeResponse
	if err := json.Unmarshal(stillLiveRec.Body.Bytes(), &stillLive); err != nil || !stillLive.Running || stillLive.JobID != runEnv.JobID {
		t.Fatalf("wrong-project stop affected projectA job: %+v (%v)", stillLive, err)
	}

	// Stop the actual running job in projectA.
	stopRec := doReq(http.MethodPost, "/api/v1/workflows/wf_stop_live/stop",
		`{"reason":"Operator halted live run","project_dir":`+strconvQuote(projectA)+`}`)
	if stopRec.Code != http.StatusOK {
		t.Fatalf("stop projectA workflow = %d (%s)", stopRec.Code, stopRec.Body.String())
	}
	var stopEnv WorkflowControlEnvelopeResponse
	if err := json.Unmarshal(stopRec.Body.Bytes(), &stopEnv); err != nil {
		t.Fatalf("decode stop response: %v", err)
	}
	if !stopEnv.StoppedJob || stopEnv.Running || stopEnv.JobID != runEnv.JobID || stopEnv.JobStatus != JobStatusCancelled || !stopEnv.Paused || stopEnv.State == nil || stopEnv.State.PauseReason != "Operator halted live run" {
		t.Fatalf("stop did not truly cancel job and pause checkpoint: %+v", stopEnv)
	}

	// Prove the coordinator goroutine genuinely exited (zero further tmux polling).
	callsAfterStop := fx.callCount()
	time.Sleep(80 * time.Millisecond)
	if callsLater := fx.callCount(); callsLater != callsAfterStop {
		t.Fatalf("workflow coordinator kept polling tmux after stop: calls grew from %d to %d", callsAfterStop, callsLater)
	}

	// Starting fresh without resume/restart must fail 409 on the paused checkpoint.
	conflictRec := doReq(http.MethodPost, "/api/v1/workflows/stop-flow/run",
		`{"session":"wf_stop_live","project_dir":`+strconvQuote(projectA)+`}`)
	if conflictRec.Code != http.StatusConflict {
		t.Fatalf("rerun without resume/restart = %d, want 409 (%s)", conflictRec.Code, conflictRec.Body.String())
	}

	// Resume continues the saved stage WITHOUT re-sending the already-delivered prompt.
	resumeRec := doReq(http.MethodPost, "/api/v1/workflows/stop-flow/run",
		`{"session":"wf_stop_live","project_dir":`+strconvQuote(projectA)+`,"resume":true,"interval":"15ms"}`)
	if resumeRec.Code != http.StatusAccepted {
		t.Fatalf("resume stop-flow = %d (%s)", resumeRec.Code, resumeRec.Body.String())
	}
	trigRec := doReq(http.MethodPost, "/api/v1/workflows/wf_stop_live/trigger",
		`{"token":`+strconvQuote(gateToken)+`,"transition":0,"reason":"Finish after resume","project_dir":`+strconvQuote(projectA)+`}`)
	if trigRec.Code != http.StatusOK {
		t.Fatalf("trigger after resume = %d (%s)", trigRec.Code, trigRec.Body.String())
	}
	resumedDone := waitWorkflowStatusCondition(t, doReq, "wf_stop_live", projectA, 8*time.Second, func(env WorkflowControlEnvelopeResponse) bool {
		return env.Completed && !env.Running && env.JobStatus == JobStatusCompleted
	})
	if resumedDone.Stage != "done" {
		t.Fatalf("unexpected resumed completion status: %+v", resumedDone)
	}
	if gotCount := strings.Count(fx.deliveries(), "===DELIVERY "); gotCount != 1 {
		t.Fatalf("resume duplicated already-delivered prompt: delivery count = %d, want 1\n%s", gotCount, fx.deliveries())
	}
}

// TestWorkflowsManagedJobsAPICancelAndDryRun proves POST /api/v1/jobs with
// type="workflow_run" shares the exact same production engine, supports
// dry_run validation, and cancels cleanly via DELETE /api/v1/jobs/{id}.
func TestWorkflowsManagedJobsAPICancelAndDryRun(t *testing.T) {
	fx := setupWorkflowProductionTmuxFixture(t, "wf_jobs_live")
	repoDir := t.TempDir()
	writeWorkflowFixtureTOML(t, repoDir, "jobs-flow.toml", `name = "jobs-flow"
description = "Managed jobs API workflow test"
coordination = "pipeline"

[[agents]]
profile = "builder"
role = "build"

[flow]
initial = "build"
stages = ["build", "done"]

[[flow.transitions]]
from = "build"
to = "done"
[flow.transitions.trigger]
type = "manual"
label = "Done"
`)

	srv := NewHermeticServer("1.39.0-bd-mu7qd")
	srv.projectDir = repoDir
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

	// 1. Dry-run through /api/v1/jobs completes with validated status and zero tmux calls.
	dryEnv := postJob(t, srv, `{"type":"workflow_run","session":"wf_jobs_live","params":{"workflow":"jobs-flow","dry_run":true}}`)
	dryFinal := pollJobTerminal(t, srv, dryEnv.Job.ID)
	if dryFinal.Job.Status != string(JobStatusCompleted) || dryFinal.Job.Result["status"] != "validated" || dryFinal.Job.Result["dry_run"] != true {
		t.Fatalf("unexpected dry_run workflow job result: %+v", dryFinal.Job)
	}
	if fx.callCount() != 0 {
		t.Fatalf("dry_run workflow job invoked tmux (%d calls)", fx.callCount())
	}
	// Dry-run via /api/v1/jobs must NEVER pollute GET /api/v1/workflows/{session}/status.
	dryStatusRec := doReq(http.MethodGet, "/api/v1/workflows/wf_jobs_live/status?project_dir="+url.QueryEscape(repoDir), "")
	if dryStatusRec.Code != http.StatusNotFound {
		t.Fatalf("dry_run workflow job polluted /workflows/status: %d %s", dryStatusRec.Code, dryStatusRec.Body.String())
	}

	// 2. Live run through /api/v1/jobs dispatches prompt and cancels cleanly via DELETE /api/v1/jobs/{id}.
	liveEnv := postJob(t, srv, `{"type":"workflow_run","session":"wf_jobs_live","params":{"workflow":"jobs-flow","interval":"15ms"}}`)
	waitWorkflowStatusCondition(t, doReq, "wf_jobs_live", repoDir, 8*time.Second, func(env WorkflowControlEnvelopeResponse) bool {
		return env.Running && env.ManualGate != nil && env.ManualGate.Token != ""
	})
	// Duplicate run rejection must return 409 without creating a phantom cancelled job row.
	jobsBeforeDup := len(srv.jobStore.List())
	dupRec := doReq(http.MethodPost, "/api/v1/workflows/jobs-flow/run", `{"session":"wf_jobs_live","resume":true}`)
	if dupRec.Code != http.StatusConflict {
		t.Fatalf("duplicate active workflow run = %d, want 409 (%s)", dupRec.Code, dupRec.Body.String())
	}
	if got := len(srv.jobStore.List()); got != jobsBeforeDup {
		t.Fatalf("409 duplicate workflow rejection created phantom job row: before=%d after=%d", jobsBeforeDup, got)
	}
	delRec := doReq(http.MethodDelete, "/api/v1/jobs/"+liveEnv.Job.ID, "")
	if delRec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/v1/jobs/%s = %d (%s)", liveEnv.Job.ID, delRec.Code, delRec.Body.String())
	}
	waitWorkflowStatusCondition(t, doReq, "wf_jobs_live", repoDir, 8*time.Second, func(env WorkflowControlEnvelopeResponse) bool {
		return !env.Running && env.JobStatus == JobStatusCancelled && env.Paused
	})

	// 3. Offline POST /api/v1/workflows/{session}/stop refuses to pause an already-completed checkpoint.
	store := workflowStateStoreForProject(repoDir)
	doneState := &workflow.WorkflowState{
		SessionName:    "wf_completed_offline",
		WorkflowName:   "jobs-flow",
		CurrentStage:   "done",
		Completed:      true,
		Paused:         false,
		StageStartedAt: time.Now().UTC(),
	}
	if err := store.Save(doneState); err != nil {
		t.Fatalf("save completed checkpoint: %v", err)
	}
	stopDoneRec := doReq(http.MethodPost, "/api/v1/workflows/wf_completed_offline/stop", `{"reason":"should not corrupt completed"}`)
	if stopDoneRec.Code != http.StatusConflict {
		t.Fatalf("offline stop on completed checkpoint = %d, want 409 (%s)", stopDoneRec.Code, stopDoneRec.Body.String())
	}
	reloaded, err := store.Load("wf_completed_offline")
	if err != nil || reloaded == nil || !reloaded.Completed || reloaded.Paused {
		t.Fatalf("offline stop corrupted completed checkpoint: state=%+v err=%v", reloaded, err)
	}
}

// TestWorkflowsPass3ReliabilityContracts covers Pass-3 fresh-eyes fixes:
// 1. Pre-bind /stop cancels reserved workflow immediately without 5s stall or race.
// 2. Request-scoped workflow timeout durably pauses the checkpoint.
// 3. Stale prior completed checkpoint never clobbers GET /status after a different workflow cancels early.
// 4. Explicit project_dir keeps Idempotency-Key replay working across PATCH /api/v1/config.
// 5. Server shutdown wakes active SSE /events streams promptly.
// 6. WS attention subscribe+replay buffers concurrent live events in strict monotonic order.
func TestWorkflowsPass3ReliabilityContracts(t *testing.T) {
	t.Run("stop_prebind_reservation_cancels_immediately_without_stall", func(t *testing.T) {
		repoDir := t.TempDir()
		srv := New(Config{Host: "127.0.0.1", Port: 0, ProjectDir: repoDir})
		defer srv.Stop()

		ctx, cancel := context.WithCancel(context.Background())
		active, err := srv.reserveWorkflowRun("prebind-wf", repoDir, "prebind_sess", cancel, true)
		if err != nil {
			t.Fatalf("reserveWorkflowRun: %v", err)
		}
		// Simulate admission unwinding immediately once its context is cancelled.
		go func() {
			<-ctx.Done()
			srv.cancelWorkflowReservation(active)
		}()

		start := time.Now()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows/prebind_sess/stop",
			strings.NewReader(`{"reason":"stop before bind"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		elapsed := time.Since(start)
		if rec.Code != http.StatusOK {
			t.Fatalf("pre-bind stop = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		if elapsed >= 2*time.Second {
			t.Fatalf("pre-bind stop stalled for %v (want immediate cancel)", elapsed)
		}
		var env WorkflowControlEnvelopeResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode stop response: %v", err)
		}
		if !env.StoppedJob || env.Running {
			t.Fatalf("pre-bind stop envelope = %+v, want stopped_job=true running=false", env)
		}
		if !srv.bindWorkflowRunJob(active, "late-job") {
			t.Fatal("bindWorkflowRunJob did not observe prior stopRequested")
		}
	})

	t.Run("stale_prior_completed_checkpoint_never_clobbers_cancelled_different_workflow", func(t *testing.T) {
		repoDir := t.TempDir()
		srv := New(Config{Host: "127.0.0.1", Port: 0, ProjectDir: repoDir})
		defer srv.Stop()

		store := workflowStateStoreForProject(repoDir)
		if err := store.Save(&workflow.WorkflowState{
			SessionName:    "stale_sess",
			WorkflowName:   "wf-alpha",
			CurrentStage:   "done",
			Completed:      true,
			StageStartedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("save prior completed state: %v", err)
		}

		// Record a newer cancelled job for wf-beta that never wrote a checkpoint.
		job := srv.jobStore.Create(JobTypeWorkflowRun, jobOwnership{projectDir: repoDir})
		srv.jobStore.Update(job.ID, JobStatusCancelled, 0, map[string]interface{}{
			"workflow":    "wf-beta",
			"session":     "stale_sess",
			"project_dir": repoDir,
		}, "cancelled before checkpoint")
		srv.recordAcceptedWorkflowJob(&activeWorkflowRun{projectDir: repoDir, session: "stale_sess"}, job.ID)

		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/workflows/stale_sess/status", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /status = %d (%s)", rec.Code, rec.Body.String())
		}
		var env WorkflowControlEnvelopeResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		if env.Workflow != "wf-beta" || env.Completed {
			t.Fatalf("stale prior checkpoint clobbered status: workflow=%q completed=%v (want wf-beta, false)", env.Workflow, env.Completed)
		}
	})

	t.Run("explicit_project_dir_preserves_idempotency_replay_across_config_patch", func(t *testing.T) {
		defaultA := t.TempDir()
		defaultB := t.TempDir()
		explicitProj := t.TempDir()
		writeWorkflowFixtureTOML(t, explicitProj, "idem-flow.toml", `name = "idem-flow"
description = "explicit dir replay test"
coordination = "ping-pong"

[[agents]]
profile = "builder"
role = "worker"

[flow]
initial = "s1"
stages = ["s1", "done"]

[[flow.transitions]]
from = "s1"
to = "done"
[flow.transitions.trigger]
type = "manual"
label = "complete"
`)

		srv := New(Config{Host: "127.0.0.1", Port: 0, ProjectDir: defaultA})
		defer srv.Stop()

		body := fmt.Sprintf(`{"session":"idem_sess","project_dir":%q,"dry_run":true}`, explicitProj)
		req1 := httptest.NewRequest(http.MethodPost, "/api/v1/workflows/idem-flow/run", strings.NewReader(body))
		req1.Header.Set("Content-Type", "application/json")
		req1.Header.Set("Idempotency-Key", "explicit-wf-key-1")
		rec1 := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec1, req1)
		if rec1.Code != http.StatusOK {
			t.Fatalf("first explicit workflow dry-run = %d (%s)", rec1.Code, rec1.Body.String())
		}

		// Mutate server default project_dir via PATCH /api/v1/config.
		patchReq := httptest.NewRequest(http.MethodPatch, "/api/v1/config",
			strings.NewReader(fmt.Sprintf(`{"project_dir":%q}`, defaultB)))
		patchReq.Header.Set("Content-Type", "application/json")
		patchRec := httptest.NewRecorder()
		srv.Router().ServeHTTP(patchRec, patchReq)
		if patchRec.Code != http.StatusOK {
			t.Fatalf("PATCH /api/v1/config = %d (%s)", patchRec.Code, patchRec.Body.String())
		}

		// Retry with identical Idempotency-Key and explicit project_dir must replay cleanly.
		req2 := httptest.NewRequest(http.MethodPost, "/api/v1/workflows/idem-flow/run", strings.NewReader(body))
		req2.Header.Set("Content-Type", "application/json")
		req2.Header.Set("Idempotency-Key", "explicit-wf-key-1")
		rec2 := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec2, req2)
		if rec2.Code != http.StatusOK || rec2.Header().Get("X-Idempotent-Replay") != "true" {
			t.Fatalf("retry explicit workflow after PATCH /config did not replay: status=%d replay=%q body=%s",
				rec2.Code, rec2.Header().Get("X-Idempotent-Replay"), rec2.Body.String())
		}
	})

	t.Run("sse_stream_exits_promptly_on_server_shutdown", func(t *testing.T) {
		srv := New(Config{Host: "127.0.0.1", Port: 0})
		defer srv.Stop()

		done := make(chan struct{})
		req := httptest.NewRequest(http.MethodGet, "/events", nil)
		rec := httptest.NewRecorder()
		go func() {
			defer close(done)
			srv.handleEventStream(rec, req)
		}()

		// Wait until client registers on sseClients.
		deadline := time.Now().Add(2 * time.Second)
		for {
			srv.sseClientsMu.RLock()
			n := len(srv.sseClients)
			srv.sseClientsMu.RUnlock()
			if n > 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("SSE client never registered")
			}
			time.Sleep(5 * time.Millisecond)
		}

		srv.signalShutdown()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("handleEventStream did not exit promptly after server shutdown signal")
		}
	})

	t.Run("ws_attention_subscribe_buffers_live_events_during_replay_in_order", func(t *testing.T) {
		feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{
			JournalSize: 128,
		})
		for i := 1; i <= 5; i++ {
			feed.Append(robot.AttentionEvent{
				Session:       "ws_order_sess",
				Category:      robot.EventCategoryAgent,
				Type:          robot.EventTypeAgentStateChange,
				Actionability: robot.ActionabilityInteresting,
				Summary:       fmt.Sprintf("hist-%d", i),
			})
		}

		srv := New(Config{Host: "127.0.0.1", Port: 0})
		srv.attentionFeed = feed
		defer srv.Stop()

		client := &WSClient{
			id:     "order-test-client",
			hub:    srv.wsHub,
			send:   make(chan []byte, 256),
			topics: make(map[string]struct{}),
		}

		var pubWG sync.WaitGroup
		pubWG.Add(1)
		go func() {
			defer pubWG.Done()
			for i := 6; i <= 25; i++ {
				feed.Append(robot.AttentionEvent{
					Session:       "ws_order_sess",
					Category:      robot.EventCategoryAgent,
					Type:          robot.EventTypeAgentStateChange,
					Actionability: robot.ActionabilityInteresting,
					Summary:       fmt.Sprintf("live-%d", i),
				})
			}
		}()

		resp := client.handleAttentionSubscribeWithFeed(feed, WSMessage{
			Data: map[string]interface{}{"since_cursor": int64(0)},
		}, []string{"attention:ws_order_sess"})
		pubWG.Wait()
		client.cancelAttentionSubscription()

		if subscribed, _ := resp["subscribed"].(bool); !subscribed {
			t.Fatalf("handleAttentionSubscribe failed: %+v", resp)
		}
		var prevSeq int64
		count := 0
		for {
			select {
			case raw := <-client.send:
				var ev WSEvent
				if err := json.Unmarshal(raw, &ev); err != nil {
					t.Fatalf("unmarshal ws event: %v", err)
				}
				if ev.Type != WSMsgEvent {
					continue
				}
				if ev.Seq <= prevSeq {
					t.Fatalf("out-of-order or duplicate WS attention cursor: got %d after %d", ev.Seq, prevSeq)
				}
				if prevSeq > 0 && ev.Seq != prevSeq+1 {
					t.Fatalf("silent cursor gap in WS attention delivery: jumped from %d to %d", prevSeq, ev.Seq)
				}
				prevSeq = ev.Seq
				count++
			default:
				goto doneDrain
			}
		}
	doneDrain:
		if count != 25 {
			t.Fatalf("expected all 25 contiguous attention events without silent gap, got %d (last seq=%d)", count, prevSeq)
		}
	})
}

func TestWorkflowsRESTRequestTimeoutPausesCheckpoint(t *testing.T) {
	fx := setupWorkflowProductionTmuxFixture(t, "wf_timeout_sess")
	repoDir := t.TempDir()
	writeWorkflowFixtureTOML(t, repoDir, "timeout-flow.toml", `name = "timeout-flow"
description = "pauses checkpoint on request timeout"
coordination = "ping-pong"

[[agents]]
profile = "builder"
role = "build"

[flow]
initial = "wait_forever"
stages = ["wait_forever", "done"]

[[flow.transitions]]
from = "wait_forever"
to = "done"
[flow.transitions.trigger]
type = "manual"
label = "never fired"
`)

	srv := New(Config{Host: "127.0.0.1", Port: 0, ProjectDir: repoDir})
	defer srv.Stop()
	doReq := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}

	runRec := doReq(http.MethodPost, "/api/v1/workflows/timeout-flow/run",
		`{"session":"wf_timeout_sess","interval":"15ms","timeout":"120ms"}`)
	if runRec.Code != http.StatusAccepted {
		t.Fatalf("POST /run = %d (%s)", runRec.Code, runRec.Body.String())
	}
	env := waitWorkflowStatusCondition(t, doReq, "wf_timeout_sess", repoDir, 8*time.Second, func(e WorkflowControlEnvelopeResponse) bool {
		return !e.Running && e.Paused
	})
	if env.State == nil || !strings.Contains(env.State.PauseReason, "timeout") {
		t.Fatalf("timeout pause reason missing: state=%+v", env.State)
	}
	_ = fx
}

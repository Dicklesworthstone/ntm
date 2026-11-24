package serve

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/kernel"
	statepkg "github.com/Dicklesworthstone/ntm/internal/state"
)

func TestIncidentsAndHandoffsOpenAPIReflection(t *testing.T) {
	spec, err := GenerateOpenAPISpecFromRouter("dev", "http://127.0.0.1:7337")
	if err != nil {
		t.Fatalf("GenerateOpenAPISpecFromRouter: %v", err)
	}

	expectedRoutes := []struct {
		method      string
		path        string
		wantRespRef string
		wantReqRef  string
		wantProp    string
	}{
		{"GET", "/api/v1/incidents", "#/components/schemas/IncidentsListEnvelopeResponse", "", "incidents"},
		{"GET", "/api/v1/incidents/{id}", "#/components/schemas/IncidentDetailEnvelopeResponse", "", "incident"},
		{"POST", "/api/v1/incidents/{id}/status", "#/components/schemas/IncidentStatusEnvelopeResponse", "#/components/schemas/IncidentStatusUpdateRequest", "status"},
		{"POST", "/api/v1/incidents/{id}/resolve", "#/components/schemas/IncidentStatusEnvelopeResponse", "#/components/schemas/IncidentResolveRequest", "incident_id"},
		{"GET", "/api/v1/handoffs", "#/components/schemas/HandoffsEnvelopeResponse", "", "handoffs"},
		{"GET", "/api/v1/handoffs/latest", "#/components/schemas/HandoffLatestEnvelopeResponse", "", "latest"},
		{"POST", "/api/v1/handoffs", "#/components/schemas/HandoffsEnvelopeResponse", "#/components/schemas/CreateHandoffRequest", "created_path"},
	}

	for _, tc := range expectedRoutes {
		item, ok := spec.Paths[tc.path]
		if !ok {
			t.Fatalf("missing OpenAPI path %s", tc.path)
		}
		var op *Operation
		switch tc.method {
		case "GET":
			op = item.Get
		case "POST":
			op = item.Post
		}
		if op == nil {
			t.Fatalf("missing operation %s %s", tc.method, tc.path)
		}
		resp200, ok := op.Responses["200"]
		if !ok {
			t.Fatalf("%s %s missing 200 response", tc.method, tc.path)
		}
		gotRef := resp200.Content["application/json"].Schema.Ref
		if gotRef != tc.wantRespRef {
			t.Fatalf("%s %s 200 ref = %q, want %q", tc.method, tc.path, gotRef, tc.wantRespRef)
		}
		compName := strings.TrimPrefix(tc.wantRespRef, "#/components/schemas/")
		compSchema := spec.Components.Schemas[compName]
		if compSchema == nil {
			t.Fatalf("component schema %q not emitted", compName)
		}
		if _, ok := compSchema.Properties[tc.wantProp]; !ok {
			t.Fatalf("component schema %q missing expected property %q", compName, tc.wantProp)
		}
		if tc.wantReqRef != "" {
			if op.RequestBody == nil {
				t.Fatalf("%s %s missing request body", tc.method, tc.path)
			}
			gotReqRef := op.RequestBody.Content["application/json"].Schema.Ref
			if gotReqRef != tc.wantReqRef {
				t.Fatalf("%s %s request ref = %q, want %q", tc.method, tc.path, gotReqRef, tc.wantReqRef)
			}
		}
	}

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

func doServeJSONRequest(t *testing.T, router http.Handler, method, target string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reqBody *bytes.Reader
	if body != nil {
		switch v := body.(type) {
		case string:
			reqBody = bytes.NewReader([]byte(v))
		default:
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal request body: %v", err)
			}
			reqBody = bytes.NewReader(raw)
		}
	} else {
		reqBody = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, reqBody)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var decoded map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("%s %s decode JSON (%d): %v; body=%s", method, target, rec.Code, err, rec.Body.String())
		}
	}
	return rec, decoded
}

func TestIncidentsRESTEndpointsLifecycle(t *testing.T) {
	// 1. Nil StateStore -> 503 Service Unavailable
	nilStoreSrv := New(Config{ProjectDir: t.TempDir()})
	t.Cleanup(nilStoreSrv.Stop)
	rec, _ := doServeJSONRequest(t, nilStoreSrv.Router(), http.MethodGet, "/api/v1/incidents", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil store GET /api/v1/incidents code = %d, want 503", rec.Code)
	}

	// 2. Backed by real SQLite runtime store
	dbPath := filepath.Join(t.TempDir(), "state.db")
	store := openFastServeStateStore(t, dbPath)
	t.Cleanup(func() { _ = store.Close() })

	srv := New(Config{ProjectDir: t.TempDir(), StateStore: store})
	t.Cleanup(srv.Stop)
	router := srv.Router()

	// Empty list
	rec, payload := doServeJSONRequest(t, router, http.MethodGet, "/api/v1/incidents", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty GET /api/v1/incidents code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if gotCount := int(payload["count"].(float64)); gotCount != 0 {
		t.Fatalf("empty count = %d, want 0", gotCount)
	}

	// Seed an open incident + linked attention event
	now := time.Now().UTC()
	inc := &statepkg.Incident{
		ID:           "inc-wave9-test-1",
		Title:        "Agent stall detected in pane 2",
		Fingerprint:  "fp-wave9-stall-pane2",
		Family:       "agent_stall",
		Category:     "agent",
		Status:       statepkg.IncidentStatusOpen,
		Severity:     statepkg.SeverityError,
		SessionNames: `["wave9proj"]`,
		AlertCount:   2,
		EventCount:   0,
		StartedAt:    now.Add(-5 * time.Minute),
		LastEventAt:  now,
	}
	if err := store.CreateIncident(inc); err != nil {
		t.Fatalf("CreateIncident: %v", err)
	}
	cursor, err := store.AppendAttentionEvent(&statepkg.StoredAttentionEvent{
		Ts:            now,
		SessionName:   "wave9proj",
		Pane:          "2",
		Category:      "incident",
		EventType:     "incident_opened",
		Source:        "test",
		Actionability: statepkg.ActionabilityActionRequired,
		Severity:      statepkg.SeverityError,
		Summary:       "Pane 2 stalled for >300s",
	})
	if err != nil {
		t.Fatalf("AppendAttentionEvent: %v", err)
	}
	if err := store.LinkEventToIncident(inc.ID, cursor); err != nil {
		t.Fatalf("LinkEventToIncident: %v", err)
	}

	// List open incidents
	rec, payload = doServeJSONRequest(t, router, http.MethodGet, "/api/v1/incidents?limit=10", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/incidents code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if int(payload["count"].(float64)) != 1 {
		t.Fatalf("count = %v, want 1", payload["count"])
	}

	// Inspect incident detail + linked events
	rec, payload = doServeJSONRequest(t, router, http.MethodGet, "/api/v1/incidents/"+inc.ID+"?events_limit=10", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/incidents/%s code = %d, body=%s", inc.ID, rec.Code, rec.Body.String())
	}
	if int(payload["event_count"].(float64)) != 1 {
		t.Fatalf("event_count = %v, want 1", payload["event_count"])
	}

	// Transition -> investigating
	rec, payload = doServeJSONRequest(t, router, http.MethodPost, "/api/v1/incidents/"+inc.ID+"/status", map[string]any{
		"status": "investigating",
		"reason": "Inspecting pane scrollback",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status investigating code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if payload["previous_status"] != "open" || payload["status"] != "investigating" {
		t.Fatalf("unexpected transition payload: %+v", payload)
	}

	// Transition -> muted (should hide from default GET /api/v1/incidents, appear with include_resolved=true)
	rec, _ = doServeJSONRequest(t, router, http.MethodPost, "/api/v1/incidents/"+inc.ID+"/status", map[string]any{
		"status": "muted",
		"reason": "Known transient during compile",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status muted code = %d, body=%s", rec.Code, rec.Body.String())
	}
	_, openOnly := doServeJSONRequest(t, router, http.MethodGet, "/api/v1/incidents", nil)
	if int(openOnly["count"].(float64)) != 0 {
		t.Fatalf("muted incident should be excluded by default, got count=%v", openOnly["count"])
	}
	_, withResolved := doServeJSONRequest(t, router, http.MethodGet, "/api/v1/incidents?include_resolved=true", nil)
	if int(withResolved["count"].(float64)) != 1 {
		t.Fatalf("include_resolved=true count = %v, want 1", withResolved["count"])
	}

	// Resolve + idempotent re-resolve
	rec, resolved := doServeJSONRequest(t, router, http.MethodPost, "/api/v1/incidents/"+inc.ID+"/resolve", map[string]any{
		"note": "Pane recovered cleanly",
	})
	if rec.Code != http.StatusOK || resolved["status"] != "resolved" || resolved["idempotent"] != false {
		t.Fatalf("first resolve unexpected: code=%d payload=%+v", rec.Code, resolved)
	}
	rec, resolvedAgain := doServeJSONRequest(t, router, http.MethodPost, "/api/v1/incidents/"+inc.ID+"/resolve", nil)
	if rec.Code != http.StatusOK || resolvedAgain["idempotent"] != true {
		t.Fatalf("repeat resolve expected idempotent=true: code=%d payload=%+v", rec.Code, resolvedAgain)
	}

	// Validation & 404 checks
	rec, _ = doServeJSONRequest(t, router, http.MethodGet, "/api/v1/incidents/does-not-exist", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing incident code = %d, want 404", rec.Code)
	}
	rec, _ = doServeJSONRequest(t, router, http.MethodGet, "/api/v1/incidents?status=bogus", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid status filter code = %d, want 400", rec.Code)
	}
	rec, _ = doServeJSONRequest(t, router, http.MethodPost, "/api/v1/incidents/"+inc.ID+"/status", map[string]any{"status": "bogus"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid target status code = %d, want 400", rec.Code)
	}
}

func TestHandoffsRESTEndpointsLifecycle(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	srv := New(Config{ProjectDir: projectDir})
	t.Cleanup(srv.Stop)
	router := srv.Router()

	// 1. Empty inspect
	rec, payload := doServeJSONRequest(t, router, http.MethodGet, "/api/v1/handoffs?session=wave9sess", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty GET /api/v1/handoffs code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if int(payload["total_returned"].(float64)) != 0 {
		t.Fatalf("empty total_returned = %v, want 0", payload["total_returned"])
	}

	// 2. Create manual handoff -> computes QualityReport
	includeGit := false
	rec, created := doServeJSONRequest(t, router, http.MethodPost, "/api/v1/handoffs", CreateHandoffRequest{
		Session:     "wave9sess",
		Goal:        "Implemented Incidents and Handoffs REST surfaces",
		Now:         "Wire Web Cockpit /attention and /sessions panels",
		Description: "wave9-rest",
		IncludeGit:  &includeGit,
		Next:        []string{"Run Vitest", "Run Chrome E2E"},
		Decisions:   map[string]string{"ori": "delegate to robot.GetHandoff"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/handoffs manual code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if created["action"] != "handoff_create" || created["created_path"] == "" {
		t.Fatalf("unexpected create payload: %+v", created)
	}
	quality, ok := created["quality"].(map[string]any)
	if !ok || int(quality["score"].(float64)) <= 0 {
		t.Fatalf("expected positive quality score on created handoff, got %+v", created["quality"])
	}

	// 3. Create auto handoff for second session
	rec, autoCreated := doServeJSONRequest(t, router, http.MethodPost, "/api/v1/handoffs", CreateHandoffRequest{
		Session:    "autosess",
		Auto:       true,
		IncludeGit: &includeGit,
	})
	if rec.Code != http.StatusOK || autoCreated["created_path"] == "" {
		t.Fatalf("POST /api/v1/handoffs auto code = %d, body=%s", rec.Code, rec.Body.String())
	}

	// 4. List across all sessions
	rec, allResp := doServeJSONRequest(t, router, http.MethodGet, "/api/v1/handoffs?session=all&limit=10", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/handoffs?session=all code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if int(allResp["total_returned"].(float64)) != 2 {
		t.Fatalf("all total_returned = %v, want 2", allResp["total_returned"])
	}

	// 5. Inspect latest for wave9sess
	rec, latestResp := doServeJSONRequest(t, router, http.MethodGet, "/api/v1/handoffs/latest?session=wave9sess", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/handoffs/latest code = %d, body=%s", rec.Code, rec.Body.String())
	}
	latestObj, ok := latestResp["latest"].(map[string]any)
	if !ok || latestObj["Goal"] != "Implemented Incidents and Handoffs REST surfaces" {
		t.Fatalf("unexpected latest handoff object: %+v", latestResp["latest"])
	}

	// 6. Validation & path traversal refusals
	rec, _ = doServeJSONRequest(t, router, http.MethodPost, "/api/v1/handoffs", map[string]any{
		"session": "../escape",
		"goal":    "bad",
		"now":     "bad",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("path traversal session code = %d, want 400", rec.Code)
	}
	rec, _ = doServeJSONRequest(t, router, http.MethodGet, "/api/v1/handoffs?project_dir=relative/path", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("relative project_dir code = %d, want 400", rec.Code)
	}
}

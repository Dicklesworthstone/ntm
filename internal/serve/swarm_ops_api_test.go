package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/summary"
	"github.com/Dicklesworthstone/ntm/internal/webhook"
)

func TestSwarmOpsOpenAPISchemaContracts(t *testing.T) {
	spec, err := GenerateOpenAPISpecFromRouter("1.39.0-wave13", "http://127.0.0.1:7337")
	if err != nil {
		t.Fatalf("GenerateOpenAPISpecFromRouter: %v", err)
	}

	checks := []struct {
		method  string
		path    string
		wantRef string
		wantReq string
	}{
		{http.MethodPost, "/api/v1/sessions/{id}/scale", "#/components/schemas/SessionScaleEnvelopeResponse", "#/components/schemas/SessionScaleRequest"},
		{http.MethodPost, "/api/v1/sessions/{id}/rebalance", "#/components/schemas/SessionRebalanceEnvelopeResponse", "#/components/schemas/SessionRebalanceRequest"},
		{http.MethodPost, "/api/v1/sessions/{id}/review-queue", "#/components/schemas/SessionReviewQueueEnvelopeResponse", "#/components/schemas/SessionReviewQueueRequest"},
		{http.MethodGet, "/api/v1/sessions/{id}/summary", "#/components/schemas/SessionSummaryEnvelopeResponse", ""},
		{http.MethodGet, "/api/v1/summaries", "#/components/schemas/StoredSummariesEnvelopeResponse", ""},
		{http.MethodGet, "/api/v1/sessions/{id}/coordinator", "#/components/schemas/SessionCoordinatorEnvelopeResponse", ""},
		{http.MethodPost, "/api/v1/sessions/{id}/coordinator/digest", "#/components/schemas/SessionCoordinatorEnvelopeResponse", "#/components/schemas/SessionCoordinatorDigestRequest"},
		{http.MethodGet, "/api/v1/sessions/{id}/coordinator/conflicts", "#/components/schemas/SessionCoordinatorEnvelopeResponse", ""},
		{http.MethodGet, "/api/v1/webhooks/outbox", "#/components/schemas/WebhookOutboxEnvelopeResponse", ""},
		{http.MethodPost, "/api/v1/webhooks/outbox/{record}/retry", "#/components/schemas/WebhookOutboxEnvelopeResponse", "#/components/schemas/WebhookOutboxMutationRequest"},
		{http.MethodPost, "/api/v1/webhooks/outbox/{record}/archive", "#/components/schemas/WebhookOutboxEnvelopeResponse", "#/components/schemas/WebhookOutboxMutationRequest"},
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

func TestSwarmOpsSummariesReviewQueueAndValidationREST(t *testing.T) {
	projectDir := t.TempDir()
	summariesDir := filepath.Join(projectDir, ".ntm", "summaries")
	if err := os.MkdirAll(summariesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeStored := func(name string, s summary.SessionSummary) {
		t.Helper()
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(summariesDir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeStored("alpha-20261116-090000.json", summary.SessionSummary{
		Session:     "alpha",
		GeneratedAt: time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC),
		Format:      summary.FormatBrief,
		Text:        "Alpha summary",
	})
	writeStored("beta-20261116-100000.json", summary.SessionSummary{
		Session:     "beta",
		GeneratedAt: time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC),
		Format:      summary.FormatDetailed,
		Text:        "Beta summary",
	})

	srv := New(Config{
		Port:       0,
		Version:    "1.39.0-wave13",
		ProjectDir: projectDir,
	})
	t.Cleanup(func() { srv.Stop() })
	router := srv.Router()

	// 1. GET /api/v1/summaries returns newest-first stored summaries and supports ?session=alpha
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/summaries", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/summaries status=%d body=%s", rec.Code, rec.Body.String())
	}
	var allSummaries StoredSummariesEnvelopeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &allSummaries); err != nil {
		t.Fatalf("decode summaries: %v", err)
	}
	if !allSummaries.Success || allSummaries.Count != 2 || allSummaries.Summaries[0].Session != "beta" {
		t.Fatalf("unexpected summaries payload: %+v", allSummaries)
	}

	recAlpha := httptest.NewRecorder()
	router.ServeHTTP(recAlpha, httptest.NewRequest(http.MethodGet, "/api/v1/summaries?session=alpha&limit=1", nil))
	if recAlpha.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/summaries?session=alpha status=%d body=%s", recAlpha.Code, recAlpha.Body.String())
	}
	var alphaSummaries StoredSummariesEnvelopeResponse
	if err := json.Unmarshal(recAlpha.Body.Bytes(), &alphaSummaries); err != nil {
		t.Fatalf("decode alpha summaries: %v", err)
	}
	if alphaSummaries.Count != 1 || alphaSummaries.Summaries[0].Session != "alpha" {
		t.Fatalf("unexpected filtered summaries: %+v", alphaSummaries)
	}

	// Invalid limit and relative project_dir -> 400
	for _, badURL := range []string{
		"/api/v1/summaries?limit=0",
		"/api/v1/summaries?project_dir=relative/path",
	} {
		badRec := httptest.NewRecorder()
		router.ServeHTTP(badRec, httptest.NewRequest(http.MethodGet, badURL, nil))
		if badRec.Code != http.StatusBadRequest {
			t.Fatalf("GET %s status=%d, want 400; body=%s", badURL, badRec.Code, badRec.Body.String())
		}
	}

	// 2. GET /api/v1/sessions/{id}/summary: invalid since -> 400, missing session -> 404
	badSinceRec := httptest.NewRecorder()
	router.ServeHTTP(badSinceRec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/alpha/summary?since=not-a-duration", nil))
	if badSinceRec.Code != http.StatusBadRequest {
		t.Fatalf("GET summary invalid since status=%d, want 400", badSinceRec.Code)
	}

	missingSummaryRec := httptest.NewRecorder()
	router.ServeHTTP(missingSummaryRec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/__missing_wave13_sess__/summary?since=30m", nil))
	if missingSummaryRec.Code != http.StatusNotFound {
		t.Fatalf("GET summary missing session status=%d, want 404; body=%s", missingSummaryRec.Code, missingSummaryRec.Body.String())
	}

	// 3. POST /api/v1/sessions/{id}/review-queue: invalid filter -> 400, missing session -> 404
	badRQRec := httptest.NewRecorder()
	badRQReq := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/alpha/review-queue", strings.NewReader(`{"filter":"bogus"}`))
	badRQReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(badRQRec, badRQReq)
	if badRQRec.Code != http.StatusBadRequest {
		t.Fatalf("POST review-queue invalid filter status=%d, want 400; body=%s", badRQRec.Code, badRQRec.Body.String())
	}

	missingRQRec := httptest.NewRecorder()
	missingRQReq := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/__missing_wave13_sess__/review-queue", strings.NewReader(`{"commits":5}`))
	missingRQReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(missingRQRec, missingRQReq)
	if missingRQRec.Code != http.StatusNotFound {
		t.Fatalf("POST review-queue missing session status=%d, want 404; body=%s", missingRQRec.Code, missingRQRec.Body.String())
	}

	// 4. POST /api/v1/sessions/{id}/scale & /rebalance validation -> 400
	emptyScaleRec := httptest.NewRecorder()
	emptyScaleReq := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/alpha/scale", strings.NewReader(`{"targets":{}}`))
	emptyScaleReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(emptyScaleRec, emptyScaleReq)
	if emptyScaleRec.Code != http.StatusBadRequest {
		t.Fatalf("POST scale empty targets status=%d, want 400", emptyScaleRec.Code)
	}

	negRebalRec := httptest.NewRecorder()
	negRebalReq := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/alpha/rebalance", strings.NewReader(`{"threshold":-0.5}`))
	negRebalReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(negRebalRec, negRebalReq)
	if negRebalRec.Code != http.StatusBadRequest {
		t.Fatalf("POST rebalance negative threshold status=%d, want 400", negRebalRec.Code)
	}
}

func TestWebhookOutboxRESTListAndArchive(t *testing.T) {
	outboxDir := filepath.Join(t.TempDir(), "outbox")
	t.Setenv("NTM_WEBHOOK_OUTBOX_DIR", outboxDir)
	projectDir := t.TempDir()

	hookReceiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer hookReceiver.Close()

	cfgYAML := "webhooks:\n  - name: serve-wave13-hook\n    url: " + hookReceiver.URL + "\n    events: [agent.error]\n    formatter: json\n    retry:\n      max_attempts: 1\n"
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm.yaml"), []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	mgr, err := webhook.NewProjectWebhookManager(projectDir, &redaction.Config{Mode: redaction.ModeOff})
	if err != nil {
		t.Fatalf("NewProjectWebhookManager: %v", err)
	}
	if err := mgr.Start(); err != nil {
		t.Fatalf("mgr.Start: %v", err)
	}
	if err := mgr.Dispatch(webhook.Event{
		ID:      "evt-serve-wave13",
		Type:    "agent.error",
		Session: "wave13-sess",
		Message: "agent crashed",
	}); err != nil {
		t.Fatalf("mgr.Dispatch: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		snap, err := mgr.ListOutbox()
		if err == nil && len(snap.Entries) == 1 && strings.EqualFold(snap.Entries[0].State, "failed") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for failed outbox entry; snap=%+v err=%v", snap, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := mgr.Stop(); err != nil {
		t.Fatalf("mgr.Stop: %v", err)
	}

	srv := New(Config{
		Port:       0,
		Version:    "1.39.0-wave13",
		ProjectDir: projectDir,
	})
	t.Cleanup(func() { srv.Stop() })
	router := srv.Router()

	// 1. GET /api/v1/webhooks/outbox -> 200 with 1 failed entry
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/api/v1/webhooks/outbox", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/webhooks/outbox status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var listed WebhookOutboxEnvelopeResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode outbox list: %v", err)
	}
	if !listed.Success || listed.Snapshot == nil || len(listed.Snapshot.Entries) != 1 {
		t.Fatalf("expected 1 outbox entry, got %+v", listed)
	}
	entry := listed.Snapshot.Entries[0]

	// 2. Missing revision on retry -> 400
	missingRevRec := httptest.NewRecorder()
	missingRevReq := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/outbox/"+entry.Record+"/retry", strings.NewReader(`{}`))
	missingRevReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(missingRevRec, missingRevReq)
	if missingRevRec.Code != http.StatusBadRequest {
		t.Fatalf("POST retry without revision status=%d, want 400", missingRevRec.Code)
	}

	// 3. Archive failed receipt via POST /api/v1/webhooks/outbox/{record}/archive -> 200
	archiveRec := httptest.NewRecorder()
	archiveBody := `{"revision":"` + entry.Revision + `"}`
	archiveReq := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/outbox/"+entry.Record+"/archive", strings.NewReader(archiveBody))
	archiveReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(archiveRec, archiveReq)
	if archiveRec.Code != http.StatusOK {
		t.Fatalf("POST archive status=%d body=%s", archiveRec.Code, archiveRec.Body.String())
	}
	var archived WebhookOutboxEnvelopeResponse
	if err := json.Unmarshal(archiveRec.Body.Bytes(), &archived); err != nil {
		t.Fatalf("decode archive response: %v", err)
	}
	if !archived.Success || archived.Archive == nil || archived.Archive.Record != entry.Record {
		t.Fatalf("unexpected archive response: %+v", archived)
	}
}

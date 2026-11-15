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

	"github.com/Dicklesworthstone/ntm/internal/events"
)

func TestAnalyticsOpenAPIReflection(t *testing.T) {
	spec, err := GenerateOpenAPISpecFromRouter("dev", "http://127.0.0.1:7337")
	if err != nil {
		t.Fatalf("GenerateOpenAPISpecFromRouter: %v", err)
	}

	item, ok := spec.Paths["/api/v1/analytics"]
	if !ok || item.Get == nil {
		t.Fatalf("missing GET /api/v1/analytics in OpenAPI spec")
	}

	op := item.Get
	resp200, ok := op.Responses["200"]
	if !ok {
		t.Fatalf("GET /api/v1/analytics missing 200 response")
	}
	const wantRef = "#/components/schemas/AnalyticsEnvelopeResponse"
	if got := resp200.Content["application/json"].Schema.Ref; got != wantRef {
		t.Fatalf("GET /api/v1/analytics 200 ref = %q, want %q", got, wantRef)
	}

	compSchema := spec.Components.Schemas["AnalyticsEnvelopeResponse"]
	if compSchema == nil {
		t.Fatalf("component schema AnalyticsEnvelopeResponse not emitted")
	}
	for _, wantProp := range []string{
		"period",
		"total_sessions",
		"total_agents",
		"total_prompts",
		"total_tokens_estimated",
		"agent_breakdown",
		"error_types",
		"agent_crashes",
		"agent_restarts",
	} {
		if _, hasProp := compSchema.Properties[wantProp]; !hasProp {
			t.Fatalf("AnalyticsEnvelopeResponse missing property %q", wantProp)
		}
	}

	gotParams := make(map[string]bool, len(op.Parameters))
	for _, p := range op.Parameters {
		if p.In == "query" {
			gotParams[p.Name] = true
		}
	}
	for _, wantParam := range []string{"days", "since", "sessions", "format"} {
		if !gotParams[wantParam] {
			t.Fatalf("GET /api/v1/analytics missing query parameter %q", wantParam)
		}
	}
}

func TestAnalyticsRESTEndpointJSONAndPrometheus(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))

	srv := NewHermeticServer("dev")
	t.Cleanup(srv.Stop)
	router := srv.Router()

	// 1. Missing events.jsonl -> graceful 200 OK empty analytics envelope
	rec, emptyPayload := doServeJSONRequest(t, router, http.MethodGet, "/api/v1/analytics?days=7&sessions=true", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty GET /api/v1/analytics code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if emptyPayload["total_sessions"] != float64(0) {
		t.Fatalf("expected total_sessions=0 on missing log, got %#v", emptyPayload["total_sessions"])
	}
	sessionsSlice, ok := emptyPayload["sessions"].([]any)
	if !ok || len(sessionsSlice) != 0 {
		t.Fatalf("expected empty sessions array when ?sessions=true, got %#v", emptyPayload["sessions"])
	}

	// 2. Seed multi-harness events into ~/.config/ntm/analytics/events.jsonl
	logPath := filepath.Join(tmpHome, ".config", "ntm", "analytics", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatalf("mkdir analytics dir: %v", err)
	}
	now := time.Now().UTC()
	seedEvents := []events.Event{
		{
			Timestamp: now.Add(-2 * time.Hour),
			Type:      events.EventSessionCreate,
			Session:   "wave10-swarm",
			Data: map[string]any{
				"claude_count":      2,
				"antigravity_count": 1,
				"opencode_count":    1,
				"ollama_count":      1,
			},
		},
		{
			Timestamp: now.Add(-90 * time.Minute),
			Type:      events.EventPromptSend,
			Session:   "wave10-swarm",
			Data: map[string]any{
				"prompt_length":    400,
				"estimated_tokens": 120,
				"target_types":     "claude,agy,oc",
			},
		},
		{
			Timestamp: now.Add(-45 * time.Minute),
			Type:      events.EventAgentCrash,
			Session:   "wave10-swarm",
			Data:      map[string]any{"agent_type": "oc"},
		},
		{
			Timestamp: now.Add(-40 * time.Minute),
			Type:      events.EventAgentRestart,
			Session:   "wave10-swarm",
			Data:      map[string]any{"agent_type": "oc"},
		},
		{
			Timestamp: now.Add(-30 * time.Minute),
			Type:      events.EventError,
			Session:   "wave10-swarm",
			Data:      map[string]any{"error_type": "rate_limit"},
		},
	}
	var lines strings.Builder
	for _, ev := range seedEvents {
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal seed event: %v", err)
		}
		lines.Write(raw)
		lines.WriteByte('\n')
	}
	if err := os.WriteFile(logPath, []byte(lines.String()), 0o600); err != nil {
		t.Fatalf("write seed events.jsonl: %v", err)
	}

	// 3. Verify JSON response with multi-harness breakdown & per-session summary
	rec, payload := doServeJSONRequest(t, router, http.MethodGet, "/api/v1/analytics?days=7&sessions=true", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/analytics code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if payload["total_sessions"] != float64(1) || payload["total_agents"] != float64(5) {
		t.Fatalf("unexpected totals: sessions=%v agents=%v", payload["total_sessions"], payload["total_agents"])
	}
	if payload["total_prompts"] != float64(1) || payload["total_tokens_estimated"] != float64(120) {
		t.Fatalf("unexpected prompt/token totals: prompts=%v tokens=%v", payload["total_prompts"], payload["total_tokens_estimated"])
	}
	if payload["agent_crashes"] != float64(1) || payload["agent_restarts"] != float64(1) || payload["error_count"] != float64(1) {
		t.Fatalf("unexpected crash/restart/error counts: %#v", payload)
	}

	breakdown, ok := payload["agent_breakdown"].(map[string]any)
	if !ok {
		t.Fatalf("missing agent_breakdown map: %#v", payload["agent_breakdown"])
	}
	for _, wantAgent := range []string{"claude", "antigravity", "oc", "ollama"} {
		if _, exists := breakdown[wantAgent]; !exists {
			t.Fatalf("agent_breakdown missing harness %q: %#v", wantAgent, breakdown)
		}
	}

	sessionsOut, ok := payload["sessions"].([]any)
	if !ok || len(sessionsOut) != 1 {
		t.Fatalf("expected 1 session summary, got %#v", payload["sessions"])
	}
	sessMap := sessionsOut[0].(map[string]any)
	if sessMap["name"] != "wave10-swarm" || sessMap["agent_count"] != float64(5) || sessMap["prompt_count"] != float64(1) {
		t.Fatalf("unexpected session summary: %#v", sessMap)
	}

	// 4. Verify Prometheus exposition via ?format=prometheus and Accept: text/plain
	promReq := httptest.NewRequest(http.MethodGet, "/api/v1/analytics?days=7&format=prometheus", nil)
	promRec := httptest.NewRecorder()
	router.ServeHTTP(promRec, promReq)
	if promRec.Code != http.StatusOK {
		t.Fatalf("Prometheus GET /api/v1/analytics code = %d, body=%s", promRec.Code, promRec.Body.String())
	}
	if ct := promRec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("expected text/plain Content-Type, got %q", ct)
	}
	promBody := promRec.Body.String()
	for _, snippet := range []string{
		`ntm_sessions_total{period="Last 7 days"} 1`,
		`agent_type="antigravity"} 1`,
		`agent_type="oc"} 1`,
		`ntm_agent_crashes_total{period="Last 7 days"} 1`,
		`error_type="rate_limit"} 1`,
	} {
		if !strings.Contains(promBody, snippet) {
			t.Fatalf("Prometheus output missing %q:\n%s", snippet, promBody)
		}
	}

	acceptReq := httptest.NewRequest(http.MethodGet, "/api/v1/analytics?days=7", nil)
	acceptReq.Header.Set("Accept", "text/plain")
	acceptRec := httptest.NewRecorder()
	router.ServeHTTP(acceptRec, acceptReq)
	if acceptRec.Code != http.StatusOK || !strings.Contains(acceptRec.Body.String(), "ntm_sessions_total") {
		t.Fatalf("Accept: text/plain negotiation failed: code=%d body=%s", acceptRec.Code, acceptRec.Body.String())
	}

	// 5. Validation error conditions -> 400 Bad Request
	for _, badQuery := range []string{
		"/api/v1/analytics?days=-1",
		"/api/v1/analytics?days=abc",
		"/api/v1/analytics?since=not-a-date",
		"/api/v1/analytics?sessions=maybe",
		"/api/v1/analytics?format=xml",
	} {
		badRec, _ := doServeJSONRequest(t, router, http.MethodGet, badQuery, nil)
		if badRec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d (%s)", badQuery, badRec.Code, badRec.Body.String())
		}
	}
}

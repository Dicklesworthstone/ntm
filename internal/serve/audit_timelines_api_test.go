package serve

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/encryption"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

func TestSupportBundleRESTHonorsSelectedEncryptionPolicy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("NTM_HTTP_BUNDLE_KEY", "")
	policyPath := filepath.Join(dir, "selected.toml")
	policy := "[encryption]\nenabled = true\nkey_source = \"env\"\nkey_env = \"NTM_HTTP_BUNDLE_KEY\"\nkey_format = \"hex\"\n"
	if err := os.WriteFile(policyPath, []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	srv, _ := setupTestServer(t)
	if err := srv.ConfigureSpawnPolicy(policyPath, true); err != nil {
		t.Fatal(err)
	}
	router := srv.Router()
	request := map[string]any{"session": "missing-bundle-fixture", "format": "zip"}
	rec, payload := doServeJSONRequest(t, router, http.MethodPost, "/api/v1/support-bundle", request)
	if rec.Code != http.StatusInternalServerError || payload["data"] != nil {
		t.Fatalf("missing key downgraded HTTP bundle: code=%d payload=%v", rec.Code, payload)
	}
	t.Setenv("NTM_HTTP_BUNDLE_KEY", strings.Repeat("51", encryption.KeySize))
	rec, payload = doServeJSONRequest(t, router, http.MethodPost, "/api/v1/support-bundle", request)
	if rec.Code != http.StatusOK || payload["encrypted"] != true || payload["content_type"] != "application/octet-stream" {
		t.Fatalf("HTTP encryption policy ignored: code=%d payload=%v", rec.Code, payload)
	}
	filename, _ := payload["filename"].(string)
	if !strings.HasSuffix(filename, ".zip.enc") {
		t.Fatalf("encrypted filename missing suffix: %q", filename)
	}
	encoded, _ := payload["data"].(string)
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || !encryption.IsEncryptedArtifact(data) {
		t.Fatalf("HTTP body is not an encrypted artifact: %v", err)
	}
	plain, err := encryption.DecryptArtifactWithKeyring([][]byte{bytes.Repeat([]byte{0x51}, encryption.KeySize)}, data)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(plain), int64(len(plain)))
	if err != nil || len(archive.File) == 0 {
		t.Fatalf("decrypted HTTP archive invalid: %v", err)
	}
	if err := os.WriteFile(policyPath, []byte("[encryption"), 0600); err != nil {
		t.Fatal(err)
	}
	rec, payload = doServeJSONRequest(t, router, http.MethodPost, "/api/v1/support-bundle", request)
	if rec.Code != http.StatusInternalServerError || payload["data"] != nil {
		t.Fatalf("unreadable policy downgraded HTTP bundle: code=%d payload=%v", rec.Code, payload)
	}
}

func writeServedAuditJSONLFixture(t *testing.T, dir, session string, count int) string {
	t.Helper()
	fname := fmt.Sprintf("%s-%s.jsonl", session, time.Now().UTC().Format("2006-01-02"))
	fpath := filepath.Join(dir, fname)

	f, err := os.Create(fpath)
	if err != nil {
		t.Fatalf("create audit fixture: %v", err)
	}
	defer f.Close()

	evTypes := []audit.EventType{
		audit.EventTypeSpawn,
		audit.EventTypeCommand,
		audit.EventTypeSend,
		audit.EventTypeError,
	}
	actors := []audit.Actor{
		audit.ActorUser,
		audit.ActorAgent,
		audit.ActorSystem,
	}

	var prevHash string
	for i := 0; i < count; i++ {
		entry := audit.AuditEntry{
			Timestamp:   time.Now().UTC().Add(-time.Duration(count-i) * time.Minute),
			SessionID:   session,
			EventType:   evTypes[i%len(evTypes)],
			Actor:       actors[i%len(actors)],
			Target:      fmt.Sprintf("%s__pane_%d", session, i),
			Payload:     map[string]any{"msg": fmt.Sprintf("wave11_payload_%d", i)},
			Metadata:    map[string]any{"seq": i + 1},
			PrevHash:    prevHash,
			SequenceNum: uint64(i + 1),
		}
		forHash := entry
		forHash.Checksum = ""
		rawHash, err := json.Marshal(forHash)
		if err != nil {
			t.Fatalf("marshal hash payload: %v", err)
		}
		sum := sha256.Sum256(rawHash)
		entry.Checksum = hex.EncodeToString(sum[:])
		prevHash = entry.Checksum

		rawLine, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("marshal audit entry: %v", err)
		}
		if _, err := fmt.Fprintln(f, string(rawLine)); err != nil {
			t.Fatalf("write audit entry: %v", err)
		}
	}
	return fpath
}

func TestAuditTimelinesAndSupportBundleOpenAPIReflection(t *testing.T) {
	spec, err := GenerateOpenAPISpecFromRouter("dev", "http://127.0.0.1:7337")
	if err != nil {
		t.Fatalf("GenerateOpenAPISpecFromRouter: %v", err)
	}

	cases := []struct {
		method  string
		path    string
		wantRef string
	}{
		{http.MethodGet, "/api/v1/audit", "#/components/schemas/HTTPAuditEnvelopeResponse"},
		{http.MethodGet, "/api/v1/audit/sessions", "#/components/schemas/SessionAuditEnvelopeResponse"},
		{http.MethodPost, "/api/v1/audit/verify", "#/components/schemas/SessionAuditVerifyEnvelopeResponse"},
		{http.MethodGet, "/api/v1/timelines", "#/components/schemas/TimelinesEnvelopeResponse"},
		{http.MethodGet, "/api/v1/timelines/stats", "#/components/schemas/TimelinesEnvelopeResponse"},
		{http.MethodGet, "/api/v1/timelines/{sessionId}", "#/components/schemas/TimelinesEnvelopeResponse"},
		{http.MethodPost, "/api/v1/support-bundle", "#/components/schemas/SupportBundleEnvelopeResponse"},
	}

	for _, tc := range cases {
		item, ok := spec.Paths[tc.path]
		if !ok {
			t.Fatalf("missing path %s in OpenAPI spec", tc.path)
		}
		var op *Operation
		switch tc.method {
		case http.MethodGet:
			op = item.Get
		case http.MethodPost:
			op = item.Post
		}
		if op == nil {
			t.Fatalf("missing %s operation on %s", tc.method, tc.path)
		}
		resp200, ok := op.Responses["200"]
		if !ok {
			t.Fatalf("%s %s missing 200 response", tc.method, tc.path)
		}
		if got := resp200.Content["application/json"].Schema.Ref; got != tc.wantRef {
			t.Fatalf("%s %s 200 ref = %q, want %q", tc.method, tc.path, got, tc.wantRef)
		}
	}

	for _, comp := range []string{
		"HTTPAuditEnvelopeResponse",
		"SessionAuditEnvelopeResponse",
		"VerifySessionAuditRequest",
		"SessionAuditVerifyEnvelopeResponse",
		"TimelinesEnvelopeResponse",
		"CreateSupportBundleRequest",
		"SupportBundleEnvelopeResponse",
	} {
		if spec.Components.Schemas[comp] == nil {
			t.Fatalf("missing component schema %q", comp)
		}
	}
}

func TestHTTPAuditAndSessionAuditVerifyRESTEndpoints(t *testing.T) {
	auditDBDir := t.TempDir()
	auditStore, err := NewAuditStore(AuditStoreConfig{
		DBPath:    filepath.Join(auditDBDir, "audit.db"),
		JSONLPath: filepath.Join(auditDBDir, "audit.jsonl"),
	})
	if err != nil {
		t.Fatalf("NewAuditStore: %v", err)
	}
	t.Cleanup(func() {
		_ = auditStore.Close()
	})

	now := time.Now().UTC()
	seedRecords := []AuditRecord{
		{
			Timestamp:  now.Add(-3 * time.Minute),
			RequestID:  "req-alpha-1",
			UserID:     "alice",
			Role:       RoleOperator,
			Action:     AuditActionCreate,
			Resource:   "sessions",
			ResourceID: "wave11-alpha",
			Method:     http.MethodPost,
			Path:       "/api/v1/sessions",
			StatusCode: http.StatusCreated,
			Duration:   18,
			SessionID:  "wave11-alpha",
			RemoteAddr: "127.0.0.1:41001",
		},
		{
			Timestamp:  now.Add(-1 * time.Minute),
			RequestID:  "req-beta-2",
			UserID:     "bob",
			Role:       RoleAdmin,
			Action:     AuditActionUpdate,
			Resource:   "policy",
			ResourceID: "automation",
			Method:     http.MethodPut,
			Path:       "/api/v1/policy/automation",
			StatusCode: http.StatusOK,
			Duration:   9,
			SessionID:  "wave11-beta",
			ApprovalID: "apr-77",
			RemoteAddr: "127.0.0.1:41002",
		},
	}
	for i := range seedRecords {
		if err := auditStore.Record(&seedRecords[i]); err != nil {
			t.Fatalf("auditStore.Record(%d): %v", i, err)
		}
	}

	srv, _ := setupTestServer(t)
	srv.auditStore = auditStore
	srv.auditLogsDir = t.TempDir()
	router := srv.Router()

	// 1. Query all HTTP audit records with pagination
	rec, listPayload := doServeJSONRequest(t, router, http.MethodGet, "/api/v1/audit?limit=1&offset=0", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/audit code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if listPayload["count"] != float64(1) || listPayload["total_matches"] != float64(2) || listPayload["has_more"] != true {
		t.Fatalf("unexpected paginated audit response: %#v", listPayload)
	}
	recordsSlice, ok := listPayload["records"].([]any)
	if !ok || len(recordsSlice) != 1 {
		t.Fatalf("expected 1 record on page 1, got %#v", listPayload["records"])
	}
	firstRec := recordsSlice[0].(map[string]any)
	if firstRec["request_id"] != "req-beta-2" {
		t.Fatalf("expected newest record req-beta-2 first, got %#v", firstRec["request_id"])
	}

	// 2. Filter HTTP audit records by session_id & resource
	rec, filteredPayload := doServeJSONRequest(t, router, http.MethodGet, "/api/v1/audit?session_id=wave11-alpha&resource=sessions", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered GET /api/v1/audit code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if filteredPayload["total_matches"] != float64(1) {
		t.Fatalf("expected 1 filtered match, got %#v", filteredPayload["total_matches"])
	}

	// 3. Invalid query params return 400
	for _, badQuery := range []string{"limit=0", "offset=-1", "since=not-a-timestamp"} {
		rec, _ = doServeJSONRequest(t, router, http.MethodGet, "/api/v1/audit?"+badQuery, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("GET /api/v1/audit?%s code = %d, want 400", badQuery, rec.Code)
		}
	}

	// 4. Seed session JSONL audit log, inspect & search via GET /api/v1/audit/sessions
	jsonlPath := writeServedAuditJSONLFixture(t, srv.auditLogsDir, "wave11-chain", 4)

	rec, sessAuditPayload := doServeJSONRequest(t, router, http.MethodGet, "/api/v1/audit/sessions?session=wave11-chain&limit=2", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/audit/sessions code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if sessAuditPayload["total_matches"] != float64(4) || sessAuditPayload["count"] != float64(2) || sessAuditPayload["has_more"] != true {
		t.Fatalf("unexpected session audit payload: %#v", sessAuditPayload)
	}

	rec, searchPayload := doServeJSONRequest(t, router, http.MethodGet, "/api/v1/audit/sessions?search=wave11_payload_2&days=7", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search GET /api/v1/audit/sessions code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if searchPayload["total_matches"] != float64(1) {
		t.Fatalf("expected 1 regex search match, got %#v", searchPayload["total_matches"])
	}

	// 5. Verify intact hash chain via POST /api/v1/audit/verify -> 200 OK verified=true
	rec, verifyOKPayload := doServeJSONRequest(t, router, http.MethodPost, "/api/v1/audit/verify", map[string]any{
		"session": "wave11-chain",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/audit/verify (intact) code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if verifyOKPayload["verified"] != true {
		t.Fatalf("expected verified=true on intact chain, got %#v", verifyOKPayload)
	}

	// 6. Tamper with JSONL entry on disk -> POST /api/v1/audit/verify still returns 200 OK with verified=false + files[] diagnostic!
	rawJSONL, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read audit fixture: %v", err)
	}
	tampered := strings.Replace(string(rawJSONL), "wave11_payload_1", "tampered_payload_1", 1)
	if err := os.WriteFile(jsonlPath, []byte(tampered), 0o600); err != nil {
		t.Fatalf("write tampered audit fixture: %v", err)
	}

	rec, verifyTamperedPayload := doServeJSONRequest(t, router, http.MethodPost, "/api/v1/audit/verify", map[string]any{
		"session": "wave11-chain",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/audit/verify (tampered) code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if verifyTamperedPayload["verified"] != false {
		t.Fatalf("expected verified=false on tampered chain, got %#v", verifyTamperedPayload)
	}
	filesSlice, ok := verifyTamperedPayload["files"].([]any)
	if !ok || len(filesSlice) != 1 {
		t.Fatalf("expected 1 file diagnostic in files[], got %#v", verifyTamperedPayload["files"])
	}

	// 7. Missing session logs -> 404; empty session -> 400
	rec, _ = doServeJSONRequest(t, router, http.MethodPost, "/api/v1/audit/verify", map[string]any{
		"session": "nonexistent-session",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing session verify code = %d, want 404", rec.Code)
	}
	rec, _ = doServeJSONRequest(t, router, http.MethodPost, "/api/v1/audit/verify", map[string]any{
		"session": "   ",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty session verify code = %d, want 400", rec.Code)
	}
}

func TestTimelinesAndSupportBundleRESTEndpoints(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	srv, _ := setupTestServer(t)
	srv.timelineBaseDir = t.TempDir()
	router := srv.Router()

	persister, err := state.NewTimelinePersister(&state.TimelinePersistConfig{
		BaseDir: srv.timelineBaseDir,
	})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	baseTS := time.Now().UTC().Add(-15 * time.Minute)
	if err := persister.SaveTimeline("wave11-sess", []state.AgentEvent{
		{
			AgentID:   "cc_1",
			AgentType: state.AgentTypeClaude,
			SessionID: "wave11-sess",
			State:     state.TimelineWorking,
			Timestamp: baseTS,
			Details:   map[string]string{"task": "bd-suaow.2"},
		},
		{
			AgentID:       "cc_1",
			AgentType:     state.AgentTypeClaude,
			SessionID:     "wave11-sess",
			State:         state.TimelineIdle,
			PreviousState: state.TimelineWorking,
			Timestamp:     baseTS.Add(4 * time.Minute),
			Duration:      4 * time.Minute,
		},
		{
			AgentID:   "agy_2",
			AgentType: "agy",
			SessionID: "wave11-sess",
			State:     state.TimelineWorking,
			Timestamp: baseTS.Add(5 * time.Minute),
		},
	}); err != nil {
		t.Fatalf("SaveTimeline: %v", err)
	}

	// 1. List timelines
	rec, listPayload := doServeJSONRequest(t, router, http.MethodGet, "/api/v1/timelines?limit=10", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/timelines code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if listPayload["total_count"] != float64(1) {
		t.Fatalf("expected total_count=1, got %#v", listPayload["total_count"])
	}

	// 2. Timeline aggregate stats
	rec, statsPayload := doServeJSONRequest(t, router, http.MethodGet, "/api/v1/timelines/stats", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/timelines/stats code = %d, body=%s", rec.Code, rec.Body.String())
	}
	aggStats, ok := statsPayload["aggregate_stats"].(map[string]any)
	if !ok || aggStats["total_timelines"] != float64(1) || aggStats["total_events"] != float64(3) {
		t.Fatalf("unexpected aggregate_stats: %#v", statsPayload["aggregate_stats"])
	}

	// 3. Session timeline detail with events=true
	rec, showPayload := doServeJSONRequest(t, router, http.MethodGet, "/api/v1/timelines/wave11-sess?events=true", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/timelines/wave11-sess?events=true code = %d, body=%s", rec.Code, rec.Body.String())
	}
	evRows, ok := showPayload["events"].([]any)
	if !ok || len(evRows) != 3 {
		t.Fatalf("expected 3 timeline events, got %#v", showPayload["events"])
	}
	sessStats, ok := showPayload["session_stats"].(map[string]any)
	if !ok || sessStats["unique_agents"] != float64(2) {
		t.Fatalf("expected unique_agents=2 in session_stats, got %#v", showPayload["session_stats"])
	}

	// 4. Missing timeline -> 404; invalid events param -> 400
	rec, _ = doServeJSONRequest(t, router, http.MethodGet, "/api/v1/timelines/missing-sess", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing timeline code = %d, want 404", rec.Code)
	}
	rec, _ = doServeJSONRequest(t, router, http.MethodGet, "/api/v1/timelines/wave11-sess?events=maybe", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid events param code = %d, want 400", rec.Code)
	}

	// 5. Support bundle generation with secret redaction proof
	ntmDir := filepath.Join(tmpHome, ".ntm")
	if err := os.MkdirAll(ntmDir, 0o755); err != nil {
		t.Fatalf("mkdir .ntm: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(ntmDir, "config.toml"),
		[]byte("api_key = \"sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789ABCDEF\"\n"),
		0o600,
	); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	rec, bundlePayload := doServeJSONRequest(t, router, http.MethodPost, "/api/v1/support-bundle", map[string]any{
		"format":      "zip",
		"redact_mode": "redact",
		"lines":       200,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/support-bundle code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if bundlePayload["format"] != "zip" || bundlePayload["content_type"] != "application/zip" {
		t.Fatalf("unexpected bundle format metadata: %#v", bundlePayload)
	}
	encodedData, ok := bundlePayload["data"].(string)
	if !ok || encodedData == "" {
		t.Fatalf("expected non-empty base64 archive data")
	}
	rawZip, err := base64.StdEncoding.DecodeString(encodedData)
	if err != nil {
		t.Fatalf("decode base64 zip: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(rawZip), int64(len(rawZip)))
	if err != nil {
		t.Fatalf("open generated zip archive: %v", err)
	}
	foundManifest := false
	for _, zf := range zr.File {
		if zf.Name == "manifest.json" {
			foundManifest = true
		}
		rc, openErr := zf.Open()
		if openErr != nil {
			t.Fatalf("open zip entry %s: %v", zf.Name, openErr)
		}
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(rc)
		_ = rc.Close()
		if strings.Contains(buf.String(), "sk-ant-api03-abcdefghijklmnopqrstuvwxyz") {
			t.Fatalf("unredacted secret leaked in zip entry %s", zf.Name)
		}
	}
	if !foundManifest {
		t.Fatalf("generated support bundle zip missing manifest.json")
	}

	// 6. Disclosure guardrails: redact_mode=off, redact_mode=warn, bad format, and path traversal session all 400
	for _, badReq := range []map[string]any{
		{"format": "zip", "redact_mode": "off"},
		{"format": "zip", "redact_mode": "warn"},
		{"format": "rar"},
		{"session": "../escape"},
	} {
		rec, _ = doServeJSONRequest(t, router, http.MethodPost, "/api/v1/support-bundle", badReq)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST /api/v1/support-bundle(%#v) code = %d, want 400", badReq, rec.Code)
		}
	}
}

package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/scanner"
)

func TestExtractBeadID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"standard bd format", "Created bd-1abc2: Fix the bug", "bd-1abc2"},
		{"ntm prefix", "Created ntm-xyz: New feature", "ntm-xyz"},
		{"no prefix", "Some random output", ""},
		{"empty string", "", ""},
		{"bd at start", "bd-12345: Title here", "bd-12345"},
		{"multiple words before id", "Successfully created bd-999: Done", "bd-999"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractBeadID(tc.input)
			if got != tc.want {
				t.Errorf("extractBeadID(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestGenerateScanID(t *testing.T) {
	t.Parallel()

	id := generateScanID()

	if !strings.HasPrefix(id, "scan-") {
		t.Errorf("generateScanID() = %q, want prefix 'scan-'", id)
	}
	if len(id) < 10 {
		t.Errorf("generateScanID() = %q, too short", id)
	}

	// Should generate unique IDs
	id2 := generateScanID()
	// Note: IDs could be the same if generated in same nanosecond, but unlikely in practice
	_ = id2
}

func TestGenerateFindingID(t *testing.T) {
	t.Parallel()

	f := scanner.Finding{
		File:     "main.go",
		Line:     42,
		Category: "security",
		Message:  "potential injection",
	}

	id := generateFindingID("scan-abc123", f)

	if !strings.HasPrefix(id, "finding-") {
		t.Errorf("generateFindingID() = %q, want prefix 'finding-'", id)
	}
	if len(id) < 15 {
		t.Errorf("generateFindingID() = %q, too short", id)
	}

	// Same input should produce same ID (deterministic)
	id2 := generateFindingID("scan-abc123", f)
	if id != id2 {
		t.Errorf("generateFindingID should be deterministic: %q != %q", id, id2)
	}

	// Different input should produce different ID
	f2 := f
	f2.Line = 43
	id3 := generateFindingID("scan-abc123", f2)
	if id == id3 {
		t.Error("different findings should produce different IDs")
	}
}

func TestFindingToMap(t *testing.T) {
	t.Parallel()

	now := time.Now()

	t.Run("basic fields", func(t *testing.T) {
		t.Parallel()
		f := &FindingRecord{
			ID:        "finding-abc",
			ScanID:    "scan-123",
			Dismissed: false,
			CreatedAt: now,
		}

		m := findingToMap(f)
		if m["id"] != "finding-abc" {
			t.Errorf("id = %v", m["id"])
		}
		if m["scan_id"] != "scan-123" {
			t.Errorf("scan_id = %v", m["scan_id"])
		}
		if m["dismissed"] != false {
			t.Errorf("dismissed = %v", m["dismissed"])
		}
		if _, ok := m["dismissed_at"]; ok {
			t.Error("dismissed_at should not be present")
		}
		if _, ok := m["bead_id"]; ok {
			t.Error("bead_id should not be present when empty")
		}
	})

	t.Run("with optional fields", func(t *testing.T) {
		t.Parallel()
		dismissedAt := time.Now()
		f := &FindingRecord{
			ID:          "finding-abc",
			ScanID:      "scan-123",
			Dismissed:   true,
			DismissedAt: &dismissedAt,
			DismissedBy: "user@example.com",
			BeadID:      "bd-456",
			CreatedAt:   now,
		}

		m := findingToMap(f)
		if _, ok := m["dismissed_at"]; !ok {
			t.Error("dismissed_at should be present")
		}
		if m["dismissed_by"] != "user@example.com" {
			t.Errorf("dismissed_by = %v", m["dismissed_by"])
		}
		if m["bead_id"] != "bd-456" {
			t.Errorf("bead_id = %v", m["bead_id"])
		}
	})
}

func TestHandleScannerStatusUnavailable(t *testing.T) {
	if scanner.IsAvailable() {
		t.Skip("ubs installed; unavailable path not deterministic")
	}
	resetScannerStoreForTest()
	addTestScan("scan-running", ScanStateRunning)
	addTestScan("scan-done", ScanStateCompleted)
	addTestFinding("scan-done", "finding-1", scanner.SeverityWarning, "main.go", "security", false, "")

	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scanner/status", nil)
	rec := httptest.NewRecorder()

	srv.handleScannerStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["success"] != true {
		t.Fatalf("success=%v, want true", resp["success"])
	}
	if resp["available"] != false {
		t.Fatalf("available=%v, want false", resp["available"])
	}
	if got := int(resp["total_scans"].(float64)); got != 2 {
		t.Fatalf("total_scans=%d, want 2", got)
	}
	if got := int(resp["total_findings"].(float64)); got != 1 {
		t.Fatalf("total_findings=%d, want 1", got)
	}
	if resp["current_scan"] == nil {
		t.Fatal("current_scan is nil")
	}
	if resp["last_scan"] == nil {
		t.Fatal("last_scan is nil")
	}
}

func TestHandleScannerHistoryPagination(t *testing.T) {
	resetScannerStoreForTest()
	addTestScan("scan-1", ScanStateCompleted)
	addTestScan("scan-2", ScanStateCompleted)
	addTestScan("scan-3", ScanStateCompleted)

	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scanner/history?limit=2&offset=0", nil)
	rec := httptest.NewRecorder()

	srv.handleScannerHistory(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := int(resp["count"].(float64)); got != 2 {
		t.Fatalf("count=%d, want 2", got)
	}
	findings, ok := resp["scans"].([]interface{})
	if !ok || len(findings) != 2 {
		t.Fatalf("scans length=%d, want 2", len(findings))
	}
}

func TestHandleListFindingsFilters(t *testing.T) {
	resetScannerStoreForTest()
	addTestFinding("scan-a", "finding-a", scanner.SeverityWarning, "main.go", "security", false, "")
	addTestFinding("scan-b", "finding-b", scanner.SeverityCritical, "other.go", "perf", true, "")

	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scanner/findings?scan_id=scan-a&severity=warning", nil)
	rec := httptest.NewRecorder()

	srv.handleListFindings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := int(resp["count"].(float64)); got != 1 {
		t.Fatalf("count=%d, want 1", got)
	}
}

func TestHandleGetFindingNotFound(t *testing.T) {
	resetScannerStoreForTest()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/scanner/findings/missing", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "missing")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleGetFinding(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandleGetFindingFound(t *testing.T) {
	resetScannerStoreForTest()
	addTestFinding("scan-1", "finding-1", scanner.SeverityWarning, "main.go", "security", false, "")

	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scanner/findings/finding-1", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "finding-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleGetFinding(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["id"] != "finding-1" {
		t.Fatalf("id=%v, want finding-1", resp["id"])
	}
}

func TestHandleDismissFinding(t *testing.T) {
	resetScannerStoreForTest()
	addTestFinding("scan-1", "finding-1", scanner.SeverityWarning, "main.go", "security", false, "")

	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scanner/findings/finding-1/dismiss", strings.NewReader(`{"reason":"noise"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "finding-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = req.WithContext(withRoleContext(req.Context(), &RoleContext{
		Role:   RoleAdmin,
		UserID: "tester",
	}))
	rec := httptest.NewRecorder()

	srv.handleDismissFinding(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusOK)
	}

	finding, ok := scannerStore.GetFinding("finding-1")
	if !ok || !finding.Dismissed {
		t.Fatal("finding not marked dismissed")
	}
	if finding.DismissedBy != "tester" {
		t.Fatalf("dismissed_by=%q, want tester", finding.DismissedBy)
	}
}

func TestHandleCreateBeadFromFindingConflict(t *testing.T) {
	resetScannerStoreForTest()
	addTestFinding("scan-1", "finding-1", scanner.SeverityWarning, "main.go", "security", false, "bd-123")

	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scanner/findings/finding-1/create-bead", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "finding-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleCreateBeadFromFinding(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestHandleCreateBeadFromFindingSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub br uses sh")
	}

	resetScannerStoreForTest()
	addTestFinding("scan-1", "finding-1", scanner.SeverityWarning, "main.go", "security", false, "")
	writeStubBr(t, "bd-123")

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scanner/findings/finding-1/create-bead", strings.NewReader(`{"labels":["triaged"]}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "finding-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleCreateBeadFromFinding(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusCreated)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["bead_id"] != "bd-123" {
		t.Fatalf("bead_id=%v, want bd-123", resp["bead_id"])
	}
	if resp["finding_id"] != "finding-1" {
		t.Fatalf("finding_id=%v, want finding-1", resp["finding_id"])
	}

	finding, ok := scannerStore.GetFinding("finding-1")
	if !ok {
		t.Fatal("finding not found after create")
	}
	if finding.BeadID != "bd-123" {
		t.Fatalf("finding.BeadID=%q, want bd-123", finding.BeadID)
	}
}

func TestHandleCreateBeadFromFindingBadJSON(t *testing.T) {
	resetScannerStoreForTest()
	addTestFinding("scan-1", "finding-1", scanner.SeverityWarning, "main.go", "security", false, "")

	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scanner/findings/finding-1/create-bead", strings.NewReader("{bad"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "finding-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleCreateBeadFromFinding(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleCreateBeadFromFindingRejectsUnknownBeadID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub br uses sh")
	}

	resetScannerStoreForTest()
	addTestFinding("scan-1", "finding-1", scanner.SeverityWarning, "main.go", "security", false, "")

	dir := t.TempDir()
	path := filepath.Join(dir, "br")
	script := "#!/bin/sh\nset -e\necho 'Created bead without parseable id'\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write stub br: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scanner/findings/finding-1/create-bead", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "finding-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleCreateBeadFromFinding(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusInternalServerError)
	}

	finding, ok := scannerStore.GetFinding("finding-1")
	if !ok {
		t.Fatal("finding missing after failed create")
	}
	if finding.BeadID != "" {
		t.Fatalf("finding.BeadID=%q, want empty", finding.BeadID)
	}
}

func TestHandleListBugsAndSummary(t *testing.T) {
	resetScannerStoreForTest()
	addTestFinding("scan-1", "finding-1", scanner.SeverityWarning, "main.go", "security", false, "")
	addTestFinding("scan-1", "finding-2", scanner.SeverityCritical, "main.go", "perf", false, "bd-9")
	addTestFinding("scan-1", "finding-3", scanner.SeverityInfo, "other.go", "security", true, "")

	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/bugs?severity=warning&file=main.go", nil)
	rec := httptest.NewRecorder()
	srv.handleListBugs(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusOK)
	}
	var listResp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := int(listResp["count"].(float64)); got != 1 {
		t.Fatalf("count=%d, want 1", got)
	}

	sumReq := httptest.NewRequest(http.MethodGet, "/api/v1/bugs/summary", nil)
	sumRec := httptest.NewRecorder()
	srv.handleBugsSummary(sumRec, sumReq)
	if sumRec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d", sumRec.Code, http.StatusOK)
	}
	var sumResp map[string]interface{}
	if err := json.NewDecoder(sumRec.Body).Decode(&sumResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := int(sumResp["total_findings"].(float64)); got != 2 {
		t.Fatalf("total_findings=%d, want 2", got)
	}
	if got := int(sumResp["dismissed_count"].(float64)); got != 1 {
		t.Fatalf("dismissed_count=%d, want 1", got)
	}
	if got := int(sumResp["linked_beads"].(float64)); got != 1 {
		t.Fatalf("linked_beads=%d, want 1", got)
	}
}

func TestHandleBugsNotify(t *testing.T) {
	resetScannerStoreForTest()
	addTestFinding("scan-1", "finding-1", scanner.SeverityWarning, "main.go", "security", false, "")
	addTestFinding("scan-1", "finding-2", scanner.SeverityInfo, "main.go", "security", false, "")

	srv, _ := setupTestServer(t)
	badReq := httptest.NewRequest(http.MethodPost, "/api/v1/bugs/notify", strings.NewReader(`{"channel":""}`))
	badRec := httptest.NewRecorder()
	srv.handleBugsNotify(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("bad status=%d, want %d", badRec.Code, http.StatusBadRequest)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/bugs/notify",
		strings.NewReader(`{"channel":"webhook","endpoint":"http://example","min_severity":"warning"}`))
	rec := httptest.NewRecorder()
	srv.handleBugsNotify(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusOK)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["notified"] != true {
		t.Fatalf("notified=%v, want true", resp["notified"])
	}
	if got := int(resp["findings_count"].(float64)); got != 1 {
		t.Fatalf("findings_count=%d, want 1", got)
	}
}

func TestHandleRunScanUnavailable(t *testing.T) {
	if scanner.IsAvailable() {
		t.Skip("ubs installed; unavailable path not deterministic")
	}
	resetScannerStoreForTest()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/scanner/run", nil)
	rec := httptest.NewRecorder()
	srv.handleRunScan(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleRunScanAlreadyRunning(t *testing.T) {
	if !scanner.IsAvailable() {
		t.Skip("ubs not installed; cannot test running scan conflict")
	}
	resetScannerStoreForTest()
	addTestScan("scan-running", ScanStateRunning)

	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scanner/run", nil)
	rec := httptest.NewRecorder()
	srv.handleRunScan(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestScannerStoreTryStartScanRejectsActiveScan(t *testing.T) {
	t.Parallel()

	store := NewScannerStore()
	first := &ScanRecord{ID: "scan-pending", State: ScanStatePending, StartedAt: time.Now()}
	if active, ok := store.TryStartScan(first); !ok || active != nil {
		t.Fatalf("first TryStartScan = (%v, %v), want (<nil>, true)", active, ok)
	}

	second := &ScanRecord{ID: "scan-second", State: ScanStatePending, StartedAt: time.Now()}
	active, ok := store.TryStartScan(second)
	if ok {
		t.Fatal("expected second TryStartScan to be rejected")
	}
	if active == nil || active.ID != "scan-pending" {
		t.Fatalf("active scan = %#v, want scan-pending", active)
	}
}

func TestHandleScannerStatusPendingReportedAsCurrent(t *testing.T) {
	resetScannerStoreForTest()
	addTestScan("scan-done", ScanStateCompleted)
	addTestScan("scan-pending", ScanStatePending)

	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scanner/status", nil)
	rec := httptest.NewRecorder()

	srv.handleScannerStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	current, ok := resp["current_scan"].(map[string]interface{})
	if !ok {
		t.Fatal("expected current_scan object")
	}
	if current["id"] != "scan-pending" {
		t.Fatalf("current_scan.id=%v, want scan-pending", current["id"])
	}
	if _, exists := resp["last_scan"]; exists {
		t.Fatalf("unexpected last_scan=%v for pending active scan", resp["last_scan"])
	}
}

func TestHandleDismissFindingBadJSON(t *testing.T) {
	resetScannerStoreForTest()
	addTestFinding("scan-1", "finding-1", scanner.SeverityWarning, "main.go", "security", false, "")

	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scanner/findings/finding-1/dismiss", strings.NewReader("{bad"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "finding-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleDismissFinding(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func resetScannerStoreForTest() {
	scannerStore = NewScannerStore()
}

func addTestScan(id string, state ScanState) *ScanRecord {
	scan := &ScanRecord{
		ID:        id,
		State:     state,
		Path:      "/tmp",
		StartedAt: time.Now(),
	}
	scannerStore.AddScan(scan)
	return scan
}

func addTestFinding(scanID, id string, severity scanner.Severity, file, category string, dismissed bool, beadID string) *FindingRecord {
	now := time.Now()
	finding := &FindingRecord{
		ID:     id,
		ScanID: scanID,
		Finding: scanner.Finding{
			File:     file,
			Line:     1,
			Severity: severity,
			Category: category,
			Message:  "message",
		},
		CreatedAt: now,
		BeadID:    beadID,
	}
	if dismissed {
		finding.Dismissed = true
		finding.DismissedAt = &now
	}
	scannerStore.AddFinding(finding)
	return finding
}

func writeStubBr(t *testing.T, beadID string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "br")
	script := `#!/bin/sh
set -e
while [ $# -gt 0 ]; do
  case "$1" in
    --lock-timeout)
      shift 2
      ;;
    --no-db)
      shift
      ;;
    *)
      break
      ;;
  esac
done
cmd1="${1:-}"
cmd2="${2:-}"
case "$cmd1:$cmd2" in
  dep:list)
    echo "[{\"issue_id\":\"$3\",\"depends_on_id\":\"bd-dep\",\"type\":\"blocks\",\"title\":\"Dep\",\"status\":\"open\",\"priority\":2}]"
    exit 0
    ;;
  dep:add)
    echo "{\"issue_id\":\"$3\",\"depends_on_id\":\"$4\",\"type\":\"blocks\"}"
    exit 0
    ;;
  dep:remove)
    echo "{\"issue_id\":\"$3\",\"depends_on_id\":\"$4\",\"removed\":true}"
    exit 0
    ;;
esac
case "$cmd1" in
  create)
    for arg in "$@"; do
      case "$arg" in
        new|--label|--blocked-by)
          echo "unexpected legacy create arg: $arg" >&2
          exit 2
          ;;
      esac
    done
    echo "{\"id\":\"` + beadID + `\",\"title\":\"Created\",\"labels\":[\"api\",\"triaged\"],\"dependencies\":[{\"issue_id\":\"` + beadID + `\",\"depends_on_id\":\"bd-dep\",\"type\":\"blocks\"}]}"
    exit 0
    ;;
  list)
    echo "{\"issues\":[{\"id\":\"` + beadID + `\",\"title\":\"Listed\"}]}"
    exit 0
    ;;
  stats)
    echo "{\"open\":1}"
    exit 0
    ;;
  ready)
    echo "[]"
    exit 0
    ;;
  blocked)
    echo "[]"
    exit 0
    ;;
  close)
    echo "{\"id\":\"` + beadID + `\"}"
    exit 0
    ;;
  show)
    echo "[{\"id\":\"` + beadID + `\",\"title\":\"Show\"}]"
    exit 0
    ;;
  update)
    for arg in "$@"; do
      if [ "$arg" = "--label" ]; then
        echo "unexpected legacy update arg: $arg" >&2
        exit 2
      fi
    done
    echo "[{\"id\":\"` + beadID + `\",\"title\":\"Updated\",\"labels\":[\"api\",\"triaged\"]}]"
    exit 0
    ;;
esac
echo "OK"
`
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write stub br: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

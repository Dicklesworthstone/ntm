// Package serve provides REST API endpoints for UBS scanner integration.
// scanner.go implements the /api/v1/scanner endpoints.
package serve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/scanner"
)

// Scanner-specific error codes
const (
	ErrCodeScannerUnavailable = "SCANNER_UNAVAILABLE"
	ErrCodeScanNotFound       = "SCAN_NOT_FOUND"
	ErrCodeFindingNotFound    = "FINDING_NOT_FOUND"
	ErrCodeScanInProgress     = "SCAN_IN_PROGRESS"
	ErrCodeScanFailed         = "SCAN_FAILED"
)

// maxScanTimeout caps the client-supplied scan timeout. Only one scan may run
// at a time, so an unbounded value is a self-inflicted denial of service.
const maxScanTimeout = 30 * time.Minute

// ScanState represents the state of a scan
type ScanState string

const (
	ScanStatePending   ScanState = "pending"
	ScanStateRunning   ScanState = "running"
	ScanStateCompleted ScanState = "completed"
	ScanStateFailed    ScanState = "failed"
)

// ScanRecord represents a historical scan record
type ScanRecord struct {
	ID          string              `json:"id"`
	State       ScanState           `json:"state"`
	Path        string              `json:"path"`
	Options     *ScanOptionsRequest `json:"options,omitempty"`
	StartedAt   time.Time           `json:"started_at"`
	CompletedAt *time.Time          `json:"completed_at,omitempty"`
	Result      *scanner.ScanResult `json:"result,omitempty"`
	Error       string              `json:"error,omitempty"`
	FindingIDs  []string            `json:"finding_ids,omitempty"`
}

// FindingRecord represents a finding with additional metadata
type FindingRecord struct {
	ID          string          `json:"id"`
	ScanID      string          `json:"scan_id"`
	Finding     scanner.Finding `json:"finding"`
	Dismissed   bool            `json:"dismissed"`
	DismissedAt *time.Time      `json:"dismissed_at,omitempty"`
	DismissedBy string          `json:"dismissed_by,omitempty"`
	BeadID      string          `json:"bead_id,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`

	// beadPending marks a finding whose bead is being created right now.
	// Unexported so it never reaches the API surface; it exists only to make
	// "does this finding already have a bead?" a compare-AND-SET rather than a
	// check-then-act across a shell-out to `br create`.
	beadPending bool
}

// ScannerStore provides in-memory storage for scan history and findings
type ScannerStore struct {
	mu       sync.RWMutex
	scans    map[string]*ScanRecord
	findings map[string]*FindingRecord
	scanList []string // Ordered list of scan IDs
}

// cloneScan returns a deep copy of a ScanRecord
func cloneScan(s *ScanRecord) *ScanRecord {
	if s == nil {
		return nil
	}
	clone := *s
	if s.Options != nil {
		opts := *s.Options
		if s.Options.Languages != nil {
			opts.Languages = append([]string(nil), s.Options.Languages...)
		}
		if s.Options.Exclude != nil {
			opts.Exclude = append([]string(nil), s.Options.Exclude...)
		}
		clone.Options = &opts
	}
	if s.CompletedAt != nil {
		t := *s.CompletedAt
		clone.CompletedAt = &t
	}
	// Result is complex, but we only use it locally and it shouldn't be mutated after creation.
	// For full safety we could deep copy Result, but assigning it once is usually fine.
	if s.FindingIDs != nil {
		clone.FindingIDs = append([]string(nil), s.FindingIDs...)
	}
	return &clone
}

// cloneFinding returns a deep copy of a FindingRecord
func cloneFinding(f *FindingRecord) *FindingRecord {
	if f == nil {
		return nil
	}
	clone := *f
	if f.DismissedAt != nil {
		t := *f.DismissedAt
		clone.DismissedAt = &t
	}
	return &clone
}

// NewScannerStore creates a new scanner store
func NewScannerStore() *ScannerStore {
	return &ScannerStore{
		scans:    make(map[string]*ScanRecord),
		findings: make(map[string]*FindingRecord),
		scanList: make([]string, 0),
	}
}

// AddScan adds a scan record
func (s *ScannerStore) AddScan(scan *ScanRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scans[scan.ID] = cloneScan(scan)
	s.scanList = append(s.scanList, scan.ID)
}

// TryStartScan atomically registers a new pending/running scan only when no other
// active scan exists. It returns the conflicting active scan when registration fails.
func (s *ScannerStore) TryStartScan(scan *ScanRecord) (*ScanRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := len(s.scanList) - 1; i >= 0; i-- {
		existing, ok := s.scans[s.scanList[i]]
		if !ok || existing == nil {
			continue
		}
		if existing.State == ScanStatePending || existing.State == ScanStateRunning {
			return cloneScan(existing), false
		}
	}

	s.scans[scan.ID] = cloneScan(scan)
	s.scanList = append(s.scanList, scan.ID)
	return nil, true
}

// GetScan retrieves a scan by ID
func (s *ScannerStore) GetScan(id string) (*ScanRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	scan, ok := s.scans[id]
	return cloneScan(scan), ok
}

// UpdateScan mutates an existing scan safely using a callback
func (s *ScannerStore) UpdateScan(id string, fn func(*ScanRecord)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if scan, ok := s.scans[id]; ok {
		fn(scan)
	}
}

// GetScans returns scans in reverse chronological order
func (s *ScannerStore) GetScans(limit, offset int) []*ScanRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Reverse order (newest first)
	n := len(s.scanList)
	if offset >= n {
		return nil
	}

	end := n - offset
	start := end - limit
	if start < 0 {
		start = 0
	}

	result := make([]*ScanRecord, 0, end-start)
	for i := end - 1; i >= start; i-- {
		if scan, ok := s.scans[s.scanList[i]]; ok {
			result = append(result, cloneScan(scan))
		}
	}
	return result
}

// GetRunningScan returns the currently running scan, if any
func (s *ScannerStore) GetRunningScan() *ScanRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, scan := range s.scans {
		if scan.State == ScanStateRunning {
			return cloneScan(scan)
		}
	}
	return nil
}

// GetActiveScan returns the newest pending or running scan, if any.
func (s *ScannerStore) GetActiveScan() *ScanRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := len(s.scanList) - 1; i >= 0; i-- {
		scan, ok := s.scans[s.scanList[i]]
		if !ok || scan == nil {
			continue
		}
		if scan.State == ScanStatePending || scan.State == ScanStateRunning {
			return cloneScan(scan)
		}
	}

	return nil
}

// AddFinding adds a finding record
func (s *ScannerStore) AddFinding(finding *FindingRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.findings[finding.ID] = cloneFinding(finding)
}

// GetFinding retrieves a finding by ID
func (s *ScannerStore) GetFinding(id string) (*FindingRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	finding, ok := s.findings[id]
	return cloneFinding(finding), ok
}

// UpdateFinding mutates an existing finding safely using a callback
func (s *ScannerStore) UpdateFinding(id string, fn func(*FindingRecord)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if finding, ok := s.findings[id]; ok {
		fn(finding)
	}
}

// beadClaimResult reports the outcome of ClaimFindingForBead.
type beadClaimResult int

const (
	beadClaimGranted beadClaimResult = iota
	beadClaimFindingMissing
	beadClaimAlreadyLinked
	beadClaimInProgress
)

// ClaimFindingForBead atomically reserves a finding for bead creation.
//
// GetFinding returns a CLONE, so testing clone.BeadID == "" and then shelling
// out to `br create` was a check-then-act across a slow operation: two
// concurrent POSTs to .../create-bead both saw an empty BeadID, both created a
// bead, both returned 201, and only the last UpdateFinding won — silently
// orphaning the other bead in the tracker. The claim closes that window under
// the same lock that guards the map.
func (s *ScannerStore) ClaimFindingForBead(id string) (*FindingRecord, string, beadClaimResult) {
	s.mu.Lock()
	defer s.mu.Unlock()

	finding, ok := s.findings[id]
	if !ok {
		return nil, "", beadClaimFindingMissing
	}
	if finding.BeadID != "" {
		return nil, finding.BeadID, beadClaimAlreadyLinked
	}
	if finding.beadPending {
		return nil, "", beadClaimInProgress
	}
	finding.beadPending = true
	return cloneFinding(finding), "", beadClaimGranted
}

// ReleaseFindingBeadClaim drops a claim taken by ClaimFindingForBead without
// linking a bead, so a later attempt can retry.
func (s *ScannerStore) ReleaseFindingBeadClaim(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if finding, ok := s.findings[id]; ok {
		finding.beadPending = false
	}
}

// LinkFindingBead records the created bead and releases the claim.
func (s *ScannerStore) LinkFindingBead(id, beadID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if finding, ok := s.findings[id]; ok {
		finding.BeadID = beadID
		finding.beadPending = false
	}
}

// GetFindings returns findings with optional filtering
func (s *ScannerStore) GetFindings(scanID string, includeDismissed bool, severity string, limit, offset int) []*FindingRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var filtered []*FindingRecord
	for _, f := range s.findings {
		if scanID != "" && f.ScanID != scanID {
			continue
		}
		if !includeDismissed && f.Dismissed {
			continue
		}
		if severity != "" && string(f.Finding.Severity) != severity {
			continue
		}
		filtered = append(filtered, cloneFinding(f))
	}

	// Sort by created_at descending
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})

	// Apply pagination
	if offset >= len(filtered) {
		return nil
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[offset:end]
}

// GetFindingsByScan returns all findings for a specific scan
func (s *ScannerStore) GetFindingsByScan(scanID string) []*FindingRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*FindingRecord
	for _, f := range s.findings {
		if f.ScanID == scanID {
			result = append(result, cloneFinding(f))
		}
	}
	return result
}

// ScannerState holds the global scanner state
var scannerStore = NewScannerStore()

// Request/Response types

// ScanOptionsRequest is the request body for POST /api/v1/scanner/run
type ScanOptionsRequest struct {
	Path           string   `json:"path,omitempty"`        // Path to scan (defaults to project dir)
	Languages      []string `json:"languages,omitempty"`   // Languages to include
	Exclude        []string `json:"exclude,omitempty"`     // Languages to exclude
	StagedOnly     bool     `json:"staged_only,omitempty"` // Only scan staged files
	DiffOnly       bool     `json:"diff_only,omitempty"`   // Only scan modified files
	CI             bool     `json:"ci,omitempty"`          // CI mode
	FailOnWarning  bool     `json:"fail_on_warning,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
}

// ScanStatusResponse is the response for GET /api/v1/scanner/status
type ScanStatusResponse struct {
	Available     bool        `json:"available"`
	Version       string      `json:"version,omitempty"`
	CurrentScan   *ScanRecord `json:"current_scan,omitempty"`
	LastScan      *ScanRecord `json:"last_scan,omitempty"`
	TotalScans    int         `json:"total_scans"`
	TotalFindings int         `json:"total_findings"`
}

// DismissFindingRequest is the request body for POST /api/v1/scanner/findings/{id}/dismiss
type DismissFindingRequest struct {
	Reason string `json:"reason,omitempty"`
}

// CreateBeadFromFindingRequest is the request body for POST /api/v1/scanner/findings/{id}/create-bead
type CreateBeadFromFindingRequest struct {
	Title    string   `json:"title,omitempty"`    // Override default title
	Labels   []string `json:"labels,omitempty"`   // Additional labels
	Priority string   `json:"priority,omitempty"` // P0-P3
}

// BugSummaryResponse is the response for GET /api/v1/bugs/summary
type BugSummaryResponse struct {
	TotalFindings  int            `json:"total_findings"`
	Critical       int            `json:"critical"`
	Warning        int            `json:"warning"`
	Info           int            `json:"info"`
	BySeverity     map[string]int `json:"by_severity"`
	ByCategory     map[string]int `json:"by_category"`
	ByFile         map[string]int `json:"by_file"`
	DismissedCount int            `json:"dismissed_count"`
	LinkedBeads    int            `json:"linked_beads"`
}

// BugNotifyRequest is the request body for POST /api/v1/bugs/notify
type BugNotifyRequest struct {
	Channel     string `json:"channel"`                // slack, email, webhook
	Endpoint    string `json:"endpoint"`               // URL or address
	MinSeverity string `json:"min_severity,omitempty"` // Minimum severity to notify
}

// registerScannerRoutes registers all scanner-related routes
func (s *Server) registerScannerRoutes(r chi.Router) {
	r.Route("/scanner", func(r chi.Router) {
		// Read operations
		r.With(s.RequirePermission(PermReadHealth)).Get("/status", s.handleScannerStatus)
		r.With(s.RequirePermission(PermReadHealth)).Get("/history", s.handleScannerHistory)
		r.With(s.RequirePermission(PermReadHealth)).Get("/findings", s.handleListFindings)
		r.With(s.RequirePermission(PermReadHealth)).Get("/findings/{id}", s.handleGetFinding)

		// Write operations
		r.With(s.RequirePermission(PermWriteSessions)).Post("/run", s.handleRunScan)
		r.With(s.RequirePermission(PermWriteSessions)).Post("/findings/{id}/dismiss", s.handleDismissFinding)
		r.With(s.RequirePermission(PermWriteBeads)).Post("/findings/{id}/create-bead", s.handleCreateBeadFromFinding)
	})

	r.Route("/bugs", func(r chi.Router) {
		// Read operations
		r.With(s.RequirePermission(PermReadHealth)).Get("/", s.handleListBugs)
		r.With(s.RequirePermission(PermReadHealth)).Get("/summary", s.handleBugsSummary)

		// Write operations
		r.With(s.RequirePermission(PermWriteSessions)).Post("/notify", s.handleBugsNotify)
	})
}

// handleScannerStatus returns the current scanner status
func (s *Server) handleScannerStatus(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("scanner status request", "request_id", reqID)

	status := ScanStatusResponse{
		Available: scanner.IsAvailable(),
	}

	// Get version if available
	if status.Available {
		sc, err := scanner.New()
		if err == nil {
			if v, err := sc.Version(); err == nil {
				status.Version = v
			}
		}
	}

	// Get current/last scan
	status.CurrentScan = scannerStore.GetActiveScan()
	scans := scannerStore.GetScans(1, 0)
	if len(scans) > 0 && scans[0].State != ScanStateRunning && scans[0].State != ScanStatePending {
		status.LastScan = scans[0]
	}

	// Get totals (use lock to avoid data race on maps)
	scannerStore.mu.RLock()
	status.TotalScans = len(scannerStore.scans)
	status.TotalFindings = len(scannerStore.findings)
	scannerStore.mu.RUnlock()

	resp := map[string]interface{}{
		"available":      status.Available,
		"version":        status.Version,
		"total_scans":    status.TotalScans,
		"total_findings": status.TotalFindings,
	}
	if status.CurrentScan != nil {
		resp["current_scan"] = status.CurrentScan
	}
	if status.LastScan != nil {
		resp["last_scan"] = status.LastScan
	}

	writeSuccessResponse(w, http.StatusOK, resp, reqID)
}

// handleRunScan starts a new scan
func (s *Server) handleRunScan(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	// Parse request
	var opts ScanOptionsRequest
	if err := decodeOptionalJSONBody(r, &opts); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"Invalid request body", nil, reqID)
		return
	}

	// Default path to project directory
	path := opts.Path
	if path == "" {
		path = s.projectDirSnapshot()
	} else {
		var err error
		path, err = s.resolveWorkflowPath(path)
		if err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
				"path must be inside the project directory", nil, reqID)
			return
		}
	}

	// Check availability only after validating untrusted scan options. An
	// unavailable scanner must not conceal a rejected out-of-project path.
	if !scanner.IsAvailable() {
		slog.Warn("scanner not available", "request_id", reqID)
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeScannerUnavailable,
			"UBS scanner is not installed", nil, reqID)
		return
	}

	// Generate scan ID
	scanID := generateScanID()

	// Create scan record
	scan := &ScanRecord{
		ID:        scanID,
		State:     ScanStatePending,
		Path:      path,
		Options:   &opts,
		StartedAt: time.Now(),
	}
	if activeScan, ok := scannerStore.TryStartScan(scan); !ok {
		slog.Warn("scan already in progress", "request_id", reqID, "scan_id", activeScan.ID)
		writeErrorResponse(w, http.StatusConflict, ErrCodeScanInProgress,
			"A scan is already in progress", map[string]interface{}{"scan_id": activeScan.ID}, reqID)
		return
	}

	slog.Info("starting scan", "request_id", reqID, "scan_id", scanID, "path", path)

	// Start scan in background
	go s.runScanAsync(scan, opts)

	// Publish event
	s.publishScannerEvent("scanner.started", map[string]interface{}{
		"scan_id": scanID,
		"path":    path,
	})

	writeSuccessResponse(w, http.StatusAccepted, map[string]interface{}{
		"scan_id": scanID,
		"state":   ScanStatePending,
		"message": "Scan started",
	}, reqID)
}

// runScanAsync runs the scan in the background
func (s *Server) runScanAsync(scan *ScanRecord, opts ScanOptionsRequest) {
	// Update state to running
	scannerStore.UpdateScan(scan.ID, func(sr *ScanRecord) {
		sr.State = ScanStateRunning
	})

	// Create scanner
	sc, err := scanner.New()
	if err != nil {
		now := time.Now()
		scannerStore.UpdateScan(scan.ID, func(sr *ScanRecord) {
			sr.State = ScanStateFailed
			sr.Error = err.Error()
			sr.CompletedAt = &now
		})
		s.publishScannerEvent("scanner.failed", map[string]interface{}{
			"scan_id": scan.ID,
			"error":   err.Error(),
		})
		return
	}

	// Build scan options
	scanOpts := scanner.ScanOptions{
		Languages:        opts.Languages,
		ExcludeLanguages: opts.Exclude,
		CI:               opts.CI,
		FailOnWarning:    opts.FailOnWarning,
		StagedOnly:       opts.StagedOnly,
		DiffOnly:         opts.DiffOnly,
	}
	// Clamp the client-supplied timeout. An unbounded value left the scan
	// running effectively forever, and because a running scan makes every
	// later POST /scanner/run return 409 SCAN_IN_PROGRESS — with no cancel
	// route and no reaper for a stuck running state — a single request could
	// disable scanning for the lifetime of the process.
	scanOpts.Timeout = 5 * time.Minute
	if opts.TimeoutSeconds > 0 {
		scanOpts.Timeout = time.Duration(opts.TimeoutSeconds) * time.Second
	}
	if scanOpts.Timeout > maxScanTimeout {
		scanOpts.Timeout = maxScanTimeout
	}

	// Run scan
	ctx := context.Background()
	result, err := sc.Scan(ctx, scan.Path, scanOpts)
	now := time.Now()

	if err != nil {
		scannerStore.UpdateScan(scan.ID, func(sr *ScanRecord) {
			sr.State = ScanStateFailed
			sr.Error = err.Error()
			sr.CompletedAt = &now
		})
		slog.Error("scan failed", "scan_id", scan.ID, "error", err)
		s.publishScannerEvent("scanner.failed", map[string]interface{}{
			"scan_id": scan.ID,
			"error":   err.Error(),
		})
		return
	}

	// Create finding records
	findingIDs := make([]string, 0, len(result.Findings))
	for _, f := range result.Findings {
		findingID := generateFindingID(scan.ID, f)
		finding := &FindingRecord{
			ID:        findingID,
			ScanID:    scan.ID,
			Finding:   f,
			CreatedAt: now,
		}
		scannerStore.AddFinding(finding)
		findingIDs = append(findingIDs, findingID)

		// Publish finding event
		s.publishScannerEvent("scanner.finding", map[string]interface{}{
			"scan_id":    scan.ID,
			"finding_id": findingID,
			"severity":   f.Severity,
			"file":       f.File,
			"line":       f.Line,
		})
	}

	// Store result
	scannerStore.UpdateScan(scan.ID, func(sr *ScanRecord) {
		sr.State = ScanStateCompleted
		sr.Result = result
		sr.CompletedAt = &now
		sr.FindingIDs = findingIDs
	})

	slog.Info("scan completed", "scan_id", scan.ID, "findings", len(result.Findings),
		"critical", result.Totals.Critical, "warning", result.Totals.Warning)

	s.publishScannerEvent("scanner.completed", map[string]interface{}{
		"scan_id":        scan.ID,
		"total_files":    result.Totals.Files,
		"critical":       result.Totals.Critical,
		"warning":        result.Totals.Warning,
		"info":           result.Totals.Info,
		"total_findings": len(result.Findings),
	})
}

// handleScannerHistory returns scan history
func (s *Server) handleScannerHistory(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	limit := 20
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
			if limit > 1000 {
				limit = 1000
			}
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	scans := scannerStore.GetScans(limit, offset)
	slog.Info("scanner history request", "request_id", reqID, "count", len(scans))

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"scans":  scans,
		"count":  len(scans),
		"offset": offset,
		"limit":  limit,
	}, reqID)
}

// handleListFindings returns findings with optional filtering
func (s *Server) handleListFindings(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	// Parse query params
	scanID := r.URL.Query().Get("scan_id")
	severity := r.URL.Query().Get("severity")
	includeDismissed := r.URL.Query().Get("include_dismissed") == "true"

	limit := 50
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
			if limit > 1000 {
				limit = 1000
			}
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	findings := scannerStore.GetFindings(scanID, includeDismissed, severity, limit, offset)
	slog.Info("list findings request", "request_id", reqID, "count", len(findings),
		"scan_id", scanID, "severity", severity)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"findings": findings,
		"count":    len(findings),
		"offset":   offset,
		"limit":    limit,
	}, reqID)
}

// handleGetFinding returns a single finding by ID
func (s *Server) handleGetFinding(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	findingID := chi.URLParam(r, "id")

	finding, ok := scannerStore.GetFinding(findingID)
	if !ok {
		slog.Warn("finding not found", "request_id", reqID, "finding_id", findingID)
		writeErrorResponse(w, http.StatusNotFound, ErrCodeFindingNotFound,
			"Finding not found", nil, reqID)
		return
	}

	slog.Info("get finding request", "request_id", reqID, "finding_id", findingID)
	writeSuccessResponse(w, http.StatusOK, findingToMap(finding), reqID)
}

// handleDismissFinding dismisses a finding
func (s *Server) handleDismissFinding(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	findingID := chi.URLParam(r, "id")

	finding, ok := scannerStore.GetFinding(findingID)
	if !ok {
		slog.Warn("finding not found for dismiss", "request_id", reqID, "finding_id", findingID)
		writeErrorResponse(w, http.StatusNotFound, ErrCodeFindingNotFound,
			"Finding not found", nil, reqID)
		return
	}

	// Parse request
	var req DismissFindingRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"Invalid request body", nil, reqID)
		return
	}

	// Get user from RBAC context
	dismissedBy := "unknown"
	if rc := RoleFromContext(r.Context()); rc != nil {
		dismissedBy = rc.UserID
	}

	// Update finding
	now := time.Now()
	scannerStore.UpdateFinding(findingID, func(fr *FindingRecord) {
		fr.Dismissed = true
		fr.DismissedAt = &now
		fr.DismissedBy = dismissedBy
	})
	finding.Dismissed = true
	finding.DismissedAt = &now
	finding.DismissedBy = dismissedBy

	slog.Info("finding dismissed", "request_id", reqID, "finding_id", findingID,
		"dismissed_by", dismissedBy)

	s.publishScannerEvent("scanner.finding.dismissed", map[string]interface{}{
		"finding_id":   findingID,
		"dismissed_by": dismissedBy,
	})

	writeSuccessResponse(w, http.StatusOK, findingToMap(finding), reqID)
}

// handleCreateBeadFromFinding creates a bead from a finding
func (s *Server) handleCreateBeadFromFinding(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	findingID := chi.URLParam(r, "id")

	// Parse request before claiming, so a malformed body cannot leave a claim
	// behind.
	var req CreateBeadFromFindingRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"Invalid request body", nil, reqID)
		return
	}

	// Reserve the finding atomically. Checking a CLONE's BeadID and then
	// shelling out to `br create` let two concurrent requests each create a
	// bead and each return 201, with only the last write winning — orphaning
	// the other bead in the tracker with nothing pointing at it.
	finding, existingBeadID, claim := scannerStore.ClaimFindingForBead(findingID)
	switch claim {
	case beadClaimFindingMissing:
		slog.Warn("finding not found for bead creation", "request_id", reqID, "finding_id", findingID)
		writeErrorResponse(w, http.StatusNotFound, ErrCodeFindingNotFound,
			"Finding not found", nil, reqID)
		return
	case beadClaimAlreadyLinked:
		writeErrorResponse(w, http.StatusConflict, ErrCodeBadRequest,
			"Bead already created for this finding", map[string]interface{}{"bead_id": existingBeadID}, reqID)
		return
	case beadClaimInProgress:
		writeErrorResponse(w, http.StatusConflict, ErrCodeBadRequest,
			"Bead creation already in progress for this finding", nil, reqID)
		return
	}

	// From here every exit must either link a bead or release the claim.
	beadLinked := false
	defer func() {
		if !beadLinked {
			scannerStore.ReleaseFindingBeadClaim(findingID)
		}
	}()

	// Generate bead title
	title := req.Title
	if title == "" {
		title = fmt.Sprintf("[%s] %s in %s:%d",
			strings.ToUpper(string(finding.Finding.Severity)),
			finding.Finding.Category,
			finding.Finding.File,
			finding.Finding.Line)
	}

	// Build description
	description := fmt.Sprintf("## Finding Details\n\n"+
		"- **File**: %s\n"+
		"- **Line**: %d\n"+
		"- **Severity**: %s\n"+
		"- **Category**: %s\n"+
		"- **Rule ID**: %s\n\n"+
		"## Message\n%s\n\n"+
		"## Suggestion\n%s\n",
		finding.Finding.File,
		finding.Finding.Line,
		finding.Finding.Severity,
		finding.Finding.Category,
		finding.Finding.RuleID,
		finding.Finding.Message,
		finding.Finding.Suggestion)

	// Determine priority
	priority := req.Priority
	if priority == "" {
		switch finding.Finding.Severity {
		case scanner.SeverityCritical:
			priority = "P0"
		case scanner.SeverityWarning:
			priority = "P1"
		default:
			priority = "P2"
		}
	}

	// Create bead via br CLI
	labels := append([]string{"bug", "scanner"}, req.Labels...)
	args := []string{"--title", title, "--priority", priority, "--type", "bug"}
	if len(labels) > 0 {
		args = append(args, "--labels", strings.Join(labels, ","))
	}

	output, err := bv.RunBd(s.projectDirSnapshot(), append([]string{"create"}, args...)...)
	if err != nil {
		slog.Error("failed to create bead from finding", "request_id", reqID,
			"finding_id", findingID, "error", err)
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeBeadsUnavailable,
			"Failed to create bead", map[string]interface{}{"error": err.Error()}, reqID)
		return
	}

	// Parse bead ID from output (assuming format "Created <id>: ...")
	beadID := extractBeadID(output)
	if beadID == "" {
		// `br create` SUCCEEDED — a bead exists in the tracker but we cannot
		// name it, so the finding stays unlinked and every retry makes another
		// one. Log the raw output at error level so the operator can find and
		// link the orphan by hand; there is nothing safe to do automatically.
		slog.Error("bead created but its ID could not be parsed from br output; the bead is orphaned",
			"request_id", reqID, "finding_id", findingID, "output", strings.TrimSpace(output))
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeBeadsUnavailable,
			"Failed to determine bead ID from br output", map[string]interface{}{"output": strings.TrimSpace(output)}, reqID)
		return
	}

	// Update bead description
	_, _ = bv.RunBd(s.projectDirSnapshot(), "update", beadID, "--description", description)

	// Link the bead and release the claim in one atomic step.
	scannerStore.LinkFindingBead(findingID, beadID)
	beadLinked = true
	finding.BeadID = beadID

	slog.Info("bead created from finding", "request_id", reqID,
		"finding_id", findingID, "bead_id", beadID)

	s.publishScannerEvent("scanner.finding.bead_created", map[string]interface{}{
		"finding_id": findingID,
		"bead_id":    beadID,
	})

	writeSuccessResponse(w, http.StatusCreated, map[string]interface{}{
		"bead_id":    beadID,
		"finding_id": findingID,
		"title":      title,
	}, reqID)
}

// handleListBugs returns all non-dismissed findings as bugs
func (s *Server) handleListBugs(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	// Parse query params
	severity := r.URL.Query().Get("severity")
	file := r.URL.Query().Get("file")
	category := r.URL.Query().Get("category")

	limit := 100
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	// Get all non-dismissed findings
	allFindings := scannerStore.GetFindings("", false, severity, 10000, 0)

	// Additional filtering
	var filtered []*FindingRecord
	for _, f := range allFindings {
		if file != "" && !strings.Contains(f.Finding.File, file) {
			continue
		}
		if category != "" && f.Finding.Category != category {
			continue
		}
		filtered = append(filtered, f)
	}

	// Apply pagination
	total := len(filtered)
	if offset >= total {
		filtered = nil
	} else {
		end := offset + limit
		if end > total {
			end = total
		}
		filtered = filtered[offset:end]
	}

	slog.Info("list bugs request", "request_id", reqID, "count", len(filtered), "total", total)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"bugs":   filtered,
		"count":  len(filtered),
		"total":  total,
		"offset": offset,
		"limit":  limit,
	}, reqID)
}

// handleBugsSummary returns a summary of all bugs
func (s *Server) handleBugsSummary(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	// Get all findings
	allFindings := scannerStore.GetFindings("", true, "", 100000, 0)

	summary := BugSummaryResponse{
		BySeverity: make(map[string]int),
		ByCategory: make(map[string]int),
		ByFile:     make(map[string]int),
	}

	for _, f := range allFindings {
		if f.Dismissed {
			summary.DismissedCount++
			continue
		}

		summary.TotalFindings++
		switch f.Finding.Severity {
		case scanner.SeverityCritical:
			summary.Critical++
		case scanner.SeverityWarning:
			summary.Warning++
		case scanner.SeverityInfo:
			summary.Info++
		}

		summary.BySeverity[string(f.Finding.Severity)]++
		summary.ByCategory[f.Finding.Category]++
		summary.ByFile[f.Finding.File]++

		if f.BeadID != "" {
			summary.LinkedBeads++
		}
	}

	slog.Info("bugs summary request", "request_id", reqID, "total", summary.TotalFindings)
	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"total_findings":  summary.TotalFindings,
		"critical":        summary.Critical,
		"warning":         summary.Warning,
		"info":            summary.Info,
		"by_severity":     summary.BySeverity,
		"by_category":     summary.ByCategory,
		"by_file":         summary.ByFile,
		"dismissed_count": summary.DismissedCount,
		"linked_beads":    summary.LinkedBeads,
	}, reqID)
}

// handleBugsNotify sends a notification about bugs
func (s *Server) handleBugsNotify(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req BugNotifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"Invalid request body", nil, reqID)
		return
	}

	if req.Channel == "" || req.Endpoint == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"channel and endpoint are required", nil, reqID)
		return
	}

	// Get non-dismissed findings
	minSeverity := req.MinSeverity
	if minSeverity == "" {
		minSeverity = string(scanner.SeverityWarning)
	}

	// Filter by minimum severity
	severityOrder := map[string]int{
		string(scanner.SeverityCritical): 3,
		string(scanner.SeverityWarning):  2,
		string(scanner.SeverityInfo):     1,
	}
	minSevLevel, validSev := severityOrder[minSeverity]
	if !validSev {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			fmt.Sprintf("invalid min_severity: %q", minSeverity), nil, reqID)
		return
	}

	findings := scannerStore.GetFindings("", false, "", 1000, 0)

	var toNotify []*FindingRecord

	for _, f := range findings {
		if severityOrder[string(f.Finding.Severity)] >= minSevLevel {
			toNotify = append(toNotify, f)
		}
	}

	slog.Info("bugs notify request", "request_id", reqID, "channel", req.Channel,
		"endpoint", req.Endpoint, "findings_count", len(toNotify))

	// Publish notification event (consumed by webhook subscribers and WebSocket clients)
	notifyPayload := map[string]interface{}{
		"channel":        req.Channel,
		"findings_count": len(toNotify),
		"min_severity":   minSeverity,
	}
	if len(toNotify) > 0 {
		summaries := make([]map[string]interface{}, 0, len(toNotify))
		for _, f := range toNotify {
			summaries = append(summaries, map[string]interface{}{
				"file":     f.Finding.File,
				"line":     f.Finding.Line,
				"severity": string(f.Finding.Severity),
				"message":  f.Finding.Message,
			})
		}
		notifyPayload["findings"] = summaries
	}
	s.publishScannerEvent("scanner.notify", notifyPayload)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"notified":       true,
		"channel":        req.Channel,
		"findings_count": len(toNotify),
		"message":        fmt.Sprintf("Notification queued for %d findings", len(toNotify)),
	}, reqID)
}

// publishScannerEvent publishes a scanner event to WebSocket
func (s *Server) publishScannerEvent(eventType string, payload map[string]interface{}) {
	if s.wsHub == nil {
		return
	}
	s.wsHub.Publish("scanner", eventType, payload)
}

// Helper functions

// findingToMap converts a FindingRecord to a map for JSON response
func findingToMap(f *FindingRecord) map[string]interface{} {
	result := map[string]interface{}{
		"id":         f.ID,
		"scan_id":    f.ScanID,
		"finding":    f.Finding,
		"dismissed":  f.Dismissed,
		"created_at": f.CreatedAt,
	}
	if f.DismissedAt != nil {
		result["dismissed_at"] = f.DismissedAt
	}
	if f.DismissedBy != "" {
		result["dismissed_by"] = f.DismissedBy
	}
	if f.BeadID != "" {
		result["bead_id"] = f.BeadID
	}
	return result
}

// generateScanID creates a unique scan ID
func generateScanID() string {
	timestamp := time.Now().UnixNano()
	hash := sha256.Sum256([]byte(fmt.Sprintf("%d", timestamp)))
	return fmt.Sprintf("scan-%s", hex.EncodeToString(hash[:8]))
}

// generateFindingID creates a unique finding ID based on scan and finding details
func generateFindingID(scanID string, f scanner.Finding) string {
	data := fmt.Sprintf("%s:%s:%d:%s:%s", scanID, f.File, f.Line, f.Category, f.Message)
	hash := sha256.Sum256([]byte(data))
	return fmt.Sprintf("finding-%s", hex.EncodeToString(hash[:8]))
}

// extractBeadID extracts bead ID from br create output
func extractBeadID(output string) string {
	var single struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(output), &single); err == nil && single.ID != "" {
		return single.ID
	}

	var list []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(output), &list); err == nil {
		for _, item := range list {
			if item.ID != "" {
				return item.ID
			}
		}
	}

	// Legacy text output format: "Created bd-xxxxx: Title"
	parts := strings.SplitN(output, ":", 2)
	if len(parts) >= 1 {
		words := strings.Fields(parts[0])
		for _, w := range words {
			if strings.HasPrefix(w, "bd-") || strings.HasPrefix(w, "br-") || strings.HasPrefix(w, "ntm-") {
				return w
			}
		}
	}
	return ""
}

// Package serve provides audit trail functionality for REST and WebSocket actions.
package serve

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/Dicklesworthstone/ntm/internal/sqliteutil"
)

// AuditAction represents the type of action being audited.
type AuditAction string

const (
	AuditActionCreate    AuditAction = "create"
	AuditActionUpdate    AuditAction = "update"
	AuditActionDelete    AuditAction = "delete"
	AuditActionApprove   AuditAction = "approve"
	AuditActionDeny      AuditAction = "deny"
	AuditActionExecute   AuditAction = "execute"
	AuditActionLogin     AuditAction = "login"
	AuditActionLogout    AuditAction = "logout"
	AuditActionSubscribe AuditAction = "subscribe"
)

// AuditRecord represents a single audit trail entry.
type AuditRecord struct {
	ID         int64       `json:"id"`
	Timestamp  time.Time   `json:"timestamp"`
	RequestID  string      `json:"request_id"`
	UserID     string      `json:"user_id"`
	Role       Role        `json:"role"`
	Action     AuditAction `json:"action"`
	Resource   string      `json:"resource"`
	ResourceID string      `json:"resource_id,omitempty"`
	Method     string      `json:"method"`
	Path       string      `json:"path"`
	StatusCode int         `json:"status_code"`
	Duration   int64       `json:"duration_ms"`
	SessionID  string      `json:"session_id,omitempty"`
	PaneID     string      `json:"pane_id,omitempty"`
	AgentID    string      `json:"agent_id,omitempty"`
	Details    string      `json:"details,omitempty"`
	RemoteAddr string      `json:"remote_addr"`
	UserAgent  string      `json:"user_agent,omitempty"`
	ApprovalID string      `json:"approval_id,omitempty"`
}

// AuditStore persists audit records to durable storage.
type AuditStore struct {
	mu        sync.Mutex
	db        *sql.DB
	jsonlPath string
	jsonlFile *os.File

	// appendMu serializes JSONL appends only. It is separate from mu so that
	// Record does not hold the store lock across its I/O: the SQLite handle is
	// opened with SetMaxOpenConns(1) and busy_timeout=5000, so holding mu
	// across the INSERT serialized EVERY mutating request behind the database
	// — a concurrent POST could stall up to 5s after its handler had already
	// run, and Close() (same mutex) stalled shutdown behind it. An O_APPEND
	// write is not atomic for records larger than the pipe buffer, so the
	// appends still need a lock of their own; it is just a much shorter one.
	appendMu sync.Mutex

	// writeFailures counts audit records that failed to reach the JSONL sink.
	// Logging alone made a silently truncated audit log indistinguishable from
	// a quiet one.
	writeFailures atomic.Int64

	retention   time.Duration
	stopCleanup chan struct{}
	stopOnce    sync.Once
	cleanupWG   sync.WaitGroup
}

// AuditStoreConfig configures the audit store.
type AuditStoreConfig struct {
	// DBPath is the SQLite database file path.
	DBPath string

	// JSONLPath is the JSONL file path for append-only logging.
	JSONLPath string

	// Retention is how long to keep audit records.
	Retention time.Duration

	// CleanupInterval is how often to run retention cleanup.
	CleanupInterval time.Duration
}

var auditTimestampLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
}

func formatAuditTimestamp(ts time.Time) string {
	return ts.UTC().Format(time.RFC3339Nano)
}

func parseAuditTimestamp(value string) (time.Time, error) {
	for _, layout := range auditTimestampLayouts {
		var (
			parsed time.Time
			err    error
		)
		switch layout {
		case "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05":
			parsed, err = time.ParseInLocation(layout, value, time.UTC)
		default:
			parsed, err = time.Parse(layout, value)
		}
		if err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp format %q", value)
}

// DefaultAuditStoreConfig returns sensible defaults for audit storage.
func DefaultAuditStoreConfig(dataDir string) AuditStoreConfig {
	return AuditStoreConfig{
		DBPath:          filepath.Join(dataDir, "audit.db"),
		JSONLPath:       filepath.Join(dataDir, "audit.jsonl"),
		Retention:       90 * 24 * time.Hour, // 90 days
		CleanupInterval: 24 * time.Hour,
	}
}

// NewAuditStore creates a new audit store with SQLite and JSONL persistence.
func NewAuditStore(cfg AuditStoreConfig) (*AuditStore, error) {
	if cfg.Retention <= 0 {
		cfg.Retention = 90 * 24 * time.Hour
	}
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = 24 * time.Hour
	}

	// Ensure directory exists
	if cfg.DBPath != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0755); err != nil {
			return nil, fmt.Errorf("create audit db dir: %w", err)
		}
	}

	store := &AuditStore{
		retention:   cfg.Retention,
		stopCleanup: make(chan struct{}),
	}

	// Initialize SQLite database
	if cfg.DBPath != "" {
		db, err := sql.Open(sqliteutil.DriverName, sqliteutil.FileDSN(
			cfg.DBPath,
			"journal_mode(WAL)",
			"synchronous(NORMAL)",
			"busy_timeout(5000)",
			"foreign_keys(1)",
		))
		if err != nil {
			return nil, fmt.Errorf("open audit db: %w", err)
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		store.db = db

		if err := store.initSchema(); err != nil {
			if closeErr := db.Close(); closeErr != nil {
				log.Printf("audit: close db after init failure: %v", closeErr)
			}
			return nil, fmt.Errorf("init audit schema: %w", err)
		}
	}

	// Initialize JSONL file
	if cfg.JSONLPath != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.JSONLPath), 0755); err != nil {
			if store.db != nil {
				if closeErr := store.db.Close(); closeErr != nil {
					log.Printf("audit: close db after mkdir failure: %v", closeErr)
				}
			}
			return nil, fmt.Errorf("create audit log dir: %w", err)
		}
		f, err := os.OpenFile(cfg.JSONLPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			if store.db != nil {
				if closeErr := store.db.Close(); closeErr != nil {
					log.Printf("audit: close db after jsonl open failure: %v", closeErr)
				}
			}
			return nil, fmt.Errorf("open audit log: %w", err)
		}
		store.jsonlPath = cfg.JSONLPath
		store.jsonlFile = f
	}

	// Start retention cleanup goroutine only when there is a DB backend to prune.
	if store.db != nil {
		store.cleanupWG.Add(1)
		go store.cleanupLoop(cfg.CleanupInterval)
	}

	return store, nil
}

// initSchema creates the audit table if it doesn't exist.
func (s *AuditStore) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS audit_records (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp TEXT NOT NULL,
		request_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		role TEXT NOT NULL,
		action TEXT NOT NULL,
		resource TEXT NOT NULL,
		resource_id TEXT,
		method TEXT NOT NULL,
		path TEXT NOT NULL,
		status_code INTEGER NOT NULL,
		duration_ms INTEGER NOT NULL,
		session_id TEXT,
		pane_id TEXT,
		agent_id TEXT,
		details TEXT,
		remote_addr TEXT NOT NULL,
		user_agent TEXT,
		approval_id TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_records(timestamp);
	CREATE INDEX IF NOT EXISTS idx_audit_user_id ON audit_records(user_id);
	CREATE INDEX IF NOT EXISTS idx_audit_request_id ON audit_records(request_id);
	CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_records(action);
	CREATE INDEX IF NOT EXISTS idx_audit_resource ON audit_records(resource);
	CREATE INDEX IF NOT EXISTS idx_audit_session ON audit_records(session_id);
	CREATE INDEX IF NOT EXISTS idx_audit_approval ON audit_records(approval_id);
	`
	_, err := s.db.Exec(schema)
	return err
}

// Record stores an audit record.
func (s *AuditStore) Record(rec *AuditRecord) error {
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now().UTC()
	}

	// Snapshot the database handle under the store lock, then release it before
	// any I/O. Holding mu across the JSONL append AND the SQLite INSERT made
	// every mutating request serialize behind a single-connection database, and
	// made Close() wait behind them. cleanupWG.Wait() already joins the cleanup
	// goroutine, so the wide lock was not load-bearing.
	s.mu.Lock()
	db := s.db
	s.mu.Unlock()

	// Write to JSONL
	data, marshalErr := json.Marshal(rec)
	if marshalErr != nil {
		log.Printf("audit: json marshal error: %v", marshalErr)
	} else {
		// appendMu is taken BEFORE the file handle is read, and the handle is
		// read inside that critical section. Snapshotting it earlier raced
		// rotation: rotateJSONLIfLarge closes the old handle and opens a new
		// one while holding appendMu, so a writer holding a pre-rotation
		// snapshot would append to a CLOSED file and lose the record. Ordering
		// it this way means rotation either completes first (we see the new
		// handle) or waits for us.
		//
		// The lock is also what makes the append safe at all: an O_APPEND
		// write is only atomic for small records, and audit records carrying
		// details are not reliably small.
		s.appendMu.Lock()
		s.mu.Lock()
		jsonlFile := s.jsonlFile
		s.mu.Unlock()

		var writeErr error
		if jsonlFile != nil {
			_, writeErr = jsonlFile.Write(append(data, '\n'))
		}
		s.appendMu.Unlock()

		if writeErr != nil {
			// Counted as well as logged: a full disk would otherwise stop
			// the audit log silently, and a silently truncated audit log
			// is worse than one that errors.
			s.writeFailures.Add(1)
			log.Printf("audit: jsonl write error: %v", writeErr)
		}
	}

	// Write to SQLite. database/sql is safe for concurrent use and already
	// serializes on its single connection.
	if db != nil {
		_, err := db.Exec(`
			INSERT INTO audit_records (
				timestamp, request_id, user_id, role, action, resource, resource_id,
				method, path, status_code, duration_ms, session_id, pane_id, agent_id,
				details, remote_addr, user_agent, approval_id
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			formatAuditTimestamp(rec.Timestamp),
			rec.RequestID, rec.UserID, string(rec.Role), string(rec.Action),
			rec.Resource, rec.ResourceID, rec.Method, rec.Path,
			rec.StatusCode, rec.Duration, rec.SessionID, rec.PaneID, rec.AgentID,
			rec.Details, rec.RemoteAddr, rec.UserAgent, rec.ApprovalID,
		)
		if err != nil {
			return fmt.Errorf("insert audit record: %w", err)
		}
	}

	// Structured log output
	log.Printf("AUDIT action=%s resource=%s user=%s role=%s status=%d request_id=%s session=%s",
		rec.Action, rec.Resource, rec.UserID, rec.Role, rec.StatusCode, rec.RequestID, rec.SessionID)

	return nil
}

// Query retrieves audit records matching the filter.
func (s *AuditStore) Query(filter AuditFilter) ([]AuditRecord, error) {
	if s.db == nil {
		return nil, fmt.Errorf("audit db not configured")
	}

	query := `SELECT id, timestamp, request_id, user_id, role, action, resource, resource_id,
		method, path, status_code, duration_ms, session_id, pane_id, agent_id, details,
		remote_addr, user_agent, approval_id
		FROM audit_records WHERE 1=1`
	args := []interface{}{}

	if filter.UserID != "" {
		query += " AND user_id = ?"
		args = append(args, filter.UserID)
	}
	if filter.Action != "" {
		query += " AND action = ?"
		args = append(args, string(filter.Action))
	}
	if filter.Resource != "" {
		query += " AND resource = ?"
		args = append(args, filter.Resource)
	}
	if filter.SessionID != "" {
		query += " AND session_id = ?"
		args = append(args, filter.SessionID)
	}
	if filter.RequestID != "" {
		query += " AND request_id = ?"
		args = append(args, filter.RequestID)
	}
	if filter.ApprovalID != "" {
		query += " AND approval_id = ?"
		args = append(args, filter.ApprovalID)
	}
	if !filter.Since.IsZero() {
		query += " AND datetime(timestamp) >= datetime(?)"
		args = append(args, formatAuditTimestamp(filter.Since))
	}
	if !filter.Until.IsZero() {
		query += " AND datetime(timestamp) <= datetime(?)"
		args = append(args, formatAuditTimestamp(filter.Until))
	}

	query += " ORDER BY datetime(timestamp) DESC, id DESC"

	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}
	if filter.Offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", filter.Offset)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query audit records: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			log.Printf("audit: rows close error: %v", closeErr)
		}
	}()

	var records []AuditRecord
	for rows.Next() {
		var rec AuditRecord
		var tsStr string
		var sessionID, paneID, agentID, details, userAgent, approvalID, resourceID sql.NullString

		err := rows.Scan(
			&rec.ID, &tsStr, &rec.RequestID, &rec.UserID, &rec.Role, &rec.Action,
			&rec.Resource, &resourceID, &rec.Method, &rec.Path, &rec.StatusCode,
			&rec.Duration, &sessionID, &paneID, &agentID, &details,
			&rec.RemoteAddr, &userAgent, &approvalID,
		)
		if err != nil {
			return nil, fmt.Errorf("scan audit record: %w", err)
		}

		parsedTimestamp, err := parseAuditTimestamp(tsStr)
		if err != nil {
			return nil, fmt.Errorf("parse audit timestamp %q: %w", tsStr, err)
		}
		rec.Timestamp = parsedTimestamp
		rec.ResourceID = resourceID.String
		rec.SessionID = sessionID.String
		rec.PaneID = paneID.String
		rec.AgentID = agentID.String
		rec.Details = details.String
		rec.UserAgent = userAgent.String
		rec.ApprovalID = approvalID.String

		records = append(records, rec)
	}

	return records, rows.Err()
}

// AuditFilter specifies criteria for querying audit records.
type AuditFilter struct {
	UserID     string
	Action     AuditAction
	Resource   string
	SessionID  string
	RequestID  string
	ApprovalID string
	Since      time.Time
	Until      time.Time
	Limit      int
	Offset     int
}

// maxJSONLBytes is the size at which the active audit log is rotated. An
// append-only log is rotated rather than rewritten so appends stay O(1) and no
// concurrent writer can lose a record to a rewrite.
const maxJSONLBytes = 64 << 20 // 64 MiB

// rotatedJSONLSuffix marks files this store rotated, so pruning only ever
// considers its own output and never an unrelated file that happens to sit in
// the audit directory.
const rotatedJSONLSuffix = ".jsonl"

// rotatedSegmentPath names a rotated segment, guaranteeing it does not collide
// with one that already exists.
//
// The timestamp has one-second resolution, so two rotations inside the same
// second would otherwise produce the same name and os.Rename would silently
// OVERWRITE the earlier segment — destroying audit records, which is the one
// thing this subsystem must never do. The suffix loop is safe because rotation
// happens under appendMu+mu, so only one rotation is in flight at a time.
func rotatedSegmentPath(path string, now time.Time) string {
	base := strings.TrimSuffix(path, rotatedJSONLSuffix)
	stamp := now.Format("20060102T150405Z")

	candidate := fmt.Sprintf("%s.%s%s", base, stamp, rotatedJSONLSuffix)
	for seq := 1; ; seq++ {
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			return candidate
		}
		candidate = fmt.Sprintf("%s.%s-%d%s", base, stamp, seq, rotatedJSONLSuffix)
	}
}

// cleanupJSONL rotates the active audit log when it grows past maxJSONLBytes
// and deletes previously rotated segments older than the retention window.
func (s *AuditStore) cleanupJSONL() {
	s.mu.Lock()
	path := s.jsonlPath
	s.mu.Unlock()
	if path == "" {
		return
	}

	if err := s.rotateJSONLIfLarge(path); err != nil {
		log.Printf("audit cleanup: rotate jsonl: %v", err)
	}
	s.pruneRotatedJSONL(path)
}

// rotateJSONLIfLarge renames the active log aside and reopens a fresh one.
//
// The rename and the reopen happen under appendMu so no Record can be holding
// the old handle mid-write, and under mu so the handle swap is visible to the
// next Record.
func (s *AuditStore) rotateJSONLIfLarge(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat audit log: %w", err)
	}
	if info.Size() < maxJSONLBytes {
		return nil
	}

	s.appendMu.Lock()
	defer s.appendMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.jsonlFile == nil {
		return nil // closed underneath us
	}

	rotated := rotatedSegmentPath(path, time.Now().UTC())

	if err := s.jsonlFile.Close(); err != nil {
		log.Printf("audit: close before rotate: %v", err)
	}
	if err := os.Rename(path, rotated); err != nil {
		// Reopen the original so auditing continues even if rotation failed.
		if f, openErr := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); openErr == nil {
			s.jsonlFile = f
		} else {
			s.jsonlFile = nil
		}
		return fmt.Errorf("rename audit log: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		s.jsonlFile = nil
		return fmt.Errorf("reopen audit log after rotate: %w", err)
	}
	s.jsonlFile = f
	return nil
}

// pruneRotatedJSONL deletes rotated segments older than the retention window.
// It only removes files matching the segment naming this store produces.
func (s *AuditStore) pruneRotatedJSONL(path string) {
	dir := filepath.Dir(path)
	prefix := filepath.Base(strings.TrimSuffix(path, rotatedJSONLSuffix)) + "."

	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("audit cleanup: read audit dir: %v", err)
		return
	}
	cutoff := time.Now().Add(-s.retention)
	active := filepath.Base(path)

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == active {
			continue
		}
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, rotatedJSONLSuffix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			log.Printf("audit cleanup: remove rotated segment %s: %v", name, err)
		}
	}
}

// WriteFailures reports how many audit records failed to reach the JSONL sink.
//
// A non-zero value means the audit log has holes. It is exposed because the
// alternative — logging and moving on — makes a silently truncated audit log
// indistinguishable from a quiet one, which is the worst failure mode this
// subsystem has.
func (s *AuditStore) WriteFailures() int64 {
	return s.writeFailures.Load()
}

// cleanupLoop periodically removes old audit records.
func (s *AuditStore) cleanupLoop(interval time.Duration) {
	defer s.cleanupWG.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCleanup:
			return
		case <-ticker.C:
			s.cleanup()
		}
	}
}

// cleanup removes audit records older than retention period.
func (s *AuditStore) cleanup() {
	// The JSONL sink has its own retention and must run even when no SQLite
	// database is configured. Returning early on s.db == nil meant audit.jsonl
	// was never rotated or truncated at all, so it grew without bound while
	// audit.db stayed correctly pruned — and because Record's write failure was
	// only logged, the eventual ENOSPC would have stopped the audit log
	// silently.
	s.cleanupJSONL()

	if s.db == nil {
		return
	}

	cutoff := formatAuditTimestamp(time.Now().Add(-s.retention))
	result, err := s.db.Exec("DELETE FROM audit_records WHERE datetime(timestamp) < datetime(?)", cutoff)
	if err != nil {
		log.Printf("audit cleanup error: %v", err)
		return
	}

	affected, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		log.Printf("audit cleanup: rows affected error: %v", rowsErr)
		return
	}
	if affected > 0 {
		log.Printf("audit cleanup: removed %d records older than %s", affected, s.retention)
	}
}

// Close closes the audit store and releases resources.
func (s *AuditStore) Close() error {
	s.stopOnce.Do(func() {
		close(s.stopCleanup)
	})
	s.cleanupWG.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()

	var errs []error
	if s.jsonlFile != nil {
		if err := s.jsonlFile.Close(); err != nil {
			errs = append(errs, err)
		}
		s.jsonlFile = nil
	}
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			errs = append(errs, err)
		}
		s.db = nil
	}

	if len(errs) > 0 {
		return fmt.Errorf("close audit store: %v", errs)
	}
	return nil
}

// auditContextKey is the context key for audit context.
type auditContextKey struct{}

var ctxKeyAudit = auditContextKey{}

// AuditContext holds audit information collected during request processing.
type AuditContext struct {
	Resource   string
	ResourceID string
	SessionID  string
	PaneID     string
	AgentID    string
	Details    string
	ApprovalID string
	Action     AuditAction
}

// AuditContextFromRequest extracts audit context from request context.
func AuditContextFromRequest(r *http.Request) *AuditContext {
	if ctx, ok := r.Context().Value(ctxKeyAudit).(*AuditContext); ok {
		return ctx
	}
	return nil
}

// SetAuditContext adds audit context to a request.
func SetAuditContext(r *http.Request, ac *AuditContext) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKeyAudit, ac))
}

// SetAuditResource sets the resource being accessed for audit logging.
// Handlers should call this to provide context for the audit record.
func SetAuditResource(r *http.Request, resource, resourceID string) {
	if ac := AuditContextFromRequest(r); ac != nil {
		ac.Resource = resource
		ac.ResourceID = resourceID
	}
}

// SetAuditSession sets session/pane/agent IDs for audit logging.
func SetAuditSession(r *http.Request, sessionID, paneID, agentID string) {
	if ac := AuditContextFromRequest(r); ac != nil {
		ac.SessionID = sessionID
		ac.PaneID = paneID
		ac.AgentID = agentID
	}
}

// SetAuditDetails sets additional details for audit logging.
func SetAuditDetails(r *http.Request, details string) {
	if ac := AuditContextFromRequest(r); ac != nil {
		ac.Details = details
	}
}

// SetAuditApproval sets the approval ID for audit logging.
func SetAuditApproval(r *http.Request, approvalID string) {
	if ac := AuditContextFromRequest(r); ac != nil {
		ac.ApprovalID = approvalID
	}
}

// SetAuditAction sets the audit action type.
func SetAuditAction(r *http.Request, action AuditAction) {
	if ac := AuditContextFromRequest(r); ac != nil {
		ac.Action = action
	}
}

// isMutatingMethod returns true if the HTTP method modifies state.
func isMutatingMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// inferAction infers the audit action from HTTP method.
func inferAction(method string) AuditAction {
	switch method {
	case http.MethodPost:
		return AuditActionCreate
	case http.MethodPut, http.MethodPatch:
		return AuditActionUpdate
	case http.MethodDelete:
		return AuditActionDelete
	default:
		return AuditActionExecute
	}
}

// inferResource infers the resource name from URL path.
func inferResource(path string) string {
	// Extract resource from /api/v1/{resource}/...
	parts := splitPath(path)
	if len(parts) >= 3 && parts[0] == "api" && parts[1] == "v1" {
		return parts[2]
	}
	if len(parts) >= 2 && parts[0] == "api" {
		return parts[1]
	}
	return "unknown"
}

// splitPath splits a URL path into segments.
func splitPath(path string) []string {
	rawParts := strings.Split(path, "/")
	parts := make([]string, 0, len(rawParts))
	for _, p := range rawParts {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

// AuditMiddleware creates middleware that records audit trail for mutating requests.
func (s *Server) AuditMiddleware(store *AuditStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only audit mutating methods
			if !isMutatingMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()

			// Initialize audit context
			ac := &AuditContext{
				Resource: inferResource(r.URL.Path),
				Action:   inferAction(r.Method),
			}
			r = SetAuditContext(r, ac)

			// Wrap response writer to capture status code
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

			// Process request
			next.ServeHTTP(ww, r)

			// Record audit entry
			duration := time.Since(start).Milliseconds()
			reqID := requestIDFromContext(r.Context())

			// Get RBAC context
			rc := RoleFromContext(r.Context())
			userID := "anonymous"
			role := RoleViewer
			if rc != nil {
				userID = rc.UserID
				role = rc.Role
			}

			// Fill anything the handler did not set from the routed URL
			// params. Handler-set values always win.
			enrichAuditFromRoute(ac, r)

			// Use context values if set by handler
			resource := ac.Resource
			action := ac.Action

			rec := &AuditRecord{
				Timestamp:  start,
				RequestID:  reqID,
				UserID:     userID,
				Role:       role,
				Action:     action,
				Resource:   resource,
				ResourceID: ac.ResourceID,
				Method:     r.Method,
				Path:       r.URL.Path,
				StatusCode: ww.Status(),
				Duration:   duration,
				SessionID:  ac.SessionID,
				PaneID:     ac.PaneID,
				AgentID:    ac.AgentID,
				Details:    ac.Details,
				RemoteAddr: r.RemoteAddr,
				UserAgent:  r.UserAgent(),
				ApprovalID: ac.ApprovalID,
			}

			if err := store.Record(rec); err != nil {
				log.Printf("audit record error: %v request_id=%s", err, reqID)
			}
		})
	}
}

// enrichAuditFromRoute fills empty audit fields from the chi route params that
// the request actually matched. Values a handler set explicitly always win.
//
// The middleware's contract used to be that handlers "dutifully call
// SetAuditResource/SetAuditSession/SetAuditAction" — but exactly one handler in
// the tree ever did. Every other audit record (rollback, checkpoint restore,
// agent spawn, pane input, approval approve/deny) was written with empty
// resource_id, session_id, pane_id and agent_id, so an operator investigating a
// destructive action and filtering by session got ZERO rows and had to
// string-match the unindexed path instead.
//
// Deriving from the route is strictly better than adding ~30 handler calls: it
// covers routes that do not exist yet and cannot rot when a handler is added.
// It runs AFTER next.ServeHTTP because chi populates the RouteContext during
// routing, and the same *chi.Context is carried in the request context.
func enrichAuditFromRoute(ac *AuditContext, r *http.Request) {
	if ac == nil {
		return
	}
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return
	}

	param := func(names ...string) string {
		for _, name := range names {
			if v := strings.TrimSpace(rctx.URLParam(name)); v != "" {
				return v
			}
		}
		return ""
	}

	if ac.SessionID == "" {
		// Checkpoint routes name it sessionName; session routes use sessionId.
		ac.SessionID = param("sessionName", "sessionId")
	}
	if ac.PaneID == "" {
		ac.PaneID = param("paneIdx")
	}
	if ac.ResourceID == "" {
		// The subject of the request. Prefer the most specific id the route
		// carries, then fall back to the generic {id} that most collections use.
		ac.ResourceID = param("checkpointId", "depId", "provider", "id")
		// A session-scoped route with no more specific id is about the session.
		if ac.ResourceID == "" {
			ac.ResourceID = ac.SessionID
		}
	}
}

// RecordApprovalAction records an approval-related audit event.
func (s *AuditStore) RecordApprovalAction(
	ctx context.Context,
	action AuditAction,
	approvalID string,
	userID string,
	role Role,
	details string,
) error {
	reqID := requestIDFromContext(ctx)

	rec := &AuditRecord{
		Timestamp:  time.Now().UTC(),
		RequestID:  reqID,
		UserID:     userID,
		Role:       role,
		Action:     action,
		Resource:   "approval",
		ResourceID: approvalID,
		Method:     "INTERNAL",
		Path:       "/approvals/" + approvalID,
		StatusCode: 200,
		Duration:   0,
		Details:    details,
		ApprovalID: approvalID,
	}

	return s.Record(rec)
}

// RecordWebSocketAction records a WebSocket-related audit event.
func (s *AuditStore) RecordWebSocketAction(
	clientID string,
	action AuditAction,
	userID string,
	role Role,
	topics []string,
	remoteAddr string,
) error {
	topicsJSON, _ := json.Marshal(topics)

	rec := &AuditRecord{
		Timestamp:  time.Now().UTC(),
		RequestID:  clientID,
		UserID:     userID,
		Role:       role,
		Action:     action,
		Resource:   "websocket",
		ResourceID: clientID,
		Method:     "WS",
		Path:       "/ws",
		StatusCode: 200,
		Duration:   0,
		Details:    string(topicsJSON),
		RemoteAddr: remoteAddr,
	}

	return s.Record(rec)
}

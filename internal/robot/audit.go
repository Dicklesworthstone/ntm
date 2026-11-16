package robot

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	auditSchemaID          = "ntm:robot:audit:v1"
	auditSchemaVersion     = "1.0.0"
	defaultRobotAuditLimit = 50
	defaultRobotAuditDays  = 30
)

// AuditOptions configures --robot-audit inspection, search, verification, and source health queries.
type AuditOptions struct {
	Session    string // session name from --robot-audit=SESSION, or "all" / "" for global list/search
	Sessions   string // optional comma-separated session filter for search
	Action     string // "list", "show", "search", "verify", "sources" (auto-inferred when empty)
	Pattern    string // --audit-search regex pattern
	Verify     bool   // --audit-verify hash-chain verification
	Sources    bool   // --audit-sources projection source health
	EventTypes string // --audit-type comma-separated event types
	Actors     string // --audit-actor comma-separated actors
	Target     string // optional target glob pattern
	Since      string // --audit-since lower time bound
	Until      string // optional upper time bound
	Days       int    // --audit-days lookback window when searching without Since (default 30)
	Limit      int    // --audit-limit page size (default 50)
	Offset     int    // --audit-offset pagination offset (default 0)
	AuditDir   string // optional explicit audit directory override; defaults to audit.Dir()
}

// AuditLogFileSummary describes a tamper-evident JSONL audit log file on disk.
type AuditLogFileSummary struct {
	File       string `json:"file"`
	Session    string `json:"session,omitempty"`
	SizeBytes  int64  `json:"size_bytes"`
	ModifiedAt string `json:"modified_at"`
}

// AuditVerifyFileResult reports hash-chain verification status for one audit log file.
type AuditVerifyFileResult struct {
	File     string `json:"file"`
	Status   string `json:"status"`
	Verified bool   `json:"verified"`
	Error    string `json:"error,omitempty"`
}

// AuditOutput represents the structured JSON envelope for --robot-audit.
type AuditOutput struct {
	RobotResponse
	SchemaID      string                  `json:"schema_id"`
	SchemaVersion string                  `json:"schema_version"`
	Action        string                  `json:"action"`
	Session       string                  `json:"session"`
	AuditDir      string                  `json:"audit_dir,omitempty"`
	Logs          []AuditLogFileSummary   `json:"logs"`
	Entries       []audit.AuditEntry      `json:"entries"`
	Count         int                     `json:"count"`
	TotalMatches  int                     `json:"total_matches"`
	Scanned       int                     `json:"scanned"`
	ScanCapped    bool                    `json:"scan_capped,omitempty"`
	DurationMs    int64                   `json:"duration_ms,omitempty"`
	HasMore       bool                    `json:"has_more"`
	Verified      *bool                   `json:"verified,omitempty"`
	Files         []AuditVerifyFileResult `json:"files,omitempty"`
	Sources       []state.SourceHealth    `json:"sources,omitempty"`
	Pagination    *PaginationInfo         `json:"pagination,omitempty"`
	AgentHints    *AgentHints             `json:"_agent_hints,omitempty"`
}

// GetAudit queries, searches, lists, verifies, or inspects source health for tamper-evident audit logs.
func GetAudit(ctx context.Context, opts AuditOptions) (*AuditOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	rawSession := strings.TrimSpace(opts.Session)
	action := inferAuditAction(opts, rawSession)
	sessionLabel := normalizeAuditSessionLabel(rawSession)

	if err := ctx.Err(); err != nil {
		return newAuditErrorOutput(action, sessionLabel, err, ErrCodeTimeout, "Retry --robot-audit with an active context"), nil
	}

	switch action {
	case "list", "show", "search", "verify", "sources":
	default:
		return newAuditErrorOutput(
			action,
			sessionLabel,
			fmt.Errorf("unsupported --audit-action %q (want list, show, search, verify, or sources)", action),
			ErrCodeInvalidFlag,
			"Use --audit-action=list, show, search, verify, or sources",
		), nil
	}

	if opts.Limit < 0 {
		return newAuditErrorOutput(
			action,
			sessionLabel,
			fmt.Errorf("--audit-limit must be zero or greater (got %d)", opts.Limit),
			ErrCodeInvalidFlag,
			"Pass a non-negative --audit-limit such as --audit-limit=50",
		), nil
	}
	if opts.Offset < 0 {
		return newAuditErrorOutput(
			action,
			sessionLabel,
			fmt.Errorf("--audit-offset must be zero or greater (got %d)", opts.Offset),
			ErrCodeInvalidFlag,
			"Pass a non-negative --audit-offset such as --audit-offset=0",
		), nil
	}
	if opts.Days < 0 {
		return newAuditErrorOutput(
			action,
			sessionLabel,
			fmt.Errorf("--audit-days must be zero or greater (got %d)", opts.Days),
			ErrCodeInvalidFlag,
			"Pass a non-negative --audit-days such as --audit-days=30",
		), nil
	}

	limit := opts.Limit
	if limit == 0 {
		limit = defaultRobotAuditLimit
	}
	offset := opts.Offset

	if !isWildcardAuditSession(rawSession) && !isSafeAuditSessionSlug(rawSession) {
		return newAuditErrorOutput(
			action,
			sessionLabel,
			fmt.Errorf("invalid session name %q", rawSession),
			ErrCodeInvalidFlag,
			"Use a safe session name without path separators or glob metacharacters",
		), nil
	}

	auditDir := resolveRobotAuditDir(opts.AuditDir)

	switch action {
	case "list":
		filterSession := ""
		if !isWildcardAuditSession(rawSession) {
			filterSession = rawSession
		}
		allLogs, err := ListAuditLogFiles(auditDir, filterSession)
		if err != nil {
			return newAuditErrorOutput(action, sessionLabel, err, ErrCodeInternalError, "Check audit directory permissions"), nil
		}
		pagedLogs, pageInfo := ApplyPagination(allLogs, PaginationOptions{Limit: limit, Offset: offset})
		if pagedLogs == nil {
			pagedLogs = []AuditLogFileSummary{}
		}
		out := &AuditOutput{
			RobotResponse: NewRobotResponse(true),
			SchemaID:      auditSchemaID,
			SchemaVersion: auditSchemaVersion,
			Action:        action,
			Session:       sessionLabel,
			AuditDir:      auditDir,
			Logs:          pagedLogs,
			Entries:       []audit.AuditEntry{},
			Count:         len(pagedLogs),
			TotalMatches:  len(allLogs),
			HasMore:       pageInfo != nil && pageInfo.HasMore,
			Pagination:    pageInfo,
		}
		out.AgentHints = buildAuditAgentHints(out, pageInfo)
		return out, nil

	case "show":
		if isWildcardAuditSession(rawSession) {
			return newAuditErrorOutput(
				action,
				sessionLabel,
				errors.New("--robot-audit action 'show' requires a concrete session name"),
				ErrCodeInvalidFlag,
				"Pass --robot-audit=SESSION, or omit 'show' to list available audit logs",
			), nil
		}
		queryOpts := opts
		queryOpts.AuditDir = auditDir
		queryOpts.Action = "show"
		res, err := RunAuditQuery(ctx, queryOpts)
		if err != nil {
			return newAuditErrorOutput(action, sessionLabel, err, classifyAuditQueryError(err), "Check --audit-since and filter syntax"), nil
		}
		return buildRobotAuditEntriesOutput(action, sessionLabel, auditDir, res, limit, offset), nil

	case "search":
		queryOpts := opts
		queryOpts.AuditDir = auditDir
		queryOpts.Action = "search"
		res, err := RunAuditQuery(ctx, queryOpts)
		if err != nil {
			return newAuditErrorOutput(action, sessionLabel, err, classifyAuditQueryError(err), "Provide a valid regular expression for --audit-search and valid time filters"), nil
		}
		return buildRobotAuditEntriesOutput(action, sessionLabel, auditDir, res, limit, offset), nil

	case "verify":
		targetSession := ""
		if !isWildcardAuditSession(rawSession) {
			targetSession = rawSession
		}
		files, verified, verifyErr, lookupErr := VerifyAuditLogs(auditDir, targetSession)
		if lookupErr != nil {
			errCode := ErrCodeInternalError
			if strings.Contains(lookupErr.Error(), "no audit logs found") {
				errCode = ErrCodeSessionNotFound
			}
			return newAuditErrorOutput(action, sessionLabel, lookupErr, errCode, "Use --robot-audit --audit-action=list to inspect available audit logs"), nil
		}
		if files == nil {
			files = []AuditVerifyFileResult{}
		}
		out := &AuditOutput{
			RobotResponse: NewRobotResponse(verified),
			SchemaID:      auditSchemaID,
			SchemaVersion: auditSchemaVersion,
			Action:        action,
			Session:       sessionLabel,
			AuditDir:      auditDir,
			Logs:          []AuditLogFileSummary{},
			Entries:       []audit.AuditEntry{},
			Count:         len(files),
			TotalMatches:  len(files),
			Verified:      &verified,
			Files:         files,
		}
		if !verified && verifyErr != nil {
			out.RobotResponse = NewErrorResponse(
				fmt.Errorf("integrity verification failed for one or more files: %w", verifyErr),
				ErrCodeInternalError,
				"Inspect files[].error for broken hash chains, sequence gaps, or checksum mismatches",
			)
		}
		out.AgentHints = buildAuditAgentHints(out, nil)
		return out, nil

	case "sources":
		sources, err := queryAuditProjectionSources()
		if err != nil {
			return newAuditErrorOutput(action, sessionLabel, err, ErrCodeInternalError, "Inspect state.db permissions or run ntm --robot-snapshot"), nil
		}
		pagedSources, pageInfo := ApplyPagination(sources, PaginationOptions{Limit: limit, Offset: offset})
		if pagedSources == nil {
			pagedSources = []state.SourceHealth{}
		}
		out := &AuditOutput{
			RobotResponse: NewRobotResponse(true),
			SchemaID:      auditSchemaID,
			SchemaVersion: auditSchemaVersion,
			Action:        action,
			Session:       sessionLabel,
			Logs:          []AuditLogFileSummary{},
			Entries:       []audit.AuditEntry{},
			Count:         len(pagedSources),
			TotalMatches:  len(sources),
			HasMore:       pageInfo != nil && pageInfo.HasMore,
			Sources:       pagedSources,
			Pagination:    pageInfo,
		}
		out.AgentHints = buildAuditAgentHints(out, pageInfo)
		return out, nil
	}

	return newAuditErrorOutput(action, sessionLabel, fmt.Errorf("unhandled action %q", action), ErrCodeInternalError, ""), nil
}

// PrintAudit executes GetAudit and writes the terminal JSON/TOON response.
func PrintAudit(ctx context.Context, opts AuditOptions) error {
	out, err := GetAudit(ctx, opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot audit failed")
}

// RunAuditQuery executes the canonical audit log query/search shared by `ntm audit show`,
// `ntm audit search`, and `ntm --robot-audit`. Query.Limit stays unset so envelope-side
// pagination computes honest totals and has_more flags.
func RunAuditQuery(ctx context.Context, opts AuditOptions) (*audit.QueryResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	auditDir := resolveRobotAuditDir(opts.AuditDir)
	searcher, err := audit.NewSearcherWithDir(auditDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create searcher: %w", err)
	}

	action := inferAuditAction(opts, strings.TrimSpace(opts.Session))
	var q audit.Query

	if strings.TrimSpace(opts.Pattern) != "" {
		q.GrepPattern = opts.Pattern
	}

	if strings.TrimSpace(opts.Sessions) != "" {
		for _, s := range strings.Split(opts.Sessions, ",") {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				q.Sessions = append(q.Sessions, trimmed)
			}
		}
	} else if rawSession := strings.TrimSpace(opts.Session); !isWildcardAuditSession(rawSession) {
		q.Sessions = []string{rawSession}
	}

	if strings.TrimSpace(opts.Since) != "" {
		parsedSince, parseErr := ParseAuditTimeArg(opts.Since)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid --since: %w", parseErr)
		}
		q.Since = &parsedSince
	} else if action == "search" {
		days := opts.Days
		if days <= 0 {
			days = defaultRobotAuditDays
		}
		since := time.Now().AddDate(0, 0, -days)
		q.Since = &since
	}

	if strings.TrimSpace(opts.Until) != "" {
		parsedUntil, parseErr := ParseAuditTimeArg(opts.Until)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid --until: %w", parseErr)
		}
		q.Until = &parsedUntil
	}

	if strings.TrimSpace(opts.EventTypes) != "" {
		for _, et := range strings.Split(opts.EventTypes, ",") {
			if trimmed := strings.TrimSpace(et); trimmed != "" {
				q.EventTypes = append(q.EventTypes, audit.EventType(trimmed))
			}
		}
	}

	if strings.TrimSpace(opts.Actors) != "" {
		for _, actor := range strings.Split(opts.Actors, ",") {
			if trimmed := strings.TrimSpace(actor); trimmed != "" {
				q.Actors = append(q.Actors, audit.Actor(trimmed))
			}
		}
	}

	if strings.TrimSpace(opts.Target) != "" {
		q.TargetPattern = strings.TrimSpace(opts.Target)
	}

	res, err := searcher.SearchContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	if res.Entries == nil {
		res.Entries = []audit.AuditEntry{}
	}
	return res, nil
}

// ListAuditLogFiles lists JSONL audit log files in auditDir, optionally scoped to session.
func ListAuditLogFiles(auditDir, session string) ([]AuditLogFileSummary, error) {
	resolvedDir := resolveRobotAuditDir(auditDir)
	pattern := filepath.Join(resolvedDir, "*.jsonl")
	if strings.TrimSpace(session) != "" {
		pattern = filepath.Join(resolvedDir, strings.TrimSpace(session)+"-*.jsonl")
	}

	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to list audit logs: %w", err)
	}
	if len(matches) == 0 {
		return []AuditLogFileSummary{}, nil
	}

	logs := make([]AuditLogFileSummary, 0, len(matches))
	for _, match := range matches {
		info, statErr := os.Stat(match)
		if statErr != nil || info.IsDir() {
			continue
		}
		fname := filepath.Base(match)
		logs = append(logs, AuditLogFileSummary{
			File:       fname,
			Session:    inferAuditLogSession(match, fname),
			SizeBytes:  info.Size(),
			ModifiedAt: info.ModTime().Format(time.RFC3339),
		})
	}
	return logs, nil
}

// VerifyAuditLogs verifies hash-chain integrity across matching audit JSONL files.
// When session is non-empty and no logs match, lookupErr is non-nil.
// When one or more files fail verification, verified is false and verificationErr joins all file errors.
func VerifyAuditLogs(auditDir, session string) ([]AuditVerifyFileResult, bool, error, error) {
	resolvedDir := resolveRobotAuditDir(auditDir)
	trimmedSession := strings.TrimSpace(session)
	pattern := filepath.Join(resolvedDir, "*.jsonl")
	if trimmedSession != "" {
		pattern = filepath.Join(resolvedDir, trimmedSession+"-*.jsonl")
	}

	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, false, nil, fmt.Errorf("failed to find log files: %w", err)
	}
	if len(matches) == 0 {
		if trimmedSession != "" {
			return nil, false, nil, fmt.Errorf("no audit logs found for session %q", trimmedSession)
		}
		return []AuditVerifyFileResult{}, true, nil, nil
	}

	files := make([]AuditVerifyFileResult, 0, len(matches))
	verified := true
	var verificationErrors []error

	for _, logPath := range matches {
		fname := filepath.Base(logPath)
		if err := audit.VerifyIntegrity(logPath); err != nil {
			verified = false
			files = append(files, AuditVerifyFileResult{
				File:     fname,
				Status:   "FAIL",
				Verified: false,
				Error:    err.Error(),
			})
			verificationErrors = append(verificationErrors, fmt.Errorf("%s: %w", fname, err))
		} else {
			files = append(files, AuditVerifyFileResult{
				File:     fname,
				Status:   "PASS",
				Verified: true,
			})
		}
	}

	return files, verified, errors.Join(verificationErrors...), nil
}

// ParseAuditTimeArg parses an RFC3339/RFC3339Nano timestamp, YYYY-MM-DD date, or
// relative duration ("30m", "2h", "7d") into a time.Time.
func ParseAuditTimeArg(s string) (time.Time, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return time.Time{}, fmt.Errorf("invalid time %q: use RFC3339 or relative like '1h', '7d'", s)
	}
	if t, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return t, nil
	}
	if len(trimmed) == 10 {
		if t, err := time.Parse("2006-01-02", trimmed); err == nil {
			return t.UTC(), nil
		}
	}
	if len(trimmed) < 2 {
		return time.Time{}, fmt.Errorf("invalid time %q: use RFC3339 or relative like '1h', '7d'", trimmed)
	}

	unit := trimmed[len(trimmed)-1]
	numStr := trimmed[:len(trimmed)-1]
	n, err := strconv.Atoi(numStr)
	if err != nil || n < 0 {
		return time.Time{}, fmt.Errorf("invalid time %q: %w", trimmed, err)
	}

	now := time.Now()
	switch unit {
	case 'm':
		return now.Add(-time.Duration(n) * time.Minute), nil
	case 'h':
		return now.Add(-time.Duration(n) * time.Hour), nil
	case 'd':
		return now.AddDate(0, 0, -n), nil
	default:
		return time.Time{}, fmt.Errorf("invalid time unit %q: use m (minutes), h (hours), or d (days)", string(unit))
	}
}

func inferAuditAction(opts AuditOptions, rawSession string) string {
	if explicit := strings.ToLower(strings.TrimSpace(opts.Action)); explicit != "" {
		return explicit
	}
	switch {
	case opts.Verify:
		return "verify"
	case strings.TrimSpace(opts.Pattern) != "":
		return "search"
	case opts.Sources:
		return "sources"
	case rawSession != "" && !isWildcardAuditSession(rawSession):
		return "show"
	default:
		return "list"
	}
}

func isWildcardAuditSession(session string) bool {
	trimmed := strings.TrimSpace(session)
	return trimmed == "" || strings.EqualFold(trimmed, "all") || trimmed == "*"
}

func normalizeAuditSessionLabel(session string) string {
	trimmed := strings.TrimSpace(session)
	if isWildcardAuditSession(trimmed) {
		return "all"
	}
	return trimmed
}

func isSafeAuditSessionSlug(session string) bool {
	if session == "" || session == "." || session == ".." || len(session) > 128 {
		return false
	}
	if strings.Contains(session, "..") || strings.ContainsAny(session, `/\*?[]`) {
		return false
	}
	return true
}

func resolveRobotAuditDir(explicit string) string {
	if trimmed := strings.TrimSpace(explicit); trimmed != "" {
		return filepath.Clean(util.ExpandPath(trimmed))
	}
	return audit.Dir()
}

func inferAuditLogSession(fullPath, fname string) string {
	if f, err := os.Open(fullPath); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var header struct {
				SessionID string `json:"session_id"`
			}
			if json.Unmarshal([]byte(line), &header) == nil && header.SessionID != "" {
				return header.SessionID
			}
			break
		}
	}

	stem := strings.TrimSuffix(fname, ".jsonl")
	if len(stem) > 11 && stem[len(stem)-11] == '-' {
		datePart := stem[len(stem)-10:]
		if _, err := time.Parse("2006-01-02", datePart); err == nil {
			return stem[:len(stem)-11]
		}
	}
	return stem
}

func classifyAuditQueryError(err error) string {
	if err == nil {
		return ErrCodeInternalError
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrCodeTimeout
	}
	msg := err.Error()
	if strings.Contains(msg, "invalid --since") ||
		strings.Contains(msg, "invalid --until") ||
		strings.Contains(msg, "invalid grep pattern") ||
		strings.Contains(msg, "invalid target pattern") {
		return ErrCodeInvalidFlag
	}
	return ErrCodeInternalError
}

func buildRobotAuditEntriesOutput(action, sessionLabel, auditDir string, res *audit.QueryResult, limit, offset int) *AuditOutput {
	entries := res.Entries
	if entries == nil {
		entries = []audit.AuditEntry{}
	}
	paged, pageInfo := ApplyPagination(entries, PaginationOptions{Limit: limit, Offset: offset})
	if paged == nil {
		paged = []audit.AuditEntry{}
	}

	out := &AuditOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      auditSchemaID,
		SchemaVersion: auditSchemaVersion,
		Action:        action,
		Session:       sessionLabel,
		AuditDir:      auditDir,
		Logs:          []AuditLogFileSummary{},
		Entries:       paged,
		Count:         len(paged),
		TotalMatches:  len(entries),
		Scanned:       res.Scanned,
		ScanCapped:    res.Truncated,
		DurationMs:    res.Duration.Milliseconds(),
		HasMore:       pageInfo != nil && pageInfo.HasMore,
		Pagination:    pageInfo,
	}
	out.AgentHints = buildAuditAgentHints(out, pageInfo)
	return out
}

func queryAuditProjectionSources() ([]state.SourceHealth, error) {
	store := currentProjectionStore()
	closeStore := false
	if store == nil {
		opened, err := state.Open("")
		if err != nil {
			return nil, fmt.Errorf("open state store: %w", err)
		}
		if err := opened.Migrate(); err != nil {
			_ = opened.Close()
			return nil, fmt.Errorf("migrate state store: %w", err)
		}
		store = opened
		closeStore = true
	}
	if closeStore {
		defer func() { _ = store.Close() }()
	}

	sources, err := store.GetAllSourceHealth()
	if err != nil {
		return nil, err
	}
	if sources == nil {
		return []state.SourceHealth{}, nil
	}
	return sources, nil
}

func newAuditErrorOutput(action, session string, err error, code, hint string) *AuditOutput {
	if action == "" {
		action = "list"
	}
	if session == "" {
		session = "all"
	}
	return &AuditOutput{
		RobotResponse: NewErrorResponse(err, code, hint),
		SchemaID:      auditSchemaID,
		SchemaVersion: auditSchemaVersion,
		Action:        action,
		Session:       session,
		Logs:          []AuditLogFileSummary{},
		Entries:       []audit.AuditEntry{},
	}
}

func buildAuditAgentHints(out *AuditOutput, pageInfo *PaginationInfo) *AgentHints {
	hints := &AgentHints{}
	if pageHints := PaginationHints(pageInfo); pageHints != nil {
		hints.NextOffset = pageHints.NextOffset
		hints.PagesRemaining = pageHints.PagesRemaining
	}

	switch out.Action {
	case "list":
		if out.TotalMatches == 0 {
			hints.Summary = "No audit log files found"
		} else {
			hints.Summary = fmt.Sprintf("Listed %d of %d audit log file(s)", out.Count, out.TotalMatches)
		}
	case "show", "search":
		if out.TotalMatches == 0 {
			hints.Summary = fmt.Sprintf("No matching audit entries found (%d scanned)", out.Scanned)
		} else {
			hints.Summary = fmt.Sprintf("Returned %d of %d matching audit entries (%d scanned)", out.Count, out.TotalMatches, out.Scanned)
		}
		if out.ScanCapped {
			hints.Warnings = append(hints.Warnings, "Audit log scan hit internal 10,000-entry cap; narrow --audit-since or --audit-type for deeper history")
		}
	case "verify":
		if out.Verified != nil && *out.Verified {
			hints.Summary = fmt.Sprintf("Verified %d audit log file(s): all hash chains intact", len(out.Files))
		} else {
			hints.Summary = fmt.Sprintf("Audit verification failed across %d file(s)", len(out.Files))
		}
	case "sources":
		degraded := 0
		for _, src := range out.Sources {
			if !src.Available || !src.Healthy {
				degraded++
			}
		}
		hints.Summary = fmt.Sprintf("Inspected %d projection source(s) (%d degraded/unavailable)", out.TotalMatches, degraded)
		if degraded > 0 {
			hints.Warnings = append(hints.Warnings, fmt.Sprintf("%d projection data source(s) reported degraded or unavailable health", degraded))
		}
	}

	return hints
}

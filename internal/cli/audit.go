package cli

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/serve"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func newAuditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Query and verify audit logs",
		Long: `Query, search, and verify tamper-evident audit logs and durable state audit projections.

Audit logs record all significant NTM actions with hash-chain integrity, while
state.db tracks structured entity history, actor activity, request traces, and
projection source health, and audit.db records mutating HTTP/WebSocket daemon requests.

Examples:
  ntm audit show myproject                  # Show audit log for session
  ntm audit search "spawn"                  # Search all logs
  ntm audit search --type=error --days=7    # Errors in last week
  ntm audit verify myproject                # Verify log integrity
  ntm audit export myproject --format=json  # Export session log
  ntm audit http --action=create --limit=20 # HTTP/WebSocket daemon audit records
  ntm audit history approval appr-123       # Entity audit trail from state.db
  ntm audit actor user alice --since=24h    # Actor activity from state.db
  ntm audit trace req-42                    # Request trace from state.db
  ntm audit sources --degraded              # Degraded projection sources`,
	}

	cmd.AddCommand(
		newAuditShowCmd(),
		newAuditSearchCmd(),
		newAuditVerifyCmd(),
		newAuditExportCmd(),
		newAuditListCmd(),
		newAuditHTTPCmd(),
		newAuditHistoryCmd(),
		newAuditActorCmd(),
		newAuditTraceCmd(),
		newAuditSourcesCmd(),
	)

	return cmd
}

func newAuditShowCmd() *cobra.Command {
	var (
		since   string
		until   string
		evTypes string
		limit   int
		offset  int
	)

	cmd := &cobra.Command{
		Use:   "show <session>",
		Short: "Display audit log for a session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuditShow(args[0], since, until, evTypes, limit, offset)
		},
	}

	cmd.Flags().StringVar(&since, "since", "", "Show entries after this time (RFC3339 or duration like '1h', '7d')")
	cmd.Flags().StringVar(&until, "until", "", "Show entries before this time (RFC3339 or duration like '1h')")
	cmd.Flags().StringVar(&evTypes, "type", "", "Filter by event type (comma-separated: command,spawn,send,response,error,state_change)")
	cmd.Flags().IntVar(&limit, "limit", 100, "Maximum entries per page")
	cmd.Flags().IntVar(&offset, "offset", 0, "Pagination offset (use _agent_hints.next_offset for the next page)")

	return cmd
}

func newAuditSearchCmd() *cobra.Command {
	var (
		sessions string
		evTypes  string
		actors   string
		target   string
		days     int
		limit    int
		offset   int
	)

	cmd := &cobra.Command{
		Use:   "search <pattern>",
		Short: "Search across all audit logs",
		Long: `Full-text search across all audit logs using regex patterns.

Examples:
  ntm audit search "auth error"
  ntm audit search --type=spawn --days=7 "cc_"
  ntm audit search --actor=system "state_change"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuditSearch(args[0], sessions, evTypes, actors, target, days, limit, offset)
		},
	}

	cmd.Flags().StringVar(&sessions, "sessions", "", "Filter by session (comma-separated)")
	cmd.Flags().StringVar(&evTypes, "type", "", "Filter by event type (comma-separated)")
	cmd.Flags().StringVar(&actors, "actor", "", "Filter by actor (comma-separated: user,agent,system)")
	cmd.Flags().StringVar(&target, "target", "", "Filter by target (glob pattern, e.g. 'proj__cc_*')")
	cmd.Flags().IntVar(&days, "days", 30, "Search logs from last N days")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum results per page")
	cmd.Flags().IntVar(&offset, "offset", 0, "Pagination offset (use _agent_hints.next_offset for the next page)")

	return cmd
}

func newAuditVerifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify <session>",
		Short: "Verify audit log integrity",
		Long: `Verify the hash chain and sequence numbers of an audit log.

Checks for:
- Broken hash chains (indicating tampering)
- Sequence number gaps (indicating deletion)
- Checksum mismatches (indicating modification)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuditVerify(args[0])
		},
	}
	return cmd
}

func newAuditExportCmd() *cobra.Command {
	var (
		format string
		output string
	)

	cmd := &cobra.Command{
		Use:   "export <session>",
		Short: "Export audit log to file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuditExport(args[0], format, output)
		},
	}

	cmd.Flags().StringVar(&format, "format", "json", "Export format (json, csv)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file path (default: stdout)")

	return cmd
}

func newAuditListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available audit logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuditList()
		},
	}
	return cmd
}

// --- Run functions ---

// AuditQueryOutput is the paginated JSON envelope for `ntm audit show` and
// `ntm audit search` (D1, bd-ws3-contract-breadth-psvyu.1). Entries holds one
// page; TotalMatches counts all matches found in the scan window; HasMore
// plus _agent_hints.next_offset drive continuation.
type AuditQueryOutput struct {
	Success      bool                        `json:"success"`
	Entries      []audit.AuditEntry          `json:"entries"`
	Count        int                         `json:"count"`
	TotalMatches int                         `json:"total_matches"`
	HasMore      bool                        `json:"has_more"`
	Scanned      int                         `json:"scanned"`
	ScanCapped   bool                        `json:"scan_capped,omitempty"`
	DurationMs   int64                       `json:"duration_ms"`
	Pagination   *robot.PaginationInfo       `json:"pagination,omitempty"`
	AgentHints   *robot.PaginationAgentHints `json:"_agent_hints,omitempty"`
}

// buildAuditQueryOutput pages a full (scan-capped) audit query result. The
// searcher is asked for every match (Query.Limit unset) so totals and
// has_more are honest; paging happens here over the collected matches.
func buildAuditQueryOutput(result *audit.QueryResult, limit, offset int) *AuditQueryOutput {
	out := &AuditQueryOutput{
		Success:      true,
		Entries:      []audit.AuditEntry{},
		TotalMatches: len(result.Entries),
		Scanned:      result.Scanned,
		ScanCapped:   result.Truncated,
		DurationMs:   result.Duration.Milliseconds(),
	}
	page, info := robot.ApplyPagination(result.Entries, robot.PaginationOptions{Limit: limit, Offset: offset})
	out.Entries = page
	out.Count = len(page)
	if info != nil {
		out.Pagination = info
		out.HasMore = info.HasMore
		out.AgentHints = robot.PaginationHints(info)
	}
	return out
}

func runAuditShow(session, since, until, evTypes string, limit, offset int) error {
	searcher, err := newAuditSearcherFunc()
	if err != nil {
		return fmt.Errorf("failed to create searcher: %w", err)
	}

	result, err := robot.RunAuditQuery(context.Background(), robot.AuditOptions{
		Session:    session,
		Action:     "show",
		Since:      since,
		Until:      until,
		EventTypes: evTypes,
		AuditDir:   searcher.AuditDir(),
	})
	if err != nil {
		return err
	}

	out := buildAuditQueryOutput(result, limit, offset)
	if IsJSONOutput() {
		return json.NewEncoder(os.Stdout).Encode(out)
	}

	return renderAuditEntries(auditPageForRender(out))
}

// auditPageForRender adapts one envelope page back into the QueryResult shape
// the text renderer expects.
func auditPageForRender(out *AuditQueryOutput) *audit.QueryResult {
	return &audit.QueryResult{
		Entries:    out.Entries,
		TotalCount: out.TotalMatches,
		Scanned:    out.Scanned,
		Truncated:  out.HasMore || out.ScanCapped,
	}
}

func runAuditSearch(pattern, sessions, evTypes, actors, target string, days, limit, offset int) error {
	searcher, err := newAuditSearcherFunc()
	if err != nil {
		return fmt.Errorf("failed to create searcher: %w", err)
	}

	result, err := robot.RunAuditQuery(context.Background(), robot.AuditOptions{
		Action:     "search",
		Pattern:    pattern,
		Sessions:   sessions,
		EventTypes: evTypes,
		Actors:     actors,
		Target:     target,
		Days:       days,
		AuditDir:   searcher.AuditDir(),
	})
	if err != nil {
		return err
	}

	out := buildAuditQueryOutput(result, limit, offset)
	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(out)
	}

	if out.TotalMatches == 0 {
		fmt.Println("No matching entries found.")
		return nil
	}

	fmt.Printf("Found %d entries (scanned %d in %s)\n\n", out.TotalMatches, out.Scanned, result.Duration.Round(time.Millisecond))
	return renderAuditEntries(auditPageForRender(out))
}

func runAuditVerify(session string) error {
	return runAuditVerifyTo(os.Stdout, session)
}

type auditVerifyFileResult = robot.AuditVerifyFileResult

type auditVerifyResult struct {
	Success  bool                    `json:"success"`
	Session  string                  `json:"session"`
	Verified bool                    `json:"verified"`
	Files    []auditVerifyFileResult `json:"files"`
	Error    string                  `json:"error,omitempty"`
}

func runAuditVerifyTo(w io.Writer, session string) error {
	searcher, err := newAuditSearcherFunc()
	if err != nil {
		return fmt.Errorf("failed to create searcher: %w", err)
	}

	files, verified, verifyErr, lookupErr := robot.VerifyAuditLogs(searcher.AuditDir(), session)
	if lookupErr != nil {
		return lookupErr
	}

	t := theme.Current()
	for _, f := range files {
		if !jsonOutput {
			if f.Verified {
				fmt.Printf("%s✓%s %s: PASS\n", colorize(t.Success), "\033[0m", f.File)
			} else {
				fmt.Printf("%s✗%s %s: FAIL - %s\n", colorize(t.Error), "\033[0m", f.File, f.Error)
			}
		}
	}

	result := auditVerifyResult{
		Success:  verified,
		Session:  session,
		Verified: verified,
		Files:    files,
	}

	if !result.Success {
		result.Error = "integrity verification failed for one or more files"
		if jsonOutput {
			return emitJSONFailureEnvelopeToWithCause(w, result, verifyErr)
		}
		return fmt.Errorf("%s: %w", result.Error, verifyErr)
	}
	if jsonOutput {
		return json.NewEncoder(w).Encode(result)
	}
	return nil
}

func runAuditExport(session, format, outputPath string) error {
	format = strings.ToLower(strings.TrimSpace(format))
	if IsJSONOutput() {
		format = "json"
	}

	searcher, err := newAuditSearcherFunc()
	if err != nil {
		return fmt.Errorf("failed to create searcher: %w", err)
	}

	q := audit.Query{
		Sessions: []string{session},
	}

	result, err := searcher.Search(q)
	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}

	var w *os.File
	if outputPath != "" {
		w, err = os.Create(outputPath)
		if err != nil {
			return fmt.Errorf("failed to create output file: %w", err)
		}
		defer w.Close()
	} else {
		w = os.Stdout
	}

	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(result.Entries)
	case "csv":
		cw := csv.NewWriter(w)
		// Write header
		if err := cw.Write([]string{"timestamp", "session_id", "event_type", "actor", "target", "sequence_num"}); err != nil {
			return fmt.Errorf("failed to write CSV header: %w", err)
		}
		// Write data rows (csv.Writer handles escaping commas, quotes, newlines)
		for _, e := range result.Entries {
			if err := cw.Write([]string{
				e.Timestamp.Format(time.RFC3339),
				e.SessionID,
				string(e.EventType),
				string(e.Actor),
				e.Target,
				strconv.FormatUint(e.SequenceNum, 10),
			}); err != nil {
				return fmt.Errorf("failed to write CSV row: %w", err)
			}
		}
		cw.Flush()
		return cw.Error()
	default:
		return fmt.Errorf("unsupported format %q (use json or csv)", format)
	}
}

func runAuditList() error {
	searcher, err := newAuditSearcherFunc()
	if err != nil {
		return fmt.Errorf("failed to create searcher: %w", err)
	}

	auditDir := searcher.AuditDir()
	summaries, err := robot.ListAuditLogFiles(auditDir, "")
	if err != nil {
		return err
	}

	if len(summaries) == 0 {
		if jsonOutput {
			return json.NewEncoder(os.Stdout).Encode([]interface{}{})
		}
		fmt.Println("No audit logs found.")
		return nil
	}

	type logInfo struct {
		File    string `json:"file"`
		Size    int64  `json:"size"`
		ModTime string `json:"mod_time"`
	}

	logs := make([]logInfo, 0, len(summaries))
	for _, s := range summaries {
		logs = append(logs, logInfo{
			File:    s.File,
			Size:    s.SizeBytes,
			ModTime: s.ModifiedAt,
		})
	}

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(logs)
	}

	fmt.Printf("Audit logs in %s:\n\n", auditDir)
	for _, l := range logs {
		fmt.Printf("  %-50s  %8s  %s\n", l.File, formatBytes(l.Size), l.ModTime)
	}
	return nil
}

// AuditHTTPQueryOutput is the paginated JSON envelope for `ntm audit http`.
type AuditHTTPQueryOutput struct {
	Success      bool                        `json:"success"`
	Records      []serve.AuditRecord         `json:"records"`
	Count        int                         `json:"count"`
	TotalMatches int                         `json:"total_matches"`
	HasMore      bool                        `json:"has_more"`
	Pagination   *robot.PaginationInfo       `json:"pagination,omitempty"`
	AgentHints   *robot.PaginationAgentHints `json:"_agent_hints,omitempty"`
}

func newAuditHTTPCmd() *cobra.Command {
	var (
		userID     string
		action     string
		resource   string
		sessionID  string
		requestID  string
		approvalID string
		since      string
		until      string
		limit      int
		offset     int
	)

	cmd := &cobra.Command{
		Use:   "http",
		Short: "Query HTTP/WebSocket daemon audit records",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuditHTTP(
				cmd.Context(),
				userID,
				action,
				resource,
				sessionID,
				requestID,
				approvalID,
				since,
				until,
				limit,
				offset,
			)
		},
	}

	cmd.Flags().StringVar(&userID, "user", "", "Filter by user ID")
	cmd.Flags().StringVar(&action, "action", "", "Filter by audit action (create, update, delete, approve, deny, execute, login, logout, subscribe)")
	cmd.Flags().StringVar(&resource, "resource", "", "Filter by resource (e.g. sessions, panes, approvals)")
	cmd.Flags().StringVar(&sessionID, "session", "", "Filter by session ID")
	cmd.Flags().StringVar(&requestID, "request-id", "", "Filter by HTTP request ID")
	cmd.Flags().StringVar(&approvalID, "approval-id", "", "Filter by approval ID")
	cmd.Flags().StringVar(&since, "since", "", "Show records after this time (RFC3339 or relative like '1h', '7d')")
	cmd.Flags().StringVar(&until, "until", "", "Show records before this time (RFC3339 or relative like '1h')")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum records per page (max 1000)")
	cmd.Flags().IntVar(&offset, "offset", 0, "Pagination offset (use _agent_hints.next_offset for the next page)")

	return cmd
}

func runAuditHTTP(
	ctx context.Context,
	userID, action, resource, sessionID, requestID, approvalID, since, until string,
	limit, offset int,
) error {
	filter := serve.AuditFilter{
		UserID:     strings.TrimSpace(userID),
		Action:     serve.AuditAction(strings.TrimSpace(action)),
		Resource:   strings.TrimSpace(resource),
		SessionID:  strings.TrimSpace(sessionID),
		RequestID:  strings.TrimSpace(requestID),
		ApprovalID: strings.TrimSpace(approvalID),
		Limit:      limit,
		Offset:     offset,
	}

	if strings.TrimSpace(since) != "" {
		parsedSince, err := parseTimeArg(since)
		if err != nil {
			return outputError(fmt.Errorf("invalid --since: %w", err), IsJSONOutput())
		}
		filter.Since = parsedSince.UTC()
	}
	if strings.TrimSpace(until) != "" {
		parsedUntil, err := parseTimeArg(until)
		if err != nil {
			return outputError(fmt.Errorf("invalid --until: %w", err), IsJSONOutput())
		}
		filter.Until = parsedUntil.UTC()
	}

	auditDir, err := serve.DefaultAuditDir()
	if err != nil {
		return outputError(err, IsJSONOutput())
	}
	dbPath := filepath.Join(auditDir, "audit.db")

	records, totalCount, err := serve.QueryAuditDBFile(ctx, dbPath, filter)
	if err != nil {
		return outputError(err, IsJSONOutput())
	}
	if records == nil {
		records = []serve.AuditRecord{}
	}

	normLimit, normOffset := serve.NormalizeAuditQueryPage(limit, offset)
	hasMore := normOffset+len(records) < totalCount
	var nextCursor *int
	if hasMore {
		next := normOffset + len(records)
		nextCursor = &next
	}
	pageInfo := &robot.PaginationInfo{
		Limit:      normLimit,
		Offset:     normOffset,
		Count:      len(records),
		Total:      totalCount,
		HasMore:    hasMore,
		NextCursor: nextCursor,
	}
	out := AuditHTTPQueryOutput{
		Success:      true,
		Records:      records,
		Count:        len(records),
		TotalMatches: totalCount,
		HasMore:      hasMore,
		Pagination:   pageInfo,
		AgentHints:   robot.PaginationHints(pageInfo),
	}

	if IsJSONOutput() {
		return json.NewEncoder(os.Stdout).Encode(out)
	}

	if len(records) == 0 {
		fmt.Println("No HTTP audit records found.")
		return nil
	}

	fmt.Printf("HTTP Audit Records (%d of %d):\n\n", len(records), totalCount)
	fmt.Printf("  %-20s  %-9s %-12s %-14s %-6s %-14s %s\n",
		"TIMESTAMP", "ACTION", "RESOURCE", "USER", "STATUS", "SESSION", "REQUEST_ID")
	for _, rec := range records {
		ts := "-"
		if !rec.Timestamp.IsZero() {
			ts = rec.Timestamp.UTC().Format("2006-01-02 15:04:05")
		}
		sess := rec.SessionID
		if sess == "" {
			sess = "-"
		}
		req := rec.RequestID
		if req == "" {
			req = "-"
		}
		fmt.Printf("  %-20s  %-9s %-12s %-14s %-6d %-14s %s\n",
			ts, rec.Action, rec.Resource, rec.UserID, rec.StatusCode, sess, req)
	}
	if hasMore && nextCursor != nil {
		fmt.Printf("\n(showing %d of %d records, use --offset=%d to see more)\n", len(records), totalCount, *nextCursor)
	}
	return nil
}

// --- Helpers ---

func renderAuditEntries(result *audit.QueryResult) error {
	t := theme.Current()

	for _, e := range result.Entries {
		var typeColor string
		switch e.EventType {
		case audit.EventTypeError:
			typeColor = colorize(t.Error)
		case audit.EventTypeSpawn:
			typeColor = colorize(t.Success)
		case audit.EventTypeCommand:
			typeColor = colorize(t.Secondary)
		default:
			typeColor = colorize(t.Subtext)
		}

		ts := e.Timestamp.Format("15:04:05")
		fmt.Printf("%s %s%-12s%s %-8s %-30s seq=%d\n",
			ts, typeColor, e.EventType, "\033[0m",
			e.Actor, e.Target, e.SequenceNum)

		if len(e.Payload) > 0 {
			payloadJSON, _ := json.Marshal(e.Payload)
			if len(payloadJSON) > 120 {
				payloadJSON = append(payloadJSON[:117], "..."...)
			}
			fmt.Printf("  payload: %s\n", string(payloadJSON))
		}
	}

	if result.Truncated {
		fmt.Printf("\n(showing %d of %d entries, use --limit to see more)\n", len(result.Entries), result.TotalCount)
	}
	return nil
}

// parseTimeArg parses a time argument that can be RFC3339 or a relative duration.
// Relative durations: "1h" (1 hour ago), "7d" (7 days ago), "30m" (30 minutes ago).
func parseTimeArg(s string) (time.Time, error) {
	return robot.ParseAuditTimeArg(s)
}

// newAuditSearcherFunc is the factory for creating audit searchers.
// Tests override this to inject a mock searcher.
var newAuditSearcherFunc = func() (*audit.Searcher, error) {
	return audit.NewSearcher()
}

// openAuditStateStoreFunc opens and migrates the durable SQLite state store for
// state-backed audit subcommands (`history`, `actor`, `trace`, `sources`).
var openAuditStateStoreFunc = func() (*state.Store, error) {
	store, err := state.Open("")
	if err != nil {
		return nil, fmt.Errorf("open state store: %w", err)
	}
	if err := store.Migrate(); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("migrate state store: %w", err)
	}
	return store, nil
}

// AuditStateEventsOutput is the JSON envelope for `ntm audit history` and `ntm audit trace`.
type AuditStateEventsOutput struct {
	Success    bool               `json:"success"`
	EntityType string             `json:"entity_type,omitempty"`
	EntityID   string             `json:"entity_id,omitempty"`
	RequestID  string             `json:"request_id,omitempty"`
	Events     []state.AuditEvent `json:"events"`
	Count      int                `json:"count"`
}

// AuditActorOutput is the JSON envelope for `ntm audit actor`.
type AuditActorOutput struct {
	Success   bool               `json:"success"`
	ActorType string             `json:"actor_type"`
	ActorID   string             `json:"actor_id"`
	Actor     *state.AuditActor  `json:"actor,omitempty"`
	Events    []state.AuditEvent `json:"events"`
	Count     int                `json:"count"`
}

// AuditSourcesOutput is the JSON envelope for `ntm audit sources`.
type AuditSourcesOutput struct {
	Success      bool                 `json:"success"`
	SourceName   string               `json:"source_name,omitempty"`
	DegradedOnly bool                 `json:"degraded_only,omitempty"`
	Sources      []state.SourceHealth `json:"sources"`
	Count        int                  `json:"count"`
}

func newAuditHistoryCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "history <entity-type> <entity-id>",
		Short: "Show durable state audit history for an entity",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuditHistory(args[0], args[1], limit)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum audit events to return")
	return cmd
}

func newAuditActorCmd() *cobra.Command {
	var (
		since string
		limit int
	)
	cmd := &cobra.Command{
		Use:   "actor <actor-type> <actor-id>",
		Short: "Show metadata and durable audit activity for an actor",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuditActor(args[0], args[1], since, limit)
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "Show activity after this time (RFC3339 or relative like '1h', '7d')")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum activity events to return (0 = all)")
	return cmd
}

func newAuditTraceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "trace <request-id>",
		Short: "Show chronological audit events correlated to a request ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuditTrace(args[0])
		},
	}
}

func newAuditSourcesCmd() *cobra.Command {
	var degradedOnly bool
	cmd := &cobra.Command{
		Use:   "sources [source-name]",
		Short: "Inspect runtime projection data-source health",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sourceName := ""
			if len(args) == 1 {
				sourceName = args[0]
			}
			return runAuditSources(sourceName, degradedOnly)
		},
	}
	cmd.Flags().BoolVar(&degradedOnly, "degraded", false, "Show only unavailable or unhealthy sources")
	return cmd
}

func runAuditHistory(entityType, entityID string, limit int) error {
	if limit <= 0 {
		limit = 50
	}
	store, err := openAuditStateStoreFunc()
	if err != nil {
		return outputError(err, IsJSONOutput())
	}
	defer store.Close()

	events, err := store.GetAuditHistory(entityType, entityID, limit)
	if err != nil {
		return outputError(err, IsJSONOutput())
	}
	if events == nil {
		events = []state.AuditEvent{}
	}

	if IsJSONOutput() {
		return json.NewEncoder(os.Stdout).Encode(AuditStateEventsOutput{
			Success:    true,
			EntityType: entityType,
			EntityID:   entityID,
			Events:     events,
			Count:      len(events),
		})
	}

	if len(events) == 0 {
		fmt.Printf("No state audit events found for %s/%s.\n", entityType, entityID)
		return nil
	}
	fmt.Printf("Audit History for %s/%s (%d):\n\n", entityType, entityID, len(events))
	renderStateAuditEvents(events)
	return nil
}

func runAuditActor(actorType, actorID, since string, limit int) error {
	var sinceTime time.Time
	if strings.TrimSpace(since) != "" {
		parsed, err := parseTimeArg(since)
		if err != nil {
			return outputError(fmt.Errorf("invalid --since: %w", err), IsJSONOutput())
		}
		sinceTime = parsed.UTC()
	}

	store, err := openAuditStateStoreFunc()
	if err != nil {
		return outputError(err, IsJSONOutput())
	}
	defer store.Close()

	actor, err := store.GetAuditActor(actorType, actorID)
	if err != nil {
		return outputError(err, IsJSONOutput())
	}
	events, err := store.GetActorActivity(actorType, actorID, sinceTime)
	if err != nil {
		return outputError(err, IsJSONOutput())
	}
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	if events == nil {
		events = []state.AuditEvent{}
	}

	if IsJSONOutput() {
		return json.NewEncoder(os.Stdout).Encode(AuditActorOutput{
			Success:   true,
			ActorType: actorType,
			ActorID:   actorID,
			Actor:     actor,
			Events:    events,
			Count:     len(events),
		})
	}

	if actor != nil {
		name := actor.DisplayName
		if name == "" {
			name = actor.ActorID
		}
		fmt.Printf("Actor: %s (%s/%s)\n", name, actor.ActorType, actor.ActorID)
		fmt.Printf("  First Seen: %s  Last Seen: %s  Recorded Count: %d\n\n",
			actor.FirstSeenAt.Format(time.RFC3339),
			actor.LastSeenAt.Format(time.RFC3339),
			actor.EventCount,
		)
	}
	if len(events) == 0 {
		fmt.Printf("No audit activity found for %s/%s.\n", actorType, actorID)
		return nil
	}
	fmt.Printf("Activity (%d):\n\n", len(events))
	renderStateAuditEvents(events)
	return nil
}

func runAuditTrace(requestID string) error {
	store, err := openAuditStateStoreFunc()
	if err != nil {
		return outputError(err, IsJSONOutput())
	}
	defer store.Close()

	events, err := store.GetRequestTrace(requestID)
	if err != nil {
		return outputError(err, IsJSONOutput())
	}
	if events == nil {
		events = []state.AuditEvent{}
	}

	if IsJSONOutput() {
		return json.NewEncoder(os.Stdout).Encode(AuditStateEventsOutput{
			Success:   true,
			RequestID: requestID,
			Events:    events,
			Count:     len(events),
		})
	}

	if len(events) == 0 {
		fmt.Printf("No audit events found for request %q.\n", requestID)
		return nil
	}
	fmt.Printf("Request Trace %s (%d):\n\n", requestID, len(events))
	renderStateAuditEvents(events)
	return nil
}

func runAuditSources(sourceName string, degradedOnly bool) error {
	store, err := openAuditStateStoreFunc()
	if err != nil {
		return outputError(err, IsJSONOutput())
	}
	defer store.Close()

	var sources []state.SourceHealth
	if strings.TrimSpace(sourceName) != "" {
		src, err := store.GetSourceHealth(strings.TrimSpace(sourceName))
		if err != nil {
			return outputError(err, IsJSONOutput())
		}
		if src == nil {
			return outputError(fmt.Errorf("source %q not found in state store", sourceName), IsJSONOutput())
		}
		if !degradedOnly || !src.Available || !src.Healthy {
			sources = append(sources, *src)
		}
	} else if degradedOnly {
		sources, err = store.GetDegradedSources()
		if err != nil {
			return outputError(err, IsJSONOutput())
		}
	} else {
		sources, err = store.GetAllSourceHealth()
		if err != nil {
			return outputError(err, IsJSONOutput())
		}
	}
	if sources == nil {
		sources = []state.SourceHealth{}
	}

	if IsJSONOutput() {
		return json.NewEncoder(os.Stdout).Encode(AuditSourcesOutput{
			Success:      true,
			SourceName:   strings.TrimSpace(sourceName),
			DegradedOnly: degradedOnly,
			Sources:      sources,
			Count:        len(sources),
		})
	}

	if len(sources) == 0 {
		if degradedOnly {
			fmt.Println("No degraded projection sources.")
			return nil
		}
		fmt.Println("No projection sources recorded.")
		return nil
	}

	for _, src := range sources {
		stateLabel := "healthy"
		if !src.Available {
			stateLabel = "unavailable"
		} else if !src.Healthy {
			stateLabel = "degraded"
		}
		fmt.Printf("  %-14s %-12s failures=%d checked=%s",
			src.SourceName, stateLabel, src.ConsecutiveFailures, src.LastCheckAt.Format(time.RFC3339))
		if src.LastError != "" {
			fmt.Printf(" error=%q", src.LastError)
		} else if src.Reason != "" {
			fmt.Printf(" reason=%q", src.Reason)
		}
		fmt.Println()
	}
	return nil
}

func renderStateAuditEvents(events []state.AuditEvent) {
	for _, ev := range events {
		actor := ev.ActorType
		if ev.ActorID != "" {
			actor += ":" + ev.ActorID
		}
		fmt.Printf("  %s  %-22s %-18s %s/%s  %s\n",
			ev.Ts.Format(time.RFC3339),
			ev.EventType,
			actor,
			ev.EntityType,
			ev.EntityID,
			ev.ChangeSummary,
		)
		if ev.Reason != "" {
			fmt.Printf("    reason: %s\n", ev.Reason)
		}
	}
}

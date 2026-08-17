package cli

import (
	"encoding/csv"
	"encoding/json"
	"errors"
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
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func newAuditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Query and verify audit logs",
		Long: `Query, search, and verify tamper-evident audit logs.

Audit logs record all significant NTM actions with hash-chain integrity.

Examples:
  ntm audit show myproject                  # Show audit log for session
  ntm audit search "spawn"                  # Search all logs
  ntm audit search --type=error --days=7    # Errors in last week
  ntm audit verify myproject                # Verify log integrity
  ntm audit export myproject --format=json  # Export session log`,
	}

	cmd.AddCommand(
		newAuditShowCmd(),
		newAuditSearchCmd(),
		newAuditVerifyCmd(),
		newAuditExportCmd(),
		newAuditListCmd(),
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

	// Leave Query.Limit unset: the searcher collects every match (with its
	// internal scan cap) and pagination happens envelope-side so totals and
	// has_more are honest.
	q := audit.Query{
		Sessions: []string{session},
	}

	if since != "" {
		t, err := parseTimeArg(since)
		if err != nil {
			return fmt.Errorf("invalid --since: %w", err)
		}
		q.Since = &t
	}
	if until != "" {
		t, err := parseTimeArg(until)
		if err != nil {
			return fmt.Errorf("invalid --until: %w", err)
		}
		q.Until = &t
	}
	if evTypes != "" {
		for _, et := range strings.Split(evTypes, ",") {
			q.EventTypes = append(q.EventTypes, audit.EventType(strings.TrimSpace(et)))
		}
	}

	result, err := searcher.Search(q)
	if err != nil {
		return fmt.Errorf("search failed: %w", err)
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

	since := time.Now().AddDate(0, 0, -days)
	// Query.Limit stays unset; see runAuditShow — pagination is envelope-side.
	q := audit.Query{
		Since:       &since,
		GrepPattern: pattern,
	}

	if sessions != "" {
		q.Sessions = strings.Split(sessions, ",")
	}
	if evTypes != "" {
		for _, et := range strings.Split(evTypes, ",") {
			q.EventTypes = append(q.EventTypes, audit.EventType(strings.TrimSpace(et)))
		}
	}
	if actors != "" {
		for _, a := range strings.Split(actors, ",") {
			q.Actors = append(q.Actors, audit.Actor(strings.TrimSpace(a)))
		}
	}
	if target != "" {
		q.TargetPattern = target
	}

	result, err := searcher.Search(q)
	if err != nil {
		return fmt.Errorf("search failed: %w", err)
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

type auditVerifyFileResult struct {
	File     string `json:"file"`
	Status   string `json:"status"`
	Verified bool   `json:"verified"`
	Error    string `json:"error,omitempty"`
}

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

	auditDir := searcher.AuditDir()
	pattern := filepath.Join(auditDir, session+"-*.jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("failed to find log files: %w", err)
	}
	if len(matches) == 0 {
		return fmt.Errorf("no audit logs found for session %q", session)
	}

	t := theme.Current()
	result := auditVerifyResult{
		Success:  true,
		Session:  session,
		Verified: true,
		Files:    make([]auditVerifyFileResult, 0, len(matches)),
	}
	var verificationErrors []error

	for _, logPath := range matches {
		fname := filepath.Base(logPath)
		if err := audit.VerifyIntegrity(logPath); err != nil {
			result.Success = false
			result.Verified = false
			result.Files = append(result.Files, auditVerifyFileResult{
				File: fname, Status: "FAIL", Verified: false, Error: err.Error(),
			})
			verificationErrors = append(verificationErrors, fmt.Errorf("%s: %w", fname, err))
			if !jsonOutput {
				fmt.Printf("%s✗%s %s: FAIL - %v\n", colorize(t.Error), "\033[0m", fname, err)
			}
		} else {
			result.Files = append(result.Files, auditVerifyFileResult{
				File: fname, Status: "PASS", Verified: true,
			})
			if !jsonOutput {
				fmt.Printf("%s✓%s %s: PASS\n", colorize(t.Success), "\033[0m", fname)
			}
		}
	}

	if !result.Success {
		result.Error = "integrity verification failed for one or more files"
		cause := errors.Join(verificationErrors...)
		if jsonOutput {
			return emitJSONFailureEnvelopeToWithCause(w, result, cause)
		}
		return fmt.Errorf("%s: %w", result.Error, cause)
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
	matches, err := filepath.Glob(filepath.Join(auditDir, "*.jsonl"))
	if err != nil {
		return fmt.Errorf("failed to list audit logs: %w", err)
	}

	if len(matches) == 0 {
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

	var logs []logInfo
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		logs = append(logs, logInfo{
			File:    filepath.Base(m),
			Size:    info.Size(),
			ModTime: info.ModTime().Format(time.RFC3339),
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
	// Try RFC3339 first
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}

	// Try relative duration
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return time.Time{}, fmt.Errorf("invalid time %q: use RFC3339 or relative like '1h', '7d'", s)
	}

	unit := s[len(s)-1]
	numStr := s[:len(s)-1]
	var n int
	if _, err := fmt.Sscanf(numStr, "%d", &n); err != nil {
		return time.Time{}, fmt.Errorf("invalid time %q: %w", s, err)
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

// newAuditSearcherFunc is the factory for creating audit searchers.
// Tests override this to inject a mock searcher.
var newAuditSearcherFunc = func() (*audit.Searcher, error) {
	return audit.NewSearcher()
}

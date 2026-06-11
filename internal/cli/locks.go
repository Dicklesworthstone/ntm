package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/reservationsim"
	"github.com/Dicklesworthstone/ntm/internal/worktrees"
)

func newLocksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "locks",
		Short: "Manage file reservations",
		Long: `Manage file path reservations for multi-agent coordination.

File reservations are advisory locks that help prevent conflicts when multiple
agents work on the same codebase.

Subcommands:
  list          Show current file reservations
  force-release Forcibly release a stale reservation
  renew         Extend the TTL of active reservations

Examples:
  ntm locks list myproject               # Show session's reservations
  ntm locks list myproject --all-agents  # Show all project reservations
  ntm locks force-release myproject 42   # Force release reservation #42
  ntm locks renew myproject              # Extend all reservations by 30m`,
	}

	cmd.AddCommand(
		newLocksListCmd(),
		newLocksAdviseCmd(),
		newLocksForceReleaseCmd(),
		newLocksRenewCmd(),
		newLocksCheckCmd(),
	)

	return cmd
}

// LocksCheckResult is the JSON envelope returned by `ntm locks check`.
// Designed to be the wrapper-facing contract from ntm#127: a single
// machine-readable read tells the caller everything needed to decide
// (a) whether the path is free, (b) who holds it if not, (c) when it
// expires, (d) an opaque audit token the wrapper can preserve in its
// own log so it can prove later "we observed this state at this time"
// without having to re-query.
type LocksCheckResult struct {
	Success    bool   `json:"success"`
	Session    string `json:"session,omitempty"`
	Pane       int    `json:"pane,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
	ProjectKey string `json:"project_key"`
	Path       string `json:"path"`

	// State is one of:
	//   "free"     — no active reservation matches the path.
	//   "held"     — caller's own agent already holds a matching
	//                reservation; safe to proceed.
	//   "blocked"  — a different agent holds the path; caller must
	//                coordinate or wait.
	State string `json:"state"`

	// Holder is populated when State == "held" or "blocked". For
	// "free" results it stays nil so downstream `jq` filters can
	// rely on `.holder == null` to mean "go ahead".
	Holder *LocksCheckHolder `json:"holder,omitempty"`

	// AuditToken is a stable opaque string identifying this single
	// observation. Two checks of the same path moments apart return
	// different tokens. Wrappers preserve it to prove "this is the
	// state I saw" without re-querying.
	AuditToken string `json:"audit_token"`

	// ObservedAt records when ntm completed the query in RFC3339
	// UTC so wrappers building an audit ledger can chain
	// (audit_token, observed_at) pairs.
	ObservedAt string `json:"observed_at"`

	Error string `json:"error,omitempty"`
}

// LocksCheckHolder identifies the agent currently holding the path
// and the metadata downstream coordinators care about.
type LocksCheckHolder struct {
	Agent         string `json:"agent"`
	Reason        string `json:"reason,omitempty"`
	ExpiresAt     string `json:"expires_at"`
	Exclusive     bool   `json:"exclusive"`
	PathPattern   string `json:"path_pattern"`
	ReservationID int    `json:"reservation_id"`
}

func newLocksCheckCmd() *cobra.Command {
	var (
		sessionFlag string
		paneFlag    int
		taskID      string
	)

	cmd := &cobra.Command{
		Use:   "check <path>",
		Short: "Check whether a path is reservable (free / held / blocked)",
		Long: `Query the reservation state of a specific file path for the
caller's session. The output is a structured JSON envelope
(LocksCheckResult) designed to replace per-wrapper reservation
ledgers — see ntm#127.

Path semantics:
  The path argument is matched against active reservations exactly
  as the reservation API matched the original pattern. A path that
  falls inside a glob reservation is reported as held by the
  matching pattern.

Output states:
  free     — no active reservation matches the path.
  held     — caller's own agent already holds a matching reservation.
             Safe to proceed.
  blocked  — a different agent holds the path. Coordinate or wait.

Examples:
  ntm locks check /path/to/file --session myproject --pane 1 --json
  ntm locks check 'src/**' --session myproject --task-id work-42`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			session := sessionFlag
			if session == "" {
				// Fall back to the resolver so `--session` is optional
				// in single-session setups, same as `ntm mail send`.
				res, err := ResolveSessionWithOptions("", cmd.OutOrStdout(), SessionResolveOptions{
					TreatAsJSON: IsJSONOutput(),
				})
				if err != nil {
					return err
				}
				session = res.Session
			}
			return runLocksCheck(session, path, paneFlag, taskID)
		},
	}

	cmd.Flags().StringVar(&sessionFlag, "session", "", "NTM session name (default: resolve from cwd)")
	cmd.Flags().IntVar(&paneFlag, "pane", 0, "Tmux pane index the caller is asking on behalf of (echoed back in the JSON envelope; not used in matching)")
	cmd.Flags().StringVar(&taskID, "task-id", "", "Optional task id the caller is tracking; echoed back in the JSON envelope for audit log correlation")

	return cmd
}

func runLocksCheck(session, path string, pane int, taskID string) error {
	session, projectKey, err := resolveAgentMailScope(session)
	observedAt := time.Now().UTC().Format(time.RFC3339Nano)
	result := LocksCheckResult{
		Session:    session,
		Pane:       pane,
		TaskID:     taskID,
		ProjectKey: projectKey,
		Path:       path,
		ObservedAt: observedAt,
		AuditToken: newLocksCheckAuditToken(projectKey, path, observedAt),
	}
	emitResult := func(r LocksCheckResult) error {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(r); encErr != nil {
			return encErr
		}
		if !r.Success {
			return jsonFailureExit()
		}
		return nil
	}
	if err != nil {
		result.Error = err.Error()
		if IsJSONOutput() {
			return emitResult(result)
		}
		return err
	}

	sessionAgent, err := loadResolvedSessionAgent(session, projectKey)
	if err != nil {
		result.Error = fmt.Sprintf("loading session agent: %v", err)
		if IsJSONOutput() {
			return emitResult(result)
		}
		return fmt.Errorf("loading session agent: %w", err)
	}
	agentName := ""
	if sessionAgent != nil {
		agentName = sessionAgent.AgentName
	}

	client := newAgentMailClient(projectKey)
	if !client.IsAvailable() {
		result.Error = "Agent Mail server unavailable"
		if IsJSONOutput() {
			return emitResult(result)
		}
		return fmt.Errorf("agent mail server unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Pull every active reservation in the project, not just the
	// caller's, so we can distinguish "held by me" from "blocked by
	// another agent" with a single round-trip.
	allReservations, err := client.ListReservations(ctx, projectKey, "", true)
	if err != nil {
		result.Error = fmt.Sprintf("listing reservations: %v", err)
		if IsJSONOutput() {
			return emitResult(result)
		}
		return fmt.Errorf("listing reservations: %w", err)
	}

	now := time.Now()
	result.Success = true
	holder := selectLocksCheckHolder(allReservations, agentName, path, projectKey, now)
	if holder == nil {
		result.State = "free"
	} else {
		if agentName != "" && holder.AgentName == agentName {
			result.State = "held"
		} else {
			result.State = "blocked"
		}
		result.Holder = &LocksCheckHolder{
			Agent:         holder.AgentName,
			Reason:        holder.Reason,
			ExpiresAt:     holder.ExpiresTS.Time.UTC().Format(time.RFC3339Nano),
			Exclusive:     holder.Exclusive,
			PathPattern:   holder.PathPattern,
			ReservationID: holder.ID,
		}
	}

	if IsJSONOutput() {
		return emitResult(result)
	}

	// Human-readable summary for terminal users.
	switch result.State {
	case "free":
		fmt.Printf("free: %s is not held by any agent\n", path)
	case "held":
		// Defensive nil-check: under the current code path the loop
		// above always sets Holder when state is "held"/"blocked", but
		// a future refactor that sets State without Holder would crash
		// terminal users here with a nil-deref panic. Cheap guard.
		if result.Holder == nil {
			fmt.Printf("held: %s (no holder details available)\n", path)
		} else {
			fmt.Printf("held: %s is reserved by %s (you), pattern=%q, expires=%s\n",
				path, result.Holder.Agent, result.Holder.PathPattern, result.Holder.ExpiresAt)
		}
	case "blocked":
		if result.Holder == nil {
			fmt.Printf("blocked: %s (no holder details available)\n", path)
		} else {
			fmt.Printf("blocked: %s is reserved by %s, pattern=%q, expires=%s\n",
				path, result.Holder.Agent, result.Holder.PathPattern, result.Holder.ExpiresAt)
		}
	}
	fmt.Printf("audit_token: %s\n", result.AuditToken)
	return nil
}

// selectLocksCheckHolder returns the exclusive reservation that decides
// `ntm locks check` state for a path. Shared reservations are observe-only:
// they are useful in list/advice output, but they neither authorize writes
// for the caller nor block other agents.
func selectLocksCheckHolder(reservations []agentmail.FileReservation, agentName, path, projectKey string, now time.Time) *agentmail.FileReservation {
	// Prefer the caller's own exclusive reservation over any other-agent's.
	// Without this priority, the first matching reservation could report
	// `blocked` even when the caller also has a valid exclusive match.
	matchPath := locksComparableReservationPath(path, projectKey)
	var ownHolder *agentmail.FileReservation
	var otherHolder *agentmail.FileReservation
	for i := range reservations {
		r := &reservations[i]
		if !r.Exclusive {
			continue
		}
		if !locksReservationActiveAt(*r, now) {
			continue
		}
		matchPattern := locksComparableReservationPath(r.PathPattern, projectKey)
		if !locksCheckPathMatches(matchPath, matchPattern) {
			continue
		}
		if agentName != "" && r.AgentName == agentName {
			ownHolder = r
			break
		}
		if otherHolder == nil {
			otherHolder = r
		}
	}
	if ownHolder != nil {
		return ownHolder
	}
	return otherHolder
}

func locksReservationActiveAt(reservation agentmail.FileReservation, now time.Time) bool {
	if reservation.ReleasedTS != nil {
		return false
	}
	if reservation.ExpiresTS.IsZero() {
		return false
	}
	return !now.After(reservation.ExpiresTS.Time)
}

// locksCheckPathMatches reports whether `path` falls under
// `pattern`. Two cases:
//   - Pattern with no glob meta — exact match OR `path` starts with
//     `pattern + "/"` (caller asking about a file inside a reserved
//     directory).
//   - Pattern with glob meta — match by slash-separated path
//     segments. A single `*` stays within one segment, while `**`
//     spans zero or more segments, mirroring the reservation patterns
//     wrappers already use for Agent Mail file reservations.
func locksCheckPathMatches(path, pattern string) bool {
	path = locksNormalizeReservationPath(path)
	pattern = locksNormalizeReservationPath(pattern)

	// Empty pattern: the loop below would compute `pattern+"/"` = "/"
	// and HasPrefix(absolute_path, "/") would falsely match every
	// absolute path. An empty pattern is meaningless (no actual
	// reservation would carry one) but we guard explicitly so a
	// future data anomaly can't silently flip every check to
	// `blocked`.
	if pattern == "" {
		return false
	}
	if path == pattern {
		return true
	}
	if pattern == "." {
		return path != "" && !pathpkg.IsAbs(path) && path != ".." && !strings.HasPrefix(path, "../")
	}
	if pattern == "**" {
		return path != ""
	}
	if strings.ContainsAny(pattern, "*?[") {
		return locksMatchPatternSegments(
			locksSplitPathSegments(pattern),
			locksSplitPathSegments(path),
		)
	}
	if strings.HasSuffix(pattern, "/") && strings.HasPrefix(path, pattern) {
		return true
	}
	if strings.HasPrefix(path, pattern+"/") {
		return true
	}
	return false
}

func locksNormalizeReservationPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = filepath.ToSlash(value)
	return pathpkg.Clean(value)
}

// locksComparableReservationPath maps absolute paths inside projectKey
// back to the project-relative form Agent Mail reservations usually store.
func locksComparableReservationPath(value, projectKey string) string {
	value = locksNormalizeReservationPath(value)
	projectKey = locksNormalizeReservationPath(projectKey)
	if value == "" || projectKey == "" {
		return value
	}
	if value == projectKey {
		return "."
	}
	if strings.HasPrefix(value, projectKey+"/") {
		return strings.TrimPrefix(value, projectKey+"/")
	}
	return value
}

func locksSplitPathSegments(value string) []string {
	parts := strings.Split(value, "/")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		segments = append(segments, part)
	}
	return segments
}

func locksMatchPatternSegments(patternSegs, pathSegs []string) bool {
	if len(patternSegs) == 0 {
		return len(pathSegs) == 0
	}

	if patternSegs[0] == "**" {
		for i := 0; i <= len(pathSegs); i++ {
			if locksMatchPatternSegments(patternSegs[1:], pathSegs[i:]) {
				return true
			}
		}
		return false
	}

	if len(pathSegs) == 0 {
		return false
	}
	if !locksMatchSegment(patternSegs[0], pathSegs[0]) {
		return false
	}
	return locksMatchPatternSegments(patternSegs[1:], pathSegs[1:])
}

func locksMatchSegment(pattern, segment string) bool {
	matched, err := filepath.Match(pattern, segment)
	return err == nil && matched
}

// newLocksCheckAuditToken produces an opaque stable identifier for
// a single check observation. The token is deterministic over its
// inputs so two callers running the same check at the same time
// see the same token (useful for cross-process audit
// correlation) but two checks moments apart see different tokens.
func newLocksCheckAuditToken(projectKey, path, observedAt string) string {
	return fmt.Sprintf(
		"ntm:locks:check:%s:%s:%s",
		strings.ReplaceAll(projectKey, ":", "_"),
		strings.ReplaceAll(path, ":", "_"),
		observedAt,
	)
}

func newLocksListCmd() *cobra.Command {
	var allAgents bool

	cmd := &cobra.Command{
		Use:   "list <session>",
		Short: "Show current file reservations",
		Long: `Display file path reservations for this session or all agents in the project.

Examples:
  ntm locks list myproject               # Show session's reservations
  ntm locks list myproject --all-agents  # Show all project reservations
  ntm locks list myproject --json        # JSON output for scripts`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]
			return runLocks(session, allAgents)
		},
	}

	cmd.Flags().BoolVar(&allAgents, "all-agents", false, "Show reservations for all agents")

	return cmd
}

func newLocksAdviseCmd() *cobra.Command {
	allAgents := true

	cmd := &cobra.Command{
		Use:   "advise <session>",
		Short: "Score reservation and worktree risks without modifying them",
		Long: `Score active file reservations and session worktrees in proof mode.

This command only reports safe next actions such as renew, message holder,
narrow reservation, inspect worktree, or ask human. It never force-releases
reservations and never removes worktrees.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLocksAdvise(args[0], allAgents)
		},
	}

	cmd.Flags().BoolVar(&allAgents, "all-agents", true, "Score reservations for all project agents")
	return cmd
}

// LocksAdviceResult is the proof-mode reservation/worktree advisor output.
type LocksAdviceResult struct {
	Success             bool                                    `json:"success"`
	Session             string                                  `json:"session"`
	Agent               string                                  `json:"agent,omitempty"`
	ProjectKey          string                                  `json:"project_key"`
	Mode                string                                  `json:"mode"`
	AgentMailAvailable  bool                                    `json:"agent_mail_available"`
	Warnings            []string                                `json:"warnings,omitempty"`
	Reservations        reservationsim.ReservationAdvisorReport `json:"reservations"`
	Worktrees           worktrees.WorktreeAdvisorReport         `json:"worktrees"`
	LogRows             []LocksAdviceLogRow                     `json:"log_rows"`
	RecommendationCount int                                     `json:"recommendation_count"`
}

// LocksAdviceLogRow joins reservation and worktree audit rows under one CLI shape.
type LocksAdviceLogRow struct {
	Source        string  `json:"source"`
	ReservationID int     `json:"reservation_id"`
	PathPattern   string  `json:"path_pattern"`
	Holder        string  `json:"holder"`
	WorktreePath  string  `json:"worktree_path"`
	RiskScore     int     `json:"risk_score"`
	Confidence    float64 `json:"confidence"`
	Action        string  `json:"action"`
}

func runLocksAdvise(session string, allAgents bool) error {
	session, projectKey, err := resolveAgentMailScope(session)
	if err != nil {
		return err
	}

	warnings := []string{}
	sessionAgent, err := loadResolvedSessionAgent(session, projectKey)
	if err != nil {
		warnings = append(warnings, "loading session agent: "+err.Error())
	}
	agentName := ""
	if sessionAgent != nil {
		agentName = sessionAgent.AgentName
	}
	if agentName == "" && !allAgents {
		warnings = append(warnings, "session has no Agent Mail identity; scoring all project reservations")
		allAgents = true
	}

	var reservations []agentmail.FileReservation
	agentMailUnavailable := false
	agentMailErr := ""
	client := newAgentMailClient(projectKey)
	if !client.IsAvailable() {
		agentMailUnavailable = true
		agentMailErr = "Agent Mail server unavailable"
		warnings = append(warnings, agentMailErr)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		reservations, err = fetchActiveReservations(ctx, client, projectKey, agentName, allAgents)
		if err != nil {
			agentMailUnavailable = true
			agentMailErr = err.Error()
			warnings = append(warnings, "listing reservations: "+err.Error())
		}
	}

	manager := worktrees.NewManager(projectKey, session)
	worktreeList, err := manager.ListWorktrees()
	if err != nil {
		warnings = append(warnings, "listing worktrees: "+err.Error())
	}

	result := buildLocksAdviceResult(
		session,
		agentName,
		projectKey,
		reservations,
		worktreeList,
		warnings,
		time.Now(),
		agentMailUnavailable,
		agentMailErr,
	)

	if IsJSONOutput() {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(result); encErr != nil {
			return encErr
		}
		return nil
	}

	return printLocksAdviceResult(result)
}

func buildLocksAdviceResult(
	session string,
	agentName string,
	projectKey string,
	reservations []agentmail.FileReservation,
	worktreeList []*worktrees.WorktreeInfo,
	warnings []string,
	now time.Time,
	agentMailUnavailable bool,
	agentMailErr string,
) LocksAdviceResult {
	reservationInputs := make([]reservationsim.ReservationRiskInput, 0, len(reservations))
	for _, r := range reservations {
		reservationInputs = append(reservationInputs, reservationsim.ReservationRiskInput{
			ID:          r.ID,
			PathPattern: r.PathPattern,
			AgentName:   r.AgentName,
			Exclusive:   r.Exclusive,
			Reason:      r.Reason,
			CreatedAt:   r.CreatedTS.Time,
			ExpiresAt:   r.ExpiresTS.Time,
		})
	}

	worktreeInputs := make([]worktrees.WorktreeRiskInput, 0, len(worktreeList))
	for _, wt := range worktreeList {
		worktreeInputs = append(worktreeInputs, worktrees.InspectRiskInput(wt, projectKey))
	}

	reservationReport := reservationsim.AdviseReservations(reservationInputs, reservationsim.ReservationAdvisorOptions{
		Now:                  now,
		AgentMailUnavailable: agentMailUnavailable,
		AgentMailError:       agentMailErr,
	})
	worktreeReport := worktrees.AdviseWorktrees(worktreeInputs, worktrees.WorktreeAdvisorOptions{Now: now})

	logRows := make([]LocksAdviceLogRow, 0, len(reservationReport.LogRows)+len(worktreeReport.LogRows))
	for _, row := range reservationReport.LogRows {
		logRows = append(logRows, LocksAdviceLogRow{
			Source:        "reservation",
			ReservationID: row.ReservationID,
			PathPattern:   row.PathPattern,
			Holder:        row.Holder,
			WorktreePath:  row.WorktreePath,
			RiskScore:     row.RiskScore,
			Confidence:    row.Confidence,
			Action:        row.Action,
		})
	}
	for _, row := range worktreeReport.LogRows {
		logRows = append(logRows, LocksAdviceLogRow{
			Source:        "worktree",
			ReservationID: row.ReservationID,
			PathPattern:   row.PathPattern,
			Holder:        row.Holder,
			WorktreePath:  row.WorktreePath,
			RiskScore:     row.RiskScore,
			Confidence:    row.Confidence,
			Action:        row.Action,
		})
	}
	sortLocksAdviceLogRows(logRows)

	return LocksAdviceResult{
		Success:             true,
		Session:             session,
		Agent:               agentName,
		ProjectKey:          projectKey,
		Mode:                "proof",
		AgentMailAvailable:  !agentMailUnavailable,
		Warnings:            warnings,
		Reservations:        reservationReport,
		Worktrees:           worktreeReport,
		LogRows:             logRows,
		RecommendationCount: len(reservationReport.Recommendations) + len(worktreeReport.Recommendations),
	}
}

func sortLocksAdviceLogRows(rows []LocksAdviceLogRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.RiskScore != b.RiskScore {
			return a.RiskScore > b.RiskScore
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.PathPattern != b.PathPattern {
			return a.PathPattern < b.PathPattern
		}
		if a.WorktreePath != b.WorktreePath {
			return a.WorktreePath < b.WorktreePath
		}
		return a.Holder < b.Holder
	})
}

func printLocksAdviceResult(result LocksAdviceResult) error {
	fmt.Printf("Reservation/worktree advisor (%s mode)\n", result.Mode)
	fmt.Printf("Session: %s\n", result.Session)
	fmt.Printf("Project: %s\n", result.ProjectKey)
	if len(result.Warnings) > 0 {
		fmt.Println("Warnings:")
		for _, warning := range result.Warnings {
			fmt.Printf("  - %s\n", warning)
		}
	}
	if result.RecommendationCount == 0 {
		fmt.Println("No reservation or worktree risks found.")
		return nil
	}
	for _, row := range result.LogRows {
		target := row.PathPattern
		if target == "" {
			target = row.WorktreePath
		}
		fmt.Printf("[%s] %s risk=%d confidence=%.2f action=%s holder=%s\n",
			row.Source, target, row.RiskScore, row.Confidence, row.Action, row.Holder)
	}
	return nil
}

// LocksResult contains the list of active file reservations.
type LocksResult struct {
	Success      bool                        `json:"success"`
	Session      string                      `json:"session"`
	Agent        string                      `json:"agent,omitempty"`
	ProjectKey   string                      `json:"project_key"`
	Reservations []agentmail.FileReservation `json:"reservations"`
	Count        int                         `json:"count"`
	Error        string                      `json:"error,omitempty"`
	// ReasonCode is a stable, machine-readable classification of a failure so
	// robot/automation callers can branch on the cause — most importantly to
	// tell an Agent Mail lock-list *timeout* apart from an auth failure or a
	// down server — without scraping the human-readable Error string.
	ReasonCode string `json:"reason_code,omitempty"`
}

// classifyLocksFailure maps a lock-list failure to a stable reason_code.
// The list path bounds the call with a 10s context deadline, so a hung daemon
// surfaces as context.DeadlineExceeded rather than an Agent Mail timeout error;
// both are classified as agentmail_lock_list_timeout.
func classifyLocksFailure(err error) string {
	switch {
	case err == nil:
		return ""
	case agentmail.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded):
		return "agentmail_lock_list_timeout"
	case agentmail.IsServerUnavailable(err):
		return "agentmail_unavailable"
	case agentmail.IsUnauthorized(err):
		return "agentmail_unauthorized"
	case errors.Is(err, agentmail.ErrAgentNotRegistered):
		return "agent_not_registered"
	case agentmail.IsNotImplemented(err):
		return "agentmail_unsupported"
	case agentmail.IsTransientBusy(err):
		return "agentmail_transient_busy"
	default:
		return "agentmail_lock_list_failed"
	}
}

func runLocks(session string, allAgents bool) error {
	session, projectKey, err := resolveAgentMailScope(session)
	if err != nil {
		return err
	}

	sessionAgent, err := loadResolvedSessionAgent(session, projectKey)
	if err != nil {
		return fmt.Errorf("loading session agent: %w", err)
	}
	if sessionAgent == nil && !allAgents {
		if IsJSONOutput() {
			result := LocksResult{Success: false, Session: session, ProjectKey: projectKey, Error: "Session has no Agent Mail identity", ReasonCode: "no_agent_identity"}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if encErr := enc.Encode(result); encErr != nil {
				return encErr
			}
			return jsonFailureExit()
		}
		return fmt.Errorf("session '%s' has no Agent Mail identity", session)
	}

	agentName := ""
	if sessionAgent != nil {
		agentName = sessionAgent.AgentName
	}

	client := newAgentMailClient(projectKey)
	if !client.IsAvailable() {
		if IsJSONOutput() {
			result := LocksResult{Success: false, Session: session, Agent: agentName, ProjectKey: projectKey, Error: "Agent Mail server unavailable", ReasonCode: "agentmail_unavailable"}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if encErr := enc.Encode(result); encErr != nil {
				return encErr
			}
			return jsonFailureExit()
		}
		return fmt.Errorf("agent mail server unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	reservations, err := fetchActiveReservations(ctx, client, projectKey, agentName, allAgents)

	result := LocksResult{
		Session:      session,
		Agent:        agentName,
		ProjectKey:   projectKey,
		Reservations: reservations,
		Count:        len(reservations),
	}

	if err != nil {
		result.Success = false
		result.Error = err.Error()
		result.ReasonCode = classifyLocksFailure(err)
	} else {
		result.Success = true
	}

	if IsJSONOutput() {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(result); encErr != nil {
			return encErr
		}
		if !result.Success {
			return jsonFailureExit()
		}
		return nil
	}

	return printLocksResult(result, allAgents)
}

func fetchActiveReservations(ctx context.Context, client *agentmail.Client, projectKey, agentName string, allAgents bool) ([]agentmail.FileReservation, error) {
	reservations, err := client.ListReservations(ctx, projectKey, agentName, allAgents)
	if err != nil {
		return nil, fmt.Errorf("listing reservations: %w", err)
	}
	return reservations, nil
}

func printLocksResult(result LocksResult, allAgents bool) error {
	if !result.Success {
		if result.Error != "" {
			return fmt.Errorf("%s", result.Error)
		}
		return fmt.Errorf("failed to list reservations")
	}

	scope := "session"
	if allAgents {
		scope = "project"
	}

	if result.Count == 0 {
		fmt.Printf("No active file reservations (%s scope)\n", scope)
		if result.Agent != "" {
			fmt.Printf("   Agent: %s\n", result.Agent)
		}
		fmt.Printf("   Project: %s\n", result.ProjectKey)
		fmt.Println("\nTip: Use 'ntm lock <session> <pattern>' to reserve files")
		return nil
	}

	fmt.Printf("File Reservations: %d active (%s scope)\n", result.Count, scope)
	fmt.Println(strings.Repeat("-", 60))

	for _, r := range result.Reservations {
		lockType := "Exclusive"
		if !r.Exclusive {
			lockType = "Shared"
		}

		remaining := time.Until(r.ExpiresTS.Time)
		expiresStr := formatLockDuration(remaining)

		fmt.Printf("[#%d] %s\n", r.ID, r.PathPattern)
		fmt.Printf("   Agent: %s | %s | Expires in %s\n", r.AgentName, lockType, expiresStr)
		if r.Reason != "" {
			fmt.Printf("   Reason: %s\n", r.Reason)
		}
		fmt.Println(strings.Repeat("-", 60))
	}

	return nil
}

func formatLockDuration(d time.Duration) string {
	if d < 0 {
		return "expired"
	}

	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60

	if hours > 0 {
		if minutes > 0 {
			return fmt.Sprintf("%dh%dm", hours, minutes)
		}
		return fmt.Sprintf("%dh", hours)
	}

	if minutes > 0 {
		return fmt.Sprintf("%dm", minutes)
	}

	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func newLocksForceReleaseCmd() *cobra.Command {
	var (
		note        string
		noNotify    bool
		skipConfirm bool
	)

	cmd := &cobra.Command{
		Use:   "force-release <session> <reservation-id>",
		Short: "Forcibly release a stale reservation",
		Long: `Forcibly release a file reservation held by another agent.

This command is intended for situations where an agent has become inactive
or unresponsive while holding a reservation that blocks other work.

The server validates inactivity heuristics before allowing the release.
By default, the previous holder is notified about the forced release.

Examples:
  ntm locks force-release myproject 42              # Force release reservation #42
  ntm locks force-release myproject 42 --note "Agent crashed"
  ntm locks force-release myproject 42 --no-notify  # Don't notify previous holder`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]
			reservationIDStr := args[1]

			reservationID, err := strconv.Atoi(reservationIDStr)
			if err != nil {
				return fmt.Errorf("invalid reservation ID '%s': must be a number", reservationIDStr)
			}
			if reservationID < 1 {
				return fmt.Errorf("invalid reservation ID '%s': must be a positive number", reservationIDStr)
			}

			return runForceRelease(session, reservationID, note, !noNotify, skipConfirm)
		},
	}

	cmd.Flags().StringVar(&note, "note", "", "Explanation for the force-release")
	cmd.Flags().BoolVar(&noNotify, "no-notify", false, "Don't notify the previous holder")
	cmd.Flags().BoolVarP(&skipConfirm, "yes", "y", false, "Skip confirmation prompt")

	return cmd
}

// ForceReleaseResult is the JSON output for force-release.
type ForceReleaseResult struct {
	Success        bool       `json:"success"`
	Session        string     `json:"session"`
	Agent          string     `json:"agent"`
	ReservationID  int        `json:"reservation_id"`
	PreviousHolder string     `json:"previous_holder,omitempty"`
	PathPattern    string     `json:"path_pattern,omitempty"`
	ReleasedAt     *time.Time `json:"released_at,omitempty"`
	Notified       bool       `json:"notified,omitempty"`
	Error          string     `json:"error,omitempty"`
}

func runForceRelease(session string, reservationID int, note string, notify, skipConfirm bool) error {
	session, projectKey, err := resolveAgentMailScope(session)
	if err != nil {
		return err
	}

	sessionAgent, err := loadResolvedSessionAgent(session, projectKey)
	if err != nil {
		return fmt.Errorf("loading session agent: %w", err)
	}

	agentName := ""
	if sessionAgent != nil {
		agentName = sessionAgent.AgentName
	} else {
		if IsJSONOutput() {
			result := ForceReleaseResult{Success: false, Session: session, ReservationID: reservationID, Error: "Session has no Agent Mail identity"}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if encErr := enc.Encode(result); encErr != nil {
				return encErr
			}
			return jsonFailureExit()
		}
		return fmt.Errorf("session '%s' has no Agent Mail identity", session)
	}

	client := newAgentMailClient(projectKey)
	if !client.IsAvailable() {
		if IsJSONOutput() {
			result := ForceReleaseResult{Success: false, Session: session, Agent: agentName, ReservationID: reservationID, Error: "Agent Mail server unavailable"}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if encErr := enc.Encode(result); encErr != nil {
				return encErr
			}
			return jsonFailureExit()
		}
		return fmt.Errorf("agent mail server unavailable")
	}

	// Confirmation prompt (unless skipped or JSON mode)
	if !skipConfirm && !IsJSONOutput() {
		fmt.Printf("Force release reservation #%d?\n", reservationID)
		fmt.Printf("  This will notify the previous holder: %v\n", notify)
		if note != "" {
			fmt.Printf("  Note: %s\n", note)
		}
		fmt.Print("\nContinue? [y/N] ")

		var response string
		fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	opts := agentmail.ForceReleaseOptions{
		ProjectKey:     projectKey,
		AgentName:      agentName,
		ReservationID:  reservationID,
		Note:           note,
		NotifyPrevious: notify,
	}

	releaseResult, err := client.ForceReleaseReservation(ctx, opts)

	result := ForceReleaseResult{
		Session:       session,
		Agent:         agentName,
		ReservationID: reservationID,
	}

	if err != nil {
		result.Success = false
		result.Error = err.Error()
	} else if releaseResult != nil {
		result.Success = releaseResult.Success
		result.PreviousHolder = releaseResult.PreviousHolder
		result.PathPattern = releaseResult.PathPattern
		if releaseResult.ReleasedAt != nil {
			t := releaseResult.ReleasedAt.Time
			result.ReleasedAt = &t
		}
		result.Notified = releaseResult.Notified
		// Server may return success=false if reservation is not stale enough
		if !releaseResult.Success {
			result.Error = "force-release denied: reservation may not be stale or agent may still be active"
		}
	}

	if IsJSONOutput() {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(result); encErr != nil {
			return encErr
		}
		if !result.Success {
			return jsonFailureExit()
		}
		return nil
	}

	if result.Success {
		fmt.Printf("Force released reservation #%d\n", reservationID)
		if result.PathPattern != "" {
			fmt.Printf("  Pattern: %s\n", result.PathPattern)
		}
		if result.PreviousHolder != "" {
			fmt.Printf("  Previous holder: %s\n", result.PreviousHolder)
		}
		if result.Notified {
			fmt.Println("  Previous holder has been notified")
		}
		return nil
	}

	if result.Error != "" {
		return fmt.Errorf("%s", result.Error)
	}
	return fmt.Errorf("force-release failed")
}

func newLocksRenewCmd() *cobra.Command {
	var extendMinutes int

	cmd := &cobra.Command{
		Use:   "renew <session>",
		Short: "Extend TTL of active reservations",
		Long: `Extend the time-to-live of all active file reservations for this session.

This is useful when work is taking longer than expected and you need more time
before the reservations expire.

Examples:
  ntm locks renew myproject              # Extend by 30 minutes (default)
  ntm locks renew myproject --extend 60  # Extend by 60 minutes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]
			return runRenewLocks(session, extendMinutes)
		},
	}

	cmd.Flags().IntVar(&extendMinutes, "extend", 30, "Minutes to extend reservations")

	return cmd
}

// RenewResult is the JSON output for renew.
type RenewResult struct {
	Success       bool   `json:"success"`
	Session       string `json:"session"`
	Agent         string `json:"agent"`
	ExtendMinutes int    `json:"extend_minutes"`
	Renewed       int    `json:"renewed"`
	Error         string `json:"error,omitempty"`
}

func runRenewLocks(session string, extendMinutes int) error {
	if extendMinutes < 1 {
		return fmt.Errorf("extend time must be at least 1 minute")
	}

	session, projectKey, err := resolveAgentMailScope(session)
	if err != nil {
		return err
	}

	sessionAgent, err := loadResolvedSessionAgent(session, projectKey)
	if err != nil {
		return fmt.Errorf("loading session agent: %w", err)
	}

	agentName := ""
	if sessionAgent != nil {
		agentName = sessionAgent.AgentName
	} else {
		if IsJSONOutput() {
			result := RenewResult{Success: false, Session: session, Error: "Session has no Agent Mail identity"}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if encErr := enc.Encode(result); encErr != nil {
				return encErr
			}
			return jsonFailureExit()
		}
		return fmt.Errorf("session '%s' has no Agent Mail identity", session)
	}

	client := newAgentMailClient(projectKey)
	if !client.IsAvailable() {
		if IsJSONOutput() {
			result := RenewResult{Success: false, Session: session, Agent: agentName, Error: "Agent Mail server unavailable"}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if encErr := enc.Encode(result); encErr != nil {
				return encErr
			}
			return jsonFailureExit()
		}
		return fmt.Errorf("agent mail server unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	extendSeconds := extendMinutes * 60
	renewResult, err := client.RenewReservations(ctx, agentmail.RenewReservationsOptions{
		ProjectKey:    projectKey,
		AgentName:     agentName,
		ExtendSeconds: extendSeconds,
	})

	result := RenewResult{
		Session:       session,
		Agent:         agentName,
		ExtendMinutes: extendMinutes,
	}

	if err != nil {
		result.Success = false
		result.Error = err.Error()
	} else if renewResult == nil {
		result.Success = false
		result.Error = "renewal returned no result"
	} else {
		result.Renewed = renewResult.Renewed
		if renewResult.Renewed == 0 {
			result.Success = false
			result.Error = "no active reservations were renewed"
		} else {
			result.Success = true
		}
	}

	if IsJSONOutput() {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(result); encErr != nil {
			return encErr
		}
		if !result.Success {
			return jsonFailureExit()
		}
		return nil
	}

	if result.Success {
		fmt.Printf("Extended %d reservation(s) by %d minutes\n", result.Renewed, extendMinutes)
		fmt.Printf("  Agent: %s\n", agentName)
		return nil
	}

	if result.Error != "" {
		return fmt.Errorf("%s", result.Error)
	}
	return fmt.Errorf("renewal failed")
}

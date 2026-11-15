package robot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/handoff"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

const defaultRobotHandoffLimit = 10

var robotHandoffSessionSlugPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// HandoffOptions configures --robot-handoff inspection and creation.
type HandoffOptions struct {
	Session     string            // session name, or "all" / "*" to list sessions + recent handoffs across sessions
	ProjectDir  string            // optional explicit project directory (defaults to resolved workspace/cwd)
	Limit       int               // max handoffs to list (default 10)
	Create      bool              // when true, create and persist a new handoff for Session
	Auto        bool              // when Create=true, auto-generate via handoff.NewGenerator(projectDir).GenerateHandoff
	Goal        string            // explicit Goal (required when Create=true && !Auto, or overrides Auto Goal when non-empty)
	Now         string            // explicit Now (required when Create=true && !Auto, or overrides Auto Now when non-empty)
	Description string            // optional filename slug description when Create=true
	IncludeGit  bool              // include git state when Create=true (default true)
	Next        []string          // optional next steps when Create=true
	Blockers    []string          // optional blockers when Create=true
	Decisions   map[string]string // optional key=value decisions when Create=true
}

// HandoffSummaryItem is the JSON-tagged summary metadata for a persisted handoff file.
type HandoffSummaryItem struct {
	Path    string `json:"path"`
	Session string `json:"session"`
	Date    string `json:"date"`
	Status  string `json:"status,omitempty"`
	Goal    string `json:"goal,omitempty"`
	IsAuto  bool   `json:"is_auto"`
}

// HandoffLedgerSummary reports continuity ledger metadata for a session.
type HandoffLedgerSummary struct {
	Exists bool   `json:"exists"`
	Path   string `json:"path"`
	Bytes  int    `json:"bytes"`
}

// HandoffOutput represents the structured JSON envelope for --robot-handoff.
type HandoffOutput struct {
	RobotResponse
	Action        string                 `json:"action"` // "handoff_inspect" or "handoff_create"
	Session       string                 `json:"session"`
	ProjectDir    string                 `json:"project_dir,omitempty"`
	CreatedPath   string                 `json:"created_path,omitempty"`
	LatestPath    string                 `json:"latest_path,omitempty"`
	Latest        *handoff.Handoff       `json:"latest,omitempty"`
	Quality       *handoff.QualityReport `json:"quality,omitempty"`
	Sessions      []string               `json:"sessions,omitempty"` // always non-nil []string{}
	Handoffs      []HandoffSummaryItem   `json:"handoffs"`           // always non-nil []HandoffSummaryItem{}
	TotalReturned int                    `json:"total_returned"`
	Ledger        *HandoffLedgerSummary  `json:"ledger,omitempty"`
	AgentHints    *AgentHints            `json:"_agent_hints,omitempty"`
}

// GetHandoff inspects or creates Goal/Now session handoffs via the canonical internal/handoff engine.
func GetHandoff(ctx context.Context, opts HandoffOptions) (*HandoffOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	action := "handoff_inspect"
	if opts.Create {
		action = "handoff_create"
	}

	session := strings.TrimSpace(opts.Session)
	if err := ctx.Err(); err != nil {
		return newHandoffErrorOutput(action, session, err, ErrCodeTimeout, "Retry --robot-handoff with an active context"), nil
	}
	if session == "" {
		return newHandoffErrorOutput(
			action,
			"",
			errors.New("--robot-handoff requires a session name or 'all'"),
			ErrCodeInvalidFlag,
			"Provide a session name or 'all': ntm --robot-handoff=myproject",
		), nil
	}

	limit := opts.Limit
	if limit < 0 {
		return newHandoffErrorOutput(
			action,
			session,
			fmt.Errorf("--handoff-limit must be zero or greater (got %d)", limit),
			ErrCodeInvalidFlag,
			"Pass a positive --handoff-limit such as --handoff-limit=10",
		), nil
	}
	if limit == 0 {
		limit = defaultRobotHandoffLimit
	}

	isWildcard := strings.EqualFold(session, "all") || session == "*"
	if opts.Create && isWildcard {
		return newHandoffErrorOutput(
			action,
			session,
			fmt.Errorf("cannot create a handoff for wildcard session %q", session),
			ErrCodeInvalidFlag,
			"Pass a concrete session name with --robot-handoff=SESSION --handoff-create",
		), nil
	}
	if !isWildcard && !isSafeHandoffSessionSlug(session) {
		return newHandoffErrorOutput(
			action,
			session,
			fmt.Errorf("invalid session name %q", session),
			ErrCodeInvalidFlag,
			"Use an alphanumeric session name (letters, digits, hyphens, underscores)",
		), nil
	}
	if isWildcard && strings.EqualFold(session, "all") {
		session = "all"
	}

	projectDir, errCode, errHint, err := resolveRobotHandoffProjectDir(ctx, opts.ProjectDir)
	if err != nil {
		return newHandoffErrorOutput(action, session, err, errCode, errHint), nil
	}

	if opts.Create {
		return createRobotHandoff(ctx, session, projectDir, limit, opts)
	}
	return inspectRobotHandoff(ctx, session, isWildcard, projectDir, limit)
}

// PrintHandoff executes GetHandoff and writes the terminal JSON/TOON response.
func PrintHandoff(ctx context.Context, opts HandoffOptions) error {
	output, err := GetHandoff(ctx, opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot handoff failed")
}

func newHandoffErrorOutput(action, session string, err error, code, hint string) *HandoffOutput {
	return &HandoffOutput{
		RobotResponse: NewErrorResponse(err, code, hint),
		Action:        action,
		Session:       session,
		Sessions:      []string{},
		Handoffs:      []HandoffSummaryItem{},
	}
}

func isSafeHandoffSessionSlug(session string) bool {
	if session == "" || len(session) > 64 {
		return false
	}
	if strings.Contains(session, "..") || strings.ContainsAny(session, `/\`) {
		return false
	}
	return robotHandoffSessionSlugPattern.MatchString(session)
}

func resolveRobotHandoffProjectDir(ctx context.Context, explicitDir string) (string, string, string, error) {
	trimmed := strings.TrimSpace(explicitDir)
	if trimmed != "" {
		if !filepath.IsAbs(trimmed) {
			return "", ErrCodeInvalidFlag, "Pass an absolute directory path via --handoff-project-dir", fmt.Errorf("--handoff-project-dir must be an absolute path (got %q)", trimmed)
		}
		cleaned := filepath.Clean(trimmed)
		info, err := os.Stat(cleaned)
		if err != nil || !info.IsDir() {
			return "", ErrCodeInvalidFlag, "Ensure --handoff-project-dir points to an existing directory", fmt.Errorf("--handoff-project-dir %q does not exist or is not a directory", cleaned)
		}
		return cleaned, "", "", nil
	}

	resolved, err := util.ResolveProjectDirContext(ctx, "")
	if err == nil && strings.TrimSpace(resolved) != "" {
		return filepath.Clean(resolved), "", "", nil
	}
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return "", ErrCodeTimeout, "Retry --robot-handoff with an active context", err
	}

	cwd, cwdErr := os.Getwd()
	if cwdErr != nil || strings.TrimSpace(cwd) == "" {
		if cwdErr == nil {
			cwdErr = errors.New("unable to resolve working directory")
		}
		return "", ErrCodeInternalError, "Pass --handoff-project-dir with an explicit absolute path", cwdErr
	}
	return filepath.Clean(cwd), "", "", nil
}

func createRobotHandoff(ctx context.Context, session, projectDir string, limit int, opts HandoffOptions) (*HandoffOutput, error) {
	const action = "handoff_create"

	goal := strings.TrimSpace(opts.Goal)
	now := strings.TrimSpace(opts.Now)
	if !opts.Auto && (goal == "" || now == "") {
		return newHandoffErrorOutput(
			action,
			session,
			errors.New("Specify both --handoff-goal and --handoff-now, or pass --handoff-auto"),
			ErrCodeInvalidFlag,
			"Specify both --handoff-goal and --handoff-now, or pass --handoff-auto",
		), nil
	}

	generator := handoff.NewGenerator(projectDir)
	var h *handoff.Handoff

	if opts.Auto {
		genCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		agentName := ""
		if session != "general" {
			if info, err := agentmail.LoadBestSessionAgent(session, projectDir); err == nil && info != nil {
				agentName = info.AgentName
			} else {
				agentName = session
			}
		}

		var capturedOutput []byte
		if goal == "" || now == "" {
			capturedOutput = captureRobotHandoffPaneOutput(genCtx, session)
		}

		generated, err := generator.GenerateHandoff(genCtx, handoff.GenerateHandoffOptions{
			SessionName:          session,
			ProjectKey:           projectDir,
			AgentName:            agentName,
			Goal:                 goal,
			Now:                  now,
			Output:               capturedOutput,
			TransferGraceSeconds: 2,
		})
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return newHandoffErrorOutput(action, session, ctxErr, ErrCodeTimeout, "Retry --robot-handoff after cancellation"), nil
			}
			return newHandoffErrorOutput(action, session, err, ErrCodeInternalError, "Check repository and workspace state or supply explicit --handoff-goal/--handoff-now"), nil
		}
		h = generated

		if !opts.IncludeGit {
			h.Files = handoff.FileChanges{}
			delete(h.Findings, "git_branch")
			delete(h.Findings, "recent_commits")
		}
		if goal != "" {
			h.Goal = goal
		}
		if now != "" {
			h.Now = now
		}
		populateAutoHandoffFallbacks(h, session)
		applyRobotHandoffAdditions(h, opts)
		if len(h.Blockers) > 0 {
			h.Status = handoff.StatusBlocked
			h.Outcome = handoff.OutcomePartialMinus
		} else {
			switch h.Status {
			case "", handoff.StatusPartial:
				h.Status = handoff.StatusComplete
				h.Outcome = handoff.OutcomeSucceeded
			}
		}
	} else {
		h = handoff.New(session)
		h.Goal = goal
		h.Now = now
		applyRobotHandoffAdditions(h, opts)
		if len(h.Blockers) > 0 {
			h.Status = handoff.StatusBlocked
			h.Outcome = handoff.OutcomePartialMinus
		} else {
			h.Status = handoff.StatusComplete
			h.Outcome = handoff.OutcomeSucceeded
		}
		if opts.IncludeGit {
			if err := generator.EnrichWithGitState(ctx, h); err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return newHandoffErrorOutput(action, session, ctxErr, ErrCodeTimeout, "Retry --robot-handoff after cancellation"), nil
				}
				h.AddWarning("git", err.Error())
			}
		}
	}

	h.SetDefaults()
	h.UpdateQuality(time.Now())
	if errs := h.Validate(); errs.HasErrors() {
		return newHandoffErrorOutput(
			action,
			session,
			errs,
			ErrCodeInvalidFlag,
			"Ensure session, --handoff-goal, --handoff-now, and --handoff-description satisfy handoff validation rules",
		), nil
	}

	slug := strings.TrimSpace(opts.Description)
	if slug == "" {
		if opts.Auto {
			slug = "auto-handoff"
		} else {
			slug = slugifyHandoffGoal(h.Goal)
		}
	}

	createdPath, err := handoff.NewWriter(projectDir).Write(h, slug)
	if err != nil {
		var valErrs handoff.ValidationErrors
		if errors.As(err, &valErrs) {
			return newHandoffErrorOutput(action, session, err, ErrCodeInvalidFlag, "Fix invalid handoff parameters and retry"), nil
		}
		return newHandoffErrorOutput(action, session, err, ErrCodeInternalError, "Check .ntm/handoffs write permissions and path safety"), nil
	}

	reader := handoff.NewReader(projectDir)
	sessions := listHandoffSessionsNonNil(reader)
	items := listSessionHandoffItems(reader, session, limit)
	ledger := inspectHandoffLedger(projectDir, session)

	return &HandoffOutput{
		RobotResponse: NewRobotResponse(true),
		Action:        action,
		Session:       session,
		ProjectDir:    projectDir,
		CreatedPath:   createdPath,
		LatestPath:    createdPath,
		Latest:        h,
		Quality:       h.Quality,
		Sessions:      sessions,
		Handoffs:      items,
		TotalReturned: len(items),
		Ledger:        ledger,
		AgentHints:    buildHandoffAgentHints(action, session, h, len(items)),
	}, nil
}

func inspectRobotHandoff(ctx context.Context, session string, isWildcard bool, projectDir string, limit int) (*HandoffOutput, error) {
	const action = "handoff_inspect"

	if err := ctx.Err(); err != nil {
		return newHandoffErrorOutput(action, session, err, ErrCodeTimeout, "Retry --robot-handoff with an active context"), nil
	}

	reader := handoff.NewReader(projectDir)
	sessions := listHandoffSessionsNonNil(reader)

	out := &HandoffOutput{
		RobotResponse: NewRobotResponse(true),
		Action:        action,
		Session:       session,
		ProjectDir:    projectDir,
		Sessions:      sessions,
		Handoffs:      []HandoffSummaryItem{},
	}

	if isWildcard {
		var allMetas []handoff.HandoffMeta
		for _, sess := range sessions {
			metas, err := reader.ListHandoffs(sess)
			if err != nil {
				continue
			}
			allMetas = append(allMetas, metas...)
		}
		sort.Slice(allMetas, func(i, j int) bool {
			if !allMetas[i].Date.Equal(allMetas[j].Date) {
				return allMetas[i].Date.After(allMetas[j].Date)
			}
			return allMetas[i].Path > allMetas[j].Path
		})
		out.Handoffs = toHandoffSummaryItems(allMetas, limit)
		out.TotalReturned = len(out.Handoffs)

		if len(allMetas) > 0 {
			if latest, err := reader.Read(allMetas[0].Path); err == nil && latest != nil {
				score := latest.ScoreQuality(time.Now())
				latest.Quality = &score
				out.LatestPath = allMetas[0].Path
				out.Latest = latest
				out.Quality = &score
			}
		}
		out.AgentHints = buildHandoffAgentHints(action, session, out.Latest, out.TotalReturned)
		return out, nil
	}

	metas, err := reader.ListHandoffs(session)
	if err != nil {
		return newHandoffErrorOutput(action, session, err, ErrCodeInternalError, "Verify .ntm/handoffs directory permissions and symlink safety"), nil
	}
	out.Handoffs = toHandoffSummaryItems(metas, limit)
	out.TotalReturned = len(out.Handoffs)
	out.Ledger = inspectHandoffLedger(projectDir, session)

	latest, latestPath, err := reader.FindLatest(session)
	if err != nil {
		return newHandoffErrorOutput(action, session, err, ErrCodeInternalError, "Inspect persisted YAML handoffs under .ntm/handoffs for syntax errors"), nil
	}
	if latest != nil {
		score := latest.ScoreQuality(time.Now())
		latest.Quality = &score
		out.LatestPath = latestPath
		out.Latest = latest
		out.Quality = &score
	}
	out.AgentHints = buildHandoffAgentHints(action, session, out.Latest, out.TotalReturned)
	return out, nil
}

func captureRobotHandoffPaneOutput(ctx context.Context, sessionName string) []byte {
	if sessionName == "" || sessionName == "general" || !tmux.IsInstalled() {
		return nil
	}
	panes, err := tmux.GetPanesContext(ctx, sessionName)
	if err != nil {
		slog.Debug("robot handoff auto: pane enumeration skipped",
			"session", sessionName, "error", err)
		return nil
	}

	const linesPerPane = 200
	var buf strings.Builder
	for _, p := range panes {
		if p.Type == tmux.AgentUser || p.Type == tmux.AgentUnknown {
			continue
		}
		content, capErr := tmux.CapturePaneOutputContext(ctx, p.ID, linesPerPane)
		if capErr != nil || strings.TrimSpace(content) == "" {
			continue
		}
		fmt.Fprintf(&buf, "=== pane %s (%s) ===\n%s\n", p.Title, p.Type, content)
	}
	if buf.Len() == 0 {
		return nil
	}
	return []byte(buf.String())
}

func populateAutoHandoffFallbacks(h *handoff.Handoff, session string) {
	if strings.TrimSpace(h.Goal) == "" {
		switch {
		case len(h.ActiveBeads) > 0:
			h.Goal = fmt.Sprintf("Captured auto-handoff state for %s (%s)", session, h.ActiveBeads[0])
		case h.TotalFileChanges() > 0:
			h.Goal = fmt.Sprintf("Captured auto-handoff state for %s with %d changed file(s)", session, h.TotalFileChanges())
		case len(strings.TrimSpace(h.Findings["recent_commits"])) > 0:
			firstCommit := strings.TrimSpace(strings.SplitN(h.Findings["recent_commits"], ";", 2)[0])
			h.Goal = fmt.Sprintf("Captured auto-handoff state for %s (%s)", session, firstCommit)
		default:
			h.Goal = fmt.Sprintf("Captured auto-handoff snapshot for session %s", session)
		}
	}

	if strings.TrimSpace(h.Now) == "" {
		switch {
		case len(h.Next) > 0 && len(strings.TrimSpace(h.Next[0])) > 0:
			h.Now = strings.TrimSpace(h.Next[0])
		case len(h.ActiveBeads) > 0:
			h.Now = fmt.Sprintf("Continue in-progress work on %s", h.ActiveBeads[0])
		case len(h.Files.Modified) > 0:
			h.Now = fmt.Sprintf("Review modified files starting with %s", h.Files.Modified[0])
		case len(h.Files.Created) > 0:
			h.Now = fmt.Sprintf("Review newly created files starting with %s", h.Files.Created[0])
		default:
			h.Now = fmt.Sprintf("Inspect workspace status and continue session %s", session)
		}
	}
}

func applyRobotHandoffAdditions(h *handoff.Handoff, opts HandoffOptions) {
	for _, step := range opts.Next {
		if trimmed := strings.TrimSpace(step); trimmed != "" {
			h.Next = append(h.Next, trimmed)
		}
	}
	for _, blocker := range opts.Blockers {
		if trimmed := strings.TrimSpace(blocker); trimmed != "" {
			h.AddBlocker(trimmed)
		}
	}
	if len(opts.Decisions) > 0 {
		keys := make([]string, 0, len(opts.Decisions))
		for k := range opts.Decisions {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			trimmedKey := strings.TrimSpace(k)
			if trimmedKey == "" {
				continue
			}
			h.AddDecision(trimmedKey, strings.TrimSpace(opts.Decisions[k]))
		}
	}
}

func slugifyHandoffGoal(goal string) string {
	desc := strings.ToLower(strings.TrimSpace(goal))
	if desc == "" {
		return "handoff"
	}
	words := strings.Fields(desc)
	if len(words) > 8 {
		words = words[:8]
	}
	desc = strings.Join(words, "-")

	var b strings.Builder
	for _, r := range desc {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	desc = b.String()
	for strings.Contains(desc, "--") {
		desc = strings.ReplaceAll(desc, "--", "-")
	}
	desc = strings.Trim(desc, "-")
	if len(desc) > 30 {
		desc = strings.TrimRight(desc[:30], "-")
	}
	if desc == "" {
		return "handoff"
	}
	return desc
}

func listHandoffSessionsNonNil(reader *handoff.Reader) []string {
	sessions, err := reader.ListSessions()
	if err != nil || len(sessions) == 0 {
		return []string{}
	}
	sort.Strings(sessions)
	return sessions
}

func listSessionHandoffItems(reader *handoff.Reader, session string, limit int) []HandoffSummaryItem {
	metas, err := reader.ListHandoffs(session)
	if err != nil {
		return []HandoffSummaryItem{}
	}
	return toHandoffSummaryItems(metas, limit)
}

func toHandoffSummaryItems(metas []handoff.HandoffMeta, limit int) []HandoffSummaryItem {
	if len(metas) == 0 || limit <= 0 {
		return []HandoffSummaryItem{}
	}
	if len(metas) > limit {
		metas = metas[:limit]
	}
	items := make([]HandoffSummaryItem, 0, len(metas))
	for _, m := range metas {
		dateStr := ""
		if !m.Date.IsZero() {
			dateStr = FormatTimestamp(m.Date)
		}
		items = append(items, HandoffSummaryItem{
			Path:    m.Path,
			Session: m.Session,
			Date:    dateStr,
			Status:  m.Status,
			Goal:    m.Goal,
			IsAuto:  m.IsAuto || strings.HasSuffix(filepath.Base(m.Path), "_auto-handoff.yaml"),
		})
	}
	return items
}

func inspectHandoffLedger(projectDir, session string) *HandoffLedgerSummary {
	ledgerPath := filepath.Join(projectDir, ".ntm", "ledgers", fmt.Sprintf("CONTINUITY_%s.md", session))
	info, err := os.Stat(ledgerPath)
	if err != nil || info.IsDir() {
		return &HandoffLedgerSummary{
			Exists: false,
			Path:   ledgerPath,
			Bytes:  0,
		}
	}
	return &HandoffLedgerSummary{
		Exists: true,
		Path:   ledgerPath,
		Bytes:  int(info.Size()),
	}
}

func buildHandoffAgentHints(action, session string, latest *handoff.Handoff, totalReturned int) *AgentHints {
	if action == "handoff_create" && latest != nil {
		score := 0
		if latest.Quality != nil {
			score = latest.Quality.Score
		}
		return &AgentHints{
			Summary: fmt.Sprintf("Created handoff for session %q (quality score %d/100)", session, score),
			SuggestedActions: []RobotAction{
				{
					Action:   "inspect_handoff",
					Target:   session,
					Reason:   "Inspect persisted handoff history and continuity ledger",
					Priority: 1,
				},
			},
		}
	}

	if latest == nil || totalReturned == 0 {
		targetSession := session
		if targetSession == "all" || targetSession == "*" {
			targetSession = "myproject"
		}
		return &AgentHints{
			Summary: fmt.Sprintf("No persisted handoffs found for %q", session),
			SuggestedActions: []RobotAction{
				{
					Action:   "create_handoff",
					Target:   targetSession,
					Reason:   fmt.Sprintf("Create one with ntm --robot-handoff=%s --handoff-create --handoff-auto or supply --handoff-goal and --handoff-now", targetSession),
					Priority: 1,
				},
			},
		}
	}

	score := 0
	if latest.Quality != nil {
		score = latest.Quality.Score
	}
	return &AgentHints{
		Summary: fmt.Sprintf("Latest handoff for %q has status=%s (quality %d/100); next: %s", latest.Session, latest.Status, score, latest.Now),
	}
}

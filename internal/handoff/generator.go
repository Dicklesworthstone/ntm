package handoff

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/cass"
)

const (
	defaultCASSLimit          = 5
	defaultCASSTimeout        = 3 * time.Second
	generatorCommandWaitDelay = 250 * time.Millisecond
)

type generatorCommandRunner func(context.Context, string, time.Duration, string, ...string) ([]byte, error)

// CASSSearcher defines the minimal CASS client surface needed for handoff enrichment.
type CASSSearcher interface {
	IsInstalled() bool
	Search(ctx context.Context, opts cass.SearchOptions) (*cass.SearchResponse, error)
}

// Generator creates handoff content from various sources.
type Generator struct {
	projectDir    string
	logger        *slog.Logger
	commandOutput generatorCommandRunner
}

// NewGenerator creates a Generator for the given project directory.
func NewGenerator(projectDir string) *Generator {
	return &Generator{
		projectDir:    projectDir,
		logger:        slog.Default().With("component", "handoff.generator"),
		commandOutput: runGeneratorCommand,
	}
}

func requireGeneratorContext(ctx context.Context, operation string) error {
	if ctx == nil {
		return fmt.Errorf("%s: context is required", operation)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}

func generatorCancellationError(ctx context.Context, err error, operation string) error {
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%s: %w", operation, ctxErr)
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}

func runGeneratorCommand(ctx context.Context, dir string, timeout time.Duration, name string, args ...string) ([]byte, error) {
	if err := requireGeneratorContext(ctx, "run handoff command"); err != nil {
		return nil, err
	}

	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(commandCtx, name, args...)
	cmd.Dir = dir
	cmd.WaitDelay = generatorCommandWaitDelay
	out, err := cmd.Output()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if commandCtx.Err() != nil {
		return nil, fmt.Errorf("%s command exceeded %s timeout", name, timeout)
	}
	return out, err
}

// EnrichWithGitState adds git information to handoff.
func (g *Generator) EnrichWithGitState(ctx context.Context, h *Handoff) error {
	if err := requireGeneratorContext(ctx, "enrich handoff with git state"); err != nil {
		return err
	}
	if h == nil {
		return errors.New("enrich handoff with git state: handoff is required")
	}

	g.logger.Debug("enriching handoff with git state")
	hasGit, err := g.hasGitContext(ctx)
	if err != nil {
		return fmt.Errorf("git probe: %w", err)
	}
	if !hasGit {
		return nil
	}

	// Get modified files from git diff
	modified, err := g.getGitModified(ctx)
	if err != nil {
		return fmt.Errorf("git modified: %w", err)
	}
	// Merge with existing, don't overwrite
	h.Files.Modified = uniqueStrings(append(h.Files.Modified, modified...))

	// Get new files from git status
	created, err := g.getGitUntracked(ctx)
	if err != nil {
		return fmt.Errorf("git untracked: %w", err)
	}
	h.Files.Created = uniqueStrings(append(h.Files.Created, created...))

	// Get current branch for context
	branch, err := g.getGitBranch(ctx)
	if cancelErr := generatorCancellationError(ctx, err, "get git branch"); cancelErr != nil {
		return cancelErr
	}
	if branch != "" {
		h.AddFinding("git_branch", branch)
	}

	// Get recent commits (session could have made commits)
	commits, err := g.getRecentCommits(ctx, 5)
	if cancelErr := generatorCancellationError(ctx, err, "get recent git commits"); cancelErr != nil {
		return cancelErr
	}
	if len(commits) > 0 {
		h.AddFinding("recent_commits", strings.Join(commits, "; "))
	}

	g.logger.Debug("enriched with git state",
		"modified", len(h.Files.Modified),
		"created", len(h.Files.Created),
		"branch", branch,
	)

	return requireGeneratorContext(ctx, "enrich handoff with git state")
}

// analysisResult holds extracted information from output.
type analysisResult struct {
	accomplishment string
	nextStep       string
	tasks          []TaskRecord
	blockers       []string
	decisions      map[string]string
	todos          []string
}

// Compiled regex patterns for performance
var (
	// Accomplishment patterns - agent-specific
	// Using (?im) for case-insensitive and multiline (^ matches line start)
	accomplishmentPatterns = []*regexp.Regexp{
		// Claude patterns
		regexp.MustCompile(`(?i)I've completed?\s+(.+?)\.`),
		regexp.MustCompile(`(?im)^Done:?\s*(.+)`),
		regexp.MustCompile(`(?im)^Finished:?\s*(.+)`),
		regexp.MustCompile(`(?im)^\s*[✓✔]\s*(.+)`),
		regexp.MustCompile(`(?i)Successfully\s+(.+?)\.`),
		regexp.MustCompile(`(?i)Implemented\s+(.+?)\.`),
		// Codex patterns
		regexp.MustCompile(`\[DONE\]\s*(.+)`),
		regexp.MustCompile(`(?i)Completed task:?\s*(.+)`),
		// Gemini patterns
		regexp.MustCompile(`(?i)Task complete:?\s*(.+)`),
	}

	// Next step patterns
	nextPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?im)^Next:?\s*(.+)`),
		regexp.MustCompile(`(?im)^TODO:?\s*(.+)`),
		regexp.MustCompile(`(?i)Should do next:?\s*(.+)`),
		regexp.MustCompile(`(?im)^Remaining:?\s*(.+)`),
		regexp.MustCompile(`(?i)Now (?:you should|we should|I should):?\s*(.+)`),
	}

	// Blocker patterns
	blockerPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?im)^Error:?\s*(.+)`),
		regexp.MustCompile(`(?im)^Failed:?\s*(.+)`),
		regexp.MustCompile(`(?i)Blocked by:?\s*(.+)`),
		regexp.MustCompile(`(?i)Cannot proceed:?\s*(.+)`),
		regexp.MustCompile(`(?i)Unable to:?\s*(.+)`),
	}

	// Decision patterns
	decisionPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)I decided to\s+(.+?)\s+because\s+(.+?)\.`),
		regexp.MustCompile(`(?i)Chose\s+(.+?)\s+over\s+(.+?)\s+because`),
		regexp.MustCompile(`(?i)Using\s+(.+?)\s+for\s+(.+)`),
	}
)

// analyzeOutput extracts key information from agent output text.
func (g *Generator) analyzeOutput(output []byte) analysisResult {
	result := analysisResult{
		decisions: make(map[string]string),
	}

	text := string(output)

	// Find accomplishment
	for _, pat := range accomplishmentPatterns {
		if match := pat.FindStringSubmatch(text); match != nil {
			result.accomplishment = strings.TrimSpace(match[1])
			break
		}
	}

	// Find next step
	for _, pat := range nextPatterns {
		if match := pat.FindStringSubmatch(text); match != nil {
			result.nextStep = strings.TrimSpace(match[1])
			break
		}
	}

	// Find blockers - collect up to 5
	for _, pat := range blockerPatterns {
		matches := pat.FindAllStringSubmatch(text, 5)
		for _, m := range matches {
			result.blockers = append(result.blockers, strings.TrimSpace(m[1]))
		}
	}
	// Limit blockers to prevent bloat
	if len(result.blockers) > 5 {
		result.blockers = result.blockers[:5]
	}

	// Find decisions
	for _, pat := range decisionPatterns {
		matches := pat.FindAllStringSubmatch(text, 5)
		for _, m := range matches {
			if len(m) >= 3 {
				key := truncateGen(m[1], 30)
				result.decisions[key] = truncateGen(m[2], 50)
			}
		}
	}

	g.logger.Debug("analyzed output",
		"has_accomplishment", result.accomplishment != "",
		"has_next", result.nextStep != "",
		"blocker_count", len(result.blockers),
		"decision_count", len(result.decisions),
	)

	return result
}

// Git helper functions

func (g *Generator) runCommand(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	runner := g.commandOutput
	if runner == nil {
		runner = runGeneratorCommand
	}
	return runner(ctx, g.projectDir, timeout, name, args...)
}

func (g *Generator) hasGitContext(ctx context.Context) (bool, error) {
	if err := requireGeneratorContext(ctx, "probe git context"); err != nil {
		return false, err
	}
	if strings.TrimSpace(g.projectDir) == "" {
		return false, nil
	}

	out, err := g.runCommand(ctx, 5*time.Second, "git", "rev-parse", "--is-inside-work-tree")
	if err != nil {
		g.logger.Debug("git probe failed", "project_dir", g.projectDir, "error", err)
		return false, err
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

func (g *Generator) getGitModified(ctx context.Context) ([]string, error) {
	out, err := g.runCommand(ctx, 10*time.Second, "git", "diff", "--name-only", "HEAD")
	if err != nil {
		return nil, err
	}
	return parseLines(out), nil
}

func (g *Generator) getGitUntracked(ctx context.Context) ([]string, error) {
	out, err := g.runCommand(ctx, 10*time.Second, "git", "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	return parseLines(out), nil
}

func (g *Generator) getGitBranch(ctx context.Context) (string, error) {
	out, err := g.runCommand(ctx, 5*time.Second, "git", "branch", "--show-current")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (g *Generator) getRecentCommits(ctx context.Context, n int) ([]string, error) {
	out, err := g.runCommand(ctx, 10*time.Second, "git", "log", fmt.Sprintf("-%d", n), "--oneline")
	if err != nil {
		return nil, err
	}
	return parseLines(out), nil
}

// Utility functions

func parseLines(data []byte) []string {
	var lines []string
	for _, line := range bytes.Split(data, []byte("\n")) {
		s := strings.TrimSpace(string(line))
		if s != "" {
			lines = append(lines, s)
		}
	}
	return lines
}

func uniqueStrings(s []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result
}

// truncateGen truncates a string to max bytes at a valid UTF-8 boundary.
func truncateGen(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Walk runes to find the last valid boundary within maxBytes
	end := 0
	for i := range s {
		if i > maxBytes {
			break
		}
		end = i
	}
	return s[:end]
}

// =============================================================================
// GenerateHandoff - Main Entry Point with BV and Agent Mail Integration
// =============================================================================

// GenerateHandoffOptions configures handoff generation.
type GenerateHandoffOptions struct {
	// SessionName identifies this session (required)
	SessionName string

	// AgentName is the agent's Agent Mail identity (optional, enables Agent Mail integration)
	AgentName string

	// AgentType is the agent type (cc, cod, gmi)
	AgentType string

	// PaneID is the tmux pane ID (optional)
	PaneID string

	// ProjectKey is the project path for Agent Mail (defaults to projectDir)
	ProjectKey string

	// TokensUsed is the current token usage
	TokensUsed int

	// TokensMax is the maximum token budget
	TokensMax int

	// Goal is the explicit goal (if known, skips analysis)
	Goal string

	// Now is the explicit next action (if known, skips analysis)
	Now string

	// Output is optional agent output to analyze
	Output []byte

	// IncludeBeads enables BV integration (default: true if bv available)
	IncludeBeads *bool

	// IncludeAgentMail enables Agent Mail integration (default: true if agentmail available)
	IncludeAgentMail *bool

	// IncludeCASS enables CASS context enrichment (default: true if cass is installed)
	IncludeCASS *bool

	// BVClient is an optional pre-configured BV client
	BVClient *bv.BVClient

	// AgentMailClient is an optional pre-configured Agent Mail client
	AgentMailClient *agentmail.Client

	// CASSClient is an optional pre-configured CASS client
	CASSClient CASSSearcher

	// CASSLimit caps context snippets pulled from CASS (default: 5, max: 20)
	CASSLimit int

	// CASSSince scopes CASS search recency (default: 30d)
	CASSSince string

	// CASSTimeout bounds CASS search time when creating a default client (default: 3s).
	CASSTimeout time.Duration

	// TransferTTLSeconds refreshes reservation TTL when preparing transfer instructions.
	TransferTTLSeconds int

	// TransferGraceSeconds adds a retry grace period during transfer (seconds).
	TransferGraceSeconds int
}

// GenerateHandoff creates a complete handoff with BV and Agent Mail integration.
// This is the main entry point for handoff generation, gathering:
//   - Git state (uncommitted changes, branch, recent commits)
//   - Active beads from BV (in-progress tasks assigned to this agent)
//   - Agent Mail state (inbox messages, file reservations)
//   - CASS context snippets with source provenance
//
// All integrations are optional and fail gracefully if unavailable.
func (g *Generator) GenerateHandoff(ctx context.Context, opts GenerateHandoffOptions) (*Handoff, error) {
	if err := requireGeneratorContext(ctx, "generate complete handoff"); err != nil {
		return nil, err
	}

	g.logger.Debug("generating complete handoff",
		"session", opts.SessionName,
		"agent_name", opts.AgentName,
		"agent_type", opts.AgentType,
	)

	// Create base handoff
	h := New(opts.SessionName)

	// Set agent info (AgentID is optional)
	h.SetAgentInfo(opts.AgentName, opts.AgentType, opts.PaneID)

	// Set token info if provided
	if opts.TokensMax > 0 {
		h.SetTokenInfo(opts.TokensUsed, opts.TokensMax)
	}

	// Set explicit goal/now if provided
	if opts.Goal != "" {
		h.Goal = opts.Goal
	}
	if opts.Now != "" {
		h.Now = opts.Now
	}

	// Analyze output if provided and goal/now not explicitly set
	if len(opts.Output) > 0 && (h.Goal == "" || h.Now == "") {
		analysis := g.analyzeOutput(opts.Output)
		if h.Goal == "" {
			h.Goal = analysis.accomplishment
		}
		if h.Now == "" {
			h.Now = analysis.nextStep
		}
		h.DoneThisSession = analysis.tasks
		h.Blockers = analysis.blockers
		h.Decisions = analysis.decisions
		h.Next = analysis.todos
	}
	if err := requireGeneratorContext(ctx, "generate complete handoff"); err != nil {
		return nil, err
	}

	// Enrich with git state
	if err := g.EnrichWithGitState(ctx, h); err != nil {
		if cancelErr := generatorCancellationError(ctx, err, "generate complete handoff"); cancelErr != nil {
			return nil, cancelErr
		}
		g.logger.Warn("git enrichment failed", "error", err)
		// Non-fatal - continue without git info
	}

	// Enrich with BV beads
	includeBeads := opts.IncludeBeads == nil || *opts.IncludeBeads
	if includeBeads {
		if err := g.enrichWithBeads(ctx, h, opts); err != nil {
			if cancelErr := generatorCancellationError(ctx, err, "generate complete handoff"); cancelErr != nil {
				return nil, cancelErr
			}
			g.logger.Warn("BV enrichment failed", "error", err)
			// Non-fatal - continue without bead info
		}
	}

	// Enrich with Agent Mail
	includeAgentMail := opts.IncludeAgentMail == nil || *opts.IncludeAgentMail
	if includeAgentMail && opts.AgentName != "" {
		if err := g.enrichWithAgentMail(ctx, h, opts); err != nil {
			if cancelErr := generatorCancellationError(ctx, err, "generate complete handoff"); cancelErr != nil {
				return nil, cancelErr
			}
			g.logger.Warn("Agent Mail enrichment failed", "error", err)
			// Non-fatal - continue without Agent Mail info
		}
	}

	// Enrich with CASS context snippets (provenance-aware; graceful degradation)
	includeCASS := opts.IncludeCASS == nil || *opts.IncludeCASS
	if includeCASS {
		if err := g.enrichWithCASS(ctx, h, opts); err != nil {
			if cancelErr := generatorCancellationError(ctx, err, "generate complete handoff"); cancelErr != nil {
				return nil, cancelErr
			}
			g.logger.Warn("CASS enrichment failed", "error", err)
			// Non-fatal - continue without CASS context
		}
	}

	if err := requireGeneratorContext(ctx, "generate complete handoff"); err != nil {
		return nil, err
	}

	// Infer status if not set
	if h.Status == "" {
		if len(h.Blockers) > 0 {
			h.Status = StatusBlocked
			h.Outcome = OutcomePartialMinus
		} else if h.Goal != "" {
			h.Status = StatusComplete
			h.Outcome = OutcomeSucceeded
		} else {
			h.Status = StatusPartial
			h.Outcome = OutcomePartialPlus
		}
	}

	// Set timestamps
	h.UpdatedAt = time.Now()

	g.logger.Info("generated complete handoff",
		"session", opts.SessionName,
		"beads_count", len(h.ActiveBeads),
		"threads_count", len(h.AgentMailThreads),
		"files_modified", len(h.Files.Modified),
		"status", h.Status,
	)

	if err := requireGeneratorContext(ctx, "generate complete handoff"); err != nil {
		return nil, err
	}
	h.UpdateQuality(time.Now())
	return h, nil
}

// enrichWithBeads adds BV bead information to the handoff.
func (g *Generator) enrichWithBeads(ctx context.Context, h *Handoff, opts GenerateHandoffOptions) error {
	if err := requireGeneratorContext(ctx, "enrich handoff with beads"); err != nil {
		return err
	}

	// Use provided client or create default
	client := opts.BVClient
	if client == nil {
		client = bv.NewBVClientWithOptions(g.projectDir, 0, 0)
	}

	// Check if BV is available
	if !client.IsAvailableContext(ctx) {
		if err := requireGeneratorContext(ctx, "check BV availability"); err != nil {
			return err
		}
		g.logger.Debug("BV not available, skipping bead enrichment")
		return nil
	}
	if err := requireGeneratorContext(ctx, "check BV availability"); err != nil {
		return err
	}

	// Get in-progress beads using br CLI (more reliable than API for filtered queries)
	beads, err := g.getInProgressBeads(ctx, opts.AgentName)
	if err != nil {
		return fmt.Errorf("get in-progress beads: %w", err)
	}

	// Add bead IDs to handoff
	h.ActiveBeads = beads

	g.logger.Debug("enriched with beads",
		"count", len(beads),
	)

	return requireGeneratorContext(ctx, "enrich handoff with beads")
}

// getInProgressBeads queries br CLI for in-progress beads.
func (g *Generator) getInProgressBeads(ctx context.Context, agentName string) ([]string, error) {
	if err := requireGeneratorContext(ctx, "get in-progress beads"); err != nil {
		return nil, err
	}

	args := []string{"list", "--status", "in_progress", "--format", "json"}

	// If agent name provided, filter by assignee
	if agentName != "" {
		args = append(args, "--assignee", agentName)
	}

	out, err := g.runCommand(ctx, 10*time.Second, "br", args...)
	if err != nil {
		if cancelErr := generatorCancellationError(ctx, err, "get in-progress beads"); cancelErr != nil {
			return nil, cancelErr
		}
		// br not installed or no beads - not an error
		return nil, nil
	}

	// Parse JSON output. br emits an envelope object ({"issues":[...],...}),
	// not a bare array, and older/other list verbs differ — UnmarshalBdList
	// tolerates every known shape and rejects phantom zero-valued beads.
	type brBead struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	beads, err := bv.UnmarshalBdList[brBead](string(out))
	if err != nil {
		return nil, fmt.Errorf("parse br output: %w", err)
	}

	// Extract bead IDs with titles for context
	var result []string
	for _, b := range beads {
		// Format: "bd-xxxx: Title"
		entry := b.ID
		if b.Title != "" {
			entry = fmt.Sprintf("%s: %s", b.ID, truncateGen(b.Title, 60))
		}
		result = append(result, entry)
	}

	if err := requireGeneratorContext(ctx, "get in-progress beads"); err != nil {
		return nil, err
	}
	return result, nil
}

// enrichWithAgentMail adds Agent Mail information to the handoff.
func (g *Generator) enrichWithAgentMail(ctx context.Context, h *Handoff, opts GenerateHandoffOptions) error {
	if err := requireGeneratorContext(ctx, "enrich handoff with agent mail"); err != nil {
		return err
	}

	// Use provided client or create default
	client := opts.AgentMailClient
	if client == nil {
		projectKey := opts.ProjectKey
		if projectKey == "" {
			projectKey = g.projectDir
		}
		client = agentmail.NewClient(agentmail.WithProjectKey(projectKey))
	}

	// Check if Agent Mail is available
	if !client.IsAvailableContext(ctx) {
		if err := requireGeneratorContext(ctx, "check agent mail availability"); err != nil {
			return err
		}
		g.logger.Debug("Agent Mail not available, skipping enrichment")
		return nil
	}
	if err := requireGeneratorContext(ctx, "check agent mail availability"); err != nil {
		return err
	}

	projectKey := opts.ProjectKey
	if projectKey == "" {
		projectKey = g.projectDir
	}

	// Fetch inbox messages (recent threads)
	threads, err := g.fetchAgentMailThreads(ctx, client, projectKey, opts.AgentName)
	if err != nil {
		if cancelErr := generatorCancellationError(ctx, err, "fetch agent mail threads"); cancelErr != nil {
			return cancelErr
		}
		g.logger.Warn("failed to fetch Agent Mail threads", "error", err)
		// Non-fatal
	} else {
		if err := requireGeneratorContext(ctx, "store agent mail threads"); err != nil {
			return err
		}
		h.AgentMailThreads = threads
	}

	// Fetch file reservations
	reservations, err := g.fetchFileReservations(ctx, client, projectKey, opts.AgentName)
	if err != nil {
		if cancelErr := generatorCancellationError(ctx, err, "fetch file reservations"); cancelErr != nil {
			return cancelErr
		}
		g.logger.Warn("failed to fetch file reservations", "error", err)
		// Non-fatal
	} else if len(reservations) > 0 {
		if err := requireGeneratorContext(ctx, "store file reservations"); err != nil {
			return err
		}
		h.AddFinding("file_reservations", strings.Join(formatReservationSummary(reservations), "; "))
		if opts.AgentName != "" {
			h.ReservationTransfer = buildReservationTransfer(opts, projectKey, reservations)
		}
	}

	g.logger.Debug("enriched with Agent Mail",
		"threads", len(h.AgentMailThreads),
		"reservations", len(reservations),
	)

	return requireGeneratorContext(ctx, "enrich handoff with agent mail")
}

// fetchAgentMailThreads retrieves recent inbox messages for the agent.
func (g *Generator) fetchAgentMailThreads(ctx context.Context, client *agentmail.Client, projectKey, agentName string) ([]string, error) {
	if err := requireGeneratorContext(ctx, "fetch agent mail threads"); err != nil {
		return nil, err
	}
	messages, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
		ProjectKey: projectKey,
		AgentName:  agentName,
		Limit:      10, // Limit to recent messages
	})
	if err != nil {
		return nil, err
	}
	if err := requireGeneratorContext(ctx, "fetch agent mail threads"); err != nil {
		return nil, err
	}

	sort.SliceStable(messages, func(i, j int) bool {
		return messages[i].CreatedTS.After(messages[j].CreatedTS.Time)
	})

	var threads []string
	seenThreads := make(map[string]bool)

	for _, msg := range messages {
		if err := requireGeneratorContext(ctx, "format agent mail threads"); err != nil {
			return nil, err
		}
		// Format thread/message info
		var entry string
		if threadID := normalizeThreadID(msg.ThreadID); threadID != "" {
			// Skip duplicate threads
			if seenThreads[threadID] {
				continue
			}
			seenThreads[threadID] = true
			entry = fmt.Sprintf("[%s] %s (from: %s)", threadID, truncateGen(msg.Subject, 40), msg.From)
		} else {
			entry = fmt.Sprintf("%s (from: %s)", truncateGen(msg.Subject, 40), msg.From)
		}

		// Add importance marker if urgent
		if strings.EqualFold(msg.Importance, "urgent") {
			entry = "⚠️ " + entry
		}

		threads = append(threads, entry)
	}

	if err := requireGeneratorContext(ctx, "format agent mail threads"); err != nil {
		return nil, err
	}
	return threads, nil
}

func normalizeThreadID(threadID *string) string {
	if threadID == nil {
		return ""
	}
	return strings.TrimSpace(*threadID)
}

// fetchFileReservations retrieves active file reservations.
func (g *Generator) fetchFileReservations(ctx context.Context, client *agentmail.Client, projectKey, agentName string) ([]agentmail.FileReservation, error) {
	if err := requireGeneratorContext(ctx, "fetch file reservations"); err != nil {
		return nil, err
	}
	reservations, err := client.ListReservations(ctx, projectKey, agentName, false)
	if err != nil {
		return nil, err
	}
	if err := requireGeneratorContext(ctx, "fetch file reservations"); err != nil {
		return nil, err
	}
	return reservations, nil
}

func formatReservationSummary(reservations []agentmail.FileReservation) []string {
	var result []string
	now := time.Now()

	for _, r := range reservations {
		// Calculate time until expiry
		expiresIn := r.ExpiresTS.Sub(now)
		expiresStr := "expired"
		if expiresIn > 0 {
			if expiresIn > time.Hour {
				expiresStr = fmt.Sprintf("%.1fh", expiresIn.Hours())
			} else {
				expiresStr = fmt.Sprintf("%dm", int(expiresIn.Minutes()))
			}
		}

		// Format: "path (expires: Xm, exclusive)"
		entry := fmt.Sprintf("%s (expires: %s", r.PathPattern, expiresStr)
		if r.Exclusive {
			entry += ", exclusive"
		}
		entry += ")"

		result = append(result, entry)
	}

	return result
}

func buildReservationTransfer(opts GenerateHandoffOptions, projectKey string, reservations []agentmail.FileReservation) *ReservationTransfer {
	if len(reservations) == 0 || opts.AgentName == "" {
		return nil
	}
	transfer := &ReservationTransfer{
		FromAgent:          opts.AgentName,
		ProjectKey:         projectKey,
		TTLSeconds:         opts.TransferTTLSeconds,
		GracePeriodSeconds: opts.TransferGraceSeconds,
		CreatedAt:          time.Now(),
	}
	for _, r := range reservations {
		transfer.Reservations = append(transfer.Reservations, ReservationSnapshot{
			PathPattern: r.PathPattern,
			Exclusive:   r.Exclusive,
			Reason:      r.Reason,
			ExpiresAt:   r.ExpiresTS.Time,
		})
	}
	return transfer
}

// enrichWithCASS adds recent relevant CASS context snippets (with provenance) to the handoff.
func (g *Generator) enrichWithCASS(ctx context.Context, h *Handoff, opts GenerateHandoffOptions) error {
	if err := requireGeneratorContext(ctx, "enrich handoff with CASS"); err != nil {
		return err
	}
	query := buildCASSQuery(h)
	if query == "" {
		return nil
	}

	client := opts.CASSClient
	if client == nil {
		client = cass.NewClient(cass.WithTimeout(resolveCASSTimeout(opts.CASSTimeout)))
	}
	if client == nil || !client.IsInstalled() {
		if err := requireGeneratorContext(ctx, "check CASS availability"); err != nil {
			return err
		}
		g.logger.Debug("CASS not installed, skipping enrichment")
		return nil
	}

	projectKey := strings.TrimSpace(opts.ProjectKey)
	if projectKey == "" {
		projectKey = g.projectDir
	}

	limit := opts.CASSLimit
	if limit <= 0 {
		limit = defaultCASSLimit
	}
	if limit > 20 {
		limit = 20
	}

	since := strings.TrimSpace(opts.CASSSince)
	if since == "" {
		since = "30d"
	}

	resp, err := client.Search(ctx, cass.SearchOptions{
		Query:     query,
		Workspace: projectKey,
		Since:     since,
		Limit:     limit,
		Fields:    "summary",
	})
	if err != nil {
		if cancelErr := generatorCancellationError(ctx, err, "search CASS"); cancelErr != nil {
			return cancelErr
		}
		if errors.Is(err, cass.ErrNotInstalled) || errors.Is(err, cass.ErrNotInitialized) {
			g.logger.Debug("CASS unavailable or uninitialized, skipping enrichment", "error", err)
			return nil
		}
		return err
	}
	if err := requireGeneratorContext(ctx, "search CASS"); err != nil {
		return err
	}
	if resp == nil || len(resp.Hits) == 0 {
		return nil
	}

	entries := buildCASSMemoryEntries(resp.Hits, limit)
	if len(entries) == 0 {
		return nil
	}
	if err := requireGeneratorContext(ctx, "store CASS results"); err != nil {
		return err
	}

	h.CMMemories = uniqueStrings(append(h.CMMemories, entries...))
	h.AddFinding("cass_query", truncateGen(query, 120))
	h.AddFinding("cass_hit_count", fmt.Sprintf("%d", len(entries)))

	g.logger.Debug("enriched with CASS context",
		"query", truncateGen(query, 80),
		"entries", len(entries),
	)

	return requireGeneratorContext(ctx, "enrich handoff with CASS")
}

func resolveCASSTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultCASSTimeout
	}
	return timeout
}

func buildCASSQuery(h *Handoff) string {
	if h == nil {
		return ""
	}
	parts := make([]string, 0, 4)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		parts = append(parts, value)
	}

	add(h.Now)
	add(h.Goal)
	if len(h.Next) > 0 {
		add(h.Next[0])
	}
	if len(h.ActiveBeads) > 0 {
		bead := strings.TrimSpace(h.ActiveBeads[0])
		if idx := strings.Index(bead, ":"); idx > 0 {
			bead = strings.TrimSpace(bead[:idx])
		}
		add(bead)
	}

	query := strings.Join(parts, " ")
	query = strings.Join(strings.Fields(query), " ")
	return truncateGen(query, 240)
}

func buildCASSMemoryEntries(hits []cass.SearchHit, limit int) []string {
	if len(hits) == 0 || limit == 0 {
		return nil
	}
	if limit < 0 {
		limit = 0
	}

	ordered := append([]cass.SearchHit(nil), hits...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Score != ordered[j].Score {
			return ordered[i].Score > ordered[j].Score
		}
		iTime := ordered[i].CreatedAtTime()
		jTime := ordered[j].CreatedAtTime()
		if !iTime.Equal(jTime) {
			return iTime.After(jTime)
		}
		if ordered[i].SourcePath != ordered[j].SourcePath {
			return ordered[i].SourcePath < ordered[j].SourcePath
		}
		iLine := 0
		jLine := 0
		if ordered[i].LineNumber != nil {
			iLine = *ordered[i].LineNumber
		}
		if ordered[j].LineNumber != nil {
			jLine = *ordered[j].LineNumber
		}
		if iLine != jLine {
			return iLine < jLine
		}
		return ordered[i].SessionID < ordered[j].SessionID
	})

	seen := make(map[string]struct{}, len(ordered))
	out := make([]string, 0, min(limit, len(ordered)))
	for _, hit := range ordered {
		loc := strings.TrimSpace(hit.SourcePath)
		if loc == "" {
			continue
		}
		if hit.LineNumber != nil && *hit.LineNumber > 0 {
			loc = fmt.Sprintf("%s#L%d", loc, *hit.LineNumber)
		}
		content := strings.TrimSpace(hit.Snippet)
		if content == "" {
			content = strings.TrimSpace(hit.Content)
		}
		content = strings.Join(strings.Fields(content), " ")
		key := strings.Join([]string{
			loc,
			strings.TrimSpace(hit.SessionID),
			strings.TrimSpace(hit.Agent),
			content,
		}, "|")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		meta := make([]string, 0, 3)
		if agent := strings.TrimSpace(hit.Agent); agent != "" {
			meta = append(meta, "agent="+agent)
		}
		if session := strings.TrimSpace(hit.SessionID); session != "" {
			meta = append(meta, "session="+session)
		}
		if hit.Score > 0 {
			meta = append(meta, fmt.Sprintf("score=%.2f", hit.Score))
		}

		entry := "cass:" + loc
		if len(meta) > 0 {
			entry += " [" + strings.Join(meta, ", ") + "]"
		}
		if content != "" {
			entry += " " + truncateGen(content, 140)
		}

		out = append(out, entry)
		if len(out) >= limit {
			break
		}
	}

	return out
}

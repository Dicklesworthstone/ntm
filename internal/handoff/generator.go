package handoff

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/bv"
)

// Generator creates handoff content from various sources.
type Generator struct {
	projectDir string
	logger     *slog.Logger
}

// NewGenerator creates a Generator for the given project directory.
func NewGenerator(projectDir string) *Generator {
	return &Generator{
		projectDir: projectDir,
		logger:     slog.Default().With("component", "handoff.generator"),
	}
}

// NewGeneratorWithLogger creates a Generator with a custom logger.
func NewGeneratorWithLogger(projectDir string, logger *slog.Logger) *Generator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Generator{
		projectDir: projectDir,
		logger:     logger.With("component", "handoff.generator"),
	}
}

// GenerateFromOutput creates a handoff by analyzing agent output text.
func (g *Generator) GenerateFromOutput(sessionName string, output []byte) (*Handoff, error) {
	g.logger.Debug("generating handoff from output",
		"session", sessionName,
		"output_size", len(output),
	)

	h := New(sessionName)

	analysis := g.analyzeOutput(output)

	// Map analysis to handoff fields
	h.Goal = analysis.accomplishment
	h.Now = analysis.nextStep
	h.DoneThisSession = analysis.tasks
	h.Blockers = analysis.blockers
	h.Decisions = analysis.decisions
	h.Next = analysis.todos

	// Infer status based on analysis results
	if len(analysis.blockers) > 0 {
		h.Status = StatusBlocked
		h.Outcome = OutcomePartialMinus
	} else if analysis.accomplishment != "" {
		h.Status = StatusComplete
		h.Outcome = OutcomeSucceeded
	} else {
		h.Status = StatusPartial
		h.Outcome = OutcomePartialPlus
	}

	// Enrich with git state
	if err := g.EnrichWithGitState(h); err != nil {
		g.logger.Warn("git enrichment failed", "error", err)
		// Non-fatal - continue without git info
	}

	g.logger.Info("generated handoff from output",
		"session", sessionName,
		"goal_len", len(h.Goal),
		"now_len", len(h.Now),
		"task_count", len(h.DoneThisSession),
		"blocker_count", len(h.Blockers),
	)

	return h, nil
}

// GenerateFromTranscript creates handoff from Claude Code transcript.
// Transcript path: ~/.claude/projects/.../session.jsonl
func (g *Generator) GenerateFromTranscript(sessionName, transcriptPath string) (*Handoff, error) {
	g.logger.Debug("generating handoff from transcript",
		"session", sessionName,
		"path", transcriptPath,
	)

	h := New(sessionName)

	file, err := os.Open(transcriptPath)
	if err != nil {
		g.logger.Error("failed to open transcript",
			"path", transcriptPath,
			"error", err,
		)
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer file.Close()

	var (
		toolCalls     []string
		lastAssistant string
		errors        []string
		filesModified []string
	)

	scanner := bufio.NewScanner(file)
	// Handle large lines - up to 10MB per line
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		var entry map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue // Skip malformed lines
		}

		// Extract tool calls
		if tools, ok := entry["tool_calls"].([]interface{}); ok {
			for _, t := range tools {
				if tm, ok := t.(map[string]interface{}); ok {
					if name, ok := tm["name"].(string); ok {
						toolCalls = append(toolCalls, name)
					}
					// Track file modifications from Edit and Write tools
					if name, _ := tm["name"].(string); name == "Edit" || name == "Write" {
						if args, ok := tm["arguments"].(map[string]interface{}); ok {
							if path, ok := args["file_path"].(string); ok {
								filesModified = append(filesModified, path)
							}
						}
					}
				}
			}
		}

		// Extract assistant messages - keep last one for analysis
		if role, _ := entry["role"].(string); role == "assistant" {
			if content, ok := entry["content"].(string); ok {
				lastAssistant = content
			}
		}

		// Extract errors from any error field
		if errStr, ok := entry["error"].(string); ok {
			errors = append(errors, errStr)
		}
	}

	if err := scanner.Err(); err != nil {
		g.logger.Error("failed to scan transcript",
			"path", transcriptPath,
			"error", err,
		)
		return nil, fmt.Errorf("scan transcript: %w", err)
	}

	// Analyze last assistant message for goal/now/todos
	if lastAssistant != "" {
		analysis := g.analyzeOutput([]byte(lastAssistant))
		h.Goal = analysis.accomplishment
		h.Now = analysis.nextStep
		h.Next = analysis.todos
		h.Decisions = analysis.decisions
	}

	// Track files from tool calls
	h.Files.Modified = uniqueStrings(filesModified)

	// Track blockers from errors - keep top 3
	if len(errors) > 0 {
		limit := len(errors)
		if limit > 3 {
			limit = 3
		}
		h.Blockers = errors[:limit]
		h.Status = StatusBlocked
		h.Outcome = OutcomePartialMinus
	}

	// Set status if not already blocked
	if h.Status == "" {
		if h.Goal != "" {
			h.Status = StatusComplete
			h.Outcome = OutcomeSucceeded
		} else {
			h.Status = StatusPartial
			h.Outcome = OutcomePartialPlus
		}
	}

	// Log tool usage summary
	toolSummary := summarizeToolCalls(toolCalls)

	g.logger.Info("generated handoff from transcript",
		"session", sessionName,
		"tool_calls", len(toolCalls),
		"tool_summary", toolSummary,
		"files_modified", len(filesModified),
		"errors", len(errors),
	)

	// Enrich with git state
	if err := g.EnrichWithGitState(h); err != nil {
		g.logger.Warn("git enrichment failed", "error", err)
	}

	return h, nil
}

// EnrichWithGitState adds git information to handoff.
func (g *Generator) EnrichWithGitState(h *Handoff) error {
	g.logger.Debug("enriching handoff with git state")

	// Get modified files from git diff
	modified, err := g.getGitModified()
	if err != nil {
		return fmt.Errorf("git modified: %w", err)
	}
	// Merge with existing, don't overwrite
	h.Files.Modified = uniqueStrings(append(h.Files.Modified, modified...))

	// Get new files from git status
	created, err := g.getGitUntracked()
	if err != nil {
		return fmt.Errorf("git untracked: %w", err)
	}
	h.Files.Created = uniqueStrings(append(h.Files.Created, created...))

	// Get current branch for context
	branch, _ := g.getGitBranch()
	if branch != "" {
		h.AddFinding("git_branch", branch)
	}

	// Get recent commits (session could have made commits)
	commits, _ := g.getRecentCommits(5)
	if len(commits) > 0 {
		h.AddFinding("recent_commits", strings.Join(commits, "; "))
	}

	g.logger.Debug("enriched with git state",
		"modified", len(h.Files.Modified),
		"created", len(h.Files.Created),
		"branch", branch,
	)

	return nil
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

func (g *Generator) getGitModified() ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only", "HEAD")
	cmd.Dir = g.projectDir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseLines(out), nil
}

func (g *Generator) getGitUntracked() ([]string, error) {
	cmd := exec.Command("git", "ls-files", "--others", "--exclude-standard")
	cmd.Dir = g.projectDir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseLines(out), nil
}

func (g *Generator) getGitBranch() (string, error) {
	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = g.projectDir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (g *Generator) getRecentCommits(n int) ([]string, error) {
	cmd := exec.Command("git", "log", fmt.Sprintf("-%d", n), "--oneline")
	cmd.Dir = g.projectDir
	out, err := cmd.Output()
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

// truncateGen truncates a string to max length (named to avoid conflict with validate.go truncate).
func truncateGen(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func summarizeToolCalls(calls []string) string {
	counts := make(map[string]int)
	for _, c := range calls {
		counts[c]++
	}
	var parts []string
	for tool, count := range counts {
		parts = append(parts, fmt.Sprintf("%s:%d", tool, count))
	}
	return strings.Join(parts, ",")
}

// ProjectDir returns the project directory for this generator.
func (g *Generator) ProjectDir() string {
	return g.projectDir
}

// GenerateAutoHandoff creates an auto-generated handoff suitable for pre-compact hooks.
// It combines output analysis with git state for a complete picture.
func (g *Generator) GenerateAutoHandoff(sessionName, agentType, paneID string, output []byte, tokensUsed, tokensMax int) (*Handoff, error) {
	g.logger.Debug("generating auto-handoff",
		"session", sessionName,
		"agent_type", agentType,
		"pane_id", paneID,
		"output_size", len(output),
		"tokens_used", tokensUsed,
		"tokens_max", tokensMax,
	)

	h, err := g.GenerateFromOutput(sessionName, output)
	if err != nil {
		return nil, fmt.Errorf("generate from output: %w", err)
	}

	// Set agent info
	h.SetAgentInfo("", agentType, paneID)

	// Set token info
	h.SetTokenInfo(tokensUsed, tokensMax)

	// Set creation timestamp
	h.CreatedAt = time.Now()
	h.UpdatedAt = h.CreatedAt

	g.logger.Info("generated auto-handoff",
		"session", sessionName,
		"agent_type", agentType,
		"tokens_pct", h.TokensPct,
		"goal", truncateGen(h.Goal, 50),
	)

	return h, nil
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

	// BVClient is an optional pre-configured BV client
	BVClient *bv.BVClient

	// AgentMailClient is an optional pre-configured Agent Mail client
	AgentMailClient *agentmail.Client

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
//
// All integrations are optional and fail gracefully if unavailable.
func (g *Generator) GenerateHandoff(ctx context.Context, opts GenerateHandoffOptions) (*Handoff, error) {
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

	// Enrich with git state
	if err := g.EnrichWithGitState(h); err != nil {
		g.logger.Warn("git enrichment failed", "error", err)
		// Non-fatal - continue without git info
	}

	// Enrich with BV beads
	includeBeads := opts.IncludeBeads == nil || *opts.IncludeBeads
	if includeBeads {
		if err := g.enrichWithBeads(ctx, h, opts); err != nil {
			g.logger.Warn("BV enrichment failed", "error", err)
			// Non-fatal - continue without bead info
		}
	}

	// Enrich with Agent Mail
	includeAgentMail := opts.IncludeAgentMail == nil || *opts.IncludeAgentMail
	if includeAgentMail && opts.AgentName != "" {
		if err := g.enrichWithAgentMail(ctx, h, opts); err != nil {
			g.logger.Warn("Agent Mail enrichment failed", "error", err)
			// Non-fatal - continue without Agent Mail info
		}
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

	return h, nil
}

// enrichWithBeads adds BV bead information to the handoff.
func (g *Generator) enrichWithBeads(ctx context.Context, h *Handoff, opts GenerateHandoffOptions) error {
	// Use provided client or create default
	client := opts.BVClient
	if client == nil {
		client = bv.NewBVClientWithOptions(g.projectDir, 0, 0)
	}

	// Check if BV is available
	if !client.IsAvailable() {
		g.logger.Debug("BV not available, skipping bead enrichment")
		return nil
	}

	// Get in-progress beads using br CLI (more reliable than API for filtered queries)
	beads, err := g.getInProgressBeads(opts.AgentName)
	if err != nil {
		return fmt.Errorf("get in-progress beads: %w", err)
	}

	// Add bead IDs to handoff
	h.ActiveBeads = beads

	g.logger.Debug("enriched with beads",
		"count", len(beads),
	)

	return nil
}

// getInProgressBeads queries br CLI for in-progress beads.
func (g *Generator) getInProgressBeads(agentName string) ([]string, error) {
	args := []string{"list", "--status", "in_progress", "--format", "json"}

	// If agent name provided, filter by assignee
	if agentName != "" {
		args = append(args, "--assignee", agentName)
	}

	cmd := exec.Command("br", args...)
	cmd.Dir = g.projectDir

	out, err := cmd.Output()
	if err != nil {
		// br not installed or no beads - not an error
		return nil, nil
	}

	// Parse JSON output
	var beads []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}

	if err := json.Unmarshal(out, &beads); err != nil {
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

	return result, nil
}

// enrichWithAgentMail adds Agent Mail information to the handoff.
func (g *Generator) enrichWithAgentMail(ctx context.Context, h *Handoff, opts GenerateHandoffOptions) error {
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
	if !client.IsAvailable() {
		g.logger.Debug("Agent Mail not available, skipping enrichment")
		return nil
	}

	projectKey := opts.ProjectKey
	if projectKey == "" {
		projectKey = g.projectDir
	}

	// Fetch inbox messages (recent threads)
	threads, err := g.fetchAgentMailThreads(ctx, client, projectKey, opts.AgentName)
	if err != nil {
		g.logger.Warn("failed to fetch Agent Mail threads", "error", err)
		// Non-fatal
	} else {
		h.AgentMailThreads = threads
	}

	// Fetch file reservations
	reservations, err := g.fetchFileReservations(ctx, client, projectKey, opts.AgentName)
	if err != nil {
		g.logger.Warn("failed to fetch file reservations", "error", err)
		// Non-fatal
	} else if len(reservations) > 0 {
		h.AddFinding("file_reservations", strings.Join(formatReservationSummary(reservations), "; "))
		if opts.AgentName != "" {
			h.ReservationTransfer = buildReservationTransfer(opts, projectKey, reservations)
		}
	}

	g.logger.Debug("enriched with Agent Mail",
		"threads", len(h.AgentMailThreads),
		"reservations", len(reservations),
	)

	return nil
}

// fetchAgentMailThreads retrieves recent inbox messages for the agent.
func (g *Generator) fetchAgentMailThreads(ctx context.Context, client *agentmail.Client, projectKey, agentName string) ([]string, error) {
	messages, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
		ProjectKey: projectKey,
		AgentName:  agentName,
		Limit:      10, // Limit to recent messages
	})
	if err != nil {
		return nil, err
	}

	var threads []string
	seenThreads := make(map[string]bool)

	for _, msg := range messages {
		// Format thread/message info
		var entry string
		if msg.ThreadID != nil && *msg.ThreadID != "" {
			// Skip duplicate threads
			if seenThreads[*msg.ThreadID] {
				continue
			}
			seenThreads[*msg.ThreadID] = true
			entry = fmt.Sprintf("[%s] %s (from: %s)", *msg.ThreadID, truncateGen(msg.Subject, 40), msg.From)
		} else {
			entry = fmt.Sprintf("%s (from: %s)", truncateGen(msg.Subject, 40), msg.From)
		}

		// Add importance marker if urgent
		if msg.Importance == "urgent" {
			entry = "⚠️ " + entry
		}

		threads = append(threads, entry)
	}

	return threads, nil
}

// fetchFileReservations retrieves active file reservations.
func (g *Generator) fetchFileReservations(ctx context.Context, client *agentmail.Client, projectKey, agentName string) ([]agentmail.FileReservation, error) {
	reservations, err := client.ListReservations(ctx, projectKey, agentName, false)
	if err != nil {
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

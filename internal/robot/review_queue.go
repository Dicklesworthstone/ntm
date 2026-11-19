package robot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const (
	reviewQueueSchemaID      = "ntm:robot:review-queue:v1"
	reviewQueueSchemaVersion = "1.0.0"
)

var (
	reviewQueueSessionExists       = tmux.SessionExists
	reviewQueueGetPanes            = tmux.GetPanes
	reviewQueueLoadAssignmentStore = assignment.LoadStore
)

// ReviewSuggestion represents a single review prompt suggestion for an idle agent.
type ReviewSuggestion struct {
	Agent     string `json:"agent"`
	Pane      int    `json:"pane"`
	AgentType string `json:"agent_type"`
	Prompt    string `json:"prompt"`
	Source    string `json:"source"` // "agent_work", "git_commit", or "idle_available"
	SourceRef string `json:"source_ref,omitempty"`
}

// IdleAgent represents an agent detected as idle.
type IdleAgent struct {
	Pane         int    `json:"pane"`
	AgentType    string `json:"agent_type"`
	AgentName    string `json:"agent_name,omitempty"`
	IdleDuration string `json:"idle_duration,omitempty"`
	LastTask     string `json:"last_task,omitempty"`
}

// ReviewPromptSender delivers a review prompt to a tmux target pane.
type ReviewPromptSender func(target, prompt string, enter bool) error

// ReviewQueueOptions configures ORI review-queue inspection and optional dispatch.
type ReviewQueueOptions struct {
	Session          string
	ProjectDir       string
	Filter           string
	IdleThreshold    time.Duration
	IdleThresholdSet bool
	CommitLimit      int
	CommitLimitSet   bool
	Dispatch         bool
	Sender           ReviewPromptSender
}

// ReviewQueueOutput represents the structured JSON envelope for --robot-review-queue.
type ReviewQueueOutput struct {
	RobotResponse
	SchemaID      string             `json:"schema_id"`
	SchemaVersion string             `json:"schema_version"`
	Session       string             `json:"session"`
	ProjectDir    string             `json:"project_dir,omitempty"`
	IdleAgents    []IdleAgent        `json:"idle_agents"`
	Suggestions   []ReviewSuggestion `json:"suggestions"`
	Dispatched    bool               `json:"dispatched,omitempty"`
	Sent          int                `json:"sent,omitempty"`
	Skipped       int                `json:"skipped,omitempty"`
	AgentHints    *AgentHints        `json:"_agent_hints,omitempty"`
}

func newReviewQueueErrorOutput(session, projectDir string, err error, code, hint string) *ReviewQueueOutput {
	return &ReviewQueueOutput{
		RobotResponse: NewErrorResponse(err, code, hint),
		SchemaID:      reviewQueueSchemaID,
		SchemaVersion: reviewQueueSchemaVersion,
		Session:       strings.TrimSpace(session),
		ProjectDir:    strings.TrimSpace(projectDir),
		IdleAgents:    []IdleAgent{},
		Suggestions:   []ReviewSuggestion{},
	}
}

// GetReviewQueue detects idle agents in a session, generates review prompt
// suggestions from completed beads and git commits, and optionally dispatches them.
func GetReviewQueue(ctx context.Context, opts ReviewQueueOptions) (*ReviewQueueOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return newReviewQueueErrorOutput(opts.Session, opts.ProjectDir, err, ErrCodeTimeout, "Retry --robot-review-queue with an active context"), nil
	}

	session := strings.TrimSpace(opts.Session)
	if session == "" {
		return newReviewQueueErrorOutput(opts.Session, opts.ProjectDir, errors.New("session is required"), ErrCodeInvalidFlag, "Pass --robot-review-queue=SESSION"), nil
	}
	if opts.IdleThreshold < 0 {
		return newReviewQueueErrorOutput(session, opts.ProjectDir, fmt.Errorf("invalid --review-idle-threshold %s: cannot be negative", opts.IdleThreshold), ErrCodeInvalidFlag, "Use a non-negative duration such as 2m or 0s"), nil
	}
	if opts.CommitLimit < 0 {
		return newReviewQueueErrorOutput(session, opts.ProjectDir, fmt.Errorf("invalid --review-commits %d: cannot be negative", opts.CommitLimit), ErrCodeInvalidFlag, "Use a non-negative commit limit such as 5"), nil
	}

	normalizedFilter, err := NormalizeReviewQueueFilter(opts.Filter)
	if err != nil {
		return newReviewQueueErrorOutput(session, opts.ProjectDir, err, ErrCodeInvalidFlag, "Filter with --type=cc|cod|gmi|agy|grok|omp|oc|cursor|windsurf|aider|ollama"), nil
	}

	if !reviewQueueSessionExists(session) {
		return newReviewQueueErrorOutput(session, opts.ProjectDir, fmt.Errorf("session '%s' does not exist", session), ErrCodeSessionNotFound, "Use 'ntm list' or 'ntm --robot-status' to see available sessions"), nil
	}

	idleThreshold := opts.IdleThreshold
	if !opts.IdleThresholdSet && idleThreshold == 0 {
		idleThreshold = 2 * time.Minute
	}
	commitLimit := opts.CommitLimit
	if !opts.CommitLimitSet && commitLimit == 0 {
		commitLimit = 5
	}

	store, err := reviewQueueLoadAssignmentStore(session)
	if err != nil {
		return newReviewQueueErrorOutput(session, opts.ProjectDir, fmt.Errorf("failed to load assignments: %w", err), ErrCodeInternalError, "Inspect session assignment store permissions"), nil
	}

	panes, err := reviewQueueGetPanes(session)
	if err != nil {
		return newReviewQueueErrorOutput(session, opts.ProjectDir, fmt.Errorf("failed to list panes: %w", err), ErrCodeInternalError, "Check tmux session health"), nil
	}
	if len(panes) == 0 {
		return newReviewQueueErrorOutput(session, opts.ProjectDir, fmt.Errorf("no panes found in session %s", session), ErrCodePaneNotFound, "Spawn agent panes before querying the review queue"), nil
	}

	idleAgents := DetectIdleAgents(store, panes, normalizedFilter, idleThreshold)
	suggestions := GenerateReviewSuggestions(store, idleAgents, opts.ProjectDir, commitLimit)
	if idleAgents == nil {
		idleAgents = []IdleAgent{}
	}
	if suggestions == nil {
		suggestions = []ReviewSuggestion{}
	}

	out := &ReviewQueueOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      reviewQueueSchemaID,
		SchemaVersion: reviewQueueSchemaVersion,
		Session:       session,
		ProjectDir:    strings.TrimSpace(opts.ProjectDir),
		IdleAgents:    idleAgents,
		Suggestions:   suggestions,
	}

	var hintWarnings []string
	if opts.Dispatch && len(suggestions) > 0 {
		if err := ValidateReviewPromptDelivery(suggestions); err != nil {
			code := ErrCodeInvalidFlag
			if errors.Is(err, agent.ErrAutomatedPromptDeliveryNotImplemented) {
				code = ErrCodeNotImplemented
			}
			return newReviewQueueErrorOutput(session, opts.ProjectDir, err, code, "Filter to harnesses that support automated prompt delivery"), nil
		}
		var sent, skipped int
		if opts.Sender != nil {
			sent, skipped, err = SendReviewPromptsWithSender(session, suggestions, opts.Sender)
		} else {
			sent, skipped, err = SendReviewPrompts(session, suggestions)
		}
		if err != nil {
			return newReviewQueueErrorOutput(session, opts.ProjectDir, err, ErrCodeInternalError, "Verify tmux pane readiness before dispatching review prompts"), nil
		}
		out.Dispatched = true
		out.Sent = sent
		out.Skipped = skipped
		if skipped > 0 {
			hintWarnings = append(hintWarnings, fmt.Sprintf("%d review prompt(s) failed delivery and were skipped", skipped))
		}
	}

	summary := fmt.Sprintf("%d idle agent(s), %d review suggestion(s)", len(idleAgents), len(suggestions))
	if out.Dispatched {
		summary = fmt.Sprintf("Dispatched %d review prompt(s) (%d skipped) across %d idle agent(s)", out.Sent, out.Skipped, len(idleAgents))
	}
	var actions []RobotAction
	if len(suggestions) > 0 && !out.Dispatched {
		actions = append(actions, RobotAction{
			Action:   "dispatch_reviews",
			Target:   session,
			Reason:   fmt.Sprintf("%d review prompt(s) ready for %d idle agent(s)", len(suggestions), len(idleAgents)),
			Priority: 1,
			Details:  fmt.Sprintf("ntm --robot-review-queue=%s --review-dispatch", session),
		})
	}
	out.AgentHints = &AgentHints{
		Summary:          summary,
		Warnings:         hintWarnings,
		SuggestedActions: actions,
	}
	return out, nil
}

// PrintReviewQueue executes GetReviewQueue and writes the terminal JSON/TOON response.
func PrintReviewQueue(ctx context.Context, opts ReviewQueueOptions) error {
	out, err := GetReviewQueue(ctx, opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot review-queue failed")
}

// DetectIdleAgents finds agent panes with no active assignments meeting idleThreshold.
func DetectIdleAgents(store *assignment.AssignmentStore, panes []tmux.Pane, filter string, idleThreshold time.Duration) []IdleAgent {
	busyPanes := make(map[int]bool)
	var allAssignments []assignment.Assignment
	if store != nil {
		for _, a := range store.ListActive() {
			busyPanes[a.Pane] = true
		}
		allAssignments = store.GetAll()
	}

	lastTaskByPane := make(map[int]string)
	lastTimeByPane := make(map[int]time.Time)
	for _, a := range allAssignments {
		if a.Status != assignment.StatusCompleted && a.Status != assignment.StatusFailed {
			continue
		}
		var ts time.Time
		if a.CompletedAt != nil {
			ts = *a.CompletedAt
		} else if a.FailedAt != nil {
			ts = *a.FailedAt
		}
		if ts.After(lastTimeByPane[a.Pane]) {
			lastTimeByPane[a.Pane] = ts
			lastTaskByPane[a.Pane] = a.BeadTitle
		}
	}

	var idle []IdleAgent
	for _, pane := range panes {
		if busyPanes[pane.Index] {
			continue
		}

		agentType := reviewQueueAgentTypeForPane(pane)
		if pane.Type.Canonical() == tmux.AgentGrok {
			agentType = tmux.AgentGrok.String()
		}
		if agentType == "" || agentType == "user" || agentType == "unknown" {
			continue
		}

		if filter != "" && !MatchesReviewQueueFilter(agentType, filter) {
			continue
		}

		idleDuration := ""
		if lastTime, ok := lastTimeByPane[pane.Index]; ok {
			dur := time.Since(lastTime)
			if dur < idleThreshold {
				continue
			}
			idleDuration = formatReviewIdleDuration(dur)
		}

		idle = append(idle, IdleAgent{
			Pane:         pane.Index,
			AgentType:    agentType,
			IdleDuration: idleDuration,
			LastTask:     lastTaskByPane[pane.Index],
		})
	}

	sort.Slice(idle, func(i, j int) bool {
		return idle[i].Pane < idle[j].Pane
	})

	return idle
}

func reviewQueueAgentTypeForPane(pane tmux.Pane) string {
	if canonical := pane.Type.Canonical(); canonical != "" && canonical != tmux.AgentUnknown {
		if detected := agentTypeString(canonical); detected != "" && detected != "unknown" {
			return detected
		}
	}
	if agent.IsPluginType(agent.AgentType(pane.Type)) {
		return strings.ToLower(strings.TrimSpace(string(pane.Type)))
	}
	if detected := detectAgentTypeFromTitle(pane.Title); detected != "" {
		return detected
	}
	return "unknown"
}

func formatReviewIdleDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// GenerateReviewSuggestions creates review prompts from completed assignments and recent git commits.
func GenerateReviewSuggestions(store *assignment.AssignmentStore, idleAgents []IdleAgent, projectDir string, commitLimit int) []ReviewSuggestion {
	if len(idleAgents) == 0 {
		return nil
	}

	var suggestions []ReviewSuggestion
	if store != nil {
		for _, a := range store.ListByStatus(assignment.StatusCompleted) {
			suggestions = append(suggestions, ReviewSuggestion{
				Prompt:    fmt.Sprintf("Review the changes for bead %s: %q", a.BeadID, a.BeadTitle),
				Source:    "agent_work",
				SourceRef: a.BeadID,
			})
		}
	}

	commits := GetRecentGitCommits(projectDir, commitLimit)
	for _, c := range commits {
		suggestions = append(suggestions, ReviewSuggestion{
			Prompt:    fmt.Sprintf("Review commit %s: %q", c.Hash[:MinInt(8, len(c.Hash))], c.Subject),
			Source:    "git_commit",
			SourceRef: c.Hash,
		})
	}

	return AssignSuggestionsToAgents(suggestions, idleAgents)
}

// AssignSuggestionsToAgents distributes review suggestions across idle agents round-robin.
func AssignSuggestionsToAgents(suggestions []ReviewSuggestion, idleAgents []IdleAgent) []ReviewSuggestion {
	if len(suggestions) == 0 || len(idleAgents) == 0 {
		return nil
	}

	result := make([]ReviewSuggestion, 0, len(suggestions))
	for i, s := range suggestions {
		idleAgent := idleAgents[i%len(idleAgents)]
		s.Agent = AgentLabel(idleAgent)
		s.Pane = idleAgent.Pane
		s.AgentType = idleAgent.AgentType
		result = append(result, s)
	}
	return result
}

// AgentLabel formats the display label for an idle agent.
func AgentLabel(a IdleAgent) string {
	if a.AgentName != "" {
		return a.AgentName
	}
	return fmt.Sprintf("%s_%d", a.AgentType, a.Pane)
}

// RecentGitCommit holds the hash and subject of a recent git commit.
type RecentGitCommit struct {
	Hash    string
	Subject string
}

// GetRecentGitCommits returns up to limit recent commits from projectDir (or cwd when empty).
func GetRecentGitCommits(projectDir string, limit int) []RecentGitCommit {
	if limit <= 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "log", fmt.Sprintf("-%d", limit), "--pretty=format:%H|%s")
	if trimmedDir := strings.TrimSpace(projectDir); trimmedDir != "" {
		cmd.Dir = trimmedDir
	}
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if err != nil {
		slog.Debug("review-queue: git log failed", "project_dir", projectDir, "error", err)
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var commits []RecentGitCommit
	for _, line := range lines {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 || parts[0] == "" {
			continue
		}
		commits = append(commits, RecentGitCommit{Hash: parts[0], Subject: parts[1]})
	}
	return commits
}

// NormalizeReviewQueueFilter normalizes an agent type alias filter to its canonical code.
func NormalizeReviewQueueFilter(filter string) (string, error) {
	trimmed := strings.TrimSpace(filter)
	if trimmed == "" {
		return "", nil
	}
	normalized := normalizeReviewAgentTypeLike(trimmed)
	if normalized == "" {
		return "", fmt.Errorf("invalid agent filter %q: must be one of claude|cc, codex|cod, gemini|gmi, antigravity|agy, grok, omp, oc, cursor, windsurf|ws, aider, ollama", filter)
	}
	return normalized, nil
}

// MatchesReviewQueueFilter reports whether agentType matches filter.
func MatchesReviewQueueFilter(agentType, filter string) bool {
	normalizedFilter, err := NormalizeReviewQueueFilter(filter)
	if err != nil {
		return false
	}
	if normalizedFilter == "" {
		return true
	}
	return normalizeReviewAgentTypeLike(agentType) == normalizedFilter
}

func normalizeReviewAgentTypeLike(value string) string {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" {
		return ""
	}
	if canonical := agent.AgentType(trimmed).Canonical(); isSupportedReviewWorkAgentType(canonical) {
		return string(canonical)
	}
	if head, _, ok := strings.Cut(trimmed, "_"); ok {
		if canonical := agent.AgentType(head).Canonical(); isSupportedReviewWorkAgentType(canonical) {
			return string(canonical)
		}
	}
	return ""
}

func isSupportedReviewWorkAgentType(agentType agent.AgentType) bool {
	switch agentType {
	case agent.AgentTypeClaudeCode, agent.AgentTypeCodex, agent.AgentTypeGemini,
		agent.AgentTypeAntigravity, agent.AgentTypeGrok, agent.AgentTypeCursor,
		agent.AgentTypeWindsurf, agent.AgentTypeAider, agent.AgentTypeOpencode,
		agent.AgentTypeOmp, agent.AgentTypeOllama:
		return true
	default:
		return false
	}
}

// MinInt returns the smaller of a and b.
func MinInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ValidateReviewPromptDelivery verifies that every suggestion's harness supports automated prompt delivery.
func ValidateReviewPromptDelivery(suggestions []ReviewSuggestion) error {
	for _, suggestion := range suggestions {
		if err := agent.AgentType(suggestion.AgentType).ValidateAutomatedPromptDelivery(); err != nil {
			return fmt.Errorf("review prompt for pane %d: %w", suggestion.Pane, err)
		}
	}
	return nil
}

// SendReviewPrompts dispatches review suggestions using tmux.SendKeys.
func SendReviewPrompts(session string, suggestions []ReviewSuggestion) (sent, skipped int, err error) {
	return SendReviewPromptsWithSender(session, suggestions, tmux.SendKeys)
}

// SendReviewPromptsWithSender dispatches review suggestions using a custom sender.
func SendReviewPromptsWithSender(session string, suggestions []ReviewSuggestion, sender ReviewPromptSender) (sent, skipped int, err error) {
	if err := ValidateReviewPromptDelivery(suggestions); err != nil {
		return 0, 0, err
	}
	if sender == nil {
		return 0, 0, fmt.Errorf("review prompt sender is required")
	}
	for _, s := range suggestions {
		target := fmt.Sprintf("%s:.%d", session, s.Pane)
		slog.Info("[E2E-REVIEWQ] send",
			"session", session,
			"agent", s.Agent,
			"pane", s.Pane,
			"source", s.Source)
		if err := sender(target, s.Prompt, true); err != nil {
			slog.Warn("[E2E-REVIEWQ] send_failed",
				"session", session,
				"pane", s.Pane,
				"error", err)
			skipped++
		} else {
			sent++
		}
	}
	return sent, skipped, nil
}

func reviewQueueCapabilityCommand() RobotCommandInfo {
	return RobotCommandInfo{
		Name:        "review-queue",
		Flag:        "--robot-review-queue",
		Category:    "control",
		Summary:     "Generate or dispatch peer review queue.",
		Description: "Detect idle agents in a session, generate peer-review prompts from completed beads and recent git commits, and optionally dispatch them.",
		Parameters: []RobotParameter{
			{Name: "session", Flag: "--robot-review-queue", Type: "string", Required: true, Description: "Session name"},
			{Name: "review-dispatch", Flag: "--review-dispatch", Type: "bool", Required: false, Default: "false", Description: "Automatically dispatch generated review prompts to idle agents"},
			{Name: "review-idle-threshold", Flag: "--review-idle-threshold", Type: "duration", Required: false, Default: "2m0s", Description: "Minimum idle duration to classify an agent as idle"},
			{Name: "review-commits", Flag: "--review-commits", Type: "int", Required: false, Default: "5", Description: "Number of recent git commits to include as review sources"},
			{Name: "type", Flag: "--type", Type: "string", Required: false, Description: "Filter idle agents by harness type"},
			{Name: "project", Flag: "--project", Type: "string", Required: false, Description: "Project directory override for git commit discovery"},
		},
		Examples: []string{
			"ntm --robot-review-queue=myproject",
			"ntm --robot-review-queue=myproject --review-idle-threshold=5m --review-commits=10",
			"ntm --robot-review-queue=myproject --review-dispatch --type=claude",
		},
	}
}

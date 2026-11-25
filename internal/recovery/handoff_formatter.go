// Package recovery provides context recovery and restoration for agent sessions.
// It formats handoff data and other recovery sources into prompt text for injection.
package recovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Dicklesworthstone/ntm/internal/handoff"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// MaxHandoffTokens is the token budget for handoff context in agent prompt.
const MaxHandoffTokens = 500

// HandoffContext contains handoff data plus derived fields for recovery injection.
type HandoffContext struct {
	Path      string
	Goal      string
	Now       string
	Blockers  []string
	Decisions map[string]string
	Findings  map[string]string
	Next      []string
	Status    string
	Outcome   string
	Age       time.Duration

	// Integration references
	ActiveBeads      []string
	AgentMailThreads []string
	CMMemories       []string
}

// LoadHandoff selects the latest readable handoff belonging to the requested
// local project and session. A missing or empty handoff is optional context,
// while invalid provenance and cancellation are returned to the caller.
func LoadHandoff(ctx context.Context, sessionName, workingDir string) (*HandoffContext, error) {
	if ctx == nil {
		return nil, errors.New("recovery handoff requires a command context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tmux.DefaultClient != nil && tmux.DefaultClient.Remote != "" {
		return nil, errors.New("local handoff recovery is unavailable for remote tmux sessions")
	}
	if err := tmux.ValidateSessionName(sessionName); err != nil {
		return nil, fmt.Errorf("invalid handoff session: %w", err)
	}
	if !filepath.IsAbs(workingDir) {
		return nil, errors.New("recovery handoff requires an absolute project directory")
	}
	projectDir, err := filepath.EvalSymlinks(workingDir)
	if err != nil {
		return nil, fmt.Errorf("resolve handoff project: %w", err)
	}
	h, path, err := handoff.NewReader(projectDir).FindLatest(sessionName)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil || h == nil {
		return nil, err
	}
	if h.Session != sessionName {
		return nil, fmt.Errorf("handoff session %q does not match spawn session %q", h.Session, sessionName)
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("resolve handoff source: %w", err)
	}
	relative, err := filepath.Rel(projectDir, resolvedPath)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.New("handoff source is outside the spawn project")
	}
	if strings.TrimSpace(h.Goal) == "" && strings.TrimSpace(h.Now) == "" &&
		len(h.Next)+len(h.Decisions)+len(h.Findings)+len(h.Blockers) == 0 {
		return nil, nil
	}
	return HandoffContextFromHandoff(h, resolvedPath), nil
}

// TruncateHandoffPrompt keeps the formatter's immediate-task-first prefix
// within a byte budget, without splitting a UTF-8 rune. A truncated prompt
// includes a marker; budgets too small to retain context return no prompt.
func TruncateHandoffPrompt(prompt string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(prompt) <= maxBytes {
		return prompt
	}
	const suffix = "\n[Handoff truncated to fit recovery budget.]\n"
	if maxBytes <= len(suffix) {
		return ""
	}
	end := maxBytes - len(suffix)
	for end > 0 && !utf8.RuneStart(prompt[end]) {
		end--
	}
	return strings.TrimSpace(prompt[:end]) + suffix
}

// SessionType indicates the type of session start for context injection.
type SessionType int

const (
	// SessionFreshSpawn is a new session - full context injection.
	SessionFreshSpawn SessionType = iota
	// SessionAfterClear is after /clear - handoff + CM.
	SessionAfterClear
	// SessionAfterCompact is after compaction - minimal context.
	SessionAfterCompact
)

// HandoffContextFromHandoff converts a Handoff to HandoffContext.
func HandoffContextFromHandoff(h *handoff.Handoff, path string) *HandoffContext {
	if h == nil {
		return nil
	}

	ctx := &HandoffContext{
		Path:             path,
		Goal:             h.Goal,
		Now:              h.Now,
		Blockers:         h.Blockers,
		Decisions:        h.Decisions,
		Findings:         h.Findings,
		Next:             h.Next,
		Status:           h.Status,
		Outcome:          h.Outcome,
		Age:              time.Since(h.CreatedAt),
		ActiveBeads:      h.ActiveBeads,
		AgentMailThreads: h.AgentMailThreads,
		CMMemories:       h.CMMemories,
	}

	return ctx
}

// FormatHandoffContext creates prompt text from handoff context.
// Prioritizes: now > goal > next > decisions > findings > blockers
func FormatHandoffContext(h *HandoffContext) string {
	if h == nil {
		return ""
	}

	var b strings.Builder
	remaining := MaxHandoffTokens * 4 // rough bytes budget

	b.WriteString("## Previous Session Context\n\n")
	remaining -= 30

	// Priority 1: Now (most important - immediate task)
	if h.Now != "" && remaining > 100 {
		text := fmt.Sprintf("**Your immediate task:** %s\n\n", truncateToTokens(h.Now, 80))
		b.WriteString(text)
		remaining -= len(text)
	}

	// Priority 2: Goal
	if h.Goal != "" && remaining > 100 {
		text := fmt.Sprintf("**Last session achieved:** %s\n\n", truncateToTokens(h.Goal, 80))
		b.WriteString(text)
		remaining -= len(text)
	}

	// Priority 3: Next steps
	if len(h.Next) > 0 && remaining > 100 {
		b.WriteString("**Suggested next steps:**\n")
		for i, step := range h.Next {
			if remaining < 50 || i >= 3 {
				break
			}
			text := fmt.Sprintf("%d. %s\n", i+1, truncateToTokens(step, 60))
			b.WriteString(text)
			remaining -= len(text)
		}
		b.WriteString("\n")
	}

	// Priority 4: Decisions
	if len(h.Decisions) > 0 && remaining > 80 {
		b.WriteString("**Key decisions made:**\n")
		count := 0
		for _, k := range sortedMapKeys(h.Decisions) {
			if remaining < 40 || count >= 3 {
				break
			}
			v := h.Decisions[k]
			text := fmt.Sprintf("- %s: %s\n", k, truncateToTokens(v, 40))
			b.WriteString(text)
			remaining -= len(text)
			count++
		}
		b.WriteString("\n")
	}

	// Priority 5: Findings
	if len(h.Findings) > 0 && remaining > 80 {
		b.WriteString("**Key findings:**\n")
		count := 0
		for _, k := range sortedMapKeys(h.Findings) {
			if remaining < 40 || count >= 2 {
				break
			}
			v := h.Findings[k]
			text := fmt.Sprintf("- %s: %s\n", k, truncateToTokens(v, 40))
			b.WriteString(text)
			remaining -= len(text)
			count++
		}
		b.WriteString("\n")
	}

	// Priority 6: Blockers (if any)
	if len(h.Blockers) > 0 && remaining > 60 {
		b.WriteString("**Known blockers:**\n")
		for i, blocker := range h.Blockers {
			if remaining < 40 || i >= 2 {
				break
			}
			text := fmt.Sprintf("- %s\n", truncateToTokens(blocker, 50))
			b.WriteString(text)
			remaining -= len(text)
		}
	}

	result := b.String()

	slog.Debug("formatted handoff context",
		"goal_len", len(h.Goal),
		"now_len", len(h.Now),
		"output_len", len(result),
		"estimated_tokens", len(result)/4,
	)

	return result
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// FormatMinimalHandoff creates just goal/now for compact injection.
func FormatMinimalHandoff(h *HandoffContext) string {
	if h == nil {
		return ""
	}

	var parts []string
	if h.Goal != "" {
		parts = append(parts, fmt.Sprintf("Last: %s", truncateToTokens(h.Goal, 60)))
	}
	if h.Now != "" {
		parts = append(parts, fmt.Sprintf("Now: %s", truncateToTokens(h.Now, 60)))
	}

	return strings.Join(parts, " | ")
}

// GetInjectionForType returns the appropriate context injection for session type.
func GetInjectionForType(sessionType SessionType, h *HandoffContext, cmMemories []string) string {
	switch sessionType {
	case SessionFreshSpawn:
		// Full handoff context + all recovery sources
		return FormatHandoffContext(h)

	case SessionAfterClear:
		// Handoff + CM memories (lighter weight)
		handoffText := FormatHandoffContext(h)
		if len(cmMemories) > 0 {
			handoffText += "\n**Relevant memories:**\n"
			for i, mem := range cmMemories {
				if i >= 3 {
					break
				}
				handoffText += fmt.Sprintf("- %s\n", truncateToTokens(mem, 60))
			}
		}
		return handoffText

	case SessionAfterCompact:
		// Just goal/now (minimize overhead)
		return FormatMinimalHandoff(h)

	default:
		return FormatHandoffContext(h)
	}
}

// HumanizeDuration returns a human-readable duration string.
func HumanizeDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// truncateToTokens shortens a string to fit within a token budget.
func truncateToTokens(s string, maxTokens int) string {
	maxBytes := maxTokens * 4
	if len(s) <= maxBytes {
		return s
	}
	// Walk rune boundaries to avoid splitting multi-byte UTF-8 characters
	target := maxBytes - 3 // leave room for "..."
	end := 0
	for i := range s {
		if i > target {
			break
		}
		end = i
	}
	return s[:end] + "..."
}

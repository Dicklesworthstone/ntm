// Package bv provides integration with the beads_viewer (bv) tool.
// markdown.go implements markdown rendering for triage output.
package bv

import (
	"fmt"
	"strings"
)

// MarkdownOptions configures markdown triage output.
type MarkdownOptions struct {
	// Compact uses ultra-compact format (fewer tokens)
	Compact bool
	// MaxRecommendations limits recommendations shown
	MaxRecommendations int
	// MaxQuickWins limits quick wins shown
	MaxQuickWins int
	// IncludeScores includes score breakdowns
	IncludeScores bool
}

// DefaultMarkdownOptions returns sensible defaults.
func DefaultMarkdownOptions() MarkdownOptions {
	return MarkdownOptions{
		Compact:            false,
		MaxRecommendations: 5,
		MaxQuickWins:       3,
		IncludeScores:      false,
	}
}

// CompactMarkdownOptions returns options for condensed output.
// Savings come from showing fewer items, not truncating text.
func CompactMarkdownOptions() MarkdownOptions {
	return MarkdownOptions{
		Compact:            true,
		MaxRecommendations: 3,
		MaxQuickWins:       2,
		IncludeScores:      false,
	}
}

// GetTriageMarkdown returns triage data as markdown with caching.
func GetTriageMarkdown(dir string, opts MarkdownOptions) (string, error) {
	triage, err := GetTriage(dir)
	if err != nil {
		return "", err
	}
	return RenderTriageMarkdown(triage, opts), nil
}

// GetTriageMarkdownNoCache returns fresh triage data as markdown.
func GetTriageMarkdownNoCache(dir string, opts MarkdownOptions) (string, error) {
	triage, err := GetTriageNoCache(dir)
	if err != nil {
		return "", err
	}
	return RenderTriageMarkdown(triage, opts), nil
}

// RenderTriageMarkdown converts a TriageResponse to markdown.
func RenderTriageMarkdown(triage *TriageResponse, opts MarkdownOptions) string {
	if triage == nil {
		return "_No triage data available._"
	}

	var sb strings.Builder

	if opts.Compact {
		renderCompactTriage(&sb, triage, opts)
	} else {
		renderFullTriage(&sb, triage, opts)
	}

	return sb.String()
}

// renderCompactTriage renders condensed markdown with fewer items but NO truncation.
// Token savings come from showing fewer items and simpler formatting, not destroying information.
func renderCompactTriage(sb *strings.Builder, triage *TriageResponse, opts MarkdownOptions) {
	qr := &triage.Triage.QuickRef

	// One-line summary
	fmt.Fprintf(sb, "## Triage: %d ready, %d blocked, %d in_progress\n\n",
		qr.ActionableCount, qr.BlockedCount, qr.InProgressCount)

	// Top picks as compact list - FULL titles and reasons, no truncation
	if len(qr.TopPicks) > 0 {
		sb.WriteString("**Next:**\n")
		for i, pick := range qr.TopPicks {
			if i >= opts.MaxRecommendations {
				break
			}
			// Format: ID(score) - title | reason (NO TRUNCATION)
			fmt.Fprintf(sb, "- `%s` (%.2f) %s", pick.ID, pick.Score, pick.Title)
			if len(pick.Reasons) > 0 {
				fmt.Fprintf(sb, " | %s", pick.Reasons[0])
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// Quick wins (if any)
	quickWins := triage.Triage.QuickWins
	if len(quickWins) > 0 && opts.MaxQuickWins > 0 {
		sb.WriteString("**Quick wins:** ")
		ids := make([]string, 0, opts.MaxQuickWins)
		for i, w := range quickWins {
			if i >= opts.MaxQuickWins {
				break
			}
			ids = append(ids, w.ID)
		}
		sb.WriteString(strings.Join(ids, ", "))
		sb.WriteString("\n\n")
	}

	// Commands
	if len(triage.Triage.Commands) > 0 {
		sb.WriteString("**Commands:**\n")
		for name, cmd := range triage.Triage.Commands {
			fmt.Fprintf(sb, "- %s: `%s`\n", name, cmd)
		}
	}
}

// renderFullTriage renders detailed markdown.
func renderFullTriage(sb *strings.Builder, triage *TriageResponse, opts MarkdownOptions) {
	qr := &triage.Triage.QuickRef

	// Header with counts
	sb.WriteString("## Beads Triage\n\n")
	sb.WriteString("| Metric | Count |\n")
	sb.WriteString("|--------|-------|\n")
	fmt.Fprintf(sb, "| Open | %d |\n", qr.OpenCount)
	fmt.Fprintf(sb, "| Actionable | %d |\n", qr.ActionableCount)
	fmt.Fprintf(sb, "| Blocked | %d |\n", qr.BlockedCount)
	fmt.Fprintf(sb, "| In Progress | %d |\n\n", qr.InProgressCount)

	// Top recommendations
	sb.WriteString("### Recommendations\n\n")
	recs := triage.Triage.Recommendations
	if len(recs) == 0 {
		sb.WriteString("_No recommendations._\n\n")
	} else {
		for i, rec := range recs {
			if i >= opts.MaxRecommendations {
				if len(recs) > opts.MaxRecommendations {
					fmt.Fprintf(sb, "_...and %d more_\n\n", len(recs)-opts.MaxRecommendations)
				}
				break
			}
			renderRecommendation(sb, &rec, i+1, opts)
		}
	}

	// Quick wins
	quickWins := triage.Triage.QuickWins
	if len(quickWins) > 0 {
		sb.WriteString("### Quick Wins\n\n")
		for i, w := range quickWins {
			if i >= opts.MaxQuickWins {
				break
			}
			fmt.Fprintf(sb, "%d. **%s** (%s) - %s\n", i+1, w.ID, w.Type, w.Title)
			if w.Action != "" {
				fmt.Fprintf(sb, "   Action: %s\n", w.Action)
			}
		}
		sb.WriteString("\n")
	}

	// Blockers to clear
	blockers := triage.Triage.BlockersToClear
	if len(blockers) > 0 {
		sb.WriteString("### Blockers to Clear\n\n")
		for i, b := range blockers {
			if i >= 3 {
				break
			}
			unblocks := ""
			if len(b.UnblocksIDs) > 0 {
				unblocks = fmt.Sprintf(" (unblocks %d)", len(b.UnblocksIDs))
			}
			fmt.Fprintf(sb, "- **%s**%s: %s\n", b.ID, unblocks, b.Title)
		}
		sb.WriteString("\n")
	}

	// Project health summary
	if triage.Triage.ProjectHealth != nil {
		renderHealthSummary(sb, triage.Triage.ProjectHealth)
	}
}

// renderRecommendation renders a single recommendation.
func renderRecommendation(sb *strings.Builder, rec *TriageRecommendation, num int, opts MarkdownOptions) {
	priority := fmt.Sprintf("P%d", rec.Priority)
	fmt.Fprintf(sb, "#### %d. %s (%s, %s)\n", num, rec.ID, rec.Type, priority)
	fmt.Fprintf(sb, "**%s**\n\n", rec.Title)

	// Reasons
	if len(rec.Reasons) > 0 {
		for _, r := range rec.Reasons {
			fmt.Fprintf(sb, "- %s\n", r)
		}
		sb.WriteString("\n")
	}

	// Action
	if rec.Action != "" {
		fmt.Fprintf(sb, "**Action:** %s\n\n", rec.Action)
	}

	// Score breakdown (optional)
	if opts.IncludeScores && rec.Breakdown != nil {
		fmt.Fprintf(sb, "Score: %.3f (pagerank: %.3f, blocker_ratio: %.3f, priority: %.3f)\n\n",
			rec.Score, rec.Breakdown.Pagerank, rec.Breakdown.BlockerRatio, rec.Breakdown.PriorityBoost)
	}
}

// renderHealthSummary renders project health metrics.
func renderHealthSummary(sb *strings.Builder, health *ProjectHealth) {
	sb.WriteString("### Project Health\n\n")

	if health.GraphMetrics != nil {
		gm := health.GraphMetrics
		fmt.Fprintf(sb, "- Nodes: %d, Edges: %d, Density: %.3f\n",
			gm.TotalNodes, gm.TotalEdges, gm.Density)
		if gm.CycleCount > 0 {
			fmt.Fprintf(sb, "- **Cycles: %d** (needs attention)\n", gm.CycleCount)
		}
	}
	sb.WriteString("\n")
}

// AgentType represents the type of AI agent.
type AgentType string

const (
	AgentClaude      AgentType = "claude"
	AgentCodex       AgentType = "codex"
	AgentGemini      AgentType = "gemini"
	AgentAntigravity AgentType = "antigravity"
)

// AgentContextBudget defines context window sizes per agent type.
var AgentContextBudget = map[AgentType]int{
	AgentClaude:      180000, // Claude has large context
	AgentCodex:       120000, // Codex has medium context
	AgentGemini:      100000, // Gemini has smaller context
	AgentAntigravity: 100000, // Antigravity (Gemini successor): mirrors Gemini
}

// TriageFormat represents the output format for triage.
type TriageFormat string

const (
	FormatJSON     TriageFormat = "json"
	FormatMarkdown TriageFormat = "markdown"
)

// PreferredFormat returns the preferred triage format for an agent type.
// Claude gets JSON (large context), Codex/Gemini get markdown (more readable).
func PreferredFormat(agent AgentType) TriageFormat {
	switch agent {
	case AgentClaude:
		return FormatJSON
	case AgentCodex, AgentGemini, AgentAntigravity:
		return FormatMarkdown
	default:
		return FormatJSON
	}
}

// GetTriageForAgent returns triage in the preferred format for the agent type.
// Returns (content, format, error).
func GetTriageForAgent(dir string, agent AgentType) (string, TriageFormat, error) {
	format := PreferredFormat(agent)

	switch format {
	case FormatMarkdown:
		opts := CompactMarkdownOptions()
		content, err := GetTriageMarkdown(dir, opts)
		return content, format, err
	default:
		triage, err := GetTriage(dir)
		if err != nil {
			return "", format, err
		}
		// Return as JSON string (caller can marshal if needed)
		return renderTriageJSON(triage), format, nil
	}
}

// renderTriageJSON returns a compact JSON representation.
func renderTriageJSON(triage *TriageResponse) string {
	if triage == nil {
		return "{}"
	}
	// Compact quick-ref counts only, by design; callers that need the full
	// triage should json.Marshal the TriageResponse directly.
	qr := &triage.Triage.QuickRef
	return fmt.Sprintf(`{"actionable":%d,"blocked":%d,"in_progress":%d,"top_picks":%d}`,
		qr.ActionableCount, qr.BlockedCount, qr.InProgressCount, len(qr.TopPicks))
}

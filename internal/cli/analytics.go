package cli

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func newAnalyticsCmd() *cobra.Command {
	var days int
	var since string
	var format string
	var showSessions bool

	cmd := &cobra.Command{
		Use:   "analytics",
		Short: "View session analytics and statistics",
		Long: `Display aggregated analytics from NTM session events.

Shows summary statistics including:
  - Total sessions created
  - Agent spawn counts by harness (Claude, Codex, Gemini, Antigravity, Opencode, Grok, Cursor, Windsurf, Aider, OMP, Ollama)
  - Prompts sent, character counts, and estimated tokens
  - Error occurrences, agent crashes, and agent restarts

Time Filtering:
  --days N      Show last N days (default: 30)
  --since DATE  Show events since DATE (YYYY-MM-DD or RFC3339)

Output Formats:
  --format text        Human-readable output (default)
  --format json        JSON output
  --format csv         CSV output
  --format prometheus  Prometheus exposition format

Examples:
  ntm analytics                           # Last 30 days summary
  ntm analytics --days 7                  # Last 7 days
  ntm analytics --since 2025-01-01        # Since specific date
  ntm analytics --format json             # JSON output
  ntm analytics --format prometheus       # Prometheus output
  ntm analytics --sessions               # Include per-session details`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAnalytics(cmd.Context(), days, since, format, showSessions)
		},
	}

	cmd.Flags().IntVar(&days, "days", 30, "show last N days of analytics")
	cmd.Flags().StringVar(&since, "since", "", "show analytics since date (YYYY-MM-DD or RFC3339)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text, json, csv, prometheus")
	cmd.Flags().BoolVar(&showSessions, "sessions", false, "include per-session breakdown")

	return cmd
}

func runAnalytics(ctx context.Context, days int, since, format string, showSessions bool) error {
	format = strings.ToLower(strings.TrimSpace(format))
	if IsJSONOutput() {
		format = "json"
	}

	out, err := robot.GetAnalytics(ctx, robot.AnalyticsOptions{
		Days:     days,
		Since:    since,
		Sessions: showSessions,
	})
	if err != nil {
		return err
	}
	if !out.Success {
		return errors.New(out.Error)
	}

	return outputAnalytics(out, format, showSessions)
}

func isAnalyticsAgentType(agentType string) bool {
	return robot.IsAnalyticsAgentType(agentType)
}

func outputAnalytics(out *robot.AnalyticsOutput, format string, showSessions bool) error {
	switch format {
	case "json":
		return outputAnalyticsJSON(out)
	case "csv":
		return outputAnalyticsCSV(out)
	case "prometheus", "prom":
		return outputAnalyticsPrometheus(out)
	default:
		return outputAnalyticsText(out, showSessions)
	}
}

func outputAnalyticsJSON(out *robot.AnalyticsOutput) error {
	breakdown := out.AgentBreakdown
	if breakdown == nil {
		breakdown = map[string]robot.AnalyticsAgentStats{}
	}
	resp := output.AnalyticsResponse{
		TimestampedResponse: output.NewTimestamped(),
		Period:              out.Period,
		Since:               out.Since,
		Days:                out.Days,
		TotalSessions:       out.TotalSessions,
		TotalAgents:         out.TotalAgents,
		TotalPrompts:        out.TotalPrompts,
		TotalCharsSent:      out.TotalCharsSent,
		TotalTokensEst:      out.TotalTokensEst,
		AgentBreakdown:      breakdown,
		Sessions:            out.Sessions,
		ErrorCount:          out.ErrorCount,
		ErrorTypes:          out.ErrorTypes,
		AgentCrashes:        out.AgentCrashes,
		AgentRestarts:       out.AgentRestarts,
	}
	return output.PrintJSON(resp)
}

func outputAnalyticsCSV(out *robot.AnalyticsOutput) error {
	w := csv.NewWriter(os.Stdout)
	defer w.Flush()

	if err := w.Write([]string{"metric", "value"}); err != nil {
		return err
	}

	rows := [][]string{
		{"period", out.Period},
		{"total_sessions", fmt.Sprintf("%d", out.TotalSessions)},
		{"total_agents", fmt.Sprintf("%d", out.TotalAgents)},
		{"total_prompts", fmt.Sprintf("%d", out.TotalPrompts)},
		{"total_chars_sent", fmt.Sprintf("%d", out.TotalCharsSent)},
		{"total_tokens_estimated", fmt.Sprintf("%d", out.TotalTokensEst)},
		{"error_count", fmt.Sprintf("%d", out.ErrorCount)},
		{"agent_crashes", fmt.Sprintf("%d", out.AgentCrashes)},
		{"agent_restarts", fmt.Sprintf("%d", out.AgentRestarts)},
	}

	for _, row := range rows {
		if err := w.Write(row); err != nil {
			return err
		}
	}

	agentTypes := make([]string, 0, len(out.AgentBreakdown))
	for agentType := range out.AgentBreakdown {
		agentTypes = append(agentTypes, agentType)
	}
	sort.Strings(agentTypes)

	for _, agentType := range agentTypes {
		agentStats := out.AgentBreakdown[agentType]
		if err := w.Write([]string{agentType + "_count", fmt.Sprintf("%d", agentStats.Count)}); err != nil {
			return err
		}
		if err := w.Write([]string{agentType + "_prompts", fmt.Sprintf("%d", agentStats.Prompts)}); err != nil {
			return err
		}
		if err := w.Write([]string{agentType + "_chars_sent", fmt.Sprintf("%d", agentStats.CharsSent)}); err != nil {
			return err
		}
		if err := w.Write([]string{agentType + "_tokens_estimated", fmt.Sprintf("%d", agentStats.TokensEst)}); err != nil {
			return err
		}
	}

	return nil
}

func outputAnalyticsPrometheus(out *robot.AnalyticsOutput) error {
	_, err := fmt.Print(robot.FormatAnalyticsPrometheus(out))
	return err
}

func outputAnalyticsText(out *robot.AnalyticsOutput, showSessions bool) error {
	fmt.Printf("NTM Analytics - %s\n", out.Period)
	fmt.Println(strings.Repeat("=", 40))

	fmt.Printf("\nSummary:\n")
	fmt.Printf("  Sessions:     %d\n", out.TotalSessions)
	fmt.Printf("  Agents:       %d\n", out.TotalAgents)
	fmt.Printf("  Prompts:      %d\n", out.TotalPrompts)
	fmt.Printf("  Characters:   %d\n", out.TotalCharsSent)
	fmt.Printf("  Tokens (est): %s\n", formatTokenCount(out.TotalTokensEst))
	if out.AgentCrashes > 0 || out.AgentRestarts > 0 {
		fmt.Printf("  Crashes:      %d\n", out.AgentCrashes)
		fmt.Printf("  Restarts:     %d\n", out.AgentRestarts)
	}

	if len(out.AgentBreakdown) > 0 {
		agentTypes := make([]string, 0, len(out.AgentBreakdown))
		for agentType := range out.AgentBreakdown {
			agentTypes = append(agentTypes, agentType)
		}
		sort.Strings(agentTypes)

		fmt.Printf("\nAgent Breakdown:\n")
		for _, agentType := range agentTypes {
			agentStats := out.AgentBreakdown[agentType]
			displayName := agentType
			if len(agentType) > 0 {
				displayName = strings.ToUpper(agentType[:1]) + agentType[1:]
			}
			fmt.Printf("  %s:\n", displayName)
			fmt.Printf("    Spawned:      %d\n", agentStats.Count)
			fmt.Printf("    Prompts:      %d\n", agentStats.Prompts)
			fmt.Printf("    Characters:   %d\n", agentStats.CharsSent)
			fmt.Printf("    Tokens (est): %s\n", formatTokenCount(agentStats.TokensEst))
		}
	}

	if out.ErrorCount > 0 {
		fmt.Printf("\nErrors: %d\n", out.ErrorCount)
		if len(out.ErrorTypes) > 0 {
			errTypes := make([]string, 0, len(out.ErrorTypes))
			for errType := range out.ErrorTypes {
				errTypes = append(errTypes, errType)
			}
			sort.Strings(errTypes)
			for _, errType := range errTypes {
				fmt.Printf("  %s: %d\n", errType, out.ErrorTypes[errType])
			}
		}
	}

	if showSessions && len(out.Sessions) > 0 {
		fmt.Printf("\nRecent Sessions:\n")
		for _, s := range out.Sessions {
			fmt.Printf("  %s (%s): %d agents, %d prompts\n",
				s.Name,
				s.CreatedAt.Format("2006-01-02 15:04"),
				s.AgentCount,
				s.PromptCount)
		}
	}

	return nil
}

// formatTokenCount formats a token count with K/M suffixes for readability.
func formatTokenCount(tokens int) string {
	if tokens >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(tokens)/1000000)
	}
	if tokens >= 1000 {
		return fmt.Sprintf("%.1fK", float64(tokens)/1000)
	}
	return fmt.Sprintf("%d", tokens)
}

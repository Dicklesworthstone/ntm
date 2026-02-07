package cli

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/output"
)

// AnalyticsStats holds aggregated analytics data.
type AnalyticsStats struct {
	Period         string                `json:"period"`
	TotalSessions  int                   `json:"total_sessions"`
	TotalAgents    int                   `json:"total_agents"`
	TotalPrompts   int                   `json:"total_prompts"`
	TotalCharsSent int                   `json:"total_chars_sent"`
	TotalTokensEst int                   `json:"total_tokens_estimated"`
	AgentBreakdown map[string]AgentStats `json:"agent_breakdown"`
	SessionDetails []SessionSummary      `json:"sessions,omitempty"`
	ErrorCount     int                   `json:"error_count"`
	ErrorTypes     map[string]int        `json:"error_types,omitempty"`
}

// AgentStats holds per-agent-type statistics.
type AgentStats struct {
	Count     int `json:"count"`
	Prompts   int `json:"prompts"`
	CharsSent int `json:"chars_sent"`
	TokensEst int `json:"tokens_estimated"`
}

// SessionSummary provides details about a single session.
type SessionSummary struct {
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"created_at"`
	AgentCount  int       `json:"agent_count"`
	PromptCount int       `json:"prompt_count"`
}

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
  - Agent spawn counts by type (Claude, Codex, Gemini)
  - Prompts sent and character counts
  - Error occurrences

Time Filtering:
  --days N      Show last N days (default: 30)
  --since DATE  Show events since DATE (YYYY-MM-DD format)

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
			return runAnalytics(days, since, format, showSessions)
		},
	}

	cmd.Flags().IntVar(&days, "days", 30, "show last N days of analytics")
	cmd.Flags().StringVar(&since, "since", "", "show analytics since date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text, json, csv, prometheus")
	cmd.Flags().BoolVar(&showSessions, "sessions", false, "include per-session breakdown")

	return cmd
}

func runAnalytics(days int, since, format string, showSessions bool) error {
	// Determine the cutoff time
	var cutoff time.Time
	if since != "" {
		parsed, err := time.Parse("2006-01-02", since)
		if err != nil {
			return fmt.Errorf("invalid --since format (expected YYYY-MM-DD): %w", err)
		}
		cutoff = parsed
	} else {
		cutoff = time.Now().AddDate(0, 0, -days)
	}

	// Read events from the log file
	logPath := events.DefaultOptions().Path
	eventList, err := readEvents(logPath, cutoff)
	if err != nil {
		if os.IsNotExist(err) {
			// No events file yet - output empty stats
			stats := AnalyticsStats{
				Period:         fmt.Sprintf("Last %d days (no events)", days),
				AgentBreakdown: make(map[string]AgentStats),
				ErrorTypes:     make(map[string]int),
			}
			return outputStats(stats, format, showSessions)
		}
		return fmt.Errorf("reading events: %w", err)
	}

	// Aggregate statistics
	stats := aggregateStats(eventList, days, since, cutoff)
	stats.SessionDetails = nil // Clear unless requested
	if showSessions {
		stats.SessionDetails = buildSessionDetails(eventList)
	}

	return outputStats(stats, format, showSessions)
}

// readEvents reads and filters events from the JSONL file using streaming.
func readEvents(path string, cutoff time.Time) ([]events.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var result []events.Event
	scanner := bufio.NewScanner(f)
	// Set a reasonable buffer limit for lines (e.g., 1MB per event line)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event events.Event
		if err := json.Unmarshal(line, &event); err != nil {
			continue // Skip malformed lines
		}

		if event.Timestamp.After(cutoff) {
			result = append(result, event)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// aggregateStats computes statistics from event list.
func aggregateStats(eventList []events.Event, days int, since string, cutoff time.Time) AnalyticsStats {
	stats := AnalyticsStats{
		AgentBreakdown: make(map[string]AgentStats),
		ErrorTypes:     make(map[string]int),
	}

	// Set period description
	if since != "" {
		stats.Period = fmt.Sprintf("Since %s", since)
	} else {
		stats.Period = fmt.Sprintf("Last %d days", days)
	}

	// Track unique sessions
	sessions := make(map[string]bool)

	for _, event := range eventList {
		switch event.Type {
		case events.EventSessionCreate:
			stats.TotalSessions++
			sessions[event.Session] = true

			// Count agents from session create data
			if cc, ok := event.Data["claude_count"].(float64); ok {
				stats.TotalAgents += int(cc)
				updateAgentStats(stats.AgentBreakdown, "claude", int(cc), 0, 0)
			}
			if cod, ok := event.Data["codex_count"].(float64); ok {
				stats.TotalAgents += int(cod)
				updateAgentStats(stats.AgentBreakdown, "codex", int(cod), 0, 0)
			}
			if gmi, ok := event.Data["gemini_count"].(float64); ok {
				stats.TotalAgents += int(gmi)
				updateAgentStats(stats.AgentBreakdown, "gemini", int(gmi), 0, 0)
			}

		case events.EventAgentSpawn:
			if agentType, ok := event.Data["agent_type"].(string); ok {
				// Agent was spawned - already counted in session create
				_ = agentType
			}

		case events.EventPromptSend:
			stats.TotalPrompts++

			if length, ok := event.Data["prompt_length"].(float64); ok {
				stats.TotalCharsSent += int(length)
			}

			// Aggregate token estimates
			var tokenEst int
			if tokens, ok := event.Data["estimated_tokens"].(float64); ok {
				tokenEst = int(tokens)
			} else if length, ok := event.Data["prompt_length"].(float64); ok {
				// Fallback: estimate from prompt_length (~3.5 chars/token)
				tokenEst = int(length) * 10 / 35
			}
			stats.TotalTokensEst += tokenEst

			// Update per-type stats based on target_types
			// When a prompt is sent to multiple agent types, divide the tokens
			// proportionally to avoid over-counting in the per-agent breakdown
			if targetTypes, ok := event.Data["target_types"].(string); ok {
				targets := parseTargetTypes(targetTypes)
				if len(targets) > 0 {
					// Divide tokens among targets to avoid overcounting
					tokensPerTarget := tokenEst / len(targets)
					for _, t := range targets {
						updateAgentStats(stats.AgentBreakdown, t, 0, 1, tokensPerTarget)
					}
				}
			}

		case events.EventError:
			stats.ErrorCount++
			if errType, ok := event.Data["error_type"].(string); ok {
				stats.ErrorTypes[errType]++
			}
		}
	}

	return stats
}

// updateAgentStats updates or creates agent stats entry.
func updateAgentStats(breakdown map[string]AgentStats, agentType string, countDelta, promptsDelta, tokensDelta int) {
	current := breakdown[agentType]
	current.Count += countDelta
	current.Prompts += promptsDelta
	current.TokensEst += tokensDelta
	breakdown[agentType] = current
}

// parseTargetTypes parses the target_types string to extract agent types.
func parseTargetTypes(targets string) []string {
	var result []string
	targets = strings.ToLower(targets)

	if strings.Contains(targets, "cc") || strings.Contains(targets, "claude") {
		result = append(result, "claude")
	}
	if strings.Contains(targets, "cod") || strings.Contains(targets, "codex") {
		result = append(result, "codex")
	}
	if strings.Contains(targets, "gmi") || strings.Contains(targets, "gemini") {
		result = append(result, "gemini")
	}
	if strings.Contains(targets, "all") || strings.Contains(targets, "agents") {
		// "all" or "agents" means all types
		if len(result) == 0 {
			result = []string{"claude", "codex", "gemini"}
		}
	}

	return result
}

// buildSessionDetails creates per-session summaries.
func buildSessionDetails(eventList []events.Event) []SessionSummary {
	sessionMap := make(map[string]*SessionSummary)

	for _, event := range eventList {
		if event.Session == "" {
			continue
		}

		summary, exists := sessionMap[event.Session]
		if !exists {
			summary = &SessionSummary{
				Name:      event.Session,
				CreatedAt: event.Timestamp,
			}
			sessionMap[event.Session] = summary
		}

		switch event.Type {
		case events.EventSessionCreate:
			summary.CreatedAt = event.Timestamp
			// Count agents
			if cc, ok := event.Data["claude_count"].(float64); ok {
				summary.AgentCount += int(cc)
			}
			if cod, ok := event.Data["codex_count"].(float64); ok {
				summary.AgentCount += int(cod)
			}
			if gmi, ok := event.Data["gemini_count"].(float64); ok {
				summary.AgentCount += int(gmi)
			}
		case events.EventPromptSend:
			summary.PromptCount++
		}
	}

	// Convert to slice and sort by time
	var result []SessionSummary
	for _, s := range sessionMap {
		result = append(result, *s)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

	return result
}

// outputStats outputs the statistics in the requested format.
func outputStats(stats AnalyticsStats, format string, showSessions bool) error {
	switch format {
	case "json":
		return outputStatsJSON(stats)
	case "csv":
		return outputStatsCSV(stats)
	case "prometheus", "prom":
		return outputStatsPrometheus(stats)
	default:
		return outputStatsText(stats, showSessions)
	}
}

func outputStatsJSON(stats AnalyticsStats) error {
	if IsJSONOutput() {
		resp := output.AnalyticsResponse{
			TimestampedResponse: output.NewTimestamped(),
			Period:              stats.Period,
			TotalSessions:       stats.TotalSessions,
			TotalAgents:         stats.TotalAgents,
			TotalPrompts:        stats.TotalPrompts,
			TotalCharsSent:      stats.TotalCharsSent,
			TotalTokensEst:      stats.TotalTokensEst,
			ErrorCount:          stats.ErrorCount,
		}
		return output.PrintJSON(resp)
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(stats)
}

func outputStatsCSV(stats AnalyticsStats) error {
	w := csv.NewWriter(os.Stdout)
	defer w.Flush()

	// Header
	if err := w.Write([]string{"metric", "value"}); err != nil {
		return err
	}

	// Write metrics
	rows := [][]string{
		{"period", stats.Period},
		{"total_sessions", fmt.Sprintf("%d", stats.TotalSessions)},
		{"total_agents", fmt.Sprintf("%d", stats.TotalAgents)},
		{"total_prompts", fmt.Sprintf("%d", stats.TotalPrompts)},
		{"total_chars_sent", fmt.Sprintf("%d", stats.TotalCharsSent)},
		{"total_tokens_estimated", fmt.Sprintf("%d", stats.TotalTokensEst)},
		{"error_count", fmt.Sprintf("%d", stats.ErrorCount)},
	}

	for _, row := range rows {
		if err := w.Write(row); err != nil {
			return err
		}
	}

	// Agent breakdown
	for agentType, agentStats := range stats.AgentBreakdown {
		if err := w.Write([]string{agentType + "_count", fmt.Sprintf("%d", agentStats.Count)}); err != nil {
			return err
		}
		if err := w.Write([]string{agentType + "_prompts", fmt.Sprintf("%d", agentStats.Prompts)}); err != nil {
			return err
		}
	}

	return nil
}

func outputStatsPrometheus(stats AnalyticsStats) error {
	fmt.Println("# HELP ntm_sessions_total Total sessions in the analytics period.")
	fmt.Println("# TYPE ntm_sessions_total gauge")
	fmt.Printf("ntm_sessions_total{period=%q} %d\n", stats.Period, stats.TotalSessions)
	fmt.Println()

	fmt.Println("# HELP ntm_agents_total Total agents spawned in the analytics period.")
	fmt.Println("# TYPE ntm_agents_total gauge")
	fmt.Printf("ntm_agents_total{period=%q} %d\n", stats.Period, stats.TotalAgents)
	fmt.Println()

	fmt.Println("# HELP ntm_prompts_total Total prompts sent in the analytics period.")
	fmt.Println("# TYPE ntm_prompts_total gauge")
	fmt.Printf("ntm_prompts_total{period=%q} %d\n", stats.Period, stats.TotalPrompts)
	fmt.Println()

	fmt.Println("# HELP ntm_chars_sent_total Total characters sent in the analytics period.")
	fmt.Println("# TYPE ntm_chars_sent_total gauge")
	fmt.Printf("ntm_chars_sent_total{period=%q} %d\n", stats.Period, stats.TotalCharsSent)
	fmt.Println()

	fmt.Println("# HELP ntm_tokens_estimated_total Estimated total tokens in the analytics period.")
	fmt.Println("# TYPE ntm_tokens_estimated_total gauge")
	fmt.Printf("ntm_tokens_estimated_total{period=%q} %d\n", stats.Period, stats.TotalTokensEst)
	fmt.Println()

	fmt.Println("# HELP ntm_errors_total Total errors in the analytics period.")
	fmt.Println("# TYPE ntm_errors_total gauge")
	fmt.Printf("ntm_errors_total{period=%q} %d\n", stats.Period, stats.ErrorCount)
	fmt.Println()

	if len(stats.AgentBreakdown) > 0 {
		fmt.Println("# HELP ntm_agent_spawns Agent spawn count by type.")
		fmt.Println("# TYPE ntm_agent_spawns gauge")
		for agentType, as := range stats.AgentBreakdown {
			fmt.Printf("ntm_agent_spawns{period=%q,agent_type=%q} %d\n", stats.Period, agentType, as.Count)
		}
		fmt.Println()

		fmt.Println("# HELP ntm_agent_prompts Prompts sent by agent type.")
		fmt.Println("# TYPE ntm_agent_prompts gauge")
		for agentType, as := range stats.AgentBreakdown {
			fmt.Printf("ntm_agent_prompts{period=%q,agent_type=%q} %d\n", stats.Period, agentType, as.Prompts)
		}
		fmt.Println()

		fmt.Println("# HELP ntm_agent_tokens_estimated Estimated tokens by agent type.")
		fmt.Println("# TYPE ntm_agent_tokens_estimated gauge")
		for agentType, as := range stats.AgentBreakdown {
			fmt.Printf("ntm_agent_tokens_estimated{period=%q,agent_type=%q} %d\n", stats.Period, agentType, as.TokensEst)
		}
		fmt.Println()
	}

	if len(stats.ErrorTypes) > 0 {
		fmt.Println("# HELP ntm_errors_by_type Error count by type.")
		fmt.Println("# TYPE ntm_errors_by_type gauge")
		for errType, count := range stats.ErrorTypes {
			fmt.Printf("ntm_errors_by_type{period=%q,error_type=%q} %d\n", stats.Period, errType, count)
		}
		fmt.Println()
	}

	return nil
}

func outputStatsText(stats AnalyticsStats, showSessions bool) error {
	fmt.Printf("NTM Analytics - %s\n", stats.Period)
	fmt.Println(strings.Repeat("=", 40))

	fmt.Printf("\nSummary:\n")
	fmt.Printf("  Sessions:     %d\n", stats.TotalSessions)
	fmt.Printf("  Agents:       %d\n", stats.TotalAgents)
	fmt.Printf("  Prompts:      %d\n", stats.TotalPrompts)
	fmt.Printf("  Characters:   %d\n", stats.TotalCharsSent)
	fmt.Printf("  Tokens (est): %s\n", formatTokenCount(stats.TotalTokensEst))

	if len(stats.AgentBreakdown) > 0 {
		fmt.Printf("\nAgent Breakdown:\n")
		for agentType, agentStats := range stats.AgentBreakdown {
			// Capitalize first letter (strings.Title is deprecated)
			displayName := agentType
			if len(agentType) > 0 {
				displayName = strings.ToUpper(agentType[:1]) + agentType[1:]
			}
			fmt.Printf("  %s:\n", displayName)
			fmt.Printf("    Spawned:      %d\n", agentStats.Count)
			fmt.Printf("    Prompts:      %d\n", agentStats.Prompts)
			fmt.Printf("    Tokens (est): %s\n", formatTokenCount(agentStats.TokensEst))
		}
	}

	if stats.ErrorCount > 0 {
		fmt.Printf("\nErrors: %d\n", stats.ErrorCount)
		if len(stats.ErrorTypes) > 0 {
			for errType, count := range stats.ErrorTypes {
				fmt.Printf("  %s: %d\n", errType, count)
			}
		}
	}

	if showSessions && len(stats.SessionDetails) > 0 {
		fmt.Printf("\nRecent Sessions:\n")
		for _, s := range stats.SessionDetails {
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

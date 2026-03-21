package robot

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
	tokenpkg "github.com/Dicklesworthstone/ntm/internal/tokens"
)

// TokensOptions configures token usage analysis
type TokensOptions struct {
	Days      int    // Number of days to analyze
	Since     string // ISO8601 timestamp to analyze since
	GroupBy   string // Grouping: agent, model, day, week, month
	Session   string // Filter to specific session (empty = all)
	AgentType string // Filter to specific agent type (empty = all)
}

// TokensOutput is the structured output for --robot-tokens
type TokensOutput struct {
	RobotResponse
	Period          string                     `json:"period"`
	GeneratedAt     time.Time                  `json:"generated_at"`
	GroupBy         string                     `json:"group_by"`
	TotalTokens     int                        `json:"total_tokens"`
	TotalPrompts    int                        `json:"total_prompts"`
	TotalCharacters int                        `json:"total_characters"`
	Breakdown       []TokenBreakdown           `json:"breakdown"`
	AgentStats      map[string]AgentTokenStats `json:"agent_stats,omitempty"`
	ModelStats      map[string]ModelTokenStats `json:"model_stats,omitempty"`
	TimeStats       []TimeTokenStats           `json:"time_stats,omitempty"`
	AgentHints      *TokensAgentHints          `json:"_agent_hints,omitempty"`
}

// TokenBreakdown is a single breakdown entry (generic for any grouping)
type TokenBreakdown struct {
	Key        string  `json:"key"`
	Tokens     int     `json:"tokens"`
	Prompts    int     `json:"prompts"`
	Characters int     `json:"characters"`
	Percentage float64 `json:"percentage"`
}

// AgentTokenStats holds per-agent-type statistics
type AgentTokenStats struct {
	Spawned    int            `json:"spawned"`
	Prompts    int            `json:"prompts"`
	Tokens     int            `json:"tokens"`
	Characters int            `json:"characters"`
	Models     map[string]int `json:"models,omitempty"` // model -> token count
}

// ModelTokenStats holds per-model statistics
type ModelTokenStats struct {
	AgentType  string `json:"agent_type"`
	Prompts    int    `json:"prompts"`
	Tokens     int    `json:"tokens"`
	Characters int    `json:"characters"`
}

// TimeTokenStats holds time-based statistics
type TimeTokenStats struct {
	Period     string `json:"period"` // "2025-12-15" or "2025-W50" or "2025-12"
	Tokens     int    `json:"tokens"`
	Prompts    int    `json:"prompts"`
	Characters int    `json:"characters"`
}

// TokensAgentHints provides actionable hints for AI agents
type TokensAgentHints struct {
	Summary           string   `json:"summary,omitempty"`
	SuggestedCommands []string `json:"suggested_commands,omitempty"`
	Warnings          []string `json:"warnings,omitempty"`
}

// GetTokens returns token usage statistics.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetTokens(opts TokensOptions) (*TokensOutput, error) {
	// Determine cutoff time
	var cutoff time.Time
	if opts.Since != "" {
		parsed, err := time.Parse(time.RFC3339, opts.Since)
		if err != nil {
			// Try date-only format
			parsed, err = time.Parse("2006-01-02", opts.Since)
			if err != nil {
				return &TokensOutput{
					RobotResponse: NewErrorResponse(
						fmt.Errorf("invalid --since format: %w", err),
						ErrCodeInvalidFlag,
						"Use ISO8601 (2025-12-15T00:00:00Z) or date (2025-12-15)",
					),
					Breakdown:  []TokenBreakdown{},
					AgentStats: make(map[string]AgentTokenStats),
					ModelStats: make(map[string]ModelTokenStats),
				}, nil
			}
		}
		cutoff = parsed
	} else {
		if opts.Days <= 0 {
			opts.Days = 30
		}
		cutoff = time.Now().AddDate(0, 0, -opts.Days)
	}

	// Normalize group by
	groupBy := strings.ToLower(opts.GroupBy)
	if groupBy == "" {
		groupBy = "agent"
	}
	validGroups := map[string]bool{"agent": true, "model": true, "day": true, "week": true, "month": true}
	if !validGroups[groupBy] {
		return &TokensOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("invalid --group-by '%s'", opts.GroupBy),
				ErrCodeInvalidFlag,
				"Valid groups: agent, model, day, week, month",
			),
			Breakdown:  []TokenBreakdown{},
			AgentStats: make(map[string]AgentTokenStats),
			ModelStats: make(map[string]ModelTokenStats),
		}, nil
	}

	// Read events
	logPath := events.DefaultOptions().Path
	eventList, err := readTokenEvents(logPath, cutoff, opts.Session, opts.AgentType)
	if err != nil {
		if os.IsNotExist(err) {
			// No events - return empty stats
			return &TokensOutput{
				RobotResponse: NewRobotResponse(true),
				Period:        formatPeriod(opts.Days, opts.Since),
				GeneratedAt:   time.Now().UTC(),
				GroupBy:       groupBy,
				Breakdown:     make([]TokenBreakdown, 0),
				AgentStats:    make(map[string]AgentTokenStats),
				ModelStats:    make(map[string]ModelTokenStats),
				AgentHints: &TokensAgentHints{
					Summary: "No events found for the specified period",
				},
			}, nil
		}
		return &TokensOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("failed to read events: %w", err),
				ErrCodeInternalError,
				"Check events file at ~/.config/ntm/analytics/events.jsonl",
			),
			Breakdown:  []TokenBreakdown{},
			AgentStats: make(map[string]AgentTokenStats),
			ModelStats: make(map[string]ModelTokenStats),
		}, nil
	}

	// Aggregate statistics
	output := aggregateTokenStats(eventList, opts.Days, opts.Since, groupBy)
	output.RobotResponse = NewRobotResponse(true)
	output.GeneratedAt = time.Now().UTC()
	output.GroupBy = groupBy
	output.Period = formatPeriod(opts.Days, opts.Since)

	// Generate hints
	output.AgentHints = generateTokenHints(output)

	return &output, nil
}

// PrintTokens outputs token usage statistics as JSON.
// This is a thin wrapper around GetTokens() for CLI output.
func PrintTokens(opts TokensOptions) error {
	output, err := GetTokens(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// readTokenEvents reads and filters events for token analysis
func readTokenEvents(path string, cutoff time.Time, sessionFilter, agentFilter string) ([]events.Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var result []events.Event
	lines := splitJSONLines(data)

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		var event events.Event
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}

		// Filter by time
		if !event.Timestamp.After(cutoff) {
			continue
		}

		// Filter by session
		if sessionFilter != "" && event.Session != sessionFilter {
			continue
		}

		// Filter by agent type (for prompt events)
		if agentFilter != "" && event.Type == events.EventPromptSend {
			if targetTypes, ok := event.Data["target_types"].(string); ok {
				if !strings.Contains(strings.ToLower(targetTypes), strings.ToLower(agentFilter)) {
					continue
				}
			}
		}

		result = append(result, event)
	}

	return result, nil
}

// splitJSONLines splits byte data into lines
func splitJSONLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

// aggregateTokenStats aggregates token statistics from events
func aggregateTokenStats(eventList []events.Event, days int, since, groupBy string) TokensOutput {
	output := TokensOutput{
		Breakdown:  make([]TokenBreakdown, 0),
		AgentStats: make(map[string]AgentTokenStats),
		ModelStats: make(map[string]ModelTokenStats),
		TimeStats:  make([]TimeTokenStats, 0),
	}

	// Track aggregations
	agentTokens := make(map[string]int)
	agentPrompts := make(map[string]int)
	agentChars := make(map[string]int)
	agentSpawns := make(map[string]int)
	agentModels := make(map[string]map[string]int) // agent -> model -> tokens

	modelTokens := make(map[string]int)
	modelPrompts := make(map[string]int)
	modelChars := make(map[string]int)
	modelAgentType := make(map[string]string)

	timeTokens := make(map[string]int)
	timePrompts := make(map[string]int)
	timeChars := make(map[string]int)

	// Process events
	for _, event := range eventList {
		switch event.Type {
		case events.EventSessionCreate:
			// Count agent spawns
			if cc, ok := event.Data["claude_count"].(float64); ok && cc > 0 {
				agentSpawns["claude"] += int(cc)
			}
			if cod, ok := event.Data["codex_count"].(float64); ok && cod > 0 {
				agentSpawns["codex"] += int(cod)
			}
			if gmi, ok := event.Data["gemini_count"].(float64); ok && gmi > 0 {
				agentSpawns["gemini"] += int(gmi)
			}
			if cursor, ok := event.Data["cursor_count"].(float64); ok && cursor > 0 {
				agentSpawns["cursor"] += int(cursor)
			}
			if windsurf, ok := event.Data["windsurf_count"].(float64); ok && windsurf > 0 {
				agentSpawns["windsurf"] += int(windsurf)
			}
			if aider, ok := event.Data["aider_count"].(float64); ok && aider > 0 {
				agentSpawns["aider"] += int(aider)
			}

		case events.EventAgentSpawn:
			// Track model info
			agentType, _ := event.Data["agent_type"].(string)
			model, _ := event.Data["model"].(string)
			if agentType != "" && model != "" {
				if agentModels[agentType] == nil {
					agentModels[agentType] = make(map[string]int)
				}
				modelAgentType[model] = agentType
			}

		case events.EventPromptSend:
			// Get token count
			var tokens int
			if t, ok := event.Data["estimated_tokens"].(float64); ok {
				tokens = int(t)
			} else if length, ok := event.Data["prompt_length"].(float64); ok {
				// Fallback: ~3.5 chars/token
				tokens = tokenpkg.EstimateTokensFromLength(int(length))
			}

			var chars int
			if c, ok := event.Data["prompt_length"].(float64); ok {
				chars = int(c)
			}

			output.TotalTokens += tokens
			output.TotalPrompts++
			output.TotalCharacters += chars

			// Get target types for agent breakdown
			targetTypes := []string{}
			if tt, ok := event.Data["target_types"].(string); ok {
				targetTypes = parseAgentTypes(tt)
			}
			if len(targetTypes) == 0 {
				targetTypes = []string{"unknown"}
			}

			// Divide among targets to avoid double-counting
			tokensPerTarget := tokens / len(targetTypes)
			charsPerTarget := chars / len(targetTypes)

			for _, agent := range targetTypes {
				agentTokens[agent] += tokensPerTarget
				agentPrompts[agent]++
				agentChars[agent] += charsPerTarget

				// Get model if available
				if model, ok := event.Data["model"].(string); ok && model != "" {
					modelTokens[model] += tokensPerTarget
					modelPrompts[model]++
					modelChars[model] += charsPerTarget
					if agentModels[agent] == nil {
						agentModels[agent] = make(map[string]int)
					}
					agentModels[agent][model] += tokensPerTarget
					modelAgentType[model] = agent
				}
			}

			// Time-based aggregation
			timeKey := formatTimeKey(event.Timestamp, groupBy)
			timeTokens[timeKey] += tokens
			timePrompts[timeKey]++
			timeChars[timeKey] += chars
		}
	}

	// Build agent stats
	for agent, tokens := range agentTokens {
		stats := AgentTokenStats{
			Spawned:    agentSpawns[agent],
			Prompts:    agentPrompts[agent],
			Tokens:     tokens,
			Characters: agentChars[agent],
			Models:     agentModels[agent],
		}
		output.AgentStats[agent] = stats
	}

	// Build model stats
	for model, tokens := range modelTokens {
		output.ModelStats[model] = ModelTokenStats{
			AgentType:  modelAgentType[model],
			Prompts:    modelPrompts[model],
			Tokens:     tokens,
			Characters: modelChars[model],
		}
	}

	// Build time stats
	timeKeys := make([]string, 0, len(timeTokens))
	for k := range timeTokens {
		timeKeys = append(timeKeys, k)
	}
	sort.Strings(timeKeys)
	for _, key := range timeKeys {
		output.TimeStats = append(output.TimeStats, TimeTokenStats{
			Period:     key,
			Tokens:     timeTokens[key],
			Prompts:    timePrompts[key],
			Characters: timeChars[key],
		})
	}

	// Build breakdown based on groupBy
	switch groupBy {
	case "agent":
		for agent, tokens := range agentTokens {
			pct := 0.0
			if output.TotalTokens > 0 {
				pct = float64(tokens) / float64(output.TotalTokens) * 100
			}
			output.Breakdown = append(output.Breakdown, TokenBreakdown{
				Key:        agent,
				Tokens:     tokens,
				Prompts:    agentPrompts[agent],
				Characters: agentChars[agent],
				Percentage: pct,
			})
		}
	case "model":
		for model, tokens := range modelTokens {
			pct := 0.0
			if output.TotalTokens > 0 {
				pct = float64(tokens) / float64(output.TotalTokens) * 100
			}
			output.Breakdown = append(output.Breakdown, TokenBreakdown{
				Key:        model,
				Tokens:     tokens,
				Prompts:    modelPrompts[model],
				Characters: modelChars[model],
				Percentage: pct,
			})
		}
	case "day", "week", "month":
		for _, ts := range output.TimeStats {
			pct := 0.0
			if output.TotalTokens > 0 {
				pct = float64(ts.Tokens) / float64(output.TotalTokens) * 100
			}
			output.Breakdown = append(output.Breakdown, TokenBreakdown{
				Key:        ts.Period,
				Tokens:     ts.Tokens,
				Prompts:    ts.Prompts,
				Characters: ts.Characters,
				Percentage: pct,
			})
		}
	}

	// Sort breakdown by tokens descending
	sort.Slice(output.Breakdown, func(i, j int) bool {
		return output.Breakdown[i].Tokens > output.Breakdown[j].Tokens
	})

	return output
}

// parseAgentTypes extracts agent type names from target_types string
func parseAgentTypes(targets string) []string {
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
	if strings.Contains(targets, "cursor") {
		result = append(result, "cursor")
	}
	if strings.Contains(targets, "windsurf") {
		result = append(result, "windsurf")
	}
	if strings.Contains(targets, "aider") {
		result = append(result, "aider")
	}
	if strings.Contains(targets, "all") || strings.Contains(targets, "agents") {
		if len(result) == 0 {
			result = []string{"claude", "codex", "gemini"}
		}
	}

	return result
}

// formatTimeKey formats timestamp based on grouping
func formatTimeKey(t time.Time, groupBy string) string {
	switch groupBy {
	case "day":
		return t.Format("2006-01-02")
	case "week":
		year, week := t.ISOWeek()
		return fmt.Sprintf("%d-W%02d", year, week)
	case "month":
		return t.Format("2006-01")
	default:
		return t.Format("2006-01-02")
	}
}

// formatPeriod creates human-readable period description
func formatPeriod(days int, since string) string {
	if since != "" {
		return fmt.Sprintf("Since %s", since)
	}
	return fmt.Sprintf("Last %d days", days)
}

// generateTokenHints creates actionable hints for AI agents
func generateTokenHints(output TokensOutput) *TokensAgentHints {
	hints := &TokensAgentHints{}

	// Build summary
	if output.TotalTokens == 0 {
		hints.Summary = "No token usage recorded in this period"
	} else {
		hints.Summary = fmt.Sprintf("%s tokens across %d prompts",
			formatTokens(output.TotalTokens), output.TotalPrompts)

		// Find highest consumer
		if len(output.Breakdown) > 0 {
			top := output.Breakdown[0]
			hints.Summary += fmt.Sprintf("; highest: %s (%.1f%%)", top.Key, top.Percentage)
		}
	}

	// Suggested commands
	hints.SuggestedCommands = []string{
		"ntm --robot-tokens --group-by=model",
		"ntm --robot-tokens --group-by=day --days=7",
		"ntm analytics --format=json",
	}

	// Warnings
	if output.TotalTokens > 1000000 {
		hints.Warnings = append(hints.Warnings, "High token usage detected (>1M tokens)")
	}

	// Check for imbalanced usage
	if len(output.AgentStats) > 1 {
		var maxTokens, minTokens int
		for _, stats := range output.AgentStats {
			if maxTokens == 0 || stats.Tokens > maxTokens {
				maxTokens = stats.Tokens
			}
			if minTokens == 0 || stats.Tokens < minTokens {
				minTokens = stats.Tokens
			}
		}
		if maxTokens > 0 && minTokens > 0 && float64(maxTokens)/float64(minTokens) > 10 {
			hints.Warnings = append(hints.Warnings, "Highly imbalanced token usage across agent types")
		}
	}

	return hints
}

// formatTokens formats token count with K/M suffix
func formatTokens(tokens int) string {
	if tokens >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(tokens)/1000000)
	}
	if tokens >= 1000 {
		return fmt.Sprintf("%.1fK", float64(tokens)/1000)
	}
	return fmt.Sprintf("%d", tokens)
}

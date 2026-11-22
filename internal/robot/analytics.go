package robot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	analyticsSchemaID      = "ntm:robot:analytics:v1"
	analyticsSchemaVersion = "1.0.0"
	defaultAnalyticsDays   = 30
)

// AnalyticsOptions configures multi-harness event analytics aggregation.
type AnalyticsOptions struct {
	Days     int    // show last N days (default 30 when <= 0 and Since == "")
	Since    string // optional YYYY-MM-DD or RFC3339 cutoff
	Sessions bool   // include per-session breakdown
	LogPath  string // optional explicit events JSONL path (defaults to events.DefaultOptions().Path)
}

// AnalyticsAgentStats holds per-harness spawn, prompt, character, and token statistics.
type AnalyticsAgentStats struct {
	Count     int `json:"count"`
	Prompts   int `json:"prompts"`
	CharsSent int `json:"chars_sent"`
	TokensEst int `json:"tokens_estimated"`
}

// AnalyticsSessionSummary provides aggregated activity details about a single session.
type AnalyticsSessionSummary struct {
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"created_at"`
	AgentCount  int       `json:"agent_count"`
	PromptCount int       `json:"prompt_count"`
}

// AnalyticsOutput represents the structured JSON envelope for --robot-analytics.
type AnalyticsOutput struct {
	RobotResponse
	SchemaID       string                         `json:"schema_id"`
	SchemaVersion  string                         `json:"schema_version"`
	Period         string                         `json:"period"`
	Since          string                         `json:"since"`
	Days           int                            `json:"days,omitempty"`
	TotalSessions  int                            `json:"total_sessions"`
	TotalAgents    int                            `json:"total_agents"`
	TotalPrompts   int                            `json:"total_prompts"`
	TotalCharsSent int                            `json:"total_chars_sent"`
	TotalTokensEst int                            `json:"total_tokens_estimated"`
	AgentBreakdown map[string]AnalyticsAgentStats `json:"agent_breakdown"`
	Sessions       []AnalyticsSessionSummary      `json:"sessions,omitempty"`
	ErrorCount     int                            `json:"error_count"`
	ErrorTypes     map[string]int                 `json:"error_types"`
	AgentCrashes   int                            `json:"agent_crashes"`
	AgentRestarts  int                            `json:"agent_restarts"`
	AgentHints     *AgentHints                    `json:"_agent_hints,omitempty"`
}

type sessionCreateHarnessField struct {
	primaryKey string
	aliasKey   string
	agentType  string
}

var analyticsSessionCreateHarnesses = []sessionCreateHarnessField{
	{primaryKey: "claude_count", agentType: "claude"},
	{primaryKey: "codex_count", agentType: "codex"},
	{primaryKey: "gemini_count", agentType: "gemini"},
	{primaryKey: "antigravity_count", aliasKey: "agy_count", agentType: "antigravity"},
	{primaryKey: "grok_count", agentType: "grok"},
	{primaryKey: "cursor_count", agentType: "cursor"},
	{primaryKey: "windsurf_count", agentType: "windsurf"},
	{primaryKey: "aider_count", agentType: "aider"},
	{primaryKey: "opencode_count", aliasKey: "oc_count", agentType: "oc"},
	{primaryKey: "omp_count", agentType: "omp"},
	{primaryKey: "ollama_count", agentType: "ollama"},
}

// GetAnalytics aggregates multi-harness session, prompt, token, crash, restart,
// and error analytics from the JSONL event log.
func GetAnalytics(ctx context.Context, opts AnalyticsOptions) (*AnalyticsOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return newAnalyticsErrorOutput(opts.Sessions, err, ErrCodeTimeout, "Retry --robot-analytics with an active context"), nil
	}

	if opts.Days < 0 {
		return newAnalyticsErrorOutput(
			opts.Sessions,
			fmt.Errorf("--analytics-days must be zero or greater (got %d)", opts.Days),
			ErrCodeInvalidFlag,
			"Pass a non-negative --analytics-days such as --analytics-days=30",
		), nil
	}

	since := strings.TrimSpace(opts.Since)
	days := opts.Days
	var cutoff time.Time

	if since != "" {
		parsed, err := parseAnalyticsSince(since)
		if err != nil {
			return newAnalyticsErrorOutput(
				opts.Sessions,
				err,
				ErrCodeInvalidFlag,
				"Use YYYY-MM-DD (e.g. 2026-01-15) or RFC3339 (e.g. 2026-01-15T00:00:00Z)",
			), nil
		}
		cutoff = parsed
		days = 0
	} else {
		if days <= 0 {
			days = defaultAnalyticsDays
		}
		cutoff = time.Now().UTC().AddDate(0, 0, -days)
	}

	logPath := strings.TrimSpace(opts.LogPath)
	if logPath == "" {
		logPath = events.DefaultOptions().Path
	} else {
		logPath = util.ExpandPath(logPath)
	}

	eventList, err := events.ReadSince(logPath, cutoff)
	if err != nil {
		if os.IsNotExist(err) {
			period := fmt.Sprintf("Last %d days (no events)", days)
			if since != "" {
				period = fmt.Sprintf("Since %s (no events)", since)
			}
			out := &AnalyticsOutput{
				RobotResponse:  NewRobotResponse(true),
				SchemaID:       analyticsSchemaID,
				SchemaVersion:  analyticsSchemaVersion,
				Period:         period,
				Since:          cutoff.UTC().Format(time.RFC3339),
				Days:           days,
				AgentBreakdown: map[string]AnalyticsAgentStats{},
				ErrorTypes:     map[string]int{},
			}
			if opts.Sessions {
				out.Sessions = []AnalyticsSessionSummary{}
			}
			out.AgentHints = buildAnalyticsAgentHints(out, opts.Sessions, true)
			return out, nil
		}
		return newAnalyticsErrorOutput(
			opts.Sessions,
			fmt.Errorf("reading events: %w", err),
			ErrCodeInternalError,
			"Check events file permissions and path at ~/.config/ntm/analytics/events.jsonl",
		), nil
	}

	out := AggregateAnalyticsEvents(eventList, days, since, cutoff)
	out.RobotResponse = NewRobotResponse(true)
	out.SchemaID = analyticsSchemaID
	out.SchemaVersion = analyticsSchemaVersion
	out.Since = cutoff.UTC().Format(time.RFC3339)
	out.Days = days
	if opts.Sessions {
		out.Sessions = BuildAnalyticsSessionDetails(eventList)
	} else {
		out.Sessions = nil
	}
	out.AgentHints = buildAnalyticsAgentHints(&out, opts.Sessions, len(eventList) == 0)

	return &out, nil
}

// PrintAnalytics executes GetAnalytics and writes the terminal JSON/TOON response.
func PrintAnalytics(ctx context.Context, opts AnalyticsOptions) error {
	out, err := GetAnalytics(ctx, opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot analytics failed")
}

// AggregateAnalyticsEvents computes multi-harness analytics statistics from a slice of events.
func AggregateAnalyticsEvents(eventList []events.Event, days int, since string, cutoff time.Time) AnalyticsOutput {
	out := AnalyticsOutput{
		SchemaID:       analyticsSchemaID,
		SchemaVersion:  analyticsSchemaVersion,
		Since:          cutoff.UTC().Format(time.RFC3339),
		AgentBreakdown: map[string]AnalyticsAgentStats{},
		ErrorTypes:     map[string]int{},
	}

	if since != "" {
		out.Period = fmt.Sprintf("Since %s", since)
	} else {
		out.Period = fmt.Sprintf("Last %d days", days)
		out.Days = days
	}

	for _, event := range eventList {
		switch event.Type {
		case events.EventSessionCreate:
			out.TotalSessions++
			for _, field := range analyticsSessionCreateHarnesses {
				if count := harnessSpawnCount(event.Data, field); count > 0 {
					out.TotalAgents += count
					updateAnalyticsAgentStats(out.AgentBreakdown, field.agentType, count, 0, 0, 0)
				}
			}

		case events.EventAgentSpawn:
			// Agents spawned during session creation are already accounted for in EventSessionCreate.

		case events.EventAgentAdd:
			// Agents registered after session creation via `ntm add` are NOT covered
			// by session_create provider counts.
			if agentType, ok := event.Data["agent_type"].(string); ok {
				normalized := ResolveAgentType(agentType)
				if isAnalyticsAgentType(normalized) {
					out.TotalAgents++
					updateAnalyticsAgentStats(out.AgentBreakdown, normalized, 1, 0, 0, 0)
				}
			}

		case events.EventPromptSend:
			out.TotalPrompts++

			var chars int
			if length, ok := eventIntValue(event.Data, "prompt_length"); ok && length > 0 {
				chars = length
				out.TotalCharsSent += chars
			}

			var tokenEst int
			if tokens, ok := eventIntValue(event.Data, "estimated_tokens"); ok && tokens > 0 {
				tokenEst = tokens
			} else if chars > 0 {
				// Fallback: estimate from prompt_length (~3.5 chars/token)
				tokenEst = chars * 10 / 35
			}
			out.TotalTokensEst += tokenEst

			if targetTypes, ok := event.Data["target_types"].(string); ok {
				targets := ParseAnalyticsTargetTypes(targetTypes)
				if len(targets) > 0 {
					tokensPerTarget := tokenEst / len(targets)
					charsPerTarget := chars / len(targets)
					charsRemainder := chars % len(targets)
					for i, t := range targets {
						charsDelta := charsPerTarget
						if i == 0 {
							charsDelta += charsRemainder
						}
						updateAnalyticsAgentStats(out.AgentBreakdown, t, 0, 1, charsDelta, tokensPerTarget)
					}
				}
			}

		case events.EventError:
			out.ErrorCount++
			if errType, ok := event.Data["error_type"].(string); ok && errType != "" {
				out.ErrorTypes[errType]++
			}

		case events.EventAgentCrash:
			out.AgentCrashes++

		case events.EventAgentRestart:
			out.AgentRestarts++
		}
	}

	return out
}

// BuildAnalyticsSessionDetails creates deterministic per-session summaries ordered most-recent first.
func BuildAnalyticsSessionDetails(eventList []events.Event) []AnalyticsSessionSummary {
	sessionMap := make(map[string]*AnalyticsSessionSummary)

	for _, event := range eventList {
		if event.Session == "" {
			continue
		}

		summary, exists := sessionMap[event.Session]
		if !exists {
			summary = &AnalyticsSessionSummary{
				Name:      event.Session,
				CreatedAt: event.Timestamp,
			}
			sessionMap[event.Session] = summary
		}

		switch event.Type {
		case events.EventSessionCreate:
			summary.CreatedAt = event.Timestamp
			for _, field := range analyticsSessionCreateHarnesses {
				if count := harnessSpawnCount(event.Data, field); count > 0 {
					summary.AgentCount += count
				}
			}

		case events.EventAgentAdd:
			if agentType, ok := event.Data["agent_type"].(string); ok && strings.TrimSpace(agentType) != "" {
				if isAnalyticsAgentType(ResolveAgentType(agentType)) {
					summary.AgentCount++
				}
			} else {
				summary.AgentCount++
			}

		case events.EventPromptSend:
			summary.PromptCount++
		}
	}

	result := make([]AnalyticsSessionSummary, 0, len(sessionMap))
	for _, s := range sessionMap {
		result = append(result, *s)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].CreatedAt.After(result[j].CreatedAt)
		}
		return result[i].Name < result[j].Name
	})

	return result
}

// ParseAnalyticsTargetTypes parses a comma-separated target_types string into canonical harness names.
func ParseAnalyticsTargetTypes(targets string) []string {
	legacyAll := false
	seen := make(map[string]struct{})
	result := make([]string, 0)

	for _, part := range strings.Split(strings.ToLower(targets), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		switch {
		case part == "all" || part == "agents" || part == "all-agents":
			legacyAll = true
			continue
		case strings.HasPrefix(part, "pane:"):
			continue
		case strings.HasPrefix(part, "tags:["):
			continue
		}
		normalized := ResolveAgentType(part)
		if !isAnalyticsAgentType(normalized) {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}

	if legacyAll && len(result) == 0 {
		return []string{"claude", "codex", "gemini", "grok"}
	}
	return result
}

// IsAnalyticsAgentType reports whether agentType is a canonical analytics-tracked harness.
func IsAnalyticsAgentType(agentType string) bool {
	return isAnalyticsAgentType(agentType)
}

func isAnalyticsAgentType(agentType string) bool {
	switch agentType {
	case "claude", "codex", "gemini", "antigravity", "grok", "cursor", "windsurf", "aider", "oc", "omp", "pi", "ollama":
		return true
	default:
		return false
	}
}

// FormatAnalyticsPrometheus renders AnalyticsOutput in deterministic Prometheus exposition format.
func FormatAnalyticsPrometheus(out *AnalyticsOutput) string {
	if out == nil {
		return ""
	}

	var b strings.Builder

	fmt.Fprintln(&b, "# HELP ntm_sessions_total Total sessions in the analytics period.")
	fmt.Fprintln(&b, "# TYPE ntm_sessions_total gauge")
	fmt.Fprintf(&b, "ntm_sessions_total{period=%q} %d\n\n", out.Period, out.TotalSessions)

	fmt.Fprintln(&b, "# HELP ntm_agents_total Total agents spawned in the analytics period.")
	fmt.Fprintln(&b, "# TYPE ntm_agents_total gauge")
	fmt.Fprintf(&b, "ntm_agents_total{period=%q} %d\n\n", out.Period, out.TotalAgents)

	fmt.Fprintln(&b, "# HELP ntm_prompts_total Total prompts sent in the analytics period.")
	fmt.Fprintln(&b, "# TYPE ntm_prompts_total gauge")
	fmt.Fprintf(&b, "ntm_prompts_total{period=%q} %d\n\n", out.Period, out.TotalPrompts)

	fmt.Fprintln(&b, "# HELP ntm_chars_sent_total Total characters sent in the analytics period.")
	fmt.Fprintln(&b, "# TYPE ntm_chars_sent_total gauge")
	fmt.Fprintf(&b, "ntm_chars_sent_total{period=%q} %d\n\n", out.Period, out.TotalCharsSent)

	fmt.Fprintln(&b, "# HELP ntm_tokens_estimated_total Estimated total tokens in the analytics period.")
	fmt.Fprintln(&b, "# TYPE ntm_tokens_estimated_total gauge")
	fmt.Fprintf(&b, "ntm_tokens_estimated_total{period=%q} %d\n\n", out.Period, out.TotalTokensEst)

	fmt.Fprintln(&b, "# HELP ntm_errors_total Total errors in the analytics period.")
	fmt.Fprintln(&b, "# TYPE ntm_errors_total gauge")
	fmt.Fprintf(&b, "ntm_errors_total{period=%q} %d\n\n", out.Period, out.ErrorCount)

	fmt.Fprintln(&b, "# HELP ntm_agent_crashes_total Total agent crashes in the analytics period.")
	fmt.Fprintln(&b, "# TYPE ntm_agent_crashes_total gauge")
	fmt.Fprintf(&b, "ntm_agent_crashes_total{period=%q} %d\n\n", out.Period, out.AgentCrashes)

	fmt.Fprintln(&b, "# HELP ntm_agent_restarts_total Total agent restarts in the analytics period.")
	fmt.Fprintln(&b, "# TYPE ntm_agent_restarts_total gauge")
	fmt.Fprintf(&b, "ntm_agent_restarts_total{period=%q} %d\n\n", out.Period, out.AgentRestarts)

	if len(out.AgentBreakdown) > 0 {
		agentTypes := make([]string, 0, len(out.AgentBreakdown))
		for agentType := range out.AgentBreakdown {
			agentTypes = append(agentTypes, agentType)
		}
		sort.Strings(agentTypes)

		fmt.Fprintln(&b, "# HELP ntm_agent_spawns Agent spawn count by type.")
		fmt.Fprintln(&b, "# TYPE ntm_agent_spawns gauge")
		for _, agentType := range agentTypes {
			as := out.AgentBreakdown[agentType]
			fmt.Fprintf(&b, "ntm_agent_spawns{period=%q,agent_type=%q} %d\n", out.Period, agentType, as.Count)
		}
		fmt.Fprintln(&b)

		fmt.Fprintln(&b, "# HELP ntm_agent_prompts Prompts sent by agent type.")
		fmt.Fprintln(&b, "# TYPE ntm_agent_prompts gauge")
		for _, agentType := range agentTypes {
			as := out.AgentBreakdown[agentType]
			fmt.Fprintf(&b, "ntm_agent_prompts{period=%q,agent_type=%q} %d\n", out.Period, agentType, as.Prompts)
		}
		fmt.Fprintln(&b)

		fmt.Fprintln(&b, "# HELP ntm_agent_chars_sent Characters sent by agent type.")
		fmt.Fprintln(&b, "# TYPE ntm_agent_chars_sent gauge")
		for _, agentType := range agentTypes {
			as := out.AgentBreakdown[agentType]
			fmt.Fprintf(&b, "ntm_agent_chars_sent{period=%q,agent_type=%q} %d\n", out.Period, agentType, as.CharsSent)
		}
		fmt.Fprintln(&b)

		fmt.Fprintln(&b, "# HELP ntm_agent_tokens_estimated Estimated tokens by agent type.")
		fmt.Fprintln(&b, "# TYPE ntm_agent_tokens_estimated gauge")
		for _, agentType := range agentTypes {
			as := out.AgentBreakdown[agentType]
			fmt.Fprintf(&b, "ntm_agent_tokens_estimated{period=%q,agent_type=%q} %d\n", out.Period, agentType, as.TokensEst)
		}
		fmt.Fprintln(&b)
	}

	if len(out.ErrorTypes) > 0 {
		errTypes := make([]string, 0, len(out.ErrorTypes))
		for errType := range out.ErrorTypes {
			errTypes = append(errTypes, errType)
		}
		sort.Strings(errTypes)

		fmt.Fprintln(&b, "# HELP ntm_errors_by_type Error count by type.")
		fmt.Fprintln(&b, "# TYPE ntm_errors_by_type gauge")
		for _, errType := range errTypes {
			fmt.Fprintf(&b, "ntm_errors_by_type{period=%q,error_type=%q} %d\n", out.Period, errType, out.ErrorTypes[errType])
		}
		fmt.Fprintln(&b)
	}

	return b.String()
}

func newAnalyticsErrorOutput(includeSessions bool, err error, code, hint string) *AnalyticsOutput {
	out := &AnalyticsOutput{
		RobotResponse:  NewErrorResponse(err, code, hint),
		SchemaID:       analyticsSchemaID,
		SchemaVersion:  analyticsSchemaVersion,
		AgentBreakdown: map[string]AnalyticsAgentStats{},
		ErrorTypes:     map[string]int{},
	}
	if includeSessions {
		out.Sessions = []AnalyticsSessionSummary{}
	}
	return out
}

func parseAnalyticsSince(raw string) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if parsed, err := time.Parse("2006-01-02", trimmed); err == nil {
		return parsed.UTC(), nil
	}
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return parsed.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid --since format %q (expected YYYY-MM-DD or RFC3339)", raw)
}

func harnessSpawnCount(data map[string]any, field sessionCreateHarnessField) int {
	count := eventIntCount(data, field.primaryKey)
	if field.aliasKey != "" && count == 0 {
		count = eventIntCount(data, field.aliasKey)
	}
	return count
}

func eventIntCount(data map[string]any, key string) int {
	val, _ := eventIntValue(data, key)
	return val
}

func eventIntValue(data map[string]any, key string) (int, bool) {
	if data == nil {
		return 0, false
	}
	raw, exists := data[key]
	if !exists || raw == nil {
		return 0, false
	}
	switch v := raw.(type) {
	case float64:
		return int(v), true
	case float32:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	case int32:
		return int(v), true
	case uint:
		return int(v), true
	case uint64:
		return int(v), true
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i), true
		}
		if f, err := v.Float64(); err == nil {
			return int(f), true
		}
		return 0, false
	default:
		return 0, false
	}
}

func updateAnalyticsAgentStats(breakdown map[string]AnalyticsAgentStats, agentType string, countDelta, promptsDelta, charsDelta, tokensDelta int) {
	current := breakdown[agentType]
	current.Count += countDelta
	current.Prompts += promptsDelta
	current.CharsSent += charsDelta
	current.TokensEst += tokensDelta
	breakdown[agentType] = current
}

func buildAnalyticsAgentHints(out *AnalyticsOutput, sessionsIncluded bool, noEvents bool) *AgentHints {
	if noEvents {
		return &AgentHints{
			Summary: fmt.Sprintf("No analytics events found for %s", out.Period),
			SuggestedActions: []RobotAction{
				{
					Action:   "spawn_session",
					Target:   "myproject",
					Reason:   "Spawn or send prompts to a session so NTM records analytics events",
					Priority: 1,
				},
			},
		}
	}

	hints := &AgentHints{
		Summary: fmt.Sprintf(
			"%s: %d session(s), %d agent(s), %d prompt(s) (~%d tokens), %d error(s), %d crash(es)",
			out.Period,
			out.TotalSessions,
			out.TotalAgents,
			out.TotalPrompts,
			out.TotalTokensEst,
			out.ErrorCount,
			out.AgentCrashes,
		),
	}

	if !sessionsIncluded && out.TotalSessions > 0 {
		hints.SuggestedActions = append(hints.SuggestedActions, RobotAction{
			Action:   "inspect_session_analytics",
			Target:   "--analytics-sessions",
			Reason:   "Re-run with --analytics-sessions to include per-session breakdown",
			Priority: 2,
		})
	}
	if out.ErrorCount > 0 || out.AgentCrashes > 0 {
		hints.Warnings = append(hints.Warnings, fmt.Sprintf("Observed %d error(s) and %d agent crash(es) in %s", out.ErrorCount, out.AgentCrashes, out.Period))
	}

	return hints
}

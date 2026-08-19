// Package context provides context window monitoring for AI agent orchestration.
// summary.go implements handoff summary generation for context window rotation.
package context

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// HandoffSummary contains the information needed to continue work in a new agent.
type HandoffSummary struct {
	GeneratedAt   time.Time `json:"generated_at"`
	OldAgentID    string    `json:"old_agent_id"`
	OldAgentType  string    `json:"old_agent_type,omitempty"`
	SessionName   string    `json:"session_name,omitempty"`
	CurrentTask   string    `json:"current_task"`
	Progress      string    `json:"progress"`
	KeyDecisions  []string  `json:"key_decisions,omitempty"`
	ActiveFiles   []string  `json:"active_files,omitempty"`
	Blockers      []string  `json:"blockers,omitempty"`
	RawSummary    string    `json:"raw_summary"`
	TokenEstimate int       `json:"token_estimate"`
}

// SummaryGenerator handles the generation of handoff summaries.
type SummaryGenerator struct {
	maxTokens     int
	promptTimeout time.Duration
}

// SummaryGeneratorConfig holds configuration for the SummaryGenerator.
type SummaryGeneratorConfig struct {
	MaxTokens     int           // Maximum tokens for summary (default: 2000)
	PromptTimeout time.Duration // Timeout for agent response (default: 30s)
}

// NewSummaryGenerator creates a new SummaryGenerator with the given config.
func NewSummaryGenerator(cfg SummaryGeneratorConfig) *SummaryGenerator {
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 2000
	}
	if cfg.PromptTimeout <= 0 {
		cfg.PromptTimeout = 30 * time.Second
	}
	return &SummaryGenerator{
		maxTokens:     cfg.MaxTokens,
		promptTimeout: cfg.PromptTimeout,
	}
}

// SummaryPromptTemplate is the template for requesting a handoff summary from an agent.
const SummaryPromptTemplate = `CONTEXT ROTATION - HANDOFF SUMMARY REQUIRED

Your context window is approaching capacity. Please provide a brief handoff summary (max 500 words) that will be passed to a fresh agent to continue your work.

Include the following sections:

## CURRENT TASK
What task are you currently working on?

## PROGRESS
- What have you accomplished so far?
- What still needs to be done?

## KEY DECISIONS
List any important technical decisions or choices made.

## ACTIVE FILES
List the files you are currently modifying or have modified.

## BLOCKERS
Note any problems, blockers, or issues the next agent should be aware of.

Please format your response with the section headers above so it can be parsed.`

// GeneratePrompt returns the prompt to send to an agent for summary generation.
func (g *SummaryGenerator) GeneratePrompt() string {
	return SummaryPromptTemplate
}

// ParseAgentResponse parses an agent's response to the summary prompt.
func (g *SummaryGenerator) ParseAgentResponse(agentID, agentType, sessionName, response string) *HandoffSummary {
	summary := &HandoffSummary{
		GeneratedAt:  time.Now(),
		OldAgentID:   agentID,
		OldAgentType: agentType,
		SessionName:  sessionName,
		RawSummary:   response,
	}

	// Parse sections from the response
	summary.CurrentTask = extractSection(response, "CURRENT TASK")
	summary.Progress = extractSection(response, "PROGRESS")
	summary.KeyDecisions = extractListSection(response, "KEY DECISIONS")
	summary.ActiveFiles = extractListSection(response, "ACTIVE FILES")
	summary.Blockers = extractListSection(response, "BLOCKERS")

	// Estimate token count (rough: ~4 chars per token)
	summary.TokenEstimate = len(response) / 4

	// Truncate if needed
	if summary.TokenEstimate > g.maxTokens {
		summary.RawSummary = truncateToTokens(response, g.maxTokens)
		summary.TokenEstimate = g.maxTokens
	}

	return summary
}

// GenerateFallbackSummary creates a summary from recent agent output when
// the agent doesn't respond to the summary request.
func (g *SummaryGenerator) GenerateFallbackSummary(
	agentID, agentType, sessionName string,
	recentOutput []string,
) *HandoffSummary {
	summary := &HandoffSummary{
		GeneratedAt:  time.Now(),
		OldAgentID:   agentID,
		OldAgentType: agentType,
		SessionName:  sessionName,
	}

	// Combine recent output
	combined := strings.Join(recentOutput, "\n")

	// Try to extract information heuristically
	summary.ActiveFiles = extractFilePaths(combined)
	summary.CurrentTask = extractLastTask(combined)

	// Build raw summary
	var sb strings.Builder
	sb.WriteString("## FALLBACK SUMMARY (Agent did not respond to summary request)\n\n")
	sb.WriteString("### Context\n")
	sb.WriteString(fmt.Sprintf("- Agent: %s (%s)\n", agentID, agentType))
	sb.WriteString(fmt.Sprintf("- Session: %s\n", sessionName))
	sb.WriteString(fmt.Sprintf("- Generated: %s\n\n", summary.GeneratedAt.Format(time.RFC3339)))

	if summary.CurrentTask != "" {
		sb.WriteString("### Last Detected Task\n")
		sb.WriteString(summary.CurrentTask)
		sb.WriteString("\n\n")
	}

	if len(summary.ActiveFiles) > 0 {
		sb.WriteString("### Detected Active Files\n")
		for _, f := range summary.ActiveFiles {
			sb.WriteString(fmt.Sprintf("- %s\n", f))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("### Recent Output (last messages)\n")
	// Include last few outputs, truncated
	maxOutputs := 5
	if len(recentOutput) < maxOutputs {
		maxOutputs = len(recentOutput)
	}
	for i := len(recentOutput) - maxOutputs; i < len(recentOutput); i++ {
		out := recentOutput[i]
		if len(out) > 500 {
			out = truncateAtRuneBoundary(out, 500) + "..."
		}
		sb.WriteString(fmt.Sprintf("```\n%s\n```\n\n", out))
	}

	summary.RawSummary = sb.String()
	summary.TokenEstimate = len(summary.RawSummary) / 4

	// Truncate if needed
	if summary.TokenEstimate > g.maxTokens {
		summary.RawSummary = truncateToTokens(summary.RawSummary, g.maxTokens)
		summary.TokenEstimate = g.maxTokens
	}

	return summary
}

// FormatForNewAgent formats the summary as context for the new agent.
func (s *HandoffSummary) FormatForNewAgent() string {
	var sb strings.Builder

	sb.WriteString("## HANDOFF CONTEXT - CONTINUING FROM PREVIOUS AGENT\n\n")
	sb.WriteString("A previous agent session was rotated due to context window limits. ")
	sb.WriteString("Here is the handoff summary to help you continue the work:\n\n")

	if s.CurrentTask != "" {
		sb.WriteString("### Current Task\n")
		sb.WriteString(s.CurrentTask)
		sb.WriteString("\n\n")
	}

	if s.Progress != "" {
		sb.WriteString("### Progress\n")
		sb.WriteString(s.Progress)
		sb.WriteString("\n\n")
	}

	if len(s.KeyDecisions) > 0 {
		sb.WriteString("### Key Decisions Made\n")
		for _, d := range s.KeyDecisions {
			sb.WriteString(fmt.Sprintf("- %s\n", d))
		}
		sb.WriteString("\n")
	}

	if len(s.ActiveFiles) > 0 {
		sb.WriteString("### Active Files\n")
		for _, f := range s.ActiveFiles {
			sb.WriteString(fmt.Sprintf("- %s\n", f))
		}
		sb.WriteString("\n")
	}

	if len(s.Blockers) > 0 {
		sb.WriteString("### Blockers/Issues\n")
		for _, b := range s.Blockers {
			sb.WriteString(fmt.Sprintf("- %s\n", b))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("---\n")
	sb.WriteString("Please continue from where the previous agent left off.\n")

	return sb.String()
}

// extractSection extracts a section from the response by header name.
func extractSection(response, header string) string {
	// Try multiple header formats with (?s) to make . match newlines
	patterns := []string{
		fmt.Sprintf(`(?is)##\s*%s\s*\n(.*?)(?:\n##|\z)`, regexp.QuoteMeta(header)),
		fmt.Sprintf(`(?is)\*\*%s\*\*:?\s*\n?(.*?)(?:\n\*\*|\n##|\z)`, regexp.QuoteMeta(header)),
		fmt.Sprintf(`(?is)%s:?\s*\n(.*?)(?:\n[A-Z][A-Z]|\z)`, regexp.QuoteMeta(header)),
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(response); len(matches) > 1 {
			return strings.TrimSpace(matches[1])
		}
	}

	return ""
}

// extractListSection extracts a bulleted list section.
// numberedListPrefix matches numbered list prefixes like "1. ", "23. " etc.
var numberedListPrefix = regexp.MustCompile(`^\d+\.\s+`)

func extractListSection(response, header string) []string {
	section := extractSection(response, header)
	if section == "" {
		return nil
	}

	var items []string
	lines := strings.Split(section, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Remove bullet points
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimPrefix(line, "* ")
		line = strings.TrimPrefix(line, "• ")
		// Remove numbered list prefixes
		line = numberedListPrefix.ReplaceAllString(line, "")
		if line != "" {
			items = append(items, line)
		}
	}

	return items
}

var filePathRegexes = []*regexp.Regexp{
	regexp.MustCompile(`(?m)(?:^|\s)([a-zA-Z0-9_./\-]+\.[a-zA-Z]{1,10})(?:\s|$|:|,)`),        // file.ext
	regexp.MustCompile(`(?m)(?:^|\s)((?:[a-zA-Z0-9_\-]+/)+[a-zA-Z0-9_\-]+\.[a-zA-Z]{1,10})`), // path/to/file.ext
	regexp.MustCompile(`(?m)(?:^|\s)(internal/[a-zA-Z0-9_./\-]+)`),                           // internal/...
	regexp.MustCompile(`(?m)(?:^|\s)(cmd/[a-zA-Z0-9_./\-]+)`),                                // cmd/...
}

// extractFilePaths extracts file paths from text using common patterns.
func extractFilePaths(text string) []string {
	seen := make(map[string]bool)
	var files []string

	for _, re := range filePathRegexes {
		matches := re.FindAllStringSubmatch(text, -1)
		for _, match := range matches {
			if len(match) > 1 {
				path := strings.TrimSpace(match[1])
				// Filter out common false positives
				if isLikelyFilePath(path) && !seen[path] {
					seen[path] = true
					files = append(files, path)
				}
			}
		}
	}

	return files
}

// isLikelyFilePath returns true if the string looks like a file path.
func isLikelyFilePath(s string) bool {
	// Exclude common false positives
	excluded := []string{
		"http://", "https://", "ftp://",
		"0.0", "1.0", "2.0", "3.0",
		"v1.", "v2.", "v3.",
	}
	for _, ex := range excluded {
		if strings.Contains(s, ex) {
			return false
		}
	}

	// Must have a reasonable extension
	validExts := []string{
		".go", ".py", ".js", ".ts", ".tsx", ".jsx",
		".md", ".txt", ".json", ".yaml", ".yml", ".toml",
		".html", ".css", ".scss", ".sh", ".bash",
		".sql", ".proto", ".graphql",
	}
	for _, ext := range validExts {
		if strings.HasSuffix(s, ext) {
			return true
		}
	}

	// Or must have a path separator
	return strings.Contains(s, "/")
}

var lastTaskRegexes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:working on|implementing|fixing|adding|creating|updating)\s+(.+?)(?:\.|$)`),
	regexp.MustCompile(`(?i)(?:task|issue|feature|bug):\s*(.+?)(?:\n|$)`),
	regexp.MustCompile(`(?i)(?:TODO|DOING):\s*(.+?)(?:\n|$)`),
}

// extractLastTask attempts to extract the last task from recent output.
func extractLastTask(text string) string {
	for _, re := range lastTaskRegexes {
		if matches := re.FindStringSubmatch(text); len(matches) > 1 {
			task := strings.TrimSpace(matches[1])
			if len(task) > 200 {
				task = truncateAtRuneBoundary(task, 200) + "..."
			}
			return task
		}
	}

	return ""
}

// truncateToTokens truncates text to approximately the given token count.
func truncateToTokens(text string, maxTokens int) string {
	// Rough estimate: 4 chars per token
	maxChars := maxTokens * 4
	if len(text) <= maxChars {
		return text
	}

	// Try to truncate at a sentence boundary, respecting UTF-8
	truncated := truncateAtRuneBoundary(text, maxChars)
	lastPeriod := strings.LastIndex(truncated, ".")
	lastNewline := strings.LastIndex(truncated, "\n")

	cutPoint := len(truncated)
	if lastPeriod > cutPoint*3/4 {
		cutPoint = lastPeriod + 1
	} else if lastNewline > cutPoint*3/4 {
		cutPoint = lastNewline + 1
	}

	return text[:cutPoint] + "\n\n[Summary truncated due to token limit]"
}

// truncateAtRuneBoundary truncates a string to at most maxBytes bytes,
// ensuring the result ends at a valid UTF-8 rune boundary.
func truncateAtRuneBoundary(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Find the last rune boundary at or before maxBytes
	lastValid := 0
	for i := range s {
		if i > maxBytes {
			break
		}
		lastValid = i
	}
	return s[:lastValid]
}

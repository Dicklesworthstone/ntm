package status

import (
	"regexp"
	"strings"
)

// ansiEscapeRegex matches ANSI escape sequences for stripping
var ansiEscapeRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// PromptPattern defines a pattern for detecting idle state
type PromptPattern struct {
	// AgentType specifies which agent type this pattern applies to.
	// Empty string means it applies to all agent types.
	AgentType string
	// Regex is a compiled regular expression for matching (optional)
	Regex *regexp.Regexp
	// Literal is a simple string suffix to match (optional, faster than regex)
	Literal string
	// Description explains what this pattern matches (for debugging)
	Description string
}

// promptPatterns contains all known prompt patterns for agent types
var promptPatterns = []PromptPattern{
	// Claude Code patterns
	{AgentType: "cc", Regex: regexp.MustCompile(`(?i)claude>?\s*$`), Description: "Claude prompt"},
	{AgentType: "cc", Regex: regexp.MustCompile(`>\s*$`), Description: "Claude simple prompt"},

	// Codex CLI patterns
	{AgentType: "cod", Regex: regexp.MustCompile(`(?i)codex>?\s*$`), Description: "Codex prompt"},

	// Gemini CLI patterns
	{AgentType: "gmi", Regex: regexp.MustCompile(`(?i)gemini>?\s*$`), Description: "Gemini prompt"},

	// Cursor patterns
	{AgentType: "cursor", Regex: regexp.MustCompile(`(?i)cursor>?\s*$`), Description: "Cursor prompt"},

	// Windsurf patterns
	{AgentType: "windsurf", Regex: regexp.MustCompile(`(?i)windsurf>?\s*$`), Description: "Windsurf prompt"},

	// Aider patterns
	{AgentType: "aider", Regex: regexp.MustCompile(`(?i)aider>?\s*$`), Description: "Aider prompt"},
	{AgentType: "aider", Regex: regexp.MustCompile(`>\s*$`), Description: "Aider simple prompt"},

	// Generic shell prompts (for user panes and fallback)
	{AgentType: "user", Regex: regexp.MustCompile(`[$%>]\s*$`), Description: "Standard shell prompt"},
	{AgentType: "user", Regex: regexp.MustCompile(`❯\s*$`), Description: "Fancy shell prompt (starship, etc)"},
	{AgentType: "user", Regex: regexp.MustCompile(`\$\s*$`), Description: "Dollar prompt"},

	// Generic patterns (apply to all types as fallback)
	{AgentType: "", Regex: regexp.MustCompile(`>\s*$`), Description: "Generic > prompt"},
	{AgentType: "", Regex: regexp.MustCompile(`[$%]\s*$`), Description: "Generic shell prompt"},
}

// StripANSI removes ANSI escape sequences from a string
func StripANSI(s string) string {
	return ansiEscapeRegex.ReplaceAllString(s, "")
}

// IsPromptLine checks if a line looks like a prompt.
// agentType can be empty to match any agent type.
func IsPromptLine(line string, agentType string) bool {
	// Strip ANSI codes first
	line = StripANSI(line)
	line = strings.TrimSpace(line)

	if line == "" {
		return false
	}

	// Try agent-specific patterns first, then generic ones
	for _, p := range promptPatterns {
		// Skip patterns for other agent types
		if p.AgentType != "" && p.AgentType != agentType {
			continue
		}

		if p.Regex != nil && p.Regex.MatchString(line) {
			return true
		}
		if p.Literal != "" && strings.HasSuffix(line, p.Literal) {
			return true
		}
	}

	return false
}

// DetectIdleFromOutput analyzes output to determine if agent is idle.
// It checks the last non-empty line for prompt patterns.
func DetectIdleFromOutput(output string, agentType string) bool {
	// Strip ANSI first for cleaner processing
	output = StripANSI(output)

	lines := strings.Split(output, "\n")
	if len(lines) == 0 {
		return false
	}

	// Only consider the last non-empty line to avoid stale prompts earlier in history.
	foundLine := false
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		foundLine = true
		return IsPromptLine(line, agentType)
	}
	// If there was no output at all, treat user panes as idle (empty buffer, likely waiting at prompt)
	if !foundLine && (agentType == "" || agentType == "user") {
		return true
	}
	return false
}

// GetLastNonEmptyLine returns the last non-empty line from output
func GetLastNonEmptyLine(output string) string {
	output = StripANSI(output)
	lines := strings.Split(output, "\n")

	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line
		}
	}

	return ""
}

// AddPromptPattern allows adding custom prompt patterns at runtime
func AddPromptPattern(agentType string, pattern string, description string) error {
	regex, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}

	promptPatterns = append(promptPatterns, PromptPattern{
		AgentType:   agentType,
		Regex:       regex,
		Description: description,
	})

	return nil
}

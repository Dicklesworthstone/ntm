package agent

import (
	"regexp"
	"strconv"
	"strings"
)

// Claude Code (cc) patterns for state detection.
var (
	// ccRateLimitPatterns indicates the agent hit an API usage limit.
	// We use broad patterns here - false positives (waiting unnecessarily) are
	// acceptable, but false negatives (interrupting a blocked agent) are costly.
	ccRateLimitPatterns = []string{
		"you've hit your limit",
		"you.ve hit your limit",
		"rate limit exceeded",
		"rate limit",
		"too many requests",
		"please wait",
		"try again later",
		"usage limit",
		"request limit",
		"exceeded.*limit",
	}

	// ccContextWarnings indicates the agent is running low on context.
	// Claude Code doesn't give explicit percentages, so we rely on warning messages.
	ccContextWarnings = []string{
		"this conversation is getting long",
		"context limit",
		"context.*limit",
		"running out of context",
		"conversation.*long",
		"approaching.*limit",
		"nearing.*capacity",
	}

	// ccWorkingPatterns indicates the agent is actively producing output.
	// CRITICAL: When these patterns match, DO NOT INTERRUPT the agent.
	ccWorkingPatterns = []string{
		"```",         // Code block delimiter (most reliable indicator)
		"writing to ", // File write operation
		"created ",    // File creation
		"modified ",   // File modification
		"deleted ",    // File deletion
		"reading ",    // File read operation
		"searching ",  // Search in progress
		"running ",    // Command execution
		"executing ",  // Command execution
		"installing ", // Package installation
		"compiling",   // Compilation
		"building",    // Build process
		"testing",     // Test execution
		"fetching",    // Network operation
		"downloading", // Download operation
		"uploading",   // Upload operation
	}

	// ccIdlePatterns indicates the agent is waiting for user input.
	// When these match at the end of output, it's safe to restart or send new work.
	ccIdlePatterns = []*regexp.Regexp{
		regexp.MustCompile(`>\s*$`),      // Prompt waiting for input
		regexp.MustCompile(`(?m)^>\s*`),  // Prompt start (handles user typing)
		regexp.MustCompile(`Human:\s*$`), // Conversation mode prompt
		regexp.MustCompile(`waiting for input`),
		regexp.MustCompile(`\?\s*$`), // Question prompt
		// Claude Code TUI patterns (welcome screen and status bar)
		regexp.MustCompile(`(?i)claude\s+code\s+v[\d.]+`), // Version banner
		regexp.MustCompile(`(?i)bypass\s+permissions\s+on`), // Status bar
		regexp.MustCompile(`(?i)welcome\s+back`),            // Welcome message
		regexp.MustCompile(`╰─>\s*$`),                       // Arrow prompt
	}

	// ccErrorPatterns indicates an error condition.
	ccErrorPatterns = []string{
		"error:",
		"failed:",
		"exception:",
		"panic:",
		"fatal:",
		"abort:",
		"permission denied",
		"access denied",
		"connection refused",
		"timeout",
	}

	// ccHeaderPattern confirms output is from Claude Code.
	ccHeaderPattern = regexp.MustCompile(`(?i)\b(opus|claude|sonnet|haiku)\b\s*\d*\.?\d*`)
)

// Codex CLI (cod) patterns for state detection.
var (
	// codContextPattern extracts the explicit context percentage.
	// This is the most valuable metric - Codex shows "47% context left".
	// Example: "47% context left · ? for shortcuts"
	codContextPattern = regexp.MustCompile(`(\d+)%\s*context\s*left`)

	// codTokenPattern extracts token usage from response.
	// Example: "Token usage: total=219,582 input=206,150 ..."
	codTokenPattern = regexp.MustCompile(`Token usage:\s*total=(\d[\d,]*)`)

	// codRateLimitPatterns indicates the agent hit usage limits.
	codRateLimitPatterns = []string{
		"you've reached your usage limit",
		"you.ve reached your usage limit",
		"rate limit exceeded",
		"rate limit",
		"quota exceeded",
		"capacity reached",
		"maximum requests",
		"too many requests",
	}

	// codWorkingPatterns indicates active output production.
	codWorkingPatterns = []string{
		"```",       // Code block
		"editing ",  // File edit
		"creating ", // File creation
		"writing ",  // File write
		"reading ",  // File read
		"running ",  // Command execution
		"$ ",        // Shell command output indicator
		"applying ", // Applying changes
		"patching ", // Patch application
		"deleting ", // File deletion
	}

	// codIdlePatterns indicates waiting for input.
	codIdlePatterns = []*regexp.Regexp{
		regexp.MustCompile(`>\s*$`),                // Standard prompt
		regexp.MustCompile(`\?\s*for\s*shortcuts`), // Codex prompt line
		regexp.MustCompile(`codex>\s*$`),           // Codex prompt
		regexp.MustCompile(`(?m)^\s*›\s*.*$`),      // Codex chevron prompt
	}

	// codErrorPatterns indicates error conditions.
	codErrorPatterns = []string{
		"error:",
		"failed:",
		"exception:",
		"could not",
		"unable to",
	}

	// codHeaderPattern confirms output is from Codex CLI.
	codHeaderPattern = regexp.MustCompile(`(?i)\b(codex|openai|gpt-\d)\b`)
)

// Gemini CLI (gmi) patterns for state detection.
var (
	// gmiMemoryPattern extracts memory usage.
	// Less precise than Codex percentage but still useful.
	// Example: "gemini-3-pro-preview /model | 396.8 MB"
	gmiMemoryPattern = regexp.MustCompile(`(\d+\.?\d*)\s*MB`)

	// gmiYoloPattern detects YOLO mode status.
	// YOLO mode affects execution behavior (auto-approve commands).
	gmiYoloPattern = regexp.MustCompile(`(?i)YOLO\s*mode:\s*(ON|OFF)`)

	// gmiRateLimitPatterns indicates rate limiting.
	// Gemini is less explicit about limits, so we use broader heuristics.
	gmiRateLimitPatterns = []string{
		"quota exceeded",
		"quota",
		"limit reached",
		"rate limit",
		"try again",
		"capacity",
		"resource exhausted",
	}

	// gmiWorkingPatterns indicates active output.
	gmiWorkingPatterns = []string{
		"```",         // Code block
		"creating ",   // File creation
		"writing ",    // File write
		"executing ",  // Command execution
		"running ",    // Running command
		"generating ", // Content generation
		"analyzing ",  // Analysis
		"processing",  // General processing
		"thinking",    // Chain of thought
	}

	// gmiIdlePatterns indicates waiting for input.
	gmiIdlePatterns = []*regexp.Regexp{
		regexp.MustCompile(`>\s*$`),       // Standard prompt
		regexp.MustCompile(`gemini>\s*$`), // Gemini prompt
	}

	// gmiErrorPatterns indicates error conditions.
	gmiErrorPatterns = []string{
		"error",
		"failed",
		"exception",
		"invalid",
	}

	// gmiShellModePattern detects shell mode.
	// GOTCHA: Shell mode is triggered by "!" prefix in prompts.
	gmiShellModePattern = regexp.MustCompile(`^!\s*`)

	// gmiHeaderPattern confirms output is from Gemini CLI.
	gmiHeaderPattern = regexp.MustCompile(`(?i)(gemini.*preview|gemini-\d|google\s+ai)`)
)

// Cursor (cursor) patterns.
var (
	cursorRateLimitPatterns = []string{
		"rate limit",
		"too many requests",
		"quota exceeded",
	}

	cursorWorkingPatterns = []string{
		"```",
		"writing ",
		"reading ",
		"searching ",
		"analyzing ",
		"generating ",
	}

	cursorIdlePatterns = []*regexp.Regexp{
		regexp.MustCompile(`>\s*$`),
		regexp.MustCompile(`cursor>\s*$`),
	}

	cursorErrorPatterns = []string{
		"error:",
		"failed:",
		"exception:",
	}

	cursorHeaderPattern = regexp.MustCompile(`(?i)(cursor|cursor\s+ai)`)
)

// Windsurf (windsurf) patterns.
var (
	windsurfRateLimitPatterns = []string{
		"rate limit",
		"too many requests",
		"quota exceeded",
	}

	windsurfWorkingPatterns = []string{
		"```",
		"writing ",
		"reading ",
		"searching ",
		"analyzing ",
		"generating ",
	}

	windsurfIdlePatterns = []*regexp.Regexp{
		regexp.MustCompile(`>\s*$`),
		regexp.MustCompile(`windsurf>\s*$`),
	}

	windsurfErrorPatterns = []string{
		"error:",
		"failed:",
		"exception:",
	}

	windsurfHeaderPattern = regexp.MustCompile(`(?i)(windsurf|windsurf\s+ide)`)
)

// Aider (aider) patterns.
var (
	aiderRateLimitPatterns = []string{
		"rate limit",
		"too many requests",
		"quota exceeded",
	}

	aiderWorkingPatterns = []string{
		"```",
		"applied edit",
		"committing",
		"repo-map",
		"analyzing",
		"searching",
	}

	aiderIdlePatterns = []*regexp.Regexp{
		regexp.MustCompile(`>\s*$`),
		regexp.MustCompile(`aider>\s*$`),
	}

	aiderErrorPatterns = []string{
		"error:",
		"failed:",
		"exception:",
	}

	aiderHeaderPattern = regexp.MustCompile(`(?i)(aider|aider\s+chat)`)
)

// matchAny returns true if text contains any of the patterns (case-insensitive).
func matchAny(text string, patterns []string) bool {
	textLower := strings.ToLower(text)
	for _, p := range patterns {
		if strings.Contains(textLower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// matchAnyRegex returns true if text matches any of the regex patterns.
func matchAnyRegex(text string, patterns []*regexp.Regexp) bool {
	for _, p := range patterns {
		if p.MatchString(text) {
			return true
		}
	}
	return false
}

// collectMatches returns all patterns that matched in the text.
func collectMatches(text string, patterns []string) []string {
	var matches []string
	textLower := strings.ToLower(text)
	for _, p := range patterns {
		if strings.Contains(textLower, strings.ToLower(p)) {
			matches = append(matches, p)
		}
	}
	return matches
}

// extractFloat extracts the last float value from a regex match group.
// Returns nil if no match or parse error.
func extractFloat(pattern *regexp.Regexp, text string) *float64 {
	matches := pattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	match := matches[len(matches)-1]
	if len(match) < 2 {
		return nil
	}
	// Handle comma-separated numbers (e.g., "219,582")
	cleaned := strings.ReplaceAll(match[1], ",", "")
	val, err := strconv.ParseFloat(cleaned, 64)
	if err == nil {
		return &val
	}
	return nil
}

// extractInt extracts the last integer value from a regex match group.
// Returns nil if no match or parse error.
func extractInt(pattern *regexp.Regexp, text string) *int64 {
	matches := pattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	match := matches[len(matches)-1]
	if len(match) < 2 {
		return nil
	}
	// Handle comma-separated numbers
	cleaned := strings.ReplaceAll(match[1], ",", "")
	val, err := strconv.ParseInt(cleaned, 10, 64)
	if err == nil {
		return &val
	}
	return nil
}

// getLastNLines returns the last n lines of text.
// If the text has fewer than n lines, returns the entire text.
func getLastNLines(text string, n int) string {
	lines := strings.Split(text, "\n")
	if len(lines) <= n {
		return text
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// stripANSICodes removes ANSI escape sequences from text.
// This ensures pattern matching works correctly on terminal output.
// Matches CSI sequences (with private mode ?) and OSC sequences (title setting etc)
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\a\x1b]*(\a|\x1b\\)`)

func stripANSICodes(text string) string {
	return ansiPattern.ReplaceAllString(text, "")
}

// PatternSet groups all patterns for a specific agent type.
// This makes it easier to pass around and test pattern collections.
type PatternSet struct {
	RateLimitPatterns []string
	WorkingPatterns   []string
	IdlePatterns      []*regexp.Regexp
	ErrorPatterns     []string
	ContextWarnings   []string       // Only used by Claude Code
	ContextPattern    *regexp.Regexp // Explicit context extraction (Codex)
	TokenPattern      *regexp.Regexp // Token usage extraction
	MemoryPattern     *regexp.Regexp // Memory usage (Gemini)
	HeaderPattern     *regexp.Regexp
}

// GetPatternSet returns the pattern set for the given agent type.
func GetPatternSet(agentType AgentType) *PatternSet {
	switch agentType {
	case AgentTypeClaudeCode:
		return &PatternSet{
			RateLimitPatterns: ccRateLimitPatterns,
			WorkingPatterns:   ccWorkingPatterns,
			IdlePatterns:      ccIdlePatterns,
			ErrorPatterns:     ccErrorPatterns,
			ContextWarnings:   ccContextWarnings,
			HeaderPattern:     ccHeaderPattern,
		}
	case AgentTypeCodex:
		return &PatternSet{
			RateLimitPatterns: codRateLimitPatterns,
			WorkingPatterns:   codWorkingPatterns,
			IdlePatterns:      codIdlePatterns,
			ErrorPatterns:     codErrorPatterns,
			ContextPattern:    codContextPattern,
			TokenPattern:      codTokenPattern,
			HeaderPattern:     codHeaderPattern,
		}
	case AgentTypeGemini:
		return &PatternSet{
			RateLimitPatterns: gmiRateLimitPatterns,
			WorkingPatterns:   gmiWorkingPatterns,
			IdlePatterns:      gmiIdlePatterns,
			ErrorPatterns:     gmiErrorPatterns,
			MemoryPattern:     gmiMemoryPattern,
			HeaderPattern:     gmiHeaderPattern,
		}
	case AgentTypeCursor:
		return &PatternSet{
			RateLimitPatterns: cursorRateLimitPatterns,
			WorkingPatterns:   cursorWorkingPatterns,
			IdlePatterns:      cursorIdlePatterns,
			ErrorPatterns:     cursorErrorPatterns,
			HeaderPattern:     cursorHeaderPattern,
		}
	case AgentTypeWindsurf:
		return &PatternSet{
			RateLimitPatterns: windsurfRateLimitPatterns,
			WorkingPatterns:   windsurfWorkingPatterns,
			IdlePatterns:      windsurfIdlePatterns,
			ErrorPatterns:     windsurfErrorPatterns,
			HeaderPattern:     windsurfHeaderPattern,
		}
	case AgentTypeAider:
		return &PatternSet{
			RateLimitPatterns: aiderRateLimitPatterns,
			WorkingPatterns:   aiderWorkingPatterns,
			IdlePatterns:      aiderIdlePatterns,
			ErrorPatterns:     aiderErrorPatterns,
			HeaderPattern:     aiderHeaderPattern,
		}
	default:
		return &PatternSet{} // Empty pattern set for unknown types
	}
}

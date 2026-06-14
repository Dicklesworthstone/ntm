package agent

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/util"
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
		"exceeded the limit",
		"exceeded your limit",
		"exceeded limit",
	}

	// ccContextWarnings indicates the agent is running low on context.
	// Claude Code doesn't give explicit percentages, so we rely on warning messages.

	ccContextWarnings = []string{
		"this conversation is getting long",
		"context limit",
		"context is at its limit",
		"running out of context",
		"conversation is getting long",
		"conversation is too long",
		"approaching the limit",
		"approaching its limit",
		"nearing capacity",
		"nearing its capacity",
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
	// NOTE: "bypass permissions on" was removed — it matches the permanent status bar,
	// causing agents to always appear idle even while actively working.
	ccIdlePatterns = []*regexp.Regexp{
		regexp.MustCompile(`>\s*$`),      // Prompt waiting for input
		regexp.MustCompile(`(?m)^>\s*$`), // Prompt line (empty prompt waiting for input)
		regexp.MustCompile(`Human:\s*$`), // Conversation mode prompt
		regexp.MustCompile(`waiting for input`),
		regexp.MustCompile(`(?m)^.{0,40}\?\s*$`), // Short question prompt (max 40 chars to avoid matching reasoning output)
		// Claude Code TUI patterns (welcome screen)
		regexp.MustCompile(`(?i)claude\s+code\s+v[\d.]+`), // Version banner
		regexp.MustCompile(`(?i)welcome\s+back`),          // Welcome message
		regexp.MustCompile(`╰─>\s*$`),                     // Arrow prompt
		regexp.MustCompile(`(?m)❯[\s\x{00a0}]*$`),         // Unicode heavy right-pointing angle prompt (multiline, NBSP-aware)
	}

	// ccSpinnerActivePatterns detect Claude Code's randomized spinner verbs
	// (e.g. "Bunning… (3s)", "Scurrying… (12s)", "Running…", "· thinking", "· thought for 5s").
	// When these match, the agent is actively working — NOT idle.
	ccSpinnerActivePatterns = []*regexp.Regexp{
		regexp.MustCompile(`\S+…\s+\(`),         // Timing spinners: "Bunning… (3s)"
		regexp.MustCompile(`·\s*thinking`),      // Extended thinking indicator
		regexp.MustCompile(`·\s*thought\s+for`), // Past thinking indicator (still active context)
		regexp.MustCompile(`Running…`),          // Explicit running spinner
	}

	// claudeActiveSpinnerLineRe matches a Claude Code *live* spinner line — the
	// status line Claude renders just above its bottom-pinned input box while a
	// turn is in flight. Derived from real captures in internal/cli/outputs/:
	//
	//   ✻ Monitoring 17 agents… (ctrl+c to interrupt · … · 15m 52s · …)
	//   ✻ Compacting conversation… (ctrl+c to interrupt · … · 17m 50s · …)
	//   ✻ Whirlpooling… (ctrl+c to interrupt · … · 2m 44s · thinking)
	//   ✢ Implementing Remaining BV Robot Modes… (ctrl+c to interrupt · …)
	//
	// The defining shape is a verb/phrase ending in the ellipsis glyph "…"
	// immediately followed by the timer/interrupt parenthetical "(". This is
	// deliberately distinct from the completion line (which has no "…(" and no
	// open paren) so the two never collide.
	claudeActiveSpinnerLineRe = regexp.MustCompile(`\S+…\s*\(`)

	// claudeInFlightHintRe matches the in-flight footer hint Claude shows only
	// while a turn is running ("ctrl+c to interrupt" / "esc to interrupt"). It is
	// a second, independent signal of active work for spinner frames that render
	// without the "…(" shape.
	claudeInFlightHintRe = regexp.MustCompile(`(?i)(ctrl\+c|esc)\s+to\s+interrupt`)

	// claudeThinkingSpinnerRe matches the extended-thinking spinner ("· thinking")
	// but NOT the past-tense annotation ("thought for 14s") which can appear as a
	// prose tail inside a finished completion summary.
	claudeThinkingSpinnerRe = regexp.MustCompile(`·\s*thinking\b`)

	// claudeCompletionLineRe matches Claude Code's turn-ended completion line.
	// Derived from real captures: glyph-led, whole-line, "<Verb> for <duration>"
	// with NO ellipsis and NO open paren. Observed verbs include Cooked, Baked,
	// Cogitated, Churned, Brewed, Worked, Crunched — Claude randomizes the verb,
	// so the regex keys on the structural "<Capitalized word> for <Nm Ns>" shape
	// behind one of the asterisk-family spinner glyphs rather than an enumerated
	// verb list.
	//
	//   ✻ Cooked for 1m 42s
	//   ✻ Baked for 44m 28s
	//
	// It must NOT match an active spinner ("Whirlpooling… (… · 2m 44s …)") nor a
	// "thought for 14s" prose annotation, hence: the leading glyph + verb, the
	// literal " for ", a duration, and a deliberately strict end-of-line anchor
	// that rejects any trailing "(" / "…" progress decoration.
	//
	// The verb is \p{Lu}\p{L}* (a capitalized Unicode word), NOT [A-Z][a-z]+:
	// Claude's whimsical verbs include accented forms like "Sautéed" / "Flambéed".
	// With [A-Z][a-z]+ the accented byte broke the verb match, so "✻ Sautéed for
	// 36s" was NOT recognized as turn-ended — a stale active spinner higher in the
	// captured buffer then went unsuppressed and ClaudeActivelyWorking returned
	// true, leaving the finished pane falsely reading as "working" (a false-working
	// that withholds an idle agent from dispatch; the dispatcher reported
	// "Idle Agents: 1" with 8 idle agents). Found in a Haiku stress run. The
	// capitalized-first-letter requirement (\p{Lu}) is kept so the line still can't
	// match lowercase prose like "...waited for 5m...".
	claudeCompletionLineRe = regexp.MustCompile(`(?m)^\s*[✻✶✳✢✽✦*]\s+\p{Lu}\p{Ll}+\s+for\s+(?:\d+\s*[hms]\s*)+$`)

	// claudeNewTaskFooterRe matches the post-turn "new task?" hint Claude parks at
	// after a turn ends (often paired with a "/clear to save … tokens" line). It
	// is a turn-ended marker, not active work.
	claudeNewTaskFooterRe = regexp.MustCompile(`(?i)new\s+task\?`)

	// claudeComposeBoxRe matches Claude Code's compose-box permission-mode footer
	// (the "⏵⏵ bypass permissions on …" line under the input box). The ⏵⏵
	// double-play glyph is unique to that footer, so its presence means the Claude
	// TUI is alive at its input box. On its own it does NOT imply idle — the box
	// is drawn during active work too, which is why an ungated "bypass permissions"
	// string match was removed from the idle patterns. But it is a sound idle
	// signal once active work has ALREADY been ruled out (the caller checks
	// ClaudeActivelyWorking first). Gated that way it covers the states the
	// empty-❯ / completion-line / "new task?" patterns miss: a freshly-spawned
	// agent whose box holds a prefilled init prompt, or a box showing only the "…"
	// placeholder, with no completion line or "new task?" hint yet on screen —
	// e.g. right after `ntm spawn`, where without this the dispatcher sees
	// "0 idle agents" and the swarm never starts.
	claudeComposeBoxRe = regexp.MustCompile(`⏵⏵`)

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
		regexp.MustCompile(`(?m)^\s*›(?:\s.*)?$`),  // Codex chevron prompt, empty or with prefilled input
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
	gmiMemoryPattern = regexp.MustCompile(`/model\s*\|\s*(\d+\.?\d*)\s*MB`)

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

// Ollama (ollama) patterns.
var (
	ollamaRateLimitPatterns = []string{
		"rate limit",
		"too many requests",
		"quota exceeded",
	}

	ollamaWorkingPatterns = []string{
		"```",
		"pulling manifest",
		"verifying sha256 digest",
		"writing manifest",
		"removing any unused layers",
		"generating ",
		"thinking",
	}

	ollamaIdlePatterns = []*regexp.Regexp{
		regexp.MustCompile(`>\s*$`),
		regexp.MustCompile(`ollama>\s*$`),
	}

	ollamaErrorPatterns = []string{
		"error:",
		"failed:",
		"exception:",
	}

	ollamaHeaderPattern = regexp.MustCompile(`(?im)(^ollama>\s*$|\bollama\s+(run|chat|serve|pull)\b|^\s*ollama\s+cli\b)`)
)

// claudeLiveTailLines bounds the live-tail window scanned for Claude
// working/idle classification. Claude Code pins its input box to the BOTTOM of
// the screen and renders the live spinner just above it, so the most-recent
// dynamic state always lives in the last handful of lines. 16 is wide enough to
// span the spinner line, the box border rules, and the status footer while
// staying short of historical scrollback that could carry a stale spinner.
const claudeLiveTailLines = 16

// claudeIdleScanLines bounds how many trailing lines are scanned for an idle
// PROMPT pattern (as opposed to the spinner/footer live tail). It is narrower
// than claudeLiveTailLines so a stale prompt deep in scrollback is not matched,
// and matches the CC window detectIdle and status.maxIdleScanLines already use.
const claudeIdleScanLines = 12

// ClaudeActivelyWorking reports whether a Claude Code pane is mid-turn (actively
// working) based on its captured terminal output.
//
// Claude Code keeps a persistent "❯ " input box pinned to the BOTTOM of the
// screen at all times, even while busy. The live activity spinner (e.g.
// "✻ Monitoring 17 agents… (ctrl+c to interrupt · 15m 52s · …)") renders just
// ABOVE that box while a turn is in flight; when the turn ends Claude replaces
// the spinner with a completion line ("✻ Cooked for 1m 42s") and/or a
// "new task?" footer. Position relative to the input box therefore tells you
// nothing — only the relative ORDER of the spinner vs. the turn-ended markers
// does.
//
// Semantics: a pane is WORKING iff an active spinner is the MOST-RECENT dynamic
// marker — i.e. no turn-ended marker (completion line or "new task?" hint)
// appears AFTER the last spinner line within the live tail.
//
// The classifier is deliberately biased to the SAFE failure: when a spinner is
// present but ordering is ambiguous it returns true (false-WORKING). A
// dispatcher must NEVER inject a second task into a working agent, so the only
// acceptable error is to treat a maybe-idle pane as busy.
func ClaudeActivelyWorking(output string) bool {
	if output == "" {
		return false
	}
	clean := stripANSICodes(output)
	tail := util.GetLastNLines(clean, claudeLiveTailLines)
	if strings.TrimSpace(tail) == "" {
		return false
	}

	lines := strings.Split(tail, "\n")

	lastSpinner := -1
	lastTurnEnded := -1
	for i, line := range lines {
		if claudeIsActiveSpinnerLine(line) {
			lastSpinner = i
		}
		if claudeIsTurnEndedLine(line) {
			lastTurnEnded = i
		}
	}

	// No spinner anywhere in the live tail → not actively working.
	if lastSpinner < 0 {
		return false
	}

	// A spinner exists. It only counts as "current" work if no turn-ended
	// marker appears AFTER it. When a completion/new-task marker is strictly
	// more recent than the spinner, the turn has ended (idle). Otherwise —
	// including the ambiguous tie where they share a line — bias to WORKING.
	return lastTurnEnded < lastSpinner
}

// claudeIsActiveSpinnerLine reports whether a single line is a live Claude
// spinner frame. A completion line ("✻ Cooked for 1m 42s") is explicitly
// excluded so it can never masquerade as active work.
func claudeIsActiveSpinnerLine(line string) bool {
	if claudeIsCompletionLine(line) {
		return false
	}
	if claudeActiveSpinnerLineRe.MatchString(line) {
		return true
	}
	if claudeInFlightHintRe.MatchString(line) {
		return true
	}
	if claudeThinkingSpinnerRe.MatchString(line) {
		return true
	}
	return false
}

// claudeIsTurnEndedLine reports whether a single line is a turn-ended marker
// (completion summary or the post-turn "new task?" hint).
func claudeIsTurnEndedLine(line string) bool {
	return claudeIsCompletionLine(line) || claudeNewTaskFooterRe.MatchString(line)
}

// claudeChevronBoxRe matches Claude Code's bottom-pinned input box prompt on
// its own line, whether empty ("❯ ") or holding queued/prefilled text
// ("❯ Continue working on your assigned bead…"). The chevron at line-start
// (after optional indentation) is the load-bearing signal; the existing
// ccIdlePatterns only match an EMPTY chevron, so this broadens idle recognition
// to the finished-turn-with-queued-text case.
var claudeChevronBoxRe = regexp.MustCompile(`(?m)^\s*❯(?:[\s\x{00a0}].*|[\s\x{00a0}]*)$`)

// ClaudeIdlePromptShowing reports whether a Claude Code pane is displaying an
// idle / finished-turn prompt: the bottom-pinned input box (empty, holding
// queued text, or an "…" placeholder), a glyph-led completion summary, or the
// post-turn "new task?" footer. It is the idle counterpart to
// ClaudeActivelyWorking and is the single shared recognizer used by every
// Claude state-detection path (parser, status, robot) so they agree.
//
// IMPORTANT: callers must gate on `!ClaudeActivelyWorking(output)` first — this
// recognizer intentionally matches the input box even while a turn is in flight
// (the box is always drawn). Used alone it would false-idle a working pane.
func ClaudeIdlePromptShowing(output string) bool {
	// Bound the prompt scan to the same trailing window detectIdle uses for
	// Claude (claudeIdleScanLines) so a stale prompt deep in scrollback is not
	// matched, then check the finished-turn footer over the slightly wider live
	// tail. Empty/whitespace output is NOT a positive idle signal here: callers
	// that want "empty ⇒ idle" (parser.detectIdle) handle that explicitly.
	tail := util.GetLastNLines(stripANSICodes(output), claudeIdleScanLines)
	if strings.TrimSpace(tail) == "" {
		return false
	}
	// Scan each trailing line for an idle prompt pattern. Joining the tail and
	// matching `>\s*$` against the whole block would miss a prompt that is not
	// the very last line (e.g. "claude>\nfollowup"), so we match per line — the
	// same line-oriented semantics the legacy prompt scan used.
	for _, line := range strings.Split(tail, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if matchAnyRegex(line, ccIdlePatterns) {
			return true
		}
	}
	return claudeFinishedTurnIdle(output)
}

// claudeFinishedTurnIdle reports whether the live tail shows a finished-turn
// idle state: the bottom input box (empty, or holding queued text / an "…"
// placeholder) and/or the post-turn "new task?" footer. Callers must already
// have ruled out ClaudeActivelyWorking; this only broadens idle recognition
// beyond the empty-chevron-only ccIdlePatterns and never overrides a working
// verdict.
func claudeFinishedTurnIdle(output string) bool {
	clean := stripANSICodes(output)
	tail := util.GetLastNLines(clean, claudeLiveTailLines)
	if strings.TrimSpace(tail) == "" {
		return false
	}
	if claudeNewTaskFooterRe.MatchString(tail) {
		return true
	}
	if claudeCompletionLineRe.MatchString(tail) {
		return true
	}
	if claudeChevronBoxRe.MatchString(tail) {
		return true
	}
	return false
}

// claudeIsCompletionLine reports whether a single line is Claude's glyph-led
// turn-ended completion summary ("✻ Cooked for 16m 20s"). It rejects active
// spinner lines (which carry a "…(" progress decoration) and "thought for"
// prose annotations.
func claudeIsCompletionLine(line string) bool {
	if strings.Contains(line, "…") || strings.Contains(line, "(") {
		return false
	}
	return claudeCompletionLineRe.MatchString(line)
}

// matchAny returns true if text contains any of the patterns (case-insensitive).
func matchAny(text string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	textLower := strings.ToLower(text)
	for _, p := range patterns {
		// Optimization: patterns are already lowercase in ntm.
		if strings.Contains(textLower, p) {
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

// lastLineIdxMatching returns the highest line index (0-based, counting from
// the start of `text`) that matches any of the regex patterns. Returns -1 when
// nothing matches. Used by detectIdle to compare ordering of competing markers
// (e.g. a fresh idle prompt below a stale spinner) so the most-recent state
// wins instead of an unconditional override.
func lastLineIdxMatching(text string, patterns []*regexp.Regexp) int {
	if text == "" || len(patterns) == 0 {
		return -1
	}
	lines := strings.Split(text, "\n")
	last := -1
	for i, line := range lines {
		for _, p := range patterns {
			if p.MatchString(line) {
				last = i
				break
			}
		}
	}
	return last
}

// collectMatches returns all patterns that matched in the text.
func collectMatches(text string, patterns []string) []string {
	var matches []string
	if len(patterns) == 0 {
		return matches
	}
	textLower := strings.ToLower(text)
	for _, p := range patterns {
		// Optimization: patterns are already lowercase in ntm.
		if strings.Contains(textLower, p) {
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

// ansiPattern matches ANSI escape sequences.
// Matches CSI sequences (with private mode ?) and OSC sequences (title setting etc)
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\a\x1b]*(\a|\x1b\\)`)

// stripANSICodes removes ANSI escape sequences and other problematic control characters.
// This ensures pattern matching works correctly on terminal output.
func stripANSICodes(text string) string {
	// First, remove common ANSI escape sequences
	clean := ansiPattern.ReplaceAllString(text, "")

	// Second, handle carriage returns by keeping only the text after the last CR in each line.
	// This simulates the terminal's behavior of overwriting lines.
	if !strings.Contains(clean, "\r") {
		return clean
	}

	var sb strings.Builder
	sb.Grow(len(clean))

	start := 0
	for {
		newlineIdx := strings.Index(clean[start:], "\n")
		var line string
		if newlineIdx == -1 {
			line = clean[start:]
		} else {
			line = clean[start : start+newlineIdx]
		}

		if lastCR := strings.LastIndex(line, "\r"); lastCR != -1 {
			sb.WriteString(line[lastCR+1:])
		} else {
			sb.WriteString(line)
		}

		if newlineIdx == -1 {
			break
		}
		sb.WriteByte('\n')
		start += newlineIdx + 1
	}

	return sb.String()
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
	case AgentTypeOllama:
		return &PatternSet{
			RateLimitPatterns: ollamaRateLimitPatterns,
			WorkingPatterns:   ollamaWorkingPatterns,
			IdlePatterns:      ollamaIdlePatterns,
			ErrorPatterns:     ollamaErrorPatterns,
			HeaderPattern:     ollamaHeaderPattern,
		}
	default:
		return &PatternSet{} // Empty pattern set for unknown types
	}
}

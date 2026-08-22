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
	// turn is in flight. Derived from the scrubbed real-capture layouts tracked
	// in internal/agent/testdata/:
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
	// The verb class is the Unicode capitalized-word class `\p{Lu}\p{Ll}+`
	// rather than ASCII `[A-Z][a-z]+`: Claude's whimsical spinner verbs include
	// accented forms ("✻ Sautéed for 36s", "✻ Flambéed for 12s") whose é/ü/etc.
	// fall outside `[a-z]` and would break recognition of the turn-ended line —
	// leaving a stale active spinner higher in the capture unsuppressed and the
	// pane misclassified as still WORKING. `\p{Lu}` (capitalized first letter)
	// preserves the anti-prose constraint; the whole-line and duration anchors
	// are unchanged.
	claudeCompletionLineRe = regexp.MustCompile(`(?m)^\s*[✻✶✳✢✽✦*]\s+\p{Lu}\p{Ll}+\s+for\s+(?:\d+\s*[hms]\s*)+$`)

	// claudeNewTaskFooterRe matches the post-turn "new task?" hint Claude parks at
	// after a turn ends (often paired with a "/clear to save … tokens" line). It
	// is a turn-ended marker, not active work.
	claudeNewTaskFooterRe = regexp.MustCompile(`(?im)^\s*new\s+task\?\s*/clear\b`)

	// claudeTerminalErrorLineRe matches Claude's rendered tool-result failure
	// line. Keep this glyph-anchored so queued compose-box text or spinner labels
	// containing "Error:" cannot terminate an otherwise live turn. Claude uses
	// NBSP padding in real captures, so the post-glyph class is Unicode-aware.
	claudeTerminalErrorLineRe = regexp.MustCompile(`(?i)^\s*⎿[\s\x{00a0}]*error:`)

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
		"timed out",
		"timeout error",
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
		// Codex's current banner phrasing (bd-wtm0w): "You've hit your
		// usage limit ... try again at <date>", plus plan-limit variants.
		// Matching is substring containment, so the bare "hit your usage
		// limit" covers both the "you've"/"you have" prefixes. This list
		// cannot delegate to internal/ratelimit (import cycle: ratelimit
		// imports agent); keep it in sync with ratelimit's canonical
		// usageLimitBannerPatterns when provider copy changes.
		"hit your usage limit",
		"usage limit reached",
		"plan limit reached",
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

	// Codex keeps its input chevron and context footer rendered while a turn is
	// active, so those idle-looking elements cannot outrank a live spinner. Keep
	// these structural markers separate from codWorkingPatterns: substring-based
	// parser hints are useful for recent output, while these markers are strong
	// enough to override the persistent prompt chrome.
	codexActiveWorkLineRe = regexp.MustCompile(`(?im)^\s*[•·◐◑◒◓⬤]\s*(?:Working|Waiting\s+for\s+background|Thinking)\b`)
	codexInterruptHintRe  = regexp.MustCompile(`(?i)esc\s+to\s+i(?:nterrupt\b|\S*…|\S*\.\.\.)`)

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

const codexLiveTailLines = 15

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

// Antigravity CLI (agy) patterns for state detection.
//
// Antigravity is the Gemini CLI's successor and shares its interactive TUI
// behavior (memory display, YOLO-style auto-approve, prompt rendering), so its
// working / idle / rate-limit / error / metric detection reuses the gmi*
// pattern sets above (see the AgentTypeAntigravity arms in parser.go). The only
// agy-specific signal we need is a header signature that distinguishes an
// Antigravity pane from a legacy Gemini pane during unhinted type detection.
var (
	// agyHeaderPattern confirms output is from the Antigravity CLI.
	agyHeaderPattern = regexp.MustCompile(`(?i)antigravity`)
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

// Grok Build (grok) patterns for state detection.
//
// Derived from live captures of the official Grok Build TUI (grok 1.0.5,
// authenticated SuperGrok session, observed at 80/120/200-column widths for
// GH#251 phase 2). The TUI renders:
//
//   - a bottom-pinned bordered composer:      │ ❯ <text>              │
//   - a status line on the composer's bottom
//     border (permanent chrome):              ─ Grok 4.6 (high) · always-approve ─╯
//   - a live activity line while a turn is
//     in flight (braille spinner + verb):     ⠹ Waiting for response… 0.7s   … [stop]
//     ⠸ Thinking… 0.0s
//     ⠙ Responding… 1.7s
//   - a footer hint that carries "Esc:cancel"
//     ONLY while a turn is in flight:         Shift+Tab:mode  │  Esc:cancel  │  Ctrl+x:shortcuts
//   - a turn-ended summary:                   Worked for 12s
//   - an interrupt acknowledgement (Ctrl+C
//     during a turn cancels it; Ctrl+C at
//     idle is a no-op):                        Turn cancelled by user in 3.0s.
//
// Like Claude Code and codex, the composer box is drawn DURING work, so the
// "❯" glyph alone is never idle evidence — GrokActivelyWorking must be
// consulted first (see detectStateFlags).
var (
	grokRateLimitPatterns = []string{
		"rate limit",
		"too many requests",
		"quota exceeded",
		"usage limit",
	}

	grokWorkingPatterns = []string{
		"```",
		"writing ",
		"reading ",
		"running ",
		"executing ",
		"searching ",
		"generating ",
	}

	// grokActiveWorkLineRe matches the live activity line grok renders while a
	// turn is in flight: a braille spinner frame followed by one of the three
	// observed phase verbs and the ellipsis glyph.
	grokActiveWorkLineRe = regexp.MustCompile(`(?m)^\s*[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]\s*(?:Waiting for response|Thinking|Responding)…`)

	// grokCancelHintRe matches the "Esc:cancel" footer hint that grok shows
	// only while a turn is in flight (it disappears at idle) — a second,
	// independent in-flight signal for frames where the spinner line wrapped
	// or scrolled out of the live tail.
	grokCancelHintRe = regexp.MustCompile(`Esc:cancel`)

	// grokIdlePatterns indicates waiting for input. The bordered composer line
	// ("│ ❯ …") is permanent chrome; callers must gate on !GrokActivelyWorking
	// (detectStateFlags does) before trusting it as idle evidence.
	grokIdlePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?m)^\s*│\s*❯`),               // Bordered composer line
		regexp.MustCompile(`(?m)^\s*Worked\s+for\s+\d`),   // Turn-ended summary
		regexp.MustCompile(`Turn cancelled by user`),      // Post-interrupt acknowledgement
		regexp.MustCompile(`(?i)grok\s+build\s+\d+\.\d+`), // Welcome banner (fresh spawn)
	}

	grokErrorPatterns = []string{
		"error:",
		"failed:",
		"exception:",
	}

	// grokHeaderPattern confirms output is from the Grok Build TUI. The
	// status-line chrome ("Grok 4.6 (high) · always-approve") and the welcome
	// banner ("Grok Build  1.0.5") are both matched.
	grokHeaderPattern = regexp.MustCompile(`(?i)(grok\s+build|grok\s+\d+\.\d+\s*\()`)
)

// grokLiveTailLines bounds the live-tail window scanned for grok working
// classification. Grok pins its composer and activity line to the bottom of
// the screen, like codex, so the same budget applies.
const grokLiveTailLines = 15

// GrokActivelyWorking reports whether a Grok Build pane's trailing live window
// shows an in-flight turn: the braille spinner activity line ("⠹ Waiting for
// response… / Thinking… / Responding…") or the "Esc:cancel" footer hint, both
// of which grok renders only while a turn is running. The live window is
// width-adaptive; paneWidth is the real tmux pane width, pass 0 when unknown.
func GrokActivelyWorking(output string, paneWidth int) bool {
	clean := stripANSICodes(output)
	tail := util.GetLastNLines(clean, util.WidthAdaptiveTailLines(paneWidth, grokLiveTailLines))
	return grokActiveWorkLineRe.MatchString(tail) || grokCancelHintRe.MatchString(tail)
}

// OpenCode (oc, https://opencode.ai) patterns (ntm#261). Derived from the
// TUI source (packages/tui/src/component/prompt/index.tsx): the composer
// hint text reads `Ask anything... "<example>"` and is drawn only while
// the prompt is empty, i.e. at idle; while a turn runs the TUI renders a
// spinner plus an `esc interrupt` (after one press: `esc again to
// interrupt`) footer hint that disappears at idle.
var (
	ocRateLimitPatterns = []string{
		"rate limit",
		"too many requests",
		"quota exceeded",
		"usage limit",
	}

	ocWorkingPatterns = []string{
		"esc interrupt",
		"esc again to interrupt",
	}

	// ocInterruptHintRe matches the footer hint OpenCode shows only while a
	// turn is in flight.
	ocInterruptHintRe = regexp.MustCompile(`(?i)\besc\s+(?:again\s+to\s+)?interrupt\b`)

	// ocIdlePatterns indicates the empty composer is waiting for input.
	// Callers gate on !OpencodeActivelyWorking first (detectStateFlags does).
	ocIdlePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bAsk anything\b`),
		regexp.MustCompile(`(?m)^\s*Interrupted\s*$`), // post-interrupt acknowledgement
	}

	ocErrorPatterns = []string{
		"error:",
		"failed:",
		"exception:",
	}
)

// ocLiveTailLines bounds the live-tail window scanned for the OpenCode
// working veto; the footer sits at the bottom of the screen like grok's.
const ocLiveTailLines = 15

// OpencodeActivelyWorking reports whether an OpenCode pane's trailing live
// window shows an in-flight turn via the `esc interrupt` footer hint.
// paneWidth is the real tmux pane width; pass 0 when unknown.
func OpencodeActivelyWorking(output string, paneWidth int) bool {
	clean := stripANSICodes(output)
	tail := util.GetLastNLines(clean, util.WidthAdaptiveTailLines(paneWidth, ocLiveTailLines))
	return ocInterruptHintRe.MatchString(tail)
}

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

// ClaudeTurnState describes the newest dynamic marker in a Claude Code pane.
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
// marker — i.e. no turn-ended marker (completion line or "new task?" hint) or
// terminal error appears AFTER the last spinner line within the live tail.
//
// The classifier is deliberately biased to the SAFE failure: when a spinner is
// present but ordering is ambiguous it returns true (false-WORKING). A
// dispatcher must NEVER inject a second task into a working agent, so the only
// acceptable error is to treat a maybe-idle pane as busy.
type ClaudeTurnState uint8

const (
	ClaudeTurnUnknown ClaudeTurnState = iota
	ClaudeTurnWorking
	ClaudeTurnEnded
	ClaudeTurnError
)

// DetectClaudeTurnState returns the state represented by Claude's newest
// dynamic marker in the live tail. Completion/error markers are evaluated
// after spinner markers on each line so an unexpected overlap fails closed.
//
// The live window is width-adaptive (bd-eeifh): the fixed line budget counts
// physical capture rows, so on narrow panes a wrapped spinner frame can fall
// outside it and a working pane reads idle. paneWidth is the real tmux pane
// width; pass 0 when unknown to keep the calibrated budget.
func DetectClaudeTurnState(output string, paneWidth int) ClaudeTurnState {
	if output == "" {
		return ClaudeTurnUnknown
	}
	clean := stripANSICodes(output)
	tail := util.GetLastNLines(clean, util.WidthAdaptiveTailLines(paneWidth, claudeLiveTailLines))
	if strings.TrimSpace(tail) == "" {
		return ClaudeTurnUnknown
	}

	lines := strings.Split(tail, "\n")
	latestState := ClaudeTurnUnknown
	for _, line := range lines {
		if claudeIsActiveSpinnerLine(line) {
			latestState = ClaudeTurnWorking
		}
		if claudeIsTurnEndedLine(line) {
			latestState = ClaudeTurnEnded
		}
		if claudeIsTerminalErrorLine(line) {
			latestState = ClaudeTurnError
		}
	}
	return latestState
}

// ClaudeActivelyWorking reports whether the newest Claude turn marker is a
// live spinner rather than completion or a terminal tool-result error. The
// live window is width-adaptive (bd-eeifh); pass the real tmux pane width,
// or 0 when unknown.
func ClaudeActivelyWorking(output string, paneWidth int) bool {
	return DetectClaudeTurnState(output, paneWidth) == ClaudeTurnWorking
}

// CodexActivelyWorking reports whether Codex's trailing live window contains a
// structural in-flight marker. Codex leaves old spinner lines in scrollback and
// keeps its input chrome visible during work, so the bounded tail is required:
// matching the full capture would make completed turns appear busy forever,
// while treating the persistent chevron as idle would make active panes unsafe
// for dispatch.
//
// The live window is width-adaptive (bd-eeifh). paneWidth is the real tmux
// pane width; pass 0 when unknown to keep the calibrated budget.
func CodexActivelyWorking(output string, paneWidth int) bool {
	clean := stripANSICodes(output)
	tail := util.GetLastNLines(clean, util.WidthAdaptiveTailLines(paneWidth, codexLiveTailLines))
	return codexActiveWorkLineRe.MatchString(tail) || codexInterruptHintRe.MatchString(tail)
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

func claudeIsTerminalErrorLine(line string) bool {
	return claudeTerminalErrorLineRe.MatchString(line)
}

// claudeChevronBoxRe matches Claude Code's bottom-pinned input box prompt on
// its own line, whether empty ("❯ ") or holding queued/prefilled text
// ("❯ Continue working on your assigned bead…"). The chevron at line-start
// (after optional indentation) is the load-bearing signal; the existing
// ccIdlePatterns only match an EMPTY chevron, so this broadens idle recognition
// to the finished-turn-with-queued-text case.
var claudeChevronBoxRe = regexp.MustCompile(`(?m)^\s*❯(?:[\s\x{00a0}].*|[\s\x{00a0}]*)$`)

// claudeComposeBoxFooterRe matches the "⏵⏵" mode footer Claude Code renders at
// the very bottom of its compose box (e.g. "⏵⏵ bypass permissions on (shift+tab
// to cycle)" or "⏵⏵ accept edits on"). A freshly-spawned agent shows its init
// prompt prefilled in the input box — or just the "…" ellipsis indicator — with
// NO completion line and NO "new task?" hint yet, so every other turn-ended
// recognizer misses it and the swarm never starts ("Idle Agents: 0"). The
// "⏵⏵" footer proves the Claude TUI is alive at its compose box.
//
// CRITICAL: this footer is drawn DURING work too (it is permanent chrome, like
// the input box). It is therefore only a positive idle signal when combined
// with `!ClaudeActivelyWorking` — which every caller of ClaudeIdlePromptShowing
// already gates on. Used ungated it would false-idle a working pane (this is
// exactly the trap the removed "bypass permissions on" ccIdlePattern fell into).
var claudeComposeBoxFooterRe = regexp.MustCompile(`⏵⏵`)

// ClaudeIdlePromptShowing reports whether a Claude Code pane is displaying an
// idle / finished-turn prompt: the bottom-pinned input box (empty, holding
// queued text, or an "…" ellipsis), a glyph-led completion summary, or the
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
// ellipsis) and/or the post-turn "new task?" footer. Callers must already
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
	// The "⏵⏵" compose-box footer means the Claude TUI is alive at its input
	// box. Combined with the caller's required `!ClaudeActivelyWorking` gate,
	// "box alive + no active work = idle" — this is what lets a fresh-spawn
	// capture (init prompt prefilled or just an "…" ellipsis, no spinner) start
	// the swarm. It is NOT gated here because ClaudeIdlePromptShowing's contract
	// already requires callers to have ruled out active work.
	if claudeComposeBoxFooterRe.MatchString(tail) {
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

package status

// Agent TUIs pin chrome to the bottom of their pane: a bordered composer box
// and, for Claude Code, a user-configured status line underneath it. Anything
// that wants to show "what this agent last said" therefore cannot just take
// the tail of a capture — the tail is the chrome.
//
// The dashboard's Detail view did exactly that: "Recent Output" rendered a
// blind trailing 200-character truncation, so on a pane whose status line is
// itself longer than 200 characters (a two-line status line easily is) the
// section showed 100% status line and none of the transcript (ntm#322).
//
// This file is the one chrome-aware tail extractor. It is used by the
// dashboard status pipeline and by --robot-interrupt's last-output field,
// which had grown its own private copy of the prompt-line half of the job.

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

// composerMarkers are the glyphs an agent's TUI renders at its live composer.
// They mirror internal/tmux's send-side markers: codex swaps "›" for "»" at
// effort ultra, so both count.
func composerMarkers(agentType string) []string {
	switch agent.AgentType(agentType).Canonical() {
	case agent.AgentTypeCodex:
		return []string{"›", "»"}
	case agent.AgentTypeClaudeCode, agent.AgentTypeGrok:
		// A bare ">" is deliberately not here: a markdown blockquote in the
		// transcript starts with it, and treating that as the input box would
		// throw away everything below a quoted line. Inside a bordered row it
		// is unambiguous, so borderedOnlyMarkers covers that case.
		return []string{"❯"}
	default:
		return nil
	}
}

// borderedOnlyMarkers are composer glyphs safe to recognise only inside a
// bordered row, where transcript prose cannot imitate them.
var borderedOnlyMarkers = []string{">"}

// boxRuleRunes are the glyphs that make up a box rule or corner row. A row
// built only from these (plus spaces) is border chrome, never content.
const boxRuleRunes = "─━╌┄┈╰╯└┘╭╮┌┐┏┓┗┛│┃"

// chromeScanLines bounds how far back from the end of a capture the composer
// is looked for. The composer and its status line live within a handful of
// rows of the bottom; scanning further risks mistaking a boxed tool result in
// the transcript for the input box.
const chromeScanLines = 40

// isBoxRuleLine reports whether a line is made only of box-drawing glyphs and
// whitespace.
func isBoxRuleLine(line string) bool {
	trimmed := strings.TrimSpace(StripANSI(line))
	if trimmed == "" {
		return false
	}
	for _, r := range trimmed {
		if r != ' ' && !strings.ContainsRune(boxRuleRunes, r) {
			return false
		}
	}
	return true
}

// isComposerAnchor reports whether a line is unmistakably the agent's live
// composer row: it carries the composer marker.
//
// Being inside a box is NOT enough. Agents render tool results and diffs in
// boxes too, and treating any bordered row as the composer cuts the
// transcript at the first boxed diff instead of at the input.
func isComposerAnchor(line string, markers []string) bool {
	stripped := strings.TrimSpace(StripANSI(line))
	if stripped == "" {
		return false
	}

	// Unbordered composer row: the marker must START the line, so "see the ❯
	// glyph" in transcript prose does not qualify.
	for _, m := range markers {
		if m != "" && strings.HasPrefix(stripped, m) {
			return true
		}
	}

	if !isBorderedRow(stripped) {
		return false
	}
	inner := strings.TrimSpace(strings.TrimLeft(stripped, "│┃ "))
	for _, m := range append(append([]string{}, markers...), borderedOnlyMarkers...) {
		if m != "" && strings.HasPrefix(inner, m) {
			return true
		}
	}
	return false
}

// isBorderedRow reports whether a line begins with a vertical box border.
func isBorderedRow(line string) bool {
	stripped := strings.TrimSpace(StripANSI(line))
	return strings.HasPrefix(stripped, "│") || strings.HasPrefix(stripped, "┃")
}

// isComposerAdjacent reports whether a line may be absorbed into the composer
// block once an anchor has been found: the box's own rules and corners, and
// the wrapped continuation rows inside its borders. On its own it proves
// nothing — absorption only ever walks outward from an anchor.
func isComposerAdjacent(line string) bool {
	stripped := strings.TrimSpace(StripANSI(line))
	if stripped == "" {
		return false
	}
	return isBoxRuleLine(stripped) || isBorderedRow(stripped)
}

// boxBottomCornerRunes close a box. A rule row carrying one of them marks the
// END of some box.
const boxBottomCornerRunes = "╰╯└┘┗┛"

// closesAnotherBox reports whether a rule row above the composer is some other
// box's bottom edge.
//
// It is the boundary that stops the upward walk. The composer's own bottom
// edge is BELOW its marker row, so any closing rule found ABOVE belongs to a
// different box — a boxed diff or tool result in the transcript. Without this
// the walk would absorb that box and everything it contained, discarding real
// output, which is the same class of loss ntm#322 is about. A blank line stops
// the walk too, but agents do not always leave one.
func closesAnotherBox(line string) bool {
	stripped := strings.TrimSpace(StripANSI(line))
	if !isBoxRuleLine(stripped) {
		return false
	}
	return strings.ContainsAny(stripped, boxBottomCornerRunes)
}

// trimAgentChrome drops the agent's pinned bottom chrome from a capture's
// lines, returning only the transcript above it.
//
// The composer box is the anchor: a status line is arbitrary user-configured
// text that cannot be recognised on its own, but it always renders BELOW the
// input box, so everything from the box's top border down is chrome. When no
// composer is found the input is returned unchanged. A capture containing
// only chrome has an empty transcript.
func trimAgentChrome(lines []string, agentType string) []string {
	if agent.AgentType(agentType).Canonical() == agent.AgentTypeOmp {
		return trimOmpChrome(lines)
	}
	if strings.EqualFold(strings.TrimSpace(agentType), "pi") {
		return trimPiChrome(lines)
	}
	markers := composerMarkers(agentType)
	if len(markers) == 0 || len(lines) == 0 {
		return lines
	}

	start := len(lines) - chromeScanLines
	if start < 0 {
		start = 0
	}

	// Find the bottom-most composer row inside the scan window.
	composerAt := -1
	for i := len(lines) - 1; i >= start; i-- {
		if isComposerAnchor(lines[i], markers) {
			composerAt = i
			break
		}
	}
	if composerAt < 0 {
		return lines
	}

	// Walk up over the contiguous box rows to the box's top border. The walk
	// is not bounded by the scan window: once an anchor is found, the real
	// boundaries are a blank line or another box's closing rule, and a
	// composer with many wrapped rows must still be trimmed whole.
	cut := composerAt
	for cut > 0 {
		prev := lines[cut-1]
		if closesAnotherBox(prev) {
			break
		}
		if !isComposerAdjacent(prev) && !isComposerAnchor(prev, markers) {
			break
		}
		cut--
	}

	return lines[:cut]
}

// trimOmpChrome drops omp's pinned chrome: the status-line composer box (and
// any completion list under it) plus the Esc-hint activity line directly
// above the box while a turn runs. omp's composer has no marker glyph, so the
// structural parser in internal/agent locates the box instead of the marker
// walk above.
func trimOmpChrome(lines []string) []string {
	composer := agent.ParseOmpComposer(strings.Join(lines, "\n"))
	if !composer.Found {
		return lines
	}
	cut := composer.TopLine
	if composer.ActivityLine >= 0 {
		cut = composer.ActivityLine
	}
	if cut < 0 || cut > len(lines) {
		return lines
	}
	return lines[:cut]
}

var (
	piPlainRule         = regexp.MustCompile(`^─{4,}$`)
	piTopScrollRule     = regexp.MustCompile(`^─+ ↑ [1-9][0-9]* more ─+$`)
	piBottomScrollRule  = regexp.MustCompile(`^─+ ↓ [1-9][0-9]* more ─+$`)
	piWorkingRule       = regexp.MustCompile(`^─{2,} [⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏](?: .*)? ─+$`)
	piNarrowWorkingRule = regexp.MustCompile(`^─{1,3}[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]─*$`)
	piContextGauge      = regexp.MustCompile(`^(?:[0-9]+(?:\.[0-9]+)?%|\?)/[0-9]+(?:\.[0-9]+)?[kKMG]?$`)
	piUsageToken        = regexp.MustCompile(`^(?:[↑↓RW][0-9]+(?:\.[0-9]+)?[kKMG]?|CH[0-9]+(?:\.[0-9]+)?%|\$[0-9]+(?:\.[0-9]+)?)$`)
	piWindowsPath       = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
	piVersionBanner     = regexp.MustCompile(`(?i)^(?:▀▀█|pi)\s+v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?$`)
	piShellLaunch       = regexp.MustCompile(`^.*[$%❯>#]\s+(?:cd\s+.+\s+&&\s+)?(?:exec\s+)?(?:[^\s]+/)?pi(?:\s|$)`)
)

// Pi permits a draft to occupy 30% of the terminal height. Keep a generous
// independent bound so a tall editor is not confused with a partial redraw.
const piComposerMaxRows = 256

// trimPiChrome recognizes pi's default editor and footer, independently of
// Oh My Pi's bordered status-line composer. Pi's editor has no prompt glyph:
// two full-width rules enclose its draft, followed by cwd and usage/model rows.
// A rule pair alone is not sufficient evidence, since tool output can contain
// the same glyphs. Unknown/custom footers and incomplete redraws stay intact.
func trimPiChrome(lines []string) []string {
	transcript, _ := parsePiChrome(lines)
	return transcript
}

// parsePiChrome also reports whether the capture contains a complete known
// editor/footer frame. A partial redraw can leave unsubmitted draft text on
// screen; preserving that text for display must not make it response evidence.
func parsePiChrome(lines []string) ([]string, bool) {
	end := len(lines)
	for end > 0 && strings.TrimSpace(StripANSI(lines[end-1])) == "" {
		end--
	}
	// The built-in footer has two rows and may add one extension-status row.
	// Anchor it at the bottom rather than scanning arbitrary transcript text.
	footer := -1
	for stats := end - 1; stats >= end-2 && stats >= 1; stats-- {
		if stats < end-1 && IsPromptLine(lines[end-1], "user") {
			continue // pi exited to a shell; this is no longer pinned chrome.
		}
		if isPiFooterCwd(lines[stats-1]) && isPiFooterStats(lines[stats]) {
			footer = stats - 1
			break
		}
	}
	if footer < 0 {
		return lines, false
	}
	start := max(0, footer-chromeScanLines)
	for bottom := footer - 1; bottom >= start; bottom-- {
		width := piRuleWidth(lines[bottom], false)
		if width == 0 {
			continue
		}
		if !piCompletionRows(lines[bottom+1 : footer]) {
			return lines, false
		}
		// The editor renders at least one draft row. Draft text has padding
		// (or a reserved cursor column), so an indented/shorter rule inside a
		// pasted draft cannot masquerade as its full-width top border.
		for top := bottom - 2; top >= max(0, bottom-piComposerMaxRows); top-- {
			if piRuleWidth(lines[top], true) == width {
				return lines[piTranscriptStart(lines[:top]):top], true
			}
			if strings.HasPrefix(StripANSI(lines[top]), "─") {
				// An unknown or mismatched border cannot be skipped to find
				// an older, conveniently matching rule in the transcript.
				return lines, false
			}
		}
		return lines, false // Do not pair a partial box with an older transcript rule.
	}
	return lines, false
}

func piRuleWidth(line string, top bool) int {
	line = strings.TrimRight(StripANSI(line), " \t\r")
	width := utf8.RuneCountInString(line)
	if width < 4 {
		return 0
	}
	if piPlainRule.MatchString(line) || (!top && piBottomScrollRule.MatchString(line)) ||
		(top && (piTopScrollRule.MatchString(line) || piWorkingRule.MatchString(line) || piNarrowWorkingRule.MatchString(line))) {
		return width
	}
	return 0
}

func isPiFooterCwd(line string) bool {
	line = strings.TrimSpace(StripANSI(line))
	return line == "~" || strings.HasPrefix(line, "~/") || strings.HasPrefix(line, "~ (") ||
		strings.HasPrefix(line, "~ • ") || strings.HasPrefix(line, "/") ||
		strings.HasPrefix(line, "\\\\") || piWindowsPath.MatchString(line)
}

func isPiFooterStats(line string) bool {
	fields := strings.Fields(StripANSI(line))
	usage := false
	for i, field := range fields {
		if piContextGauge.MatchString(field) {
			return true
		}
		if piUsageToken.MatchString(field) {
			usage = true
			continue
		}
		if field == "(sub)" && i > 0 && strings.HasPrefix(fields[i-1], "$") {
			continue
		}
		// Narrow panes truncate the stats before its context gauge. Require
		// recognizable usage first and the renderer's terminal ellipsis.
		return i == len(fields)-1 && strings.HasSuffix(field, "...") &&
			(usage || piUsageToken.MatchString(strings.TrimSuffix(field, "...")))
	}
	return false
}

// Completion rows belong to the editor, but arbitrary text between a rule
// and the footer does not. Pi's SelectList has one arrow-selected row and
// indented alternatives, or the single "No matching commands" empty-list row.
func piCompletionRows(lines []string) bool {
	selected, nonblank, noMatch := 0, 0, false
	for _, line := range lines {
		line = StripANSI(line)
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		nonblank++
		switch {
		case strings.HasPrefix(trimmed, "→ "):
			selected++
		case trimmed == "No matching commands":
			noMatch = true
		case !strings.HasPrefix(line, "  "):
			return false
		}
	}
	return nonblank == 0 || (selected == 1 && !noMatch) || (nonblank == 1 && noMatch)
}

// pi clears the launch command above its startup header on its first turn.
// Exclude only that recognizable preamble, never arbitrary text before a
// banner quoted in a later answer. The header itself remains a stable anchor.
func piTranscriptStart(lines []string) int {
	first := 0
	for first < len(lines) && strings.TrimSpace(StripANSI(lines[first])) == "" {
		first++
	}
	if first >= len(lines) {
		return 0
	}
	header := first
	if piShellLaunch.MatchString(StripANSI(lines[first])) {
		header++
		for header < len(lines) && strings.TrimSpace(StripANSI(lines[header])) == "" {
			header++
		}
	}
	if header+1 < len(lines) && piVersionBanner.MatchString(strings.TrimSpace(StripANSI(lines[header]))) &&
		strings.Contains(strings.ToLower(StripANSI(lines[header+1])), "interrupt") {
		return header
	}
	return 0
}

// AgentTranscript removes recognized pinned composer and footer chrome while
// preserving the transcript's text and spacing. Unlike a display preview, an
// empty transcript stays empty and content is never truncated. This provides a
// stable boundary across composer redraws; it does not prove who authored the
// remaining terminal text. Unknown chrome remains intact for display; response
// attribution must use AgentResponseTranscript to reject incomplete pi frames.
func AgentTranscript(output, agentType string) string {
	return strings.Join(trimAgentChrome(strings.Split(StripANSI(output), "\n"), agentType), "\n")
}

// AgentResponseTranscript extracts text suitable for delivery-boundary checks.
// Pi's default TUI always renders its composer and footer, so a missing or
// unrecognized frame can be a partial repaint with unsubmitted draft text.
// Those captures return false and must not replace a previous boundary or
// advance response/idle evidence. A complete chrome-only frame returns "", true.
// Other agents preserve their existing AgentTranscript behavior.
func AgentResponseTranscript(output, agentType string) (string, bool) {
	if !strings.EqualFold(strings.TrimSpace(agentType), "pi") {
		return AgentTranscript(output, agentType), true
	}
	transcript, complete := parsePiChrome(strings.Split(StripANSI(output), "\n"))
	if !complete {
		return "", false
	}
	return strings.Join(transcript, "\n"), true
}

// LastMeaningfulOutput returns the last maxLen characters of an agent pane's
// real output: its transcript with the pinned composer, status line, border
// chrome, blank rows and bare prompt lines removed.
//
// agentType may be empty, in which case only blank and prompt lines are
// dropped. maxLen <= 0 returns the empty string; a result longer than maxLen
// keeps its tail (the most recent output), ellipsised when there is room.
func LastMeaningfulOutput(output string, agentType string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	return LastMeaningfulOutputLines(strings.Split(output, "\n"), agentType, maxLen)
}

// LastMeaningfulOutputLines is LastMeaningfulOutput over already-split lines.
func LastMeaningfulOutputLines(lines []string, agentType string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}

	if transcript := trimAgentChrome(lines, agentType); len(transcript) > 0 {
		// A preview may still show a chrome-only capture; response callers use
		// AgentResponseTranscript, where that fallback would fabricate evidence.
		lines = transcript
	}

	var meaningful []string
	total := 0
	for i := len(lines) - 1; i >= 0 && total < maxLen; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || IsPromptLine(line, agentType) {
			continue
		}
		// A row of nothing but box-drawing glyphs carries no information in a
		// text preview, and dropping it here means a border that escaped
		// trimAgentChrome — a box taller than the scan window, say — still
		// cannot reach the reader as "recent output".
		if isBoxRuleLine(line) {
			continue
		}
		meaningful = append([]string{line}, meaningful...)
		total += len(line) + 1
	}

	result := strings.Join(meaningful, "\n")
	if len(result) <= maxLen {
		return result
	}
	// Keep the tail: the most recent output is the interesting end.
	if maxLen < 4 {
		return truncateOutput(result, maxLen)
	}
	return "..." + truncateOutput(result, maxLen-3)
}

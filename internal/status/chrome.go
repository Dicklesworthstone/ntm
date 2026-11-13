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
	"strings"

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
	composer := agent.ParsePiComposerLines(lines)
	if !composer.Found || composer.TranscriptStart < 0 || composer.TopLine < composer.TranscriptStart || composer.TopLine > len(lines) {
		return lines, false
	}
	return lines[composer.TranscriptStart:composer.TopLine], true
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

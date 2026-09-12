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

// trimAgentChrome drops the agent's pinned bottom chrome from a capture's
// lines, returning only the transcript above it.
//
// The composer box is the anchor: a status line is arbitrary user-configured
// text that cannot be recognised on its own, but it always renders BELOW the
// input box, so everything from the box's top border down is chrome. When no
// composer is found the input is returned unchanged — trimming is a
// refinement, never a reason to show nothing.
func trimAgentChrome(lines []string, agentType string) []string {
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

	// Walk up over the contiguous box rows to the box's top border.
	cut := composerAt
	for cut > start && (isComposerAdjacent(lines[cut-1]) || isComposerAnchor(lines[cut-1], markers)) {
		cut--
	}

	// A composer that consumed the whole capture means we found a box but no
	// transcript; showing the raw tail is more useful than showing nothing.
	if cut == 0 {
		return lines
	}
	return lines[:cut]
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

	lines = trimAgentChrome(lines, agentType)

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

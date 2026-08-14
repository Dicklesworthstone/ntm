package agent

// gate.go implements shared interactive-gate-screen detection (bd-jf22c).
//
// An "interactive gate" is a modal screen an agent CLI renders that blocks all
// work until a human answers it with a keystroke: first-run trust dialogs
// (e.g. Antigravity's "Do you trust the contents of this project?"), auth /
// login gates (OAuth token races during multi-spawn leave panes frozen on an
// authentication-error frame), and onboarding choice screens (Claude's
// first-run theme picker). A pane parked on one of these is alive by every
// process-level signal, so health surfaces historically reported it
// "✓ OK / active" while it could never accept work.

import (
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

// gateTailBaseLines bounds the live-tail window scanned for gate screens at
// the ~80-column reference width. The window is deliberately small: a gate
// screen is only blocking while it is still sitting at the BOTTOM of the
// pane. A dialog that has scrolled up into history was answered (or the CLI
// recovered past it), so matching deep scrollback would re-flag a healthy
// pane forever.
const gateTailBaseLines = 20

// interactiveGateMarkers lists lowercase substrings that identify known
// blocking gate screens. Matching is case-insensitive against the pane's
// live tail (curly apostrophes normalized to ASCII). Order is most-specific
// first so the returned gate text is the most descriptive match.
//
// Keep entries specific enough that ordinary agent transcript chatter cannot
// contain them as running prose; see DetectInteractiveGateWidth for the
// residual-risk discussion.
var interactiveGateMarkers = []string{
	// Trust / workspace dialogs (Antigravity, VS Code-style CLIs).
	"do you trust the contents of this project",
	"do you trust the files in this folder",
	"trust this workspace",
	"trust this folder",

	// Auth / login gates (Claude /login, Codex device flow, OAuth races).
	// Only phrases that appear on the GATE SCREEN itself belong here.
	// Generic failure vocabulary ("authentication error", "session
	// expired", "enter to confirm") is deliberately excluded: those strings
	// occur constantly in ordinary agent output (error logs the agent is
	// reading, codex's normal approval modal "Press enter to confirm or esc
	// to go back"), and a gate verdict drives restart-urgency downstream —
	// a false positive here kills a healthy pane and its context.
	"browser didn't open? use the url below",
	"press enter to open browser",
	"select login method",
	"please run /login",
	"run claude login",
	"paste the code from the browser",

	// Onboarding / first-run choice gates (Claude theme picker etc.).
	"choose the text style that looks best",
	"select your theme to get started",
}

// gateQuoteNormalizer maps typographic apostrophes to ASCII so markers like
// "browser didn't open?" match however the CLI renders them.
var gateQuoteNormalizer = strings.NewReplacer("‘", "'", "’", "'")

// DetectInteractiveGate reports whether the pane's live tail shows a known
// interactive gate screen (trust dialog, auth/login gate, onboarding choice)
// and, if so, which gate phrase matched. Use DetectInteractiveGateWidth when
// the real pane width is known.
func DetectInteractiveGate(content string) (gate string, found bool) {
	return DetectInteractiveGateWidth(content, 0)
}

// DetectInteractiveGateWidth is DetectInteractiveGate with a width-adaptive
// live window (see util.WidthAdaptiveTailLines): capture rows are hard-
// wrapped at the pane width, so narrow panes need a proportionally larger
// row budget to cover the same logical screen. paneWidth is the real tmux
// pane width; pass 0 when unknown to keep the calibrated budget.
//
// False-positive mitigation: an agent DISCUSSING one of these phrases (for
// example while editing this very file, or summarizing an auth failure it
// just recovered from) could match. Two guards narrow that window:
//
//  1. live-tail-only matching — phrases in deep scrollback never match; and
//  2. working-chrome veto — when the pane shows a live Claude spinner or
//     Codex in-flight marker, the agent is demonstrably mid-turn, so a gate
//     phrase in the tail is transcript text, not a modal screen.
//
// Residual risk (accepted): an IDLE agent whose final transcript lines quote
// a gate phrase verbatim in the last ~20 rows will be flagged until new
// output scrolls the quote away. That trade is deliberate — a false "blocked"
// on an idle pane costs an operator one glance, while a false "healthy" on a
// gated pane silently stalls an unattended swarm.
func DetectInteractiveGateWidth(content string, paneWidth int) (gate string, found bool) {
	if strings.TrimSpace(content) == "" {
		return "", false
	}
	if ClaudeActivelyWorkingWidth(content, paneWidth) || CodexActivelyWorkingWidth(content, paneWidth) {
		return "", false
	}
	clean := stripANSICodes(content)
	tail := util.GetLastNLines(clean, util.WidthAdaptiveTailLines(paneWidth, gateTailBaseLines))
	if strings.TrimSpace(tail) == "" {
		return "", false
	}
	normalized := strings.ToLower(gateQuoteNormalizer.Replace(tail))
	for _, marker := range interactiveGateMarkers {
		if strings.Contains(normalized, marker) {
			return marker, true
		}
	}
	return "", false
}

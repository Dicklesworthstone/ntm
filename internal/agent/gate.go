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
	"regexp"
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
// contain them as running prose; see DetectInteractiveGate for the
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
// and, if so, which gate phrase matched.
//
// The live window is width-adaptive (see util.WidthAdaptiveTailLines):
// capture rows are hard-wrapped at the pane width, so narrow panes need a
// proportionally larger row budget to cover the same logical screen.
// paneWidth is the real tmux pane width; pass 0 when unknown to keep the
// calibrated budget.
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
func DetectInteractiveGate(content string, paneWidth int) (gate string, found bool) {
	if strings.TrimSpace(content) == "" {
		return "", false
	}
	if ClaudeActivelyWorking(content, paneWidth) || CodexActivelyWorking(content, paneWidth) {
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
	// Grok's workspace gate never says "trust". Require its actual decision
	// frame, rather than adding generic "security risks" prose as a marker.
	if TrustDialogVisible(clean) {
		return "workspace trust dialog", true
	}
	return "", false
}

var (
	trustDialogChoicePattern = regexp.MustCompile(`(?i)^\s*([❯>]\s*)?(?:[1-9]\.\s+)?(yes|no)\b.*$`)
	trustDialogHotkeyPattern = regexp.MustCompile(`(?i)[\t ]{2,}([yn])\s*$`)
)

// TrustDialogVisible reports a live workspace-trust decision frame, not just
// a trust phrase in a transcript. Unlike the advisory health detector above,
// this predicate is strict enough to gate prompt delivery: a current yes/no
// menu must end the capture and show confirmation chrome or explicit y/n
// hotkeys. A new composer, a response, or a closing Markdown fence below the
// menu proves it is history and never blocks delivery.
//
// No choice is made here. A menu without a selected cursor still owns input,
// and therefore still blocks a normal send, even if it cannot yet be answered
// safely by --robot-answer-dialog. This also covers agent families for which
// the generic composer readiness check has no known composer glyph.
func TrustDialogVisible(content string) bool {
	lines := strings.Split(strings.TrimSpace(stripANSICodes(content)), "\n")
	confirm, yes, no := false, false, false
	choices, hotkeys := 0, 0
	first := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if choices == 0 && ((strings.Contains(lower, "enter") && strings.Contains(lower, "confirm")) ||
			(strings.Contains(lower, "esc") && strings.Contains(lower, "cancel"))) {
			confirm = true
			continue
		}
		match := trustDialogChoicePattern.FindStringSubmatch(line)
		if match == nil {
			break
		}
		first = i
		choices++
		if choices > 9 {
			return false
		}
		decision := strings.ToLower(match[2])
		yes = yes || decision == "yes"
		no = no || decision == "no"
		if key := trustDialogHotkeyPattern.FindStringSubmatch(line); key != nil &&
			strings.EqualFold(key[1], decision[:1]) {
			hotkeys++
		}
	}
	if !yes || !no || (!confirm && hotkeys != choices) {
		return false
	}
	// Collapse hard wraps in the explanatory text, so narrow panes retain
	// the same trust evidence as a full-width capture. Never match arbitrary
	// "trust" or "security" words from source code or agent discussion.
	header := strings.ToLower(strings.Join(strings.Fields(strings.Join(lines[:first], "\n")), " "))
	for _, marker := range []string{
		"is this a project you created or one you trust",
		"do you trust the contents of this project",
		"do you trust the files in this folder",
		"do you trust this folder",
		"trust this workspace",
	} {
		if strings.Contains(header, marker) {
			return true
		}
	}
	return strings.Contains(header, "grok build may run or modify contents") &&
		strings.Contains(header, "security risks")
}

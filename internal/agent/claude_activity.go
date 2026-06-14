package agent

import (
	"regexp"
	"strings"
)

// ccNewTaskHintPattern matches Claude Code's post-turn "ready for input" footer
// hint (e.g. "new task? /clear to save 113.1k tokens"). It is rendered ONLY once
// the agent has finished its turn and is waiting — a work-exclusive idle signal.
var ccNewTaskHintPattern = regexp.MustCompile(`(?i)new task\?\s*/clear`)

// ccCompletionLinePattern matches Claude Code's turn-completion line, e.g.
// "✻ Baked for 16m 20s" / "✻ Cooked for 4m 43s". It is intentionally NARROW —
// glyph/symbol-led and anchored to the whole (trimmed) line as "<Verb> for
// <duration>" — so it cannot match either (a) an active spinner annotation like
// "Noodling… (4m · thought for 14s)" (excluded separately because it contains
// "…") or (b) prose that merely says "...waited for 5m before...". A narrow
// match biases the classifier toward the SAFE failure: if a real completion line
// is missed the pane reads as still-working (no dispatch), never falsely idle.
var ccCompletionLinePattern = regexp.MustCompile(`(?i)^[\W_]*[A-Za-z]+\s+for\s+\d+\s*[ms]\b(?:\s+\d+\s*[ms])?\s*$`)

// IsClaudeTurnEnded reports whether a single line is a "turn has finished"
// marker — the work-exclusive "new task? /clear" hint or a completion line
// ("✻ Baked for 16m 20s"). Lines that are themselves active spinners (they
// contain the "…" timing glyph) are never turn-ended. Exported so other
// detectors can decide whether a thinking/spinner signal above this line is
// stale.
func IsClaudeTurnEnded(line string) bool { return isClaudeTurnEnded(line) }

// isClaudeTurnEnded reports whether a single line is a "turn has finished"
// marker — the work-exclusive new-task hint or a completion line. Lines that are
// themselves active spinners (they contain the "…" timing glyph) are never
// turn-ended.
func isClaudeTurnEnded(line string) bool {
	if ccNewTaskHintPattern.MatchString(line) {
		return true
	}
	if strings.Contains(line, "…") {
		return false
	}
	return ccCompletionLinePattern.MatchString(strings.TrimSpace(line))
}

// ClaudeActivelyWorking reports whether the Claude Code pane whose recent output
// is `text` is currently doing work, as opposed to having finished a turn and
// settled at an idle prompt.
//
// WHY position-of-prompt does not work: Claude Code pins its input box to the
// BOTTOM of the screen at all times, so the "❯" prompt is the lowest line even
// during active work (the spinner renders ABOVE the box). Comparing a spinner's
// position against the prompt's position therefore always says "prompt is below
// the spinner" and misclassifies a busy pane as idle. The reliable signal is
// whether an ACTIVE SPINNER is the most-recent DYNAMIC marker, i.e. no
// turn-ended marker (new-task hint / completion line) appears after the last
// spinner. A spinner left in scrollback above a turn-ended marker is stale.
// claudeActivityWindowLines bounds how much of the tail ClaudeActivelyWorking
// inspects. Claude Code renders its active spinner immediately above the input
// box at the bottom of the screen, so a LIVE spinner is always within a few
// lines of the bottom; a spinner farther up is stale scrollback. Bounding the
// scan keeps callers that pass a large capture (e.g. the 50-line status window)
// consistent with callers that pass a small live window (~15 lines) — otherwise
// a stale spinner deep in a big capture, with no turn-ended marker after it,
// would read as active work (a false-working).
const claudeActivityWindowLines = 16

func ClaudeActivelyWorking(text string) bool {
	if text == "" {
		return false
	}
	lines := strings.Split(text, "\n")
	if len(lines) > claudeActivityWindowLines {
		lines = lines[len(lines)-claudeActivityWindowLines:]
	}
	lastSpin := -1
	lastEnded := -1
	for i, ln := range lines {
		if matchAnyRegex(ln, ccSpinnerActivePatterns) {
			lastSpin = i
		}
		if isClaudeTurnEnded(ln) {
			lastEnded = i
		}
	}
	return lastSpin >= 0 && lastSpin > lastEnded
}

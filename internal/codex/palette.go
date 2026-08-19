// Package codex classifies the on-screen state of a Codex agent pane.
//
// Codex (the OpenAI Codex CLI, agent type "cod") presents a terminal UI with
// transient overlays: a slash-command palette, a goal/plan prime indicator, and
// modal dialogs (approvals, confirmations). NTM commands that drive Codex panes
// (the #165-169 pane-control set) need to know which overlay, if any, is
// currently visible before they send keystrokes. This package answers that
// question by classifying a captured pane snapshot into a small, well-defined
// state machine.
//
// # Fragility note
//
// The classification depends on matching literal strings that Codex prints to
// the terminal. Those strings are NOT a stable public contract — Codex can
// change its TUI copy at any release. To keep that fragility contained, ALL
// match patterns live in exactly one place: the StateMarkers table below. When
// Codex changes its UI, update that table (and the table only); the classifier
// logic never needs to change.
//
// Markers that are best-effort inferences (rather than strings we have verified
// against a live Codex pane) are flagged with an "ASSUMPTION:" comment so they
// can be validated and corrected against a real pane.
package codex

import "strings"

// PaletteState is the classified state of a Codex pane's command/goal palette.
//
// The state set is intentionally small and closed. Any pane content that does
// not match a more specific state falls through to StateUnknown (when a Codex
// overlay marker is absent but the content is non-trivial) or StateIdle (when
// the pane shows a quiescent Codex prompt waiting for input).
type PaletteState string

const (
	// StateIdle means the pane shows a quiescent Codex input prompt with no
	// overlay open — Codex is waiting for the user to type. This is the safe
	// state to send a fresh prompt or open the palette.
	StateIdle PaletteState = "idle"

	// StateSlashPaletteOpen means the slash-command palette is open: the user
	// has typed "/" and Codex is showing the filterable list of slash commands
	// (/model, /approvals, /init, ...). Sending a plain prompt here would be
	// interpreted as palette filtering, not as a message.
	StateSlashPaletteOpen PaletteState = "slash_palette_open"

	// StateGoalPalettePrimed means a goal/plan prompt has been primed but not
	// yet submitted — Codex is showing the goal-entry / plan-prime affordance
	// (e.g. the "/plan" or goal-prompt surface) awaiting confirmation.
	StateGoalPalettePrimed PaletteState = "goal_palette_primed"

	// StateDialogOpen means a modal dialog/overlay is open — an approval
	// request, a confirmation, or another framed modal that captures input.
	// Keystrokes are routed to the dialog, not the main prompt.
	StateDialogOpen PaletteState = "dialog_open"

	// StateUnknown means the content did not match any known state. Callers
	// should treat this conservatively (do not assume the palette is closed).
	StateUnknown PaletteState = "unknown"
)

// String returns the state's stable string value (matches the JSON encoding).
func (s PaletteState) String() string { return string(s) }

// Marker is a single substring (or set of substrings) whose presence in the
// captured pane content is evidence of a particular state.
type Marker struct {
	// Substr is the literal substring to look for in the captured content.
	// Matching is case-insensitive (the classifier lowercases both sides).
	Substr string

	// Why documents what this marker corresponds to in the Codex UI. Keep this
	// current; it is the primary aid for re-validating markers against a live
	// pane.
	Why string

	// Assumed is true when Substr is a best-effort inference rather than a
	// string verified against Codex's source or a live pane. All markers in the
	// table below are currently verified (Assumed=false): the slash-command
	// tokens and approval-dialog copy were taken from Codex's own source
	// (codex-rs/tui/src/bottom_pane/command_popup.rs and approval_overlay.rs),
	// and the idle markers mirror NTM's shipping codIdlePatterns. Assumed
	// markers, if any are added later, are the first thing to re-check when
	// classification misbehaves.
	Assumed bool
}

// StateRule binds a state to the markers that select it. A rule fires when ANY
// of its markers is present in the captured content (logical OR). Rules are
// evaluated in Priority order (lowest number first); the first state whose
// rule fires wins. This ordering is how mutually-overlapping UIs (e.g. a dialog
// drawn over an open palette) resolve deterministically to the most specific,
// most input-capturing state.
type StateRule struct {
	State    PaletteState
	Priority int
	Markers  []Marker
}

// StateMarkers is the SINGLE SOURCE OF TRUTH for Codex pane-state classification.
//
// Every marker here is verified against Codex's own source (the slash palette
// renders each command as "/<command>" in command_popup.rs; the approval
// headers/option labels are literals in approval_overlay.rs) or against NTM's
// shipping idle parser (internal/agent/patterns.go codIdlePatterns).
//
// To adapt to a Codex UI change, edit ONLY this table:
//   - Add/adjust Marker.Substr values to match the new on-screen strings.
//   - Keep Marker.Assumed=false for source/live-verified strings; set true only
//     for a temporary best-effort guess pending verification.
//   - Adjust Priority only if the precedence between overlapping overlays changes.
//
// Do NOT scatter string matching elsewhere. The classifier (Classify) is pure
// table-driven logic and should not need edits when Codex's copy changes.
//
// Priority rationale (lower fires first):
//
//	10 dialog_open         — a modal captures input; it wins over everything.
//	20 slash_palette_open  — the slash list is open over the prompt.
//	30 goal_palette_primed — a goal/plan prompt is primed but not submitted.
//	40 idle                — quiescent prompt, no overlay.
var StateMarkers = []StateRule{
	{
		State:    StateDialogOpen,
		Priority: 10,
		Markers: []Marker{
			// Approval-modal headers, verified against Codex source
			// (codex-rs/tui/src/bottom_pane/approval_overlay.rs). Codex asks the
			// user to approve a command, an edit/patch, or network access.
			{Substr: "Would you like to run the following command?", Why: "command-approval modal header (approval_overlay.rs)", Assumed: false},
			{Substr: "Would you like to make the following edits?", Why: "edit/patch-approval modal header (approval_overlay.rs)", Assumed: false},
			{Substr: "Do you want to approve network access to", Why: "network-approval modal header (approval_overlay.rs)", Assumed: false},
			// Approval option labels (same source). These appear only inside the
			// approval overlay's selectable choices, so they are unambiguous.
			{Substr: "No, and tell Codex what to do differently", Why: "approval reject option label (approval_overlay.rs)", Assumed: false},
			{Substr: "Yes, proceed", Why: "approval accept option label (approval_overlay.rs)", Assumed: false},
		},
	},
	{
		State:    StateSlashPaletteOpen,
		Priority: 20,
		Markers: []Marker{
			// When the slash palette is open Codex renders each built-in command
			// as "/<command>" — codex-rs/tui/src/bottom_pane/command_popup.rs:
			// `format!("/{}", item.command())`. These tokens are from Codex's
			// actual SlashCommand set; several only appear inside the palette
			// popup, so their presence is a distinctive, copy-stable signal.
			{Substr: "/compact", Why: "Codex /compact command — palette entry", Assumed: false},
			{Substr: "/mention", Why: "Codex /mention command — palette entry", Assumed: false},
			{Substr: "/review", Why: "Codex /review command — palette entry", Assumed: false},
			{Substr: "/mcp", Why: "Codex /mcp command — palette entry", Assumed: false},
			{Substr: "/init", Why: "Codex /init command — palette entry", Assumed: false},
			{Substr: "/model", Why: "Codex /model command — palette entry", Assumed: false},
		},
	},
	{
		State:    StateGoalPalettePrimed,
		Priority: 30,
		Markers: []Marker{
			// /goal and /plan are real Codex SlashCommands (verified in
			// codex-rs/tui/src/bottom_pane/command_popup.rs). This rule fires when
			// a goal/plan command is present WITHOUT the broader palette listing
			// (priority 20 wins when the full palette is open). NOTE: the one
			// distinction still worth a live-pane confirmation is "goal primed
			// (selected, awaiting submit)" vs "/goal merely listed" — the tokens
			// themselves are source-verified, but the primed sub-state rendering
			// was not captured live (this sandbox kills detached tmux panes).
			{Substr: "/goal", Why: "Codex /goal command (goal prime)", Assumed: false},
			{Substr: "/plan", Why: "Codex /plan command (plan mode)", Assumed: false},
		},
	},
	{
		State:    StateIdle,
		Priority: 40,
		Markers: []Marker{
			// Quiescent Codex prompt waiting for input. These mirror the idle
			// markers already shipping in NTM's agent state parser
			// (internal/agent/patterns.go codIdlePatterns), so they are the most
			// battle-tested markers in this table.
			{Substr: "? for shortcuts", Why: "Codex idle prompt hint line", Assumed: false},
			{Substr: "codex>", Why: "Codex shell-style prompt", Assumed: false},
			{Substr: "›", Why: "Codex chevron input prompt (patterns.go codIdlePatterns)", Assumed: false},
		},
	},
}

// Classification is the full result of classifying captured pane content.
type Classification struct {
	// State is the resolved pane state.
	State PaletteState

	// MarkersMatched lists the literal marker substrings that fired for the
	// winning state, in table order. Empty when State is idle-by-default or
	// unknown.
	MarkersMatched []string
}

// Classify inspects captured Codex pane content and returns its palette state
// plus the markers that selected it.
//
// Algorithm (pure, table-driven over StateMarkers):
//  1. Evaluate rules in ascending Priority order.
//  2. The first rule with at least one matching marker wins; its matched
//     markers are recorded.
//  3. If no rule matches, return StateUnknown with no markers.
//
// Matching is case-insensitive. content is the raw captured pane text.
func Classify(content string) Classification {
	lower := strings.ToLower(content)

	// Evaluate in priority order. StateMarkers is authored in priority order,
	// but sort defensively so reordering the table literal cannot change
	// precedence semantics.
	rules := orderedRules()

	for _, rule := range rules {
		var matched []string
		for _, m := range rule.Markers {
			if m.Substr == "" {
				continue
			}
			if strings.Contains(lower, strings.ToLower(m.Substr)) {
				matched = append(matched, m.Substr)
			}
		}
		if len(matched) > 0 {
			return Classification{State: rule.State, MarkersMatched: matched}
		}
	}

	return Classification{State: StateUnknown, MarkersMatched: nil}
}

// orderedRules returns StateMarkers sorted by ascending Priority. It does not
// mutate the package-level table.
func orderedRules() []StateRule {
	rules := make([]StateRule, len(StateMarkers))
	copy(rules, StateMarkers)
	// Simple insertion sort; the table is tiny and this avoids importing sort
	// for a handful of entries.
	for i := 1; i < len(rules); i++ {
		for j := i; j > 0 && rules[j].Priority < rules[j-1].Priority; j-- {
			rules[j], rules[j-1] = rules[j-1], rules[j]
		}
	}
	return rules
}

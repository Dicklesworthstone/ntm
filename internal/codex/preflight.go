package codex

// Preflight classifies a Codex pane snapshot into a goal-lifecycle "preflight
// verdict": is it safe to drive this pane toward a goal right now, and if not,
// what should the orchestrator do instead?
//
// This is the first layer of the Codex goal-lifecycle cluster (NTM #167; #165,
// #168, #169 build on it). It is a strict SUPERSET of the palette-state machine
// in palette.go: the palette states answer "which overlay is open" for
// keystroke routing, while the preflight states answer "what is the pane's
// readiness for goal work" for orchestration decisions.
//
// # Grounding discipline (same as palette.go)
//
// Every marker below is either:
//   - grounded in a real, observable Codex output string (cited in Why), or
//   - DERIVED from an already-grounded source in this codebase
//     (internal/agent/patterns.go, internal/quota/codex.go), or
//   - explicitly marked Assumed=true with an "ASSUMPTION:" note when the
//     rendered sub-state could not be verified against a live pane.
//
// Where a required #167 state has NO groundable marker string (the goal
// in-progress / completed / replace-goal sub-state renderings were not
// capturable in the sandbox, which kills detached tmux panes), this file does
// NOT invent a marker. Instead it documents the gap and classifies
// conservatively — those states are reachable only via genuinely grounded
// signals, and absent those signals the pane falls through to a safe verdict.

import (
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/quota"
)

// PreflightState is the readiness classification of a Codex pane for goal work.
//
// The set is closed. It is a superset of the palette states: a pane that the
// palette classifier would call "dialog_open" or "idle" maps here into a
// preflight state that also encodes WHY that matters for goal orchestration.
type PreflightState string

const (
	// PreflightCodexLive: a live Codex CLI is present and at a quiescent prompt
	// (or otherwise clearly running). Safe to proceed with goal work.
	PreflightCodexLive PreflightState = "codex-live"

	// PreflightShellNoCodex: the pane shows a plain shell, with NO observable
	// Codex marker. Codex is not running here, so goal work cannot proceed in
	// this pane. CONSERVATIVE: there is no single universal "this is a bare
	// shell" string, so this state is reached only as the negative case — a
	// non-trivial capture that matches no Codex marker AND looks shell-like.
	PreflightShellNoCodex PreflightState = "shell-no-codex"

	// PreflightGoalInProgress: Codex is actively working a goal/task (it shows
	// its working/interrupt footer). Sending a new goal now would interrupt or
	// queue behind in-flight work, so the caller should wait.
	PreflightGoalInProgress PreflightState = "goal-in-progress"

	// PreflightGoalCompleted: Codex finished a goal and is back at an idle
	// prompt with a completion signal still on screen. Safe to proceed (e.g.
	// to send the next goal). CONSERVATIVE: Codex has no verified, unambiguous
	// "goal completed" banner string; without one this state is only entered
	// via a grounded completion marker, otherwise a finished pane reads as
	// codex-live (which is also "proceed"), so the verdict is unaffected.
	PreflightGoalCompleted PreflightState = "goal-completed"

	// PreflightReplaceGoalDialog: a modal is asking whether to replace/overwrite
	// an existing goal. Routing keystrokes blindly would answer the dialog. The
	// caller must resolve the dialog first. CONSERVATIVE: the exact replace-goal
	// dialog copy was not verifiable; this state is entered only via a grounded
	// replace/overwrite-goal string, otherwise such a modal classifies as the
	// generic dialog (refuse), which is the same safe direction.
	PreflightReplaceGoalDialog PreflightState = "replace-goal-dialog"

	// PreflightBackgroundTerminalWait: Codex is blocked waiting on a long-running
	// or background command/terminal. The pane is busy but not on the goal
	// itself; the caller should wait rather than send input. CONSERVATIVE: the
	// specific background-wait rendering was not verifiable, so this is entered
	// only via grounded "running …"/background signals; otherwise a busy pane
	// reads as goal-in-progress (also "wait").
	PreflightBackgroundTerminalWait PreflightState = "background-terminal-wait"

	// PreflightUsageLimit: the account hit a usage/rate/quota limit. No goal
	// work can proceed until the limit resets; the pane likely needs a respawn
	// on a different account. Detection reuses internal/quota.DetectUsageLimit.
	PreflightUsageLimit PreflightState = "usage-limit"

	// PreflightStaleScrollback: the capture is empty or too trivial to classify
	// (a fresh/torn-down pane, or a capture that produced nothing). This is a
	// TEMPORAL/meta condition, not a content string — it is detected from the
	// capture itself, never from a marker. The caller should refuse to act on
	// stale evidence and re-capture.
	PreflightStaleScrollback PreflightState = "stale-scrollback"

	// PreflightUnknown: a non-trivial Codex-looking capture that matched no
	// more specific preflight rule. Treated conservatively (refuse).
	PreflightUnknown PreflightState = "unknown"
)

// String returns the stable string value (matches JSON encoding).
func (s PreflightState) String() string { return string(s) }

// PreflightAction is the recommended orchestration action. Closed set.
type PreflightAction string

const (
	// ActionProceed: safe to send/continue goal work in this pane.
	ActionProceed PreflightAction = "proceed"
	// ActionRespawn: this pane cannot do goal work; respawn the Codex agent
	// (e.g. usage limit — respawn on a different account).
	ActionRespawn PreflightAction = "respawn"
	// ActionAlternatePane: Codex is not in this pane; use a different pane.
	ActionAlternatePane PreflightAction = "alternate_pane"
	// ActionWait: the pane is busy; retry after a delay.
	ActionWait PreflightAction = "wait"
	// ActionRefuse: do not act on this pane (ambiguous/stale/modal evidence).
	ActionRefuse PreflightAction = "refuse"
)

// String returns the stable string value (matches JSON encoding).
func (a PreflightAction) String() string { return string(a) }

// PreflightVerdict is the full result of a preflight classification.
type PreflightVerdict struct {
	// State is the resolved preflight state.
	State PreflightState
	// Action is the recommended action mapped from State (closed set).
	Action PreflightAction
	// Reason is a human-readable explanation of the verdict.
	Reason string
	// MarkersMatched lists the literal marker substrings that selected the
	// state, in table order. Empty for states detected without a content marker
	// (stale-scrollback, shell-no-codex) or when nothing matched.
	MarkersMatched []string
}

// preflightMarker is one grounded substring (case-insensitive) that is evidence
// of a preflight state. Same shape/discipline as palette.go's Marker.
type preflightMarker struct {
	Substr  string
	Why     string
	Assumed bool
}

// preflightRule binds a state+action to its markers. A rule fires when ANY
// marker is present. Rules are evaluated in ascending Priority; first hit wins.
//
// Priority rationale (lower fires first — most input-capturing / most blocking
// conditions win so the verdict is conservative):
//
//	10 usage-limit              — account is blocked; nothing else matters.
//	20 replace-goal-dialog      — a modal is capturing input.
//	30 background-terminal-wait — pane blocked on a background command.
//	40 goal-in-progress         — Codex is actively working.
//	50 goal-completed           — completion banner at an idle prompt.
//	60 codex-live               — quiescent live Codex prompt.
type preflightRule struct {
	State    PreflightState
	Action   PreflightAction
	Priority int
	Markers  []preflightMarker
}

// preflightRules is the SINGLE SOURCE OF TRUTH for preflight markers, mirroring
// palette.go's StateMarkers convention. Edit ONLY this table to adapt to Codex
// UI changes. The shell-no-codex and stale-scrollback states are intentionally
// absent here: they are decided structurally in Preflight (negative / temporal
// cases), not by a content marker.
var preflightRules = []preflightRule{
	{
		State:    PreflightUsageLimit,
		Action:   ActionRespawn,
		Priority: 10,
		Markers: []preflightMarker{
			// DERIVED from internal/agent/patterns.go codRateLimitPatterns, which
			// are grounded in real Codex usage-limit output. Usage-limit detection
			// is ALSO wired via internal/quota.DetectUsageLimit in Preflight(); these
			// markers give the matched-substring provenance for the verdict.
			{Substr: "you've reached your usage limit", Why: "Codex usage-limit banner (agent/patterns.go codRateLimitPatterns)"},
			{Substr: "you’ve reached your usage limit", Why: "Codex usage-limit banner, curly apostrophe variant"},
			{Substr: "rate limit exceeded", Why: "Codex rate-limit message (agent/patterns.go codRateLimitPatterns)"},
			{Substr: "quota exceeded", Why: "Codex quota-exhausted message (agent/patterns.go codRateLimitPatterns)"},
		},
	},
	{
		State:    PreflightReplaceGoalDialog,
		Action:   ActionRefuse,
		Priority: 20,
		Markers: []preflightMarker{
			// GROUNDED against live Codex 0.137.0: setting a second /goal while one
			// is active opens a "Replace goal?" modal with these exact strings
			// (verified live, NTM #168 implementation session). The modal renders:
			//   Replace goal?
			//   New objective: <text>
			//   › 1. Replace current goal  Set the new objective and start it now
			//     2. Cancel                Keep the current goal
			//   Press enter to confirm or esc to go back
			{Substr: "replace goal?", Why: "replace-goal modal header (live Codex 0.137.0)"},
			{Substr: "replace current goal", Why: "replace-goal modal option 1 label (live Codex 0.137.0)"},
			{Substr: "keep the current goal", Why: "replace-goal modal option 2 description (live Codex 0.137.0)"},
			// Legacy/best-effort phrasings kept as a fallback for older/newer copy.
			{Substr: "replace the current goal", Why: "replace-goal modal prompt (legacy/best-effort)", Assumed: true},
			{Substr: "a goal is already active", Why: "replace-goal modal preamble (legacy/best-effort)", Assumed: true},
		},
	},
	{
		State:    PreflightBackgroundTerminalWait,
		Action:   ActionWait,
		Priority: 30,
		Markers: []preflightMarker{
			// CONSERVATIVE / ASSUMPTION: the precise background-terminal wait
			// rendering was not verifiable live. These phrasings reflect Codex
			// waiting on a backgrounded/long-running command. If absent, a busy
			// pane falls through to goal-in-progress (also → wait), so a miss does
			// not change the action.
			{Substr: "waiting for command to finish", Why: "background command wait (unverified rendering)", Assumed: true},
			{Substr: "running in the background", Why: "background terminal wait (unverified rendering)", Assumed: true},
			{Substr: "running in background", Why: "background terminal wait (unverified rendering)", Assumed: true},
		},
	},
	{
		State:    PreflightGoalInProgress,
		Action:   ActionWait,
		Priority: 40,
		Markers: []preflightMarker{
			// GROUNDED (verified live Codex 0.137.0): while actively working a goal
			// Codex shows a "Working (Ns • esc to interrupt)" footer and a
			// "Pursuing goal (Ns)" segment in its status bar. Sending a new goal
			// while pursuing would open the replace-goal dialog or queue behind it.
			{Substr: "esc to interrupt", Why: "Codex working footer shown while a task is in flight (live 0.137.0)"},
			{Substr: "pursuing goal", Why: "Codex status-bar pursuing-goal segment while engaged (live 0.137.0)"},
			// GROUNDED: Codex prints "(working)"/"Working" status during a turn.
			{Substr: "working…", Why: "Codex in-progress status (ellipsis variant)"},
			{Substr: "esc to cancel", Why: "Codex cancel-while-working footer variant"},
		},
	},
	{
		State:    PreflightGoalCompleted,
		Action:   ActionProceed,
		Priority: 50,
		Markers: []preflightMarker{
			// CONSERVATIVE / ASSUMPTION: Codex has no verified, unambiguous
			// "goal completed" banner. These are best-effort completion phrasings.
			// If none matches, a finished pane reads as codex-live (also →
			// proceed), so the verdict is unaffected by a miss.
			{Substr: "goal completed", Why: "goal-completed banner (unverified rendering)", Assumed: true},
			{Substr: "task complete", Why: "goal-completed banner (unverified rendering)", Assumed: true},
		},
	},
	{
		State:    PreflightCodexLive,
		Action:   ActionProceed,
		Priority: 60,
		Markers: []preflightMarker{
			// GROUNDED in internal/agent/patterns.go (codIdlePatterns,
			// codContextPattern, codHeaderPattern) AND verified against live Codex
			// 0.137.0: a live Codex pane shows the idle prompt hint, the chevron
			// prompt, and/or a "Context N% left" status line. Any of these confirms
			// a live Codex CLI at/near a prompt.
			{Substr: "? for shortcuts", Why: "Codex idle prompt hint (agent/patterns.go codIdlePatterns)"},
			{Substr: "% context left", Why: "Codex context status line (agent/patterns.go codContextPattern; lower-cased variant)"},
			{Substr: "% context used", Why: "Codex context status line (used variant)"},
			{Substr: "openai codex", Why: "Codex welcome banner '>_ OpenAI Codex (vX)' (live Codex 0.137.0)"},
			{Substr: "codex>", Why: "Codex shell-style prompt (agent/patterns.go codIdlePatterns)"},
			{Substr: "›", Why: "Codex chevron input prompt (agent/patterns.go codIdlePatterns)"},
		},
	},
}

// shellPromptMarkers are weak, GROUNDED-by-convention signals that a pane is at a
// bare shell prompt (used only to distinguish shell-no-codex from unknown when no
// Codex marker is present). These are intentionally generic; they only matter in
// the negative case after every Codex marker has failed.
var shellPromptMarkers = []string{"$ ", "# ", "~$", "bash-", "zsh:", "% "}

// minMeaningfulRunes is the capture-size floor below which we refuse to classify
// content and report stale-scrollback. A genuine Codex pane prints far more than
// this; anything smaller is a fresh/torn-down pane or an empty capture.
const minMeaningfulRunes = 8

// Preflight classifies captured Codex pane content into a goal-readiness verdict.
//
// Decision order (each step is conservative — when evidence is thin or
// blocking, the verdict steers the caller away from acting):
//
//  1. Empty/trivial capture  -> stale-scrollback / refuse (temporal, not a marker).
//  2. Usage limit            -> usage-limit / respawn (reuses quota.DetectUsageLimit
//     AND the grounded usage-limit markers for provenance).
//  3. Grounded marker table  -> first matching preflightRule wins (priority order).
//  4. No Codex marker at all  -> shell-no-codex / alternate_pane if it looks like a
//     bare shell, else unknown / refuse.
func Preflight(content string) PreflightVerdict {
	// (1) Temporal: too little captured to trust any classification.
	if len([]rune(strings.TrimSpace(content))) < minMeaningfulRunes {
		return PreflightVerdict{
			State:          PreflightStaleScrollback,
			Action:         ActionRefuse,
			Reason:         "Captured pane content is empty or too trivial to classify; re-capture before acting.",
			MarkersMatched: []string{},
		}
	}

	lower := strings.ToLower(content)

	// (2) Usage limit gets first say. Wire the quota detector AND record the
	// grounded markers that fired (for auditable provenance). DetectUsageLimit
	// is the authoritative signal; the marker scan only enriches the reason.
	if quota.DetectUsageLimit(content) {
		matched := matchMarkers(lower, usageLimitRule().Markers)
		return PreflightVerdict{
			State:          PreflightUsageLimit,
			Action:         ActionRespawn,
			Reason:         "Account hit a usage/rate/quota limit (quota.DetectUsageLimit); respawn on a different account once it resets.",
			MarkersMatched: nonNilStrings(matched),
		}
	}

	// (3) Grounded marker table, priority order.
	for _, rule := range orderedPreflightRules() {
		if matched := matchMarkers(lower, rule.Markers); len(matched) > 0 {
			return PreflightVerdict{
				State:          rule.State,
				Action:         rule.Action,
				Reason:         reasonFor(rule.State),
				MarkersMatched: matched,
			}
		}
	}

	// (4) No Codex marker matched. If it looks like a bare shell, say so;
	// otherwise refuse on unknown content.
	for _, sh := range shellPromptMarkers {
		if strings.Contains(lower, strings.ToLower(sh)) {
			return PreflightVerdict{
				State:          PreflightShellNoCodex,
				Action:         ActionAlternatePane,
				Reason:         "Pane shows a shell prompt with no Codex marker; Codex is not running here — use a pane that is running Codex.",
				MarkersMatched: []string{},
			}
		}
	}

	return PreflightVerdict{
		State:          PreflightUnknown,
		Action:         ActionRefuse,
		Reason:         "No known Codex preflight marker matched; treating conservatively — do not act on this pane.",
		MarkersMatched: []string{},
	}
}

// reasonFor returns the canonical human-readable reason for a marker-driven
// state. Usage-limit, shell-no-codex, stale-scrollback and unknown are handled
// inline in Preflight (they carry detection-specific detail).
func reasonFor(s PreflightState) string {
	switch s {
	case PreflightReplaceGoalDialog:
		return "A replace/overwrite-goal modal is open; resolve the dialog before sending input."
	case PreflightBackgroundTerminalWait:
		return "Codex is blocked on a background/long-running command; wait and re-check."
	case PreflightGoalInProgress:
		return "Codex is actively working a task (interrupt footer present); wait rather than sending a new goal."
	case PreflightGoalCompleted:
		return "Codex finished a goal and is back at an idle prompt; safe to proceed with the next goal."
	case PreflightCodexLive:
		return "Live Codex CLI at a quiescent prompt; safe to proceed with goal work."
	default:
		return "Preflight classified the pane state."
	}
}

// usageLimitRule returns the usage-limit rule from the table (for its markers).
func usageLimitRule() preflightRule {
	for _, r := range preflightRules {
		if r.State == PreflightUsageLimit {
			return r
		}
	}
	return preflightRule{}
}

// orderedPreflightRules returns preflightRules sorted by ascending Priority
// without mutating the package table (insertion sort; tiny table).
func orderedPreflightRules() []preflightRule {
	rules := make([]preflightRule, len(preflightRules))
	copy(rules, preflightRules)
	for i := 1; i < len(rules); i++ {
		for j := i; j > 0 && rules[j].Priority < rules[j-1].Priority; j-- {
			rules[j], rules[j-1] = rules[j-1], rules[j]
		}
	}
	return rules
}

// matchMarkers returns the substrings (in table order) present in lower-cased
// content. lower must already be lower-cased; markers are lower-cased here.
func matchMarkers(lower string, markers []preflightMarker) []string {
	var matched []string
	for _, m := range markers {
		if m.Substr == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(m.Substr)) {
			matched = append(matched, m.Substr)
		}
	}
	return matched
}

// nonNilStrings returns s, or an empty (non-nil) slice when s is nil, so JSON
// encodes [] not null.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// AllPreflightStates returns the closed preflight state set in canonical order.
func AllPreflightStates() []PreflightState {
	return []PreflightState{
		PreflightCodexLive,
		PreflightShellNoCodex,
		PreflightGoalInProgress,
		PreflightGoalCompleted,
		PreflightReplaceGoalDialog,
		PreflightBackgroundTerminalWait,
		PreflightUsageLimit,
		PreflightStaleScrollback,
		PreflightUnknown,
	}
}

// AllPreflightActions returns the closed action set in canonical order.
func AllPreflightActions() []PreflightAction {
	return []PreflightAction{
		ActionProceed,
		ActionRespawn,
		ActionAlternatePane,
		ActionWait,
		ActionRefuse,
	}
}

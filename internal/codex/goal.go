package codex

// Goal-lifecycle logic for the Codex pane-control cluster (NTM #165, #168, #169).
//
// This file holds the PURE, table-driven classification + parsing logic shared by
// the three goal-lifecycle CLI commands. The CLI layer (internal/cli/codex.go)
// does the tmux capture + keystroke injection; everything that can be unit-tested
// over a captured string lives here, mirroring palette.go / preflight.go.
//
// # Grounding discipline
//
// Every marker / regexp below is grounded in a real, observed Codex output string
// captured against live Codex 0.137.0 during the #165/#168/#169 implementation
// session, OR derived from an already-grounded source in preflight.go. The
// load-bearing live captures were:
//
//	Goal set / engaged:
//	  • Goal active   Objective: <text>
//	  • Working (1s • esc to interrupt)
//	  status bar: … · Working · … · Pursuing goal (1s)
//
//	Replace-goal dialog (second /goal while one is active):
//	  Replace goal?
//	  New objective: <text>
//	  › 1. Replace current goal  Set the new objective and start it now
//	    2. Cancel                Keep the current goal
//	  Press enter to confirm or esc to go back
//
//	No goal set:
//	  • Usage: /goal <objective> No goal is currently set.

import (
	"regexp"
	"strings"
)

// -----------------------------------------------------------------------------
// #169: goal-engagement classification
// -----------------------------------------------------------------------------

// EngagementOutcome is the closed set of outcomes for a bounded wait on Codex
// goal engagement (NTM #169). It distinguishes a confirmed engagement from the
// various non-terminal and terminal failure shapes the wrapper used to text-scrape.
type EngagementOutcome string

const (
	// EngagementEngaged: Codex is confirmably pursuing the goal — a
	// "Pursuing goal (Ns)" counter is present (and ideally advancing), and/or a
	// "Goal active" banner is on screen. Safe to consider the goal accepted.
	EngagementEngaged EngagementOutcome = "engaged"

	// EngagementEngaging: Codex looks like it is starting the goal (working
	// footer / goal banner present) but no monotonically-advancing pursuing-goal
	// counter has been confirmed yet. The caller may keep waiting.
	EngagementEngaging EngagementOutcome = "engaging"

	// EngagementDialogStuck: a replace-goal (or other) modal is open and is
	// capturing input, so the goal cannot engage until the dialog is resolved.
	// The caller should resolve it (e.g. `ntm codex replace-goal`) and retry.
	EngagementDialogStuck EngagementOutcome = "dialog_stuck"

	// EngagementUnconfirmed: the wait timed out with no engagement signal and no
	// blocking dialog — Codex is live but never picked up the goal. Non-terminal
	// ambiguity; the caller decides whether to resend or escalate.
	EngagementUnconfirmed EngagementOutcome = "unconfirmed"

	// EngagementRespawnRequired: a terminal condition was observed that no amount
	// of waiting fixes — a usage/rate limit, or a pane that is not running Codex
	// at all. The Codex agent must be respawned (e.g. on a fresh account).
	EngagementRespawnRequired EngagementOutcome = "respawn_required"
)

// String returns the stable string value (matches JSON encoding).
func (o EngagementOutcome) String() string { return string(o) }

// AllEngagementOutcomes returns the closed outcome set in canonical order.
func AllEngagementOutcomes() []EngagementOutcome {
	return []EngagementOutcome{
		EngagementEngaged,
		EngagementEngaging,
		EngagementDialogStuck,
		EngagementUnconfirmed,
		EngagementRespawnRequired,
	}
}

// pursuingGoalRe extracts the elapsed-seconds counter from a Codex
// "Pursuing goal (Ns)" status-bar segment. Codex renders the elapsed time as
// "(1s)", "(45s)", "(2m3s)", etc.; we normalize that to whole seconds. The
// regexp is intentionally permissive about the unit suffix so a future "(1m)"
// rendering still parses.
var pursuingGoalRe = regexp.MustCompile(`(?i)pursuing goal\s*\(([0-9hms ]+?)\)`)

// goalActiveRe matches the "Goal active" banner Codex prints once a goal is set.
var goalActiveRe = regexp.MustCompile(`(?i)goal active`)

// hookInitRe matches the Agent Mail / hook init signal Codex emits when it
// begins executing a goal (it calls mcp_agent_mail.macro_start_session). This is
// the "hook-init signal" referenced in #169; its presence is evidence Codex is
// actually executing, not just displaying a banner.
var hookInitRe = regexp.MustCompile(`(?i)macro_start_session|file_reservation_paths|agent mail`)

// EngagementSample is a single point-in-time read of a Codex pane's goal-engagement
// signals, derived purely from captured content. The #169 command takes one of
// these per poll and feeds them to ClassifyEngagement.
type EngagementSample struct {
	// PursuingCounter is the parsed seconds value from "Pursuing goal (Ns)", or
	// -1 when no pursuing-goal segment is present in this capture.
	PursuingCounter int
	// PursuingPresent is true when a "Pursuing goal (...)" segment was seen at all
	// (independent of whether the counter parsed to a number).
	PursuingPresent bool
	// GoalActive is true when a "Goal active" banner is present.
	GoalActive bool
	// HookInit is true when a hook/Agent-Mail init signal is present.
	HookInit bool
	// Working is true when Codex's working footer ("esc to interrupt") is present.
	Working bool
	// ReplaceDialog is true when a replace-goal modal is capturing input.
	ReplaceDialog bool
	// UsageLimit is true when a usage/rate/quota limit was detected.
	UsageLimit bool
	// CodexLive is true when the capture shows a live Codex CLI at all.
	CodexLive bool
}

// SampleEngagement reads a single capture into an EngagementSample using the
// grounded markers above plus the preflight verdict for the cross-cutting
// usage-limit / codex-live / shell signals (single source of truth reuse).
func SampleEngagement(content string) EngagementSample {
	lower := strings.ToLower(content)
	s := EngagementSample{PursuingCounter: -1}

	if m := pursuingGoalRe.FindStringSubmatch(content); m != nil {
		s.PursuingPresent = true
		s.PursuingCounter = parseElapsedSeconds(m[1])
	}
	s.GoalActive = goalActiveRe.MatchString(content)
	s.HookInit = hookInitRe.MatchString(content)
	s.Working = strings.Contains(lower, "esc to interrupt")

	// Reuse the grounded preflight verdict so cross-cutting states are classified
	// in exactly one place. ReplaceGoalDialog → dialog; UsageLimit → respawn;
	// any codex-live/in-progress/completed verdict means Codex is present.
	v := Preflight(content)
	switch v.State {
	case PreflightReplaceGoalDialog:
		s.ReplaceDialog = true
	case PreflightUsageLimit:
		s.UsageLimit = true
	}
	switch v.State {
	case PreflightCodexLive, PreflightGoalInProgress, PreflightGoalCompleted,
		PreflightReplaceGoalDialog, PreflightBackgroundTerminalWait:
		s.CodexLive = true
	}
	return s
}

// parseElapsedSeconds turns a Codex elapsed token like "45s", "2m3s", "1m" or a
// bare number into whole seconds. Unparseable input yields 0.
func parseElapsedSeconds(tok string) int {
	tok = strings.TrimSpace(strings.ToLower(tok))
	if tok == "" {
		return 0
	}
	total, cur := 0, 0
	sawUnit := false
	for _, r := range tok {
		switch {
		case r >= '0' && r <= '9':
			cur = cur*10 + int(r-'0')
		case r == 'h':
			total += cur * 3600
			cur = 0
			sawUnit = true
		case r == 'm':
			total += cur * 60
			cur = 0
			sawUnit = true
		case r == 's':
			total += cur
			cur = 0
			sawUnit = true
		default:
			// ignore spaces / stray chars
		}
	}
	if !sawUnit {
		// A bare number with no unit (e.g. "12") — treat as seconds.
		return cur
	}
	return total
}

// EngagementClassification is the result of a bounded engagement wait (#169).
type EngagementClassification struct {
	// Outcome is the resolved engagement outcome (closed set).
	Outcome EngagementOutcome
	// Reason is a human-readable explanation of the outcome.
	Reason string
	// CounterInit is the first observed pursuing-goal counter (-1 if never seen).
	CounterInit int
	// CounterLast is the last observed pursuing-goal counter (-1 if never seen).
	CounterLast int
	// CumulativeDelta is CounterLast-CounterInit when both were seen, else 0.
	CumulativeDelta int
	// HookInit is true when any sample showed a hook/Agent-Mail init signal.
	HookInit bool
	// UsageLimitMixedState is true when usage-limit evidence co-occurred with
	// live/goal evidence in the sample window — an ambiguous, respawn-worthy mix.
	UsageLimitMixedState bool
	// TimedOut records whether the wait exhausted its budget without a terminal
	// (engaged / dialog_stuck / respawn_required) outcome.
	TimedOut bool
}

// ClassifyEngagement folds an ordered series of samples (oldest→newest) into a
// single engagement verdict. It is pure so it can be unit-tested without tmux.
//
// Precedence (most blocking / most terminal first, so the verdict is conservative):
//
//  1. respawn_required — any sample shows a usage/rate/quota limit, OR the LAST
//     sample shows no live Codex at all (pane died / never had Codex).
//  2. dialog_stuck     — the LAST sample shows a replace-goal modal capturing input.
//  3. engaged          — a pursuing-goal counter advanced across samples, OR a
//     pursuing-goal counter is present together with a hook-init signal.
//  4. engaging         — goal/working/banner signals present but no confirmed
//     advancing counter yet (and we have not timed out, or timed out while still
//     visibly engaging).
//  5. unconfirmed      — none of the above; Codex never picked up the goal.
func ClassifyEngagement(samples []EngagementSample, timedOut bool) EngagementClassification {
	res := EngagementClassification{
		Outcome:     EngagementUnconfirmed,
		CounterInit: -1,
		CounterLast: -1,
		TimedOut:    timedOut,
	}
	if len(samples) == 0 {
		res.Reason = "No samples were captured; nothing to classify."
		return res
	}

	var sawUsageLimit, sawLiveOrGoal, sawAdvancingCounter, sawCounter, sawWorkingOrBanner bool
	for _, s := range samples {
		if s.HookInit {
			res.HookInit = true
		}
		if s.UsageLimit {
			sawUsageLimit = true
		}
		if s.CodexLive || s.GoalActive || s.PursuingPresent {
			sawLiveOrGoal = true
		}
		if s.GoalActive || s.Working || s.PursuingPresent {
			sawWorkingOrBanner = true
		}
		if s.PursuingPresent && s.PursuingCounter >= 0 {
			sawCounter = true
			if res.CounterInit < 0 {
				res.CounterInit = s.PursuingCounter
			}
			if res.CounterLast >= 0 && s.PursuingCounter > res.CounterLast {
				sawAdvancingCounter = true
			}
			res.CounterLast = s.PursuingCounter
		}
	}
	if sawCounter && res.CounterInit >= 0 && res.CounterLast >= 0 {
		res.CumulativeDelta = res.CounterLast - res.CounterInit
		if res.CumulativeDelta > 0 {
			sawAdvancingCounter = true
		}
	}
	res.UsageLimitMixedState = sawUsageLimit && sawLiveOrGoal

	last := samples[len(samples)-1]

	// (1) respawn_required.
	if sawUsageLimit {
		res.Outcome = EngagementRespawnRequired
		res.Reason = "A usage/rate/quota limit was observed during the wait; respawn Codex on a fresh account."
		return res
	}
	if !last.CodexLive && !last.ReplaceDialog {
		res.Outcome = EngagementRespawnRequired
		res.Reason = "The pane no longer shows a live Codex CLI; the agent likely died and must be respawned."
		return res
	}

	// (2) dialog_stuck.
	if last.ReplaceDialog {
		res.Outcome = EngagementDialogStuck
		res.Reason = "A replace-goal modal is open and capturing input; resolve it (ntm codex replace-goal) before the goal can engage."
		return res
	}

	// (3) engaged.
	if sawAdvancingCounter || (sawCounter && res.HookInit) {
		res.Outcome = EngagementEngaged
		if sawAdvancingCounter {
			res.Reason = "Codex is pursuing the goal: the pursuing-goal counter advanced across samples."
		} else {
			res.Reason = "Codex is pursuing the goal: a pursuing-goal counter is present together with a hook-init signal."
		}
		return res
	}

	// (4) engaging.
	if sawWorkingOrBanner || sawCounter {
		res.Outcome = EngagementEngaging
		res.Reason = "Codex appears to be starting the goal (working/banner/counter present) but no advancing pursuing-goal counter was confirmed yet."
		return res
	}

	// (5) unconfirmed.
	res.Outcome = EngagementUnconfirmed
	res.Reason = "No goal-engagement signal was observed before the wait ended; Codex did not pick up the goal."
	return res
}

// EngagementExitNonZero reports whether an outcome is a terminal non-engagement
// that should map to a non-zero process exit (per #169 acceptance criteria).
func EngagementExitNonZero(o EngagementOutcome) bool {
	switch o {
	case EngagementEngaged, EngagementEngaging:
		return false
	default:
		// dialog_stuck, unconfirmed, respawn_required are non-engagement.
		return true
	}
}

// -----------------------------------------------------------------------------
// #168: replace-goal dialog detection + safe selection
// -----------------------------------------------------------------------------

// ReplaceGoalSelection is the closed set of choices a caller may request for the
// Codex "Replace goal?" dialog.
type ReplaceGoalSelection string

const (
	// ReplaceGoalReplace selects option 1 ("Replace current goal").
	ReplaceGoalReplace ReplaceGoalSelection = "replace"
	// ReplaceGoalCancel selects option 2 ("Cancel" — keep the current goal).
	ReplaceGoalCancel ReplaceGoalSelection = "cancel"
)

// String returns the stable string value.
func (s ReplaceGoalSelection) String() string { return string(s) }

// replaceDialogConfirmRe matches the dialog's confirm-affordance line, which is
// the strongest single proof the modal is actually rendered and interactive.
var replaceDialogConfirmRe = regexp.MustCompile(`(?i)press enter to confirm`)

// ReplaceGoalDialog is a parsed view of the Codex replace-goal modal, derived
// purely from captured content.
type ReplaceGoalDialog struct {
	// Present is true when a replace-goal modal is detected at all (via the
	// grounded preflight classifier).
	Present bool
	// Interactive is true when the dialog's "press enter to confirm" affordance is
	// visible — i.e. the modal is fully rendered and ready for a selection.
	Interactive bool
	// NewObjective is the text after "New objective:" when present (best-effort).
	NewObjective string
	// OldGoalClosed is the #168 "old-goal-closed proof": true when there is
	// affirmative evidence the prior goal is replaceable — the dialog explicitly
	// offers to "keep the current goal" as the alternative, which only renders
	// when there IS a current goal that Replace would close. Without this proof
	// the command refuses to blind-select Replace.
	OldGoalClosed bool
	// MarkersMatched lists the replace-goal markers that fired (from preflight).
	MarkersMatched []string
}

var newObjectiveRe = regexp.MustCompile(`(?i)new objective:\s*(.+)`)

// DetectReplaceGoalDialog parses captured pane content into a ReplaceGoalDialog.
// It reuses the grounded preflight classifier as the single source of truth for
// "is a replace-goal modal present", then enriches with #168-specific proof.
func DetectReplaceGoalDialog(content string) ReplaceGoalDialog {
	d := ReplaceGoalDialog{}
	v := Preflight(content)
	if v.State == PreflightReplaceGoalDialog {
		d.Present = true
		d.MarkersMatched = v.MarkersMatched
	}
	if !d.Present {
		return d
	}

	lower := strings.ToLower(content)
	d.Interactive = replaceDialogConfirmRe.MatchString(content)

	// Old-goal-closed proof: the modal offers "keep the current goal" as the
	// alternative to replacing — that option only exists because a current goal
	// is active and Replace would close it. This is the affirmative, observable
	// proof #168 requires before selecting Replace.
	d.OldGoalClosed = strings.Contains(lower, "keep the current goal") ||
		strings.Contains(lower, "replace current goal")

	if m := newObjectiveRe.FindStringSubmatch(content); m != nil {
		d.NewObjective = strings.TrimSpace(stripTrailingBox(m[1]))
	}
	return d
}

// stripTrailingBox trims trailing TUI box-drawing/padding from a captured line so
// a parsed objective doesn't carry the modal's right border.
func stripTrailingBox(s string) string {
	s = strings.TrimRight(s, " ")
	for _, b := range []string{"│", "┃", "|"} {
		s = strings.TrimRight(s, b)
	}
	return strings.TrimRight(s, " ")
}

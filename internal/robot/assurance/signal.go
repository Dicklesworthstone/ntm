// Package assurance defines the shared signal vocabulary that operator
// assurance features use to reason about swarm trust.
//
// A Signal carries one observation about the operator's confidence in
// some aspect of the swarm — classifier accuracy, evidence freshness,
// coordination state, provider liveness, and so on. Each Signal has a
// stable SignalType, a tri-valued SignalStatus, and a list of stable
// ReasonCodes that justify the status.
//
// Stability rules for the constants in this file:
//
//   - String values are an external contract. Once a constant is shipped,
//     its string value is permanent. Add new constants instead of
//     repurposing or renaming existing ones.
//   - Reason codes are namespaced by signal type ("classifier.", "freshness.",
//     etc.) so consumers can group them without parsing.
//   - The zero value of Signal MUST encode a useful "unknown evidence"
//     report — i.e. SignalStatus("") must be interpretable.
package assurance

import (
	"encoding/json"
	"time"
)

// SignalType identifies one class of operator-assurance evidence.
//
// New types should describe a single, falsifiable claim. Avoid catch-alls
// like "general_health"; prefer narrow types so reason codes stay tight.
type SignalType string

const (
	// SignalClassifierConfidence reports how sure a classifier (pane health,
	// rate-limit detection, etc.) is about its current decision.
	SignalClassifierConfidence SignalType = "classifier_confidence"

	// SignalEvidenceFreshness reports how stale the underlying evidence is
	// relative to "now". Old evidence may be correct but warrants caution.
	SignalEvidenceFreshness SignalType = "evidence_freshness"

	// SignalCoordinationRisk reports whether multi-agent coordination
	// state (locks, reservations, leader election) is consistent.
	SignalCoordinationRisk SignalType = "coordination_risk"

	// SignalQuiescenceCandidate reports whether the swarm appears to be
	// at a safe quiet point for snapshot, handoff, or shutdown.
	SignalQuiescenceCandidate SignalType = "quiescence_candidate"

	// SignalProviderDegraded reports whether an upstream provider
	// (CASS, Agent Mail, beads, model API) is in a degraded mode.
	SignalProviderDegraded SignalType = "provider_degraded"

	// SignalReservationContention reports contention on shared file or
	// path reservations across agents.
	SignalReservationContention SignalType = "reservation_contention"

	// SignalCloseoutIntegrity reports whether bead/PR closeout has the
	// expected artifacts (tests, commits, changelog) attached.
	SignalCloseoutIntegrity SignalType = "closeout_integrity"
)

// SignalStatus is the operator-facing rollup for one Signal. Use
// Signal.EffectiveStatus to read it with the zero-value contract applied.
type SignalStatus string

const (
	// SignalStatusUnknown means insufficient evidence to classify. The
	// SignalStatus zero value maps to this value in EffectiveStatus and JSON.
	SignalStatusUnknown SignalStatus = "unknown"

	// SignalStatusHealthy means evidence supports operator confidence.
	SignalStatusHealthy SignalStatus = "healthy"

	// SignalStatusDegraded means evidence supports lowered operator
	// confidence — not a hard failure, but worth surfacing.
	SignalStatusDegraded SignalStatus = "degraded"
)

// ReasonCode is a stable, machine-checkable identifier for *why* a
// Signal reached its Status. Reason codes are dot-namespaced by signal
// type so a list of codes is groupable without parsing each one.
//
// Once shipped, a ReasonCode string is permanent. Rename only by adding
// a new code and deprecating the old one in the registry below.
type ReasonCode string

const (
	// classifier_confidence
	ReasonClassifierLowSampleSize       ReasonCode = "classifier.low_sample_size"
	ReasonClassifierConflictingEvidence ReasonCode = "classifier.conflicting_evidence"
	ReasonClassifierStaleHeuristic      ReasonCode = "classifier.stale_heuristic"

	// evidence_freshness
	ReasonFreshnessStaleSnapshot   ReasonCode = "freshness.stale_snapshot"
	ReasonFreshnessFutureTimestamp ReasonCode = "freshness.future_timestamp"
	ReasonFreshnessMissingTime     ReasonCode = "freshness.missing_time"

	// coordination_risk
	ReasonCoordinationLockContended  ReasonCode = "coordination.lock_contended"
	ReasonCoordinationOrphanLock     ReasonCode = "coordination.orphan_lock"
	ReasonCoordinationLeaderUnstable ReasonCode = "coordination.leader_unstable"

	// quiescence_candidate
	ReasonQuiescenceNoActiveAgents  ReasonCode = "quiescence.no_active_agents"
	ReasonQuiescenceQueueDry        ReasonCode = "quiescence.queue_dry"
	ReasonQuiescencePendingWork     ReasonCode = "quiescence.pending_work"
	ReasonQuiescenceReadyWork       ReasonCode = "quiescence.ready_work"
	ReasonQuiescenceInProgressWork  ReasonCode = "quiescence.in_progress_work"
	ReasonQuiescenceUrgentMail      ReasonCode = "quiescence.urgent_mail"
	ReasonQuiescencePendingAckMail  ReasonCode = "quiescence.pending_ack_mail"
	ReasonQuiescenceTrackerDirty    ReasonCode = "quiescence.tracker_dirty"
	ReasonQuiescenceTrackerUnknown  ReasonCode = "quiescence.tracker_unknown"
	ReasonQuiescenceDirtyWorktree   ReasonCode = "quiescence.dirty_worktree"
	ReasonQuiescenceReviewSaturated ReasonCode = "quiescence.review_saturated"

	// provider_degraded
	ReasonProviderRateLimited ReasonCode = "provider.rate_limited"
	ReasonProviderTimeout     ReasonCode = "provider.timeout"
	ReasonProviderAuthFailed  ReasonCode = "provider.auth_failed"

	// reservation_contention
	ReasonReservationOverdue       ReasonCode = "reservation.overdue"
	ReasonReservationPathConflict  ReasonCode = "reservation.path_conflict"
	ReasonReservationOrphanedAgent ReasonCode = "reservation.orphaned_agent"
	ReasonReservationUnknown       ReasonCode = "reservation.unknown"

	// closeout_integrity
	ReasonCloseoutMissingTests    ReasonCode = "closeout.missing_tests"
	ReasonCloseoutDirtyWorktree   ReasonCode = "closeout.dirty_worktree"
	ReasonCloseoutNoBeadReference ReasonCode = "closeout.no_bead_reference"
)

// Signal carries one operator-assurance observation.
//
// JSON tags are part of the external contract (see signal_test.go). The
// "omitempty" markers on Confidence/Reasons/Evidence/ObservedAt let a
// minimal Signal serialize as a tight {"type":"...","status":"..."}.
type Signal struct {
	// Type is the signal vocabulary entry; required.
	Type SignalType `json:"type"`

	// Status is the operator-facing rollup. Empty string means unknown.
	Status SignalStatus `json:"status"`

	// Confidence is an optional 0.0–1.0 score for how strong the evidence
	// is. 0 is treated as "not provided"; consumers should call
	// EffectiveConfidence() rather than reading the field directly when
	// they need a numeric value.
	Confidence float64 `json:"confidence,omitempty"`

	// Reasons enumerates the stable ReasonCode strings that justify
	// the Status. May be empty when Status is Healthy or Unknown.
	Reasons []ReasonCode `json:"reasons,omitempty"`

	// Evidence is a short human-readable note pointing at the supporting
	// data (file path, bead id, log marker). NOT a place for free-form
	// commentary — keep it pointer-shaped.
	Evidence string `json:"evidence,omitempty"`

	// ObservedAt is the time the underlying evidence was captured. Nil means
	// "now / not specified" and is omitted from JSON.
	ObservedAt *time.Time `json:"observed_at,omitempty"`
}

// EffectiveStatus returns SignalStatusUnknown when Status is the empty
// zero value, so callers don't need to special-case the zero value.
func (s Signal) EffectiveStatus() SignalStatus {
	if s.Status == "" {
		return SignalStatusUnknown
	}
	return s.Status
}

// MarshalJSON applies the zero-value contract to Status while preserving the
// stable external field names.
func (s Signal) MarshalJSON() ([]byte, error) {
	type signalJSON struct {
		Type       SignalType   `json:"type"`
		Status     SignalStatus `json:"status"`
		Confidence float64      `json:"confidence,omitempty"`
		Reasons    []ReasonCode `json:"reasons,omitempty"`
		Evidence   string       `json:"evidence,omitempty"`
		ObservedAt *time.Time   `json:"observed_at,omitempty"`
	}

	return json.Marshal(signalJSON{
		Type:       s.Type,
		Status:     s.EffectiveStatus(),
		Confidence: s.EffectiveConfidence(),
		Reasons:    s.Reasons,
		Evidence:   s.Evidence,
		ObservedAt: s.ObservedAt,
	})
}

// EffectiveConfidence returns the Confidence clamped to [0,1]. A zero
// or negative input becomes 0; anything > 1 becomes 1. Consumers that
// distinguish "no confidence reported" from "confidence is zero"
// should read s.Confidence directly.
func (s Signal) EffectiveConfidence() float64 {
	if s.Confidence <= 0 {
		return 0
	}
	if s.Confidence > 1 {
		return 1
	}
	return s.Confidence
}

// allSignalTypes is the canonical list used by tests to assert
// completeness when new types are added.
var allSignalTypes = []SignalType{
	SignalClassifierConfidence,
	SignalEvidenceFreshness,
	SignalCoordinationRisk,
	SignalQuiescenceCandidate,
	SignalProviderDegraded,
	SignalReservationContention,
	SignalCloseoutIntegrity,
}

// AllSignalTypes returns a copy of the canonical signal-type list. The
// copy is intentional so callers can sort/filter without mutating the
// shared registry.
func AllSignalTypes() []SignalType {
	out := make([]SignalType, len(allSignalTypes))
	copy(out, allSignalTypes)
	return out
}

// KnownSignalType reports whether t is in the stable signal-type registry.
func KnownSignalType(t SignalType) bool {
	for _, known := range allSignalTypes {
		if t == known {
			return true
		}
	}
	return false
}

// allReasonCodes is the canonical reason-code registry. Order is
// grouped by SignalType prefix; new codes should be appended within
// their group and never reordered (tests pin first-and-last).
var allReasonCodes = []ReasonCode{
	ReasonClassifierLowSampleSize,
	ReasonClassifierConflictingEvidence,
	ReasonClassifierStaleHeuristic,

	ReasonFreshnessStaleSnapshot,
	ReasonFreshnessFutureTimestamp,
	ReasonFreshnessMissingTime,

	ReasonCoordinationLockContended,
	ReasonCoordinationOrphanLock,
	ReasonCoordinationLeaderUnstable,

	ReasonQuiescenceNoActiveAgents,
	ReasonQuiescenceQueueDry,
	ReasonQuiescencePendingWork,
	ReasonQuiescenceReadyWork,
	ReasonQuiescenceInProgressWork,
	ReasonQuiescenceUrgentMail,
	ReasonQuiescencePendingAckMail,
	ReasonQuiescenceTrackerDirty,
	ReasonQuiescenceTrackerUnknown,
	ReasonQuiescenceDirtyWorktree,
	ReasonQuiescenceReviewSaturated,

	ReasonProviderRateLimited,
	ReasonProviderTimeout,
	ReasonProviderAuthFailed,

	ReasonReservationOverdue,
	ReasonReservationPathConflict,
	ReasonReservationOrphanedAgent,
	ReasonReservationUnknown,

	ReasonCloseoutMissingTests,
	ReasonCloseoutDirtyWorktree,
	ReasonCloseoutNoBeadReference,
}

// AllReasonCodes returns a copy of the canonical reason-code list.
func AllReasonCodes() []ReasonCode {
	out := make([]ReasonCode, len(allReasonCodes))
	copy(out, allReasonCodes)
	return out
}

// KnownReasonCode reports whether code is in the stable reason-code registry.
func KnownReasonCode(code ReasonCode) bool {
	for _, known := range allReasonCodes {
		if code == known {
			return true
		}
	}
	return false
}

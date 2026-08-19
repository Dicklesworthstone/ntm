package assurance

import (
	"time"
)

// DigestStatus is the rolled-up operator-visible state. It is the
// single field a dashboard can colour-code without reading any of
// the per-section detail.
type DigestStatus string

const (
	// DigestStatusHealthy means no critical or warning findings are
	// outstanding and the swarm is at a safe quiet point.
	DigestStatusHealthy DigestStatus = "healthy"

	// DigestStatusDegraded means at least one warning condition or
	// degraded provider exists, but no hard blocker. The operator
	// can proceed cautiously.
	DigestStatusDegraded DigestStatus = "degraded"

	// DigestStatusUnsafe means a critical condition exists that
	// should block commit/handoff/shutdown until resolved.
	DigestStatusUnsafe DigestStatus = "unsafe"
)

// DigestSeverity rolls all findings up into one priority token. The
// strings sort lexicographically ascending for stable JSON output:
// "critical" < "info" < "ok" < "warning" alphabetically, so we use
// a numeric rank for ordering and emit the strings as-is.
type DigestSeverity string

const (
	DigestSeverityCritical DigestSeverity = "critical"
	DigestSeverityWarning  DigestSeverity = "warning"
	DigestSeverityInfo     DigestSeverity = "info"
	DigestSeverityOK       DigestSeverity = "ok"
)

// DigestFinding is one stripped-down finding the digest evaluator
// folds into the rollup. Callers convert from richer per-source
// types (commitlint.Finding, identityhygiene.Finding, ...) by
// mapping their severity to the four-value DigestSeverity scale and
// providing a stable Code + Source.
type DigestFinding struct {
	Code     string         `json:"code"`
	Severity DigestSeverity `json:"severity"`
	Source   string         `json:"source"` // "commit_readiness" | "identity_hygiene" | "slo" | "evidence_budget" | ...
	Hint     string         `json:"hint,omitempty"`
}

// DigestSLO captures the SLO summary in the most compact way
// the digest cares about. The full distribution lives elsewhere;
// the digest only needs a healthy/unhealthy bit + optional notes.
type DigestSLO struct {
	Healthy bool     `json:"healthy"`
	Notes   []string `json:"notes,omitempty"`
}

// DigestInput is the full set of evidence ComputeDigest reduces.
type DigestInput struct {
	// Quiescence is the existing per-swarm quiescence assessment.
	// Its zero value (empty State, zero Signal) maps to "unknown",
	// which the digest treats as a non-blocker.
	Quiescence QuiescenceAssessment

	// CoordinationFindings folds in every commitlint / identity
	// hygiene / per-surface finding the caller could collect. The
	// digest only inspects Severity + Source + Hint; full text
	// remains in the per-source detail of the snapshot.
	CoordinationFindings []DigestFinding

	// SLO is a compact health bit. Zero-value (Healthy=false,
	// Notes=nil) is interpreted as "unknown SLO" and does not
	// trigger a degraded status by itself; callers should set
	// Healthy=true explicitly when a snapshot was taken.
	SLO DigestSLO

	// DegradedSources lists provider names that returned anything
	// other than a healthy result (slow, unavailable, malformed,
	// stale, partial). One non-empty entry triggers degraded status
	// even with no findings.
	DegradedSources []string

	// Now lets tests pin the wall clock. Defaults to time.Now().
	Now time.Time
}

// Digest is the rolled-up operator-visible summary. It is small on
// purpose so a TUI tile and a robot JSON consumer can both render
// it without inspecting every raw section.
type Digest struct {
	GeneratedAt         time.Time       `json:"generated_at"`
	Status              DigestStatus    `json:"status"`
	HighestSeverity     DigestSeverity  `json:"highest_severity"`
	Quiescence          QuiescenceState `json:"quiescence,omitempty"`
	DegradedSources     []string        `json:"degraded_sources,omitempty"`
	ReasonCodes         []ReasonCode    `json:"reason_codes,omitempty"`
	SuggestedNextAction string          `json:"suggested_next_action"`
	Summary             string          `json:"summary"`
	Counts              DigestCounts    `json:"counts"`
}

// DigestCounts breaks down findings by severity so a dashboard tile
// can show "3 critical / 1 warning / 0 info" without re-traversing
// the per-source list.
type DigestCounts struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

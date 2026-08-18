// Package swarmslo computes operator-facing service metrics from
// existing NTM coordination signals: Agent Mail acks, Beads status
// transitions, and Agent Mail file reservations.
//
// The package is pure: callers gather events from the durable
// stores (state timeline persister, agentmail client, beads JSONL/DB,
// reservation list) and feed them in as plain views. The reducer
// computes count + p50/p95/max distributions per metric and surfaces
// missing_source warnings when a particular signal could not be
// loaded.
//
// First slice is read-only and stateless — there is no daemon, no
// background sampler, and no on-disk emission. A future slice can
// schedule periodic computation; this slice answers a single
// "snapshot of the last N hours" query in one function call.
//
// See bd-3v1gs.7.
package swarmslo

import (
	"time"
)

// MailEvent is one Agent Mail message used by time_to_first_ack.
// Callers gather these from the inbox; AckedAt is nil for messages
// that are still unread/unacked.
type MailEvent struct {
	ID          int
	CreatedAt   time.Time
	AckedAt     *time.Time
	AckRequired bool
	From        string
	To          string
}

// BeadTransition is one status change for a bead. The reducer pairs
// transitions per BeadID to compute ready→claim and claim→close
// durations. "ready" is the marker for a bead that newly entered the
// br ready set; "in_progress" is the claim event; "closed" is the
// terminal event.
type BeadTransition struct {
	BeadID    string
	Status    string // "ready" | "in_progress" | "closed" | other
	EnteredAt time.Time
}

// ReservationWindow is one agent's hold on a path pattern, used by
// reservation_contention to compute the wait time other agents
// experienced before they could acquire the same pattern.
type ReservationWindow struct {
	PathPattern string
	AgentName   string
	AcquiredAt  time.Time
	ReleasedAt  *time.Time // nil means still held
}

// Inputs is the full set of evidence the SLO reducer consumes.
type Inputs struct {
	Mail         []MailEvent
	Beads        []BeadTransition
	Reservations []ReservationWindow

	// MissingSources lists the named sources the caller could NOT
	// load. The reducer surfaces these into Summary.Warnings so
	// consumers see partial-data states explicitly rather than
	// silently scoring a "0 events" distribution.
	MissingSources []string

	// Now defaults to time.Now() when zero. Used only for
	// stale_in_progress age computation.
	Now time.Time
}

// Distribution is the per-metric summary shape. All durations are in
// seconds (float64) so the JSON envelope stays consumer-friendly.
type Distribution struct {
	Count       int     `json:"count"`
	P50Seconds  float64 `json:"p50_seconds"`
	P95Seconds  float64 `json:"p95_seconds"`
	MaxSeconds  float64 `json:"max_seconds"`
	MeanSeconds float64 `json:"mean_seconds"`
	// Pending counts samples that the metric is waiting on but
	// could not measure yet. Currently used by time_to_first_ack
	// for ack-required messages whose AckedAt is still nil — the
	// docstring promised to surface this and the value used to be
	// silently discarded (bd-h1i8z). 0 means "no pending state for
	// this metric" or "this metric has no pending concept" — it is
	// omitted from JSON in either case.
	Pending int  `json:"pending,omitempty"`
	Missing bool `json:"missing_source,omitempty"`
}

// Summary is the operator-facing JSON envelope.
type Summary struct {
	GeneratedAt           time.Time    `json:"generated_at"`
	TimeToFirstAck        Distribution `json:"time_to_first_ack"`
	ReadyToClaim          Distribution `json:"ready_to_claim"`
	ClaimToCloseout       Distribution `json:"claim_to_closeout"`
	ReservationContention Distribution `json:"reservation_contention"`
	StaleInProgress       Distribution `json:"stale_in_progress"`
	Warnings              []string     `json:"warnings,omitempty"`
}

// RecommendationSchemaVersion is the stable JSON contract for the
// advisory scheduling layer derived from a Summary.
const RecommendationSchemaVersion = "ntm.swarm.slo_recommendations.v1"

// RecommendationAction is an advisory-only scheduling change. These
// values are intentionally operational verbs that a robot consumer can
// route to dashboards, proof bundles, or future scheduler experiments
// without mutating live scheduling state in this package.
type RecommendationAction string

const (
	RecommendationContinue         RecommendationAction = "continue_current_schedule"
	RecommendationAddReviewer      RecommendationAction = "add_reviewer"
	RecommendationSplitBead        RecommendationAction = "split_bead"
	RecommendationReduceFanOut     RecommendationAction = "reduce_fan_out"
	RecommendationRenewReservation RecommendationAction = "renew_reservation"
	RecommendationStopIdlePane     RecommendationAction = "stop_idle_pane"
	RecommendationRefreshSource    RecommendationAction = "refresh_source"
)

// RecommendationSeverity gives consumers a coarse ordering bucket
// while keeping the recommendation itself advisory.
type RecommendationSeverity string

const (
	RecommendationSeverityAction RecommendationSeverity = "action"
	RecommendationSeverityWatch  RecommendationSeverity = "watch"
	RecommendationSeverityOK     RecommendationSeverity = "ok"
)

// RecommendationReasonCode is a stable machine-readable reason for a
// scheduling recommendation. It is local to swarmslo so the operator
// assurance reason registry does not need churn for every SLO policy
// tweak.
type RecommendationReasonCode string

const (
	ReasonSLOHealthy                   RecommendationReasonCode = "slo.healthy"
	ReasonSLOMissingSource             RecommendationReasonCode = "slo.missing_source"
	ReasonSLOInsufficientData          RecommendationReasonCode = "slo.insufficient_data"
	ReasonSLOAckLatencyHigh            RecommendationReasonCode = "slo.time_to_first_ack.high_p95"
	ReasonSLOAckPending                RecommendationReasonCode = "slo.time_to_first_ack.pending"
	ReasonSLOReadyToClaimHigh          RecommendationReasonCode = "slo.ready_to_claim.high_p95"
	ReasonSLOClaimToCloseoutHigh       RecommendationReasonCode = "slo.claim_to_closeout.high_p95"
	ReasonSLOReservationContentionHigh RecommendationReasonCode = "slo.reservation_contention.high_p95"
	ReasonSLOStaleInProgressHigh       RecommendationReasonCode = "slo.stale_in_progress.high_p95"
)

// RecommendationThresholds are the SLO budgets used by Recommend.
// Zero values are filled from DefaultRecommendationThresholds.
type RecommendationThresholds struct {
	TimeToFirstAckP95Seconds        float64 `json:"time_to_first_ack_p95_seconds"`
	PendingAckCount                 int     `json:"pending_ack_count"`
	ReadyToClaimP95Seconds          float64 `json:"ready_to_claim_p95_seconds"`
	ClaimToCloseoutP95Seconds       float64 `json:"claim_to_closeout_p95_seconds"`
	ReservationContentionP95Seconds float64 `json:"reservation_contention_p95_seconds"`
	StaleInProgressP95Seconds       float64 `json:"stale_in_progress_p95_seconds"`
}

type RecommendationInput struct {
	Summary    Summary                  `json:"summary"`
	Thresholds RecommendationThresholds `json:"thresholds,omitempty"`
}

// Recommendation is one reason-coded scheduling recommendation.
type Recommendation struct {
	Metric         string                     `json:"metric"`
	P95Seconds     float64                    `json:"p95_seconds"`
	Pending        int                        `json:"pending"`
	Threshold      float64                    `json:"threshold"`
	Recommendation RecommendationAction       `json:"recommendation"`
	Confidence     float64                    `json:"confidence"`
	Severity       RecommendationSeverity     `json:"severity"`
	ReasonCodes    []RecommendationReasonCode `json:"reason_codes"`
	Evidence       string                     `json:"evidence,omitempty"`
}

// RecommendationLogRow is the projection that can be emitted through
// slog, robot JSON, or a proof bundle without reinterpreting the richer
// Recommendation shape.
type RecommendationLogRow struct {
	Metric         string                     `json:"metric"`
	P95Seconds     float64                    `json:"p95_seconds"`
	Pending        int                        `json:"pending"`
	Threshold      float64                    `json:"threshold"`
	Recommendation RecommendationAction       `json:"recommendation"`
	Confidence     float64                    `json:"confidence"`
	ReasonCodes    []RecommendationReasonCode `json:"reason_codes"`
}

// RecommendationSummary is the robot-friendly advisory envelope.
type RecommendationSummary struct {
	SchemaVersion   string                 `json:"schema_version"`
	GeneratedAt     time.Time              `json:"generated_at"`
	Healthy         bool                   `json:"healthy"`
	Recommendations []Recommendation       `json:"recommendations"`
	LogRows         []RecommendationLogRow `json:"log_rows"`
	Warnings        []string               `json:"warnings,omitempty"`
}

// SchedulingRecommendationBundle packages the source SLO summary with
// the recommendation envelope so proof-bundle producers can persist the
// exact evidence and policy output together.
type SchedulingRecommendationBundle struct {
	SchemaVersion   string                `json:"schema_version"`
	GeneratedAt     time.Time             `json:"generated_at"`
	SLO             Summary               `json:"slo"`
	Recommendations RecommendationSummary `json:"recommendations"`
}

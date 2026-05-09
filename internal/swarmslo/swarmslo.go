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
	"math"
	"sort"
	"strconv"
	"strings"
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

// DefaultRecommendationThresholds returns conservative operator-facing
// budgets. Callers can override any non-zero field for local policy.
func DefaultRecommendationThresholds() RecommendationThresholds {
	return RecommendationThresholds{
		TimeToFirstAckP95Seconds:        5 * 60,
		PendingAckCount:                 0,
		ReadyToClaimP95Seconds:          10 * 60,
		ClaimToCloseoutP95Seconds:       2 * 60 * 60,
		ReservationContentionP95Seconds: 60,
		StaleInProgressP95Seconds:       2 * 60 * 60,
	}
}

// RecommendationInput is the advisory scheduling evaluator input.
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

// Compute reduces inputs to a Summary. Pure: never reads files,
// never mutates state.
func Compute(in Inputs) Summary {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}

	out := Summary{
		GeneratedAt:           now.UTC(),
		TimeToFirstAck:        computeAckLatencies(in.Mail),
		ReadyToClaim:          computeReadyToClaim(in.Beads),
		ClaimToCloseout:       computeClaimToCloseout(in.Beads),
		ReservationContention: computeReservationContention(in.Reservations),
		StaleInProgress:       computeStaleInProgress(in.Beads, now),
	}

	// MissingSources flags propagate into per-metric Missing booleans
	// so consumers can grey out the relevant tile rather than read
	// "p95=0".
	for _, raw := range in.MissingSources {
		s := strings.TrimSpace(strings.ToLower(raw))
		switch s {
		case "mail", "agentmail", "agent_mail":
			out.TimeToFirstAck.Missing = true
		case "beads", "br":
			out.ReadyToClaim.Missing = true
			out.ClaimToCloseout.Missing = true
			out.StaleInProgress.Missing = true
		case "reservations", "agentmail_reservations":
			out.ReservationContention.Missing = true
		}
		if raw != "" {
			out.Warnings = append(out.Warnings, raw+" unavailable: source not loaded")
		}
	}
	return out
}

// Recommend converts SLO distributions into advisory scheduling
// recommendations. It is pure: it does not mutate scheduler state or
// emit logs itself.
func Recommend(in RecommendationInput) RecommendationSummary {
	summary := in.Summary
	thresholds := normalizeRecommendationThresholds(in.Thresholds)

	out := RecommendationSummary{
		SchemaVersion: RecommendationSchemaVersion,
		GeneratedAt:   summary.GeneratedAt,
		Warnings:      append([]string(nil), summary.Warnings...),
	}

	out.addMissingRecommendation("time_to_first_ack", summary.TimeToFirstAck)
	out.addMissingRecommendation("ready_to_claim", summary.ReadyToClaim)
	out.addMissingRecommendation("claim_to_closeout", summary.ClaimToCloseout)
	out.addMissingRecommendation("reservation_contention", summary.ReservationContention)
	out.addMissingRecommendation("stale_in_progress", summary.StaleInProgress)

	if r, ok := recommendAck(summary.TimeToFirstAck, thresholds); ok {
		out.Recommendations = append(out.Recommendations, r)
	}
	if r, ok := recommendP95(
		"ready_to_claim",
		summary.ReadyToClaim,
		thresholds.ReadyToClaimP95Seconds,
		RecommendationAddReviewer,
		ReasonSLOReadyToClaimHigh,
	); ok {
		out.Recommendations = append(out.Recommendations, r)
	}
	if r, ok := recommendP95(
		"claim_to_closeout",
		summary.ClaimToCloseout,
		thresholds.ClaimToCloseoutP95Seconds,
		RecommendationSplitBead,
		ReasonSLOClaimToCloseoutHigh,
	); ok {
		out.Recommendations = append(out.Recommendations, r)
	}
	if r, ok := recommendP95(
		"reservation_contention",
		summary.ReservationContention,
		thresholds.ReservationContentionP95Seconds,
		RecommendationRenewReservation,
		ReasonSLOReservationContentionHigh,
	); ok {
		out.Recommendations = append(out.Recommendations, r)
	}
	if r, ok := recommendP95(
		"stale_in_progress",
		summary.StaleInProgress,
		thresholds.StaleInProgressP95Seconds,
		RecommendationStopIdlePane,
		ReasonSLOStaleInProgressHigh,
	); ok {
		out.Recommendations = append(out.Recommendations, r)
	}

	if len(out.Recommendations) == 0 {
		out.Healthy = true
		out.Recommendations = append(out.Recommendations, Recommendation{
			Metric:         "scheduling",
			Recommendation: RecommendationContinue,
			Confidence:     0.9,
			Severity:       RecommendationSeverityOK,
			ReasonCodes:    []RecommendationReasonCode{ReasonSLOHealthy},
			Evidence:       "all_slo_metrics_within_thresholds",
		})
	}

	sortRecommendations(out.Recommendations)
	out.LogRows = buildRecommendationLogRows(out.Recommendations)
	out.Warnings = uniqueSortedStrings(out.Warnings)
	return out
}

// BuildSchedulingRecommendationBundle returns the versioned JSON shape
// intended for support/proof bundles.
func BuildSchedulingRecommendationBundle(summary Summary, thresholds RecommendationThresholds) SchedulingRecommendationBundle {
	recs := Recommend(RecommendationInput{Summary: summary, Thresholds: thresholds})
	return SchedulingRecommendationBundle{
		SchemaVersion:   RecommendationSchemaVersion,
		GeneratedAt:     summary.GeneratedAt,
		SLO:             summary,
		Recommendations: recs,
	}
}

func normalizeRecommendationThresholds(in RecommendationThresholds) RecommendationThresholds {
	def := DefaultRecommendationThresholds()
	if in.TimeToFirstAckP95Seconds <= 0 {
		in.TimeToFirstAckP95Seconds = def.TimeToFirstAckP95Seconds
	}
	if in.PendingAckCount < 0 {
		in.PendingAckCount = def.PendingAckCount
	}
	if in.ReadyToClaimP95Seconds <= 0 {
		in.ReadyToClaimP95Seconds = def.ReadyToClaimP95Seconds
	}
	if in.ClaimToCloseoutP95Seconds <= 0 {
		in.ClaimToCloseoutP95Seconds = def.ClaimToCloseoutP95Seconds
	}
	if in.ReservationContentionP95Seconds <= 0 {
		in.ReservationContentionP95Seconds = def.ReservationContentionP95Seconds
	}
	if in.StaleInProgressP95Seconds <= 0 {
		in.StaleInProgressP95Seconds = def.StaleInProgressP95Seconds
	}
	return in
}

func (out *RecommendationSummary) addMissingRecommendation(metric string, d Distribution) {
	if !d.Missing {
		return
	}
	out.Recommendations = append(out.Recommendations, Recommendation{
		Metric:         metric,
		P95Seconds:     d.P95Seconds,
		Pending:        d.Pending,
		Recommendation: RecommendationRefreshSource,
		Confidence:     0.4,
		Severity:       RecommendationSeverityWatch,
		ReasonCodes:    []RecommendationReasonCode{ReasonSLOMissingSource},
		Evidence:       metric + "_source_missing",
	})
	out.Warnings = append(out.Warnings, metric+" unavailable: source not loaded")
}

func recommendAck(d Distribution, thresholds RecommendationThresholds) (Recommendation, bool) {
	if d.Missing {
		return Recommendation{}, false
	}
	reasons := make([]RecommendationReasonCode, 0, 2)
	confidence := 0.0
	threshold := thresholds.TimeToFirstAckP95Seconds
	if d.Count > 0 && d.P95Seconds > thresholds.TimeToFirstAckP95Seconds {
		reasons = append(reasons, ReasonSLOAckLatencyHigh)
		confidence = maxFloat(confidence, confidenceForRatio(d.P95Seconds, thresholds.TimeToFirstAckP95Seconds))
	}
	if d.Pending > thresholds.PendingAckCount {
		reasons = append(reasons, ReasonSLOAckPending)
		confidence = maxFloat(confidence, confidenceForPending(d.Pending, thresholds.PendingAckCount))
		if len(reasons) == 1 {
			threshold = float64(thresholds.PendingAckCount)
		}
	}
	if len(reasons) == 0 {
		return Recommendation{}, false
	}
	return Recommendation{
		Metric:         "time_to_first_ack",
		P95Seconds:     d.P95Seconds,
		Pending:        d.Pending,
		Threshold:      threshold,
		Recommendation: RecommendationReduceFanOut,
		Confidence:     confidence,
		Severity:       RecommendationSeverityAction,
		ReasonCodes:    reasons,
		Evidence:       distributionEvidence(d),
	}, true
}

func recommendP95(metric string, d Distribution, threshold float64, action RecommendationAction, reason RecommendationReasonCode) (Recommendation, bool) {
	if d.Missing || d.Count == 0 || d.P95Seconds <= threshold {
		return Recommendation{}, false
	}
	return Recommendation{
		Metric:         metric,
		P95Seconds:     d.P95Seconds,
		Pending:        d.Pending,
		Threshold:      threshold,
		Recommendation: action,
		Confidence:     confidenceForRatio(d.P95Seconds, threshold),
		Severity:       RecommendationSeverityAction,
		ReasonCodes:    []RecommendationReasonCode{reason},
		Evidence:       distributionEvidence(d),
	}, true
}

func sortRecommendations(recs []Recommendation) {
	sort.SliceStable(recs, func(i, j int) bool {
		a := recs[i]
		b := recs[j]
		if severityRank(a.Severity) != severityRank(b.Severity) {
			return severityRank(a.Severity) < severityRank(b.Severity)
		}
		if metricRank(a.Metric) != metricRank(b.Metric) {
			return metricRank(a.Metric) < metricRank(b.Metric)
		}
		if a.Recommendation != b.Recommendation {
			return a.Recommendation < b.Recommendation
		}
		return strings.Join(reasonStrings(a.ReasonCodes), ",") < strings.Join(reasonStrings(b.ReasonCodes), ",")
	})
}

func buildRecommendationLogRows(recs []Recommendation) []RecommendationLogRow {
	rows := make([]RecommendationLogRow, 0, len(recs))
	for _, r := range recs {
		rows = append(rows, RecommendationLogRow{
			Metric:         r.Metric,
			P95Seconds:     r.P95Seconds,
			Pending:        r.Pending,
			Threshold:      r.Threshold,
			Recommendation: r.Recommendation,
			Confidence:     r.Confidence,
			ReasonCodes:    append([]RecommendationReasonCode(nil), r.ReasonCodes...),
		})
	}
	return rows
}

func severityRank(s RecommendationSeverity) int {
	switch s {
	case RecommendationSeverityAction:
		return 0
	case RecommendationSeverityWatch:
		return 1
	case RecommendationSeverityOK:
		return 2
	default:
		return 3
	}
}

func metricRank(metric string) int {
	switch metric {
	case "time_to_first_ack":
		return 10
	case "ready_to_claim":
		return 20
	case "claim_to_closeout":
		return 30
	case "reservation_contention":
		return 40
	case "stale_in_progress":
		return 50
	case "scheduling":
		return 90
	default:
		return 100
	}
}

func confidenceForRatio(observed, threshold float64) float64 {
	if threshold <= 0 {
		if observed > 0 {
			return 0.8
		}
		return 0
	}
	ratio := observed / threshold
	switch {
	case ratio >= 2:
		return 0.95
	case ratio >= 1.5:
		return 0.85
	default:
		return 0.75
	}
}

func confidenceForPending(pending, threshold int) float64 {
	over := pending - threshold
	if over <= 0 {
		return 0
	}
	confidence := 0.75 + float64(over)*0.05
	if confidence > 0.95 {
		confidence = 0.95
	}
	return round3(confidence)
}

func distributionEvidence(d Distribution) string {
	parts := []string{
		"count=" + strconv.Itoa(d.Count),
		"p95=" + strconv.FormatFloat(d.P95Seconds, 'f', -1, 64),
	}
	if d.Pending > 0 {
		parts = append(parts, "pending="+strconv.Itoa(d.Pending))
	}
	return strings.Join(parts, " ")
}

func reasonStrings(codes []RecommendationReasonCode) []string {
	out := make([]string, len(codes))
	for i, code := range codes {
		out[i] = string(code)
	}
	return out
}

func uniqueSortedStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// computeAckLatencies measures (AckedAt - CreatedAt) over messages
// that required an ack and have one. Unacked ack-required messages
// are reported via Distribution.Pending so consumers can distinguish
// "no messages at all" from "many messages, all still pending."
func computeAckLatencies(events []MailEvent) Distribution {
	var seconds []float64
	pending := 0
	for _, m := range events {
		if !m.AckRequired {
			continue
		}
		if m.AckedAt == nil {
			pending++
			continue
		}
		if m.CreatedAt.IsZero() || m.AckedAt.IsZero() {
			continue
		}
		dt := m.AckedAt.Sub(m.CreatedAt).Seconds()
		if dt < 0 {
			continue
		}
		seconds = append(seconds, dt)
	}
	d := distributionFromSeconds(seconds)
	d.Pending = pending
	return d
}

// computeReadyToClaim pairs each "ready" transition with the next
// "in_progress" transition for the same bead.
func computeReadyToClaim(transitions []BeadTransition) Distribution {
	byBead := groupBeadTransitions(transitions)
	var seconds []float64
	for _, ts := range byBead {
		var readyAt *time.Time
		for _, t := range ts {
			tt := t
			switch t.Status {
			case "ready":
				ra := tt.EnteredAt
				readyAt = &ra
			case "in_progress":
				if readyAt == nil {
					continue
				}
				if t.EnteredAt.Before(*readyAt) {
					continue
				}
				seconds = append(seconds, t.EnteredAt.Sub(*readyAt).Seconds())
				readyAt = nil
			}
		}
	}
	return distributionFromSeconds(seconds)
}

// computeClaimToCloseout pairs each "in_progress" with the next
// "closed" transition for the same bead.
func computeClaimToCloseout(transitions []BeadTransition) Distribution {
	byBead := groupBeadTransitions(transitions)
	var seconds []float64
	for _, ts := range byBead {
		var claimedAt *time.Time
		for _, t := range ts {
			tt := t
			switch t.Status {
			case "in_progress":
				ca := tt.EnteredAt
				claimedAt = &ca
			case "closed":
				if claimedAt == nil {
					continue
				}
				if t.EnteredAt.Before(*claimedAt) {
					continue
				}
				seconds = append(seconds, t.EnteredAt.Sub(*claimedAt).Seconds())
				claimedAt = nil
			}
		}
	}
	return distributionFromSeconds(seconds)
}

// computeStaleInProgress measures (now - last_in_progress) for any
// bead whose most recent transition is in_progress (i.e. it has not
// yet been closed).
func computeStaleInProgress(transitions []BeadTransition, now time.Time) Distribution {
	byBead := groupBeadTransitions(transitions)
	var seconds []float64
	for _, ts := range byBead {
		if len(ts) == 0 {
			continue
		}
		last := ts[len(ts)-1]
		if last.Status != "in_progress" {
			continue
		}
		if last.EnteredAt.IsZero() || last.EnteredAt.After(now) {
			continue
		}
		seconds = append(seconds, now.Sub(last.EnteredAt).Seconds())
	}
	return distributionFromSeconds(seconds)
}

// computeReservationContention groups reservation windows by their
// path pattern and, for each subsequent reservation under the same
// pattern by a *different* agent, records the gap from the prior
// reservation's release to the new one's acquisition. Adjacent same-
// agent reservations do not count (a renewal is not contention).
func computeReservationContention(windows []ReservationWindow) Distribution {
	byPattern := make(map[string][]ReservationWindow, len(windows))
	for _, w := range windows {
		byPattern[w.PathPattern] = append(byPattern[w.PathPattern], w)
	}

	var seconds []float64
	for _, ws := range byPattern {
		sort.SliceStable(ws, func(i, j int) bool {
			return ws[i].AcquiredAt.Before(ws[j].AcquiredAt)
		})
		for i := 1; i < len(ws); i++ {
			prev := ws[i-1]
			cur := ws[i]
			if prev.AgentName == cur.AgentName {
				continue
			}
			if prev.ReleasedAt == nil {
				// Still held when the next acquired — degenerate or
				// concurrent shared lock; skip rather than emit a
				// negative value.
				continue
			}
			gap := cur.AcquiredAt.Sub(*prev.ReleasedAt).Seconds()
			if gap < 0 {
				continue
			}
			seconds = append(seconds, gap)
		}
	}
	return distributionFromSeconds(seconds)
}

// groupBeadTransitions returns transitions grouped by bead id, each
// group sorted by EnteredAt ascending. Empty/whitespace bead ids are
// dropped.
func groupBeadTransitions(transitions []BeadTransition) map[string][]BeadTransition {
	byBead := make(map[string][]BeadTransition)
	for _, t := range transitions {
		id := strings.TrimSpace(t.BeadID)
		if id == "" {
			continue
		}
		byBead[id] = append(byBead[id], t)
	}
	for id, ts := range byBead {
		sort.SliceStable(ts, func(i, j int) bool {
			return ts[i].EnteredAt.Before(ts[j].EnteredAt)
		})
		byBead[id] = ts
	}
	return byBead
}

// distributionFromSeconds reduces a slice of latencies to count,
// mean, p50, p95, and max. Returns a zero Distribution when empty.
func distributionFromSeconds(values []float64) Distribution {
	if len(values) == 0 {
		return Distribution{}
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	sum := 0.0
	for _, v := range sorted {
		sum += v
	}
	return Distribution{
		Count:       len(sorted),
		MeanSeconds: round3(sum / float64(len(sorted))),
		P50Seconds:  round3(percentile(sorted, 50)),
		P95Seconds:  round3(percentile(sorted, 95)),
		MaxSeconds:  round3(sorted[len(sorted)-1]),
	}
}

// percentile expects a sorted slice and returns the value at the
// requested percentile using nearest-rank.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	if p <= 0 {
		return sorted[0]
	}
	idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// round3 quantizes a float to 3 decimals so JSON output stays stable
// across platforms with subtly different floating-point widening.
func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}

package reservationsim

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	// ReservationAdvisorSchemaVersion identifies the stable proof-mode
	// reservation risk report shape.
	ReservationAdvisorSchemaVersion = "ntm.reservation_risk_advisor.v1"

	ReservationActionRenew         = "renew"
	ReservationActionMessageHolder = "message_holder"
	ReservationActionNarrow        = "narrow_reservation"
	ReservationActionAskHuman      = "ask_human"
)

const (
	staleReservationAge = 2 * time.Hour
	inactiveHolderAge   = time.Hour
	shortTTL            = 15 * time.Minute
)

// ReservationRiskInput is a stable, source-agnostic reservation snapshot
// for proof-mode risk scoring.
type ReservationRiskInput struct {
	ID          int       `json:"id"`
	PathPattern string    `json:"path_pattern"`
	AgentName   string    `json:"agent_name"`
	Exclusive   bool      `json:"exclusive"`
	Reason      string    `json:"reason,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// ReservationAdvisorOptions configures proof-mode reservation advice.
type ReservationAdvisorOptions struct {
	Now                     time.Time
	AgentMailUnavailable    bool
	AgentMailError          string
	HolderLastActive        map[string]time.Time
	StaleInProgressByHolder map[string]bool
	StaleInProgressByReason map[string]bool
}

// ReservationAdvisorReport is the JSON-friendly advisor result.
type ReservationAdvisorReport struct {
	SchemaVersion      string                      `json:"schema_version"`
	GeneratedAt        time.Time                   `json:"generated_at"`
	Mode               string                      `json:"mode"`
	AgentMailAvailable bool                        `json:"agent_mail_available"`
	Warnings           []string                    `json:"warnings,omitempty"`
	Recommendations    []ReservationRecommendation `json:"recommendations"`
	LogRows            []ReservationAdvisorLogRow  `json:"log_rows"`
}

// ReservationRecommendation describes one proof-mode safe action.
type ReservationRecommendation struct {
	ReservationID int      `json:"reservation_id"`
	PathPattern   string   `json:"path_pattern"`
	Holder        string   `json:"holder"`
	RiskScore     int      `json:"risk_score"`
	Risk          string   `json:"risk"`
	Confidence    float64  `json:"confidence"`
	Action        string   `json:"action"`
	Evidence      []string `json:"evidence"`
	ReasonCodes   []string `json:"reason_codes"`
}

// ReservationAdvisorLogRow contains the audit fields operators need.
type ReservationAdvisorLogRow struct {
	ReservationID int     `json:"reservation_id"`
	PathPattern   string  `json:"path_pattern"`
	Holder        string  `json:"holder"`
	WorktreePath  string  `json:"worktree_path"`
	RiskScore     int     `json:"risk_score"`
	Confidence    float64 `json:"confidence"`
	Action        string  `json:"action"`
}

// AdviseReservations scores active reservations and emits proof-mode actions.
func AdviseReservations(reservations []ReservationRiskInput, opts ReservationAdvisorOptions) ReservationAdvisorReport {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	report := ReservationAdvisorReport{
		SchemaVersion:      ReservationAdvisorSchemaVersion,
		GeneratedAt:        now.UTC(),
		Mode:               "proof",
		AgentMailAvailable: !opts.AgentMailUnavailable,
		Recommendations:    []ReservationRecommendation{},
		LogRows:            []ReservationAdvisorLogRow{},
	}
	if opts.AgentMailUnavailable {
		warning := "agent_mail_unavailable"
		if !reservationTextEmpty(opts.AgentMailError) {
			warning += ": " + strings.TrimSpace(opts.AgentMailError)
		}
		report.Warnings = append(report.Warnings, warning)
		return report
	}

	for _, r := range reservations {
		rec := scoreReservation(r, reservations, opts, now)
		report.Recommendations = append(report.Recommendations, rec)
		report.LogRows = append(report.LogRows, ReservationAdvisorLogRow{
			ReservationID: rec.ReservationID,
			PathPattern:   rec.PathPattern,
			Holder:        rec.Holder,
			RiskScore:     rec.RiskScore,
			Confidence:    rec.Confidence,
			Action:        rec.Action,
		})
	}

	sort.SliceStable(report.Recommendations, func(i, j int) bool {
		a, b := report.Recommendations[i], report.Recommendations[j]
		if a.RiskScore != b.RiskScore {
			return a.RiskScore > b.RiskScore
		}
		if cmp := strings.Compare(a.PathPattern, b.PathPattern); cmp != 0 {
			return cmp < 0
		}
		return strings.Compare(a.Holder, b.Holder) < 0
	})
	sort.SliceStable(report.LogRows, func(i, j int) bool {
		a, b := report.LogRows[i], report.LogRows[j]
		if a.RiskScore != b.RiskScore {
			return a.RiskScore > b.RiskScore
		}
		if cmp := strings.Compare(a.PathPattern, b.PathPattern); cmp != 0 {
			return cmp < 0
		}
		return strings.Compare(a.Holder, b.Holder) < 0
	})

	return report
}

func scoreReservation(r ReservationRiskInput, all []ReservationRiskInput, opts ReservationAdvisorOptions, now time.Time) ReservationRecommendation {
	score := 5
	confidence := 0.45
	action := ReservationActionMessageHolder
	evidence := []string{}
	reasons := []string{}

	if r.Exclusive {
		score += 5
		evidence = append(evidence, "exclusive=true")
		reasons = append(reasons, "exclusive_reservation")
	}

	if !r.CreatedAt.IsZero() {
		age := now.Sub(r.CreatedAt)
		if age < 0 {
			age = 0
		}
		confidence += 0.1
		evidence = append(evidence, fmt.Sprintf("age_minutes=%d", int(age.Minutes())))
		if age >= staleReservationAge {
			score += 35
			reasons = append(reasons, "stale_reservation")
			action = ReservationActionMessageHolder
		}
	}

	if !r.ExpiresAt.IsZero() {
		remaining := r.ExpiresAt.Sub(now)
		confidence += 0.1
		evidence = append(evidence, fmt.Sprintf("ttl_remaining_minutes=%d", int(remaining.Minutes())))
		if remaining <= 0 {
			score += 50
			reasons = append(reasons, "expired_ttl")
			action = ReservationActionRenew
		} else if remaining <= shortTTL {
			score += 30
			reasons = append(reasons, "short_ttl")
			action = ReservationActionRenew
		}
	}

	if breadthScore, breadthReason := reservationBreadthRisk(r.PathPattern); breadthScore > 0 {
		score += breadthScore
		evidence = append(evidence, "path_breadth="+breadthReason)
		reasons = append(reasons, "broad_path_pattern")
		action = ReservationActionNarrow
	}

	if lastActive, ok := opts.HolderLastActive[r.AgentName]; ok && !lastActive.IsZero() {
		inactive := now.Sub(lastActive)
		if inactive < 0 {
			inactive = 0
		}
		confidence += 0.15
		evidence = append(evidence, fmt.Sprintf("holder_inactive_minutes=%d", int(inactive.Minutes())))
		if inactive >= inactiveHolderAge {
			score += 35
			reasons = append(reasons, "inactive_holder")
			action = ReservationActionMessageHolder
		}
	}

	if opts.StaleInProgressByHolder[r.AgentName] || opts.StaleInProgressByReason[r.Reason] {
		score += 30
		evidence = append(evidence, "stale_in_progress_context=true")
		reasons = append(reasons, "stale_in_progress_context")
		action = ReservationActionMessageHolder
	}

	overlaps := overlappingReservationCount(r, all)
	if overlaps > 0 {
		score += 25
		confidence += 0.1
		evidence = append(evidence, fmt.Sprintf("overlapping_reservations=%d", overlaps))
		reasons = append(reasons, "overlapping_reservation")
		action = ReservationActionMessageHolder
	}

	score = clampInt(score, 0, 100)
	confidence = clampFloat(confidence, 0, 1)
	if score >= 85 && confidence < 0.65 {
		action = ReservationActionAskHuman
		reasons = append(reasons, "low_confidence_high_risk")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "low_risk")
		evidence = append(evidence, "no_risk_threshold_crossed")
	}

	return ReservationRecommendation{
		ReservationID: r.ID,
		PathPattern:   r.PathPattern,
		Holder:        r.AgentName,
		RiskScore:     score,
		Risk:          riskLevel(score),
		Confidence:    confidence,
		Action:        action,
		Evidence:      evidence,
		ReasonCodes:   uniqueSortedStrings(reasons),
	}
}

func reservationBreadthRisk(pattern string) (int, string) {
	pattern = strings.TrimSpace(pattern)
	switch pattern {
	case "", ".", "/", "**", "/**":
		return 40, "repository_wide"
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.Trim(pattern[:len(pattern)-3], "/")
		if reservationTextEmpty(prefix) || !strings.Contains(prefix, "/") {
			return 35, "top_level_glob"
		}
		return 15, "deep_glob"
	}
	if strings.Contains(pattern, "*") {
		return 10, "wildcard"
	}
	return 0, "narrow"
}

func overlappingReservationCount(target ReservationRiskInput, all []ReservationRiskInput) int {
	count := 0
	for _, other := range all {
		if target.ID != 0 && other.ID == target.ID {
			continue
		}
		if target.ID == 0 && reservationTextEqual(target.PathPattern, other.PathPattern) && reservationTextEqual(target.AgentName, other.AgentName) {
			continue
		}
		if reservationTextEqual(target.AgentName, other.AgentName) {
			continue
		}
		if !target.Exclusive && !other.Exclusive {
			continue
		}
		if patternsOverlap(target.PathPattern, other.PathPattern) {
			count++
		}
	}
	return count
}

func riskLevel(score int) string {
	switch {
	case score >= 80:
		return "critical"
	case score >= 55:
		return "high"
	case score >= 30:
		return "medium"
	default:
		return "low"
	}
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func clampFloat(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func uniqueSortedStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if reservationTextEmpty(value) {
			continue
		}
		set[value] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func reservationTextEmpty(value string) bool {
	return strings.Compare(strings.TrimSpace(value), "") == 0
}

func reservationTextEqual(a, b string) bool {
	return strings.Compare(a, b) == 0
}

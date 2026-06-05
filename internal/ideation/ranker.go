package ideation

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

type RankingDecision string

const (
	RankingDecisionIdeate           RankingDecision = "ideate"
	RankingDecisionReviewRecentWork RankingDecision = "review_recent_work"
	RankingDecisionStandDown        RankingDecision = "stand_down"
)

type RankOptions struct {
	TopLimit  int `json:"top_limit"`
	NextLimit int `json:"next_limit"`
}

type RankingResult struct {
	Decision       RankingDecision   `json:"decision"`
	Summary        string            `json:"summary"`
	CandidateCount int               `json:"candidate_count"`
	Selected       []RankedCandidate `json:"selected"`
	NextBest       []RankedCandidate `json:"next_best"`
	Suppressed     []RankedCandidate `json:"suppressed"`
	Notes          []ValidationNote  `json:"notes"`
}

type RankedCandidate struct {
	Rank             int           `json:"rank"`
	Candidate        IdeaCandidate `json:"candidate"`
	Score            float64       `json:"score"`
	Factors          RankFactors   `json:"factors"`
	Included         bool          `json:"included"`
	Reasons          []string      `json:"reasons"`
	ExclusionReasons []string      `json:"exclusion_reasons"`
}

type RankFactors struct {
	Robustness         float64 `json:"robustness"`
	Reliability        float64 `json:"reliability"`
	Performance        float64 `json:"performance"`
	Ergonomics         float64 `json:"ergonomics"`
	Usefulness         float64 `json:"usefulness"`
	UserVisibleValue   float64 `json:"user_visible_value"`
	ImplementationCost float64 `json:"implementation_cost"`
	Testability        float64 `json:"testability"`
	DegradedSourceRisk float64 `json:"degraded_source_risk"`
	Overlap            float64 `json:"overlap"`
}

func DefaultRankOptions() RankOptions {
	return RankOptions{TopLimit: 5, NextLimit: 10}
}

func CandidateSet(snapshot IdeaEvidenceSnapshot) []IdeaCandidate {
	if len(snapshot.Candidates) > 0 {
		return normalizeCandidates(snapshot.Candidates)
	}

	candidates := make([]IdeaCandidate, 0, 8)
	if snapshot.Queue.CountsVerified && snapshot.Queue.ActionableCount == 0 && snapshot.Queue.ReadyCount == 0 {
		candidates = append(candidates, IdeaCandidate{
			ID:        "generated-queue-dry-plan",
			Title:     "Queue-dry duplicate-aware planning path",
			Summary:   "Create a safe dry-run planning path only after br and bv both show no ready work.",
			Labels:    []string{"queue-dry", "planning"},
			Keywords:  []string{"duplicate", "dry-run", "queue"},
			SourceIDs: []string{"br", "bv"},
			Evidence:  []string{"queue counts verified with no ready or actionable work"},
		})
	}
	for _, source := range snapshot.Sources {
		if source.Available {
			continue
		}
		candidates = append(candidates, IdeaCandidate{
			ID:        "generated-degraded-" + normalizeIDPart(source.ID),
			Title:     fmt.Sprintf("Harden degraded %s source handling", source.Kind),
			Summary:   "Preserve queue-dry ideation usefulness when an optional evidence source is unavailable.",
			Labels:    []string{"degraded-source", "queue-dry"},
			Keywords:  []string{"degraded", "resilience"},
			SourceIDs: []string{source.ID},
			Evidence:  append([]string{"generated from unavailable evidence source"}, source.Evidence...),
		})
	}
	for _, doc := range snapshot.Documents {
		if doc.Exists {
			continue
		}
		candidates = append(candidates, IdeaCandidate{
			ID:        "generated-doc-" + normalizeIDPart(doc.Path),
			Title:     "Restore queue-dry documentation evidence",
			Summary:   "Make project document freshness visible to queue-dry ideation.",
			Labels:    []string{"docs", "queue-dry"},
			Keywords:  []string{"docs", "freshness"},
			SourceIDs: []string{doc.SourceID},
			Evidence:  append([]string{"project document marker missing"}, doc.Evidence...),
		})
	}
	return normalizeCandidates(candidates)
}

func RankCandidates(snapshot IdeaEvidenceSnapshot, opts RankOptions) RankingResult {
	opts = normalizeRankOptions(opts)
	candidates := CandidateSet(snapshot)
	result := RankingResult{
		Decision:       RankingDecisionStandDown,
		Summary:        "no candidates available",
		CandidateCount: len(candidates),
		Selected:       []RankedCandidate{},
		NextBest:       []RankedCandidate{},
		Suppressed:     []RankedCandidate{},
		Notes:          []ValidationNote{},
	}
	if len(candidates) == 0 {
		result.Notes = append(result.Notes, ValidationNote{
			Code:     "no_candidates",
			Severity: ValidationInfo,
			Message:  "no candidate ideas were available after evidence reduction",
			Evidence: []string{},
		})
		return result
	}

	ranked := make([]RankedCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Overlap.Kind == "" {
			candidate = AttachOverlap(candidate, snapshot.ExistingWork)
		} else if candidate.Novelty.Level == "" {
			candidate.Novelty = NoveltyFromOverlap(candidate.Overlap)
		}
		item := scoreCandidate(snapshot, candidate)
		ranked = append(ranked, item)
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Included != ranked[j].Included {
			return ranked[i].Included
		}
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		if ranked[i].Candidate.Title != ranked[j].Candidate.Title {
			return ranked[i].Candidate.Title < ranked[j].Candidate.Title
		}
		return ranked[i].Candidate.ID < ranked[j].Candidate.ID
	})

	includedRank := 0
	for _, item := range ranked {
		if !item.Included {
			item.Rank = 0
			result.Suppressed = append(result.Suppressed, item)
			continue
		}
		includedRank++
		item.Rank = includedRank
		item.Reasons = stableStrings(append(item.Reasons, fmt.Sprintf("rank=%d", includedRank)))
		switch {
		case len(result.Selected) < opts.TopLimit:
			result.Selected = append(result.Selected, item)
		case len(result.NextBest) < opts.NextLimit:
			result.NextBest = append(result.NextBest, item)
		}
	}

	switch {
	case len(result.Selected) > 0:
		result.Decision = RankingDecisionIdeate
		result.Summary = fmt.Sprintf("selected %d candidates and retained %d next-best candidates", len(result.Selected), len(result.NextBest))
	case len(result.Suppressed) == len(ranked):
		result.Decision = RankingDecisionReviewRecentWork
		result.Summary = "all candidates were suppressed as duplicates; review recent closed work instead of creating new beads"
	default:
		result.Decision = RankingDecisionStandDown
		result.Summary = "no candidate passed ranking gates"
	}
	return result
}

func scoreCandidate(snapshot IdeaEvidenceSnapshot, candidate IdeaCandidate) RankedCandidate {
	factors := candidateFactors(snapshot, candidate)
	score := factors.Robustness*1.1 +
		factors.Reliability*1.2 +
		factors.Performance*0.9 +
		factors.Ergonomics*0.9 +
		factors.Usefulness*1.2 +
		factors.UserVisibleValue*1.2 +
		factors.Testability +
		factors.Overlap*1.5 -
		factors.ImplementationCost*0.7 -
		factors.DegradedSourceRisk*0.8
	score = roundScore(score)

	included := true
	exclusions := []string{}
	if candidate.Overlap.Kind == OverlapExactDuplicate {
		included = false
		exclusions = append(exclusions, overlapReason(candidate.Overlap))
	}
	if candidate.Overlap.Kind == OverlapLikelyDuplicate && !hasPriorGapEvidence(candidate) {
		included = false
		exclusions = append(exclusions, overlapReason(candidate.Overlap))
	}
	if len(candidate.Evidence) == 0 && len(candidate.SourceIDs) == 0 {
		exclusions = append(exclusions, "low evidence: no candidate evidence or source IDs")
	}

	return RankedCandidate{
		Candidate:        candidate,
		Score:            score,
		Factors:          factors,
		Included:         included,
		Reasons:          rankReasons(candidate, factors),
		ExclusionReasons: stableStrings(exclusions),
	}
}

func candidateFactors(snapshot IdeaEvidenceSnapshot, candidate IdeaCandidate) RankFactors {
	words := candidateWords(candidate)
	sourceEvidenceCount := 0
	degradedSourceIDs := make(map[string]struct{}, len(snapshot.DegradedSources))
	for _, note := range snapshot.DegradedSources {
		if note.SourceID != "" {
			degradedSourceIDs[note.SourceID] = struct{}{}
		}
	}
	for _, source := range snapshot.Sources {
		if hasString(candidate.SourceIDs, source.ID) {
			sourceEvidenceCount += len(source.Evidence)
		}
	}

	evidenceCount := len(candidate.Evidence) + sourceEvidenceCount
	degradedRisk := 0.0
	for _, id := range candidate.SourceIDs {
		if _, ok := degradedSourceIDs[id]; ok {
			degradedRisk += 0.35
		}
	}
	if len(candidate.SourceIDs) == 0 {
		degradedRisk += 0.15
	}
	if evidenceCount == 0 {
		degradedRisk += 0.35
	}
	degradedRisk += effectivenessValidationRisk(candidate.ValidationNotes)

	return RankFactors{
		Robustness:         keywordFactor(words, "robust", "safety", "safe", "resilience", "guard", "recovery"),
		Reliability:        keywordFactor(words, "reliable", "reliability", "deterministic", "stable", "contract", "replay"),
		Performance:        keywordFactor(words, "performance", "latency", "scale", "memory", "cpu", "throughput"),
		Ergonomics:         keywordFactor(words, "cli", "operator", "ergonomic", "ux", "workflow", "planning"),
		Usefulness:         usefulnessFactor(candidate, evidenceCount),
		UserVisibleValue:   keywordFactor(words, "operator", "queue", "dashboard", "robot", "bead", "work"),
		ImplementationCost: implementationCostFactor(candidate),
		Testability:        keywordFactor(words, "test", "tests", "testing", "golden", "fixture", "fuzz", "e2e"),
		DegradedSourceRisk: clamp(degradedRisk, 0, 1),
		Overlap:            overlapFactor(candidate),
	}
}

func effectivenessValidationRisk(notes []ValidationNote) float64 {
	risk := 0.0
	for _, note := range notes {
		switch note.Code {
		case "effectiveness_churn_history":
			risk += 0.25
		case "effectiveness_low_yield_source":
			risk += 0.2
		case "effectiveness_operator_rejection":
			risk += 0.15
		}
	}
	return clamp(risk, 0, 0.45)
}

func rankReasons(candidate IdeaCandidate, factors RankFactors) []string {
	reasons := []string{
		fmt.Sprintf("robustness=%.2f", factors.Robustness),
		fmt.Sprintf("reliability=%.2f", factors.Reliability),
		fmt.Sprintf("performance=%.2f", factors.Performance),
		fmt.Sprintf("ergonomics=%.2f", factors.Ergonomics),
		fmt.Sprintf("usefulness=%.2f", factors.Usefulness),
		fmt.Sprintf("user_visible_value=%.2f", factors.UserVisibleValue),
		fmt.Sprintf("implementation_cost=%.2f", factors.ImplementationCost),
		fmt.Sprintf("testability=%.2f", factors.Testability),
		fmt.Sprintf("degraded_source_risk=%.2f", factors.DegradedSourceRisk),
		fmt.Sprintf("overlap=%s confidence=%.2f", candidate.Overlap.Kind, candidate.Overlap.Confidence),
	}
	reasons = append(reasons, candidate.Overlap.Evidence...)
	return stableStrings(reasons)
}

func overlapFactor(candidate IdeaCandidate) float64 {
	switch candidate.Overlap.Kind {
	case OverlapExactDuplicate:
		return -1
	case OverlapLikelyDuplicate:
		if hasPriorGapEvidence(candidate) {
			return 0.1
		}
		return -0.65
	case OverlapAdjacentFollowUp:
		return 0.45
	case OverlapNovel:
		return 0.7
	default:
		return 0
	}
}

func overlapReason(overlap OverlapVerdict) string {
	if overlap.WorkID == "" {
		return fmt.Sprintf("suppressed %s overlap", overlap.Kind)
	}
	return fmt.Sprintf("suppressed %s overlap with %s", overlap.Kind, overlap.WorkID)
}

func usefulnessFactor(candidate IdeaCandidate, evidenceCount int) float64 {
	score := 0.25
	if strings.TrimSpace(candidate.Summary) != "" {
		score += 0.2
	}
	if evidenceCount > 0 {
		score += math.Min(0.35, float64(evidenceCount)*0.08)
	}
	if len(candidate.SourceIDs) > 0 {
		score += 0.1
	}
	if len(candidate.Paths) > 0 {
		score += 0.1
	}
	return clamp(score, 0, 1)
}

func implementationCostFactor(candidate IdeaCandidate) float64 {
	score := 0.25
	score += math.Min(0.45, float64(len(candidate.Paths))*0.12)
	words := candidateWords(candidate)
	if hasAny(words, "e2e", "integration", "migration", "serve", "websocket") {
		score += 0.2
	}
	if hasAny(words, "model", "pure", "fixture", "golden") {
		score -= 0.1
	}
	return clamp(score, 0.05, 1)
}

func keywordFactor(words map[string]struct{}, needles ...string) float64 {
	hits := 0
	for _, needle := range needles {
		if _, ok := words[needle]; ok {
			hits++
		}
	}
	if hits == 0 {
		return 0.15
	}
	return clamp(0.25+float64(hits)*0.18, 0, 1)
}

func hasPriorGapEvidence(candidate IdeaCandidate) bool {
	for _, item := range append(append([]string{}, candidate.Evidence...), candidate.Summary) {
		text := strings.ToLower(item)
		for _, marker := range []string{"gap", "left", "remaining", "follow-up", "follow up", "regression", "failed", "skipped", "not covered"} {
			if strings.Contains(text, marker) {
				return true
			}
		}
	}
	for _, related := range candidate.RelatedWork {
		for _, item := range related.Evidence {
			text := strings.ToLower(item)
			if strings.Contains(text, "gap") || strings.Contains(text, "remaining") || strings.Contains(text, "follow") {
				return true
			}
		}
	}
	return false
}

func normalizeCandidates(candidates []IdeaCandidate) []IdeaCandidate {
	out := make([]IdeaCandidate, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for i, candidate := range candidates {
		candidate = normalizeCandidate(candidate)
		if candidate.ID == "" {
			candidate.ID = fmt.Sprintf("candidate-%03d", i+1)
		}
		key := normalizeText(candidate.Title)
		if key == "" {
			key = candidate.ID
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, candidate)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Title != out[j].Title {
			return out[i].Title < out[j].Title
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func candidateWords(candidate IdeaCandidate) map[string]struct{} {
	value := strings.Join([]string{
		candidate.Title,
		candidate.Summary,
		strings.Join(candidate.Labels, " "),
		strings.Join(candidate.Keywords, " "),
		strings.Join(candidate.Evidence, " "),
	}, " ")
	return tokenSet(value)
}

func hasAny(words map[string]struct{}, values ...string) bool {
	for _, value := range values {
		if _, ok := words[value]; ok {
			return true
		}
	}
	return false
}

func hasString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func normalizeRankOptions(opts RankOptions) RankOptions {
	if opts.TopLimit <= 0 {
		opts.TopLimit = DefaultRankOptions().TopLimit
	}
	// NextLimit < 0 is the only "unset" sentinel; NextLimit == 0 means the
	// caller explicitly does not want any next-best candidates emitted.
	if opts.NextLimit < 0 {
		opts.NextLimit = DefaultRankOptions().NextLimit
	}
	return opts
}

func roundScore(score float64) float64 {
	return math.Round(score*10000) / 10000
}

func normalizeIDPart(value string) string {
	value = normalizeText(value)
	value = strings.ReplaceAll(value, " ", "-")
	if value == "" {
		return "unknown"
	}
	return value
}

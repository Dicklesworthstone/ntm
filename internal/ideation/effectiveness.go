package ideation

import (
	"fmt"
	"sort"
	"strings"
)

const (
	EffectivenessSourceID = "effectiveness:history"

	defaultMaxEffectivenessSignals           = 12
	defaultMaxEffectivenessCandidateEvidence = 4
)

type EffectivenessEventKind string

const (
	EffectivenessEventCandidateGenerated  EffectivenessEventKind = "candidate_generated"
	EffectivenessEventDuplicateSuppressed EffectivenessEventKind = "candidate_suppressed_duplicate"
	EffectivenessEventCandidateRendered   EffectivenessEventKind = "candidate_rendered"
	EffectivenessEventBeadCreated         EffectivenessEventKind = "bead_created"
	EffectivenessEventBeadClosed          EffectivenessEventKind = "bead_closed"
	EffectivenessEventBeadReopened        EffectivenessEventKind = "bead_reopened"
	EffectivenessEventFollowUpBug         EffectivenessEventKind = "follow_up_bug"
	EffectivenessEventVerificationFailed  EffectivenessEventKind = "verification_failed"
	EffectivenessEventOperatorRejected    EffectivenessEventKind = "operator_rejected"
)

type EffectivenessOutcome string

const (
	EffectivenessOutcomeClean    EffectivenessOutcome = "clean"
	EffectivenessOutcomeChurn    EffectivenessOutcome = "churn"
	EffectivenessOutcomeLowYield EffectivenessOutcome = "low_yield"
	EffectivenessOutcomeNeutral  EffectivenessOutcome = "neutral"
)

type EffectivenessOptions struct {
	ScenarioID string `json:"scenario_id,omitempty"`
	MaxSignals int    `json:"max_signals,omitempty"`
}

type EffectivenessFeedbackOptions struct {
	MaxSignals              int `json:"max_signals,omitempty"`
	MaxEvidencePerCandidate int `json:"max_evidence_per_candidate,omitempty"`
}

type EffectivenessEvent struct {
	CandidateID     string                 `json:"candidate_id,omitempty"`
	CandidateFamily string                 `json:"candidate_family,omitempty"`
	SourceID        string                 `json:"source_id,omitempty"`
	BeadID          string                 `json:"bead_id,omitempty"`
	Kind            EffectivenessEventKind `json:"kind"`
	OccurredAt      string                 `json:"occurred_at,omitempty"`
	DurationHours   float64                `json:"duration_hours,omitempty"`
	Reason          string                 `json:"reason,omitempty"`
	Evidence        []string               `json:"evidence,omitempty"`
}

type EffectivenessReport struct {
	ScenarioID               string                       `json:"scenario_id,omitempty"`
	HistoryAvailable         bool                         `json:"history_available"`
	AdvisoryOnly             bool                         `json:"advisory_only"`
	CandidateGeneratedCount  int                          `json:"candidate_generated_count"`
	DuplicateSuppressedCount int                          `json:"duplicate_suppressed_count"`
	RenderedCount            int                          `json:"rendered_count"`
	CreatedCount             int                          `json:"created_count"`
	ClosedCount              int                          `json:"closed_count"`
	ReopenedCount            int                          `json:"reopened_count"`
	FollowUpBugCount         int                          `json:"follow_up_bug_count"`
	VerificationFailureCount int                          `json:"verification_failure_count"`
	OperatorRejectedCount    int                          `json:"operator_rejected_count"`
	AverageTimeToCloseHours  float64                      `json:"average_time_to_close_hours,omitempty"`
	CleanFamilyIDs           []string                     `json:"clean_family_ids"`
	ChurnFamilyIDs           []string                     `json:"churn_family_ids"`
	LowYieldSourceIDs        []string                     `json:"low_yield_source_ids"`
	Families                 []EffectivenessFamilySummary `json:"families"`
	Sources                  []EffectivenessSourceSummary `json:"sources"`
	Signals                  []OptionalSignal             `json:"signals,omitempty"`
	Notes                    []ValidationNote             `json:"notes,omitempty"`
}

type EffectivenessFamilySummary struct {
	FamilyID                 string               `json:"family_id"`
	Outcome                  EffectivenessOutcome `json:"outcome"`
	Score                    float64              `json:"score"`
	CandidateIDs             []string             `json:"candidate_ids"`
	SourceIDs                []string             `json:"source_ids"`
	BeadIDs                  []string             `json:"bead_ids"`
	CandidateGeneratedCount  int                  `json:"candidate_generated_count"`
	DuplicateSuppressedCount int                  `json:"duplicate_suppressed_count"`
	RenderedCount            int                  `json:"rendered_count"`
	CreatedCount             int                  `json:"created_count"`
	ClosedCount              int                  `json:"closed_count"`
	ReopenedCount            int                  `json:"reopened_count"`
	FollowUpBugCount         int                  `json:"follow_up_bug_count"`
	VerificationFailureCount int                  `json:"verification_failure_count"`
	OperatorRejectedCount    int                  `json:"operator_rejected_count"`
	AverageTimeToCloseHours  float64              `json:"average_time_to_close_hours,omitempty"`
	Evidence                 []string             `json:"evidence"`
}

type EffectivenessSourceSummary struct {
	SourceID                 string               `json:"source_id"`
	Outcome                  EffectivenessOutcome `json:"outcome"`
	Score                    float64              `json:"score"`
	FamilyIDs                []string             `json:"family_ids"`
	CandidateIDs             []string             `json:"candidate_ids"`
	BeadIDs                  []string             `json:"bead_ids"`
	CandidateGeneratedCount  int                  `json:"candidate_generated_count"`
	DuplicateSuppressedCount int                  `json:"duplicate_suppressed_count"`
	RenderedCount            int                  `json:"rendered_count"`
	CreatedCount             int                  `json:"created_count"`
	ClosedCount              int                  `json:"closed_count"`
	ReopenedCount            int                  `json:"reopened_count"`
	FollowUpBugCount         int                  `json:"follow_up_bug_count"`
	VerificationFailureCount int                  `json:"verification_failure_count"`
	OperatorRejectedCount    int                  `json:"operator_rejected_count"`
	AverageTimeToCloseHours  float64              `json:"average_time_to_close_hours,omitempty"`
	Evidence                 []string             `json:"evidence"`
}

func BuildEffectivenessReport(events []EffectivenessEvent, opts EffectivenessOptions) EffectivenessReport {
	opts = normalizeEffectivenessOptions(opts)
	normalizedEvents := normalizeEffectivenessEvents(events)
	report := EffectivenessReport{
		ScenarioID:        opts.ScenarioID,
		HistoryAvailable:  len(normalizedEvents) > 0,
		AdvisoryOnly:      true,
		CleanFamilyIDs:    []string{},
		ChurnFamilyIDs:    []string{},
		LowYieldSourceIDs: []string{},
		Families:          []EffectivenessFamilySummary{},
		Sources:           []EffectivenessSourceSummary{},
		Signals:           []OptionalSignal{},
		Notes:             []ValidationNote{},
	}
	if len(normalizedEvents) == 0 {
		report.Notes = append(report.Notes, ValidationNote{
			Code:     "effectiveness_history_absent",
			Severity: ValidationInfo,
			Message:  "no generated-bead effectiveness history was available",
			SourceID: EffectivenessSourceID,
			Evidence: []string{"ranking proceeds without historical effectiveness feedback"},
		})
		return report
	}

	families := map[string]*effectivenessAccumulator{}
	sources := map[string]*effectivenessAccumulator{}
	closeHours := []float64{}
	for _, event := range normalizedEvents {
		report.countEvent(event)
		if event.Kind == EffectivenessEventBeadClosed && event.DurationHours > 0 {
			closeHours = append(closeHours, event.DurationHours)
		}
		familyID := effectivenessFamilyID(event)
		if familyID != "" {
			acc := effectivenessAccumulatorFor(families, familyID)
			acc.record(event, false)
		}
		sourceID := strings.TrimSpace(event.SourceID)
		if sourceID != "" {
			acc := effectivenessAccumulatorFor(sources, sourceID)
			acc.record(event, true)
		}
	}
	report.AverageTimeToCloseHours = averageEffectivenessHours(closeHours)

	report.Families = effectivenessFamilySummaries(families)
	report.Sources = effectivenessSourceSummaries(sources)
	for _, family := range report.Families {
		switch family.Outcome {
		case EffectivenessOutcomeClean:
			report.CleanFamilyIDs = append(report.CleanFamilyIDs, family.FamilyID)
		case EffectivenessOutcomeChurn:
			report.ChurnFamilyIDs = append(report.ChurnFamilyIDs, family.FamilyID)
		}
	}
	for _, source := range report.Sources {
		if source.Outcome == EffectivenessOutcomeLowYield {
			report.LowYieldSourceIDs = append(report.LowYieldSourceIDs, source.SourceID)
		}
	}
	report.CleanFamilyIDs = stableStrings(report.CleanFamilyIDs)
	report.ChurnFamilyIDs = stableStrings(report.ChurnFamilyIDs)
	report.LowYieldSourceIDs = stableStrings(report.LowYieldSourceIDs)
	report.Signals = effectivenessSignals(report, opts.MaxSignals)
	return report
}

func EffectivenessEventsFromArtifacts(ranking RankingResult, plan RoadmapPlan, creation BeadCreationReport) []EffectivenessEvent {
	events := []EffectivenessEvent{}
	candidates := rankedCandidateIndex(ranking)
	for _, item := range append(append([]RankedCandidate{}, ranking.Selected...), ranking.NextBest...) {
		events = append(events, effectivenessEventFromCandidate(item.Candidate, EffectivenessEventCandidateGenerated, "", []string{
			fmt.Sprintf("rank=%d", item.Rank),
			fmt.Sprintf("score=%.4f", item.Score),
		}))
	}
	for _, item := range ranking.Suppressed {
		events = append(events, effectivenessEventFromCandidate(item.Candidate, EffectivenessEventCandidateGenerated, "", []string{
			"suppressed candidate remained part of generated set",
		}))
		if item.Candidate.Overlap.Kind == OverlapExactDuplicate || item.Candidate.Overlap.Kind == OverlapLikelyDuplicate {
			events = append(events, effectivenessEventFromCandidate(item.Candidate, EffectivenessEventDuplicateSuppressed, overlapReason(item.Candidate.Overlap), item.Candidate.Overlap.Evidence))
		}
	}
	for _, bead := range plan.ProposedBeads {
		candidate := candidates[bead.CandidateID]
		events = append(events, EffectivenessEvent{
			CandidateID:     bead.CandidateID,
			CandidateFamily: candidateEffectivenessFamily(candidate, bead.CandidateID),
			SourceID:        firstSourceID(candidate.SourceIDs),
			Kind:            EffectivenessEventCandidateRendered,
			Reason:          "candidate rendered to dry-run roadmap",
			Evidence:        stableStrings([]string{fmt.Sprintf("ref=%s", bead.Ref), fmt.Sprintf("rank=%d", bead.Rank)}),
		})
	}
	for _, created := range creation.Created {
		candidate := candidates[created.CandidateID]
		events = append(events, EffectivenessEvent{
			CandidateID:     created.CandidateID,
			CandidateFamily: candidateEffectivenessFamily(candidate, created.CandidateID),
			SourceID:        firstSourceID(candidate.SourceIDs),
			BeadID:          created.BeadID,
			Kind:            EffectivenessEventBeadCreated,
			Reason:          "candidate created through br",
			Evidence:        stableStrings([]string{created.Ref, created.Command}),
		})
	}
	for _, candidateID := range creation.SkippedCandidates {
		candidate := candidates[candidateID]
		events = append(events, EffectivenessEvent{
			CandidateID:     candidateID,
			CandidateFamily: candidateEffectivenessFamily(candidate, candidateID),
			SourceID:        firstSourceID(candidate.SourceIDs),
			Kind:            EffectivenessEventDuplicateSuppressed,
			Reason:          "creation skipped duplicate candidate",
			Evidence:        []string{"bead creation runner did not create duplicate candidate"},
		})
	}
	return events
}

func SnapshotWithEffectivenessFeedback(snapshot IdeaEvidenceSnapshot, report EffectivenessReport, opts EffectivenessFeedbackOptions) IdeaEvidenceSnapshot {
	opts = normalizeEffectivenessFeedbackOptions(opts)
	out := cloneIdeaEvidenceSnapshot(snapshot)
	source := CandidateSource{
		ID:        EffectivenessSourceID,
		Kind:      SourceEffectiveness,
		Available: report.HistoryAvailable,
		Required:  false,
		Evidence:  effectivenessReportEvidence(report),
	}
	if !report.HistoryAvailable {
		source.Error = "effectiveness history unavailable"
		out.RecordSource(source)
		out.ValidationNotes = append(out.ValidationNotes, report.Notes...)
		return out
	}
	out.RecordSource(source)
	out.OptionalSignals = append(out.OptionalSignals, boundedOptionalSignals(report.Signals, opts.MaxSignals)...)
	sortOptionalSignals(out.OptionalSignals)

	for i := range out.Candidates {
		candidate := out.Candidates[i]
		keys := candidateEffectivenessKeys(candidate)
		addedEvidence := 0
		for _, family := range report.Families {
			if !matchesEffectivenessFamily(keys, family) {
				continue
			}
			switch family.Outcome {
			case EffectivenessOutcomeClean:
				if addedEvidence < opts.MaxEvidencePerCandidate {
					candidate.Evidence = append(candidate.Evidence, fmt.Sprintf("effectiveness: family %s closed %d generated bead(s) without churn", family.FamilyID, family.ClosedCount))
					addedEvidence++
				}
				candidate.SourceIDs = append(candidate.SourceIDs, EffectivenessSourceID)
			case EffectivenessOutcomeChurn:
				candidate.ValidationNotes = append(candidate.ValidationNotes, ValidationNote{
					Code:     "effectiveness_churn_history",
					Severity: ValidationWarning,
					Message:  "historical generated beads in this family produced churn",
					SourceID: EffectivenessSourceID,
					Evidence: []string{fmt.Sprintf("family=%s", family.FamilyID)},
				})
			}
		}
		for _, sourceSummary := range report.Sources {
			if sourceSummary.Outcome != EffectivenessOutcomeLowYield || !hasString(candidate.SourceIDs, sourceSummary.SourceID) {
				continue
			}
			candidate.ValidationNotes = append(candidate.ValidationNotes, ValidationNote{
				Code:     "effectiveness_low_yield_source",
				Severity: ValidationWarning,
				Message:  "historical generated beads from this source had low yield",
				SourceID: EffectivenessSourceID,
				Evidence: []string{fmt.Sprintf("source=%s", sourceSummary.SourceID)},
			})
		}
		candidate.Evidence = stableStrings(candidate.Evidence)
		candidate.SourceIDs = stableStrings(candidate.SourceIDs)
		candidate.ValidationNotes = sortValidationNotes(candidate.ValidationNotes)
		out.Candidates[i] = candidate
	}
	return out
}

func (report *EffectivenessReport) countEvent(event EffectivenessEvent) {
	switch event.Kind {
	case EffectivenessEventCandidateGenerated:
		report.CandidateGeneratedCount++
	case EffectivenessEventDuplicateSuppressed:
		report.DuplicateSuppressedCount++
	case EffectivenessEventCandidateRendered:
		report.RenderedCount++
	case EffectivenessEventBeadCreated:
		report.CreatedCount++
	case EffectivenessEventBeadClosed:
		report.ClosedCount++
	case EffectivenessEventBeadReopened:
		report.ReopenedCount++
	case EffectivenessEventFollowUpBug:
		report.FollowUpBugCount++
	case EffectivenessEventVerificationFailed:
		report.VerificationFailureCount++
	case EffectivenessEventOperatorRejected:
		report.OperatorRejectedCount++
	}
}

type effectivenessAccumulator struct {
	id                       string
	familyIDs                []string
	candidateIDs             []string
	sourceIDs                []string
	beadIDs                  []string
	candidateGeneratedCount  int
	duplicateSuppressedCount int
	renderedCount            int
	createdCount             int
	closedCount              int
	reopenedCount            int
	followUpBugCount         int
	verificationFailureCount int
	operatorRejectedCount    int
	closeHours               []float64
	evidence                 []string
}

func effectivenessAccumulatorFor(index map[string]*effectivenessAccumulator, id string) *effectivenessAccumulator {
	id = strings.TrimSpace(id)
	if id == "" {
		id = "unknown"
	}
	if acc, ok := index[id]; ok {
		return acc
	}
	acc := &effectivenessAccumulator{id: id}
	index[id] = acc
	return acc
}

func (acc *effectivenessAccumulator) record(event EffectivenessEvent, sourceAccumulator bool) {
	if event.CandidateFamily != "" {
		acc.familyIDs = append(acc.familyIDs, event.CandidateFamily)
	}
	if event.CandidateID != "" {
		acc.candidateIDs = append(acc.candidateIDs, event.CandidateID)
	}
	if event.SourceID != "" {
		acc.sourceIDs = append(acc.sourceIDs, event.SourceID)
	}
	if event.BeadID != "" {
		acc.beadIDs = append(acc.beadIDs, event.BeadID)
	}
	if event.DurationHours > 0 && event.Kind == EffectivenessEventBeadClosed {
		acc.closeHours = append(acc.closeHours, event.DurationHours)
	}
	if event.Reason != "" {
		acc.evidence = append(acc.evidence, event.Reason)
	}
	acc.evidence = append(acc.evidence, event.Evidence...)
	if sourceAccumulator {
		acc.evidence = append(acc.evidence, "source "+acc.id)
	} else {
		acc.evidence = append(acc.evidence, "family "+acc.id)
	}

	switch event.Kind {
	case EffectivenessEventCandidateGenerated:
		acc.candidateGeneratedCount++
	case EffectivenessEventDuplicateSuppressed:
		acc.duplicateSuppressedCount++
	case EffectivenessEventCandidateRendered:
		acc.renderedCount++
	case EffectivenessEventBeadCreated:
		acc.createdCount++
	case EffectivenessEventBeadClosed:
		acc.closedCount++
	case EffectivenessEventBeadReopened:
		acc.reopenedCount++
	case EffectivenessEventFollowUpBug:
		acc.followUpBugCount++
	case EffectivenessEventVerificationFailed:
		acc.verificationFailureCount++
	case EffectivenessEventOperatorRejected:
		acc.operatorRejectedCount++
	}
}

func (acc effectivenessAccumulator) outcome() EffectivenessOutcome {
	if acc.reopenedCount+acc.followUpBugCount+acc.verificationFailureCount > 0 {
		return EffectivenessOutcomeChurn
	}
	if acc.closedCount > 0 {
		return EffectivenessOutcomeClean
	}
	if acc.operatorRejectedCount > 0 || (acc.duplicateSuppressedCount > 0 && acc.createdCount+acc.closedCount == 0) {
		return EffectivenessOutcomeLowYield
	}
	return EffectivenessOutcomeNeutral
}

func (acc effectivenessAccumulator) score() float64 {
	positive := float64(acc.closedCount*3 + acc.createdCount)
	negative := float64(acc.reopenedCount*3 + acc.followUpBugCount*2 + acc.verificationFailureCount*2 + acc.operatorRejectedCount + acc.duplicateSuppressedCount)
	total := positive + negative + float64(acc.candidateGeneratedCount+acc.renderedCount)
	if total == 0 {
		return 0
	}
	return roundScore(clamp((positive-negative)/total, -1, 1))
}

func effectivenessFamilySummaries(index map[string]*effectivenessAccumulator) []EffectivenessFamilySummary {
	out := make([]EffectivenessFamilySummary, 0, len(index))
	for _, acc := range index {
		out = append(out, EffectivenessFamilySummary{
			FamilyID:                 acc.id,
			Outcome:                  acc.outcome(),
			Score:                    acc.score(),
			CandidateIDs:             stableStrings(acc.candidateIDs),
			SourceIDs:                stableStrings(acc.sourceIDs),
			BeadIDs:                  stableStrings(acc.beadIDs),
			CandidateGeneratedCount:  acc.candidateGeneratedCount,
			DuplicateSuppressedCount: acc.duplicateSuppressedCount,
			RenderedCount:            acc.renderedCount,
			CreatedCount:             acc.createdCount,
			ClosedCount:              acc.closedCount,
			ReopenedCount:            acc.reopenedCount,
			FollowUpBugCount:         acc.followUpBugCount,
			VerificationFailureCount: acc.verificationFailureCount,
			OperatorRejectedCount:    acc.operatorRejectedCount,
			AverageTimeToCloseHours:  averageEffectivenessHours(acc.closeHours),
			Evidence:                 stableStrings(acc.evidence),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Outcome != out[j].Outcome {
			return out[i].Outcome < out[j].Outcome
		}
		return out[i].FamilyID < out[j].FamilyID
	})
	return out
}

func effectivenessSourceSummaries(index map[string]*effectivenessAccumulator) []EffectivenessSourceSummary {
	out := make([]EffectivenessSourceSummary, 0, len(index))
	for _, acc := range index {
		out = append(out, EffectivenessSourceSummary{
			SourceID:                 acc.id,
			Outcome:                  acc.outcome(),
			Score:                    acc.score(),
			FamilyIDs:                stableStrings(acc.familyIDs),
			CandidateIDs:             stableStrings(acc.candidateIDs),
			BeadIDs:                  stableStrings(acc.beadIDs),
			CandidateGeneratedCount:  acc.candidateGeneratedCount,
			DuplicateSuppressedCount: acc.duplicateSuppressedCount,
			RenderedCount:            acc.renderedCount,
			CreatedCount:             acc.createdCount,
			ClosedCount:              acc.closedCount,
			ReopenedCount:            acc.reopenedCount,
			FollowUpBugCount:         acc.followUpBugCount,
			VerificationFailureCount: acc.verificationFailureCount,
			OperatorRejectedCount:    acc.operatorRejectedCount,
			AverageTimeToCloseHours:  averageEffectivenessHours(acc.closeHours),
			Evidence:                 stableStrings(acc.evidence),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Outcome != out[j].Outcome {
			return out[i].Outcome < out[j].Outcome
		}
		return out[i].SourceID < out[j].SourceID
	})
	return out
}

func effectivenessSignals(report EffectivenessReport, max int) []OptionalSignal {
	if max <= 0 {
		max = defaultMaxEffectivenessSignals
	}
	signals := make([]OptionalSignal, 0, max)
	for _, family := range report.Families {
		if family.Outcome == EffectivenessOutcomeNeutral {
			continue
		}
		if len(signals) >= max {
			break
		}
		signals = append(signals, OptionalSignal{
			ID:       "effectiveness-family-" + normalizeIDPart(family.FamilyID),
			SourceID: EffectivenessSourceID,
			Kind:     "effectiveness_family",
			Title:    "Effectiveness: " + family.FamilyID + " " + string(family.Outcome),
			Summary:  effectivenessFamilySummaryText(family),
			Tags:     stableStrings([]string{"effectiveness", string(family.Outcome)}),
			Evidence: stableStrings(append([]string{fmt.Sprintf("score=%.4f", family.Score)}, family.Evidence...)),
		})
	}
	for _, source := range report.Sources {
		if source.Outcome != EffectivenessOutcomeLowYield {
			continue
		}
		if len(signals) >= max {
			break
		}
		signals = append(signals, OptionalSignal{
			ID:       "effectiveness-source-" + normalizeIDPart(source.SourceID),
			SourceID: EffectivenessSourceID,
			Kind:     "effectiveness_source",
			Title:    "Effectiveness: " + source.SourceID + " low_yield",
			Summary:  effectivenessSourceSummaryText(source),
			Tags:     stableStrings([]string{"effectiveness", "low_yield"}),
			Evidence: stableStrings(append([]string{fmt.Sprintf("score=%.4f", source.Score)}, source.Evidence...)),
		})
	}
	sortOptionalSignals(signals)
	return signals
}

func effectivenessFamilySummaryText(family EffectivenessFamilySummary) string {
	return fmt.Sprintf("created=%d closed=%d reopened=%d follow_up_bugs=%d verification_failures=%d rejected=%d duplicate_suppressed=%d",
		family.CreatedCount,
		family.ClosedCount,
		family.ReopenedCount,
		family.FollowUpBugCount,
		family.VerificationFailureCount,
		family.OperatorRejectedCount,
		family.DuplicateSuppressedCount,
	)
}

func effectivenessSourceSummaryText(source EffectivenessSourceSummary) string {
	return fmt.Sprintf("created=%d closed=%d rejected=%d duplicate_suppressed=%d",
		source.CreatedCount,
		source.ClosedCount,
		source.OperatorRejectedCount,
		source.DuplicateSuppressedCount,
	)
}

func normalizeEffectivenessOptions(opts EffectivenessOptions) EffectivenessOptions {
	if opts.MaxSignals <= 0 {
		opts.MaxSignals = defaultMaxEffectivenessSignals
	}
	return opts
}

func normalizeEffectivenessFeedbackOptions(opts EffectivenessFeedbackOptions) EffectivenessFeedbackOptions {
	if opts.MaxSignals <= 0 {
		opts.MaxSignals = defaultMaxEffectivenessSignals
	}
	if opts.MaxEvidencePerCandidate <= 0 {
		opts.MaxEvidencePerCandidate = defaultMaxEffectivenessCandidateEvidence
	}
	return opts
}

func normalizeEffectivenessEvents(events []EffectivenessEvent) []EffectivenessEvent {
	out := make([]EffectivenessEvent, 0, len(events))
	for _, event := range events {
		event.CandidateID = strings.TrimSpace(event.CandidateID)
		event.CandidateFamily = strings.TrimSpace(event.CandidateFamily)
		event.SourceID = strings.TrimSpace(event.SourceID)
		event.BeadID = strings.TrimSpace(event.BeadID)
		event.Reason = strings.TrimSpace(event.Reason)
		event.Evidence = stableStrings(event.Evidence)
		if event.Kind == "" {
			continue
		}
		out = append(out, event)
	}
	sort.SliceStable(out, func(i, j int) bool {
		left := effectivenessEventSortKey(out[i])
		right := effectivenessEventSortKey(out[j])
		return left < right
	})
	return out
}

func effectivenessEventSortKey(event EffectivenessEvent) string {
	return strings.Join([]string{
		string(event.Kind),
		effectivenessFamilyID(event),
		event.CandidateID,
		event.SourceID,
		event.BeadID,
		event.Reason,
	}, "\x00")
}

func effectivenessFamilyID(event EffectivenessEvent) string {
	if event.CandidateFamily != "" {
		return event.CandidateFamily
	}
	if event.CandidateID != "" {
		return event.CandidateID
	}
	if event.BeadID != "" {
		return beadFamilyID(event.BeadID)
	}
	return ""
}

func candidateEffectivenessFamily(candidate IdeaCandidate, fallback string) string {
	if candidate.Overlap.FamilyID != "" {
		return candidate.Overlap.FamilyID
	}
	for _, related := range candidate.RelatedWork {
		if related.FamilyID != "" {
			return related.FamilyID
		}
	}
	if candidate.ID != "" {
		return candidate.ID
	}
	return fallback
}

func candidateEffectivenessKeys(candidate IdeaCandidate) map[string]struct{} {
	values := []string{candidate.ID, candidate.Overlap.FamilyID}
	values = append(values, candidate.Labels...)
	values = append(values, candidate.Keywords...)
	values = append(values, candidate.SourceIDs...)
	for _, related := range candidate.RelatedWork {
		values = append(values, related.ID, related.FamilyID)
	}
	keys := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		keys[value] = struct{}{}
	}
	return keys
}

func matchesEffectivenessFamily(keys map[string]struct{}, family EffectivenessFamilySummary) bool {
	if _, ok := keys[family.FamilyID]; ok {
		return true
	}
	for _, id := range family.CandidateIDs {
		if _, ok := keys[id]; ok {
			return true
		}
	}
	return false
}

func beadFamilyID(beadID string) string {
	beadID = strings.TrimSpace(beadID)
	if beadID == "" {
		return ""
	}
	if idx := strings.LastIndex(beadID, "."); idx > 0 {
		return beadID[:idx]
	}
	return beadID
}

func firstSourceID(ids []string) string {
	ids = stableStrings(ids)
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

func rankedCandidateIndex(ranking RankingResult) map[string]IdeaCandidate {
	index := map[string]IdeaCandidate{}
	for _, item := range append(append([]RankedCandidate{}, ranking.Selected...), ranking.NextBest...) {
		if item.Candidate.ID != "" {
			index[item.Candidate.ID] = item.Candidate
		}
	}
	for _, item := range ranking.Suppressed {
		if item.Candidate.ID != "" {
			index[item.Candidate.ID] = item.Candidate
		}
	}
	return index
}

func effectivenessEventFromCandidate(candidate IdeaCandidate, kind EffectivenessEventKind, reason string, evidence []string) EffectivenessEvent {
	return EffectivenessEvent{
		CandidateID:     candidate.ID,
		CandidateFamily: candidateEffectivenessFamily(candidate, candidate.ID),
		SourceID:        firstSourceID(candidate.SourceIDs),
		Kind:            kind,
		Reason:          reason,
		Evidence:        stableStrings(append(append([]string{}, evidence...), candidate.Evidence...)),
	}
}

func averageEffectivenessHours(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	return roundScore(total / float64(len(values)))
}

func effectivenessReportEvidence(report EffectivenessReport) []string {
	if !report.HistoryAvailable {
		return []string{"effectiveness history unavailable"}
	}
	return stableStrings([]string{
		fmt.Sprintf("clean_families=%d", len(report.CleanFamilyIDs)),
		fmt.Sprintf("churn_families=%d", len(report.ChurnFamilyIDs)),
		fmt.Sprintf("low_yield_sources=%d", len(report.LowYieldSourceIDs)),
	})
}

func boundedOptionalSignals(signals []OptionalSignal, max int) []OptionalSignal {
	if max <= 0 {
		max = defaultMaxEffectivenessSignals
	}
	out := append([]OptionalSignal{}, signals...)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

func cloneIdeaEvidenceSnapshot(snapshot IdeaEvidenceSnapshot) IdeaEvidenceSnapshot {
	out := snapshot
	out.Documents = append([]ProjectDocumentMarker{}, snapshot.Documents...)
	out.CloseoutProof = append([]CloseoutProofEvidence{}, snapshot.CloseoutProof...)
	out.Git = append([]GitTouchSummary{}, snapshot.Git...)
	out.Sources = append([]CandidateSource{}, snapshot.Sources...)
	out.ExistingWork = append([]ExistingWorkFingerprint{}, snapshot.ExistingWork...)
	out.Candidates = append([]IdeaCandidate{}, snapshot.Candidates...)
	out.OptionalSignals = append([]OptionalSignal{}, snapshot.OptionalSignals...)
	out.DegradedSources = append([]ValidationNote{}, snapshot.DegradedSources...)
	out.ValidationNotes = append([]ValidationNote{}, snapshot.ValidationNotes...)
	for i := range out.Candidates {
		out.Candidates[i].Labels = append([]string{}, out.Candidates[i].Labels...)
		out.Candidates[i].Keywords = append([]string{}, out.Candidates[i].Keywords...)
		out.Candidates[i].Paths = append([]string{}, out.Candidates[i].Paths...)
		out.Candidates[i].SourceIDs = append([]string{}, out.Candidates[i].SourceIDs...)
		out.Candidates[i].Evidence = append([]string{}, out.Candidates[i].Evidence...)
		out.Candidates[i].RelatedWork = append([]RelatedWorkReference{}, out.Candidates[i].RelatedWork...)
		out.Candidates[i].ValidationNotes = append([]ValidationNote{}, out.Candidates[i].ValidationNotes...)
	}
	return out
}

func sortValidationNotes(notes []ValidationNote) []ValidationNote {
	out := append([]ValidationNote{}, notes...)
	for i := range out {
		out[i].Evidence = stableStrings(out[i].Evidence)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Code != out[j].Code {
			return out[i].Code < out[j].Code
		}
		if out[i].Severity != out[j].Severity {
			return out[i].Severity < out[j].Severity
		}
		if out[i].SourceID != out[j].SourceID {
			return out[i].SourceID < out[j].SourceID
		}
		return out[i].Message < out[j].Message
	})
	return out
}

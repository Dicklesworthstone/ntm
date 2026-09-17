package robot

import "strings"

// attentionWaitState retains positive event evidence for one wait. Pane and
// convergence predicates describe current state; attention predicates describe
// events since a cursor. An event must not be forgotten merely because another
// condition becomes true on a later replay page or polling iteration.
//
// Retain only a first witness and count per requested condition, not the event
// stream. The caller advances the replay cursor and stops collecting after all
// attention conditions match, so evidence is bounded and never double-counted.
type attentionWaitState struct {
	matches   map[string]singleAttentionConditionMatch
	completed *AttentionConditionResult
}

// observe consumes an already session/profile/visibility-filtered replay page.
// A fresh result supplies that page's cursor metadata; retained matches supply
// the evidence accumulated since this wait started.
func (s *attentionWaitState) observe(conditions []string, events []AttentionEvent, result *AttentionConditionResult) *AttentionConditionResult {
	if s.completed != nil {
		return s.completed
	}
	if s.matches == nil {
		s.matches = make(map[string]singleAttentionConditionMatch, len(conditions))
	}

	requested := make([]string, 0, len(conditions))
	seen := make(map[string]bool, len(conditions))
	for _, condition := range conditions {
		condition = strings.TrimSpace(condition)
		if seen[condition] || !isAttentionBasedCondition(condition) {
			continue
		}
		seen[condition] = true
		requested = append(requested, condition)
		if match := checkSingleAttentionCondition(condition, events); match != nil {
			if prior, ok := s.matches[condition]; ok {
				prior.TriggerCount += match.TriggerCount
				s.matches[condition] = prior
			} else {
				// checkSingleAttentionCondition clones the witness, including
				// mutable event details, before this replay page is released.
				s.matches[condition] = *match
			}
		}
	}

	matchedConditions := make([]string, 0, len(requested))
	matchCounts := make(map[string]int, len(requested))
	var decisiveMatch *singleAttentionConditionMatch
	for _, condition := range requested {
		match, ok := s.matches[condition]
		if !ok {
			continue
		}
		matchedConditions = append(matchedConditions, condition)
		matchCounts[condition] = match.TriggerCount
		if decisiveMatch == nil || match.TriggerEvent.Cursor > decisiveMatch.TriggerEvent.Cursor {
			witness := match
			decisiveMatch = &witness
		}
	}

	result.Details["matched_conditions"] = matchedConditions
	result.Details["match_count_by_condition"] = matchCounts
	if len(requested) == 0 || len(matchedConditions) != len(requested) {
		return result
	}

	result.Met = true
	result.Condition = decisiveMatch.Condition
	result.TriggerEvent = decisiveMatch.TriggerEvent
	result.TriggerCount = decisiveMatch.TriggerCount
	result.ObservedCursor = decisiveMatch.TriggerEvent.Cursor
	s.completed = result
	return result
}

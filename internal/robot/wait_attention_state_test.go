package robot

import (
	"reflect"
	"testing"
)

func waitStateMailEvent(cursor int64) AttentionEvent {
	return AttentionEvent{
		Cursor: cursor, Session: "proj", Category: EventCategoryMail,
		Type: EventTypeMailReceived, Actionability: ActionabilityInteresting,
		Summary: "mail arrived",
	}
}

func waitStateActionEvent(cursor int64) AttentionEvent {
	return AttentionEvent{
		Cursor: cursor, Session: "proj", Category: EventCategoryAlert,
		Type: EventTypeAlertWarning, Actionability: ActionabilityActionRequired,
		Summary: "operator intervention needed",
	}
}

func TestAttentionWaitStateAccumulatesAcrossPages(t *testing.T) {
	var progress attentionWaitState
	conditions := []string{WaitConditionMailPending, WaitConditionActionRequired}
	first := progress.observe(conditions, []AttentionEvent{waitStateMailEvent(3)}, newAttentionConditionResult("", 0, 1000, 1))
	if first.Met {
		t.Fatal("one event cannot satisfy both conditions")
	}
	if got := first.Details["matched_conditions"]; !reflect.DeepEqual(got, []string{WaitConditionMailPending}) {
		t.Fatalf("missing partial evidence: %v", got)
	}

	second := progress.observe(conditions, []AttentionEvent{waitStateActionEvent(1001)}, newAttentionConditionResult("", 1000, 1001, 1))
	if !second.Met || second.TriggerEvent == nil || second.TriggerEvent.Cursor != 1001 {
		t.Fatalf("lost first-page evidence: %+v", second)
	}
	if second.Condition != WaitConditionActionRequired || second.ObservedCursor != 1001 || second.NextCursor != 1001 {
		t.Fatalf("wrong decisive witness/cursor: %+v", second)
	}
	if got := second.Details["match_count_by_condition"]; !reflect.DeepEqual(got, map[string]int{
		WaitConditionMailPending: 1, WaitConditionActionRequired: 1,
	}) {
		t.Fatalf("incorrect witness counts: %v", got)
	}
}

func TestAttentionWaitStateKeepsEvidenceAcrossEmptyPolls(t *testing.T) {
	var progress attentionWaitState
	conditions := []string{WaitConditionMailPending, WaitConditionActionRequired}
	progress.observe(conditions, []AttentionEvent{waitStateMailEvent(1)}, newAttentionConditionResult("", 0, 1, 1))
	for i := 0; i < 100; i++ {
		result := progress.observe(conditions, nil, newAttentionConditionResult("", 1, 1, 1))
		if result.Met || len(progress.matches) != 1 || progress.matches[WaitConditionMailPending].TriggerCount != 1 {
			t.Fatalf("empty poll changed evidence: %+v", result)
		}
	}
	result := progress.observe(conditions, []AttentionEvent{waitStateActionEvent(2)}, newAttentionConditionResult("", 1, 2, 1))
	if !result.Met {
		t.Fatal("empty polls erased an already observed event")
	}
}

func TestAttentionWaitStateBoundsEvidenceAndPreservesFirstWitness(t *testing.T) {
	var progress attentionWaitState
	conditions := []string{WaitConditionMailPending, WaitConditionActionRequired}
	for cursor := int64(1); cursor <= 5000; cursor++ {
		progress.observe(conditions, []AttentionEvent{waitStateMailEvent(cursor)}, newAttentionConditionResult("", cursor-1, cursor, 1))
	}
	if len(progress.matches) != 1 {
		t.Fatalf("retained event history instead of one witness: %d entries", len(progress.matches))
	}
	mail := progress.matches[WaitConditionMailPending]
	if mail.TriggerCount != 5000 || mail.TriggerEvent.Cursor != 1 {
		t.Fatalf("count/first witness changed: %+v", mail)
	}
	result := progress.observe(conditions, []AttentionEvent{waitStateActionEvent(5001)}, newAttentionConditionResult("", 5000, 5001, 1))
	if !result.Met || len(progress.matches) != 2 {
		t.Fatalf("expected bounded completed state: %+v", result)
	}
}

func TestAttentionWaitStateDeduplicatesConditions(t *testing.T) {
	var progress attentionWaitState
	conditions := []string{" mail_pending ", WaitConditionMailPending, WaitConditionIdle, ""}
	result := progress.observe(conditions, []AttentionEvent{waitStateMailEvent(1)}, newAttentionConditionResult("", 0, 1, 1))
	if !result.Met || result.TriggerCount != 1 || len(progress.matches) != 1 {
		t.Fatalf("duplicates or non-attention predicates corrupted state: %+v", result)
	}
	if got := result.Details["matched_conditions"]; !reflect.DeepEqual(got, []string{WaitConditionMailPending}) {
		t.Fatalf("conditions were not normalized/deduplicated: %v", got)
	}
}

func TestAttentionWaitStateNoAttentionDoesNotMatch(t *testing.T) {
	var progress attentionWaitState
	result := progress.observe([]string{WaitConditionIdle}, []AttentionEvent{waitStateMailEvent(1)}, newAttentionConditionResult("", 0, 1, 1))
	if result.Met || len(progress.matches) != 0 {
		t.Fatalf("no attention predicates must not fabricate an event match: %+v", result)
	}
}

func TestAttentionWaitStateIsolationAndOwnedWitness(t *testing.T) {
	conditions := []string{WaitConditionMailPending, WaitConditionActionRequired}
	event := waitStateMailEvent(1)
	event.Details = map[string]any{"message": "original"}
	var first, second attentionWaitState
	first.observe(conditions, []AttentionEvent{event}, newAttentionConditionResult("", 0, 1, 1))
	event.Details["message"] = "changed after collection"
	result := second.observe(conditions, []AttentionEvent{waitStateActionEvent(2)}, newAttentionConditionResult("", 1, 2, 1))
	if result.Met {
		t.Fatal("a separate wait inherited another wait's evidence")
	}
	result = first.observe(conditions, []AttentionEvent{waitStateActionEvent(2)}, newAttentionConditionResult("", 1, 2, 1))
	if !result.Met || first.matches[WaitConditionMailPending].TriggerEvent.Details["message"] != "original" {
		t.Fatalf("retained evidence aliases the replay page: %+v", result)
	}
}

func TestAttentionWaitStateDecisiveWitnessFollowsCursorNotConditionOrder(t *testing.T) {
	var progress attentionWaitState
	conditions := []string{WaitConditionActionRequired, WaitConditionMailPending}
	progress.observe(conditions, []AttentionEvent{waitStateMailEvent(1)}, newAttentionConditionResult("", 0, 1, 1))
	result := progress.observe(conditions, []AttentionEvent{waitStateActionEvent(2)}, newAttentionConditionResult("", 1, 2, 1))
	if !result.Met || result.Condition != WaitConditionActionRequired || result.ObservedCursor != 2 {
		t.Fatalf("condition order selected an older witness: %+v", result)
	}
}

func TestAttentionWaitStateLatchesCompletedEvidenceAndCursor(t *testing.T) {
	var progress attentionWaitState
	conditions := []string{WaitConditionMailPending}
	first := progress.observe(conditions, []AttentionEvent{waitStateMailEvent(1)}, newAttentionConditionResult("", 0, 1, 1))
	if !first.Met {
		t.Fatal("first event should complete the event predicate")
	}
	// A mixed wait may need many more pane polls. They must not overwrite the
	// triggering witness or consume the next operator iteration's events.
	second := progress.observe(conditions, []AttentionEvent{waitStateMailEvent(2)}, newAttentionConditionResult("", 1, 2, 1))
	if second != first || second.NextCursor != 1 || second.TriggerCount != 1 || second.TriggerEvent.Cursor != 1 {
		t.Fatalf("completed evidence or cursor was overwritten: %+v", second)
	}
}

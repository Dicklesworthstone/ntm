package robot

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

// Exercise the production feed, not just the per-wait accumulator. Filtering
// and replay continuation must happen before a witness becomes durable in a wait.
func withAttentionReplayTestFeed(t *testing.T) *AttentionFeed {
	t.Helper()
	_ = GetAttentionFeed()
	old := PeekAttentionFeed()
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize: 2048, RetentionPeriod: time.Hour, HeartbeatInterval: 0,
	})
	SetAttentionFeed(feed)
	t.Cleanup(func() { SetAttentionFeed(old) })
	return feed
}

func TestAttentionWaitReplayComposedConditionsSpanPages(t *testing.T) {
	feed := withAttentionReplayTestFeed(t)
	feed.Append(waitStateMailEvent(0))
	for i := 1; i < 1000; i++ {
		feed.Append(AttentionEvent{
			Session: "elsewhere", Category: EventCategoryAgent, Type: EventTypePaneOutput,
			Actionability: ActionabilityBackground, Severity: SeverityInfo, Summary: "output",
		})
	}
	action := feed.Append(waitStateActionEvent(0))
	var progress attentionWaitState
	conditions := []string{WaitConditionMailPending, WaitConditionActionRequired}
	first := checkAttentionConditionsWithState(conditions, 0, "proj", "", &progress)
	if first == nil || first.Met || first.NextCursor != 1000 {
		t.Fatalf("incorrect first-page result: %+v", first)
	}
	second := checkAttentionConditionsWithState(conditions, first.NextCursor, "proj", "", &progress)
	if second == nil || !second.Met || second.TriggerEvent.Cursor != action.Cursor || second.NextCursor != action.Cursor {
		t.Fatalf("composed predicates failed across replay pages: %+v", second)
	}
}

func TestAttentionWaitReplayComposedConditionsSpanPolls(t *testing.T) {
	feed := withAttentionReplayTestFeed(t)
	mail := feed.Append(waitStateMailEvent(0))
	var progress attentionWaitState
	conditions := []string{WaitConditionMailPending, WaitConditionActionRequired}
	first := checkAttentionConditionsWithState(conditions, 0, "proj", "", &progress)
	if first == nil || first.Met || first.NextCursor != mail.Cursor {
		t.Fatalf("incorrect initial result: %+v", first)
	}
	action := feed.Append(waitStateActionEvent(0))
	second := checkAttentionConditionsWithState(conditions, first.NextCursor, "proj", "", &progress)
	if second == nil || !second.Met || second.ObservedCursor != action.Cursor {
		t.Fatalf("event from previous poll was forgotten: %+v", second)
	}
	// A one-shot caller starting after the mail must NOT inherit these witnesses.
	isolated := checkAttentionConditions(conditions, mail.Cursor, "proj", "")
	if isolated == nil || isolated.Met {
		t.Fatalf("one-shot checks shared state: %+v", isolated)
	}
}

func TestAttentionWaitReplayFiltersBeforeRetainingWitnesses(t *testing.T) {
	feed := withAttentionReplayTestFeed(t)
	wrongSession := waitStateMailEvent(0)
	wrongSession.Session = "other"
	feed.Append(wrongSession)
	hidden := waitStateMailEvent(0)
	hidden.Details = map[string]any{
		attentionDetailState:        string(state.AttentionStateAcknowledged),
		attentionDetailHiddenReason: attentionHiddenReasonAcknowledged,
	}
	feed.Append(hidden)
	feed.Append(waitStateActionEvent(0))
	var progress attentionWaitState
	conditions := []string{WaitConditionMailPending, WaitConditionActionRequired}
	first := checkAttentionConditionsWithState(conditions, 0, "proj", "", &progress)
	if first == nil || first.Met || len(progress.matches) != 1 {
		t.Fatalf("wrong-session or hidden evidence was retained: %+v", first)
	}
	mail := feed.Append(waitStateMailEvent(0))
	second := checkAttentionConditionsWithState(conditions, first.NextCursor, "proj", "", &progress)
	if second == nil || !second.Met || second.TriggerEvent.Cursor != mail.Cursor {
		t.Fatalf("visible scoped mail did not complete the wait: %+v", second)
	}
}

func TestAttentionWaitReplayStopsAfterWitnessWhilePaneStateRemainsLive(t *testing.T) {
	feed := withAttentionReplayTestFeed(t)
	mail := feed.Append(waitStateMailEvent(0))
	var progress attentionWaitState
	conditions := []string{WaitConditionMailPending}
	first := checkAttentionConditionsWithState(conditions, 0, "proj", "", &progress)
	if first == nil || !first.Met {
		t.Fatalf("expected event evidence: %+v", first)
	}
	busy := []*AgentActivity{{PaneID: "%1", State: StateGenerating}}
	met, _, _ := checkWaitPaneObservations(busy, nil, WaitOptions{}, []string{WaitConditionIdle}, nil, nil)
	if met {
		t.Fatal("event witness must not make a busy pane idle")
	}
	later := feed.Append(waitStateActionEvent(0))
	second := checkAttentionConditionsWithState(conditions, first.NextCursor, "proj", "", &progress)
	if second != first || second.NextCursor != mail.Cursor || second.TriggerEvent.Cursor != mail.Cursor {
		t.Fatalf("pending mixed wait consumed later attention: %+v", second)
	}
	idle := []*AgentActivity{{PaneID: "%1", State: StateWaiting}}
	met, _, _ = checkWaitPaneObservations(idle, nil, WaitOptions{}, []string{WaitConditionIdle}, nil, nil)
	if !met || !second.Met {
		t.Fatal("mixed wait lost event evidence before the pane became idle")
	}
	followup := checkAttentionConditions([]string{WaitConditionActionRequired}, second.NextCursor, "proj", "")
	if followup == nil || !followup.Met || followup.TriggerEvent.Cursor != later.Cursor {
		t.Fatalf("next consumer lost later event: %+v", followup)
	}
}

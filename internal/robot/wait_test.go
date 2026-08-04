package robot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestGetWaitFailureUsesGeneralErrorExit(t *testing.T) {
	response, exitCode := GetWait(WaitOptions{
		Session:   "ntm-robot-contract-wait-missing",
		Condition: WaitConditionIdle,
	})
	if exitCode != 1 {
		t.Fatalf("GetWait() exit code = %d, want 1", exitCode)
	}
	if response.Success || response.ErrorCode != ErrCodeSessionNotFound {
		t.Fatalf("GetWait() response = %+v, want SESSION_NOT_FOUND failure", response.RobotResponse)
	}
}

func TestPrintWaitFailureForcesJSONUnderTOON(t *testing.T) {
	originalFormat := GetOutputFormat()
	SetOutputFormat(FormatTOON)
	t.Cleanup(func() { SetOutputFormat(originalFormat) })

	var exitCode int
	stdout, err := captureStdout(t, func() error {
		exitCode = PrintWait(WaitOptions{
			Session:   "ntm-robot-contract-wait-missing-toon",
			Condition: WaitConditionIdle,
		})
		return nil
	})
	if err != nil {
		t.Fatalf("capture PrintWait: %v", err)
	}
	if exitCode != 1 {
		t.Fatalf("PrintWait() exit code = %d, want 1", exitCode)
	}

	var response WaitResponse
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("PrintWait failure is not JSON: %v\noutput=%q", err, stdout)
	}
	if response.Success || response.ErrorCode != ErrCodeSessionNotFound || response.OutputFormat != string(FormatJSON) {
		t.Fatalf("PrintWait response = %+v, want SESSION_NOT_FOUND JSON failure", response.RobotResponse)
	}
}

func TestIsValidWaitCondition(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		want      bool
	}{
		// Pane-based conditions
		{"idle valid", "idle", true},
		{"complete valid", "complete", true},
		{"generating valid", "generating", true},
		{"healthy valid", "healthy", true},
		{"stalled valid", "stalled", true},
		{"rate_limited valid", "rate_limited", true},
		{"rate_limit_lifted valid", "rate_limit_lifted", true},

		// Attention-based conditions
		{"attention valid", "attention", true},
		{"action_required valid", "action_required", true},
		{"mail_pending valid", "mail_pending", true},
		{"mail_ack_required valid", "mail_ack_required", true},
		{"context_hot valid", "context_hot", true},
		{"reservation_conflict valid", "reservation_conflict", true},
		{"file_conflict valid", "file_conflict", true},
		{"session_changed valid", "session_changed", true},
		{"pane_changed valid", "pane_changed", true},

		// Composed conditions
		{"composed valid", "idle,healthy", true},
		{"composed with spaces", "idle, healthy", true},
		{"three conditions", "idle,healthy,complete", true},
		{"mixed pane and attention", "idle,action_required", true},

		// Invalid conditions
		{"invalid condition", "invalid", false},
		{"empty string", "", false},
		{"partial invalid", "idle,invalid", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidWaitCondition(tt.condition)
			if got != tt.want {
				t.Errorf("isValidWaitCondition(%q) = %v, want %v", tt.condition, got, tt.want)
			}
		})
	}
}

func TestHasAttentionBasedConditions(t *testing.T) {
	tests := []struct {
		name       string
		conditions []string
		want       bool
	}{
		{"pane only", []string{"idle", "healthy"}, false},
		{"attention only", []string{"action_required", "mail_pending"}, true},
		{"mixed", []string{"idle", "action_required"}, true},
		{"single pane", []string{"idle"}, false},
		{"single attention", []string{"attention"}, true},
		{"empty", []string{}, false},
		{"all attention types", []string{"attention", "action_required", "mail_pending", "context_hot"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasAttentionBasedConditions(tt.conditions)
			if got != tt.want {
				t.Errorf("hasAttentionBasedConditions(%v) = %v, want %v", tt.conditions, got, tt.want)
			}
		})
	}
}

func TestMeetsSingleWaitCondition(t *testing.T) {
	tests := []struct {
		name      string
		state     AgentState
		condition string
		want      bool
	}{
		{"waiting meets idle", StateWaiting, WaitConditionIdle, true},
		{"generating meets generating", StateGenerating, WaitConditionGenerating, true},
		{"waiting meets healthy", StateWaiting, WaitConditionHealthy, true},
		{"thinking meets healthy", StateThinking, WaitConditionHealthy, true},
		{"generating meets healthy", StateGenerating, WaitConditionHealthy, true},
		{"unknown meets healthy", StateUnknown, WaitConditionHealthy, true},
		{"error does not meet healthy", StateError, WaitConditionHealthy, false},
		{"stalled does not meet healthy", StateStalled, WaitConditionHealthy, false},
		{"generating does not meet idle", StateGenerating, WaitConditionIdle, false},
		{"thinking does not meet idle", StateThinking, WaitConditionIdle, false},
		{"unknown does not meet idle", StateUnknown, WaitConditionIdle, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			activity := &AgentActivity{
				State: tt.state,
			}
			got := meetsSingleWaitCondition(activity, tt.condition)
			if got != tt.want {
				t.Errorf("meetsSingleWaitCondition(state=%s, condition=%s) = %v, want %v",
					tt.state, tt.condition, got, tt.want)
			}
		})
	}
}

func TestMeetsAllWaitConditions(t *testing.T) {
	tests := []struct {
		name       string
		state      AgentState
		conditions []string
		want       bool
	}{
		{"single condition met", StateWaiting, []string{"idle"}, true},
		{"single condition not met", StateGenerating, []string{"idle"}, false},
		{"both conditions met", StateWaiting, []string{"idle", "healthy"}, true},
		{"first met second not", StateError, []string{"healthy"}, false},
		{"empty conditions", StateWaiting, []string{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			activity := &AgentActivity{
				State: tt.state,
			}
			got := meetsAllWaitConditions(activity, tt.conditions)
			if got != tt.want {
				t.Errorf("meetsAllWaitConditions(state=%s, conditions=%v) = %v, want %v",
					tt.state, tt.conditions, got, tt.want)
			}
		})
	}
}

func TestCheckWaitConditionMet_AllMode(t *testing.T) {
	// Test with default (ALL) mode
	opts := WaitOptions{
		Condition:  "idle",
		WaitForAny: false,
	}

	t.Run("all agents idle", func(t *testing.T) {
		activities := []*AgentActivity{
			{PaneID: "test__cc_1", State: StateWaiting},
			{PaneID: "test__cc_2", State: StateWaiting},
		}
		met, matching, pending := checkWaitConditionMet(activities, opts)
		if !met {
			t.Error("Expected condition to be met when all agents are idle")
		}
		if len(matching) != 2 {
			t.Errorf("Expected 2 matching agents, got %d", len(matching))
		}
		if len(pending) != 0 {
			t.Errorf("Expected 0 pending agents, got %d", len(pending))
		}
	})

	t.Run("some agents not idle", func(t *testing.T) {
		activities := []*AgentActivity{
			{PaneID: "test__cc_1", State: StateWaiting},
			{PaneID: "test__cc_2", State: StateGenerating},
		}
		met, matching, pending := checkWaitConditionMet(activities, opts)
		if met {
			t.Error("Expected condition not to be met when some agents are generating")
		}
		if len(matching) != 1 {
			t.Errorf("Expected 1 matching agent, got %d", len(matching))
		}
		if len(pending) != 1 {
			t.Errorf("Expected 1 pending agent, got %d", len(pending))
		}
	})

	t.Run("no agents", func(t *testing.T) {
		activities := []*AgentActivity{}
		met, _, _ := checkWaitConditionMet(activities, opts)
		if met {
			t.Error("Expected condition not to be met with no agents")
		}
	})
}

func TestCheckWaitConditionMet_AnyMode(t *testing.T) {
	// Test with ANY mode
	opts := WaitOptions{
		Condition:  "idle",
		WaitForAny: true,
		CountN:     1,
	}

	t.Run("one agent idle", func(t *testing.T) {
		activities := []*AgentActivity{
			{PaneID: "test__cc_1", State: StateWaiting},
			{PaneID: "test__cc_2", State: StateGenerating},
		}
		met, matching, _ := checkWaitConditionMet(activities, opts)
		if !met {
			t.Error("Expected condition to be met when at least one agent is idle")
		}
		if len(matching) != 1 {
			t.Errorf("Expected 1 matching agent, got %d", len(matching))
		}
	})

	t.Run("no agents idle", func(t *testing.T) {
		activities := []*AgentActivity{
			{PaneID: "test__cc_1", State: StateGenerating},
			{PaneID: "test__cc_2", State: StateGenerating},
		}
		met, _, _ := checkWaitConditionMet(activities, opts)
		if met {
			t.Error("Expected condition not to be met when no agents are idle")
		}
	})

	t.Run("count N requirement", func(t *testing.T) {
		opts := WaitOptions{
			Condition:  "idle",
			WaitForAny: true,
			CountN:     2,
		}
		activities := []*AgentActivity{
			{PaneID: "test__cc_1", State: StateWaiting},
			{PaneID: "test__cc_2", State: StateGenerating},
			{PaneID: "test__cc_3", State: StateWaiting},
		}
		met, matching, _ := checkWaitConditionMet(activities, opts)
		if !met {
			t.Error("Expected condition to be met when 2 agents are idle and CountN=2")
		}
		if len(matching) != 2 {
			t.Errorf("Expected 2 matching agents, got %d", len(matching))
		}
	})
}

func TestCompleteCondition(t *testing.T) {
	t.Run("waiting with no recent output", func(t *testing.T) {
		activity := &AgentActivity{
			State:      StateWaiting,
			LastOutput: time.Time{}, // Zero time - no output recorded
		}
		got := meetsSingleWaitCondition(activity, WaitConditionComplete)
		if !got {
			t.Error("Expected 'complete' condition to be met for waiting agent with no output")
		}
	})

	t.Run("waiting with recent output", func(t *testing.T) {
		activity := &AgentActivity{
			State:      StateWaiting,
			LastOutput: time.Now(), // Just now
		}
		got := meetsSingleWaitCondition(activity, WaitConditionComplete)
		if got {
			t.Error("Expected 'complete' condition not to be met for waiting agent with recent output")
		}
	})

	t.Run("waiting with old output", func(t *testing.T) {
		activity := &AgentActivity{
			State:      StateWaiting,
			LastOutput: time.Now().Add(-10 * time.Second), // 10 seconds ago
		}
		got := meetsSingleWaitCondition(activity, WaitConditionComplete)
		if !got {
			t.Error("Expected 'complete' condition to be met for waiting agent with old output")
		}
	})

	t.Run("generating does not meet complete", func(t *testing.T) {
		activity := &AgentActivity{
			State: StateGenerating,
		}
		got := meetsSingleWaitCondition(activity, WaitConditionComplete)
		if got {
			t.Error("Expected 'complete' condition not to be met for generating agent")
		}
	})
}

func TestWaitConditionConstants(t *testing.T) {
	// Ensure condition constants have expected string values
	if WaitConditionIdle != "idle" {
		t.Errorf("WaitConditionIdle = %q, want %q", WaitConditionIdle, "idle")
	}
	if WaitConditionComplete != "complete" {
		t.Errorf("WaitConditionComplete = %q, want %q", WaitConditionComplete, "complete")
	}
	if WaitConditionGenerating != "generating" {
		t.Errorf("WaitConditionGenerating = %q, want %q", WaitConditionGenerating, "generating")
	}
	if WaitConditionHealthy != "healthy" {
		t.Errorf("WaitConditionHealthy = %q, want %q", WaitConditionHealthy, "healthy")
	}
}

func TestWaitOptionsDefaults(t *testing.T) {
	opts := WaitOptions{
		Session:   "test",
		Condition: "idle",
	}

	// Check that zero values are handled correctly
	if opts.CountN != 0 {
		t.Errorf("Default CountN should be 0, got %d", opts.CountN)
	}
	if opts.WaitForAny {
		t.Error("Default WaitForAny should be false")
	}
	if opts.ExitOnError {
		t.Error("Default ExitOnError should be false")
	}
}

func TestSplitWaitConditions(t *testing.T) {
	paneConditions, attentionConditions := splitWaitConditions([]string{
		" idle ",
		"action_required",
		" pane_changed ",
		"healthy",
	})

	if len(paneConditions) != 2 || paneConditions[0] != "idle" || paneConditions[1] != "healthy" {
		t.Fatalf("paneConditions = %#v, want [idle healthy]", paneConditions)
	}
	if len(attentionConditions) != 2 || attentionConditions[0] != "action_required" || attentionConditions[1] != "pane_changed" {
		t.Fatalf("attentionConditions = %#v, want [action_required pane_changed]", attentionConditions)
	}
}

func TestAttentionEventMatchesWaitCondition(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		event     AttentionEvent
		want      bool
	}{
		{
			name:      "attention requires interesting or higher",
			condition: WaitConditionAttention,
			event: AttentionEvent{
				Actionability: ActionabilityInteresting,
			},
			want: true,
		},
		{
			name:      "background does not satisfy attention",
			condition: WaitConditionAttention,
			event: AttentionEvent{
				Actionability: ActionabilityBackground,
			},
			want: false,
		},
		{
			name:      "context hot uses derived signal",
			condition: WaitConditionContextHot,
			event: AttentionEvent{
				Type:    EventTypeAlertWarning,
				Source:  "event_bus.context",
				Summary: "context usage 95%",
				Details: map[string]any{"usage_percent": 95.0},
			},
			want: true,
		},
		{
			name:      "session changed uses lifecycle event",
			condition: WaitConditionSessionChanged,
			event: AttentionEvent{
				Type: EventTypeSessionCreated,
			},
			want: true,
		},
		{
			name:      "pane changed uses lifecycle event",
			condition: WaitConditionPaneChanged,
			event: AttentionEvent{
				Type: EventTypePaneResized,
			},
			want: true,
		},
		{
			name:      "reservation conflict uses conflict classifier",
			condition: WaitConditionReservationConflict,
			event: AttentionEvent{
				Type:   EventTypeFileConflict,
				Source: "watcher.file_reservation",
				Details: map[string]any{
					"path":            "internal/robot/wait.go",
					"holders":         []string{"BlueLake"},
					"requestor_agent": "QuietSeal",
				},
			},
			want: true,
		},
		{
			name:      "file conflict uses conflict classifier",
			condition: WaitConditionFileConflict,
			event: AttentionEvent{
				Type:   EventTypeFileConflict,
				Source: "tracker.conflicts",
				Details: map[string]any{
					"path":   "internal/robot/wait.go",
					"agents": []string{"BlueLake", "QuietSeal"},
				},
			},
			want: true,
		},
		{
			name:      "mail pending uses mail received events",
			condition: WaitConditionMailPending,
			event: AttentionEvent{
				Category: EventCategoryMail,
				Type:     EventTypeMailReceived,
			},
			want: true,
		},
		{
			name:      "mail ack required uses ack events",
			condition: WaitConditionMailAckRequired,
			event: AttentionEvent{
				Category: EventCategoryMail,
				Type:     EventTypeMailAckRequired,
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := attentionEventMatchesWaitCondition(tt.condition, tt.event)
			if got != tt.want {
				t.Fatalf("attentionEventMatchesWaitCondition(%q, %#v) = %v, want %v", tt.condition, tt.event, got, tt.want)
			}
		})
	}
}

func TestCheckAttentionConditions_AllConditionsRequired(t *testing.T) {
	// Ensure globalFeedOnce has fired before we override globalFeed
	_ = GetAttentionFeed()
	oldFeed := PeekAttentionFeed()
	feed := newWaitTestFeed(time.Hour)
	SetAttentionFeed(feed)
	defer SetAttentionFeed(oldFeed)

	mailEvent := feed.Append(AttentionEvent{
		Session:       "proj",
		Category:      EventCategoryMail,
		Type:          EventTypeMailReceived,
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "new mail",
	})
	actionRequiredEvent := feed.Append(AttentionEvent{
		Session:       "proj",
		Category:      EventCategoryAlert,
		Type:          EventTypeAlertWarning,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
		Summary:       "operator action required",
	})

	result := checkAttentionConditions([]string{WaitConditionMailPending, WaitConditionActionRequired}, 0, "proj", "")
	if result == nil || !result.Met {
		t.Fatalf("expected attention conditions to be met, got %#v", result)
	}
	if result.Condition != WaitConditionActionRequired {
		t.Fatalf("Condition = %q, want %q", result.Condition, WaitConditionActionRequired)
	}
	if result.TriggerEvent == nil || result.TriggerEvent.Cursor != actionRequiredEvent.Cursor {
		t.Fatalf("TriggerEvent cursor = %#v, want %d", result.TriggerEvent, actionRequiredEvent.Cursor)
	}
	if result.ObservedCursor != actionRequiredEvent.Cursor {
		t.Fatalf("ObservedCursor = %d, want %d", result.ObservedCursor, actionRequiredEvent.Cursor)
	}
	if result.NextCursor != actionRequiredEvent.Cursor {
		t.Fatalf("NextCursor = %d, want %d", result.NextCursor, actionRequiredEvent.Cursor)
	}
	if result.OldestCursor != mailEvent.Cursor {
		t.Fatalf("OldestCursor = %d, want %d", result.OldestCursor, mailEvent.Cursor)
	}
	matchedConditions, ok := result.Details["matched_conditions"].([]string)
	if !ok {
		t.Fatalf("matched_conditions type = %T, want []string", result.Details["matched_conditions"])
	}
	if len(matchedConditions) != 2 || matchedConditions[0] != WaitConditionMailPending || matchedConditions[1] != WaitConditionActionRequired {
		t.Fatalf("matched_conditions = %#v", matchedConditions)
	}
	matchCounts, ok := result.Details["match_count_by_condition"].(map[string]int)
	if !ok {
		t.Fatalf("match_count_by_condition type = %T, want map[string]int", result.Details["match_count_by_condition"])
	}
	if matchCounts[WaitConditionMailPending] != 1 || matchCounts[WaitConditionActionRequired] != 1 {
		t.Fatalf("match_count_by_condition = %#v", matchCounts)
	}

	notMet := checkAttentionConditions([]string{WaitConditionMailPending, WaitConditionActionRequired}, 0, "other", "")
	if notMet == nil {
		t.Fatal("expected a non-nil result for unmet conditions")
	}
	if notMet.Met {
		t.Fatalf("expected session-filtered conditions not to match, got %#v", notMet)
	}
	if notMet.NextCursor != actionRequiredEvent.Cursor {
		t.Fatalf("NextCursor = %d, want %d", notMet.NextCursor, actionRequiredEvent.Cursor)
	}
}

func TestCheckAttentionConditions_CursorExpired(t *testing.T) {
	// Ensure globalFeedOnce has fired before we override globalFeed
	_ = GetAttentionFeed()
	oldFeed := PeekAttentionFeed()
	feed := newWaitTestFeed(time.Nanosecond)
	SetAttentionFeed(feed)
	defer SetAttentionFeed(oldFeed)

	first := feed.Append(AttentionEvent{
		Session:       "proj",
		Category:      EventCategoryMail,
		Type:          EventTypeMailReceived,
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "old mail",
	})
	second := feed.Append(AttentionEvent{
		Session:       "proj",
		Category:      EventCategoryAlert,
		Type:          EventTypeAlertWarning,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
		Summary:       "new attention",
	})

	time.Sleep(2 * time.Millisecond)

	result := checkAttentionConditions([]string{WaitConditionAttention}, first.Cursor, "proj", "")
	if result == nil || result.CursorExpired == nil {
		t.Fatalf("expected cursor expiration, got %#v", result)
	}
	if result.Met {
		t.Fatalf("expected expired cursor result not to be met, got %#v", result)
	}
	if result.OldestCursor != second.Cursor {
		t.Fatalf("OldestCursor = %d, want %d", result.OldestCursor, second.Cursor)
	}
	if result.NextCursor != second.Cursor {
		t.Fatalf("NextCursor = %d, want %d", result.NextCursor, second.Cursor)
	}
}

func TestCheckAttentionConditions_ProfileFiltersLifecycleNoise(t *testing.T) {
	// Ensure globalFeedOnce has fired before we override globalFeed
	_ = GetAttentionFeed()
	oldFeed := PeekAttentionFeed()
	feed := newWaitTestFeed(time.Hour)
	SetAttentionFeed(feed)
	defer SetAttentionFeed(oldFeed)

	lifecycle := feed.Append(AttentionEvent{
		Session:       "proj",
		Category:      EventCategorySession,
		Type:          EventTypeSessionCreated,
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       "session created",
	})

	operator := checkAttentionConditions([]string{WaitConditionSessionChanged}, 0, "proj", "operator")
	if operator == nil {
		t.Fatal("expected operator result")
	}
	if operator.Met {
		t.Fatalf("operator profile should suppress background lifecycle noise, got %#v", operator)
	}
	if operator.NextCursor != lifecycle.Cursor {
		t.Fatalf("NextCursor = %d, want %d", operator.NextCursor, lifecycle.Cursor)
	}
	if got := operator.Details["profile"]; got != "operator" {
		t.Fatalf("profile detail = %#v, want operator", got)
	}
	if got := operator.Details["raw_event_count"]; got != 1 {
		t.Fatalf("raw_event_count = %#v, want 1", got)
	}
	if got := operator.Details["scanned_event_count"]; got != 0 {
		t.Fatalf("scanned_event_count = %#v, want 0 after operator filtering", got)
	}

	debug := checkAttentionConditions([]string{WaitConditionSessionChanged}, 0, "proj", "debug")
	if debug == nil || !debug.Met {
		t.Fatalf("debug profile should retain lifecycle event, got %#v", debug)
	}
	if debug.TriggerEvent == nil || debug.TriggerEvent.Cursor != lifecycle.Cursor {
		t.Fatalf("TriggerEvent = %#v, want cursor %d", debug.TriggerEvent, lifecycle.Cursor)
	}
}

func TestCheckAttentionConditions_IgnoresHiddenOperatorState(t *testing.T) {
	_ = GetAttentionFeed()
	oldFeed := PeekAttentionFeed()
	feed := newWaitTestFeed(time.Hour)
	SetAttentionFeed(feed)
	defer SetAttentionFeed(oldFeed)

	hidden := feed.Append(AttentionEvent{
		Session:       "proj",
		Category:      EventCategoryAlert,
		Type:          EventTypeAlertWarning,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
		Summary:       "acknowledged alert",
		Details: map[string]any{
			attentionDetailState:        string(state.AttentionStateAcknowledged),
			attentionDetailHiddenReason: attentionHiddenReasonAcknowledged,
		},
	})

	result := checkAttentionConditions([]string{WaitConditionAttention}, 0, "proj", "")
	if result == nil {
		t.Fatal("expected attention result")
	}
	if result.Met {
		t.Fatalf("hidden attention item should not wake operator loop, got %#v", result)
	}
	if result.NextCursor != hidden.Cursor {
		t.Fatalf("NextCursor = %d, want %d", result.NextCursor, hidden.Cursor)
	}
	if got := result.Details["raw_event_count"]; got != 1 {
		t.Fatalf("raw_event_count = %#v, want 1", got)
	}
	if got := result.Details["scanned_event_count"]; got != 0 {
		t.Fatalf("scanned_event_count = %#v, want 0 after operator-state filtering", got)
	}
}

func newWaitTestFeed(retention time.Duration) *AttentionFeed {
	return NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       64,
		RetentionPeriod:   retention,
		HeartbeatInterval: 0,
	})
}

// =============================================================================
// filterWaitPanes Tests
// =============================================================================

func TestFilterWaitPanes(t *testing.T) {

	// Create test panes with various agent types and indices
	// Note: detectAgentType looks for patterns like "claude", "codex", "gemini" in title
	// or short forms like "__cc_", "__cod_", "__gmi_" with word boundaries
	testPanes := []tmux.Pane{
		{Index: 0, Title: "user_0"},                                 // User pane, should be filtered out
		{Index: 1, Title: "myproject__cc_1"},                        // Claude agent (short form with prefix)
		{Index: 2, Title: "myproject__cod_2"},                       // Codex agent (short form with prefix)
		{Index: 3, Title: "myproject__gmi_3"},                       // Gemini agent (short form with prefix)
		{Index: 4, Title: "myproject__cc_4"},                        // Another Claude agent
		{Index: 5, Title: "unknown_agent"},                          // Unknown, should be filtered out
		{Index: 6, Title: "bash"},                                   // Non-agent pane
		{Index: 7, Title: "claude_session", Type: tmux.AgentClaude}, // Using full type name
	}

	t.Run("no_filters_returns_only_agents", func(t *testing.T) {
		opts := WaitOptions{}
		result := filterWaitPanes(testPanes, opts)

		// Should exclude user_0, unknown_agent, bash
		// Should include cc_1, cod_2, gmi_3, cc_4, claude_7
		if len(result) != 5 {
			t.Errorf("filterWaitPanes() returned %d panes, want 5", len(result))
			for _, p := range result {
				t.Logf("  included: Index=%d Title=%q", p.Index, p.Title)
			}
		}
	})

	t.Run("filter_by_pane_indices", func(t *testing.T) {
		opts := WaitOptions{
			PaneIndices: []int{1, 3},
		}
		result := filterWaitPanes(testPanes, opts)

		if len(result) != 2 {
			t.Errorf("filterWaitPanes() returned %d panes, want 2", len(result))
		}

		// Verify correct panes selected
		indices := make(map[int]bool)
		for _, p := range result {
			indices[p.Index] = true
		}
		if !indices[1] || !indices[3] {
			t.Errorf("Expected panes 1 and 3, got indices: %v", indices)
		}
	})

	t.Run("filter_by_agent_type_claude", func(t *testing.T) {
		opts := WaitOptions{
			AgentType: "claude", // detectAgentType returns canonical names like "claude" not "cc"
		}
		result := filterWaitPanes(testPanes, opts)

		// myproject__cc_1, myproject__cc_4, and claude_session should match "claude" type
		if len(result) != 3 {
			t.Errorf("filterWaitPanes(AgentType=claude) returned %d panes, want 3", len(result))
		}
	})

	t.Run("filter_by_agent_type_codex", func(t *testing.T) {
		opts := WaitOptions{
			AgentType: "codex",
		}
		result := filterWaitPanes(testPanes, opts)

		// cod_2 should match "codex" type (detectAgentType maps cod->codex)
		if len(result) != 1 {
			t.Errorf("filterWaitPanes(AgentType=codex) returned %d panes, want 1", len(result))
		}
		if len(result) > 0 && result[0].Index != 2 {
			t.Errorf("Expected pane index 2, got %d", result[0].Index)
		}
	})

	t.Run("filter_by_agent_type_gemini", func(t *testing.T) {
		opts := WaitOptions{
			AgentType: "gemini",
		}
		result := filterWaitPanes(testPanes, opts)

		// gmi_3 should match "gemini" type (detectAgentType maps gmi->gemini)
		if len(result) != 1 {
			t.Errorf("filterWaitPanes(AgentType=gemini) returned %d panes, want 1", len(result))
		}
		if len(result) > 0 && result[0].Index != 3 {
			t.Errorf("Expected pane index 3, got %d", result[0].Index)
		}
	})

	t.Run("filter_by_both_indices_and_type", func(t *testing.T) {
		opts := WaitOptions{
			PaneIndices: []int{1, 2, 3, 4},
			AgentType:   "claude", // Use canonical name
		}
		result := filterWaitPanes(testPanes, opts)

		// Only myproject__cc_1 and myproject__cc_4 should match (both in indices AND type=claude)
		if len(result) != 2 {
			t.Errorf("filterWaitPanes() returned %d panes, want 2", len(result))
		}
	})

	t.Run("empty_panes_input", func(t *testing.T) {
		opts := WaitOptions{}
		result := filterWaitPanes([]tmux.Pane{}, opts)

		if len(result) != 0 {
			t.Errorf("filterWaitPanes() with empty input returned %d panes, want 0", len(result))
		}
	})

	t.Run("no_matching_indices", func(t *testing.T) {
		opts := WaitOptions{
			PaneIndices: []int{99, 100},
		}
		result := filterWaitPanes(testPanes, opts)

		if len(result) != 0 {
			t.Errorf("filterWaitPanes() returned %d panes, want 0", len(result))
		}
	})

	t.Run("case_insensitive_agent_type", func(t *testing.T) {
		opts := WaitOptions{
			AgentType: "CLAUDE", // uppercase canonical name
		}
		result := filterWaitPanes(testPanes, opts)

		// Should still match claude agents (case insensitive via strings.EqualFold)
		if len(result) != 3 {
			t.Errorf("filterWaitPanes(AgentType=CLAUDE) returned %d panes, want 3 (case insensitive)", len(result))
		}
	})

	t.Run("alias_agent_type_matches", func(t *testing.T) {
		tests := []struct {
			name      string
			agentType string
			wantCount int
		}{
			{name: "claude short alias", agentType: "cc", wantCount: 3},
			{name: "claude cli alias", agentType: "claude_code", wantCount: 3},
			{name: "codex alias", agentType: "openai-codex", wantCount: 1},
			{name: "gemini alias", agentType: "google-gemini", wantCount: 1},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				opts := WaitOptions{AgentType: tc.agentType}
				result := filterWaitPanes(testPanes, opts)
				if len(result) != tc.wantCount {
					t.Errorf("filterWaitPanes(AgentType=%q) returned %d panes, want %d", tc.agentType, len(result), tc.wantCount)
				}
			})
		}
	})

	t.Run("prefers_parsed_pane_type_over_title", func(t *testing.T) {
		panes := []tmux.Pane{
			{Index: 1, Title: "custom scratchpad", Type: tmux.AgentClaude},
			{Index: 2, Title: "claude_notes", Type: tmux.AgentUser},
		}

		result := filterWaitPanes(panes, WaitOptions{})
		if len(result) != 1 {
			t.Fatalf("filterWaitPanes() returned %d panes, want 1", len(result))
		}
		if result[0].Index != 1 {
			t.Fatalf("filterWaitPanes() selected pane %d, want 1", result[0].Index)
		}
	})

	t.Run("user_pane_always_filtered", func(t *testing.T) {
		// Even if explicitly requested by index, user pane should be excluded
		opts := WaitOptions{
			PaneIndices: []int{0}, // user_0 pane
		}
		result := filterWaitPanes(testPanes, opts)

		if len(result) != 0 {
			t.Errorf("User pane should be filtered out, got %d panes", len(result))
		}
	})
}

func TestWaitPaneAgentTypePrefersParsedPaneType(t *testing.T) {
	pane := tmux.Pane{
		Title: "operator-notes",
		Type:  tmux.AgentGemini,
	}

	if got := waitPaneAgentType(pane); got != "gemini" {
		t.Fatalf("waitPaneAgentType() = %q, want gemini", got)
	}
}

func TestResolveWaitPanesCanonicalSelectors(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%10", WindowIndex: 0, Index: 0, Type: tmux.AgentUser},
		{ID: "%11", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude},
		{ID: "%20", WindowIndex: 1, Index: 0, Type: tmux.AgentCodex},
		{ID: "%21", WindowIndex: 1, Index: 1, Type: tmux.AgentGemini},
	}

	selected, err := resolveWaitPanes(panes, WaitOptions{PaneSelectors: []string{"1"}})
	if err != nil {
		t.Fatalf("resolveWaitPanes(window selector) error = %v", err)
	}
	if len(selected) != 2 || selected[0].ID != "%20" || selected[1].ID != "%21" {
		t.Fatalf("window selector resolved %+v, want %%20 then %%21", selected)
	}

	selected, err = resolveWaitPanes(panes, WaitOptions{PaneSelectors: []string{"0.1", "%11", "0.1"}})
	if err != nil {
		t.Fatalf("resolveWaitPanes(alias dedup) error = %v", err)
	}
	if len(selected) != 1 || selected[0].ID != "%11" {
		t.Fatalf("alias dedup resolved %+v, want only %%11", selected)
	}

	if _, err := resolveWaitPanes(panes, WaitOptions{PaneSelectors: []string{"9.0"}}); err == nil || paneSelectorRobotErrorCode(err) != ErrCodePaneNotFound {
		t.Fatalf("missing selector error = %v, code = %q", err, paneSelectorRobotErrorCode(err))
	}
	if _, err := resolveWaitPanes(panes, WaitOptions{PaneSelectors: []string{"1.x"}}); err == nil || paneSelectorRobotErrorCode(err) != ErrCodeInvalidFlag {
		t.Fatalf("malformed selector error = %v, code = %q", err, paneSelectorRobotErrorCode(err))
	}
}

// rate_limited vs rate_limit_lifted semantics (ntm-xh9t): rate_limited fires
// when a pane BECOMES limited; rate_limit_lifted is met only while the pane is
// clear, so the wait blocks until every target pane's wall drops. Several doc
// sites historically taught rate_limited with inverted semantics; these tests
// pin the real contract for both directions.
func TestRateLimitWaitConditionDirections(t *testing.T) {
	limited := &AgentActivity{State: StateError, RateLimited: true}
	clear := &AgentActivity{State: StateWaiting, RateLimited: false}

	if !meetsSingleWaitCondition(limited, WaitConditionRateLimited) {
		t.Error("rate_limited must be met by a limited pane")
	}
	if meetsSingleWaitCondition(clear, WaitConditionRateLimited) {
		t.Error("rate_limited must not be met by a clear pane")
	}
	if meetsSingleWaitCondition(limited, WaitConditionRateLimitLifted) {
		t.Error("rate_limit_lifted must not be met while the pane is limited")
	}
	if !meetsSingleWaitCondition(clear, WaitConditionRateLimitLifted) {
		t.Error("rate_limit_lifted must be met by a clear pane")
	}

	// Composed check: a mixed swarm keeps the lifted-wait pending until the
	// last limited pane clears.
	opts := WaitOptions{Condition: WaitConditionRateLimitLifted}
	activities := []*AgentActivity{
		{PaneID: "1", State: StateWaiting, RateLimited: false},
		{PaneID: "2", State: StateError, RateLimited: true},
	}
	met, _, pending := checkWaitConditionMet(activities, opts)
	if met {
		t.Fatal("lifted-wait reported met while a pane is still limited")
	}
	if len(pending) != 1 || pending[0] != "2" {
		t.Fatalf("pending = %v, want the limited pane", pending)
	}
	activities[1].RateLimited = false
	activities[1].State = StateWaiting
	met, matching, _ := checkWaitConditionMet(activities, opts)
	if !met || len(matching) != 2 {
		t.Fatalf("lifted-wait met=%v matching=%d after all panes cleared, want met with 2", met, len(matching))
	}
}

// agent_ready semantics (ntm-3a9i): ready = classified agent state (prompt or
// working), pending on unknown/error/stalled/rate-limited. Replaces the
// documented "sleep 8-10 after relaunch" folklore.
func TestAgentReadyWaitCondition(t *testing.T) {
	cases := []struct {
		name     string
		activity *AgentActivity
		want     bool
	}{
		{"waiting at prompt is ready", &AgentActivity{State: StateWaiting}, true},
		{"thinking is ready", &AgentActivity{State: StateThinking}, true},
		{"generating is ready", &AgentActivity{State: StateGenerating}, true},
		{"unknown (bare shell / booting) pending", &AgentActivity{State: StateUnknown}, false},
		{"error pending", &AgentActivity{State: StateError}, false},
		{"stalled pending", &AgentActivity{State: StateStalled}, false},
		{"rate-limited prompt not ready", &AgentActivity{State: StateWaiting, RateLimited: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := meetsSingleWaitCondition(tc.activity, WaitConditionAgentReady); got != tc.want {
				t.Errorf("agent_ready(%+v) = %v, want %v", tc.activity, got, tc.want)
			}
		})
	}
	if !isSingleValidWaitCondition(WaitConditionAgentReady) {
		t.Error("agent_ready must be a valid wait condition")
	}
}

package robot

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

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
		event      AttentionEvent
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
					"path":           "internal/robot/wait.go",
					"holders":        []string{"BlueLake"},
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

	result := checkAttentionConditions([]string{WaitConditionMailPending, WaitConditionActionRequired}, 0, "proj")
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

	notMet := checkAttentionConditions([]string{WaitConditionMailPending, WaitConditionActionRequired}, 0, "other")
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

	result := checkAttentionConditions([]string{WaitConditionAttention}, first.Cursor, "proj")
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
	t.Parallel()

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

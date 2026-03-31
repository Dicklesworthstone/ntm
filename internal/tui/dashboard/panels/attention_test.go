package panels

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func TestNewAttentionPanel(t *testing.T) {
	panel := NewAttentionPanel()
	if panel == nil {
		t.Fatal("NewAttentionPanel returned nil")
	}

	cfg := panel.Config()
	if cfg.ID != "attention" {
		t.Fatalf("Config().ID = %q, want %q", cfg.ID, "attention")
	}
	if cfg.Title != "Attention" {
		t.Fatalf("Config().Title = %q, want %q", cfg.Title, "Attention")
	}
}

func TestAttentionPanelViewFeedInactive(t *testing.T) {
	panel := NewAttentionPanel()
	panel.SetSize(80, 18)
	panel.SetData(nil, false)

	view := panel.View()
	if !strings.Contains(view, "Feed not active") {
		t.Fatalf("expected inactive attention panel title, got %q", view)
	}
	if !strings.Contains(view, "Attention Feed not active") {
		t.Fatalf("expected inactive attention panel description, got %q", view)
	}
}

func TestAttentionPanelViewAllClear(t *testing.T) {
	panel := NewAttentionPanel()
	panel.SetSize(80, 18)
	panel.SetData(nil, true)

	view := panel.View()
	if !strings.Contains(view, "All clear") {
		t.Fatalf("expected all-clear title, got %q", view)
	}
	if !strings.Contains(view, "No attention items") {
		t.Fatalf("expected empty-state description, got %q", view)
	}
}

func TestAttentionPanelSelectedItemAndCursorClamp(t *testing.T) {
	panel := NewAttentionPanel()
	panel.SetData([]AttentionItem{
		{Summary: "first", Actionability: robot.ActionabilityInteresting, Timestamp: time.Now()},
	}, true)
	panel.cursor = 5
	panel.SetData(panel.items, true)

	item := panel.SelectedItem()
	if item == nil || item.Summary != "first" {
		t.Fatalf("SelectedItem() = %+v, want first item", item)
	}
}

func TestAttentionPanelSetDataSortsActionabilityAndRecency(t *testing.T) {
	panel := NewAttentionPanel()
	base := time.Date(2026, 3, 21, 12, 0, 0, 0, time.UTC)

	panel.SetData([]AttentionItem{
		{Summary: "interesting-old", Actionability: robot.ActionabilityInteresting, Timestamp: base},
		{Summary: "action-new", Actionability: robot.ActionabilityActionRequired, Timestamp: base.Add(2 * time.Minute)},
		{Summary: "interesting-new", Actionability: robot.ActionabilityInteresting, Timestamp: base.Add(3 * time.Minute)},
	}, true)

	if got := panel.items[0].Summary; got != "action-new" {
		t.Fatalf("expected action_required item first, got %q", got)
	}
	if got := panel.items[1].Summary; got != "interesting-new" {
		t.Fatalf("expected newer interesting item second, got %q", got)
	}
}

func TestAttentionPanelNavigationMovesSelection(t *testing.T) {
	panel := NewAttentionPanel()
	panel.SetSize(80, 18)
	panel.SetData([]AttentionItem{
		{Summary: "first", Actionability: robot.ActionabilityInteresting, Timestamp: time.Now()},
		{Summary: "second", Actionability: robot.ActionabilityInteresting, Timestamp: time.Now().Add(-2 * time.Minute)},
	}, true)
	panel.Focus()

	updated, cmd := panel.Update(tea.KeyMsg{Type: tea.KeyDown})
	if cmd != nil {
		t.Fatalf("expected nil command from navigation update, got %v", cmd)
	}

	got := updated.(*AttentionPanel).SelectedItem()
	if got == nil || got.Summary != "second" {
		t.Fatalf("selected item after down key = %+v, want second", got)
	}
}

func TestAttentionPanelKeybindingsIncludeZoomToSource(t *testing.T) {
	panel := NewAttentionPanel()
	bindings := panel.Keybindings()

	found := false
	for _, binding := range bindings {
		if binding.Action == "zoom_to_source" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected zoom_to_source keybinding")
	}
}

func TestAttentionPanelSelectCursor(t *testing.T) {
	panel := NewAttentionPanel()
	panel.SetData([]AttentionItem{
		{Summary: "older", Cursor: 10, Timestamp: time.Now().Add(-2 * time.Minute)},
		{Summary: "newer", Cursor: 25, Timestamp: time.Now()},
	}, true)

	if !panel.SelectCursor(25) {
		t.Fatal("expected SelectCursor to find cursor 25")
	}
	if got := panel.SelectedItem(); got == nil || got.Cursor != 25 {
		t.Fatalf("selected item = %+v, want cursor 25", got)
	}
}

func TestAttentionPanelSelectNearestCursor(t *testing.T) {
	panel := NewAttentionPanel()
	panel.SetData([]AttentionItem{
		{Summary: "older", Cursor: 40, Timestamp: time.Now().Add(-3 * time.Minute)},
		{Summary: "newer", Cursor: 44, Timestamp: time.Now()},
	}, true)

	if !panel.SelectNearestCursor(42) {
		t.Fatal("expected SelectNearestCursor to pick a fallback item")
	}
	if got := panel.SelectedItem(); got == nil || got.Cursor != 44 {
		t.Fatalf("selected item = %+v, want nearest newer cursor 44", got)
	}
}

func TestAttentionPanelScrollsSelectedItemIntoView(t *testing.T) {
	panel := NewAttentionPanel()
	panel.SetSize(40, 10)
	panel.Focus()

	items := make([]AttentionItem, 0, 8)
	now := time.Now()
	for i := 0; i < 8; i++ {
		items = append(items, AttentionItem{
			Summary:       "item",
			Actionability: robot.ActionabilityInteresting,
			Timestamp:     now.Add(time.Duration(i) * time.Minute),
			SourcePane:    i,
			SourceAgent:   "codex",
			Cursor:        int64(i + 1),
		})
	}
	panel.SetData(items, true)

	for i := 0; i < len(items)-1; i++ {
		panel.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	view := panel.View()

	if panel.scroll == nil {
		t.Fatal("expected scroll panel to be initialized")
	}
	if got := panel.scroll.YOffset(); got == 0 {
		t.Fatalf("expected scroll offset to move for deep selection, got %d", got)
	}
	if !strings.Contains(view, "%") {
		t.Fatalf("expected attention view to include scroll percent badge, got %q", view)
	}
}

// =============================================================================
// Overlay-Feed Integration Tests (br-rb9oj)
// =============================================================================

func TestAttentionPanelTruncationAwareness(t *testing.T) {
	t.Parallel()

	panel := NewAttentionPanel()
	panel.SetSize(80, 10) // Small height to force truncation awareness

	// Create more items than can display
	items := make([]AttentionItem, 20)
	now := time.Now()
	for i := range items {
		items[i] = AttentionItem{
			Summary:       "item " + string(rune('A'+i)),
			Actionability: robot.ActionabilityInteresting,
			Timestamp:     now.Add(time.Duration(i) * time.Minute),
			Cursor:        int64(i + 1),
		}
	}
	panel.SetData(items, true)

	// Panel should be aware it has more items than visible
	if len(panel.items) != 20 {
		t.Errorf("expected 20 items in panel data, got %d", len(panel.items))
	}

	// Scroll should be initialized for overflow handling
	if panel.scroll == nil {
		t.Fatal("expected scroll panel to be initialized for overflow")
	}

	t.Logf("TRUNCATION_AWARENESS items=%d scroll_initialized=%v", len(panel.items), panel.scroll != nil)
}

func TestAttentionPanelCursorContinuityAcrossUpdates(t *testing.T) {
	t.Parallel()

	panel := NewAttentionPanel()
	panel.SetSize(80, 18)

	// Initial data with cursor 50 selected
	items1 := []AttentionItem{
		{Summary: "item-a", Cursor: 40, Timestamp: time.Now().Add(-2 * time.Minute)},
		{Summary: "item-b", Cursor: 50, Timestamp: time.Now()},
		{Summary: "item-c", Cursor: 60, Timestamp: time.Now().Add(time.Minute)},
	}
	panel.SetData(items1, true)
	panel.SelectCursor(50)

	selected1 := panel.SelectedItem()
	if selected1 == nil || selected1.Cursor != 50 {
		t.Fatalf("initial selection = %+v, want cursor 50", selected1)
	}

	// Update with same cursor still present
	items2 := []AttentionItem{
		{Summary: "item-b", Cursor: 50, Timestamp: time.Now()},
		{Summary: "item-d", Cursor: 70, Timestamp: time.Now().Add(2 * time.Minute)},
	}
	panel.SetData(items2, true)

	// Selection should persist across update (cursor 50 still exists)
	selected2 := panel.SelectedItem()
	if selected2 == nil || selected2.Cursor != 50 {
		t.Fatalf("expected selection to remain on cursor 50 after update, got %+v", selected2)
	}
}

func TestAttentionPanelSetDataPreservesSelectionAcrossInsertedItems(t *testing.T) {
	t.Parallel()

	panel := NewAttentionPanel()
	panel.SetSize(80, 18)

	items1 := []AttentionItem{
		{Summary: "oldest", Cursor: 10, Timestamp: time.Now().Add(-3 * time.Minute)},
		{Summary: "target", Cursor: 20, Timestamp: time.Now().Add(-2 * time.Minute)},
	}
	panel.SetData(items1, true)
	if !panel.SelectCursor(20) {
		t.Fatal("expected initial selection on cursor 20")
	}

	items2 := []AttentionItem{
		{Summary: "newest", Cursor: 40, Timestamp: time.Now()},
		{Summary: "newer", Cursor: 30, Timestamp: time.Now().Add(-time.Minute)},
		{Summary: "target", Cursor: 20, Timestamp: time.Now().Add(-2 * time.Minute)},
	}
	panel.SetData(items2, true)

	selected := panel.SelectedItem()
	if selected == nil || selected.Cursor != 20 {
		t.Fatalf("expected selection to stay on cursor 20 after insertions, got %+v", selected)
	}
}

func TestAttentionPanelSetDataFallsBackToNearestCursorWhenSelectionRemoved(t *testing.T) {
	t.Parallel()

	panel := NewAttentionPanel()
	panel.SetSize(80, 18)

	items1 := []AttentionItem{
		{Summary: "older", Cursor: 40, Timestamp: time.Now().Add(-3 * time.Minute)},
		{Summary: "target", Cursor: 42, Timestamp: time.Now().Add(-2 * time.Minute)},
		{Summary: "newer", Cursor: 44, Timestamp: time.Now().Add(-time.Minute)},
	}
	panel.SetData(items1, true)
	if !panel.SelectCursor(42) {
		t.Fatal("expected initial selection on cursor 42")
	}

	items2 := []AttentionItem{
		{Summary: "older", Cursor: 40, Timestamp: time.Now().Add(-3 * time.Minute)},
		{Summary: "newer", Cursor: 44, Timestamp: time.Now()},
	}
	panel.SetData(items2, true)

	selected := panel.SelectedItem()
	if selected == nil || selected.Cursor != 44 {
		t.Fatalf("expected nearest surviving cursor 44 after removal, got %+v", selected)
	}
}

func TestAttentionPanelGracefulDegradationWhenFeedUnavailable(t *testing.T) {
	t.Parallel()

	panel := NewAttentionPanel()
	panel.SetSize(80, 18)

	// Set feed as unavailable
	panel.SetData(nil, false)

	view := panel.View()

	// Should show user-friendly unavailable state
	if !strings.Contains(view, "not active") && !strings.Contains(view, "unavailable") {
		t.Logf("WARN: view doesn't clearly indicate feed unavailability: %s", view)
	}

	// Should not panic or show broken state
	if strings.Contains(view, "nil") || strings.Contains(view, "panic") {
		t.Fatalf("view shows broken state: %s", view)
	}

	// SelectedItem should return nil gracefully
	if item := panel.SelectedItem(); item != nil {
		t.Fatalf("expected nil selected item when feed unavailable, got %+v", item)
	}

	t.Logf("GRACEFUL_DEGRADATION feed_available=false view_ok=true")
}

func TestAttentionPanelItemCountTracking(t *testing.T) {
	t.Parallel()

	panel := NewAttentionPanel()
	panel.SetSize(80, 18)

	// Test empty state
	panel.SetData([]AttentionItem{}, true)
	if len(panel.items) != 0 {
		t.Errorf("expected 0 items after setting empty data, got %d", len(panel.items))
	}

	// Test with items
	items := []AttentionItem{
		{Summary: "item-1", Actionability: robot.ActionabilityActionRequired, Timestamp: time.Now()},
		{Summary: "item-2", Actionability: robot.ActionabilityInteresting, Timestamp: time.Now()},
		{Summary: "item-3", Actionability: robot.ActionabilityInteresting, Timestamp: time.Now()},
	}
	panel.SetData(items, true)

	// Item count should be tracked correctly
	if len(panel.items) != 3 {
		t.Errorf("expected 3 items after SetData, got %d", len(panel.items))
	}

	t.Logf("ITEM_COUNT_TRACKING empty=%d with_items=%d", 0, len(panel.items))
}

func TestAttentionPanelFocusTransitionsPreserveSelection(t *testing.T) {
	t.Parallel()

	panel := NewAttentionPanel()
	panel.SetSize(80, 18)

	items := []AttentionItem{
		{Summary: "first", Cursor: 10, Timestamp: time.Now()},
		{Summary: "second", Cursor: 20, Timestamp: time.Now().Add(-time.Minute)},
	}
	panel.SetData(items, true)
	panel.SelectCursor(20)

	// Get initial selection
	before := panel.SelectedItem()
	if before == nil || before.Cursor != 20 {
		t.Fatalf("expected cursor 20 selected, got %+v", before)
	}

	// Blur and refocus
	panel.Blur()
	panel.Focus()

	// Selection should persist
	after := panel.SelectedItem()
	if after == nil || after.Cursor != before.Cursor {
		t.Fatalf("selection changed after focus transition: before=%+v after=%+v", before, after)
	}

	t.Logf("FOCUS_TRANSITION before=%d after=%d", before.Cursor, after.Cursor)
}

func TestAttentionPanelActionabilityPriorityInSorting(t *testing.T) {
	t.Parallel()

	panel := NewAttentionPanel()
	base := time.Date(2026, 3, 21, 12, 0, 0, 0, time.UTC)

	// Mix of actionability levels with varying timestamps
	items := []AttentionItem{
		{Summary: "interesting-newest", Actionability: robot.ActionabilityInteresting, Timestamp: base.Add(10 * time.Minute)},
		{Summary: "action-oldest", Actionability: robot.ActionabilityActionRequired, Timestamp: base},
		{Summary: "interesting-oldest", Actionability: robot.ActionabilityInteresting, Timestamp: base.Add(-10 * time.Minute)},
		{Summary: "action-newest", Actionability: robot.ActionabilityActionRequired, Timestamp: base.Add(5 * time.Minute)},
	}

	panel.SetData(items, true)

	// ActionRequired items should come first regardless of timestamp
	if len(panel.items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(panel.items))
	}

	// First two should be ActionRequired
	for i := 0; i < 2; i++ {
		if panel.items[i].Actionability != robot.ActionabilityActionRequired {
			t.Errorf("items[%d] actionability = %v, want ActionRequired", i, panel.items[i].Actionability)
		}
	}

	// Within ActionRequired, newest should come first
	if panel.items[0].Summary != "action-newest" {
		t.Errorf("items[0] = %q, want action-newest (newest ActionRequired first)", panel.items[0].Summary)
	}

	t.Logf("ACTIONABILITY_PRIORITY order=%v", []string{
		panel.items[0].Summary, panel.items[1].Summary, panel.items[2].Summary, panel.items[3].Summary,
	})
}

func TestAttentionPanelEmptyStateIsDistinctFromUnavailable(t *testing.T) {
	t.Parallel()

	panel := NewAttentionPanel()
	panel.SetSize(80, 18)

	// Empty but available (all clear)
	panel.SetData([]AttentionItem{}, true)
	emptyView := panel.View()

	// Unavailable (feed not running)
	panel.SetData(nil, false)
	unavailableView := panel.View()

	// Views should be different
	if emptyView == unavailableView {
		t.Fatalf("empty and unavailable views should be distinct:\nempty: %s\nunavailable: %s", emptyView, unavailableView)
	}

	// Empty should indicate "all clear" or similar
	if !strings.Contains(strings.ToLower(emptyView), "clear") && !strings.Contains(strings.ToLower(emptyView), "no ") {
		t.Logf("WARN: empty view doesn't clearly indicate all-clear state")
	}

	// Unavailable should indicate the feed is not running
	if !strings.Contains(strings.ToLower(unavailableView), "not") {
		t.Logf("WARN: unavailable view doesn't clearly indicate feed not running")
	}

	t.Logf("EMPTY_VS_UNAVAILABLE distinct=true")
}

func TestAttentionPanelSelectNearestCursorHandlesEdgeCases(t *testing.T) {
	t.Parallel()

	panel := NewAttentionPanel()

	testCases := []struct {
		name         string
		items        []AttentionItem
		targetCursor int64
		expectFound  bool
		expectCursor int64
	}{
		{
			name:         "empty_panel",
			items:        []AttentionItem{},
			targetCursor: 50,
			expectFound:  false,
			expectCursor: 0,
		},
		{
			name: "exact_match",
			items: []AttentionItem{
				{Summary: "a", Cursor: 50, Timestamp: time.Now()},
			},
			targetCursor: 50,
			expectFound:  true,
			expectCursor: 50,
		},
		{
			name: "nearest_lower",
			items: []AttentionItem{
				{Summary: "a", Cursor: 40, Timestamp: time.Now()},
				{Summary: "b", Cursor: 60, Timestamp: time.Now()},
			},
			targetCursor: 45,
			expectFound:  true,
			expectCursor: 40, // Closest to 45
		},
		{
			name: "nearest_higher",
			items: []AttentionItem{
				{Summary: "a", Cursor: 40, Timestamp: time.Now()},
				{Summary: "b", Cursor: 60, Timestamp: time.Now()},
			},
			targetCursor: 55,
			expectFound:  true,
			expectCursor: 60, // Closest to 55
		},
		{
			name: "target_below_all",
			items: []AttentionItem{
				{Summary: "a", Cursor: 100, Timestamp: time.Now()},
				{Summary: "b", Cursor: 200, Timestamp: time.Now()},
			},
			targetCursor: 10,
			expectFound:  true,
			expectCursor: 100, // Nearest to 10
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			panel.SetData(tc.items, true)
			found := panel.SelectNearestCursor(tc.targetCursor)

			if found != tc.expectFound {
				t.Errorf("SelectNearestCursor(%d) = %v, want %v", tc.targetCursor, found, tc.expectFound)
			}

			if found {
				selected := panel.SelectedItem()
				if selected == nil {
					t.Fatal("expected selected item after successful SelectNearestCursor")
				}
				if selected.Cursor != tc.expectCursor {
					t.Errorf("selected cursor = %d, want %d", selected.Cursor, tc.expectCursor)
				}
			}
		})
	}
}

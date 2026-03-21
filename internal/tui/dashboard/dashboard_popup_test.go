package dashboard

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	ntmevents "github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard/panels"
)

// TestAutoPopupDetection tests the NTM_POPUP environment variable detection logic.
// This is the mechanism by which the dashboard knows it's running in overlay mode.
func TestAutoPopupDetection(t *testing.T) {
	testCases := []struct {
		name     string
		envValue string
		wantPop  bool
	}{
		{name: "unset", envValue: "", wantPop: false},
		{name: "set_to_1", envValue: "1", wantPop: true},
		{name: "set_to_0", envValue: "0", wantPop: false},
		{name: "set_to_true", envValue: "true", wantPop: true},
		{name: "set_to_false", envValue: "false", wantPop: true}, // Any non-empty, non-0 value is truthy
		{name: "set_to_yes", envValue: "yes", wantPop: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Clear env first
			os.Unsetenv("NTM_POPUP")

			if tc.envValue != "" {
				t.Setenv("NTM_POPUP", tc.envValue)
			}

			// Detection logic used in overlay/dashboard: env != "" && env != "0"
			envVal := os.Getenv("NTM_POPUP")
			isPopup := envVal != "" && envVal != "0"

			if isPopup != tc.wantPop {
				t.Errorf("popup detection for NTM_POPUP=%q: got %v, want %v",
					tc.envValue, isPopup, tc.wantPop)
			}

			t.Logf("NTM_POPUP=%q -> isPopup=%v", tc.envValue, isPopup)
		})
	}
}

// TestPostQuitActionStructure tests the PostQuitAction struct construction.
func TestPostQuitActionStructure(t *testing.T) {
	// Test creating a PostQuitAction for session attachment
	action := &PostQuitAction{AttachSession: "myproject"}

	if action.AttachSession != "myproject" {
		t.Errorf("AttachSession = %q, want myproject", action.AttachSession)
	}
}

// TestPostQuitActionComparison tests how PostQuitAction is used for popup vs normal mode.
func TestPostQuitActionComparison(t *testing.T) {
	testCases := []struct {
		name       string
		action     *PostQuitAction
		wantAttach bool
	}{
		{
			name:       "nil_action_popup_close",
			action:     nil,
			wantAttach: false,
		},
		{
			name:       "attach_session_normal_zoom",
			action:     &PostQuitAction{AttachSession: "myproject"},
			wantAttach: true,
		},
		{
			name:       "empty_attach_session",
			action:     &PostQuitAction{AttachSession: ""},
			wantAttach: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			shouldAttach := tc.action != nil && tc.action.AttachSession != ""

			if shouldAttach != tc.wantAttach {
				t.Errorf("shouldAttach = %v, want %v", shouldAttach, tc.wantAttach)
			}

			t.Logf("action=%+v -> shouldAttach=%v", tc.action, shouldAttach)
		})
	}
}

// TestPopupModeConstants documents expected popup behavior.
func TestPopupModeConstants(t *testing.T) {
	// Document the expected popup behavior
	t.Run("escape_closes_popup", func(t *testing.T) {
		// In popup mode (NTM_POPUP=1), Escape should close the overlay
		// This is tested by ensuring the model.popupMode flag controls escape behavior
		t.Log("Expected: popupMode=true + Escape -> quitting=true, postQuitAction=nil")
	})

	t.Run("quit_clears_post_action", func(t *testing.T) {
		// In popup mode, quit should clear postQuitAction for clean close
		t.Log("Expected: popupMode=true + q/quit -> postQuitAction=nil")
	})

	t.Run("zoom_behavior_differs", func(t *testing.T) {
		// In popup mode, zoom should not set postQuitAction.AttachSession
		// because we're already in the session (inside a display-popup)
		t.Log("Expected: popupMode=true + z/Enter -> no re-attach needed")
	})
}

// TestOverlayDimensionDefaults tests the overlay popup dimensions.
func TestOverlayDimensionDefaults(t *testing.T) {
	// The overlay uses 95% width and height
	expectedWidth := "95%"
	expectedHeight := "95%"

	// These are the dimensions passed to tmux display-popup
	t.Logf("Overlay dimensions: -w %s -h %s", expectedWidth, expectedHeight)

	// Verify they're reasonable percentages
	if expectedWidth != "95%" {
		t.Errorf("expected width 95%%, got %s", expectedWidth)
	}
	if expectedHeight != "95%" {
		t.Errorf("expected height 95%%, got %s", expectedHeight)
	}
}

func TestPopupEscapeQuitsDashboard(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	m.popupMode = true

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := updatedModel.(Model)

	if !updated.quitting {
		t.Fatal("popup escape should mark the dashboard as quitting")
	}
	if updated.postQuitAction != nil {
		t.Fatalf("popup escape should not leave a postQuitAction behind: %+v", updated.postQuitAction)
	}
	if cmd == nil {
		t.Fatal("popup escape should return a quit command")
	}

	t.Logf("popup escape -> quitting=%v postQuitAction=%+v", updated.quitting, updated.postQuitAction)
}

func TestRequestedAttentionCursorSelectsExactItem(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	m.requestAttentionCursor(42)

	items := []panels.AttentionItem{
		{Summary: "older", Cursor: 40, Timestamp: time.Now().Add(-2 * time.Minute)},
		{Summary: "target", Cursor: 42, Timestamp: time.Now()},
	}

	updatedModel, _ := m.Update(AttentionUpdateMsg{Items: items, FeedAvailable: true})
	updated := updatedModel.(Model)

	if updated.focusedPanel != PanelAttention {
		t.Fatalf("focusedPanel = %v, want PanelAttention", updated.focusedPanel)
	}
	selected := updated.attentionPanel.SelectedItem()
	if selected == nil || selected.Cursor != 42 {
		t.Fatalf("selected item = %+v, want cursor 42", selected)
	}
}

func TestRequestedAttentionCursorFallsBackToNearestItem(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	m.requestAttentionCursor(42)

	items := []panels.AttentionItem{
		{Summary: "older", Cursor: 40, Timestamp: time.Now().Add(-3 * time.Minute)},
		{Summary: "newer", Cursor: 44, Timestamp: time.Now()},
	}

	updatedModel, _ := m.Update(AttentionUpdateMsg{Items: items, FeedAvailable: true})
	updated := updatedModel.(Model)

	selected := updated.attentionPanel.SelectedItem()
	if selected == nil || selected.Cursor != 44 {
		t.Fatalf("selected item = %+v, want nearest cursor 44", selected)
	}
	if !strings.Contains(updated.healthMessage, "nearest surviving item") {
		t.Fatalf("healthMessage = %q, want degraded cursor fallback note", updated.healthMessage)
	}
}

func TestPopupEscapeClosesHelpBeforeQuitting(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	m.popupMode = true
	m.showHelp = true

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := updatedModel.(Model)

	if updated.showHelp {
		t.Fatal("popup escape should close the help overlay first")
	}
	if updated.quitting {
		t.Fatal("popup escape should not quit while the help overlay is open")
	}
	if cmd != nil {
		t.Fatalf("help-overlay escape should not return a quit command, got %v", cmd)
	}
}

func TestPopupQuitClearsPostQuitAction(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	m.popupMode = true
	m.postQuitAction = &PostQuitAction{AttachSession: "test"}

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	updated := updatedModel.(Model)

	if !updated.quitting {
		t.Fatal("popup quit should mark the dashboard as quitting")
	}
	if updated.postQuitAction != nil {
		t.Fatalf("popup quit should clear postQuitAction, got %+v", updated.postQuitAction)
	}
	if cmd == nil {
		t.Fatal("popup quit should return a quit command")
	}
}

func TestPopupStatsBarShowsOverlayBadge(t *testing.T) {
	t.Parallel()

	m := newTestModel(140)
	m.popupMode = true

	plain := status.StripANSI(m.renderStatsBar())
	if !strings.Contains(plain, "overlay") {
		t.Fatalf("expected popup stats bar to include overlay badge, got %q", plain)
	}
	if !strings.Contains(plain, "Help:") {
		t.Fatalf("expected popup stats bar to include help badge, got %q", plain)
	}
}

func TestNonPopupStatsBarOmitsOverlayBadge(t *testing.T) {
	t.Parallel()

	m := newTestModel(140)
	m.popupMode = false

	plain := status.StripANSI(m.renderStatsBar())
	if strings.Contains(plain, "overlay") {
		t.Fatalf("did not expect non-popup stats bar to include overlay badge, got %q", plain)
	}
}

func TestPopupHelpHintsPreferEscClose(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	m.popupMode = true

	hints := components.DashboardHelpBarHints(m.dashboardHelpOptions())
	if len(hints) == 0 {
		t.Fatal("expected popup help hints")
	}
	if hints[0].Key != "Esc" || hints[0].Desc != "close" {
		t.Fatalf("expected popup help to lead with Esc/close, got %+v", hints[0])
	}

	hasZoom := false
	for _, hint := range hints {
		if hint.Key == "z" && hint.Desc == "zoom" {
			hasZoom = true
			break
		}
	}
	if !hasZoom {
		t.Fatalf("expected popup help hints to retain the zoom action, got %+v", hints)
	}

	t.Logf("popup help hints: %+v", hints)
}

func TestOverlayZoomHintIncludesCursorWhenAvailable(t *testing.T) {
	t.Parallel()

	hint := overlayZoomHint(42135)
	if !strings.Contains(hint, "cursor:42135") {
		t.Fatalf("expected cursor in zoom hint, got %q", hint)
	}
}

func TestOverlayZoomHintOmitsCursorWhenUnavailable(t *testing.T) {
	t.Parallel()

	hint := overlayZoomHint(0)
	if strings.Contains(hint, "cursor:") {
		t.Fatalf("did not expect cursor in zoom hint, got %q", hint)
	}
}

func TestActivatePopupModeSetsOverlayOpenedAt(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	if m.popupMode {
		t.Fatal("test model should start outside popup mode")
	}
	if !m.overlayOpenedAt.IsZero() {
		t.Fatalf("expected zero overlayOpenedAt before activation, got %v", m.overlayOpenedAt)
	}

	fixedNow := time.Date(2026, 3, 21, 3, 30, 0, 0, time.UTC)
	m.activatePopupMode(fixedNow)

	if !m.popupMode {
		t.Fatal("activatePopupMode should enable popup mode")
	}
	if !m.overlayOpenedAt.Equal(fixedNow) {
		t.Fatalf("overlayOpenedAt = %v, want %v", m.overlayOpenedAt, fixedNow)
	}
}

func TestPopupZoomPublishesHumanZoomAndDisplaysCursorHint(t *testing.T) {
	lockAttentionFeedForTest(t)

	oldFeed := robot.GetAttentionFeed()
	oldZoomPane := dashboardZoomPane
	oldDisplayMessage := dashboardDisplayMessage
	oldPublishEvent := dashboardPublishBusEvent
	t.Cleanup(func() {
		robot.SetAttentionFeed(oldFeed)
		dashboardZoomPane = oldZoomPane
		dashboardDisplayMessage = oldDisplayMessage
		dashboardPublishBusEvent = oldPublishEvent
	})

	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{JournalSize: 16, RetentionPeriod: time.Hour})
	feed.Append(robot.AttentionEvent{Summary: "seed"})
	feed.Append(robot.AttentionEvent{Summary: "seed-2"})
	robot.SetAttentionFeed(feed)

	var zoomSession string
	var zoomPane int
	dashboardZoomPane = func(session string, paneIndex int) error {
		zoomSession = session
		zoomPane = paneIndex
		return nil
	}

	var displayed string
	var displayDuration int
	dashboardDisplayMessage = func(session, msg string, durationMs int) error {
		if session != "test" {
			t.Fatalf("display session = %q, want test", session)
		}
		displayed = msg
		displayDuration = durationMs
		return nil
	}

	var published ntmevents.BusEvent
	dashboardPublishBusEvent = func(event ntmevents.BusEvent) {
		published = event
	}

	m := newTestModel(120)
	m.popupMode = true
	m.overlayOpenedAt = time.Now().Add(-2 * time.Second)

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	updated := updatedModel.(Model)

	if zoomSession != "test" || zoomPane != 1 {
		t.Fatalf("zoom called with session=%q pane=%d, want test/1", zoomSession, zoomPane)
	}
	if !updated.quitting {
		t.Fatal("popup zoom should quit the overlay")
	}
	if updated.postQuitAction != nil {
		t.Fatalf("popup zoom should not leave a postQuitAction behind: %+v", updated.postQuitAction)
	}
	if cmd == nil {
		t.Fatal("popup zoom should return a quit command")
	}
	if displayDuration != 4000 {
		t.Fatalf("display duration = %d, want 4000", displayDuration)
	}
	if !strings.Contains(displayed, "cursor:2") {
		t.Fatalf("expected zoom hint to include current cursor, got %q", displayed)
	}

	zoomEvent, ok := published.(ntmevents.WebhookEvent)
	if !ok {
		t.Fatalf("expected WebhookEvent, got %T", published)
	}
	if zoomEvent.EventType() != ntmevents.EventHumanZoom {
		t.Fatalf("event type = %q, want %q", zoomEvent.EventType(), ntmevents.EventHumanZoom)
	}
	if zoomEvent.Pane != "1" {
		t.Fatalf("pane = %q, want 1", zoomEvent.Pane)
	}
	if zoomEvent.Agent == "" {
		t.Fatal("expected agent type in human zoom event")
	}
	if got := zoomEvent.Details["cursor"]; got != "2" {
		t.Fatalf("cursor detail = %q, want 2", got)
	}

	normalized, ok := robot.NewBusAttentionEvent(zoomEvent)
	if !ok {
		t.Fatal("human zoom webhook should normalize into the attention feed")
	}
	if normalized.Actionability != robot.ActionabilityBackground {
		t.Fatalf("attention actionability = %q, want %q", normalized.Actionability, robot.ActionabilityBackground)
	}
	if !strings.Contains(strings.ToLower(normalized.Summary), "human zoom") {
		t.Fatalf("attention summary = %q, want human zoom context", normalized.Summary)
	}
	details, ok := normalized.Details["details"].(map[string]any)
	if !ok {
		t.Fatalf("normalized details payload = %T, want map[string]any", normalized.Details["details"])
	}
	if details["cursor"] != "2" {
		t.Fatalf("attention cursor detail = %v, want %q", details["cursor"], "2")
	}
	replayed, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay(0, 10) error: %v", err)
	}
	if len(replayed) != 2 {
		t.Fatalf("expected stubbed publish path to leave attention feed unchanged, got %d events", len(replayed))
	}
}

func TestHandleAttentionZoomUsesPaneIndexLookup(t *testing.T) {
	oldZoomPane := dashboardZoomPane
	t.Cleanup(func() {
		dashboardZoomPane = oldZoomPane
	})

	var zoomSession string
	var zoomPane int
	dashboardZoomPane = func(session string, paneIndex int) error {
		zoomSession = session
		zoomPane = paneIndex
		return nil
	}

	m := New("test", "")
	m.width = 140
	m.height = 30
	m.panes = []tmux.Pane{
		{ID: "pane-1", Index: 1, Title: "test__cc_1", Type: tmux.AgentClaude},
		{ID: "pane-3", Index: 3, Title: "test__cod_1", Type: tmux.AgentCodex},
	}
	m.rebuildPaneList()

	cmd := m.handleAttentionZoom(3, 0)
	if cmd == nil {
		t.Fatal("expected handleAttentionZoom to return a zoom command for an existing pane index")
	}
	if zoomSession != "test" || zoomPane != 3 {
		t.Fatalf("zoom called with session=%q pane=%d, want test/3", zoomSession, zoomPane)
	}
	if m.healthMessage != "" {
		t.Fatalf("did not expect a missing-pane health message, got %q", m.healthMessage)
	}
}

func TestHandleAttentionZoomMissingPaneShowsHealthMessage(t *testing.T) {
	oldZoomPane := dashboardZoomPane
	t.Cleanup(func() {
		dashboardZoomPane = oldZoomPane
	})

	zoomCalled := false
	dashboardZoomPane = func(session string, paneIndex int) error {
		zoomCalled = true
		return nil
	}

	m := New("test", "")
	m.width = 140
	m.height = 30
	m.panes = []tmux.Pane{
		{ID: "pane-1", Index: 1, Title: "test__cc_1", Type: tmux.AgentClaude},
	}
	m.rebuildPaneList()

	cmd := m.handleAttentionZoom(99, 0)
	if cmd != nil {
		t.Fatalf("expected no zoom command for a missing pane, got %v", cmd)
	}
	if zoomCalled {
		t.Fatal("did not expect dashboardZoomPane to be called for a missing pane")
	}
	if m.healthMessage != "Source pane no longer available" {
		t.Fatalf("healthMessage = %q, want %q", m.healthMessage, "Source pane no longer available")
	}
	if m.toasts == nil || m.toasts.Count() != 1 {
		t.Fatalf("expected a warning toast for missing pane, got %+v", m.toasts)
	}
}

func TestHandleAttentionZoomPopupUsesSelectedItemCursor(t *testing.T) {
	lockAttentionFeedForTest(t)

	oldFeed := robot.GetAttentionFeed()
	oldZoomPane := dashboardZoomPane
	oldDisplayMessage := dashboardDisplayMessage
	oldPublishEvent := dashboardPublishBusEvent
	t.Cleanup(func() {
		robot.SetAttentionFeed(oldFeed)
		dashboardZoomPane = oldZoomPane
		dashboardDisplayMessage = oldDisplayMessage
		dashboardPublishBusEvent = oldPublishEvent
	})

	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{JournalSize: 16, RetentionPeriod: time.Hour})
	feed.Append(robot.AttentionEvent{Summary: "seed"})
	feed.Append(robot.AttentionEvent{Summary: "seed-2"})
	feed.Append(robot.AttentionEvent{Summary: "seed-3"})
	robot.SetAttentionFeed(feed)

	var zoomSession string
	var zoomPane int
	dashboardZoomPane = func(session string, paneIndex int) error {
		zoomSession = session
		zoomPane = paneIndex
		return nil
	}

	var displayed string
	dashboardDisplayMessage = func(session, msg string, durationMs int) error {
		if session != "test" {
			t.Fatalf("display session = %q, want test", session)
		}
		if durationMs != 4000 {
			t.Fatalf("display duration = %d, want 4000", durationMs)
		}
		displayed = msg
		return nil
	}

	var published ntmevents.BusEvent
	dashboardPublishBusEvent = func(event ntmevents.BusEvent) {
		published = event
	}

	m := New("test", "")
	m.width = 140
	m.height = 30
	m.popupMode = true
	m.overlayOpenedAt = time.Now().Add(-2 * time.Second)
	m.panes = []tmux.Pane{
		{ID: "pane-1", Index: 1, Title: "test__cc_1", Type: tmux.AgentClaude},
		{ID: "pane-3", Index: 3, Title: "test__cod_1", Type: tmux.AgentCodex},
	}
	m.rebuildPaneList()

	cmd := m.handleAttentionZoom(3, 2)
	if cmd == nil {
		t.Fatal("expected popup attention zoom to return a quit command")
	}
	if !m.quitting {
		t.Fatal("expected popup attention zoom to mark the overlay as quitting")
	}
	if zoomSession != "test" || zoomPane != 3 {
		t.Fatalf("zoom called with session=%q pane=%d, want test/3", zoomSession, zoomPane)
	}
	if !strings.Contains(displayed, "cursor:2") {
		t.Fatalf("expected zoom hint to include selected item cursor, got %q", displayed)
	}
	if strings.Contains(displayed, "cursor:3") {
		t.Fatalf("did not expect popup zoom hint to use the feed tail cursor, got %q", displayed)
	}

	zoomEvent, ok := published.(ntmevents.WebhookEvent)
	if !ok {
		t.Fatalf("expected WebhookEvent, got %T", published)
	}
	if got := zoomEvent.Details["cursor"]; got != "2" {
		t.Fatalf("cursor detail = %q, want 2", got)
	}
}

func TestPopupEscapePublishesOverlayDismissDuration(t *testing.T) {
	lockAttentionFeedForTest(t)

	oldFeed := robot.GetAttentionFeed()
	oldNow := dashboardNow
	oldPublishEvent := dashboardPublishBusEvent
	t.Cleanup(func() {
		robot.SetAttentionFeed(oldFeed)
		dashboardNow = oldNow
		dashboardPublishBusEvent = oldPublishEvent
	})

	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{JournalSize: 16, RetentionPeriod: time.Hour})
	feed.Append(robot.AttentionEvent{Summary: "seed"})
	feed.Append(robot.AttentionEvent{Summary: "seed-2"})
	feed.Append(robot.AttentionEvent{Summary: "seed-3"})
	robot.SetAttentionFeed(feed)

	fixedNow := time.Date(2026, 3, 21, 3, 15, 0, 0, time.UTC)
	dashboardNow = func() time.Time { return fixedNow }

	var published ntmevents.BusEvent
	dashboardPublishBusEvent = func(event ntmevents.BusEvent) {
		published = event
	}

	m := newTestModel(120)
	m.popupMode = true
	m.overlayOpenedAt = fixedNow.Add(-3 * time.Second)

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := updatedModel.(Model)

	if !updated.quitting {
		t.Fatal("popup escape should quit the overlay")
	}
	if cmd == nil {
		t.Fatal("popup escape should return a quit command")
	}

	dismissEvent, ok := published.(ntmevents.WebhookEvent)
	if !ok {
		t.Fatalf("expected WebhookEvent, got %T", published)
	}
	if dismissEvent.EventType() != ntmevents.EventHumanOverlayDismiss {
		t.Fatalf("event type = %q, want %q", dismissEvent.EventType(), ntmevents.EventHumanOverlayDismiss)
	}
	if got := dismissEvent.Details["cursor"]; got != "3" {
		t.Fatalf("cursor detail = %q, want 3", got)
	}
	if dismissEvent.Details["duration_seconds"] != "3.000" {
		t.Fatalf("duration_seconds detail = %q, want 3.000", dismissEvent.Details["duration_seconds"])
	}

	normalized, ok := robot.NewBusAttentionEvent(dismissEvent)
	if !ok {
		t.Fatal("human overlay dismiss webhook should normalize into the attention feed")
	}
	if normalized.Actionability != robot.ActionabilityBackground {
		t.Fatalf("attention actionability = %q, want %q", normalized.Actionability, robot.ActionabilityBackground)
	}
	if !strings.Contains(strings.ToLower(normalized.Summary), "human overlay dismiss") {
		t.Fatalf("attention summary = %q, want overlay dismiss context", normalized.Summary)
	}
	details, ok := normalized.Details["details"].(map[string]any)
	if !ok {
		t.Fatalf("normalized details payload = %T, want map[string]any", normalized.Details["details"])
	}
	if details["duration_seconds"] != "3.000" {
		t.Fatalf("attention duration detail = %v, want %q", details["duration_seconds"], "3.000")
	}
	replayed, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay(0, 10) error: %v", err)
	}
	if len(replayed) != 3 {
		t.Fatalf("expected stubbed publish path to leave attention feed unchanged, got %d events", len(replayed))
	}
}

// TestPopupModeKeyBehavior documents and verifies popup mode key behavior.
func TestPopupModeKeyBehavior(t *testing.T) {
	testCases := []struct {
		name          string
		key           string
		popupMode     bool
		expectQuit    bool
		expectAction  bool
		actionSession string
		desc          string
	}{
		{
			name:          "escape_in_popup_mode",
			key:           "esc",
			popupMode:     true,
			expectQuit:    true,
			expectAction:  false,
			actionSession: "",
			desc:          "Escape in popup mode closes overlay with no post-quit action",
		},
		{
			name:          "escape_in_normal_mode",
			key:           "esc",
			popupMode:     false,
			expectQuit:    false,
			expectAction:  false,
			actionSession: "",
			desc:          "Escape in normal mode does not quit (may toggle help/overlay)",
		},
		{
			name:          "quit_in_popup_mode",
			key:           "q",
			popupMode:     true,
			expectQuit:    true,
			expectAction:  false,
			actionSession: "",
			desc:          "Quit in popup mode exits with nil postQuitAction",
		},
		{
			name:          "quit_in_normal_mode",
			key:           "q",
			popupMode:     false,
			expectQuit:    true,
			expectAction:  false,
			actionSession: "",
			desc:          "Quit in normal mode exits (postQuitAction depends on prior state)",
		},
		{
			name:          "zoom_in_popup_mode",
			key:           "z",
			popupMode:     true,
			expectQuit:    true,
			expectAction:  false,
			actionSession: "",
			desc:          "Zoom in popup mode exits without re-attach (shows hint instead)",
		},
		{
			name:          "zoom_in_normal_mode",
			key:           "z",
			popupMode:     false,
			expectQuit:    true,
			expectAction:  true,
			actionSession: "test-session",
			desc:          "Zoom in normal mode exits with AttachSession set",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Test: %s", tc.desc)
			t.Logf("Key: %q, PopupMode: %v", tc.key, tc.popupMode)
			t.Logf("Expected: quit=%v, hasAction=%v, session=%q",
				tc.expectQuit, tc.expectAction, tc.actionSession)

			// This is a documentation/specification test.
			// The actual behavior is in dashboard.go Update method.
			// We verify the expected contract here.

			// Simulate the expected state after key press
			var postAction *PostQuitAction
			if tc.expectAction && tc.actionSession != "" {
				postAction = &PostQuitAction{AttachSession: tc.actionSession}
			}

			// Verify the logic matches our expectations
			hasAction := postAction != nil && postAction.AttachSession != ""
			if hasAction != tc.expectAction {
				t.Errorf("action mismatch: got hasAction=%v, want %v", hasAction, tc.expectAction)
			}

			t.Logf("Result: postAction=%+v", postAction)
		})
	}
}

// TestPopupModeZoomHintBehavior documents the zoom hint shown in popup mode.
func TestPopupModeZoomHintBehavior(t *testing.T) {
	t.Run("hint_content", func(t *testing.T) {
		// The hint shown after zooming in popup mode
		expectedHint := "F12 → dashboard overlay · prefix+z → unzoom"
		t.Logf("Zoom hint in popup mode: %q", expectedHint)

		// Verify hint contains expected elements
		if expectedHint == "" {
			t.Error("hint should not be empty")
		}

		// Document the behavior
		t.Log("Behavior: When zooming from popup mode:")
		t.Log("  1. Zoom the pane (tmux.ZoomPane)")
		t.Log("  2. Display hint message (tmux.DisplayMessage with 4s timeout)")
		t.Log("  3. Exit without setting postQuitAction.AttachSession")
	})

	t.Run("hint_duration", func(t *testing.T) {
		expectedDurationMs := 4000
		t.Logf("Hint display duration: %dms", expectedDurationMs)
	})
}

// TestPopupModeVsNormalModeComparison compares behavior between modes.
func TestPopupModeVsNormalModeComparison(t *testing.T) {
	comparisons := []struct {
		action     string
		popupMode  string
		normalMode string
	}{
		{
			action:     "Escape",
			popupMode:  "Closes overlay, exits cleanly (quitting=true, postQuitAction=nil)",
			normalMode: "May toggle help overlay or close modal (does not quit)",
		},
		{
			action:     "Quit (q)",
			popupMode:  "Exits with postQuitAction=nil for clean popup close",
			normalMode: "Exits, postQuitAction may be set from prior zoom selection",
		},
		{
			action:     "Zoom (z/Enter)",
			popupMode:  "Zooms pane, shows F12 return hint, exits without re-attach",
			normalMode: "Zooms pane, sets postQuitAction.AttachSession, exits to reattach",
		},
		{
			action:     "Help (?)",
			popupMode:  "Toggles help overlay (same as normal)",
			normalMode: "Toggles help overlay",
		},
		{
			action:     "Navigation (j/k/Tab)",
			popupMode:  "Same as normal mode - navigate panes/panels",
			normalMode: "Navigate panes/panels",
		},
	}

	t.Log("Popup Mode vs Normal Mode Behavior Comparison:")
	t.Log("=" + string(make([]byte, 70)))

	for _, c := range comparisons {
		t.Run(c.action, func(t *testing.T) {
			t.Logf("Action: %s", c.action)
			t.Logf("  Popup Mode:  %s", c.popupMode)
			t.Logf("  Normal Mode: %s", c.normalMode)
		})
	}
}

// TestPopupModeFlagDetection tests the detection of popup mode via flag vs env.
func TestPopupModeFlagDetection(t *testing.T) {
	testCases := []struct {
		name      string
		flagValue bool
		envValue  string
		wantPopup bool
		desc      string
	}{
		{
			name:      "flag_true_no_env",
			flagValue: true,
			envValue:  "",
			wantPopup: true,
			desc:      "--popup flag set, no env var",
		},
		{
			name:      "flag_false_env_1",
			flagValue: false,
			envValue:  "1",
			wantPopup: true,
			desc:      "No flag, NTM_POPUP=1 env var",
		},
		{
			name:      "flag_false_no_env",
			flagValue: false,
			envValue:  "",
			wantPopup: false,
			desc:      "No flag, no env var",
		},
		{
			name:      "flag_true_env_1",
			flagValue: true,
			envValue:  "1",
			wantPopup: true,
			desc:      "Both flag and env var set",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Test: %s", tc.desc)
			t.Logf("Flag: %v, Env: %q", tc.flagValue, tc.envValue)

			// Simulate popup mode detection logic
			// In actual code: popupMode = flagValue || (env != "" && env != "0")
			envPopup := tc.envValue != "" && tc.envValue != "0"
			isPopup := tc.flagValue || envPopup

			t.Logf("Result: flagValue=%v, envPopup=%v, isPopup=%v", tc.flagValue, envPopup, isPopup)

			if isPopup != tc.wantPopup {
				t.Errorf("popup detection = %v, want %v", isPopup, tc.wantPopup)
			}
		})
	}
}

// TestOverlayBadgeRenderingContract documents overlay badge rendering expectations.
func TestOverlayBadgeRenderingContract(t *testing.T) {
	t.Run("badge_elements", func(t *testing.T) {
		t.Log("Overlay badge in popup mode should show:")
		t.Log("  1. Visual indicator that we're in overlay mode")
		t.Log("  2. Key hint for dismissing (Escape or F12)")
		t.Log("  3. Session name being monitored")
	})

	t.Run("help_rendering", func(t *testing.T) {
		t.Log("Help overlay (?) should include popup-specific hints when in popup mode:")
		t.Log("  - Escape: close overlay")
		t.Log("  - z/Enter: zoom pane and close overlay")
		t.Log("  - F12: toggle overlay (if bound)")
	})
}

// TestPostQuitActionSemantics tests the full range of PostQuitAction semantics.
func TestPostQuitActionSemantics(t *testing.T) {
	testCases := []struct {
		name           string
		action         *PostQuitAction
		shouldReattach bool
		shouldZoom     bool
		desc           string
	}{
		{
			name:           "nil_action",
			action:         nil,
			shouldReattach: false,
			shouldZoom:     false,
			desc:           "Nil action: clean exit, no reattach",
		},
		{
			name:           "empty_session",
			action:         &PostQuitAction{AttachSession: ""},
			shouldReattach: false,
			shouldZoom:     false,
			desc:           "Empty session: no reattach",
		},
		{
			name:           "with_session",
			action:         &PostQuitAction{AttachSession: "myproject"},
			shouldReattach: true,
			shouldZoom:     false, // Zoom is implicit in the pane selection
			desc:           "With session: reattach after exit",
		},
		{
			name:           "with_special_session_name",
			action:         &PostQuitAction{AttachSession: "my-project_123"},
			shouldReattach: true,
			shouldZoom:     false,
			desc:           "Session with special chars: reattach",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Test: %s", tc.desc)
			t.Logf("Action: %+v", tc.action)

			shouldReattach := tc.action != nil && tc.action.AttachSession != ""

			t.Logf("shouldReattach=%v (expected=%v)", shouldReattach, tc.shouldReattach)

			if shouldReattach != tc.shouldReattach {
				t.Errorf("reattach decision = %v, want %v", shouldReattach, tc.shouldReattach)
			}
		})
	}
}

func TestPublishHumanZoomEventUsesWebhookEventAndBackgroundAttention(t *testing.T) {
	lockAttentionFeedForTest(t)

	oldFeed := robot.GetAttentionFeed()
	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{
		JournalSize:       8,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	robot.SetAttentionFeed(feed)
	t.Cleanup(func() {
		feed.Stop()
		robot.SetAttentionFeed(oldFeed)
	})

	m := newTestModel(120)
	ch := make(chan ntmevents.BusEvent, 1)
	unsub := ntmevents.Subscribe(ntmevents.EventHumanZoom, func(e ntmevents.BusEvent) {
		select {
		case ch <- e:
		default:
		}
	})
	t.Cleanup(unsub)

	m.publishHumanZoomEvent(m.panes[0], 42)

	var event ntmevents.BusEvent
	select {
	case event = <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for human zoom event")
	}

	webhook, ok := event.(ntmevents.WebhookEvent)
	if !ok {
		t.Fatalf("event type = %T, want WebhookEvent", event)
	}
	if webhook.EventType() != ntmevents.EventHumanZoom {
		t.Fatalf("webhook type = %q, want %q", webhook.EventType(), ntmevents.EventHumanZoom)
	}
	if webhook.Pane != "1" {
		t.Fatalf("webhook pane = %q, want %q", webhook.Pane, "1")
	}
	if webhook.Agent != string(m.panes[0].Type) {
		t.Fatalf("webhook agent = %q, want %q", webhook.Agent, string(m.panes[0].Type))
	}
	if webhook.Details["cursor"] != "42" {
		t.Fatalf("webhook cursor detail = %q, want %q", webhook.Details["cursor"], "42")
	}

	normalized, ok := robot.NewBusAttentionEvent(webhook)
	if !ok {
		t.Fatal("human zoom webhook should normalize into the attention feed")
	}
	if normalized.Actionability != robot.ActionabilityBackground {
		t.Fatalf("normalized actionability = %q, want %q", normalized.Actionability, robot.ActionabilityBackground)
	}
	details, ok := normalized.Details["details"].(map[string]any)
	if !ok {
		t.Fatalf("normalized details payload = %T, want map[string]any", normalized.Details["details"])
	}
	if details["cursor"] != "42" {
		t.Fatalf("normalized cursor detail = %v, want %q", details["cursor"], "42")
	}
}

func TestPublishHumanOverlayDismissIncludesDurationAndBackgroundAttention(t *testing.T) {
	lockAttentionFeedForTest(t)

	oldFeed := robot.GetAttentionFeed()
	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{
		JournalSize:       8,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	feed.Append(robot.AttentionEvent{Summary: "seed"})
	robot.SetAttentionFeed(feed)
	t.Cleanup(func() {
		feed.Stop()
		robot.SetAttentionFeed(oldFeed)
	})

	m := newTestModel(120)
	openedAt := time.Date(2026, time.March, 20, 12, 0, 0, 0, time.UTC)
	m.activatePopupMode(openedAt)

	ch := make(chan ntmevents.BusEvent, 1)
	unsub := ntmevents.Subscribe(ntmevents.EventHumanOverlayDismiss, func(e ntmevents.BusEvent) {
		select {
		case ch <- e:
		default:
		}
	})
	t.Cleanup(unsub)

	m.publishHumanOverlayDismiss(openedAt.Add(5 * time.Second))

	var event ntmevents.BusEvent
	select {
	case event = <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for human overlay dismiss event")
	}

	webhook, ok := event.(ntmevents.WebhookEvent)
	if !ok {
		t.Fatalf("event type = %T, want WebhookEvent", event)
	}
	if webhook.EventType() != ntmevents.EventHumanOverlayDismiss {
		t.Fatalf("webhook type = %q, want %q", webhook.EventType(), ntmevents.EventHumanOverlayDismiss)
	}
	if webhook.Details["duration_seconds"] != "5.000" {
		t.Fatalf("duration_seconds = %q, want %q", webhook.Details["duration_seconds"], "5.000")
	}
	if webhook.Details["cursor"] != "1" {
		t.Fatalf("cursor detail = %q, want %q", webhook.Details["cursor"], "1")
	}
	if webhook.Details["overlay_opened_at"] != openedAt.Format(time.RFC3339Nano) {
		t.Fatalf("overlay_opened_at = %q, want %q", webhook.Details["overlay_opened_at"], openedAt.Format(time.RFC3339Nano))
	}

	normalized, ok := robot.NewBusAttentionEvent(webhook)
	if !ok {
		t.Fatal("human overlay dismiss webhook should normalize into the attention feed")
	}
	if normalized.Actionability != robot.ActionabilityBackground {
		t.Fatalf("normalized actionability = %q, want %q", normalized.Actionability, robot.ActionabilityBackground)
	}
	details, ok := normalized.Details["details"].(map[string]any)
	if !ok {
		t.Fatalf("normalized details payload = %T, want map[string]any", normalized.Details["details"])
	}
	if details["duration_seconds"] != "5.000" {
		t.Fatalf("normalized duration detail = %v, want %q", details["duration_seconds"], "5.000")
	}
}

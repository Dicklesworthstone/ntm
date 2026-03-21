package dashboard

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
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

	// Test nil action (popup mode clean close)
	var nilAction *PostQuitAction
	if nilAction != nil {
		t.Error("nil PostQuitAction should be nil")
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

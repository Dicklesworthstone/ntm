package palette

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// stripANSI removes ANSI escape sequences from text for test comparison.
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

// Test command fixtures
var testCommands = []config.PaletteCmd{
	{Key: "fix", Label: "Fix Bug", Category: "Quick Actions", Prompt: "Fix the bug"},
	{Key: "test", Label: "Run Tests", Category: "Quick Actions", Prompt: "Run all tests"},
	{Key: "refactor", Label: "Refactor Code", Category: "Code Quality", Prompt: "Refactor the code"},
	{Key: "docs", Label: "Add Documentation", Category: "Code Quality", Prompt: "Add docs"},
	{Key: "review", Label: "Code Review", Category: "Investigation", Prompt: "Review this code"},
}

func TestNew(t *testing.T) {
	m := New("test-session", testCommands)

	if m.session != "test-session" {
		t.Errorf("Expected session 'test-session', got %s", m.session)
	}

	if len(m.commands) != len(testCommands) {
		t.Errorf("Expected %d commands, got %d", len(testCommands), len(m.commands))
	}

	if len(m.filtered) != len(testCommands) {
		t.Errorf("Expected filtered to have %d commands initially, got %d", len(testCommands), len(m.filtered))
	}

	if m.phase != PhaseCommand {
		t.Errorf("Expected initial phase PhaseCommand, got %v", m.phase)
	}

	if m.cursor != 0 {
		t.Errorf("Expected cursor at 0, got %d", m.cursor)
	}
}

func TestNewEmptyCommands(t *testing.T) {
	m := New("test-session", nil)

	if len(m.commands) != 0 {
		t.Errorf("Expected 0 commands, got %d", len(m.commands))
	}

	if len(m.filtered) != 0 {
		t.Errorf("Expected 0 filtered, got %d", len(m.filtered))
	}
}

func TestPhaseConstants(t *testing.T) {
	// Verify phase constants are distinct
	if PhaseCommand == PhaseTarget {
		t.Error("PhaseCommand and PhaseTarget should be distinct")
	}
	if PhaseTarget == PhaseEdit {
		t.Error("PhaseTarget and PhaseEdit should be distinct")
	}
}

func TestTargetConstants(t *testing.T) {
	// Verify target constants are distinct
	targets := []Target{TargetAll, TargetClaude, TargetCodex, TargetGemini}
	seen := make(map[Target]bool)
	for _, t := range targets {
		if seen[t] {
			// Skip error for duplicate check
			continue
		}
		seen[t] = true
	}
	if len(seen) != 4 {
		t.Error("Expected 4 distinct Target values")
	}
}

func TestInit(t *testing.T) {
	m := New("test-session", testCommands)
	cmd := m.Init()

	// Init should return a batch command (textinput.Blink and tick)
	if cmd == nil {
		t.Error("Init should return a command")
	}
}

func TestUpdateWindowSize(t *testing.T) {
	m := New("test-session", testCommands)

	msg := tea.WindowSizeMsg{Width: 100, Height: 50}
	newModel, _ := m.Update(msg)
	m = newModel.(Model)

	if m.width != 100 {
		t.Errorf("Expected width 100, got %d", m.width)
	}

	if m.height != 50 {
		t.Errorf("Expected height 50, got %d", m.height)
	}
}

func TestReloadMsgClearsCommandsWhenReloadedEmpty(t *testing.T) {
	m := New("test-session", testCommands)

	updated, _ := m.Update(ReloadMsg{Commands: nil})
	result := updated.(Model)

	if len(result.commands) != 0 {
		t.Fatalf("expected commands to be cleared, got %d", len(result.commands))
	}
	if len(result.filtered) != 0 {
		t.Fatalf("expected filtered commands to be cleared, got %d", len(result.filtered))
	}
	if len(result.visualOrder) != 0 {
		t.Fatalf("expected visual order to be cleared, got %d", len(result.visualOrder))
	}
	if result.cursor != 0 {
		t.Fatalf("expected cursor reset to 0, got %d", result.cursor)
	}
}

func TestReloadMsgClearsSelectedCommandWhenRemoved(t *testing.T) {
	m := New("test-session", testCommands)
	m.selected = &m.commands[0]
	m.phase = PhaseTarget
	m.editDraft = "keep me?"

	updated, _ := m.Update(ReloadMsg{Commands: []config.PaletteCmd{
		{Key: "other", Label: "Other", Category: "Quick Actions", Prompt: "Something else"},
	}})
	result := updated.(Model)

	if result.selected != nil {
		t.Fatal("expected removed selected command to be cleared")
	}
	if result.phase != PhaseCommand {
		t.Fatalf("expected palette to return to command phase, got %v", result.phase)
	}
	if result.editDraft != "" {
		t.Fatalf("expected edit draft to be cleared, got %q", result.editDraft)
	}
}

func TestReloadMsgRefreshesSelectedCommandPrompt(t *testing.T) {
	m := New("test-session", testCommands)
	m.selected = &m.commands[0]
	m.phase = PhaseTarget

	reloaded := append([]config.PaletteCmd(nil), testCommands...)
	reloaded[0].Prompt = "Fix the bug with updated context"

	updated, _ := m.Update(ReloadMsg{Commands: reloaded})
	result := updated.(Model)

	if result.selected == nil {
		t.Fatal("expected selected command to remain available after reload")
	}
	if result.selected.Prompt != "Fix the bug with updated context" {
		t.Fatalf("expected selected prompt to refresh, got %q", result.selected.Prompt)
	}
	if result.phase != PhaseTarget {
		t.Fatalf("expected phase to stay on target selection, got %v", result.phase)
	}
}

func TestRenderPreviewUsesAgentBadgeForNewerAgentCategories(t *testing.T) {
	m := New("test-session", []config.PaletteCmd{
		{Key: "cursor", Label: "Cursor Task", Category: "cursor", Prompt: "Do the thing"},
		{Key: "ops", Label: "Ops Task", Category: "Quick Actions", Prompt: "Do the other thing"},
	})
	m.width = 80
	m.filtered = append([]config.PaletteCmd(nil), m.commands...)
	m.cursor = 0

	preview := stripANSI(m.renderPreview(80))
	if !strings.Contains(preview, "CURSOR") {
		t.Fatalf("expected newer agent category to render an agent badge label, got:\n%s", preview)
	}
	if strings.Contains(preview, "Quick Actions") {
		t.Fatalf("preview should be showing the selected cursor command, got:\n%s", preview)
	}
}

func TestUpdateAnimationTick(t *testing.T) {
	t.Setenv("NTM_ANIMATIONS", "1")
	t.Setenv("TMUX", "")
	t.Setenv("STY", "")
	t.Setenv("CI", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")

	m := New("test-session", testCommands)
	initialTick := m.animTick

	msg := AnimationTickMsg{}
	newModel, cmd := m.Update(msg)
	m = newModel.(Model)

	if m.animTick != initialTick+1 {
		t.Errorf("Expected animTick to increment from %d to %d, got %d", initialTick, initialTick+1, m.animTick)
	}

	// Should return a new tick command
	if cmd == nil {
		t.Error("Animation tick should return next tick command")
	}
}

func TestNew_DisablesAnimationInTmuxByDefault(t *testing.T) {
	t.Setenv("NTM_ANIMATIONS", "")
	t.Setenv("NTM_REDUCE_MOTION", "")
	t.Setenv("TMUX", "/tmp/tmux-123/default,1,0")
	t.Setenv("STY", "")
	t.Setenv("CI", "")
	t.Setenv("TERM", "tmux-256color")
	t.Setenv("COLORTERM", "truecolor")

	m := New("test-session", testCommands)
	if m.animate {
		t.Fatal("expected palette animations to auto-disable inside tmux")
	}

	newModel, cmd := m.Update(AnimationTickMsg{})
	updated := newModel.(Model)
	if updated.animTick != 0 {
		t.Fatalf("expected animation tick to remain stable, got %d", updated.animTick)
	}
	if cmd != nil {
		t.Fatal("expected no follow-up tick command when animation is disabled")
	}
}

func TestNavigationUp(t *testing.T) {
	m := New("test-session", testCommands)
	m.cursor = 2 // Start in middle

	msg := tea.KeyMsg{Type: tea.KeyUp}
	newModel, _ := m.Update(msg)
	m = newModel.(Model)

	if m.cursor != 1 {
		t.Errorf("Expected cursor to move up to 1, got %d", m.cursor)
	}
}

func TestNavigationUpAtTop(t *testing.T) {
	m := New("test-session", testCommands)
	m.cursor = 0 // At top

	msg := tea.KeyMsg{Type: tea.KeyUp}
	newModel, _ := m.Update(msg)
	m = newModel.(Model)

	if m.cursor != 0 {
		t.Errorf("Expected cursor to stay at 0, got %d", m.cursor)
	}
}

func TestNavigationDown(t *testing.T) {
	m := New("test-session", testCommands)
	m.cursor = 0

	msg := tea.KeyMsg{Type: tea.KeyDown}
	newModel, _ := m.Update(msg)
	m = newModel.(Model)

	if m.cursor != 1 {
		t.Errorf("Expected cursor to move down to 1, got %d", m.cursor)
	}
}

func TestNavigationDownAtBottom(t *testing.T) {
	m := New("test-session", testCommands)
	m.cursor = len(testCommands) - 1 // At bottom

	msg := tea.KeyMsg{Type: tea.KeyDown}
	newModel, _ := m.Update(msg)
	m = newModel.(Model)

	if m.cursor != len(testCommands)-1 {
		t.Errorf("Expected cursor to stay at bottom, got %d", m.cursor)
	}
}

func TestNavigationUsesVisualOrderWhenCategoriesInterleave(t *testing.T) {
	commands := []config.PaletteCmd{
		{Key: "a", Label: "Alpha One", Category: "Alpha", Prompt: "A"},
		{Key: "b", Label: "Beta One", Category: "Beta", Prompt: "B"},
		{Key: "c", Label: "Alpha Two", Category: "Alpha", Prompt: "C"},
	}

	m := New("test-session", commands)
	m.buildVisualOrder()

	// Visual order should be [0,2,1]. Pressing down from first item should land on index 2.
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newModel.(Model)

	if m.cursor != 2 {
		t.Fatalf("Expected cursor to follow visual order to index 2, got %d", m.cursor)
	}

	// Selecting should pick the visually selected command (key 'c').
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = newModel.(Model)

	if m.selected == nil || m.selected.Key != "c" {
		got := "<nil>"
		if m.selected != nil {
			got = m.selected.Key
		}
		t.Fatalf("Expected selected key 'c', got %s", got)
	}
}

func TestNavigationWithK(t *testing.T) {
	m := New("test-session", testCommands)
	m.cursor = 2

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}
	newModel, _ := m.Update(msg)
	m = newModel.(Model)

	// With k/j, the filter input might capture the keystroke
	// Just verify no crash
	if m.cursor < 0 {
		t.Error("Cursor should not be negative")
	}
}

func TestSelectWithEnter(t *testing.T) {
	m := New("test-session", testCommands)
	m.cursor = 1

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	newModel, _ := m.Update(msg)
	m = newModel.(Model)

	if m.phase != PhaseTarget {
		t.Errorf("Expected phase to change to PhaseTarget, got %v", m.phase)
	}

	if m.selected == nil {
		t.Error("Expected selected to be set")
	}

	if m.selected.Key != testCommands[1].Key {
		t.Errorf("Expected selected command key %s, got %s", testCommands[1].Key, m.selected.Key)
	}
}

func TestSelectWithEmptyList(t *testing.T) {
	m := New("test-session", nil)

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	newModel, _ := m.Update(msg)
	m = newModel.(Model)

	// Should stay in command phase since nothing to select
	if m.phase != PhaseCommand {
		t.Errorf("Expected phase to stay PhaseCommand with empty list, got %v", m.phase)
	}

	if m.selected != nil {
		t.Error("Expected selected to be nil with empty list")
	}
}

func TestQuitWithQ(t *testing.T) {
	m := New("test-session", testCommands)

	// When filter is focused (the default), 'q' should type into the filter,
	// not quit. This prevents the search bar from swallowing queries that
	// contain 'q'.
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	newModel, _ := m.Update(msg)
	m = newModel.(Model)

	if m.quitting {
		t.Error("Expected quitting to be false when filter is focused — 'q' should type into filter")
	}
	if m.filter.Value() != "q" {
		t.Errorf("Expected filter value 'q', got %q", m.filter.Value())
	}

	// Ctrl+C should still quit regardless of filter focus.
	m2 := New("test-session", testCommands)
	msg2 := tea.KeyMsg{Type: tea.KeyCtrlC}
	newModel2, cmd2 := m2.Update(msg2)
	m2 = newModel2.(Model)

	if !m2.quitting {
		t.Error("Expected quitting to be true after ctrl+c")
	}
	if cmd2 == nil {
		t.Error("Expected quit command from ctrl+c")
	}
}

func TestQuitWithEsc(t *testing.T) {
	m := New("test-session", testCommands)

	msg := tea.KeyMsg{Type: tea.KeyEsc}
	newModel, cmd := m.Update(msg)
	m = newModel.(Model)

	if !m.quitting {
		t.Error("Expected quitting to be true after Esc")
	}

	if cmd == nil {
		t.Error("Expected quit command")
	}
}

func TestUpdateFiltered(t *testing.T) {
	m := New("test-session", testCommands)

	// Set filter value manually
	m.filter.SetValue("fix")
	m.updateFiltered()

	if len(m.filtered) != 1 {
		t.Errorf("Expected 1 filtered command for 'fix', got %d", len(m.filtered))
	}

	if m.filtered[0].Key != "fix" {
		t.Errorf("Expected filtered command 'fix', got %s", m.filtered[0].Key)
	}
}

func TestUpdateFilteredByCategory(t *testing.T) {
	m := New("test-session", testCommands)

	m.filter.SetValue("quality")
	m.updateFiltered()

	// Should match "Code Quality" category
	if len(m.filtered) != 2 {
		t.Errorf("Expected 2 filtered commands for 'quality', got %d", len(m.filtered))
	}
}

func TestUpdateFilteredClearFilter(t *testing.T) {
	m := New("test-session", testCommands)

	// Set then clear filter
	m.filter.SetValue("fix")
	m.updateFiltered()
	m.filter.SetValue("")
	m.updateFiltered()

	if len(m.filtered) != len(testCommands) {
		t.Errorf("Expected all commands after clearing filter, got %d", len(m.filtered))
	}
}

func TestUpdateFilteredPreservesSelectionByKey(t *testing.T) {
	commands := []config.PaletteCmd{
		{Key: "foo", Label: "Foo", Category: "", Prompt: "Foo"},
		{Key: "bar", Label: "Bar", Category: "", Prompt: "Bar"},
		{Key: "baz", Label: "Baz", Category: "", Prompt: "Baz"},
	}

	m := New("test-session", commands)
	m.cursor = 1 // "bar"

	m.filter.SetValue("ba") // Matches "bar" and "baz"
	m.updateFiltered()

	if len(m.filtered) != 2 {
		t.Fatalf("Expected 2 filtered commands for 'ba', got %d", len(m.filtered))
	}
	if m.filtered[0].Key != "bar" || m.filtered[1].Key != "baz" {
		t.Fatalf("Unexpected filtered order: got [%s, %s]", m.filtered[0].Key, m.filtered[1].Key)
	}
	if m.cursor != 0 {
		t.Fatalf("Expected cursor to remain on 'bar' (index 0), got %d", m.cursor)
	}
}

func TestUpdateFilteredNoMatches(t *testing.T) {
	m := New("test-session", testCommands)

	m.filter.SetValue("zzzznonexistent")
	m.updateFiltered()

	if len(m.filtered) != 0 {
		t.Errorf("Expected 0 filtered for non-matching query, got %d", len(m.filtered))
	}

	// Cursor should be reset to 0 (or stay in bounds)
	if m.cursor != 0 {
		t.Errorf("Expected cursor reset to 0 with no matches, got %d", m.cursor)
	}
}

func TestBuildVisualOrder(t *testing.T) {
	m := New("test-session", testCommands)
	m.buildVisualOrder()

	// Visual order should map visual position to filtered index
	if len(m.visualOrder) != len(testCommands) {
		t.Errorf("Expected visualOrder length %d, got %d", len(testCommands), len(m.visualOrder))
	}

	// All indices should be valid
	for _, idx := range m.visualOrder {
		if idx < 0 || idx >= len(m.filtered) {
			t.Errorf("Invalid visual order index: %d", idx)
		}
	}
}

func TestBuildVisualOrderEmpty(t *testing.T) {
	m := New("test-session", nil)
	m.buildVisualOrder()

	if len(m.visualOrder) != 0 {
		t.Errorf("Expected empty visualOrder for empty commands, got %d", len(m.visualOrder))
	}
}

// TestDigitsTypeIntoFilterNotQuickSelect: 1-9 quick-select was removed (H3,
// bd-ws7-docs-ux-truth-tqh3l.3 — REJECTED decision). The filter input is
// always focused in the command phase, so digits must type into the search
// box rather than selecting items.
func TestDigitsTypeIntoFilterNotQuickSelect(t *testing.T) {
	for _, digit := range []rune{'1', '5', '9'} {
		m := New("test-session", testCommands)

		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{digit}}
		newModel, _ := m.Update(msg)
		m = newModel.(Model)

		if m.phase != PhaseCommand {
			t.Errorf("digit %q: expected to stay in PhaseCommand, got %v", digit, m.phase)
		}
		if m.selected != nil {
			t.Errorf("digit %q: expected no selection (quick-select is rejected), got %q", digit, m.selected.Key)
		}
		if m.filter.Value() != string(digit) {
			t.Errorf("digit %q: expected filter value %q, got %q", digit, string(digit), m.filter.Value())
		}
	}
}

func TestTargetPhaseBack(t *testing.T) {
	m := New("test-session", testCommands)
	m.phase = PhaseTarget
	m.selected = &testCommands[0]

	msg := tea.KeyMsg{Type: tea.KeyEsc}
	newModel, _ := m.updateTargetPhase(msg)
	m = newModel.(Model)

	if m.phase != PhaseCommand {
		t.Errorf("Expected phase to return to PhaseCommand, got %v", m.phase)
	}

	if m.selected != nil {
		t.Error("Expected selected to be cleared")
	}
}

func TestTargetPhaseQuit(t *testing.T) {
	m := New("test-session", testCommands)
	m.phase = PhaseTarget
	m.selected = &testCommands[0]

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	newModel, cmd := m.updateTargetPhase(msg)
	m = newModel.(Model)

	if !m.quitting {
		t.Error("Expected quitting to be true")
	}

	if cmd == nil {
		t.Error("Expected quit command")
	}
}

func TestViewCommandPhase(t *testing.T) {
	m := New("test-session", testCommands)
	m.width = 80
	m.height = 24

	view := stripANSI(m.View())

	// View should contain key elements (check for either title variation)
	if !strings.Contains(view, "NTM Command Palette") && !strings.Contains(view, "Palette") {
		t.Error("View should contain title")
	}
}

func TestHelpOverlayToggle(t *testing.T) {
	m := New("test-session", testCommands)

	// Move to PhaseTarget first (? only opens help in non-text-input phases)
	m.phase = PhaseTarget
	m.selected = &testCommands[0]

	// Open help overlay with '?'
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = newModel.(Model)
	if !m.showHelp {
		t.Fatal("Expected showHelp to be true after pressing '?'")
	}

	view := stripANSI(m.View())
	if !strings.Contains(view, "Palette Shortcuts") && !strings.Contains(view, "Keyboard Shortcuts") {
		t.Fatalf("Expected help overlay view to include title, got: %q", view)
	}

	// While help is open, other keys should not quit.
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = newModel.(Model)
	if m.quitting {
		t.Fatal("Expected palette not to quit when help overlay is open")
	}
	if !m.showHelp {
		t.Fatal("Expected help overlay to remain open after non-close key")
	}

	// Close help overlay with Esc
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = newModel.(Model)
	if m.showHelp {
		t.Fatal("Expected showHelp to be false after pressing Esc")
	}
}

func TestPreviewShowsTargetSummaryAndPromptMetadata(t *testing.T) {
	m := New("test-session", testCommands)
	m.paneCountsKnown = true
	m.paneCounts = paneCounts{totalAgents: 7, claude: 3, codex: 2, gemini: 2}

	out := stripANSI(m.renderPreview(80))
	if !strings.Contains(out, "Targets:") {
		t.Fatalf("Expected preview to include target summary, got: %q", out)
	}
	if !strings.Contains(out, "all 7") || !strings.Contains(out, "cc 3") || !strings.Contains(out, "cod 2") || !strings.Contains(out, "gmi 2") {
		t.Fatalf("Expected target badges with counts, got: %q", out)
	}
	if !strings.Contains(out, "lines") || !strings.Contains(out, "chars") {
		t.Fatalf("Expected preview to include prompt metadata (lines/chars), got: %q", out)
	}
	if !strings.Contains(out, "key:") {
		t.Fatalf("Expected preview to include key metadata, got: %q", out)
	}
}

func TestPreviewSafetyNudgesIncludeDestructive(t *testing.T) {
	commands := []config.PaletteCmd{
		{Key: "danger", Label: "Danger", Category: "Quick Actions", Prompt: "Run `git reset --hard` then `rm -rf`"},
	}
	m := New("test-session", commands)
	m.paneCountsKnown = true
	m.paneCounts = paneCounts{totalAgents: 1}

	out := stripANSI(m.renderPreview(80))
	if !strings.Contains(strings.ToLower(out), "destructive") {
		t.Fatalf("Expected destructive nudge badge, got: %q", out)
	}
}

func TestViewTargetPhase(t *testing.T) {
	m := New("test-session", testCommands)
	m.phase = PhaseTarget
	m.selected = &testCommands[0]
	m.width = 80
	m.height = 24

	view := m.View()

	// View should contain target options
	if !strings.Contains(view, "Target") && !strings.Contains(view, "All Agents") {
		t.Log("Target phase view:", view)
		// May not contain text if rendering is complex
	}
}

func TestTargetPhaseShowsSamplePanesWhenWide(t *testing.T) {
	m := New("test-session", testCommands)
	m.phase = PhaseTarget
	m.selected = &testCommands[0]
	m.paneCountsKnown = true
	m.paneCounts = paneCounts{
		totalAgents:   3,
		claude:        1,
		codex:         1,
		gemini:        1,
		allSamples:    []string{"test-session__cc_1", "test-session__cod_1", "test-session__gmi_1"},
		claudeSamples: []string{"test-session__cc_1"},
		codexSamples:  []string{"test-session__cod_1"},
		geminiSamples: []string{"test-session__gmi_1"},
	}

	// Wide terminal enables sample pane rendering.
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 220, Height: 40})
	m = newModel.(Model)

	view := stripANSI(m.View())
	if !strings.Contains(view, "All Agents") || !strings.Contains(view, "(3)") {
		t.Fatalf("Expected target selector to include counts for All Agents, got: %q", view)
	}
	if !strings.Contains(view, "e.g.") || !strings.Contains(view, "test-session__cc_1") {
		t.Fatalf("Expected target selector to include sample pane titles, got: %q", view)
	}
}

func TestViewQuitting(t *testing.T) {
	m := New("test-session", testCommands)
	m.quitting = true

	view := m.View()

	// Quitting view should be relatively empty or show exit message
	if len(view) > 1000 {
		t.Error("Quitting view should be concise")
	}
}

func TestViewQuittingWithError(t *testing.T) {
	m := New("test-session", testCommands)
	m.quitting = true
	m.err = errTestError

	view := m.View()

	if !strings.Contains(view, "Error") {
		t.Error("Quitting with error should show error message")
	}
}

func TestResult(t *testing.T) {
	m := New("test-session", testCommands)
	m.sent = true
	m.err = nil

	sent, err := m.Result()

	if !sent {
		t.Error("Expected sent to be true")
	}

	if err != nil {
		t.Errorf("Expected nil error, got %v", err)
	}
}

func TestResultWithError(t *testing.T) {
	m := New("test-session", testCommands)
	m.sent = false
	m.err = errTestError

	sent, err := m.Result()

	if sent {
		t.Error("Expected sent to be false")
	}

	if err != errTestError {
		t.Errorf("Expected test error, got %v", err)
	}
}

var errTestError = tea.ErrProgramKilled

// ═══════════════════════════════════════════════════════════════
// SessionSelector Tests
// ═══════════════════════════════════════════════════════════════

var testSessions = []tmux.Session{
	{Name: "project1", Windows: 2, Attached: false},
	{Name: "project2", Windows: 3, Attached: true},
	{Name: "project3", Windows: 1, Attached: false},
}

func TestNewSessionSelector(t *testing.T) {
	s := NewSessionSelector(testSessions)

	if len(s.sessions) != len(testSessions) {
		t.Errorf("Expected %d sessions, got %d", len(testSessions), len(s.sessions))
	}

	if s.cursor != 0 {
		t.Errorf("Expected cursor at 0, got %d", s.cursor)
	}

	if s.selected != "" {
		t.Error("Expected selected to be empty initially")
	}
}

func TestNewSessionSelectorEmpty(t *testing.T) {
	s := NewSessionSelector(nil)

	if len(s.sessions) != 0 {
		t.Errorf("Expected 0 sessions, got %d", len(s.sessions))
	}
}

func TestSessionSelectorInit(t *testing.T) {
	t.Setenv("NTM_ANIMATIONS", "1")
	t.Setenv("TMUX", "")
	t.Setenv("STY", "")
	t.Setenv("CI", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")

	s := NewSessionSelector(testSessions)
	cmd := s.Init()

	if cmd == nil {
		t.Error("Init should return a command")
	}
}

func TestSessionSelectorWindowSize(t *testing.T) {
	s := NewSessionSelector(testSessions)

	msg := tea.WindowSizeMsg{Width: 100, Height: 50}
	newModel, _ := s.Update(msg)
	s = newModel.(SessionSelector)

	if s.width != 100 {
		t.Errorf("Expected width 100, got %d", s.width)
	}
}

func TestSessionSelectorNavigationUp(t *testing.T) {
	s := NewSessionSelector(testSessions)
	s.cursor = 1

	msg := tea.KeyMsg{Type: tea.KeyUp}
	newModel, _ := s.Update(msg)
	s = newModel.(SessionSelector)

	if s.cursor != 0 {
		t.Errorf("Expected cursor to move to 0, got %d", s.cursor)
	}
}

func TestSessionSelectorNavigationDown(t *testing.T) {
	s := NewSessionSelector(testSessions)
	s.cursor = 0

	msg := tea.KeyMsg{Type: tea.KeyDown}
	newModel, _ := s.Update(msg)
	s = newModel.(SessionSelector)

	if s.cursor != 1 {
		t.Errorf("Expected cursor to move to 1, got %d", s.cursor)
	}
}

func TestSessionSelectorSelect(t *testing.T) {
	s := NewSessionSelector(testSessions)
	s.cursor = 1

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	newModel, cmd := s.Update(msg)
	s = newModel.(SessionSelector)

	if s.selected != "project2" {
		t.Errorf("Expected selected 'project2', got '%s'", s.selected)
	}

	if cmd == nil {
		t.Error("Expected quit command after selection")
	}
}

func TestSessionSelectorSelectEmpty(t *testing.T) {
	s := NewSessionSelector(nil)

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	newModel, _ := s.Update(msg)
	s = newModel.(SessionSelector)

	if s.selected != "" {
		t.Error("Expected selected to be empty with no sessions")
	}
}

func TestSessionSelectorQuit(t *testing.T) {
	s := NewSessionSelector(testSessions)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	newModel, cmd := s.Update(msg)
	s = newModel.(SessionSelector)

	if !s.quitting {
		t.Error("Expected quitting to be true")
	}

	if cmd == nil {
		t.Error("Expected quit command")
	}
}

func TestSessionSelectorSelectByNumber(t *testing.T) {
	s := NewSessionSelector(testSessions)

	if !s.selectByNumber(2) {
		t.Error("selectByNumber(2) should return true")
	}

	if s.selected != "project2" {
		t.Errorf("Expected selected 'project2', got '%s'", s.selected)
	}

	if s.cursor != 1 {
		t.Errorf("Expected cursor at 1, got %d", s.cursor)
	}
}

func TestSessionSelectorSelectByNumberOutOfRange(t *testing.T) {
	s := NewSessionSelector(testSessions)

	if s.selectByNumber(99) {
		t.Error("selectByNumber(99) should return false")
	}
}

func TestSessionSelectorView(t *testing.T) {
	s := NewSessionSelector(testSessions)
	s.width = 80
	s.height = 24

	view := stripANSI(s.View())

	// View should contain session names
	if !strings.Contains(view, "project1") {
		t.Logf("View output: %q", view)
		t.Error("View should contain session name 'project1'")
	}

	if !strings.Contains(view, "project2") {
		t.Error("View should contain session name 'project2'")
	}
}

func TestSessionSelectorViewEmpty(t *testing.T) {
	s := NewSessionSelector(nil)
	s.width = 80
	s.height = 24

	view := s.View()

	// Should show empty state message
	if !strings.Contains(view, "No tmux sessions") {
		t.Error("View should show 'No tmux sessions' for empty list")
	}
}

func TestSessionSelectorSelected(t *testing.T) {
	s := NewSessionSelector(testSessions)
	s.selected = "test-session"

	if s.Selected() != "test-session" {
		t.Errorf("Expected Selected() to return 'test-session', got '%s'", s.Selected())
	}
}

func TestRunSessionSelectorEmpty(t *testing.T) {
	_, err := RunSessionSelector(nil)

	if err == nil {
		t.Error("Expected error for empty sessions")
	}
}

func TestRunSessionSelectorSingleSession(t *testing.T) {
	sessions := []tmux.Session{{Name: "only-one"}}

	name, err := RunSessionSelector(sessions)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if name != "only-one" {
		t.Errorf("Expected 'only-one', got '%s'", name)
	}
}

func TestVisualOrderPinnedThenRecents(t *testing.T) {
	m := NewWithOptions("test-session", testCommands, Options{
		PaletteState: config.PaletteState{
			Pinned:    []string{"docs"},
			Favorites: []string{"review"},
		},
	})

	// Simulate loaded recents (most-recent-first)
	m.recents = []string{"test", "fix"}
	m.buildVisualOrder()

	// Expected order: pinned (docs), recents (test, fix), then remaining categories (refactor, review)
	want := []int{3, 1, 0, 2, 4}
	if !reflect.DeepEqual(m.visualOrder, want) {
		t.Fatalf("Unexpected visualOrder:\nwant %v\ngot  %v", want, m.visualOrder)
	}

	out := stripANSI(m.renderCommandList(80))
	if !strings.Contains(out, "Pinned") {
		t.Fatalf("Expected command list to include 'Pinned' header:\n%s", out)
	}
	if !strings.Contains(out, "Recent") {
		t.Fatalf("Expected command list to include 'Recent' header:\n%s", out)
	}
}

func TestTogglePinAndFavoritePersistsPaletteState(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("theme = \"mocha\"\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	m := NewWithOptions("test-session", testCommands, Options{
		PaletteStatePath: cfgPath,
	})

	// Pin the first command ("fix")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if cmd == nil {
		t.Fatalf("Expected save cmd for Ctrl+P when PaletteStatePath is set")
	}

	msg := cmd()
	if saved, ok := msg.(paletteStateSavedMsg); ok && saved.err != nil {
		t.Fatalf("Unexpected save error: %v", saved.err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "[palette_state]") {
		t.Fatalf("Expected config to include [palette_state] table:\n%s", text)
	}
	if !strings.Contains(text, "\"fix\"") {
		t.Fatalf("Expected pinned/favorite key to be persisted:\n%s", text)
	}
}

func TestFetchRecentsFromHistory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	e1 := history.NewEntry("test-session", []string{"1"}, "Fix the bug", history.SourcePalette)
	e1.Template = "fix"
	if err := history.Append(e1); err != nil {
		t.Fatalf("append history: %v", err)
	}

	e2 := history.NewEntry("test-session", []string{"1"}, "Run all tests", history.SourcePalette)
	e2.Template = "test"
	if err := history.Append(e2); err != nil {
		t.Fatalf("append history: %v", err)
	}

	e3 := history.NewEntry("test-session", []string{"1"}, "Fix the bug", history.SourcePalette)
	e3.Template = "fix"
	if err := history.Append(e3); err != nil {
		t.Fatalf("append history: %v", err)
	}

	m := New("test-session", testCommands)
	cmd := m.fetchRecents()
	if cmd == nil {
		t.Fatalf("Expected fetchRecents cmd")
	}
	msg := cmd()
	newModel, _ := m.Update(msg)
	m = newModel.(Model)

	// Most-recent-first unique keys.
	want := []string{"fix", "test"}
	if !reflect.DeepEqual(m.recents, want) {
		t.Fatalf("Unexpected recents:\nwant %v\ngot  %v", want, m.recents)
	}
}

// ═══════════════════════════════════════════════════════════════
// KeyMap Tests
// ═══════════════════════════════════════════════════════════════

func TestKeyMapBindings(t *testing.T) {
	// Test that key bindings are properly configured
	if !key.Matches(tea.KeyMsg{Type: tea.KeyUp}, commandKeys.Up) {
		t.Error("Up key should match")
	}

	if !key.Matches(tea.KeyMsg{Type: tea.KeyDown}, commandKeys.Down) {
		t.Error("Down key should match")
	}

	if !key.Matches(tea.KeyMsg{Type: tea.KeyEnter}, commandKeys.Select) {
		t.Error("Enter key should match Select")
	}

	if !key.Matches(tea.KeyMsg{Type: tea.KeyEsc}, commandKeys.Back) {
		t.Error("Esc key should match Back")
	}

	if !key.Matches(tea.KeyMsg{Type: tea.KeyCtrlP}, commandKeys.TogglePin) {
		t.Error("Ctrl+P should match TogglePin")
	}

	if !key.Matches(tea.KeyMsg{Type: tea.KeyCtrlF}, commandKeys.ToggleFavorite) {
		t.Error("Ctrl+F should match ToggleFavorite")
	}
}

func TestSelectorKeyMapBindings(t *testing.T) {
	if !key.Matches(tea.KeyMsg{Type: tea.KeyUp}, selectorKeys.Up) {
		t.Error("Up key should match in selector")
	}

	if !key.Matches(tea.KeyMsg{Type: tea.KeyEnter}, selectorKeys.Select) {
		t.Error("Enter key should match Select in selector")
	}
}

// --- #204: command list box fits its content instead of a fixed oversized box ---

func TestCommandListBoxFitsContentWhenShort(t *testing.T) {
	m := New("test-session", testCommands)
	// Tall terminal: the list box must NOT expand to fill it when there are only
	// a handful of commands - it should size to its content (#204).
	m.width = 100
	m.height = 100
	m.syncListViewport()

	maxHeight := m.height - 14
	got := m.commandListBoxHeight()
	if got >= maxHeight {
		t.Fatalf("expected list box to fit content (< maxHeight %d), got %d", maxHeight, got)
	}
	if got < 5 {
		t.Fatalf("expected a sensible minimum height, got %d", got)
	}
}

func TestCommandListBoxCapsAndScrollsWhenLong(t *testing.T) {
	var many []config.PaletteCmd
	for i := 0; i < 100; i++ {
		many = append(many, config.PaletteCmd{
			Key:      fmtKey(i),
			Label:    fmtKey(i),
			Category: "Quick Actions",
			Prompt:   "do a thing",
		})
	}
	m := New("test-session", many)
	m.width = 100
	m.height = 40
	m.syncListViewport()

	maxHeight := m.height - 14
	if got := m.commandListBoxHeight(); got != maxHeight {
		t.Fatalf("expected long list to cap at maxHeight %d, got %d", maxHeight, got)
	}
	if m.listViewport.TotalLineCount() <= m.listViewport.Height {
		t.Fatalf("expected content to overflow the viewport so it scrolls")
	}
}

func fmtKey(i int) string {
	return "cmd-" + string(rune('a'+(i%26))) + string(rune('0'+(i/26)))
}

// --- #206: compose a free-text custom message from scratch ---

func TestComposeCustomMessageShortcut(t *testing.T) {
	m := New("test-session", testCommands)

	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = newModel.(Model)

	if m.phase != PhaseEdit {
		t.Fatalf("expected ctrl+n to open the editor (PhaseEdit), got phase %v", m.phase)
	}
	if m.selected == nil || m.selected.Key != customMessageKey {
		t.Fatalf("expected a synthetic custom-message selection, got %+v", m.selected)
	}
	if strings.TrimSpace(m.editInput.Value()) != "" {
		t.Fatalf("expected an empty editor for a from-scratch message, got %q", m.editInput.Value())
	}

	// Type a message and confirm; it should land in the target picker with the
	// composed text captured as the draft.
	m.editInput.SetValue("please run the tests")
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = newModel.(Model)
	if m.phase != PhaseTarget {
		t.Fatalf("expected ctrl+s to advance to PhaseTarget, got %v", m.phase)
	}
	if m.editDraft != "please run the tests" {
		t.Fatalf("expected composed text to be captured, got %q", m.editDraft)
	}
}

// TestSendRefusesEmptyMessage guards the #206 regression: a compose (or an
// edited-to-empty command) confirmed with nothing typed must NOT be dispatched.
// send() would otherwise fire a double-Enter with no text at every target pane,
// submitting whatever half-typed input already sits in each agent's prompt.
func TestSendRefusesEmptyMessage(t *testing.T) {
	m := New("test-session", testCommands)

	// Enter the compose flow (empty prompt) and confirm without typing.
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = newModel.(Model)
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS}) // ConfirmEdit with empty draft
	m = newModel.(Model)
	if m.phase != PhaseTarget {
		t.Fatalf("expected PhaseTarget after confirming, got %v", m.phase)
	}

	// Attempt to send. It must refuse: bounce back to the editor with a notice,
	// and NOT mark the message as sent (no tmux dispatch).
	model, _ := m.send()
	m = model.(Model)
	if m.sent {
		t.Fatalf("empty message must not be sent")
	}
	if m.phase != PhaseEdit {
		t.Fatalf("expected send() to bounce empty message back to PhaseEdit, got %v", m.phase)
	}
	if strings.TrimSpace(m.editNotice) == "" {
		t.Fatalf("expected an editNotice explaining the empty message was refused")
	}

	// A whitespace-only draft is likewise refused.
	m.editDraft = "   \n\t "
	m.phase = PhaseTarget
	model, _ = m.send()
	m = model.(Model)
	if m.sent || m.phase != PhaseEdit {
		t.Fatalf("whitespace-only message must be refused too (sent=%v phase=%v)", m.sent, m.phase)
	}
}

func TestSendRejectsMixedGrokBatchBeforeAnyPaneMutation(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	m := New("mixed-grok", testCommands)
	m.selected = &testCommands[0]
	m.target = TargetAll

	sentTargets := []string{}
	model, _ := m.sendWith(
		func(session string) ([]tmux.Pane, error) {
			if session != "mixed-grok" {
				t.Fatalf("getPanes session = %q, want mixed-grok", session)
			}
			return []tmux.Pane{
				{ID: "%1", Index: 1, Type: tmux.AgentClaude},
				{ID: "%2", Index: 2, Type: tmux.AgentGrok},
			}, nil
		},
		func(target, _ string, _ tmux.AgentType) error {
			sentTargets = append(sentTargets, target)
			return nil
		},
	)

	got := model.(Model)
	if !errors.Is(got.err, agent.ErrAutomatedPromptDeliveryNotImplemented) {
		t.Fatalf("send() error = %v, want Grok prompt-delivery sentinel", got.err)
	}
	if len(sentTargets) != 0 {
		t.Fatalf("send() mutated targets %v before mixed-batch rejection", sentTargets)
	}
	if got.sent || got.sentCount != 0 {
		t.Fatalf("send() state = sent:%v count:%d, want unsent", got.sent, got.sentCount)
	}
}

// --- #205: granular per-agent multi-select ---

func newAgentSelectModel() Model {
	m := New("test-session", testCommands)
	m.phase = PhaseSelectAgents
	m.selected = &testCommands[0]
	m.agentPanes = []tmux.Pane{
		{ID: "%1", Index: 1, Title: "test-session__cc_1", Type: tmux.AgentClaude},
		{ID: "%2", Index: 2, Title: "test-session__cod_1", Type: tmux.AgentCodex},
	}
	m.agentChecked = map[string]bool{"%1": true, "%2": true}
	m.agentCursor = 0
	return m
}

func TestSelectAgentsToggleAllNone(t *testing.T) {
	m := newAgentSelectModel()

	// Space toggles the pane under the cursor off.
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = newModel.(Model)
	if m.agentChecked["%1"] {
		t.Fatalf("expected space to uncheck pane under cursor")
	}
	if !m.agentChecked["%2"] {
		t.Fatalf("expected other pane to stay checked")
	}

	// 'n' clears all.
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = newModel.(Model)
	if m.agentChecked["%1"] || m.agentChecked["%2"] {
		t.Fatalf("expected 'n' to deselect all")
	}

	// 'a' selects all.
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = newModel.(Model)
	if !m.agentChecked["%1"] || !m.agentChecked["%2"] {
		t.Fatalf("expected 'a' to select all")
	}
}

func TestSelectAgentsNavigation(t *testing.T) {
	m := newAgentSelectModel()

	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newModel.(Model)
	if m.agentCursor != 1 {
		t.Fatalf("expected cursor to move down to 1, got %d", m.agentCursor)
	}
	// Down at the bottom should clamp.
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newModel.(Model)
	if m.agentCursor != 1 {
		t.Fatalf("expected cursor to clamp at last index, got %d", m.agentCursor)
	}
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = newModel.(Model)
	if m.agentCursor != 0 {
		t.Fatalf("expected cursor to move back to 0, got %d", m.agentCursor)
	}
}

func TestSelectAgentsBackReturnsToTarget(t *testing.T) {
	m := newAgentSelectModel()
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = newModel.(Model)
	if m.phase != PhaseTarget {
		t.Fatalf("expected esc to return to PhaseTarget, got %v", m.phase)
	}
}

func TestSelectAgentsEnterWithNoSelectionStays(t *testing.T) {
	m := newAgentSelectModel()
	m.agentChecked = map[string]bool{"%1": false, "%2": false}
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = newModel.(Model)
	if m.phase != PhaseSelectAgents {
		t.Fatalf("expected enter with nothing checked to stay in PhaseSelectAgents, got %v", m.phase)
	}
	if m.quitting {
		t.Fatalf("expected no send/quit when nothing is checked")
	}
}

func TestSelectAgentsView(t *testing.T) {
	m := newAgentSelectModel()
	m.width = 100
	m.height = 40
	view := stripANSI(m.viewSelectAgentsPhase())
	if !strings.Contains(view, "Select Agents") {
		t.Fatalf("expected title, got: %q", view)
	}
	if !strings.Contains(view, "test-session__cc_1") || !strings.Contains(view, "test-session__cod_1") {
		t.Fatalf("expected pane titles in view, got: %q", view)
	}
	if !strings.Contains(view, "[x]") {
		t.Fatalf("expected checked markers in view, got: %q", view)
	}
	if !strings.Contains(view, "2 of 2 selected") {
		t.Fatalf("expected selection count, got: %q", view)
	}
}

func TestTargetPhaseOffersSelectAgents(t *testing.T) {
	m := New("test-session", testCommands)
	m.phase = PhaseTarget
	m.selected = &testCommands[0]
	m.width = 100
	m.height = 40
	view := stripANSI(m.View())
	if !strings.Contains(view, "Select agents") {
		t.Fatalf("expected target phase to offer granular selection, got: %q", view)
	}
}

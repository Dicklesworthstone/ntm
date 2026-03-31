package panels

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestNewHistoryPanel(t *testing.T) {
	panel := NewHistoryPanel()
	if panel == nil {
		t.Fatal("NewHistoryPanel returned nil")
	}
}

func TestHistoryPanelConfig(t *testing.T) {
	panel := NewHistoryPanel()
	cfg := panel.Config()

	if cfg.ID != "history" {
		t.Errorf("expected ID 'history', got %q", cfg.ID)
	}
	if cfg.Title != "Command History" {
		t.Errorf("expected Title 'Command History', got %q", cfg.Title)
	}
	if cfg.Priority != PriorityNormal {
		t.Errorf("expected PriorityNormal, got %v", cfg.Priority)
	}
	if cfg.RefreshInterval != 30*time.Second {
		t.Errorf("expected 30s refresh, got %v", cfg.RefreshInterval)
	}
	if !cfg.Collapsible {
		t.Error("expected Collapsible to be true")
	}
	if cfg.MinWidth != 35 {
		t.Errorf("expected MinWidth 35, got %d", cfg.MinWidth)
	}
	if cfg.MinHeight != 8 {
		t.Errorf("expected MinHeight 8, got %d", cfg.MinHeight)
	}
}

func TestHistoryPanelSetSize(t *testing.T) {
	panel := NewHistoryPanel()
	panel.SetSize(80, 20)

	if panel.Width() != 80 {
		t.Errorf("expected width 80, got %d", panel.Width())
	}
	if panel.Height() != 20 {
		t.Errorf("expected height 20, got %d", panel.Height())
	}
}

func TestHistoryPanelFocusBlur(t *testing.T) {
	panel := NewHistoryPanel()

	panel.Focus()
	if !panel.IsFocused() {
		t.Error("expected IsFocused to be true after Focus()")
	}

	panel.Blur()
	if panel.IsFocused() {
		t.Error("expected IsFocused to be false after Blur()")
	}
}

func TestHistoryPanelSetEntries(t *testing.T) {
	panel := NewHistoryPanel()

	entries := []history.HistoryEntry{
		{ID: "entry-1", Prompt: "First command", Targets: []string{"cc_1"}, Success: true},
		{ID: "entry-2", Prompt: "Second command", Targets: []string{"cc_2"}, Success: false},
		{ID: "entry-3", Prompt: "Third command", Targets: []string{}, Success: true},
	}

	panel.SetEntries(entries, nil)

	if len(panel.entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(panel.entries))
	}
	if panel.entries[0].ID != "entry-1" {
		t.Errorf("expected first entry ID 'entry-1', got %q", panel.entries[0].ID)
	}
}

func TestHistoryPanelSetEntriesCursorBounds(t *testing.T) {
	panel := NewHistoryPanel()

	// Set initial entries and move cursor
	entries := []history.HistoryEntry{
		{ID: "entry-1", Prompt: "First"},
		{ID: "entry-2", Prompt: "Second"},
		{ID: "entry-3", Prompt: "Third"},
	}
	panel.SetEntries(entries, nil)
	panel.cursor = 2 // Point to last entry

	// Replace with fewer entries
	newEntries := []history.HistoryEntry{
		{ID: "entry-new", Prompt: "Only entry"},
	}
	panel.SetEntries(newEntries, nil)

	// Cursor should be adjusted to valid range
	if panel.cursor != 0 {
		t.Errorf("expected cursor to be adjusted to 0, got %d", panel.cursor)
	}
}

func TestHistoryPanelSetEntriesPreservesSelectedEntryIDAcrossRefresh(t *testing.T) {
	panel := NewHistoryPanel()

	initial := []history.HistoryEntry{
		{ID: "entry-a", Prompt: "Alpha"},
		{ID: "entry-b", Prompt: "Bravo"},
		{ID: "entry-c", Prompt: "Charlie"},
	}
	panel.SetEntries(initial, nil)
	panel.cursor = 1

	refreshed := []history.HistoryEntry{
		{ID: "entry-new", Prompt: "Newest"},
		{ID: "entry-a", Prompt: "Alpha"},
		{ID: "entry-b", Prompt: "Bravo"},
		{ID: "entry-c", Prompt: "Charlie"},
	}
	panel.SetEntries(refreshed, nil)

	entry, ok := panel.selectedEntry()
	if !ok {
		t.Fatal("expected selected entry after refresh")
	}
	if entry.ID != "entry-b" {
		t.Fatalf("expected selection to stay on entry-b across refresh, got %q", entry.ID)
	}
	if panel.cursor != 2 {
		t.Fatalf("expected cursor to move with entry-b after inserted row, got %d", panel.cursor)
	}
}

func TestHistoryPanelSetEntriesEmpty(t *testing.T) {
	panel := NewHistoryPanel()
	panel.cursor = 5 // Invalid cursor

	panel.SetEntries([]history.HistoryEntry{}, nil)

	// Cursor should be 0 for empty list
	if panel.cursor != 0 {
		t.Errorf("expected cursor to be 0 for empty list, got %d", panel.cursor)
	}
}

func TestHistoryPanelKeybindings(t *testing.T) {
	panel := NewHistoryPanel()
	bindings := panel.Keybindings()

	if len(bindings) == 0 {
		t.Error("expected non-empty keybindings")
	}

	// Check for expected actions
	actions := make(map[string]bool)
	for _, b := range bindings {
		actions[b.Action] = true
	}

	if !actions["replay"] {
		t.Error("expected 'replay' action in keybindings")
	}
	if !actions["copy"] {
		t.Error("expected 'copy' action in keybindings")
	}
	if !actions["down"] {
		t.Error("expected 'down' action in keybindings")
	}
	if !actions["up"] {
		t.Error("expected 'up' action in keybindings")
	}
}

func TestHistoryPanelInit(t *testing.T) {
	panel := NewHistoryPanel()
	cmd := panel.Init()
	if cmd != nil {
		t.Error("expected Init() to return nil")
	}
}

func TestHistoryPanelUpdateNotFocused(t *testing.T) {
	panel := NewHistoryPanel()
	panel.SetEntries([]history.HistoryEntry{
		{ID: "entry-1", Prompt: "First"},
		{ID: "entry-2", Prompt: "Second"},
	}, nil)

	// Panel is not focused
	panel.Blur()

	keyMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	newModel, cmd := panel.Update(keyMsg)

	if newModel != panel {
		t.Error("expected Update to return same model")
	}
	if cmd != nil {
		t.Error("expected Update to return nil cmd when not focused")
	}
	// Cursor should not have changed
	if panel.cursor != 0 {
		t.Error("cursor should not change when not focused")
	}
}

func TestHistoryPanelUpdateFocusedNavigation(t *testing.T) {
	panel := NewHistoryPanel()
	panel.SetSize(80, 20)
	panel.SetEntries([]history.HistoryEntry{
		{ID: "entry-1", Prompt: "First"},
		{ID: "entry-2", Prompt: "Second"},
		{ID: "entry-3", Prompt: "Third"},
	}, nil)
	panel.Focus()

	// Test down navigation with 'j'
	keyMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	panel.Update(keyMsg)
	if panel.cursor != 1 {
		t.Errorf("expected cursor 1 after 'j', got %d", panel.cursor)
	}

	// Test down navigation with 'down' key
	downMsg := tea.KeyMsg{Type: tea.KeyDown}
	panel.Update(downMsg)
	if panel.cursor != 2 {
		t.Errorf("expected cursor 2 after 'down', got %d", panel.cursor)
	}

	// Test that cursor doesn't go past end
	panel.Update(downMsg)
	if panel.cursor != 2 {
		t.Errorf("expected cursor to stay at 2, got %d", panel.cursor)
	}

	// Test up navigation with 'k'
	upMsgK := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}
	panel.Update(upMsgK)
	if panel.cursor != 1 {
		t.Errorf("expected cursor 1 after 'k', got %d", panel.cursor)
	}

	// Test up navigation with 'up' key
	upMsg := tea.KeyMsg{Type: tea.KeyUp}
	panel.Update(upMsg)
	if panel.cursor != 0 {
		t.Errorf("expected cursor 0 after 'up', got %d", panel.cursor)
	}

	// Test that cursor doesn't go below 0
	panel.Update(upMsg)
	if panel.cursor != 0 {
		t.Errorf("expected cursor to stay at 0, got %d", panel.cursor)
	}
}

func TestHistoryPanelViewContainsTitle(t *testing.T) {
	panel := NewHistoryPanel()
	panel.SetSize(80, 20)

	view := panel.View()

	if !strings.Contains(view, "Command History") {
		t.Error("expected view to contain title 'Command History'")
	}
}

func TestHistoryPanelViewNoHistory(t *testing.T) {
	panel := NewHistoryPanel()
	panel.SetSize(80, 20)
	panel.SetEntries([]history.HistoryEntry{}, nil)

	view := panel.View()

	if !strings.Contains(view, "No command history") {
		t.Error("expected view to contain 'No command history' when empty")
	}
}

func TestHistoryPanelViewShowsErrorState(t *testing.T) {
	panel := NewHistoryPanel()
	panel.SetSize(80, 20)
	panel.SetEntries(nil, errors.New("history fetch failed"))

	view := panel.View()

	if !strings.Contains(view, "Error") {
		t.Error("expected view to include error badge")
	}
	if !strings.Contains(view, "history fetch failed") {
		t.Error("expected view to include error message")
	}
	if !strings.Contains(view, "Press r") {
		t.Error("expected view to include retry hint")
	}
}

func TestHistoryPanelViewShowsEntries(t *testing.T) {
	panel := NewHistoryPanel()
	panel.SetSize(100, 25)

	entries := []history.HistoryEntry{
		{ID: "abcd1234", Prompt: "First command", Targets: []string{"cc_1"}, Success: true},
		{ID: "efgh5678", Prompt: "Failed command", Targets: []string{"cod_1"}, Success: false},
	}
	panel.SetEntries(entries, nil)

	view := panel.View()

	// Should contain truncated IDs (first 4 chars)
	if !strings.Contains(view, "abcd") {
		t.Error("expected view to contain first entry ID prefix 'abcd'")
	}
	if !strings.Contains(view, "efgh") {
		t.Error("expected view to contain second entry ID prefix 'efgh'")
	}

	// Should contain targets
	if !strings.Contains(view, "cc_1") {
		t.Error("expected view to contain target 'cc_1'")
	}

	// Should contain success/failure indicators
	if !strings.Contains(view, "✓") {
		t.Error("expected view to contain success indicator '✓'")
	}
	if !strings.Contains(view, "✗") {
		t.Error("expected view to contain failure indicator '✗'")
	}
}

func TestHistoryPanelViewShortIDDoesNotPanic(t *testing.T) {
	panel := NewHistoryPanel()
	panel.SetSize(80, 20)

	entries := []history.HistoryEntry{
		{ID: "1", Prompt: "Short ID entry", Targets: []string{"cc_1"}, Success: true},
	}
	panel.SetEntries(entries, nil)

	view := panel.View()
	if !strings.Contains(view, "Short ID entry") {
		t.Error("expected view to contain entry prompt when ID is short")
	}
}

func TestHistoryPanelViewShowsAllTargets(t *testing.T) {
	panel := NewHistoryPanel()
	panel.SetSize(100, 20)

	entries := []history.HistoryEntry{
		{ID: "test1234", Prompt: "Broadcast command", Targets: []string{}, Success: true},
	}
	panel.SetEntries(entries, nil)

	view := panel.View()

	// Empty targets should show "all"
	if !strings.Contains(view, "all") {
		t.Error("expected view to show 'all' for empty targets")
	}
}

func TestHistoryPanelViewTruncatesLongTargets(t *testing.T) {
	panel := NewHistoryPanel()
	panel.SetSize(100, 20)

	entries := []history.HistoryEntry{
		{ID: "test1234", Prompt: "Multi-target", Targets: []string{"agent1", "agent2", "agent3"}, Success: true},
	}
	panel.SetEntries(entries, nil)

	view := panel.View()

	// Long target lists should be truncated with ellipsis
	if !strings.Contains(view, "…") {
		t.Error("expected view to truncate long target lists with ellipsis")
	}
}

func TestHistoryPanelViewTruncatesLongPrompts(t *testing.T) {
	panel := NewHistoryPanel()
	panel.SetSize(60, 20) // Narrow width

	longPrompt := "This is a very long prompt that should definitely be truncated because it exceeds the available width"
	entries := []history.HistoryEntry{
		{ID: "test1234", Prompt: longPrompt, Targets: []string{"cc_1"}, Success: true},
	}
	panel.SetEntries(entries, nil)

	view := panel.View()

	// Long prompts should be truncated - either with ellipsis or by being cut off
	hasEllipsis := strings.Contains(view, "…")
	hasFullPrompt := strings.Contains(view, "exceeds the available width")

	if !hasEllipsis && hasFullPrompt {
		t.Error("expected view to truncate long prompts (either with ellipsis or by cutting)")
	}

	// Log for debugging
	t.Logf("View contains ellipsis: %v, full prompt: %v", hasEllipsis, hasFullPrompt)
}

func TestHistoryPanelViewMultilinePromptFlattened(t *testing.T) {
	panel := NewHistoryPanel()
	panel.SetSize(100, 20)

	entries := []history.HistoryEntry{
		{ID: "test1234", Prompt: "Line one\nLine two\nLine three", Targets: []string{}, Success: true},
	}
	panel.SetEntries(entries, nil)

	view := panel.View()

	// Newlines should be replaced with spaces
	if strings.Contains(view, "\nLine") {
		t.Error("expected newlines in prompt to be replaced")
	}
}

func TestHistoryPanelContentHeight(t *testing.T) {
	panel := NewHistoryPanel()
	panel.SetSize(80, 20)

	// contentHeight = Height - 4 (borders + header)
	expected := 16
	if panel.contentHeight() != expected {
		t.Errorf("expected contentHeight %d, got %d", expected, panel.contentHeight())
	}
}

func TestHistoryPanelScrollingOffset(t *testing.T) {
	panel := NewHistoryPanel()
	panel.SetSize(80, 10) // Small height to force scrolling
	panel.Focus()

	// Create many entries
	entries := make([]history.HistoryEntry, 20)
	for i := 0; i < 20; i++ {
		entries[i] = history.HistoryEntry{
			ID:      "entry" + string(rune('0'+i)),
			Prompt:  "Command",
			Success: true,
		}
	}
	panel.SetEntries(entries, nil)

	// Navigate down past visible area
	downMsg := tea.KeyMsg{Type: tea.KeyDown}
	for i := 0; i < 15; i++ {
		panel.Update(downMsg)
	}

	// Offset should have adjusted
	if panel.offset == 0 {
		t.Error("expected offset to be non-zero after scrolling down")
	}

	// Navigate back up
	upMsg := tea.KeyMsg{Type: tea.KeyUp}
	for i := 0; i < 15; i++ {
		panel.Update(upMsg)
	}

	// Should be back at top
	if panel.cursor != 0 {
		t.Errorf("expected cursor to be 0 after scrolling up, got %d", panel.cursor)
	}
}

func TestHistoryPanelFocusedBorderStyle(t *testing.T) {
	panel := NewHistoryPanel()
	panel.SetSize(80, 20)

	// The focused state should produce different styling
	panel.Blur()
	viewBlurred := panel.View()

	panel.Focus()
	viewFocused := panel.View()

	// Views should be different due to border color change
	// Note: This is a weak test since we can't easily check ANSI codes
	if viewBlurred == "" || viewFocused == "" {
		t.Error("views should not be empty")
	}
}

func TestHistoryPanelHandlesOwnHeight(t *testing.T) {
	panel := NewHistoryPanel()
	if !panel.HandlesOwnHeight() {
		t.Fatal("expected history panel to manage its own height")
	}
}

func TestHistoryPanelViewShowsScrollFooterWhenOverflowing(t *testing.T) {
	panel := NewHistoryPanel()
	panel.SetSize(80, 10)

	entries := make([]history.HistoryEntry, 0, 20)
	for i := 0; i < 20; i++ {
		entries = append(entries, history.HistoryEntry{
			ID:        "entry-id",
			Prompt:    "prompt",
			Targets:   []string{"cc_1"},
			Success:   true,
			Timestamp: time.Now().UTC(),
		})
	}
	panel.SetEntries(entries, nil)

	view := panel.View()
	if !strings.Contains(view, "▼") {
		t.Fatalf("expected overflowing history panel to show scroll footer, got %q", view)
	}
}

func TestHistoryEntryStruct(t *testing.T) {
	entry := history.HistoryEntry{
		ID:      "test-entry-id",
		Prompt:  "Test prompt",
		Targets: []string{"cc_1", "cod_1"},
		Success: true,
	}

	if entry.ID != "test-entry-id" {
		t.Errorf("expected ID 'test-entry-id', got %q", entry.ID)
	}
	if entry.Prompt != "Test prompt" {
		t.Errorf("expected Prompt 'Test prompt', got %q", entry.Prompt)
	}
	if len(entry.Targets) != 2 {
		t.Errorf("expected 2 targets, got %d", len(entry.Targets))
	}
	if !entry.Success {
		t.Error("expected Success to be true")
	}
}

func TestHistoryPanel_FilterStatusCycle(t *testing.T) {
	panel := NewHistoryPanel()
	panel.SetSize(80, 20)
	panel.Focus()

	entries := []history.HistoryEntry{
		{ID: "ok", Prompt: "ok", Targets: []string{"1"}, Success: true, Timestamp: time.Now().UTC()},
		{ID: "bad", Prompt: "bad", Targets: []string{"2"}, Success: false, Timestamp: time.Now().UTC()},
	}
	panel.SetEntries(entries, nil)

	if got := len(panel.visibleEntries); got != 2 {
		t.Fatalf("expected 2 visible entries initially, got %d", got)
	}

	// f => success-only
	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	if got := len(panel.visibleEntries); got != 1 {
		t.Fatalf("expected 1 visible entry after success-only filter, got %d", got)
	}
	if panel.visibleEntries[0].ID != "ok" {
		t.Fatalf("expected 'ok' entry visible, got %q", panel.visibleEntries[0].ID)
	}

	// f => failure-only
	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	if got := len(panel.visibleEntries); got != 1 {
		t.Fatalf("expected 1 visible entry after failure-only filter, got %d", got)
	}
	if panel.visibleEntries[0].ID != "bad" {
		t.Fatalf("expected 'bad' entry visible, got %q", panel.visibleEntries[0].ID)
	}

	// f => all
	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	if got := len(panel.visibleEntries); got != 2 {
		t.Fatalf("expected 2 visible entries after resetting filter, got %d", got)
	}
}

func TestHistoryPanel_FilterAgentCycle_UsesPaneMeta(t *testing.T) {
	panel := NewHistoryPanel()
	panel.SetSize(80, 20)
	panel.Focus()

	entries := []history.HistoryEntry{
		{ID: "cc", Prompt: "to claude", Targets: []string{"1"}, Success: true, Timestamp: time.Now().UTC()},
		{ID: "cod", Prompt: "to codex", Targets: []string{"2"}, Success: true, Timestamp: time.Now().UTC()},
	}
	panel.SetEntries(entries, nil)
	panel.SetPanes([]tmux.Pane{
		{Index: 1, Type: tmux.AgentClaude, NTMIndex: 1, Title: "sess__cc_1"},
		{Index: 2, Type: tmux.AgentCodex, NTMIndex: 1, Title: "sess__cod_1"},
	})

	// a => Claude only
	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if got := len(panel.visibleEntries); got != 1 {
		t.Fatalf("expected 1 visible entry after Claude filter, got %d", got)
	}
	if panel.visibleEntries[0].ID != "cc" {
		t.Fatalf("expected 'cc' entry visible, got %q", panel.visibleEntries[0].ID)
	}

	// a => Codex only
	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if got := len(panel.visibleEntries); got != 1 {
		t.Fatalf("expected 1 visible entry after Codex filter, got %d", got)
	}
	if panel.visibleEntries[0].ID != "cod" {
		t.Fatalf("expected 'cod' entry visible, got %q", panel.visibleEntries[0].ID)
	}

	// a => Gemini only (none)
	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if got := len(panel.visibleEntries); got != 0 {
		t.Fatalf("expected 0 visible entries after Gemini filter, got %d", got)
	}

	// a => Any (all)
	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if got := len(panel.visibleEntries); got != 2 {
		t.Fatalf("expected 2 visible entries after resetting agent filter, got %d", got)
	}
}

func TestHistoryPanel_FilterTimeCycle(t *testing.T) {
	panel := NewHistoryPanel()
	panel.SetSize(80, 20)
	panel.Focus()

	now := time.Now().UTC()
	entries := []history.HistoryEntry{
		{ID: "recent", Prompt: "recent", Targets: []string{"1"}, Success: true, Timestamp: now.Add(-10 * time.Minute)},
		{ID: "old", Prompt: "old", Targets: []string{"1"}, Success: true, Timestamp: now.Add(-2 * time.Hour)},
	}
	panel.SetEntries(entries, nil)

	// t => 1h
	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if got := len(panel.visibleEntries); got != 1 {
		t.Fatalf("expected 1 visible entry after 1h filter, got %d", got)
	}
	if panel.visibleEntries[0].ID != "recent" {
		t.Fatalf("expected 'recent' entry visible, got %q", panel.visibleEntries[0].ID)
	}
}

func TestHistoryPanel_PreviewOverlayToggleAndCopyMsg(t *testing.T) {
	panel := NewHistoryPanel()
	panel.SetSize(80, 20)
	panel.Focus()

	entries := []history.HistoryEntry{
		{ID: "entry-1", Prompt: "First command", Targets: []string{"1"}, Success: true, Timestamp: time.Now().UTC()},
	}
	panel.SetEntries(entries, nil)

	// v => open preview overlay
	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	if !panel.showPreview {
		t.Fatal("expected preview overlay to be shown after 'v'")
	}

	// y should produce a CopyMsg command while preview is open.
	_, cmd := panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("expected non-nil cmd after 'y' in preview overlay")
	}
	msg := cmd()
	copyMsg, ok := msg.(CopyMsg)
	if !ok {
		t.Fatalf("expected CopyMsg, got %T", msg)
	}
	if copyMsg.Text != "First command" {
		t.Fatalf("expected copied text to match prompt, got %q", copyMsg.Text)
	}

	// Esc => close preview overlay
	panel.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if panel.showPreview {
		t.Fatal("expected preview overlay to be closed after Esc")
	}
}

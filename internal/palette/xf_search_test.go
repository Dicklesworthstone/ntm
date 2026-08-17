package palette

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

func TestEnterXFSearch(t *testing.T) {
	t.Parallel()
	m := New("test-session", testCommands)
	m.enterXFSearch()

	if m.phase != PhaseXFSearch {
		t.Fatalf("expected PhaseXFSearch, got %d", m.phase)
	}
	if m.xfSearching {
		t.Fatal("expected xfSearching=false")
	}
	if m.xfErr != nil {
		t.Fatalf("expected xfErr=nil, got %v", m.xfErr)
	}
	if len(m.xfResults) != 0 {
		t.Fatalf("expected empty xfResults, got %d", len(m.xfResults))
	}
}

func TestCtrlKTriggersXFSearch(t *testing.T) {
	t.Parallel()
	m := New("test-session", testCommands)

	// Simulate Ctrl+K in command phase
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	result := updated.(Model)

	if result.phase != PhaseXFSearch {
		t.Fatalf("expected PhaseXFSearch after Ctrl+K, got %d", result.phase)
	}
}

func TestXFSearchSelectCommandEntry(t *testing.T) {
	t.Parallel()
	// Create commands with xf-search entry
	cmds := append(testCommands, config.PaletteCmd{
		Key:      "xf-search",
		Label:    "XF Archive Search",
		Category: "xf",
		Prompt:   "Search X/Twitter archive (Ctrl+K)",
	})
	m := New("test-session", cmds)

	// Navigate to the xf-search entry
	xfIdx := -1
	for i, cmd := range m.filtered {
		if cmd.Key == "xf-search" {
			xfIdx = i
			break
		}
	}
	if xfIdx == -1 {
		t.Fatal("xf-search command not found in filtered list")
	}
	m.cursor = xfIdx

	// Press Enter should enter XF search instead of target phase
	updated, _ := m.updateCommandPhase(tea.KeyMsg{Type: tea.KeyEnter})
	result := updated.(Model)

	if result.phase != PhaseXFSearch {
		t.Fatalf("expected PhaseXFSearch after selecting xf-search, got %d", result.phase)
	}
}

func TestXFSearchBackReturnsToCommand(t *testing.T) {
	t.Parallel()
	m := New("test-session", testCommands)
	m.enterXFSearch()

	// Press Esc to go back
	updated, _ := m.updateXFSearchPhase(tea.KeyMsg{Type: tea.KeyEscape})
	result := updated.(Model)

	if result.phase != PhaseCommand {
		t.Fatalf("expected PhaseCommand after Esc, got %d", result.phase)
	}
}

func TestXFSearchEmptyQueryNoOp(t *testing.T) {
	t.Parallel()
	m := New("test-session", testCommands)
	m.enterXFSearch()

	// Press Enter with empty query should not start search
	updated, cmd := m.updateXFSearchPhase(tea.KeyMsg{Type: tea.KeyEnter})
	result := updated.(Model)

	if result.xfSearching {
		t.Fatal("expected xfSearching=false for empty query")
	}
	if cmd != nil {
		t.Fatal("expected nil cmd for empty query")
	}
}

func TestXFSearchResultsMsg(t *testing.T) {
	t.Parallel()
	m := New("test-session", testCommands)
	m.enterXFSearch()
	m.xfSearching = true

	results := []tools.XFSearchResult{
		{ID: "tweet-1", Content: "Go error handling patterns", CreatedAt: "2024-01-15", Type: "tweet", Score: 0.95},
		{ID: "tweet-2", Content: "Concurrency in Go", CreatedAt: "2024-02-20", Type: "tweet", Score: 0.88},
	}

	updated, _ := m.Update(XFSearchResultsMsg{
		Query:   "go patterns",
		Results: results,
	})
	result := updated.(Model)

	if result.phase != PhaseXFResults {
		t.Fatalf("expected PhaseXFResults, got %d", result.phase)
	}
	if result.xfSearching {
		t.Fatal("expected xfSearching=false after results")
	}
	if len(result.xfResults) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.xfResults))
	}
	if result.xfCursor != 0 {
		t.Fatalf("expected cursor at 0, got %d", result.xfCursor)
	}
}

func TestXFSearchResultsMsgClearsPreviousErrorOnSuccess(t *testing.T) {
	t.Parallel()

	m := New("test-session", testCommands)
	m.enterXFSearch()
	m.xfErr = tools.ErrTimeout

	results := []tools.XFSearchResult{
		{ID: "tweet-1", Content: "Recovered search result", CreatedAt: "2024-01-15", Type: "tweet", Score: 0.95},
	}

	updated, _ := m.Update(XFSearchResultsMsg{
		Query:   "recovered",
		Results: results,
	})
	result := updated.(Model)

	if result.phase != PhaseXFResults {
		t.Fatalf("expected PhaseXFResults, got %d", result.phase)
	}
	if result.xfErr != nil {
		t.Fatalf("expected xfErr to be cleared on success, got %v", result.xfErr)
	}
}

func TestXFSearchResultsMsgEmpty(t *testing.T) {
	t.Parallel()
	m := New("test-session", testCommands)
	m.enterXFSearch()
	m.xfSearching = true

	updated, _ := m.Update(XFSearchResultsMsg{
		Query:   "nonexistent",
		Results: nil,
	})
	result := updated.(Model)

	// Should stay in search phase with error
	if result.phase != PhaseXFSearch {
		t.Fatalf("expected PhaseXFSearch for empty results, got %d", result.phase)
	}
	if result.xfErr == nil {
		t.Fatal("expected xfErr to be set for empty results")
	}
}

func TestXFSearchResultsMsgError(t *testing.T) {
	t.Parallel()
	m := New("test-session", testCommands)
	m.enterXFSearch()
	m.xfSearching = true

	updated, _ := m.Update(XFSearchResultsMsg{
		Query: "test",
		Err:   tools.ErrTimeout,
	})
	result := updated.(Model)

	if result.phase != PhaseXFSearch {
		t.Fatalf("expected PhaseXFSearch on error, got %d", result.phase)
	}
	if result.xfErr == nil {
		t.Fatal("expected xfErr to be set")
	}
	if result.xfSearching {
		t.Fatal("expected xfSearching=false after error")
	}
}

func TestXFResultsNavigation(t *testing.T) {
	t.Parallel()
	m := New("test-session", testCommands)
	m.phase = PhaseXFResults
	m.xfResults = []tools.XFSearchResult{
		{ID: "1", Content: "First"},
		{ID: "2", Content: "Second"},
		{ID: "3", Content: "Third"},
	}
	m.xfCursor = 0

	// Navigate down
	updated, _ := m.updateXFResultsPhase(tea.KeyMsg{Type: tea.KeyDown})
	result := updated.(Model)
	if result.xfCursor != 1 {
		t.Fatalf("expected cursor 1 after down, got %d", result.xfCursor)
	}

	// Navigate down again
	m.xfCursor = 1
	updated, _ = m.updateXFResultsPhase(tea.KeyMsg{Type: tea.KeyDown})
	result = updated.(Model)
	if result.xfCursor != 2 {
		t.Fatalf("expected cursor 2 after second down, got %d", result.xfCursor)
	}

	// Navigate up
	m.xfCursor = 2
	updated, _ = m.updateXFResultsPhase(tea.KeyMsg{Type: tea.KeyUp})
	result = updated.(Model)
	if result.xfCursor != 1 {
		t.Fatalf("expected cursor 1 after up, got %d", result.xfCursor)
	}

	// Don't go below 0
	m.xfCursor = 0
	updated, _ = m.updateXFResultsPhase(tea.KeyMsg{Type: tea.KeyUp})
	result = updated.(Model)
	if result.xfCursor != 0 {
		t.Fatalf("expected cursor 0 at top, got %d", result.xfCursor)
	}

	// Don't go past end
	m.xfCursor = 2
	updated, _ = m.updateXFResultsPhase(tea.KeyMsg{Type: tea.KeyDown})
	result = updated.(Model)
	if result.xfCursor != 2 {
		t.Fatalf("expected cursor 2 at bottom, got %d", result.xfCursor)
	}
}

func TestXFResultsSelectSetsPrompt(t *testing.T) {
	t.Parallel()
	m := New("test-session", testCommands)
	m.phase = PhaseXFResults
	m.xfResults = []tools.XFSearchResult{
		{ID: "tweet-42", Content: "Go concurrency is great", CreatedAt: "2024-03-10", Type: "tweet", Score: 0.92},
	}
	m.xfCursor = 0

	// Select result
	updated, _ := m.updateXFResultsPhase(tea.KeyMsg{Type: tea.KeyEnter})
	result := updated.(Model)

	if result.phase != PhaseTarget {
		t.Fatalf("expected PhaseTarget after select, got %d", result.phase)
	}
	if result.selected == nil {
		t.Fatal("expected selected to be set")
	}
	if result.selected.Key != "xf-result" {
		t.Fatalf("expected key 'xf-result', got %q", result.selected.Key)
	}
	if !strings.Contains(result.selected.Prompt, "Go concurrency is great") {
		t.Fatalf("expected prompt to contain tweet content, got %q", result.selected.Prompt)
	}
	if !strings.Contains(result.selected.Prompt, "tweet-42") {
		t.Fatalf("expected prompt to contain tweet ID, got %q", result.selected.Prompt)
	}
}

func TestXFResultsBackReturnsToSearch(t *testing.T) {
	t.Parallel()
	m := New("test-session", testCommands)
	m.phase = PhaseXFResults
	m.xfResults = []tools.XFSearchResult{
		{ID: "1", Content: "Test"},
	}

	updated, _ := m.updateXFResultsPhase(tea.KeyMsg{Type: tea.KeyEscape})
	result := updated.(Model)

	if result.phase != PhaseXFSearch {
		t.Fatalf("expected PhaseXFSearch after Esc, got %d", result.phase)
	}
}

func TestFormatXFResultPrompt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		result    tools.XFSearchResult
		wantParts []string
	}{
		{
			name: "full result",
			result: tools.XFSearchResult{
				ID:        "tweet-123",
				Content:   "Error handling in Go",
				CreatedAt: "2024-01-15",
				Type:      "tweet",
				Score:     0.95,
			},
			wantParts: []string{"Error handling in Go", "2024-01-15", "tweet-123", "tweet", "0.95"},
		},
		{
			name: "minimal result",
			result: tools.XFSearchResult{
				Content: "Just content",
			},
			wantParts: []string{"Just content"},
		},
		{
			name: "no score",
			result: tools.XFSearchResult{
				ID:      "dm-456",
				Content: "DM content",
				Type:    "dm",
			},
			wantParts: []string{"DM content", "dm-456", "dm"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := formatXFResultPrompt(tc.result)
			for _, want := range tc.wantParts {
				if !strings.Contains(got, want) {
					t.Errorf("expected prompt to contain %q, got %q", want, got)
				}
			}
		})
	}
}

func TestXFSearchViewRendering(t *testing.T) {
	t.Parallel()
	m := New("test-session", testCommands)
	m.width = 80
	m.height = 24
	m.enterXFSearch()

	view := m.viewXFSearchPhase()
	plain := stripANSI(view)

	if !strings.Contains(plain, "XF Archive Search") {
		t.Error("expected XF Archive Search header in view")
	}
	if !strings.Contains(plain, "enter: search") {
		t.Error("expected help text in view")
	}
}

func TestXFResultsViewRendering(t *testing.T) {
	t.Parallel()
	m := New("test-session", testCommands)
	m.width = 80
	m.height = 24
	m.phase = PhaseXFResults
	m.xfResults = []tools.XFSearchResult{
		{ID: "tweet-1", Content: "First tweet about Go", CreatedAt: "2024-01-15", Score: 0.95},
		{ID: "tweet-2", Content: "Second tweet about Rust", CreatedAt: "2024-02-20", Score: 0.88},
	}
	m.xfCursor = 0
	m.xfQuery = initXFQuery(m.theme)
	m.xfQuery.SetValue("test query")

	view := m.viewXFResultsPhase()
	plain := stripANSI(view)

	if !strings.Contains(plain, "2 results") {
		t.Error("expected result count in view")
	}
	if !strings.Contains(plain, "First tweet about Go") {
		t.Error("expected first result content in view")
	}
	if !strings.Contains(plain, "enter: send to agent") {
		t.Error("expected help text in view")
	}
}

func TestXFSearchQuitFromSearchPhase(t *testing.T) {
	t.Parallel()
	m := New("test-session", testCommands)
	m.enterXFSearch()

	updated, cmd := m.updateXFSearchPhase(tea.KeyMsg{Type: tea.KeyCtrlC})
	result := updated.(Model)

	if !result.quitting {
		t.Fatal("expected quitting=true after ctrl+c")
	}
	if cmd == nil {
		t.Fatal("expected tea.Quit command")
	}
}

func TestXFSearchQuitFromResultsPhase(t *testing.T) {
	t.Parallel()
	m := New("test-session", testCommands)
	m.phase = PhaseXFResults
	m.xfResults = []tools.XFSearchResult{{ID: "1", Content: "Test"}}

	updated, cmd := m.updateXFResultsPhase(tea.KeyMsg{Type: tea.KeyCtrlC})
	result := updated.(Model)

	if !result.quitting {
		t.Fatal("expected quitting=true after ctrl+c")
	}
	if cmd == nil {
		t.Fatal("expected tea.Quit command")
	}
}

// Verify that the Ctrl+K key binding is properly declared.
func TestXFSearchKeyBinding(t *testing.T) {
	t.Parallel()
	if !key.Matches(tea.KeyMsg{Type: tea.KeyCtrlK}, commandKeys.XFSearch) {
		t.Fatal("expected Ctrl+K to match XFSearch keybinding")
	}
}

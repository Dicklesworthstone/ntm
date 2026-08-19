package robot

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// appendHistoryEntries seeds history for tests one entry at a time (the
// batch writer was removed as dead production code).
func appendHistoryEntries(t *testing.T, entries []*history.HistoryEntry) {
	t.Helper()
	for _, entry := range entries {
		if err := history.Append(entry); err != nil {
			t.Fatalf("failed to write history: %v", err)
		}
	}
}

func TestGetHistoryPagination(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tempDir)

	session := "history-pagination-session"
	entries := []*history.HistoryEntry{
		history.NewEntry(session, []string{"1"}, "first", history.SourceCLI),
		history.NewEntry(session, []string{"1"}, "second", history.SourceCLI),
		history.NewEntry(session, []string{"1"}, "third", history.SourceCLI),
	}
	for _, entry := range entries {
		entry.SetSuccess()
	}

	appendHistoryEntries(t, entries)

	output, err := GetHistory(HistoryOptions{
		Session: session,
		Limit:   1,
		Offset:  1,
	})
	if err != nil {
		t.Fatalf("GetHistory failed: %v", err)
	}

	if output.Pagination == nil {
		t.Fatal("expected pagination info, got nil")
	}
	if output.Pagination.Total != 3 || output.Pagination.Count != 1 {
		t.Fatalf("unexpected pagination info: %+v", output.Pagination)
	}
	if !output.Pagination.HasMore || output.Pagination.NextCursor == nil || *output.Pagination.NextCursor != 2 {
		t.Fatalf("expected next_cursor=2 and has_more=true, got %+v", output.Pagination)
	}
	if output.AgentHints == nil || output.AgentHints.NextOffset == nil || *output.AgentHints.NextOffset != 2 {
		t.Fatalf("expected _agent_hints.next_offset=2, got %+v", output.AgentHints)
	}
	if output.AgentHints.PagesRemaining == nil || *output.AgentHints.PagesRemaining != 1 {
		t.Fatalf("expected _agent_hints.pages_remaining=1, got %+v", output.AgentHints)
	}
	if output.AgentHints.Summary != "Showing 1 of 3 commands" {
		t.Fatalf("expected paginated summary to reflect page size, got %q", output.AgentHints.Summary)
	}
	if len(output.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(output.Entries))
	}
	if output.Entries[0].Prompt != "second" {
		t.Fatalf("expected second entry, got %q", output.Entries[0].Prompt)
	}
}

func TestGetHistoryPagination_WithFiltersSummarizesFilteredAndTotal(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tempDir)

	session := "history-pagination-filtered-session"
	entries := []*history.HistoryEntry{
		history.NewEntry(session, []string{"1"}, "claude 1", history.SourceCLI),
		history.NewEntry(session, []string{"1"}, "claude 2", history.SourceCLI),
		history.NewEntry(session, []string{"2"}, "codex 1", history.SourceCLI),
		history.NewEntry(session, []string{"2"}, "codex 2", history.SourceCLI),
	}
	for i, entry := range entries {
		if i < 2 {
			entry.SetAgentTypes([]string{"cc"})
		} else {
			entry.SetAgentTypes([]string{"cod"})
		}
		entry.SetSuccess()
	}

	appendHistoryEntries(t, entries)

	output, err := GetHistory(HistoryOptions{
		Session:   session,
		AgentType: "claude",
		Limit:     1,
	})
	if err != nil {
		t.Fatalf("GetHistory failed: %v", err)
	}

	if output.AgentHints == nil {
		t.Fatal("expected agent hints, got nil")
	}
	if output.AgentHints.Summary != "Showing 1 of 2 commands (from 4 total)" {
		t.Fatalf("expected filtered paginated summary, got %q", output.AgentHints.Summary)
	}
}

func TestGetHistoryFiltersByPersistedAgentTypes(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tempDir)

	session := "history-agent-filter-session"
	claudeEntry := history.NewEntry(session, []string{"1"}, "claude prompt", history.SourceCLI)
	claudeEntry.SetAgentTypes([]string{"cc"})
	claudeEntry.SetSuccess()

	codexEntry := history.NewEntry(session, []string{"2"}, "codex prompt", history.SourceCLI)
	codexEntry.SetAgentTypes([]string{"cod"})
	codexEntry.SetSuccess()

	appendHistoryEntries(t, []*history.HistoryEntry{claudeEntry, codexEntry})

	output, err := GetHistory(HistoryOptions{
		Session:   session,
		AgentType: "claude",
	})
	if err != nil {
		t.Fatalf("GetHistory failed: %v", err)
	}

	if output.Filtered != 1 {
		t.Fatalf("expected 1 filtered entry, got %d", output.Filtered)
	}
	if len(output.Entries) != 1 || output.Entries[0].Prompt != "claude prompt" {
		t.Fatalf("expected claude-only entry, got %+v", output.Entries)
	}
}

func TestGetHistoryStatsHonorsFilters(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tempDir)

	session := "history-stats-filter-session"
	oldEntry := history.NewEntry(session, []string{"1"}, "old prompt", history.SourceCLI)
	oldEntry.SetAgentTypes([]string{"cc"})
	oldEntry.Timestamp = oldEntry.Timestamp.Add(-2 * time.Hour)
	oldEntry.SetSuccess()

	recentFailure := history.NewEntry(session, []string{"2"}, "recent codex prompt", history.SourceCLI)
	recentFailure.SetAgentTypes([]string{"cod"})
	recentFailure.SetError(assertAnError("send failed"))

	appendHistoryEntries(t, []*history.HistoryEntry{oldEntry, recentFailure})

	output, err := GetHistory(HistoryOptions{
		Session:   session,
		AgentType: "codex",
		Since:     "1h",
		Stats:     true,
	})
	if err != nil {
		t.Fatalf("GetHistory failed: %v", err)
	}

	if output.Stats == nil {
		t.Fatal("expected stats output")
	}
	if output.Stats.TotalEntries != 1 || output.Stats.FailureCount != 1 || output.Stats.SuccessCount != 0 {
		t.Fatalf("expected filtered stats for one recent codex failure, got %+v", output.Stats)
	}
	if output.Filtered != 1 {
		t.Fatalf("expected filtered count 1, got %d", output.Filtered)
	}
}

func TestHistoryPaneFilterAliasesResolveCanonicalTopology(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%11", WindowIndex: 0, Index: 1, Title: "proj__cc_1"},
		{ID: "%20", WindowIndex: 1, Index: 0, Title: "proj__cod_1"},
		{ID: "%21", WindowIndex: 1, Index: 1, Title: "proj__gmi_1"},
	}

	aliases, err := resolveHistoryPaneFilterAliases(panes, "0.1")
	if err != nil {
		t.Fatalf("resolveHistoryPaneFilterAliases() error = %v", err)
	}
	entry := history.NewEntry("proj", []string{"0.1"}, "prompt", history.SourceCLI)
	if !historyEntryMatchesPaneFilter(*entry, aliases) {
		t.Fatal("pane aliases must match history entries stored by canonical address")
	}
	wrongEntry := history.NewEntry("proj", []string{"1"}, "wrong prompt", history.SourceCLI)
	if historyEntryMatchesPaneFilter(*wrongEntry, aliases) {
		t.Fatal("multi-window aliases must not over-match a bare pane index")
	}

	idAliases, err := resolveHistoryPaneFilterAliases(panes, "%11")
	if err != nil {
		t.Fatalf("ID selector error = %v", err)
	}
	if _, ok := idAliases["0.1"]; !ok {
		t.Fatalf("ID selector aliases = %v, want canonical physical address", idAliases)
	}

	windowAliases, err := resolveHistoryPaneFilterAliases(panes, "1")
	if err != nil {
		t.Fatalf("window selector error = %v", err)
	}
	for _, want := range []string{"1.0", "1.1", "%20", "%21"} {
		if _, ok := windowAliases[want]; !ok {
			t.Fatalf("window selector aliases = %v, missing %q", windowAliases, want)
		}
	}

	if _, err := resolveHistoryPaneFilterAliases(panes, "9.0"); err == nil || paneSelectorRobotErrorCode(err) != ErrCodePaneNotFound {
		t.Fatalf("missing selector error = %v, code = %q", err, paneSelectorRobotErrorCode(err))
	}
	if _, err := resolveHistoryPaneFilterAliases(panes, "proj__cc_1"); err == nil || paneSelectorRobotErrorCode(err) != ErrCodeInvalidFlag {
		t.Fatalf("title selector error = %v, code = %q", err, paneSelectorRobotErrorCode(err))
	}
}

type historyTestError string

func (e historyTestError) Error() string { return string(e) }

func assertAnError(msg string) error {
	return historyTestError(msg)
}

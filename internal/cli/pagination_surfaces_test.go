package cli

// pagination_surfaces_test.go is the D1 behavioral proof
// (bd-ws3-contract-breadth-psvyu.1) for the five unbounded list surfaces:
// sessions, history, audit, checkpoints, approvals. Each surface is driven
// over a fixture with MORE rows than two pages and must show:
//   - the first page holds EXACTLY limit rows with has_more=true
//   - following _agent_hints.next_offset yields the remainder
//   - pages are pairwise DISJOINT and their union is COMPLETE and in order
//   - has_more flips to false only on the final page
// A surface that grows the pagination fields but still returns everything
// (has_more always false, oversized pages) fails these assertions.

import (
	"fmt"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

const (
	pagingFixtureRows = 25
	pagingLimit       = 10
)

// pageResult captures one page in surface-agnostic form for the shared
// pagination assertions.
type pageResult struct {
	ids        []string
	count      int
	total      int
	hasMore    bool
	nextOffset *int
}

// assertPagingContract drives fetch(offset) from offset 0 following
// next_offset hints and checks the full D1 behavioral contract.
func assertPagingContract(t *testing.T, surface string, fetch func(offset int) pageResult, wantIDs []string) {
	t.Helper()

	total := len(wantIDs)
	wantPages := (total + pagingLimit - 1) / pagingLimit
	if wantPages < 3 {
		t.Fatalf("%s: fixture must span >2 pages, got %d", surface, wantPages)
	}

	seen := make(map[string]int)
	var union []string
	offset := 0
	for pageNo := 0; ; pageNo++ {
		if pageNo > wantPages {
			t.Fatalf("%s: pagination did not terminate after %d pages", surface, pageNo)
		}
		page := fetch(offset)

		if page.total != total {
			t.Errorf("%s page %d: total=%d want %d", surface, pageNo, page.total, total)
		}
		if page.count != len(page.ids) {
			t.Errorf("%s page %d: count=%d but %d rows returned", surface, pageNo, page.count, len(page.ids))
		}
		wantSize := pagingLimit
		if remaining := total - pageNo*pagingLimit; remaining < wantSize {
			wantSize = remaining
		}
		if len(page.ids) != wantSize {
			t.Errorf("%s page %d: page size=%d want EXACTLY %d", surface, pageNo, len(page.ids), wantSize)
		}

		for _, id := range page.ids {
			if prev, dup := seen[id]; dup {
				t.Errorf("%s page %d: row %q already returned on page %d (pages must be disjoint)", surface, pageNo, id, prev)
			}
			seen[id] = pageNo
		}
		union = append(union, page.ids...)

		lastPage := pageNo == wantPages-1
		if page.hasMore == lastPage {
			t.Errorf("%s page %d: has_more=%v want %v", surface, pageNo, page.hasMore, !lastPage)
		}
		if lastPage {
			if page.nextOffset != nil {
				t.Errorf("%s final page: next_offset must be absent, got %d", surface, *page.nextOffset)
			}
			break
		}
		if page.nextOffset == nil {
			t.Fatalf("%s page %d: has_more=true but _agent_hints.next_offset missing", surface, pageNo)
		}
		wantNext := (pageNo + 1) * pagingLimit
		if *page.nextOffset != wantNext {
			t.Errorf("%s page %d: next_offset=%d want %d", surface, pageNo, *page.nextOffset, wantNext)
		}
		offset = *page.nextOffset
	}

	if len(union) != total {
		t.Fatalf("%s: union of pages has %d rows, want %d (must be complete)", surface, len(union), total)
	}
	for i, id := range union {
		if id != wantIDs[i] {
			t.Errorf("%s: union[%d]=%q want %q (ordering must be stable across pages)", surface, i, id, wantIDs[i])
			break
		}
	}
}

func hintOffset(h *robot.PaginationAgentHints) *int {
	if h == nil {
		return nil
	}
	return h.NextOffset
}

// --- sessions (`ntm list --limit/--offset`) --------------------------------

func TestSessionListPaginationBehavior(t *testing.T) {
	items := make([]output.SessionListItem, pagingFixtureRows)
	want := make([]string, pagingFixtureRows)
	for i := range items {
		name := fmt.Sprintf("proj__lane%02d", i)
		items[i] = output.SessionListItem{Name: name, BaseProject: "proj"}
		want[i] = name
	}

	assertPagingContract(t, "sessions", func(offset int) pageResult {
		resp := output.ListResponse{
			TimestampedResponse: output.NewTimestamped(),
			Sessions:            append([]output.SessionListItem(nil), items...),
			Count:               len(items),
			TotalMatches:        len(items),
		}
		paginateSessionList(&resp, pagingLimit, offset)
		ids := make([]string, len(resp.Sessions))
		for i, s := range resp.Sessions {
			ids[i] = s.Name
		}
		return pageResult{
			ids:        ids,
			count:      resp.Count,
			total:      resp.TotalMatches,
			hasMore:    resp.HasMore,
			nextOffset: hintOffset(resp.AgentHints),
		}
	}, want)
}

// --- history (`ntm history --limit/--offset`, offset from newest) -----------

func TestHistoryListPaginationBehavior(t *testing.T) {
	entries := make([]history.HistoryEntry, pagingFixtureRows)
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for i := range entries {
		entries[i] = history.HistoryEntry{
			ID:        fmt.Sprintf("h%02d", i),
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			Session:   "proj",
		}
	}
	// Offset counts back from the newest entry: page 0 is the newest 10
	// (chronological within the page), then older pages follow.
	var want []string
	for start := pagingFixtureRows - pagingLimit; ; start -= pagingLimit {
		s := start
		if s < 0 {
			s = 0
		}
		end := start + pagingLimit
		if end > pagingFixtureRows {
			end = pagingFixtureRows
		}
		for i := s; i < end; i++ {
			want = append(want, entries[i].ID)
		}
		if s == 0 {
			break
		}
	}

	assertPagingContract(t, "history", func(offset int) pageResult {
		page, info := paginateHistoryTail(entries, pagingLimit, offset)
		result := &HistoryListResult{
			Entries:    page,
			TotalCount: len(entries),
			Showing:    len(page),
			HasMore:    info.HasMore,
			Pagination: info,
			AgentHints: robot.PaginationHints(info),
		}
		ids := make([]string, len(result.Entries))
		for i, e := range result.Entries {
			ids[i] = e.ID
		}
		return pageResult{
			ids:        ids,
			count:      info.Count,
			total:      info.Total,
			hasMore:    result.HasMore,
			nextOffset: hintOffset(result.AgentHints),
		}
	}, want)
}

// --- audit (`ntm audit show/search --limit/--offset`) ------------------------

func TestAuditQueryPaginationBehavior(t *testing.T) {
	entries := make([]audit.AuditEntry, pagingFixtureRows)
	want := make([]string, pagingFixtureRows)
	for i := range entries {
		target := fmt.Sprintf("pane_%02d", i)
		entries[i] = audit.AuditEntry{
			Timestamp:   time.Date(2026, 8, 1, 0, i, 0, 0, time.UTC),
			SessionID:   "proj",
			EventType:   audit.EventTypeCommand,
			Actor:       audit.ActorUser,
			Target:      target,
			SequenceNum: uint64(i + 1),
		}
		want[i] = target
	}
	full := &audit.QueryResult{
		Entries:    entries,
		TotalCount: len(entries),
		Scanned:    len(entries),
	}

	assertPagingContract(t, "audit", func(offset int) pageResult {
		out := buildAuditQueryOutput(full, pagingLimit, offset)
		ids := make([]string, len(out.Entries))
		for i, e := range out.Entries {
			ids[i] = e.Target
		}
		return pageResult{
			ids:        ids,
			count:      out.Count,
			total:      out.TotalMatches,
			hasMore:    out.HasMore,
			nextOffset: hintOffset(out.AgentHints),
		}
	}, want)
}

// TestAuditQueryPaginationEndToEnd drives the real searcher over on-disk
// fixture logs spanning >2 pages, proving the wired surface (searcher +
// envelope) pages correctly, not just the pure builder.
func TestAuditQueryPaginationEndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestAuditLog(t, tmpDir, "paging_e2e", pagingFixtureRows)
	searcher := audit.NewSearcherWithPath(tmpDir)

	result, err := searcher.Search(audit.Query{Sessions: []string{"paging_e2e"}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(result.Entries) != pagingFixtureRows {
		t.Fatalf("fixture: got %d entries, want %d", len(result.Entries), pagingFixtureRows)
	}
	want := make([]string, len(result.Entries))
	for i, e := range result.Entries {
		want[i] = fmt.Sprintf("%d", e.SequenceNum)
	}

	assertPagingContract(t, "audit-e2e", func(offset int) pageResult {
		res, err := searcher.Search(audit.Query{Sessions: []string{"paging_e2e"}})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		out := buildAuditQueryOutput(res, pagingLimit, offset)
		ids := make([]string, len(out.Entries))
		for i, e := range out.Entries {
			ids[i] = fmt.Sprintf("%d", e.SequenceNum)
		}
		return pageResult{
			ids:        ids,
			count:      out.Count,
			total:      out.TotalMatches,
			hasMore:    out.HasMore,
			nextOffset: hintOffset(out.AgentHints),
		}
	}, want)
}

// --- checkpoints (`ntm checkpoint list <session> --limit/--offset`) ----------

func TestCheckpointListPaginationBehavior(t *testing.T) {
	cps := make([]*checkpoint.Checkpoint, pagingFixtureRows)
	want := make([]string, pagingFixtureRows)
	for i := range cps {
		id := fmt.Sprintf("cp-%02d", i)
		cps[i] = &checkpoint.Checkpoint{ID: id, SessionName: "proj"}
		want[i] = id
	}

	assertPagingContract(t, "checkpoints", func(offset int) pageResult {
		out := buildCheckpointListOutput("proj", cps, nil, false, pagingLimit, offset)
		ids := make([]string, len(out.Checkpoints))
		for i, cp := range out.Checkpoints {
			ids[i] = cp.ID
		}
		return pageResult{
			ids:        ids,
			count:      out.Count,
			total:      out.TotalMatches,
			hasMore:    out.HasMore,
			nextOffset: hintOffset(out.AgentHints),
		}
	}, want)
}

func TestCheckpointSessionsPaginationBehavior(t *testing.T) {
	rows := make([]CheckpointSessionInfo, pagingFixtureRows)
	want := make([]string, pagingFixtureRows)
	for i := range rows {
		name := fmt.Sprintf("sess-%02d", i)
		rows[i] = CheckpointSessionInfo{Session: name}
		want[i] = name
	}

	assertPagingContract(t, "checkpoint-sessions", func(offset int) pageResult {
		out := buildCheckpointSessionsOutput(rows, pagingLimit, offset)
		ids := make([]string, len(out.Sessions))
		for i, row := range out.Sessions {
			ids[i] = row.Session
		}
		return pageResult{
			ids:        ids,
			count:      out.Count,
			total:      out.TotalMatches,
			hasMore:    out.HasMore,
			nextOffset: hintOffset(out.AgentHints),
		}
	}, want)
}

// --- approvals (`ntm approve list --limit/--offset`) -------------------------

func TestApprovalsListPaginationBehavior(t *testing.T) {
	pending := make([]state.Approval, pagingFixtureRows)
	want := make([]string, pagingFixtureRows)
	for i := range pending {
		id := fmt.Sprintf("appr-%02d", i)
		pending[i] = state.Approval{ID: id, Action: "force-release"}
		want[i] = id
	}

	assertPagingContract(t, "approvals", func(offset int) pageResult {
		out := buildApprovalsListOutput(pending, pagingLimit, offset)
		ids := make([]string, len(out.Pending))
		for i, a := range out.Pending {
			ids[i] = a.ID
		}
		return pageResult{
			ids:        ids,
			count:      out.Count,
			total:      out.TotalMatches,
			hasMore:    out.HasMore,
			nextOffset: hintOffset(out.AgentHints),
		}
	}, want)
}

// --- degenerate cases --------------------------------------------------------

// TestPaginationUnrequestedReturnsEverything pins the back-compat default:
// no --limit/--offset means the full list with no pagination block.
func TestPaginationUnrequestedReturnsEverything(t *testing.T) {
	resp := output.ListResponse{
		Sessions: []output.SessionListItem{{Name: "a"}, {Name: "b"}},
	}
	paginateSessionList(&resp, 0, 0)
	if len(resp.Sessions) != 2 || resp.Count != 2 || resp.TotalMatches != 2 {
		t.Errorf("unpaginated list mutated: %+v", resp)
	}
	if resp.Pagination != nil || resp.HasMore || resp.AgentHints != nil {
		t.Errorf("unrequested pagination must not add continuation state: %+v", resp)
	}

	out := buildApprovalsListOutput([]state.Approval{{ID: "x"}}, 0, 0)
	if out.Count != 1 || out.TotalMatches != 1 || out.HasMore || out.Pagination != nil {
		t.Errorf("unpaginated approvals envelope wrong: %+v", out)
	}
}

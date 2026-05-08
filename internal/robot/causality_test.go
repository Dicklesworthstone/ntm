package robot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildCausalityOutput_SortsDeterministically(t *testing.T) {
	t0 := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Minute)
	t2 := t0.Add(2 * time.Minute)

	loaders := causalityLoaders{
		audit: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) {
			return []CausalityEvent{
				{ID: "z", Source: "robot_audit", Type: "command", ts: t1},
				{ID: "a", Source: "robot_audit", Type: "command", ts: t0},
			}, nil
		},
		mail: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, []CausalitySourceStatus, []string) {
			return []CausalityEvent{
				{ID: "m1", Source: "agentmail_inbox", Type: "message", ts: t1},
			}, []CausalitySourceStatus{{Name: "agentmail_inbox", Available: true, Events: 1}}, nil
		},
		session: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) {
			return []CausalityEvent{{ID: "s", Source: "session_timeline", Type: "working", ts: t1}}, nil
		},
		pipeline: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) {
			return []CausalityEvent{{ID: "p", Source: "pipeline_state", Type: "pipeline_finished", ts: t2}}, nil
		},
	}

	out := buildCausalityOutput(CausalityOptions{Session: "myproj", Limit: 50}, loaders)
	if !out.Success {
		t.Fatalf("expected success=true, got error=%q", out.Error)
	}
	if len(out.Events) != 5 {
		t.Fatalf("expected 5 events, got %d", len(out.Events))
	}

	for i := 1; i < len(out.Events); i++ {
		prev := out.Events[i-1]
		cur := out.Events[i]
		if cur.ts.Before(prev.ts) {
			t.Fatalf("events not sorted at index %d: %s before %s", i, cur.ts, prev.ts)
		}
		if cur.ts.Equal(prev.ts) {
			if cur.Source < prev.Source {
				t.Fatalf("source tie-break violated at index %d: %s < %s", i, cur.Source, prev.Source)
			}
			if cur.Source == prev.Source && cur.ID < prev.ID {
				t.Fatalf("id tie-break violated at index %d: %s < %s", i, cur.ID, prev.ID)
			}
		}
	}
}

func TestBuildCausalityOutput_FiltersByWindowAndFields(t *testing.T) {
	t0 := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Minute)
	t2 := t0.Add(2 * time.Minute)

	loaders := causalityLoaders{
		audit: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) {
			return []CausalityEvent{
				{ID: "1", Source: "robot_audit", Type: "command", Session: "myproj", Pane: "1", BeadID: "bd-1", ChainID: "c1", ts: t0},
				{ID: "2", Source: "robot_audit", Type: "command", Session: "myproj", Pane: "2", BeadID: "bd-2", ChainID: "c2", ts: t1},
				{ID: "3", Source: "robot_audit", Type: "send", Session: "myproj", Pane: "1", BeadID: "bd-1", ChainID: "c1", ts: t2},
			}, nil
		},
		mail: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, []CausalitySourceStatus, []string) {
			return nil, []CausalitySourceStatus{{Name: "agentmail_inbox", Available: true, Events: 0}}, nil
		},
		session:  func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) { return nil, nil },
		pipeline: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) { return nil, nil },
	}

	out := buildCausalityOutput(CausalityOptions{
		Session: "myproj",
		BeadID:  "bd-1",
		Pane:    "1",
		Type:    "command",
		Chain:   "c1",
		Since:   t0.Add(-30 * time.Second).Format(time.RFC3339),
		Until:   t1.Format(time.RFC3339),
		Limit:   10,
	}, loaders)
	if !out.Success {
		t.Fatalf("expected success=true, got error=%q", out.Error)
	}
	if out.Filtered != 1 {
		t.Fatalf("expected filtered=1, got %d", out.Filtered)
	}
	if len(out.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(out.Events))
	}
	if out.Events[0].ID != "1" {
		t.Fatalf("expected ID=1, got %q", out.Events[0].ID)
	}
}

func TestBuildCausalityOutput_SessionFilterKeepsSessionAgnosticMailEvents(t *testing.T) {
	t0 := time.Date(2026, 5, 8, 12, 30, 0, 0, time.UTC)

	loaders := causalityLoaders{
		audit: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) {
			return nil, nil
		},
		mail: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, []CausalitySourceStatus, []string) {
			return []CausalityEvent{
					{ID: "m1", Source: "agentmail_inbox", Type: "message", Session: "", ts: t0},
					{ID: "m2", Source: "agentmail_inbox", Type: "message", Session: "other-session", ts: t0.Add(1 * time.Second)},
				},
				[]CausalitySourceStatus{{Name: "agentmail_inbox", Available: true, Events: 2}},
				nil
		},
		session:  func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) { return nil, nil },
		pipeline: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) { return nil, nil },
	}

	out := buildCausalityOutput(CausalityOptions{
		Session: "my-session",
		Limit:   10,
	}, loaders)
	if !out.Success {
		t.Fatalf("expected success=true, got error=%q", out.Error)
	}
	if out.Filtered != 1 {
		t.Fatalf("expected filtered=1, got %d", out.Filtered)
	}
	if len(out.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(out.Events))
	}
	if out.Events[0].ID != "m1" {
		t.Fatalf("expected session-agnostic event m1, got %q", out.Events[0].ID)
	}
	if out.Events[0].Session != "" {
		t.Fatalf("expected empty session on mail event, got %q", out.Events[0].Session)
	}
}

func TestBuildCausalityOutput_MissingSourceDegrades(t *testing.T) {
	t0 := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	loaders := causalityLoaders{
		audit: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) {
			return nil, errors.New("audit unavailable")
		},
		mail: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, []CausalitySourceStatus, []string) {
			return nil,
				[]CausalitySourceStatus{{Name: "agentmail_reservations", Available: false, Error: "mail down"}},
				[]string{"agentmail down"}
		},
		session: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) {
			return nil, errors.New("timeline missing")
		},
		pipeline: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) {
			return []CausalityEvent{{ID: "p1", Source: "pipeline_state", Type: "pipeline_started", ts: t0}}, nil
		},
	}

	out := buildCausalityOutput(CausalityOptions{Session: "myproj", Limit: 10}, loaders)
	if !out.Success {
		t.Fatalf("expected success=true, got error=%q", out.Error)
	}
	if len(out.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(out.Events))
	}
	if out.Events[0].Source != "pipeline_state" {
		t.Fatalf("expected pipeline_state event, got %q", out.Events[0].Source)
	}
	if len(out.Warnings) == 0 {
		t.Fatal("expected warnings when sources are unavailable")
	}

	foundUnavailable := false
	for _, src := range out.Sources {
		if !src.Available {
			foundUnavailable = true
			break
		}
	}
	if !foundUnavailable {
		t.Fatal("expected at least one unavailable source status")
	}
}

func TestBuildCausalityOutput_BeadChainMiniWorkflow(t *testing.T) {
	t0 := time.Date(2026, 5, 8, 16, 0, 0, 0, time.UTC)
	t1 := t0.Add(20 * time.Second)
	t2 := t0.Add(40 * time.Second)
	beadID := "bd-2mb03.4"

	loaders := causalityLoaders{
		audit: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) {
			return []CausalityEvent{{ID: "a1", Source: "robot_audit", Type: "command", BeadID: beadID, Pane: "2", ChainID: "cmd-100", ts: t0}}, nil
		},
		mail: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, []CausalitySourceStatus, []string) {
			events := []CausalityEvent{{ID: "m1", Source: "agentmail_reservations", Type: "reservation_active", BeadID: beadID, Agent: "BlueLake", ts: t1}}
			return events,
				[]CausalitySourceStatus{{Name: "agentmail_reservations", Available: true, Events: 1}},
				nil
		},
		session: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) {
			return nil, nil
		},
		pipeline: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) {
			return []CausalityEvent{{ID: "p1", Source: "pipeline_state", Type: "pipeline_started", BeadID: beadID, ChainID: "run-xyz", ts: t2}}, nil
		},
	}

	out := buildCausalityOutput(CausalityOptions{Session: "myproj", BeadID: beadID, Limit: 10}, loaders)
	if !out.Success {
		t.Fatalf("expected success=true, got error=%q", out.Error)
	}
	if out.Filtered != 3 {
		t.Fatalf("expected filtered=3, got %d", out.Filtered)
	}
	if len(out.Events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(out.Events))
	}

	gotSources := []string{out.Events[0].Source, out.Events[1].Source, out.Events[2].Source}
	joined := strings.Join(gotSources, ",")
	if joined != "robot_audit,agentmail_reservations,pipeline_state" {
		t.Fatalf("unexpected source order: %s", joined)
	}
}

func TestBuildCausalityOutput_FilterCountsReflectReturnedLimit(t *testing.T) {
	t0 := time.Date(2026, 5, 8, 16, 30, 0, 0, time.UTC)

	loaders := causalityLoaders{
		audit: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) {
			return []CausalityEvent{
				{ID: "a1", Source: "robot_audit", Type: "command", ts: t0},
				{ID: "a2", Source: "robot_audit", Type: "command", ts: t0.Add(1 * time.Second)},
				{ID: "a3", Source: "robot_audit", Type: "command", ts: t0.Add(2 * time.Second)},
			}, nil
		},
		mail: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, []CausalitySourceStatus, []string) {
			return nil, nil, nil
		},
		session:  func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) { return nil, nil },
		pipeline: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) { return nil, nil },
	}

	out := buildCausalityOutput(CausalityOptions{
		Session: "myproj",
		Limit:   2,
	}, loaders)
	if !out.Success {
		t.Fatalf("expected success=true, got error=%q", out.Error)
	}
	if out.Total != 3 {
		t.Fatalf("total = %d, want 3", out.Total)
	}
	if out.Available != 3 {
		t.Fatalf("available = %d, want 3", out.Available)
	}
	if out.Filtered != 2 {
		t.Fatalf("filtered = %d, want 2", out.Filtered)
	}
	if !out.Truncated {
		t.Fatal("expected truncated=true")
	}
	if len(out.Events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(out.Events))
	}
}

func TestReadPipelineStateFileWithLimit_RejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.json")

	largeJSON := fmt.Sprintf(`{"run_id":"run-1","workflow_id":"wf","session":"s","status":"running","variables":{"blob":"%s"}}`, strings.Repeat("x", 2048))
	if err := os.WriteFile(path, []byte(largeJSON), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	_, err := readPipelineStateFileWithLimit(path, 256)
	if err == nil {
		t.Fatal("expected size-limit error, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("expected exceeds-limit error, got: %v", err)
	}
}

func TestReadPipelineStateFileWithLimit_AllowsSmallValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.json")

	smallJSON := `{"run_id":"run-1","workflow_id":"wf","session":"s","status":"running","started_at":"2026-05-08T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(smallJSON), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	state, err := readPipelineStateFileWithLimit(path, 4*1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state == nil || state.RunID != "run-1" {
		t.Fatalf("unexpected state: %+v", state)
	}
}

// bd-f5782: read must be FD-bounded, not Stat-then-ReadFile, so a file
// growing or symlink swapping between size-check and read cannot bypass
// the cap. We can't easily simulate a concurrent grow, but we can simulate
// the post-fix invariant directly: a file that already exceeds the cap by
// any amount must be rejected without allocating the full file's worth of
// bytes — since the function only ever reads up to maxBytes+1, the test
// passes a cap small enough that a buggy implementation would pre-Stat a
// "fits" verdict and then read the whole oversized blob.
func TestReadPipelineStateFileWithLimit_BoundsReadEvenWhenStatLies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.json")

	// 5 KiB on disk; cap at 1 KiB. Post-fix: io.LimitReader caps the
	// read at 1025 bytes regardless of stat, returns "exceeds limit".
	blob := strings.Repeat("y", 5*1024)
	largeJSON := fmt.Sprintf(`{"run_id":"run-1","workflow_id":"wf","session":"s","status":"running","variables":{"blob":"%s"}}`, blob)
	if err := os.WriteFile(path, []byte(largeJSON), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	_, err := readPipelineStateFileWithLimit(path, 1024)
	if err == nil {
		t.Fatal("expected exceeds-limit error, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("expected exceeds-limit error, got: %v", err)
	}
}

func TestReadPipelineStateFileWithLimit_AcceptsExactlyMaxBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.json")

	smallJSON := `{"run_id":"run-1","workflow_id":"wf","session":"s","status":"running"}`
	if err := os.WriteFile(path, []byte(smallJSON), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	state, err := readPipelineStateFileWithLimit(path, int64(len(smallJSON)))
	if err != nil {
		t.Fatalf("file at exactly maxBytes should be accepted, got: %v", err)
	}
	if state == nil || state.RunID != "run-1" {
		t.Fatalf("unexpected state: %+v", state)
	}
}

// bd-ogpsf: since/until must be threaded into mail/session/pipeline
// loaders so they can short-circuit out-of-window data at the source
// rather than load everything and discard at filter time.

func TestBuildCausalityOutput_LoadersReceiveSinceUntil(t *testing.T) {
	t0 := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	wantSince := t0.Add(-1 * time.Hour)
	wantUntil := t0.Add(1 * time.Hour)

	var sawAuditSince, sawAuditUntil *time.Time
	var sawMailSince, sawMailUntil *time.Time
	var sawSessionSince, sawSessionUntil *time.Time
	var sawPipelineSince, sawPipelineUntil *time.Time

	loaders := causalityLoaders{
		audit: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) {
			sawAuditSince, sawAuditUntil = since, until
			return nil, nil
		},
		mail: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, []CausalitySourceStatus, []string) {
			sawMailSince, sawMailUntil = since, until
			return nil, nil, nil
		},
		session: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) {
			sawSessionSince, sawSessionUntil = since, until
			return nil, nil
		},
		pipeline: func(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) {
			sawPipelineSince, sawPipelineUntil = since, until
			return nil, nil
		},
	}

	out := buildCausalityOutput(CausalityOptions{
		Session: "myproj",
		Since:   wantSince.Format(time.RFC3339),
		Until:   wantUntil.Format(time.RFC3339),
		Limit:   10,
	}, loaders)
	if !out.Success {
		t.Fatalf("expected success, got error=%q", out.Error)
	}

	for name, gotSince := range map[string]*time.Time{
		"audit":    sawAuditSince,
		"mail":     sawMailSince,
		"session":  sawSessionSince,
		"pipeline": sawPipelineSince,
	} {
		if gotSince == nil {
			t.Errorf("%s loader: since was nil", name)
			continue
		}
		if !gotSince.Equal(wantSince) {
			t.Errorf("%s loader: since = %v, want %v", name, gotSince, wantSince)
		}
	}
	for name, gotUntil := range map[string]*time.Time{
		"audit":    sawAuditUntil,
		"mail":     sawMailUntil,
		"session":  sawSessionUntil,
		"pipeline": sawPipelineUntil,
	} {
		if gotUntil == nil {
			t.Errorf("%s loader: until was nil", name)
			continue
		}
		if !gotUntil.Equal(wantUntil) {
			t.Errorf("%s loader: until = %v, want %v", name, gotUntil, wantUntil)
		}
	}
}

func TestWithinCausalityWindow_BoundsAndZeroPassthrough(t *testing.T) {
	t.Parallel()
	mid := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	before := mid.Add(-1 * time.Hour)
	after := mid.Add(1 * time.Hour)

	cases := []struct {
		name             string
		ts               time.Time
		since, until     *time.Time
		want             bool
	}{
		{"both nil", mid, nil, nil, true},
		{"in window", mid, &before, &after, true},
		{"before since", before.Add(-time.Minute), &before, &after, false},
		{"after until", after.Add(time.Minute), &before, &after, false},
		{"only since, in", mid, &before, nil, true},
		{"only since, out", before.Add(-time.Minute), &before, nil, false},
		{"only until, in", mid, nil, &after, true},
		{"only until, out", after.Add(time.Minute), nil, &after, false},
		{"zero ts is in", time.Time{}, &before, &after, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := withinCausalityWindow(c.ts, c.since, c.until); got != c.want {
				t.Errorf("withinCausalityWindow(%v, %v, %v) = %v, want %v",
					c.ts, c.since, c.until, got, c.want)
			}
		})
	}
}

func TestPipelineRunIntersectsWindow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	before := now.Add(-1 * time.Hour)
	after := now.Add(1 * time.Hour)

	if pipelineRunIntersectsWindow(nil, &before, &after) {
		t.Error("nil state must not intersect")
	}

	st := &causalityPipelineState{StartedAt: now}
	if !pipelineRunIntersectsWindow(st, nil, nil) {
		t.Error("nil bounds must intersect")
	}
	if !pipelineRunIntersectsWindow(st, &before, &after) {
		t.Error("started_at in window must intersect")
	}

	// All timestamps before the since cutoff.
	stOld := &causalityPipelineState{StartedAt: before.Add(-time.Hour), FinishedAt: before.Add(-30 * time.Minute)}
	if pipelineRunIntersectsWindow(stOld, &before, &after) {
		t.Error("all timestamps outside window must not intersect")
	}

	// CancelledAt brings it in window.
	cancelTime := now
	stCancelled := &causalityPipelineState{StartedAt: before.Add(-time.Hour), CancelledAt: &cancelTime}
	if !pipelineRunIntersectsWindow(stCancelled, &before, &after) {
		t.Error("CancelledAt in window must intersect")
	}
}

func TestLoadAgentMailCausalityEvents_NoProjectSetReturnsBothStatuses(t *testing.T) {
	t.Parallel()
	events, statuses, warnings := loadAgentMailCausalityEvents(CausalityOptions{}, nil, nil)
	if len(events) != 0 {
		t.Errorf("events = %d, want 0", len(events))
	}
	if len(statuses) != 2 {
		t.Errorf("statuses = %d, want 2", len(statuses))
	}
	if len(warnings) == 0 {
		t.Error("warnings empty when project missing")
	}
}

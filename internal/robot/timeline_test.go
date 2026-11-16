package robot

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

func TestTimelineListShowStatsAndErrorCases(t *testing.T) {
	baseDir := t.TempDir()

	// 1. Empty timeline directory -> non-nil Timelines & Events
	emptyOut, err := GetTimeline(context.Background(), TimelineOptions{BaseDir: baseDir})
	if err != nil {
		t.Fatalf("GetTimeline(empty): %v", err)
	}
	if !emptyOut.Success || emptyOut.Action != "list" || emptyOut.SchemaID != "ntm:robot:timeline:v1" {
		t.Fatalf("unexpected empty timeline output: %+v", emptyOut)
	}
	if emptyOut.Timelines == nil || emptyOut.Events == nil || emptyOut.TotalCount != 0 || emptyOut.Showing != 0 {
		t.Fatalf("expected non-nil empty Timelines & Events, got %+v", emptyOut)
	}

	// Seed two persisted timelines
	persister, err := state.NewTimelinePersister(&state.TimelinePersistConfig{
		BaseDir:           baseDir,
		CompressOlderThan: -1,
	})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	baseTS := time.Date(2026, 11, 15, 14, 0, 0, 0, time.UTC)
	alphaEvents := []state.AgentEvent{
		{
			AgentID:   "cc_1",
			AgentType: state.AgentTypeClaude,
			SessionID: "alpha",
			State:     state.TimelineWorking,
			Timestamp: baseTS,
		},
		{
			AgentID:       "cc_1",
			AgentType:     state.AgentTypeClaude,
			SessionID:     "alpha",
			PreviousState: state.TimelineWorking,
			State:         state.TimelineIdle,
			Timestamp:     baseTS.Add(5 * time.Minute),
			Duration:      5 * time.Minute,
		},
		{
			AgentID:   "cod_1",
			AgentType: state.AgentTypeCodex,
			SessionID: "alpha",
			State:     state.TimelineError,
			Timestamp: baseTS.Add(8 * time.Minute),
		},
	}
	if err := persister.SaveTimeline("alpha", alphaEvents); err != nil {
		t.Fatalf("SaveTimeline(alpha): %v", err)
	}

	betaEvents := []state.AgentEvent{
		{
			AgentID:   "agy_1",
			AgentType: state.AgentTypeGemini,
			SessionID: "beta",
			State:     state.TimelineWorking,
			Timestamp: baseTS.Add(10 * time.Minute),
		},
	}
	if err := persister.SaveTimeline("beta", betaEvents); err != nil {
		t.Fatalf("SaveTimeline(beta): %v", err)
	}

	// 2. List with limit=1
	listed, err := GetTimeline(context.Background(), TimelineOptions{
		BaseDir: baseDir,
		Limit:   1,
	})
	if err != nil {
		t.Fatalf("GetTimeline(list): %v", err)
	}
	if !listed.Success || listed.Action != "list" || listed.TotalCount != 2 || listed.Showing != 1 || len(listed.Timelines) != 1 {
		t.Fatalf("unexpected listed output: %+v", listed)
	}

	// 3. Show without --timeline-events computes SessionStats while keeping Events empty []
	shownSummary, err := GetTimeline(context.Background(), TimelineOptions{
		Session:       "alpha",
		IncludeEvents: false,
		BaseDir:       baseDir,
	})
	if err != nil {
		t.Fatalf("GetTimeline(show summary): %v", err)
	}
	if !shownSummary.Success || shownSummary.Action != "show" || shownSummary.Info == nil || shownSummary.SessionStats == nil {
		t.Fatalf("unexpected show summary: %+v", shownSummary)
	}
	if shownSummary.Events == nil || len(shownSummary.Events) != 0 {
		t.Fatalf("expected non-nil empty Events when IncludeEvents=false, got %v", shownSummary.Events)
	}
	if shownSummary.SessionStats.TotalEvents != 3 || shownSummary.SessionStats.UniqueAgents != 2 || shownSummary.SessionStats.Duration != 8*time.Minute {
		t.Fatalf("unexpected SessionStats: %+v", shownSummary.SessionStats)
	}

	// 4. Show with --timeline-events includes all 3 state transitions
	shownFull, err := GetTimeline(context.Background(), TimelineOptions{
		Session:       "alpha",
		IncludeEvents: true,
		BaseDir:       baseDir,
	})
	if err != nil {
		t.Fatalf("GetTimeline(show full): %v", err)
	}
	if !shownFull.Success || len(shownFull.Events) != 3 {
		t.Fatalf("expected 3 events in show full, got %+v", shownFull)
	}

	// 5. Aggregate stats via Stats=true and Session="stats"
	statsOut, err := GetTimeline(context.Background(), TimelineOptions{
		Stats:   true,
		BaseDir: baseDir,
	})
	if err != nil {
		t.Fatalf("GetTimeline(stats): %v", err)
	}
	if !statsOut.Success || statsOut.Action != "stats" || statsOut.AggregateStats == nil {
		t.Fatalf("unexpected stats output: %+v", statsOut)
	}
	if statsOut.AggregateStats.TotalTimelines != 2 || statsOut.AggregateStats.TotalEvents != 4 || statsOut.AggregateStats.TotalAgents != 3 {
		t.Fatalf("unexpected aggregate stats: %+v", statsOut.AggregateStats)
	}

	statsViaSession, err := GetTimeline(context.Background(), TimelineOptions{
		Session: "stats",
		BaseDir: baseDir,
	})
	if err != nil || !statsViaSession.Success || statsViaSession.Action != "stats" {
		t.Fatalf("expected Session='stats' to infer action='stats', got (%+v, %v)", statsViaSession, err)
	}

	// 6. Error cases: missing session, path traversal, negative limit, cancelled context
	missing, _ := GetTimeline(context.Background(), TimelineOptions{
		Session: "missing-session",
		BaseDir: baseDir,
	})
	if missing.Success || missing.ErrorCode != ErrCodeSessionNotFound {
		t.Fatalf("expected SESSION_NOT_FOUND, got %+v", missing)
	}

	traversal, _ := GetTimeline(context.Background(), TimelineOptions{
		Session: "../bad",
		BaseDir: baseDir,
	})
	if traversal.Success || traversal.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG for path traversal, got %+v", traversal)
	}

	negLim, _ := GetTimeline(context.Background(), TimelineOptions{
		Limit:   -5,
		BaseDir: baseDir,
	})
	if negLim.Success || negLim.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG for negative limit, got %+v", negLim)
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelledOut, _ := GetTimeline(cancelledCtx, TimelineOptions{BaseDir: baseDir})
	if cancelledOut.Success || cancelledOut.ErrorCode != ErrCodeTimeout {
		t.Fatalf("expected TIMEOUT for cancelled context, got %+v", cancelledOut)
	}

	// Empty arrays marshal as [] in JSON
	rawJSON, err := json.Marshal(emptyOut)
	if err != nil {
		t.Fatalf("marshal emptyOut: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(rawJSON, &decoded); err != nil {
		t.Fatalf("unmarshal emptyOut: %v", err)
	}
	if _, ok := decoded["timelines"].([]interface{}); !ok {
		t.Fatalf("timelines marshaled as %T (%v), want JSON array", decoded["timelines"], decoded["timelines"])
	}
	if _, ok := decoded["events"].([]interface{}); !ok {
		t.Fatalf("events marshaled as %T (%v), want JSON array", decoded["events"], decoded["events"])
	}

	// Registry + Schema verification
	surface, ok := GetRobotRegistry().Surface("timeline")
	if !ok {
		t.Fatal("expected 'timeline' surface in GetRobotRegistry()")
	}
	if surface.SchemaID != "ntm:robot:timeline:v1" || surface.SchemaType != "timeline" || surface.Flag != "--robot-timeline" {
		t.Fatalf("unexpected timeline surface descriptor: %+v", surface)
	}

	schemaOut, err := GetSchema("timeline")
	if err != nil {
		t.Fatalf("GetSchema(timeline): %v", err)
	}
	if !schemaOut.Success || schemaOut.Schema == nil {
		t.Fatalf("expected valid JSON schema for timeline, got %+v", schemaOut)
	}
	for _, prop := range []string{"schema_id", "schema_version", "action", "session", "timelines", "total_count", "showing", "events"} {
		if _, exists := schemaOut.Schema.Properties[prop]; !exists {
			t.Fatalf("expected property %q in timeline schema", prop)
		}
	}
}

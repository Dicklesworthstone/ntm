package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewTimelinePersister(t *testing.T) {
	tmpDir := t.TempDir()
	config := &TimelinePersistConfig{
		BaseDir:      tmpDir,
		MaxTimelines: 10,
	}

	persister, err := NewTimelinePersister(config)
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	if persister.config.BaseDir != tmpDir {
		t.Errorf("Expected BaseDir %q, got %q", tmpDir, persister.config.BaseDir)
	}

	if persister.config.MaxTimelines != 10 {
		t.Errorf("Expected MaxTimelines 10, got %d", persister.config.MaxTimelines)
	}

	// Verify directory was created
	if _, err := os.Stat(tmpDir); os.IsNotExist(err) {
		t.Error("Expected base directory to be created")
	}
}

func TestSaveAndLoadTimeline(t *testing.T) {
	tmpDir := t.TempDir()
	config := &TimelinePersistConfig{BaseDir: tmpDir}

	persister, err := NewTimelinePersister(config)
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	sessionID := "test-session-1"
	now := time.Now()

	events := []AgentEvent{
		{
			AgentID:   "cc_1",
			AgentType: AgentTypeClaude,
			SessionID: sessionID,
			State:     TimelineWorking,
			Timestamp: now.Add(-5 * time.Minute),
			Details:   map[string]string{"task": "implementing feature"},
		},
		{
			AgentID:       "cc_1",
			AgentType:     AgentTypeClaude,
			SessionID:     sessionID,
			State:         TimelineIdle,
			PreviousState: TimelineWorking,
			Timestamp:     now.Add(-2 * time.Minute),
			Duration:      3 * time.Minute,
		},
		{
			AgentID:       "cc_1",
			AgentType:     AgentTypeClaude,
			SessionID:     sessionID,
			State:         TimelineWorking,
			PreviousState: TimelineIdle,
			Timestamp:     now,
			Duration:      2 * time.Minute,
		},
	}

	// Save
	t.Log("Saving timeline...")
	if err := persister.SaveTimeline(sessionID, events); err != nil {
		t.Fatalf("SaveTimeline failed: %v", err)
	}

	// Verify file exists
	path := filepath.Join(tmpDir, sessionID+".jsonl")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("Timeline file was not created")
	}

	// Load
	t.Log("Loading timeline...")
	loaded, err := persister.LoadTimeline(sessionID)
	if err != nil {
		t.Fatalf("LoadTimeline failed: %v", err)
	}

	if len(loaded) != len(events) {
		t.Fatalf("Expected %d events, got %d", len(events), len(loaded))
	}

	// Verify first event
	if loaded[0].AgentID != events[0].AgentID {
		t.Errorf("Expected AgentID %q, got %q", events[0].AgentID, loaded[0].AgentID)
	}
	if loaded[0].State != events[0].State {
		t.Errorf("Expected State %q, got %q", events[0].State, loaded[0].State)
	}

	t.Logf("PASS: Saved and loaded %d events successfully", len(loaded))
}

func TestSaveTimelineHeaderUsesMinMaxTimestamps(t *testing.T) {
	tmpDir := t.TempDir()
	config := &TimelinePersistConfig{BaseDir: tmpDir}

	persister, err := NewTimelinePersister(config)
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	sessionID := "header-minmax"
	now := time.Now()
	expectedFirst := now.Add(-2 * time.Minute)
	expectedLast := now

	// Intentionally unsorted timestamps.
	events := []AgentEvent{
		{AgentID: "cc_1", SessionID: sessionID, State: TimelineWorking, Timestamp: expectedLast},
		{AgentID: "cc_1", SessionID: sessionID, State: TimelineIdle, Timestamp: expectedFirst},
		{AgentID: "cc_1", SessionID: sessionID, State: TimelineWorking, Timestamp: now.Add(-1 * time.Minute)},
	}
	if err := persister.SaveTimeline(sessionID, events); err != nil {
		t.Fatalf("SaveTimeline failed: %v", err)
	}

	header, err := persister.readHeader(filepath.Join(tmpDir, sessionID+".jsonl"), false)
	if err != nil {
		t.Fatalf("readHeader failed: %v", err)
	}
	if header == nil {
		t.Fatalf("expected non-nil header")
	}
	if !header.FirstEvent.Equal(expectedFirst) {
		t.Fatalf("expected FirstEvent=%v, got %v", expectedFirst, header.FirstEvent)
	}
	if !header.LastEvent.Equal(expectedLast) {
		t.Fatalf("expected LastEvent=%v, got %v", expectedLast, header.LastEvent)
	}
}

func TestLoadNonExistentTimeline(t *testing.T) {
	tmpDir := t.TempDir()
	config := &TimelinePersistConfig{BaseDir: tmpDir}

	persister, err := NewTimelinePersister(config)
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	events, err := persister.LoadTimeline("nonexistent")
	if err != nil {
		t.Fatalf("LoadTimeline should not error for nonexistent: %v", err)
	}

	if events != nil {
		t.Error("Expected nil events for nonexistent timeline")
	}

	t.Log("PASS: Correctly returned nil for nonexistent timeline")
}

func TestListTimelines(t *testing.T) {
	tmpDir := t.TempDir()
	config := &TimelinePersistConfig{BaseDir: tmpDir}

	persister, err := NewTimelinePersister(config)
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	// Create multiple timelines
	sessions := []string{"session-a", "session-b", "session-c"}
	for _, sessionID := range sessions {
		events := []AgentEvent{
			{
				AgentID:   "cc_1",
				SessionID: sessionID,
				State:     TimelineWorking,
				Timestamp: time.Now(),
			},
		}
		if err := persister.SaveTimeline(sessionID, events); err != nil {
			t.Fatalf("SaveTimeline failed for %s: %v", sessionID, err)
		}
	}

	// List
	timelines, err := persister.ListTimelines()
	if err != nil {
		t.Fatalf("ListTimelines failed: %v", err)
	}

	if len(timelines) != len(sessions) {
		t.Errorf("Expected %d timelines, got %d", len(sessions), len(timelines))
	}

	for _, ti := range timelines {
		t.Logf("Found timeline: %s (events=%d, size=%d bytes)",
			ti.SessionID, ti.EventCount, ti.Size)
	}

	t.Log("PASS: Listed all timelines correctly")
}

func TestDeleteTimeline(t *testing.T) {
	tmpDir := t.TempDir()
	config := &TimelinePersistConfig{BaseDir: tmpDir}

	persister, err := NewTimelinePersister(config)
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	sessionID := "to-delete"
	events := []AgentEvent{
		{AgentID: "cc_1", SessionID: sessionID, State: TimelineWorking, Timestamp: time.Now()},
	}

	// Save
	if err := persister.SaveTimeline(sessionID, events); err != nil {
		t.Fatalf("SaveTimeline failed: %v", err)
	}

	// Verify exists
	loaded, err := persister.LoadTimeline(sessionID)
	if err != nil || len(loaded) == 0 {
		t.Fatal("Timeline should exist after save")
	}

	// Delete
	if err := persister.DeleteTimeline(sessionID); err != nil {
		t.Fatalf("DeleteTimeline failed: %v", err)
	}

	// Verify deleted
	loaded, err = persister.LoadTimeline(sessionID)
	if err != nil {
		t.Fatalf("LoadTimeline error after delete: %v", err)
	}
	if loaded != nil {
		t.Error("Timeline should be nil after deletion")
	}

	t.Log("PASS: Deleted timeline successfully")
}

func TestCleanupOldTimelines(t *testing.T) {
	tmpDir := t.TempDir()
	config := &TimelinePersistConfig{
		BaseDir:      tmpDir,
		MaxTimelines: 3,
	}

	persister, err := NewTimelinePersister(config)
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	// Create 5 timelines
	for i := 0; i < 5; i++ {
		sessionID := "session-" + string(rune('a'+i))
		events := []AgentEvent{
			{AgentID: "cc_1", SessionID: sessionID, State: TimelineWorking, Timestamp: time.Now()},
		}
		if err := persister.SaveTimeline(sessionID, events); err != nil {
			t.Fatalf("SaveTimeline failed: %v", err)
		}
		// Small delay to ensure different mod times
		time.Sleep(10 * time.Millisecond)
	}

	// Verify 5 exist
	timelines, _ := persister.ListTimelines()
	if len(timelines) != 5 {
		t.Fatalf("Expected 5 timelines before cleanup, got %d", len(timelines))
	}

	// Cleanup
	deleted, err := persister.Cleanup()
	if err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}

	t.Logf("Cleaned up %d timelines", deleted)

	// Verify only MaxTimelines remain
	timelines, _ = persister.ListTimelines()
	if len(timelines) > config.MaxTimelines {
		t.Errorf("Expected at most %d timelines after cleanup, got %d",
			config.MaxTimelines, len(timelines))
	}

	t.Log("PASS: Cleanup removed old timelines")
}

func TestEmptySessionIDError(t *testing.T) {
	tmpDir := t.TempDir()
	config := &TimelinePersistConfig{BaseDir: tmpDir}

	persister, err := NewTimelinePersister(config)
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	// SaveTimeline with empty ID
	err = persister.SaveTimeline("", []AgentEvent{})
	if err == nil {
		t.Error("Expected error for empty session ID on save")
	}

	// LoadTimeline with empty ID
	_, err = persister.LoadTimeline("")
	if err == nil {
		t.Error("Expected error for empty session ID on load")
	}

	// DeleteTimeline with empty ID
	err = persister.DeleteTimeline("")
	if err == nil {
		t.Error("Expected error for empty session ID on delete")
	}

	t.Log("PASS: Empty session ID errors correctly")
}

func TestSaveTimelineOverwrite(t *testing.T) {
	tmpDir := t.TempDir()
	config := &TimelinePersistConfig{BaseDir: tmpDir}

	persister, err := NewTimelinePersister(config)
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	sessionID := "overwrite-test"

	// First save with 2 events
	events1 := []AgentEvent{
		{AgentID: "cc_1", SessionID: sessionID, State: TimelineWorking, Timestamp: time.Now()},
		{AgentID: "cc_1", SessionID: sessionID, State: TimelineIdle, Timestamp: time.Now()},
	}
	if err := persister.SaveTimeline(sessionID, events1); err != nil {
		t.Fatalf("First save failed: %v", err)
	}

	// Second save with 3 events (should overwrite)
	events2 := []AgentEvent{
		{AgentID: "cc_1", SessionID: sessionID, State: TimelineWorking, Timestamp: time.Now()},
		{AgentID: "cc_1", SessionID: sessionID, State: TimelineIdle, Timestamp: time.Now()},
		{AgentID: "cc_1", SessionID: sessionID, State: TimelineError, Timestamp: time.Now()},
	}
	if err := persister.SaveTimeline(sessionID, events2); err != nil {
		t.Fatalf("Second save failed: %v", err)
	}

	// Load and verify
	loaded, err := persister.LoadTimeline(sessionID)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(loaded) != 3 {
		t.Errorf("Expected 3 events after overwrite, got %d", len(loaded))
	}

	t.Log("PASS: Overwrite works correctly")
}

func TestTimelineWithMultipleAgents(t *testing.T) {
	tmpDir := t.TempDir()
	config := &TimelinePersistConfig{BaseDir: tmpDir}

	persister, err := NewTimelinePersister(config)
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	sessionID := "multi-agent-session"
	now := time.Now()

	events := []AgentEvent{
		{AgentID: "cc_1", AgentType: AgentTypeClaude, SessionID: sessionID, State: TimelineWorking, Timestamp: now},
		{AgentID: "cod_1", AgentType: AgentTypeCodex, SessionID: sessionID, State: TimelineWorking, Timestamp: now.Add(1 * time.Second)},
		{AgentID: "gmi_1", AgentType: AgentTypeGemini, SessionID: sessionID, State: TimelineWorking, Timestamp: now.Add(2 * time.Second)},
		{AgentID: "cc_1", AgentType: AgentTypeClaude, SessionID: sessionID, State: TimelineIdle, Timestamp: now.Add(3 * time.Second)},
	}

	if err := persister.SaveTimeline(sessionID, events); err != nil {
		t.Fatalf("SaveTimeline failed: %v", err)
	}

	loaded, err := persister.LoadTimeline(sessionID)
	if err != nil {
		t.Fatalf("LoadTimeline failed: %v", err)
	}

	if len(loaded) != 4 {
		t.Errorf("Expected 4 events, got %d", len(loaded))
	}

	// Check agent diversity
	agents := make(map[string]bool)
	for _, e := range loaded {
		agents[e.AgentID] = true
	}

	if len(agents) != 3 {
		t.Errorf("Expected 3 unique agents, got %d", len(agents))
	}

	t.Logf("PASS: Multi-agent timeline with %d unique agents", len(agents))
}

func TestGetTimelineInfo(t *testing.T) {
	tmpDir := t.TempDir()
	config := &TimelinePersistConfig{BaseDir: tmpDir}

	persister, err := NewTimelinePersister(config)
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	sessionID := "info-test"
	events := []AgentEvent{
		{AgentID: "cc_1", SessionID: sessionID, State: TimelineWorking, Timestamp: time.Now()},
		{AgentID: "cc_2", SessionID: sessionID, State: TimelineWorking, Timestamp: time.Now()},
	}

	if err := persister.SaveTimeline(sessionID, events); err != nil {
		t.Fatalf("SaveTimeline failed: %v", err)
	}

	info, err := persister.GetTimelineInfo(sessionID)
	if err != nil {
		t.Fatalf("GetTimelineInfo failed: %v", err)
	}

	if info == nil {
		t.Fatal("Expected non-nil info")
	}

	if info.SessionID != sessionID {
		t.Errorf("Expected SessionID %q, got %q", sessionID, info.SessionID)
	}

	t.Logf("Timeline info: session=%s events=%d agents=%d size=%d",
		info.SessionID, info.EventCount, info.AgentCount, info.Size)

	t.Log("PASS: GetTimelineInfo works correctly")
}

func TestDefaultTimelinePersistConfig(t *testing.T) {
	config := DefaultTimelinePersistConfig()

	if config.MaxTimelines != 30 {
		t.Errorf("Expected MaxTimelines 30, got %d", config.MaxTimelines)
	}

	if config.CompressOlderThan != 24*time.Hour {
		t.Errorf("Expected CompressOlderThan 24h, got %v", config.CompressOlderThan)
	}

	if config.CheckpointInterval != 5*time.Minute {
		t.Errorf("Expected CheckpointInterval 5m, got %v", config.CheckpointInterval)
	}

	if config.BaseDir == "" {
		t.Error("Expected non-empty BaseDir")
	}

	t.Logf("Default config: BaseDir=%s MaxTimelines=%d CompressOlderThan=%v",
		config.BaseDir, config.MaxTimelines, config.CompressOlderThan)

	t.Log("PASS: Default config has sensible values")
}

func TestDefaultTimelinePersistConfigRespectsXDGDataHome(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("NTM_CONFIG", "")
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg-data"))

	config := DefaultTimelinePersistConfig()
	want := filepath.Join(tmpDir, "xdg-data", "ntm", "timelines")
	if config.BaseDir != want {
		t.Fatalf("BaseDir = %q, want %q", config.BaseDir, want)
	}
}

func TestDefaultTimelinePersistConfigRespectsSelectedConfigPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg-data"))
	t.Setenv("NTM_CONFIG", filepath.Join(tmpDir, "custom-root", "config.toml"))

	config := DefaultTimelinePersistConfig()
	want := filepath.Join(tmpDir, "custom-root", "timelines")
	if config.BaseDir != want {
		t.Fatalf("BaseDir = %q, want %q", config.BaseDir, want)
	}
}

func TestCompressTimeline(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{BaseDir: tmpDir})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	sessionID := "compress-me"
	events := []AgentEvent{
		{AgentID: "cc_1", SessionID: sessionID, State: TimelineWorking, Timestamp: time.Now()},
		{AgentID: "cc_2", SessionID: sessionID, State: TimelineIdle, Timestamp: time.Now()},
	}
	if err := persister.SaveTimeline(sessionID, events); err != nil {
		t.Fatalf("SaveTimeline: %v", err)
	}

	// Verify uncompressed file exists
	uncompressed := filepath.Join(tmpDir, sessionID+".jsonl")
	if _, err := os.Stat(uncompressed); err != nil {
		t.Fatalf("uncompressed file missing: %v", err)
	}

	// Compress
	if err := persister.compressTimeline(sessionID); err != nil {
		t.Fatalf("compressTimeline: %v", err)
	}

	// Verify .gz exists and original is removed
	compressed := filepath.Join(tmpDir, sessionID+".jsonl.gz")
	if _, err := os.Stat(compressed); err != nil {
		t.Fatalf("compressed file missing: %v", err)
	}
	if _, err := os.Stat(uncompressed); !os.IsNotExist(err) {
		t.Error("original file should be removed after compression")
	}

	// Verify we can still load the compressed timeline
	loaded, err := persister.LoadTimeline(sessionID)
	if err != nil {
		t.Fatalf("LoadTimeline from compressed: %v", err)
	}
	if len(loaded) != 2 {
		t.Errorf("expected 2 events from compressed, got %d", len(loaded))
	}
}

func TestCompressTimeline_NonExistent(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{BaseDir: tmpDir})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	err = persister.compressTimeline("does-not-exist")
	if err == nil {
		t.Error("expected error compressing nonexistent timeline")
	}
}

func TestCompressOldTimelines(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	// Use a very short compress threshold so we can test it
	persister, err := NewTimelinePersister(&TimelinePersistConfig{
		BaseDir:           tmpDir,
		CompressOlderThan: 1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	// Create two timelines
	for _, id := range []string{"old-session", "old-session-2"} {
		events := []AgentEvent{
			{AgentID: "cc_1", SessionID: id, State: TimelineWorking, Timestamp: time.Now()},
		}
		if err := persister.SaveTimeline(id, events); err != nil {
			t.Fatalf("SaveTimeline(%s): %v", id, err)
		}
	}

	// Wait past the threshold
	time.Sleep(5 * time.Millisecond)

	compressed, err := persister.CompressOldTimelines()
	if err != nil {
		t.Fatalf("CompressOldTimelines: %v", err)
	}
	if compressed != 2 {
		t.Errorf("expected 2 compressed, got %d", compressed)
	}

	// Verify both are now .gz
	for _, id := range []string{"old-session", "old-session-2"} {
		gzPath := filepath.Join(tmpDir, id+".jsonl.gz")
		if _, err := os.Stat(gzPath); err != nil {
			t.Errorf("expected %s to be compressed: %v", id, err)
		}
		plainPath := filepath.Join(tmpDir, id+".jsonl")
		if _, err := os.Stat(plainPath); !os.IsNotExist(err) {
			t.Errorf("expected original %s to be removed", id)
		}
	}
}

func TestCompressOldTimelines_DisabledWhenZero(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{
		BaseDir:           tmpDir,
		CompressOlderThan: -1, // disabled
	})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	// Should return 0 immediately since compression is disabled
	compressed, err := persister.CompressOldTimelines()
	if err != nil {
		t.Fatalf("CompressOldTimelines: %v", err)
	}
	if compressed != 0 {
		t.Errorf("expected 0 compressed when disabled, got %d", compressed)
	}
}

func TestCompressOldTimelines_SkipsAlreadyCompressed(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{
		BaseDir:           tmpDir,
		CompressOlderThan: 1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	sessionID := "already-compressed"
	events := []AgentEvent{
		{AgentID: "cc_1", SessionID: sessionID, State: TimelineWorking, Timestamp: time.Now()},
	}
	if err := persister.SaveTimeline(sessionID, events); err != nil {
		t.Fatalf("SaveTimeline: %v", err)
	}

	time.Sleep(5 * time.Millisecond)

	// First compression
	n1, err := persister.CompressOldTimelines()
	if err != nil {
		t.Fatalf("first CompressOldTimelines: %v", err)
	}
	if n1 != 1 {
		t.Fatalf("expected 1 compressed, got %d", n1)
	}

	// Second compression should skip (already compressed)
	n2, err := persister.CompressOldTimelines()
	if err != nil {
		t.Fatalf("second CompressOldTimelines: %v", err)
	}
	if n2 != 0 {
		t.Errorf("expected 0 on second run, got %d", n2)
	}
}

func TestListTimelines_IncludesCompressed(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{BaseDir: tmpDir})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	// Save two sessions
	for _, id := range []string{"plain-session", "gz-session"} {
		events := []AgentEvent{
			{AgentID: "cc_1", SessionID: id, State: TimelineWorking, Timestamp: time.Now()},
		}
		if err := persister.SaveTimeline(id, events); err != nil {
			t.Fatalf("SaveTimeline(%s): %v", id, err)
		}
	}

	// Compress one
	if err := persister.compressTimeline("gz-session"); err != nil {
		t.Fatalf("compressTimeline: %v", err)
	}

	timelines, err := persister.ListTimelines()
	if err != nil {
		t.Fatalf("ListTimelines: %v", err)
	}
	if len(timelines) != 2 {
		t.Fatalf("expected 2 timelines, got %d", len(timelines))
	}

	// Verify one is compressed and one is not
	var gotPlain, gotCompressed bool
	for _, ti := range timelines {
		if ti.SessionID == "plain-session" && !ti.Compressed {
			gotPlain = true
		}
		if ti.SessionID == "gz-session" && ti.Compressed {
			gotCompressed = true
		}
	}
	if !gotPlain {
		t.Error("expected plain-session to be uncompressed")
	}
	if !gotCompressed {
		t.Error("expected gz-session to be compressed")
	}
}

func TestStopCheckpoint(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{
		BaseDir:            tmpDir,
		CheckpointInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	// StopCheckpoint on non-existent session should be safe
	persister.StopCheckpoint("nonexistent")

	// Stop on empty persister should be safe
	persister.Stop()
}

func TestGetTimelineInfo_NotFound(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{BaseDir: tmpDir})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	info, err := persister.GetTimelineInfo("nonexistent")
	if err != nil {
		t.Fatalf("GetTimelineInfo: %v", err)
	}
	if info != nil {
		t.Error("expected nil for nonexistent session")
	}
}

func TestCountUniqueAgents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		events []AgentEvent
		want   int
	}{
		{"empty", nil, 0},
		{"single agent", []AgentEvent{
			{AgentID: "cc_1"},
			{AgentID: "cc_1"},
		}, 1},
		{"multiple agents", []AgentEvent{
			{AgentID: "cc_1"},
			{AgentID: "cod_1"},
			{AgentID: "gmi_1"},
			{AgentID: "cc_1"},
		}, 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := countUniqueAgents(tc.events)
			if got != tc.want {
				t.Errorf("countUniqueAgents() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDeleteTimeline_CompressedFile(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{BaseDir: tmpDir})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	sessionID := "delete-compressed"
	events := []AgentEvent{
		{AgentID: "cc_1", SessionID: sessionID, State: TimelineWorking, Timestamp: time.Now()},
	}
	if err := persister.SaveTimeline(sessionID, events); err != nil {
		t.Fatalf("SaveTimeline: %v", err)
	}
	if err := persister.compressTimeline(sessionID); err != nil {
		t.Fatalf("compressTimeline: %v", err)
	}

	// Delete the compressed timeline
	if err := persister.DeleteTimeline(sessionID); err != nil {
		t.Fatalf("DeleteTimeline: %v", err)
	}

	// Verify it's gone
	loaded, err := persister.LoadTimeline(sessionID)
	if err != nil {
		t.Fatalf("LoadTimeline after delete: %v", err)
	}
	if loaded != nil {
		t.Error("expected nil after deleting compressed timeline")
	}
}

func TestReadHeader_Compressed(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{BaseDir: tmpDir})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	sessionID := "header-gz"
	now := time.Now()
	events := []AgentEvent{
		{AgentID: "cc_1", SessionID: sessionID, State: TimelineWorking, Timestamp: now},
		{AgentID: "cc_2", SessionID: sessionID, State: TimelineIdle, Timestamp: now.Add(time.Second)},
	}
	if err := persister.SaveTimeline(sessionID, events); err != nil {
		t.Fatalf("SaveTimeline: %v", err)
	}

	// Compress the file
	if err := persister.compressTimeline(sessionID); err != nil {
		t.Fatalf("compressTimeline: %v", err)
	}

	// Read header from compressed file
	gzPath := filepath.Join(tmpDir, sessionID+".jsonl.gz")
	header, err := persister.readHeader(gzPath, true)
	if err != nil {
		t.Fatalf("readHeader compressed: %v", err)
	}
	if header == nil {
		t.Fatal("expected non-nil header")
	}
	if header.SessionID != sessionID {
		t.Errorf("expected SessionID %q, got %q", sessionID, header.SessionID)
	}
	if header.EventCount != 2 {
		t.Errorf("expected EventCount 2, got %d", header.EventCount)
	}
	if header.AgentCount != 2 {
		t.Errorf("expected AgentCount 2, got %d", header.AgentCount)
	}
}

func TestReadHeader_EmptyFile(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{BaseDir: tmpDir})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	// Create empty file
	emptyPath := filepath.Join(tmpDir, "empty.jsonl")
	if err := os.WriteFile(emptyPath, nil, 0644); err != nil {
		t.Fatalf("create empty file: %v", err)
	}

	_, err = persister.readHeader(emptyPath, false)
	if err == nil {
		t.Error("expected error for empty file")
	}
}

func TestNewTimelinePersister_DefaultConfig(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	// Pass nil config — should use defaults but override BaseDir
	persister, err := NewTimelinePersister(&TimelinePersistConfig{BaseDir: tmpDir})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	if persister.config.MaxTimelines != 30 {
		t.Errorf("expected default MaxTimelines 30, got %d", persister.config.MaxTimelines)
	}
	if persister.config.CompressOlderThan != 24*time.Hour {
		t.Errorf("expected default CompressOlderThan 24h, got %v", persister.config.CompressOlderThan)
	}
	if persister.config.CheckpointInterval != 5*time.Minute {
		t.Errorf("expected default CheckpointInterval 5m, got %v", persister.config.CheckpointInterval)
	}
}

func TestLoadTimeline_MalformedLines(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{BaseDir: tmpDir})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	sessionID := "malformed"
	path := filepath.Join(tmpDir, sessionID+".jsonl")

	// Write header + malformed line + valid event
	content := `{"version":"1.0","session_id":"malformed","event_count":1}
not valid json
{"agent_id":"cc_1","session_id":"malformed","state":"working","timestamp":"2026-01-01T00:00:00Z"}
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	events, err := persister.LoadTimeline(sessionID)
	if err != nil {
		t.Fatalf("LoadTimeline: %v", err)
	}
	// Should skip malformed line and return the valid event
	if len(events) != 1 {
		t.Errorf("expected 1 valid event, got %d", len(events))
	}
}

func TestCleanup_NothingToClean(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{
		BaseDir:      tmpDir,
		MaxTimelines: 100,
	})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	// Create just 2 timelines with limit 100
	for _, id := range []string{"s1", "s2"} {
		events := []AgentEvent{
			{AgentID: "cc_1", SessionID: id, State: TimelineWorking, Timestamp: time.Now()},
		}
		if err := persister.SaveTimeline(id, events); err != nil {
			t.Fatalf("SaveTimeline: %v", err)
		}
	}

	deleted, err := persister.Cleanup()
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if deleted != 0 {
		t.Errorf("expected 0 deleted, got %d", deleted)
	}
}

func TestListTimelines_EmptyDir(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{BaseDir: tmpDir})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	timelines, err := persister.ListTimelines()
	if err != nil {
		t.Fatalf("ListTimelines: %v", err)
	}
	if len(timelines) != 0 {
		t.Errorf("expected 0 timelines in empty dir, got %d", len(timelines))
	}
}

func TestListTimelines_IgnoresNonJSONL(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{BaseDir: tmpDir})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	// Create a non-jsonl file
	if err := os.WriteFile(filepath.Join(tmpDir, "readme.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Create a valid timeline
	events := []AgentEvent{
		{AgentID: "cc_1", SessionID: "valid", State: TimelineWorking, Timestamp: time.Now()},
	}
	if err := persister.SaveTimeline("valid", events); err != nil {
		t.Fatalf("SaveTimeline: %v", err)
	}

	timelines, err := persister.ListTimelines()
	if err != nil {
		t.Fatalf("ListTimelines: %v", err)
	}
	if len(timelines) != 1 {
		t.Errorf("expected 1 timeline (ignoring .txt), got %d", len(timelines))
	}
}

func TestStartCheckpointAndStop(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{
		BaseDir:            tmpDir,
		CheckpointInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	sessionID := "checkpoint-session"
	tracker.RecordEvent(AgentEvent{
		AgentID:   "cc_1",
		SessionID: sessionID,
		State:     TimelineWorking,
		Timestamp: time.Now(),
	})

	// Start checkpointing
	persister.StartCheckpoint(sessionID, tracker)

	// Wait for at least one checkpoint to fire
	time.Sleep(120 * time.Millisecond)

	// Verify timeline was saved via checkpoint
	loaded, err := persister.LoadTimeline(sessionID)
	if err != nil {
		t.Fatalf("LoadTimeline: %v", err)
	}
	if len(loaded) == 0 {
		t.Error("expected events to be checkpointed")
	}

	// Stop should clean up all checkpoints
	persister.Stop()
}

func TestStartCheckpoint_RestartsExisting(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{
		BaseDir:            tmpDir,
		CheckpointInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	sessionID := "restart-session"

	// Start checkpoint twice - second should replace first without error
	persister.StartCheckpoint(sessionID, tracker)
	persister.StartCheckpoint(sessionID, tracker)

	persister.Stop()
}

func TestStartCheckpoint_NilTrackerNoop(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{
		BaseDir:            tmpDir,
		CheckpointInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	// Should not panic with nil tracker
	persister.StartCheckpoint("session", nil)
	persister.Stop()
}

func TestStartCheckpoint_EmptySessionNoop(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{
		BaseDir:            tmpDir,
		CheckpointInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	// Should not panic with empty session ID
	persister.StartCheckpoint("", tracker)
	persister.StartCheckpoint("   ", tracker)
	persister.Stop()
}

func TestTimelineOperationsRejectInvalidSessionIDs(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{BaseDir: tmpDir})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	events := []AgentEvent{
		{AgentID: "cc_1", SessionID: "valid", State: TimelineWorking, Timestamp: time.Now()},
	}

	for _, sessionID := range []string{"", "   ", ".", "..", "../escape", `nested\escape`} {
		if err := persister.SaveTimeline(sessionID, events); err == nil {
			t.Fatalf("SaveTimeline(%q) expected error", sessionID)
		}
		if _, err := persister.LoadTimeline(sessionID); err == nil {
			t.Fatalf("LoadTimeline(%q) expected error", sessionID)
		}
		if err := persister.DeleteTimeline(sessionID); err == nil {
			t.Fatalf("DeleteTimeline(%q) expected error", sessionID)
		}
		if _, err := persister.GetTimelineInfo(sessionID); err == nil {
			t.Fatalf("GetTimelineInfo(%q) expected error", sessionID)
		}
	}
}

func TestSaveTimelineRemovesStaleCompressedSibling(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{BaseDir: tmpDir})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	sessionID := "reused-session"
	oldEvents := []AgentEvent{
		{AgentID: "cc_1", SessionID: sessionID, State: TimelineWorking, Timestamp: time.Now().Add(-time.Minute)},
	}
	if err := persister.SaveTimeline(sessionID, oldEvents); err != nil {
		t.Fatalf("initial SaveTimeline: %v", err)
	}
	if err := persister.compressTimeline(sessionID); err != nil {
		t.Fatalf("compressTimeline: %v", err)
	}

	compressedPath := filepath.Join(tmpDir, sessionID+".jsonl.gz")
	if _, err := os.Stat(compressedPath); err != nil {
		t.Fatalf("expected compressed timeline: %v", err)
	}

	newEvents := []AgentEvent{
		{AgentID: "cc_1", SessionID: sessionID, State: TimelineWorking, Timestamp: time.Now()},
		{AgentID: "cc_1", SessionID: sessionID, State: TimelineIdle, Timestamp: time.Now().Add(time.Second)},
	}
	if err := persister.SaveTimeline(sessionID, newEvents); err != nil {
		t.Fatalf("replacement SaveTimeline: %v", err)
	}

	if _, err := os.Stat(compressedPath); !os.IsNotExist(err) {
		t.Fatalf("expected stale compressed timeline to be removed, stat err=%v", err)
	}

	timelines, err := persister.ListTimelines()
	if err != nil {
		t.Fatalf("ListTimelines: %v", err)
	}
	if len(timelines) != 1 {
		t.Fatalf("expected 1 deduplicated timeline entry, got %d", len(timelines))
	}
	if timelines[0].Compressed {
		t.Fatalf("expected live timeline entry to be uncompressed")
	}

	loaded, err := persister.LoadTimeline(sessionID)
	if err != nil {
		t.Fatalf("LoadTimeline: %v", err)
	}
	if len(loaded) != len(newEvents) {
		t.Fatalf("expected %d replacement events, got %d", len(newEvents), len(loaded))
	}
}

func TestListTimelinesDeduplicatesCompressedAndUncompressedEntries(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{BaseDir: tmpDir})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	sessionID := "duplicate-session"
	events := []AgentEvent{
		{AgentID: "cc_1", SessionID: sessionID, State: TimelineWorking, Timestamp: time.Now()},
	}
	if err := persister.SaveTimeline(sessionID, events); err != nil {
		t.Fatalf("SaveTimeline: %v", err)
	}

	gzPath := filepath.Join(tmpDir, sessionID+".jsonl.gz")
	if err := os.WriteFile(gzPath, []byte(`{"version":"1.0","session_id":"duplicate-session","event_count":999}`+"\n"), 0644); err != nil {
		t.Fatalf("write stale compressed sibling: %v", err)
	}

	timelines, err := persister.ListTimelines()
	if err != nil {
		t.Fatalf("ListTimelines: %v", err)
	}
	if len(timelines) != 1 {
		t.Fatalf("expected deduplicated timeline list, got %d entries", len(timelines))
	}
	if timelines[0].Compressed {
		t.Fatalf("expected uncompressed timeline to win tie-breaker")
	}
}

func TestStopWithActiveCheckpoints(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{
		BaseDir:            tmpDir,
		CheckpointInterval: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	// Start multiple checkpoints
	persister.StartCheckpoint("session-1", tracker)
	persister.StartCheckpoint("session-2", tracker)
	persister.StartCheckpoint("session-3", tracker)

	// Stop should clean up all without panic
	persister.Stop()
}

func TestGetTimelinePath(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{BaseDir: tmpDir})
	if err != nil {
		t.Fatalf("NewTimelinePersister: %v", err)
	}

	plain := persister.getTimelinePath("my-session", false)
	if plain != filepath.Join(tmpDir, "my-session.jsonl") {
		t.Errorf("plain path = %q", plain)
	}

	gz := persister.getTimelinePath("my-session", true)
	if gz != filepath.Join(tmpDir, "my-session.jsonl.gz") {
		t.Errorf("gz path = %q", gz)
	}
}

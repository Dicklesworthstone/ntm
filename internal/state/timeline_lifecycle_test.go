package state

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestNewTimelineLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	config := &TimelinePersistConfig{BaseDir: tmpDir}

	persister, err := NewTimelinePersister(config)
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	tracker := NewTimelineTracker(nil)

	lifecycle, err := NewTimelineLifecycle(tracker, persister)
	if err != nil {
		t.Fatalf("NewTimelineLifecycle failed: %v", err)
	}

	if lifecycle.tracker != tracker {
		t.Error("GetTracker returned wrong tracker")
	}

	if lifecycle.persister != persister {
		t.Error("GetPersister returned wrong persister")
	}

	t.Log("PASS: NewTimelineLifecycle creates lifecycle manager correctly")
}

func TestTimelineLifecycleStartSession(t *testing.T) {
	tmpDir := t.TempDir()
	config := &TimelinePersistConfig{
		BaseDir:            tmpDir,
		CheckpointInterval: 100 * time.Millisecond, // Short interval for testing
	}

	persister, err := NewTimelinePersister(config)
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	tracker := NewTimelineTracker(nil)

	lifecycle, err := NewTimelineLifecycle(tracker, persister)
	if err != nil {
		t.Fatalf("NewTimelineLifecycle failed: %v", err)
	}
	defer stopLifecycleForTest(lifecycle)

	sessionID := "test-session"

	// Start session
	lifecycle.StartSession(sessionID)

	if !lifecycleIsActiveForTest(lifecycle, sessionID) {
		t.Error("Session should be active after StartSession")
	}

	sessions := lifecycleSessionsForTest(lifecycle)
	if len(sessions) != 1 || sessions[0] != sessionID {
		t.Errorf("Expected active sessions [%s], got %v", sessionID, sessions)
	}

	// Starting same session again should be idempotent
	lifecycle.StartSession(sessionID)
	sessions = lifecycleSessionsForTest(lifecycle)
	if len(sessions) != 1 {
		t.Errorf("Expected 1 active session after duplicate start, got %d", len(sessions))
	}

	t.Log("PASS: StartSession activates timeline tracking")
}

func TestTimelineLifecycleStartSession_IgnoresInvalidSessionID(t *testing.T) {
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{BaseDir: tmpDir})
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	tracker := NewTimelineTracker(nil)

	lifecycle, err := NewTimelineLifecycle(tracker, persister)
	if err != nil {
		t.Fatalf("NewTimelineLifecycle failed: %v", err)
	}
	defer stopLifecycleForTest(lifecycle)

	lifecycle.StartSession("")
	lifecycle.StartSession("   ")
	lifecycle.StartSession("../escape")

	if len(lifecycleSessionsForTest(lifecycle)) != 0 {
		t.Fatalf("expected no active sessions for invalid input, got %v", lifecycleSessionsForTest(lifecycle))
	}
}

func TestTimelineLifecycleEndSession(t *testing.T) {
	tmpDir := t.TempDir()
	config := &TimelinePersistConfig{BaseDir: tmpDir}

	persister, err := NewTimelinePersister(config)
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	tracker := NewTimelineTracker(nil)

	lifecycle, err := NewTimelineLifecycle(tracker, persister)
	if err != nil {
		t.Fatalf("NewTimelineLifecycle failed: %v", err)
	}
	defer stopLifecycleForTest(lifecycle)

	sessionID := "end-test-session"

	// Record some events
	tracker.RecordEvent(AgentEvent{
		AgentID:   "cc_1",
		SessionID: sessionID,
		State:     TimelineWorking,
		Timestamp: time.Now(),
	})
	tracker.RecordEvent(AgentEvent{
		AgentID:   "cc_1",
		SessionID: sessionID,
		State:     TimelineIdle,
		Timestamp: time.Now().Add(time.Second),
	})

	// Start and then end session
	lifecycle.StartSession(sessionID)
	err = lifecycle.EndSession(sessionID)
	if err != nil {
		t.Fatalf("EndSession failed: %v", err)
	}

	if lifecycleIsActiveForTest(lifecycle, sessionID) {
		t.Error("Session should not be active after EndSession")
	}

	// Verify timeline was saved
	path := filepath.Join(tmpDir, sessionID+".jsonl")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("Timeline file should exist after EndSession")
	}

	// Verify events were persisted
	events, err := persister.LoadTimeline(sessionID)
	if err != nil {
		t.Fatalf("LoadTimeline failed: %v", err)
	}

	if len(events) != 2 {
		t.Errorf("Expected 2 events, got %d", len(events))
	}

	t.Log("PASS: EndSession finalizes and persists timeline")
}

func TestTimelineLifecycleEndSession_RejectsInvalidSessionID(t *testing.T) {
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{BaseDir: tmpDir})
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	tracker := NewTimelineTracker(nil)

	lifecycle, err := NewTimelineLifecycle(tracker, persister)
	if err != nil {
		t.Fatalf("NewTimelineLifecycle failed: %v", err)
	}
	defer stopLifecycleForTest(lifecycle)

	if err := lifecycle.EndSession("   "); err == nil {
		t.Fatal("expected EndSession to reject blank session IDs")
	}
	if err := lifecycle.EndSession("../escape"); err == nil {
		t.Fatal("expected EndSession to reject path-like session IDs")
	}
}

func TestTimelineLifecycleMultipleSessions(t *testing.T) {
	tmpDir := t.TempDir()
	config := &TimelinePersistConfig{BaseDir: tmpDir}

	persister, err := NewTimelinePersister(config)
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	tracker := NewTimelineTracker(nil)

	lifecycle, err := NewTimelineLifecycle(tracker, persister)
	if err != nil {
		t.Fatalf("NewTimelineLifecycle failed: %v", err)
	}
	defer stopLifecycleForTest(lifecycle)

	// Start multiple sessions
	sessions := []string{"session-a", "session-b", "session-c"}
	for _, s := range sessions {
		lifecycle.StartSession(s)
	}

	activeSessions := lifecycleSessionsForTest(lifecycle)
	if len(activeSessions) != 3 {
		t.Errorf("Expected 3 active sessions, got %d", len(activeSessions))
	}

	// End one session
	err = lifecycle.EndSession("session-b")
	if err != nil {
		t.Fatalf("EndSession failed: %v", err)
	}

	activeSessions = lifecycleSessionsForTest(lifecycle)
	if len(activeSessions) != 2 {
		t.Errorf("Expected 2 active sessions after ending one, got %d", len(activeSessions))
	}

	t.Log("PASS: Multiple sessions can be tracked concurrently")
}

func TestStartSessionTimeline(t *testing.T) {
	// This tests the convenience function
	// Note: This uses the global lifecycle, so we need to be careful about state.
	// bd-ev740: scope HOME to a temp dir and clear ambient
	// XDG_CONFIG_HOME / NTM_CONFIG so the global lifecycle's lazy
	// initialization cannot route through an outer-shell-injected
	// invalid config path (e.g. /nonexistent/config.toml).
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("NTM_CONFIG", "")
	resetGlobalTimelineLifecycleForTest()

	sessionID := "convenience-test-" + time.Now().Format("20060102150405")

	err := StartSessionTimeline(sessionID)
	if err != nil {
		t.Fatalf("StartSessionTimeline failed: %v", err)
	}

	lifecycle, err := GetGlobalTimelineLifecycle()
	if err != nil {
		t.Fatalf("GetGlobalTimelineLifecycle failed: %v", err)
	}

	if !lifecycleIsActiveForTest(lifecycle, sessionID) {
		t.Error("Session should be active after StartSessionTimeline")
	}

	// Clean up
	_ = EndSessionTimeline(sessionID)

	t.Log("PASS: StartSessionTimeline convenience function works")
}

func TestStartSessionTimeline_RejectsInvalidSessionID(t *testing.T) {
	if err := StartSessionTimeline("   "); err == nil {
		t.Fatal("expected StartSessionTimeline to reject blank session IDs")
	}
	if err := StartSessionTimeline("../escape"); err == nil {
		t.Fatal("expected StartSessionTimeline to reject path-like session IDs")
	}
}

func TestEndSessionTimeline(t *testing.T) {
	// bd-ev740: hermetic HOME/XDG/NTM_CONFIG isolation (see
	// TestStartSessionTimeline comment).
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("NTM_CONFIG", "")
	resetGlobalTimelineLifecycleForTest()

	sessionID := "end-convenience-test-" + time.Now().Format("20060102150405")

	// Start first
	err := StartSessionTimeline(sessionID)
	if err != nil {
		t.Fatalf("StartSessionTimeline failed: %v", err)
	}

	// End session
	err = EndSessionTimeline(sessionID)
	if err != nil {
		t.Fatalf("EndSessionTimeline failed: %v", err)
	}

	lifecycle, err := GetGlobalTimelineLifecycle()
	if err != nil {
		t.Fatalf("GetGlobalTimelineLifecycle failed: %v", err)
	}

	if lifecycleIsActiveForTest(lifecycle, sessionID) {
		t.Error("Session should not be active after EndSessionTimeline")
	}

	t.Log("PASS: EndSessionTimeline convenience function works")
}

func TestEndSessionTimeline_RejectsInvalidSessionID(t *testing.T) {
	if err := EndSessionTimeline("   "); err == nil {
		t.Fatal("expected EndSessionTimeline to reject blank session IDs")
	}
	if err := EndSessionTimeline("../escape"); err == nil {
		t.Fatal("expected EndSessionTimeline to reject path-like session IDs")
	}
}

func TestGetGlobalTimelineTracker(t *testing.T) {
	tracker := GetGlobalTimelineTracker()
	if tracker == nil {
		t.Fatal("GetGlobalTimelineTracker returned nil")
	}

	// Should return the same instance
	tracker2 := GetGlobalTimelineTracker()
	if tracker != tracker2 {
		t.Error("GetGlobalTimelineTracker should return singleton")
	}

	t.Log("PASS: GetGlobalTimelineTracker returns singleton")
}

// lifecycleIsActiveForTest reports whether the session has active tracking.
func lifecycleIsActiveForTest(l *TimelineLifecycle, sessionID string) bool {
	normalizedSessionID, err := validateTimelineSessionID(sessionID)
	if err != nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, active := l.activeSessions[normalizedSessionID]
	return active
}

// lifecycleSessionsForTest returns the sessions with active tracking.
func lifecycleSessionsForTest(l *TimelineLifecycle) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	sessions := make([]string, 0, len(l.activeSessions))
	for s := range l.activeSessions {
		sessions = append(sessions, s)
	}
	return sessions
}

// stopLifecycleForTest finalizes all active sessions and stops the lifecycle.
func stopLifecycleForTest(l *TimelineLifecycle) {
	l.lifecycleMu.Lock()
	defer l.lifecycleMu.Unlock()
	if l.stopped {
		return
	}
	l.stopped = true

	l.mu.Lock()
	sessions := make([]string, 0, len(l.activeSessions))
	for s := range l.activeSessions {
		sessions = append(sessions, s)
		delete(l.activeSessions, s)
	}
	l.mu.Unlock()

	for _, sessionID := range sessions {
		_ = l.persister.FinalizeSession(sessionID, l.tracker)
	}
}

// resetGlobalTimelineLifecycleForTest tears down the singleton state so a test
// can re-initialize it under a hermetic HOME/XDG_CONFIG_HOME/NTM_CONFIG
// environment. Tests should call this AFTER setting their env overrides via
// t.Setenv.
func resetGlobalTimelineLifecycleForTest() {
	globalTimelineLifecycle = nil
	globalTimelineLifecycleErr = nil
	globalTimelineLifecycleOnce = sync.Once{}
}

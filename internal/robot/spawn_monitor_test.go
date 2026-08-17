package robot

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TestSpawnMonitorDefaultIsSharedResiliencePath pins bd-ws1-truth-safety-l5ddi.8
// (B5): robot spawn's monitor port defaults to resilience.StartSessionMonitor —
// the SAME function CLI spawn calls. Call-site sharing, not a copy.
func TestSpawnMonitorDefaultIsSharedResiliencePath(t *testing.T) {
	deps := spawnLifecycleDeps(nil)
	if deps.StartSessionMonitor == nil {
		t.Fatal("default lifecycle deps must wire StartSessionMonitor")
	}
	got := reflect.ValueOf(deps.StartSessionMonitor).Pointer()
	want := reflect.ValueOf(resilience.StartSessionMonitor).Pointer()
	if got != want {
		t.Fatalf("robot spawn monitor port = %#x, want resilience.StartSessionMonitor at %#x (shared code path)", got, want)
	}
}

func spawnMonitorTestPanes() []tmux.Pane {
	return []tmux.Pane{
		{ID: "%0", WindowIndex: 0, Index: 0, Title: "user"},
		{ID: "%1", WindowIndex: 0, Index: 1},
		{ID: "%2", WindowIndex: 0, Index: 2},
	}
}

// TestGetSpawn_MonitorStartedEnvelope asserts the success path: robot spawn
// invokes the shared manifest+monitor path with correct session/pane entries
// and reports monitor_started:true with the monitor PID on the envelope.
func TestGetSpawn_MonitorStartedEnvelope(t *testing.T) {
	var captured resilience.SpawnMonitorRequest
	deps := testSpawnLifecycleDependencies(spawnMonitorTestPanes())
	deps.StartSessionMonitor = func(_ context.Context, req resilience.SpawnMonitorRequest) (*resilience.SpawnMonitorResult, error) {
		captured = req
		return &resilience.SpawnMonitorResult{
			Manifest:       &resilience.SpawnManifest{},
			MonitorStarted: true,
			MonitorPID:     4242,
		}, nil
	}

	opts := SpawnOptions{
		Session:       "monitorproj",
		CCCount:       1,
		CodCount:      1,
		LifecycleDeps: deps,
	}
	out, err := GetSpawn(t.Context(), opts, testSpawnConfig())
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if out.Error != "" {
		t.Fatalf("spawn error = %q, want success", out.Error)
	}
	if !out.MonitorStarted {
		t.Fatal("monitor_started = false, want true")
	}
	if out.MonitorPID != 4242 {
		t.Fatalf("monitor_pid = %d, want 4242", out.MonitorPID)
	}
	if out.MonitorError != "" {
		t.Fatalf("monitor_error = %q, want empty", out.MonitorError)
	}

	// The shared path received the session and the launched agent panes.
	if captured.Session != "monitorproj" {
		t.Fatalf("request.Session = %q, want %q", captured.Session, "monitorproj")
	}
	if len(captured.Agents) != 2 {
		t.Fatalf("request.Agents = %+v, want 2 launched agent panes", captured.Agents)
	}
	wantPanes := map[string]string{"0.1": "claude", "0.2": "codex"}
	for _, agent := range captured.Agents {
		if wantPanes[agent.PaneID] != agent.Type {
			t.Fatalf("unexpected manifest agent %+v (want panes %v)", agent, wantPanes)
		}
		if agent.PaneIndex == 0 {
			t.Fatalf("agent %q pane index not populated: %+v", agent.PaneID, agent)
		}
	}
}

// TestGetSpawn_MonitorFailureIsBestEffort asserts the degraded path: a monitor
// failure never fails the spawn; the envelope carries monitor_started:false
// plus the error, and the degraded-event row lands in the state DB.
func TestGetSpawn_MonitorFailureIsBestEffort(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	deps := testSpawnLifecycleDependencies(spawnMonitorTestPanes())
	monitorErr := errors.New("monitor exploded")
	deps.StartSessionMonitor = func(context.Context, resilience.SpawnMonitorRequest) (*resilience.SpawnMonitorResult, error) {
		return nil, monitorErr
	}

	opts := SpawnOptions{
		Session:       "degradedproj",
		CCCount:       1,
		LifecycleDeps: deps,
	}
	out, err := GetSpawn(t.Context(), opts, testSpawnConfig())
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if out.Error != "" {
		t.Fatalf("spawn error = %q — monitor failure must NEVER fail the spawn", out.Error)
	}
	if out.MonitorStarted {
		t.Fatal("monitor_started = true, want false")
	}
	if out.MonitorError != "monitor exploded" {
		t.Fatalf("monitor_error = %q, want %q", out.MonitorError, "monitor exploded")
	}

	// Degraded-event row was written (visible degradation, A1 posture).
	store, err := state.Open("")
	if err != nil {
		t.Fatalf("open state store: %v", err)
	}
	defer store.Close()
	events, err := store.GetAttentionEventsSince(0, 10)
	if err != nil {
		t.Fatalf("list attention events: %v", err)
	}
	found := false
	for _, event := range events {
		if event.EventType == "spawn_monitor_unavailable" && event.SessionName == "degradedproj" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no spawn_monitor_unavailable degraded-event row for degradedproj; events = %+v", events)
	}
}

// TestGetSpawn_MonitorDisabledEnvelope asserts the explicit disabled guard is
// reported but NOT treated as a degradation.
func TestGetSpawn_MonitorDisabledEnvelope(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	deps := testSpawnLifecycleDependencies(spawnMonitorTestPanes())
	deps.StartSessionMonitor = func(context.Context, resilience.SpawnMonitorRequest) (*resilience.SpawnMonitorResult, error) {
		return nil, resilience.ErrInternalMonitorDisabled
	}

	out, err := GetSpawn(t.Context(), SpawnOptions{Session: "disabledproj", CCCount: 1, LifecycleDeps: deps}, testSpawnConfig())
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if out.Error != "" || out.MonitorStarted {
		t.Fatalf("unexpected envelope: error=%q monitor_started=%v", out.Error, out.MonitorStarted)
	}
	if out.MonitorError == "" {
		t.Fatal("monitor_error must report the disabled guard")
	}

	store, err := state.Open("")
	if err != nil {
		t.Fatalf("open state store: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	events, err := store.GetAttentionEventsSince(0, 10)
	if err != nil {
		t.Fatalf("list attention events: %v", err)
	}
	for _, event := range events {
		if event.EventType == "spawn_monitor_unavailable" {
			t.Fatalf("disabled guard must not write a degraded-event row: %+v", event)
		}
	}
}

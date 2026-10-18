package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/coordinator"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

type recordingAccountRotationRunner struct {
	run   func(context.Context) []coordinator.AccountFailoverDecision
	close func() error
}

func TestAccountRotationSessionIdentityRejectsMalformedEvidence(t *testing.T) {
	_, dir := accountRotationMonitorFixture(t)
	for _, name := range []string{"", ":", "$1", "%1", "cc_agents_1:0", "=cc_agents_1"} {
		if _, err := accountRotationSessionIdentity(t.Context(), name); err == nil {
			t.Fatalf("accepted ambiguous session target %q", name)
		}
	}
	for _, value := range []string{"", "0:$1:1770000000", "700:1:1770000000", "700:$1:0", "700:$1:1770000000\n700:$2:1770000000", "-1:$1:1770000000"} {
		if err := os.WriteFile(filepath.Join(dir, "identity"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := accountRotationSessionIdentity(t.Context(), "cc_agents_1"); err == nil {
			t.Fatalf("accepted identity %q", value)
		}
	}
}

func TestAccountRotationMonitorRealTmux(t *testing.T) {
	bin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("real tmux unavailable")
	}
	dir := t.TempDir()
	// Isolate the server, including its configuration and environment, from
	// any operator sessions. A spare session keeps the server PID unchanged
	// while the monitored name is replaced.
	wrapper := filepath.Join(dir, "tmux")
	socket := filepath.Join(dir, "socket")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+tmux.ShellQuote(bin)+" -S "+tmux.ShellQuote(socket)+" -f /dev/null \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", wrapper)
	oldClient, oldRunner, oldPreflight := tmux.DefaultClient, newAccountRotationRunner, swarmPreflightAccountRotation
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() {
		_, _ = tmux.DefaultClient.Run("kill-server")
		tmux.DefaultClient, newAccountRotationRunner, swarmPreflightAccountRotation = oldClient, oldRunner, oldPreflight
	})
	run := func(args ...string) string {
		t.Helper()
		out, err := tmux.DefaultClient.Run(args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if _, err := tmux.DefaultClient.Run("new-session", "-d", "-s", "spare"); err != nil {
		if strings.Contains(err.Error(), "Operation not permitted") {
			t.Skipf("host prohibits tmux server sockets: %v", err)
		}
		t.Fatal(err)
	}
	run("new-session", "-d", "-s", "rotation")
	pane := run("display-message", "-p", "-t", "=rotation:", "#{pane_id}")
	run("select-pane", "-t", pane, "-T", "rotation__cc_1")
	run("set-option", "-p", "-t", pane, "@ntm_agent_type", "cc")
	identity, err := accountRotationSessionIdentity(t.Context(), "rotation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accountRotationSessionIdentity(t.Context(), "rot"); err == nil {
		t.Fatal("prefix target accepted")
	}
	manifest := &resilience.SpawnManifest{Session: "rotation", SessionIdentity: identity, ProjectDir: dir,
		Agents:          []resilience.AgentConfig{{PaneID: pane, Type: "cc", ProjectDir: dir}},
		AccountRotation: &resilience.RotationMonitorOptions{Providers: []string{"claude"}, PollSeconds: 1}}
	swarmPreflightAccountRotation = func(context.Context, config.CAAMConfig) error { return nil }
	ran, closed, ready := false, false, false
	newAccountRotationRunner = func(string, coordinator.AccountFailoverOptions) (accountRotationRunner, error) {
		return recordingAccountRotationRunner{run: func(context.Context) []coordinator.AccountFailoverDecision { ran = true; return nil }, close: func() error { closed = true; return nil }}, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err = runAccountRotationMonitor(ctx, manifest, func(context.Context, io.ReadCloser) error {
		ready = true
		run("kill-session", "-t", "=rotation")
		run("new-session", "-d", "-s", "rotation")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "replaced") || !ready || ran || !closed {
		t.Fatalf("replacement lifecycle: err=%v ready=%v ran=%v closed=%v", err, ready, ran, closed)
	}
	newIdentity, err := accountRotationSessionIdentity(t.Context(), "rotation")
	if err != nil || newIdentity == identity {
		t.Fatalf("replacement identity=%q old=%q error=%v", newIdentity, identity, err)
	}
}

func (r recordingAccountRotationRunner) RunOnce(ctx context.Context) []coordinator.AccountFailoverDecision {
	if r.run == nil {
		return nil
	}
	return r.run(ctx)
}

func (r recordingAccountRotationRunner) Close() error {
	if r.close == nil {
		return nil
	}
	return r.close()
}

func accountRotationMonitorFixture(t *testing.T) (*resilience.SpawnManifest, string) {
	t.Helper()
	project, dir, _ := swarmRotationCommandFixture(t)
	t.Setenv("NTM_INTERNAL_MONITOR_GENERATION", "")
	for name, value := range map[string]string{"%41-title": "cc_agents_1__cc_1", "%41-command": "claude"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	oldRunner := newAccountRotationRunner
	t.Cleanup(func() { newAccountRotationRunner = oldRunner })
	manifest := &resilience.SpawnManifest{
		Session: "cc_agents_1", SessionIdentity: "700:$1:1770000000", ProjectDir: project,
		Agents: []resilience.AgentConfig{{PaneID: "%41", PaneIndex: 7, Type: "cc", ProjectDir: project}},
		AccountRotation: &resilience.RotationMonitorOptions{
			Providers: []string{"claude"}, CAAMBinary: cfg.Integrations.CAAM.BinaryPath, PollSeconds: 1,
		},
	}
	return manifest, dir
}

// Exercise the actual hidden Cobra surface and resident ownership protocol.
// Stop is not acknowledged while the canonical checker still has work in
// flight, even after its context has been canceled.
// The internal monitor is the only process that lives as long as the session,
// so it records the timeline `ntm timeline` reads. Each observation's state
// changes become timeline events until the monitor stops.
func TestRecordSessionTimelineRecordsObservedTransitions(t *testing.T) {
	session := fmt.Sprintf("tlrec_%d", time.Now().UnixNano())
	pane := tmux.Pane{ID: "%7", Title: session + "__cod_1", Type: tmux.AgentCodex}
	states := []statuspkg.AgentState{statuspkg.StateWorking, statuspkg.StateWorking, statuspkg.StateIdle}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	observe := func(_ context.Context, observed string) (statuspkg.SessionObservation, error) {
		if observed != session {
			t.Errorf("observed %q, want %q", observed, session)
		}
		st := states[min(calls, len(states)-1)]
		calls++
		if calls == len(states) {
			cancel()
		}
		return statuspkg.SessionObservation{Session: session, Panes: []statuspkg.PaneObservation{{
			Metadata: pane,
			Current:  statuspkg.StateObservation{Status: statuspkg.AgentStatus{PaneID: "%7", AgentType: "cod", State: st, UpdatedAt: time.Now()}},
		}}}, nil
	}

	recordSessionTimeline(ctx, session, time.Millisecond, observe)

	events := state.GetGlobalTimelineTracker().GetEventsForSession(session, time.Time{})
	if calls != len(states) || len(events) != 2 || events[0].State != state.TimelineWorking || events[1].State != state.TimelineIdle {
		t.Fatalf("after %d observations, events = %+v; want working then idle", calls, events)
	}
}

func TestAccountRotationMonitorStopJoinsInFlightRecovery(t *testing.T) {
	manifest, _ := accountRotationMonitorFixture(t)
	if err := resilience.SaveManifest(manifest); err != nil {
		t.Fatal(err)
	}
	entered, canceled, release, closed := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseRecovery := func() { releaseOnce.Do(func() { close(release) }) }
	newAccountRotationRunner = func(session string, options coordinator.AccountFailoverOptions) (accountRotationRunner, error) {
		if session != manifest.Session || len(options.Targets) != 1 || options.Targets["%41"].ProjectDir != manifest.ProjectDir || options.Config == nil || !options.Config.Integrations.CAAM.AutoFailover {
			return nil, errors.New("resident lost its explicit launch scope")
		}
		return recordingAccountRotationRunner{
			run: func(ctx context.Context) []coordinator.AccountFailoverDecision {
				close(entered)
				<-ctx.Done()
				close(canceled)
				<-release
				return nil
			},
			close: func() error { close(closed); return nil },
		}, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := newMonitorCmd()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{manifest.Session})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()
	t.Cleanup(func() {
		cancel()
		releaseRecovery()
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Error("resident runner did not close")
		}
	})
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("resident exited before its first account check: %v", err)
	case <-ctx.Done():
		t.Fatal("resident did not begin account monitoring")
	}
	status, err := resilience.ReadSessionMonitorStatus(manifest.Session)
	if err != nil || status == nil || !status.Healthy || !status.AccountRotation {
		t.Fatalf("active resident status = %+v, %v", status, err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- resilience.StopSessionMonitor(ctx, manifest.Session) }()
	select {
	case <-canceled:
	case err := <-stopped:
		t.Fatalf("stop returned before cancellation reached the checker: %v", err)
	case <-ctx.Done():
		t.Fatal("stop did not cancel in-flight recovery")
	}
	select {
	case err := <-stopped:
		t.Fatalf("stop acknowledged while recovery was still in flight: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	releaseRecovery()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("quiescent stop failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("stop did not acknowledge the joined runner")
	}
	select {
	case <-closed:
	default:
		t.Fatal("resident owner was released before its runner closed")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("monitor command returned an error after a clean stop: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("monitor command did not return after stop")
	}
}

func TestAccountRotationMonitorRequiresAuthorizationBeforeChecks(t *testing.T) {
	manifest, _ := accountRotationMonitorFixture(t)
	ran, closed := false, false
	newAccountRotationRunner = func(string, coordinator.AccountFailoverOptions) (accountRotationRunner, error) {
		return recordingAccountRotationRunner{
			run:   func(context.Context) []coordinator.AccountFailoverDecision { ran = true; return nil },
			close: func() error { closed = true; return nil },
		}, nil
	}
	want := errors.New("parent authorization canceled")
	err := runAccountRotationMonitor(t.Context(), manifest, func(context.Context, io.ReadCloser) error { return want })
	if !errors.Is(err, want) || ran || !closed {
		t.Fatalf("authorization refusal = %v, ran=%t closed=%t", err, ran, closed)
	}
}

func TestAccountRotationMonitorRejectsChangedScopeBeforeAcknowledgment(t *testing.T) {
	for _, cause := range []string{"missing_pane", "wrong_provider", "replaced_session", "replaced_during_preflight", "empty_scope", "remote"} {
		t.Run(cause, func(t *testing.T) {
			manifest, dir := accountRotationMonitorFixture(t)
			acknowledged, ran := false, false
			newAccountRotationRunner = func(string, coordinator.AccountFailoverOptions) (accountRotationRunner, error) {
				return recordingAccountRotationRunner{run: func(context.Context) []coordinator.AccountFailoverDecision { ran = true; return nil }}, nil
			}
			switch cause {
			case "missing_pane":
				manifest.Agents[0].PaneID = "%99"
			case "wrong_provider":
				manifest.Agents[0].Type = "cod"
			case "replaced_session":
				manifest.SessionIdentity = "700:$2:1770000001"
			case "replaced_during_preflight":
				swarmPreflightAccountRotation = func(context.Context, config.CAAMConfig) error {
					return os.WriteFile(filepath.Join(dir, "identity"), []byte("701:$1:1770000000"), 0600)
				}
			case "empty_scope":
				manifest.Agents = nil
			case "remote":
				tmux.DefaultClient.Remote = "test@host"
			}
			err := runAccountRotationMonitor(t.Context(), manifest, func(context.Context, io.ReadCloser) error { acknowledged = true; return nil })
			if err == nil || acknowledged || ran {
				t.Fatalf("changed startup scope accepted: err=%v acknowledged=%t ran=%t", err, acknowledged, ran)
			}
		})
	}
}

func TestAccountRotationMonitorPassesLiveScopeGuardToRecovery(t *testing.T) {
	manifest, dir := accountRotationMonitorFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	checked := false
	newAccountRotationRunner = func(_ string, options coordinator.AccountFailoverOptions) (accountRotationRunner, error) {
		if options.ValidateScope == nil {
			t.Fatal("resident recovery has no scope guard")
		}
		return recordingAccountRotationRunner{run: func(ctx context.Context) []coordinator.AccountFailoverDecision {
			if err := options.ValidateScope(ctx); err != nil {
				t.Errorf("original session refused: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, "identity"), []byte("701:$1:1770000000"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := options.ValidateScope(ctx); err == nil {
				t.Error("scope guard reused the pre-poll identity after server replacement")
			}
			checked = true
			cancel()
			return nil
		}}, nil
	}
	if err := runAccountRotationMonitor(ctx, manifest, func(context.Context, io.ReadCloser) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("recovery runner was not exercised")
	}
}

func TestAccountRotationMonitorStopsBeforeCheckingReplacementSession(t *testing.T) {
	manifest, dir := accountRotationMonitorFixture(t)
	ran, closed := false, false
	newAccountRotationRunner = func(string, coordinator.AccountFailoverOptions) (accountRotationRunner, error) {
		return recordingAccountRotationRunner{
			run:   func(context.Context) []coordinator.AccountFailoverDecision { ran = true; return nil },
			close: func() error { closed = true; return nil },
		}, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := runAccountRotationMonitor(ctx, manifest, func(context.Context, io.ReadCloser) error {
		return os.WriteFile(filepath.Join(dir, "identity"), []byte("700:$2:1770000001"), 0600)
	})
	if err == nil || !strings.Contains(err.Error(), "original session was replaced") || ran || !closed {
		t.Fatalf("replacement session handling = %v, ran=%t closed=%t", err, ran, closed)
	}
}

func TestMonitorSnapshotCancellationStopsCapture(t *testing.T) {
	manifest, dir := accountRotationMonitorFixture(t)
	t.Setenv("NTM_SWARM_BLOCK", "capture-pane")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	outputs := make(map[string]string)
	go func() {
		defer close(done)
		captureSessionOutputs(ctx, manifest.Session, outputs)
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(dir, "blocked")); err == nil {
			break
		}
		select {
		case <-done:
			t.Fatal("snapshot returned before capture began")
		case <-ctx.Done():
			t.Fatal("snapshot capture never started")
		case <-ticker.C:
		}
	}
	cancel()
	select {
	case <-done:
		if len(outputs) != 0 {
			t.Fatalf("canceled capture published output: %v", outputs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot kept the resident busy after owner cancellation")
	}
}

func TestMonitorSummarySkipsCanceledOwner(t *testing.T) {
	manifest, _ := accountRotationMonitorFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	generateEndSessionSummary(ctx, manifest.Session, map[string]string{"%41": "Finished implementation"}, manifest)
	if _, err := os.Stat(filepath.Join(manifest.ProjectDir, ".ntm", "summaries")); !os.IsNotExist(err) {
		t.Fatalf("explicit owner cancellation created summary work: %v", err)
	}
}

func TestMonitorRemoteInvocationDoesNotCreateOwnership(t *testing.T) {
	manifest, _ := accountRotationMonitorFixture(t)
	tmux.DefaultClient.Remote = "recording@host"
	cmd := newMonitorCmd()
	cmd.SetArgs([]string{manifest.Session})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "local tmux sessions") {
		t.Fatalf("remote monitor invocation was accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(resilience.ManifestDir(), "monitors")); !os.IsNotExist(err) {
		t.Fatalf("remote monitor invocation touched local ownership files: %v", err)
	}
}

// TestOpenDurableAttentionFeedPersistsBusAndActuationEvents: the internal
// monitor publishes agent lifecycle events on its process bus, and `ntm
// coordinator run` publishes actuation records on the global feed. Both used
// to stay in that process (webhooks only / an in-memory feed). Through the
// durable feed they reach the store that --robot-attention reads (bd-viwo4).
func TestOpenDurableAttentionFeedPersistsBusAndActuationEvents(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NTM_CONFIG", filepath.Join(dir, "config.toml"))
	previous := robot.GetAttentionFeed()
	t.Cleanup(func() { robot.SetAttentionFeed(previous) })
	wasLive := busEventsPersistedLive.Load()
	t.Cleanup(func() { busEventsPersistedLive.Store(wasLive) })

	closeAttention, err := openDurableAttentionFeed()
	if err != nil {
		t.Fatalf("openDurableAttentionFeed: %v", err)
	}
	events.DefaultBus.PublishSync(events.NewWebhookEvent(events.WebhookAgentCrashed, "durable-attention", "2", "claude", "agent crashed", nil))
	robot.GetAttentionFeed().PublishActuation(robot.ActuationRecord{
		Session: "durable-attention",
		Action:  "rotate",
		Stage:   robot.ActuationStageRequest,
		Summary: "context rotation enqueued for durable-attention__cc_2",
	})
	closeAttention()

	store, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("reopen state store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	stored, err := store.GetAttentionEventsSince(0, 100)
	if err != nil {
		t.Fatalf("read attention events: %v", err)
	}
	var crash, actuation bool
	for _, event := range stored {
		if event.SessionName != "durable-attention" {
			continue
		}
		switch {
		case strings.Contains(event.Summary, "rotation enqueued"):
			actuation = true
		case event.Source == "event_bus.webhook" && event.Pane == "2" && event.Actionability == "action_required":
			crash = true // the normalizer files a crash as an agent error that needs action
		}
	}
	if !crash || !actuation {
		t.Fatalf("durable store events (crash=%v actuation=%v): %+v", crash, actuation, stored)
	}
	if !busEventsPersistedLive.Load() {
		t.Fatal("a live bus subscription must mark the process so its exit does not replay events")
	}
}

// The monitor emits its session-ended event through the async emitter right
// before it returns; closing the durable feed must not drop it.
func TestDurableAttentionFeedCloseDeliversEventsEmittedJustBefore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NTM_CONFIG", filepath.Join(dir, "config.toml"))
	previous := robot.GetAttentionFeed()
	t.Cleanup(func() { robot.SetAttentionFeed(previous) })
	wasLive := busEventsPersistedLive.Load()
	t.Cleanup(func() { busEventsPersistedLive.Store(wasLive) })

	closeAttention, err := openDurableAttentionFeed()
	if err != nil {
		t.Fatalf("openDurableAttentionFeed: %v", err)
	}
	events.DefaultEmitter().Emit(events.NewWebhookEvent(events.WebhookSessionEnded, "close-drain", "", "", "Session close-drain ended", nil))
	closeAttention()

	store, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("reopen state store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	stored, err := store.GetAttentionEventsSince(0, 100)
	if err != nil {
		t.Fatalf("read attention events: %v", err)
	}
	for _, event := range stored {
		if event.SessionName == "close-drain" && strings.Contains(event.Summary, "session ended") {
			return
		}
	}
	t.Fatalf("session-ended event emitted just before close was not stored: %+v", stored)
}

// A short-lived command's webhook events reach the durable feed at exit: the
// emitter publishes asynchronously, and the process used to exit with them
// (bd-viwo4). A process whose feed subscribed live skips the replay, so nothing
// is stored twice.
func TestPersistEmittedBusEventsStoresShortLivedCommandEvents(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NTM_CONFIG", filepath.Join(dir, "config.toml"))
	previous := robot.GetAttentionFeed()
	t.Cleanup(func() { robot.SetAttentionFeed(previous) })
	wasLive := busEventsPersistedLive.Load()
	t.Cleanup(func() { busEventsPersistedLive.Store(wasLive) })
	if robotStateStore != nil {
		t.Fatal("robot persistence is unexpectedly open")
	}
	stored := func(session string) []state.StoredAttentionEvent {
		t.Helper()
		store, err := state.Open(filepath.Join(dir, "state.db"))
		if err != nil {
			t.Fatalf("open state store: %v", err)
		}
		defer store.Close()
		if err := store.Migrate(); err != nil {
			t.Fatalf("migrate state store: %v", err)
		}
		all, err := store.GetAttentionEventsSince(0, 100)
		if err != nil {
			t.Fatalf("read attention events: %v", err)
		}
		var rows []state.StoredAttentionEvent
		for _, event := range all {
			if event.SessionName == session {
				rows = append(rows, event)
			}
		}
		return rows
	}
	emitAndPersist := func(session string, live bool) {
		t.Helper()
		bus := events.NewEventBus(100)
		emitter := events.NewEventEmitter(bus, 16)
		busEventsPersistedLive.Store(live)
		emitter.Emit(events.NewWebhookEvent(events.WebhookBeadFailed, session, "", "", "bead failed", map[string]string{"bead_id": "bd-1"}))
		emitter.Emit(events.NewWebhookEvent(events.WebhookSessionCreated, session, "", "", "session created", nil))
		persistEmittedBusEvents(emitter, bus)
	}

	emitAndPersist("live-subscriber", true)
	if rows := stored("live-subscriber"); len(rows) != 0 {
		t.Fatalf("replayed events a live subscription already stored: %+v", rows)
	}

	emitAndPersist("plain-command", false)
	rows := stored("plain-command")
	if len(rows) != 2 || rows[0].Actionability != "action_required" || !strings.Contains(rows[0].Summary, "bead failed") || !strings.Contains(rows[1].Summary, "session created") || rows[0].Cursor >= rows[1].Cursor {
		t.Fatalf("plain command rows = %+v, want bead failed (action_required) then session created", rows)
	}

	if err := initializeRobotPersistence(context.Background(), false); err != nil {
		t.Fatalf("initializeRobotPersistence: %v", err)
	}
	emitAndPersist("robot-command", false)
	closeRobotPersistence()
	if rows := stored("robot-command"); len(rows) != 2 {
		t.Fatalf("robot command rows = %+v, want both emitted events through its persistence feed", rows)
	}
}

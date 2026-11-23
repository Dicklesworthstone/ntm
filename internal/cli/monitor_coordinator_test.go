package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// startMonitorCoordinatorHostForTest runs the host the internal monitor
// starts, with cadences shortened for tests. The returned stop joins it.
func startMonitorCoordinatorHostForTest(t *testing.T, session, project string) func() {
	t.Helper()
	host := newMonitorCoordinatorHost(session, project)
	host.pollEvery, host.reloadEvery, host.maintenanceEvery = 4*time.Millisecond, 15*time.Millisecond, 8*time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		host.run(ctx)
	}()
	stopped := false
	stop := func() {
		t.Helper()
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("monitor coordinator host did not stop")
		}
	}
	t.Cleanup(stop)
	return stop
}

func waitForMonitorCoordinator(t *testing.T, session, what string, accept func(*resilience.SessionCoordinatorStatus) bool) *resilience.SessionCoordinatorStatus {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		status, err := resilience.ReadSessionCoordinatorStatus(session)
		if err == nil && accept(status) {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: status=%+v err=%v", what, status, err)
		}
		time.Sleep(3 * time.Millisecond)
	}
}

func monitorCoordinatorRunning(mode string, features ...string) func(*resilience.SessionCoordinatorStatus) bool {
	if features == nil {
		features = []string{}
	}
	return func(status *resilience.SessionCoordinatorStatus) bool {
		return status.Alive && status.State == resilience.SessionCoordinatorRunning && status.Mode == mode &&
			reflect.DeepEqual(status.Features, features)
	}
}

// prepareMonitorCoordinatorFixture isolates the monitor control files and
// rewrites the fixture's config. bv always fails, so enabled auto-assignment
// can never reach real triage.
func prepareMonitorCoordinatorFixture(t *testing.T, unobservableSibling bool, configTOML string) *coordinatorMaintenanceCLIFixture {
	t.Helper()
	f := newCoordinatorMaintenanceCLIFixture(t, unobservableSibling)
	t.Setenv("XDG_DATA_HOME", filepath.Join(os.Getenv("HOME"), "data"))
	if err := os.WriteFile(cfgFile, []byte(configTOML), 0600); err != nil {
		t.Fatal(err)
	}
	binDir := os.Getenv("NTM_MAINTENANCE_FIXTURE")
	if err := os.WriteFile(filepath.Join(binDir, "bv"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *coordinatorMaintenanceCLIFixture) brCalls(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(os.Getenv("NTM_MAINTENANCE_FIXTURE"), "br-calls"))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

func (f *coordinatorMaintenanceCLIFixture) calledTools() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.toolCalls...)
}

func runCoordinatorStatusJSON(t *testing.T, session string) coordinatorRuntimeView {
	t.Helper()
	cmd := newCoordinatorStatusCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{session})
	out, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatalf("coordinator status: %v; output=%s", err, out)
	}
	var response struct {
		Session string                 `json:"session"`
		Runtime coordinatorRuntimeView `json:"runtime"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil || response.Session != session {
		t.Fatalf("coordinator status output=%s decode=%v", out, err)
	}
	return response.Runtime
}

// TestMonitorCoordinatorMaintainsLeasesWithNoFeatureEnabled: with every
// [coordinator] feature at its default (off), the coordinator the session
// monitor hosts still renews the exact leases of in-progress work and releases
// the leases of closed work — the one-shot `ntm assign` lifecycle that used to
// need `assign --watch` or `coordinator run`. It never registers an identity,
// sends mail, or reserves new files, repeats no lease effect, and `ntm
// coordinator status` reports where it runs.
func TestMonitorCoordinatorMaintainsLeasesWithNoFeatureEnabled(t *testing.T) {
	f := prepareMonitorCoordinatorFixture(t, false, "[coordinator]\nauto_assign = false\n")
	stop := startMonitorCoordinatorHostForTest(t, f.session, f.project)

	select {
	case <-f.renewed:
	case <-time.After(15 * time.Second):
		t.Fatal("the monitor-hosted coordinator never renewed the in-progress assignment's leases")
	}
	status := waitForMonitorCoordinator(t, f.session, "a clean maintenance pass", func(status *resilience.SessionCoordinatorStatus) bool {
		return monitorCoordinatorRunning(monitorCoordinatorModeMaintenance)(status) && status.LastMaintenanceAt != nil
	})
	if status.MaintenanceError != "" || status.Error != "" || status.Host != resilience.SessionCoordinatorHostMonitor ||
		status.ProjectKey != f.project || status.ConfigPath != cfgFile || status.PID != os.Getpid() {
		t.Fatalf("hosted coordinator status = %+v", status)
	}
	if err := f.store.LoadStrict(); err != nil {
		t.Fatal(err)
	}
	if active := f.store.Get("ntm-maintenance"); active.Status != assignment.StatusWorking || active.ReservationRenewalError != "" ||
		active.ReservationExpiresAt.Before(time.Now().Add(time.Hour)) {
		t.Fatalf("in-progress assignment lost protection: %+v", active)
	}
	if finished := f.store.Get("ntm-finished"); finished.Status != assignment.StatusCompleted || len(finished.ReservationIDs) != 0 {
		t.Fatalf("closed assignment kept stale leases: %+v", finished)
	}
	// Further passes find nothing due: no lease is renewed or released twice.
	passes := f.brCalls(t)
	deadline := time.Now().Add(10 * time.Second)
	for f.brCalls(t) < passes+3 {
		if time.Now().After(deadline) {
			t.Fatal("maintenance passes stopped after the first")
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.mu.Lock()
	renewals, releases := renewalIDs(f), append([][]int(nil), f.releaseIDs...)
	f.mu.Unlock()
	if !reflect.DeepEqual(renewals, []agentmailRenewal{{agent: "BlueLake", ids: "941,942"}}) || !reflect.DeepEqual(releases, [][]int{{961}}) {
		t.Fatalf("lease effects repeated or widened: renewals=%v releases=%v", renewals, releases)
	}
	for _, tool := range f.calledTools() {
		if tool == "register_agent" || tool == "send_message" || tool == "file_reservation_paths" {
			t.Fatalf("maintenance-only coordinator acted beyond maintenance: %v", f.calledTools())
		}
	}

	runtime := runCoordinatorStatusJSON(t, f.session)
	if runtime.Host != resilience.SessionCoordinatorHostMonitor || runtime.State != resilience.SessionCoordinatorRunning ||
		runtime.Mode != monitorCoordinatorModeMaintenance || len(runtime.Features) != 0 || !runtime.Healthy ||
		!strings.Contains(runtime.Summary, "running inside the session monitor") || runtime.LastMaintenanceAt == nil {
		t.Fatalf("coordinator status runtime = %+v", runtime)
	}

	stop()
	if stopped, err := resilience.ReadSessionCoordinatorStatus(f.session); err != nil || stopped.State != resilience.SessionCoordinatorStopped || stopped.Alive {
		t.Fatalf("stopped host status = %+v err=%v", stopped, err)
	}
	if runtime := readCoordinatorRuntime(f.session); runtime.State != coordinatorRuntimeNotRunning || runtime.Host != coordinatorRuntimeHostNone {
		t.Fatalf("status after the monitor stopped = %+v", runtime)
	}
}

type agentmailRenewal struct {
	agent string
	ids   string
}

func renewalIDs(f *coordinatorMaintenanceCLIFixture) []agentmailRenewal {
	out := make([]agentmailRenewal, 0, len(f.renewals))
	for _, renewal := range f.renewals {
		ids := make([]string, 0, len(renewal.ReservationIDs))
		for _, id := range renewal.ReservationIDs {
			ids = append(ids, fmt.Sprint(id))
		}
		out = append(out, agentmailRenewal{agent: renewal.AgentName, ids: strings.Join(ids, ",")})
	}
	return out
}

// TestInternalMonitorHostsSessionCoordinator drives the hidden
// `internal-monitor` surface `ntm spawn` launches: with no coordinator feature
// enabled and no `ntm coordinator run`, the session monitor renews the leases
// of in-progress work and releases those of closed work, `ntm coordinator
// status` reports it, and the stop `ntm kill` sends joins the coordinator
// before the monitor releases its lease.
func TestInternalMonitorHostsSessionCoordinator(t *testing.T) {
	f := prepareMonitorCoordinatorFixture(t, false, "[coordinator]\nauto_assign = false\n")
	t.Setenv("NTM_INTERNAL_MONITOR_GENERATION", "")
	// The monitor also starts supervised daemons; keep it from launching the
	// host machine's real ones.
	t.Setenv("PATH", strings.Join([]string{os.Getenv("NTM_MAINTENANCE_FIXTURE"), "/usr/bin", "/bin"}, string(os.PathListSeparator)))
	previousFeed, wasLive := robot.GetAttentionFeed(), busEventsPersistedLive.Load()
	t.Cleanup(func() {
		robot.SetAttentionFeed(previousFeed)
		busEventsPersistedLive.Store(wasLive)
	})
	if err := resilience.SaveManifest(&resilience.SpawnManifest{Session: f.session, ProjectDir: f.project}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := newMonitorCmd()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{f.session})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()
	exited := false
	t.Cleanup(func() {
		cancel()
		if !exited {
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Error("session monitor did not exit")
			}
		}
	})

	select {
	case <-f.renewed:
	case err := <-done:
		exited = true
		t.Fatalf("session monitor exited before maintaining assignment leases: %v", err)
	case <-ctx.Done():
		t.Fatal("the session monitor never renewed the in-progress assignment's leases")
	}
	status := waitForMonitorCoordinator(t, f.session, "the monitor-hosted maintenance pass", func(status *resilience.SessionCoordinatorStatus) bool {
		return monitorCoordinatorRunning(monitorCoordinatorModeMaintenance)(status) && status.LastMaintenanceAt != nil
	})
	if status.PID != os.Getpid() || status.MaintenanceError != "" {
		t.Fatalf("hosted coordinator status = %+v", status)
	}
	if err := f.store.LoadStrict(); err != nil {
		t.Fatal(err)
	}
	if finished := f.store.Get("ntm-finished"); finished.Status != assignment.StatusCompleted || len(finished.ReservationIDs) != 0 {
		t.Fatalf("closed assignment kept stale leases: %+v", finished)
	}
	if runtime := runCoordinatorStatusJSON(t, f.session); runtime.Host != resilience.SessionCoordinatorHostMonitor ||
		runtime.State != resilience.SessionCoordinatorRunning || !strings.Contains(runtime.Summary, "running inside the session monitor") {
		t.Fatalf("coordinator status runtime = %+v", runtime)
	}

	if err := resilience.StopSessionMonitor(ctx, f.session); err != nil {
		t.Fatalf("stop session monitor: %v", err)
	}
	select {
	case err := <-done:
		exited = true
		if err != nil {
			t.Fatalf("session monitor returned an error after a clean stop: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("session monitor did not return after stop")
	}
	if stopped, err := resilience.ReadSessionCoordinatorStatus(f.session); err != nil || stopped.State != resilience.SessionCoordinatorStopped {
		t.Fatalf("coordinator outlived its monitor: %+v err=%v", stopped, err)
	}
}

// TestMonitorCoordinatorYieldsToForegroundCoordinatorRun: a foreground
// coordinator for the same session pauses the monitor-hosted one, which does
// no maintenance while paused and resumes once the foreground exits; `ntm
// coordinator run --once` reports that it paused the monitor.
func TestMonitorCoordinatorYieldsToForegroundCoordinatorRun(t *testing.T) {
	f := prepareMonitorCoordinatorFixture(t, false, "[coordinator]\nauto_assign = false\n")
	startMonitorCoordinatorHostForTest(t, f.session, f.project)
	waitForMonitorCoordinator(t, f.session, "the first maintenance pass", func(status *resilience.SessionCoordinatorStatus) bool {
		return monitorCoordinatorRunning(monitorCoordinatorModeMaintenance)(status) && status.LastMaintenanceAt != nil
	})

	claim, err := resilience.ClaimSessionCoordinator(t.Context(), f.session)
	if err != nil {
		t.Fatal(err)
	}
	if !claim.Preempted {
		t.Fatal("claim did not have to wait for the running monitor coordinator")
	}
	waitForMonitorCoordinator(t, f.session, "the monitor to yield", func(status *resilience.SessionCoordinatorStatus) bool {
		return status.Alive && status.State == resilience.SessionCoordinatorYielded
	})
	if runtime := readCoordinatorRuntime(f.session); !strings.Contains(runtime.Summary, "paused inside the session monitor") {
		t.Fatalf("status while paused = %+v", runtime)
	}
	paused := f.brCalls(t)
	time.Sleep(6 * 20 * time.Millisecond)
	if after := f.brCalls(t); after != paused {
		t.Fatalf("paused monitor coordinator kept maintaining: br calls %d -> %d", paused, after)
	}
	claim.Release()
	waitForMonitorCoordinator(t, f.session, "the monitor to resume", monitorCoordinatorRunning(monitorCoordinatorModeMaintenance))
	deadline := time.Now().Add(10 * time.Second)
	for f.brCalls(t) == paused {
		if time.Now().After(deadline) {
			t.Fatal("resumed monitor coordinator never maintained again")
		}
		time.Sleep(20 * time.Millisecond)
	}

	var output bytes.Buffer
	cmd := newCoordinatorRunCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetOut(&output)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{f.session, "--once"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("coordinator run --once: %v output=%s", err, output.String())
	}
	var response coordinatorRunOutput
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("decode coordinator output: %v\n%s", err, output.String())
	}
	if !response.Success || !response.Once || !response.MonitorYielded {
		t.Fatalf("foreground cycle did not pause the monitor coordinator: %+v", response)
	}
	waitForMonitorCoordinator(t, f.session, "the monitor to resume after coordinator run", monitorCoordinatorRunning(monitorCoordinatorModeMaintenance))
}

// TestMonitorCoordinatorAppliesPersistedTogglesWithoutRestart: `ntm
// coordinator enable auto-assign` switches the running monitor's coordinator
// to the full loop (which registers the coordinator's identity) without
// restarting anything, and `disable` returns it to maintenance only.
func TestMonitorCoordinatorAppliesPersistedTogglesWithoutRestart(t *testing.T) {
	f := prepareMonitorCoordinatorFixture(t, false, "[coordinator]\nauto_assign = false\npoll_interval = '100ms'\n")
	startMonitorCoordinatorHostForTest(t, f.session, f.project)
	before := waitForMonitorCoordinator(t, f.session, "maintenance mode", monitorCoordinatorRunning(monitorCoordinatorModeMaintenance))
	if slices.Contains(f.calledTools(), "register_agent") {
		t.Fatalf("maintenance mode registered an identity: %v", f.calledTools())
	}

	toggle := func(enable bool) {
		t.Helper()
		cmd := newCoordinatorDisableCmd()
		if enable {
			cmd = newCoordinatorEnableCmd()
		}
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"auto-assign"})
		if out, err := captureStdout(t, cmd.Execute); err != nil {
			t.Fatalf("toggle auto-assign enable=%t: %v output=%s", enable, err, out)
		}
	}
	toggle(true)
	full := waitForMonitorCoordinator(t, f.session, "the full coordinator", monitorCoordinatorRunning(monitorCoordinatorModeFull, "auto-assign"))
	if full.PID != before.PID || full.Error != "" {
		t.Fatalf("toggle restarted the host or failed: before=%+v after=%+v", before, full)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !slices.Contains(f.calledTools(), "register_agent") {
		if time.Now().After(deadline) {
			t.Fatalf("full coordinator never registered its identity: %v", f.calledTools())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if runtime := runCoordinatorStatusJSON(t, f.session); runtime.Mode != monitorCoordinatorModeFull ||
		!reflect.DeepEqual(runtime.Features, []string{"auto-assign"}) || !strings.Contains(runtime.Summary, "with auto-assign") {
		t.Fatalf("coordinator status runtime = %+v", runtime)
	}

	toggle(false)
	waitForMonitorCoordinator(t, f.session, "maintenance mode again", monitorCoordinatorRunning(monitorCoordinatorModeMaintenance))
}

// TestMonitorCoordinatorFailsClosedWhenAssignmentPolicyDoesNotLoad: enabled
// auto-assignment whose safety policy cannot load must not admit work; the
// host keeps maintaining leases and reports why.
func TestMonitorCoordinatorFailsClosedWhenAssignmentPolicyDoesNotLoad(t *testing.T) {
	f := prepareMonitorCoordinatorFixture(t, false, "[coordinator]\nauto_assign = true\n")
	if err := os.MkdirAll(filepath.Join(f.project, ".ntm"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.project, ".ntm", "config.toml"), []byte("[assign\nbroken"), 0600); err != nil {
		t.Fatal(err)
	}
	startMonitorCoordinatorHostForTest(t, f.session, f.project)
	status := waitForMonitorCoordinator(t, f.session, "fail-closed maintenance", func(status *resilience.SessionCoordinatorStatus) bool {
		return monitorCoordinatorRunning(monitorCoordinatorModeMaintenance)(status) && status.LastMaintenanceAt != nil
	})
	if !strings.Contains(status.Error, "auto-assign is disabled: assignment safety policy did not load") {
		t.Fatalf("policy failure was not reported: %+v", status)
	}
	select {
	case <-f.renewed:
	case <-time.After(15 * time.Second):
		t.Fatal("maintenance stopped with the admission policy")
	}
	for _, tool := range f.calledTools() {
		if tool == "register_agent" || tool == "send_message" || tool == "file_reservation_paths" {
			t.Fatalf("coordinator admitted work without its safety policy: %v", f.calledTools())
		}
	}
}

// TestMonitorCoordinatorSettingsReloadIsStable: re-reading an unchanged config
// must not restart the hosted coordinator (a restart resets its nudge and
// conflict cooldowns); changing any coordinator-relevant setting must.
func TestMonitorCoordinatorSettingsReloadIsStable(t *testing.T) {
	isolateSessionAgentStorage(t)
	previousConfig := cfgFile
	t.Cleanup(func() { cfgFile = previousConfig })
	project := t.TempDir()
	cfgFile = filepath.Join(t.TempDir(), "config.toml")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(cfgFile, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("[coordinator]\nsend_digests = true\ndigest_interval = \"30m\"\n\n[integrations.caam]\nauto_failover = true\n")
	stubCoordinatorLiveTopology(t, []tmux.Pane{{ID: "%1", Index: 1}}, map[string]string{"%1": project})
	host := newMonitorCoordinatorHost("settings-reload", project)

	first, err := host.loadSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := host.loadSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !first.equal(second) {
		t.Fatal("an unchanged config reloaded as different settings")
	}
	if first.mode() != monitorCoordinatorModeFull || !reflect.DeepEqual(first.features(), []string{"digest", "caam-failover"}) ||
		first.projectKey != project || first.runtime.DigestInterval != 30*time.Minute {
		t.Fatalf("settings = mode %s features %v project %s runtime %+v", first.mode(), first.features(), first.projectKey, first.runtime)
	}

	write("[coordinator]\nsend_digests = true\ndigest_interval = \"45m\"\n\n[integrations.caam]\nauto_failover = true\n")
	if changed, err := host.loadSettings(t.Context()); err != nil || first.equal(changed) {
		t.Fatalf("digest interval change was not detected: err=%v", err)
	}
	write("[coordinator]\nsend_digests = true\ndigest_interval = \"30m\"\n\n[integrations.caam]\nauto_failover = false\n")
	if changed, err := host.loadSettings(t.Context()); err != nil || first.equal(changed) || !reflect.DeepEqual(changed.features(), []string{"digest"}) {
		t.Fatalf("CAAM toggle change was not detected: err=%v", err)
	}
	write("[coordinator]\nsend_digests = false\n")
	if changed, err := host.loadSettings(t.Context()); err != nil || changed.mode() != monitorCoordinatorModeMaintenance || len(changed.features()) != 0 {
		t.Fatalf("all features off = mode %s features %v err=%v", changed.mode(), changed.features(), err)
	}
	write("[coordinator\n")
	if _, err := host.loadSettings(t.Context()); err == nil {
		t.Fatal("a broken config loaded")
	}
}

// TestMonitorCoordinatorLeavesConflictNotifyToCoordinatorRun: configs written
// by `ntm config init` up to v1.36.1 carry conflict_notify = true (the old
// default). The monitor every session runs must not turn that into
// project-wide conflict mail and must say why in its status, while
// `ntm coordinator run` keeps the setting and a deliberate conflict-negotiate
// opt-in still switches the monitor to the full loop.
func TestMonitorCoordinatorLeavesConflictNotifyToCoordinatorRun(t *testing.T) {
	isolateSessionAgentStorage(t)
	previousConfig := cfgFile
	t.Cleanup(func() { cfgFile = previousConfig })
	project := t.TempDir()
	cfgFile = filepath.Join(t.TempDir(), "config.toml")
	stubCoordinatorLiveTopology(t, []tmux.Pane{{ID: "%1", Index: 1}}, map[string]string{"%1": project})
	host := newMonitorCoordinatorHost("conflict-notify-legacy", project)

	if err := os.WriteFile(cfgFile, []byte("[coordinator]\nconflict_notify = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	legacy, err := host.loadSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if legacy.runtime.ConflictNotify || legacy.mode() != monitorCoordinatorModeMaintenance || len(legacy.features()) != 0 {
		t.Fatalf("legacy conflict_notify = true: notify=%t mode=%s features=%v, want notify off in maintenance mode",
			legacy.runtime.ConflictNotify, legacy.mode(), legacy.features())
	}
	if !strings.Contains(legacy.warning, "ntm coordinator run") {
		t.Fatalf("monitor status does not explain the skipped conflict-notify: warning=%q", legacy.warning)
	}
	if foreground, _ := loadCoordinatorRuntimeConfigWithNTM(); !foreground.ConflictNotify {
		t.Fatal("`ntm coordinator run` lost conflict_notify = true; only the monitor may skip it")
	}

	if err := os.WriteFile(cfgFile, []byte("[coordinator]\nconflict_notify = true\nconflict_negotiate = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	negotiate, err := host.loadSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if negotiate.runtime.ConflictNotify || !negotiate.runtime.ConflictNegotiate || negotiate.mode() != monitorCoordinatorModeFull ||
		!reflect.DeepEqual(negotiate.features(), []string{"conflict-negotiate"}) {
		t.Fatalf("conflict_negotiate opt-in: notify=%t negotiate=%t mode=%s features=%v",
			negotiate.runtime.ConflictNotify, negotiate.runtime.ConflictNegotiate, negotiate.mode(), negotiate.features())
	}
}

// TestWatchLoopPausesMonitorCoordinatorWhileMaintaining: watch mode consumes
// the completion events its own maintenance records, so it claims the session
// before its first maintenance pass and the monitor's coordinator stays paused
// until watch mode exits.
func TestWatchLoopPausesMonitorCoordinatorWhileMaintaining(t *testing.T) {
	isolateSessionAgentStorage(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(os.Getenv("HOME"), "data"))
	const session = "watch-claims-coordination"
	host, err := resilience.OpenSessionCoordinatorHost(session)
	if err != nil {
		t.Skip(err)
	}
	defer host.Close()
	if owned, err := host.Activate(); err != nil || !owned {
		t.Fatalf("monitor side could not coordinate: owned=%t err=%v", owned, err)
	}

	loop := NewWatchLoop(session, assignment.NewStore(session), &AutoReassignOptions{
		Session: session, ProjectDir: t.TempDir(), Quiet: true, ReserveFiles: true,
	})
	loop.scanInterval = time.Hour
	loop.maintenanceInterval = 5 * time.Millisecond
	var passes atomic.Int32
	loop.newMaintainer = func(context.Context, string, string) (assignWatchMaintainer, error) {
		return watchAssignmentMaintainerFunc(func(context.Context) error {
			passes.Add(1)
			return nil
		}), nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runResult := make(chan error, 1)
	go func() { runResult <- loop.Run(ctx) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		pending, err := host.ClaimPending()
		if err != nil {
			t.Fatal(err)
		}
		if pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("watch mode never claimed session coordination")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if passes.Load() != 0 {
		t.Fatalf("watch maintained %d time(s) while the monitor still coordinated", passes.Load())
	}
	host.Deactivate()
	for passes.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("watch maintenance never started after the monitor yielded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if owned, err := host.Activate(); err != nil || owned {
		t.Fatalf("monitor resumed while watch mode maintained: owned=%t err=%v", owned, err)
	}
	cancel()
	select {
	case err := <-runResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("watch Run() = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("watch mode did not stop")
	}
	if owned, err := host.Activate(); err != nil || !owned {
		t.Fatalf("monitor could not resume after watch mode exited: owned=%t err=%v", owned, err)
	}
}

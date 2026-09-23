package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/completion"
	"github.com/Dicklesworthstone/ntm/internal/config"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestAncillaryDispatchCancellationPreventsPostReturnEnter(t *testing.T) {
	pane := tmux.Pane{ID: "%9101", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude, Title: "cancel_cc_1"}
	tests := []struct {
		name     string
		dispatch func(context.Context, *dispatchsvc.Service, string, []tmux.Pane, []tmux.Pane, string) (dispatchsvc.Result, error)
	}{
		{name: "file watch", dispatch: dispatchWatchCommand},
		{name: "replay", dispatch: dispatchReplayPrompt},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			staged := make(chan struct{})
			entered := make(chan struct{}, 1)
			service, err := dispatchsvc.NewService(dispatchsvc.Ports{
				Redactor:  dispatchsvc.AllowAllRedactor{},
				Protocols: shellDispatchProtocolPlanner{},
				Deliverer: dispatchsvc.DelivererFunc(func(ctx context.Context, delivery dispatchsvc.Delivery) error {
					if delivery.Protocol != dispatchsvc.ProtocolDoubleEnter {
						return errors.New("expected double-enter protocol")
					}
					close(staged)
					timer := time.NewTimer(80 * time.Millisecond)
					defer timer.Stop()
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-timer.C:
						entered <- struct{}{}
						return nil
					}
				}),
			})
			if err != nil {
				t.Fatalf("NewService() error = %v", err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				_, dispatchErr := tc.dispatch(ctx, service, "cancel", []tmux.Pane{pane}, []tmux.Pane{pane}, "staged prompt")
				done <- dispatchErr
			}()

			select {
			case <-staged:
			case <-time.After(time.Second):
				t.Fatal("delivery never staged the prompt")
			}
			cancel()

			select {
			case dispatchErr := <-done:
				if !errors.Is(dispatchErr, context.Canceled) {
					t.Fatalf("dispatch error = %v, want context.Canceled", dispatchErr)
				}
			case <-time.After(time.Second):
				t.Fatal("dispatch did not return after cancellation")
			}

			select {
			case <-entered:
				t.Fatal("Enter was submitted after canceled dispatch returned")
			case <-time.After(120 * time.Millisecond):
			}
		})
	}
}

func TestParseWatchInterval(t *testing.T) {

	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{name: "default", input: "", want: 250 * time.Millisecond},
		{name: "duration", input: "2s", want: 2 * time.Second},
		{name: "milliseconds integer", input: "500", want: 500 * time.Millisecond},
		{name: "invalid", input: "abc", wantErr: true},
		{name: "zero invalid", input: "0", wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseWatchInterval(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseWatchInterval returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("duration = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExtractBeadMentions(t *testing.T) {

	re, err := beadMentionRegexp("bd-123")
	if err != nil {
		t.Fatalf("beadMentionRegexp error: %v", err)
	}

	input := "working on bd-123 now\nnoise line\nbd-1234 should not match\nDone with BD-123"
	got := extractBeadMentions(input, re)

	if len(got) != 2 {
		t.Fatalf("mentions count = %d, want 2", len(got))
	}
	if got[0] != "working on bd-123 now" {
		t.Fatalf("first mention = %q", got[0])
	}
	if got[1] != "Done with BD-123" {
		t.Fatalf("second mention = %q", got[1])
	}
}

func TestFilterPanesCanonicalizesAliases(t *testing.T) {

	panes := []tmux.Pane{
		{Index: 0, Type: tmux.AgentUser, Title: "user_0"},
		{Index: 1, Type: tmux.AgentType("claude_code"), Title: "cc_1"},
		{Index: 2, Type: tmux.AgentType("openai-codex"), Title: "cod_2"},
		{Index: 3, Type: tmux.AgentType("google-gemini"), Title: "gmi_3"},
	}

	tests := []struct {
		name string
		opts watchOptions
		want []int
	}{
		{name: "claude alias", opts: watchOptions{filterClaude: true}, want: []int{1}},
		{name: "codex alias", opts: watchOptions{filterCodex: true}, want: []int{2}},
		{name: "gemini alias", opts: watchOptions{filterGemini: true}, want: []int{3}},
		{name: "multiple aliases", opts: watchOptions{filterClaude: true, filterGemini: true}, want: []int{1, 3}},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := filterPanes(panes, tc.opts)
			if len(got) != len(tc.want) {
				t.Fatalf("filterPanes(%+v) len = %d, want %d", tc.opts, len(got), len(tc.want))
			}
			for i, wantIdx := range tc.want {
				if got[i].Index != wantIdx {
					t.Fatalf("filterPanes(%+v)[%d].Index = %d, want %d", tc.opts, i, got[i].Index, wantIdx)
				}
			}
		})
	}
}

func TestResolveWatchProjectDir_ExplicitUsesSavedSessionProject(t *testing.T) {
	isolateSessionAgentStorage(t)
	session := fmt.Sprintf("saved-watch-project-%d", time.Now().UnixNano())

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	cfg = &config.Config{ProjectsBase: t.TempDir()}

	cwdRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatal(err)
	}

	actualProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(actualProject, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	saveSessionAgentForTest(t, session, actualProject, "GreenCastle")

	got, err := resolveWatchProjectDir(t.Context(), session, false)
	if err != nil {
		t.Fatalf("resolveWatchProjectDir() error = %v", err)
	}
	if got != actualProject {
		t.Fatalf("resolveWatchProjectDir() = %q, want %q", got, actualProject)
	}
}

func TestResolveWatchProjectDir_ExplicitRejectsWorkspaceFallback(t *testing.T) {
	isolateSessionAgentStorage(t)
	session := fmt.Sprintf("missing-watch-project-%d", time.Now().UnixNano())

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	cfg = &config.Config{ProjectsBase: t.TempDir()}

	cwdRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatal(err)
	}

	if _, err := resolveWatchProjectDir(t.Context(), session, false); err == nil {
		t.Fatal("expected missing project root error")
	}
}

func TestWatchEventMatchesPattern_UsesWatchRootRelativePath(t *testing.T) {

	watchRoot := filepath.Join(string(filepath.Separator), "tmp", "actual-project")
	eventPath := filepath.Join(watchRoot, "internal", "cli", "watch.go")
	if !watchEventMatchesPattern("internal/cli/*.go", watchRoot, eventPath) {
		t.Fatalf("watchEventMatchesPattern should match nested path relative to watch root")
	}
}

func TestWatchEventMatchesPattern_RejectsOutsideWatchRoot(t *testing.T) {

	watchRoot := filepath.Join(string(filepath.Separator), "tmp", "actual-project")
	eventPath := filepath.Join(string(filepath.Separator), "tmp", "other-project", "internal", "cli", "watch.go")
	if watchEventMatchesPattern("internal/cli/*.go", watchRoot, eventPath) {
		t.Fatalf("watchEventMatchesPattern should reject paths outside the watch root")
	}
}

// ============================================================================
// FIX B: Periodic ready-work scan in the watch loop
// ============================================================================

// TestWatchLoop_PeriodicScanFiresWithoutCompletionEvents proves the regression
// fix: a freshly-started watch loop that dispatched nothing at startup (no idle
// agents OR no ready work) must NOT sit inert forever. The periodic ready-work
// scan ticker re-runs the plan/dispatch pass so work that becomes ready later
// (a gate unblocking, new beads, a startup-busy agent going idle) gets picked
// up even though no completion event ever fires.
//
// We inject scanFn to observe the ticker-driven scan without standing up tmux
// or bv: the empty assignment store guarantees the completion detector emits
// nothing, so the scan firing is solely the new ticker path.
func TestWatchLoop_PeriodicScanFiresWithoutCompletionEvents(t *testing.T) {
	isolateSessionAgentStorage(t)

	const session = "fixb"
	store := assignment.NewStore(session) // empty store ⇒ no completion events

	opts := &AutoReassignOptions{Session: session, Quiet: true}
	w := NewWatchLoop(session, store, opts)

	// Tight scan cadence so the test is fast; default completion poll interval
	// is whatever assignWatchInterval is, but the empty store means it never
	// produces an event regardless.
	w.scanInterval = 20 * time.Millisecond

	scanned := make(chan struct{}, 1)
	w.scanFn = func(context.Context) error {
		select {
		case scanned <- struct{}{}:
		default:
		}
		return nil
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- w.Run(ctx) }()

	select {
	case <-scanned:
		// Ticker-driven scan fired with zero completion events — the fix works.
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("periodic ready-work scan never fired; watch loop is inert without completion events")
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
		t.Fatal("watch loop did not shut down after context cancel")
	}
}

// TestWatchLoop_ScanReadyWorkNilOptsIsNoop guards that scanReadyWork degrades
// safely when no scan options are configured (it must not panic or dispatch).
func TestWatchLoop_ScanReadyWorkNilOptsIsNoop(t *testing.T) {
	isolateSessionAgentStorage(t)
	store := assignment.NewStore("fixb-nil")
	w := NewWatchLoop("fixb-nil", store, &AutoReassignOptions{Session: "fixb-nil", Quiet: true})
	w.scanOpts = nil
	if err := w.scanReadyWork(t.Context()); err != nil {
		t.Fatalf("scanReadyWork with nil scanOpts should be a no-op, got error: %v", err)
	}
}

type watchAssignmentMaintainerFunc func(context.Context) error

func (f watchAssignmentMaintainerFunc) MaintainAssignments(ctx context.Context) error {
	return f(ctx)
}

func TestWatchLoopAssignmentMaintenanceSkipsDryRunAndUnreservedWatches(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reserve bool
		dryRun  bool
	}{
		{name: "dry run", reserve: true, dryRun: true},
		{name: "without reservations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateSessionAgentStorage(t)
			const session = "watch-no-reservation-maintenance"
			loop := NewWatchLoop(session, assignment.NewStore(session), &AutoReassignOptions{
				Session: session, Quiet: true, ReserveFiles: tc.reserve, DryRun: tc.dryRun,
			})
			loop.scanInterval = time.Millisecond
			loop.maintenanceInterval = time.Millisecond
			var initializations atomic.Int32
			loop.newMaintainer = func(context.Context, string, string) (assignWatchMaintainer, error) {
				initializations.Add(1)
				return nil, errors.New("maintenance must not initialize")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var scans int
			loop.scanFn = func(context.Context) error {
				scans++
				if scans == 3 {
					cancel()
				}
				return nil
			}
			if err := loop.Run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("Run() error = %v, want cancellation after scans", err)
			}
			if initializations.Load() != 0 || scans != 3 {
				t.Fatalf("maintenance initializations=%d scans=%d, want 0/3", initializations.Load(), scans)
			}
		})
	}
}

func TestWatchLoopAssignmentMaintenanceContinuesDuringBlockedScanAndCompletion(t *testing.T) {
	isolateSessionAgentStorage(t)
	previousInterval := assignWatchInterval
	assignWatchInterval = 25 * time.Millisecond
	t.Cleanup(func() { assignWatchInterval = previousInterval })
	const session = "watch-maintenance-during-completion"
	project := t.TempDir()
	detectedAt := time.Now().UTC()
	store := assignment.NewStore(session)
	store.Assignments["ntm-watch-maintenance-completion"] = &assignment.Assignment{
		BeadID: "ntm-watch-maintenance-completion", Status: assignment.StatusCompleted,
		AssignedAt: detectedAt.Add(-time.Second), Pane: 1, DispatchTarget: "%902", OccupancyKey: "%902",
		PendingCompletionEventID: "maintenance-completion", CompletionDetectedAt: &detectedAt,
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed completion event: %v", err)
	}
	loop := NewWatchLoop(session, store, &AutoReassignOptions{
		Session: session, ProjectDir: project, Quiet: true, ReserveFiles: true,
	})
	loop.scanInterval = time.Millisecond
	loop.maintenanceInterval = 5 * time.Millisecond
	loop.delay = time.Hour
	loop.lastAssignmentAt = time.Now()
	var initializations, passes, concurrent, maxConcurrent atomic.Int32
	loop.newMaintainer = func(_ context.Context, gotSession, gotProject string) (assignWatchMaintainer, error) {
		initializations.Add(1)
		if gotSession != session || gotProject != project {
			return nil, fmt.Errorf("maintenance scope = %s/%s", gotSession, gotProject)
		}
		return watchAssignmentMaintainerFunc(func(ctx context.Context) error {
			active := concurrent.Add(1)
			defer concurrent.Add(-1)
			for previous := maxConcurrent.Load(); active > previous; previous = maxConcurrent.Load() {
				if maxConcurrent.CompareAndSwap(previous, active) {
					break
				}
			}
			passes.Add(1)
			return ctx.Err()
		}), nil
	}
	scanStarted := make(chan struct{})
	loop.scanFn = func(ctx context.Context) error {
		close(scanStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	handlerStarted := make(chan struct{})
	loop.handleCompletionFn = func(ctx context.Context, event completion.CompletionEvent) error {
		close(handlerStarted)
		return loop.handleCompletion(ctx, event)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runResult := make(chan error, 1)
	go func() { runResult <- loop.Run(ctx) }()
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			select {
			case <-runResult:
			case <-time.After(2 * time.Second):
				t.Error("watch did not join during cleanup")
			}
		}
	})
	for _, started := range []<-chan struct{}{scanStarted, handlerStarted} {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("ready scan or completion handler did not start")
		}
	}
	// The real handler holds the statistics mutex while waiting for --delay.
	// Holding that mutex must not block the maintenance worker's next pass.
	locked := false
	lockDeadline := time.Now().Add(time.Second)
	for time.Now().Before(lockDeadline) {
		if !loop.mu.TryLock() {
			locked = true
			break
		}
		loop.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	if !locked {
		t.Fatal("completion handler did not enter assignment pacing")
	}
	before := passes.Load()
	deadline := time.Now().Add(time.Second)
	for passes.Load() < before+2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if passes.Load() < before+2 {
		t.Fatal("assignment protection stopped while scan and completion handler were blocked")
	}
	cancel()
	select {
	case err := <-runResult:
		joined = true
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not join after cancellation")
	}
	if initializations.Load() != 1 || maxConcurrent.Load() != 1 || concurrent.Load() != 0 {
		t.Fatalf("maintenance initializations=%d max concurrent=%d remaining=%d", initializations.Load(), maxConcurrent.Load(), concurrent.Load())
	}
}

func TestWatchLoopAssignmentMaintenanceRetriesAndReportsDegradedProtection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		timeout  bool
		wantText string
	}{
		{name: "unavailable mail", wantText: "agent mail unavailable"},
		{name: "bounded pass", timeout: true, wantText: "context deadline exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateSessionAgentStorage(t)
			const session = "watch-maintenance-retry"
			loop := NewWatchLoop(session, assignment.NewStore(session), &AutoReassignOptions{
				Session: session, Quiet: true, ReserveFiles: true,
			})
			loop.scanInterval = time.Millisecond
			loop.maintenanceInterval = 5 * time.Millisecond
			loop.maintenanceTimeout = 200 * time.Millisecond
			var calls, scans atomic.Int32
			secondStarted := make(chan struct{})
			releaseSecond := make(chan struct{})
			thirdStarted := make(chan struct{})
			maintenanceStopped := make(chan struct{})
			loop.newMaintainer = func(context.Context, string, string) (assignWatchMaintainer, error) {
				return watchAssignmentMaintainerFunc(func(ctx context.Context) error {
					switch calls.Add(1) {
					case 1:
						if tc.timeout {
							<-ctx.Done()
							return ctx.Err()
						}
						return errors.New("agent mail unavailable")
					case 2:
						close(secondStarted)
						select {
						case <-releaseSecond:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					case 3:
						close(thirdStarted)
						<-ctx.Done()
						close(maintenanceStopped)
						return ctx.Err()
					default:
						return nil
					}
				}), nil
			}
			loop.scanFn = func(context.Context) error { scans.Add(1); return nil }
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			runResult := make(chan error, 1)
			go func() { runResult <- loop.Run(ctx) }()
			joined := false
			t.Cleanup(func() {
				cancel()
				if !joined {
					select {
					case <-runResult:
					case <-time.After(2 * time.Second):
						t.Error("watch did not join during cleanup")
					}
				}
			})
			select {
			case <-secondStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("failed maintenance was never retried")
			}
			if summary := loop.Summary(); !strings.Contains(summary, "reservation maintenance degraded") || !strings.Contains(summary, tc.wantText) {
				t.Fatalf("summary hides degraded protection: %s", summary)
			}
			close(releaseSecond)
			select {
			case <-thirdStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("maintenance did not continue after recovery")
			}
			if summary := loop.Summary(); strings.Contains(summary, "reservation maintenance degraded") {
				t.Fatalf("summary retained recovered maintenance error: %s", summary)
			}
			cancel()
			select {
			case err := <-runResult:
				joined = true
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Run() error = %v, want context.Canceled", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("watch did not return after cancellation")
			}
			select {
			case <-maintenanceStopped:
			default:
				t.Fatal("watch returned before its active maintenance pass stopped")
			}
			if scans.Load() == 0 {
				t.Fatal("maintenance failure blocked ready-work scans")
			}
		})
	}
}

func TestWatchLoopMaintainsExactDurableLeasesWithoutReadyWork(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, *coordinatorMaintenanceCLIFixture)
	}{
		{name: "healthy assignment"},
		{name: "missing exact lease", change: func(_ *testing.T, f *coordinatorMaintenanceCLIFixture) {
			for i := range f.reservations {
				if f.reservations[i].ID == 942 {
					f.reservations[i].ID = 1942
				}
			}
		}},
		{name: "expired lease", change: func(_ *testing.T, f *coordinatorMaintenanceCLIFixture) {
			for i := range f.reservations {
				if f.reservations[i].ID == 942 {
					f.reservations[i].ExpiresTS.Time = time.Now().Add(-time.Minute)
				}
			}
		}},
		{name: "different lease holder", change: func(_ *testing.T, f *coordinatorMaintenanceCLIFixture) {
			for i := range f.reservations {
				if f.reservations[i].ID == 942 {
					f.reservations[i].AgentName = "RedLake"
				}
			}
		}},
		{name: "replaced pane identity", change: func(t *testing.T, f *coordinatorMaintenanceCLIFixture) {
			registry, err := agentmail.LoadSessionAgentRegistry(f.session, f.project)
			if err != nil {
				t.Fatal(err)
			}
			registry.AddAgent(f.session+"__cc_1", "%94", "RedLake")
			if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCoordinatorMaintenanceCLIFixture(t, false)
			if tc.change != nil {
				func() {
					f.mu.Lock()
					defer f.mu.Unlock()
					tc.change(t, f)
				}()
			}
			previousInterval := assignWatchInterval
			assignWatchInterval = time.Hour
			t.Cleanup(func() { assignWatchInterval = previousInterval })
			before := f.store.Get("ntm-maintenance")
			loop := NewWatchLoop(f.session, f.store, &AutoReassignOptions{
				Session: f.session, ProjectDir: f.project, Quiet: true, ReserveFiles: true,
			})
			loop.stopWhenDone = false
			loop.scanInterval = time.Hour
			var scans atomic.Int32
			loop.scanFn = func(context.Context) error { scans.Add(1); return nil }
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			runResult := make(chan error, 1)
			go func() { runResult <- loop.Run(ctx) }()
			joined := false
			t.Cleanup(func() {
				cancel()
				if !joined {
					select {
					case <-runResult:
					case <-time.After(2 * time.Second):
						t.Error("watch did not join during cleanup")
					}
				}
			})
			var current *assignment.Assignment
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				latest, err := assignment.LoadStoreStrictReadOnly(f.session)
				if err == nil {
					current = latest.Get(before.BeadID)
					if current != nil && current.ReservationRenewalCheckedAt != nil {
						loop.maintenanceMu.Lock()
						reported := loop.maintenanceError
						loop.maintenanceMu.Unlock()
						if tc.change == nil || reported != "" {
							break
						}
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			if current == nil || current.ReservationRenewalCheckedAt == nil {
				t.Fatal("watch never maintained the inherited assignment without ready work")
			}
			cancel()
			select {
			case err := <-runResult:
				joined = true
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Run() error = %v, want context.Canceled", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("watch did not stop its canonical maintenance worker")
			}
			if scans.Load() != 0 {
				t.Fatalf("maintenance depended on %d ready-work scans", scans.Load())
			}
			if current.Status != assignment.StatusWorking || !reflect.DeepEqual(current.ReservationIDs, before.ReservationIDs) ||
				current.IdempotencyKey != before.IdempotencyKey || current.ClaimActor != before.ClaimActor {
				t.Fatalf("maintenance changed the active assignment owner: %+v", current)
			}
			// runWatchMode saves its original store at shutdown. Its merge must
			// retain renewal/terminal changes persisted by the separate worker.
			if err := f.store.Save(); err != nil {
				t.Fatalf("save original watcher snapshot: %v", err)
			}
			persisted := f.store.Get(before.BeadID)
			if !reflect.DeepEqual(persisted.ReservationExpiresAt, current.ReservationExpiresAt) || persisted.ReservationRenewalError != current.ReservationRenewalError {
				t.Fatalf("watch shutdown overwrote the maintenance receipt: before=%+v after=%+v", current, persisted)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if tc.change == nil {
				if len(f.renewals) != 1 || !reflect.DeepEqual(f.renewals[0].ReservationIDs, []int{941, 942}) || len(f.renewals[0].Paths) != 0 ||
					f.renewals[0].AgentName != "BlueLake" || f.renewals[0].ProjectKey != f.project || f.renewals[0].ExtendSeconds != 3600 {
					t.Fatalf("watch changed reservation scope: %+v", f.renewals)
				}
				if current.ReservationRenewalError != "" || current.ReservationExpiresAt == nil || !current.ReservationExpiresAt.After(time.Now().Add(50*time.Minute)) {
					t.Fatalf("watch did not persist verified lease protection: %+v", current)
				}
			} else if len(f.renewals) != 0 || current.ReservationRenewalError == "" || !current.ReservationExpiresAt.Equal(*before.ReservationExpiresAt) ||
				!strings.Contains(loop.Summary(), "reservation maintenance degraded") {
				t.Fatalf("unsafe lease was renewed or protection was overstated: calls=%+v assignment=%+v summary=%s", f.renewals, current, loop.Summary())
			}
			if len(f.releaseIDs) != 1 || !reflect.DeepEqual(f.releaseIDs[0], []int{961}) {
				t.Fatalf("watch terminal cleanup released unrelated leases: %+v", f.releaseIDs)
			}
			for _, call := range f.toolCalls {
				if call != "ensure_project" && call != "renew_file_reservations" && call != "release_file_reservations" {
					t.Fatalf("watch maintenance started unrelated automation: %v", f.toolCalls)
				}
			}
		})
	}
}

func TestWatchLoopStopWhenDoneUsesAssignmentsPersistedAfterStartup(t *testing.T) {
	isolateSessionAgentStorage(t)
	const session = "watch-stop-after-initial-dispatch"
	project := t.TempDir()
	stale := assignment.NewStore(session)
	loop := NewWatchLoop(session, stale, &AutoReassignOptions{
		Session: session, ProjectDir: project, policyProject: filepath.Clean(project), Quiet: true,
	})
	previousActionable, previousIdle := getActionableRecommendationsForWatch, getIdleAgentsForWatchStop
	t.Cleanup(func() {
		getActionableRecommendationsForWatch, getIdleAgentsForWatchStop = previousActionable, previousIdle
	})
	queries := 0
	getActionableRecommendationsForWatch = func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
		queries++
		return nil, nil // Initial dispatch has drained the ready queue.
	}
	getIdleAgentsForWatchStop = func(context.Context, string, string, bool) ([]assignAgentInfo, error) {
		return nil, nil
	}
	// The initial dispatch path uses its own durable store. Reproduce that
	// publication while leaving the watcher's pre-dispatch snapshot untouched.
	dispatchStore := assignment.NewStore(session)
	dispatched, err := dispatchStore.Assign("ntm-initial", "Initial task", 1, "cc", "BlueLake", "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(stale.ListActive()) != 0 {
		t.Fatal("test did not retain the watcher's original empty snapshot")
	}
	if stop, err := loop.shouldStop(t.Context()); err != nil || stop || queries != 0 {
		t.Fatalf("newly dispatched work lost its watcher: stop=%t err=%v ready_queries=%d", stop, err, queries)
	}
	if applied, err := dispatchStore.MarkCompletedIfCurrent(t.Context(), dispatched); err != nil || !applied {
		t.Fatalf("finish assignment from separate store: applied=%t err=%v", applied, err)
	}
	if stop, err := loop.shouldStop(t.Context()); err != nil || !stop || queries != 1 {
		t.Fatalf("finished durable work did not drain watch: stop=%t err=%v ready_queries=%d", stop, err, queries)
	}
	pending, applied, err := dispatchStore.BeginTerminalReconciliationWithCompletionEventIfCurrent(t.Context(), dispatched, assignment.StatusCompleted, "")
	if err != nil || !applied {
		t.Fatalf("publish completion from separate maintainer: applied=%t err=%v", applied, err)
	}
	if stop, err := loop.shouldStop(t.Context()); err != nil || stop || queries != 1 {
		t.Fatalf("watch exited before consuming durable completion: stop=%t err=%v ready_queries=%d", stop, err, queries)
	}
	const consumer = "watch-stop-test-consumer"
	if _, claimed, err := dispatchStore.ClaimPendingCompletionEvent(t.Context(), pending.BeadID, pending.PendingCompletionEventID, consumer, time.Minute); err != nil || !claimed {
		t.Fatalf("claim completion: claimed=%t err=%v", claimed, err)
	}
	if acked, err := dispatchStore.AcknowledgeCompletionEvent(t.Context(), pending.BeadID, pending.PendingCompletionEventID, consumer); err != nil || !acked {
		t.Fatalf("acknowledge completion: acknowledged=%t err=%v", acked, err)
	}
	if stop, err := loop.shouldStop(t.Context()); err != nil || !stop || queries != 2 {
		t.Fatalf("acknowledged completion did not drain watch: stop=%t err=%v ready_queries=%d", stop, err, queries)
	}
	ledgerPath := filepath.Join(assignment.StorageDir(), session, "assignments.json")
	if err := os.WriteFile(ledgerPath, []byte("{invalid ledger"), 0600); err != nil {
		t.Fatal(err)
	}
	if stop, err := loop.shouldStop(t.Context()); stop || err == nil || !strings.Contains(err.Error(), "read assignment ledger before stopping watch") || queries != 2 {
		t.Fatalf("unreadable ledger was treated as drained: stop=%t err=%v ready_queries=%d", stop, err, queries)
	}
}

func TestWatchLoopConsumesCompletionWhenMaintenanceWinsDetectorRace(t *testing.T) {
	f := newCoordinatorMaintenanceCLIFixture(t, false)
	previousInterval, previousAutoReassign := assignWatchInterval, assignAutoReassign
	assignWatchInterval, assignAutoReassign = 20*time.Millisecond, false
	t.Cleanup(func() {
		assignWatchInterval, assignAutoReassign = previousInterval, previousAutoReassign
	})
	loop := NewWatchLoop(f.session, f.store, &AutoReassignOptions{
		Session: f.session, ProjectDir: f.project, Quiet: true, ReserveFiles: true,
	})
	loop.stopWhenDone = false
	loop.scanInterval = time.Hour
	var eventID string
	loop.newMaintainer = func(ctx context.Context, session, project string) (assignWatchMaintainer, error) {
		maintainer, err := newAssignWatchMaintainer(ctx, session, project)
		if err != nil {
			return nil, err
		}
		// Complete the real maintenance pass before Run creates its detector,
		// deterministically selecting the maintenance-first race ordering.
		if err := maintainer.MaintainAssignments(ctx); err != nil {
			return nil, err
		}
		latest, err := assignment.LoadStoreStrictReadOnly(session)
		if err != nil {
			return nil, err
		}
		terminal := latest.Get("ntm-finished")
		if terminal == nil || terminal.Status != assignment.StatusCompleted || terminal.PendingCompletionEventID == "" {
			return nil, fmt.Errorf("maintenance retired work without a completion event: %+v", terminal)
		}
		eventID = terminal.PendingCompletionEventID
		return maintainer, nil
	}
	var handled, acknowledged, acknowledgementAttempts atomic.Int32
	loop.handleCompletionFn = func(ctx context.Context, event completion.CompletionEvent) error {
		if event.BeadID != "ntm-finished" || event.EventID != eventID || event.IsFailed {
			return fmt.Errorf("unexpected completion after maintenance: %+v", event)
		}
		handled.Add(1)
		return loop.handleCompletion(ctx, event)
	}
	acked := make(chan struct{}, 1)
	loop.ackCompletionEventFn = func(ctx context.Context, bead, event, consumer string) (bool, error) {
		acknowledgementAttempts.Add(1)
		ok, err := loop.store.AcknowledgeCompletionEvent(ctx, bead, event, consumer)
		if ok && err == nil {
			acknowledged.Add(1)
			select {
			case acked <- struct{}{}:
			default:
			}
		}
		return ok, err
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runResult := make(chan error, 1)
	go func() { runResult <- loop.Run(ctx) }()
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			select {
			case <-runResult:
			case <-time.After(2 * time.Second):
				t.Error("watch did not join during cleanup")
			}
		}
	})
	select {
	case <-acked:
	case err := <-runResult:
		joined = true
		t.Fatalf("watch exited before consuming maintenance completion: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance completion never reached the watch consumer")
	}
	// Let several detector polls re-read the acknowledged ledger. They must
	// not replay the consumer effects or create another acknowledgement.
	select {
	case <-time.After(4 * assignWatchInterval):
	case err := <-runResult:
		joined = true
		t.Fatalf("watch stopped unexpectedly after acknowledgement: %v", err)
	}
	cancel()
	select {
	case err := <-runResult:
		joined = true
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not join after consuming completion")
	}
	if handled.Load() != 1 || acknowledged.Load() != 1 || acknowledgementAttempts.Load() != 1 || !strings.Contains(loop.Summary(), "1 completed") {
		t.Fatalf("completion handling=%d acknowledgements=%d attempts=%d summary=%s", handled.Load(), acknowledged.Load(), acknowledgementAttempts.Load(), loop.Summary())
	}
	latest, err := assignment.LoadStoreStrictReadOnly(f.session)
	if err != nil {
		t.Fatal(err)
	}
	terminal := latest.Get("ntm-finished")
	if terminal == nil || terminal.Status != assignment.StatusCompleted || terminal.PendingCompletionEventID != "" || terminal.CompletionConsumerToken != "" || terminal.CompletionLeaseExpiresAt != nil {
		t.Fatalf("completion was not durably acknowledged: %+v", terminal)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.releaseIDs) != 1 || !reflect.DeepEqual(f.releaseIDs[0], []int{961}) {
		t.Fatalf("detector repeated maintenance cleanup: %+v", f.releaseIDs)
	}
}

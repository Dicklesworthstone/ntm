package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestDashboardReservationStartupRecoversWithCappedBackoff(t *testing.T) {
	var waits []time.Duration
	var probes []context.Context
	startup := dashboardReservationStartup{
		probe: func(ctx context.Context) error {
			probes = append(probes, ctx)
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 2*time.Second || time.Until(deadline) <= 0 {
				t.Fatalf("probe has no bounded live deadline: %v %v", deadline, ok)
			}
			if len(probes) < 9 {
				return errors.New("Agent Mail is starting")
			}
			return nil
		},
		wait: func(ctx context.Context, delay time.Duration) bool {
			if ctx.Err() != nil {
				t.Fatal("backoff entered after cancellation")
			}
			waits = append(waits, delay)
			if probes[len(probes)-1].Err() == nil {
				t.Fatal("probe context leaked into backoff")
			}
			return true
		},
	}
	if !startup.waitUntilReady(context.Background()) {
		t.Fatal("transient startup outage permanently disabled reservations")
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second}
	if !reflect.DeepEqual(waits, want) || len(probes) != 9 {
		t.Fatalf("retry schedule=%v calls=%d", waits, len(probes))
	}
	if probes[len(probes)-1].Err() == nil {
		t.Fatal("successful probe context not released")
	}
}

func TestDashboardReservationStartupReadyDoesNotRetry(t *testing.T) {
	calls := 0
	startup := dashboardReservationStartup{
		probe: func(context.Context) error { calls++; return nil },
		wait: func(context.Context, time.Duration) bool {
			t.Fatal("healthy service entered backoff")
			return false
		},
	}
	if !startup.waitUntilReady(context.Background()) || calls != 1 {
		t.Fatalf("ready=%d probes", calls)
	}
}

func TestDashboardReservationStartupCancellation(t *testing.T) {
	for _, mode := range []string{"before probe", "during probe", "late success", "during backoff"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{})
			var calls atomic.Int32
			startup := dashboardReservationStartup{
				probe: func(probeCtx context.Context) error {
					calls.Add(1)
					if mode == "during probe" || mode == "late success" {
						close(entered)
						<-probeCtx.Done()
						if mode == "late success" {
							return nil
						}
						return probeCtx.Err()
					}
					return errors.New("unavailable")
				},
			}
			if mode == "before probe" {
				cancel()
			} else if mode == "during backoff" {
				startup.wait = func(ctx context.Context, _ time.Duration) bool {
					close(entered)
					return waitDashboardReservationRetry(ctx, time.Hour)
				}
			}
			done := make(chan bool, 1)
			go func() { done <- startup.waitUntilReady(ctx) }()
			if mode != "before probe" {
				select {
				case <-entered:
				case <-time.After(time.Second):
					cancel()
					t.Fatal("startup did not enter cancellation boundary")
				}
				cancel()
			}
			select {
			case ready := <-done:
				if ready {
					t.Fatal("cancelled startup authorized reservation work")
				}
			case <-time.After(time.Second):
				t.Fatal("cancel did not join startup promptly")
			}
			wantCalls := int32(1)
			if mode == "before probe" {
				wantCalls = 0
			}
			if calls.Load() != wantCalls {
				t.Fatalf("probes=%d, want %d", calls.Load(), wantCalls)
			}
		})
	}
}

func TestDashboardReservationStartupRejectsLateProbeSuccess(t *testing.T) {
	// The 2s probe budget expires independently of the still-live dashboard.
	// A non-cooperative probe returning success after it expires cannot start
	// the watcher. Follow it with a real successful retry.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	calls, waits := 0, 0
	startup := dashboardReservationStartup{
		probe: func(probeCtx context.Context) error {
			calls++
			if calls == 1 {
				<-probeCtx.Done()
			}
			return nil
		},
		wait: func(context.Context, time.Duration) bool { waits++; return true },
	}
	if !startup.waitUntilReady(ctx) || calls != 2 || waits != 1 {
		t.Fatalf("late probe admitted: probes=%d waits=%d parent=%v", calls, waits, ctx.Err())
	}
}

func TestDashboardReservationRetryTimer(t *testing.T) {
	if !waitDashboardReservationRetry(context.Background(), time.Millisecond) {
		t.Fatal("live timer failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 100; i++ {
		if waitDashboardReservationRetry(ctx, 0) {
			t.Fatal("timer race ignored cancellation")
		}
	}
}

func TestDashboardReservationStartupMissingInputs(t *testing.T) {
	if (dashboardReservationStartup{}).waitUntilReady(context.Background()) {
		t.Fatal("missing probe treated as available")
	}
	if (dashboardReservationStartup{probe: func(context.Context) error { return nil }}).waitUntilReady(nil) {
		t.Fatal("missing lifecycle context accepted")
	}
}

func TestDashboardReservationStartupRecoversRealHTTP(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/health" {
			t.Errorf("startup sent an unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if requests.Add(1) < 3 {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	startup := dashboardReservationStartup{
		probe: func(ctx context.Context) error {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/health", nil)
			if err != nil {
				return err
			}
			response, err := server.Client().Do(req)
			if err != nil {
				return err
			}
			defer response.Body.Close()
			_, _ = io.Copy(io.Discard, response.Body)
			if response.StatusCode != http.StatusOK {
				return errors.New("not ready")
			}
			return nil
		},
		wait: func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil },
	}
	if !startup.waitUntilReady(context.Background()) || requests.Load() != 3 {
		t.Fatalf("startup did not recover: requests=%d", requests.Load())
	}
}

func TestDashboardReservationLifecycleStartsOnceAndJoinsCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var probes, starts, stops atomic.Int32
	started := make(chan struct{})
	cleanupEntered := make(chan struct{})
	finishCleanup := make(chan struct{})
	done := make(chan struct{})
	startup := dashboardReservationStartup{
		probe: func(context.Context) error {
			if probes.Add(1) < 3 {
				return errors.New("starting")
			}
			return nil
		},
		wait: func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil },
	}
	go func() {
		defer close(done)
		startup.run(ctx, func(watcherCtx context.Context) func() {
			if watcherCtx != ctx || watcherCtx.Err() != nil {
				t.Error("watcher started with wrong or cancelled lifecycle context")
			}
			starts.Add(1)
			close(started)
			return func() {
				stops.Add(1)
				if watcherCtx.Err() == nil {
					t.Error("cleanup called before cancellation")
				}
				close(cleanupEntered)
				<-finishCleanup
			}
		})
	}()
	defer func() {
		cancel()
		close(finishCleanup)
		<-done
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("watcher never started after health recovered")
	}
	if probes.Load() != 3 || starts.Load() != 1 || stops.Load() != 0 {
		t.Fatalf("before cancellation: probes=%d starts=%d stops=%d", probes.Load(), starts.Load(), stops.Load())
	}
	cancel()
	select {
	case <-cleanupEntered:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach watcher cleanup")
	}
	select {
	case <-done:
		t.Fatal("dashboard returned before watcher cleanup completed")
	default:
	}
	if probes.Load() != 3 || starts.Load() != 1 || stops.Load() != 1 {
		t.Fatalf("after cancellation: probes=%d starts=%d stops=%d", probes.Load(), starts.Load(), stops.Load())
	}
}

func TestDashboardReservationLifecycleDoesNotStartAfterCancellation(t *testing.T) {
	for _, duringProbe := range []bool{false, true} {
		t.Run(map[bool]string{false: "before probe", true: "late probe success"}[duringProbe], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			probes, starts := 0, 0
			startup := dashboardReservationStartup{
				probe: func(context.Context) error { probes++; cancel(); return nil },
			}
			if !duringProbe {
				cancel()
			}
			startup.run(ctx, func(context.Context) func() {
				starts++
				return func() {}
			})
			if starts != 0 || (duringProbe && probes != 1) || (!duringProbe && probes != 0) {
				t.Fatalf("cancelled lifecycle: probes=%d starts=%d", probes, starts)
			}
		})
	}
}

func TestDashboardReservationLifecycleNoWatcher(t *testing.T) {
	probes := 0
	startup := dashboardReservationStartup{probe: func(context.Context) error { probes++; return nil }}
	startup.run(context.Background(), nil)
	if probes != 0 {
		t.Fatal("missing watcher factory probed the service")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		startup.run(context.Background(), func(context.Context) func() { return nil })
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("nil watcher waited forever for cancellation")
	}
	if probes != 1 {
		t.Fatalf("probes=%d, want 1", probes)
	}
}

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/robot/adapters"
	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

func observationWork(count, visible int) *adapters.WorkSection {
	return &adapters.WorkSection{
		Available: true,
		Summary:   &adapters.WorkSummary{Ready: count},
		Ready:     make([]adapters.WorkItem, visible),
		Verification: &adapters.WorkVerification{
			Source:     &worksource.Identity{ProjectDir: "/project", JSONLSHA256: "source-digest"},
			CountScope: "verified_candidates", VerifiedReady: &count,
			FromCache: true, CacheCollectedAt: "2026-09-01T00:00:00Z", CacheExpiresAt: "2026-09-01T00:00:45Z",
			PreviewTruncated: count > visible,
		},
	}
}

func observationOptions(minimum int) workObservationOptions {
	return workObservationOptions{Interval: minimumWorkObservationInterval, SampleTimeout: time.Second, MinimumReady: minimum}
}

func TestWorkReadyWaitUsesVerifiedCountNotPreview(t *testing.T) {
	work := observationWork(3, 1)
	original := *work.Verification
	var got []workObservation
	err := observeWorkSnapshots(context.Background(), observationOptions(3),
		func(context.Context) (*adapters.WorkSection, error) { return work, nil },
		func(o workObservation) error { got = append(got, o); return nil })
	if err != nil || len(got) != 1 || got[0].Work != work || !got[0].Info.Matched || !got[0].Info.Terminal || got[0].Info.Samples != 1 {
		t.Fatalf("complete count did not satisfy wait with a truncated preview: %+v, %v", got, err)
	}
	if !reflect.DeepEqual(original, *work.Verification) {
		t.Fatal("observation restamped or mutated original cache evidence")
	}
}

func TestWorkReadyWaitRejectsUnverifiedAndInconsistentCounts(t *testing.T) {
	for _, mode := range []string{"missing verification", "missing source", "unbound source", "tool count", "preview count", "missing count", "wrong count", "negative count", "oversized preview", "unavailable", "nil"} {
		t.Run(mode, func(t *testing.T) {
			work := observationWork(3, 1)
			switch mode {
			case "missing verification":
				work.Verification = nil
			case "missing source":
				work.Verification.Source = nil
			case "unbound source":
				work.Verification.Source.JSONLSHA256 = ""
			case "tool count":
				work.Verification.CountScope = "tool_reported_unverified"
			case "preview count":
				work.Verification.CountScope = "verified_preview"
			case "missing count":
				work.Verification.VerifiedReady = nil
			case "wrong count":
				work.Summary.Ready = 100
			case "negative count":
				n := -1
				work.Verification.VerifiedReady = &n
				work.Summary.Ready = n
			case "oversized preview":
				work.Ready = make([]adapters.WorkItem, 4)
			case "unavailable":
				work.Available = false
			case "nil":
				work = nil
			}
			var calls, outputs int
			err := observeWorkSnapshots(context.Background(), observationOptions(1),
				func(context.Context) (*adapters.WorkSection, error) { calls++; return work, nil },
				func(o workObservation) error {
					outputs++
					if o.Work != nil || o.Err == nil || o.Info.Matched || !o.Info.Terminal {
						t.Errorf("unsafe ready result: %+v", o)
					}
					return nil
				})
			if err == nil || calls != 1 || outputs != 1 {
				t.Fatalf("invalid evidence was retried or accepted: calls=%d outputs=%d err=%v", calls, outputs, err)
			}
		})
	}
}

func TestWorkReadyWaitObservesChangedEligibility(t *testing.T) {
	var reads, outputs int
	start := time.Now()
	err := observeWorkSnapshots(context.Background(), observationOptions(2),
		func(context.Context) (*adapters.WorkSection, error) {
			reads++
			if reads == 1 {
				return observationWork(0, 0), nil
			}
			return observationWork(2, 1), nil
		}, func(o workObservation) error {
			outputs++
			if o.Err != nil || !o.Info.Matched || o.Info.Samples != 2 || o.Work.Summary.Ready != 2 {
				t.Errorf("bad terminal observation: %+v", o)
			}
			return nil
		})
	if err != nil || reads != 2 || outputs != 1 || time.Since(start) < minimumWorkObservationInterval {
		t.Fatalf("wait did not recollect or paced incorrectly: reads=%d outputs=%d err=%v", reads, outputs, err)
	}
}

func TestWorkReadyWaitCanRequireObservedReservations(t *testing.T) {
	var reads int
	opts := observationOptions(2)
	opts.RequireReservations = true
	err := observeWorkSnapshots(context.Background(), opts,
		func(context.Context) (*adapters.WorkSection, error) {
			reads++
			work := observationWork(2, 1)
			work.Verification.Reservations = &adapters.WorkReservationVerification{State: "unavailable"}
			if reads == 2 {
				work.Verification.Reservations = &adapters.WorkReservationVerification{State: "observed", ProjectID: 7, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
			}
			return work, nil
		}, func(o workObservation) error {
			if o.Err != nil || !o.Info.Matched || o.Info.Samples != 2 {
				t.Errorf("unobserved reservations satisfied wait: %+v", o)
			}
			return nil
		})
	if err != nil || reads != 2 {
		t.Fatalf("strict wait: reads=%d err=%v", reads, err)
	}
	for _, r := range []*adapters.WorkReservationVerification{nil, {State: "not_checked"}, {State: "observed", ProjectID: 7}, {State: "observed", ProjectID: 0, ObservedAt: time.Now().Format(time.RFC3339Nano)}} {
		work := observationWork(2, 1)
		work.Verification.Reservations = r
		if observedWorkReservations(work) {
			t.Fatalf("accepted incomplete reservation evidence: %+v", r)
		}
	}
}

func TestWorkWatchEmitsUnavailableThenRecoversWithoutReplay(t *testing.T) {
	failure := errors.New("read failed")
	stop := errors.New("consumer stopped")
	var reads int
	var got []workObservation
	err := observeWorkSnapshots(context.Background(), observationOptions(0),
		func(context.Context) (*adapters.WorkSection, error) {
			reads++
			if reads == 2 {
				return observationWork(99, 1), failure
			}
			return observationWork(reads, 1), nil
		}, func(o workObservation) error {
			got = append(got, o)
			if len(got) == 3 {
				return stop
			}
			return nil
		})
	if !errors.Is(err, stop) || reads != 3 || len(got) != 3 {
		t.Fatalf("watch: reads=%d output=%+v err=%v", reads, got, err)
	}
	for i, o := range got {
		if o.Info.Sequence != uint64(i+1) || o.Info.Samples != uint64(i+1) || o.Info.CheckedAt.IsZero() {
			t.Errorf("bad sample identity: %+v", o.Info)
		}
	}
	if got[0].Work.Summary.Ready != 1 || got[1].Work != nil || !errors.Is(got[1].Err, failure) || got[2].Work.Summary.Ready != 3 {
		t.Fatalf("failed read replayed work: %+v", got)
	}
}

func TestWorkObservationCancellationIsTerminalAndNonmutating(t *testing.T) {
	for _, mode := range []string{"before read", "during read", "between reads"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "before read" {
				cancel()
			}
			var reads int
			var got []workObservation
			err := observeWorkSnapshots(ctx, observationOptions(0),
				func(readCtx context.Context) (*adapters.WorkSection, error) {
					reads++
					if mode == "during read" {
						cancel()
						<-readCtx.Done()
					}
					return observationWork(100, 1), nil // late success must not escape cancellation
				}, func(o workObservation) error {
					got = append(got, o)
					if mode == "between reads" {
						cancel()
					}
					return nil
				})
			if !errors.Is(err, context.Canceled) || len(got) == 0 {
				t.Fatalf("cancellation lost: %+v %v", got, err)
			}
			last := got[len(got)-1]
			if !last.Info.Terminal || last.Work != nil || !errors.Is(last.Err, context.Canceled) {
				t.Fatalf("cancelled sample carried stale work: %+v", last)
			}
			want := 1
			if mode == "before read" {
				want = 0
			}
			if reads != want {
				t.Fatalf("read after cancellation: got %d want %d", reads, want)
			}
		})
	}
}

func TestWorkObservationHonorsReadAndWaitDeadlines(t *testing.T) {
	opts := observationOptions(1)
	opts.SampleTimeout = 5 * time.Millisecond
	var calls int
	err := observeWorkSnapshots(context.Background(), opts,
		func(ctx context.Context) (*adapters.WorkSection, error) {
			calls++
			<-ctx.Done()
			return observationWork(100, 1), nil
		},
		func(o workObservation) error {
			if o.Work != nil || !errors.Is(o.Err, context.DeadlineExceeded) {
				t.Errorf("read timeout became success: %+v", o)
			}
			return nil
		})
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("read timeout: calls=%d err=%v", calls, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	err = observeWorkSnapshots(ctx, observationOptions(1),
		func(context.Context) (*adapters.WorkSection, error) { return observationWork(0, 0), nil },
		func(o workObservation) error {
			if o.Work != nil || !o.Info.Terminal || !errors.Is(o.Err, context.DeadlineExceeded) {
				t.Errorf("wait timeout carried old work: %+v", o)
			}
			return nil
		})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait timeout lost: %v", err)
	}
}

func TestWorkObservationOutputBackpressureStopsCollection(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	stop := errors.New("broken output")
	var reads atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- observeWorkSnapshots(context.Background(), observationOptions(0),
			func(context.Context) (*adapters.WorkSection, error) { reads.Add(1); return observationWork(1, 1), nil },
			func(workObservation) error { close(entered); <-release; return stop })
	}()
	<-entered
	if reads.Load() != 1 {
		t.Fatal("unexpected read count before backpressure")
	}
	// The writer owns the current sample. No additional source work is queued.
	time.Sleep(2 * minimumWorkObservationInterval)
	if reads.Load() != 1 {
		t.Errorf("collector ran ahead of its consumer: %d", reads.Load())
	}
	close(release)
	if err := <-done; !errors.Is(err, stop) || reads.Load() != 1 {
		t.Fatalf("writer failure did not stop collection: %v", err)
	}
}

func TestWorkReadyWaitDoesNotRepairFailedEvidence(t *testing.T) {
	failure := &worksource.StaleError{Reason: "source changed"}
	var calls int
	err := observeWorkSnapshots(context.Background(), observationOptions(1),
		func(context.Context) (*adapters.WorkSection, error) { calls++; return observationWork(999, 1), failure },
		func(o workObservation) error {
			if o.Work != nil || !o.Info.Terminal || !errors.Is(o.Err, worksource.ErrStale) {
				t.Errorf("stale evidence escaped: %+v", o)
			}
			return nil
		})
	if !errors.Is(err, worksource.ErrStale) || calls != 1 {
		t.Fatalf("failed evidence was retried: calls=%d err=%v", calls, err)
	}
}

func TestWorkObservationPinsProjectAcrossAliasesAndReplacement(t *testing.T) {
	root := t.TempDir()
	original, other := filepath.Join(root, "original"), filepath.Join(root, "other")
	if err := os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(original, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	pinned, check, err := pinWorkObservationProject(alias)
	expected, expectedErr := filepath.EvalSymlinks(original)
	if err != nil || expectedErr != nil || pinned != expected {
		t.Fatalf("pin project: %s %v", pinned, err)
	}
	if err := os.Rename(alias, alias+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, alias); err != nil {
		t.Fatal(err)
	}
	if err := check(); err != nil {
		t.Fatalf("input alias redirected the pinned observer: %v", err)
	}
	if err := os.Rename(original, original+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	if err := check(); !errors.Is(err, errWorkObservationProjectChanged) {
		t.Fatalf("replaced project accepted: %v", err)
	}
	var outputs int
	err = observeWorkSnapshots(context.Background(), observationOptions(0),
		func(context.Context) (*adapters.WorkSection, error) { return nil, check() },
		func(o workObservation) error {
			outputs++
			if !o.Info.Terminal {
				t.Error("project replacement was not terminal")
			}
			return nil
		})
	if !errors.Is(err, errWorkObservationProjectChanged) || outputs != 1 {
		t.Fatalf("observer followed replacement: outputs=%d err=%v", outputs, err)
	}
}

func TestWorkObservationRejectsInvalidOptionsBeforeCollection(t *testing.T) {
	for _, opts := range []workObservationOptions{
		{}, {Interval: time.Nanosecond, SampleTimeout: time.Second},
		{Interval: time.Hour, SampleTimeout: time.Second},
		{Interval: time.Second, SampleTimeout: -time.Second},
		{Interval: time.Second, SampleTimeout: 2 * time.Minute},
		{Interval: time.Second, SampleTimeout: time.Second, MinimumReady: -1},
		{Interval: time.Second, SampleTimeout: time.Second, MinimumReady: 100001},
		{Interval: time.Second, SampleTimeout: time.Second, RequireReservations: true},
	} {
		err := observeWorkSnapshots(context.Background(), opts,
			func(context.Context) (*adapters.WorkSection, error) {
				t.Error("invalid options collected work")
				return nil, nil
			},
			func(workObservation) error { t.Error("invalid options emitted a sample"); return nil })
		if err == nil {
			t.Fatalf("invalid options accepted: %+v", opts)
		}
	}
}

package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRestoreProgressOrdersEvidenceAndUsesExecutionContext(t *testing.T) {
	type journalKey struct{}
	var order []string
	var events []RestoreProgress
	parent := WithRestoreProgress(context.Background(), func(ctx context.Context, event RestoreProgress) error {
		if ctx.Value(journalKey{}) != "late reporter" {
			t.Fatal("observer captured admission context instead of execution context")
		}
		order = append(order, event.Phase)
		events = append(events, event)
		return nil
	})
	parent = context.WithValue(parent, journalKey{}, "late reporter")
	ctx, finish := beginRestoreProgress(parent, RestoreProgress{
		CheckpointID: "saved", SourceSession: "source", Session: "destination", WorkingDir: "/project", PlannedPanes: 2,
	}, false)
	defer finish()
	id, err := runRestoreMutation(ctx, RestoreProgress{Stage: "create_pane", Session: "wrong", Sequence: 99}, func() (string, error) {
		order = append(order, "action")
		return "%21\n", nil
	})
	if err != nil || id != "%21" || !reflect.DeepEqual(order, []string{"before", "action", "after"}) {
		t.Fatalf("mutation ordering/result = %v, %q, %v", order, id, err)
	}
	for i, event := range events {
		if event.Sequence != uint64(i+1) || event.Session != "destination" || event.SourceSession != "source" || event.CheckpointID != "saved" || event.WorkingDir != "/project" || event.PlannedPanes != 2 {
			t.Fatalf("identity or sequence drift: %+v", event)
		}
		if _, err := time.Parse(time.RFC3339Nano, event.ObservedAt); err != nil {
			t.Fatal(err)
		}
	}
	if events[0].Outcome != "uncertain" || events[0].PaneID != "" || events[1].Outcome != "succeeded" || events[1].PaneID != "%21" {
		t.Fatalf("action evidence = %+v", events)
	}
	finish()
	if parent.Err() != nil {
		t.Fatal("finishing restore cancelled its caller")
	}
}

func TestRestoreProgressFailureStopsFurtherActions(t *testing.T) {
	for _, failAt := range []string{"before", "after"} {
		t.Run(failAt, func(t *testing.T) {
			cause := errors.New("journal unavailable")
			calls, actions := 0, 0
			parent := WithRestoreProgress(context.Background(), func(_ context.Context, event RestoreProgress) error {
				calls++
				if event.Phase == failAt {
					return cause
				}
				return nil
			})
			ctx, finish := beginRestoreProgress(parent, RestoreProgress{}, false)
			defer finish()
			run := func() (string, error) { actions++; return "%4", nil }
			id, err := runRestoreMutation(ctx, RestoreProgress{Stage: "create_pane"}, run)
			if !errors.Is(err, cause) || !errors.Is(err, ErrRestoreProgress) || !errors.Is(context.Cause(ctx), cause) {
				t.Fatalf("lost checkpoint failure: id=%q err=%v cause=%v", id, err, context.Cause(ctx))
			}
			wantActions, wantCalls := 0, 1
			if failAt == "after" {
				wantActions, wantCalls = 1, 2
				if id != "%4" {
					t.Fatal("failed checkpoint lost already-created pane ID")
				}
			}
			if _, err := runRestoreMutation(ctx, RestoreProgress{Stage: "launch_agent"}, run); !errors.Is(err, cause) {
				t.Fatalf("sticky checkpoint failure disappeared: %v", err)
			}
			if actions != wantActions || calls != wantCalls {
				t.Fatalf("continued after checkpoint failure: actions=%d calls=%d", actions, calls)
			}
		})
	}
}

func TestRestoreProgressCancellationDuringBeforeWritePreventsAction(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	parent = WithRestoreProgress(parent, func(_ context.Context, event RestoreProgress) error {
		cancel()
		return nil
	})
	ctx, finish := beginRestoreProgress(parent, RestoreProgress{}, false)
	defer finish()
	_, err := runRestoreMutation(ctx, RestoreProgress{Stage: "stop_session"}, func() (string, error) {
		t.Fatal("cancelled before-write was followed by a destructive action")
		return "", nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}

func TestRestoreProgressRecordsOutcomeAfterCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []RestoreProgress
	parent = WithRestoreProgress(parent, func(_ context.Context, event RestoreProgress) error { events = append(events, event); return nil })
	ctx, finish := beginRestoreProgress(parent, RestoreProgress{}, false)
	defer finish()
	id, err := runRestoreMutation(ctx, RestoreProgress{Stage: "create_pane"}, func() (string, error) {
		cancel()
		return "%9", nil
	})
	if err != nil || id != "%9" || len(events) != 2 || events[1].Outcome != "succeeded" || events[1].PaneID != "%9" {
		t.Fatalf("cancellation discarded completed effect: %q, %v, %+v", id, err, events)
	}
	_, err = runRestoreMutation(ctx, RestoreProgress{}, func() (string, error) { t.Fatal("continued after cancellation"); return "", nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRestoreProgressActionFailureIsUncertainAndPreservesBothCauses(t *testing.T) {
	actionErr, journalErr := errors.New("tmux response lost"), errors.New("disk full")
	parent := WithRestoreProgress(context.Background(), func(_ context.Context, event RestoreProgress) error {
		if event.Phase == "after" {
			if event.Outcome != "uncertain" || event.PaneID != "%8" {
				t.Fatalf("failed action fabricated success: %+v", event)
			}
			return journalErr
		}
		return nil
	})
	ctx, finish := beginRestoreProgress(parent, RestoreProgress{}, false)
	defer finish()
	id, err := runRestoreMutation(ctx, RestoreProgress{Stage: "create_pane"}, func() (string, error) { return "%8", actionErr })
	if id != "%8" || !errors.Is(err, actionErr) || !errors.Is(err, journalErr) || !errors.Is(err, ErrRestoreProgress) {
		t.Fatalf("lost partial identity or causes: %q %v", id, err)
	}
}

func TestRestoreProgressObserverPanicFailsClosedWithoutPayload(t *testing.T) {
	parent := WithRestoreProgress(context.Background(), func(context.Context, RestoreProgress) error { panic("credential-in-callback") })
	ctx, finish := beginRestoreProgress(parent, RestoreProgress{}, false)
	defer finish()
	_, err := runRestoreMutation(ctx, RestoreProgress{Stage: "stop_session"}, func() (string, error) { t.Fatal("observer panic allowed actuation"); return "", nil })
	if !errors.Is(err, ErrRestoreProgress) || strings.Contains(err.Error(), "credential-in-callback") {
		t.Fatalf("panic not contained safely: %v", err)
	}
}

func TestRestoreProgressDryRunAndIndependentLifetimes(t *testing.T) {
	var sequences []uint64
	parent := WithRestoreProgress(context.Background(), func(_ context.Context, event RestoreProgress) error {
		sequences = append(sequences, event.Sequence)
		return nil
	})
	for _, dryRun := range []bool{false, true, false} {
		ctx, finish := beginRestoreProgress(parent, RestoreProgress{}, dryRun)
		if err := reportRestoreProgress(ctx, RestoreProgress{Stage: "test"}); err != nil {
			t.Fatal(err)
		}
		finish()
		if !dryRun {
			if err := reportRestoreProgress(ctx, RestoreProgress{Stage: "late"}); !errors.Is(err, ErrRestoreProgress) {
				t.Fatalf("late reporter wrote after lifetime ended: %v", err)
			}
		}
	}
	if !reflect.DeepEqual(sequences, []uint64{1, 1}) || parent.Err() != nil {
		t.Fatalf("restore lifetimes leaked: %v, %v", sequences, parent.Err())
	}
	base := context.Background()
	if WithRestoreProgress(base, nil) != base {
		t.Fatal("nil observer changed context")
	}
	if err := reportRestoreProgress(base, RestoreProgress{}); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreProgressSerializesConcurrentObservers(t *testing.T) {
	var seen []uint64
	parent := WithRestoreProgress(context.Background(), func(_ context.Context, event RestoreProgress) error { seen = append(seen, event.Sequence); return nil })
	ctx, finish := beginRestoreProgress(parent, RestoreProgress{}, false)
	defer finish()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := reportRestoreProgress(ctx, RestoreProgress{Stage: "pane_identified", PaneID: fmt.Sprintf("%%%d", i)}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if len(seen) != 100 {
		t.Fatalf("lost events: %d", len(seen))
	}
	for i, sequence := range seen {
		if sequence != uint64(i+1) {
			t.Fatalf("callback ordering changed: %v", seen)
		}
	}
}

func TestRestoreProgressCloseWaitsForInflightObserver(t *testing.T) {
	entered, release, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	parent := WithRestoreProgress(context.Background(), func(context.Context, RestoreProgress) error { close(entered); <-release; return nil })
	ctx, finish := beginRestoreProgress(parent, RestoreProgress{}, false)
	reported := make(chan error, 1)
	go func() { reported <- reportRestoreProgress(ctx, RestoreProgress{Stage: "create_session"}) }()
	<-entered
	go func() { finish(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("lifetime ended during a checkpoint write")
	case <-time.After(10 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	if err := <-reported; err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("observer lifetime did not close")
	}
	if err := reportRestoreProgress(ctx, RestoreProgress{}); !errors.Is(err, ErrRestoreProgress) {
		t.Fatal("late callback was accepted")
	}
}

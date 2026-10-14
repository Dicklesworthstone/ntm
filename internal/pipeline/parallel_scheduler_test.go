package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func schedulerGraph(t *testing.T, tasks []parallelTask, external map[string]parallelTaskOutcome) *parallelTaskGraph {
	t.Helper()
	g, err := newParallelTaskGraph(tasks, external)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func schedulerDeadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestParallelSchedulerReverseChainAndSettledOutputs(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("requires a POSIX shell")
	}
	g := schedulerGraph(t, []parallelTask{
		{id: "group_consumer", aliases: []string{"consumer"}, dependsOn: []string{"producer", "group_producer", "producer"}},
		{id: "group_producer", aliases: []string{"producer"}, dependsOn: []string{"seed"}},
		{id: "group_seed", aliases: []string{"seed"}},
	}, nil)
	if !reflect.DeepEqual(g.dependencies[0], []int{1}) {
		t.Fatalf("duplicate aliases produced duplicate edges: %v", g.dependencies)
	}
	dir := t.TempDir()
	var order []int
	out := make([]string, 3)
	settled := make([]string, 3)
	commands := []string{
		`test "$INPUT" = seed-produced && printf '%s' "$INPUT-consumed"`,
		`test "$INPUT" = seed && printf '%s' "$INPUT-produced"`,
		`printf seed`,
	}
	err := g.run(schedulerDeadline(t), 1, false, nil, func(int) error { return nil },
		func(ctx context.Context, i int) parallelTaskOutcome {
			cmd := exec.CommandContext(ctx, "sh", "-c", commands[i])
			input := ""
			if i < 2 {
				input = settled[i+1]
			}
			cmd.Env = append(os.Environ(), "INPUT="+input)
			data, err := cmd.Output()
			if err != nil {
				t.Errorf("real consumer %d ran before its producer: %v", i, err)
				return parallelTaskFailed
			}
			out[i] = string(data)
			return parallelTaskSucceeded
		},
		func(i int, state parallelTaskOutcome, _ []string) error {
			if state != parallelTaskSucceeded {
				return fmt.Errorf("unexpected outcome %d", state)
			}
			settled[i] = out[i]
			order = append(order, i)
			return os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.receipt", i)), []byte(out[i]), 0600)
		})
	if err != nil || !reflect.DeepEqual(order, []int{2, 1, 0}) || settled[0] != "seed-produced-consumed" {
		t.Fatalf("err=%v order=%v outputs=%v", err, order, settled)
	}
}

func TestParallelSchedulerReadyChildDoesNotWaitForUnrelatedWork(t *testing.T) {
	g := schedulerGraph(t, []parallelTask{
		{id: "slow"}, {id: "consumer", dependsOn: []string{"producer"}}, {id: "producer"},
	}, nil)
	ctx := schedulerDeadline(t)
	slowStarted := make(chan struct{})
	consumerStarted := make(chan struct{})
	releaseSlow := make(chan struct{})
	producerSettled := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(releaseSlow) }) }
	defer release()
	done := make(chan error, 1)
	go func() {
		done <- g.run(ctx, 2, false, nil, func(int) error { return nil },
			func(ctx context.Context, i int) parallelTaskOutcome {
				switch i {
				case 0:
					close(slowStarted)
					select {
					case <-ctx.Done():
						return parallelTaskCancelled
					case <-releaseSlow:
					}
				case 1:
					select {
					case <-producerSettled:
					default:
						t.Error("consumer ran before settlement")
					}
					close(consumerStarted)
				case 2:
					select {
					case <-slowStarted:
					case <-ctx.Done():
						return parallelTaskCancelled
					}
				}
				return parallelTaskSucceeded
			},
			func(i int, state parallelTaskOutcome, _ []string) error {
				if i == 2 && state == parallelTaskSucceeded {
					close(producerSettled)
				}
				return nil
			})
	}()
	select {
	case <-consumerStarted:
	case <-ctx.Done():
		t.Error("a ready dependent waited for the unrelated slow sibling")
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestParallelSchedulerFailedDependenciesPropagateWithoutDispatch(t *testing.T) {
	g := schedulerGraph(t, []parallelTask{
		{id: "grandchild", dependsOn: []string{"child"}},
		{id: "child", dependsOn: []string{"producer"}},
		{id: "producer"}, {id: "independent"},
	}, nil)
	var called [4]atomic.Int32
	states := make([]parallelTaskOutcome, 4)
	blocks := make([][]string, 4)
	err := g.run(schedulerDeadline(t), 2, false, nil, func(int) error { return nil },
		func(_ context.Context, i int) parallelTaskOutcome {
			called[i].Add(1)
			if i == 2 {
				return parallelTaskFailed
			}
			return parallelTaskSucceeded
		},
		func(i int, state parallelTaskOutcome, blocked []string) error {
			states[i], blocks[i] = state, blocked
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if called[0].Load() != 0 || called[1].Load() != 0 || called[3].Load() != 1 {
		t.Fatalf("failed dependencies dispatched or independent work suppressed: %d %d %d", called[0].Load(), called[1].Load(), called[3].Load())
	}
	if states[0] != parallelTaskBlocked || states[1] != parallelTaskBlocked ||
		!reflect.DeepEqual(blocks[0], []string{"child"}) || !reflect.DeepEqual(blocks[1], []string{"producer"}) {
		t.Fatalf("states=%v blockers=%v", states, blocks)
	}
}

func TestParallelSchedulerFailFastCancelsAndJoinsActiveWork(t *testing.T) {
	g := schedulerGraph(t, []parallelTask{{id: "fails"}, {id: "active"}, {id: "queued"}}, nil)
	ctx := schedulerDeadline(t)
	activeStarted := make(chan struct{})
	var activeExited atomic.Bool
	var queued atomic.Bool
	err := g.run(ctx, 2, true, nil, func(int) error { return nil },
		func(ctx context.Context, i int) parallelTaskOutcome {
			switch i {
			case 0:
				select {
				case <-activeStarted:
				case <-ctx.Done():
					return parallelTaskCancelled
				}
				return parallelTaskFailed
			case 1:
				close(activeStarted)
				<-ctx.Done()
				activeExited.Store(true)
				return parallelTaskCancelled
			default:
				queued.Store(true)
				return parallelTaskSucceeded
			}
		}, func(int, parallelTaskOutcome, []string) error { return nil })
	if err != nil || !activeExited.Load() || queued.Load() {
		t.Fatalf("err=%v joined=%v queued=%v", err, activeExited.Load(), queued.Load())
	}
}

func TestParallelSchedulerCancellationBeforeAdmission(t *testing.T) {
	g := schedulerGraph(t, []parallelTask{{id: "producer"}, {id: "consumer", dependsOn: []string{"producer"}}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var records int
	err := g.run(ctx, 1, false, nil,
		func(int) error { t.Error("started after cancellation"); return nil },
		func(context.Context, int) parallelTaskOutcome {
			t.Error("ran after cancellation")
			return parallelTaskSucceeded
		},
		func(_ int, state parallelTaskOutcome, _ []string) error {
			if state != parallelTaskCancelled {
				t.Errorf("outcome=%v", state)
			}
			records++
			return nil
		})
	if err != nil || records != 2 {
		t.Fatalf("err=%v records=%d", err, records)
	}
}

func TestParallelSchedulerCheckpointFailureStopsDependentEffects(t *testing.T) {
	for _, atStart := range []bool{true, false} {
		t.Run(fmt.Sprintf("start=%t", atStart), func(t *testing.T) {
			g := schedulerGraph(t, []parallelTask{{id: "producer"}, {id: "consumer", dependsOn: []string{"producer"}}}, nil)
			failure := errors.New("checkpoint disk unavailable")
			var calls atomic.Int32
			err := g.run(schedulerDeadline(t), 1, false, nil,
				func(i int) error {
					if atStart && i == 0 {
						return failure
					}
					return nil
				},
				func(_ context.Context, i int) parallelTaskOutcome {
					calls.Add(1)
					if i == 1 {
						t.Error("consumer side effect crossed a failed checkpoint")
					}
					return parallelTaskSucceeded
				},
				func(i int, state parallelTaskOutcome, _ []string) error {
					if !atStart && i == 0 && state == parallelTaskSucceeded {
						return failure
					}
					return nil
				})
			want := int32(1)
			if atStart {
				want = 0
			}
			if !errors.Is(err, failure) || calls.Load() != want {
				t.Fatalf("err=%v calls=%d want=%d", err, calls.Load(), want)
			}
		})
	}
}

func TestParallelSchedulerRetainsProducerAndRejectsStaleConsumer(t *testing.T) {
	g := schedulerGraph(t, []parallelTask{{id: "consumer", dependsOn: []string{"producer"}}, {id: "producer"}}, nil)
	var calls [2]atomic.Int32
	var producerSettled bool
	err := g.run(schedulerDeadline(t), 1, false, map[int]bool{1: true}, func(int) error { return nil },
		func(_ context.Context, i int) parallelTaskOutcome {
			calls[i].Add(1)
			if !producerSettled {
				t.Error("retained output was not restored")
			}
			return parallelTaskSucceeded
		}, func(i int, _ parallelTaskOutcome, _ []string) error {
			if i == 1 {
				producerSettled = true
			}
			return nil
		})
	if err != nil || calls[0].Load() != 1 || calls[1].Load() != 0 {
		t.Fatalf("err=%v replay counts=%d/%d", err, calls[0].Load(), calls[1].Load())
	}
	var writes int
	err = g.run(schedulerDeadline(t), 1, false, map[int]bool{0: true},
		func(int) error { writes++; return nil },
		func(context.Context, int) parallelTaskOutcome { writes++; return parallelTaskSucceeded },
		func(int, parallelTaskOutcome, []string) error { writes++; return nil })
	if err == nil || !strings.Contains(err.Error(), "stale recovery") || writes != 0 {
		t.Fatalf("stale recovery err=%v writes=%d", err, writes)
	}
}

func TestParallelSchedulerRejectsInvalidGraphsBeforeWork(t *testing.T) {
	cases := []struct {
		name     string
		tasks    []parallelTask
		external map[string]parallelTaskOutcome
	}{
		{"empty id", []parallelTask{{}}, nil},
		{"duplicate id", []parallelTask{{id: "a"}, {id: "a"}}, nil},
		{"alias collision", []parallelTask{{id: "g_a", aliases: []string{"a"}}, {id: "g_b", aliases: []string{"g_a"}}}, nil},
		{"external collision", []parallelTask{{id: "g_a", aliases: []string{"a"}}}, map[string]parallelTaskOutcome{"a": parallelTaskSucceeded}},
		{"self cycle", []parallelTask{{id: "g_a", aliases: []string{"a"}, dependsOn: []string{"a"}}}, nil},
		{"cycle", []parallelTask{{id: "a", dependsOn: []string{"b"}}, {id: "b", dependsOn: []string{"a"}}}, nil},
		{"unknown", []parallelTask{{id: "a", dependsOn: []string{"missing"}}}, nil},
		{"unfinished external", []parallelTask{{id: "a", dependsOn: []string{"external"}}}, map[string]parallelTaskOutcome{"external": parallelTaskPending}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newParallelTaskGraph(test.tasks, test.external); err == nil {
				t.Fatal("invalid graph accepted")
			}
		})
	}
}

func TestParallelSchedulerExternalPrerequisites(t *testing.T) {
	g := schedulerGraph(t, []parallelTask{
		{id: "blocked", dependsOn: []string{"outside"}},
		{id: "after", dependsOn: []string{"blocked"}},
		{id: "independent", dependsOn: []string{"ready"}},
	}, map[string]parallelTaskOutcome{"outside": parallelTaskFailed, "ready": parallelTaskSucceeded})
	var calls atomic.Int32
	var blocked int
	err := g.run(schedulerDeadline(t), 2, false, nil, func(int) error { return nil },
		func(_ context.Context, i int) parallelTaskOutcome {
			if i != 2 {
				t.Error("external failure bypassed")
			}
			calls.Add(1)
			return parallelTaskSucceeded
		},
		func(_ int, state parallelTaskOutcome, _ []string) error {
			if state == parallelTaskBlocked {
				blocked++
			}
			return nil
		})
	if err != nil || calls.Load() != 1 || blocked != 2 {
		t.Fatalf("err=%v calls=%d blocked=%d", err, calls.Load(), blocked)
	}
}

func TestParallelSchedulerBoundsConcurrentReadyWork(t *testing.T) {
	tasks := make([]parallelTask, 150)
	for i := range tasks {
		tasks[i] = parallelTask{id: fmt.Sprint(i)}
	}
	g := schedulerGraph(t, tasks, nil)
	var active, peak, total atomic.Int32
	ctx := schedulerDeadline(t)
	barrier := make(chan struct{})
	var once sync.Once
	err := g.run(ctx, 3, false, nil, func(int) error { return nil },
		func(ctx context.Context, _ int) parallelTaskOutcome {
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old; old = peak.Load() {
				if peak.CompareAndSwap(old, n) {
					break
				}
			}
			if n == 3 {
				once.Do(func() { close(barrier) })
			}
			select {
			case <-barrier:
			case <-ctx.Done():
				return parallelTaskCancelled
			}
			total.Add(1)
			return parallelTaskSucceeded
		}, func(int, parallelTaskOutcome, []string) error { return nil })
	if err != nil || peak.Load() != 3 || active.Load() != 0 || total.Load() != 150 {
		t.Fatalf("err=%v peak=%d active=%d total=%d", err, peak.Load(), active.Load(), total.Load())
	}
}

func TestParallelSchedulerPreservesDependencyCancellation(t *testing.T) {
	g := schedulerGraph(t, []parallelTask{
		{id: "grandchild", dependsOn: []string{"child"}},
		{id: "child", dependsOn: []string{"producer"}},
		{id: "producer"}, {id: "independent"},
		{id: "external", dependsOn: []string{"outside"}},
	}, map[string]parallelTaskOutcome{"outside": parallelTaskCancelled})
	states := make([]parallelTaskOutcome, 5)
	err := g.run(schedulerDeadline(t), 2, false, nil, func(int) error { return nil },
		func(_ context.Context, i int) parallelTaskOutcome {
			if i == 2 {
				return parallelTaskCancelled
			}
			if i != 3 {
				t.Errorf("cancelled dependency ran: %d", i)
			}
			return parallelTaskSucceeded
		}, func(i int, state parallelTaskOutcome, _ []string) error { states[i] = state; return nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{0, 1, 2, 4} {
		if states[i] != parallelTaskCancelled {
			t.Errorf("state[%d]=%v; cancellation became failure/success", i, states[i])
		}
	}
	if states[3] != parallelTaskSucceeded {
		t.Fatal("independent work unexpectedly suppressed")
	}
}

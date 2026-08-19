package pipeline

// Tests for bd-jio7h: simultaneously-ready independent top-level steps must
// execute concurrently (bounded by settings.limits.max_parallel_steps) while
// preserving on_error semantics, resume correctness, output_var capture, and
// per-pane dispatch serialization.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TestParallelReadySteps_DiamondOverlap proves the two middle legs of a
// diamond graph A -> (B, C) -> D actually overlap in time. B and C each
// rendezvous on the other's start marker; under serial scheduling one of them
// would time out waiting and the workflow would fail.
func TestParallelReadySteps_DiamondOverlap(t *testing.T) {
	tmpDir := t.TempDir()

	rendezvous := func(self, other string) string {
		return "touch " + self + ".start; i=0; while [ $i -lt 100 ]; do " +
			"[ -f " + other + ".start ] && exit 0; sleep 0.05; i=$((i+1)); done; exit 1"
	}

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "diamond-overlap",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "a", Command: "echo start", OutputVar: "a_out"},
			{ID: "b", Command: rendezvous("b", "c"), DependsOn: []string{"a"}},
			{ID: "c", Command: rendezvous("c", "b"), DependsOn: []string{"a"}},
			{ID: "d", Command: "echo '${steps.a.output}' done", DependsOn: []string{"b", "c"}, OutputVar: "d_out"},
		},
	}

	cfg := DefaultExecutorConfig("diamond-session")
	cfg.ProjectDir = tmpDir
	cfg.DefaultTimeout = 10 * time.Second
	executor := NewExecutor(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, err := executor.Run(ctx, workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v (serial scheduling would make the rendezvous time out)", err)
	}
	if state.Status != StatusCompleted {
		t.Fatalf("workflow status = %q, want %q", state.Status, StatusCompleted)
	}

	b, c, d := state.Steps["b"], state.Steps["c"], state.Steps["d"]
	if b.Status != StatusCompleted || c.Status != StatusCompleted || d.Status != StatusCompleted {
		t.Fatalf("statuses b=%q c=%q d=%q, want all completed", b.Status, c.Status, d.Status)
	}

	// Interval overlap: B started before C finished AND C started before B
	// finished.
	if !b.StartedAt.Before(c.FinishedAt) || !c.StartedAt.Before(b.FinishedAt) {
		t.Fatalf("b [%s..%s] and c [%s..%s] did not overlap",
			b.StartedAt.Format(time.RFC3339Nano), b.FinishedAt.Format(time.RFC3339Nano),
			c.StartedAt.Format(time.RFC3339Nano), c.FinishedAt.Format(time.RFC3339Nano))
	}

	// D only ran after both dependencies finished.
	if d.StartedAt.Before(b.FinishedAt) || d.StartedAt.Before(c.FinishedAt) {
		t.Fatalf("d started at %s before deps finished (b=%s c=%s)",
			d.StartedAt.Format(time.RFC3339Nano), b.FinishedAt.Format(time.RFC3339Nano), c.FinishedAt.Format(time.RFC3339Nano))
	}

	// Deterministic variable interpolation: D consumed A's output var.
	if got, _ := state.Variables["d_out"].(string); !strings.Contains(got, "start") {
		t.Fatalf("d_out = %q, want it to contain a's output %q", got, "start")
	}
}

// TestParallelReadySteps_BoundedConcurrency asserts max_parallel_steps caps
// the number of simultaneously-running top-level steps.
func TestParallelReadySteps_BoundedConcurrency(t *testing.T) {
	tmpDir := t.TempDir()
	gauge := filepath.Join(tmpDir, "gauge")

	// Each step increments a "currently running" gauge implemented with
	// files, records the high-water mark, sleeps, then decrements. mkdir is
	// atomic on POSIX so it serves as the critical-section lock.
	cmd := `
lock() { while ! mkdir ` + gauge + `.lock 2>/dev/null; do sleep 0.01; done; }
unlock() { rmdir ` + gauge + `.lock; }
lock
n=$(cat ` + gauge + ` 2>/dev/null || echo 0)
n=$((n+1))
printf '%s' "$n" > ` + gauge + `
hi=$(cat ` + gauge + `.hi 2>/dev/null || echo 0)
[ "$n" -gt "$hi" ] && printf '%s' "$n" > ` + gauge + `.hi
unlock
sleep 0.3
lock
n=$(cat ` + gauge + `)
printf '%s' "$((n-1))" > ` + gauge + `
unlock
`

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "bounded-concurrency",
		Settings: WorkflowSettings{
			OnError: ErrorActionFail,
			Limits:  LimitsConfig{MaxParallelSteps: 2},
		},
		Steps: []Step{
			{ID: "s1", Command: cmd},
			{ID: "s2", Command: cmd},
			{ID: "s3", Command: cmd},
			{ID: "s4", Command: cmd},
		},
	}

	cfg := DefaultExecutorConfig("bounded-session")
	cfg.ProjectDir = tmpDir
	cfg.DefaultTimeout = 10 * time.Second
	executor := NewExecutor(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, err := executor.Run(ctx, workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if state.Status != StatusCompleted {
		t.Fatalf("workflow status = %q, want %q", state.Status, StatusCompleted)
	}

	raw, err := os.ReadFile(gauge + ".hi")
	if err != nil {
		t.Fatalf("reading high-water mark: %v", err)
	}
	hi, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parsing high-water mark %q: %v", raw, err)
	}
	if hi > 2 {
		t.Fatalf("high-water concurrency = %d, want <= max_parallel_steps (2)", hi)
	}
	if hi < 2 {
		t.Fatalf("high-water concurrency = %d, want 2 (steps did not overlap at all)", hi)
	}
}

// TestParallelReadySteps_FailFastCancelsInFlight asserts a fail_fast failure
// cancels concurrently-running sibling steps instead of waiting them out.
func TestParallelReadySteps_FailFastCancelsInFlight(t *testing.T) {
	tmpDir := t.TempDir()

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "fail-fast-cancel",
		Settings: WorkflowSettings{
			OnError: ErrorActionFailFast,
		},
		Steps: []Step{
			{ID: "fails", Command: "sleep 0.1; exit 3"},
			{ID: "slow", Command: "sleep 30"},
		},
	}

	cfg := DefaultExecutorConfig("failfast-session")
	cfg.ProjectDir = tmpDir
	cfg.DefaultTimeout = 60 * time.Second
	executor := NewExecutor(cfg)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	state, err := executor.Run(ctx, workflow, nil, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Run() error = nil, want failure from fail_fast step")
	}
	if state.Status != StatusFailed {
		t.Fatalf("workflow status = %q, want %q", state.Status, StatusFailed)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("Run() took %s; the 30s sibling was not cancelled in flight", elapsed)
	}
	if got := state.Steps["fails"].Status; got != StatusFailed {
		t.Fatalf("fails status = %q, want %q", got, StatusFailed)
	}
	if got := state.Steps["slow"].Status; got != StatusCancelled {
		t.Fatalf("slow status = %q, want %q (cancelled in flight)", got, StatusCancelled)
	}
}

// TestParallelReadySteps_ContinueDoesNotCancel asserts on_error=continue lets
// concurrently-running siblings finish after a failure.
func TestParallelReadySteps_ContinueDoesNotCancel(t *testing.T) {
	tmpDir := t.TempDir()
	marker := filepath.Join(tmpDir, "survivor.txt")

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "continue-no-cancel",
		Settings: WorkflowSettings{
			OnError: ErrorActionContinue,
		},
		Steps: []Step{
			{ID: "fails", Command: "exit 3"},
			{ID: "survivor", Command: "sleep 0.3; touch " + marker},
		},
	}

	cfg := DefaultExecutorConfig("continue-session")
	cfg.ProjectDir = tmpDir
	cfg.DefaultTimeout = 10 * time.Second
	executor := NewExecutor(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, err := executor.Run(ctx, workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil under on_error=continue", err)
	}
	if got := state.Steps["fails"].Status; got != StatusFailed {
		t.Fatalf("fails status = %q, want %q", got, StatusFailed)
	}
	if got := state.Steps["survivor"].Status; got != StatusCompleted {
		t.Fatalf("survivor status = %q, want %q (must not be cancelled)", got, StatusCompleted)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("survivor marker missing: %v", statErr)
	}
}

// TestParallelReadySteps_ResumeMidParallelGroup: first run fails one leg of
// the diamond after the sibling leg completed; resume must re-run only the
// failed leg (plus its dependents), not the completed sibling.
func TestParallelReadySteps_ResumeMidParallelGroup(t *testing.T) {
	tmpDir := t.TempDir()
	bRuns := filepath.Join(tmpDir, "b-runs.txt")
	allowC := filepath.Join(tmpDir, "allow-c.txt")

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "resume-diamond",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "a", Command: "echo seed"},
			{ID: "b", Command: "printf 'run\\n' >> " + bRuns, DependsOn: []string{"a"}},
			{ID: "c", Command: "sleep 0.3; test -f " + allowC, DependsOn: []string{"a"}},
			{ID: "d", Command: "echo done", DependsOn: []string{"b", "c"}},
		},
	}

	cfg := DefaultExecutorConfig("resume-diamond-session")
	cfg.ProjectDir = tmpDir
	cfg.DefaultTimeout = 10 * time.Second

	first := NewExecutor(cfg)
	prior, err := first.Run(context.Background(), workflow, nil, nil)
	if err == nil {
		t.Fatal("first Run() error = nil, want step c to fail")
	}
	if prior.Steps["b"].Status != StatusCompleted {
		t.Fatalf("first run b status = %q, want completed before c fails", prior.Steps["b"].Status)
	}
	if prior.Steps["c"].Status != StatusFailed {
		t.Fatalf("first run c status = %q, want failed", prior.Steps["c"].Status)
	}

	// Unblock c and resume.
	if err := os.WriteFile(allowC, []byte("ok"), 0o644); err != nil {
		t.Fatalf("writing allow-c marker: %v", err)
	}
	second := NewExecutor(cfg)
	state, err := second.Resume(context.Background(), workflow, prior, nil)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if state.Status != StatusCompleted {
		t.Fatalf("resumed workflow status = %q, want %q", state.Status, StatusCompleted)
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		if got := state.Steps[id].Status; got != StatusCompleted {
			t.Fatalf("resumed step %s status = %q, want completed", id, got)
		}
	}

	// b must have executed exactly once across both runs.
	content, err := os.ReadFile(bRuns)
	if err != nil {
		t.Fatalf("reading b run log: %v", err)
	}
	if got := strings.Count(string(content), "run\n"); got != 1 {
		t.Fatalf("b executed %d times across run+resume, want 1", got)
	}
}

// timestampedTmuxClient wraps a TmuxClient and records the wall-clock time of
// every PasteKeys call so tests can assert dispatch serialization.
type timestampedTmuxClient struct {
	inner TmuxClient
	mu    sync.Mutex
	calls []struct {
		target string
		at     time.Time
	}
}

func (c *timestampedTmuxClient) GetPanes(session string) ([]tmux.Pane, error) {
	return c.inner.GetPanes(session)
}

func (c *timestampedTmuxClient) PasteKeys(target, content string, enter bool) error {
	c.mu.Lock()
	c.calls = append(c.calls, struct {
		target string
		at     time.Time
	}{target, time.Now()})
	c.mu.Unlock()
	return c.inner.PasteKeys(target, content, enter)
}

func (c *timestampedTmuxClient) CapturePaneOutput(target string, lines int) (string, error) {
	return c.inner.CapturePaneOutput(target, lines)
}

func (c *timestampedTmuxClient) pasteTimes(target string) []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []time.Time
	for _, call := range c.calls {
		if call.target == target {
			out = append(out, call.at)
		}
	}
	return out
}

// TestParallelReadySteps_SamePaneSerialized asserts two independent steps
// that resolve to the SAME tmux pane do not interleave their dispatch
// windows even though the scheduler runs them concurrently.
func TestParallelReadySteps_SamePaneSerialized(t *testing.T) {
	tmpDir := t.TempDir()

	mock := NewMockTmuxClient(tmux.Pane{ID: "%1", Index: 1, Type: tmux.AgentClaude})
	t.Cleanup(mock.Reset)
	stamped := &timestampedTmuxClient{inner: mock}

	const hold = 400 * time.Millisecond
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "same-pane-serialized",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{
				ID:      "p1",
				Prompt:  "first prompt",
				Pane:    PaneSpec{Index: 1},
				Wait:    WaitTime,
				Timeout: Duration{Duration: hold},
			},
			{
				ID:      "p2",
				Prompt:  "second prompt",
				Pane:    PaneSpec{Index: 1},
				Wait:    WaitTime,
				Timeout: Duration{Duration: hold},
			},
		},
	}

	cfg := DefaultExecutorConfig("same-pane-session")
	cfg.ProjectDir = tmpDir
	cfg.DefaultTimeout = 10 * time.Second
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(stamped)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, err := executor.Run(ctx, workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if state.Status != StatusCompleted {
		t.Fatalf("workflow status = %q, want %q", state.Status, StatusCompleted)
	}

	times := stamped.pasteTimes("%1")
	if len(times) != 2 {
		t.Fatalf("recorded %d pastes to %%1, want 2", len(times))
	}
	gap := times[1].Sub(times[0])
	if gap < 0 {
		gap = -gap
	}
	// The pane lock is held across the WaitTime window, so the second paste
	// must trail the first by roughly the hold duration. Allow slack for
	// scheduler jitter but reject anything close to simultaneous dispatch.
	if gap < hold/2 {
		t.Fatalf("pastes to the same pane were %s apart, want >= %s (dispatch windows interleaved)", gap, hold/2)
	}
}

// TestParallelReadySteps_SerialWhenLimitOne pins the legacy behavior:
// max_parallel_steps=1 must not overlap independent steps.
func TestParallelReadySteps_SerialWhenLimitOne(t *testing.T) {
	tmpDir := t.TempDir()

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "serial-limit-one",
		Settings: WorkflowSettings{
			OnError: ErrorActionFail,
			Limits:  LimitsConfig{MaxParallelSteps: 1},
		},
		Steps: []Step{
			{ID: "s1", Command: "sleep 0.15"},
			{ID: "s2", Command: "sleep 0.15"},
		},
	}

	cfg := DefaultExecutorConfig("serial-session")
	cfg.ProjectDir = tmpDir
	cfg.DefaultTimeout = 10 * time.Second
	executor := NewExecutor(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, err := executor.Run(ctx, workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	s1, s2 := state.Steps["s1"], state.Steps["s2"]
	overlap := s1.StartedAt.Before(s2.FinishedAt) && s2.StartedAt.Before(s1.FinishedAt)
	if overlap {
		t.Fatalf("s1 [%s..%s] overlapped s2 [%s..%s] despite max_parallel_steps=1",
			s1.StartedAt.Format(time.RFC3339Nano), s1.FinishedAt.Format(time.RFC3339Nano),
			s2.StartedAt.Format(time.RFC3339Nano), s2.FinishedAt.Format(time.RFC3339Nano))
	}
}

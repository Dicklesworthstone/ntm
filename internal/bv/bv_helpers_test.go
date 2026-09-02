package bv

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type doneObservedContext struct {
	context.Context
	doneObserved chan<- struct{}
}

func (c doneObservedContext) Done() <-chan struct{} {
	select {
	case c.doneObserved <- struct{}{}:
	default:
	}
	return c.Context.Done()
}

type retryWaitObservedContext struct {
	context.Context
	retryWaitEntered chan<- time.Duration
	continueRetry    <-chan struct{}
}

func (c retryWaitObservedContext) observeBeadsRetryWaitForTest(delay time.Duration) {
	select {
	case c.retryWaitEntered <- delay:
	case <-c.Context.Done():
		return
	}
	select {
	case <-c.continueRetry:
	case <-c.Context.Done():
	}
}

func waitForTestFile(t *testing.T, path, description string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("inspect %s: %v", description, err)
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", description)
		}
	}
}

func TestBeadsMutatorsRejectInactiveContextWithoutInvokingSubprocesses(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	binDir := t.TempDir()
	invoked := filepath.Join(t.TempDir(), "subprocess-invoked")
	script := "#!/bin/sh\nprintf 'invoked\\n' > " + invoked + "\nexit 1\n"
	for _, name := range []string{"br", "git"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o700); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}
	t.Setenv("PATH", binDir)

	tests := []struct {
		name string
		run  func(context.Context) error
	}{
		{
			name: "generic claim",
			run: func(ctx context.Context) error {
				_, err := ClaimBead(ctx, dir, "ntm-test", "TestActor")
				return err
			},
		},
		{
			name: "assignment claim",
			run: func(ctx context.Context) error {
				_, err := ClaimBeadForAssignment(ctx, dir, "ntm-test", "TestActor")
				return err
			},
		},
		{
			name: "assignment claim with policy",
			run: func(ctx context.Context) error {
				_, err := ClaimBeadForAssignmentWithOperatorGatedLabels(ctx, dir, "ntm-test", "TestActor", []string{"human-gated"})
				return err
			},
		},
		{
			name: "stale assignment claim",
			run: func(ctx context.Context) error {
				_, err := ClaimStaleBeadForAssignment(ctx, dir, "ntm-test", "TestActor", time.Now().UTC())
				return err
			},
		},
		{
			name: "stale assignment claim with policy",
			run: func(ctx context.Context) error {
				_, err := ClaimStaleBeadForAssignmentWithOperatorGatedLabels(ctx, dir, "ntm-test", "TestActor", time.Now().UTC(), []string{"human-gated"})
				return err
			},
		},
		{
			name: "claim release",
			run: func(ctx context.Context) error {
				_, err := ReleaseBeadClaim(ctx, dir, "ntm-test", "TestActor")
				return err
			},
		},
	}

	contexts := []struct {
		name    string
		context func() context.Context
		want    error
	}{
		{name: "nil", context: func() context.Context { return nil }},
		{name: "pre-canceled", context: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, want: context.Canceled},
	}
	for _, test := range tests {
		for _, contextCase := range contexts {
			t.Run(test.name+"/"+contextCase.name, func(t *testing.T) {
				err := test.run(contextCase.context())
				if contextCase.want != nil {
					if !errors.Is(err, contextCase.want) {
						t.Fatalf("error=%v, want errors.Is(%v)", err, contextCase.want)
					}
				} else if err == nil || !strings.Contains(err.Error(), "beads mutation context is required") {
					t.Fatalf("error=%v, want required-context failure", err)
				}
				if _, statErr := os.Stat(invoked); !os.IsNotExist(statErr) {
					t.Fatalf("subprocess executed for inactive context: stat error=%v", statErr)
				}
			})
		}
	}
}

func TestRunBdContextCancelsWhileQueuedForWorkspaceGate(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	normalized, err := normalizeTriageDir(dir)
	if err != nil {
		t.Fatalf("normalize triage dir: %v", err)
	}
	gate := workspaceBDMutex(normalized)
	gate.Lock()
	defer gate.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneObserved := make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() {
		_, runErr := RunBdContext(doneObservedContext{
			Context:      ctx,
			doneObserved: doneObserved,
		}, dir, "ready", "--json")
		result <- runErr
	}()

	select {
	case <-doneObserved:
	case err := <-result:
		t.Fatalf("RunBdContext returned before waiting for the workspace gate: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("RunBdContext did not reach the workspace gate")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunBdContext error=%v, want context canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunBdContext did not stop after cancellation while queued for the workspace gate")
	}
}

func TestGetBeadsSummaryContextCancelsWhileStatsWaitsForWorkspaceGate(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".git", ".beads"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	binDir := t.TempDir()
	invoked := filepath.Join(t.TempDir(), "br-invoked")
	script := "#!/bin/sh\nprintf 'invoked\\n' > " + invoked + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	normalized, err := normalizeTriageDir(dir)
	if err != nil {
		t.Fatalf("normalize triage dir: %v", err)
	}
	gate := workspaceBDMutex(normalized)
	gate.Lock()
	defer gate.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneObserved := make(chan struct{}, 1)
	type summaryResult struct {
		summary *BeadsSummary
		err     error
	}
	result := make(chan summaryResult, 1)
	go func() {
		summary, summaryErr := GetBeadsSummaryContext(doneObservedContext{
			Context:      ctx,
			doneObserved: doneObserved,
		}, dir, 10)
		result <- summaryResult{summary: summary, err: summaryErr}
	}()

	select {
	case <-doneObserved:
	case got := <-result:
		t.Fatalf("GetBeadsSummaryContext returned before stats waited for the workspace gate: result=%+v error=%v", got.summary, got.err)
	case <-time.After(5 * time.Second):
		t.Fatal("GetBeadsSummaryContext stats did not reach the workspace gate")
	}
	cancel()

	select {
	case got := <-result:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("GetBeadsSummaryContext result=%+v error=%v, want context canceled", got.summary, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GetBeadsSummaryContext did not stop after cancellation while stats waited for the workspace gate")
	}
	if _, err := os.Stat(invoked); !os.IsNotExist(err) {
		t.Fatalf("gated br command executed before cancellation: stat error=%v", err)
	}
}

func TestRunBdContextCancelsDuringTransientRetryBackoff(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "attempts")
	script := "#!/bin/sh\nprintf 'x\\n' >> " + marker + "\nprintf 'database is locked\\n' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", binDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	retryWaitEntered := make(chan time.Duration)
	continueRetry := make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() {
		_, err := RunBdContext(retryWaitObservedContext{
			Context:          ctx,
			retryWaitEntered: retryWaitEntered,
			continueRetry:    continueRetry,
		}, dir, "ready", "--json")
		result <- err
	}()
	for attempt := 1; attempt <= 3; attempt++ {
		select {
		case delay := <-retryWaitEntered:
			if low, high := transientBeadsDBBackoffBounds(attempt); delay < low || delay > high {
				t.Fatalf("retry backoff %d delay=%s, want within [%s, %s]", attempt, delay, low, high)
			}
			if attempt < 3 {
				continueRetry <- struct{}{}
			}
		case err := <-result:
			t.Fatalf("RunBdContext returned before entering retry backoff %d: %v", attempt, err)
		case <-time.After(5 * time.Second):
			t.Fatalf("RunBdContext did not enter retry backoff %d", attempt)
		}
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunBdContext error=%v, want context canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunBdContext ignored cancellation during retry backoff")
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read fake br attempts: %v", err)
	}
	if attempts := strings.Count(string(data), "x"); attempts != 3 {
		t.Fatalf("fake br attempts=%d, want exactly 3 with no post-cancellation execution", attempts)
	}
}

// installFakeBRWithAttempts installs a fake br on PATH that appends one line
// to the returned attempts file per invocation and runs script after that.
// The script may reference $attempts (the file path) and $count (invocations
// so far, including the current one).
func installFakeBRWithAttempts(t *testing.T, script string) (dir, attempts string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	binDir := t.TempDir()
	attempts = filepath.Join(t.TempDir(), "attempts")
	full := "#!/bin/sh\nattempts=" + attempts + "\nprintf 'x\\n' >> \"$attempts\"\ncount=$(wc -l < \"$attempts\" | tr -d ' ')\n" + script
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(full), 0o700); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	// Keep the host PATH: the script shells out to wc/tr.
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir, attempts
}

func countFakeBRAttempts(t *testing.T, attempts string) int {
	t.Helper()
	data, err := os.ReadFile(attempts)
	if err != nil {
		t.Fatalf("read fake br attempts: %v", err)
	}
	return strings.Count(string(data), "x")
}

// brWriteLockTimeoutDiagnostic mirrors the exact CONFIG_ERROR br prints when
// its cross-process workspace write lock times out.
const brWriteLockTimeoutDiagnostic = "CONFIG_ERROR: Timed out after 5000ms waiting for write lock (database) at .beads/.write.lock. Another br process may be holding that authority; retry after it exits or investigate a stuck process."

func TestIsTransientBeadsDBError(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   bool
	}{
		{name: "sqlite locked", stderr: "Error: database is locked", want: true},
		{name: "sqlite busy", stderr: "database is busy", want: true},
		{name: "br write lock timeout", stderr: brWriteLockTimeoutDiagnostic, want: true},
		{name: "unwritable write lock is terminal", stderr: "CONFIG_ERROR: Failed to open write lock .beads/.write.lock: permission denied", want: false},
		{name: "other config error is terminal", stderr: "CONFIG_ERROR: invalid [sync] configuration", want: false},
		{name: "generic timeout is terminal", stderr: "Timed out after 30s waiting for remote", want: false},
		{name: "empty", stderr: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientBeadsDBError(tc.stderr, ""); got != tc.want {
				t.Fatalf("isTransientBeadsDBError(%q) = %v, want %v", tc.stderr, got, tc.want)
			}
			if got := isTransientBeadsDBError("", tc.stderr); got != tc.want {
				t.Fatalf("isTransientBeadsDBError(stdout=%q) = %v, want %v", tc.stderr, got, tc.want)
			}
		})
	}
}

func TestTransientBeadsDBBackoffIsBoundedExponentialWithJitter(t *testing.T) {
	wantCeilings := []time.Duration{
		50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond,
		400 * time.Millisecond, 800 * time.Millisecond, 800 * time.Millisecond,
		800 * time.Millisecond,
	}
	for i, ceiling := range wantCeilings {
		attempt := i + 1
		low, high := transientBeadsDBBackoffBounds(attempt)
		if high != ceiling || low != ceiling/2 {
			t.Fatalf("attempt %d bounds = [%s, %s], want [%s, %s]", attempt, low, high, ceiling/2, ceiling)
		}
		for sample := 0; sample < 64; sample++ {
			if got := transientBeadsDBBackoff(attempt); got < low || got > high {
				t.Fatalf("attempt %d backoff %s outside [%s, %s]", attempt, got, low, high)
			}
		}
	}
	if low, high := transientBeadsDBBackoffBounds(0); low != 25*time.Millisecond || high != 50*time.Millisecond {
		t.Fatalf("attempt 0 bounds = [%s, %s], want clamped to attempt 1", low, high)
	}
}

func TestTransientBeadsRetryDelayHonorsAttemptAndDeadlineBudgets(t *testing.T) {
	farDeadline := time.Now().Add(time.Hour)
	if _, retry := transientBeadsRetryDelay(6, 6, farDeadline); retry {
		t.Fatal("final attempt must not be retried")
	}
	if delay, retry := transientBeadsRetryDelay(1, 6, farDeadline); !retry || delay <= 0 {
		t.Fatalf("attempt 1 retry = (%s, %v), want positive delay", delay, retry)
	}
	if _, retry := transientBeadsRetryDelay(1, 6, time.Now()); retry {
		t.Fatal("an expired deadline must stop retries even with attempts remaining")
	}
}

func TestRunBdContextRetriesBeadsWriteLockTimeoutThenSucceeds(t *testing.T) {
	dir, attempts := installFakeBRWithAttempts(t, `
if [ "$count" -lt 3 ]; then
  printf '%s\n' "`+brWriteLockTimeoutDiagnostic+`" >&2
  exit 1
fi
printf '%s\n' '{"issues":[],"total":0}'
`)

	output, err := RunBdContext(context.Background(), dir, "ready", "--json")
	if err != nil {
		t.Fatalf("RunBdContext returned the transient write-lock timeout: %v", err)
	}
	if !strings.Contains(output, `"issues":[]`) {
		t.Fatalf("RunBdContext output=%q, want the successful third attempt", output)
	}
	if got := countFakeBRAttempts(t, attempts); got != 3 {
		t.Fatalf("fake br attempts=%d, want exactly 3", got)
	}
}

func TestRunBdContextDoesNotRetryTerminalConfigError(t *testing.T) {
	dir, attempts := installFakeBRWithAttempts(t, `
printf '%s\n' 'CONFIG_ERROR: Failed to open write lock .beads/.write.lock: permission denied' >&2
exit 1
`)

	_, err := RunBdContext(context.Background(), dir, "ready", "--json")
	if err == nil || !strings.Contains(err.Error(), "Failed to open write lock") {
		t.Fatalf("RunBdContext error=%v, want the terminal CONFIG_ERROR surfaced", err)
	}
	if got := countFakeBRAttempts(t, attempts); got != 1 {
		t.Fatalf("fake br attempts=%d, want exactly 1 (no retry on a terminal error)", got)
	}
}

func TestRunBdContextStopsRetryingContentionAtTotalDeadline(t *testing.T) {
	previous := transientBeadsRetryBudget
	transientBeadsRetryBudget = 120 * time.Millisecond
	t.Cleanup(func() { transientBeadsRetryBudget = previous })

	dir, attempts := installFakeBRWithAttempts(t, `
printf '%s\n' "`+brWriteLockTimeoutDiagnostic+`" >&2
exit 1
`)

	start := time.Now()
	_, err := RunBdContext(context.Background(), dir, "ready", "--json")
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "still contended") || !strings.Contains(err.Error(), "waiting for write lock") {
		t.Fatalf("RunBdContext error=%v, want contention surfaced after the retry budget", err)
	}
	if got := countFakeBRAttempts(t, attempts); got < 1 || got >= 6 {
		t.Fatalf("fake br attempts=%d, want the deadline to cut retries short of the %d-attempt cap", got, 6)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("RunBdContext took %s, want the total retry deadline to bound it", elapsed)
	}
}

func TestGetRecentlyCompletedListContext(t *testing.T) {
	t.Run("returns_limited_completed_rows", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{".git", ".beads"} {
			if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", name, err)
			}
		}
		binDir := t.TempDir()
		script := "#!/bin/sh\ncase \"$*\" in\n  *\"list --status=closed --json\"*) printf '[{\"id\":\"ntm-done-1\",\"title\":\"first\"},{\"id\":\"ntm-done-2\",\"title\":\"second\"}]\\n' ;;\n  *) printf 'unexpected br arguments: %s\\n' \"$*\" >&2; exit 64 ;;\nesac\n"
		if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
			t.Fatalf("write fake br: %v", err)
		}
		t.Setenv("PATH", binDir)

		items, err := GetRecentlyCompletedListContext(context.Background(), dir, 1)
		if err != nil {
			t.Fatalf("GetRecentlyCompletedListContext error: %v", err)
		}
		if len(items) != 1 || items[0].ID != "ntm-done-1" || items[0].Title != "first" {
			t.Fatalf("completed items=%+v, want first row only", items)
		}
	})

	t.Run("cancels_blocking_br", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{".git", ".beads"} {
			if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", name, err)
			}
		}
		binDir := t.TempDir()
		started := filepath.Join(t.TempDir(), "br-started")
		script := "#!/bin/sh\n: > " + started + "\nexec /bin/sleep 30\n"
		if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
			t.Fatalf("write blocking fake br: %v", err)
		}
		t.Setenv("PATH", binDir)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		type completedResult struct {
			items []BeadPreview
			err   error
		}
		result := make(chan completedResult, 1)
		go func() {
			items, listErr := GetRecentlyCompletedListContext(ctx, dir, 10)
			result <- completedResult{items: items, err: listErr}
		}()
		waitForTestFile(t, started, "blocking fake br startup")
		cancel()

		select {
		case got := <-result:
			if !errors.Is(got.err, context.Canceled) {
				t.Fatalf("items=%+v error=%v, want context canceled", got.items, got.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("blocking completed-list br process ignored cancellation")
		}
	})
}

func TestGetInProgressListContextZeroLimitReturnsFullList(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".git", ".beads"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	binDir := t.TempDir()
	script := "#!/bin/sh\nprintf '[{\"id\":\"ntm-active-1\",\"title\":\"first\"},{\"id\":\"ntm-active-2\",\"title\":\"second\"}]\\n'\n"
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", binDir)

	items, err := GetInProgressListContext(context.Background(), dir, 0)
	if err != nil {
		t.Fatalf("GetInProgressListContext error: %v", err)
	}
	if len(items) != 2 || items[0].ID != "ntm-active-1" || items[1].ID != "ntm-active-2" {
		t.Fatalf("in-progress items=%+v, want both rows", items)
	}
}

func TestUnmarshalBdList(t *testing.T) {
	t.Parallel()

	type bead struct {
		ID string `json:"id"`
	}

	tests := []struct {
		name    string
		input   string
		wantIDs []string
	}{
		{
			name:    "raw_array",
			input:   `[{"id":"bd-1"},{"id":"bd-2"}]`,
			wantIDs: []string{"bd-1", "bd-2"},
		},
		{
			name:    "issues_envelope",
			input:   `{"issues":[{"id":"bd-3"},{"id":"bd-4"}]}`,
			wantIDs: []string{"bd-3", "bd-4"},
		},
		{
			name:    "single_object",
			input:   `{"id":"bd-5"}`,
			wantIDs: []string{"bd-5"},
		},
		{
			name:    "empty_null",
			input:   `null`,
			wantIDs: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := UnmarshalBdList[bead](tt.input)
			if err != nil {
				t.Fatalf("UnmarshalBdList() error = %v", err)
			}

			if len(got) != len(tt.wantIDs) {
				t.Fatalf("len(got) = %d, want %d", len(got), len(tt.wantIDs))
			}
			for i, wantID := range tt.wantIDs {
				if got[i].ID != wantID {
					t.Fatalf("got[%d].ID = %q, want %q", i, got[i].ID, wantID)
				}
			}
		})
	}
}

func TestUnmarshalBdList_RawMessagesFromEnvelope(t *testing.T) {
	t.Parallel()

	raw, err := UnmarshalBdList[json.RawMessage](`{"issues":[{"id":"bd-7"}]}`)
	if err != nil {
		t.Fatalf("UnmarshalBdList() error = %v", err)
	}
	if len(raw) != 1 {
		t.Fatalf("len(raw) = %d, want 1", len(raw))
	}

	var decoded map[string]string
	if err := json.Unmarshal(raw[0], &decoded); err != nil {
		t.Fatalf("json.Unmarshal(raw[0]) error = %v", err)
	}
	if decoded["id"] != "bd-7" {
		t.Fatalf("decoded id = %q, want %q", decoded["id"], "bd-7")
	}
}

func TestCheckDrift_EarlyValidation(t *testing.T) {
	t.Parallel()

	t.Run("missing_project_dir", func(t *testing.T) {
		t.Parallel()

		res := CheckDrift("/path/that/does/not/exist")
		if res.Status != DriftNoBaseline {
			t.Fatalf("Status = %v, want %v", res.Status, DriftNoBaseline)
		}
		if IsInstalled() {
			if !strings.Contains(res.Message, "project directory does not exist") {
				t.Fatalf("Message = %q, want contains %q", res.Message, "project directory does not exist")
			}
		} else {
			if !strings.Contains(res.Message, "bv not installed") {
				t.Fatalf("Message = %q, want contains %q", res.Message, "bv not installed")
			}
		}
	})

	t.Run("missing_beads_dir", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		res := CheckDrift(dir)
		if res.Status != DriftNoBaseline {
			t.Fatalf("Status = %v, want %v", res.Status, DriftNoBaseline)
		}
		if IsInstalled() {
			if !strings.Contains(res.Message, "no .beads directory") {
				t.Fatalf("Message = %q, want contains %q", res.Message, "no .beads directory")
			}
		} else {
			if !strings.Contains(res.Message, "bv not installed") {
				t.Fatalf("Message = %q, want contains %q", res.Message, "bv not installed")
			}
		}
	})
}

func TestGetBeadsSummary_EarlyValidation(t *testing.T) {
	t.Run("missing_project_dir", func(t *testing.T) {
		res := GetBeadsSummary("/path/that/does/not/exist", 3)
		if res.Available {
			t.Fatalf("Available = true, want false")
		}
		if !strings.Contains(res.Reason, "project directory does not exist") {
			t.Fatalf("Reason = %q, want contains %q", res.Reason, "project directory does not exist")
		}
	})

	t.Run("missing_beads_dir", func(t *testing.T) {
		dir := t.TempDir()
		res := GetBeadsSummary(dir, 3)
		if res.Available {
			t.Fatalf("Available = true, want false")
		}
		if !strings.Contains(res.Reason, "no .beads/ directory") {
			t.Fatalf("Reason = %q, want contains %q", res.Reason, "no .beads/ directory")
		}
	})

	t.Run("missing_br_binary_with_beads_dir", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".beads"), 0755); err != nil {
			t.Fatalf("mkdir .beads: %v", err)
		}
		t.Setenv("PATH", "")

		res := GetBeadsSummary(dir, 3)
		if res.Available {
			t.Fatalf("Available = true, want false")
		}
		if res.Reason != "br not installed" {
			t.Fatalf("Reason = %q, want %q", res.Reason, "br not installed")
		}
	})
}

func TestGetBeadsSummaryParsesCurrentStatsSummaryEnvelope(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	binDir := t.TempDir()
	script := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    stats)
      echo '{"summary":{"total_issues":12,"open_issues":7,"in_progress_issues":2,"blocked_issues":3,"ready_issues":4,"closed_issues":5}}'
      exit 0
      ;;
    ready|list)
      echo '[]'
      exit 0
      ;;
  esac
done
exit 1
`
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", binDir)

	summary, err := GetBeadsSummaryContext(context.Background(), dir, 5)
	if err != nil {
		t.Fatalf("GetBeadsSummaryContext: %v", err)
	}
	if !summary.Available {
		t.Fatalf("summary unavailable: %s", summary.Reason)
	}
	if summary.Total != 12 || summary.Open != 7 || summary.InProgress != 2 || summary.Blocked != 3 || summary.Ready != 4 || summary.Closed != 5 {
		t.Fatalf("summary counts = total=%d open=%d in_progress=%d blocked=%d ready=%d closed=%d, want 12/7/2/3/4/5", summary.Total, summary.Open, summary.InProgress, summary.Blocked, summary.Ready, summary.Closed)
	}
}

func TestGetPlanContextRequiresPlanTracksEnvelope(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		wantErr    string
		wantTracks int
	}{
		{name: "missing plan", output: `{}`, wantErr: "missing plan object"},
		{name: "null plan", output: `{"plan":null}`, wantErr: "missing plan object"},
		{name: "missing tracks", output: `{"plan":{}}`, wantErr: "missing plan.tracks array"},
		{name: "null tracks", output: `{"plan":{"tracks":null}}`, wantErr: "missing plan.tracks array"},
		{name: "empty tracks is valid", output: `{"plan":{"tracks":[]}}`, wantTracks: 0},
		{name: "missing track items", output: `{"plan":{"tracks":[{"track_id":"one"}]}}`, wantErr: "missing plan.tracks[0].items array"},
		{name: "null track items", output: `{"plan":{"tracks":[{"track_id":"one","items":null}]}}`, wantErr: "missing plan.tracks[0].items array"},
		{name: "empty track items is valid", output: `{"plan":{"tracks":[{"track_id":"one","items":[]}]}}`, wantTracks: 1},
		{name: "missing item status", output: `{"plan":{"tracks":[{"track_id":"one","items":[{"id":"ntm-test","priority":1}]}]}}`, wantErr: "missing or blank plan.tracks[0].items[0].status"},
		{name: "null item status", output: `{"plan":{"tracks":[{"track_id":"one","items":[{"id":"ntm-test","status":null,"priority":1}]}]}}`, wantErr: "missing or blank plan.tracks[0].items[0].status"},
		{name: "blank item status", output: `{"plan":{"tracks":[{"track_id":"one","items":[{"id":"ntm-test","status":"   ","priority":1}]}]}}`, wantErr: "missing or blank plan.tracks[0].items[0].status"},
		{name: "missing item priority", output: `{"plan":{"tracks":[{"track_id":"one","items":[{"id":"ntm-test","status":"open"}]}]}}`, wantErr: "missing plan.tracks[0].items[0].priority"},
		{name: "null item priority", output: `{"plan":{"tracks":[{"track_id":"one","items":[{"id":"ntm-test","status":"open","priority":null}]}]}}`, wantErr: "missing plan.tracks[0].items[0].priority"},
		{name: "negative item priority", output: `{"plan":{"tracks":[{"track_id":"one","items":[{"id":"ntm-test","status":"open","priority":-1}]}]}}`, wantErr: "priority -1 is outside 0..4"},
		{name: "above-range item priority", output: `{"plan":{"tracks":[{"track_id":"one","items":[{"id":"ntm-test","status":"open","priority":5}]}]}}`, wantErr: "priority 5 is outside 0..4"},
		{name: "explicit priority zero is valid", output: `{"plan":{"tracks":[{"track_id":"one","items":[{"id":"ntm-test","status":"open","priority":0}]}]}}`, wantTracks: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			binDir := t.TempDir()
			script := "#!/bin/sh\nprintf '%s\\n' '" + tt.output + "'\n"
			if err := os.WriteFile(filepath.Join(binDir, "bv"), []byte(script), 0o700); err != nil {
				t.Fatalf("write fake bv: %v", err)
			}
			t.Setenv("PATH", binDir)

			plan, err := GetPlanContext(context.Background(), dir)
			if tt.wantErr != "" {
				if err == nil || plan != nil {
					t.Fatalf("GetPlanContext() = plan:%+v err:%v, want error containing %q", plan, err, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("GetPlanContext error = %q, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetPlanContext() error: %v", err)
			}
			if plan == nil {
				t.Fatal("GetPlanContext() returned nil plan")
			}
			if got := len(plan.Plan.Tracks); got != tt.wantTracks {
				t.Fatalf("len(plan.tracks) = %d, want %d", got, tt.wantTracks)
			}
			if plan.Plan.Tracks == nil {
				t.Fatal("valid empty plan.tracks decoded as nil, want present empty array")
			}
		})
	}
}

func TestGetHealthSummary_NonFatalBottlenecksError(t *testing.T) {
	t.Parallel()

	summary, err := GetHealthSummary("/path/that/does/not/exist")
	if err != nil {
		t.Fatalf("GetHealthSummary err = %v, want nil", err)
	}
	if summary == nil {
		t.Fatalf("GetHealthSummary summary = nil")
	}
	if summary.BottleneckCount != 0 {
		t.Fatalf("BottleneckCount = %d, want 0", summary.BottleneckCount)
	}
	if summary.DriftStatus != DriftNoBaseline {
		t.Fatalf("DriftStatus = %v, want %v", summary.DriftStatus, DriftNoBaseline)
	}
}

func TestGetDependencyContext_HandlesToolErrors(t *testing.T) {
	t.Parallel()

	ctx, err := GetDependencyContext("/path/that/does/not/exist", 3)
	if err != nil {
		t.Fatalf("GetDependencyContext err = %v, want nil", err)
	}
	if ctx == nil {
		t.Fatalf("GetDependencyContext ctx = nil")
	}
	if ctx.BlockedCount != 0 || ctx.ReadyCount != 0 {
		t.Fatalf("BlockedCount/ReadyCount = %d/%d, want 0/0", ctx.BlockedCount, ctx.ReadyCount)
	}
	if len(ctx.InProgressTasks) != 0 {
		t.Fatalf("len(InProgressTasks) = %d, want 0", len(ctx.InProgressTasks))
	}
	if len(ctx.TopBlockers) != 0 {
		t.Fatalf("len(TopBlockers) = %d, want 0", len(ctx.TopBlockers))
	}
}

// An envelope whose list key is present but empty means "no results". It used
// to fall through to the single-object branch, which unmarshalled the
// envelope itself into T (unknown fields are ignored, so it always succeeded)
// and returned one phantom zero-valued item. `br list --json` answers exactly
// this shape for an empty result, so callers with no matching beads got one
// blank bead — the alert generator then emitted a stale-bead alert for ID ""
// because a zero UpdatedAt is older than any threshold.
func TestUnmarshalBdListEmptyEnvelopeReturnsNoItems(t *testing.T) {
	type bead struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	cases := []struct {
		name    string
		payload string
	}{
		{"br list empty result", `{"issues":[],"total":0,"limit":0,"offset":0,"has_more":false}`},
		{"empty beads key", `{"beads":[],"total":0}`},
		{"empty items key", `{"items":[]}`},
		{"null issues", `{"issues":null,"total":0}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := UnmarshalBdList[bead](tc.payload)
			if err != nil {
				t.Fatalf("UnmarshalBdList: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("got %d items (%+v), want 0 — an empty envelope must not yield a phantom bead", len(got), got)
			}
		})
	}
}

// A genuine single object must still parse, so the empty-envelope guard cannot
// simply disable the fallback.
func TestUnmarshalBdListStillAcceptsSingleObject(t *testing.T) {
	type bead struct {
		ID string `json:"id"`
	}
	got, err := UnmarshalBdList[bead](`{"id":"ntm-abc12"}`)
	if err != nil {
		t.Fatalf("UnmarshalBdList: %v", err)
	}
	if len(got) != 1 || got[0].ID != "ntm-abc12" {
		t.Fatalf("got %+v, want the single bead", got)
	}
}

// bd-u7v3p: isEmptyBrListEnvelope recognizes only issues/beads/items, so any
// OTHER JSON object still fell through to the single-object branch — which,
// because json.Unmarshal ignores unknown fields, always succeeded and returned
// one zero-valued phantom item. That is the same failure the empty-envelope
// guard was added for (a stale-bead alert for ID "", an envelope returned as a
// bead), just reached through a different shape.
func TestUnmarshalBdList_UnrecognizedObjectYieldsNoPhantom(t *testing.T) {
	type bead struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	cases := []struct {
		name    string
		payload string
	}{
		{"future envelope key", `{"records":[],"total":0}`},
		{"renamed envelope key", `{"work_items":[{"id":"ntm-1"}],"total":1}`},
		{"error object on stdout", `{"error":"no beads database found","code":"NO_DB"}`},
		{"unrelated metadata object", `{"total":0,"limit":50,"offset":0,"has_more":false}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := UnmarshalBdList[bead](tc.payload)
			if err != nil {
				t.Fatalf("UnmarshalBdList: %v", err)
			}
			for _, item := range got {
				if item.ID == "" {
					t.Fatalf("got a phantom zero-valued bead from %s; alert generators treat ID \"\" with a zero UpdatedAt as a stale bead", tc.payload)
				}
			}
			if len(got) != 0 {
				t.Fatalf("got %d items (%+v), want 0 for a payload that maps onto nothing in T", len(got), got)
			}
		})
	}
}

// A partially-matching object is still a real item and must survive.
func TestUnmarshalBdList_PartiallyPopulatedObjectIsKept(t *testing.T) {
	type bead struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	got, err := UnmarshalBdList[bead](`{"title":"no id but real","extra":"ignored"}`)
	if err != nil {
		t.Fatalf("UnmarshalBdList: %v", err)
	}
	if len(got) != 1 || got[0].Title != "no id but real" {
		t.Fatalf("got %+v, want the single partially-populated bead kept", got)
	}
}

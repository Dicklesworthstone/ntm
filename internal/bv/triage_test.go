package bv

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type controlledErrorContext struct {
	context.Context
	done chan struct{}
	once sync.Once
	mu   sync.RWMutex
	err  error
}

func newControlledErrorContext() *controlledErrorContext {
	return &controlledErrorContext{Context: context.Background(), done: make(chan struct{})}
}

func (c *controlledErrorContext) Done() <-chan struct{} {
	return c.done
}

func (c *controlledErrorContext) Err() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.err
}

func (c *controlledErrorContext) finish(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
	})
}

func TestGetTriageContextCancelsWhileWaitingForRunLock(t *testing.T) {
	triageRunMu.Lock()
	defer triageRunMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	release, err := acquireTriageRunLock(ctx, time.Now().Add(time.Second), time.Second)
	if release != nil {
		release()
		t.Fatal("canceled triage waiter acquired run lock")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquireTriageRunLock error=%v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("triage run-lock cancellation took %s", elapsed)
	}
}

func TestGetTriageContextCancelsRunningSubprocess(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "bv"), []byte("#!/bin/sh\nexec /bin/sleep 10\n"), 0o700); err != nil {
		t.Fatalf("write fake bv: %v", err)
	}
	t.Setenv("PATH", binDir)
	InvalidateTriageCache()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := GetTriageContext(ctx, dir)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("GetTriageContext error=%v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("triage subprocess cancellation took %s", elapsed)
	}
}

// testTriageCache caches the triage response for all tests to share.
// GetTriage takes ~30 seconds, and many tests use the same data.
var testTriageCache struct {
	once   sync.Once
	root   string
	triage *TriageResponse
	err    error
}

// getCachedTriage returns cached triage or fetches it once.
func getCachedTriage(t *testing.T) (*TriageResponse, string) {
	t.Helper()
	testTriageCache.once.Do(func() {
		testTriageCache.root = getProjectRoot()
		if testTriageCache.root == "" {
			return
		}
		// Use the direct GetTriage (which may be uncached on first call)
		testTriageCache.triage, testTriageCache.err = GetTriage(testTriageCache.root)
	})

	if testTriageCache.root == "" {
		t.Skip("No .beads directory found")
	}
	if testTriageCache.err != nil {
		// Skip tests when bv times out - expected for large projects
		if strings.Contains(testTriageCache.err.Error(), "timed out") {
			t.Skipf("bv timed out (expected for large projects): %v", testTriageCache.err)
		}
		t.Fatalf("getCachedTriage: %v", testTriageCache.err)
	}
	return testTriageCache.triage, testTriageCache.root
}

func TestGetTriage(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	// Use cached triage to avoid slow bv command
	triage, _ := getCachedTriage(t)

	if triage == nil {
		t.Fatal("GetTriage returned nil")
	}

	if triage.DataHash == "" {
		t.Error("DataHash should not be empty")
	}

	if triage.Triage.Meta.IssueCount == 0 {
		t.Error("IssueCount should not be 0")
	}

	t.Logf("Triage: %d issues, %d actionable, %d blocked",
		triage.Triage.Meta.IssueCount,
		triage.Triage.QuickRef.ActionableCount,
		triage.Triage.QuickRef.BlockedCount)
}

func TestTriageCache(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	// Ensure test cache is populated first (so we don't hit slow bv command)
	triage1, projectRoot := getCachedTriage(t)

	// The production cache should now be valid (getCachedTriage uses GetTriage)
	if !IsCacheValid() {
		t.Error("Cache should be valid after GetTriage")
	}
	if triageCacheDir != projectRoot {
		t.Errorf("Cache directory mismatch: got %q want %q", triageCacheDir, projectRoot)
	}

	// Second call should return cached result
	triage2, err := GetTriage(projectRoot)
	if err != nil {
		t.Fatalf("Second GetTriage failed: %v", err)
	}

	// Should be the same object (from cache)
	if triage1 != triage2 {
		t.Error("Expected cached result to be returned")
	}

	// Cache age should be reasonable (might be several seconds if tests ran before this)
	age := GetCacheAge()
	if age > 30*time.Second {
		t.Errorf("Cache age too high: %v", age)
	}
}

func TestInvalidateTriageCache(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	// Ensure test cache is populated (so we don't hit slow bv command)
	getCachedTriage(t)

	if !IsCacheValid() {
		t.Error("Cache should be valid")
	}

	// Invalidate
	InvalidateTriageCache()

	if IsCacheValid() {
		t.Error("Cache should be invalid after InvalidateTriageCache")
	}
}

func TestGetTriageQuickRef(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	// Use cached triage to avoid slow bv command
	triage, _ := getCachedTriage(t)

	quickRef := &triage.Triage.QuickRef

	if quickRef.OpenCount == 0 && quickRef.BlockedCount == 0 && quickRef.InProgressCount == 0 {
		t.Log("All counts are 0 - might be an empty project")
	}
}

func TestGetNextRecommendation(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	// Use cached triage to avoid slow bv command
	triage, _ := getCachedTriage(t)

	// Extract next recommendation from cached triage
	var rec *TriageRecommendation
	if len(triage.Triage.Recommendations) > 0 {
		rec = &triage.Triage.Recommendations[0]
	}

	if rec == nil {
		t.Log("No recommendations available")
		return
	}

	if rec.ID == "" {
		t.Error("Recommendation has empty ID")
	}

	if rec.Action == "" {
		t.Error("Recommendation has empty action")
	}

	t.Logf("Top recommendation: %s - %s (score: %.2f)", rec.ID, rec.Title, rec.Score)
}

// TestReadyBeadLabelsMergesOpenListForEpics is the #224 regression: `br ready`
// excludes epic-type beads, so an operator-gated epic surfaced by
// bv --robot-plan must get its labels from the merged `br list --status open`
// pass. Ready entries stay authoritative when a bead appears in both.
func TestReadyBeadLabelsMergesOpenListForEpics(t *testing.T) {
	dir := t.TempDir()
	binDir := t.TempDir()
	script := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    ready)
      echo '[{"id":"br-task","labels":["backend"]},{"id":"br-both","labels":["from-ready"]},{"id":"br-unlabeled","labels":[]},{"id":"br-omitted"}]'
      exit 0
      ;;
    list)
      echo '[{"id":"br-epic","labels":["human-gated"]},{"id":"br-both","labels":["from-list"]},{"id":"br-task","labels":["backend"]},{"id":"br-omitted","labels":["from-open"]}]'
      exit 0
      ;;
  esac
done
echo '[]'
`
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", binDir)

	labels, _, err := readyBeadLabelsContext(context.Background(), dir)
	if err != nil {
		t.Fatalf("readyBeadLabelsContext: %v", err)
	}
	if got := labels["br-epic"]; len(got) != 1 || got[0] != "human-gated" {
		t.Fatalf("epic labels = %v, want [human-gated] (br list must fill br ready's epic gap)", got)
	}
	if got := labels["br-both"]; len(got) != 1 || got[0] != "from-ready" {
		t.Fatalf("merged labels = %v, want [from-ready] (br ready entries must win)", got)
	}
	if got := labels["br-task"]; len(got) != 1 || got[0] != "backend" {
		t.Fatalf("task labels = %v, want [backend]", got)
	}
	if got, verified := labels["br-unlabeled"]; !verified || len(got) != 0 {
		t.Fatalf("verified unlabeled task = %v, present=%t; want present empty labels", got, verified)
	}
	if got := labels["br-omitted"]; len(got) != 1 || got[0] != "from-open" {
		t.Fatalf("omitted ready labels = %v, want open-list fallback [from-open]", got)
	}
}

// TestReadyBeadLabelsOpenListFillsEmptyReadyLabels protects the operator gate
// when br ready covers a bead but omits labels that br list still reports.
func TestReadyBeadLabelsOpenListFillsEmptyReadyLabels(t *testing.T) {
	dir := t.TempDir()
	binDir := t.TempDir()
	script := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    ready)
      echo '[{"id":"br-gated","labels":[]},{"id":"br-ready-wins","labels":["from-ready"]}]'
      exit 0
      ;;
    list)
      echo '[{"id":"br-gated","labels":["human-gated"]},{"id":"br-ready-wins","labels":["from-list"]}]'
      exit 0
      ;;
  esac
done
echo '[]'
`
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", binDir)

	labels, _, err := readyBeadLabelsContext(context.Background(), dir)
	if err != nil {
		t.Fatalf("readyBeadLabelsContext: %v", err)
	}
	if got := labels["br-gated"]; len(got) != 1 || got[0] != "human-gated" {
		t.Fatalf("gated labels = %v, want [human-gated] from open list", got)
	}
	if got := labels["br-ready-wins"]; len(got) != 1 || got[0] != "from-ready" {
		t.Fatalf("non-empty ready labels = %v, want [from-ready]", got)
	}
}

func TestReadyBeadLabelsRejectsMalformedReadyLabelsBeforeOpenListFallback(t *testing.T) {
	for _, test := range []struct {
		name   string
		labels string
	}{
		{name: "null container", labels: `null`},
		{name: "null entry", labels: `[null]`},
		{name: "blank", labels: `["   "]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			binDir := t.TempDir()
			script := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    ready)
      echo '[{"id":"br-gated","labels":` + test.labels + `}]'
      exit 0
      ;;
    list)
      echo '[{"id":"br-gated","labels":["human-gated"]}]'
      exit 0
      ;;
  esac
done
echo '[]'
`
			if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
				t.Fatalf("write fake br: %v", err)
			}
			t.Setenv("PATH", binDir)

			labels, _, err := readyBeadLabelsContext(t.Context(), dir)
			if labels != nil || err == nil {
				t.Fatalf("labels=%v error=%v, want fail-closed malformed-label error", labels, err)
			}
			if !strings.Contains(err.Error(), "null or blank label") || !strings.Contains(err.Error(), "br-gated") {
				t.Fatalf("error=%q, want malformed-label identity", err)
			}
		})
	}
}

// TestReadyBeadLabelsFailsClosedWhenOpenListFails asserts a br list failure is
// fatal: an incomplete label map would silently bypass the operator gate.
func TestReadyBeadLabelsFailsClosedWhenOpenListFails(t *testing.T) {
	dir := t.TempDir()
	binDir := t.TempDir()
	script := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    ready)
      echo '[{"id":"br-task","labels":["backend"]}]'
      exit 0
      ;;
    list)
      echo 'boom' >&2
      exit 3
      ;;
  esac
done
echo '[]'
`
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", binDir)

	if _, _, err := readyBeadLabelsContext(context.Background(), dir); err == nil {
		t.Fatal("readyBeadLabelsContext succeeded despite br list failure; want fail-closed error")
	}
}

func TestGetActionableRecommendationsPreservesLabelCancellationIdentity(t *testing.T) {
	tests := []struct {
		name    string
		wantErr error
	}{
		{name: "explicit cancellation", wantErr: context.Canceled},
		{name: "deadline expiry", wantErr: context.DeadlineExceeded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			binDir := t.TempDir()
			marker := filepath.Join(t.TempDir(), "label-lookup-started")
			bvScript := `#!/bin/sh
case "$1" in
  --robot-triage)
    echo '{"triage":{"recommendations":[]}}'
    ;;
  --robot-plan)
    echo '{"plan":{"tracks":[{"track_id":"one","items":[{"id":"br-blocked","title":"blocked label lookup","status":"open","priority":1}]}]}}'
    ;;
  *)
    echo "unexpected bv args: $*" >&2
    exit 64
    ;;
esac
`
			brScript := `#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "ready" ]; then
    : > "$NTM_TEST_LABEL_MARKER"
    exec /bin/sleep 10
  fi
done
echo '[]'
`
			for name, script := range map[string]string{"bv": bvScript, "br": brScript} {
				if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o700); err != nil {
					t.Fatalf("write fake %s: %v", name, err)
				}
			}
			t.Setenv("PATH", binDir)
			t.Setenv("NTM_TEST_LABEL_MARKER", marker)
			InvalidateTriageCache()
			t.Cleanup(InvalidateTriageCache)

			ctx := newControlledErrorContext()
			defer ctx.finish(context.Canceled)
			type result struct {
				recs []TriageRecommendation
				err  error
			}
			resultCh := make(chan result, 1)
			go func() {
				recs, err := GetActionableRecommendationsContext(ctx, dir, 0)
				resultCh <- result{recs: recs, err: err}
			}()

			waitForTestFile(t, marker, "label lookup startup")
			started := time.Now()
			ctx.finish(tt.wantErr)
			var got result
			select {
			case got = <-resultCh:
			case <-time.After(2 * time.Second):
				t.Fatal("label lookup did not stop within 2s of context completion")
			}
			recs, err := got.recs, got.err
			if recs != nil || !errors.Is(err, tt.wantErr) {
				t.Fatalf("GetActionableRecommendationsContext() = recs:%+v err:%v, want %v", recs, err, tt.wantErr)
			}
			if errors.Is(err, ErrActionableLabelsUnverified) {
				t.Fatalf("cancellation error %v was misclassified as ErrActionableLabelsUnverified", err)
			}
			if elapsed := time.Since(started); elapsed > 2*time.Second {
				t.Fatalf("label lookup cancellation took %s", elapsed)
			}
		})
	}
}

func TestGetActionableRecommendationsRejectsNilContext(t *testing.T) {
	recs, err := GetActionableRecommendationsContext(nil, t.TempDir(), 0)
	if recs != nil || err == nil || !strings.Contains(err.Error(), "context is required") {
		t.Fatalf("GetActionableRecommendationsContext(nil) = recs:%+v err:%v, want context-required error", recs, err)
	}
}

func TestClassifyActionableLabelsErrorPreservesWrappedCancellation(t *testing.T) {
	for _, test := range []struct {
		name    string
		wrapped error
		want    error
	}{
		{
			name:    "canceled transport",
			wrapped: errors.Join(errors.New("label transport stopped"), context.Canceled),
			want:    context.Canceled,
		},
		{
			name:    "deadline transport",
			wrapped: errors.Join(errors.New("label transport timed out"), context.DeadlineExceeded),
			want:    context.DeadlineExceeded,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := classifyActionableLabelsError(t.Context(), test.wrapped)
			if !errors.Is(err, test.want) {
				t.Fatalf("classifyActionableLabelsError() error = %v, want %v", err, test.want)
			}
			if errors.Is(err, ErrActionableLabelsUnverified) {
				t.Fatalf("cancellation error %v was also classified as ErrActionableLabelsUnverified", err)
			}
		})
	}

	cause := errors.New("malformed label response")
	err := classifyActionableLabelsError(t.Context(), cause)
	if !errors.Is(err, ErrActionableLabelsUnverified) {
		t.Fatalf("generic label error = %v, want ErrActionableLabelsUnverified", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("generic label error = %v, want original cause preserved", err)
	}
}

func TestGetActionableRecommendationsFailsClosedWhenPlanItemLabelsAreUnverified(t *testing.T) {
	dir := t.TempDir()
	binDir := t.TempDir()
	bvScript := `#!/bin/sh
case "$1" in
  --robot-triage)
    echo '{"triage":{"recommendations":[]}}'
    ;;
  --robot-plan)
    echo '{"plan":{"tracks":[{"track_id":"one","items":[{"id":"br-missing","title":"missing from tracker sources","status":"open","priority":1}]}]}}'
    ;;
  *)
    echo "unexpected bv args: $*" >&2
    exit 64
    ;;
esac
`
	brScript := `#!/bin/sh
if [ "$1" != "--lock-timeout" ] || [ "$2" != "5000" ]; then
  echo "missing br lock timeout: $*" >&2
  exit 64
fi
shift 2
case "$1" in
  ready|list)
    echo '[]'
    ;;
  *)
    echo "unexpected br args: $*" >&2
    exit 64
    ;;
esac
`
	for name, script := range map[string]string{"bv": bvScript, "br": brScript} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o700); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}
	t.Setenv("PATH", binDir)
	InvalidateTriageCache()
	t.Cleanup(InvalidateTriageCache)

	_, err := GetActionableRecommendationsContext(context.Background(), dir, 0)
	if err == nil || !errors.Is(err, ErrActionableLabelsUnverified) || !strings.Contains(err.Error(), `actionable plan item "br-missing"`) {
		t.Fatalf("GetActionableRecommendationsContext error = %v, want unverified-label failure", err)
	}
}

func installActionableRecommendationTestTools(
	t *testing.T,
	triageOutput string,
	planOutput string,
	planExit int,
	readyOutput string,
	listOutput string,
) {
	t.Helper()
	binDir := t.TempDir()
	planCommand := "printf '%s\\n' '" + planOutput + "'"
	if planExit != 0 {
		planCommand = "printf '%s\\n' 'plan failed' >&2\n    exit 7"
	}
	bvScript := `#!/bin/sh
case "$1" in
  --robot-triage)
    printf '%s\n' '` + triageOutput + `'
    ;;
  --robot-plan)
    ` + planCommand + `
    ;;
  *)
    printf 'unexpected bv args: %s\n' "$*" >&2
    exit 64
    ;;
esac
`
	brScript := `#!/bin/sh
if [ "$1" != "--lock-timeout" ] || [ "$2" != "5000" ]; then
  printf 'missing br lock timeout: %s\n' "$*" >&2
  exit 64
fi
shift 2
case "$1" in
  ready)
    printf '%s\n' '` + readyOutput + `'
    ;;
  list)
    printf '%s\n' '` + listOutput + `'
    ;;
  *)
    printf 'unexpected br args: %s\n' "$*" >&2
    exit 64
    ;;
esac
`
	for name, script := range map[string]string{"bv": bvScript, "br": brScript} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o700); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}
	t.Setenv("PATH", binDir)
	InvalidateTriageCache()
	t.Cleanup(InvalidateTriageCache)
}

func TestGetActionableRecommendationsFailsClosedOnUnverifiedPlan(t *testing.T) {
	tests := []struct {
		name       string
		planOutput string
		planExit   int
		wantError  string
	}{
		{name: "plan command failure", planExit: 7, wantError: "load bv --robot-plan"},
		{name: "plan parse failure", planOutput: `{`, wantError: "parsing plan"},
		{name: "missing plan structure", planOutput: `{}`, wantError: "missing plan object"},
		{name: "missing tracks structure", planOutput: `{"plan":{}}`, wantError: "missing plan.tracks array"},
		{name: "null tracks structure", planOutput: `{"plan":{"tracks":null}}`, wantError: "missing plan.tracks array"},
		{name: "missing track items structure", planOutput: `{"plan":{"tracks":[{"track_id":"one"}]}}`, wantError: "missing plan.tracks[0].items array"},
		{name: "null track items structure", planOutput: `{"plan":{"tracks":[{"track_id":"one","items":null}]}}`, wantError: "missing plan.tracks[0].items array"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installActionableRecommendationTestTools(
				t,
				`{"triage":{"recommendations":[]}}`,
				tt.planOutput,
				tt.planExit,
				`[]`,
				`[]`,
			)

			recs, err := GetActionableRecommendationsContext(context.Background(), t.TempDir(), 0)
			if err == nil || recs != nil {
				t.Fatalf("GetActionableRecommendationsContext() = recs:%+v err:%v, want fail-closed plan error", recs, err)
			}
			if !errors.Is(err, ErrActionablePlanUnverified) {
				t.Fatalf("error = %v, want ErrActionablePlanUnverified", err)
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error = %q, want substring %q", err, tt.wantError)
			}
			if tt.planExit != 0 {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("error = %v, want preserved plan process failure", err)
				}
			}
		})
	}
}

func TestGetActionableRecommendationsClassifiesTriageFailuresAsUnverifiedPlan(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
		want   string
	}{
		{
			name:   "command failure",
			script: "#!/bin/sh\nprintf 'triage unavailable\\n' >&2\nexit 71\n",
			want:   "triage unavailable",
		},
		{
			name:   "parse failure",
			script: "#!/bin/sh\nprintf '{\\n'\n",
			want:   "parsing triage",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			binDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(binDir, "bv"), []byte(test.script), 0o700); err != nil {
				t.Fatalf("write fake bv: %v", err)
			}
			t.Setenv("PATH", binDir)
			InvalidateTriageCache()
			t.Cleanup(InvalidateTriageCache)

			recommendations, err := GetActionableRecommendationsContext(t.Context(), t.TempDir(), 0)
			if recommendations != nil || err == nil || !errors.Is(err, ErrActionablePlanUnverified) ||
				!strings.Contains(err.Error(), "load bv --robot-triage") || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("triage failure recommendations=%+v err=%v, want ErrActionablePlanUnverified containing %q", recommendations, err, test.want)
			}
		})
	}
}

func TestGetActionableRecommendationsRejectsBlankPlanItemID(t *testing.T) {
	installActionableRecommendationTestTools(
		t,
		`{"triage":{"recommendations":[]}}`,
		`{"plan":{"tracks":[{"track_id":"one","items":[{"id":"  \t ","title":"blank","status":"open","priority":1}]}]}}`,
		0,
		`[]`,
		`[]`,
	)

	recs, err := GetActionableRecommendationsContext(context.Background(), t.TempDir(), 0)
	if err == nil || recs != nil {
		t.Fatalf("GetActionableRecommendationsContext() = recs:%+v err:%v, want blank-ID error", recs, err)
	}
	if !errors.Is(err, ErrActionablePlanUnverified) || !strings.Contains(err.Error(), "empty id") {
		t.Fatalf("error = %v, want ErrActionablePlanUnverified with empty-id context", err)
	}
}

func TestGetActionableRecommendationsRejectsUnverifiedPlanItemStatus(t *testing.T) {
	for _, test := range []struct {
		name string
		item string
	}{
		{name: "missing", item: `{"id":"br-covered","title":"covered","priority":1}`},
		{name: "null", item: `{"id":"br-covered","title":"covered","status":null,"priority":1}`},
		{name: "blank", item: `{"id":"br-covered","title":"covered","status":"   ","priority":1}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			installActionableRecommendationTestTools(
				t,
				`{"triage":{"recommendations":[{"id":"br-covered","title":"covered","status":"open","priority":1}]}}`,
				`{"plan":{"tracks":[{"track_id":"one","items":[`+test.item+`]}]}}`,
				0,
				`[{"id":"br-covered","labels":[]}]`,
				`[{"id":"br-covered","labels":[]}]`,
			)

			recs, err := GetActionableRecommendationsContext(t.Context(), t.TempDir(), 0)
			if recs != nil || err == nil || !errors.Is(err, ErrActionablePlanUnverified) {
				t.Fatalf("recommendations=%+v error=%v, want ErrActionablePlanUnverified", recs, err)
			}
			if !strings.Contains(err.Error(), "missing or blank") || !strings.Contains(err.Error(), ".status") {
				t.Fatalf("error=%q, want missing-status context", err)
			}
		})
	}
}

func TestGetActionableRecommendationsRejectsUnverifiedPlanItemPriority(t *testing.T) {
	for _, test := range []struct {
		name string
		item string
	}{
		{name: "missing", item: `{"id":"br-covered","title":"covered","status":"open"}`},
		{name: "null", item: `{"id":"br-covered","title":"covered","status":"open","priority":null}`},
		{name: "negative", item: `{"id":"br-covered","title":"covered","status":"open","priority":-1}`},
		{name: "above range", item: `{"id":"br-covered","title":"covered","status":"open","priority":5}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			installActionableRecommendationTestTools(
				t,
				`{"triage":{"recommendations":[{"id":"br-covered","title":"covered","status":"open","priority":3}]}}`,
				`{"plan":{"tracks":[{"track_id":"one","items":[`+test.item+`]}]}}`,
				0,
				`[{"id":"br-covered","labels":[]}]`,
				`[{"id":"br-covered","labels":[]}]`,
			)

			recs, err := GetActionableRecommendationsContext(t.Context(), t.TempDir(), 0)
			if recs != nil || err == nil || !errors.Is(err, ErrActionablePlanUnverified) {
				t.Fatalf("recommendations=%+v error=%v, want ErrActionablePlanUnverified", recs, err)
			}
			if !strings.Contains(err.Error(), ".priority") {
				t.Fatalf("error=%q, want priority context", err)
			}
		})
	}
}

func TestGetActionableRecommendationsUsesPlanMembershipAndLiveBeadState(t *testing.T) {
	installActionableRecommendationTestTools(
		t,
		`{"triage":{"recommendations":[{"id":"br-overlap","title":"Ranked overlap","status":"blocked","priority":4,"labels":["stale-gate"],"score":99,"blocked_by":["stale-blocker"],"unblocks_ids":["stale-unblock"]},{"id":"br-triage-only","title":"Triage only","status":"open","priority":0,"labels":["triage-only"],"score":100}]}}`,
		`{"plan":{"tracks":[{"track_id":"one","items":[{"id":"br-overlap","title":"Plan overlap","status":"open","priority":1,"unblocks":["br-downstream"]},{"id":"br-plan-only","title":"Plan only","status":"open","priority":2,"unblocks":["br-later"]}]}]}}`,
		0,
		`[{"id":"br-overlap","issue_type":"task","labels":["live-gate"]}]`,
		`[{"id":"br-overlap","issue_type":"task","labels":["stale-list-value"]},{"id":"br-plan-only","issue_type":"feature","labels":["plan-only-live"]},{"id":"br-triage-only","issue_type":"task","labels":["irrelevant"]}]`,
	)

	recs, err := GetActionableRecommendationsContext(context.Background(), t.TempDir(), 0)
	if err != nil {
		t.Fatalf("GetActionableRecommendationsContext() error: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("recommendations = %+v, want overlap plus plan-only item", recs)
	}

	overlap := recs[0]
	if overlap.ID != "br-overlap" || overlap.Title != "Ranked overlap" || overlap.Score != 99 {
		t.Fatalf("ranked overlap identity = %+v, want preserved triage rank metadata", overlap)
	}
	if overlap.Status != "open" || overlap.Priority != 1 {
		t.Fatalf("overlap state = status:%q priority:%d, want plan state open/1", overlap.Status, overlap.Priority)
	}
	if got, want := overlap.Labels, []string{"live-gate"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("overlap labels = %#v, want live labels %#v", got, want)
	}
	if got, want := overlap.UnblocksIDs, []string{"br-downstream"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("overlap unblocks = %#v, want plan unblocks %#v", got, want)
	}
	if len(overlap.BlockedBy) != 0 {
		t.Fatalf("overlap blocked_by = %#v, want plan-authoritative empty blockers", overlap.BlockedBy)
	}

	if overlap.Type != "task" {
		t.Fatalf("overlap issue type = %q, want live-restored %q (GH#242)", overlap.Type, "task")
	}

	planOnly := recs[1]
	if planOnly.ID != "br-plan-only" || planOnly.Title != "Plan only" || planOnly.Status != "open" || planOnly.Priority != 2 {
		t.Fatalf("plan-only recommendation = %+v, want plan item retained", planOnly)
	}
	if planOnly.Type != "feature" {
		t.Fatalf("plan-only issue type = %q, want live-restored %q (GH#242)", planOnly.Type, "feature")
	}
	if got, want := planOnly.Labels, []string{"plan-only-live"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("plan-only labels = %#v, want live labels %#v", got, want)
	}
	for _, rec := range recs {
		if rec.ID == "br-triage-only" {
			t.Fatalf("triage-only recommendation escaped plan membership boundary: %+v", rec)
		}
	}
}

func TestGetActionableRecommendationsExcludesNonOpenPlanRowsBeforeLabelVerification(t *testing.T) {
	installActionableRecommendationTestTools(
		t,
		`{"triage":{"recommendations":[{"id":"br-open","title":"Open work","status":"open","priority":1,"score":10},{"id":"br-progress","title":"Stale recovery candidate","status":"in_progress","priority":1,"score":20}]}}`,
		`{"plan":{"tracks":[{"track_id":"mixed-state","items":[{"id":"br-progress","title":"Stale recovery candidate","status":"in_progress","priority":1},{"id":"br-open","title":"Open work","status":"open","priority":1}]}]}}`,
		0,
		`[{"id":"br-open","issue_type":"task","labels":["verified-live-label"]}]`,
		`[{"id":"br-open","issue_type":"task","labels":["verified-list-label"]}]`,
	)

	recs, err := GetActionableRecommendationsContext(t.Context(), t.TempDir(), 0)
	if err != nil {
		t.Fatalf("GetActionableRecommendationsContext() error: %v", err)
	}
	if len(recs) != 1 || recs[0].ID != "br-open" || recs[0].Status != "open" {
		t.Fatalf("recommendations = %+v, want only verified open work", recs)
	}
	if got, want := recs[0].Labels, []string{"verified-live-label"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("open recommendation labels = %#v, want %#v", got, want)
	}
}

func TestGetActionableRecommendationsAcceptsEmptyPlanTracks(t *testing.T) {
	installActionableRecommendationTestTools(
		t,
		`{"triage":{"recommendations":[{"id":"br-triage-only","title":"Triage only","status":"open","priority":0}]}}`,
		`{"plan":{"tracks":[]}}`,
		0,
		`not valid json`,
		`not valid json`,
	)

	recs, err := GetActionableRecommendationsContext(context.Background(), t.TempDir(), 0)
	if err != nil {
		t.Fatalf("GetActionableRecommendationsContext() empty plan error: %v", err)
	}
	if recs == nil || len(recs) != 0 {
		t.Fatalf("recommendations = %#v, want present empty result", recs)
	}
}

// TestGetActionableRecommendationsFailsClosedWithoutIssueType enforces the
// GH#242 coverage invariant: without a verified issue type the classifier
// cannot prove a plan item is not a container (epic), so assignment stops.
func TestGetActionableRecommendationsFailsClosedWithoutIssueType(t *testing.T) {
	installActionableRecommendationTestTools(
		t,
		`{"triage":{"recommendations":[{"id":"br-open","title":"Open work","status":"open","priority":1,"score":10}]}}`,
		`{"plan":{"tracks":[{"track_id":"one","items":[{"id":"br-open","title":"Open work","status":"open","priority":1}]}]}}`,
		0,
		`[{"id":"br-open","labels":["a-label"]}]`,
		`[{"id":"br-open","labels":["a-label"]}]`,
	)

	if _, err := GetActionableRecommendationsContext(t.Context(), t.TempDir(), 0); err == nil {
		t.Fatal("expected fail-closed error when issue type is absent from br ready and br list")
	} else if !strings.Contains(err.Error(), "issue type") {
		t.Fatalf("error %q should name the missing issue type", err.Error())
	}
}

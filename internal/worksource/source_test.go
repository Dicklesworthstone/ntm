package worksource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

const sampleJSONL = "{\"id\":\"ready\",\"status\":\"open\",\"issue_type\":\"task\"}\n"

func runGit(t *testing.T, project string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", project}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func projectFixture(t *testing.T, data string, withGit bool) string {
	t.Helper()
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTracker(t, project, data)
	if withGit {
		runGit(t, project, "init", "-b", "main")
		runGit(t, project, "add", ".beads/issues.jsonl")
		runGit(t, project, "commit", "-m", "fixture")
	}
	return project
}
func writeTracker(t *testing.T, project, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(project, ".beads", "issues.jsonl"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}
func readFixture(t *testing.T, project string) *Snapshot {
	t.Helper()
	s, err := Read(context.Background(), project, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestIdentityBindsExactBytesAndCheckoutWithoutWrites(t *testing.T) {
	project := projectFixture(t, sampleJSONL, true)
	index := filepath.Join(project, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	s := readFixture(t, project)
	hash := sha256.Sum256([]byte(sampleJSONL))
	if s.Identity.HeadSHA != runGit(t, project, "rev-parse", "HEAD") || s.Identity.JSONLSHA256 != hex.EncodeToString(hash[:]) || s.Dirty {
		t.Fatalf("identity: %+v", s.Identity)
	}
	if err := Validate(context.Background(), s.Identity); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(index)
	afterInfo, _ := os.Stat(index)
	if !bytes.Equal(before, after) || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("source observation modified Git index")
	}
	if got := runGit(t, project, "status", "--porcelain"); got != "" {
		t.Fatalf("source observation dirtied checkout: %s", got)
	}
}

func TestDirtyCheckoutIsAdvisoryAndDoesNotInvalidateIdentity(t *testing.T) {
	project := projectFixture(t, sampleJSONL, true)
	initial := readFixture(t, project)
	if err := os.WriteFile(filepath.Join(project, "new-work.go"), []byte("package work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty := readFixture(t, project)
	if !dirty.Dirty || !initial.Identity.SameSource(dirty.Identity) {
		t.Fatalf("dirty metadata changed identity: %+v", dirty.Identity)
	}
	if err := Validate(context.Background(), initial.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(context.Background(), project, Policy{RequireClean: true}); !errors.Is(err, ErrStale) {
		t.Fatalf("strict policy accepted dirty checkout: %v", err)
	}
}

func TestDigestMismatchEvenWithSameSizeAndTimestamp(t *testing.T) {
	project := projectFixture(t, sampleJSONL, true)
	s := readFixture(t, project)
	path := filepath.Join(project, ".beads", "issues.jsonl")
	info, _ := os.Stat(path)
	writeTracker(t, project, strings.Replace(sampleJSONL, "ready", "other", 1))
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	err := Validate(context.Background(), s.Identity)
	var receipt *StaleError
	if !errors.Is(err, ErrStale) || !errors.As(err, &receipt) || receipt.Expected.JSONLSHA256 == receipt.Observed.JSONLSHA256 {
		t.Fatalf("stale digest receipt: %v", err)
	}
}

func TestHeadMismatchWithoutTrackerChanges(t *testing.T) {
	project := projectFixture(t, sampleJSONL, true)
	s := readFixture(t, project)
	runGit(t, project, "commit", "--allow-empty", "-m", "new head")
	if err := Validate(context.Background(), s.Identity); !errors.Is(err, ErrStale) {
		t.Fatalf("old HEAD authorized current work: %v", err)
	}
}

func TestSourceLocationsAreCanonicalAndWorktreeSpecific(t *testing.T) {
	project := projectFixture(t, sampleJSONL, true)
	s := readFixture(t, project)
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(project, link); err != nil {
		t.Skip(err)
	}
	if alias := readFixture(t, link); !s.Identity.SameSource(alias.Identity) {
		t.Fatal("symlink spelling changed identity")
	}
	worktree := filepath.Join(t.TempDir(), "worktree")
	runGit(t, project, "worktree", "add", "--detach", worktree, "HEAD")
	other := readFixture(t, worktree)
	if s.Identity.SameSource(other.Identity) {
		t.Fatal("different worktree reused source receipt")
	}
	if _, err := Read(context.Background(), worktree, Policy{Expected: &s.Identity}); !errors.Is(err, ErrStale) {
		t.Fatalf("cross-project receipt: %v", err)
	}
}

func TestStrictRefIsOptInLocalAndRejectsExpressions(t *testing.T) {
	project := projectFixture(t, sampleJSONL, true)
	runGit(t, project, "update-ref", "refs/remotes/origin/main", "HEAD")
	if _, err := Read(context.Background(), project, Policy{RequiredRef: "refs/remotes/origin/main", RequireClean: true}); err != nil {
		t.Fatal(err)
	}
	runGit(t, project, "commit", "--allow-empty", "-m", "local work")
	readFixture(t, project) // Remote tracking ref need not be current by default.
	for _, ref := range []string{"refs/remotes/origin/main", "refs/heads/missing", "--help", "HEAD", "refs/heads/main~1", "refs/heads/main\n"} {
		if ref == "refs/heads/main\n" {
			continue
		} // Whitespace trimming is accepted.
		if _, err := Read(context.Background(), project, Policy{RequiredRef: ref}); !errors.Is(err, ErrStale) {
			t.Errorf("ref %q: %v", ref, err)
		}
	}
}

func TestNonGitAndUnbornRepositories(t *testing.T) {
	project := projectFixture(t, sampleJSONL, false)
	if s := readFixture(t, project); s.Identity.HeadSHA != "" {
		t.Fatal("fabricated HEAD outside Git")
	}
	runGit(t, project, "init", "-b", "main")
	if s := readFixture(t, project); s.Identity.HeadSHA != "unborn:refs/heads/main" {
		t.Fatal("unborn branch lost the existing identity contract")
	}
	if _, err := Read(context.Background(), project, Policy{RequireClean: true}); !errors.Is(err, ErrStale) {
		t.Fatalf("strict policy accepted uncommitted project: %v", err)
	}
}

func TestSourceErrorsFailClosed(t *testing.T) {
	for _, data := range []string{"not JSON", "null", "{}", "[]", sampleJSONL + sampleJSONL, "{\"id\":\"x\",\"status\":7}", "{\"id\":\"x\",\"status\":\"open\"} trailing"} {
		t.Run(data[:min(12, len(data))], func(t *testing.T) {
			project := projectFixture(t, data, false)
			if s, err := Read(context.Background(), project, Policy{}); s != nil || !errors.Is(err, ErrStale) {
				t.Fatalf("invalid source became work: %v %+v", err, s)
			}
		})
	}
	if _, err := Read(context.Background(), t.TempDir(), Policy{}); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if _, err := Read(context.Background(), "", Policy{}); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if _, err := Read(nil, "", Policy{}); err == nil {
		t.Fatal("nil context accepted")
	}
	project := projectFixture(t, "\n", false)
	if got := readFixture(t, project).Filter(nil, EligibilityPolicy{}); got.ReasonCode != NoClaimableCode {
		t.Fatal(got)
	}
}

func TestCancellationPreservesContextCause(t *testing.T) {
	project := projectFixture(t, sampleJSONL, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Read(ctx, project, Policy{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := decodeIssues(ctx, []byte(sampleJSONL)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestInheritedGitOverridesCannotRebindProject(t *testing.T) {
	project := projectFixture(t, sampleJSONL, true)
	other := projectFixture(t, strings.Replace(sampleJSONL, "ready", "other", 1), true)
	expected := readFixture(t, project)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	actual := readFixture(t, project)
	if !expected.Identity.SameSource(actual.Identity) {
		t.Fatalf("inherited Git override rebound project: %+v", actual.Identity)
	}
}

func TestEligibilityFiltersAllExclusionsWithoutLeakingTaskText(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	data := strings.Join([]string{
		`{"id":"eligible","status":"open","labels":["program:alpha"]}`,
		`{"id":"closed","status":"closed"}`,
		`{"id":"tombstone","status":"tombstone"}`,
		`{"id":"blocked","status":"open","dependencies":[{"depends_on_id":"working","type":"blocks"}]}`,
		`{"id":"working","status":"in_progress","labels":["mutex:db"]}`,
		`{"id":"assigned","status":"open","assignee":"BlueFox"}`,
		`{"id":"external-owner","status":"open"}`,
		`{"id":"reserved","status":"open"}`,
		`{"id":"gated","status":"open","labels":[" Human-Only "]}`,
		`{"id":"secret","status":"open","labels":["private"],"title":"DO_NOT_LEAK_PRIVATE_TEXT"}`,
		`{"id":"mutex","status":"open","labels":["mutex:db"]}`,
		`{"id":"out-of-scope","status":"open","labels":["program:beta"]}`,
		`{"id":"epic","status":"open","issue_type":"epic"}`,
		`{"id":"deferred","status":"open","defer_until":"2027-01-01T00:00:00Z"}`,
		`{"id":"template","status":"open","is_template":true}`,
	}, "\n")
	rows, err := decodeIssues(context.Background(), []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	s := &Snapshot{issues: rows}
	policy := EligibilityPolicy{Now: now, GatedLabels: []string{"human-only"}, ProgramLabels: []string{"program:alpha"}, OwnedBeads: map[string]string{"external-owner": "peer"}, ReservedBeads: map[string][]string{"reserved": {"peer"}}}
	ids := []string{"eligible", "blocked", "working", "assigned", "external-owner", "reserved", "gated", "secret", "mutex", "out-of-scope", "epic", "deferred", "template", "missing", "eligible"}
	got := s.Filter(ids, policy)
	if !reflect.DeepEqual(got.EligibleIDs, []string{"eligible"}) || len(got.Excluded) != 13 {
		t.Fatalf("eligibility = %+v", got)
	}
	byID := make(map[string]Exclusion)
	for _, e := range got.Excluded {
		byID[e.ID] = e
	}
	for id, reason := range map[string]string{"blocked": "blocked", "working": "lifecycle_not_open", "assigned": "assigned", "external-owner": "assigned", "reserved": "reserved", "gated": "operator_gated", "secret": "private", "mutex": "mutex_held", "out-of-scope": "program_scope", "epic": "container", "deferred": "deferred", "template": "non_dispatchable", "missing": "source_missing"} {
		if !contains(byID[id].Reasons, reason) {
			t.Errorf("%s missing %s: %+v", id, reason, byID[id])
		}
	}
	encoded, _ := json.Marshal(got)
	if bytes.Contains(encoded, []byte("DO_NOT_LEAK")) {
		t.Fatal("private task text leaked into exclusions")
	}
	for i := 0; i < 20; i++ {
		if repeat := s.Filter(ids, policy); !reflect.DeepEqual(got, repeat) {
			t.Fatal("nondeterministic eligibility")
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.Filter(ids, policy) }()
	}
	wg.Wait()
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func TestDependencyKindsMissingBlockersAndClosedDependencies(t *testing.T) {
	for _, kind := range []string{"blocks", "conditional-blocks", "waits-for", ""} {
		for _, status := range []string{"open", "in_progress", "blocked", "deferred", "closed", "tombstone"} {
			data := fmtIssueDependency(kind, status)
			rows, err := decodeIssues(context.Background(), []byte(data))
			if err != nil {
				t.Fatal(err)
			}
			got := (&Snapshot{issues: rows}).Filter([]string{"join"}, EligibilityPolicy{})
			want := status == "closed" || status == "tombstone"
			if (len(got.EligibleIDs) == 1) != want {
				t.Errorf("kind=%q status=%q: %+v", kind, status, got)
			}
		}
	}
	rows, _ := decodeIssues(context.Background(), []byte(`{"id":"join","status":"open","dependencies":[{"depends_on_id":"absent","type":"blocks"}]}`))
	got := (&Snapshot{issues: rows}).Filter([]string{"join"}, EligibilityPolicy{})
	if got.ReasonCode != NoClaimableCode || !reflect.DeepEqual(got.Excluded[0].BlockedBy, []string{"absent"}) {
		t.Fatal(got)
	}
}
func fmtIssueDependency(kind, status string) string {
	data, _ := json.Marshal(issue{ID: "join", Status: "open", Dependencies: []dependency{{ID: "dep", Type: kind}}})
	other, _ := json.Marshal(issue{ID: "dep", Status: status})
	return string(data) + "\n" + string(other)
}

func TestMetadataDependenciesDoNotBlockAndExpiredDeferralIsReady(t *testing.T) {
	rows, err := decodeIssues(context.Background(), []byte(`{"id":"task","status":" OPEN ","defer_until":"2000-01-01T00:00:00Z","dependencies":[{"depends_on_id":"parent","type":"parent-child"},{"depends_on_id":"related","type":"related"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got := (&Snapshot{issues: rows}).Filter([]string{"task"}, EligibilityPolicy{})
	if !reflect.DeepEqual(got.EligibleIDs, []string{"task"}) {
		t.Fatal(got)
	}
	var missing *Snapshot
	if missing.Filter(nil, EligibilityPolicy{}).ReasonCode != StaleCode {
		t.Fatal("nil source marked verified")
	}
}

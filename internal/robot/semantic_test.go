package robot

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Token / label formatting
// ---------------------------------------------------------------------------

func TestPaneWorkTokenFormat(t *testing.T) {
	got := PaneWorkToken("myproj", 1, 2)
	want := "NTM-Pane: myproj/1.2"
	if got != want {
		t.Fatalf("PaneWorkToken = %q, want %q", got, want)
	}
}

func TestPaneBeadLabelSanitizes(t *testing.T) {
	// Session names with characters that are illegal in a bead label must be
	// sanitized to [A-Za-z0-9_-] so the label is always valid, and the function
	// must be deterministic (same input -> same label) so stamp and read agree.
	got := PaneBeadLabel("my proj/v2:x", 0, 3)
	if strings.ContainsAny(got, " /:.") {
		t.Fatalf("PaneBeadLabel kept an illegal character: %q", got)
	}
	if got != PaneBeadLabel("my proj/v2:x", 0, 3) {
		t.Fatalf("PaneBeadLabel not deterministic")
	}
	if !strings.HasPrefix(got, "ntm-pane-") || !strings.HasSuffix(got, "-0-3") {
		t.Fatalf("unexpected label shape: %q", got)
	}
}

// ---------------------------------------------------------------------------
// buildSemanticProgress — the pure safety core
// ---------------------------------------------------------------------------

func TestBuildSemanticProgress_SourceNoneWhenUnstamped(t *testing.T) {
	// Guardrail #4: a pane that was never stamped (no token commit, no labeled
	// bead) yields source "none" and NEVER a wedge — even with terminal velocity.
	now := time.Now()
	sp := buildSemanticProgress(
		PaneWorkToken("s", 0, 1), 30*time.Minute,
		true, // velocity positive (terminal painting)
		gitTokenActivity{}, claimActivity{}, now,
	)
	if sp.Source != "none" {
		t.Fatalf("source = %q, want none", sp.Source)
	}
	if sp.SuspectedWedge != "" {
		t.Fatalf("unstamped pane must never be flagged as a suspected wedge, got %q", sp.SuspectedWedge)
	}
	if sp.LastCommitAt != nil {
		t.Fatalf("last_commit_at should be nil when there is no token commit")
	}
}

func TestBuildSemanticProgress_SourceTokenFromCommit(t *testing.T) {
	now := time.Now()
	last := now.Add(-5 * time.Minute)
	sp := buildSemanticProgress(
		PaneWorkToken("s", 0, 1), 30*time.Minute,
		true,
		gitTokenActivity{commitsInWindow: 2, anyTokenCommit: true, lastCommitAt: &last},
		claimActivity{}, now,
	)
	if sp.Source != "token" {
		t.Fatalf("source = %q, want token", sp.Source)
	}
	if sp.CommitsInWindow != 2 {
		t.Fatalf("commits_in_window = %d, want 2", sp.CommitsInWindow)
	}
	if sp.LastCommitAt == nil {
		t.Fatalf("last_commit_at should be set")
	}
	// Recent commit within window => NOT a wedge.
	if sp.SuspectedWedge != "" {
		t.Fatalf("recent commit must not be flagged as wedge, got %q", sp.SuspectedWedge)
	}
}

func TestBuildSemanticProgress_SourceTokenFromBeadOnly(t *testing.T) {
	// A pane that was stamped (bead carries its label) but has not committed yet
	// is still source "token".
	now := time.Now()
	sp := buildSemanticProgress(
		PaneWorkToken("s", 0, 1), 30*time.Minute,
		false,
		gitTokenActivity{},
		claimActivity{claimsInWindow: 1, anyLabeledBead: true}, now,
	)
	if sp.Source != "token" {
		t.Fatalf("source = %q, want token", sp.Source)
	}
	if sp.ClaimsInWindow != 1 {
		t.Fatalf("claims_in_window = %d, want 1", sp.ClaimsInWindow)
	}
}

func TestBuildSemanticProgress_WedgeTellRequiresAllConditions(t *testing.T) {
	now := time.Now()
	stale := now.Add(-2 * time.Hour) // older than the 30m window
	window := 30 * time.Minute

	// Stamped (token commit exists) + stale + velocity-positive + no progress
	// in window => advisory wedge fires.
	wedge := buildSemanticProgress(
		PaneWorkToken("s", 0, 1), window, true,
		gitTokenActivity{commitsInWindow: 0, anyTokenCommit: true, lastCommitAt: &stale},
		claimActivity{}, now,
	)
	if wedge.SuspectedWedge == "" {
		t.Fatalf("expected suspected_wedge to fire (stamped + stale + velocity + no progress)")
	}

	// Same, but velocity NEGATIVE => no wedge (defer to velocity signal).
	noVel := buildSemanticProgress(
		PaneWorkToken("s", 0, 1), window, false,
		gitTokenActivity{commitsInWindow: 0, anyTokenCommit: true, lastCommitAt: &stale},
		claimActivity{}, now,
	)
	if noVel.SuspectedWedge != "" {
		t.Fatalf("velocity-negative pane must never be flagged wedge, got %q", noVel.SuspectedWedge)
	}

	// Same, but a claim landed in the window => not a wedge (forward motion).
	withClaim := buildSemanticProgress(
		PaneWorkToken("s", 0, 1), window, true,
		gitTokenActivity{commitsInWindow: 0, anyTokenCommit: true, lastCommitAt: &stale},
		claimActivity{claimsInWindow: 1, anyLabeledBead: true}, now,
	)
	if withClaim.SuspectedWedge != "" {
		t.Fatalf("a claim within the window means forward motion; must not be wedge, got %q", withClaim.SuspectedWedge)
	}
}

// ---------------------------------------------------------------------------
// Guardrail #3: the signal can NEVER flip is_working true -> false.
// This mirrors exactly how is_working.go wires the field.
// ---------------------------------------------------------------------------

func TestSemanticNeverFlipsIsWorking(t *testing.T) {
	dir := initTempRepo(t)
	// One stale token commit -> a wedge scenario, the most "dangerous" case.
	stale := time.Now().Add(-2 * time.Hour)
	commitWithTrailer(t, dir, "old work", PaneWorkToken("sess", 0, 1), stale)

	status := PaneWorkStatus{IsWorking: true, IsIdle: false}
	addr := PaneAddr{Session: "sess", Window: 0, Pane: 1}

	// Exact wiring used by GetIsWorking under --semantic:
	status.SemanticProgress = PaneSemanticProgress(addr, dir, 30*time.Minute, status.IsWorking, time.Now())

	if !status.IsWorking {
		t.Fatalf("semantic signal flipped IsWorking to false — guardrail violation")
	}
	if status.IsIdle {
		t.Fatalf("semantic signal flipped IsIdle to true — guardrail violation")
	}
	if status.SemanticProgress == nil || status.SemanticProgress.SuspectedWedge == "" {
		t.Fatalf("expected the wedge advisory to be present in this scenario")
	}
	if status.Recommendation != "" {
		t.Fatalf("semantic signal must not set a recommendation on its own, got %q", status.Recommendation)
	}
}

// ---------------------------------------------------------------------------
// Gate (e): git log --grep token attribution returns the RIGHT pane's commits.
// ---------------------------------------------------------------------------

func TestGitTokenAttributionPerPane(t *testing.T) {
	dir := initTempRepo(t)
	now := time.Now()
	tokenA := PaneWorkToken("sess", 0, 1)
	tokenB := PaneWorkToken("sess", 0, 2)

	commitWithTrailer(t, dir, "pane A work 1", tokenA, now.Add(-10*time.Minute))
	commitWithTrailer(t, dir, "pane A work 2", tokenA, now.Add(-5*time.Minute))
	commitWithTrailer(t, dir, "pane B work 1", tokenB, now.Add(-3*time.Minute))
	commitPlain(t, dir, "unattributed work", now.Add(-1*time.Minute))

	a := gatherGitTokenActivity(dir, tokenA, 30*time.Minute, now)
	if a.commitsInWindow != 2 || !a.anyTokenCommit {
		t.Fatalf("pane A: commits=%d any=%v, want 2/true", a.commitsInWindow, a.anyTokenCommit)
	}

	b := gatherGitTokenActivity(dir, tokenB, 30*time.Minute, now)
	if b.commitsInWindow != 1 || !b.anyTokenCommit {
		t.Fatalf("pane B: commits=%d any=%v, want 1/true (sibling commits must not be miscredited)", b.commitsInWindow, b.anyTokenCommit)
	}

	// A pane that never committed must get NO attribution (source none).
	c := gatherGitTokenActivity(dir, PaneWorkToken("sess", 0, 3), 30*time.Minute, now)
	if c.commitsInWindow != 0 || c.anyTokenCommit {
		t.Fatalf("unstamped pane: commits=%d any=%v, want 0/false", c.commitsInWindow, c.anyTokenCommit)
	}
}

func TestGitTokenWindowExcludesStale(t *testing.T) {
	dir := initTempRepo(t)
	now := time.Now()
	token := PaneWorkToken("sess", 0, 1)
	commitWithTrailer(t, dir, "stale work", token, now.Add(-2*time.Hour))

	a := gatherGitTokenActivity(dir, token, 30*time.Minute, now)
	if a.commitsInWindow != 0 {
		t.Fatalf("stale commit should be outside the 30m window, got %d", a.commitsInWindow)
	}
	if !a.anyTokenCommit {
		t.Fatalf("a stale token commit must still mark the pane as stamped")
	}
	if a.lastCommitAt == nil {
		t.Fatalf("lastCommitAt should reflect the stale commit")
	}
}

func TestGitTokenActivityNonRepoDegradesSafely(t *testing.T) {
	// A directory that is not a git repo must degrade to "no activity", never an
	// error or a wedge.
	dir := t.TempDir()
	a := gatherGitTokenActivity(dir, PaneWorkToken("s", 0, 1), 30*time.Minute, time.Now())
	if a.anyTokenCommit || a.commitsInWindow != 0 || a.lastCommitAt != nil {
		t.Fatalf("non-repo dir should yield zero activity, got %+v", a)
	}
	// Empty repoDir likewise.
	empty := gatherGitTokenActivity("", PaneWorkToken("s", 0, 1), 30*time.Minute, time.Now())
	if empty.anyTokenCommit {
		t.Fatalf("empty repoDir should yield zero activity")
	}
}

// ---------------------------------------------------------------------------
// claims parser (pure)
// ---------------------------------------------------------------------------

func TestCountClaimsInWindow(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-5 * time.Minute).Format(time.RFC3339)
	old := now.Add(-3 * time.Hour).Format(time.RFC3339)

	raw := []byte(fmt.Sprintf(`{"issues":[
		{"status":"in_progress","updated_at":%q,"closed_at":""},
		{"status":"closed","updated_at":%q,"closed_at":%q},
		{"status":"open","updated_at":%q,"closed_at":""}
	]}`, recent, old, recent, old))

	got := countClaimsInWindow(raw, 30*time.Minute, now)
	if !got.anyLabeledBead {
		t.Fatalf("anyLabeledBead should be true with 3 labeled issues")
	}
	if got.claimsInWindow != 2 {
		t.Fatalf("claims_in_window = %d, want 2 (one updated recently, one closed recently)", got.claimsInWindow)
	}
}

func TestCountClaimsInWindowEmptyAndMalformed(t *testing.T) {
	now := time.Now()
	if got := countClaimsInWindow([]byte(`{"issues":[]}`), time.Minute, now); got.anyLabeledBead || got.claimsInWindow != 0 {
		t.Fatalf("empty issues should yield zero, got %+v", got)
	}
	if got := countClaimsInWindow([]byte("not json"), time.Minute, now); got.anyLabeledBead || got.claimsInWindow != 0 {
		t.Fatalf("malformed json should degrade to zero, got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// Additive shape: semantic_progress is omitted when nil, present when set.
// ---------------------------------------------------------------------------

func TestPaneWorkStatusOmitsSemanticWhenNil(t *testing.T) {
	st := PaneWorkStatus{AgentType: "claude", IsWorking: true}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "semantic_progress") {
		t.Fatalf("semantic_progress must be omitted when nil; got %s", b)
	}

	st.SemanticProgress = &SemanticProgress{Source: "none", Token: "NTM-Pane: s/0.1"}
	b, err = json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"semantic_progress"`) {
		t.Fatalf("semantic_progress must be present when set; got %s", b)
	}
	if !strings.Contains(string(b), `"last_commit_at":null`) {
		t.Fatalf("last_commit_at should serialize as explicit null when unset; got %s", b)
	}
}

// ---------------------------------------------------------------------------
// test fixtures
// ---------------------------------------------------------------------------

func initTempRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGitInDir(t, dir, nil, "init", "-q")
	runGitInDir(t, dir, nil, "config", "user.email", "test@ntm.local")
	runGitInDir(t, dir, nil, "config", "user.name", "ntm test")
	runGitInDir(t, dir, nil, "config", "commit.gpgsign", "false")
	return dir
}

func commitWithTrailer(t *testing.T, dir, subject, token string, when time.Time) {
	t.Helper()
	msg := subject + "\n\n" + token + "\n"
	commitMsg(t, dir, msg, when)
}

func commitPlain(t *testing.T, dir, subject string, when time.Time) {
	t.Helper()
	commitMsg(t, dir, subject+"\n", when)
}

var commitSeq int

func commitMsg(t *testing.T, dir, msg string, when time.Time) {
	t.Helper()
	commitSeq++
	// Make a unique change so each commit is non-empty.
	fname := fmt.Sprintf("%s/f%d.txt", dir, commitSeq)
	if err := os.WriteFile(fname, []byte(fmt.Sprintf("change %d\n", commitSeq)), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, dir, nil, "add", "-A")
	stamp := when.Format(time.RFC3339)
	env := append(os.Environ(),
		"GIT_AUTHOR_DATE="+stamp,
		"GIT_COMMITTER_DATE="+stamp,
	)
	runGitInDir(t, dir, env, "commit", "-q", "-m", msg)
}

func runGitInDir(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

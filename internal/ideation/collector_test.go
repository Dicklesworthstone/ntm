package ideation

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollectLocalEvidenceSuccess(t *testing.T) {
	dir := t.TempDir()
	mustWriteCollectorFile(t, filepath.Join(dir, "AGENTS.md"), []byte("agent rules"))
	mustWriteCollectorFile(t, filepath.Join(dir, "README.md"), []byte("readme"))
	mustWriteCollectorFile(t, filepath.Join(dir, "tests", "artifacts", "closeout-proof.md"), []byte("proof"))

	runner := fakeRunner{outputs: map[string][]byte{
		"br ready --json --no-auto-flush --no-auto-import":                                                   []byte(`[{"id":"bd-ready","title":"Ready","status":"open","priority":1,"issue_type":"task","labels":["queue-dry"],"updated_at":"2026-05-09T00:00:00Z"}]`),
		"br list --status open --limit 0 --json --no-auto-flush --no-auto-import":                            []byte(`[{"id":"bd-open","title":"Open","status":"open","priority":2,"issue_type":"task","labels":["ops"]}]`),
		"br list --status in_progress --limit 0 --json --no-auto-flush --no-auto-import":                     []byte(`[{"id":"bd-progress","title":"Progress","status":"in_progress","priority":1,"issue_type":"task","labels":["ops"]}]`),
		"br list --status closed --all --limit 80 --sort updated_at --json --no-auto-flush --no-auto-import": []byte(`[{"id":"bd-2mb03.5","title":"Queue dry operator autopilot","description":"closed family","status":"closed","priority":1,"issue_type":"task","labels":["queue-dry"],"closed_at":"2026-05-08T00:00:00Z"}]`),
		"bv --robot-triage": []byte(`{"triage":{"quick_ref":{"open_count":3,"actionable_count":1,"blocked_count":1,"in_progress_count":1,"top_picks":[{"id":"bd-ready"}]},"recommendations":[],"project_health":{"graph":{"node_count":3,"edge_count":2,"density":0.5,"has_cycles":false,"phase2_ready":true}}}}`),
		"git log -n 8 --name-only --pretty=format:__NTM_COMMIT__%x00%H%x00%s": []byte("__NTM_COMMIT__\x00abc123\x00subject one\ninternal/a.go\ninternal/b.go\n"),
	}}

	snapshot := Collector{Runner: runner}.Collect(context.Background(), CollectorOptions{ProjectDir: dir})
	if !snapshot.Queue.CountsVerified || snapshot.Queue.OpenCount != 3 || snapshot.Queue.ReadyCount != 1 || snapshot.Queue.InProgressCount != 1 {
		t.Fatalf("queue=%+v, want bv-verified counts", snapshot.Queue)
	}
	if len(snapshot.Documents) != 2 || !snapshot.Documents[0].Exists || !snapshot.Documents[1].Exists {
		t.Fatalf("documents=%+v, want AGENTS and README markers", snapshot.Documents)
	}
	if len(snapshot.Git) != 1 || len(snapshot.Git[0].Paths) != 2 {
		t.Fatalf("git=%+v, want one commit with two paths", snapshot.Git)
	}
	if len(snapshot.CloseoutProof) == 0 {
		t.Fatalf("expected local closeout proof evidence")
	}
	if !hasExistingWork(snapshot.ExistingWork, "bd-2mb03.5", "bd-2mb03") {
		t.Fatalf("existing work missing known closed family: %+v", snapshot.ExistingWork)
	}
	if len(snapshot.DegradedSources) != 0 {
		t.Fatalf("degraded sources=%+v, want none", snapshot.DegradedSources)
	}
}

func TestCollectBRUsesNewestClosedWorkForOverlap(t *testing.T) {
	runner := fakeRunner{outputs: map[string][]byte{
		"br ready --json --no-auto-flush --no-auto-import":                               []byte(`[]`),
		"br list --status open --limit 0 --json --no-auto-flush --no-auto-import":        []byte(`[]`),
		"br list --status in_progress --limit 0 --json --no-auto-flush --no-auto-import": []byte(`[]`),
		"br list --status closed --all --limit 80 --sort updated_at --json --no-auto-flush --no-auto-import": []byte(`[{
			"id":"bd-e7xm1",
			"title":"Idea Wizard: queue-dry ideation pipeline vNext",
			"description":"duplicate-aware ranking, dry-run roadmap rendering, novelty guard, gated bead creation, docs, and effectiveness feedback loop are complete",
			"status":"closed",
			"priority":4,
			"issue_type":"epic",
			"labels":["idea-wizard","planning","queue-dry"],
			"updated_at":"2026-05-09T14:14:01Z",
			"closed_at":"2026-05-09T14:14:01Z"
		}]`),
	}}
	snapshot := NewIdeaEvidenceSnapshot(t.TempDir())
	Collector{Runner: runner}.CollectBR(context.Background(), &snapshot, CollectorOptions{ProjectDir: snapshot.Project})
	if !hasExistingWork(snapshot.ExistingWork, "bd-e7xm1", "bd-e7xm1") {
		t.Fatalf("existing work missing newest closed queue-dry family: %+v", snapshot.ExistingWork)
	}

	result := RankCandidates(snapshot, DefaultRankOptions())
	if len(result.Selected) != 0 {
		t.Fatalf("selected=%v, want generated queue-dry plan suppressed after bd-e7xm1 closeout", result.Selected)
	}
	if result.Decision != RankingDecisionReviewRecentWork {
		t.Fatalf("decision=%q, want review_recent_work", result.Decision)
	}
	if len(result.Suppressed) != 1 || result.Suppressed[0].Candidate.ID != "generated-queue-dry-plan" {
		t.Fatalf("suppressed=%v, want generated queue-dry plan", result.Suppressed)
	}
	if result.Suppressed[0].Candidate.Overlap.FamilyID != "bd-e7xm1" {
		t.Fatalf("suppressed family=%q, want bd-e7xm1", result.Suppressed[0].Candidate.Overlap.FamilyID)
	}
}

func TestCollectLocalEvidenceRecordsMissingTool(t *testing.T) {
	runner := fakeRunner{err: map[string]error{
		"br ready --json --no-auto-flush --no-auto-import": errors.New("br not found"),
	}}
	snapshot := Collector{Runner: runner}.Collect(context.Background(), CollectorOptions{ProjectDir: t.TempDir(), GitCommitLimit: 1, SupportBundleLimit: 1})
	if !hasDegradedSource(snapshot, "br:ready") {
		t.Fatalf("degraded=%+v, want br:ready degraded", snapshot.DegradedSources)
	}
}

func TestCollectBVTimeoutBecomesDegradedSource(t *testing.T) {
	runner := fakeRunner{err: map[string]error{
		"bv --robot-triage": context.DeadlineExceeded,
	}}
	snapshot := NewIdeaEvidenceSnapshot(t.TempDir())
	Collector{Runner: runner}.CollectBV(context.Background(), &snapshot, CollectorOptions{ProjectDir: snapshot.Project})
	if !hasDegradedSource(snapshot, "bv:triage") {
		t.Fatalf("degraded=%+v, want bv timeout", snapshot.DegradedSources)
	}
	if len(snapshot.Triage.Warnings) == 0 {
		t.Fatalf("warnings empty, want bv timeout warning")
	}
}

func TestCollectBRMalformedJSONBecomesDegradedSource(t *testing.T) {
	runner := fakeRunner{outputs: map[string][]byte{
		"br ready --json --no-auto-flush --no-auto-import": []byte(`[`),
	}}
	snapshot := NewIdeaEvidenceSnapshot(t.TempDir())
	Collector{Runner: runner}.CollectBR(context.Background(), &snapshot, CollectorOptions{ProjectDir: snapshot.Project})
	if !hasDegradedSource(snapshot, "br:ready") {
		t.Fatalf("degraded=%+v, want parse failure", snapshot.DegradedSources)
	}
}

func TestCollectorHugeOutputBecomesDegradedSource(t *testing.T) {
	runner := fakeRunner{outputs: map[string][]byte{
		"git log -n 8 --name-only --pretty=format:__NTM_COMMIT__%x00%H%x00%s": []byte(strings.Repeat("x", 64)),
	}}
	snapshot := NewIdeaEvidenceSnapshot(t.TempDir())
	Collector{Runner: runner}.CollectGit(context.Background(), &snapshot, CollectorOptions{ProjectDir: snapshot.Project, OutputLimitBytes: 8})
	if !hasDegradedSource(snapshot, "git:recent") {
		t.Fatalf("degraded=%+v, want huge output failure", snapshot.DegradedSources)
	}
}

func TestCollectGitUnavailableIsOptionalDegradedSource(t *testing.T) {
	runner := fakeRunner{err: map[string]error{
		"git log -n 8 --name-only --pretty=format:__NTM_COMMIT__%x00%H%x00%s": errors.New("not a git repo"),
	}}
	snapshot := NewIdeaEvidenceSnapshot(t.TempDir())
	Collector{Runner: runner}.CollectGit(context.Background(), &snapshot, CollectorOptions{ProjectDir: snapshot.Project})
	if !hasDegradedSource(snapshot, "git:recent") {
		t.Fatalf("degraded=%+v, want git degraded", snapshot.DegradedSources)
	}
	if len(snapshot.DegradedSources) > 0 && snapshot.DegradedSources[0].Severity != ValidationWarning {
		t.Fatalf("severity=%q, want optional warning", snapshot.DegradedSources[0].Severity)
	}
}

func TestCollectBRDoesNotMutateIssuesJSONL(t *testing.T) {
	if _, err := exec.LookPath("br"); err != nil {
		t.Skip("br not installed")
	}
	dir := t.TempDir()
	runCollectorCommand(t, dir, "git", "init", "-q")
	runCollectorCommand(t, dir, "br", "init", "--prefix", "bd")
	runCollectorCommand(t, dir, "br", "create", "--title", "Ready one", "--type", "task", "--priority", "1")
	runCollectorCommand(t, dir, "br", "sync", "--flush-only")

	issuesPath := filepath.Join(dir, ".beads", "issues.jsonl")
	before := mustReadCollectorFile(t, issuesPath)
	snapshot := NewIdeaEvidenceSnapshot(dir)
	Collector{Runner: ExecCommandRunner{OutputLimitBytes: defaultCollectorOutputLimit}}.CollectBR(context.Background(), &snapshot, CollectorOptions{ProjectDir: dir})
	after := mustReadCollectorFile(t, issuesPath)
	if string(before) != string(after) {
		t.Fatalf("issues.jsonl mutated by collector")
	}
	if snapshot.Queue.ReadyCount != 1 {
		t.Fatalf("ready=%d, want 1", snapshot.Queue.ReadyCount)
	}
}

type fakeRunner struct {
	outputs map[string][]byte
	err     map[string]error
}

func (runner fakeRunner) Run(ctx context.Context, workdir string, name string, args []string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	if err := runner.err[key]; err != nil {
		return nil, err
	}
	if output, ok := runner.outputs[key]; ok {
		return output, nil
	}
	return []byte(`[]`), nil
}

func hasExistingWork(items []ExistingWorkFingerprint, id, family string) bool {
	for _, item := range items {
		if item.ID == id && item.FamilyID == family {
			return true
		}
	}
	return false
}

func hasDegradedSource(snapshot IdeaEvidenceSnapshot, sourceID string) bool {
	for _, note := range snapshot.DegradedSources {
		if note.SourceID == sourceID {
			return true
		}
	}
	return false
}

func mustWriteCollectorFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func mustReadCollectorFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return data
}

func runCollectorCommand(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, output)
	}
}

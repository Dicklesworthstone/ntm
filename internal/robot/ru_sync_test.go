package robot

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestParseRUSyncPayload buckets every status ru's aggregate_results knows
// (repo_updater/ru) the same way ru's own summary does.
func TestParseRUSyncPayload(t *testing.T) {
	payload := `{"generated_at":"2026-10-06T21:16:50Z","version":"1.5.0","output_format":"json","command":"sync",
"data":{"summary":{"total":17},"repos":[
 {"name":"o/cloned","status":"ok"},{"name":"o/updated","status":"updated"},{"name":"o/current","status":"current"},
 {"name":"o/skipped","status":"skipped"},{"name":"o/dry","status":"dry_run"},{"name":"o/newer","status":"something_new"},
 {"name":"o/failed","status":"failed"},{"name":"o/timeout","status":"timeout"},{"name":"o/dep","status":"dep_error"},{"name":"o/auth","status":"auth_error"},
 {"name":"o/diverged","status":"diverged"},{"name":"o/dirty","status":"dirty"},{"name":"o/mismatch","status":"mismatch"},
 {"name":"o/notgit","status":"not_git"},{"name":"o/branch","status":"branch_error"},{"name":"o/noremote","status":"no_remote"},
 {"name":"o/noupstream","status":"no_upstream"}]},
"_meta":{"duration_seconds":0,"exit_code":2}}`

	got, err := parseRUSyncPayload([]byte(payload))
	if err != nil {
		t.Fatalf("parseRUSyncPayload error: %v", err)
	}
	if want := []string{"o/cloned", "o/updated", "o/current"}; !reflect.DeepEqual(got.repos.Synced, want) {
		t.Errorf("synced = %v, want %v", got.repos.Synced, want)
	}
	if want := []string{"o/skipped", "o/dry", "o/newer"}; !reflect.DeepEqual(got.repos.Skipped, want) {
		t.Errorf("skipped = %v, want %v", got.repos.Skipped, want)
	}
	if want := []string{"o/failed", "o/timeout", "o/dep", "o/auth"}; !reflect.DeepEqual(got.failed, want) {
		t.Errorf("failed = %v, want %v", got.failed, want)
	}
	if want := []string{"o/diverged", "o/dirty", "o/mismatch", "o/notgit", "o/branch", "o/noremote", "o/noupstream"}; !reflect.DeepEqual(got.conflicts, want) {
		t.Errorf("conflicts = %v, want %v", got.conflicts, want)
	}
}

// TestParseRUSyncPayloadListsRepeatedRepoOnce: installed ru (1.5.0) sometimes
// writes a repo's mismatch result twice; ntm lists it once per bucket.
func TestParseRUSyncPayloadListsRepeatedRepoOnce(t *testing.T) {
	payload := `{"command":"sync","data":{"repos":[
 {"name":"o/demo","status":"mismatch"},{"name":"o/absent","status":"dry_run"},{"name":"o/demo","status":"mismatch"},
 {"name":"o/fresh","status":"ok"},{"name":"o/fresh","status":"current"}]}}`

	got, err := parseRUSyncPayload([]byte(payload))
	if err != nil {
		t.Fatalf("parseRUSyncPayload error: %v", err)
	}
	if !reflect.DeepEqual(got.conflicts, []string{"o/demo"}) || !reflect.DeepEqual(got.repos.Skipped, []string{"o/absent"}) ||
		!reflect.DeepEqual(got.repos.Synced, []string{"o/fresh"}) {
		t.Errorf("conflicts=%v skipped=%v synced=%v, want each repo once", got.conflicts, got.repos.Skipped, got.repos.Synced)
	}
}

func TestParseRUSyncPayloadRejectsNonEnvelopes(t *testing.T) {
	for _, payload := range []string{"", "not-json", `{"synced":["repo-a"]}`, `[{"name":"a","status":"ok"}]`} {
		if _, err := parseRUSyncPayload([]byte(payload)); err == nil {
			t.Errorf("parseRUSyncPayload(%q) = nil error, want one", payload)
		}
	}
}

// TestGetRUSyncReportsInstalledRUConflicts runs the installed ru against an
// isolated config: one listed repo is cloned locally from an origin that is not
// the configured GitHub URL (ru: mismatch, a conflict) and one is absent
// (dry_run). Dry-run keeps ru from cloning, so no network is touched.
func TestGetRUSyncReportsInstalledRUConflicts(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("ru"); err != nil {
		t.Skip("ru not installed")
	}
	root := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@example.com", "-c", "user.name=t"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git(root, "init", "-q", "--bare", "origin.git")
	git(root, "clone", "-q", "origin.git", "seed")
	git(filepath.Join(root, "seed"), "commit", "-q", "--allow-empty", "-m", "init")
	git(filepath.Join(root, "seed"), "push", "-q", "origin", "HEAD:main")
	git(root, "clone", "-q", "-b", "main", "origin.git", filepath.Join("projects", "demo"))

	reposDir := filepath.Join(root, "config", "repos.d")
	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reposDir, "public.txt"), []byte("testowner/demo\ntestowner/absent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "home"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := GetRUSync(RUSyncOptions{
		DryRun: true,
		extraEnv: []string{
			"HOME=" + filepath.Join(root, "home"),
			"RU_CONFIG_DIR=" + filepath.Join(root, "config"),
			"RU_PROJECTS_DIR=" + filepath.Join(root, "projects"),
		},
	})
	if err != nil {
		t.Fatalf("GetRUSync error: %v", err)
	}
	if out.ExitCode != 2 || out.Success || out.ErrorCode != "RU_CONFLICTS" {
		t.Fatalf("GetRUSync = exit:%d success:%t code:%q err:%q stderr:%q, want ru's conflict exit as RU_CONFLICTS",
			out.ExitCode, out.Success, out.ErrorCode, out.Error, out.Stderr)
	}
	if !reflect.DeepEqual(out.Conflicts, []string{"testowner/demo"}) || !reflect.DeepEqual(out.Repos.Skipped, []string{"testowner/absent"}) {
		t.Fatalf("GetRUSync conflicts=%v skipped=%v synced=%v, want demo as conflict and absent as skipped",
			out.Conflicts, out.Repos.Skipped, out.Repos.Synced)
	}
}

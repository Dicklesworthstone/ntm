package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func gitCaptureRecoveryRepo(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	root := t.TempDir()
	gitSnapshotTestCommand(t, root, "init", "-q", "-b", "main")
	gitSnapshotTestCommand(t, root, "config", "user.name", "Capture test")
	gitSnapshotTestCommand(t, root, "config", "user.email", "capture@example.invalid")
	gitSnapshotTestWrite(t, root, "tracked.txt", []byte("base\n"), 0644)
	gitSnapshotTestWrite(t, root, "asset.bin", []byte{0, 1, 2, 255}, 0644)
	gitSnapshotTestWrite(t, root, "sub/.keep", nil, 0644)
	gitSnapshotTestWrite(t, root, ".gitignore", []byte(".env\nignored/\n"), 0644)
	gitSnapshotTestCommand(t, root, "add", ".")
	gitSnapshotTestCommand(t, root, "commit", "-qm", "baseline")
	return root, gitSnapshotTestCommand(t, root, "rev-parse", "HEAD")
}

func gitCaptureTestSession(t *testing.T, workDir string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("requires a tmux session")
	}
	testutil.RequireTmuxThrottled(t)
	session := fmt.Sprintf("ntm-git-recovery-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(session, workDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(session) })
	return session
}

func gitCaptureTestOptions(include bool) []CheckpointOption {
	return []CheckpointOption{
		WithUntrackedCapture(include), WithScrollbackLines(0),
		func(o *checkpointOptions) { o.captureAssignments = false; o.captureBVSnapshot = false },
	}
}

func TestCheckpointCreateCapturesRecoverableBinaryAndUntrackedFiles(t *testing.T) {
	root, _ := gitCaptureRecoveryRepo(t)
	binary := []byte{0, 254, 129, 1, 4, 0}
	gitSnapshotTestWrite(t, root, "asset.bin", binary, 0644)
	gitSnapshotTestWrite(t, root, "new-source.go", []byte("package saved\n"), 0644)
	gitSnapshotTestWrite(t, root, "empty", nil, 0644)
	gitSnapshotTestWrite(t, root, ".env", []byte("never captured\n"), 0600)
	session := gitCaptureTestSession(t, filepath.Join(root, "sub"))
	storage := NewStorageWithDir(t.TempDir())
	cp, err := NewCapturerWithStorage(storage).Create(session, "full-code", gitCaptureTestOptions(true)...)
	if err != nil {
		t.Fatal(err)
	}
	if !cp.Git.Captured || !cp.Git.IsDirty || cp.Git.UntrackedCount != 2 || cp.Git.UntrackedCaptured != 2 || cp.Git.PatchFile == "" {
		t.Fatalf("incomplete recorded code snapshot: %+v", cp.Git)
	}
	loaded, err := storage.Load(session, cp.ID)
	if err != nil || loaded.Git.UntrackedCaptured != 2 || loaded.ArtifactIntegrity == nil {
		t.Fatalf("snapshot metadata did not survive persistence: %+v, %v", loaded, err)
	}
	patch, err := storage.LoadGitPatch(session, cp.ID)
	if err != nil {
		t.Fatal(err)
	}
	recovery := gitSnapshotTestApply(t, root, patch)
	if got, err := os.ReadFile(filepath.Join(recovery, "asset.bin")); err != nil || !bytes.Equal(got, binary) {
		t.Fatalf("stored binary patch did not recover data: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(recovery, "new-source.go")); err != nil || string(got) != "package saved\n" {
		t.Fatalf("stored untracked patch did not recover source: %q, %v", got, err)
	}
}

func TestCheckpointCreateDefaultOmitsUntrackedWithAccurateCounts(t *testing.T) {
	root, _ := gitCaptureRecoveryRepo(t)
	gitSnapshotTestWrite(t, root, "new/a.go", []byte("a\n"), 0644)
	gitSnapshotTestWrite(t, root, "new/b.go", []byte("b\n"), 0644)
	// Even a user's status configuration hiding untracked files must not
	// make the saved snapshot claim there was no uncaptured work.
	gitSnapshotTestCommand(t, root, "config", "status.showUntrackedFiles", "no")
	session := gitCaptureTestSession(t, filepath.Join(root, "sub"))
	cp, err := NewCapturerWithStorage(NewStorageWithDir(t.TempDir())).Create(session, "default", gitCaptureTestOptions(false)...)
	if err != nil {
		t.Fatal(err)
	}
	if cp.Git.UntrackedCount != 2 || cp.Git.UntrackedCaptured != 0 || cp.Git.PatchFile != "" {
		t.Fatalf("default capture hid omitted files or captured them implicitly: %+v", cp.Git)
	}
}

func TestCheckpointCreateRequestedGitFailurePreservesPartialCheckpoint(t *testing.T) {
	root, _ := gitCaptureRecoveryRepo(t)
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0755); err != nil {
		t.Fatal(err)
	}
	gitSnapshotTestCommand(t, nested, "init", "-q", "-b", "main")
	gitSnapshotTestWrite(t, nested, "not-omittable.go", []byte("nested work\n"), 0644)
	session := gitCaptureTestSession(t, filepath.Join(root, "sub"))
	storage := NewStorageWithDir(t.TempDir())
	cp, err := NewCapturerWithStorage(storage).Create(session, "incomplete", gitCaptureTestOptions(true)...)
	if err == nil || cp == nil {
		t.Fatalf("explicit snapshot failure must return a preserved checkpoint AND error: %+v, %v", cp, err)
	}
	if cp.Git.Captured || cp.Git.SkipReason != GitSkipCaptureFailed || cp.Git.UntrackedCaptured != 0 || cp.Git.PatchFile != "" {
		t.Fatalf("failed snapshot presented as saved code: %+v", cp.Git)
	}
	loaded, loadErr := storage.Load(session, cp.ID)
	if loadErr != nil || loaded.Git.SkipReason != GitSkipCaptureFailed || !strings.Contains(err.Error(), cp.ID) {
		t.Fatalf("partial checkpoint identity/failure was not preserved: %+v, %v, %v", loaded, loadErr, err)
	}
}

func TestCheckpointCreateRejectsUntrackedWithoutGitBeforeSessionLookup(t *testing.T) {
	cp, err := NewCapturerWithStorage(NewStorageWithDir(t.TempDir())).Create("not-a-session", "invalid",
		WithGitCapture(false), WithUntrackedCapture(true))
	if err == nil || cp != nil || !strings.Contains(err.Error(), "requires git capture") {
		t.Fatalf("incompatible options were not rejected first: %+v, %v", cp, err)
	}
}

func gitRecoveryTestCheckpoint(t *testing.T, root, commit, patch string) (*Storage, *Checkpoint) {
	t.Helper()
	storage := NewStorageWithDir(t.TempDir())
	cp := &Checkpoint{Version: CurrentVersion, ID: "code-snapshot", SessionName: "snapshot-session", WorkingDir: root, CreatedAt: time.Now(),
		Git: GitState{Captured: true, Commit: commit, IsDirty: true, PatchFile: GitPatchFile}}
	if err := storage.SaveGitPatch(cp.SessionName, cp.ID, patch); err != nil {
		t.Fatal(err)
	}
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}
	return storage, cp
}

func TestGitRollbackPreflightAndApplyUseVerifiedBytes(t *testing.T) {
	root, commit := gitCaptureRecoveryRepo(t)
	gitSnapshotTestWrite(t, root, "asset.bin", []byte{0, 44, 255}, 0644)
	gitSnapshotTestWrite(t, root, "new-source.go", []byte("package saved\n"), 0644)
	snapshot, err := captureWorkingTreePatch(context.Background(), root, commit, gitPatchCaptureOptions{IncludeUntracked: true, MaxBytes: MaxGitOutputBytes})
	if err != nil {
		t.Fatal(err)
	}
	storage, cp := gitRecoveryTestCheckpoint(t, root, commit, snapshot.Patch)
	indexBefore, err := os.ReadFile(filepath.Join(root, ".git/index"))
	if err != nil {
		t.Fatal(err)
	}
	statusBefore := gitSnapshotTestCommand(t, root, "status", "--porcelain")
	plan, err := PrepareGitRollback(context.Background(), storage, cp, filepath.Join(root, "sub"))
	if err != nil || plan.WorkDir != root || plan.Commit != commit || plan.Patch != snapshot.Patch {
		t.Fatalf("preflight = %+v, %v", plan, err)
	}
	indexAfter, _ := os.ReadFile(filepath.Join(root, ".git/index"))
	if !bytes.Equal(indexBefore, indexAfter) || gitSnapshotTestCommand(t, root, "status", "--porcelain") != statusBefore || gitSnapshotTestCommand(t, root, "rev-parse", "HEAD") != commit {
		t.Fatal("preflight changed working files, the real staging index or HEAD")
	}
	recovery := filepath.Join(t.TempDir(), "recovery")
	gitSnapshotTestCommand(t, root, "clone", "--quiet", "--local", root, recovery)
	plan, err = PrepareGitRollback(context.Background(), storage, cp, filepath.Join(recovery, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	// Applying must not reopen the artifact after preflight verified it.
	if err := os.WriteFile(filepath.Join(storage.CheckpointDir(cp.SessionName, cp.ID), GitPatchFile), []byte("replacement corruption"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyGitRollbackPatch(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(recovery, "asset.bin")); err != nil || !bytes.Equal(got, []byte{0, 44, 255}) {
		t.Fatalf("binary recovery failed: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(recovery, "new-source.go")); err != nil || string(got) != "package saved\n" {
		t.Fatalf("untracked recovery failed: %q, %v", got, err)
	}
}

func TestGitRollbackPreflightRejectsCorruptAndUnusablePatches(t *testing.T) {
	for _, kind := range []string{"corrupt", "malformed", "wrong-base", "missing"} {
		t.Run(kind, func(t *testing.T) {
			root, commit := gitCaptureRecoveryRepo(t)
			gitSnapshotTestWrite(t, root, "tracked.txt", []byte("saved\n"), 0644)
			snapshot, err := captureWorkingTreePatch(context.Background(), root, commit, gitPatchCaptureOptions{MaxBytes: MaxGitOutputBytes})
			if err != nil {
				t.Fatal(err)
			}
			patch := snapshot.Patch
			if kind == "malformed" {
				patch = "not a patch\n"
			}
			if kind == "wrong-base" {
				patch = strings.ReplaceAll(patch, "-base\n", "-other-base\n")
			}
			storage, cp := gitRecoveryTestCheckpoint(t, root, commit, patch)
			if kind == "corrupt" {
				patch = strings.ReplaceAll(patch, "+saved\n", "+wrong\n")
				if err := os.WriteFile(filepath.Join(storage.CheckpointDir(cp.SessionName, cp.ID), GitPatchFile), []byte(patch), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "missing" {
				// Use a different declared artifact rather than deleting data.
				cp.Git.PatchFile = "missing.patch"
				cp.ArtifactIntegrity = nil
			}
			index, _ := os.ReadFile(filepath.Join(root, ".git/index"))
			status := gitSnapshotTestCommand(t, root, "status", "--porcelain")
			plan, err := PrepareGitRollback(context.Background(), storage, cp, root)
			if err == nil || plan != nil {
				t.Fatalf("unusable %s patch accepted: %+v, %v", kind, plan, err)
			}
			if kind == "corrupt" && !errors.Is(err, ErrArtifactIntegrity) {
				t.Fatalf("corruption not classified as artifact integrity failure: %v", err)
			}
			after, _ := os.ReadFile(filepath.Join(root, ".git/index"))
			if !bytes.Equal(index, after) || gitSnapshotTestCommand(t, root, "status", "--porcelain") != status || gitSnapshotTestCommand(t, root, "rev-parse", "HEAD") != commit {
				t.Fatal("failed preflight mutated the repository")
			}
		})
	}
}

func TestGitRollbackLegacyWarningAndCancellation(t *testing.T) {
	root, commit := gitCaptureRecoveryRepo(t)
	gitSnapshotTestWrite(t, root, "tracked.txt", []byte("saved\n"), 0644)
	snapshot, err := captureWorkingTreePatch(context.Background(), root, commit, gitPatchCaptureOptions{MaxBytes: MaxGitOutputBytes})
	if err != nil {
		t.Fatal(err)
	}
	storage, cp := gitRecoveryTestCheckpoint(t, root, commit, snapshot.Patch)
	cp.ArtifactIntegrity = nil
	cp.Git.UntrackedCount = 2
	plan, err := PrepareGitRollback(context.Background(), storage, cp, root)
	if err != nil || len(plan.Warnings) != 2 {
		t.Fatalf("legacy/uncaptured warnings not preserved: %+v, %v", plan, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PrepareGitRollback(ctx, storage, cp, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preflight: %v", err)
	}
	if err := ApplyGitRollbackPatch(ctx, plan); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled apply: %v", err)
	}
}

func TestGitCheckpointExportReportsOnlyIncludedUntrackedWork(t *testing.T) {
	cp := &Checkpoint{Git: GitState{PatchFile: GitPatchFile, UntrackedCount: 3, UntrackedCaptured: 3}}
	for _, include := range []bool{false, true} {
		exported := rewriteCheckpointForExport(cp, ExportOptions{IncludeGitPatch: include})
		if include {
			if exported.Git.UntrackedCaptured != 3 || exported.Git.PatchFile != GitPatchFile {
				t.Fatalf("included patch lost its capture metadata: %+v", exported.Git)
			}
		} else if exported.Git.UntrackedCaptured != 0 || exported.Git.PatchFile != "" {
			t.Fatalf("filtered export claims to preserve omitted files: %+v", exported.Git)
		}
		if cp.Git.UntrackedCaptured != 3 || cp.Git.PatchFile != GitPatchFile || exported.Git.UntrackedCount != 3 {
			t.Fatal("export filtering mutated the original or hid the original untracked count")
		}
	}
}

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func journalFixture(t *testing.T) (*WorktreeManager, *MergePreview) {
	t.Helper()
	plan := fenceFixture(t)
	parent := filepath.Join(plan.TargetPath, ".ntm", "worktrees", plan.SessionID)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plan.TargetPath, ".git", "info", "exclude"), []byte(".ntm/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, plan.AgentName)
	fenceGit(t, plan.TargetPath, "worktree", "move", plan.SourcePath, path)
	plan.SourcePath = path
	return NewManager(plan.TargetPath, plan.SessionID), plan
}

func prepareJournalFixture(t *testing.T, plan *MergePreview) *MergeRecovery {
	t.Helper()
	fence, err := acquireMergeFence(context.Background(), plan.TargetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.close()
	record, err := fence.prepareMerge(plan)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestMergeRecoveryPublishesVerifiedReceiptThroughMergeBack(t *testing.T) {
	manager, plan := journalFixture(t)
	if err := manager.MergeBack(context.Background(), plan.AgentName); err != nil {
		t.Fatal(err)
	}
	record, err := manager.InspectMerge(context.Background())
	if err != nil || record == nil || record.State != "committed" || record.Commit != fenceGit(t, plan.TargetPath, "rev-parse", "HEAD") || record.Plan.SourceCommit != plan.SourceCommit {
		t.Fatalf("successful merge lacks its verified receipt: %+v %v", record, err)
	}
	before, err := os.ReadFile(filepath.Join(plan.TargetPath, ".git", "ntm-merge.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.MergeBack(context.Background(), plan.AgentName); err != nil {
		t.Fatalf("already-merged retry: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(plan.TargetPath, ".git", "ntm-merge.json"))
	if string(before) != string(after) {
		t.Fatal("no-op retry replaced the real merge receipt")
	}
}

func TestMergeRecoveryCrashBarrierAndExplicitAbandonment(t *testing.T) {
	manager, plan := journalFixture(t)
	child, path := startFenceChild(t, plan, "prepare")
	awaitFenceMarker(t, path+".ready")
	record, err := manager.InspectMerge(context.Background())
	if err != nil || record == nil || record.State != "prepared" {
		t.Fatalf("inspection while writer is alive: %+v %v", record, err)
	}
	if _, err := manager.RecoverMerge(context.Background(), record.AttemptID, true); !errors.Is(err, ErrMergeBusy) {
		t.Fatalf("recovery crossed a live owner: %v", err)
	}
	_ = child.Process.Kill()
	_ = child.Wait()
	preview, err := manager.PreviewMerge(context.Background(), plan.AgentName)
	if err != nil || preview.CanMerge || !strings.Contains(strings.Join(preview.Blockers, ","), "merge_recovery_required") {
		t.Fatalf("preview ignored dead owner's receipt: %+v %v", preview, err)
	}
	if err := manager.MergeBack(context.Background(), plan.AgentName); err == nil {
		t.Fatal("crashed application was silently retried")
	}
	if _, err := manager.RecoverMerge(context.Background(), strings.Repeat("0", 32), true); !errors.Is(err, ErrMergeSnapshotChanged) {
		t.Fatalf("stale operator acknowledgment accepted: %v", err)
	}
	if _, err := manager.RecoverMerge(context.Background(), record.AttemptID, false); err == nil {
		t.Fatal("unchanged HEAD was treated as proof of completed work")
	}
	settled, err := manager.RecoverMerge(context.Background(), record.AttemptID, true)
	if err != nil || settled.State != "abandoned" {
		t.Fatalf("explicit clean-target abandonment: %+v %v", settled, err)
	}
	if got := fenceGit(t, plan.TargetPath, "rev-parse", "HEAD"); got != plan.TargetCommit {
		t.Fatal("abandonment changed Git history")
	}
	if err := manager.MergeBack(context.Background(), plan.AgentName); err != nil {
		t.Fatalf("settled barrier did not permit a new inspected application: %v", err)
	}
}

func TestMergeRecoveryAcknowledgesLostCompletionWithoutRerunningGit(t *testing.T) {
	manager, plan := journalFixture(t)
	record := prepareJournalFixture(t, plan)
	// Model the crash window after Git commits but before NTM saves completion.
	fenceGit(t, plan.TargetPath, "merge", "--no-ff", "-m", "completed before worker died", plan.SourceCommit)
	head := fenceGit(t, plan.TargetPath, "rev-parse", "HEAD")
	preview, err := manager.PreviewMerge(context.Background(), plan.AgentName)
	if err != nil || !preview.AlreadyMerged || preview.CanMerge {
		t.Fatalf("already-merged check bypassed uncertain receipt: %+v %v", preview, err)
	}
	if _, err := manager.RecoverMerge(context.Background(), record.AttemptID, true); err == nil {
		t.Fatal("completed history was abandoned as if nothing happened")
	}
	recovered, err := manager.RecoverMerge(context.Background(), record.AttemptID, false)
	if err != nil || recovered.State != "recovered" || recovered.Commit != head {
		t.Fatalf("could not reconcile exact Git evidence: %+v %v", recovered, err)
	}
	if _, err := manager.RecoverMerge(context.Background(), record.AttemptID, false); err != nil {
		t.Fatalf("recovery is not idempotent: %v", err)
	}
	if got := fenceGit(t, plan.TargetPath, "rev-parse", "HEAD"); got != head {
		t.Fatal("recovery created another merge commit")
	}
}

func TestMergeRecoveryRefusesAmbiguousOrDirtyEvidence(t *testing.T) {
	for _, mode := range []string{"dirty", "pending", "other_commit", "wrong_tree", "other_checkout", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			manager, plan := journalFixture(t)
			record := prepareJournalFixture(t, plan)
			ctx := context.Background()
			abandon := true
			switch mode {
			case "dirty":
				if err := os.WriteFile(filepath.Join(plan.TargetPath, "untracked"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "pending":
				if err := os.WriteFile(filepath.Join(plan.TargetPath, ".git", "MERGE_HEAD"), []byte(plan.SourceCommit+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "other_commit":
				fenceGit(t, plan.TargetPath, "commit", "--allow-empty", "-m", "independent work")
			case "wrong_tree":
				fenceGit(t, plan.TargetPath, "merge", "--strategy=ours", "--no-ff", "-m", "wrong result", plan.SourceCommit)
				abandon = false
			case "other_checkout":
				manager = NewManager(plan.SourcePath, plan.SessionID)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before, _ := os.ReadFile(filepath.Join(plan.TargetPath, ".git", "ntm-merge.json"))
			if _, err := manager.RecoverMerge(ctx, record.AttemptID, abandon); err == nil {
				t.Fatal("ambiguous evidence cleared the recovery barrier")
			}
			after, _ := os.ReadFile(filepath.Join(plan.TargetPath, ".git", "ntm-merge.json"))
			if string(before) != string(after) {
				t.Fatal("failed recovery changed its receipt")
			}
		})
	}
}

func TestMergeRecoveryCorruptionAndFailedFinalSaveRetainBarrier(t *testing.T) {
	t.Run("corrupt", func(t *testing.T) {
		manager, plan := journalFixture(t)
		if err := os.WriteFile(filepath.Join(plan.TargetPath, ".git", "ntm-merge.json"), []byte(`{"version":1,"state":"committed"`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := manager.MergeBack(context.Background(), plan.AgentName); err == nil {
			t.Fatal("corrupt record authorized Git")
		}
		if fenceGit(t, plan.TargetPath, "rev-parse", "HEAD") != plan.TargetCommit {
			t.Fatal("corrupt record still merged")
		}
	})
	t.Run("completion write failed", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("fixture uses a POSIX Git hook")
		}
		manager, plan := journalFixture(t)
		path := filepath.Join(plan.TargetPath, ".git", "ntm-merge.json")
		quote := "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'"
		hook := "#!/bin/sh\nmv " + quote + " " + quote + ".held\nmkdir " + quote + "\n"
		if err := os.WriteFile(filepath.Join(plan.TargetPath, ".git", "hooks", "post-merge"), []byte(hook), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := manager.MergeBack(context.Background(), plan.AgentName); !errors.Is(err, ErrMergeOutcomeUnverified) {
			t.Fatalf("lost completion was acknowledged: %v", err)
		}
		head := fenceGit(t, plan.TargetPath, "rev-parse", "HEAD")
		if head == plan.TargetCommit {
			t.Fatal("fixture never executed Git")
		}
		// Preserve both pieces of evidence; restore only the receipt location.
		if err := os.Rename(path, path+".blocked"); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".held", path); err != nil {
			t.Fatal(err)
		}
		record, err := manager.InspectMerge(context.Background())
		if err != nil || record.State != "prepared" {
			t.Fatalf("lost pre-execution evidence: %+v %v", record, err)
		}
		if _, err := manager.RecoverMerge(context.Background(), record.AttemptID, false); err != nil {
			t.Fatal(err)
		}
		if fenceGit(t, plan.TargetPath, "rev-parse", "HEAD") != head {
			t.Fatal("reconciliation changed history")
		}
	})
}

package worktrees

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func provisionGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func provisionRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	provisionGit(t, repo, "init", "-b", "main")
	provisionGit(t, repo, "config", "user.name", "Provisioning Test")
	provisionGit(t, repo, "config", "user.email", "provisioning@example.invalid")
	provisionGit(t, repo, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "initial")
	return repo
}

func provisionExisting(t *testing.T, repo string) (*WorktreeManager, *WorktreeInfo) {
	t.Helper()
	m := NewManager(repo, "provision-test")
	info, err := m.CreateForAgent(context.Background(), "existing")
	if err != nil || info == nil || !info.Created {
		t.Fatalf("create existing checkout: %+v %v", info, err)
	}
	return m, info
}

func provisionWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func provisionAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected provisioning side effect at %s: %v", path, err)
	}
}

func provisionAwait(t *testing.T, path string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("provisioning subprocess did not reach %s", path)
		}
	}
}

func provisionWrapper(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("process fault injection needs a POSIX shell")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nreal_git='" + strings.ReplaceAll(git, "'", "'\"'\"'") + "'\n" + body + "\nexec \"$real_git\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestProvisioningRefusesActiveRepositoryOwner(t *testing.T) {
	m, existing := provisionExisting(t, provisionRepo(t))
	fence, err := acquireMergeFence(context.Background(), m.projectPath)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.close()
	for _, name := range []string{"new-agent", existing.AgentName} {
		info, err := m.CreateForAgent(context.Background(), name)
		if !errors.Is(err, ErrMergeBusy) || info == nil || info.Created || info.Error == "" {
			t.Fatalf("provisioning bypassed repository owner: %+v %v", info, err)
		}
	}
	provisionAbsent(t, filepath.Join(m.sessionRoot(), "new-agent"))
	other := NewManager(m.projectPath, "other-session")
	if _, err := other.CreateForAgent(context.Background(), "agent"); !errors.Is(err, ErrMergeBusy) {
		t.Fatalf("different session bypassed repository owner: %v", err)
	}
	provisionAbsent(t, other.sessionRoot())
	independent := NewManager(provisionRepo(t), "other-session")
	if _, err := independent.CreateForAgent(context.Background(), "agent"); err != nil {
		t.Fatalf("one repository's owner blocked an independent repository: %v", err)
	}
}

func TestProvisioningHonorsMergeRecoveryBeforeCreationAndReuse(t *testing.T) {
	for _, state := range []string{"prepared", "corrupt", "unsupported", "abandoned", "committed", "recovered"} {
		t.Run(state, func(t *testing.T) {
			m, existing := provisionExisting(t, provisionRepo(t))
			marker := filepath.Join(existing.Path, "unfinished-agent-work")
			provisionWrite(t, marker, "retain this work")
			common, err := mergeCommonDirectory(context.Background(), m.projectPath)
			if err != nil {
				t.Fatal(err)
			}
			head := provisionGit(t, m.projectPath, "rev-parse", "HEAD")
			now := time.Now().UTC()
			record := MergeRecovery{Version: 1, AttemptID: strings.Repeat("a", 32), State: state, StartedAt: now, UpdatedAt: now,
				Plan: MergePreview{SourcePath: existing.Path, TargetPath: m.projectPath, SourceBranch: existing.BranchName,
					TargetBranch: "main", SourceCommit: head, TargetCommit: head, MergeTree: provisionGit(t, m.projectPath, "rev-parse", "HEAD^{tree}")}}
			if state == "committed" || state == "recovered" {
				record.Commit = head
			}
			if state == "unsupported" {
				record.Version = 2
			}
			raw, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if state == "corrupt" {
				raw = []byte("{incomplete")
			}
			journal := filepath.Join(common, "ntm-merge.json")
			provisionWrite(t, journal, string(raw))
			blocked := state == "prepared" || state == "corrupt" || state == "unsupported"
			for _, name := range []string{"new-agent", existing.AgentName} {
				info, err := m.CreateForAgent(context.Background(), name)
				if blocked {
					if !errors.Is(err, ErrMergeRecoveryRequired) || info == nil || info.Created || info.Error == "" {
						t.Fatalf("recovery barrier bypassed: %+v %v", info, err)
					}
				} else if err != nil || info.Created != (name == "new-agent") {
					t.Fatalf("settled recovery blocked normal provisioning: %+v %v", info, err)
				}
			}
			if blocked {
				provisionAbsent(t, filepath.Join(m.sessionRoot(), "new-agent"))
			}
			got, err := os.ReadFile(journal)
			if err != nil || !bytes.Equal(got, raw) {
				t.Fatalf("provisioning changed recovery evidence: %q %v", got, err)
			}
			got, err = os.ReadFile(marker)
			if err != nil || string(got) != "retain this work" {
				t.Fatalf("provisioning changed retained agent work: %q %v", got, err)
			}
		})
	}
}

// Invoked only in a child test process. The real CreateForAgent path owns the
// lock; a real-Git wrapper pauses that path at a chosen mutation boundary.
func TestProvisioningProcessHelper(t *testing.T) {
	if os.Getenv("NTM_PROVISION_HELPER") != "create" {
		return
	}
	m := NewManager(os.Getenv("NTM_PROVISION_REPO"), "provision-test")
	info, err := m.CreateForAgent(context.Background(), "new-agent")
	if err != nil || info == nil || !info.Created {
		t.Fatalf("child provisioning: %+v %v", info, err)
	}
}

func TestProvisioningOwnsFenceAcrossGitExecution(t *testing.T) {
	for _, afterMutation := range []bool{false, true} {
		t.Run(fmt.Sprintf("after_mutation_%t", afterMutation), func(t *testing.T) {
			m, existing := provisionExisting(t, provisionRepo(t))
			marker := filepath.Join(existing.Path, "unfinished-agent-work")
			provisionWrite(t, marker, "retain this work")
			barrier := filepath.Join(t.TempDir(), "started")
			release := filepath.Join(t.TempDir(), "release")
			t.Setenv("NTM_PROVISION_STARTED", barrier)
			t.Setenv("NTM_PROVISION_RELEASE", release)
			body := ""
			if afterMutation {
				body = `"$real_git" "$@" || exit $?`
			}
			provisionWrapper(t, `if [ "$1 $2" = "worktree add" ]; then
`+body+`
  : > "$NTM_PROVISION_STARTED"
  while [ ! -e "$NTM_PROVISION_RELEASE" ]; do sleep 0.01; done
`+func() string {
				if afterMutation {
					return "exit 0"
				}
				return ""
			}()+`
fi`)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProvisioningProcessHelper$")
			cmd.Env = append(os.Environ(), "NTM_PROVISION_HELPER=create", "NTM_PROVISION_REPO="+m.projectPath)
			var log bytes.Buffer
			cmd.Stdout, cmd.Stderr = &log, &log
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			joined := false
			defer func() {
				// Release the wrapper even on an assertion failure so the fixture
				// cannot leave an orphan waiting for an absent marker.
				_ = os.WriteFile(release, []byte("released"), 0o600)
				if !joined {
					_ = cmd.Wait()
				}
			}()
			provisionAwait(t, barrier)
			if err := m.PreflightForAgent(context.Background(), "preflight-agent"); !errors.Is(err, ErrMergeBusy) {
				t.Fatalf("spawn preflight ignored a live provisioning process: %v", err)
			}
			if err := m.RemoveWorktree(context.Background(), existing.AgentName); !errors.Is(err, ErrMergeBusy) {
				t.Fatalf("removal overlapped provisioning: %v", err)
			}
			if err := m.Cleanup(context.Background()); !errors.Is(err, ErrMergeBusy) {
				t.Fatalf("cleanup overlapped provisioning: %v", err)
			}
			other := NewManager(m.projectPath, "other-session")
			if _, err := other.CreateForAgent(context.Background(), "competing"); !errors.Is(err, ErrMergeBusy) {
				t.Fatalf("parallel provisioning bypassed common lock: %v", err)
			}
			got, err := os.ReadFile(marker)
			if err != nil || string(got) != "retain this work" {
				t.Fatalf("competing cleanup lost work: %q %v", got, err)
			}
			provisionWrite(t, release, "released")
			err = cmd.Wait()
			joined = true
			if err != nil {
				t.Fatalf("provisioning child failed: %v\n%s", err, &log)
			}
			if _, err := m.CreateForAgent(context.Background(), "new-agent"); err != nil {
				t.Fatalf("successful provisioning retained its lock: %v", err)
			}
		})
	}
}

func TestProvisioningCancellationPreservesMutationAndReleasesFence(t *testing.T) {
	for _, afterMutation := range []bool{false, true} {
		t.Run(fmt.Sprintf("after_mutation_%t", afterMutation), func(t *testing.T) {
			m := NewManager(provisionRepo(t), "provision-test")
			barrier := filepath.Join(t.TempDir(), "started")
			t.Setenv("NTM_PROVISION_STARTED", barrier)
			body := ""
			if afterMutation {
				body = `"$real_git" "$@" || exit $?`
			}
			provisionWrapper(t, `if [ "$1 $2" = "worktree add" ]; then
`+body+`
  : > "$NTM_PROVISION_STARTED"
  exec sleep 30
fi`)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				info *WorktreeInfo
				err  error
			}
			done := make(chan result, 1)
			go func() {
				info, err := m.CreateForAgent(ctx, "new-agent")
				done <- result{info, err}
			}()
			provisionAwait(t, barrier)
			cancel()
			select {
			case r := <-done:
				if !errors.Is(r.err, context.Canceled) || r.info == nil || r.info.Created != afterMutation || r.info.Error == "" {
					t.Fatalf("incorrect cancellation evidence: %+v %v", r.info, r.err)
				}
				if afterMutation {
					provisionGit(t, r.info.Path, "show-ref", "--verify", "refs/heads/"+r.info.BranchName)
				} else {
					provisionAbsent(t, r.info.Path)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("canceled provisioning did not join")
			}
			fence, err := acquireMergeFence(context.Background(), m.projectPath)
			if err != nil {
				t.Fatalf("cancellation leaked repository ownership: %v", err)
			}
			fence.close()
		})
	}
}

func TestProvisioningRejectsRedirectedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture requires local symlink privileges")
	}
	for _, relative := range []string{".ntm", ".ntm/worktrees", ".ntm/worktrees/provision-test", ".ntm/worktrees/provision-test/new-agent"} {
		t.Run(relative, func(t *testing.T) {
			m := NewManager(provisionRepo(t), "provision-test")
			outside := t.TempDir()
			provisionWrite(t, filepath.Join(outside, "valuable"), "not an agent checkout")
			link := filepath.Join(m.projectPath, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			info, err := m.CreateForAgent(context.Background(), "new-agent")
			if err == nil || info == nil || info.Created || info.Error == "" {
				t.Fatalf("provisioning followed a substituted directory: %+v %v", info, err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 1 || entries[0].Name() != "valuable" {
				t.Fatalf("provisioning wrote into redirected directory: %v %v", entries, err)
			}
		})
	}
}

func TestProvisioningRejectsForeignGitIdentityOnReuse(t *testing.T) {
	m, info := provisionExisting(t, provisionRepo(t))
	_, foreign := provisionExisting(t, provisionRepo(t))
	foreignGit, err := os.ReadFile(filepath.Join(foreign.Path, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	// The main repo still lists the original path; the same branch name in
	// another repo is not sufficient evidence that it is safe to hand out.
	provisionWrite(t, filepath.Join(info.Path, ".git"), string(foreignGit))
	reused, err := m.CreateForAgent(context.Background(), info.AgentName)
	if err == nil || reused == nil || reused.Created || reused.Error == "" {
		t.Fatalf("foreign checkout handed to agent as existing work: %+v %v", reused, err)
	}
}

func TestProvisioningVerifiesCreatedRepositoryIdentity(t *testing.T) {
	m := NewManager(provisionRepo(t), "provision-test")
	foreignManager := NewManager(provisionRepo(t), "provision-test")
	foreign, err := foreignManager.CreateForAgent(context.Background(), "new-agent")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_PROVISION_FOREIGN_GIT", filepath.Join(foreign.Path, ".git"))
	provisionWrapper(t, `if [ "$1 $2" = "worktree add" ]; then
  "$real_git" "$@" || exit $?
  cp "$NTM_PROVISION_FOREIGN_GIT" "$5/.git" || exit $?
  exit 0
fi`)
	info, err := m.CreateForAgent(context.Background(), "new-agent")
	if err == nil || info == nil || !info.Created || info.Error == "" {
		t.Fatalf("false-success provisioning hid foreign identity or partial creation: %+v %v", info, err)
	}
	provisionGit(t, m.projectPath, "show-ref", "--verify", "refs/heads/"+info.BranchName)
	if _, err := os.Stat(info.Path); err != nil {
		t.Fatalf("failed postcondition deleted retained checkout: %v", err)
	}
}

func TestProvisioningAllowsProjectDirectoryAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture requires local symlink privileges")
	}
	repo := provisionRepo(t)
	alias := filepath.Join(t.TempDir(), "project-alias")
	if err := os.Symlink(repo, alias); err != nil {
		t.Fatal(err)
	}
	m := NewManager(alias, "provision-test")
	for i := 0; i < 2; i++ {
		info, err := m.CreateForAgent(context.Background(), "agent")
		if err != nil || info.Created != (i == 0) {
			t.Fatalf("valid project alias failed create/reuse: %+v %v", info, err)
		}
	}
}

func TestProvisioningRelativeScopeAndIdempotentReuse(t *testing.T) {
	repo := provisionRepo(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Dir(repo)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatal(err)
		}
	}()
	m := NewManager("repo", "provision-test")
	for i := 0; i < 2; i++ {
		info, err := m.CreateForAgent(context.Background(), "agent")
		if err != nil || info.Created != (i == 0) || info.Path != filepath.Join(repo, ".ntm", "worktrees", "provision-test", "agent") {
			t.Fatalf("relative creation/reuse %d: %+v %v", i, info, err)
		}
	}
	if m.projectPath != "repo" {
		t.Fatal("provisioning mutated reusable manager scope")
	}
	provisionAbsent(t, filepath.Join(repo, "repo"))
}

func TestProvisioningRejectsMissingScopeAndStoppedContext(t *testing.T) {
	for _, m := range []*WorktreeManager{nil, NewManager("", "session"), NewManager("   ", "session")} {
		info, err := m.CreateForAgent(context.Background(), "agent")
		if err == nil || info == nil || info.Created || info.Error == "" {
			t.Fatalf("missing scope fell back to the shell's project: %+v %v", info, err)
		}
	}
	m := NewManager(provisionRepo(t), "provision-test")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, stopped := range []context.Context{nil, ctx} {
		info, err := m.CreateForAgent(stopped, "agent")
		if err == nil || info == nil || info.Created || info.Error == "" {
			t.Fatalf("stopped provisioning created state: %+v %v", info, err)
		}
	}
	provisionAbsent(t, filepath.Join(m.projectPath, ".ntm"))
	provisionAbsent(t, filepath.Join(m.projectPath, ".git", "ntm-merge.lock"))
}

func TestProvisioningPreflightRecoveryBarrier(t *testing.T) {
	for _, state := range []string{"prepared", "corrupt", "unsupported", "abandoned", "committed", "recovered"} {
		t.Run(state, func(t *testing.T) {
			m := NewManager(provisionRepo(t), "preflight-test")
			info, err := m.buildWorktreeInfo("agent")
			if err != nil {
				t.Fatal(err)
			}
			head := provisionGit(t, m.projectPath, "rev-parse", "HEAD")
			now := time.Now().UTC()
			record := MergeRecovery{Version: 1, AttemptID: strings.Repeat("b", 32), State: state, StartedAt: now, UpdatedAt: now,
				Plan: MergePreview{SourcePath: info.Path, TargetPath: m.projectPath, SourceBranch: info.BranchName,
					TargetBranch: "main", SourceCommit: head, TargetCommit: head, MergeTree: provisionGit(t, m.projectPath, "rev-parse", "HEAD^{tree}")}}
			if state == "committed" || state == "recovered" {
				record.Commit = head
			}
			if state == "unsupported" {
				record.Version = 2
			}
			raw, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if state == "corrupt" {
				raw = []byte("{incomplete")
			}
			journal := filepath.Join(m.projectPath, ".git", "ntm-merge.json")
			provisionWrite(t, journal, string(raw))
			err = m.PreflightForAgent(context.Background(), "agent")
			blocked := state == "prepared" || state == "corrupt" || state == "unsupported"
			if blocked && !errors.Is(err, ErrMergeRecoveryRequired) {
				t.Fatalf("preflight bypassed recovery evidence: %v", err)
			}
			if !blocked && err != nil {
				t.Fatalf("settled receipt refused preflight: %v", err)
			}
			provisionAbsent(t, filepath.Join(m.projectPath, ".ntm"))
			provisionAbsent(t, filepath.Join(m.projectPath, ".git", "ntm-merge.lock"))
			got, err := os.ReadFile(journal)
			if err != nil || !bytes.Equal(got, raw) {
				t.Fatalf("preflight rewrote recovery evidence: %q %v", got, err)
			}
		})
	}
}

func TestProvisioningPreflightExistingIdentity(t *testing.T) {
	for _, substituted := range []bool{false, true} {
		t.Run(fmt.Sprintf("foreign_%t", substituted), func(t *testing.T) {
			m, info := provisionExisting(t, provisionRepo(t))
			gitFile := filepath.Join(info.Path, ".git")
			if substituted {
				_, foreign := provisionExisting(t, provisionRepo(t))
				raw, err := os.ReadFile(filepath.Join(foreign.Path, ".git"))
				if err != nil {
					t.Fatal(err)
				}
				provisionWrite(t, gitFile, string(raw))
			}
			before, err := os.ReadFile(gitFile)
			if err != nil {
				t.Fatal(err)
			}
			err = m.PreflightForAgent(context.Background(), info.AgentName)
			if substituted && err == nil {
				t.Fatal("preflight accepted a matching branch name from a foreign repository")
			}
			if !substituted && err != nil {
				t.Fatalf("preflight rejected verified existing checkout: %v", err)
			}
			after, readErr := os.ReadFile(gitFile)
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatalf("preflight changed the inspected checkout: %v", readErr)
			}
		})
	}
}

func TestProvisioningPreflightRejectsRedirectedParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture requires local symlink privileges")
	}
	m := NewManager(provisionRepo(t), "preflight-test")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(m.projectPath, ".ntm")); err != nil {
		t.Fatal(err)
	}
	if err := m.PreflightForAgent(context.Background(), "agent"); err == nil {
		t.Fatal("preflight approved creation through a substituted ancestor")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("preflight wrote to a redirected directory: %v %v", entries, err)
	}
	provisionAbsent(t, filepath.Join(m.projectPath, ".git", "ntm-merge.lock"))
}

func TestProvisioningPreflightDetectsDetachedOrphanRegistration(t *testing.T) {
	m := NewManager(provisionRepo(t), "preflight-test")
	info, err := m.buildWorktreeInfo("agent")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(info.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	provisionGit(t, m.projectPath, "worktree", "add", "--detach", info.Path)
	retained := filepath.Join(t.TempDir(), "retained-checkout")
	if err := os.Rename(info.Path, retained); err != nil {
		t.Fatal(err)
	}
	before := provisionGit(t, m.projectPath, "worktree", "list", "--porcelain", "-z")
	if err := m.PreflightForAgent(context.Background(), info.AgentName); err == nil {
		t.Fatal("preflight ignored a registered path because the agent branch was absent")
	}
	if after := provisionGit(t, m.projectPath, "worktree", "list", "--porcelain", "-z"); after != before {
		t.Fatal("preflight pruned or rewrote the detached registration")
	}
	if _, err := os.Stat(filepath.Join(retained, ".git")); err != nil {
		t.Fatalf("preflight lost the retained checkout: %v", err)
	}
}

func TestProvisioningPreflightIsReadOnlySnapshot(t *testing.T) {
	repo := provisionRepo(t)
	provisionWrite(t, filepath.Join(repo, "tracked"), "unchanged")
	provisionGit(t, repo, "add", "tracked")
	provisionGit(t, repo, "-c", "commit.gpgsign=false", "commit", "-m", "tracked fixture")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(cwd, repo)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(rel, "preflight-test")
	index := filepath.Join(repo, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	head := provisionGit(t, repo, "rev-parse", "HEAD")
	if err := m.PreflightForAgent(context.Background(), "agent"); err != nil {
		t.Fatal(err)
	}
	if m.projectPath != rel {
		t.Fatal("preflight mutated a reusable manager")
	}
	after, err := os.ReadFile(index)
	if err != nil || !bytes.Equal(before, after) || provisionGit(t, repo, "rev-parse", "HEAD") != head {
		t.Fatal("preflight changed index bytes or HEAD")
	}
	provisionAbsent(t, filepath.Join(repo, ".ntm"))
	provisionAbsent(t, filepath.Join(repo, ".git", "ntm-merge.lock"))
	fence, err := acquireMergeFence(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.PreflightForAgent(context.Background(), "agent"); !errors.Is(err, ErrMergeBusy) {
		fence.close()
		t.Fatalf("preflight ignored a newly acquired owner: %v", err)
	}
	if _, err := m.CreateForAgent(context.Background(), "agent"); !errors.Is(err, ErrMergeBusy) {
		fence.close()
		t.Fatalf("old preflight authorized provisioning under a new owner: %v", err)
	}
	fence.close()
	if err := m.PreflightForAgent(context.Background(), "agent"); err != nil {
		t.Fatalf("released ownership still blocks preflight: %v", err)
	}
	provisionAbsent(t, filepath.Join(repo, ".ntm"))
	if _, err := m.CreateForAgent(context.Background(), "agent"); err != nil {
		t.Fatalf("successful preflight retained its probe lock: %v", err)
	}
}

func TestProvisioningPreflightRejectsInvalidEvidence(t *testing.T) {
	for _, mode := range []string{"lock directory", "bad listing"} {
		t.Run(mode, func(t *testing.T) {
			m := NewManager(provisionRepo(t), "preflight-test")
			if mode == "lock directory" {
				if err := os.Mkdir(filepath.Join(m.projectPath, ".git", "ntm-merge.lock"), 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				provisionWrapper(t, `if [ "$1 $2" = "worktree list" ]; then printf 'invalid listing'; exit 0; fi`)
			}
			if err := m.PreflightForAgent(context.Background(), "agent"); err == nil {
				t.Fatalf("preflight treated %s as permission to proceed", mode)
			}
			provisionAbsent(t, filepath.Join(m.projectPath, ".ntm"))
		})
	}
}

func TestProvisioningPreflightRejectsMissingScopeAndCancellation(t *testing.T) {
	for _, m := range []*WorktreeManager{nil, NewManager("", "session"), NewManager("   ", "session")} {
		if err := m.PreflightForAgent(context.Background(), "agent"); err == nil {
			t.Fatal("preflight defaulted an absent scope to the shell's project")
		}
	}
	m := NewManager(provisionRepo(t), "preflight-test")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.PreflightForAgent(ctx, "agent"); !errors.Is(err, context.Canceled) {
		t.Fatalf("preflight lost owner cancellation: %v", err)
	}
	if err := m.PreflightForAgent(nil, "agent"); err == nil {
		t.Fatal("preflight accepted nil context")
	}
	provisionWrapper(t, "exec sleep 30")
	ctx, cancel = context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := m.PreflightForAgent(ctx, "agent"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("preflight did not stop with the blocked Git probe: %v", err)
	}
	provisionAbsent(t, filepath.Join(m.projectPath, ".ntm"))
	provisionAbsent(t, filepath.Join(m.projectPath, ".git", "ntm-merge.lock"))
}

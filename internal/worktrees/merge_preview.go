package worktrees

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/git"
)

// ErrMergeSnapshotChanged means the named branches no longer identify the
// commits inspected by the preview. Re-plan; never silently merge newer work.
var ErrMergeSnapshotChanged = errors.New("worktree merge snapshot changed")

// MergePreview describes a merge of committed work, not unstaged agent edits.
// Git may write unreachable objects while computing MergeTree, but preview
// never checks out a branch, changes an index, advances a ref, or starts a merge.
// CanMerge is a point-in-time assessment, not a lock on other Git writers.
type MergePreview struct {
	AgentName      string   `json:"agent_name"`
	SessionID      string   `json:"session_id"`
	SourcePath     string   `json:"source_path"`
	TargetPath     string   `json:"target_path"`
	SourceBranch   string   `json:"source_branch"`
	TargetBranch   string   `json:"target_branch"`
	SourceCommit   string   `json:"source_commit"`
	TargetCommit   string   `json:"target_commit"`
	MergeTree      string   `json:"merge_tree"`
	SourceCommits  int      `json:"source_commits"`
	AlreadyMerged  bool     `json:"already_merged"`
	HasConflicts   bool     `json:"has_conflicts"`
	Conflicts      []string `json:"conflicts"`
	OverwriteRisks []string `json:"overwrite_risks"`
	Blockers       []string `json:"blockers"`
	CanMerge       bool     `json:"can_merge"`
}

// PreviewMerge evaluates Git's real three-way merge without touching either
// checkout. Missing/unsupported Git functionality is an error, never a clean
// merge. A completed inspection may return CanMerge=false with actionable
// blockers (dirty checkout, wrong target branch, pending operation, conflicts).
func (m *WorktreeManager) PreviewMerge(ctx context.Context, agentName string) (*MergePreview, error) {
	if ctx == nil {
		return nil, errors.New("merge preview requires a context")
	}
	if m == nil || strings.TrimSpace(m.projectPath) == "" {
		return nil, errors.New("merge preview requires a project directory")
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	project, err := filepath.Abs(m.projectPath)
	if err != nil {
		return nil, fmt.Errorf("resolve merge project: %w", err)
	}
	// Resolve relative scope once; never mutate a reusable manager.
	m = NewManager(project, m.session)
	info, err := m.GetWorktreeForAgent(ctx, agentName)
	if err != nil {
		return nil, fmt.Errorf("inspect source worktree: %w", err)
	}
	if !info.Created || info.Error != "" {
		return nil, fmt.Errorf("cannot preview agent %s: %s", info.AgentName, info.Error)
	}
	if err := verifyMergeRepository(ctx, m.projectPath, info.Path); err != nil {
		return nil, err
	}
	target, err := git.DefaultBranch(ctx, m.projectPath)
	if err != nil {
		return nil, fmt.Errorf("determine merge target: %w", err)
	}
	if target == info.BranchName {
		return nil, errors.New("source worktree branch is also the resolved merge target")
	}
	plan := &MergePreview{
		AgentName: info.AgentName, SessionID: info.SessionID,
		SourcePath: info.Path, TargetPath: m.projectPath,
		SourceBranch: info.BranchName, TargetBranch: target,
		Conflicts: []string{}, Blockers: []string{}, OverwriteRisks: []string{},
	}
	plan.SourceCommit, err = mergeCommitAt(ctx, m.projectPath, "refs/heads/"+info.BranchName)
	if err != nil {
		return nil, fmt.Errorf("resolve source commit: %w", err)
	}
	plan.TargetCommit, err = mergeCommitAt(ctx, m.projectPath, "refs/heads/"+target)
	if err != nil {
		return nil, fmt.Errorf("resolve target commit: %w", err)
	}
	count, err := gitOutput(ctx, m.projectPath, "rev-list", "--count", plan.TargetCommit+".."+plan.SourceCommit)
	if err != nil {
		return nil, fmt.Errorf("count unmerged source commits: %w", err)
	}
	plan.SourceCommits, err = strconv.Atoi(strings.TrimSpace(string(count)))
	if err != nil || plan.SourceCommits < 0 {
		return nil, errors.New("git returned an invalid unmerged commit count")
	}
	plan.AlreadyMerged = plan.SourceCommits == 0

	// Keep stdout separate: stderr diagnostics must not become conflict paths.
	cmd := exec.CommandContext(ctx, "git", "merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", plan.TargetCommit, plan.SourceCommit)
	cmd.Dir = m.projectPath
	cmd.WaitDelay = time.Second
	raw, mergeErr := cmd.Output()
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("merge preview stopped: %w", err)
	}
	if mergeErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(mergeErr, &exitErr) || exitErr.ExitCode() != 1 {
			return nil, fmt.Errorf("git merge-tree could not evaluate merge (Git 2.38+ required): %w", mergeErr)
		}
		// Some directory-rename conflicts have no conflicted file entries.
		// The exit status, not the number of paths, determines conflict state.
		plan.HasConflicts = true
	}
	plan.MergeTree, plan.Conflicts, err = parseMergeTreePreview(raw)
	if err != nil {
		return nil, err
	}
	if !plan.HasConflicts && len(plan.Conflicts) != 0 {
		return nil, errors.New("git merge-tree reported conflicting paths with a successful exit")
	}
	if plan.HasConflicts {
		plan.Blockers = append(plan.Blockers, "merge_conflicts")
	}
	for _, checkout := range []struct{ path, branch, role string }{
		{info.Path, info.BranchName, "source"}, {m.projectPath, target, "target"},
	} {
		blockers, err := mergeCheckoutBlockers(ctx, checkout.path, checkout.branch, checkout.role)
		if err != nil {
			return nil, err
		}
		plan.Blockers = append(plan.Blockers, blockers...)
	}
	plan.OverwriteRisks, err = mergeIgnoredOverwriteRisks(ctx, plan)
	if err != nil {
		return nil, err
	}
	if len(plan.OverwriteRisks) > 0 {
		plan.Blockers = append(plan.Blockers, "target_ignored_path_collision")
	}
	if err := verifyMergeSnapshot(ctx, plan); err != nil {
		return nil, err
	}
	plan.CanMerge = len(plan.Blockers) == 0
	return plan, nil
}

func mergeCommitAt(ctx context.Context, dir, ref string) (string, error) {
	out, err := gitOutput(ctx, dir, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	oid := strings.TrimSpace(string(out))
	if !isMergeObjectID(oid) {
		return "", errors.New("git returned an invalid commit identity")
	}
	return oid, nil
}

func isMergeObjectID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	for _, c := range oid {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// --no-messages --name-only -z returns a tree ID followed by NUL-delimited
// conflict paths. Preserve filenames byte-for-byte, including tabs/newlines.
func parseMergeTreePreview(raw []byte) (string, []string, error) {
	parts := bytes.Split(raw, []byte{0})
	if len(parts) < 2 || len(parts[len(parts)-1]) != 0 || !isMergeObjectID(string(parts[0])) {
		return "", nil, errors.New("malformed git merge-tree preview output")
	}
	paths := make([]string, 0, len(parts)-2)
	for _, part := range parts[1 : len(parts)-1] {
		if len(part) == 0 {
			return "", nil, errors.New("unexpected empty conflict path in git merge-tree output")
		}
		paths = append(paths, string(part))
	}
	return string(parts[0]), paths, nil
}

func mergeCheckoutBlockers(ctx context.Context, dir, expectedBranch, role string) ([]string, error) {
	blockers := []string{}
	branch, err := worktreeCurrentBranch(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("inspect %s checkout branch: %w", role, err)
	}
	if branch != expectedBranch {
		blockers = append(blockers, role+"_branch_not_checked_out")
	}
	// Read status without opportunistically refreshing/writing the index.
	out, err := gitOutput(ctx, dir, "--no-optional-locks", "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return nil, fmt.Errorf("inspect %s checkout changes: %w", role, err)
	}
	if len(out) != 0 {
		blockers = append(blockers, role+"_has_uncommitted_changes")
	}
	gitDir, err := gitOutput(ctx, dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, fmt.Errorf("inspect %s operation state: %w", role, err)
	}
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer", "BISECT_START"} {
		_, err := os.Lstat(filepath.Join(strings.TrimSpace(string(gitDir)), name))
		if err == nil {
			blockers = append(blockers, role+"_operation_in_progress:"+name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect %s %s: %w", role, name, err)
		}
	}
	return blockers, nil
}

// Git's ort merge can replace ignored files even with --no-overwrite-ignore.
// Compare paths the planned tree will write with ignored, untracked files
// before mutation. Include ancestor/descendant collisions for file/directory
// transitions, without blocking unrelated ignored caches or worktree storage.
func mergeIgnoredOverwriteRisks(ctx context.Context, plan *MergePreview) ([]string, error) {
	raw, err := gitOutput(ctx, plan.TargetPath, "diff-tree", "-r", "--no-commit-id", "--name-only", "--no-renames", "--diff-filter=ACMT", "-z", plan.TargetCommit, plan.MergeTree)
	if err != nil {
		return nil, fmt.Errorf("inspect merge write paths: %w", err)
	}
	writes, err := mergeNULPaths(raw)
	if err != nil {
		return nil, err
	}
	if len(writes) == 0 {
		return []string{}, nil
	}
	raw, err = gitOutput(ctx, plan.TargetPath, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("inspect ignored target files: %w", err)
	}
	ignored, err := mergeNULPaths(raw)
	if err != nil {
		return nil, err
	}
	if len(ignored) == 0 {
		return []string{}, nil
	}
	ignoreCase := false
	raw, err = gitOutput(ctx, plan.TargetPath, "config", "--type=bool", "--get", "core.ignorecase")
	if err == nil {
		ignoreCase = strings.TrimSpace(string(raw)) == "true"
	} else {
		var exitErr *exec.ExitError
		if ctx.Err() != nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return nil, fmt.Errorf("inspect Git path case policy: %w", err)
		}
	}
	keyFor := func(path string) string {
		path = strings.TrimSuffix(path, "/") // opaque nested repository entries
		if ignoreCase {
			path = strings.ToLower(path)
		}
		return path
	}
	byKey := make(map[string][]string, len(ignored))
	for _, path := range ignored {
		key := keyFor(path)
		byKey[key] = append(byKey[key], path)
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	risks := make(map[string]bool)
	for _, path := range writes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := keyFor(path)
		for parent := key; parent != ""; {
			for _, original := range byKey[parent] {
				risks[original] = true
			}
			cut := strings.LastIndexByte(parent, '/')
			if cut < 0 {
				break
			}
			parent = parent[:cut]
		}
		prefix := key + "/"
		for i := sort.SearchStrings(keys, prefix); i < len(keys) && strings.HasPrefix(keys[i], prefix); i++ {
			for _, original := range byKey[keys[i]] {
				risks[original] = true
			}
		}
	}
	result := make([]string, 0, len(risks))
	for path := range risks {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

func mergeNULPaths(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return []string{}, nil
	}
	parts := strings.Split(string(raw), "\x00")
	if parts[len(parts)-1] != "" {
		return nil, errors.New("Git path inspection returned an unterminated path")
	}
	for _, path := range parts[:len(parts)-1] {
		if path == "" {
			return nil, errors.New("Git path inspection returned an empty path")
		}
	}
	return parts[:len(parts)-1], nil
}

func verifyMergeSnapshot(ctx context.Context, plan *MergePreview) error {
	for _, item := range []struct{ branch, want string }{
		{plan.SourceBranch, plan.SourceCommit}, {plan.TargetBranch, plan.TargetCommit},
	} {
		got, err := mergeCommitAt(ctx, plan.TargetPath, "refs/heads/"+item.branch)
		if err != nil {
			return errors.Join(ErrMergeSnapshotChanged, err)
		}
		if got != item.want {
			return fmt.Errorf("%w: branch %s no longer identifies %s", ErrMergeSnapshotChanged, item.branch, item.want)
		}
	}
	return nil
}

func verifyMergeRepository(ctx context.Context, target, source string) error {
	var common string
	for _, dir := range []string{target, source} {
		out, err := gitOutput(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return fmt.Errorf("inspect merge repository identity: %w", err)
		}
		path := strings.TrimSpace(string(out))
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
			return errors.New("merge repository identity is not an absolute directory")
		}
		path = canonicalisePath(path)
		if common != "" && path != common {
			return errors.New("source worktree belongs to a different repository")
		}
		common = path
	}
	return nil
}

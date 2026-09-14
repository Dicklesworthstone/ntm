package tracker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Git-backed file change capture.
//
// The original capture pipeline (SnapshotDirectory + RecordFileChanges) walked
// the entire project tree twice per dispatch and slept in a goroutine to do it;
// it was deleted in 31fc0f5f, which left every reader of GlobalFileChanges —
// `ntm changes`, `ntm conflicts`, --robot-status, the dashboard Files panel and
// the work-coordination adapter — permanently empty while still reporting a
// clean result.
//
// This replaces it with sampling that costs a `git status` plus one stat per
// dirty file: git narrows the candidate set (honoring .gitignore) and the stats
// distinguish a file edited twice from one that merely stayed dirty.

// gitStatusTimeout bounds a single `git status` invocation. Sampling is
// best-effort telemetry and must never wedge the monitor that drives it.
const gitStatusTimeout = 20 * time.Second

// GitRecorder observes one git working tree and records what changed since its
// previous observation, attributed to a single identity.
//
// Attribution is only ever as precise as the tree allows, and is never invented:
// a per-agent worktree yields that agent's name, while a shared checkout yields
// the session, because git cannot say which of several agents in one working
// tree wrote a file. That distinction matters — DetectConflicts treats three
// distinct agents on one path as a critical conflict, so attributing a shared
// tree's edits to every agent in the session would manufacture critical
// conflicts out of one agent editing one file twice.
type GitRecorder struct {
	root       string
	projectDir string
	session    string
	identity   string
	store      *FileChangeStore

	prev    map[string]FileState
	sampled bool
}

// GitRecorderConfig describes one attribution unit.
type GitRecorderConfig struct {
	// Root is the git working tree to sample. For an isolated agent this is
	// that agent's worktree, not the project checkout.
	Root string
	// ProjectDir keys the durable ledger. It is the project the changes belong
	// to rather than the sampled tree, so every agent's worktree reports into
	// the one project a reader will later ask about. Defaults to Root.
	ProjectDir string
	// Session and Identity are the attribution recorded with each change.
	Session  string
	Identity string
	// Store is the in-process ring to also write to. Defaults to
	// GlobalFileChanges.
	Store *FileChangeStore
}

// NewGitRecorder returns a recorder for the working tree described by cfg.
func NewGitRecorder(cfg GitRecorderConfig) *GitRecorder {
	store := cfg.Store
	if store == nil {
		store = GlobalFileChanges
	}
	projectDir := cfg.ProjectDir
	if projectDir == "" {
		projectDir = cfg.Root
	}
	return &GitRecorder{
		root: cfg.Root,
		// Resolved the same way a reader resolves its own location, not merely
		// normalized. A reader keys the ledger by its git toplevel, so a
		// manifest whose project dir is a subdirectory of the repository would
		// be written under one key and read under another, and every surface
		// would show nothing while recording worked perfectly.
		projectDir: resolveProjectDir(projectDir),
		session:    cfg.Session,
		identity:   cfg.Identity,
		store:      store,
	}
}

// Sample takes one observation and records the delta since the previous one,
// returning how many changes were recorded.
//
// The first call only establishes a baseline: a tree that was already dirty when
// monitoring started did not change *now*, and recording it as though it had
// would date every pre-existing edit to monitor startup.
func (r *GitRecorder) Sample(ctx context.Context) (int, error) {
	if r == nil || r.root == "" {
		return 0, nil
	}

	current, err := gitDirtySnapshot(ctx, r.root)
	if err != nil {
		return 0, err
	}

	if !r.sampled {
		r.prev = current
		r.sampled = true
		return 0, nil
	}

	changes := DetectFileChanges(r.root, r.prev, current)
	if len(changes) == 0 {
		r.prev = current
		return 0, nil
	}

	now := time.Now()
	entries := make([]RecordedFileChange, 0, len(changes))
	for _, change := range changes {
		entries = append(entries, RecordedFileChange{
			Timestamp: now,
			Session:   r.session,
			Agents:    []string{r.identity},
			Change:    change,
		})
	}

	// Persist before advancing. The readers all live in other processes, so the
	// durable ledger is what actually reaches `ntm changes`, `ntm conflicts` and
	// the dashboard, and AppendFileChanges is one transaction: a failure wrote
	// nothing. Advancing prev first would compare the next sample against a
	// state whose changes were never recorded anywhere, dropping them for good
	// on a transient busy database. Leaving prev where it is costs a re-detect
	// on the next tick instead.
	if err := persistChanges(r.projectDir, entries); err != nil {
		return 0, err
	}

	r.prev = current
	for _, entry := range entries {
		r.store.Add(entry)
	}
	return len(entries), nil
}

// DetectFileChanges compares two dirty-file snapshots of the tree at root and
// returns the delta. Paths are repo-relative so the same file compares equal
// across two agents' worktrees, which is what makes cross-worktree conflict
// detection possible.
func DetectFileChanges(root string, before, after map[string]FileState) []FileChange {
	changes := make([]FileChange, 0)

	for path, afterState := range after {
		afterState := afterState
		beforeState, existed := before[path]

		// A snapshot holds only dirty files, so a path absent from before was
		// clean then: git's own status says whether it appeared, was edited, or
		// was removed.
		if !existed {
			switch {
			case strings.Contains(afterState.GitStatus, "D"):
				changes = append(changes, FileChange{Path: path, Type: FileDeleted, After: &afterState})
			case strings.ContainsAny(afterState.GitStatus, "MR"):
				changes = append(changes, FileChange{Path: path, Type: FileModified, After: &afterState})
			default:
				changes = append(changes, FileChange{Path: path, Type: FileAdded, After: &afterState})
			}
			continue
		}

		if strings.Contains(afterState.GitStatus, "D") {
			if !strings.Contains(beforeState.GitStatus, "D") {
				beforeState := beforeState
				changes = append(changes, FileChange{Path: path, Type: FileDeleted, Before: &beforeState, After: &afterState})
			}
			continue
		}

		// Still dirty in both samples: only a changed signature means it was
		// edited again rather than merely staying dirty.
		if !afterState.ModTime.Equal(beforeState.ModTime) || afterState.Size != beforeState.Size {
			beforeState := beforeState
			changes = append(changes, FileChange{Path: path, Type: FileModified, Before: &beforeState, After: &afterState})
		}
	}

	for path, beforeState := range before {
		if _, ok := after[path]; ok {
			continue
		}
		if strings.Contains(beforeState.GitStatus, "D") {
			// Already reported as deleted when it went missing.
			continue
		}
		// Leaving `git status` usually means committed or reverted, not deleted.
		// Stat against the tree root — the paths here are repo-relative, so
		// statting them bare would resolve against the process working
		// directory and call every committed file a deletion.
		if _, err := os.Stat(filepath.Join(root, path)); err == nil {
			continue
		}
		beforeState := beforeState
		changes = append(changes, FileChange{Path: path, Type: FileDeleted, Before: &beforeState})
	}

	return changes
}

// gitDirtySnapshot returns every path git reports as dirty in the tree at root,
// keyed repo-relative, with the stat signature used to spot repeat edits.
func gitDirtySnapshot(ctx context.Context, root string) (map[string]FileState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmdCtx, cancel := context.WithTimeout(ctx, gitStatusTimeout)
	defer cancel()

	// -z survives paths containing spaces, quotes and newlines, which the
	// default porcelain output escapes instead.
	cmd := exec.CommandContext(cmdCtx, "git", "status", "--porcelain", "-z", "--untracked-files=all")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		if cmdCtx.Err() != nil {
			return nil, fmt.Errorf("git status in %s: %w", root, cmdCtx.Err())
		}
		return nil, fmt.Errorf("git status in %s: %w", root, err)
	}

	snapshot := make(map[string]FileState)
	records := bytes.Split(out, []byte{0})
	for i := 0; i < len(records); i++ {
		record := string(records[i])
		if len(record) < 4 {
			continue
		}
		status := strings.TrimSpace(record[:2])
		path := record[3:]

		// Renames and copies spend a second NUL-record on the source path.
		if strings.ContainsAny(record[:2], "RC") {
			i++
		}
		if path == "" {
			continue
		}

		state := FileState{GitStatus: status}
		if info, err := os.Stat(filepath.Join(root, path)); err == nil {
			state.ModTime = info.ModTime()
			state.Size = info.Size()
		}
		snapshot[path] = state
	}

	return snapshot, nil
}

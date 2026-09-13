package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
	"github.com/Dicklesworthstone/ntm/internal/worktrees"
)

// fileChangeSampleInterval is how often each monitored working tree is sampled.
// Slow enough that `git status` stays negligible even with a monitor per
// session, fast enough that two agents racing on one file land in the same
// conflict window (tracker.CriticalConflictWindow is 10 minutes).
const fileChangeSampleInterval = 15 * time.Second

// File change capture for the session monitor.
//
// tracker.GlobalFileChanges is what backs `ntm changes`, `ntm conflicts`,
// --robot-status file_changes/conflicts, the dashboard Files panel and the
// work-coordination adapter's FileConflicts input. Its original writer was
// deleted in 31fc0f5f, so all of those reported an empty all-clear no matter
// what the agents did. The monitor already runs one process per session for the
// lifetime of that session, which makes it the natural place to sample from.

// buildFileChangeRecorders returns one recorder per attribution unit for a
// session.
//
// Attribution is taken from the filesystem layout rather than guessed. When a
// session uses worktree isolation each agent owns a separate working tree, so
// git can say exactly which agent touched a file and each worktree gets its own
// recorder. A shared checkout gets a single recorder attributed to the session,
// because git cannot distinguish agents inside one tree — and attributing a
// shared tree's edits to every agent in the session would be worse than
// recording nothing: tracker.DetectConflicts treats three distinct agents on one
// path as a *critical* conflict, so one agent editing one file twice would be
// reported as a critical three-way conflict.
func buildFileChangeRecorders(ctx context.Context, manifest *resilience.SpawnManifest) []*tracker.GitRecorder {
	if manifest == nil || manifest.ProjectDir == "" || manifest.Session == "" {
		return nil
	}
	session := manifest.Session

	if worktrees.SessionHasWorktrees(manifest.ProjectDir, session) {
		manager := worktrees.NewManager(manifest.ProjectDir, session)
		infos, err := manager.ListWorktrees(ctx)
		if err != nil {
			slog.Default().Debug("file change recorders: listing worktrees failed",
				"session", session, "error", err)
		} else if len(infos) > 0 {
			recorders := make([]*tracker.GitRecorder, 0, len(infos))
			for _, info := range infos {
				if info == nil || info.Path == "" || info.AgentName == "" {
					continue
				}
				if stat, err := os.Stat(info.Path); err != nil || !stat.IsDir() {
					continue
				}
				recorders = append(recorders, tracker.NewGitRecorder(tracker.GitRecorderConfig{
					Root:       info.Path,
					ProjectDir: manifest.ProjectDir,
					Session:    session,
					Identity:   info.AgentName,
				}))
			}
			if len(recorders) > 0 {
				return recorders
			}
		}
	}

	return []*tracker.GitRecorder{tracker.NewGitRecorder(tracker.GitRecorderConfig{
		Root:       manifest.ProjectDir,
		ProjectDir: manifest.ProjectDir,
		Session:    session,
		Identity:   session,
	})}
}

// sampleFileChanges takes one observation from every recorder.
//
// Sampling is best-effort telemetry: a tree that is not a git repository, or a
// git invocation that fails or times out, is logged at debug and skipped rather
// than allowed to disturb the monitor loop that drives it.
func sampleFileChanges(ctx context.Context, recorders []*tracker.GitRecorder) {
	for _, recorder := range recorders {
		if recorder == nil {
			continue
		}
		if _, err := recorder.Sample(ctx); err != nil {
			slog.Default().Debug("file change sample failed", "error", err)
		}
	}
}

// describeFileChangeRecorders renders the startup line for the monitor log.
func describeFileChangeRecorders(recorders []*tracker.GitRecorder) string {
	if len(recorders) == 0 {
		return "file change tracking disabled (no project directory)"
	}
	if len(recorders) == 1 {
		return "Tracking file changes for 1 working tree"
	}
	return fmt.Sprintf("Tracking file changes for %d agent worktrees", len(recorders))
}

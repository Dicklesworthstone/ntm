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

// File change capture for the session monitor.
//
// tracker.GlobalFileChanges is what backs `ntm changes`, `ntm conflicts`,
// --robot-status file_changes/conflicts, the dashboard Files panel and the
// work-coordination adapter's FileConflicts input. Its original writer was
// deleted in 31fc0f5f, so all of those reported an empty all-clear no matter
// what the agents did. The monitor already runs one process per session for the
// lifetime of that session, which makes it the natural place to sample from.

// fileChangeSampleInterval is how often each monitored working tree is sampled.
// Slow enough that `git status` stays negligible even with a monitor per
// session, fast enough that two agents racing on one file land in the same
// conflict window (tracker.CriticalConflictWindow is 10 minutes).
const fileChangeSampleInterval = 15 * time.Second

// fileChangeSampler samples every working tree belonging to a session.
//
// The recorder set is re-derived on each tick rather than fixed at startup.
// `ntm add` can give a running session new agents — and with worktree isolation,
// new working trees — and a set built once would keep attributing everything to
// the session. Worse, it would never look inside those trees at all: worktrees
// live under .ntm, which is gitignored, so the project-root recorder cannot see
// them and the agents' work would go unrecorded entirely.
type fileChangeSampler struct {
	manifest *resilience.SpawnManifest
	// recorders is keyed by working tree so a recorder — and with it the
	// baseline it has established — survives re-derivation. Entries are never
	// dropped: a transient failure to list worktrees would otherwise discard
	// baselines and the next sample would silently re-baseline, losing every
	// change made in between.
	recorders map[string]*tracker.GitRecorder
}

func newFileChangeSampler(manifest *resilience.SpawnManifest) *fileChangeSampler {
	return &fileChangeSampler{
		manifest:  manifest,
		recorders: make(map[string]*tracker.GitRecorder),
	}
}

// Sample takes one observation from every working tree the session currently
// has.
//
// Sampling is best-effort telemetry: a tree that is not a git repository, or a
// git invocation that fails or times out, is logged at debug and skipped rather
// than allowed to disturb the monitor loop that drives it.
func (s *fileChangeSampler) Sample(ctx context.Context) {
	if s == nil {
		return
	}
	for _, cfg := range fileChangeRecorderConfigs(ctx, s.manifest) {
		recorder, ok := s.recorders[cfg.Root]
		if !ok {
			recorder = tracker.NewGitRecorder(cfg)
			s.recorders[cfg.Root] = recorder
		}
		if _, err := recorder.Sample(ctx); err != nil {
			slog.Default().Debug("file change sample failed", "root", cfg.Root, "error", err)
		}
	}
}

// fileChangeRecorderConfigs describes one recorder per attribution unit.
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
func fileChangeRecorderConfigs(ctx context.Context, manifest *resilience.SpawnManifest) []tracker.GitRecorderConfig {
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
			configs := make([]tracker.GitRecorderConfig, 0, len(infos))
			for _, info := range infos {
				if info == nil || info.Path == "" || info.AgentName == "" {
					continue
				}
				if stat, err := os.Stat(info.Path); err != nil || !stat.IsDir() {
					continue
				}
				configs = append(configs, tracker.GitRecorderConfig{
					Root:       info.Path,
					ProjectDir: manifest.ProjectDir,
					Session:    session,
					Identity:   info.AgentName,
				})
			}
			if len(configs) > 0 {
				return configs
			}
		}
	}

	return []tracker.GitRecorderConfig{{
		Root:       manifest.ProjectDir,
		ProjectDir: manifest.ProjectDir,
		Session:    session,
		Identity:   session,
	}}
}

// describeFileChangeSampler renders the startup line for the monitor log.
func describeFileChangeSampler(ctx context.Context, manifest *resilience.SpawnManifest) string {
	configs := fileChangeRecorderConfigs(ctx, manifest)
	switch {
	case len(configs) == 0:
		return "File change tracking disabled (no project directory)"
	case len(configs) == 1:
		return "Tracking file changes for 1 working tree"
	default:
		return fmt.Sprintf("Tracking file changes for %d agent worktrees", len(configs))
	}
}

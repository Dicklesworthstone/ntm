package serve

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// jobExecutionResources maps the normalized engine request to exclusive
// execution scopes. These are scheduling keys, not authorization decisions:
// the existing dispatcher still validates every option and enforces policy.
// Unknown or implicit targets use a global barrier rather than guessing a pane
// or racing an operation whose destination has not yet been resolved.
func jobExecutionResources(jobType string, params map[string]interface{}) []string {
	session, ok := params["session"].(string)
	if !ok || session == "" {
		return []string{"*"}
	}
	switch jobType {
	case JobTypePipelineRun, JobTypePipelineExec:
		return []string{"session:" + session}
	case JobTypeSwarmSpawn:
		if raw, present := params["label"]; present {
			label, ok := raw.(string)
			if !ok {
				return []string{"*"}
			}
			if label != "" {
				session += "--" + label
			}
		}
		return []string{"session:" + session}
	case JobTypeCheckpointRestore:
		// Storage.Load verifies that the checkpoint's session matches its
		// source namespace. Without an override that is the restore target;
		// mismatched metadata is rejected before the restorer can act.
		target := session
		if raw, present := params["target_session"]; present {
			explicit, ok := raw.(string)
			if !ok {
				return []string{"*"}
			}
			if explicit != "" {
				target = explicit
			}
		}
		return []string{"session:" + target}
	case JobTypePipelineResume:
		// Admission replaces this barrier only when both the saved-run
		// namespace and the effective session have been bound together.
		return []string{"*"}
	default:
		return []string{"*"}
	}
}

type jobResumeTargetKey struct{}

// jobResumeTarget binds identity, not checkpoint contents. The engine must still
// reload completed steps under its existing run lock. This private value never
// changes the submitted request's operation-ID fingerprint or persists commands.
type jobResumeTarget struct {
	ProjectDir string
	StateDir   string
	RunID      string
	Session    string
}

// resolveJobResumeTarget uses read-only admission-time evidence. Unresolvable
// targets retain the global barrier and the dispatcher's original error path.
// In particular, an operation-ID replay must not require a still-present saved
// run: its durable result can be returned without entering the resume engine.
func resolveJobResumeTarget(projectDir string, params map[string]interface{}, loadSession func(string, string) (string, error)) *jobResumeTarget {
	runID, ok := params["run_id"].(string)
	if !ok || strings.TrimSpace(runID) == "" || runID == "." || runID == ".." || strings.ContainsAny(runID, "/\\\x00") {
		return nil
	}
	session := ""
	if raw, present := params["session"]; present {
		var ok bool
		session, ok = raw.(string)
		if !ok {
			return nil
		}
	}
	root, err := filepath.Abs(projectDir)
	if err != nil || projectDir == "" {
		return nil
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil
	}
	stateDir, err := jobResumeStateDirectory(root)
	if err != nil {
		return nil
	}
	if session == "" {
		if loadSession == nil {
			return nil
		}
		session, err = loadSession(root, runID)
		if err != nil {
			return nil
		}
	}
	if strings.TrimSpace(session) == "" {
		return nil
	}
	return &jobResumeTarget{ProjectDir: root, StateDir: stateDir, RunID: runID, Session: session}
}

func jobResumeStateDirectory(projectDir string) (string, error) {
	// LoadState/SaveState's documented namespace. Resolve its parent rather
	// than following a particular state-file symlink: SaveState publishes by
	// atomic rename, replacing that directory entry, not its symlink target.
	dir, err := filepath.EvalSymlinks(filepath.Join(projectDir, ".ntm", "pipelines"))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("pipeline state namespace is not a directory: %s", dir)
	}
	return dir, nil
}

func (target jobResumeTarget) resources() []string {
	// Hash the complete canonical state pathname, not just the run ID. Two
	// sessions resuming the same run conflict; independent project runs do not.
	key := sha256.Sum256([]byte(filepath.Join(target.StateDir, target.RunID+".json")))
	return []string{"session:" + target.Session, fmt.Sprintf("pipeline-run:%x", key)}
}

// applyJobResumeTarget is called before loading execution state. A queued job
// retains its admitted destination even if another resume changed the saved
// Session field. Fail closed if request identity or the storage namespace has
// drifted; do not silently execute against a resource the scheduler never held.
func applyJobResumeTarget(ctx context.Context, projectDir, runID, session string) (string, error) {
	target, bound := ctx.Value(jobResumeTargetKey{}).(jobResumeTarget)
	if !bound {
		return session, nil
	}
	if target.ProjectDir != projectDir || target.RunID != runID || (session != "" && target.Session != session) {
		return "", fmt.Errorf("pipeline resume target differs from its admitted identity")
	}
	stateDir, err := jobResumeStateDirectory(projectDir)
	if err != nil {
		return "", fmt.Errorf("inspect admitted pipeline state namespace: %w", err)
	}
	if stateDir != target.StateDir {
		return "", fmt.Errorf("pipeline state namespace changed after job admission")
	}
	return target.Session, nil
}

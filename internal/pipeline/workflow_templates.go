package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	templateSnapshotDir = "template-snapshots"
	// Bound both individual reads and the aggregate unique source material.
	// Workflow limits may impose a stricter per-template bound.
	maxSnapshotTemplateBytes = 16 << 20
)

// snapshotWorkflowTemplates replaces existing template dependencies with
// content-addressed copies. Call it on the private parsed workflow, never the
// caller's object. Resolve paths BEFORE relocating the workflow definition:
// the executor's source-directory-first search order must not change.
//
// An absent absolute path denotes a template generated during execution and
// stays a live dependency. Missing relative paths are ambiguous and rejected.
// Existing managed copies are verified, never re-hashed into trusted artifacts
// after corruption. Template bytes are not rendered during snapshotting, so
// runtime variables, parameters, and output substitution keep their semantics.
func snapshotWorkflowTemplates(ctx context.Context, root, sourceFile string, workflow *Workflow) error {
	resolver := &Executor{config: ExecutorConfig{ProjectDir: root, WorkflowFile: sourceFile}}
	limit := int64(maxSnapshotTemplateBytes)
	if configured := workflow.Settings.Limits.EffectiveLimits().MaxTemplateBytes; configured > 0 && configured < limit {
		limit = configured
	}
	copied := make(map[string]string)
	remaining := int64(maxSnapshotTemplateBytes)
	var destination string
	return visitWorkflowTemplates(workflow, true, func(step *Step) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		original := step.Template
		if managedTemplateSnapshot(original) {
			if _, err := readTemplateSnapshot(original, limit); err != nil {
				return fmt.Errorf("snapshot step %q: %w", step.ID, err)
			}
		}
		resolved := resolver.resolveTemplatePath(original)
		if resolved == "" {
			if filepath.IsAbs(original) {
				if _, err := os.Stat(original); errors.Is(err, os.ErrNotExist) {
					slog.Warn("pipeline template remains a live generated dependency", "step_id", step.ID, "template", original)
					return nil
				}
			}
			return fmt.Errorf("snapshot step %q: cannot resolve template %q; generated templates require an absolute path", step.ID, original)
		}
		canonical, err := filepath.EvalSymlinks(resolved)
		if err != nil {
			return fmt.Errorf("resolve template for step %q: %w", step.ID, err)
		}
		canonical, err = filepath.Abs(canonical)
		if err != nil {
			return err
		}
		if saved, ok := copied[canonical]; ok {
			step.Template = saved
			return nil
		}
		readLimit := limit
		if remaining < readLimit {
			readLimit = remaining
		}
		data, err := readTemplateFile(canonical, readLimit)
		if err != nil {
			return fmt.Errorf("snapshot template for step %q (16 MiB aggregate limit): %w", step.ID, err)
		}
		remaining -= int64(len(data))
		if err := ctx.Err(); err != nil {
			return err
		}
		if destination == "" {
			destination, err = prepareTemplateSnapshotDirectory(root)
			if err != nil {
				return err
			}
		}
		saved, err := publishTemplateSnapshot(destination, data)
		if err != nil {
			return fmt.Errorf("persist template for step %q: %w", step.ID, err)
		}
		copied[canonical] = saved
		step.Template = saved
		return nil
	})
}

// verifyWorkflowTemplates runs before a managed workflow is resumed. It
// verifies only frozen dependencies; deliberately live generated paths and
// pre-existing legacy snapshots retain their existing executor semantics.
func verifyWorkflowTemplates(workflowPath string, workflow *Workflow) error {
	base, err := filepath.Abs(filepath.Dir(workflowPath))
	if err != nil {
		return err
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return err
	}
	expectedDir := filepath.Join(base, templateSnapshotDir)
	checked := make(map[string]bool)
	remaining := int64(maxSnapshotTemplateBytes)
	return visitWorkflowTemplates(workflow, false, func(step *Step) error {
		path := step.Template
		if !managedTemplateSnapshot(path) || checked[path] {
			return nil
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil || parent != expectedDir {
			return fmt.Errorf("step %q template snapshot escapes its workflow bundle", step.ID)
		}
		data, err := readTemplateSnapshot(path, remaining)
		if err != nil {
			return fmt.Errorf("step %q template snapshot: %w", step.ID, err)
		}
		remaining -= int64(len(data))
		checked[path] = true
		return nil
	})
}

func visitWorkflowTemplates(workflow *Workflow, rewriteBranches bool, visit func(*Step) error) error {
	var walk func([]Step) error
	walk = func(steps []Step) error {
		for i := range steps {
			step := &steps[i]
			if step.Template != "" {
				if err := visit(step); err != nil {
					return err
				}
			}
			children := [][]Step{step.Parallel.Steps, step.OnSuccess}
			if step.Loop != nil {
				children = append(children, step.Loop.Steps)
			}
			if step.Foreach != nil {
				children = append(children, step.Foreach.Steps)
			}
			if step.ForeachPane != nil {
				children = append(children, step.ForeachPane.Steps)
			}
			for _, foreach := range []*ForeachConfig{step.Foreach, step.ForeachPane} {
				if foreach != nil && foreach.Template != "" {
					dependency := Step{ID: step.ID + ".foreach", Template: foreach.Template}
					if err := visit(&dependency); err != nil {
						return err
					}
					foreach.Template = dependency.Template
				}
			}
			if path, ok := step.OnFailure.Fallback["template"].(string); ok && path != "" {
				dependency := Step{ID: step.ID + ".on_failure", Template: path}
				if err := visit(&dependency); err != nil {
					return err
				}
				step.OnFailure.Fallback["template"] = dependency.Template
			}
			for _, group := range children {
				if err := walk(group); err != nil {
					return err
				}
			}
			keys := make([]string, 0, len(step.Branches))
			for key := range step.Branches {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				// Use the executor's decoder rather than inventing another branch
				// schema. Typed steps retain native YAML marshalers in the snapshot.
				branch, err := parseBranchSteps(step.Branches[key], step.ID, key)
				if err != nil {
					return err
				}
				if err := walk(branch); err != nil {
					return err
				}
				if rewriteBranches {
					// parseBranchSteps supplies runtime IDs for anonymous steps.
					// Do not freeze those: foreach/loop parents may be renamed
					// before dispatch and must derive IDs from their live scope.
					if err := restoreAuthoredBranchIDs(step.Branches[key], branch); err != nil {
						return err
					}
					step.Branches[key] = branch
				}
			}
		}
		return nil
	}
	for _, group := range [][]Step{workflow.Steps, workflow.PostPipelineSteps, workflow.Settings.OnCancel} {
		if err := walk(group); err != nil {
			return err
		}
	}
	return nil
}

func restoreAuthoredBranchIDs(value interface{}, steps []Step) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	type identity struct {
		ID string `json:"id"`
	}
	var ids []identity
	if err := json.Unmarshal(data, &ids); err != nil {
		var single identity
		if err := json.Unmarshal(data, &single); err != nil {
			return err
		}
		ids = []identity{single}
	}
	if len(ids) != len(steps) {
		return errors.New("branch shape changed while snapshotting templates")
	}
	for i := range steps {
		steps[i].ID = ids[i].ID
	}
	return nil
}

func prepareTemplateSnapshotDirectory(root string) (string, error) {
	root = normalizeLockRoot(root)
	dir := filepath.Join(pipelineStateDir(root), workflowSnapshotDir, templateSnapshotDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return "", errors.New("template snapshot directory must not be a symlink")
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	// Do not let symlinked storage relocate the bundle outside the project.
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("template snapshot directory escapes project")
	}
	// Keep the managed directory marker in the stored path even when a
	// parent (for example .ntm) is a legitimate in-project symlink.
	return dir, nil
}

func managedTemplateSnapshot(path string) bool {
	return filepath.Base(filepath.Dir(path)) == templateSnapshotDir
}

func readTemplateSnapshot(path string, limit int64) ([]byte, error) {
	name := filepath.Base(path)
	encoded := strings.TrimSuffix(strings.TrimPrefix(name, "sha256-"), ".tmpl")
	expected, err := hex.DecodeString(encoded)
	if err != nil || len(expected) != sha256.Size || name != "sha256-"+encoded+".tmpl" {
		return nil, errors.New("invalid template snapshot filename")
	}
	data, err := readTemplateFile(path, limit)
	if err != nil {
		return nil, err
	}
	actual := sha256.Sum256(data)
	if !bytes.Equal(expected, actual[:]) {
		return nil, errors.New("template snapshot content hash mismatch; restore the original artifact before resuming")
	}
	return data, nil
}

func readTemplateFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("template must be a regular file, not a symlink or special file")
	}
	if limit < 0 || info.Size() > limit {
		return nil, errors.New("template exceeds snapshot byte limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errors.New("template changed while opening it")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("template exceeds snapshot byte limit")
	}
	return data, nil
}

// Publish without replacement: a concurrent writer or a corrupt destination
// cannot be silently overwritten by the check-then-rename race. The temporary
// file is fully written and synced before its immutable name becomes visible.
func publishTemplateSnapshot(dir string, data []byte) (string, error) {
	digest := sha256.Sum256(data)
	path := filepath.Join(dir, "sha256-"+hex.EncodeToString(digest[:])+".tmpl")
	verify := func() (string, error) {
		existing, err := readTemplateSnapshot(path, maxSnapshotTemplateBytes)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(existing, data) {
			return "", errors.New("existing template snapshot differs from source")
		}
		return path, nil
	}
	if _, err := os.Lstat(path); err == nil {
		return verify()
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	file, err := os.CreateTemp(dir, ".template-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	if _, err := file.Write(data); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := os.Link(file.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return verify()
		}
		return "", fmt.Errorf("publish template snapshot without replacement: %w", err)
	}
	if err := util.SyncDirectory(dir); err != nil {
		return "", err
	}
	return path, nil
}

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
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	workflowSnapshotDir      = "workflow-snapshots"
	maxWorkflowSnapshotBytes = 16 << 20
)

// SnapshotWorkflow freezes the workflow before the first step can run. The
// artifact uses the schema's YAML marshalers and the SAME strict YAML parser
// used for ordinary workflow files, so CLI resume can read it too. Execute
// the returned copy, not the caller's mutable object. File-backed workflows
// are frozen as well; editing their source later cannot change a saved run.
//
// The content hash in the filename is retained in ExecutionState.WorkflowFile.
// Existing artifacts are verified, never silently repaired or overwritten.
// sourceFile optionally supplies the original workflow location for relative
// template lookup. Omit it for inline workflows rooted at projectDir.
func SnapshotWorkflow(ctx context.Context, projectDir string, workflow *Workflow, sourceFile ...string) (*Workflow, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(projectDir) == "" || workflow == nil {
		return nil, "", errors.New("project directory and workflow are required for a resumable run")
	}
	if len(sourceFile) > 1 {
		return nil, "", errors.New("at most one workflow source file is allowed")
	}
	root := normalizeLockRoot(projectDir)
	source := ""
	if len(sourceFile) == 1 && sourceFile[0] != "" {
		source = sourceFile[0]
		if !filepath.IsAbs(source) {
			source = filepath.Join(root, source)
		}
	}
	data, err := marshalWorkflowSnapshot(workflow)
	if err != nil {
		return nil, "", fmt.Errorf("encode workflow snapshot: %w", err)
	}
	if len(data) > maxWorkflowSnapshotBytes {
		return nil, "", errors.New("workflow snapshot exceeds 16 MiB")
	}
	// Parse before publishing or dispatching: a schema serialization regression
	// must not create a run whose first recovery attempt discovers a bad file.
	frozen, validation, err := parseWorkflowSnapshot(data)
	if err != nil {
		return nil, "", err
	}
	if !validation.Valid {
		return nil, "", fmt.Errorf("invalid workflow snapshot: %v", validation.Errors)
	}
	if err := snapshotWorkflowTemplates(ctx, root, source, frozen); err != nil {
		return nil, "", err
	}
	// Persist normalized defaults, not just the request's shorthand. Reparse
	// the final representation so the live run uses precisely its saved form.
	data, err = marshalWorkflowSnapshot(frozen)
	if err != nil {
		return nil, "", fmt.Errorf("encode normalized workflow snapshot: %w", err)
	}
	if len(data) > maxWorkflowSnapshotBytes {
		return nil, "", errors.New("normalized workflow snapshot exceeds 16 MiB")
	}
	frozen, validation, err = parseWorkflowSnapshot(data)
	if err != nil {
		return nil, "", err
	}
	if !validation.Valid {
		return nil, "", fmt.Errorf("invalid normalized workflow snapshot: %v", validation.Errors)
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	dir := filepath.Join(pipelineStateDir(root), workflowSnapshotDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, "", fmt.Errorf("create workflow snapshot directory: %w", err)
	}
	// Do not publish executable definitions through a directory symlink that
	// escapes the project. Resume independently applies its path confinement.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, "", fmt.Errorf("resolve workflow snapshot directory: %w", err)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, "", errors.New("workflow snapshot directory escapes project")
	}
	digest := sha256.Sum256(data)
	// Keep the managed namespace in the locator even if an in-project parent
	// is symlinked. Resolving away the marker would bypass resume verification.
	path := filepath.Join(dir, "sha256-"+hex.EncodeToString(digest[:])+".yaml")
	existing, err := readWorkflowSnapshot(path)
	switch {
	case err == nil:
		if !bytes.Equal(existing, data) {
			return nil, "", errors.New("existing workflow snapshot does not match its content hash")
		}
	case errors.Is(err, os.ErrNotExist):
		// Concurrent publishers of this content-addressed path necessarily
		// publish identical bytes. Atomic rename prevents partial reads.
		if err := util.AtomicWriteFile(path, data, 0600); err != nil {
			return nil, "", fmt.Errorf("persist workflow snapshot: %w", err)
		}
	default:
		return nil, "", fmt.Errorf("verify existing workflow snapshot: %w", err)
	}
	return frozen, path, nil
}

func marshalWorkflowSnapshot(workflow *Workflow) ([]byte, error) {
	// Reject cycles, unsupported values and excessive input before YAML's
	// recursive encoder. This is validation only: JSON's struct representation
	// is NOT the YAML wire shape for PaneSpec, IntOrExpr, ParallelSpec, etc.
	encoded, err := json.Marshal(workflow)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxWorkflowSnapshotBytes {
		return nil, errors.New("workflow snapshot exceeds 16 MiB")
	}
	return yaml.Marshal(workflow)
}

// LoadResumeWorkflow verifies a managed snapshot before parsing it. Ordinary
// saved YAML/TOML paths keep using the existing file loader. There is no
// fallback to a mutable source when a snapshot is missing or damaged.
func LoadResumeWorkflow(path string) (*Workflow, ValidationResult, error) {
	if filepath.Base(filepath.Dir(path)) != workflowSnapshotDir {
		return LoadAndValidate(path)
	}
	name := filepath.Base(path)
	encoded := strings.TrimSuffix(strings.TrimPrefix(name, "sha256-"), ".yaml")
	expected, err := hex.DecodeString(encoded)
	if err != nil || len(expected) != sha256.Size || name != "sha256-"+encoded+".yaml" {
		return nil, ValidationResult{}, errors.New("invalid workflow snapshot filename")
	}
	data, err := readWorkflowSnapshot(path)
	if err != nil {
		return nil, ValidationResult{}, fmt.Errorf("read workflow snapshot: %w", err)
	}
	actual := sha256.Sum256(data)
	if !bytes.Equal(expected, actual[:]) {
		return nil, ValidationResult{}, errors.New("workflow snapshot content hash mismatch; restore the original artifact before resuming")
	}
	workflow, validation, err := parseWorkflowSnapshot(data)
	if err != nil || !validation.Valid {
		return workflow, validation, err
	}
	if err := verifyWorkflowTemplates(path, workflow); err != nil {
		return nil, ValidationResult{}, err
	}
	return workflow, validation, nil
}

func parseWorkflowSnapshot(data []byte) (*Workflow, ValidationResult, error) {
	workflow, err := ParseString(string(data), "yaml")
	if err != nil {
		return nil, ValidationResult{}, fmt.Errorf("parse workflow snapshot: %w", err)
	}
	return workflow, Validate(workflow), nil
}

func readWorkflowSnapshot(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("workflow snapshot must be a regular file")
	}
	if info.Size() > maxWorkflowSnapshotBytes {
		return nil, errors.New("workflow snapshot exceeds 16 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxWorkflowSnapshotBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxWorkflowSnapshotBytes {
		return nil, errors.New("workflow snapshot exceeds 16 MiB")
	}
	return data, nil
}

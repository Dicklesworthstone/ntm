package worktrees

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrMergeRecoveryRequired = errors.New("previous NTM merge requires recovery")

const mergeRecoveryLimit = 64 << 10

// MergeRecovery is the last admitted application, not an instruction to replay
// Git. A prepared record is deliberately outcome-unknown, not proof of a live
// worker. Only a verified commit or explicit abandonment settles that barrier.
type MergeRecovery struct {
	Version   int          `json:"version"`
	AttemptID string       `json:"attempt_id"`
	State     string       `json:"state"`
	StartedAt time.Time    `json:"started_at"`
	UpdatedAt time.Time    `json:"updated_at"`
	Plan      MergePreview `json:"plan"`
	Commit    string       `json:"commit,omitempty"`
}

func readMergeRecovery(common string) (*MergeRecovery, error) {
	path := filepath.Join(common, "ntm-merge.json")
	file, err := openMergeRecoveryFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read merge recovery record: %w", err)
	}
	defer file.Close()
	if err := verifyMergeFenceFile(file, path); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, mergeRecoveryLimit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > mergeRecoveryLimit {
		return nil, errors.New("merge recovery record exceeds its size limit")
	}
	var record MergeRecovery
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return nil, fmt.Errorf("invalid merge recovery record; refusing new execution: %w", err)
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		return nil, errors.New("merge recovery record contains trailing data")
	}
	if err := validateMergeRecovery(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func validateMergeRecovery(record *MergeRecovery) error {
	id, err := hex.DecodeString(record.AttemptID)
	if record.Version != 1 || err != nil || len(id) != 16 || hex.EncodeToString(id) != record.AttemptID || record.StartedAt.IsZero() || record.UpdatedAt.Before(record.StartedAt) {
		return errors.New("invalid merge recovery identity or version")
	}
	plan := &record.Plan
	if !isMergeObjectID(plan.SourceCommit) || !isMergeObjectID(plan.TargetCommit) || !isMergeObjectID(plan.MergeTree) || plan.SourceBranch == "" || plan.TargetBranch == "" {
		return errors.New("invalid merge recovery snapshot")
	}
	for _, path := range []string{plan.SourcePath, plan.TargetPath} {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
			return errors.New("invalid merge recovery checkout path")
		}
	}
	switch record.State {
	case "prepared", "abandoned":
		if record.Commit != "" {
			return errors.New("uncommitted merge record names a commit")
		}
	case "committed", "recovered":
		if !isMergeObjectID(record.Commit) {
			return errors.New("settled merge record lacks a verified commit")
		}
	default:
		return errors.New("unknown merge recovery state")
	}
	return nil
}

func (f *mergeFence) requireSettledMerge() error {
	return requireSettledMergeRecovery(f.common)
}

// Share the recovery barrier with read-only preflight. Mutation paths still
// call it while holding the repository's writer fence.
func requireSettledMergeRecovery(common string) error {
	record, err := readMergeRecovery(common)
	if err != nil {
		return errors.Join(ErrMergeRecoveryRequired, err)
	}
	if record != nil && record.State == "prepared" {
		return fmt.Errorf("%w: attempt %s; inspect with ntm worktrees merge-status before merge-recover", ErrMergeRecoveryRequired, record.AttemptID)
	}
	return nil
}

// preflightMergeReadiness probes an existing lock without creating one. No
// lock file means only that no owner was observed, not permission to mutate;
// actual provisioning must acquire its own fence and recheck the receipt.
func preflightMergeReadiness(ctx context.Context, common string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path := filepath.Join(common, "ntm-merge.lock")
	file, err := openMergeRecoveryFile(path)
	if err == nil {
		defer file.Close()
		if err := verifyMergeFenceFile(file, path); err != nil {
			return err
		}
		if err := tryMergeFenceLock(file); err != nil {
			return err
		}
		if err := verifyMergeFenceFile(file, path); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect worktree ownership before provisioning: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return requireSettledMergeRecovery(common)
}

func (f *mergeFence) prepareMerge(plan *MergePreview) (*MergeRecovery, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	record := &MergeRecovery{Version: 1, AttemptID: hex.EncodeToString(id[:]), State: "prepared", StartedAt: now, UpdatedAt: now, Plan: *plan}
	if err := f.saveMergeRecovery(record); err != nil {
		return nil, errors.Join(ErrMergeRecoveryRequired, err)
	}
	return record, nil
}

func (f *mergeFence) saveMergeRecovery(record *MergeRecovery) error {
	record.UpdatedAt = time.Now().UTC()
	if err := validateMergeRecovery(record); err != nil {
		return err
	}
	if err := verifyMergeFenceFile(f.file, f.file.Name()); err != nil {
		return err
	}
	path := filepath.Join(f.common, "ntm-merge.json")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("merge recovery destination is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil || len(raw)+1 > mergeRecoveryLimit {
		return errors.Join(err, errors.New("merge recovery snapshot cannot be persisted"))
	}
	file, err := os.CreateTemp(f.common, ".ntm-merge-*.tmp")
	if err != nil {
		return err
	}
	defer file.Close()
	// Leave a failed temporary write for inspection; it never authorizes Git.
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return installMergeRecovery(file.Name(), path)
}

// InspectMerge reads the last durable receipt without taking the writer lock.
// It remains usable during a live merge; "prepared" makes no liveness claim.
func (m *WorktreeManager) InspectMerge(ctx context.Context) (*MergeRecovery, error) {
	if m == nil {
		return nil, errors.New("merge inspection requires a manager")
	}
	common, err := mergeCommonDirectory(ctx, m.projectPath)
	if err != nil {
		return nil, err
	}
	return readMergeRecovery(common)
}

// RecoverMerge never runs a merge, commit, reset, abort or stash. The caller
// must first stop Git writers (including orphaned hooks). Recovery only records
// exact observed completion, or explicitly abandons an unchanged clean target.
// The attempt ID prevents acknowledging a newer operation than was inspected.
func (m *WorktreeManager) RecoverMerge(ctx context.Context, attemptID string, abandon bool) (*MergeRecovery, error) {
	if m == nil {
		return nil, errors.New("merge recovery requires a manager")
	}
	project, err := filepath.Abs(m.projectPath)
	if err != nil || strings.TrimSpace(m.projectPath) == "" {
		return nil, errors.Join(err, errors.New("merge recovery requires a target directory"))
	}
	m = NewManager(project, m.session)
	fence, err := acquireMergeFence(ctx, m.projectPath)
	if err != nil {
		return nil, err
	}
	defer fence.close()
	record, err := readMergeRecovery(fence.common)
	if err != nil {
		return nil, err
	}
	if record == nil || record.AttemptID != attemptID {
		return record, fmt.Errorf("%w: recovery attempt ID does not match the inspected record", ErrMergeSnapshotChanged)
	}
	plan := &record.Plan
	if canonicalisePath(m.projectPath) != canonicalisePath(plan.TargetPath) {
		return record, errors.New("recover from the recorded target checkout, not another linked worktree")
	}
	if record.State != "prepared" {
		if abandon && record.State != "abandoned" {
			return record, errors.New("cannot abandon a verified merge commit")
		}
		return record, nil
	}
	blockers, err := mergeCheckoutBlockers(ctx, plan.TargetPath, plan.TargetBranch, "target")
	if err != nil || len(blockers) > 0 {
		return record, errors.Join(ErrMergeOutcomeUnverified, err, fmt.Errorf("recovery requires a clean target with no pending operation: %s", strings.Join(blockers, ", ")))
	}
	head, err := mergeCommitAt(ctx, plan.TargetPath, "HEAD")
	if err != nil {
		return record, err
	}
	updated := *record
	if abandon {
		if head != plan.TargetCommit {
			return record, errors.New("cannot abandon: target no longer matches the recorded pre-merge commit")
		}
		updated.State = "abandoned"
	} else {
		commit, err := verifyAppliedMerge(ctx, plan)
		if err != nil {
			return record, errors.Join(err, errors.New("no verified completed merge; inspect Git state and stop all writers before explicitly abandoning this attempt"))
		}
		updated.State, updated.Commit = "recovered", commit
	}
	if err := ctx.Err(); err != nil {
		return record, err
	}
	if err := fence.saveMergeRecovery(&updated); err != nil {
		return record, errors.Join(ErrMergeRecoveryRequired, err)
	}
	return &updated, nil
}

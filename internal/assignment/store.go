// Package assignment provides assignment tracking for bead-to-agent mappings.
package assignment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// ErrAssignmentStatusMismatch means a guarded mutation reloaded a different
// lifecycle state than the caller selected before acquiring the bead lock.
var ErrAssignmentStatusMismatch = errors.New("assignment status changed")

const (
	// assignmentsDirName is the directory name for assignment storage
	assignmentsDirName     = "assignments"
	fileExtension          = ".json"
	assignmentStoreVersion = 9

	// assignmentStoreGenerationVersion is the first schema version whose
	// snapshots carry a monotonic persistence generation. The generation makes
	// a fully published backup distinguishable from an older primary without
	// relying on wall-clock ordering.
	assignmentStoreGenerationVersion = 9

	// assignmentSaveFailAfterBackupEnv is an E2E-only failpoint. Its value is a
	// bead ID; the save fails after publishing a snapshot containing that bead's
	// delivery receipt to the backup, but before replacing the primary.
	assignmentSaveFailAfterBackupEnv = "NTM_TEST_ASSIGNMENT_SAVE_FAIL_AFTER_BACKUP"

	// completionAckFailOnceEnv is an E2E-only failpoint. Its value is a bead ID;
	// the first acknowledgement attempt for that bead fails before persistence,
	// while later attempts in the same process proceed normally.
	completionAckFailOnceEnv = "NTM_TEST_COMPLETION_ACK_FAIL_ONCE"
)

var completionAckFailures sync.Map

// AssignmentStatus represents the current state of an assignment
type AssignmentStatus string

// AssignmentClearState tracks the external lease-release boundary for clear.
type AssignmentClearState string

const (
	StatusClaiming   AssignmentStatus = "claiming"   // Durable intent exists; external claim outcome may need reconciliation
	StatusClaimed    AssignmentStatus = "claimed"    // Bead claimed; reservation/dispatch may still be pending
	StatusAssigned   AssignmentStatus = "assigned"   // Prompt sent, waiting to start
	StatusWorking    AssignmentStatus = "working"    // Agent actively working
	StatusCompleted  AssignmentStatus = "completed"  // Bead closed successfully
	StatusFailed     AssignmentStatus = "failed"     // Agent crashed or gave up
	StatusReassigned AssignmentStatus = "reassigned" // Moved to different agent

	ClearStateNone                 AssignmentClearState = ""
	ClearStateReservationReleasing AssignmentClearState = "reservation_releasing"
	ClearStateLeasesReleased       AssignmentClearState = "leases_released"
)

// Assignment represents a bead assigned to an agent
type Assignment struct {
	BeadID        string           `json:"bead_id"`
	BeadTitle     string           `json:"bead_title"`
	Pane          int              `json:"pane"`
	AgentType     string           `json:"agent_type"`           // claude, codex, gemini
	AgentName     string           `json:"agent_name,omitempty"` // Agent Mail name if registered
	Status        AssignmentStatus `json:"status"`
	AssignedAt    time.Time        `json:"assigned_at"`
	StartedAt     *time.Time       `json:"started_at,omitempty"` // When agent started working
	CompletedAt   *time.Time       `json:"completed_at,omitempty"`
	FailedAt      *time.Time       `json:"failed_at,omitempty"`
	FailReason    string           `json:"fail_reason,omitempty"`
	FailureReason string           `json:"failure_reason,omitempty"` // Detailed failure reason
	RetryCount    int              `json:"retry_count,omitempty"`    // Number of retry attempts
	PromptSent    string           `json:"prompt_sent,omitempty"`    // The actual prompt sent

	// Atomic assignment metadata is persisted before each external boundary so
	// retries can distinguish completed, recoverable, and outcome-unknown work.
	IdempotencyKey           string               `json:"idempotency_key,omitempty"`
	ClaimActor               string               `json:"claim_actor,omitempty"`
	ClaimState               ClaimState           `json:"claim_state,omitempty"`
	ClaimStatus              string               `json:"claim_status,omitempty"`
	ClaimAttempts            int                  `json:"claim_attempts,omitempty"`
	ClaimStartedAt           *time.Time           `json:"claim_started_at,omitempty"`
	ClaimError               string               `json:"claim_error,omitempty"`
	ClaimedAt                *time.Time           `json:"claimed_at,omitempty"`
	ClaimRequiresNonTerminal bool                 `json:"claim_requires_non_terminal,omitempty"`
	ReservationRequired      bool                 `json:"reservation_required,omitempty"`
	ReservationDiscovery     bool                 `json:"reservation_discovery,omitempty"`
	ReservationInputPaths    []string             `json:"reservation_input_paths,omitempty"`
	ReservationState         ReservationState     `json:"reservation_state,omitempty"`
	ReservationAttempts      int                  `json:"reservation_attempts,omitempty"`
	ReservationStartedAt     *time.Time           `json:"reservation_started_at,omitempty"`
	ReservationCompleted     bool                 `json:"reservation_completed,omitempty"`
	ReservationAgent         string               `json:"reservation_agent,omitempty"`
	ReservationTarget        string               `json:"reservation_target,omitempty"`
	ReservationRequested     []string             `json:"reservation_requested,omitempty"`
	ReservedPaths            []string             `json:"reserved_paths,omitempty"`
	ReservationIDs           []int                `json:"reservation_ids,omitempty"`
	ReservationExpiresAt     *time.Time           `json:"reservation_expires_at,omitempty"`
	ReservationError         string               `json:"reservation_error,omitempty"`
	DispatchState            DispatchState        `json:"dispatch_state,omitempty"`
	DispatchTarget           string               `json:"dispatch_target,omitempty"`
	OccupancyKey             string               `json:"occupancy_key,omitempty"`
	PromptSHA256             string               `json:"prompt_sha256,omitempty"`
	IntentSHA256             string               `json:"intent_sha256,omitempty"`
	PendingPrompt            string               `json:"pending_prompt,omitempty"`
	DispatchAttempts         int                  `json:"dispatch_attempts,omitempty"`
	DispatchStartedAt        *time.Time           `json:"dispatch_started_at,omitempty"`
	DispatchedAt             *time.Time           `json:"dispatched_at,omitempty"`
	DispatchReceiptID        string               `json:"dispatch_receipt_id,omitempty"`
	DispatchDuration         time.Duration        `json:"dispatch_duration,omitempty"`
	LastDispatchError        string               `json:"last_dispatch_error,omitempty"`
	ClearState               AssignmentClearState `json:"clear_state,omitempty"`
	ClearStartedAt           *time.Time           `json:"clear_started_at,omitempty"`
	ClearError               string               `json:"clear_error,omitempty"`
	PendingTerminalStatus    AssignmentStatus     `json:"pending_terminal_status,omitempty"`
	PendingTerminalReason    string               `json:"pending_terminal_reason,omitempty"`
	TerminalClaimReleased    bool                 `json:"terminal_claim_released,omitempty"`
	PendingCompletionEventID string               `json:"pending_completion_event_id,omitempty"`
	CompletionDetectedAt     *time.Time           `json:"completion_detected_at,omitempty"`
	CompletionConsumerToken  string               `json:"completion_consumer_token,omitempty"`
	CompletionLeaseExpiresAt *time.Time           `json:"completion_lease_expires_at,omitempty"`

	// Audit record for a dispatch_state="sending" barrier that was resolved
	// from external evidence rather than from its own transport outcome
	// (ntm#304). The reason names the concrete observation that justified the
	// resolution; it is never cleared by a later generation transition, so an
	// operator can always see why an outcome-unknown dispatch stopped being
	// unknown.
	StrandedDispatchReason     string     `json:"stranded_dispatch_reason,omitempty"`
	StrandedDispatchResolvedAt *time.Time `json:"stranded_dispatch_resolved_at,omitempty"`
}

// AssignmentUpdate describes mutable assignment metadata that can be updated
// after the initial assignment record is created.
type AssignmentUpdate struct {
	PromptSent *string
	RetryCount *int
}

// ReassignmentTarget identifies the physical pane and agent that will own the
// replacement generation of an assignment.
type ReassignmentTarget struct {
	Pane           int
	AgentType      string
	AgentName      string
	DispatchTarget string
	OccupancyKey   string
}

func normalizeFailureReason(a *Assignment) {
	if a == nil {
		return
	}
	if strings.TrimSpace(a.FailReason) == "" && strings.TrimSpace(a.FailureReason) != "" {
		a.FailReason = a.FailureReason
	}
	if strings.TrimSpace(a.FailReason) != "" {
		a.FailureReason = ""
	}
}

func cloneTimePtr(ts *time.Time) *time.Time {
	if ts == nil {
		return nil
	}
	cloned := *ts
	return &cloned
}

func cloneAssignment(a *Assignment) *Assignment {
	if a == nil {
		return nil
	}
	cloned := *a
	cloned.StartedAt = cloneTimePtr(a.StartedAt)
	cloned.CompletedAt = cloneTimePtr(a.CompletedAt)
	cloned.FailedAt = cloneTimePtr(a.FailedAt)
	cloned.ClaimedAt = cloneTimePtr(a.ClaimedAt)
	cloned.ClaimStartedAt = cloneTimePtr(a.ClaimStartedAt)
	cloned.ReservationExpiresAt = cloneTimePtr(a.ReservationExpiresAt)
	cloned.ReservationStartedAt = cloneTimePtr(a.ReservationStartedAt)
	cloned.DispatchStartedAt = cloneTimePtr(a.DispatchStartedAt)
	cloned.DispatchedAt = cloneTimePtr(a.DispatchedAt)
	cloned.ClearStartedAt = cloneTimePtr(a.ClearStartedAt)
	cloned.CompletionDetectedAt = cloneTimePtr(a.CompletionDetectedAt)
	cloned.CompletionLeaseExpiresAt = cloneTimePtr(a.CompletionLeaseExpiresAt)
	cloned.StrandedDispatchResolvedAt = cloneTimePtr(a.StrandedDispatchResolvedAt)
	cloned.ReservationRequested = append([]string(nil), a.ReservationRequested...)
	cloned.ReservationInputPaths = append([]string(nil), a.ReservationInputPaths...)
	cloned.ReservedPaths = append([]string(nil), a.ReservedPaths...)
	cloned.ReservationIDs = append([]int(nil), a.ReservationIDs...)
	normalizeFailureReason(&cloned)
	return &cloned
}

// AssignmentStore manages bead-to-agent assignments for a session
type AssignmentStore struct {
	SessionName           string                 `json:"session_name"`
	Assignments           map[string]*Assignment `json:"assignments"`                   // bead_id -> active or terminal assignment
	ClearedGenerations    map[string]uint64      `json:"cleared_generations,omitempty"` // bead_id -> completed explicit clears
	PersistenceGeneration uint64                 `json:"persistence_generation,omitempty"`
	UpdatedAt             time.Time              `json:"updated_at"`
	Version               int                    `json:"version"` // Schema version for migrations

	mutex                sync.RWMutex
	path                 string                 // Path to persistence file
	baseline             map[string]*Assignment // Last disk snapshot used to derive local deltas
	replace              map[string]struct{}    // Beads intentionally replaced as whole records
	afterBackupPublished func(*AssignmentStore) error
	completionLeaseClock func() time.Time
	completionLeaseLock  func(context.Context, string, string) (func(), error)
}

// completionLeaseNow samples the authoritative lease clock only after the
// caller has acquired the cross-process bead lock and refreshed durable state.
func (s *AssignmentStore) completionLeaseNow() (time.Time, error) {
	now := time.Now().UTC()
	if s.completionLeaseClock != nil {
		now = s.completionLeaseClock().UTC()
	}
	if now.IsZero() {
		return time.Time{}, errors.New("completion lease clock returned a zero timestamp")
	}
	return now, nil
}

func (s *AssignmentStore) acquireCompletionLeaseOperationLock(ctx context.Context, beadID string) (func(), error) {
	if s.completionLeaseLock != nil {
		return s.completionLeaseLock(ctx, s.path, beadID)
	}
	return acquireAtomicBeadOperationLock(ctx, s.path, beadID)
}

// PersistenceError represents an error during persistence operations
type PersistenceError struct {
	Operation string
	Path      string
	Cause     error
}

func (e *PersistenceError) Error() string {
	return fmt.Sprintf("[ASSIGN] %s failed at %s: %v", e.Operation, e.Path, e.Cause)
}

func (e *PersistenceError) Unwrap() error {
	return e.Cause
}

// InvalidTransitionError represents an invalid state transition
type InvalidTransitionError struct {
	BeadID string
	From   AssignmentStatus
	To     AssignmentStatus
}

// ConcurrentMutationError reports a destructive mutation based on a stale
// ledger snapshot. Retrying requires reloading and re-evaluating the latest
// assignment so a newer atomic barrier or receipt is never overwritten.
type ConcurrentMutationError struct {
	BeadID    string
	Operation string
}

func (e *ConcurrentMutationError) Error() string {
	return fmt.Sprintf("[ASSIGN] Concurrent %s conflict for %s; reload assignment state", e.Operation, e.BeadID)
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("[ASSIGN] Invalid transition %s -> %s for %s", e.From, e.To, e.BeadID)
}

// StorageDir returns the path to the assignment storage directory.
// Uses ~/.ntm/sessions/ (assignments are stored within session directories).
func StorageDir() string {
	ntmDir, err := util.NTMDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "ntm", "sessions")
	}
	return filepath.Join(ntmDir, "sessions")
}

// NewStore creates a new AssignmentStore for a session
func NewStore(sessionName string) *AssignmentStore {
	return newStore(sessionName, true)
}

func newStore(sessionName string, createSessionDir bool) *AssignmentStore {
	// Store assignments inside the session directory: ~/.ntm/sessions/<session>/assignments.json
	baseDir := StorageDir()
	sessionDir := filepath.Join(baseDir, sessionName)

	if createSessionDir {
		// Ensure session directory exists (it might not if we are just creating assignments before session save)
		_ = os.MkdirAll(sessionDir, 0700)
	}

	return &AssignmentStore{
		SessionName:           sessionName,
		Assignments:           make(map[string]*Assignment),
		ClearedGenerations:    make(map[string]uint64),
		PersistenceGeneration: 0,
		UpdatedAt:             time.Now().UTC(),
		Version:               assignmentStoreVersion,
		path:                  filepath.Join(sessionDir, assignmentsDirName+fileExtension),
		baseline:              make(map[string]*Assignment),
		replace:               make(map[string]struct{}),
	}
}

// LoadStore loads an AssignmentStore from disk, creating a new one if it doesn't exist
func LoadStore(sessionName string) (*AssignmentStore, error) {
	store := NewStore(sessionName)
	if err := store.Load(); err != nil {
		// If load fails, start fresh
		return store, nil
	}
	return store, nil
}

// LoadStoreStrict loads the durable assignment ledger without treating
// corruption as an empty store. Mutating orchestration paths must use this so
// a lost `sending` marker cannot turn an ambiguous delivery into a duplicate.
func LoadStoreStrict(sessionName string) (*AssignmentStore, error) {
	store := NewStore(sessionName)
	if err := store.LoadStrict(); err != nil {
		return nil, err
	}
	return store, nil
}

// LoadStoreStrictReadOnly returns the same fail-closed durable snapshot that a
// strict load would select without creating directories or lock files and
// without promoting a newer backup into the primary. It is intended for
// preview and inspection paths that must remain externally mutation-free.
func LoadStoreStrictReadOnly(sessionName string) (*AssignmentStore, error) {
	store := newStore(sessionName, false)
	selection, err := selectAssignmentSnapshot(store.path, store.SessionName, true)
	if err != nil {
		return nil, &PersistenceError{Operation: "read-only load", Path: store.path, Cause: err}
	}
	if selection != nil {
		store.applySnapshotLocked(selection.snapshot)
	}
	return store, nil
}

// LoadStrict is the fail-closed counterpart to Load. A corrupt primary is never
// replaced from backup because the backup may predate an external side-effect
// barrier. Strict recovery accepts only the two publication windows proven by
// durable generations: backup generation 1 before the first primary exists, or
// a backup exactly one generation ahead of a valid primary.
func (s *AssignmentStore) LoadStrict() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	unlock, err := acquireStoreFileLock(s.path)
	if err != nil {
		return &PersistenceError{Operation: "lock for load", Path: s.path, Cause: err}
	}
	defer unlock()

	selection, err := selectAssignmentSnapshot(s.path, s.SessionName, true)
	if err != nil {
		return &PersistenceError{Operation: "load", Path: s.path, Cause: err}
	}
	if selection == nil {
		return nil
	}
	if selection.promoteBackup {
		if err := publishAssignmentPrimary(s.path, selection.data); err != nil {
			return &PersistenceError{Operation: "promote newer backup", Path: s.path, Cause: err}
		}
	}
	s.applySnapshotLocked(selection.snapshot)
	return nil
}

// Load reads the assignment store from disk
func (s *AssignmentStore) Load() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	unlock, lockErr := acquireStoreFileLock(s.path)
	if lockErr != nil {
		return &PersistenceError{Operation: "lock for load", Path: s.path, Cause: lockErr}
	}
	defer unlock()

	selection, err := selectAssignmentSnapshot(s.path, s.SessionName, false)
	if err != nil {
		return &PersistenceError{Operation: "load", Path: s.path, Cause: err}
	}
	if selection == nil {
		return nil
	}
	if selection.sourcePath != s.path {
		slog.Warn("recovered assignment store from backup", "path", selection.sourcePath, "generation", selection.snapshot.PersistenceGeneration)
	}
	if selection.promoteBackup {
		if err := publishAssignmentPrimary(s.path, selection.data); err != nil {
			return &PersistenceError{Operation: "promote newer backup", Path: s.path, Cause: err}
		}
	}
	s.applySnapshotLocked(selection.snapshot)
	return nil
}

func (s *AssignmentStore) applySnapshotLocked(loaded *AssignmentStore) {
	if loaded == nil {
		return
	}
	s.SessionName = loaded.SessionName
	s.Assignments = cloneAssignmentMap(loaded.Assignments)
	s.ClearedGenerations = cloneClearedGenerationMap(loaded.ClearedGenerations)
	s.PersistenceGeneration = loaded.PersistenceGeneration
	s.UpdatedAt = loaded.UpdatedAt
	s.Version = loaded.Version
	if s.Version < assignmentStoreVersion {
		s.Version = assignmentStoreVersion
	}
	if s.Assignments == nil {
		s.Assignments = make(map[string]*Assignment)
	}
	if s.ClearedGenerations == nil {
		s.ClearedGenerations = make(map[string]uint64)
	}
	for _, assignment := range s.Assignments {
		normalizeFailureReason(assignment)
	}
	s.baseline = cloneAssignmentMap(s.Assignments)
	s.replace = make(map[string]struct{})
}

// Save writes the assignment store to disk with backup
func (s *AssignmentStore) Save() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	return s.saveLocked()
}

// saveLocked performs the actual save (must hold lock)
func (s *AssignmentStore) saveLocked() error {
	// Ensure directory exists
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return &PersistenceError{Operation: "save", Path: s.path, Cause: fmt.Errorf("create directory: %w", err)}
	}
	unlock, err := acquireStoreFileLock(s.path)
	if err != nil {
		return &PersistenceError{Operation: "lock for save", Path: s.path, Cause: err}
	}
	defer unlock()

	latestSnapshot, err := readAssignmentStateForMerge(s.path, s.SessionName)
	if err != nil {
		return &PersistenceError{Operation: "reload before save", Path: s.path, Cause: err}
	}
	latest := latestSnapshot.Assignments
	latestClearedGenerations := latestSnapshot.ClearedGenerations
	merged, mergeErr := mergeAssignmentDeltas(latest, s.baseline, s.Assignments, s.replace)
	if mergeErr != nil {
		s.Assignments = cloneAssignmentMap(latest)
		s.ClearedGenerations = cloneClearedGenerationMap(latestClearedGenerations)
		s.PersistenceGeneration = latestSnapshot.PersistenceGeneration
		s.UpdatedAt = latestSnapshot.UpdatedAt
		s.Version = latestSnapshot.Version
		s.baseline = cloneAssignmentMap(latest)
		s.replace = make(map[string]struct{})
		return &PersistenceError{Operation: "merge before save", Path: s.path, Cause: mergeErr}
	}
	mergedClearedGenerations := mergeClearedGenerations(latestClearedGenerations, s.ClearedGenerations)
	if latestSnapshot.PersistenceGeneration == ^uint64(0) {
		return &PersistenceError{Operation: "save", Path: s.path, Cause: errors.New("persistence generation exhausted")}
	}
	nextGeneration := latestSnapshot.PersistenceGeneration + 1

	updatedAt := time.Now().UTC()

	snapshot := &AssignmentStore{
		SessionName:           s.SessionName,
		Assignments:           merged,
		ClearedGenerations:    mergedClearedGenerations,
		PersistenceGeneration: nextGeneration,
		UpdatedAt:             updatedAt,
		Version:               assignmentStoreVersion,
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return &PersistenceError{Operation: "save", Path: s.path, Cause: fmt.Errorf("marshal: %w", err)}
	}

	// A unique temp file avoids collisions between independent processes even
	// before the advisory lock is acquired on unusual filesystems.
	tmpFile, err := os.CreateTemp(dir, ".assignments-*.tmp")
	if err != nil {
		return &PersistenceError{Operation: "save", Path: dir, Cause: err}
	}
	tmpPath := tmpFile.Name()
	if chmodErr := tmpFile.Chmod(0600); chmodErr != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return &PersistenceError{Operation: "save", Path: tmpPath, Cause: chmodErr}
	}
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return &PersistenceError{Operation: "save", Path: tmpPath, Cause: err}
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return &PersistenceError{Operation: "sync", Path: tmpPath, Cause: err}
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return &PersistenceError{Operation: "save", Path: tmpPath, Cause: err}
	}
	defer func() { _ = os.Remove(tmpPath) }()

	// Publish the same new snapshot to the backup before the primary. The durable
	// generation lets restart recovery recognize this exact publication window,
	// including saves that persist a receipt after an external side effect.
	bakPath := s.path + ".bak"
	backupTemp, err := os.CreateTemp(dir, ".assignments-backup-*.tmp")
	if err != nil {
		return &PersistenceError{Operation: "save backup", Path: dir, Cause: err}
	}
	backupTempPath := backupTemp.Name()
	if chmodErr := backupTemp.Chmod(0600); chmodErr != nil {
		_ = backupTemp.Close()
		_ = os.Remove(backupTempPath)
		return &PersistenceError{Operation: "save backup", Path: backupTempPath, Cause: chmodErr}
	}
	if _, writeErr := backupTemp.Write(data); writeErr != nil {
		_ = backupTemp.Close()
		_ = os.Remove(backupTempPath)
		return &PersistenceError{Operation: "save backup", Path: backupTempPath, Cause: writeErr}
	}
	if syncErr := backupTemp.Sync(); syncErr != nil {
		_ = backupTemp.Close()
		_ = os.Remove(backupTempPath)
		return &PersistenceError{Operation: "sync backup", Path: backupTempPath, Cause: syncErr}
	}
	if closeErr := backupTemp.Close(); closeErr != nil {
		_ = os.Remove(backupTempPath)
		return &PersistenceError{Operation: "save backup", Path: backupTempPath, Cause: closeErr}
	}
	defer func() { _ = os.Remove(backupTempPath) }()
	if err := os.Rename(backupTempPath, bakPath); err != nil {
		return &PersistenceError{Operation: "save backup", Path: bakPath, Cause: err}
	}
	if err := syncAssignmentDirectory(dir); err != nil {
		return &PersistenceError{Operation: "sync backup directory", Path: dir, Cause: err}
	}
	if err := s.failAfterBackupPublished(snapshot); err != nil {
		return &PersistenceError{Operation: "publish backup", Path: bakPath, Cause: err}
	}

	// Rename temp to current
	if err := os.Rename(tmpPath, s.path); err != nil {
		return &PersistenceError{Operation: "save", Path: s.path, Cause: err}
	}
	if err := syncAssignmentDirectory(dir); err != nil {
		return &PersistenceError{Operation: "sync directory", Path: dir, Cause: err}
	}
	s.Assignments = merged
	s.ClearedGenerations = mergedClearedGenerations
	s.baseline = cloneAssignmentMap(merged)
	s.replace = make(map[string]struct{})
	s.PersistenceGeneration = nextGeneration
	s.UpdatedAt = updatedAt
	s.Version = assignmentStoreVersion

	return nil
}

func cloneAssignmentMap(input map[string]*Assignment) map[string]*Assignment {
	cloned := make(map[string]*Assignment, len(input))
	for beadID, value := range input {
		cloned[beadID] = cloneAssignment(value)
	}
	return cloned
}

func cloneClearedGenerationMap(input map[string]uint64) map[string]uint64 {
	cloned := make(map[string]uint64, len(input))
	for beadID, generation := range input {
		cloned[beadID] = generation
	}
	return cloned
}

func mergeClearedGenerations(latest, current map[string]uint64) map[string]uint64 {
	merged := cloneClearedGenerationMap(latest)
	for beadID, generation := range current {
		if generation > merged[beadID] {
			merged[beadID] = generation
		}
	}
	return merged
}

type assignmentSnapshotCandidate struct {
	path     string
	snapshot *AssignmentStore
	data     []byte
	exists   bool
	err      error
}

type assignmentSnapshotSelection struct {
	snapshot      *AssignmentStore
	sourcePath    string
	data          []byte
	promoteBackup bool
}

func readAssignmentStateForMerge(path, expectedSession string) (*AssignmentStore, error) {
	selection, err := selectAssignmentSnapshot(path, expectedSession, true)
	if err != nil {
		return nil, err
	}
	if selection == nil {
		return &AssignmentStore{
			SessionName:        expectedSession,
			Assignments:        make(map[string]*Assignment),
			ClearedGenerations: make(map[string]uint64),
			Version:            assignmentStoreVersion,
		}, nil
	}
	if selection.promoteBackup {
		if err := publishAssignmentPrimary(path, selection.data); err != nil {
			return nil, fmt.Errorf("promote newer backup: %w", err)
		}
	}
	return selection.snapshot, nil
}

func selectAssignmentSnapshot(path, expectedSession string, strict bool) (*assignmentSnapshotSelection, error) {
	primary := readAssignmentSnapshotCandidate(path, expectedSession)
	backup := readAssignmentSnapshotCandidate(path+".bak", expectedSession)

	if strict {
		if primary.exists && primary.err != nil {
			return nil, fmt.Errorf("invalid primary ledger: %w", primary.err)
		}
		if backup.exists && backup.err != nil {
			return nil, fmt.Errorf("invalid backup ledger: %w", backup.err)
		}
		if !primary.exists {
			if !backup.exists {
				return nil, nil
			}
			if backup.snapshot.PersistenceGeneration != 1 {
				return nil, fmt.Errorf("primary ledger is missing while backup generation is %d, want initial generation 1", backup.snapshot.PersistenceGeneration)
			}
			return selectBackupSnapshot(backup, true), nil
		}
		if !backup.exists {
			return selectPrimarySnapshot(primary), nil
		}
		return selectAssignmentSnapshotPair(primary, backup)
	}

	if primary.exists && primary.err == nil {
		if !backup.exists || backup.err != nil {
			return selectPrimarySnapshot(primary), nil
		}
		return selectAssignmentSnapshotPair(primary, backup)
	}
	if backup.exists && backup.err == nil {
		promote := backup.snapshot.PersistenceGeneration == 1 && !primary.exists
		return selectBackupSnapshot(backup, promote), nil
	}
	if !primary.exists && !backup.exists {
		return nil, nil
	}
	if primary.err != nil {
		return nil, fmt.Errorf("invalid primary ledger: %w", primary.err)
	}
	return nil, fmt.Errorf("invalid backup ledger: %w", backup.err)
}

func selectAssignmentSnapshotPair(primary, backup assignmentSnapshotCandidate) (*assignmentSnapshotSelection, error) {
	primaryGeneration := primary.snapshot.PersistenceGeneration
	backupGeneration := backup.snapshot.PersistenceGeneration
	switch {
	case backupGeneration > primaryGeneration:
		if backupGeneration-primaryGeneration != 1 {
			return nil, fmt.Errorf("backup generation %d is not the immediate successor of primary generation %d", backupGeneration, primaryGeneration)
		}
		return selectBackupSnapshot(backup, true), nil
	case backupGeneration < primaryGeneration:
		return selectPrimarySnapshot(primary), nil
	default:
		if !bytes.Equal(primary.data, backup.data) {
			return nil, fmt.Errorf("primary and backup diverge at persistence generation %d", primaryGeneration)
		}
		return selectPrimarySnapshot(primary), nil
	}
}

func selectPrimarySnapshot(primary assignmentSnapshotCandidate) *assignmentSnapshotSelection {
	return &assignmentSnapshotSelection{
		snapshot:   primary.snapshot,
		sourcePath: primary.path,
		data:       primary.data,
	}
}

func selectBackupSnapshot(backup assignmentSnapshotCandidate, promote bool) *assignmentSnapshotSelection {
	return &assignmentSnapshotSelection{
		snapshot:      backup.snapshot,
		sourcePath:    backup.path,
		data:          backup.data,
		promoteBackup: promote,
	}
}

func readAssignmentSnapshotCandidate(path, expectedSession string) assignmentSnapshotCandidate {
	candidate := assignmentSnapshotCandidate{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return candidate
		}
		candidate.exists = true
		candidate.err = err
		return candidate
	}
	candidate.exists = true
	candidate.data = data
	candidate.snapshot, candidate.err = decodeAssignmentSnapshot(data, expectedSession)
	return candidate
}

func decodeAssignmentSnapshot(data []byte, expectedSession string) (*AssignmentStore, error) {
	var loaded AssignmentStore
	if err := json.Unmarshal(data, &loaded); err != nil {
		return nil, err
	}
	if expectedSession != "" && loaded.SessionName != expectedSession {
		return nil, fmt.Errorf("ledger session %q does not match requested session %q", loaded.SessionName, expectedSession)
	}
	if loaded.Version > assignmentStoreVersion {
		return nil, fmt.Errorf("ledger schema version %d is newer than supported version %d", loaded.Version, assignmentStoreVersion)
	}
	if loaded.Version >= assignmentStoreGenerationVersion && loaded.PersistenceGeneration == 0 {
		return nil, fmt.Errorf("schema version %d is missing persistence generation", loaded.Version)
	}
	if loaded.Version < assignmentStoreGenerationVersion && loaded.PersistenceGeneration != 0 {
		return nil, fmt.Errorf("legacy schema version %d has persistence generation %d", loaded.Version, loaded.PersistenceGeneration)
	}
	if loaded.Assignments == nil {
		loaded.Assignments = make(map[string]*Assignment)
	}
	if loaded.ClearedGenerations == nil {
		loaded.ClearedGenerations = make(map[string]uint64)
	}
	for beadID, assignment := range loaded.Assignments {
		if err := validateCompletionOutboxAssignment(assignment); err != nil {
			return nil, fmt.Errorf("assignment %q completion outbox: %w", beadID, err)
		}
		normalizeFailureReason(assignment)
	}
	return &loaded, nil
}

func validateCompletionOutboxAssignment(current *Assignment) error {
	if current == nil {
		return errors.New("assignment is null")
	}
	eventID := strings.TrimSpace(current.PendingCompletionEventID)
	consumerToken := strings.TrimSpace(current.CompletionConsumerToken)
	if current.PendingCompletionEventID != "" && eventID == "" {
		return errors.New("pending completion event ID is blank")
	}
	if current.CompletionConsumerToken != "" && consumerToken == "" {
		return errors.New("completion consumer token is blank")
	}
	hasEvent := eventID != ""
	hasDetectedAt := current.CompletionDetectedAt != nil
	if hasEvent != hasDetectedAt {
		return errors.New("pending completion event ID and detection timestamp must appear together")
	}
	if hasDetectedAt {
		if err := validateCompletionOutboxTimestamp("detection", current.CompletionDetectedAt); err != nil {
			return err
		}
		terminalStatus := current.Status == StatusCompleted || current.Status == StatusFailed
		pendingTerminalStatus := current.ClearState != ClearStateNone &&
			(current.PendingTerminalStatus == StatusCompleted || current.PendingTerminalStatus == StatusFailed)
		if !terminalStatus && !pendingTerminalStatus {
			return errors.New("pending completion event requires a terminal assignment outcome")
		}
	}

	hasConsumerToken := consumerToken != ""
	hasLeaseExpiry := current.CompletionLeaseExpiresAt != nil
	if hasConsumerToken != hasLeaseExpiry {
		return errors.New("completion consumer token and lease expiry must appear together")
	}
	if !hasConsumerToken {
		return nil
	}
	if !hasEvent {
		return errors.New("completion consumer lease requires a pending completion event")
	}
	if current.Status != StatusCompleted && current.Status != StatusFailed {
		return errors.New("completion consumer lease requires a terminal assignment status")
	}
	if current.ClearState != ClearStateNone {
		return errors.New("completion consumer lease requires completed terminal reconciliation")
	}
	return validateCompletionOutboxTimestamp("lease expiry", current.CompletionLeaseExpiresAt)
}

func validateCompletionOutboxTimestamp(name string, timestamp *time.Time) error {
	if timestamp == nil || timestamp.IsZero() {
		return fmt.Errorf("completion %s timestamp is invalid", name)
	}
	_, offset := timestamp.Zone()
	if offset != 0 {
		return fmt.Errorf("completion %s timestamp must be UTC", name)
	}
	return nil
}

func publishAssignmentPrimary(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, ".assignments-recovery-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmpFile.Chmod(0600); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return syncAssignmentDirectory(dir)
}

func (s *AssignmentStore) failAfterBackupPublished(snapshot *AssignmentStore) error {
	if s.afterBackupPublished != nil {
		if err := s.afterBackupPublished(snapshot); err != nil {
			return err
		}
	}
	beadID := strings.TrimSpace(os.Getenv(assignmentSaveFailAfterBackupEnv))
	if beadID == "" || snapshot == nil {
		return nil
	}
	assignment := snapshot.Assignments[beadID]
	if assignment == nil || assignment.DispatchState != DispatchSent || strings.TrimSpace(assignment.DispatchReceiptID) == "" {
		return nil
	}
	return fmt.Errorf("injected failure after publishing dispatch receipt for %s to backup", beadID)
}

func mergeAssignmentDeltas(latest, baseline, current map[string]*Assignment, replacements map[string]struct{}) (map[string]*Assignment, error) {
	merged := cloneAssignmentMap(latest)
	for beadID, previous := range baseline {
		if _, stillPresent := current[beadID]; !stillPresent {
			if durable, exists := latest[beadID]; exists && !reflect.DeepEqual(durable, previous) {
				return nil, &ConcurrentMutationError{BeadID: beadID, Operation: "remove"}
			}
			delete(merged, beadID)
		}
	}
	for beadID, value := range current {
		if previous, existed := baseline[beadID]; !existed || !reflect.DeepEqual(previous, value) {
			if !existed {
				if durable, createdConcurrently := latest[beadID]; createdConcurrently && !reflect.DeepEqual(durable, value) {
					return nil, &ConcurrentMutationError{BeadID: beadID, Operation: "create"}
				}
				merged[beadID] = cloneAssignment(value)
				continue
			}
			if _, replace := replacements[beadID]; replace {
				if durable, exists := latest[beadID]; !exists || !reflect.DeepEqual(durable, previous) {
					return nil, &ConcurrentMutationError{BeadID: beadID, Operation: "replace"}
				}
				merged[beadID] = cloneAssignment(value)
				continue
			}
			durable, exists := latest[beadID]
			if !exists {
				return nil, &ConcurrentMutationError{BeadID: beadID, Operation: "update removed assignment"}
			}
			if !sameAssignmentGeneration(previous, durable) {
				return nil, &ConcurrentMutationError{BeadID: beadID, Operation: "update superseded generation"}
			}
			merged[beadID] = mergeAssignmentDelta(durable, previous, value)
		}
	}
	return merged, nil
}

func sameAssignmentGeneration(previous, durable *Assignment) bool {
	if previous == nil || durable == nil {
		return false
	}
	previousKey := strings.TrimSpace(previous.IdempotencyKey)
	durableKey := strings.TrimSpace(durable.IdempotencyKey)
	if previousKey != "" || durableKey != "" {
		return previousKey != "" && previousKey == durableKey
	}
	return previous.AssignedAt.Equal(durable.AssignedAt)
}

// mergeAssignmentDelta applies only fields changed from the caller's baseline
// onto the latest durable record. This prevents a stale lifecycle writer from
// erasing a newer dispatch barrier or receipt for the same bead.
func mergeAssignmentDelta(latest, baseline, current *Assignment) *Assignment {
	if current == nil {
		return nil
	}
	if latest == nil {
		return cloneAssignment(current)
	}
	if baseline == nil {
		baseline = &Assignment{}
	}

	merged := cloneAssignment(latest)
	baselineValue := reflect.ValueOf(baseline).Elem()
	currentValue := reflect.ValueOf(current).Elem()
	mergedValue := reflect.ValueOf(merged).Elem()
	assignmentType := currentValue.Type()
	localStatusChanged := current.Status != baseline.Status

	for i := 0; i < currentValue.NumField(); i++ {
		fieldName := assignmentType.Field(i).Name
		if fieldName == "Status" {
			continue
		}
		if localStatusChanged && isAssignmentLifecycleField(fieldName) {
			continue
		}
		if !reflect.DeepEqual(baselineValue.Field(i).Interface(), currentValue.Field(i).Interface()) {
			mergedValue.Field(i).Set(currentValue.Field(i))
		}
	}

	if localStatusChanged && shouldApplyAssignmentStatusDelta(baseline.Status, latest.Status, current.Status) {
		merged.Status = current.Status
		merged.StartedAt = cloneTimePtr(current.StartedAt)
		merged.CompletedAt = cloneTimePtr(current.CompletedAt)
		merged.FailedAt = cloneTimePtr(current.FailedAt)
		merged.FailReason = current.FailReason
		merged.FailureReason = current.FailureReason
	}
	return cloneAssignment(merged)
}

func isAssignmentLifecycleField(name string) bool {
	switch name {
	case "Status", "StartedAt", "CompletedAt", "FailedAt", "FailReason", "FailureReason":
		return true
	default:
		return false
	}
}

// confirmPersistedStatusLocked verifies that a just-saved status transition
// actually survived the on-disk merge, and returns the PERSISTED record to
// clone events from.
//
// saveLocked merges this process's delta against the file another process may
// have advanced, and mergeAssignmentDelta refuses a delta whose on-disk record
// is already terminal. Callers used to mutate their in-memory pointer, call
// saveLocked, and then report success while cloning that pointer — which
// saveLocked had already orphaned by replacing s.Assignments with the merged
// map. The caller believed the transition landed and a webhook announced a
// status that exists nowhere on disk (bd-fresh-eyes-audit .15).
//
// Must be called with s.mutex held.
func (s *AssignmentStore) confirmPersistedStatusLocked(beadID string, want AssignmentStatus) (*Assignment, error) {
	persisted, ok := s.Assignments[beadID]
	if !ok {
		return nil, fmt.Errorf("%w: assignment %s disappeared during persist", ErrAssignmentStatusMismatch, beadID)
	}
	if persisted.Status != want {
		return nil, fmt.Errorf("%w: assignment %s is %s on disk, not %s (another process advanced it)",
			ErrAssignmentStatusMismatch, beadID, persisted.Status, want)
	}
	return persisted, nil
}

func shouldApplyAssignmentStatusDelta(baseline, latest, current AssignmentStatus) bool {
	if latest == baseline {
		return true
	}
	if isTerminalAssignmentStatus(latest) {
		return false
	}
	if isTerminalAssignmentStatus(current) {
		return true
	}
	return assignmentStatusRank(current) > assignmentStatusRank(latest)
}

func isTerminalAssignmentStatus(status AssignmentStatus) bool {
	switch status {
	case StatusCompleted, StatusFailed, StatusReassigned:
		return true
	default:
		return false
	}
}

func assignmentStatusRank(status AssignmentStatus) int {
	switch status {
	case StatusClaiming:
		return 1
	case StatusClaimed:
		return 2
	case StatusAssigned:
		return 3
	case StatusWorking:
		return 4
	case StatusCompleted, StatusFailed, StatusReassigned:
		return 5
	default:
		return 0
	}
}

// Assign creates or updates an assignment for a bead
func (s *AssignmentStore) Assign(beadID, beadTitle string, pane int, agentType, agentName, prompt string) (*Assignment, error) {
	s.mutex.Lock()
	if _, exists := s.Assignments[beadID]; exists {
		if s.replace == nil {
			s.replace = make(map[string]struct{})
		}
		s.replace[beadID] = struct{}{}
	}

	now := time.Now().UTC()
	assignment := &Assignment{
		BeadID:     beadID,
		BeadTitle:  beadTitle,
		Pane:       pane,
		AgentType:  agentType,
		AgentName:  agentName,
		Status:     StatusAssigned,
		AssignedAt: now,
		PromptSent: prompt,
	}

	s.Assignments[beadID] = assignment

	// Persist immediately
	if err := s.saveLocked(); err != nil {
		s.mutex.Unlock()
		return nil, err
	}

	persisted, err := s.confirmPersistedStatusLocked(beadID, StatusAssigned)
	if err != nil {
		s.mutex.Unlock()
		return nil, err
	}
	cloned := cloneAssignment(persisted)
	s.mutex.Unlock()

	events.DefaultEmitter().Emit(events.NewWebhookEvent(
		events.WebhookBeadAssigned,
		s.SessionName,
		fmt.Sprintf("%d", pane),
		agentType,
		fmt.Sprintf("Bead assigned: %s", beadID),
		map[string]string{
			"bead_id":     beadID,
			"bead_title":  beadTitle,
			"pane_index":  fmt.Sprintf("%d", pane),
			"agent_type":  agentType,
			"agent_name":  agentName,
			"retry_count": fmt.Sprintf("%d", cloned.RetryCount),
		},
	))

	return cloned, nil
}

// Get retrieves an assignment by bead ID
func (s *AssignmentStore) Get(beadID string) *Assignment {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return cloneAssignment(s.Assignments[beadID])
}

// ClearedGeneration returns the number of completed explicit clears for a
// bead. It is retained after the assignment row is removed so a deliberate
// post-clear assignment can derive a fresh, replay-stable identity.
func (s *AssignmentStore) ClearedGeneration(beadID string) uint64 {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.ClearedGenerations[beadID]
}

// GetAll returns all assignments as values
func (s *AssignmentStore) GetAll() []Assignment {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var result []Assignment
	for _, a := range s.Assignments {
		result = append(result, *cloneAssignment(a))
	}
	return result
}

// List returns all assignments
func (s *AssignmentStore) List() []*Assignment {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	result := make([]*Assignment, 0, len(s.Assignments))
	for _, a := range s.Assignments {
		result = append(result, cloneAssignment(a))
	}
	return result
}

// ListByStatus returns all assignments with a specific status
func (s *AssignmentStore) ListByStatus(status AssignmentStatus) []*Assignment {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var result []*Assignment
	for _, a := range s.Assignments {
		if a.Status == status {
			result = append(result, cloneAssignment(a))
		}
	}
	return result
}

// ListActive returns all assignments that are assigned or working
func (s *AssignmentStore) ListActive() []*Assignment {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var result []*Assignment
	for _, a := range s.Assignments {
		if a.ClearState != ClearStateNone || a.Status == StatusClaiming || a.Status == StatusClaimed || a.Status == StatusAssigned || a.Status == StatusWorking {
			result = append(result, cloneAssignment(a))
		}
	}
	return result
}

// BeginClear persists a cross-process barrier before external reservation
// release. The barrier retains exact reservation IDs and blocks new assignment
// generations until CompleteClear removes the record.
func (s *AssignmentStore) BeginClear(ctx context.Context, beadID string, startedAt time.Time) (*Assignment, error) {
	return s.beginClear(ctx, beadID, startedAt, false, nil)
}

// BeginClearForce establishes the clear barrier even when dispatch is stuck at
// the outcome-unknown sending boundary. This is an explicit operator escape
// hatch (ntm assign --clear --force): the original delivery may or may not
// have happened, but the durable claim, leases, and pane occupancy can still
// be released so the bead and pane do not stay occupied forever. The dispatch
// state is returned to pending so a later retry is possible.
func (s *AssignmentStore) BeginClearForce(ctx context.Context, beadID string, startedAt time.Time) (*Assignment, error) {
	return s.beginClear(ctx, beadID, startedAt, true, nil)
}

// BeginClearIfStatus establishes the clear barrier only if the status still
// matches one of expected after acquiring the bead lock and reloading durable
// state. This closes filter-then-clear races such as --clear-failed clearing a
// concurrently retried assignment.
func (s *AssignmentStore) BeginClearIfStatus(ctx context.Context, beadID string, startedAt time.Time, expected ...AssignmentStatus) (*Assignment, error) {
	if len(expected) == 0 {
		return nil, errors.New("at least one expected assignment status is required")
	}
	return s.beginClear(ctx, beadID, startedAt, false, expected)
}

// BeginClearForceIfStatus is the status-guarded variant of BeginClearForce.
func (s *AssignmentStore) BeginClearForceIfStatus(ctx context.Context, beadID string, startedAt time.Time, expected ...AssignmentStatus) (*Assignment, error) {
	if len(expected) == 0 {
		return nil, errors.New("at least one expected assignment status is required")
	}
	return s.beginClear(ctx, beadID, startedAt, true, expected)
}

// BeginTerminalReconciliationIfCurrent durably records the desired terminal
// outcome on the exact assignment generation observed by the caller. The clear
// barrier is persisted before any external lease or claim release may run. It
// does not create a completion outbox entry because most reconciliation callers
// have no event consumer that can acknowledge one.
func (s *AssignmentStore) BeginTerminalReconciliationIfCurrent(ctx context.Context, observed *Assignment, terminalStatus AssignmentStatus, reason string) (*Assignment, bool, error) {
	return s.beginTerminalReconciliationIfCurrent(ctx, observed, terminalStatus, reason, false)
}

// BeginTerminalReconciliationWithCompletionEventIfCurrent establishes the same
// exact-generation barrier and also creates a durable completion outbox entry.
// Only callers with an acknowledge-capable completion-event consumer may use it.
func (s *AssignmentStore) BeginTerminalReconciliationWithCompletionEventIfCurrent(ctx context.Context, observed *Assignment, terminalStatus AssignmentStatus, reason string) (*Assignment, bool, error) {
	return s.beginTerminalReconciliationIfCurrent(ctx, observed, terminalStatus, reason, true)
}

func (s *AssignmentStore) beginTerminalReconciliationIfCurrent(ctx context.Context, observed *Assignment, terminalStatus AssignmentStatus, reason string, createCompletionEvent bool) (*Assignment, bool, error) {
	if observed == nil || strings.TrimSpace(observed.BeadID) == "" {
		return nil, false, errors.New("observed assignment generation is required")
	}
	if terminalStatus != StatusCompleted && terminalStatus != StatusFailed {
		return nil, false, fmt.Errorf("terminal reconciliation status must be completed or failed, got %s", terminalStatus)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	operationUnlock, err := acquireAtomicBeadOperationLock(ctx, s.path, observed.BeadID)
	if err != nil {
		return nil, false, fmt.Errorf("lock terminal assignment reconciliation %s: %w", observed.BeadID, err)
	}
	defer operationUnlock()
	if err := s.LoadStrict(); err != nil {
		return nil, false, fmt.Errorf("refresh terminal assignment reconciliation %s: %w", observed.BeadID, err)
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()
	current := s.Assignments[observed.BeadID]
	if !SameAssignmentGeneration(observed, current) {
		return nil, false, nil
	}
	if current.DispatchState == DispatchSending {
		return nil, false, fmt.Errorf("%w: cannot retire %s while dispatch outcome is unknown", ErrDispatchOutcomeUnknown, observed.BeadID)
	}
	ensureCompletionEvent := func() (bool, error) {
		if !createCompletionEvent || strings.TrimSpace(current.PendingCompletionEventID) != "" {
			return true, nil
		}
		detectedAt := time.Now().UTC()
		previous := cloneAssignment(current)
		current.PendingCompletionEventID = terminalCompletionEventID(current, terminalStatus)
		current.CompletionDetectedAt = cloneTimePtr(&detectedAt)
		current.CompletionConsumerToken = ""
		current.CompletionLeaseExpiresAt = nil
		if s.replace == nil {
			s.replace = make(map[string]struct{})
		}
		s.replace[observed.BeadID] = struct{}{}
		if err := s.saveLocked(); err != nil {
			var concurrentMutation *ConcurrentMutationError
			if errors.As(err, &concurrentMutation) {
				return false, nil
			}
			s.Assignments[observed.BeadID] = previous
			delete(s.replace, observed.BeadID)
			return false, err
		}
		return true, nil
	}
	if current.ClearState != ClearStateNone {
		if current.PendingTerminalStatus == terminalStatus && current.PendingTerminalReason == reason {
			if persisted, err := ensureCompletionEvent(); err != nil || !persisted {
				return nil, false, err
			}
			return cloneAssignment(current), true, nil
		}
		return nil, false, nil
	}
	if createCompletionEvent && current.Status == terminalStatus {
		if terminalStatus == StatusFailed && current.FailReason != reason {
			return nil, false, nil
		}
		if persisted, err := ensureCompletionEvent(); err != nil || !persisted {
			return nil, false, err
		}
		return cloneAssignment(current), true, nil
	}
	if current.Status != StatusClaiming && current.Status != StatusClaimed && current.Status != StatusAssigned && current.Status != StatusWorking {
		return nil, false, nil
	}

	startedAt := time.Now().UTC()
	previous := cloneAssignment(current)
	current.ClearState = ClearStateReservationReleasing
	current.ClearStartedAt = &startedAt
	current.ClearError = ""
	current.PendingTerminalStatus = terminalStatus
	current.PendingTerminalReason = reason
	current.TerminalClaimReleased = false
	current.PendingCompletionEventID = ""
	current.CompletionDetectedAt = nil
	current.CompletionConsumerToken = ""
	current.CompletionLeaseExpiresAt = nil
	if createCompletionEvent {
		current.PendingCompletionEventID = terminalCompletionEventID(current, terminalStatus)
		current.CompletionDetectedAt = cloneTimePtr(&startedAt)
	}
	if s.replace == nil {
		s.replace = make(map[string]struct{})
	}
	s.replace[observed.BeadID] = struct{}{}
	if err := s.saveLocked(); err != nil {
		var concurrentMutation *ConcurrentMutationError
		if errors.As(err, &concurrentMutation) {
			return nil, false, nil
		}
		s.Assignments[observed.BeadID] = previous
		delete(s.replace, observed.BeadID)
		return nil, false, err
	}
	return cloneAssignment(current), true, nil
}

func (s *AssignmentStore) beginClear(ctx context.Context, beadID string, startedAt time.Time, forceOutcomeUnknown bool, expected []AssignmentStatus) (*Assignment, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	operationUnlock, err := acquireAtomicBeadOperationLock(ctx, s.path, beadID)
	if err != nil {
		return nil, fmt.Errorf("lock assignment clear %s: %w", beadID, err)
	}
	defer operationUnlock()
	if err := s.LoadStrict(); err != nil {
		return nil, fmt.Errorf("refresh assignment clear %s: %w", beadID, err)
	}
	return s.beginClearWithOperationLock(beadID, startedAt, forceOutcomeUnknown, expected)
}

// beginClearWithOperationLock mutates clear state while the caller owns the
// bead operation lock and has refreshed the store. Atomic replacement uses it
// to avoid recursively acquiring the same cross-process lock.
func (s *AssignmentStore) beginClearWithOperationLock(beadID string, startedAt time.Time, forceOutcomeUnknown bool, expected []AssignmentStatus) (*Assignment, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	assignment := s.Assignments[beadID]
	if assignment == nil {
		return nil, fmt.Errorf("[ASSIGN] Assignment not found: %s", beadID)
	}
	if len(expected) > 0 && !assignmentStatusAllowed(assignment.Status, expected) {
		return nil, fmt.Errorf("%w: %s is %s, expected %s", ErrAssignmentStatusMismatch, beadID, assignment.Status, formatAssignmentStatuses(expected))
	}
	if assignment.DispatchState == DispatchSending && !forceOutcomeUnknown {
		return nil, fmt.Errorf("%w: cannot clear %s while dispatch outcome is unknown", ErrDispatchOutcomeUnknown, beadID)
	}
	if strings.TrimSpace(assignment.PendingCompletionEventID) != "" {
		return nil, fmt.Errorf("%w: cannot clear %s before event %s is acknowledged", ErrCompletionEventPending, beadID, assignment.PendingCompletionEventID)
	}
	if assignment.ClearState != ClearStateNone {
		return cloneAssignment(assignment), nil
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	previous := cloneAssignment(assignment)
	if assignment.DispatchState == DispatchSending {
		// Force-clear only: terminalize the outcome-unknown dispatch barrier
		// so the release below can proceed and a later retry stays possible.
		assignment.DispatchState = DispatchPending
		assignment.LastDispatchError = "force-cleared after dispatch outcome became unknown"
	}
	assignment.ClearState = ClearStateReservationReleasing
	assignment.ClearStartedAt = &startedAt
	assignment.ClearError = ""
	if s.replace == nil {
		s.replace = make(map[string]struct{})
	}
	s.replace[beadID] = struct{}{}
	if err := s.saveLocked(); err != nil {
		var concurrentMutation *ConcurrentMutationError
		if !errors.As(err, &concurrentMutation) {
			s.Assignments[beadID] = previous
			delete(s.replace, beadID)
		}
		return nil, err
	}
	return cloneAssignment(assignment), nil
}

func assignmentStatusAllowed(actual AssignmentStatus, expected []AssignmentStatus) bool {
	for _, status := range expected {
		if actual == status {
			return true
		}
	}
	return false
}

func formatAssignmentStatuses(statuses []AssignmentStatus) string {
	values := make([]string, 0, len(statuses))
	for _, status := range statuses {
		values = append(values, string(status))
	}
	return strings.Join(values, ",")
}

// RecordClearReleaseFailed preserves a retryable clear barrier and diagnostic.
func (s *AssignmentStore) RecordClearReleaseFailed(ctx context.Context, beadID string, releaseErr error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	operationUnlock, err := acquireAtomicBeadOperationLock(ctx, s.path, beadID)
	if err != nil {
		return fmt.Errorf("lock assignment clear failure %s: %w", beadID, err)
	}
	defer operationUnlock()
	if err := s.LoadStrict(); err != nil {
		return fmt.Errorf("refresh assignment clear failure %s: %w", beadID, err)
	}
	return s.recordClearReleaseFailedWithOperationLock(beadID, releaseErr)
}

// recordClearReleaseFailedWithOperationLock records a retry diagnostic while
// the caller owns the bead operation lock and has refreshed the store.
func (s *AssignmentStore) recordClearReleaseFailedWithOperationLock(beadID string, releaseErr error) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	assignment := s.Assignments[beadID]
	if assignment == nil {
		return nil
	}
	if assignment.ClearState == ClearStateNone {
		return fmt.Errorf("assignment %s is not awaiting reservation release", beadID)
	}
	previous := cloneAssignment(assignment)
	assignment.ClearError = ""
	if releaseErr != nil {
		assignment.ClearError = releaseErr.Error()
	}
	if s.replace == nil {
		s.replace = make(map[string]struct{})
	}
	s.replace[beadID] = struct{}{}
	if err := s.saveLocked(); err != nil {
		var concurrentMutation *ConcurrentMutationError
		if !errors.As(err, &concurrentMutation) {
			s.Assignments[beadID] = previous
			delete(s.replace, beadID)
		}
		return err
	}
	return nil
}

// RecordClearLeasesReleased durably records that every matching external lease
// is absent. Later clear retries can skip the non-idempotent release call while
// still retrying tracker-claim release and local ledger completion.
func (s *AssignmentStore) RecordClearLeasesReleased(ctx context.Context, beadID string) (*Assignment, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	operationUnlock, err := acquireAtomicBeadOperationLock(ctx, s.path, beadID)
	if err != nil {
		return nil, fmt.Errorf("lock assignment lease-release completion %s: %w", beadID, err)
	}
	defer operationUnlock()
	if err := s.LoadStrict(); err != nil {
		return nil, fmt.Errorf("refresh assignment lease-release completion %s: %w", beadID, err)
	}
	return s.recordClearLeasesReleasedWithOperationLock(beadID)
}

// RecordTerminalClaimReleased durably checkpoints that the exact Beads claim
// recorded on a pending terminal assignment is no longer externally owned.
func (s *AssignmentStore) RecordTerminalClaimReleased(ctx context.Context, beadID string) (*Assignment, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	operationUnlock, err := acquireAtomicBeadOperationLock(ctx, s.path, beadID)
	if err != nil {
		return nil, fmt.Errorf("lock assignment claim-release completion %s: %w", beadID, err)
	}
	defer operationUnlock()
	if err := s.LoadStrict(); err != nil {
		return nil, fmt.Errorf("refresh assignment claim-release completion %s: %w", beadID, err)
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()
	current := s.Assignments[beadID]
	if current == nil {
		return nil, fmt.Errorf("[ASSIGN] Assignment not found: %s", beadID)
	}
	if current.PendingTerminalStatus != StatusCompleted && current.PendingTerminalStatus != StatusFailed {
		return nil, fmt.Errorf("assignment %s is not awaiting terminal reconciliation", beadID)
	}
	if current.ClearState != ClearStateLeasesReleased {
		return nil, fmt.Errorf("assignment %s has not durably completed reservation release", beadID)
	}
	if current.TerminalClaimReleased {
		return cloneAssignment(current), nil
	}
	previous := cloneAssignment(current)
	current.TerminalClaimReleased = true
	current.ClearError = ""
	if s.replace == nil {
		s.replace = make(map[string]struct{})
	}
	s.replace[beadID] = struct{}{}
	if err := s.saveLocked(); err != nil {
		var concurrentMutation *ConcurrentMutationError
		if !errors.As(err, &concurrentMutation) {
			s.Assignments[beadID] = previous
			delete(s.replace, beadID)
		}
		return nil, err
	}
	return cloneAssignment(current), nil
}

// recordClearLeasesReleasedWithOperationLock persists the release checkpoint
// while the caller owns the bead operation lock and has refreshed the store.
func (s *AssignmentStore) recordClearLeasesReleasedWithOperationLock(beadID string) (*Assignment, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	assignment := s.Assignments[beadID]
	if assignment == nil {
		return nil, fmt.Errorf("[ASSIGN] Assignment not found: %s", beadID)
	}
	if assignment.ClearState == ClearStateLeasesReleased {
		return cloneAssignment(assignment), nil
	}
	if assignment.ClearState != ClearStateReservationReleasing {
		return nil, fmt.Errorf("assignment %s is not awaiting reservation release", beadID)
	}
	previous := cloneAssignment(assignment)
	assignment.ClearState = ClearStateLeasesReleased
	assignment.ClearError = ""
	assignment.ReservationState = ReservationReleased
	assignment.ReservationCompleted = false
	assignment.ReservedPaths = nil
	assignment.ReservationIDs = nil
	assignment.ReservationExpiresAt = nil
	assignment.ReservationError = ""
	if s.replace == nil {
		s.replace = make(map[string]struct{})
	}
	s.replace[beadID] = struct{}{}
	if err := s.saveLocked(); err != nil {
		var concurrentMutation *ConcurrentMutationError
		if !errors.As(err, &concurrentMutation) {
			s.Assignments[beadID] = previous
			delete(s.replace, beadID)
		}
		return nil, err
	}
	return cloneAssignment(assignment), nil
}

// CompleteClear removes an assignment only after its external reservations are
// confirmed released. Missing records are an idempotent success for racers.
func (s *AssignmentStore) CompleteClear(ctx context.Context, beadID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	operationUnlock, err := acquireAtomicBeadOperationLock(ctx, s.path, beadID)
	if err != nil {
		return fmt.Errorf("lock assignment clear completion %s: %w", beadID, err)
	}
	defer operationUnlock()
	if err := s.LoadStrict(); err != nil {
		return fmt.Errorf("refresh assignment clear completion %s: %w", beadID, err)
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()
	assignment := s.Assignments[beadID]
	if assignment == nil {
		return nil
	}
	if strings.TrimSpace(assignment.PendingCompletionEventID) != "" {
		return fmt.Errorf("%w: cannot complete clear for %s before event %s is acknowledged", ErrCompletionEventPending, beadID, assignment.PendingCompletionEventID)
	}
	if assignment.ClearState != ClearStateLeasesReleased {
		return fmt.Errorf("assignment %s has not durably completed reservation release", beadID)
	}
	if s.ClearedGenerations == nil {
		s.ClearedGenerations = make(map[string]uint64)
	}
	s.ClearedGenerations[beadID]++
	delete(s.Assignments, beadID)
	return s.saveLocked()
}

// CompleteTerminalReconciliation retires tracker-terminal work only after the
// caller has proven every external reservation and exact claim released.
// BeginTerminalReconciliationIfCurrent establishes the barrier consumed here.
func (s *AssignmentStore) CompleteTerminalReconciliation(ctx context.Context, beadID string, terminalStatus AssignmentStatus, reason string) error {
	if terminalStatus != StatusCompleted && terminalStatus != StatusFailed {
		return fmt.Errorf("terminal reconciliation status must be completed or failed, got %s", terminalStatus)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	operationUnlock, err := acquireAtomicBeadOperationLock(ctx, s.path, beadID)
	if err != nil {
		return fmt.Errorf("lock terminal assignment reconciliation %s: %w", beadID, err)
	}
	defer operationUnlock()
	if err := s.LoadStrict(); err != nil {
		return fmt.Errorf("refresh terminal assignment reconciliation %s: %w", beadID, err)
	}

	s.mutex.Lock()
	assignment := s.Assignments[beadID]
	if assignment == nil {
		s.mutex.Unlock()
		return nil
	}
	if assignment.ClearState != ClearStateLeasesReleased {
		s.mutex.Unlock()
		return fmt.Errorf("assignment %s has not durably completed reservation release", beadID)
	}
	if assignment.PendingTerminalStatus != terminalStatus || assignment.PendingTerminalReason != reason {
		s.mutex.Unlock()
		return fmt.Errorf("assignment %s terminal outcome changed: pending %s %q, requested %s %q", beadID, assignment.PendingTerminalStatus, assignment.PendingTerminalReason, terminalStatus, reason)
	}
	if !assignment.TerminalClaimReleased {
		s.mutex.Unlock()
		return fmt.Errorf("assignment %s has not durably completed claim release", beadID)
	}
	if assignment.DispatchState == DispatchSending {
		s.mutex.Unlock()
		return fmt.Errorf("%w: cannot retire %s while dispatch outcome is unknown", ErrDispatchOutcomeUnknown, beadID)
	}

	previousStatus := assignment.Status
	now := time.Now().UTC()
	assignment.Status = terminalStatus
	assignment.ReservationState = ReservationReleased
	assignment.ReservationCompleted = false
	assignment.ReservedPaths = nil
	assignment.ReservationIDs = nil
	assignment.ReservationExpiresAt = nil
	assignment.ReservationError = ""
	assignment.ClearState = ClearStateNone
	assignment.ClearStartedAt = nil
	assignment.ClearError = ""
	assignment.PendingTerminalStatus = ""
	assignment.PendingTerminalReason = ""
	assignment.TerminalClaimReleased = false
	assignment.CompletedAt = nil
	assignment.FailedAt = nil
	assignment.FailReason = ""
	assignment.FailureReason = ""
	if terminalStatus == StatusCompleted {
		assignment.CompletedAt = &now
	} else {
		assignment.FailedAt = &now
		assignment.FailReason = reason
	}
	if err := s.saveLocked(); err != nil {
		s.mutex.Unlock()
		return err
	}
	emitIdle := s.shouldEmitAgentIdleLocked(assignment, previousStatus, terminalStatus)
	cloned := cloneAssignment(assignment)
	s.mutex.Unlock()

	emitAssignmentStatusEvent(s.SessionName, cloned, terminalStatus, reason)
	if emitIdle {
		emitAgentIdle(s.SessionName, cloned, previousStatus, terminalStatus)
	}
	return nil
}

// ListPendingCompletionEvents returns terminal rows whose completion event has
// not yet been acknowledged by the consumer. The durable row is an outbox:
// enqueue may repeat after a restart, but a completion cannot be lost between
// terminal reconciliation and in-memory delivery.
func (s *AssignmentStore) ListPendingCompletionEvents() []*Assignment {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	result := make([]*Assignment, 0)
	for _, current := range s.Assignments {
		if current == nil || strings.TrimSpace(current.PendingCompletionEventID) == "" || current.ClearState != ClearStateNone {
			continue
		}
		if current.Status != StatusCompleted && current.Status != StatusFailed {
			continue
		}
		result = append(result, cloneAssignment(current))
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		leftDetectedAt, rightDetectedAt := time.Time{}, time.Time{}
		if left.CompletionDetectedAt != nil {
			leftDetectedAt = *left.CompletionDetectedAt
		}
		if right.CompletionDetectedAt != nil {
			rightDetectedAt = *right.CompletionDetectedAt
		}
		if !leftDetectedAt.Equal(rightDetectedAt) {
			return leftDetectedAt.Before(rightDetectedAt)
		}
		if left.BeadID != right.BeadID {
			return left.BeadID < right.BeadID
		}
		return left.PendingCompletionEventID < right.PendingCompletionEventID
	})
	return result
}

// ClaimPendingCompletionEvent acquires the durable consumer lease for one
// exact outbox generation. A different live lease is a clean non-acquisition;
// an expired lease may be taken over so a crashed consumer cannot strand work.
func (s *AssignmentStore) ClaimPendingCompletionEvent(ctx context.Context, beadID, eventID, consumerToken string, leaseDuration time.Duration) (*Assignment, bool, error) {
	beadID = strings.TrimSpace(beadID)
	eventID = strings.TrimSpace(eventID)
	consumerToken = strings.TrimSpace(consumerToken)
	if beadID == "" || eventID == "" || consumerToken == "" {
		return nil, false, errors.New("bead ID, completion event ID, and consumer token are required")
	}
	if leaseDuration <= 0 {
		return nil, false, errors.New("completion lease duration must be positive")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	operationUnlock, err := s.acquireCompletionLeaseOperationLock(ctx, beadID)
	if err != nil {
		return nil, false, fmt.Errorf("lock completion event claim %s: %w", beadID, err)
	}
	defer operationUnlock()
	if err := s.LoadStrict(); err != nil {
		return nil, false, fmt.Errorf("refresh completion event claim %s: %w", beadID, err)
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()
	now, err := s.completionLeaseNow()
	if err != nil {
		return nil, false, err
	}
	current := s.Assignments[beadID]
	if current == nil || strings.TrimSpace(current.PendingCompletionEventID) != eventID {
		return nil, false, nil
	}
	if current.Status != StatusCompleted && current.Status != StatusFailed {
		return nil, false, fmt.Errorf("assignment %s completion event is not terminal", beadID)
	}
	existingToken := strings.TrimSpace(current.CompletionConsumerToken)
	if (existingToken == "") != (current.CompletionLeaseExpiresAt == nil) {
		return nil, false, fmt.Errorf("assignment %s completion event has an incomplete consumer lease", beadID)
	}
	if existingToken != "" && now.Before(*current.CompletionLeaseExpiresAt) {
		if existingToken != consumerToken {
			return cloneAssignment(current), false, nil
		}
		return cloneAssignment(current), true, nil
	}

	expiresAt := now.Add(leaseDuration)
	previous := cloneAssignment(current)
	current.CompletionConsumerToken = consumerToken
	current.CompletionLeaseExpiresAt = &expiresAt
	if s.replace == nil {
		s.replace = make(map[string]struct{})
	}
	s.replace[beadID] = struct{}{}
	if err := s.saveLocked(); err != nil {
		s.Assignments[beadID] = previous
		delete(s.replace, beadID)
		return nil, false, err
	}
	return cloneAssignment(current), true, nil
}

// RenewPendingCompletionEventLease extends a live lease only for its exact
// owner and event generation. Once the durable expiry passes, renewal is
// refused so recovery consumers have an unambiguous takeover boundary.
func (s *AssignmentStore) RenewPendingCompletionEventLease(ctx context.Context, beadID, eventID, consumerToken string, leaseDuration time.Duration) (bool, error) {
	beadID = strings.TrimSpace(beadID)
	eventID = strings.TrimSpace(eventID)
	consumerToken = strings.TrimSpace(consumerToken)
	if beadID == "" || eventID == "" || consumerToken == "" {
		return false, errors.New("bead ID, completion event ID, and consumer token are required")
	}
	if leaseDuration <= 0 {
		return false, errors.New("completion lease duration must be positive")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	operationUnlock, err := s.acquireCompletionLeaseOperationLock(ctx, beadID)
	if err != nil {
		return false, fmt.Errorf("lock completion event lease renewal %s: %w", beadID, err)
	}
	defer operationUnlock()
	if err := s.LoadStrict(); err != nil {
		return false, fmt.Errorf("refresh completion event lease renewal %s: %w", beadID, err)
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()
	now, err := s.completionLeaseNow()
	if err != nil {
		return false, err
	}
	current := s.Assignments[beadID]
	if current == nil || strings.TrimSpace(current.PendingCompletionEventID) != eventID ||
		strings.TrimSpace(current.CompletionConsumerToken) != consumerToken {
		return false, nil
	}
	if current.CompletionLeaseExpiresAt == nil {
		return false, fmt.Errorf("assignment %s completion event has no lease expiry", beadID)
	}
	if !now.Before(*current.CompletionLeaseExpiresAt) {
		return false, nil
	}

	expiresAt := now.Add(leaseDuration)
	previous := cloneAssignment(current)
	current.CompletionLeaseExpiresAt = &expiresAt
	if s.replace == nil {
		s.replace = make(map[string]struct{})
	}
	s.replace[beadID] = struct{}{}
	if err := s.saveLocked(); err != nil {
		s.Assignments[beadID] = previous
		delete(s.replace, beadID)
		return false, err
	}
	return true, nil
}

func ownsLiveCompletionEventLease(current *Assignment, eventID, consumerToken string, now time.Time) bool {
	return current != nil &&
		strings.TrimSpace(current.PendingCompletionEventID) == eventID &&
		strings.TrimSpace(current.CompletionConsumerToken) == consumerToken &&
		current.CompletionLeaseExpiresAt != nil &&
		current.CompletionLeaseExpiresAt.After(now.UTC())
}

// AcknowledgeCompletionEvent clears the exact outbox generation only after its
// lease-owning consumer finishes handling it. A stale acknowledgement cannot
// clear a newer assignment generation's event or another consumer's lease.
func (s *AssignmentStore) AcknowledgeCompletionEvent(ctx context.Context, beadID, eventID, consumerToken string) (bool, error) {
	beadID = strings.TrimSpace(beadID)
	eventID = strings.TrimSpace(eventID)
	consumerToken = strings.TrimSpace(consumerToken)
	if beadID == "" || eventID == "" || consumerToken == "" {
		return false, errors.New("bead ID, completion event ID, and consumer token are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(os.Getenv(completionAckFailOnceEnv)) == beadID {
		failureKey := beadID + "\x00" + eventID
		if _, alreadyFailed := completionAckFailures.LoadOrStore(failureKey, struct{}{}); !alreadyFailed {
			return false, fmt.Errorf("injected one-shot completion acknowledgement failure for %s", beadID)
		}
	}
	operationUnlock, err := s.acquireCompletionLeaseOperationLock(ctx, beadID)
	if err != nil {
		return false, fmt.Errorf("lock completion event acknowledgement %s: %w", beadID, err)
	}
	defer operationUnlock()
	if err := s.LoadStrict(); err != nil {
		return false, fmt.Errorf("refresh completion event acknowledgement %s: %w", beadID, err)
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()
	now, err := s.completionLeaseNow()
	if err != nil {
		return false, err
	}
	current := s.Assignments[beadID]
	if !ownsLiveCompletionEventLease(current, eventID, consumerToken, now) {
		return false, nil
	}
	if current.Status != StatusCompleted && current.Status != StatusFailed {
		return false, fmt.Errorf("assignment %s completion event is not terminal", beadID)
	}
	previous := cloneAssignment(current)
	current.PendingCompletionEventID = ""
	current.CompletionDetectedAt = nil
	current.CompletionConsumerToken = ""
	current.CompletionLeaseExpiresAt = nil
	if s.replace == nil {
		s.replace = make(map[string]struct{})
	}
	s.replace[beadID] = struct{}{}
	if err := s.saveLocked(); err != nil {
		s.Assignments[beadID] = previous
		delete(s.replace, beadID)
		return false, err
	}
	return true, nil
}

// Update updates mutable assignment metadata while preserving snapshot semantics
// for store callers.
func (s *AssignmentStore) Update(beadID string, update AssignmentUpdate) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	assignment, ok := s.Assignments[beadID]
	if !ok {
		return fmt.Errorf("[ASSIGN] Assignment not found: %s", beadID)
	}

	changed := false
	if update.PromptSent != nil && assignment.PromptSent != *update.PromptSent {
		assignment.PromptSent = *update.PromptSent
		changed = true
	}
	if update.RetryCount != nil && assignment.RetryCount != *update.RetryCount {
		assignment.RetryCount = *update.RetryCount
		changed = true
	}
	if !changed {
		return nil
	}

	if err := s.saveLocked(); err != nil {
		return err
	}

	return nil
}

// ValidTransitions defines valid state transitions.
//
// `StatusAssigned -> StatusCompleted` is permitted because beads can be closed
// externally (via `br close` from another agent, or by the assigned agent
// before the assignment store observed a `working` transition). The watch
// loop correlates closures back into the store after the fact and would
// otherwise drop those completions on the floor with an "invalid transition"
// warning, leaving the assignment forever stuck in "assigned" (#124).
var ValidTransitions = map[AssignmentStatus][]AssignmentStatus{
	StatusClaimed:    {StatusAssigned, StatusFailed},
	StatusAssigned:   {StatusWorking, StatusCompleted, StatusFailed},
	StatusWorking:    {StatusCompleted, StatusFailed, StatusReassigned},
	StatusFailed:     {StatusAssigned}, // Retry
	StatusCompleted:  {},               // Terminal
	StatusReassigned: {},               // Terminal (new assignment created)
}

// isValidTransition checks if a state transition is valid
func isValidTransition(from, to AssignmentStatus) bool {
	validTargets, ok := ValidTransitions[from]
	if !ok {
		return false
	}
	for _, valid := range validTargets {
		if valid == to {
			return true
		}
	}
	return false
}

// UpdateStatus changes the status of an assignment with validation
func (s *AssignmentStore) UpdateStatus(beadID string, newStatus AssignmentStatus) error {
	s.mutex.Lock()

	assignment, ok := s.Assignments[beadID]
	if !ok {
		s.mutex.Unlock()
		return fmt.Errorf("[ASSIGN] Assignment not found: %s", beadID)
	}

	prevStatus := assignment.Status

	if !isValidTransition(prevStatus, newStatus) {
		s.mutex.Unlock()
		return &InvalidTransitionError{
			BeadID: beadID,
			From:   prevStatus,
			To:     newStatus,
		}
	}

	now := time.Now().UTC()

	// Update status and timestamps
	assignment.Status = newStatus
	switch newStatus {
	case StatusClaimed:
		assignment.StartedAt = nil
		assignment.CompletedAt = nil
		assignment.FailedAt = nil
		assignment.FailReason = ""
		assignment.FailureReason = ""
	case StatusAssigned:
		assignment.StartedAt = nil
		assignment.CompletedAt = nil
		assignment.FailedAt = nil
		assignment.FailReason = ""
		assignment.FailureReason = ""
	case StatusWorking:
		assignment.StartedAt = &now
	case StatusCompleted:
		assignment.CompletedAt = &now
	case StatusFailed:
		assignment.FailedAt = &now
	}

	// Persist
	if err := s.saveLocked(); err != nil {
		s.mutex.Unlock()
		return err
	}

	persisted, err := s.confirmPersistedStatusLocked(beadID, newStatus)
	if err != nil {
		s.mutex.Unlock()
		return err
	}
	emitIdle := s.shouldEmitAgentIdleLocked(persisted, prevStatus, newStatus)
	cloned := cloneAssignment(persisted)
	s.mutex.Unlock()

	emitAssignmentStatusEvent(s.SessionName, cloned, newStatus, "")
	if emitIdle {
		emitAgentIdle(s.SessionName, cloned, prevStatus, newStatus)
	}

	return nil
}

// MarkWorking marks an assignment as actively working
func (s *AssignmentStore) MarkWorking(beadID string) error {
	return s.UpdateStatus(beadID, StatusWorking)
}

// MarkCompleted marks an assignment as completed
func (s *AssignmentStore) MarkCompleted(beadID string) error {
	return s.UpdateStatus(beadID, StatusCompleted)
}

// MarkFailed marks an assignment as failed with a reason
func (s *AssignmentStore) MarkFailed(beadID, reason string) error {
	s.mutex.Lock()

	assignment, ok := s.Assignments[beadID]
	if !ok {
		s.mutex.Unlock()
		return fmt.Errorf("[ASSIGN] Assignment not found: %s", beadID)
	}

	prevStatus := assignment.Status

	if !isValidTransition(prevStatus, StatusFailed) {
		s.mutex.Unlock()
		return &InvalidTransitionError{
			BeadID: beadID,
			From:   prevStatus,
			To:     StatusFailed,
		}
	}

	now := time.Now().UTC()
	assignment.Status = StatusFailed
	assignment.FailedAt = &now
	assignment.FailReason = reason
	assignment.FailureReason = ""

	if err := s.saveLocked(); err != nil {
		s.mutex.Unlock()
		return err
	}

	persisted, err := s.confirmPersistedStatusLocked(beadID, StatusFailed)
	if err != nil {
		s.mutex.Unlock()
		return err
	}
	emitIdle := s.shouldEmitAgentIdleLocked(persisted, prevStatus, StatusFailed)
	cloned := cloneAssignment(persisted)
	s.mutex.Unlock()

	emitAssignmentStatusEvent(s.SessionName, cloned, StatusFailed, reason)
	if emitIdle {
		emitAgentIdle(s.SessionName, cloned, prevStatus, StatusFailed)
	}

	return nil
}

// MarkWorkingIfCurrent advances only the exact assignment generation observed
// by the caller. A superseded, terminal, or clearing row is a stale observation
// and returns applied=false without mutation.
func (s *AssignmentStore) MarkWorkingIfCurrent(ctx context.Context, observed *Assignment) (bool, error) {
	return s.transitionIfCurrent(ctx, observed, StatusWorking, "")
}

// MarkCompletedIfCurrent completes only the exact assignment generation
// observed by the caller.
func (s *AssignmentStore) MarkCompletedIfCurrent(ctx context.Context, observed *Assignment) (bool, error) {
	return s.transitionIfCurrent(ctx, observed, StatusCompleted, "")
}

// MarkFailedIfCurrent fails only the exact assignment generation observed by
// the caller.
func (s *AssignmentStore) MarkFailedIfCurrent(ctx context.Context, observed *Assignment, reason string) (bool, error) {
	return s.transitionIfCurrent(ctx, observed, StatusFailed, reason)
}

func (s *AssignmentStore) transitionIfCurrent(ctx context.Context, observed *Assignment, newStatus AssignmentStatus, reason string) (bool, error) {
	if observed == nil || strings.TrimSpace(observed.BeadID) == "" {
		return false, errors.New("observed assignment generation is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	operationUnlock, err := acquireAtomicBeadOperationLock(ctx, s.path, observed.BeadID)
	if err != nil {
		return false, fmt.Errorf("lock assignment lifecycle %s: %w", observed.BeadID, err)
	}
	defer operationUnlock()
	if err := s.LoadStrict(); err != nil {
		return false, fmt.Errorf("refresh assignment lifecycle %s: %w", observed.BeadID, err)
	}

	s.mutex.Lock()
	current := s.Assignments[observed.BeadID]
	if !SameAssignmentGeneration(observed, current) || current.ClearState != ClearStateNone || !isValidTransition(current.Status, newStatus) {
		s.mutex.Unlock()
		return false, nil
	}

	previous := cloneAssignment(current)
	previousStatus := current.Status
	now := time.Now().UTC()
	current.Status = newStatus
	switch newStatus {
	case StatusWorking:
		current.StartedAt = &now
	case StatusCompleted:
		current.CompletedAt = &now
	case StatusFailed:
		current.FailedAt = &now
		current.FailReason = reason
		current.FailureReason = ""
	default:
		s.mutex.Unlock()
		return false, fmt.Errorf("unsupported guarded assignment transition to %s", newStatus)
	}
	if s.replace == nil {
		s.replace = make(map[string]struct{})
	}
	s.replace[observed.BeadID] = struct{}{}
	if err := s.saveLocked(); err != nil {
		var concurrentMutation *ConcurrentMutationError
		if errors.As(err, &concurrentMutation) {
			s.mutex.Unlock()
			return false, nil
		}
		s.Assignments[observed.BeadID] = previous
		delete(s.replace, observed.BeadID)
		s.mutex.Unlock()
		return false, err
	}
	persisted, err := s.confirmPersistedStatusLocked(observed.BeadID, newStatus)
	if err != nil {
		s.mutex.Unlock()
		// A guarded transition that lost the race is a "did not apply"
		// outcome, which this API already reports as (false, nil).
		if errors.Is(err, ErrAssignmentStatusMismatch) {
			return false, nil
		}
		return false, err
	}
	emitIdle := s.shouldEmitAgentIdleLocked(persisted, previousStatus, newStatus)
	cloned := cloneAssignment(persisted)
	s.mutex.Unlock()

	emitAssignmentStatusEvent(s.SessionName, cloned, newStatus, reason)
	if emitIdle {
		emitAgentIdle(s.SessionName, cloned, previousStatus, newStatus)
	}
	return true, nil
}

// SameAssignmentGeneration reports whether two snapshots identify the same
// durable assignment attempt even when lifecycle metadata has advanced.
func SameAssignmentGeneration(observed, current *Assignment) bool {
	if observed == nil || current == nil || observed.BeadID != current.BeadID {
		return false
	}
	observedKey := strings.TrimSpace(observed.IdempotencyKey)
	currentKey := strings.TrimSpace(current.IdempotencyKey)
	if observedKey != "" || currentKey != "" {
		return observedKey != "" && observedKey == currentKey
	}
	return !observed.AssignedAt.IsZero() && observed.AssignedAt.Equal(current.AssignedAt)
}

func terminalCompletionEventID(current *Assignment, terminalStatus AssignmentStatus) string {
	if current == nil {
		return ""
	}
	generation := strings.TrimSpace(current.IdempotencyKey)
	if generation == "" && !current.AssignedAt.IsZero() {
		generation = current.AssignedAt.UTC().Format(time.RFC3339Nano)
	}
	return fmt.Sprintf("%s/%s/%s", strings.TrimSpace(current.BeadID), generation, terminalStatus)
}

// Reassign moves an assignment to a different agent
func (s *AssignmentStore) Reassign(beadID string, target ReassignmentTarget) (*Assignment, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	oldAssignment, ok := s.Assignments[beadID]
	if !ok {
		return nil, fmt.Errorf("[ASSIGN] Assignment not found: %s", beadID)
	}

	if !isValidTransition(oldAssignment.Status, StatusReassigned) {
		return nil, &InvalidTransitionError{
			BeadID: beadID,
			From:   oldAssignment.Status,
			To:     StatusReassigned,
		}
	}

	// Mark old assignment as reassigned
	oldAssignment.Status = StatusReassigned

	// Create new assignment
	now := time.Now().UTC()
	newAssignment := &Assignment{
		BeadID:         beadID,
		BeadTitle:      oldAssignment.BeadTitle,
		Pane:           target.Pane,
		AgentType:      target.AgentType,
		AgentName:      target.AgentName,
		Status:         StatusAssigned,
		AssignedAt:     now,
		RetryCount:     oldAssignment.RetryCount,
		DispatchTarget: target.DispatchTarget,
		OccupancyKey:   target.OccupancyKey,
	}

	s.Assignments[beadID] = newAssignment
	if s.replace == nil {
		s.replace = make(map[string]struct{})
	}
	s.replace[beadID] = struct{}{}

	if err := s.saveLocked(); err != nil {
		return nil, err
	}

	persisted, err := s.confirmPersistedStatusLocked(beadID, StatusAssigned)
	if err != nil {
		return nil, err
	}

	return cloneAssignment(persisted), nil
}

// Remove removes an assignment from the store.
func (s *AssignmentStore) Remove(beadID string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	delete(s.Assignments, beadID)

	return s.saveLocked()
}

// Clear removes all assignments from the store.
func (s *AssignmentStore) Clear() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.Assignments = make(map[string]*Assignment)

	return s.saveLocked()
}

// Stats returns summary statistics about assignments
func (s *AssignmentStore) Stats() AssignmentStats {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	stats := AssignmentStats{}
	for _, a := range s.Assignments {
		stats.Total++
		switch a.Status {
		case StatusClaimed:
			stats.Claimed++
		case StatusAssigned:
			stats.Assigned++
		case StatusWorking:
			stats.Working++
		case StatusCompleted:
			stats.Completed++
		case StatusFailed:
			stats.Failed++
		case StatusReassigned:
			stats.Reassigned++
		}
	}
	return stats
}

// AssignmentStats contains summary statistics
type AssignmentStats struct {
	Total      int `json:"total"`
	Claimed    int `json:"claimed"`
	Assigned   int `json:"assigned"`
	Working    int `json:"working"`
	Completed  int `json:"completed"`
	Failed     int `json:"failed"`
	Reassigned int `json:"reassigned"`
}

func emitAssignmentStatusEvent(session string, a *Assignment, newStatus AssignmentStatus, failReason string) {
	if a == nil {
		return
	}
	paneID, err := assignmentEventPaneID(a)
	if err != nil {
		return
	}

	baseDetails := map[string]string{
		"bead_id":    a.BeadID,
		"bead_title": a.BeadTitle,
		"pane_id":    paneID,
		"pane_index": fmt.Sprintf("%d", a.Pane),
		"agent_type": a.AgentType,
		"agent_name": a.AgentName,
		"status":     string(newStatus),
	}

	switch newStatus {
	case StatusWorking:
		events.DefaultEmitter().Emit(events.NewWebhookEvent(
			events.WebhookAgentBusy,
			session,
			paneID,
			a.AgentType,
			fmt.Sprintf("Agent busy on %s", a.BeadID),
			baseDetails,
		))
	case StatusCompleted:
		events.DefaultEmitter().Emit(events.NewWebhookEvent(
			events.WebhookBeadCompleted,
			session,
			paneID,
			a.AgentType,
			fmt.Sprintf("Bead completed: %s", a.BeadID),
			baseDetails,
		))
		events.DefaultEmitter().Emit(events.NewWebhookEvent(
			events.WebhookAgentCompleted,
			session,
			paneID,
			a.AgentType,
			fmt.Sprintf("Agent completed bead %s", a.BeadID),
			baseDetails,
		))
	case StatusFailed:
		details := baseDetails
		if strings.TrimSpace(failReason) != "" {
			// Clone to avoid mutating base map used by other emissions.
			details = make(map[string]string, len(baseDetails)+1)
			for k, v := range baseDetails {
				details[k] = v
			}
			details["fail_reason"] = failReason
		}
		msg := fmt.Sprintf("Bead failed: %s", a.BeadID)
		if strings.TrimSpace(failReason) != "" {
			msg = fmt.Sprintf("%s (%s)", msg, strings.TrimSpace(failReason))
		}
		events.DefaultEmitter().Emit(events.NewWebhookEvent(
			events.WebhookBeadFailed,
			session,
			paneID,
			a.AgentType,
			msg,
			details,
		))
		events.DefaultEmitter().Emit(events.NewWebhookEvent(
			events.WebhookAgentError,
			session,
			paneID,
			a.AgentType,
			msg,
			details,
		))
	}
}

func (s *AssignmentStore) shouldEmitAgentIdleLocked(a *Assignment, prevStatus, newStatus AssignmentStatus) bool {
	if a == nil {
		return false
	}
	if prevStatus != StatusWorking {
		return false
	}
	if newStatus != StatusCompleted && newStatus != StatusFailed {
		return false
	}
	paneID, err := assignmentEventPaneID(a)
	if err != nil {
		return false
	}

	// Only emit idle when there are no remaining "working" assignments for this pane.
	for _, other := range s.Assignments {
		if other == nil {
			continue
		}
		if other.Status == StatusWorking {
			otherPaneID, identityErr := assignmentEventPaneID(other)
			if identityErr != nil || otherPaneID == paneID {
				return false
			}
		}
	}

	return true
}

func emitAgentIdle(session string, a *Assignment, prevStatus, newStatus AssignmentStatus) {
	paneID, err := assignmentEventPaneID(a)
	if err != nil {
		return
	}
	events.DefaultEmitter().Emit(events.NewWebhookEvent(
		events.WebhookAgentIdle,
		session,
		paneID,
		a.AgentType,
		"Agent idle (no active bead assignments)",
		map[string]string{
			"bead_id":     a.BeadID,
			"bead_title":  a.BeadTitle,
			"pane_id":     paneID,
			"pane_index":  fmt.Sprintf("%d", a.Pane),
			"agent_type":  a.AgentType,
			"agent_name":  a.AgentName,
			"prev_status": string(prevStatus),
			"new_status":  string(newStatus),
		},
	))
}

func assignmentEventPaneID(a *Assignment) (string, error) {
	return CanonicalPaneIdentity(a)
}

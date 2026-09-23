// Package context provides context window monitoring for AI agent orchestration.
// pending.go implements persistent storage for pending rotation confirmations.
package context

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	pendingRotationDir  = "rotation_history"
	pendingRotationFile = "pending.jsonl"
)

var (
	// pendingMu provides goroutine safety for pending operations
	pendingMu sync.Mutex
)

// StoredPendingRotation is the serialized form of PendingRotation for persistence.
type StoredPendingRotation struct {
	AgentID        string          `json:"agent_id"`
	SessionName    string          `json:"session_name"`
	PaneID         string          `json:"pane_id"`
	ContextPercent float64         `json:"context_percent"`
	CreatedAt      time.Time       `json:"created_at"`
	TimeoutAt      time.Time       `json:"timeout_at"`
	DefaultAction  ConfirmAction   `json:"default_action"`
	WorkDir        string          `json:"work_dir"`
	PanePID        int             `json:"pane_pid,omitempty"`
	PaneType       string          `json:"pane_type,omitempty"`
	Remote         string          `json:"remote,omitempty"`
	SelectedAction ConfirmAction   `json:"selected_action,omitempty"`
	ExecutionState RotationState   `json:"execution_state,omitempty"`
	ExecutionID    string          `json:"execution_id,omitempty"`
	Result         *RotationResult `json:"result,omitempty"`
}

// ToPendingRotation converts a StoredPendingRotation to PendingRotation.
func (s *StoredPendingRotation) ToPendingRotation() *PendingRotation {
	return &PendingRotation{
		AgentID:        s.AgentID,
		SessionName:    s.SessionName,
		PaneID:         s.PaneID,
		ContextPercent: s.ContextPercent,
		CreatedAt:      s.CreatedAt,
		TimeoutAt:      s.TimeoutAt,
		DefaultAction:  s.DefaultAction,
		WorkDir:        s.WorkDir,
		PanePID:        s.PanePID,
		PaneType:       s.PaneType,
		Remote:         s.Remote,
		SelectedAction: s.SelectedAction,
		ExecutionState: s.ExecutionState,
		ExecutionID:    s.ExecutionID,
		Result:         s.Result,
	}
}

// FromPendingRotation creates a StoredPendingRotation from PendingRotation.
func FromPendingRotation(p *PendingRotation) *StoredPendingRotation {
	return &StoredPendingRotation{
		AgentID:        p.AgentID,
		SessionName:    p.SessionName,
		PaneID:         p.PaneID,
		ContextPercent: p.ContextPercent,
		CreatedAt:      p.CreatedAt,
		TimeoutAt:      p.TimeoutAt,
		DefaultAction:  p.DefaultAction,
		WorkDir:        p.WorkDir,
		PanePID:        p.PanePID,
		PaneType:       p.PaneType,
		Remote:         p.Remote,
		SelectedAction: p.SelectedAction,
		ExecutionState: p.ExecutionState,
		ExecutionID:    p.ExecutionID,
		Result:         p.Result,
	}
}

// PendingRotationStore provides persistent storage for pending rotations.
type PendingRotationStore struct {
	storagePath string
}

// NewPendingRotationStore creates a new pending rotation store with default path.
func NewPendingRotationStore() *PendingRotationStore {
	return &PendingRotationStore{
		storagePath: defaultPendingRotationPath(),
	}
}

// NewPendingRotationStoreWithPath creates a store with a custom path.
// Test-only hook: retained because tests in other packages (notably
// internal/coordinator) isolate the global store with a temp path.
func NewPendingRotationStoreWithPath(path string) *PendingRotationStore {
	return &PendingRotationStore{
		storagePath: path,
	}
}

// defaultPendingRotationPath returns the path to the pending rotation file.
func defaultPendingRotationPath() string {
	ntmDir, err := util.NTMDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "ntm", pendingRotationDir, pendingRotationFile)
	}
	return filepath.Join(ntmDir, pendingRotationDir, pendingRotationFile)
}

// Add adds or updates a pending rotation in the store.
func (s *PendingRotationStore) Add(pending *PendingRotation) error {
	if pending == nil {
		return fmt.Errorf("pending rotation is nil")
	}

	pendingMu.Lock()
	defer pendingMu.Unlock()
	unlock, err := s.lock(context.Background())
	if err != nil {
		return err
	}
	defer unlock()

	// Preserve other requests, including expired ones awaiting their configured
	// default action. Only the matching agent may be replaced here.
	entries, err := s.readAllLocked()
	if err != nil {
		return fmt.Errorf("reading pending rotations: %w", err)
	}
	var newEntries []StoredPendingRotation

	for _, e := range entries {
		if pendingAgentIDEqual(e.AgentID, pending.AgentID) && e.ExecutionState != "" && e.ExecutionState != RotationStateCompleted {
			return fmt.Errorf("pending rotation %s has an unresolved confirmation", pending.AgentID)
		}
		if pendingAgentIDEqual(e.AgentID, pending.AgentID) && e.ExecutionState == RotationStateCompleted &&
			e.SelectedAction == ConfirmRotate && e.Result != nil && e.Result.NewPaneID != "" &&
			e.PaneID == pending.PaneID && e.PanePID > 0 && e.PanePID == pending.PanePID && e.Remote == pending.Remote {
			return fmt.Errorf("agent %s already rotated to %s; the original pane must not be rotated again", pending.AgentID, e.Result.NewPaneID)
		}
		if !pendingAgentIDEqual(e.AgentID, pending.AgentID) {
			newEntries = append(newEntries, e)
		}
	}

	// Add the new/updated entry
	newEntries = append(newEntries, *FromPendingRotation(pending))

	return s.writeAllLocked(newEntries)
}

// Remove removes a pending rotation by agent ID.
func (s *PendingRotationStore) Remove(agentID string) error {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	unlock, err := s.lock(context.Background())
	if err != nil {
		return err
	}
	defer unlock()

	entries, err := s.readAllLocked()
	if err != nil {
		return fmt.Errorf("reading pending rotations: %w", err)
	}
	var newEntries []StoredPendingRotation

	for _, e := range entries {
		if pendingAgentIDEqual(e.AgentID, agentID) && e.ExecutionState != "" && e.ExecutionState != RotationStateCompleted {
			return fmt.Errorf("pending rotation %s has an unresolved confirmation", agentID)
		}
		if !pendingAgentIDEqual(e.AgentID, agentID) {
			newEntries = append(newEntries, e)
		}
	}

	return s.writeAllLocked(newEntries)
}

// Get retrieves a pending rotation by agent ID.
func (s *PendingRotationStore) Get(agentID string) (*PendingRotation, error) {
	pendingMu.Lock()
	defer pendingMu.Unlock()

	entries, err := s.readAllLocked()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	for _, e := range entries {
		if pendingAgentIDEqual(e.AgentID, agentID) && storedPendingVisible(e, now) {
			return e.ToPendingRotation(), nil
		}
	}

	return nil, nil
}

// GetAll retrieves all non-expired pending rotations.
func (s *PendingRotationStore) GetAll() ([]*PendingRotation, error) {
	pendingMu.Lock()
	defer pendingMu.Unlock()

	entries, err := s.readAllLocked()
	if err != nil {
		return nil, err
	}

	var result []*PendingRotation
	now := time.Now()
	for _, e := range entries {
		if storedPendingVisible(e, now) {
			result = append(result, e.ToPendingRotation())
		}
	}

	return result, nil
}

// GetForSession retrieves pending rotations for a specific session.
func (s *PendingRotationStore) GetForSession(session string) ([]*PendingRotation, error) {
	pendingMu.Lock()
	defer pendingMu.Unlock()

	entries, err := s.readAllLocked()
	if err != nil {
		return nil, err
	}

	var result []*PendingRotation
	now := time.Now()
	for _, e := range entries {
		if pendingSessionNameEqual(e.SessionName, session) && storedPendingVisible(e, now) {
			result = append(result, e.ToPendingRotation())
		}
	}

	return result, nil
}

func storedPendingRetained(entry StoredPendingRotation, now time.Time) bool {
	return entry.ExecutionState != "" || entry.TimeoutAt.After(now)
}

func storedPendingVisible(entry StoredPendingRotation, now time.Time) bool {
	return entry.ExecutionState != RotationStateCompleted && storedPendingRetained(entry, now)
}

func (s *PendingRotationStore) lock(ctx context.Context) (func(), error) {
	return acquirePendingFileLock(ctx, s.storagePath+".lock")
}

// BeginConfirmation owns one agent's confirmation across processes. The
// selected action is written before execution; a crashed owner leaves an
// explicit interrupted attempt rather than an apparently untouched request.
// The caller must retain release until FinishConfirmation has saved its result.
func (s *PendingRotationStore) BeginConfirmation(ctx context.Context, agentID string, action ConfirmAction, retry bool) (*PendingRotation, func(), error) {
	return s.beginConfirmation(ctx, agentID, action, retry, false)
}

// BeginExpiredConfirmation admits the configured timeout action without
// reopening any choice the operator or a previous executor already made.
func (s *PendingRotationStore) BeginExpiredConfirmation(ctx context.Context, agentID string, action ConfirmAction) (*PendingRotation, func(), error) {
	return s.beginConfirmation(ctx, agentID, action, false, true)
}

func (s *PendingRotationStore) beginConfirmation(ctx context.Context, agentID string, action ConfirmAction, retry, allowExpired bool) (*PendingRotation, func(), error) {
	if ctx == nil {
		return nil, nil, errors.New("confirmation context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if agentID == "" {
		return nil, nil, errors.New("agent ID is required")
	}
	switch action {
	case ConfirmRotate, ConfirmCompact, ConfirmIgnore, ConfirmPostpone:
	default:
		return nil, nil, fmt.Errorf("unknown confirmation action: %s", action)
	}
	digest := sha256.Sum256([]byte(agentID))
	release, err := acquirePendingFileLock(ctx, fmt.Sprintf("%s.confirm-%x.lock", s.storagePath, digest[:16]))
	if err != nil {
		return nil, nil, err
	}
	claimed := false
	defer func() {
		if !claimed {
			release()
		}
	}()
	pendingMu.Lock()
	defer pendingMu.Unlock()
	unlock, err := s.lock(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	entries, err := s.readAllLocked()
	if err != nil {
		return nil, nil, err
	}
	for index := range entries {
		entry := &entries[index]
		if !pendingAgentIDEqual(entry.AgentID, agentID) {
			continue
		}
		if !storedPendingRetained(*entry, time.Now()) && !allowExpired && !retry {
			return nil, nil, fmt.Errorf("pending rotation for %s has expired", agentID)
		}
		if allowExpired && entry.ExecutionState == RotationStateCompleted && entry.Result != nil {
			claimed = true
			return entry.ToPendingRotation(), release, nil
		}
		if entry.SelectedAction != "" && entry.SelectedAction != action && !(action == ConfirmIgnore && retry && entry.ExecutionState != RotationStateCompleted) {
			return nil, nil, fmt.Errorf("confirmation for %s already selected %s; cannot change it to %s", agentID, entry.SelectedAction, action)
		}
		if entry.ExecutionState == RotationStateCompleted && entry.Result != nil {
			claimed = true
			return entry.ToPendingRotation(), release, nil
		}
		if allowExpired && (entry.ExecutionState != "" || entry.SelectedAction != "" || entry.TimeoutAt.After(time.Now()) || entry.DefaultAction != action) {
			return nil, nil, errors.New("timeout confirmation is no longer eligible")
		}
		if entry.ExecutionState != "" && !retry {
			return nil, nil, fmt.Errorf("confirmation for %s is %s; inspect pending state and use --retry to explicitly retry the same action", agentID, entry.ExecutionState)
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		entry.SelectedAction = action
		entry.ExecutionState = RotationStateInProgress
		entry.ExecutionID = rand.Text()
		entry.Result = nil
		if err := s.writeAllLocked(entries); err != nil {
			return nil, nil, fmt.Errorf("persist confirmation ownership: %w", err)
		}
		claimed = true
		return entry.ToPendingRotation(), release, nil
	}
	return nil, nil, fmt.Errorf("no pending rotation found for agent %s", agentID)
}

// FinishConfirmation records the actual outcome, even when the execution
// context was canceled. Successful receipts remain replayable until a new
// threshold request is enqueued; unresolved outcomes remain visible in pending.
func (s *PendingRotationStore) FinishConfirmation(ctx context.Context, pending *PendingRotation, result RotationResult) error {
	if ctx == nil || pending == nil || pending.ExecutionID == "" {
		return errors.New("confirmation ownership is required")
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	pendingMu.Lock()
	defer pendingMu.Unlock()
	unlock, err := s.lock(persistCtx)
	if err != nil {
		return err
	}
	defer unlock()
	entries, err := s.readAllLocked()
	if err != nil {
		return err
	}
	for index := range entries {
		entry := &entries[index]
		if entry.AgentID != pending.AgentID {
			continue
		}
		if entry.ExecutionID != pending.ExecutionID || entry.SelectedAction != pending.SelectedAction {
			return errors.New("confirmation ownership changed before recording outcome")
		}
		entry.Result = &result
		entry.ExecutionState = RotationStateFailed
		if result.Success {
			entry.ExecutionState = RotationStateCompleted
			if pending.SelectedAction == ConfirmPostpone {
				entry.TimeoutAt = pending.TimeoutAt
				entry.SelectedAction = ""
				entry.ExecutionState = ""
				entry.ExecutionID = ""
				entry.Result = nil
			}
		}
		return s.writeAllLocked(entries)
	}
	return errors.New("confirmation request disappeared before recording outcome")
}

// Clear removes all pending rotations.
func (s *PendingRotationStore) Clear() error {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	unlock, err := s.lock(context.Background())
	if err != nil {
		return err
	}
	defer unlock()

	entries, err := s.readAllLocked()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.ExecutionState != "" && entry.ExecutionState != RotationStateCompleted {
			return fmt.Errorf("cannot clear unresolved confirmation for %s", entry.AgentID)
		}
	}
	err = os.Remove(s.storagePath)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// readAllLocked reads all entries (caller must hold lock).
func (s *PendingRotationStore) readAllLocked() ([]StoredPendingRotation, error) {
	f, err := os.Open(s.storagePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []StoredPendingRotation{}, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []StoredPendingRotation
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	line := 0
	for scanner.Scan() {
		line++
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var entry StoredPendingRotation
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return nil, fmt.Errorf("pending rotation store is invalid at line %d: %w", line, err)
		}
		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return entries, err
	}

	return entries, nil
}

func pendingAgentIDEqual(left, right string) bool {
	return strings.Compare(left, right) == 0
}

func pendingSessionNameEqual(left, right string) bool {
	return strings.Compare(left, right) == 0
}

// writeAllLocked writes all entries atomically (caller must hold lock).
func (s *PendingRotationStore) writeAllLocked(entries []StoredPendingRotation) error {
	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(s.storagePath), 0755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	// Write to temp file first
	tmpFile, err := os.CreateTemp(filepath.Dir(s.storagePath), "pending-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	tmpFileClosed := false
	defer func() {
		if !tmpFileClosed {
			_ = tmpFile.Close()
		}
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	writer := bufio.NewWriter(tmpFile)
	for _, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("marshaling pending rotation %q: %w", entry.AgentID, err)
		}
		if _, err := writer.Write(data); err != nil {
			return err
		}
		if err := writer.WriteByte('\n'); err != nil {
			return err
		}
	}

	if err := writer.Flush(); err != nil {
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		return err
	}
	if err := tmpFile.Close(); err != nil {
		tmpFileClosed = true
		return err
	}
	tmpFileClosed = true

	if err := os.Chmod(tmpPath, 0600); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, s.storagePath); err != nil {
		return err
	}
	tmpPath = "" // Prevent defer from removing the successfully renamed file
	return nil
}

// DefaultPendingRotationStore is the default global pending rotation store.
var DefaultPendingRotationStore = NewPendingRotationStore()

// AddPendingRotation adds a pending rotation to the default store.
func AddPendingRotation(pending *PendingRotation) error {
	return DefaultPendingRotationStore.Add(pending)
}

// RemovePendingRotation removes a pending rotation from the default store.
func RemovePendingRotation(agentID string) error {
	return DefaultPendingRotationStore.Remove(agentID)
}

// GetPendingRotationByID retrieves a pending rotation from the default store.
func GetPendingRotationByID(agentID string) (*PendingRotation, error) {
	return DefaultPendingRotationStore.Get(agentID)
}

// GetAllPendingRotations retrieves all pending rotations from the default store.
func GetAllPendingRotations() ([]*PendingRotation, error) {
	return DefaultPendingRotationStore.GetAll()
}

// GetPendingRotationsForSession retrieves pending rotations for a session.
func GetPendingRotationsForSession(session string) ([]*PendingRotation, error) {
	return DefaultPendingRotationStore.GetForSession(session)
}

// Package assignment provides assignment tracking for bead-to-agent mappings.
package assignment

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	// assignmentsDirName is the directory name for assignment storage
	assignmentsDirName = "assignments"
	fileExtension      = ".json"
)

// AssignmentStatus represents the current state of an assignment
type AssignmentStatus string

const (
	StatusAssigned   AssignmentStatus = "assigned"   // Prompt sent, waiting to start
	StatusWorking    AssignmentStatus = "working"    // Agent actively working
	StatusCompleted  AssignmentStatus = "completed"  // Bead closed successfully
	StatusFailed     AssignmentStatus = "failed"     // Agent crashed or gave up
	StatusReassigned AssignmentStatus = "reassigned" // Moved to different agent
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
}

// AssignmentStore manages bead-to-agent assignments for a session
type AssignmentStore struct {
	SessionName string                 `json:"session_name"`
	Assignments map[string]*Assignment `json:"assignments"` // bead_id -> assignment
	UpdatedAt   time.Time              `json:"updated_at"`
	Version     int                    `json:"version"` // Schema version for migrations

	mutex sync.RWMutex
	path  string // Path to persistence file
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
	// Store assignments inside the session directory: ~/.ntm/sessions/<session>/assignments.json
	baseDir := StorageDir()
	sessionDir := filepath.Join(baseDir, sessionName)

	// Ensure session directory exists (it might not if we are just creating assignments before session save)
	_ = os.MkdirAll(sessionDir, 0755)

	return &AssignmentStore{
		SessionName: sessionName,
		Assignments: make(map[string]*Assignment),
		UpdatedAt:   time.Now().UTC(),
		Version:     1,
		path:        filepath.Join(sessionDir, assignmentsDirName+fileExtension),
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

// Load reads the assignment store from disk
func (s *AssignmentStore) Load() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			// Try backup
			bakPath := s.path + ".bak"
			data, err = os.ReadFile(bakPath)
			if err != nil {
				// Start fresh - not an error
				return nil
			}
			// Log recovery from backup
			slog.Warn("recovered assignment store from backup", "path", bakPath)
		} else {
			return &PersistenceError{Operation: "load", Path: s.path, Cause: err}
		}
	}

	var loaded AssignmentStore
	if err := json.Unmarshal(data, &loaded); err != nil {
		// Try backup on corrupt JSON
		bakPath := s.path + ".bak"
		data, bakErr := os.ReadFile(bakPath)
		if bakErr != nil {
			// Start fresh
			slog.Warn("assignment store corrupted, starting fresh", "error", err)
			return nil
		}
		if err := json.Unmarshal(data, &loaded); err != nil {
			slog.Warn("assignment store and backup corrupted, starting fresh", "error", err)
			return nil
		}
		slog.Warn("assignment store corrupted, recovered from backup")
	}

	s.SessionName = loaded.SessionName
	s.Assignments = loaded.Assignments
	s.UpdatedAt = loaded.UpdatedAt
	s.Version = loaded.Version

	if s.Assignments == nil {
		s.Assignments = make(map[string]*Assignment)
	}

	return nil
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
	if err := os.MkdirAll(dir, 0755); err != nil {
		return &PersistenceError{Operation: "save", Path: s.path, Cause: fmt.Errorf("create directory: %w", err)}
	}

	s.UpdatedAt = time.Now().UTC()

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return &PersistenceError{Operation: "save", Path: s.path, Cause: fmt.Errorf("marshal: %w", err)}
	}

	// Write to temporary file first
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return &PersistenceError{Operation: "save", Path: tmpPath, Cause: err}
	}

	// Create backup of current file (if exists)
	bakPath := s.path + ".bak"
	if _, err := os.Stat(s.path); err == nil {
		_ = os.Rename(s.path, bakPath)
	}

	// Rename temp to current
	if err := os.Rename(tmpPath, s.path); err != nil {
		return &PersistenceError{Operation: "save", Path: s.path, Cause: err}
	}

	return nil
}

// Assign creates or updates an assignment for a bead
func (s *AssignmentStore) Assign(beadID, beadTitle string, pane int, agentType, agentName, prompt string) (*Assignment, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

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
		// Log but don't fail - keep in-memory state
		slog.Warn("failed to persist assignment store", "error", err)
	}

	return assignment, nil
}

// Get retrieves an assignment by bead ID
func (s *AssignmentStore) Get(beadID string) *Assignment {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.Assignments[beadID]
}

// GetAll returns all assignments as values
func (s *AssignmentStore) GetAll() []Assignment {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var result []Assignment
	for _, a := range s.Assignments {
		result = append(result, *a)
	}
	return result
}

// List returns all assignments
func (s *AssignmentStore) List() []*Assignment {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	result := make([]*Assignment, 0, len(s.Assignments))
	for _, a := range s.Assignments {
		result = append(result, a)
	}
	return result
}

// ListByPane returns all assignments for a specific pane
func (s *AssignmentStore) ListByPane(pane int) []*Assignment {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var result []*Assignment
	for _, a := range s.Assignments {
		if a.Pane == pane {
			result = append(result, a)
		}
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
			result = append(result, a)
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
		if a.Status == StatusAssigned || a.Status == StatusWorking {
			result = append(result, a)
		}
	}
	return result
}

// ValidTransitions defines valid state transitions
var ValidTransitions = map[AssignmentStatus][]AssignmentStatus{
	StatusAssigned:   {StatusWorking, StatusFailed},
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
	defer s.mutex.Unlock()

	assignment, ok := s.Assignments[beadID]
	if !ok {
		return fmt.Errorf("[ASSIGN] Assignment not found: %s", beadID)
	}

	if !isValidTransition(assignment.Status, newStatus) {
		return &InvalidTransitionError{
			BeadID: beadID,
			From:   assignment.Status,
			To:     newStatus,
		}
	}

	now := time.Now().UTC()

	// Update status and timestamps
	assignment.Status = newStatus
	switch newStatus {
	case StatusWorking:
		assignment.StartedAt = &now
	case StatusCompleted:
		assignment.CompletedAt = &now
	case StatusFailed:
		assignment.FailedAt = &now
	}

	// Persist
	if err := s.saveLocked(); err != nil {
		slog.Warn("failed to persist assignment store", "error", err)
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
	defer s.mutex.Unlock()

	assignment, ok := s.Assignments[beadID]
	if !ok {
		return fmt.Errorf("[ASSIGN] Assignment not found: %s", beadID)
	}

	if !isValidTransition(assignment.Status, StatusFailed) {
		return &InvalidTransitionError{
			BeadID: beadID,
			From:   assignment.Status,
			To:     StatusFailed,
		}
	}

	now := time.Now().UTC()
	assignment.Status = StatusFailed
	assignment.FailedAt = &now
	assignment.FailReason = reason

	if err := s.saveLocked(); err != nil {
		slog.Warn("failed to persist assignment store", "error", err)
	}

	return nil
}

// Reassign moves an assignment to a different agent
func (s *AssignmentStore) Reassign(beadID string, newPane int, newAgentType, newAgentName string) (*Assignment, error) {
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
		BeadID:     beadID,
		BeadTitle:  oldAssignment.BeadTitle,
		Pane:       newPane,
		AgentType:  newAgentType,
		AgentName:  newAgentName,
		Status:     StatusAssigned,
		AssignedAt: now,
		PromptSent: oldAssignment.PromptSent,
	}

	s.Assignments[beadID] = newAssignment

	if err := s.saveLocked(); err != nil {
		slog.Warn("failed to persist assignment store", "error", err)
	}

	return newAssignment, nil
}

// Remove removes an assignment from the store
func (s *AssignmentStore) Remove(beadID string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	delete(s.Assignments, beadID)

	if err := s.saveLocked(); err != nil {
		slog.Warn("failed to persist assignment store", "error", err)
	}
}

// Clear removes all assignments from the store
func (s *AssignmentStore) Clear() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.Assignments = make(map[string]*Assignment)

	if err := s.saveLocked(); err != nil {
		slog.Warn("failed to persist assignment store", "error", err)
	}
}

// Stats returns summary statistics about assignments
func (s *AssignmentStore) Stats() AssignmentStats {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	stats := AssignmentStats{}
	for _, a := range s.Assignments {
		stats.Total++
		switch a.Status {
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
	Assigned   int `json:"assigned"`
	Working    int `json:"working"`
	Completed  int `json:"completed"`
	Failed     int `json:"failed"`
	Reassigned int `json:"reassigned"`
}

package ensemble

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

// StateStore provides SQLite-backed persistence for ensemble sessions.
type StateStore struct {
	store     *state.Store
	ensembles *state.EnsembleStore
}

// NewStateStore opens the shared state store and ensures migrations are applied.
func NewStateStore(path string) (*StateStore, error) {
	store, err := state.Open(path)
	if err != nil {
		return nil, err
	}
	if err := store.Migrate(); err != nil {
		_ = store.Close()
		return nil, err
	}

	ensembleStore := state.NewEnsembleStore(store)
	if ensembleStore == nil {
		_ = store.Close()
		return nil, errors.New("ensemble store unavailable")
	}

	return &StateStore{store: store, ensembles: ensembleStore}, nil
}

// Close closes the underlying SQLite store.
func (s *StateStore) Close() error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Close()
}

// Save persists an ensemble session to SQLite.
func (s *StateStore) Save(session *EnsembleSession) error {
	if s == nil || s.ensembles == nil {
		return errors.New("ensemble state store is nil")
	}
	if session == nil {
		return errors.New("ensemble session is nil")
	}
	if session.SessionName == "" {
		return errors.New("session name is required")
	}
	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now().UTC()
	}

	return s.ensembles.SaveEnsemble(toStateSession(session))
}

// Load fetches an ensemble session from SQLite.
func (s *StateStore) Load(sessionName string) (*EnsembleSession, error) {
	if s == nil || s.ensembles == nil {
		return nil, errors.New("ensemble state store is nil")
	}
	if sessionName == "" {
		return nil, errors.New("session name is required")
	}

	session, err := s.ensembles.GetEnsemble(sessionName)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, os.ErrNotExist
	}
	return fromStateSession(session), nil
}

// UpdateStatus updates the overall status of an ensemble session.
func (s *StateStore) UpdateStatus(sessionName string, status EnsembleStatus) error {
	if s == nil || s.ensembles == nil {
		return errors.New("ensemble state store is nil")
	}
	return s.ensembles.UpdateStatus(sessionName, status.String())
}

// UpdateAssignmentStatus updates the status for a specific mode assignment.
func (s *StateStore) UpdateAssignmentStatus(sessionName, modeID string, status AssignmentStatus) error {
	if s == nil || s.ensembles == nil {
		return errors.New("ensemble state store is nil")
	}
	return s.ensembles.UpdateAssignmentStatus(sessionName, modeID, string(status))
}

// List returns all ensemble sessions in the store.
func (s *StateStore) List() ([]*EnsembleSession, error) {
	if s == nil || s.ensembles == nil {
		return nil, errors.New("ensemble state store is nil")
	}

	sessions, err := s.ensembles.ListEnsembles()
	if err != nil {
		return nil, err
	}
	results := make([]*EnsembleSession, 0, len(sessions))
	for _, session := range sessions {
		results = append(results, fromStateSession(session))
	}
	return results, nil
}

// Delete removes an ensemble session and its assignments from SQLite.
func (s *StateStore) Delete(sessionName string) error {
	if s == nil || s.ensembles == nil {
		return errors.New("ensemble state store is nil")
	}
	return s.ensembles.DeleteEnsemble(sessionName)
}

var defaultStateStore struct {
	once  sync.Once
	store *StateStore
	err   error
}

func defaultSQLiteStore() (*StateStore, error) {
	defaultStateStore.once.Do(func() {
		defaultStateStore.store, defaultStateStore.err = NewStateStore("")
		if defaultStateStore.err != nil {
			defaultStateStore.err = fmt.Errorf("open ensemble state store: %w", defaultStateStore.err)
		}
	})
	return defaultStateStore.store, defaultStateStore.err
}

// CloseDefaultStateStore closes the singleton state store. It is safe to call
// even if the store was never opened or has already been closed.
func CloseDefaultStateStore() {
	if defaultStateStore.store != nil {
		_ = defaultStateStore.store.Close()
	}
}

func toStateSession(session *EnsembleSession) *state.EnsembleSession {
	if session == nil {
		return nil
	}

	assignments := make([]state.ModeAssignment, 0, len(session.Assignments))
	for _, assignment := range session.Assignments {
		status := string(assignment.Status)
		if status == "" {
			status = string(AssignmentPending)
		}
		assignments = append(assignments, state.ModeAssignment{
			ModeID:      assignment.ModeID,
			PaneName:    assignment.PaneName,
			AgentType:   assignment.AgentType,
			Status:      status,
			OutputPath:  assignment.OutputPath,
			AssignedAt:  assignment.AssignedAt,
			CompletedAt: assignment.CompletedAt,
			Error:       assignment.Error,
		})
	}

	return &state.EnsembleSession{
		SessionName:       session.SessionName,
		Question:          session.Question,
		PresetUsed:        session.PresetUsed,
		Status:            session.Status.String(),
		SynthesisStrategy: session.SynthesisStrategy.String(),
		CreatedAt:         session.CreatedAt,
		SynthesizedAt:     session.SynthesizedAt,
		SynthesisOutput:   session.SynthesisOutput,
		Error:             session.Error,
		Assignments:       assignments,
	}
}

func fromStateSession(session *state.EnsembleSession) *EnsembleSession {
	if session == nil {
		return nil
	}

	assignments := make([]ModeAssignment, 0, len(session.Assignments))
	for _, assignment := range session.Assignments {
		assignments = append(assignments, ModeAssignment{
			ModeID:      assignment.ModeID,
			PaneName:    assignment.PaneName,
			AgentType:   assignment.AgentType,
			Status:      AssignmentStatus(assignment.Status),
			OutputPath:  assignment.OutputPath,
			AssignedAt:  assignment.AssignedAt,
			CompletedAt: assignment.CompletedAt,
			Error:       assignment.Error,
		})
	}

	return &EnsembleSession{
		SessionName:       session.SessionName,
		Question:          session.Question,
		PresetUsed:        session.PresetUsed,
		Assignments:       assignments,
		Status:            EnsembleStatus(session.Status),
		SynthesisStrategy: SynthesisStrategy(session.SynthesisStrategy),
		CreatedAt:         session.CreatedAt,
		SynthesizedAt:     session.SynthesizedAt,
		SynthesisOutput:   session.SynthesisOutput,
		Error:             session.Error,
	}
}

package ensemble

import (
	"encoding/json"
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

// NewStateStoreFromDB wraps an existing shared state.Store without opening a second handle.
func NewStateStoreFromDB(store *state.Store) *StateStore {
	if store == nil {
		return nil
	}
	ensembleStore := state.NewEnsembleStore(store)
	if ensembleStore == nil {
		return nil
	}
	return &StateStore{store: store, ensembles: ensembleStore}
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

	persisted, err := toStateSession(session)
	if err != nil {
		return err
	}
	return s.ensembles.SaveEnsemble(persisted)
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
	return fromStateSession(session)
}

// List returns all persisted ensemble sessions from SQLite, newest first.
func (s *StateStore) List() ([]EnsembleSession, error) {
	if s == nil || s.ensembles == nil {
		return nil, errors.New("ensemble state store is nil")
	}

	persisted, err := s.ensembles.ListEnsembles()
	if err != nil {
		return nil, err
	}
	out := make([]EnsembleSession, 0, len(persisted))
	for i := range persisted {
		converted, err := fromStateSession(&persisted[i])
		if err != nil {
			return nil, err
		}
		if converted != nil {
			out = append(out, *converted)
		}
	}
	return out, nil
}

var defaultStateStore struct {
	mu    sync.Mutex
	store *StateStore
}

func defaultSQLiteStore() (*StateStore, error) {
	defaultStateStore.mu.Lock()
	defer defaultStateStore.mu.Unlock()

	if defaultStateStore.store != nil {
		return defaultStateStore.store, nil
	}

	store, err := NewStateStore("")
	if err != nil {
		return nil, fmt.Errorf("open ensemble state store: %w", err)
	}
	defaultStateStore.store = store
	return store, nil
}

// CloseDefaultStateStore closes the singleton state store. It is safe to call
// even if the store was never opened or has already been closed.
func CloseDefaultStateStore() {
	defaultStateStore.mu.Lock()
	s := defaultStateStore.store
	defaultStateStore.store = nil
	defaultStateStore.mu.Unlock()
	if s != nil {
		_ = s.Close()
	}
}

func toStateSession(session *EnsembleSession) (*state.EnsembleSession, error) {
	if session == nil {
		return nil, nil
	}

	var budgetJSON, synthesisJSON string
	if session.Budget != nil {
		encoded, err := json.Marshal(session.Budget)
		if err != nil {
			return nil, fmt.Errorf("encode ensemble budget: %w", err)
		}
		budgetJSON = string(encoded)
	}
	if session.Synthesis != nil {
		encoded, err := json.Marshal(session.Synthesis)
		if err != nil {
			return nil, fmt.Errorf("encode ensemble synthesis config: %w", err)
		}
		synthesisJSON = string(encoded)
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
		SessionName:         session.SessionName,
		Question:            session.Question,
		PresetUsed:          session.PresetUsed,
		Status:              session.Status.String(),
		SynthesisStrategy:   session.SynthesisStrategy.String(),
		BudgetJSON:          budgetJSON,
		SynthesisConfigJSON: synthesisJSON,
		CreatedAt:           session.CreatedAt,
		SynthesizedAt:       session.SynthesizedAt,
		SynthesisOutput:     session.SynthesisOutput,
		Error:               session.Error,
		Assignments:         assignments,
	}, nil
}

func fromStateSession(session *state.EnsembleSession) (*EnsembleSession, error) {
	if session == nil {
		return nil, nil
	}

	var budget *BudgetConfig
	if session.BudgetJSON != "" {
		if err := json.Unmarshal([]byte(session.BudgetJSON), &budget); err != nil {
			return nil, fmt.Errorf("decode ensemble budget: %w", err)
		}
		if budget == nil {
			return nil, errors.New("decode ensemble budget: expected a configuration object")
		}
	}
	var synthesis *SynthesisConfig
	if session.SynthesisConfigJSON != "" {
		if err := json.Unmarshal([]byte(session.SynthesisConfigJSON), &synthesis); err != nil {
			return nil, fmt.Errorf("decode ensemble synthesis config: %w", err)
		}
		if synthesis == nil {
			return nil, errors.New("decode ensemble synthesis config: expected a configuration object")
		}
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
		Budget:            budget,
		Synthesis:         synthesis,
		CreatedAt:         session.CreatedAt,
		SynthesizedAt:     session.SynthesizedAt,
		SynthesisOutput:   session.SynthesisOutput,
		Error:             session.Error,
	}, nil
}

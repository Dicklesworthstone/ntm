package ensemble

import (
	"errors"
	"time"
)

// EffectiveBudget returns the settings persisted when this session was spawned.
// Only legacy sessions without a snapshot use the caller's fallback; zero values
// in a saved snapshot are intentional and must survive later configuration changes.
func (s *EnsembleSession) EffectiveBudget(fallback BudgetConfig) BudgetConfig {
	if s != nil && s.Budget != nil {
		return *s.Budget
	}
	return fallback
}

// EffectiveSynthesis returns the saved settings, including explicit zero and false
// values. SynthesisStrategy remains authoritative when an operator chooses a new
// strategy for an existing session. Legacy sessions use the supplied fallback.
func (s *EnsembleSession) EffectiveSynthesis(fallback SynthesisConfig) SynthesisConfig {
	if s == nil {
		return fallback
	}
	if s.Synthesis != nil {
		fallback = *s.Synthesis
	}
	if s.SynthesisStrategy != "" {
		fallback.Strategy = s.SynthesisStrategy
	}
	return fallback
}

// LoadSession loads an ensemble session state from SQLite.
func LoadSession(sessionName string) (*EnsembleSession, error) {
	if sessionName == "" {
		return nil, errors.New("session name is required")
	}

	store, err := defaultSQLiteStore()
	if err != nil {
		return nil, err
	}

	return store.Load(sessionName)
}

// ListSessions returns all persisted ensemble sessions from SQLite, newest first.
func ListSessions() ([]EnsembleSession, error) {
	store, err := defaultSQLiteStore()
	if err != nil {
		return nil, err
	}
	return store.List()
}

// SaveSession persists an ensemble session state to SQLite.
func SaveSession(sessionName string, state *EnsembleSession) error {
	if state == nil {
		return errors.New("ensemble state is nil")
	}
	if sessionName == "" {
		sessionName = state.SessionName
	}
	if sessionName == "" {
		return errors.New("session name is required")
	}

	if state.SessionName == "" {
		state.SessionName = sessionName
	}
	if state.CreatedAt.IsZero() {
		state.CreatedAt = time.Now().UTC()
	}

	store, err := defaultSQLiteStore()
	if err != nil {
		return err
	}

	if err := store.Save(state); err != nil {
		return err
	}
	return nil
}

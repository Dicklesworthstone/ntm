package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WorkflowState is the durable, session-scoped checkpoint for a workflow runtime.
type WorkflowState struct {
	WorkflowName   string            `json:"workflow"`
	SessionName    string            `json:"session"`
	CurrentStage   string            `json:"current_stage"`
	StageStartedAt time.Time         `json:"stage_started_at"`
	Paused         bool              `json:"paused"`
	PausedAt       *time.Time        `json:"paused_at,omitempty"`
	PauseReason    string            `json:"pause_reason,omitempty"`
	Agents         map[string]string `json:"agents"`
	Variables      map[string]string `json:"variables"`
	StageHistory   []StageRecord     `json:"history"`
	Errors         []WorkflowError   `json:"errors"`
}
type StageRecord struct {
	Stage       string    `json:"stage"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	DurationSec int       `json:"duration_sec"`
	Result      string    `json:"result"`
	Trigger     string    `json:"trigger"`
}

type StateStore struct{ Dir string }

func DefaultStateStore() (*StateStore, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	return &StateStore{Dir: filepath.Join(base, "ntm", "workflows")}, nil
}
func (s *StateStore) path(session string) (string, error) {
	if strings.TrimSpace(session) == "" || filepath.Base(session) != session {
		return "", errors.New("workflow session name must be a single path component")
	}
	return filepath.Join(s.Dir, session+".json"), nil
}
func (s *StateStore) Save(state *WorkflowState) error {
	if state == nil {
		return errors.New("workflow state is required")
	}
	path, err := s.path(state.SessionName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("create workflow state directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode workflow state: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}
func (s *StateStore) Load(session string) (*WorkflowState, error) {
	path, err := s.path(session)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read workflow state: %w", err)
	}
	var state WorkflowState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode workflow state: %w", err)
	}
	return &state, nil
}
func (s *StateStore) Pause(state *WorkflowState, reason string, now time.Time) error {
	if state == nil {
		return errors.New("workflow state is required")
	}
	state.Paused = true
	state.PausedAt = &now
	state.PauseReason = reason
	return s.Save(state)
}
func (s *StateStore) Resume(state *WorkflowState) error {
	if state == nil {
		return errors.New("workflow state is required")
	}
	if !state.Paused {
		return errors.New("workflow is not paused")
	}
	state.Paused = false
	state.PausedAt = nil
	state.PauseReason = ""
	return s.Save(state)
}
func (s *StateStore) RecordStage(state *WorkflowState, result, trigger string, now time.Time) error {
	if state == nil {
		return errors.New("workflow state is required")
	}
	record := StageRecord{Stage: state.CurrentStage, StartedAt: state.StageStartedAt, CompletedAt: now, DurationSec: int(now.Sub(state.StageStartedAt).Seconds()), Result: result, Trigger: trigger}
	state.StageHistory = append(state.StageHistory, record)
	return s.Save(state)
}

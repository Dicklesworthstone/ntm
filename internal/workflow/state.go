package workflow

import (
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

// PaneSequence is a durable, named prompt sequence. Each pane advances through
// the same ordered prompts independently, so a reviewer can resume after a
// process restart without reconstructing state in a shell array or /tmp file.
type PaneSequence struct {
	Name      string         `json:"name"`
	Steps     []string       `json:"steps"`
	Positions map[string]int `json:"positions"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// PaneSequencePosition is the next prompt available to one pane. Position is
// zero-based and Complete reports that the pane has consumed every step.
type PaneSequencePosition struct {
	Sequence string `json:"sequence"`
	Pane     string `json:"pane"`
	Position int    `json:"position"`
	Prompt   string `json:"prompt,omitempty"`
	Complete bool   `json:"complete"`
	Advanced bool   `json:"advanced"`
}

// PaneSequenceStore persists project-local sequence state beneath .ntm. It is
// deliberately separate from template state: sequences represent live
// per-pane progress, not a reusable workflow definition.
type PaneSequenceStore struct {
	ProjectDir string
	Now        func() time.Time
}

var paneSequenceMu sync.Mutex

func NewPaneSequenceStore(projectDir string) (*PaneSequenceStore, error) {
	if strings.TrimSpace(projectDir) == "" {
		return nil, errors.New("sequence project directory is required")
	}
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return nil, fmt.Errorf("resolve sequence project directory: %w", err)
	}
	return &PaneSequenceStore{ProjectDir: abs}, nil
}

func (s *PaneSequenceStore) sequenceDir() string {
	return filepath.Join(s.ProjectDir, ".ntm", "workflows", "sequences")
}

func (s *PaneSequenceStore) path(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, "/\\\x00") {
		return "", fmt.Errorf("invalid sequence name %q", name)
	}
	return filepath.Join(s.sequenceDir(), name+".json"), nil
}

func (s *PaneSequenceStore) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func normalizePaneSequenceSteps(steps []string) ([]string, error) {
	if len(steps) == 0 {
		return nil, errors.New("sequence requires at least one prompt")
	}
	normalized := make([]string, len(steps))
	for i, step := range steps {
		if strings.TrimSpace(step) == "" {
			return nil, fmt.Errorf("sequence prompt %d is empty", i+1)
		}
		normalized[i] = step
	}
	return normalized, nil
}

func validatePaneSequence(sequence PaneSequence) error {
	if _, err := (&PaneSequenceStore{ProjectDir: "."}).path(sequence.Name); err != nil {
		return err
	}
	if _, err := normalizePaneSequenceSteps(sequence.Steps); err != nil {
		return err
	}
	for pane, position := range sequence.Positions {
		if strings.TrimSpace(pane) == "" {
			return errors.New("sequence contains an empty pane identity")
		}
		if position < 0 || position > len(sequence.Steps) {
			return fmt.Errorf("sequence pane %q has invalid position %d", pane, position)
		}
	}
	return nil
}

// Create records a named sequence. Existing names are rejected so that a
// retry cannot silently reset another pane's in-flight progress.
func (s *PaneSequenceStore) Create(name string, steps []string) (*PaneSequence, error) {
	if s == nil {
		return nil, errors.New("sequence store is required")
	}
	path, err := s.path(name)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizePaneSequenceSteps(steps)
	if err != nil {
		return nil, err
	}

	paneSequenceMu.Lock()
	defer paneSequenceMu.Unlock()
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("sequence %q already exists", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect sequence %q: %w", name, err)
	}
	sequence := &PaneSequence{
		Name:      name,
		Steps:     normalized,
		Positions: make(map[string]int),
		CreatedAt: s.now(),
		UpdatedAt: s.now(),
	}
	if err := s.saveLocked(sequence); err != nil {
		return nil, err
	}
	return clonePaneSequence(sequence), nil
}

func (s *PaneSequenceStore) Load(name string) (*PaneSequence, error) {
	if s == nil {
		return nil, errors.New("sequence store is required")
	}
	path, err := s.path(name)
	if err != nil {
		return nil, err
	}
	paneSequenceMu.Lock()
	defer paneSequenceMu.Unlock()
	return s.loadLocked(path)
}

// Next returns a pane's next prompt without changing its durable position.
func (s *PaneSequenceStore) Next(name, pane string) (PaneSequencePosition, error) {
	if strings.TrimSpace(pane) == "" {
		return PaneSequencePosition{}, errors.New("sequence pane is required")
	}
	sequence, err := s.Load(name)
	if err != nil {
		return PaneSequencePosition{}, err
	}
	return sequencePosition(sequence, pane, false), nil
}

// Advance records that a pane consumed its current prompt and returns the new
// next prompt. Advancing a completed pane is idempotent and reports Advanced
// false, which lets recovery code retry safely after observing completion.
func (s *PaneSequenceStore) Advance(name, pane string) (PaneSequencePosition, error) {
	if s == nil {
		return PaneSequencePosition{}, errors.New("sequence store is required")
	}
	if strings.TrimSpace(pane) == "" {
		return PaneSequencePosition{}, errors.New("sequence pane is required")
	}
	path, err := s.path(name)
	if err != nil {
		return PaneSequencePosition{}, err
	}

	paneSequenceMu.Lock()
	defer paneSequenceMu.Unlock()
	sequence, err := s.loadLocked(path)
	if err != nil {
		return PaneSequencePosition{}, err
	}
	position := sequence.Positions[pane]
	if position >= len(sequence.Steps) {
		return sequencePosition(sequence, pane, false), nil
	}
	sequence.Positions[pane] = position + 1
	sequence.UpdatedAt = s.now()
	if err := s.saveLocked(sequence); err != nil {
		return PaneSequencePosition{}, err
	}
	return sequencePosition(sequence, pane, true), nil
}

func (s *PaneSequenceStore) loadLocked(path string) (*PaneSequence, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("sequence %q not found", strings.TrimSuffix(filepath.Base(path), ".json"))
		}
		return nil, fmt.Errorf("read sequence: %w", err)
	}
	var sequence PaneSequence
	if err := json.Unmarshal(data, &sequence); err != nil {
		return nil, fmt.Errorf("decode sequence: %w", err)
	}
	if sequence.Positions == nil {
		sequence.Positions = make(map[string]int)
	}
	if err := validatePaneSequence(sequence); err != nil {
		return nil, fmt.Errorf("invalid persisted sequence: %w", err)
	}
	return &sequence, nil
}

func (s *PaneSequenceStore) saveLocked(sequence *PaneSequence) error {
	if err := validatePaneSequence(*sequence); err != nil {
		return err
	}
	if err := os.MkdirAll(s.sequenceDir(), 0o755); err != nil {
		return fmt.Errorf("create sequence directory: %w", err)
	}
	path, err := s.path(sequence.Name)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(sequence, "", "  ")
	if err != nil {
		return fmt.Errorf("encode sequence: %w", err)
	}
	if err := util.AtomicWriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write sequence: %w", err)
	}
	return nil
}

func sequencePosition(sequence *PaneSequence, pane string, advanced bool) PaneSequencePosition {
	position := sequence.Positions[pane]
	result := PaneSequencePosition{
		Sequence: sequence.Name,
		Pane:     pane,
		Position: position,
		Advanced: advanced,
		Complete: position >= len(sequence.Steps),
	}
	if !result.Complete {
		result.Prompt = sequence.Steps[position]
	}
	return result
}

func clonePaneSequence(sequence *PaneSequence) *PaneSequence {
	clone := *sequence
	clone.Steps = append([]string(nil), sequence.Steps...)
	clone.Positions = make(map[string]int, len(sequence.Positions))
	for pane, position := range sequence.Positions {
		clone.Positions[pane] = position
	}
	return &clone
}

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

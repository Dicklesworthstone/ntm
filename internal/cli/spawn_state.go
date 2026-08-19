package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

const spawnStatePath = ".ntm/spawn-state.json"
const spawnStateCompletionGracePeriod = 5 * time.Second

// SpawnState tracks the stagger schedule for dashboard display.
// Written by spawn command, read by dashboard for countdown display.
type SpawnState struct {
	mu sync.Mutex `json:"-"`

	// BatchID is the unique identifier for this spawn batch
	BatchID string `json:"batch_id"`

	// StartedAt is when the spawn began
	StartedAt time.Time `json:"started_at"`

	// StaggerSeconds is the interval between agent prompts
	StaggerSeconds int `json:"stagger_seconds"`

	// TotalAgents is the total number of agents spawned
	TotalAgents int `json:"total_agents"`

	// Prompts tracks the status of each agent's prompt delivery
	Prompts []PromptStatus `json:"prompts"`

	// CompletedAt is when all prompts were delivered (zero if still in progress)
	CompletedAt time.Time `json:"completed_at,omitempty"`
}

// PromptStatus tracks the delivery status of a single agent's prompt.
type PromptStatus struct {
	// Pane is the pane title (e.g., "proj__cc_1")
	Pane string `json:"pane"`

	// PaneID is the tmux pane ID for reliable lookup
	PaneID string `json:"pane_id"`

	// Order is the 1-based spawn order
	Order int `json:"order"`

	// ScheduledAt is when the prompt is scheduled to be sent
	ScheduledAt time.Time `json:"scheduled"`

	// Sent indicates whether the prompt has been delivered
	Sent bool `json:"sent"`

	// SentAt is when the prompt was actually sent (zero if not sent)
	SentAt time.Time `json:"sent_at,omitempty"`
}

// NewSpawnState creates a new spawn state for tracking staggered prompts.
func NewSpawnState(batchID string, staggerSeconds int, totalAgents int) *SpawnState {
	return &SpawnState{
		BatchID:        batchID,
		StartedAt:      time.Now(),
		StaggerSeconds: staggerSeconds,
		TotalAgents:    totalAgents,
		Prompts:        make([]PromptStatus, 0, totalAgents),
	}
}

// AddPrompt adds a prompt to the schedule.
func (s *SpawnState) AddPrompt(pane, paneID string, order int, scheduledAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Prompts = append(s.Prompts, PromptStatus{
		Pane:        pane,
		PaneID:      paneID,
		Order:       order,
		ScheduledAt: scheduledAt,
		Sent:        false,
	})
}

// MarkSent marks a prompt as sent.
func (s *SpawnState) MarkSent(paneID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for i := range s.Prompts {
		if s.Prompts[i].PaneID == paneID {
			s.Prompts[i].Sent = true
			s.Prompts[i].SentAt = now
			break
		}
	}

	// Check if all prompts are sent
	allSent := true
	for _, p := range s.Prompts {
		if !p.Sent {
			allSent = false
			break
		}
	}
	if allSent && s.CompletedAt.IsZero() {
		s.CompletedAt = now
	}
}

// MarkComplete marks the spawn as complete.
func (s *SpawnState) MarkComplete() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.CompletedAt = time.Now()
}

// IsComplete returns whether all prompts have been delivered.
func (s *SpawnState) IsComplete() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return !s.CompletedAt.IsZero()
}

// PendingCount returns the number of prompts not yet sent.
func (s *SpawnState) PendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0
	for _, p := range s.Prompts {
		if !p.Sent {
			count++
		}
	}
	return count
}

// Save writes the spawn state to disk.
func (s *SpawnState) Save(projectDir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(projectDir, spawnStatePath)
	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create spawn state dir: %w", err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal spawn state: %w", err)
	}

	if err := util.AtomicWriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write spawn state: %w", err)
	}

	return nil
}

// TimeUntilNextPrompt returns the duration until the next pending prompt.
// Returns zero if all prompts are sent or if there's no pending prompt.
func (s *SpawnState) TimeUntilNextPrompt() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	var minDuration time.Duration

	for _, p := range s.Prompts {
		if !p.Sent && p.ScheduledAt.After(now) {
			remaining := p.ScheduledAt.Sub(now)
			if minDuration == 0 || remaining < minDuration {
				minDuration = remaining
			}
		}
	}

	return minDuration
}

// GetPromptStatuses returns a copy of all prompt statuses.
func (s *SpawnState) GetPromptStatuses() []PromptStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]PromptStatus, len(s.Prompts))
	copy(result, s.Prompts)
	return result
}

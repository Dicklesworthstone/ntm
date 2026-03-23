// Package completion provides detection for when agents complete their assigned work.
package completion

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// DetectionMethod describes how completion was detected
type DetectionMethod string

const (
	// MethodBeadClosed indicates the bead was detected as closed in br
	MethodBeadClosed DetectionMethod = "bead_closed"
	// MethodPatternMatch indicates completion phrase was found in output
	MethodPatternMatch DetectionMethod = "pattern_match"
	// MethodIdle indicates no activity for threshold duration
	MethodIdle DetectionMethod = "idle"
	// MethodAgentMail indicates agent sent completion message
	MethodAgentMail DetectionMethod = "agent_mail"
	// MethodPaneLost indicates the pane no longer exists
	MethodPaneLost DetectionMethod = "pane_lost"
)

// CompletionEvent represents a detected completion
type CompletionEvent struct {
	Pane       int             `json:"pane"`
	AgentType  string          `json:"agent_type"`
	BeadID     string          `json:"bead_id"`
	Method     DetectionMethod `json:"method"`
	Timestamp  time.Time       `json:"timestamp"`
	Duration   time.Duration   `json:"duration"`    // How long agent worked
	Output     string          `json:"output"`      // Last N lines (for debugging)
	IsFailed   bool            `json:"is_failed"`   // True if failure detected
	FailReason string          `json:"fail_reason"` // Reason for failure
}

// DetectionConfig configures the detector behavior
type DetectionConfig struct {
	PollInterval      time.Duration // Interval for bead status polling (default 5s)
	IdleThreshold     time.Duration // Duration of inactivity to consider complete (default 120s)
	RetryOnError      bool          // Retry failed checks (default true)
	RetryInterval     time.Duration // Time between retries (default 10s)
	MaxRetries        int           // Max retries before giving up (default 3)
	DedupWindow       time.Duration // Prevent duplicate events (default 5s)
	GracefulDegrading bool          // Fall back to lesser methods (default true)
	CaptureLines      int           // Lines to capture for pattern matching (default 50)
}

// DefaultConfig returns sensible default configuration
func DefaultConfig() DetectionConfig {
	return DetectionConfig{
		PollInterval:      5 * time.Second,
		IdleThreshold:     120 * time.Second,
		RetryOnError:      true,
		RetryInterval:     10 * time.Second,
		MaxRetries:        3,
		DedupWindow:       5 * time.Second,
		GracefulDegrading: true,
		CaptureLines:      50,
	}
}

// CompletionDetector monitors agents for work completion
type CompletionDetector struct {
	Session     string
	Config      DetectionConfig
	Store       *assignment.AssignmentStore
	Patterns    []*regexp.Regexp // Completion patterns
	FailPattern []*regexp.Regexp // Failure patterns

	mu              sync.RWMutex
	activityTracker map[int]*activityState // pane -> activity state
	recentEvents    map[string]time.Time   // beadID -> last event time (for dedup)
	brAvailable     *bool                  // nil = unknown, cached after first check
}

// activityState tracks output activity per pane
type activityState struct {
	lastOutputTime time.Time
	lastOutput     string
	burstStarted   time.Time
	burstActive    bool
}

// Default completion patterns (case-insensitive)
var defaultCompletionPatterns = []string{
	`(?i)bead\s+\S+\s+complete`,
	`(?i)task\s+\S+\s+(done|finished|complete)`,
	`(?i)closing\s+bead`,
	`(?i)br\s+(close|update.*closed)`,
	`(?i)marked\s+as\s+complete`,
	`(?i)successfully\s+completed`,
	`(?i)work\s+complete`,
	`(?i)finished\s+working`,
}

// Default failure patterns (case-insensitive)
var defaultFailurePatterns = []string{
	`(?i)unable\s+to\s+complete`,
	`(?i)cannot\s+proceed`,
	`(?i)blocked\s+by`,
	`(?i)giving\s+up`,
	`(?i)need\s+help`,
	`(?i)failed\s+to`,
	`(?i)error:.*fatal`,
	`(?i)aborting`,
}

// New creates a new CompletionDetector with default configuration
func New(session string, store *assignment.AssignmentStore) *CompletionDetector {
	return NewWithConfig(session, store, DefaultConfig())
}

// NewWithConfig creates a new CompletionDetector with custom configuration
func NewWithConfig(session string, store *assignment.AssignmentStore, cfg DetectionConfig) *CompletionDetector {
	d := &CompletionDetector{
		Session:         session,
		Config:          cfg,
		Store:           store,
		activityTracker: make(map[int]*activityState),
		recentEvents:    make(map[string]time.Time),
	}

	// Compile default patterns
	for _, p := range defaultCompletionPatterns {
		if re, err := regexp.Compile(p); err == nil {
			d.Patterns = append(d.Patterns, re)
		}
	}
	for _, p := range defaultFailurePatterns {
		if re, err := regexp.Compile(p); err == nil {
			d.FailPattern = append(d.FailPattern, re)
		}
	}

	return d
}

// AddPattern adds a custom completion pattern
func (d *CompletionDetector) AddPattern(pattern string) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("invalid pattern %q: %w", pattern, err)
	}
	d.mu.Lock()
	d.Patterns = append(d.Patterns, re)
	d.mu.Unlock()
	return nil
}

// AddFailurePattern adds a custom failure pattern
func (d *CompletionDetector) AddFailurePattern(pattern string) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("invalid pattern %q: %w", pattern, err)
	}
	d.mu.Lock()
	d.FailPattern = append(d.FailPattern, re)
	d.mu.Unlock()
	return nil
}

// Watch starts continuous monitoring and returns a channel of completion events.
// The channel is closed when the context is cancelled.
func (d *CompletionDetector) Watch(ctx context.Context) <-chan CompletionEvent {
	events := make(chan CompletionEvent, 10)

	go func() {
		defer close(events)

		ticker := time.NewTicker(d.Config.PollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.checkAll(ctx, events)
			}
		}
	}()

	return events
}

// checkAll checks all active assignments for completion
func (d *CompletionDetector) checkAll(ctx context.Context, events chan<- CompletionEvent) {
	if d.Store == nil {
		return
	}

	assignments := d.Store.ListActive()
	for _, a := range assignments {
		select {
		case <-ctx.Done():
			return
		default:
			if event := d.checkAssignment(ctx, a); event != nil {
				// Check dedup window
				d.mu.Lock()
				lastEvent, exists := d.recentEvents[a.BeadID]
				if exists && time.Since(lastEvent) < d.Config.DedupWindow {
					d.mu.Unlock()
					continue
				}
				d.recentEvents[a.BeadID] = time.Now()
				d.mu.Unlock()

				// Update assignment store
				if event.IsFailed {
					_ = d.Store.MarkFailed(a.BeadID, event.FailReason)
				} else {
					_ = d.Store.MarkCompleted(a.BeadID)
				}

				events <- *event
			}
		}
	}
}

// checkAssignment checks a single assignment for completion
func (d *CompletionDetector) checkAssignment(ctx context.Context, a *assignment.Assignment) *CompletionEvent {
	startTime := a.AssignedAt
	if a.StartedAt != nil {
		startTime = *a.StartedAt
	}

	// Build tmux target
	target := fmt.Sprintf("%s.%d", d.Session, a.Pane)

	// 1. Check if pane exists
	panes, err := tmux.GetPanes(d.Session)
	if err != nil {
		return nil // Can't check, try later
	}

	paneExists := false
	for _, p := range panes {
		if p.Index == a.Pane {
			paneExists = true
			break
		}
	}

	if !paneExists {
		return &CompletionEvent{
			Pane:       a.Pane,
			AgentType:  a.AgentType,
			BeadID:     a.BeadID,
			Method:     MethodPaneLost,
			Timestamp:  time.Now(),
			Duration:   time.Since(startTime),
			IsFailed:   true,
			FailReason: "pane no longer exists (agent crashed)",
		}
	}

	// 2. Check bead status via br (most reliable)
	if d.isBrAvailable() {
		if closed, err := d.checkBeadClosed(ctx, a.BeadID); err == nil && closed {
			output, _ := tmux.CapturePaneOutput(target, d.Config.CaptureLines)
			return &CompletionEvent{
				Pane:      a.Pane,
				AgentType: a.AgentType,
				BeadID:    a.BeadID,
				Method:    MethodBeadClosed,
				Timestamp: time.Now(),
				Duration:  time.Since(startTime),
				Output:    truncateOutput(output, 500),
			}
		}
	}

	// 3. Capture pane output for pattern/idle detection
	output, err := tmux.CapturePaneOutput(target, d.Config.CaptureLines)
	if err != nil {
		// Can't capture, rely on bead polling
		return nil
	}

	// 4. Check for failure patterns
	if reason := d.matchFailurePatterns(output); reason != "" {
		return &CompletionEvent{
			Pane:       a.Pane,
			AgentType:  a.AgentType,
			BeadID:     a.BeadID,
			Method:     MethodPatternMatch,
			Timestamp:  time.Now(),
			Duration:   time.Since(startTime),
			Output:     truncateOutput(output, 500),
			IsFailed:   true,
			FailReason: reason,
		}
	}

	// 5. Check for completion patterns
	if d.matchCompletionPatterns(output) {
		return &CompletionEvent{
			Pane:      a.Pane,
			AgentType: a.AgentType,
			BeadID:    a.BeadID,
			Method:    MethodPatternMatch,
			Timestamp: time.Now(),
			Duration:  time.Since(startTime),
			Output:    truncateOutput(output, 500),
		}
	}

	// 6. Check idle detection
	if event := d.checkIdle(a, output, startTime); event != nil {
		return event
	}

	return nil
}

// CheckNow performs an immediate check for a specific pane
func (d *CompletionDetector) CheckNow(pane int) (*CompletionEvent, error) {
	if d.Store == nil {
		return nil, fmt.Errorf("no assignment store configured")
	}

	// Find assignment for this pane
	var target *assignment.Assignment
	for _, a := range d.Store.ListActive() {
		if a.Pane == pane {
			target = a
			break
		}
	}

	if target == nil {
		return nil, fmt.Errorf("no active assignment for pane %d", pane)
	}

	return d.checkAssignment(context.Background(), target), nil
}

// isBrAvailable checks if the br CLI is available (cached)
func (d *CompletionDetector) isBrAvailable() bool {
	d.mu.RLock()
	if d.brAvailable != nil {
		result := *d.brAvailable
		d.mu.RUnlock()
		return result
	}
	d.mu.RUnlock()

	// Check availability
	d.mu.Lock()
	defer d.mu.Unlock()

	// Double-check after acquiring write lock
	if d.brAvailable != nil {
		return *d.brAvailable
	}

	_, err := exec.LookPath("br")
	available := err == nil
	d.brAvailable = &available

	if !available && d.Config.GracefulDegrading {
		fmt.Println("[DETECT] br CLI unavailable, using pattern matching and idle detection")
	}

	return available
}

// checkBeadClosed uses br CLI to check if a bead is closed
func (d *CompletionDetector) checkBeadClosed(ctx context.Context, beadID string) (bool, error) {
	cmd := exec.CommandContext(ctx, "br", "show", beadID, "--json")
	cmd.WaitDelay = 2 * time.Second
	output, err := cmd.Output()
	if err != nil {
		return false, err
	}

	// Check for closed status in JSON output
	outputStr := string(output)
	return strings.Contains(outputStr, `"status":"closed"`) ||
		strings.Contains(outputStr, `"status": "closed"`), nil
}

// matchCompletionPatterns checks output against completion patterns
func (d *CompletionDetector) matchCompletionPatterns(output string) bool {
	d.mu.RLock()
	patterns := d.Patterns
	d.mu.RUnlock()

	for _, re := range patterns {
		if re.MatchString(output) {
			return true
		}
	}
	return false
}

// matchFailurePatterns checks output against failure patterns, returns matched reason
func (d *CompletionDetector) matchFailurePatterns(output string) string {
	d.mu.RLock()
	patterns := d.FailPattern
	d.mu.RUnlock()

	for _, re := range patterns {
		if match := re.FindString(output); match != "" {
			return match
		}
	}
	return ""
}

// checkIdle detects completion via inactivity
func (d *CompletionDetector) checkIdle(a *assignment.Assignment, output string, startTime time.Time) *CompletionEvent {
	d.mu.Lock()
	defer d.mu.Unlock()

	state, exists := d.activityTracker[a.Pane]
	if !exists {
		state = &activityState{
			lastOutputTime: time.Now(),
			lastOutput:     output,
		}
		d.activityTracker[a.Pane] = state
		return nil
	}

	// Check if output changed
	if output != state.lastOutput {
		// Activity detected
		state.lastOutput = output
		state.lastOutputTime = time.Now()

		// Check for activity burst start
		if !state.burstActive {
			state.burstActive = true
			state.burstStarted = time.Now()
		}
		return nil
	}

	// Output unchanged - check for idle timeout after burst
	if state.burstActive && time.Since(state.lastOutputTime) >= d.Config.IdleThreshold {
		// Reset state
		state.burstActive = false

		return &CompletionEvent{
			Pane:      a.Pane,
			AgentType: a.AgentType,
			BeadID:    a.BeadID,
			Method:    MethodIdle,
			Timestamp: time.Now(),
			Duration:  time.Since(startTime),
			Output:    truncateOutput(output, 500),
		}
	}

	return nil
}

// truncateOutput limits output to maxLen characters
func truncateOutput(output string, maxLen int) string {
	if len(output) <= maxLen {
		return output
	}
	return "..." + output[len(output)-maxLen:]
}

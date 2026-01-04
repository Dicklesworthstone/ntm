package status

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const (
	// DefaultRecoveryPrompt is sent to agents when compaction is detected.
	// This prompts Claude Code to re-read the project context.
	DefaultRecoveryPrompt = "Reread AGENTS.md so it's still fresh in your mind. Use ultrathink."

	// DefaultCooldown prevents spamming recovery prompts.
	DefaultCooldown = 30 * time.Second

	// DefaultMaxRecoveriesPerPane limits recovery attempts.
	DefaultMaxRecoveriesPerPane = 5
)

// RecoveryEvent records when a recovery prompt was sent.
type RecoveryEvent struct {
	PaneID      string    `json:"pane_id"`
	Session     string    `json:"session"`
	PaneIndex   int       `json:"pane_index"`
	SentAt      time.Time `json:"sent_at"`
	Prompt      string    `json:"prompt"`
	TriggerText string    `json:"trigger_text"` // The compaction text that triggered recovery
}

// RecoveryManager handles sending recovery prompts with cooldown protection.
type RecoveryManager struct {
	mu                 sync.RWMutex
	lastRecovery       map[string]time.Time // paneID -> last recovery time
	recoveryCount      map[string]int       // paneID -> number of recoveries
	recoveryEvents     []RecoveryEvent      // History of recovery events
	cooldown           time.Duration
	prompt             string
	maxRecoveries      int
	maxEventAge        time.Duration
	includeBeadContext bool
}

// RecoveryConfig holds configuration for recovery behavior.
type RecoveryConfig struct {
	Cooldown           time.Duration // Minimum time between recovery prompts
	Prompt             string        // The recovery prompt to send
	MaxRecoveries      int           // Max recoveries per pane before giving up
	MaxEventAge        time.Duration // How long to keep recovery events
	IncludeBeadContext bool          // Whether to include bead context in prompts
}

// DefaultRecoveryConfig returns default recovery configuration.
func DefaultRecoveryConfig() RecoveryConfig {
	return RecoveryConfig{
		Cooldown:           DefaultCooldown,
		Prompt:             DefaultRecoveryPrompt,
		MaxRecoveries:      DefaultMaxRecoveriesPerPane,
		MaxEventAge:        10 * time.Minute,
		IncludeBeadContext: true, // Enable by default
	}
}

// NewRecoveryManager creates a new recovery manager.
func NewRecoveryManager(config RecoveryConfig) *RecoveryManager {
	if config.Cooldown == 0 {
		config.Cooldown = DefaultCooldown
	}
	if config.Prompt == "" {
		config.Prompt = DefaultRecoveryPrompt
	}
	if config.MaxRecoveries == 0 {
		config.MaxRecoveries = DefaultMaxRecoveriesPerPane
	}
	if config.MaxEventAge == 0 {
		config.MaxEventAge = 10 * time.Minute
	}

	return &RecoveryManager{
		lastRecovery:       make(map[string]time.Time),
		recoveryCount:      make(map[string]int),
		recoveryEvents:     make([]RecoveryEvent, 0),
		cooldown:           config.Cooldown,
		prompt:             config.Prompt,
		maxRecoveries:      config.MaxRecoveries,
		maxEventAge:        config.MaxEventAge,
		includeBeadContext: config.IncludeBeadContext,
	}
}

// NewRecoveryManagerDefault creates a recovery manager with default config.
func NewRecoveryManagerDefault() *RecoveryManager {
	return NewRecoveryManager(DefaultRecoveryConfig())
}

// CanSendRecovery checks if a recovery prompt can be sent to a pane.
func (rm *RecoveryManager) CanSendRecovery(paneID string) (bool, string) {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	// Check cooldown
	if last, ok := rm.lastRecovery[paneID]; ok {
		remaining := rm.cooldown - time.Since(last)
		if remaining > 0 {
			return false, fmt.Sprintf("cooldown: %s remaining", remaining.Round(time.Second))
		}
	}

	// Check max recoveries
	if count := rm.recoveryCount[paneID]; count >= rm.maxRecoveries {
		return false, fmt.Sprintf("max recoveries reached: %d/%d", count, rm.maxRecoveries)
	}

	return true, ""
}

// SendRecoveryPrompt sends the recovery prompt if cooldown has passed.
// Returns true if the prompt was sent, false if skipped.
func (rm *RecoveryManager) SendRecoveryPrompt(session string, paneIndex int) (bool, error) {
	paneID := makePaneID(session, paneIndex)
	return rm.SendRecoveryPromptByID(session, paneIndex, paneID, "")
}

// SendRecoveryPromptByID sends recovery with explicit pane ID.
// It executes asynchronously to avoid blocking the UI during prompt construction.
func (rm *RecoveryManager) SendRecoveryPromptByID(session string, paneIndex int, paneID, triggerText string) (bool, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	// Check cooldown
	if last, ok := rm.lastRecovery[paneID]; ok {
		if time.Since(last) < rm.cooldown {
			return false, nil // Still in cooldown, skip
		}
	}

	// Check max recoveries
	if count := rm.recoveryCount[paneID]; count >= rm.maxRecoveries {
		return false, nil // Max recoveries reached
	}

	// Update state immediately to prevent re-entry
	now := time.Now()
	rm.lastRecovery[paneID] = now
	rm.recoveryCount[paneID]++

	// Build target for tmux (session:pane_index)
	target := fmt.Sprintf("%s:%d", session, paneIndex)

	// Capture values while holding lock to avoid data race with SetPrompt()
	prompt := rm.prompt
	includeBeadContext := rm.includeBeadContext

	// Execute recovery asynchronously
	go func() {
		// Build prompt with optional bead context (potentially slow)
		promptToSend := BuildContextAwarePrompt(prompt, includeBeadContext)

		// Send the recovery prompt
		if err := tmux.SendKeys(target, promptToSend, true); err != nil {
			// Log error if possible, or just fail silently
			// Since we can't return the error, we rely on the next check to retry (after cooldown)
			return
		}

		// Record event after successful send
		rm.mu.Lock()
		defer rm.mu.Unlock()
		rm.recoveryEvents = append(rm.recoveryEvents, RecoveryEvent{
			PaneID:      paneID,
			Session:     session,
			PaneIndex:   paneIndex,
			SentAt:      now,
			Prompt:      rm.prompt,
			TriggerText: triggerText,
		})
		rm.pruneEvents()
	}()

	return true, nil
}

// HandleCompactionEvent processes a compaction event and sends recovery if appropriate.
func (rm *RecoveryManager) HandleCompactionEvent(event *CompactionEvent, session string, paneIndex int) (bool, error) {
	if event == nil {
		return false, nil
	}

	paneID := makePaneID(session, paneIndex)
	event.PaneID = paneID

	return rm.SendRecoveryPromptByID(session, paneIndex, paneID, event.MatchedText)
}

// GetRecoveryEvents returns recent recovery events.
func (rm *RecoveryManager) GetRecoveryEvents() []RecoveryEvent {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	rm.pruneEvents()
	result := make([]RecoveryEvent, len(rm.recoveryEvents))
	copy(result, rm.recoveryEvents)
	return result
}

// GetRecoveryCount returns the number of recoveries for a pane.
func (rm *RecoveryManager) GetRecoveryCount(paneID string) int {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.recoveryCount[paneID]
}

// GetLastRecoveryTime returns when recovery was last sent to a pane.
func (rm *RecoveryManager) GetLastRecoveryTime(paneID string) (time.Time, bool) {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	t, ok := rm.lastRecovery[paneID]
	return t, ok
}

// ResetPane resets the recovery state for a pane (e.g., after user intervention).
func (rm *RecoveryManager) ResetPane(paneID string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	delete(rm.lastRecovery, paneID)
	delete(rm.recoveryCount, paneID)
}

// ResetAll clears all recovery state.
func (rm *RecoveryManager) ResetAll() {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.lastRecovery = make(map[string]time.Time)
	rm.recoveryCount = make(map[string]int)
	rm.recoveryEvents = make([]RecoveryEvent, 0)
}

// SetPrompt updates the recovery prompt.
func (rm *RecoveryManager) SetPrompt(prompt string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.prompt = prompt
}

// SetCooldown updates the cooldown duration.
func (rm *RecoveryManager) SetCooldown(cooldown time.Duration) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.cooldown = cooldown
}

// pruneEvents removes old events (must be called with lock held).
func (rm *RecoveryManager) pruneEvents() {
	cutoff := time.Now().Add(-rm.maxEventAge)
	kept := make([]RecoveryEvent, 0, len(rm.recoveryEvents))
	for _, e := range rm.recoveryEvents {
		if e.SentAt.After(cutoff) {
			kept = append(kept, e)
		}
	}
	rm.recoveryEvents = kept
}

// makePaneID creates a consistent pane ID.
func makePaneID(session string, paneIndex int) string {
	return fmt.Sprintf("%s:%d", session, paneIndex)
}

// BeadContext contains bead-related context for recovery prompts
type BeadContext struct {
	TopBottlenecks []string // Top bottleneck issue IDs
	NextActions    []string // Recommended next actions
	HealthStatus   string   // Project health summary
	HasDrift       bool     // Whether drift was detected
	// Dependency context
	InProgressTasks []string // Currently in-progress tasks
	BlockedCount    int      // Number of blocked issues
	ReadyCount      int      // Number of ready issues
	TopBlockers     []string // Top issues blocking progress
}

// BuildContextAwarePrompt creates a recovery prompt that includes bead context
func BuildContextAwarePrompt(basePrompt string, includeBeadContext bool) string {
	if !includeBeadContext {
		return basePrompt
	}

	ctx := GetBeadContext()
	if ctx == nil {
		return basePrompt
	}

	var sb strings.Builder
	sb.WriteString(basePrompt)

	// Add bead context
	sb.WriteString("\n\n# Project Context from Beads\n")

	if len(ctx.TopBottlenecks) > 0 {
		sb.WriteString("\n## Current Bottlenecks (resolve these to unblock progress):\n")
		for _, b := range ctx.TopBottlenecks {
			sb.WriteString(fmt.Sprintf("- %s\n", b))
		}
	}

	if len(ctx.NextActions) > 0 {
		sb.WriteString("\n## Recommended Next Actions:\n")
		for _, a := range ctx.NextActions {
			sb.WriteString(fmt.Sprintf("- %s\n", a))
		}
	}

	if ctx.HealthStatus != "" {
		sb.WriteString(fmt.Sprintf("\n## Project Health: %s\n", ctx.HealthStatus))
	}

	if ctx.HasDrift {
		sb.WriteString("\n**Warning**: Project has drifted from baseline. Consider running `bv` to review.\n")
	}

	// Add dependency/blocker context
	if len(ctx.InProgressTasks) > 0 || ctx.BlockedCount > 0 || len(ctx.TopBlockers) > 0 {
		sb.WriteString("\n## Dependency Summary\n")

		if len(ctx.InProgressTasks) > 0 {
			sb.WriteString("\n### Tasks In Progress:\n")
			for _, task := range ctx.InProgressTasks {
				sb.WriteString(fmt.Sprintf("- %s\n", task))
			}
		}

		if ctx.BlockedCount > 0 || ctx.ReadyCount > 0 {
			sb.WriteString(fmt.Sprintf("\n**Status**: %d blocked, %d ready to work on\n", ctx.BlockedCount, ctx.ReadyCount))
		}

		if len(ctx.TopBlockers) > 0 {
			sb.WriteString("\n### Top Blockers (completing these unblocks many tasks):\n")
			for _, blocker := range ctx.TopBlockers {
				sb.WriteString(fmt.Sprintf("- %s\n", blocker))
			}
		}
	}

	return sb.String()
}

// GetBeadContext retrieves current bead context from bv
func GetBeadContext() *BeadContext {
	if !bv.IsInstalled() {
		return nil
	}

	ctx := &BeadContext{}

	// Get top bottlenecks
	bottlenecks, err := bv.GetTopBottlenecks("", 3)
	if err == nil && len(bottlenecks) > 0 {
		for _, b := range bottlenecks {
			ctx.TopBottlenecks = append(ctx.TopBottlenecks, b.ID)
		}
	}

	// Get recommended actions
	actions, err := bv.GetNextActions("", 3)
	if err == nil && len(actions) > 0 {
		for _, a := range actions {
			ctx.NextActions = append(ctx.NextActions, fmt.Sprintf("[%s] %s", a.IssueID, a.Title))
		}
	}

	// Get health summary
	health, err := bv.GetHealthSummary("")
	if err == nil && health != nil {
		ctx.HealthStatus = health.DriftStatus.String()
		ctx.HasDrift = health.DriftStatus == bv.DriftCritical || health.DriftStatus == bv.DriftWarning
	}

	// Get dependency context (from bd, not bv)
	if bv.IsBdInstalled() {
		depCtx, err := bv.GetDependencyContext("", 5)
		if err == nil && depCtx != nil {
			ctx.BlockedCount = depCtx.BlockedCount
			ctx.ReadyCount = depCtx.ReadyCount

			// Format in-progress tasks
			for _, task := range depCtx.InProgressTasks {
				ctx.InProgressTasks = append(ctx.InProgressTasks,
					fmt.Sprintf("[%s] %s", task.ID, task.Title))
			}

			// Format top blockers
			for _, blocker := range depCtx.TopBlockers {
				blockedByStr := ""
				if len(blocker.BlockedBy) > 0 {
					blockedByStr = fmt.Sprintf(" (blocked by: %s)", strings.Join(blocker.BlockedBy, ", "))
				}
				ctx.TopBlockers = append(ctx.TopBlockers,
					fmt.Sprintf("[%s] %s%s", blocker.ID, blocker.Title, blockedByStr))
			}
		}
	}

	return ctx
}

// CompactionRecoveryIntegration combines compaction detection and recovery.
type CompactionRecoveryIntegration struct {
	detector *CompactionDetector
	recovery *RecoveryManager
}

// NewCompactionRecoveryIntegration creates an integrated compaction recovery system.
func NewCompactionRecoveryIntegration(recoveryConfig RecoveryConfig) *CompactionRecoveryIntegration {
	return &CompactionRecoveryIntegration{
		detector: NewCompactionDetector(5 * time.Minute),
		recovery: NewRecoveryManager(recoveryConfig),
	}
}

// NewCompactionRecoveryIntegrationDefault creates with default config.
func NewCompactionRecoveryIntegrationDefault() *CompactionRecoveryIntegration {
	return NewCompactionRecoveryIntegration(DefaultRecoveryConfig())
}

// CheckAndRecover checks for compaction and sends recovery if needed.
// Returns the compaction event (if any) and whether recovery was sent.
func (cri *CompactionRecoveryIntegration) CheckAndRecover(output, agentType, session string, paneIndex int) (*CompactionEvent, bool, error) {
	paneID := makePaneID(session, paneIndex)

	// Check for compaction
	event := cri.detector.Check(output, agentType, paneID)
	if event == nil {
		return nil, false, nil
	}

	// Send recovery
	sent, err := cri.recovery.HandleCompactionEvent(event, session, paneIndex)
	return event, sent, err
}

// Detector returns the compaction detector.
func (cri *CompactionRecoveryIntegration) Detector() *CompactionDetector {
	return cri.detector
}

// Recovery returns the recovery manager.
func (cri *CompactionRecoveryIntegration) Recovery() *RecoveryManager {
	return cri.recovery
}

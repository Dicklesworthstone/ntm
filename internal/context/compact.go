// Package context provides context window monitoring for AI agent orchestration.
// compact.go implements graceful degradation strategies before rotation.
package context

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

// CompactionMethod identifies the compaction strategy used.
type CompactionMethod string

const (
	// CompactionBuiltin uses the agent's built-in /compact command
	CompactionBuiltin CompactionMethod = "builtin"
	// CompactionSummarize asks the agent to summarize conversation
	CompactionSummarize CompactionMethod = "summarize"
	// CompactionClearHistory clears non-essential history
	CompactionClearHistory CompactionMethod = "clear_history"
	// CompactionFailed indicates compaction was attempted but failed
	CompactionFailed CompactionMethod = "failed"
)

// CompactionResult contains the outcome of a compaction attempt.
type CompactionResult struct {
	Success         bool             `json:"success"`
	Method          CompactionMethod `json:"method"`
	TokensBefore    int64            `json:"tokens_before"`
	TokensAfter     int64            `json:"tokens_after"`
	TokensReclaimed int64            `json:"tokens_reclaimed"`
	UsageBefore     float64          `json:"usage_before"`
	UsageAfter      float64          `json:"usage_after"`
	Duration        time.Duration    `json:"duration"`
	Error           string           `json:"error,omitempty"`
}

// Compactor handles graceful degradation of context before rotation.
type Compactor struct {
	monitor          *ContextMonitor
	minReduction     float64       // Minimum usage reduction to consider success (e.g., 0.10 = 10%)
	builtinTimeout   time.Duration // Timeout for builtin compaction
	summarizeTimeout time.Duration // Timeout for summarization request
}

// CompactorConfig holds configuration for the Compactor.
type CompactorConfig struct {
	MinReduction     float64       // Minimum usage reduction to consider success (default: 0.10)
	BuiltinTimeout   time.Duration // Timeout for builtin compaction (default: 10s)
	SummarizeTimeout time.Duration // Timeout for summarization (default: 30s)
}

// DefaultCompactorConfig returns sensible defaults.
func DefaultCompactorConfig() CompactorConfig {
	return CompactorConfig{
		MinReduction:     0.10,
		BuiltinTimeout:   10 * time.Second,
		SummarizeTimeout: 30 * time.Second,
	}
}

// NewCompactor creates a new Compactor with the given configuration.
func NewCompactor(monitor *ContextMonitor, cfg CompactorConfig) *Compactor {
	if cfg.MinReduction <= 0 {
		cfg.MinReduction = 0.10
	}
	if cfg.BuiltinTimeout <= 0 {
		cfg.BuiltinTimeout = 10 * time.Second
	}
	if cfg.SummarizeTimeout <= 0 {
		cfg.SummarizeTimeout = 30 * time.Second
	}
	return &Compactor{
		monitor:          monitor,
		minReduction:     cfg.MinReduction,
		builtinTimeout:   cfg.BuiltinTimeout,
		summarizeTimeout: cfg.SummarizeTimeout,
	}
}

// AgentCapabilities describes what compaction features an agent supports.
type AgentCapabilities struct {
	SupportsBuiltinCompact bool
	SupportsHistoryClear   bool
	BuiltinCompactCommand  string // e.g., "/compact"
	HistoryClearCommand    string // e.g., "/clear"
}

// GetAgentCapabilities returns the compaction capabilities for an agent type.
func GetAgentCapabilities(agentType string) AgentCapabilities {
	switch agent.AgentType(agentType).Canonical() {
	case agent.AgentTypeClaudeCode:
		return AgentCapabilities{
			SupportsBuiltinCompact: true,
			SupportsHistoryClear:   true,
			BuiltinCompactCommand:  "/compact",
			HistoryClearCommand:    "/clear",
		}
	case agent.AgentTypeCodex:
		return AgentCapabilities{
			SupportsBuiltinCompact: false, // Codex doesn't have /compact
			SupportsHistoryClear:   false,
			BuiltinCompactCommand:  "",
			HistoryClearCommand:    "",
		}
	case agent.AgentTypeGemini, agent.AgentTypeAntigravity:
		return AgentCapabilities{
			SupportsBuiltinCompact: false, // Gemini/Antigravity CLI doesn't have /compact
			SupportsHistoryClear:   true,
			BuiltinCompactCommand:  "",
			HistoryClearCommand:    "/clear",
		}
	case agent.AgentTypeOmp:
		// omp ships both as slash commands (omp v18.2.3): "/compact"
		// ("Compact: context N% used") and "/clear" ("drop context, keep
		// session").
		return AgentCapabilities{
			SupportsBuiltinCompact: true,
			SupportsHistoryClear:   true,
			BuiltinCompactCommand:  "/compact",
			HistoryClearCommand:    "/clear",
		}
	case agent.AgentTypeCursor, agent.AgentTypeWindsurf, agent.AgentTypeAider, agent.AgentTypeOpencode, agent.AgentTypeOllama:
		// These IDE-based agents typically manage their own context or don't support explicit compaction commands yet
		return AgentCapabilities{
			SupportsBuiltinCompact: false,
			SupportsHistoryClear:   false,
			BuiltinCompactCommand:  "",
			HistoryClearCommand:    "",
		}
	default:
		return AgentCapabilities{}
	}
}

// CompactionPromptTemplate is the prompt for requesting a summarization compaction.
const CompactionPromptTemplate = `[System Context Management]

Your context window is reaching capacity. To continue working effectively, please:

1. SUMMARIZE the key points of our conversation so far
2. NOTE any critical context that must be preserved (file paths, decisions, blockers)
3. LIST any in-progress tasks and their current state

After you provide this summary, we will use it to help you continue with fresh context.

Please provide this summary now. Be concise but comprehensive.`

// GenerateCompactionPrompt returns the prompt for requesting summarization.
func (c *Compactor) GenerateCompactionPrompt() string {
	return CompactionPromptTemplate
}

// CompactionCommand represents a command to send to an agent for compaction.
type CompactionCommand struct {
	Command     string // The command/text to send
	Method      CompactionMethod
	IsPrompt    bool // True if this is a prompt, false if a slash command
	WaitTime    time.Duration
	Description string
}

func compactionMethodForCommand(cmd CompactionCommand) CompactionMethod {
	if cmd.Method != "" {
		return cmd.Method
	}
	if cmd.IsPrompt {
		return CompactionSummarize
	}
	return CompactionBuiltin
}

// GetCompactionCommands returns the sequence of commands to try for compaction.
func (c *Compactor) GetCompactionCommands(agentType string) []CompactionCommand {
	caps := GetAgentCapabilities(agentType)
	var commands []CompactionCommand

	// Try builtin compaction first if available.
	if caps.SupportsBuiltinCompact && caps.BuiltinCompactCommand != "" {
		commands = append(commands, CompactionCommand{
			Command:     caps.BuiltinCompactCommand,
			Method:      CompactionBuiltin,
			IsPrompt:    false,
			WaitTime:    c.builtinTimeout,
			Description: "builtin compaction command",
		})
	}

	// If the agent supports clearing non-essential history, try that before asking
	// for a manual summary. This gives agents with native history pruning a cheaper
	// fallback than a full summarization round-trip.
	if caps.SupportsHistoryClear && caps.HistoryClearCommand != "" {
		commands = append(commands, CompactionCommand{
			Command:     caps.HistoryClearCommand,
			Method:      CompactionClearHistory,
			IsPrompt:    false,
			WaitTime:    c.builtinTimeout,
			Description: "history clear command",
		})
	}

	// Fallback to summarization request.
	commands = append(commands, CompactionCommand{
		Command:     c.GenerateCompactionPrompt(),
		Method:      CompactionSummarize,
		IsPrompt:    true,
		WaitTime:    c.summarizeTimeout,
		Description: "summarization request",
	})

	return commands
}

// ShouldTryCompaction returns true if compaction should be attempted for the agent.
// warningThreshold is a decimal (e.g., 0.80 for 80%).
func (c *Compactor) ShouldTryCompaction(agentID string, warningThreshold float64) (bool, string) {
	if c.monitor == nil {
		return false, "no monitor available"
	}

	state := c.monitor.GetState(agentID)
	if state == nil {
		return false, "agent not registered"
	}

	// Get the best estimate
	estimate := c.monitor.GetEstimate(agentID)
	if estimate == nil {
		return false, "cannot estimate context: no estimate available"
	}

	// UsagePercent from monitor is already a percentage (e.g., 80.0 for 80%)
	// Convert threshold to percentage for comparison
	thresholdPercent := warningThreshold * 100

	if estimate.UsagePercent >= thresholdPercent {
		return true, fmt.Sprintf("usage %.1f%% >= warning threshold %.1f%%",
			estimate.UsagePercent, thresholdPercent)
	}

	return false, fmt.Sprintf("usage %.1f%% < warning threshold %.1f%%",
		estimate.UsagePercent, thresholdPercent)
}

// EvaluateCompactionResult determines if the compaction was successful.
func (c *Compactor) EvaluateCompactionResult(before, after *ContextEstimate) *CompactionResult {
	result := &CompactionResult{
		TokensBefore: before.TokensUsed,
		TokensAfter:  after.TokensUsed,
		UsageBefore:  before.UsagePercent,
		UsageAfter:   after.UsagePercent,
	}

	reduction := before.UsagePercent - after.UsagePercent
	result.TokensReclaimed = before.TokensUsed - after.TokensUsed

	// minReduction is a decimal (e.g., 0.10 for 10%), but UsagePercent is 0-100
	// Convert minReduction to percentage points for comparison
	minReductionPercent := c.minReduction * 100

	if reduction >= minReductionPercent {
		result.Success = true
	} else if result.TokensReclaimed > 0 {
		// Some tokens reclaimed but not enough
		result.Success = false
		result.Error = fmt.Sprintf("insufficient reduction: %.1f%% (need %.1f%%)",
			reduction, minReductionPercent)
	} else {
		result.Success = false
		result.Error = "no reduction achieved"
	}

	return result
}

// CompactionState tracks the state of a compaction attempt.
type CompactionState struct {
	AgentID        string
	StartedAt      time.Time
	Method         CompactionMethod
	CommandsSent   int
	LastCommand    string
	WaitingUntil   time.Time
	EstimateBefore *ContextEstimate
}

// NewCompactionState creates a new state for tracking a compaction attempt.
func (c *Compactor) NewCompactionState(agentID string) (*CompactionState, error) {
	if c.monitor == nil {
		return nil, fmt.Errorf("no monitor available")
	}

	estimate := c.monitor.GetEstimate(agentID)
	if estimate == nil {
		return nil, fmt.Errorf("failed to get initial estimate: agent not found or no data")
	}

	return &CompactionState{
		AgentID:        agentID,
		StartedAt:      time.Now(),
		EstimateBefore: estimate,
	}, nil
}

// UpdateState updates the compaction state after sending a command.
func (state *CompactionState) UpdateState(cmd CompactionCommand, method CompactionMethod) {
	state.CommandsSent++
	state.LastCommand = cmd.Command
	state.Method = method
	state.WaitingUntil = time.Now().Add(cmd.WaitTime)
}

// FinishCompaction completes a compaction attempt and returns the result.
func (c *Compactor) FinishCompaction(state *CompactionState) (*CompactionResult, error) {
	if c.monitor == nil {
		return &CompactionResult{
			Success: false,
			Method:  state.Method,
			Error:   "no monitor available",
		}, fmt.Errorf("no monitor available")
	}

	afterEstimate := c.monitor.GetEstimate(state.AgentID)
	if afterEstimate == nil {
		return &CompactionResult{
			Success:     false,
			Method:      state.Method,
			UsageBefore: state.EstimateBefore.UsagePercent,
			Error:       "failed to get post-compaction estimate: no data available",
		}, fmt.Errorf("no estimate available after compaction")
	}

	result := c.EvaluateCompactionResult(state.EstimateBefore, afterEstimate)
	result.Method = state.Method
	result.Duration = time.Since(state.StartedAt)

	return result, nil
}

// FormatCompactionResult formats the result for display.
func (r *CompactionResult) FormatForDisplay() string {
	var sb strings.Builder

	if r.Success {
		sb.WriteString("✓ Compaction successful\n")
	} else {
		sb.WriteString("✗ Compaction failed\n")
	}

	sb.WriteString(fmt.Sprintf("  Method: %s\n", r.Method))
	// UsageBefore/UsageAfter are already 0-100 percentages from ContextEstimate.UsagePercent
	sb.WriteString(fmt.Sprintf("  Usage: %.1f%% → %.1f%%\n", r.UsageBefore, r.UsageAfter))
	sb.WriteString(fmt.Sprintf("  Tokens reclaimed: %d\n", r.TokensReclaimed))
	sb.WriteString(fmt.Sprintf("  Duration: %s\n", r.Duration.Round(time.Millisecond)))

	if r.Error != "" {
		sb.WriteString(fmt.Sprintf("  Error: %s\n", r.Error))
	}

	return sb.String()
}

// PreRotationCheck performs a final check before rotation is executed.
// Returns true if rotation should proceed, false if compaction helped enough.
// rotateThreshold is a decimal (e.g., 0.95 for 95%).
func (c *Compactor) PreRotationCheck(
	agentID string,
	rotateThreshold float64,
	lastCompactionResult *CompactionResult,
) (shouldRotate bool, reason string) {
	if c.monitor == nil {
		return true, "no monitor available, proceeding with rotation"
	}

	estimate := c.monitor.GetEstimate(agentID)
	if estimate == nil {
		return true, "cannot estimate context, proceeding with rotation: no estimate available"
	}

	// UsagePercent from monitor is already a percentage (e.g., 80.0 for 80%)
	// Convert threshold to percentage for comparison
	thresholdPercent := rotateThreshold * 100

	// Check if we're still above the rotation threshold
	if estimate.UsagePercent >= thresholdPercent {
		if lastCompactionResult != nil && lastCompactionResult.Success {
			// UsageBefore/UsageAfter are already 0-100 percentages, no need to multiply
			return true, fmt.Sprintf("compaction helped (%.1f%% freed) but still at %.1f%% >= %.1f%%",
				lastCompactionResult.UsageBefore-lastCompactionResult.UsageAfter,
				estimate.UsagePercent, thresholdPercent)
		}
		return true, fmt.Sprintf("context usage %.1f%% >= rotation threshold %.1f%%",
			estimate.UsagePercent, thresholdPercent)
	}

	// Compaction brought us below threshold
	return false, fmt.Sprintf("compaction reduced usage to %.1f%% < rotation threshold %.1f%%, rotation not needed",
		estimate.UsagePercent, thresholdPercent)
}

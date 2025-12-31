// Package context provides context window monitoring for AI agent orchestration.
// monitor.go implements multi-source estimation of context window usage.
package context

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ContextLimits maps model names to their context window sizes in tokens.
// These are approximate values based on published specifications.
var ContextLimits = map[string]int64{
	// Claude models
	"claude-sonnet-4":       200000,
	"claude-opus-4":         200000,
	"claude-opus-4.5":       200000,
	"claude-haiku":          200000,
	"claude-3-opus":         200000,
	"claude-3-sonnet":       200000,
	"claude-3-haiku":        200000,
	"claude-3.5-sonnet":     200000,
	"claude-3.5-haiku":      200000,
	"claude-sonnet-4-5":     200000,
	"claude-opus-4-5":       200000,

	// OpenAI models
	"gpt-4":           128000,
	"gpt-4-turbo":     128000,
	"gpt-4o":          128000,
	"gpt-4o-mini":     128000,
	"gpt-5":           256000,
	"gpt-5-codex":     256000,
	"o1":              128000,
	"o1-mini":         128000,
	"o1-preview":      128000,
	"o3-mini":         200000,

	// Google models
	"gemini-2.0-flash":       1000000,
	"gemini-2.0-flash-lite":  1000000,
	"gemini-1.5-pro":         1000000,
	"gemini-1.5-flash":       1000000,
	"gemini-pro":             32000,

	// Default fallback
	"default": 128000,
}

// GetContextLimit returns the context limit for a model.
// Returns the default limit if the model is not found.
func GetContextLimit(model string) int64 {
	// Try exact match first
	if limit, ok := ContextLimits[model]; ok {
		return limit
	}

	// Try normalized name (lowercase, remove version suffixes)
	normalized := normalizeModelName(model)
	if limit, ok := ContextLimits[normalized]; ok {
		return limit
	}

	// Try prefix matching for families
	modelLower := strings.ToLower(model)
	for key, limit := range ContextLimits {
		if strings.HasPrefix(modelLower, key) {
			return limit
		}
	}

	return ContextLimits["default"]
}

// normalizeModelName normalizes a model name for lookup.
func normalizeModelName(model string) string {
	model = strings.ToLower(model)
	// Remove date suffixes like -20251101
	model = regexp.MustCompile(`-\d{8}$`).ReplaceAllString(model, "")
	return model
}

// EstimationMethod identifies how the estimate was computed.
type EstimationMethod string

const (
	MethodRobotMode        EstimationMethod = "robot_mode"        // Direct report from agent
	MethodMessageCount     EstimationMethod = "message_count"     // Estimated from message count
	MethodCumulativeTokens EstimationMethod = "cumulative_tokens" // Sum of input+output tokens
	MethodDurationActivity EstimationMethod = "duration_activity" // Time + activity heuristic
	MethodUnknown          EstimationMethod = "unknown"
)

// ContextEstimate represents an estimate of context window usage.
type ContextEstimate struct {
	TokensUsed   int64            `json:"tokens_used"`
	ContextLimit int64            `json:"context_limit"`
	UsagePercent float64          `json:"usage_percent"` // 0.0-100.0
	Confidence   float64          `json:"confidence"`    // 0.0-1.0
	Method       EstimationMethod `json:"method"`
	Model        string           `json:"model,omitempty"`
	UpdatedAt    time.Time        `json:"updated_at"`
}

// ContextState holds the full context tracking state for an agent.
type ContextState struct {
	AgentID      string           `json:"agent_id"`
	PaneID       string           `json:"pane_id"`
	Model        string           `json:"model"`
	Estimate     *ContextEstimate `json:"estimate"`
	MessageCount int              `json:"message_count"`
	SessionStart time.Time        `json:"session_start"`
	LastActivity time.Time        `json:"last_activity"`

	// Internal tracking
	cumulativeInputTokens  int64
	cumulativeOutputTokens int64
}

// ContextEstimator defines the interface for estimation strategies.
type ContextEstimator interface {
	// Estimate computes a context usage estimate for the given agent.
	Estimate(state *ContextState) (*ContextEstimate, error)

	// Confidence returns the base confidence level for this strategy.
	Confidence() float64

	// Name returns the strategy name for logging.
	Name() string
}

// RobotModeEstimator parses context info from robot mode output.
type RobotModeEstimator struct{}

// Name returns the estimator name.
func (e *RobotModeEstimator) Name() string { return "robot_mode" }

// Confidence returns the base confidence for this strategy.
func (e *RobotModeEstimator) Confidence() float64 { return 0.95 }

// Estimate extracts context usage from robot mode output.
func (e *RobotModeEstimator) Estimate(state *ContextState) (*ContextEstimate, error) {
	// This would parse robot mode output for context_used, context_limit
	// Currently returns nil as Claude Code doesn't expose this directly
	return nil, nil
}

// ParseRobotModeContext attempts to parse context info from robot mode JSON output.
// Returns nil if context info is not present in the output.
func ParseRobotModeContext(output string) *ContextEstimate {
	// Look for JSON that might contain context info
	// Expected format from agent output:
	// {"context_used": 145000, "context_limit": 200000, ...}
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(output), &data); err != nil {
		return nil
	}

	contextUsed, hasUsed := data["context_used"].(float64)
	contextLimit, hasLimit := data["context_limit"].(float64)

	if !hasUsed || !hasLimit {
		// Try alternate field names
		if cu, ok := data["tokens_used"].(float64); ok {
			contextUsed = cu
			hasUsed = true
		}
		if cl, ok := data["tokens_limit"].(float64); ok {
			contextLimit = cl
			hasLimit = true
		}
	}

	if !hasUsed || contextLimit == 0 {
		return nil
	}

	if !hasLimit {
		contextLimit = float64(ContextLimits["default"])
	}

	return &ContextEstimate{
		TokensUsed:   int64(contextUsed),
		ContextLimit: int64(contextLimit),
		UsagePercent: (contextUsed / contextLimit) * 100,
		Confidence:   0.95,
		Method:       MethodRobotMode,
		UpdatedAt:    time.Now(),
	}
}

// MessageCountEstimator estimates context from message count.
type MessageCountEstimator struct {
	TokensPerMessage int // Average tokens per message, default 1500
}

// Name returns the estimator name.
func (e *MessageCountEstimator) Name() string { return "message_count" }

// Confidence returns the base confidence for this strategy.
func (e *MessageCountEstimator) Confidence() float64 { return 0.60 }

// Estimate computes context usage from message count.
func (e *MessageCountEstimator) Estimate(state *ContextState) (*ContextEstimate, error) {
	if state.MessageCount == 0 {
		return nil, nil
	}

	tokensPerMsg := e.TokensPerMessage
	if tokensPerMsg <= 0 {
		tokensPerMsg = 1500
	}

	estimatedTokens := int64(state.MessageCount * tokensPerMsg)
	contextLimit := GetContextLimit(state.Model)

	return &ContextEstimate{
		TokensUsed:   estimatedTokens,
		ContextLimit: contextLimit,
		UsagePercent: float64(estimatedTokens) / float64(contextLimit) * 100,
		Confidence:   e.Confidence(),
		Method:       MethodMessageCount,
		Model:        state.Model,
		UpdatedAt:    time.Now(),
	}, nil
}

// CumulativeTokenEstimator sums input+output tokens with compaction discount.
type CumulativeTokenEstimator struct {
	CompactionDiscount float64 // Factor to apply for expected compaction, default 0.7
}

// Name returns the estimator name.
func (e *CumulativeTokenEstimator) Name() string { return "cumulative_tokens" }

// Confidence returns the base confidence for this strategy.
func (e *CumulativeTokenEstimator) Confidence() float64 { return 0.70 }

// Estimate computes context usage from cumulative token counts.
func (e *CumulativeTokenEstimator) Estimate(state *ContextState) (*ContextEstimate, error) {
	totalTokens := state.cumulativeInputTokens + state.cumulativeOutputTokens
	if totalTokens == 0 {
		return nil, nil
	}

	discount := e.CompactionDiscount
	if discount <= 0 || discount > 1 {
		discount = 0.7 // Conservative: assume 30% is compacted away
	}

	estimatedTokens := int64(float64(totalTokens) * discount)
	contextLimit := GetContextLimit(state.Model)

	return &ContextEstimate{
		TokensUsed:   estimatedTokens,
		ContextLimit: contextLimit,
		UsagePercent: float64(estimatedTokens) / float64(contextLimit) * 100,
		Confidence:   e.Confidence(),
		Method:       MethodCumulativeTokens,
		Model:        state.Model,
		UpdatedAt:    time.Now(),
	}, nil
}

// DurationActivityEstimator estimates from session duration and activity level.
type DurationActivityEstimator struct {
	TokensPerMinuteActive   int // Tokens consumed per minute of active conversation
	TokensPerMinuteInactive int // Tokens for inactive time (system overhead)
}

// Name returns the estimator name.
func (e *DurationActivityEstimator) Name() string { return "duration_activity" }

// Confidence returns the base confidence for this strategy.
func (e *DurationActivityEstimator) Confidence() float64 { return 0.30 }

// Estimate computes context usage from duration and activity.
func (e *DurationActivityEstimator) Estimate(state *ContextState) (*ContextEstimate, error) {
	if state.SessionStart.IsZero() {
		return nil, nil
	}

	duration := time.Since(state.SessionStart)
	if duration < time.Minute {
		return nil, nil // Too short for meaningful estimate
	}

	// Determine activity level based on message frequency
	messagesPerMinute := float64(state.MessageCount) / duration.Minutes()

	tokensPerMinActive := e.TokensPerMinuteActive
	if tokensPerMinActive <= 0 {
		tokensPerMinActive = 1000
	}

	tokensPerMinInactive := e.TokensPerMinuteInactive
	if tokensPerMinInactive <= 0 {
		tokensPerMinInactive = 100
	}

	// Estimate based on activity
	var estimatedTokens int64
	if messagesPerMinute > 2 {
		// High activity
		estimatedTokens = int64(duration.Minutes() * float64(tokensPerMinActive))
	} else if messagesPerMinute > 0.5 {
		// Medium activity
		estimatedTokens = int64(duration.Minutes() * float64((tokensPerMinActive+tokensPerMinInactive)/2))
	} else {
		// Low activity
		estimatedTokens = int64(duration.Minutes() * float64(tokensPerMinInactive))
	}

	contextLimit := GetContextLimit(state.Model)

	return &ContextEstimate{
		TokensUsed:   estimatedTokens,
		ContextLimit: contextLimit,
		UsagePercent: float64(estimatedTokens) / float64(contextLimit) * 100,
		Confidence:   e.Confidence(),
		Method:       MethodDurationActivity,
		Model:        state.Model,
		UpdatedAt:    time.Now(),
	}, nil
}

// ContextMonitor manages context tracking for multiple agents.
type ContextMonitor struct {
	estimators []ContextEstimator
	states     map[string]*ContextState // agentID -> state
	mu         sync.RWMutex

	// Configuration
	warningThreshold float64 // Default 60%
	rotateThreshold  float64 // Default 80%
}

// MonitorConfig holds configuration for the context monitor.
type MonitorConfig struct {
	WarningThreshold float64 // Percentage at which to warn (default 60)
	RotateThreshold  float64 // Percentage at which to rotate (default 80)
	TokensPerMessage int     // For message count estimation (default 1500)
}

// DefaultMonitorConfig returns sensible defaults.
func DefaultMonitorConfig() MonitorConfig {
	return MonitorConfig{
		WarningThreshold: 60.0,
		RotateThreshold:  80.0,
		TokensPerMessage: 1500,
	}
}

// NewContextMonitor creates a new context monitor with default estimators.
func NewContextMonitor(cfg MonitorConfig) *ContextMonitor {
	if cfg.WarningThreshold <= 0 {
		cfg.WarningThreshold = 60.0
	}
	if cfg.RotateThreshold <= 0 {
		cfg.RotateThreshold = 80.0
	}
	if cfg.TokensPerMessage <= 0 {
		cfg.TokensPerMessage = 1500
	}

	return &ContextMonitor{
		estimators: []ContextEstimator{
			&RobotModeEstimator{},
			&CumulativeTokenEstimator{CompactionDiscount: 0.7},
			&MessageCountEstimator{TokensPerMessage: cfg.TokensPerMessage},
			&DurationActivityEstimator{
				TokensPerMinuteActive:   1000,
				TokensPerMinuteInactive: 100,
			},
		},
		states:           make(map[string]*ContextState),
		warningThreshold: cfg.WarningThreshold,
		rotateThreshold:  cfg.RotateThreshold,
	}
}

// RegisterAgent registers or updates an agent for monitoring.
func (m *ContextMonitor) RegisterAgent(agentID, paneID, model string) *ContextState {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, exists := m.states[agentID]
	if !exists {
		state = &ContextState{
			AgentID:      agentID,
			PaneID:       paneID,
			Model:        model,
			SessionStart: time.Now(),
		}
		m.states[agentID] = state
	} else {
		// Update fields
		state.PaneID = paneID
		state.Model = model
	}

	return state
}

// UnregisterAgent removes an agent from monitoring.
func (m *ContextMonitor) UnregisterAgent(agentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.states, agentID)
}

// GetState returns the current state for an agent.
func (m *ContextMonitor) GetState(agentID string) *ContextState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.states[agentID]
}

// RecordMessage records a message for an agent.
func (m *ContextMonitor) RecordMessage(agentID string, inputTokens, outputTokens int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, exists := m.states[agentID]
	if !exists {
		return
	}

	state.MessageCount++
	state.cumulativeInputTokens += inputTokens
	state.cumulativeOutputTokens += outputTokens
	state.LastActivity = time.Now()
}

// UpdateFromRobotMode updates context estimate from robot mode output.
func (m *ContextMonitor) UpdateFromRobotMode(agentID, output string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, exists := m.states[agentID]
	if !exists {
		return
	}

	if estimate := ParseRobotModeContext(output); estimate != nil {
		estimate.Model = state.Model
		state.Estimate = estimate
	}
}

// GetEstimate computes the current context estimate for an agent.
// Uses the highest-confidence available strategy.
func (m *ContextMonitor) GetEstimate(agentID string) *ContextEstimate {
	m.mu.RLock()
	state := m.states[agentID]
	m.mu.RUnlock()

	if state == nil {
		return nil
	}

	// If we have a recent robot mode estimate, use it
	if state.Estimate != nil && time.Since(state.Estimate.UpdatedAt) < 30*time.Second {
		return state.Estimate
	}

	// Try estimators in order (already sorted by confidence)
	for _, estimator := range m.estimators {
		estimate, err := estimator.Estimate(state)
		if err == nil && estimate != nil {
			return estimate
		}
	}

	// No estimate available
	return nil
}

// AgentsAboveThreshold returns agents above the given usage percentage.
// Results are sorted by confidence (higher confidence first).
func (m *ContextMonitor) AgentsAboveThreshold(threshold float64) []AgentContextInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []AgentContextInfo

	for agentID, state := range m.states {
		estimate := m.getEstimateLocked(state)
		if estimate == nil {
			continue
		}

		if estimate.UsagePercent >= threshold {
			results = append(results, AgentContextInfo{
				AgentID:    agentID,
				PaneID:     state.PaneID,
				Model:      state.Model,
				Estimate:   estimate,
				NeedsWarn:  estimate.UsagePercent >= m.warningThreshold,
				NeedsRotat: estimate.UsagePercent >= m.rotateThreshold,
			})
		}
	}

	// Sort by confidence descending
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].Estimate.Confidence > results[i].Estimate.Confidence {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	return results
}

// getEstimateLocked computes estimate without taking locks (caller must hold lock).
func (m *ContextMonitor) getEstimateLocked(state *ContextState) *ContextEstimate {
	// If we have a recent robot mode estimate, use it
	if state.Estimate != nil && time.Since(state.Estimate.UpdatedAt) < 30*time.Second {
		return state.Estimate
	}

	// Try estimators in order
	for _, estimator := range m.estimators {
		estimate, err := estimator.Estimate(state)
		if err == nil && estimate != nil {
			return estimate
		}
	}

	return nil
}

// AgentContextInfo provides context info for an agent.
type AgentContextInfo struct {
	AgentID    string           `json:"agent_id"`
	PaneID     string           `json:"pane_id"`
	Model      string           `json:"model"`
	Estimate   *ContextEstimate `json:"estimate"`
	NeedsWarn  bool             `json:"needs_warning"`
	NeedsRotat bool             `json:"needs_rotation"`
}

// GetAllEstimates returns estimates for all monitored agents.
func (m *ContextMonitor) GetAllEstimates() map[string]*ContextEstimate {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*ContextEstimate, len(m.states))
	for agentID, state := range m.states {
		if estimate := m.getEstimateLocked(state); estimate != nil {
			result[agentID] = estimate
		}
	}

	return result
}

// Clear removes all agents from monitoring.
func (m *ContextMonitor) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.states = make(map[string]*ContextState)
}

// Count returns the number of monitored agents.
func (m *ContextMonitor) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.states)
}

// ResetAgent resets the context tracking for an agent (e.g., after rotation).
func (m *ContextMonitor) ResetAgent(agentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if state, exists := m.states[agentID]; exists {
		state.MessageCount = 0
		state.cumulativeInputTokens = 0
		state.cumulativeOutputTokens = 0
		state.SessionStart = time.Now()
		state.Estimate = nil
	}
}

// EstimateTokens provides a simple token estimation from character count.
// Uses the standard ~3.5-4 characters per token approximation.
func EstimateTokens(chars int) int64 {
	// ~3.5-4 characters per token is typical
	// We use 3.5 for a slight overestimate (safer for context limits)
	return int64(float64(chars) / 3.5)
}

// ParseTokenCount extracts a token count from text that might contain numbers.
// Handles formats like "145000", "145,000", "145k", "1.5M".
func ParseTokenCount(text string) (int64, bool) {
	text = strings.TrimSpace(text)
	text = strings.ReplaceAll(text, ",", "")
	text = strings.ToLower(text)

	// Handle k/m suffixes
	multiplier := int64(1)
	if strings.HasSuffix(text, "k") {
		multiplier = 1000
		text = strings.TrimSuffix(text, "k")
	} else if strings.HasSuffix(text, "m") {
		multiplier = 1000000
		text = strings.TrimSuffix(text, "m")
	}

	// Try parsing as float first (handles "1.5")
	if f, err := strconv.ParseFloat(text, 64); err == nil {
		return int64(f * float64(multiplier)), true
	}

	// Try as integer
	if i, err := strconv.ParseInt(text, 10, 64); err == nil {
		return i * multiplier, true
	}

	return 0, false
}

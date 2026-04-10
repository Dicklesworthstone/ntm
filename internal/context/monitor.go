// Package context provides context window monitoring for AI agent orchestration.
// monitor.go implements multi-source estimation of context window usage.
package context

import (
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/models"
)

// GetContextLimit returns the context limit for a model.
// Delegates to the canonical registry in internal/models.
func GetContextLimit(model string) int64 {
	return int64(models.GetContextLimit(model))
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
	AgentID        string           `json:"agent_id"`
	PaneID         string           `json:"pane_id"`
	Model          string           `json:"model"`
	AgentType      string           `json:"agent_type,omitempty"` // cc, cod, gmi
	SessionName    string           `json:"session_name,omitempty"`
	TranscriptPath string           `json:"transcript_path,omitempty"` // Path to agent's transcript file
	Estimate       *ContextEstimate `json:"estimate"`
	MessageCount   int              `json:"message_count"`
	SessionStart   time.Time        `json:"session_start"`
	LastActivity   time.Time        `json:"last_activity"`

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
	var data map[string]interface{}

	// Attempt to extract JSON object from the output string
	// Find the first '{' and the last '}' to handle mixed text/markdown
	firstBrace := strings.Index(output, "{")
	lastBrace := strings.LastIndex(output, "}")

	if firstBrace != -1 && lastBrace != -1 && lastBrace > firstBrace {
		jsonStr := output[firstBrace : lastBrace+1]
		if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
			return nil
		}
	} else {
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

	// Apply default limit if none was found in the data
	if !hasLimit {
		contextLimit = float64(models.DefaultContextLimit)
	}

	if !hasUsed || contextLimit == 0 {
		return nil
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
	warningThreshold float64 // Default 70%
	rotateThreshold  float64 // Default 75%
}

// MonitorConfig holds configuration for the context monitor.
type MonitorConfig struct {
	WarningThreshold float64 // Percentage at which to warn (default 70)
	RotateThreshold  float64 // Percentage at which to rotate (default 75)
	TokensPerMessage int     // For message count estimation (default 1500)
}

// DefaultMonitorConfig returns sensible defaults.
func DefaultMonitorConfig() MonitorConfig {
	return MonitorConfig{
		WarningThreshold: 70.0,
		RotateThreshold:  75.0,
		TokensPerMessage: 1500,
	}
}

// NewContextMonitor creates a new context monitor with default estimators.
func NewContextMonitor(cfg MonitorConfig) *ContextMonitor {
	if cfg.WarningThreshold <= 0 {
		cfg.WarningThreshold = 70.0
	}
	if cfg.RotateThreshold <= 0 {
		cfg.RotateThreshold = 75.0
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

// SetAgentType updates the agent type for a monitored agent.
func (m *ContextMonitor) SetAgentType(agentID, agentType string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if state, exists := m.states[agentID]; exists {
		state.AgentType = agentType
	}
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
	defer m.mu.RUnlock()
	state := m.states[agentID]

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
// Results are sorted by confidence (higher first), then by AgentID (lexicographically).
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
				AgentID:     agentID,
				PaneID:      state.PaneID,
				Model:       state.Model,
				Estimate:    estimate,
				NeedsWarn:   estimate.UsagePercent >= m.warningThreshold,
				NeedsRotate: estimate.UsagePercent >= m.rotateThreshold,
			})
		}
	}

	// Sort by confidence descending, then AgentID ascending for determinism
	sort.Slice(results, func(i, j int) bool {
		if results[i].Estimate.Confidence != results[j].Estimate.Confidence {
			return results[i].Estimate.Confidence > results[j].Estimate.Confidence
		}
		return results[i].AgentID < results[j].AgentID
	})

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
	AgentID     string           `json:"agent_id"`
	PaneID      string           `json:"pane_id"`
	Model       string           `json:"model"`
	Estimate    *ContextEstimate `json:"estimate"`
	NeedsWarn   bool             `json:"needs_warning"`
	NeedsRotate bool             `json:"needs_rotation"`
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

// RegisterAgentWithTranscript registers an agent with extended info including transcript path.
// This is used for pre-compact handoff generation that needs to read the transcript.
func (m *ContextMonitor) RegisterAgentWithTranscript(agentID, paneID, model, agentType, sessionName, transcriptPath string) *ContextState {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, exists := m.states[agentID]
	if !exists {
		state = &ContextState{
			AgentID:        agentID,
			PaneID:         paneID,
			Model:          model,
			AgentType:      agentType,
			SessionName:    sessionName,
			TranscriptPath: transcriptPath,
			SessionStart:   time.Now(),
		}
		m.states[agentID] = state
	} else {
		// Update fields
		state.PaneID = paneID
		state.Model = model
		state.AgentType = agentType
		state.SessionName = sessionName
		state.TranscriptPath = transcriptPath
	}

	return state
}

// UpdateFromTranscript reads the agent's transcript file and estimates tokens from file size.
// Uses ~4 bytes per token as approximation (conservative for safety).
// Returns the estimated tokens, or 0 if the file doesn't exist or can't be read.
func (m *ContextMonitor) UpdateFromTranscript(agentID string) (int64, error) {
	m.mu.RLock()
	state, exists := m.states[agentID]
	m.mu.RUnlock()

	if !exists {
		return 0, nil
	}

	if state.TranscriptPath == "" {
		return 0, nil // No transcript path configured
	}

	info, err := os.Stat(state.TranscriptPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // File doesn't exist yet, not an error
		}
		return 0, err
	}

	// Estimate tokens from file size (~3.5 bytes per token, conservative)
	estimatedTokens := int64(float64(info.Size()) / 3.5)

	// Update the state with this estimate
	m.mu.Lock()
	if state, exists := m.states[agentID]; exists {
		state.cumulativeInputTokens = estimatedTokens / 2
		state.cumulativeOutputTokens = estimatedTokens / 2
		state.LastActivity = time.Now()
	}
	m.mu.Unlock()

	return estimatedTokens, nil
}

// HandoffRecommendation contains the result of ShouldTriggerHandoff.
type HandoffRecommendation struct {
	ShouldTrigger bool    // True if handoff should be triggered
	ShouldWarn    bool    // True if warning threshold exceeded
	Reason        string  // Human-readable reason for the recommendation
	UsagePercent  float64 // Current usage percentage
	TokenVelocity float64 // Tokens per minute (if available)
}

// ShouldTriggerHandoff checks if an agent should generate a handoff.
// Returns a recommendation with reason explaining why.
// Uses configured thresholds (RotateThreshold, WarningThreshold).
func (m *ContextMonitor) ShouldTriggerHandoff(agentID string, predictor *ContextPredictor) *HandoffRecommendation {
	estimate := m.GetEstimate(agentID)
	if estimate == nil {
		return &HandoffRecommendation{
			ShouldTrigger: false,
			ShouldWarn:    false,
			Reason:        "no context estimate available",
		}
	}

	rec := &HandoffRecommendation{
		UsagePercent: estimate.UsagePercent,
	}

	// Rotate threshold check
	if estimate.UsagePercent >= m.rotateThreshold {
		rec.ShouldTrigger = true
		rec.ShouldWarn = true
		rec.Reason = "usage " + strconv.FormatFloat(estimate.UsagePercent, 'f', 1, 64) + "% exceeds " + strconv.FormatFloat(m.rotateThreshold, 'f', 1, 64) + "% threshold"
		return rec
	}

	// Warning threshold check
	if estimate.UsagePercent >= m.warningThreshold {
		rec.ShouldWarn = true
		rec.Reason = "usage " + strconv.FormatFloat(estimate.UsagePercent, 'f', 1, 64) + "% exceeds " + strconv.FormatFloat(m.warningThreshold, 'f', 1, 64) + "% warning threshold"
	}

	// Velocity-based prediction (if predictor available)
	if predictor != nil {
		state := m.GetState(agentID)
		if state != nil {
			prediction := predictor.PredictExhaustion(GetContextLimit(state.Model))
			if prediction != nil {
				rec.TokenVelocity = prediction.TokenVelocity

				// If velocity predicts exhaustion in next 8 minutes, trigger
				if prediction.TokenVelocity > 0 && prediction.MinutesToExhaustion > 0 && prediction.MinutesToExhaustion < 8 {
					rec.ShouldTrigger = true
					rec.ShouldWarn = true
					rec.Reason = "velocity " + strconv.FormatFloat(prediction.TokenVelocity, 'f', 0, 64) +
						" tpm predicts exhaustion in " + strconv.FormatFloat(prediction.MinutesToExhaustion, 'f', 1, 64) + " min"
				}
			}
		}
	}

	if rec.Reason == "" {
		rec.Reason = "usage " + strconv.FormatFloat(estimate.UsagePercent, 'f', 1, 64) + "% within normal range"
	}

	return rec
}

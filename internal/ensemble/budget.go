package ensemble

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	tokenpkg "github.com/Dicklesworthstone/ntm/internal/tokens"
)

// BudgetTracker tracks token usage across an ensemble session and enforces budget caps.
// It monitors per-agent and total spending, providing remaining budget information
// and enforcing limits when exceeded.
type BudgetTracker struct {
	mu sync.RWMutex

	// config is the budget configuration defining limits.
	config BudgetConfig

	// perAgent tracks token spend per agent (keyed by agent/pane name).
	perAgent map[string]int

	// totalSpent is the cumulative token spend across all agents.
	totalSpent int

	// startTime is when the tracker was initialized.
	startTime time.Time

	// logger is the structured logger for budget events.
	logger *slog.Logger
}

// BudgetState represents the current budget status.
type BudgetState struct {
	// TotalSpent is tokens used across all agents.
	TotalSpent int `json:"total_spent"`

	// TotalRemaining is tokens remaining against the total cap.
	TotalRemaining int `json:"total_remaining"`

	// TotalLimit is the configured total token cap.
	TotalLimit int `json:"total_limit"`

	// PerAgentSpent maps agent names to their token usage.
	PerAgentSpent map[string]int `json:"per_agent_spent"`

	// PerAgentRemaining maps agent names to their remaining budget.
	PerAgentRemaining map[string]int `json:"per_agent_remaining"`

	// PerAgentLimit is the per-agent token cap.
	PerAgentLimit int `json:"per_agent_limit"`

	// IsOverBudget indicates if total budget has been exceeded.
	IsOverBudget bool `json:"is_over_budget"`

	// OverBudgetAgents lists agents that have exceeded their caps.
	OverBudgetAgents []string `json:"over_budget_agents,omitempty"`

	// ElapsedTime is how long the tracker has been running.
	ElapsedTime time.Duration `json:"elapsed_time"`
}

// SpendResult is returned from RecordSpend to indicate if spending should continue.
type SpendResult struct {
	// Allowed indicates if the spend was within budget.
	Allowed bool `json:"allowed"`

	// Remaining is tokens remaining for this agent after the spend.
	Remaining int `json:"remaining"`

	// TotalRemaining is overall tokens remaining after the spend.
	TotalRemaining int `json:"total_remaining"`

	// Message provides context about any budget enforcement.
	Message string `json:"message,omitempty"`
}

// NewBudgetTracker creates a new budget tracker with the given configuration.
// If config has zero values, defaults from DefaultBudgetConfig() are used.
func NewBudgetTracker(config BudgetConfig, logger *slog.Logger) *BudgetTracker {
	if config.MaxTokensPerMode == 0 {
		config.MaxTokensPerMode = DefaultBudgetConfig().MaxTokensPerMode
	}
	if config.MaxTotalTokens == 0 {
		config.MaxTotalTokens = DefaultBudgetConfig().MaxTotalTokens
	}

	if logger == nil {
		logger = slog.Default()
	}

	return &BudgetTracker{
		config:    config,
		perAgent:  make(map[string]int),
		startTime: time.Now(),
		logger:    logger,
	}
}

// RecordSpend records token usage for an agent and checks budget limits.
// Returns a SpendResult indicating if the spend was allowed and remaining budget.
func (bt *BudgetTracker) RecordSpend(agentName string, tokens int) SpendResult {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	// Record the spend
	bt.perAgent[agentName] += tokens
	bt.totalSpent += tokens

	agentSpent := bt.perAgent[agentName]
	agentRemaining := bt.config.MaxTokensPerMode - agentSpent
	totalRemaining := bt.config.MaxTotalTokens - bt.totalSpent

	// Log the spend
	bt.logger.Info("budget: recorded spend",
		slog.String("agent", agentName),
		slog.Int("tokens", tokens),
		slog.Int("agent_spent", agentSpent),
		slog.Int("agent_remaining", agentRemaining),
		slog.Int("total_spent", bt.totalSpent),
		slog.Int("total_remaining", totalRemaining),
	)

	// Check limits
	result := SpendResult{
		Allowed:        true,
		Remaining:      agentRemaining,
		TotalRemaining: totalRemaining,
	}

	if agentRemaining < 0 {
		result.Allowed = false
		result.Message = "agent budget exceeded"
		bt.logger.Warn("budget: agent over budget",
			slog.String("agent", agentName),
			slog.Int("spent", agentSpent),
			slog.Int("limit", bt.config.MaxTokensPerMode),
		)
	}

	if totalRemaining < 0 {
		result.Allowed = false
		result.Message = "total budget exceeded"
		bt.logger.Warn("budget: total budget exceeded",
			slog.Int("spent", bt.totalSpent),
			slog.Int("limit", bt.config.MaxTotalTokens),
		)
	}

	return result
}

// RemainingForAgent returns the remaining token budget for a specific agent.
func (bt *BudgetTracker) RemainingForAgent(agentName string) int {
	bt.mu.RLock()
	defer bt.mu.RUnlock()

	spent := bt.perAgent[agentName]
	return bt.config.MaxTokensPerMode - spent
}

// TotalRemaining returns the overall remaining token budget.
func (bt *BudgetTracker) TotalRemaining() int {
	bt.mu.RLock()
	defer bt.mu.RUnlock()

	return bt.config.MaxTotalTokens - bt.totalSpent
}

// IsOverBudget returns true if total spending exceeds the configured limit.
func (bt *BudgetTracker) IsOverBudget() bool {
	bt.mu.RLock()
	defer bt.mu.RUnlock()

	return bt.totalSpent > bt.config.MaxTotalTokens
}

// IsAgentOverBudget returns true if the specified agent has exceeded its budget.
func (bt *BudgetTracker) IsAgentOverBudget(agentName string) bool {
	bt.mu.RLock()
	defer bt.mu.RUnlock()

	return bt.perAgent[agentName] > bt.config.MaxTokensPerMode
}

// GetState returns a snapshot of the current budget state.
func (bt *BudgetTracker) GetState() BudgetState {
	bt.mu.RLock()
	defer bt.mu.RUnlock()

	perAgentSpent := make(map[string]int, len(bt.perAgent))
	perAgentRemaining := make(map[string]int, len(bt.perAgent))
	var overBudgetAgents []string

	for agent, spent := range bt.perAgent {
		perAgentSpent[agent] = spent
		remaining := bt.config.MaxTokensPerMode - spent
		perAgentRemaining[agent] = remaining
		if remaining < 0 {
			overBudgetAgents = append(overBudgetAgents, agent)
		}
	}

	return BudgetState{
		TotalSpent:        bt.totalSpent,
		TotalRemaining:    bt.config.MaxTotalTokens - bt.totalSpent,
		TotalLimit:        bt.config.MaxTotalTokens,
		PerAgentSpent:     perAgentSpent,
		PerAgentRemaining: perAgentRemaining,
		PerAgentLimit:     bt.config.MaxTokensPerMode,
		IsOverBudget:      bt.totalSpent > bt.config.MaxTotalTokens,
		OverBudgetAgents:  overBudgetAgents,
		ElapsedTime:       time.Since(bt.startTime),
	}
}

// Reset clears all spend tracking, keeping the same configuration.
func (bt *BudgetTracker) Reset() {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	bt.perAgent = make(map[string]int)
	bt.totalSpent = 0
	bt.startTime = time.Now()

	bt.logger.Info("budget: tracker reset")
}

// Config returns the budget configuration (read-only copy).
func (bt *BudgetTracker) Config() BudgetConfig {
	bt.mu.RLock()
	defer bt.mu.RUnlock()
	return bt.config
}

// EstimateOutputTokens provides a thin wrapper around internal/tokens for
// ensemble outputs. Raw agent output is usually Markdown-like text.
func EstimateOutputTokens(raw string) int {
	if strings.TrimSpace(raw) == "" {
		return 0
	}
	return tokenpkg.EstimateTokensWithLanguageHint(raw, tokenpkg.ContentMarkdown)
}

// EstimateModeOutputTokens estimates the token count for a ModeOutput.
// If RawOutput is present, it is used directly; otherwise we fall back
// to a JSON-encoded representation (or a structured text fallback).
func EstimateModeOutputTokens(output *ModeOutput) int {
	if output == nil {
		return 0
	}
	if strings.TrimSpace(output.RawOutput) != "" {
		return EstimateOutputTokens(output.RawOutput)
	}

	blob, err := json.Marshal(output)
	if err != nil {
		return tokenpkg.EstimateTokensWithLanguageHint(fallbackModeOutputText(output), tokenpkg.ContentMarkdown)
	}
	return tokenpkg.EstimateTokensWithLanguageHint(string(blob), tokenpkg.ContentJSON)
}

// EstimateModeOutputsTokens sums token estimates across multiple ModeOutput values.
func EstimateModeOutputsTokens(outputs []ModeOutput) int {
	total := 0
	for i := range outputs {
		total += EstimateModeOutputTokens(&outputs[i])
	}
	return total
}

func fallbackModeOutputText(output *ModeOutput) string {
	if output == nil {
		return ""
	}

	var b strings.Builder
	if output.ModeID != "" {
		b.WriteString("Mode: ")
		b.WriteString(output.ModeID)
		b.WriteString("\n")
	}
	if output.Thesis != "" {
		b.WriteString("Thesis: ")
		b.WriteString(output.Thesis)
		b.WriteString("\n")
	}
	if len(output.TopFindings) > 0 {
		b.WriteString("Findings:\n")
		for _, f := range output.TopFindings {
			b.WriteString("- ")
			b.WriteString(f.Finding)
			if f.Reasoning != "" {
				b.WriteString(" (")
				b.WriteString(f.Reasoning)
				b.WriteString(")")
			}
			b.WriteString("\n")
		}
	}
	if len(output.Risks) > 0 {
		b.WriteString("Risks:\n")
		for _, r := range output.Risks {
			b.WriteString("- ")
			b.WriteString(r.Risk)
			if r.Mitigation != "" {
				b.WriteString(" | mitigation: ")
				b.WriteString(r.Mitigation)
			}
			b.WriteString("\n")
		}
	}
	if len(output.Recommendations) > 0 {
		b.WriteString("Recommendations:\n")
		for _, rec := range output.Recommendations {
			b.WriteString("- ")
			b.WriteString(rec.Recommendation)
			b.WriteString("\n")
		}
	}
	if len(output.QuestionsForUser) > 0 {
		b.WriteString("Questions:\n")
		for _, q := range output.QuestionsForUser {
			b.WriteString("- ")
			b.WriteString(q.Question)
			b.WriteString("\n")
		}
	}
	if len(output.FailureModesToWatch) > 0 {
		b.WriteString("Failure modes:\n")
		for _, f := range output.FailureModesToWatch {
			b.WriteString("- ")
			b.WriteString(f.Mode)
			b.WriteString(": ")
			b.WriteString(f.Description)
			b.WriteString("\n")
		}
	}
	return b.String()
}

package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// SpawnContext holds information about the spawn batch for agent coordination.
// This context enables agents to self-coordinate based on their position in the spawn order.
type SpawnContext struct {
	BatchID     string    // Unique identifier for this spawn batch
	TotalAgents int       // Total number of agents in this spawn
	CreatedAt   time.Time // When the spawn batch was initiated
}

// AgentSpawnContext holds spawn context for a specific agent.
type AgentSpawnContext struct {
	SpawnContext
	Order       int           // 1-based position in spawn order (1, 2, 3...)
	StaggerDelay time.Duration // Delay before this agent receives its prompt
}

// NewSpawnContext creates a new spawn context with a unique batch ID.
func NewSpawnContext(totalAgents int) *SpawnContext {
	return &SpawnContext{
		BatchID:     generateBatchID(),
		TotalAgents: totalAgents,
		CreatedAt:   time.Now(),
	}
}

// ForAgent creates an agent-specific context for the given agent position.
func (sc *SpawnContext) ForAgent(order int, staggerDelay time.Duration) *AgentSpawnContext {
	return &AgentSpawnContext{
		SpawnContext: *sc,
		Order:        order,
		StaggerDelay: staggerDelay,
	}
}

// EnvVars returns environment variables to set for this agent.
// These variables allow agents to programmatically access their spawn context.
func (asc *AgentSpawnContext) EnvVars() map[string]string {
	return map[string]string{
		"NTM_SPAWN_ORDER":    fmt.Sprintf("%d", asc.Order),
		"NTM_SPAWN_TOTAL":    fmt.Sprintf("%d", asc.TotalAgents),
		"NTM_SPAWN_BATCH_ID": asc.BatchID,
	}
}

// EnvVarPrefix returns a shell command prefix that exports the spawn context.
// Example: "export NTM_SPAWN_ORDER=2 NTM_SPAWN_TOTAL=4 NTM_SPAWN_BATCH_ID=spawn-abc123; "
func (asc *AgentSpawnContext) EnvVarPrefix() string {
	return fmt.Sprintf(
		"export NTM_SPAWN_ORDER=%d NTM_SPAWN_TOTAL=%d NTM_SPAWN_BATCH_ID=%s; ",
		asc.Order, asc.TotalAgents, asc.BatchID,
	)
}

// PromptAnnotation returns a minimal context annotation for the prompt.
// Only used when stagger is enabled to help agents understand their position.
// Format: "[Spawn context: Agent 2/4, batch spawn-abc123]"
func (asc *AgentSpawnContext) PromptAnnotation() string {
	return fmt.Sprintf("[Spawn context: Agent %d/%d, batch %s]", asc.Order, asc.TotalAgents, asc.BatchID)
}

// AnnotatePrompt prepends the spawn context annotation to the prompt.
// Returns the original prompt unchanged if annotation is empty.
func (asc *AgentSpawnContext) AnnotatePrompt(prompt string, includeAnnotation bool) string {
	if !includeAnnotation || prompt == "" {
		return prompt
	}
	return asc.PromptAnnotation() + "\n\n" + prompt
}

// generateBatchID creates a unique identifier for a spawn batch.
// Format: "spawn-{timestamp}-{random}" for human readability and uniqueness.
func generateBatchID() string {
	timestamp := time.Now().Format("20060102-150405")
	randBytes := make([]byte, 4)
	if _, err := rand.Read(randBytes); err != nil {
		// Fallback to timestamp-based if crypto/rand fails
		return fmt.Sprintf("spawn-%s-%x", timestamp, time.Now().UnixNano()%0xffffffff)
	}
	return fmt.Sprintf("spawn-%s-%s", timestamp, hex.EncodeToString(randBytes))
}

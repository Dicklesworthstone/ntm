package swarm

import (
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

// gracefulExitMethod selects the key sequence used to ask an agent CLI to exit
// cleanly before a pane is respawned.
type gracefulExitMethod int

const (
	gracefulExitDoubleInterrupt gracefulExitMethod = iota
	gracefulExitSlashExit
	gracefulExitEscapeInterrupt
	gracefulExitSingleInterrupt
)

// gracefulExitMethodForAgent returns the graceful-exit key sequence for an agent type.
func gracefulExitMethodForAgent(agentType string) gracefulExitMethod {
	switch normalizeSwarmAgentType(agentType) {
	case "cod":
		return gracefulExitSlashExit
	case "gmi":
		return gracefulExitEscapeInterrupt
	case "cursor", "windsurf", "aider", "ollama":
		return gracefulExitSingleInterrupt
	default:
		return gracefulExitDoubleInterrupt
	}
}

// normalizeSwarmAgentType converts agent type aliases to canonical forms.
func normalizeSwarmAgentType(agentType string) string {
	normalized := strings.ToLower(strings.TrimSpace(agentType))
	if normalized == "" {
		return ""
	}
	return string(agent.AgentType(normalized).Canonical())
}

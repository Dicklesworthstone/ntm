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
	gracefulExitEscapeClearEOF
	gracefulExitSingleInterrupt
)

// gracefulExitMethodForAgent returns the graceful-exit key sequence for an agent type
// from the declarative 12-harness capability registry.
func gracefulExitMethodForAgent(agentType string) gracefulExitMethod {
	switch agent.CapabilitiesFor(agent.AgentType(normalizeSwarmAgentType(agentType))).GracefulExit {
	case agent.GracefulExitSlashExit:
		return gracefulExitSlashExit
	case agent.GracefulExitEscapeInterrupt:
		return gracefulExitEscapeInterrupt
	case agent.GracefulExitEscapeClearEOF:
		return gracefulExitEscapeClearEOF
	case agent.GracefulExitSingleInterrupt:
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

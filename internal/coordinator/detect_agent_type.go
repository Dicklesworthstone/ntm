package coordinator

import (
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

// detectAgentType extracts the canonical agent type from a pane title.
// Pane titles follow the pattern: <session>__<type>_<index>
func detectAgentType(title string) string {
	sep := strings.LastIndex(title, "__")
	if sep == -1 || sep+2 >= len(title) {
		return ""
	}
	typePart := title[sep+2:]
	if typePart == "" {
		return ""
	}

	agentName := typePart
	if idx := strings.IndexRune(typePart, '_'); idx != -1 {
		agentName = typePart[:idx]
	}

	switch agent.AgentType(agentName).Canonical() {
	case agent.AgentTypeClaudeCode:
		return "cc"
	case agent.AgentTypeCodex:
		return "cod"
	case agent.AgentTypeGemini:
		return "gmi"
	case agent.AgentTypeAntigravity:
		return "agy"
	case agent.AgentTypePi:
		return "pi"
	case agent.AgentTypeCursor:
		return "cursor"
	case agent.AgentTypeWindsurf:
		return "windsurf"
	case agent.AgentTypeAider:
		return "aider"
	case agent.AgentTypeOllama:
		return "ollama"
	default:
		return ""
	}
}

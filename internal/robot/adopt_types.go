package robot

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

const (
	adoptSchemaID      = "ntm:robot:adopt:v1"
	adoptSchemaVersion = "1.0.0"

	// Exported constants for cross-package CLI/Kernel envelope construction.
	AdoptSchemaID      = adoptSchemaID
	AdoptSchemaVersion = adoptSchemaVersion
)

var adoptSummaryOrder = []agent.AgentType{
	agent.AgentTypeClaudeCode,
	agent.AgentTypeCodex,
	agent.AgentTypeGemini,
	agent.AgentTypeAntigravity,
	agent.AgentTypeGrok,
	agent.AgentTypeCursor,
	agent.AgentTypeWindsurf,
	agent.AgentTypeAider,
	agent.AgentTypeOpencode,
	agent.AgentTypeOmp,
	agent.AgentTypeOllama,
	agent.AgentTypeUser,
}

// AdoptedPaneInfo describes a pane adopted into NTM management.
type AdoptedPaneInfo struct {
	PaneID      string `json:"pane_id"`
	PaneIndex   int    `json:"pane_index"`
	WindowIndex int    `json:"window_index"`
	AgentType   string `json:"agent_type"`
	OldTitle    string `json:"old_title,omitempty"`
	NewTitle    string `json:"new_title"`
	NTMIndex    int    `json:"ntm_index"`
}

// AdoptedAgentCounts tracks adopted pane counts across all 12 supported pane types.
type AdoptedAgentCounts struct {
	CC       int `json:"cc"`
	Cod      int `json:"cod"`
	Gmi      int `json:"gmi"`
	Agy      int `json:"agy"`
	Grok     int `json:"grok"`
	Cursor   int `json:"cursor"`
	Windsurf int `json:"windsurf"`
	Aider    int `json:"aider"`
	Opencode int `json:"oc"`
	Omp      int `json:"omp"`
	Ollama   int `json:"ollama"`
	User     int `json:"user"`
}

// Total returns the total number of adopted panes across all types.
func (a AdoptedAgentCounts) Total() int {
	return a.CC + a.Cod + a.Gmi + a.Agy + a.Grok + a.Cursor + a.Windsurf + a.Aider + a.Opencode + a.Omp + a.Ollama + a.User
}

// Summary formats non-zero adopted pane counts in canonical harness order.
func (a AdoptedAgentCounts) Summary() string {
	parts := make([]string, 0, len(adoptSummaryOrder))
	for _, agentType := range adoptSummaryOrder {
		count := a.CountFor(agentType)
		if count > 0 {
			parts = append(parts, fmt.Sprintf("%s:%d", agentType, count))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// Increment adds 1 to the canonical counter for agentType.
func (a *AdoptedAgentCounts) Increment(agentType agent.AgentType) {
	switch agentType.Canonical() {
	case agent.AgentTypeClaudeCode:
		a.CC++
	case agent.AgentTypeCodex:
		a.Cod++
	case agent.AgentTypeGemini:
		a.Gmi++
	case agent.AgentTypeAntigravity:
		a.Agy++
	case agent.AgentTypeGrok:
		a.Grok++
	case agent.AgentTypeCursor:
		a.Cursor++
	case agent.AgentTypeWindsurf:
		a.Windsurf++
	case agent.AgentTypeAider:
		a.Aider++
	case agent.AgentTypeOpencode:
		a.Opencode++
	case agent.AgentTypeOmp:
		a.Omp++
	case agent.AgentTypeOllama:
		a.Ollama++
	case agent.AgentTypeUser:
		a.User++
	}
}

// CountFor returns the adopted count for agentType.
func (a AdoptedAgentCounts) CountFor(agentType agent.AgentType) int {
	switch agentType.Canonical() {
	case agent.AgentTypeClaudeCode:
		return a.CC
	case agent.AgentTypeCodex:
		return a.Cod
	case agent.AgentTypeGemini:
		return a.Gmi
	case agent.AgentTypeAntigravity:
		return a.Agy
	case agent.AgentTypeGrok:
		return a.Grok
	case agent.AgentTypeCursor:
		return a.Cursor
	case agent.AgentTypeWindsurf:
		return a.Windsurf
	case agent.AgentTypeAider:
		return a.Aider
	case agent.AgentTypeOpencode:
		return a.Opencode
	case agent.AgentTypeOmp:
		return a.Omp
	case agent.AgentTypeOllama:
		return a.Ollama
	case agent.AgentTypeUser:
		return a.User
	default:
		return 0
	}
}

// AdoptInput is the structured input for sessions.adopt and --robot-adopt.
type AdoptInput struct {
	Session      string            `json:"session"`
	Panes        map[string]string `json:"panes,omitempty"` // keys: cc, cod, gmi, agy, grok, cursor, windsurf, aider, oc, omp, ollama, user
	ByWindow     bool              `json:"by_window,omitempty"`
	ByWindowType string            `json:"by_window_type,omitempty"`
	AutoName     *bool             `json:"auto_name,omitempty"` // defaults to true when nil
	DryRun       bool              `json:"dry_run,omitempty"`
}

// AdoptOutput is the canonical robot envelope for --robot-adopt and sessions.adopt.
type AdoptOutput struct {
	RobotResponse
	SchemaID      string             `json:"schema_id"`
	SchemaVersion string             `json:"schema_version"`
	Session       string             `json:"session"`
	AdoptedPanes  []AdoptedPaneInfo  `json:"adopted_panes"`
	TotalPanes    int                `json:"total_panes"`
	Agents        AdoptedAgentCounts `json:"agents"`
	AutoName      bool               `json:"auto_name"`
	ByWindow      bool               `json:"by_window,omitempty"`
	DryRun        bool               `json:"dry_run,omitempty"`
	AgentHints    *AgentHints        `json:"_agent_hints,omitempty"`
}

// PrintAdoptOutput writes the terminal JSON/TOON response for --robot-adopt.
func PrintAdoptOutput(out *AdoptOutput) error {
	if out == nil {
		return errors.New("robot adopt output is nil")
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot adopt failed")
}

func adoptCapabilityCommand() RobotCommandInfo {
	return RobotCommandInfo{
		Name:        "adopt",
		Flag:        "--robot-adopt",
		Category:    "spawn",
		Summary:     "Adopt an external tmux session.",
		Description: "Adopt an external tmux session into NTM management across all 11 agent harnesses plus user panes with 2D window.pane or --adopt-by-window addressing.",
		Parameters: []RobotParameter{
			{Name: "session", Flag: "--robot-adopt", Type: "string", Required: true, Description: "External tmux session name to adopt"},
			{Name: "adopt-cc", Flag: "--adopt-cc", Type: "string", Required: false, Description: "Pane specs (bare, range, or window.pane) for Claude (cc) agents"},
			{Name: "adopt-cod", Flag: "--adopt-cod", Type: "string", Required: false, Description: "Pane specs for Codex (cod) agents"},
			{Name: "adopt-gmi", Flag: "--adopt-gmi", Type: "string", Required: false, Description: "Pane specs for Gemini (gmi) agents"},
			{Name: "adopt-agy", Flag: "--adopt-agy", Type: "string", Required: false, Description: "Pane specs for Antigravity (agy) agents"},
			{Name: "adopt-grok", Flag: "--adopt-grok", Type: "string", Required: false, Description: "Pane specs for Grok Build (grok) agents"},
			{Name: "adopt-cursor", Flag: "--adopt-cursor", Type: "string", Required: false, Description: "Pane specs for Cursor agents"},
			{Name: "adopt-windsurf", Flag: "--adopt-windsurf", Type: "string", Required: false, Description: "Pane specs for Windsurf agents"},
			{Name: "adopt-aider", Flag: "--adopt-aider", Type: "string", Required: false, Description: "Pane specs for Aider agents"},
			{Name: "adopt-oc", Flag: "--adopt-oc", Type: "string", Required: false, Description: "Pane specs for OpenCode (oc) agents"},
			{Name: "adopt-omp", Flag: "--adopt-omp", Type: "string", Required: false, Description: "Pane specs for Oh My Pi (omp) agents"},
			{Name: "adopt-ollama", Flag: "--adopt-ollama", Type: "string", Required: false, Description: "Pane specs for Ollama agents"},
			{Name: "adopt-user", Flag: "--adopt-user", Type: "string", Required: false, Description: "Pane specs for user shell panes"},
			{Name: "adopt-by-window", Flag: "--adopt-by-window", Type: "string", Required: false, Description: "Map every window's sole pane to one harness type (e.g. cc, cod, agy)"},
			{Name: "adopt-auto-name", Flag: "--adopt-auto-name", Type: "bool", Required: false, Default: "true", Description: "Automatically rename adopted panes to NTM title convention"},
			{Name: "dry-run", Flag: "--dry-run", Type: "bool", Required: false, Default: "false", Description: "Preview adoption plan without mutating tmux pane titles or options"},
		},
		Examples: []string{
			"ntm --robot-adopt=external_proj --adopt-cc=0,1 --adopt-cod=2",
			"ntm --robot-adopt=external_proj --adopt-cc=1.0,2.0 --adopt-agy=3.0 --dry-run",
			"ntm --robot-adopt=external_proj --adopt-by-window=cc",
		},
	}
}

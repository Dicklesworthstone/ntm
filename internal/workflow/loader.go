// Package workflow provides workflow template definitions and coordination for multi-agent patterns.
package workflow

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

//go:embed builtins/*.toml
var builtinFS embed.FS

// Loader loads workflow templates from multiple sources with proper precedence.
type Loader struct {
	// UserConfigDir is the user config directory (default: ~/.config/ntm)
	UserConfigDir string
	// ProjectDir is the current project directory (for .ntm/workflows/)
	ProjectDir string
}

// NewLoader creates a new workflow template loader with default paths.
func NewLoader() *Loader {
	userConfigDir := defaultUserConfigDir()
	projectDir, _ := os.Getwd()
	return &Loader{
		UserConfigDir: userConfigDir,
		ProjectDir:    projectDir,
	}
}

// defaultUserConfigDir returns the default user config directory.
func defaultUserConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "ntm")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "ntm")
}

// builtinWorkflows returns the built-in workflow templates.
func builtinWorkflows() ([]WorkflowTemplate, error) {
	entries, err := builtinFS.ReadDir("builtins")
	if err != nil {
		return nil, fmt.Errorf("read builtins dir: %w", err)
	}

	var workflows []WorkflowTemplate
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}

		content, err := builtinFS.ReadFile(filepath.Join("builtins", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
		}

		tmpls, err := parseWorkflowsFromContent(string(content))
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", entry.Name(), err)
		}
		for i := range tmpls {
			tmpls[i].Source = "builtin"
			workflows = append(workflows, tmpls[i])
		}
	}

	return workflows, nil
}

// parseWorkflowsFromContent parses workflows from TOML content.
// It handles both single workflow format and workflows array format.
func parseWorkflowsFromContent(content string) ([]WorkflowTemplate, error) {
	// Try workflows array format first (used by builtins)
	var wf WorkflowsFile
	if _, err := toml.Decode(content, &wf); err == nil && len(wf.Workflows) > 0 {
		return validateWorkflows(wf.Workflows)
	}

	// Try single workflow format
	var tmpl WorkflowTemplate
	if _, err := toml.Decode(content, &tmpl); err != nil {
		return nil, fmt.Errorf("parse TOML: %w", err)
	}
	return validateWorkflows([]WorkflowTemplate{tmpl})
}

func validateWorkflows(workflows []WorkflowTemplate) ([]WorkflowTemplate, error) {
	for i := range workflows {
		if err := workflows[i].Validate(); err != nil {
			return nil, fmt.Errorf("validate workflow[%d]: %w", i, err)
		}
	}
	return workflows, nil
}

// LoadAll loads workflow templates from all sources with proper precedence.
// Order: builtin < user (~/.config/ntm/workflows/) < project (.ntm/workflows/)
// Later sources override earlier ones by name.
func (l *Loader) LoadAll() ([]WorkflowTemplate, error) {
	workflows := make(map[string]WorkflowTemplate)

	// 1. Load builtin workflows
	builtins, err := builtinWorkflows()
	if err != nil {
		return nil, fmt.Errorf("load builtins: %w", err)
	}
	for _, w := range builtins {
		workflows[w.Name] = w
	}

	// 2. Load user workflows
	userDir := filepath.Join(l.UserConfigDir, "workflows")
	if userWorkflows, err := loadFromDir(userDir, "user"); err == nil {
		for _, w := range userWorkflows {
			workflows[w.Name] = w
		}
	}

	// 3. Load project workflows (highest priority)
	projectDir := filepath.Join(l.ProjectDir, ".ntm", "workflows")
	if projectWorkflows, err := loadFromDir(projectDir, "project"); err == nil {
		for _, w := range projectWorkflows {
			workflows[w.Name] = w
		}
	}

	// Convert map to slice, preserving reasonable order (builtins first)
	result := make([]WorkflowTemplate, 0, len(workflows))

	// Add builtins first in original order
	builtinNames := []string{"red-green", "review-pipeline", "specialist-team", "parallel-explore"}
	for _, name := range builtinNames {
		if workflow, ok := workflows[name]; ok {
			result = append(result, workflow)
			delete(workflows, name)
		}
	}

	// Add remaining workflows (user/project-defined)
	for _, w := range workflows {
		result = append(result, w)
	}

	return result, nil
}

// loadFromDir loads workflow templates from a directory.
func loadFromDir(dir, source string) ([]WorkflowTemplate, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var workflows []WorkflowTemplate
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}

		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue // Skip files we can't read
		}

		tmpls, err := parseWorkflowsFromContent(string(content))
		if err != nil {
			continue // Skip files we can't parse
		}
		for i := range tmpls {
			tmpls[i].Source = source
			workflows = append(workflows, tmpls[i])
		}
	}

	return workflows, nil
}

// Get returns a workflow template by name, or nil if not found.
func (l *Loader) Get(name string) (*WorkflowTemplate, error) {
	workflows, err := l.LoadAll()
	if err != nil {
		return nil, err
	}

	for _, w := range workflows {
		if strings.EqualFold(w.Name, name) {
			return &w, nil
		}
	}

	return nil, fmt.Errorf("workflow template not found: %s", name)
}

// BuiltinNames returns the names of all builtin workflow templates.
func BuiltinNames() []string {
	return []string{"red-green", "review-pipeline", "specialist-team", "parallel-explore"}
}

// SourceDescription returns a human-readable description of the source.
func SourceDescription(source string) string {
	switch source {
	case "builtin":
		return "Built-in"
	case "user":
		return "User (~/.config/ntm/workflows/)"
	case "project":
		return "Project (.ntm/workflows/)"
	default:
		return source
	}
}

// ProfileToAgentType maps a workflow profile name to an agent type.
// Known mappings:
//   - "claude", "cc", "claude-code" → "cc"
//   - "codex", "cod", "codex-cli" → "cod"
//   - "gemini", "gmi", "gemini-cli" → "gmi"
//   - Other profiles default to "cc" (Claude Code)
func ProfileToAgentType(profile string) string {
	profile = strings.ToLower(profile)
	switch profile {
	case "claude", "cc", "claude-code":
		return string(agent.AgentTypeClaudeCode)
	case "codex", "cod", "codex-cli":
		return string(agent.AgentTypeCodex)
	case "gemini", "gmi", "gemini-cli":
		return string(agent.AgentTypeGemini)
	case "cursor":
		return string(agent.AgentTypeCursor)
	case "windsurf":
		return string(agent.AgentTypeWindsurf)
	case "aider":
		return string(agent.AgentTypeAider)
	case "ollama":
		return string(agent.AgentTypeOllama)
	default:
		// Default to Claude for unknown profiles (most capable agent)
		return string(agent.AgentTypeClaudeCode)
	}
}

// AgentCounts returns a map of agent type to count based on template agents.
// This enables integration with the spawn command.
func (t *WorkflowTemplate) AgentCounts() map[string]int {
	counts := make(map[string]int)
	for _, agent := range t.Agents {
		agentType := ProfileToAgentType(agent.Profile)
		count := agent.Count
		if count == 0 {
			count = 1 // Default to 1 if not specified
		}
		counts[agentType] += count
	}
	return counts
}

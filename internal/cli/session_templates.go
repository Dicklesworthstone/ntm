package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/templates"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// SessionTemplatesListResult is the JSON output for session-templates list command.
type SessionTemplatesListResult struct {
	Templates []SessionTemplateInfo `json:"templates"`
	Total     int                   `json:"total"`
}

// SessionTemplateInfo is a session template summary for JSON output.
type SessionTemplateInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Source      string   `json:"source"`
	Tags        []string `json:"tags,omitempty"`
	AgentCount  int      `json:"agent_count"`
}

// SessionTemplateShowResult is the JSON output for session-templates show command.
type SessionTemplateShowResult struct {
	Name        string                        `json:"name"`
	Description string                        `json:"description"`
	Source      string                        `json:"source"`
	Tags        []string                      `json:"tags,omitempty"`
	Agents      *templates.AgentsSpec         `json:"agents"`
	Prompts     *templates.PromptsSpec        `json:"prompts,omitempty"`
	CASS        *templates.CASSSpec           `json:"cass,omitempty"`
	Beads       *templates.BeadsSpec          `json:"beads,omitempty"`
	Options     *templates.SessionOptionsSpec `json:"options,omitempty"`
}

func newSessionTemplatesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "session-templates",
		Aliases: []string{"templates", "st"},
		Short:   "Manage session templates (multi-agent configurations)",
		Long: `List and view session templates (multi-agent configurations).

Session templates define pre-configured agent setups for common workflows:
  - refactor: Large-scale code refactoring with architecture focus
  - feature: Implementing new features in existing codebases
  - bug-hunt: Debugging and systematic bug fixing
  - documentation: Writing and improving documentation
  - migration: Database or API migrations with safety checks

Sources (in precedence order):
  1. Built-in templates (lowest priority)
  2. User templates (~/.config/ntm/templates/)
  3. Project templates (.ntm/templates/) (highest priority)

Examples:
  ntm session-templates list              # List all available templates
  ntm templates list                      # Same (alias)
  ntm session-templates show refactor     # Show details of a template
  ntm session-templates list --json       # JSON output for scripts`,
	}

	cmd.AddCommand(newSessionTemplatesListCmd())
	cmd.AddCommand(newSessionTemplatesShowCmd())

	return cmd
}

func newSessionTemplatesListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available session templates",
		Long: `List all available session templates from all sources.

Templates are shown with name, description, tags, and agent count.

Examples:
  ntm session-templates list           # Human-readable table
  ntm templates list --json            # JSON output for scripts`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionTemplatesList()
		},
	}

	return cmd
}

func runSessionTemplatesList() error {
	loader := templates.NewSessionTemplateLoader()
	tmpls, err := loader.List()
	if err != nil {
		if jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(sessionTemplateErrorResponse(err, ""))
		}
		return err
	}

	if jsonOutput {
		result := SessionTemplatesListResult{
			Templates: make([]SessionTemplateInfo, len(tmpls)),
			Total:     len(tmpls),
		}
		for i, t := range tmpls {
			result.Templates[i] = SessionTemplateInfo{
				Name:        t.Metadata.Name,
				Description: t.Metadata.Description,
				Source:      t.Metadata.Source,
				Tags:        t.Metadata.Tags,
				AgentCount:  t.GetAgentCount(),
			}
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	if len(tmpls) == 0 {
		fmt.Println("No session templates found.")
		return nil
	}

	t := theme.Current()
	fmt.Printf("%sAvailable Session Templates%s\n", "\033[1m", "\033[0m")
	fmt.Printf("%s%s%s\n\n", "\033[2m", strings.Repeat("\u2500", 70), "\033[0m")

	// Group by source
	bySource := make(map[string][]*templates.SessionTemplate)
	for _, tmpl := range tmpls {
		bySource[tmpl.Metadata.Source] = append(bySource[tmpl.Metadata.Source], tmpl)
	}

	// Print in order: builtin, user, project
	sources := []string{"builtin", "user", "project"}
	sourceLabels := map[string]string{
		"builtin": "Built-in",
		"user":    "User (~/.config/ntm/templates/)",
		"project": "Project (.ntm/templates/)",
	}

	for _, source := range sources {
		group := bySource[source]
		if len(group) == 0 {
			continue
		}

		fmt.Printf("  %s%s:%s\n", colorize(t.Info), sourceLabels[source], "\033[0m")
		for _, tmpl := range group {
			fmt.Printf("    %s%-20s%s  %s\n",
				colorize(t.Primary), tmpl.Metadata.Name, "\033[0m",
				tmpl.Metadata.Description)
			if len(tmpl.Metadata.Tags) > 0 {
				fmt.Printf("    %s                     [%d agents, tags: %s]%s\n",
					"\033[2m", tmpl.GetAgentCount(), strings.Join(tmpl.Metadata.Tags, ", "), "\033[0m")
			} else {
				fmt.Printf("    %s                     [%d agents]%s\n",
					"\033[2m", tmpl.GetAgentCount(), "\033[0m")
			}
		}
		fmt.Println()
	}

	fmt.Printf("%sTotal: %d template(s)%s\n", "\033[2m", len(tmpls), "\033[0m")

	return nil
}

func newSessionTemplatesShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <template-name>",
		Short: "Show details of a session template",
		Long: `Show detailed information about a specific session template.

Examples:
  ntm session-templates show refactor        # Show refactor template
  ntm templates show feature --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionTemplatesShow(args[0])
		},
	}

	return cmd
}

func runSessionTemplatesShow(name string) error {
	loader := templates.NewSessionTemplateLoader()
	tmpl, err := loader.Load(name)
	if err != nil {
		if jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(sessionTemplateErrorResponse(err, name))
		}
		return err
	}
	if err := tmpl.Validate(); err != nil {
		if jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(sessionTemplateErrorResponse(err, name))
		}
		return err
	}

	if jsonOutput {
		result := SessionTemplateShowResult{
			Name:        tmpl.Metadata.Name,
			Description: tmpl.Metadata.Description,
			Source:      tmpl.Metadata.Source,
			Tags:        tmpl.Metadata.Tags,
			Agents:      &tmpl.Spec.Agents,
		}
		if tmpl.Spec.Prompts.Initial != "" || len(tmpl.Spec.Prompts.PerAgent) > 0 {
			result.Prompts = &tmpl.Spec.Prompts
		}
		if tmpl.Spec.CASS.Enabled != nil && *tmpl.Spec.CASS.Enabled {
			result.CASS = &tmpl.Spec.CASS
		}
		if tmpl.Spec.Beads.AutoAssign || tmpl.Spec.Beads.Filter != "" {
			result.Beads = &tmpl.Spec.Beads
		}
		if tmpl.Spec.Options.Stagger != nil || tmpl.Spec.Options.Checkpoint != nil {
			result.Options = &tmpl.Spec.Options
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	t := theme.Current()
	fmt.Printf("%sSession Template: %s%s\n", "\033[1m", tmpl.Metadata.Name, "\033[0m")
	fmt.Printf("%s%s%s\n\n", "\033[2m", strings.Repeat("\u2500", 60), "\033[0m")

	fmt.Printf("  Description:  %s\n", tmpl.Metadata.Description)
	fmt.Printf("  Source:       %s\n", tmpl.Metadata.Source)
	if len(tmpl.Metadata.Tags) > 0 {
		fmt.Printf("  Tags:         %s\n", strings.Join(tmpl.Metadata.Tags, ", "))
	}
	fmt.Printf("  Total Agents: %d\n\n", tmpl.GetAgentCount())

	// Agents section
	fmt.Printf("  %sAgents:%s\n", "\033[1m", "\033[0m")
	if tmpl.Spec.Agents.Claude != nil {
		model := tmpl.Spec.Agents.Claude.Model
		if model == "" {
			model = "default"
		}
		fmt.Printf("    %sClaude%s (cc) x %d [model: %s]\n",
			colorize(t.Primary), "\033[0m", tmpl.Spec.Agents.Claude.Count, model)
	}
	if tmpl.Spec.Agents.Codex != nil {
		model := tmpl.Spec.Agents.Codex.Model
		if model == "" {
			model = "default"
		}
		fmt.Printf("    %sCodex%s (cod) x %d [model: %s]\n",
			colorize(t.Primary), "\033[0m", tmpl.Spec.Agents.Codex.Count, model)
	}
	if tmpl.Spec.Agents.Gemini != nil {
		model := tmpl.Spec.Agents.Gemini.Model
		if model == "" {
			model = "default"
		}
		fmt.Printf("    %sGemini%s (gmi) x %d [model: %s]\n",
			colorize(t.Primary), "\033[0m", tmpl.Spec.Agents.Gemini.Count, model)
	}
	if tmpl.Spec.Agents.Cursor != nil {
		model := tmpl.Spec.Agents.Cursor.Model
		if model == "" {
			model = "default"
		}
		fmt.Printf("    %sCursor%s (cursor) x %d [model: %s]\n",
			colorize(t.Primary), "\033[0m", tmpl.Spec.Agents.Cursor.Count, model)
	}
	if tmpl.Spec.Agents.Windsurf != nil {
		model := tmpl.Spec.Agents.Windsurf.Model
		if model == "" {
			model = "default"
		}
		fmt.Printf("    %sWindsurf%s (windsurf) x %d [model: %s]\n",
			colorize(t.Primary), "\033[0m", tmpl.Spec.Agents.Windsurf.Count, model)
	}
	if tmpl.Spec.Agents.Aider != nil {
		model := tmpl.Spec.Agents.Aider.Model
		if model == "" {
			model = "default"
		}
		fmt.Printf("    %sAider%s (aider) x %d [model: %s]\n",
			colorize(t.Primary), "\033[0m", tmpl.Spec.Agents.Aider.Count, model)
	}
	if tmpl.Spec.Agents.Ollama != nil {
		model := tmpl.Spec.Agents.Ollama.Model
		if model == "" {
			model = "default"
		}
		fmt.Printf("    %sOllama%s (ollama) x %d [model: %s]\n",
			colorize(t.Primary), "\033[0m", tmpl.Spec.Agents.Ollama.Count, model)
	}
	fmt.Println()

	// Prompts section
	if tmpl.Spec.Prompts.Initial != "" {
		fmt.Printf("  %sInitial Prompt:%s\n", "\033[1m", "\033[0m")
		// Show first 3 lines of initial prompt
		lines := strings.Split(tmpl.Spec.Prompts.Initial, "\n")
		maxLines := 5
		if len(lines) > maxLines {
			for _, line := range lines[:maxLines] {
				fmt.Printf("    %s\n", line)
			}
			fmt.Printf("    %s... (%d more lines)%s\n", "\033[2m", len(lines)-maxLines, "\033[0m")
		} else {
			for _, line := range lines {
				fmt.Printf("    %s\n", line)
			}
		}
		fmt.Println()
	}

	// CASS section
	if tmpl.Spec.CASS.Enabled != nil && *tmpl.Spec.CASS.Enabled {
		fmt.Printf("  %sCASS:%s enabled", "\033[1m", "\033[0m")
		if tmpl.Spec.CASS.Query != "" {
			fmt.Printf(" (query: %q)", tmpl.Spec.CASS.Query)
		}
		if tmpl.Spec.CASS.MaxSessions > 0 {
			fmt.Printf(" [max: %d sessions]", tmpl.Spec.CASS.MaxSessions)
		}
		fmt.Print("\n\n")
	}

	// Beads section
	if tmpl.Spec.Beads.AutoAssign || tmpl.Spec.Beads.Filter != "" {
		fmt.Printf("  %sBeads:%s", "\033[1m", "\033[0m")
		if tmpl.Spec.Beads.AutoAssign {
			fmt.Printf(" auto-assign")
		}
		if tmpl.Spec.Beads.Filter != "" {
			fmt.Printf(" (filter: %q)", tmpl.Spec.Beads.Filter)
		}
		fmt.Print("\n\n")
	}

	// Options section
	if tmpl.Spec.Options.Stagger != nil && tmpl.Spec.Options.Stagger.Enabled {
		fmt.Printf("  %sOptions:%s stagger=%s\n\n", "\033[1m", "\033[0m", tmpl.Spec.Options.Stagger.Interval)
	}

	return nil
}

func sessionTemplateErrorResponse(err error, name string) map[string]interface{} {
	resp := map[string]interface{}{
		"error": err.Error(),
	}
	if suggestions := sessionTemplateSuggestions(err, name); len(suggestions) > 0 {
		resp["suggestions"] = suggestions
	}
	return resp
}

func sessionTemplateSuggestions(err error, name string) []string {
	if err == nil {
		return nil
	}

	errStr := err.Error()
	var suggestions []string

	if strings.Contains(errStr, "session template not found") {
		suggestions = append(suggestions, "Run `ntm session-templates list` to see available templates")
		suggestions = append(suggestions, "Check ~/.config/ntm/templates or .ntm/templates for custom templates")
		if name != "" {
			suggestions = append(suggestions, fmt.Sprintf("Check spelling for template name %q", name))
		}
	}

	if strings.Contains(errStr, "validation failed") {
		suggestions = append(suggestions, "Fix validation errors in the template YAML")
		suggestions = append(suggestions, "Ensure metadata.name uses only letters, numbers, '-' or '_'")
		if name != "" {
			suggestions = append(suggestions, fmt.Sprintf("Rename metadata.name to a valid value like %q", name))
		}
	}

	if len(suggestions) == 0 {
		suggestions = append(suggestions, "Run `ntm session-templates list` to see available templates")
	}

	return suggestions
}

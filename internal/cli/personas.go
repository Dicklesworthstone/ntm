package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func newPersonasCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "personas",
		Short: "Manage agent personas",
		Long: `List and inspect available agent personas.

Personas define agent characteristics including agent type, model,
system prompts, and behavioral settings.

Persona sources (later overrides earlier):
  1. Built-in: Compiled into ntm
  2. User: ~/.config/ntm/personas.toml
  3. Project: .ntm/personas.toml

Examples:
  ntm personas list              # List all personas
  ntm personas list --json       # JSON output
  ntm personas show architect    # Show persona details
  ntm personas show architect --json`,
	}

	cmd.AddCommand(
		newPersonasListCmd(),
		newPersonasShowCmd(),
		newProfileSwitchCmd(),
	)

	return cmd
}

func newPersonasListCmd() *cobra.Command {
	var (
		filterAgent string
		filterTag   string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available personas",
		Long:  `List all available personas from built-in, user, and project sources.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPersonasList(filterAgent, filterTag)
		},
	}

	cmd.Flags().StringVar(&filterAgent, "agent", "", "Filter by agent type alias (claude|cc, codex|cod, gemini|gmi, cursor, windsurf|ws, aider, ollama)")
	cmd.Flags().StringVar(&filterTag, "tag", "", "Filter by tag")

	return cmd
}

func runPersonasList(filterAgent, filterTag string) error {
	// Get project directory (current working directory)
	cwd, _ := os.Getwd()

	registry, err := persona.LoadRegistry(cwd)
	if err != nil {
		return err
	}

	personas := registry.List()

	// Sort by name
	sort.Slice(personas, func(i, j int) bool {
		return personas[i].Name < personas[j].Name
	})

	// Apply filters
	filtered := make([]*persona.Persona, 0, len(personas))
	for _, p := range personas {
		// Agent type filter
		if !matchesPersonaAgentFilter(p.AgentType, filterAgent) {
			continue
		}

		// Tag filter
		if filterTag != "" {
			hasTag := false
			for _, tag := range p.Tags {
				if strings.EqualFold(tag, filterTag) {
					hasTag = true
					break
				}
			}
			if !hasTag {
				continue
			}
		}

		filtered = append(filtered, p)
	}

	if jsonOutput {
		// Ensure profile sets is never null in JSON output
		sets := registry.ListSets()
		if sets == nil {
			sets = []*persona.PersonaSet{}
		}
		// Include both personas and profile sets in JSON output
		output := struct {
			Personas    []*persona.Persona    `json:"personas"`
			ProfileSets []*persona.PersonaSet `json:"profile_sets"`
		}{
			Personas:    filtered,
			ProfileSets: sets,
		}
		return json.NewEncoder(os.Stdout).Encode(output)
	}

	th := theme.Current()
	ic := icons.Current()

	if len(filtered) == 0 {
		fmt.Println(InfoMessage("No personas found matching filters."))
		return nil
	}

	// Build styled table
	table := NewStyledTable("NAME", "AGENT", "MODEL", "DESCRIPTION")
	table.WithTitle(ic.Profile + " Agent Profiles")

	for _, p := range filtered {
		desc := truncateRunes(p.Description, 38, "...")
		model := p.Model
		if model == "" {
			model = "(default)"
		}
		model = truncateRunes(model, 6, "..")

		table.AddRow(
			p.Name,
			formatAgentType(p.AgentTypeFlag(), th, ic),
			model,
			desc,
		)
	}

	builtinCount := len(persona.BuiltinPersonas())
	table.WithFooter(fmt.Sprintf("  %s %d profiles (%d built-in)", ic.Info, len(filtered), builtinCount))
	fmt.Print(table.Render())

	// Show profile sets
	sets := registry.ListSets()
	if len(sets) > 0 {
		fmt.Println()
		renderProfileSets(sets, th, ic)
	}

	return nil
}

// formatAgentType formats an agent type with icon and color
func formatAgentType(agentType string, th theme.Theme, ic icons.IconSet) string {
	icon, color, label := personaAgentPresentation(agentType, th, ic)
	style := lipgloss.NewStyle().Foreground(color)
	return style.Render(icon + " " + label)
}

func matchesPersonaAgentFilter(agentType, filter string) bool {
	if strings.TrimSpace(filter) == "" {
		return true
	}
	return normalizePersonaAgentType(agentType) == normalizePersonaAgentType(filter)
}

func personaAgentPresentation(agentType string, th theme.Theme, ic icons.IconSet) (string, lipgloss.Color, string) {
	canonical := normalizePersonaAgentType(agentType)
	switch canonical {
	case "cc":
		return ic.Claude, th.Claude, "cc"
	case "cod":
		return ic.Codex, th.Codex, "cod"
	case "gmi":
		return ic.Gemini, th.Gemini, "gmi"
	case "cursor":
		return ic.Cursor, th.Cursor, "cursor"
	case "windsurf":
		return ic.Windsurf, th.Windsurf, "windsurf"
	case "aider":
		return ic.Aider, th.Aider, "aider"
	case "ollama":
		return ic.Ollama, th.Ollama, "ollama"
	case "user":
		return ic.User, th.User, "user"
	default:
		label := strings.TrimSpace(agentType)
		if label == "" {
			label = "unknown"
		}
		return ic.Robot, th.Overlay, label
	}
}

func normalizePersonaAgentType(agentType string) string {
	return string(agentpkg.AgentType(agentType).Canonical())
}

// renderProfileSets renders profile sets section with styling
func renderProfileSets(sets []*persona.PersonaSet, th theme.Theme, ic icons.IconSet) {
	headerStyle := lipgloss.NewStyle().Foreground(th.Primary).Bold(true)
	nameStyle := lipgloss.NewStyle().Foreground(th.Text).Bold(true)
	countStyle := lipgloss.NewStyle().Foreground(th.Subtext)
	descStyle := lipgloss.NewStyle().Foreground(th.Subtext)

	fmt.Println(headerStyle.Render("╭─ " + ic.Folder + " Profile Sets ─"))

	sort.Slice(sets, func(i, j int) bool {
		return sets[i].Name < sets[j].Name
	})

	for _, s := range sets {
		desc := s.Description
		if desc == "" {
			desc = strings.Join(s.Personas, ", ")
		}
		desc = truncateRunes(desc, 60, "...")

		fmt.Printf("  %s %s %s\n",
			nameStyle.Render(s.Name),
			countStyle.Render(fmt.Sprintf("(%d)", len(s.Personas))),
			descStyle.Render(desc),
		)
	}
}

func newPersonasShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show persona details",
		Long:  `Show detailed information about a specific persona.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPersonasShow(args[0])
		},
	}

	return cmd
}

func runPersonasShow(name string) error {
	cwd, _ := os.Getwd()

	registry, err := persona.LoadRegistry(cwd)
	if err != nil {
		return err
	}

	p, ok := registry.Get(name)
	if !ok {
		if jsonOutput {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("persona %q not found", name),
			})
		}
		return fmt.Errorf("persona %q not found", name)
	}

	if jsonOutput {
		type personaWithSource struct {
			*persona.Persona
			Source string `json:"source"`
		}
		output := personaWithSource{
			Persona: p,
			Source:  determineSource(p.Name),
		}
		return json.NewEncoder(os.Stdout).Encode(output)
	}

	th := theme.Current()
	ic := icons.Current()

	// Styles
	headerStyle := lipgloss.NewStyle().Foreground(th.Primary).Bold(true)
	labelStyle := lipgloss.NewStyle().Foreground(th.Subtext)
	valueStyle := lipgloss.NewStyle().Foreground(th.Text)
	borderStyle := lipgloss.NewStyle().Foreground(th.Surface2)
	sectionStyle := lipgloss.NewStyle().Foreground(th.Primary).Bold(true)
	bulletStyle := lipgloss.NewStyle().Foreground(th.Blue)

	// Box-drawing characters
	topLeft := "╭"
	vertical := "│"
	branch := "├"
	bottomLeft := "╰"

	// Header - use appropriate icon for agent type
	agentIcon, _, _ := personaAgentPresentation(p.AgentType, th, ic)
	fmt.Println(headerStyle.Render(topLeft + "─ " + agentIcon + " Profile: " + p.Name))
	fmt.Println(borderStyle.Render(vertical))

	// Basic info with tree structure
	fmt.Println(borderStyle.Render(vertical) + " " + labelStyle.Render("Agent Type:") + "    " + formatAgentType(p.AgentType, th, ic))
	fmt.Println(borderStyle.Render(vertical) + " " + labelStyle.Render("Model:") + "         " + valueStyle.Render(valueOrDefault(p.Model, "(default)")))

	if p.Description != "" {
		fmt.Println(borderStyle.Render(vertical) + " " + labelStyle.Render("Description:") + "   " + valueStyle.Render(p.Description))
	}

	if len(p.Tags) > 0 {
		fmt.Println(borderStyle.Render(vertical) + " " + labelStyle.Render("Tags:") + "          " + renderTags(p.Tags, th))
	}

	if p.Temperature != nil {
		tempStr := fmt.Sprintf("%.1f %s", *p.Temperature, renderTempBar(*p.Temperature, th))
		fmt.Println(borderStyle.Render(vertical) + " " + labelStyle.Render("Temperature:") + "   " + valueStyle.Render(tempStr))
	}

	fmt.Println(borderStyle.Render(vertical) + " " + labelStyle.Render("Source:") + "        " + valueStyle.Render(ic.Star+" "+determineSource(p.Name)))

	// Focus patterns
	if len(p.FocusPatterns) > 0 {
		fmt.Println(borderStyle.Render(vertical))
		fmt.Println(borderStyle.Render(branch) + "─ " + sectionStyle.Render(ic.Folder+" Focus Patterns"))
		for _, fp := range p.FocusPatterns {
			fmt.Println(borderStyle.Render(vertical) + "   " + bulletStyle.Render("•") + " " + valueStyle.Render(fp))
		}
	}

	// Context files
	if len(p.ContextFiles) > 0 {
		fmt.Println(borderStyle.Render(vertical))
		fmt.Println(borderStyle.Render(branch) + "─ " + sectionStyle.Render(ic.File+" Context Files"))
		for _, cf := range p.ContextFiles {
			fmt.Println(borderStyle.Render(vertical) + "   " + bulletStyle.Render("•") + " " + valueStyle.Render(cf))
		}
	}

	// System prompt
	if p.SystemPrompt != "" {
		fmt.Println(borderStyle.Render(vertical))
		fmt.Println(borderStyle.Render(branch) + "─ " + sectionStyle.Render(ic.Terminal+" System Prompt"))
		fmt.Println(borderStyle.Render(vertical))
		lines := strings.Split(p.SystemPrompt, "\n")
		for _, line := range lines {
			fmt.Println(borderStyle.Render(vertical) + "   " + valueStyle.Render(line))
		}
	}

	// Bottom border
	fmt.Println(borderStyle.Render(bottomLeft + strings.Repeat("─", 50)))

	return nil
}

// renderTempBar renders a visual temperature indicator
func renderTempBar(temp float64, th theme.Theme) string {
	var color lipgloss.Color
	var label string

	switch {
	case temp <= 0.3:
		color = th.Blue
		label = "focused"
	case temp <= 0.7:
		color = th.Green
		label = "balanced"
	case temp <= 1.0:
		color = th.Yellow
		label = "creative"
	default:
		color = th.Red
		label = "wild"
	}

	style := lipgloss.NewStyle().Foreground(color)
	return style.Render("(" + label + ")")
}

// renderTags renders tags as styled hashtags
func renderTags(tags []string, th theme.Theme) string {
	var parts []string
	tagStyle := lipgloss.NewStyle().Foreground(th.Blue)

	for _, tag := range tags {
		parts = append(parts, tagStyle.Render("#"+tag))
	}
	return strings.Join(parts, " ")
}

// determineSource returns the source of a persona (built-in, user, or project)
func determineSource(name string) string {
	cwd, _ := os.Getwd()
	return determineSourceFromProjectDir(name, cwd)
}

func determineSourceFromProjectDir(name, projectDir string) string {
	projectPath := filepath.Join(projectDir, persona.DefaultProjectPath())
	if cfg, err := persona.LoadFromFile(projectPath); err == nil {
		for _, p := range cfg.Personas {
			if strings.EqualFold(p.Name, name) {
				return fmt.Sprintf("project (%s)", persona.DefaultProjectPath())
			}
		}
	}

	if cfg, userPath, err := persona.LoadUserConfig(); err == nil && cfg != nil {
		for _, p := range cfg.Personas {
			if strings.EqualFold(p.Name, name) {
				return fmt.Sprintf("user (%s)", userPath)
			}
		}
	}

	for _, bp := range persona.BuiltinPersonas() {
		if strings.EqualFold(bp.Name, name) {
			return "built-in"
		}
	}

	return "unknown"
}

func valueOrDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// truncateRunes truncates a string to maxRunes runes plus suffix.
// This is UTF-8 safe unlike byte slicing.
func truncateRunes(s string, maxRunes int, suffix string) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + suffix
}

// newProfilesCmd creates an alias for 'personas' command as 'profiles'
// This provides user-friendly naming that aligns with the spawn --profiles flag
func newProfilesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profiles",
		Short: "Manage agent profiles (alias for 'personas')",
		Long: `List and inspect available agent profiles.

This is an alias for 'ntm personas'. Profiles define agent characteristics
including agent type, model, system prompts, and behavioral settings.

Profile sources (later overrides earlier):
  1. Built-in: Compiled into ntm
  2. User: ~/.config/ntm/personas.toml
  3. Project: .ntm/personas.toml

Examples:
  ntm profiles list              # List all profiles
  ntm profiles list --json       # JSON output
  ntm profiles show architect    # Show profile details
  ntm profiles show architect --json`,
	}

	cmd.AddCommand(
		newPersonasListCmd(),
		newPersonasShowCmd(),
		newProfileSwitchCmd(),
	)

	return cmd
}

func init() {
	rootCmd.AddCommand(newPersonasCmd())
	rootCmd.AddCommand(newProfilesCmd())
}

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/recipe"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/spf13/cobra"
)

// RecipesListResult is the JSON output for recipes list command.
type RecipesListResult struct {
	Recipes []RecipeInfo `json:"recipes"`
	Total   int          `json:"total"`
}

// RecipeInfo is a recipe summary for JSON output.
type RecipeInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	TotalAgents int    `json:"total_agents"`
}

// RecipeShowResult is the JSON output for recipes show command.
type RecipeShowResult struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Source      string             `json:"source"`
	TotalAgents int                `json:"total_agents"`
	Agents      []recipe.AgentSpec `json:"agents"`
}

func newRecipesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recipes",
		Short: "Manage session recipes (presets)",
		Long: `List and view session recipes (presets).

Recipes are reusable session configurations that define agent types,
counts, and optional model/persona overrides.

Sources (in precedence order):
  1. Built-in recipes (lowest priority)
  2. User recipes (~/.config/ntm/recipes.toml)
  3. Project recipes (.ntm/recipes.toml) (highest priority)

Examples:
  ntm recipes list              # List all available recipes
  ntm recipes show full-stack   # Show details of a recipe
  ntm recipes list --json       # JSON output for scripts`,
	}

	cmd.AddCommand(newRecipesListCmd())
	cmd.AddCommand(newRecipesShowCmd())

	return cmd
}

func newRecipesListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available recipes",
		Long: `List all available recipes from all sources.

Recipes are shown with their name, description, source, and total agent count.

Examples:
  ntm recipes list           # Human-readable table
  ntm recipes list --json    # JSON output for scripts`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRecipesList()
		},
	}

	return cmd
}

func runRecipesList() error {
	loader := recipe.NewLoader()
	recipes, err := loader.LoadAll()
	if err != nil {
		if jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
				"error": err.Error(),
			})
		}
		return err
	}

	if jsonOutput {
		result := RecipesListResult{
			Recipes: make([]RecipeInfo, len(recipes)),
			Total:   len(recipes),
		}
		for i, r := range recipes {
			result.Recipes[i] = RecipeInfo{
				Name:        r.Name,
				Description: r.Description,
				Source:      r.Source,
				TotalAgents: r.TotalAgents(),
			}
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	if len(recipes) == 0 {
		fmt.Println("No recipes found.")
		return nil
	}

	t := theme.Current()
	fmt.Printf("%sAvailable Recipes%s\n", "\033[1m", "\033[0m")
	fmt.Printf("%s%s%s\n\n", "\033[2m", strings.Repeat("\u2500", 60), "\033[0m")

	// Group by source
	bySource := make(map[string][]recipe.Recipe)
	for _, r := range recipes {
		bySource[r.Source] = append(bySource[r.Source], r)
	}

	// Print in order: builtin, user, project
	sources := []string{"builtin", "user", "project"}
	sourceLabels := map[string]string{
		"builtin": "Built-in",
		"user":    "User (~/.config/ntm/recipes.toml)",
		"project": "Project (.ntm/recipes.toml)",
	}

	for _, source := range sources {
		group := bySource[source]
		if len(group) == 0 {
			continue
		}

		fmt.Printf("  %s%s:%s\n", colorize(t.Info), sourceLabels[source], "\033[0m")
		for _, r := range group {
			counts := r.AgentCounts()
			var parts []string
			if c := counts["cc"]; c > 0 {
				parts = append(parts, fmt.Sprintf("%d cc", c))
			}
			if c := counts["cod"]; c > 0 {
				parts = append(parts, fmt.Sprintf("%d cod", c))
			}
			if c := counts["gmi"]; c > 0 {
				parts = append(parts, fmt.Sprintf("%d gmi", c))
			}
			agentSummary := strings.Join(parts, ", ")

			fmt.Printf("    %s%-15s%s %s\n", colorize(t.Primary), r.Name, "\033[0m", r.Description)
			fmt.Printf("    %s               [%s]%s\n", "\033[2m", agentSummary, "\033[0m")
		}
		fmt.Println()
	}

	fmt.Printf("%sTotal: %d recipe(s)%s\n", "\033[2m", len(recipes), "\033[0m")

	return nil
}

func newRecipesShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <recipe-name>",
		Short: "Show details of a recipe",
		Long: `Show detailed information about a specific recipe.

Examples:
  ntm recipes show full-stack     # Show full-stack recipe
  ntm recipes show balanced --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRecipesShow(args[0])
		},
	}

	return cmd
}

func runRecipesShow(name string) error {
	loader := recipe.NewLoader()
	r, err := loader.Get(name)
	if err != nil {
		if jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
				"error": err.Error(),
			})
		}
		return err
	}

	if jsonOutput {
		result := RecipeShowResult{
			Name:        r.Name,
			Description: r.Description,
			Source:      r.Source,
			TotalAgents: r.TotalAgents(),
			Agents:      r.Agents,
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	t := theme.Current()
	fmt.Printf("%sRecipe: %s%s\n", "\033[1m", r.Name, "\033[0m")
	fmt.Printf("%s%s%s\n\n", "\033[2m", strings.Repeat("\u2500", 50), "\033[0m")

	fmt.Printf("  Description: %s\n", r.Description)
	fmt.Printf("  Source:      %s\n", sourceDescription(r.Source))
	fmt.Printf("  Total:       %d agent(s)\n\n", r.TotalAgents())

	fmt.Printf("  %sAgents:%s\n", "\033[1m", "\033[0m")
	for _, a := range r.Agents {
		agentType := formatAgentType(a.Type)
		line := fmt.Sprintf("    %s%s%s x %d", colorize(t.Primary), agentType, "\033[0m", a.Count)
		if a.Model != "" {
			line += fmt.Sprintf(" (model: %s)", a.Model)
		}
		if a.Persona != "" {
			line += fmt.Sprintf(" (persona: %s)", a.Persona)
		}
		fmt.Println(line)
	}

	fmt.Println()
	fmt.Printf("%sUsage: ntm spawn <session> --recipe=%s%s\n", "\033[2m", r.Name, "\033[0m")

	return nil
}

// sourceDescription returns a human-readable description of the source.
func sourceDescription(source string) string {
	switch source {
	case "builtin":
		return "Built-in"
	case "user":
		return "User (~/.config/ntm/recipes.toml)"
	case "project":
		return "Project (.ntm/recipes.toml)"
	default:
		return source
	}
}

// formatAgentType returns a formatted agent type name.
func formatAgentType(t string) string {
	switch t {
	case "cc":
		return "Claude"
	case "cod":
		return "Codex"
	case "gmi":
		return "Gemini"
	default:
		return t
	}
}

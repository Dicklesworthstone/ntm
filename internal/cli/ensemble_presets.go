package cli

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/output"
)

// ensemblePresetsOptions holds flags for the ensemble presets command.
type ensemblePresetsOptions struct {
	Format  string
	Verbose bool
	Tag     string
}

// ensemblePresetRow is a summary row for table/JSON output.
type ensemblePresetRow struct {
	Name          string   `json:"name" yaml:"name"`
	DisplayName   string   `json:"display_name,omitempty" yaml:"display_name,omitempty"`
	Description   string   `json:"description" yaml:"description"`
	ModeCodes     []string `json:"mode_codes" yaml:"mode_codes"`
	ModeCount     int      `json:"mode_count" yaml:"mode_count"`
	Strategy      string   `json:"synthesis_strategy" yaml:"synthesis_strategy"`
	MaxTokens     int      `json:"max_total_tokens" yaml:"max_total_tokens"`
	AllowAdvanced bool     `json:"allow_advanced,omitempty" yaml:"allow_advanced,omitempty"`
	Tags          []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	Source        string   `json:"source" yaml:"source"`
}

// ensemblePresetDetail is a verbose row with full configuration.
type ensemblePresetDetail struct {
	Name              string                        `json:"name" yaml:"name"`
	DisplayName       string                        `json:"display_name,omitempty" yaml:"display_name,omitempty"`
	Description       string                        `json:"description" yaml:"description"`
	Modes             []ensemblePresetModeDetail    `json:"modes" yaml:"modes"`
	ModeCount         int                           `json:"mode_count" yaml:"mode_count"`
	Synthesis         ensemblePresetSynthesisDetail `json:"synthesis" yaml:"synthesis"`
	Budget            ensemblePresetBudgetDetail    `json:"budget" yaml:"budget"`
	AllowAdvanced     bool                          `json:"allow_advanced,omitempty" yaml:"allow_advanced,omitempty"`
	AgentDistribution *agentDistributionDetail      `json:"agent_distribution,omitempty" yaml:"agent_distribution,omitempty"`
	Tags              []string                      `json:"tags,omitempty" yaml:"tags,omitempty"`
	Source            string                        `json:"source" yaml:"source"`
}

// ensemblePresetModeDetail holds mode info for verbose output.
type ensemblePresetModeDetail struct {
	ID       string `json:"id" yaml:"id"`
	Code     string `json:"code,omitempty" yaml:"code,omitempty"`
	Name     string `json:"name,omitempty" yaml:"name,omitempty"`
	Category string `json:"category,omitempty" yaml:"category,omitempty"`
	Tier     string `json:"tier,omitempty" yaml:"tier,omitempty"`
}

// ensemblePresetSynthesisDetail holds synthesis config for verbose output.
type ensemblePresetSynthesisDetail struct {
	Strategy      string  `json:"strategy" yaml:"strategy"`
	MinConfidence float64 `json:"min_confidence" yaml:"min_confidence"`
	MaxFindings   int     `json:"max_findings" yaml:"max_findings"`
}

// ensemblePresetBudgetDetail holds budget config for verbose output.
type ensemblePresetBudgetDetail struct {
	MaxTokensPerMode int    `json:"max_tokens_per_mode" yaml:"max_tokens_per_mode"`
	MaxTotalTokens   int    `json:"max_total_tokens" yaml:"max_total_tokens"`
	TimeoutPerMode   string `json:"timeout_per_mode" yaml:"timeout_per_mode"`
	TotalTimeout     string `json:"total_timeout" yaml:"total_timeout"`
}

// agentDistributionDetail holds agent distribution config for verbose output.
type agentDistributionDetail struct {
	Strategy           string `json:"strategy" yaml:"strategy"`
	MaxAgents          int    `json:"max_agents,omitempty" yaml:"max_agents,omitempty"`
	PreferredAgentType string `json:"preferred_agent_type,omitempty" yaml:"preferred_agent_type,omitempty"`
}

// ensemblePresetsOutput is the top-level output structure.
type ensemblePresetsOutput struct {
	GeneratedAt time.Time              `json:"generated_at" yaml:"generated_at"`
	Count       int                    `json:"count" yaml:"count"`
	Presets     []ensemblePresetRow    `json:"presets,omitempty" yaml:"presets,omitempty"`
	Details     []ensemblePresetDetail `json:"details,omitempty" yaml:"details,omitempty"`
}

// newEnsemblePresetsCmd creates the ensemble presets command.
// This command lists available ensemble presets (built-in + user-defined).
// Alias: ntm ensemble list
func newEnsemblePresetsCmd() *cobra.Command {
	opts := ensemblePresetsOptions{
		Format: "table",
	}

	cmd := &cobra.Command{
		Use:     "presets",
		Aliases: []string{"list"},
		Short:   "List available ensemble presets",
		Long: `List all available ensemble presets (built-in + user-defined).

Presets are pre-configured ensembles that bundle related reasoning modes
for common tasks like project diagnosis, bug hunting, or architecture review.

Sources (in precedence order):
  1. Embedded (built into NTM)
  2. User (~/.config/ntm/ensembles.toml)
  3. Project (.ntm/ensembles.toml - highest priority)

Formats:
  --format=table (default) - Human-readable table
  --format=json            - JSON output for automation
  --format=yaml            - YAML output

Use --verbose to include full preset configurations including mode details,
synthesis settings, and budget limits.`,
		Example: `  ntm ensemble presets                     # List all presets
  ntm ensemble presets --format=json       # JSON output
  ntm ensemble presets --verbose           # Full configuration details
  ntm ensemble presets --tag=analysis      # Filter by tag
  ntm ensemble list                        # Alias for presets`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnsemblePresets(cmd.OutOrStdout(), opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Format, "format", "f", "table", "Output format: table, json, yaml")
	cmd.Flags().BoolVarP(&opts.Verbose, "verbose", "v", false, "Show full preset configurations")
	cmd.Flags().StringVarP(&opts.Tag, "tag", "t", "", "Filter by tag")

	return cmd
}

// runEnsemblePresets executes the ensemble presets command.
func runEnsemblePresets(w io.Writer, opts ensemblePresetsOptions) error {
	format := strings.ToLower(strings.TrimSpace(opts.Format))
	if format == "" {
		format = "table"
	}
	if jsonOutput {
		format = "json"
	}

	slog.Default().Info("ensemble presets: loading registry",
		"format", format,
		"verbose", opts.Verbose,
		"tag_filter", opts.Tag,
	)

	// Load ensemble registry
	registry, err := ensemble.GlobalEnsembleRegistry()
	if err != nil {
		slog.Default().Error("ensemble presets: failed to load registry", "error", err)
		if format == "json" {
			return output.WriteJSON(w, output.NewError(err.Error()), true)
		}
		return fmt.Errorf("load ensemble registry: %w", err)
	}

	// Get presets (optionally filtered by tag)
	var presets []ensemble.EnsemblePreset
	if opts.Tag != "" {
		presets = registry.ListByTag(opts.Tag)
	} else {
		presets = registry.List()
	}

	slog.Default().Info("ensemble presets: loaded presets",
		"count", len(presets),
		"tag_filter", opts.Tag,
	)

	// Load catalog for mode resolution
	catalog, err := ensemble.GlobalCatalog()
	if err != nil {
		slog.Default().Warn("ensemble presets: failed to load mode catalog", "error", err)
		// Continue without mode resolution - we can still show mode IDs
	}

	if opts.Verbose {
		return renderPresetsVerbose(w, presets, catalog, format)
	}
	return renderPresetsSummary(w, presets, catalog, format)
}

// renderPresetsSummary renders presets in summary format.
func renderPresetsSummary(w io.Writer, presets []ensemble.EnsemblePreset, catalog *ensemble.ModeCatalog, format string) error {
	rows := make([]ensemblePresetRow, 0, len(presets))
	for _, p := range presets {
		modeCodes := make([]string, 0, len(p.Modes))
		for _, mref := range p.Modes {
			code := mref.ID
			if catalog != nil {
				if mode := catalog.GetMode(mref.ID); mode != nil {
					code = mode.Code
				}
			}
			modeCodes = append(modeCodes, code)
		}

		rows = append(rows, ensemblePresetRow{
			Name:          p.Name,
			DisplayName:   p.DisplayName,
			Description:   p.Description,
			ModeCodes:     modeCodes,
			ModeCount:     len(p.Modes),
			Strategy:      p.Synthesis.Strategy.String(),
			MaxTokens:     p.Budget.MaxTotalTokens,
			AllowAdvanced: p.AllowAdvanced,
			Tags:          p.Tags,
			Source:        p.Source,
		})
	}

	result := ensemblePresetsOutput{
		GeneratedAt: output.Timestamp(),
		Count:       len(rows),
		Presets:     rows,
	}

	switch format {
	case "json":
		return output.WriteJSON(w, result, true)
	case "yaml", "yml":
		return renderYAML(w, result)
	default:
		return renderPresetsTable(w, rows)
	}
}

// renderPresetsVerbose renders presets with full configuration details.
func renderPresetsVerbose(w io.Writer, presets []ensemble.EnsemblePreset, catalog *ensemble.ModeCatalog, format string) error {
	details := make([]ensemblePresetDetail, 0, len(presets))
	for _, p := range presets {
		modes := make([]ensemblePresetModeDetail, 0, len(p.Modes))
		for _, mref := range p.Modes {
			md := ensemblePresetModeDetail{
				ID: mref.ID,
			}
			if catalog != nil {
				if mode := catalog.GetMode(mref.ID); mode != nil {
					md.Code = mode.Code
					md.Name = mode.Name
					md.Category = mode.Category.String()
					md.Tier = mode.Tier.String()
				}
			}
			modes = append(modes, md)
		}

		detail := ensemblePresetDetail{
			Name:        p.Name,
			DisplayName: p.DisplayName,
			Description: p.Description,
			Modes:       modes,
			ModeCount:   len(p.Modes),
			Synthesis: ensemblePresetSynthesisDetail{
				Strategy:      p.Synthesis.Strategy.String(),
				MinConfidence: float64(p.Synthesis.MinConfidence),
				MaxFindings:   p.Synthesis.MaxFindings,
			},
			Budget: ensemblePresetBudgetDetail{
				MaxTokensPerMode: p.Budget.MaxTokensPerMode,
				MaxTotalTokens:   p.Budget.MaxTotalTokens,
				TimeoutPerMode:   p.Budget.TimeoutPerMode.String(),
				TotalTimeout:     p.Budget.TotalTimeout.String(),
			},
			AllowAdvanced: p.AllowAdvanced,
			Tags:          p.Tags,
			Source:        p.Source,
		}

		// Add agent distribution if present
		if p.AgentDistribution != nil {
			detail.AgentDistribution = &agentDistributionDetail{
				Strategy:           p.AgentDistribution.Strategy,
				MaxAgents:          p.AgentDistribution.MaxAgents,
				PreferredAgentType: p.AgentDistribution.PreferredAgentType,
			}
		}

		details = append(details, detail)
	}

	result := ensemblePresetsOutput{
		GeneratedAt: output.Timestamp(),
		Count:       len(details),
		Details:     details,
	}

	switch format {
	case "json":
		return output.WriteJSON(w, result, true)
	case "yaml", "yml":
		return renderYAML(w, result)
	default:
		return renderPresetsTableVerbose(w, details)
	}
}

// renderPresetsTable renders presets as a human-readable table.
func renderPresetsTable(w io.Writer, rows []ensemblePresetRow) error {
	if len(rows) == 0 {
		fmt.Fprintln(w, "No ensemble presets found.")
		return nil
	}

	// Print header
	fmt.Fprintf(w, "%-20s %-25s %-6s %-12s %-8s %-8s %s\n",
		"NAME", "DISPLAY", "MODES", "STRATEGY", "TOKENS", "SOURCE", "TAGS")
	fmt.Fprintln(w, strings.Repeat("-", 100))

	for _, r := range rows {
		displayName := r.DisplayName
		if len(displayName) > 25 {
			displayName = displayName[:22] + "..."
		}

		tags := "-"
		if len(r.Tags) > 0 {
			tags = strings.Join(r.Tags, ", ")
		}

		fmt.Fprintf(w, "%-20s %-25s %-6d %-12s %-8d %-8s %s\n",
			r.Name,
			displayName,
			r.ModeCount,
			r.Strategy,
			r.MaxTokens,
			r.Source,
			tags,
		)
	}

	fmt.Fprintf(w, "\nTotal: %d presets\n", len(rows))
	fmt.Fprintln(w, "\nUse --verbose for full configuration details.")
	return nil
}

// renderPresetsTableVerbose renders detailed preset info in a readable format.
func renderPresetsTableVerbose(w io.Writer, details []ensemblePresetDetail) error {
	if len(details) == 0 {
		fmt.Fprintln(w, "No ensemble presets found.")
		return nil
	}

	for i, d := range details {
		if i > 0 {
			fmt.Fprintln(w, strings.Repeat("=", 80))
		}

		fmt.Fprintf(w, "Name:        %s\n", d.Name)
		if d.DisplayName != "" {
			fmt.Fprintf(w, "Display:     %s\n", d.DisplayName)
		}
		fmt.Fprintf(w, "Description: %s\n", d.Description)
		fmt.Fprintf(w, "Source:      %s\n", d.Source)

		if len(d.Tags) > 0 {
			fmt.Fprintf(w, "Tags:        %s\n", strings.Join(d.Tags, ", "))
		}

		if d.AllowAdvanced {
			fmt.Fprintf(w, "Advanced:    yes (may include advanced-tier modes)\n")
		}

		fmt.Fprintf(w, "\nModes (%d):\n", d.ModeCount)
		for _, m := range d.Modes {
			if m.Name != "" {
				fmt.Fprintf(w, "  - %-20s [%s] %s (%s)\n", m.ID, m.Code, m.Name, m.Tier)
			} else {
				fmt.Fprintf(w, "  - %s\n", m.ID)
			}
		}

		fmt.Fprintf(w, "\nSynthesis:\n")
		fmt.Fprintf(w, "  Strategy:       %s\n", d.Synthesis.Strategy)
		fmt.Fprintf(w, "  Min Confidence: %.2f\n", d.Synthesis.MinConfidence)
		fmt.Fprintf(w, "  Max Findings:   %d\n", d.Synthesis.MaxFindings)

		fmt.Fprintf(w, "\nBudget:\n")
		fmt.Fprintf(w, "  Tokens/Mode: %d\n", d.Budget.MaxTokensPerMode)
		fmt.Fprintf(w, "  Total:       %d\n", d.Budget.MaxTotalTokens)
		fmt.Fprintf(w, "  Timeout/Mode: %s\n", d.Budget.TimeoutPerMode)
		fmt.Fprintf(w, "  Total Timeout: %s\n", d.Budget.TotalTimeout)

		if d.AgentDistribution != nil {
			fmt.Fprintf(w, "\nAgent Distribution:\n")
			fmt.Fprintf(w, "  Strategy: %s", d.AgentDistribution.Strategy)
			if d.AgentDistribution.MaxAgents > 0 {
				fmt.Fprintf(w, ", Max Agents: %d", d.AgentDistribution.MaxAgents)
			}
			if d.AgentDistribution.PreferredAgentType != "" {
				fmt.Fprintf(w, ", Preferred: %s", d.AgentDistribution.PreferredAgentType)
			}
			fmt.Fprintln(w)
		}

		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "Total: %d presets\n", len(details))
	return nil
}

// renderYAML outputs data as YAML.
func renderYAML(w io.Writer, v interface{}) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		_, err = w.Write([]byte("\n"))
		return err
	}
	return nil
}

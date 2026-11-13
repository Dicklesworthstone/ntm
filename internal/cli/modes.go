package cli

import (
	"encoding/json"
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

type modesListOutput struct {
	GeneratedAt    time.Time      `json:"generated_at" yaml:"generated_at"`
	Modes          []modesListRow `json:"modes" yaml:"modes"`
	Count          int            `json:"count" yaml:"count"`
	CatalogCount   int            `json:"catalog_count,omitempty" yaml:"catalog_count,omitempty"`
	CatalogVersion string         `json:"catalog_version,omitempty" yaml:"catalog_version,omitempty"`
	Filter         string         `json:"filter,omitempty" yaml:"filter,omitempty"`
}

type modesListRow struct {
	ID        string `json:"id" yaml:"id"`
	Code      string `json:"code" yaml:"code"`
	Name      string `json:"name" yaml:"name"`
	Category  string `json:"category" yaml:"category"`
	Tier      string `json:"tier" yaml:"tier"`
	ShortDesc string `json:"short_desc" yaml:"short_desc"`
}

type modesExplainOutput struct {
	GeneratedAt time.Time          `json:"generated_at" yaml:"generated_at"`
	Card        *ensemble.ModeCard `json:"card" yaml:"card"`
}

func newModesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "modes",
		Short: "Browse and explore reasoning modes",
		Long: `Browse, search, and get detailed explanations of reasoning modes.

Reasoning modes are specialized analytical perspectives that can be used
in ensemble runs to approach problems from different angles.

Use 'ntm modes list' to see all available modes.
Use 'ntm modes explain <mode>' to get detailed information about a mode.`,
		Example: `  ntm modes list
  ntm modes list --category Formal
  ntm modes list --tier core
  ntm modes explain deductive
  ntm modes explain A1 --format json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(newModesListCmd())
	cmd.AddCommand(newModesExplainCmd())

	return cmd
}

func newModesListCmd() *cobra.Command {
	var (
		format      string
		category    string
		tier        string
		search      string
		all         bool
		defaultOnly bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available reasoning modes",
		Long: `List all available reasoning modes with their codes and descriptions.

By default, shows only core-tier modes. Use --all to show all tiers,
--default to explicitly select the default core subset, or --search to match
modes by name, description, or best_for problem types.
Filter by category or tier to narrow results.`,
		Example: `  ntm modes list
  ntm modes list --all
  ntm modes list --default
  ntm modes list --search bayesian
  ntm modes list --category Formal
  ntm modes list --tier advanced
  ntm modes list --format json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runModesList(cmd.OutOrStdout(), format, category, tier, search, all, defaultOnly)
		},
	}

	cmd.Flags().StringVarP(&format, "format", "f", "text", "Output format: text, json, yaml")
	cmd.Flags().StringVarP(&category, "category", "c", "", "Filter by category (e.g., Formal, Causal)")
	cmd.Flags().StringVarP(&tier, "tier", "t", "", "Filter by tier (core, advanced, experimental)")
	cmd.Flags().StringVarP(&search, "search", "s", "", "Search modes by name, description, or best_for")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "Show all tiers (default: core only)")
	cmd.Flags().BoolVar(&defaultOnly, "default", false, "Show default (core) modes only")

	return cmd
}

func runModesList(w io.Writer, format, category, tier, search string, all, defaultOnly bool) error {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "text"
	}
	if jsonOutput {
		format = "json"
	}

	catalog, err := ensemble.LoadModeCatalog()
	if err != nil {
		return fmt.Errorf("load mode catalog: %w", err)
	}

	searchTerm := strings.TrimSpace(search)
	tierNorm := strings.ToLower(strings.TrimSpace(tier))

	var modes []ensemble.ReasoningMode
	switch {
	case searchTerm != "":
		modes = catalog.SearchModes(searchTerm)
		effectiveTier := tierNorm
		if defaultOnly && effectiveTier == "" {
			effectiveTier = "core"
		}
		if effectiveTier != "" && effectiveTier != "all" {
			var filtered []ensemble.ReasoningMode
			for _, m := range modes {
				if strings.EqualFold(string(m.Tier), effectiveTier) {
					filtered = append(filtered, m)
				}
			}
			modes = filtered
		}
	case defaultOnly || (!all && tierNorm == ""):
		modes = catalog.ListDefault()
		tier = "core"
	case tierNorm == "all" || (all && tierNorm == ""):
		modes = catalog.ListModes()
	default:
		modes = catalog.ListByTier(ensemble.ModeTier(tierNorm))
	}

	// Filter by category
	if category != "" {
		var filtered []ensemble.ReasoningMode
		for _, m := range modes {
			if strings.EqualFold(string(m.Category), category) {
				filtered = append(filtered, m)
			}
		}
		modes = filtered
	}

	rows := make([]modesListRow, 0, len(modes))
	for _, m := range modes {
		rows = append(rows, modesListRow{
			ID:        m.ID,
			Code:      m.Code,
			Name:      m.Name,
			Category:  string(m.Category),
			Tier:      string(m.Tier),
			ShortDesc: m.ShortDesc,
		})
	}

	var filters []string
	if category != "" {
		filters = append(filters, "category="+category)
	}
	if tier != "" && !strings.EqualFold(tier, "core") {
		filters = append(filters, "tier="+tier)
	}
	if searchTerm != "" {
		filters = append(filters, "search="+searchTerm)
	}
	filterDesc := strings.Join(filters, ", ")

	result := modesListOutput{
		GeneratedAt:    output.Timestamp(),
		Modes:          rows,
		Count:          len(rows),
		CatalogCount:   catalog.Count(),
		CatalogVersion: catalog.Version(),
		Filter:         filterDesc,
	}

	slog.Default().Info("modes list",
		"count", len(rows),
		"catalog_count", result.CatalogCount,
		"catalog_version", result.CatalogVersion,
		"category", category,
		"tier", tier,
		"search", searchTerm,
		"all", all,
		"default", defaultOnly,
	)

	return renderModesList(w, result, format)
}

func renderModesList(w io.Writer, payload modesListOutput, format string) error {
	switch format {
	case "json":
		return output.WriteJSON(w, payload, true)
	case "yaml":
		data, err := yaml.Marshal(payload)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	default:
		if len(payload.Modes) == 0 {
			fmt.Fprintf(w, "No modes found matching criteria\n")
			return nil
		}

		table := output.NewTable(w, "CODE", "ID", "NAME", "CATEGORY", "TIER")
		for _, row := range payload.Modes {
			table.AddRow(row.Code, row.ID, row.Name, row.Category, row.Tier)
		}
		table.Render()
		if payload.CatalogCount > 0 {
			ver := ""
			if payload.CatalogVersion != "" {
				ver = " v" + payload.CatalogVersion
			}
			fmt.Fprintf(w, "\n%d modes total (catalog%s: %d)\n", payload.Count, ver, payload.CatalogCount)
		} else {
			fmt.Fprintf(w, "\n%d modes total\n", payload.Count)
		}
		return nil
	}
}

func newModesExplainCmd() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "explain <mode>",
		Short: "Show detailed explanation of a reasoning mode",
		Long: `Display a detailed explanation card for a reasoning mode.

The card includes:
  - Full description and what makes the mode unique
  - Best use cases and example prompts
  - Common pitfalls to avoid
  - Estimated token cost
  - Complementary modes that work well together

You can reference a mode by ID (e.g., "deductive") or code (e.g., "A1").`,
		Example: `  ntm modes explain deductive
  ntm modes explain A1
  ntm modes explain bayesian --format json
  ntm modes explain root-cause --format yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			modeRef := args[0]
			return runModesExplain(cmd.OutOrStdout(), modeRef, format)
		},
	}

	cmd.Flags().StringVarP(&format, "format", "f", "text", "Output format: text, json, yaml")

	return cmd
}

func runModesExplain(w io.Writer, modeRef, format string) error {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "text"
	}
	if jsonOutput {
		format = "json"
	}

	catalog, err := ensemble.LoadModeCatalog()
	if err != nil {
		return fmt.Errorf("load mode catalog: %w", err)
	}

	card, err := catalog.GetModeCard(modeRef)
	if err != nil {
		return err
	}

	slog.Default().Info("mode explain",
		"mode_ref", modeRef,
		"mode_id", card.ModeID,
	)

	result := modesExplainOutput{
		GeneratedAt: output.Timestamp(),
		Card:        card,
	}

	return renderModesExplain(w, result, format)
}

func renderModesExplain(w io.Writer, payload modesExplainOutput, format string) error {
	switch format {
	case "json":
		data, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	case "yaml":
		data, err := yaml.Marshal(payload)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	default:
		fmt.Fprint(w, ensemble.FormatCard(payload.Card))
		return nil
	}
}

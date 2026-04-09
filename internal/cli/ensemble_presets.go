package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/output"
)

// ensemblePresetsOptions holds flags for the ensemble presets command.
type ensemblePresetsOptions struct {
	Format       string
	Verbose      bool
	Tag          string
	ImportedOnly bool
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
synthesis settings, and budget limits.

Use --imported to show only presets imported from external files.`,
		Example: `  ntm ensemble presets                     # List all presets
  ntm ensemble presets --format=json       # JSON output
  ntm ensemble presets --verbose           # Full configuration details
  ntm ensemble presets --imported          # Only imported presets
  ntm ensemble presets --tag=analysis      # Filter by tag
  ntm ensemble list                        # Alias for presets`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnsemblePresets(cmd.OutOrStdout(), opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Format, "format", "f", "table", "Output format: table, json, yaml")
	cmd.Flags().BoolVarP(&opts.Verbose, "verbose", "v", false, "Show full preset configurations")
	cmd.Flags().StringVarP(&opts.Tag, "tag", "t", "", "Filter by tag")
	cmd.Flags().BoolVar(&opts.ImportedOnly, "imported", false, "Show only imported presets")

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

	if opts.ImportedOnly {
		filtered := make([]ensemble.EnsemblePreset, 0, len(presets))
		for _, p := range presets {
			if p.Source == "imported" {
				filtered = append(filtered, p)
			}
		}
		presets = filtered
	}

	slog.Default().Info("ensemble presets: loaded presets",
		"count", len(presets),
		"tag_filter", opts.Tag,
		"imported_only", opts.ImportedOnly,
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

type ensembleExportOptions struct {
	Output string
	Force  bool
}

type ensembleExportOutput struct {
	GeneratedAt time.Time `json:"generated_at" yaml:"generated_at"`
	Name        string    `json:"name" yaml:"name"`
	Output      string    `json:"output" yaml:"output"`
	Bytes       int       `json:"bytes" yaml:"bytes"`
}

func newEnsembleExportCmd() *cobra.Command {
	opts := ensembleExportOptions{}

	cmd := &cobra.Command{
		Use:   "export <preset>",
		Short: "Export an ensemble preset to a TOML file",
		Long: `Export an ensemble preset for cross-project sharing.

The output is a portable TOML file that includes schema_version,
display metadata, modes, synthesis, and budget configuration.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnsembleExport(cmd.OutOrStdout(), args[0], opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Output, "output", "o", "", "Output file path (default: <preset>.toml)")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite output file if it exists")

	return cmd
}

func runEnsembleExport(w io.Writer, name string, opts ensembleExportOptions) error {
	outputPath := strings.TrimSpace(opts.Output)
	if outputPath == "" {
		outputPath = fmt.Sprintf("%s.toml", name)
	}
	outputPath = filepath.Clean(outputPath)

	slog.Default().Info("ensemble export",
		"preset", name,
		"output", outputPath,
		"force", opts.Force,
	)

	if !opts.Force {
		if _, err := os.Stat(outputPath); err == nil {
			return fmt.Errorf("output file already exists: %s (use --force to overwrite)", outputPath)
		}
	}

	registry, err := ensemble.GlobalEnsembleRegistry()
	if err != nil {
		return fmt.Errorf("load ensemble registry: %w", err)
	}
	preset := registry.Get(name)
	if preset == nil {
		return fmt.Errorf("ensemble preset %q not found", name)
	}

	payload := ensemble.ExportFromPreset(*preset)
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create output file: %w", err)
	}
	if err := toml.NewEncoder(f).Encode(payload); err != nil {
		_ = f.Close()
		return fmt.Errorf("encode TOML: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close output file: %w", err)
	}

	// Use os.Stat after close (f.Stat on closed file is undefined)
	info, err := os.Stat(outputPath)
	size := 0
	if err == nil {
		size = int(info.Size())
	}
	slog.Default().Info("ensemble export complete",
		"preset", name,
		"output", outputPath,
		"bytes", size,
	)

	result := ensembleExportOutput{
		GeneratedAt: output.Timestamp(),
		Name:        name,
		Output:      outputPath,
		Bytes:       size,
	}
	if jsonOutput {
		return output.WriteJSON(w, result, true)
	}
	_, _ = fmt.Fprintf(w, "Exported %s to %s (%d bytes)\n", name, outputPath, size)
	return nil
}

type ensembleImportOptions struct {
	AllowRemote bool
	SHA256      string
}

type ensembleImportOutput struct {
	GeneratedAt time.Time `json:"generated_at" yaml:"generated_at"`
	Name        string    `json:"name" yaml:"name"`
	Target      string    `json:"target" yaml:"target"`
	Source      string    `json:"source" yaml:"source"`
}

func undecodedEnsembleImportFields(md toml.MetaData) []string {
	fields := md.Undecoded()
	if len(fields) == 0 {
		return nil
	}
	names := make([]string, 0, len(fields))
	for _, field := range fields {
		names = append(names, field.String())
	}
	sort.Strings(names)
	return names
}

func newEnsembleImportCmd() *cobra.Command {
	opts := ensembleImportOptions{}

	cmd := &cobra.Command{
		Use:   "import <file-or-url>",
		Short: "Import an ensemble preset from a TOML file",
		Long: `Import an ensemble preset into the local imported ensembles registry.

Remote URLs are blocked by default. Use --allow-remote and provide a SHA256 checksum
to import from a remote URL safely.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnsembleImport(cmd.OutOrStdout(), args[0], opts)
		},
	}

	cmd.Flags().BoolVar(&opts.AllowRemote, "allow-remote", false, "Allow importing from http(s) URLs")
	cmd.Flags().StringVar(&opts.SHA256, "sha256", "", "SHA256 checksum for remote imports (required for http(s))")

	return cmd
}

func runEnsembleImport(w io.Writer, input string, opts ensembleImportOptions) error {
	slog.Default().Info("ensemble import",
		"source_input", input,
		"allow_remote", opts.AllowRemote,
		"checksum_provided", strings.TrimSpace(opts.SHA256) != "",
	)

	data, source, err := readEnsembleImportSource(input, opts)
	if err != nil {
		return err
	}

	var payload ensemble.EnsembleExport
	md, err := toml.Decode(string(data), &payload)
	if err != nil {
		return fmt.Errorf("parse TOML: %w", err)
	}
	if unknown := undecodedEnsembleImportFields(md); len(unknown) > 0 {
		return fmt.Errorf("parse TOML: unknown field(s): %s", strings.Join(unknown, ", "))
	}

	catalog, err := ensemble.GlobalCatalog()
	if err != nil {
		return fmt.Errorf("load mode catalog: %w", err)
	}

	registry, err := ensemble.GlobalEnsembleRegistry()
	if err != nil {
		return fmt.Errorf("load ensemble registry: %w", err)
	}
	if err := payload.Validate(catalog, registry); err != nil {
		return err
	}

	// Validate against full registry (embedded + user + project)
	allPresets, err := ensemble.LoadEnsembles(catalog)
	if err != nil {
		return fmt.Errorf("load ensembles: %w", err)
	}

	preset := payload.ToPreset()
	if existing := registry.Get(preset.Name); existing != nil && existing.Source != "imported" {
		return fmt.Errorf("import conflict: preset %q already exists from %s", preset.Name, existing.Source)
	}
	allPresets = upsertPresetByName(allPresets, preset)
	report := ensemble.ValidateEnsemblePresets(allPresets, catalog)
	if err := report.Error(); err != nil {
		return err
	}

	importPath := ensemble.ImportedEnsemblesPath("")
	importedPresets, err := ensemble.LoadEnsemblesFile(importPath)
	if err != nil {
		return err
	}
	if conflict, err := detectPresetConflict(importedPresets, preset); err != nil {
		return err
	} else if conflict {
		return fmt.Errorf("import conflict: preset %q already exists with different content", preset.Name)
	}
	importedPresets = upsertPresetByName(importedPresets, preset)
	if err := ensemble.SaveEnsemblesFile(importPath, importedPresets); err != nil {
		return err
	}

	slog.Default().Info("ensemble import complete",
		"name", preset.Name,
		"target", importPath,
		"source", source,
	)

	result := ensembleImportOutput{
		GeneratedAt: output.Timestamp(),
		Name:        preset.Name,
		Target:      importPath,
		Source:      source,
	}
	if jsonOutput {
		return output.WriteJSON(w, result, true)
	}
	_, _ = fmt.Fprintf(w, "Imported %s into %s\n", preset.Name, importPath)
	return nil
}

func readEnsembleImportSource(input string, opts ensembleImportOptions) ([]byte, string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, "", fmt.Errorf("input path or URL is required")
	}

	parsed, err := url.Parse(trimmed)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		if !opts.AllowRemote {
			return nil, "", fmt.Errorf("remote imports are disabled (use --allow-remote)")
		}
		expected := normalizeSHA256(opts.SHA256)
		if expected == "" {
			return nil, "", fmt.Errorf("sha256 checksum required for remote imports")
		}

		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Get(trimmed)
		if err != nil {
			return nil, "", fmt.Errorf("download: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, "", fmt.Errorf("download failed: %s", resp.Status)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024*1024))
		if err != nil {
			return nil, "", fmt.Errorf("read remote body: %w", err)
		}
		if err := resp.Body.Close(); err != nil {
			return nil, "", fmt.Errorf("close remote body: %w", err)
		}
		if !checkSHA256(data, expected) {
			return nil, "", fmt.Errorf("sha256 checksum mismatch")
		}
		return data, "remote", nil
	}

	data, err := os.ReadFile(trimmed)
	if err != nil {
		return nil, "", fmt.Errorf("read file: %w", err)
	}
	if checksum := normalizeSHA256(opts.SHA256); checksum != "" {
		if !checkSHA256(data, checksum) {
			return nil, "", fmt.Errorf("sha256 checksum mismatch")
		}
	}
	return data, "local", nil
}

func normalizeSHA256(raw string) string {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	trimmed = strings.TrimPrefix(trimmed, "sha256:")
	return trimmed
}

func checkSHA256(data []byte, expected string) bool {
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	return strings.EqualFold(actual, expected)
}

func upsertPresetByName(presets []ensemble.EnsemblePreset, preset ensemble.EnsemblePreset) []ensemble.EnsemblePreset {
	for i := range presets {
		if presets[i].Name == preset.Name {
			presets[i] = preset
			return presets
		}
	}
	return append(presets, preset)
}

func detectPresetConflict(presets []ensemble.EnsemblePreset, incoming ensemble.EnsemblePreset) (bool, error) {
	for _, existing := range presets {
		if existing.Name != incoming.Name {
			continue
		}
		left, err := presetFingerprint(existing)
		if err != nil {
			return false, err
		}
		right, err := presetFingerprint(incoming)
		if err != nil {
			return false, err
		}
		return left != right, nil
	}
	return false, nil
}

func presetFingerprint(p ensemble.EnsemblePreset) (string, error) {
	clone := p
	clone.Source = ""
	data, err := json.Marshal(clone)
	if err != nil {
		return "", fmt.Errorf("marshal preset: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
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
func renderYAML(w io.Writer, v any) error {
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

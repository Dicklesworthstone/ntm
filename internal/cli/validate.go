package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// Canonical validation wire types live in internal/robot (ADR ORI).
type ValidationResult = robot.ValidationResult
type ValidationIssue = robot.ValidationIssue
type ValidationSummary = robot.ValidationSummary
type ConfigLocation = robot.ConfigLocation

// ValidationReport is the complete CLI validation output envelope.
type ValidationReport struct {
	Success bool               `json:"success"`
	Valid   bool               `json:"valid"`
	Error   string             `json:"error,omitempty"`
	Results []ValidationResult `json:"results"`
	Summary ValidationSummary  `json:"summary"`
}

func newConfigValidateCmd() *cobra.Command {
	var all bool
	var fix bool

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate configuration files",
		Long: `Validate NTM configuration files for errors and inconsistencies.

Checks:
  - Main config (selected via --config, or ~/.config/ntm/config.toml by default)
  - Project config (.ntm/config.toml)
  - Recipes files (user and project)
  - Personas files (user and project)
  - Policy file (.ntm/policy.yaml)

Validation types:
  - Schema: Required fields, valid types, value ranges
  - References: File paths exist, directories accessible
  - Consistency: Cross-field dependencies, logical constraints
  - Executables: Agent commands are valid across all 12 harnesses

Examples:
  ntm config validate           # Validate applicable configs
  ntm config validate --all     # Check all config locations
  ntm config validate --fix     # Auto-fix fixable issues
  ntm config validate --json    # Output as JSON`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runValidation(all, fix)
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "check all config locations")
	cmd.Flags().BoolVar(&fix, "fix", false, "auto-fix fixable issues")

	return cmd
}

// discoverConfigs finds all config files to validate.
func discoverConfigs(all bool) []ConfigLocation {
	var projectDir string
	if cwd, err := os.Getwd(); err == nil && dirExists(cwd) {
		projectDir = cwd
	}
	return robot.DiscoverConfigs(robot.ValidationOptions{
		MainConfigPath: selectedConfigPath(),
		ProjectDir:     projectDir,
		All:            all,
	})
}

// runValidation executes the validation process.
func runValidation(all, fix bool) error {
	locations := discoverConfigs(all)

	report := ValidationReport{
		Success: true,
		Valid:   true,
		Results: make([]ValidationResult, 0, len(locations)),
	}

	for _, loc := range locations {
		result := validateConfigFile(loc, fix)
		report.Results = append(report.Results, result)

		if !result.Valid {
			report.Success = false
			report.Valid = false
		}
		report.Summary.FilesChecked++
		report.Summary.ErrorCount += len(result.Errors)
		report.Summary.WarningCount += len(result.Warnings)
		for _, w := range result.Warnings {
			if w.Fixable {
				report.Summary.FixableCount++
			}
		}
	}

	if IsJSONOutput() {
		if !report.Valid {
			cause := fmt.Errorf("validation failed with %d errors", report.Summary.ErrorCount)
			report.Error = cause.Error()
			return emitJSONFailureEnvelopeWithCause(report, cause)
		}
		return output.PrintJSON(report)
	}

	return printValidationReport(report)
}

// validateConfigFile validates a single config file.
func validateConfigFile(loc ConfigLocation, fix bool) ValidationResult {
	result := ValidationResult{
		Path:   loc.Path,
		Type:   loc.Type,
		Valid:  true,
		Errors: []ValidationIssue{},
	}

	if !loc.Exists {
		result.Info = append(result.Info, "file does not exist")
		return result
	}

	switch loc.Type {
	case "main":
		if cfg, ok := robot.LoadMainConfigForValidation(loc.Path, &result); ok {
			configDir := ""
			if strings.TrimSpace(loc.Path) != "" {
				configDir = filepath.Dir(loc.Path)
			}
			validateMainConfigReferences(cfg, &result, fix, configDir)
		}
	case "project":
		validateProjectConfig(loc.Path, &result, fix)
	case "recipes":
		validateRecipesFile(loc.Path, &result)
	case "personas":
		validatePersonasFile(loc.Path, &result)
	case "policy":
		validatePolicyFile(loc.Path, &result)
	}

	result.Valid = len(result.Errors) == 0
	return result
}

func validateMainConfigReferences(cfg *config.Config, result *ValidationResult, fix bool, configDirs ...string) {
	robot.ValidateMainConfigReferencesWithoutAgents(cfg, result, fix)
	validateAgentExecutables(cfg, result, configDirs...)
}

func validateAgentExecutables(cfg *config.Config, result *ValidationResult, configDirs ...string) {
	robot.ValidateAgentExecutables(cfg, result, configDirs...)
}

func validateProjectConfig(path string, result *ValidationResult, fix bool) {
	robot.ValidateProjectConfig(path, result, fix)
}

func validateRecipesFile(path string, result *ValidationResult) {
	robot.ValidateRecipesFile(path, result)
}

func validatePersonasFile(path string, result *ValidationResult) {
	robot.ValidatePersonasFile(path, result)
}

func validatePolicyFile(path string, result *ValidationResult) {
	robot.ValidatePolicyFile(path, result)
}

func dirExists(path string) bool {
	return robot.DirExists(path)
}

// printValidationReport outputs the report in human-readable format.
func printValidationReport(report ValidationReport) error {
	for _, r := range report.Results {
		if !r.Valid || len(r.Warnings) > 0 || len(r.Info) > 0 {
			fmt.Printf("\n%s (%s)\n", r.Path, r.Type)

			for _, e := range r.Errors {
				if e.Field != "" {
					fmt.Printf("  ✗ %s: %s\n", e.Field, e.Message)
				} else {
					fmt.Printf("  ✗ %s\n", e.Message)
				}
			}

			for _, w := range r.Warnings {
				suffix := ""
				if w.Fixable {
					suffix = " (--fix)"
				}
				if w.Field != "" {
					fmt.Printf("  ⚠ %s: %s%s\n", w.Field, w.Message, suffix)
				} else {
					fmt.Printf("  ⚠ %s%s\n", w.Message, suffix)
				}
			}

			for _, i := range r.Info {
				fmt.Printf("  ℹ %s\n", i)
			}
		}
	}

	fmt.Println()
	if report.Valid {
		fmt.Printf("✓ Validation passed (%d files checked)\n", report.Summary.FilesChecked)
	} else {
		fmt.Printf("✗ Validation failed: %d errors, %d warnings\n",
			report.Summary.ErrorCount, report.Summary.WarningCount)
	}

	if report.Summary.FixableCount > 0 {
		fmt.Printf("  %d issues can be auto-fixed with --fix\n", report.Summary.FixableCount)
	}

	if !report.Valid {
		return fmt.Errorf("validation failed with %d errors", report.Summary.ErrorCount)
	}
	return nil
}

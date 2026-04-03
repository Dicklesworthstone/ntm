package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/policy"
	"github.com/Dicklesworthstone/ntm/internal/recipe"
)

// ValidationResult represents the outcome of validating a config file or section.
type ValidationResult struct {
	Path     string            `json:"path"`
	Type     string            `json:"type"` // "main", "project", "recipes", "personas", "policy"
	Valid    bool              `json:"valid"`
	Errors   []ValidationIssue `json:"errors,omitempty"`
	Warnings []ValidationIssue `json:"warnings,omitempty"`
	Info     []string          `json:"info,omitempty"`
}

// ValidationIssue represents a single validation error or warning.
type ValidationIssue struct {
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
	Fixable bool   `json:"fixable,omitempty"`
}

// ValidationReport is the complete validation output.
type ValidationReport struct {
	Valid   bool               `json:"valid"`
	Results []ValidationResult `json:"results"`
	Summary ValidationSummary  `json:"summary"`
}

// ValidationSummary provides counts of issues found.
type ValidationSummary struct {
	FilesChecked int `json:"files_checked"`
	ErrorCount   int `json:"error_count"`
	WarningCount int `json:"warning_count"`
	FixableCount int `json:"fixable_count"`
}

// ConfigLocation represents a discovered config file.
type ConfigLocation struct {
	Path   string
	Type   string
	Exists bool
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
  - Executables: Agent commands are valid

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
	var locations []ConfigLocation

	// Main user config
	mainPath := selectedConfigPath()
	locations = append(locations, ConfigLocation{
		Path:   mainPath,
		Type:   "main",
		Exists: fileExists(mainPath),
	})

	// User config directory files
	userConfigDir := filepath.Dir(mainPath)
	userFiles := []struct {
		name  string
		ctype string
	}{
		{"recipes.toml", "recipes"},
		{"personas.toml", "personas"},
	}
	for _, f := range userFiles {
		path := filepath.Join(userConfigDir, f.name)
		exists := fileExists(path)
		if all || exists {
			locations = append(locations, ConfigLocation{
				Path:   path,
				Type:   f.ctype,
				Exists: exists,
			})
		}
	}

	// Project config (.ntm/)
	cwd, err := os.Getwd()
	if err == nil {
		projectDir, _, _ := config.FindProjectConfig(cwd)
		if projectDir != "" {
			ntmDir := filepath.Join(projectDir, ".ntm")
			projectFiles := []struct {
				name  string
				ctype string
			}{
				{"config.toml", "project"},
				{"recipes.toml", "recipes"},
				{"personas.toml", "personas"},
				{"policy.yaml", "policy"},
			}
			for _, f := range projectFiles {
				path := filepath.Join(ntmDir, f.name)
				exists := fileExists(path)
				if all || exists {
					locations = append(locations, ConfigLocation{
						Path:   path,
						Type:   f.ctype,
						Exists: exists,
					})
				}
			}
		} else if all {
			// Report that no project config exists
			ntmDir := filepath.Join(cwd, ".ntm")
			locations = append(locations, ConfigLocation{
				Path:   filepath.Join(ntmDir, "config.toml"),
				Type:   "project",
				Exists: false,
			})
		}
	}

	return locations
}

// runValidation executes the validation process.
func runValidation(all, fix bool) error {
	locations := discoverConfigs(all)

	report := ValidationReport{
		Valid:   true,
		Results: make([]ValidationResult, 0, len(locations)),
	}

	for _, loc := range locations {
		result := validateConfigFile(loc, fix)
		report.Results = append(report.Results, result)

		if !result.Valid {
			report.Valid = false
		}
		report.Summary.FilesChecked++
		report.Summary.ErrorCount += len(result.Errors)
		report.Summary.WarningCount += len(result.Warnings)
		// Count fixable warnings (Fixable is only set on warnings, not errors)
		for _, w := range result.Warnings {
			if w.Fixable {
				report.Summary.FixableCount++
			}
		}
	}

	// Output results
	if IsJSONOutput() {
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
		validateMainConfig(loc.Path, &result, fix)
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

// validateMainConfig validates the main config.toml file.
func validateMainConfig(path string, result *ValidationResult, fix bool) {
	cfg, err := config.Load(path)
	if err != nil {
		result.Errors = append(result.Errors, ValidationIssue{
			Message: fmt.Sprintf("failed to load: %v", err),
		})
		return
	}

	// Use existing Validate function
	errs := config.Validate(cfg)
	for _, e := range errs {
		result.Errors = append(result.Errors, ValidationIssue{
			Message: e.Error(),
		})
	}

	// Additional reference validation
	validateMainConfigReferences(cfg, result, fix)
}

// validateMainConfigReferences checks that referenced files/dirs exist.
func validateMainConfigReferences(cfg *config.Config, result *ValidationResult, fix bool) {
	// Check projects_base exists
	if cfg.ProjectsBase != "" {
		expanded := config.ExpandHome(cfg.ProjectsBase)
		if !dirExists(expanded) {
			result.Warnings = append(result.Warnings, ValidationIssue{
				Field:   "projects_base",
				Message: fmt.Sprintf("directory does not exist: %s", expanded),
				Fixable: true,
			})
			if fix {
				if err := os.MkdirAll(expanded, 0755); err == nil {
					result.Info = append(result.Info, fmt.Sprintf("created projects_base: %s", expanded))
				}
			}
		}
	}

	// Check palette file if specified
	if cfg.PaletteFile != "" {
		expanded := config.ExpandHome(cfg.PaletteFile)
		if !regularFileExists(expanded) {
			result.Warnings = append(result.Warnings, ValidationIssue{
				Field:   "palette_file",
				Message: fmt.Sprintf("file does not exist: %s", expanded),
			})
		}
	}

	validateRegularFileReference("send.base_prompt_file", cfg.Send.BasePromptFile, result)
	validateRegularFileReference("prompts.cc_default_file", cfg.Prompts.CCDefaultFile, result)
	validateRegularFileReference("prompts.cod_default_file", cfg.Prompts.CodDefaultFile, result)
	validateRegularFileReference("prompts.gmi_default_file", cfg.Prompts.GmiDefaultFile, result)
	if cfg.Encryption.Enabled && strings.EqualFold(strings.TrimSpace(cfg.Encryption.KeySource), "file") {
		validateRegularFileReference("encryption.key_file", cfg.Encryption.KeyFile, result)
	}

	validateBinaryReference("integrations.dcg.binary_path", cfg.Integrations.DCG.BinaryPath, result)
	validateBinaryReference("integrations.caam.binary_path", cfg.Integrations.CAAM.BinaryPath, result)
	validateBinaryReference("integrations.rch.binary_path", cfg.Integrations.RCH.BinaryPath, result)
	validateBinaryReference("integrations.caut.binary_path", cfg.Integrations.Caut.BinaryPath, result)
	validateBinaryReference("integrations.process_triage.binary_path", cfg.Integrations.ProcessTriage.BinaryPath, result)
	validateBinaryReference("integrations.rano.binary_path", cfg.Integrations.Rano.BinaryPath, result)
	validateBinaryReference("cass.binary_path", cfg.CASS.BinaryPath, result)
	validateBinaryReference("scanner.ubs_path", cfg.Scanner.UBSPath, result)

	// Check agent executables
	validateAgentExecutables(cfg, result)

	// Check DCG integration availability when enabled
	if cfg.Integrations.DCG.Enabled && cfg.Integrations.DCG.BinaryPath == "" {
		if _, err := exec.LookPath("dcg"); err != nil {
			result.Warnings = append(result.Warnings, ValidationIssue{
				Field:   "integrations.dcg.binary_path",
				Message: "dcg binary not found on PATH (set integrations.dcg.binary_path or install dcg)",
			})
		}
	}
}

// validateAgentExecutables checks that agent commands are valid.
func validateAgentExecutables(cfg *config.Config, result *ValidationResult) {
	agents := map[string]string{
		"agents.claude": cfg.Agents.Claude,
		"agents.codex":  cfg.Agents.Codex,
		"agents.gemini": cfg.Agents.Gemini,
	}

	for field, cmd := range agents {
		if cmd == "" {
			continue
		}
		// Extract the executable from the command, skipping env var assignments
		parts := strings.Fields(cmd)
		exe := ""
		for _, part := range parts {
			// Skip environment variable assignments (e.g., NODE_OPTIONS="...")
			if strings.Contains(part, "=") {
				continue
			}
			exe = part
			break
		}
		if exe == "" {
			continue
		}

		// Check if executable exists
		_, err := exec.LookPath(exe)
		if err != nil {
			result.Warnings = append(result.Warnings, ValidationIssue{
				Field:   field,
				Message: fmt.Sprintf("executable not found in PATH: %s", exe),
			})
		}
	}
}

// validateProjectConfig validates .ntm/config.toml.
func validateProjectConfig(path string, result *ValidationResult, fix bool) {
	cfg, err := config.LoadProjectConfig(path)
	if err != nil {
		result.Errors = append(result.Errors, ValidationIssue{
			Message: fmt.Sprintf("failed to load: %v", err),
		})
		return
	}

	ntmDir := filepath.Dir(path)
	projectDir := filepath.Dir(ntmDir)

	// Check palette file reference
	if cfg.Palette.File != "" {
		palettePath, err := config.ResolveProjectPalettePath(projectDir, cfg)
		if err != nil {
			result.Warnings = append(result.Warnings, ValidationIssue{
				Field:   "palette.file",
				Message: err.Error(),
			})
		} else if !regularFileExists(palettePath) {
			result.Warnings = append(result.Warnings, ValidationIssue{
				Field:   "palette.file",
				Message: fmt.Sprintf("file does not exist: %s", palettePath),
			})
		}
	}

	// Check templates directory
	if cfg.Templates.Dir != "" {
		templatesPath, err := config.ResolveProjectTemplatesDir(projectDir, cfg)
		if err != nil {
			result.Warnings = append(result.Warnings, ValidationIssue{
				Field:   "templates.dir",
				Message: err.Error(),
			})
			return
		}
		if !dirExists(templatesPath) {
			result.Warnings = append(result.Warnings, ValidationIssue{
				Field:   "templates.dir",
				Message: fmt.Sprintf("directory does not exist: %s", templatesPath),
				Fixable: true,
			})
			if fix {
				if err := os.MkdirAll(templatesPath, 0755); err == nil {
					result.Info = append(result.Info, fmt.Sprintf("created templates dir: %s", templatesPath))
				}
			}
		}
	}
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func validateRegularFileReference(field, path string, result *ValidationResult) {
	if strings.TrimSpace(path) == "" {
		return
	}
	expanded := config.ExpandHome(path)
	if regularFileExists(expanded) {
		return
	}
	result.Warnings = append(result.Warnings, ValidationIssue{
		Field:   field,
		Message: fmt.Sprintf("file does not exist: %s", expanded),
	})
}

func validateBinaryReference(field, binaryPath string, result *ValidationResult) {
	if strings.TrimSpace(binaryPath) == "" {
		return
	}

	expanded := config.ExpandHome(binaryPath)
	if filepath.IsAbs(expanded) || strings.HasPrefix(binaryPath, "~") || strings.ContainsRune(binaryPath, filepath.Separator) {
		if regularFileExists(expanded) {
			return
		}
		result.Warnings = append(result.Warnings, ValidationIssue{
			Field:   field,
			Message: fmt.Sprintf("binary does not exist: %s", expanded),
		})
		return
	}

	if _, err := exec.LookPath(binaryPath); err != nil {
		result.Warnings = append(result.Warnings, ValidationIssue{
			Field:   field,
			Message: fmt.Sprintf("binary not found on PATH: %s", binaryPath),
		})
	}
}

// validateRecipesFile validates a recipes.toml file.
func validateRecipesFile(path string, result *ValidationResult) {
	data, err := os.ReadFile(path)
	if err != nil {
		result.Errors = append(result.Errors, ValidationIssue{
			Message: fmt.Sprintf("failed to read: %v", err),
		})
		return
	}

	var rf struct {
		Recipes []recipe.Recipe `toml:"recipes"`
	}
	if err := tomlUnmarshal(data, &rf); err != nil {
		result.Errors = append(result.Errors, ValidationIssue{
			Message: fmt.Sprintf("invalid TOML syntax: %v", err),
		})
		return
	}
	if len(rf.Recipes) == 0 {
		result.Errors = append(result.Errors, ValidationIssue{
			Field:   "recipes",
			Message: "recipes.toml must contain at least one [[recipes]] entry",
		})
		return
	}

	for i, r := range rf.Recipes {
		field := fmt.Sprintf("recipes[%d]", i)
		if strings.TrimSpace(r.Name) != "" {
			field = r.Name
		}
		if strings.TrimSpace(r.Description) == "" {
			result.Warnings = append(result.Warnings, ValidationIssue{
				Field:   field,
				Message: "recipe missing description field",
			})
		}
		if err := r.Validate(); err != nil {
			result.Errors = append(result.Errors, ValidationIssue{
				Field:   field,
				Message: err.Error(),
			})
		}
	}
}

// validatePersonasFile validates a personas.toml file.
func validatePersonasFile(path string, result *ValidationResult) {
	cfg, err := persona.LoadFromFile(path)
	if err != nil {
		result.Errors = append(result.Errors, ValidationIssue{
			Message: err.Error(),
		})
		return
	}
	if len(cfg.Personas) == 0 {
		result.Errors = append(result.Errors, ValidationIssue{
			Field:   "personas",
			Message: "personas.toml must contain at least one [[personas]] entry",
		})
		return
	}
	if _, err := buildValidationPersonaRegistry(path, cfg); err != nil {
		result.Errors = append(result.Errors, ValidationIssue{
			Message: err.Error(),
		})
		return
	}

	for i := range cfg.Personas {
		p := &cfg.Personas[i]
		field := fmt.Sprintf("personas[%d]", i)
		if strings.TrimSpace(p.Name) != "" {
			field = p.Name
		}
		if strings.TrimSpace(p.SystemPrompt) == "" && strings.TrimSpace(p.Extends) == "" {
			result.Warnings = append(result.Warnings, ValidationIssue{
				Field:   field,
				Message: "persona missing system_prompt field",
			})
		}
	}
}

func buildValidationPersonaRegistry(path string, primary *persona.PersonasConfig) (*persona.Registry, error) {
	registry := persona.NewRegistry()
	for _, p := range persona.BuiltinPersonas() {
		registry.Add(&p)
	}
	for _, s := range persona.BuiltinPersonaSets() {
		registry.AddSet(&s)
	}

	if isProjectPersonasPath(path) {
		userCfg, userPath, err := persona.LoadUserConfig()
		if err != nil {
			return nil, err
		}
		if userCfg != nil && filepath.Clean(userPath) != filepath.Clean(path) {
			for i := range userCfg.Personas {
				registry.Add(&userCfg.Personas[i])
			}
			for i := range userCfg.PersonaSets {
				registry.AddSet(&userCfg.PersonaSets[i])
			}
		}
	}

	for i := range primary.Personas {
		registry.Add(&primary.Personas[i])
	}
	for i := range primary.PersonaSets {
		registry.AddSet(&primary.PersonaSets[i])
	}

	if err := registry.ResolveInheritance(); err != nil {
		return nil, fmt.Errorf("resolving persona inheritance: %w", err)
	}
	if err := registry.ValidatePersonaSets(); err != nil {
		return nil, err
	}
	return registry, nil
}

func isProjectPersonasPath(path string) bool {
	return filepath.Base(path) == "personas.toml" && filepath.Base(filepath.Dir(path)) == ".ntm"
}

// validatePolicyFile validates .ntm/policy.yaml.
func validatePolicyFile(path string, result *ValidationResult) {
	data, err := os.ReadFile(path)
	if err != nil {
		result.Errors = append(result.Errors, ValidationIssue{
			Message: fmt.Sprintf("failed to read: %v", err),
		})
		return
	}

	p, err := policy.DecodeYAML(data)
	if err != nil {
		result.Errors = append(result.Errors, ValidationIssue{
			Message: fmt.Sprintf("invalid YAML syntax: %v", err),
		})
		return
	}

	if p.Version == 0 {
		result.Warnings = append(result.Warnings, ValidationIssue{
			Field:   "version",
			Message: "no version specified, defaulting to 1",
		})
	}

	if err := p.Validate(); err != nil {
		result.Errors = append(result.Errors, ValidationIssue{
			Message: err.Error(),
		})
		return
	}

	blocked, approval, allowed := p.Stats()
	if blocked == 0 && approval == 0 && allowed == 0 {
		result.Warnings = append(result.Warnings, ValidationIssue{
			Message: "policy has no rules defined",
		})
	}
}

// printValidationReport outputs the report in human-readable format.
func printValidationReport(report ValidationReport) error {
	for _, r := range report.Results {
		if !r.Valid || len(r.Warnings) > 0 || len(r.Info) > 0 {
			fmt.Printf("\n%s (%s)\n", r.Path, r.Type)

			for _, e := range r.Errors {
				prefix := "✗"
				if e.Field != "" {
					fmt.Printf("  %s %s: %s\n", prefix, e.Field, e.Message)
				} else {
					fmt.Printf("  %s %s\n", prefix, e.Message)
				}
			}

			for _, w := range r.Warnings {
				prefix := "⚠"
				suffix := ""
				if w.Fixable {
					suffix = " (--fix)"
				}
				if w.Field != "" {
					fmt.Printf("  %s %s: %s%s\n", prefix, w.Field, w.Message, suffix)
				} else {
					fmt.Printf("  %s %s%s\n", prefix, w.Message, suffix)
				}
			}

			for _, i := range r.Info {
				fmt.Printf("  ℹ %s\n", i)
			}
		}
	}

	// Print summary
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

func undecodedTOMLFields(md toml.MetaData) []string {
	keys := md.Undecoded()
	if len(keys) == 0 {
		return nil
	}
	fields := make([]string, 0, len(keys))
	for _, key := range keys {
		fields = append(fields, key.String())
	}
	sort.Strings(fields)
	return fields
}

// tomlUnmarshal wraps TOML unmarshaling.
func tomlUnmarshal(data []byte, v interface{}) error {
	md, err := toml.Decode(string(data), v)
	if err != nil {
		return err
	}
	if fields := undecodedTOMLFields(md); len(fields) > 0 {
		return fmt.Errorf("unknown field(s): %s", strings.Join(fields, ", "))
	}
	return nil
}

// dirExists checks if a directory exists.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

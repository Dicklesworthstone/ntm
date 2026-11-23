package robot

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/internal/policy"
	"github.com/Dicklesworthstone/ntm/internal/recipe"
)

const (
	validateSchemaID      = "ntm:robot:validate:v1"
	validateSchemaVersion = "1.0.0"

	ValidateSchemaID      = validateSchemaID
	ValidateSchemaVersion = validateSchemaVersion
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

// ValidationSummary provides counts of issues found.
type ValidationSummary struct {
	FilesChecked int `json:"files_checked"`
	ErrorCount   int `json:"error_count"`
	WarningCount int `json:"warning_count"`
	FixableCount int `json:"fixable_count"`
}

// ConfigLocation represents a discovered config file.
type ConfigLocation struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Exists bool   `json:"exists"`
}

// ValidationOptions configures GetValidation.
type ValidationOptions struct {
	MainConfigPath string
	ProjectDir     string
	All            bool
	Fix            bool
}

// ValidationOutput is the canonical robot envelope for --robot-validate and /api/v1/config/validate.
type ValidationOutput struct {
	RobotResponse
	SchemaID      string             `json:"schema_id"`
	SchemaVersion string             `json:"schema_version"`
	Valid         bool               `json:"valid"`
	All           bool               `json:"all,omitempty"`
	FixApplied    bool               `json:"fix_applied,omitempty"`
	Results       []ValidationResult `json:"results"`
	Summary       ValidationSummary  `json:"summary"`
}

// PathExists reports whether path exists on disk (file or directory).
func PathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// DirExists reports whether path exists and is a directory.
func DirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// RegularFileExists reports whether path exists and is a non-directory file.
func RegularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// DiscoverConfigs finds all main, user, and project config files to validate.
func DiscoverConfigs(opts ValidationOptions) []ConfigLocation {
	mainPath := strings.TrimSpace(opts.MainConfigPath)
	if mainPath == "" {
		if env := strings.TrimSpace(os.Getenv("NTM_CONFIG")); env != "" {
			mainPath = config.ExpandHome(env)
		} else {
			mainPath = config.DefaultPath()
		}
	}

	var locations []ConfigLocation
	locations = append(locations, ConfigLocation{
		Path:   mainPath,
		Type:   "main",
		Exists: PathExists(mainPath),
	})

	userConfigDir := filepath.Dir(mainPath)
	for _, f := range []struct {
		name  string
		ctype string
	}{
		{"recipes.toml", "recipes"},
		{"personas.toml", "personas"},
	} {
		p := filepath.Join(userConfigDir, f.name)
		exists := PathExists(p)
		if opts.All || exists {
			locations = append(locations, ConfigLocation{
				Path:   p,
				Type:   f.ctype,
				Exists: exists,
			})
		}
	}

	searchRoot := strings.TrimSpace(opts.ProjectDir)
	if searchRoot == "" {
		if cwd, err := os.Getwd(); err == nil {
			searchRoot = cwd
		}
	}
	if searchRoot != "" {
		projectDir, _, _ := config.FindProjectConfig(searchRoot)
		if projectDir == "" && DirExists(filepath.Join(searchRoot, ".ntm")) {
			projectDir = searchRoot
		}
		if projectDir != "" {
			ntmDir := filepath.Join(projectDir, ".ntm")
			for _, f := range []struct {
				name  string
				ctype string
			}{
				{"config.toml", "project"},
				{"recipes.toml", "recipes"},
				{"personas.toml", "personas"},
				{"policy.yaml", "policy"},
			} {
				p := filepath.Join(ntmDir, f.name)
				exists := PathExists(p)
				if opts.All || exists {
					locations = append(locations, ConfigLocation{
						Path:   p,
						Type:   f.ctype,
						Exists: exists,
					})
				}
			}
		} else if opts.All {
			ntmDir := filepath.Join(searchRoot, ".ntm")
			locations = append(locations, ConfigLocation{
				Path:   filepath.Join(ntmDir, "config.toml"),
				Type:   "project",
				Exists: false,
			})
		}
	}

	return locations
}

// GetValidation validates user and project NTM configuration, recipes, personas, and policy files.
func GetValidation(opts ValidationOptions) (*ValidationOutput, error) {
	locations := DiscoverConfigs(opts)
	out := &ValidationOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      validateSchemaID,
		SchemaVersion: validateSchemaVersion,
		Valid:         true,
		All:           opts.All,
		FixApplied:    opts.Fix,
		Results:       make([]ValidationResult, 0, len(locations)),
	}

	for _, loc := range locations {
		res := ValidateConfigFile(loc, opts.Fix)
		out.Results = append(out.Results, res)
		if !res.Valid {
			out.Valid = false
		}
		out.Summary.FilesChecked++
		out.Summary.ErrorCount += len(res.Errors)
		out.Summary.WarningCount += len(res.Warnings)
		for _, w := range res.Warnings {
			if w.Fixable {
				out.Summary.FixableCount++
			}
		}
	}

	if !out.Valid {
		out.RobotResponse = NewErrorResponse(
			fmt.Errorf("validation failed with %d errors", out.Summary.ErrorCount),
			"VALIDATION_FAILED",
			"Inspect results[].errors and fix invalid TOML/YAML fields or references",
		)
	}
	return out, nil
}

// ValidateConfigFile validates a single discovered configuration location.
func ValidateConfigFile(loc ConfigLocation, fix bool) ValidationResult {
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
		ValidateMainConfig(loc.Path, &result, fix)
	case "project":
		ValidateProjectConfig(loc.Path, &result, fix)
	case "recipes":
		ValidateRecipesFile(loc.Path, &result)
	case "personas":
		ValidatePersonasFile(loc.Path, &result)
	case "policy":
		ValidatePolicyFile(loc.Path, &result)
	}
	result.Valid = len(result.Errors) == 0
	return result
}

// LoadMainConfigForValidation loads path and records schema-level config errors into result.
func LoadMainConfigForValidation(path string, result *ValidationResult) (*config.Config, bool) {
	cfg, err := config.Load(path)
	if err != nil {
		result.Errors = append(result.Errors, ValidationIssue{
			Message: fmt.Sprintf("failed to load: %v", err),
		})
		return nil, false
	}
	for _, e := range config.Validate(cfg) {
		result.Errors = append(result.Errors, ValidationIssue{
			Message: e.Error(),
		})
	}
	return cfg, true
}

// ValidateMainConfig validates a main config.toml file.
func ValidateMainConfig(path string, result *ValidationResult, fix bool) {
	cfg, ok := LoadMainConfigForValidation(path, result)
	if !ok {
		return
	}
	ValidateMainConfigReferencesWithoutAgents(cfg, result, fix)
	configDir := ""
	if strings.TrimSpace(path) != "" {
		configDir = filepath.Dir(path)
	}
	ValidateAgentExecutables(cfg, result, configDir)
}

// ValidateMainConfigReferencesWithoutAgents validates non-agent file/directory/binary references in cfg.
func ValidateMainConfigReferencesWithoutAgents(cfg *config.Config, result *ValidationResult, fix bool) {
	if cfg.ProjectsBase != "" {
		expanded := config.ExpandHome(cfg.ProjectsBase)
		if !DirExists(expanded) {
			result.Warnings = append(result.Warnings, ValidationIssue{
				Field:   "projects_base",
				Message: fmt.Sprintf("directory does not exist: %s", expanded),
				Fixable: true,
			})
			if fix {
				if err := os.MkdirAll(expanded, 0o755); err == nil {
					result.Info = append(result.Info, fmt.Sprintf("created projects_base: %s", expanded))
				}
			}
		}
	}

	if cfg.PaletteFile != "" {
		expanded := config.ExpandHome(cfg.PaletteFile)
		if !RegularFileExists(expanded) {
			result.Warnings = append(result.Warnings, ValidationIssue{
				Field:   "palette_file",
				Message: fmt.Sprintf("file does not exist: %s", expanded),
			})
		}
	}

	validateRegularFileRef("send.base_prompt_file", cfg.Send.BasePromptFile, result)
	validateRegularFileRef("prompts.cc_default_file", cfg.Prompts.CCDefaultFile, result)
	validateRegularFileRef("prompts.cod_default_file", cfg.Prompts.CodDefaultFile, result)
	validateRegularFileRef("prompts.gmi_default_file", cfg.Prompts.GmiDefaultFile, result)
	validateRegularFileRef("prompts.agy_default_file", cfg.Prompts.AgyDefaultFile, result)
	validateRegularFileRef("prompts.omp_default_file", cfg.Prompts.OmpDefaultFile, result)
	validateRegularFileRef("prompts.grok_default_file", cfg.Prompts.GrokDefaultFile, result)
	validateRegularFileRef("prompts.oc_default_file", cfg.Prompts.OcDefaultFile, result)
	if cfg.Encryption.Enabled && strings.EqualFold(strings.TrimSpace(cfg.Encryption.KeySource), "file") {
		validateRegularFileRef("encryption.key_file", cfg.Encryption.KeyFile, result)
	}

	validateBinaryRef("integrations.dcg.binary_path", cfg.Integrations.DCG.BinaryPath, result)
	validateBinaryRef("integrations.caam.binary_path", cfg.Integrations.CAAM.BinaryPath, result)
	validateBinaryRef("integrations.rch.binary_path", cfg.Integrations.RCH.BinaryPath, result)
	validateBinaryRef("cass.binary_path", cfg.CASS.BinaryPath, result)
	validateBinaryRef("scanner.ubs_path", cfg.Scanner.UBSPath, result)

	if cfg.Integrations.DCG.Enabled && cfg.Integrations.DCG.BinaryPath == "" {
		if _, err := exec.LookPath("dcg"); err != nil {
			result.Warnings = append(result.Warnings, ValidationIssue{
				Field:   "integrations.dcg.binary_path",
				Message: "dcg binary not found on PATH (set integrations.dcg.binary_path or install dcg)",
			})
		}
	}
}

// ValidateAgentExecutables checks configured agent launch templates across all 12 harnesses.
// When a non-empty configDir is supplied, companion agent plugins are loaded from
// <configDir>/agents rather than the default config directory.
func ValidateAgentExecutables(cfg *config.Config, result *ValidationResult, configDirs ...string) {
	configDir := ""
	if len(configDirs) > 0 {
		configDir = strings.TrimSpace(configDirs[0])
	}
	if configDir == "" {
		configDir = filepath.Dir(config.DefaultPath())
	}
	piCmd := plugins.ShippedPiPlugin().Command
	if loadedPlugins, err := plugins.LoadAgentPluginsWithDefaults(filepath.Join(configDir, "agents")); err == nil {
		for _, p := range loadedPlugins {
			if (strings.EqualFold(strings.TrimSpace(p.Name), "pi") || strings.EqualFold(strings.TrimSpace(p.Alias), "pi")) && strings.TrimSpace(p.Command) != "" {
				piCmd = p.Command
				break
			}
		}
	}

	specs := []struct {
		field string
		cmd   string
	}{
		{"agents.aider", cfg.Agents.Aider},
		{"agents.antigravity", cfg.Agents.Antigravity},
		{"agents.claude", cfg.Agents.Claude},
		{"agents.codex", cfg.Agents.Codex},
		{"agents.cursor", cfg.Agents.Cursor},
		{"agents.gemini", cfg.Agents.Gemini},
		{"agents.grok", cfg.Agents.Grok},
		{"agents.oc", cfg.Agents.Opencode},
		{"agents.ollama", cfg.Agents.Ollama},
		{"agents.omp", config.OmpCommandOrDefault(cfg.Agents.Omp)},
		{"agents.pi", piCmd},
		{"agents.windsurf", cfg.Agents.Windsurf},
	}

	for _, spec := range specs {
		cmd := strings.TrimSpace(spec.cmd)
		if cmd == "" {
			continue
		}
		rendered := cmd
		if config.IsTemplateCommand(cmd) {
			out, err := config.GenerateAgentCommand(cmd, config.AgentTemplateVars{})
			if err != nil {
				result.Warnings = append(result.Warnings, ValidationIssue{
					Field:   spec.field,
					Message: fmt.Sprintf("agent command template failed to render: %v", err),
				})
				continue
			}
			rendered = out
		}

		exe := ""
		for _, part := range strings.Fields(rendered) {
			if strings.Contains(part, "=") {
				continue
			}
			exe = part
			break
		}
		if exe == "" {
			continue
		}
		if _, err := exec.LookPath(exe); err != nil {
			result.Warnings = append(result.Warnings, ValidationIssue{
				Field:   spec.field,
				Message: fmt.Sprintf("executable not found in PATH: %s", exe),
			})
		}
	}
}

// ValidateProjectConfig validates .ntm/config.toml.
func ValidateProjectConfig(path string, result *ValidationResult, fix bool) {
	cfg, err := config.LoadProjectConfig(path)
	if err != nil {
		result.Errors = append(result.Errors, ValidationIssue{
			Message: fmt.Sprintf("failed to load: %v", err),
		})
		return
	}

	ntmDir := filepath.Dir(path)
	projectDir := filepath.Dir(ntmDir)

	if cfg.Palette.File != "" {
		palettePath, err := config.ResolveProjectPalettePath(projectDir, cfg)
		if err != nil {
			result.Warnings = append(result.Warnings, ValidationIssue{
				Field:   "palette.file",
				Message: err.Error(),
			})
		} else if !RegularFileExists(palettePath) {
			result.Warnings = append(result.Warnings, ValidationIssue{
				Field:   "palette.file",
				Message: fmt.Sprintf("file does not exist: %s", palettePath),
			})
		}
	}

	if cfg.Templates.Dir != "" {
		templatesPath, err := config.ResolveProjectTemplatesDir(projectDir, cfg)
		if err != nil {
			result.Warnings = append(result.Warnings, ValidationIssue{
				Field:   "templates.dir",
				Message: err.Error(),
			})
			return
		}
		if !DirExists(templatesPath) {
			result.Warnings = append(result.Warnings, ValidationIssue{
				Field:   "templates.dir",
				Message: fmt.Sprintf("directory does not exist: %s", templatesPath),
				Fixable: true,
			})
			if fix {
				if err := os.MkdirAll(templatesPath, 0o755); err == nil {
					result.Info = append(result.Info, fmt.Sprintf("created templates dir: %s", templatesPath))
				}
			}
		}
	}
}

func validateRegularFileRef(field, path string, result *ValidationResult) {
	if strings.TrimSpace(path) == "" {
		return
	}
	expanded := config.ExpandHome(path)
	if RegularFileExists(expanded) {
		return
	}
	result.Warnings = append(result.Warnings, ValidationIssue{
		Field:   field,
		Message: fmt.Sprintf("file does not exist: %s", expanded),
	})
}

func validateBinaryRef(field, binaryPath string, result *ValidationResult) {
	if strings.TrimSpace(binaryPath) == "" {
		return
	}
	expanded := config.ExpandHome(binaryPath)
	if filepath.IsAbs(expanded) || strings.HasPrefix(binaryPath, "~") || strings.ContainsRune(binaryPath, filepath.Separator) {
		if RegularFileExists(expanded) {
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

// ValidateRecipesFile validates a recipes.toml file.
func ValidateRecipesFile(path string, result *ValidationResult) {
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
	if err := strictTOMLUnmarshal(data, &rf); err != nil {
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

// ValidatePersonasFile validates a personas.toml file including inheritance and sets.
func ValidatePersonasFile(path string, result *ValidationResult) {
	cfg, err := persona.LoadFromFile(path)
	if err != nil {
		result.Errors = append(result.Errors, ValidationIssue{Message: err.Error()})
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
		result.Errors = append(result.Errors, ValidationIssue{Message: err.Error()})
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

	if filepath.Base(path) == "personas.toml" && filepath.Base(filepath.Dir(path)) == ".ntm" {
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

// ValidatePolicyFile validates .ntm/policy.yaml.
func ValidatePolicyFile(path string, result *ValidationResult) {
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
		result.Errors = append(result.Errors, ValidationIssue{Message: err.Error()})
		return
	}

	blocked, approval, allowed := p.Stats()
	if blocked == 0 && approval == 0 && allowed == 0 {
		result.Warnings = append(result.Warnings, ValidationIssue{
			Message: "policy has no rules defined",
		})
	}
}

func strictTOMLUnmarshal(data []byte, v interface{}) error {
	md, err := toml.Decode(string(data), v)
	if err != nil {
		return err
	}
	keys := md.Undecoded()
	if len(keys) == 0 {
		return nil
	}
	fields := make([]string, 0, len(keys))
	for _, key := range keys {
		fields = append(fields, key.String())
	}
	sort.Strings(fields)
	return fmt.Errorf("unknown field(s): %s", strings.Join(fields, ", "))
}

// PrintValidation writes the terminal JSON/TOON response for --robot-validate.
func PrintValidation(opts ValidationOptions) error {
	out, err := GetValidation(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot validate failed")
}

func validateCapabilityCommand() RobotCommandInfo {
	return RobotCommandInfo{
		Name:        "validate",
		Flag:        "--robot-validate",
		Category:    "utility",
		Summary:     "Validate NTM config, recipes, personas, and policy.",
		Description: "Validate main config.toml, project .ntm/config.toml, recipes.toml, personas.toml, and policy.yaml schemas, file references, and 12-harness agent executables.",
		Parameters: []RobotParameter{
			{Name: "validate-all", Flag: "--validate-all", Type: "bool", Required: false, Default: "false", Description: "Include absent optional user and project config locations"},
			{Name: "validate-fix", Flag: "--validate-fix", Type: "bool", Required: false, Default: "false", Description: "Auto-create missing fixable directories (projects_base, templates.dir)"},
			{Name: "project", Flag: "--project", Type: "string", Required: false, Description: "Project directory override for .ntm/* discovery"},
		},
		Examples: []string{
			"ntm --robot-validate",
			"ntm --robot-validate --validate-all --project=/path/to/repo",
			"ntm --robot-validate --validate-fix",
		},
	}
}

package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"text/template"
	"time"

	"github.com/BurntSushi/toml"
)

// ProjectConfig represents the structure of .ntm/config.toml
type ProjectConfig struct {
	Project      ProjectMeta         `toml:"project"`
	Defaults     ProjectDefaults     `toml:"defaults"`
	Alerts       *ProjectAlerts      `toml:"alerts"`
	Palette      ProjectPalette      `toml:"palette"`
	PaletteState PaletteState        `toml:"palette_state"`
	Templates    ProjectTemplates    `toml:"templates"`
	Agents       AgentConfig         `toml:"agents"`
	Integrations ProjectIntegrations `toml:"integrations"`
}

// ProjectMeta holds basic project metadata.
type ProjectMeta struct {
	Name    string `toml:"name"`
	Created string `toml:"created"`
}

// ProjectIntegrations declares optional integrations for the project.
type ProjectIntegrations struct {
	AgentMail bool `toml:"agent_mail"`
	Beads     bool `toml:"beads"`
	CASS      bool `toml:"cass"`
	CM        bool `toml:"cm"`
}

// ProjectDefaults holds default settings for the project
type ProjectDefaults struct {
	Agents map[string]int `toml:"agents"` // e.g., { cc = 2, cod = 1 }
}

// ProjectAlerts holds project-scoped alert overrides. Pointer fields preserve
// the distinction between "unset" and explicit zero/false values.
type ProjectAlerts struct {
	Enabled                 *bool    `toml:"enabled"`
	AgentStuckMinutes       *int     `toml:"agent_stuck_minutes"`
	DiskLowThresholdGB      *float64 `toml:"disk_low_threshold_gb"`
	MailBacklogThreshold    *int     `toml:"mail_backlog_threshold"`
	BeadStaleHours          *int     `toml:"bead_stale_hours"`
	ContextWarningThreshold *float64 `toml:"context_warning_threshold"`
	ResolvedPruneMinutes    *int     `toml:"resolved_prune_minutes"`
}

// ProjectPalette holds palette configuration
type ProjectPalette struct {
	File string `toml:"file"` // Path to palette.md relative to .ntm/
}

// ProjectTemplates holds template configuration
type ProjectTemplates struct {
	Dir string `toml:"dir"` // Path to templates dir relative to .ntm/
}

// ProjectInitResult captures what init created.
type ProjectInitResult struct {
	ProjectDir   string
	NTMDir       string
	CreatedDirs  []string
	CreatedFiles []string
}

// FindProjectConfig searches for .ntm/config.toml starting from dir and going up.
// Returns the directory containing .ntm/ and the loaded config.
func FindProjectConfig(startDir string) (string, *ProjectConfig, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", nil, err
	}

	for {
		configPath := filepath.Join(dir, ".ntm", "config.toml")
		if info, err := os.Stat(configPath); err == nil && !info.IsDir() {
			cfg, err := LoadProjectConfig(configPath)
			return dir, cfg, err
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil, nil // Reached root, no config found
		}
		dir = parent
	}
}

// LoadProjectConfig loads a project configuration from a file
func LoadProjectConfig(path string) (*ProjectConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg ProjectConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing project config: %w", err)
	}

	return &cfg, nil
}

// InitProjectConfig initializes .ntm configuration for the current directory.
// If force is true, it overwrites .ntm/config.toml if it already exists.
func InitProjectConfig(force bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	result, err := InitProjectConfigAt(cwd, force)
	if err != nil {
		return err
	}

	fmt.Printf("Initialized project config in %s\n", result.NTMDir)
	return nil
}

// InitProjectConfigAt initializes .ntm configuration for the provided directory.
// If force is true, it overwrites .ntm/config.toml if it already exists.
func InitProjectConfigAt(projectDir string, force bool) (*ProjectInitResult, error) {
	if projectDir == "" {
		return nil, fmt.Errorf("project directory is required")
	}

	absDir, err := filepath.Abs(projectDir)
	if err != nil {
		return nil, fmt.Errorf("resolve project directory: %w", err)
	}

	info, err := os.Stat(absDir)
	if err != nil {
		return nil, fmt.Errorf("project directory not found: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("project path is not a directory: %s", absDir)
	}

	result := &ProjectInitResult{
		ProjectDir: absDir,
		NTMDir:     filepath.Join(absDir, ".ntm"),
	}

	if _, err := os.Stat(result.NTMDir); os.IsNotExist(err) {
		result.CreatedDirs = append(result.CreatedDirs, result.NTMDir)
	}
	if err := os.MkdirAll(result.NTMDir, 0755); err != nil {
		return nil, fmt.Errorf("creating .ntm directory: %w", err)
	}

	// Create config.toml (or skip if it already exists and force is false).
	configPath := filepath.Join(result.NTMDir, "config.toml")
	configExists := false
	if _, err := os.Stat(configPath); err == nil {
		if !force {
			configExists = true
		}
	}

	if !configExists {
		projectName := filepath.Base(absDir)
		created := time.Now().UTC().Format(time.RFC3339)
		content, err := renderProjectConfig(projectName, created)
		if err != nil {
			return nil, fmt.Errorf("rendering config.toml: %w", err)
		}
		if err := os.WriteFile(configPath, content, 0644); err != nil {
			return nil, fmt.Errorf("writing config.toml: %w", err)
		}
		result.CreatedFiles = append(result.CreatedFiles, configPath)
	}

	// Create palette.md scaffold
	palettePath := filepath.Join(result.NTMDir, "palette.md")
	if _, err := os.Stat(palettePath); os.IsNotExist(err) {
		paletteContent := `# Project Commands

## Project
### build | Build Project
make build

### test | Run Tests
go test ./...
`
		if err := os.WriteFile(palettePath, []byte(paletteContent), 0644); err != nil {
			return nil, fmt.Errorf("writing palette.md: %w", err)
		}
		result.CreatedFiles = append(result.CreatedFiles, palettePath)
	}

	// Create templates dir
	templatesDir := filepath.Join(result.NTMDir, "templates")
	if _, err := os.Stat(templatesDir); os.IsNotExist(err) {
		result.CreatedDirs = append(result.CreatedDirs, templatesDir)
	}
	if err := os.MkdirAll(templatesDir, 0755); err != nil {
		return nil, fmt.Errorf("creating templates dir: %w", err)
	}

	// Create pipelines dir
	pipelinesDir := filepath.Join(result.NTMDir, "pipelines")
	if _, err := os.Stat(pipelinesDir); os.IsNotExist(err) {
		result.CreatedDirs = append(result.CreatedDirs, pipelinesDir)
	}
	if err := os.MkdirAll(pipelinesDir, 0755); err != nil {
		return nil, fmt.Errorf("creating pipelines dir: %w", err)
	}

	// Create personas.toml scaffold
	personasPath := filepath.Join(result.NTMDir, "personas.toml")
	if _, err := os.Stat(personasPath); os.IsNotExist(err) {
		personaContent := `# Project personas for NTM
# Define specialized agent roles and behaviors here.
# Example:
# [[personas]]
# name = "architect"
# agent = "claude"
# description = "High-level design and architecture"
# system_prompt = """You are the architecture specialist..."""
`
		if err := os.WriteFile(personasPath, []byte(personaContent), 0644); err != nil {
			return nil, fmt.Errorf("writing personas.toml: %w", err)
		}
		result.CreatedFiles = append(result.CreatedFiles, personasPath)
	}

	return result, nil
}

func renderProjectConfig(projectName, created string) ([]byte, error) {
	const configTemplate = `# Project-specific NTM configuration
# Overrides global settings for this project

[project]
name = {{quote .ProjectName}}
created = {{quote .Created}}

[agents]
default_count = 3
# claude = "claude --project ..."
# codex = "codex"
# gemini = "gemini"

[integrations]
agent_mail = true
beads = true
cass = true
cm = true

[defaults]
# agents = { cc = 2, cod = 1 }

[palette]
# file = "palette.md"  # Relative to .ntm/

[palette_state]
# pinned = ["build", "test"]
# favorites = ["build"]

[templates]
# dir = "templates"    # Relative to .ntm/
`

	tmpl, err := template.New("project-config").Funcs(template.FuncMap{
		"quote": strconv.Quote,
	}).Parse(configTemplate)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]string{
		"ProjectName": projectName,
		"Created":     created,
	}); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

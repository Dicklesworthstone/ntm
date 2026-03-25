package templates

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

// Loader finds and loads templates from various sources.
type Loader struct {
	projectDir string // Project-specific templates directory
	userDir    string // User templates directory
}

// NewLoader creates a template loader with default paths.
func NewLoader() *Loader {
	projectDir := ".ntm/templates"
	if cwd, err := os.Getwd(); err == nil {
		projectDir = resolveProjectTemplateDir(cwd, "")
	}
	return &Loader{
		projectDir: projectDir,
		userDir:    getDefaultUserTemplateDir(),
	}
}

// NewLoaderWithProject creates a template loader for a specific project.
func NewLoaderWithProject(projectPath string) *Loader {
	projectDir := resolveProjectTemplateDir(projectPath, projectPath)
	return &Loader{
		projectDir: projectDir,
		userDir:    getDefaultUserTemplateDir(),
	}
}

func resolveProjectTemplateDir(startDir, fallbackProjectRoot string) string {
	projectDir, projectCfg, err := config.FindProjectConfig(startDir)
	if err == nil && projectCfg != nil && projectDir != "" {
		return projectTemplateDirFromConfig(projectDir, projectCfg)
	}
	if fallbackProjectRoot != "" {
		return filepath.Join(fallbackProjectRoot, ".ntm", "templates")
	}
	return ".ntm/templates"
}

func projectTemplateDirFromConfig(projectDir string, cfg *config.ProjectConfig) string {
	templatesDir, err := config.ResolveProjectTemplatesDir(projectDir, cfg)
	if err != nil {
		return filepath.Join(projectDir, ".ntm", "templates")
	}
	return templatesDir
}

// getDefaultUserTemplateDir returns the default user templates directory.
func getDefaultUserTemplateDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "ntm", "templates")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "ntm", "templates")
}

// Load finds and loads a template by name.
// Search order: project > user > builtin
// Returns the first matching template found.
func (l *Loader) Load(name string) (*Template, error) {
	// Normalize name (remove .md extension if present)
	name = strings.TrimSuffix(name, ".md")

	// 1. Check project templates
	if l.projectDir != "" {
		if tmpl, err := l.loadFromDir(l.projectDir, name, SourceProject); err == nil {
			return tmpl, nil
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}

	// 2. Check user templates
	if l.userDir != "" {
		if tmpl, err := l.loadFromDir(l.userDir, name, SourceUser); err == nil {
			return tmpl, nil
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}

	// 3. Check builtin templates
	if tmpl := GetBuiltin(name); tmpl != nil {
		return tmpl, nil
	}

	return nil, &TemplateNotFoundError{Name: name}
}

// loadFromDir attempts to load a template from a directory.
func (l *Loader) loadFromDir(dir, name string, source TemplateSource) (*Template, error) {
	path := filepath.Join(dir, name+".md")

	// Security check: ensure path is inside dir
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(rel, "..") || strings.HasPrefix(rel, "/") {
		return nil, fmt.Errorf("template path traversal attempt: %s", name)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	tmpl, err := Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("parsing template %s: %w", path, err)
	}

	// Override name if not set in frontmatter
	if tmpl.Name == "" {
		tmpl.Name = name
	}
	tmpl.Source = source
	tmpl.SourcePath = path

	return tmpl, nil
}

// List returns all available templates.
func (l *Loader) List() ([]*Template, error) {
	seen := make(map[string]bool)
	var templates []*Template

	// 1. Project templates (highest priority)
	if l.projectDir != "" {
		if tmpls, err := l.listFromDir(l.projectDir, SourceProject); err == nil {
			for _, t := range tmpls {
				if !seen[t.Name] {
					seen[t.Name] = true
					templates = append(templates, t)
				}
			}
		}
	}

	// 2. User templates
	if l.userDir != "" {
		if tmpls, err := l.listFromDir(l.userDir, SourceUser); err == nil {
			for _, t := range tmpls {
				if !seen[t.Name] {
					seen[t.Name] = true
					templates = append(templates, t)
				}
			}
		}
	}

	// 3. Builtin templates
	for _, t := range ListBuiltins() {
		if !seen[t.Name] {
			seen[t.Name] = true
			templates = append(templates, t)
		}
	}

	return templates, nil
}

// listFromDir lists all templates in a directory.
func (l *Loader) listFromDir(dir string, source TemplateSource) ([]*Template, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var templates []*Template
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		name := strings.TrimSuffix(entry.Name(), ".md")
		path := filepath.Join(dir, entry.Name())

		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		tmpl, err := Parse(string(content))
		if err != nil {
			continue
		}

		if tmpl.Name == "" {
			tmpl.Name = name
		}
		tmpl.Source = source
		tmpl.SourcePath = path

		templates = append(templates, tmpl)
	}

	return templates, nil
}

// TemplateNotFoundError indicates a template was not found.
type TemplateNotFoundError struct {
	Name string
}

func (e *TemplateNotFoundError) Error() string {
	return "template not found: " + e.Name
}

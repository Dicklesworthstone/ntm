package persona

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PrepareSystemPrompt writes a persona's system prompt to a file and returns the path.
// If the persona has no system prompt, returns empty string and nil error.
// The prompt file is written to {projectDir}/.ntm/prompts/{personaName}.md
func PrepareSystemPrompt(p *Persona, projectDir string) (string, error) {
	return PrepareSystemPromptWithContext(p, projectDir, nil)
}

// PrepareSystemPromptWithContext writes a persona's system prompt with template context.
func PrepareSystemPromptWithContext(p *Persona, projectDir string, ctx *TemplateContext) (string, error) {
	if p == nil || p.SystemPrompt == "" {
		return "", nil
	}

	// Create prompts directory
	promptsDir := filepath.Join(projectDir, ".ntm", "prompts")
	if err := os.MkdirAll(promptsDir, 0755); err != nil {
		return "", fmt.Errorf("creating prompts directory: %w", err)
	}

	// Load context if not provided
	if ctx == nil {
		ctx = LoadTemplateContext(projectDir)
	}

	// Build the prompt content
	content := p.SystemPrompt

	// If persona has context_files, prepend them
	if len(p.ContextFiles) > 0 {
		contextContent, err := PrepareContextFiles(p, projectDir)
		if err != nil {
			// Log warning but continue without context files
			fmt.Fprintf(os.Stderr, "warning: could not load context files for persona %s: %v\n", p.Name, err)
		} else if contextContent != "" {
			content = contextContent + "\n\n---\n\n" + content
		}
	}

	// Expand any template variables in the prompt
	content = ExpandPromptVarsWithContext(content, p, ctx)

	// Write to file
	promptFile := filepath.Join(promptsDir, p.Name+".md")
	if err := os.WriteFile(promptFile, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("writing prompt file: %w", err)
	}

	return promptFile, nil
}

// PrepareContextFiles reads and concatenates all context_files for a persona.
// Returns the concatenated content as a string.
func PrepareContextFiles(p *Persona, projectDir string) (string, error) {
	if p == nil || len(p.ContextFiles) == 0 {
		return "", nil
	}

	var files []string

	// Expand globs and collect file paths
	for _, pattern := range p.ContextFiles {
		// Handle relative patterns
		fullPattern := pattern
		if !filepath.IsAbs(pattern) {
			fullPattern = filepath.Join(projectDir, pattern)
		}

		matches, err := filepath.Glob(fullPattern)
		if err != nil {
			return "", fmt.Errorf("expanding glob %q: %w", pattern, err)
		}
		files = append(files, matches...)
	}

	if len(files) == 0 {
		return "", nil
	}

	// Read and concatenate files
	var content strings.Builder
	content.WriteString("# Context Files\n\n")

	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			// Skip unreadable files with warning
			fmt.Fprintf(os.Stderr, "warning: could not read context file %s: %v\n", f, err)
			continue
		}

		// Get relative path for display
		relPath := f
		if rel, err := filepath.Rel(projectDir, f); err == nil {
			relPath = rel
		}

		content.WriteString(fmt.Sprintf("## %s\n\n", relPath))
		content.WriteString(string(data))
		content.WriteString("\n\n")
	}

	return content.String(), nil
}

// TemplateContext holds variables for template expansion.
type TemplateContext struct {
	ProjectName     string            // From config or git remote
	Language        string            // Primary language
	CodebaseSummary string            // Project description
	CustomVars      map[string]string // User-defined variables
}

// DefaultTemplateContext returns a TemplateContext with defaults.
func DefaultTemplateContext() *TemplateContext {
	return &TemplateContext{
		ProjectName:     "",
		Language:        "",
		CodebaseSummary: "",
		CustomVars:      make(map[string]string),
	}
}

// LoadTemplateContext loads template context from project directory.
func LoadTemplateContext(projectDir string) *TemplateContext {
	ctx := DefaultTemplateContext()

	// Try to detect project name from git remote or directory name
	ctx.ProjectName = detectProjectName(projectDir)

	// Try to detect primary language
	ctx.Language = detectPrimaryLanguage(projectDir)

	// Load custom vars from .ntm/config.toml if present
	loadCustomVars(projectDir, ctx)

	return ctx
}

// detectProjectName tries to determine project name from git or directory.
func detectProjectName(projectDir string) string {
	// Try git remote first
	if name := getGitRepoName(projectDir); name != "" {
		return name
	}
	// Fall back to directory name
	return filepath.Base(projectDir)
}

// getGitRepoName extracts repo name from git remote origin.
func getGitRepoName(projectDir string) string {
	gitDir := filepath.Join(projectDir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return ""
	}

	// Read git config
	configPath := filepath.Join(gitDir, "config")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}

	// Look for remote origin URL
	lines := strings.Split(string(data), "\n")
	inOrigin := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "[remote \"origin\"]" {
			inOrigin = true
			continue
		}
		if inOrigin && strings.HasPrefix(line, "url = ") {
			url := strings.TrimPrefix(line, "url = ")
			// Extract repo name from URL
			// Handles: git@github.com:user/repo.git or https://github.com/user/repo.git
			url = strings.TrimSuffix(url, ".git")
			parts := strings.Split(url, "/")
			if len(parts) > 0 {
				return parts[len(parts)-1]
			}
		}
		if strings.HasPrefix(line, "[") && line != "[remote \"origin\"]" {
			inOrigin = false
		}
	}

	return ""
}

// detectPrimaryLanguage detects the primary programming language.
func detectPrimaryLanguage(projectDir string) string {
	// Check for common language indicators
	checks := []struct {
		file     string
		language string
	}{
		{"go.mod", "Go"},
		{"Cargo.toml", "Rust"},
		{"package.json", "JavaScript/TypeScript"},
		{"requirements.txt", "Python"},
		{"pyproject.toml", "Python"},
		{"Gemfile", "Ruby"},
		{"pom.xml", "Java"},
		{"build.gradle", "Java/Kotlin"},
	}

	for _, check := range checks {
		if _, err := os.Stat(filepath.Join(projectDir, check.file)); err == nil {
			return check.language
		}
	}

	return ""
}

// loadCustomVars loads custom variables from .ntm/config.toml.
func loadCustomVars(projectDir string, ctx *TemplateContext) {
	configPath := filepath.Join(projectDir, ".ntm", "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return
	}

	// Simple parsing for [template_vars] section
	lines := strings.Split(string(data), "\n")
	inVars := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "[template_vars]" {
			inVars = true
			continue
		}
		if strings.HasPrefix(line, "[") && line != "[template_vars]" {
			inVars = false
			continue
		}
		if inVars && strings.Contains(line, "=") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				key := strings.TrimSpace(parts[0])
				value := strings.TrimSpace(parts[1])
				// Remove quotes
				value = strings.Trim(value, "\"'")
				ctx.CustomVars[key] = value

				// Also set special fields if matching
				switch key {
				case "project_name":
					ctx.ProjectName = value
				case "language":
					ctx.Language = value
				case "codebase_summary":
					ctx.CodebaseSummary = value
				}
			}
		}
	}
}

// ExpandPromptVarsWithContext replaces template variables with context support.
func ExpandPromptVarsWithContext(content string, p *Persona, ctx *TemplateContext) string {
	if p == nil && ctx == nil {
		return content
	}

	// Persona-specific replacements
	if p != nil {
		replacements := map[string]string{
			"{{.Name}}":        p.Name,
			"{{.Description}}": p.Description,
			"{{.AgentType}}":   p.AgentType,
			"{{.Model}}":       p.Model,
		}
		for old, new := range replacements {
			content = strings.ReplaceAll(content, old, new)
		}
	}

	// Context replacements
	if ctx != nil {
		contextReplacements := map[string]string{
			"{{project_name}}":     ctx.ProjectName,
			"{{language}}":         ctx.Language,
			"{{codebase_summary}}": ctx.CodebaseSummary,
		}
		for old, new := range contextReplacements {
			content = strings.ReplaceAll(content, old, new)
		}

		// Custom variables
		for key, value := range ctx.CustomVars {
			content = strings.ReplaceAll(content, "{{"+key+"}}", value)
		}
	}

	return content
}

// CleanupPromptFiles removes prompt files for a session.
// This should be called when a session is killed.
func CleanupPromptFiles(projectDir string) error {
	promptsDir := filepath.Join(projectDir, ".ntm", "prompts")

	// Check if directory exists
	if _, err := os.Stat(promptsDir); os.IsNotExist(err) {
		return nil // Nothing to clean up
	}

	// Remove the entire prompts directory
	return os.RemoveAll(promptsDir)
}

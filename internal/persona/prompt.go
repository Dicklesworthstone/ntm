package persona

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	maxPersonaContextFileBytes = 4 << 20
	maxPersonaContextBytes     = 8 << 20
)

// PrepareSystemPrompt materializes a persona's rendered system/context prompt and
// returns its immutable content-addressed path. A persona with neither a system
// prompt nor context files returns an empty path.
func PrepareSystemPrompt(p *Persona, projectDir string) (string, error) {
	return PrepareSystemPromptWithContext(p, projectDir, nil)
}

// PrepareSystemPromptWithContext writes a persona's system prompt with template context.
func PrepareSystemPromptWithContext(p *Persona, projectDir string, ctx *TemplateContext) (string, error) {
	if p == nil || (p.SystemPrompt == "" && len(p.ContextFiles) == 0) {
		return "", nil
	}
	if err := validatePromptPersonaName(p.Name); err != nil {
		return "", err
	}

	promptsDir, err := ensurePromptsDir(projectDir)
	if err != nil {
		return "", err
	}

	// Load context if not provided
	if ctx == nil {
		ctx = LoadTemplateContext(projectDir)
	}

	// Build the prompt content
	content := p.SystemPrompt

	// Context is part of the persona contract: never silently launch a
	// different prompt when a declared source is invalid or unreadable.
	if len(p.ContextFiles) > 0 {
		contextContent, err := PrepareContextFiles(p, projectDir)
		if err != nil {
			return "", fmt.Errorf("preparing context files for persona %q: %w", p.Name, err)
		}
		if contextContent != "" {
			if content == "" {
				content = contextContent
			} else {
				content = contextContent + "\n\n---\n\n" + content
			}
		}
	}

	// Expand any template variables in the prompt
	content = ExpandPromptVarsWithContext(content, p, ctx)

	// A launch must keep the exact prompt it was prepared with. Keying only by
	// persona name lets a later launch overwrite the file while an earlier agent
	// is still starting. Content addressing makes prepared prompts immutable by
	// identity while naturally reusing identical renderings.
	sum := sha256.Sum256([]byte(content))
	digest := hex.EncodeToString(sum[:16])
	promptFile := filepath.Join(promptsDir, p.Name+"-"+digest+".md")
	if err := writePromptSnapshot(promptFile, []byte(content)); err != nil {
		return "", fmt.Errorf("writing prompt file: %w", err)
	}

	return promptFile, nil
}

func writePromptSnapshot(path string, content []byte) error {
	if existing, err := os.ReadFile(path); err == nil {
		if string(existing) != string(content) {
			return fmt.Errorf("existing content-addressed prompt does not match rendered content: %s", path)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".prompt-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0644); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		if existing, readErr := os.ReadFile(path); readErr == nil && string(existing) == string(content) {
			return nil
		}
		return err
	}
	return nil
}

// PrepareContextFiles reads and concatenates all context_files for a persona.
// Returns the concatenated content as a string.
func PrepareContextFiles(p *Persona, projectDir string) (string, error) {
	if p == nil || len(p.ContextFiles) == 0 {
		return "", nil
	}

	var files []string
	seen := make(map[string]struct{})

	// Expand globs and collect file paths
	for _, pattern := range p.ContextFiles {
		fullPattern, err := resolveContextPattern(projectDir, pattern)
		if err != nil {
			return "", err
		}

		matches, err := filepath.Glob(fullPattern)
		if err != nil {
			return "", fmt.Errorf("expanding glob %q: %w", pattern, err)
		}
		for _, match := range matches {
			resolved, err := resolveContextFile(projectDir, match)
			if err != nil {
				return "", err
			}
			key := filepath.Clean(resolved)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			files = append(files, resolved)
		}
	}

	if len(files) == 0 {
		return "", nil
	}

	// Read and concatenate files
	var content strings.Builder
	content.WriteString("# Context Files\n\n")

	var totalBytes int64
	for _, f := range files {
		file, err := os.Open(f)
		if err != nil {
			return "", fmt.Errorf("opening context file %q: %w", f, err)
		}
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return "", fmt.Errorf("stat context file %q: %w", f, err)
		}
		if info.Size() > maxPersonaContextFileBytes {
			_ = file.Close()
			return "", fmt.Errorf("context file %q exceeds %d-byte limit", f, maxPersonaContextFileBytes)
		}
		data, err := io.ReadAll(io.LimitReader(file, maxPersonaContextFileBytes+1))
		closeErr := file.Close()
		if err != nil {
			return "", fmt.Errorf("reading context file %q: %w", f, err)
		}
		if closeErr != nil {
			return "", fmt.Errorf("closing context file %q: %w", f, closeErr)
		}
		if int64(len(data)) > maxPersonaContextFileBytes {
			return "", fmt.Errorf("context file %q exceeds %d-byte limit", f, maxPersonaContextFileBytes)
		}
		totalBytes += int64(len(data))
		if totalBytes > maxPersonaContextBytes {
			return "", fmt.Errorf("persona context files exceed %d-byte aggregate limit", maxPersonaContextBytes)
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

	var cfg struct {
		TemplateVars map[string]string `toml:"template_vars"`
	}

	if err := toml.Unmarshal(data, &cfg); err != nil {
		// Log error but continue (best effort)
		slog.Warn("error parsing config", "path", configPath, "error", err)
		return
	}

	for key, value := range cfg.TemplateVars {
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

func validatePromptPersonaName(name string) error {
	if !nameRegex.MatchString(name) {
		return fmt.Errorf("persona name %q contains invalid characters (allowed: a-z, A-Z, 0-9, _, -)", name)
	}
	return nil
}

func validatePromptDir(path, kind string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s path: %w", kind, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s path must not be a symlink: %s", kind, path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s path is not a directory: %s", kind, path)
	}
	return nil
}

func ensurePromptsDir(projectDir string) (string, error) {
	ntmDir := filepath.Join(projectDir, ".ntm")
	if err := validatePromptDir(ntmDir, "ntm"); err != nil {
		return "", err
	}
	if err := os.MkdirAll(ntmDir, 0755); err != nil {
		return "", fmt.Errorf("creating ntm directory: %w", err)
	}
	if err := validatePromptDir(ntmDir, "ntm"); err != nil {
		return "", err
	}

	promptsDir := filepath.Join(ntmDir, "prompts")
	if err := validatePromptDir(promptsDir, "prompts"); err != nil {
		return "", err
	}
	if err := os.MkdirAll(promptsDir, 0755); err != nil {
		return "", fmt.Errorf("creating prompts directory: %w", err)
	}
	if err := validatePromptDir(promptsDir, "prompts"); err != nil {
		return "", err
	}

	return promptsDir, nil
}

func resolveContextPattern(projectDir, pattern string) (string, error) {
	if strings.TrimSpace(pattern) == "" {
		return "", fmt.Errorf("context_files pattern cannot be empty")
	}
	fullPattern := pattern
	if !filepath.IsAbs(pattern) {
		fullPattern = filepath.Join(projectDir, pattern)
	}
	if err := ensureWithinProject(projectDir, fullPattern, "context_files pattern"); err != nil {
		return "", err
	}
	return fullPattern, nil
}

func resolveContextFile(projectDir, path string) (string, error) {
	if err := ensureWithinProject(projectDir, path, "context file"); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("stat context file %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("context file must not be a symlink: %s", path)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("context file is not a regular file: %s", path)
	}
	if err := ensureResolvedWithinProject(projectDir, path, "context file"); err != nil {
		return "", err
	}
	return path, nil
}

func ensureWithinProject(projectDir, path, kind string) error {
	projectAbs, err := filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("resolving project directory: %w", err)
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolving %s %q: %w", kind, path, err)
	}
	rel, err := filepath.Rel(projectAbs, pathAbs)
	if err != nil {
		return fmt.Errorf("checking %s %q: %w", kind, path, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%s escapes project directory: %s", kind, path)
	}
	return nil
}

func ensureResolvedWithinProject(projectDir, path, kind string) error {
	projectAbs, err := filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("resolving project directory: %w", err)
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolving %s %q: %w", kind, path, err)
	}
	projectReal, err := filepath.EvalSymlinks(projectAbs)
	if err != nil {
		return fmt.Errorf("resolving project directory symlinks: %w", err)
	}
	pathReal, err := filepath.EvalSymlinks(pathAbs)
	if err != nil {
		return fmt.Errorf("resolving %s symlinks %q: %w", kind, path, err)
	}
	rel, err := filepath.Rel(projectReal, pathReal)
	if err != nil {
		return fmt.Errorf("checking resolved %s %q: %w", kind, path, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%s escapes project directory through symlink: %s", kind, path)
	}
	return nil
}
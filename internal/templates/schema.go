// Package templates provides prompt template loading, parsing, and variable substitution.
// This file defines the schema for session templates that capture common workflow patterns.
package templates

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SessionTemplate defines the schema for session workflow templates.
// Session templates capture common multi-agent workflow patterns including
// agent configuration, prompts, file reservations, and environment setup.
//
// Templates can be stored in:
//   - Project: .ntm/templates/<name>.yaml
//   - User: ~/.config/ntm/templates/<name>.yaml
//   - Builtin: compiled into NTM
type SessionTemplate struct {
	// APIVersion specifies the schema version for forward compatibility.
	// Current version: "v1"
	APIVersion string `yaml:"apiVersion"`

	// Kind identifies this as a SessionTemplate (vs other template types).
	Kind string `yaml:"kind"`

	// Metadata contains template identification and categorization.
	Metadata SessionTemplateMetadata `yaml:"metadata"`

	// Spec defines the template specification.
	Spec SessionTemplateSpec `yaml:"spec"`
}

// SessionTemplateMetadata contains template identification.
type SessionTemplateMetadata struct {
	// Name is the unique identifier for this template (required).
	Name string `yaml:"name"`

	// Description explains what this template does.
	Description string `yaml:"description,omitempty"`

	// Tags for categorization and filtering.
	Tags []string `yaml:"tags,omitempty"`

	// Author information.
	Author string `yaml:"author,omitempty"`

	// Version of this template definition.
	Version string `yaml:"version,omitempty"`

	// Extends specifies a base template to inherit from.
	// The base template's values are used as defaults,
	// and this template's values override them.
	Extends string `yaml:"extends,omitempty"`

	// Source indicates where this template was loaded from.
	// Values: "builtin", "user", "project"
	// This is set at load time, not from YAML.
	Source string `yaml:"-"`
}

// SessionTemplateSpec defines the template's behavior.
type SessionTemplateSpec struct {
	// Agents defines the agent configuration for this session.
	Agents AgentsSpec `yaml:"agents"`

	// Prompts defines initial prompts to send after spawn.
	Prompts PromptsSpec `yaml:"prompts,omitempty"`

	// FileReservations defines files to reserve via Agent Mail before starting.
	FileReservations FileReservationsSpec `yaml:"fileReservations,omitempty"`

	// Beads defines integration with the beads issue tracker.
	Beads BeadsSpec `yaml:"beads,omitempty"`

	// CASS defines CASS memory system context injection.
	CASS CASSSpec `yaml:"cass,omitempty"`

	// Environment defines pre/post spawn hooks and environment setup.
	Environment EnvironmentSpec `yaml:"environment,omitempty"`

	// Options defines spawn behavior options.
	Options SessionOptionsSpec `yaml:"options,omitempty"`
}

// AgentsSpec defines agent counts and configurations.
type AgentsSpec struct {
	// Claude defines Claude Code agent configuration.
	Claude *AgentTypeSpec `yaml:"claude,omitempty"`

	// Codex defines OpenAI Codex agent configuration.
	Codex *AgentTypeSpec `yaml:"codex,omitempty"`

	// Gemini defines Google Gemini agent configuration.
	Gemini *AgentTypeSpec `yaml:"gemini,omitempty"`

	// UserPane controls whether a user pane is included (default: true).
	UserPane *bool `yaml:"userPane,omitempty"`

	// Personas defines persona-based agent configuration.
	// Use personas for role-based agents with system prompts.
	Personas []PersonaSpec `yaml:"personas,omitempty"`

	// Total is an optional field to validate total agent count.
	// If set, validation will fail if actual count differs.
	Total *int `yaml:"total,omitempty"`
}

// AgentTypeSpec defines configuration for a specific agent type.
type AgentTypeSpec struct {
	// Count is the number of agents of this type.
	Count int `yaml:"count"`

	// Model specifies the model variant (e.g., "opus", "sonnet" for Claude).
	Model string `yaml:"model,omitempty"`

	// Variants allows mixed model configurations.
	// Example: [{count: 2, model: "opus"}, {count: 1, model: "sonnet"}]
	Variants []AgentVariantSpec `yaml:"variants,omitempty"`
}

// AgentVariantSpec defines a specific model variant configuration.
type AgentVariantSpec struct {
	// Count is the number of agents with this variant.
	Count int `yaml:"count"`

	// Model specifies the model variant.
	Model string `yaml:"model"`
}

// PersonaSpec defines a persona-based agent configuration.
type PersonaSpec struct {
	// Name is the persona name (e.g., "architect", "implementer").
	Name string `yaml:"name"`

	// Count is the number of agents with this persona (default: 1).
	Count int `yaml:"count,omitempty"`
}

// PromptsSpec defines prompt configuration.
type PromptsSpec struct {
	// Initial is the prompt sent to all agents after spawn.
	Initial string `yaml:"initial,omitempty"`

	// Template references a prompt template by name.
	// The template is loaded and executed with available context.
	Template string `yaml:"template,omitempty"`

	// PerAgent allows different prompts for different agents.
	// Keys can be: "cc", "cod", "gmi", or specific pane patterns.
	PerAgent map[string]string `yaml:"perAgent,omitempty"`

	// Variables provides variable values for template substitution.
	Variables map[string]string `yaml:"variables,omitempty"`

	// Delay before sending the initial prompt after spawn.
	Delay string `yaml:"delay,omitempty"`
}

// FileReservationsSpec defines file reservation configuration.
type FileReservationsSpec struct {
	// Enabled controls whether file reservations are requested.
	Enabled bool `yaml:"enabled,omitempty"`

	// Patterns are glob patterns for files to reserve.
	// Example: ["internal/**/*.go", "cmd/**/*.go"]
	Patterns []string `yaml:"patterns,omitempty"`

	// Exclusive controls whether reservations are exclusive (default: true).
	Exclusive *bool `yaml:"exclusive,omitempty"`

	// TTL is the time-to-live for reservations.
	TTL string `yaml:"ttl,omitempty"`
}

// BeadsSpec defines beads integration configuration.
type BeadsSpec struct {
	// Recipe is the name of a bv recipe to use for work assignment.
	Recipe string `yaml:"recipe,omitempty"`

	// AutoAssign enables automatic bead assignment to agents.
	AutoAssign bool `yaml:"autoAssign,omitempty"`

	// Filter limits which beads are assigned (e.g., by label or type).
	Filter string `yaml:"filter,omitempty"`
}

// CASSSpec defines CASS memory system configuration.
type CASSSpec struct {
	// Enabled controls whether CASS context is injected.
	Enabled *bool `yaml:"enabled,omitempty"`

	// Query is the context search query.
	// If empty, uses the initial prompt or infers from context.
	Query string `yaml:"query,omitempty"`

	// MaxSessions limits how many past sessions to include.
	MaxSessions int `yaml:"maxSessions,omitempty"`
}

// EnvironmentSpec defines environment and hook configuration.
type EnvironmentSpec struct {
	// PreSpawn hooks run before agents are spawned.
	PreSpawn []HookSpec `yaml:"preSpawn,omitempty"`

	// PostSpawn hooks run after all agents are spawned.
	PostSpawn []HookSpec `yaml:"postSpawn,omitempty"`

	// Env defines environment variables to set.
	Env map[string]string `yaml:"env,omitempty"`

	// WorkDir sets the working directory for the session.
	WorkDir string `yaml:"workDir,omitempty"`
}

// HookSpec defines a pre/post spawn hook.
type HookSpec struct {
	// Name identifies this hook (for logging/debugging).
	Name string `yaml:"name,omitempty"`

	// Command is the shell command to run.
	Command string `yaml:"command"`

	// Timeout for the command (default: 30s).
	Timeout string `yaml:"timeout,omitempty"`

	// ContinueOnError allows spawn to continue if hook fails.
	ContinueOnError bool `yaml:"continueOnError,omitempty"`
}

// SessionOptionsSpec defines spawn behavior options.
type SessionOptionsSpec struct {
	// Stagger controls staggered agent spawning.
	Stagger *StaggerSpec `yaml:"stagger,omitempty"`

	// AutoRestart enables automatic agent restart on failure.
	AutoRestart bool `yaml:"autoRestart,omitempty"`

	// Checkpoint enables automatic session checkpointing.
	Checkpoint *CheckpointSpec `yaml:"checkpoint,omitempty"`
}

// StaggerSpec defines staggered spawn configuration.
type StaggerSpec struct {
	// Enabled controls whether staggered spawn is used.
	Enabled bool `yaml:"enabled,omitempty"`

	// Interval between agent spawns.
	Interval string `yaml:"interval,omitempty"`
}

// CheckpointSpec defines checkpoint configuration.
type CheckpointSpec struct {
	// Enabled controls whether checkpointing is active.
	Enabled bool `yaml:"enabled,omitempty"`

	// Interval between automatic checkpoints.
	Interval string `yaml:"interval,omitempty"`
}

// Error definitions for template validation.
var (
	ErrMissingAPIVersion = errors.New("apiVersion is required")
	ErrInvalidAPIVersion = errors.New("unsupported apiVersion")
	ErrMissingKind       = errors.New("kind is required")
	ErrInvalidKind       = errors.New("invalid kind")
	ErrMissingName       = errors.New("metadata.name is required")
	ErrInvalidName       = errors.New("metadata.name must be alphanumeric with hyphens/underscores")
	ErrNoAgents          = errors.New("spec.agents must define at least one agent")
	ErrInvalidAgentCount = errors.New("agent count must be positive")
	ErrTotalMismatch     = errors.New("total agent count does not match specification")
	ErrInvalidDuration   = errors.New("invalid duration format")
	ErrInvalidPattern    = errors.New("invalid file pattern")
	ErrConflictingAgents = errors.New("cannot specify both count/model and variants")
	ErrCircularInherit   = errors.New("circular template inheritance detected")
	ErrMaxInheritDepth   = errors.New("maximum template inheritance depth exceeded")
)

// envVarPattern matches ${VAR} and ${VAR:-default} patterns.
var envVarPattern = regexp.MustCompile(`\$\{([a-zA-Z_][a-zA-Z0-9_]*)(?::-([^}]*))?\}`)

// expandEnvVars expands environment variables in the given string.
// Supports ${VAR} and ${VAR:-default} syntax.
func expandEnvVars(s string) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		parts := envVarPattern.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}

		varName := parts[1]
		defaultVal := ""
		if len(parts) >= 3 {
			defaultVal = parts[2]
		}

		if val := os.Getenv(varName); val != "" {
			return val
		}
		return defaultVal
	})
}

// expandEnvVarsInContent expands environment variables in YAML content.
func expandEnvVarsInContent(content []byte) []byte {
	return []byte(expandEnvVars(string(content)))
}

// ParseSessionTemplate parses a session template from YAML content.
// Environment variables in the content are expanded using ${VAR} or ${VAR:-default} syntax.
func ParseSessionTemplate(content []byte) (*SessionTemplate, error) {
	expanded := expandEnvVarsInContent(content)

	var tmpl SessionTemplate
	if err := yaml.Unmarshal(expanded, &tmpl); err != nil {
		return nil, fmt.Errorf("parsing session template: %w", err)
	}
	return &tmpl, nil
}

// ParseSessionTemplateRaw parses a session template without environment variable expansion.
func ParseSessionTemplateRaw(content []byte) (*SessionTemplate, error) {
	var tmpl SessionTemplate
	if err := yaml.Unmarshal(content, &tmpl); err != nil {
		return nil, fmt.Errorf("parsing session template: %w", err)
	}
	return &tmpl, nil
}

// LoadSessionTemplate loads a session template from a file path.
func LoadSessionTemplate(path string) (*SessionTemplate, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading session template %s: %w", path, err)
	}
	return ParseSessionTemplate(content)
}

// Validate checks the template for errors and returns helpful messages.
func (t *SessionTemplate) Validate() error {
	var errs []string

	if t.APIVersion == "" {
		errs = append(errs, ErrMissingAPIVersion.Error())
	} else if t.APIVersion != "v1" {
		errs = append(errs, fmt.Sprintf("%s: got %q", ErrInvalidAPIVersion.Error(), t.APIVersion))
	}

	if t.Kind == "" {
		errs = append(errs, ErrMissingKind.Error())
	} else if t.Kind != "SessionTemplate" {
		errs = append(errs, fmt.Sprintf("%s: got %q", ErrInvalidKind.Error(), t.Kind))
	}

	if t.Metadata.Name == "" {
		errs = append(errs, ErrMissingName.Error())
	} else if !isValidTemplateName(t.Metadata.Name) {
		errs = append(errs, fmt.Sprintf("%s: got %q", ErrInvalidName.Error(), t.Metadata.Name))
	}

	if err := t.Spec.Agents.Validate(); err != nil {
		errs = append(errs, err.Error())
	}

	if err := t.Spec.Prompts.Validate(); err != nil {
		errs = append(errs, err.Error())
	}

	if err := t.Spec.FileReservations.Validate(); err != nil {
		errs = append(errs, err.Error())
	}

	if err := t.Spec.Options.Validate(); err != nil {
		errs = append(errs, err.Error())
	}

	if err := t.Spec.Environment.Validate(); err != nil {
		errs = append(errs, err.Error())
	}

	if len(errs) > 0 {
		return fmt.Errorf("session template validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// GetAgentCount returns the total number of agents specified in this template.
func (t *SessionTemplate) GetAgentCount() int {
	count := 0
	if t.Spec.Agents.Claude != nil {
		count += t.Spec.Agents.Claude.Count
	}
	if t.Spec.Agents.Codex != nil {
		count += t.Spec.Agents.Codex.Count
	}
	if t.Spec.Agents.Gemini != nil {
		count += t.Spec.Agents.Gemini.Count
	}
	return count
}

// MergeFrom applies values from a parent template, keeping child values where set.
// This is used for template inheritance via the "extends" field.
func (t *SessionTemplate) MergeFrom(parent *SessionTemplate) {
	if t.Metadata.Description == "" && parent.Metadata.Description != "" {
		t.Metadata.Description = parent.Metadata.Description
	}
	if len(t.Metadata.Tags) == 0 && len(parent.Metadata.Tags) > 0 {
		t.Metadata.Tags = append([]string{}, parent.Metadata.Tags...)
	}

	if parent.Spec.Agents.Claude != nil && t.Spec.Agents.Claude == nil {
		t.Spec.Agents.Claude = parent.Spec.Agents.Claude
	}
	if parent.Spec.Agents.Codex != nil && t.Spec.Agents.Codex == nil {
		t.Spec.Agents.Codex = parent.Spec.Agents.Codex
	}
	if parent.Spec.Agents.Gemini != nil && t.Spec.Agents.Gemini == nil {
		t.Spec.Agents.Gemini = parent.Spec.Agents.Gemini
	}
	if parent.Spec.Agents.UserPane != nil && t.Spec.Agents.UserPane == nil {
		t.Spec.Agents.UserPane = parent.Spec.Agents.UserPane
	}
	if len(parent.Spec.Agents.Personas) > 0 && len(t.Spec.Agents.Personas) == 0 {
		t.Spec.Agents.Personas = append([]PersonaSpec{}, parent.Spec.Agents.Personas...)
	}

	if t.Spec.Prompts.Initial == "" && parent.Spec.Prompts.Initial != "" {
		t.Spec.Prompts.Initial = parent.Spec.Prompts.Initial
	}
	if t.Spec.Prompts.Template == "" && parent.Spec.Prompts.Template != "" {
		t.Spec.Prompts.Template = parent.Spec.Prompts.Template
	}
	if t.Spec.Prompts.Delay == "" && parent.Spec.Prompts.Delay != "" {
		t.Spec.Prompts.Delay = parent.Spec.Prompts.Delay
	}
	if len(parent.Spec.Prompts.Variables) > 0 {
		if t.Spec.Prompts.Variables == nil {
			t.Spec.Prompts.Variables = make(map[string]string)
		}
		for k, v := range parent.Spec.Prompts.Variables {
			if _, exists := t.Spec.Prompts.Variables[k]; !exists {
				t.Spec.Prompts.Variables[k] = v
			}
		}
	}
	if len(parent.Spec.Prompts.PerAgent) > 0 {
		if t.Spec.Prompts.PerAgent == nil {
			t.Spec.Prompts.PerAgent = make(map[string]string)
		}
		for k, v := range parent.Spec.Prompts.PerAgent {
			if _, exists := t.Spec.Prompts.PerAgent[k]; !exists {
				t.Spec.Prompts.PerAgent[k] = v
			}
		}
	}

	if !t.Spec.FileReservations.Enabled && parent.Spec.FileReservations.Enabled {
		t.Spec.FileReservations = parent.Spec.FileReservations
	}

	if t.Spec.Beads.Recipe == "" && parent.Spec.Beads.Recipe != "" {
		t.Spec.Beads = parent.Spec.Beads
	}

	if (t.Spec.CASS.Enabled == nil || !*t.Spec.CASS.Enabled) &&
		parent.Spec.CASS.Enabled != nil && *parent.Spec.CASS.Enabled {
		t.Spec.CASS = parent.Spec.CASS
	}

	if len(parent.Spec.Environment.PreSpawn) > 0 && len(t.Spec.Environment.PreSpawn) == 0 {
		t.Spec.Environment.PreSpawn = append([]HookSpec{}, parent.Spec.Environment.PreSpawn...)
	}
	if len(parent.Spec.Environment.PostSpawn) > 0 && len(t.Spec.Environment.PostSpawn) == 0 {
		t.Spec.Environment.PostSpawn = append([]HookSpec{}, parent.Spec.Environment.PostSpawn...)
	}
	if len(parent.Spec.Environment.Env) > 0 {
		if t.Spec.Environment.Env == nil {
			t.Spec.Environment.Env = make(map[string]string)
		}
		for k, v := range parent.Spec.Environment.Env {
			if _, exists := t.Spec.Environment.Env[k]; !exists {
				t.Spec.Environment.Env[k] = v
			}
		}
	}

	if parent.Spec.Options.Stagger != nil && t.Spec.Options.Stagger == nil {
		stagger := *parent.Spec.Options.Stagger
		t.Spec.Options.Stagger = &stagger
	}
	if parent.Spec.Options.Checkpoint != nil && t.Spec.Options.Checkpoint == nil {
		checkpoint := *parent.Spec.Options.Checkpoint
		t.Spec.Options.Checkpoint = &checkpoint
	}
	if parent.Spec.Options.AutoRestart && !t.Spec.Options.AutoRestart {
		t.Spec.Options.AutoRestart = parent.Spec.Options.AutoRestart
	}
}

// Validate checks the agents spec.
func (a *AgentsSpec) Validate() error {
	total := 0

	if a.Claude != nil {
		if err := a.Claude.Validate("claude"); err != nil {
			return err
		}
		total += a.Claude.TotalCount()
	}

	if a.Codex != nil {
		if err := a.Codex.Validate("codex"); err != nil {
			return err
		}
		total += a.Codex.TotalCount()
	}

	if a.Gemini != nil {
		if err := a.Gemini.Validate("gemini"); err != nil {
			return err
		}
		total += a.Gemini.TotalCount()
	}

	for _, p := range a.Personas {
		count := p.Count
		if count == 0 {
			count = 1
		}
		if count < 0 {
			return fmt.Errorf("persona %q: %w", p.Name, ErrInvalidAgentCount)
		}
		total += count
	}

	if total == 0 {
		return ErrNoAgents
	}

	if a.Total != nil && *a.Total != total {
		return fmt.Errorf("%w: expected %d, got %d", ErrTotalMismatch, *a.Total, total)
	}

	return nil
}

// Validate checks an agent type spec.
func (a *AgentTypeSpec) Validate(agentType string) error {
	if a.Count > 0 && len(a.Variants) > 0 {
		return fmt.Errorf("%s: %w", agentType, ErrConflictingAgents)
	}

	if a.Count < 0 {
		return fmt.Errorf("%s: %w", agentType, ErrInvalidAgentCount)
	}

	for i, v := range a.Variants {
		if v.Count <= 0 {
			return fmt.Errorf("%s variants[%d]: %w", agentType, i, ErrInvalidAgentCount)
		}
	}

	return nil
}

// TotalCount returns the total number of agents for this type.
func (a *AgentTypeSpec) TotalCount() int {
	if len(a.Variants) > 0 {
		total := 0
		for _, v := range a.Variants {
			total += v.Count
		}
		return total
	}
	return a.Count
}

// Validate checks the prompts spec.
func (p *PromptsSpec) Validate() error {
	if p.Delay != "" {
		if _, err := time.ParseDuration(p.Delay); err != nil {
			return fmt.Errorf("prompts.delay: %w", ErrInvalidDuration)
		}
	}
	return nil
}

// Validate checks the file reservations spec.
func (f *FileReservationsSpec) Validate() error {
	if f.TTL != "" {
		if _, err := time.ParseDuration(f.TTL); err != nil {
			return fmt.Errorf("fileReservations.ttl: %w", ErrInvalidDuration)
		}
	}

	for i, pattern := range f.Patterns {
		if pattern == "" {
			return fmt.Errorf("fileReservations.patterns[%d]: %w", i, ErrInvalidPattern)
		}
	}

	return nil
}

// Validate checks the options spec.
func (o *SessionOptionsSpec) Validate() error {
	if o.Stagger != nil && o.Stagger.Interval != "" {
		if _, err := time.ParseDuration(o.Stagger.Interval); err != nil {
			return fmt.Errorf("options.stagger.interval: %w", ErrInvalidDuration)
		}
	}

	if o.Checkpoint != nil && o.Checkpoint.Interval != "" {
		if _, err := time.ParseDuration(o.Checkpoint.Interval); err != nil {
			return fmt.Errorf("options.checkpoint.interval: %w", ErrInvalidDuration)
		}
	}

	return nil
}

// Validate checks the environment spec.
func (e *EnvironmentSpec) Validate() error {
	for i, hook := range e.PreSpawn {
		if hook.Command == "" {
			return fmt.Errorf("environment.preSpawn[%d]: command is required", i)
		}
		if hook.Timeout != "" {
			if _, err := time.ParseDuration(hook.Timeout); err != nil {
				return fmt.Errorf("environment.preSpawn[%d].timeout: %w", i, ErrInvalidDuration)
			}
		}
	}

	for i, hook := range e.PostSpawn {
		if hook.Command == "" {
			return fmt.Errorf("environment.postSpawn[%d]: command is required", i)
		}
		if hook.Timeout != "" {
			if _, err := time.ParseDuration(hook.Timeout); err != nil {
				return fmt.Errorf("environment.postSpawn[%d].timeout: %w", i, ErrInvalidDuration)
			}
		}
	}

	return nil
}

// isValidTemplateName checks if a name contains only valid characters.
func isValidTemplateName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= '0' && r <= '9' && i > 0 {
			continue
		}
		if (r == '-' || r == '_') && i > 0 {
			continue
		}
		return false
	}
	return true
}

// SessionTemplateLoader loads session templates from various sources.
type SessionTemplateLoader struct {
	projectDir string
	userDir    string
}

// NewSessionTemplateLoader creates a loader with default paths.
func NewSessionTemplateLoader() *SessionTemplateLoader {
	return &SessionTemplateLoader{
		projectDir: ".ntm/templates",
		userDir:    getDefaultSessionTemplateDir(),
	}
}

// NewSessionTemplateLoaderWithProject creates a loader for a specific project.
func NewSessionTemplateLoaderWithProject(projectPath string) *SessionTemplateLoader {
	return &SessionTemplateLoader{
		projectDir: filepath.Join(projectPath, ".ntm", "templates"),
		userDir:    getDefaultSessionTemplateDir(),
	}
}

func getDefaultSessionTemplateDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "ntm", "templates")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "ntm", "templates")
}

// maxInheritanceDepth is the maximum depth for template inheritance chains.
const maxInheritanceDepth = 10

// Load finds and loads a session template by name, resolving inheritance.
// Search order: project > user > builtin
func (l *SessionTemplateLoader) Load(name string) (*SessionTemplate, error) {
	return l.loadWithInheritance(name, nil, 0)
}

// loadWithInheritance loads a template and resolves its inheritance chain.
func (l *SessionTemplateLoader) loadWithInheritance(name string, seen map[string]bool, depth int) (*SessionTemplate, error) {
	if seen == nil {
		seen = make(map[string]bool)
	}
	if seen[name] {
		return nil, fmt.Errorf("%w: %s", ErrCircularInherit, name)
	}
	if depth > maxInheritanceDepth {
		return nil, fmt.Errorf("%w: depth %d", ErrMaxInheritDepth, depth)
	}
	seen[name] = true

	tmpl, err := l.loadDirect(name)
	if err != nil {
		return nil, err
	}

	if tmpl.Metadata.Extends == "" {
		return tmpl, nil
	}

	parent, err := l.loadWithInheritance(tmpl.Metadata.Extends, seen, depth+1)
	if err != nil {
		return nil, fmt.Errorf("loading parent template %q: %w", tmpl.Metadata.Extends, err)
	}

	tmpl.MergeFrom(parent)

	return tmpl, nil
}

// loadDirect loads a template without resolving inheritance.
func (l *SessionTemplateLoader) loadDirect(name string) (*SessionTemplate, error) {
	name = strings.TrimSuffix(name, ".yaml")
	name = strings.TrimSuffix(name, ".yml")

	if l.projectDir != "" {
		path := filepath.Join(l.projectDir, name+".yaml")
		if tmpl, err := LoadSessionTemplate(path); err == nil {
			tmpl.Metadata.Source = "project"
			return tmpl, nil
		}
		path = filepath.Join(l.projectDir, name+".yml")
		if tmpl, err := LoadSessionTemplate(path); err == nil {
			tmpl.Metadata.Source = "project"
			return tmpl, nil
		}
	}

	if l.userDir != "" {
		path := filepath.Join(l.userDir, name+".yaml")
		if tmpl, err := LoadSessionTemplate(path); err == nil {
			tmpl.Metadata.Source = "user"
			return tmpl, nil
		}
		path = filepath.Join(l.userDir, name+".yml")
		if tmpl, err := LoadSessionTemplate(path); err == nil {
			tmpl.Metadata.Source = "user"
			return tmpl, nil
		}
	}

	if tmpl := GetBuiltinSessionTemplate(name); tmpl != nil {
		tmpl.Metadata.Source = "builtin"
		return tmpl, nil
	}

	return nil, fmt.Errorf("session template not found: %s", name)
}

// List returns all available session templates.
func (l *SessionTemplateLoader) List() ([]*SessionTemplate, error) {
	seen := make(map[string]bool)
	var templates []*SessionTemplate

	// Project templates (highest precedence)
	if l.projectDir != "" {
		if tmpls, err := listSessionTemplatesFromDir(l.projectDir); err == nil {
			for _, t := range tmpls {
				if !seen[t.Metadata.Name] {
					seen[t.Metadata.Name] = true
					t.Metadata.Source = "project"
					templates = append(templates, t)
				}
			}
		}
	}

	// User templates
	if l.userDir != "" {
		if tmpls, err := listSessionTemplatesFromDir(l.userDir); err == nil {
			for _, t := range tmpls {
				if !seen[t.Metadata.Name] {
					seen[t.Metadata.Name] = true
					t.Metadata.Source = "user"
					templates = append(templates, t)
				}
			}
		}
	}

	// Built-in templates (lowest precedence)
	for _, t := range ListBuiltinSessionTemplates() {
		if !seen[t.Metadata.Name] {
			seen[t.Metadata.Name] = true
			t.Metadata.Source = "builtin"
			templates = append(templates, t)
		}
	}

	return templates, nil
}

func listSessionTemplatesFromDir(dir string) ([]*SessionTemplate, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var templates []*SessionTemplate
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		path := filepath.Join(dir, name)
		tmpl, err := LoadSessionTemplate(path)
		if err != nil {
			continue
		}

		if err := tmpl.Validate(); err != nil {
			continue
		}

		templates = append(templates, tmpl)
	}

	return templates, nil
}

// Builtin session templates registry.
var builtinSessionTemplates = make(map[string]*SessionTemplate)

// RegisterBuiltinSessionTemplate adds a template to the builtin registry.
func RegisterBuiltinSessionTemplate(tmpl *SessionTemplate) {
	builtinSessionTemplates[tmpl.Metadata.Name] = tmpl
}

// GetBuiltinSessionTemplate retrieves a builtin template by name.
func GetBuiltinSessionTemplate(name string) *SessionTemplate {
	return builtinSessionTemplates[name]
}

// ListBuiltinSessionTemplates returns all registered builtin templates.
func ListBuiltinSessionTemplates() []*SessionTemplate {
	templates := make([]*SessionTemplate, 0, len(builtinSessionTemplates))
	for _, t := range builtinSessionTemplates {
		templates = append(templates, t)
	}
	return templates
}

// Register default builtin templates.
func init() {
	RegisterBuiltinSessionTemplate(&SessionTemplate{
		APIVersion: "v1",
		Kind:       "SessionTemplate",
		Metadata: SessionTemplateMetadata{
			Name:        "quick-claude",
			Description: "Quick single-agent Claude session",
			Tags:        []string{"quick", "solo"},
		},
		Spec: SessionTemplateSpec{
			Agents: AgentsSpec{
				Claude: &AgentTypeSpec{Count: 1},
			},
		},
	})

	RegisterBuiltinSessionTemplate(&SessionTemplate{
		APIVersion: "v1",
		Kind:       "SessionTemplate",
		Metadata: SessionTemplateMetadata{
			Name:        "full-stack",
			Description: "Full-stack team with multiple agent types",
			Tags:        []string{"team", "multi-agent"},
		},
		Spec: SessionTemplateSpec{
			Agents: AgentsSpec{
				Claude: &AgentTypeSpec{Count: 2, Model: "opus"},
				Codex:  &AgentTypeSpec{Count: 1},
				Gemini: &AgentTypeSpec{Count: 1},
			},
			Options: SessionOptionsSpec{
				Stagger: &StaggerSpec{
					Enabled:  true,
					Interval: "30s",
				},
			},
		},
	})

	RegisterBuiltinSessionTemplate(&SessionTemplate{
		APIVersion: "v1",
		Kind:       "SessionTemplate",
		Metadata: SessionTemplateMetadata{
			Name:        "review-team",
			Description: "Code review team setup",
			Tags:        []string{"review", "team"},
		},
		Spec: SessionTemplateSpec{
			Agents: AgentsSpec{
				Claude: &AgentTypeSpec{
					Variants: []AgentVariantSpec{
						{Count: 1, Model: "opus"},
						{Count: 2, Model: "sonnet"},
					},
				},
			},
			FileReservations: FileReservationsSpec{
				Enabled:  true,
				Patterns: []string{"**/*.go", "**/*.ts"},
			},
		},
	})

	// code-review: Optimized for reviewing PRs and providing feedback
	RegisterBuiltinSessionTemplate(&SessionTemplate{
		APIVersion: "v1",
		Kind:       "SessionTemplate",
		Metadata: SessionTemplateMetadata{
			Name:        "code-review",
			Description: "Optimized for reviewing PRs and providing feedback",
			Tags:        []string{"review", "pr", "quality"},
		},
		Spec: SessionTemplateSpec{
			Agents: AgentsSpec{
				Claude: &AgentTypeSpec{Count: 2, Model: "opus"},
				Codex:  &AgentTypeSpec{Count: 1},
			},
			Prompts: PromptsSpec{
				Initial: `You are part of a code review team. Your focus is on:
1. Code correctness and potential bugs
2. Security vulnerabilities (OWASP top 10)
3. Performance implications
4. Code maintainability and readability
5. Test coverage gaps

Read AGENTS.md first, then register with Agent Mail.
When reviewing, be specific about line numbers and provide concrete suggestions.`,
				PerAgent: map[string]string{
					"cc": `You are a senior code reviewer focusing on architecture and design patterns.
Look for:
- SOLID principle violations
- Code duplication opportunities
- Error handling completeness
- API design consistency`,
					"cod": `You are a test coverage analyst.
Focus on:
- Missing test cases
- Edge cases not covered
- Integration test gaps
- Mocking strategy improvements`,
				},
				Delay: "3s",
			},
			FileReservations: FileReservationsSpec{
				Enabled:  true,
				Patterns: []string{"**/*.go", "**/*.ts", "**/*.py"},
				TTL:      "2h",
			},
		},
	})

	// refactor: For large-scale code refactoring
	RegisterBuiltinSessionTemplate(&SessionTemplate{
		APIVersion: "v1",
		Kind:       "SessionTemplate",
		Metadata: SessionTemplateMetadata{
			Name:        "refactor",
			Description: "For large-scale code refactoring with architecture focus",
			Tags:        []string{"refactor", "architecture", "cleanup"},
		},
		Spec: SessionTemplateSpec{
			Agents: AgentsSpec{
				Claude: &AgentTypeSpec{Count: 3, Model: "opus"},
			},
			Prompts: PromptsSpec{
				Initial: `You are part of a refactoring team. Read AGENTS.md first.

Key principles:
1. Make incremental, testable changes
2. Maintain backwards compatibility unless explicitly told otherwise
3. Run tests after each significant change
4. Document breaking changes clearly
5. Use br to track discovered work

Before making changes:
- Understand the existing code thoroughly
- Identify all callers/consumers
- Plan the migration path`,
				PerAgent: map[string]string{
					"cc:1": `You are the architect. Your role:
- Design the target architecture
- Identify migration steps
- Review changes from other agents
- Ensure consistency across the codebase`,
					"cc:2": `You are an implementer. Your role:
- Execute refactoring tasks assigned to you
- Write migration code
- Update tests to match new structure
- Report blockers to the architect`,
					"cc:3": `You are an implementer. Your role:
- Execute refactoring tasks assigned to you
- Write migration code
- Update tests to match new structure
- Report blockers to the architect`,
				},
				Delay: "5s",
			},
			FileReservations: FileReservationsSpec{
				Enabled:   true,
				Patterns:  []string{"**/*.go", "**/*.ts", "**/*.py"},
				Exclusive: boolPtr(true),
				TTL:       "1h",
			},
			Beads: BeadsSpec{
				AutoAssign: true,
				Recipe:     "actionable",
			},
			Options: SessionOptionsSpec{
				Stagger: &StaggerSpec{
					Enabled:  true,
					Interval: "10s",
				},
				AutoRestart: true,
			},
		},
	})

	// greenfield: For new project development
	RegisterBuiltinSessionTemplate(&SessionTemplate{
		APIVersion: "v1",
		Kind:       "SessionTemplate",
		Metadata: SessionTemplateMetadata{
			Name:        "greenfield",
			Description: "For starting new projects from scratch",
			Tags:        []string{"new", "project", "bootstrap"},
		},
		Spec: SessionTemplateSpec{
			Agents: AgentsSpec{
				Claude: &AgentTypeSpec{Count: 2, Model: "opus"},
				Codex:  &AgentTypeSpec{Count: 2},
			},
			Prompts: PromptsSpec{
				Initial: `You are building a new project from scratch. Read AGENTS.md first.

Guidelines:
1. Start with project structure and core abstractions
2. Establish coding standards early
3. Set up CI/CD pipeline
4. Write tests as you build
5. Document architectural decisions

Work collaboratively - communicate via Agent Mail when you need to coordinate.`,
				PerAgent: map[string]string{
					"cc": `You are responsible for:
- Core architecture and design
- API design and contracts
- Code review of implementations
- Documentation structure`,
					"cod": `You are responsible for:
- Feature implementation
- Unit and integration tests
- Build system setup
- Performance optimization`,
				},
				Delay: "3s",
			},
			CASS: CASSSpec{
				Enabled:     boolPtr(true),
				MaxSessions: 5,
			},
			Options: SessionOptionsSpec{
				Stagger: &StaggerSpec{
					Enabled:  true,
					Interval: "15s",
				},
				Checkpoint: &CheckpointSpec{
					Enabled:  true,
					Interval: "30m",
				},
			},
		},
	})

	// feature: For implementing new features in existing codebases
	RegisterBuiltinSessionTemplate(&SessionTemplate{
		APIVersion: "v1",
		Kind:       "SessionTemplate",
		Metadata: SessionTemplateMetadata{
			Name:        "feature",
			Description: "For implementing new features in existing codebases",
			Tags:        []string{"feature", "enhancement", "implementation"},
		},
		Spec: SessionTemplateSpec{
			Agents: AgentsSpec{
				Claude: &AgentTypeSpec{Count: 2, Model: "opus"},
				Codex:  &AgentTypeSpec{Count: 1},
			},
			Prompts: PromptsSpec{
				Initial: `You are a feature implementation team. Read AGENTS.md first.

Feature development workflow:
1. Understand the existing codebase architecture
2. Identify integration points for the new feature
3. Design the feature to fit existing patterns and conventions
4. Implement incrementally with tests
5. Update documentation as needed
6. Ensure backward compatibility

Use br to track feature tasks and sub-tasks.`,
				PerAgent: map[string]string{
					"cc": `You are the feature architect. Your role:
- Analyze how the feature fits into existing architecture
- Design the feature API and interfaces
- Review implementations for consistency
- Ensure the feature follows project conventions`,
					"cod": `You are a feature implementer. Your role:
- Implement feature components
- Write unit and integration tests
- Handle edge cases
- Optimize performance`,
				},
				Delay: "2s",
			},
			FileReservations: FileReservationsSpec{
				Enabled: true,
				TTL:     "1h",
			},
			CASS: CASSSpec{
				Enabled:     boolPtr(true),
				MaxSessions: 10,
			},
			Beads: BeadsSpec{
				AutoAssign: true,
				Filter:     "type:feature",
			},
			Options: SessionOptionsSpec{
				Stagger: &StaggerSpec{
					Enabled:  true,
					Interval: "10s",
				},
			},
		},
	})

	// bug-hunt: For debugging and bug fixing
	RegisterBuiltinSessionTemplate(&SessionTemplate{
		APIVersion: "v1",
		Kind:       "SessionTemplate",
		Metadata: SessionTemplateMetadata{
			Name:        "bug-hunt",
			Description: "For debugging and systematic bug fixing",
			Tags:        []string{"debug", "bug", "fix"},
		},
		Spec: SessionTemplateSpec{
			Agents: AgentsSpec{
				Claude: &AgentTypeSpec{Count: 1, Model: "opus"},
				Codex:  &AgentTypeSpec{Count: 2},
			},
			Prompts: PromptsSpec{
				Initial: `You are on a bug hunting team. Read AGENTS.md first.

Debugging methodology:
1. Reproduce the bug consistently
2. Isolate the root cause (not symptoms)
3. Write a failing test that demonstrates the bug
4. Fix the root cause
5. Verify the fix doesn't break other functionality
6. Document the fix and any related issues discovered

Use br to track bugs found during investigation.`,
				PerAgent: map[string]string{
					"cc": `You are the lead debugger. Your role:
- Analyze bug reports and stack traces
- Form hypotheses about root causes
- Direct investigation strategy
- Review proposed fixes`,
					"cod": `You are a bug hunter. Your role:
- Reproduce reported bugs
- Write regression tests
- Implement fixes
- Test edge cases`,
				},
				Delay: "2s",
			},
			CASS: CASSSpec{
				Enabled:     boolPtr(true),
				Query:       "bug error fix debug",
				MaxSessions: 10,
			},
			Beads: BeadsSpec{
				AutoAssign: true,
				Filter:     "type:bug",
			},
		},
	})

	// documentation: For documentation tasks
	RegisterBuiltinSessionTemplate(&SessionTemplate{
		APIVersion: "v1",
		Kind:       "SessionTemplate",
		Metadata: SessionTemplateMetadata{
			Name:        "documentation",
			Description: "For writing and improving documentation",
			Tags:        []string{"docs", "readme", "api-docs"},
		},
		Spec: SessionTemplateSpec{
			Agents: AgentsSpec{
				Claude: &AgentTypeSpec{Count: 2, Model: "sonnet"},
				Gemini: &AgentTypeSpec{Count: 1},
			},
			Prompts: PromptsSpec{
				Initial: `You are a documentation team. Read AGENTS.md first.

Documentation principles:
1. Write for the intended audience (user vs developer)
2. Include working examples
3. Keep docs close to code (update together)
4. Use clear, concise language
5. Add diagrams where they help understanding

Types of documentation to consider:
- README and getting started guides
- API reference documentation
- Architecture decision records (ADRs)
- Runbooks and troubleshooting guides`,
				PerAgent: map[string]string{
					"cc": `You focus on:
- Technical documentation (API docs, architecture)
- Code examples that actually work
- Ensuring accuracy of technical details`,
					"gmi": `You focus on:
- User-facing documentation
- Tutorials and guides
- Improving clarity and readability`,
				},
				Delay: "3s",
			},
			FileReservations: FileReservationsSpec{
				Enabled:  true,
				Patterns: []string{"**/*.md", "**/docs/**", "README*"},
				TTL:      "1h",
			},
		},
	})

	// swarm: For large-scale parallel work with many agents
	RegisterBuiltinSessionTemplate(&SessionTemplate{
		APIVersion: "v1",
		Kind:       "SessionTemplate",
		Metadata: SessionTemplateMetadata{
			Name:        "swarm",
			Description: "Large swarm of agents for parallel task execution",
			Tags:        []string{"swarm", "parallel", "scale"},
		},
		Spec: SessionTemplateSpec{
			Agents: AgentsSpec{
				Claude: &AgentTypeSpec{Count: 6, Model: "sonnet"},
				Codex:  &AgentTypeSpec{Count: 4},
			},
			Prompts: PromptsSpec{
				Initial: `You are part of an agent swarm. Read AGENTS.md first.

CRITICAL - Coordination rules:
1. Register with Agent Mail immediately
2. Reserve files before editing (use file_reservation_paths)
3. Check inbox regularly for coordination messages
4. Claim work via br before starting (br update <id> --status in_progress)
5. Keep other agents informed via Agent Mail

Work distribution:
- Use bv --robot-triage to find your next task
- One agent, one task at a time
- Report completion via br close <id>`,
				Delay: "5s",
			},
			FileReservations: FileReservationsSpec{
				Enabled:   true,
				Exclusive: boolPtr(true),
				TTL:       "30m",
			},
			Beads: BeadsSpec{
				AutoAssign: true,
				Recipe:     "actionable",
			},
			Options: SessionOptionsSpec{
				Stagger: &StaggerSpec{
					Enabled:  true,
					Interval: "5s",
				},
				AutoRestart: true,
			},
		},
	})

	// migration: For database or API migrations
	RegisterBuiltinSessionTemplate(&SessionTemplate{
		APIVersion: "v1",
		Kind:       "SessionTemplate",
		Metadata: SessionTemplateMetadata{
			Name:        "migration",
			Description: "For database schemas or API version migrations",
			Tags:        []string{"migration", "database", "api"},
		},
		Spec: SessionTemplateSpec{
			Agents: AgentsSpec{
				Claude: &AgentTypeSpec{Count: 2, Model: "opus"},
			},
			Prompts: PromptsSpec{
				Initial: `You are handling a migration. Read AGENTS.md first.

Migration safety rules:
1. NEVER run destructive commands without explicit approval
2. Always have a rollback plan
3. Test migrations on non-production data first
4. Document all changes in migration files
5. Verify data integrity after each step

Migration phases:
1. Analysis - understand current state
2. Planning - design migration steps
3. Testing - validate on test data
4. Execution - run with monitoring
5. Verification - confirm success`,
				PerAgent: map[string]string{
					"cc:1": `You are the migration planner. Your role:
- Analyze current schema/API
- Design migration steps
- Identify data transformation needs
- Plan rollback procedures`,
					"cc:2": `You are the migration implementer. Your role:
- Write migration scripts
- Create data transformation logic
- Test migrations thoroughly
- Document edge cases`,
				},
				Delay: "5s",
			},
			Environment: EnvironmentSpec{
				Env: map[string]string{
					"NTM_MIGRATION_MODE": "true",
				},
			},
		},
	})
}

// boolPtr is a helper to create a pointer to a bool value.
func boolPtr(b bool) *bool {
	return &b
}

// ExampleSessionTemplateYAML provides documentation and examples.
const ExampleSessionTemplateYAML = `# Session Template Example
# This template demonstrates all available configuration options.
apiVersion: v1
kind: SessionTemplate
metadata:
  name: full-stack-review
  description: Full-stack team for code review
  tags: [review, team, full-stack]
  author: NTM Team
  version: "1.0"
spec:
  agents:
    claude:
      count: 3
      model: opus
    codex:
      count: 1
    gemini:
      count: 1
    userPane: true
  prompts:
    initial: |
      You are part of a code review team.
      Focus on quality, security, and maintainability.
    delay: "5s"
    variables:
      project: my-project
  fileReservations:
    enabled: true
    patterns:
      - "**/*.go"
      - "**/*.ts"
    ttl: "2h"
  beads:
    recipe: actionable
    autoAssign: true
  cass:
    enabled: true
    maxSessions: 5
  environment:
    preSpawn:
      - name: build
        command: go build ./...
        timeout: "2m"
    env:
      DEBUG: "true"
  options:
    stagger:
      enabled: true
      interval: "30s"
    autoRestart: true
`

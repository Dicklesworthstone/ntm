package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// ParseError represents a validation or parsing error with location info
type ParseError struct {
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

func (e ParseError) Error() string {
	var parts []string
	if e.File != "" {
		parts = append(parts, e.File)
	}
	if e.Line > 0 {
		parts = append(parts, fmt.Sprintf("line %d", e.Line))
	}
	if e.Field != "" {
		parts = append(parts, e.Field)
	}

	location := strings.Join(parts, ":")
	if location != "" {
		return fmt.Sprintf("%s: %s", location, e.Message)
	}
	return e.Message
}

// ValidationResult contains the result of validating a workflow
type ValidationResult struct {
	Valid    bool         `json:"valid"`
	Errors   []ParseError `json:"errors,omitempty"`
	Warnings []ParseError `json:"warnings,omitempty"`
}

// ParseFile parses a workflow file (YAML or TOML) and returns the workflow
func ParseFile(path string) (*Workflow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	ext := strings.ToLower(filepath.Ext(path))
	var workflow Workflow

	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, &workflow); err != nil {
			return nil, &ParseError{
				File:    path,
				Message: fmt.Sprintf("YAML parse error: %v", err),
				Hint:    "Check YAML syntax - indentation and colons matter",
			}
		}
	case ".toml":
		if _, err := toml.Decode(string(data), &workflow); err != nil {
			return nil, &ParseError{
				File:    path,
				Message: fmt.Sprintf("TOML parse error: %v", err),
				Hint:    "Check TOML syntax - keys and values must be properly formatted",
			}
		}
	default:
		return nil, &ParseError{
			File:    path,
			Message: fmt.Sprintf("unsupported file extension: %s", ext),
			Hint:    "Use .yaml, .yml, or .toml extension",
		}
	}

	return &workflow, nil
}

// ParseString parses workflow from a string (auto-detects format)
func ParseString(content string, format string) (*Workflow, error) {
	var workflow Workflow

	switch strings.ToLower(format) {
	case "yaml", "yml":
		if err := yaml.Unmarshal([]byte(content), &workflow); err != nil {
			return nil, &ParseError{
				Message: fmt.Sprintf("YAML parse error: %v", err),
			}
		}
	case "toml":
		if _, err := toml.Decode(content, &workflow); err != nil {
			return nil, &ParseError{
				Message: fmt.Sprintf("TOML parse error: %v", err),
			}
		}
	default:
		return nil, &ParseError{
			Message: fmt.Sprintf("unsupported format: %s", format),
			Hint:    "Use 'yaml' or 'toml'",
		}
	}

	return &workflow, nil
}

// Validate validates a workflow and returns all errors found
func Validate(w *Workflow) ValidationResult {
	result := ValidationResult{Valid: true}

	// Required fields
	if w.SchemaVersion == "" {
		result.addError(ParseError{
			Field:   "schema_version",
			Message: "schema_version is required",
			Hint:    fmt.Sprintf("Add schema_version: \"%s\"", SchemaVersion),
		})
	} else if w.SchemaVersion != SchemaVersion {
		result.addWarning(ParseError{
			Field:   "schema_version",
			Message: fmt.Sprintf("schema version %s differs from current %s", w.SchemaVersion, SchemaVersion),
			Hint:    "Workflow may use features not available in this version",
		})
	}

	if w.Name == "" {
		result.addError(ParseError{
			Field:   "name",
			Message: "name is required",
			Hint:    "Add a unique name for this workflow",
		})
	}

	if len(w.Steps) == 0 {
		result.addError(ParseError{
			Field:   "steps",
			Message: "at least one step is required",
			Hint:    "Add steps to define the workflow",
		})
	}

	// Validate steps
	stepIDs := make(map[string]bool)
	for i, step := range w.Steps {
		validateStep(&step, fmt.Sprintf("steps[%d]", i), stepIDs, &result)
	}

	// Check for dependency cycles
	if cycles := detectCycles(w.Steps); len(cycles) > 0 {
		for _, cycle := range cycles {
			result.addError(ParseError{
				Field:   "depends_on",
				Message: fmt.Sprintf("circular dependency detected: %s", strings.Join(cycle, " -> ")),
				Hint:    "Remove one of the dependencies to break the cycle",
			})
		}
	}

	// Validate variable references
	validateVariableRefs(w, &result)

	return result
}

func (r *ValidationResult) addError(e ParseError) {
	r.Valid = false
	r.Errors = append(r.Errors, e)
}

func (r *ValidationResult) addWarning(e ParseError) {
	r.Warnings = append(r.Warnings, e)
}

func validateStep(step *Step, stepField string, stepIDs map[string]bool, result *ValidationResult) {

	// Required: ID
	if step.ID == "" {
		result.addError(ParseError{
			Field:   stepField + ".id",
			Message: "step id is required",
			Hint:    "Add a unique id for this step",
		})
	} else {
		// Check for valid ID format
		if !isValidID(step.ID) {
			result.addError(ParseError{
				Field:   stepField + ".id",
				Message: fmt.Sprintf("invalid step id: %s", step.ID),
				Hint:    "Use alphanumeric characters, underscores, and hyphens only",
			})
		}

		// Check for duplicate IDs
		if stepIDs[step.ID] {
			result.addError(ParseError{
				Field:   stepField + ".id",
				Message: fmt.Sprintf("duplicate step id: %s", step.ID),
				Hint:    "Each step must have a unique id",
			})
		}
		stepIDs[step.ID] = true
	}

	// Check for parallel vs prompt mutual exclusivity
	hasPrompt := step.Prompt != "" || step.PromptFile != ""
	hasParallel := len(step.Parallel) > 0

	if hasPrompt && hasParallel {
		result.addError(ParseError{
			Field:   stepField,
			Message: "step cannot have both prompt and parallel",
			Hint:    "Use prompt for single-agent steps, parallel for concurrent steps",
		})
	}

	if !hasPrompt && !hasParallel && step.Loop == nil {
		result.addError(ParseError{
			Field:   stepField,
			Message: "step must have prompt, prompt_file, or parallel",
			Hint:    "Add a prompt for this step",
		})
	}

	// Validate agent selection
	agentMethods := 0
	if step.Agent != "" {
		agentMethods++
		if !IsValidAgentType(step.Agent) {
			result.addWarning(ParseError{
				Field:   stepField + ".agent",
				Message: fmt.Sprintf("unknown agent type: %s", step.Agent),
				Hint:    "Valid types: claude, codex, gemini (and aliases)",
			})
		}
	}
	if step.Pane > 0 {
		agentMethods++
	}
	if step.Route != "" {
		agentMethods++
		if !isValidRoute(step.Route) {
			result.addError(ParseError{
				Field:   stepField + ".route",
				Message: fmt.Sprintf("invalid routing strategy: %s", step.Route),
				Hint:    "Valid strategies: least-loaded, first-available, round-robin",
			})
		}
	}

	if agentMethods > 1 {
		result.addError(ParseError{
			Field:   stepField,
			Message: "step can only use one of: agent, pane, route",
			Hint:    "Choose one agent selection method",
		})
	}

	// Validate prompt file exists (if specified)
	if step.PromptFile != "" {
		// Note: We only validate format, not existence (checked at runtime)
		if !isValidPath(step.PromptFile) {
			result.addWarning(ParseError{
				Field:   stepField + ".prompt_file",
				Message: fmt.Sprintf("prompt_file path may be invalid: %s", step.PromptFile),
			})
		}
	}

	// Validate error handling
	if step.OnError != "" && !isValidErrorAction(step.OnError) {
		result.addError(ParseError{
			Field:   stepField + ".on_error",
			Message: fmt.Sprintf("invalid on_error value: %s", step.OnError),
			Hint:    "Valid values: fail, continue, retry",
		})
	}

	if step.OnError == ErrorActionRetry && step.RetryCount == 0 {
		result.addWarning(ParseError{
			Field:   stepField + ".retry_count",
			Message: "on_error is retry but retry_count is 0",
			Hint:    "Set retry_count > 0 for retry to work",
		})
	}

	// Validate wait condition
	if step.Wait != "" && !isValidWaitCondition(step.Wait) {
		result.addError(ParseError{
			Field:   stepField + ".wait",
			Message: fmt.Sprintf("invalid wait condition: %s", step.Wait),
			Hint:    "Valid values: completion, idle, time, none",
		})
	}

	// Validate parallel sub-steps
	for j, pStep := range step.Parallel {
		validateStep(&pStep, fmt.Sprintf("%s.parallel[%d]", stepField, j), stepIDs, result)
	}

	// Validate loop configuration
	if step.Loop != nil {
		if step.Loop.Items == "" {
			result.addError(ParseError{
				Field:   stepField + ".loop.items",
				Message: "loop items is required",
				Hint:    "Specify the variable to iterate over",
			})
		}
		if step.Loop.MaxIterations < 0 {
			result.addError(ParseError{
				Field:   stepField + ".loop.max_iterations",
				Message: "max_iterations cannot be negative",
			})
		}
		for j, lStep := range step.Loop.Steps {
			validateStep(&lStep, fmt.Sprintf("%s.loop.steps[%d]", stepField, j), stepIDs, result)
		}
	}
}

// detectCycles finds circular dependencies in steps
func detectCycles(steps []Step) [][]string {
	// Build dependency graph
	graph := make(map[string][]string)
	for _, step := range steps {
		graph[step.ID] = step.DependsOn
		// Include parallel sub-steps
		for _, pStep := range step.Parallel {
			graph[pStep.ID] = pStep.DependsOn
		}
	}

	var cycles [][]string
	visited := make(map[string]bool)
	recStack := make(map[string]bool)
	path := make([]string, 0)

	var dfs func(node string)
	dfs = func(node string) {
		visited[node] = true
		recStack[node] = true
		path = append(path, node)

		for _, dep := range graph[node] {
			if !visited[dep] {
				dfs(dep)
			} else if recStack[dep] {
				// Found cycle - extract it
				cycleStart := -1
				for i, n := range path {
					if n == dep {
						cycleStart = i
						break
					}
				}
				if cycleStart >= 0 {
					// Create a new slice to avoid corrupting path's backing array
					cycle := make([]string, len(path)-cycleStart+1)
					copy(cycle, path[cycleStart:])
					cycle[len(cycle)-1] = dep
					cycles = append(cycles, cycle)
				}
				// Don't return early - continue to allow proper cleanup
			}
		}

		path = path[:len(path)-1]
		recStack[node] = false
	}

	for node := range graph {
		if !visited[node] {
			dfs(node)
		}
	}

	return cycles
}

// validateVariableRefs checks that variable references are valid
func validateVariableRefs(w *Workflow, result *ValidationResult) {
	varRefPattern := regexp.MustCompile(`\$\{([^}]+)\}`)

	var checkString func(s string, field string)
	checkString = func(s string, field string) {
		matches := varRefPattern.FindAllStringSubmatch(s, -1)
		for _, match := range matches {
			ref := match[1]
			parts := strings.Split(ref, ".")
			if len(parts) == 0 {
				continue
			}

			// Check reference type
			switch parts[0] {
			case "vars":
				if len(parts) < 2 {
					result.addWarning(ParseError{
						Field:   field,
						Message: fmt.Sprintf("incomplete variable reference: ${%s}", ref),
						Hint:    "Use ${vars.variable_name}",
					})
				}
			case "steps":
				if len(parts) < 3 {
					result.addWarning(ParseError{
						Field:   field,
						Message: fmt.Sprintf("incomplete step reference: ${%s}", ref),
						Hint:    "Use ${steps.step_id.output}",
					})
				}
			case "env", "session", "timestamp", "run_id", "workflow", "loop":
				// Valid built-in references
			default:
				result.addWarning(ParseError{
					Field:   field,
					Message: fmt.Sprintf("unknown reference type: ${%s}", ref),
					Hint:    "Valid types: vars, steps, env, session, timestamp, run_id, workflow",
				})
			}
		}
	}

	// Check all prompts and conditions
	for i, step := range w.Steps {
		stepField := fmt.Sprintf("steps[%d]", i)
		if step.Prompt != "" {
			checkString(step.Prompt, stepField+".prompt")
		}
		if step.When != "" {
			checkString(step.When, stepField+".when")
		}
		for j, pStep := range step.Parallel {
			pField := fmt.Sprintf("%s.parallel[%d]", stepField, j)
			if pStep.Prompt != "" {
				checkString(pStep.Prompt, pField+".prompt")
			}
			if pStep.When != "" {
				checkString(pStep.When, pField+".when")
			}
		}
	}
}

// Helper validation functions

func isValidID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func isValidRoute(r RoutingStrategy) bool {
	switch r {
	case RouteLeastLoaded, RouteFirstAvailable, RouteRoundRobin:
		return true
	}
	return false
}

func isValidErrorAction(a ErrorAction) bool {
	switch a {
	case ErrorActionFail, ErrorActionContinue, ErrorActionRetry:
		return true
	}
	return false
}

func isValidWaitCondition(w WaitCondition) bool {
	switch w {
	case WaitCompletion, WaitIdle, WaitTime, WaitNone:
		return true
	}
	return false
}

func isValidPath(p string) bool {
	// Basic path validation - not empty, no null bytes
	if p == "" {
		return false
	}
	for _, r := range p {
		if r == 0 {
			return false
		}
	}
	return true
}

// LoadAndValidate is a convenience function that parses and validates a workflow file
func LoadAndValidate(path string) (*Workflow, ValidationResult, error) {
	workflow, err := ParseFile(path)
	if err != nil {
		return nil, ValidationResult{Valid: false}, err
	}

	result := Validate(workflow)
	return workflow, result, nil
}

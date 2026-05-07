package pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// ParseError represents a validation or parsing error with location info
type ParseError struct {
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
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
		if e.Column > 0 {
			parts = append(parts, fmt.Sprintf("line %d, column %d", e.Line, e.Column))
		} else {
			parts = append(parts, fmt.Sprintf("line %d", e.Line))
		}
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

var (
	parseErrorLinePattern     = regexp.MustCompile(`(?i)\bline\s+([0-9]+)`)
	parseErrorColumnPattern   = regexp.MustCompile(`(?i)\bcolumn\s+([0-9]+)`)
	yamlUnknownFieldPattern   = regexp.MustCompile(`field ([^[:space:]]+) not found`)
	workflowSchemaDocsSection = "docs/WORKFLOW_SCHEMA.md"
)

func buildYAMLParseError(path string, err error) *ParseError {
	message := err.Error()
	var typeErr *yaml.TypeError
	if errors.As(err, &typeErr) && len(typeErr.Errors) > 0 {
		message = strings.Join(typeErr.Errors, "; ")
	}

	parseErr := &ParseError{
		File:    path,
		Message: fmt.Sprintf("YAML parse error: %s", message),
		Hint:    fmt.Sprintf("Check YAML syntax at the reported location and verify fields against %s", workflowSchemaDocsSection),
	}
	parseErr.Line, parseErr.Column = extractParseErrorLocation(message)
	if field := extractYAMLUnknownField(message); field != "" {
		parseErr.Field = field
		parseErr.Hint = fmt.Sprintf("Remove or rename %q; supported workflow fields are documented in %s", field, workflowSchemaDocsSection)
	}
	return parseErr
}

func buildTOMLParseError(path string, err error) *ParseError {
	parseErr := &ParseError{
		File:    path,
		Message: fmt.Sprintf("TOML parse error: %v", err),
		Hint:    fmt.Sprintf("Check TOML syntax at the reported location and verify fields against %s", workflowSchemaDocsSection),
	}

	var tomlErr toml.ParseError
	if errors.As(err, &tomlErr) {
		if tomlErr.Message != "" {
			parseErr.Message = fmt.Sprintf("TOML parse error: %s", tomlErr.Message)
		}
		parseErr.Line = tomlErr.Position.Line
		parseErr.Column = tomlErr.Position.Col
		parseErr.Field = tomlErr.LastKey
		if tomlErr.Usage != "" {
			parseErr.Hint = tomlErr.Usage
		}
	}
	return parseErr
}

func buildUnknownTOMLFieldError(path string, undecoded []toml.Key) *ParseError {
	field := undecoded[0].String()
	return &ParseError{
		File:    path,
		Field:   field,
		Message: fmt.Sprintf("unknown TOML field(s): %s", formatUndecodedTOMLKeys(undecoded)),
		Hint:    fmt.Sprintf("Remove or rename %q; supported workflow fields are documented in %s", field, workflowSchemaDocsSection),
	}
}

func extractParseErrorLocation(message string) (int, int) {
	return firstIntSubmatch(parseErrorLinePattern, message), firstIntSubmatch(parseErrorColumnPattern, message)
}

func firstIntSubmatch(pattern *regexp.Regexp, s string) int {
	match := pattern.FindStringSubmatch(s)
	if len(match) < 2 {
		return 0
	}
	n, err := strconv.Atoi(match[1])
	if err != nil {
		return 0
	}
	return n
}

func extractYAMLUnknownField(message string) string {
	match := yamlUnknownFieldPattern.FindStringSubmatch(message)
	if len(match) < 2 {
		return ""
	}
	return strings.Trim(match[1], "`\"'")
}

// ValidationResult contains the result of validating a workflow
type ValidationResult struct {
	Valid    bool         `json:"valid"`
	Errors   []ParseError `json:"errors,omitempty"`
	Warnings []ParseError `json:"warnings,omitempty"`
}

func parseYAMLWorkflow(data []byte, workflow *Workflow) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(workflow); err != nil {
		if err == io.EOF {
			return nil
		}
		return err
	}
	return nil
}

func formatUndecodedTOMLKeys(keys []toml.Key) string {
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key.String())
	}
	return strings.Join(parts, ", ")
}

func filterUndecodedTOMLKeys(keys []toml.Key) []toml.Key {
	filtered := make([]toml.Key, 0, len(keys))
	for _, key := range keys {
		if isKnownParallelInlineStepKey(key) {
			continue
		}
		filtered = append(filtered, key)
	}
	return filtered
}

// isKnownParallelInlineStepKey reports whether an undecoded key is a leftover
// from the canonical inline-step shape `[[steps.parallel.steps]]`. After
// ParallelSpec.UnmarshalTOML consumes those entries via JSON round-trip, the
// BurntSushi decoder still considers them undecoded because UnmarshalTOML
// cannot mark child keys as consumed; we suppress only those (and only when
// they match a known step field) so genuine config errors still surface.
//
// bd-k44ib: previously this matched any `steps.parallel.<known-step-field>`
// key, which silently swallowed malformed `[steps.parallel] id=... prompt=...`
// shapes. The narrowed check now requires the path to begin with the
// canonical `steps.parallel.steps.*` so unrelated shapes get a real error.
func isKnownParallelInlineStepKey(key toml.Key) bool {
	if len(key) < 4 || key[0] != "steps" || key[1] != "parallel" || key[2] != "steps" {
		return false
	}
	return knownStepTOMLFields[key[3]]
}

var knownStepTOMLFields = map[string]bool{
	"id":                       true,
	"name":                     true,
	"description":              true,
	"agent":                    true,
	"pane":                     true,
	"route":                    true,
	"prompt":                   true,
	"prompt_file":              true,
	"command":                  true,
	"args":                     true,
	"template":                 true,
	"params":                   true,
	"template_params":          true,
	"wait":                     true,
	"timeout":                  true,
	"depends_on":               true,
	"after":                    true,
	"on_error":                 true,
	"on_failure":               true,
	"on_success":               true,
	"retry_count":              true,
	"retry_delay":              true,
	"retry_backoff":            true,
	"when":                     true,
	"branch":                   true,
	"branches":                 true,
	"bead_query":               true,
	"output_var":               true,
	"output_parse":             true,
	"parallel":                 true,
	"loop":                     true,
	"loop_control":             true,
	"foreach":                  true,
	"foreach_pane":             true,
	"mail_send":                true,
	"file_reservation_paths":   true,
	"mail_inbox_check":         true,
	"file_reservation_release": true,
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
		if err := parseYAMLWorkflow(data, &workflow); err != nil {
			return nil, buildYAMLParseError(path, err)
		}
	case ".toml":
		md, err := toml.Decode(string(data), &workflow)
		if err != nil {
			return nil, buildTOMLParseError(path, err)
		}
		if undecoded := filterUndecodedTOMLKeys(md.Undecoded()); len(undecoded) > 0 {
			return nil, buildUnknownTOMLFieldError(path, undecoded)
		}
	default:
		return nil, &ParseError{
			File:    path,
			Message: fmt.Sprintf("unsupported file extension: %s", ext),
			Hint:    "Use .yaml, .yml, or .toml extension",
		}
	}

	// Fold alias / convenience fields into their canonical counterparts so
	// downstream validation, deps, and execution only need one path.
	workflow.Normalize()
	return &workflow, nil
}

// ParseString parses workflow from a string (auto-detects format)
func ParseString(content string, format string) (*Workflow, error) {
	var workflow Workflow

	switch strings.ToLower(format) {
	case "yaml", "yml":
		if err := parseYAMLWorkflow([]byte(content), &workflow); err != nil {
			return nil, buildYAMLParseError("", err)
		}
	case "toml":
		md, err := toml.Decode(content, &workflow)
		if err != nil {
			return nil, buildTOMLParseError("", err)
		}
		if undecoded := filterUndecodedTOMLKeys(md.Undecoded()); len(undecoded) > 0 {
			return nil, buildUnknownTOMLFieldError("", undecoded)
		}
	default:
		return nil, &ParseError{
			Message: fmt.Sprintf("unsupported format: %s", format),
			Hint:    "Use 'yaml' or 'toml'",
		}
	}

	workflow.Normalize()
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

	validateSettings(w.Settings, &result)

	// Validate steps
	stepIDs := make(map[string]bool)
	for i, step := range w.Steps {
		validateStep(&step, fmt.Sprintf("steps[%d]", i), stepIDs, &result)
	}

	// bd-tpz1a: validate settings.on_cancel cleanup steps with the same rules
	// and the same global step-ID namespace as normal steps. Without this,
	// (a) malformed cleanup steps (no kind, missing required fields) only fail
	// during cancellation cleanup at runtime and (b) cleanup IDs that collide
	// with a normal step ID silently overwrite the cancelled step's persisted
	// result inside runOnCancelSteps. validateOnCancelSteps assigns synthetic
	// "on_cancel_N" IDs to bare entries before validation so duplicates with
	// regular steps using those names are still detected.
	validateOnCancelSteps(w.Settings.OnCancel, stepIDs, &result)

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

func validateSettings(settings WorkflowSettings, result *ValidationResult) {
	if settings.OnError != "" && !isValidErrorAction(settings.OnError) {
		result.addError(ParseError{
			Field:   "settings.on_error",
			Message: fmt.Sprintf("invalid on_error value: %s", settings.OnError),
			Hint:    "Valid values: fail, fail_fast, continue, retry",
		})
	}
}

// validateOnCancelSteps applies the regular validateStep rules to cleanup
// steps declared under settings.on_cancel and registers their IDs in the
// workflow-wide stepIDs map so collisions with regular step IDs surface as
// validation errors. Empty IDs get the synthetic "on_cancel_N" identity that
// runOnCancelSteps already assigns at runtime, keeping validation and
// execution aligned.
func validateOnCancelSteps(onCancel []Step, stepIDs map[string]bool, result *ValidationResult) {
	for i := range onCancel {
		step := onCancel[i]
		if step.ID == "" {
			step.ID = fmt.Sprintf("on_cancel_%d", i+1)
		}
		validateStep(&step, fmt.Sprintf("settings.on_cancel[%d]", i), stepIDs, result)
	}
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

	// Check for parallel vs prompt mutual exclusivity. Each step must do one
	// (and only one) of: agent prompt, shell command, template render, foreach
	// fan-out, parallel sub-steps, loop, or branch dispatch. Treat `parallel:
	// true` as a flag rather than work; only inline parallel sub-steps count.
	hasPrompt := step.Prompt != "" || step.PromptFile != ""
	hasCommand := step.Command != ""
	hasTemplate := step.Template != ""
	hasParallel := len(step.Parallel.Steps) > 0
	hasForeach := step.Foreach != nil || step.ForeachPane != nil
	hasBranch := step.Branch != "" || len(step.Branches) > 0
	hasBeadQuery := step.BeadQuery != nil
	mailStepKinds := step.mailStepKindNames()
	hasMailStep := len(mailStepKinds) > 0

	if hasPrompt && hasParallel {
		result.addError(ParseError{
			Field:   stepField,
			Message: "step cannot have both prompt and parallel",
			Hint:    "Use prompt for single-agent steps, parallel for concurrent steps",
		})
	}
	if hasPrompt && hasCommand {
		result.addError(ParseError{
			Field:   stepField,
			Message: "step cannot have both prompt and command",
			Hint:    "Use prompt for agent dispatch, command for shell execution",
		})
	}
	if hasPrompt && hasTemplate {
		result.addError(ParseError{
			Field:   stepField,
			Message: "step cannot have both prompt and template",
			Hint:    "Use prompt for inline text, template for an MO file rendered with --params",
		})
	}
	if hasCommand && hasTemplate {
		result.addError(ParseError{
			Field:   stepField,
			Message: "step cannot have both command and template",
			Hint:    "Pick one: command runs a shell command; template renders an MO and dispatches it",
		})
	}
	if len(mailStepKinds) > 1 {
		result.addError(ParseError{
			Field:   stepField,
			Message: fmt.Sprintf("step can only use one Agent Mail step kind, got: %s", strings.Join(mailStepKinds, ", ")),
			Hint:    "Choose one of mail_send, file_reservation_paths, mail_inbox_check, or file_reservation_release",
		})
	}
	if hasMailStep && (hasPrompt || hasParallel || hasCommand || hasTemplate || hasForeach || hasBranch || step.Loop != nil) {
		result.addError(ParseError{
			Field:   stepField,
			Message: "step cannot combine Agent Mail step kind with prompt, command, template, parallel, loop, foreach, or branch",
			Hint:    "Agent Mail step kinds run through MCP Agent Mail rather than tmux pane dispatch",
		})
	}
	if hasBeadQuery && (hasPrompt || hasParallel || hasCommand || hasTemplate || hasForeach || hasBranch || hasMailStep || step.Loop != nil) {
		result.addError(ParseError{
			Field:   stepField,
			Message: "step cannot combine bead_query with prompt, command, template, parallel, loop, foreach, branch, or Agent Mail step kinds",
			Hint:    "bead_query runs a structured br query and stores its result through output_var",
		})
	}

	// Loop-control-only steps are a first-class step kind for foreach/loop
	// bodies (bd-oqv4c). The runtime already supports
	// `{loop_control: break/continue, when: ...}` via foreachControlOnlyStep
	// in foreach.go; validation must accept it without requiring the author
	// to attach loop_control to an unrelated work step.
	hasLoopControlOnly := step.LoopControl == LoopControlBreak || step.LoopControl == LoopControlContinue

	if !hasPrompt && !hasParallel && !hasCommand && !hasTemplate &&
		!hasForeach && !hasBranch && !hasBeadQuery && !hasMailStep &&
		!hasLoopControlOnly && step.Loop == nil {
		result.addError(ParseError{
			Field:   stepField,
			Message: "step must have prompt, prompt_file, command, template, parallel, loop, foreach, branch, bead_query, or loop_control",
			Hint:    "Pick the step kind that matches the work you want done.",
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
				Hint:    "Valid types: claude, codex, gemini, cursor, windsurf, aider, ollama (and aliases)",
			})
		}
	}
	if !step.Pane.IsZero() {
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
			Hint:    "Valid values: fail, fail_fast, continue, retry",
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

	validateOutputVarCollisions(step, stepField, result)

	// Validate parallel sub-steps
	for j, pStep := range step.Parallel.Steps {
		validateStep(&pStep, fmt.Sprintf("%s.parallel[%d]", stepField, j), stepIDs, result)
	}

	// Validate loop configuration
	if step.Loop != nil {
		if step.Loop.Items == "" && step.Loop.While == "" && step.Loop.Until == "" && step.Loop.Times <= 0 {
			result.addError(ParseError{
				Field:   stepField + ".loop",
				Message: "loop must specify items, while, until, or times",
				Hint:    "Set items for for-each, while for condition loop, until for predicate-exit loop, or times for count loop",
			})
		}
		if step.Loop.MaxIterations.Value < 0 {
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

	var addToGraph func(steps []Step)
	addToGraph = func(steps []Step) {
		for _, step := range steps {
			graph[step.ID] = step.DependsOn
			// Include parallel sub-steps
			if len(step.Parallel.Steps) > 0 {
				addToGraph(step.Parallel.Steps)
			}
			// Include loop sub-steps
			if step.Loop != nil {
				addToGraph(step.Loop.Steps)
			}
		}
	}

	addToGraph(steps)

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
	// Uses package-level varPattern from variables.go
	checkString := func(s, field string) {
		matches := varPattern.FindAllStringSubmatch(s, -1)
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

	// Check all prompts and conditions recursively
	var checkSteps func(steps []Step, prefix string)
	checkSteps = func(steps []Step, prefix string) {
		for i, step := range steps {
			stepField := fmt.Sprintf("%s[%d]", prefix, i)
			if step.Prompt != "" {
				checkString(step.Prompt, stepField+".prompt")
			}
			if step.When != "" {
				checkString(step.When, stepField+".when")
			}
			// Check parallel sub-steps
			if len(step.Parallel.Steps) > 0 {
				checkSteps(step.Parallel.Steps, stepField+".parallel")
			}
			// Check loop sub-steps
			if step.Loop != nil {
				checkSteps(step.Loop.Steps, stepField+".loop.steps")
			}
		}
	}

	checkSteps(w.Steps, "steps")
}

// Helper validation functions

func isValidID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
			(r < '0' || r > '9') && r != '_' && r != '-' {
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
	case ErrorActionFail, ErrorActionFailFast, ErrorActionContinue, ErrorActionRetry:
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

// Package pipeline provides workflow execution for AI agent orchestration.
// variables.go implements variable substitution and output parsing for workflows.
package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// PrepareWorkflowVariables applies runtime overrides and workflow defaults,
// validating declared variable types as values enter the execution state.
func PrepareWorkflowVariables(workflow *Workflow, overrides map[string]interface{}) (map[string]interface{}, error) {
	if workflow == nil {
		return nil, fmt.Errorf("workflow is nil")
	}

	vars := make(map[string]interface{}, len(workflow.Vars)+len(overrides))
	for name, val := range overrides {
		if def, ok := workflow.Vars[name]; ok {
			normalized, err := normalizeWorkflowVar(name, def.Type, val)
			if err != nil {
				return nil, err
			}
			vars[name] = normalized
			continue
		}
		vars[name] = val
	}

	resolving := make(map[string]bool)
	resolved := make(map[string]bool)
	var resolveDefault func(string) error
	resolveDefault = func(name string) error {
		if _, ok := vars[name]; ok || resolved[name] {
			return nil
		}
		def, ok := workflow.Vars[name]
		if !ok || def.Default == nil {
			resolved[name] = true
			return nil
		}
		if resolving[name] {
			return fmt.Errorf("variable %s: cyclic default reference", name)
		}
		resolving[name] = true
		defer delete(resolving, name)

		value := def.Default
		if s, ok := value.(string); ok {
			for _, ref := range variableDefaultRefs(s) {
				if err := resolveDefault(ref); err != nil {
					return err
				}
			}
			resolved, err := resolveDefaultString(s, vars)
			if err != nil {
				return fmt.Errorf("variable %s: default %q: %w", name, s, err)
			}
			value = resolved
		}

		normalized, err := normalizeWorkflowVar(name, def.Type, value)
		if err != nil {
			return err
		}
		vars[name] = normalized
		resolved[name] = true
		return nil
	}

	for name := range workflow.Vars {
		if err := resolveDefault(name); err != nil {
			return nil, err
		}
	}
	for name, def := range workflow.Vars {
		if _, ok := vars[name]; !ok && def.Required {
			return nil, fmt.Errorf("variable %s: required value missing", name)
		}
	}

	return vars, nil
}

// VariableValidationResult captures normalized runtime variables plus non-fatal
// input warnings that callers can surface before execution starts.
type VariableValidationResult struct {
	Variables map[string]interface{}
	Warnings  []ParseError
}

// ValidateWorkflowVariables checks runtime overrides against declared workflow
// variable metadata. Undeclared overrides remain available but are reported as
// warnings because they cannot receive VarType validation.
func ValidateWorkflowVariables(workflow *Workflow, overrides map[string]interface{}) (*VariableValidationResult, *ParseError) {
	result := &VariableValidationResult{}
	if workflow != nil {
		for name := range overrides {
			if _, ok := workflow.Vars[name]; ok {
				continue
			}
			result.Warnings = append(result.Warnings, ParseError{
				Field:   fmt.Sprintf("vars.%s", name),
				Message: fmt.Sprintf("undeclared variable %q; will be available but not validated", name),
				Hint:    "Declare the variable under workflow vars to enable type validation.",
			})
		}
	}

	vars, err := PrepareWorkflowVariables(workflow, overrides)
	if err != nil {
		return result, workflowVariableParseError(err)
	}
	result.Variables = vars
	return result, nil
}

func workflowVariableParseError(err error) *ParseError {
	message := err.Error()
	name := workflowVariableNameFromError(message)
	field := "vars"
	if name != "" {
		field = fmt.Sprintf("vars.%s", name)
	}
	return &ParseError{
		Field:   field,
		Message: message,
		Hint:    workflowVariableErrorHint(name, message),
	}
}

func workflowVariableNameFromError(message string) string {
	rest, ok := strings.CutPrefix(message, "variable ")
	if !ok {
		return ""
	}
	name, _, ok := strings.Cut(rest, ":")
	if !ok {
		return ""
	}
	return strings.TrimSpace(name)
}

func workflowVariableErrorHint(name, message string) string {
	switch {
	case strings.Contains(message, "expected number"):
		if name == "" {
			return "Use a parseable number such as 5 or 5.0."
		}
		return fmt.Sprintf("use --var %s=5 or --var %s=5.0", name, name)
	case strings.Contains(message, "expected boolean"):
		if name == "" {
			return "Use true/false, 1/0, or yes/no."
		}
		return fmt.Sprintf("use --var %s=true or --var %s=false", name, name)
	case strings.Contains(message, "expected array"):
		if name == "" {
			return "Use comma-separated CLI input or a JSON array in --var-file."
		}
		return fmt.Sprintf("use --var %s=a,b,c or provide %q as a JSON array in --var-file", name, name)
	case strings.Contains(message, "required value missing"):
		if name == "" {
			return "Provide required variables with --var or --var-file."
		}
		return fmt.Sprintf("provide --var %s=value or include %q in --var-file", name, name)
	case strings.Contains(message, "cyclic default reference"):
		return "Remove the cycle between workflow var defaults."
	case strings.Contains(message, "unknown declared type"):
		return "Use one of: string, number, boolean, array."
	default:
		return "Fix the workflow variable value and retry."
	}
}

func variableDefaultRefs(s string) []string {
	matches := varPattern.FindAllStringSubmatch(s, -1)
	refs := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		ref := strings.TrimSpace(match[1])
		ref, _, _ = parseDefault(ref)
		parts := strings.Split(ref, ".")
		if len(parts) >= 2 && parts[0] == "vars" {
			refs = append(refs, parts[1])
		}
	}
	return refs
}

// resolveDefaultString substitutes variable references inside a string-typed
// workflow var default. Returns an error when the default contains an
// unresolved reference (typo, missing variable, missing env) without an
// explicit | fallback. Without this, a default like ${vars.projet_name}
// would pass through as the literal placeholder and silently dispatch
// unresolved ${...} markers to prompts/commands at runtime.
func resolveDefaultString(s string, vars map[string]interface{}) (interface{}, error) {
	state := &ExecutionState{Variables: vars}
	sub := NewSubstitutor(state, "", "")
	trimmed := strings.TrimSpace(s)
	if match := varPattern.FindStringSubmatch(trimmed); len(match) == 2 && match[0] == trimmed {
		if value, err := sub.resolveVar(match[1]); err == nil {
			return value, nil
		}
	}
	resolved, err := sub.SubstituteStrict(s)
	if err != nil {
		return nil, err
	}
	return resolved, nil
}

func normalizeWorkflowVar(name string, typ VarType, value interface{}) (interface{}, error) {
	switch typ {
	case "", VarTypeString:
		return value, nil
	case VarTypeNumber:
		return normalizeNumberVar(name, value)
	case VarTypeBoolean:
		return normalizeBooleanVar(name, value)
	case VarTypeArray:
		return normalizeArrayVar(name, value)
	default:
		return nil, fmt.Errorf("variable %s: unknown declared type %q", name, typ)
	}
}

func normalizeNumberVar(name string, value interface{}) (interface{}, error) {
	switch v := value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return value, nil
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i, nil
		}
		f, err := v.Float64()
		if err != nil {
			return nil, fmt.Errorf("variable %s: expected number, got '%s'", name, v.String())
		}
		return f, nil
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return nil, fmt.Errorf("variable %s: expected number, got ''", name)
		}
		if !strings.ContainsAny(s, ".eE") {
			if i, err := strconv.ParseInt(s, 10, 64); err == nil {
				return int(i), nil
			}
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("variable %s: expected number, got '%s'", name, v)
		}
		return f, nil
	default:
		return nil, fmt.Errorf("variable %s: expected number, got %T", name, value)
	}
}

func normalizeBooleanVar(name string, value interface{}) (interface{}, error) {
	switch v := value.(type) {
	case bool:
		return v, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "t", "1", "yes", "y":
			return true, nil
		case "false", "f", "0", "no", "n":
			return false, nil
		default:
			return nil, fmt.Errorf("variable %s: expected boolean, got '%s'", name, v)
		}
	default:
		return nil, fmt.Errorf("variable %s: expected boolean, got %T", name, value)
	}
}

func normalizeArrayVar(name string, value interface{}) (interface{}, error) {
	switch v := value.(type) {
	case []interface{}:
		return v, nil
	case []string:
		return v, nil
	case string:
		if strings.TrimSpace(v) == "" {
			return []string{}, nil
		}
		parts := strings.Split(v, ",")
		items := make([]string, 0, len(parts))
		for _, part := range parts {
			items = append(items, strings.TrimSpace(part))
		}
		return items, nil
	default:
		return nil, fmt.Errorf("variable %s: expected array, got %T", name, value)
	}
}

// Substitutor handles variable substitution in workflow prompts and conditions.
// It supports multiple variable types: vars, steps, env, and context variables.
type Substitutor struct {
	state    *ExecutionState
	session  string
	workflow string
	defaults map[string]interface{}
}

// NewSubstitutor creates a new substitutor with the given execution context.
func NewSubstitutor(state *ExecutionState, session, workflow string) *Substitutor {
	return &Substitutor{
		state:    state,
		session:  session,
		workflow: workflow,
	}
}

// SetDefaults sets the workflow-level defaults map for ${defaults.X} resolution.
func (s *Substitutor) SetDefaults(d map[string]interface{}) {
	s.defaults = d
}

// SubstitutionError represents an error during variable substitution
type SubstitutionError struct {
	VarRef  string // The variable reference that failed
	Message string // Error description
}

func (e *SubstitutionError) Error() string {
	return fmt.Sprintf("variable substitution error for '%s': %s", e.VarRef, e.Message)
}

// varPattern matches variable references: ${...}
// Group 1: full expression (may include default value)
var varPattern = regexp.MustCompile(`(?s)\$\{([^}]+)\}`)

// escapedPattern matches escaped variable references: \${...}
var escapedPattern = regexp.MustCompile(`\\\$\{`)

// escapedDollarPattern matches escaped literal dollar signs: \$.
var escapedDollarPattern = regexp.MustCompile(`\\\$`)

// placeholder for escaped variable starts during substitution
const escapePlaceholder = "\x00ESC_VAR\x00"

// placeholder for escaped literal dollar signs during substitution
const escapedDollarPlaceholder = "\x00ESC_DOLLAR\x00"

// Substitute replaces all ${...} variable references in the template string.
// Returns the substituted string and any errors encountered.
func (s *Substitutor) Substitute(template string) (string, error) {
	result := protectEscapedSubstitutions(template)
	var firstErr error

	for depth := 0; depth < DefaultMaxSubstitutionDepth; depth++ {
		next, err, resolved := s.substituteOnce(result)
		next = protectEscapedSubstitutions(next)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return restoreEscapedSubstitutions(next), firstErr
		}

		result = next
		if !varPattern.MatchString(result) {
			return restoreEscapedSubstitutions(result), firstErr
		}
		if resolved == 0 {
			return restoreEscapedSubstitutions(result), firstErr
		}
	}

	match := varPattern.FindString(result)
	varRef := match
	if strings.HasPrefix(match, "${") && strings.HasSuffix(match, "}") {
		varRef = strings.TrimSpace(match[2 : len(match)-1])
	}
	return restoreEscapedSubstitutions(result), &SubstitutionError{
		VarRef:  varRef,
		Message: fmt.Sprintf("substitution recursion depth exceeded after %d passes", DefaultMaxSubstitutionDepth),
	}
}

func (s *Substitutor) substituteOnce(template string) (string, error, int) {
	var firstErr error
	resolved := 0

	result := varPattern.ReplaceAllStringFunc(template, func(match string) string {
		// Extract expression between ${ and }
		expr := match[2 : len(match)-1]

		// Parse for default value: ${var | "default"} or ${var | default}
		varPath, defaultVal, hasDefault := parseDefault(expr)

		// Resolve the variable
		value, err := s.resolveVar(varPath)
		if err != nil {
			if hasDefault {
				// bd-wwmo9: count the default fallback as progress so the
				// outer Substitute loop keeps recursing when the default
				// itself contains a variable reference, e.g.
				// ${vars.missing | ${vars.present}}. Without this the
				// outer loop sees `resolved == 0` and exits early,
				// returning the literal ${vars.present} instead of its
				// resolved value.
				if defaultVal != match {
					resolved++
				}
				return defaultVal
			}
			if firstErr == nil {
				firstErr = &SubstitutionError{VarRef: varPath, Message: err.Error()}
			}
			return match // Leave unsubstituted if resolution fails
		}

		resolved++
		formatted := formatValue(value)
		// bd-447se: values resolved from untrusted namespaces (step output,
		// process env, bead_query) must not be re-processed by later passes
		// of the substitution loop. Otherwise a step that prints
		// `${env.GITHUB_TOKEN}` and a downstream `${steps.A.output}` form a
		// secret-disclosure / control-flow injection vector. Replace any
		// `${` sequences in the substituted value with the escape
		// placeholder so the outer Substitute loop sees them as opaque
		// data; restoreEscapedSubstitutions converts them back to literal
		// `${` in the final output.
		if isTerminalSubstitutionNamespace(varPath) {
			formatted = sealTerminalValue(formatted)
		}
		return formatted
	})

	return result, firstErr, resolved
}

// isTerminalSubstitutionNamespace reports whether values resolved from this
// variable path should be treated as opaque data rather than recursively
// re-substituted. Author-controlled namespaces (vars, defaults, loop, pane,
// runtime, session/timestamp/run_id/workflow) keep recursive expansion;
// untrusted/external namespaces (step output, process env, bead query
// records, agent output) are sealed to prevent
// secret-disclosure / control-flow injection through external data
// (bd-447se).
func isTerminalSubstitutionNamespace(varPath string) bool {
	path := strings.TrimSpace(varPath)
	if path == "" {
		return false
	}
	root := path
	if idx := strings.IndexAny(path, ".["); idx >= 0 {
		root = path[:idx]
	}
	switch root {
	case "steps", "env", "bead_query", "agent":
		return true
	}
	return false
}

// sealTerminalValue replaces every ${ in v with escapePlaceholder so the
// next substitution pass treats it as data. Final restoreEscapedSubstitutions
// reverts placeholders to literal ${ for output.
func sealTerminalValue(v string) string {
	if !strings.Contains(v, "${") {
		return v
	}
	return strings.ReplaceAll(v, "${", escapePlaceholder)
}

func protectEscapedSubstitutions(template string) string {
	result := escapedPattern.ReplaceAllString(template, escapePlaceholder)
	return escapedDollarPattern.ReplaceAllString(result, escapedDollarPlaceholder)
}

func restoreEscapedSubstitutions(template string) string {
	result := strings.ReplaceAll(template, escapePlaceholder, "${")
	return strings.ReplaceAll(result, escapedDollarPlaceholder, "$")
}

// SubstituteStrict is like Substitute but returns an error if any variable is undefined.
func (s *Substitutor) SubstituteStrict(template string) (string, error) {
	result, err := s.Substitute(template)
	if err != nil {
		return "", err
	}

	// Check for any remaining unsubstituted variables
	if varPattern.MatchString(result) {
		matches := varPattern.FindAllString(result, -1)
		return "", &SubstitutionError{
			VarRef:  matches[0],
			Message: "undefined variable (no default provided)",
		}
	}

	return result, nil
}

// parseDefault extracts variable path and optional default value.
// Supports: ${var | "default"}, ${var | 'default'}, ${var | default}
func parseDefault(expr string) (varPath, defaultVal string, hasDefault bool) {
	// Look for | delimiter (not inside nested structures)
	pipeIdx := strings.Index(expr, "|")
	if pipeIdx == -1 {
		return strings.TrimSpace(expr), "", false
	}

	varPath = strings.TrimSpace(expr[:pipeIdx])
	defaultPart := strings.TrimSpace(expr[pipeIdx+1:])

	// Strip quotes if present
	if len(defaultPart) >= 2 {
		if (defaultPart[0] == '"' && defaultPart[len(defaultPart)-1] == '"') ||
			(defaultPart[0] == '\'' && defaultPart[len(defaultPart)-1] == '\'') {
			defaultPart = defaultPart[1 : len(defaultPart)-1]
		}
	}

	return varPath, defaultPart, true
}

// resolveVar resolves a variable reference path to its value.
// Supported paths:
//   - vars.name, vars.name.nested.field
//   - steps.id.output, steps.id.data.field
//   - steps.id.pane, steps.id.duration, steps.id.status, steps.id.agent
//   - env.NAME
//   - pane.role, pane.model, pane.domain, pane.index
//   - session, timestamp, run_id, workflow
//   - loop.item, loop.index, loop.count, loop.first, loop.last
func (s *Substitutor) resolveVar(path string) (interface{}, error) {
	path = strings.TrimSpace(path)
	parts := strings.Split(path, ".")

	if len(parts) == 0 || parts[0] == "" {
		return nil, fmt.Errorf("empty variable reference")
	}

	switch parts[0] {
	case "vars":
		return s.resolveVars(parts[1:])
	case "steps":
		return s.resolveSteps(parts[1:])
	case "env":
		return s.resolveEnv(parts[1:])
	case "loop":
		return s.resolveLoop(parts[1:])
	case "defaults":
		return s.resolveDefaults(parts[1:])
	case "item":
		return s.resolveItem(parts[1:])
	case "pane":
		return s.resolvePane(parts[1:])
	case "session":
		return s.session, nil
	case "run_id":
		if s.state != nil {
			return s.state.RunID, nil
		}
		return "", nil
	case "timestamp":
		return time.Now().Format(time.RFC3339), nil
	case "workflow":
		return s.workflow, nil
	default:
		return nil, fmt.Errorf("unknown variable namespace: %s", parts[0])
	}
}

// resolveVars handles vars.X and vars.X.nested.field references
func (s *Substitutor) resolveVars(parts []string) (interface{}, error) {
	if len(parts) == 0 {
		return nil, fmt.Errorf("vars requires a variable name")
	}
	if s.state == nil || s.state.Variables == nil {
		return nil, fmt.Errorf("no variables context")
	}

	// Get the root variable
	varName := parts[0]
	value, ok := s.state.Variables[varName]
	if !ok {
		return nil, fmt.Errorf("undefined variable: %s", varName)
	}

	// Navigate nested fields
	if len(parts) > 1 {
		return navigateNested(value, parts[1:])
	}

	return value, nil
}

// resolveSteps handles steps.X.output, steps.X.data.field, steps.X.status, etc.
func (s *Substitutor) resolveSteps(parts []string) (interface{}, error) {
	if len(parts) < 2 {
		return nil, fmt.Errorf("steps requires step ID and field")
	}
	if s.state == nil {
		return nil, fmt.Errorf("no execution state")
	}

	stepID := parts[0]
	field := parts[1]
	rest := parts[2:]
	if base, indexes, ok := splitBracketAccess(field); ok {
		field = base
		rest = append(indexes, rest...)
	}

	// First, check Variables for flat key lookup (backward compatible)
	key := "steps." + stepID + "." + field
	if val, exists := s.state.Variables[key]; exists {
		if len(rest) > 0 {
			return navigateNested(val, rest)
		}
		return val, nil
	}
	if field == "parsed_data" || field == "data" {
		if val, exists := s.state.Variables[stepID+"_parsed"]; exists {
			if len(rest) > 0 {
				return navigateNested(val, rest)
			}
			return val, nil
		}
	}

	// Then check Steps map if available
	if s.state.Steps == nil {
		return nil, fmt.Errorf("step not found: %s", stepID)
	}

	result, ok := s.state.Steps[stepID]
	if !ok {
		return nil, fmt.Errorf("step not found: %s", stepID)
	}

	switch field {
	case "output":
		if len(rest) > 0 {
			// Accessing parsed data field: steps.id.output.field
			if result.ParsedData != nil {
				return navigateNested(result.ParsedData, rest)
			}
			return nil, fmt.Errorf("step %s has no parsed data", stepID)
		}
		return result.Output, nil
	case "data", "parsed_data":
		if result.ParsedData == nil {
			return nil, fmt.Errorf("step %s has no parsed data", stepID)
		}
		if len(rest) > 0 {
			return navigateNested(result.ParsedData, rest)
		}
		return result.ParsedData, nil
	case "pane":
		return result.PaneUsed, nil
	case "duration":
		if result.FinishedAt.IsZero() {
			return "0s", nil
		}
		return result.FinishedAt.Sub(result.StartedAt).String(), nil
	case "status":
		return string(result.Status), nil
	case "agent":
		return result.AgentType, nil
	default:
		return nil, fmt.Errorf("unknown step field: %s", field)
	}
}

func splitBracketAccess(part string) (string, []string, bool) {
	start := strings.IndexByte(part, '[')
	if start < 0 {
		return part, nil, false
	}
	base := part[:start]
	if base == "" {
		return part, nil, false
	}
	rest := part[start:]
	var indexes []string
	for rest != "" {
		if !strings.HasPrefix(rest, "[") {
			return part, nil, false
		}
		end := strings.IndexByte(rest, ']')
		if end <= 1 {
			return part, nil, false
		}
		indexes = append(indexes, strings.TrimSpace(rest[1:end]))
		rest = rest[end+1:]
	}
	return base, indexes, true
}

// resolveEnv handles env.NAME references
func (s *Substitutor) resolveEnv(parts []string) (interface{}, error) {
	if len(parts) == 0 {
		return nil, fmt.Errorf("env requires a variable name")
	}

	envName := parts[0]
	value, ok := os.LookupEnv(envName)
	if !ok {
		return nil, fmt.Errorf("environment variable %s not set", envName)
	}
	return value, nil
}

// resolveLoop handles loop.item, loop.index, etc.
func (s *Substitutor) resolveLoop(parts []string) (interface{}, error) {
	if len(parts) == 0 {
		return nil, fmt.Errorf("loop requires a field name")
	}
	if s.state == nil || s.state.Variables == nil {
		return nil, fmt.Errorf("no loop context")
	}

	field := parts[0]

	// Loop variables are stored as loop.X in the Variables map
	loopKey := "loop." + field
	value, ok := s.state.Variables[loopKey]
	if !ok {
		return nil, fmt.Errorf("loop variable not set: %s", field)
	}

	// Handle nested access: loop.item.field
	if len(parts) > 1 {
		return navigateNested(value, parts[1:])
	}

	return value, nil
}

// resolveDefaults handles defaults.X and defaults.X.nested.field references.
func (s *Substitutor) resolveDefaults(parts []string) (interface{}, error) {
	if len(parts) == 0 {
		return nil, fmt.Errorf("defaults requires a key name")
	}
	if s.defaults == nil {
		return nil, fmt.Errorf("no defaults defined in workflow")
	}

	root := parts[0]
	value, ok := s.defaults[root]
	if !ok {
		return nil, fmt.Errorf("undefined default: %s", root)
	}

	if len(parts) > 1 {
		return navigateNested(value, parts[1:])
	}

	return value, nil
}

// resolveItem handles ${item} and ${item.X} references inside foreach iterations.
func (s *Substitutor) resolveItem(parts []string) (interface{}, error) {
	if s.state == nil || s.state.Variables == nil {
		return nil, fmt.Errorf("item is only available inside foreach iterations")
	}

	item, ok := s.state.Variables["loop.item"]
	if !ok {
		return nil, fmt.Errorf("item is only available inside foreach iterations")
	}

	if len(parts) == 0 {
		return item, nil
	}

	return navigateNested(item, parts)
}

// navigateNested traverses nested data structures using dot notation.
// Supports maps and arrays (with numeric indices).
func navigateNested(value interface{}, parts []string) (interface{}, error) {
	current := value

	for _, part := range parts {
		if current == nil {
			return nil, fmt.Errorf("cannot access '%s' on nil value", part)
		}

		switch v := current.(type) {
		case map[string]interface{}:
			var ok bool
			current, ok = v[part]
			if !ok {
				return nil, fmt.Errorf("field '%s' not found", part)
			}

		case map[interface{}]interface{}:
			// YAML sometimes returns this type
			var found bool
			for k, val := range v {
				if fmt.Sprintf("%v", k) == part {
					current = val
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("field '%s' not found", part)
			}

		case []interface{}:
			// Array access with numeric index
			idx, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid array index '%s': must be numeric", part)
			}
			if idx < 0 || idx >= len(v) {
				return nil, fmt.Errorf("array index %d out of bounds (length: %d)", idx, len(v))
			}
			current = v[idx]

		case []string:
			idx, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid array index '%s': must be numeric", part)
			}
			if idx < 0 || idx >= len(v) {
				return nil, fmt.Errorf("array index %d out of bounds (length: %d)", idx, len(v))
			}
			current = v[idx]

		default:
			return nil, fmt.Errorf("cannot access field '%s' on type %T", part, current)
		}
	}

	return current, nil
}

// formatValue converts a value to a string for substitution.
func formatValue(value interface{}) string {
	if value == nil {
		return ""
	}

	switch v := value.(type) {
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case []byte:
		return string(v)
	case time.Time:
		return v.Format(time.RFC3339)
	case time.Duration:
		return v.String()
	default:
		// For complex types, use JSON encoding
		if data, err := json.Marshal(v); err == nil {
			return string(data)
		}
		return fmt.Sprintf("%v", v)
	}
}

// OutputParser handles parsing of step outputs into structured data.
type OutputParser struct{}

// NewOutputParser creates a new output parser.
func NewOutputParser() *OutputParser {
	return &OutputParser{}
}

// Parse parses output according to the parse configuration.
func (p *OutputParser) Parse(output string, config OutputParse) (interface{}, error) {
	// Trim output before parsing
	output = strings.TrimSpace(output)

	switch config.Type {
	case "", "none":
		return output, nil

	case "first_line":
		return p.parseFirstLine(output)

	case "lines":
		return p.parseLines(output)

	case "json":
		return p.parseJSON(output)

	case "yaml":
		return p.parseYAML(output)

	case "regex":
		return p.parseRegex(output, config.Pattern)

	default:
		return nil, fmt.Errorf("unknown parse type: %s", config.Type)
	}
}

// parseFirstLine extracts the first non-empty line from output.
func (p *OutputParser) parseFirstLine(output string) (string, error) {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			return line, nil
		}
	}
	return "", nil
}

// parseLines splits output into an array of non-empty lines.
func (p *OutputParser) parseLines(output string) ([]string, error) {
	lines := strings.Split(output, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result, nil
}

// parseJSON parses output as JSON.
func (p *OutputParser) parseJSON(output string) (interface{}, error) {
	// Try to find JSON in the output (skip any prefix/suffix text)
	jsonStart := strings.Index(output, "{")
	jsonArrayStart := strings.Index(output, "[")

	if jsonStart == -1 && jsonArrayStart == -1 {
		return nil, fmt.Errorf("no JSON object or array found in output")
	}

	// Use the first occurrence (object or array)
	start := jsonStart
	if jsonArrayStart != -1 && (jsonStart == -1 || jsonArrayStart < jsonStart) {
		start = jsonArrayStart
	}

	// Try to parse starting from found position
	output = output[start:]

	var result interface{}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		// Try to find the matching end bracket
		output = extractJSONBlock(output)
		if err2 := json.Unmarshal([]byte(output), &result); err2 != nil {
			return nil, fmt.Errorf("failed to parse JSON (initial error: %v, fallback error: %w)", err, err2)
		}
	}

	return result, nil
}

// extractJSONBlock attempts to extract a complete JSON block from mixed output.
func extractJSONBlock(s string) string {
	if len(s) == 0 {
		return s
	}

	openChar := s[0]
	closeChar := byte('}')
	if openChar == '[' {
		closeChar = ']'
	} else if openChar != '{' {
		return s
	}

	depth := 0
	inString := false
	escaped := false

	for i := 0; i < len(s); i++ {
		c := s[i]

		if escaped {
			escaped = false
			continue
		}

		if c == '\\' && inString {
			escaped = true
			continue
		}

		if c == '"' {
			inString = !inString
			continue
		}

		if inString {
			continue
		}

		switch c {
		case openChar:
			depth++
		case closeChar:
			depth--
			if depth == 0 {
				return s[:i+1]
			}
		}
	}

	return s
}

// parseYAML parses output as YAML.
func (p *OutputParser) parseYAML(output string) (interface{}, error) {
	var result interface{}
	if err := yaml.Unmarshal([]byte(output), &result); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}
	return result, nil
}

// parseRegex extracts values using a regex pattern with named groups.
func (p *OutputParser) parseRegex(output string, pattern string) (interface{}, error) {
	if pattern == "" {
		return nil, fmt.Errorf("regex pattern is required")
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}

	matches := re.FindStringSubmatch(output)
	if matches == nil {
		return nil, nil // No match is not an error
	}

	// Check for named groups
	names := re.SubexpNames()
	hasNamedGroups := false
	for _, name := range names {
		if name != "" {
			hasNamedGroups = true
			break
		}
	}

	if hasNamedGroups {
		// Return as map with named groups
		result := make(map[string]interface{})
		for i, name := range names {
			if name != "" && i < len(matches) {
				result[name] = matches[i]
			}
		}
		return result, nil
	}

	// Return captured groups as []string for backward compatibility
	// matches[0] is full match, matches[1:] are captured groups
	if len(matches) == 1 {
		return matches[0], nil // No capture groups, return full match
	}

	// Return captured groups as []string
	return matches[1:], nil
}

// SetLoopVars sets loop context variables in the execution state.
func SetLoopVars(state *ExecutionState, varName string, item interface{}, index, total int) {
	if state.Variables == nil {
		state.Variables = make(map[string]interface{})
	}

	// Set the loop item variable (e.g., loop.file for as: file)
	state.Variables["loop."+varName] = item
	state.Variables["loop.item"] = item
	state.Variables["loop.index"] = index
	state.Variables["loop.count"] = total
	state.Variables["loop.first"] = index == 0
	state.Variables["loop.last"] = index == total-1
}

// ClearLoopVars removes loop context variables from execution state.
func ClearLoopVars(state *ExecutionState, varName string) {
	if state.Variables == nil {
		return
	}

	delete(state.Variables, "loop."+varName)
	delete(state.Variables, "loop.item")
	delete(state.Variables, "loop.index")
	delete(state.Variables, "loop.count")
	delete(state.Variables, "loop.first")
	delete(state.Variables, "loop.last")
}

// StoreStepOutput stores a step's output in the execution state for variable access.
// Note: Caller must hold any necessary locks on state.Variables if used concurrently.
func StoreStepOutput(state *ExecutionState, stepID string, output string, parsedData interface{}) {
	if state.Variables == nil {
		state.Variables = make(map[string]interface{})
	}

	state.Variables["steps."+stepID+".output"] = output
	if parsedData != nil {
		state.Variables["steps."+stepID+".data"] = parsedData
	}
}

// ValidateVarRefs validates that all variable references in a string are valid.
// Returns a list of invalid references.
func ValidateVarRefs(template string, availableVars []string) []string {
	var invalid []string

	// Find all variable references (excluding escaped ones)
	escaped := escapedPattern.ReplaceAllString(template, "")
	matches := varPattern.FindAllString(escaped, -1)

	varSet := make(map[string]bool)
	for _, v := range availableVars {
		varSet[v] = true
	}

	for _, match := range matches {
		// Extract variable path (without default)
		expr := match[2 : len(match)-1]
		varPath, _, _ := parseDefault(expr)

		// Check if the root namespace is valid
		parts := strings.Split(varPath, ".")
		if len(parts) == 0 {
			continue
		}

		// Valid namespaces that don't need to be pre-declared
		switch parts[0] {
		case "env", "session", "run_id", "timestamp", "workflow", "loop", "defaults", "item":
			continue
		case "vars":
			if len(parts) > 1 && !varSet["vars."+parts[1]] && !varSet[parts[1]] {
				invalid = append(invalid, match)
			}
		case "steps":
			// Steps are validated elsewhere during parsing
			continue
		default:
			invalid = append(invalid, match)
		}
	}

	return invalid
}

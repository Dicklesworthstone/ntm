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

// Substitutor handles variable substitution in workflow prompts and conditions.
// It supports multiple variable types: vars, steps, env, and context variables.
type Substitutor struct {
	state    *ExecutionState
	session  string
	workflow string
}

// NewSubstitutor creates a new substitutor with the given execution context.
func NewSubstitutor(state *ExecutionState, session, workflow string) *Substitutor {
	return &Substitutor{
		state:    state,
		session:  session,
		workflow: workflow,
	}
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

// placeholder for escaped sequences during substitution
const escapePlaceholder = "\x00ESC_VAR\x00"

// Substitute replaces all ${...} variable references in the template string.
// Returns the substituted string and any errors encountered.
func (s *Substitutor) Substitute(template string) (string, error) {
	// First, replace escaped \${...} with placeholder to preserve them
	escaped := escapedPattern.ReplaceAllString(template, escapePlaceholder)

	var firstErr error

	result := varPattern.ReplaceAllStringFunc(escaped, func(match string) string {
		// Extract expression between ${ and }
		expr := match[2 : len(match)-1]

		// Parse for default value: ${var | "default"} or ${var | default}
		varPath, defaultVal, hasDefault := parseDefault(expr)

		// Resolve the variable
		value, err := s.resolveVar(varPath)
		if err != nil {
			if hasDefault {
				return defaultVal
			}
			if firstErr == nil {
				firstErr = &SubstitutionError{VarRef: varPath, Message: err.Error()}
			}
			return match // Leave unsubstituted if resolution fails
		}

		return formatValue(value)
	})

	// Restore escaped ${...} sequences
	result = strings.ReplaceAll(result, escapePlaceholder, "${")

	return result, firstErr
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

	// First, check Variables for flat key lookup (backward compatible)
	key := "steps." + stepID + "." + field
	if val, exists := s.state.Variables[key]; exists {
		if len(parts) > 2 {
			return navigateNested(val, parts[2:])
		}
		return val, nil
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
		if len(parts) > 2 {
			// Accessing parsed data field: steps.id.output.field
			if result.ParsedData != nil {
				return navigateNested(result.ParsedData, parts[2:])
			}
			return nil, fmt.Errorf("step %s has no parsed data", stepID)
		}
		return result.Output, nil
	case "data":
		if result.ParsedData == nil {
			return nil, fmt.Errorf("step %s has no parsed data", stepID)
		}
		if len(parts) > 2 {
			return navigateNested(result.ParsedData, parts[2:])
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

// resolveEnv handles env.NAME references
func (s *Substitutor) resolveEnv(parts []string) (interface{}, error) {
	if len(parts) == 0 {
		return nil, fmt.Errorf("env requires a variable name")
	}

	envName := parts[0]
	value := os.Getenv(envName)

	// Note: We return empty string if env var is not set.
	// Use default syntax ${env.X | "fallback"} for required env vars.
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
		case "env", "session", "run_id", "timestamp", "workflow", "loop":
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

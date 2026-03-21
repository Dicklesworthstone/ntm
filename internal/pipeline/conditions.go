// Package pipeline provides workflow execution for AI agent orchestration.
// conditions.go implements conditional expression evaluation for workflow steps.
package pipeline

import (
	"fmt"
	"strconv"
	"strings"
)

// ConditionEvaluator evaluates conditional expressions for workflow steps.
// It supports boolean, equality, comparison, contains, and logical operators.
type ConditionEvaluator struct {
	substitutor *Substitutor
}

// NewConditionEvaluator creates a new condition evaluator with the given substitutor.
func NewConditionEvaluator(sub *Substitutor) *ConditionEvaluator {
	return &ConditionEvaluator{
		substitutor: sub,
	}
}

// ConditionResult contains the result of condition evaluation.
type ConditionResult struct {
	Value  bool   // True if condition is met (step should RUN)
	Skip   bool   // True if step should be skipped (inverse of Value)
	Reason string // Human-readable explanation
}

// Evaluate evaluates a condition expression.
// Returns ConditionResult with Value=true if the condition is met (step should run),
// Value=false if condition is not met (step should be skipped).
func (e *ConditionEvaluator) Evaluate(condition string) (ConditionResult, error) {
	if condition == "" {
		return ConditionResult{Value: true, Skip: false, Reason: "no condition"}, nil
	}

	// Substitute variables first
	substituted, err := e.substitutor.Substitute(condition)
	if err != nil {
		return ConditionResult{}, fmt.Errorf("variable substitution failed: %w", err)
	}

	// Evaluate the expression - don't trim here, evaluateExpr handles it internally
	// to preserve spaces needed for logical operator detection
	value, err := e.evaluateExpr(substituted)
	if err != nil {
		return ConditionResult{}, err
	}

	result := ConditionResult{
		Value: value,
		Skip:  !value,
	}

	if value {
		result.Reason = fmt.Sprintf("condition '%s' evaluated to true", condition)
	} else {
		result.Reason = fmt.Sprintf("condition '%s' evaluated to false", condition)
	}

	return result, nil
}

// evaluateExpr evaluates a (substituted) expression recursively.
func (e *ConditionEvaluator) evaluateExpr(expr string) (bool, error) {
	// Don't trim the full expression yet - we need trailing spaces for operator matching
	// We'll trim individual parts after splitting

	if strings.TrimSpace(expr) == "" {
		return false, nil
	}

	// Handle logical operators (lowest precedence, so check first)
	// Split by AND first, then by OR within each part

	// Check for OR (lowest precedence among logical ops)
	if idx := findLogicalOpFlexible(expr, "OR"); idx >= 0 {
		left := strings.TrimSpace(expr[:idx])
		// Find where the right side starts (after " OR " or " OR")
		rightStart := idx + 4 // skip " OR "
		if rightStart > len(expr) {
			rightStart = len(expr)
		}
		right := strings.TrimSpace(expr[rightStart:])
		leftVal, err := e.evaluateExpr(left)
		if err != nil {
			return false, err
		}
		if leftVal {
			return true, nil // Short-circuit
		}
		return e.evaluateExpr(right)
	}

	// Check for AND (higher precedence than OR)
	if idx := findLogicalOpFlexible(expr, "AND"); idx >= 0 {
		left := strings.TrimSpace(expr[:idx])
		// Find where the right side starts (after " AND " or " AND")
		rightStart := idx + 5 // skip " AND "
		if rightStart > len(expr) {
			rightStart = len(expr)
		}
		right := strings.TrimSpace(expr[rightStart:])
		leftVal, err := e.evaluateExpr(left)
		if err != nil {
			return false, err
		}
		if !leftVal {
			return false, nil // Short-circuit
		}
		return e.evaluateExpr(right)
	}

	// Trim for remaining checks
	expr = strings.TrimSpace(expr)

	// Handle NOT prefix
	if strings.HasPrefix(expr, "NOT ") {
		inner := strings.TrimSpace(expr[4:])
		val, err := e.evaluateExpr(inner)
		if err != nil {
			return false, err
		}
		return !val, nil
	}

	// Handle legacy negation prefix (!)
	if strings.HasPrefix(expr, "!") {
		inner := strings.TrimSpace(expr[1:])
		val, err := e.evaluateExpr(inner)
		if err != nil {
			return false, err
		}
		return !val, nil
	}

	// Handle parentheses
	if strings.HasPrefix(expr, "(") && strings.HasSuffix(expr, ")") {
		inner := expr[1 : len(expr)-1]
		return e.evaluateExpr(inner)
	}

	// Handle comparison and equality operators
	return e.evaluateComparison(expr)
}

// findLogicalOpFlexible finds the position of a logical operator (AND/OR) in the expression.
// It handles cases where the operator may or may not have trailing content.
// Returns the position where the space before the operator starts.
func findLogicalOpFlexible(expr, op string) int {
	opWithSpaces := " " + op + " " // e.g., " AND "
	opAtEnd := " " + op            // e.g., " AND" at end

	// First try the full pattern with spaces
	idx := findLogicalOp(expr, opWithSpaces)
	if idx >= 0 {
		return idx
	}

	// Then try the pattern at the end of the expression (no trailing space)
	if strings.HasSuffix(strings.TrimSpace(expr), op) {
		idx := findLogicalOp(expr, opAtEnd)
		if idx >= 0 {
			return idx
		}
	}

	return -1
}

// findLogicalOp finds the position of a logical operator in the expression.
// It respects parentheses nesting and string literals.
func findLogicalOp(expr, op string) int {
	depth := 0
	inString := false
	stringChar := byte(0)
	escaped := false

	for i := 0; i < len(expr)-len(op)+1; i++ {
		c := expr[i]

		if escaped {
			escaped = false
			continue
		}

		if c == '\\' && inString {
			escaped = true
			continue
		}

		// Handle string literals
		if !inString && (c == '"' || c == '\'') {
			inString = true
			stringChar = c
			continue
		}
		if inString && c == stringChar {
			inString = false
			continue
		}
		if inString {
			continue
		}

		// Handle parentheses
		if c == '(' {
			depth++
			continue
		}
		if c == ')' {
			depth--
			continue
		}

		// Only match at depth 0
		if depth == 0 && strings.HasPrefix(expr[i:], op) {
			return i
		}
	}

	return -1
}

// evaluateComparison evaluates a comparison expression.
func (e *ConditionEvaluator) evaluateComparison(expr string) (bool, error) {
	expr = strings.TrimSpace(expr)

	// Order matters: check longer operators first
	operators := []struct {
		op     string
		evalFn func(left, right string) (bool, error)
	}{
		{">=", e.evalGreaterEqual},
		{"<=", e.evalLessEqual},
		{"!=", e.evalNotEqual},
		{"==", e.evalEqual},
		{">", e.evalGreater},
		{"<", e.evalLess},
		{" contains ", e.evalContains},
	}

	for _, op := range operators {
		if idx := strings.Index(expr, op.op); idx >= 0 {
			left := strings.TrimSpace(expr[:idx])
			right := strings.TrimSpace(expr[idx+len(op.op):])
			return op.evalFn(left, right)
		}
	}

	// No operator found - evaluate as truthy
	return e.evalTruthy(expr), nil
}

// evalEqual evaluates left == right
func (e *ConditionEvaluator) evalEqual(left, right string) (bool, error) {
	left = cleanValue(left)
	right = cleanValue(right)
	return left == right, nil
}

// evalNotEqual evaluates left != right
func (e *ConditionEvaluator) evalNotEqual(left, right string) (bool, error) {
	left = cleanValue(left)
	right = cleanValue(right)
	return left != right, nil
}

// evalGreater evaluates left > right (numeric comparison)
func (e *ConditionEvaluator) evalGreater(left, right string) (bool, error) {
	leftNum, rightNum, err := parseNumericPair(left, right)
	if err != nil {
		return false, fmt.Errorf("cannot compare: %w", err)
	}
	return leftNum > rightNum, nil
}

// evalLess evaluates left < right (numeric comparison)
func (e *ConditionEvaluator) evalLess(left, right string) (bool, error) {
	leftNum, rightNum, err := parseNumericPair(left, right)
	if err != nil {
		return false, fmt.Errorf("cannot compare: %w", err)
	}
	return leftNum < rightNum, nil
}

// evalGreaterEqual evaluates left >= right (numeric comparison)
func (e *ConditionEvaluator) evalGreaterEqual(left, right string) (bool, error) {
	leftNum, rightNum, err := parseNumericPair(left, right)
	if err != nil {
		return false, fmt.Errorf("cannot compare: %w", err)
	}
	return leftNum >= rightNum, nil
}

// evalLessEqual evaluates left <= right (numeric comparison)
func (e *ConditionEvaluator) evalLessEqual(left, right string) (bool, error) {
	leftNum, rightNum, err := parseNumericPair(left, right)
	if err != nil {
		return false, fmt.Errorf("cannot compare: %w", err)
	}
	return leftNum <= rightNum, nil
}

// evalContains evaluates left contains right (substring check)
func (e *ConditionEvaluator) evalContains(left, right string) (bool, error) {
	left = cleanValue(left)
	right = cleanValue(right)
	return strings.Contains(left, right), nil
}

// evalTruthy evaluates a value as truthy/falsy.
func (e *ConditionEvaluator) evalTruthy(value string) bool {
	value = cleanValue(value)
	lower := strings.ToLower(value)

	// Falsy values
	switch lower {
	case "", "false", "0", "no", "null", "nil", "none", "undefined":
		return false
	}

	return true
}

// cleanValue removes surrounding quotes and trims whitespace.
func cleanValue(s string) string {
	s = strings.TrimSpace(s)

	// Remove surrounding quotes (single or double)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') ||
			(s[0] == '\'' && s[len(s)-1] == '\'') {
			s = s[1 : len(s)-1]
		}
	}

	return s
}

// parseNumericPair parses two values as floats for numeric comparison.
func parseNumericPair(left, right string) (float64, float64, error) {
	left = cleanValue(left)
	right = cleanValue(right)

	leftNum, err := strconv.ParseFloat(left, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("left operand '%s' is not a number", left)
	}

	rightNum, err := strconv.ParseFloat(right, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("right operand '%s' is not a number", right)
	}

	return leftNum, rightNum, nil
}

// EvaluateCondition is a convenience function that evaluates a condition string
// using a substitutor. Returns true if the step should be SKIPPED.
// This maintains backward compatibility with the original evaluateCondition API.
func EvaluateCondition(condition string, sub *Substitutor) (skip bool, err error) {
	if condition == "" {
		return false, nil // No condition = don't skip
	}

	evaluator := NewConditionEvaluator(sub)
	result, err := evaluator.Evaluate(condition)
	if err != nil {
		return false, err
	}

	return result.Skip, nil
}

// ValidateCondition checks if a condition expression has valid syntax.
// Returns a list of issues found (empty if valid).
func ValidateCondition(condition string) []string {
	var issues []string

	if condition == "" {
		return issues
	}

	// Check for balanced parentheses
	depth := 0
	for _, c := range condition {
		if c == '(' {
			depth++
		}
		if c == ')' {
			depth--
		}
		if depth < 0 {
			issues = append(issues, "unbalanced parentheses: too many closing ')'")
			break
		}
	}
	if depth > 0 {
		issues = append(issues, "unbalanced parentheses: missing closing ')'")
	}

	// Check for balanced quotes
	inDouble := false
	inSingle := false
	escaped := false
	for _, c := range condition {
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == '"' && !inSingle {
			inDouble = !inDouble
		}
		if c == '\'' && !inDouble {
			inSingle = !inSingle
		}
	}
	if inDouble {
		issues = append(issues, "unbalanced double quotes")
	}
	if inSingle {
		issues = append(issues, "unbalanced single quotes")
	}

	return issues
}

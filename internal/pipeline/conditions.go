// Package pipeline provides workflow execution for AI agent orchestration.
// conditions.go implements injection-proof conditional expression evaluation
// for workflow steps using a phase-separated lexer, LL(1) recursive-descent
// AST parser, and late variable-binding evaluator.
//
// Security invariant: raw expressions are tokenized and parsed into an AST
// BEFORE any `${...}` substitution runs. Substituted variable values are
// strictly bound as leaf data values and NEVER re-parsed as operators, quotes,
// or parentheses — so arbitrary step output containing " OR 1 == 1" or " >= "
// cannot hijack boolean gates.
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

// Evaluate parses the raw condition into an AST before binding any variables,
// then evaluates the AST with short-circuiting logical operators.
func (e *ConditionEvaluator) Evaluate(condition string) (ConditionResult, error) {
	if strings.TrimSpace(condition) == "" {
		return ConditionResult{Value: true, Skip: false, Reason: "no condition"}, nil
	}

	tokens, err := lexCondition(condition)
	if err != nil {
		return ConditionResult{}, fmt.Errorf("invalid condition syntax: %w", err)
	}
	p := &condParser{tokens: tokens}
	node, err := p.parseExpr()
	if err != nil {
		return ConditionResult{}, fmt.Errorf("invalid condition syntax: %w", err)
	}

	value, err := e.evalNode(node)
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

type condTokKind int

const (
	condTokEOF condTokKind = iota
	condTokLParen
	condTokRParen
	condTokOr
	condTokAnd
	condTokNot
	condTokOp
	condTokString
	condTokAtom
)

type condToken struct {
	kind   condTokKind
	text   string
	quoted bool
}

func lexCondition(input string) ([]condToken, error) {
	var tokens []condToken
	i := 0
	n := len(input)

	for i < n {
		// Skip whitespace
		for i < n && (input[i] == ' ' || input[i] == '\t' || input[i] == '\n' || input[i] == '\r') {
			i++
		}
		if i >= n {
			break
		}

		ch := input[i]
		switch ch {
		case '(':
			tokens = append(tokens, condToken{kind: condTokLParen, text: "("})
			i++
			continue
		case ')':
			tokens = append(tokens, condToken{kind: condTokRParen, text: ")"})
			i++
			continue
		case '|':
			if i+1 < n && input[i+1] == '|' {
				tokens = append(tokens, condToken{kind: condTokOr, text: "||"})
				i += 2
				continue
			}
		case '&':
			if i+1 < n && input[i+1] == '&' {
				tokens = append(tokens, condToken{kind: condTokAnd, text: "&&"})
				i += 2
				continue
			}
		case '=':
			if i+1 < n && input[i+1] == '=' {
				tokens = append(tokens, condToken{kind: condTokOp, text: "=="})
				i += 2
				continue
			}
		case '!':
			if i+1 < n && input[i+1] == '=' {
				tokens = append(tokens, condToken{kind: condTokOp, text: "!="})
				i += 2
				continue
			}
			tokens = append(tokens, condToken{kind: condTokNot, text: "!"})
			i++
			continue
		case '>':
			if i+1 < n && input[i+1] == '=' {
				tokens = append(tokens, condToken{kind: condTokOp, text: ">="})
				i += 2
				continue
			}
			tokens = append(tokens, condToken{kind: condTokOp, text: ">"})
			i++
			continue
		case '<':
			if i+1 < n && input[i+1] == '=' {
				tokens = append(tokens, condToken{kind: condTokOp, text: "<="})
				i += 2
				continue
			}
			tokens = append(tokens, condToken{kind: condTokOp, text: "<"})
			i++
			continue
		case '"', '\'':
			quote := ch
			i++
			var sb strings.Builder
			closed := false
			for i < n {
				c := input[i]
				if c == '\\' && i+1 < n {
					next := input[i+1]
					switch next {
					case 'n':
						sb.WriteByte('\n')
					case 't':
						sb.WriteByte('\t')
					case 'r':
						sb.WriteByte('\r')
					case '\\', '"', '\'':
						sb.WriteByte(next)
					default:
						sb.WriteByte(c)
						sb.WriteByte(next)
					}
					i += 2
					continue
				}
				if c == quote {
					i++
					closed = true
					break
				}
				sb.WriteByte(c)
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated string literal")
			}
			tokens = append(tokens, condToken{kind: condTokString, text: sb.String(), quoted: true})
			continue
		}

		// Scan an unquoted atom, keeping balanced `${...}` variable references
		// intact (including spaces, pipes, or quotes inside `${vars.x | "default"}`).
		start := i
		for i < n {
			if input[i] == '$' && i+1 < n && input[i+1] == '{' {
				i += 2
				braceDepth := 1
				var inQuote byte
				for i < n && braceDepth > 0 {
					c := input[i]
					if inQuote != 0 {
						if c == '\\' && i+1 < n {
							i += 2
							continue
						}
						if c == inQuote {
							inQuote = 0
						}
						i++
						continue
					}
					if c == '"' || c == '\'' {
						inQuote = c
					} else if c == '{' {
						braceDepth++
					} else if c == '}' {
						braceDepth--
					}
					i++
				}
				continue
			}
			c := input[i]
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' ||
				c == '(' || c == ')' || c == '>' || c == '<' || c == '!' ||
				(c == '=' && i+1 < n && input[i+1] == '=') ||
				(c == '&' && i+1 < n && input[i+1] == '&') ||
				(c == '|' && i+1 < n && input[i+1] == '|') {
				break
			}
			i++
		}
		word := input[start:i]
		if word == "" {
			return nil, fmt.Errorf("unexpected character %q at offset %d", input[i], i)
		}
		switch strings.ToUpper(word) {
		case "OR":
			tokens = append(tokens, condToken{kind: condTokOr, text: word})
		case "AND":
			tokens = append(tokens, condToken{kind: condTokAnd, text: word})
		case "NOT":
			tokens = append(tokens, condToken{kind: condTokNot, text: word})
		case "CONTAINS":
			tokens = append(tokens, condToken{kind: condTokOp, text: "contains"})
		default:
			tokens = append(tokens, condToken{kind: condTokAtom, text: word})
		}
	}

	tokens = append(tokens, condToken{kind: condTokEOF})
	return tokens, nil
}

type condNode interface {
	condNode()
}

type condOrNode struct {
	left, right condNode
}

type condAndNode struct {
	left, right condNode
}

type condNotNode struct {
	inner condNode
}

type condCompareNode struct {
	op          string
	left, right condAtomNode
}

type condAtomNode struct {
	raw    string
	quoted bool
}

func (condOrNode) condNode()      {}
func (condAndNode) condNode()     {}
func (condNotNode) condNode()     {}
func (condCompareNode) condNode() {}
func (condAtomNode) condNode()    {}

type condParser struct {
	tokens []condToken
	pos    int
}

func (p *condParser) peek() condToken {
	if p.pos >= len(p.tokens) {
		return condToken{kind: condTokEOF}
	}
	return p.tokens[p.pos]
}

func (p *condParser) next() condToken {
	tok := p.peek()
	if p.pos < len(p.tokens) {
		p.pos++
	}
	return tok
}

func (p *condParser) parseExpr() (condNode, error) {
	node, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if tok := p.peek(); tok.kind != condTokEOF {
		return nil, fmt.Errorf("unexpected token %q", tok.text)
	}
	return node, nil
}

func (p *condParser) parseOr() (condNode, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == condTokOr {
		p.next()
		// Preserve compatibility with legacy trailing-operator tests ("a OR" -> empty falsy RHS).
		if p.peek().kind == condTokEOF || p.peek().kind == condTokRParen {
			left = condOrNode{left: left, right: condAtomNode{raw: ""}}
			break
		}
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = condOrNode{left: left, right: right}
	}
	return left, nil
}

func (p *condParser) parseAnd() (condNode, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == condTokAnd {
		p.next()
		if p.peek().kind == condTokEOF || p.peek().kind == condTokRParen {
			left = condAndNode{left: left, right: condAtomNode{raw: ""}}
			break
		}
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = condAndNode{left: left, right: right}
	}
	return left, nil
}

func (p *condParser) parseNot() (condNode, error) {
	if p.peek().kind == condTokNot {
		p.next()
		inner, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return condNotNode{inner: inner}, nil
	}
	return p.parsePrimaryOrComparison()
}

func (p *condParser) parsePrimaryOrComparison() (condNode, error) {
	if p.peek().kind == condTokLParen {
		p.next()
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != condTokRParen {
			return nil, fmt.Errorf("expected closing ')'")
		}
		p.next()
		return inner, nil
	}

	left, err := p.parseAtom()
	if err != nil {
		return nil, err
	}
	if p.peek().kind == condTokOp {
		op := p.next().text
		right, err := p.parseAtom()
		if err != nil {
			return nil, err
		}
		return condCompareNode{op: op, left: left, right: right}, nil
	}
	return left, nil
}

func (p *condParser) parseAtom() (condAtomNode, error) {
	tok := p.peek()
	if tok.kind == condTokString || tok.kind == condTokAtom {
		p.next()
		return condAtomNode{raw: tok.text, quoted: tok.quoted}, nil
	}
	return condAtomNode{}, fmt.Errorf("expected operand, got %q", tok.text)
}

func (e *ConditionEvaluator) resolveAtom(atom condAtomNode) (string, error) {
	if atom.raw == "" || e.substitutor == nil {
		return atom.raw, nil
	}
	if !strings.Contains(atom.raw, "${") {
		return atom.raw, nil
	}
	val, err := e.substitutor.Substitute(atom.raw)
	if err != nil {
		return "", fmt.Errorf("variable substitution failed: %w", err)
	}
	return val, nil
}

func (e *ConditionEvaluator) evalNode(node condNode) (bool, error) {
	switch n := node.(type) {
	case condOrNode:
		left, err := e.evalNode(n.left)
		if err != nil {
			return false, err
		}
		if left {
			return true, nil
		}
		return e.evalNode(n.right)
	case condAndNode:
		left, err := e.evalNode(n.left)
		if err != nil {
			return false, err
		}
		if !left {
			return false, nil
		}
		return e.evalNode(n.right)
	case condNotNode:
		val, err := e.evalNode(n.inner)
		if err != nil {
			return false, err
		}
		return !val, nil
	case condAtomNode:
		val, err := e.resolveAtom(n)
		if err != nil {
			return false, err
		}
		return e.evalTruthyValue(val, n.quoted), nil
	case condCompareNode:
		left, err := e.resolveAtom(n.left)
		if err != nil {
			return false, err
		}
		right, err := e.resolveAtom(n.right)
		if err != nil {
			return false, err
		}
		return e.evalCompareValues(n.op, left, n.left.quoted, right, n.right.quoted)
	default:
		return false, fmt.Errorf("unknown condition node type %T", node)
	}
}

func (e *ConditionEvaluator) evalCompareValues(op, left string, leftQuoted bool, right string, rightQuoted bool) (bool, error) {
	lVal := left
	if !leftQuoted {
		lVal = strings.TrimSpace(lVal)
	}
	rVal := right
	if !rightQuoted {
		rVal = strings.TrimSpace(rVal)
	}

	switch op {
	case "==":
		return lVal == rVal, nil
	case "!=":
		return lVal != rVal, nil
	case "contains":
		return strings.Contains(lVal, rVal), nil
	case ">", "<", ">=", "<=":
		leftNum, rightNum, err := parseNumericPair(lVal, rVal)
		if err != nil {
			return false, fmt.Errorf("cannot compare: %w", err)
		}
		switch op {
		case ">":
			return leftNum > rightNum, nil
		case "<":
			return leftNum < rightNum, nil
		case ">=":
			return leftNum >= rightNum, nil
		case "<=":
			return leftNum <= rightNum, nil
		}
	}
	return false, fmt.Errorf("unsupported comparison operator %q", op)
}

func (e *ConditionEvaluator) evalTruthyValue(value string, quoted bool) bool {
	if !quoted {
		value = strings.TrimSpace(value)
	}
	switch strings.ToLower(value) {
	case "", "false", "0", "no", "null", "nil", "none", "undefined":
		return false
	}
	return true
}

// cleanValue removes surrounding quotes and trims whitespace.
func cleanValue(s string) string {
	s = strings.TrimSpace(s)
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
func EvaluateCondition(condition string, sub *Substitutor) (skip bool, err error) {
	if condition == "" {
		return false, nil
	}

	evaluator := NewConditionEvaluator(sub)
	result, err := evaluator.Evaluate(condition)
	if err != nil {
		return false, err
	}

	return result.Skip, nil
}

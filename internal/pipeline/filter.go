package pipeline

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// FilterContext contains the per-iteration values available to foreach.Filter.
type FilterContext struct {
	Item interface{}
	Pane interface{}
}

// EvaluateForeachFilter evaluates a foreach filter expression against the
// current iteration item and pane metadata.
func EvaluateForeachFilter(expr string, ctx FilterContext) (bool, error) {
	tokens, err := tokenizeFilter(expr)
	if err != nil {
		return false, err
	}
	parser := filterParser{tokens: tokens, ctx: ctx}
	node, err := parser.parseOr()
	if err != nil {
		return false, err
	}
	if !parser.atEnd() {
		return false, fmt.Errorf("unexpected filter token %q", parser.peek().value)
	}
	return node.eval(ctx)
}

type filterTokenKind int

const (
	filterTokenEOF filterTokenKind = iota
	filterTokenValue
	filterTokenString
	filterTokenEqual
	filterTokenNotEqual
	filterTokenAnd
	filterTokenOr
	filterTokenLeftParen
	filterTokenRightParen
)

type filterToken struct {
	kind  filterTokenKind
	value string
}

func tokenizeFilter(expr string) ([]filterToken, error) {
	var tokens []filterToken
	for i := 0; i < len(expr); {
		r, size := utf8.DecodeRuneInString(expr[i:])
		if unicode.IsSpace(r) {
			i += size
			continue
		}
		switch {
		case strings.HasPrefix(expr[i:], "=="):
			tokens = append(tokens, filterToken{kind: filterTokenEqual, value: "=="})
			i += 2
		case strings.HasPrefix(expr[i:], "!="):
			tokens = append(tokens, filterToken{kind: filterTokenNotEqual, value: "!="})
			i += 2
		case strings.HasPrefix(expr[i:], "&&"):
			tokens = append(tokens, filterToken{kind: filterTokenAnd, value: "&&"})
			i += 2
		case strings.HasPrefix(expr[i:], "||"):
			tokens = append(tokens, filterToken{kind: filterTokenOr, value: "||"})
			i += 2
		case expr[i] == '(':
			tokens = append(tokens, filterToken{kind: filterTokenLeftParen, value: "("})
			i++
		case expr[i] == ')':
			tokens = append(tokens, filterToken{kind: filterTokenRightParen, value: ")"})
			i++
		case expr[i] == '"' || expr[i] == '\'':
			value, next, err := scanFilterString(expr, i)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, filterToken{kind: filterTokenString, value: value})
			i = next
		default:
			start := i
			for i < len(expr) && !isFilterDelimiter(expr[i]) {
				i++
			}
			value := strings.TrimSpace(expr[start:i])
			if value == "" {
				return nil, fmt.Errorf("unexpected filter character %q", expr[start])
			}
			tokens = append(tokens, filterToken{kind: filterTokenValue, value: value})
		}
	}
	tokens = append(tokens, filterToken{kind: filterTokenEOF})
	return tokens, nil
}

func isFilterDelimiter(b byte) bool {
	return unicode.IsSpace(rune(b)) || strings.ContainsRune("()=!&|", rune(b))
}

func scanFilterString(expr string, start int) (string, int, error) {
	quote := expr[start]
	var b strings.Builder
	for i := start + 1; i < len(expr); i++ {
		if expr[i] == '\\' && i+1 < len(expr) {
			i++
			b.WriteByte(expr[i])
			continue
		}
		if expr[i] == quote {
			return b.String(), i + 1, nil
		}
		b.WriteByte(expr[i])
	}
	return "", 0, fmt.Errorf("unterminated filter string")
}

type filterParser struct {
	tokens []filterToken
	pos    int
	ctx    FilterContext
}

func (p *filterParser) parseOr() (filterNode, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.match(filterTokenOr) {
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = filterLogicalNode{op: "||", left: left, right: right}
	}
	return left, nil
}

func (p *filterParser) parseAnd() (filterNode, error) {
	left, err := p.parseComparison()
	if err != nil {
		return nil, err
	}
	for p.match(filterTokenAnd) {
		right, err := p.parseComparison()
		if err != nil {
			return nil, err
		}
		left = filterLogicalNode{op: "&&", left: left, right: right}
	}
	return left, nil
}

func (p *filterParser) parseComparison() (filterNode, error) {
	if p.match(filterTokenLeftParen) {
		node, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if !p.match(filterTokenRightParen) {
			return nil, fmt.Errorf("missing closing ')' in filter")
		}
		return node, nil
	}

	left, err := p.consumeOperand("expected filter operand")
	if err != nil {
		return nil, err
	}
	if p.match(filterTokenEqual) {
		right, err := p.consumeOperand("expected right side of ==")
		if err != nil {
			return nil, err
		}
		return filterCompareNode{op: "==", left: left, right: right}, nil
	}
	if p.match(filterTokenNotEqual) {
		right, err := p.consumeOperand("expected right side of !=")
		if err != nil {
			return nil, err
		}
		return filterCompareNode{op: "!=", left: left, right: right}, nil
	}
	return filterTruthyNode{operand: left}, nil
}

func (p *filterParser) consumeOperand(message string) (filterOperand, error) {
	tok := p.peek()
	switch tok.kind {
	case filterTokenValue, filterTokenString:
		p.pos++
		return filterOperand{value: tok.value, quoted: tok.kind == filterTokenString}, nil
	default:
		return filterOperand{}, fmt.Errorf("%s", message)
	}
}

func (p *filterParser) match(kind filterTokenKind) bool {
	if p.peek().kind != kind {
		return false
	}
	p.pos++
	return true
}

func (p *filterParser) peek() filterToken {
	if p.pos >= len(p.tokens) {
		return filterToken{kind: filterTokenEOF}
	}
	return p.tokens[p.pos]
}

func (p *filterParser) atEnd() bool {
	return p.peek().kind == filterTokenEOF
}

type filterNode interface {
	eval(FilterContext) (bool, error)
}

type filterLogicalNode struct {
	op          string
	left, right filterNode
}

func (n filterLogicalNode) eval(ctx FilterContext) (bool, error) {
	left, err := n.left.eval(ctx)
	if err != nil {
		return false, err
	}
	switch n.op {
	case "&&":
		if !left {
			return false, nil
		}
		return n.right.eval(ctx)
	case "||":
		if left {
			return true, nil
		}
		return n.right.eval(ctx)
	default:
		return false, fmt.Errorf("unknown filter logical operator %q", n.op)
	}
}

type filterCompareNode struct {
	op          string
	left, right filterOperand
}

func (n filterCompareNode) eval(ctx FilterContext) (bool, error) {
	left, err := n.left.resolve(ctx, true)
	if err != nil {
		return false, err
	}
	right, err := n.right.resolve(ctx, n.right.requiresResolution())
	if err != nil {
		return false, err
	}
	equal := filterValuesEqual(left, right)
	switch n.op {
	case "==":
		return equal, nil
	case "!=":
		return !equal, nil
	default:
		return false, fmt.Errorf("unknown filter comparison operator %q", n.op)
	}
}

type filterTruthyNode struct {
	operand filterOperand
}

func (n filterTruthyNode) eval(ctx FilterContext) (bool, error) {
	value, err := n.operand.resolve(ctx, true)
	if err != nil {
		return false, err
	}
	return filterTruthy(value), nil
}

type filterOperand struct {
	value  string
	quoted bool
}

func (o filterOperand) requiresResolution() bool {
	if o.quoted {
		return false
	}
	value := unwrapFilterReference(o.value)
	if value == "item" || value == "pane" {
		return true
	}
	return strings.HasPrefix(value, "item.") || strings.HasPrefix(value, "pane.")
}

func (o filterOperand) resolve(ctx FilterContext, requireBinding bool) (interface{}, error) {
	if o.quoted {
		return o.value, nil
	}
	if v, ok := parseFilterLiteral(o.value); ok {
		return v, nil
	}
	if requireBinding || o.requiresResolution() {
		return resolveFilterBinding(unwrapFilterReference(o.value), ctx)
	}
	return o.value, nil
}

func unwrapFilterReference(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "${") && strings.HasSuffix(value, "}") {
		return strings.TrimSpace(value[2 : len(value)-1])
	}
	return value
}

func parseFilterLiteral(value string) (interface{}, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	if n, err := strconv.Atoi(value); err == nil {
		return n, true
	}
	return nil, false
}

func resolveFilterBinding(name string, ctx FilterContext) (interface{}, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "item":
		// Bare item resolves to the iteration value itself so filters like
		// item=="a" work for scalar foreach lists (bd-8cx7e).
		if ctx.Item == nil {
			return nil, fmt.Errorf("undefined filter variable %q", name)
		}
		return ctx.Item, nil
	case name == "pane":
		if ctx.Pane == nil {
			return nil, fmt.Errorf("undefined filter variable %q", name)
		}
		return ctx.Pane, nil
	case strings.HasPrefix(name, "item."):
		return resolveFilterPath(ctx.Item, strings.TrimPrefix(name, "item."), name)
	case strings.HasPrefix(name, "pane."):
		return resolveFilterPath(ctx.Pane, strings.TrimPrefix(name, "pane."), name)
	default:
		if value, err := resolveFilterPath(ctx.Item, name, name); err == nil {
			return value, nil
		}
		return resolveFilterPath(ctx.Pane, name, name)
	}
}

func resolveFilterPath(root interface{}, path, display string) (interface{}, error) {
	if root == nil {
		return nil, fmt.Errorf("undefined filter variable %q", display)
	}
	value := root
	for _, part := range strings.Split(path, ".") {
		var err error
		value, err = resolveFilterPart(value, part)
		if err != nil {
			return nil, fmt.Errorf("undefined filter variable %q: %w", display, err)
		}
	}
	return value, nil
}

func resolveFilterPart(root interface{}, part string) (interface{}, error) {
	part = strings.TrimSpace(part)
	if part == "" {
		return nil, fmt.Errorf("empty path segment")
	}
	if m, ok := root.(map[string]interface{}); ok {
		value, ok := m[part]
		if !ok {
			return nil, fmt.Errorf("field %q not found", part)
		}
		return value, nil
	}
	if m, ok := root.(map[string]string); ok {
		value, ok := m[part]
		if !ok {
			return nil, fmt.Errorf("field %q not found", part)
		}
		return value, nil
	}

	value := reflect.ValueOf(root)
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil, fmt.Errorf("nil value")
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("map key type %s is not string", value.Type().Key())
		}
		entry := value.MapIndex(reflect.ValueOf(part))
		if !entry.IsValid() {
			return nil, fmt.Errorf("field %q not found", part)
		}
		return entry.Interface(), nil
	case reflect.Struct:
		want := normalizeFilterField(part)
		typ := value.Type()
		for i := 0; i < value.NumField(); i++ {
			field := typ.Field(i)
			if field.PkgPath != "" {
				continue
			}
			if normalizeFilterField(field.Name) == want {
				return value.Field(i).Interface(), nil
			}
		}
	}
	return nil, fmt.Errorf("field %q not found", part)
}

func normalizeFilterField(name string) string {
	return strings.ReplaceAll(strings.ToLower(name), "_", "")
}

func filterValuesEqual(left, right interface{}) bool {
	if lf, ok := filterNumber(left); ok {
		if rf, ok := filterNumber(right); ok {
			return lf == rf
		}
	}
	return fmt.Sprint(left) == fmt.Sprint(right)
}

func filterNumber(value interface{}) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case string:
		n, err := strconv.ParseFloat(v, 64)
		return n, err == nil
	default:
		return 0, false
	}
}

func filterTruthy(value interface{}) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return v != "" && v != "false" && v != "0"
	case int:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	case nil:
		return false
	default:
		return true
	}
}

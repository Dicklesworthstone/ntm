package pipeline

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
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
	node, err := parseFilterExpression(expr)
	if err != nil {
		return false, err
	}
	return node.eval(ctx)
}

const (
	maxFilterExpressionBytes  = 64 * 1024
	maxFilterExpressionTokens = 4096
	maxFilterExpressionDepth  = 128
)

// parseFilterExpression is shared by foreach and bead_query. Parsing is
// separate from evaluation so callers can validate every branch before
// querying external tools, including when a query will return no records.
func parseFilterExpression(expr string) (filterNode, error) {
	if len(expr) > maxFilterExpressionBytes {
		return nil, fmt.Errorf("filter exceeds %d bytes", maxFilterExpressionBytes)
	}
	tokens, err := tokenizeFilter(expr)
	if err != nil {
		return nil, err
	}
	parser := filterParser{tokens: tokens}
	node, err := parser.parseOr()
	if err != nil {
		return nil, err
	}
	if !parser.atEnd() {
		return nil, fmt.Errorf("unexpected filter token %q", parser.peek().value)
	}
	return node, nil
}

type filterTokenKind int

const (
	filterTokenEOF filterTokenKind = iota
	filterTokenValue
	filterTokenString
	filterTokenComparison
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
		if len(tokens) >= maxFilterExpressionTokens {
			return nil, fmt.Errorf("filter exceeds %d tokens", maxFilterExpressionTokens)
		}
		r, size := utf8.DecodeRuneInString(expr[i:])
		if unicode.IsSpace(r) {
			i += size
			continue
		}
		switch {
		case strings.HasPrefix(expr[i:], "=="):
			tokens = append(tokens, filterToken{kind: filterTokenComparison, value: "=="})
			i += 2
		case strings.HasPrefix(expr[i:], "!="):
			tokens = append(tokens, filterToken{kind: filterTokenComparison, value: "!="})
			i += 2
		case expr[i] == '<' || expr[i] == '>':
			start := i
			i++
			if i < len(expr) && expr[i] == '=' {
				i++
			}
			tokens = append(tokens, filterToken{kind: filterTokenComparison, value: expr[start:i]})
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
			for i < len(expr) {
				r, size := utf8.DecodeRuneInString(expr[i:])
				if unicode.IsSpace(r) || isFilterDelimiter(expr[i]) {
					break
				}
				i += size
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
	return strings.ContainsRune("()=<>!&|\"'", rune(b))
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
	depth  int
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
		if p.depth >= maxFilterExpressionDepth {
			return nil, fmt.Errorf("filter exceeds %d levels of parentheses", maxFilterExpressionDepth)
		}
		p.depth++
		node, err := p.parseOr()
		p.depth--
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
	operator := p.peek()
	if p.match(filterTokenComparison) {
		right, err := p.consumeOperand("expected right side of " + operator.value)
		if err != nil {
			return nil, err
		}
		return filterCompareNode{op: operator.value, left: left, right: right}, nil
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
	switch n.op {
	case "==":
		return filterValuesEqual(left, right), nil
	case "!=":
		return !filterValuesEqual(left, right), nil
	case "<", "<=", ">", ">=":
		l, lok := filterNumber(left)
		r, rok := filterNumber(right)
		if !lok || !rok {
			return false, fmt.Errorf("filter %s requires finite numeric operands", n.op)
		}
		return numericOrderMatches(l.cmp(r), n.op), nil
	default:
		return false, fmt.Errorf("unknown filter comparison operator %q", n.op)
	}
}

// numericOrderMatches evaluates a validated ordering operator against a
// three-way comparison, shared by foreach numbers and bead priorities.
func numericOrderMatches(order int, op string) bool {
	switch op {
	case "<":
		return order < 0
	case "<=":
		return order <= 0
	case ">":
		return order > 0
	case ">=":
		return order >= 0
	}
	return false
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
	if n, ok := parsePipelineNumber(value); ok {
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
			return lf.cmp(rf) == 0
		}
	}
	if lf, ok := parseFilterSpecialNumber(left); ok {
		if rf, ok := parseFilterSpecialNumber(right); ok {
			return lf == rf
		}
	}
	return fmt.Sprint(left) == fmt.Sprint(right)
}

// parseFilterSpecialNumber preserves legacy NaN/Inf equality for native floats
// and numeric strings. Finite values never use this floating-point fallback.
func parseFilterSpecialNumber(value interface{}) (float64, bool) {
	var number float64
	switch v := value.(type) {
	case float32:
		number = float64(v)
	case float64:
		number = v
	case string:
		var err error
		number, err = strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return number, math.IsNaN(number) || math.IsInf(number, 0)
}

// pipelineNumber represents sign * 0.digits * 10^magnitude. Significant digits
// have no leading or trailing zeros. Keeping the exponent separate preserves
// decimal input exactly without allocating space proportional to its value:
// even 1e999999999999999999999 uses only its input-sized exponent and mantissa.
// Float inputs use their shortest round-trip decimal spelling, matching the
// value exposed by workflow substitution and JSON serialization.
type pipelineNumber struct {
	sign      int
	digits    string
	magnitude big.Int
}

func (n pipelineNumber) cmp(other pipelineNumber) int {
	if n.sign < other.sign {
		return -1
	}
	if n.sign > other.sign {
		return 1
	}
	if n.sign == 0 {
		return 0
	}
	order := n.magnitude.Cmp(&other.magnitude)
	if order == 0 {
		// With trailing zeros removed, lexicographic ordering is equivalent
		// to right-padding either significand with zeros, without expansion.
		order = strings.Compare(n.digits, other.digits)
	}
	return n.sign * order
}

func parsePipelineNumber(value string) (pipelineNumber, bool) {
	var number pipelineNumber
	if value == "" {
		return number, false
	}
	i, sign := 0, 1
	if value[i] == '-' || value[i] == '+' {
		if value[i] == '-' {
			sign = -1
		}
		i++
	}
	start := i
	for i < len(value) && value[i] >= '0' && value[i] <= '9' {
		i++
	}
	integerEnd := i
	fractionStart := i
	if i < len(value) && value[i] == '.' {
		i++
		fractionStart = i
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
	}
	mantissaEnd := i
	if integerEnd == start && mantissaEnd == fractionStart {
		return number, false
	}
	if i < len(value) && (value[i] == 'e' || value[i] == 'E') {
		i++
		exponentStart := i
		if i < len(value) && (value[i] == '-' || value[i] == '+') {
			i++
		}
		digitsStart := i
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
		if i == digitsStart || i != len(value) {
			return number, false
		}
		if _, ok := number.magnitude.SetString(value[exponentStart:i], 10); !ok {
			return pipelineNumber{}, false
		}
	}
	if i != len(value) {
		return pipelineNumber{}, false
	}
	digits := strings.TrimLeft(value[start:integerEnd]+value[fractionStart:mantissaEnd], "0")
	if digits == "" {
		return pipelineNumber{}, true // All signed/exponent forms of zero agree.
	}
	decimalPlaces := mantissaEnd - fractionStart
	number.magnitude.Add(&number.magnitude, big.NewInt(int64(len(digits)-decimalPlaces)))
	number.sign = sign
	number.digits = strings.TrimRight(digits, "0")
	return number, true
}

func filterNumber(value interface{}) (pipelineNumber, bool) {
	switch v := value.(type) {
	case pipelineNumber:
		return v, true
	case json.Number:
		return parsePipelineNumber(v.String())
	case string:
		return parsePipelineNumber(v)
	}
	// Include every numeric kind, including aliases and unsigned YAML values;
	// coercing an integer through float64 can merge adjacent workflow targets.
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return parsePipelineNumber(strconv.FormatInt(v.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return parsePipelineNumber(strconv.FormatUint(v.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		return parsePipelineNumber(strconv.FormatFloat(v.Float(), 'g', -1, v.Type().Bits()))
	}
	return pipelineNumber{}, false
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
	case pipelineNumber:
		return v.sign != 0
	case json.Number:
		// Inspect the mantissa so tiny nonzero values stay truthy and large
		// exponents never require floating-point conversion or expansion.
		for _, digit := range v {
			if digit == 'e' || digit == 'E' {
				break
			}
			if digit >= '1' && digit <= '9' {
				return true
			}
		}
		return false
	case nil:
		return false
	default:
		if number, ok := filterNumber(value); ok {
			return number.sign != 0
		}
		return true
	}
}

// Package pipeline — iteration_sources.go implements the resolver for
// foreach iteration source fields (Beads, Pairs, Debates).
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"syscall"
)

// IterationSourceResolver expands foreach iteration source expressions into
// concrete []interface{} item lists. Runners are injectable for tests; nil
// runners default to real /bin/sh and `br` exec.
type IterationSourceResolver struct {
	ProjectDir string
	RunShell   func(ctx context.Context, shellCmd string) ([]byte, error)
	RunBr      func(ctx context.Context, args []string) ([]byte, error)
}

// ResolveItems expands a foreach.Items expression to a uniform item list.
//
// Supported forms:
//   - JSON array literals such as ["cc","cod"] or [1,2.5,true].
//   - Exact workflow variable references such as ${vars.hypotheses}, where
//     the referenced value must already be an array-like value.
//   - Exact direct variable references such as ${session_id}, which may be a
//     scalar and is then wrapped as a single iteration item.
func (r *IterationSourceResolver) ResolveItems(_ context.Context, expr string, vars map[string]interface{}) ([]interface{}, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return []interface{}{}, nil
	}

	if strings.HasPrefix(expr, "[") {
		items, err := parseItemsJSONLiteral(expr)
		if err != nil {
			return nil, fmt.Errorf("items JSON array: %w", err)
		}
		return items, nil
	}

	if ref, ok := exactVariableReference(expr); ok {
		value, err := lookupItemsVariable(ref, vars)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(ref, "vars.") {
			items, err := arrayLikeItems(value)
			if err != nil {
				return nil, fmt.Errorf("items reference ${%s}: %w", ref, err)
			}
			return items, nil
		}
		return scalarOrArrayItems(value)
	}

	return []interface{}{expr}, nil
}

// ResolveModels expands a foreach.Models field into model-family slugs.
//
// Inline list form is returned as-is after trimming empty whitespace. A
// single string can either be a literal slug ("cc") or a shell command
// source, detected by "$(...)" or pipe syntax, whose output is parsed as one
// slug per non-empty line.
func (r *IterationSourceResolver) ResolveModels(ctx context.Context, models StringOrList) ([]string, error) {
	if len(models) == 0 {
		return []string{}, nil
	}

	if len(models) > 1 {
		return compactStringList(models), nil
	}

	expr := strings.TrimSpace(models[0])
	if expr == "" {
		return []string{}, nil
	}
	if !looksLikeShellModelSource(expr) {
		return []string{expr}, nil
	}

	shellCmd := expr
	if inner, ok := stripShellInvocation(expr); ok {
		shellCmd = inner
	}
	out, err := r.runShell(ctx, shellCmd)
	if err != nil {
		return nil, fmt.Errorf("models shell command failed: %w", err)
	}
	return parseModelShellOutput(out), nil
}

// ResolveBeads expands a foreach.Beads expression to a list of bead records.
//
// Two input forms:
//   - Shell pipe: "$(br list --label=hypothesis ... | jq ...)" — execute the
//     shell command; if stdout parses as a JSON array, each element becomes an
//     iteration item; otherwise stdout is line-delimited and each non-empty
//     line is treated as a bead ID string.
//   - Structured query: "hypothesis,state:active" — comma-separated terms.
//     Bare tokens are treated as labels (AND); `key:value` terms map to
//     `br list` flags (status/state, type, priority, assignee). The resolver
//     shells out to `br list --json` and returns one map per bead with at
//     minimum {id, title, description, labels, status}, suitable for
//     ${item.id}, ${item.title}, etc.
//
// Empty stdout yields zero iterations (not an error).
func (r *IterationSourceResolver) ResolveBeads(ctx context.Context, expr string) ([]interface{}, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return []interface{}{}, nil
	}

	if shellCmd, ok := stripShellInvocation(expr); ok {
		out, err := r.runShell(ctx, shellCmd)
		if err != nil {
			return nil, fmt.Errorf("beads shell command failed: %w", err)
		}
		return parseBeadsShellOutput(out)
	}

	args, err := parseStructuredBeadsQuery(expr)
	if err != nil {
		return nil, fmt.Errorf("beads structured query %q: %w", expr, err)
	}
	out, err := r.runBr(ctx, args)
	if err != nil {
		return nil, fmt.Errorf("br list failed: %w", err)
	}
	return parseBrListJSON(out)
}

// stripShellInvocation returns the inner command for a "$(...)" expression
// and ok=true. For any other input it returns ("", false).
func stripShellInvocation(s string) (string, bool) {
	if strings.HasPrefix(s, "$(") && strings.HasSuffix(s, ")") {
		return s[2 : len(s)-1], true
	}
	return "", false
}

// parseBeadsShellOutput parses stdout from a beads-source shell command.
// Tries JSON-array first; falls back to one-ID-per-line.
func parseBeadsShellOutput(out []byte) ([]interface{}, error) {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return []interface{}{}, nil
	}

	if strings.HasPrefix(trimmed, "[") {
		var arr []interface{}
		if err := json.Unmarshal([]byte(trimmed), &arr); err == nil {
			return arr, nil
		}
		// fall through to line parse if the [ was just inside a line
	}

	lines := strings.Split(trimmed, "\n")
	items := make([]interface{}, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Strip surrounding quotes — `jq '.issues[].id'` emits "bd-foo".
		line = strings.Trim(line, `"'`)
		if line == "" {
			continue
		}
		items = append(items, line)
	}
	return items, nil
}

func compactStringList(values []string) []string {
	items := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			items = append(items, value)
		}
	}
	return items
}

func looksLikeShellModelSource(expr string) bool {
	if _, ok := stripShellInvocation(expr); ok {
		return true
	}
	return strings.Contains(expr, "|") || strings.Contains(expr, "$(")
}

func parseModelShellOutput(out []byte) []string {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return []string{}
	}
	lines := strings.Split(trimmed, "\n")
	models := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		line = strings.Trim(line, `"'`)
		if line == "" {
			continue
		}
		models = append(models, line)
	}
	return models
}

func parseItemsJSONLiteral(expr string) ([]interface{}, error) {
	dec := json.NewDecoder(strings.NewReader(expr))
	dec.UseNumber()

	var raw interface{}
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	var trailing interface{}
	if err := dec.Decode(&trailing); err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("contains trailing JSON tokens")
	}

	arr, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("expected JSON array, got %T", raw)
	}
	return normalizeJSONNumbers(arr).([]interface{}), nil
}

func exactVariableReference(expr string) (string, bool) {
	if !strings.HasPrefix(expr, "${") || !strings.HasSuffix(expr, "}") {
		return "", false
	}
	ref := strings.TrimSpace(expr[2 : len(expr)-1])
	if ref == "" {
		return "", false
	}
	return ref, true
}

func lookupItemsVariable(ref string, vars map[string]interface{}) (interface{}, error) {
	if vars == nil {
		return nil, fmt.Errorf("items reference ${%s}: no variables available", ref)
	}

	key := ref
	if strings.HasPrefix(ref, "vars.") {
		key = strings.TrimPrefix(ref, "vars.")
	}
	value, ok := vars[key]
	if !ok {
		return nil, fmt.Errorf("items reference ${%s}: undefined variable", ref)
	}
	return value, nil
}

func scalarOrArrayItems(value interface{}) ([]interface{}, error) {
	if items, err := arrayLikeItems(value); err == nil {
		return items, nil
	}
	return []interface{}{value}, nil
}

func arrayLikeItems(value interface{}) ([]interface{}, error) {
	switch v := value.(type) {
	case nil:
		return nil, fmt.Errorf("expected array-like value, got %T", value)
	case []interface{}:
		return v, nil
	case []byte:
		return nil, fmt.Errorf("expected array-like value, got %T", value)
	case []string:
		items := make([]interface{}, len(v))
		for i, item := range v {
			items[i] = item
		}
		return items, nil
	case []int:
		items := make([]interface{}, len(v))
		for i, item := range v {
			items[i] = item
		}
		return items, nil
	case []int64:
		items := make([]interface{}, len(v))
		for i, item := range v {
			items[i] = item
		}
		return items, nil
	case []float64:
		items := make([]interface{}, len(v))
		for i, item := range v {
			items[i] = item
		}
		return items, nil
	case []bool:
		items := make([]interface{}, len(v))
		for i, item := range v {
			items[i] = item
		}
		return items, nil
	}

	// Reflection fallback so typed slices like []BeadRecord, []*Issue, or any
	// other Go slice produced by a parsed step output (bd-682z9) fan out per
	// element instead of being wrapped as a single iteration item.
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		items := make([]interface{}, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			items[i] = rv.Index(i).Interface()
		}
		return items, nil
	default:
		return nil, fmt.Errorf("expected array-like value, got %T", value)
	}
}

func normalizeJSONNumbers(value interface{}) interface{} {
	switch v := value.(type) {
	case []interface{}:
		for i, item := range v {
			v[i] = normalizeJSONNumbers(item)
		}
		return v
	case map[string]interface{}:
		for key, item := range v {
			v[key] = normalizeJSONNumbers(item)
		}
		return v
	case json.Number:
		s := v.String()
		if !strings.ContainsAny(s, ".eE") {
			if n, err := strconv.Atoi(s); err == nil {
				return n
			}
		}
		if n, err := v.Float64(); err == nil {
			return n
		}
		return s
	default:
		return value
	}
}

// parseStructuredBeadsQuery turns "label1,label2,key:value,..." into the
// argv for `br list --json`. Bare tokens become --label flags (AND logic).
// Recognised key:value forms: status/state, type, priority, assignee.
//
// `--limit 0` is appended unconditionally so the foreach iteration set is the
// complete match — without it `br list` returns its default page size and
// foreach silently skips every bead beyond the first page (bd-ftsqw).
func parseStructuredBeadsQuery(expr string) ([]string, error) {
	args := []string{"list", "--json", "--limit", "0"}
	for _, raw := range strings.Split(expr, ",") {
		term := strings.TrimSpace(raw)
		if term == "" {
			continue
		}
		key, val, hasColon := splitKV(term)
		if !hasColon {
			args = append(args, "--label", term)
			continue
		}
		val = strings.TrimSpace(val)
		if val == "" {
			return nil, fmt.Errorf("empty value for %q", key)
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "label":
			args = append(args, "--label", val)
		case "status", "state":
			args = append(args, "--status", val)
		case "type":
			args = append(args, "--type", val)
		case "priority":
			args = append(args, "--priority", val)
		case "assignee":
			args = append(args, "--assignee", val)
		default:
			return nil, fmt.Errorf("unsupported filter key %q (allowed: label, status, type, priority, assignee)", key)
		}
	}
	return args, nil
}

// splitKV splits "key:value" once on the first colon. Returns hasColon=false
// when no colon is present.
func splitKV(s string) (key, value string, hasColon bool) {
	idx := strings.IndexByte(s, ':')
	if idx < 0 {
		return s, "", false
	}
	return s[:idx], s[idx+1:], true
}

// parseBrListJSON parses `br list --json` output (an object with "issues":
// [...]) into []interface{} of map records. Empty output yields zero items.
func parseBrListJSON(out []byte) ([]interface{}, error) {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return []interface{}{}, nil
	}
	var doc struct {
		Issues []map[string]interface{} `json:"issues"`
	}
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return nil, fmt.Errorf("parse br --json output: %w", err)
	}
	items := make([]interface{}, 0, len(doc.Issues))
	for _, issue := range doc.Issues {
		items = append(items, issue)
	}
	return items, nil
}

// ResolvePairs expands a foreach.Pairs expression into a list of pair-record
// maps. The input is a "$(...)" shell command whose stdout follows the
// brennerbot debate-pair format:
//
//	DEBATE-001|H-001|H-002|champion-pane-1|champion-pane-2
//
// Each well-formed line becomes a map with keys
// {debate_id, h1, h2, champion_a, champion_b}, accessible via ${item.h1},
// etc. Malformed lines are logged at Warn and skipped. Empty stdout yields
// zero iterations (not an error).
func (r *IterationSourceResolver) ResolvePairs(ctx context.Context, expr string) ([]interface{}, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return []interface{}{}, nil
	}
	shellCmd, ok := stripShellInvocation(expr)
	if !ok {
		return nil, fmt.Errorf("pairs source must be a shell expression of the form $(...): %q", expr)
	}
	out, err := r.runShell(ctx, shellCmd)
	if err != nil {
		return nil, fmt.Errorf("pairs shell command failed: %w", err)
	}
	return parsePairsOutput(out), nil
}

// parsePairsOutput parses pipe-delimited debate-pair lines. Malformed lines
// are warned and skipped; well-formed lines become record maps.
func parsePairsOutput(out []byte) []interface{} {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return []interface{}{}
	}
	lines := strings.Split(trimmed, "\n")
	items := make([]interface{}, 0, len(lines))
	for lineNo, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) != 5 {
			slog.Warn("pairs source: malformed line skipped",
				"line_no", lineNo+1,
				"line", line,
				"fields", len(fields),
				"want_fields", 5,
			)
			continue
		}
		debateID := strings.TrimSpace(fields[0])
		h1 := strings.TrimSpace(fields[1])
		h2 := strings.TrimSpace(fields[2])
		champA := strings.TrimSpace(fields[3])
		champB := strings.TrimSpace(fields[4])
		if debateID == "" || h1 == "" || h2 == "" || champA == "" || champB == "" {
			slog.Warn("pairs source: line has empty field, skipped",
				"line_no", lineNo+1,
				"line", line,
			)
			continue
		}
		items = append(items, map[string]interface{}{
			"debate_id":  debateID,
			"h1":         h1,
			"h2":         h2,
			"champion_a": champA,
			"champion_b": champB,
		})
	}
	return items
}

// ResolveDebates expands a foreach.Debates expression into a list of
// DEBATE-* bead-ID strings. The input is a "$(...)" shell command whose
// stdout is one ID per line. JSON-array stdout is also supported (so
// `jq '.issues|map(.id)'`-style commands work). Empty stdout yields zero
// iterations.
func (r *IterationSourceResolver) ResolveDebates(ctx context.Context, expr string) ([]interface{}, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return []interface{}{}, nil
	}
	shellCmd, ok := stripShellInvocation(expr)
	if !ok {
		return nil, fmt.Errorf("debates source must be a shell expression of the form $(...): %q", expr)
	}
	out, err := r.runShell(ctx, shellCmd)
	if err != nil {
		return nil, fmt.Errorf("debates shell command failed: %w", err)
	}
	return parseBeadsShellOutput(out)
}

// runShell executes a shell command, defaulting to /bin/sh -c in ProjectDir.
func (r *IterationSourceResolver) runShell(ctx context.Context, shellCmd string) ([]byte, error) {
	if r.RunShell != nil {
		return r.RunShell(ctx, shellCmd)
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", shellCmd)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	if r.ProjectDir != "" {
		cmd.Dir = r.ProjectDir
	}
	return cmd.Output()
}

// runBr executes the br CLI with the given args, defaulting to ProjectDir.
func (r *IterationSourceResolver) runBr(ctx context.Context, args []string) ([]byte, error) {
	if r.RunBr != nil {
		return r.RunBr(ctx, args)
	}
	cmd := exec.CommandContext(ctx, "br", args...)
	if r.ProjectDir != "" {
		cmd.Dir = r.ProjectDir
	}
	return cmd.Output()
}

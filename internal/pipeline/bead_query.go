package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// BeadQueryStep describes a first-class br query operation. It replaces
// shell-piped br|jq command steps when a pipeline needs structured bead data.
type BeadQueryStep struct {
	Label    StringOrList `yaml:"label,omitempty" toml:"label,omitempty" json:"label,omitempty"`
	Status   string       `yaml:"status,omitempty" toml:"status,omitempty" json:"status,omitempty"`
	State    string       `yaml:"state,omitempty" toml:"state,omitempty" json:"state,omitempty"`
	Type     string       `yaml:"type,omitempty" toml:"type,omitempty" json:"type,omitempty"`
	Priority string       `yaml:"priority,omitempty" toml:"priority,omitempty" json:"priority,omitempty"`
	Assignee string       `yaml:"assignee,omitempty" toml:"assignee,omitempty" json:"assignee,omitempty"`
	Filter   string       `yaml:"filter,omitempty" toml:"filter,omitempty" json:"filter,omitempty"`
}

// BeadRecord is the typed subset of `br list --json` issue fields that
// pipeline authors commonly consume downstream. Keep zero and empty fields
// in the wire representation: persisted execution state decodes records into
// maps, which must expose the same fields as fresh native records.
type BeadRecord struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
	Status      string   `json:"status"`
	State       string   `json:"state"`
	Priority    int      `json:"priority"`
	IssueType   string   `json:"issue_type"`
	Assignee    string   `json:"assignee"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
	ClosedAt    string   `json:"closed_at"`
	SourceRepo  string   `json:"source_repo"`
}

func (e *Executor) executeBeadQuery(ctx context.Context, step *Step, workflow *Workflow) StepResult {
	result := StepResult{
		StepID:    step.ID,
		Status:    StatusRunning,
		StartedAt: time.Now(),
		AgentType: "bead_query",
	}

	resolvedQuery, err := e.substituteBeadQueryFields(ctx, step.BeadQuery)
	if err != nil {
		result.Status = StatusFailed
		result.Error = stepRuntimeError(step, "bead_query", "bead_query",
			fmt.Sprintf("variable substitution failed: %v", err),
			"reference declared workflow vars or provide a | fallback for optional values",
			err.Error())
		result.FinishedAt = time.Now()
		return result
	}
	filter, err := compileBeadFilter(resolvedQuery.Filter)
	if err != nil {
		result.Status = StatusFailed
		result.Error = stepRuntimeError(step, "bead_query", "bead_query",
			fmt.Sprintf("filter failed: %v", err),
			"use field==value or field!=value with &&, ||, and parentheses; quote values containing spaces or operators",
			err.Error())
		result.FinishedAt = time.Now()
		return result
	}
	args := resolvedQuery.brListArgs()
	if e.config.DryRun {
		result.Status = StatusCompleted
		// bd-g3mfx: sanitize the substituted args before joining — upstream
		// ${steps.X.output} can carry ANSI/OSC/C0 bytes into bead_query
		// scalar fields, and the dry-run banner would otherwise reach the
		// operator's terminal with control sequences intact. Same attack
		// class bd-82zsc / bd-g40ad close for command/agent/parallel/branch.
		sanitized := make([]string, len(args))
		for i, a := range args {
			sanitized[i] = SanitizeDescriptionForTerminal(a)
		}
		result.Output = dryRunOutput(step, "Would run br "+strings.Join(sanitized, " "))
		result.FinishedAt = time.Now()
		return result
	}

	slog.Info("bead_query step starting",
		"run_id", e.state.RunID,
		"workflow", workflow.Name,
		"step_id", step.ID,
		"agent_type", "bead_query",
	)

	resolver := IterationSourceResolver{
		ProjectDir: e.config.ProjectDir,
		Limits:     e.limits,
		RunBr:      e.config.BeadQueryRunBr,
	}
	evaluationCtx, cancel := e.selectorEvaluationContext(ctx, step)
	defer cancel()
	out, err := resolver.runBr(evaluationCtx, args)
	if err != nil {
		result.Status = StatusFailed
		errorType := "bead_query"
		hint := "verify br is installed and project_dir points at a Beads repo"
		if errors.Is(err, errCommandOutputUnsettled) {
			errorType = "execution_unknown"
			hint = "inspect the original query process and its effects before explicitly authorizing replay"
		}
		result.Error = stepRuntimeError(step, "bead_query", errorType,
			fmt.Sprintf("br list failed: %v", err),
			hint,
			err.Error())
		result.FinishedAt = time.Now()
		return result
	}

	records, err := parseBeadQueryRecords(out)
	if err != nil {
		result.Status = StatusFailed
		result.Error = stepRuntimeError(step, "bead_query", "bead_query",
			fmt.Sprintf("failed to parse br output: %v", err),
			"br list --json should emit an object with an issues array",
			err.Error())
		result.FinishedAt = time.Now()
		return result
	}
	records, err = filterBeadRecords(records, filter)
	if err != nil {
		result.Status = StatusFailed
		result.Error = stepRuntimeError(step, "bead_query", "bead_query",
			fmt.Sprintf("filter failed: %v", err),
			"use field==value or field!=value with &&, ||, and parentheses; quote values containing spaces or operators",
			err.Error())
		result.FinishedAt = time.Now()
		return result
	}

	data, err := json.Marshal(records)
	if err != nil {
		result.Status = StatusFailed
		result.Error = stepRuntimeError(step, "bead_query", "bead_query",
			fmt.Sprintf("failed to encode bead records: %v", err),
			"check for unsupported values in br output",
			err.Error())
		result.FinishedAt = time.Now()
		return result
	}
	if err := evaluationCtx.Err(); err != nil {
		result.Status = StatusFailed
		result.Error = stepRuntimeError(step, "bead_query", "bead_query",
			fmt.Sprintf("query evaluation cancelled: %v", err),
			"check the query deadline before retrying",
			err.Error())
		result.FinishedAt = time.Now()
		return result
	}

	result.Output = string(data)
	result.ParsedData = records
	result.Status = StatusCompleted
	result.FinishedAt = time.Now()

	slog.Info("bead_query step completed",
		"run_id", e.state.RunID,
		"workflow", workflow.Name,
		"step_id", step.ID,
		"agent_type", "bead_query",
		"records", len(records),
	)
	return result
}

// substituteBeadQueryFields resolves ${vars.X}, ${steps.X.output}, ${defaults.X},
// and other supported namespaces inside every bead_query field that becomes a
// br list argument or in-memory filter clause. Strict substitution surfaces
// unresolvable references as an explicit error rather than silently sending
// the literal "${vars.X}" to br, which would otherwise produce empty result
// sets without any diagnostic.
func (e *Executor) substituteBeadQueryFields(ctx context.Context, in *BeadQueryStep) (BeadQueryStep, error) {
	if in == nil {
		return BeadQueryStep{}, nil
	}
	q := *in
	if len(q.Label) > 0 {
		labels := make(StringOrList, 0, len(q.Label))
		for i, label := range q.Label {
			if label == "" {
				labels = append(labels, label)
				continue
			}
			resolved, err := e.substituteStrictCtx(ctx, label)
			if err != nil {
				return BeadQueryStep{}, fmt.Errorf("label[%d]: %w", i, err)
			}
			labels = append(labels, resolved)
		}
		q.Label = labels
	}

	scalarFields := []struct {
		name string
		ptr  *string
	}{
		{"status", &q.Status},
		{"state", &q.State},
		{"type", &q.Type},
		{"priority", &q.Priority},
		{"assignee", &q.Assignee},
		{"filter", &q.Filter},
	}
	for _, f := range scalarFields {
		if *f.ptr == "" {
			continue
		}
		resolved, err := e.substituteStrictCtx(ctx, *f.ptr)
		if err != nil {
			return BeadQueryStep{}, fmt.Errorf("%s: %w", f.name, err)
		}
		*f.ptr = resolved
	}
	return q, nil
}

// substituteStrictCtx resolves variable references with the execution's round
// overrides and returns an error when any reference cannot be resolved.
func (e *Executor) substituteStrictCtx(ctx context.Context, s string) (string, error) {
	// bd-6vp7y: canonical lock order is stateMu before varMu (matches
	// the bd-8wo27 / bd-eslpu convention). Pre-fix this site held
	// varMu first, presenting the same AB-BA risk against any
	// goroutine using the canonical order.
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	e.varMu.RLock()
	defer e.varMu.RUnlock()
	sub := NewSubstitutor(e.state, e.config.Session, e.state.WorkflowID)
	sub.SetDefaults(e.defaults)
	sub.SetMaxDepth(e.limits.MaxSubstitutionDepth)
	if overrides := roundOverridesFromCtx(ctx); overrides != nil {
		sub.SetLocalOverrides(overrides)
	}
	s = e.substituteRuntimeVariables(s)
	return sub.SubstituteStrict(s)
}

func (q *BeadQueryStep) brListArgs() []string {
	args := []string{"list", "--json", "--limit", "0"}
	if q == nil {
		return args
	}
	for _, label := range q.Label {
		label = strings.TrimSpace(label)
		if label != "" {
			args = append(args, "--label", label)
		}
	}
	if q.Status != "" {
		args = append(args, "--status", q.Status)
	} else if q.State != "" {
		args = append(args, "--status", q.State)
	}
	if q.Type != "" {
		args = append(args, "--type", q.Type)
	}
	if q.Priority != "" {
		args = append(args, "--priority", q.Priority)
	}
	if q.Assignee != "" {
		args = append(args, "--assignee", q.Assignee)
	}
	return args
}

func parseBeadQueryRecords(out []byte) ([]BeadRecord, error) {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return []BeadRecord{}, nil
	}
	var doc struct {
		Issues []BeadRecord `json:"issues"`
	}
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return nil, err
	}
	if doc.Issues == nil {
		return []BeadRecord{}, nil
	}
	for i := range doc.Issues {
		if doc.Issues[i].Labels == nil {
			doc.Issues[i].Labels = []string{}
		}
	}
	return doc.Issues, nil
}

func filterBeadRecords(records []BeadRecord, filter filterNode) ([]BeadRecord, error) {
	if filter == nil {
		return records, nil
	}
	out := make([]BeadRecord, 0, len(records))
	for _, record := range records {
		ok, err := filter.eval(FilterContext{Item: record})
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, record)
		}
	}
	return out, nil
}

// compileBeadFilter reuses the foreach expression grammar while retaining
// bead-specific semantics: labels are membership tests, priority is an
// integer, and other fields compare exact strings. Every comparison is
// validated before br is invoked, rather than only along a matching row's
// short-circuit path. The compiled predicate is reused for the whole result.
func compileBeadFilter(expr string) (filterNode, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, nil
	}
	node, err := parseFilterExpression(expr)
	if err != nil {
		return nil, err
	}
	return compileBeadFilterNode(node)
}

func compileBeadFilterNode(node filterNode) (filterNode, error) {
	switch n := node.(type) {
	case filterLogicalNode:
		left, err := compileBeadFilterNode(n.left)
		if err != nil {
			return nil, err
		}
		right, err := compileBeadFilterNode(n.right)
		if err != nil {
			return nil, err
		}
		return filterLogicalNode{op: n.op, left: left, right: right}, nil
	case filterCompareNode:
		field := strings.ToLower(strings.TrimSpace(n.left.value))
		if n.left.quoted {
			return nil, fmt.Errorf("bead field %q must not be quoted", field)
		}
		if _, ok := beadRecordField(BeadRecord{}, field); !ok {
			return nil, fmt.Errorf("unknown bead field %q", field)
		}
		compiled := beadFilterComparison{field: field, value: n.right.value, negate: n.op == "!="}
		if field == "priority" {
			priority, err := strconv.Atoi(n.right.value)
			if err != nil {
				return nil, fmt.Errorf("bead priority requires an integer, got %q", n.right.value)
			}
			compiled.priority = priority
		}
		return compiled, nil
	default:
		return nil, fmt.Errorf("bead filters require field==value or field!=value comparisons")
	}
}

type beadFilterComparison struct {
	field    string
	value    string
	priority int
	negate   bool
}

func (n beadFilterComparison) eval(ctx FilterContext) (bool, error) {
	record, ok := ctx.Item.(BeadRecord)
	if !ok {
		return false, fmt.Errorf("bead filter requires a bead record")
	}
	var matches bool
	switch n.field {
	case "label", "labels":
		for _, label := range record.Labels {
			if label == n.value {
				matches = true
				break
			}
		}
	case "priority":
		matches = record.Priority == n.priority
	default:
		got, _ := beadRecordField(record, n.field) // Validated during compilation.
		matches = got == n.value
	}
	if n.negate {
		return !matches, nil
	}
	return matches, nil
}

func beadRecordField(record BeadRecord, field string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "id":
		return record.ID, true
	case "title":
		return record.Title, true
	case "description":
		return record.Description, true
	case "status":
		return record.Status, true
	case "state":
		if record.State != "" {
			return record.State, true
		}
		return record.Status, true
	case "priority":
		return strconv.Itoa(record.Priority), true
	case "type", "issue_type":
		return record.IssueType, true
	case "assignee":
		return record.Assignee, true
	case "label", "labels":
		return strings.Join(record.Labels, ","), true
	default:
		return "", false
	}
}

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
// pipeline authors commonly consume downstream.
type BeadRecord struct {
	ID          string   `json:"id"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	Status      string   `json:"status,omitempty"`
	State       string   `json:"state,omitempty"`
	Priority    int      `json:"priority,omitempty"`
	IssueType   string   `json:"issue_type,omitempty"`
	Assignee    string   `json:"assignee,omitempty"`
	CreatedAt   string   `json:"created_at,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
	ClosedAt    string   `json:"closed_at,omitempty"`
	SourceRepo  string   `json:"source_repo,omitempty"`
}

func (e *Executor) executeBeadQuery(ctx context.Context, step *Step, workflow *Workflow) StepResult {
	result := StepResult{
		StepID:    step.ID,
		Status:    StatusRunning,
		StartedAt: time.Now(),
		AgentType: "bead_query",
	}

	resolvedQuery, err := e.substituteBeadQueryFields(step.BeadQuery)
	if err != nil {
		result.Status = StatusFailed
		result.Error = stepRuntimeError(step, "bead_query", "bead_query",
			fmt.Sprintf("variable substitution failed: %v", err),
			"reference declared workflow vars or provide a | fallback for optional values",
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
		RunBr:      e.config.BeadQueryRunBr,
	}
	out, err := resolver.runBr(e.commandEvaluationContext(ctx, step), args)
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
	records, err = filterBeadRecords(records, resolvedQuery.Filter)
	if err != nil {
		result.Status = StatusFailed
		result.Error = stepRuntimeError(step, "bead_query", "bead_query",
			fmt.Sprintf("filter failed: %v", err),
			"use simple field==value or field!=value clauses joined by && or ||",
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
func (e *Executor) substituteBeadQueryFields(in *BeadQueryStep) (BeadQueryStep, error) {
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
			resolved, err := e.substituteStrict(label)
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
		resolved, err := e.substituteStrict(*f.ptr)
		if err != nil {
			return BeadQueryStep{}, fmt.Errorf("%s: %w", f.name, err)
		}
		*f.ptr = resolved
	}
	return q, nil
}

// substituteStrict resolves variable references using the standard substitutor
// stack and returns an error when any reference cannot be resolved. Mirrors
// substituteVariables but propagates errors so callers can fail closed.
func (e *Executor) substituteStrict(s string) (string, error) {
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
	return doc.Issues, nil
}

func filterBeadRecords(records []BeadRecord, filter string) ([]BeadRecord, error) {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return records, nil
	}
	out := make([]BeadRecord, 0, len(records))
	for _, record := range records {
		ok, err := matchBeadFilter(record, filter)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, record)
		}
	}
	return out, nil
}

func matchBeadFilter(record BeadRecord, expr string) (bool, error) {
	// bd-3at8h: the existing string-split parser cannot handle the
	// parenthesized E2.6 filter shapes the docs imply (e.g.
	// `status==open && (label==hypothesis || label==phase-4)`). Splitting
	// on `||` first chops the parens into clauses like `(label==hypothesis`
	// and `label==phase-4)` that then fail with confusing field/value
	// errors or silently match the wrong record set. Reject parentheses
	// up front with an actionable error so authors don't get a wrong-set
	// surprise; the deeper "reuse foreach filter parser" path is tracked
	// for a follow-up.
	if strings.ContainsAny(expr, "()") {
		return false, fmt.Errorf("bead_query.filter does not yet support parenthesized expressions (got %q): rewrite without grouping (e.g. status==open && label==hypothesis || status==open && label==phase-4) or pre-narrow with bead_query.labels/status", expr)
	}
	orParts := strings.Split(expr, "||")
	for _, orPart := range orParts {
		andParts := strings.Split(orPart, "&&")
		all := true
		for _, rawClause := range andParts {
			ok, err := matchBeadFilterClause(record, rawClause)
			if err != nil {
				return false, err
			}
			if !ok {
				all = false
				break
			}
		}
		if all {
			return true, nil
		}
	}
	return false, nil
}

func matchBeadFilterClause(record BeadRecord, rawClause string) (bool, error) {
	clause := strings.TrimSpace(rawClause)
	if clause == "" {
		return false, fmt.Errorf("empty filter clause")
	}

	// bd-o4ji9: detect the operator by the earliest occurrence of `==` or
	// `!=`. Preferring `==` regardless of position mis-parsed clauses like
	// `field!=foo==bar`, where SplitN(clause, "==", 2) splits cleanly into
	// two parts but locks `op` to `==` and assigns the literal `field!=foo`
	// as the field name.
	eqIdx := strings.Index(clause, "==")
	neIdx := strings.Index(clause, "!=")
	op := ""
	splitIdx := -1
	switch {
	case eqIdx >= 0 && neIdx >= 0:
		if eqIdx <= neIdx {
			op, splitIdx = "==", eqIdx
		} else {
			op, splitIdx = "!=", neIdx
		}
	case eqIdx >= 0:
		op, splitIdx = "==", eqIdx
	case neIdx >= 0:
		op, splitIdx = "!=", neIdx
	}
	if splitIdx < 0 {
		return false, fmt.Errorf("unsupported filter clause %q", clause)
	}
	parts := []string{clause[:splitIdx], clause[splitIdx+2:]}

	// bd-6uylc: beadRecordField lowercases the field internally, so the
	// label/labels list-membership branch must compare against the same
	// canonical form. Otherwise "Label==alpha" returned the comma-joined
	// label string, mismatched the user's value, and silently excluded
	// records that should match.
	field := strings.ToLower(strings.TrimSpace(parts[0]))
	want := trimFilterValue(parts[1])
	got, ok := beadRecordField(record, field)
	if !ok {
		return false, fmt.Errorf("unknown bead field %q", field)
	}

	matches := got == want
	if field == "label" || field == "labels" {
		matches = false
		for _, label := range record.Labels {
			if label == want {
				matches = true
				break
			}
		}
	}
	if op == "!=" {
		return !matches, nil
	}
	return matches, nil
}

func trimFilterValue(raw string) string {
	return strings.Trim(strings.TrimSpace(raw), `"'`)
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

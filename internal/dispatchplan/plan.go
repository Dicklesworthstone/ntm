// Package dispatchplan budgets context before sending work to an
// agent. Given a bead, recent Agent Mail thread context, CASS/CM
// search results, and agent-type constraints, Plan() decides what to
// include and what to omit so the agent receives a focused prompt
// that fits within a token budget.
//
// The planner is pure: callers gather candidates from each source
// (mail, cass, cm, history) and pass them in. Plan returns a
// structured PlanReport explaining every inclusion and every
// omission with a stable reason code so dashboards and the robot
// surface can render the decision without re-deriving it.
//
// First slice keeps the heuristic deliberately simple: priority-
// ordered sources, greedy fill until budget is exhausted, omit-with-
// reason for everything that didn't fit. Future slices can add
// per-token cost models or relevance scoring on top of the same
// PlanReport shape.
//
// Required candidates are system-level headers (agent identity, run
// metadata) that must always reach the agent. They bypass every gate
// except duplicate-ID — i.e., the EstimatedTokens<=0, source-disabled,
// agent-type-filter, and budget gates are all skipped for Required.
// A Required candidate whose ID has already been emitted is still
// recorded as ReasonOmittedDuplicate so the report reflects the
// final emitted set.
//
// See bd-fxj4f.11, bd-njp52.
package dispatchplan

import (
	"sort"
	"strings"
	"time"
)

// Source names a content origin so the planner can apply per-source
// priority ordering and the report can group included/omitted items.
type Source string

const (
	SourceBead    Source = "bead"
	SourceMail    Source = "mail"
	SourceCASS    Source = "cass"
	SourceCM      Source = "cm"
	SourceHistory Source = "history"
)

// Reason explains why a candidate was included or omitted. Stable
// strings consumers may route on.
type Reason string

const (
	ReasonIncluded         Reason = "included"
	ReasonRequiredHeader   Reason = "included_required_header"
	ReasonOmittedBudget    Reason = "omitted_budget_exhausted"
	ReasonOmittedAgentType Reason = "omitted_agent_type_filter"
	ReasonOmittedEmpty     Reason = "omitted_empty"
	ReasonOmittedDuplicate Reason = "omitted_duplicate"
	ReasonOmittedSourceOff Reason = "omitted_source_disabled"
)

// Candidate is one piece of context the planner can include in the
// dispatch prompt. The caller assigns the Source, an optional ID for
// dedupe, the EstimatedTokens cost, an integer Priority (lower =
// more important), and an optional AgentTypeFilter list.
//
// Body itself is not inspected by the planner — only its size in
// EstimatedTokens. Callers that have a tokenizer should populate
// EstimatedTokens from it; tests use a fixed-cost-per-line proxy.
type Candidate struct {
	ID              string // dedupe key; if empty, not deduped
	Source          Source
	Priority        int // lower = more important
	EstimatedTokens int // > 0
	// Required marks a system-level header that always lands in the
	// dispatch prompt regardless of caller-side gating. Required
	// candidates bypass the EstimatedTokens<=0, source-disabled,
	// agent-type-filter, AND budget gates; they still respect the
	// duplicate-ID gate (a Required candidate whose ID was already
	// emitted is recorded as ReasonOmittedDuplicate) (bd-njp52).
	Required        bool      // bypasses every gate except duplicate-ID — see Reason mapping
	AgentTypeFilter []string  // when non-empty, non-Required candidates are only included if AgentType ∈ filter
	Body            string    // opaque; not inspected by the planner
	Description     string    // short label rendered in the report
	CreatedAt       time.Time // tiebreaker for equal-priority candidates
}

// Inputs configures one Plan call.
type Inputs struct {
	AgentType    string
	BudgetTokens int
	Candidates   []Candidate

	// DisabledSources lets the operator turn off whole sources
	// (e.g. CASS off when the index is rebuilding) without filtering
	// the candidate list themselves.
	DisabledSources []Source

	Now time.Time
}

// Decision is one row in the report explaining what happened to one
// candidate.
type Decision struct {
	ID              string `json:"id,omitempty"`
	Source          Source `json:"source"`
	Description     string `json:"description,omitempty"`
	Priority        int    `json:"priority"`
	EstimatedTokens int    `json:"estimated_tokens"`
	Reason          Reason `json:"reason"`
	Detail          string `json:"detail,omitempty"`
}

// PlanReport is the full envelope the planner emits.
type PlanReport struct {
	GeneratedAt   time.Time  `json:"generated_at"`
	AgentType     string     `json:"agent_type,omitempty"`
	BudgetTokens  int        `json:"budget_tokens"`
	UsedTokens    int        `json:"used_tokens"`
	IncludedCount int        `json:"included_count"`
	OmittedCount  int        `json:"omitted_count"`
	Decisions     []Decision `json:"decisions"`
	Summary       string     `json:"summary"`
}

// Plan reduces inputs into a PlanReport. Pure: no I/O.
func Plan(in Inputs) PlanReport {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}

	disabled := make(map[Source]struct{}, len(in.DisabledSources))
	for _, s := range in.DisabledSources {
		disabled[s] = struct{}{}
	}

	// Stable sort: required first, then by priority asc, then by
	// CreatedAt asc, then by source asc, then by ID asc — fully
	// deterministic for byte-stable JSON.
	cands := make([]Candidate, len(in.Candidates))
	copy(cands, in.Candidates)
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Required != cands[j].Required {
			return cands[i].Required
		}
		if cands[i].Priority != cands[j].Priority {
			return cands[i].Priority < cands[j].Priority
		}
		if !cands[i].CreatedAt.Equal(cands[j].CreatedAt) {
			return cands[i].CreatedAt.Before(cands[j].CreatedAt)
		}
		if cands[i].Source != cands[j].Source {
			return cands[i].Source < cands[j].Source
		}
		return cands[i].ID < cands[j].ID
	})

	report := PlanReport{
		GeneratedAt:  now.UTC(),
		AgentType:    strings.TrimSpace(in.AgentType),
		BudgetTokens: in.BudgetTokens,
	}

	seenIDs := make(map[string]struct{})
	for _, c := range cands {
		dec := Decision{
			ID:              c.ID,
			Source:          c.Source,
			Description:     c.Description,
			Priority:        c.Priority,
			EstimatedTokens: c.EstimatedTokens,
		}

		switch {
		case c.EstimatedTokens <= 0 && !c.Required:
			dec.Reason = ReasonOmittedEmpty
		case sourceDisabled(disabled, c.Source) && !c.Required:
			dec.Reason = ReasonOmittedSourceOff
			dec.Detail = "source=" + string(c.Source) + " disabled"
		case c.ID != "" && containsKey(seenIDs, c.ID):
			dec.Reason = ReasonOmittedDuplicate
		case !agentTypeAllowed(c.AgentTypeFilter, in.AgentType) && !c.Required:
			dec.Reason = ReasonOmittedAgentType
			if len(c.AgentTypeFilter) > 0 {
				dec.Detail = "filter=" + strings.Join(c.AgentTypeFilter, ",") + " agent=" + in.AgentType
			}
		case c.Required:
			report.UsedTokens += c.EstimatedTokens
			dec.Reason = ReasonRequiredHeader
			report.IncludedCount++
		case in.BudgetTokens > 0 && report.UsedTokens+c.EstimatedTokens > in.BudgetTokens:
			dec.Reason = ReasonOmittedBudget
			dec.Detail = "would_overflow_by=" +
				itoa(report.UsedTokens+c.EstimatedTokens-in.BudgetTokens)
		default:
			report.UsedTokens += c.EstimatedTokens
			dec.Reason = ReasonIncluded
			report.IncludedCount++
		}

		if c.ID != "" && (dec.Reason == ReasonIncluded || dec.Reason == ReasonRequiredHeader) {
			seenIDs[c.ID] = struct{}{}
		}
		report.Decisions = append(report.Decisions, dec)
	}

	report.OmittedCount = len(report.Decisions) - report.IncludedCount
	report.Summary = composeSummary(report)
	return report
}

func sourceDisabled(set map[Source]struct{}, s Source) bool {
	_, ok := set[s]
	return ok
}

func containsKey(m map[string]struct{}, k string) bool {
	_, ok := m[k]
	return ok
}

func agentTypeAllowed(filter []string, agent string) bool {
	if len(filter) == 0 {
		return true
	}
	agent = strings.TrimSpace(strings.ToLower(agent))
	for _, f := range filter {
		if strings.EqualFold(strings.TrimSpace(f), agent) {
			return true
		}
	}
	return false
}

func composeSummary(r PlanReport) string {
	parts := []string{
		"included=" + itoa(r.IncludedCount),
		"omitted=" + itoa(r.OmittedCount),
		"used=" + itoa(r.UsedTokens) + "/" + itoa(r.BudgetTokens),
	}
	if r.AgentType != "" {
		parts = append(parts, "agent="+r.AgentType)
	}
	return strings.Join(parts, " ")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

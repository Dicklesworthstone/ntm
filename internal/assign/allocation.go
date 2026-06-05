package assign

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const (
	// AllocationPlanSchemaVersion is the stable robot JSON schema token for
	// reviewable cross-swarm allocation plans.
	AllocationPlanSchemaVersion = "ntm.allocation_plan.v1"

	defaultAllocationMinScore         = 0.25
	defaultAllocationAlternatives     = 3
	defaultAllocationResourceCost     = 0.50
	defaultAllocationGraphValue       = 0.50
	defaultAllocationResourceHeadroom = 0.65
)

// AllocationDecision is a stable robot token for planner outcomes.
type AllocationDecision string

const (
	AllocationDecisionRecommend   AllocationDecision = "recommend"
	AllocationDecisionAlternative AllocationDecision = "alternative"
	AllocationDecisionReject      AllocationDecision = "reject"
	AllocationDecisionDefer       AllocationDecision = "defer"
	AllocationDecisionNoReadyWork AllocationDecision = "no_ready_work"
	AllocationDecisionNoCapacity  AllocationDecision = "no_capacity"
)

// AllocationReasonCode is a stable robot token explaining a score or decision.
type AllocationReasonCode string

const (
	AllocationReasonNoReadyBeads      AllocationReasonCode = "no_ready_beads"
	AllocationReasonNoAgents          AllocationReasonCode = "no_agents"
	AllocationReasonAgentBusy         AllocationReasonCode = "agent_busy"
	AllocationReasonContextHigh       AllocationReasonCode = "context_high"
	AllocationReasonCapacityExhausted AllocationReasonCode = "capacity_exhausted"
	AllocationReasonGraphValue        AllocationReasonCode = "graph_value"
	AllocationReasonPriorityConflict  AllocationReasonCode = "priority_conflict"
	AllocationReasonUnblockImpact     AllocationReasonCode = "unblock_impact"
	AllocationReasonResourceFit       AllocationReasonCode = "resource_fit"
	AllocationReasonResourcePressure  AllocationReasonCode = "resource_pressure"
	AllocationReasonAgentCapability   AllocationReasonCode = "agent_capability"
	AllocationReasonAgentTypeMismatch AllocationReasonCode = "agent_type_mismatch"
	AllocationReasonFairnessBalance   AllocationReasonCode = "fairness_balance"
	AllocationReasonStarvationRisk    AllocationReasonCode = "starvation_risk"
	AllocationReasonBVMissing         AllocationReasonCode = "bv_missing"
	AllocationReasonPressureMissing   AllocationReasonCode = "pressure_missing"
	AllocationReasonCriticalPressure  AllocationReasonCode = "critical_pressure"
	AllocationReasonBelowThreshold    AllocationReasonCode = "below_threshold"
)

// AllocationInput captures the side-effect-free inputs needed to rank ready
// work across panes, sessions, and agent types.
type AllocationInput struct {
	ReadyBeads          []AllocationReadyBead `json:"ready_beads"`
	Agents              []AllocationAgent     `json:"agents"`
	Sessions            []AllocationSession   `json:"sessions,omitempty"`
	Pressure            AllocationPressure    `json:"pressure,omitempty"`
	Fairness            AllocationFairness    `json:"fairness,omitempty"`
	BVAvailable         bool                  `json:"bv_available"`
	MaxRecommendations  int                   `json:"max_recommendations,omitempty"`
	AlternativesPerBead int                   `json:"alternatives_per_bead,omitempty"`
	MinScore            float64               `json:"min_score,omitempty"`
	Matrix              *CapabilityMatrix     `json:"-"`
}

// AllocationReadyBead is the planner's normalized ready-work row.
type AllocationReadyBead struct {
	ID                  string           `json:"id"`
	Title               string           `json:"title,omitempty"`
	TaskType            TaskType         `json:"task_type,omitempty"`
	Priority            int              `json:"priority"`
	Labels              []string         `json:"labels,omitempty"`
	UnblocksIDs         []string         `json:"unblocks_ids,omitempty"`
	GraphScore          float64          `json:"graph_score,omitempty"`
	PageRank            float64          `json:"pagerank,omitempty"`
	Betweenness         float64          `json:"betweenness,omitempty"`
	CriticalPath        bool             `json:"critical_path,omitempty"`
	ResourceCost        float64          `json:"resource_cost,omitempty"`
	PreferredAgentTypes []tmux.AgentType `json:"preferred_agent_types,omitempty"`
}

// AllocationAgent is the planner's normalized worker row.
type AllocationAgent struct {
	ID                string         `json:"id"`
	Session           string         `json:"session"`
	PaneIndex         int            `json:"pane_index,omitempty"`
	AgentType         tmux.AgentType `json:"agent_type"`
	Idle              bool           `json:"idle"`
	ContextUsage      float64        `json:"context_usage,omitempty"`
	ActiveAssignments int            `json:"active_assignments,omitempty"`
	AssignmentLimit   int            `json:"assignment_limit,omitempty"`
	ResourceHeadroom  float64        `json:"resource_headroom,omitempty"`
	CurrentBeadID     string         `json:"current_bead_id,omitempty"`
}

// AllocationSession captures session-level capacity and fairness signals.
type AllocationSession struct {
	Name              string  `json:"name"`
	ActiveAssignments int     `json:"active_assignments,omitempty"`
	AssignmentLimit   int     `json:"assignment_limit,omitempty"`
	ResourceHeadroom  float64 `json:"resource_headroom,omitempty"`
}

// AllocationPressure captures the resource pressure snapshot used by the plan.
type AllocationPressure struct {
	Available     bool     `json:"available"`
	Level         string   `json:"level,omitempty"`
	Limiting      []string `json:"limiting,omitempty"`
	AgentHeadroom int      `json:"agent_headroom,omitempty"`
}

// AllocationFairness captures recent allocation history and explicit starvation
// markers from scheduler or operator inputs.
type AllocationFairness struct {
	AgentRecentAssignments   map[string]int `json:"agent_recent_assignments,omitempty"`
	SessionRecentAssignments map[string]int `json:"session_recent_assignments,omitempty"`
	StarvedAgents            []string       `json:"starved_agents,omitempty"`
	StarvedSessions          []string       `json:"starved_sessions,omitempty"`
}

// AllocationPlan is the robot-stable planner result. It is intentionally
// advisory; callers decide whether to dispatch any recommendation.
type AllocationPlan struct {
	SchemaVersion   string                     `json:"schema_version"`
	Decision        AllocationDecision         `json:"decision"`
	Summary         AllocationSummary          `json:"summary"`
	Recommendations []AllocationRecommendation `json:"recommendations"`
	UnassignedBeads []string                   `json:"unassigned_beads,omitempty"`
	Logs            []AllocationLogRow         `json:"logs"`
	Warnings        []string                   `json:"warnings,omitempty"`
}

// AllocationSummary aggregates the plan shape for robot consumers.
type AllocationSummary struct {
	ReadyBeads      int  `json:"ready_beads"`
	Agents          int  `json:"agents"`
	Candidates      int  `json:"candidates"`
	Recommended     int  `json:"recommended"`
	Alternatives    int  `json:"alternatives"`
	BVMissing       bool `json:"bv_missing,omitempty"`
	PressureMissing bool `json:"pressure_missing,omitempty"`
}

// AllocationRecommendation is a selected bead-to-agent recommendation.
type AllocationRecommendation struct {
	BeadID          string                    `json:"bead_id"`
	BeadTitle       string                    `json:"bead_title,omitempty"`
	Priority        int                       `json:"priority"`
	Session         string                    `json:"session"`
	AgentID         string                    `json:"agent_id"`
	PaneIndex       int                       `json:"pane_index,omitempty"`
	AgentType       tmux.AgentType            `json:"agent_type"`
	Score           float64                   `json:"score"`
	ScoreComponents AllocationScoreComponents `json:"score_components"`
	Decision        AllocationDecision        `json:"decision"`
	ReasonCodes     []AllocationReasonCode    `json:"reason_codes"`
	Reason          string                    `json:"reason"`
	Alternatives    []AllocationCandidate     `json:"alternatives,omitempty"`
}

// AllocationCandidate is a scored candidate or alternative.
type AllocationCandidate struct {
	BeadID          string                    `json:"bead_id"`
	BeadTitle       string                    `json:"bead_title,omitempty"`
	Priority        int                       `json:"priority"`
	Session         string                    `json:"session"`
	AgentID         string                    `json:"agent_id"`
	PaneIndex       int                       `json:"pane_index,omitempty"`
	AgentType       tmux.AgentType            `json:"agent_type"`
	Score           float64                   `json:"score"`
	ScoreComponents AllocationScoreComponents `json:"score_components"`
	Decision        AllocationDecision        `json:"decision"`
	ReasonCodes     []AllocationReasonCode    `json:"reason_codes"`
	Reason          string                    `json:"reason"`
}

// AllocationScoreComponents records the scoring surface used in logs and JSON.
type AllocationScoreComponents struct {
	GraphValue     float64 `json:"graph_value"`
	Priority       float64 `json:"priority"`
	UnblockImpact  float64 `json:"unblock_impact"`
	ResourceFit    float64 `json:"resource_fit"`
	Capability     float64 `json:"capability"`
	Fairness       float64 `json:"fairness"`
	StarvationRisk float64 `json:"starvation_risk"`
	Total          float64 `json:"total"`
}

// AllocationLogRow is a compact audit row for every scored or rejected pair.
type AllocationLogRow struct {
	BeadID          string                    `json:"bead_id,omitempty"`
	Session         string                    `json:"session,omitempty"`
	AgentID         string                    `json:"agent_id,omitempty"`
	AgentType       tmux.AgentType            `json:"agent_type,omitempty"`
	ScoreComponents AllocationScoreComponents `json:"score_components"`
	Decision        AllocationDecision        `json:"decision"`
	ReasonCodes     []AllocationReasonCode    `json:"reason_codes"`
}

// AllocationPlanner ranks ready beads against agents without mutating state.
type AllocationPlanner struct {
	matrix *CapabilityMatrix
}

// NewAllocationPlanner creates a planner using the global capability matrix.
func NewAllocationPlanner() *AllocationPlanner {
	return &AllocationPlanner{matrix: GlobalMatrix()}
}

// NewAllocationPlannerWithMatrix creates a planner with an injected capability matrix.
func NewAllocationPlannerWithMatrix(matrix *CapabilityMatrix) *AllocationPlanner {
	if matrix == nil {
		matrix = GlobalMatrix()
	}
	return &AllocationPlanner{matrix: matrix}
}

// PlanAllocations is a convenience wrapper around AllocationPlanner.Plan.
func PlanAllocations(in AllocationInput) AllocationPlan {
	matrix := in.Matrix
	if matrix == nil {
		matrix = GlobalMatrix()
	}
	return NewAllocationPlannerWithMatrix(matrix).Plan(in)
}

// Plan builds a reviewable allocation plan from normalized inputs.
func (p *AllocationPlanner) Plan(in AllocationInput) AllocationPlan {
	plan := AllocationPlan{
		SchemaVersion:   AllocationPlanSchemaVersion,
		Decision:        AllocationDecisionRecommend,
		Recommendations: []AllocationRecommendation{},
		Logs:            []AllocationLogRow{},
		Summary: AllocationSummary{
			ReadyBeads:      len(in.ReadyBeads),
			Agents:          len(in.Agents),
			BVMissing:       !in.BVAvailable,
			PressureMissing: !in.Pressure.Available,
		},
	}

	if !in.BVAvailable {
		plan.Warnings = append(plan.Warnings, "bv graph data unavailable; using neutral graph value")
	}
	if !in.Pressure.Available {
		plan.Warnings = append(plan.Warnings, "pressure snapshot unavailable; using conservative resource fit")
	}

	if len(in.ReadyBeads) == 0 {
		plan.Decision = AllocationDecisionNoReadyWork
		plan.Logs = append(plan.Logs, AllocationLogRow{
			Decision:    AllocationDecisionNoReadyWork,
			ReasonCodes: []AllocationReasonCode{AllocationReasonNoReadyBeads},
		})
		return plan
	}
	if len(in.Agents) == 0 {
		plan.Decision = AllocationDecisionNoCapacity
		plan.UnassignedBeads = readyBeadIDs(in.ReadyBeads)
		plan.Logs = append(plan.Logs, AllocationLogRow{
			Decision:    AllocationDecisionNoCapacity,
			ReasonCodes: []AllocationReasonCode{AllocationReasonNoAgents},
		})
		return plan
	}

	minScore := in.MinScore
	if minScore <= 0 {
		minScore = defaultAllocationMinScore
	}
	alternativeLimit := in.AlternativesPerBead
	if alternativeLimit <= 0 {
		alternativeLimit = defaultAllocationAlternatives
	}
	maxRecommendations := in.MaxRecommendations
	if maxRecommendations <= 0 || maxRecommendations > len(in.ReadyBeads) {
		maxRecommendations = len(in.ReadyBeads)
	}

	sessions := allocationSessionsByName(in.Sessions)
	candidatesByBead := make(map[string][]AllocationCandidate)
	allCandidates := make([]AllocationCandidate, 0, len(in.ReadyBeads)*len(in.Agents))

	for _, bead := range in.ReadyBeads {
		for _, worker := range in.Agents {
			candidate := p.scoreCandidate(in, sessions, bead, worker)

			if allocationValueNotEqual(candidate.Decision, AllocationDecisionAlternative) {
				plan.Logs = append(plan.Logs, candidate.logRow())
				continue
			}
			if candidate.Score < minScore {
				candidate.Decision = AllocationDecisionReject
				candidate.ReasonCodes = appendMissingReason(candidate.ReasonCodes, AllocationReasonBelowThreshold)
				candidate.Reason = buildAllocationReason(candidate.ReasonCodes)
				plan.Logs = append(plan.Logs, candidate.logRow())
				continue
			}

			plan.Logs = append(plan.Logs, candidate.logRow())
			candidatesByBead[bead.ID] = append(candidatesByBead[bead.ID], candidate)
			allCandidates = append(allCandidates, candidate)
		}
	}

	for beadID := range candidatesByBead {
		sortAllocationCandidates(candidatesByBead[beadID])
	}
	sortAllocationCandidates(allCandidates)
	plan.Summary.Candidates = len(allCandidates)

	if len(allCandidates) == 0 {
		plan.Decision = allocationNoCandidateDecision(in.Pressure)
		plan.UnassignedBeads = readyBeadIDs(in.ReadyBeads)
		return plan
	}

	usedBeads := make(map[string]bool)
	usedAgents := make(map[string]bool)
	for _, candidate := range allCandidates {
		if len(plan.Recommendations) >= maxRecommendations {
			break
		}
		if usedBeads[candidate.BeadID] || usedAgents[candidate.AgentID] {
			continue
		}

		candidate.Decision = AllocationDecisionRecommend
		recommendation := AllocationRecommendation{
			BeadID:          candidate.BeadID,
			BeadTitle:       candidate.BeadTitle,
			Priority:        candidate.Priority,
			Session:         candidate.Session,
			AgentID:         candidate.AgentID,
			PaneIndex:       candidate.PaneIndex,
			AgentType:       candidate.AgentType,
			Score:           candidate.Score,
			ScoreComponents: candidate.ScoreComponents,
			Decision:        AllocationDecisionRecommend,
			ReasonCodes:     candidate.ReasonCodes,
			Reason:          candidate.Reason,
			Alternatives:    allocationAlternatives(candidatesByBead[candidate.BeadID], candidate.AgentID, alternativeLimit),
		}
		plan.Recommendations = append(plan.Recommendations, recommendation)
		plan.Summary.Alternatives += len(recommendation.Alternatives)
		usedBeads[candidate.BeadID] = true
		usedAgents[candidate.AgentID] = true
	}

	plan.Summary.Recommended = len(plan.Recommendations)
	for _, bead := range in.ReadyBeads {
		if !usedBeads[bead.ID] {
			plan.UnassignedBeads = append(plan.UnassignedBeads, bead.ID)
		}
	}
	if len(plan.Recommendations) == 0 {
		plan.Decision = AllocationDecisionNoCapacity
	}

	return plan
}

func (p *AllocationPlanner) scoreCandidate(
	in AllocationInput,
	sessions map[string]AllocationSession,
	bead AllocationReadyBead,
	worker AllocationAgent,
) AllocationCandidate {
	reasons := make([]AllocationReasonCode, 0, 8)
	components := AllocationScoreComponents{}
	session := sessions[worker.Session]

	candidate := AllocationCandidate{
		BeadID:    bead.ID,
		BeadTitle: bead.Title,
		Priority:  bead.Priority,
		Session:   worker.Session,
		AgentID:   worker.ID,
		PaneIndex: worker.PaneIndex,
		AgentType: worker.AgentType,
		Decision:  AllocationDecisionAlternative,
	}

	if !worker.Idle {
		candidate.Decision = AllocationDecisionReject
		candidate.ReasonCodes = []AllocationReasonCode{AllocationReasonAgentBusy}
		candidate.Reason = buildAllocationReason(candidate.ReasonCodes)
		return candidate
	}
	if worker.ContextUsage >= 0.95 {
		candidate.Decision = AllocationDecisionReject
		candidate.ReasonCodes = []AllocationReasonCode{AllocationReasonContextHigh}
		candidate.Reason = buildAllocationReason(candidate.ReasonCodes)
		return candidate
	}
	if allocationLimitReached(worker.ActiveAssignments, worker.AssignmentLimit) ||
		allocationLimitReached(session.ActiveAssignments, session.AssignmentLimit) {
		candidate.Decision = AllocationDecisionReject
		candidate.ReasonCodes = []AllocationReasonCode{AllocationReasonCapacityExhausted}
		candidate.Reason = buildAllocationReason(candidate.ReasonCodes)
		return candidate
	}
	if in.Pressure.Available && normalizedPressureLevel(in.Pressure.Level) == "critical" && in.Pressure.AgentHeadroom <= 0 {
		candidate.Decision = AllocationDecisionDefer
		candidate.ReasonCodes = []AllocationReasonCode{AllocationReasonCriticalPressure}
		candidate.Reason = buildAllocationReason(candidate.ReasonCodes)
		return candidate
	}

	components.GraphValue = allocationGraphValue(bead, in.BVAvailable, &reasons)
	components.Priority = allocationPriorityValue(bead.Priority, &reasons)
	components.UnblockImpact = allocationUnblockImpact(bead, &reasons)
	components.Capability = p.allocationCapability(bead, worker, &reasons)
	components.ResourceFit = allocationResourceFit(in.Pressure, session, bead, worker, &reasons)
	components.Fairness, components.StarvationRisk = allocationFairnessScore(in.Fairness, session, worker, &reasons)
	components.Total = allocationTotalScore(components)

	if components.ResourceFit >= 0.70 {
		reasons = appendMissingReason(reasons, AllocationReasonResourceFit)
	}
	if components.Capability >= 0.75 && !hasReason(reasons, AllocationReasonAgentTypeMismatch) {
		reasons = appendMissingReason(reasons, AllocationReasonAgentCapability)
	}

	candidate.Score = roundAllocationScore(components.Total)
	components.Total = candidate.Score
	candidate.ScoreComponents = roundAllocationComponents(components)
	candidate.ReasonCodes = reasons
	candidate.Reason = buildAllocationReason(reasons)
	return candidate
}

func allocationGraphValue(bead AllocationReadyBead, bvAvailable bool, reasons *[]AllocationReasonCode) float64 {
	if !bvAvailable {
		*reasons = appendMissingReason(*reasons, AllocationReasonBVMissing)
		return defaultAllocationGraphValue
	}

	score := clampScore(bead.GraphScore)
	if score == 0 {
		unblocks := min(float64(len(bead.UnblocksIDs))/5.0, 1.0)
		score = clampScore(bead.PageRank*0.55 + bead.Betweenness*0.30 + unblocks*0.15)
	}
	if bead.CriticalPath && score < 0.80 {
		score = 0.80
	}
	if score >= 0.65 || bead.CriticalPath {
		*reasons = appendMissingReason(*reasons, AllocationReasonGraphValue)
	}
	return score
}

func allocationPriorityValue(priority int, reasons *[]AllocationReasonCode) float64 {
	switch {
	case priority <= 0:
		*reasons = appendMissingReason(*reasons, AllocationReasonPriorityConflict)
		return 1.00
	case priority == 1:
		*reasons = appendMissingReason(*reasons, AllocationReasonPriorityConflict)
		return 0.85
	case priority == 2:
		return 0.60
	case priority == 3:
		return 0.35
	default:
		return 0.15
	}
}

func allocationUnblockImpact(bead AllocationReadyBead, reasons *[]AllocationReasonCode) float64 {
	score := min(float64(len(bead.UnblocksIDs))/5.0, 1.0)
	if bead.CriticalPath && score < 0.75 {
		score = 0.75
	}
	if score > 0 {
		*reasons = appendMissingReason(*reasons, AllocationReasonUnblockImpact)
	}
	return score
}

func (p *AllocationPlanner) allocationCapability(
	bead AllocationReadyBead,
	worker AllocationAgent,
	reasons *[]AllocationReasonCode,
) float64 {
	taskType := bead.TaskType
	if taskType == "" {
		taskType = TaskTask
	}
	score := p.matrix.GetScore(worker.AgentType, taskType)
	if len(bead.PreferredAgentTypes) > 0 && !allocationAgentTypeAllowed(worker.AgentType, bead.PreferredAgentTypes) {
		*reasons = appendMissingReason(*reasons, AllocationReasonAgentTypeMismatch)
		return clampScore(score * 0.35)
	}
	return clampScore(score)
}

func allocationResourceFit(
	pressure AllocationPressure,
	session AllocationSession,
	bead AllocationReadyBead,
	worker AllocationAgent,
	reasons *[]AllocationReasonCode,
) float64 {
	headroom := defaultAllocationResourceHeadroom
	if worker.ResourceHeadroom > 0 {
		headroom = clampScore(worker.ResourceHeadroom)
	} else if session.ResourceHeadroom > 0 {
		headroom = clampScore(session.ResourceHeadroom)
	} else if worker.ContextUsage > 0 {
		headroom = clampScore(1.0 - worker.ContextUsage)
	}

	if !pressure.Available {
		*reasons = appendMissingReason(*reasons, AllocationReasonPressureMissing)
		return clampScore((headroom + defaultAllocationResourceHeadroom) / 2)
	}

	factor := allocationPressureFactor(pressure.Level)
	if factor < 0.90 {
		*reasons = appendMissingReason(*reasons, AllocationReasonResourcePressure)
	}

	cost := bead.ResourceCost
	if cost <= 0 {
		cost = defaultAllocationResourceCost
	}
	score := headroom * factor
	if cost > defaultAllocationResourceCost {
		score -= (cost - defaultAllocationResourceCost) * 0.35
	}
	if pressure.AgentHeadroom == 0 && factor <= 0.55 {
		score *= 0.50
	}
	return clampScore(score)
}

func allocationFairnessScore(
	fairness AllocationFairness,
	session AllocationSession,
	worker AllocationAgent,
	reasons *[]AllocationReasonCode,
) (float64, float64) {
	recent := worker.ActiveAssignments
	if fairness.AgentRecentAssignments != nil {
		recent += fairness.AgentRecentAssignments[worker.ID]
	}
	if fairness.SessionRecentAssignments != nil {
		recent += fairness.SessionRecentAssignments[worker.Session] / 2
	}
	recent += session.ActiveAssignments / 2

	score := 1.0 / (1.0 + float64(max(recent, 0)))
	if recent == 0 {
		*reasons = appendMissingReason(*reasons, AllocationReasonFairnessBalance)
	}

	starved := 0.0
	if containsString(fairness.StarvedAgents, worker.ID) || containsString(fairness.StarvedSessions, worker.Session) {
		starved = 1.0
		*reasons = appendMissingReason(*reasons, AllocationReasonStarvationRisk)
	}
	return clampScore(score), starved
}

func allocationTotalScore(c AllocationScoreComponents) float64 {
	return clampScore(
		c.GraphValue*0.24 +
			c.Priority*0.22 +
			c.UnblockImpact*0.10 +
			c.ResourceFit*0.18 +
			c.Capability*0.14 +
			c.Fairness*0.06 +
			c.StarvationRisk*0.06,
	)
}

func allocationSessionsByName(sessions []AllocationSession) map[string]AllocationSession {
	byName := make(map[string]AllocationSession, len(sessions))
	for _, session := range sessions {
		byName[session.Name] = session
	}
	return byName
}

func allocationLimitReached(active, limit int) bool {
	return limit > 0 && active >= limit
}

func allocationAgentTypeAllowed(agentType tmux.AgentType, allowed []tmux.AgentType) bool {
	canonicalAgentType := agentType.Canonical()
	for _, item := range allowed {
		if canonicalAgentType == item.Canonical() {
			return true
		}
	}
	return false
}

func allocationPressureFactor(level string) float64 {
	switch normalizedPressureLevel(level) {
	case "low":
		return 1.00
	case "normal":
		return 0.95
	case "elevated":
		return 0.78
	case "high":
		return 0.55
	case "critical":
		return 0.20
	default:
		return 0.85
	}
}

func normalizedPressureLevel(level string) string {
	trimmed := strings.TrimSpace(strings.ToLower(level))
	if trimmed == "" {
		return "normal"
	}
	return trimmed
}

func allocationNoCandidateDecision(pressure AllocationPressure) AllocationDecision {
	if pressure.Available && normalizedPressureLevel(pressure.Level) == "critical" {
		return AllocationDecisionDefer
	}
	return AllocationDecisionNoCapacity
}

func readyBeadIDs(beads []AllocationReadyBead) []string {
	ids := make([]string, 0, len(beads))
	for _, bead := range beads {
		ids = append(ids, bead.ID)
	}
	return ids
}

func sortAllocationCandidates(candidates []AllocationCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority < candidates[j].Priority
		}
		if candidates[i].BeadID != candidates[j].BeadID {
			return candidates[i].BeadID < candidates[j].BeadID
		}
		return candidates[i].AgentID < candidates[j].AgentID
	})
}

func allocationAlternatives(candidates []AllocationCandidate, selectedAgentID string, limit int) []AllocationCandidate {
	alternatives := make([]AllocationCandidate, 0, min(len(candidates), limit))
	for _, candidate := range candidates {
		if allocationValueEqual(candidate.AgentID, selectedAgentID) {
			continue
		}
		candidate.Decision = AllocationDecisionAlternative
		alternatives = append(alternatives, candidate)
		if len(alternatives) >= limit {
			break
		}
	}
	return alternatives
}

func (c AllocationCandidate) logRow() AllocationLogRow {
	return AllocationLogRow{
		BeadID:          c.BeadID,
		Session:         c.Session,
		AgentID:         c.AgentID,
		AgentType:       c.AgentType,
		ScoreComponents: c.ScoreComponents,
		Decision:        c.Decision,
		ReasonCodes:     c.ReasonCodes,
	}
}

func appendMissingReason(reasons []AllocationReasonCode, reason AllocationReasonCode) []AllocationReasonCode {
	if hasReason(reasons, reason) {
		return reasons
	}
	return append(reasons, reason)
}

func hasReason(reasons []AllocationReasonCode, reason AllocationReasonCode) bool {
	for _, item := range reasons {
		if item == reason {
			return true
		}
	}
	return false
}

func containsString(items []string, needle string) bool {
	for _, item := range items {
		if item == needle {
			return true
		}
	}
	return false
}

func allocationValueEqual[T comparable](left, right T) bool {
	return left == right
}

func allocationValueNotEqual[T comparable](left, right T) bool {
	return !allocationValueEqual(left, right)
}

func buildAllocationReason(reasons []AllocationReasonCode) string {
	if len(reasons) == 0 {
		return "balanced graph value, resource fit, capability, and fairness signals"
	}
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		parts = append(parts, allocationReasonText(reason))
	}
	return strings.Join(parts, "; ")
}

func allocationReasonText(reason AllocationReasonCode) string {
	switch reason {
	case AllocationReasonNoReadyBeads:
		return "no ready beads"
	case AllocationReasonNoAgents:
		return "no agents available"
	case AllocationReasonAgentBusy:
		return "agent is already busy"
	case AllocationReasonContextHigh:
		return "agent context usage is too high"
	case AllocationReasonCapacityExhausted:
		return "assignment capacity is exhausted"
	case AllocationReasonGraphValue:
		return "high graph value"
	case AllocationReasonPriorityConflict:
		return "priority conflict favors urgent work"
	case AllocationReasonUnblockImpact:
		return "unblocks downstream work"
	case AllocationReasonResourceFit:
		return "resource fit is healthy"
	case AllocationReasonResourcePressure:
		return "resource pressure reduces fit"
	case AllocationReasonAgentCapability:
		return "agent capability matches task type"
	case AllocationReasonAgentTypeMismatch:
		return "agent type does not match preferred type"
	case AllocationReasonFairnessBalance:
		return "fairness favors a less-used agent"
	case AllocationReasonStarvationRisk:
		return "starvation risk boosts this lane"
	case AllocationReasonBVMissing:
		return "bv graph data missing"
	case AllocationReasonPressureMissing:
		return "pressure data missing"
	case AllocationReasonCriticalPressure:
		return "critical pressure defers new allocation"
	case AllocationReasonBelowThreshold:
		return "score below allocation threshold"
	default:
		return fmt.Sprintf("reason %s", reason)
	}
}

func roundAllocationComponents(c AllocationScoreComponents) AllocationScoreComponents {
	c.GraphValue = roundAllocationScore(c.GraphValue)
	c.Priority = roundAllocationScore(c.Priority)
	c.UnblockImpact = roundAllocationScore(c.UnblockImpact)
	c.ResourceFit = roundAllocationScore(c.ResourceFit)
	c.Capability = roundAllocationScore(c.Capability)
	c.Fairness = roundAllocationScore(c.Fairness)
	c.StarvationRisk = roundAllocationScore(c.StarvationRisk)
	c.Total = roundAllocationScore(c.Total)
	return c
}

func roundAllocationScore(score float64) float64 {
	return float64(int(score*1000+0.5)) / 1000
}

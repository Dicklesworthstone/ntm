package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Placeability verdicts. `ntm assign` records exactly one verdict per pane in
// the session, naming the first gate that excluded it (in evaluation order),
// so "no idle agents" can be told apart from "every seat is fenced by an
// assignment record", "the agent-type filter excluded them", or "the evidence
// is stale".
const (
	// PlaceabilityPlaceable: the pane can receive work right now.
	PlaceabilityPlaceable = "placeable"
	// PlaceabilityNotAgentPane: the pane runs a user shell or an unrecognized
	// process; it is not part of the agent pool at all.
	PlaceabilityNotAgentPane = "not_agent_pane"
	// PlaceabilityAgentTypeFiltered: excluded by the agent-type filter
	// (--agent, --cc-only, ... or a reassignment --to-type).
	PlaceabilityAgentTypeFiltered = "agent_type_filtered"
	// PlaceabilityActiveAssignment: the pane holds an assigned/working
	// assignment record. Its observed state is still reported: "idle and
	// fenced" is the signature of a leaked claim on an otherwise free pane.
	PlaceabilityActiveAssignment = "active_assignment"
	// The SafeToDispatch refusals, spelled exactly as internal/status reports
	// them so the vocabulary is shared across surfaces.
	PlaceabilityObservationStale = string(statuspkg.DispatchRefusalStale)
	PlaceabilityObservationError = string(statuspkg.DispatchRefusalError)
	PlaceabilityLowConfidence    = string(statuspkg.DispatchRefusalLowConfidence)
	PlaceabilityNotIdle          = string(statuspkg.DispatchRefusalNotIdle)
	// PlaceabilityRoleIneligible: the pane cleared every dispatch gate but is
	// bound to the opposite persona role of the requested template.
	PlaceabilityRoleIneligible = "role_ineligible"
)

// placeabilityVerdictOrder is the gate evaluation order, used to render
// breakdowns deterministically.
var placeabilityVerdictOrder = []string{
	PlaceabilityPlaceable,
	PlaceabilityNotAgentPane,
	PlaceabilityAgentTypeFiltered,
	PlaceabilityActiveAssignment,
	PlaceabilityObservationStale,
	PlaceabilityObservationError,
	PlaceabilityLowConfidence,
	PlaceabilityNotIdle,
	PlaceabilityRoleIneligible,
}

// SkipReasonNoPlaceableAgents is the skipped-bead reason when no pane cleared
// the placeability gates. It replaces the older "no_idle_agents", which read as
// "the seats are busy" even when the panes were idle and merely fenced or
// filtered; the per-pane breakdown lives in AssignOutputEnhanced.Panes.
const SkipReasonNoPlaceableAgents = "no_placeable_agents"

// AssignPanePlaceability is one pane's placeability verdict in assign output.
// `state` answers "what is this agent doing?"; `placeable` answers "can I
// dispatch here?". They are different questions: an idle pane can be fenced by
// an assignment record and a waiting pane can be perfectly placeable.
type AssignPanePlaceability struct {
	PaneID     string `json:"pane_id"`
	PaneTarget string `json:"pane_target"`
	AgentType  string `json:"agent_type"`
	// State and Confidence are the observed classification when one was
	// available. They are omitted for panes rejected before observation.
	State      string  `json:"state,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Placeable  bool    `json:"placeable"`
	// Verdict is one of the Placeability* constants.
	Verdict string `json:"verdict"`
	// Detail is a short human explanation of the verdict (the filter value,
	// the bead holding the pane, the observed state, the confidence floor).
	Detail string `json:"detail,omitempty"`
}

// activeAssignmentPaneOwner reports the bead whose active assignment fences
// pane, if any.
func activeAssignmentPaneOwner(active map[string]string, pane tmux.Pane) (string, bool) {
	for _, key := range []string{assignmentPaneStableKey(pane), assignmentPaneTarget(pane)} {
		if beadID, ok := active[key]; ok {
			return beadID, true
		}
	}
	return "", false
}

// evaluateAssignPanePlaceability runs every pane in the session through the
// dispatch gates in order and returns both the agents that cleared them and
// one verdict per pane. It is the single implementation behind the assign
// recommendation path and the auto-reassignment idle-agent scan.
//
// Evidence failures for a candidate pane (a stale session observation, a pane
// missing from it, or a capture error) still abort the whole evaluation: the
// assigner must not act on evidence it cannot trust, and a partial pool would
// hide that from the operator.
func evaluateAssignPanePlaceability(
	panes []tmux.Pane,
	activePanes map[string]string,
	observation statuspkg.SessionObservation,
	agentTypeFilter string,
	now time.Time,
) ([]assignAgentInfo, []AssignPanePlaceability, error) {
	var agents []assignAgentInfo
	verdicts := make([]AssignPanePlaceability, 0, len(panes))
	for _, pane := range panes {
		verdict, agent, err := evaluateAssignPane(pane, activePanes, observation, agentTypeFilter, now)
		if err != nil {
			return nil, nil, err
		}
		verdicts = append(verdicts, verdict)
		if agent != nil {
			agents = append(agents, *agent)
		}
	}
	return agents, verdicts, nil
}

func evaluateAssignPane(
	pane tmux.Pane,
	activePanes map[string]string,
	observation statuspkg.SessionObservation,
	agentTypeFilter string,
	now time.Time,
) (AssignPanePlaceability, *assignAgentInfo, error) {
	agentType := agentTypeForPane(pane)
	verdict := AssignPanePlaceability{
		PaneID:     pane.ID,
		PaneTarget: assignmentPaneTarget(pane),
		AgentType:  agentType,
	}

	if agentType == "user" || agentType == "unknown" {
		verdict.Verdict = PlaceabilityNotAgentPane
		return verdict, nil, nil
	}
	if agentTypeFilter != "" && agentType != agentTypeFilter {
		verdict.Verdict = PlaceabilityAgentTypeFiltered
		verdict.Detail = "filter=" + agentTypeFilter
		return verdict, nil, nil
	}
	if beadID, held := activeAssignmentPaneOwner(activePanes, pane); held {
		verdict.Verdict = PlaceabilityActiveAssignment
		verdict.Detail = "held by " + beadID
		// Best effort only: a fenced pane is excluded regardless of evidence,
		// so its observation must not be able to abort the command.
		if observed, ok := observation.PaneByID(pane.ID); ok {
			verdict.State = string(observed.Current.Status.State)
			verdict.Confidence = observed.Current.Confidence
		}
		return verdict, nil, nil
	}

	paneObservation, err := currentAssignPaneObservation(observation, pane.ID, now)
	if err != nil {
		return AssignPanePlaceability{}, nil, err
	}
	verdict.State = string(paneObservation.Current.Status.State)
	verdict.Confidence = paneObservation.Current.Confidence
	if refusal := paneObservation.DispatchRefusal(); refusal != statuspkg.DispatchRefusalNone {
		verdict.Verdict = string(refusal)
		verdict.Detail = dispatchRefusalDetail(paneObservation, refusal)
		return verdict, nil, nil
	}

	verdict.Placeable = true
	verdict.Verdict = PlaceabilityPlaceable
	return verdict, &assignAgentInfo{
		pane:       pane,
		agentType:  agentType,
		model:      detectModelFromTitle(agentType, pane.Title),
		state:      verdict.State,
		scrollback: paneObservation.RawOutput,
	}, nil
}

// dispatchRefusalDetail explains a SafeToDispatch refusal with the value that
// tripped it.
func dispatchRefusalDetail(p statuspkg.PaneObservation, refusal statuspkg.DispatchRefusal) string {
	switch refusal {
	case statuspkg.DispatchRefusalStale:
		return "freshness=" + string(p.Current.Freshness)
	case statuspkg.DispatchRefusalError:
		return p.Current.Error
	case statuspkg.DispatchRefusalLowConfidence:
		return fmt.Sprintf("confidence %.2f outside [%.2f, 1.00]", p.Current.Confidence, statuspkg.MinimumDispatchConfidence)
	case statuspkg.DispatchRefusalNotIdle:
		return "state=" + string(p.Current.Status.State)
	default:
		return ""
	}
}

// markRoleIneligiblePanes downgrades the verdict of every pane that cleared the
// dispatch gates but was then dropped by persona role gating for template.
// Verdicts recorded by earlier gates are never overwritten.
func markRoleIneligiblePanes(verdicts []AssignPanePlaceability, eligible []assignAgentInfo, template string) {
	survived := make(map[string]struct{}, len(eligible))
	for _, agent := range eligible {
		survived[agent.pane.ID] = struct{}{}
	}
	for i := range verdicts {
		if !verdicts[i].Placeable {
			continue
		}
		if _, ok := survived[verdicts[i].PaneID]; ok {
			continue
		}
		verdicts[i].Placeable = false
		verdicts[i].Verdict = PlaceabilityRoleIneligible
		verdicts[i].Detail = "template=" + strings.ToLower(strings.TrimSpace(template))
	}
}

// placeabilityCounts tallies verdicts for the summary.
func placeabilityCounts(verdicts []AssignPanePlaceability) map[string]int {
	if len(verdicts) == 0 {
		return nil
	}
	counts := make(map[string]int, len(placeabilityVerdictOrder))
	for _, verdict := range verdicts {
		counts[verdict.Verdict]++
	}
	return counts
}

// placeabilityBreakdown renders counts as "2 active_assignment, 1 not_idle" in
// gate order, followed by any verdict this build does not know about.
func placeabilityBreakdown(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	known := make(map[string]struct{}, len(placeabilityVerdictOrder))
	parts := make([]string, 0, len(counts))
	for _, verdict := range placeabilityVerdictOrder {
		known[verdict] = struct{}{}
		if n := counts[verdict]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, verdict))
		}
	}
	var extra []string
	for verdict, n := range counts {
		if _, ok := known[verdict]; !ok && n > 0 {
			extra = append(extra, fmt.Sprintf("%d %s", n, verdict))
		}
	}
	sort.Strings(extra)
	return strings.Join(append(parts, extra...), ", ")
}

// formatPanePlaceabilityLines renders one aligned line per pane for human
// output. Lines are returned unstyled so callers can apply the theme.
func formatPanePlaceabilityLines(verdicts []AssignPanePlaceability) []string {
	if len(verdicts) == 0 {
		return []string{"  Panes: none found in this session"}
	}
	lines := make([]string, 0, len(verdicts)+1)
	lines = append(lines, "  Panes:")
	for _, verdict := range verdicts {
		state := verdict.State
		if state == "" {
			state = "-"
		}
		confidence := "   -"
		if verdict.State != "" {
			confidence = fmt.Sprintf("%.2f", verdict.Confidence)
		}
		mark := "  "
		if verdict.Placeable {
			mark = "ok"
		}
		line := fmt.Sprintf("    %s %-5s %-6s %-8s %-8s %s %s",
			mark, verdict.PaneTarget, verdict.PaneID, verdict.AgentType, state, confidence, verdict.Verdict)
		if verdict.Detail != "" {
			line += " (" + verdict.Detail + ")"
		}
		lines = append(lines, line)
	}
	return lines
}

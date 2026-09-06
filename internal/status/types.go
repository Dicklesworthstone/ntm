// Package status provides agent status detection for NTM.
// It enables monitoring whether agents are idle, working, or in error state
// by analyzing tmux pane activity and output patterns.
package status

import (
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// AgentState represents the current state of an agent
type AgentState string

const (
	// StateIdle indicates the agent is waiting for input at a prompt
	StateIdle AgentState = "idle"
	// StateWorking indicates the agent is actively producing output
	StateWorking AgentState = "working"
	// StateError indicates the agent encountered a problem
	StateError AgentState = "error"
	// StateUnknown indicates the state cannot be determined
	StateUnknown AgentState = "unknown"
)

// Icon returns the visual indicator for a state
func (s AgentState) Icon() string {
	switch s {
	case StateIdle:
		return "\u26aa" // white circle
	case StateWorking:
		return "\U0001f7e2" // green circle
	case StateError:
		return "\U0001f534" // red circle
	default:
		return "\u26ab" // black circle
	}
}

// String returns the string representation of the state
func (s AgentState) String() string {
	return string(s)
}

// ErrorType categorizes detected errors
type ErrorType string

const (
	// ErrorNone indicates no error detected
	ErrorNone ErrorType = ""
	// ErrorRateLimit indicates API rate limiting
	ErrorRateLimit ErrorType = "rate_limit"
	// ErrorCrash indicates the agent process crashed
	ErrorCrash ErrorType = "crash"
	// ErrorAuth indicates authentication failure
	ErrorAuth ErrorType = "auth"
	// ErrorConnection indicates network/connection issues
	ErrorConnection ErrorType = "connection"
	// ErrorGeneric indicates an unspecified error
	ErrorGeneric ErrorType = "error"
)

// Message returns a human-readable description of the error
func (e ErrorType) Message() string {
	switch e {
	case ErrorRateLimit:
		return "Rate limited - too many requests"
	case ErrorCrash:
		return "Agent crashed"
	case ErrorAuth:
		return "Authentication error"
	case ErrorConnection:
		return "Connection error"
	case ErrorGeneric:
		return "Error detected"
	default:
		return ""
	}
}

// String returns the string representation of the error type
func (e ErrorType) String() string {
	return string(e)
}

// AgentStatus represents the full status of an agent pane
type AgentStatus struct {
	// PaneID is the tmux pane identifier (e.g., "%0")
	PaneID string `json:"pane_id"`
	// PaneName is the custom pane title (e.g., "myproject__cc_1")
	PaneName string `json:"pane_name"`
	// AgentType identifies the agent type ("cc", "cod", "gmi", "user")
	AgentType string `json:"agent_type"`
	// State is the current agent state
	State AgentState `json:"state"`
	// ErrorType categorizes the error if State == StateError
	ErrorType ErrorType `json:"error_type,omitempty"`
	// LastActive is when the pane last had output activity
	LastActive time.Time `json:"last_active"`
	// LastOutput contains the last N characters of output (for preview)
	LastOutput string `json:"last_output,omitempty"`
	// ContextUsage is the estimated context usage percentage (0-100)
	ContextUsage float64 `json:"context_usage,omitempty"`
	// TokensUsed is the estimated token count
	TokensUsed int64 `json:"tokens_used,omitempty"`
	// UpdatedAt is when this status was computed
	UpdatedAt time.Time `json:"updated_at"`
}

// ObservationFreshness distinguishes a current reading from historical or
// unavailable evidence. Consumers must not treat stale state as current state.
type ObservationFreshness string

const (
	FreshnessFresh       ObservationFreshness = "fresh"
	FreshnessStale       ObservationFreshness = "stale"
	FreshnessUnavailable ObservationFreshness = "unavailable"
)

// ObservationProvenance identifies the subsystem that produced evidence.
type ObservationProvenance string

const (
	ProvenanceTMUXTopology ObservationProvenance = "tmux_topology"
	ProvenanceTMUXCapture  ObservationProvenance = "tmux_capture"
	ProvenanceDetector     ObservationProvenance = "status_detector"
	ProvenanceLastKnown    ObservationProvenance = "last_known"
)

// ObservationEvidence records one input to a state estimate. Error is data
// about an unavailable source, not the raw pane output.
type ObservationEvidence struct {
	Provenance ObservationProvenance `json:"provenance"`
	ObservedAt time.Time             `json:"observed_at"`
	Freshness  ObservationFreshness  `json:"freshness"`
	Confidence float64               `json:"confidence"`
	Error      string                `json:"error,omitempty"`
}

// StateObservation is one state estimate and the evidence quality attached to
// it. Last-known estimates are carried separately from Current and are always
// marked stale when the current capture is unavailable.
type StateObservation struct {
	Status     AgentStatus           `json:"status"`
	ObservedAt time.Time             `json:"observed_at"`
	Freshness  ObservationFreshness  `json:"freshness"`
	Confidence float64               `json:"confidence"`
	Evidence   []ObservationEvidence `json:"evidence"`
	Error      string                `json:"error,omitempty"`
}

// PaneObservation is the canonical observation for one physical tmux pane.
// RawOutput is intentionally process-private; serialized APIs expose only the
// bounded LastOutput preview inside AgentStatus.
type PaneObservation struct {
	Pane      tmux.PaneRef      `json:"pane"`
	PaneName  string            `json:"pane_name"`
	AgentType string            `json:"agent_type"`
	Current   StateObservation  `json:"current"`
	LastKnown *StateObservation `json:"last_known,omitempty"`
	Metadata  tmux.Pane         `json:"-"`
	RawOutput string            `json:"-"`
}

// MinimumDispatchConfidence is the classification confidence floor below which
// an observation may not authorize a dispatch or a durable state transition.
const MinimumDispatchConfidence = 0.75

const minimumDispatchConfidence = MinimumDispatchConfidence

// ObservationConfidenceIsActionable reports whether evidence is strong enough
// to authorize a durable state transition or dispatch decision.
func ObservationConfidenceIsActionable(confidence float64) bool {
	return confidence >= minimumDispatchConfidence && confidence <= 1
}

// DispatchObservationMaxAge bounds how long an idle observation may authorize
// a new side effect. FreshnessFresh describes capture quality at collection
// time; this age check prevents cached evidence from remaining valid forever.
const DispatchObservationMaxAge = 5 * time.Second

// DispatchObservationIsCurrent reports whether dispatch evidence is recent
// enough to use at now. Missing and future timestamps fail closed.
func DispatchObservationIsCurrent(observedAt, now time.Time) bool {
	if observedAt.IsZero() || now.IsZero() || observedAt.After(now) {
		return false
	}
	return now.Sub(observedAt) <= DispatchObservationMaxAge
}

// DispatchRefusal names the first SafeToDispatch clause an observation fails.
// The empty value means the observation is safe to dispatch to.
type DispatchRefusal string

const (
	// DispatchRefusalNone: the observation passes every SafeToDispatch clause.
	DispatchRefusalNone DispatchRefusal = ""
	// DispatchRefusalStale: the current capture is not fresh.
	DispatchRefusalStale DispatchRefusal = "observation_stale"
	// DispatchRefusalError: the current capture recorded an error.
	DispatchRefusalError DispatchRefusal = "observation_error"
	// DispatchRefusalLowConfidence: the classification is below the dispatch
	// confidence floor (or above 1, which is invalid evidence).
	DispatchRefusalLowConfidence DispatchRefusal = "low_confidence"
	// DispatchRefusalNotIdle: the pane is classified as something other than
	// idle (working, waiting, error, unknown, ...).
	DispatchRefusalNotIdle DispatchRefusal = "not_idle"
)

// DispatchRefusal reports why SafeToDispatch rejects this observation, in
// clause order, or DispatchRefusalNone when it does not. SafeToDispatch is
// defined in terms of this method so the two cannot disagree.
func (p PaneObservation) DispatchRefusal() DispatchRefusal {
	switch {
	case p.Current.Freshness != FreshnessFresh:
		return DispatchRefusalStale
	case p.Current.Error != "":
		return DispatchRefusalError
	case !ObservationConfidenceIsActionable(p.Current.Confidence):
		return DispatchRefusalLowConfidence
	case p.Current.Status.State != StateIdle:
		return DispatchRefusalNotIdle
	default:
		return DispatchRefusalNone
	}
}

// SafeToDispatch fails closed. Only a fresh, confident idle classification can
// receive new work; stale, unknown, working, error, and failed observations are
// all rejected. DispatchRefusal names the clause that rejected an observation.
func (p PaneObservation) SafeToDispatch() bool {
	return p.DispatchRefusal() == DispatchRefusalNone
}

// ObservationFailure describes one failed observation stage without dropping
// successful pane results from the same session scan.
type ObservationFailure struct {
	PaneID string `json:"pane_id,omitempty"`
	Stage  string `json:"stage"`
	Error  string `json:"error"`
}

// SessionObservation is a deterministic, point-in-time view of a tmux session.
// Complete is false when any pane capture failed, while Panes still contains an
// explicit unavailable Current estimate for every pane found in the topology.
type SessionObservation struct {
	Session    string               `json:"session"`
	ObservedAt time.Time            `json:"observed_at"`
	Complete   bool                 `json:"complete"`
	Panes      []PaneObservation    `json:"panes"`
	Failures   []ObservationFailure `json:"failures"`
}

// PaneByID returns the unique pane with the requested tmux ID.
func (o SessionObservation) PaneByID(paneID string) (PaneObservation, bool) {
	if paneID == "" {
		return PaneObservation{}, false
	}
	var match PaneObservation
	found := false
	for _, pane := range o.Panes {
		if pane.Pane.ID != paneID {
			continue
		}
		if found {
			return PaneObservation{}, false
		}
		match = pane
		found = true
	}
	return match, found
}

// SafeToDispatch fails closed for missing and duplicate pane IDs.
func (o SessionObservation) SafeToDispatch(paneID string) bool {
	pane, ok := o.PaneByID(paneID)
	return ok && pane.SafeToDispatch()
}

// IsHealthy returns true if the agent is in a healthy state (idle or working)
func (s *AgentStatus) IsHealthy() bool {
	return s.State == StateIdle || s.State == StateWorking
}

// IdleDuration returns how long the agent has been idle since LastActive
func (s *AgentStatus) IdleDuration() time.Duration {
	return time.Since(s.LastActive)
}

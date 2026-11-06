package workflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	manualGateVersion       = 1
	manualDecisionMaxBytes  = 64 << 10
	manualReasonMaxBytes    = 4096
	workflowStateMaxBytes   = 64 << 20
	manualCheckpointTimeout = 10 * time.Second
)

// ManualTransitionChoice identifies one exact edge in Flow.Transitions. Labels
// may be empty or repeated; the index is the operator's unambiguous selector.
type ManualTransitionChoice struct {
	Index int    `json:"index"`
	Label string `json:"label,omitempty"`
	To    string `json:"to"`
}

// ManualGate authorizes decisions only for one completely dispatched stage
// visit. Its token survives observation updates but never a retry or rebind.
type ManualGate struct {
	Version     int                      `json:"version"`
	Token       string                   `json:"token"`
	VisitHash   string                   `json:"visit_hash"`
	Stage       string                   `json:"stage"`
	Transitions []ManualTransitionChoice `json:"transitions"`
}

// ManualDecision is an immutable operator request, not proof that a running
// coordinator has already advanced. The next-stage checkpoint records the
// same decision in history when the runner applies it.
type ManualDecision struct {
	Version         int       `json:"version"`
	Session         string    `json:"session"`
	Token           string    `json:"token"`
	VisitHash       string    `json:"visit_hash"`
	Stage           string    `json:"stage"`
	TransitionIndex int       `json:"transition"`
	To              string    `json:"to"`
	Reason          string    `json:"reason"`
	RequestedAt     time.Time `json:"requested_at"`
	// Replayed is set only on the API's copied idempotent response. It is
	// always false in the canonical receipt and in checkpoint history.
	Replayed bool `json:"replayed,omitempty"`
}

// ManualGateStatus exposes choices and their recorded decision without the
// task variables, pane captures, or other private material bound by VisitHash.
type ManualGateStatus struct {
	Token       string                   `json:"token"`
	Stage       string                   `json:"stage"`
	Transitions []ManualTransitionChoice `json:"transitions"`
	Decision    *ManualDecision          `json:"decision,omitempty"`
}

// Fingerprint binds the full workflow definition while allowing the same
// template to be addressed by its name or its file path.
func (t *WorkflowTemplate) Fingerprint() (string, error) {
	if t == nil {
		return "", errors.New("workflow template is required")
	}
	definition := *t
	definition.Source = ""
	data, err := json.Marshal(definition)
	if err != nil {
		return "", fmt.Errorf("fingerprint workflow template: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// RefreshManualGate runs before the runner saves a checkpoint. Pending or
// uncertain deliveries expose no manual choice. Observations do not rotate a
// gate, but any change to the stage's execution identity creates a new token.
func (s *WorkflowState) RefreshManualGate(template *WorkflowTemplate) error {
	if s == nil {
		return errors.New("workflow checkpoint is required")
	}
	var choices []ManualTransitionChoice
	if template != nil && template.Flow != nil {
		for index, transition := range template.Flow.Transitions {
			if transition.From == s.CurrentStage && transition.Trigger.Type == TriggerManual {
				choices = append(choices, ManualTransitionChoice{Index: index, Label: transition.Trigger.Label, To: transition.To})
			}
		}
	}
	if len(choices) == 0 || s.Completed {
		s.ManualGate = nil
		return nil
	}
	ready, err := manualStageReady(s)
	if err != nil {
		return err
	}
	if !ready {
		s.ManualGate = nil
		return nil
	}
	templateHash, err := template.Fingerprint()
	if err != nil {
		return err
	}
	if s.TemplateHash != templateHash || s.WorkflowName != template.Name {
		return errors.New("workflow manual gate template differs from its execution checkpoint")
	}
	visitHash, err := manualVisitHash(s, choices)
	if err != nil {
		return err
	}
	if gate := s.ManualGate; gate != nil && gate.Version == manualGateVersion &&
		gate.Stage == s.CurrentStage && gate.VisitHash == visitHash && validManualDigest(gate.Token) {
		if err := validateManualGate(s); err == nil {
			return nil
		}
	}
	var token [sha256.Size]byte
	if _, err := rand.Read(token[:]); err != nil {
		return fmt.Errorf("create workflow manual gate: %w", err)
	}
	s.ManualGate = &ManualGate{
		Version: manualGateVersion, Token: hex.EncodeToString(token[:]), VisitHash: visitHash,
		Stage: s.CurrentStage, Transitions: choices,
	}
	return nil
}

func manualStageReady(state *WorkflowState) (bool, error) {
	if state == nil || state.Completed || len(state.Dispatches) == 0 {
		return false, nil
	}
	for _, delivery := range state.Dispatches {
		if delivery.Status == "pending" || delivery.Status == "sending" {
			return false, nil
		}
		if delivery.Status != "delivered" {
			return false, errors.New("workflow manual gate has an invalid delivery status")
		}
	}
	if state.ResumeVersion != 1 || state.WorkflowName == "" || state.SessionName == "" ||
		state.ProjectRoot == "" || state.CurrentStage == "" || state.StageStartedAt.IsZero() ||
		state.Turn <= 0 || !validManualDigest(state.TemplateHash) || len(state.Agents) == 0 {
		return false, errors.New("workflow manual gate lacks a complete execution identity")
	}
	evidence := state.Evidence
	if evidence == nil || evidence.Version != 1 || evidence.Stage != state.CurrentStage ||
		!evidence.StartedAt.Equal(state.StageStartedAt) || evidence.Round != state.Turn {
		return false, errors.New("workflow manual gate lacks the current stage evidence boundary")
	}
	for pane, role := range state.Agents {
		pid, ok := state.PanePIDs[pane]
		boundary, observed := evidence.Panes[pane]
		if pane == "" || role == "" || !ok || pid < 0 || !observed || boundary.PID != pid {
			return false, errors.New("workflow manual gate has inconsistent pane identities")
		}
	}
	seen := make(map[string]bool, len(state.Dispatches))
	for _, delivery := range state.Dispatches {
		if seen[delivery.Pane] || delivery.Role == "" || state.Agents[delivery.Pane] != delivery.Role ||
			delivery.Turn <= 0 || delivery.Turn > state.Turn {
			return false, errors.New("workflow manual gate has inconsistent dispatch identities")
		}
		seen[delivery.Pane] = true
	}
	return true, nil
}

func manualVisitHash(state *WorkflowState, choices []ManualTransitionChoice) (string, error) {
	identity := struct {
		Workflow, Session, Template, Project, Stage string
		StartedAt                                   time.Time
		Agents, Variables                           map[string]string
		PanePIDs                                    map[string]int
		Turn, EvidenceRound                         int
		Dispatches                                  []StageDispatch
		Transitions                                 []ManualTransitionChoice
	}{
		Workflow: state.WorkflowName, Session: state.SessionName, Template: state.TemplateHash,
		Project: state.ProjectRoot, Stage: state.CurrentStage, StartedAt: state.StageStartedAt,
		Agents: state.Agents, Variables: state.Variables, PanePIDs: state.PanePIDs,
		Turn: state.Turn, EvidenceRound: state.Evidence.Round, Dispatches: state.Dispatches, Transitions: choices,
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("fingerprint workflow manual stage: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func validManualDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func validateManualGate(state *WorkflowState) error {
	ready, err := manualStageReady(state)
	if err != nil {
		return err
	}
	if !ready || state.ManualGate == nil {
		return errors.New("workflow has no current manual gate; inspect workflow status")
	}
	gate := state.ManualGate
	if gate.Version != manualGateVersion || gate.Stage != state.CurrentStage ||
		!validManualDigest(gate.Token) || !validManualDigest(gate.VisitHash) || len(gate.Transitions) == 0 {
		return errors.New("workflow manual gate has an invalid identity")
	}
	previous := -1
	for _, choice := range gate.Transitions {
		if choice.Index <= previous || choice.To == "" {
			return errors.New("workflow manual gate has invalid transition choices")
		}
		previous = choice.Index
	}
	visitHash, err := manualVisitHash(state, gate.Transitions)
	if err != nil {
		return err
	}
	if visitHash != gate.VisitHash {
		return errors.New("workflow manual stage changed; inspect workflow status for its current gate")
	}
	return nil
}

// lockManualCheckpoint is independent of the runner's lifetime lease. State
// saves share this short lock so a request cannot race its validation against
// the atomic publication of a different stage visit.
func (s *StateStore) lockManualCheckpoint(ctx context.Context, session string) (func(), error) {
	if ctx == nil {
		return nil, errors.New("workflow manual request context is required")
	}
	path, err := s.path(session)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("create workflow state directory: %w", err)
	}
	unlock, err := lockPaneSequenceFile(ctx, path+".manual.lock")
	if err != nil {
		return nil, fmt.Errorf("lock workflow manual checkpoint: %w", err)
	}
	return unlock, nil
}

func (s *StateStore) manualDecisionPath(session, token string) (string, error) {
	if _, err := s.path(session); err != nil {
		return "", err
	}
	if !validManualDigest(token) {
		return "", errors.New("manual transition requires the exact token from workflow status")
	}
	return filepath.Join(s.Dir, "manual-decisions", session, token+".json"), nil
}

// RequestManualTransition records a decision without taking the live runner's
// lease or sending a prompt. Exact retries replay the immutable receipt even
// after advancement; a different decision can never replace it.
func (s *StateStore) RequestManualTransition(ctx context.Context, session, token string, transitionIndex int, reason string) (*ManualDecision, error) {
	reason = strings.TrimSpace(reason)
	if transitionIndex < 0 || reason == "" || len(reason) > manualReasonMaxBytes || !utf8.ValidString(reason) {
		return nil, fmt.Errorf("manual transition requires a non-negative transition index and a reason of 1-%d UTF-8 bytes", manualReasonMaxBytes)
	}
	path, err := s.manualDecisionPath(session, token)
	if err != nil {
		return nil, err
	}
	unlock, err := s.lockManualCheckpoint(ctx, session)
	if err != nil {
		return nil, err
	}
	defer unlock()
	prior, err := readManualDecisionFile(path)
	if err != nil {
		return nil, err
	}
	state, err := s.Load(session)
	if err != nil {
		return nil, err
	}
	if prior != nil {
		if prior.Session != session || prior.Token != token {
			return nil, errors.New("workflow manual decision file has a different identity")
		}
		if prior.TransitionIndex != transitionIndex || prior.Reason != reason {
			return nil, errors.New("workflow manual gate already has a different immutable decision")
		}
		if state != nil && state.ManualGate != nil && state.ManualGate.Token == token {
			if err := validateManualGate(state); err != nil {
				return nil, err
			}
			if err := validateDecisionForManualGate(state, prior); err != nil {
				return nil, err
			}
		}
		prior.Replayed = true
		return prior, nil
	}
	if err := validateManualGate(state); err != nil {
		return nil, err
	}
	if state.ManualGate.Token != token {
		return nil, errors.New("workflow manual gate token is stale; inspect workflow status")
	}
	var selected *ManualTransitionChoice
	for i := range state.ManualGate.Transitions {
		choice := &state.ManualGate.Transitions[i]
		if choice.Index == transitionIndex {
			selected = choice
			break
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("transition %d is not a manual choice for the current stage", transitionIndex)
	}
	decision := &ManualDecision{
		Version: manualGateVersion, Session: session, Token: token, VisitHash: state.ManualGate.VisitHash,
		Stage: state.CurrentStage, TransitionIndex: transitionIndex, To: selected.To,
		Reason: reason, RequestedAt: time.Now().UTC(),
	}
	data, err := json.MarshalIndent(decision, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode workflow manual decision: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create workflow manual decision directory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := util.AtomicWriteFile(path, data, 0o600); err != nil {
		return nil, fmt.Errorf("persist workflow manual decision: %w", err)
	}
	return decision, nil
}

// ReadManualDecision looks only at the current visit. Old receipt files remain
// available for audit and idempotent command retries but can never advance a
// later stage, even when that stage has the same name.
func (s *StateStore) ReadManualDecision(state *WorkflowState) (*ManualDecision, error) {
	if state == nil || state.ManualGate == nil || state.Completed {
		return nil, nil
	}
	if err := validateManualGate(state); err != nil {
		return nil, err
	}
	path, err := s.manualDecisionPath(state.SessionName, state.ManualGate.Token)
	if err != nil {
		return nil, err
	}
	decision, err := readManualDecisionFile(path)
	if err != nil || decision == nil {
		return decision, err
	}
	if err := validateDecisionForManualGate(state, decision); err != nil {
		return nil, err
	}
	return decision, nil
}

func validateDecisionForManualGate(state *WorkflowState, decision *ManualDecision) error {
	if decision.Session != state.SessionName || decision.Token != state.ManualGate.Token ||
		decision.VisitHash != state.ManualGate.VisitHash || decision.Stage != state.CurrentStage {
		return errors.New("workflow manual decision does not match the current stage visit")
	}
	for _, choice := range state.ManualGate.Transitions {
		if choice.Index == decision.TransitionIndex && choice.To == decision.To {
			return nil
		}
	}
	return errors.New("workflow manual decision is not a current manual transition")
}

func (s *StateStore) ManualStatus(state *WorkflowState) (*ManualGateStatus, error) {
	if state == nil || state.ManualGate == nil || state.Completed {
		return nil, nil
	}
	decision, err := s.ReadManualDecision(state)
	if err != nil {
		return nil, err
	}
	return &ManualGateStatus{
		Token: state.ManualGate.Token, Stage: state.CurrentStage,
		Transitions: append([]ManualTransitionChoice(nil), state.ManualGate.Transitions...), Decision: decision,
	}, nil
}

func readManualDecisionFile(path string) (*ManualDecision, error) {
	data, err := readWorkflowRegularFile(path, manualDecisionMaxBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read workflow manual decision: %w", err)
	}
	var decision ManualDecision
	if err := json.Unmarshal(data, &decision); err != nil {
		return nil, fmt.Errorf("decode workflow manual decision: %w", err)
	}
	if decision.Version != manualGateVersion || decision.Session == "" || decision.Stage == "" ||
		!validManualDigest(decision.Token) || !validManualDigest(decision.VisitHash) || decision.TransitionIndex < 0 ||
		decision.To == "" || decision.Reason == "" || decision.Reason != strings.TrimSpace(decision.Reason) ||
		len(decision.Reason) > manualReasonMaxBytes || decision.RequestedAt.IsZero() || decision.Replayed {
		return nil, errors.New("workflow manual decision has an invalid receipt")
	}
	return &decision, nil
}

func readWorkflowRegularFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("workflow checkpoint must be a regular file no larger than %d bytes", limit)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errors.New("workflow checkpoint changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("workflow checkpoint exceeds %d bytes", limit)
	}
	return data, nil
}

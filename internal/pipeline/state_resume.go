package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ResumeMode controls how persisted state is interpreted when execution is
// resumed after interruption.
type ResumeMode string

const (
	ResumeModeContinue      ResumeMode = "continue"
	ResumeModeRestartFailed ResumeMode = "restart-failed"
	ResumeModeForceIter     ResumeMode = "force-iter"
)

// ResumeRosterChangePolicy decides whether a resume may target a different
// tmux session than the one recorded in the prior state.
type ResumeRosterChangePolicy string

const (
	ResumeRosterAbort   ResumeRosterChangePolicy = "abort"
	ResumeRosterProceed ResumeRosterChangePolicy = "proceed"
)

// ResumeOptions configures Executor.Resume. By default completed step
// outputs are preserved; set Reset=true to clear prior step outputs and
// re-run the workflow from the beginning while retaining non-step input
// variables. KeepState is the historical alias and remains honored, but new
// callers should rely on the default-keep semantics. After
// normalizeResumeOptions, KeepState reflects the effective value.
type ResumeOptions struct {
	Mode ResumeMode `json:"mode,omitempty"`
	// KeepState preserves completed step outputs across resume. When the
	// caller passes a non-zero options struct without setting KeepState=true,
	// normalizeResumeOptions still defaults to keep-state unless Reset=true
	// is set explicitly (bd-uyjdn). The field stays exported so existing
	// JSON-serialized state continues to round-trip and CLI callers that
	// already wire --keep-state continue to work unchanged.
	KeepState bool `json:"keep_state,omitempty"`
	// Reset, when true, opts the caller into the legacy KeepState=false
	// behavior: completed Steps, durable foreach/parallel state, and step
	// variables are cleared before resume.
	Reset          bool                     `json:"reset,omitempty"`
	MaxResumeAge   time.Duration            `json:"max_resume_age,omitempty"`
	OnRosterChange ResumeRosterChangePolicy `json:"on_roster_change,omitempty"`
	StepID         string                   `json:"step_id,omitempty"`
	Iteration      int                      `json:"iteration,omitempty"`
}

// ForeachIterationState records durable progress for loop/foreach-style
// iteration steps. CompletedIterationIDs use the stable "<step>_iter<N>" form.
//
// ItemsFingerprint (bd-3awat) records a content hash of the resolved items
// list at the time the foreach started. CompletedIterationIDs are keyed by
// integer index, so resuming against a list whose elements have shifted
// (resolved from a dynamic source between runs — vars expression, prior
// step output, glob, etc.) would silently apply old completion records to
// new items. Recording the fingerprint lets executeForEach refuse resume
// when the resolved items diverge from the original run.
//
// CollectedOutputs (bd-t3q8a) preserves the loop.Collect outputs for
// already-completed iterations so a mid-loop resume can reconstruct them
// before continuing. Without this the resumed loop would store loop.Collect
// using only the iterations that ran on the resume, silently dropping the
// outputs from iterations completed in the prior run.
//
// CompletedRounds (bd-r2pan) records the highest fully-completed round per
// iteration ID for foreach steps with max_rounds > 1. bd-qeatk's iteration-
// level CompletedIterationIDs only tracks iterations that finished every
// round; an iteration interrupted mid-rounds is NOT in that set, so on
// resume the iteration would re-dispatch from round 1, duplicating the
// side effects of any rounds that had completed before the interruption.
// CompletedRounds maps "<step>_iter<N>" → highest completed round so the
// resume loop can skip rounds whose watermark already advanced. Empty/
// missing entry means "no rounds completed", so legacy state files
// upgrade cleanly (their resumes re-run all rounds, matching pre-bd-r2pan
// behavior). Single-round iterations (max_rounds unset or 1) never write
// here.
type ForeachIterationState struct {
	StepID                string         `json:"step_id"`
	CurrentIteration      int            `json:"current_iteration"`
	Total                 int            `json:"total"`
	CompletedIterationIDs []string       `json:"completed_iteration_ids,omitempty"`
	ItemsFingerprint      string         `json:"items_fingerprint,omitempty"`
	CollectedOutputs      []interface{}  `json:"collected_outputs,omitempty"`
	CompletedRounds       map[string]int `json:"completed_rounds,omitempty"`
	StartedAt             time.Time      `json:"started_at,omitempty"`
	UpdatedAt             time.Time      `json:"updated_at,omitempty"`
}

// ParallelGroupState records the persisted progress of a parallel step group.
type ParallelGroupState struct {
	StepID             string    `json:"step_id"`
	Total              int       `json:"total"`
	CompletedStepIDs   []string  `json:"completed_step_ids,omitempty"`
	FailedStepIDs      []string  `json:"failed_step_ids,omitempty"`
	InFlightStepIDs    []string  `json:"in_flight_step_ids,omitempty"`
	StartedAt          time.Time `json:"started_at,omitempty"`
	UpdatedAt          time.Time `json:"updated_at,omitempty"`
	CompletedAt        time.Time `json:"completed_at,omitempty"`
	AllSubstepsSettled bool      `json:"all_substeps_settled,omitempty"`

	// Immutable versioned JSON binds each settled child's definition, result
	// and owned collection aliases. Persisted atomically with Steps; scoped
	// reset removes this map together with the enclosing parallel group.
	ChildCheckpoints map[string]string `json:"child_checkpoints,omitempty"`
}

// ScopeFrame is the serializable view of active loop/branch variable scopes.
type ScopeFrame struct {
	Kind      string                 `json:"kind"`
	Name      string                 `json:"name,omitempty"`
	Variables map[string]interface{} `json:"variables,omitempty"`
}

// InFlightStepState marks work that started but had not produced a finished
// StepResult at the last checkpoint.
type InFlightStepState struct {
	StepID    string    `json:"step_id"`
	Kind      string    `json:"kind,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	Iteration int       `json:"iteration,omitempty"`
	Output    string    `json:"output,omitempty"`
}

const (
	commandExecutionVersion  = 1
	commandExecutionStarting = "starting"
	commandExecutionRunning  = "running"
	commandExecutionSettled  = "settled"
)

// CommandRecoveryMaxOutputBytes bounds the explicitly supplied final output of
// an interrupted command. Recovery also observes the frozen workflow's limit.
const CommandRecoveryMaxOutputBytes = 16 << 20

// CommandExecutionResolutionAccept records the operator's attestation that the
// original foreground command group ended with the supplied outcome. It never
// authorizes another launch or treats a recorded numeric PID as a live handle.
const CommandExecutionResolutionAccept = "accept"

// CommandExecutionState keeps a launch intent until the entire command group
// has settled and its result is durable. InFlightSteps is scheduler bookkeeping:
// another worker may checkpoint after it is cleared but before the StepResult
// is published. It cannot prove that replaying an external command is safe.
// ProcessID is diagnostic evidence only; a resumed process must never signal a
// numeric PID from an earlier lifetime. A crash can leave that group running.
type CommandExecutionState struct {
	Version    int        `json:"version"`
	StepID     string     `json:"step_id"`
	StepHash   string     `json:"step_hash"`
	Status     string     `json:"status"`
	ProcessID  int        `json:"process_id,omitempty"`
	Background bool       `json:"background,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	SettledAt  time.Time  `json:"settled_at,omitempty"`
	Result     StepResult `json:"result,omitempty"`
	// An accepted Result retains the exact supplied output, before parsing.
	// Ordinary resume interprets it only after checking the materialized step
	// definition. The scheduler's Steps and output aliases remain untouched here.
	ResolutionAction     string    `json:"resolution_action,omitempty"`
	ResolutionToken      string    `json:"resolution_token,omitempty"`
	ResolutionReason     string    `json:"resolution_reason,omitempty"`
	ResolutionExitCode   int       `json:"resolution_exit_code,omitempty"`
	ResolutionOutputHash string    `json:"resolution_output_hash,omitempty"`
	ResolvedAt           time.Time `json:"resolved_at,omitempty"`
}

// CommandExecutionRecoveryEntry exposes identity and decision metadata without
// command text, environment variables, stdin, or supplied command output.
type CommandExecutionRecoveryEntry struct {
	StepID           string    `json:"step_id"`
	Status           string    `json:"status"`
	ReceiptToken     string    `json:"receipt_token"`
	ProcessID        int       `json:"process_id,omitempty"`
	Background       bool      `json:"background"`
	StartedAt        time.Time `json:"started_at"`
	CanAccept        bool      `json:"can_accept"`
	RefusalReason    string    `json:"refusal_reason,omitempty"`
	ResolutionAction string    `json:"resolution_action,omitempty"`
	ResolutionReason string    `json:"resolution_reason,omitempty"`
	ResolvedAt       time.Time `json:"resolved_at,omitempty"`
	ExitCode         *int      `json:"exit_code,omitempty"`
	OutputBytes      int       `json:"output_bytes,omitempty"`
	OutputHash       string    `json:"output_hash,omitempty"`
}

type CommandExecutionRecoveryInspection struct {
	RunID      string                          `json:"run_id"`
	WorkflowID string                          `json:"workflow_id"`
	Session    string                          `json:"session"`
	Commands   []CommandExecutionRecoveryEntry `json:"commands"`
}

type CommandExecutionRecoveryResult struct {
	RunID      string                        `json:"run_id"`
	WorkflowID string                        `json:"workflow_id"`
	Session    string                        `json:"session"`
	Command    CommandExecutionRecoveryEntry `json:"command"`
	Replayed   bool                          `json:"replayed"`
}

func commandDefinitionHash(step *Step) (string, error) {
	encoded, err := json.Marshal(step)
	if err != nil {
		return "", fmt.Errorf("hash command step: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func cloneCommandExecution(record CommandExecutionState) CommandExecutionState {
	record.Result.ParsedData = cloneInterfaceValue(record.Result.ParsedData)
	if record.Result.Error != nil {
		copy := cloneStepErrorValue(*record.Result.Error)
		record.Result.Error = &copy
	}
	return record
}

func (e *Executor) loadCommandExecution(stepID string) (CommandExecutionState, bool) {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	if e.state == nil {
		return CommandExecutionState{}, false
	}
	record, ok := e.state.CommandExecutions[stepID]
	return cloneCommandExecution(record), ok
}

func (e *Executor) saveCommandExecution(record CommandExecutionState) error {
	if err := validateCommandExecution(record.StepID, record); err != nil {
		return err
	}
	e.stateMu.Lock()
	if e.state == nil {
		e.stateMu.Unlock()
		return fmt.Errorf("cannot save command execution without execution state")
	}
	if e.state.CommandExecutions == nil {
		e.state.CommandExecutions = make(map[string]CommandExecutionState)
	}
	e.state.CommandExecutions[record.StepID] = cloneCommandExecution(record)
	e.stateMu.Unlock()
	return e.persistState()
}

func validateCommandExecution(key string, record CommandExecutionState) error {
	invalid := func(reason string) error {
		return fmt.Errorf("invalid command execution for step %q: %s", key, reason)
	}
	if strings.TrimSpace(key) == "" || record.StepID != key || record.Version != commandExecutionVersion {
		return invalid("unsupported version or mismatched step identity")
	}
	hash, err := hex.DecodeString(record.StepHash)
	if err != nil || len(hash) != sha256.Size || record.StartedAt.IsZero() || record.ProcessID < 0 {
		return invalid("missing definition hash, invalid process ID or start time")
	}
	switch record.Status {
	case commandExecutionStarting, commandExecutionRunning:
		if !record.SettledAt.IsZero() || record.Result.Status != "" {
			return invalid("unfinished launch contains a settled outcome")
		}
		if record.Status == commandExecutionRunning && record.ProcessID == 0 {
			return invalid("running command is missing its process ID")
		}
	case commandExecutionSettled:
		if record.SettledAt.IsZero() || record.SettledAt.Before(record.StartedAt) || record.Result.StepID != key || record.Result.FinishedAt.IsZero() {
			return invalid("missing or inconsistent settlement evidence")
		}
		switch record.Result.Status {
		case StatusCompleted, StatusFailed, StatusCancelled:
		default:
			return invalid("invalid settled result status")
		}
	default:
		return invalid(fmt.Sprintf("unknown status %q; refusing to discard launch evidence", record.Status))
	}
	if record.ResolutionAction == "" {
		if record.ResolutionToken != "" || record.ResolutionReason != "" || record.ResolutionExitCode != 0 ||
			record.ResolutionOutputHash != "" || !record.ResolvedAt.IsZero() {
			return invalid("incomplete operator command resolution")
		}
		return nil
	}
	if record.ResolutionAction != CommandExecutionResolutionAccept || record.Status != commandExecutionSettled || record.Background ||
		record.ResolutionReason == "" || record.ResolutionReason != strings.TrimSpace(record.ResolutionReason) ||
		len(record.ResolutionReason) > agentDeliveryMaxReasonBytes || !utf8.ValidString(record.ResolutionReason) ||
		record.ResolutionExitCode < 0 || record.ResolutionExitCode > 255 ||
		record.ResolvedAt.IsZero() || !record.ResolvedAt.Equal(record.SettledAt) ||
		!record.Result.StartedAt.Equal(record.StartedAt) || !record.Result.FinishedAt.Equal(record.ResolvedAt) ||
		record.Result.AgentType != "command" || record.Result.ParsedData != nil || record.Result.Error != nil ||
		record.Result.RerunOnResume || record.Result.Attempts != 0 || record.Result.PaneUsed != "" ||
		record.Result.SkipReason != "" || record.Result.SkipKind != "" ||
		len(record.Result.Output) > CommandRecoveryMaxOutputBytes || !utf8.ValidString(record.Result.Output) {
		return invalid("inconsistent operator command outcome")
	}
	expectedStatus := StatusCompleted
	if record.ResolutionExitCode != 0 {
		expectedStatus = StatusFailed
	}
	if record.Result.Status != expectedStatus {
		return invalid("operator exit code disagrees with its recorded outcome")
	}
	for _, value := range []string{record.ResolutionToken, record.ResolutionOutputHash} {
		digest, err := hex.DecodeString(value)
		if err != nil || len(digest) != sha256.Size || value != strings.ToLower(value) {
			return invalid("invalid operator command fingerprint")
		}
	}
	outputHash := sha256.Sum256([]byte(record.Result.Output))
	if hex.EncodeToString(outputHash[:]) != record.ResolutionOutputHash {
		return invalid("operator command output no longer matches its recorded fingerprint")
	}
	return nil
}

func uncertainCommandExecutionError(stepID string, processID int) error {
	return fmt.Errorf("command step %q has an unresolved launch (recorded process ID %d); its process group may still be running or may already have performed side effects. Ordinary resume will not execute it again. Inspect the command and its effects; after verifying the foreground command group ended, use pipeline recover --accept-command with its exact receipt, final exit code, complete output, and reason. Deliberately use --mode=restart-failed (or --keep-state=false for a whole-workflow replay) only if repeating it is intended", stepID, processID)
}

func commandExecutionReceiptToken(state *ExecutionState, record CommandExecutionState) (string, error) {
	encoded, err := json.Marshal(struct {
		Version          int
		RunID            string
		WorkflowID       string
		WorkflowSnapshot string
		Session          string
		StepID           string
		StepHash         string
		ProcessID        int
		Background       bool
		StartedAt        time.Time
	}{record.Version, state.RunID, state.WorkflowID, filepath.Base(state.WorkflowFile), state.Session,
		record.StepID, record.StepHash, record.ProcessID, record.Background, record.StartedAt.UTC()})
	if err != nil {
		return "", fmt.Errorf("fingerprint command execution: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func commandExecutionRecoveryEntry(state *ExecutionState, record CommandExecutionState) (CommandExecutionRecoveryEntry, error) {
	if err := validateCommandExecution(record.StepID, record); err != nil {
		return CommandExecutionRecoveryEntry{}, err
	}
	token, err := commandExecutionReceiptToken(state, record)
	if err != nil {
		return CommandExecutionRecoveryEntry{}, err
	}
	if record.ResolutionAction != "" && record.ResolutionToken != token {
		return CommandExecutionRecoveryEntry{}, fmt.Errorf("operator resolution for command %q no longer matches its original receipt identity", record.StepID)
	}
	entry := CommandExecutionRecoveryEntry{
		StepID: record.StepID, Status: record.Status, ReceiptToken: token, ProcessID: record.ProcessID,
		Background: record.Background, StartedAt: record.StartedAt,
		CanAccept:        record.Status != commandExecutionSettled && !record.Background,
		ResolutionAction: record.ResolutionAction, ResolutionReason: record.ResolutionReason, ResolvedAt: record.ResolvedAt,
	}
	if record.ResolutionAction != "" {
		exitCode := record.ResolutionExitCode
		entry.ExitCode, entry.OutputBytes, entry.OutputHash = &exitCode, len(record.Result.Output), record.ResolutionOutputHash
	}
	return entry, nil
}

// InspectCommandExecutionRecovery reads one atomic checkpoint without looking
// up or signalling saved process IDs. Acceptance always reloads under ownership.
func InspectCommandExecutionRecovery(ctx context.Context, projectDir, runID string) (*CommandExecutionRecoveryInspection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(projectDir) == "" {
		return nil, fmt.Errorf("project directory is required for pipeline recovery")
	}
	projectDir = normalizeLockRoot(projectDir)
	state, err := LoadState(projectDir, runID)
	if err != nil {
		return nil, err
	}
	result := &CommandExecutionRecoveryInspection{
		RunID: state.RunID, WorkflowID: state.WorkflowID, Session: state.Session,
		Commands: make([]CommandExecutionRecoveryEntry, 0),
	}
	workflow, workflowErr := loadRecoveryWorkflow(projectDir, state)
	checker := NewExecutor(DefaultExecutorConfig(state.Session))
	ids := make([]string, 0, len(state.CommandExecutions))
	for id := range state.CommandExecutions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		record := state.CommandExecutions[id]
		if err := validateCommandExecution(id, record); err != nil {
			return nil, err
		}
		entry, err := commandExecutionRecoveryEntry(state, record)
		if err != nil {
			return nil, err
		}
		if record.Status == commandExecutionSettled && record.ResolutionAction == "" {
			continue
		}
		if record.ResolutionAction == "" {
			if workflowErr != nil {
				entry.CanAccept, entry.RefusalReason = false, "a valid frozen workflow is required before accepting command output"
			} else if err := checker.validateCommandRecovery(workflow, state, record); err != nil {
				entry.CanAccept, entry.RefusalReason = false, err.Error()
			}
		}
		result.Commands = append(result.Commands, entry)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (e *Executor) validateCommandRecovery(workflow *Workflow, state *ExecutionState, record CommandExecutionState) error {
	if record.Background {
		return fmt.Errorf("command %q was launched with wait:none; its already-published background outcome cannot be replaced", record.StepID)
	}
	if record.Status == commandExecutionSettled {
		return fmt.Errorf("command %q already has a settled outcome", record.StepID)
	}
	if _, cleanup := resolveOnCancelRuntimeStep(workflow, record.StepID); cleanup {
		return fmt.Errorf("command %q belongs to cancellation cleanup; ordinary resume may run that phase again", record.StepID)
	}
	for i := range workflow.PostPipelineSteps {
		if _, tail := resolveHookRuntimeStep(&workflow.PostPipelineSteps[i], "post_pipeline", i+1, record.StepID); tail {
			return fmt.Errorf("command %q belongs to post-pipeline finalization; ordinary resume may reopen that phase", record.StepID)
		}
	}
	switch state.Status {
	case StatusPending, StatusRunning, StatusFailed, StatusCancelled:
	default:
		return fmt.Errorf("pipeline %q has status %q and cannot accept an unfinished command", state.RunID, state.Status)
	}
	if prior := state.Steps[record.StepID]; prior.Status == StatusCompleted || prior.Status == StatusSkipped {
		return fmt.Errorf("command %q already published a terminal step result; refusing to replace consumed output", record.StepID)
	}
	if err := validateRecoveryWorkflowStep(workflow, state, record.StepID, StepKindCommand, record.StepHash, commandDefinitionHash); err != nil {
		return err
	}
	if err := e.validateCommandReplayScopeLocked(workflow, state, record.StepID); err != nil {
		return fmt.Errorf("cannot accept output for a command that ordinary resume will not revisit: %w", err)
	}
	return nil
}

// AcceptCommandExecutionContext records an operator-observed final outcome for
// an unresolved foreground command. The caller must have verified that the
// entire original process group ended; a PID lookup cannot establish that.
// It changes only the command journal. Resume validates the materialized step,
// applies its normal output parser/error policy, and publishes dependent data.
func AcceptCommandExecutionContext(ctx context.Context, projectDir, runID, stepID, receiptToken, reason string, exitCode int, output string) (*CommandExecutionRecoveryResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reason = strings.TrimSpace(reason)
	if strings.TrimSpace(projectDir) == "" || strings.TrimSpace(stepID) == "" || stepID != strings.TrimSpace(stepID) {
		return nil, fmt.Errorf("command recovery requires a project directory and exact step ID")
	}
	if reason == "" || len(reason) > agentDeliveryMaxReasonBytes || !utf8.ValidString(reason) {
		return nil, fmt.Errorf("command recovery reason must contain 1 to %d UTF-8 bytes", agentDeliveryMaxReasonBytes)
	}
	if exitCode < 0 || exitCode > 255 || len(output) > CommandRecoveryMaxOutputBytes || !utf8.ValidString(output) {
		return nil, fmt.Errorf("command recovery requires an exit code from 0 to 255 and at most %d bytes of complete UTF-8 output", CommandRecoveryMaxOutputBytes)
	}
	digest, err := hex.DecodeString(receiptToken)
	if err != nil || len(digest) != sha256.Size || receiptToken != strings.ToLower(receiptToken) {
		return nil, fmt.Errorf("command recovery requires the exact inspected receipt token")
	}
	projectDir = normalizeLockRoot(projectDir)
	control, err := AcquireRunControl(ctx, projectDir, runID)
	if err != nil {
		return nil, err
	}
	defer control.Close()
	ctx = control.Context()
	state, err := LoadState(projectDir, runID)
	if err != nil {
		return nil, err
	}
	for id, saved := range state.CommandExecutions {
		if err := validateCommandExecution(id, saved); err != nil {
			return nil, err
		}
		if _, err := commandExecutionRecoveryEntry(state, saved); err != nil {
			return nil, err
		}
	}
	record, exists := state.CommandExecutions[stepID]
	if !exists {
		return nil, fmt.Errorf("step %q has no saved command execution receipt", stepID)
	}
	entry, err := commandExecutionRecoveryEntry(state, record)
	if err != nil {
		return nil, err
	}
	if entry.ReceiptToken != receiptToken {
		return nil, fmt.Errorf("receipt token for command %q is stale or belongs to a different execution", stepID)
	}
	result := &CommandExecutionRecoveryResult{RunID: state.RunID, WorkflowID: state.WorkflowID, Session: state.Session, Command: entry}
	if record.ResolutionAction != "" {
		if record.ResolutionAction != CommandExecutionResolutionAccept || record.ResolutionReason != reason ||
			record.ResolutionExitCode != exitCode || record.Result.Output != output {
			return nil, fmt.Errorf("command %q already has an immutable operator decision with a different reason or outcome", stepID)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result.Replayed = true
		return result, nil
	}
	workflow, err := loadRecoveryWorkflow(projectDir, state)
	if err != nil {
		return nil, err
	}
	checker := NewExecutor(DefaultExecutorConfig(state.Session))
	if err := checker.validateCommandRecovery(workflow, state, record); err != nil {
		return nil, err
	}
	if limit := workflow.Settings.Limits.EffectiveLimits().MaxCommandStdoutBytes; int64(len(output)) > limit {
		return nil, fmt.Errorf("supplied command output exceeds the frozen workflow's %d-byte stdout limit", limit)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if now.Before(record.StartedAt) {
		return nil, fmt.Errorf("command start time is in the future; refusing an inconsistent outcome")
	}
	status := StatusCompleted
	if exitCode != 0 {
		status = StatusFailed
	}
	outputHash := sha256.Sum256([]byte(output))
	record.Status, record.SettledAt = commandExecutionSettled, now
	record.Result = StepResult{StepID: stepID, Status: status, StartedAt: record.StartedAt, FinishedAt: now, AgentType: "command", Output: output}
	record.ResolutionAction, record.ResolutionToken, record.ResolutionReason = CommandExecutionResolutionAccept, receiptToken, reason
	record.ResolutionExitCode, record.ResolutionOutputHash, record.ResolvedAt = exitCode, hex.EncodeToString(outputHash[:]), now
	entry, err = commandExecutionRecoveryEntry(state, record)
	if err != nil {
		return nil, err
	}
	state.CommandExecutions[stepID] = record
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := SaveState(projectDir, state); err != nil {
		return nil, fmt.Errorf("persist operator command acceptance: %w", err)
	}
	result.Command = entry
	slog.Info("pipeline command outcome accepted by operator", "run_id", state.RunID, "step_id", stepID, "exit_code", exitCode, "output_bytes", len(output))
	return result, nil
}

const (
	agentDeliveryVersion         = 1
	agentDeliveryNotSent         = "not_sent"
	agentDeliverySending         = "sending"
	agentDeliveryDelivered       = "delivered"
	agentDeliveryCompleted       = "completed"
	agentDeliveryMaxCaptureBytes = 1 << 20
	agentDeliveryMaxOutputBytes  = 1 << 20
	agentDeliveryMaxReasonBytes  = 4096
)

// AgentDeliveryResolutionAccept records an operator's attestation that an
// uncertain prompt was delivered. It never authorizes sending it again.
const AgentDeliveryResolutionAccept = "accept"

// AgentDeliveryState journals a single runtime leaf's external dispatch. The
// enclosing ExecutionState binds it to a run and workflow; StepID includes any
// iteration or parallel namespace. Only not_sent proves no prompt actuation.
// A sending record is deliberately ambiguous: recovery must not assume that
// absence of a delivered timestamp means no send.
type AgentDeliveryState struct {
	Version        int       `json:"version"`
	StepID         string    `json:"step_id"`
	Kind           string    `json:"kind"`
	Session        string    `json:"session"`
	Endpoint       string    `json:"endpoint"`
	PaneID         string    `json:"pane_id"`
	PanePID        int       `json:"pane_pid"`
	AgentType      string    `json:"agent_type"`
	StepHash       string    `json:"step_hash"`
	PromptHash     string    `json:"prompt_hash"`
	PromptEchoHash string    `json:"prompt_echo_hash"`
	Status         string    `json:"status"`
	BeforeOutput   string    `json:"before_output,omitempty"`
	Output         string    `json:"output,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	DeliveredAt    time.Time `json:"delivered_at,omitempty"`
	CompletedAt    time.Time `json:"completed_at,omitempty"`
	// Resolution fields remain values so snapshots cannot share mutable audit
	// data. The boundary hash keeps the accepted receipt identity stable after
	// ordinary resume advances or clears BeforeOutput.
	ResolutionAction       string    `json:"resolution_action,omitempty"`
	ResolutionToken        string    `json:"resolution_token,omitempty"`
	ResolutionReason       string    `json:"resolution_reason,omitempty"`
	ResolutionBoundaryHash string    `json:"resolution_boundary_hash,omitempty"`
	ResolvedAt             time.Time `json:"resolved_at,omitempty"`
}

// AgentDeliveryRecoveryEntry exposes receipt identity without prompt text or
// captured agent output. CanAccept describes the journal phase; acceptance also
// verifies the saved workflow and the original live pane's process lifetime.
type AgentDeliveryRecoveryEntry struct {
	StepID           string    `json:"step_id"`
	Kind             string    `json:"kind"`
	Status           string    `json:"status"`
	ReceiptToken     string    `json:"receipt_token"`
	Endpoint         string    `json:"endpoint"`
	PaneID           string    `json:"pane_id"`
	PanePID          int       `json:"pane_pid"`
	AgentType        string    `json:"agent_type"`
	StartedAt        time.Time `json:"started_at"`
	CanAccept        bool      `json:"can_accept"`
	ResolutionAction string    `json:"resolution_action,omitempty"`
	ResolutionReason string    `json:"resolution_reason,omitempty"`
	ResolvedAt       time.Time `json:"resolved_at,omitempty"`
}

// AgentDeliveryRecoveryInspection includes unfinished deliveries and retained
// operator decisions, including decisions whose original delivery completed.
type AgentDeliveryRecoveryInspection struct {
	RunID      string                       `json:"run_id"`
	WorkflowID string                       `json:"workflow_id"`
	Session    string                       `json:"session"`
	Deliveries []AgentDeliveryRecoveryEntry `json:"deliveries"`
}

type AgentDeliveryRecoveryResult struct {
	RunID      string                     `json:"run_id"`
	WorkflowID string                     `json:"workflow_id"`
	Session    string                     `json:"session"`
	Delivery   AgentDeliveryRecoveryEntry `json:"delivery"`
	Replayed   bool                       `json:"replayed"`
}

func agentStepHash(step *Step) (string, error) {
	if step == nil {
		return "", fmt.Errorf("cannot hash a nil agent step")
	}
	encoded, err := json.Marshal(step)
	if err != nil {
		return "", fmt.Errorf("hash agent step %q: %w", step.ID, err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (e *Executor) loadAgentDelivery(stepID string) (AgentDeliveryState, bool) {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	if e.state == nil {
		return AgentDeliveryState{}, false
	}
	// Records contain only value fields, so callers cannot mutate the journal.
	record, ok := e.state.AgentDeliveries[stepID]
	return record, ok
}

func (e *Executor) saveAgentDelivery(record AgentDeliveryState) error {
	e.stateMu.Lock()
	if e.state == nil {
		e.stateMu.Unlock()
		return fmt.Errorf("cannot save agent delivery without execution state")
	}
	if err := validateAgentDelivery(record.StepID, record, e.state.Session); err != nil {
		e.stateMu.Unlock()
		return err
	}
	if record.ResolutionAction != "" {
		if _, err := agentDeliveryRecoveryEntry(e.state, record); err != nil {
			e.stateMu.Unlock()
			return err
		}
	}
	if e.state.AgentDeliveries == nil {
		e.state.AgentDeliveries = make(map[string]AgentDeliveryState)
	}
	e.state.AgentDeliveries[record.StepID] = record
	e.stateMu.Unlock()
	return e.persistState()
}

// markAgentDeliveryNotSent is only for the live owner's final preflight
// refusal, before PasteKeys. Crash recovery and post-paste errors cannot prove
// non-delivery and must retain their sending receipt. Persist this proof before
// permitting retry; a storage failure trips the existing checkpoint fence even
// if the in-memory phase has already changed.
func (e *Executor) markAgentDeliveryNotSent(record AgentDeliveryState) error {
	e.stateMu.Lock()
	if e.state == nil {
		e.stateMu.Unlock()
		return fmt.Errorf("cannot record an unsent agent delivery without execution state")
	}
	current, ok := e.state.AgentDeliveries[record.StepID]
	if !ok || current != record || record.Status != agentDeliverySending {
		e.stateMu.Unlock()
		return fmt.Errorf("unsent agent delivery for step %q no longer matches its live owner", record.StepID)
	}
	record.Status, record.BeforeOutput = agentDeliveryNotSent, ""
	e.state.AgentDeliveries[record.StepID] = record
	e.stateMu.Unlock()
	return e.persistState()
}

func validateAgentDelivery(key string, record AgentDeliveryState, session string) error {
	invalid := func(reason string) error {
		return fmt.Errorf("invalid agent delivery for step %q: %s", key, reason)
	}
	if strings.TrimSpace(key) == "" || record.StepID != key {
		return invalid("journal key does not match its step identity")
	}
	if record.Version != agentDeliveryVersion {
		return invalid(fmt.Sprintf("unsupported version %d", record.Version))
	}
	if record.Kind != StepKindPrompt && record.Kind != StepKindTemplate {
		return invalid(fmt.Sprintf("unsupported step kind %q", record.Kind))
	}
	if strings.TrimSpace(record.Session) == "" || record.Session != session {
		return invalid("session identity does not match execution state")
	}
	if record.Endpoint != "local" && (!strings.HasPrefix(record.Endpoint, "ssh:") || strings.TrimSpace(strings.TrimPrefix(record.Endpoint, "ssh:")) == "") {
		return invalid("tmux endpoint must be local or ssh: followed by a remote target")
	}
	if len(record.PaneID) < 2 || record.PaneID[0] != '%' || record.PanePID <= 0 {
		return invalid("physical pane ID and positive pane PID are required")
	}
	for _, digit := range record.PaneID[1:] {
		if digit < '0' || digit > '9' {
			return invalid("physical pane ID must contain only % followed by digits")
		}
	}
	if strings.TrimSpace(record.AgentType) == "" {
		return invalid("agent type is missing")
	}
	for _, hash := range []string{record.StepHash, record.PromptHash, record.PromptEchoHash} {
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != sha256.Size {
			return invalid("step and prompt hashes must be SHA-256 hex digests")
		}
	}
	if len(record.BeforeOutput) > agentDeliveryMaxCaptureBytes || len(record.Output) > agentDeliveryMaxOutputBytes {
		return invalid("captured output exceeds the agent delivery size limit")
	}
	if record.StartedAt.IsZero() {
		return invalid("start timestamp is missing")
	}
	if record.ResolutionAction == "" {
		if record.ResolutionToken != "" || record.ResolutionReason != "" || record.ResolutionBoundaryHash != "" || !record.ResolvedAt.IsZero() {
			return invalid("incomplete operator resolution")
		}
	} else {
		if record.ResolutionAction != AgentDeliveryResolutionAccept ||
			(record.Status != agentDeliveryDelivered && record.Status != agentDeliveryCompleted) ||
			record.ResolutionReason == "" || record.ResolutionReason != strings.TrimSpace(record.ResolutionReason) ||
			len(record.ResolutionReason) > agentDeliveryMaxReasonBytes || record.ResolvedAt.IsZero() ||
			record.ResolvedAt.Before(record.StartedAt) || !record.ResolvedAt.Equal(record.DeliveredAt) {
			return invalid("inconsistent operator acceptance")
		}
		for _, value := range []string{record.ResolutionToken, record.ResolutionBoundaryHash} {
			digest, err := hex.DecodeString(value)
			if err != nil || len(digest) != sha256.Size || value != strings.ToLower(value) {
				return invalid("invalid operator resolution fingerprint")
			}
		}
	}
	switch record.Status {
	case agentDeliveryNotSent:
		if record.BeforeOutput != "" || record.Output != "" || !record.DeliveredAt.IsZero() || !record.CompletedAt.IsZero() {
			return invalid("not-sent record contains capture, delivery, or completion evidence")
		}
	case agentDeliverySending:
		if !record.DeliveredAt.IsZero() || !record.CompletedAt.IsZero() {
			return invalid("sending record contains delivery or completion timestamps")
		}
	case agentDeliveryDelivered, agentDeliveryCompleted:
		if record.DeliveredAt.IsZero() || record.DeliveredAt.Before(record.StartedAt) {
			return invalid("delivery timestamp is missing or precedes dispatch")
		}
		if record.Status == agentDeliveryCompleted {
			if record.CompletedAt.IsZero() || record.CompletedAt.Before(record.DeliveredAt) {
				return invalid("completion timestamp is missing or precedes delivery")
			}
		} else if !record.CompletedAt.IsZero() {
			return invalid("delivered record contains a completion timestamp")
		}
	default:
		return invalid(fmt.Sprintf("unknown delivery status %q; refusing to discard dispatch evidence", record.Status))
	}
	return nil
}

// agentDeliveryReceiptToken binds the inspected attempt, including its original
// capture boundary. A decision retains that boundary's hash while subsequent
// observation can advance the transcript without changing the decision token.
func agentDeliveryReceiptToken(state *ExecutionState, record AgentDeliveryState) (string, error) {
	boundary := record.ResolutionBoundaryHash
	if record.ResolutionAction == "" {
		digest := sha256.Sum256([]byte(record.BeforeOutput))
		boundary = hex.EncodeToString(digest[:])
	}
	identity := record
	identity.Status = agentDeliverySending
	identity.BeforeOutput, identity.Output = "", ""
	identity.DeliveredAt, identity.CompletedAt, identity.ResolvedAt = time.Time{}, time.Time{}, time.Time{}
	identity.ResolutionAction, identity.ResolutionToken, identity.ResolutionReason, identity.ResolutionBoundaryHash = "", "", "", ""
	identity.StartedAt = identity.StartedAt.UTC()
	encoded, err := json.Marshal(struct {
		Version          int
		RunID            string
		WorkflowID       string
		WorkflowSnapshot string
		Receipt          AgentDeliveryState
		BoundaryHash     string
	}{1, state.RunID, state.WorkflowID, filepath.Base(state.WorkflowFile), identity, boundary})
	if err != nil {
		return "", fmt.Errorf("fingerprint agent delivery: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func agentDeliveryRecoveryEntry(state *ExecutionState, record AgentDeliveryState) (AgentDeliveryRecoveryEntry, error) {
	if err := validateAgentDelivery(record.StepID, record, state.Session); err != nil {
		return AgentDeliveryRecoveryEntry{}, err
	}
	token, err := agentDeliveryReceiptToken(state, record)
	if err != nil {
		return AgentDeliveryRecoveryEntry{}, err
	}
	if record.ResolutionAction != "" && record.ResolutionToken != token {
		return AgentDeliveryRecoveryEntry{}, fmt.Errorf("operator resolution for step %q no longer matches its original receipt identity", record.StepID)
	}
	return AgentDeliveryRecoveryEntry{
		StepID: record.StepID, Kind: record.Kind, Status: record.Status, ReceiptToken: token,
		Endpoint: record.Endpoint, PaneID: record.PaneID, PanePID: record.PanePID,
		AgentType: record.AgentType, StartedAt: record.StartedAt,
		CanAccept:        record.Status == agentDeliverySending && record.ResolutionAction == "",
		ResolutionAction: record.ResolutionAction, ResolutionReason: record.ResolutionReason, ResolvedAt: record.ResolvedAt,
	}, nil
}

// InspectAgentDeliveryRecovery reads one atomic checkpoint and exposes safe
// receipt identities. It creates no locks or state and does not contact tmux.
// Acceptance must re-read the receipt under run ownership; an inspection token
// never permits replacing a newer attempt with an older decision.
func InspectAgentDeliveryRecovery(ctx context.Context, projectDir, runID string) (*AgentDeliveryRecoveryInspection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(projectDir) == "" {
		return nil, fmt.Errorf("project directory is required for pipeline recovery")
	}
	state, err := LoadState(normalizeLockRoot(projectDir), runID)
	if err != nil {
		return nil, err
	}
	result := &AgentDeliveryRecoveryInspection{
		RunID: state.RunID, WorkflowID: state.WorkflowID, Session: state.Session,
		Deliveries: make([]AgentDeliveryRecoveryEntry, 0),
	}
	ids := make([]string, 0, len(state.AgentDeliveries))
	for id := range state.AgentDeliveries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		record := state.AgentDeliveries[id]
		if err := validateAgentDelivery(id, record, state.Session); err != nil {
			return nil, err
		}
		entry, err := agentDeliveryRecoveryEntry(state, record)
		if err != nil {
			return nil, err
		}
		if record.Status != agentDeliveryCompleted || record.ResolutionAction != "" {
			result.Deliveries = append(result.Deliveries, entry)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// AcceptAgentDeliveryContext records an explicit operator attestation for one
// uncertain delivery. It never sends, marks a step completed, or runs dependent
// work. Normal resume observes the original agent using its saved boundary.
// Frozen workflow integrity and structural ownership are checked here. Scoped
// foreach and selected branch definitions retain the executor's canonical
// materialized StepHash check on resume, before any observation result or
// dependent work.
func AcceptAgentDeliveryContext(ctx context.Context, projectDir, runID, stepID, receiptToken, reason string) (*AgentDeliveryRecoveryResult, error) {
	cfg := DefaultExecutorConfig("")
	cfg.ProjectDir, cfg.RunID = projectDir, runID
	return NewExecutor(cfg).acceptAgentDeliveryContext(ctx, stepID, receiptToken, reason)
}

func (e *Executor) acceptAgentDeliveryContext(ctx context.Context, stepID, receiptToken, reason string) (*AgentDeliveryRecoveryResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reason = strings.TrimSpace(reason)
	if strings.TrimSpace(e.config.ProjectDir) == "" || strings.TrimSpace(stepID) == "" || stepID != strings.TrimSpace(stepID) {
		return nil, fmt.Errorf("pipeline recovery requires a project directory and exact step ID")
	}
	if reason == "" || len(reason) > agentDeliveryMaxReasonBytes {
		return nil, fmt.Errorf("pipeline recovery reason must contain 1 to %d bytes", agentDeliveryMaxReasonBytes)
	}
	digest, err := hex.DecodeString(receiptToken)
	if err != nil || len(digest) != sha256.Size || receiptToken != strings.ToLower(receiptToken) {
		return nil, fmt.Errorf("pipeline recovery requires the exact inspected receipt token")
	}
	if e.config.DryRun {
		return nil, fmt.Errorf("acceptance cannot be a dry run; inspect pipeline recovery instead")
	}
	e.config.ProjectDir = normalizeLockRoot(e.config.ProjectDir)
	control, err := AcquireRunControl(ctx, e.config.ProjectDir, e.config.RunID)
	if err != nil {
		return nil, err
	}
	defer control.Close()
	ctx = control.Context()
	state, err := LoadState(e.config.ProjectDir, e.config.RunID)
	if err != nil {
		return nil, err
	}
	for id, saved := range state.AgentDeliveries {
		if err := validateAgentDelivery(id, saved, state.Session); err != nil {
			return nil, err
		}
		if _, err := agentDeliveryRecoveryEntry(state, saved); err != nil {
			return nil, err
		}
	}
	record, exists := state.AgentDeliveries[stepID]
	if !exists {
		return nil, fmt.Errorf("step %q has no saved agent delivery", stepID)
	}
	entry, err := agentDeliveryRecoveryEntry(state, record)
	if err != nil {
		return nil, err
	}
	if entry.ReceiptToken != receiptToken {
		return nil, fmt.Errorf("receipt token for step %q is stale or belongs to a different delivery", stepID)
	}
	result := &AgentDeliveryRecoveryResult{RunID: state.RunID, WorkflowID: state.WorkflowID, Session: state.Session, Delivery: entry}
	if record.ResolutionAction != "" {
		if record.ResolutionAction != AgentDeliveryResolutionAccept || record.ResolutionReason != reason {
			return nil, fmt.Errorf("step %q already has an immutable operator decision with a different action or reason", stepID)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result.Replayed = true
		return result, nil
	}
	if record.Status != agentDeliverySending {
		return nil, fmt.Errorf("step %q is %s; only an uncertain sending receipt can be accepted", stepID, record.Status)
	}
	switch state.Status {
	case StatusPending, StatusRunning, StatusFailed, StatusCancelled:
	default:
		return nil, fmt.Errorf("pipeline %q has status %q and cannot accept an unfinished delivery", state.RunID, state.Status)
	}
	if prior := state.Steps[stepID]; prior.Status == StatusCompleted || prior.Status == StatusSkipped {
		return nil, fmt.Errorf("step %q already has a terminal outcome; refusing to replace it with an operator acceptance", stepID)
	}
	if err := validateAgentRecoveryWorkflow(e.config.ProjectDir, state, record); err != nil {
		return nil, err
	}
	e.config.Session = state.Session
	release, err := e.acquirePaneLockCrossProcess(ctx, record.PaneID)
	if err != nil {
		return nil, err
	}
	defer release()
	pane, err := e.agentDeliveryPane(ctx, record.PaneID, record.PanePID, record.Endpoint)
	if err != nil {
		return nil, err
	}
	if pane.IsServicePane() || normalizeAgentType(string(pane.Type)) != normalizeAgentType(record.AgentType) {
		return nil, fmt.Errorf("original agent pane %s changed agent identity", record.PaneID)
	}
	current, err := e.captureAgentDelivery(ctx, record)
	if err != nil {
		return nil, fmt.Errorf("inspect original agent delivery: %w", err)
	}
	if _, err := agentDeliveryNewOutput(record.BeforeOutput, current); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if now.Before(record.StartedAt) {
		return nil, fmt.Errorf("agent delivery start time is in the future; refusing an inconsistent acceptance")
	}
	boundary := sha256.Sum256([]byte(record.BeforeOutput))
	record.Status, record.DeliveredAt = agentDeliveryDelivered, now
	record.ResolutionAction, record.ResolutionToken, record.ResolutionReason = AgentDeliveryResolutionAccept, receiptToken, reason
	record.ResolutionBoundaryHash, record.ResolvedAt = hex.EncodeToString(boundary[:]), now
	entry, err = agentDeliveryRecoveryEntry(state, record)
	if err != nil {
		return nil, err
	}
	state.AgentDeliveries[stepID] = record
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := SaveState(e.config.ProjectDir, state); err != nil {
		return nil, fmt.Errorf("persist operator delivery acceptance: %w", err)
	}
	result.Delivery = entry
	slog.Info("pipeline agent delivery accepted by operator", "run_id", state.RunID, "step_id", stepID, "session", state.Session, "pane", record.PaneID, "agent_type", record.AgentType)
	return result, nil
}

// recoveryWorkflowStep uses the scheduler's runtime namespace resolver, counting
// root owners so a literal authored ID cannot silently steal a scoped receipt.
func recoveryWorkflowStep(workflow *Workflow, stepID string) (*Step, error) {
	var found *Step
	for _, phase := range []struct {
		steps  []Step
		prefix string
	}{{workflow.Steps, "steps"}, {workflow.PostPipelineSteps, "post_pipeline"}, {workflow.Settings.OnCancel, "on_cancel"}} {
		for i := range phase.steps {
			if candidate, owns := resolveHookRuntimeStep(&phase.steps[i], phase.prefix, i+1, stepID); owns {
				if found != nil {
					return nil, fmt.Errorf("step %q has ambiguous ownership in the saved workflow", stepID)
				}
				found = candidate
			}
		}
	}
	if found == nil {
		return nil, fmt.Errorf("step %q has no structural owner in the saved workflow", stepID)
	}
	return found, nil
}

func validateAgentRecoveryWorkflow(projectDir string, state *ExecutionState, record AgentDeliveryState) error {
	workflow, err := loadRecoveryWorkflow(projectDir, state)
	if err != nil {
		return err
	}
	return validateRecoveryWorkflowStep(workflow, state, record.StepID, record.Kind, record.StepHash, agentStepHash)
}

func loadRecoveryWorkflow(projectDir string, state *ExecutionState) (*Workflow, error) {
	path := strings.TrimSpace(state.WorkflowFile)
	if path == "" || state.WorkflowID == "" {
		return nil, fmt.Errorf("pipeline recovery requires its saved workflow identity and definition")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(projectDir, path)
	}
	if filepath.Base(filepath.Dir(path)) != workflowSnapshotDir {
		return nil, fmt.Errorf("pipeline recovery requires a verified frozen workflow snapshot")
	}
	workflow, validation, err := LoadResumeWorkflow(path)
	if err != nil {
		return nil, fmt.Errorf("verify saved recovery workflow: %w", err)
	}
	if !validation.Valid || workflow.Name != state.WorkflowID {
		return nil, fmt.Errorf("saved recovery workflow is invalid or no longer matches pipeline %q", state.RunID)
	}
	return workflow, nil
}

// A frozen definition establishes structural ownership at acceptance. Dynamic
// scopes retain the executor's exact materialized hash check before any outcome
// can be parsed, published, or consumed by downstream work on ordinary resume.
func validateRecoveryWorkflowStep(workflow *Workflow, state *ExecutionState, stepID, kind, stepHash string, hashStep func(*Step) (string, error)) error {
	step, err := recoveryWorkflowStep(workflow, stepID)
	if err != nil {
		return err
	}
	deferred := false
	// Branch alternatives can share an authored child ID. The generic namespace
	// resolver can then return a different alternative; only normal resume uses
	// the durable selected branch and original variable scope to materialize it.
	for id, result := range state.Steps {
		if !isBranchDecisionID(id) {
			continue
		}
		decision, err := decodeBranchDecision(id, result)
		if err != nil {
			return err
		}
		parent, err := recoveryWorkflowStep(workflow, decision.ParentID)
		if err != nil {
			continue
		}
		branch, exists := parent.Branches[decision.BranchKey]
		if !exists {
			continue
		}
		children, err := parseBranchSteps(branch, decision.ParentID, decision.BranchKey)
		if err != nil {
			return err
		}
		if _, _, owns := resolveScopedRuntimeStepFromSteps(decision.ParentID, stepID, children); owns {
			deferred = true
		}
	}
	for parentID := range state.ForeachState {
		parent, err := recoveryWorkflowStep(workflow, parentID)
		if err != nil || (parent.Foreach == nil && parent.ForeachPane == nil) {
			continue
		}
		if _, _, owns := resolveScopedRuntimeChildren(parentID, stepID, parent); owns && iterationIndexFromID(stepID, parentID+"_iter") >= 0 {
			deferred = true
		}
	}
	if stepKind(step) != kind && !deferred {
		return fmt.Errorf("step %q kind differs from its saved execution", stepID)
	}
	definition := cloneStep(*step)
	definition.ID = stepID
	hash, err := hashStep(&definition)
	if err != nil {
		return err
	}
	if hash == stepHash || deferred {
		return nil
	}
	return fmt.Errorf("step %q definition differs from its saved execution", stepID)
}

func defaultResumeOptions() ResumeOptions {
	return ResumeOptions{
		Mode:           ResumeModeContinue,
		KeepState:      true,
		OnRosterChange: ResumeRosterAbort,
	}
}

func normalizeResumeOptions(opts ResumeOptions) (ResumeOptions, error) {
	if opts == (ResumeOptions{}) {
		return defaultResumeOptions(), nil
	}
	if opts.Mode == "" {
		opts.Mode = ResumeModeContinue
	}
	if opts.OnRosterChange == "" {
		opts.OnRosterChange = ResumeRosterAbort
	}
	// bd-uyjdn: a non-zero ResumeOptions that omits KeepState used to leave
	// it at the Go zero value (false), silently disabling keep-state for
	// callers that only set Mode or MaxResumeAge. Default to keep-state
	// here; callers must opt into reset via Reset=true.
	opts.KeepState = !opts.Reset

	switch opts.Mode {
	case ResumeModeContinue, ResumeModeRestartFailed:
	case ResumeModeForceIter:
		if strings.TrimSpace(opts.StepID) == "" {
			return opts, fmt.Errorf("resume mode force-iter requires StepID")
		}
		if opts.Iteration < 0 {
			return opts, fmt.Errorf("resume mode force-iter requires a non-negative Iteration")
		}
	default:
		return opts, fmt.Errorf("unknown resume mode %q", opts.Mode)
	}

	switch opts.OnRosterChange {
	case ResumeRosterAbort, ResumeRosterProceed:
	default:
		return opts, fmt.Errorf("unknown resume roster-change policy %q", opts.OnRosterChange)
	}

	return opts, nil
}

// ResumeWithOptions resumes using explicit policy without requiring callers to
// mutate ExecutorConfig directly.
func (e *Executor) ResumeWithOptions(ctx context.Context, workflow *Workflow, prior *ExecutionState, opts ResumeOptions, progress chan<- ProgressEvent) (*ExecutionState, error) {
	previous := e.config.ResumeOptions
	e.config.ResumeOptions = opts
	defer func() { e.config.ResumeOptions = previous }()
	return e.Resume(ctx, workflow, prior, progress)
}

func (e *Executor) applyResumeOptions(workflow *Workflow, opts ResumeOptions) error {
	opts, err := normalizeResumeOptions(opts)
	if err != nil {
		return err
	}

	e.stateMu.RLock()
	state := e.state
	if state == nil {
		e.stateMu.RUnlock()
		return fmt.Errorf("resume state is nil")
	}
	graph := e.graph
	if graph == nil && workflow != nil {
		graph = NewDependencyGraph(workflow)
	}
	if graph != nil {
		if phaseErrors := graph.validatePhaseOwnership(state); len(phaseErrors) > 0 {
			e.stateMu.RUnlock()
			return phaseErrors[0]
		}
	}
	checkpoint := resumeCheckpointTime(state)
	priorSession := state.Session
	targetSession := e.config.Session
	// Check every record before interpreting an explicit restart. In particular,
	// a future or corrupt status must not be erased and mistaken for permission
	// to send the same prompt again.
	for stepID, record := range state.AgentDeliveries {
		if err := validateAgentDelivery(stepID, record, priorSession); err != nil {
			e.stateMu.RUnlock()
			return err
		}
		if record.ResolutionAction != "" {
			if _, err := agentDeliveryRecoveryEntry(state, record); err != nil {
				e.stateMu.RUnlock()
				return err
			}
		}
		if targetSession != "" && priorSession != targetSession && !resumeResetsAgentDelivery(state, opts, stepID) {
			e.stateMu.RUnlock()
			return fmt.Errorf("agent delivery for step %q belongs to session %q; explicit reset is required before moving to session %q", stepID, priorSession, targetSession)
		}
	}
	for stepID, record := range state.CommandExecutions {
		if err := validateCommandExecution(stepID, record); err != nil {
			e.stateMu.RUnlock()
			return err
		}
		if record.ResolutionAction != "" {
			if _, err := commandExecutionRecoveryEntry(state, record); err != nil {
				e.stateMu.RUnlock()
				return err
			}
			if targetSession != "" && priorSession != targetSession && !resumeResetsCommandExecution(state, opts, stepID) {
				e.stateMu.RUnlock()
				return fmt.Errorf("accepted command outcome for step %q belongs to session %q; explicit reset is required before moving to session %q", stepID, priorSession, targetSession)
			}
		}
		if record.Status != commandExecutionSettled && !resumeResetsCommandExecution(state, opts, stepID) {
			e.stateMu.RUnlock()
			return uncertainCommandExecutionError(stepID, record.ProcessID)
		}
		if _, cleanup := resolveOnCancelRuntimeStep(workflow, stepID); cleanup && record.Status == commandExecutionSettled {
			// Proven-settled cleanup belongs to the previous cancellation
			// attempt. A resumed attempt can acquire resources again.
			continue
		}
		if opts.KeepState && opts.Mode != ResumeModeForceIter &&
			(record.Result.RerunOnResume || resumeResetsCommandExecution(state, opts, stepID)) {
			if err := e.validateCommandReplayScopeLocked(workflow, state, stepID); err != nil {
				e.stateMu.RUnlock()
				return err
			}
		}
	}
	if opts.KeepState && opts.Mode == ResumeModeForceIter {
		if err := validateForceResumeIteration(workflow, state, opts); err != nil {
			e.stateMu.RUnlock()
			return err
		}
	}
	if err := e.validateLegacyAgentResumeLocked(state, opts, workflow); err != nil {
		e.stateMu.RUnlock()
		return err
	}
	if err := e.validateLegacyCommandResumeLocked(state, opts, workflow); err != nil {
		e.stateMu.RUnlock()
		return err
	}
	e.stateMu.RUnlock()

	if opts.MaxResumeAge > 0 && !checkpoint.IsZero() && time.Since(checkpoint) > opts.MaxResumeAge {
		return fmt.Errorf("resume state %q is older than MaxResumeAge %s", state.RunID, opts.MaxResumeAge)
	}

	if priorSession != "" && targetSession != "" && priorSession != targetSession && opts.OnRosterChange != ResumeRosterProceed {
		return fmt.Errorf("resume state was captured for session %q but target session is %q; set OnRosterChange=proceed to override", priorSession, targetSession)
	}
	if targetSession != "" && priorSession != targetSession && opts.OnRosterChange == ResumeRosterProceed {
		e.stateMu.Lock()
		if e.state != nil {
			e.state.Session = targetSession
		}
		e.stateMu.Unlock()
	}

	if !opts.KeepState {
		e.resetResumeState(workflow)
		return nil
	}

	if opts.Mode == ResumeModeForceIter {
		e.forceResumeIteration(opts.StepID, opts.Iteration)
	} else if opts.Mode == ResumeModeRestartFailed {
		e.stateMu.Lock()
		for stepID := range e.state.AgentDeliveries {
			if resumeResetsAgentDelivery(e.state, opts, stepID) {
				delete(e.state.AgentDeliveries, stepID)
			}
		}
		for stepID := range e.state.CommandExecutions {
			if resumeResetsCommandExecution(e.state, opts, stepID) {
				// WaitNone records a synchronous Completed result while its
				// process still runs. An explicit replay must reopen that leaf.
				result := e.state.Steps[stepID]
				result.StepID, result.Status = stepID, StatusPending
				if e.state.Steps == nil {
					e.state.Steps = make(map[string]StepResult)
				}
				e.state.Steps[stepID] = result
				delete(e.state.CommandExecutions, stepID)
			}
		}
		e.stateMu.Unlock()
	}
	e.resetSettledOnCancelState(workflow)

	return nil
}

// Resolve cleanup ownership structurally, including dynamic iterations and
// success hooks. A prefix match could otherwise erase an unrelated main step
// whose authored name happens to start with a cleanup step's ID.
func resolveOnCancelRuntimeStep(workflow *Workflow, stepID string) (*Step, bool) {
	if workflow == nil {
		return nil, false
	}
	for i := range workflow.Steps {
		main := &workflow.Steps[i]
		if main.ID == stepID {
			return nil, false
		}
		if _, _, mainOwned := resolveScopedRuntimeChildren(main.ID, stepID, main); mainOwned {
			// Authored IDs can collide with another phase's materialized
			// namespace. Preserve main-work evidence when ownership is ambiguous.
			return nil, false
		}
	}
	for i := range workflow.Settings.OnCancel {
		step := &workflow.Settings.OnCancel[i]
		rootID := step.ID
		if rootID == "" {
			rootID = fmt.Sprintf("on_cancel_%d", i+1)
		}
		if stepID == rootID {
			return step, true
		}
		if child, _, ok := resolveScopedRuntimeChildren(rootID, stepID, step); ok {
			return child, true
		}
	}
	return nil, false
}

// Called only after resume preflight accepts all launch evidence. Successful
// on_cancel commands must run again after a later cancellation, including
// bodies behind completed parallel groups or foreach iteration watermarks.
// Unsettled command receipts are never discarded here.
func (e *Executor) resetSettledOnCancelState(workflow *Workflow) {
	if workflow == nil || len(workflow.Settings.OnCancel) == 0 {
		return
	}
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	e.varMu.Lock()
	defer e.varMu.Unlock()
	owned := make(map[string]*Step)
	collect := func(id string) {
		if step, ok := resolveOnCancelRuntimeStep(workflow, id); ok {
			owned[id] = step
		}
	}
	for id := range e.state.Steps {
		collect(id)
	}
	for id := range e.state.CommandExecutions {
		collect(id)
	}
	for id := range e.state.ForeachState {
		collect(id)
	}
	for id := range e.state.ParallelState {
		collect(id)
	}
	for id, step := range owned {
		if record, ok := e.state.CommandExecutions[id]; ok && record.Status == commandExecutionSettled {
			delete(e.state.CommandExecutions, id)
		}
		delete(e.state.Steps, id)
		delete(e.state.ForeachState, id)
		delete(e.state.ParallelState, id)
		delete(e.state.InFlightSteps, id)
		delete(e.state.Variables, "steps."+id+".output")
		delete(e.state.Variables, "steps."+id+".data")
		if step.OutputVar != "" {
			delete(e.state.Variables, step.OutputVar)
			delete(e.state.Variables, step.OutputVar+"_parsed")
		}
	}
}

// A crash midway through finalization adopts the completed tail. If resume
// reopens actual main work, its old handoff/cleanup belongs to the previous
// attempt and must run again after the new work. Inspect top-level roots,
// because a completed container may retain unmarked child graph placeholders.
// Admission has already validated launch evidence; unfinished receipts are
// deliberately retained even when the surrounding tail bookkeeping is reset.
func (e *Executor) resetPostPipelineForMainAttempt(workflow *Workflow) {
	if workflow == nil || len(workflow.PostPipelineSteps) == 0 || e.graph == nil {
		return
	}
	reopensMain := false
	for _, step := range workflow.Steps {
		if !e.graph.IsExecuted(step.ID) {
			reopensMain = true
			break
		}
	}
	if !reopensMain {
		return
	}
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	e.varMu.Lock()
	defer e.varMu.Unlock()
	owned := make(map[string]*Step)
	collect := func(id string) {
		if step, ok := e.graph.resolvePostPipelineRuntimeStep(id); ok {
			owned[id] = step
		}
	}
	for id := range e.state.Steps {
		collect(id)
	}
	for id := range e.state.CommandExecutions {
		collect(id)
	}
	for id := range e.state.AgentDeliveries {
		collect(id)
	}
	for id := range e.state.ForeachState {
		collect(id)
	}
	for id := range e.state.ParallelState {
		collect(id)
	}
	for id := range e.state.InFlightSteps {
		collect(id)
	}
	for id, step := range owned {
		if record, ok := e.state.CommandExecutions[id]; ok && record.Status == commandExecutionSettled {
			delete(e.state.CommandExecutions, id)
		}
		if record, ok := e.state.AgentDeliveries[id]; ok && record.Status == agentDeliveryCompleted {
			delete(e.state.AgentDeliveries, id)
		}
		delete(e.state.Steps, id)
		delete(e.state.ForeachState, id)
		delete(e.state.ParallelState, id)
		delete(e.state.InFlightSteps, id)
		delete(e.state.Variables, "steps."+id+".output")
		delete(e.state.Variables, "steps."+id+".data")
		if step.OutputVar != "" {
			delete(e.state.Variables, step.OutputVar)
			delete(e.state.Variables, step.OutputVar+"_parsed")
		}
	}
}

// validateForceResumeIteration checks the replay scope before any checkpoint
// or session mutation. A forced suffix authorizes iteration replay, not reuse
// of downstream results produced from the old loop output, nor automatic
// redispatch of those consumers. Such recovery needs an explicit whole-run
// reset. The caller holds stateMu for the supplied state.
func validateForceResumeIteration(workflow *Workflow, state *ExecutionState, opts ResumeOptions) error {
	if workflow == nil {
		return fmt.Errorf("force-iter requires a workflow")
	}
	var target *Step
	for i := range workflow.Steps {
		if workflow.Steps[i].ID == opts.StepID {
			target = &workflow.Steps[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("force-iter requires a top-level iteration step; %q is not one", opts.StepID)
	}
	if target.Foreach == nil && target.ForeachPane == nil && (target.Loop == nil || target.Loop.Items == "") {
		return fmt.Errorf("force-iter step %q must use foreach, foreach_pane, or loop.items; while/times loops have no durable iteration cursor", opts.StepID)
	}
	if prior, ok := state.ForeachState[opts.StepID]; ok {
		if prior.StepID != "" && prior.StepID != opts.StepID {
			return fmt.Errorf("force-iter checkpoint for %q names a different step %q", opts.StepID, prior.StepID)
		}
		if prior.Total <= 0 || opts.Iteration >= prior.Total {
			return fmt.Errorf("force-iter iteration %d is outside the %d recorded iterations for %q", opts.Iteration, prior.Total, opts.StepID)
		}
	} else if opts.Iteration != 0 {
		return fmt.Errorf("force-iter step %q has no recorded iterations; only iteration 0 can restart an unstarted loop", opts.StepID)
	}
	if _, recorded := state.AgentDeliveries[opts.StepID]; recorded {
		return fmt.Errorf("force-iter step %q has a leaf agent-delivery receipt; inspect it before an explicit reset", opts.StepID)
	}
	if _, recorded := state.CommandExecutions[opts.StepID]; recorded {
		return fmt.Errorf("force-iter step %q has a leaf command-execution receipt; inspect it before an explicit reset", opts.StepID)
	}
	for _, step := range workflow.Steps {
		if iterationIndexFromID(step.ID, opts.StepID+"_iter") >= opts.Iteration {
			return fmt.Errorf("force-iter namespace for %q overlaps top-level step %q; refusing to discard unrelated work", opts.StepID, step.ID)
		}
	}

	// Dependency edges are structural and independent of the live scheduler.
	// Build from the supplied workflow so detached-worker preflight (before an
	// executor graph exists) enforces the same boundary as foreground resume.
	graph := NewDependencyGraph(workflow)
	seen := map[string]bool{opts.StepID: true}
	consumerSet := make(map[string]bool)
	queue := []string{opts.StepID}
	var consumers []string
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, dependent := range graph.GetDependents(id) {
			if seen[dependent] {
				continue
			}
			seen[dependent] = true
			queue = append(queue, dependent)
			// A dependency declared in a nested body belongs to its owning
			// top-level container's runtime namespace, not the authored child
			// name. Inspect that container and follow its consumers as well.
			owner := dependent
			owners := make(map[string]bool)
			for graph.container[owner] != "" {
				if owners[owner] {
					return fmt.Errorf("force-iter dependency %q has ambiguous container ownership", dependent)
				}
				owners[owner] = true
				owner = graph.container[owner]
			}
			if owner == opts.StepID {
				continue
			}
			if !consumerSet[owner] {
				consumerSet[owner] = true
				consumers = append(consumers, owner)
			}
			if !seen[owner] {
				seen[owner] = true
				queue = append(queue, owner)
			}
		}
	}
	sort.Strings(consumers)
	for _, consumer := range consumers {
		if hasRuntimeCheckpoint(state.Steps, consumer) ||
			hasRuntimeCheckpoint(state.AgentDeliveries, consumer) ||
			hasRuntimeCheckpoint(state.CommandExecutions, consumer) ||
			hasRuntimeCheckpoint(state.InFlightSteps, consumer) ||
			hasRuntimeCheckpoint(state.ForeachState, consumer) ||
			hasRuntimeCheckpoint(state.ParallelState, consumer) {
			return fmt.Errorf("force-iter step %q has downstream recovery evidence for %q; inspect that work and use an explicit reset instead of retaining stale dependent results", opts.StepID, consumer)
		}
	}
	return nil
}

// A consumer's nested runtime work is evidence even when the containing step
// has not checkpointed a result. Ambiguous textual overlaps only refuse replay;
// they never authorize deleting another step's receipts.
func hasRuntimeCheckpoint[T any](entries map[string]T, root string) bool {
	for id := range entries {
		if id == root || strings.HasPrefix(id, root+"_") || strings.HasPrefix(id, root+".") {
			return true
		}
	}
	return false
}

func resumeResetsAgentDelivery(state *ExecutionState, opts ResumeOptions, stepID string) bool {
	if !opts.KeepState {
		return true
	}
	switch opts.Mode {
	case ResumeModeRestartFailed:
		// The receipt may reach disk before the containing step checkpoints its
		// result or clears its in-flight marker. Its completion is authoritative.
		if record, ok := state.AgentDeliveries[stepID]; ok && record.Status == agentDeliveryCompleted {
			return false
		}
		if _, inFlight := state.InFlightSteps[stepID]; inFlight {
			return true
		}
		switch state.Steps[stepID].Status {
		case StatusFailed, StatusCancelled, StatusRunning, StatusPending:
			return true
		}
	case ResumeModeForceIter:
		return iterationIndexFromID(stepID, opts.StepID+"_iter") >= opts.Iteration
	}
	return false
}

func resumeResetsCommandExecution(state *ExecutionState, opts ResumeOptions, stepID string) bool {
	if !opts.KeepState {
		return true
	}
	switch opts.Mode {
	case ResumeModeForceIter:
		return iterationIndexFromID(stepID, opts.StepID+"_iter") >= opts.Iteration
	case ResumeModeRestartFailed:
		if record, ok := state.CommandExecutions[stepID]; ok {
			if record.Status != commandExecutionSettled || record.Result.RerunOnResume {
				return true
			}
			if record.Result.Status == StatusCompleted {
				return false
			}
		}
		if _, ok := state.InFlightSteps[stepID]; ok {
			return true
		}
		if result, ok := state.Steps[stepID]; ok {
			return shouldRerunStep(result)
		}
	}
	return false
}

// Resume must classify dynamic children against the durable branch selection.
// Alternative bodies may reuse a runtime ID for different step kinds; choosing
// the first alternative can turn a recorded command into an unfinished prompt.
func resumeRuntimeStepResolver(graph *DependencyGraph, state *ExecutionState) (func(string) (*Step, bool), error) {
	type selectedBranch struct {
		parentID string
		steps    []Step
	}
	var selected []selectedBranch
	resolve := func(id string) (*Step, bool) {
		if graph == nil {
			return nil, false
		}
		if step, ok := graph.GetStep(id); ok {
			return step, true
		}
		// Alternatives may reuse child IDs with different kinds. Prefer the
		// durable selected body, from the innermost recorded branch outward.
		for i := len(selected) - 1; i >= 0; i-- {
			if child, _, ok := resolveScopedRuntimeStepFromSteps(selected[i].parentID, id, selected[i].steps); ok {
				return child, true
			}
		}
		step, _, ok := graph.ResolveScopedRuntimeStep(id)
		return step, ok
	}
	if graph == nil || state == nil {
		return resolve, nil
	}
	decisionIDs := make([]string, 0)
	for id := range state.Steps {
		if isBranchDecisionID(id) {
			decisionIDs = append(decisionIDs, id)
		}
	}
	sort.Slice(decisionIDs, func(i, j int) bool {
		if len(decisionIDs[i]) != len(decisionIDs[j]) {
			return len(decisionIDs[i]) < len(decisionIDs[j])
		}
		return decisionIDs[i] < decisionIDs[j]
	})
	for _, id := range decisionIDs {
		decision, err := decodeBranchDecision(id, state.Steps[id])
		if err != nil {
			return nil, err
		}
		parent, ok := resolve(decision.ParentID)
		if !ok {
			continue
		}
		branch, ok := parent.Branches[decision.BranchKey]
		if !ok {
			continue
		}
		children, err := parseBranchSteps(branch, decision.ParentID, decision.BranchKey)
		if err != nil {
			return nil, err
		}
		selected = append(selected, selectedBranch{parentID: decision.ParentID, steps: children})
	}
	return resolve, nil
}

// Replaying a leaf must not silently retain a completed container/iteration
// that prevents the scheduler from ever reaching it. Nor may a leaf-only
// restart implicitly invalidate dependent work already produced by that
// container. Existing force-iter and whole-run reset provide those explicit
// replay scopes. Validate before deleting any launch evidence.
func (e *Executor) validateCommandReplayScopeLocked(workflow *Workflow, state *ExecutionState, stepID string) error {
	if workflow == nil {
		return fmt.Errorf("command replay requires its saved workflow")
	}
	for i := range workflow.Steps {
		if workflow.Steps[i].ID == stepID && stepKind(&workflow.Steps[i]) == StepKindCommand {
			return nil // An independently scheduled command.
		}
	}
	refuse := func(container string) error {
		return fmt.Errorf("cannot replay command %q independently of completed or unscheduled container %q; inspect the old command and its effects, then use --mode=force-iter for a containing top-level iteration or --keep-state=false to deliberately repeat the whole workflow", stepID, container)
	}
	graph := e.graph
	if graph == nil {
		graph = NewDependencyGraph(workflow)
	}
	resolve, err := resumeRuntimeStepResolver(graph, state)
	if err != nil {
		return err
	}
	if step, ok := resolve(stepID); !ok || stepKind(step) != StepKindCommand {
		// An unknown or differently typed leaf cannot be independently replayed.
		return refuse(stepID)
	}
	for parentID, result := range state.Steps {
		if shouldRerunStep(result) {
			continue
		}
		parent, ok := resolve(parentID)
		if !ok {
			continue
		}
		if _, _, owns := resolveScopedRuntimeChildren(parentID, stepID, parent); owns {
			return refuse(parentID)
		}
	}
	for parentID, progress := range state.ForeachState {
		iterationID, ok := scopedRuntimeOrdinal(stepID, parentID+"_iter", 0)
		if !ok || !strings.HasPrefix(stepID, iterationID+"_") {
			continue
		}
		for _, completed := range progress.CompletedIterationIDs {
			if completed == iterationID {
				return refuse(iterationID)
			}
		}
		lastRound := progress.CompletedRounds[iterationID]
		parent, ok := resolve(parentID)
		if !ok || lastRound <= 0 {
			continue
		}
		for _, config := range []*ForeachConfig{parent.Foreach, parent.ForeachPane} {
			if config == nil {
				continue
			}
			body, err := foreachBodySteps(parent, config)
			if err != nil {
				return refuse(parentID)
			}
			for i, child := range body {
				name := child.ID
				if name == "" {
					name = fmt.Sprintf("step%d", i)
				}
				prefix := iterationID + "_" + name + "_round"
				roundID, ok := scopedRuntimeOrdinal(stepID, prefix, 1)
				if ok {
					round, _ := strconv.Atoi(strings.TrimPrefix(roundID, prefix))
					if round <= lastRound {
						return refuse(roundID)
					}
				}
			}
		}
	}
	return nil
}

// Existing terminal failures retain ordinary retry semantics. A legacy running
// command, however, has no proof its old process stopped or even that its side
// effects were captured. It needs the same explicit replay decision as a new
// unresolved launch receipt. The caller holds stateMu.
func (e *Executor) validateLegacyCommandResumeLocked(state *ExecutionState, opts ResumeOptions, workflow *Workflow) error {
	if !opts.KeepState {
		return nil
	}
	graph := e.graph
	if graph == nil && workflow != nil {
		graph = NewDependencyGraph(workflow)
	}
	resolve, err := resumeRuntimeStepResolver(graph, state)
	if err != nil {
		return err
	}
	unfinished := make(map[string]bool)
	for stepID := range state.InFlightSteps {
		unfinished[stepID] = true
	}
	for stepID, result := range state.Steps {
		if result.Status == StatusRunning || result.Status == StatusPending {
			unfinished[stepID] = true
		}
	}
	for stepID := range unfinished {
		if _, recorded := state.CommandExecutions[stepID]; recorded || resumeResetsCommandExecution(state, opts, stepID) {
			continue
		}
		command := state.InFlightSteps[stepID].Kind == StepKindCommand || state.Steps[stepID].AgentType == "command"
		if step, ok := resolve(stepID); ok {
			command = command || stepKind(step) == StepKindCommand
		}
		if command {
			return uncertainCommandExecutionError(stepID, 0)
		}
	}
	return nil
}

// The caller holds stateMu. Legacy checkpoints have no way to distinguish a
// delivered prompt from work that never reached tmux. Only an explicit restart
// can authorize redispatch of a known unfinished agent leaf without a receipt.
func (e *Executor) validateLegacyAgentResumeLocked(state *ExecutionState, opts ResumeOptions, workflow *Workflow) error {
	if !opts.KeepState {
		return nil
	}
	graph := e.graph
	if graph == nil && workflow != nil {
		// Detached resume preflight precedes executor graph construction.
		// Classify against the same workflow there instead of silently
		// bypassing the missing-delivery guard until after worker launch.
		graph = NewDependencyGraph(workflow)
	}
	if graph == nil {
		return nil
	}
	resolve, err := resumeRuntimeStepResolver(graph, state)
	if err != nil {
		return err
	}
	unfinished := make(map[string]bool)
	for stepID, result := range state.Steps {
		switch result.Status {
		case StatusFailed, StatusCancelled, StatusRunning, StatusPending:
			unfinished[stepID] = true
		}
	}
	for stepID := range state.InFlightSteps {
		unfinished[stepID] = true
	}
	for stepID := range unfinished {
		if _, recorded := state.AgentDeliveries[stepID]; recorded || resumeResetsAgentDelivery(state, opts, stepID) {
			continue
		}
		step, ok := resolve(stepID)
		// A changed/ambiguous runtime namespace must not erase explicit
		// evidence that the old execution dispatched agent work. The saved
		// kind is independent of the current workflow's interpretation.
		kind := state.InFlightSteps[stepID].Kind
		agentWork := kind == StepKindPrompt || kind == StepKindTemplate
		if ok {
			agentWork = agentWork || stepKind(step) == StepKindPrompt || stepKind(step) == StepKindTemplate
		}
		if agentWork {
			return fmt.Errorf("unfinished agent step %q has no durable delivery evidence; use an explicit restart-failed or reset to authorize resending", stepID)
		}
	}
	return nil
}

func (e *Executor) resetResumeState(workflow *Workflow) {
	e.stateMu.Lock()
	if e.state == nil {
		e.stateMu.Unlock()
		return
	}
	e.state.Steps = make(map[string]StepResult)
	e.state.ForeachState = nil
	e.state.ParallelState = nil
	e.state.ScopeStack = nil
	e.state.InFlightSteps = nil
	e.state.AgentDeliveries = nil
	e.state.CommandExecutions = nil
	e.state.CurrentStep = ""
	e.state.Errors = nil
	e.stateMu.Unlock()

	e.varMu.Lock()
	if e.state != nil && e.state.Variables != nil {
		clearWorkflowStepVariables(e.state.Variables, workflow)
	}
	e.varMu.Unlock()
}

func clearWorkflowStepVariables(vars map[string]interface{}, workflow *Workflow) {
	for key := range vars {
		if strings.HasPrefix(key, "steps.") {
			delete(vars, key)
		}
	}
	if workflow == nil {
		return
	}
	var walk func([]Step)
	walk = func(steps []Step) {
		for _, step := range steps {
			if step.OutputVar != "" {
				delete(vars, step.OutputVar)
				delete(vars, step.OutputVar+"_parsed")
			}
			if len(step.Parallel.Steps) > 0 {
				walk(step.Parallel.Steps)
			}
			if step.Loop != nil {
				walk(step.Loop.Steps)
			}
			if step.Foreach != nil {
				walk(step.Foreach.Steps)
			}
			if step.ForeachPane != nil {
				walk(step.ForeachPane.Steps)
			}
			walk(step.OnSuccess)
		}
	}
	walk(workflow.Steps)
	walk(workflow.PostPipelineSteps)
}

func (e *Executor) forceResumeIteration(stepID string, iteration int) {
	if strings.TrimSpace(stepID) == "" || iteration < 0 {
		return
	}
	var prunedStepIDs []string
	e.stateMu.Lock()
	if e.state == nil {
		e.stateMu.Unlock()
		return
	}
	if e.state.ForeachState == nil {
		e.state.ForeachState = make(map[string]ForeachIterationState)
	}
	state := e.state.ForeachState[stepID]
	state.StepID = stepID
	state.CurrentIteration = iteration
	state.CompletedIterationIDs = filterCompletedIterationsBefore(state.CompletedIterationIDs, stepID, iteration)
	prefix := fmt.Sprintf("%s_iter", stepID)
	// A forced iteration must restart its rounds too. Otherwise max_rounds
	// can skip every round even after its StepResults have been removed.
	removeIterationEntries(state.CompletedRounds, prefix, iteration)
	// bd-t3q8a: sequential foreach (loops.go) appends collected outputs in
	// iteration order, so truncating to length=iteration drops the entries
	// for the iterations that are about to rerun. Without this, the
	// rerun would observe duplicated entries (prior + reran) in the final
	// loop.Collect store.
	if iteration >= 0 && iteration < len(state.CollectedOutputs) {
		state.CollectedOutputs = append([]interface{}(nil), state.CollectedOutputs[:iteration]...)
	}
	state.UpdatedAt = time.Now()
	e.state.ForeachState[stepID] = state
	// Reopen the containing scheduler node as well as its iteration suffix.
	// Keeping a completed parent result would make applyResumeState mark the
	// whole loop executed and silently skip the requested replay.
	delete(e.state.Steps, stepID)
	delete(e.state.InFlightSteps, stepID)
	prunedStepIDs = append(prunedStepIDs, stepID)

	// All iteration-local checkpoints participate in the rewind. Nested
	// loops and parallel groups otherwise restore stale completion markers;
	// receipts and in-flight markers can exist before any StepResult does.
	prunedStepIDs = append(prunedStepIDs, removeIterationEntries(e.state.Steps, prefix, iteration)...)
	prunedStepIDs = append(prunedStepIDs, removeIterationEntries(e.state.AgentDeliveries, prefix, iteration)...)
	prunedStepIDs = append(prunedStepIDs, removeIterationEntries(e.state.CommandExecutions, prefix, iteration)...)
	prunedStepIDs = append(prunedStepIDs, removeIterationEntries(e.state.InFlightSteps, prefix, iteration)...)
	prunedStepIDs = append(prunedStepIDs, removeIterationEntries(e.state.ParallelState, prefix, iteration)...)
	prunedStepIDs = append(prunedStepIDs, removeIterationEntries(e.state.ForeachState, prefix, iteration)...)
	e.stateMu.Unlock()

	// bd-a3fwf: deleting StepResults is not enough — Substitutor.resolveSteps
	// reads flat steps.<id>.output / steps.<id>.data variables (and per-step
	// output_var entries) before falling back to state.Steps. Without
	// scrubbing those flat keys, a forced rerun from iteration N can still
	// resolve ghost outputs from iterations >=N whose StepResults were
	// pruned. Clear variables outside stateMu to keep lock ordering consistent
	// with substituteVariablesStrict (varMu acquired separately, never nested
	// under stateMu — see snapshotState bd-xuxev note). Use a graph-nil-safe
	// inline path rather than executor.clearStepVariables: forceResumeIteration
	// can be invoked before the workflow graph is rebuilt (and in unit tests
	// the graph is never set), and the executor helper would NPE on
	// e.graph.GetStep(id) in those cases.
	e.varMu.Lock()
	if e.state != nil && e.state.Variables != nil {
		// Flat output keys may survive a checkpoint without a StepResult.
		// Scan them independently rather than relying on prunedStepIDs alone.
		for key := range e.state.Variables {
			if !strings.HasPrefix(key, "steps.") {
				continue
			}
			id := strings.TrimPrefix(key, "steps.")
			switch {
			case strings.HasSuffix(id, ".output"):
				id = strings.TrimSuffix(id, ".output")
			case strings.HasSuffix(id, ".data"):
				id = strings.TrimSuffix(id, ".data")
			default:
				continue
			}
			if iterationIndexFromID(id, prefix) >= iteration {
				delete(e.state.Variables, key)
				prunedStepIDs = append(prunedStepIDs, id)
			}
		}
		for _, id := range prunedStepIDs {
			delete(e.state.Variables, "steps."+id+".output")
			delete(e.state.Variables, "steps."+id+".data")
			if e.graph != nil {
				step, ok := e.graph.GetStep(id)
				if !ok {
					step, _, ok = e.graph.ResolveScopedRuntimeStep(id)
				}
				if ok {
					if step.OutputVar != "" {
						delete(e.state.Variables, step.OutputVar)
						delete(e.state.Variables, step.OutputVar+"_parsed")
					}
					if step.Loop != nil && step.Loop.Collect != "" {
						delete(e.state.Variables, step.Loop.Collect)
					}
				}
			}
		}
	}
	e.varMu.Unlock()
}

func filterCompletedIterationsBefore(ids []string, stepID string, iteration int) []string {
	prefix := fmt.Sprintf("%s_iter", stepID)
	out := ids[:0]
	for _, id := range ids {
		idx := iterationIndexFromID(id, prefix)
		if idx >= 0 && idx < iteration {
			out = append(out, id)
		}
	}
	return out
}

func iterationIndexFromID(id, prefix string) int {
	if !strings.HasPrefix(id, prefix) {
		return -1
	}
	rest := strings.TrimPrefix(id, prefix)
	for i, r := range rest {
		if r < '0' || r > '9' {
			// Runtime descendants are separated with underscores. An arbitrary
			// textual suffix may instead name an unrelated workflow step.
			if i == 0 || r != '_' {
				return -1
			}
			var idx int
			if _, err := fmt.Sscanf(rest[:i], "%d", &idx); err != nil {
				return -1
			}
			return idx
		}
	}
	var idx int
	if _, err := fmt.Sscanf(rest, "%d", &idx); err != nil {
		return -1
	}
	return idx
}

// removeIterationEntries clears a suffix of a loop's runtime namespace while
// preserving prior iterations and unrelated work. Callers hold the lock that
// protects entries. Returning the removed IDs also permits output-var cleanup
// for checkpoints that have no corresponding StepResult yet.
func removeIterationEntries[T any](entries map[string]T, prefix string, iteration int) []string {
	if iteration < 0 {
		return nil
	}
	var removed []string
	for id := range entries {
		if iterationIndexFromID(id, prefix) >= iteration {
			delete(entries, id)
			removed = append(removed, id)
		}
	}
	return removed
}

func (e *Executor) markStepInFlight(stepID, kind string, iteration int) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil || stepID == "" {
		return
	}
	if e.state.InFlightSteps == nil {
		e.state.InFlightSteps = make(map[string]InFlightStepState)
	}
	e.state.InFlightSteps[stepID] = InFlightStepState{
		StepID:    stepID,
		Kind:      kind,
		StartedAt: time.Now(),
		Iteration: iteration,
	}
}

func (e *Executor) clearStepInFlight(stepID string) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil || e.state.InFlightSteps == nil {
		return
	}
	delete(e.state.InFlightSteps, stepID)
	if len(e.state.InFlightSteps) == 0 {
		e.state.InFlightSteps = nil
	}
}

func (e *Executor) beginForeachState(stepID string, total int) int {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil || stepID == "" {
		return 0
	}
	if e.state.ForeachState == nil {
		e.state.ForeachState = make(map[string]ForeachIterationState)
	}
	state := e.state.ForeachState[stepID]
	now := time.Now()
	if state.StepID == "" {
		state.StepID = stepID
		state.StartedAt = now
	}
	state.Total = total
	state.UpdatedAt = now
	// bd-p12ti: CompletedIterationIDs is the durable record of work safe to
	// skip; CurrentIteration is only a cursor and must not jump past an
	// incomplete gap. Always start at the first incomplete iteration so a
	// persisted state with a gap (partial writes, force-resume edge cases,
	// future out-of-order completion metadata) does not silently drop the
	// missing iteration on resume.
	start := firstIncompleteIteration(state.CompletedIterationIDs, stepID, total)
	state.CurrentIteration = start
	e.state.ForeachState[stepID] = state
	return start
}

// computeForeachItemsFingerprint returns a stable hash of the resolved
// foreach items so resume can detect drift when items resolve from a
// dynamic source (vars expression, prior step output, glob, etc.).
//
// Empty inputs hash deterministically (the empty-array form), matching the
// JSON encoding so an unchanged empty-source resume verifies cleanly.
// Unmarshalable values fall back to the Go-formatted form: a fingerprint
// is best-effort drift detection, not a security boundary.
func computeForeachItemsFingerprint(items []interface{}) string {
	if items == nil {
		items = []interface{}{}
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		encoded = []byte(fmt.Sprintf("%#v", items))
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// verifyForeachItemsFingerprint compares the supplied fingerprint against
// the one persisted on prior foreach state. Returns nil if no prior state
// exists, the fingerprints match, or there is no recorded fingerprint
// (legacy state files predate this field). Any iteration-local progress binds
// the items, not just whole-iteration completion: interrupted rounds, nested
// checkpoints and dispatch receipts can all predate the first completed
// iteration. Reusing them against changed items would skip work or attribute
// an earlier dispatch to a different item.
func (e *Executor) verifyForeachItemsFingerprint(stepID, fingerprint string) error {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	if e.state == nil || e.state.ForeachState == nil {
		return nil
	}
	prior, ok := e.state.ForeachState[stepID]
	if !ok {
		return nil
	}
	if prior.ItemsFingerprint == "" || prior.ItemsFingerprint == fingerprint {
		return nil
	}
	prefix := fmt.Sprintf("%s_iter", stepID)
	hasProgress := len(prior.CompletedIterationIDs) > 0 ||
		len(prior.CompletedRounds) > 0 || len(prior.CollectedOutputs) > 0 ||
		hasIterationEntries(e.state.Steps, prefix) ||
		hasIterationEntries(e.state.AgentDeliveries, prefix) ||
		hasIterationEntries(e.state.CommandExecutions, prefix) ||
		hasIterationEntries(e.state.InFlightSteps, prefix) ||
		hasIterationEntries(e.state.ForeachState, prefix) ||
		hasIterationEntries(e.state.ParallelState, prefix)
	if !hasProgress {
		return nil
	}
	// Precision bounds diagnostics without slicing unchecked checkpoint data.
	// A corrupt/short fingerprint must produce an error, not panic on resume.
	return fmt.Errorf("foreach %q items changed since prior run (completed=%d, prior_fingerprint=%.12s, current_fingerprint=%.12s); resume would apply old iteration records to different items",
		stepID, len(prior.CompletedIterationIDs), prior.ItemsFingerprint, fingerprint)
}

// hasIterationEntries recognizes only this loop's runtime namespace. Callers
// hold the read lock protecting entries; unrelated steps do not bind its items.
func hasIterationEntries[T any](entries map[string]T, prefix string) bool {
	for id := range entries {
		if iterationIndexFromID(id, prefix) >= 0 {
			return true
		}
	}
	return false
}

// appendForeachCollectedOutput persists a single loop.Collect output for
// a completed iteration. The slice on ForeachIterationState mirrors the
// in-memory LoopResult.Collected so a mid-loop resume can reconstruct
// outputs from iterations completed before the interruption (bd-t3q8a).
// Sequential foreach is the only caller; parallel foreach (foreach.go)
// has its own bookkeeping path.
func (e *Executor) appendForeachCollectedOutput(stepID string, value interface{}) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil {
		return
	}
	if e.state.ForeachState == nil {
		e.state.ForeachState = make(map[string]ForeachIterationState)
	}
	state := e.state.ForeachState[stepID]
	if state.StepID == "" {
		state.StepID = stepID
		state.StartedAt = time.Now()
	}
	state.CollectedOutputs = append(state.CollectedOutputs, value)
	state.UpdatedAt = time.Now()
	e.state.ForeachState[stepID] = state
}

// loadForeachCollectedOutputs returns a copy of the persisted collected
// outputs for a foreach step, or nil if none exist (bd-t3q8a). The copy
// isolates the caller's slice from concurrent appends to the underlying
// state.
func (e *Executor) loadForeachCollectedOutputs(stepID string) []interface{} {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	if e.state == nil || e.state.ForeachState == nil {
		return nil
	}
	state, ok := e.state.ForeachState[stepID]
	if !ok || len(state.CollectedOutputs) == 0 {
		return nil
	}
	out := make([]interface{}, len(state.CollectedOutputs))
	copy(out, state.CollectedOutputs)
	return out
}

// recordForeachItemsFingerprint stores the fingerprint of the resolved
// items on the foreach state. Idempotent: re-records the same fingerprint
// on resume when nothing has changed, so verifyForeachItemsFingerprint can
// catch drift on a subsequent resume even after a checkpointed run that
// was originally written before this field existed.
func (e *Executor) recordForeachItemsFingerprint(stepID, fingerprint string) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil {
		return
	}
	if e.state.ForeachState == nil {
		e.state.ForeachState = make(map[string]ForeachIterationState)
	}
	state := e.state.ForeachState[stepID]
	if state.StepID == "" {
		state.StepID = stepID
		state.StartedAt = time.Now()
	}
	state.ItemsFingerprint = fingerprint
	state.UpdatedAt = time.Now()
	e.state.ForeachState[stepID] = state
}

func firstIncompleteIteration(completed []string, stepID string, total int) int {
	if total <= 0 {
		return 0
	}
	done := make(map[int]bool, len(completed))
	prefix := fmt.Sprintf("%s_iter", stepID)
	for _, id := range completed {
		idx := iterationIndexFromID(id, prefix)
		if idx >= 0 {
			done[idx] = true
		}
	}
	for i := 0; i < total; i++ {
		if !done[i] {
			return i
		}
	}
	return total
}

func (e *Executor) markForeachIterationStarted(stepID string, iteration, total int) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil || stepID == "" {
		return
	}
	if e.state.ForeachState == nil {
		e.state.ForeachState = make(map[string]ForeachIterationState)
	}
	state := e.state.ForeachState[stepID]
	if state.StepID == "" {
		state.StepID = stepID
		state.StartedAt = time.Now()
	}
	state.CurrentIteration = iteration
	state.Total = total
	state.UpdatedAt = time.Now()
	e.state.ForeachState[stepID] = state
}

// foreachIterationCompletedRounds returns the highest fully-completed round
// recorded for the given iteration ID, or 0 if none. Used by
// executeForeachIteration to skip rounds on resume that have already
// finished in a prior run (bd-r2pan).
func (e *Executor) foreachIterationCompletedRounds(stepID, iterID string) int {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	if e.state == nil || e.state.ForeachState == nil {
		return 0
	}
	state, ok := e.state.ForeachState[stepID]
	if !ok || state.CompletedRounds == nil {
		return 0
	}
	return state.CompletedRounds[iterID]
}

// markForeachIterationRoundCompleted records that round N of the given
// iteration finished cleanly so a subsequent resume can skip it (bd-r2pan).
// Only called from the max_rounds path; single-round iterations don't write
// here so legacy state files stay byte-identical to pre-bd-r2pan output.
func (e *Executor) markForeachIterationRoundCompleted(stepID, iterID string, round int) {
	if stepID == "" || iterID == "" || round <= 0 {
		return
	}
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil {
		return
	}
	if e.state.ForeachState == nil {
		e.state.ForeachState = make(map[string]ForeachIterationState)
	}
	state := e.state.ForeachState[stepID]
	if state.StepID == "" {
		state.StepID = stepID
		state.StartedAt = time.Now()
	}
	if state.CompletedRounds == nil {
		state.CompletedRounds = make(map[string]int)
	}
	if cur := state.CompletedRounds[iterID]; round > cur {
		state.CompletedRounds[iterID] = round
	}
	state.UpdatedAt = time.Now()
	e.state.ForeachState[stepID] = state
}

func (e *Executor) markForeachIterationCompleted(stepID string, iteration, total int) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil || stepID == "" {
		return
	}
	if e.state.ForeachState == nil {
		e.state.ForeachState = make(map[string]ForeachIterationState)
	}
	state := e.state.ForeachState[stepID]
	if state.StepID == "" {
		state.StepID = stepID
		state.StartedAt = time.Now()
	}
	iterID := loopIterationID(stepID, iteration)
	if !stringSliceContains(state.CompletedIterationIDs, iterID) {
		state.CompletedIterationIDs = append(state.CompletedIterationIDs, iterID)
	}
	state.CurrentIteration = iteration + 1
	state.Total = total
	state.UpdatedAt = time.Now()
	e.state.ForeachState[stepID] = state
}

func loopIterationID(stepID string, iteration int) string {
	return fmt.Sprintf("%s_iter%d", stepID, iteration)
}

func iterationSucceeded(results []StepResult, shouldBreak bool) bool {
	if shouldBreak {
		return false
	}
	for _, result := range results {
		switch result.Status {
		case StatusFailed, StatusCancelled, StatusRunning, StatusPending:
			return false
		}
	}
	return true
}

// shouldCompleteForeachIteration decides whether to record an iteration
// as durably complete. completedAllSteps (bd-vq8bc) is the explicit signal
// from executeIteration that every nested step in loop.Steps was processed,
// distinguishing a fully-executed iteration that observed late ctx
// cancellation (safe to checkpoint) from a partial iteration cancelled
// mid-body (must rerun on resume so the unfinished body steps still run).
//
// Replaces the previous len(results)>0 heuristic, which silently dropped
// remaining body steps when ctx fired between the success of step N and
// the dispatch of step N+1 — see bd-vq8bc impact analysis.
func shouldCompleteForeachIteration(ctx context.Context, results []StepResult, shouldBreak, completedAllSteps bool) bool {
	if !iterationSucceeded(results, shouldBreak) {
		return false
	}
	if ctx.Err() == nil {
		return true
	}
	return completedAllSteps
}

func (e *Executor) beginParallelState(stepID string, total int) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil || stepID == "" {
		return
	}
	if e.state.ParallelState == nil {
		e.state.ParallelState = make(map[string]ParallelGroupState)
	}
	state := e.state.ParallelState[stepID]
	now := time.Now()
	if state.StepID == "" {
		state.StepID = stepID
		state.StartedAt = now
	}
	state.Total = total
	state.UpdatedAt = now
	e.state.ParallelState[stepID] = state
}

func (e *Executor) markParallelSubstepStarted(groupID, stepID string) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil || e.state.ParallelState == nil {
		return
	}
	state := e.state.ParallelState[groupID]
	state.InFlightStepIDs = appendUniqueString(state.InFlightStepIDs, stepID)
	state.UpdatedAt = time.Now()
	e.state.ParallelState[groupID] = state
}

func (e *Executor) markParallelSubstepFinished(groupID, stepID string, status ExecutionStatus) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil || e.state.ParallelState == nil {
		return
	}
	state := e.state.ParallelState[groupID]
	state.InFlightStepIDs = removeString(state.InFlightStepIDs, stepID)
	switch status {
	case StatusCompleted, StatusSkipped:
		state.CompletedStepIDs = appendUniqueString(state.CompletedStepIDs, stepID)
	case StatusFailed, StatusCancelled:
		state.FailedStepIDs = appendUniqueString(state.FailedStepIDs, stepID)
	}
	state.UpdatedAt = time.Now()
	e.state.ParallelState[groupID] = state
}

func (e *Executor) completeParallelState(groupID string) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil || e.state.ParallelState == nil {
		return
	}
	state := e.state.ParallelState[groupID]
	state.InFlightStepIDs = nil
	state.AllSubstepsSettled = true
	state.CompletedAt = time.Now()
	state.UpdatedAt = state.CompletedAt
	e.state.ParallelState[groupID] = state
}

func (e *Executor) pushScopeFrameLocked(frame ScopeFrame) {
	if e.state == nil {
		return
	}
	e.state.ScopeStack = append(e.state.ScopeStack, frame)
}

func (e *Executor) popScopeFrameLocked() {
	if e.state == nil || len(e.state.ScopeStack) == 0 {
		return
	}
	e.state.ScopeStack = e.state.ScopeStack[:len(e.state.ScopeStack)-1]
}

func appendUniqueString(values []string, value string) []string {
	if value == "" || stringSliceContains(values, value) {
		return values
	}
	return append(values, value)
}

func stringSliceContains(values []string, value string) bool {
	for _, current := range values {
		if current == value {
			return true
		}
	}
	return false
}

func removeString(values []string, value string) []string {
	out := values[:0]
	for _, current := range values {
		if current != value {
			out = append(out, current)
		}
	}
	return out
}

func resumeCheckpointTime(state *ExecutionState) time.Time {
	if state == nil {
		return time.Time{}
	}
	if !state.LastCheckpointAt.IsZero() {
		return state.LastCheckpointAt
	}

	checkpoint := state.StartedAt
	// bd-05l02: legacy state files often record only UpdatedAt and have no
	// child timestamps (cancelled/persisted before any step result, or a
	// minimal fixture). Without this, resumeCheckpointTime would return
	// the zero time and applyResumeOptions would skip the MaxResumeAge
	// check, letting stale legacy state bypass the resume safety contract.
	checkpoint = mostRecentTime(checkpoint, state.UpdatedAt)
	checkpoint = mostRecentTime(checkpoint, state.FinishedAt)

	for _, result := range state.Steps {
		checkpoint = mostRecentTime(checkpoint, stepResultCheckpointTime(result))
	}
	for _, execErr := range state.Errors {
		checkpoint = mostRecentTime(checkpoint, execErr.Timestamp)
	}
	for _, foreach := range state.ForeachState {
		checkpoint = mostRecentTime(checkpoint, foreach.StartedAt)
		checkpoint = mostRecentTime(checkpoint, foreach.UpdatedAt)
	}
	for _, parallel := range state.ParallelState {
		checkpoint = mostRecentTime(checkpoint, parallel.StartedAt)
		checkpoint = mostRecentTime(checkpoint, parallel.UpdatedAt)
		checkpoint = mostRecentTime(checkpoint, parallel.CompletedAt)
	}
	for _, inFlight := range state.InFlightSteps {
		checkpoint = mostRecentTime(checkpoint, inFlight.StartedAt)
	}

	return checkpoint
}

func stepResultCheckpointTime(result StepResult) time.Time {
	checkpoint := mostRecentTime(result.StartedAt, result.FinishedAt)
	if result.Error != nil {
		checkpoint = mostRecentTime(checkpoint, result.Error.Timestamp)
	}
	return checkpoint
}

func mostRecentTime(a, b time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	if b.IsZero() {
		return a
	}
	if b.After(a) {
		return b
	}
	return a
}

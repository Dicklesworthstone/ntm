// Package robot: operation_idempotency.go implements the durable idempotent
// operation protocol shared by every retry-safe robot actuation: --robot-send
// (#245) and --robot-interrupt with --op-id, plus their REST Idempotency-Key
// equivalents.
//
// A caller may supply an operation ID. The operation is claimed atomically
// in the runtime projection store BEFORE the actuation mutates any pane and
// is durably bound to the actuation kind plus a kind-specific digest of the
// caller's command spec (sendOperationBindingHash,
// interruptOperationBindingHash).
//
//   - An identical retry of a completed operation returns the original
//     recorded outcome without touching any pane again.
//   - Reusing an operation ID with a different command spec — or for a
//     different actuation kind — is rejected as IDEMPOTENCY_CONFLICT.
//     Operation IDs form one namespace per session across kinds.
//   - A retry that races a live concurrent claimant observes the operation
//     in progress (OPERATION_IN_PROGRESS) and is told to reconcile via
//     --robot-send-receipt; a claim abandoned by a crashed process is taken
//     over after a staleness window only if it never crossed the durable
//     dispatch boundary. Once input may have been delivered, an unknown
//     outcome requires reconciliation, never implicit redelivery.
//   - An actuation that terminates before mutating anything releases its
//     claim so the ID stays retryable; once a mutation was attempted, the
//     outcome is recorded as terminal.
//
// Receipts expose only a payload digest and byte count — never the payload
// bytes — so ordinary logs gain an audit trail without retaining message
// contents.
package robot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

// Typed per-target admission states. Admission is about whether the target
// pane accepted the actuation's submission — it deliberately claims nothing
// about agent comprehension (see --verify-render for rendered-output
// evidence).
const (
	// AdmissionNotAttempted: the operation terminated before this target
	// was attempted (preflight failure, selector error, block), or the
	// actuation had nothing to submit to it (an interrupt target that was
	// already ready and received no follow-up message).
	AdmissionNotAttempted = "not_attempted"
	// AdmissionSubmitted: the pane accepted the submission.
	AdmissionSubmitted = "submitted"
	// AdmissionRejected: the pane was attempted and delivery failed.
	AdmissionRejected = "rejected"
	// AdmissionUnknown: the outcome could not be determined (crash or
	// in-flight operation); reconcile via the receipt query.
	AdmissionUnknown = "unknown"
)

// ErrCodeIdempotencyConflict signals that an operation ID was reused with a
// different command spec (selector, delivery toggles, or input message) or
// for a different actuation kind than the one it is durably bound to.
const ErrCodeIdempotencyConflict = "IDEMPOTENCY_CONFLICT"

// ErrCodeOperationInProgress signals that the operation is claimed but its
// outcome is not yet recorded (concurrent caller, or a crash mid-actuation).
const ErrCodeOperationInProgress = "OPERATION_IN_PROGRESS"

// ErrCodeOperationOutcomeUnknown means dispatch started but no terminal
// outcome was recorded before the claim became stale. Repeating the same
// operation must not repeat its possibly completed external side effects.
const ErrCodeOperationOutcomeUnknown = "OPERATION_OUTCOME_UNKNOWN"

// operationStaleClaimWindow is how long an in_progress claim is trusted
// before a retry may recover an unstarted claim. Age alone never proves
// that an actuation did not happen, nor that the original caller is dead:
// the durable dispatch marker and ownership token enforce those boundaries.
const operationStaleClaimWindow = 10 * time.Minute

// OperationAdmission is the typed per-target admission receipt.
type OperationAdmission struct {
	Target string `json:"target"`
	State  string `json:"state"` // not_attempted | submitted | rejected | unknown
	Error  string `json:"error,omitempty"`
}

// OperationInfo is the public view of a durable idempotent operation
// attached to actuation output (send, interrupt) and returned by receipt
// queries.
type OperationInfo struct {
	OperationID       string               `json:"operation_id"`
	Kind              string               `json:"kind"`   // send | interrupt
	Status            string               `json:"status"` // in_progress | completed
	Replayed          bool                 `json:"replayed,omitempty"`
	PayloadSHA256     string               `json:"payload_sha256"`
	PayloadBytes      int64                `json:"payload_bytes"`
	Admissions        []OperationAdmission `json:"admissions,omitempty"`
	CreatedAt         time.Time            `json:"created_at"`
	DispatchStartedAt *time.Time           `json:"dispatch_started_at,omitempty"`
	CompletedAt       *time.Time           `json:"completed_at,omitempty"`
}

// operationPayloadDigest returns the SHA-256 digest (hex) and byte count of
// the exact payload string NTM attempts to deliver.
func operationPayloadDigest(payload string) (string, int64) {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:]), int64(len(payload))
}

// operationBindingHasher accumulates a caller's canonical command spec into
// a binding digest. A field is its bytes followed by NUL; a list is its
// trimmed, non-empty members in sorted order followed by 0x01, so logically
// identical selector lists bind identically. The encoding is load-bearing:
// changing it turns every recorded operation ID into a conflict
// (TestSendOperationBindingHashIsStableAcrossVersions pins it).
type operationBindingHasher struct {
	h hash.Hash
}

func newOperationBindingHasher() *operationBindingHasher {
	return &operationBindingHasher{h: sha256.New()}
}

func (b *operationBindingHasher) field(value string) {
	b.h.Write([]byte(value))
	b.h.Write([]byte{0})
}

func (b *operationBindingHasher) list(values []string) {
	sorted := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			sorted = append(sorted, v)
		}
	}
	sort.Strings(sorted)
	for _, v := range sorted {
		b.field(v)
	}
	b.h.Write([]byte{1})
}

func (b *operationBindingHasher) sum() string {
	return hex.EncodeToString(b.h.Sum(nil))
}

// operationRecordKind returns the actuation kind of a stored record; rows
// written before kinds existed were all sends.
func operationRecordKind(op *state.SendOperation) string {
	if op == nil || op.Kind == "" {
		return state.OperationKindSend
	}
	return op.Kind
}

// operationAdmissionsOutcome is the admissions subset every recorded outcome
// carries, decoded generically for operation views.
type operationAdmissionsOutcome struct {
	Admissions []OperationAdmission `json:"admissions"`
}

func operationInfoFromRecord(op *state.SendOperation, replayed bool) *OperationInfo {
	if op == nil {
		return nil
	}
	info := &OperationInfo{
		OperationID:       op.OperationID,
		Kind:              operationRecordKind(op),
		Status:            op.Status,
		Replayed:          replayed,
		PayloadSHA256:     op.PayloadSHA256,
		PayloadBytes:      op.PayloadBytes,
		CreatedAt:         op.CreatedAt,
		DispatchStartedAt: op.DispatchStartedAt,
		CompletedAt:       op.CompletedAt,
	}
	if op.Status == state.SendOperationInProgress {
		info.Admissions = unknownAdmissions(op.Targets)
	}
	if op.OutcomeJSON != "" {
		var outcome operationAdmissionsOutcome
		if err := json.Unmarshal([]byte(op.OutcomeJSON), &outcome); err == nil {
			info.Admissions = outcome.Admissions
		}
	}
	return info
}

// unknownAdmissions marks every target as outcome-unknown; used when an
// operation is observed in progress.
func unknownAdmissions(targets []string) []OperationAdmission {
	admissions := make([]OperationAdmission, 0, len(targets))
	for _, target := range targets {
		admissions = append(admissions, OperationAdmission{Target: target, State: AdmissionUnknown})
	}
	return admissions
}

// operationClaimRequest describes one idempotent actuation about to mutate
// panes.
type operationClaimRequest struct {
	Kind        string // state.OperationKindSend | state.OperationKindInterrupt
	OperationID string
	Session     string
	// BindingHash is the kind-specific digest of the caller's command spec.
	BindingHash string
	// Payload is the exact post-transformation payload about to be
	// delivered; only its digest and byte count are persisted.
	Payload string
	// Targets are the resolved targets, reported as outcome-unknown when a
	// live claimant holds the operation.
	Targets       []string
	Store         *state.Store `json:"-"`
	ExplicitStore bool         `json:"-"`
}

// operationClaimVerdict tells an actuation how to proceed after a claim.
type operationClaimVerdict int

const (
	// operationExecute: this caller owns the claim (fresh, or a stale claim
	// taken over) and must execute, then finish the claim exactly once via
	// durableOperation.complete or durableOperation.release.
	operationExecute operationClaimVerdict = iota
	// operationReplay: an identical operation already completed; replay its
	// recorded outcome without mutating anything.
	operationReplay
	// operationRefused: the actuation must not run — the store is
	// unavailable or failed, the binding conflicts, or a live claimant holds
	// the operation. Response carries the typed error envelope.
	operationRefused
)

// operationClaimResult is the outcome of claimRobotOperation.
type operationClaimResult struct {
	Verdict operationClaimVerdict
	// Record is the completed row to replay (operationReplay).
	Record *state.SendOperation
	// Owned is the claim the caller must finish (operationExecute).
	Owned *durableOperation
	// Response is the refusal envelope (operationRefused).
	Response RobotResponse
	// Info is the operation view to attach to a refusal, or nil when no
	// record applies (store unavailable or failed).
	Info *OperationInfo
}

// claimRobotOperation runs the durable claim protocol shared by every
// idempotent robot actuation. It must be called after the actuation's
// preflight validation and BEFORE its first pane mutation.
func claimRobotOperation(req operationClaimRequest) operationClaimResult {
	refuse := func(resp RobotResponse, info *OperationInfo) operationClaimResult {
		return operationClaimResult{Verdict: operationRefused, Response: resp, Info: info}
	}

	store := resolveProjectionStore(req.Store, req.ExplicitStore)
	if store == nil {
		return refuse(NewErrorResponse(
			fmt.Errorf("idempotent %s requires the runtime projection store", req.Kind),
			ErrCodeNotImplemented,
			"Re-run without an operation ID, or ensure the runtime state store is available",
		), nil)
	}

	payloadSHA, payloadBytes := operationPayloadDigest(req.Payload)
	claim := &state.SendOperation{
		OperationID:   req.OperationID,
		SessionName:   req.Session,
		Kind:          req.Kind,
		BindingHash:   req.BindingHash,
		PayloadSHA256: payloadSHA,
		PayloadBytes:  payloadBytes,
		Targets:       req.Targets,
	}
	stored, claimed, err := store.ClaimSendOperation(claim)
	if err != nil {
		return refuse(NewErrorResponse(err, ErrCodeInternalError, fmt.Sprintf("Failed to claim %s operation", req.Kind)), nil)
	}
	if claimed {
		return operationClaimResult{Verdict: operationExecute, Owned: &durableOperation{store: store, record: stored}}
	}

	if storedKind := operationRecordKind(stored); storedKind != req.Kind {
		return refuse(NewErrorResponse(
			fmt.Errorf("operation ID '%s' is already bound to a %s operation in session '%s'", req.OperationID, storedKind, req.Session),
			ErrCodeIdempotencyConflict,
			fmt.Sprintf("Use a fresh operation ID for this %s; operation IDs share one namespace per session across actuations", req.Kind),
		), operationInfoFromRecord(stored, false))
	}
	if stored.BindingHash != req.BindingHash {
		return refuse(NewErrorResponse(
			fmt.Errorf("operation ID '%s' is already bound to a different selector or payload in session '%s'", req.OperationID, req.Session),
			ErrCodeIdempotencyConflict,
			fmt.Sprintf("Use a fresh operation ID for a different %s, or repeat the original command exactly", req.Kind),
		), operationInfoFromRecord(stored, false))
	}
	if stored.Status == state.SendOperationCompleted {
		return operationClaimResult{Verdict: operationReplay, Record: stored}
	}
	if stored.Status == state.SendOperationCanceled {
		return refuse(NewErrorResponse(
			fmt.Errorf("operation '%s' was canceled before dispatch", req.OperationID),
			"OPERATION_CANCELED",
			"Use a fresh operation ID if you want to dispatch this actuation now",
		), operationInfoFromRecord(stored, false))
	}

	staleBefore := time.Now().UTC().Add(-operationStaleClaimWindow)
	if stored.DispatchStartedAt == nil && stored.CreatedAt.Before(staleBefore) {
		// A stale claimant that has not started may be recovered. The CAS
		// rotates its ownership token; a paused original caller can no longer
		// start, complete, or release the replacement operation.
		takenOver, takeoverErr := store.TakeOverStaleSendOperation(
			claim.OperationID, claim.SessionName, claim.BindingHash, stored.ClaimToken, staleBefore,
		)
		if takeoverErr != nil {
			return refuse(NewErrorResponse(takeoverErr, ErrCodeInternalError,
				"Could not recover the operation claim; no input was sent"), operationInfoFromRecord(stored, false))
		}
		if takenOver != nil {
			return operationClaimResult{Verdict: operationExecute, Owned: &durableOperation{store: store, record: takenOver}}
		}
		// The original caller may have completed while we attempted the CAS.
		// Report its current receipt instead of the obsolete pre-CAS state.
		current, readErr := store.GetSendOperation(claim.OperationID, claim.SessionName)
		if readErr != nil {
			return refuse(NewErrorResponse(readErr, ErrCodeInternalError,
				"Could not read the updated operation claim; no input was sent"), nil)
		}
		if current != nil {
			stored = current
			if operationRecordKind(stored) != req.Kind || stored.BindingHash != req.BindingHash {
				return refuse(NewErrorResponse(fmt.Errorf("operation '%s' was claimed by a different command", req.OperationID),
					ErrCodeIdempotencyConflict, "Use the original command or a new operation ID"), operationInfoFromRecord(stored, false))
			}
			if stored.Status == state.SendOperationCompleted {
				return operationClaimResult{Verdict: operationReplay, Record: stored}
			}
			if stored.Status == state.SendOperationCanceled {
				return refuse(NewErrorResponse(
					fmt.Errorf("operation '%s' was canceled before dispatch", req.OperationID),
					"OPERATION_CANCELED",
					"Use a fresh operation ID if you want to dispatch this actuation now",
				), operationInfoFromRecord(stored, false))
			}
		}
	}
	info := operationInfoFromRecord(stored, true)
	if stored.DispatchStartedAt != nil && stored.CreatedAt.Before(staleBefore) {
		return refuse(NewErrorResponse(
			fmt.Errorf("operation '%s' started dispatch but its outcome is unknown", req.OperationID),
			ErrCodeOperationOutcomeUnknown,
			unresolvedOperationHint(req.OperationID),
		), info)
	}
	return refuse(NewErrorResponse(
		fmt.Errorf("operation '%s' is in progress", req.OperationID),
		ErrCodeOperationInProgress,
		fmt.Sprintf("Query the durable receipt with --robot-send-receipt=%s before retrying", req.OperationID),
	), info)
}

// durableOperation is a claim owned by the current actuation. It must be
// finished exactly once: complete after any pane mutation was attempted,
// release when the actuation terminated before mutating anything.
type durableOperation struct {
	store  *state.Store
	record *state.SendOperation
}

// start records the exact prepared payload and targets before any pane can
// be mutated. Losing the ownership fence or failing this write stops the
// actuation. A crash after this point leaves evidence that replay is unsafe.
func (op *durableOperation) start(payload string, targets []string) error {
	prepared := *op.record
	prepared.PayloadSHA256, prepared.PayloadBytes = operationPayloadDigest(payload)
	prepared.Targets = append([]string{}, targets...)
	started, err := op.store.StartSendOperation(&prepared)
	if err != nil {
		return err
	}
	op.record = started
	return nil
}

func operationStartErrorResponse(err error) RobotResponse {
	if errors.Is(err, state.ErrSendOperationClaimLost) {
		return NewErrorResponse(err, ErrCodeOperationInProgress,
			"Operation ownership changed before dispatch; query its durable receipt before retrying")
	}
	return NewErrorResponse(err, ErrCodeInternalError,
		"The dispatch boundary could not be recorded; no input was sent")
}

func unresolvedOperationHint(operationID string) string {
	return fmt.Sprintf("Inspect --robot-send-receipt=%s and the original panes to resolve the prior delivery; this operation ID will not dispatch again. Use a new ID only after deciding that another delivery is needed", operationID)
}

// release frees the claim when the actuation terminated BEFORE any pane was
// touched (preflight failure). A retry with the same operation ID must get a
// fresh attempt rather than a stored transient failure. Best-effort: the
// returned warning is empty on success.
func (op *durableOperation) release() string {
	if err := op.store.ReleaseSendOperation(op.record.OperationID, op.record.SessionName, op.record.ClaimToken); err != nil {
		return fmt.Sprintf("%s operation %s claim not released: %v (retry may report in-progress until taken over)",
			operationRecordKind(op.record), op.record.OperationID, err)
	}
	return ""
}

// complete persists the terminal outcome (a kind-specific outcome record
// that embeds its admissions) and returns the completed operation view.
// Best-effort: a persistence failure is returned as a warning rather than
// failing an actuation that already happened, and the view is then nil.
func (op *durableOperation) complete(outcome any, admissions []OperationAdmission) (*OperationInfo, string) {
	kind := operationRecordKind(op.record)
	data, err := json.Marshal(outcome)
	if err != nil {
		return nil, fmt.Sprintf("%s operation %s outcome not recorded: %v", kind, op.record.OperationID, err)
	}
	completedAt := time.Now().UTC()
	if err := op.store.CompleteSendOperation(op.record.OperationID, op.record.SessionName, op.record.ClaimToken, string(data), completedAt); err != nil {
		return nil, fmt.Sprintf("%s operation %s outcome not recorded: %v", kind, op.record.OperationID, err)
	}
	op.record.Status = state.SendOperationCompleted
	op.record.CompletedAt = &completedAt
	info := operationInfoFromRecord(op.record, false)
	info.Admissions = admissions
	return info, ""
}

// replayedOperationInfo is the operation view attached to a replayed
// outcome: the stored record, flagged replayed, with the recorded admissions.
func replayedOperationInfo(op *state.SendOperation, admissions []OperationAdmission) *OperationInfo {
	info := operationInfoFromRecord(op, true)
	info.Admissions = admissions
	return info
}

// SendReceiptOutput is the structured output for --robot-send-receipt, the
// durable receipt of any idempotent robot operation (send or interrupt).
type SendReceiptOutput struct {
	RobotResponse
	Session   string         `json:"session,omitempty"`
	Warnings  []string       `json:"warnings,omitempty"`
	Operation *OperationInfo `json:"operation,omitempty"`
	// Outcome carries the recorded terminal result of a completed send
	// operation (operation.kind == "send").
	Outcome *SendReceiptOutcome `json:"outcome,omitempty"`
	// InterruptOutcome carries the recorded terminal result of a completed
	// interrupt operation (operation.kind == "interrupt").
	InterruptOutcome *InterruptReceiptOutcome `json:"interrupt_outcome,omitempty"`
}

// GetSendReceipt returns the durable receipt for an operation ID, whichever
// actuation (send or interrupt) claimed it.
func GetSendReceipt(operationID string) (*SendReceiptOutput, error) {
	operationID = strings.TrimSpace(operationID)
	output := &SendReceiptOutput{RobotResponse: NewRobotResponse(true)}
	if operationID == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("operation ID is required"),
			ErrCodeInvalidFlag,
			"Pass the operation ID supplied to --robot-send or --robot-interrupt via --op-id",
		)
		return output, nil
	}

	store := currentProjectionStore()
	if store == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("operation receipts require the runtime projection store"),
			ErrCodeNotImplemented,
			"The runtime state store is unavailable in this invocation",
		)
		return output, nil
	}

	ops, err := store.GetSendOperationsByID(operationID)
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Failed to read operation record")
		return output, nil
	}
	if len(ops) == 0 {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("operation '%s' not found", operationID),
			ErrCodeNotFound,
			"Unknown operation ID; receipts exist only for --robot-send/--robot-interrupt calls that supplied --op-id (or a REST Idempotency-Key)",
		)
		return output, nil
	}
	// Operation IDs are scoped per session; the same ID may exist in several
	// sessions. Report the newest and surface the others so the caller can
	// disambiguate.
	op := &ops[0]
	if len(ops) > 1 {
		sessions := make([]string, 0, len(ops))
		for _, other := range ops {
			sessions = append(sessions, other.SessionName)
		}
		output.Warnings = append(output.Warnings, fmt.Sprintf(
			"operation ID %q exists in %d sessions (%s); showing the most recent (%s)",
			operationID, len(ops), strings.Join(sessions, ", "), op.SessionName))
	}

	output.Session = op.SessionName
	output.Operation = operationInfoFromRecord(op, false)
	if op.Status != state.SendOperationCompleted || op.OutcomeJSON == "" {
		if op.DispatchStartedAt != nil && op.CreatedAt.Before(time.Now().UTC().Add(-operationStaleClaimWindow)) {
			output.Hint = unresolvedOperationHint(op.OperationID)
		}
		return output, nil
	}
	var decodeErr error
	switch kind := operationRecordKind(op); kind {
	case state.OperationKindSend:
		output.Outcome, decodeErr = sendReceiptOutcomeFromJSON(op.OutcomeJSON)
	case state.OperationKindInterrupt:
		output.InterruptOutcome, decodeErr = interruptReceiptOutcomeFromJSON(op.OutcomeJSON)
	default:
		output.Warnings = append(output.Warnings, fmt.Sprintf(
			"operation kind %q is not known to this ntm; the recorded outcome is omitted", kind))
	}
	if decodeErr != nil {
		output.Warnings = append(output.Warnings, fmt.Sprintf("recorded outcome could not be decoded: %v", decodeErr))
	}
	return output, nil
}

// PrintSendReceipt handles the --robot-send-receipt command.
func PrintSendReceipt(operationID string) error {
	output, err := GetSendReceipt(operationID)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot send-receipt failed")
}

// ActionStatusOutput is the unified request/operation inspection output for
// --robot-action-status and --robot-wait-request (docs/robot-request-identity.md).
type ActionStatusOutput struct {
	RobotResponse
	RequestID         string                   `json:"request_id"`
	Source            string                   `json:"source,omitempty"` // send_operation | wait_handle
	Kind              string                   `json:"kind,omitempty"`   // send | interrupt | wait
	Session           string                   `json:"session,omitempty"`
	Status            string                   `json:"status,omitempty"` // in_progress | completed | canceled
	Terminal          bool                     `json:"terminal"`
	Cancellable       bool                     `json:"cancellable"`
	CreatedAt         string                   `json:"created_at,omitempty"`
	DispatchStartedAt string                   `json:"dispatch_started_at,omitempty"`
	CompletedAt       string                   `json:"completed_at,omitempty"`
	WaitedMs          int64                    `json:"waited_ms,omitempty"`
	Warnings          []string                 `json:"warnings,omitempty"`
	Operation         *OperationInfo           `json:"operation,omitempty"`
	Outcome           *SendReceiptOutcome      `json:"outcome,omitempty"`
	InterruptOutcome  *InterruptReceiptOutcome `json:"interrupt_outcome,omitempty"`
}

// WaitRequestOptions configures --robot-wait-request polling.
type WaitRequestOptions struct {
	RequestID     string
	Timeout       time.Duration
	PollInterval  time.Duration
	Store         *state.Store
	ExplicitStore bool
}

// CancelRequestOutput is the structured output for --robot-cancel-request.
type CancelRequestOutput struct {
	RobotResponse
	RequestID   string         `json:"request_id"`
	Source      string         `json:"source,omitempty"`
	Kind        string         `json:"kind,omitempty"`
	Session     string         `json:"session,omitempty"`
	Status      string         `json:"status,omitempty"`
	Canceled    bool           `json:"canceled"`
	CompletedAt string         `json:"completed_at,omitempty"`
	Operation   *OperationInfo `json:"operation,omitempty"`
}

// GetActionStatus inspects any durable robot operation or wait handle by ID.
func GetActionStatus(requestID string) (*ActionStatusOutput, error) {
	return GetActionStatusWithStore(requestID, nil, false)
}

// GetActionStatusWithStore inspects a durable robot operation or wait handle
// using an explicit store (for example from ntm serve) or the process
// projection store when explicitStore is false.
func GetActionStatusWithStore(requestID string, store *state.Store, explicitStore bool) (*ActionStatusOutput, error) {
	requestID = strings.TrimSpace(requestID)
	output := &ActionStatusOutput{
		RobotResponse: NewRobotResponse(true),
		RequestID:     requestID,
	}
	if requestID == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("request ID is required"),
			ErrCodeInvalidFlag,
			"Pass --robot-action-status=OP_ID or --robot-action-status --request-id=OP_ID",
		)
		return output, nil
	}

	if !explicitStore {
		store = currentProjectionStore()
	}
	if store == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("request status inspection requires the runtime projection store"),
			ErrCodeNotImplemented,
			"The runtime state store is unavailable in this invocation",
		)
		return output, nil
	}

	ops, err := store.GetSendOperationsByID(requestID)
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Failed to read operation record")
		return output, nil
	}
	if len(ops) > 0 {
		op := &ops[0]
		if len(ops) > 1 {
			sessions := make([]string, 0, len(ops))
			for _, other := range ops {
				sessions = append(sessions, other.SessionName)
			}
			output.Warnings = append(output.Warnings, fmt.Sprintf(
				"request ID %q exists in %d sessions (%s); showing the most recent (%s)",
				requestID, len(ops), strings.Join(sessions, ", "), op.SessionName,
			))
		}
		kind := operationRecordKind(op)
		output.Source = "send_operation"
		output.Kind = kind
		output.Session = op.SessionName
		output.Status = op.Status
		output.Terminal = op.Status == state.SendOperationCompleted || op.Status == state.SendOperationCanceled
		output.Cancellable = op.Status == state.SendOperationInProgress && op.DispatchStartedAt == nil
		output.CreatedAt = FormatTimestamp(op.CreatedAt)
		if op.DispatchStartedAt != nil {
			output.DispatchStartedAt = FormatTimestamp(*op.DispatchStartedAt)
		}
		if op.CompletedAt != nil {
			output.CompletedAt = FormatTimestamp(*op.CompletedAt)
		}
		output.Operation = operationInfoFromRecord(op, false)
		if op.Status == state.SendOperationCompleted && op.OutcomeJSON != "" {
			var decodeErr error
			switch kind {
			case state.OperationKindSend:
				output.Outcome, decodeErr = sendReceiptOutcomeFromJSON(op.OutcomeJSON)
			case state.OperationKindInterrupt:
				output.InterruptOutcome, decodeErr = interruptReceiptOutcomeFromJSON(op.OutcomeJSON)
			}
			if decodeErr != nil {
				output.Warnings = append(output.Warnings, fmt.Sprintf("recorded outcome could not be decoded: %v", decodeErr))
			}
		} else if op.Status == state.SendOperationInProgress && op.DispatchStartedAt != nil &&
			op.CreatedAt.Before(time.Now().UTC().Add(-operationStaleClaimWindow)) {
			output.Hint = unresolvedOperationHint(op.OperationID)
		}
		return output, nil
	}

	waitHandle, err := store.GetRobotWaitHandle(requestID)
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Failed to read wait handle")
		return output, nil
	}
	if waitHandle != nil {
		output.Source = "wait_handle"
		output.Kind = "wait"
		output.Session = waitHandle.SessionName
		output.CreatedAt = FormatTimestamp(waitHandle.CreatedAt)
		switch {
		case waitHandle.CanceledAt != nil:
			output.Status = state.SendOperationCanceled
			output.Terminal = true
			output.CompletedAt = FormatTimestamp(*waitHandle.CanceledAt)
		case waitHandle.CompletedAt != nil:
			output.Status = state.SendOperationCompleted
			output.Terminal = true
			output.CompletedAt = FormatTimestamp(*waitHandle.CompletedAt)
		default:
			output.Status = state.SendOperationInProgress
			output.Terminal = false
			output.Cancellable = true
		}
		return output, nil
	}

	output.RobotResponse = NewErrorResponse(
		fmt.Errorf("request '%s' not found", requestID),
		ErrCodeNotFound,
		"Unknown request/operation ID; pass an --op-id from --robot-send/--robot-interrupt, a REST Idempotency-Key, or a wait_id from --robot-wait",
	)
	return output, nil
}

// PrintActionStatus executes --robot-action-status.
func PrintActionStatus(requestID string) error {
	output, err := GetActionStatus(requestID)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot action-status failed")
}

// GetWaitRequest blocks until a durable operation or wait handle reaches a
// terminal state, or returns TIMEOUT when opts.Timeout elapses.
func GetWaitRequest(ctx context.Context, opts WaitRequestOptions) (*ActionStatusOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	poll := opts.PollInterval
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}

	started := time.Now()
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		status, err := GetActionStatusWithStore(opts.RequestID, opts.Store, opts.ExplicitStore)
		if err != nil {
			return nil, err
		}
		status.WaitedMs = time.Since(started).Milliseconds()
		if !status.Success || status.Terminal {
			return status, nil
		}

		timer := time.NewTimer(poll)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			status.WaitedMs = time.Since(started).Milliseconds()
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				status.RobotResponse = NewErrorResponse(
					fmt.Errorf("timed out after %s waiting for request '%s' (status=%s)", timeout, strings.TrimSpace(opts.RequestID), status.Status),
					ErrCodeTimeout,
					fmt.Sprintf("Re-check with --robot-action-status=%s or increase --timeout", strings.TrimSpace(opts.RequestID)),
				)
			} else {
				status.RobotResponse = NewErrorResponse(
					waitCtx.Err(),
					ErrCodeInternalError,
					"Wait interrupted before request reached a terminal state",
				)
			}
			return status, nil
		case <-timer.C:
		}
	}
}

// PrintWaitRequest executes --robot-wait-request.
func PrintWaitRequest(ctx context.Context, opts WaitRequestOptions) error {
	output, err := GetWaitRequest(ctx, opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot wait-request failed")
}

// GetCancelRequest cancels a pre-dispatch durable operation claim or an active
// --robot-wait handle, refusing safely if dispatch already crossed the pane
// mutation boundary or the request is already terminal.
func GetCancelRequest(requestID string) (*CancelRequestOutput, error) {
	return GetCancelRequestWithStore(requestID, nil, false)
}

// GetCancelRequestWithStore cancels a pre-dispatch durable operation claim or
// an active wait handle using an explicit store (for example from ntm serve)
// or the process projection store when explicitStore is false.
func GetCancelRequestWithStore(requestID string, store *state.Store, explicitStore bool) (*CancelRequestOutput, error) {
	requestID = strings.TrimSpace(requestID)
	output := &CancelRequestOutput{
		RobotResponse: NewRobotResponse(true),
		RequestID:     requestID,
	}
	if requestID == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("request ID is required"),
			ErrCodeInvalidFlag,
			"Pass --robot-cancel-request=OP_ID or --robot-cancel-request --request-id=OP_ID",
		)
		return output, nil
	}

	if !explicitStore {
		store = currentProjectionStore()
	}
	if store == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("request cancellation requires the runtime projection store"),
			ErrCodeNotImplemented,
			"The runtime state store is unavailable in this invocation",
		)
		return output, nil
	}

	now := time.Now().UTC()
	op, canceled, err := store.CancelSendOperationByID(requestID, now)
	if errors.Is(err, state.ErrSendOperationDispatched) && op != nil {
		output.Source = "send_operation"
		output.Kind = operationRecordKind(op)
		output.Session = op.SessionName
		output.Status = op.Status
		output.Operation = operationInfoFromRecord(op, false)
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("request '%s' already started dispatch and cannot be canceled safely", requestID),
			"REQUEST_ALREADY_DISPATCHED",
			unresolvedOperationHint(requestID),
		)
		return output, nil
	}
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Failed to cancel operation")
		return output, nil
	}
	if op != nil {
		output.Source = "send_operation"
		output.Kind = operationRecordKind(op)
		output.Session = op.SessionName
		output.Status = op.Status
		if op.CompletedAt != nil {
			output.CompletedAt = FormatTimestamp(*op.CompletedAt)
		}
		output.Operation = operationInfoFromRecord(op, false)
		if !canceled {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("request '%s' is already %s and cannot be canceled", requestID, op.Status),
				"REQUEST_NOT_CANCELLABLE",
				fmt.Sprintf("Inspect final state via --robot-action-status=%s", requestID),
			)
			return output, nil
		}
		output.Canceled = true
		return output, nil
	}

	waitHandle, err := store.GetRobotWaitHandle(requestID)
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Failed to read wait handle")
		return output, nil
	}
	if waitHandle != nil {
		output.Source = "wait_handle"
		output.Kind = "wait"
		output.Session = waitHandle.SessionName
		waitCanceled, cancelErr := store.CancelRobotWaitHandle(requestID, now)
		if cancelErr != nil {
			output.RobotResponse = NewErrorResponse(cancelErr, ErrCodeInternalError, "Failed to cancel wait handle")
			return output, nil
		}
		if !waitCanceled {
			status := state.SendOperationCompleted
			if waitHandle.CanceledAt != nil {
				status = state.SendOperationCanceled
			}
			output.Status = status
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("wait '%s' is already %s and cannot be canceled", requestID, status),
				"REQUEST_NOT_CANCELLABLE",
				fmt.Sprintf("Inspect final state via --robot-action-status=%s", requestID),
			)
			return output, nil
		}
		output.Status = state.SendOperationCanceled
		output.Canceled = true
		output.CompletedAt = FormatTimestamp(now)
		return output, nil
	}

	output.RobotResponse = NewErrorResponse(
		fmt.Errorf("request '%s' not found", requestID),
		ErrCodeNotFound,
		"Unknown request/operation ID; pass an --op-id from --robot-send/--robot-interrupt, a REST Idempotency-Key, or a wait_id from --robot-wait",
	)
	return output, nil
}

// PrintCancelRequest executes --robot-cancel-request.
func PrintCancelRequest(requestID string) error {
	output, err := GetCancelRequest(requestID)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot cancel-request failed")
}

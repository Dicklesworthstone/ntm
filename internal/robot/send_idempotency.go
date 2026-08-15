// Package robot: send_idempotency.go implements durable idempotent send
// operations and per-target admission receipts (#245).
//
// A caller may supply an operation ID with --robot-send (or the REST
// Idempotency-Key header). The operation is claimed atomically in the
// runtime projection store BEFORE any keystroke is injected and is durably
// bound to the caller's command spec — session, target selector, delivery
// toggles, and a digest of the caller's input message (see
// sendOperationBindingHash for why the selector and input message are
// bound rather than the resolved panes and delivered payload).
//
//   - An identical retry of a completed operation returns the original
//     recorded outcome without sending again.
//   - Reusing an operation ID with a different command spec is rejected as
//     a conflict.
//   - A retry that races a live concurrent sender observes the operation
//     in progress and is told to reconcile via --robot-send-receipt; a
//     claim abandoned by a crashed process is taken over after a staleness
//     window.
//   - Preflight failures (nothing typed) release the claim so the ID stays
//     retryable; only real dispatch attempts record terminal outcomes.
//
// The receipt exposes only a payload digest and byte count — never the
// payload bytes — so ordinary logs gain an audit trail without retaining
// message contents.
package robot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

// Typed per-target admission states. Admission is about whether the target
// pane accepted the submission — it deliberately claims nothing about agent
// comprehension (see --verify-render for rendered-output evidence).
const (
	// AdmissionNotAttempted: the operation terminated before this target
	// was attempted (preflight failure, selector error, block).
	AdmissionNotAttempted = "not_attempted"
	// AdmissionSubmitted: the pane accepted the keystroke submission.
	AdmissionSubmitted = "submitted"
	// AdmissionRejected: the pane was attempted and delivery failed.
	AdmissionRejected = "rejected"
	// AdmissionUnknown: the outcome could not be determined (crash or
	// in-flight operation); reconcile via the receipt query.
	AdmissionUnknown = "unknown"
)

// ErrCodeIdempotencyConflict signals that an operation ID was reused with a
// different command spec (selector, delivery toggles, or input message)
// than the one it is durably bound to.
const ErrCodeIdempotencyConflict = "IDEMPOTENCY_CONFLICT"

// ErrCodeOperationInProgress signals that the operation is claimed but its
// outcome is not yet recorded (concurrent sender, or a crash mid-send).
const ErrCodeOperationInProgress = "OPERATION_IN_PROGRESS"

// sendOperationStaleClaimWindow is how long an in_progress claim is trusted
// before a retry may take it over. Sends complete in seconds; a claim this
// old with no recorded outcome means the original claimant died before its
// deferred completion ran. Taking over risks at most a duplicate delivery
// when the dead process had already typed keystrokes — the caller opted
// into retry semantics by reusing the operation ID, and the alternative is
// an operation ID poisoned forever.
const sendOperationStaleClaimWindow = 10 * time.Minute

// SendAdmission is the typed per-target admission receipt.
type SendAdmission struct {
	Target string `json:"target"`
	State  string `json:"state"` // not_attempted | submitted | rejected | unknown
	Error  string `json:"error,omitempty"`
}

// SendOperationInfo is the public view of a durable send operation attached
// to send output and returned by receipt queries.
type SendOperationInfo struct {
	OperationID   string          `json:"operation_id"`
	Status        string          `json:"status"` // in_progress | completed
	Replayed      bool            `json:"replayed,omitempty"`
	PayloadSHA256 string          `json:"payload_sha256"`
	PayloadBytes  int64           `json:"payload_bytes"`
	Admissions    []SendAdmission `json:"admissions,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	CompletedAt   *time.Time      `json:"completed_at,omitempty"`
}

// sendOperationOutcome is the durable outcome record stored as JSON in the
// send_operations row and replayed verbatim to identical retries.
type sendOperationOutcome struct {
	Success        bool            `json:"success"`
	SentAt         time.Time       `json:"sent_at"`
	Targets        []string        `json:"targets"`
	Successful     []string        `json:"successful"`
	Failed         []SendError     `json:"failed"`
	Admissions     []SendAdmission `json:"admissions"`
	Error          string          `json:"error,omitempty"`
	ErrorCode      string          `json:"error_code,omitempty"`
	MessagePreview string          `json:"message_preview,omitempty"`
}

// sendPayloadDigest returns the SHA-256 digest (hex) and byte count of the
// exact payload string NTM attempts to deliver.
func sendPayloadDigest(payload string) (string, int64) {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:]), int64(len(payload))
}

// sendOperationBindingHash binds an operation to the caller's canonical
// COMMAND spec: session, target selector, delivery behavior, and a digest
// of the caller's INPUT message.
//
// The selector (not the resolved pane list) keeps a byte-identical retry a
// replay even when pane topology changed between attempts: under --all or
// --type the caller cannot re-specify "the original targets", only the
// original selector. List-valued selectors are sorted so logically
// identical retries bind identically.
//
// The input message (opts.Message, before CASS injection) is used rather
// than the delivered payload because CASS injection is time-varying: a
// byte-identical `--with-cass` retry would otherwise digest differently and
// be rejected as a conflict. The receipt's PayloadSHA256 still records the
// exact post-transformation bytes NTM attempted to deliver.
//
// Delivery-behavior flags (--enter/--submit, --clear-input) are bound too:
// reusing an operation ID with a different submit behavior is a different
// operation, not a replay of the original.
func sendOperationBindingHash(opts SendOptions) string {
	h := sha256.New()
	writeField := func(field string) {
		h.Write([]byte(field))
		h.Write([]byte{0})
	}
	writeList := func(values []string) {
		sorted := make([]string, 0, len(values))
		for _, v := range values {
			v = strings.TrimSpace(v)
			if v != "" {
				sorted = append(sorted, v)
			}
		}
		sort.Strings(sorted)
		for _, v := range sorted {
			writeField(v)
		}
		h.Write([]byte{1})
	}

	writeField(opts.Session)
	if opts.All {
		writeField("all")
	}
	writeField(opts.Pane)
	writeList(opts.Panes)
	writeList(opts.AgentTypes)
	writeList(opts.Exclude)
	enter := "default"
	if opts.Enter != nil {
		enter = strconv.FormatBool(*opts.Enter)
	}
	writeField(enter)
	writeField(strconv.FormatBool(opts.ClearInput))
	// The --with-cass/--with-memory TOGGLES are part of the command; the
	// injected content is deliberately not (it varies between attempts).
	writeField(strconv.FormatBool(opts.WithCASS))
	writeField(strconv.FormatBool(opts.WithMemory))
	inputSHA, _ := sendPayloadDigest(opts.Message)
	writeField(inputSHA)
	return hex.EncodeToString(h.Sum(nil))
}

// admissionsFromSendOutput derives typed per-target admission states from a
// finished dispatch result.
func admissionsFromSendOutput(output *SendOutput) []SendAdmission {
	if output == nil {
		return nil
	}
	successful := make(map[string]bool, len(output.Successful))
	for _, target := range output.Successful {
		successful[target] = true
	}
	failures := make(map[string]string, len(output.Failed))
	for _, failure := range output.Failed {
		failures[failure.Pane] = failure.Error
	}

	admissions := make([]SendAdmission, 0, len(output.Targets))
	for _, target := range output.Targets {
		if successful[target] {
			admissions = append(admissions, SendAdmission{Target: target, State: AdmissionSubmitted})
			continue
		}
		// Presence check, not message check: a failure recorded with an
		// empty error string is still a rejection, not "never attempted".
		if msg, failed := failures[target]; failed {
			admissions = append(admissions, SendAdmission{
				Target: target, State: AdmissionRejected, Error: msg,
			})
			continue
		}
		admissions = append(admissions, SendAdmission{Target: target, State: AdmissionNotAttempted})
	}
	return admissions
}

// unknownAdmissions marks every recorded target as outcome-unknown; used
// when an operation is observed in progress.
func unknownAdmissions(targets []string) []SendAdmission {
	admissions := make([]SendAdmission, 0, len(targets))
	for _, target := range targets {
		admissions = append(admissions, SendAdmission{Target: target, State: AdmissionUnknown})
	}
	return admissions
}

func sendOperationInfoFromRecord(op *state.SendOperation, replayed bool) *SendOperationInfo {
	if op == nil {
		return nil
	}
	info := &SendOperationInfo{
		OperationID:   op.OperationID,
		Status:        op.Status,
		Replayed:      replayed,
		PayloadSHA256: op.PayloadSHA256,
		PayloadBytes:  op.PayloadBytes,
		CreatedAt:     op.CreatedAt,
		CompletedAt:   op.CompletedAt,
	}
	if op.OutcomeJSON != "" {
		var outcome sendOperationOutcome
		if err := json.Unmarshal([]byte(op.OutcomeJSON), &outcome); err == nil {
			info.Admissions = outcome.Admissions
		}
	}
	return info
}

// applyReplayedOutcome restores a stored outcome onto a fresh SendOutput so
// an identical retry observes the original result without a second send.
func applyReplayedOutcome(output *SendOutput, op *state.SendOperation) error {
	var outcome sendOperationOutcome
	if err := json.Unmarshal([]byte(op.OutcomeJSON), &outcome); err != nil {
		return fmt.Errorf("decode stored send outcome: %w", err)
	}
	output.Success = outcome.Success
	output.Error = outcome.Error
	output.ErrorCode = outcome.ErrorCode
	output.SentAt = outcome.SentAt
	output.Targets = outcome.Targets
	output.Successful = outcome.Successful
	output.Failed = outcome.Failed
	if outcome.MessagePreview != "" {
		output.MessagePreview = outcome.MessagePreview
	}
	info := sendOperationInfoFromRecord(op, true)
	info.Admissions = outcome.Admissions
	output.Operation = info
	// A replayed failure is terminal for THIS operation ID: keystrokes may
	// already have landed, so the retry semantics the caller opted into
	// forbid a second delivery attempt under the same ID. Say so instead of
	// letting the caller retry the same command forever.
	if !outcome.Success && output.Hint == "" {
		output.Hint = "recorded outcome replayed without re-sending; use a new operation ID to attempt delivery again"
	}
	return nil
}

// releaseSendOperationClaim frees a claim when the operation terminated
// BEFORE any delivery was attempted (preflight failure). Nothing was typed
// into any pane, so a retry with the same operation ID must get a fresh
// attempt rather than a stored transient failure. Best-effort.
func releaseSendOperationClaim(store *state.Store, op *state.SendOperation, output *SendOutput) {
	if err := store.ReleaseSendOperation(op.OperationID, op.SessionName); err != nil {
		output.Warnings = append(output.Warnings,
			fmt.Sprintf("send operation %s claim not released: %v (retry may report in-progress until taken over)", op.OperationID, err))
	}
}

// completeSendOperationRecord persists the terminal outcome for a claimed
// operation and attaches the operation info to the output. Best-effort: a
// persistence failure surfaces as a warning rather than failing the send
// that already happened.
func completeSendOperationRecord(store *state.Store, op *state.SendOperation, output *SendOutput) {
	admissions := admissionsFromSendOutput(output)
	outcome := sendOperationOutcome{
		Success:        output.Success,
		SentAt:         output.SentAt,
		Targets:        output.Targets,
		Successful:     output.Successful,
		Failed:         output.Failed,
		Admissions:     admissions,
		Error:          output.Error,
		ErrorCode:      output.ErrorCode,
		MessagePreview: output.MessagePreview,
	}
	data, err := json.Marshal(outcome)
	if err != nil {
		output.Warnings = append(output.Warnings, fmt.Sprintf("send operation %s outcome not recorded: %v", op.OperationID, err))
		return
	}
	completedAt := time.Now().UTC()
	if err := store.CompleteSendOperation(op.OperationID, op.SessionName, string(data), completedAt); err != nil {
		output.Warnings = append(output.Warnings, fmt.Sprintf("send operation %s outcome not recorded: %v", op.OperationID, err))
		return
	}
	op.Status = state.SendOperationCompleted
	op.CompletedAt = &completedAt
	info := sendOperationInfoFromRecord(op, false)
	info.Admissions = admissions
	output.Operation = info
}

// SendReceiptOutput is the structured output for --robot-send-receipt.
type SendReceiptOutput struct {
	RobotResponse
	Session   string             `json:"session,omitempty"`
	Warnings  []string           `json:"warnings,omitempty"`
	Operation *SendOperationInfo `json:"operation,omitempty"`
	// Outcome carries the recorded terminal result for completed operations.
	Outcome *SendReceiptOutcome `json:"outcome,omitempty"`
}

// SendReceiptOutcome is the recorded terminal result of a completed send
// operation as exposed by receipt queries.
type SendReceiptOutcome struct {
	Success    bool        `json:"success"`
	SentAt     time.Time   `json:"sent_at"`
	Targets    []string    `json:"targets"`
	Successful []string    `json:"successful"`
	Failed     []SendError `json:"failed"`
	Error      string      `json:"error,omitempty"`
	ErrorCode  string      `json:"error_code,omitempty"`
}

// GetSendReceipt returns the durable receipt for an operation ID.
func GetSendReceipt(operationID string) (*SendReceiptOutput, error) {
	operationID = strings.TrimSpace(operationID)
	output := &SendReceiptOutput{RobotResponse: NewRobotResponse(true)}
	if operationID == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("operation ID is required"),
			ErrCodeInvalidFlag,
			"Pass the operation ID supplied to --robot-send --op-id",
		)
		return output, nil
	}

	store := currentProjectionStore()
	if store == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("send receipts require the runtime projection store"),
			ErrCodeNotImplemented,
			"The runtime state store is unavailable in this invocation",
		)
		return output, nil
	}

	ops, err := store.GetSendOperationsByID(operationID)
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Failed to read send operation record")
		return output, nil
	}
	if len(ops) == 0 {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("send operation '%s' not found", operationID),
			ErrCodeNotFound,
			"Unknown operation ID; receipts exist only for sends that supplied --op-id",
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
	output.Operation = sendOperationInfoFromRecord(op, false)
	if op.Status == state.SendOperationCompleted && op.OutcomeJSON != "" {
		var outcome sendOperationOutcome
		if err := json.Unmarshal([]byte(op.OutcomeJSON), &outcome); err == nil {
			output.Outcome = &SendReceiptOutcome{
				Success:    outcome.Success,
				SentAt:     outcome.SentAt,
				Targets:    outcome.Targets,
				Successful: outcome.Successful,
				Failed:     outcome.Failed,
				Error:      outcome.Error,
				ErrorCode:  outcome.ErrorCode,
			}
		}
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

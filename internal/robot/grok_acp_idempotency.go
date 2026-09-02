package robot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/grok"
	"github.com/Dicklesworthstone/ntm/internal/provider"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

const grokACPOperationScope = "provider:xai-acp"

// GrokACPOperationLedger is the durable, cross-process replay boundary. The
// existing send-operation table already provides an atomic unique claim and a
// safe JSON outcome slot; ACP deliberately never uses its stale-takeover path,
// because an abandoned provider operation may already have been accepted.
type GrokACPOperationLedger interface {
	ClaimSendOperation(*state.SendOperation) (*state.SendOperation, bool, error)
	ReleaseSendOperation(operationID, sessionName string) error
	CompleteSendOperation(operationID, sessionName, outcomeJSON string, completedAt time.Time) error
	GetSendOperation(operationID, sessionName string) (*state.SendOperation, error)
}

func grokACPBindingHash(identity provider.Identity, logicalPromptHash, cwd, binary, policyHash string) string {
	fields := []string{
		"ntm.grok-acp.binding.v1",
		identity.Hash(),
		logicalPromptHash,
		sha256String(strings.TrimSpace(cwd)),
		sha256String(strings.TrimSpace(binary)),
		policyHash,
	}
	return sha256String(strings.Join(fields, "\x00"))
}

func sha256String(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func validGrokACPOperationID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		switch r {
		case '-', '_', '.', ':':
			continue
		default:
			return false
		}
	}
	return true
}

func claimGrokACPOperation(ledger GrokACPOperationLedger, operationID, bindingHash, promptHash string, promptBytes int64, now time.Time) (*state.SendOperation, bool, error) {
	if ledger == nil {
		return nil, false, errors.New("durable Grok ACP operation ledger is unavailable")
	}
	return ledger.ClaimSendOperation(&state.SendOperation{
		OperationID:   operationID,
		SessionName:   grokACPOperationScope,
		BindingHash:   bindingHash,
		PayloadSHA256: promptHash,
		PayloadBytes:  promptBytes,
		CreatedAt:     now,
	})
}

func applyStoredGrokACPOutcome(output *GrokACPOperationOutput, stored *state.SendOperation) error {
	if stored == nil || stored.Status != state.SendOperationCompleted || strings.TrimSpace(stored.OutcomeJSON) == "" {
		return errors.New("durable Grok ACP outcome is unavailable")
	}
	var replay GrokACPOperationOutput
	if err := json.Unmarshal([]byte(stored.OutcomeJSON), &replay); err != nil {
		return fmt.Errorf("decode durable Grok ACP outcome: %w", err)
	}
	*output = replay
	output.Replayed = true
	output.ReceiptState = "replayed"
	return nil
}

func completeGrokACPOperation(ledger GrokACPOperationLedger, claimed *state.SendOperation, output *GrokACPOperationOutput, now time.Time) error {
	if ledger == nil || claimed == nil || output == nil {
		return errors.New("durable Grok ACP completion ledger is unavailable")
	}
	output.ReceiptState = "completed"
	data, err := json.Marshal(output)
	if err != nil {
		return fmt.Errorf("encode durable Grok ACP outcome: %w", err)
	}
	if err := ledger.CompleteSendOperation(claimed.OperationID, claimed.SessionName, string(data), now); err != nil {
		return fmt.Errorf("persist durable Grok ACP outcome: %w", err)
	}
	return nil
}

func applyGrokACPLedgerFailure(output *GrokACPOperationOutput) error {
	err := errors.New("Grok ACP may have executed, but its durable outcome receipt was not recorded")
	output.RobotResponse = NewErrorResponse(err, ErrCodeDispatchUnknown, "Do not replay this operation ID; inspect or reconcile the provider session, then use a new operation ID")
	output.State = grok.StateOutcomeUnknown
	output.FailureCode = grok.ErrOutcomeUnknown
	output.ReceiptState = "persistence_failed"
	return &grok.Error{Code: grok.ErrOutcomeUnknown, Err: err}
}

func replayedGrokACPError(output *GrokACPOperationOutput) error {
	if output == nil || output.Success {
		return nil
	}
	code := output.FailureCode
	if code == "" {
		code = grok.ErrOutcomeUnknown
	}
	return &grok.Error{Code: code, Err: errors.New("recorded Grok ACP outcome replayed without provider dispatch")}
}

// GetGrokACPOperationReceipt reads the durable ACP outcome without provider
// dispatch. An in-progress row remains outcome-unknown forever until explicit
// reconciliation; ACP intentionally has no stale automatic takeover.
func GetGrokACPOperationReceipt(operationID string, ledger GrokACPOperationLedger) (*GrokACPOperationOutput, error) {
	operationID = strings.TrimSpace(operationID)
	if !validGrokACPOperationID(operationID) {
		return nil, errors.New("a valid Grok ACP operation ID is required")
	}
	if ledger == nil {
		return nil, errors.New("durable Grok ACP operation ledger is unavailable")
	}
	stored, err := ledger.GetSendOperation(operationID, grokACPOperationScope)
	if err != nil {
		return nil, fmt.Errorf("read durable Grok ACP receipt: %w", err)
	}
	if stored == nil {
		return nil, fmt.Errorf("Grok ACP operation %q was not found", operationID)
	}
	if stored.Status != state.SendOperationCompleted {
		err := errors.New("Grok ACP operation remains in progress or has an unrecorded outcome")
		return &GrokACPOperationOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeOperationInProgress, "Do not redispatch; reconcile the provider session and use a new operation ID if another attempt is required"),
			OperationID:   operationID,
			Provider:      grokACPProvider,
			Transport:     grokACPTransport,
			Target:        grokACPTarget,
			PromptSHA256:  stored.PayloadSHA256,
			BindingSHA256: stored.BindingHash,
			ReceiptState:  "in_progress",
			State:         grok.StateOutcomeUnknown,
			FailureCode:   grok.ErrOutcomeUnknown,
			CleanupState:  "not_observed",
			StartedAt:     stored.CreatedAt,
		}, nil
	}
	var output GrokACPOperationOutput
	if err := json.Unmarshal([]byte(stored.OutcomeJSON), &output); err != nil {
		return nil, fmt.Errorf("decode durable Grok ACP receipt: %w", err)
	}
	output.ReceiptState = "queried"
	output.Replayed = false
	return &output, nil
}

func PrintGrokACPOperationReceipt(operationID string) error {
	output, err := GetGrokACPOperationReceipt(operationID, currentProjectionStore())
	if err != nil {
		return EncodeErrorJSON(err, ErrCodeInvalidArgs, "Use an existing --op-id from --robot-grok-acp-run", "robot-grok-acp-receipt")
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "Grok ACP receipt query failed")
}

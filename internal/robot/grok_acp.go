package robot

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/grok"
	"github.com/Dicklesworthstone/ntm/internal/provider"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

// GrokACPOperationOptions is the deliberately narrow input for one direct
// Grok ACP operation. Prompt and nonce are accepted only as input; neither is
// returned in the robot receipt.
type GrokACPOperationOptions struct {
	Prompt      string
	CWD         string
	Binary      string
	Model       string
	OperationID string
	Nonce       string
	Identity    provider.Identity
}

// GrokACPTokenUsage and GrokACPCost are optional provider metadata. The ACP
// engine does not invent them; an adapter may attach them only from a
// structured provider completion record.
type GrokACPTokenUsage struct {
	Input  *int64 `json:"input,omitempty"`
	Output *int64 `json:"output,omitempty"`
	Total  *int64 `json:"total,omitempty"`
}

type GrokACPCost struct {
	Currency string `json:"currency"`
	Micro    int64  `json:"micro"`
}

// GrokACPExecutionEvidence is intentionally safe to emit. It carries no
// provider text, prompt, credentials, or raw tool calls.
type GrokACPExecutionEvidence struct {
	TokenUsage   *GrokACPTokenUsage
	Cost         *GrokACPCost
	ExitCode     *int
	CleanupState string
}

// GrokACPOperationOutput is the robot-safe receipt for direct ACP execution.
// It embeds the standard envelope and exposes only hashes, counts, and
// completion metadata.
type GrokACPOperationOutput struct {
	RobotResponse
	OperationID            string `json:"operation_id"`
	Provider               string `json:"provider"`
	ProviderIdentitySHA256 string `json:"provider_identity_sha256"`
	// ProviderIdentityEvidence describes the complete tuple. Structured ACP
	// observations can prove session/model facts, but endpoint/config remain
	// profile-attested unless an adapter records separate runtime proof.
	ProviderIdentityEvidence provider.IdentityEvidenceGrade `json:"provider_identity_evidence"`
	Model                    string                         `json:"model,omitempty"`
	ModelEvidence            string                         `json:"model_evidence,omitempty"`
	Transport                string                         `json:"transport"`
	Target                   string                         `json:"target"`
	PromptSHA256             string                         `json:"prompt_sha256"`
	NonceSHA256              string                         `json:"nonce_sha256"`
	ToolDigest               string                         `json:"tool_digest,omitempty"`
	ProviderSessionID        string                         `json:"provider_session_id,omitempty"`
	StopReason               string                         `json:"stop_reason,omitempty"`
	CompletionConfirmed      bool                           `json:"completion_confirmed"`
	AcknowledgementVerified  bool                           `json:"acknowledgement_verified"`
	AssistantTextChunks      int                            `json:"assistant_text_chunks"`
	AssistantTextBytes       int64                          `json:"assistant_text_bytes"`
	OutputSHA256             string                         `json:"output_sha256,omitempty"`
	ToolEventCount           int                            `json:"tool_event_count"`
	ToolEventsSHA256         string                         `json:"tool_events_sha256"`
	TokenUsage               *GrokACPTokenUsage             `json:"token_usage,omitempty"`
	Cost                     *GrokACPCost                   `json:"cost,omitempty"`
	ExitCode                 *int                           `json:"exit_code,omitempty"`
	CleanupState             string                         `json:"cleanup_state"`
	State                    string                         `json:"state"`
	FailureCode              grok.ErrorCode                 `json:"failure_code,omitempty"`
	Admission                AdmissionEvidence              `json:"admission"`
	StartedAt                time.Time                      `json:"started_at"`
	CompletedAt              time.Time                      `json:"completed_at"`
	BindingSHA256            string                         `json:"binding_sha256"`
	ReceiptState             string                         `json:"receipt_state"`
	Replayed                 bool                           `json:"replayed"`
}

// GrokACPEngine is the injectable direct-provider boundary. It deliberately
// returns the redaction-safe grok.Result rather than text.
type GrokACPEngine interface {
	Run(context.Context, grok.Request) (grok.Result, error)
}

type grokACPEngineFunc func(context.Context, grok.Request) (grok.Result, error)

func (f grokACPEngineFunc) Run(ctx context.Context, req grok.Request) (grok.Result, error) {
	return f(ctx, req)
}

type nativeGrokACPEngine struct{}

func (nativeGrokACPEngine) Run(ctx context.Context, req grok.Request) (grok.Result, error) {
	return grok.Run(ctx, grok.OSRunner{}, req)
}

// GrokACPEvidenceProvider is optional because xAI completion metadata may not
// contain usage/cost or local process cleanup details on every CLI version.
// Omission is represented explicitly as "not_observed", never guessed.
type GrokACPEvidenceProvider interface {
	Evidence(grok.Result) GrokACPExecutionEvidence
}

// GrokACPOperationDeps makes the adapter fully offline-testable.
type GrokACPOperationDeps struct {
	Engine    GrokACPEngine
	Evidence  GrokACPEvidenceProvider
	Admission ProviderAdmission
	Random    io.Reader
	Now       func() time.Time
	Ledger    GrokACPOperationLedger
}

// ProviderAdmission is the exact-identity dispatch boundary. It exposes no
// target-selection or fallback method, so a denial cannot silently switch
// provider, account, model, endpoint, runtime, or config.
type ProviderAdmission interface {
	Acquire(provider.Identity) ratelimit.Decision
	Release(provider.Identity)
	RecordResult(provider.Identity, ratelimit.ErrorClass, time.Duration) ratelimit.Decision
	RecordSuccess(provider.Identity)
}

type AdmissionEvidence struct {
	Allowed    bool                 `json:"allowed"`
	Reason     ratelimit.ErrorClass `json:"reason,omitempty"`
	RetryAt    *time.Time           `json:"retry_at,omitempty"`
	NoFailover bool                 `json:"no_failover"`
	// CapacityControlScope prevents a receipt from implying a shared fleet
	// quota. The built-in controller is intentionally process-local.
	CapacityControlScope provider.CapacityControlScope `json:"capacity_control_scope"`
}

const (
	grokACPProvider  = "xai"
	grokACPTransport = "acp_stdio"
	grokACPTarget    = "stdio"
)

// RunGrokACPOperation binds an operation ID and nonce to one ACP prompt. It
// never claims an acknowledgement from a mere local completion: the engine
// must have observed the exact nonce in an assistant text chunk.
func RunGrokACPOperation(ctx context.Context, opts GrokACPOperationOptions, deps GrokACPOperationDeps) (output *GrokACPOperationOutput, returnErr error) {
	if deps.Engine == nil {
		deps.Engine = nativeGrokACPEngine{}
	}
	if deps.Random == nil {
		deps.Random = rand.Reader
	}
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now().UTC() }
	}
	if deps.Admission == nil {
		deps.Admission = ratelimit.DefaultAdmissionController()
	}

	operationID, err := nonEmptyOrGenerated(opts.OperationID, "grok-acp-", deps.Random)
	if err != nil {
		return invalidGrokACPOperationOutput(opts, deps.Now(), err), err
	}
	if !validGrokACPOperationID(operationID) {
		err := errors.New("operation ID must be 1-128 characters using letters, digits, dot, colon, underscore, or hyphen")
		return invalidGrokACPOperationOutput(opts, deps.Now(), err), err
	}
	nonce, err := nonEmptyOrGenerated(opts.Nonce, "NTM_ACK_", deps.Random)
	if err != nil {
		return invalidGrokACPOperationOutput(opts, deps.Now(), err), err
	}
	if !grok.ValidNonce(nonce) {
		err := errors.New("nonce must be an NTM_ACK_ token with at least 128 bits of hex entropy")
		return invalidGrokACPOperationOutput(opts, deps.Now(), err), err
	}
	if strings.TrimSpace(opts.Prompt) == "" {
		err := errors.New("prompt is required")
		return invalidGrokACPOperationOutput(opts, deps.Now(), err), err
	}

	logicalPromptHash := sha256Hex(strings.TrimSpace(opts.Prompt))
	transmittedPrompt := bindNonceInstruction(opts.Prompt, nonce)
	promptHash := sha256Hex(transmittedPrompt)
	// The public robot operation is permanently bound to the compiled named
	// policy. Callers cannot expand permissions while presenting its digest.
	policyArgs := agentpkg.DefaultGrokAutomationACPPolicyArgs()
	toolDigest := agentpkg.DefaultGrokAutomationPolicySHA256()
	output = &GrokACPOperationOutput{
		RobotResponse:            NewRobotResponse(true),
		OperationID:              operationID,
		Provider:                 grokACPProvider,
		ProviderIdentitySHA256:   opts.Identity.Hash(),
		ProviderIdentityEvidence: opts.Identity.EvidenceGrade(),
		// A requested model is not receipt evidence. Model stays empty until
		// Grok returns structured completion metadata naming the effective one.
		Model:        "",
		Transport:    grokACPTransport,
		Target:       grokACPTarget,
		PromptSHA256: promptHash,
		NonceSHA256:  sha256Hex(nonce),
		ToolDigest:   toolDigest,
		CleanupState: "not_observed",
		State:        grok.StateFailed,
		ReceiptState: "not_claimed",
		StartedAt:    deps.Now(),
	}
	if !opts.Identity.Valid() || opts.Identity.Provider() != grokACPProvider {
		err := errors.New("a valid xAI provider identity is required for Grok ACP admission")
		output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Select an exact xAI provider profile")
		output.State = "admission_rejected"
		output.Admission = AdmissionEvidence{Reason: ratelimit.ErrorIdentityMismatch, NoFailover: true, CapacityControlScope: provider.CapacityControlScopeProcessLocal}
		return output, err
	}
	if requestedModel := strings.TrimSpace(opts.Model); requestedModel != "" && requestedModel != opts.Identity.Model() {
		err := errors.New("requested Grok model does not match the selected provider identity")
		output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Use the model bound by the exact provider profile")
		output.State = "identity_rejected"
		output.FailureCode = grok.ErrIdentityMismatch
		output.Admission = AdmissionEvidence{Reason: ratelimit.ErrorIdentityMismatch, NoFailover: true, CapacityControlScope: provider.CapacityControlScopeProcessLocal}
		return output, err
	}
	// The durable operation binds the caller's logical work, not the generated
	// per-dispatch transport nonce. This lets an ordinary retry with the same
	// operation ID replay the stored receipt without retaining the nonce or
	// dispatching again. PromptSHA256 still records the exact nonce-bound packet.
	output.BindingSHA256 = grokACPBindingHash(opts.Identity, logicalPromptHash, opts.CWD, opts.Binary, toolDigest)
	claimed, wonClaim, claimErr := claimGrokACPOperation(deps.Ledger, operationID, output.BindingSHA256, promptHash, int64(len(transmittedPrompt)), output.StartedAt)
	if claimErr != nil {
		output.RobotResponse = NewErrorResponse(errors.New("durable Grok ACP operation claim failed"), ErrCodeInternalError, "Repair the NTM state store before provider dispatch")
		output.ReceiptState = "claim_failed"
		return output, claimErr
	}
	if !wonClaim {
		if claimed.BindingHash != output.BindingSHA256 {
			err := errors.New("operation ID is already bound to a different Grok ACP request")
			output.RobotResponse = NewErrorResponse(err, ErrCodeIdempotencyConflict, "Use a new operation ID for different provider identity, prompt, path, executable, or policy")
			output.ReceiptState = "conflict"
			return output, err
		}
		if claimed.Status == state.SendOperationCompleted {
			if err := applyStoredGrokACPOutcome(output, claimed); err != nil {
				output.RobotResponse = NewErrorResponse(errors.New("stored Grok ACP receipt is unreadable"), ErrCodeDispatchUnknown, "Do not redispatch; repair or reconcile the durable receipt")
				output.State = grok.StateOutcomeUnknown
				output.ReceiptState = "corrupt"
				return output, err
			}
			return output, replayedGrokACPError(output)
		}
		err := errors.New("Grok ACP operation is already in progress or has an unrecorded outcome")
		output.RobotResponse = NewErrorResponse(err, ErrCodeOperationInProgress, "Do not take over or replay; reconcile the original provider operation and use a new operation ID if needed")
		output.State = grok.StateOutcomeUnknown
		output.ReceiptState = "in_progress"
		return output, err
	}
	output.ReceiptState = "claimed"
	decision := deps.Admission.Acquire(opts.Identity)
	output.Admission = admissionEvidence(decision)
	if !decision.Allowed {
		err := fmt.Errorf("provider admission denied for identity %s: %s", opts.Identity.Hash(), decision.Reason)
		output.RobotResponse = NewErrorResponse(err, ErrCodeResourceBusy, "Honor retry_at or remediate the exact provider profile; failover is prohibited")
		output.State = "admission_denied"
		if releaseErr := deps.Ledger.ReleaseSendOperation(claimed.OperationID, claimed.SessionName); releaseErr != nil {
			output.ReceiptState = "release_failed"
		}
		return output, err
	}
	defer deps.Admission.Release(opts.Identity)
	dispatched := true
	defer func() {
		if !dispatched {
			return
		}
		if err := completeGrokACPOperation(deps.Ledger, claimed, output, deps.Now()); err != nil {
			returnErr = applyGrokACPLedgerFailure(output)
		}
	}()

	result, runErr := deps.Engine.Run(ctx, grok.Request{
		Prompt:               transmittedPrompt,
		ExpectedNonce:        nonce,
		CWD:                  opts.CWD,
		Binary:               opts.Binary,
		Model:                opts.Model,
		AutomationPolicyArgs: policyArgs,
	})
	applyGrokACPResult(output, result, deps.Evidence)
	if runErr != nil {
		// Only exact provider/account conditions belong in the provider circuit.
		// Local launch, protocol, timeout, and outcome-unknown failures are not
		// evidence that this account/model is rate limited or permanently bad.
		if class, record := classifyGrokAdmissionError(runErr); record {
			deps.Admission.RecordResult(opts.Identity, class, 0)
		}
		applyGrokACPError(output, runErr)
		return output, runErr
	}
	if !result.CompletionConfirmed || strings.TrimSpace(result.StopReason) == "" {
		output.RobotResponse = NewErrorResponse(errors.New("Grok ACP completed without authoritative terminal metadata"), ErrCodeDispatchUnknown, "Inspect the provider session before retrying")
		output.State = grok.StateOutcomeUnknown
		output.FailureCode = grok.ErrOutcomeUnknown
		return output, &grok.Error{Code: grok.ErrOutcomeUnknown, Err: errors.New("completion or stop reason was not confirmed")}
	}
	if !output.AcknowledgementVerified {
		output.RobotResponse = NewErrorResponse(errors.New("Grok ACP completion did not include the required nonce acknowledgement"), string(grok.ErrAcknowledgementUnconfirmed), "Inspect the provider session; do not treat completion alone as acknowledged delivery")
		output.State = "acknowledgement_unconfirmed"
		output.FailureCode = grok.ErrAcknowledgementUnconfirmed
		return output, &grok.Error{Code: grok.ErrAcknowledgementUnconfirmed, Err: errors.New("required nonce acknowledgement was not observed")}
	}
	if strings.TrimSpace(result.Model) == "" || strings.TrimSpace(result.Model) != opts.Identity.Model() || !grokModelIdentityEvidenceConfirmed(result.ModelEvidence) {
		err := errors.New("Grok ACP completion did not provide session-scoped exact model identity evidence")
		output.Model = ""
		output.ModelEvidence = ""
		output.RobotResponse = NewErrorResponse(err, ErrCodeDispatchUnknown, "Inspect the exact xAI provider session and profile; provider failover is prohibited")
		output.State = "identity_unconfirmed"
		output.FailureCode = grok.ErrIdentityMismatch
		return output, &grok.Error{Code: grok.ErrIdentityMismatch, Err: err}
	}
	// Do not clear transient backoff until the structured completion, nonce,
	// and exact model identity have all been verified.
	deps.Admission.RecordSuccess(opts.Identity)
	output.RobotResponse = NewRobotResponse(true)
	output.State = grok.StateCompleted
	return output, nil
}

// grokModelIdentityEvidenceConfirmed deliberately excludes a provider catalog:
// a catalog proves only availability, never which model served this session.
func grokModelIdentityEvidenceConfirmed(evidence string) bool {
	switch strings.TrimSpace(evidence) {
	case "completion_metadata", "provider_session_notification_plus_exact_launch":
		return true
	default:
		return false
	}
}

func admissionEvidence(decision ratelimit.Decision) AdmissionEvidence {
	return AdmissionEvidence{Allowed: decision.Allowed, Reason: decision.Reason, RetryAt: decision.RetryAt, NoFailover: decision.NoFailover, CapacityControlScope: provider.CapacityControlScopeProcessLocal}
}

func classifyGrokAdmissionError(err error) (ratelimit.ErrorClass, bool) {
	var typed *grok.Error
	if !errors.As(err, &typed) {
		return ratelimit.ErrorUnknown, false
	}
	switch typed.Code {
	case grok.ErrAuthRequired, grok.ErrAuthFailed:
		return ratelimit.ErrorAuthentication, true
	default:
		return ratelimit.ErrorUnknown, false
	}
}

// PrintGrokACPOperation executes the production ACP adapter and emits exactly
// one robot envelope. Provider failures remain visible in the typed receipt
// while the returned process error preserves the robot exit contract.
func PrintGrokACPOperation(ctx context.Context, opts GrokACPOperationOptions) error {
	output, _ := RunGrokACPOperation(ctx, opts, GrokACPOperationDeps{Ledger: currentProjectionStore()})
	if output == nil {
		return EncodeErrorJSON(errors.New("Grok ACP produced no receipt"), ErrCodeInternalError, "Inspect local Grok installation", "robot-grok-acp-run")
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "Grok ACP operation failed")
}

func applyGrokACPResult(output *GrokACPOperationOutput, result grok.Result, evidence GrokACPEvidenceProvider) {
	if output == nil {
		return
	}
	output.ProviderSessionID = result.ProviderSessionID
	output.StopReason = result.StopReason
	output.CompletionConfirmed = result.CompletionConfirmed
	// Completion alone is insufficient. This is true only after grok's bounded
	// matcher observed the exact transmitted nonce across assistant chunks.
	output.AcknowledgementVerified = result.CompletionConfirmed && result.AcknowledgementVerified
	output.AssistantTextChunks = result.AssistantTextChunks
	output.AssistantTextBytes = result.AssistantTextBytes
	output.OutputSHA256 = result.OutputSHA256
	output.ToolEventCount = result.ToolEventCount
	output.ToolEventsSHA256 = result.ToolEventsSHA256
	if strings.TrimSpace(result.Model) != "" {
		output.Model = strings.TrimSpace(result.Model)
		output.ModelEvidence = strings.TrimSpace(result.ModelEvidence)
	}
	output.TokenUsage = grokUsageToReceipt(result.Usage)
	output.ExitCode = cloneGrokACPExitCode(result.ExitCode)
	if strings.TrimSpace(result.CleanupState) != "" {
		output.CleanupState = strings.TrimSpace(result.CleanupState)
	}
	output.State = result.State
	output.FailureCode = result.FailureCode
	output.StartedAt = result.StartedAt
	output.CompletedAt = result.CompletedAt
	if evidence == nil {
		return
	}
	meta := evidence.Evidence(result)
	if output.TokenUsage == nil {
		output.TokenUsage = cloneGrokACPTokenUsage(meta.TokenUsage)
	}
	output.Cost = cloneGrokACPCost(meta.Cost)
	if output.ExitCode == nil {
		output.ExitCode = cloneGrokACPExitCode(meta.ExitCode)
	}
	if strings.TrimSpace(meta.CleanupState) != "" {
		output.CleanupState = strings.TrimSpace(meta.CleanupState)
	}
}

func applyGrokACPError(output *GrokACPOperationOutput, err error) {
	if output == nil {
		return
	}
	var typed *grok.Error
	if errors.As(err, &typed) {
		output.FailureCode = typed.Code
		switch typed.Code {
		case grok.ErrOutcomeUnknown:
			output.State = grok.StateOutcomeUnknown
			output.RobotResponse = NewErrorResponse(errors.New("Grok ACP outcome is unknown"), ErrCodeDispatchUnknown, "Inspect the provider session before retrying; do not blindly replay the operation")
			return
		case grok.ErrTimeout:
			output.State = grok.StateAbortedSafe
			output.RobotResponse = NewErrorResponse(errors.New("Grok ACP timed out before prompt acceptance"), ErrCodeTimeout, "Retry with the same operation intent only after confirming no provider session was created")
			return
		case grok.ErrAcknowledgementUnconfirmed:
			output.State = "acknowledgement_unconfirmed"
			output.RobotResponse = NewErrorResponse(errors.New("Grok ACP completion did not include the required nonce acknowledgement"), string(grok.ErrAcknowledgementUnconfirmed), "Inspect the provider session; do not treat completion alone as acknowledged delivery")
			return
		}
	}
	output.RobotResponse = NewErrorResponse(errors.New("Grok ACP operation failed"), ErrCodeInternalError, "Inspect the typed failure code; prompts, provider output, and credentials are intentionally not included")
}

func invalidGrokACPOperationOutput(opts GrokACPOperationOptions, now time.Time, err error) *GrokACPOperationOutput {
	return &GrokACPOperationOutput{
		RobotResponse: NewErrorResponse(errors.New("invalid Grok ACP operation"), ErrCodeInvalidArgs, "Provide a non-empty prompt and valid operation identity"),
		Provider:      grokACPProvider,
		Model:         "",
		Transport:     grokACPTransport,
		Target:        grokACPTarget,
		CleanupState:  "not_started",
		State:         grok.StateFailed,
		FailureCode:   grok.ErrInvalidRequest,
		StartedAt:     now,
		CompletedAt:   now,
	}
}

func bindNonceInstruction(prompt, nonce string) string {
	return strings.TrimSpace(prompt) + "\n\nAutomation acknowledgement: after completing the task, reply with this exact token and nothing else on one line: " + nonce
}

func nonEmptyOrGenerated(value, prefix string, random io.Reader) (string, error) {
	value = strings.TrimSpace(value)
	if value != "" {
		return value, nil
	}
	bytes := make([]byte, 16)
	if _, err := io.ReadFull(random, bytes); err != nil {
		return "", fmt.Errorf("generate Grok ACP identity: %w", err)
	}
	return prefix + hex.EncodeToString(bytes), nil
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func cloneGrokACPTokenUsage(value *GrokACPTokenUsage) *GrokACPTokenUsage {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func grokUsageToReceipt(value *grok.Usage) *GrokACPTokenUsage {
	if value == nil {
		return nil
	}
	return &GrokACPTokenUsage{
		Input:  cloneInt64(value.InputTokens),
		Output: cloneInt64(value.OutputTokens),
		Total:  cloneInt64(value.TotalTokens),
	}
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneGrokACPCost(value *GrokACPCost) *GrokACPCost {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneGrokACPExitCode(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

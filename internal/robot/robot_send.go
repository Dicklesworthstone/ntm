package robot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/audit"
	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	ntmevents "github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// SendOutput is the structured output for --robot-send
type SendOutput struct {
	RobotResponse                        // Embed standard response fields (success, timestamp, error)
	Session         string               `json:"session"`
	SentAt          time.Time            `json:"sent_at"`
	Blocked         bool                 `json:"blocked"`
	Redaction       RedactionSummary     `json:"redaction"`
	Warnings        []string             `json:"warnings"`
	Targets         []string             `json:"targets"`
	Successful      []string             `json:"successful"`
	Failed          []SendError          `json:"failed"`
	MessagePreview  string               `json:"message_preview"`
	DryRun          bool                 `json:"dry_run,omitempty"`
	WouldSendTo     []string             `json:"would_send_to,omitempty"`
	CASSInjection   *CASSInjectionInfo   `json:"cass_injection,omitempty"`
	MemoryInjection *CMInjectionInfo     `json:"memory_injection,omitempty"`
	AgentHints      *SendAgentHints      `json:"_agent_hints,omitempty"`
	RenderEvidence  []SendRenderEvidence `json:"render_evidence,omitempty"`

	// Operation is the durable idempotent-operation receipt (#245), present
	// when the caller supplied an operation ID (--op-id / Idempotency-Key).
	Operation *OperationInfo `json:"operation,omitempty"`
}

// SendRenderEvidence records bounded before/after terminal observations for one
// requested send. It deliberately excludes pane contents so verification does
// not add a new prompt or scrollback disclosure surface.
type SendRenderEvidence struct {
	Pane                 string `json:"pane"`
	Delivered            bool   `json:"delivered"`
	DeliveredAndRendered bool   `json:"delivered_and_rendered"`
	BaselineAvailable    bool   `json:"baseline_available"`
	RenderAvailable      bool   `json:"render_available"`
	RenderChanged        bool   `json:"render_changed"`
	CaptureError         string `json:"capture_error,omitempty"`
}

// RedactionSummary is a safe-to-print summary of redaction findings.
// It intentionally does NOT include the matched secret values.
type RedactionSummary struct {
	Mode       string         `json:"mode"`
	Findings   int            `json:"findings"`
	Categories map[string]int `json:"categories,omitempty"`
	Action     string         `json:"action,omitempty"` // off|warn|redact|block
}

// CASSInjectionInfo reports CASS context injection details in robot responses.
type CASSInjectionInfo struct {
	// Enabled indicates whether CASS injection was enabled.
	Enabled bool `json:"enabled"`
	// Query is the search query that was executed.
	Query string `json:"query,omitempty"`
	// ItemsFound is how many CASS hits were found.
	ItemsFound int `json:"items_found"`
	// ItemsInjected is how many items were actually injected.
	ItemsInjected int `json:"items_injected"`
	// TokensAdded is the estimated token count of injected content.
	TokensAdded int `json:"tokens_added"`
	// Sources lists the sessions that provided context.
	Sources []CASSSource `json:"sources,omitempty"`
	// SkippedReason explains why injection was skipped, if applicable.
	SkippedReason string `json:"skipped_reason,omitempty"`
}

// CASSSource represents a session that provided CASS context.
type CASSSource struct {
	// Session is the session name or path.
	Session string `json:"session"`
	// Relevance is the relevance score (0-100).
	Relevance int `json:"relevance"`
	// AgeDays is how many days old the session is.
	AgeDays int `json:"age_days"`
}

// NewCASSInjectionInfo creates CASSInjectionInfo from an InjectionResult.
func NewCASSInjectionInfo(result InjectionResult, query string, hits []ScoredHit) *CASSInjectionInfo {
	info := &CASSInjectionInfo{
		Enabled:       result.Metadata.Enabled,
		Query:         query,
		ItemsFound:    result.Metadata.ItemsFound,
		ItemsInjected: result.Metadata.ItemsInjected,
		TokensAdded:   result.Metadata.TokensAdded,
		SkippedReason: result.Metadata.SkippedReason,
		Sources:       make([]CASSSource, 0, len(hits)),
	}
	// Degraded path (bd-ws2-wire-or-delete-ykmcz.11): a failed query (e.g.
	// cass not installed) must leave a recorded skip in the envelope rather
	// than a silent empty block — injection is enrichment, never a gate.
	if !result.Success && info.SkippedReason == "" && result.Error != "" {
		info.SkippedReason = result.Error
	}

	now := time.Now()
	for _, hit := range hits {
		sessionDate := extractSessionDate(hit.SourcePath)
		ageDays := 0
		if !sessionDate.IsZero() {
			ageDays = int(now.Sub(sessionDate).Hours() / 24)
		}

		info.Sources = append(info.Sources, CASSSource{
			Session:   extractSessionName(hit.SourcePath),
			Relevance: int(hit.ComputedScore * 100),
			AgeDays:   ageDays,
		})
	}

	return info
}

// SendAgentHints provides agent guidance specific to send output
type SendAgentHints struct {
	Summary     string   `json:"summary,omitempty"`     // One-line summary of what happened
	Suggestions []string `json:"suggestions,omitempty"` // Actionable next steps
}

// SendError represents a failed send attempt
type SendError struct {
	Pane  string `json:"pane"`
	Error string `json:"error"`
}

// SendOptions configures the PrintSend operation
type SendOptions struct {
	Context        context.Context `json:"-"` // Optional caller cancellation; nil uses a background context
	Session        string          // Target session name
	Message        string          // Message to send
	Redaction      redaction.Config
	All            bool     // Send to all panes (including user)
	Pane           string   // Singular N, W.P, or %N selector; mutually exclusive with Panes
	Panes          []string // Specific pane indices (e.g., "0", "1", "2")
	AgentTypes     []string // Filter by agent types (e.g., "claude", "codex")
	Exclude        []string // Panes to exclude
	DelayMs        int      // Delay between sends in milliseconds
	DryRun         bool     // If true, show what would be sent without actually sending
	ClearInput     bool     // Clear residual composer text (verified) before typing (ntm-5p0b)
	Enter          *bool    // If set, override Enter behavior after paste
	VerifyRender   bool     // Capture bounded before/after pane output and require rendered evidence
	RequestID      string   // External request identifier for REST parity
	CorrelationID  string   // Correlation identifier for tracing request/outcome/verification
	IdempotencyKey string   // Idempotency key when provided by an upstream caller
	// MessageGuard checks the final payload against all resolved physical
	// targets before operation admission, then each target immediately before
	// its input. It is request-local policy,
	// never part of the caller's durable command identity or serialized options.
	MessageGuard func(context.Context, string, []tmux.Pane) error `json:"-"`
	Store        *state.Store                                     `json:"-"`
	// ExplicitStore binds durable idempotency to Store (even when nil) rather
	// than the process-global projection store.
	ExplicitStore bool `json:"-"`

	// CASS injection options
	WithCASS     bool          // Enable CASS context injection
	CASSConfig   *CASSConfig   // CASS query configuration (optional)
	FilterConfig *FilterConfig // CASS filter configuration (optional)
	InjectConfig *InjectConfig // CASS injection configuration (optional)

	// CM memory injection options (--with-memory, bd-3j6hm). Mirrors the
	// CASS toggle: WithMemory drives injection per call; MemoryInject
	// carries config-derived defaults (nil means compiled-in defaults).
	WithMemory   bool
	MemoryInject *CMInjectConfig

	// HistorySource is the prompt-history source a dispatched send is
	// recorded under; empty records history.SourceRobot. --robot-replay sets
	// history.SourceReplay.
	HistorySource history.Source
}

// recordRobotSendHistory appends a dispatched robot send to the prompt
// history that ntm send, the palette and replay already write. Without it,
// orchestrator sends — most sends in a swarm — were absent from
// --robot-history and --robot-replay, and --robot-metrics could not count the
// prompts an agent received. Targets are the canonical pane keys the send
// resolved; Success follows ntm send's rule (every target delivered), so a
// partially failed send is kept with its error and is not attributed to any
// pane as a received prompt.
//
// The caller's message is stored, not the CASS/CM-augmented payload: the
// injected context is derived per send, and replay re-sends what the caller
// asked for. When this send runs under a redaction mode, the stored prompt is
// redacted under it (warn and block included, as for every persisted prompt);
// history.Append then applies the configured history redaction on top.
func recordRobotSendHistory(opts SendOptions, redactCfg redaction.Config, targetPanes []tmux.Pane, targetKeys []string, output SendOutput, startedAt time.Time) {
	if len(targetKeys) == 0 {
		return
	}
	prompt := opts.Message
	if redactCfg.Mode != "" && redactCfg.Mode != redaction.ModeOff {
		persistCfg := redactCfg
		persistCfg.Mode = redaction.ModeRedact
		prompt = redaction.ScanAndRedact(prompt, persistCfg).Output
	}
	source := opts.HistorySource
	if source == "" {
		source = history.SourceRobot
	}
	entry := history.NewEntry(opts.Session, append([]string(nil), targetKeys...), prompt, source)
	agentTypes := make([]string, 0, len(targetPanes))
	for _, pane := range targetPanes {
		agentTypes = append(agentTypes, pane.Type.String())
	}
	entry.SetAgentTypes(agentTypes)
	entry.DurationMs = int(time.Since(startedAt) / time.Millisecond)
	switch {
	case len(output.Failed) == 0 && len(output.Successful) == len(targetKeys):
		entry.SetSuccess()
	case len(output.Failed) > 0:
		entry.SetError(fmt.Errorf("%d of %d target(s) failed: %s", len(output.Failed), len(targetKeys), output.Failed[0].Error))
	default:
		entry.SetError(fmt.Errorf("%d of %d target(s) delivered", len(output.Successful), len(targetKeys)))
	}
	if err := history.Append(entry); err != nil {
		slog.Debug("robot send: prompt history not recorded", "session", opts.Session, "error", err)
	}
}

type actuationTrace struct {
	RequestID      string
	CorrelationID  string
	IdempotencyKey string
}

func normalizeActuationTrace(requestID, correlationID, idempotencyKey string) actuationTrace {
	requestID = strings.TrimSpace(requestID)
	correlationID = strings.TrimSpace(correlationID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)

	if correlationID == "" {
		if requestID != "" {
			correlationID = requestID
		} else {
			correlationID = audit.NewCorrelationID()
		}
	}
	if requestID == "" {
		requestID = correlationID
	}

	return actuationTrace{
		RequestID:      requestID,
		CorrelationID:  correlationID,
		IdempotencyKey: idempotencyKey,
	}
}

func sendErrorsToDetails(failed []SendError) []map[string]any {
	if len(failed) == 0 {
		return nil
	}
	details := make([]map[string]any, 0, len(failed))
	for _, failure := range failed {
		details = append(details, map[string]any{
			"pane":  failure.Pane,
			"error": failure.Error,
		})
	}
	return details
}

func ackConfirmationsToDetails(confirmations []AckConfirmation) []map[string]any {
	if len(confirmations) == 0 {
		return nil
	}
	details := make([]map[string]any, 0, len(confirmations))
	for _, confirmation := range confirmations {
		details = append(details, map[string]any{
			"pane":       confirmation.Pane,
			"ack_type":   confirmation.AckType,
			"ack_at":     confirmation.AckAt,
			"latency_ms": confirmation.LatencyMs,
		})
	}
	return details
}

func interruptErrorsToDetails(failed []InterruptError) []map[string]any {
	if len(failed) == 0 {
		return nil
	}
	details := make([]map[string]any, 0, len(failed))
	for _, failure := range failed {
		details = append(details, map[string]any{
			"pane":   failure.Pane,
			"reason": failure.Reason,
		})
	}
	return details
}

func publishSendActuationRequest(trace actuationTrace, opts SendOptions, targets []string, messagePreview string) {
	if len(targets) == 0 {
		return
	}
	targetWord := "targets"
	if len(targets) == 1 {
		targetWord = "target"
	}
	GetAttentionFeed().PublishActuation(ActuationRecord{
		Session:        opts.Session,
		Action:         "send",
		Stage:          ActuationStageRequest,
		Source:         "robot.send",
		Method:         "paste_enter",
		RequestID:      trace.RequestID,
		CorrelationID:  trace.CorrelationID,
		IdempotencyKey: trace.IdempotencyKey,
		Targets:        targets,
		MessagePreview: messagePreview,
		Summary:        fmt.Sprintf("send requested for %d %s", len(targets), targetWord),
		ReasonCode:     "actuation_requested",
		Actionability:  ActionabilityInteresting,
		Severity:       SeverityInfo,
	})
}

func publishSendActuationOutcome(trace actuationTrace, opts SendOptions, output SendOutput) {
	result := "failed"
	summary := "send failed"
	reasonCode := "actuation_failed"
	actionability := ActionabilityActionRequired
	severity := SeverityError

	switch {
	case output.Blocked:
		result = "blocked"
		summary = "send blocked before dispatch"
		reasonCode = "actuation_blocked"
	case output.ErrorCode == ErrCodeInvalidFlag:
		result = "invalid"
		summary = "send request was invalid"
		reasonCode = "actuation_invalid_request"
		actionability = ActionabilityActionRequired
		severity = SeverityWarning
	case output.ErrorCode == ErrCodeSessionNotFound:
		result = "failed"
		summary = "send failed: session not found"
		reasonCode = "actuation_session_not_found"
		actionability = ActionabilityActionRequired
		severity = SeverityWarning
	case output.ErrorCode == ErrCodeInternalError && len(output.Targets) == 0:
		result = "failed"
		summary = "send failed before dispatch"
		reasonCode = "actuation_internal_error"
	case len(output.Targets) == 0:
		result = "no_targets"
		summary = "send matched no target panes"
		reasonCode = "actuation_no_targets"
		actionability = ActionabilityActionRequired
		severity = SeverityWarning
	case len(output.Failed) == 0 && len(output.Successful) > 0:
		result = "succeeded"
		summary = fmt.Sprintf("send completed for %d target(s)", len(output.Successful))
		reasonCode = "actuation_succeeded"
		actionability = ActionabilityInteresting
		severity = SeverityInfo
	case len(output.Successful) > 0:
		result = "partial"
		summary = fmt.Sprintf("send partially failed for %d of %d target(s)", len(output.Failed), len(output.Targets))
		reasonCode = "actuation_partial_failure"
		actionability = ActionabilityActionRequired
		severity = SeverityWarning
	default:
		result = "failed"
		summary = fmt.Sprintf("send failed for %d target(s)", len(output.Targets))
		reasonCode = "actuation_failed"
		actionability = ActionabilityActionRequired
		severity = SeverityError
	}

	GetAttentionFeed().PublishActuation(ActuationRecord{
		Session:        opts.Session,
		Action:         "send",
		Stage:          ActuationStageOutcome,
		Source:         "robot.send",
		Method:         "paste_enter",
		RequestID:      trace.RequestID,
		CorrelationID:  trace.CorrelationID,
		IdempotencyKey: trace.IdempotencyKey,
		Targets:        output.Targets,
		Successful:     output.Successful,
		Failed:         sendErrorsToDetails(output.Failed),
		MessagePreview: output.MessagePreview,
		Result:         result,
		ErrorCode:      output.ErrorCode,
		Error:          output.Error,
		Blocked:        output.Blocked,
		Summary:        summary,
		ReasonCode:     reasonCode,
		Actionability:  actionability,
		Severity:       severity,
	})
}

func finalizeTerminalSendActuation(trace actuationTrace, opts SendOptions, output *SendOutput) *SendOutput {
	if output != nil {
		publishSendActuationOutcome(trace, opts, *output)
	}
	return output
}

func publishSendActuationVerification(trace actuationTrace, opts SendAndAckOptions, sendOutput SendOutput, ackOutput AckOutput) {
	if len(sendOutput.Successful) == 0 && len(ackOutput.Confirmations) == 0 && len(ackOutput.Pending) == 0 {
		return
	}

	verification := "confirmed"
	summary := fmt.Sprintf("send verification confirmed for %d target(s)", len(ackOutput.Confirmations))
	reasonCode := "actuation_verification_confirmed"
	actionability := ActionabilityInteresting
	severity := SeverityInfo

	switch {
	case ackOutput.TimedOut && len(ackOutput.Confirmations) > 0:
		verification = "partial_timeout"
		summary = fmt.Sprintf("send verification timed out with %d target(s) still pending", len(ackOutput.Pending))
		reasonCode = "actuation_verification_partial_timeout"
		actionability = ActionabilityActionRequired
		severity = SeverityWarning
	case ackOutput.TimedOut:
		verification = "timed_out"
		summary = fmt.Sprintf("send verification timed out for %d target(s)", len(ackOutput.Pending))
		reasonCode = "actuation_verification_timeout"
		actionability = ActionabilityActionRequired
		severity = SeverityWarning
	case len(ackOutput.Failed) > 0 || !ackOutput.Success:
		verification = "failed"
		summary = "send verification failed"
		if len(ackOutput.Failed) > 0 {
			summary = fmt.Sprintf("send verification failed for %d target(s)", len(ackOutput.Failed))
		} else if len(ackOutput.Pending) > 0 {
			summary = fmt.Sprintf("send verification stopped with %d target(s) still pending", len(ackOutput.Pending))
		}
		reasonCode = "actuation_verification_failed"
		actionability = ActionabilityActionRequired
		severity = SeverityWarning
	case len(ackOutput.Pending) > 0:
		verification = "pending"
		summary = fmt.Sprintf("send verification still pending for %d target(s)", len(ackOutput.Pending))
		reasonCode = "actuation_verification_pending"
		actionability = ActionabilityActionRequired
		severity = SeverityWarning
	}

	targets := sendOutput.Successful
	if len(targets) == 0 {
		targets = sendOutput.Targets
	}

	GetAttentionFeed().PublishActuation(ActuationRecord{
		Session:        opts.Session,
		Action:         "send",
		Stage:          ActuationStageVerification,
		Source:         "robot.send_ack",
		Method:         "paste_enter",
		RequestID:      trace.RequestID,
		CorrelationID:  trace.CorrelationID,
		IdempotencyKey: trace.IdempotencyKey,
		Targets:        targets,
		Confirmations:  ackConfirmationsToDetails(ackOutput.Confirmations),
		Pending:        ackOutput.Pending,
		Failed:         ackFailuresToDetails(ackOutput.Failed),
		Verification:   verification,
		ErrorCode:      ackOutput.ErrorCode,
		Error:          ackOutput.Error,
		TimedOut:       ackOutput.TimedOut,
		Summary:        summary,
		ReasonCode:     reasonCode,
		Actionability:  actionability,
		Severity:       severity,
		MessagePreview: sendOutput.MessagePreview,
	})
}

func ackFailuresToDetails(failures []AckFailure) []map[string]any {
	details := make([]map[string]any, 0, len(failures))
	for _, failure := range failures {
		details = append(details, map[string]any{"pane": failure.Pane, "error": failure.Reason})
	}
	return details
}

func finalizeTerminalSendAndAckActuation(trace actuationTrace, opts SendAndAckOptions, output *SendAndAckOutput) *SendAndAckOutput {
	if output != nil {
		publishSendActuationOutcome(trace, opts.SendOptions, output.Send)
		publishSendActuationVerification(trace, opts, output.Send, output.Ack)
	}
	return output
}

func publishInterruptActuationRequest(trace actuationTrace, opts InterruptOptions, targets []string) {
	if len(targets) == 0 {
		return
	}
	targetWord := "targets"
	if len(targets) == 1 {
		targetWord = "target"
	}
	GetAttentionFeed().PublishActuation(ActuationRecord{
		Session:        opts.Session,
		Action:         "interrupt",
		Stage:          ActuationStageRequest,
		Source:         "robot.interrupt",
		Method:         "ctrl_c",
		RequestID:      trace.RequestID,
		CorrelationID:  trace.CorrelationID,
		IdempotencyKey: trace.IdempotencyKey,
		Targets:        targets,
		Summary:        fmt.Sprintf("interrupt requested for %d %s", len(targets), targetWord),
		ReasonCode:     "actuation_requested",
		Actionability:  ActionabilityInteresting,
		Severity:       SeverityWarning,
	})
}

func publishInterruptActuationOutcome(trace actuationTrace, opts InterruptOptions, targets []string, output *InterruptOutput) {
	if output == nil {
		return
	}

	result := "failed"
	summary := "interrupt failed"
	reasonCode := "actuation_failed"
	actionability := ActionabilityActionRequired
	severity := SeverityError

	switch {
	case output.ErrorCode == ErrCodeSessionNotFound:
		result = "failed"
		summary = "interrupt failed: session not found"
		reasonCode = "actuation_session_not_found"
		actionability = ActionabilityActionRequired
		severity = SeverityWarning
	case output.ErrorCode == ErrCodeInternalError && len(targets) == 0:
		result = "failed"
		summary = "interrupt failed before dispatch"
		reasonCode = "actuation_internal_error"
	case len(targets) == 0:
		result = "no_targets"
		summary = "interrupt matched no target panes"
		reasonCode = "actuation_no_targets"
		actionability = ActionabilityActionRequired
		severity = SeverityWarning
	case len(output.Interrupted) == 0 && len(output.ReadyForInput) > 0 && len(output.Failed) == 0:
		result = "already_ready"
		summary = fmt.Sprintf("interrupt skipped because %d target(s) were already ready", len(output.ReadyForInput))
		reasonCode = "actuation_already_ready"
		actionability = ActionabilityInteresting
		severity = SeverityInfo
	case len(output.Interrupted) > 0 && len(output.Failed) == 0:
		result = "succeeded"
		summary = fmt.Sprintf("interrupt sent to %d target(s)", len(output.Interrupted))
		reasonCode = "actuation_succeeded"
		actionability = ActionabilityInteresting
		severity = SeverityWarning
	case len(output.Interrupted) > 0:
		result = "partial"
		summary = fmt.Sprintf("interrupt partially failed for %d of %d target(s)", len(output.Failed), len(targets))
		reasonCode = "actuation_partial_failure"
		actionability = ActionabilityActionRequired
		severity = SeverityWarning
	default:
		result = "failed"
		summary = fmt.Sprintf("interrupt failed for %d target(s)", len(targets))
		reasonCode = "actuation_failed"
		actionability = ActionabilityActionRequired
		severity = SeverityError
	}

	GetAttentionFeed().PublishActuation(ActuationRecord{
		Session:        opts.Session,
		Action:         "interrupt",
		Stage:          ActuationStageOutcome,
		Source:         "robot.interrupt",
		Method:         "ctrl_c",
		RequestID:      trace.RequestID,
		CorrelationID:  trace.CorrelationID,
		IdempotencyKey: trace.IdempotencyKey,
		Targets:        targets,
		Successful:     output.Interrupted,
		Failed:         interruptErrorsToDetails(output.Failed),
		Result:         result,
		ErrorCode:      output.ErrorCode,
		Error:          output.Error,
		Summary:        summary,
		ReasonCode:     reasonCode,
		Actionability:  actionability,
		Severity:       severity,
	})
}

func finalizeTerminalInterruptActuation(trace actuationTrace, opts InterruptOptions, targets []string, output *InterruptOutput) *InterruptOutput {
	if output != nil {
		publishInterruptActuationOutcome(trace, opts, targets, output)
		publishInterruptActuationVerification(trace, opts, targets, output)
	}
	return output
}

func publishInterruptActuationVerification(trace actuationTrace, opts InterruptOptions, targets []string, output *InterruptOutput) {
	if output == nil || len(targets) == 0 || (output.ErrorCode == ErrCodeNotImplemented && len(output.Interrupted) == 0) {
		return
	}

	verification := "ready"
	summary := fmt.Sprintf("interrupt verification confirmed for %d target(s)", len(output.ReadyForInput))
	reasonCode := "actuation_verification_confirmed"
	actionability := ActionabilityInteresting
	severity := SeverityInfo

	switch {
	case opts.NoWait:
		verification = "not_requested"
		summary = "interrupt returned without closed-loop verification"
		reasonCode = "actuation_verification_skipped"
		actionability = ActionabilityInteresting
		severity = SeverityWarning
	case output.TimedOut && len(output.ReadyForInput) > 0:
		verification = "partial_timeout"
		summary = fmt.Sprintf("interrupt verification timed out with %d target(s) still unresolved", len(targets)-len(output.ReadyForInput))
		reasonCode = "actuation_verification_partial_timeout"
		actionability = ActionabilityActionRequired
		severity = SeverityWarning
	case output.TimedOut:
		verification = "timed_out"
		summary = fmt.Sprintf("interrupt verification timed out for %d target(s)", len(targets))
		reasonCode = "actuation_verification_timeout"
		actionability = ActionabilityActionRequired
		severity = SeverityWarning
	case len(output.ReadyForInput) < len(targets):
		verification = "partial"
		summary = fmt.Sprintf("interrupt verification incomplete for %d of %d target(s)", len(targets)-len(output.ReadyForInput), len(targets))
		reasonCode = "actuation_verification_partial"
		actionability = ActionabilityActionRequired
		severity = SeverityWarning
	}

	pending := []string{}
	if len(output.ReadyForInput) < len(targets) {
		ready := make(map[string]struct{}, len(output.ReadyForInput))
		for _, pane := range output.ReadyForInput {
			ready[pane] = struct{}{}
		}
		for _, target := range targets {
			if _, ok := ready[target]; !ok {
				pending = append(pending, target)
			}
		}
	}

	GetAttentionFeed().PublishActuation(ActuationRecord{
		Session:        opts.Session,
		Action:         "interrupt",
		Stage:          ActuationStageVerification,
		Source:         "robot.interrupt",
		Method:         "ctrl_c",
		RequestID:      trace.RequestID,
		CorrelationID:  trace.CorrelationID,
		IdempotencyKey: trace.IdempotencyKey,
		Targets:        targets,
		Successful:     output.ReadyForInput,
		Pending:        pending,
		Verification:   verification,
		ErrorCode:      output.ErrorCode,
		Error:          output.Error,
		TimedOut:       output.TimedOut,
		Summary:        summary,
		ReasonCode:     reasonCode,
		Actionability:  actionability,
		Severity:       severity,
	})
}

func normalizeSendRedactionConfig(cfg redaction.Config) redaction.Config {
	// Zero value is treated as off for backwards compatibility.
	if cfg.Mode == "" {
		cfg.Mode = redaction.ModeOff
	}
	return cfg
}

func summarizeSendRedactionResult(result redaction.Result) RedactionSummary {
	summary := RedactionSummary{
		Mode:     string(result.Mode),
		Findings: len(result.Findings),
	}

	cats := make(map[string]int, len(result.Findings))
	for _, f := range result.Findings {
		cats[string(f.Category)]++
	}
	if len(cats) > 0 {
		summary.Categories = cats
	}

	switch result.Mode {
	case redaction.ModeOff:
		summary.Action = "off"
	case redaction.ModeWarn:
		summary.Action = "warn"
	case redaction.ModeRedact:
		summary.Action = "redact"
	case redaction.ModeBlock:
		summary.Action = "block"
	}

	return summary
}

func formatRedactionCategoryCounts(categories map[string]int) string {
	if len(categories) == 0 {
		return ""
	}
	keys := make([]string, 0, len(categories))
	for k := range categories {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, categories[k]))
	}
	return strings.Join(parts, ", ")
}

func applySendMessageRedaction(message string, cfg redaction.Config) (messageToSend string, preview string, summary RedactionSummary, warnings []string, blocked bool) {
	cfg = normalizeSendRedactionConfig(cfg)
	warnings = []string{}

	// Default summary always reflects configured mode.
	summary = RedactionSummary{
		Mode:     string(cfg.Mode),
		Findings: 0,
		Action:   string(cfg.Mode),
	}
	if cfg.Mode == redaction.ModeOff {
		summary.Action = "off"
		return message, truncateMessage(message), summary, warnings, false
	}

	result := redaction.ScanAndRedact(message, cfg)
	summary = summarizeSendRedactionResult(result)

	if len(result.Findings) == 0 {
		// No findings: keep message unchanged regardless of mode.
		return message, truncateMessage(message), summary, warnings, false
	}

	switch cfg.Mode {
	case redaction.ModeWarn:
		msg := "Warning: potential secrets detected in message"
		if parts := formatRedactionCategoryCounts(summary.Categories); parts != "" {
			msg = fmt.Sprintf("%s (%s)", msg, parts)
		}
		warnings = append(warnings, msg)
		return message, truncateMessage(message), summary, warnings, false
	case redaction.ModeRedact:
		msg := "Warning: redacted potential secrets in message"
		if parts := formatRedactionCategoryCounts(summary.Categories); parts != "" {
			msg = fmt.Sprintf("%s (%s)", msg, parts)
		}
		warnings = append(warnings, msg)
		return result.Output, truncateMessage(result.Output), summary, warnings, false
	case redaction.ModeBlock:
		msg := "Blocked: potential secrets detected in message (redaction mode: block)"
		if parts := formatRedactionCategoryCounts(summary.Categories); parts != "" {
			msg = fmt.Sprintf("%s (%s)", msg, parts)
		}
		warnings = append(warnings, msg)

		previewCfg := cfg
		previewCfg.Mode = redaction.ModeRedact
		previewRes := redaction.ScanAndRedact(message, previewCfg)
		return "", truncateMessage(previewRes.Output), summary, warnings, true
	default:
		return message, truncateMessage(message), summary, warnings, false
	}
}

// robotFinalMessageRedactor adapts the robot redaction envelope to the neutral
// dispatch service. Robot send has one final message for every target, so the
// scan is memoized while the service still invokes the final-message policy for
// every prepared pane.
type robotFinalMessageRedactor struct {
	config redaction.Config
	once   sync.Once
	result dispatchsvc.RedactionResult
	view   struct {
		preview  string
		summary  RedactionSummary
		warnings []string
	}
}

func (r *robotFinalMessageRedactor) RedactFinalMessage(_ context.Context, _ dispatchsvc.Target, message string) (dispatchsvc.RedactionResult, error) {
	r.once.Do(func() {
		final, preview, summary, warnings, blocked := applySendMessageRedaction(message, r.config)
		r.result = dispatchsvc.RedactionResult{
			Message:    final,
			Mode:       summary.Mode,
			Findings:   summary.Findings,
			Categories: cloneStringIntMap(summary.Categories),
			Warnings:   cloneStringsPreserveNil(warnings),
			Blocked:    blocked,
		}
		r.view.preview = preview
		r.view.summary = summary
		r.view.summary.Categories = cloneStringIntMap(summary.Categories)
		r.view.warnings = cloneStringsPreserveNil(warnings)
	})
	result := r.result
	result.Categories = cloneStringIntMap(r.result.Categories)
	result.Warnings = cloneStringsPreserveNil(r.result.Warnings)
	return result, nil
}

func (r *robotFinalMessageRedactor) outputView() (string, RedactionSummary, []string) {
	preview := r.view.preview
	summary := r.view.summary
	summary.Categories = cloneStringIntMap(summary.Categories)
	return preview, summary, cloneStringsPreserveNil(r.view.warnings)
}

func cloneStringsPreserveNil(input []string) []string {
	if input == nil {
		return nil
	}
	return append(make([]string, 0, len(input)), input...)
}

func cloneStringIntMap(input map[string]int) map[string]int {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]int, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

type robotDispatchProtocolPlanner struct{}

func (robotDispatchProtocolPlanner) PlanDelivery(_ context.Context, target dispatchsvc.Target, submit bool) (dispatchsvc.ProtocolPlan, error) {
	if !submit {
		return dispatchsvc.ProtocolPlan{Protocol: dispatchsvc.ProtocolStageOnly}, nil
	}
	resolvedType := paneAgentType(target.Pane)
	if robotSendUsesDoubleEnter(target.Pane.Type, resolvedType, true) {
		return dispatchsvc.ProtocolPlan{
			Protocol:         dispatchsvc.ProtocolDoubleEnter,
			EnterDelay:       tmux.DoubleEnterFirstDelay,
			SecondEnterDelay: tmux.DoubleEnterSecondDelay,
		}, nil
	}
	enterDelay := tmux.DefaultEnterDelay
	if target.Pane.Type == tmux.AgentUser || resolvedType == "user" || resolvedType == "unknown" {
		enterDelay = tmux.ShellEnterDelay
	}
	return dispatchsvc.ProtocolPlan{Protocol: dispatchsvc.ProtocolSingleEnter, EnterDelay: enterDelay}, nil
}

func newRobotDispatchService(redactCfg redaction.Config, deliverer dispatchsvc.Deliverer, pacer dispatchsvc.Pacer) (*dispatchsvc.Service, *robotFinalMessageRedactor, error) {
	if deliverer == nil {
		deliverer = dispatchsvc.TMUXDeliverer{}
	}
	redactor := &robotFinalMessageRedactor{config: redactCfg}
	service, err := dispatchsvc.NewService(dispatchsvc.Ports{
		Redactor:  redactor,
		Protocols: robotDispatchProtocolPlanner{},
		Deliverer: deliverer,
		Pacer:     pacer,
	})
	return service, redactor, err
}

// A successful preflight is not authority to send into a replacement pane
// after pacing or an interrupt's readiness wait. Recheck request-local policy
// on the original physical target and exact prepared bytes at delivery time.
func guardedRobotMessageDeliverer(guard func(context.Context, string, []tmux.Pane) error) dispatchsvc.Deliverer {
	if guard == nil {
		return nil
	}
	return dispatchsvc.DelivererFunc(func(ctx context.Context, delivery dispatchsvc.Delivery) error {
		if err := guard(ctx, delivery.Message, []tmux.Pane{delivery.Target.Pane}); err != nil {
			return dispatchsvc.GuaranteeNoDeliveryActuation(err)
		}
		return (dispatchsvc.TMUXDeliverer{}).Deliver(ctx, delivery)
	})
}

func robotDispatchPrepareErrorResponse(err error) RobotResponse {
	errorCode := ErrCodeInternalError
	hint := "Inspect the dispatch target and message policy"
	var dispatchErr *dispatchsvc.Error
	if !errors.As(err, &dispatchErr) {
		return NewErrorResponse(err, errorCode, hint)
	}

	switch dispatchErr.Code {
	case dispatchsvc.ErrPromptDeliveryUnsupported:
		errorCode = ErrCodeNotImplemented
		hint = agent.PromptDeliveryCapabilityHint
	case dispatchsvc.ErrInvalidRequest, dispatchsvc.ErrInvalidSelector, dispatchsvc.ErrNoTargets:
		errorCode = ErrCodeInvalidFlag
		hint = "Check pane selectors, agent filters, delay, and message options"
	}
	return NewErrorResponse(err, errorCode, hint)
}

func robotPreparedDispatchRequest(allPanes, targetPanes []tmux.Pane, opts SendOptions, message string, submit bool) dispatchsvc.Request {
	planningPanes := make([]tmux.Pane, len(allPanes))
	for i, pane := range allPanes {
		planningPanes[i] = pane
		planningPanes[i].Tags = append([]string(nil), pane.Tags...)
		switch paneAgentType(pane) {
		case "grok":
			planningPanes[i].Type = tmux.AgentGrok
		case "omp":
			// A title-only omp pane must still plan the omp composer gate,
			// Ctrl+C clear, and submission verifier.
			planningPanes[i].Type = tmux.AgentOmp
		case "oc":
			planningPanes[i].Type = tmux.AgentOpencode
		case "antigravity":
			planningPanes[i].Type = tmux.AgentAntigravity
		}
	}
	selectors := make([]string, 0, len(targetPanes))
	for _, pane := range targetPanes {
		if pane.ID != "" {
			selectors = append(selectors, pane.ID)
		} else {
			selectors = append(selectors, pane.Ref().Physical())
		}
	}
	return dispatchsvc.Request{
		Session:           opts.Session,
		Panes:             planningPanes,
		Selectors:         selectors,
		IncludeUser:       true,
		Message:           message,
		AllowEmptyMessage: true,
		Submit:            submit,
		Delay:             time.Duration(opts.DelayMs) * time.Millisecond,
		DryRun:            opts.DryRun,
		ClearInput:        opts.ClearInput,
	}
}

func validateRobotPromptDeliveryTargets(allPanes, targetPanes []tmux.Pane, opts SendOptions, message string, submit bool) error {
	targets, err := dispatchsvc.PlanTargets(robotPreparedDispatchRequest(allPanes, targetPanes, opts, message, submit))
	if err != nil {
		return err
	}
	return dispatchsvc.ValidatePromptDeliveryTargets(targets)
}

func applyRobotDispatchResult(output *SendOutput, result dispatchsvc.Result) {
	for _, receipt := range result.Receipts {
		switch receipt.Status {
		case dispatchsvc.ReceiptDelivered:
			output.Successful = append(output.Successful, receipt.Target.Address)
		case dispatchsvc.ReceiptFailed, dispatchsvc.ReceiptSkipped, dispatchsvc.ReceiptBlocked:
			message := receipt.Error
			if message == "" {
				message = "dispatch did not deliver to pane"
			}
			output.Failed = append(output.Failed, SendError{Pane: receipt.Target.Address, Error: message})
		}
	}
}

func finalizeRobotSendDispatchStatus(output *SendOutput) {
	if output == nil {
		return
	}
	if !output.DryRun {
		output.Success = len(output.Failed) == 0 && len(output.Successful) > 0
	}
	if len(output.Failed) == 0 {
		return
	}
	if output.Error == "" {
		output.Error = fmt.Sprintf("%d of %d sends failed", len(output.Failed), len(output.Targets))
	}
	if output.ErrorCode == "" {
		output.ErrorCode = ErrCodeInternalError
	}
}

// ErrSendCommandBlocked marks a send refused by the destructive-command guard.
// Guards wrap it with the reason; any other guard error means the check itself
// failed, and the send is refused fail-closed.
var ErrSendCommandBlocked = errors.New("blocked by dcg")

// sendCommandGuard vets the exact payload about to reach the selected panes for
// destructive shell commands. The cli package installs its dcg check here
// (robot cannot import cli without a cycle) so --robot-send, REST sends and
// `ntm send` apply one guard rather than leaving the robot path unchecked.
var sendCommandGuard func(ctx context.Context, message, session string, panes []tmux.Pane) error

// SetSendCommandGuard registers the destructive-command guard GetSend applies
// before delivery. Passing nil disables it.
func SetSendCommandGuard(guard func(ctx context.Context, message, session string, panes []tmux.Pane) error) {
	sendCommandGuard = guard
}

// checkSendCommandGuard runs the registered destructive-command guard on the
// exact payload about to reach panes. A nil error allows delivery; otherwise
// resp is the typed refusal and blocked reports a dcg verdict, as opposed to a
// check that failed or did not finish (both refuse fail-closed).
func checkSendCommandGuard(ctx context.Context, message, session string, panes []tmux.Pane) (resp RobotResponse, blocked bool, err error) {
	if sendCommandGuard == nil {
		return RobotResponse{}, false, nil
	}
	err = sendCommandGuard(ctx, message, session, panes)
	switch {
	case err == nil:
		return RobotResponse{}, false, nil
	case errors.Is(err, ErrSendCommandBlocked):
		return NewErrorResponse(err, ErrCodeDestructiveCommandBlocked,
			"Remove the destructive command from the message, or run it yourself after review"), true, err
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return NewErrorResponse(fmt.Errorf("destructive-command check did not finish: %w", err),
			ErrCodeTimeout, "The message was refused because the dcg check was interrupted; retry"), false, err
	default:
		return NewErrorResponse(fmt.Errorf("destructive-command check failed: %w", err),
			ErrCodeInternalError, "Check the dcg installation; the message was refused fail-closed"), false, err
	}
}

// guardSendCommand applies checkSendCommandGuard to a send. On refusal it
// records the typed error on output and returns false; the caller must not
// dispatch.
func guardSendCommand(ctx context.Context, output *SendOutput, message, session string, panes []tmux.Pane, dryRun bool) bool {
	resp, blocked, err := checkSendCommandGuard(ctx, message, session, panes)
	if err == nil {
		return true
	}
	output.DryRun = dryRun
	output.Blocked = blocked
	output.RobotResponse = resp
	output.Failed = append(output.Failed, SendError{Pane: "guard", Error: err.Error()})
	return false
}

// GetSend sends a message to multiple panes atomically and returns structured results.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetSend(opts SendOptions) (*SendOutput, error) {
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	trace := normalizeActuationTrace(opts.RequestID, opts.CorrelationID, opts.IdempotencyKey)
	redactCfg := normalizeSendRedactionConfig(opts.Redaction)
	_, initialPreview, initialSummary, initialWarnings, initialBlocked := applySendMessageRedaction(opts.Message, redactCfg)

	if initialBlocked {
		errMsg := "refusing to proceed: potential secrets detected (redaction mode: block)"
		if parts := formatRedactionCategoryCounts(initialSummary.Categories); parts != "" {
			errMsg = fmt.Sprintf("refusing to proceed: potential secrets detected (%s) (redaction mode: block)", parts)
		}
		return finalizeTerminalSendActuation(trace, opts, &SendOutput{
			RobotResponse:  NewErrorResponse(fmt.Errorf("%s", errMsg), "SENSITIVE_DATA_BLOCKED", "Re-run with --allow-secret to bypass, or use --redact=warn/--redact=redact"),
			Session:        opts.Session,
			SentAt:         time.Now().UTC(),
			Blocked:        true,
			Redaction:      initialSummary,
			Warnings:       initialWarnings,
			Targets:        []string{},
			Successful:     []string{},
			Failed:         []SendError{},
			MessagePreview: initialPreview,
		}), nil
	}

	if strings.TrimSpace(opts.Session) == "" {
		return finalizeTerminalSendActuation(trace, opts, &SendOutput{
			RobotResponse:  NewErrorResponse(fmt.Errorf("session name is required"), ErrCodeInvalidFlag, "Provide a session name"),
			Session:        opts.Session,
			SentAt:         time.Now().UTC(),
			Blocked:        false,
			Redaction:      initialSummary,
			Warnings:       initialWarnings,
			Targets:        []string{},
			Successful:     []string{},
			Failed:         []SendError{{Pane: "session", Error: "session name is required"}},
			MessagePreview: initialPreview,
		}), nil
	}

	if !tmux.SessionExists(opts.Session) {
		return finalizeTerminalSendActuation(trace, opts, &SendOutput{
			RobotResponse:  NewErrorResponse(fmt.Errorf("session '%s' not found", opts.Session), ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions"),
			Session:        opts.Session,
			SentAt:         time.Now().UTC(),
			Blocked:        false,
			Redaction:      initialSummary,
			Warnings:       initialWarnings,
			Targets:        []string{},
			Successful:     []string{},
			Failed:         []SendError{{Pane: "session", Error: fmt.Sprintf("session '%s' not found", opts.Session)}},
			MessagePreview: initialPreview,
		}), nil
	}

	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		return finalizeTerminalSendActuation(trace, opts, &SendOutput{
			RobotResponse:  NewErrorResponse(fmt.Errorf("failed to get panes: %w", err), ErrCodeInternalError, "Check tmux is running"),
			Session:        opts.Session,
			SentAt:         time.Now().UTC(),
			Blocked:        false,
			Redaction:      initialSummary,
			Warnings:       initialWarnings,
			Targets:        []string{},
			Successful:     []string{},
			Failed:         []SendError{{Pane: "panes", Error: fmt.Sprintf("failed to get panes: %v", err)}},
			MessagePreview: initialPreview,
		}), nil
	}

	output := SendOutput{
		RobotResponse:  NewRobotResponse(true), // Will be updated based on results
		Session:        opts.Session,
		SentAt:         time.Now().UTC(),
		Blocked:        false,
		Redaction:      initialSummary,
		Warnings:       initialWarnings,
		Targets:        []string{},
		Successful:     []string{},
		Failed:         []SendError{},
		MessagePreview: initialPreview,
	}

	// Build exclusion map
	excludeMap := make(map[string]bool)
	for _, e := range opts.Exclude {
		excludeMap[e] = true
	}

	targetPanes, targetKeys, err := selectSendTargets(panes, opts, excludeMap)
	if err != nil {
		errorCode := ErrCodeInvalidFlag
		var selectorErr *tmux.PaneSelectorError
		if errors.As(err, &selectorErr) && selectorErr.Kind == tmux.PaneSelectorNotFound {
			errorCode = ErrCodePaneNotFound
		}
		output.RobotResponse = NewErrorResponse(err, errorCode, "Use --robot-is-working to inspect canonical pane addresses")
		output.Failed = append(output.Failed, SendError{Pane: "selector", Error: err.Error()})
		return finalizeTerminalSendActuation(trace, opts, &output), nil
	}
	output.Targets = append(output.Targets, targetKeys...)
	var renderBaselines map[string]sendRenderBaseline
	if opts.VerifyRender {
		renderBaselines = captureSendRenderBaselines(targetPanes, targetKeys)
	}

	// CASS history and CM memory rules (--with-cass / --with-memory, bd-3j6hm)
	// through the pipeline every assignment surface shares
	// (prompt_context.go). The retrieval query is the caller's original
	// message, and CM memory is scoped to the verified project of the live
	// targets. Degradation is graceful: cass or cm being unavailable records a
	// skip reason and the send proceeds unmodified.
	if opts.WithCASS && (opts.CASSConfig == nil || opts.CASSConfig.Enabled) {
		inject := DefaultInjectConfig()
		if opts.InjectConfig != nil {
			inject = *opts.InjectConfig
		}
		if inject.CurrentContextPct == 0 || inject.CurrentContextPct < inject.SkipThreshold {
			used, known, err := ntmctx.MaxPaneContextUsage(ctx, tmux.DefaultClient, targetPanes, inject.SkipThreshold)
			if err != nil {
				return nil, fmt.Errorf("send canceled during CASS capacity measurement: %w", err)
			}
			if known {
				inject.CurrentContextPct = max(inject.CurrentContextPct, used)
			}
		}
		opts.InjectConfig = &inject
	}
	messageToSend, cassInjection, memoryInjection := injectPromptContext(
		ctx, opts.Message, opts.Message, sendInjectionFormat(targetPanes),
		PromptContextOptions{
			WithCASS: opts.WithCASS, CASSConfig: opts.CASSConfig, FilterConfig: opts.FilterConfig, InjectConfig: opts.InjectConfig,
			WithMemory: opts.WithMemory, MemoryInject: opts.MemoryInject,
		},
		func(cfg CMInjectConfig) (CMInjectConfig, *CMInjectionInfo) {
			return prepareCMSendContext(ctx, opts.Session, targetPanes, cfg)
		},
	)
	output.CASSInjection, output.MemoryInjection = cassInjection, memoryInjection
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	sendEnter := true
	if opts.Enter != nil {
		sendEnter = *opts.Enter
	}

	// No-target behavior remains a robot-envelope concern. It still performs the
	// final redaction preflight so blocked content cannot leak into previews.
	if len(targetPanes) == 0 {
		_, preview, summary, warnings, blocked := applySendMessageRedaction(messageToSend, redactCfg)
		output.Redaction = summary
		output.Warnings = warnings
		output.Blocked = blocked
		output.MessagePreview = preview
		if blocked {
			errMsg := "refusing to proceed: potential secrets detected (redaction mode: block)"
			if parts := formatRedactionCategoryCounts(summary.Categories); parts != "" {
				errMsg = fmt.Sprintf("refusing to proceed: potential secrets detected (%s) (redaction mode: block)", parts)
			}
			output.RobotResponse = NewErrorResponse(fmt.Errorf("%s", errMsg), "SENSITIVE_DATA_BLOCKED", "Re-run with --allow-secret to bypass, or use --redact=warn/--redact=redact")
			return &output, nil
		}
		output.DryRun = opts.DryRun
		output.RobotResponse = NewErrorResponse(
			errors.New("no target panes matched the filter criteria"),
			ErrCodePaneNotFound,
			"Check --all, --panes, --type, and --exclude filters",
		)
		output.AgentHints = generateSendHints(output)
		return finalizeTerminalSendActuation(trace, opts, &output), nil
	}

	if opts.MessageGuard != nil {
		finalMessage, _, _, _, blocked := applySendMessageRedaction(messageToSend, redactCfg)
		if !blocked {
			if err := opts.MessageGuard(ctx, finalMessage, append([]tmux.Pane(nil), targetPanes...)); err != nil {
				return nil, fmt.Errorf("message policy check: %w", err)
			}
		}
	}

	// Destructive-command guard on the exact post-injection payload, before an
	// operation ID is claimed or any keystroke is injected.
	if !guardSendCommand(ctx, &output, messageToSend, opts.Session, targetPanes, opts.DryRun) {
		return finalizeTerminalSendActuation(trace, opts, &output), nil
	}

	// Durable idempotent operation claim (#245; protocol shared with
	// --robot-interrupt in operation_idempotency.go). The claim happens
	// BEFORE any keystroke is injected and binds the operation ID to the
	// caller's canonical selector spec plus input-message digest; the
	// receipt's payload digest records the exact bytes about to be delivered
	// (post CASS injection). Identical retries replay the recorded outcome;
	// conflicting reuse is rejected; only a stale claim with durable proof
	// that dispatch never started may be taken over. Preflight failures
	// release the claim so the ID stays retryable.
	dispatchAttempted := false
	var owned *durableOperation
	if trace.IdempotencyKey != "" && !opts.DryRun {
		claim := claimRobotOperation(operationClaimRequest{
			Kind:          state.OperationKindSend,
			OperationID:   trace.IdempotencyKey,
			Session:       opts.Session,
			BindingHash:   sendOperationBindingHash(opts),
			Payload:       messageToSend,
			Targets:       output.Targets,
			Store:         opts.Store,
			ExplicitStore: opts.ExplicitStore,
		})
		switch claim.Verdict {
		case operationRefused:
			output.RobotResponse = claim.Response
			output.Operation = claim.Info
			return finalizeTerminalSendActuation(trace, opts, &output), nil
		case operationReplay:
			if err := applyReplayedSendOutcome(&output, claim.Record); err != nil {
				output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Stored send outcome could not be decoded")
				return finalizeTerminalSendActuation(trace, opts, &output), nil
			}
			output.AgentHints = generateSendHints(output)
			return finalizeTerminalSendActuation(trace, opts, &output), nil
		}
		owned = claim.Owned
		defer func() {
			// After a dispatch attempt the outcome is terminal: record it so
			// the receipt survives caller timeouts. Before any dispatch
			// attempt (preflight failure) nothing was typed, so the claim is
			// released and the operation ID stays retryable.
			if dispatchAttempted {
				completeSendOperation(owned, &output)
			} else if warning := owned.release(); warning != "" {
				output.Warnings = append(output.Warnings, warning)
			}
		}()
	}

	service, finalRedactor, err := newRobotDispatchService(redactCfg, guardedRobotMessageDeliverer(opts.MessageGuard), nil)
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Dispatch service initialization failed")
		return finalizeTerminalSendActuation(trace, opts, &output), nil
	}
	prepared, prepareErr := service.Prepare(
		ctx,
		robotPreparedDispatchRequest(panes, targetPanes, opts, messageToSend, sendEnter),
	)
	preview, summary, warnings := finalRedactor.outputView()
	if summary.Mode != "" {
		output.Redaction = summary
		output.Warnings = warnings
		output.Blocked = summary.Action == string(redaction.ModeBlock) && summary.Findings > 0
		output.MessagePreview = preview
	}
	if prepareErr != nil {
		var dispatchErr *dispatchsvc.Error
		if errors.As(prepareErr, &dispatchErr) && dispatchErr.Code == dispatchsvc.ErrRedactionBlocked {
			errMsg := "refusing to proceed: potential secrets detected (redaction mode: block)"
			if parts := formatRedactionCategoryCounts(summary.Categories); parts != "" {
				errMsg = fmt.Sprintf("refusing to proceed: potential secrets detected (%s) (redaction mode: block)", parts)
			}
			output.RobotResponse = NewErrorResponse(fmt.Errorf("%s", errMsg), "SENSITIVE_DATA_BLOCKED", "Re-run with --allow-secret to bypass, or use --redact=warn/--redact=redact")
			output.Blocked = true
			return &output, nil
		}
		output.RobotResponse = robotDispatchPrepareErrorResponse(prepareErr)
		output.Failed = append(output.Failed, SendError{Pane: "dispatch", Error: prepareErr.Error()})
		return finalizeTerminalSendActuation(trace, opts, &output), nil
	}

	if opts.DryRun {
		result, dispatchErr := service.Dispatch(ctx, prepared)
		if dispatchErr != nil {
			output.RobotResponse = NewErrorResponse(dispatchErr, ErrCodeInternalError, "Dispatch dry-run failed")
			return finalizeTerminalSendActuation(trace, opts, &output), nil
		}
		output.DryRun = result.DryRun
		output.WouldSendTo = append(output.WouldSendTo, output.Targets...)
		output.Success = result.Success
		return &output, nil
	}

	if owned != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// The redactor memoizes the exact final message shared by all
		// prepared targets. Persist its digest after redaction, together
		// with the ownership-fenced dispatch boundary, before any input.
		if err := owned.start(finalRedactor.result.Message, output.Targets); err != nil {
			output.RobotResponse = operationStartErrorResponse(err)
			return finalizeTerminalSendActuation(trace, opts, &output), nil
		}
		output.Operation = operationInfoFromRecord(owned.record, false)
	}

	publishSendActuationRequest(trace, opts, output.Targets, output.MessagePreview)
	dispatchAttempted = true
	dispatchStarted := time.Now()
	result, _ := service.Dispatch(ctx, prepared)
	applyRobotDispatchResult(&output, result)
	recordRobotSendHistory(opts, redactCfg, targetPanes, targetKeys, output, dispatchStarted)
	if opts.VerifyRender {
		output.RenderEvidence = verifySendRenderEvidence(targetPanes, targetKeys, output.Successful, renderBaselines)
	}

	// Update success based on results without clobbering a typed preflight error.
	finalizeRobotSendDispatchStatus(&output)
	if opts.VerifyRender && !sendRenderEvidenceComplete(output.RenderEvidence) {
		output.Success = false
		if output.Error == "" {
			output.Error = "send delivery was not confirmed by bounded rendered-output evidence"
			output.ErrorCode = ErrCodeInternalError
		}
	}

	// Account delivered payload bytes in the analytics event stream so
	// robot-mode sends accumulate into chars_sent alongside CLI sends
	// (bd-ws7-docs-ux-truth-tqh3l.4). len(messageToSend) is the exact
	// post-injection payload delivered to each successful pane.
	if delivered := len(output.Successful); delivered > 0 {
		ntmevents.EmitPromptSend(opts.Session, delivered, len(messageToSend), "",
			robotSendObservedTargetTypes(targetPanes, targetKeys, output.Successful), false)
	}

	// Generate agent hints
	output.AgentHints = generateSendHints(output)
	publishSendActuationOutcome(trace, opts, output)

	return &output, nil
}

// robotSendObservedTargetTypes joins the distinct agent types of the panes
// whose delivery succeeded, for prompt_send attribution.
func robotSendObservedTargetTypes(panes []tmux.Pane, keys, successful []string) string {
	ok := make(map[string]struct{}, len(successful))
	for _, key := range successful {
		ok[key] = struct{}{}
	}
	seen := make(map[string]struct{})
	types := make([]string, 0, len(panes))
	for i, pane := range panes {
		if i >= len(keys) {
			break
		}
		if _, delivered := ok[keys[i]]; !delivered {
			continue
		}
		agentType := paneAgentType(pane)
		if agentType == "" || agentType == "unknown" || agentType == "user" {
			continue
		}
		if _, dup := seen[agentType]; dup {
			continue
		}
		seen[agentType] = struct{}{}
		types = append(types, agentType)
	}
	return strings.Join(types, ",")
}

const sendRenderVerificationLines = 20

type sendRenderBaseline struct {
	output string
	err    error
}

func captureSendRenderBaselines(panes []tmux.Pane, keys []string) map[string]sendRenderBaseline {
	return captureSendRenderBaselinesWith(panes, keys, func(pane tmux.Pane) (string, error) {
		return capturePaneFallback(pane, sendRenderVerificationLines)
	})
}

func captureSendRenderBaselinesWith(panes []tmux.Pane, keys []string, capture func(tmux.Pane) (string, error)) map[string]sendRenderBaseline {
	baselines := make(map[string]sendRenderBaseline, len(panes))
	for i, pane := range panes {
		if i >= len(keys) {
			break
		}
		output, err := capture(pane)
		baselines[keys[i]] = sendRenderBaseline{output: output, err: err}
	}
	return baselines
}

// sendRenderSettle bounds how long --verify-render keeps re-capturing a
// delivered pane whose screen still matches the pre-send baseline. A TUI such
// as Claude Code redraws its composer a few milliseconds after the paste
// lands, so a single capture taken the instant Dispatch returns can miss text
// that is already in the composer — most visibly on a stage-only send
// (--enter=false), which returns as soon as the keys are written (GH #332).
type sendRenderSettle struct {
	// polls is the maximum number of re-captures after the first capture.
	polls int
	// interval is the pause before each re-capture.
	interval time.Duration
	sleep    func(time.Duration)
}

// defaultSendRenderSettle re-captures for up to ~2s. Panes whose render
// changes stop being polled immediately, so only a pane that genuinely shows
// no change pays the full window.
var defaultSendRenderSettle = sendRenderSettle{polls: 20, interval: 100 * time.Millisecond, sleep: time.Sleep}

func verifySendRenderEvidence(panes []tmux.Pane, keys, successful []string, baselines map[string]sendRenderBaseline) []SendRenderEvidence {
	return verifySendRenderEvidenceWith(panes, keys, successful, baselines, func(pane tmux.Pane) (string, error) {
		return capturePaneFallback(pane, sendRenderVerificationLines)
	}, defaultSendRenderSettle)
}

func verifySendRenderEvidenceWith(panes []tmux.Pane, keys, successful []string, baselines map[string]sendRenderBaseline, capture func(tmux.Pane) (string, error), settle sendRenderSettle) []SendRenderEvidence {
	successfulSet := make(map[string]struct{}, len(successful))
	for _, key := range successful {
		successfulSet[key] = struct{}{}
	}
	evidence := make([]SendRenderEvidence, 0, len(keys))
	// pending holds indexes into evidence (and panes) that are worth
	// re-capturing: delivered, with a baseline, and not yet changed.
	var pending []int
	for i, pane := range panes {
		if i >= len(keys) {
			break
		}
		key := keys[i]
		baseline := baselines[key]
		entry := SendRenderEvidence{Pane: key, BaselineAvailable: baseline.err == nil}
		_, entry.Delivered = successfulSet[key]
		observeSendRender(&entry, baseline, pane, capture)
		if entry.Delivered && entry.BaselineAvailable && !entry.RenderChanged {
			pending = append(pending, len(evidence))
		}
		evidence = append(evidence, entry)
	}

	for poll := 0; poll < settle.polls && len(pending) > 0; poll++ {
		if settle.sleep != nil && settle.interval > 0 {
			settle.sleep(settle.interval)
		}
		still := pending[:0]
		for _, idx := range pending {
			observeSendRender(&evidence[idx], baselines[keys[idx]], panes[idx], capture)
			if !evidence[idx].RenderChanged {
				still = append(still, idx)
			}
		}
		pending = still
	}

	for i := range evidence {
		entry := &evidence[i]
		entry.DeliveredAndRendered = entry.Delivered && entry.BaselineAvailable && entry.RenderAvailable && entry.RenderChanged
	}
	return evidence
}

// observeSendRender records one post-send capture of pane into entry,
// replacing any earlier observation so the evidence reflects the latest look.
func observeSendRender(entry *SendRenderEvidence, baseline sendRenderBaseline, pane tmux.Pane, capture func(tmux.Pane) (string, error)) {
	entry.CaptureError = ""
	entry.RenderAvailable = false
	entry.RenderChanged = false
	if baseline.err != nil {
		entry.CaptureError = "pre-send capture unavailable: " + baseline.err.Error()
	}
	post, err := capture(pane)
	if err != nil {
		if entry.CaptureError != "" {
			entry.CaptureError += "; "
		}
		entry.CaptureError += "post-send capture unavailable: " + err.Error()
		return
	}
	entry.RenderAvailable = true
	entry.RenderChanged = baseline.err == nil && baseline.output != post
}

func sendRenderEvidenceComplete(evidence []SendRenderEvidence) bool {
	if len(evidence) == 0 {
		return false
	}
	for _, entry := range evidence {
		if !entry.DeliveredAndRendered {
			return false
		}
	}
	return true
}

// robotSendUsesDoubleEnter reports whether --robot-send should deliver via the
// double-Enter submission protocol (#187): agent panes with Enter requested.
// User/unknown panes keep the single delayed-Enter path (shells need no
// double-Enter and would execute a spurious empty command), as does
// --enter=false (text staged without submission).
func robotSendUsesDoubleEnter(paneType tmux.AgentType, resolvedType string, sendEnter bool) bool {
	if !sendEnter {
		return false
	}
	if paneType == tmux.AgentUser || resolvedType == "user" || resolvedType == "unknown" {
		return false
	}
	return true
}

// paneSessionIsMultiWindow reports whether the session's panes span more than
// one tmux window. On a multi-window session a bare window-local pane index is
// no longer a unique address, so targeting and the response-map key switch to
// the canonical "window.pane" form (#172).
func paneSessionIsMultiWindow(panes []tmux.Pane) bool {
	return tmux.PanesSpanMultipleWindows(panes)
}

// paneTargetKey returns the canonical response-map / target key for a pane.
// Single-window sessions use the bare window-local index (byte-identical to the
// historical behavior every robot consumer expects). Multi-window sessions use
// the "window.pane" address so a window-per-agent layout (N windows × 1 pane,
// every pane sharing window-local index, e.g. 1) does not collapse onto one
// key (#172).
func paneTargetKey(pane tmux.Pane, multiWindow bool) string {
	return tmux.PaneTargetKey(pane, multiWindow)
}

// paneMatchesToken reports whether a --panes token addresses the given pane.
// The grammar (first non-empty match wins) is topology-aware (#172):
//
//   - "%N"  -> tmux pane ID (pane.ID); base-index-independent, always unambiguous.
//   - "W.P" -> WindowIndex.Index; explicit window.pane address.
//   - bare integer N:
//   - on a multi-window session, N selects a whole WINDOW (WindowIndex==N),
//     so --panes=1 hits exactly the panes in window 1 instead of broadcasting
//     to every window's index-1 pane.
//   - on a single-window session, N is the window-local pane index
//     (Index==N) — byte-identical to the historical behavior.
//
// Returning here is purely a membership test; callers build the response keys
// via paneTargetKey so single-window output stays unchanged.
func paneMatchesToken(pane tmux.Pane, token string, multiWindow bool) bool {
	return tmux.PaneMatchesSelector(pane, token, multiWindow)
}

// paneMatchesAnyToken reports whether any token in the filter set addresses the
// pane (topology-aware, #172).
func paneMatchesAnyToken(pane tmux.Pane, tokens []string, multiWindow bool) bool {
	for _, t := range tokens {
		if paneMatchesToken(pane, t, multiWindow) {
			return true
		}
	}
	return false
}

func selectSendTargets(panes []tmux.Pane, opts SendOptions, excludeMap map[string]bool) ([]tmux.Pane, []string, error) {
	ordered := tmux.SortPanesByTopology(panes)
	singularPane := strings.TrimSpace(opts.Pane)
	if singularPane != "" && len(opts.Panes) > 0 {
		return nil, nil, &dispatchsvc.Error{
			Code: dispatchsvc.ErrInvalidRequest,
			Err:  errors.New("--pane and --panes are mutually exclusive for robot send"),
		}
	}
	selectors := append([]string(nil), opts.Panes...)
	requireSingle := false
	if singularPane != "" {
		selectors = []string{singularPane}
		requireSingle = true
	}
	excludeTokens := make([]string, 0, len(excludeMap))
	for k := range excludeMap {
		excludeTokens = append(excludeTokens, k)
	}
	sort.Strings(excludeTokens)

	planningPanes := make([]tmux.Pane, len(ordered))
	originalByKey := make(map[string]tmux.Pane, len(ordered))
	for i, pane := range ordered {
		planningPanes[i] = pane
		planningPanes[i].Tags = append([]string(nil), pane.Tags...)
		planningPanes[i].Type = tmux.AgentType(paneAgentType(pane)).Canonical()
		originalByKey[pane.Ref().StableKey()] = pane
	}

	filters := make([]dispatchsvc.AgentFilter, 0, len(opts.AgentTypes))
	for _, t := range opts.AgentTypes {
		filters = append(filters, dispatchsvc.AgentFilter{Type: tmux.AgentType(normalizeAgentType(t))})
	}

	// Preserve the historical robot default that treats an unclassified pane 0
	// as the operator pane. Explicit pane/type filters still opt it back in.
	if !opts.All && len(selectors) == 0 && len(opts.AgentTypes) == 0 {
		for _, pane := range ordered {
			if pane.Index != 0 || paneAgentType(pane) != "unknown" {
				continue
			}
			if pane.ID != "" {
				excludeTokens = append(excludeTokens, pane.ID)
			} else {
				excludeTokens = append(excludeTokens, pane.Ref().Physical())
			}
		}
	}

	targets, err := dispatchsvc.PlanTargets(dispatchsvc.Request{
		Panes:                 planningPanes,
		Selectors:             selectors,
		RequireSingleSelector: requireSingle,
		ExcludeSelectors:      excludeTokens,
		AgentFilters:          filters,
		IncludeUser:           opts.All,
	})
	if err != nil {
		return nil, nil, err
	}
	targetPanes := make([]tmux.Pane, 0, len(targets))
	targetKeys := make([]string, 0, len(targets))
	for _, target := range targets {
		original, ok := originalByKey[target.Ref.StableKey()]
		if !ok {
			return nil, nil, fmt.Errorf("dispatch target %q was not present in the source pane snapshot", target.Ref.StableKey())
		}
		targetPanes = append(targetPanes, original)
		targetKeys = append(targetKeys, target.Address)
	}
	return targetPanes, targetKeys, nil
}

// PrintSend outputs the send operation result as JSON.
// This is a thin wrapper around GetSend() for CLI output.
func PrintSend(opts SendOptions) error {
	output, err := GetSend(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot send failed")
}

// generateSendHints creates actionable hints based on send results
func generateSendHints(output SendOutput) *SendAgentHints {
	var suggestions []string
	var summary string

	if len(output.Failed) == 0 && len(output.Successful) > 0 {
		summary = fmt.Sprintf("Sent to %d agent(s) successfully", len(output.Successful))
		suggestions = append(suggestions, "Wait for agent acknowledgment using --robot-tail")
	} else if len(output.Failed) > 0 && len(output.Successful) > 0 {
		summary = fmt.Sprintf("Partial success: %d sent, %d failed", len(output.Successful), len(output.Failed))
		suggestions = append(suggestions, "Retry failed panes individually")
	} else if len(output.Failed) > 0 {
		summary = fmt.Sprintf("All %d sends failed", len(output.Failed))
		suggestions = append(suggestions, "Check agent states with --robot-tail")
		suggestions = append(suggestions, "Verify session and pane existence")
	} else if len(output.Targets) == 0 {
		summary = "No target panes matched the filter criteria"
		suggestions = append(suggestions, "Check --all, --panes, or --agent-types flags")
	}

	if summary == "" {
		return nil
	}

	return &SendAgentHints{
		Summary:     summary,
		Suggestions: suggestions,
	}
}

// truncateMessage truncates a message to 50 runes with ellipsis.
// Uses rune count instead of byte count to handle UTF-8 correctly.
func truncateMessage(msg string) string {
	runes := []rune(msg)
	if len(runes) > 50 {
		return string(runes[:47]) + "..."
	}
	return msg
}

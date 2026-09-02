// Package dispatch provides the application service shared by human and robot
// prompt-delivery surfaces. It owns target planning, final-message preflight,
// delivery protocol selection, pacing, and per-pane receipts. Command-specific
// concerns such as hooks, audit events, checkpoints, and CASS lookup remain in
// adapters around the two-phase Prepare/Dispatch boundary.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ErrorCode is a stable classification for dispatch failures.
type ErrorCode string

const (
	ErrInvalidRequest            ErrorCode = "invalid_request"
	ErrInvalidSelector           ErrorCode = "invalid_selector"
	ErrNoTargets                 ErrorCode = "no_targets"
	ErrOrdering                  ErrorCode = "target_ordering_failed"
	ErrMessageBuild              ErrorCode = "message_build_failed"
	ErrRedaction                 ErrorCode = "redaction_failed"
	ErrRedactionBlocked          ErrorCode = "redaction_blocked"
	ErrPromptDeliveryUnsupported ErrorCode = "prompt_delivery_unsupported"
	ErrProtocol                  ErrorCode = "protocol_failed"
	ErrLifecycle                 ErrorCode = "lifecycle_failed"
	ErrPacing                    ErrorCode = "pacing_failed"
	ErrDelivery                  ErrorCode = "delivery_failed"
	ErrAlreadyDispatched         ErrorCode = "already_dispatched"
	ErrPreparedByOtherSvc        ErrorCode = "prepared_by_other_service"
)

// Error carries a machine-readable failure code without exposing message
// contents. Target is set for pane-specific failures.
type Error struct {
	Code   ErrorCode
	Target *Target
	Err    error
}

type guaranteedNoDeliveryActuationError struct {
	err error
}

func (e *guaranteedNoDeliveryActuationError) Error() string { return e.err.Error() }
func (e *guaranteedNoDeliveryActuationError) Unwrap() error { return e.err }

// GuaranteeNoDeliveryActuation marks a delivery failure that happened before
// any byte of the requested message was typed into the target. Callers with a
// durable idempotency barrier may safely retry only errors carrying this mark.
func GuaranteeNoDeliveryActuation(err error) error {
	if err == nil {
		return nil
	}
	return &guaranteedNoDeliveryActuationError{err: err}
}

// IsGuaranteedNoDeliveryActuation reports whether retrying the requested
// message cannot duplicate a prior delivery.
func IsGuaranteedNoDeliveryActuation(err error) bool {
	var target *guaranteedNoDeliveryActuationError
	return errors.As(err, &target)
}

func (e *Error) Error() string {
	if e == nil {
		return "dispatch error"
	}
	if e.Target != nil {
		return fmt.Sprintf("dispatch %s for pane %s: %v", e.Code, e.Target.Address, e.Err)
	}
	return fmt.Sprintf("dispatch %s: %v", e.Code, e.Err)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// AgentFilter selects a canonical agent type and, optionally, a variant.
// Multiple filters are ORed; they are intersected with selectors and tags.
type AgentFilter struct {
	Type    tmux.AgentType
	Variant string
}

// Request is the complete neutral dispatch input. Panes must be the current
// topology snapshot for Session. Selectors use the shared N, W.P, or %N grammar.
type Request struct {
	Session               string
	Panes                 []tmux.Pane
	Selectors             []string
	RequireSingleSelector bool
	ExcludeSelectors      []string
	AgentFilters          []AgentFilter
	Tags                  []string
	IncludeUser           bool
	SkipFirst             bool
	Message               string
	AllowEmptyMessage     bool
	Submit                bool
	Delay                 time.Duration
	StopOnFailure         bool
	DryRun                bool
	// ClearInput performs the per-agent composer clear (Escape ritual + C-u)
	// and verifies emptiness before typing the prompt (ntm-5p0b / AP-16):
	// codex TUIs concatenate a fresh send onto leftover buffer text after an
	// interrupt, corrupting both.
	ClearInput bool
}

// Target is a resolved pane with a topology-safe response address.
type Target struct {
	Pane      tmux.Pane      `json:"-"`
	Ref       tmux.PaneRef   `json:"ref"`
	Address   string         `json:"address"`
	AgentType tmux.AgentType `json:"agent_type"`
	Variant   string         `json:"variant,omitempty"`
	Tags      []string       `json:"tags,omitempty"`
}

// DeliveryProtocol describes how the final message is submitted.
type DeliveryProtocol string

const (
	ProtocolStageOnly   DeliveryProtocol = "stage_only"
	ProtocolSingleEnter DeliveryProtocol = "single_enter"
	ProtocolDoubleEnter DeliveryProtocol = "double_enter"
)

// ProtocolPlan contains the protocol and its observable timing contract.
type ProtocolPlan struct {
	Protocol         DeliveryProtocol
	EnterDelay       time.Duration
	SecondEnterDelay time.Duration
}

// RedactionResult is the safe result returned by the final-message redaction
// port. Message is retained only inside Prepared and is never exposed by Result.
type RedactionResult struct {
	Message    string
	Mode       string
	Findings   int
	Categories map[string]int
	Warnings   []string
	Blocked    bool
}

// RedactionReceipt is safe to expose in command output.
type RedactionReceipt struct {
	Mode       string         `json:"mode,omitempty"`
	Findings   int            `json:"findings"`
	Categories map[string]int `json:"categories,omitempty"`
	Blocked    bool           `json:"blocked,omitempty"`
}

// Delivery is the fully prepared actuation passed to the delivery port.
type Delivery struct {
	Session          string
	Target           Target
	Message          string
	Protocol         DeliveryProtocol
	EnterDelay       time.Duration
	SecondEnterDelay time.Duration
	ClearInput       bool
}

// Pace describes one wait between two adjacent delivery attempts.
type Pace struct {
	Previous Target
	Next     Target
	Delay    time.Duration
	Ordinal  int
}

// ReceiptStatus is the terminal or preview state of a per-pane attempt.
type ReceiptStatus string

const (
	ReceiptPrepared  ReceiptStatus = "prepared"
	ReceiptDelivered ReceiptStatus = "delivered"
	ReceiptFailed    ReceiptStatus = "failed"
	ReceiptBlocked   ReceiptStatus = "blocked"
	ReceiptSkipped   ReceiptStatus = "skipped"
)

// Receipt records one target without retaining the outbound message.
type Receipt struct {
	Target           Target           `json:"target"`
	Status           ReceiptStatus    `json:"status"`
	Protocol         DeliveryProtocol `json:"protocol"`
	EnterDelay       time.Duration    `json:"enter_delay"`
	SecondEnterDelay time.Duration    `json:"second_enter_delay,omitempty"`
	Redaction        RedactionReceipt `json:"redaction"`
	Warnings         []string         `json:"warnings,omitempty"`
	Error            string           `json:"error,omitempty"`
}

// Result is the complete safe receipt envelope for a dispatch.
type Result struct {
	Success   bool      `json:"success"`
	DryRun    bool      `json:"dry_run,omitempty"`
	Targets   []Target  `json:"targets"`
	Receipts  []Receipt `json:"receipts"`
	Delivered int       `json:"delivered"`
	Failed    int       `json:"failed"`
	Blocked   int       `json:"blocked"`
	Skipped   int       `json:"skipped"`
	Warnings  []string  `json:"warnings,omitempty"`
}

// FinalMessageBuilder performs per-target enrichment before final redaction.
type FinalMessageBuilder interface {
	BuildFinalMessage(context.Context, BuildInput) (string, error)
}

// TargetOrderer controls actuation order after target planning. The default
// preserves deterministic topology order; callers requesting a seeded order
// must return an exact permutation of the planned targets.
type TargetOrderer interface {
	OrderTargets(context.Context, OrderInput) ([]Target, error)
}

// OrderInput is passed to TargetOrderer without exposing message content.
type OrderInput struct {
	Session string
	Targets []Target
}

// TargetOrdererFunc adapts a function to TargetOrderer.
type TargetOrdererFunc func(context.Context, OrderInput) ([]Target, error)

func (f TargetOrdererFunc) OrderTargets(ctx context.Context, input OrderInput) ([]Target, error) {
	return f(ctx, input)
}

type topologyOrderer struct{}

func (topologyOrderer) OrderTargets(_ context.Context, input OrderInput) ([]Target, error) {
	return cloneTargets(input.Targets), nil
}

// BuildInput is passed to FinalMessageBuilder.
type BuildInput struct {
	Session     string
	Target      Target
	BaseMessage string
}

// FinalMessageBuilderFunc adapts a function to FinalMessageBuilder.
type FinalMessageBuilderFunc func(context.Context, BuildInput) (string, error)

func (f FinalMessageBuilderFunc) BuildFinalMessage(ctx context.Context, in BuildInput) (string, error) {
	return f(ctx, in)
}

// FinalMessageRedactor applies policy to the actual final message after all
// per-target enrichment.
type FinalMessageRedactor interface {
	RedactFinalMessage(context.Context, Target, string) (RedactionResult, error)
}

// FinalMessageRedactorFunc adapts a function to FinalMessageRedactor.
type FinalMessageRedactorFunc func(context.Context, Target, string) (RedactionResult, error)

func (f FinalMessageRedactorFunc) RedactFinalMessage(ctx context.Context, target Target, message string) (RedactionResult, error) {
	return f(ctx, target, message)
}

// AllowAllRedactor is the explicit no-redaction policy. Callers must opt into
// it; a missing redaction port is rejected by NewService.
type AllowAllRedactor struct{}

func (AllowAllRedactor) RedactFinalMessage(_ context.Context, _ Target, message string) (RedactionResult, error) {
	return RedactionResult{Message: message, Mode: "off"}, nil
}

// ProtocolPlanner chooses the submission protocol for one target.
type ProtocolPlanner interface {
	PlanDelivery(context.Context, Target, bool) (ProtocolPlan, error)
}

// ProtocolPlannerFunc adapts a function to ProtocolPlanner.
type ProtocolPlannerFunc func(context.Context, Target, bool) (ProtocolPlan, error)

// DefaultProtocolPlanner mirrors NTM's established agent and shell protocols.
type DefaultProtocolPlanner struct{}

func (DefaultProtocolPlanner) PlanDelivery(_ context.Context, target Target, submit bool) (ProtocolPlan, error) {
	if !submit {
		return ProtocolPlan{Protocol: ProtocolStageOnly}, nil
	}
	canonical := target.AgentType.Canonical()
	switch {
	case canonical == tmux.AgentUser, canonical == tmux.AgentUnknown, !canonical.IsValid():
		return ProtocolPlan{Protocol: ProtocolSingleEnter, EnterDelay: tmux.ShellEnterDelay}, nil
	default:
		return ProtocolPlan{
			Protocol:         ProtocolDoubleEnter,
			EnterDelay:       tmux.DoubleEnterFirstDelay,
			SecondEnterDelay: tmux.DoubleEnterSecondDelay,
		}, nil
	}
}

// Deliverer actuates one fully prepared delivery.
type Deliverer interface {
	Deliver(context.Context, Delivery) error
}

// DelivererFunc adapts a function to Deliverer.
type DelivererFunc func(context.Context, Delivery) error

func (f DelivererFunc) Deliver(ctx context.Context, delivery Delivery) error {
	return f(ctx, delivery)
}

// TMUXDeliverer maps the neutral delivery protocol to NTM's tmux primitives.
type TMUXDeliverer struct{}

// validateTMUXDeliveryProtocol rejects malformed transport plans before any
// pane inspection or actuation. Service.Prepare normally validates plans, but
// TMUXDeliverer is also a public port implementation and must fail
// deterministically when called directly.
func validateTMUXDeliveryProtocol(delivery Delivery) error {
	if delivery.EnterDelay < 0 || delivery.SecondEnterDelay < 0 {
		return errors.New("tmux delivery delays cannot be negative")
	}
	switch delivery.Protocol {
	case ProtocolStageOnly:
		if delivery.EnterDelay != 0 || delivery.SecondEnterDelay != 0 {
			return errors.New("tmux stage-only protocol cannot include enter delays")
		}
	case ProtocolSingleEnter:
		if delivery.SecondEnterDelay != 0 {
			return errors.New("tmux single-enter protocol cannot include a second-enter delay")
		}
	case ProtocolDoubleEnter:
		if delivery.EnterDelay != tmux.DoubleEnterFirstDelay || delivery.SecondEnterDelay != tmux.DoubleEnterSecondDelay {
			return fmt.Errorf("tmux double-enter protocol requires delays %s and %s", tmux.DoubleEnterFirstDelay, tmux.DoubleEnterSecondDelay)
		}
	default:
		return fmt.Errorf("unsupported delivery protocol %q", delivery.Protocol)
	}
	return nil
}

// RefuseDeadAgentPane rejects delivery to an agent-typed pane whose CLI has
// exited back to a bare shell (ntm-0g0b): the keystrokes would land in zsh —
// possibly EXECUTING message text as shell commands — while the send reported
// success. User panes are exempt (a shell is their normal state), and the
// check is conservative (only definite shells refuse), so a live agent can
// never be false-refused.
func RefuseDeadAgentPane(pane tmux.Pane) error {
	if !pane.AgentCLIDead() {
		return nil
	}
	return fmt.Errorf(
		"PANE_AGENT_DEAD: pane %s is titled %s but its foreground process is %q (agent CLI exited to a bare shell); restart it with --robot-restart-pane, or target the user pane for deliberate shell input",
		pane.Ref().Physical(), pane.Title, pane.Command)
}

func (TMUXDeliverer) Deliver(ctx context.Context, delivery Delivery) error {
	if err := contextError(ctx); err != nil {
		return GuaranteeNoDeliveryActuation(err)
	}
	if err := validateTMUXDeliveryProtocol(delivery); err != nil {
		return GuaranteeNoDeliveryActuation(err)
	}
	if err := RefuseDeadAgentPane(delivery.Target.Pane); err != nil {
		return GuaranteeNoDeliveryActuation(err)
	}
	target := delivery.Target.Ref.ID
	if target == "" {
		target = fmt.Sprintf("%s:%s", delivery.Session, delivery.Target.Ref.Physical())
	}
	// Fail closed on panes that cannot accept typed input yet (bd-dp9oy):
	// a Claude/codex TUI still initializing, or showing a trust dialog or
	// menu instead of its input box, silently swallows pasted prompts while
	// the send reports success. The check fails open on capture problems and
	// unknown agent types, so it can only refuse when the screen positively
	// shows neither a composer nor working-state chrome.
	if ready, reason := tmux.ComposerReadyForDelivery(ctx, target, delivery.Target.AgentType, delivery.Target.Pane.Width); !ready {
		return GuaranteeNoDeliveryActuation(fmt.Errorf("pane %s not ready for delivery: %s", target, reason))
	}
	if err := ClearComposerForDelivery(ctx, target, delivery); err != nil {
		// ClearComposer may press control keys, but it never types the requested
		// message. Re-running the clear choreography cannot duplicate delivery.
		return GuaranteeNoDeliveryActuation(err)
	}
	switch delivery.Protocol {
	case ProtocolStageOnly:
		return tmux.SendKeysForAgentWithDelayContext(ctx, target, delivery.Message, false, 0, delivery.Target.AgentType)
	case ProtocolSingleEnter:
		return tmux.SendKeysForAgentWithDelayContext(ctx, target, delivery.Message, true, delivery.EnterDelay, delivery.Target.AgentType)
	case ProtocolDoubleEnter:
		if err := tmux.SendKeysForAgentDoubleEnterContext(ctx, target, delivery.Message, delivery.Target.AgentType); err != nil {
			return err
		}
		return VerifyAgentSubmission(ctx, target, delivery.Message, delivery.Target.AgentType, delivery.Target.Pane.Width)
	default:
		return GuaranteeNoDeliveryActuation(fmt.Errorf("unsupported delivery protocol %q", delivery.Protocol))
	}
}

// ClearComposerForDelivery runs the pre-send composer clear when the
// delivery requests it (ntm-5p0b). A composer that POSITIVELY still holds
// text after the clear is a distinct hard failure — typing into it would
// concatenate and corrupt both payloads — while an unverifiable clear
// (no marker visible) proceeds best-effort. User/unknown panes are skipped:
// C-u in an operator shell would kill whatever the human was typing.
func ClearComposerForDelivery(ctx context.Context, target string, delivery Delivery) error {
	if !delivery.ClearInput {
		return nil
	}
	canonical := delivery.Target.AgentType.Canonical()
	if canonical == tmux.AgentUser || canonical == tmux.AgentUnknown || !canonical.IsValid() {
		return nil
	}
	cleared, verified, err := tmux.ClearComposerContext(ctx, target, delivery.Target.AgentType)
	if err != nil {
		return fmt.Errorf("COMPOSER_CLEAR_FAILED: pane %s: %w", delivery.Target.Ref.Physical(), err)
	}
	if !cleared && verified {
		return fmt.Errorf("COMPOSER_NOT_CLEARED: pane %s composer still holds text after the clear sequence; inspect with --robot-tail before resending", delivery.Target.Ref.Physical())
	}
	return nil
}

// VerifyAgentSubmission closes the gap between "keys were sent" and "the
// prompt actually submitted" for agent TUIs that can strand a delivered
// prompt in their composer: codex consumes the double-Enter protocol's Enters
// as part of the bracketed paste (ntm-8ubn), and Claude Code's autocomplete
// picker swallows the submit Enter as a menu selection (GH#241 / ntm-utq6).
// The per-agent verifiers capture the visible screen, rescue a stranded
// composer (codex: one Enter; claude: Escape then Enter), and this wrapper
// fails the delivery loudly when submission remains unconfirmed so
// "delivered" keeps meaning "submitted". Verification infrastructure errors
// (a capture hiccup) are logged but do not fail an already-delivered send.
// Agent types without a verifier return nil immediately.
//
// paneWidth is the real tmux pane width threaded into the width-adaptive
// working detectors (bd-eeifh); pass 0 when unknown.
func VerifyAgentSubmission(ctx context.Context, target, message string, agentType tmux.AgentType, paneWidth int) error {
	var (
		confirmed, rescued bool
		err                error
		kind               string
	)
	switch agentType.Canonical() {
	case tmux.AgentCodex:
		kind = "codex"
		confirmed, rescued, err = tmux.VerifyCodexSubmissionContext(ctx, target, message, paneWidth)
	case tmux.AgentClaude:
		kind = "claude"
		confirmed, rescued, err = tmux.VerifyClaudeSubmissionContext(ctx, target, message, paneWidth)
	case tmux.AgentGrok:
		kind = "grok"
		confirmed, rescued, err = tmux.VerifyGrokSubmissionContext(ctx, target, message, paneWidth)
	default:
		return nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.Warn("agent submission verification inconclusive", "agent", kind, "target", target, "error", err)
		return nil
	}
	if !confirmed {
		return fmt.Errorf("%s submission unconfirmed: prompt still in composer after rescue (pane %s); submit manually or retry", kind, target)
	}
	if rescued {
		slog.Info("agent composer needed a rescue to submit", "agent", kind, "target", target)
	}
	return nil
}

// Pacer waits between adjacent delivery attempts.
type Pacer interface {
	Wait(context.Context, Pace) error
}

// LifecycleHooks are explicit orchestration boundaries around the neutral
// service. They let command adapters preserve audit, duplicate checks, hooks,
// checkpoints, history, events, and per-pane timeline behavior without moving
// those domain dependencies into this package.
type LifecycleHooks struct {
	RequestAccepted func(context.Context, Request) error
	TargetsPlanned  func(context.Context, Request, []Target) error
	Prepared        func(context.Context, Request, []Delivery) error
	BeforeDispatch  func(context.Context, Request, []Delivery) error
	AfterReceipt    func(context.Context, Delivery, Receipt)
	Finished        func(context.Context, Request, Result, error)
}

// PacerFunc adapts a function to Pacer.
type PacerFunc func(context.Context, Pace) error

// TimerPacer implements context-aware wall-clock pacing.
type TimerPacer struct{}

func (TimerPacer) Wait(ctx context.Context, pace Pace) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if pace.Delay <= 0 {
		return nil
	}
	timer := time.NewTimer(pace.Delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Ports are the injectable side-effect boundaries used by Service.
type Ports struct {
	Builder   FinalMessageBuilder
	Redactor  FinalMessageRedactor
	Orderer   TargetOrderer
	Protocols ProtocolPlanner
	Deliverer Deliverer
	Pacer     Pacer
	Lifecycle LifecycleHooks
}

// Service is a two-phase dispatch application service.
type Service struct {
	builder   FinalMessageBuilder
	redactor  FinalMessageRedactor
	orderer   TargetOrderer
	protocols ProtocolPlanner
	deliverer Deliverer
	pacer     Pacer
	lifecycle LifecycleHooks
}

// NewService validates mandatory policy and actuation ports and supplies pure
// defaults for identity building, protocol selection, and pacing.
func NewService(ports Ports) (*Service, error) {
	if ports.Redactor == nil {
		return nil, &Error{Code: ErrInvalidRequest, Err: errors.New("final-message redactor is required")}
	}
	if ports.Deliverer == nil {
		return nil, &Error{Code: ErrInvalidRequest, Err: errors.New("deliverer is required")}
	}
	if ports.Builder == nil {
		ports.Builder = FinalMessageBuilderFunc(func(_ context.Context, in BuildInput) (string, error) {
			return in.BaseMessage, nil
		})
	}
	if ports.Protocols == nil {
		ports.Protocols = DefaultProtocolPlanner{}
	}
	if ports.Orderer == nil {
		ports.Orderer = topologyOrderer{}
	}
	if ports.Pacer == nil {
		ports.Pacer = TimerPacer{}
	}
	return &Service{
		builder:   ports.Builder,
		redactor:  ports.Redactor,
		orderer:   ports.Orderer,
		protocols: ports.Protocols,
		deliverer: ports.Deliverer,
		pacer:     ports.Pacer,
		lifecycle: ports.Lifecycle,
	}, nil
}

type preparedDelivery struct {
	delivery Delivery
	receipt  Receipt
}

// Prepared is an immutable, preflighted dispatch. Its final messages are kept
// private and it may be dispatched at most once.
type Prepared struct {
	owner      *Service
	request    Request
	entries    []preparedDelivery
	dispatched atomic.Bool
}

// Targets returns a defensive copy of the prepared target plan.
func (p *Prepared) Targets() []Target {
	if p == nil {
		return []Target{}
	}
	targets := make([]Target, 0, len(p.entries))
	for _, entry := range p.entries {
		targets = append(targets, cloneTarget(entry.receipt.Target))
	}
	return targets
}

// FinalMessageForSingleTarget returns the already-redacted message for an
// exact one-target preflight. Atomic adapters use it before claiming work so
// blocked content cannot enter their durable ledger or cross a side-effect
// boundary. Multi-target callers must preflight each intent independently.
func (p *Prepared) FinalMessageForSingleTarget() (string, error) {
	if p == nil {
		return "", errors.New("prepared dispatch is required")
	}
	if len(p.entries) != 1 {
		return "", fmt.Errorf("prepared dispatch has %d targets, want exactly one", len(p.entries))
	}
	return p.entries[0].delivery.Message, nil
}

// Preview returns a safe dry-run result without exposing final messages.
func (p *Prepared) Preview() Result {
	if p == nil {
		return Result{DryRun: true, Targets: []Target{}, Receipts: []Receipt{}}
	}
	result := resultFromEntries(p.entries)
	result.DryRun = true
	result.Success = len(result.Targets) > 0 && result.Failed == 0 && result.Blocked == 0
	return result
}

// PreflightResult returns the current safe receipt state without claiming the
// operation was a dry run. It is useful when Prepare returns a blocked or
// otherwise rejected Prepared alongside an error.
func (p *Prepared) PreflightResult() Result {
	if p == nil {
		return Result{Targets: []Target{}, Receipts: []Receipt{}}
	}
	result := resultFromEntries(p.entries)
	result.DryRun = p.request.DryRun
	result.Success = false
	return result
}

// Prepare resolves targets and preflights every final message, redaction policy,
// and protocol before any delivery is attempted.
func (s *Service) Prepare(ctx context.Context, req Request) (prepared *Prepared, returnErr error) {
	if s == nil {
		return nil, &Error{Code: ErrInvalidRequest, Err: errors.New("nil service")}
	}
	if err := contextError(ctx); err != nil {
		return nil, &Error{Code: ErrInvalidRequest, Err: err}
	}
	if strings.TrimSpace(req.Session) == "" {
		return nil, &Error{Code: ErrInvalidRequest, Err: errors.New("session is required")}
	}
	if strings.TrimSpace(req.Message) == "" && !req.AllowEmptyMessage {
		return nil, &Error{Code: ErrInvalidRequest, Err: errors.New("message is required")}
	}
	if req.Delay < 0 {
		return nil, &Error{Code: ErrInvalidRequest, Err: errors.New("delay cannot be negative")}
	}
	lifecycleStarted := true
	defer func() {
		if returnErr != nil && lifecycleStarted && s.lifecycle.Finished != nil {
			result := Result{DryRun: req.DryRun, Targets: []Target{}, Receipts: []Receipt{}}
			if prepared != nil {
				result = prepared.PreflightResult()
			}
			s.lifecycle.Finished(ctx, cloneRequest(req), result, returnErr)
		}
	}()
	if s.lifecycle.RequestAccepted != nil {
		if err := s.lifecycle.RequestAccepted(ctx, cloneRequest(req)); err != nil {
			return nil, &Error{Code: ErrLifecycle, Err: fmt.Errorf("request accepted hook: %w", err)}
		}
	}

	targets, err := PlanTargets(req)
	if err != nil {
		return nil, err
	}
	if err := ValidatePromptDeliveryTargets(targets); err != nil {
		return nil, err
	}
	orderedTargets, err := s.orderer.OrderTargets(ctx, OrderInput{
		Session: req.Session,
		Targets: cloneTargets(targets),
	})
	if err != nil {
		return nil, &Error{Code: ErrOrdering, Err: err}
	}
	if err := validateTargetPermutation(targets, orderedTargets); err != nil {
		return nil, &Error{Code: ErrOrdering, Err: err}
	}
	targets = cloneTargets(orderedTargets)
	if s.lifecycle.TargetsPlanned != nil {
		if err := s.lifecycle.TargetsPlanned(ctx, cloneRequest(req), cloneTargets(targets)); err != nil {
			return nil, &Error{Code: ErrLifecycle, Err: fmt.Errorf("targets planned hook: %w", err)}
		}
	}
	prepared = &Prepared{
		owner:   s,
		request: cloneRequest(req),
		entries: make([]preparedDelivery, len(targets)),
	}
	for i, target := range targets {
		prepared.entries[i].receipt = Receipt{Target: cloneTarget(target), Status: ReceiptPrepared}
	}

	for i, target := range targets {
		if err := contextError(ctx); err != nil {
			return rejectPrepared(prepared, i, ErrMessageBuild, err, ReceiptFailed)
		}
		message, err := s.builder.BuildFinalMessage(ctx, BuildInput{
			Session:     req.Session,
			Target:      cloneTarget(target),
			BaseMessage: req.Message,
		})
		if err != nil {
			return rejectPrepared(prepared, i, ErrMessageBuild, err, ReceiptFailed)
		}
		if strings.TrimSpace(message) == "" && !req.AllowEmptyMessage {
			return rejectPrepared(prepared, i, ErrMessageBuild, errors.New("final message is empty"), ReceiptFailed)
		}

		redacted, err := s.redactor.RedactFinalMessage(ctx, cloneTarget(target), message)
		prepared.entries[i].receipt.Redaction = safeRedactionReceipt(redacted)
		prepared.entries[i].receipt.Warnings = append([]string(nil), redacted.Warnings...)
		if err != nil {
			return rejectPrepared(prepared, i, ErrRedaction, err, ReceiptFailed)
		}
		if err := validateRedactionResult(redacted); err != nil {
			return rejectPrepared(prepared, i, ErrRedaction, err, ReceiptFailed)
		}
		if redacted.Blocked {
			return rejectPrepared(prepared, i, ErrRedactionBlocked, errors.New("final message blocked by redaction policy"), ReceiptBlocked)
		}
		if strings.TrimSpace(redacted.Message) == "" && !req.AllowEmptyMessage {
			return rejectPrepared(prepared, i, ErrRedaction, errors.New("redactor returned an empty final message"), ReceiptFailed)
		}

		plan, err := s.protocols.PlanDelivery(ctx, cloneTarget(target), req.Submit)
		if err != nil {
			return rejectPrepared(prepared, i, ErrProtocol, err, ReceiptFailed)
		}
		if err := validateProtocolPlan(plan, req.Submit); err != nil {
			return rejectPrepared(prepared, i, ErrProtocol, err, ReceiptFailed)
		}

		prepared.entries[i].delivery = Delivery{
			Session:          req.Session,
			Target:           cloneTarget(target),
			Message:          redacted.Message,
			Protocol:         plan.Protocol,
			EnterDelay:       plan.EnterDelay,
			SecondEnterDelay: plan.SecondEnterDelay,
			ClearInput:       req.ClearInput,
		}
		prepared.entries[i].receipt.Protocol = plan.Protocol
		prepared.entries[i].receipt.EnterDelay = plan.EnterDelay
		prepared.entries[i].receipt.SecondEnterDelay = plan.SecondEnterDelay
	}
	if s.lifecycle.Prepared != nil {
		if err := s.lifecycle.Prepared(ctx, cloneRequest(req), deliveriesFromEntries(prepared.entries)); err != nil {
			return prepared, &Error{Code: ErrLifecycle, Err: fmt.Errorf("prepared hook: %w", err)}
		}
	}

	return prepared, nil
}

func rejectPrepared(prepared *Prepared, index int, code ErrorCode, err error, status ReceiptStatus) (*Prepared, error) {
	target := cloneTarget(prepared.entries[index].receipt.Target)
	prepared.entries[index].receipt.Status = status
	prepared.entries[index].receipt.Error = err.Error()
	for i := range prepared.entries {
		if i != index && prepared.entries[i].receipt.Status == ReceiptPrepared {
			prepared.entries[i].receipt.Status = ReceiptSkipped
		}
	}
	return prepared, &Error{Code: code, Target: &target, Err: err}
}

// Dispatch actuates a prepared request once. Delivery failures are represented
// both in Result receipts and by the returned typed error.
func (s *Service) Dispatch(ctx context.Context, prepared *Prepared) (result Result, returnErr error) {
	if s == nil || prepared == nil {
		return Result{Targets: []Target{}, Receipts: []Receipt{}}, &Error{Code: ErrInvalidRequest, Err: errors.New("prepared dispatch is required")}
	}
	if prepared.owner != s {
		return prepared.PreflightResult(), &Error{Code: ErrPreparedByOtherSvc, Err: errors.New("prepared dispatch belongs to another service")}
	}
	if !prepared.dispatched.CompareAndSwap(false, true) {
		return prepared.PreflightResult(), &Error{Code: ErrAlreadyDispatched, Err: errors.New("prepared dispatch was already used")}
	}
	defer func() {
		if s.lifecycle.Finished != nil {
			s.lifecycle.Finished(ctx, cloneRequest(prepared.request), result, returnErr)
		}
	}()
	if prepared.request.DryRun {
		return prepared.Preview(), nil
	}
	if err := contextError(ctx); err != nil {
		markRemainingSkipped(prepared.entries, 0, err)
		result = resultFromEntries(prepared.entries)
		return result, &Error{Code: ErrDelivery, Err: err}
	}
	if s.lifecycle.BeforeDispatch != nil {
		if err := s.lifecycle.BeforeDispatch(ctx, cloneRequest(prepared.request), deliveriesFromEntries(prepared.entries)); err != nil {
			markRemainingSkipped(prepared.entries, 0, err)
			result = resultFromEntries(prepared.entries)
			return result, &Error{Code: ErrLifecycle, Err: fmt.Errorf("before dispatch hook: %w", err)}
		}
	}

	var firstErr error
	for i := range prepared.entries {
		if i > 0 && prepared.request.Delay > 0 {
			pace := Pace{
				Previous: cloneTarget(prepared.entries[i-1].receipt.Target),
				Next:     cloneTarget(prepared.entries[i].receipt.Target),
				Delay:    prepared.request.Delay,
				Ordinal:  i,
			}
			if err := s.pacer.Wait(ctx, pace); err != nil {
				prepared.entries[i].receipt.Status = ReceiptFailed
				prepared.entries[i].receipt.Error = err.Error()
				s.notifyReceipt(ctx, prepared.entries[i].delivery, prepared.entries[i].receipt)
				markRemainingSkipped(prepared.entries, i+1, err)
				target := cloneTarget(prepared.entries[i].receipt.Target)
				firstErr = &Error{Code: ErrPacing, Target: &target, Err: err}
				break
			}
		}

		entry := &prepared.entries[i]
		if err := s.deliverer.Deliver(ctx, cloneDelivery(entry.delivery)); err != nil {
			entry.receipt.Status = ReceiptFailed
			entry.receipt.Error = err.Error()
			if firstErr == nil {
				target := cloneTarget(entry.receipt.Target)
				firstErr = &Error{Code: ErrDelivery, Target: &target, Err: err}
			}
			if prepared.request.StopOnFailure {
				s.notifyReceipt(ctx, entry.delivery, entry.receipt)
				markRemainingSkipped(prepared.entries, i+1, err)
				break
			}
			s.notifyReceipt(ctx, entry.delivery, entry.receipt)
			continue
		}
		entry.receipt.Status = ReceiptDelivered
		s.notifyReceipt(ctx, entry.delivery, entry.receipt)
	}

	result = resultFromEntries(prepared.entries)
	result.Success = result.Delivered == len(result.Targets) && result.Delivered > 0
	return result, firstErr
}

func (s *Service) notifyReceipt(ctx context.Context, delivery Delivery, receipt Receipt) {
	if s.lifecycle.AfterReceipt != nil {
		s.lifecycle.AfterReceipt(ctx, cloneDelivery(delivery), cloneReceipt(receipt))
	}
}

// Execute is the one-shot convenience around Prepare and Dispatch.
func (s *Service) Execute(ctx context.Context, req Request) (Result, error) {
	prepared, err := s.Prepare(ctx, req)
	if err != nil {
		if prepared != nil {
			return prepared.PreflightResult(), err
		}
		return Result{DryRun: req.DryRun, Targets: []Target{}, Receipts: []Receipt{}}, err
	}
	return s.Dispatch(ctx, prepared)
}

// PlanTargets validates and resolves Request's target policy without performing
// message or I/O work.
func PlanTargets(req Request) ([]Target, error) {
	if len(req.Panes) == 0 {
		return nil, &Error{Code: ErrNoTargets, Err: errors.New("no panes are available")}
	}
	if req.SkipFirst && len(req.Selectors) > 0 {
		return nil, &Error{Code: ErrInvalidRequest, Err: errors.New("skip-first cannot be combined with explicit selectors")}
	}
	if req.RequireSingleSelector && len(req.Selectors) != 1 {
		return nil, &Error{Code: ErrInvalidRequest, Err: errors.New("single-pane selection requires exactly one selector")}
	}

	panes, err := canonicalizePanes(req.Panes)
	if err != nil {
		return nil, err
	}
	multiWindow := tmux.PanesSpanMultipleWindows(panes)

	selected := panes
	if len(req.Selectors) > 0 {
		selected, err = tmux.ResolvePaneSelectors(panes, req.Selectors, req.RequireSingleSelector)
		if err != nil {
			return nil, &Error{Code: ErrInvalidSelector, Err: err}
		}
	} else if req.SkipFirst {
		selected = selected[1:]
	}

	excluded := make(map[string]struct{})
	if len(req.ExcludeSelectors) > 0 {
		excludedPanes, resolveErr := tmux.ResolvePaneSelectors(panes, req.ExcludeSelectors, false)
		if resolveErr != nil {
			return nil, &Error{Code: ErrInvalidSelector, Err: resolveErr}
		}
		for _, pane := range excludedPanes {
			excluded[pane.Ref().StableKey()] = struct{}{}
		}
	}

	filters, err := normalizeAgentFilters(req.AgentFilters)
	if err != nil {
		return nil, err
	}
	tags, err := normalizeTags(req.Tags)
	if err != nil {
		return nil, err
	}
	explicitPolicy := len(req.Selectors) > 0 || len(filters) > 0 || len(tags) > 0

	targets := make([]Target, 0, len(selected))
	seen := make(map[string]struct{}, len(selected))
	for _, pane := range selected {
		stableKey := pane.Ref().StableKey()
		if _, skip := excluded[stableKey]; skip {
			continue
		}
		if len(filters) > 0 && !matchesAgentFilters(pane, filters) {
			continue
		}
		if len(tags) > 0 && !hasAnyTag(pane.Tags, tags) {
			continue
		}
		if !req.IncludeUser && !explicitPolicy && pane.Type.Canonical() == tmux.AgentUser {
			continue
		}
		if _, duplicate := seen[stableKey]; duplicate {
			continue
		}
		seen[stableKey] = struct{}{}
		targets = append(targets, targetFromPane(pane, multiWindow))
	}
	if len(targets) == 0 {
		return nil, &Error{Code: ErrNoTargets, Err: errors.New("no panes matched the target policy")}
	}
	return targets, nil
}

// ValidatePromptDeliveryTargets rejects agents whose prompt-delivery protocol
// is not implemented. Callers that combine prompt delivery with another
// actuation can use this pure preflight before performing that actuation.
func ValidatePromptDeliveryTargets(targets []Target) error {
	for _, target := range targets {
		err := target.AgentType.ValidateAutomatedPromptDelivery()
		if err == nil {
			continue
		}
		unsupported := cloneTarget(target)
		return &Error{
			Code:   ErrPromptDeliveryUnsupported,
			Target: &unsupported,
			Err:    err,
		}
	}
	return nil
}

func canonicalizePanes(input []tmux.Pane) ([]tmux.Pane, error) {
	ordered := tmux.SortPanesByTopology(input)
	unique := make([]tmux.Pane, 0, len(ordered))
	seen := make(map[string]tmux.Pane, len(ordered))
	for _, pane := range ordered {
		key := pane.Ref().StableKey()
		if prior, ok := seen[key]; ok {
			if !samePaneSnapshot(prior, pane) {
				return nil, &Error{Code: ErrInvalidRequest, Err: fmt.Errorf("conflicting pane snapshots share identity %q", key)}
			}
			continue
		}
		seen[key] = pane
		unique = append(unique, clonePane(pane))
	}
	return unique, nil
}

func samePaneSnapshot(a, b tmux.Pane) bool {
	if a.ID != b.ID || a.Index != b.Index || a.WindowIndex != b.WindowIndex || a.NTMIndex != b.NTMIndex ||
		a.Title != b.Title || a.Type != b.Type || a.Variant != b.Variant || a.Command != b.Command ||
		a.Width != b.Width || a.Height != b.Height || a.Active != b.Active || a.PID != b.PID || len(a.Tags) != len(b.Tags) {
		return false
	}
	for i := range a.Tags {
		if a.Tags[i] != b.Tags[i] {
			return false
		}
	}
	return true
}

func normalizeAgentFilters(input []AgentFilter) ([]AgentFilter, error) {
	result := make([]AgentFilter, 0, len(input))
	seen := make(map[string]struct{}, len(input))
	for _, filter := range input {
		canonical := filter.Type.Canonical()
		if canonical == "" || (!canonical.IsValid() && canonical != tmux.AgentUnknown) {
			return nil, &Error{Code: ErrInvalidRequest, Err: fmt.Errorf("unknown agent type %q", filter.Type)}
		}
		variant := strings.TrimSpace(filter.Variant)
		key := string(canonical) + "\x00" + strings.ToLower(variant)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, AgentFilter{Type: canonical, Variant: variant})
	}
	return result, nil
}

func normalizeTags(input []string) ([]string, error) {
	result := make([]string, 0, len(input))
	seen := make(map[string]struct{}, len(input))
	for _, raw := range input {
		tag := strings.TrimSpace(raw)
		if tag == "" {
			return nil, &Error{Code: ErrInvalidRequest, Err: errors.New("tag filters cannot be empty")}
		}
		key := strings.ToLower(tag)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	sort.Strings(result)
	return result, nil
}

func matchesAgentFilters(pane tmux.Pane, filters []AgentFilter) bool {
	typeName := pane.Type.Canonical()
	for _, filter := range filters {
		if typeName != filter.Type {
			continue
		}
		if filter.Variant == "" || strings.EqualFold(strings.TrimSpace(pane.Variant), filter.Variant) {
			return true
		}
	}
	return false
}

func hasAnyTag(paneTags, filters []string) bool {
	for _, paneTag := range paneTags {
		for _, filter := range filters {
			if strings.EqualFold(strings.TrimSpace(paneTag), filter) {
				return true
			}
		}
	}
	return false
}

func validateProtocolPlan(plan ProtocolPlan, submit bool) error {
	if plan.EnterDelay < 0 || plan.SecondEnterDelay < 0 {
		return errors.New("protocol delays cannot be negative")
	}
	switch plan.Protocol {
	case ProtocolStageOnly:
		if submit {
			return errors.New("stage-only protocol conflicts with submit=true")
		}
		if plan.EnterDelay != 0 || plan.SecondEnterDelay != 0 {
			return errors.New("stage-only protocol cannot include enter delays")
		}
	case ProtocolSingleEnter:
		if !submit {
			return errors.New("single-enter protocol conflicts with submit=false")
		}
		if plan.SecondEnterDelay != 0 {
			return errors.New("single-enter protocol cannot include a second-enter delay")
		}
	case ProtocolDoubleEnter:
		if !submit {
			return errors.New("double-enter protocol conflicts with submit=false")
		}
	default:
		return fmt.Errorf("unknown delivery protocol %q", plan.Protocol)
	}
	return nil
}

func validateRedactionResult(result RedactionResult) error {
	if result.Findings < 0 {
		return errors.New("redaction findings cannot be negative")
	}
	for category, count := range result.Categories {
		if strings.TrimSpace(category) == "" || count <= 0 {
			return errors.New("redaction categories require non-empty names and positive counts")
		}
	}
	return nil
}

func validateTargetPermutation(planned, ordered []Target) error {
	if len(ordered) != len(planned) {
		return fmt.Errorf("target order returned %d targets, want %d", len(ordered), len(planned))
	}
	want := make(map[string]int, len(planned))
	for _, target := range planned {
		want[target.Ref.StableKey()]++
	}
	for _, target := range ordered {
		key := target.Ref.StableKey()
		if want[key] == 0 {
			return fmt.Errorf("target order contains unknown or duplicate pane %q", key)
		}
		want[key]--
	}
	for key, count := range want {
		if count != 0 {
			return fmt.Errorf("target order omitted pane %q", key)
		}
	}
	return nil
}

func markRemainingSkipped(entries []preparedDelivery, start int, cause error) {
	for i := start; i < len(entries); i++ {
		if entries[i].receipt.Status == ReceiptPrepared {
			entries[i].receipt.Status = ReceiptSkipped
			entries[i].receipt.Error = cause.Error()
		}
	}
}

func resultFromEntries(entries []preparedDelivery) Result {
	result := Result{
		Targets:  make([]Target, 0, len(entries)),
		Receipts: make([]Receipt, 0, len(entries)),
	}
	warningSeen := make(map[string]struct{})
	for _, entry := range entries {
		receipt := cloneReceipt(entry.receipt)
		result.Targets = append(result.Targets, cloneTarget(receipt.Target))
		result.Receipts = append(result.Receipts, receipt)
		switch receipt.Status {
		case ReceiptDelivered:
			result.Delivered++
		case ReceiptFailed:
			result.Failed++
		case ReceiptBlocked:
			result.Blocked++
		case ReceiptSkipped:
			result.Skipped++
		}
		for _, warning := range receipt.Warnings {
			if _, ok := warningSeen[warning]; ok {
				continue
			}
			warningSeen[warning] = struct{}{}
			result.Warnings = append(result.Warnings, warning)
		}
	}
	if result.Warnings == nil {
		result.Warnings = []string{}
	}
	return result
}

func safeRedactionReceipt(result RedactionResult) RedactionReceipt {
	categories := make(map[string]int, len(result.Categories))
	for category, count := range result.Categories {
		categories[category] = count
	}
	if len(categories) == 0 {
		categories = nil
	}
	return RedactionReceipt{
		Mode:       result.Mode,
		Findings:   result.Findings,
		Categories: categories,
		Blocked:    result.Blocked,
	}
}

func targetFromPane(pane tmux.Pane, multiWindow bool) Target {
	copy := clonePane(pane)
	return Target{
		Pane:      copy,
		Ref:       copy.Ref(),
		Address:   copy.Ref().Canonical(multiWindow),
		AgentType: copy.Type.Canonical(),
		Variant:   copy.Variant,
		Tags:      append([]string(nil), copy.Tags...),
	}
}

func clonePane(pane tmux.Pane) tmux.Pane {
	pane.Tags = append([]string(nil), pane.Tags...)
	return pane
}

func cloneTarget(target Target) Target {
	target.Pane = clonePane(target.Pane)
	target.Tags = append([]string(nil), target.Tags...)
	return target
}

func cloneTargets(targets []Target) []Target {
	result := make([]Target, len(targets))
	for i := range targets {
		result[i] = cloneTarget(targets[i])
	}
	return result
}

func cloneDelivery(delivery Delivery) Delivery {
	delivery.Target = cloneTarget(delivery.Target)
	return delivery
}

func deliveriesFromEntries(entries []preparedDelivery) []Delivery {
	deliveries := make([]Delivery, len(entries))
	for i := range entries {
		deliveries[i] = cloneDelivery(entries[i].delivery)
	}
	return deliveries
}

func cloneReceipt(receipt Receipt) Receipt {
	receipt.Target = cloneTarget(receipt.Target)
	receipt.Warnings = append([]string(nil), receipt.Warnings...)
	receipt.Redaction = safeRedactionReceipt(RedactionResult{
		Mode:       receipt.Redaction.Mode,
		Findings:   receipt.Redaction.Findings,
		Categories: receipt.Redaction.Categories,
		Blocked:    receipt.Redaction.Blocked,
	})
	return receipt
}

func cloneRequest(req Request) Request {
	req.Panes = append([]tmux.Pane(nil), req.Panes...)
	for i := range req.Panes {
		req.Panes[i] = clonePane(req.Panes[i])
	}
	req.Selectors = append([]string(nil), req.Selectors...)
	req.ExcludeSelectors = append([]string(nil), req.ExcludeSelectors...)
	req.AgentFilters = append([]AgentFilter(nil), req.AgentFilters...)
	req.Tags = append([]string(nil), req.Tags...)
	return req
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

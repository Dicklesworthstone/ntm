package assignment

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
)

// DispatchState records the durable boundary reached by an atomic assignment.
type DispatchState string

const (
	DispatchPending DispatchState = "pending"
	DispatchSending DispatchState = "sending"
	DispatchSent    DispatchState = "sent"
)

// ClaimState records the crash-recovery state of the external Beads claim.
type ClaimState string

const (
	ClaimPending    ClaimState = "pending"
	ClaimClaiming   ClaimState = "claiming"
	ClaimClaimed    ClaimState = "claimed"
	ClaimFailed     ClaimState = "failed"
	ClaimIneligible ClaimState = "ineligible"
	ClaimUnknown    ClaimState = "unknown"
)

// ReservationState records the crash-recovery state of the Agent Mail lease.
type ReservationState string

const (
	ReservationPending   ReservationState = "pending"
	ReservationReserving ReservationState = "reserving"
	ReservationReserved  ReservationState = "reserved"
	ReservationFailed    ReservationState = "failed"
	ReservationUnknown   ReservationState = "unknown"
	ReservationReleased  ReservationState = "released"
)

var (
	// ErrClaimConflict means another actor owns the bead.
	ErrClaimConflict = errors.New("bead claim is owned by another actor")
	// ErrClaimIneligible means the tracker atomically rejected a new assignment
	// because the work item is not open and ready for automated dispatch.
	ErrClaimIneligible = errors.New("bead is not eligible for automated assignment")
	// ErrReservationRequired means the assignment intent requires Agent Mail
	// reservation, but no reservation port is available to enforce it.
	ErrReservationRequired = errors.New("file reservation is required but unavailable")
	// ErrReservationPathsRequired means a required reservation did not define
	// explicit paths and did not opt into reservation-port path discovery.
	ErrReservationPathsRequired = errors.New("file reservation is required but no reservation paths were defined")
	// ErrReservationOutcomeUnknown means a process stopped while acquiring a
	// lease and no reconciliation port could prove whether it was created.
	ErrReservationOutcomeUnknown = errors.New("file reservation outcome is unknown")
	// ErrReservationReleaseRequired means a reservation call returned durable
	// lease handles but the lease is not valid for dispatch. The handles must be
	// reconciled or released before retrying or replacing the assignment.
	ErrReservationReleaseRequired = errors.New("file reservation must be reconciled or released")
	// ErrClaimOutcomeUnknown means the durable claim barrier could not be
	// reconciled with the external tracker.
	ErrClaimOutcomeUnknown = errors.New("bead claim outcome is unknown")
	// ErrDispatchOutcomeUnknown means a prior process stopped after recording the
	// send intent but before recording the transport result. Retrying blindly
	// could duplicate the prompt.
	ErrDispatchOutcomeUnknown = errors.New("assignment dispatch outcome is unknown")
	// ErrTargetOccupied means another active assignment owns the exact pane or
	// mail delivery target. It is detected before claiming or dispatching.
	ErrTargetOccupied = errors.New("assignment target already has active work")
	// ErrPaneIdentityMigrationRequired means a durable assignment predates the
	// canonical pane identity fields required for unambiguous physical targeting.
	ErrPaneIdentityMigrationRequired = errors.New("assignment requires canonical pane identity migration")
	// ErrTerminalAssignmentAttempt means a completed assignment generation was
	// retried with its old idempotency key. Reopened work must start a new
	// generation so it receives a new durable dispatch receipt.
	ErrTerminalAssignmentAttempt = errors.New("assignment generation is terminal; reopen the work item and use a new idempotency key")
	// ErrCompletionEventPending means a terminal generation still owns an
	// unacknowledged completion event. A reopened generation cannot replace the
	// durable row until the exact event consumer acknowledges that outbox entry.
	ErrCompletionEventPending = errors.New("assignment completion event is pending acknowledgement")
	// ErrWorkingReplacementNotAllowed means a caller requested an atomic
	// reassignment without establishing the exact durable handoff barrier: the
	// same bead must still be actionable and working, and its old leases must be
	// released or reconciled before the replacement generation is persisted.
	ErrWorkingReplacementNotAllowed = errors.New("atomic replacement requires the same working assignment with released leases")
)

// PaneIdentityMigrationError identifies a durable assignment that cannot be
// acted on until its ambiguous pane reference is migrated to a physical tmux
// pane ID. Topology addresses such as window.pane are routing metadata only and
// are never durable occupancy identities.
type PaneIdentityMigrationError struct {
	BeadID         string
	Pane           int
	OccupancyKey   string
	DispatchTarget string
}

func (e *PaneIdentityMigrationError) Error() string {
	if e == nil {
		return ErrPaneIdentityMigrationRequired.Error()
	}
	beadID := strings.TrimSpace(e.BeadID)
	if beadID == "" {
		beadID = "<unknown>"
	}
	return fmt.Sprintf("%s: assignment %s has no physical tmux pane ID (legacy pane index %d); migrate occupancy_key or dispatch_target to a %%N tmux pane ID", ErrPaneIdentityMigrationRequired, beadID, e.Pane)
}

func (e *PaneIdentityMigrationError) Unwrap() error {
	return ErrPaneIdentityMigrationRequired
}

type guaranteedNoActuationError struct {
	err error
}

type guaranteedNoReservationError struct {
	err error
}

type reservationReleaseRequiredError struct {
	err error
}

func (e *guaranteedNoActuationError) Error() string { return e.err.Error() }
func (e *guaranteedNoActuationError) Unwrap() error { return e.err }

// GuaranteeNoActuation marks a dispatch failure that is known to have happened
// before the transport attempted delivery. Only these errors are retryable.
func GuaranteeNoActuation(err error) error {
	if err == nil {
		return nil
	}
	return &guaranteedNoActuationError{err: err}
}

// IsGuaranteedNoActuation reports whether retrying cannot duplicate delivery.
func IsGuaranteedNoActuation(err error) bool {
	var target *guaranteedNoActuationError
	return errors.As(err, &target)
}

func (e *guaranteedNoReservationError) Error() string { return e.err.Error() }
func (e *guaranteedNoReservationError) Unwrap() error { return e.err }

// GuaranteeNoReservation marks a reservation failure that is known to have
// happened before the service created or renewed a lease.
func GuaranteeNoReservation(err error) error {
	if err == nil {
		return nil
	}
	return &guaranteedNoReservationError{err: err}
}

// IsGuaranteedNoReservation reports whether a failed reservation can be
// retried without first inspecting the remote service.
func IsGuaranteedNoReservation(err error) bool {
	var target *guaranteedNoReservationError
	return errors.As(err, &target)
}

func (e *reservationReleaseRequiredError) Error() string { return e.err.Error() }
func (e *reservationReleaseRequiredError) Unwrap() error { return e.err }

// RequireReservationRelease marks a reservation failure that is known to
// have created or retained an external lease. Adapters should return every
// available lease handle alongside this marker so clear can release it.
func RequireReservationRelease(err error) error {
	if err == nil {
		return &reservationReleaseRequiredError{err: ErrReservationReleaseRequired}
	}
	if IsReservationReleaseRequired(err) {
		return err
	}
	return &reservationReleaseRequiredError{err: errors.Join(ErrReservationReleaseRequired, err)}
}

// IsReservationReleaseRequired reports whether retrying Reserve would risk
// leaking or duplicating a known external lease.
func IsReservationReleaseRequired(err error) bool {
	var target *reservationReleaseRequiredError
	return errors.Is(err, ErrReservationReleaseRequired) || errors.As(err, &target)
}

// ClaimReceipt is the durable result of br's atomic claim transaction.
type ClaimReceipt struct {
	BeadID    string
	Actor     string
	Status    string
	ClaimedAt time.Time
}

// ClaimPort atomically claims a bead. Production implementations must use
// `br update <id> --claim --actor <actor> --json` rather than a read/update pair.
type ClaimPort interface {
	Claim(context.Context, string, string) (ClaimReceipt, error)
}

// ClaimReconciliationState is the result of inspecting a durable claim barrier.
type ClaimReconciliationState string

const (
	ClaimReconciliationAbsent   ClaimReconciliationState = "absent"
	ClaimReconciliationOwned    ClaimReconciliationState = "owned"
	ClaimReconciliationConflict ClaimReconciliationState = "conflict"
	ClaimReconciliationUnknown  ClaimReconciliationState = "unknown"
)

// ClaimReconciliation reports whether the intended actor already owns the
// claim. Receipt is required when State is owned.
type ClaimReconciliation struct {
	State   ClaimReconciliationState
	Receipt ClaimReceipt
}

// ClaimReconciliationPort is an optional capability implemented by claim
// adapters that can inspect tracker ownership after a crash. ClaimPort itself
// must still make repeated claims by the same actor idempotent.
type ClaimReconciliationPort interface {
	ReconcileClaim(context.Context, string, string) (ClaimReconciliation, error)
}

// ClaimFunc adapts a function to ClaimPort.
type ClaimFunc func(context.Context, string, string) (ClaimReceipt, error)

func (f ClaimFunc) Claim(ctx context.Context, beadID, actor string) (ClaimReceipt, error) {
	return f(ctx, beadID, actor)
}

// ReservationRequest contains the claim-owned handoff information needed to
// reserve files before dispatch.
type ReservationRequest struct {
	BeadID         string
	BeadTitle      string
	AgentName      string
	Target         string
	RequestedPaths []string
	TTL            time.Duration
}

// LeaseReceipt captures Agent Mail reservation metadata needed for renewal,
// release, and crash recovery.
type LeaseReceipt struct {
	AgentName      string
	Target         string
	Requested      []string
	Granted        []string
	ReservationIDs []int
	ExpiresAt      *time.Time
}

// ReservationPort reserves the edit surface after the bead is claimed and
// before the assignment is delivered.
type ReservationPort interface {
	Reserve(context.Context, ReservationRequest) (LeaseReceipt, error)
}

// ReservationReconciliationState is the result of inspecting a durable
// reservation barrier.
type ReservationReconciliationState string

const (
	ReservationReconciliationAbsent   ReservationReconciliationState = "absent"
	ReservationReconciliationReserved ReservationReconciliationState = "reserved"
	ReservationReconciliationUnknown  ReservationReconciliationState = "unknown"
)

// ReservationReconciliation reports whether the intended lease exists.
type ReservationReconciliation struct {
	State ReservationReconciliationState
	Lease LeaseReceipt
}

// ReservationReconciliationPort is an optional capability. A retry never
// repeats an ambiguous Reserve call unless this inspection proves absence.
type ReservationReconciliationPort interface {
	ReconcileReservation(context.Context, ReservationRequest, LeaseReceipt) (ReservationReconciliation, error)
}

// ReservationFunc adapts a function to ReservationPort.
type ReservationFunc func(context.Context, ReservationRequest) (LeaseReceipt, error)

// Reserve implements ReservationPort. Test-only hook: retained because tests
// in other packages (notably internal/coordinator) use ReservationFunc as a
// fake ReservationPort; production code never converts ReservationFunc.
func (f ReservationFunc) Reserve(ctx context.Context, req ReservationRequest) (LeaseReceipt, error) {
	return f(ctx, req)
}

// DispatchRequest is the transport-neutral assignment payload.
type DispatchRequest struct {
	BeadID         string
	BeadTitle      string
	Target         string
	Pane           int
	AgentType      string
	AgentName      string
	Prompt         string
	IdempotencyKey string
	// RequestedPaths are exposed to preflight so production policy can reject
	// credential-bearing reservation paths before they are persisted or sent to
	// Agent Mail. Dispatch adapters must not treat them as transport payload.
	RequestedPaths []string
}

// DispatchReceipt identifies the completed transport operation.
type DispatchReceipt struct {
	DeliveryID string
	Duration   time.Duration
}

// DispatchPort delivers an already-claimed, already-reserved assignment.
type DispatchPort interface {
	Dispatch(context.Context, DispatchRequest) (DispatchReceipt, error)
}

// DispatchFunc adapts a function to DispatchPort.
type DispatchFunc func(context.Context, DispatchRequest) (DispatchReceipt, error)

func (f DispatchFunc) Dispatch(ctx context.Context, req DispatchRequest) (DispatchReceipt, error) {
	return f(ctx, req)
}

// PromptPreflightResult separates the configured transport payload from the
// sanitized payload that may be written to the durable assignment ledger.
type PromptPreflightResult struct {
	DispatchPrompt string
	DurablePrompt  string
	// DurableTitle is the sanitized work-item title safe for persistence and
	// transport metadata. Empty preserves the supplied title for adapters that
	// do not transform titles.
	DurableTitle string
}

// PromptPreflightPort validates and redacts the final target-specific prompt.
// Implementations must not actuate transport or mutate external state.
type PromptPreflightPort interface {
	Preflight(context.Context, DispatchRequest) (PromptPreflightResult, error)
}

// PromptPreflightFunc adapts a function to PromptPreflightPort.
type PromptPreflightFunc func(context.Context, DispatchRequest) (PromptPreflightResult, error)

func (f PromptPreflightFunc) Preflight(ctx context.Context, req DispatchRequest) (PromptPreflightResult, error) {
	return f(ctx, req)
}

// WorkItemStatusPort proves that terminal work was reopened in the external
// tracker before a new assignment generation replaces its durable receipt.
type WorkItemStatusPort interface {
	WorkItemStatus(context.Context, string) (string, error)
}

// WorkItemStatusFunc adapts a function to WorkItemStatusPort.
type WorkItemStatusFunc func(context.Context, string) (string, error)

func (f WorkItemStatusFunc) WorkItemStatus(ctx context.Context, beadID string) (string, error) {
	return f(ctx, beadID)
}

// AssignmentEligibilityAuthorizationRequest describes the exact tracker state
// an atomic generation may accept at its pre-mutation authorization boundary.
// Production adapters must still perform an atomic claim compare-and-set after
// this read-only proof because tracker state can change between the two calls.
type AssignmentEligibilityAuthorizationRequest struct {
	BeadID               string
	ClaimActor           string
	AllowUnassignedOpen  bool
	AllowOwnedOpen       bool
	AllowOwnedInProgress bool
}

// AssignmentEligibilityAuthorizationPort proves that every live automation
// gate still permits an unsent or replacement assignment generation. The port
// must not mutate tracker, lease, transport, or ledger state.
type AssignmentEligibilityAuthorizationPort interface {
	AuthorizeAssignment(context.Context, AssignmentEligibilityAuthorizationRequest) error
}

// AssignmentEligibilityAuthorizationFunc adapts a function to the eligibility
// authorization port.
type AssignmentEligibilityAuthorizationFunc func(context.Context, AssignmentEligibilityAuthorizationRequest) error

func (f AssignmentEligibilityAuthorizationFunc) AuthorizeAssignment(ctx context.Context, req AssignmentEligibilityAuthorizationRequest) error {
	return f(ctx, req)
}

// WorkingReplacementAuthorization is the live tracker ownership proof required
// immediately before a working generation's external leases are released.
type WorkingReplacementAuthorization struct {
	Status   string
	Assignee string
}

// WorkingReplacementAuthorizationPort reads exact live tracker status and
// ownership. Replacement release fails closed when this capability is absent,
// errors, or does not prove the durable claim actor still owns in-progress work.
type WorkingReplacementAuthorizationPort interface {
	AuthorizeWorkingReplacement(context.Context, string) (WorkingReplacementAuthorization, error)
}

// WorkingReplacementAuthorizationFunc adapts a function to the authorization
// port.
type WorkingReplacementAuthorizationFunc func(context.Context, string) (WorkingReplacementAuthorization, error)

func (f WorkingReplacementAuthorizationFunc) AuthorizeWorkingReplacement(ctx context.Context, beadID string) (WorkingReplacementAuthorization, error) {
	return f(ctx, beadID)
}

// WorkingReplacementReleaseReceipt reports the old assignment lease surface
// removed before a replacement generation is persisted. A successful port must
// return the exact complete durable path and reservation-ID sets supplied by
// the source Assignment, including on an idempotent retry that finds the remote
// leases already absent. Empty durable sets require empty receipt sets.
type WorkingReplacementReleaseReceipt struct {
	ReleasedPaths          []string
	ReleasedReservationIDs []int
}

// WorkingReplacementReleasePort releases or reconciles the previous working
// assignment's external leases. Implementations must be idempotent because a
// crash after remote release but before the durable checkpoint repeats this
// call while ClearState is reservation_releasing.
type WorkingReplacementReleasePort interface {
	ReleaseWorkingAssignment(context.Context, *Assignment) (WorkingReplacementReleaseReceipt, error)
}

// WorkingReplacementReleaseFunc adapts a function to the release port.
type WorkingReplacementReleaseFunc func(context.Context, *Assignment) (WorkingReplacementReleaseReceipt, error)

func (f WorkingReplacementReleaseFunc) ReleaseWorkingAssignment(ctx context.Context, current *Assignment) (WorkingReplacementReleaseReceipt, error) {
	return f(ctx, current)
}

type nonTerminalClaimContextKey struct{}

// WithNonTerminalClaimGuard marks a tracker claim that must compare-and-set
// only a non-terminal work item. The marker survives crash recovery through
// Assignment.ClaimRequiresNonTerminal and is consumed by Beads adapters.
func WithNonTerminalClaimGuard(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, nonTerminalClaimContextKey{}, true)
}

// NonTerminalClaimGuardRequired reports whether a claim adapter must refuse a
// closed or tombstoned work item instead of allowing a generic claim to reopen
// it as a side effect.
func NonTerminalClaimGuardRequired(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	required, _ := ctx.Value(nonTerminalClaimContextKey{}).(bool)
	return required
}

// AtomicRequest describes one claim-reserve-dispatch transaction.
type AtomicRequest struct {
	BeadID    string
	BeadTitle string
	Target    string
	// OccupancyKey is the stable physical pane or recipient identity used for
	// locking and exclusion. Adapters should pass tmux pane IDs; raw selector
	// spellings belong in Target. Empty falls back to Target.
	OccupancyKey   string
	Pane           int
	AgentType      string
	AgentName      string
	Actor          string
	Prompt         string
	IdempotencyKey string
	// IntentSHA256 is populated by AtomicCoordinator from the original prompt
	// before preflight. Callers must not use it to bypass that calculation.
	IntentSHA256 string
	// RecoveredIntentSHA256 may carry the exact checksum from an existing same-key
	// durable row when the original unredacted prompt is intentionally unavailable.
	RecoveredIntentSHA256 string
	// RequireReservation fails closed before a new claim or dispatch when the
	// reservation port is unavailable. RequestedPaths also implies this flag.
	RequireReservation        bool
	AllowReservationDiscovery bool
	RequestedPaths            []string
	ReservationTTL            time.Duration
	// ReplaceWorkingAssignment starts a new atomic generation in place of the
	// same bead's working generation. The coordinator releases or reconciles the
	// old generation's leases under the external-cleanup, bead, and target
	// operation locks before persisting the replacement. Exact retries of the
	// newly persisted idempotency key remain ordinary recovery attempts.
	ReplaceWorkingAssignment bool
	claimRequiresNonTerminal bool
}

// AtomicResult reports the durable state reached by Execute.
type AtomicResult struct {
	Assignment             *Assignment
	Claim                  ClaimReceipt
	Lease                  LeaseReceipt
	Dispatch               DispatchReceipt
	ReleasedPaths          []string
	ReleasedReservationIDs []int
	Sent                   bool
	Replayed               bool
	Recovered              bool
}

// AtomicCoordinator owns the single claim-before-reserve-before-send boundary.
type AtomicCoordinator struct {
	store       *AssignmentStore
	claimer     ClaimPort
	reserver    ReservationPort
	dispatcher  DispatchPort
	preflight   PromptPreflightPort
	workStatus  WorkItemStatusPort
	eligibility AssignmentEligibilityAuthorizationPort
	replaceAuth WorkingReplacementAuthorizationPort
	releaser    WorkingReplacementReleasePort
	now         func() time.Time
}

// NewAtomicCoordinator creates an assignment coordinator with injectable
// external ports. The reservation port may be nil when no paths are known.
func NewAtomicCoordinator(store *AssignmentStore, claimer ClaimPort, reserver ReservationPort, dispatcher DispatchPort, preflight ...PromptPreflightPort) *AtomicCoordinator {
	coordinator := &AtomicCoordinator{
		store:      store,
		claimer:    claimer,
		reserver:   reserver,
		dispatcher: dispatcher,
		now:        func() time.Time { return time.Now().UTC() },
	}
	if len(preflight) > 0 {
		coordinator.preflight = preflight[0]
	}
	return coordinator
}

// WithWorkItemStatusPort requires external reopen proof for terminal-to-new
// assignment generations. Production adapters should always configure it.
func (c *AtomicCoordinator) WithWorkItemStatusPort(port WorkItemStatusPort) *AtomicCoordinator {
	c.workStatus = port
	return c
}

// WithAssignmentEligibilityAuthorizationPort installs the final read-only
// policy proof used before an unsent generation can mutate durable or external
// state. Exact sent-receipt replay exits before this port is consulted.
func (c *AtomicCoordinator) WithAssignmentEligibilityAuthorizationPort(port AssignmentEligibilityAuthorizationPort) *AtomicCoordinator {
	c.eligibility = port
	return c
}

// WithWorkingReplacementAuthorizationPort installs the exact live ownership
// proof required before releasing a working assignment's external leases.
func (c *AtomicCoordinator) WithWorkingReplacementAuthorizationPort(port WorkingReplacementAuthorizationPort) *AtomicCoordinator {
	c.replaceAuth = port
	return c
}

// WithWorkingReplacementReleasePort installs the idempotent lease handoff used
// by ReplaceWorkingAssignment. The callback executes while the external-cleanup,
// bead, and target operation locks are held.
func (c *AtomicCoordinator) WithWorkingReplacementReleasePort(port WorkingReplacementReleasePort) *AtomicCoordinator {
	c.releaser = port
	return c
}

// AssignmentIdempotencyKey returns a deterministic digest for callers that
// already have a stable external request identity. New independent attempts
// must use NewAssignmentIdempotencyKey so Beads does not treat them as the same
// actor retry.
func AssignmentIdempotencyKey(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(strings.TrimSpace(part)))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// NewAssignmentIdempotencyKey generates the unique identity of one assignment
// attempt. Persist this value and reuse it only when recovering that attempt.
func NewAssignmentIdempotencyKey() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate assignment idempotency key: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func PromptSHA256(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}

// DispatchDeliveryID identifies one durable transport generation. Exact
// retries reuse the idempotency key and therefore the receipt; independent or
// reopened generations receive a different ID even on the same pane/protocol.
func DispatchDeliveryID(target, protocol, idempotencyKey string) string {
	return fmt.Sprintf("%s/%s/%s", strings.TrimSpace(target), strings.TrimSpace(protocol), strings.TrimSpace(idempotencyKey))
}

func normalizeOccupancyKey(target, occupancyKey string) string {
	if normalized := strings.TrimSpace(occupancyKey); normalized != "" {
		return normalized
	}
	return strings.TrimSpace(target)
}

// CanonicalPaneIdentity returns an assignment's unambiguous physical pane
// identity. Bare pane indexes and arbitrary delivery labels are deliberately
// rejected because they cannot distinguish equal local indexes across windows.
func CanonicalPaneIdentity(a *Assignment) (string, error) {
	if a == nil {
		return "", errors.New("assignment is required")
	}
	if occupancyKey := strings.TrimSpace(a.OccupancyKey); occupancyKey != "" {
		if isCanonicalPaneIdentity(occupancyKey) {
			return occupancyKey, nil
		}
		return "", paneIdentityMigrationError(a)
	}
	if dispatchTarget := strings.TrimSpace(a.DispatchTarget); dispatchTarget != "" && isCanonicalPaneIdentity(dispatchTarget) {
		return dispatchTarget, nil
	}
	return "", paneIdentityMigrationError(a)
}

func paneIdentityMigrationError(a *Assignment) error {
	if a == nil {
		return errors.New("assignment is required")
	}
	return &PaneIdentityMigrationError{
		BeadID:         a.BeadID,
		Pane:           a.Pane,
		OccupancyKey:   a.OccupancyKey,
		DispatchTarget: a.DispatchTarget,
	}
}

// StableClaimActor makes independent assignment intents distinct Beads actors
// while keeping a retry of the same idempotency key stable and human-readable.
func StableClaimActor(baseActor, idempotencyKey string) string {
	baseActor = strings.TrimSpace(baseActor)
	if baseActor == "" {
		baseActor = "ntm"
	}
	key := strings.TrimSpace(idempotencyKey)
	if len(key) > 12 {
		key = key[:12]
	}
	if key == "" {
		key = AssignmentIdempotencyKey(baseActor)[:12]
	}
	suffix := "/ntm-" + key
	if strings.HasSuffix(baseActor, suffix) {
		return baseActor
	}
	return baseActor + suffix
}

// Execute claims the bead, persists the claim, reserves files, persists the
// lease, and only then dispatches. Same-key completed retries are replayed
// without external side effects.
func (c *AtomicCoordinator) Execute(ctx context.Context, req AtomicRequest) (AtomicResult, error) {
	var result AtomicResult
	if len(req.RequestedPaths) > 0 {
		req.RequireReservation = true
	}
	req.OccupancyKey = normalizeOccupancyKey(req.Target, req.OccupancyKey)
	// A generic `br --claim` can reopen terminal work as a side effect. Every
	// assignment generation must use the guarded nonterminal compare-and-set,
	// including the first generation when no local ledger row exists yet.
	req.claimRequiresNonTerminal = true
	if err := validateAtomicRequest(req); err != nil {
		return result, err
	}
	if c.store == nil || c.claimer == nil || c.dispatcher == nil {
		return result, errors.New("atomic assignment coordinator is not fully configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	rawPrompt := req.Prompt
	req.IntentSHA256 = PromptSHA256(rawPrompt)
	cleanupUnlock, err := c.store.AcquireExternalCleanupLock(ctx, req.BeadID)
	if err != nil {
		return result, fmt.Errorf("lock atomic assignment external cleanup %s: %w", req.BeadID, err)
	}
	defer cleanupUnlock()
	operationUnlock, err := acquireAtomicBeadOperationLock(ctx, c.store.path, req.BeadID)
	if err != nil {
		return result, fmt.Errorf("lock atomic assignment %s: %w", req.BeadID, err)
	}
	defer operationUnlock()
	targetUnlock, err := acquireAtomicTargetOperationLock(ctx, c.store.path, req.OccupancyKey)
	if err != nil {
		return result, fmt.Errorf("lock atomic assignment target %s: %w", req.Target, err)
	}
	defer targetUnlock()
	if err := c.store.LoadStrict(); err != nil {
		return result, fmt.Errorf("refresh atomic assignment %s: %w", req.BeadID, err)
	}
	occupied, occupancyErr := activeAssignmentForTarget(c.store.List(), req.BeadID, req.OccupancyKey)
	if occupancyErr != nil {
		result.Assignment = occupied
		return result, occupancyErr
	}
	if occupied != nil {
		result.Assignment = occupied
		return result, targetOccupiedError(req.Target, occupied, c.now())
	}

	prior := c.store.Get(req.BeadID)
	if prior != nil && isTerminalAssignmentStatus(prior.Status) &&
		prior.IdempotencyKey != req.IdempotencyKey && strings.TrimSpace(prior.PendingCompletionEventID) != "" {
		result.Assignment = prior
		return result, fmt.Errorf("%w: acknowledge event %s for %s before starting a new generation", ErrCompletionEventPending, prior.PendingCompletionEventID, req.BeadID)
	}
	actor := StableClaimActor(req.Actor, req.IdempotencyKey)
	if prior != nil && prior.IdempotencyKey == req.IdempotencyKey && strings.TrimSpace(prior.ClaimActor) != "" {
		// The durable row is authoritative during same-key crash recovery. A
		// replacement generation intentionally reuses its predecessor's actor,
		// whose suffix may belong to an older idempotency key; stabilizing that
		// value again would invent a different tracker owner and deadlock retry.
		actor = prior.ClaimActor
	}
	replacementStart := false
	if req.ReplaceWorkingAssignment {
		if prior == nil {
			return result, fmt.Errorf("%w: no durable assignment exists for %s", ErrWorkingReplacementNotAllowed, req.BeadID)
		}
		if prior.IdempotencyKey != req.IdempotencyKey {
			if err := validateWorkingReplacementCandidate(prior, req); err != nil {
				result.Assignment = prior
				return result, err
			}
			actor, err = workingReplacementClaimActor(prior)
			if err != nil {
				result.Assignment = prior
				return result, err
			}
			replacementStart = true
			if strings.TrimSpace(req.BeadTitle) == "" {
				req.BeadTitle = prior.BeadTitle
			}
		} else if strings.TrimSpace(prior.ClaimActor) != "" {
			// A retry of a replacement generation must retain the actor copied
			// from the old generation rather than derive one from the new agent.
			actor = prior.ClaimActor
		}
	}
	recoveringDurableIntent := false
	if recoveredIntent := strings.TrimSpace(req.RecoveredIntentSHA256); recoveredIntent != "" {
		if prior == nil || prior.IdempotencyKey != req.IdempotencyKey {
			return result, errors.New("recovered intent checksum requires an existing same-key assignment")
		}
		storedIntent := strings.TrimSpace(prior.IntentSHA256)
		if storedIntent == "" {
			storedIntent = strings.TrimSpace(prior.PromptSHA256)
		}
		if storedIntent == "" || recoveredIntent != storedIntent {
			return result, errors.New("recovered intent checksum does not match the durable assignment")
		}
		req.IntentSHA256 = storedIntent
		recoveringDurableIntent = true
		if prior.DispatchState != DispatchSent && prior.DispatchState != DispatchSending {
			if strings.TrimSpace(prior.PendingPrompt) == "" {
				return result, errors.New("recovered unsent assignment has no durable pending prompt")
			}
			rawPrompt = prior.PendingPrompt
			req.Prompt = prior.PendingPrompt
			req.BeadTitle = prior.BeadTitle
		}
	}
	if prior != nil {
		if prior.ClearState != ClearStateNone && !replacementStart {
			result.Assignment = prior
			return result, fmt.Errorf("%w: %s is awaiting reservation release", ErrClaimConflict, req.BeadID)
		}
		if prior.IdempotencyKey == req.IdempotencyKey && prior.ClaimActor == actor {
			switch effectiveClaimState(prior) {
			case ClaimIneligible:
				result.Assignment = prior
				return result, fmt.Errorf("claim %s: %w", req.BeadID, ErrClaimIneligible)
			case ClaimFailed:
				result.Assignment = prior
				return result, fmt.Errorf("claim %s: %w", req.BeadID, ErrClaimConflict)
			}
			if isTerminalAssignmentStatus(prior.Status) {
				result.Assignment = prior
				return result, fmt.Errorf("%w: %s", ErrTerminalAssignmentAttempt, req.BeadID)
			}
			if !matchesAtomicRawIntent(prior, req) {
				result.Assignment = prior
				return result, fmt.Errorf("idempotency key %s was reused for a different assignment intent", req.IdempotencyKey)
			}
			switch prior.DispatchState {
			case DispatchSent:
				dispatch := DispatchReceipt{DeliveryID: prior.DispatchReceiptID, Duration: prior.DispatchDuration}
				if receiptErr := validateDispatchReceipt(dispatch); receiptErr != nil {
					result.Assignment = prior
					return result, errors.Join(ErrDispatchOutcomeUnknown, receiptErr)
				}
				result.Assignment = prior
				result.Sent = true
				result.Replayed = true
				result.Dispatch = dispatch
				return result, nil
			case DispatchSending:
				result.Assignment = prior
				return result, ErrDispatchOutcomeUnknown
			}
			result.Recovered = true
		} else if prior.IdempotencyKey != "" && !replacementStart {
			switch prior.Status {
			case StatusClaiming, StatusClaimed, StatusAssigned, StatusWorking:
				result.Assignment = prior
				return result, fmt.Errorf("%w: %s is recorded for %s", ErrClaimConflict, req.BeadID, prior.ClaimActor)
			}
		}
		if isTerminalAssignmentStatus(prior.Status) && prior.IdempotencyKey != req.IdempotencyKey {
			if assignmentHasUnresolvedReservation(prior) {
				result.Assignment = prior
				return result, fmt.Errorf("%w: reconcile and clear assignment %s before starting a new generation", ErrReservationReleaseRequired, req.BeadID)
			}
			if c.workStatus == nil {
				result.Assignment = prior
				return result, fmt.Errorf("%w: cannot prove bead %s was reopened", ErrTerminalAssignmentAttempt, req.BeadID)
			}
			trackerStatus, statusErr := c.workStatus.WorkItemStatus(ctx, req.BeadID)
			if statusErr != nil {
				result.Assignment = prior
				return result, fmt.Errorf("%w: verify bead %s reopen status: %v", ErrTerminalAssignmentAttempt, req.BeadID, statusErr)
			}
			if !workItemStatusAllowsNewGeneration(trackerStatus) {
				result.Assignment = prior
				return result, fmt.Errorf("%w: bead %s tracker status is %q", ErrTerminalAssignmentAttempt, req.BeadID, trackerStatus)
			}
			// `br reopen` retains the assignee. Reuse that actor for the new
			// generation so the atomic claim remains valid, while the new key and
			// ledger record still distinguish the new dispatch attempt.
			if strings.TrimSpace(prior.ClaimActor) != "" {
				actor = prior.ClaimActor
			}
			req.claimRequiresNonTerminal = true
		}
	}
	durablePrompt := rawPrompt
	durableTitle := req.BeadTitle
	if c.preflight != nil {
		preflightResult, preflightErr := c.preflight.Preflight(ctx, DispatchRequest{
			BeadID: req.BeadID, BeadTitle: req.BeadTitle, Target: req.Target, Pane: req.Pane,
			AgentType: req.AgentType, AgentName: req.AgentName, Prompt: rawPrompt, IdempotencyKey: req.IdempotencyKey,
			RequestedPaths: append([]string(nil), req.RequestedPaths...),
		})
		if preflightErr != nil {
			return result, fmt.Errorf("preflight assignment prompt for %s: %w", req.BeadID, preflightErr)
		}
		if strings.TrimSpace(preflightResult.DispatchPrompt) == "" || strings.TrimSpace(preflightResult.DurablePrompt) == "" {
			return result, errors.New("assignment prompt preflight returned an empty payload")
		}
		req.Prompt = preflightResult.DispatchPrompt
		durablePrompt = preflightResult.DurablePrompt
		if strings.TrimSpace(preflightResult.DurableTitle) != "" {
			durableTitle = preflightResult.DurableTitle
		}
	}
	if recoveringDurableIntent {
		if strings.TrimSpace(prior.PendingPrompt) != "" {
			durablePrompt = prior.PendingPrompt
		}
		durableTitle = prior.BeadTitle
	}
	req.BeadTitle = durableTitle
	persistedReq := req
	persistedReq.Prompt = durablePrompt
	if prior != nil && prior.IdempotencyKey == req.IdempotencyKey && prior.ClaimActor == actor && !matchesAtomicIntent(prior, persistedReq) {
		result.Assignment = prior
		return result, fmt.Errorf("idempotency key %s was reused for a different durable assignment intent", req.IdempotencyKey)
	}
	if req.RequireReservation && c.reserver == nil {
		switch {
		case prior != nil && prior.IdempotencyKey == req.IdempotencyKey && reservationOutcomeAmbiguous(prior):
			result.Assignment = prior
			return result, ErrReservationOutcomeUnknown
		case prior == nil || prior.IdempotencyKey != req.IdempotencyKey || reservationNeedsRefresh(prior, c.now()):
			result.Assignment = prior
			return result, ErrReservationRequired
		}
	}
	if c.eligibility != nil {
		authorization := assignmentEligibilityAuthorizationRequest(prior, req, actor, replacementStart)
		if authorizationErr := c.eligibility.AuthorizeAssignment(ctx, authorization); authorizationErr != nil {
			result.Assignment = prior
			return result, fmt.Errorf("authorize assignment %s before mutation: %w", req.BeadID, authorizationErr)
		}
	}
	if replacementStart {
		releaseReceipt, releasedAssignment, releaseErr := c.releaseWorkingReplacementWithOperationLocks(ctx, prior, req)
		result.ReleasedPaths = append([]string(nil), releaseReceipt.ReleasedPaths...)
		result.ReleasedReservationIDs = append([]int(nil), releaseReceipt.ReleasedReservationIDs...)
		result.Assignment = releasedAssignment
		if releaseErr != nil {
			return result, releaseErr
		}
	}

	recorded, err := c.store.RecordAtomicIntent(persistedReq, actor, c.now())
	if err != nil {
		return result, err
	}
	result.Assignment = recorded

	claim, recorded, err := c.ensureClaim(ctx, persistedReq, actor, recorded)
	if err != nil {
		result.Assignment = c.store.Get(req.BeadID)
		return result, err
	}
	result.Claim = claim
	result.Assignment = recorded

	lease, recorded, err := c.ensureReservation(ctx, req, recorded)
	result.Lease = lease
	result.Assignment = recorded
	if err != nil {
		return result, err
	}

	if err := c.store.RecordAtomicDispatchStarted(req.BeadID, req.IdempotencyKey, c.now()); err != nil {
		return result, err
	}
	dispatch, dispatchErr := c.dispatcher.Dispatch(ctx, DispatchRequest{
		BeadID:         req.BeadID,
		BeadTitle:      req.BeadTitle,
		Target:         req.Target,
		Pane:           req.Pane,
		AgentType:      req.AgentType,
		AgentName:      req.AgentName,
		Prompt:         req.Prompt,
		IdempotencyKey: req.IdempotencyKey,
	})
	result.Dispatch = dispatch
	if dispatchErr != nil {
		if IsGuaranteedNoActuation(dispatchErr) {
			if persistErr := c.store.RecordAtomicDispatchFailed(req.BeadID, req.IdempotencyKey, dispatchErr); persistErr != nil {
				return result, errors.Join(dispatchErr, persistErr)
			}
			result.Assignment = c.store.Get(req.BeadID)
			return result, fmt.Errorf("dispatch %s: %w", req.BeadID, dispatchErr)
		}
		result.Assignment = c.store.Get(req.BeadID)
		return result, fmt.Errorf("dispatch %s: %w", req.BeadID, errors.Join(ErrDispatchOutcomeUnknown, dispatchErr))
	}
	if receiptErr := validateDispatchReceipt(dispatch); receiptErr != nil {
		result.Assignment = c.store.Get(req.BeadID)
		return result, fmt.Errorf("dispatch %s: %w", req.BeadID, errors.Join(ErrDispatchOutcomeUnknown, receiptErr))
	}
	if err := c.store.RecordAtomicDispatchSent(req.BeadID, req.IdempotencyKey, durablePrompt, dispatch, c.now()); err != nil {
		result.Assignment = c.store.Get(req.BeadID)
		return result, fmt.Errorf("persist dispatch receipt for %s: %w", req.BeadID, errors.Join(ErrDispatchOutcomeUnknown, err))
	}
	result.Assignment = c.store.Get(req.BeadID)
	result.Sent = true
	return result, nil
}

func targetOccupiedError(target string, occupied *Assignment, now time.Time) error {
	if occupied == nil {
		return fmt.Errorf("%w: %s has an unknown owner", ErrTargetOccupied, target)
	}
	age := "unknown"
	assignedAt := "unknown"
	if !occupied.AssignedAt.IsZero() {
		assignedAt = occupied.AssignedAt.UTC().Format(time.RFC3339)
		elapsed := now.Sub(occupied.AssignedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		age = elapsed.Truncate(time.Second).String()
	}
	return fmt.Errorf("%w: %s is owned by bead %s (status=%s, age=%s, assigned_at=%s)",
		ErrTargetOccupied, target, occupied.BeadID, occupied.Status, age, assignedAt)
}

func assignmentEligibilityAuthorizationRequest(prior *Assignment, req AtomicRequest, actor string, replacementStart bool) AssignmentEligibilityAuthorizationRequest {
	authorization := AssignmentEligibilityAuthorizationRequest{
		BeadID:     req.BeadID,
		ClaimActor: actor,
	}
	switch {
	case replacementStart:
		authorization.AllowOwnedInProgress = true
	case prior == nil:
		authorization.AllowUnassignedOpen = true
	case isTerminalAssignmentStatus(prior.Status) && prior.IdempotencyKey != req.IdempotencyKey:
		authorization.AllowUnassignedOpen = true
		authorization.AllowOwnedOpen = true
		authorization.AllowOwnedInProgress = true
	case prior.IdempotencyKey == req.IdempotencyKey:
		authorization.AllowOwnedInProgress = true
		switch effectiveClaimState(prior) {
		case ClaimPending, ClaimClaiming, ClaimUnknown:
			// A crash may have happened on either side of the tracker claim.
			authorization.AllowUnassignedOpen = true
		}
	}
	return authorization
}

func workItemStatusAllowsNewGeneration(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "open", "in_progress":
		return true
	default:
		return false
	}
}

func (c *AtomicCoordinator) releaseWorkingReplacementWithOperationLocks(ctx context.Context, prior *Assignment, req AtomicRequest) (WorkingReplacementReleaseReceipt, *Assignment, error) {
	var receipt WorkingReplacementReleaseReceipt
	if err := validateWorkingReplacementCandidate(prior, req); err != nil {
		return receipt, prior, err
	}
	current := prior
	switch current.ClearState {
	case ClearStateLeasesReleased:
		if err := validateWorkingReplacement(current, req); err != nil {
			return receipt, current, err
		}
		return receipt, current, nil
	case ClearStateNone, ClearStateReservationReleasing:
		if c.releaser == nil {
			return receipt, current, fmt.Errorf("%w: no working-assignment release port is configured", ErrWorkingReplacementNotAllowed)
		}
	default:
		return receipt, current, fmt.Errorf("%w: %s has invalid clear state %q", ErrWorkingReplacementNotAllowed, req.BeadID, current.ClearState)
	}
	if err := c.authorizeWorkingReplacementRelease(ctx, current); err != nil {
		return receipt, current, err
	}

	if current.ClearState == ClearStateNone {
		clearing, err := c.store.beginClearWithOperationLock(req.BeadID, c.now(), false, []AssignmentStatus{StatusWorking})
		if err != nil {
			return receipt, c.store.Get(req.BeadID), err
		}
		current = clearing
	}

	receipt, releaseErr := c.releaser.ReleaseWorkingAssignment(ctx, cloneAssignment(current))
	if releaseErr != nil {
		return c.recordWorkingReplacementReleaseFailure(req.BeadID, receipt, releaseErr)
	}
	normalizedPaths, normalizedIDs, receiptErr := validateWorkingReplacementReleaseReceipt(current, receipt)
	receipt.ReleasedPaths = normalizedPaths
	receipt.ReleasedReservationIDs = normalizedIDs
	if receiptErr != nil {
		return c.recordWorkingReplacementReleaseFailure(req.BeadID, receipt, receiptErr)
	}

	released, err := c.store.recordClearLeasesReleasedWithOperationLock(req.BeadID)
	if err != nil {
		return receipt, c.store.Get(req.BeadID), fmt.Errorf("persist working-assignment release checkpoint: %w", err)
	}
	if err := validateWorkingReplacement(released, req); err != nil {
		return receipt, released, err
	}
	return receipt, released, nil
}

func (c *AtomicCoordinator) authorizeWorkingReplacementRelease(ctx context.Context, current *Assignment) error {
	if current == nil {
		return fmt.Errorf("%w: replacement source assignment is missing", ErrWorkingReplacementNotAllowed)
	}
	if c.replaceAuth == nil {
		return fmt.Errorf("%w: cannot prove bead %s live tracker ownership", ErrWorkingReplacementNotAllowed, current.BeadID)
	}
	authorization, err := c.replaceAuth.AuthorizeWorkingReplacement(ctx, current.BeadID)
	if err != nil {
		return fmt.Errorf("%w: verify bead %s replacement authorization: %v", ErrWorkingReplacementNotAllowed, current.BeadID, err)
	}
	if status := strings.TrimSpace(authorization.Status); status != "in_progress" {
		return fmt.Errorf("%w: bead %s tracker status is %q, want in_progress", ErrWorkingReplacementNotAllowed, current.BeadID, authorization.Status)
	}
	expectedActor := strings.TrimSpace(current.ClaimActor)
	actualAssignee := strings.TrimSpace(authorization.Assignee)
	if expectedActor == "" {
		return fmt.Errorf("%w: bead %s durable claim actor is empty", ErrWorkingReplacementNotAllowed, current.BeadID)
	}
	if actualAssignee == "" {
		return fmt.Errorf("%w: bead %s live tracker assignee is empty", ErrWorkingReplacementNotAllowed, current.BeadID)
	}
	if actualAssignee != expectedActor {
		return fmt.Errorf("%w: bead %s live tracker assignee %q does not match durable claim actor %q", ErrWorkingReplacementNotAllowed, current.BeadID, actualAssignee, expectedActor)
	}
	return nil
}

func (c *AtomicCoordinator) recordWorkingReplacementReleaseFailure(beadID string, receipt WorkingReplacementReleaseReceipt, releaseErr error) (WorkingReplacementReleaseReceipt, *Assignment, error) {
	if persistErr := c.store.recordClearReleaseFailedWithOperationLock(beadID, releaseErr); persistErr != nil {
		return receipt, c.store.Get(beadID), errors.Join(releaseErr, fmt.Errorf("persist working-assignment release failure: %w", persistErr))
	}
	return receipt, c.store.Get(beadID), fmt.Errorf("release working assignment %s: %w", beadID, releaseErr)
}

func validateWorkingReplacementReleaseReceipt(current *Assignment, receipt WorkingReplacementReleaseReceipt) ([]string, []int, error) {
	released, err := normalizedUniqueReservationPaths(receipt.ReleasedPaths, "working replacement released path")
	if err != nil {
		return nil, nil, err
	}
	releasedIDs, err := normalizedUniqueReservationIDs(receipt.ReleasedReservationIDs, "working replacement released reservation ID")
	if err != nil {
		return nil, nil, err
	}
	if current == nil {
		return nil, nil, errors.New("working replacement release receipt has no source assignment")
	}
	known := make([]string, 0, len(current.ReservedPaths)+len(current.ReservationRequested)+len(current.ReservationInputPaths))
	seenKnown := make(map[string]struct{}, cap(known))
	for _, group := range [][]string{current.ReservedPaths, current.ReservationRequested, current.ReservationInputPaths} {
		normalized, groupErr := normalizedUniqueReservationPaths(group, "durable working replacement path")
		if groupErr != nil {
			return nil, nil, groupErr
		}
		for _, path := range normalized {
			if _, duplicate := seenKnown[path]; duplicate {
				continue
			}
			seenKnown[path] = struct{}{}
			known = append(known, path)
		}
	}
	if missing := reservationPathDifference(known, released); len(missing) > 0 {
		return nil, nil, fmt.Errorf("working replacement release receipt omitted expected paths: %s", strings.Join(missing, ", "))
	}
	if unexpected := reservationPathDifference(released, known); len(unexpected) > 0 {
		return nil, nil, fmt.Errorf("working replacement release receipt contains unexpected paths: %s", strings.Join(unexpected, ", "))
	}
	expectedIDs, err := normalizedUniqueReservationIDs(current.ReservationIDs, "durable working replacement reservation ID")
	if err != nil {
		return nil, nil, err
	}
	if !sameReservationIDSet(expectedIDs, releasedIDs) {
		return nil, nil, fmt.Errorf("working replacement release receipt IDs %v do not match expected IDs %v", releasedIDs, expectedIDs)
	}
	return released, releasedIDs, nil
}

func normalizedUniqueReservationIDs(ids []int, field string) ([]int, error) {
	result := make([]int, 0, len(ids))
	seen := make(map[int]struct{}, len(ids))
	var validationErrors []error
	for _, id := range ids {
		if id <= 0 {
			validationErrors = append(validationErrors, fmt.Errorf("%s %d must be positive", field, id))
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			validationErrors = append(validationErrors, fmt.Errorf("%s %d is duplicated", field, id))
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Ints(result)
	return result, errors.Join(validationErrors...)
}

func sameReservationIDSet(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validateWorkingReplacementCandidate(prior *Assignment, req AtomicRequest) error {
	if prior == nil || prior.BeadID != req.BeadID {
		return fmt.Errorf("%w: no matching durable assignment exists for %s", ErrWorkingReplacementNotAllowed, req.BeadID)
	}
	if prior.Status != StatusWorking {
		return fmt.Errorf("%w: %s is %s, expected %s", ErrWorkingReplacementNotAllowed, req.BeadID, prior.Status, StatusWorking)
	}
	switch prior.ClearState {
	case ClearStateNone, ClearStateReservationReleasing, ClearStateLeasesReleased:
	default:
		return fmt.Errorf("%w: %s has invalid clear state %q", ErrWorkingReplacementNotAllowed, req.BeadID, prior.ClearState)
	}
	if prior.DispatchState == DispatchSending {
		return fmt.Errorf("%w: %s has an unknown dispatch outcome", ErrWorkingReplacementNotAllowed, req.BeadID)
	}
	if strings.TrimSpace(prior.IdempotencyKey) == strings.TrimSpace(req.IdempotencyKey) {
		return fmt.Errorf("%w: replacement generation must use a new idempotency key", ErrWorkingReplacementNotAllowed)
	}
	return nil
}

func validateWorkingReplacement(prior *Assignment, req AtomicRequest) error {
	if err := validateWorkingReplacementCandidate(prior, req); err != nil {
		return err
	}
	if prior.ClearState != ClearStateLeasesReleased {
		return fmt.Errorf("%w: %s clear state is %q, expected %q", ErrWorkingReplacementNotAllowed, req.BeadID, prior.ClearState, ClearStateLeasesReleased)
	}
	if assignmentHasUnresolvedReservation(prior) {
		return fmt.Errorf("%w: %s still has unresolved reservation metadata", ErrWorkingReplacementNotAllowed, req.BeadID)
	}
	return nil
}

func workingReplacementClaimActor(prior *Assignment) (string, error) {
	if prior == nil {
		return "", ErrWorkingReplacementNotAllowed
	}
	if actor := strings.TrimSpace(prior.ClaimActor); actor != "" {
		return actor, nil
	}
	return "", fmt.Errorf("%w: %s has no durable claim actor; migrate the assignment before replacement", ErrWorkingReplacementNotAllowed, prior.BeadID)
}

func (c *AtomicCoordinator) ensureClaim(ctx context.Context, req AtomicRequest, actor string, recorded *Assignment) (ClaimReceipt, *Assignment, error) {
	if req.claimRequiresNonTerminal || (recorded != nil && recorded.ClaimRequiresNonTerminal) {
		ctx = WithNonTerminalClaimGuard(ctx)
	}
	state := effectiveClaimState(recorded)
	if state == ClaimClaimed {
		claim := claimFromAssignment(recorded)
		if receiptErr := validateClaimReceipt(claim, req.BeadID, actor); receiptErr != nil {
			return ClaimReceipt{}, recorded, errors.Join(ErrClaimOutcomeUnknown, receiptErr)
		}
		return claim, recorded, nil
	}
	if state == ClaimFailed {
		return ClaimReceipt{}, recorded, fmt.Errorf("claim %s: %w", req.BeadID, ErrClaimConflict)
	}
	if state == ClaimIneligible {
		return ClaimReceipt{}, recorded, fmt.Errorf("claim %s: %w", req.BeadID, ErrClaimIneligible)
	}

	if state == ClaimClaiming || state == ClaimUnknown {
		if reconciler, ok := c.claimer.(ClaimReconciliationPort); ok {
			reconciliation, reconcileErr := reconciler.ReconcileClaim(ctx, req.BeadID, actor)
			if reconcileErr != nil {
				unknownErr := errors.Join(ErrClaimOutcomeUnknown, reconcileErr)
				if persistErr := c.store.RecordAtomicClaimUncertain(req.BeadID, req.IdempotencyKey, unknownErr); persistErr != nil {
					return ClaimReceipt{}, recorded, errors.Join(unknownErr, persistErr)
				}
				return ClaimReceipt{}, c.store.Get(req.BeadID), fmt.Errorf("reconcile claim %s: %w", req.BeadID, unknownErr)
			}
			switch reconciliation.State {
			case ClaimReconciliationOwned:
				if receiptErr := validateClaimReceipt(reconciliation.Receipt, req.BeadID, actor); receiptErr != nil {
					unknownErr := errors.Join(ErrClaimOutcomeUnknown, receiptErr)
					if persistErr := c.store.RecordAtomicClaimUncertain(req.BeadID, req.IdempotencyKey, unknownErr); persistErr != nil {
						return ClaimReceipt{}, recorded, errors.Join(unknownErr, persistErr)
					}
					return ClaimReceipt{}, c.store.Get(req.BeadID), unknownErr
				}
				claim := normalizeClaimReceipt(reconciliation.Receipt, req.BeadID, actor, c.now())
				stored, err := c.store.RecordAtomicClaim(req, claim)
				return claim, stored, err
			case ClaimReconciliationConflict:
				conflictErr := fmt.Errorf("%w: %s is owned by another actor", ErrClaimConflict, req.BeadID)
				if persistErr := c.store.RecordAtomicClaimUncertain(req.BeadID, req.IdempotencyKey, conflictErr); persistErr != nil {
					return ClaimReceipt{}, recorded, errors.Join(conflictErr, persistErr)
				}
				return ClaimReceipt{}, c.store.Get(req.BeadID), conflictErr
			case ClaimReconciliationAbsent:
				// It is now safe to retry the claim below.
			case ClaimReconciliationUnknown, "":
				unknownErr := ErrClaimOutcomeUnknown
				if persistErr := c.store.RecordAtomicClaimUncertain(req.BeadID, req.IdempotencyKey, unknownErr); persistErr != nil {
					return ClaimReceipt{}, recorded, errors.Join(unknownErr, persistErr)
				}
				return ClaimReceipt{}, c.store.Get(req.BeadID), unknownErr
			default:
				return ClaimReceipt{}, recorded, fmt.Errorf("reconcile claim %s: invalid state %q", req.BeadID, reconciliation.State)
			}
		}
	}

	if _, err := c.store.RecordAtomicClaimStarted(req.BeadID, req.IdempotencyKey, c.now()); err != nil {
		return ClaimReceipt{}, recorded, err
	}
	claim, claimErr := c.claimer.Claim(ctx, req.BeadID, actor)
	if claimErr != nil {
		if persistErr := c.store.RecordAtomicClaimUncertain(req.BeadID, req.IdempotencyKey, claimErr); persistErr != nil {
			return ClaimReceipt{}, c.store.Get(req.BeadID), errors.Join(claimErr, persistErr)
		}
		return ClaimReceipt{}, c.store.Get(req.BeadID), fmt.Errorf("claim %s: %w", req.BeadID, claimErr)
	}
	if receiptErr := validateClaimReceipt(claim, req.BeadID, actor); receiptErr != nil {
		unknownErr := errors.Join(ErrClaimOutcomeUnknown, receiptErr)
		if persistErr := c.store.RecordAtomicClaimUncertain(req.BeadID, req.IdempotencyKey, unknownErr); persistErr != nil {
			return ClaimReceipt{}, c.store.Get(req.BeadID), errors.Join(unknownErr, persistErr)
		}
		return ClaimReceipt{}, c.store.Get(req.BeadID), fmt.Errorf("claim %s: %w", req.BeadID, unknownErr)
	}
	claim = normalizeClaimReceipt(claim, req.BeadID, actor, c.now())
	stored, err := c.store.RecordAtomicClaim(req, claim)
	return claim, stored, err
}

func (c *AtomicCoordinator) ensureReservation(ctx context.Context, req AtomicRequest, recorded *Assignment) (LeaseReceipt, *Assignment, error) {
	if recorded == nil {
		return LeaseReceipt{}, nil, errors.New("atomic assignment disappeared before reservation")
	}
	reservationReq := ReservationRequest{
		BeadID:         req.BeadID,
		BeadTitle:      req.BeadTitle,
		AgentName:      req.AgentName,
		Target:         req.OccupancyKey,
		RequestedPaths: append([]string(nil), req.RequestedPaths...),
		TTL:            req.ReservationTTL,
	}
	if strings.TrimSpace(reservationReq.BeadTitle) == "" {
		reservationReq.BeadTitle = recorded.BeadTitle
	}
	if len(reservationReq.RequestedPaths) == 0 {
		if len(recorded.ReservationRequested) > 0 {
			reservationReq.RequestedPaths = append([]string(nil), recorded.ReservationRequested...)
		} else if len(recorded.ReservationInputPaths) > 0 {
			reservationReq.RequestedPaths = append([]string(nil), recorded.ReservationInputPaths...)
		}
	}
	state := effectiveReservationState(recorded)
	lease := leaseFromAssignment(recorded)

	if state == ReservationReserved {
		if recorded.ReservationError != "" {
			return lease, recorded, fmt.Errorf("reserve files for %s: %w", req.BeadID, RequireReservationRelease(errors.New(recorded.ReservationError)))
		}
		validationErr := validateLeaseReceipt(reservationReq, lease)
		if validationErr == nil && req.RequireReservation {
			validationErr = validateRequiredLease(req, lease, c.now())
		}
		if validationErr != nil {
			reservationState, reservationErr := classifyValidatedReservation(lease, validationErr)
			if persistErr := c.store.RecordAtomicReservation(req.BeadID, req.IdempotencyKey, reservationState, lease, reservationErr); persistErr != nil {
				return lease, recorded, errors.Join(reservationErr, persistErr)
			}
			return lease, c.store.Get(req.BeadID), fmt.Errorf("validate persisted reservation for %s: %w", req.BeadID, reservationErr)
		}
		if !reservationExpired(recorded, c.now()) {
			return lease, recorded, nil
		}
	}

	if state == ReservationReserving || state == ReservationUnknown {
		reconciler, ok := c.reserver.(ReservationReconciliationPort)
		if !ok {
			return lease, recorded, ErrReservationOutcomeUnknown
		}
		reconciliation, reconcileErr := reconciler.ReconcileReservation(ctx, reservationReq, lease)
		if reconcileErr != nil {
			unknownErr := errors.Join(ErrReservationOutcomeUnknown, reconcileErr)
			if persistErr := c.store.RecordAtomicReservation(req.BeadID, req.IdempotencyKey, ReservationUnknown, lease, unknownErr); persistErr != nil {
				return lease, recorded, errors.Join(unknownErr, persistErr)
			}
			return lease, c.store.Get(req.BeadID), fmt.Errorf("reconcile reservation for %s: %w", req.BeadID, unknownErr)
		}
		switch reconciliation.State {
		case ReservationReconciliationReserved:
			lease = reconciliation.Lease
			validationErr := validateLeaseReceipt(reservationReq, lease)
			if validationErr == nil && req.RequireReservation {
				validationErr = validateRequiredLease(req, lease, c.now())
			}
			reservationState, reservationErr := classifyValidatedReservation(lease, validationErr)
			if persistErr := c.store.RecordAtomicReservation(req.BeadID, req.IdempotencyKey, reservationState, lease, reservationErr); persistErr != nil {
				return lease, recorded, persistErr
			}
			recorded = c.store.Get(req.BeadID)
			if reservationErr != nil {
				return lease, recorded, fmt.Errorf("reserve files for %s: %w", req.BeadID, reservationErr)
			}
			return lease, recorded, nil
		case ReservationReconciliationAbsent:
			if persistErr := c.store.RecordAtomicReservation(req.BeadID, req.IdempotencyKey, ReservationFailed, LeaseReceipt{}, nil); persistErr != nil {
				return lease, recorded, persistErr
			}
			recorded = c.store.Get(req.BeadID)
		case ReservationReconciliationUnknown, "":
			unknownLease := reconciliation.Lease
			if !leaseHasReservationHandles(unknownLease) {
				unknownLease = lease
			}
			if persistErr := c.store.RecordAtomicReservation(req.BeadID, req.IdempotencyKey, ReservationUnknown, unknownLease, ErrReservationOutcomeUnknown); persistErr != nil {
				return lease, recorded, errors.Join(ErrReservationOutcomeUnknown, persistErr)
			}
			return unknownLease, c.store.Get(req.BeadID), ErrReservationOutcomeUnknown
		default:
			return lease, recorded, fmt.Errorf("reconcile reservation for %s: invalid state %q", req.BeadID, reconciliation.State)
		}
	}
	if c.reserver == nil {
		if req.RequireReservation {
			return lease, recorded, ErrReservationRequired
		}
		return lease, recorded, nil
	}
	if err := c.store.RecordAtomicReservationStarted(req.BeadID, req.IdempotencyKey, c.now()); err != nil {
		return lease, recorded, err
	}
	lease, reserveErr := c.reserver.Reserve(ctx, reservationReq)
	if reserveErr != nil {
		reservationState, returnedErr := classifyReservationFailure(lease, reserveErr)
		if persistErr := c.store.RecordAtomicReservation(req.BeadID, req.IdempotencyKey, reservationState, lease, returnedErr); persistErr != nil {
			return lease, c.store.Get(req.BeadID), errors.Join(returnedErr, persistErr)
		}
		return lease, c.store.Get(req.BeadID), fmt.Errorf("reserve files for %s: %w", req.BeadID, returnedErr)
	}
	validationErr := validateLeaseReceipt(reservationReq, lease)
	if validationErr == nil && req.RequireReservation {
		validationErr = validateRequiredLease(req, lease, c.now())
	}
	reservationState, reservationErr := classifyValidatedReservation(lease, validationErr)
	if persistErr := c.store.RecordAtomicReservation(req.BeadID, req.IdempotencyKey, reservationState, lease, reservationErr); persistErr != nil {
		return lease, c.store.Get(req.BeadID), persistErr
	}
	recorded = c.store.Get(req.BeadID)
	if reservationErr != nil {
		return lease, recorded, fmt.Errorf("reserve files for %s: %w", req.BeadID, reservationErr)
	}
	return lease, recorded, nil
}

func normalizeClaimReceipt(claim ClaimReceipt, beadID, actor string, now time.Time) ClaimReceipt {
	claim.BeadID = beadID
	claim.Actor = actor
	if claim.ClaimedAt.IsZero() {
		claim.ClaimedAt = now
	}
	return claim
}

func validateClaimReceipt(claim ClaimReceipt, beadID, actor string) error {
	if actual := strings.TrimSpace(claim.BeadID); actual == "" {
		return errors.New("claim receipt has no bead ID")
	} else if actual != strings.TrimSpace(beadID) {
		return fmt.Errorf("claim receipt bead mismatch: got %s, want %s", actual, beadID)
	}
	if actual := strings.TrimSpace(claim.Actor); actual == "" {
		return errors.New("claim receipt has no actor")
	} else if actual != strings.TrimSpace(actor) {
		return fmt.Errorf("claim receipt actor mismatch: got %s, want %s", actual, actor)
	}
	if status := strings.ToLower(strings.TrimSpace(claim.Status)); status != "in_progress" {
		return fmt.Errorf("claim receipt status %q does not prove an in-progress claim", claim.Status)
	}
	if claim.ClaimedAt.IsZero() {
		return errors.New("claim receipt has no claim timestamp")
	}
	return nil
}

func claimFromAssignment(a *Assignment) ClaimReceipt {
	if a == nil {
		return ClaimReceipt{}
	}
	claim := ClaimReceipt{BeadID: a.BeadID, Actor: a.ClaimActor, Status: a.ClaimStatus}
	if a.ClaimedAt != nil {
		claim.ClaimedAt = *a.ClaimedAt
	}
	return claim
}

func validateAtomicRequest(req AtomicRequest) error {
	switch {
	case strings.TrimSpace(req.BeadID) == "":
		return errors.New("bead ID is required")
	case strings.TrimSpace(req.IdempotencyKey) == "":
		return errors.New("idempotency key is required")
	case strings.TrimSpace(req.Target) == "":
		return errors.New("assignment target is required")
	case !isPhysicalPaneOccupancyKey(req.OccupancyKey):
		return &PaneIdentityMigrationError{
			BeadID:         req.BeadID,
			Pane:           req.Pane,
			OccupancyKey:   req.OccupancyKey,
			DispatchTarget: req.Target,
		}
	case strings.TrimSpace(req.Prompt) == "":
		return errors.New("assignment prompt is required")
	case req.RequireReservation && len(req.RequestedPaths) == 0 && !req.AllowReservationDiscovery:
		return ErrReservationPathsRequired
	}
	for _, path := range req.RequestedPaths {
		if strings.TrimSpace(path) == "" {
			return errors.New("reservation paths cannot be empty")
		}
	}
	return nil
}

func activeAssignmentForTarget(assignments []*Assignment, beadID, occupancyKey string) (*Assignment, error) {
	occupancyKey = strings.TrimSpace(occupancyKey)
	for _, candidate := range assignments {
		if candidate == nil || candidate.BeadID == beadID {
			continue
		}
		active := candidate.DispatchState == DispatchSending || candidate.ClearState != ClearStateNone
		switch candidate.Status {
		case StatusClaiming, StatusClaimed, StatusAssigned, StatusWorking:
			active = true
		}
		if !active {
			continue
		}
		occupied, err := assignmentOccupiesTarget(candidate, occupancyKey)
		if err != nil {
			return candidate, err
		}
		if occupied {
			return candidate, nil
		}
	}
	return nil, nil
}

func assignmentOccupiesTarget(candidate *Assignment, occupancyKey string) (bool, error) {
	if candidate == nil {
		return false, nil
	}
	canonicalKey, err := CanonicalPaneIdentity(candidate)
	if err != nil {
		return false, err
	}
	return canonicalKey == strings.TrimSpace(occupancyKey), nil
}

func isPhysicalPaneOccupancyKey(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 2 || value[0] != '%' {
		return false
	}
	digits := value[1:]
	if len(digits) > 1 && digits[0] == '0' {
		return false
	}
	for _, ch := range digits {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	_, err := strconv.ParseUint(digits, 10, strconv.IntSize)
	return err == nil
}

func isCanonicalPaneIdentity(value string) bool {
	return isPhysicalPaneOccupancyKey(value)
}

func validateRequiredLease(req AtomicRequest, lease LeaseReceipt, now time.Time) error {
	var validationErrors []error
	explicit, explicitErr := normalizedUniqueReservationPaths(req.RequestedPaths, "requested reservation path")
	if explicitErr != nil {
		validationErrors = append(validationErrors, explicitErr)
	}
	receiptRequested, requestedErr := normalizedUniqueReservationPaths(lease.Requested, "reservation receipt requested path")
	if requestedErr != nil {
		validationErrors = append(validationErrors, requestedErr)
	}

	expected := explicit
	if req.AllowReservationDiscovery && len(explicit) == 0 {
		expected = receiptRequested
	} else if requestedErr == nil && !sameReservationPathSet(explicit, receiptRequested) {
		validationErrors = append(validationErrors, fmt.Errorf("reservation receipt requested paths %v do not match %v", receiptRequested, explicit))
	}
	if len(expected) == 0 {
		validationErrors = append(validationErrors, ErrReservationPathsRequired)
	}

	granted, grantedErr := normalizedUniqueReservationPaths(lease.Granted, "reservation receipt granted path")
	if grantedErr != nil {
		validationErrors = append(validationErrors, grantedErr)
	}
	if grantedErr == nil {
		missing := reservationPathDifference(expected, granted)
		if len(missing) > 0 {
			validationErrors = append(validationErrors, fmt.Errorf("required file reservations were not granted: %s", strings.Join(missing, ", ")))
		}
		unexpected := reservationPathDifference(granted, expected)
		if len(unexpected) > 0 {
			validationErrors = append(validationErrors, fmt.Errorf("reservation receipt granted unexpected paths: %s", strings.Join(unexpected, ", ")))
		}
	}

	seenIDs := make(map[int]struct{}, len(lease.ReservationIDs))
	for _, id := range lease.ReservationIDs {
		if id <= 0 {
			validationErrors = append(validationErrors, fmt.Errorf("reservation receipt contains invalid ID %d", id))
			continue
		}
		if _, duplicate := seenIDs[id]; duplicate {
			validationErrors = append(validationErrors, fmt.Errorf("reservation receipt repeats ID %d", id))
			continue
		}
		seenIDs[id] = struct{}{}
	}
	if len(seenIDs) == 0 {
		validationErrors = append(validationErrors, errors.New("reservation receipt has no positive durable reservation IDs"))
	}
	if lease.ExpiresAt == nil || lease.ExpiresAt.IsZero() || !lease.ExpiresAt.After(now) {
		validationErrors = append(validationErrors, errors.New("reservation receipt has no live future expiry"))
	}
	return errors.Join(validationErrors...)
}

func validateLeaseReceipt(req ReservationRequest, lease LeaseReceipt) error {
	if strings.TrimSpace(lease.AgentName) == "" {
		return errors.New("reservation receipt has no agent")
	}
	if strings.TrimSpace(lease.AgentName) != strings.TrimSpace(req.AgentName) {
		return fmt.Errorf("reservation receipt agent mismatch: got %q, want %q", lease.AgentName, req.AgentName)
	}
	if strings.TrimSpace(lease.Target) == "" {
		return errors.New("reservation receipt has no target")
	}
	if strings.TrimSpace(lease.Target) != strings.TrimSpace(req.Target) {
		return fmt.Errorf("reservation receipt target mismatch: got %q, want %q", lease.Target, req.Target)
	}
	return nil
}

func normalizedUniqueReservationPaths(paths []string, field string) ([]string, error) {
	result := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	var validationErrors []error
	for _, raw := range paths {
		path := strings.TrimSpace(raw)
		if path == "" {
			validationErrors = append(validationErrors, fmt.Errorf("%s cannot be empty", field))
			continue
		}
		if _, duplicate := seen[path]; duplicate {
			validationErrors = append(validationErrors, fmt.Errorf("%s %q is duplicated", field, path))
			continue
		}
		seen[path] = struct{}{}
		result = append(result, path)
	}
	return result, errors.Join(validationErrors...)
}

func sameReservationPathSet(left, right []string) bool {
	return len(left) == len(right) && len(reservationPathDifference(left, right)) == 0
}

func reservationPathDifference(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, path := range right {
		rightSet[path] = struct{}{}
	}
	difference := make([]string, 0)
	for _, path := range left {
		if _, found := rightSet[path]; !found {
			difference = append(difference, path)
		}
	}
	return difference
}

func validateDispatchReceipt(receipt DispatchReceipt) error {
	if strings.TrimSpace(receipt.DeliveryID) == "" {
		return errors.New("dispatch returned no concrete delivery receipt")
	}
	return nil
}

func leaseHasReservationHandles(lease LeaseReceipt) bool {
	return len(lease.ReservationIDs) > 0 || len(lease.Granted) > 0
}

func assignmentHasReservationHandles(a *Assignment) bool {
	return a != nil && (len(a.ReservationIDs) > 0 || len(a.ReservedPaths) > 0)
}

func assignmentHasUnresolvedReservation(a *Assignment) bool {
	if a == nil {
		return false
	}
	if assignmentHasReservationHandles(a) {
		return true
	}
	switch effectiveReservationState(a) {
	case ReservationReserving, ReservationUnknown:
		return true
	case ReservationReserved:
		return !a.ReservationCompleted || strings.TrimSpace(a.ReservationError) != ""
	default:
		return false
	}
}

// ReservationOutcomeNeedsReconciliation reports whether stored handles alone
// are insufficient to prove the complete external lease set. Cleanup callers
// must enumerate the original requested paths before releasing these outcomes.
func ReservationOutcomeNeedsReconciliation(a *Assignment) bool {
	if a == nil || !a.ReservationRequired {
		return false
	}
	hasHandles := assignmentHasReservationHandles(a)
	switch effectiveReservationState(a) {
	case ReservationReleased:
		return false
	case ReservationPending:
		return a.ReservationAttempts > 0 || hasHandles
	case ReservationFailed:
		return hasHandles
	case ReservationReserved:
		return !a.ReservationCompleted || strings.TrimSpace(a.ReservationError) != ""
	case ReservationReserving, ReservationUnknown:
		return true
	default:
		return a.ReservationAttempts > 0 || hasHandles || strings.TrimSpace(a.ReservationError) != ""
	}
}

func classifyReservationFailure(lease LeaseReceipt, reservationErr error) (ReservationState, error) {
	if leaseHasReservationHandles(lease) || IsReservationReleaseRequired(reservationErr) {
		return ReservationReserved, RequireReservationRelease(reservationErr)
	}
	if IsGuaranteedNoReservation(reservationErr) {
		return ReservationFailed, reservationErr
	}
	return ReservationUnknown, errors.Join(ErrReservationOutcomeUnknown, reservationErr)
}

func classifyValidatedReservation(lease LeaseReceipt, validationErr error) (ReservationState, error) {
	if validationErr == nil {
		return ReservationReserved, nil
	}
	if leaseHasReservationHandles(lease) {
		return ReservationReserved, RequireReservationRelease(validationErr)
	}
	// A successful reservation response with no durable handles cannot leave a
	// releasable lease. Validation failures such as an empty grant are therefore
	// known, retryable failures rather than ambiguous outcomes.
	return ReservationFailed, GuaranteeNoReservation(validationErr)
}

func reservationNeedsRefresh(a *Assignment, now time.Time) bool {
	if a == nil {
		return true
	}
	if effectiveReservationState(a) == ReservationReserved {
		return reservationExpired(a, now)
	}
	return true
}

func reservationExpired(a *Assignment, now time.Time) bool {
	return a != nil && a.ReservationExpiresAt != nil && !a.ReservationExpiresAt.After(now)
}

func reservationOutcomeAmbiguous(a *Assignment) bool {
	state := effectiveReservationState(a)
	return state == ReservationReserving || state == ReservationUnknown
}

func effectiveClaimState(a *Assignment) ClaimState {
	if a == nil {
		return ClaimPending
	}
	if a.ClaimState != "" {
		return a.ClaimState
	}
	if a.ClaimedAt != nil || a.ClaimStatus != "" || a.Status == StatusClaimed || a.Status == StatusAssigned || a.Status == StatusWorking {
		return ClaimClaimed
	}
	return ClaimPending
}

func effectiveReservationState(a *Assignment) ReservationState {
	if a == nil {
		return ReservationPending
	}
	if a.ReservationState != "" {
		return a.ReservationState
	}
	if a.ReservationCompleted || len(a.ReservationIDs) > 0 || len(a.ReservedPaths) > 0 {
		return ReservationReserved
	}
	return ReservationPending
}

func matchesAtomicRawIntent(a *Assignment, req AtomicRequest) bool {
	if a == nil {
		return false
	}
	intentSHA256 := req.IntentSHA256
	if intentSHA256 == "" {
		intentSHA256 = PromptSHA256(req.Prompt)
	}
	storedIntentSHA256 := a.IntentSHA256
	if storedIntentSHA256 == "" {
		storedIntentSHA256 = a.PromptSHA256
	}
	storedOccupancyKey := a.OccupancyKey
	if storedOccupancyKey == "" {
		storedOccupancyKey = a.DispatchTarget
	}
	return a.DispatchTarget == req.Target && storedOccupancyKey == normalizeOccupancyKey(req.Target, req.OccupancyKey) &&
		a.AgentName == req.AgentName && a.AgentType == req.AgentType &&
		storedIntentSHA256 == intentSHA256 &&
		a.ReservationRequired == req.RequireReservation &&
		a.ReservationDiscovery == req.AllowReservationDiscovery &&
		stringSlicesEqual(a.ReservationInputPaths, req.RequestedPaths)
}

func matchesAtomicIntent(a *Assignment, req AtomicRequest) bool {
	return matchesAtomicRawIntent(a, req) &&
		a.BeadTitle == req.BeadTitle &&
		a.PromptSHA256 == PromptSHA256(req.Prompt)
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func leaseFromAssignment(a *Assignment) LeaseReceipt {
	if a == nil {
		return LeaseReceipt{}
	}
	return LeaseReceipt{
		AgentName:      a.ReservationAgent,
		Target:         a.ReservationTarget,
		Requested:      append([]string(nil), a.ReservationRequested...),
		Granted:        append([]string(nil), a.ReservedPaths...),
		ReservationIDs: append([]int(nil), a.ReservationIDs...),
		ExpiresAt:      cloneTimePtr(a.ReservationExpiresAt),
	}
}

// RecordAtomicIntent creates the durable transaction identity before any
// external operation. A different live idempotency key is never overwritten.
func (s *AssignmentStore) RecordAtomicIntent(req AtomicRequest, actor string, createdAt time.Time) (*Assignment, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	existing := s.Assignments[req.BeadID]
	if existing != nil && isTerminalAssignmentStatus(existing.Status) &&
		existing.IdempotencyKey != req.IdempotencyKey && strings.TrimSpace(existing.PendingCompletionEventID) != "" {
		return nil, fmt.Errorf("%w: acknowledge event %s for %s before starting a new generation", ErrCompletionEventPending, existing.PendingCompletionEventID, req.BeadID)
	}
	replacementStart := req.ReplaceWorkingAssignment && existing != nil && existing.IdempotencyKey != req.IdempotencyKey
	if req.ReplaceWorkingAssignment && existing == nil {
		return nil, fmt.Errorf("%w: no durable assignment exists for %s", ErrWorkingReplacementNotAllowed, req.BeadID)
	}
	if replacementStart {
		if err := validateWorkingReplacement(existing, req); err != nil {
			return nil, err
		}
		expectedActor, err := workingReplacementClaimActor(existing)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(actor) != expectedActor {
			return nil, fmt.Errorf("%w: replacement claim actor changed from %s to %s", ErrClaimConflict, expectedActor, actor)
		}
		req.claimRequiresNonTerminal = true
	}
	if existing != nil && existing.ClearState != ClearStateNone && !replacementStart {
		return nil, fmt.Errorf("%w: %s is awaiting reservation release", ErrClaimConflict, req.BeadID)
	}
	if existing != nil && existing.IdempotencyKey != "" && existing.IdempotencyKey != req.IdempotencyKey && !replacementStart {
		switch existing.Status {
		case StatusClaiming, StatusClaimed, StatusAssigned, StatusWorking:
			return nil, fmt.Errorf("%w: %s is recorded for %s", ErrClaimConflict, req.BeadID, existing.ClaimActor)
		}
		if isTerminalAssignmentStatus(existing.Status) && assignmentHasUnresolvedReservation(existing) {
			return nil, fmt.Errorf("%w: reconcile and clear assignment %s before starting a new generation", ErrReservationReleaseRequired, req.BeadID)
		}
	}
	if existing != nil && existing.IdempotencyKey == req.IdempotencyKey {
		if existing.ClaimActor != "" && existing.ClaimActor != actor {
			return nil, fmt.Errorf("%w: idempotency key actor changed from %s to %s", ErrClaimConflict, existing.ClaimActor, actor)
		}
		if !matchesAtomicIntent(existing, req) {
			return nil, fmt.Errorf("idempotency key %s was reused for a different assignment intent", req.IdempotencyKey)
		}
		if req.claimRequiresNonTerminal && !existing.ClaimRequiresNonTerminal {
			existing.ClaimRequiresNonTerminal = true
			if err := s.saveLocked(); err != nil {
				return nil, err
			}
		}
		return cloneAssignment(existing), nil
	}

	assignment := &Assignment{
		BeadID:                   req.BeadID,
		BeadTitle:                req.BeadTitle,
		Pane:                     req.Pane,
		AgentType:                req.AgentType,
		AgentName:                req.AgentName,
		Status:                   StatusClaiming,
		AssignedAt:               createdAt,
		IdempotencyKey:           req.IdempotencyKey,
		ClaimActor:               actor,
		ClaimState:               ClaimPending,
		ClaimRequiresNonTerminal: req.claimRequiresNonTerminal,
		ReservationRequired:      req.RequireReservation,
		ReservationDiscovery:     req.AllowReservationDiscovery,
		ReservationInputPaths:    append([]string(nil), req.RequestedPaths...),
		ReservationState:         ReservationPending,
		DispatchState:            DispatchPending,
		DispatchTarget:           req.Target,
		OccupancyKey:             normalizeOccupancyKey(req.Target, req.OccupancyKey),
		PromptSHA256:             PromptSHA256(req.Prompt),
		IntentSHA256:             req.IntentSHA256,
		PendingPrompt:            req.Prompt,
	}
	previous := existing
	if replacementStart {
		assignment.RetryCount = existing.RetryCount
	}
	s.Assignments[req.BeadID] = assignment
	if previous != nil {
		if s.replace == nil {
			s.replace = make(map[string]struct{})
		}
		s.replace[req.BeadID] = struct{}{}
	}
	if err := s.saveLocked(); err != nil {
		var concurrentMutation *ConcurrentMutationError
		if !errors.As(err, &concurrentMutation) {
			if previous == nil {
				delete(s.Assignments, req.BeadID)
			} else {
				s.Assignments[req.BeadID] = previous
			}
			delete(s.replace, req.BeadID)
		}
		return nil, err
	}
	return cloneAssignment(assignment), nil
}

// RecordAtomicClaimStarted writes the ambiguity barrier immediately before
// calling the external tracker.
func (s *AssignmentStore) RecordAtomicClaimStarted(beadID, idempotencyKey string, startedAt time.Time) (*Assignment, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	a, err := s.atomicAssignmentLocked(beadID, idempotencyKey)
	if err != nil {
		return nil, err
	}
	if effectiveClaimState(a) == ClaimClaimed {
		return cloneAssignment(a), nil
	}
	a.Status = StatusClaiming
	a.ClaimState = ClaimClaiming
	a.ClaimAttempts++
	a.ClaimStartedAt = cloneTimePtr(&startedAt)
	a.ClaimError = ""
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return cloneAssignment(a), nil
}

// RecordAtomicClaim durably records tracker ownership before reservations or
// delivery.
func (s *AssignmentStore) RecordAtomicClaim(req AtomicRequest, claim ClaimReceipt) (*Assignment, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	existing := s.Assignments[req.BeadID]
	if existing == nil {
		return nil, fmt.Errorf("[ASSIGN] Atomic intent not found: %s", req.BeadID)
	}
	if existing.IdempotencyKey != req.IdempotencyKey {
		return nil, fmt.Errorf("%w: idempotency key does not own %s", ErrClaimConflict, req.BeadID)
	}
	if existing.ClaimActor != "" && existing.ClaimActor != claim.Actor {
		return nil, fmt.Errorf("%w: idempotency key actor changed from %s to %s", ErrClaimConflict, existing.ClaimActor, claim.Actor)
	}
	if !matchesAtomicIntent(existing, req) {
		return nil, fmt.Errorf("idempotency key %s was reused for a different assignment intent", req.IdempotencyKey)
	}
	existing.Status = StatusClaimed
	existing.ClaimActor = claim.Actor
	existing.ClaimState = ClaimClaimed
	existing.ClaimStatus = claim.Status
	existing.ClaimError = ""
	existing.ClaimedAt = cloneTimePtr(&claim.ClaimedAt)
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return cloneAssignment(existing), nil
}

// RecordAtomicClaimUncertain records a returned claim error without erasing
// the pre-call barrier. Conflict and atomic eligibility rejection are known
// failures; all other errors remain unknown until reconciled.
func (s *AssignmentStore) RecordAtomicClaimUncertain(beadID, idempotencyKey string, claimErr error) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	a, err := s.atomicAssignmentLocked(beadID, idempotencyKey)
	if err != nil {
		return err
	}
	a.ClaimState = ClaimUnknown
	if errors.Is(claimErr, ErrClaimIneligible) {
		a.ClaimState = ClaimIneligible
		a.Status = StatusFailed
		failedAt := time.Now().UTC()
		a.FailedAt = &failedAt
	} else if errors.Is(claimErr, ErrClaimConflict) {
		a.ClaimState = ClaimFailed
		a.Status = StatusFailed
		failedAt := time.Now().UTC()
		a.FailedAt = &failedAt
	}
	a.ClaimError = ""
	if claimErr != nil {
		a.ClaimError = claimErr.Error()
	}
	return s.saveLocked()
}

// RecordAtomicReservationStarted writes the ambiguity barrier immediately
// before calling the external reservation service.
func (s *AssignmentStore) RecordAtomicReservationStarted(beadID, idempotencyKey string, startedAt time.Time) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	a, err := s.atomicAssignmentLocked(beadID, idempotencyKey)
	if err != nil {
		return err
	}
	a.ReservationState = ReservationReserving
	a.ReservationAttempts++
	a.ReservationStartedAt = cloneTimePtr(&startedAt)
	a.ReservationError = ""
	return s.saveLocked()
}

// RecordAtomicReservation persists Agent Mail handoff metadata even when the
// reservation call partially succeeds and returns an error.
func (s *AssignmentStore) RecordAtomicReservation(beadID, idempotencyKey string, state ReservationState, lease LeaseReceipt, reservationErr error) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	a, err := s.atomicAssignmentLocked(beadID, idempotencyKey)
	if err != nil {
		return err
	}
	if state == ReservationFailed && leaseHasReservationHandles(lease) {
		state = ReservationReserved
		reservationErr = RequireReservationRelease(reservationErr)
	}
	if state == ReservationReserved && reservationErr != nil {
		if leaseHasReservationHandles(lease) || IsReservationReleaseRequired(reservationErr) {
			reservationErr = RequireReservationRelease(reservationErr)
		} else {
			state = ReservationFailed
			reservationErr = GuaranteeNoReservation(reservationErr)
		}
	}
	if state == ReservationUnknown && reservationErr == nil {
		reservationErr = ErrReservationOutcomeUnknown
	}
	a.ReservationAgent = lease.AgentName
	a.ReservationTarget = lease.Target
	a.ReservationRequested = append([]string(nil), lease.Requested...)
	a.ReservedPaths = append([]string(nil), lease.Granted...)
	a.ReservationIDs = append([]int(nil), lease.ReservationIDs...)
	a.ReservationExpiresAt = cloneTimePtr(lease.ExpiresAt)
	a.ReservationState = state
	a.ReservationCompleted = state == ReservationReserved && reservationErr == nil
	a.ReservationError = ""
	if reservationErr != nil {
		a.ReservationError = reservationErr.Error()
	}
	return s.saveLocked()
}

// RecordAtomicDispatchStarted writes the ambiguity barrier before transport.
func (s *AssignmentStore) RecordAtomicDispatchStarted(beadID, idempotencyKey string, startedAt time.Time) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	a, err := s.atomicAssignmentLocked(beadID, idempotencyKey)
	if err != nil {
		return err
	}
	if a.DispatchState == DispatchSent {
		return nil
	}
	if a.DispatchState == DispatchSending {
		return ErrDispatchOutcomeUnknown
	}
	a.DispatchState = DispatchSending
	a.DispatchAttempts++
	a.DispatchStartedAt = cloneTimePtr(&startedAt)
	a.LastDispatchError = ""
	return s.saveLocked()
}

// RecordAtomicDispatchFailed records a known transport failure as retryable.
func (s *AssignmentStore) RecordAtomicDispatchFailed(beadID, idempotencyKey string, dispatchErr error) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	a, err := s.atomicAssignmentLocked(beadID, idempotencyKey)
	if err != nil {
		return err
	}
	a.DispatchState = DispatchPending
	a.LastDispatchError = ""
	if dispatchErr != nil {
		a.LastDispatchError = dispatchErr.Error()
	}
	return s.saveLocked()
}

// RecordAtomicDispatchSent commits the final delivery receipt and exposes the
// assignment to existing assigned/working consumers.
func (s *AssignmentStore) RecordAtomicDispatchSent(beadID, idempotencyKey, prompt string, receipt DispatchReceipt, dispatchedAt time.Time) error {
	if err := validateDispatchReceipt(receipt); err != nil {
		return err
	}
	s.mutex.Lock()
	a, err := s.atomicAssignmentLocked(beadID, idempotencyKey)
	if err != nil {
		s.mutex.Unlock()
		return err
	}
	previous := cloneAssignment(a)
	a.Status = StatusAssigned
	a.PromptSent = prompt
	a.PendingPrompt = ""
	a.DispatchState = DispatchSent
	a.DispatchedAt = cloneTimePtr(&dispatchedAt)
	a.DispatchReceiptID = receipt.DeliveryID
	a.DispatchDuration = receipt.Duration
	a.LastDispatchError = ""
	if err := s.saveLocked(); err != nil {
		var concurrentMutation *ConcurrentMutationError
		if !errors.As(err, &concurrentMutation) {
			s.Assignments[beadID] = previous
		}
		s.mutex.Unlock()
		return err
	}
	cloned := cloneAssignment(a)
	s.mutex.Unlock()

	emitAtomicAssignmentEvent(s.SessionName, cloned)
	return nil
}

func (s *AssignmentStore) atomicAssignmentLocked(beadID, idempotencyKey string) (*Assignment, error) {
	a := s.Assignments[beadID]
	if a == nil {
		return nil, fmt.Errorf("[ASSIGN] Assignment not found: %s", beadID)
	}
	if a.IdempotencyKey != idempotencyKey {
		return nil, fmt.Errorf("%w: idempotency key does not own %s", ErrClaimConflict, beadID)
	}
	return a, nil
}

func emitAtomicAssignmentEvent(session string, a *Assignment) {
	if a == nil {
		return
	}
	events.DefaultEmitter().Emit(events.NewWebhookEvent(
		events.WebhookBeadAssigned,
		session,
		fmt.Sprintf("%d", a.Pane),
		a.AgentType,
		fmt.Sprintf("Bead assigned: %s", a.BeadID),
		map[string]string{
			"bead_id":         a.BeadID,
			"bead_title":      a.BeadTitle,
			"pane_index":      fmt.Sprintf("%d", a.Pane),
			"agent_type":      a.AgentType,
			"agent_name":      a.AgentName,
			"claim_actor":     a.ClaimActor,
			"idempotency_key": a.IdempotencyKey,
		},
	))
}

package rotation

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// LifecycleState is the unified state of a pane undergoing context rotation,
// coordinator-triggered rotation, CAAM account failover, or auth restart.
type LifecycleState string

const (
	StateIdle              LifecycleState = "idle"
	StateDraining          LifecycleState = "draining"
	StateHandingOff        LifecycleState = "handing_off"
	StateVerifying         LifecycleState = "verifying"
	StateActive            LifecycleState = "active"
	StateFailedRecoverable LifecycleState = "failed_recoverable"
)

var (
	// ErrLifecycleLeaseHeld indicates another rotation/failover owner currently
	// holds an active in-flight transition lease for the target pane.
	ErrLifecycleLeaseHeld = errors.New("pane lifecycle lease held by another owner")
	// ErrLeaseFenced indicates a stale or mismatched lease token attempted a
	// transition after ownership expired, completed, or was taken over.
	ErrLeaseFenced = errors.New("pane lifecycle lease is stale or fenced")
	// ErrInvalidTransition indicates a disallowed FSM state transition.
	ErrInvalidTransition = errors.New("invalid pane lifecycle state transition")
)

// validLifecycleTransitions defines the canonical transition table shared by
// context rotation, coordinator rotation, CAAM failover, and auth restart.
var validLifecycleTransitions = map[LifecycleState]map[LifecycleState]bool{
	StateIdle: {
		StateDraining: true,
	},
	StateDraining: {
		StateHandingOff:        true,
		StateFailedRecoverable: true,
		StateIdle:              true,
	},
	StateHandingOff: {
		StateVerifying:         true,
		StateFailedRecoverable: true,
	},
	StateVerifying: {
		StateActive:            true,
		StateFailedRecoverable: true,
	},
	StateActive: {
		StateIdle: true,
	},
	StateFailedRecoverable: {
		StateDraining: true,
		StateIdle:     true,
	},
}

// ValidTransition reports whether moving from -> to is permitted by the unified
// rotation/failover FSM transition table.
func ValidTransition(from, to LifecycleState) bool {
	allowed, ok := validLifecycleTransitions[from]
	if !ok {
		return false
	}
	return allowed[to]
}

// LifecycleTransition records one audited state change on a pane lifecycle.
type LifecycleTransition struct {
	From      LifecycleState `json:"from"`
	To        LifecycleState `json:"to"`
	OwnerID   string         `json:"owner_id"`
	OpID      string         `json:"op_id"`
	Kind      string         `json:"kind"`
	Reason    string         `json:"reason,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
}

// PaneLifecycle is an immutable snapshot of a pane's rotation/failover FSM state.
type PaneLifecycle struct {
	PaneKey        string                `json:"pane_key"`
	State          LifecycleState        `json:"state"`
	OwnerID        string                `json:"owner_id,omitempty"`
	OpID           string                `json:"op_id,omitempty"`
	Kind           string                `json:"kind,omitempty"`
	Token          uint64                `json:"token,omitempty"`
	FailedFrom     LifecycleState        `json:"failed_from,omitempty"`
	LastCheckpoint string                `json:"last_checkpoint,omitempty"`
	RecoveryHint   string                `json:"recovery_hint,omitempty"`
	UpdatedAt      time.Time             `json:"updated_at"`
	History        []LifecycleTransition `json:"history,omitempty"`
}

// LifecycleLease is a fenced single-owner capability token for one pane's
// rotation or failover attempt.
type LifecycleLease struct {
	PaneKey string
	OwnerID string
	OpID    string
	Kind    string
	Token   uint64
}

// LifecycleCoordinator enforces single-owner pane lease fencing, valid FSM
// transitions, idempotent retries, and unambiguous crash-recovery states across
// all rotation and failover engines.
type LifecycleCoordinator struct {
	mu      sync.Mutex
	panes   map[string]*PaneLifecycle
	nextTok atomic.Uint64
	now     func() time.Time
}

var (
	defaultLifecycleOnce sync.Once
	defaultLifecycle     *LifecycleCoordinator
)

// DefaultLifecycleCoordinator returns the process-wide rotation/failover FSM
// coordinator shared across context rotation, coordinator rotation, CAAM
// failover, and auth restart.
func DefaultLifecycleCoordinator() *LifecycleCoordinator {
	defaultLifecycleOnce.Do(func() {
		defaultLifecycle = NewLifecycleCoordinator()
	})
	return defaultLifecycle
}

// NewLifecycleCoordinator constructs an isolated FSM coordinator (used by the
// default singleton and parallel tests).
func NewLifecycleCoordinator() *LifecycleCoordinator {
	return &LifecycleCoordinator{
		panes: make(map[string]*PaneLifecycle),
		now:   time.Now,
	}
}

const maxLifecycleHistory = 64

func isInFlightState(st LifecycleState) bool {
	return st == StateDraining || st == StateHandingOff || st == StateVerifying
}

func appendLifecycleHistory(cur *PaneLifecycle, tr LifecycleTransition) {
	cur.History = append(cur.History, tr)
	if len(cur.History) > maxLifecycleHistory {
		trimmed := make([]LifecycleTransition, maxLifecycleHistory)
		copy(trimmed, cur.History[len(cur.History)-maxLifecycleHistory:])
		cur.History = trimmed
	}
}

// Acquire claims exclusive ownership for paneKey and transitions idle (or
// failed_recoverable on retry) into draining. Re-acquiring with the same
// (ownerID, opID) while already active is idempotent and returns the live lease.
func (c *LifecycleCoordinator) Acquire(paneKey, ownerID, opID, kind string) (*LifecycleLease, error) {
	paneKey = strings.TrimSpace(paneKey)
	ownerID = strings.TrimSpace(ownerID)
	opID = strings.TrimSpace(opID)
	kind = strings.TrimSpace(kind)
	if paneKey == "" {
		return nil, fmt.Errorf("%w: pane key is required", ErrInvalidTransition)
	}
	if ownerID == "" {
		ownerID = kind
	}
	if opID == "" {
		opID = fmt.Sprintf("%s-%d", ownerID, c.nextTok.Add(1))
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	cur, ok := c.panes[paneKey]
	if !ok {
		cur = &PaneLifecycle{
			PaneKey: paneKey,
			State:   StateIdle,
		}
		c.panes[paneKey] = cur
	}

	if isInFlightState(cur.State) {
		if cur.OwnerID == ownerID && cur.OpID == opID {
			return &LifecycleLease{
				PaneKey: paneKey,
				OwnerID: cur.OwnerID,
				OpID:    cur.OpID,
				Kind:    cur.Kind,
				Token:   cur.Token,
			}, nil
		}
		return nil, fmt.Errorf("%w: pane %s currently in %s owned by %s (kind=%s op=%s)",
			ErrLifecycleLeaseHeld, paneKey, cur.State, cur.OwnerID, cur.Kind, cur.OpID)
	}

	if !ValidTransition(cur.State, StateDraining) {
		return nil, fmt.Errorf("%w: cannot acquire pane %s from state %s", ErrInvalidTransition, paneKey, cur.State)
	}

	now := c.now().UTC()
	tok := c.nextTok.Add(1)
	from := cur.State
	cur.State = StateDraining
	cur.OwnerID = ownerID
	cur.OpID = opID
	cur.Kind = kind
	cur.Token = tok
	cur.FailedFrom = ""
	cur.LastCheckpoint = "acquired"
	cur.RecoveryHint = ""
	cur.UpdatedAt = now
	appendLifecycleHistory(cur, LifecycleTransition{
		From:      from,
		To:        StateDraining,
		OwnerID:   ownerID,
		OpID:      opID,
		Kind:      kind,
		Reason:    "lease_acquired",
		Timestamp: now,
	})

	slog.Info("rotation fsm transition",
		"pane", paneKey,
		"owner", ownerID,
		"op_id", opID,
		"kind", kind,
		"from", string(from),
		"to", string(StateDraining))

	return &LifecycleLease{
		PaneKey: paneKey,
		OwnerID: ownerID,
		OpID:    opID,
		Kind:    kind,
		Token:   tok,
	}, nil
}

// Transition advances the leased pane to next while enforcing owner/token
// fencing and the canonical transition table. Re-applying the current state
// with the same lease is idempotent.
func (c *LifecycleCoordinator) Transition(lease *LifecycleLease, next LifecycleState, reason string) error {
	if lease == nil {
		return ErrLeaseFenced
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	cur, ok := c.panes[lease.PaneKey]
	if !ok || cur.Token != lease.Token || cur.OwnerID != lease.OwnerID || cur.OpID != lease.OpID {
		return fmt.Errorf("%w: pane %s owner %s op %s", ErrLeaseFenced, lease.PaneKey, lease.OwnerID, lease.OpID)
	}
	if !isInFlightState(cur.State) {
		return fmt.Errorf("%w: pane %s is in non-active state %s; re-acquire lease", ErrLeaseFenced, lease.PaneKey, cur.State)
	}
	if cur.State == next {
		return nil // idempotent retry
	}
	if next == StateIdle || next == StateDraining || next == StateActive || next == StateFailedRecoverable {
		return fmt.Errorf("%w: pane %s cannot use Transition to move %s -> %s", ErrInvalidTransition, lease.PaneKey, cur.State, next)
	}
	if !ValidTransition(cur.State, next) {
		return fmt.Errorf("%w: pane %s cannot move %s -> %s", ErrInvalidTransition, lease.PaneKey, cur.State, next)
	}

	now := c.now().UTC()
	from := cur.State
	cur.State = next
	cur.LastCheckpoint = string(next)
	cur.UpdatedAt = now
	appendLifecycleHistory(cur, LifecycleTransition{
		From:      from,
		To:        next,
		OwnerID:   lease.OwnerID,
		OpID:      lease.OpID,
		Kind:      lease.Kind,
		Reason:    reason,
		Timestamp: now,
	})

	slog.Info("rotation fsm transition",
		"pane", lease.PaneKey,
		"owner", lease.OwnerID,
		"op_id", lease.OpID,
		"kind", lease.Kind,
		"from", string(from),
		"to", string(next),
		"reason", reason)
	return nil
}

// FailRecoverable moves an active lease into failed_recoverable and records
// the interrupted checkpoint and recovery hint so a retry or operator can
// recover unambiguously without split-brain ownership.
func (c *LifecycleCoordinator) FailRecoverable(lease *LifecycleLease, checkpoint, reason, recoveryHint string) error {
	if lease == nil {
		return ErrLeaseFenced
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	cur, ok := c.panes[lease.PaneKey]
	if !ok || cur.Token != lease.Token || cur.OwnerID != lease.OwnerID || cur.OpID != lease.OpID {
		return fmt.Errorf("%w: pane %s owner %s op %s", ErrLeaseFenced, lease.PaneKey, lease.OwnerID, lease.OpID)
	}
	if cur.State == StateFailedRecoverable {
		return nil
	}
	if !ValidTransition(cur.State, StateFailedRecoverable) {
		return fmt.Errorf("%w: pane %s cannot fail from %s", ErrInvalidTransition, lease.PaneKey, cur.State)
	}

	now := c.now().UTC()
	from := cur.State
	cur.FailedFrom = from
	cur.State = StateFailedRecoverable
	if strings.TrimSpace(checkpoint) != "" {
		cur.LastCheckpoint = checkpoint
	}
	cur.RecoveryHint = recoveryHint
	cur.UpdatedAt = now
	appendLifecycleHistory(cur, LifecycleTransition{
		From:      from,
		To:        StateFailedRecoverable,
		OwnerID:   lease.OwnerID,
		OpID:      lease.OpID,
		Kind:      lease.Kind,
		Reason:    reason,
		Timestamp: now,
	})

	slog.Warn("rotation fsm failed_recoverable",
		"pane", lease.PaneKey,
		"owner", lease.OwnerID,
		"op_id", lease.OpID,
		"kind", lease.Kind,
		"from", string(from),
		"checkpoint", cur.LastCheckpoint,
		"reason", reason,
		"recovery_hint", recoveryHint)
	return nil
}

// Complete finishes a verified lifecycle transition (verifying -> active -> idle)
// and releases the pane for subsequent cycles.
func (c *LifecycleCoordinator) Complete(lease *LifecycleLease, reason string) error {
	if lease == nil {
		return ErrLeaseFenced
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	cur, ok := c.panes[lease.PaneKey]
	if !ok || cur.Token != lease.Token || cur.OwnerID != lease.OwnerID || cur.OpID != lease.OpID {
		return fmt.Errorf("%w: pane %s owner %s op %s", ErrLeaseFenced, lease.PaneKey, lease.OwnerID, lease.OpID)
	}
	if cur.State == StateIdle {
		if cur.LastCheckpoint == "completed" {
			return nil
		}
		return fmt.Errorf("%w: pane %s was already released via %s", ErrInvalidTransition, lease.PaneKey, cur.LastCheckpoint)
	}

	now := c.now().UTC()
	if cur.State == StateVerifying {
		cur.State = StateActive
		appendLifecycleHistory(cur, LifecycleTransition{
			From:      StateVerifying,
			To:        StateActive,
			OwnerID:   lease.OwnerID,
			OpID:      lease.OpID,
			Kind:      lease.Kind,
			Reason:    reason,
			Timestamp: now,
		})
	}
	if cur.State != StateActive {
		return fmt.Errorf("%w: pane %s cannot complete from %s", ErrInvalidTransition, lease.PaneKey, cur.State)
	}

	cur.State = StateIdle
	cur.LastCheckpoint = "completed"
	cur.RecoveryHint = ""
	cur.UpdatedAt = now
	appendLifecycleHistory(cur, LifecycleTransition{
		From:      StateActive,
		To:        StateIdle,
		OwnerID:   lease.OwnerID,
		OpID:      lease.OpID,
		Kind:      lease.Kind,
		Reason:    "lease_released",
		Timestamp: now,
	})
	return nil
}

// Abort safely returns a draining or failed_recoverable pane back to idle
// (for example when compaction resolved pressure before handoff or a preflight
// guard declined failover before mutation).
func (c *LifecycleCoordinator) Abort(lease *LifecycleLease, reason string) error {
	if lease == nil {
		return ErrLeaseFenced
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	cur, ok := c.panes[lease.PaneKey]
	if !ok || cur.Token != lease.Token || cur.OwnerID != lease.OwnerID || cur.OpID != lease.OpID {
		return fmt.Errorf("%w: pane %s owner %s op %s", ErrLeaseFenced, lease.PaneKey, lease.OwnerID, lease.OpID)
	}
	if cur.State == StateIdle {
		if cur.LastCheckpoint == "aborted" {
			return nil
		}
		return fmt.Errorf("%w: pane %s was already released via %s", ErrInvalidTransition, lease.PaneKey, cur.LastCheckpoint)
	}
	if !ValidTransition(cur.State, StateIdle) {
		return fmt.Errorf("%w: pane %s cannot abort from %s", ErrInvalidTransition, lease.PaneKey, cur.State)
	}

	now := c.now().UTC()
	from := cur.State
	cur.State = StateIdle
	cur.LastCheckpoint = "aborted"
	cur.RecoveryHint = ""
	cur.UpdatedAt = now
	appendLifecycleHistory(cur, LifecycleTransition{
		From:      from,
		To:        StateIdle,
		OwnerID:   lease.OwnerID,
		OpID:      lease.OpID,
		Kind:      lease.Kind,
		Reason:    reason,
		Timestamp: now,
	})
	return nil
}

// IsActive reports whether paneKey currently has an in-flight lease
// (draining, handing_off, or verifying).
func (c *LifecycleCoordinator) IsActive(paneKey string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.panes[strings.TrimSpace(paneKey)]
	return ok && isInFlightState(cur.State)
}

// Snapshot returns a defensive copy of the pane's lifecycle state.
func (c *LifecycleCoordinator) Snapshot(paneKey string) (PaneLifecycle, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.panes[strings.TrimSpace(paneKey)]
	if !ok {
		return PaneLifecycle{}, false
	}
	cp := *cur
	cp.History = append([]LifecycleTransition(nil), cur.History...)
	return cp, true
}

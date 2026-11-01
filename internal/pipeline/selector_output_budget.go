package pipeline

import (
	"errors"
	"fmt"
	"time"
)

// ErrSelectorOutputBudget means a fresh selector was refused before launch.
// Existing output and uncertain execution receipts remain available for reuse
// or recovery; exhausting this budget never authorizes their eviction.
var ErrSelectorOutputBudget = errors.New("pipeline selector output budget exceeded")

// Only an evaluation still owned by this attempt can release a reservation.
// A persisted starting receipt from an interrupted owner is not a live waiter
// dependency. Bind ownership to the complete launch identity, not just step ID.
type selectorOutputReservation struct {
	version        int
	stepHash       string
	invocationHash string
	startedAt      time.Time
	stdoutLimit    int64
	stderrLimit    int64
}

func selectorOutputReservationFor(record CommandEvaluationState) selectorOutputReservation {
	return selectorOutputReservation{
		version: record.Version, stepHash: record.StepHash, invocationHash: record.InvocationHash,
		startedAt: record.StartedAt, stdoutLimit: record.StdoutLimit, stderrLimit: record.StderrLimit,
	}
}

func (reservation selectorOutputReservation) matches(record CommandEvaluationState) bool {
	return reservation.version == record.Version && reservation.stepHash == record.StepHash && reservation.invocationHash == record.InvocationHash &&
		reservation.startedAt.Equal(record.StartedAt) && reservation.stdoutLimit == record.StdoutLimit &&
		reservation.stderrLimit == record.StderrLimit
}

// selectorOutputAdmissionLocked derives the budget from the journal rather
// than a second byte ledger. Recovery and explicit replay/pruning therefore
// cannot leave stale counters. Callers hold stateMu and insert their starting
// receipt before unlocking if admission succeeds. A non-nil channel asks the
// caller to wait without the lock, then repeat the entire admission check.
func (e *Executor) selectorOutputAdmissionLocked(stepID string, stdoutLimit int64) (<-chan struct{}, error) {
	limit := e.limits.EffectiveLimits().MaxSelectorOutputBytesTotal
	refuse := func() (<-chan struct{}, error) {
		return nil, fmt.Errorf("%w for selector %q: cannot reserve %d stdout bytes within the %d-byte total; increase max_selector_output_bytes_total or lower the per-command stdout cap for new work", ErrSelectorOutputBudget, stepID, stdoutLimit, limit)
	}
	if stdoutLimit <= 0 {
		return nil, fmt.Errorf("selector %q requires a positive stdout reservation", stepID)
	}
	if stdoutLimit > limit {
		return refuse()
	}
	// Subtract from remaining capacity instead of summing arbitrary recorded
	// int64 caps. -1 means insufficient space and cannot wrap back to positive.
	remaining, fixedRemaining := limit-stdoutLimit, limit-stdoutLimit
	subtract := func(capacity, charge int64) int64 {
		if capacity < 0 || charge > capacity {
			return -1
		}
		return capacity - charge
	}
	for id, record := range e.state.CommandEvaluations {
		if record.Version != commandEvaluationVersion || record.StepID != id || record.StdoutLimit <= 0 {
			return nil, fmt.Errorf("cannot account for selector output reservation %q: invalid launch identity or stdout cap", id)
		}
		var charge int64
		active := false
		switch record.Status {
		case commandExecutionSettled, commandEvaluationAccepted:
			charge = int64(len(record.Output))
			if charge > record.StdoutLimit {
				return nil, fmt.Errorf("cannot account for selector output reservation %q: output exceeds its recorded cap", id)
			}
		case commandExecutionStarting, commandExecutionUnresolved:
			if len(record.Output) != 0 {
				return nil, fmt.Errorf("cannot account for selector output reservation %q: unfinished evaluation contains output", id)
			}
			charge = record.StdoutLimit
			if reservation, ok := e.selectorOutputActive[id]; ok && record.Status == commandExecutionStarting {
				active = reservation.matches(record)
			}
		default:
			return nil, fmt.Errorf("cannot account for selector output reservation %q: unknown status %q", id, record.Status)
		}
		remaining = subtract(remaining, charge)
		if !active {
			fixedRemaining = subtract(fixedRemaining, charge)
		}
	}
	if remaining >= 0 {
		return nil, nil
	}
	if fixedRemaining < 0 {
		return refuse()
	}
	// Active captures can still shrink enough to admit this request. Wait
	// under the selector's own context instead of failing healthy siblings.
	if e.selectorOutputWake == nil {
		e.selectorOutputWake = make(chan struct{})
	}
	return e.selectorOutputWake, nil
}

// Called with stateMu held, together with insertion of the durable reservation.
func (e *Executor) startSelectorOutputReservationLocked(record CommandEvaluationState) {
	if e.selectorOutputActive == nil {
		e.selectorOutputActive = make(map[string]selectorOutputReservation)
	}
	e.selectorOutputActive[record.StepID] = selectorOutputReservationFor(record)
}

// Every callback return ends the attempt's ability to shrink this reservation,
// even when invalid evidence or failed persistence leaves its durable fence.
func (e *Executor) finishSelectorOutputReservation(record CommandEvaluationState) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if reservation, ok := e.selectorOutputActive[record.StepID]; ok && reservation.matches(record) {
		delete(e.selectorOutputActive, record.StepID)
		if e.selectorOutputWake != nil {
			close(e.selectorOutputWake)
			e.selectorOutputWake = nil
		}
	}
}

// Run and Resume own separate attempt lifetimes. The journal survives Resume;
// only live callback ownership and wait notifications are reset here.
func (e *Executor) resetSelectorOutputBudgetLocked() {
	e.selectorOutputActive = nil
	if e.selectorOutputWake != nil {
		close(e.selectorOutputWake)
		e.selectorOutputWake = nil
	}
}

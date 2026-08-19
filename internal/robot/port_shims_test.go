package robot

import (
	"context"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
)

// testReservationFunc adapts a function to assignment.ReservationPort. It
// replaces the removed assignment.ReservationFunc.Reserve production method,
// which now exists only as a test helper inside the assignment package.
type testReservationFunc func(context.Context, assignment.ReservationRequest) (assignment.LeaseReceipt, error)

func (f testReservationFunc) Reserve(ctx context.Context, req assignment.ReservationRequest) (assignment.LeaseReceipt, error) {
	return f(ctx, req)
}

// testPacerFunc adapts a function to dispatch.Pacer. It replaces the removed
// dispatch.PacerFunc.Wait production method, which now exists only as a test
// helper inside the dispatch package.
type testPacerFunc func(context.Context, dispatchsvc.Pace) error

func (f testPacerFunc) Wait(ctx context.Context, pace dispatchsvc.Pace) error {
	return f(ctx, pace)
}

// clearAlertTracker resets the shared alert tracker between tests by
// resolving every active alert (the Tracker.Clear method was removed as dead
// production code and the tracker's internals are unexported).
func clearAlertTracker(tr *alerts.Tracker) {
	for _, a := range tr.GetActive() {
		tr.ManualResolve(a.ID)
	}
}

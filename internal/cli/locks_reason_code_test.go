package cli

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// TestClassifyLocksFailure pins the machine-readable reason_code surfaced by
// `ntm locks --json` on failure. The most important case is the lock-list
// timeout (issue #182): the list path bounds the call with a 10s context
// deadline, so a hung daemon arrives as context.DeadlineExceeded, while a
// server-side timeout arrives as agentmail.ErrTimeout — both must classify as
// agentmail_lock_list_timeout, distinct from auth/unavailable/registration.
func TestClassifyLocksFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"context deadline", context.DeadlineExceeded, "agentmail_lock_list_timeout"},
		// Mirror the real wrap: fetchActiveReservations returns "listing reservations: %w".
		{"wrapped context deadline", fmt.Errorf("listing reservations: %w", context.DeadlineExceeded), "agentmail_lock_list_timeout"},
		{"agent mail timeout", fmt.Errorf("listing reservations: %w", agentmail.ErrTimeout), "agentmail_lock_list_timeout"},
		{"server unavailable", fmt.Errorf("listing reservations: %w", agentmail.ErrServerUnavailable), "agentmail_unavailable"},
		{"unauthorized", fmt.Errorf("listing reservations: %w", agentmail.ErrUnauthorized), "agentmail_unauthorized"},
		{"not registered", fmt.Errorf("%w", agentmail.ErrAgentNotRegistered), "agent_not_registered"},
		{"not implemented", fmt.Errorf("%w", agentmail.ErrNotImplemented), "agentmail_unsupported"},
		{"transient busy", fmt.Errorf("%w", agentmail.ErrTransientBusy), "agentmail_transient_busy"},
		{"generic", errors.New("boom"), "agentmail_lock_list_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyLocksFailure(tc.err); got != tc.want {
				t.Fatalf("classifyLocksFailure(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

package robot

import (
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Test-only helper moved out of tmux_adapter.go for the G1 dead-code gate.
//
// #288 made per-session state counts tail-aware, and every production caller
// moved to normalizeSessionWithTails with real captured tails. This no-tails
// wrapper kept only its test callers, so it lives here rather than as
// unreachable code in the production build.

// NormalizeSession transforms a tmux.Session into a RuntimeSession.
func (a *TmuxAdapter) NormalizeSession(sess *tmux.Session, agents []Agent) *state.RuntimeSession {
	return a.normalizeSessionWithTails(sess, agents, nil)
}

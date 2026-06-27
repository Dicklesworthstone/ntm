package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// pane_work_token.go implements the DISPATCH-TIME stamping half of the opt-in
// semantic-progress signal (#199).
//
// When (and only when) `[robot.semantic].stamp = true`, `ntm send` / `ntm
// assign` inject a per-pane `NTM-Pane: <session>/<window>.<pane>` commit-trailer
// INSTRUCTION into the dispatched marching orders. This is deliberately a
// marching-orders instruction, NOT a git hook or a git-config mutation: hooks /
// config are fragile, global, and collide with shared repos, whereas an
// instruction is per-pane by construction and degrades safely (an agent that
// ignores it simply yields source:"none" at read time — never a false wedge).
//
// Default behavior is preserved exactly: when stamping is off,
// stampMarchingOrders returns the prompt UNCHANGED, so dispatch is
// byte-identical to the pre-feature behavior.

// semanticStampEnabled reports whether dispatch-time pane work-token stamping is
// opted in via config. Default false.
func semanticStampEnabled() bool {
	return cfg != nil && cfg.Robot.Semantic.Stamp
}

// stampMarchingOrders appends the per-pane NTM-Pane commit-trailer instruction
// to a dispatched prompt when stamping is enabled. It is idempotent (it never
// double-stamps a prompt that already carries the token) and a no-op — returning
// the prompt verbatim — when the feature is off.
func stampMarchingOrders(prompt, session string, window, pane int) string {
	if !semanticStampEnabled() {
		return prompt
	}
	token := robot.PaneWorkToken(session, window, pane)
	if strings.Contains(prompt, token) {
		return prompt
	}
	instruction := fmt.Sprintf(
		"\n\n---\n[ntm progress token] For any git commits you make while working on this, "+
			"append this exact trailer line to the commit message (after a blank line), and do not alter or remove it:\n"+
			"%s\n"+
			"It lets the orchestrator confirm your pane is making real forward progress; it does not change what you commit.",
		token)
	return prompt + instruction
}

// bestEffortStampBeadLabel attaches the pane's bead label to a bead at dispatch
// (the SECONDARY "and/or" attribution key per the design). It is:
//   - gated behind the same opt-in (no-op when stamping is off),
//   - best-effort and NON-FATAL: any error is logged at debug and swallowed so
//     it can never block or fail a dispatch,
//   - bounded by a short context timeout so a slow `br` never delays delivery.
//
// Callers should invoke it AFTER the prompt has been delivered so bead I/O is
// never on the critical path of getting work to the agent. It is only wired in
// where a bead id is cleanly known (assign paths); the plain `ntm send` path has
// no associated bead and therefore only gets the commit-trailer instruction.
func bestEffortStampBeadLabel(beadID, session string, window, pane int) {
	if !semanticStampEnabled() {
		return
	}
	beadID = strings.TrimSpace(beadID)
	if beadID == "" {
		return
	}
	label := robot.PaneBeadLabel(session, window, pane)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "br", "update", beadID, "--add-label", label)
	if out, err := cmd.CombinedOutput(); err != nil {
		slog.Debug("semantic: best-effort bead label stamp failed (non-fatal)",
			"bead", beadID, "label", label, "error", err, "output", strings.TrimSpace(string(out)))
	}
}

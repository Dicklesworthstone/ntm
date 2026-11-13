// Package tmux provides a wrapper around tmux commands.
// pane_border.go implements per-pane border style control for activity indicators.
package tmux

import (
	"context"
	"strings"
)

// PaneBorderStatusPosition is where NTM places pane titles when it enables
// pane-border-status on a session it spawns.
const PaneBorderStatusPosition = "top"

// EnsurePaneBorderStatus enables tmux's pane-border-status for the given
// session so pane titles are actually visible on stock tmux (whose default is
// "off", which hides every title NTM sets). See EnsurePaneBorderStatusContext.
func (c *Client) EnsurePaneBorderStatus(session string) error {
	return c.EnsurePaneBorderStatusContext(context.Background(), session)
}

// EnsurePaneBorderStatusContext enables pane-border-status for the session's
// window with cancellation support.
//
// Design (bd-ws7-docs-ux-truth-tqh3l.8):
//   - SESSION-LOCAL only: the option is set as a window option on the target
//     session (no -g), so a user's global tmux configuration is never mutated
//     and other sessions are unaffected.
//   - Respects existing user config: if the effective value (local or
//     inherited from the user's tmux.conf) already makes titles visible
//     ("top" or "bottom"), nothing is changed.
func (c *Client) EnsurePaneBorderStatusContext(ctx context.Context, session string) error {
	// -A includes values inherited from the global/window defaults, so a user
	// who globally chose "bottom" (or already enabled "top") is left alone.
	if out, err := c.RunContext(ctx, "show-options", "-A", "-w", "-v", "-t", SessionOptionTarget(session), "pane-border-status"); err == nil {
		switch strings.TrimSpace(out) {
		case "top", "bottom":
			return nil
		}
	}
	return c.RunSilentContext(ctx, "set-option", "-w", "-t", SessionOptionTarget(session),
		"pane-border-status", PaneBorderStatusPosition)
}

// EnsurePaneBorderStatus enables pane-border-status for a session (default client).
func EnsurePaneBorderStatus(session string) error {
	return DefaultClient.EnsurePaneBorderStatus(session)
}

// EnsurePaneBorderStatusContext enables pane-border-status with context support
// (default client).
func EnsurePaneBorderStatusContext(ctx context.Context, session string) error {
	return DefaultClient.EnsurePaneBorderStatusContext(ctx, session)
}

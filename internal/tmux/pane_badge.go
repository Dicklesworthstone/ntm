// Package tmux — pane_badge.go
//
// Per-pane Agent Mail identity badges (ntm#312). NTM caches each managed
// pane's assigned Agent Mail name and its last reconciliation outcome in tmux
// user options on the pane, and composes a fragment into the window's
// pane-border-format that renders the cached label next to the pane title.
// Rendering reads only these cached options: nothing in tmux's periodic
// format evaluation touches the registry, identity files or the Agent Mail
// server. Reconciliation (deciding what the options should say) lives in
// package agentmail; this file owns the tmux side only: option names, value
// sanitisation, publication, and the border-format composition/restore.
package tmux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Pane user options carrying the cached badge state. Each is written at
// identity-assignment time, before the agent process launches, and refreshed
// on the lifecycle paths that reconcile the session registry.
const (
	// PaneOptionAgentMailName is the name NTM assigned to the pane (display
	// authority). Empty when the pane has no current assignment.
	PaneOptionAgentMailName = "@ntm_agent_mail_name"
	// PaneOptionAgentMailState is the compact reconciliation outcome
	// (e.g. matched, legacy-unverified, name-disagreement, missing-file,
	// assignment-stale). See agentmail.PaneObservationState.
	PaneOptionAgentMailState = "@ntm_agent_mail_state"
	// PaneOptionAgentMailLifecycle is the agent lifecycle observed at the
	// last reconciliation: starting, running, exited or unknown.
	PaneOptionAgentMailLifecycle = "@ntm_agent_mail_lifecycle"
	// PaneOptionAgentMailLabel is the sanitised display label the border
	// format renders, e.g. "[BlueLake]" or "[BlueLake!] (exited)".
	PaneOptionAgentMailLabel = "@ntm_agent_mail_label"

	// SessionOptionAgentMailBadges is a per-session opt-out: set it to "off"
	// on a session (`tmux set-option -t =name: @ntm_agent_mail_badges off`)
	// and NTM leaves that session's borders and pane options alone even when
	// badges are enabled in its config.
	SessionOptionAgentMailBadges = "@ntm_agent_mail_badges"

	// Window user options recording what NTM changed on a window so disable
	// can restore it (including inheritance) without touching unrelated
	// edits the user made while badges were on.
	windowOptionBadgeBorderSet        = "@ntm_agent_mail_border_set"
	windowOptionBadgeBorderPrev       = "@ntm_agent_mail_border_prev"
	windowOptionBadgeBorderPrevScope  = "@ntm_agent_mail_border_prev_scope"
	windowOptionBadgeBorderStatusPrev = "@ntm_agent_mail_border_status_prev"

	borderScopeLocal     = "local"
	borderScopeInherited = "inherited"
)

// PaneBorderBadgeFragment is the format fragment NTM appends to a window's
// pane-border-format. It renders a space plus the cached label after the
// existing content and collapses to nothing at all on panes without a label
// (user panes, service panes, panes NTM never assigned).
const PaneBorderBadgeFragment = `#{?` + PaneOptionAgentMailLabel + `, #{` + PaneOptionAgentMailLabel + `},}`

// badgeOptionPrefix is shared by every option this feature owns; a user
// border format that already references it renders badges its own way.
const badgeOptionPrefix = "@ntm_agent_mail_"

// defaultPaneBorderFormat is tmux's own default; used only when the
// effective value cannot be read.
const defaultPaneBorderFormat = `#{?pane_active,#[reverse],}#{pane_index}#[default] "#{pane_title}"`

// MaxBadgeTextLen bounds any value written into a badge pane option.
const MaxBadgeTextLen = 96

// PaneBadge is the cached badge state written to one pane.
type PaneBadge struct {
	Name      string
	State     string
	Lifecycle string
	Label     string
}

// SanitizeBadgeText makes a string safe to store in a badge pane option and
// render through a format: tmux format syntax (`#{…}`, `#[…]`, `#(…)`), the
// command separator, quotes and every control character are dropped, and
// the result is bounded to MaxBadgeTextLen bytes. Identity data never reaches
// a border unfiltered; raw identity-file contents belong in status output.
func SanitizeBadgeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r == '#' || r == '{' || r == '}' || r == ';' || r == '"' || r == '\'' || r == '`' || r == '\\' || r == '$':
			continue
		case r < 0x20 || r == 0x7f:
			continue
		case r > 0x7e:
			// Keep the badge ASCII-only: identity names are, and a stray
			// multi-byte glyph in a border is more likely injection than
			// intent.
			continue
		}
		if b.Len() >= MaxBadgeTextLen {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// PublishPaneBadgeContext writes the four badge options on paneID in one
// tmux invocation and returns the pane's #{pane_pid} as reported by the same
// invocation, so the caller can detect a pane whose generation changed
// (respawn-pane keeps %N but replaces the pid) between observing it and
// publishing. Values are sanitised here regardless of what the caller did.
// The identity data is passed as argv, never through a shell.
func (c *Client) PublishPaneBadgeContext(ctx context.Context, paneID string, badge PaneBadge) (int, error) {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return 0, fmt.Errorf("pane id is required")
	}
	target := ExactTarget(paneID)
	args := []string{
		"set-option", "-p", "-t", target, PaneOptionAgentMailName, SanitizeBadgeText(badge.Name), ";",
		"set-option", "-p", "-t", target, PaneOptionAgentMailState, SanitizeBadgeText(badge.State), ";",
		"set-option", "-p", "-t", target, PaneOptionAgentMailLifecycle, SanitizeBadgeText(badge.Lifecycle), ";",
		"set-option", "-p", "-t", target, PaneOptionAgentMailLabel, SanitizeBadgeText(badge.Label), ";",
		"display-message", "-p", "-t", target, "#{pane_pid}",
	}
	out, err := c.RunContext(ctx, args...)
	if err != nil {
		return 0, err
	}
	pid, convErr := strconv.Atoi(strings.TrimSpace(out))
	if convErr != nil {
		return 0, nil
	}
	return pid, nil
}

// ClearPaneBadgeContext removes every badge option from paneID. Unsetting an
// option that was never set is a tmux no-op, so this is safe to call on any
// pane.
func (c *Client) ClearPaneBadgeContext(ctx context.Context, paneID string) error {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return fmt.Errorf("pane id is required")
	}
	target := ExactTarget(paneID)
	return c.RunSilentContext(ctx,
		"set-option", "-p", "-u", "-t", target, PaneOptionAgentMailName, ";",
		"set-option", "-p", "-u", "-t", target, PaneOptionAgentMailState, ";",
		"set-option", "-p", "-u", "-t", target, PaneOptionAgentMailLifecycle, ";",
		"set-option", "-p", "-u", "-t", target, PaneOptionAgentMailLabel,
	)
}

// ReadPaneBadgeContext returns the badge options currently cached on paneID
// (each empty when unset).
func (c *Client) ReadPaneBadgeContext(ctx context.Context, paneID string) (PaneBadge, error) {
	sep := "\x1f"
	format := "#{" + PaneOptionAgentMailName + "}" + sep +
		"#{" + PaneOptionAgentMailState + "}" + sep +
		"#{" + PaneOptionAgentMailLifecycle + "}" + sep +
		"#{" + PaneOptionAgentMailLabel + "}"
	out, err := c.RunContext(ctx, "display-message", "-p", "-t", ExactTarget(paneID), format)
	if err != nil {
		return PaneBadge{}, err
	}
	parts := strings.SplitN(out, sep, 4)
	for len(parts) < 4 {
		parts = append(parts, "")
	}
	return PaneBadge{Name: parts[0], State: parts[1], Lifecycle: parts[2], Label: parts[3]}, nil
}

// WindowBadgeInfo is what badge publication needs to know about a window.
type WindowBadgeInfo struct {
	// ID is the stable window id (@N).
	ID string
	// Index is the window's index within the session; panes report the same
	// number as Pane.WindowIndex.
	Index int
	// Linked is tmux's #{window_linked}: the window is linked into more than
	// one session, so its window options are shared. The first release
	// skips such windows with a diagnostic rather than pick a conflict
	// policy.
	Linked bool
	// SessionOptOut is true when the session carries
	// SessionOptionAgentMailBadges=off.
	SessionOptOut bool
	// SocketPath is the tmux server's socket, for comparison against the
	// socket recorded in a structured identity record.
	SocketPath string
}

// ListWindowBadgeInfoContext lists the session's windows with the facts
// badge publication needs.
func (c *Client) ListWindowBadgeInfoContext(ctx context.Context, session string) ([]WindowBadgeInfo, error) {
	sep := "\x1f"
	format := strings.Join([]string{
		"#{window_id}", "#{window_index}", "#{window_linked}", "#{socket_path}",
		"#{" + SessionOptionAgentMailBadges + "}",
	}, sep)
	out, err := c.RunContext(ctx, "list-windows", "-t", TargetSession(session), "-F", format)
	if err != nil {
		return nil, err
	}
	var windows []WindowBadgeInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, sep, 5)
		if len(parts) < 5 {
			return nil, fmt.Errorf("unexpected list-windows output: %q", line)
		}
		index, convErr := strconv.Atoi(strings.TrimSpace(parts[1]))
		if convErr != nil {
			return nil, fmt.Errorf("unexpected window index in list-windows output: %q", line)
		}
		windows = append(windows, WindowBadgeInfo{
			ID:            strings.TrimSpace(parts[0]),
			Index:         index,
			Linked:        strings.TrimSpace(parts[2]) == "1",
			SessionOptOut: strings.EqualFold(strings.TrimSpace(parts[4]), "off"),
			SocketPath:    strings.TrimSpace(parts[3]),
		})
	}
	return windows, nil
}

// BorderFormatReferencesBadge reports whether a pane-border-format already
// renders badge options — either NTM's own fragment or a user format that
// references the options directly, which NTM treats as the user owning the
// rendering.
func BorderFormatReferencesBadge(format string) bool {
	return strings.Contains(format, badgeOptionPrefix)
}

// ComposeBadgeBorderFormat appends the badge fragment to an existing
// pane-border-format. An empty existing format falls back to tmux's default
// so the pane index and title are not lost. A format that already renders
// badge options is returned unchanged.
func ComposeBadgeBorderFormat(existing string) string {
	if strings.TrimSpace(existing) == "" {
		existing = defaultPaneBorderFormat
	}
	if BorderFormatReferencesBadge(existing) {
		return existing
	}
	return existing + PaneBorderBadgeFragment
}

// StripBadgeBorderFormat removes NTM's fragment from a pane-border-format.
// The second result reports whether anything was removed.
func StripBadgeBorderFormat(format string) (string, bool) {
	if !strings.Contains(format, PaneBorderBadgeFragment) {
		return format, false
	}
	return strings.Replace(format, PaneBorderBadgeFragment, "", 1), true
}

// BorderChange describes what enabling or disabling the badge border did to a
// window.
type BorderChange struct {
	// Changed is true when a tmux option was modified.
	Changed bool
	// Skipped carries a diagnostic when nothing was done for a reason the
	// caller should surface (user-owned format, not NTM-owned).
	Skipped string
}

// windowOption reads one window option's value. With local=false the value
// includes inheritance (-A). Unset user options read as "" without error.
func (c *Client) windowOption(ctx context.Context, windowID, name string, local bool) (string, error) {
	args := []string{"show-options", "-w", "-q", "-v"}
	if !local {
		args = append(args, "-A")
	}
	args = append(args, "-t", ExactTarget(windowID), name)
	out, err := c.RunContext(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// EnableWindowBadgeBorderContext composes the badge fragment into the
// window's effective pane-border-format and makes the border visible
// (pane-border-status), recording the previous values (and whether they
// were local or inherited) on the window so DisableWindowBadgeBorderContext
// can restore them. Idempotent: a window NTM already prepared is left alone,
// and a format the user wrote to reference the badge options themselves is
// never rewritten.
func (c *Client) EnableWindowBadgeBorderContext(ctx context.Context, windowID string) (BorderChange, error) {
	windowID = strings.TrimSpace(windowID)
	if windowID == "" {
		return BorderChange{}, fmt.Errorf("window id is required")
	}
	alreadySet, err := c.windowOption(ctx, windowID, windowOptionBadgeBorderSet, true)
	if err != nil {
		return BorderChange{}, err
	}
	localFormat, err := c.windowOption(ctx, windowID, "pane-border-format", true)
	if err != nil {
		return BorderChange{}, err
	}
	if alreadySet != "" && localFormat == alreadySet {
		return BorderChange{}, nil
	}
	effective, err := c.windowOption(ctx, windowID, "pane-border-format", false)
	if err != nil || effective == "" {
		effective = defaultPaneBorderFormat
	}
	if BorderFormatReferencesBadge(effective) {
		if alreadySet != "" {
			// Our fragment is present but the local value drifted from what we
			// set (user edited the format while badges were on). The user's
			// edit still renders badges; keep it. Disable handles this case
			// by stripping only our fragment from the current value, so the
			// recorded previous value needs no refresh.
			return BorderChange{}, nil
		}
		return BorderChange{Skipped: "pane-border-format already references " + badgeOptionPrefix + "* options; left as-is"}, nil
	}

	composed := ComposeBadgeBorderFormat(effective)
	prevScope := borderScopeInherited
	if localFormat != "" {
		prevScope = borderScopeLocal
	}
	args := []string{
		"set-option", "-w", "-t", ExactTarget(windowID), "pane-border-format", composed, ";",
		"set-option", "-w", "-t", ExactTarget(windowID), windowOptionBadgeBorderSet, composed, ";",
		"set-option", "-w", "-t", ExactTarget(windowID), windowOptionBadgeBorderPrev, localFormat, ";",
		"set-option", "-w", "-t", ExactTarget(windowID), windowOptionBadgeBorderPrevScope, prevScope,
	}

	// Titles (and therefore badges) are invisible while pane-border-status is
	// off. Mirror EnsurePaneBorderStatus for this window, remembering what we
	// found so disable can put it back.
	status, statusErr := c.windowOption(ctx, windowID, "pane-border-status", false)
	if statusErr == nil {
		switch status {
		case "top", "bottom":
		default:
			localStatus, _ := c.windowOption(ctx, windowID, "pane-border-status", true)
			prev := borderScopeInherited
			if localStatus != "" {
				prev = borderScopeLocal + ":" + localStatus
			}
			args = append(args, ";",
				"set-option", "-w", "-t", ExactTarget(windowID), "pane-border-status", PaneBorderStatusPosition, ";",
				"set-option", "-w", "-t", ExactTarget(windowID), windowOptionBadgeBorderStatusPrev, prev)
		}
	}
	if err := c.RunSilentContext(ctx, args...); err != nil {
		return BorderChange{}, err
	}
	return BorderChange{Changed: true}, nil
}

// DisableWindowBadgeBorderContext restores the pane-border-format NTM
// replaced on the window: the previous local value, or inheritance when the
// window had none. A format the user edited while badges were on is kept,
// minus NTM's fragment. Windows NTM never prepared are left untouched.
func (c *Client) DisableWindowBadgeBorderContext(ctx context.Context, windowID string) (BorderChange, error) {
	windowID = strings.TrimSpace(windowID)
	if windowID == "" {
		return BorderChange{}, fmt.Errorf("window id is required")
	}
	set, err := c.windowOption(ctx, windowID, windowOptionBadgeBorderSet, true)
	if err != nil {
		return BorderChange{}, err
	}
	if set == "" {
		return BorderChange{Skipped: "window " + windowID + " carries no NTM-owned border format"}, nil
	}
	current, err := c.windowOption(ctx, windowID, "pane-border-format", true)
	if err != nil {
		return BorderChange{}, err
	}
	prev, _ := c.windowOption(ctx, windowID, windowOptionBadgeBorderPrev, true)
	prevScope, _ := c.windowOption(ctx, windowID, windowOptionBadgeBorderPrevScope, true)
	statusPrev, _ := c.windowOption(ctx, windowID, windowOptionBadgeBorderStatusPrev, true)

	target := ExactTarget(windowID)
	var args []string
	switch {
	case current == set && prevScope == borderScopeLocal && prev != "":
		// A local scope is only ever recorded with a non-empty value; an
		// empty record means the marker was lost, and restoring "" would
		// blank every pane title in the window. Fall through to -u instead.
		args = append(args, "set-option", "-w", "-t", target, "pane-border-format", prev)
	case current == set:
		args = append(args, "set-option", "-w", "-u", "-t", target, "pane-border-format")
	default:
		// User edited the format while badges were on: keep their edit,
		// drop only our fragment.
		if stripped, removed := StripBadgeBorderFormat(current); removed {
			args = append(args, "set-option", "-w", "-t", target, "pane-border-format", stripped)
		}
	}
	if statusPrev != "" {
		if len(args) > 0 {
			args = append(args, ";")
		}
		if value, ok := strings.CutPrefix(statusPrev, borderScopeLocal+":"); ok && value != "" {
			args = append(args, "set-option", "-w", "-t", target, "pane-border-status", value)
		} else {
			args = append(args, "set-option", "-w", "-u", "-t", target, "pane-border-status")
		}
	}
	for _, opt := range []string{windowOptionBadgeBorderSet, windowOptionBadgeBorderPrev, windowOptionBadgeBorderPrevScope, windowOptionBadgeBorderStatusPrev} {
		if len(args) > 0 {
			args = append(args, ";")
		}
		args = append(args, "set-option", "-w", "-u", "-t", target, opt)
	}
	if err := c.RunSilentContext(ctx, args...); err != nil {
		return BorderChange{}, err
	}
	return BorderChange{Changed: true}, nil
}

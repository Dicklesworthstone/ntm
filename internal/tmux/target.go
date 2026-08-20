// Package tmux — target.go
//
// Canonical construction of tmux `-t` target arguments.
//
// tmux resolves a bare session name in a `-t` target by PREFIX MATCH when no
// exact match exists (and tmux <3.4 ordering quirks make this worse): with
// only `midas_edge_api` running, `-t midas_edge` silently resolves to
// `midas_edge_api`. Field evidence (ts2, 2026-08-01): spawning `midas_edge`
// while `midas_edge_api` existed landed the new panes INSIDE midas_edge_api
// with cross-wired titles.
//
// The fix is tmux's documented exact-match sigil: a target session prefixed
// with `=` only ever resolves to a session with exactly that name. The sigil
// applies to the session portion of compound targets too (`=sess:win.pane`),
// verified against tmux 3.6a for has-session, kill-session, split-window,
// send-keys, capture-pane, list-panes, list-windows, select-layout,
// select-pane, display-message, respawn-pane, pipe-pane, and paste-buffer.
//
// The ONE exception (verified empirically): session/window-scoped option
// commands (`set-option`/`show-options` without `-p`) reject `=name` with
// "no such session: =name" but accept `=name:`. Use SessionOptionTarget for
// those call sites.
//
// G6 single-definition convention: every `-t` argument that can carry a
// session name MUST be routed through one of these helpers. Do not hand-roll
// `"=" + name` elsewhere.
package tmux

// ExactTarget pins the session portion of a tmux target to exact-match by
// prefixing tmux's `=` sigil. Targets that cannot be (or are already)
// prefix-ambiguous are returned unchanged:
//
//   - ""            (empty: caller bug, tmux will error either way)
//   - "=..."        (already exact)
//   - "%N" pane IDs, "$N" session IDs, "@N" window IDs (always exact)
//   - ":..." / "..." starting with '.' (current-session relative forms)
//
// Safe for both bare session names ("foo") and compound targets
// ("foo:1.2", "foo:.3").
func ExactTarget(target string) string {
	if target == "" {
		return target
	}
	switch target[0] {
	case '=', '%', '$', '@', ':', '.':
		return target
	}
	return "=" + target
}

// TargetSession returns the exact-match tmux target for a bare session name.
// This is the canonical helper for `-t` arguments that are session names.
func TargetSession(name string) string {
	return ExactTarget(name)
}

// SessionOptionTarget returns the exact-match target for session/window
// scoped option commands (`set-option`, `show-options` without `-p`), which
// reject the bare `=name` form but accept `=name:` (verified on tmux 3.6a).
func SessionOptionTarget(name string) string {
	if name == "" {
		return name
	}
	switch name[0] {
	case '%', '$', '@', ':', '.':
		return name
	case '=':
		return name + ":"
	}
	return "=" + name + ":"
}

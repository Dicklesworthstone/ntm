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
// Version floor: the `=` exact-match sigil has existed since tmux 2.1
// (Oct 2015, the cmd-find rewrite). NTM elsewhere already assumes tmux 3.0+
// features (per-pane options like allow-set-title), so no runtime version
// guard is needed here.
//
// TWO exceptions (verified empirically):
//
//   - session/window-scoped option commands (`set-option`/`show-options`
//     without `-p`) reject `=name` with "no such session: =name" but accept
//     `=name:`. Use SessionOptionTarget for those call sites.
//   - `display-message -p -t =name FORMAT` exits 0 with EMPTY output on tmux
//     3.4 and 3.6a (even for a session that does not exist), so pane-format
//     queries keyed on a bare exact session silently read as "" (ntm#310:
//     checkpoints recorded an empty working_dir and skipped Git capture).
//     Use SessionPaneTarget for those call sites; `=name:` resolves to the
//     session's current window and its active pane.
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

// SessionPaneTarget returns the exact-match target that resolves a bare
// session name to its current window's active pane (`=name:`). Use it for
// pane-scoped format queries such as `display-message -p -t … '#{…}'`, which
// answer the bare `=name` form with exit 0 and empty output (tmux 3.4, 3.6a).
// Pane IDs, session/window IDs and current-session relative forms are
// returned unchanged; an already exact `=name` gains the trailing colon.
func SessionPaneTarget(name string) string {
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

package tmux

// Tests for the tmux side of pane identity badges (ntm#312): option
// sanitisation, border-format composition/restore, and publication against
// a real tmux server on the package's isolated socket.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSanitizeBadgeText(t *testing.T) {
	cases := map[string]string{
		"BlueLake":                    "BlueLake",
		"  [BlueLake!] (exited)  ":    "[BlueLake!] (exited)",
		"#[fg=red]X":                  "[fg=red]X", // no '#': literal text, not a style
		"#{pane_id}":                  "pane_id",
		"a;kill-server":               "akill-server",
		"q\"uo'te`s$":                 "quotes",
		"tab\tnew\nline\x1b[31m":      "tabnewline[31m",
		"ünïcödé":                     "ncd",
		strings.Repeat("x", 200):      strings.Repeat("x", MaxBadgeTextLen),
		"\\backslash{brace}":          "backslashbrace",
		"name-with_underscore.dot:ok": "name-with_underscore.dot:ok",
	}
	for in, want := range cases {
		if got := SanitizeBadgeText(in); got != want {
			t.Errorf("SanitizeBadgeText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestComposeAndStripBadgeBorderFormat(t *testing.T) {
	user := `#{pane_index}: #{pane_title}`
	composed := ComposeBadgeBorderFormat(user)
	if !strings.HasPrefix(composed, user) || !strings.HasSuffix(composed, PaneBorderBadgeFragment) {
		t.Fatalf("composed = %q", composed)
	}
	if again := ComposeBadgeBorderFormat(composed); again != composed {
		t.Fatalf("compose must be idempotent: %q", again)
	}
	stripped, removed := StripBadgeBorderFormat(composed)
	if !removed || stripped != user {
		t.Fatalf("strip = %q (%v)", stripped, removed)
	}
	if _, removed := StripBadgeBorderFormat(user); removed {
		t.Fatal("strip reported a removal on a format without the fragment")
	}
	if got := ComposeBadgeBorderFormat("   "); !strings.HasPrefix(got, defaultPaneBorderFormat) {
		t.Fatalf("empty format must fall back to tmux's default, got %q", got)
	}
	custom := `#{pane_title} #{@ntm_agent_mail_name}`
	if got := ComposeBadgeBorderFormat(custom); got != custom {
		t.Fatalf("user format referencing badge options must be left alone, got %q", got)
	}
	if !BorderFormatReferencesBadge(custom) || BorderFormatReferencesBadge(user) {
		t.Fatal("BorderFormatReferencesBadge misclassified")
	}
}

// createBadgeTestSession creates a session on the package's isolated tmux
// server (TestMain sets TMUX_TMPDIR), serialised with the other real-tmux
// tests; cleanup kills only that session.
func createBadgeTestSession(t *testing.T) string {
	t.Helper()
	return createTestSession(t)
}

func createSecondBadgeTestSession(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("ntm_test_badgelink_%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = KillSession(name) })
	if err := CreateSession(name, t.TempDir()); err != nil {
		t.Fatalf("CreateSession(%s): %v", name, err)
	}
	return name
}

func windowOptionValue(t *testing.T, windowID, option string, local bool) string {
	t.Helper()
	value, err := DefaultClient.windowOption(context.Background(), windowID, option, local)
	if err != nil {
		t.Fatalf("show-options %s: %v", option, err)
	}
	return value
}

// TestRealPaneBadgePublishReadClear drives the pane options against a real
// server: publish sanitises and reports the pane pid, read returns what was
// written, clear removes every option.
func TestRealPaneBadgePublishReadClear(t *testing.T) {
	session := createBadgeTestSession(t)
	ctx := context.Background()
	panes, err := GetPanes(session)
	if err != nil || len(panes) == 0 {
		t.Fatalf("GetPanes: %v", err)
	}
	pane := panes[0]

	pid, err := DefaultClient.PublishPaneBadgeContext(ctx, pane.ID, PaneBadge{
		Name: "BlueLake", State: "matched", Lifecycle: "running", Label: "[BlueLake] #{pane_id}",
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if pid != pane.PID {
		t.Fatalf("publish reported pid %d, listing says %d", pid, pane.PID)
	}
	got, err := DefaultClient.ReadPaneBadgeContext(ctx, pane.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := PaneBadge{Name: "BlueLake", State: "matched", Lifecycle: "running", Label: "[BlueLake] pane_id"}
	if got != want {
		t.Fatalf("read = %+v, want %+v", got, want)
	}
	// The value is rendered verbatim by a format: no expansion of what was
	// stored.
	rendered, err := DefaultClient.RunContext(ctx, "display-message", "-p", "-t", pane.ID, "#{?"+PaneOptionAgentMailLabel+",#{"+PaneOptionAgentMailLabel+"},none}")
	if err != nil || rendered != "[BlueLake] pane_id" {
		t.Fatalf("rendered = %q (%v)", rendered, err)
	}

	if err := DefaultClient.ClearPaneBadgeContext(ctx, pane.ID); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got, err := DefaultClient.ReadPaneBadgeContext(ctx, pane.ID); err != nil || got != (PaneBadge{}) {
		t.Fatalf("after clear = %+v (%v)", got, err)
	}
	if _, err := DefaultClient.PublishPaneBadgeContext(ctx, "%999999", PaneBadge{Label: "x"}); err == nil {
		t.Fatal("publishing to a missing pane must fail")
	}
}

// TestRealWindowBadgeBorderEnableDisable: enabling composes the fragment
// into the effective format and makes titles visible; disabling restores
// inheritance (no local option left behind) and the border status.
func TestRealWindowBadgeBorderEnableDisable(t *testing.T) {
	session := createBadgeTestSession(t)
	ctx := context.Background()
	windows, err := DefaultClient.ListWindowBadgeInfoContext(ctx, session)
	if err != nil || len(windows) != 1 {
		t.Fatalf("ListWindowBadgeInfo = %+v, %v", windows, err)
	}
	w := windows[0]
	if w.ID == "" || w.Linked || w.SessionOptOut || w.SocketPath == "" {
		t.Fatalf("window info = %+v", w)
	}
	if local := windowOptionValue(t, w.ID, "pane-border-format", true); local != "" {
		t.Skipf("test server already carries a local pane-border-format %q", local)
	}
	inheritedFormat := windowOptionValue(t, w.ID, "pane-border-format", false)
	inheritedStatus := windowOptionValue(t, w.ID, "pane-border-status", false)

	change, err := DefaultClient.EnableWindowBadgeBorderContext(ctx, w.ID)
	if err != nil || !change.Changed {
		t.Fatalf("enable = %+v, %v", change, err)
	}
	local := windowOptionValue(t, w.ID, "pane-border-format", true)
	if local != ComposeBadgeBorderFormat(inheritedFormat) {
		t.Fatalf("local format = %q, want %q", local, ComposeBadgeBorderFormat(inheritedFormat))
	}
	switch windowOptionValue(t, w.ID, "pane-border-status", false) {
	case "top", "bottom":
	default:
		t.Fatal("border status not made visible")
	}
	// Idempotent.
	if change, err := DefaultClient.EnableWindowBadgeBorderContext(ctx, w.ID); err != nil || change.Changed {
		t.Fatalf("second enable = %+v, %v", change, err)
	}

	// The badge renders through the composed format.
	panes, _ := GetPanes(session)
	if _, err := DefaultClient.PublishPaneBadgeContext(ctx, panes[0].ID, PaneBadge{Label: "[BlueLake!]"}); err != nil {
		t.Fatal(err)
	}
	rendered, err := DefaultClient.RunContext(ctx, "display-message", "-p", "-t", panes[0].ID, local)
	if err != nil || !strings.HasSuffix(rendered, " [BlueLake!]") {
		t.Fatalf("rendered border = %q (%v)", rendered, err)
	}

	change, err = DefaultClient.DisableWindowBadgeBorderContext(ctx, w.ID)
	if err != nil || !change.Changed {
		t.Fatalf("disable = %+v, %v", change, err)
	}
	if local := windowOptionValue(t, w.ID, "pane-border-format", true); local != "" {
		t.Fatalf("disable left a local pane-border-format %q; inheritance not restored", local)
	}
	if got := windowOptionValue(t, w.ID, "pane-border-status", false); got != inheritedStatus {
		t.Fatalf("pane-border-status = %q after disable, want %q", got, inheritedStatus)
	}
	for _, opt := range []string{windowOptionBadgeBorderSet, windowOptionBadgeBorderPrev, windowOptionBadgeBorderPrevScope, windowOptionBadgeBorderStatusPrev} {
		if v := windowOptionValue(t, w.ID, opt, true); v != "" {
			t.Errorf("marker %s still set to %q", opt, v)
		}
	}
	if change, err := DefaultClient.DisableWindowBadgeBorderContext(ctx, w.ID); err != nil || change.Changed || change.Skipped == "" {
		t.Fatalf("disable on an unowned window = %+v, %v", change, err)
	}
}

// TestRealWindowBadgeBorderPreservesUserEdits: a local user format is
// restored verbatim on disable, and an edit made while badges were on is
// kept minus NTM's fragment.
func TestRealWindowBadgeBorderPreservesUserEdits(t *testing.T) {
	session := createBadgeTestSession(t)
	ctx := context.Background()
	windows, err := DefaultClient.ListWindowBadgeInfoContext(ctx, session)
	if err != nil || len(windows) != 1 {
		t.Fatalf("ListWindowBadgeInfo: %v", err)
	}
	w := windows[0]
	userFormat := `#{pane_index}: #{pane_title}`
	if err := DefaultClient.RunSilentContext(ctx, "set-option", "-w", "-t", w.ID, "pane-border-format", userFormat); err != nil {
		t.Fatal(err)
	}
	if _, err := DefaultClient.EnableWindowBadgeBorderContext(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	if got := windowOptionValue(t, w.ID, "pane-border-format", true); got != userFormat+PaneBorderBadgeFragment {
		t.Fatalf("composed = %q", got)
	}
	if _, err := DefaultClient.DisableWindowBadgeBorderContext(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	if got := windowOptionValue(t, w.ID, "pane-border-format", true); got != userFormat {
		t.Fatalf("restored = %q, want the user's local format %q", got, userFormat)
	}

	// Edit while enabled.
	if _, err := DefaultClient.EnableWindowBadgeBorderContext(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	edited := `EDITED ` + userFormat + PaneBorderBadgeFragment
	if err := DefaultClient.RunSilentContext(ctx, "set-option", "-w", "-t", w.ID, "pane-border-format", edited); err != nil {
		t.Fatal(err)
	}
	if _, err := DefaultClient.DisableWindowBadgeBorderContext(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	if got := windowOptionValue(t, w.ID, "pane-border-format", true); got != `EDITED `+userFormat {
		t.Fatalf("after disable with user edit = %q", got)
	}

	// A user format that references the badge options is never rewritten.
	own := `#{pane_title} #{@ntm_agent_mail_label}`
	if err := DefaultClient.RunSilentContext(ctx, "set-option", "-w", "-t", w.ID, "pane-border-format", own); err != nil {
		t.Fatal(err)
	}
	change, err := DefaultClient.EnableWindowBadgeBorderContext(ctx, w.ID)
	if err != nil || change.Changed || change.Skipped == "" {
		t.Fatalf("enable over user-owned format = %+v, %v", change, err)
	}
	if got := windowOptionValue(t, w.ID, "pane-border-format", true); got != own {
		t.Fatalf("user-owned format rewritten to %q", got)
	}
}

// TestRealListWindowBadgeInfoLinkedAndOptOut: linked windows and the session
// opt-out are reported so the caller can skip them.
func TestRealListWindowBadgeInfoLinkedAndOptOut(t *testing.T) {
	session := createBadgeTestSession(t)
	other := createSecondBadgeTestSession(t)
	ctx := context.Background()
	if err := DefaultClient.RunSilentContext(ctx, "link-window", "-s", SessionOptionTarget(session)+"0", "-t", SessionOptionTarget(other)+"9"); err != nil {
		t.Fatalf("link-window: %v", err)
	}
	if err := DefaultClient.RunSilentContext(ctx, "set-option", "-t", SessionOptionTarget(session), SessionOptionAgentMailBadges, "off"); err != nil {
		t.Fatal(err)
	}
	windows, err := DefaultClient.ListWindowBadgeInfoContext(ctx, session)
	if err != nil || len(windows) != 1 {
		t.Fatalf("windows = %+v, %v", windows, err)
	}
	if !windows[0].Linked || !windows[0].SessionOptOut {
		t.Fatalf("window = %+v, want linked and opted out", windows[0])
	}
	// The other session did not opt out.
	others, err := DefaultClient.ListWindowBadgeInfoContext(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range others {
		if w.SessionOptOut {
			t.Fatalf("opt-out leaked into %s: %+v", other, w)
		}
	}
}

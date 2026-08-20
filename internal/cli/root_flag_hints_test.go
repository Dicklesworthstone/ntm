package cli

import (
	"testing"

	"github.com/spf13/pflag"
)

// curatedFlagHint backs both the robot-surface INVALID_FLAG hints and the
// cobra SetFlagErrorFunc did-you-mean path. Exact curated keys and their
// single-edit typos must hit the curated hint (the generic edit-distance
// suggester picks red herrings for these: --sesion's nearest flag on
// 'ntm send' is --json); everything else must fall through.
func TestCuratedFlagHint(t *testing.T) {
	tests := []struct {
		unknown string
		wantHit bool
	}{
		{"session", true},
		{"sesion", true},   // 1 edit from "session"
		{"sessions", true}, // 1 edit from "session"
		{"message", true},
		{"messge", true}, // 1 edit from "message"
		{"pane", true},
		{"timeout", true},
		{"project-dir", true},
		{"msg", true},
		{"pnae", false},          // 2 edits from "pane": falls through to the flag suggester
		{"robot-state", false},   // near-miss robot flag: flag suggester territory
		{"totally-novel", false}, //
		{"", false},
	}
	for _, tt := range tests {
		hint, ok := curatedFlagHint(tt.unknown)
		if ok != tt.wantHit {
			t.Errorf("curatedFlagHint(%q) hit = %v, want %v (hint %q)", tt.unknown, ok, tt.wantHit, hint)
		}
		if ok && hint == "" {
			t.Errorf("curatedFlagHint(%q) hit with empty hint", tt.unknown)
		}
	}
}

// flagErrorGuidance precedence: exact curated hint first; then a REAL flag
// one edit away (a command that has --session must answer --sesion with its
// own flag, not the curated redirection); then the fuzzy curated hint; then
// the general suggester.
func TestFlagErrorGuidancePrecedence(t *testing.T) {
	withSession := pflag.NewFlagSet("withSession", pflag.ContinueOnError)
	withSession.String("session", "", "")
	withSession.Bool("json", false, "")

	sendLike := pflag.NewFlagSet("sendLike", pflag.ContinueOnError)
	sendLike.Bool("json", false, "")
	sendLike.String("panes", "", "")

	// Exact curated key wins even when the command has a near flag.
	if hint, sugg := flagErrorGuidance("session", sendLike); hint == "" || sugg != "" {
		t.Errorf("exact curated: got hint=%q sugg=%q", hint, sugg)
	}
	// Typo of a real flag on this surface: suggest the real flag.
	if hint, sugg := flagErrorGuidance("sesion", withSession); hint != "" || sugg != "session" {
		t.Errorf("real-flag typo: got hint=%q sugg=%q, want suggestion 'session'", hint, sugg)
	}
	// Typo of a curated key with no near real flag: curated hint, not --json.
	if hint, sugg := flagErrorGuidance("sesion", sendLike); hint == "" || sugg != "" {
		t.Errorf("fuzzy curated: got hint=%q sugg=%q, want curated hint", hint, sugg)
	}
	// Plain near-miss of a real flag: general suggester.
	if hint, sugg := flagErrorGuidance("pane", sendLike); hint != flagMisGuessHints["pane"] || sugg != "" {
		t.Errorf("exact curated 'pane': got hint=%q sugg=%q", hint, sugg)
	}
	if hint, sugg := flagErrorGuidance("jsno", sendLike); hint != "" || sugg != "json" {
		t.Errorf("suggester: got hint=%q sugg=%q, want suggestion 'json'", hint, sugg)
	}
	// Nothing close: no guidance.
	if hint, sugg := flagErrorGuidance("zzzzzzzzzz", sendLike); hint != "" || sugg != "" {
		t.Errorf("no match: got hint=%q sugg=%q", hint, sugg)
	}
}

// The fuzzy match must be deterministic despite map iteration order.
func TestCuratedFlagHintDeterministic(t *testing.T) {
	first, ok := curatedFlagHint("sesion")
	if !ok {
		t.Fatal("expected curated hit for 'sesion'")
	}
	for i := 0; i < 20; i++ {
		if got, _ := curatedFlagHint("sesion"); got != first {
			t.Fatalf("curatedFlagHint non-deterministic: %q then %q", first, got)
		}
	}
}

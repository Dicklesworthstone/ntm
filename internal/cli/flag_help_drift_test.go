package cli

import (
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// TestFlagHelpDoesNotTeachDeprecatedSpellings guards the drift that kept a
// whole class of stale documentation alive.
//
// Cobra hides deprecated flags from `--help`, so a reader cannot discover that
// `--inspect-index` was renamed to `--index`. But several *live* flags kept
// showing the old spelling inside their own `Example:` text — `--robot-metrics`
// advertised `--metrics-period`, `--robot-beads-list` advertised
// `--beads-status`, and so on. The examples were the most authoritative-looking
// source available and they taught the deprecated form, which is how downstream
// docs and skills ended up pinned to names that no longer appear in help output.
//
// The rule: a flag's usage text must not advertise a spelling that
// MarkDeprecated has retired. Explaining that a canonical flag *replaces* older
// names is fine and is what the exemption below covers.
func TestFlagHelpDoesNotTeachDeprecatedSpellings(t *testing.T) {
	deprecated := map[string]string{}
	rootCmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Deprecated != "" {
			// f.Deprecated holds the "use --x instead" guidance.
			deprecated[f.Name] = f.Deprecated
		}
	})
	if len(deprecated) == 0 {
		t.Skip("no deprecated flags registered; nothing to guard")
	}

	// Flags whose whole purpose is to name the spellings they superseded.
	migrationNotes := map[string]bool{"session": true}

	var problems []string
	rootCmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Deprecated != "" || migrationNotes[f.Name] {
			return
		}
		for old, guidance := range deprecated {
			// Match the flag token, not a substring of a longer name.
			for _, form := range []string{"--" + old + " ", "--" + old + "=", "--" + old + ")", "--" + old + "."} {
				if strings.Contains(f.Usage+" ", form) {
					problems = append(problems,
						"--"+f.Name+" usage advertises deprecated --"+old+" ("+guidance+")")
					break
				}
			}
		}
	})

	if len(problems) > 0 {
		t.Errorf("%d flag description(s) teach a deprecated spelling:", len(problems))
		for _, p := range problems {
			t.Errorf("  %s", p)
		}
		t.Error("fix the usage text to show the canonical flag; help output is what downstream docs copy")
	}
}

// TestRobotStatusHelpDoesNotPromisePaneDetail pins the description against the
// payload it actually returns.
//
// The old text — "Get tmux sessions, panes, agent states" — described
// --robot-snapshot. --robot-status deliberately returns compact session headers
// with nested agent detail excluded (StatusSessionHeader), so the help sent
// readers looking for `.sessions[].agents[]` on the wrong surface.
func TestRobotStatusHelpDoesNotPromisePaneDetail(t *testing.T) {
	f := rootCmd.Flags().Lookup("robot-status")
	if f == nil {
		t.Fatal("--robot-status flag is not registered")
	}
	usage := f.Usage

	if !strings.Contains(usage, "--robot-snapshot") {
		t.Errorf("--robot-status usage should point at --robot-snapshot for per-pane detail; got: %s", usage)
	}
	for _, claim := range []string{"agent_count", "health"} {
		if !strings.Contains(usage, claim) {
			t.Errorf("--robot-status usage should name what it actually returns (%q); got: %s", claim, usage)
		}
	}
}

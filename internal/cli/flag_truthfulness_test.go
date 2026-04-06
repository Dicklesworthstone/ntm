package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// =============================================================================
// TestFlagTruthfulness — CLI flag usage description audit
//
// Walks all subcommands from the root command and checks that no flag has
// "not implemented" or "TODO" in its usage description. Flags with such
// descriptions mislead users and agents about available functionality.
//
// Bead: bd-1aae9.8.2
// =============================================================================

// flagTruthfulnessAllowlist lists flag names (across any command) that are
// known exceptions. Each entry MUST include a justification.
var flagTruthfulnessAllowlist = map[string]string{
	// No current exceptions. Add entries here if a flag's usage string
	// legitimately contains "TODO" or "not implemented" and cannot be rephrased.
}

func TestFlagTruthfulness(t *testing.T) {
	root := rootCmd

	type violation struct {
		command string
		flag    string
		usage   string
		reason  string
	}
	var violations []violation

	// walkCommand recursively visits cmd and all descendants.
	var walkCommand func(cmd *cobra.Command, path string)
	walkCommand = func(cmd *cobra.Command, path string) {
		// Check both local and inherited flags.
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			lower := strings.ToLower(f.Usage)
			switch {
			case strings.Contains(lower, "not implemented"):
				if _, ok := flagTruthfulnessAllowlist[f.Name]; ok {
					return
				}
				violations = append(violations, violation{
					command: path,
					flag:    f.Name,
					usage:   f.Usage,
					reason:  "contains 'not implemented'",
				})
			case strings.Contains(lower, "todo"):
				if _, ok := flagTruthfulnessAllowlist[f.Name]; ok {
					return
				}
				violations = append(violations, violation{
					command: path,
					flag:    f.Name,
					usage:   f.Usage,
					reason:  "contains 'TODO'",
				})
			}
		})

		for _, child := range cmd.Commands() {
			childPath := path + " " + child.Name()
			walkCommand(child, childPath)
		}
	}

	walkCommand(root, "ntm")

	for _, v := range violations {
		t.Run(v.command+"/--"+v.flag, func(t *testing.T) {
			t.Errorf("flag --%s on %q has misleading usage (%s): %q\n"+
				"  Fix the usage description or add to flagTruthfulnessAllowlist",
				v.flag, v.command, v.reason, v.usage)
		})
	}
}

// TestFlagTruthfulness_AllowlistDocumented ensures every allowlist entry has a justification.
func TestFlagTruthfulness_AllowlistDocumented(t *testing.T) {
	t.Parallel()

	for flag, justification := range flagTruthfulnessAllowlist {
		if strings.TrimSpace(justification) == "" {
			t.Errorf("flagTruthfulnessAllowlist[%q] has no justification", flag)
		}
	}
}

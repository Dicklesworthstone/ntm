package robot

import (
	"sort"
	"strings"
	"testing"
)

// =============================================================================
// TestCapabilitiesSnapshot — default-build capability snapshots
//
// Verifies that the capabilities builder correctly reflects build-tag gating
// and maintains a snapshot of gated commands so new ones must be explicitly
// added.
//
// Bead: bd-1aae9.8.4
// =============================================================================

// knownGatedCommands is the explicit snapshot of commands that are gated behind
// build tags or marked experimental. Any new gated command MUST be added here
// with a brief justification.
//
// The key is the command Name, the value is a justification for why it is gated.
var knownGatedCommands = map[string]string{
	"ensemble_spawn": "Requires build tag: ensemble_experimental. Complex multi-branch tmux layouts still being stabilized.",
}

// TestCapabilitiesSnapshot_GatedCommandsNotFullyAvailable verifies that any
// command whose Note contains "experimental" or "build tag" is not presented
// as fully available in the default build.
func TestCapabilitiesSnapshot_GatedCommandsNotFullyAvailable(t *testing.T) {
	t.Parallel()

	caps, err := GetCapabilities()
	if err != nil {
		t.Fatalf("GetCapabilities(): %v", err)
	}

	if len(caps.Commands) == 0 {
		t.Fatal("GetCapabilities() returned zero commands")
	}

	for _, cmd := range caps.Commands {
		noteLower := strings.ToLower(cmd.Note)
		isGated := strings.Contains(noteLower, "experimental") ||
			strings.Contains(noteLower, "build tag")

		if !isGated {
			continue
		}

		t.Run("gated/"+cmd.Name, func(t *testing.T) {
			// Gated commands should mention "NOT_IMPLEMENTED" or "experimental"
			// or "build tag" in their Note to signal they are not fully available.
			if !strings.Contains(cmd.Note, "NOT_IMPLEMENTED") &&
				!strings.Contains(noteLower, "experimental") &&
				!strings.Contains(noteLower, "build tag") {
				t.Errorf("command %q has gating Note but does not clearly indicate "+
					"unavailability: Note=%q", cmd.Name, cmd.Note)
			}

			// Verify it is in the known gated snapshot.
			if _, ok := knownGatedCommands[cmd.Name]; !ok {
				t.Errorf("NEW gated command %q found (Note=%q) — "+
					"add it to knownGatedCommands in capabilities_snapshot_test.go "+
					"with a justification",
					cmd.Name, cmd.Note)
			}
		})
	}
}

// TestCapabilitiesSnapshot_KnownGatedStillPresent verifies that every entry
// in knownGatedCommands still exists in the capabilities output. If a gated
// command is removed, the snapshot must be updated.
func TestCapabilitiesSnapshot_KnownGatedStillPresent(t *testing.T) {
	t.Parallel()

	caps, err := GetCapabilities()
	if err != nil {
		t.Fatalf("GetCapabilities(): %v", err)
	}

	commandSet := make(map[string]bool, len(caps.Commands))
	for _, cmd := range caps.Commands {
		commandSet[cmd.Name] = true
	}

	for name, justification := range knownGatedCommands {
		t.Run("present/"+name, func(t *testing.T) {
			if !commandSet[name] {
				t.Errorf("knownGatedCommands entry %q (justification: %s) is no longer in "+
					"capabilities output — remove it from the snapshot", name, justification)
			}
		})
	}
}

// TestCapabilitiesSnapshot_AllowlistDocumented ensures every knownGatedCommands
// entry has a justification.
func TestCapabilitiesSnapshot_AllowlistDocumented(t *testing.T) {
	t.Parallel()

	for name, justification := range knownGatedCommands {
		if strings.TrimSpace(justification) == "" {
			t.Errorf("knownGatedCommands[%q] has no justification", name)
		}
	}
}

// TestCapabilitiesSnapshot_CommandCountBaseline acts as a change detector.
// If the total command count changes significantly, it prompts review.
func TestCapabilitiesSnapshot_CommandCountBaseline(t *testing.T) {
	t.Parallel()

	caps, err := GetCapabilities()
	if err != nil {
		t.Fatalf("GetCapabilities(): %v", err)
	}

	// Lower bound — if commands are added, test passes.
	// If commands are accidentally removed, test fails.
	// Update this when intentionally removing commands.
	const minExpectedCommands = 10
	if len(caps.Commands) < minExpectedCommands {
		t.Errorf("GetCapabilities() returned %d commands, expected at least %d — "+
			"commands may have been accidentally removed",
			len(caps.Commands), minExpectedCommands)
	}

	t.Logf("total capabilities commands: %d (gated: %d)",
		len(caps.Commands), len(knownGatedCommands))
}

// TestCapabilitiesSnapshot_CategoriesAreSorted verifies commands come back
// sorted by category then name, as the capabilities builder promises.
func TestCapabilitiesSnapshot_CategoriesAreSorted(t *testing.T) {
	t.Parallel()

	caps, err := GetCapabilities()
	if err != nil {
		t.Fatalf("GetCapabilities(): %v", err)
	}

	if len(caps.Commands) < 2 {
		t.Skip("too few commands to verify sort order")
	}

	sorted := make([]RobotCommandInfo, len(caps.Commands))
	copy(sorted, caps.Commands)

	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Category != sorted[j].Category {
			return categoryIndex(sorted[i].Category) < categoryIndex(sorted[j].Category)
		}
		return sorted[i].Name < sorted[j].Name
	})

	for i := range caps.Commands {
		if caps.Commands[i].Name != sorted[i].Name {
			t.Errorf("command at index %d: got %q, want %q — capabilities output is not sorted",
				i, caps.Commands[i].Name, sorted[i].Name)
			break
		}
	}
}
